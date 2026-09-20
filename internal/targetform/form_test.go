package targetform

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func press(m *Model, key string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch key {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+o":
		msg = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	default:
		msg = tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
	}
	_, cmd := m.Update(msg)
	return cmd
}
func draft() core.Target {
	return core.Target{ID: "local", Name: "Existing", TrackingURI: "http://localhost:5000", Python: "/project/.venv", MLflowVersion: "3.12.0", TokenEnv: "MLFLOW_TRACKING_TOKEN", Env: map[string]string{"AWS_PROFILE": "MY_AWS_PROFILE"}}
}
func TestSharedFormReviewPreservesRuntimeAndAdvancedValues(t *testing.T) {
	m := New(draft(), "/tmp/config.toml", false)
	m.Init()
	for i := 0; i < basicFields-1; i++ {
		press(m, "enter")
	}
	cmd := press(m, "enter")
	if cmd == nil || m.review || !m.validating {
		t.Fatal("validation was not deferred")
	}
	m.Update(cmd())
	if !m.review {
		t.Fatalf("did not enter review: %v", m.Err)
	}
	view := m.View(120, 25)
	for _, want := range []string{"/tmp/config.toml", "/project/.venv", "http://localhost:5000"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review missing %q: %s", want, view)
		}
	}
	press(m, "enter")
	if !m.Done || m.Target.Python != "/project/.venv" || m.Target.MLflowVersion != "3.12.0" || m.Target.TokenEnv != "MLFLOW_TRACKING_TOKEN" || m.Target.Env["AWS_PROFILE"] != "MY_AWS_PROFILE" {
		t.Fatalf("runtime/advanced config changed: %#v", m.Target)
	}
}
func TestSharedFormTypingAndFixedEditID(t *testing.T) {
	m := New(draft(), "/tmp/config.toml", true)
	m.Init()
	for _, r := range "qjkh/l?" {
		press(m, string(r))
	}
	if m.Cancelled || m.Done || m.focus != 1 {
		t.Fatal("printable keys dispatched form actions")
	}
	if m.fields[0].Value() != "local" {
		t.Fatal("edit changed fixed target ID")
	}
	press(m, "esc")
	if !m.Cancelled {
		t.Fatal("Escape should cancel before review")
	}
}
func TestSharedFormAdvancedControlsAndPaste(t *testing.T) {
	m := New(draft(), "/tmp/config.toml", false)
	m.Init()
	press(m, "ctrl+o")
	if !m.advanced || m.count() != len(labels) {
		t.Fatal("advanced fields not available")
	}
	m.focus = 13
	m.fields[13].Focus()
	m.fields[13].SetValue("")
	m.Update(tea.PasteMsg{Content: "AWS_PROFILE=MY_PROFILE\n"})
	press(m, "enter") // SSH alias is the final advanced field.
	cmd := press(m, "enter")
	m.Update(cmd())
	if m.Err != nil || m.Target.Env["AWS_PROFILE"] != "MY_PROFILE" {
		t.Fatalf("pasted mapping invalid: %#v %v", m.Target.Env, m.Err)
	}
}

