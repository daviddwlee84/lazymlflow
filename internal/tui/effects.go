package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
	"github.com/daviddwlee84/lazymlflow/internal/targetform"
)

type connectedMsg struct {
	target  string
	gen     uint64
	session *core.Session
	err     error
}
type experimentsMsg struct {
	target string
	gen    uint64
	page   core.ExperimentPage
	append bool
	err    error
}
type runsMsg struct {
	target, experiment string
	gen                uint64
	page               core.RunPage
	append             bool
	err                error
}
type artifactsMsg struct {
	target, key string
	gen         uint64
	page        core.ArtifactPage
	err         error
}
type historyMsg struct {
	target    string
	gen       uint64
	histories map[string][]core.Metric
	errs      map[string]string
}
type comparedMsg struct {
	target string
	gen    uint64
	runs   []core.Run
	err    error
}
type resultMsg struct {
	target, text string
	err          error
}
type downloadedMsg struct {
	target string
	gen    uint64
	result core.DownloadResult
	exists bool
	err    error
}
type savedMsg struct {
	targets  []core.Target
	selected string
	err      error
}

func (m *model) connect() tea.Cmd {
	s := m.state()
	if s == nil {
		return nil
	}
	if s.Session != nil {
		return m.loadExperiments(false)
	}
	if m.opts.Connector == nil {
		s.ConnectErr = "No connection manager configured"
		return nil
	}
	ctx, g := m.operation("connect")
	s.ConnectGen = g
	s.ConnectPending = true
	s.ConnectErr = ""
	t := m.target()
	connector := m.opts.Connector
	priorSession := s.Retiring
	return func() tea.Msg {
		if priorSession != nil {
			if err := priorSession.Close(); err != nil {
				return connectedMsg{target: t.ID, gen: g, err: fmt.Errorf("close previous target session: %w", err)}
			}
		}
		if err := ctx.Err(); err != nil {
			return connectedMsg{target: t.ID, gen: g, err: err}
		}
		session, err := connector.Open(ctx, t)
		return connectedMsg{t.ID, g, session, err}
	}
}
func (m *model) acceptConnected(v connectedMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || v.gen != s.ConnectGen || v.target != m.active {
		return nil
	}
	s.ConnectPending = false
	if v.err != nil {
		s.ConnectErr = v.err.Error()
		return nil
	}
	if v.session == nil || v.session.Backend == nil {
		s.ConnectErr = "Connection returned no backend"
		return nil
	}
	s.Session = v.session
	s.Retiring = nil
	s.ConnectErr = ""
	return tea.Batch(m.loadExperiments(false), m.loadVisibility())
}
func (m *model) refresh() tea.Cmd {
	if m.inspect != nil && ((m.focus == 2 && m.tab == 1 && m.run() != nil) || (m.compare && m.chart)) {
		return m.refreshInspection()
	}
	s := m.state()
	if s == nil {
		return nil
	}
	if s.Session == nil {
		return m.connect()
	}
	if m.chart && (m.compare || (m.focus == 2 && m.tab == 1)) {
		return m.loadHistory()
	}
	if m.compare {
		return m.loadComparison()
	}
	if m.focus == 2 && m.tab == 4 {
		return m.loadArtifacts()
	}
	if m.focus == 0 {
		return m.loadExperiments(false)
	}
	return m.loadRuns(false)
}
func (m *model) loadExperiments(more bool) tea.Cmd {
	s := m.state()
	if s == nil || s.Session == nil {
		return nil
	}
	if more && s.Next == "" {
		m.status = "All experiments are loaded"
		return nil
	}
	ctx, g := m.operation("experiments")
	s.Gen = g
	s.Pending = true
	s.Err = ""
	q := core.ExperimentQuery{Filter: s.Filter, MaxResults: 100, ViewType: "ACTIVE_ONLY"}
	if s.Order != "" {
		q.OrderBy = splitOrder(s.Order)
	} else {
		q.OrderBy = []string{"name ASC"}
	}
	if more {
		q.PageToken = s.Next
	}
	b, t := s.Session.Backend, m.active
	return func() tea.Msg { p, err := b.SearchExperiments(ctx, q); return experimentsMsg{t, g, p, more, err} }
}
func (m *model) acceptExperiments(v experimentsMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || s.Gen != v.gen || m.active != v.target {
		return nil
	}
	s.Pending = false
	if v.err != nil {
		s.Err = v.err.Error()
		return nil
	}
	selected, index := s.Selected, s.Index
	if v.append {
		seen := map[string]bool{}
		for _, e := range s.Experiments {
			seen[e.ID] = true
		}
		for _, e := range v.page.Experiments {
			if !seen[e.ID] {
				s.Experiments = append(s.Experiments, e)
			}
		}
	} else {
		s.Experiments = v.page.Experiments
	}
	s.Next = v.page.NextPageToken
	s.Err = ""
	for i, e := range m.experimentsVisible() {
		if e.ID == selected {
			index = i
			break
		}
	}
	m.selectExperiment(index)
	return tea.Batch(m.loadRuns(false), m.loadView())
}
func (m *model) loadRuns(more bool) tea.Cmd {
	s, r := m.state(), m.runs()
	if s == nil || s.Session == nil || s.Selected == "" || r == nil {
		return nil
	}
	if more && r.Pending {
		m.status = "Runs are updating; wait for this page before requesting another"
		return nil
	}
	if more && r.Err != "" {
		r.LoadingAll = false
		m.status = "Paging stopped after an error; press r to refresh the query"
		return nil
	}
	if more && r.Next == "" {
		r.LoadingAll = false
		m.status = "All runs are loaded"
		return nil
	}
	for _, prior := range s.Runs {
		prior.Pending = false
	}
	ctx, g := m.operation("runs")
	r.Gen = g
	r.Pending = true
	r.Err = ""
	if !more {
		m.clearCatalogInspection()
		r.LoadingAll = false
		r.Next = ""
		r.SeenPageTokens = map[string]bool{}
	}
	r.FirstPagePending = !more
	q := core.RunQuery{ExperimentIDs: []string{s.Selected}, Filter: r.Filter, OrderBy: splitOrder(r.Order), MaxResults: 100, ViewType: "ACTIVE_ONLY"}
	if more {
		q.PageToken = r.Next
		if r.SeenPageTokens == nil {
			r.SeenPageTokens = map[string]bool{}
		}
		r.SeenPageTokens[q.PageToken] = true
	}
	b, t, e := s.Session.Backend, m.active, s.Selected
	fetch := func() tea.Msg { p, err := b.SearchRuns(ctx, q); return runsMsg{t, e, g, p, more, err} }
	prefs := m.loadView()
	if prefs != nil {
		return tea.Batch(fetch, prefs)
	}
	return fetch
}
func (m *model) acceptRuns(v runsMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || m.active != v.target || s.Selected != v.experiment {
		return nil
	}
	r := s.Runs[v.experiment]
	if r == nil || r.Gen != v.gen {
		return nil
	}
	r.Pending = false
	r.FirstPagePending = false
	if v.err != nil {
		r.Err = v.err.Error()
		r.LoadingAll = false
		return nil
	}
	r.RowsVersion++
	selected, index := r.Selected, r.Index
	if v.append {
		seen := map[string]bool{}
		for _, run := range r.Rows {
			seen[run.ID()] = true
		}
		for _, run := range v.page.Runs {
			if !seen[run.ID()] {
				r.Rows = append(r.Rows, run)
				seen[run.ID()] = true
			}
		}
	} else {
		r.Rows = v.page.Runs
	}
	r.Next = v.page.NextPageToken
	r.Err = ""
	if r.Next != "" {
		if r.SeenPageTokens == nil {
			r.SeenPageTokens = map[string]bool{}
		}
		if r.SeenPageTokens[r.Next] {
			r.Next = ""
			r.LoadingAll = false
			r.Err = "Pagination stopped: server repeated a page token; loaded rows retained. Press r to refresh."
		} else {
			r.SeenPageTokens[r.Next] = true
		}
	}
	for i, run := range m.runRows() {
		if run.ID == selected {
			index = i
			break
		}
	}
	m.selectRun(index)
	for _, run := range r.Rows {
		if _, ok := s.Basket[run.ID()]; ok {
			s.Basket[run.ID()] = run
		}
	}
	details := m.ensureDetails()
	if r.LoadingAll {
		if r.Next != "" {
			return tea.Batch(details, m.loadRuns(true))
		}
		r.LoadingAll = false
		m.status = fmt.Sprintf("All %d matching runs loaded", len(r.Rows))
	}
	return details
}
func (m *model) loadArtifacts() tea.Cmd {
	s, r, a := m.state(), m.run(), m.artifacts()
	if s == nil || s.Session == nil || r == nil {
		return nil
	}
	if a == nil {
		a = &artifactState{}
		s.Artifacts[m.artifactKey()] = a
	}
	for _, prior := range s.Artifacts {
		prior.Pending = false
	}
	ctx, g := m.operation("artifacts")
	a.Gen = g
	a.Pending = true
	a.Err = ""
	b, t, id, p, k := s.Session.Backend, m.active, r.ID(), m.currentPath(), m.artifactKey()
	return func() tea.Msg { page, err := b.ListArtifacts(ctx, id, p); return artifactsMsg{t, k, g, page, err} }
}
func (m *model) acceptArtifacts(v artifactsMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || m.active != v.target || m.artifactKey() != v.key {
		return nil
	}
	a := s.Artifacts[v.key]
	if a == nil || a.Gen != v.gen {
		return nil
	}
	a.Pending = false
	if v.err != nil {
		a.Err = v.err.Error()
		return nil
	}
	a.Rows = v.page.Files
	a.Root = v.page.RootURI
	a.Err = ""
	sort.SliceStable(a.Rows, func(i, j int) bool {
		if a.Rows[i].IsDir != a.Rows[j].IsDir {
			return a.Rows[i].IsDir
		}
		return a.Rows[i].Path < a.Rows[j].Path
	})
	for i, f := range a.Rows {
		if f.Path == a.Selected {
			a.Index = i
			break
		}
	}
	a.Index = clamp(a.Index, 0, len(a.Rows)-1)
	a.Selected = ""
	if len(a.Rows) > 0 {
		a.Selected = a.Rows[a.Index].Path
	}
	return nil
}
func (m *model) selectedRuns() []core.Run {
	s := m.state()
	if s == nil {
		return nil
	}
	if !m.compare {
		if r := m.run(); r != nil {
			return []core.Run{*r}
		}
		return nil
	}
	var runs []core.Run
	for _, id := range s.BasketOrder {
		if r, ok := s.Basket[id]; ok {
			runs = append(runs, r)
		}
	}
	return runs
}
func (m *model) loadComparison() tea.Cmd {
	s := m.state()
	if s == nil || s.Session == nil || len(s.BasketOrder) == 0 {
		return nil
	}
	ctx, g := m.operation("compare")
	s.CompareGen = g
	s.ComparePending = true
	s.CompareErr = ""
	ids := append([]string(nil), s.BasketOrder...)
	b, target := s.Session.Backend, m.active
	return func() tea.Msg { runs, err := core.Compare(ctx, b, ids); return comparedMsg{target, g, runs, err} }
}
func (m *model) acceptCompared(v comparedMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || m.active != v.target || s.CompareGen != v.gen {
		return nil
	}
	s.ComparePending = false
	if v.err != nil {
		s.CompareErr = v.err.Error()
		return nil
	}
	s.CompareErr = ""
	for _, run := range v.runs {
		if _, exists := s.Basket[run.ID()]; exists {
			s.Basket[run.ID()] = run
		}
	}
	return nil
}
func (m *model) metricKeys() []string {
	seen := map[string]bool{}
	for _, r := range m.selectedRuns() {
		for _, metric := range r.Data.Metrics {
			seen[metric.Key] = true
		}
	}
	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func (m *model) loadHistory() tea.Cmd {
	return m.ensureHistories(true)
}
func (m *model) acceptHistory(v historyMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil || v.target != m.active || s.HistoryGen != v.gen {
		return nil
	}
	s.HistoryPending = false
	for k, h := range v.histories {
		s.Histories[k] = h
		delete(s.HistoryErrors, k)
	}
	for k, e := range v.errs {
		s.HistoryErrors[k] = e
	}
	return nil
}
func (m *model) open() tea.Cmd {
	s := m.state()
	if s == nil || s.Session == nil {
		return nil
	}
	base := s.Session.WebURL
	if base == "" {
		base = s.Session.BaseURL
	}
	id := ""
	if m.focus != 0 {
		if r := m.run(); r != nil {
			id = r.ID()
		}
	}
	url := platform.ResourceURL(base, s.Selected, id)
	ctx, target := m.ctx, m.active
	return func() tea.Msg { err := platform.OpenURL(ctx, url); return resultMsg{target, "Opened " + url, err} }
}
func (m *model) copy() tea.Cmd {
	s := m.state()
	if s == nil {
		return nil
	}
	value := s.Selected
	if m.focus != 0 {
		if r := m.run(); r != nil {
			value = r.ID()
		}
	}
	if m.focus == 2 && m.tab == 4 {
		value = m.currentPath()
		if a := m.artifacts(); a != nil && len(a.Rows) > 0 {
			value = a.Rows[clamp(a.Index, 0, len(a.Rows)-1)].Path
		}
	}
	ctx, target := m.ctx, m.active
	return func() tea.Msg { err := platform.Copy(ctx, value); return resultMsg{target, "Copied " + value, err} }
}
func (m *model) prepareDownload() tea.Cmd {
	r := m.run()
	if r == nil {
		return nil
	}
	if m.downloadPending {
		m.status = "A download is already running; Ctrl+X cancels it"
		return nil
	}
	p := m.currentPath()
	if a := m.artifacts(); a != nil && len(a.Rows) > 0 {
		p = a.Rows[clamp(a.Index, 0, len(a.Rows)-1)].Path
	}
	m.downloadRequest = core.DownloadRequest{RunID: r.ID(), Path: p}
	m.downloadTarget = m.active
	return m.startInput("download", "Download destination (exact file or directory path)", filepath.Join(r.ID(), filepath.FromSlash(p)))
}
func (m *model) prepareDirectoryDownload() tea.Cmd {
	r := m.run()
	if r == nil {
		return nil
	}
	if m.downloadPending {
		m.status = "A download is already running; Ctrl+X cancels it"
		return nil
	}
	p := m.currentPath()
	m.downloadRequest = core.DownloadRequest{RunID: r.ID(), Path: p}
	m.downloadTarget = m.active
	return m.startInput("download", "Download directory destination", filepath.Join(r.ID(), filepath.FromSlash(p)))
}
func (m *model) download(overwrite bool) tea.Cmd {
	s := m.states[m.downloadTarget]
	if s == nil || s.Session == nil {
		return nil
	}
	req := m.downloadRequest
	if strings.TrimSpace(req.Destination) == "" {
		m.status = "Download destination cannot be empty"
		return nil
	}
	ctx, g := m.operation("download")
	m.downloadGen = g
	m.downloadPending = true
	m.status = "Downloading " + req.Path + " (Ctrl+X cancels)"
	req.Overwrite = overwrite
	b, target := s.Session.Backend, m.downloadTarget
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return downloadedMsg{target: target, gen: g, err: err}
		}
		if !overwrite {
			_, err := os.Lstat(req.Destination)
			if err == nil {
				if err := ctx.Err(); err != nil {
					return downloadedMsg{target: target, gen: g, err: err}
				}
				return downloadedMsg{target: target, gen: g, exists: true}
			}
			if !errors.Is(err, os.ErrNotExist) {
				return downloadedMsg{target: target, gen: g, err: err}
			}
		}
		r, err := b.DownloadArtifact(ctx, req, nil)
		return downloadedMsg{target: target, gen: g, result: r, err: err}
	}
}
func (m *model) acceptDownload(v downloadedMsg) tea.Cmd {
	if v.gen != m.downloadGen {
		return nil
	}
	m.downloadPending = false
	if v.exists {
		m.overlay = "overwrite"
		m.menuIndex = 0
		return nil
	}
	if v.err != nil {
		if errors.Is(v.err, context.Canceled) {
			m.status = "Download cancelled"
		} else {
			m.status = "Download failed: " + v.err.Error()
		}
		return nil
	}
	m.status = fmt.Sprintf("Downloaded %d files (%s) to %s", v.result.Files, bytes(v.result.Bytes), v.result.Path)
	return nil
}
func splitOrder(s string) []string {
	var result []string
	start := 0
	quoted := false
	for i, r := range s {
		if r == '`' {
			quoted = !quoted
		}
		if r == ',' && !quoted {
			if v := strings.TrimSpace(s[start:i]); v != "" {
				result = append(result, v)
			}
			start = i + 1
		}
	}
	if v := strings.TrimSpace(s[start:]); v != "" {
		result = append(result, v)
	}
	return result
}

