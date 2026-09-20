package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestLocalSortIsImmediateAndInvalidatesPriorQuery(t *testing.T) {
	m, b := readyModel()
	m.focus = 1
	rows := m.runs().Rows
	rows[0].Data.Params = []core.KeyValue{{Key: "batch size", Value: "100"}}
	rows[1].Data.Params = []core.KeyValue{{Key: "batch size", Value: "2"}}
	rows[2].Data.Params = []core.KeyValue{{Key: "batch size", Value: "30"}}
	m.loadRuns(false)
	old := m.runs().Gen
	m.sortColumn(core.ColumnSpec{Kind: "param", Key: "batch size", Numeric: true}, false)
	visible := m.runRows()
	if visible[0].Run.ID() != "r2" || visible[2].Run.ID() != "r1" {
		t.Fatal("numeric local sort not immediate")
	}
	if b.calls != 0 {
		t.Fatal("sort did blocking I/O")
	}
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: old, page: core.RunPage{Runs: []core.Run{sampleRun("late")}}})
	if len(m.runs().Rows) != 3 {
		t.Fatal("obsolete query accepted after local sort")
	}
	if !strings.Contains(strings.Join(m.runLines(100, 20), "\n"), "local sort") {
		t.Fatal("local-only scope not visible")
	}
}
func TestServerSortRetainsRowsAndRejectsOldResponse(t *testing.T) {
	m, _ := readyModel()
	m.loadRuns(false)
	old := m.runs().Gen
	cmd := m.sortColumn(core.ColumnSpec{Kind: "metric", Key: "loss"}, false)
	if cmd == nil || !m.runs().Pending || len(m.runs().Rows) != 3 {
		t.Fatal("server sort cleared available rows")
	}
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: old, page: core.RunPage{Runs: []core.Run{sampleRun("late")}}})
	if len(m.runs().Rows) != 3 {
		t.Fatal("late sort result accepted")
	}
}

type pagingBackend struct {
	fakeBackend
	pages int
}

func (b *pagingBackend) SearchRuns(_ context.Context, q core.RunQuery) (core.RunPage, error) {
	b.pages++
	if q.PageToken == "page2" {
		return core.RunPage{Runs: []core.Run{sampleRun("r4")}, NextPageToken: "page3"}, nil
	}
	if q.PageToken == "page3" {
		return core.RunPage{Runs: []core.Run{sampleRun("r5")}}, nil
	}
	return core.RunPage{}, nil
}
func TestLoadAllProgressPreservesSelectionAndCancels(t *testing.T) {
	m, _ := readyModel()
	b := &pagingBackend{}
	m.state().Session.Backend = b
	m.focus = 1
	m.runs().Next = "page2"
	m.selectRun(1)
	cmd := m.perform("loadall")
	_, next := m.Update(cmd())
	if len(m.runs().Rows) != 4 || m.run().ID() != "r2" || !m.runs().LoadingAll {
		t.Fatal("load-all progress lost rows or selection")
	}
	m.Update(next())
	if len(m.runs().Rows) != 5 || m.runs().LoadingAll || b.pages != 2 {
		t.Fatal("load-all did not finish every page")
	}
	m.runs().Next = "page2"
	cmd = m.perform("loadall")
	reply := cmd()
	m.perform("cancel-all")
	m.Update(reply)
	if len(m.runs().Rows) != 5 || m.runs().Pending || m.runs().LoadingAll {
		t.Fatal("cancelled load-all accepted late page")
	}
}
func TestNestedAndGroupedRowsKeepEntityActionsSafe(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	parent, child, grandchild := sampleRun("parent"), sampleRun("child"), sampleRun("grandchild")
	child.Data.Tags = []core.KeyValue{{Key: "mlflow.parentRunId", Value: "parent"}}
	grandchild.Data.Tags = []core.KeyValue{{Key: "mlflow.parentRunId", Value: "child"}}
	m.runs().Rows = []core.Run{grandchild, child, parent}
	m.selectRun(0)
	rows := m.runRows()
	if len(rows) != 2 || rows[0].ID != "parent" || rows[1].ID != "child" {
		t.Fatalf("first-level nested expansion: %#v", rows)
	}
	m.selectRun(1)
	m.perform("right")
	if len(m.runRows()) != 3 {
		t.Fatal("nested child did not expand")
	}
	m.perform("left")
	if len(m.runRows()) != 2 {
		t.Fatal("nested child did not collapse")
	}
	m.selectRun(0)
	m.setVisibility(core.VisibilityHidden)
	rows = m.runRows()
	if len(rows) < 2 || rows[0].Kind != "context" || rows[1].ID != "child" {
		t.Fatal("hiding parent lost children")
	}
	m.selectRun(0)
	for _, action := range m.actions() {
		if action.ID == "copy" || action.ID == "open" || action.ID == "hide" {
			t.Fatalf("context row exposes entity action %s", action.ID)
		}
	}
	m.runs().View.Mode = "grouped"
	m.runs().View.GroupBy = []string{"lr"}
	m.reselectRun()
	m.selectRun(0)
	if m.run() != nil {
		t.Fatal("group header became a run")
	}
	before := len(m.state().Basket)
	m.perform("basket")
	if len(m.state().Basket) != before {
		t.Fatal("group header entered comparison basket")
	}
	m.perform("enter")
	if len(m.runRows()) != 1 {
		t.Fatal("group collapse failed")
	}
}
func TestMouseWheelUsesHoveredPaneWithoutChangingFocus(t *testing.T) {
	m, _ := readyModel()
	m.width = 140
	m.height = 35
	m.focus = 0
	pane := m.geometry().Panes[1]
	m.Update(tea.MouseWheelMsg{X: pane.X + 5, Y: pane.Y + 4, Button: tea.MouseWheelDown})
	if m.focus != 0 || m.run().ID() != "r3" {
		t.Fatal("wheel did not scroll hovered run pane independently")
	}
}

