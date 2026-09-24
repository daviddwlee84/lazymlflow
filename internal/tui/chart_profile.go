package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type chartProfile struct {
	Smooth, Normalize, Extrema bool
	Span                       int
	Draw                       string            // points, lines, both; empty means both
	Best                       map[string]string // exact metric key => min or max; absent displays both
}

func (p chartProfile) span() int {
	if p.Span < 1 {
		return 10
	}
	return p.Span
}
func (p chartProfile) draw() string {
	if p.Draw == "" {
		return "both"
	}
	return p.Draw
}
func cloneChartProfile(p chartProfile) chartProfile {
	best := map[string]string{}
	for k, v := range p.Best {
		best[k] = v
	}
	p.Best = best
	return p
}

func (m *model) chartContextKey(run *core.Run) string {
	if m.compare {
		return m.historyNamespace() + "\x00compare"
	}
	if scope := m.activityScope(); scope != scopeExperiment {
		return m.historyNamespace() + "\x00activity:" + string(scope)
	}
	if run == nil {
		return m.historyNamespace() + "\x00chart"
	}
	return metricExperimentKey(m.historyNamespace(), run.Info.ExperimentID)
}

func (m *model) chartContextLabel() string {
	if m.compare {
		return "Comparison"
	}
	if scope := m.activityScope(); scope != scopeExperiment {
		return activityLabel(scope)
	}
	return "This experiment"
}

// Activity profiles are seeded once. Returning to a normal experiment never
// copies Activity settings back into that experiment's preferences.
func (m *model) chartPreferences(run *core.Run) *experimentMetricPreferences {
	base := m.metricPreferences(run)
	if !m.compare && m.activityScope() == scopeExperiment {
		if run != nil {
			if m.inspect.LastExperimentChart == nil {
				m.inspect.LastExperimentChart = map[string]string{}
			}
			m.inspect.LastExperimentChart[m.historyNamespace()] = metricExperimentKey(m.historyNamespace(), run.Info.ExperimentID)
		}
		return base
	}
	if m.inspect.ChartContexts == nil {
		m.inspect.ChartContexts = map[string]*experimentMetricPreferences{}
	}
	key := m.chartContextKey(run)
	p := m.inspect.ChartContexts[key]
	if p == nil {
		p = &experimentMetricPreferences{Known: map[string]bool{}}
		if !m.compare {
			if previous := m.inspect.MetricPreferences[m.inspect.LastExperimentChart[m.historyNamespace()]]; previous != nil {
				base = previous
			}
		}
		if base != nil && !m.compare {
			p.Overlay, p.OverlayDisabled, p.Mode = slices.Clone(base.Overlay), base.OverlayDisabled, base.Mode
			p.Chart = cloneChartProfile(base.Chart)
			for key := range base.Known {
				p.Known[key] = true
			}
		}
		m.inspect.ChartContexts[key] = p
	}
	if run != nil {
		for _, metric := range run.Data.Metrics {
			p.Known[metric.Key] = true
		}
	}
	return p
}

func (m *model) currentChartProfile() chartProfile {
	// Views are pure: profile creation belongs to navigation and effects.
	if m.inspect == nil {
		return chartProfile{}
	}
	key := m.chartContextKey(m.run())
	if p := m.inspect.ChartContexts[key]; p != nil {
		return p.Chart
	}
	if p := m.inspect.MetricPreferences[key]; p != nil {
		return p.Chart
	}
	return chartProfile{}
}

func (m *model) chartActions() []action {
	if m.compare && !m.chart {
		return nil
	}
	if !m.compare && (m.focus != 2 || m.tab != 1 || m.run() == nil) {
		return nil
	}
	a := []action{act("chart-options", "f", "Chart options / reset"), act("chart-smooth", "F", "Toggle EMA smoothing"), act("chart-normalize", "n", "Normalize each series to 0–1"), act("chart-draw", "d", "Raw points / lines / both"), act("chart-extrema", "b", "Toggle raw extrema markers")}
	if !m.compare {
		a = append(a, act("chart-overlay-toggle", "0", "Toggle overlay without clearing selected metrics"))
	}
	return a
}

