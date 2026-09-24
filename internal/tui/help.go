package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *model) helpLines() (all, filtered []string) {
	for _, a := range m.actions() {
		all = append(all, fit(strings.Join(a.Keys, " / "), 20)+a.Label)
	}
	all = append(all, "gg: first row", "Metrics shown are latest values; missing values are —.", "Local search scans loaded rows; f and s query all server rows.", "Ctrl+C exits; Ctrl+X cancels an active download.")
	query := strings.ToLower(strings.TrimSpace(m.helpSearch.Value()))
	for _, line := range all {
		if strings.Contains(strings.ToLower(line), query) {
			filtered = append(filtered, line)
		}
	}
	return all, filtered
}

func (m *model) updateHelpSearch(msg tea.Msg) tea.Cmd {
	before := m.helpSearch.Value()
	var cmd tea.Cmd
	m.helpSearch, cmd = m.helpSearch.Update(msg)
	if before != m.helpSearch.Value() {
		m.menuIndex = 0
		m.mousePressed = ""
	}
	return cmd
}

func (m *model) handleHelp(msg tea.Msg, key string) tea.Cmd {
	if key == "esc" {
		if m.helpTyping || m.helpSearch.Value() != "" {
			m.helpSearch.SetValue("")
			m.helpSearch.Blur()
			m.helpTyping = false
			m.menuIndex = 0
		} else {
			m.overlay = ""
		}
		return nil
	}
	_, lines := m.helpLines()
	if key == "up" || key == "down" {
		delta := 1
		if key == "up" {
			delta = -1
		}
		m.menuIndex = clamp(m.menuIndex+delta, 0, len(lines)-1)
		return nil
	}
	if m.helpTyping {
		if key == "enter" {
			m.helpTyping = false
			m.helpSearch.Blur()
			return nil
		}
		return m.updateHelpSearch(msg)
	}
	switch key {
	case "/":
		m.helpTyping = true
		return m.helpSearch.Focus()
	case "q", "enter":
		m.overlay = ""
	case "k":
		m.menuIndex = max(0, m.menuIndex-1)
	case "j":
		m.menuIndex = clamp(m.menuIndex+1, 0, len(lines)-1)
	case "home":
		m.menuIndex = 0
	case "end", "G":
		m.menuIndex = max(0, len(lines)-1)
	}
	return nil
}

func (m *model) helpView(w, h int) string {
	all, matches := m.helpLines()
	search := m.helpSearch
	search.SetWidth(max(1, w-12))
	lines := []string{search.View()}
	if !m.helpTyping {
		lines[0] = "Search: " + clean(search.Value()) + "  (/ to filter)"
	}
	capacity := max(0, h-3)
	selected := clamp(m.menuIndex, 0, len(matches)-1)
	start := listStart(selected, len(matches), capacity)
	for i := start; i < min(len(matches), start+capacity); i++ {
		lines = append(lines, row(matches[i], i == selected, w-2))
	}
	if len(matches) == 0 {
		lines = append(lines, "No matching help entries. Esc clears the filter.")
	}
	return frame(fmt.Sprintf("Keyboard actions · %d/%d", len(matches), len(all)), lines, w, h, true)
}

func (m *model) helpHit(x, y int, r rect) string {
	if m.width < 16 || m.height < 5 || x <= r.X || x >= r.X+r.W-1 || y <= r.Y+1 || y >= r.Y+r.H-1 {
		return ""
	}
	_, lines := m.helpLines()
	start := listStart(clamp(m.menuIndex, 0, len(lines)-1), len(lines), max(0, r.H-3))
	i := start + y - r.Y - 2
	if i >= 0 && i < len(lines) {
		return fmt.Sprintf("overlay:%d", i)
	}
	return ""
}
