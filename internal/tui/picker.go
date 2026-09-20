package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type pickerItem struct {
	ID, Label string
	Column    core.ColumnSpec
}

func columnID(c core.ColumnSpec) string { return c.Kind + "/" + c.Key + "/" + c.DatasetContext }
func (m *model) isPicker() bool {
	switch m.overlay {
	case "columns", "sort", "group", "layout", "visibility", "info", "parent-info":
		return true
	}
	return false
}
func (m *model) openPicker(kind string) tea.Cmd {
	m.overlay = kind
	m.menuIndex = 0
	m.mousePressed = ""
	m.prefix = false
	m.pickerSearch.SetValue("")
	m.pickerSearch.SetWidth(max(1, m.width-14))
	m.pickerTyping = kind == "columns" || kind == "sort" || kind == "group"
	if m.pickerTyping {
		return m.pickerSearch.Focus()
	}
	m.pickerSearch.Blur()
	return nil
}
func (m *model) viewColumns() []core.ColumnSpec {
	if r := m.runs(); r != nil {
		return r.View.Columns
	}
	return nil
}
func (m *model) pickerItems() []pickerItem {
	var items []pickerItem
	r := m.runs()
	switch m.overlay {
	case "columns", "sort":
		if r == nil {
			return nil
		}
		for _, c := range core.DiscoverColumns(r.Rows, r.View.Columns) {
			mark := "[ ] "
			extra := ""
			if m.overlay == "columns" {
				for i, selected := range r.View.Columns {
					if columnID(c) == columnID(selected) {
						mark = "[x] "
						c = selected
						extra = fmt.Sprintf(" · #%d · width %d", i+1, columnWidth(c))
						if c.Kind == "attribute" && c.Key == "name" {
							extra += " · pinned"
						}
						if c.Numeric {
							extra += " · numeric"
						}
						break
					}
				}
			} else {
				for i, s := range r.View.Sort {
					if columnID(c) == columnID(s.Column) {
						mark = fmt.Sprintf("[%d] ", i+1)
						c = s.Column
						extra = " ASC"
						if s.Desc {
							extra = " DESC"
						}
						break
					}
				}
			}
			category := map[string]string{"attribute": "Attributes", "dataset": "Datasets", "param": "Parameters", "metric": "Metrics", "tag": "Tags"}[c.Kind]
			items = append(items, pickerItem{columnID(c), mark + category + " / " + core.ColumnLabel(c) + extra, c})
		}
	case "group":
		if r == nil {
			return nil
		}
		for _, mode := range []string{"auto", "flat", "tree", "grouped"} {
			mark := "[ ] "
			if r.View.Mode == mode {
				mark = "[x] "
			}
			items = append(items, pickerItem{ID: "mode/" + mode, Label: mark + "Layout: " + mode})
		}
		for _, mode := range []string{"collapsed", "first", "all"} {
			mark := "[ ] "
			if r.View.Expansion == mode {
				mark = "[x] "
			}
			items = append(items, pickerItem{ID: "expansion/" + mode, Label: mark + "Expansion: " + mode})
		}
		keys := map[string]bool{}
		for _, run := range r.Rows {
			for _, p := range run.Data.Params {
				keys[p.Key] = true
			}
		}
		for _, k := range r.View.GroupBy {
			keys[k] = true
		}
		var names []string
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			mark := "[ ] "
			for _, g := range r.View.GroupBy {
				if g == k {
					mark = "[x] "
				}
			}
			items = append(items, pickerItem{ID: "param/" + k, Label: mark + "Parameter / " + k})
		}
	case "layout":
		items = []pickerItem{{ID: "resize", Label: "Resize panes with Ctrl+W, then h/l/k/j"}, {ID: "reset", Label: "Reset proportions to 30% left / 55% top"}, {ID: "zoom", Label: "Zoom / restore focused pane (z)"}, {ID: "mouse", Label: fmt.Sprintf("Mouse capture: %t (M to toggle)", m.layout.Mouse)}}
	case "visibility":
		want := m.layout.ExperimentVisibility
		if m.focus != 0 && r != nil {
			want = r.View.Visibility
		}
		for _, v := range []string{"normal", "hidden", "archived", "all"} {
			mark := "[ ] "
			if want == v {
				mark = "[x] "
			}
			items = append(items, pickerItem{ID: v, Label: mark + v + " (local preferences only)"})
		}
	}
	q := strings.ToLower(m.pickerSearch.Value())
	if q != "" {
		var filtered []pickerItem
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Label), q) {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}
	return items
}
func (m *model) handlePicker(msg tea.Msg, key string) tea.Cmd {
	if key == "esc" {
		m.overlay = ""
		m.pickerTyping = false
		m.pickerSearch.Blur()
		m.mousePressed = ""
		return nil
	}
	if m.overlay == "info" || m.overlay == "parent-info" {
		switch key {
		case "q", "enter":
			m.overlay = ""
		case "up", "k":
			m.menuIndex = max(0, m.menuIndex-1)
		case "down", "j":
			m.menuIndex++
		}
		return nil
	}
	if key == "tab" {
		m.pickerTyping = !m.pickerTyping
		if m.pickerTyping {
			return m.pickerSearch.Focus()
		}
		m.pickerSearch.Blur()
		return nil
	}
	if key == "up" || key == "down" {
		d := 1
		if key == "up" {
			d = -1
		}
		m.menuIndex = clamp(m.menuIndex+d, 0, len(m.pickerItems())-1)
		return nil
	}
	if m.pickerTyping {
		if key == "enter" {
			m.pickerTyping = false
			m.pickerSearch.Blur()
			return nil
		}
		var cmd tea.Cmd
		m.pickerSearch, cmd = m.pickerSearch.Update(msg)
		m.menuIndex = 0
		return cmd
	}
	switch key {
	case "/":
		m.pickerTyping = true
		return m.pickerSearch.Focus()
	case "q":
		m.overlay = ""
		return nil
	case "j":
		m.menuIndex = clamp(m.menuIndex+1, 0, len(m.pickerItems())-1)
	case "k":
		m.menuIndex = max(0, m.menuIndex-1)
	case "home":
		m.menuIndex = 0
	case "end", "G":
		m.menuIndex = max(0, len(m.pickerItems())-1)
	case "space", "enter", "<", ">", "[", "]", "n", "0", "backspace":
		return m.choosePicker(key)
	}
	return nil
}
func (m *model) choosePicker(key string) tea.Cmd {
	r := m.runs()
	items := m.pickerItems()
	if len(items) == 0 {
		return nil
	}
	item := items[clamp(m.menuIndex, 0, len(items)-1)]
	switch m.overlay {
	case "columns":
		if r == nil {
			return nil
		}
		index := -1
		for i, c := range r.View.Columns {
			if columnID(c) == item.ID {
				index = i
				break
			}
		}
		switch key {
		case "space", "enter":
			if item.Column.Kind == "attribute" && item.Column.Key == "name" {
				m.status = "Run name stays pinned so each row remains identifiable"
				return nil
			}
			if index >= 0 {
				if len(r.View.Columns) == 1 {
					m.status = "Keep at least one visible column"
					return nil
				}
				r.View.Columns = append(r.View.Columns[:index], r.View.Columns[index+1:]...)
			} else {
				c := item.Column
				c.Width = columnWidth(c)
				r.View.Columns = append(r.View.Columns, c)
			}
		case "0":
			r.View.Columns = core.DefaultView(m.metricColumns, m.paramColumns).Columns
		case "<", ">":
			if index >= 0 {
				other := index - 1
				if key == ">" {
					other = index + 1
				}
				if other > 0 && index > 0 && other < len(r.View.Columns) {
					r.View.Columns[index], r.View.Columns[other] = r.View.Columns[other], r.View.Columns[index]
				}
			}
		case "[", "]":
			if index >= 0 {
				d := -2
				if key == "]" {
					d = 2
				}
				r.View.Columns[index].Width = clamp(columnWidth(r.View.Columns[index])+d, 6, 100)
			}
		case "n":
			if index >= 0 {
				r.View.Columns[index].Numeric = !r.View.Columns[index].Numeric
				for i, s := range r.View.Sort {
					if columnID(s.Column) == item.ID {
						r.View.Sort[i].Column.Numeric = r.View.Columns[index].Numeric
					}
				}
				return m.applySort()
			}
		default:
			return nil
		}
		m.columnPan = clamp(m.columnPan, 0, len(r.View.Columns)-2)
		return m.saveView()
	case "sort":
		if r == nil {
			return nil
		}
		if key == "0" {
			r.View.Sort = core.DefaultView(nil, nil).Sort
			return m.applySort()
		}
		if key == "backspace" {
			for i, s := range r.View.Sort {
				if columnID(s.Column) == item.ID {
					r.View.Sort = append(r.View.Sort[:i], r.View.Sort[i+1:]...)
					break
				}
			}
			return m.applySort()
		}
		if key == "n" {
			c := item.Column
			c.Numeric = !c.Numeric
			return m.sortColumn(c, false)
		}
		if key == "space" || key == "enter" {
			return m.sortColumn(item.Column, key == "space")
		}
	case "group":
		if r == nil {
			return nil
		}
		kind, value, _ := strings.Cut(item.ID, "/")
		switch kind {
		case "mode":
			r.View.Mode = value
		case "expansion":
			r.View.Expansion = value
			r.View.Expanded = map[string]bool{}
		case "param":
			index := -1
			for i, k := range r.View.GroupBy {
				if k == value {
					index = i
					break
				}
			}
			if index < 0 {
				r.View.GroupBy = append(r.View.GroupBy, value)
			} else {
				r.View.GroupBy = append(r.View.GroupBy[:index], r.View.GroupBy[index+1:]...)
			}
			r.View.Mode = "grouped"
		}
		m.reselectRun()
		return m.saveView()
	case "layout":
		m.overlay = ""
		switch item.ID {
		case "reset":
			m.layout.LeftRatio = .30
			m.layout.TopRatio = .55
			return m.saveLayout()
		default:
			return m.perform(item.ID)
		}
	case "visibility":
		m.overlay = ""
		if m.focus == 0 {
			m.layout.ExperimentVisibility = item.ID
			before := m.selectedExperiment()
			m.reselectExperiment()
			cmd := m.saveLayout()
			if before != m.selectedExperiment() {
				return tea.Batch(cmd, m.loadRuns(false))
			}
			return cmd
		}
		if r != nil {
			r.View.Visibility = item.ID
			m.reselectRun()
			return m.saveView()
		}
	}
	return nil
}
func (m *model) sortColumn(c core.ColumnSpec, secondary bool) tea.Cmd {
	r := m.runs()
	if r == nil {
		return nil
	}
	desc := false
	found := -1
	for i, s := range r.View.Sort {
		if columnID(c) == columnID(s.Column) {
			desc = !s.Desc
			found = i
			break
		}
	}
	next := core.SortSpec{Column: c, Desc: desc}
	if secondary {
		if found >= 0 {
			r.View.Sort[found] = next
		} else {
			r.View.Sort = append(r.View.Sort, next)
		}
	} else {
		r.View.Sort = []core.SortSpec{next}
	}
	return m.applySort()
}
func (m *model) applySort() tea.Cmd {
	r := m.runs()
	if r == nil {
		return nil
	}
	m.reselectRun()
	save := m.saveView()
	if order, ok := core.ServerOrder(r.View.Sort); ok {
		r.Order = joinOrder(order)
		return tea.Batch(save, m.loadRuns(false))
	}
	if c := m.cancel["runs"]; c != nil {
		c()
	}
	m.seq++
	r.Gen = m.seq
	r.Pending = false
	r.FirstPagePending = false
	r.LoadingAll = false
	m.status = fmt.Sprintf("Sorted %d loaded runs locally; A loads all matching results", len(r.Rows))
	return save
}
func (m *model) pickerView(w, h int) string {
	if m.overlay == "info" || m.overlay == "parent-info" {
		var lines []string
		title := "Experiment information"
		if m.overlay == "parent-info" {
			title = "Parent run (context only)"
			if m.parentInfo != nil {
				r := m.parentInfo
				lines = []string{"Name: " + r.Name(), "Run ID: " + r.ID(), "Experiment: " + r.Info.ExperimentID, "Status: " + r.Info.Status, "Parent: " + r.ParentID()}
				lines = append(lines, kvLines(r.Data.Params)...)
			} else {
				lines = []string{"Loading parent…"}
			}
		} else if s := m.state(); s != nil {
			for _, e := range s.Experiments {
				if e.ID == s.Selected {
					lines = []string{"Name: " + e.Name, "ID: " + e.ID, "Artifacts: " + e.ArtifactLocation, "Lifecycle: " + e.LifecycleStage}
					lines = append(lines, kvLines(e.Tags)...)
					break
				}
			}
		}
		var wrapped []string
		for _, line := range lines {
			wrapped = append(wrapped, wrapText(clean(line), max(1, w-2))...)
		}
		start := clamp(m.menuIndex, 0, max(0, len(wrapped)-max(1, h-2)))
		return frame(title, wrapped[start:], w, h, true)
	}
	items := m.pickerItems()
	title := map[string]string{"columns": "Columns", "sort": "Sort order", "group": "Group by / nested runs", "layout": "Layout", "visibility": "Local visibility"}[m.overlay]
	if m.overlay == "columns" {
		title += fmt.Sprintf(" · %d selected", len(m.viewColumns()))
	}
	lines := []string{m.pickerSearch.View()}
	if !m.pickerTyping {
		lines[0] = "Search: " + clean(m.pickerSearch.Value()) + "  (/ or Tab edits)"
	}
	capacity := max(1, h-3)
	start := listStart(m.menuIndex, len(items), capacity)
	for i := start; i < min(len(items), start+capacity); i++ {
		lines = append(lines, row(items[i].Label, i == m.menuIndex, w-2))
	}
	if len(items) == 0 {
		lines = append(lines, "No matching fields in loaded runs.")
	}
	return frame(title, lines, w, h, true)
}
func (m *model) pickerHit(x, y, w, h int) string {
	if y < 1 || x < 0 || x >= w {
		return ""
	}
	items := m.pickerItems()
	start := listStart(m.menuIndex, len(items), max(1, h-1))
	i := start + y - 1
	if i >= 0 && i < len(items) && y < h {
		return "picker:" + items[i].ID
	}
	return ""
}
func wrapText(s string, width int) []string {
	return strings.Split(ansi.Hardwrap(s, max(1, width), true), "\n")
}
