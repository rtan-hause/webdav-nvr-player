package store

import (
	"context"
	"database/sql"
	"errors"
)

// User is a local account (password sign-in).
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// Session is a signed-in browser. TokenHash is the SHA-256 of the cookie value.
type Session struct {
	TokenHash string
	Username  string
	CreatedMS int64
	SeenMS    int64
	ExpiresMS int64
}

// CountUsers returns how many local users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds a local user.
func (s *Store) CreateUser(ctx context.Context, username, hash string, nowMS int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO users (username, password_hash, created_ms) VALUES (?, ?, ?)`), username, hash, nowMS)
	return err
}

// UserByName returns a local user, or ErrNotFound.
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id, username, password_hash FROM users WHERE username = ?`), username).Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// SetPassword replaces a user's password hash. ErrNotFound if there's no such user.
func (s *Store) SetPassword(ctx context.Context, username, hash string) error {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE users SET password_hash = ? WHERE username = ?`), hash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO sessions (token_hash, username, created_ms, seen_ms, expires_ms) VALUES (?, ?, ?, ?, ?)`),
		x.TokenHash, x.Username, x.CreatedMS, x.SeenMS, x.ExpiresMS)
	return err
}

// SessionByHash returns a session that hasn't expired at nowMS, or ErrNotFound.
func (s *Store) SessionByHash(ctx context.Context, tokenHash string, nowMS int64) (Session, error) {
	var x Session
	err := s.db.QueryRowContext(ctx, s.q(`SELECT token_hash, username, created_ms, seen_ms, expires_ms FROM sessions WHERE token_hash = ? AND expires_ms > ?`), tokenHash, nowMS).
		Scan(&x.TokenHash, &x.Username, &x.CreatedMS, &x.SeenMS, &x.ExpiresMS)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// TouchSession records activity and moves the expiry (sliding sessions).
func (s *Store) TouchSession(ctx context.Context, tokenHash string, seenMS, expiresMS int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE sessions SET seen_ms = ?, expires_ms = ? WHERE token_hash = ?`), seenMS, expiresMS, tokenHash)
	return err
}

// DeleteSession signs one browser out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE token_hash = ?`), tokenHash)
	return err
}

// DeleteOtherSessions signs out every session except keepHash (empty keeps none).
// With username set it only touches that user's sessions.
func (s *Store) DeleteOtherSessions(ctx context.Context, username, keepHash string) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE token_hash <> ? AND (? = '' OR username = ?)`), keepHash, username, username)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, nowMS int64) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM sessions WHERE expires_ms <= ?`), nowMS)
	return err
}

// Setting returns a stored setting; ok is false when it was never set.
func (s *Store) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, s.q(`SELECT value FROM settings WHERE key = ?`), key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`), key, value)
	return err
}
