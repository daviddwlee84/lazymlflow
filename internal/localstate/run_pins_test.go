package localstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func testRunPin(id string, pinnedAt int64) core.RunPin {
	return core.RunPin{RunID: id, ExperimentID: "experiment", RunName: "Run " + id, ExperimentName: "Experiment", Status: "RUNNING", StartTime: 100, LifecycleStage: "active", PinnedAt: pinnedAt, ObservedAt: 200}
}

func TestRunPinsMissingReadsAndDeletesDoNotCreateStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	s := New(path)
	defer s.Close()
	ctx := context.Background()
	pins, err := s.LoadRunPins(ctx, "source")
	if err != nil || pins == nil || len(pins) != 0 {
		t.Fatalf("missing read %+v %v", pins, err)
	}
	if err := s.DeleteRunPin(ctx, "source", "run"); err != nil {
		t.Fatal(err)
	}
	for _, pin := range []core.RunPin{{RunID: "run"}, {ExperimentID: "e"}, {RunID: "r", ExperimentID: "e", PinnedAt: -1}} {
		if err := s.SaveRunPin(ctx, "source", pin); err == nil {
			t.Fatalf("invalid pin accepted: %+v", pin)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("reads, deletion or invalid pin created state", err)
	}
}

func TestRunPinsPersistOrderMetadataAndOriginalPinnedTime(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s := New(path)
	for _, pin := range []core.RunPin{testRunPin("z", 10), testRunPin("b", 20), testRunPin("a", 20)} {
		if err := s.SaveRunPin(ctx, "source", pin); err != nil {
			t.Fatal(err)
		}
	}
	updated := testRunPin("a", 999)
	updated.RunName, updated.Status, updated.EndTime, updated.ObservedAt = "Renamed", "FINISHED", 400, 500
	if err := s.SaveRunPin(ctx, "source", updated); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLayout(ctx, core.DefaultLayout()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = New(path)
	defer s.Close()
	pins, err := s.LoadRunPins(ctx, "source")
	if err != nil || len(pins) != 3 {
		t.Fatalf("reopened pins %+v %v", pins, err)
	}
	if got := []string{pins[0].RunID, pins[1].RunID, pins[2].RunID}; !reflect.DeepEqual(got, []string{"a", "b", "z"}) {
		t.Fatal("wrong stable pin order", got)
	}
	if pins[0].PinnedAt != 20 || pins[0].RunName != "Renamed" || pins[0].Status != "FINISHED" || pins[0].ObservedAt != 500 {
		t.Fatalf("metadata upsert changed ordering or lost data: %+v", pins[0])
	}
	if _, found, err := s.LoadLayout(ctx); err != nil || !found {
		t.Fatal("pin writes lost unrelated preferences", err)
	}
	if err := s.ClearActivityCache(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if again, err := s.LoadRunPins(ctx, "source"); err != nil || !reflect.DeepEqual(again, pins) {
		t.Fatal("activity cache clear erased durable pins", err)
	}
	if err := s.DeleteRunPin(ctx, "source", "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRunPin(ctx, "source", "b"); err != nil {
		t.Fatal(err)
	}
	pins, err = s.LoadRunPins(ctx, "source")
	if err != nil || len(pins) != 2 || pins[0].RunID != "a" || pins[1].RunID != "z" {
		t.Fatal("unpin changed other pins", pins, err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatal("run pins unexpectedly changed schema", version, err)
	}
}

func TestRunPinNamespacesIsolateSourcesAndArbitraryIDs(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	sources := []string{"source", "source/other", "source_", "source%", "source0", "專案"}
	for _, source := range sources {
		pin := testRunPin("run/%_/中文", 50)
		pin.RunName = source
		if err := s.SaveRunPin(ctx, source, pin); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range sources {
		pins, err := s.LoadRunPins(ctx, source)
		if err != nil || len(pins) != 1 || pins[0].RunName != source {
			t.Fatalf("source %q leaked namespace: %+v %v", source, pins, err)
		}
	}
	if err := s.DeleteRunPin(ctx, sources[0], "run/%_/中文"); err != nil {
		t.Fatal(err)
	}
	for _, source := range sources[1:] {
		pins, err := s.LoadRunPins(ctx, source)
		if err != nil || len(pins) != 1 {
			t.Fatal("unpin leaked across source", source, err)
		}
	}
}

func TestIndependentRunPinWritesDoNotOverwriteOtherProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	seed := New(path)
	if err := seed.SaveLayout(ctx, core.DefaultLayout()); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := New(path)
			defer s.Close()
			errors <- s.SaveRunPin(ctx, "source", testRunPin(fmt.Sprintf("run-%02d", i), int64(i+1)))
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	s := New(path)
	defer s.Close()
	pins, err := s.LoadRunPins(ctx, "source")
	if err != nil || len(pins) != 16 {
		t.Fatalf("concurrent updates lost pins: %d %v", len(pins), err)
	}
	if pins[0].RunID != "run-15" || pins[15].RunID != "run-00" {
		t.Fatal("concurrent ordering incorrect")
	}
}

func TestRunPinJSONContainsOnlyCompactMetadataAndCorruptionIsPreserved(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	pin := testRunPin("run", 0)
	if err := s.SaveRunPin(ctx, "source", pin); err != nil {
		t.Fatal(err)
	}
	var raw string
	key := runPinKey("source", "run")
	if err := s.db.QueryRow(`SELECT value FROM preferences WHERE key=?`, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"run_id": true, "experiment_id": true, "run_name": true, "experiment_name": true, "status": true, "start_time": true, "end_time": true, "lifecycle_stage": true, "pinned_at": true, "observed_at": true}
	for name := range fields {
		if !allowed[name] {
			t.Fatalf("unexpected persisted field %q", name)
		}
	}
	pins, err := s.LoadRunPins(ctx, "source")
	if err != nil || pins[0].PinnedAt <= 0 {
		t.Fatal("missing first pin timestamp", err)
	}
	broken := `{"run_id":"different","experiment_id":"experiment"}`
	if _, err := s.db.Exec(`UPDATE preferences SET value=? WHERE key=?`, broken, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRunPins(ctx, "source"); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatal("corrupt pin identity accepted", err)
	}
	if err := s.SaveRunPin(ctx, "source", pin); err == nil {
		t.Fatal("corrupt pin silently overwritten")
	}
	if err := s.db.QueryRow(`SELECT value FROM preferences WHERE key=?`, key).Scan(&raw); err != nil || raw != broken {
		t.Fatal("corrupt pin not preserved", err)
	}
	if err := s.DeleteRunPin(ctx, "source", "run"); err != nil {
		t.Fatal("explicit unpin could not remove damaged entry", err)
	}
}
