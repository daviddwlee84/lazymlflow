package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

func pinsModel(t *testing.T) (*model, *activityState, *localstate.Store) {
	t.Helper()
	m, _ := readyModel()
	store := localstate.New(filepath.Join(t.TempDir(), "state.db"))
	m.opts.State = store
	a := m.activityState()
	a.Loaded, m.focus = true, 1
	load := m.loadRunPins(false, false)
	m.Update(load())
	t.Cleanup(func() { m.stopAll(); store.Close() })
	return m, a, store
}

func TestRunPinsAcrossExperimentsPersistAndDoNotSelectComparison(t *testing.T) {
	m, a, store := pinsModel(t)
	m.toggleRunPin()
	if !m.runPinned("r1") || len(m.state().Basket) != 0 {
		t.Fatal("pinning did not keep bookmarks separate from comparison selection")
	}
	m.selectExperiment(1)
	other := sampleRun("other")
	other.Info.ExperimentID = "e2"
	m.runs().Rows = []core.Run{other}
	m.selectRun(0)
	m.toggleRunPin()
	if err := m.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.openActivity(scopePinned, true)
	if len(m.runRows()) != 2 || len(a.Snapshot.Records) != 0 || a.ScopeCounts[scopePinned] != 2 {
		t.Fatal("pins depend on the activity discovery window or selected experiment")
	}
	for _, row := range m.runRows() {
		if row.Run == nil || !m.runPinned(row.ID) {
			t.Fatal("pinned list lost a cached run")
		}
	}
	// A separate store and model reproduce reopening the dashboard.
	reopened := localstate.New(store.Path())
	defer reopened.Close()
	next, _ := readyModel()
	next.opts.State = reopened
	defer next.stopAll()
	next.Update(next.loadRunPins(false, false)())
	next.openActivity(scopePinned, true)
	if len(next.runRows()) != 2 || next.activityExperimentName("e2") != "Second" {
		t.Fatal("reopening lost cross-experiment bookmarks or names")
	}
	next.stopAll()
	next.active = "b"
	next.Update(next.loadRunPins(false, false)())
	next.openActivity(scopePinned, true)
	if len(next.runRows()) != 0 {
		t.Fatal("pins leaked into a different tracking source")
	}
}

func TestRunPinUnpinSurvivesStaleLoadAndHydration(t *testing.T) {
	m, a, store := pinsModel(t)
	m.toggleRunPin()
	if err := m.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := m.loadRunPins(true, false)()
	m.openActivity(scopePinned, true)
	hydrationGen := a.RunGen
	m.toggleRunPin()
	_, reload := m.Update(old)
	if m.runPinned("r1") || len(m.runRows()) != 0 || m.run() != nil || reload == nil {
		t.Fatal("late local load resurrected an explicitly removed pin")
	}
	m.Update(reload())
	m.Update(activityRunMsg{source: core.SourceKey(m.target()), gen: hydrationGen, id: "r1", run: sampleRun("r1"), startedAt: 10})
	if len(m.runRows()) != 0 || m.run() != nil {
		t.Fatal("late run hydration resurrected the removed selection")
	}
	values, err := store.LoadRunPins(context.Background(), core.SourceKey(m.target()))
	if err != nil || len(values) != 0 {
		t.Fatalf("unpin was not persisted: %v %v", values, err)
	}
}

func TestPinnedMissingAndDeletedRunsRemainRemovable(t *testing.T) {
	m, a, _ := pinsModel(t)
	m.toggleRunPin()
	a.Snapshot.Initialized = true
	a.Snapshot.Experiments = []core.Experiment{{ID: "unrelated"}}
	m.openActivity(scopePinned, true)
	m.Update(activityRunMsg{source: core.SourceKey(m.target()), gen: a.RunGen, id: "r1", startedAt: 10, err: errors.New("run no longer available")})
	if len(m.runRows()) != 1 || m.run() == nil || m.ensureActivityRun() != nil || !strings.Contains(strings.Join(m.runContent(100, 15).Lines, "\n"), "Selected pin unavailable") {
		t.Fatal("missing pin vanished or triggered another automatic request")
	}
	if m.loadActivityRun(true) == nil {
		t.Fatal("explicit retry was blocked")
	}
	deleted := sampleRun("r1")
	deleted.Info.LifecycleStage = "deleted"
	m.Update(activityRunMsg{source: core.SourceKey(m.target()), gen: a.RunGen, id: "r1", run: deleted, startedAt: 20})
	if len(m.runRows()) != 1 || !strings.Contains(strings.Join(m.runContent(100, 15).Lines, "\n"), "deleted remotely") {
		t.Fatal("deleted pin was not retained and labelled")
	}
	m.toggleRunPin()
	if len(m.runRows()) != 0 {
		t.Fatal("unavailable/deleted bookmark could not be removed")
	}
}

