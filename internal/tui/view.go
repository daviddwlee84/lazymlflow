package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

var focusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
var selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true)
var dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
func fit(s string, w int) string {
	w = max(0, w)
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func textFit(s string, w int) string { return fit(clean(s), w) }
func frame(title string, lines []string, w, h int, focused bool) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	if w < 3 || h < 3 {
		all := append([]string{title}, lines...)
		return block(all, w, h)
	}
	inner := w - 2
	marker := " "
	if focused {
		marker = "* "
	}
	t := marker + clean(title) + " "
	t = ansi.Truncate(t, inner, "")
	top := "┌" + t + strings.Repeat("─", max(0, inner-ansi.StringWidth(t))) + "┐"
	bottom := "└" + strings.Repeat("─", inner) + "┘"
	if focused {
		top = focusStyle.Render(top)
		bottom = focusStyle.Render(bottom)
	}
	out := []string{top}
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, "│"+fit(line, inner)+"│")
	}
	out = append(out, bottom)
	return strings.Join(out, "\n")
}
func block(lines []string, w, h int) string {
	var out []string
	for i := 0; i < h; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		out = append(out, fit(line, w))
	}
	return strings.Join(out, "\n")
}
func row(s string, selected bool, w int) string {
	prefix := "  "
	if selected {
		prefix = "> "
	}
	v := textFit(prefix+s, w)
	if selected {
		return selectedStyle.Render(v)
	}
	return v
}
func listStart(index, count, capacity int) int {
	capacity = max(1, capacity)
	return clamp(index-capacity+1, 0, max(0, count-capacity))
}

