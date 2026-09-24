package tui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type curvePoint struct {
	X, Y float64
	Gap  bool
}
type curveSeries struct {
	Name    string
	Points  []curvePoint
	Raw     []curvePoint
	Markers []curveMarker
	Slot    int // one-based stable overlay slot; zero uses series position
}
type curveMarker struct {
	Point curvePoint
	Glyph rune
}
type curveRenderOptions struct {
	Draw      string
	Normalize bool
}
type historyEntry struct {
	Target, Run, Metric    string
	StartTime              int64
	Gen                    uint64
	Pending                bool
	NeedsRefresh           bool
	Err                    string
	Updated                time.Time
	Used                   uint64
	Bytes                  int
	History                []core.Metric
	TimeOrder              []int
	StepPoints, TimePoints []curvePoint
	Summary                core.MetricSummary
	Cancel                 context.CancelFunc
	Revision               uint64
	Extrema                chartExtrema
	Derived                *chartDerived
	TransformKey           string
	TransformTicket        uint64
	TransformPending       bool
	TransformCancel        context.CancelFunc
}
type metricLoadedMsg struct {
	Target, Key   string
	Gen           uint64
	Bytes         int
	History       []core.Metric
	TimeOrder     []int
	Step, Elapsed []curvePoint
	Summary       core.MetricSummary
	Extrema       chartExtrema
	Err           error
}

