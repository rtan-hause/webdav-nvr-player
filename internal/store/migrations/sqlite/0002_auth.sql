-- Sign-in: local users (password mode), sessions, and settings changed in the app.
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_ms    INTEGER NOT NULL
);

-- token_hash is the SHA-256 of the cookie value. The cookie itself is never stored.
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  username   TEXT NOT NULL,
  created_ms INTEGER NOT NULL,
  seen_ms    INTEGER NOT NULL,
  expires_ms INTEGER NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_ms);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
