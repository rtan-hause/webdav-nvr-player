// Package store is the clip index: cameras, clips, snapshots and the work
// still to do on them. It runs on SQLite (default) or Postgres (DATABASE_URL)
// with the same SQL, apart from the schema files in migrations/.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx"
	_ "modernc.org/sqlite"             // registers "sqlite"
)

//go:embed migrations
var migrations embed.FS

// ErrNotFound is returned when a row doesn't exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	pg bool
}

// Open connects to Postgres when databaseURL is a postgres:// URL, otherwise
// opens (creating if needed) SQLite at dataDir/nvr.db, and applies migrations.
func Open(ctx context.Context, databaseURL, dataDir string) (*Store, error) {
	var s *Store
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		db, err := sql.Open("pgx", databaseURL)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(10)
		s = &Store{db: db, pg: true}
	case databaseURL == "":
		if err := os.MkdirAll(dataDir, 0o750); err != nil {
			return nil, err
		}
		dsn := "file:" + filepath.Join(dataDir, "nvr.db") +
			"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		// One connection serialises writers, which avoids SQLITE_BUSY entirely.
		// Every query here takes milliseconds, so readers don't notice.
		db.SetMaxOpenConns(1)
		s = &Store{db: db}
	default:
		return nil, fmt.Errorf("DATABASE_URL must start with postgres:// (or be empty for SQLite)")
	}
	if err := s.db.PingContext(ctx); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Dialect returns "postgres" or "sqlite".
func (s *Store) Dialect() string {
	if s.pg {
		return "postgres"
	}
	return "sqlite"
}

// q rewrites ? placeholders as $1, $2... for Postgres. Queries never contain a literal "?".
func (s *Store) q(query string) string {
	if !s.pg {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	dir := "migrations/" + s.Dialect()
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: name must start with a number", name)
		}
		var exists int
		err = s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`), v).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := fs.ReadFile(migrations, dir+"/"+name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(string(body), ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations (version) VALUES (?)`), v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
