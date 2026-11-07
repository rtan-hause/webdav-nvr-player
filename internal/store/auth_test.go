package store

import (
	"errors"
	"testing"
)

func TestUsersSessionsSettings(t *testing.T) {
	backends(t, func(t *testing.T, s *Store) {
		if n, err := s.CountUsers(ctx); err != nil || n != 0 {
			t.Fatalf("CountUsers = %d, %v", n, err)
		}
		if err := s.CreateUser(ctx, "admin", "hash1", t0); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateUser(ctx, "admin", "hash2", t0); err == nil {
			t.Fatal("duplicate username must fail")
		}
		u, err := s.UserByName(ctx, "admin")
		if err != nil || u.PasswordHash != "hash1" {
			t.Fatalf("UserByName = %+v, %v", u, err)
		}
		if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing user: %v", err)
		}
		if err := s.SetPassword(ctx, "admin", "hash3"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPassword(ctx, "nobody", "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetPassword for a missing user: %v", err)
		}

		for _, h := range []string{"a", "b", "c"} {
			if err := s.CreateSession(ctx, Session{TokenHash: h, Username: "admin", CreatedMS: t0, SeenMS: t0, ExpiresMS: t0 + 10*sec}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.SessionByHash(ctx, "a", t0+5*sec); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionByHash(ctx, "a", t0+10*sec); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired session must not be found: %v", err)
		}
		if err := s.TouchSession(ctx, "a", t0+9*sec, t0+100*sec); err != nil {
			t.Fatal(err)
		}
		if x, err := s.SessionByHash(ctx, "a", t0+50*sec); err != nil || x.SeenMS != t0+9*sec {
			t.Fatalf("touched session = %+v, %v", x, err)
		}
		if n, err := s.DeleteOtherSessions(ctx, "admin", "a"); err != nil || n != 2 {
			t.Fatalf("DeleteOtherSessions = %d, %v", n, err)
		}
		if err := s.DeleteSession(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SessionByHash(ctx, "a", t0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted session: %v", err)
		}
		s.CreateSession(ctx, Session{TokenHash: "old", Username: "admin", ExpiresMS: t0})
		if err := s.PruneSessions(ctx, t0+1); err != nil {
			t.Fatal(err)
		}

		if _, ok, err := s.Setting(ctx, "session_days"); ok || err != nil {
			t.Fatalf("unset setting: ok=%v err=%v", ok, err)
		}
		for _, v := range []string{"30", "7"} {
			if err := s.SetSetting(ctx, "session_days", v); err != nil {
				t.Fatal(err)
			}
		}
		if v, ok, err := s.Setting(ctx, "session_days"); !ok || err != nil || v != "7" {
			t.Fatalf("Setting = %q %v %v", v, ok, err)
		}
	})
}
