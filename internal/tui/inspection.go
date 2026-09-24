package tui

import (
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

type detailRow struct {
	Key, Value, Kind, Required, Shape string
	Metric                            *core.Metric
	Field                             *core.SchemaField
	Depth, Index                      int
}
type inspectionView struct {
	DatasetKey                         string
	Query                              string
	Sort                               int
	Index                              int
	Selected                           string
	Rows, All                          []detailRow
	Dataset                            int
	Expanded                           map[string]bool
	Schema                             core.DatasetSchema
	Profile                            core.DatasetProfile
	Run                                *core.Run
	Dashboard, ExpandedChart, Raw      bool
	MetricScope                        int
	Overlay                            []string
	Cursor                             int
	MetricMetadata                     string
	MetricRevision, MetricViewRevision uint64
}
type inspectionState struct {
	CompareCursor     int
	Views             map[string]*inspectionView
	Histories         map[string]*historyEntry
	Search            textinput.Model
	Typing            bool
	Picker            []string
	PickerIndex       int
	Value             string
	Auto              bool
	PollGen           uint64
	RunGen            uint64
	RunPending        bool
	Slots             chan struct{}
	ASCII             bool
	MetricPreferences map[string]*experimentMetricPreferences
	OverlayDraft      []string
	OverlayDraftKey   string
}

func (m *model) initInspection() {
	s := textinput.New()
	s.Prompt = "Search: "
	m.inspect = &inspectionState{Views: map[string]*inspectionView{}, Histories: map[string]*historyEntry{}, MetricPreferences: map[string]*experimentMetricPreferences{}, Search: s, Slots: make(chan struct{}, 4), Auto: m.opts.RefreshSeconds > 0, ASCII: os.Getenv("TERM") == "dumb" || os.Getenv("LC_ALL") == "C" || os.Getenv("LC_ALL") == "POSIX"}
}
func (m *model) isInspectionTab() bool { return m.tab == 1 || m.tab == 2 || m.tab == 3 || m.tab == 5 }
func (m *model) inspectionKey() string {
	r := m.run()
	if r == nil {
		return ""
	}
	return core.SourceKey(m.target()) + "\x00" + r.ID() + "\x00" + strconv.Itoa(m.tab)
}
func (m *model) inspectionView() *inspectionView {
	if m.inspect == nil {
		return nil
	}
	return m.inspect.Views[m.inspectionKey()]
}
func (m *model) inspectionDatasetIndex() int {
	if v := m.inspectionView(); v != nil {
		return v.Dataset
	}
	return 0
}
func (m *model) syncInspection() {
	if m.inspect == nil || !m.isInspectionTab() {
		return
	}
	r := m.run()
	if r == nil {
		return
	}
	key := m.inspectionKey()
	v := m.inspect.Views[key]
	if v == nil {
		v = &inspectionView{Expanded: map[string]bool{}}
		m.inspect.Views[key] = v
	}
	metricChanged := false
	if m.tab == 1 {
		metricChanged = m.syncMetricPreferences(v, r)
	}
	if v.Run == r && !metricChanged {
		return
	}
	v.Run = r
	m.prepareInspection(v)
}
func (m *model) prepareInspection(v *inspectionView) {
	r := v.Run
	v.All = nil
	switch m.tab {
	case 1:
		for i := range r.Data.Metrics {
			x := &r.Data.Metrics[i]
			v.All = append(v.All, detailRow{Key: x.Key, Value: x.Value.String(), Metric: x})
		}
	case 2, 3:
		pairs := r.Data.Params
		if m.tab == 3 {
			pairs = r.Data.Tags
		}
		for _, x := range pairs {
			v.All = append(v.All, detailRow{Key: x.Key, Value: x.Value})
		}
	case 5:
		if len(r.Inputs.DatasetInputs) > 0 {
			if v.DatasetKey != "" {
				for i, input := range r.Inputs.DatasetInputs {
					if core.DatasetIdentity(m.historyNamespace(), input.Dataset) == v.DatasetKey {
						v.Dataset = i
						break
					}
				}
			}
			v.Dataset = clamp(v.Dataset, 0, len(r.Inputs.DatasetInputs)-1)
			d := r.Inputs.DatasetInputs[v.Dataset].Dataset
			v.DatasetKey = core.DatasetIdentity(m.historyNamespace(), d)
			v.Schema = core.ParseDatasetSchema(d.Schema)
			v.Profile = core.ParseDatasetProfile(d.Profile)
			for _, f := range core.FlattenSchemaFields(v.Schema) {
				required := "—"
				if f.Required != nil {
					required = strconv.FormatBool(*f.Required)
				}
				shape := ""
				if len(f.Shape) > 0 {
					shape = fmt.Sprintf("%v rank %d", f.Shape, f.Rank)
					if f.Dimensions != nil {
						shape += fmt.Sprintf(" · features %d", *f.Dimensions)
					}
				}
				field := f
				v.All = append(v.All, detailRow{Key: f.Path, Value: f.Type, Required: required, Shape: shape, Field: &field, Depth: f.Depth, Index: f.Index})
			}
		}
	}
	m.filterInspection(v)
}
func (m *model) filterInspection(v *inspectionView) {
	old := v.Selected
	v.Rows = nil
	q := strings.ToLower(v.Query)
	for _, r := range v.All {
		if m.tab == 1 {
			system := strings.HasPrefix(r.Key, "system/")
			if v.MetricScope == 0 && system || v.MetricScope == 1 && !system {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Key+" "+r.Value+" "+r.Shape), q) {
			continue
		}
		if q == "" && r.Field != nil && r.Depth > 0 {
			visible := true
			for _, parent := range v.All {
				if parent.Depth >= r.Depth {
					continue
				}
				if strings.HasPrefix(r.Key, parent.Key+".") || strings.HasPrefix(r.Key, parent.Key+"[") || strings.HasPrefix(r.Key, parent.Key+"{") {
					if !v.Expanded[parent.Key] {
						visible = false
						break
					}
				}
			}
			if !visible {
				continue
			}
		}
		v.Rows = append(v.Rows, r)
	}
	if m.tab != 5 || v.Sort != 0 {
		pins := map[string]int{}
		if m.tab == 1 {
			for i, key := range m.metricPins(v.Run) {
				pins[key] = i
			}
		}
		sort.SliceStable(v.Rows, func(i, j int) bool {
			a, b := v.Rows[i], v.Rows[j]
			ap, aPinned := pins[a.Key]
			bp, bPinned := pins[b.Key]
			if aPinned != bPinned {
				return aPinned
			}
			if aPinned && ap != bp {
				return ap < bp
			}
			cmp := 0
			switch v.Sort / 2 {
			case 1:
				if a.Metric != nil && b.Metric != nil {
					av, bv := float64(a.Metric.Value), float64(b.Metric.Value)
					af, bf := !math.IsNaN(av), !math.IsNaN(bv)
					if af != bf {
						return af
					}
					if av < bv {
						cmp = -1
					} else if av > bv {
						cmp = 1
					}
				} else {
					cmp = strings.Compare(a.Value, b.Value)
				}
			case 2:
				if a.Metric != nil && b.Metric != nil {
					if a.Metric.Step < b.Metric.Step {
						cmp = -1
					} else if a.Metric.Step > b.Metric.Step {
						cmp = 1
					}
				}
			case 3:
				if a.Metric != nil && b.Metric != nil {
					if a.Metric.Timestamp < b.Metric.Timestamp {
						cmp = -1
					} else if a.Metric.Timestamp > b.Metric.Timestamp {
						cmp = 1
					}
				}
			default:
				cmp = strings.Compare(a.Key, b.Key)
			}
			if cmp == 0 {
				return a.Key < b.Key
			}
			if v.Sort%2 == 1 {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	v.Index = clamp(v.Index, 0, len(v.Rows)-1)
	for i, r := range v.Rows {
		if r.Key == old {
			v.Index = i
			break
		}
	}
	v.Selected = ""
	if len(v.Rows) > 0 {
		v.Selected = v.Rows[v.Index].Key
	}
	if m.tab == 1 && !m.compare {
		m.metric = v.Selected
	}
}
func (m *model) inspectionActions() []action {
	if m.compare {
		return []action{act("inspect-metric", "m", "Choose history metric"), act("inspect-auto", "R", "Toggle automatic metric refresh"), act("inspect-ascii", "u", "Toggle Braille / ASCII curves")}
	}
	if m.focus != 2 || !m.isInspectionTab() || m.run() == nil {
		return nil
	}
	a := []action{act("inspect-search", "/", "Search this table"), act("inspect-sort", "s", "Sort this table"), act("inspect-copy", "Y", "Copy selected value")}
	if m.tab == 1 {
		a = append(a, act("inspect-metric", "m", "Choose metric"), act("inspect-dashboard", "v", "Table / metric dashboard"), act("inspect-overlay", "p", "Overlay up to four metrics"), act("inspect-auto", "R", "Toggle automatic metric refresh"), act("inspect-ascii", "u", "Toggle Braille / ASCII curves"), act("inspect-system", "e", "Model / system / all metrics"))
		a = append(a, act("inspect-pin", "*", "Pin / unpin metric for this experiment"), act("inspect-pin-prev", "<", "Move pinned metric earlier"), act("inspect-pin-next", ">", "Move pinned metric later"))
	}
	if m.tab == 5 {
		a = append(a, act("inspect-dataset-prev", "{", "Previous dataset"), act("inspect-dataset-next", "}", "Next dataset"), act("inspect-raw", "v", "Schema table / raw JSON"), act("inspect-expand", "space", "Expand / collapse nested field"))
	}
	return a
}
func (m *model) inspectionFooter() string {
	if strings.HasPrefix(m.overlay, "inspect-") {
		switch m.overlay {
		case "inspect-value":
			return "↑↓/jk scroll · Y copy full value · Esc return"
		case "inspect-overlay":
			return "Space toggle (maximum four) · * pin · < > order · Enter apply · / search · Esc cancel"
		default:
			return "↑↓ select · Enter apply · / search · Esc return"
		}
	}
	if m.overlay != "" || m.inputMode != "" || m.targetForm != nil || m.focus != 2 || !m.isInspectionTab() {
		return ""
	}
	if m.tab == 1 {
		if v := m.inspectionView(); v != nil && v.ExpandedChart {
			return "←→/hl sample · a axis · p overlay · u ASCII · R auto · Esc table · z zoom"
		}
		return "↑↓ select · / search · s sort · Enter curve · v dashboard · p overlay · * pin · < > order · R auto · z zoom"
	}
	if m.tab == 5 {
		return "↑↓ select · / features · Space expand · { } dataset · v raw · Enter full field · Y copy · z zoom"
	}
	return "↑↓ select · / search · s sort · Enter full value · Y copy value · y run ID · z zoom"
}
func (m *model) performInspection(id string) (tea.Cmd, bool) {
	v := m.inspectionView()
	in := !m.compare && m.focus == 2 && m.isInspectionTab()
	switch id {
	case "inspect-search":
		if v == nil {
			return nil, true
		}
		m.inspect.Search.SetValue(v.Query)
		m.inspect.Typing = true
		m.overlay = "inspect-search"
		return m.inspect.Search.Focus(), true
	case "inspect-sort":
		if v == nil {
			return nil, true
		}
		m.inspect.Picker = []string{"Name ascending", "Name descending", "Value ascending", "Value descending"}
		if m.tab == 1 {
			m.inspect.Picker = append(m.inspect.Picker, "Step ascending", "Step descending", "Updated ascending", "Updated descending")
		}
		m.inspect.PickerIndex = v.Sort
		m.overlay = "inspect-sort"
		return nil, true
	case "inspect-copy":
		return m.copyInspection(), true
	case "inspect-metric", "inspect-overlay":
		m.inspect.Search.SetValue("")
		m.inspect.Typing = false
		m.inspect.PickerIndex = 0
		m.overlay = id
		if id == "inspect-overlay" {
			m.beginMetricOverlay(v)
		}
		return nil, true
	case "inspect-pin", "inspect-pin-prev", "inspect-pin-next":
		if v == nil {
			return nil, true
		}
		delta := 0
		if id == "inspect-pin-prev" {
			delta = -1
		}
		if id == "inspect-pin-next" {
			delta = 1
		}
		return m.changeMetricPin(v.Selected, delta), true
	case "inspect-dashboard":
		if v != nil {
			v.Dashboard = !v.Dashboard
			v.ExpandedChart = false
			m.rememberMetricMode(v)
			return m.ensureHistories(false), true
		}
		return nil, true
	case "inspect-ascii":
		m.inspect.ASCII = !m.inspect.ASCII
		return nil, true
	case "inspect-auto":
		m.inspect.Auto = !m.inspect.Auto
		m.inspect.PollGen++
		if !m.inspect.Auto {
			m.status = "Automatic metric refresh off"
			return nil, true
		}
		m.status = "Automatic metric refresh enabled"
		if m.overlay != "" || m.inputMode != "" || m.targetForm != nil || m.work != nil && (m.work.catalog || m.work.modal != "") {
			return m.inspectionTick(), true
		}
		return tea.Batch(m.inspectionTick(), m.pollInspection()), true
	case "inspect-system":
		if v != nil {
			v.MetricScope = (v.MetricScope + 1) % 3
			v.Selected = ""
			v.Index = 0
			m.filterInspection(v)
			return m.ensureHistories(false), true
		}
		return nil, true
	case "inspect-dataset-prev", "inspect-dataset-next":
		if v != nil && len(v.Run.Inputs.DatasetInputs) > 0 {
			d := 1
			if id == "inspect-dataset-prev" {
				d = -1
			}
			v.Dataset = (v.Dataset + d + len(v.Run.Inputs.DatasetInputs)) % len(v.Run.Inputs.DatasetInputs)
			v.DatasetKey = ""
			v.Index = 0
			v.Selected = ""
			v.Expanded = map[string]bool{}
			m.prepareInspection(v)
		}
		return nil, true
	case "inspect-raw":
		if v != nil {
			v.Raw = !v.Raw
			m.detailOffset = 0
		}
		return nil, true
	case "inspect-expand":
		if v != nil && len(v.Rows) > 0 {
			x := v.Rows[v.Index]
			v.Expanded[x.Key] = !v.Expanded[x.Key]
			m.filterInspection(v)
		}
		return nil, true
	case "enter":
		if !in || v == nil {
			return nil, false
		}
		if m.tab == 1 {
			v.ExpandedChart = true
			v.Cursor = -1
			m.rememberMetricMode(v)
			return m.ensureHistories(false), true
		}
		if len(v.Rows) > 0 {
			x := v.Rows[v.Index]
			m.inspect.Value = x.Key + "\n\n" + x.Value
			if x.Field != nil {
				m.inspect.Value = fmt.Sprintf("%s\n\nType: %s\nRequired: %s\nShape: %s", x.Key, x.Value, x.Required, x.Shape)
				if x.Field.Rank > 0 {
					m.inspect.Value += fmt.Sprintf("\nRank: %d", x.Field.Rank)
				}
				if x.Field.Dimensions != nil {
					m.inspect.Value += fmt.Sprintf("\nFeature dimensions (excluding observation axis): %d", *x.Field.Dimensions)
				}
			}
			m.overlay = "inspect-value"
			m.menuIndex = 0
		}
		return nil, true
	case "back":
		if in && v != nil && (v.ExpandedChart || v.Raw) {
			v.ExpandedChart = false
			v.Raw = false
			if m.tab == 1 {
				m.rememberMetricMode(v)
			}
			return m.ensureHistories(false), true
		}
	case "left", "right":
		if m.compare && m.chart {
			d := 1
			if id == "left" {
				d = -1
			}
			m.moveChartCursor(d)
			return nil, true
		}
		if in && v != nil && m.tab == 1 && v.ExpandedChart {
			delta := 1
			if id == "left" {
				delta = -1
			}
			m.moveChartCursor(delta)
			return nil, true
		}
	case "axis":
		if in && v != nil && m.tab == 1 {
			m.elapsed = !m.elapsed
			return nil, true
		}
	}
	return nil, false
}
func (m *model) moveInspection(delta int) tea.Cmd {
	v := m.inspectionView()
	if v == nil {
		return nil
	}
	if v.Raw {
		m.detailOffset = max(0, m.detailOffset+delta)
		return nil
	}
	v.Index = clamp(v.Index+delta, 0, len(v.Rows)-1)
	if len(v.Rows) > 0 {
		v.Selected = v.Rows[v.Index].Key
	}
	if m.tab == 1 {
		m.metric = v.Selected
		v.Cursor = -1
		return m.ensureHistories(false)
	}
	return nil
}
func (m *model) copyInspection() tea.Cmd {
	v := m.inspectionView()
	if v == nil || len(v.Rows) == 0 {
		return nil
	}
	value := v.Rows[v.Index].Value
	if m.tab == 5 {
		value = v.Rows[v.Index].Key
	}
	if m.overlay == "inspect-value" {
		value = m.inspect.Value
	}
	ctx, target := m.ctx, m.active
	return func() tea.Msg { return resultMsg{target, "Copied selected value", platform.Copy(ctx, value)} }
}
func (m *model) inspectionPickerKeys() []string {
	q := strings.ToLower(m.inspect.Search.Value())
	var keys []string
	for _, key := range m.metricPickerCandidates() {
		if strings.Contains(strings.ToLower(key), q) {
			keys = append(keys, key)
		}
	}
	return keys
}
func (m *model) updateInspection(msg tea.Msg) (tea.Cmd, bool) {
	if m.inspect == nil {
		return nil, false
	}
	switch v := msg.(type) {
	case metricPreferencesLoadedMsg:
		return m.acceptMetricPreferences(v), true
	case metricLoadedMsg:
		return m.acceptMetric(v), true
	case inspectionTickMsg:
		if v.Gen != m.inspect.PollGen || !m.inspect.Auto {
			return nil, true
		}
		if m.overlay != "" || m.inputMode != "" || m.targetForm != nil || m.work != nil && (m.work.catalog || m.work.modal != "") {
			return m.inspectionTick(), true
		}
		return tea.Batch(m.inspectionTick(), m.pollInspection()), true
	case inspectionRunsMsg:
		return m.acceptInspectionRuns(v), true
	}
	if !strings.HasPrefix(m.overlay, "inspect-") {
		return nil, false
	}
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		return nil, false
	}
	switch msg.(type) {
	case tea.MouseMotionMsg, tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		return m.inspectionMouse(msg), true
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if m.inspect.Typing && workspaceTextEvent(msg) {
			var cmd tea.Cmd
			m.inspect.Search, cmd = m.inspect.Search.Update(msg)
			m.applyInspectionSearch()
			return cmd, true
		}
		return nil, false
	}
	k := key.String()
	if k == "ctrl+c" {
		return nil, false
	}
	if k == "esc" {
		m.inspect.OverlayDraft, m.inspect.OverlayDraftKey = nil, ""
		if m.overlay == "inspect-search" {
			v := m.inspectionView()
			if v != nil {
				v.Query = ""
				m.filterInspection(v)
			}
		}
		m.overlay = ""
		m.inspect.Typing = false
		m.inspect.Search.Blur()
		return m.ensureHistories(false), true
	}
	if m.overlay == "inspect-value" {
		switch k {
		case "Y":
			return m.copyInspection(), true
		case "up", "k":
			m.menuIndex = max(0, m.menuIndex-1)
		case "down", "j":
			m.menuIndex++
		case "enter":
			m.overlay = ""
		}
		return nil, true
	}
	if m.inspect.Typing {
		if k == "enter" || k == "tab" {
			m.inspect.Typing = false
			m.inspect.Search.Blur()
			if m.overlay == "inspect-search" {
				m.overlay = ""
				return m.ensureHistories(false), true
			}
			return nil, true
		}
		if k == "up" || k == "down" {
			d := 1
			if k == "up" {
				d = -1
			}
			if m.overlay == "inspect-search" {
				return m.moveInspection(d), true
			}
			m.inspect.PickerIndex = max(0, m.inspect.PickerIndex+d)
			return nil, true
		}
		before := m.inspect.Search.Value()
		var cmd tea.Cmd
		m.inspect.Search, cmd = m.inspect.Search.Update(msg)
		if before != m.inspect.Search.Value() {
			m.inspect.PickerIndex = 0
			m.applyInspectionSearch()
		}
		return cmd, true
	}
	if k == "/" && m.overlay != "inspect-sort" {
		m.inspect.Typing = true
		return m.inspect.Search.Focus(), true
	}
	keys := m.inspect.Picker
	if m.overlay == "inspect-metric" || m.overlay == "inspect-overlay" {
		keys = m.inspectionPickerKeys()
	}
	m.inspect.PickerIndex = clamp(m.inspect.PickerIndex, 0, len(keys)-1)
	if (k == "*" || k == "<" || k == ">") && m.overlay != "inspect-sort" && !m.compare && len(keys) > 0 {
		delta := 0
		if k == "<" {
			delta = -1
		}
		if k == ">" {
			delta = 1
		}
		return m.changeMetricPin(keys[m.inspect.PickerIndex], delta), true
	}
	switch k {
	case "up", "k":
		m.inspect.PickerIndex = max(0, m.inspect.PickerIndex-1)
	case "down", "j":
		m.inspect.PickerIndex = min(max(0, len(keys)-1), m.inspect.PickerIndex+1)
	case "home":
		m.inspect.PickerIndex = 0
	case "end", "G":
		m.inspect.PickerIndex = max(0, len(keys)-1)
	case "space", "enter":
		v := m.inspectionView()
		if m.overlay == "inspect-overlay" && v != nil {
			if k == "enter" {
				return m.applyMetricOverlay(v), true
			}
			if len(keys) > 0 {
				m.toggleMetricOverlay(keys[m.inspect.PickerIndex])
			}
			return nil, true
		}
		if k != "enter" {
			break
		}
		if m.compare && m.overlay == "inspect-metric" && len(keys) > 0 {
			m.metric = keys[m.inspect.PickerIndex]
			m.chart = true
			m.overlay = ""
			m.inspect.CompareCursor = -1
			return m.ensureHistories(false), true
		}
		if m.overlay == "inspect-sort" && v != nil {
			v.Sort = m.inspect.PickerIndex
			m.filterInspection(v)
		} else if len(keys) > 0 && v != nil {
			v.Selected = keys[m.inspect.PickerIndex]
			v.MetricScope = 2
			v.Query = ""
			m.filterInspection(v)
			m.metric = keys[m.inspect.PickerIndex]
			v.ExpandedChart = true
			m.rememberMetricMode(v)
		}
		m.overlay = ""
		return m.ensureHistories(false), true
	}
	return nil, true
}
func (m *model) applyInspectionSearch() {
	if m.overlay != "inspect-search" {
		return
	}
	v := m.inspectionView()
	if v == nil {
		return
	}
	v.Query = m.inspect.Search.Value()
	v.Selected = ""
	v.Index = 0
	m.filterInspection(v)
}
func (m *model) inspectionOverlay(w, h int) (string, bool) {
	if !strings.HasPrefix(m.overlay, "inspect-") {
		return "", false
	}
	var lines []string
	title := "Inspect"
	if m.overlay == "inspect-search" {
		v := m.inspectionView()
		lines = []string{m.inspect.Search.View(), "Enter keeps query · Esc clears · ↑↓ select"}
		if v != nil {
			lines = append(lines, fmt.Sprintf("%d / %d rows match", len(v.Rows), len(v.All)))
			for _, r := range v.Rows {
				lines = append(lines, r.Key+" = "+r.Value)
			}
		}
		title = "Search detail table"
	} else if m.overlay == "inspect-value" {
		all := strings.Split(m.inspect.Value, "\n")
		var wrapped []string
		for _, line := range all {
			wrapped = append(wrapped, wrapInspection(line, max(1, w-4))...)
		}
		offset := clamp(m.menuIndex, 0, max(0, len(wrapped)-h+2))
		lines = wrapped[offset:]
		title = "Full value"
	} else {
		keys := m.inspect.Picker
		if m.overlay != "inspect-sort" {
			keys = m.inspectionPickerKeys()
			lines = append(lines, m.inspect.Search.View())
		}
		if m.overlay == "inspect-overlay" {
			title = "Overlay metrics · this experiment / session · maximum four"
		} else if m.overlay == "inspect-sort" {
			title = "Sort table"
		} else {
			title = "Choose metric"
		}
		index := clamp(m.inspect.PickerIndex, 0, len(keys)-1)
		start := listStart(index, len(keys), max(1, h-3))
		for i := start; i < min(len(keys), start+max(1, h-3)); i++ {
			label := keys[i]
			if !m.compare && m.overlay != "inspect-sort" {
				label = missingMetricLabel(m.run(), label)
				if slices.Contains(m.metricPins(m.run()), keys[i]) {
					label = "* " + label
				}
			}
			if m.overlay == "inspect-overlay" {
				mark := "[ ] "
				if slices.Contains(m.inspect.OverlayDraft, keys[i]) {
					mark = "[x] "
				}
				label = mark + label
			}
			lines = append(lines, row(label, i == index, w-2))
		}
		if len(keys) == 0 {
			lines = append(lines, "No matching metrics")
		}
	}
	return frame(title, lines, w, h, true), true
}
func wrapInspection(s string, w int) []string {
	r := []rune(clean(s))
	if len(r) == 0 {
		return []string{""}
	}
	var out []string
	for len(r) > 0 {
		n := min(len(r), w)
		for n > 1 && textWidth(string(r[:n])) > w {
			n--
		}
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}
func (m *model) inspectionContent(w, h int) paneContent {
	p := paneContent{}
	v := m.inspectionView()
	if v == nil {
		return paneContent{Lines: []string{"Choose a run to inspect."}}
	}
	if m.tab == 1 {
		return m.metricContent(v, w, h)
	}
	if m.tab == 5 {
		r := v.Run
		if len(r.Inputs.DatasetInputs) == 0 {
			return paneContent{Lines: []string{"No dataset inputs logged for this run."}}
		}
		d := r.Inputs.DatasetInputs[v.Dataset]
		if len(r.Inputs.DatasetInputs) > 1 {
			line := "Datasets: "
			for i, input := range r.Inputs.DatasetInputs {
				label := clean(input.Dataset.Name)
				if i == v.Dataset {
					label = "[" + label + "]"
				}
				start := textWidth(line)
				p.Hits = append(p.Hits, hit{rect{start, len(p.Lines), textWidth(label), 1}, "inspect-dataset:" + strconv.Itoa(i)})
				line += label + "  "
			}
			p.add(line)
		}
		p.add(fmt.Sprintf("Dataset %d/%d · %s · %s", v.Dataset+1, len(r.Inputs.DatasetInputs), clean(d.Dataset.Name), findKV(d.Tags, "mlflow.data.context")))
		rows := "unknown rows"
		if v.Profile.Rows != nil {
			rows = fmt.Sprintf("%s logged rows", groupedInteger(*v.Profile.Rows))
		}
		counts := fmt.Sprintf("%d columns · %d leaves", v.Schema.Columns, v.Schema.Leaves)
		if v.Schema.Kind == "tensor" {
			counts = fmt.Sprintf("%d tensors · shape includes observation axis", len(v.Schema.Fields))
		}
		p.add(fmt.Sprintf("%s · %s · %d shown", counts, rows, len(v.Rows)))
		if dimension := findKV(r.Data.Params, "feature_dim"); dimension != "—" {
			p.add("Logged feature_dim=" + dimension)
		}
		if v.Raw || v.Schema.Error != "" || len(v.Schema.Fields) == 0 {
			if v.Schema.Error != "" {
				p.add("Schema: " + clean(v.Schema.Error))
			}
			raw := "Digest: " + d.Dataset.Digest + "\nSource: " + d.Dataset.SourceType + " " + d.Dataset.Source + "\nSchema: " + d.Dataset.Schema + "\nProfile: " + d.Dataset.Profile
			lines := wrapInspection(raw, max(1, w))
			offset := clamp(m.detailOffset, 0, max(0, len(lines)-1))
			p.Lines = append(p.Lines, lines[offset:min(len(lines), offset+max(1, h-len(p.Lines)))]...)
			return p
		}
	}
	if v.Query != "" {
		p.add(fmt.Sprintf("Search: %s · %d / %d matches", clean(v.Query), len(v.Rows), len(v.All)))
	}
	keyWidth := max(10, w/2)
	if m.tab == 5 {
		keyWidth = max(10, w-30)
		p.add(dimStyle.Render(textFit("#  Feature / path", keyWidth) + textFit("Type", 14) + textFit("Required", 9) + "Shape"))
	} else {
		p.add(dimStyle.Render(textFit("Name", keyWidth) + "Value"))
	}
	capacity := max(1, h-len(p.Lines))
	start := listStart(v.Index, len(v.Rows), capacity)
	for i := start; i < min(len(v.Rows), start+capacity); i++ {
		x := v.Rows[i]
		label := textFit(x.Key, keyWidth-2) + textFit(x.Value, max(1, w-keyWidth))
		if m.tab == 5 {
			marker := ""
			if x.Field != nil && len(x.Field.Children) > 0 {
				marker = "▸ "
				if v.Expanded[x.Key] {
					marker = "▾ "
				}
			}
			label = textFit(fmt.Sprintf("%d %s%s%s", x.Index, strings.Repeat(" ", x.Depth), marker, x.Key), keyWidth-2) + textFit(x.Value, 14) + textFit(x.Required, 9) + textFit(x.Shape, max(0, w-keyWidth-23))
		}
		p.addHit(row(label, i == v.Index, w), "inspect-row:"+x.Key, w)
	}
	if len(v.Rows) == 0 {
		p.add("No matching rows. / changes the search.")
	}
	return p
}
func groupedInteger(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func (m *model) activateInspectionHit(id string) (tea.Cmd, bool) {
	if strings.HasPrefix(id, "inspect-dataset:") {
		v := m.inspectionView()
		if v == nil {
			return nil, true
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(id, "inspect-dataset:"))
		v.Dataset = clamp(i, 0, len(v.Run.Inputs.DatasetInputs)-1)
		v.DatasetKey = ""
		v.Index = 0
		v.Selected = ""
		m.focus = 2
		m.prepareInspection(v)
		return nil, true
	}

	if strings.HasPrefix(id, "inspect-row:") {
		v := m.inspectionView()
		if v == nil {
			return nil, true
		}
		key := strings.TrimPrefix(id, "inspect-row:")
		i := -1
		for j, row := range v.Rows {
			if row.Key == key {
				i = j
				break
			}
		}
		if i < 0 {
			return nil, true
		}
		m.focus = 2
		v.Index = i
		if len(v.Rows) > 0 {
			v.Selected = v.Rows[v.Index].Key
		}
		if m.tab == 1 {
			m.metric = v.Selected
			v.Cursor = -1
			return m.ensureHistories(false), true
		}
		return nil, true
	}
	if strings.HasPrefix(id, "inspect-chart:") {
		v := m.inspectionView()
		if v == nil {
			return nil, true
		}
		parts := strings.Split(strings.TrimPrefix(id, "inspect-chart:"), ":")
		if len(parts) == 2 {
			x, _ := strconv.Atoi(parts[0])
			width, _ := strconv.Atoi(parts[1])
			m.setChartCursorFraction(float64(x) / float64(max(1, width-1)))
			m.focus = 2
		}
		return nil, true
	}
	return nil, false
}

type inspectionTickMsg struct{ Gen uint64 }

func (m *model) inspectionTick() tea.Cmd {
	if !m.inspect.Auto {
		return nil
	}
	gen := m.inspect.PollGen
	seconds := m.opts.RefreshSeconds
	if seconds <= 0 {
		seconds = 5
	}
	return tea.Tick(time.Duration(seconds)*time.Second, func(time.Time) tea.Msg { return inspectionTickMsg{gen} })
}

func (m *model) inspectionOverlayHit(x, y int) string {
	r := m.geometry().Content
	if !r.contains(x, y) || x <= r.X || x >= r.X+r.W-1 || y <= r.Y || y >= r.Y+r.H-1 {
		return ""
	}
	localY := y - r.Y - 1
	if m.overlay == "inspect-value" {
		return ""
	}
	if m.overlay == "inspect-search" {
		if localY == 0 {
			return "search"
		}
		v := m.inspectionView()
		if v != nil && localY >= 3 && localY-3 < len(v.Rows) {
			return "row:" + v.Rows[localY-3].Key
		}
		return ""
	}
	keys := m.inspect.Picker
	prefix := 0
	if m.overlay != "inspect-sort" {
		keys = m.inspectionPickerKeys()
		prefix = 1
		if localY == 0 {
			return "search"
		}
	}
	index := clamp(m.inspect.PickerIndex, 0, len(keys)-1)
	start := listStart(index, len(keys), max(1, r.H-3))
	i := start + localY - prefix
	if localY < prefix || i < 0 || i >= len(keys) || localY >= r.H-2 {
		return ""
	}
	if m.overlay == "inspect-sort" {
		return "sort:" + strconv.Itoa(i)
	}
	return "metric:" + keys[i]
}
func (m *model) inspectionMouse(msg tea.Msg) tea.Cmd {
	if !m.layout.Mouse {
		return nil
	}
	var event tea.Mouse
	switch v := msg.(type) {
	case tea.MouseClickMsg:
		event = v.Mouse()
	case tea.MouseReleaseMsg:
		event = v.Mouse()
	case tea.MouseMotionMsg:
		return nil
	case tea.MouseWheelMsg:
		d := 1
		if v.Mouse().Button == tea.MouseWheelUp {
			d = -1
		}
		if m.overlay == "inspect-value" {
			m.menuIndex = max(0, m.menuIndex+d)
			return nil
		}
		if m.overlay == "inspect-search" {
			return m.moveInspection(d)
		}
		keys := m.inspect.Picker
		if m.overlay != "inspect-sort" {
			keys = m.inspectionPickerKeys()
		}
		m.inspect.PickerIndex = clamp(m.inspect.PickerIndex+d, 0, len(keys)-1)
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		if event.Button == tea.MouseLeft {
			m.mousePressed = m.inspectionOverlayHit(event.X, event.Y)
			m.mouseContext = m.mouseScope()
		}
		return nil
	}
	if event.Button != tea.MouseLeft && event.Button != tea.MouseNone {
		return nil
	}
	id := m.mousePressed
	m.mousePressed = ""
	if id == "" || m.mouseContext != m.mouseScope() || id != m.inspectionOverlayHit(event.X, event.Y) {
		return nil
	}
	if id == "search" {
		m.inspect.Typing = true
		return m.inspect.Search.Focus()
	}
	kind, key, _ := strings.Cut(id, ":")
	v := m.inspectionView()
	switch kind {
	case "sort":
		if v != nil {
			v.Sort, _ = strconv.Atoi(key)
			m.filterInspection(v)
			m.overlay = ""
		}
	case "row":
		if v != nil {
			v.Selected = key
			m.filterInspection(v)
			m.overlay = ""
			m.inspect.Typing = false
			return m.ensureHistories(false)
		}
	case "metric":
		if m.overlay == "inspect-overlay" && v != nil {
			m.toggleMetricOverlay(key)
			return nil
		}
		m.metric = key
		if m.compare {
			m.chart = true
			m.inspect.CompareCursor = -1
		} else if v != nil {
			v.Selected = key
			v.Query = ""
			v.MetricScope = 2
			v.ExpandedChart = true
			m.filterInspection(v)
			m.rememberMetricMode(v)
		}
		m.overlay = ""
		m.inspect.Typing = false
		return m.ensureHistories(false)
	}
	return nil
}