func historyCacheKey(target, run, metric string) string {
	return target + "\x00" + run + "\x00" + metric
}
func (m *model) desiredHistories() map[string]struct {
	Run    core.Run
	Metric string
} {
	out := map[string]struct {
		Run    core.Run
		Metric string
	}{}
	if m.inspect == nil {
		return out
	}
	if m.targetForm != nil || m.overlay != "" || m.work != nil && (m.work.catalog || m.work.modal != "") {
		return out
	}
	if m.compare {
		if !m.chart || m.metric == "" {
			return out
		}
		for _, r := range m.selectedRuns() {
			out[historyCacheKey(m.historyNamespace(), r.ID(), m.metric)] = struct {
				Run    core.Run
				Metric string
			}{r, m.metric}
		}
		return out
	}
	if m.tab != 1 {
		return out
	}
	pane := m.geometry().Panes[2]
	if pane.W <= 2 || pane.H <= 2 {
		return out
	}
	v := m.inspectionView()
	r := m.run()
	if v == nil || r == nil {
		return out
	}
	var keys []string
	if v.ExpandedChart {
		keys = []string{v.Selected}
		if len(v.Overlay) > 0 {
			keys = append([]string(nil), v.Overlay...)
		}
	} else if v.Dashboard {
		for _, row := range m.dashboardRows(v, pane.W-2, pane.H-2) {
			keys = append(keys, row.Key)
		}
	} else if metricPreviewVisible(v, pane.H-2) {
		keys = []string{v.Selected}
	}

	for _, key := range keys {
		_, exists := r.Metric(key)
		if key != "" && exists {
			out[historyCacheKey(m.historyNamespace(), r.ID(), key)] = struct {
				Run    core.Run
				Metric string
			}{*r, key}
		}
	}
	return out
}
func (m *model) ensureHistories(force bool) tea.Cmd {
	if m.inspect == nil {
		return nil
	}
	var preferences tea.Cmd
	if m.tab == 1 && !m.compare {
		preferences = m.loadMetricPreferences()
	}
	s := m.state()
	if s == nil || s.Session == nil {
		return preferences
	}
	desired := m.desiredHistories()
	for key, e := range m.inspect.Histories {
		if _, ok := desired[key]; !ok && e.Pending {
			if e.Cancel != nil {
				e.Cancel()
			}
			e.Gen++
			e.Pending = false
		}
	}
	cmds := []tea.Cmd{preferences}
	for key, wanted := range desired {
		e := m.inspect.Histories[key]
		if e == nil {
			e = &historyEntry{Target: m.active, Run: wanted.Run.ID(), Metric: wanted.Metric, StartTime: wanted.Run.Info.StartTime}
			m.inspect.Histories[key] = e
		}
		m.seq++
		e.Used = m.seq
		if e.Pending && force {
			e.NeedsRefresh = true
		}
		if e.Pending || !force && !e.NeedsRefresh && (!e.Updated.IsZero() || e.Err != "") {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		e.Cancel = cancel
		e.Gen++
		e.Pending = true
		e.NeedsRefresh = false
		e.Err = ""
		gen, target, b, run, metric, slots := e.Gen, m.active, s.Session.Backend, wanted.Run, wanted.Metric, m.inspect.Slots
		cmds = append(cmds, func() tea.Msg {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return metricLoadedMsg{Target: target, Key: key, Gen: gen, Err: ctx.Err()}
			}
			raw, err := b.MetricHistory(ctx, run.ID(), metric)
			if err != nil {
				return metricLoadedMsg{Target: target, Key: key, Gen: gen, Err: err}
			}
			summary := core.SummarizeHistory(raw)
			ordered := core.SampleHistory(raw, 0)
			sample := core.SampleHistory(ordered, 2400)
			step := prepareSampledCurve(ordered, sample, nil, run.Info.StartTime, false)
			order := make([]int, len(ordered))
			size := len(ordered)*144 + len(order)*8 + len(step)*24
			for i := range order {
				order[i] = i
				metric := ordered[i]
				size += len(metric.Key) + len(metric.ModelID) + len(metric.DatasetName) + len(metric.DatasetDigest)
			}
			sort.SliceStable(order, func(i, j int) bool { return ordered[order[i]].Timestamp < ordered[order[j]].Timestamp })
			elapsed := prepareSampledCurve(ordered, sample, order, run.Info.StartTime, true)
			size += len(elapsed) * 24
			return metricLoadedMsg{Target: target, Key: key, Gen: gen, Bytes: size, History: ordered, TimeOrder: order, Step: step, Elapsed: elapsed, Summary: summary, Extrema: deriveExtrema(ordered, summary)}
		})
	}
	m.evictHistories(desired)
	cmds = append(cmds, m.ensureChartTransforms())
	return tea.Batch(cmds...)
}
func prepareCurve(samples []core.Metric, start int64, elapsed bool) []curvePoint {
	samples = append([]core.Metric(nil), samples...)
	if elapsed {
		sort.SliceStable(samples, func(i, j int) bool { return samples[i].Timestamp < samples[j].Timestamp })
	}
	out := make([]curvePoint, 0, len(samples))
	for _, m := range samples {
		x := float64(m.Step)
		if elapsed {
			x = (float64(m.Timestamp) - float64(start)) / 1000
		}
		y := float64(m.Value)
		out = append(out, curvePoint{x, y, math.IsNaN(y) || math.IsInf(y, 0)})
	}
	return out
}
func (m *model) acceptMetric(v metricLoadedMsg) tea.Cmd {
	e := m.inspect.Histories[v.Key]
	if e == nil || e.Gen != v.Gen || !e.Pending || v.Target != m.active {
		return nil
	}
	e.Pending = false
	if e.Cancel != nil {
		e.Cancel()
		e.Cancel = nil
	}
	if v.Err != nil {
		e.Err = v.Err.Error()
		return nil
	}
	e.Err = ""
	e.Bytes = v.Bytes
	e.History = v.History
	e.TimeOrder = v.TimeOrder
	e.StepPoints = v.Step
	e.TimePoints = v.Elapsed
	e.Summary = v.Summary
	e.Extrema = v.Extrema
	e.Revision++
	e.Updated = time.Now()
	m.evictHistories(m.desiredHistories())
	return m.ensureHistories(false)
}
func (m *model) evictHistories(active map[string]struct {
	Run    core.Run
	Metric string
}) {
	type item struct {
		Key   string
		Used  uint64
		Bytes int
	}
	var inactive []item
	total := 0
	for k, e := range m.inspect.Histories {
		size := max(e.Bytes, len(e.History)*144+len(e.TimeOrder)*8+(len(e.StepPoints)+len(e.TimePoints))*24)
		if e.Derived != nil {
			size += e.Derived.Bytes
		}
		total += size
		if _, ok := active[k]; !ok && !e.Pending && !e.TransformPending {
			inactive = append(inactive, item{k, e.Used, size})
		}
	}
	sort.Slice(inactive, func(i, j int) bool { return inactive[i].Used < inactive[j].Used })
	for _, i := range inactive {
		if len(m.inspect.Histories) <= 16 && total <= 64<<20 {
			break
		}
		delete(m.inspect.Histories, i.Key)
		total -= i.Bytes
	}
}
func (m *model) stopInspection() {
	if m.inspect == nil {
		return
	}
	m.inspect.Auto = false
	m.inspect.PollGen++
	m.inspect.RunGen++
	m.inspect.RunPending = false
	for _, e := range m.inspect.Histories {
		if e.TransformCancel != nil {
			e.TransformCancel()
		}
		e.TransformTicket++
		e.TransformPending = false
		if e.Cancel != nil {
			e.Cancel()
		}
		e.Pending = false
		e.Gen++
	}
}

type inspectionRunsMsg struct {
	Scope  string
	Target string
	Gen    uint64
	Runs   []core.Run
	Err    error
}

