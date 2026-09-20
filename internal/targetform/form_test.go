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
	cmd := press(m, "enter")
	m.Update(cmd())
	if m.Err != nil || m.Target.Env["AWS_PROFILE"] != "MY_PROFILE" {
		t.Fatalf("pasted mapping invalid: %#v %v", m.Target.Env, m.Err)
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
