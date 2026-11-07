CREATE TABLE cameras (
  id      INTEGER PRIMARY KEY,
  name    TEXT NOT NULL,
  channel TEXT NOT NULL DEFAULT '',
  UNIQUE (name, channel)
);

CREATE TABLE clips (
  id               INTEGER PRIMARY KEY,
  camera_id        INTEGER NOT NULL REFERENCES cameras (id),
  path             TEXT NOT NULL UNIQUE,
  start_ms         INTEGER NOT NULL,
  end_ms           INTEGER NOT NULL DEFAULT 0,
  codec            TEXT NOT NULL DEFAULT '',
  width            INTEGER NOT NULL DEFAULT 0,
  height           INTEGER NOT NULL DEFAULT 0,
  size             INTEGER NOT NULL DEFAULT 0,
  etag             TEXT NOT NULL DEFAULT '',
  mod_ms           INTEGER NOT NULL DEFAULT 0,
  status           TEXT NOT NULL DEFAULT 'pending',
  error            TEXT NOT NULL DEFAULT '',
  attempts         INTEGER NOT NULL DEFAULT 0,
  next_attempt_ms  INTEGER NOT NULL DEFAULT 0,
  thumb_state      TEXT NOT NULL DEFAULT 'none',
  thumb_kind       TEXT NOT NULL DEFAULT '',
  thumb_path       TEXT NOT NULL DEFAULT '',
  snapshot_id      INTEGER,
  pair_deadline_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX clips_camera_start ON clips (camera_id, start_ms);
CREATE INDEX clips_start ON clips (start_ms);
CREATE INDEX clips_work ON clips (status, thumb_state, next_attempt_ms);

CREATE TABLE snapshots (
  id        INTEGER PRIMARY KEY,
  camera_id INTEGER NOT NULL REFERENCES cameras (id),
  path      TEXT NOT NULL UNIQUE,
  ts_ms     INTEGER NOT NULL,
  size      INTEGER NOT NULL DEFAULT 0,
  etag      TEXT NOT NULL DEFAULT '',
  mod_ms    INTEGER NOT NULL DEFAULT 0,
  status    TEXT NOT NULL DEFAULT 'pending',
  clip_id   INTEGER
);
CREATE INDEX snapshots_camera_ts ON snapshots (camera_id, ts_ms);
CREATE INDEX snapshots_status ON snapshots (status);
