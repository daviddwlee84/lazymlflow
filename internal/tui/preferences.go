package tui

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// stateWriter serializes effects and coalesces superseded snapshots. It also
// owns pending writes when the program exits before a scheduled effect runs.
type stateWriter struct {
	mu   sync.Mutex
	gate chan struct{}
	jobs map[string]stateJob
	seq  uint64
}
type stateJob struct {
	seq   uint64
	write func(context.Context) error
}

func newStateWriter() *stateWriter {
	return &stateWriter{gate: make(chan struct{}, 1), jobs: map[string]stateJob{}}
}
func (w *stateWriter) add(key string, write func(context.Context) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	w.jobs[key] = stateJob{w.seq, write}
}
func (w *stateWriter) flush(ctx context.Context) error {
	select {
	case w.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-w.gate }()
	for {
		w.mu.Lock()
		jobs := w.jobs
		w.jobs = map[string]stateJob{}
		w.mu.Unlock()
		if len(jobs) == 0 {
			return nil
		}
		var failed error
		for key, j := range jobs {
			if err := j.write(ctx); err != nil {
				failed = err
				w.mu.Lock()
				if latest, ok := w.jobs[key]; !ok || latest.seq < j.seq {
					w.jobs[key] = j
				}
				w.mu.Unlock()
			}
		}
		if failed != nil {
			return failed
		}
	}
}
func cloneView(v core.ExperimentView) core.ExperimentView {
	v.Columns = append([]core.ColumnSpec(nil), v.Columns...)
	v.Sort = append([]core.SortSpec(nil), v.Sort...)
	v.GroupBy = append([]string(nil), v.GroupBy...)
	expanded := map[string]bool{}
	for k, b := range v.Expanded {
		expanded[k] = b
	}
	v.Expanded = expanded
	return v
}

type layoutLoadedMsg struct {
	value    core.LayoutPreferences
	found    bool
	revision uint64
	err      error
}
type viewLoadedMsg struct {
	target, source, experiment string
	value                      core.ExperimentView
	found                      bool
	revision                   uint64
	err                        error
}
type visibilityLoadedMsg struct {
	target, source string
	values         map[string]core.Visibility
	err            error
}
type preferencesSavedMsg struct{ err error }

