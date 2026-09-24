package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func runInspectionCommands(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runInspectionCommands(m, c)
		}
		return
	}
	_, next := m.Update(msg)
	if _, ok := msg.(metricLoadedMsg); ok {
		runInspectionCommands(m, next)
	}
}
func TestDetailTablesSearchSortPreserveAndOwnTyping(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 2
	r := m.run()
	r.Data.Params = []core.KeyValue{{Key: "optimizer", Value: "adam"}, {Key: "learning rate", Value: "0.1"}, {Key: "sequence", Value: "qjS"}}
	m.syncInspection()
	if v := m.inspectionView(); v.Rows[0].Key != "learning rate" {
		t.Fatalf("unordered %+v", v.Rows)
	}
	key(m, "/")
	key(m, "q")
	key(m, "j")
	key(m, "S")
	if m.overlay != "inspect-search" || m.work.modal != "" {
		t.Fatal("typing leaked shortcut")
	}
	if v := m.inspectionView(); v.Query != "qjS" || len(v.Rows) != 1 {
		t.Fatalf("bad search %+v", v)
	}
	key(m, "enter")
	if m.overlay != "" {
		t.Fatal("enter did not accept search")
	}
	key(m, "enter")
	if m.overlay != "inspect-value" || !strings.Contains(m.inspect.Value, "qjS") {
		t.Fatal("value expansion")
	}
	key(m, "esc")
	key(m, "]")
	key(m, "[")
	if m.inspectionView().Query != "qjS" {
		t.Fatal("tab lost query")
	}
	key(m, "/")
	key(m, "esc")
	if len(m.inspectionView().Rows) != 3 {
		t.Fatal("escape did not clear filter")
	}
}
func TestDatasetSchemaCountsSearchNestedAndUnknown(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 5
	var fields []string
	for i := 0; i < 146; i++ {
		fields = append(fields, fmt.Sprintf(`{"name":"feature_%d","type":"double","required":true}`, i))
	}
	r := m.run()
	r.Inputs.DatasetInputs = []core.DatasetInput{{Dataset: core.Dataset{Name: "valid", Schema: `{"mlflow_colspec":[` + strings.Join(fields, ",") + `]}`, Profile: `{"num_rows":4558}`}}}
	r.Data.Params = append(r.Data.Params, core.KeyValue{Key: "feature_dim", Value: "146"})
	m.syncInspection()
	view := strings.Join(m.detailLines(100, 30), "\n")
	for _, want := range []string{"146 columns", "4,558 logged rows", "feature_dim=146"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %s in %s", want, view)
		}
	}
	v := m.inspectionView()
	v.Query = "feature_145"
	m.filterInspection(v)
	if len(v.Rows) != 1 || v.Schema.Columns != 146 {
		t.Fatal("filter changed schema totals")
	}
	r.Inputs.DatasetInputs[0].Dataset.Schema = `{"mlflow_colspec":[{"name":"meta","type":{"type":"object","properties":{"id":{"type":"long"}}}}]}`
	v.Query = ""
	v.Run = nil
	m.syncInspection()
	if len(v.Rows) != 1 {
		t.Fatal("nested schema not collapsed")
	}
	m.performInspection("inspect-expand")
	if len(v.Rows) != 2 {
		t.Fatal("nested field not expanded")
	}
	r.Inputs.DatasetInputs[0].Dataset.Schema = "unsupported"
	v.Run = nil
	m.syncInspection()
	if lines := strings.Join(m.detailLines(70, 20), "\n"); !strings.Contains(lines, "unsupported") {
		t.Fatal("unknown schema raw fallback missing")
	}
}
func TestMetricHistoryPreparedCacheRefreshAndCursor(t *testing.T) {
	m, b := readyModel()
	m.focus = 2
	m.tab = 1
	b.history = []core.Metric{{Step: 5, Timestamp: 4000, Value: 2}, {Step: 1, Timestamp: 2000, Value: 1}, {Step: 1, Timestamp: 2500, Value: 3}, {Step: 2, Timestamp: 3000, Value: core.Number(math.NaN())}}
	m.syncInspection()
	runInspectionCommands(m, m.ensureHistories(false))
	entry := m.historyEntryFor("r1", "loss")
	if entry == nil || entry.Summary.Count != 4 || len(entry.History) != 4 || entry.History[0].Step != 1 {
		t.Fatalf("history not prepared: %+v", entry)
	}
	before := append([]curvePoint(nil), entry.StepPoints...)
	m.performInspection("enter")
	for _, size := range [][2]int{{160, 45}, {80, 24}, {30, 8}} {
		m.width, m.height = size[0], size[1]
		lines := m.detailLines(size[0]-2, size[1]-6)
		for _, line := range lines {
			if strings.Contains(line, "\x1b]52") {
				t.Fatal("unexpected terminal sequence")
			}
		}
	}
	if len(before) != len(entry.StepPoints) {
		t.Fatal("render mutated data")
	}
	for i, p := range before {
		after := entry.StepPoints[i]
		if p.X != after.X || p.Gap != after.Gap || !p.Gap && p.Y != after.Y {
			t.Fatal("render mutated data")
		}
	}
	m.inspectionView().Cursor = 1
	m.moveChartCursor(1)
	if m.inspectionView().Cursor != 2 {
		t.Fatal("cursor did not move")
	}
	m.elapsed = true
	m.setChartCursorFraction(0)
	if m.inspectionView().Cursor != 0 {
		t.Fatal("cursor elapsed coordinate mismatch")
	}
	oldGen := entry.Gen
	m.ensureHistories(true)
	m.acceptMetric(metricLoadedMsg{Target: m.active, Key: historyCacheKey(m.historyNamespace(), "r1", "loss"), Gen: oldGen, Err: fmt.Errorf("stale")})
	if entry.Err != "" || !entry.Pending {
		t.Fatal("stale error changed active request")
	}
}
func TestCurveRendererContinuousGapAndASCII(t *testing.T) {
	series := []curveSeries{{Name: "loss", Points: []curvePoint{{X: 0, Y: 0}, {X: 10, Y: 10}}}}
	out := ansi.Strip(strings.Join(renderCurves(series, 60, 12, false), "\n"))
	dots := 0
	for _, r := range out {
		if r >= 0x2800 && r <= 0x28ff {
			dots++
		}
	}
	if dots < 20 {
		t.Fatalf("curve disconnected, dots=%d", dots)
	}
	ascii := ansi.Strip(strings.Join(renderCurves(series, 60, 12, true), "\n"))
	if strings.ContainsRune(ascii, '│') || !strings.Contains(ascii, "111") {
		t.Fatal("ASCII fallback missing")
	}
	gap := []curveSeries{{Name: "loss", Points: []curvePoint{{X: 0, Y: 0}, {X: 5, Y: math.NaN(), Gap: true}, {X: 10, Y: 10}}}}
	dots = 0
	for _, r := range ansi.Strip(strings.Join(renderCurves(gap, 60, 12, false), "\n")) {
		if r >= 0x2800 && r <= 0x28ff {
			dots++
		}
	}
	if dots != 2 {
		t.Fatalf("connected over nonfinite gap: %d", dots)
	}
	if len(renderCurves(series, 1, 1, false)) != 1 {
		t.Fatal("tiny resize failed")
	}
}

