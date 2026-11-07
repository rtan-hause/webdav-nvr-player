package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Clip statuses.
const (
	StatusPending = "pending" // found, not probed yet
	StatusReady   = "ready"
	StatusFailed  = "failed" // gave up after repeated errors
	StatusDeleted = "deleted"
)

// Clip thumbnail states.
const (
	ThumbNone     = "none"     // clip not probed yet
	ThumbWaiting  = "waiting"  // waiting for the camera's snapshot until PairDeadlineMS
	ThumbSnapshot = "snapshot" // paired: make the thumbnail from the snapshot
	ThumbFrame    = "frame"    // no snapshot: grab a frame from the video
	ThumbDone     = "done"
	ThumbFailed   = "failed"
)

// Camera is one camera (name + vendor channel).
type Camera struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
}

// NewFile is a file found on the server, ready to insert.
type NewFile struct {
	CameraID int64
	Path     string
	TimeMS   int64 // clip start or snapshot time, Unix ms UTC
	Size     int64
	ETag     string
	ModMS    int64
}

// Clip is one video file.
type Clip struct {
	ID             int64
	CameraID       int64
	Path           string
	StartMS        int64
	EndMS          int64 // 0 until probed
	Codec          string
	Width          int
	Height         int
	Size           int64
	ETag           string
	ModMS          int64
	Status         string
	Error          string
	Attempts       int
	NextAttemptMS  int64
	ThumbState     string
	ThumbKind      string
	ThumbPath      string
	SnapshotID     int64 // 0 if none
	PairDeadlineMS int64
}

// Snapshot is one still image uploaded by a camera.
type Snapshot struct {
	ID       int64
	CameraID int64
	Path     string
	TSMS     int64
	Size     int64
	ModMS    int64
	Status   string
	ClipID   int64 // 0 if unpaired
}

const clipCols = `id, camera_id, path, start_ms, end_ms, codec, width, height, size, etag, mod_ms, status, error,
	attempts, next_attempt_ms, thumb_state, thumb_kind, thumb_path, snapshot_id, pair_deadline_ms`

type scanner interface{ Scan(...any) error }

func scanClip(r scanner) (Clip, error) {
	var c Clip
	var snap sql.NullInt64
	err := r.Scan(&c.ID, &c.CameraID, &c.Path, &c.StartMS, &c.EndMS, &c.Codec, &c.Width, &c.Height, &c.Size, &c.ETag,
		&c.ModMS, &c.Status, &c.Error, &c.Attempts, &c.NextAttemptMS, &c.ThumbState, &c.ThumbKind, &c.ThumbPath, &snap, &c.PairDeadlineMS)
	c.SnapshotID = snap.Int64
	return c, err
}

const snapCols = `id, camera_id, path, ts_ms, size, mod_ms, status, clip_id`

