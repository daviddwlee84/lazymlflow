package localstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestMissingStoreReadsAndResetDoNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "state.db")
	s := New(path)
	defer s.Close()
	ctx := context.Background()
	if _, found, err := s.LoadView(ctx, "source", "1"); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if _, found, err := s.LoadLayout(ctx); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if rows, err := s.ListVisibility(ctx, "source"); err != nil || len(rows) != 0 {
		t.Fatalf("%v %v", rows, err)
	}
	if err := s.ResetView(ctx, "source", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVisibility(ctx, "source", "run", "1", core.VisibilityNormal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created state directory: %v", err)
	}
}
func TestPersistenceIsolationAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs", "state.db")
	ctx := context.Background()
	s := New(path)
	view := core.DefaultView([]string{"accuracy"}, []string{"learning rate"})
	view.Expanded = map[string]bool{"run": true}
	view.Filter = "metrics.accuracy > 0.5"
	if err := s.SaveView(ctx, "source", "1", view); err != nil {
		t.Fatal(err)
	}
	layout := core.DefaultLayout()
	layout.Mouse = false
	layout.LeftRatio = .42
	if err := s.SaveLayout(ctx, layout); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVisibility(ctx, "source", "run", "a", core.VisibilityHidden); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVisibility(ctx, "source", "experiment", "1", core.VisibilityArchived); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = New(path)
	defer s.Close()
	got, found, err := s.LoadView(ctx, "source", "1")
	if err != nil || !found || !reflect.DeepEqual(got, view) {
		t.Fatalf("%#v %v %v", got, found, err)
	}
	if _, found, err := s.LoadView(ctx, "other", "1"); err != nil || found {
		t.Fatalf("source leaked: %v %v", found, err)
	}
	l, found, err := s.LoadLayout(ctx)
	if err != nil || !found || l != layout {
		t.Fatalf("%#v %v %v", l, found, err)
	}
	visibility, err := s.ListVisibility(ctx, "source")
	if err != nil || visibility["run/a"] != core.VisibilityHidden || visibility["experiment/1"] != core.VisibilityArchived {
		t.Fatalf("%v %v", visibility, err)
	}
	if err := s.SetVisibility(ctx, "source", "run", "a", core.VisibilityNormal); err != nil {
		t.Fatal(err)
	}
	visibility, err = s.ListVisibility(ctx, "source")
	if err != nil || len(visibility) != 1 {
		t.Fatalf("restore: %v %v", visibility, err)
	}
	if err := s.ResetView(ctx, "source", "1"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.LoadView(ctx, "source", "1"); err != nil || found {
		t.Fatalf("reset: %v %v", found, err)
	}
	for p, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(p)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != mode) {
			t.Fatalf("permissions %s: %v %v", p, info, err)
		}
	}
}
func TestConcurrentStoresPreserveDifferentExperiments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := New(path)
			defer s.Close()
			errs <- s.SaveView(ctx, "source", fmt.Sprint(i), core.DefaultView([]string{fmt.Sprint(i)}, nil))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := New(path)
	defer s.Close()
	for i := range 20 {
		v, found, err := s.LoadView(ctx, "source", fmt.Sprint(i))
		if err != nil || !found || v.Columns[3].Key != fmt.Sprint(i) {
			t.Fatalf("missing %d: %#v %v %v", i, v, found, err)
		}
	}
}
func TestFutureSchemaRefusedWithoutChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('keep'); PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := New(path)
	defer s.Close()
	if err := s.SaveView(context.Background(), "source", "1", core.DefaultView(nil, nil)); err == nil {
		t.Fatal("accepted future schema")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	var value, journal string
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 99 {
		t.Fatalf("version %d %v", v, err)
	}
	if err = db.QueryRow(`SELECT value FROM sentinel`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("data %q %v", value, err)
	}
	if err = db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "delete" {
		t.Fatalf("journal %q %v", journal, err)
	}
}
func TestLockedDatabaseHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s := New(path)
	defer s.Close()
	if err := s.SaveView(context.Background(), "source", "1", core.DefaultView(nil, nil)); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`ROLLBACK`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.SaveView(ctx, "source", "2", core.DefaultView(nil, nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want cancellation: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation blocked %v", elapsed)
	}
}
func TestInvalidDatabaseIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	raw := []byte("not a database")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(path)
	defer s.Close()
	if _, _, err := s.LoadLayout(context.Background()); err == nil {
		t.Fatal("corruption silently ignored")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("corrupt data overwritten")
	}
}
func TestPathErrorAndClosedStore(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := s.SaveLayout(context.Background(), core.DefaultLayout()); err == nil {
		t.Fatal("directory accepted as database")
	}
	s = New(filepath.Join(dir, "state.db"))
	s.Close()
	if _, _, err := s.LoadLayout(context.Background()); err == nil {
		t.Fatal("closed store accepted")
	}
}
func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", "relative")
	if got := DefaultPath(); got != filepath.Join(home, ".local/share/lazymlflow/state.db") {
		t.Fatal(got)
	}
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	if got := DefaultPath(); got != filepath.Join(base, "lazymlflow/state.db") {
		t.Fatal(got)
	}
}