type concurrentHistoryBackend struct {
	*fakeBackend
	active, max atomic.Int32
	release     chan struct{}
}

func (b *concurrentHistoryBackend) MetricHistory(ctx context.Context, run, metric string) ([]core.Metric, error) {
	n := b.active.Add(1)
	defer b.active.Add(-1)
	for {
		old := b.max.Load()
		if n <= old || b.max.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-b.release:
		return []core.Metric{{Key: metric, Value: 1}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestHistoriesBoundConcurrencyAndRetainActive(t *testing.T) {
	m, _ := readyModel()
	m.width, m.height = 180, 70
	m.focus = 2
	m.tab = 1
	r := m.run()
	r.Data.Metrics = nil
	for i := 0; i < 9; i++ {
		r.Data.Metrics = append(r.Data.Metrics, core.Metric{Key: fmt.Sprintf("metric%d", i)})
	}
	m.syncInspection()
	m.inspectionView().Dashboard = true
	b := &concurrentHistoryBackend{fakeBackend: &fakeBackend{}, release: make(chan struct{})}
	m.state().Session.Backend = b
	cmd := m.ensureHistories(false)
	msg := cmd()
	cmds, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected concurrent commands, got %T", msg)
	}
	results := make(chan tea.Msg, len(cmds))
	var wg sync.WaitGroup
	for _, c := range cmds {
		wg.Add(1)
		go func(c tea.Cmd) { defer wg.Done(); results <- c() }(c)
	}
	deadline := time.After(time.Second)
	for b.active.Load() < 4 {
		select {
		case <-deadline:
			t.Fatal("workers did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if b.max.Load() != 4 {
		t.Fatalf("concurrency=%d", b.max.Load())
	}
	close(b.release)
	wg.Wait()
	close(results)
	for msg := range results {
		m.Update(msg)
	}
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("inactive-%d", i)
		m.inspect.Histories[key] = &historyEntry{Used: uint64(i)}
	}
	m.evictHistories(m.desiredHistories())
	if len(m.inspect.Histories) > 16 {
		t.Fatalf("cache retained %d entries", len(m.inspect.Histories))
	}
	if m.historyEntryFor(r.ID(), "metric0") == nil {
		t.Fatal("active history evicted")
	}
}
func TestInspectionCompletedRunFinalRefreshAndScope(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 1
	m.syncInspection()
	m.inspect.Auto = true
	m.inspect.RunGen = 3
	m.inspect.RunPending = true
	r := *m.run()
	r.Info.Status = "FINISHED"
	cmd := m.acceptInspectionRuns(inspectionRunsMsg{Scope: m.inspectionRunScope(), Target: m.active, Gen: 3, Runs: []core.Run{r}})
	if !m.inspect.Auto || cmd == nil || m.run().Info.Status != "FINISHED" {
		t.Fatal("finished run did not retain auto and refresh")
	}
	runInspectionCommands(m, cmd)
	entry := m.historyEntryFor("r1", "loss")
	gen := entry.Gen
	if m.pollInspection() != nil || entry.Gen != gen {
		t.Fatal("completed scope still polled")
	}
	m.selectRun(1)
	m.syncInspection()
	if m.pollInspection() == nil {
		t.Fatal("selecting running run did not resume polling")
	}
	m.inspect.RunGen = 4
	m.inspect.RunPending = true
	m.acceptInspectionRuns(inspectionRunsMsg{Scope: "old selection", Target: m.active, Gen: 4, Runs: []core.Run{sampleRun("r2")}})
	if m.inspect.RunPending {
		t.Fatal("stale selection left refresh pending")
	}
}

func TestMetricPickerOverlayAndSortBindings(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 1
	r := m.run()
	r.Data.Metrics = []core.Metric{{Key: "train_loss", Value: .9}, {Key: "valid_loss", Value: .4}, {Key: "system/cpu", Value: 3}}
	m.syncInspection()
	seen := map[string]string{}
	for _, a := range m.actions() {
		for _, k := range a.Keys {
			if prior, ok := seen[k]; ok {
				t.Fatalf("conflicting %s: %s/%s", k, prior, a.ID)
			}
			seen[k] = a.ID
		}
	}
	if len(m.inspectionView().Rows) != 2 {
		t.Fatal("model metrics scope includes system metrics")
	}
	key(m, "e")
	if len(m.inspectionView().Rows) != 1 || m.inspectionView().Rows[0].Key != "system/cpu" {
		t.Fatal("system metrics scope")
	}
	key(m, "e")
	if len(m.inspectionView().Rows) != 3 {
		t.Fatal("all metrics scope")
	}
	key(m, "s")
	key(m, "down")
	key(m, "down")
	key(m, "enter")
	if m.inspectionView().Rows[0].Key != "valid_loss" {
		t.Fatal("numeric ascending table sort")
	}
	key(m, "m")
	key(m, "/")
	key(m, "train")
	key(m, "enter")
	key(m, "enter")
	if m.metric != "train_loss" || !m.inspectionView().ExpandedChart {
		t.Fatal("metric picker failed")
	}
	key(m, "p")
	key(m, "space")
	key(m, "down")
	key(m, "space")
	key(m, "enter")
	if len(m.inspectionView().Overlay) != 2 {
		t.Fatal("overlay selections lost")
	}
}
func TestHistoryRefreshQueuedWhilePendingAndSourceIsolation(t *testing.T) {
	m, b := readyModel()
	m.focus = 2
	m.tab = 1
	b.history = []core.Metric{{Value: 2}}
	m.syncInspection()
	first := m.ensureHistories(false)
	m.ensureHistories(true)
	entry := m.historyEntryFor("r1", "loss")
	if !entry.NeedsRefresh {
		t.Fatal("lost requested final refresh")
	}
	runInspectionCommands(m, first)
	if entry.NeedsRefresh || entry.Pending || entry.Updated.IsZero() {
		t.Fatal("queued refresh did not finish")
	}
	prior := m.historyNamespace()
	m.targets[0].TrackingURI = "http://other-host"
	m.syncInspection()
	if prior == m.historyNamespace() || m.historyEntryFor("r1", "loss") != nil {
		t.Fatal("cache leaked across target endpoint edit")
	}
}
func BenchmarkMetricChartPreparedHistory(b *testing.B) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 1
	m.width, m.height = 160, 50
	m.syncInspection()
	v := m.inspectionView()
	v.ExpandedChart = true
	raw := make([]core.Metric, 100000)
	for i := range raw {
		raw[i] = core.Metric{Step: int64(i), Timestamp: int64(i) * 1000, Value: core.Number(math.Sin(float64(i) / 400))}
	}
	e := &historyEntry{Run: "r1", Metric: "loss", History: raw, StepPoints: prepareCurve(core.SampleHistory(raw, 2400), 0, false), Summary: core.SummarizeHistory(raw), Updated: time.Now()}
	m.inspect.Histories[historyCacheKey(m.historyNamespace(), "r1", "loss")] = e
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.detailLines(158, 44)
	}
}

func TestInspectionMousePickerAndSemanticRows(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 1
	m.run().Data.Metrics = append(m.run().Data.Metrics, core.Metric{Key: "valid_loss", Value: .5})
	m.syncInspection()
	key(m, "p")
	mouseClick(m, 12, 3)
	if len(m.inspect.OverlayDraft) != 1 || m.inspect.OverlayDraft[0] != "loss" || len(m.inspectionView().Overlay) != 0 {
		t.Fatal("mouse checkbox did not edit the overlay draft")
	}
	mouseClick(m, 0, 2)
	if m.overlay != "inspect-overlay" {
		t.Fatal("outside click fell through modal")
	}
	key(m, "esc")
	m.activateInspectionHit("inspect-row:valid_loss")
	if m.inspectionView().Selected != "valid_loss" {
		t.Fatal("row click did not preserve semantic identity")
	}
	m.inspectionView().Sort = 1
	m.filterInspection(m.inspectionView())
	m.activateInspectionHit("inspect-row:loss")
	if m.inspectionView().Selected != "loss" {
		t.Fatal("row click used stale row index")
	}
	key(m, "s")
	mouseClick(m, 12, 4)
	if m.overlay != "" || m.inspectionView().Sort != 2 {
		t.Fatal("mouse sort picker failed")
	}
}

func TestHistoriesOnlyForVisibleCurves(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 1
	m.width, m.height = 160, 40
	m.syncInspection()
	if len(m.desiredHistories()) != 1 {
		t.Fatal("visible preview missing")
	}
	m.zoom = true
	m.focus = 1
	if len(m.desiredHistories()) != 0 {
		t.Fatal("hidden detail pane fetched history")
	}
	m.focus = 2
	m.work.modal = "notes"
	if len(m.desiredHistories()) != 0 {
		t.Fatal("workspace modal fetched history")
	}
	m.work.modal = ""
	m.work.catalog = true
	if len(m.desiredHistories()) != 0 {
		t.Fatal("workspace fetched run history")
	}
	m.work.catalog = false
	m.overlay = "inspect-metric"
	if len(m.desiredHistories()) != 0 {
		t.Fatal("picker fetched background history")
	}
	m.overlay = ""
	m.height = 8
	if len(m.desiredHistories()) != 0 {
		t.Fatal("table without preview fetched history")
	}
	m.height = 40
	r := m.run()
	r.Data.Metrics = nil
	for i := 0; i < 20; i++ {
		r.Data.Metrics = append(r.Data.Metrics, core.Metric{Key: fmt.Sprintf("metric%02d", i)})
	}
	v := m.inspectionView()
	v.Run = nil
	m.syncInspection()
	v.Dashboard = true
	v.Index = 19
	v.Selected = "metric00"
	visible := m.dashboardRows(v, m.geometry().Panes[2].W-2, m.geometry().Panes[2].H-2)
	desired := m.desiredHistories()
	if len(desired) != len(visible) {
		t.Fatalf("dashboard fetched hidden metrics %d != %d", len(desired), len(visible))
	}
	if _, ok := desired[historyCacheKey(m.historyNamespace(), r.ID(), "metric00")]; ok {
		t.Fatal("dashboard fetched stale offpage selection")
	}
}
func TestCurvesExtremeFiniteRangeDoesNotOverflow(t *testing.T) {
	if f := curveFraction(0, -math.MaxFloat64, math.MaxFloat64); f != .5 {
		t.Fatalf("extreme fraction=%v", f)
	}
	for _, value := range []float64{-math.MaxFloat64, 0, math.MaxFloat64} {
		f := curveFraction(value, -math.MaxFloat64, math.MaxFloat64)
		if math.IsInf(f, 0) || math.IsNaN(f) || f < 0 || f > 1 {
			t.Fatalf("invalid coordinate %v", f)
		}
	}
	series := []curveSeries{{Points: []curvePoint{{X: 0, Y: -math.MaxFloat64}, {X: 1, Y: 0}, {X: 2, Y: math.MaxFloat64}}}}
	out := renderCurves(series, 70, 12, false)
	for _, index := range []int{0, 10} {
		found := false
		for _, r := range ansi.Strip(out[index]) {
			if r >= 0x2800 && r <= 0x28ff {
				found = true
			}
		}
		if !found {
			t.Fatalf("extreme curve collapsed: %q", out[index])
		}
	}
	if lines := renderCurves([]curveSeries{{Points: []curvePoint{{X: 0, Y: math.MaxFloat64}, {X: 1, Y: math.MaxFloat64}}}}, 60, 12, false); strings.Contains(strings.Join(lines, ""), "Inf") {
		t.Fatal("constant max value overflowed axis")
	}
}
func TestSampledCurvesPreserveOmittedNonfiniteGapsOnBothAxes(t *testing.T) {
	var raw []core.Metric
	for i := 0; i < 100; i++ {
		v := core.Number(i)
		if i%2 == 1 {
			v = core.Number(math.NaN())
		}
		raw = append(raw, core.Metric{Step: int64(i), Timestamp: int64(100-i) * 1000, Value: v})
	}
	samples := core.SampleHistory(raw, 12)
	order := make([]int, len(raw))
	for i := range order {
		order[i] = len(raw) - 1 - i
	}
	for _, elapsed := range []bool{false, true} {
		indices := []int(nil)
		if elapsed {
			indices = order
		}
		points := prepareSampledCurve(raw, samples, indices, 0, elapsed)
		previous := false
		finite := 0
		for _, p := range points {
			if p.Gap {
				previous = false
				continue
			}
			finite++
			if previous {
				t.Fatal("downsampling connected across a skipped nonfinite sample")
			}
			previous = true
		}
		if finite < 2 {
			t.Fatal("lost finite sample selection")
		}
	}
}
