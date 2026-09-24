package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// The cache is prepared by Update/selection, never mutated while rendering.
// A fingerprint fallback keeps direct model users correct before the next event.
func (m *model) rowCacheKey() string {
	r := m.runs()
	if r == nil {
		return ""
	}
	semanticView := r.View
	semanticView.Columns = nil
	semanticView.Filter = ""
	semanticView.Sort = append([]core.SortSpec(nil), r.View.Sort...)
	for i := range semanticView.Sort {
		semanticView.Sort[i].Column.Width = 0
		semanticView.Sort[i].Column.Label = ""
	}
	view, _ := json.Marshal(semanticView)
	visibility, _ := json.Marshal(m.state().Visibility)
	return fmt.Sprintf("%p/%d/%d/%s/%s/%s/%s", r.Rows, len(r.Rows), r.RowsVersion, r.Local, view, visibility, m.catalogInspectionKey())
}
func (m *model) buildRunRows() []core.RunRow {
	r := m.runs()
	if r == nil {
		return nil
	}
	if m.activityScope() != scopeExperiment {
		return m.activityPresentationRows(r)
	}
	var rows []core.Run
	q := strings.ToLower(r.Local)
	if q == "" {
		rows = r.Rows
	} else {
		for _, run := range r.Rows {
			if strings.Contains(strings.ToLower(run.Name()+" "+run.ID()+" "+run.Info.Status+" "+m.activityExperimentName(run.Info.ExperimentID)), q) {
				rows = append(rows, run)
			}
		}
	}
	return m.catalogInspectionRows(core.BuildRunRows(rows, r.View, m.state().Visibility))
}

