package store

import (
	"context"
	"os"
	"slices"
	"testing"
)

// Every test runs against SQLite, and against Postgres too when
// NVR_TEST_POSTGRES_URL points at a database the tests may wipe, e.g.
//
//	docker run -d --rm -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:17-alpine
//	NVR_TEST_POSTGRES_URL=postgres://postgres:test@localhost:55432/postgres?sslmode=disable go test ./internal/store
func backends(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Run("sqlite", func(t *testing.T) {
		s, err := Open(context.Background(), "", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		url := os.Getenv("NVR_TEST_POSTGRES_URL")
		if url == "" {
			t.Skip("set NVR_TEST_POSTGRES_URL to run against Postgres")
		}
		s, err := Open(context.Background(), url, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, tbl := range []string{"sessions", "users", "settings", "snapshots", "clips", "cameras", "schema_migrations"} {
			if _, err := s.db.Exec(`DROP TABLE IF EXISTS ` + tbl + ` CASCADE`); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
		if s, err = Open(context.Background(), url, ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

var ctx = context.Background()

const sec = int64(1000)

// t0 is 2026-11-06 21:58:52 PST in Unix ms.
const t0 = int64(1794031132000)

func mustCam(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.EnsureCamera(ctx, name, "00")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func addClip(t *testing.T, s *Store, cam int64, path string, startMS int64) Clip {
	t.Helper()
	if _, err := s.AddClips(ctx, []NewFile{{CameraID: cam, Path: path, TimeMS: startMS, Size: 1}}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.collectByPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func (s *Store) collectByPath(path string) (Clip, error) {
	return scanClip(s.db.QueryRowContext(ctx, s.q(`SELECT `+clipCols+` FROM clips WHERE path = ?`), path))
}

func addSnap(t *testing.T, s *Store, cam int64, path string, tsMS int64) Snapshot {
	t.Helper()
	if _, err := s.AddSnapshots(ctx, []NewFile{{CameraID: cam, Path: path, TimeMS: tsMS, Size: 1}}); err != nil {
		t.Fatal(err)
	}
	sn, err := scanSnapshot(s.db.QueryRowContext(ctx, s.q(`SELECT `+snapCols+` FROM snapshots WHERE path = ?`), path))
	if err != nil {
		t.Fatal(err)
	}
	return sn
}

func probe(t *testing.T, s *Store, c Clip, durMS int64) {
	t.Helper()
	if err := s.SetClipProbed(ctx, c.ID, c.StartMS+durMS, "hvc1", 3840, 2160); err != nil {
		t.Fatal(err)
	}
}

func TestCamerasAreDeduplicated(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		a := mustCam(t, s, "Living Room")
		b := mustCam(t, s, "Living Room")
		c := mustCam(t, s, "Garage")
		if a != b || a == c {
			t.Fatalf("ids: %d %d %d", a, b, c)
		}
		cams, err := s.Cameras(ctx)
		if err != nil || len(cams) != 2 || cams[0].Name != "Garage" {
			t.Fatalf("cameras = %+v %v", cams, err)
		}
	})
}

func TestAddIsIdempotentAndRevivesDeleted(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		cam := mustCam(t, s, "Living Room")
		f := []NewFile{{CameraID: cam, Path: "2026/11/06/a.mp4", TimeMS: t0}}
		if n, err := s.AddClips(ctx, f); err != nil || n != 1 {
			t.Fatalf("first add: %d %v", n, err)
		}
		if n, err := s.AddClips(ctx, f); err != nil || n != 0 {
			t.Fatalf("second add should be a no-op: %d %v", n, err)
		}
		c, _ := s.collectByPath("2026/11/06/a.mp4")
		probe(t, s, c, 28*sec)
		if _, err := s.MarkDeleted(ctx, []string{"2026/11/06/a.mp4"}); err != nil {
			t.Fatal(err)
		}
		if n, err := s.AddClips(ctx, f); err != nil || n != 1 {
			t.Fatalf("re-upload should revive: %d %v", n, err)
		}
		c, _ = s.collectByPath("2026/11/06/a.mp4")
		if c.Status != StatusPending || c.EndMS != 0 || c.ThumbState != ThumbNone {
			t.Fatalf("revived clip not reset: %+v", c)
		}
	})
}

func TestProbeQueueAndFailures(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		cam := mustCam(t, s, "Garage")
		older := addClip(t, s, cam, "old.mp4", t0-60*sec)
		newer := addClip(t, s, cam, "new.mp4", t0)

		due, err := s.ClipsToProbe(ctx, t0, 10)
		if err != nil || len(due) != 2 || due[0].ID != newer.ID {
			t.Fatalf("newest first: %+v %v", due, err)
		}
		if err := s.SetClipFailed(ctx, older.ID, "timeout", t0+30*sec, false); err != nil {
			t.Fatal(err)
		}
		due, _ = s.ClipsToProbe(ctx, t0, 10)
		if len(due) != 1 || due[0].ID != newer.ID {
			t.Fatalf("failed clip must wait for its retry time: %+v", due)
		}
		due, _ = s.ClipsToProbe(ctx, t0+30*sec, 10)
		if len(due) != 2 {
			t.Fatalf("retry due: %+v", due)
		}
		if err := s.SetClipFailed(ctx, older.ID, "not an MP4", 0, true); err != nil {
			t.Fatal(err)
		}
		c, _ := s.Clip(ctx, older.ID)
		if c.Status != StatusFailed || c.Attempts != 2 || c.Error != "not an MP4" {
			t.Fatalf("permanent failure: %+v", c)
		}
	})
}

// Pairing scenarios from the real archive: same second, +1 s, +4 s, a
// neighbouring clip's snapshot, snapshot arriving after the clip, and none.
func TestPairing(t *testing.T) {
	const before = 2 * sec
	backends(t, func(t *testing.T, s *Store) {
		cam := mustCam(t, s, "Living Room")
		other := mustCam(t, s, "Backyard")

		// Clip A 0–28 s, clip B 28–56 s (back to back).
		a := addClip(t, s, cam, "A.mp4", t0)
		b := addClip(t, s, cam, "B.mp4", t0+28*sec)
		probe(t, s, a, 28*sec)
		probe(t, s, b, 28*sec)

		// Snapshots arrive first (as they usually do): +1 s for A, +4 s for B,
		// and one from another camera at A's exact time.
		sa := addSnap(t, s, cam, "A.jpg", t0+1*sec)
		sb := addSnap(t, s, cam, "B.jpg", t0+32*sec)
		sx := addSnap(t, s, other, "X.jpg", t0)
		for _, sn := range []Snapshot{sa, sb, sx} {
			if _, err := s.PairSnapshot(ctx, sn.ID, before); err != nil {
				t.Fatal(err)
			}
		}
		ca, _ := s.Clip(ctx, a.ID)
		cb, _ := s.Clip(ctx, b.ID)
		if ca.SnapshotID != sa.ID || ca.ThumbState != ThumbSnapshot {
			t.Fatalf("A should pair with its +1 s snapshot: %+v", ca)
		}
		if cb.SnapshotID != sb.ID {
			t.Fatalf("B should pair with its +4 s snapshot, not A's: %+v", cb)
		}
		x, _ := s.Snapshot(ctx, sx.ID)
		if x.ClipID != 0 || x.Status != StatusReady {
			t.Fatalf("other camera's snapshot must stay loose: %+v", x)
		}

		// Clip C has no snapshot yet: it waits until the deadline...
		c := addClip(t, s, cam, "C.mp4", t0+600*sec)
		probe(t, s, c, 28*sec)
		if id, err := s.PairClip(ctx, c.ID, before, t0+700*sec, t0+600*sec); err != nil || id != 0 {
			t.Fatalf("no snapshot yet: %d %v", id, err)
		}
		cc, _ := s.Clip(ctx, c.ID)
		if cc.ThumbState != ThumbWaiting {
			t.Fatalf("should wait: %+v", cc)
		}
		if due, _ := s.ClipsNeedingThumb(ctx, t0+650*sec, 10); containsClip(due, c.ID) {
			t.Fatal("waiting clip must not be due before its deadline")
		}
		// ...and a snapshot taken 1 s before it arrives late and claims it.
		late := addSnap(t, s, cam, "C.jpg", t0+599*sec)
		if id, err := s.PairSnapshot(ctx, late.ID, before); err != nil || id != c.ID {
			t.Fatalf("late snapshot should pair with C: %d %v", id, err)
		}

		// Clip D never gets a snapshot: after the deadline it falls back to a frame.
		d := addClip(t, s, cam, "D.mp4", t0+1200*sec)
		probe(t, s, d, 28*sec)
		if _, err := s.PairClip(ctx, d.ID, before, t0+1260*sec, t0+1200*sec); err != nil {
			t.Fatal(err)
		}
		if due, _ := s.ClipsNeedingThumb(ctx, t0+1260*sec, 10); !containsClip(due, d.ID) {
			t.Fatal("clip past its deadline should be due")
		}
		if _, err := s.PairClip(ctx, d.ID, before, t0+1260*sec, t0+1260*sec); err != nil {
			t.Fatal(err)
		}
		cd, _ := s.Clip(ctx, d.ID)
		if cd.ThumbState != ThumbFrame {
			t.Fatalf("should fall back to a frame: %+v", cd)
		}

		// A snapshot 3 s before a clip is outside the 2 s window.
		e := addClip(t, s, cam, "E.mp4", t0+1800*sec)
		probe(t, s, e, 28*sec)
		early := addSnap(t, s, cam, "early.jpg", t0+1797*sec)
		if id, _ := s.PairSnapshot(ctx, early.ID, before); id != 0 {
			t.Fatalf("snapshot 3 s early must not pair, got clip %d", id)
		}
	})
}

func containsClip(cs []Clip, id int64) bool {
	return slices.ContainsFunc(cs, func(c Clip) bool { return c.ID == id })
}

func TestThumbLifecycleAndDeletion(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		cam := mustCam(t, s, "Front of House")
		c := addClip(t, s, cam, "2026/11/06/F.mp4", t0)
		probe(t, s, c, 28*sec)
		sn := addSnap(t, s, cam, "2026/11/06/F.jpg", t0)
		if _, err := s.PairSnapshot(ctx, sn.ID, 2*sec); err != nil {
			t.Fatal(err)
		}
		if err := s.SetThumb(ctx, c.ID, "thumbs/0/1.jpg", ThumbKindSnapshot); err != nil {
			t.Fatal(err)
		}
		if due, _ := s.ClipsNeedingThumb(ctx, t0, 10); len(due) != 0 {
			t.Fatalf("done clip still due: %+v", due)
		}

		live, err := s.LivePathsIn(ctx, "2026/11/06")
		if err != nil || len(live) != 2 {
			t.Fatalf("live paths: %v %v", live, err)
		}
		if root, _ := s.LivePathsIn(ctx, ""); len(root) != 0 {
			t.Fatalf("root has no direct files: %v", root)
		}
		thumbs, err := s.MarkDeleted(ctx, []string{"2026/11/06/F.mp4"})
		if err != nil || len(thumbs) != 1 || thumbs[0] != "thumbs/0/1.jpg" {
			t.Fatalf("deleted thumbs: %v %v", thumbs, err)
		}
		got, _ := s.Snapshot(ctx, sn.ID)
		if got.ClipID != 0 {
			t.Fatal("deleting a clip must free its snapshot")
		}
		all, _ := s.AllLivePaths(ctx)
		if len(all) != 1 || all[0] != "2026/11/06/F.jpg" {
			t.Fatalf("all live: %v", all)
		}
	})
}

func TestRangeQueriesAndStats(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		front := mustCam(t, s, "Front of House")
		back := mustCam(t, s, "Backyard")
		// Spans the window start: must be included.
		c1 := addClip(t, s, front, "c1.mp4", t0-10*sec)
		c2 := addClip(t, s, back, "c2.mp4", t0+60*sec)
		c3 := addClip(t, s, front, "c3.mp4", t0+3600*sec) // outside
		pending := addClip(t, s, front, "c4.mp4", t0+5*sec)
		_ = pending
		for _, c := range []Clip{c1, c2, c3} {
			probe(t, s, c, 28*sec)
		}
		loose := addSnap(t, s, back, "loose.jpg", t0+120*sec)
		if _, err := s.PairSnapshot(ctx, loose.ID, 2*sec); err != nil {
			t.Fatal(err)
		}

		got, err := s.ClipsBetween(ctx, t0, t0+600*sec, 0)
		if err != nil || len(got) != 2 || got[0].ID != c1.ID || got[1].ID != c2.ID {
			t.Fatalf("all cameras: %+v %v", got, err)
		}
		got, _ = s.ClipsBetween(ctx, t0, t0+600*sec, back)
		if len(got) != 1 || got[0].ID != c2.ID {
			t.Fatalf("one camera: %+v", got)
		}
		snaps, _ := s.LooseSnapshotsBetween(ctx, t0, t0+600*sec, 0)
		if len(snaps) != 1 || snaps[0].ID != loose.ID {
			t.Fatalf("loose snapshots: %+v", snaps)
		}
		if ok, _ := s.HasClipsBetween(ctx, t0+60*sec, t0+61*sec); !ok {
			t.Fatal("HasClipsBetween missed c2")
		}
		if ok, _ := s.HasClipsBetween(ctx, t0+7200*sec, t0+7300*sec); ok {
			t.Fatal("HasClipsBetween found nothing there")
		}

		st, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.Clips[StatusReady] != 3 || st.Clips[StatusPending] != 1 || st.Snapshots[StatusReady] != 1 || st.Cameras != 2 {
			t.Fatalf("stats: %+v", st)
		}
		if st.EarliestClipMS != c1.StartMS || st.LatestClipMS != c3.StartMS {
			t.Fatalf("stats range: %+v", st)
		}
	})
}

func TestMigrationsAreReapplicable(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(ctx, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, "", dir) // second open must not re-run 0001
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
