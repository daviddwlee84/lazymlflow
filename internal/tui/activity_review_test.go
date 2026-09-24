package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

func activityReviewModel(t *testing.T) (*model, *activityState) {
	t.Helper()
	m, _ := readyModel()
	store := localstate.New(filepath.Join(t.TempDir(), "state.db"))
	m.opts.State = store
	m.opts.Activity = core.DefaultActivitySettings()
	t.Cleanup(func() { m.stopAll(); store.Close() })
	a := m.activityState()
	a.Loaded, a.Scope, m.focus = true, scopeUnread, 1
	return m, a
}

func activityReviewSnapshot(m *model, records ...core.ActivityRecord) core.ActivitySnapshot {
	snapshot := core.NewActivitySnapshot(core.SourceKey(m.target()))
	for _, record := range records {
		snapshot.Records[record.RunID] = record
		snapshot.UpdatedAt = max(snapshot.UpdatedAt, record.ObservedAt)
	}
	return snapshot
}

func TestActivityReviewRefreshKeepsFilteredSelectionIndex(t *testing.T) {
	m, a := activityReviewModel(t)
	rows := []core.ActivityRecord{
		{RunID: "r1", RunName: "other", ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: 100, UpdatedAt: 300},
		{RunID: "r2", RunName: "keep two", ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: 100, UpdatedAt: 200},
		{RunID: "r3", RunName: "keep three", ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: 100, UpdatedAt: 100},
	}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, rows...))
	r := a.Views[scopeUnread]
	r.Local = "keep"
	m.selectRun(1)
	if r.Selected != "r3" {
		t.Fatalf("setup selected %q", r.Selected)
	}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, rows...))
	if r.Selected != "r3" || r.Index != 1 {
		t.Fatalf("poll lost visible selection position: selected=%s index=%d; filtered r3 is row 1", r.Selected, r.Index)
	}
}

func TestActivityReviewRetainedReadInspectionReceivesUpdates(t *testing.T) {
	m, a := activityReviewModel(t)
	row := core.ActivityRecord{RunID: "r1", RunName: "Run", ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: 100, Metrics: []core.Metric{{Key: "loss", Value: 0.5}}}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, row))
	full := row.Run()
	full.Data.Params = []core.KeyValue{{Key: "lr", Value: "0.1"}}
	a.Runs[row.RunID], a.Inspect, m.focus = &full, &full, 2
	row.ReadRevision, row.ObservedAt, row.Status, row.EndTime = 2, 200, "FINISHED", 190
	row.Metrics = []core.Metric{{Key: "loss", Value: 0.2}}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, row))
	inspected := m.run()
	if inspected == nil || inspected.Info.Status != "FINISHED" || len(inspected.Data.Metrics) != 1 || inspected.Data.Metrics[0].Value != 0.2 {
		t.Fatalf("retained details froze after marking read: %#v", inspected)
	}
	if len(inspected.Data.Params) != 1 {
		t.Fatal("updating retained metadata discarded full run params")
	}
}

func TestActivityReviewLateSnapshotCannotDropNewlyObservedRuns(t *testing.T) {
	m, _ := activityReviewModel(t)
	older := core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100}
	newer := older
	newer.ObservedAt = 200
	added := core.ActivityRecord{RunID: "r2", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 200}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, newer, added))
	m.acceptActivitySnapshot(activityReviewSnapshot(m, older))
	if _, exists := m.activityCurrent().Snapshot.Records["r2"]; !exists {
		t.Fatal("late concurrent scan snapshot removed a run discovered by a newer result")
	}
}

func TestActivityReviewReadReceiptKeepsExperimentBadgeConsistent(t *testing.T) {
	m, a := activityReviewModel(t)
	a.ReadReceipts["r1"] = 2
	row := core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100}
	snapshot := activityReviewSnapshot(m, row)
	snapshot.Counts["e1"] = core.ExperimentActivityCounts{ExperimentID: "e1", Total: 1, Unread: 1}
	m.acceptActivitySnapshot(snapshot)
	if a.Snapshot.Records["r1"].Unread() {
		t.Fatal("receipt was not applied")
	}
	if a.Snapshot.Counts["e1"].Unread != 0 {
		t.Fatal("experiment unread badge still counts a run whose read receipt was applied")
	}
}