func (m *model) refreshInspection() tea.Cmd {
	if m.inspect == nil || m.inspect.RunPending {
		return nil
	}
	s := m.state()
	runs := m.selectedRuns()
	if s == nil || s.Session == nil || len(runs) == 0 {
		return nil
	}
	m.inspect.RunGen++
	m.inspect.RunPending = true
	scope := m.inspectionRunScope()
	ctx, _ := m.operation("inspection-runs")
	gen, target, backend := m.inspect.RunGen, m.active, s.Session.Backend
	return func() tea.Msg {
		var out []core.Run
		for _, run := range runs {
			r, err := backend.GetRun(ctx, run.ID())
			if err != nil {
				return inspectionRunsMsg{scope, target, gen, out, err}
			}
			out = append(out, r)
		}
		return inspectionRunsMsg{scope, target, gen, out, nil}
	}
}
func (m *model) acceptInspectionRuns(v inspectionRunsMsg) tea.Cmd {
	if v.Target != m.active || v.Gen != m.inspect.RunGen {
		return nil
	}
	m.inspect.RunPending = false
	if v.Scope != m.inspectionRunScope() {
		return nil
	}
	if v.Err != nil {
		m.status = "Run refresh failed; retaining previous data: " + v.Err.Error()
		return nil
	}
	s := m.state()
	running := false
	m.rememberActivityMetadata(v.Runs)
	for _, r := range v.Runs {
		for cacheKey, e := range m.inspect.Histories {
			if e.Run == r.ID() && strings.HasPrefix(cacheKey, m.historyNamespace()+"\x00") {
				e.NeedsRefresh = true
			}
		}
		for _, view := range m.inspect.Views {
			if view.Run != nil && view.Run.ID() == r.ID() {
				view.Run = nil
			}
		}
		if !terminalRunStatus(r.Info.Status) {
			running = true
		}
		for _, rs := range s.Runs {
			for i := range rs.Rows {
				if rs.Rows[i].ID() == r.ID() {
					rs.Rows[i] = r
					rs.RowsVersion++
				}
			}
		}
		if _, ok := s.Basket[r.ID()]; ok {
			s.Basket[r.ID()] = r
		}
		if activity := m.activityCurrent(); activity != nil {
			if _, exists := activity.Runs[r.ID()]; exists || activity.Scope != scopeExperiment {
				updated := r
				activity.Runs[r.ID()] = &updated
				if activity.Inspect != nil && activity.Inspect.ID() == r.ID() {
					activity.Inspect = &updated
				}
			}
		}
	}
	if view := m.inspectionView(); view != nil {
		view.Run = nil
	}
	m.syncInspection()
	if m.inspect.Auto && !running {
		m.status = "Run finished; final metric update requested · auto refresh resumes for a running run"
	}
	return m.ensureHistories(true)
}
func (m *model) historyEntryFor(run, metric string) *historyEntry {
	if m.inspect == nil {
		return nil
	}
	return m.inspect.Histories[historyCacheKey(m.historyNamespace(), run, metric)]
}
func (m *model) chartEntries(v *inspectionView) []*historyEntry {
	var entries []*historyEntry
	if m.compare {
		for _, r := range m.selectedRuns() {
			if e := m.historyEntryFor(r.ID(), m.metric); e != nil {
				entries = append(entries, e)
			}
		}
		return entries
	}
	r := m.run()
	if r == nil || v == nil {
		return entries
	}
	keys := []string{v.Selected}
	if v.ExpandedChart && len(v.Overlay) > 0 {
		keys = v.Overlay
	}
	for _, key := range keys {
		if _, exists := r.Metric(key); !exists {
			continue
		}
		if e := m.historyEntryFor(r.ID(), key); e != nil {
			entries = append(entries, e)
		}
	}
	return entries
}
func (m *model) metricContent(v *inspectionView, w, h int) paneContent {
	p := paneContent{}
	if v.ExpandedChart {
		return m.curveContent(v, m.chartEntries(v), w, h, true)
	}
	if v.Dashboard {
		return m.dashboardContent(v, w, h)
	}
	scope := []string{"Model", "System", "All"}[v.MetricScope]
	suffix := ""
	if m.inspect.Auto {
		suffix = " · AUTO"
	}
	p.add(fmt.Sprintf("%s metrics · LATEST · %d%s · / search · Enter curve", scope, len(v.Rows), suffix))
	if v.Query != "" {
		p.add("Search: " + clean(v.Query))
	}
	keyW := max(10, w/2)
	valueW := min(16, max(8, w/4))
	head := textFit("Name", keyW) + textFit("Latest", valueW)
	if w >= 48 {
		head += textFit("Step", 9)
	}
	if w >= 85 {
		head += "Updated"
	}
	p.add(dimStyle.Render(head))
	capacity := max(1, h-len(p.Lines))
	if h >= 12 {
		capacity = min(5, max(2, h/3))
	}
	start := listStart(v.Index, len(v.Rows), capacity)
	for i := start; i < min(len(v.Rows), start+capacity); i++ {
		x := v.Rows[i]
		label := x.Key
		for _, pin := range m.metricPins(v.Run) {
			if pin == x.Key {
				label = "* " + label
				break
			}
		}
		line := textFit(label, keyW-2) + textFit(x.Value, valueW)
		if w >= 48 && x.Metric != nil {
			line += textFit(strconv.FormatInt(x.Metric.Step, 10), 9)
		}
		if w >= 85 && x.Metric != nil {
			line += timestamp(x.Metric.Timestamp)
		}
		p.addHit(row(line, i == v.Index, w), "inspect-row:"+x.Key, w)
	}
	if len(v.Rows) == 0 {
		p.add("No metrics match this search.")
		return p
	}
	remaining := h - len(p.Lines)
	if remaining >= 6 {
		p.add(dimStyle.Render(strings.Repeat("─", max(0, w))))
		chart := m.curveContent(v, m.chartEntries(v), w, remaining-1, false)
		offset := len(p.Lines)
		p.Lines = append(p.Lines, chart.Lines...)
		for _, hit := range chart.Hits {
			hit.Y += offset
			p.Hits = append(p.Hits, hit)
		}
	}
	return p
}
func (m *model) dashboardRows(v *inspectionView, w, h int) []detailRow {
	cols := clamp(w/46, 1, 3)
	count := cols * max(1, (h-1)/12)
	start := (v.Index / count) * count
	return v.Rows[clamp(start, 0, len(v.Rows)):min(len(v.Rows), start+count)]
}
func (m *model) dashboardContent(v *inspectionView, w, h int) paneContent {
	p := paneContent{}
	rows := m.dashboardRows(v, w, h)
	if len(rows) == 0 {
		return paneContent{Lines: []string{"No metrics match this search."}}
	}
	cols := min(clamp(w/46, 1, 3), len(rows))
	countRows := (len(rows) + cols - 1) / cols
	cardW := max(1, w/cols)
	cardH := max(6, (h-1)/max(1, countRows))
	p.add(fmt.Sprintf("Metric dashboard · %d metrics · ↑↓ select · Enter expand · v table", len(v.Rows)))
	for offset := 0; offset < len(rows); offset += cols {
		cards := []string{}
		for j := 0; j < cols && offset+j < len(rows); j++ {
			r := rows[offset+j]
			entry := m.historyEntryFor(v.Run.ID(), r.Key)
			entries := []*historyEntry{}
			if entry != nil {
				entries = append(entries, entry)
			}
			cv := m.curveContent(v, entries, cardW-2, cardH-2, false)
			cards = append(cards, frame(r.Key, cv.Lines, cardW, cardH, r.Key == v.Selected))
			p.Hits = append(p.Hits, hit{rect{j * cardW, 1 + (offset/cols)*cardH, cardW, cardH}, "inspect-row:" + r.Key})
		}
		p.Lines = append(p.Lines, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, cards...), "\n")...)
	}
	return p
}
func (m *model) curveContent(v *inspectionView, entries []*historyEntry, w, h int, expanded bool) paneContent {
	profile := m.currentChartProfile()
	if m.compare {
		v = &inspectionView{Selected: m.metric, Cursor: m.inspect.CompareCursor}
	}
	p := paneContent{}
	axis := "Step"
	if m.elapsed {
		axis = "Seconds since run started"
	}
	title := m.metric
	if v != nil {
		title = v.Selected
	}
	if len(entries) == 1 {
		title = entries[0].Metric
	}
	if len(entries) > 1 {
		title = "Shared Y axis"
	}
	mode := profile.draw()
	if profile.Smooth {
		mode += fmt.Sprintf(" · EMA %d samples", profile.span())
	}
	if profile.Normalize {
		mode += " · per-series 0–1"
	}
	p.add(clean(title) + " · " + axis + " · " + mode)
	if expanded {
		legend := "Digits = raw logged samples · lines connect plotted samples"
		if profile.Smooth {
			legend = "Digits = raw logged samples · line = EMA"
		}
		if profile.draw() == "points" {
			legend = "Raw logged points only; display may be sampled"
		}
		if profile.draw() == "lines" {
			legend = "Connecting line only; cursor values remain raw"
			if profile.Smooth {
				legend = "EMA line only; cursor values remain raw"
			}
		}
		if profile.Extrema {
			legend += " · v min / ^ max / * both"
		}
		p.add(legend)
	}
	if !m.compare && expanded && v != nil && v.Run != nil && len(v.Overlay) > 0 {
		var missing []string
		for _, key := range v.Overlay {
			if _, exists := v.Run.Metric(key); !exists {
				missing = append(missing, key)
			}
		}
		if len(missing) == len(v.Overlay) {
			p.add("Selected metrics not logged for this run yet.")
			p.add(clean(strings.Join(missing, ", ")))
			return p
		}
		if len(missing) > 0 {
			p.add("Not logged yet: " + clean(strings.Join(missing, ", ")))
		}
	}
	var series []curveSeries
	preparing, ready := false, false
	for i, e := range entries {
		points := e.StepPoints
		raw := e.StepPoints
		if m.elapsed {
			points = e.TimePoints
			raw = e.TimePoints
		}
		if profile.Smooth || profile.Normalize {
			if d := e.Derived; d != nil && d.Key == chartTransformKey(e.Revision, profile) {
				points, raw = d.Step, d.RawStep
				if m.elapsed {
					points, raw = d.Time, d.RawTime
				}
			} else {
				points, raw = nil, nil
				preparing = true
			}
		}
		ready = ready || len(points) > 0 || len(raw) > 0
		name := e.Metric
		if m.compare {
			name = shortID(e.Run) + " " + name
		}
		slot := i + 1
		if !m.compare && v != nil && len(v.Overlay) > 0 {
			for index, key := range v.Overlay {
				if key == e.Metric {
					slot = index + 1
					break
				}
			}
		}
		entry := curveSeries{Name: name, Points: points, Raw: raw, Slot: slot}
		if profile.Extrema {
			for index, metric := range []*core.Metric{e.Summary.Min, e.Summary.Max} {
				if metric == nil {
					continue
				}
				x := float64(metric.Step)
				if m.elapsed {
					x = (float64(metric.Timestamp) - float64(e.StartTime)) / 1000
				}
				glyph := 'v'
				if index == 1 {
					glyph = '^'
				}
				entry.Markers = append(entry.Markers, curveMarker{curvePoint{X: x, Y: displayChartValue(float64(metric.Value), e.Summary, profile.Normalize)}, glyph})
			}
		}
		series = append(series, entry)
	}
	statusLines := []string{}
	for i, e := range entries {
		state := "not loaded"
		if !e.Updated.IsZero() {
			state = "updated " + e.Updated.Format("15:04:05")
		}
		if e.Pending {
			state += " · loading"
		}
		if e.Err != "" {
			state += " · error: " + clean(e.Err)
		}
		if expanded || len(entries) == 1 {
			statusLines = append(statusLines, fmt.Sprintf("%c %s · %d samples (%d nonfinite) · %d plotted · %s", seriesSymbol(series[i].Slot-1), clean(series[i].Name), e.Summary.Count, e.Summary.NonFiniteCount, min(2400, e.Summary.Count), state))
			if e.Summary.Min != nil {
				best := profile.Best[e.Metric]
				if best == "min" {
					statusLines = append(statusLines, distanceText("Best (min)", e.Extrema.Min))
				} else if best == "max" {
					statusLines = append(statusLines, distanceText("Best (max)", e.Extrema.Max))
				} else if profile.Extrema {
					statusLines = append(statusLines, distanceText("Min", e.Extrema.Min), distanceText("Max", e.Extrema.Max))
				} else {
					statusLines = append(statusLines, fmt.Sprintf("Raw min %s @%d · max %s @%d", e.Summary.Min.Value.String(), e.Summary.Min.Step, e.Summary.Max.Value.String(), e.Summary.Max.Step))
				}
			}
		} else {
			statusLines = append(statusLines, fmt.Sprintf("%c %s · %d samples", seriesSymbol(i), clean(series[i].Name), e.Summary.Count))
		}
	}
	if len(entries) == 0 {
		if !m.compare && v != nil && v.Run != nil {
			if _, exists := v.Run.Metric(v.Selected); !exists {
				p.add("Selected metric not logged for this run yet.")
				return p
			}
		}
		p.add("Loading history…")
		return p
	}
	if preparing && !ready {
		p.add("Preparing chart from full raw history…")
		return p
	}
	if preparing {
		p.add("Preparing remaining series…")
	}
	statusLimit := max(1, min(len(statusLines), h/3))
	graphH := max(2, h-len(p.Lines)-statusLimit)
	var cursorX *float64
	if expanded && v != nil && len(entries) > 0 && len(entries[0].History) > 0 {
		e := entries[0]
		idx := v.Cursor
		if idx < 0 {
			idx = len(e.History) - 1
		}
		idx = clamp(idx, 0, len(e.History)-1)
		point := e.History[idx]
		if m.elapsed && idx < len(e.TimeOrder) {
			point = e.History[e.TimeOrder[idx]]
		}
		x := float64(point.Step)
		if m.elapsed {
			x = (float64(point.Timestamp) - float64(e.StartTime)) / 1000
		}
		cursorX = &x
	}
	graph := renderCurvesStyled(series, w, graphH, m.inspect.ASCII, cursorX, curveRenderOptions{Draw: profile.draw(), Normalize: profile.Normalize})
	graphStart := len(p.Lines)
	p.Lines = append(p.Lines, graph...)
	p.Hits = append(p.Hits, hit{rect{12, graphStart, max(1, w-12), max(1, len(graph)-1)}, "inspect-chart"})
	p.Lines = append(p.Lines, statusLines[:statusLimit]...)
	if expanded && v != nil && len(entries) > 0 && len(entries[0].History) > 0 {
		e := entries[0]
		idx := v.Cursor
		if idx < 0 {
			idx = len(e.History) - 1
		}
		idx = clamp(idx, 0, len(e.History)-1)
		point := e.History[idx]
		rawIndex := idx
		if m.elapsed && idx < len(e.TimeOrder) {
			rawIndex = e.TimeOrder[idx]
			point = e.History[rawIndex]
		}
		line := fmt.Sprintf("Cursor %d/%d · raw %s · step %d · %s", idx+1, len(e.History), point.Value.String(), point.Step, time.UnixMilli(point.Timestamp).Format(time.RFC3339Nano))
		if d := e.Derived; (profile.Smooth || profile.Normalize) && d != nil && d.Key == chartTransformKey(e.Revision, profile) && rawIndex < len(d.Values) {
			line += fmt.Sprintf(" · displayed %.6g", d.Values[rawIndex])
		}
		if len(p.Lines) >= h {
			p.Lines[max(0, h-1)] = line
		} else {
			p.add(line)
		}
	}
	return p
}
func (m *model) moveChartCursor(delta int) {
	v := m.inspectionView()
	if m.compare {
		v = &inspectionView{Cursor: m.inspect.CompareCursor}
		defer func() { m.inspect.CompareCursor = v.Cursor }()
	}
	entries := m.chartEntries(v)
	if v == nil || len(entries) == 0 {
		return
	}
	n := len(entries[0].History)
	if v.Cursor < 0 {
		v.Cursor = n - 1
	}
	v.Cursor = clamp(v.Cursor+delta, 0, n-1)
}
func (m *model) setChartCursorFraction(f float64) {
	v := m.inspectionView()
	entries := m.chartEntries(v)
	if v == nil || len(entries) == 0 {
		return
	}
	e := entries[0]
	if len(e.History) == 0 {
		return
	}
	x := func(i int) float64 {
		j := i
		if m.elapsed {
			j = e.TimeOrder[i]
			return float64(e.History[j].Timestamp)
		}
		return float64(e.History[j].Step)
	}
	lo, hi := x(0), x(len(e.History)-1)
	desired := lo + f*(hi-lo)
	idx := sort.Search(len(e.History), func(i int) bool { return x(i) >= desired })
	if idx >= len(e.History) {
		idx = len(e.History) - 1
	}
	if idx > 0 && math.Abs(x(idx-1)-desired) < math.Abs(x(idx)-desired) {
		idx--
	}
	v.Cursor = idx
}