func (m *model) View() tea.View {
	if view, ok := m.artifactPreviewView(); ok {
		return view
	}
	if v, ok := m.extensionView(); ok {
		return v
	}
	if v, ok := m.workspaceView(); ok {
		return v
	}
	w, h := max(1, m.width), max(1, m.height)
	if h < 5 || w < 16 {
		v := tea.NewView(block([]string{"lazymlflow", "Resize terminal", "q quit"}, w, h))
		v.AltScreen = true
		if m.layout.Mouse {
			v.MouseMode = tea.MouseModeCellMotion
		}
		return v
	}
	target := m.target().Label()
	if target == "" {
		target = "No target"
	}
	connection := "disconnected"
	s := m.state()
	if s != nil {
		if s.ConnectPending {
			connection = "connecting… (Esc cancels)"
		} else if s.ConnectErr != "" {
			connection = "connection failed"
		} else if s.Session != nil {
			connection = "connected"
			if s.Session.RuntimeVersion != "" {
				connection += " · MLflow " + s.Session.RuntimeVersion
			}
		}
	}
	header := focusStyle.Render(textFit("lazymlflow · "+target+" · "+connection, w))
	if h < 8 && m.inputMode != "" {
		v := tea.NewView(block([]string{header, clean(m.inputLabel), m.input.View(), clean(m.status), "Enter accept · Esc cancel"}, w, h))
		v.AltScreen = true
		if m.layout.Mouse {
			v.MouseMode = tea.MouseModeCellMotion
		}
		return v
	}
	contentHeight := h - 4
	if m.inputMode != "" {
		contentHeight = max(1, contentHeight-3)
	}
	var content string
	if m.targetForm != nil {
		content = frame("Tracking target configuration", strings.Split(m.targetForm.View(w-2, contentHeight-2), "\n"), w, contentHeight, true)
	} else if m.overlay != "" {
		content = m.overlayView(w, contentHeight)
	} else if m.compare {
		content = frame("Compare · "+fmt.Sprint(len(m.selectedRuns()))+" runs · same target", m.compareLines(w-2, contentHeight-2), w, contentHeight, true)
	} else {
		layout := m.geometry()
		if layout.Split {
			left, upper := layout.Panes[0].W, layout.Panes[1].H
			right := w - left
			exp := frame("1 Experiments", m.experimentLines(left-2, contentHeight-2), left, contentHeight, m.focus == 0)
			runs := frame("2 "+activityLabel(m.activityScope()), m.runLines(right-2, upper-2), right, upper, m.focus == 1)
			detail := frame("3 "+m.detailTitle(), m.detailLines(right-2, contentHeight-upper-2), right, contentHeight-upper, m.focus == 2)
			content = lipgloss.JoinHorizontal(lipgloss.Top, exp, runs+"\n"+detail)
		} else {
			switch m.focus {
			case 0:
				content = frame("1 Experiments · z zoom / restore", m.experimentLines(w-2, contentHeight-2), w, contentHeight, true)
			case 1:
				content = frame("2 "+activityLabel(m.activityScope()), m.runLines(w-2, contentHeight-2), w, contentHeight, true)
			case 2:
				content = frame("3 "+m.detailTitle(), m.detailLines(w-2, contentHeight-2), w, contentHeight, true)
			}
		}
	}
	if m.inputMode != "" {
		content += "\n" + frame(m.inputLabel, []string{m.input.View()}, w, 3, true)
	}
	status := m.status
	if s != nil && s.ConnectErr != "" {
		status = "Connection error: " + s.ConnectErr + " · r retry · t targets"
	}
	if m.resizing {
		status = "Resize: h/l left width · k/j upper height · 0 default · Enter/Esc save"
	}
	if m.prefix {
		status = "g … (g again: first row)"
	}
	footer := m.footer()
	output := header + "\n" + content + "\n" + textFit(status, w) + "\n" + dimStyle.Render(textFit(footer, w)) + "\n" + dimStyle.Render(textFit(m.contextLine(), w))
	v := tea.NewView(output)
	v.AltScreen = true
	if m.layout.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.WindowTitle = "lazymlflow"
	return v
}
func (m *model) contextLine() string {
	if m.overlay == "" && m.activityScope() != scopeExperiment {
		if m.focus == 1 {
			return "★ pinned · ● unread · ! alert · i full name · * pin run · J/K unread · w/W read"
		}
		return "★ pinned · ● unread · ! alert · Enter inspect/read · J/K unread · w/W read"
	}
	if m.overlay != "" {
		switch m.overlay {
		case "help":
			return "Search shortcut keys and descriptions for the current view."
		case "columns":
			return "Columns are saved separately for each experiment; names match logged keys exactly."
		case "sort":
			return "Server-supported sorts refresh globally; other sorts apply to loaded rows (A loads all)."
		case "group":
			return "Groups show loaded run counts; nested runs use mlflow.parentRunId."
		default:
			return "Esc returns to the previous pane and selection."
		}
	}
	if m.targetForm != nil {
		return "Shared target form · Python / .venv supported · Ctrl+O advanced settings"
	}
	if m.inputMode != "" {
		return "Enter accept · Esc cancel · printable keys type into the field"
	}
	if m.downloadPending {
		return "Download pending · Ctrl+X cancel · q exits and cancels"
	}
	if m.focus == 2 && m.isInspectionTab() {
		return "N notes · S summary · B datasets · 1/2/3 pane · [/] tabs · z zoom · ? help · q quit"
	}
	if m.focus == 1 && !m.compare {
		return "N notes · S summary · B datasets · 1/2/3 pane · z zoom · Ctrl+W resize · M mouse · [/] pan columns · ? help · q quit"
	}
	return "↑↓/jk move · Tab/Shift+Tab focus · ←→/hl context · gg/G first/last · ? help · q quit"
}
func (m *model) footer() string {
	if m.overlay == "" && m.activityScope() != scopeExperiment && m.focus < 2 {
		if m.activityScope() == scopePinned {
			if m.focus == 0 {
				return "Enter / 2 focus pinned runs · r refresh pins · I inbox · ? help"
			}
			return "* pin/unpin · i full name · Enter inspect · / search · s sort · r refresh pins · ? help"
		}
		return "I activity · J/K unread · Enter inspect/read · w/W read · a acknowledge · r refresh · ! settings · ? help"
	}
	if v := m.inspectionFooter(); v != "" {
		return v
	}
	if m.targetForm != nil {
		return "Tab / Enter next · Shift+Tab previous · Esc back/cancel · Ctrl+C cancel form"
	}
	if m.overlay != "" {
		if m.isPicker() {
			if m.pickerTyping {
				return "Type to search · ↑↓ select · Enter / Tab list focus · Esc close"
			}
			switch m.overlay {
			case "columns":
				return "Space select · < > reorder · [ ] width · n numeric · 0 reset · / search · Esc close"
			case "sort":
				return "Enter primary sort · Space secondary / direction · Backspace remove · n numeric · 0 reset · Esc close"
			case "info":
				return "↑↓/jk scroll · PgUp/PgDn · Y copy full name · r rebuild counts · Enter / Esc close"
			case "run-info":
				return "↑↓/jk scroll · PgUp/PgDn · Y copy full name · Enter / Esc close"
			case "parent-info":
				return "↑↓/jk scroll · Enter / Esc close"
			default:
				return "Space / Enter select · / search · Esc close"
			}
		}
		switch m.overlay {
		case "help":
			if m.helpTyping {
				return "Type to filter · ↑↓ scroll · Enter keep filter · Esc clear"
			}
			if m.helpSearch.Value() != "" {
				return "↑↓/jk scroll · / edit filter · Esc clear · Enter / q close"
			}
			return "↑↓/jk scroll · / filter · Enter / Esc / q close"
		case "targets":
			return "↑↓/jk select · Enter connect · a add · e edit · s setup server · E environment · Esc back"
		case "overwrite":
			return "y replace destination · n / Esc keep existing destination"
		default:
			return "↑↓/jk scroll · Enter choose · Esc back"
		}
	}
	priorities := []string{"info", "run-info", "toggle-run-pin", "targets", "local", "filter", "sort", "basket", "compare", "columns", "history", "chart", "axis", "diff", "download", "open", "copy", "refresh", "journal", "summary", "dataset-workspace"}
	available := m.actions()
	var out []string
	for _, id := range priorities {
		for _, a := range available {
			if a.ID == id {
				out = append(out, a.Keys[0]+" "+a.Label)
			}
		}
	}
	return strings.Join(out, " · ")
}
func (m *model) experimentLines(w, h int) []string { return m.experimentContent(w, h).Lines }
func (m *model) runLines(w, h int) []string        { return m.runContent(w, h).Lines }
func detailTabs() []string {
	return []string{"Overview", "Metrics", "Params", "Tags", "Artifacts", "Datasets"}
}
func (m *model) detailTitle() string {
	tabs := detailTabs()
	for i, t := range tabs {
		if i == m.tab {
			tabs[i] = "[" + t + "]"
		}
	}
	return strings.Join(tabs, " ")
}
func (m *model) detailLines(w, h int) []string {
	if m.isInspectionTab() && m.inspectionView() != nil {
		return m.inspectionContent(w, h).Lines
	}
	r := m.run()
	if r == nil {
		return []string{"Choose a run to inspect."}
	}
	var lines []string
	switch m.tab {
	case 0:
		lines = []string{"Name: " + clean(r.Name()), "Run ID: " + r.ID(), "Experiment: " + r.Info.ExperimentID, "Status: " + r.Info.Status, "Started: " + timestamp(r.Info.StartTime), "Ended: " + timestamp(r.Info.EndTime), "Artifacts: " + clean(r.Info.ArtifactURI), "o open web · y copy full run ID · [ / ] switch tabs"}
	case 1:
		if m.chart {
			return m.chartLines(w, h)
		}
		lines = append(lines, "LATEST metric (MLflow semantics; not best)")
		metrics := append([]core.Metric(nil), r.Data.Metrics...)
		sort.Slice(metrics, func(i, j int) bool { return metrics[i].Key < metrics[j].Key })
		for _, v := range metrics {
			lines = append(lines, clean(v.Key)+" = "+v.Value.String()+fmt.Sprintf("  step %d", v.Step))
		}
		lines = append(lines, "m: choose a metric history")
	case 2:
		lines = kvLines(r.Data.Params)
	case 3:
		lines = kvLines(r.Data.Tags)
	case 5:
		for _, input := range r.Inputs.DatasetInputs {
			d := input.Dataset
			lines = append(lines, "Dataset: "+clean(d.Name), "Digest: "+clean(d.Digest), "Context: "+findKV(input.Tags, "mlflow.data.context"), "Source: "+clean(d.SourceType+" "+d.Source), "Schema: "+clean(d.Schema))
			lines = append(lines, kvLines(input.Tags)...)
		}
	case 4:
		a := m.artifacts()
		lines = append(lines, "/"+clean(m.currentPath())+"   (h: parent · l/Enter: enter · d: download)")
		if a == nil {
			return lines
		}
		if a.Pending {
			lines = append(lines, "Loading artifacts…")
		}
		if a.Err != "" {
			lines = append(lines, "Refresh failed; previous rows kept", clean(a.Err))
		}
		if len(a.Rows) == 0 && !a.Pending {
			lines = append(lines, "Empty directory. d downloads this directory.")
		}
		capacity := max(1, h-len(lines))
		start := listStart(a.Index, len(a.Rows), capacity)
		for i := start; i < min(len(a.Rows), start+capacity); i++ {
			f := a.Rows[i]
			name := strings.TrimPrefix(f.Path, m.currentPath())
			name = strings.TrimPrefix(name, "/")
			label := name + "  " + bytes(f.FileSize)
			if f.IsDir {
				label = name + "/"
			}
			lines = append(lines, row(label, i == a.Index, w))
		}
		return lines
	}
	if len(lines) == 0 {
		lines = []string{"No values logged."}
	}
	start := clamp(m.detailOffset, 0, max(0, len(lines)-max(1, h)))
	return lines[start:]
}
func (m *model) overlayView(w, h int) string {
	if v, ok := m.inspectionOverlay(w, h); ok {
		return v
	}
	if m.isPicker() {
		return m.pickerView(w, h)
	}
	var title string
	var lines []string
	switch m.overlay {
	case "targets":
		title = "Tracking targets"
		for _, t := range m.targets {
			label := t.Label() + " · " + t.ID
			if t.ID == m.active {
				label += " (active)"
			}
			lines = append(lines, label)
		}
		if len(lines) == 0 {
			lines = []string{"No targets. Press a to connect or s to set up a new server."}
		}
		if len(m.targets) > 0 {
			t := m.targets[clamp(m.menuIndex, 0, len(m.targets)-1)]
			title += " · " + clean(t.TrackingURI)
		}
	case "metrics":
		title = "Choose metric history"
		lines = m.metricKeys()
	case "help":
		return m.helpView(w, h)
	case "palette":
		title = "Keyboard actions"
		for _, a := range m.actions() {
			lines = append(lines, fit(strings.Join(a.Keys, " / "), 20)+a.Label)
		}
		lines = append(lines, "gg: first row", "Metrics shown are latest values; missing values are —.", "Local search scans loaded rows; f and s query all server rows.", "Ctrl+C exits; Ctrl+X cancels an active download.")
	case "overwrite":
		title = "Replace existing download destination?"
		lines = []string{"Destination: " + clean(m.downloadRequest.Destination), "Artifact: " + clean(m.downloadRequest.Path), "This replaces the existing file or directory.", "y confirms replacement; n or Esc cancels."}
	}
	if m.overlay == "overwrite" {
		return frame(title, lines, w, h, true)
	}
	capacity := max(1, h-2)
	start := listStart(m.menuIndex, len(lines), capacity)
	var visible []string
	for i := start; i < min(len(lines), start+capacity); i++ {
		visible = append(visible, row(lines[i], i == m.menuIndex, w-2))
	}
	return frame(title, visible, w, h, true)
}