func (m *model) activityPresentationRows(r *runState) []core.RunRow {
	query := strings.ToLower(r.Local)
	var rows []core.Run
	for _, run := range r.Rows {
		retained := m.activityScope() == scopeRunning && m.activityCurrent() != nil && m.activityCurrent().RetainedRun == run.ID()
		if core.Visible(m.state().Visibility[core.VisibilityKey("run", run.ID())], r.View.Visibility) && (retained || strings.Contains(strings.ToLower(run.Name()+" "+run.ID()+" "+run.Info.Status+" "+m.activityExperimentName(run.Info.ExperimentID)), query)) {
			rows = append(rows, run)
		}
	}
	if len(r.View.Sort) > 0 {
		rows = core.SortRuns(rows, r.View.Sort)
	}
	out := make([]core.RunRow, 0, len(rows))
	for i := range rows {
		run := &rows[i]
		note := ""
		if m.activityScope() == scopeRunning && m.activityCurrent() != nil && m.activityCurrent().RetainedRun == run.ID() {
			note = "ended · kept while reading"
		}
		if m.activityScope() == scopePinned {
			if pins := m.currentRunPins(); pins != nil && pins.RunErrors[run.ID()] != "" {
				note = "unavailable · cached"
			} else if run.Info.LifecycleStage == "deleted" {
				note = "deleted remotely"
			}
		}
		out = append(out, core.RunRow{ID: run.ID(), Kind: "run", Run: run, Label: run.Name(), Note: note})
	}
	return out
}
func (m *model) refreshRowCache() {
	r := m.runs()
	if r == nil {
		return
	}
	key := m.rowCacheKey()
	if r.presentationKey != key {
		r.presentation = m.buildRunRows()
		r.presentationKey = key
	}
}
func (m *model) runRows() []core.RunRow {
	r := m.runs()
	if r == nil {
		return nil
	}
	if r.presentationKey == m.rowCacheKey() {
		return r.presentation
	}
	return m.buildRunRows()
}
func (m *model) selectedRunRow() *core.RunRow {
	r := m.runs()
	if r == nil {
		return nil
	}
	for _, row := range m.runRows() {
		if row.ID == r.Selected {
			return &row
		}
	}
	return nil
}
func (m *model) toggleRow(row core.RunRow) tea.Cmd {
	r := m.runs()
	if r == nil || !row.Expandable {
		return nil
	}
	if r.View.Expanded == nil {
		r.View.Expanded = map[string]bool{}
	}
	r.View.Expanded[row.ID] = !row.Expanded
	m.reselectRun()
	return m.saveView()
}
func (m *model) treeNavigation(d int) bool {
	r := m.runs()
	row := m.selectedRunRow()
	if r == nil || row == nil {
		return false
	}
	mode := r.View.Mode
	if mode == "flat" {
		return false
	}
	if d > 0 && row.Expandable && !row.Expanded {
		if r.View.Expanded == nil {
			r.View.Expanded = map[string]bool{}
		}
		r.View.Expanded[row.ID] = true
		return true
	}
	if d < 0 {
		if row.Expandable && row.Expanded {
			if r.View.Expanded == nil {
				r.View.Expanded = map[string]bool{}
			}
			r.View.Expanded[row.ID] = false
			return true
		}
		if row.ParentID != "" {
			for i, p := range m.runRows() {
				if p.ID == row.ParentID || (p.Kind == "context" && p.ParentID == row.ParentID) {
					m.selectRun(i)
					return true
				}
			}
		}
	}
	return mode == "tree" || mode == "grouped" || row.Expandable || row.Depth > 0
}
func (m *model) experimentContent(w, h int) paneContent {
	p := paneContent{}
	s := m.state()
	if s == nil {
		p.Lines = []string{"No tracking targets.", "Press t, then a to add one."}
		return p
	}
	if s.ConnectPending && len(s.Experiments) == 0 {
		m.activitySidebar(&p, w)
		p.Lines = append(p.Lines, "Connecting in background…", "Navigation remains available.", "Esc cancels; t switches target.")
		return p
	}
	m.activitySidebar(&p, w)
	p.add(fmt.Sprintf("%d loaded%s · %s", len(s.Experiments), pending(s.Pending), m.layout.ExperimentVisibility))
	if s.Local != "" {
		p.add("Local: " + clean(s.Local))
	}
	if s.Filter != "" {
		p.add("Server: " + clean(s.Filter))
	}
	if s.Err != "" {
		p.add("Refresh failed; previous rows kept")
		p.add(clean(s.Err))
	}
	rows := m.experimentsVisible()
	if len(rows) == 0 {
		if !s.Pending {
			p.add("No matching experiments.")
			p.add("/ search · V visibility")
		}
		return p
	}
	capacity := max(1, h-len(p.Lines)-1)
	start := listStart(s.Index, len(rows), capacity)
	for i := start; i < min(len(rows), start+capacity); i++ {
		e := rows[i]
		label := e.Name + " [" + e.ID + "]"
		if v := s.Visibility[core.VisibilityKey("experiment", e.ID)]; v != "" && v != core.VisibilityNormal {
			label += " (" + string(v) + ")"
		}
		badge := m.experimentActivityBadge(e.ID)
		if textWidth(badge) > w/2 {
			badge = ""
		}
		label = middleName(label, max(1, w-2-textWidth(badge))) + badge
		p.addHit(row(label, e.ID == s.Selected && m.activityScope() == scopeExperiment, w), "experiment:"+e.ID, w)
	}
	if s.Next != "" {
		p.add("n: next page (server ordered)")
	}
	return p
}
func (m *model) runContent(w, h int) paneContent {
	p := paneContent{}
	r := m.runs()
	if r == nil || m.selectedExperiment() == "" && m.activityScope() == scopeExperiment {
		p.add("Choose an experiment.")
		return p
	}
	x := 0
	controlLine := ""
	for _, control := range []struct{ ID, Label string }{{"columns", "Columns"}, {"sort", "Sort"}, {"group", "Group by"}, {"layout", "Layout"}} {
		if control.ID == "group" && m.activityScope() != scopeExperiment {
			continue
		}
		label := "[" + control.Label + "] "
		clippedHit(&p, x, 0, textWidth(label), w, "action:"+control.ID)
		controlLine += label
		x += textWidth(label)
	}
	p.add(controlLine)
	scope := "server ordered"
	if r.Pending {
		scope = "updating server order · loaded preview"
	}
	if _, ok := core.ServerOrder(r.View.Sort); !ok {
		scope = "local sort · loaded rows"
	}
	if r.LoadingAll {
		scope = "loading all · Ctrl+X cancel"
	}
	if m.activityScope() != scopeExperiment {
		if m.activityScope() == scopePinned {
			p.add(m.pinnedDescription())
			if pins := m.currentRunPins(); pins != nil {
				if pins.Err != "" {
					p.add("Pins refresh failed; cached rows retained: " + clean(pins.Err))
				}
				if err := pins.RunErrors[r.Selected]; err != "" {
					p.add("Selected pin unavailable · r retry · * unpin: " + clean(err))
				} else if run := m.run(); run != nil && run.Info.LifecycleStage == "deleted" {
					p.add("Selected pin deleted remotely · * unpin · i full name")
				}
			}
		} else {
			p.add(m.activityDescription())
		}
		if a := m.activityCurrent(); a != nil && a.Scope == scopeRunning && a.RetainedRun != "" {
			p.add("Selected run ended · kept while reading · move away or r in list to dismiss")
		}
		if a := m.activityCurrent(); a != nil && a.Err != "" {
			p.add("Refresh failed; cached rows retained")
			p.add(clean(a.Err))
		}
	} else {
		p.add(fmt.Sprintf("%d loaded%s · %s · %s", len(r.Rows), pending(r.Pending), scope, r.View.Visibility))
	}
	if r.Local != "" {
		p.add("Local: " + clean(r.Local))
	}
	if r.Filter != "" {
		p.add("Server: " + clean(r.Filter))
	}
	if r.Err != "" {
		p.add("Refresh failed; previous rows kept")
		p.add(clean(r.Err))
	}
	cols := r.View.Columns
	indices := []int{}
	if len(cols) > 0 {
		indices = append(indices, 0)
		first := clamp(m.columnPan+1, 1, len(cols))
		for i := first; i < len(cols); i++ {
			indices = append(indices, i)
		}
	}
	head := fit("", 4)
	x = 4
	headerY := len(p.Lines)
	for _, i := range indices {
		c := cols[i]
		label := core.ColumnLabel(c)
		for _, s := range r.View.Sort {
			if columnID(c) == columnID(s.Column) {
				if s.Desc {
					label += " ↓"
				} else {
					label += " ↑"
				}
				break
			}
		}
		cw := columnWidth(c)
		head += textFit(label, max(1, cw-1)) + " "
		clippedHit(&p, x, headerY, cw-1, w, "sort:"+columnID(c))
		clippedHit(&p, x+cw-1, headerY, 1, w, "width:"+columnID(c))
		x += cw
	}
	p.add(dimStyle.Render(head))
	rows := m.runRows()
	if len(rows) == 0 {
		if !r.Pending {
			if m.activityScope() == scopePinned {
				p.add("No matching pins. * pins a run in any experiment · / search · V visibility")
			} else {
				p.add("No matching runs. / search · f filter · V visibility")
			}
		}
		return p
	}
	capacity := max(1, h-len(p.Lines)-1)
	start := listStart(r.Index, len(rows), capacity)
	for i := start; i < min(len(rows), start+capacity); i++ {
		item := rows[i]
		label := ""
		toggle := "  "
		if item.Expandable {
			toggle = "▸ "
			if item.Expanded {
				toggle = "▾ "
			}
		}
		if item.Run == nil {
			label = strings.Repeat("  ", min(item.Depth, 10)) + toggle + item.Label
			if item.Kind == "group" {
				label += fmt.Sprintf(" (%d loaded)", item.Count)
			}
			if item.Note != "" {
				label += " · " + item.Note
			}
		} else {
			run := *item.Run
			mark := "  "
			if _, ok := m.state().Basket[run.ID()]; ok {
				mark = "* "
			}
			label = mark
			for _, ci := range indices {
				c := cols[ci]
				value := core.DisplayValue(run, c)
				if m.activityScope() != scopeExperiment && c.Kind == "attribute" && c.Key == "experiment_id" {
					value = m.activityExperimentName(run.Info.ExperimentID)
				}
				if c.Kind == "attribute" && c.Key == "name" {
					value = strings.Repeat("  ", min(item.Depth, 10)) + toggle + value
					if m.runPinned(run.ID()) {
						value = "★ " + value
					}
					if activityMark := m.activityRunMark(run.ID()); activityMark != "" {
						value = activityMark + " " + value
					}
					if item.Note != "" {
						value += " · " + item.Note
					}
				}
				label += textFit(value, max(1, columnWidth(c)-1)) + " "
			}
		}
		y := len(p.Lines)
		p.addHit(row(label, item.ID == r.Selected, w), "run:"+item.ID, w)
		if item.Expandable {
			toggleX := 2 + min(item.Depth, 10)*2
			if item.Run != nil {
				toggleX += 2
			}
			p.Hits = append([]hit{{rect{toggleX, y, wMin(w-toggleX, 2), 1}, "expand:" + item.ID}}, p.Hits...)
		}
	}
	if r.Next != "" {
		p.add("n: next page · A: load all matched rows")
	}
	return p
}
func wMin(a, b int) int {
	if a < 0 {
		return 0
	}
	return min(a, b)
}

type parentLoadedMsg struct {
	target, child string
	gen           uint64
	run           core.Run
	err           error
}

func (m *model) loadParent() tea.Cmd {
	s, row := m.state(), m.selectedRunRow()
	if s == nil || s.Session == nil || row == nil {
		return nil
	}
	id := row.ParentID
	child := row.ID
	if row.Run != nil {
		id = row.Run.ParentID()
	}
	if id == "" {
		m.status = "This row has no parent run"
		return nil
	}
	m.parentInfo = nil
	m.overlay = "parent-info"
	m.menuIndex = 0
	m.parentChild = child
	ctx, gen := m.operation("parent")
	m.parentGen = gen
	b, t := s.Session.Backend, m.active
	return func() tea.Msg { r, err := b.GetRun(ctx, id); return parentLoadedMsg{t, child, gen, r, err} }
}
func (m *model) acceptParent(v parentLoadedMsg) tea.Cmd {
	if v.target != m.active || m.overlay != "parent-info" || m.parentChild != v.child || m.parentGen != v.gen {
		return nil
	}
	if v.err != nil {
		m.overlay = ""
		m.status = "Could not load parent: " + v.err.Error()
		return nil
	}
	m.parentInfo = &v.run
	return nil
}
