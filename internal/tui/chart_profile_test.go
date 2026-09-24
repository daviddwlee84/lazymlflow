package tui

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

func TestChartEMAUsesFullHistoryAndResetsAcrossGaps(t *testing.T) {
	raw := []core.Metric{{Step: 0, Timestamp: 3000, Value: 0}, {Step: 1, Timestamp: 1000, Value: 10}, {Step: 1, Timestamp: 2000, Value: 20}, {Step: 2, Value: core.Number(math.NaN())}, {Step: 3, Value: 10}, {Step: 4, Value: core.Number(math.Inf(1))}, {Step: 5, Value: -10}}
	before, _ := json.Marshal(raw)
	d, err := transformChart(context.Background(), raw, core.SummarizeHistory(raw), 0, chartProfile{Smooth: true, Span: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range map[int]float64{0: 0, 1: 5, 2: 12.5, 4: 10, 6: -10} {
		if d.Values[i] != want {
			t.Fatalf("EMA sample %d=%g want %g", i, d.Values[i], want)
		}
	}
	if !math.IsNaN(d.Values[3]) || !math.IsInf(d.Values[5], 1) {
		t.Fatal("nonfinite samples changed")
	}
	if got, _ := json.Marshal(raw); string(got) != string(before) {
		t.Fatal("transformation changed raw history")
	}
	// Time ordering reuses the same derived value at each original timestamp.
	var valueAtThree float64
	for _, point := range d.Time {
		if point.X == 3 {
			valueAtThree = point.Y
		}
	}
	if valueAtThree != 0 {
		t.Fatal("elapsed axis recomputed EMA in timestamp order")
	}
	long := make([]core.Metric, 5000)
	expected := 0.0
	for i := range long {
		value := math.Sin(float64(i) / 17)
		long[i] = core.Metric{Step: int64(i), Value: core.Number(value)}
		if i == 0 {
			expected = value
		} else {
			expected = (2.0/11)*value + (9.0/11)*expected
		}
	}
	d, err = transformChart(context.Background(), long, core.SummarizeHistory(long), 0, chartProfile{Smooth: true, Span: 10})
	if err != nil || len(d.Values) != 5000 || math.Abs(d.Values[4999]-expected) > 1e-12 || len(d.Step) > 2401 {
		t.Fatal("EMA was sampled before transformation", err)
	}
}

func TestChartNormalizationConstantExtremeAndCancellation(t *testing.T) {
	for _, raw := range [][]core.Metric{{{Value: 7}, {Step: 1, Value: 7}}, {{Value: core.Number(-math.MaxFloat64)}, {Step: 1, Value: 0}, {Step: 2, Value: core.Number(math.MaxFloat64)}}} {
		d, err := transformChart(context.Background(), raw, core.SummarizeHistory(raw), 0, chartProfile{Normalize: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range d.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
				t.Fatalf("normalized %g", v)
			}
		}
		if len(raw) == 2 && !slices.Equal(d.Values, []float64{.5, .5}) {
			t.Fatal("constant normalization did not use .5")
		}
	}
	raw := []core.Metric{{Value: core.Number(math.MaxFloat64)}, {Step: 1, Value: core.Number(math.MaxFloat64)}, {Step: 2, Value: core.Number(-math.MaxFloat64)}}
	d, err := transformChart(context.Background(), raw, core.SummarizeHistory(raw), 0, chartProfile{Smooth: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range d.Values {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			t.Fatal("EMA overflowed a finite convex combination")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transformChart(ctx, raw, core.SummarizeHistory(raw), 0, chartProfile{}); err != context.Canceled {
		t.Fatal("cancellation ignored", err)
	}
}

func TestChartRawExtremaTiesAndDistance(t *testing.T) {
	raw := []core.Metric{{Key: "loss", Step: 3, Timestamp: 3000, Value: 1}, {Key: "loss", Step: 1, Timestamp: 1000, Value: 1}, {Key: "loss", Step: 2, Timestamp: 2000, Value: 4}, {Key: "loss", Step: 5, Timestamp: 5000, Value: core.Number(math.NaN())}}
	summary := core.SummarizeHistory(raw)
	if summary.Min.Step != 1 || summary.Max.Step != 2 {
		t.Fatal("extrema ties depend on API order")
	}
	stats := deriveExtrema(core.SampleHistory(raw, 0), summary)
	if stats.Min.Steps != 4 || stats.Min.Samples != 3 || stats.Min.Elapsed != 4*time.Second {
		t.Fatalf("wrong per-metric raw end distance: %+v", stats.Min)
	}
	if stats.Max.Steps != 3 || stats.Max.Samples != 2 {
		t.Fatal("maximum distance wrong")
	}
	missing := []core.Metric{{Step: 0, Value: 1}, {Step: 1, Value: 2}}
	unknown := deriveExtrema(missing, core.SummarizeHistory(missing)).Min
	if unknown.TimeKnown || !strings.Contains(distanceText("Min", unknown), " / —") {
		t.Fatal("missing timestamps rendered as elapsed zero")
	}
	extreme := []core.Metric{{Step: math.MinInt64, Timestamp: 1, Value: 1}, {Step: math.MaxInt64, Timestamp: math.MaxInt64, Value: 2}}
	wide := deriveExtrema(extreme, core.SummarizeHistory(extreme)).Min
	if wide.Steps != math.MaxUint64 || !wide.TimeKnown || strings.Contains(distanceText("Min", wide), "Δ-") {
		t.Fatal("distance arithmetic overflowed")
	}
}

func TestChartPointsLinesMarkersAndNormalizedAxis(t *testing.T) {
	series := []curveSeries{{Name: "loss", Points: []curvePoint{{X: 0, Y: 0}, {X: 10, Y: 10}}, Raw: []curvePoint{{X: 0, Y: 0}, {X: 10, Y: 10}}}}
	points := ansi.Strip(strings.Join(renderCurvesStyled(series, 60, 12, true, nil, curveRenderOptions{Draw: "points"}), "\n"))
	both := ansi.Strip(strings.Join(renderCurvesStyled(series, 60, 12, true, nil, curveRenderOptions{Draw: "both"}), "\n"))
	if strings.Contains(points, "...") || !strings.Contains(both, "...") || !strings.Contains(both, "1") {
		t.Fatal("raw samples and connecting lines were not distinguished")
	}
	series[0].Markers = []curveMarker{{curvePoint{X: 0, Y: 0}, 'v'}, {curvePoint{X: 10, Y: 10}, '^'}}
	marked := ansi.Strip(strings.Join(renderCurvesStyled(series, 60, 12, true, nil, curveRenderOptions{Draw: "both"}), "\n"))
	if !strings.Contains(marked, "v") || !strings.Contains(marked, "^") {
		t.Fatal("raw extrema markers missing")
	}
	series = append(series, series[0])
	overlap := ansi.Strip(strings.Join(renderCurvesStyled(series, 60, 12, true, nil, curveRenderOptions{Draw: "both"}), "\n"))
	if !strings.Contains(overlap, "v") || !strings.Contains(overlap, "^") {
		t.Fatal("coincident series hid extrema under raw point markers")
	}
	constant := []curveSeries{{Points: []curvePoint{{X: 0, Y: .5}, {X: 10, Y: .5}}}}
	plot := ansi.Strip(strings.Join(renderCurvesStyled(constant, 60, 12, false, nil, curveRenderOptions{Draw: "lines", Normalize: true}), "\n"))
	if !strings.HasPrefix(plot, "1 ") {
		t.Fatal("normalized constant chart did not retain 0–1 axis")
	}
}

func TestActivityChartContextInheritsOnceAndRestoresExperiment(t *testing.T) {
	m := metricTestModel()
	db := localstate.New(t.TempDir() + "/state.db")
	defer db.Close()
	m.opts.State = db
	applyOverlayForTest(m, "loss", "valid_corr")
	base := m.metricPreferences(m.run())
	base.Chart.Normalize = true
	a := m.activityState()
	a.Scope = scopeRunning
	first := sampleRun("running-one")
	first.Info.ExperimentID = "e2"
	second := sampleRun("running-two")
	a.Views[scopeRunning].Rows = []core.Run{first, second}
	a.Views[scopeRunning].Selected = first.ID()
	m.syncInspection()
	if !slices.Equal(m.inspectionView().Overlay, base.Overlay) || !m.currentChartProfile().Normalize {
		t.Fatal("Activity was not seeded from first experiment")
	}
	key(m, "F")
	a.Views[scopeRunning].Selected = second.ID()
	m.syncInspection()
	if !m.currentChartProfile().Smooth || !slices.Equal(m.inspectionView().Overlay, []string{"loss", "valid_corr"}) {
		t.Fatal("cross-experiment Activity browsing lost profile")
	}
	key(m, "0")
	if len(m.inspectionView().Overlay) != 0 || len(m.chartPreferences(m.run()).Overlay) != 2 {
		t.Fatal("overlay off cleared keys")
	}
	key(m, "0")
	if len(m.inspectionView().Overlay) != 2 {
		t.Fatal("overlay on did not restore keys")
	}
	a.Scope = scopeRecent
	a.Views[scopeRecent].Rows = []core.Run{second}
	a.Views[scopeRecent].Selected = second.ID()
	m.syncInspection()
	if m.currentChartProfile().Smooth || !slices.Equal(m.inspectionView().Overlay, base.Overlay) {
		t.Fatal("Activity lists leaked profiles")
	}
	a.Scope = scopeExperiment
	m.syncInspection()
	if m.currentChartProfile().Smooth || !m.currentChartProfile().Normalize || !slices.Equal(m.inspectionView().Overlay, base.Overlay) {
		t.Fatal("Activity overwrote real experiment profile")
	}
}

func TestChartOptionsClearDraftAndNoKeyConflicts(t *testing.T) {
	m := metricTestModel()
	applyOverlayForTest(m, "loss", "future")
	key(m, "p")
	key(m, "0")
	key(m, "esc")
	if len(m.inspectionView().Overlay) != 2 {
		t.Fatal("cancel after draft clear changed live overlay")
	}
	key(m, "p")
	key(m, "0")
	key(m, "enter")
	if len(m.inspectionView().Overlay) != 0 {
		t.Fatal("applying cleared overlay failed")
	}
	key(m, "f")
	key(m, "down")
	key(m, "right")
	key(m, "esc")
	if m.currentChartProfile().span() != 11 {
		t.Fatal("EMA span not configurable")
	}
	key(m, "F")
	key(m, "n")
	key(m, "d")
	key(m, "b")
	p := m.currentChartProfile()
	if !p.Smooth || !p.Normalize || !p.Extrema || p.draw() != "points" {
		t.Fatalf("chart controls: %+v", p)
	}
	for _, compare := range []bool{false, true} {
		m.compare = compare
		m.chart = true
		if compare {
			m.focus = 1
		}
		seen := map[string]string{}
		for _, a := range m.actions() {
			for _, key := range a.Keys {
				if key == "" {
					continue
				}
				if prior, ok := seen[key]; ok {
					t.Fatalf("%s conflicts: %s/%s", key, prior, a.ID)
				}
				seen[key] = a.ID
			}
		}
		if compare && seen["F"] != "chart-smooth" {
			t.Fatal("full-screen comparison chart hid chart controls behind stored focus")
		}
		if compare && !strings.Contains(m.inspectionFooter(), "F EMA") {
			t.Fatal("comparison chart controls missing from footer")
		}
	}
}

func TestChartTransformRejectsStaleOptionsAndReusesCache(t *testing.T) {
	m := metricTestModel()
	run := m.run()
	raw := []core.Metric{{Key: "loss", Value: 0}, {Key: "loss", Step: 1, Value: 10}}
	key := historyCacheKey(m.historyNamespace(), run.ID(), "loss")
	e := &historyEntry{Run: run.ID(), Metric: "loss", History: raw, Summary: core.SummarizeHistory(raw), Updated: time.Now(), Revision: 1}
	m.inspect.Histories[key] = e
	p := m.chartPreferences(run)
	p.Chart.Smooth = true
	first := m.ensureChartTransforms()
	if first == nil {
		t.Fatal("no background transform")
	}
	oldTicket := e.TransformTicket
	p.Chart.Normalize = true
	second := m.ensureChartTransforms()
	m.acceptChartTransform(chartTransformedMsg{Source: m.historyNamespace(), Key: key, Ticket: oldTicket, Derived: &chartDerived{Key: "old"}})
	if e.Derived != nil || !e.TransformPending {
		t.Fatal("stale options completion applied")
	}
	metricCommandTree(m, second)
	if e.Derived == nil || e.Derived.Key != chartTransformKey(1, p.Chart) || m.ensureChartTransforms() != nil {
		t.Fatal("derived cache not reused")
	}
	if e.History[1].Value != 10 {
		t.Fatal("raw cache mutated")
	}
}
