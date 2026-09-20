package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *model) workspaceMouseScope() string {
	w := m.work
	c := m.catalogState()
	revision := ""
	if c != nil {
		revision = fmt.Sprintf("/%d/%d/%s/%d", c.Gen, c.Value.UpdatedAt, c.Selected, c.Tab)
	}
	return fmt.Sprintf("%s/%s/%d/%d/%d/%d/%d/%t/%d/%s", m.active, w.modal, w.noteGen, w.reportGen, m.width, m.height, m.layoutRevision, m.zoom, m.focus, w.subject.ID) + revision
}
func (m *model) workspaceHit(x, y int) string {
	w := m.work
	if w.modal != "" {
		r := workspaceDialog(m.width, m.height)
		if !r.contains(x, y) {
			return ""
		}
		if w.modal == "edit" {
			lines := m.workspaceModalLines(r.W-2, r.H-2)
			if y == r.Y+len(lines) {
				pos := x - r.X - 1
				if pos >= 0 && pos < 13 {
					return "note:save"
				}
				if pos >= 15 && pos < 30 {
					return "note:editor"
				}
				if pos >= 32 {
					return "note:cancel"
				}
			}
			if y >= r.Y+2 && y < r.Y+2+w.editor.Height() {
				return "note:textarea"
			}
		}
		if w.modal == "notes" {
			rows := m.visibleNotes()
			head := 1
			if w.typing || w.query != "" {
				head++
			}
			if w.notePending {
				head++
			}
			if w.noteErr != "" {
				head++
			}
			cap := max(1, min(5, (r.H-2)/3))
			start := listStart(w.noteIndex, len(rows), cap)
			index := start + y - r.Y - 1 - head
			if index >= start && index < min(len(rows), start+cap) {
				return "note:select:" + rows[index].ID
			}
		}
		return ""
	}
	if !w.catalog {
		return ""
	}
	l := m.geometry()
	if l.Split && l.Vertical.contains(x, y) {
		return "divider:left"
	}
	if l.Split && l.Horizontal.contains(x, y) {
		return "divider:top"
	}
	for i, r := range l.Panes {
		if !r.contains(x, y) {
			continue
		}
		if i == 2 && y == r.Y {
			cursor := r.X + 4
			if m.focus == 2 {
				cursor++
			}
			c := m.catalogState()
			for j, name := range []string{"Schema", "Metadata", "Notes"} {
				if c != nil && c.Tab == j {
					name = "[" + name + "]"
				}
				width := textWidth(name)
				if x >= cursor && x < cursor+width {
					return "catalog:tab:" + strconv.Itoa(j)
				}
				cursor += width + 1
			}
		}
		p := m.catalogPane(i, r.W-2, r.H-2)
		for _, h := range p.Hits {
			if h.contains(x-r.X-1, y-r.Y-1) && x < r.X+r.W-1 && y < r.Y+r.H-1 {
				return h.ID
			}
		}
		return "catalog:pane:" + strconv.Itoa(i)
	}
	return ""
}

