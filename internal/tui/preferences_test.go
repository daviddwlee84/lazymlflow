package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type memoryState struct {
	mu         sync.Mutex
	views      map[string]core.ExperimentView
	layout     core.LayoutPreferences
	visibility map[string]core.Visibility
	saves      int
	fail       error
}

func (s *memoryState) LoadView(_ context.Context, source, id string) (core.ExperimentView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[source+"/"+id]
	return cloneView(v), ok, s.fail
}
func (s *memoryState) SaveView(_ context.Context, source, id string, v core.ExperimentView) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if s.views == nil {
		s.views = map[string]core.ExperimentView{}
	}
	s.views[source+"/"+id] = cloneView(v)
	s.saves++
	return nil
}
func (s *memoryState) ResetView(context.Context, string, string) error { return nil }
func (s *memoryState) LoadLayout(context.Context) (core.LayoutPreferences, bool, error) {
	return s.layout, true, s.fail
}
func (s *memoryState) SaveLayout(_ context.Context, v core.LayoutPreferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.layout = v
	s.saves++
	return nil
}
func (s *memoryState) ListVisibility(context.Context, string) (map[string]core.Visibility, error) {
	return s.visibility, s.fail
}
func (s *memoryState) SetVisibility(_ context.Context, source, kind, id string, v core.Visibility) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	if s.visibility == nil {
		s.visibility = map[string]core.Visibility{}
	}
	s.visibility[core.VisibilityKey(kind, id)] = v
	s.saves++
	return nil
}
func (s *memoryState) Close() error { return nil }
func TestPreferenceEffectsCoalesceAndOwnTheirSnapshots(t *testing.T) {
	m, _ := readyModel()
	db := &memoryState{}
	m.opts.State = db
	r := m.runs()
	r.View.Columns[0].Width = 40
	first := m.saveView()
	r.View.Columns[0].Width = 55
	second := m.saveView()
	r.View.Columns[0].Width = 99
	if db.saves != 0 {
		t.Fatal("state I/O happened in Update")
	}
	m.Update(first())
	m.Update(second())
	v := db.views[core.SourceKey(m.target())+"/e1"]
	if db.saves != 1 || v.Columns[0].Width != 55 {
		t.Fatalf("snapshot lost or stale write: %#v saves %d", v, db.saves)
	}
}
func TestLatePreferencesCannotOverwriteManualChanges(t *testing.T) {
	m, _ := readyModel()
	db := &memoryState{views: map[string]core.ExperimentView{}}
	m.opts.State = db
	source := core.SourceKey(m.target())
	old := core.DefaultView(nil, nil)
	old.Filter = "metrics.old > 1"
	old.Columns[0].Width = 88
	db.views[source+"/e1"] = old
	load := m.loadView()
	reply := load()
	m.runs().View.Columns[0].Width = 42
	m.saveView()
	m.Update(reply)
	if m.runs().View.Columns[0].Width != 42 || m.runs().Filter != "" {
		t.Fatal("late saved view overwrote user change")
	}
	m.layoutRevision = 1
	m.layout.LeftRatio = .4
	m.Update(layoutLoadedMsg{value: core.DefaultLayout(), found: true, revision: 0})
	if m.layout.LeftRatio != .4 {
		t.Fatal("late layout replaced resize")
	}
}
func TestPreferenceFailureVisibleAndRetriedOnFinalFlush(t *testing.T) {
	m, _ := readyModel()
	db := &memoryState{fail: errors.New("database locked")}
	m.opts.State = db
	save := m.saveView()
	m.Update(save())
	if !strings.Contains(m.status, "not saved") {
		t.Fatalf("failure hidden: %s", m.status)
	}
	db.fail = nil
	if err := m.writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.saves != 1 {
		t.Fatal("failed write not retained for flush")
	}
}
func TestLocalVisibilityDoesNotWriteBackend(t *testing.T) {
	m, b := readyModel()
	db := &memoryState{}
	m.opts.State = db
	m.focus = 1
	selected := m.run().ID()
	cmd := m.setVisibility(core.VisibilityHidden)
	if len(m.runsVisible()) != 2 || m.run().ID() == selected {
		t.Fatal("hide did not move to a visible row")
	}
	if b.calls != 0 {
		t.Fatal("hide mutated or queried MLflow")
	}
	m.Update(cmd())
	if db.visibility[core.VisibilityKey("run", selected)] != core.VisibilityHidden {
		t.Fatal("visibility not persisted")
	}
	m.runs().View.Visibility = "hidden"
	m.reselectRun()
	if m.run().ID() != selected {
		t.Fatal("hidden run not recoverable")
	}
	cmd = m.setVisibility(core.VisibilityNormal)
	m.Update(cmd())
	if len(m.runsVisible()) != 0 {
		t.Fatal("restore left run hidden")
	}
}
func TestLoadingVisibilityMergesLocallyChangedKeys(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.setVisibility(core.VisibilityHidden)
	source := core.SourceKey(m.target())
	m.Update(visibilityLoadedMsg{target: m.active, source: source, values: map[string]core.Visibility{"run/r1": core.VisibilityNormal, "run/r3": core.VisibilityArchived}})
	if m.state().Visibility["run/r1"] != core.VisibilityHidden || m.state().Visibility["run/r3"] != core.VisibilityArchived {
		t.Fatal("delayed visibility erased current action or other saved rows")
	}
}
