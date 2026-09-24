package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/activity"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// A virtual list is never an experiment ID. The real browser selection stays
// intact while Activity owns its own rows and inspection selection.
type runListScope string

const (
	scopeExperiment runListScope = ""
	scopeRunning    runListScope = "running"
	scopeRecent     runListScope = "recent"
	scopeUnread     runListScope = "unread"
	scopeAlerts     runListScope = "alerts"
	scopePinned     runListScope = "pinned"
)

var activityScopes = []runListScope{scopeRunning, scopeRecent, scopeUnread, scopeAlerts, scopePinned}

func (m *model) sidebarActivityScopes() []runListScope {
	if m.runPinStore() == nil {
		return activityScopes[:len(activityScopes)-1]
	}
	return activityScopes
}

type activityState struct {
	Scope, LastScope                                     runListScope
	Snapshot                                             core.ActivitySnapshot
	Views                                                map[runListScope]*runState
	Runs                                                 map[string]*core.Run
	Inspect                                              *core.Run
	Loaded, Loading, Pending, FullPending, PolicyLoading bool
	Gen, FullGen, RunGen                                 uint64
	RunPending                                           string
	Err, AlertMode                                       string
	RecentLimit                                          int
	FullAttempted                                        map[string]bool
	ReadReceipts                                         map[string]int64
	AckReceipts                                          map[string]bool
	ScopeCounts                                          map[runListScope]int
	ExperimentNames                                      map[string]string
	MetadataAt                                           map[string]int64
	PolicyEdits                                          map[string]core.ActivityPolicy
	RetainedRun                                          string
	SuppressRetention                                    bool
	ManualRefreshGen                                     uint64
	ManualRefreshScope                                   runListScope
	Pins                                                 *runPinState
}

