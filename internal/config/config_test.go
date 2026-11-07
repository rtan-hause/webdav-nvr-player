package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.DataDir != "./data" || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.WebDAV.Configured() || cfg.WebDAV.Root != "/" || cfg.CameraProfile != "reolink" || cfg.CameraTZ.String() != "UTC" {
		t.Fatalf("unexpected source defaults: %+v", cfg)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"PORT": "9090", "DATA_DIR": "/data", "LOG_LEVEL": "debug",
		"WEBDAV_URL": "https://sftpgo.example.com/dav/", "WEBDAV_USERNAME": "nvr", "WEBDAV_PASSWORD": "s3cret",
		"WEBDAV_ROOT": "recordings/reolink", "CAMERA_PROFILE": "Reolink", "CAMERA_TZ": "America/Los_Angeles",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9090" || cfg.DataDir != "/data" || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	w := cfg.WebDAV
	if !w.Configured() || w.Username != "nvr" || w.Password != "s3cret" || w.Root != "/recordings/reolink/" {
		t.Fatalf("webdav not applied: %+v", w)
	}
	if cfg.CameraProfile != "reolink" || cfg.CameraTZ.String() != "America/Los_Angeles" {
		t.Fatalf("camera settings not applied: %+v", cfg)
	}
}

func TestFromEnvIngestSettings(t *testing.T) {
	cfg, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workers != 2 || cfg.ScanEvery.String() != "5m0s" || cfg.FullScanEvery.String() != "24h0m0s" || cfg.FFmpeg != "ffmpeg" || cfg.IngestSecret != "" {
		t.Fatalf("defaults: %+v", cfg)
	}
	cfg, err = FromEnv(env(map[string]string{
		"DATABASE_URL": "postgres://nvr@db/nvr", "INGEST_SECRET": "0123456789abcdef0123",
		"INGEST_WORKERS": "4", "SCAN_INTERVAL": "2m", "FULL_SCAN_INTERVAL": "6h", "FFMPEG_PATH": "/opt/ffmpeg",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://nvr@db/nvr" || cfg.Workers != 4 || cfg.ScanEvery.String() != "2m0s" || cfg.FullScanEvery.String() != "6h0m0s" || cfg.FFmpeg != "/opt/ffmpeg" {
		t.Fatalf("overrides: %+v", cfg)
	}
}

func TestFromEnvFallsBackToTZ(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"TZ": "Europe/Berlin"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CameraTZ.String() != "Europe/Berlin" {
		t.Fatalf("CameraTZ = %s", cfg.CameraTZ)
	}
}

func TestFromEnvPasswordFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(f, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := FromEnv(env(map[string]string{"WEBDAV_PASSWORD_FILE": f}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebDAV.Password != "from-file" {
		t.Fatalf("password = %q", cfg.WebDAV.Password)
	}
}

func TestFromEnvRejectsBadValues(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"port not a number":     {"PORT": "http"},
		"port out of range":     {"PORT": "70000"},
		"unknown log level":     {"LOG_LEVEL": "loud"},
		"webdav not http":       {"WEBDAV_URL": "ftp://example.com/"},
		"webdav no host":        {"WEBDAV_URL": "https:///dav"},
		"creds in url":          {"WEBDAV_URL": "https://nvr:pw@example.com/dav/"},
		"both password sources": {"WEBDAV_PASSWORD": "a", "WEBDAV_PASSWORD_FILE": "/nonexistent"},
		"missing password file": {"WEBDAV_PASSWORD_FILE": "/nonexistent/pw"},
		"bad time zone":         {"CAMERA_TZ": "Mars/Olympus"},
		"mysql database":        {"DATABASE_URL": "mysql://x"},
		"short secret":          {"INGEST_SECRET": "abc"},
		"zero workers":          {"INGEST_WORKERS": "0"},
		"scan too often":        {"SCAN_INTERVAL": "5s"},
		"scan not a duration":   {"SCAN_INTERVAL": "often"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(env(m)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
