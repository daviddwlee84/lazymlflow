package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpFilterTypingAndReturnPreserveContext(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.setLocal("r")
	m.selectRun(1)
	m.detailOffset = 7
	selected := m.runs().Selected
	key(m, "?")
	key(m, "/")
	for _, r := range "jkhql/?123M" {
		key(m, string(r))
	}
	m.Update(tea.PasteMsg{Content: "專案"})
	if m.overlay != "help" || !m.helpTyping || m.helpSearch.Value() != "jkhql/?123M專案" {
		t.Fatalf("help search dispatched a shortcut: %q %t %q", m.overlay, m.helpTyping, m.helpSearch.Value())
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "No matching help entries") {
		t.Fatal("empty search did not explain how to recover")
	}
	key(m, "enter")
	if m.overlay != "help" || m.helpTyping || m.helpSearch.Value() == "" {
		t.Fatal("Enter must keep the query and return to help navigation")
	}
	key(m, "esc")
	if m.overlay != "help" || m.helpSearch.Value() != "" {
		t.Fatal("first Esc must clear the accepted filter")
	}
	key(m, "esc")
	if m.overlay != "" || m.focus != 1 || m.runs().Local != "r" || m.runs().Selected != selected || m.detailOffset != 7 {
		t.Fatal("closing help changed the underlying reading context")
	}
}

func TestHelpFilterMatchesKeysDescriptionsAndNotes(t *testing.T) {
	m, _ := readyModel()
	key(m, "?")
	key(m, "/")
	key(m, "PANE")
	all, matches := m.helpLines()
	if len(matches) < 2 || len(matches) >= len(all) {
		t.Fatalf("description filter did not narrow results: %d/%d", len(matches), len(all))
	}
	for _, line := range matches {
		if !strings.Contains(strings.ToLower(line), "pane") {
			t.Fatalf("unmatched help row: %s", line)
		}
	}
	key(m, "down")
	index := m.menuIndex
	key(m, "left")
	if !m.helpTyping || m.menuIndex != index || index == 0 {
		t.Fatal("cursor-only movement reset help selection")
	}
	key(m, "right")
	m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if m.menuIndex != index || m.helpSearch.Value() != "PANE" {
		t.Fatal("a no-op edit reset help selection")
	}
	key(m, "x")
	if m.menuIndex != 0 {
		t.Fatal("a changed query did not reset to the first result")
	}
	key(m, "enter")
	if m.overlay != "help" || m.helpTyping {
		t.Fatal("accepting the filter closed help")
	}
	key(m, "/")
	key(m, "esc")
	key(m, "/")
	key(m, "CTRL+C")
	_, matches = m.helpLines()
	if len(matches) != 1 || !strings.Contains(matches[0], "Ctrl+C exits") {
		t.Fatalf("shortcut or explanatory note was not searchable: %v", matches)
	}
	key(m, "esc")
	key(m, "/")
	key(m, "shift+tab")
	_, matches = m.helpLines()
	if len(matches) != 1 || !strings.Contains(matches[0], "Previous pane") {
		t.Fatalf("shortcut binding was not searchable: %v", matches)
	}
}

func TestHelpFilteredLayoutAndMouseAgree(t *testing.T) {
	m, _ := readyModel()
	key(m, "?")
	key(m, "/")
	key(m, "pane")
	key(m, "enter")
	for _, size := range [][2]int{{120, 36}, {38, 12}, {20, 8}, {12, 4}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.menuIndex = 2
		view := ansi.Strip(m.View().Content)
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("help overflowed width %d: %q", size[0], line)
			}
		}
		r := m.geometry().Content
		if got := m.hitAt(2, r.Y+1); got != "" {
			t.Fatalf("search field acted as a help row: %s", got)
		}
		if size[0] < 16 || size[1] < 5 {
			if got := m.hitAt(2, r.Y+2); got != "" {
				t.Fatalf("hidden help row was clickable: %s", got)
			}
			continue
		}
		_, matches := m.helpLines()
		start := listStart(m.menuIndex, len(matches), r.H-3)
		if got := m.hitAt(2, r.Y+2); got != "overlay:"+strconv.Itoa(start) {
			t.Fatalf("mouse row did not match the filtered viewport: start=%d hit=%s", start, got)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	key(m, "/")
	key(m, "no-match")
	if got := m.hitAt(2, m.geometry().Content.Y+2); got != "" {
		t.Fatalf("no-match message activated a stale help row: %s", got)
	}
}