func (m *model) activityStore() core.ActivityStore {
	store, _ := m.opts.State.(core.ActivityStore)
	return store
}
func (m *model) activityAvailable() bool { return m.activityStore() != nil && m.state() != nil }
func (m *model) activityCurrent() *activityState {
	return m.activities[core.SourceKey(m.target())]
}
func (m *model) activityState() *activityState {
	if !m.activityAvailable() {
		return nil
	}
	if m.activities == nil {
		m.activities = map[string]*activityState{}
	}
	key := core.SourceKey(m.target())
	a := m.activities[key]
	if a == nil {
		a = &activityState{LastScope: scopeUnread, Snapshot: core.NewActivitySnapshot(key), Views: map[runListScope]*runState{}, Runs: map[string]*core.Run{}, AlertMode: "active", RecentLimit: 100, FullAttempted: map[string]bool{}, ReadReceipts: map[string]int64{}, AckReceipts: map[string]bool{}, MetadataAt: map[string]int64{}}
		for _, scope := range activityScopes {
			v := core.DefaultView(m.metricColumns, m.paramColumns)
			v.Mode = "flat"
			v.Columns = append([]core.ColumnSpec{{Kind: "attribute", Key: "name", Width: 28}, {Kind: "attribute", Key: "experiment_id", Label: "Experiment", Width: 24}, {Kind: "attribute", Key: "status", Width: 12}, {Kind: "attribute", Key: "end_time", Width: 18}}, v.Columns[3:]...)
			v.Sort = nil // service order includes activity time, which is not a Run attribute
			a.Views[scope] = &runState{View: v}
		}
		m.activities[key] = a
	}
	return a
}
func (m *model) activityScope() runListScope {
	if a := m.activityCurrent(); a != nil {
		return a.Scope
	}
	return scopeExperiment
}
func activityLabel(scope runListScope) string {
	switch scope {
	case scopeRunning:
		return "Running"
	case scopeRecent:
		return "Recent"
	case scopeUnread:
		return "Unread"
	case scopeAlerts:
		return "Alerts"
	case scopePinned:
		return "Pinned"
	}
	return "Runs"
}
func (m *model) activityActions() []action {
	if !m.activityAvailable() {
		return nil
	}
	a := []action{act("activity", "I", "Activity inbox"), act("next-unread", "J", "Next unread run"), act("prev-unread", "K", "Previous unread run"), act("activity-settings", "!", "Activity / alert preferences"), act("read-all", "W", "Mark all target inbox read"), act("activity-full", "", "Rebuild all experiment counts / alerts"), act("activity-pause", "", "Pause / resume activity refresh")}
	if state := m.activityCurrent(); state != nil && (state.Pending || state.FullPending || state.Pins != nil && (state.Pins.Loading || state.Pins.Refreshing)) && !m.downloadPending {
		a = append(a, act("activity-cancel", "ctrl+x", "Cancel activity scan"))
	}
	if m.run() != nil {
		a = append(a, act("read", "w", "Mark this run read"))
	}
	if m.activityScope() == scopeAlerts {
		a = append(a, act("acknowledge-all", "", "Acknowledge all matching alerts"), act("alert-filter", "", "Cycle active / acknowledged / all alerts"))
	}
	if m.activityScope() != scopeExperiment && m.focus == 1 && !m.compare && m.run() != nil {
		a = append(a, act("acknowledge", "a", "Acknowledge this run's current alerts"), act("restore-alerts", "", "Restore alerts for this run"))
	}
	return a
}
func (m *model) performActivity(id string) (tea.Cmd, bool) {
	if !m.activityAvailable() {
		return nil, false
	}
	switch id {
	case "activity":
		a := m.activityState()
		return m.openActivity(a.LastScope, true), true
	case "next-unread", "prev-unread":
		d := 1
		if id == "prev-unread" {
			d = -1
		}
		return m.nextUnread(d), true
	case "read":
		return m.activityReadSelected(false), true
	case "read-all":
		return m.changeActivity("read-all"), true
	case "acknowledge", "acknowledge-all", "restore-alerts":
		return m.changeActivity(id), true
	case "activity-settings":
		return tea.Batch(m.openPicker("activity-settings"), m.loadActivityPolicy()), true
	case "activity-full":
		return m.refreshActivity(true, nil), true
	case "activity-pause":
		if m.opts.Activity.RefreshSeconds == 0 {
			m.opts.Activity.RefreshSeconds = 30
		} else {
			m.opts.Activity.RefreshSeconds = 0
		}
		return m.saveActivitySettings(), true
	case "alert-filter":
		a := m.activityState()
		switch a.AlertMode {
		case "active":
			a.AlertMode = "acknowledged"
		case "acknowledged":
			a.AlertMode = "all"
		default:
			a.AlertMode = "active"
		}
		m.rebuildActivityViews()
		return nil, true
	case "enter":
		if m.activityScope() != scopeExperiment && m.focus == 0 {
			m.focus = 1
			return m.ensureDetails(), true
		}
	case "loadall":
		if m.activityScope() != scopeExperiment {
			a := m.activityState()
			if a.Scope != scopeRecent {
				m.status = "All observed matching activity is already loaded"
				return nil, true
			}
			if a.Pending {
				m.status = "Activity is refreshing; wait before loading more"
				return nil, true
			}
			a.RecentLimit = -1
			return m.refreshActivityLimit(false, nil, -1), true
		}
	case "cancel-all", "activity-cancel":
		if a := m.activityCurrent(); a != nil && (a.Pending || a.FullPending || a.Pins != nil && (a.Pins.Loading || a.Pins.Refreshing)) {
			m.stopActivity()
			m.status = "Activity scan cancelled; cached rows retained"
			return nil, true
		}
	case "filter":
		if m.activityScope() != scopeExperiment && m.focus == 1 {
			m.status = "Activity uses its own query; / searches cached rows, s sorts them"
			return nil, true
		}
	}
	return nil, false
}
func (m *model) openActivity(scope runListScope, focus bool) tea.Cmd {
	if scope == scopePinned && m.runPinStore() == nil {
		m.status = "Local run pin storage is unavailable"
		return nil
	}
	a := m.activityState()
	if a == nil {
		return nil
	}
	valid := false
	for _, candidate := range activityScopes {
		valid = valid || scope == candidate
	}
	if !valid {
		scope = scopeUnread
	}
	if a.Scope != scope {
		m.releaseActivityRetention()
		a.ManualRefreshGen = 0
	}
	a.Scope = scope
	// Bookmarks have their own entry; I still returns to the last inbox view.
	if scope != scopePinned {
		a.LastScope = scope
	}
	m.compare = false
	m.chart = false
	m.mousePressed = ""
	if focus {
		m.focus = 1
	}
	m.rebuildActivityViews()
	if scope == scopePinned {
		return tea.Batch(m.loadRunPins(false, false), m.ensureDetails())
	}
	if !a.Loaded {
		return m.loadActivity()
	}
	return tea.Batch(m.ensureDetails(), m.refreshActivity(false, nil))
}
func (m *model) leaveActivity() {
	m.releaseActivityRetention()
	if a := m.activityCurrent(); a != nil {
		a.Scope = scopeExperiment
		a.Inspect = nil
		a.ManualRefreshGen = 0
	}
}
func (m *model) moveActivitySidebar(delta int) tea.Cmd {
	a, s := m.activityState(), m.state()
	if a == nil || s == nil {
		return nil
	}
	scopes := m.sidebarActivityScopes()
	index := len(scopes) + s.Index
	if a.Scope != scopeExperiment {
		for i, scope := range scopes {
			if scope == a.Scope {
				index = i
			}
		}
	}
	index = clamp(index+delta, 0, len(scopes)+len(m.experimentsVisible())-1)
	if index < len(scopes) {
		return m.openActivity(scopes[index], false)
	}
	before := s.Selected
	wasActivity := a.Scope != scopeExperiment
	m.leaveActivity()
	m.selectExperiment(index - len(scopes))
	if wasActivity || before != s.Selected {
		return tea.Batch(m.loadRuns(false), m.loadView())
	}
	return nil
}

type activityLoadedMsg struct {
	source   string
	gen      uint64
	snapshot core.ActivitySnapshot
	err      error
}
type activityScanMsg struct {
	source     string
	gen        uint64
	full, done bool
	snapshot   core.ActivitySnapshot
	err        error
	events     <-chan activityScanMsg
}
type activityTickMsg struct{ gen uint64 }
type activityRunMsg struct {
	source, id           string
	gen                  uint64
	run                  core.Run
	err                  error
	observedAt, revision int64
	startedAt            int64
}
type activityChangedMsg struct {
	source, label string
	snapshot      core.ActivitySnapshot
	err           error
	receipts      []core.ActivityReceipt
	read, ack     bool
}
type activitySettingsSavedMsg struct{ err error }

