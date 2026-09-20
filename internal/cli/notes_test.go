package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestNotesCLILocalLifecycleAndStdin(t *testing.T) {
	isolateLocalState(t)
	opts, c, out, stderr := testOptions(t)
	opts.In = strings.NewReader("## Observation\ntrain_loss decreased")
	if status := Execute(context.Background(), []string{"notes", "add", "--run", "one", "--file", "-", "--json"}, opts); status != 0 {
		t.Fatalf("status %d %s", status, stderr)
	}
	var note core.Note
	if err := json.Unmarshal(out.Bytes(), &note); err != nil || note.Revision != 1 || note.Body != "## Observation\ntrain_loss decreased" {
		t.Fatalf("%s %v", out, err)
	}
	if len(c.opened) > 0 {
		t.Fatal("local note connected")
	}
	for _, args := range [][]string{{"notes", "edit", note.ID, "--run", "one", "--revision", "1", "--body", "Updated\nLine 2", "--json"}, {"notes", "delete", note.ID, "--run", "one", "--revision", "2", "--json"}, {"notes", "restore", note.ID, "--run", "one", "--revision", "3", "--json"}} {
		opts, c, out, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v status %d %s", args, status, stderr)
		}
		if len(c.opened) > 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("connected or bad data %s", out)
		}
	}
	opts, c, out, stderr = testOptions(t)
	if status := Execute(context.Background(), []string{"notes", "list", "--run", "one", "--search", "updated", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var result struct {
		Notes []core.Note `json:"notes"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Notes) != 1 || result.Notes[0].Revision != 4 || result.Notes[0].DeletedAt != 0 {
		t.Fatalf("%s %v", out, err)
	}
	if len(c.opened) > 0 {
		t.Fatal("list connected")
	}
	opts, _, out, stderr = testOptions(t)
	if status := Execute(context.Background(), []string{"notes", "edit", note.ID, "--run", "one", "--revision", "1", "--body", "stale", "--json"}, opts); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "reload") {
		t.Fatalf("stale update status %d %s %s", status, out, stderr)
	}
}

func TestNotesUsageAndMissingReadNeverCreateState(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{{"notes", "add", "--run", "one"}, {"notes", "add", "--run", "one", "--experiment", "1", "--body", "x"}, {"notes", "add", "--run", "one", "--body", "x", "--file", "-"}, {"notes", "edit", "id", "--run", "one", "--body", "x"}, {"notes", "delete", "id", "--run", "one"}, {"notes", "add", "--run", "one", "--body", "  "}, {"notes", "list", "--run", "one", "--index", "2"}, {"notes", "list", "--run", ""}} {
		opts, c, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 2 {
			t.Fatalf("%v status %d %s", args, status, stderr)
		}
		if len(c.opened) > 0 {
			t.Fatal("invalid args connected")
		}
	}
	opts, c, out, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"notes", "list", "--run", "one", "--json"}, opts); status != 0 || !strings.Contains(out.String(), `"notes": []`) {
		t.Fatalf("%d %s %s", status, out, stderr)
	}
	if len(c.opened) > 0 {
		t.Fatal("list connected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("usage/read created DB %v", err)
	}
}

func TestDatasetNoteResolvesStableIdentity(t *testing.T) {
	isolateLocalState(t)
	opts, c, out, stderr := testOptions(t)
	run := cliDatasetRun("run", "one")
	c.backend = &fakeBackend{getRun: func(context.Context, string) (core.Run, error) { return run, nil }}
	if status := Execute(context.Background(), []string{"notes", "add", "--dataset-run", "run", "--body", "Dataset observation", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var note core.Note
	if err := json.Unmarshal(out.Bytes(), &note); err != nil || note.Subject.ID != core.DatasetIdentity(firstSource(), run.Inputs.DatasetInputs[0].Dataset) || note.Subject.Label != "features" {
		t.Fatalf("%s %v", out, err)
	}
	opts, c, out, stderr = testOptions(t)
	if status := Execute(context.Background(), []string{"notes", "list", "--dataset", note.Subject.ID, "--json"}, opts); status != 0 || !strings.Contains(out.String(), "Dataset observation") {
		t.Fatalf("%d %s %s", status, out, stderr)
	}
	if len(c.opened) != 0 {
		t.Fatal("direct dataset identity connected")
	}
}
