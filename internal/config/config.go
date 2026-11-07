// Package config loads runtime settings from the environment.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds process-wide settings. Auth and database settings are added as
// those subsystems land.
type Config struct {
	Addr     string     // listen address, from PORT (default 8080)
	DataDir  string     // index database and thumbnails, from DATA_DIR (default ./data)
	LogLevel slog.Level // from LOG_LEVEL: debug, info, warn, error (default info)

	WebDAV        WebDAV
	CameraProfile string         // from CAMERA_PROFILE (default reolink)
	CameraTZ      *time.Location // from CAMERA_TZ, then TZ (default UTC)

	DatabaseURL   string        // DATABASE_URL: postgres://... or empty for SQLite in DATA_DIR
	IngestSecret  string        // INGEST_SECRET: shared secret for the upload webhook; empty turns it off
	Workers       int           // INGEST_WORKERS (default 2)
	ScanEvery     time.Duration // SCAN_INTERVAL: rescan today+yesterday (default 5m)
	FullScanEvery time.Duration // FULL_SCAN_INTERVAL: crawl everything (default 24h)
	FFmpeg        string        // FFMPEG_PATH (default "ffmpeg" on PATH)
}

// WebDAV describes the server holding the camera uploads.
type WebDAV struct {
	URL      string // WEBDAV_URL, e.g. https://sftpgo.example.com/dav/
	Username string // WEBDAV_USERNAME
	Password string // WEBDAV_PASSWORD, or the contents of WEBDAV_PASSWORD_FILE
	Root     string // WEBDAV_ROOT: folder inside the share where date folders start (default /)
}

// Configured reports whether a WebDAV server was set.
func (w WebDAV) Configured() bool { return w.URL != "" }

// FromEnv reads Config using getenv (os.Getenv in production, a map in tests).
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	cfg := Config{Addr: ":8080", DataDir: "./data", LogLevel: slog.LevelInfo, CameraProfile: "reolink", CameraTZ: time.UTC}

	if p := get("PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("PORT must be a number from 1 to 65535, got %q", p)
		}
		cfg.Addr = ":" + p
	}
	if d := get("DATA_DIR"); d != "" {
		cfg.DataDir = d
	}
	if l := get("LOG_LEVEL"); l != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(l)); err != nil {
			return Config{}, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", l)
		}
	}

	if u := get("WEBDAV_URL"); u != "" {
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Config{}, fmt.Errorf("WEBDAV_URL must be an http:// or https:// URL, got %q", u)
		}
		if parsed.User != nil {
			return Config{}, fmt.Errorf("WEBDAV_URL must not contain credentials; use WEBDAV_USERNAME and WEBDAV_PASSWORD")
		}
		cfg.WebDAV.URL = u
	}
	cfg.WebDAV.Username = get("WEBDAV_USERNAME")
	cfg.WebDAV.Password = getenv("WEBDAV_PASSWORD") // passwords may legitimately have surrounding spaces
	if f := get("WEBDAV_PASSWORD_FILE"); f != "" {
		if cfg.WebDAV.Password != "" {
			return Config{}, fmt.Errorf("set WEBDAV_PASSWORD or WEBDAV_PASSWORD_FILE, not both")
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading WEBDAV_PASSWORD_FILE: %w", err)
		}
		cfg.WebDAV.Password = strings.TrimRight(string(b), "\r\n")
	}
	cfg.WebDAV.Root = "/" + strings.Trim(get("WEBDAV_ROOT"), "/")
	if cfg.WebDAV.Root != "/" {
		cfg.WebDAV.Root += "/"
	}

	if p := get("CAMERA_PROFILE"); p != "" {
		cfg.CameraProfile = strings.ToLower(p)
	}
	tz := get("CAMERA_TZ")
	if tz == "" {
		tz = get("TZ")
	}
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return Config{}, fmt.Errorf("CAMERA_TZ must be an IANA time zone like America/Los_Angeles, got %q", tz)
		}
		cfg.CameraTZ = loc
	}

	cfg.DatabaseURL = get("DATABASE_URL")
	if cfg.DatabaseURL != "" && !strings.HasPrefix(cfg.DatabaseURL, "postgres://") && !strings.HasPrefix(cfg.DatabaseURL, "postgresql://") {
		return Config{}, fmt.Errorf("DATABASE_URL must be a postgres:// URL (leave it empty to use SQLite in DATA_DIR)")
	}
	cfg.IngestSecret = get("INGEST_SECRET")
	if cfg.IngestSecret != "" && len(cfg.IngestSecret) < 16 {
		return Config{}, fmt.Errorf("INGEST_SECRET must be at least 16 characters (try: openssl rand -hex 24)")
	}
	cfg.Workers = 2
	if v := get("INGEST_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 32 {
			return Config{}, fmt.Errorf("INGEST_WORKERS must be a number from 1 to 32, got %q", v)
		}
		cfg.Workers = n
	}
	var err error
	if cfg.ScanEvery, err = duration(get("SCAN_INTERVAL"), 5*time.Minute, time.Minute); err != nil {
		return Config{}, fmt.Errorf("SCAN_INTERVAL: %w", err)
	}
	if cfg.FullScanEvery, err = duration(get("FULL_SCAN_INTERVAL"), 24*time.Hour, time.Hour); err != nil {
		return Config{}, fmt.Errorf("FULL_SCAN_INTERVAL: %w", err)
	}
	cfg.FFmpeg = get("FFMPEG_PATH")
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	return cfg, nil
}

func duration(v string, def, minimum time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("must be a duration like 5m or 24h, got %q", v)
	}
	if d < minimum {
		return 0, fmt.Errorf("must be at least %v, got %v", minimum, d)
	}
	return d, nil
}