func (m *model) activityTick() tea.Cmd {
	if !m.activityAvailable() || m.opts.Activity.RefreshSeconds <= 0 {
		return nil
	}
	gen := m.activityTickGen
	return tea.Tick(time.Duration(m.opts.Activity.RefreshSeconds)*time.Second, func(time.Time) tea.Msg { return activityTickMsg{gen} })
}
func (m *model) loadActivity() tea.Cmd {
	a := m.activityState()
	if a == nil || a.Loading {
		return nil
	}
	if a.Loaded {
		return m.refreshActivity(false, nil)
	}
	a.Loading = true
	m.seq++
	a.Gen = m.seq
	source, gen, db, ctx := core.SourceKey(m.target()), a.Gen, m.activityStore(), m.ctx
	return func() tea.Msg {
		snapshot, err := db.LoadActivity(ctx, source)
		return activityLoadedMsg{source, gen, snapshot, err}
	}
}
func (m *model) refreshActivity(full bool, experiments []string) tea.Cmd {
	return m.refreshActivityLimit(full, experiments, 100)
}
func (m *model) refreshActivityManually() tea.Cmd {
	a := m.activityState()
	if a == nil {
		return nil
	}
	// A deliberate refresh supersedes an automatic read instead of being
	// silently ignored while that read is pending.
	if cancel := m.cancel["activity:fast"]; cancel != nil {
		cancel()
	}
	a.Pending = false
	cmd := m.refreshActivity(false, nil)
	a.ManualRefreshGen = a.Gen
	a.ManualRefreshScope = a.Scope
	return cmd
}
func (m *model) refreshActivityLimit(full bool, experiments []string, limit int) tea.Cmd {
	a, s := m.activityState(), m.state()
	if a == nil || s == nil || s.Session == nil {
		return nil
	}
	if !a.Loaded {
		return m.loadActivity()
	}
	if full && a.FullPending || !full && a.Pending {
		return nil
	}
	name := "activity:fast"
	if full {
		name = "activity:full"
	}
	ctx, gen := m.operation(name)
	if full {
		a.FullPending = true
		a.FullGen = gen
	} else {
		a.Pending = true
		a.Gen = gen
	}
	a.Err = ""
	source, backend, store, state := core.SourceKey(m.target()), s.Session.Backend, m.activityStore(), m.opts.State
	settings, alerts := m.opts.Activity, m.opts.Alerts
	events := make(chan activityScanMsg, 1)
	opts := activity.Options{Settings: settings, Alerts: alerts, Full: full, RecentLimit: limit, ExperimentIDs: append([]string(nil), experiments...)}
	opts.Policy = func(ctx context.Context, experiment string) (core.ActivityPolicy, error) {
		v, _, err := state.LoadView(ctx, source, experiment)
		if v.Activity != nil {
			return *core.CloneActivityPolicy(v.Activity), err
		}
		return core.ActivityPolicy{}, err
	}
	opts.Progress = func(snapshot core.ActivitySnapshot) {
		select {
		case events <- activityScanMsg{source: source, gen: gen, full: full, snapshot: snapshot, events: events}:
		case <-ctx.Done():
		}
	}
	return func() tea.Msg {
		go func() {
			defer close(events)
			snapshot, err := activity.Refresh(ctx, backend, store, source, opts)
			select {
			case events <- activityScanMsg{source: source, gen: gen, full: full, done: true, snapshot: snapshot, err: err, events: events}:
			case <-ctx.Done():
			}
		}()
		return waitActivityEvent(ctx, events)()
	}
}
func waitActivityEvent(ctx context.Context, events <-chan activityScanMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-events:
			if ok {
				return msg
			}
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) stopActivity() {
	for _, name := range []string{"activity:fast", "activity:full", "activity:run", "pins:load", "pins:refresh"} {
		if c := m.cancel[name]; c != nil {
			c()
			delete(m.cancel, name)
		}
	}
	m.seq++
	for _, a := range m.activities {
		a.ManualRefreshGen = 0
		a.Gen = m.seq
		a.FullGen = m.seq
		a.RunGen = m.seq
		a.Pending = false
		a.FullPending = false
		a.Loading = false
		a.RunPending = ""
		if a.Pins != nil {
			a.Pins.Loading, a.Pins.Refreshing = false, false
			a.Pins.Gen, a.Pins.RefreshGen = m.seq, m.seq
		}
	}
}
func (m *model) updateActivity(msg tea.Msg) (tea.Cmd, bool) {
	source := core.SourceKey(m.target())
	switch v := msg.(type) {
	case activityPolicyLoadedMsg:
		a := m.activityCurrent()
		if a == nil || source != v.source {
			return nil, true
		}
		a.PolicyLoading = false
		if v.err != nil {
			m.status = "Could not load activity preferences: " + v.err.Error()
			return nil, true
		}
		if r := m.state().Runs[v.experiment]; r != nil && r.ViewRevision == v.revision && v.found {
			r.View = cloneView(v.value)
			r.ViewRequested = true
			delete(a.PolicyEdits, v.experiment)
		}
		return nil, true
	case activityTickMsg:
		if v.gen != m.activityTickGen {
			return nil, true
		}
		return tea.Batch(m.activityTick(), m.refreshActivity(false, nil)), true
	case activityLoadedMsg:
		a := m.activityCurrent()
		if a == nil || source != v.source || a.Gen != v.gen {
			return nil, true
		}
		a.Loading = false
		if v.err != nil {
			a.Err = v.err.Error()
			return nil, true
		}
		a.Loaded = true
		m.acceptActivitySnapshot(v.snapshot)
		return m.refreshActivity(false, nil), true
	case activityScanMsg:
		a := m.activityCurrent()
		if a == nil || source != v.source || v.full && a.FullGen != v.gen || !v.full && a.Gen != v.gen {
			return nil, true
		}
		if v.snapshot.Records != nil {
			m.acceptActivitySnapshot(v.snapshot)
		}
		if !v.done {
			return waitActivityEvent(m.ctx, v.events), true
		}
		if v.full {
			a.FullPending = false
		} else {
			a.Pending = false
		}
		if v.err != nil {
			a.Err = v.err.Error()
			return nil, true
		}
		a.Err = ""
		if !v.full && v.gen == a.ManualRefreshGen {
			if a.Scope == a.ManualRefreshScope {
				m.releaseActivityRetention()
			}
			a.ManualRefreshGen = 0
		}
		var missing []string
		if !v.full && !a.FullPending {
			for _, e := range a.Snapshot.Experiments {
				if !a.Snapshot.Counts[e.ID].Complete && !a.FullAttempted[e.ID] {
					missing = append(missing, e.ID)
					a.FullAttempted[e.ID] = true
				}
			}
			sort.SliceStable(missing, func(i, j int) bool {
				return missing[i] == m.selectedExperiment() && missing[j] != m.selectedExperiment()
			})
		}
		var fullCmd tea.Cmd
		if len(missing) > 0 {
			fullCmd = m.refreshActivity(true, missing)
		}
		return tea.Batch(m.ensureDetails(), fullCmd), true
	case activityRunMsg:
		a := m.activityCurrent()
		if a == nil || source != v.source || a.RunGen != v.gen {
			return nil, true
		}
		a.RunPending = ""
		if a.Pins != nil && a.Pins.FetchedAt[v.id] > v.startedAt && v.startedAt > 0 {
			return m.ensureDetails(), true
		}
		if v.err != nil {
			if a.Pins != nil && m.runPinned(v.id) {
				a.Pins.RunErrors[v.id] = v.err.Error()
			}
			m.status = "Run details unavailable: " + v.err.Error()
			return nil, true
		}
		if v.run.ID() != v.id {
			m.status = "Run details returned an unexpected run ID"
			if a.Pins != nil && m.runPinned(v.id) {
				a.Pins.RunErrors[v.id] = m.status
			}
			return nil, true
		}
		readAt := v.startedAt
		if readAt == 0 {
			readAt = v.observedAt
		}
		if record, ok := a.Snapshot.Records[v.id]; ok && record.ObservedAt > readAt {
			v.run = mergeActivityMetadata(v.run, record)
			readAt = record.ObservedAt
		}
		a.Runs[v.id] = &v.run
		a.MetadataAt[v.id] = readAt
		if a.Pins != nil {
			delete(a.Pins.RunErrors, v.id)
			a.Pins.FetchedAt[v.id] = v.startedAt
		}
		m.rebuildActivityViews()
		if r := m.runs(); r != nil && r.Selected == v.id && a.Scope != scopeExperiment {
			a.Inspect = &v.run
			return m.ensureDetails(), true
		}
		return nil, true
	case activityChangedMsg:
		if source != v.source {
			return nil, true
		}
		if v.err != nil {
			m.status = "Activity change failed: " + v.err.Error()
			return nil, true
		}
		a := m.activityCurrent()
		for _, receipt := range v.receipts {
			if v.read {
				a.ReadReceipts[receipt.RunID] = max(a.ReadReceipts[receipt.RunID], receipt.Revision)
			} else if record, ok := v.snapshot.Records[receipt.RunID]; ok {
				for _, alert := range record.Alerts {
					if alert.Active && alert.Episode <= receipt.Revision {
						a.AckReceipts[activityAlertReceipt(receipt.RunID, alert)] = v.ack
					}
				}
			}
		}
		m.acceptActivitySnapshot(v.snapshot)
		m.status = v.label
		return nil, true
	case activitySettingsSavedMsg:
		if v.err != nil {
			m.status = "Activity settings were not saved: " + v.err.Error()
		} else {
			m.status = "Activity preferences saved"
			return m.refreshActivity(false, nil), true
		}
		return nil, true
	}
	return nil, false
}
func (m *model) acceptActivitySnapshot(snapshot core.ActivitySnapshot) {
	a := m.activityState()
	if a == nil {
		return
	}
	if a.Snapshot.Version > 0 && snapshot.Version <= a.Snapshot.Version {
		return
	}
	if snapshot.Version == 0 {
		for id, old := range a.Snapshot.Records {
			if next, ok := snapshot.Records[id]; !ok || old.ObservedAt > next.ObservedAt {
				snapshot.Records[id] = old
			}
		}
		if snapshot.UpdatedAt < a.Snapshot.UpdatedAt {
			snapshot.Experiments = a.Snapshot.Experiments
			for id, count := range a.Snapshot.Counts {
				if count.ScannedAt >= snapshot.Counts[id].ScannedAt {
					snapshot.Counts[id] = count
				}
			}
		}
		snapshot.Checkpoint = max(snapshot.Checkpoint, a.Snapshot.Checkpoint)
		snapshot.LastFullScan = max(snapshot.LastFullScan, a.Snapshot.LastFullScan)
		snapshot.UpdatedAt = max(snapshot.UpdatedAt, a.Snapshot.UpdatedAt)
		snapshot.Initialized = snapshot.Initialized || a.Snapshot.Initialized
	} else {
		a.ReadReceipts = map[string]int64{}
		a.AckReceipts = map[string]bool{}
	}
	for id, record := range snapshot.Records {
		if read := a.ReadReceipts[id]; read > record.ReadRevision {
			record.ReadRevision = read
		}
		for i, alert := range record.Alerts {
			if ack, ok := a.AckReceipts[activityAlertReceipt(id, alert)]; ok {
				record.Alerts[i].Acknowledged = ack
			}
		}
		snapshot.Records[id] = record
		if full := a.Runs[id]; full != nil && record.ObservedAt >= a.MetadataAt[id] {
			copy := mergeActivityMetadata(*full, record)
			a.Runs[id] = &copy
		}
		if a.Inspect != nil && a.Inspect.ID() == id && record.ObservedAt >= a.MetadataAt[id] {
			copy := mergeActivityMetadata(*a.Inspect, record)
			a.Inspect = &copy
		}
	}
	// Lookup by identity once per loaded row, rather than scanning an entire
	// experiment for every tracked record during the event loop.
	if state := m.state(); state != nil {
		for _, runs := range state.Runs {
			changed := false
			for i, run := range runs.Rows {
				if record, ok := snapshot.Records[run.ID()]; ok && record.ObservedAt > 0 && record.ObservedAt >= a.MetadataAt[run.ID()] {
					runs.Rows[i] = mergeActivityMetadata(run, record)
					changed = true
				}
			}
			if changed {
				runs.RowsVersion++
			}
		}
	}
	for id, record := range snapshot.Records {
		if record.ObservedAt > a.MetadataAt[id] {
			a.MetadataAt[id] = record.ObservedAt
		}
	}
	for id, count := range snapshot.Counts {
		count.Unread = 0
		count.Alerts = 0
		snapshot.Counts[id] = count
	}
	activeExperiments := map[string]bool{}
	for _, experiment := range snapshot.Experiments {
		activeExperiments[experiment.ID] = true
	}
	for _, record := range snapshot.Records {
		if record.LifecycleStage == "deleted" || snapshot.Initialized && !activeExperiments[record.ExperimentID] {
			continue
		}
		count := snapshot.Counts[record.ExperimentID]
		count.ExperimentID = record.ExperimentID
		if record.Unread() {
			count.Unread++
		}
		if record.HasAlerts() {
			count.Alerts++
		}
		snapshot.Counts[record.ExperimentID] = count
	}
	a.Snapshot = snapshot
	a.ExperimentNames = map[string]string{}
	for _, experiment := range snapshot.Experiments {
		a.ExperimentNames[experiment.ID] = experiment.Name
	}
	m.rebuildActivityViews()
	if a.Scope == scopeExperiment {
		m.reselectRun()
	}
}
func activityAlertReceipt(id string, a core.ActivityAlert) string {
	return fmt.Sprintf("%s/%s/%d", id, a.ID, a.Episode)
}
func (m *model) activityRecords(scope runListScope) []core.ActivityRecord {
	a := m.activityCurrent()
	if a == nil {
		return nil
	}
	view := string(scope)
	if scope == scopeAlerts && a.AlertMode != "active" {
		view = a.AlertMode
	}
	var rows []core.ActivityRecord
	if scope == scopePinned {
		rows = m.pinnedRecords()
	} else {
		rows = core.ActivityRecords(a.Snapshot, view)
	}
	var out []core.ActivityRecord
	visibility := "normal"
	if r := a.Views[scope]; r != nil {
		visibility = r.View.Visibility
	}
	for _, r := range rows {
		if scope == scopeAlerts && a.AlertMode == "all" {
			found := false
			for _, alert := range r.Alerts {
				found = found || alert.Active
			}
			if !found {
				continue
			}
		}
		if !visibilityMatches(visibility, m.state().Visibility[core.VisibilityKey("run", r.RunID)]) || !visibilityMatches(visibility, m.state().Visibility[core.VisibilityKey("experiment", r.ExperimentID)]) {
			continue
		}
		out = append(out, r)
	}
	return out
}
func (m *model) rebuildActivityViews() {
	a := m.activityCurrent()
	if a == nil {
		return
	}
	a.ScopeCounts = map[runListScope]int{}
	for _, scope := range activityScopes {
		r := a.Views[scope]
		selected, index := r.Selected, r.Index
		wasMember := false
		for _, run := range r.Rows {
			if run.ID() == selected {
				wasMember = true
				break
			}
		}
		rows := m.activityRecords(scope)
		a.ScopeCounts[scope] = len(rows)
		if scope == scopeRunning && a.Scope == scope && !a.SuppressRetention && selected != "" {
			if record, ok := a.Snapshot.Records[selected]; ok && terminalRunStatus(record.Status) && m.activityRetentionVisible(record, r.View.Visibility) && (wasMember || a.RetainedRun == selected) {
				a.RetainedRun = selected
				// This row is presentation-only; counts still use the Running predicate.
				at := clamp(index, 0, len(rows))
				rows = append(rows, core.ActivityRecord{})
				copy(rows[at+1:], rows[at:])
				rows[at] = record
			} else if a.RetainedRun == selected {
				a.RetainedRun = ""
			}
		}
		if scope == scopeRecent && a.RecentLimit > 0 && len(rows) > a.RecentLimit {
			rows = rows[:a.RecentLimit]
		}
		r.Rows = nil
		for _, record := range rows {
			if full := a.Runs[record.RunID]; full != nil {
				r.Rows = append(r.Rows, *full)
			} else {
				r.Rows = append(r.Rows, record.Run())
			}
		}
		r.RowsVersion++
		r.Next = ""
		if scope == scopeRecent && a.RecentLimit > 0 && len(rows) >= a.RecentLimit {
			r.Next = "activity:more"
		}
		found := false
		presented := m.activityPresentationRows(r)
		for i, row := range presented {
			if row.ID == selected {
				index = i
				found = true
				break
			}
		}
		if !found && scope != scopeRunning && scope != scopePinned && a.Scope == scope && a.Inspect != nil && a.Inspect.ID() == selected && m.focus == 2 {
			continue
		}
		r.Index = clamp(index, 0, len(presented)-1)
		r.Selected = ""
		if len(presented) > 0 {
			r.Selected = presented[r.Index].ID
		}
	}
}
func (m *model) activityRetentionVisible(record core.ActivityRecord, visibility string) bool {
	if record.LifecycleStage == "deleted" {
		return false
	}
	a, s := m.activityCurrent(), m.state()
	if a == nil || s == nil {
		return false
	}
	if !visibilityMatches(visibility, s.Visibility[core.VisibilityKey("run", record.RunID)]) || !visibilityMatches(visibility, s.Visibility[core.VisibilityKey("experiment", record.ExperimentID)]) {
		return false
	}
	if a.Snapshot.Initialized {
		for _, experiment := range a.Snapshot.Experiments {
			if experiment.ID == record.ExperimentID {
				return true
			}
		}
		return false
	}
	return true
}
func (m *model) releaseActivityRetention() {
	a := m.activityCurrent()
	if a == nil {
		return
	}
	// Navigation invalidates an earlier manual-refresh cleanup intent, even
	// when the newly selected run has not finished yet.
	a.ManualRefreshGen = 0
	if a.RetainedRun == "" {
		return
	}
	id := a.RetainedRun
	a.RetainedRun = ""
	a.SuppressRetention = true
	if a.Inspect != nil && a.Inspect.ID() == id {
		a.Inspect = nil
	}
	m.rebuildActivityViews()
	a.SuppressRetention = false
}
func (m *model) loadActivityRows(more bool) tea.Cmd {
	a := m.activityState()
	if a == nil {
		return nil
	}
	if more && a.Scope == scopeRecent && a.RecentLimit > 0 {
		if a.Pending {
			m.status = "Activity is refreshing; wait before loading more"
			return nil
		}
		a.RecentLimit += 100
		return m.refreshActivityLimit(false, nil, a.RecentLimit)
	}
	m.rebuildActivityViews()
	return m.ensureDetails()
}
func (m *model) ensureActivityRun() tea.Cmd {
	return m.loadActivityRun(false)
}
func (m *model) loadActivityRun(force bool) tea.Cmd {
	a, s := m.activityCurrent(), m.state()
	if a == nil || a.Scope == scopeExperiment || s == nil || s.Session == nil {
		return nil
	}
	r := m.runs()
	if r == nil || r.Selected == "" {
		return nil
	}
	id := r.Selected
	if a.Scope == scopePinned && a.Pins != nil {
		if !force && (a.Pins.Refreshing || a.Pins.RunErrors[id] != "") {
			return nil
		}
		if force {
			delete(a.Pins.RunErrors, id)
		}
	}
	if !force && a.Runs[id] != nil || a.RunPending == id {
		return nil
	}
	ctx, gen := m.operation("activity:run")
	a.RunGen = gen
	a.RunPending = id
	source, backend := core.SourceKey(m.target()), s.Session.Backend
	record := a.Snapshot.Records[id]
	startedAt := time.Now().UnixMilli()
	return func() tea.Msg {
		run, err := backend.GetRun(ctx, id)
		return activityRunMsg{source: source, id: id, gen: gen, run: run, err: err, observedAt: record.ObservedAt, revision: record.Revision, startedAt: startedAt}
	}
}
func (m *model) nextUnread(delta int) tea.Cmd {
	a := m.activityState()
	if a == nil {
		return nil
	}
	current := ""
	if r := m.run(); r != nil {
		current = r.ID()
	}
	m.rebuildActivityViews()
	rows := m.activityPresentationRows(a.Views[scopeUnread])
	if len(rows) == 0 {
		m.status = "No unread runs match the Unread view"
		return nil
	}
	index := -1
	for i, r := range rows {
		if r.ID == current {
			index = i
			break
		}
	}
	if index < 0 {
		if delta > 0 {
			index = 0
		} else {
			index = len(rows) - 1
		}
	} else {
		index = (index + delta + len(rows)) % len(rows)
	}
	m.releaseActivityRetention()
	a.Scope = scopeUnread
	a.ManualRefreshGen = 0
	a.LastScope = scopeUnread
	a.Inspect = nil
	m.focus = 1
	m.compare = false
	m.rebuildActivityViews()
	r := a.Views[scopeUnread]
	r.Selected = rows[index].ID
	r.Index = index
	return tea.Batch(m.ensureDetails(), m.activityReadSelected(true))
}