func (m *model) handleOverlay(key string) tea.Cmd {
	if m.formPending {
		return nil
	}
	if key == "esc" || key == "q" {
		m.overlay = ""
		return nil
	}
	switch m.overlay {
	case "overwrite":
		if key == "y" {
			m.overlay = ""
			return m.download(true)
		}
		if key == "n" {
			m.overlay = ""
			m.status = "Download cancelled; existing destination kept"
		}
		return nil
	case "targets":
		if key == "s" {
			return m.openServerSetup()
		}
		if key == "E" && len(m.targets) > 0 {
			return m.openTargetEnvironment(m.targets[clamp(m.menuIndex, 0, len(m.targets)-1)])
		}
		if key == "a" {
			return m.startTargetForm(core.Target{}, false)
		}
		if key == "e" && len(m.targets) > 0 {
			draft := m.targets[clamp(m.menuIndex, 0, len(m.targets)-1)]
			if draft.Transient {
				m.status = "This target is temporary. Press a to save a new target."
				return nil
			}
			return m.startTargetForm(draft, true)
		}
	}
	length := 0
	switch m.overlay {
	case "targets":
		length = len(m.targets)
	case "metrics":
		length = len(m.metricKeys())
	case "palette", "help":
		length = len(m.actions())
		if m.overlay == "help" {
			length += 4
		}
	}
	switch key {
	case "up", "k":
		m.menuIndex = clamp(m.menuIndex-1, 0, length-1)
	case "down", "j":
		m.menuIndex = clamp(m.menuIndex+1, 0, length-1)
	case "home":
		m.menuIndex = 0
	case "end", "G":
		m.menuIndex = max(0, length-1)
	case "enter":
		kind := m.overlay
		m.overlay = ""
		switch kind {
		case "targets":
			if len(m.targets) == 0 {
				return nil
			}
			t := m.targets[clamp(m.menuIndex, 0, len(m.targets)-1)]
			if t.ID == m.active {
				return nil
			}
			m.stopAll()
			m.active = t.ID
			m.compare = false
			m.chart = false
			m.detailOffset = 0
			m.focus = 0
			return tea.Batch(m.connect(), m.loadVisibility())
		case "metrics":
			keys := m.metricKeys()
			if len(keys) == 0 {
				return nil
			}
			m.metric = keys[clamp(m.menuIndex, 0, len(keys)-1)]
			m.chart = true
			return m.loadHistory()
		case "palette":
			a := m.actions()
			if len(a) > 0 {
				return m.perform(a[clamp(m.menuIndex, 0, len(a)-1)].ID)
			}
		}
	}
	return nil
}
func (m *model) startTargetForm(draft core.Target, editing bool) tea.Cmd {
	m.overlay = ""
	m.draftEdit = editing
	m.draft = draft
	configPath := m.opts.ConfigPath
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	m.targetForm = targetform.New(draft, configPath, editing)
	m.targetForm.Update(m.targetFormSize())
	return m.targetForm.Init()
}
func (m *model) targetFormSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: max(1, m.width-2), Height: max(1, m.geometry().Content.H-2)}
}
func (m *model) updateTargetForm(msg tea.Msg) tea.Cmd {
	// The shared form renders inside the dashboard frame at (1,2). Its hit
	// rectangles use coordinates local to that same inner content area.
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		msg = m.targetFormSize()
	case tea.MouseClickMsg:
		v.X--
		v.Y -= 2
		msg = v
	case tea.MouseReleaseMsg:
		v.X--
		v.Y -= 2
		msg = v
	case tea.MouseMotionMsg:
		v.X--
		v.Y -= 2
		msg = v
	case tea.MouseWheelMsg:
		v.X--
		v.Y -= 2
		msg = v
	}
	var cmd tea.Cmd
	m.targetForm, cmd = m.targetForm.Update(msg)
	if m.targetForm.Cancelled {
		m.targetForm = nil
		m.status = "Target configuration cancelled"
		return nil
	}
	if m.targetForm.Done {
		if !m.draftEdit {
			for _, t := range m.targets {
				if t.ID == m.targetForm.Target.ID {
					return m.targetForm.Reject(fmt.Errorf("target ID %q already exists", t.ID))
				}
			}
		}
		m.draft = m.targetForm.Target
		m.targetForm = nil
		return m.saveTarget()
	}
	return cmd
}
func (m *model) saveTarget() tea.Cmd {
	if m.opts.SaveTargets == nil {
		m.status = "Saving targets is unavailable; use lazymlflow targets add"
		m.overlay = ""
		return nil
	}
	targets := append([]core.Target(nil), m.targets...)
	found := false
	for i, t := range targets {
		if t.ID == m.draft.ID {
			targets[i] = m.draft
			found = true
		}
	}
	if !found {
		targets = append(targets, m.draft)
	}
	save := m.opts.SaveTargets
	selected := m.draft.ID
	defaultID := m.opts.InitialTarget
	if defaultID == "" {
		defaultID = selected
	}
	m.formPending = true
	m.status = "Saving target configuration…"
	return func() tea.Msg {
		for i, t := range targets {
			if t.ID == selected {
				normalized, err := config.NormalizeTarget(t, "")
				if err != nil {
					return savedMsg{err: err}
				}
				targets[i] = normalized
			}
		}
		err := save(targets, defaultID)
		return savedMsg{targets, selected, err}
	}
}
func (m *model) acceptSaved(v savedMsg) tea.Cmd {
	m.formPending = false
	if v.err != nil {
		m.status = "Could not save target: " + v.err.Error()
		return m.startTargetForm(m.draft, m.draftEdit)
	}
	m.overlay = ""
	var priorSession *core.Session
	if previous := m.states[v.selected]; previous != nil {
		priorSession = previous.Session
		if priorSession == nil {
			priorSession = previous.Retiring
		}
	}
	m.stopAll()
	m.targets = v.targets
	m.states[v.selected] = newTargetState()
	m.states[v.selected].Retiring = priorSession
	m.active = v.selected
	m.status = "Saved target " + v.selected
	return m.connect()
}