func scanSnapshot(r scanner) (Snapshot, error) {
	var s Snapshot
	var clip sql.NullInt64
	err := r.Scan(&s.ID, &s.CameraID, &s.Path, &s.TSMS, &s.Size, &s.ModMS, &s.Status, &clip)
	s.ClipID = clip.Int64
	return s, err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func collect[T any](ctx context.Context, q querier, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- cameras ----

// EnsureCamera returns the camera's id, creating it on first sight.
func (s *Store) EnsureCamera(ctx context.Context, name, channel string) (int64, error) {
	if _, err := s.db.ExecContext(ctx, s.q(`INSERT INTO cameras (name, channel) VALUES (?, ?) ON CONFLICT (name, channel) DO NOTHING`), name, channel); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id FROM cameras WHERE name = ? AND channel = ?`), name, channel).Scan(&id)
	return id, err
}

// Cameras lists cameras by name.
func (s *Store) Cameras(ctx context.Context) ([]Camera, error) {
	return collect(ctx, s.db, func(r scanner) (Camera, error) {
		var c Camera
		return c, r.Scan(&c.ID, &c.Name, &c.Channel)
	}, `SELECT id, name, channel FROM cameras ORDER BY name, channel`)
}

// ---- inserting files ----

// AddClips inserts clips not seen before, and revives ones previously marked
// deleted (a file re-uploaded at the same path). It returns how many changed.
func (s *Store) AddClips(ctx context.Context, files []NewFile) (int, error) {
	return s.addFiles(ctx, files, s.q(`INSERT INTO clips (camera_id, path, start_ms, size, etag, mod_ms) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET status = 'pending', camera_id = excluded.camera_id, start_ms = excluded.start_ms,
			size = excluded.size, etag = excluded.etag, mod_ms = excluded.mod_ms, end_ms = 0, error = '', attempts = 0,
			next_attempt_ms = 0, thumb_state = 'none', thumb_kind = '', thumb_path = '', snapshot_id = NULL, pair_deadline_ms = 0
		WHERE clips.status = 'deleted'`))
}

// AddSnapshots is AddClips for snapshots.
func (s *Store) AddSnapshots(ctx context.Context, files []NewFile) (int, error) {
	return s.addFiles(ctx, files, s.q(`INSERT INTO snapshots (camera_id, path, ts_ms, size, etag, mod_ms) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET status = 'pending', camera_id = excluded.camera_id, ts_ms = excluded.ts_ms,
			size = excluded.size, etag = excluded.etag, mod_ms = excluded.mod_ms, clip_id = NULL
		WHERE snapshots.status = 'deleted'`))
}

func (s *Store) addFiles(ctx context.Context, files []NewFile, query string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, f := range files {
		res, err := stmt.ExecContext(ctx, f.CameraID, f.Path, f.TimeMS, f.Size, f.ETag, f.ModMS)
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, tx.Commit()
}

// ---- work queues ----

// ClipsToProbe returns pending clips that are due, newest first.
func (s *Store) ClipsToProbe(ctx context.Context, nowMS int64, limit int) ([]Clip, error) {
	return collect(ctx, s.db, scanClip, s.q(`SELECT `+clipCols+` FROM clips
		WHERE status = 'pending' AND next_attempt_ms <= ? ORDER BY start_ms DESC LIMIT ?`), nowMS, limit)
}

// SnapshotsToPair returns pending snapshots, newest first.
func (s *Store) SnapshotsToPair(ctx context.Context, limit int) ([]Snapshot, error) {
	return collect(ctx, s.db, scanSnapshot, s.q(`SELECT `+snapCols+` FROM snapshots
		WHERE status = 'pending' ORDER BY ts_ms DESC LIMIT ?`), limit)
}

// ClipsNeedingThumb returns ready clips whose thumbnail can be made now, newest first.
func (s *Store) ClipsNeedingThumb(ctx context.Context, nowMS int64, limit int) ([]Clip, error) {
	return collect(ctx, s.db, scanClip, s.q(`SELECT `+clipCols+` FROM clips
		WHERE status = 'ready' AND next_attempt_ms <= ?
		  AND (thumb_state IN ('snapshot', 'frame') OR (thumb_state = 'waiting' AND pair_deadline_ms <= ?))
		ORDER BY start_ms DESC LIMIT ?`), nowMS, nowMS, limit)
}

// NextWakeMS is the earliest future time any waiting work becomes due (0 if none).
func (s *Store) NextWakeMS(ctx context.Context, nowMS int64) (int64, error) {
	var a, b sql.NullInt64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT
		(SELECT MIN(pair_deadline_ms) FROM clips WHERE status = 'ready' AND thumb_state = 'waiting' AND pair_deadline_ms > ?),
		(SELECT MIN(next_attempt_ms) FROM clips WHERE status IN ('pending', 'ready') AND thumb_state NOT IN ('done', 'failed') AND next_attempt_ms > ?)`),
		nowMS, nowMS).Scan(&a, &b)
	if err != nil {
		return 0, err
	}
	switch {
	case a.Valid && b.Valid:
		return min(a.Int64, b.Int64), nil
	case a.Valid:
		return a.Int64, nil
	default:
		return b.Int64, nil
	}
}