func (m *model) workspaceMouse(msg tea.Msg) tea.Cmd {
	w := m.work
	if !m.layout.Mouse {
		return nil
	}
	var e tea.Mouse
	switch v := msg.(type) {
	case tea.MouseClickMsg:
		e = v.Mouse()
	case tea.MouseReleaseMsg:
		e = v.Mouse()
	case tea.MouseMotionMsg:
		e = v.Mouse()
	case tea.MouseWheelMsg:
		e = v.Mouse()
	}
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		w.pressed = ""
		d := 3
		if e.Button == tea.MouseWheelUp {
			d = -3
		}
		if w.modal != "" {
			switch w.modal {
			case "notes":
				w.noteIndex = clamp(w.noteIndex+d, 0, len(m.visibleNotes())-1)
				w.noteOffset = 0
			case "report", "field":
				w.reportOffset = max(0, w.reportOffset+d)
			case "edit":
				var cmd tea.Cmd
				w.editor, cmd = w.editor.Update(msg)
				return cmd
			}
			return nil
		}
		for i, r := range m.geometry().Panes {
			if r.contains(e.X, e.Y) {
				before := m.focus
				m.focus = i
				m.catalogMove(d)
				m.focus = before
				break
			}
		}
		return nil
	}
	if _, ok := msg.(tea.MouseMotionMsg); ok {
		if w.modal != "" {
			return nil
		}
		if m.drag == "left" {
			m.layout.LeftRatio = max(.18, min(.75, float64(e.X)/float64(max(1, m.width))))
			m.layoutRevision++
		}
		if m.drag == "top" {
			m.layout.TopRatio = max(.25, min(.80, float64(e.Y-1)/float64(max(1, m.geometry().Content.H))))
			m.layoutRevision++
		}
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		if e.Button != tea.MouseLeft {
			return nil
		}
		w.pressed = m.workspaceHit(e.X, e.Y)
		w.pressedScope = m.workspaceMouseScope()
		if strings.HasPrefix(w.pressed, "divider:") {
			m.drag = strings.TrimPrefix(w.pressed, "divider:")
			w.pressed = ""
		}
		return nil
	}
	if _, ok := msg.(tea.MouseReleaseMsg); !ok {
		return nil
	}
	if m.drag != "" {
		m.drag = ""
		w.pressed = ""
		return m.saveLayout()
	}
	pressed, scope := w.pressed, w.pressedScope
	w.pressed = ""
	if e.Button != tea.MouseLeft || pressed == "" || scope != m.workspaceMouseScope() || pressed != m.workspaceHit(e.X, e.Y) {
		return nil
	}
	switch pressed {
	case "note:save":
		return m.saveJournal()
	case "note:editor":
		return m.openNoteEditor()
	case "note:cancel":
		return m.workspaceModalKey(nil, "esc")
	case "note:textarea":
		r := workspaceDialog(m.width, m.height)
		w.editor.BeginSelection(max(0, e.X-r.X-1), max(0, e.Y-r.Y-2))
		w.editor.EndSelection()
		return w.editor.Focus()
	}
	if strings.HasPrefix(pressed, "note:select:") {
		id := strings.TrimPrefix(pressed, "note:select:")
		for i, n := range m.visibleNotes() {
			if n.ID == id {
				w.noteIndex = i
				w.noteOffset = 0
				break
			}
		}
		return nil
	}
	c := m.catalogState()
	if c == nil {
		return nil
	}
	if strings.HasPrefix(pressed, "catalog:pane:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(pressed, "catalog:pane:"))
		m.focus = clamp(i, 0, 2)
	}
	if strings.HasPrefix(pressed, "catalog:tab:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(pressed, "catalog:tab:"))
		c.Tab = clamp(i, 0, 2)
		c.DetailIndex = 0
		m.focus = 2
	}
	if strings.HasPrefix(pressed, "catalog:dataset:") {
		id := strings.TrimPrefix(pressed, "catalog:dataset:")
		for i, r := range c.Rows {
			if r.ID == id {
				c.Index = i
				c.Selected = id
				c.RunIndex = 0
				c.DetailIndex = 0
				break
			}
		}
		m.focus = 0
	}
	if strings.HasPrefix(pressed, "catalog:group:") {
		name := strings.TrimPrefix(pressed, "catalog:group:")
		c.Collapsed[name] = !c.Collapsed[name]
		c.Selected = "name:" + name
		m.rebuildCatalogRows(c)
		m.focus = 0
	}
	if strings.HasPrefix(pressed, "catalog:run:") {
		id := strings.TrimPrefix(pressed, "catalog:run:")
		for i, u := range m.catalogUses() {
			if u.RunID == id {
				c.RunIndex = i
				break
			}
		}
		m.focus = 1
	}
	if strings.HasPrefix(pressed, "catalog:field:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(pressed, "catalog:field:"))
		c.DetailIndex = i
		m.focus = 2
	}
	return nil
}