func TestActivityReviewCatalogRunJumpLeavesVirtualScope(t *testing.T) {
	m, a := activityReviewModel(t)
	row := core.ActivityRecord{RunID: "old", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100}
	m.acceptActivitySnapshot(activityReviewSnapshot(m, row))
	source := core.SourceKey(m.target())
	m.work.catalogs[source] = &catalogState{Value: core.DatasetCatalog{Source: source}, RunGen: 7}
	m.work.catalog = true
	requested := sampleRun("requested")
	requested.Info.ExperimentID = "e2"
	m.acceptCatalogRun(catalogRunMsg{source: source, gen: 7, run: requested, experiment: core.Experiment{ID: "e2", Name: "Second"}})
	if a.Scope != scopeExperiment || m.run() == nil || m.run().ID() != "requested" {
		t.Fatalf("dataset jump retained virtual selection: scope=%q run=%#v", a.Scope, m.run())
	}
}

func TestActivityReviewLoadAllRunningDoesNotExpandRecentHistory(t *testing.T) {
	m, a := activityReviewModel(t)
	a.Scope = scopeRunning
	m.performActivity("loadall")
	if a.RecentLimit != 100 {
		t.Fatal("load-all in Running changed Recent's fetch limit and requests unrelated historical rows")
	}
}

func TestActivityReviewComparisonKeepsAxisShortcut(t *testing.T) {
	m, _ := activityReviewModel(t)
	m.acceptActivitySnapshot(activityReviewSnapshot(m, core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100}))
	m.compare = true
	var matches []string
	for _, action := range m.actions() {
		for _, key := range action.Keys {
			if key == "a" {
				matches = append(matches, action.ID)
			}
		}
	}
	if len(matches) != 1 || matches[0] != "axis" {
		t.Fatalf("comparison a shortcut collides: %v", matches)
	}
}

func TestActivityReviewPendingScanExposesCancellation(t *testing.T) {
	m, a := activityReviewModel(t)
	a.Pending, a.FullPending = true, true
	ctx, _ := m.operation("activity:full")
	for _, action := range m.actions() {
		for _, key := range action.Keys {
			if key == "ctrl+x" {
				m.perform(action.ID)
				if a.Pending || a.FullPending || ctx.Err() == nil {
					t.Fatal("scan cancel did not clear pending state and cancel its operation")
				}
				return
			}
		}
	}
	t.Fatal("long-running activity scan has no Ctrl+X cancel action")
}

type delayedActivityReviewBackend struct {
	*fakeBackend
	started, release chan struct{}
	run              core.Run
}

func (b *delayedActivityReviewBackend) GetRun(ctx context.Context, _ string) (core.Run, error) {
	copy := b.run
	close(b.started)
	select {
	case <-b.release:
		return copy, nil
	case <-ctx.Done():
		return core.Run{}, ctx.Err()
	}
}

func TestActivityReviewLateHydrationCannotReplaceNewerObservation(t *testing.T) {
	m, a := activityReviewModel(t)
	row := core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: time.Now().UnixMilli() - 1000}
	first := activityReviewSnapshot(m, row)
	first.Version = 1
	m.acceptActivitySnapshot(first)
	backend := &delayedActivityReviewBackend{fakeBackend: &fakeBackend{}, started: make(chan struct{}), release: make(chan struct{}), run: row.Run()}
	m.state().Session.Backend = backend
	cmd := m.ensureActivityRun()
	if cmd == nil {
		t.Fatal("hydration was not requested")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("hydration did not start")
	}
	row.Status, row.EndTime, row.ObservedAt = "FINISHED", time.Now().UnixMilli(), time.Now().UnixMilli()+1
	second := activityReviewSnapshot(m, row)
	second.Version = 2
	m.acceptActivitySnapshot(second)
	close(backend.release)
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("hydration did not complete")
	}
	if a.Runs["r1"] == nil || m.run() == nil || m.run().Info.Status != "FINISHED" {
		t.Fatalf("older hydrated metadata replaced newer polling result: %#v", m.run())
	}
}