var curveColors = []string{"75", "214", "120", "207", "117", "221"}

func renderCurves(series []curveSeries, width, height int, ascii bool) []string {
	return renderCurvesAt(series, width, height, ascii, nil)
}
func renderCurvesAt(series []curveSeries, width, height int, ascii bool, cursorX *float64) []string {
	return renderCurvesStyled(series, width, height, ascii, cursorX, curveRenderOptions{})
}
func renderCurvesStyled(series []curveSeries, width, height int, ascii bool, cursorX *float64, options curveRenderOptions) []string {
	if width < 16 || height < 3 {
		return []string{"Enlarge pane for curve"}
	}
	loX, hiX, loY, hiY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	count := 0
	include := func(p curvePoint) {
		if !finiteCurvePoint(p) {
			return
		}
		loX = math.Min(loX, p.X)
		hiX = math.Max(hiX, p.X)
		loY = math.Min(loY, p.Y)
		hiY = math.Max(hiY, p.Y)
		count++
	}
	for _, s := range series {
		if options.Draw != "points" {
			for _, point := range s.Points {
				include(point)
			}
		}
		if options.Draw == "points" || options.Draw == "both" {
			raw := s.Raw
			if raw == nil {
				raw = s.Points
			}
			for _, point := range raw {
				include(point)
			}
		}
		for _, marker := range s.Markers {
			include(marker.Point)
		}
	}
	if count == 0 {
		return []string{"No finite history samples to plot."}
	}
	if options.Normalize {
		loY, hiY = 0, 1
	}
	cols, rows := max(1, width-12), max(1, height-1)
	sx, sy := 2, 4
	if ascii {
		sx, sy = 1, 1
	}
	pw, ph := cols*sx, rows*sy
	bits := make([]byte, cols*rows)
	owners := make([]int, cols*rows)
	chars := make([]rune, cols*rows)
	markers := make([]rune, cols*rows)
	markerOwners := make([]int, cols*rows)
	markerExtrema := make([]bool, cols*rows)
	for i := range owners {
		owners[i] = -1
		chars[i] = ' '
		markerOwners[i] = -1
	}
	if loY == hiY {
		pad := math.Max(math.Abs(loY)*.01, 1)
		a, b := loY-pad, hiY+pad
		if !math.IsInf(a, 0) && !math.IsInf(b, 0) {
			loY, hiY = a, b
		}
	}
	dots := [4][2]byte{{1, 8}, {2, 16}, {4, 32}, {64, 128}}
	set := func(x, y, owner int) {
		x = clamp(x, 0, pw-1)
		y = clamp(y, 0, ph-1)
		i := (y/sy)*cols + x/sx
		bits[i] |= dots[y%sy][x%sx]
		if owners[i] < 0 {
			owners[i] = owner
		} else if owners[i] != owner {
			owners[i] = len(curveColors)
		}
		chars[i] = seriesSymbol(owner)
		if options.Draw == "both" {
			chars[i] = '.'
		}
	}
	for index, s := range series {
		if options.Draw == "points" {
			continue
		}
		owner := index
		if s.Slot > 0 {
			owner = s.Slot - 1
		}
		px, py := 0, 0
		previous := false
		for _, p := range s.Points {
			if !finiteCurvePoint(p) {
				previous = false
				continue
			}
			x := clamp(int(math.Round(curveFraction(p.X, loX, hiX)*float64(pw-1))), 0, pw-1)
			y := clamp(ph-1-int(math.Round(curveFraction(p.Y, loY, hiY)*float64(ph-1))), 0, ph-1)
			if previous {
				dx := int(math.Abs(float64(x - px)))
				dy := -int(math.Abs(float64(y - py)))
				stepX, stepY := 1, 1
				if px > x {
					stepX = -1
				}
				if py > y {
					stepY = -1
				}
				err := dx + dy
				cx, cy := px, py
				for {
					set(cx, cy, owner)
					if cx == x && cy == y {
						break
					}
					twice := 2 * err
					if twice >= dy {
						err += dy
						cx += stepX
					}
					if twice <= dx {
						err += dx
						cy += stepY
					}
				}
			} else {
				set(x, y, owner)
			}
			px, py, previous = x, y, true
		}
	}
	placeMarker := func(point curvePoint, glyph rune, owner int, extreme bool) {
		if !finiteCurvePoint(point) {
			return
		}
		x := clamp(int(math.Round(curveFraction(point.X, loX, hiX)*float64(pw-1))), 0, pw-1)
		y := clamp(ph-1-int(math.Round(curveFraction(point.Y, loY, hiY)*float64(ph-1))), 0, ph-1)
		i := (y/sy)*cols + x/sx
		if extreme {
			if markerExtrema[i] {
				if markers[i] != glyph {
					glyph = '*'
				}
				if markerOwners[i] != owner {
					owner = len(curveColors)
				}
			}
			markers[i], markerOwners[i], markerExtrema[i] = glyph, owner, true
			return
		}
		if markers[i] != 0 && markerOwners[i] != owner {
			markers[i] = '+'
			markerOwners[i] = len(curveColors)
			return
		}
		markers[i], markerOwners[i] = glyph, owner
	}
	if options.Draw == "points" || options.Draw == "both" {
		for index, s := range series {
			owner := index
			if s.Slot > 0 {
				owner = s.Slot - 1
			}
			raw := s.Raw
			if raw == nil {
				raw = s.Points
			}
			for _, point := range raw {
				placeMarker(point, seriesSymbol(owner), owner, false)
			}
		}
	}
	for index, s := range series {
		owner := index
		if s.Slot > 0 {
			owner = s.Slot - 1
		}
		for _, marker := range s.Markers {
			placeMarker(marker.Point, marker.Glyph, owner, true)
		}
	}
	out := make([]string, 0, rows+1)
	for y := 0; y < rows; y++ {
		label := ""
		if y == 0 {
			label = fmt.Sprintf("%.5g", hiY)
		} else if y == rows-1 {
			label = fmt.Sprintf("%.5g", loY)
		}
		axis := "│"
		if ascii {
			axis = "|"
		}
		var line strings.Builder
		line.WriteString(textFit(label, 11))
		line.WriteString(axis)
		var segment strings.Builder
		segmentOwner := -1
		flush := func() {
			if segment.Len() == 0 {
				return
			}
			text := segment.String()
			if segmentOwner >= 0 {
				color := "255"
				if segmentOwner < len(curveColors) {
					color = curveColors[segmentOwner]
				}
				text = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
			}
			line.WriteString(text)
			segment.Reset()
		}
		cursorColumn := -1
		if cursorX != nil {
			cursorColumn = clamp(int(math.Round(curveFraction(*cursorX, loX, hiX)*float64(cols-1))), 0, cols-1)
		}
		for x := 0; x < cols; x++ {
			i := y*cols + x
			glyph := ' '
			owner := -1
			if bits[i] != 0 {
				glyph = rune(0x2800) + rune(bits[i])
				if ascii {
					glyph = chars[i]
				}
				owner = owners[i]
			} else if x == cursorColumn {
				glyph = '┊'
				if ascii {
					glyph = ':'
				}
				owner = len(curveColors)
			}
			if markers[i] != 0 {
				glyph, owner = markers[i], markerOwners[i]
			}
			if owner != segmentOwner {
				flush()
				segmentOwner = owner
			}
			segment.WriteRune(glyph)
		}
		flush()

		out = append(out, line.String())
	}
	out = append(out, textFit(fmt.Sprintf("            %.5g → %.5g", loX, hiX), width))
	return out
}

