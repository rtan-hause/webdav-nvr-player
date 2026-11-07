// Command nvr serves the webdav-nvr-player web UI and API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // camera time zones work even on hosts without zoneinfo

	"github.com/rtan-hause/webdav-nvr-player/internal/auth"
	"github.com/rtan-hause/webdav-nvr-player/internal/config"
	"github.com/rtan-hause/webdav-nvr-player/internal/httpapi"
	"github.com/rtan-hause/webdav-nvr-player/internal/ingest"
	"github.com/rtan-hause/webdav-nvr-player/internal/profile"
	_ "github.com/rtan-hause/webdav-nvr-player/internal/profile/reolink" // registers the "reolink" profile
	"github.com/rtan-hause/webdav-nvr-player/internal/signedurl"
	"github.com/rtan-hause/webdav-nvr-player/internal/source/webdav"
	"github.com/rtan-hause/webdav-nvr-player/internal/store"
	"github.com/rtan-hause/webdav-nvr-player/internal/thumbs"
	"github.com/rtan-hause/webdav-nvr-player/internal/webui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "reset-password":
		err = resetPassword(os.Args[2:])
	case len(os.Args) > 1:
		fmt.Fprintf(os.Stderr, "usage: nvr                         start the server (settings come from the environment)\n       nvr reset-password [username]  give the user (default admin) a new password and sign it out everywhere\n")
		os.Exit(2)
	default:
		err = run()
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// resetPassword is for when the admin password is lost: it prints a new one.
// Run it where the server runs (same DATA_DIR or DATABASE_URL), for example
// `docker exec nvr nvr reset-password`.
func resetPassword(args []string) error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.Auth != config.AuthPassword {
		return fmt.Errorf("AUTH=%s: passwords are only used with AUTH=password", cfg.Auth)
	}
	user := auth.AdminUser
	if len(args) > 0 {
		user = args[0]
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	pw, err := auth.ResetPassword(ctx, st, user, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("New password for %s: %s\nAll of %s's sessions were signed out. Change the password in Settings after signing in.\n", user, pw, user)
	return nil
}

func run() error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	if err := checkIPv4PortFree(cfg.Addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("can't listen on %s: %w (another program may be using this port; set PORT)", cfg.Addr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The database holds the index and sign-in, so it's needed even without a recordings server.
	st, err := store.Open(ctx, cfg.DatabaseURL, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	authm := auth.New(auth.Options{Mode: cfg.Auth, Store: st, PublicURL: cfg.PublicURL, OIDC: cfg.OIDC})
	switch cfg.Auth {
	case config.AuthPassword:
		pw, err := authm.Bootstrap(ctx)
		if err != nil {
			return fmt.Errorf("creating the admin user: %w", err)
		}
		if pw != "" {
			slog.Warn("created the admin user; sign in with this password, then change it in Settings. It isn't shown again (nvr reset-password makes a new one)",
				"username", auth.AdminUser, "password", pw)
		}
	case config.AuthOIDC:
		slog.Info("sign-in through OpenID Connect", "issuer", cfg.OIDC.Issuer, "callback", cfg.PublicURL+"/auth/oidc/callback")
	case config.AuthNone:
		slog.Warn("AUTH=none: anyone who can reach this server can watch the cameras; only use this behind a proxy that signs people in")
	}
	go pruneSessions(ctx, st)

	opts := httpapi.Options{Version: version, UI: webui.FS(), Auth: authm}
	var ing *ingest.Ingester
	if cfg.WebDAV.Configured() {
		prof, err := profile.Lookup(cfg.CameraProfile)
		if err != nil {
			return fmt.Errorf("CAMERA_PROFILE: %w", err)
		}
		client, err := webdav.New(cfg.WebDAV.URL, cfg.WebDAV.Root, cfg.WebDAV.Username, cfg.WebDAV.Password, nil)
		if err != nil {
			return err
		}
		if _, err := exec.LookPath(cfg.FFmpeg); err != nil {
			slog.Warn("ffmpeg not found; clips without a camera snapshot won't get thumbnails", "ffmpeg", cfg.FFmpeg)
		}

		signer := signedurl.New()
		port := ln.Addr().(*net.TCPAddr).Port
		tm := &thumbs.Maker{Dir: cfg.DataDir, FFmpeg: cfg.FFmpeg}
		ing = ingest.New(ingest.Options{
			Store: st, Source: client, Profile: prof, Location: cfg.CameraTZ, Thumbs: tm,
			FrameURL: func(p string) string {
				return fmt.Sprintf("http://127.0.0.1:%d/internal/raw/%s", port, signer.Sign(p, time.Now().Add(10*time.Minute)))
			},
			Workers: cfg.Workers, RecentEvery: cfg.ScanEvery, FullEvery: cfg.FullScanEvery,
		})
		opts.Source = sourceCheck(client, cfg)
		opts.Index = &httpapi.Index{
			Store: st, Ingest: ing, Source: client, Thumbs: tm, Signer: signer,
			Location: cfg.CameraTZ, IngestSecret: cfg.IngestSecret,
		}
		slog.Info("index ready", "database", st.Dialect(), "profile", prof.ID(), "camera_tz", cfg.CameraTZ.String(), "webhook", cfg.IngestSecret != "")
	} else {
		slog.Warn("no WebDAV server configured; set WEBDAV_URL, WEBDAV_USERNAME and WEBDAV_PASSWORD (see .env.example)")
	}

	srv := &http.Server{Handler: httpapi.New(opts), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "url", "http://localhost"+cfg.Addr, "version", version, "data_dir", cfg.DataDir)
		errc <- srv.Serve(ln)
	}()

	ingestDone := make(chan struct{})
	if ing != nil {
		go func() {
			defer close(ingestDone)
			if st := opts.Source(ctx); st.OK {
				slog.Info("connected to WebDAV server", "url", st.URL, "user", st.Username, "folders", st.Folders)
			} else {
				slog.Error("WebDAV check failed; scans will retry on schedule", "url", st.URL, "user", st.Username, "err", st.Error)
			}
			ing.Run(ctx)
		}()
	} else {
		close(ingestDone)
	}

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	<-ingestDone
	return err
}

// pruneSessions deletes expired sessions every hour.
func pruneSessions(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := st.PruneSessions(ctx, time.Now().UnixMilli()); err != nil && ctx.Err() == nil {
			slog.Warn("pruning sessions failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkIPv4PortFree catches a port that another program holds on IPv4 only. macOS
// then lets us bind the IPv6 side without an error, and browsers opening
// localhost (127.0.0.1) silently reach the other program instead of this one.
func checkIPv4PortFree(addr string) error {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %s is already used by another program (for example an SSH port-forward); set PORT to a free port", strings.TrimPrefix(addr, ":"))
		}
		return nil // e.g. an IPv6-only host; the real listen below reports anything fatal
	}
	return ln.Close()
}

// sourceCheck lists the root folder to confirm the URL, credentials and root are right.
func sourceCheck(c *webdav.Client, cfg config.Config) func(context.Context) httpapi.SourceStatus {
	return func(ctx context.Context) httpapi.SourceStatus {
		st := httpapi.SourceStatus{
			Configured: true,
			URL:        c.URL(),
			Username:   cfg.WebDAV.Username,
			Profile:    cfg.CameraProfile,
			TimeZone:   cfg.CameraTZ.String(),
			CheckedAt:  time.Now().UTC(),
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		entries, err := c.List(ctx, "")
		if err != nil {
			st.Error = err.Error()
			return st
		}
		st.OK = true
		for _, e := range entries {
			if e.IsDir {
				if len(st.Folders) < 50 {
					st.Folders = append(st.Folders, e.Name)
				}
			} else {
				st.Files++
			}
		}
		return st
	}
}
