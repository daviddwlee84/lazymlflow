// Package tui provides the interactive MLflow browser. Network and filesystem
// operations run as effects; the model exclusively owns presentation state.
package tui

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/targetform"
)

type Options struct {
	Targets          []core.Target
	InitialTarget    string
	Connector        core.Connector
	SaveTargets      func([]core.Target, string) error
	MetricColumns    []string
	ParameterColumns []string
	RefreshSeconds   int
	Input            io.Reader
	Output           io.Writer
	ConfigPath       string
	State            core.StateStore
	Mouse            *bool
	Activity         core.ActivitySettings
	Alerts           core.AlertSettings
	SaveActivity     func(core.ActivitySettings, core.AlertSettings) error
	PreviewMaxBytes  int64
}

func Run(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, opts)
	po := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		po = append(po, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		po = append(po, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(m, po...).Run()
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer flushCancel()
	if flushErr := m.writer.flush(flushCtx); flushErr != nil && err == nil {
		err = fmt.Errorf("save local preferences: %w", flushErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type listState struct {
	Selected                   string
	Index, Offset              int
	Local, Filter, Order, Next string
	Pending                    bool
	Err                        string
	Gen                        uint64
}
type runState struct {
	listState
	Rows             []core.Run
	View             core.ExperimentView
	ViewRequested    bool
	ViewRevision     uint64
	LoadingAll       bool
	FirstPagePending bool
	SeenPageTokens   map[string]bool
	RowsVersion      uint64
	presentationKey  string
	presentation     []core.RunRow
}
type artifactState struct {
	listState
	Rows []core.Artifact
	Root string
}
type targetState struct {
	listState
	Visibility          map[string]core.Visibility
	VisibilityTouched   map[string]bool
	VisibilityRequested bool
	Experiments         []core.Experiment
	Runs                map[string]*runState
	Artifacts           map[string]*artifactState
	Basket              map[string]core.Run
	BasketOrder         []string
	Session             *core.Session
	Retiring            *core.Session
	ConnectPending      bool
	ConnectGen          uint64
	ConnectErr          string
	Histories           map[string][]core.Metric
	HistoryErrors       map[string]string
	HistoryGen          uint64
	HistoryPending      bool
	CompareGen          uint64
	ComparePending      bool
	CompareErr          string
}

type model struct {
	preview                                 *artifactPreviewState
	activities                              map[string]*activityState
	activityTickGen                         uint64
	extensions                              *extensionState
	work                                    *workspaceState
	inspect                                 *inspectionState
	ctx                                     context.Context
	opts                                    Options
	targets                                 []core.Target
	active                                  string
	states                                  map[string]*targetState
	width, height, focus, tab, detailOffset int
	seq                                     uint64
	cancel                                  map[string]context.CancelFunc
	status                                  string
	input                                   textinput.Model
	inputMode, inputLabel                   string
	overlay                                 string
	menuIndex, menuOffset                   int
	prefix                                  bool
	compare, differences, chart, elapsed    bool
	compareOffset, comparePan               int
	metric                                  string
	artifactPath                            map[string]string
	metricColumns, paramColumns             []string
	columnPan                               int
	downloadPending                         bool
	downloadRequest                         core.DownloadRequest
	downloadTarget                          string
	downloadGen                             uint64
	draft                                   core.Target
	draftEdit                               bool
	formPending                             bool
	targetForm                              *targetform.Model
	layout                                  core.LayoutPreferences
	layoutRevision                          uint64
	writer                                  *stateWriter
	zoom, resizing                          bool
	mousePressed                            string
	mouseContext                            string
	drag                                    string
	dragX, dragWidth                        int
	parentInfo                              *core.Run
	parentChild                             string
	parentGen                               uint64
	pickerSearch                            textinput.Model
	pickerTyping                            bool
	helpSearch                              textinput.Model
	helpTyping                              bool
}

func newModel(ctx context.Context, o Options) *model {
	i := textinput.New()
	i.CharLimit = 8192
	i.Prompt = "> "
	m := &model{ctx: ctx, opts: o, targets: append([]core.Target(nil), o.Targets...), active: o.InitialTarget, states: map[string]*targetState{}, cancel: map[string]context.CancelFunc{}, width: 100, height: 30, input: i, artifactPath: map[string]string{}, metricColumns: append([]string(nil), o.MetricColumns...), paramColumns: append([]string(nil), o.ParameterColumns...)}
	m.layout = core.DefaultLayout()
	m.writer = newStateWriter()
	m.pickerSearch = textinput.New()
	m.pickerSearch.Prompt = "Search: "
	m.helpSearch = textinput.New()
	m.helpSearch.Prompt = "Search: "
	if o.Mouse != nil {
		m.layout.Mouse = *o.Mouse
	}
	if m.active == "" && len(m.targets) > 0 {
		m.active = m.targets[0].ID
	}
	for _, t := range m.targets {
		m.states[t.ID] = newTargetState()
	}
	if len(m.targets) == 0 {
		m.status = "No targets. Press t then a to connect, or Ctrl+N to set up a new MLflow server."
	}
	m.initWorkspace()
	m.initInspection()
	return m
}
func newTargetState() *targetState {
	return &targetState{Visibility: map[string]core.Visibility{}, VisibilityTouched: map[string]bool{}, Runs: map[string]*runState{}, Artifacts: map[string]*artifactState{}, Basket: map[string]core.Run{}, Histories: map[string][]core.Metric{}, HistoryErrors: map[string]string{}}
}
func (m *model) Init() tea.Cmd {
	return tea.Batch(m.connect(), m.tick(), m.inspectionTick(), m.loadLayout(), m.loadVisibility(), m.activityTick(), m.loadRunPins(false, false))
}
func (m *model) tick() tea.Cmd {
	if m.opts.RefreshSeconds <= 0 {
		return nil
	}
	return tea.Tick(time.Duration(m.opts.RefreshSeconds)*time.Second, func(time.Time) tea.Msg { return refreshMsg{} })
}

type refreshMsg struct{}
type prefixExpired struct{ seq uint64 }

func (m *model) state() *targetState { return m.states[m.active] }
func (m *model) target() core.Target {
	for _, t := range m.targets {
		if t.ID == m.active {
			return t
		}
	}
	return core.Target{}
}
func (m *model) runs() *runState {
	if a := m.activityCurrent(); a != nil && a.Scope != scopeExperiment {
		return a.Views[a.Scope]
	}
	s := m.state()
	if s == nil {
		return nil
	}
	id := s.Selected
	return s.Runs[id]
}
func (m *model) run() *core.Run {
	s := m.runs()
	if s == nil {
		return nil
	}
	for i := range s.Rows {
		if s.Rows[i].ID() == s.Selected {
			if a := m.activityCurrent(); a != nil && a.Scope != scopeExperiment {
				if full, ok := a.Runs[s.Selected]; ok {
					return full
				}
			}
			return &s.Rows[i]
		}
	}
	if a := m.activityCurrent(); a != nil && a.Scope != scopeExperiment && a.Inspect != nil && a.Inspect.ID() == s.Selected {
		return a.Inspect
	}
	return nil
}
func (m *model) artifactKey() string {
	r := m.run()
	if r == nil {
		return ""
	}
	return r.ID() + "\x00" + m.artifactPath[m.active+"\x00"+r.ID()]
}
func (m *model) artifacts() *artifactState {
	s := m.state()
	k := m.artifactKey()
	if s == nil || k == "" {
		return nil
	}
	return s.Artifacts[k]
}
func (m *model) currentPath() string {
	r := m.run()
	if r == nil {
		return ""
	}
	return m.artifactPath[m.active+"\x00"+r.ID()]
}
func (m *model) operation(name string) (context.Context, uint64) {
	if c := m.cancel[name]; c != nil {
		c()
	}
	ctx, c := context.WithCancel(m.ctx)
	m.cancel[name] = c
	m.seq++
	return ctx, m.seq
}
func (m *model) stopAll() {
	m.stopArtifactPreview()
	m.releaseActivityRetention()
	m.stopActivity()
	m.stopInspection()
	m.seq++
	m.downloadGen = m.seq
	m.downloadPending = false
	for key, c := range m.cancel {
		c()
		delete(m.cancel, key)
	}
	for _, s := range m.states {
		s.ConnectGen = m.seq
		s.Gen = m.seq
		s.HistoryGen = m.seq
		s.CompareGen = m.seq
		s.ComparePending = false
		s.ConnectPending = false
		s.Pending = false
		s.HistoryPending = false
		for _, r := range s.Runs {
			r.Gen = m.seq
			r.Pending = false
			r.FirstPagePending = false
			r.LoadingAll = false
		}
		for _, a := range s.Artifacts {
			if a.Pending {
				a.Gen = 0
			} else {
				a.Gen = m.seq
			}
			a.Pending = false
		}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.refreshRowCache()
	defer m.syncInspection()
	if cmd, handled := m.updateArtifactPreview(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateRunPins(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateActivity(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateExtensions(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateWorkspace(msg); handled {
		return m, cmd
	}
	if cmd, handled := m.updateInspection(msg); handled {
		return m, cmd
	}
	switch v := msg.(type) {
	case layoutLoadedMsg:
		before := m.selectedExperiment()
		if v.err != nil {
			m.status = "Could not load layout: " + v.err.Error()
		} else if v.found && v.revision == m.layoutRevision {
			m.layout = v.value
			if m.layout.LeftRatio == .30 {
				m.layout.LeftRatio = .22
			}
			m.reselectExperiment()
			if m.opts.Mouse != nil {
				m.layout.Mouse = *m.opts.Mouse
			}
		}
		if before != m.selectedExperiment() {
			return m, m.loadRuns(false)
		}
		return m, nil
	case viewLoadedMsg:
		return m, m.acceptView(v)
	case visibilityLoadedMsg:
		return m, m.acceptVisibility(v)
	case preferencesSavedMsg:
		if v.err != nil {
			m.status = "Preferences not saved: " + v.err.Error()
		}
		return m, nil
	case parentLoadedMsg:
		return m, m.acceptParent(v)
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
		return m, m.handleMouse(msg)
	case tea.WindowSizeMsg:
		m.mousePressed = ""
		m.drag = ""
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		m.input.SetWidth(max(1, m.width-8))
		m.helpSearch.SetWidth(max(1, m.width-12))
		if m.targetForm != nil {
			return m, m.updateTargetForm(v)
		}
		return m, m.ensureHistories(false)
	case refreshMsg:
		if m.focus == 2 && m.tab == 1 || m.compare && m.chart {
			return m, m.tick()
		}
		if m.inputMode != "" || m.overlay != "" || m.targetForm != nil {
			return m, m.tick()
		}
		if s := m.state(); s != nil {
			if s.ConnectPending || s.Pending || s.HistoryPending || s.ComparePending {
				return m, m.tick()
			}
			if r := m.runs(); r != nil && r.Pending {
				return m, m.tick()
			}
			if a := m.artifacts(); a != nil && a.Pending {
				return m, m.tick()
			}
		}
		return m, tea.Batch(m.tick(), m.refresh())
	case prefixExpired:
		if v.seq == m.seq {
			m.prefix = false
		}
		return m, nil
	case connectedMsg:
		return m, m.acceptConnected(v)
	case experimentsMsg:
		return m, m.acceptExperiments(v)
	case runsMsg:
		return m, m.acceptRuns(v)
	case artifactsMsg:
		return m, m.acceptArtifacts(v)
	case historyMsg:
		return m, m.acceptHistory(v)
	case comparedMsg:
		return m, m.acceptCompared(v)
	case resultMsg:
		if v.target == "" || v.target == m.active {
			if v.err != nil {
				m.status = "Error: " + v.err.Error()
			} else {
				m.status = v.text
			}
		}
		return m, nil
	case downloadedMsg:
		return m, m.acceptDownload(v)
	case savedMsg:
		return m, m.acceptSaved(v)
	case tea.KeyPressMsg:
		m.mousePressed = ""
		m.drag = ""
		if m.targetForm != nil {
			return m, m.updateTargetForm(msg)
		}
		key := v.String()
		if key == "ctrl+c" {
			m.stopAll()
			return m, tea.Quit
		}
		if m.formPending {
			return m, nil
		}
		if m.inputMode != "" {
			return m, m.handleInput(msg, key)
		}
		if m.overlay != "" {
			if m.overlay == "help" {
				return m, m.handleHelp(msg, key)
			}
			if m.isPicker() {
				return m, m.handlePicker(msg, key)
			}
			return m, m.handleOverlay(key)
		}
		if m.resizing {
			return m, m.resizeKey(key)
		}
		if key == "g" {
			if m.prefix {
				m.prefix = false
				return m, m.move(-1 << 30)
			}
			m.prefix = true
			m.seq++
			seq := m.seq
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return prefixExpired{seq} })
		}
		m.prefix = false
		for _, a := range m.actions() {
			for _, k := range a.Keys {
				if k == key {
					return m, m.perform(a.ID)
				}
			}
		}
		return m, nil
	}
	if m.targetForm != nil {
		return m, m.updateTargetForm(msg)
	}
	if m.overlay == "help" && m.helpTyping {
		return m, m.updateHelpSearch(msg)
	}
	if m.isPicker() && m.pickerTyping {
		var cmd tea.Cmd
		m.pickerSearch, cmd = m.pickerSearch.Update(msg)
		m.menuIndex = 0
		return m, cmd
	}
	if m.inputMode != "" {
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.inputMode == "local" && before != m.input.Value() {
			m.setLocal(m.input.Value())
		}
		return m, cmd
	}
	return m, nil
}

type action struct {
	ID    string
	Keys  []string
	Label string
}

func act(id, keys, label string) action { return action{id, strings.Split(keys, "|"), label} }
func (m *model) actions() []action {
	a := append(m.artifactPreviewActions(), m.activityActions()...)
	a = append(a, m.runPinActions()...)
	a = append(a, m.workspaceActions()...)
	a = append(a, m.inspectionActions()...)
	a = append(a, act("server-setup", "ctrl+n", "Set up a persistent MLflow server"), act("model-registry", "O", "Browse registered models"), act("target-environment", "E", "Experiment environment"))
	if m.run() != nil {
		a = append(a, act("model-related", "C", "Models related to this run"), act("model-source", "ctrl+o", "Inspect a model or artifact URI"))
	}
	a = append(a, []action{act("quit", "q", "Quit"), act("help", "?", "Help"), act("palette", ":", "Actions"), act("targets", "t", "Switch target"), act("refresh", "r", "Refresh"), act("up", "up|k", "Move up"), act("down", "down|j", "Move down"), act("first", "home", "First row (also gg)"), act("last", "end|G", "Last row"), act("back", "esc", "Back / cancel"), act("nextpane", "tab", "Next pane"), act("prevpane", "shift+tab", "Previous pane"), act("left", "left|h", "Previous pane / parent / pan left"), act("right", "right|l", "Next pane / enter / pan right"), act("enter", "enter", "Inspect selection")}...)
	a = append(a, act("pane1", "1", "Focus experiments"), act("pane2", "2", "Focus runs"), act("pane3", "3", "Focus details"), act("zoom", "z", "Zoom / restore pane"), act("resize", "ctrl+w", "Resize panes"), act("mouse", "M", "Toggle mouse capture"), act("layout", "L", "Layout options"))
	if m.focus == 0 && m.activityScope() == scopeExperiment {
		a = append(a, act("info", "i", "Full experiment information"))
	}
	if m.compare {
		a = append(a, act("compare", "c", "Close comparison"), act("diff", "x", "Only differences"), act("chart", "v", "Toggle table / history chart"), act("axis", "a", "Toggle step / elapsed time"))
		return a
	}
	if m.focus < 2 {
		a = append(a, act("local", "/", "Search loaded rows"), act("filter", "f", "MLflow server filter"), act("sort", "s", "Sort order"), act("more", "n", "Load next page"))
	}
	if m.focus == 1 {
		a = append(a, act("basket", "space", "Select run for comparison"), act("columns", "v", "Choose columns"), act("previous-column", "[", "Previous metric / parameter column"), act("next-column", "]", "Next metric / parameter column"))
		if !m.compare && m.run() != nil {
			a = append(a, act("run-info", "i", "Full run information / name"))
		}
	}
	if m.focus == 1 {
		if m.activityScope() == scopeExperiment {
			a = append(a, act("group", "b", "Group runs"))
		}
		a = append(a, act("loadall", "A", "Load all matching runs"), act("parent", "P", "Inspect parent run"))
	}
	if m.focus < 2 {
		a = append(a, act("visibility", "V", "Local visibility filter"))
	}
	if (m.focus == 0 && m.activityScope() == scopeExperiment && m.state() != nil && m.state().Selected != "") || (m.focus != 0 && m.run() != nil) {
		a = append(a, act("hide", "H", "Hide locally"), act("archive", "X", "Archive locally"), act("restore", "U", "Restore local visibility"))
	}
	if r := m.runs(); r != nil && r.LoadingAll {
		a = append(a, act("cancel-all", "ctrl+x", "Cancel loading all"))
	}
	if s := m.state(); s != nil && len(s.Basket) > 0 {
		a = append(a, act("compare", "c", fmt.Sprintf("Compare %d selected runs", len(s.Basket))))
	}
	if (m.focus == 0 && m.activityScope() == scopeExperiment && m.state() != nil && m.state().Selected != "") || (m.focus != 0 && m.run() != nil) {
		a = append(a, act("open", "o", "Open MLflow web page"), act("copy", "y", "Copy full ID / artifact path"))
	}
	if m.focus == 2 {
		a = append(a, act("prevtab", "[", "Previous detail tab"), act("nexttab", "]", "Next detail tab"))
		if m.tab == 4 && m.run() != nil {
			a = append(a, act("download", "d", "Download artifact / directory"), act("download-current", "D", "Download current directory"))
		}
		if m.tab == 1 && m.run() != nil {
			a = append(a, act("axis", "a", "Toggle step / elapsed time"))
		}
	}
	if m.downloadPending {
		a = append(a, act("cancel-download", "ctrl+x", "Cancel download"))
	}
	return a
}

func (m *model) perform(id string) tea.Cmd {
	if id == "open-pinned" {
		return m.openActivity(scopePinned, true)
	}
	if id == "toggle-run-pin" {
		return m.toggleRunPin()
	}
	if id == "run-info" {
		return m.openPicker("run-info")
	}
	if id == "artifact-preview" {
		return m.openArtifactPreview(false)
	}
	if cmd, ok := m.performActivity(id); ok {
		return cmd
	}
	switch id {
	case "server-setup":
		return m.openServerSetup()
	case "target-environment":
		return m.openTargetEnvironment(m.target())
	case "model-registry":
		return m.openModels("registry", "")
	case "model-related":
		if r := m.run(); r != nil {
			return m.openModels("related", r.ID())
		}
		return nil
	case "model-source":
		return m.openModelSource()
	}
	m.syncInspection()
	if cmd, ok := m.performWorkspace(id); ok {
		return cmd
	}
	if cmd, ok := m.performInspection(id); ok {
		return cmd
	}
	switch id {
	case "pane1", "pane2", "pane3":
		m.focus = int(id[len(id)-1] - '1')
		m.mousePressed = ""
		return m.ensureDetails()
	case "zoom":
		m.zoom = !m.zoom
		m.mousePressed = ""
	case "resize":
		m.resizing = true
		m.layoutRevision++
		m.status = "Resize: h/l left pane · k/j top pane · 0 reset · Enter/Esc finish"
	case "mouse":
		m.layout.Mouse = !m.layout.Mouse
		m.mousePressed = ""
		return m.saveLayout()
	case "layout", "group", "visibility", "info":
		return m.openPicker(id)
	case "hide":
		return m.setVisibility(core.VisibilityHidden)
	case "archive":
		return m.setVisibility(core.VisibilityArchived)
	case "restore":
		return m.setVisibility(core.VisibilityNormal)
	case "loadall":
		if r := m.runs(); r != nil {
			if r.Pending {
				m.status = "Runs are updating; wait for this page before loading all"
				return nil
			}
			if r.Err != "" {
				m.status = "Paging stopped after an error; press r to refresh the query"
				return nil
			}
			r.LoadingAll = true
			m.status = "Loading all matching runs · Ctrl+X cancels"
			if len(r.Rows) == 0 && r.Next == "" {
				cmd := m.loadRuns(false)
				r.LoadingAll = true
				return cmd
			}
			return m.loadRuns(true)
		}
	case "cancel-all":
		if c := m.cancel["runs"]; c != nil {
			c()
		}
		if r := m.runs(); r != nil {
			m.seq++
			r.Gen = m.seq
			r.Pending = false
			r.FirstPagePending = false
			r.LoadingAll = false
		}
		m.status = "Loading all cancelled; loaded rows retained"
	case "parent":
		return m.loadParent()
	case "quit":
		m.stopAll()
		return tea.Quit
	case "help":
		m.overlay = id
		m.menuIndex, m.menuOffset = 0, 0
		m.prefix, m.helpTyping = false, false
		m.mousePressed = ""
		m.helpSearch.SetValue("")
		m.helpSearch.SetWidth(max(1, m.width-12))
		m.helpSearch.Blur()
	case "palette":
		m.overlay = id
		m.menuIndex = 0
		m.menuOffset = 0
	case "targets":
		m.overlay = "targets"
		m.menuIndex = 0
		for i, t := range m.targets {
			if t.ID == m.active {
				m.menuIndex = i
			}
		}
	case "refresh":
		return m.refresh()
	case "up":
		return m.move(-1)
	case "down":
		return m.move(1)
	case "first":
		return m.move(-1 << 30)
	case "last":
		return m.move(1 << 30)
	case "nextpane":
		m.focus = (m.focus + 1) % 3
		return m.ensureDetails()
	case "prevpane":
		m.focus = (m.focus + 2) % 3
		return m.ensureDetails()
	case "left", "right":
		d := 1
		if id == "left" {
			d = -1
		}
		if m.compare {
			m.comparePan = max(0, m.comparePan+d)
			return nil
		}
		if m.focus == 1 && m.treeNavigation(d) {
			return m.saveView()
		}
		if m.focus == 2 {
			if m.tab == 4 {
				if d < 0 {
					return m.artifactParent()
				}
				return m.artifactEnter()
			}
			m.tab = (m.tab + d + 6) % 6
			m.detailOffset = 0
			return m.ensureDetails()
		}
		m.focus = (m.focus + d + 3) % 3
		return m.ensureDetails()
	case "enter":
		if m.focus == 0 {
			m.focus = 1
			return m.loadRuns(false)
		}
		if m.focus == 1 {
			if row := m.selectedRunRow(); row != nil && row.Expandable {
				return m.toggleRow(*row)
			}
			if m.run() == nil {
				return nil
			}
			m.focus = 2
			return tea.Batch(m.ensureDetails(), m.activityReadSelected(false))
		}
		if m.tab == 4 {
			return m.artifactEnter()
		}
	case "back":
		if s := m.state(); s != nil && s.ConnectPending {
			if c := m.cancel["connect"]; c != nil {
				c()
			}
			m.seq++
			s.ConnectGen = m.seq
			s.ConnectPending = false
			m.status = "Connection cancelled. Press r to retry."
			return nil
		}
		if r := m.runs(); r != nil && r.LoadingAll {
			return m.perform("cancel-all")
		}
		if m.downloadPending {
			return m.perform("cancel-download")
		}
		if m.compare {
			m.compare = false
			m.chart = false
			return nil
		}
		if m.focus == 2 && m.tab == 4 && m.currentPath() != "" {
			return m.artifactParent()
		}
		m.focus = max(0, m.focus-1)
	case "local":
		value := ""
		if m.focus == 0 {
			if s := m.state(); s != nil {
				value = s.Local
			}
		} else if r := m.runs(); r != nil {
			value = r.Local
		}
		return m.startInput("local", "Search loaded rows (Enter keeps query; Esc clears)", value)
	case "filter":
		value := ""
		if m.focus == 0 {
			if s := m.state(); s != nil {
				value = s.Filter
			}
		} else if r := m.runs(); r != nil {
			value = r.Filter
		}
		return m.startInput("filter", "MLflow filter (submitted to server)", value)
	case "sort":
		if m.focus == 1 {
			return m.openPicker("sort")
		}
		value := "name ASC"
		if m.focus == 0 {
			if s := m.state(); s != nil && s.Order != "" {
				value = s.Order
			}
		} else if r := m.runs(); r != nil {
			value = r.Order
		}
		return m.startInput("sort", "Server order: attributes.start_time DESC, metrics.loss ASC", value)
	case "columns":
		return m.openPicker("columns")
	case "previous-column":
		m.columnPan = max(0, m.columnPan-1)
	case "next-column":
		m.columnPan = clamp(m.columnPan+1, 0, len(m.viewColumns())-2)
	case "more":
		if m.focus == 0 {
			return m.loadExperiments(true)
		}
		return m.loadRuns(true)
	case "basket":
		if s, r := m.state(), m.run(); s != nil && r != nil {
			if _, ok := s.Basket[r.ID()]; ok {
				delete(s.Basket, r.ID())
				for i, id := range s.BasketOrder {
					if id == r.ID() {
						s.BasketOrder = append(s.BasketOrder[:i], s.BasketOrder[i+1:]...)
						break
					}
				}
			} else {
				s.Basket[r.ID()] = *r
				s.BasketOrder = append(s.BasketOrder, r.ID())
			}
			m.status = fmt.Sprintf("%d runs selected on %s", len(s.Basket), m.target().Label())
		}
	case "compare":
		m.compare = !m.compare
		m.chart = false
		m.compareOffset = 0
		m.comparePan = 0
		if m.compare {
			return m.loadComparison()
		}
	case "diff":
		m.differences = !m.differences
		m.compareOffset = 0
	case "history":
		keys := m.metricKeys()
		if len(keys) == 0 {
			m.status = "No metrics available"
			return nil
		}
		m.overlay = "metrics"
		m.menuIndex = 0
	case "chart":
		m.chart = !m.chart
		if m.chart && m.metric == "" {
			return m.perform("inspect-metric")
		}
		if m.chart {
			return m.loadHistory()
		}
	case "axis":
		m.elapsed = !m.elapsed
	case "prevtab":
		m.tab = (m.tab + 5) % 6
		m.detailOffset = 0
		return m.ensureDetails()
	case "nexttab":
		m.tab = (m.tab + 1) % 6
		m.detailOffset = 0
		return m.ensureDetails()
	case "open":
		return m.open()
	case "copy":
		return m.copy()
	case "download":
		return m.prepareDownload()
	case "download-current":
		return m.prepareDirectoryDownload()
	case "cancel-download":
		if c := m.cancel["download"]; c != nil {
			c()
		}
		m.status = "Cancelling download…"
	}
	return nil
}

func (m *model) startInput(mode, label, value string) tea.Cmd {
	m.inputMode = mode
	m.inputLabel = label
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.SetWidth(max(1, m.width-8))
	m.prefix = false
	return m.input.Focus()
}
func (m *model) closeInput() { m.inputMode = ""; m.input.Blur() }
func (m *model) handleInput(msg tea.Msg, key string) tea.Cmd {
	mode := m.inputMode
	if key == "esc" {
		m.closeInput()
		if mode == "local" {
			m.setLocal("")
		}
		return nil
	}
	if key == "enter" {
		value := m.input.Value()
		m.closeInput()
		switch mode {
		case "local":
			if m.focus == 0 {
				return m.loadRuns(false)
			}
			return m.ensureDetails()
		case "filter", "sort":
			if m.focus == 0 {
				if s := m.state(); s != nil {
					if mode == "filter" {
						s.Filter = value
					} else {
						s.Order = value
					}
					return m.loadExperiments(false)
				}
			} else if r := m.runs(); r != nil {
				if mode == "filter" {
					r.Filter = value
				} else {
					r.Order = value
				}
				return tea.Batch(m.saveView(), m.loadRuns(false))
			}
		case "columns":
			var metrics, params []string
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				kind, name, ok := strings.Cut(item, ":")
				name = strings.TrimSpace(name)
				if !ok || name == "" || (kind != "metric" && kind != "param") {
					m.status = "Invalid column; use metric:loss,param:learning_rate"
					return m.startInput(mode, m.inputLabel, value)
				}
				if kind == "metric" {
					metrics = append(metrics, name)
				} else {
					params = append(params, name)
				}
			}
			m.metricColumns = metrics
			m.paramColumns = params
			m.columnPan = 0
		case "download":
			m.downloadRequest.Destination = value
			return m.download(false)
		}
		return nil
	}
	if mode == "local" && (key == "up" || key == "down") {
		if key == "up" {
			return m.move(-1)
		}
		return m.move(1)
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if mode == "local" && before != m.input.Value() {
		m.setLocal(m.input.Value())
	}
	return cmd
}

func (m *model) setLocal(value string) {
	if m.activityScope() != scopeExperiment && m.focus == 1 {
		m.releaseActivityRetention()
	}
	if m.focus == 0 {
		if s := m.state(); s != nil {
			s.Local = value
			s.Index = 0
			s.Offset = 0
			m.selectExperiment(0)
		}
	} else if r := m.runs(); r != nil {
		r.Local = value
		r.Index = 0
		r.Offset = 0
		m.selectRun(0)
	}
}
func (m *model) experimentsVisible() []core.Experiment {
	s := m.state()
	if s == nil {
		return nil
	}
	var out []core.Experiment
	q := strings.ToLower(s.Local)
	for _, e := range s.Experiments {
		if visibilityMatches(m.layout.ExperimentVisibility, s.Visibility[core.VisibilityKey("experiment", e.ID)]) && strings.Contains(strings.ToLower(e.Name+" "+e.ID), q) {
			out = append(out, e)
		}
	}
	return m.catalogInspectionExperiments(out)
}
func (m *model) runsVisible() []core.Run {
	r := m.runs()
	if r == nil {
		return nil
	}
	var out []core.Run
	q := strings.ToLower(r.Local)
	for _, v := range r.Rows {
		if visibilityMatches(r.View.Visibility, m.state().Visibility[core.VisibilityKey("run", v.ID())]) && strings.Contains(strings.ToLower(v.Name()+" "+v.ID()+" "+v.Info.Status), q) {
			out = append(out, v)
		}
	}
	return out
}
func (m *model) selectExperiment(index int) {
	s := m.state()
	rows := m.experimentsVisible()
	if s == nil {
		return
	}
	s.Index = clamp(index, 0, len(rows)-1)
	s.Selected = ""
	if len(rows) > 0 {
		s.Selected = rows[s.Index].ID
		if s.Runs[s.Selected] == nil {
			s.Runs[s.Selected] = &runState{listState: listState{Order: "attributes.start_time DESC"}, View: core.DefaultView(m.metricColumns, m.paramColumns)}
		}
	}
}
func (m *model) selectRun(index int) {
	m.refreshRowCache()
	s := m.runs()
	rows := m.runRows()
	if s == nil {
		return
	}
	previous := s.Selected
	s.Index = clamp(index, 0, len(rows)-1)
	s.Selected = ""
	if len(rows) > 0 {
		s.Selected = rows[s.Index].ID
	}
	if s.Selected != previous {
		m.chart = false
		m.releaseActivityRetention()
	}
	m.detailOffset = 0
}
func (m *model) move(delta int) tea.Cmd {
	if !m.compare && m.focus == 2 && m.isInspectionTab() {
		return m.moveInspection(delta)
	}
	if m.compare {
		m.compareOffset = clamp(m.compareOffset+delta, 0, max(0, len(comparisonRows(m.selectedRuns(), m.differences))-1))
		return nil
	}
	switch m.focus {
	case 0:
		if m.activityAvailable() {
			return m.moveActivitySidebar(delta)
		}
		if s := m.state(); s != nil {
			before := s.Selected
			m.selectExperiment(s.Index + delta)
			if before != s.Selected {
				return m.loadRuns(false)
			}
		}
	case 1:
		if r := m.runs(); r != nil {
			m.selectRun(r.Index + delta)
			return tea.Batch(m.ensureDetails(), m.activityReadSelected(true))
		}
	case 2:
		if m.tab == 4 {
			if a := m.artifacts(); a != nil {
				a.Index = clamp(a.Index+delta, 0, len(a.Rows)-1)
				if len(a.Rows) > 0 {
					a.Selected = a.Rows[a.Index].Path
				}
			}
		} else {
			count := 8
			if r := m.run(); r != nil {
				switch m.tab {
				case 1:
					count = len(r.Data.Metrics) + 2
				case 2:
					count = len(r.Data.Params)
				case 3:
					count = len(r.Data.Tags)
				case 5:
					count = len(r.Inputs.DatasetInputs) * 5
				}
			}
			m.detailOffset = clamp(m.detailOffset+delta, 0, max(0, count-1))
		}
	}
	return nil
}
func (m *model) ensureDetails() tea.Cmd {
	if cmd := m.ensureActivityRun(); cmd != nil {
		return cmd
	}
	m.syncInspection()
	if m.tab != 1 {
		m.cancelUnusedHistories()
	}
	if m.tab == 1 {
		return m.ensureHistories(false)
	}
	if m.tab == 4 && m.run() != nil {
		a := m.artifacts()
		if a == nil || a.Gen == 0 {
			return m.loadArtifacts()
		}
	}
	return nil
}
func (m *model) artifactParent() tea.Cmd {
	r := m.run()
	if r == nil {
		return nil
	}
	p := path.Dir(m.currentPath())
	if p == "." {
		p = ""
	}
	m.artifactPath[m.active+"\x00"+r.ID()] = p
	return m.loadArtifacts()
}
func (m *model) artifactEnter() tea.Cmd {
	a, r := m.artifacts(), m.run()
	if a == nil || r == nil || len(a.Rows) == 0 {
		return nil
	}
	v := a.Rows[clamp(a.Index, 0, len(a.Rows)-1)]
	if !v.IsDir {
		return m.openArtifactPreview(false)
	}
	m.artifactPath[m.active+"\x00"+r.ID()] = v.Path
	return m.loadArtifacts()
}
func clamp(n, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(n, lo), hi)
}
