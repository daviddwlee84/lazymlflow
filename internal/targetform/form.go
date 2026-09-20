// Package targetform owns the configuration form shared by the standalone CLI
// and the dashboard. It never owns the terminal, persists configuration, or
// emits tea.Quit; its host decides how to handle Done and Cancelled.
package targetform

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type Model struct {
	Done                                  bool
	Cancelled                             bool
	Target                                core.Target
	Err                                   error
	path                                  string
	editing, advanced, review, validating bool
	fields                                []textinput.Model
	focus                                 int
	generation                            uint64
	width                                 int
}
type validatedMsg struct {
	owner      *Model
	generation uint64
	target     core.Target
	err        error
}

var labels = []string{"ID", "Display name (optional)", "Tracking URI", "Python / virtual environment (optional)", "Managed MLflow version (optional)", "Original working directory (optional)", "Artifact destination (optional)", "Browser URL (optional)", "Bearer token environment variable (optional)", "Username environment variable (optional)", "Password environment variable (optional)", "Custom CA file (optional)", "Extra Python packages (comma separated)", "Child environment references (NAME=SOURCE, comma separated)"}

const basicFields = 5

func New(initial core.Target, configPath string, editing bool) *Model {
	m := &Model{Target: initial, path: configPath, editing: editing, width: 80}
	var env []string
	for k, v := range initial.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	values := []string{initial.ID, initial.Name, initial.TrackingURI, initial.Python, initial.MLflowVersion, initial.WorkingDir, initial.ArtifactsDestination, initial.WebURL, initial.TokenEnv, initial.UsernameEnv, initial.PasswordEnv, initial.CAFile, strings.Join(initial.ExtraPackages, ","), strings.Join(env, ",")}
	for i, value := range values {
		field := textinput.New()
		field.Prompt = "> "
		field.CharLimit = 8192
		field.SetWidth(65)
		field.SetValue(value)
		switch i {
		case 2:
			field.Placeholder = "https://server:5000, ./mlruns, sqlite:///mlflow.db"
		case 3:
			field.Placeholder = "/project/.venv or /path/to/python"
		case 4:
			field.Placeholder = "Pinned application default"
		}
		m.fields = append(m.fields, field)
	}
	if editing {
		m.focus = 1
	}
	return m
}
func (m *Model) Init() tea.Cmd { return m.fields[m.focus].Focus() }
func (m *Model) count() int {
	if m.advanced {
		return len(m.fields)
	}
	return basicFields
}
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	if m.Done || m.Cancelled {
		return m, nil
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		for i := range m.fields {
			m.fields[i].SetWidth(max(1, min(100, m.width-8)))
		}
		return m, nil
	case validatedMsg:
		if v.owner != m || v.generation != m.generation {
			return m, nil
		}
		m.validating = false
		if v.err != nil {
			m.Err = v.err
			return m, m.fields[m.focus].Focus()
		}
		m.Target = v.target
		m.Err = nil
		m.review = true
		return m, nil
	case tea.KeyPressMsg:
		key := v.String()
		if key == "ctrl+c" {
			m.Cancelled = true
			m.generation++
			return m, nil
		}
		if key == "esc" {
			m.generation++
			if m.review || m.validating {
				m.review = false
				m.validating = false
				return m, m.fields[m.focus].Focus()
			}
			m.Cancelled = true
			return m, nil
		}
		if m.validating {
			return m, nil
		}
		if m.review {
			switch key {
			case "enter":
				m.Done = true
				return m, nil
			case "shift+tab", "left", "backspace":
				m.review = false
				return m, m.fields[m.focus].Focus()
			}
			return m, nil
		}
		if key == "ctrl+o" {
			m.advanced = !m.advanced
			if m.focus >= m.count() {
				m.fields[m.focus].Blur()
				m.focus = m.count() - 1
			}
			return m, m.fields[m.focus].Focus()
		}
		if key == "tab" || key == "enter" || key == "shift+tab" {
			m.fields[m.focus].Blur()
			if key == "shift+tab" {
				m.focus--
				if m.focus < 0 || (m.editing && m.focus == 0) {
					m.focus = m.count() - 1
				}
			} else {
				m.focus++
			}
			if m.focus == m.count() {
				m.focus = m.count() - 1
				return m, m.validate()
			}
			return m, m.fields[m.focus].Focus()
		}
	}
	if !m.review && !m.validating {
		var cmd tea.Cmd
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}
func (m *Model) validate() tea.Cmd {
	m.validating = true
	m.Err = nil
	m.generation++
	generation := m.generation
	owner := m
	draft := m.Target
	values := make([]string, len(m.fields))
	for i, f := range m.fields {
		values[i] = strings.TrimSpace(f.Value())
	}
	return func() tea.Msg {
		draft.ID = values[0]
		draft.Name = values[1]
		draft.TrackingURI = values[2]
		draft.Python = values[3]
		draft.MLflowVersion = values[4]
		draft.WorkingDir = values[5]
		draft.ArtifactsDestination = values[6]
		draft.WebURL = values[7]
		draft.TokenEnv = values[8]
		draft.UsernameEnv = values[9]
		draft.PasswordEnv = values[10]
		draft.CAFile = values[11]
		draft.ExtraPackages = nil
		for _, p := range strings.Split(values[12], ",") {
			if p = strings.TrimSpace(p); p != "" {
				draft.ExtraPackages = append(draft.ExtraPackages, p)
			}
		}
		draft.Env = nil
		for _, item := range strings.Split(values[13], ",") {
			if item = strings.TrimSpace(item); item == "" {
				continue
			}
			name, source, ok := strings.Cut(item, "=")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(source) == "" {
				return validatedMsg{owner: owner, generation: generation, err: errors.New("environment references require NAME=SOURCE")}
			}
			if draft.Env == nil {
				draft.Env = map[string]string{}
			}
			draft.Env[strings.TrimSpace(name)] = strings.TrimSpace(source)
		}
		draft, err := config.NormalizeTarget(draft, "")
		return validatedMsg{owner, generation, draft, err}
	}
}