func TestSharedFormSSHIsAdvancedAndReviewed(t *testing.T) {
	target := draft()
	target.SSHHost = "remote-lab"
	m := New(target, "/tmp/config.toml", false)
	m.Init()
	m.focus = basicFields - 1
	cmd := press(m, "enter")
	m.Update(cmd())
	if m.Err != nil || m.Target.SSHHost != "remote-lab" || !strings.Contains(m.View(120, 30), "SSH host: remote-lab") {
		t.Fatalf("SSH field not retained/reviewed: %+v %v", m.Target, m.Err)
	}
}
func TestSharedFormValidationIsInstanceScopedAndCancelSafe(t *testing.T) {
	old := New(draft(), "/tmp/config.toml", false)
	old.Init()
	old.focus = 4
	cmd := press(old, "enter")
	press(old, "esc")
	newer := New(core.Target{ID: "other", TrackingURI: "http://different"}, "/tmp/config.toml", false)
	newer.Init()
	newer.focus = 4
	newCmd := press(newer, "enter")
	newer.Update(cmd())
	if newer.review || newer.Target.ID != "other" {
		t.Fatal("old validation affected replacement form")
	}
	newer.Update(newCmd())
	if !newer.review || newer.Target.ID != "other" {
		t.Fatal("current validation was rejected")
	}
}
func TestSharedFormInvalidInputStaysEditable(t *testing.T) {
	m := New(core.Target{ID: "bad/id", TrackingURI: "http://localhost:5000"}, "/tmp/config.toml", false)
	m.Init()
	m.focus = 4
	cmd := press(m, "enter")
	m.Update(cmd())
	if m.Err == nil || m.review || m.Done {
		t.Fatal("invalid ID was submitted")
	}
	if !strings.Contains(m.View(100, 20), "Error:") {
		t.Fatal("validation failure not visible")
	}
}
func TestSharedFormReviewEscapeAndDimensions(t *testing.T) {
	m := New(draft(), "/tmp/config.toml", false)
	m.Init()
	m.focus = 4
	cmd := press(m, "enter")
	m.Update(cmd())
	press(m, "esc")
	if m.Cancelled || m.review {
		t.Fatal("review Escape should return to editing")
	}
	for _, size := range [][2]int{{1, 1}, {12, 4}, {38, 12}, {80, 24}, {120, 36}} {
		view := m.View(size[0], size[1])
		lines := strings.Split(view, "\n")
		if len(lines) > size[1] {
			t.Fatal("form exceeds available height")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("form exceeds available width")
			}
		}
	}
}

func formPoint(t *testing.T, m *Model, id string) (int, int) {
	t.Helper()
	_, hits := m.content(m.width, m.height)
	for _, h := range hits {
		if h.ID == id && h.X < m.width && h.Y < m.height {
			return h.X, h.Y
		}
	}
	t.Fatalf("missing visible form hit %s", id)
	return 0, 0
}
func clickForm(m *Model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}
func TestFormMouseFocusTypingAndNoPressThrough(t *testing.T) {
	m := New(draft(), "/tmp/config.toml", false)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	x, y := formPoint(t, m, "field:2")
	clickForm(m, x, y)
	if m.focus != 2 {
		t.Fatal("click did not focus Tracking URI")
	}
	m.fields[2].SetValue("")
	for _, r := range "qjk/123" {
		press(m, string(r))
	}
	if m.fields[2].Value() != "qjk/123" || m.Cancelled || m.Done {
		t.Fatal("typing invoked form shortcut")
	}
	x, y = formPoint(t, m, "field:1")
	otherX, otherY := formPoint(t, m, "field:4")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: otherX, Y: otherY, Button: tea.MouseLeft})
	if m.focus != 2 {
		t.Fatal("release over another field activated it")
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 29})
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.focus != 2 {
		t.Fatal("resize retained stale mouse press")
	}
}
func TestFormMouseReviewUsesSemanticActions(t *testing.T) {
	for _, action := range []string{"save", "edit", "cancel"} {
		t.Run(action, func(t *testing.T) {
			m := New(draft(), "/tmp/config.toml", false)
			m.Init()
			m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
			m.focus = basicFields - 1
			cmd := press(m, "enter")
			m.Update(cmd())
			x, y := formPoint(t, m, action)
			m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			if m.Done || m.Cancelled || !m.review {
				t.Fatal("action executed on press")
			}
			m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
			switch action {
			case "save":
				if !m.Done {
					t.Fatal("Save not submitted")
				}
			case "edit":
				if m.review || m.Done || m.Cancelled {
					t.Fatal("Edit did not restore fields")
				}
			case "cancel":
				if !m.Cancelled {
					t.Fatal("Cancel not applied")
				}
			}
		})
	}
}
func TestFormMouseHitGeometryIsPureAndDoesNotMatchUserText(t *testing.T) {
	initial := draft()
	initial.Name = "Enter save"
	m := New(initial, "/tmp/config.toml", true)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	if m.hitAt(1, 1) != "" {
		t.Fatal("fixed edit ID is clickable")
	}
	m.focus = 4
	cmd := press(m, "enter")
	m.Update(cmd())
	beforeFocus, beforeGeneration := m.focus, m.generation
	for i := 0; i < 3; i++ {
		m.View(100, 12)
		m.hitAt(10, 3)
	}
	if m.Done || m.focus != beforeFocus || m.generation != beforeGeneration {
		t.Fatal("paint/hit calculation mutated the model")
	}
	if hit := m.hitAt(8, 3); hit != "" {
		t.Fatalf("user-controlled review text became an action: %s", hit)
	}
}
