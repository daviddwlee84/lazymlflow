package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

func metricTestModel() *model {
	m, _ := readyModel()
	m.focus, m.tab, m.width, m.height = 2, 1, 150, 42
	m.syncInspection()
	return m
}

func applyOverlayForTest(m *model, keys ...string) {
	m.performInspection("inspect-overlay")
	m.inspect.OverlayDraft = slices.Clone(keys)
	m.applyMetricOverlay(m.inspectionView())
	m.syncInspection()
}

func metricCommandTree(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			metricCommandTree(m, next)
		}
		return
	}
	_, next := m.Update(msg)
	metricCommandTree(m, next)
}

func TestMetricOverlayFollowsExperimentAndReturnsToPreviousExperiment(t *testing.T) {
	m := metricTestModel()
	m.run().Data.Metrics = append(m.run().Data.Metrics, core.Metric{Key: "valid_corr"})
	m.syncInspection()
	applyOverlayForTest(m, "loss", "valid_corr")
	m.selectRun(1)
	m.syncInspection()
	if v := m.inspectionView(); !v.ExpandedChart || !slices.Equal(v.Overlay, []string{"loss", "valid_corr"}) {
		t.Fatalf("same experiment did not inherit overlay: %+v", v)
	}
	m.selectExperiment(1)
	other := sampleRun("other")
	other.Info.ExperimentID = "e2"
	m.runs().Rows = []core.Run{other}
	m.selectRun(0)
	m.syncInspection()
	if v := m.inspectionView(); v.ExpandedChart || len(v.Overlay) != 0 {
		t.Fatal("overlay leaked to another experiment")
	}
	applyOverlayForTest(m, "loss")
	m.selectExperiment(0)
	m.syncInspection()
	if !slices.Equal(m.inspectionView().Overlay, []string{"loss", "valid_corr"}) {
		t.Fatal("returning to an experiment lost its overlay")
	}
	m.targets[0].TrackingURI = "http://different-source"
	m.syncInspection()
	if len(m.inspectionView().Overlay) != 0 {
		t.Fatal("overlay leaked across source identity")
	}
}

func TestMetricOverlaySkipsMissingAndPicksUpNewMetadata(t *testing.T) {
	m := metricTestModel()
	applyOverlayForTest(m, "loss", "valid_corr")
	if got := m.desiredHistories(); len(got) != 1 {
		t.Fatalf("missing metric requested: %+v", got)
	}
	if !slices.Contains(m.metricPickerCandidates(), "valid_corr") {
		t.Fatal("missing selected metric cannot be removed in picker")
	}
	before := m.run()
	before.Data.Metrics = append(before.Data.Metrics, core.Metric{Key: "valid_corr", Value: .8})
	m.syncInspection()
	if m.run() != before || len(m.desiredHistories()) != 2 || len(m.inspectionView().All) != 2 {
		t.Fatal("in-place metadata update did not discover metric")
	}
	applyOverlayForTest(m, "future_metric")
	if len(m.desiredHistories()) != 0 {
		t.Fatal("all-missing overlay requested history")
	}
	text := strings.Join(m.metricContent(m.inspectionView(), 90, 20).Lines, "\n")
	if !strings.Contains(text, "not logged for this run yet") || strings.Contains(text, "Loading history") {
		t.Fatalf("all-missing overlay state: %s", text)
	}
	if !slices.Equal(m.inspectionView().Overlay, []string{"future_metric"}) {
		t.Fatal("missing metric selection was discarded")
	}
}

func TestMetricOverlayDraftCancelApplyAndMissingLimit(t *testing.T) {
	m := metricTestModel()
	key(m, "p")
	key(m, "space")
	if len(m.inspectionView().Overlay) != 0 || !slices.Equal(m.inspect.OverlayDraft, []string{"loss"}) {
		t.Fatal("draft changed live overlay")
	}
	key(m, "esc")
	if len(m.inspectionView().Overlay) != 0 {
		t.Fatal("cancel applied overlay")
	}
	key(m, "p")
	key(m, "space")
	key(m, "enter")
	if !slices.Equal(m.inspectionView().Overlay, []string{"loss"}) || !m.inspectionView().ExpandedChart {
		t.Fatal("enter did not apply overlay")
	}
	applyOverlayForTest(m, "missing1", "missing2", "missing3", "missing4")
	key(m, "p")
	m.toggleMetricOverlay("loss")
	if len(m.inspect.OverlayDraft) != 4 || !strings.Contains(m.status, "at most four") {
		t.Fatal("missing selected keys stopped counting toward limit")
	}
	m.toggleMetricOverlay("missing2")
	m.toggleMetricOverlay("loss")
	key(m, "enter")
	if !slices.Equal(m.inspectionView().Overlay, []string{"missing1", "missing3", "missing4", "loss"}) {
		t.Fatal("missing-key removal changed selection order")
	}
}

