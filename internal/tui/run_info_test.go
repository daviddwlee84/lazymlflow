package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestRunInfoShowsCompleteNameAndPreservesSelection(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.width, m.height = 42, 16
	runName := strings.Repeat("training-long-segment_", 18) + "完整名稱é👩🏽‍💻"
	experimentName := strings.Repeat("experiment-section_", 12) + "驗證專案"
	m.state().Runs["e1"].Rows[0].Info.RunName = runName
	m.state().Experiments[0].Name = experimentName
	selected := m.runs().Selected
	key(m, "i")
	if m.overlay != "run-info" {
		t.Fatalf("i did not open run information: %q", m.overlay)
	}
	title, lines := m.informationLines()
	if title != "Run information" || lines[0] != "Name: "+runName || !containsString(lines, "Experiment: "+experimentName) || !containsString(lines, "Run ID: "+selected) || !containsString(lines, "Experiment ID: e1") || !containsString(lines, "Status: RUNNING") {
		t.Fatalf("run information lost identity: %q %v", title, lines)
	}
	for _, line := range lines[:4] {
		wrapped := wrapText(clean(line), m.width-2)
		if strings.Join(wrapped, "") != clean(line) {
			t.Fatalf("wrapping changed full text: %q", line)
		}
		for _, part := range wrapped {
			if ansi.StringWidth(part) > m.width-2 {
				t.Fatalf("wrapped line exceeds pane: %q", part)
			}
		}
	}
	if strings.Contains(ansi.Strip(m.pickerView(m.width, m.height-4)), "…") {
		t.Fatal("full information name was truncated with ellipsis")
	}
	key(m, "G")
	maximum := m.informationMaxOffset()
	if maximum == 0 || m.menuIndex != maximum {
		t.Fatalf("end did not expose final lines: offset=%d max=%d", m.menuIndex, maximum)
	}
	key(m, "up")
	if m.menuIndex != maximum-1 {
		t.Fatal("scrolling up from the end did not move")
	}
	key(m, "home")
	if m.menuIndex != 0 {
		t.Fatal("home did not restore first name line")
	}
	if copied, ok := m.informationName(); !ok || copied != runName {
		t.Fatal("copy source is not the exact full name")
	}
	if cmd := key(m, "Y"); cmd == nil {
		t.Fatal("full-name copy action is missing")
	}
	// Do not execute clipboard commands against the test user's desktop.
	key(m, "esc")
	if m.overlay != "" || m.runs().Selected != selected || m.state().Selected != "e1" {
		t.Fatal("run information changed browser selection")
	}
}

func TestRunInfoUsesActivityRunsActualExperiment(t *testing.T) {
	m, a := activityReviewModel(t)
	runName := "activity run " + strings.Repeat("long_name", 20)
	experimentName := "actual experiment " + strings.Repeat("跨實驗", 20)
	snapshot := activityReviewSnapshot(m, core.ActivityRecord{RunID: "activity-run", RunName: runName, ExperimentID: "e2", ExperimentName: experimentName, Status: "FINISHED", Revision: 2, ReadRevision: 1, ObservedAt: 1})
	snapshot.Experiments = []core.Experiment{{ID: "e2", Name: experimentName}}
	m.acceptActivitySnapshot(snapshot)
	key(m, "i")
	_, lines := m.informationLines()
	if m.overlay != "run-info" || !containsString(lines, "Experiment: "+experimentName) || !containsString(lines, "Experiment ID: e2") || lines[0] != "Name: "+runName {
		t.Fatalf("Activity used sidebar experiment instead of run identity: %v", lines)
	}
	if m.state().Selected != "e1" || a.Scope != scopeUnread {
		t.Fatal("information popup followed or changed experiment selection")
	}
	key(m, "enter")
	if m.run() == nil || m.run().ID() != "activity-run" || a.Scope != scopeUnread {
		t.Fatal("closing Activity information lost its run context")
	}
}

func TestInformationNamesCopyAndBoundedScrolling(t *testing.T) {
	m, _ := readyModel()
	m.width, m.height, m.focus = 40, 14, 0
	name := strings.Repeat("very-long-experiment-name_", 30)
	m.state().Experiments[0].Name = name
	key(m, "i")
	if value, ok := m.informationName(); !ok || value != name {
		t.Fatal("existing experiment info lost full-name copy")
	}
	m.menuIndex = 1 << 20
	key(m, "up")
	if m.menuIndex != max(0, m.informationMaxOffset()-1) {
		t.Fatal("old overscroll prevents useful information navigation")
	}
	key(m, "home")
	m.Update(tea.MouseWheelMsg{X: 4, Y: 5, Button: tea.MouseWheelDown})
	if m.menuIndex == 0 {
		t.Fatal("mouse wheel does not scroll information")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	key(m, "end")
	if m.menuIndex != m.informationMaxOffset() {
		t.Fatal("resize did not use actual wrapped information length")
	}
	key(m, "esc")
	parent := sampleRun("parent")
	parent.Info.RunName = "full parent name"
	m.parentInfo = &parent
	m.openPicker("parent-info")
	if value, ok := m.informationName(); !ok || value != "full parent name" {
		t.Fatal("parent information name was confused with selected child")
	}
}

func TestRunInfoUnavailableForGroupHeaders(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.runs().View.Mode = "grouped"
	m.runs().View.GroupBy = []string{"lr"}
	m.reselectRun()
	m.selectRun(0)
	if m.run() != nil {
		t.Fatal("setup did not select group header")
	}
	for _, action := range m.actions() {
		if action.ID == "run-info" {
			t.Fatal("group header exposes a run information action")
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
