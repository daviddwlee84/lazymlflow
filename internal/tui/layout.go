package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type rect struct{ X, Y, W, H int }

func (r rect) contains(x, y int) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

type paneLayout struct {
	Panes                [3]rect
	Content              rect
	Vertical, Horizontal rect
	Split                bool
}

// calculateLayout is the only source of pane geometry for rendering and input.
func calculateLayout(w, h, focus int, zoom, input bool, p core.LayoutPreferences) paneLayout {
	height := max(1, h-4)
	if input {
		height = max(1, height-3)
	}
	l := paneLayout{Content: rect{0, 1, w, height}}
	if w < 90 || height < 12 || zoom {
		l.Panes[clamp(focus, 0, 2)] = l.Content
		return l
	}
	left := clamp(int(float64(w)*p.LeftRatio), 18, w-28)
	top := clamp(int(float64(height)*p.TopRatio), 6, height-4)
	l.Split = true
	l.Panes = [3]rect{{0, 1, left, height}, {left, 1, w - left, top}, {left, 1 + top, w - left, height - top}}
	l.Vertical = rect{left - 1, 1, 2, height}
	l.Horizontal = rect{left, top, w - left, 2}
	return l
}
func (m *model) geometry() paneLayout {
	return calculateLayout(m.width, m.height, m.focus, m.zoom, m.inputMode != "", m.layout)
}

type hit struct {
	rect
	ID string
}
type paneContent struct {
	Lines []string
	Hits  []hit
}

