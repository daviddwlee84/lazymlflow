package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// Overlay and display mode are browsing choices for this session. Pins live in
// the existing experiment view, independently of which run list is on screen.
type experimentMetricPreferences struct {
	Overlay         []string
	Mode            string
	Known           map[string]bool
	Revision        uint64
	Loading, Loaded bool
	PinsDirty       bool
	PinEdits        uint64
}

type metricPreferencesLoadedMsg struct {
	target, source, experiment string
	revision, pinEdits         uint64
	value                      core.ExperimentView
	found                      bool
	err                        error
}

func metricExperimentKey(source, experiment string) string { return source + "\x00" + experiment }

func (m *model) metricExperimentState(run *core.Run) *runState {
	s := m.state()
	if s == nil || run == nil || run.Info.ExperimentID == "" {
		return nil
	}
	id := run.Info.ExperimentID
	if s.Runs[id] == nil {
		s.Runs[id] = &runState{listState: listState{Order: "attributes.start_time DESC"}, View: core.DefaultView(m.metricColumns, m.paramColumns)}
	}
	return s.Runs[id]
}

func (m *model) metricPreferences(run *core.Run) *experimentMetricPreferences {
	if m.inspect == nil || run == nil || run.Info.ExperimentID == "" {
		return nil
	}
	if m.inspect.MetricPreferences == nil {
		m.inspect.MetricPreferences = map[string]*experimentMetricPreferences{}
	}
	key := metricExperimentKey(m.historyNamespace(), run.Info.ExperimentID)
	p := m.inspect.MetricPreferences[key]
	if p == nil {
		p = &experimentMetricPreferences{Known: map[string]bool{}}
		m.inspect.MetricPreferences[key] = p
	}
	for _, metric := range run.Data.Metrics {
		p.Known[metric.Key] = true
	}
	return p
}

func metricMetadataKey(run *core.Run) string {
	// Metric values can change without replacing the containing Run pointer.
	// Number's JSON form also keeps NaN stable across otherwise equal refreshes.
	b, _ := json.Marshal(run.Data.Metrics)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (m *model) syncMetricPreferences(v *inspectionView, run *core.Run) bool {
	p, state := m.metricPreferences(run), m.metricExperimentState(run)
	if p == nil || state == nil {
		return false
	}
	metadata := metricMetadataKey(run)
	changed := v.MetricMetadata != metadata || v.MetricRevision != p.Revision || v.MetricViewRevision != state.ViewRevision
	v.MetricMetadata, v.MetricRevision, v.MetricViewRevision = metadata, p.Revision, state.ViewRevision
	v.Overlay = slices.Clone(p.Overlay)
	if p.Mode != "" {
		v.Dashboard, v.ExpandedChart = p.Mode == "dashboard", p.Mode == "curve"
	}
	return changed
}

func (m *model) rememberMetricMode(v *inspectionView) {
	if v == nil {
		return
	}
	p := m.metricPreferences(v.Run)
	if p == nil {
		return
	}
	mode := "table"
	if v.ExpandedChart {
		mode = "curve"
	} else if v.Dashboard {
		mode = "dashboard"
	}
	if p.Mode != mode {
		p.Mode = mode
		p.Revision++
	}
}

func (m *model) metricPins(run *core.Run) []string {
	s := m.state()
	if s == nil || run == nil || s.Runs[run.Info.ExperimentID] == nil {
		return nil
	}
	return s.Runs[run.Info.ExperimentID].View.MetricPins
}

func (m *model) loadMetricPreferences() tea.Cmd {
	run := m.run()
	p, state := m.metricPreferences(run), m.metricExperimentState(run)
	if p == nil || state == nil || p.Loaded || p.Loading {
		return nil
	}
	if m.opts.State == nil {
		p.Loaded = true
		return nil
	}
	p.Loading = true
	target, source, experiment := m.active, m.historyNamespace(), run.Info.ExperimentID
	revision, edits, db := state.ViewRevision, p.PinEdits, m.opts.State
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		v, found, err := db.LoadView(ctx, source, experiment)
		return metricPreferencesLoadedMsg{target, source, experiment, revision, edits, v, found, err}
	}
}

func (m *model) acceptMetricPreferences(v metricPreferencesLoadedMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil {
		return nil
	}
	validSource := false
	for _, target := range m.targets {
		if target.ID == v.target && core.SourceKey(target) == v.source {
			validSource = true
			break
		}
	}
	if !validSource {
		return nil
	}
	p := m.inspect.MetricPreferences[metricExperimentKey(v.source, v.experiment)]
	state := s.Runs[v.experiment]
	if p == nil || state == nil || !p.Loading {
		return nil
	}
	p.Loading = false
	if v.err != nil {
		m.status = "Could not load metric preferences: " + v.err.Error()
		return nil
	}
	p.Loaded = true
	// Pin edits made while loading are replayed over the saved view, so opening
	// a run through Activity cannot overwrite its unrelated saved columns/filter.
	if v.found && state.ViewRevision == v.revision+p.PinEdits-v.pinEdits {
		pins := slices.Clone(state.View.MetricPins)
		state.View = core.NormalizeView(cloneView(v.value))
		state.Filter = state.View.Filter
		if order, ok := core.ServerOrder(state.View.Sort); ok {
			state.Order = joinOrder(order)
		}
		if p.PinsDirty {
			state.View.MetricPins = pins
		}
	}
	p.Revision++
	var save tea.Cmd
	if p.PinsDirty {
		p.PinsDirty = false
		save = m.saveMetricView(v.source, v.experiment, state)
	}
	if m.active == v.target {
		m.syncInspection()
		return tea.Batch(save, m.ensureHistories(false))
	}
	return save
}