func TestCommaInColumnKeyPreservesServerOrderField(t *testing.T) {
	m, b := readyModel()
	m.focus = 1
	cmd := m.sortColumn(core.ColumnSpec{Kind: "param", Key: "data, version"}, false)
	cmd()
	if len(b.runQuery.OrderBy) != 2 || b.runQuery.OrderBy[0] != "params.`data, version` ASC" {
		t.Fatalf("column key was split: %#v", b.runQuery.OrderBy)
	}
}
func TestRunNameIsPinnedWhenPanningAndCannotBeUnchecked(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.columnPan = 2
	if text := strings.Join(m.runLines(120, 20), "\n"); !strings.Contains(text, "Run name") || !strings.Contains(text, "r1") {
		t.Fatal("horizontal pan lost row identity")
	}
	m.openPicker("columns")
	m.pickerSearch.SetValue("Run name")
	m.pickerTyping = false
	m.choosePicker("space")
	if len(m.runs().View.Columns) == 0 || m.runs().View.Columns[0].Kind != "attribute" || m.runs().View.Columns[0].Key != "name" {
		t.Fatal("pinned name was removed")
	}
}

func TestReplacementQueryInvalidatesCursorAndPagingCannotSupersedeIt(t *testing.T) {
	m, b := readyModel()
	r := m.runs()
	r.Next = "old-query-page"
	r.SeenPageTokens = map[string]bool{"old-query-page": true}
	r.Filter = "metrics.loss < 0.2"
	first := m.loadRuns(false)
	gen := r.Gen
	if r.Next != "" || len(r.SeenPageTokens) != 0 || !r.FirstPagePending || len(r.Rows) != 3 {
		t.Fatal("replacement query did not reset pagination while retaining preview")
	}
	if cmd := m.loadRuns(true); cmd != nil {
		t.Fatal("next page started during replacement query")
	}
	if cmd := m.perform("loadall"); cmd != nil {
		t.Fatal("load-all started during replacement query")
	}
	if r.Gen != gen || r.LoadingAll || !r.Pending {
		t.Fatal("paging superseded the current first page")
	}
	if text := strings.Join(m.runLines(150, 20), "\n"); !strings.Contains(text, "updating") || !strings.Contains(text, "preview") {
		t.Fatal("pending server order presented as final")
	}
	reply := first()
	if b.runQuery.PageToken != "" || b.runQuery.Filter != r.Filter {
		t.Fatal("old query cursor leaked into new query")
	}
	m.Update(reply)
	if r.Pending || r.FirstPagePending || len(r.Rows) != 0 {
		t.Fatal("new first page was cancelled or not accepted")
	}
}
func TestRepeatedAndCyclicRunPageTokensStopWithRowsRetained(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		m, _ := readyModel()
		m.focus = 1
		m.loadRuns(false)
		r := m.runs()
		m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: r.Gen, page: core.RunPage{Runs: []core.Run{sampleRun("r1")}, NextPageToken: "A"}})
		m.perform("loadall")
		next := "A"
		if cycle {
			next = "B"
		}
		cmd := m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: r.Gen, append: true, page: core.RunPage{Runs: []core.Run{sampleRun("r2")}, NextPageToken: next}})
		if cycle {
			if cmd == nil || !r.LoadingAll {
				t.Fatal("valid second page stopped prematurely")
			}
			cmd = m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: r.Gen, append: true, page: core.RunPage{Runs: []core.Run{sampleRun("r3")}, NextPageToken: "A"}})
		}
		if cmd != nil || r.Pending || r.LoadingAll || r.Next != "" || !strings.Contains(r.Err, "repeated a page token") || len(r.Rows) < 2 {
			t.Fatalf("cycle=%t failed to stop safely: %#v", cycle, r)
		}
		if m.perform("loadall") != nil || m.loadRuns(true) != nil {
			t.Fatal("invalid pagination cursor remained actionable")
		}
		m.loadRuns(false)
		if r.Err != "" || len(r.SeenPageTokens) != 0 {
			t.Fatal("explicit refresh did not reset failed pagination")
		}
		m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: r.Gen, page: core.RunPage{Runs: []core.Run{sampleRun("fresh")}, NextPageToken: "A"}})
		if r.Err != "" || r.Next != "A" {
			t.Fatal("fresh query inherited old token history")
		}
	}
}
func TestLatePageDoesNotRepopulateReplacementQueryCursor(t *testing.T) {
	m, _ := readyModel()
	r := m.runs()
	r.Next = "old-next"
	m.loadRuns(true)
	old := r.Gen
	m.loadRuns(false)
	latest := r.Gen
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: old, append: true, page: core.RunPage{Runs: []core.Run{sampleRun("stale")}, NextPageToken: "old-later"}})
	if r.Gen != latest || r.Next != "" || len(r.SeenPageTokens) != 0 || !r.Pending {
		t.Fatal("late page contaminated replacement pagination")
	}
}