func TestPinRefreshRejectsOldHydrationAndRepinnedResults(t *testing.T) {
	m, a, _ := pinsModel(t)
	m.toggleRunPin()
	m.openActivity(scopePinned, true)
	pins := m.currentRunPins()
	newer := sampleRun("r1")
	newer.Info.RunName = "fresh full metadata"
	result := pinnedRunResult{run: newer, startedAt: 200, revision: pins.Edits["r1"], pinnedAt: pins.Items["r1"].PinnedAt}
	pins.RefreshGen = 7
	m.Update(runPinsRefreshedMsg{source: core.SourceKey(m.target()), gen: 7, results: map[string]pinnedRunResult{"r1": result}})
	for _, err := range []error{nil, errors.New("old failed request")} {
		m.Update(activityRunMsg{source: core.SourceKey(m.target()), id: "r1", gen: a.RunGen, run: sampleRun("r1"), startedAt: 100, err: err})
		if m.run().Name() != newer.Name() || a.MetadataAt["r1"] != 200 || pins.RunErrors["r1"] != "" {
			t.Fatal("late hydration overwrote a newer full fetch or marked it unavailable")
		}
	}
	m.toggleRunPin()
	m.leaveActivity()
	m.selectExperiment(0)
	m.selectRun(0)
	m.toggleRunPin()
	if !m.runPinned("r1") {
		t.Fatal("repin setup failed")
	}
	result.run.Info.RunName = "stale refresh after repin"
	result.startedAt = 300
	m.Update(runPinsRefreshedMsg{source: core.SourceKey(m.target()), gen: 7, results: map[string]pinnedRunResult{"r1": result}})
	if a.Runs["r1"].Name() == result.run.Name() {
		t.Fatal("an old refresh result changed a newly repinned run")
	}
}

type pinnedFetchBackend struct {
	fakeBackend
	mu    sync.Mutex
	calls map[string]int
	runs  map[string]core.Run
}

func (b *pinnedFetchBackend) GetRun(_ context.Context, id string) (core.Run, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[id]++
	if run, ok := b.runs[id]; ok {
		return run, nil
	}
	return core.Run{}, errors.New("missing bookmarked run")
}

func TestPinnedManualRefreshFetchesExactIDsAndKeepsFailures(t *testing.T) {
	m, a, store := pinsModel(t)
	source := core.SourceKey(m.target())
	for _, id := range []string{"old", "missing"} {
		if err := store.SaveRunPin(context.Background(), source, core.RunPin{RunID: id, ExperimentID: "past", RunName: id}); err != nil {
			t.Fatal(err)
		}
	}
	backend := &pinnedFetchBackend{calls: map[string]int{}, runs: map[string]core.Run{"old": {Info: core.RunInfo{RunID: "old", ExperimentID: "past", RunName: "renamed old run", Status: "FINISHED"}}}}
	m.state().Session.Backend = backend
	a.Scope = scopePinned
	load := m.refresh()
	_, refresh := m.Update(load())
	if refresh == nil {
		t.Fatal("list refresh did not fetch bookmark metadata")
	}
	m.Update(refresh())
	if backend.calls["old"] != 1 || backend.calls["missing"] != 1 || len(backend.calls) != 2 || len(m.runRows()) != 2 {
		t.Fatalf("refresh lost pins or queried something else: %v", backend.calls)
	}
	if a.Runs["old"].Name() != "renamed old run" || m.currentRunPins().RunErrors["missing"] == "" {
		t.Fatal("refresh did not apply successful metadata alongside a failed bookmark")
	}
	if m.activityScope() != scopePinned || m.selectedExperiment() != "e1" {
		t.Fatal("refresh navigated to a real experiment")
	}
}

func TestRunPinToggleDoesNotShadowMetricPinsOrGroupHeaders(t *testing.T) {
	m, _, _ := pinsModel(t)
	m.focus, m.tab = 2, 1
	for _, action := range m.runPinActions() {
		if action.ID == "toggle-run-pin" {
			t.Fatal("run pin action shadows metric pin in details")
		}
	}
	m.focus = 1
	m.runs().Rows = nil
	m.runs().Selected = ""
	if m.toggleRunPin() != nil || len(m.currentRunPins().Items) != 0 {
		t.Fatal("an empty/group selection created a run bookmark")
	}
}

func TestPinRefreshKeepsFullDetailsWhenActivityMetadataIsNewer(t *testing.T) {
	m, a, _ := pinsModel(t)
	m.toggleRunPin()
	pins := m.currentRunPins()
	a.Snapshot.Records["r1"] = core.ActivityRecord{RunID: "r1", RunName: "latest name", ExperimentID: "e1", Status: "FINISHED", ObservedAt: 300, Metrics: []core.Metric{{Key: "loss", Value: .1}}}
	a.MetadataAt["r1"] = 300
	full := sampleRun("r1")
	full.Data.Params = []core.KeyValue{{Key: "configuration", Value: "fresh full details"}}
	result := pinnedRunResult{run: full, startedAt: 200, revision: pins.Edits["r1"], pinnedAt: pins.Items["r1"].PinnedAt}
	pins.RefreshGen = 9
	m.Update(runPinsRefreshedMsg{source: core.SourceKey(m.target()), gen: 9, results: map[string]pinnedRunResult{"r1": result}})
	run := a.Runs["r1"]
	if run == nil || run.Info.Status != "FINISHED" || run.Name() != "latest name" || run.Data.Metrics[0].Value != .1 || run.Data.Params[0].Value != "fresh full details" || a.MetadataAt["r1"] != 300 || pins.FetchedAt["r1"] != 200 {
		t.Fatalf("metadata-only poll discarded full details or was overwritten: %#v", run)
	}
}

func TestPinnedKeepsInboxShortcutAndWorksWhileConnecting(t *testing.T) {
	m, a, _ := pinsModel(t)
	m.toggleRunPin()
	a.LastScope = scopeUnread
	m.state().Experiments = nil
	m.state().Session = nil
	m.state().ConnectPending = true
	if !strings.Contains(strings.Join(m.experimentContent(38, 24).Lines, "\n"), "Pinned 1") {
		t.Fatal("connection startup hid the local bookmark entry")
	}
	m.openActivity(scopePinned, true)
	if len(m.runRows()) != 1 || a.LastScope != scopeUnread {
		t.Fatal("opening cached pins depends on a connection or changes the inbox shortcut")
	}
	key(m, "I")
	if a.Scope != scopeUnread {
		t.Fatal("I did not restore the previously selected inbox")
	}
}