func mergeActivityMetadata(run core.Run, record core.ActivityRecord) core.Run {
	run.Info.RunName = record.RunName
	run.Info.ExperimentID = record.ExperimentID
	run.Info.Status = record.Status
	run.Info.StartTime = record.StartTime
	run.Info.EndTime = record.EndTime
	run.Info.LifecycleStage = record.LifecycleStage
	run.Data.Metrics = append([]core.Metric(nil), record.Metrics...)
	return run
}

func (m *model) rememberActivityMetadata(runs []core.Run) {
	a := m.activityCurrent()
	if a == nil {
		return
	}
	at := time.Now().UnixMilli()
	for _, run := range runs {
		copy := run
		a.Runs[run.ID()] = &copy
		a.MetadataAt[run.ID()] = at
		if a.Pins != nil {
			delete(a.Pins.RunErrors, run.ID())
			a.Pins.FetchedAt[run.ID()] = at
		}
		if a.Inspect != nil && a.Inspect.ID() == run.ID() {
			a.Inspect = &copy
		}
	}
}
func (m *model) activityReadSelected(selection bool) tea.Cmd {
	a, r := m.activityCurrent(), m.run()
	if a == nil || r == nil {
		return nil
	}
	if selection {
		return m.changeActivity("read-select")
	}
	return m.changeActivity("read")
}
func (m *model) changeActivity(operation string) tea.Cmd {
	a := m.activityState()
	if a == nil {
		return nil
	}
	var records []core.ActivityRecord
	if operation == "read-all" {
		records = core.ActivityRecords(a.Snapshot, "unread")
	} else if operation == "acknowledge-all" {
		for _, record := range m.activityRecords(scopeAlerts) {
			if strings.Contains(strings.ToLower(record.RunName+" "+record.RunID+" "+record.ExperimentName+" "+record.Status), strings.ToLower(a.Views[scopeAlerts].Local)) {
				records = append(records, record)
			}
		}
	} else if run := m.run(); run != nil {
		if record, ok := a.Snapshot.Records[run.ID()]; ok {
			records = []core.ActivityRecord{record}
		}
		if a.Scope != scopeExperiment {
			copy := *run
			a.Inspect = &copy
		}
	}
	var receipts []core.ActivityReceipt
	read := strings.HasPrefix(operation, "read")
	ack := operation != "restore-alerts"
	for _, record := range records {
		if read && !record.Unread() {
			continue
		}
		receipts = append(receipts, core.ActivityReceipt{RunID: record.RunID, Revision: record.Revision})
	}
	if len(receipts) == 0 {
		m.status = "No matching activity to update"
		return nil
	}
	source, store := core.SourceKey(m.target()), m.activityStore()
	settings, alerts, preferences := m.opts.Activity, m.opts.Alerts, m.opts.State
	localPolicy, hasLocalPolicy := a.PolicyEdits[records[0].ExperimentID]
	localPolicy = *core.CloneActivityPolicy(&localPolicy)
	label := fmt.Sprintf("Marked %d runs read", len(receipts))
	if !read {
		label = fmt.Sprintf("Acknowledged current alerts for %d runs", len(receipts))
		if !ack {
			label = "Restored current alerts"
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		defer cancel()
		var err error
		if operation == "read-select" {
			policy := core.ActivityPolicy{}
			if hasLocalPolicy {
				policy = localPolicy
			} else {
				view, _, e := preferences.LoadView(ctx, source, records[0].ExperimentID)
				if e != nil {
					return activityChangedMsg{source: source, err: e}
				}
				if view.Activity != nil {
					policy = *view.Activity
				}
			}
			if core.EffectiveActivityPolicy(settings, alerts, policy).ReadOn != "select" {
				return nil
			}
		}
		if read {
			err = store.MarkActivityRead(ctx, source, receipts)
		} else {
			err = store.AcknowledgeActivity(ctx, source, receipts, ack)
		}
		var snapshot core.ActivitySnapshot
		if err == nil {
			snapshot, err = store.LoadActivity(ctx, source)
		}
		return activityChangedMsg{source: source, label: label, snapshot: snapshot, err: err, receipts: receipts, read: read, ack: ack}
	}
}

func (m *model) activitySidebar(p *paneContent, w int) {
	if !m.activityAvailable() {
		return
	}
	for _, scope := range m.sidebarActivityScopes() {
		count := 0
		if a := m.activityCurrent(); a != nil {
			count = a.ScopeCounts[scope]
		}
		suffix := ""
		if a := m.activityCurrent(); a == nil || !a.Loaded {
			suffix = " …"
		} else {
			if scope == scopeAlerts {
				for _, experiment := range a.Snapshot.Experiments {
					if !a.Snapshot.Counts[experiment.ID].Complete {
						suffix = "+"
						break
					}
				}
			}
			if scope == scopeRecent && a.RecentLimit > 0 && count >= a.RecentLimit {
				suffix = "+"
			}
			if a.Err != "" {
				suffix += " stale"
			}
		}
		if scope == scopePinned {
			suffix = ""
			if pins := m.currentRunPins(); pins == nil || !pins.Loaded {
				suffix = " …"
			} else if pins.Err != "" {
				suffix = " stale"
			}
		}
		p.addHit(row(fmt.Sprintf("%s %d%s", activityLabel(scope), count, suffix), m.activityScope() == scope, w), "activity:"+string(scope), w)
	}
	p.add(dimStyle.Render("── Experiments ──"))
}
func middleName(s string, width int) string {
	s = clean(s)
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width < 3 {
		return ansi.Truncate(s, max(0, width), "…")
	}
	left := (width - 1) / 2
	right := width - left - 1
	return ansi.Truncate(s, left, "") + "…" + ansi.Cut(s, ansi.StringWidth(s)-right, ansi.StringWidth(s))
}
func (m *model) experimentActivityBadge(id string) string {
	a := m.activityCurrent()
	if a == nil {
		return ""
	}
	c, ok := a.Snapshot.Counts[id]
	if !ok {
		return ""
	}
	var bits []string
	if c.Running > 0 {
		bits = append(bits, fmt.Sprintf("R%d", c.Running))
	}
	if c.Unread > 0 {
		bits = append(bits, fmt.Sprintf("U%d", c.Unread))
	}
	if c.Alerts > 0 {
		bits = append(bits, fmt.Sprintf("!%d", c.Alerts))
	}
	if len(bits) == 0 {
		return ""
	}
	return " " + strings.Join(bits, " ")
}
func (m *model) experimentActivityOverview(id string) []string {
	a := m.activityCurrent()
	if a == nil {
		return nil
	}
	c, ok := a.Snapshot.Counts[id]
	if !ok {
		return []string{"", "Activity counts: waiting for background scan"}
	}
	state := "partial / cached"
	if a.FullPending {
		state = "partial / scanning"
	}
	if c.Complete {
		state = "cached all-history"
	}
	if c.Stale {
		state += " · refresh needed"
	}
	lines := []string{"", "Run overview · " + state, "All active runs (including locally hidden runs)", fmt.Sprintf("Total: %d", c.Total)}
	keys := make([]string, 0, len(c.Statuses))
	for k := range c.Statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %d", k, c.Statuses[k]))
	}
	lines = append(lines, fmt.Sprintf("Unread: %d · active alerts: %d", c.Unread, c.Alerts), "Last full scan: "+timestamp(c.ScannedAt), "Last activity poll: "+timestamp(a.Snapshot.UpdatedAt), "r rebuilds this experiment's counts / alerts")
	return lines
}
func (m *model) activityRunMark(id string) string {
	a := m.activityCurrent()
	if a == nil {
		return ""
	}
	r, ok := a.Snapshot.Records[id]
	if !ok {
		return ""
	}
	s := ""
	if r.Unread() {
		s += "●"
	}
	if r.HasAlerts() {
		s += "!"
	}
	return s
}
func (m *model) activityExperimentName(id string) string {
	if a := m.activityCurrent(); a != nil {
		if name := a.ExperimentNames[id]; name != "" {
			return name
		}
	}
	if s := m.state(); s != nil {
		for _, e := range s.Experiments {
			if e.ID == id {
				return e.Name
			}
		}
	}
	if pins := m.currentRunPins(); pins != nil {
		for _, pin := range pins.Items {
			if pin.ExperimentID == id && pin.ExperimentName != "" {
				return pin.ExperimentName
			}
		}
	}
	return id
}
func (m *model) activityDescription() string {
	a := m.activityCurrent()
	if a == nil {
		return ""
	}
	s := activityLabel(a.Scope) + " · cached " + timestamp(a.Snapshot.UpdatedAt)
	if a.Pending {
		s += " · refreshing"
	}
	if a.FullPending {
		s += " · indexing history"
	}
	if a.Scope == scopeAlerts {
		s += " · " + a.AlertMode
	}
	return s
}