// Reject lets a host report an application-level validation error, such as an
// already-used target ID, while retaining every field for correction.
func (m *Model) Reject(err error) tea.Cmd {
	m.Done = false
	m.review = false
	m.validating = false
	m.Err = err
	m.focus = 0
	if m.editing {
		m.focus = 1
	}
	return m.fields[m.focus].Focus()
}

func (m *Model) View(width, height int) string {
	if m.Done || m.Cancelled {
		return ""
	}
	width = max(1, width)
	height = max(1, height)
	var lines []string
	if m.review {
		lines = []string{"Review tracking target", "Save to: " + safe(m.path), "ID: " + safe(m.Target.ID), "Name: " + safe(m.Target.Label()), "Tracking URI: " + safe(config.RedactURI(m.Target.TrackingURI))}
		if m.Target.Python != "" {
			lines = append(lines, "Existing Python / venv: "+safe(m.Target.Python))
		} else {
			version := m.Target.MLflowVersion
			if version == "" {
				version = "application default"
			}
			lines = append(lines, "Managed runtime: MLflow "+safe(version)+" (local stores only)")
		}
		if m.Target.WorkingDir != "" {
			lines = append(lines, "Working directory: "+safe(m.Target.WorkingDir))
		}
		if m.Target.ArtifactsDestination != "" {
			lines = append(lines, "Artifacts: "+safe(config.RedactURI(m.Target.ArtifactsDestination)))
		}
		if m.advanced {
			lines = append(lines, "Advanced runtime / authentication references included.")
		}
		lines = append(lines, "Enter save · Esc edit · Ctrl+C cancel")
	} else {
		mode := "basic"
		if m.advanced {
			mode = "advanced"
		}
		lines = append(lines, "Configure tracking target · "+mode)
		if m.editing {
			lines = append(lines, "ID: "+safe(m.Target.ID)+" (fixed)")
		}
		start := 0
		if m.editing {
			start = 1
		}
		capacity := max(1, (height-4)/2)
		if m.focus >= start+capacity {
			start = m.focus - capacity + 1
		}
		for i := start; i < min(m.count(), start+capacity); i++ {
			marker := "  "
			if i == m.focus {
				marker = "> "
			}
			lines = append(lines, marker+labels[i], m.fields[i].View())
		}
		if m.validating {
			lines = append(lines, "Validating target…")
		}
		if m.Err != nil {
			lines = append(lines, "Error: "+safe(m.Err.Error()))
		}
		lines = append(lines, fmt.Sprintf("Field %d/%d · Tab/Enter next · Shift+Tab previous", m.focus+1, m.count()), "Ctrl+O basic/advanced · Esc cancel")
	}
	if len(lines) > height {
		if m.review {
			lines = append(lines[:max(0, height-1)], "Enter save · Esc edit")
		} else {
			lines = lines[:height]
		}
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}
func safe(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}
