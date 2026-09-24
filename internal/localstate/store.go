// Package localstate stores personal preferences, journal notes and activity
// receipts, plus a separately rebuildable compact run-status index. It never
// stores connection credentials or full remote run/history payloads.
package localstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/lazymlflow/internal/fileuri"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"modernc.org/sqlite"
)

const schemaVersion = 3
const busyBudget = 2 * time.Second

// Store opens lazily. Reading a missing store does not create directories/files.
// Each independent value is written atomically, so separate CLI/TUI processes do
// not overwrite one another's experiments or visibility choices.
type Store struct {
	mu     sync.Mutex
	path   string
	db     *sql.DB
	closed bool
}

var _ core.StateStore = (*Store)(nil)

func DefaultPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" || !filepath.IsAbs(home) {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "lazymlflow", "state.db")
}
func New(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	return &Store{path: path}
}
func (s *Store) Path() string { return s.path }

func retry(ctx context.Context, f func() error) error {
	deadline := time.Now().Add(busyBudget)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := f()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		var se *sqlite.Error
		if err == nil || !errors.As(err, &se) || (se.Code()&255 != 5 && se.Code()&255 != 6) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local preferences database is busy; retry shortly: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (s *Store) open(ctx context.Context, create bool) (*sql.DB, error) {
	for !s.mu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed {
		return nil, errors.New("local preferences store is closed")
	}
	if s.db != nil {
		return s.db, nil
	}
	if s.path == "" {
		return nil, errors.New("cannot locate local preferences: set an absolute XDG_DATA_HOME or HOME")
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, nil
		}
		if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return nil, fmt.Errorf("create preferences directory: %w", err)
		}
		var f *os.File
		f, err = os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create preferences database: %w", err)
		}
		if f != nil {
			if err := f.Close(); err != nil {
				return nil, err
			}
		}
		info, err = os.Lstat(s.path)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect preferences database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("preferences database must be a regular file (not a symlink or directory)")
	}
	abs, err := filepath.Abs(s.path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: fileuri.Path(abs)}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(50)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Check before WAL, then again under a write lock. An older binary must not
	// change a database prepared by a future schema, even its journal mode.
	var version int
	err = retry(ctx, func() error { return db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) })
	if err == nil && version > schemaVersion {
		err = fmt.Errorf("preferences schema %d is newer than supported version %d; upgrade lazymlflow", version, schemaVersion)
	}
	if err == nil && version < schemaVersion {
		err = migrate(ctx, db)
	}
	if err == nil {
		err = retry(ctx, func() error { _, e := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); return e })
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open local preferences: %w", err)
	}
	s.db = db
	return db, nil
}
func migrate(ctx context.Context, db *sql.DB) error {
	return retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version > schemaVersion {
			return fmt.Errorf("preferences schema %d is newer than supported version %d", version, schemaVersion)
		}
		if version == 0 {
			for _, statement := range []string{
				`CREATE TABLE experiment_views (source TEXT NOT NULL, experiment TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(source, experiment))`,
				`CREATE TABLE preferences (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)`,
				`CREATE TABLE visibility (source TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('experiment','run')), id TEXT NOT NULL, value TEXT NOT NULL CHECK(value IN ('hidden','archived')), PRIMARY KEY(source,kind,id))`,
				`PRAGMA user_version=1`,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			version = 1
		}
		if version == 1 {
			for _, statement := range []string{
				`CREATE TABLE notes (id TEXT PRIMARY KEY NOT NULL, source TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('run','experiment','dataset')), subject_id TEXT NOT NULL, label TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0, revision INTEGER NOT NULL CHECK(revision > 0))`,
				`CREATE INDEX notes_subject ON notes(source,kind,subject_id,created_at,id)`,
				`PRAGMA user_version=2`,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			version = 2
		}
		if version == 2 {
			for _, statement := range []string{
				`CREATE TABLE activity_sources (source TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)`,
				`CREATE TABLE activity_records (source TEXT NOT NULL, run_id TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(source,run_id))`,
				`CREATE TABLE activity_run_cache (source TEXT NOT NULL, run_id TEXT NOT NULL, experiment TEXT NOT NULL, status TEXT NOT NULL, start_time INTEGER NOT NULL, end_time INTEGER NOT NULL, observed_at INTEGER NOT NULL, PRIMARY KEY(source,run_id))`,
				`CREATE INDEX activity_cache_experiment ON activity_run_cache(source,experiment,status)`,
				`CREATE TABLE activity_count_cache (source TEXT NOT NULL, experiment TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(source,experiment))`,
				`PRAGMA user_version=3`,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	})
}
func identity(source, id string) error {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(id) == "" {
		return errors.New("source and resource identity are required")
	}
	return nil
}
func (s *Store) read(ctx context.Context, query string, args []any, value any) (bool, error) {
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return false, err
	}
	var raw string
	err = retry(ctx, func() error { return db.QueryRowContext(ctx, query, args...).Scan(&raw) })
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), value); err != nil {
		return false, fmt.Errorf("invalid saved preferences (database preserved): %w", err)
	}
	return true, nil
}
func (s *Store) write(ctx context.Context, query string, args ...any) error {
	db, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	return retry(ctx, func() error { _, err := db.ExecContext(ctx, query, args...); return err })
}
func (s *Store) LoadView(ctx context.Context, source, experiment string) (core.ExperimentView, bool, error) {
	v := core.ExperimentView{}
	if err := identity(source, experiment); err != nil {
		return v, false, err
	}
	found, err := s.read(ctx, `SELECT value FROM experiment_views WHERE source=? AND experiment=?`, []any{source, experiment}, &v)
	if err == nil && found {
		if validation := core.ValidateView(v); validation != nil {
			return v, false, fmt.Errorf("invalid saved view (database preserved): %w", validation)
		}
	}
	return v, found, err
}
func (s *Store) SaveView(ctx context.Context, source, experiment string, v core.ExperimentView) error {
	if err := identity(source, experiment); err != nil {
		return err
	}
	if err := core.ValidateView(v); err != nil {
		return err
	}
	value, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.write(ctx, `INSERT INTO experiment_views(source,experiment,value) VALUES(?,?,?) ON CONFLICT(source,experiment) DO UPDATE SET value=excluded.value`, source, experiment, string(value))
}
func (s *Store) ResetView(ctx context.Context, source, experiment string) error {
	if err := identity(source, experiment); err != nil {
		return err
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return err
	}
	return retry(ctx, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM experiment_views WHERE source=? AND experiment=?`, source, experiment)
		return err
	})
}
func (s *Store) LoadLayout(ctx context.Context) (core.LayoutPreferences, bool, error) {
	v := core.DefaultLayout()
	found, err := s.read(ctx, `SELECT value FROM preferences WHERE key='layout'`, nil, &v)
	return v, found, err
}
func (s *Store) SaveLayout(ctx context.Context, v core.LayoutPreferences) error {
	if v.LeftRatio <= 0 || v.LeftRatio >= 1 || v.TopRatio <= 0 || v.TopRatio >= 1 {
		return errors.New("pane proportions must be between zero and one")
	}
	if !validVisibilityFilter(v.ExperimentVisibility) {
		return errors.New("invalid experiment visibility preference")
	}
	value, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.write(ctx, `INSERT INTO preferences(key,value) VALUES('layout',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(value))
}
func validVisibilityFilter(value string) bool {
	return value == "normal" || value == "hidden" || value == "archived" || value == "all"
}
func (s *Store) ListVisibility(ctx context.Context, source string) (map[string]core.Visibility, error) {
	result := map[string]core.Visibility{}
	if strings.TrimSpace(source) == "" {
		return result, errors.New("source identity is required")
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return result, err
	}
	err = retry(ctx, func() error {
		rows, err := db.QueryContext(ctx, `SELECT kind,id,value FROM visibility WHERE source=?`, source)
		if err != nil {
			return err
		}
		defer rows.Close()
		current := map[string]core.Visibility{}
		for rows.Next() {
			var kind, id string
			var value core.Visibility
			if err := rows.Scan(&kind, &id, &value); err != nil {
				return err
			}
			current[core.VisibilityKey(kind, id)] = value
		}
		if err := rows.Err(); err != nil {
			return err
		}
		result = current
		return nil
	})
	return result, err
}
func (s *Store) SetVisibility(ctx context.Context, source, kind, id string, value core.Visibility) error {
	if err := identity(source, id); err != nil {
		return err
	}
	if kind != "experiment" && kind != "run" {
		return errors.New("visibility kind must be experiment or run")
	}
	if value == core.VisibilityNormal {
		db, err := s.open(ctx, false)
		if err != nil || db == nil {
			return err
		}
		return retry(ctx, func() error {
			_, err := db.ExecContext(ctx, `DELETE FROM visibility WHERE source=? AND kind=? AND id=?`, source, kind, id)
			return err
		})
	}
	if value != core.VisibilityHidden && value != core.VisibilityArchived {
		return errors.New("visibility must be normal, hidden, or archived")
	}
	return s.write(ctx, `INSERT INTO visibility(source,kind,id,value) VALUES(?,?,?,?) ON CONFLICT(source,kind,id) DO UPDATE SET value=excluded.value`, source, kind, id, string(value))
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