func (m *model) inspectionRunScope() string {
	var ids []string
	for _, r := range m.selectedRuns() {
		ids = append(ids, r.ID())
	}
	return m.active + "/" + strings.Join(ids, ",")
}
func (m *model) cancelUnusedHistories() {
	if m.inspect == nil {
		return
	}
	desired := m.desiredHistories()
	for key, e := range m.inspect.Histories {
		if _, ok := desired[key]; !ok && e.Pending {
			if e.Cancel != nil {
				e.Cancel()
			}
			e.Gen++
			e.Pending = false
		}
	}
}

func (m *model) historyNamespace() string { return core.SourceKey(m.target()) }

// Polling preference survives a completed run. Current metadata determines
// whether its next tick is active, so selecting a running run resumes polling.
func (m *model) pollInspection() tea.Cmd {
	if !m.inspect.Auto {
		return nil
	}
	for _, r := range m.selectedRuns() {
		if !terminalRunStatus(r.Info.Status) {
			return m.refreshInspection()
		}
	}
	return nil
}
func terminalRunStatus(status string) bool {
	return status == "FINISHED" || status == "FAILED" || status == "KILLED"
}

func metricPreviewVisible(v *inspectionView, h int) bool {
	prefix := 2
	if v.Query != "" {
		prefix++
	}
	capacity := max(1, h-prefix)
	if h >= 12 {
		capacity = min(5, max(2, h/3))
	}
	rows := min(len(v.Rows), capacity)
	return rows > 0 && h-prefix-rows >= 6
}
func curveFraction(v, lo, hi float64) float64 {
	if lo == hi {
		return .5
	}
	span := hi - lo
	if math.IsInf(span, 0) {
		scale := math.Max(math.Abs(lo), math.Abs(hi))
		return (v/scale - lo/scale) / (hi/scale - lo/scale)
	}
	return (v - lo) / span
}
func finiteCurvePoint(p curvePoint) bool {
	return !p.Gap && !math.IsNaN(p.X) && !math.IsInf(p.X, 0) && !math.IsNaN(p.Y) && !math.IsInf(p.Y, 0)
}
func sameHistorySample(a, b core.Metric) bool {
	return a.Step == b.Step && a.Timestamp == b.Timestamp && math.Float64bits(float64(a.Value)) == math.Float64bits(float64(b.Value)) && a.Key == b.Key && a.ModelID == b.ModelID && a.DatasetName == b.DatasetName && a.DatasetDigest == b.DatasetDigest
}

