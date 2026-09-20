package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/inspection"
)

// workspaceState owns the dataset workspace and its local, modal operations.
// It is deliberately separate from remote run/query selection state.
type workspaceState struct {
	catalog                bool
	returnFocus            int
	returnZoom             bool
	catalogs               map[string]*catalogState
	modal                  string
	backModal              string
	subject                core.Subject
	notes                  []core.Note
	noteIndex, noteOffset  int
	showDeleted            bool
	noteGen                uint64
	notePending            bool
	noteErr                string
	draft                  core.Note
	original               string
	editor                 textarea.Model
	search                 textinput.Model
	typing                 bool
	query                  string
	pressed                string
	pressedScope           string
	discardQuit            bool
	external               bool
	report                 inspection.Snapshot
	reportText, promptText string
	reportPrompt           bool
	reportGen              uint64
	reportPending          bool
	reportOffset           int
	reportErr              string
	reportSubjects         []core.Subject
	export                 textinput.Model
	subjectIndex           int
}

func (m *model) initWorkspace() {
	if m.work != nil {
		return
	}
	a := textarea.New()
	a.Placeholder = "Write an observation, result, or next step…"
	a.ShowLineNumbers = false
	a.Prompt = ""
	a.CharLimit = 1024 * 1024
	a.MaxHeight = 0
	a.MaxContentHeight = 1 << 20
	a.SetVirtualCursor(true)
	s := textinput.New()
	s.Prompt = "Search: "
	s.CharLimit = 8192
	e := textinput.New()
	e.Prompt = "Path: "
	e.CharLimit = 8192
	m.work = &workspaceState{catalogs: map[string]*catalogState{}, editor: a, search: s, export: e}
}

func (m *model) workspaceActions() []action {
	return []action{act("dataset-workspace", "B", "Browse datasets across experiments"), act("journal", "N", "Local notes for selection"), act("summary", "S", "Summary / agent context")}
}

func (m *model) performWorkspace(id string) (tea.Cmd, bool) {
	m.initWorkspace()
	switch id {
	case "dataset-workspace":
		return m.openCatalog(), true
	case "journal":
		subject, ok := m.currentSubject()
		if !ok {
			m.status = "Select an experiment, run, or dataset first"
			return nil, true
		}
		return m.openNotes(subject), true
	case "summary":
		return m.openSummary(), true
	}
	return nil, false
}

func (m *model) currentSubject() (core.Subject, bool) {
	source := core.SourceKey(m.target())
	if m.work != nil && m.work.catalog {
		if m.focus == 1 {
			if u := m.catalogUse(); u != nil {
				return core.Subject{Source: source, Kind: "run", ID: u.RunID, Label: u.RunName}, true
			}
		} else if d := m.catalogEntry(); d != nil {
			return core.Subject{Source: source, Kind: "dataset", ID: d.ID, Label: d.Dataset.Name + " · " + d.Dataset.Digest}, true
		}
		return core.Subject{}, false
	}
	if m.focus == 0 {
		if s := m.state(); s != nil && s.Selected != "" {
			label := s.Selected
			for _, e := range s.Experiments {
				if e.ID == s.Selected {
					label = e.Name
					break
				}
			}
			return core.Subject{Source: source, Kind: "experiment", ID: s.Selected, Label: label}, true
		}
	}
	if r := m.run(); r != nil {
		if m.focus == 2 && m.tab == 5 && len(r.Inputs.DatasetInputs) > 0 {
			i := clamp(m.inspectionDatasetIndex(), 0, len(r.Inputs.DatasetInputs)-1)
			d := r.Inputs.DatasetInputs[i].Dataset
			return core.Subject{Source: source, Kind: "dataset", ID: core.DatasetIdentity(source, d), Label: d.Name + " · " + d.Digest}, true
		}
		return core.Subject{Source: source, Kind: "run", ID: r.ID(), Label: r.Name()}, true
	}
	return core.Subject{}, false
}

func (m *model) noteStore() core.NoteStore {
	s, _ := m.opts.State.(core.NoteStore)
	return s
}