func (p *paneContent) add(text string) { p.Lines = append(p.Lines, text) }
func (p *paneContent) addHit(text, id string, w int) {
	y := len(p.Lines)
	p.add(text)
	p.Hits = append(p.Hits, hit{rect{0, y, w, 1}, id})
}
func (m *model) resizeKey(key string) tea.Cmd {
	m.layoutRevision++
	switch key {
	case "esc", "enter":
		m.resizing = false
		m.status = "Pane proportions updated"
		return m.saveLayout()
	case "h", "left":
		m.layout.LeftRatio -= .025
	case "l", "right":
		m.layout.LeftRatio += .025
	case "k", "up":
		m.layout.TopRatio -= .025
	case "j", "down":
		m.layout.TopRatio += .025
	case "0":
		m.layout.LeftRatio = .30
		m.layout.TopRatio = .55
	}
	m.layout.LeftRatio = max(.18, min(.75, m.layout.LeftRatio))
	m.layout.TopRatio = max(.25, min(.80, m.layout.TopRatio))
	m.mousePressed = ""
	return nil
}
func (m *model) mouseScope() string {
	return fmt.Sprintf("%s/%s/%s/%s/%d/%d/%d/%t", m.active, m.selectedExperiment()+"/"+m.selectedRowID(), m.overlay, m.inputMode, m.width, m.height, m.layoutRevision, m.zoom)
}
func (m *model) selectedRowID() string {
	if r := m.runs(); r != nil {
		return r.Selected
	}
	return ""
}
func (m *model) selectedExperiment() string {
	if s := m.state(); s != nil {
		return s.Selected
	}
	return ""
}
func (m *model) hitAt(x, y int) string {
	l := m.geometry()
	if m.targetForm != nil || m.inputMode != "" || m.compare {
		return ""
	}
	if m.overlay != "" {
		if !l.Content.contains(x, y) {
			return ""
		}
		if m.isPicker() {
			return m.pickerHit(x-l.Content.X-1, y-l.Content.Y-1, l.Content.W-2, l.Content.H-2)
		}
		return m.overlayHit(x, y, l.Content)
	}
	if l.Split {
		if l.Vertical.contains(x, y) {
			return "divider:left"
		}
	}
	for pane, r := range l.Panes {
		if !r.contains(x, y) {
			continue
		}
		if pane == 2 && y == r.Y {
			cursor := r.X + 4
			if m.focus == 2 {
				cursor++
			}
			for i, name := range detailTabs() {
				if i == m.tab {
					name = "[" + name + "]"
				}
				width := textWidth(name)
				if x >= cursor && x < min(cursor+width, r.X+r.W-1) {
					return fmt.Sprintf("tab:%d", i)
				}
				cursor += width + 1
			}
		}
		if l.Split && l.Horizontal.contains(x, y) {
			return "divider:top"
		}
		px, py := x-r.X-1, y-r.Y-1
		var content paneContent
		switch pane {
		case 0:
			content = m.experimentContent(r.W-2, r.H-2)
		case 1:
			content = m.runContent(r.W-2, r.H-2)
		case 2:
			content = m.detailContent(r.W-2, r.H-2)
		}
		for _, h := range content.Hits {
			if py >= 0 && py < r.H-2 && px >= 0 && px < r.W-2 && h.contains(px, py) {
				return h.ID
			}
		}
		return fmt.Sprintf("pane:%d", pane)
	}
	return ""
}
func (m *model) handleMouse(msg tea.Msg) tea.Cmd {
	if !m.layout.Mouse {
		return nil
	}
	if m.targetForm != nil {
		return m.updateTargetForm(msg)
	}
	if m.inputMode != "" {
		m.mousePressed = ""
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
		m.mousePressed = ""
		d := 3
		if e.Button == tea.MouseWheelUp {
			d = -3
		}
		if m.overlay != "" {
			if m.isPicker() {
				if m.overlay == "info" || m.overlay == "parent-info" {
					m.menuIndex = max(0, m.menuIndex+d)
					return nil
				}
				m.menuIndex = clamp(m.menuIndex+d, 0, len(m.pickerItems())-1)
				return nil
			}
			key := "down"
			if d < 0 {
				key = "up"
			}
			return m.handleOverlay(key)
		}
		if m.compare {
			return m.move(d)
		}
		for i, r := range m.geometry().Panes {
			if r.contains(e.X, e.Y) {
				before := m.focus
				m.focus = i
				cmd := m.move(d)
				m.focus = before
				return cmd
			}
		}
		return nil
	}
	if _, ok := msg.(tea.MouseMotionMsg); ok {
		if m.drag != "" {
			m.layoutRevision++
		}
		switch m.drag {
		case "left":
			m.layout.LeftRatio = max(.18, min(.75, float64(e.X)/float64(max(1, m.width))))
		case "top":
			m.layout.TopRatio = max(.25, min(.80, float64(e.Y-1)/float64(max(1, m.geometry().Content.H))))
		default:
			if strings.HasPrefix(m.drag, "column:") {
				id := strings.TrimPrefix(m.drag, "column:")
				if r := m.runs(); r != nil {
					for i, c := range r.View.Columns {
						if columnID(c) == id {
							r.View.Columns[i].Width = clamp(m.dragWidth+e.X-m.dragX, 6, 100)
						}
					}
				}
			}
		}
		if strings.HasPrefix(m.drag, "column:") {
			return m.saveView()
		}
		if m.drag != "" {
			return m.saveLayout()
		}
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		if e.Button != tea.MouseLeft {
			return nil
		}
		target := m.hitAt(e.X, e.Y)
		m.mousePressed = target
		m.mouseContext = m.mouseScope()
		if strings.HasPrefix(target, "divider:") {
			m.drag = strings.TrimPrefix(target, "divider:")
			m.mousePressed = ""
		}
		if strings.HasPrefix(target, "width:") {
			id := strings.TrimPrefix(target, "width:")
			m.drag = "column:" + id
			m.dragX = e.X
			m.focus = 1
			if r := m.runs(); r != nil {
				r.ViewRevision++
				for _, c := range r.View.Columns {
					if columnID(c) == id {
						m.dragWidth = columnWidth(c)
					}
				}
			}
			m.mousePressed = ""
		}
		return nil
	}
	if _, ok := msg.(tea.MouseReleaseMsg); ok {
		if e.Button != tea.MouseLeft && e.Button != tea.MouseNone {
			m.mousePressed = ""
			return nil
		}
		if m.drag != "" {
			drag := m.drag
			m.drag = ""
			m.mousePressed = ""
			if strings.HasPrefix(drag, "column:") {
				return m.saveView()
			}
			return m.saveLayout()
		}
		target, scope := m.mousePressed, m.mouseContext
		m.mousePressed = ""
		if target == "" || scope != m.mouseScope() || target != m.hitAt(e.X, e.Y) {
			return nil
		}
		return m.activateHit(target)
	}
	return nil
}
func (m *model) activateHit(id string) tea.Cmd {
	kind, key, _ := strings.Cut(id, ":")
	switch kind {
	case "action":
		if key == "columns" || key == "sort" || key == "group" {
			m.focus = 1
		}
		for _, a := range m.actions() {
			if a.ID == key {
				return m.perform(key)
			}
		}
	case "pane":
		m.focus, _ = strconv.Atoi(key)
		return m.ensureDetails()
	case "experiment":
		for i, e := range m.experimentsVisible() {
			if e.ID == key {
				m.focus = 0
				m.selectExperiment(i)
				return m.loadRuns(false)
			}
		}
	case "run":
		for i, r := range m.runRows() {
			if r.ID == key {
				m.focus = 1
				m.selectRun(i)
				return m.ensureDetails()
			}
		}
	case "expand":
		m.focus = 1
		for _, r := range m.runRows() {
			if r.ID == key {
				return m.toggleRow(r)
			}
		}
	case "sort":
		m.focus = 1
		for _, c := range m.viewColumns() {
			if columnID(c) == key {
				return m.sortColumn(c, false)
			}
		}
	case "artifact":
		if a := m.artifacts(); a != nil {
			for i, f := range a.Rows {
				if f.Path == key {
					m.focus = 2
					a.Index = i
					a.Selected = f.Path
					return nil
				}
			}
		}
	case "tab":
		m.tab, _ = strconv.Atoi(key)
		m.focus = 2
		m.detailOffset = 0
		return m.ensureDetails()
	case "picker":
		for i, item := range m.pickerItems() {
			if item.ID == key {
				m.menuIndex = i
				m.pickerTyping = false
				return m.choosePicker("space")
			}
		}
	case "overlay":
		i, _ := strconv.Atoi(key)
		m.menuIndex = i
		return m.handleOverlay("enter")
	}
	return nil
}
func (m *model) overlayHit(x, y int, r rect) string {
	count := 0
	switch m.overlay {
	case "targets":
		count = len(m.targets)
	case "metrics":
		count = len(m.metricKeys())
	case "help", "palette":
		count = len(m.actions()) + 4
	default:
		return ""
	}
	start := listStart(m.menuIndex, count, max(1, r.H-2))
	i := start + y - r.Y - 1
	if x > r.X && x < r.X+r.W-1 && y > r.Y && y < r.Y+r.H-1 && i >= 0 && i < count {
		return fmt.Sprintf("overlay:%d", i)
	}
	return ""
}
func (m *model) detailContent(w, h int) paneContent {
	p := paneContent{Lines: m.detailLines(w, h)}
	if m.tab != 4 {
		return p
	}
	a := m.artifacts()
	if a == nil {
		return p
	}
	prefix := 1
	if a.Pending {
		prefix++
	}
	if a.Err != "" {
		prefix += 2
	}
	capacity := max(1, h-prefix)
	start := listStart(a.Index, len(a.Rows), capacity)
	for i := start; i < min(len(a.Rows), start+capacity); i++ {
		p.Hits = append(p.Hits, hit{rect{0, prefix + i - start, w, 1}, "artifact:" + a.Rows[i].Path})
	}
	return p
}
func columnWidth(c core.ColumnSpec) int {
	if c.Width > 0 {
		return clamp(c.Width, 6, 100)
	}
	if c.Kind == "attribute" && c.Key == "name" {
		return 28
	}
	return 15
}
func clippedHit(p *paneContent, x, y, w, available int, id string) {
	if x >= available {
		return
	}
	p.Hits = append(p.Hits, hit{rect{x, y, min(w, available-x), 1}, id})
}
func textWidth(v string) int { return ansi.StringWidth(v) }