func (m *model) saveMetricView(source, experiment string, state *runState) tea.Cmd {
	if m.opts.State == nil {
		return m.saveEffect()
	}
	value, db := cloneView(state.View), m.opts.State
	m.writer.add("view/"+source+"/"+experiment, func(ctx context.Context) error { return db.SaveView(ctx, source, experiment, value) })
	return m.saveEffect()
}

func (m *model) changeMetricPin(key string, move int) tea.Cmd {
	run := m.run()
	p, state := m.metricPreferences(run), m.metricExperimentState(run)
	if p == nil || state == nil || key == "" {
		return nil
	}
	pins := slices.Clone(state.View.MetricPins)
	i := slices.Index(pins, key)
	if move == 0 {
		if i < 0 {
			pins = append(pins, key)
		} else {
			pins = append(pins[:i], pins[i+1:]...)
		}
	} else if i < 0 {
		m.status = "Pin this metric with * before changing its order"
		return nil
	} else if next := i + move; next >= 0 && next < len(pins) {
		pins[i], pins[next] = pins[next], pins[i]
	} else {
		return nil
	}
	state.View.MetricPins = pins
	state.ViewRevision++
	p.Revision++
	p.PinEdits++
	m.syncInspection()
	if m.overlay == "inspect-overlay" || m.overlay == "inspect-metric" {
		if index := slices.Index(m.inspectionPickerKeys(), key); index >= 0 {
			m.inspect.PickerIndex = index
		}
	}
	if !p.Loaded && m.opts.State != nil {
		p.PinsDirty = true
		// Own the pin write immediately, even if the user quits before the load
		// result is processed. Merge only pins while the rest of the saved view
		// is still unknown; a later load will replay these edits in memory.
		source, experiment, db := m.historyNamespace(), run.Info.ExperimentID, m.opts.State
		fallback, selected := cloneView(state.View), slices.Clone(pins)
		if state.ViewRevision > p.PinEdits {
			return tea.Batch(m.loadMetricPreferences(), m.saveMetricView(source, experiment, state))
		}
		m.writer.add("view/"+source+"/"+experiment, func(ctx context.Context) error {
			value, found, err := db.LoadView(ctx, source, experiment)
			if err != nil {
				return err
			}
			if !found {
				value = fallback
			}
			value.MetricPins = slices.Clone(selected)
			return db.SaveView(ctx, source, experiment, value)
		})
		return tea.Batch(m.loadMetricPreferences(), m.saveEffect())
	}
	return m.saveMetricView(m.historyNamespace(), run.Info.ExperimentID, state)
}

func (m *model) metricPickerCandidates() []string {
	if m.compare {
		return m.metricKeys()
	}
	run := m.run()
	p := m.metricPreferences(run)
	if p == nil {
		return m.metricKeys()
	}
	seen := make(map[string]bool, len(p.Known))
	for key := range p.Known {
		seen[key] = true
	}
	for _, key := range append(slices.Clone(p.Overlay), m.inspect.OverlayDraft...) {
		seen[key] = true
	}
	var keys []string
	for _, key := range m.metricPins(run) {
		keys = append(keys, key)
		delete(seen, key)
	}
	var remaining []string
	for key := range seen {
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

func (m *model) beginMetricOverlay(v *inspectionView) {
	if v == nil || v.Run == nil {
		return
	}
	m.inspect.OverlayDraft = slices.Clone(v.Overlay)
	m.inspect.OverlayDraftKey = metricExperimentKey(m.historyNamespace(), v.Run.Info.ExperimentID)
}

func (m *model) toggleMetricOverlay(key string) {
	draft := m.inspect.OverlayDraft
	if i := slices.Index(draft, key); i >= 0 {
		m.inspect.OverlayDraft = append(draft[:i], draft[i+1:]...)
	} else if len(draft) < 4 {
		m.inspect.OverlayDraft = append(draft, key)
	} else {
		m.status = "Overlay supports at most four metrics"
	}
}

func (m *model) applyMetricOverlay(v *inspectionView) tea.Cmd {
	if v == nil || v.Run == nil || m.inspect.OverlayDraftKey != metricExperimentKey(m.historyNamespace(), v.Run.Info.ExperimentID) {
		m.overlay = ""
		return nil
	}
	p := m.metricPreferences(v.Run)
	p.Overlay = slices.Clone(m.inspect.OverlayDraft)
	p.Mode = "curve"
	p.Revision++
	v.Overlay = slices.Clone(p.Overlay)
	v.ExpandedChart, v.Dashboard, v.Cursor = true, false, -1
	m.inspect.OverlayDraft, m.inspect.OverlayDraftKey = nil, ""
	m.overlay = ""
	return m.ensureHistories(false)
}

func missingMetricLabel(run *core.Run, key string) string {
	if run != nil {
		if _, exists := run.Metric(key); !exists {
			return fmt.Sprintf("%s · not logged in this run", key)
		}
	}
	return key
}