func TestMetricPinsOrderAndFiltersAreIndependentFromOverlay(t *testing.T) {
	m := metricTestModel()
	m.run().Data.Metrics = []core.Metric{{Key: "a", Value: 1}, {Key: "z", Value: 2}, {Key: "system/cpu", Value: 3}}
	m.syncInspection()
	m.changeMetricPin("z", 0)
	m.changeMetricPin("a", 0)
	m.changeMetricPin("future", 0)
	if !slices.Equal(m.metricPins(m.run()), []string{"z", "a", "future"}) || m.inspectionView().Rows[0].Key != "z" {
		t.Fatal("pin insertion order was not applied")
	}
	m.inspectionView().Selected = "a"
	key(m, "<")
	if m.inspectionView().Rows[0].Key != "a" || !slices.Equal(m.metricPins(m.run()), []string{"a", "z", "future"}) {
		t.Fatal("pin reorder failed")
	}
	applyOverlayForTest(m, "a", "system/cpu", "future")
	key(m, "e")
	if len(m.inspectionView().Rows) != 1 || m.inspectionView().Rows[0].Key != "system/cpu" || len(m.desiredHistories()) != 2 {
		t.Fatal("scope either ignored system filter or deleted explicit overlay")
	}
	v := m.inspectionView()
	v.Query = "nothing matches"
	m.filterInspection(v)
	if len(v.Rows) != 0 || len(v.Overlay) != 3 || len(m.desiredHistories()) != 2 {
		t.Fatal("table search changed overlay selections")
	}
	m.selectRun(1)
	m.run().Data.Metrics = []core.Metric{{Key: "a"}, {Key: "z"}, {Key: "future"}}
	m.syncInspection()
	if got := m.inspectionView().Rows; len(got) != 3 || got[0].Key != "a" || got[1].Key != "z" || got[2].Key != "future" {
		t.Fatal("new run did not use pins, including a previously missing metric")
	}
}

func TestMetricPinsPersistWithoutOverwritingUnloadedExperimentView(t *testing.T) {
	m := metricTestModel()
	source := m.historyNamespace()
	db := localstate.New(t.TempDir() + "/state.db")
	defer db.Close()
	saved := core.DefaultView(nil, nil)
	saved.Columns[0].Width = 47
	saved.Filter = "tags.stage = 'review'"
	if err := db.SaveView(context.Background(), source, "e1", saved); err != nil {
		t.Fatal(err)
	}
	m.opts.State = db
	load := m.loadMetricPreferences()
	if load == nil {
		t.Fatal("no explicit experiment preference load")
	}
	m.changeMetricPin("loss", 0)
	metricCommandTree(m, load)
	got, found, err := db.LoadView(context.Background(), source, "e1")
	if err != nil || !found || got.Columns[0].Width != 47 || got.Filter != saved.Filter || !slices.Equal(got.MetricPins, []string{"loss"}) {
		t.Fatalf("pin edit overwrote unloaded view: %+v, %v", got, err)
	}
	applyOverlayForTest(m, "loss")
	restarted := metricTestModel()
	restarted.opts.State = db
	metricCommandTree(restarted, restarted.loadMetricPreferences())
	if !slices.Equal(restarted.metricPins(restarted.run()), []string{"loss"}) || len(restarted.inspectionView().Overlay) != 0 || restarted.inspectionView().ExpandedChart {
		t.Fatal("restart did not preserve only durable pins")
	}
}

func TestMetricPreferencesUseActualRunExperiment(t *testing.T) {
	m := metricTestModel()
	db := &memoryState{}
	m.opts.State = db
	// A synthetic Activity list may contain a run from an experiment other than
	// the sidebar selection. Preferences must follow the run's real identity.
	m.run().Info.ExperimentID = "actual"
	m.syncInspection()
	metricCommandTree(m, m.changeMetricPin("loss", 0))
	if _, ok := db.views[m.historyNamespace()+"/actual"]; !ok {
		t.Fatal("pin saved under sidebar experiment instead of run experiment")
	}
	if _, ok := db.views[m.historyNamespace()+"/e1"]; ok {
		t.Fatal("pin leaked into the selected experiment view")
	}
}

func TestMetricPinWriteSurvivesQuitBeforePreferenceLoad(t *testing.T) {
	m := metricTestModel()
	source := m.historyNamespace()
	saved := core.DefaultView(nil, nil)
	saved.Columns[0].Width = 61
	db := &memoryState{views: map[string]core.ExperimentView{source + "/e1": saved}}
	m.opts.State = db
	_ = m.changeMetricPin("loss", 0) // Quit before any returned effects run.
	if err := m.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := db.views[source+"/e1"]
	if got.Columns[0].Width != 61 || !slices.Equal(got.MetricPins, []string{"loss"}) {
		t.Fatal("final flush lost a pending pin or overwrote unloaded preferences")
	}
}

func TestMetricRefreshUpdatesActivityHydratedRun(t *testing.T) {
	m := metricTestModel()
	db := localstate.New(t.TempDir() + "/state.db")
	defer db.Close()
	m.opts.State = db
	a := m.activityState()
	a.Scope = scopeRunning
	run := sampleRun("activity-only")
	run.Info.ExperimentID = "outside"
	a.Views[scopeRunning].Rows = []core.Run{run}
	a.Views[scopeRunning].Selected = run.ID()
	a.Runs[run.ID()] = &run
	a.Inspect = &run
	m.syncInspection()
	applyOverlayForTest(m, "loss", "valid_corr")
	updated := run
	updated.Data.Metrics = append(slices.Clone(run.Data.Metrics), core.Metric{Key: "valid_corr", Value: .9})
	m.inspect.RunGen, m.inspect.RunPending = 7, true
	m.acceptInspectionRuns(inspectionRunsMsg{Scope: m.inspectionRunScope(), Target: m.active, Gen: 7, Runs: []core.Run{updated}})
	if _, ok := m.run().Metric("valid_corr"); !ok || a.Inspect != m.run() {
		t.Fatal("metric refresh left Activity's hydrated detail cache stale")
	}
	if len(m.desiredHistories()) != 2 {
		t.Fatal("new metric was not added to the Activity overlay")
	}
}