// Clip loads one clip.
func (s *Store) Clip(ctx context.Context, id int64) (Clip, error) {
	c, err := scanClip(s.db.QueryRowContext(ctx, s.q(`SELECT `+clipCols+` FROM clips WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Snapshot loads one snapshot.
func (s *Store) Snapshot(ctx context.Context, id int64) (Snapshot, error) {
	sn, err := scanSnapshot(s.db.QueryRowContext(ctx, s.q(`SELECT `+snapCols+` FROM snapshots WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return sn, ErrNotFound
	}
	return sn, err
}

// SetClipProbed records the probe result and marks the clip ready.
func (s *Store) SetClipProbed(ctx context.Context, id, endMS int64, codec string, width, height int) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE clips SET status = 'ready', end_ms = ?, codec = ?, width = ?, height = ?,
		error = '', attempts = 0, next_attempt_ms = 0 WHERE id = ?`), endMS, codec, width, height, id)
	return err
}

// SetClipFailed records a probe error. A permanent failure stops retries.
func (s *Store) SetClipFailed(ctx context.Context, id int64, msg string, nextAttemptMS int64, permanent bool) error {
	status := StatusPending
	if permanent {
		status = StatusFailed
	}
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE clips SET status = ?, error = ?, attempts = attempts + 1, next_attempt_ms = ?
		WHERE id = ? AND status = 'pending'`), status, msg, nextAttemptMS, id)
	return err
}

// ---- pairing ----

// PairClip links a ready clip with the closest unpaired snapshot of the same
// camera taken between beforeMS ahead of its start and its end. Without one,
// the clip waits until deadlineMS for a snapshot to arrive, or (when the
// deadline has passed) falls back to a video frame. Returns the snapshot id, or 0.
func (s *Store) PairClip(ctx context.Context, clipID, beforeMS, deadlineMS, nowMS int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	c, err := scanClip(tx.QueryRowContext(ctx, s.q(`SELECT `+clipCols+` FROM clips WHERE id = ?`), clipID))
	if err != nil {
		return 0, err
	}
	if c.SnapshotID != 0 || c.Status != StatusReady {
		return c.SnapshotID, tx.Commit()
	}
	var snapID int64
	err = tx.QueryRowContext(ctx, s.q(`SELECT id FROM snapshots
		WHERE camera_id = ? AND status = 'ready' AND clip_id IS NULL AND ts_ms >= ? AND ts_ms <= ?
		ORDER BY ABS(ts_ms - ?), id LIMIT 1`), c.CameraID, c.StartMS-beforeMS, c.EndMS, c.StartMS).Scan(&snapID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		state := ThumbWaiting
		if deadlineMS <= nowMS {
			state = ThumbFrame
		}
		if c.ThumbState == ThumbDone && c.ThumbKind == "frame" {
			return 0, tx.Commit() // already has a frame thumbnail; nothing better arrived
		}
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE clips SET thumb_state = ?, pair_deadline_ms = ? WHERE id = ?`), state, deadlineMS, clipID); err != nil {
			return 0, err
		}
		return 0, tx.Commit()
	case err != nil:
		return 0, err
	}
	if err := link(ctx, s, tx, clipID, snapID); err != nil {
		return 0, err
	}
	return snapID, tx.Commit()
}

// PairSnapshot marks a pending snapshot ready and links it with the closest
// ready clip of the same camera that it falls inside (allowing beforeMS ahead
// of the clip's start) and that has no snapshot yet. Returns the clip id, or 0.
func (s *Store) PairSnapshot(ctx context.Context, snapID, beforeMS int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	sn, err := scanSnapshot(tx.QueryRowContext(ctx, s.q(`SELECT `+snapCols+` FROM snapshots WHERE id = ?`), snapID))
	if err != nil {
		return 0, err
	}
	if sn.Status == StatusDeleted {
		return 0, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE snapshots SET status = 'ready' WHERE id = ?`), snapID); err != nil {
		return 0, err
	}
	if sn.ClipID != 0 {
		return sn.ClipID, tx.Commit()
	}
	var clipID int64
	err = tx.QueryRowContext(ctx, s.q(`SELECT id FROM clips
		WHERE camera_id = ? AND status = 'ready' AND snapshot_id IS NULL AND start_ms - ? <= ? AND end_ms >= ?
		ORDER BY ABS(start_ms - ?), id LIMIT 1`), sn.CameraID, beforeMS, sn.TSMS, sn.TSMS, sn.TSMS).Scan(&clipID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, tx.Commit()
	}
	if err != nil {
		return 0, err
	}
	if err := link(ctx, s, tx, clipID, snapID); err != nil {
		return 0, err
	}
	return clipID, tx.Commit()
}

