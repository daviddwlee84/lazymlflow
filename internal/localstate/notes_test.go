package localstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestNotesMissingStoreIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	s := New(path)
	defer s.Close()
	notes, err := s.ListNotes(context.Background(), core.Subject{Source: "source", Kind: "run", ID: "run"}, false)
	if err != nil || notes == nil || len(notes) != 0 {
		t.Fatalf("%v %v", notes, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created directory: %v", err)
	}
}

func TestNotesMigrateV1AndPreservePreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE experiment_views(source TEXT,experiment TEXT,value TEXT,PRIMARY KEY(source,experiment))`, `CREATE TABLE preferences(key TEXT PRIMARY KEY,value TEXT)`, `CREATE TABLE visibility(source TEXT,kind TEXT,id TEXT,value TEXT,PRIMARY KEY(source,kind,id))`, `PRAGMA user_version=1`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	view := core.DefaultView([]string{"loss"}, nil)
	raw, _ := json.Marshal(view)
	if _, err := db.Exec(`INSERT INTO experiment_views VALUES('source','1',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO visibility VALUES('source','run','hidden','hidden')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := New(path)
	defer s.Close()
	ctx := context.Background()
	note, err := s.SaveNote(ctx, core.Note{Subject: core.Subject{Source: "source", Kind: "run", ID: "run"}, Body: "Observation"}, 0)
	if err != nil || note.Revision != 1 {
		t.Fatalf("%+v %v", note, err)
	}
	got, found, err := s.LoadView(ctx, "source", "1")
	if err != nil || !found || got.Columns[3].Key != "loss" {
		t.Fatalf("%+v %v %v", got, found, err)
	}
	visibility, err := s.ListVisibility(ctx, "source")
	if err != nil || visibility["run/hidden"] != core.VisibilityHidden {
		t.Fatalf("%+v %v", visibility, err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version %d %v", version, err)
	}
}

func TestNotesIsolationRevisionDeletionAndRestore(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	ctx := context.Background()
	subject := core.Subject{Source: "source", Kind: "run", ID: "one", Label: "Original name"}
	note, err := s.SaveNote(ctx, core.Note{Subject: subject, Body: "## Observations\nTrain loss decreased."}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(note.ID) != 36 || note.Revision != 1 || note.CreatedAt == 0 || note.DeletedAt != 0 {
		t.Fatalf("%+v", note)
	}
	for _, other := range []core.Subject{{Source: "other", Kind: "run", ID: "one"}, {Source: "source", Kind: "experiment", ID: "one"}, {Source: "source", Kind: "dataset", ID: "one"}, {Source: "source", Kind: "run", ID: "other"}} {
		notes, err := s.ListNotes(ctx, other, true)
		if err != nil || len(notes) != 0 {
			t.Fatalf("subject leaked: %v %v", notes, err)
		}
		if _, err := s.DeleteNote(ctx, other, note.ID, 1, false); !errors.Is(err, core.ErrNoteNotFound) {
			t.Fatalf("cross-subject delete: %v", err)
		}
	}
	stale := note
	note.Body = "Updated"
	note.Subject.Label = "Renamed"
	note, err = s.SaveNote(ctx, note, 1)
	if err != nil || note.Revision != 2 || note.CreatedAt != stale.CreatedAt {
		t.Fatalf("%+v %v", note, err)
	}
	if _, err := s.SaveNote(ctx, stale, 1); !errors.Is(err, core.ErrNoteConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := s.DeleteNote(ctx, subject, note.ID, 1, false); !errors.Is(err, core.ErrNoteConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	deleted, err := s.DeleteNote(ctx, subject, note.ID, 2, false)
	if err != nil || deleted.DeletedAt == 0 || deleted.Revision != 3 {
		t.Fatalf("%+v %v", deleted, err)
	}
	if notes, err := s.ListNotes(ctx, subject, false); err != nil || len(notes) != 0 {
		t.Fatalf("%+v %v", notes, err)
	}
	if notes, err := s.ListNotes(ctx, subject, true); err != nil || len(notes) != 1 || notes[0].Body != "Updated" {
		t.Fatalf("%+v %v", notes, err)
	}
	if _, err := s.SaveNote(ctx, deleted, 3); !errors.Is(err, core.ErrNoteConflict) {
		t.Fatalf("edit deleted: %v", err)
	}
	restored, err := s.DeleteNote(ctx, subject, note.ID, 3, true)
	if err != nil || restored.DeletedAt != 0 || restored.Revision != 4 || restored.Body != "Updated" {
		t.Fatalf("%+v %v", restored, err)
	}
	note.Subject = subject
	if _, err := s.SaveNote(ctx, note, 0); !errors.Is(err, core.ErrNoteConflict) {
		t.Fatalf("duplicate create: %v", err)
	}
}

func TestNotesConcurrentEditsCompareAndSwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()
	s := New(path)
	note, err := s.SaveNote(ctx, core.Note{Subject: core.Subject{Source: "s", Kind: "run", ID: "r"}, Body: "First"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	start := make(chan struct{})
	out := make(chan error, 2)
	var wg sync.WaitGroup
	for _, body := range []string{"writer one", "writer two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store := New(path)
			defer store.Close()
			n := note
			n.Body = body
			<-start
			_, err := store.SaveNote(ctx, n, 1)
			out <- err
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	success, conflict := 0, 0
	for err := range out {
		if err == nil {
			success++
		} else if errors.Is(err, core.ErrNoteConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	s = New(path)
	defer s.Close()
	notes, err := s.ListNotes(ctx, note.Subject, false)
	if err != nil || len(notes) != 1 || notes[0].Revision != 2 {
		t.Fatalf("%+v %v", notes, err)
	}
}

func TestInvalidNotesDoNotCreateStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	s := New(path)
	defer s.Close()
	for _, n := range []core.Note{{Subject: core.Subject{Source: "s", Kind: "run", ID: "r"}, Body: "  \n"}, {Subject: core.Subject{Source: "s", Kind: "bad", ID: "r"}, Body: "body"}, {Body: "body"}} {
		if _, err := s.SaveNote(context.Background(), n, 0); err == nil {
			t.Fatalf("accepted %+v", n)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