type comparisonRow struct {
	Key       string
	Values    []string
	Different bool
}

func comparisonRows(runs []core.Run, differences bool) []comparisonRow {
	keys := map[string]bool{}
	values := make([]map[string]string, len(runs))
	for i, r := range runs {
		values[i] = map[string]string{}
		for _, v := range r.Data.Metrics {
			k := "metric/" + v.Key
			keys[k] = true
			values[i][k] = v.Value.String()
		}
		for _, v := range r.Data.Params {
			k := "param/" + v.Key
			keys[k] = true
			values[i][k] = v.Value
		}
		for _, v := range r.Data.Tags {
			k := "tag/" + v.Key
			keys[k] = true
			values[i][k] = v.Value
		}
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []comparisonRow
	for _, k := range sorted {
		row := comparisonRow{Key: k}
		first, firstOK := "", false
		for i, v := range values {
			value, ok := v[k]
			if i == 0 {
				first, firstOK = value, ok
			} else if value != first || ok != firstOK {
				row.Different = true
			}
			if !ok {
				value = "—"
			}
			row.Values = append(row.Values, value)
		}
		if !differences || row.Different {
			out = append(out, row)
		}
	}
	return out
}
func (m *model) compareLines(w, h int) []string {
	if m.chart {
		return m.chartLines(w, h)
	}
	runs := m.selectedRuns()
	if len(runs) == 0 {
		return []string{"Select runs with Space before comparing."}
	}
	keyWidth := min(28, max(12, w/3))
	colWidth := 22
	visible := max(1, (w-keyWidth)/colWidth)
	startRun := clamp(m.comparePan, 0, max(0, len(runs)-visible))
	endRun := min(len(runs), startRun+visible)
	header := fit("LATEST / VALUE", keyWidth)
	identity := fit("RUN ID", keyWidth)
	experiments := fit("EXPERIMENT", keyWidth)
	for _, r := range runs[startRun:endRun] {
		header += textFit(r.Name(), colWidth)
		identity += textFit(shortID(r.ID()), colWidth)
		experiments += textFit(r.Info.ExperimentID, colWidth)
	}
	lines := []string{fmt.Sprintf("%d runs · columns %d–%d · x differences: %t · m history · h/l pan", len(runs), startRun+1, endRun, m.differences), dimStyle.Render(header), dimStyle.Render(identity), dimStyle.Render(experiments)}
	if s := m.state(); s != nil {
		if s.ComparePending {
			lines = append(lines, "Refreshing selected runs across experiments…")
		}
		if s.CompareErr != "" {
			lines = append(lines, "Refresh failed; previous values kept: "+clean(s.CompareErr))
		}
	}
	rows := comparisonRows(runs, m.differences)
	capacity := max(1, h-len(lines))
	start := clamp(m.compareOffset, 0, max(0, len(rows)-capacity))
	for _, r := range rows[start:min(len(rows), start+capacity)] {
		mark := "  "
		if r.Different {
			mark = "! "
		}
		line := textFit(mark+r.Key, keyWidth)
		for _, v := range r.Values[startRun:endRun] {
			line += textFit(v, colWidth)
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		lines = append(lines, "No differences in logged values.")
	}
	return lines
}

type plotPoint struct{ X, Y float64 }
type plotSeries struct {
	Name                         string
	RunID, ExperimentID          string
	Points                       []plotPoint
	Total, Nonfinite, DuplicateX int
}

func historySeries(run core.Run, history []core.Metric, elapsed bool) plotSeries {
	s := plotSeries{Name: run.Name(), RunID: run.ID(), ExperimentID: run.Info.ExperimentID, Total: len(history)}
	xs := map[float64]bool{}
	for _, metric := range history {
		x := float64(metric.Step)
		if elapsed {
			x = float64(metric.Timestamp-run.Info.StartTime) / 1000
		}
		y := float64(metric.Value)
		if math.IsNaN(y) || math.IsInf(y, 0) {
			s.Nonfinite++
			continue
		}
		if xs[x] {
			s.DuplicateX++
		}
		xs[x] = true
		s.Points = append(s.Points, plotPoint{x, y})
	}
	sort.SliceStable(s.Points, func(i, j int) bool { return s.Points[i].X < s.Points[j].X })
	return s
}
func (m *model) chartLines(w, h int) []string {
	if m.inspect != nil {
		if entries := m.chartEntries(m.inspectionView()); len(entries) > 0 {
			return m.curveContent(m.inspectionView(), entries, w, h, true).Lines
		}
	}
	s := m.state()
	if s == nil {
		return nil
	}
	axis := "step"
	if m.elapsed {
		axis = "seconds since each run started"
	}
	lines := []string{"History: " + clean(m.metric) + " · x=" + axis + pending(s.HistoryPending)}
	var series []plotSeries
	for _, r := range m.selectedRuns() {
		key := r.ID() + "\x00" + m.metric
		if err := s.HistoryErrors[key]; err != "" {
			lines = append(lines, shortID(r.ID())+" · "+clean(r.Name())+": "+clean(err))
		}
		series = append(series, historySeries(r, s.Histories[key], m.elapsed))
	}
	graphHeight := max(2, h-len(lines)-len(series)-2)
	lines = append(lines, plot(series, w, graphHeight)...)
	for i, se := range series {
		lines = append(lines, fmt.Sprintf("%c %s · exp %s · %s · %d samples · %d nonfinite · %d duplicate x", seriesSymbol(i), shortID(se.RunID), clean(se.ExperimentID), clean(se.Name), se.Total, se.Nonfinite, se.DuplicateX))
	}
	lines = append(lines, "a: step/elapsed · m: metric · v: table (compare) · points coalesced only on screen")
	return lines
}
func seriesSymbol(i int) rune { return []rune("123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")[i%35] }
func shortID(id string) string {
	runes := []rune(clean(id))
	if len(runes) > 8 {
		return string(runes[:8])
	}
	return string(runes)
}
func plot(series []plotSeries, width, height int) []string {
	xlo, xhi, ylo, yhi := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	n := 0
	for _, s := range series {
		for _, p := range s.Points {
			n++
			xlo = math.Min(xlo, p.X)
			xhi = math.Max(xhi, p.X)
			ylo = math.Min(ylo, p.Y)
			yhi = math.Max(yhi, p.Y)
		}
	}
	if n == 0 {
		return []string{"No finite history samples to plot."}
	}
	cols := max(1, width-13)
	rows := max(1, height-1)
	grid := make([][]rune, rows)
	for y := range grid {
		grid[y] = []rune(strings.Repeat(" ", cols))
	}
	xspan, yspan := xhi-xlo, yhi-ylo
	if xspan == 0 {
		xspan = 1
	}
	if yspan == 0 {
		yspan = 1
	}
	for i, s := range series {
		for _, p := range s.Points {
			x := clamp(int(math.Round((p.X-xlo)/xspan*float64(cols-1))), 0, cols-1)
			y := clamp(rows-1-int(math.Round((p.Y-ylo)/yspan*float64(rows-1))), 0, rows-1)
			symbol := seriesSymbol(i)
			if grid[y][x] != ' ' && grid[y][x] != symbol {
				symbol = '*'
			}
			grid[y][x] = symbol
		}
	}
	var out []string
	for i, row := range grid {
		label := ""
		if i == 0 {
			label = fmt.Sprintf("%.5g", yhi)
		} else if i == rows-1 {
			label = fmt.Sprintf("%.5g", ylo)
		}
		out = append(out, fit(label, 11)+"│"+string(row))
	}
	out = append(out, fmt.Sprintf("x %.5g → %.5g", xlo, xhi))
	return out
}
func findKV(kv []core.KeyValue, key string) string {
	for _, v := range kv {
		if v.Key == key {
			return v.Value
		}
	}
	return "—"
}
func kvLines(kv []core.KeyValue) []string {
	values := append([]core.KeyValue(nil), kv...)
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	var lines []string
	for _, v := range values {
		lines = append(lines, clean(v.Key)+" = "+clean(v.Value))
	}
	return lines
}
func pending(v bool) string {
	if v {
		return " · loading…"
	}
	return ""
}
func timestamp(v int64) string {
	if v == 0 {
		return "—"
	}
	return time.UnixMilli(v).Local().Format("2006-01-02 15:04")
}
func bytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/(1024*1024*1024))
}