func link(ctx context.Context, s *Store, tx *sql.Tx, clipID, snapID int64) error {
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE snapshots SET clip_id = ? WHERE id = ?`), clipID, snapID); err != nil {
		return err
	}
	// A clip that already has a frame thumbnail is upgraded to the camera's snapshot.
	_, err := tx.ExecContext(ctx, s.q(`UPDATE clips SET snapshot_id = ?, thumb_state = 'snapshot', attempts = 0, next_attempt_ms = 0 WHERE id = ?`), snapID, clipID)
	return err
}

// UnpairForFrame drops a clip's snapshot (it vanished or was unreadable) and falls back to a frame.
func (s *Store) UnpairForFrame(ctx context.Context, clipID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE snapshots SET clip_id = NULL WHERE clip_id = ?`), clipID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE clips SET snapshot_id = NULL, thumb_state = 'frame' WHERE id = ?`), clipID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- thumbnails ----

// SetThumb records a finished thumbnail.
func (s *Store) SetThumb(ctx context.Context, clipID int64, path, kind string) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE clips SET thumb_state = 'done', thumb_kind = ?, thumb_path = ?, error = '',
		attempts = 0, next_attempt_ms = 0 WHERE id = ?`), kind, path, clipID)
	return err
}

// SetThumbFailed records a thumbnail error; permanent failures stop retries.
func (s *Store) SetThumbFailed(ctx context.Context, clipID int64, msg string, nextAttemptMS int64, permanent bool) error {
	q := `UPDATE clips SET error = ?, attempts = attempts + 1, next_attempt_ms = ? WHERE id = ?`
	if permanent {
		q = `UPDATE clips SET thumb_state = 'failed', error = ?, attempts = attempts + 1, next_attempt_ms = ? WHERE id = ?`
	}
	_, err := s.db.ExecContext(ctx, s.q(q), msg, nextAttemptMS, clipID)
	return err
}

// ---- reconciliation with the server ----

// LivePathsIn returns indexed (not deleted) clip and snapshot paths directly inside dir ("" for the root).
func (s *Store) LivePathsIn(ctx context.Context, dir string) ([]string, error) {
	prefix := ""
	if dir = strings.Trim(dir, "/"); dir != "" {
		prefix = dir + "/"
	}
	// Range scan on the path index in byte order (SQLite BINARY, Postgres COLLATE "C"):
	// [prefix, prefix + the highest code point). Direct children are filtered below.
	hi := prefix + "\U0010FFFF"
	paths, err := collect(ctx, s.db, func(r scanner) (string, error) {
		var p string
		return p, r.Scan(&p)
	}, s.q(`SELECT path FROM clips WHERE path >= ? AND path < ? AND status <> 'deleted'
		UNION ALL SELECT path FROM snapshots WHERE path >= ? AND path < ? AND status <> 'deleted'`), prefix, hi, prefix, hi)
	if err != nil {
		return nil, err
	}
	out := paths[:0]
	for _, p := range paths {
		if !strings.Contains(p[len(prefix):], "/") {
			out = append(out, p)
		}
	}
	return out, nil
}

// AllLivePaths returns every indexed (not deleted) path.
func (s *Store) AllLivePaths(ctx context.Context) ([]string, error) {
	return collect(ctx, s.db, func(r scanner) (string, error) {
		var p string
		return p, r.Scan(&p)
	}, `SELECT path FROM clips WHERE status <> 'deleted' UNION ALL SELECT path FROM snapshots WHERE status <> 'deleted'`)
}