func TestActivityReviewSelectLoadsActualExperimentReadPolicy(t *testing.T) {
	m, _ := activityReviewModel(t)
	ctx, source := context.Background(), core.SourceKey(m.target())
	view := core.DefaultView(nil, nil)
	view.Activity = &core.ActivityPolicy{ReadOn: "open"}
	if err := m.opts.State.SaveView(ctx, source, "e2", view); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	run := sampleRun("actual-experiment-run")
	run.Info.ExperimentID, run.Info.StartTime = "e2", now
	snapshot, err := m.activityStore().ObserveActivity(ctx, source, core.ActivityBatch{Experiments: []core.Experiment{{ID: "e2", Name: "Actual experiment"}}, ReplaceExperiments: true, Observations: []core.ActivityObservation{{Run: run}}, StartedAt: now, ObservedAt: now, InitialUnreadSince: now - 1, Checkpoint: now, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	m.acceptActivitySnapshot(snapshot)
	if m.state().Runs["e2"] != nil {
		t.Fatal("setup unexpectedly preloaded experiment policy")
	}
	m.opts.Activity.ReadOn = "select"
	cmd := m.activityReadSelected(true)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
	snapshot, err = m.activityStore().LoadActivity(ctx, source)
	if err != nil || !snapshot.Records[run.ID()].Unread() {
		t.Fatalf("global select overrode saved per-experiment open policy: %+v %v", snapshot.Records[run.ID()], err)
	}
	view.Activity.ReadOn = "select"
	if err := m.opts.State.SaveView(ctx, source, "e2", view); err != nil {
		t.Fatal(err)
	}
	m.opts.Activity.ReadOn = "open"
	cmd = m.activityReadSelected(true)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
	snapshot, err = m.activityStore().LoadActivity(ctx, source)
	if err != nil || snapshot.Records[run.ID()].Unread() {
		t.Fatalf("saved select override was ignored: %+v %v", snapshot.Records[run.ID()], err)
	}
}

func TestActivityReviewNextUnreadKeepsSelectionVisible(t *testing.T) {
	m, a := activityReviewModel(t)
	m.acceptActivitySnapshot(activityReviewSnapshot(m,
		core.ActivityRecord{RunID: "r1", RunName: "keep one", ExperimentID: "e1", Revision: 2, ReadRevision: 1, UpdatedAt: 30},
		core.ActivityRecord{RunID: "r2", RunName: "other", ExperimentID: "e1", Revision: 2, ReadRevision: 1, UpdatedAt: 20},
		core.ActivityRecord{RunID: "r3", RunName: "keep three", ExperimentID: "e1", Revision: 2, ReadRevision: 1, UpdatedAt: 10},
	))
	r := a.Views[scopeUnread]
	r.Local = "keep"
	m.selectRun(0)
	m.nextUnread(1)
	for i, row := range m.runRows() {
		if row.ID == r.Selected {
			if i != r.Index {
				t.Fatalf("unread navigation selected visible row %d but stored index %d", i, r.Index)
			}
			return
		}
	}
	t.Fatalf("unread navigation selected a run hidden by the active view: %q", r.Selected)
}

func BenchmarkActivityReviewDashboard(b *testing.B) {
	m, _ := readyModel()
	store := localstate.New(filepath.Join(b.TempDir(), "state.db"))
	defer store.Close()
	m.opts.State = store
	m.width, m.height, m.focus = 160, 45, 1
	a := m.activityState()
	a.Loaded, a.Scope = true, scopeUnread
	snapshot := core.NewActivitySnapshot(core.SourceKey(m.target()))
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("run-%05d", i)
		snapshot.Records[id] = core.ActivityRecord{RunID: id, RunName: id, ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, UpdatedAt: int64(i), StartTime: int64(i)}
	}
	m.acceptActivitySnapshot(snapshot)
	m.refreshRowCache()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.View()
	}
}

func BenchmarkActivityReviewApplySnapshotWithLoadedRuns(b *testing.B) {
	m, _ := readyModel()
	store := localstate.New(filepath.Join(b.TempDir(), "state.db"))
	defer store.Close()
	m.opts.State = store
	a := m.activityState()
	a.Loaded = true
	runs := m.state().Runs["e1"]
	runs.Rows = make([]core.Run, 10000)
	snapshot := core.NewActivitySnapshot(core.SourceKey(m.target()))
	for i := range runs.Rows {
		id := fmt.Sprintf("run-%05d", i)
		runs.Rows[i] = sampleRun(id)
		snapshot.Records[id] = core.ActivityRecord{RunID: id, RunName: id, ExperimentID: "e1", Status: "RUNNING", Revision: 2, ReadRevision: 1, ObservedAt: 1, UpdatedAt: int64(i), StartTime: int64(i)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot.Version = uint64(i + 1)
		m.acceptActivitySnapshot(snapshot)
	}
}