// Sample selection can omit a nonfinite record; retain a gap marker whenever
// any skipped raw record between two displayed points is nonfinite. This scan
// occurs only in the history effect, independently for step and time order.
func prepareSampledCurve(raw, samples []core.Metric, order []int, start int64, elapsed bool) []curvePoint {
	sample := append([]core.Metric(nil), samples...)
	if elapsed {
		sort.SliceStable(sample, func(i, j int) bool { return sample[i].Timestamp < sample[j].Timestamp })
	}
	at := func(i int) core.Metric {
		if len(order) > 0 {
			return raw[order[i]]
		}
		return raw[i]
	}
	nonfinite := func(m core.Metric) bool { return math.IsNaN(float64(m.Value)) || math.IsInf(float64(m.Value), 0) }
	out := make([]curvePoint, 0, len(sample))
	index := 0
	previousFinite := false
	for _, metric := range sample {
		gap := false
		for index < len(raw) && !sameHistorySample(at(index), metric) {
			if nonfinite(at(index)) {
				gap = true
			}
			index++
		}
		if index < len(raw) {
			index++
		}
		x := float64(metric.Step)
		if elapsed {
			x = (float64(metric.Timestamp) - float64(start)) / 1000
		}
		bad := nonfinite(metric)
		if previousFinite && gap && !bad {
			out = append(out, curvePoint{X: x, Gap: true})
		}
		out = append(out, curvePoint{x, float64(metric.Value), bad})
		previousFinite = !bad
	}
	return out
}