func (m *model) updateWorkspace(msg tea.Msg) (tea.Cmd, bool) {
	m.initWorkspace()
	w := m.work
	switch v := msg.(type) {
	case journalLoadedMsg:
		return m.acceptJournal(v), true
	case journalSavedMsg:
		return m.acceptJournalSave(v), true
	case editorPreparedMsg:
		return m.acceptEditorPrepared(v), true
	case editorFinishedMsg:
		return m.acceptEditorFinished(v), true
	case catalogMsg:
		return m.acceptCatalog(v), true
	case catalogRunMsg:
		return m.acceptCatalogRun(v), true
	case reportMsg:
		return m.acceptReport(v), true
	case reportExportedMsg:
		if v.gen != w.reportGen {
			return nil, true
		}
		if v.err != nil {
			m.status = "Export failed: " + v.err.Error()
		} else {
			m.status = "Exported " + v.path
			w.modal = "report"
		}
		return nil, true
	case tea.WindowSizeMsg:
		w.pressed = ""
		m.resizeWorkspace(v.Width, v.Height)
		return nil, false
	}
	active := w.catalog || w.modal != ""
	if !active {
		return nil, false
	}
	if _, ok := msg.(refreshMsg); ok {
		return m.tick(), true
	}
	if v, ok := msg.(tea.KeyPressMsg); ok {
		w.pressed = ""
		key := v.String()
		if w.modal != "" {
			return m.workspaceModalKey(msg, key), true
		}
		return m.catalogKey(msg, key), true
	}
	switch msg.(type) {
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
		return m.workspaceMouse(msg), true
	}
	if !workspaceTextEvent(msg) {
		return nil, false
	}
	if w.modal == "edit" {
		var cmd tea.Cmd
		w.editor, cmd = w.editor.Update(msg)
		return cmd, true
	}
	if w.typing {
		var cmd tea.Cmd
		w.search, cmd = w.search.Update(msg)
		w.query = w.search.Value()
		m.workspaceFilterChanged()
		return cmd, true
	}
	if w.modal == "export" {
		var cmd tea.Cmd
		w.export, cmd = w.export.Update(msg)
		return cmd, true
	}
	return nil, false
}

func workspaceTextEvent(msg tea.Msg) bool {
	if _, ok := msg.(tea.PasteMsg); ok {
		return true
	}
	t := reflect.TypeOf(msg)
	if t == nil {
		return false
	}
	switch t.PkgPath() {
	case "charm.land/bubbles/v2/textarea", "charm.land/bubbles/v2/textinput", "charm.land/bubbles/v2/cursor":
		return true
	}
	return false
}

func (m *model) resizeWorkspace(width, height int) {
	r := workspaceDialog(width, height)
	m.work.editor.SetWidth(max(1, r.W-4))
	m.work.editor.SetHeight(max(1, r.H-6))
	m.work.search.SetWidth(max(1, r.W-12))
	m.work.export.SetWidth(max(1, r.W-10))
}

func workspaceDialog(width, height int) rect {
	w := min(100, max(1, width-4))
	h := min(24, max(1, height-4))
	return rect{max(0, (width-w)/2), max(0, (height-h)/2), w, h}
}

func (m *model) workspaceView() (tea.View, bool) {
	if m.work == nil || (!m.work.catalog && m.work.modal == "") {
		return tea.View{}, false
	}
	w, h := max(1, m.width), max(1, m.height)
	header := focusStyle.Render(textFit("lazymlflow · "+m.target().Label()+" · "+m.workspaceTitle(), w))
	content := m.workspaceBackground()
	status := m.status
	if m.resizing {
		status = "Resize: h/l width · k/j height · Enter save"
	}
	out := header + "\n" + content + "\n" + textFit(status, w) + "\n" + dimStyle.Render(textFit(m.workspaceFooter(), w)) + "\n" + dimStyle.Render(textFit("1/2/3 pane · z zoom · M mouse · B experiments/datasets · N notes · S summary", w))
	if m.work.modal != "" {
		r := workspaceDialog(w, h)
		modal := frame(m.workspaceModalTitle(), m.workspaceModalLines(r.W-2, r.H-2), r.W, r.H, true)
		out = lipgloss.NewCompositor(lipgloss.NewLayer(out), lipgloss.NewLayer(modal).X(r.X).Y(r.Y).Z(1)).Render()
	}
	v := tea.NewView(out)
	v.AltScreen = true
	v.WindowTitle = "lazymlflow"
	if m.layout.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v, true
}