func (m *model) performChartAction(id string) (tea.Cmd, bool) {
	if !strings.HasPrefix(id, "chart-") {
		return nil, false
	}
	p := m.chartPreferences(m.run())
	if p == nil {
		return nil, true
	}
	switch id {
	case "chart-options":
		m.overlay = "inspect-chart-options"
		m.inspect.PickerIndex = 0
		m.inspect.Typing = false
		return nil, true
	case "chart-smooth":
		p.Chart.Smooth = !p.Chart.Smooth
	case "chart-normalize":
		p.Chart.Normalize = !p.Chart.Normalize
	case "chart-extrema":
		p.Chart.Extrema = !p.Chart.Extrema
	case "chart-draw":
		switch p.Chart.draw() {
		case "both":
			p.Chart.Draw = "points"
		case "points":
			p.Chart.Draw = "lines"
		default:
			p.Chart.Draw = "both"
		}
	case "chart-overlay-toggle":
		if len(p.Overlay) == 0 {
			m.status = "No overlay selected; p chooses metrics"
			return nil, true
		}
		p.OverlayDisabled = !p.OverlayDisabled
		m.status = "Overlay on"
		if p.OverlayDisabled {
			m.status = "Overlay off; selected metric keys retained"
		}
	default:
		return nil, false
	}
	p.Revision++
	m.syncInspection()
	return m.ensureHistories(false), true
}

func (m *model) chartOptionRows() []string {
	p := m.currentChartProfile()
	rows := []string{fmt.Sprintf("EMA smoothing: %t", p.Smooth), fmt.Sprintf("EMA span: %d samples · ←/→ adjust", p.span()), fmt.Sprintf("Per-series 0–1 normalization: %t", p.Normalize), "Drawing: " + p.draw(), fmt.Sprintf("Raw min/max markers: %t", p.Extrema)}
	keys := []string{}
	if m.compare {
		keys = []string{m.metric}
	} else if v := m.inspectionView(); v != nil {
		keys = slices.Clone(v.Overlay)
		if len(keys) == 0 {
			keys = []string{v.Selected}
		}
	}
	for _, key := range keys {
		if key != "" {
			best := p.Best[key]
			if best == "" {
				best = "both extrema"
			}
			rows = append(rows, "Best "+key+": "+best)
		}
	}
	return append(rows, "Reset chart options and clear overlay")
}

func (m *model) changeChartOption(index, direction int) tea.Cmd {
	p := m.chartPreferences(m.run())
	rows := m.chartOptionRows()
	if p == nil || index < 0 || index >= len(rows) {
		return nil
	}
	switch index {
	case 0:
		p.Chart.Smooth = !p.Chart.Smooth
	case 1:
		if direction == 0 {
			direction = 1
		}
		p.Chart.Span = clamp(p.Chart.span()+direction, 1, 10000)
	case 2:
		p.Chart.Normalize = !p.Chart.Normalize
	case 3:
		switch p.Chart.draw() {
		case "both":
			p.Chart.Draw = "points"
		case "points":
			p.Chart.Draw = "lines"
		default:
			p.Chart.Draw = "both"
		}
	case 4:
		p.Chart.Extrema = !p.Chart.Extrema
	default:
		if index == len(rows)-1 {
			p.Chart = chartProfile{}
			p.Overlay = nil
			p.OverlayDisabled = false
		} else {
			key := strings.TrimPrefix(rows[index], "Best ")
			key = key[:strings.LastIndex(key, ": ")]
			if p.Chart.Best == nil {
				p.Chart.Best = map[string]string{}
			}
			switch p.Chart.Best[key] {
			case "":
				p.Chart.Best[key] = "min"
			case "min":
				p.Chart.Best[key] = "max"
			default:
				delete(p.Chart.Best, key)
			}
		}
	}
	p.Revision++
	m.syncInspection()
	m.inspect.PickerIndex = clamp(m.inspect.PickerIndex, 0, len(m.chartOptionRows())-1)
	return m.ensureHistories(false)
}

func (m *model) chartOptionsKey(key string) tea.Cmd {
	count := len(m.chartOptionRows())
	switch key {
	case "up", "k":
		m.inspect.PickerIndex = max(0, m.inspect.PickerIndex-1)
	case "down", "j":
		m.inspect.PickerIndex = min(count-1, m.inspect.PickerIndex+1)
	case "home":
		m.inspect.PickerIndex = 0
	case "end", "G":
		m.inspect.PickerIndex = count - 1
	case "enter", "space", "right", "l", "+":
		return m.changeChartOption(m.inspect.PickerIndex, 1)
	case "left", "h", "-":
		return m.changeChartOption(m.inspect.PickerIndex, -1)
	}
	return nil
}

func (m *model) chartOptionsHit(x, y int) string {
	r := m.geometry().Content
	if !r.contains(x, y) || x <= r.X || x >= r.X+r.W-1 || y <= r.Y || y >= r.Y+r.H-1 {
		return ""
	}
	rows := m.chartOptionRows()
	start := listStart(m.inspect.PickerIndex, len(rows), max(1, r.H-2))
	i := start + y - r.Y - 1
	if i < 0 || i >= len(rows) {
		return ""
	}
	return "chart-option:" + strconv.Itoa(i)
}