func (m *model) saveEffect() tea.Cmd {
	if m.opts.State == nil {
		m.status = "Preferences apply for this session; local state storage is unavailable"
		return nil
	}
	writer := m.writer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return preferencesSavedMsg{writer.flush(ctx)}
	}
}
func (m *model) saveView() tea.Cmd {
	r, s := m.runs(), m.state()
	if r == nil || s == nil {
		return nil
	}
	r.ViewRevision++
	r.View.Filter = r.Filter
	if m.opts.State == nil {
		return m.saveEffect()
	}
	v := cloneView(r.View)
	source, experiment, db := core.SourceKey(m.target()), s.Selected, m.opts.State
	m.writer.add("view/"+source+"/"+experiment, func(ctx context.Context) error { return db.SaveView(ctx, source, experiment, v) })
	return m.saveEffect()
}
func (m *model) saveLayout() tea.Cmd {
	m.layoutRevision++
	if m.opts.State == nil {
		return m.saveEffect()
	}
	value, db := m.layout, m.opts.State
	m.writer.add("layout", func(ctx context.Context) error { return db.SaveLayout(ctx, value) })
	return m.saveEffect()
}
func (m *model) loadLayout() tea.Cmd {
	if m.opts.State == nil {
		return nil
	}
	db, revision := m.opts.State, m.layoutRevision
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		v, ok, err := db.LoadLayout(ctx)
		return layoutLoadedMsg{v, ok, revision, err}
	}
}
func (m *model) loadView() tea.Cmd {
	r, s := m.runs(), m.state()
	if r == nil || s == nil || r.ViewRequested || m.opts.State == nil {
		return nil
	}
	r.ViewRequested = true
	db, t, source, e, revision := m.opts.State, m.active, core.SourceKey(m.target()), s.Selected, r.ViewRevision
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		v, ok, err := db.LoadView(ctx, source, e)
		return viewLoadedMsg{t, source, e, v, ok, revision, err}
	}
}
func (m *model) loadVisibility() tea.Cmd {
	s := m.state()
	if s == nil || s.VisibilityRequested || m.opts.State == nil {
		return nil
	}
	s.VisibilityRequested = true
	db, t, source := m.opts.State, m.active, core.SourceKey(m.target())
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		v, err := db.ListVisibility(ctx, source)
		return visibilityLoadedMsg{t, source, v, err}
	}
}
func (m *model) acceptView(v viewLoadedMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil {
		return nil
	}
	r := s.Runs[v.experiment]
	if r == nil || r.ViewRevision != v.revision {
		return nil
	}
	for _, t := range m.targets {
		if t.ID == v.target && core.SourceKey(t) != v.source {
			return nil
		}
	}
	if v.err != nil {
		m.status = "Could not load experiment preferences: " + v.err.Error()
		r.ViewRequested = false
		return nil
	}
	if !v.found {
		return nil
	}
	r.View = core.NormalizeView(cloneView(v.value))
	r.Filter = r.View.Filter
	if order, ok := core.ServerOrder(r.View.Sort); ok {
		r.Order = joinOrder(order)
	} else {
		r.Order = "attributes.start_time DESC"
	}
	if m.active == v.target && s.Selected == v.experiment {
		m.reselectRun()
		return m.loadRuns(false)
	}
	return nil
}
func (m *model) acceptVisibility(v visibilityLoadedMsg) tea.Cmd {
	s := m.states[v.target]
	if s == nil {
		return nil
	}
	for _, t := range m.targets {
		if t.ID == v.target && core.SourceKey(t) != v.source {
			return nil
		}
	}
	if v.err != nil {
		m.status = "Could not load local visibility: " + v.err.Error()
		s.VisibilityRequested = false
		return nil
	}
	for k, value := range v.values {
		if !s.VisibilityTouched[k] {
			s.Visibility[k] = value
		}
	}
	if m.active == v.target {
		before := s.Selected
		m.reselectExperiment()
		m.reselectRun()
		if before != s.Selected {
			return m.loadRuns(false)
		}
	}
	return nil
}
func visibilityMatches(want string, value core.Visibility) bool {
	if value == "" {
		value = core.VisibilityNormal
	}
	return want == "all" || want == string(value) || (want == "" && value == core.VisibilityNormal)
}
func (m *model) setVisibility(value core.Visibility) tea.Cmd {
	s := m.state()
	if s == nil {
		return nil
	}
	kind, id := "run", ""
	if m.focus == 0 {
		kind, id = "experiment", s.Selected
	} else if r := m.run(); r != nil {
		id = r.ID()
	}
	if id == "" {
		return nil
	}
	key := core.VisibilityKey(kind, id)
	s.Visibility[key] = value
	s.VisibilityTouched[key] = true
	var next tea.Cmd
	if kind == "experiment" {
		before := s.Selected
		m.reselectExperiment()
		if before != s.Selected {
			next = m.loadRuns(false)
		}
	} else {
		m.reselectRun()
	}
	m.status = fmt.Sprintf("Local %s %s: %s (MLflow data unchanged)", kind, id, value)
	if m.opts.State == nil {
		return tea.Batch(next, m.saveEffect())
	}
	source, db := core.SourceKey(m.target()), m.opts.State
	m.writer.add("visibility/"+source+"/"+key, func(ctx context.Context) error { return db.SetVisibility(ctx, source, kind, id, value) })
	return tea.Batch(next, m.saveEffect())
}
func (m *model) reselectExperiment() {
	s := m.state()
	if s == nil {
		return
	}
	index := s.Index
	for i, e := range m.experimentsVisible() {
		if e.ID == s.Selected {
			index = i
			break
		}
	}
	m.selectExperiment(index)
}
func (m *model) reselectRun() {
	r := m.runs()
	if r == nil {
		return
	}
	index := r.Index
	for i, row := range m.runRows() {
		if row.ID == r.Selected {
			index = i
			break
		}
	}
	m.selectRun(index)
}
func joinOrder(order []string) string {
	s := ""
	for i, v := range order {
		if i > 0 {
			s += ", "
		}
		s += v
	}
	return s
}
