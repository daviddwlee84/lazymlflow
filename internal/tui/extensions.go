package tui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/connection"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/models"
	"github.com/daviddwlee84/lazymlflow/internal/server"
	"github.com/daviddwlee84/lazymlflow/internal/serverform"
)

type modelChoice struct{ label, source, name string }
type extensionState struct {
	mode, title, body, err                  string
	form                                    *serverform.Model
	setupDraft                              *serverform.Model
	rows                                    []modelChoice
	index, offset                           int
	kind, name, next, target, returnOverlay string
	rowsKind, rowsName                      string
	gen                                     uint64
	pending                                 bool
	input                                   textinput.Model
	inputKind                               string
	review                                  bool
	inspection                              *models.Inspection
	service                                 *models.Service
	pressed                                 int
}
type modelRowsMsg struct {
	gen                      uint64
	target, kind, name, next string
	rows                     []modelChoice
	append                   bool
	err                      error
}
type modelInspectionMsg struct {
	gen        uint64
	target     string
	inspection models.Inspection
	err        error
}
type modelExportMsg struct {
	gen    uint64
	target string
	result models.ExportResult
	err    error
}
type setupResultMsg struct {
	gen    uint64
	stack  *server.Stack
	target *core.Target
	err    error
}

func (m *model) newExtension(mode string) *extensionState {
	e := &extensionState{mode: mode, target: m.active, returnOverlay: m.overlay, pressed: -1}
	e.input = textinput.New()
	e.input.Prompt = "> "
	e.input.CharLimit = 4096
	e.input.SetWidth(max(1, m.width-6))
	m.extensions = e
	return e
}
func (m *model) closeExtension() {
	if c := m.cancel["extension"]; c != nil {
		c()
	}
	if m.extensions != nil {
		if m.extensions.pending {
			m.status = "Cancelled " + m.extensions.title + "; any completed file or service changes remain."
		}
		m.overlay = m.extensions.returnOverlay
	}
	m.extensions = nil
}
func (m *model) openServerSetup() tea.Cmd {
	e := m.newExtension("setup")
	e.form = serverform.New("", server.DefaultSpec())
	e.form.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	return e.form.Init()
}
func (m *model) openTargetEnvironment(t core.Target) tea.Cmd {
	e := m.newExtension("text")
	e.title = "Experiment environment · " + t.Label()
	if t.SSHHost != "" {
		e.body = "This target needs a live SSH tunnel. Run training under:\n\nlazymlflow targets exec " + shellWord(t.ID) + " -- python train.py\n\nThe tunnel remains available until the command exits."
		return nil
	}
	p, err := connection.ClientEnvironmentPlan(t)
	if err != nil {
		e.err = err.Error()
		return nil
	}
	e.body = p.RenderSH()
	return nil
}
func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (m *model) modelService() (*models.Service, error) {
	s := m.state()
	if s == nil || s.Session == nil {
		return nil, fmt.Errorf("connect a target first")
	}
	svc, err := models.New(s.Session.Backend)
	if err == nil {
		svc.SourceKey = core.SourceKey(m.target())
	}
	return svc, err
}
func (m *model) openModels(kind, name string) tea.Cmd {
	svc, err := m.modelService()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	e := m.newExtension("models")
	e.service = svc
	e.kind = kind
	e.name = name
	return m.loadModelRows(false)
}
func (m *model) openModelSource() tea.Cmd {
	svc, err := m.modelService()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	e := m.newExtension("models")
	e.service = svc
	e.inputKind = "source"
	e.title = "Inspect model or artifact URI"
	if r := m.run(); r != nil {
		e.input.SetValue("runs:/" + r.ID() + "/" + m.artifactPath[m.active+"\x00"+r.ID()])
	}
	return e.input.Focus()
}
func (m *model) loadModelRows(more bool) tea.Cmd {
	e := m.extensions
	ctx, gen := m.operation("extension")
	e.gen = gen
	e.pending = true
	e.err = ""
	e.mode = "models"
	e.inspection = nil
	if !more {
		e.index = 0
		e.offset = 0
	}
	kind, name, target, svc := e.kind, e.name, e.target, e.service
	if e.rowsKind != kind || e.rowsName != name {
		e.rows = nil
		e.index = 0
	}
	token := ""
	if more {
		token = e.next
	}
	e.title = map[string]string{"registry": "Registered models", "versions": "Versions · " + name, "related": "Models used or produced by run " + name}[kind]
	return func() tea.Msg {
		out := modelRowsMsg{gen: gen, target: target, kind: kind, name: name, append: more, rows: []modelChoice{}}
		q := models.Query{MaxResults: 100, PageToken: token}
		switch kind {
		case "registry":
			p, err := svc.List(ctx, q, false)
			out.err = err
			out.next = p.NextPageToken
			for _, v := range p.Models {
				out.rows = append(out.rows, modelChoice{label: v.Name, name: v.Name})
			}
		case "versions":
			p, err := svc.Versions(ctx, name, q, false)
			out.err = err
			out.next = p.NextPageToken
			for _, v := range p.Versions {
				out.rows = append(out.rows, modelChoice{label: fmt.Sprintf("v%s · %s · %s", v.Version, v.Status, strings.Join(v.Aliases, ", ")), source: "models:/" + url.PathEscape(v.Name) + "/" + v.Version})
			}
		case "related":
			p, err := svc.Related(ctx, name)
			out.err = err
			for _, v := range p.Models {
				label := v.Role + " · " + v.Source
				if v.Step != nil {
					label += fmt.Sprintf(" · step %d", *v.Step)
				}
				if v.Error != "" {
					label += " · " + v.Error
				}
				out.rows = append(out.rows, modelChoice{label: label, source: v.Source})
			}
		}
		return out
	}
}
func (m *model) inspectModel(source string) tea.Cmd {
	e := m.extensions
	ctx, gen := m.operation("extension")
	e.gen = gen
	e.pending = true
	e.err = ""
	e.mode = "model-inspect"
	e.inputKind = ""
	e.input.Blur()
	e.inspection = nil
	e.offset = 0
	e.title = "Model inspection · " + source
	target, svc := e.target, e.service
	return func() tea.Msg { i, err := svc.Inspect(ctx, source); return modelInspectionMsg{gen, target, i, err} }
}
func (m *model) exportModel() tea.Cmd {
	e := m.extensions
	if e.inspection == nil {
		return nil
	}
	dest := strings.TrimSpace(e.input.Value())
	if dest == "" {
		e.err = "Choose an output directory"
		return nil
	}
	ctx, gen := m.operation("extension")
	e.gen = gen
	e.pending = true
	e.err = ""
	e.review = false
	e.inputKind = ""
	e.input.Blur()
	svc, target := e.service, e.target
	// The reviewed immutable reference is used, never a re-resolved alias.
	source := e.inspection.Resolution.ResolvedURI
	return func() tea.Msg {
		r, err := svc.Export(ctx, models.ExportOptions{Source: source, Destination: dest}, nil)
		return modelExportMsg{gen, target, r, err}
	}
}
func (m *model) applyServerSetup(r serverform.Result) tea.Cmd {
	e := m.extensions
	if r.RegisterTarget {
		if m.opts.SaveTargets == nil {
			e.form.Done = false
			e.form.Err = fmt.Errorf("target saving unavailable")
			return nil
		}
		for _, t := range m.targets {
			if t.ID == r.Spec.ID {
				e.form.Done = false
				e.form.Err = fmt.Errorf("target ID %q already exists", t.ID)
				return nil
			}
		}
	}
	ctx, gen := m.operation("extension")
	e.gen = gen
	e.setupDraft = e.form
	e.form = nil
	e.mode = "text"
	e.title = "Creating server project"
	e.body = "Requested directory: " + r.Directory
	e.pending = true
	targets := append([]core.Target(nil), m.targets...)
	save := m.opts.SaveTargets
	defaultID := m.opts.InitialTarget
	return func() tea.Msg {
		s, err := server.Init(ctx, r.Directory, r.Spec, server.InitOptions{AdminPasswordEnv: r.AdminPasswordEnv})
		out := setupResultMsg{gen: gen, stack: s, err: err}
		if err != nil {
			return out
		}
		if r.Start {
			if err := server.NewManager().Up(ctx, s); err != nil {
				out.err = err
				return out
			}
		}
		if r.RegisterTarget {
			t := s.Target()
			targets = append(targets, t)
			if err := save(targets, defaultID); err != nil {
				out.err = fmt.Errorf("stack created; could not save target: %w", err)
				return out
			}
			out.target = &t
		}
		return out
	}
}
func (m *model) updateExtensions(msg tea.Msg) (tea.Cmd, bool) {
	e := m.extensions
	switch v := msg.(type) {
	case setupResultMsg:
		if v.target != nil {
			found := false
			for _, t := range m.targets {
				if t.ID == v.target.ID {
					found = true
				}
			}
			if !found {
				m.targets = append(m.targets, *v.target)
				m.states[v.target.ID] = newTargetState()
			}
		}
		if e == nil || e.gen != v.gen {
			if v.stack != nil {
				m.status = "Server project created at " + v.stack.Dir
			}
			return nil, true
		}
		e.pending = false
		if v.err != nil && v.stack == nil && e.setupDraft != nil {
			e.mode = "setup"
			e.form = e.setupDraft
			e.form.Done = false
			e.form.Err = v.err
			return nil, true
		}
		e.title = "Server project"
		if v.stack != nil {
			e.body = "Directory: " + v.stack.Dir + "\nEndpoint: " + v.stack.TrackingURI + "\n\nlazymlflow server up --dir " + shellWord(v.stack.Dir) + "\nlazymlflow server status --dir " + shellWord(v.stack.Dir) + "\n"
			if v.stack.Spec.Auth == "native" {
				e.body += "\nBootstrap password file: " + v.stack.AdminPasswordPath() + "\nUse MLflow /admin to create training accounts and role grants."
			}
		}
		if v.err != nil {
			e.err = v.err.Error()
		}
		if v.target != nil {
			e.body += "\n\nConnection target saved: " + v.target.ID
		}
		return nil, true
	case modelRowsMsg:
		if e == nil || v.gen != e.gen || v.target != m.active {
			return nil, true
		}
		e.pending = false
		e.pressed = -1
		if v.err == nil || len(v.rows) > 0 {
			e.next = v.next
			e.rowsKind = v.kind
			e.rowsName = v.name
			if v.append {
				e.rows = append(e.rows, v.rows...)
			} else {
				e.rows = v.rows
			}
		}
		e.index = clamp(e.index, 0, max(0, len(e.rows)-1))
		if v.err != nil {
			e.err = v.err.Error()
		}
		return nil, true
	case modelInspectionMsg:
		if e == nil || v.gen != e.gen || v.target != m.active || e.mode != "model-inspect" {
			return nil, true
		}
		e.pending = false
		if v.err != nil {
			e.err = v.err.Error()
		} else {
			e.inspection = &v.inspection
			b, _ := json.MarshalIndent(v.inspection, "", "  ")
			e.body = string(b)
		}
		return nil, true
	case modelExportMsg:
		if e == nil || v.gen != e.gen || v.target != m.active {
			return nil, true
		}
		e.pending = false
		if v.err != nil {
			e.err = v.err.Error()
		} else {
			e.err = ""
			m.status = fmt.Sprintf("Exported %d files to %s", v.result.Files, v.result.Path)
			e.body = "Bundle: " + v.result.Path + "\nManifest: " + v.result.Manifest + "\n\n" + e.body
		}
		return nil, true
	}
	if e == nil {
		return nil, false
	}
	if v, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		e.input.SetWidth(max(1, m.width-6))
		e.pressed = -1
		if e.form != nil {
			var cmd tea.Cmd
			e.form, cmd = e.form.Update(msg)
			return cmd, true
		}
		return nil, true
	}
	if e.form != nil {
		if !m.layout.Mouse {
			switch msg.(type) {
			case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
				return nil, true
			}
		}
		var cmd tea.Cmd
		e.form, cmd = e.form.Update(msg)
		if e.form.Cancelled {
			m.closeExtension()
			m.status = "Server setup cancelled"
			return nil, true
		}
		if e.form.Done {
			return m.applyServerSetup(e.form.Result), true
		}
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
			return cmd, true
		}
		return cmd, cmd != nil
	}
	switch v := msg.(type) {
	case tea.KeyPressMsg:
		k := v.String()
		e.pressed = -1
		if k == "ctrl+c" || k == "ctrl+x" {
			if e.pending {
				if c := m.cancel["extension"]; c != nil {
					c()
				}
				e.gen++
				e.pending = false
				e.err = "Operation cancelled; any created files or started services remain visible on disk."
				return nil, true
			}
			m.closeExtension()
			return nil, true
		}
		if k == "esc" {
			if e.inputKind != "" {
				e.inputKind = ""
				e.review = false
				e.input.Blur()
				return nil, true
			}
			if e.pending {
				e.err = "Operation running; Ctrl+X cancels. Esc closes this view."
			}
			m.closeExtension()
			return nil, true
		}
		if e.inputKind != "" {
			if k == "enter" {
				if e.inputKind == "source" {
					return m.inspectModel(strings.TrimSpace(e.input.Value())), true
				}
				if !e.review {
					e.review = true
					return nil, true
				}
				return m.exportModel(), true
			}
			if e.review {
				if k == "backspace" {
					e.review = false
				}
				return nil, true
			}
			var cmd tea.Cmd
			e.input, cmd = e.input.Update(msg)
			return cmd, true
		}
		switch k {
		case "q":
			m.closeExtension()
		case "up", "k":
			if e.mode == "models" {
				e.index = max(0, e.index-1)
			} else {
				e.offset = max(0, e.offset-1)
			}
		case "down", "j":
			if e.mode == "models" {
				e.index = min(max(0, len(e.rows)-1), e.index+1)
			} else {
				e.offset++
			}
		case "pgdown", "ctrl+d":
			if e.mode == "models" {
				e.index = min(max(0, len(e.rows)-1), e.index+10)
			} else {
				e.offset += 10
			}
		case "pgup", "ctrl+u":
			if e.mode == "models" {
				e.index = max(0, e.index-10)
			} else {
				e.offset = max(0, e.offset-10)
			}
		case "enter":
			if e.mode == "models" && len(e.rows) > 0 && !e.pending {
				r := e.rows[e.index]
				if r.name != "" {
					e.kind = "versions"
					e.name = r.name
					return m.loadModelRows(false), true
				}
				return m.inspectModel(r.source), true
			}
		case "n":
			if e.mode == "models" && e.next != "" && !e.pending {
				return m.loadModelRows(true), true
			}
		case "r":
			if e.mode == "models" && !e.pending {
				return m.loadModelRows(false), true
			}
		case "b":
			if e.mode == "model-inspect" && e.kind != "" {
				if c := m.cancel["extension"]; c != nil {
					c()
				}
				m.seq++
				e.gen = m.seq
				e.pending = false
				e.mode = "models"
				e.inspection = nil
				e.body = ""
				e.offset = 0
			} else if e.kind == "versions" && !e.pending {
				e.kind = "registry"
				e.name = ""
				return m.loadModelRows(false), true
			}
		case "i":
			if e.service != nil && !e.pending {
				e.inputKind = "source"
				e.input.SetValue("")
				return e.input.Focus(), true
			}
		case "e":
			if e.inspection != nil && !e.pending {
				e.inputKind = "export"
				e.review = false
				e.input.SetValue("./model-bundle")
				return e.input.Focus(), true
			}
		case "y":
			if e.body != "" {
				return m.copyWorkspace(e.body, e.title), true
			}
		}
		return nil, true
	case tea.MouseClickMsg:
		e.pressed = -1
		if m.layout.Mouse && v.Button == tea.MouseLeft && e.mode == "models" && e.inputKind == "" {
			e.pressed = m.extensionRowAt(v.X, v.Y)
		}
		return nil, true
	case tea.MouseReleaseMsg:
		if m.layout.Mouse && v.Button == tea.MouseLeft && e.pressed >= 0 {
			index := m.extensionRowAt(v.X, v.Y)
			if index == e.pressed {
				e.index = index
			}
		}
		e.pressed = -1
		return nil, true
	case tea.MouseMotionMsg:
		return nil, true
	case tea.MouseWheelMsg:
		if m.layout.Mouse {
			delta := 1
			if v.Button == tea.MouseWheelUp {
				delta = -1
			}
			if e.mode == "models" {
				e.index = clamp(e.index+delta, 0, max(0, len(e.rows)-1))
			} else {
				e.offset = max(0, e.offset+delta)
			}
		}
		return nil, true
	case refreshMsg:
		return m.tick(), true
	}
	if e.inputKind != "" {
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(msg)
		return cmd, false
	}
	return nil, false
}
func (m *model) extensionRowAt(x, y int) int {
	e := m.extensions
	if e == nil || e.mode != "models" || m.width < 16 || m.height < 5 || x < 1 || x >= m.width-1 || y < 1 || y >= m.height-4 {
		return -1
	}
	index := listStart(e.index, len(e.rows), max(1, m.height-5)) + y - 1
	if index < 0 || index >= len(e.rows) {
		return -1
	}
	return index
}
func (m *model) extensionView() (tea.View, bool) {
	e := m.extensions
	if e == nil {
		return tea.View{}, false
	}
	var content string
	if e.form != nil {
		content = e.form.View(m.width, m.height)
	} else {
		w, h := max(1, m.width), max(1, m.height)
		if h < 5 || w < 16 {
			v := tea.NewView(block([]string{e.title, "Resize terminal", "Esc back"}, w, h))
			v.AltScreen = true
			return v, true
		}
		capacity := max(1, h-5)
		var lines []string
		if e.mode == "models" {
			start := listStart(e.index, len(e.rows), capacity)
			for i := start; i < min(len(e.rows), start+capacity); i++ {
				lines = append(lines, row(e.rows[i].label, i == e.index, w-2))
			}
			if len(lines) == 0 && !e.pending {
				lines = []string{"No models returned. Press i to inspect an explicit source URI."}
			}
		} else {
			all := workspaceTextLines(e.body, max(1, w-2))
			start := clamp(e.offset, 0, max(0, len(all)-capacity))
			lines = append(lines, all[start:]...)
		}
		if e.inputKind != "" {
			title := "Model source URI"
			if e.inputKind == "export" {
				title = "Export directory (must not exist)"
			}
			lines = []string{title, e.input.View()}
			if e.review {
				lines = append(lines, "", "Source: "+e.inspection.Resolution.ResolvedURI, "Destination: "+clean(e.input.Value()), "Enter exports · Backspace edits · Esc cancels")
			}
		}
		status := m.status
		if e.pending {
			status = "Working… · Ctrl+X cancels"
		}
		if e.err != "" {
			status = "Error: " + e.err
		}
		footer := "↑↓/jk scroll · y copy · Esc back"
		if e.mode == "models" {
			footer = "↑↓/jk select · Enter inspect · n next page · i source URI · b back · Esc close"
		}
		if e.mode == "model-inspect" {
			footer = "↑↓/jk scroll · e export · y copy · b model list · Esc close"
		}
		if e.inputKind != "" {
			footer = "Type value · Enter continues · Esc cancels"
		}
		hint := "Esc returns to your previous view and selection."
		if strings.HasPrefix(e.mode, "model") {
			hint = "Model inspection reads metadata only; no model code is loaded."
		}
		content = frame(e.title, lines, w, max(1, h-3), true) + "\n" + textFit(status, w) + "\n" + textFit(footer, w) + "\n" + textFit(hint, w)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "lazymlflow"
	if m.layout.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v, true
}