func (m *model) workspaceBackground() string {
	l := m.geometry()
	w, h := m.width, l.Content.H
	titles := [3]string{"1 Experiments", "2 Runs", "3 " + m.detailTitle()}
	lines := func(i, width, height int) []string {
		if m.work.catalog {
			return m.catalogPane(i, width, height).Lines
		}
		switch i {
		case 0:
			return m.experimentLines(width, height)
		case 1:
			return m.runLines(width, height)
		default:
			return m.detailLines(width, height)
		}
	}
	if m.work.catalog {
		titles = [3]string{"1 Datasets · name / version", "2 Related runs", "3 " + m.catalogDetailTitle()}
	}
	if l.Split {
		a, b, c := l.Panes[0], l.Panes[1], l.Panes[2]
		return lipgloss.JoinHorizontal(lipgloss.Top, frame(titles[0], lines(0, a.W-2, a.H-2), a.W, a.H, m.focus == 0), frame(titles[1], lines(1, b.W-2, b.H-2), b.W, b.H, m.focus == 1)+"\n"+frame(titles[2], lines(2, c.W-2, c.H-2), c.W, c.H, m.focus == 2))
	}
	return frame(titles[m.focus], lines(m.focus, w-2, h-2), w, h, true)
}

func (m *model) workspaceTitle() string {
	if m.work.catalog {
		return "Dataset workspace"
	}
	return "Local inspection"
}
func (m *model) workspaceFooter() string {
	if m.work.modal != "" {
		return "Esc back · modal owns keyboard and mouse"
	}
	return "↑↓/jk select · / search · A scan all · Ctrl+X cancel · Enter inspect · [/] detail tabs · V lifecycle · H visibility"
}

func workspaceTextLines(text string, width int) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = clean(line)
		wrapped := lipgloss.NewStyle().Width(max(1, width)).Render(line)
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	return lines
}

func subjectEqual(a, b core.Subject) bool {
	return a.Source == b.Source && a.Kind == b.Kind && a.ID == b.ID
}
func subjectLabel(s core.Subject) string {
	v := s.Label
	if v == "" {
		v = s.ID
	}
	return s.Kind + " · " + v
}
func (m *model) copyWorkspace(value, label string) tea.Cmd {
	return func() tea.Msg {
		err := copyWorkspaceText(m.ctx, value)
		return resultMsg{text: label + " copied", err: err}
	}
}

// Capture a source-bound context before starting every operation.
func (m *model) workspaceContext(kind string) (context.Context, uint64) {
	return m.operation("workspace:" + kind)
}

func (m *model) workspaceFilterChanged() {
	w := m.work
	if w.modal == "notes" {
		w.noteIndex = 0
		w.noteOffset = 0
		return
	}
	if c := m.catalogState(); c != nil {
		if m.focus == 0 {
			c.Query = w.query
			c.Index = 0
			m.rebuildCatalogRows(c)
		} else if m.focus == 1 {
			c.RunQuery = w.query
			c.RunIndex = 0
		} else {
			c.SchemaQuery = w.query
			c.DetailIndex = 0
		}
	}
}

func (m *model) startWorkspaceSearch(value string) tea.Cmd {
	w := m.work
	w.typing = true
	w.query = value
	w.search.SetValue(value)
	w.search.CursorEnd()
	return w.search.Focus()
}

func (m *model) workspaceSearchKey(msg tea.Msg, key string) tea.Cmd {
	w := m.work
	switch key {
	case "enter":
		w.typing = false
		w.search.Blur()
		return nil
	case "esc":
		w.typing = false
		w.query = ""
		w.search.SetValue("")
		w.search.Blur()
		m.workspaceFilterChanged()
		return nil
	case "up", "down":
		d := 1
		if key == "up" {
			d = -1
		}
		if w.modal == "notes" {
			w.noteIndex = clamp(w.noteIndex+d, 0, len(m.visibleNotes())-1)
		} else {
			m.catalogMove(d)
		}
		return nil
	}
	before := w.search.Value()
	var cmd tea.Cmd
	w.search, cmd = w.search.Update(msg)
	w.query = w.search.Value()
	if before != w.query {
		m.workspaceFilterChanged()
	}
	return cmd
}

func (m *model) closeWorkspaceModal() {
	w := m.work
	w.modal = ""
	w.typing = false
	w.query = ""
	w.search.Blur()
	w.pressed = ""
	w.noteGen++
	w.reportGen++
}

func (m *model) workspaceModalTitle() string {
	w := m.work
	switch w.modal {
	case "notes":
		return "Local journal · " + subjectLabel(w.subject)
	case "edit":
		return "Write note · " + subjectLabel(w.subject)
	case "discard":
		return "Unsaved note"
	case "report":
		return "Summary · Markdown / agent prompt"
	case "export":
		return "Export summary"
	case "report-subject":
		return "Save report as note · choose subject"
	case "field":
		return "Dataset field"
	case "workspace-help":
		return "Dataset workspace help"
	}
	return fmt.Sprint(w.modal)
}