// MarkDeleted marks clips and snapshots at these paths as deleted and returns
// the thumbnail files that are no longer needed.
func (s *Store) MarkDeleted(ctx context.Context, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var thumbs []string
	for _, p := range paths {
		var thumb string
		err := tx.QueryRowContext(ctx, s.q(`SELECT thumb_path FROM clips WHERE path = ? AND status <> 'deleted'`), p).Scan(&thumb)
		if err == nil {
			if thumb != "" {
				thumbs = append(thumbs, thumb)
			}
			if _, err := tx.ExecContext(ctx, s.q(`UPDATE snapshots SET clip_id = NULL WHERE clip_id = (SELECT id FROM clips WHERE path = ?)`), p); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, s.q(`UPDATE clips SET status = 'deleted', thumb_path = '', thumb_state = 'none', snapshot_id = NULL WHERE path = ?`), p); err != nil {
				return nil, err
			}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// A deleted snapshot keeps any thumbnail already made from it.
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE snapshots SET status = 'deleted' WHERE path = ?`), p); err != nil {
			return nil, err
		}
	}
	return thumbs, tx.Commit()
}

// ---- reads for the API ----

// MaxClipMS bounds how long a clip can be, so range queries can use the start index.
const MaxClipMS = 60 * 60 * 1000

// ClipsBetween returns ready clips overlapping [fromMS, toMS), oldest first.
// cameraID 0 means all cameras.
func (s *Store) ClipsBetween(ctx context.Context, fromMS, toMS, cameraID int64) ([]Clip, error) {
	return collect(ctx, s.db, scanClip, s.q(`SELECT `+clipCols+` FROM clips
		WHERE status = 'ready' AND start_ms >= ? AND start_ms < ? AND end_ms > ? AND (? = 0 OR camera_id = ?)
		ORDER BY start_ms, id`), fromMS-MaxClipMS, toMS, fromMS, cameraID, cameraID)
}

// LooseSnapshotsBetween returns ready snapshots in [fromMS, toMS) not paired with any clip.
func (s *Store) LooseSnapshotsBetween(ctx context.Context, fromMS, toMS, cameraID int64) ([]Snapshot, error) {
	return collect(ctx, s.db, scanSnapshot, s.q(`SELECT `+snapCols+` FROM snapshots
		WHERE status = 'ready' AND clip_id IS NULL AND ts_ms >= ? AND ts_ms < ? AND (? = 0 OR camera_id = ?)
		ORDER BY ts_ms, id`), fromMS, toMS, cameraID, cameraID)
}

// HasClipsBetween reports whether any ready clip starts in [fromMS, toMS).
func (s *Store) HasClipsBetween(ctx context.Context, fromMS, toMS int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT 1 FROM clips WHERE status = 'ready' AND start_ms >= ? AND start_ms < ? LIMIT 1`), fromMS, toMS).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Stats summarises the index for the status page.
type Stats struct {
	Clips          map[string]int `json:"clips"`     // by status
	Thumbs         map[string]int `json:"thumbs"`    // ready clips by thumbnail state
	Snapshots      map[string]int `json:"snapshots"` // by status
	PairedSnaps    int            `json:"paired_snapshots"`
	Cameras        int            `json:"cameras"`
	LatestClipMS   int64          `json:"latest_clip_ms"`
	EarliestClipMS int64          `json:"earliest_clip_ms"`
}

// Stats counts rows by state.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{Clips: map[string]int{}, Thumbs: map[string]int{}, Snapshots: map[string]int{}}
	type kv struct {
		k string
		n int
	}
	scanKV := func(r scanner) (kv, error) {
		var v kv
		return v, r.Scan(&v.k, &v.n)
	}
	for _, q := range []struct {
		m     map[string]int
		query string
	}{
		{st.Clips, `SELECT status, COUNT(*) FROM clips GROUP BY status`},
		{st.Thumbs, `SELECT thumb_state, COUNT(*) FROM clips WHERE status = 'ready' GROUP BY thumb_state`},
		{st.Snapshots, `SELECT status, COUNT(*) FROM snapshots GROUP BY status`},
	} {
		rows, err := collect(ctx, s.db, scanKV, q.query)
		if err != nil {
			return st, err
		}
		for _, r := range rows {
			q.m[r.k] = r.n
		}
	}
	var lo, hi sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM cameras),
		(SELECT COUNT(*) FROM snapshots WHERE clip_id IS NOT NULL),
		(SELECT MIN(start_ms) FROM clips WHERE status = 'ready'),
		(SELECT MAX(start_ms) FROM clips WHERE status = 'ready')`).Scan(&st.Cameras, &st.PairedSnaps, &lo, &hi)
	st.EarliestClipMS, st.LatestClipMS = lo.Int64, hi.Int64
	return st, err
}
