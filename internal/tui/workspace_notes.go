package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

type journalLoadedMsg struct {
	gen     uint64
	subject core.Subject
	notes   []core.Note
	err     error
}
type journalSavedMsg struct {
	gen     uint64
	subject core.Subject
	note    core.Note
	err     error
}
type editorPreparedMsg struct {
	gen  uint64
	path string
	argv []string
	err  error
}
type editorFinishedMsg struct {
	gen  uint64
	body string
	err  error
}

func copyWorkspaceText(ctx context.Context, value string) error { return platform.Copy(ctx, value) }

func (m *model) openNotes(subject core.Subject) tea.Cmd {
	w := m.work
	w.modal = "notes"
	w.subject = subject
	w.notes = nil
	w.noteIndex = 0
	w.noteOffset = 0
	w.noteErr = ""
	w.query = ""
	w.typing = false
	w.showDeleted = false
	m.cancelUnusedHistories()
	return m.loadJournal()
}
func (m *model) loadJournal() tea.Cmd {
	w := m.work
	store := m.noteStore()
	if store == nil {
		w.noteErr = "Local journal storage unavailable"
		return nil
	}
	ctx, gen := m.workspaceContext("notes")
	w.noteGen = gen
	w.notePending = true
	subject, deleted := w.subject, w.showDeleted
	return func() tea.Msg {
		notes, err := store.ListNotes(ctx, subject, deleted)
		return journalLoadedMsg{gen, subject, notes, err}
	}
}
func (m *model) acceptJournal(v journalLoadedMsg) tea.Cmd {
	w := m.work
	if v.gen != w.noteGen || !subjectEqual(v.subject, w.subject) {
		return nil
	}
	w.notePending = false
	if v.err != nil {
		w.noteErr = v.err.Error()
		return nil
	}
	w.notes = v.notes
	w.noteErr = ""
	w.noteIndex = clamp(w.noteIndex, 0, len(m.visibleNotes())-1)
	return nil
}
func (m *model) visibleNotes() []core.Note {
	w := m.work
	out := make([]core.Note, 0, len(w.notes))
	q := strings.ToLower(w.query)
	for _, n := range w.notes {
		if strings.Contains(strings.ToLower(n.Body), q) {
			out = append(out, n)
		}
	}
	return out
}
func (m *model) selectedNote() *core.Note {
	rows := m.visibleNotes()
	if len(rows) == 0 {
		return nil
	}
	n := rows[clamp(m.work.noteIndex, 0, len(rows)-1)]
	return &n
}
func (m *model) editJournal(note *core.Note, body string) tea.Cmd {
	w := m.work
	if w.notePending {
		return nil
	}
	w.draft = core.Note{Subject: w.subject, Body: body}
	if note != nil {
		w.draft = *note
	}
	w.original = w.draft.Body
	w.editor.SetValue(w.draft.Body)
	w.modal = "edit"
	w.noteErr = ""
	w.typing = false
	w.external = false
	m.resizeWorkspace(m.width, m.height)
	return w.editor.Focus()
}
func (m *model) saveJournal() tea.Cmd {
	w := m.work
	if w.notePending || w.external {
		return nil
	}
	store := m.noteStore()
	if store == nil {
		w.noteErr = "Local journal storage unavailable"
		return nil
	}
	if strings.TrimSpace(w.editor.Value()) == "" {
		w.noteErr = "A note cannot be empty"
		return nil
	}
	w.draft.Body = w.editor.Value()
	ctx, gen := m.workspaceContext("notes-write")
	w.noteGen = gen
	w.notePending = true
	w.noteErr = ""
	note := w.draft
	expected := note.Revision
	return func() tea.Msg {
		n, err := store.SaveNote(ctx, note, expected)
		return journalSavedMsg{gen, note.Subject, n, err}
	}
}
func (m *model) deleteJournal(restore bool) tea.Cmd {
	w := m.work
	if w.notePending {
		return nil
	}
	n := m.selectedNote()
	store := m.noteStore()
	if n == nil || store == nil {
		return nil
	}
	ctx, gen := m.workspaceContext("notes-write")
	w.noteGen = gen
	w.notePending = true
	return func() tea.Msg {
		note, err := store.DeleteNote(ctx, n.Subject, n.ID, n.Revision, restore)
		return journalSavedMsg{gen, n.Subject, note, err}
	}
}
func (m *model) acceptJournalSave(v journalSavedMsg) tea.Cmd {
	w := m.work
	if v.gen != w.noteGen || !subjectEqual(v.subject, w.subject) {
		return nil
	}
	w.notePending = false
	if v.err != nil {
		w.noteErr = v.err.Error()
		m.status = "Note not saved; draft retained"
		return nil
	}
	w.modal = "notes"
	w.editor.Blur()
	w.noteErr = ""
	w.noteOffset = 0
	w.original = ""
	m.status = "Local note saved · MLflow unchanged"
	return m.loadJournal()
}

func (m *model) workspaceModalKey(msg tea.Msg, key string) tea.Cmd {
	w := m.work
	if w.typing {
		return m.workspaceSearchKey(msg, key)
	}
	if w.modal == "discard" {
		switch key {
		case "d":
			w.editor.Blur()
			if w.discardQuit {
				m.stopAll()
				return tea.Quit
			}
			w.modal = "notes"
			w.noteErr = ""
		case "esc", "k":
			w.modal = "edit"
			w.discardQuit = false
			return w.editor.Focus()
		case "ctrl+s":
			w.modal = "edit"
			w.discardQuit = false
			return m.saveJournal()
		}
		return nil
	}
	if w.modal == "edit" {
		if w.notePending || w.external {
			return nil
		}
		switch key {
		case "ctrl+s":
			return m.saveJournal()
		case "ctrl+e":
			return m.openNoteEditor()
		case "esc", "ctrl+c":
			if w.editor.Value() != w.original {
				w.modal = "discard"
				w.discardQuit = key == "ctrl+c"
				return nil
			}
			w.editor.Blur()
			w.modal = "notes"
			return nil
		}
		var cmd tea.Cmd
		w.editor, cmd = w.editor.Update(msg)
		return cmd
	}
	if key == "ctrl+c" {
		m.stopAll()
		return tea.Quit
	}
	if w.modal == "notes" {
		switch key {
		case "esc":
			if !w.notePending {
				m.closeWorkspaceModal()
				return m.ensureDetails()
			}
			return nil
		case "c", "n":
			return m.editJournal(nil, "")
		case "e", "enter":
			if n := m.selectedNote(); n != nil && n.DeletedAt == 0 {
				return m.editJournal(n, "")
			}
		case "d":
			return m.deleteJournal(false)
		case "u":
			return m.deleteJournal(true)
		case "D":
			if !w.notePending {
				w.showDeleted = !w.showDeleted
				return m.loadJournal()
			}
		case "r":
			if !w.notePending {
				return m.loadJournal()
			}
		case "/":
			return m.startWorkspaceSearch(w.query)
		case "j", "down":
			w.noteIndex = clamp(w.noteIndex+1, 0, len(m.visibleNotes())-1)
			w.noteOffset = 0
		case "k", "up":
			w.noteIndex = clamp(w.noteIndex-1, 0, len(m.visibleNotes())-1)
			w.noteOffset = 0
		case "home", "g":
			w.noteIndex = 0
			w.noteOffset = 0
		case "end", "G":
			w.noteIndex = max(0, len(m.visibleNotes())-1)
			w.noteOffset = 0
		case "pgdown", "ctrl+d":
			w.noteOffset += 8
		case "pgup", "ctrl+u":
			w.noteOffset = max(0, w.noteOffset-8)
		case "y":
			if n := m.selectedNote(); n != nil {
				return m.copyWorkspace(n.Body, "Note")
			}
		}
		return nil
	}
	return m.reportModalKey(msg, key)
}

func (m *model) workspaceModalLines(width, height int) []string {
	w := m.work
	switch w.modal {
	case "edit":
		state := "Markdown · local only"
		if w.notePending {
			state = "Saving…"
		}
		if w.external {
			state = "Opening editor…"
		}
		out := []string{state}
		out = append(out, strings.Split(w.editor.View(), "\n")...)
		out = append(out, clean(w.noteErr), "[Ctrl+S Save]  [Ctrl+E Editor]  [Esc Cancel]")
		return out
	case "discard":
		return []string{"This note has unsaved changes.", "", "Ctrl+S save · k / Esc keep editing · d discard"}
	case "notes":
		out := []string{fmt.Sprintf("%d entries · local SQLite · %s", len(m.visibleNotes()), map[bool]string{true: "including deleted", false: "active"}[w.showDeleted])}
		if w.typing {
			out = append(out, w.search.View())
		} else if w.query != "" {
			out = append(out, "Search: "+clean(w.query))
		}
		if w.notePending {
			out = append(out, "Loading / saving…")
		}
		if w.noteErr != "" {
			out = append(out, "Error: "+clean(w.noteErr))
		}
		rows := m.visibleNotes()
		capacity := max(1, min(5, height/3))
		start := listStart(w.noteIndex, len(rows), capacity)
		for i := start; i < min(len(rows), start+capacity); i++ {
			n := rows[i]
			tag := ""
			if n.DeletedAt != 0 {
				tag = " [deleted]"
			}
			title := strings.SplitN(n.Body, "\n", 2)[0]
			out = append(out, row(time.UnixMilli(n.CreatedAt).Local().Format("01-02 15:04")+tag+" · "+title, i == w.noteIndex, width))
		}
		if len(rows) == 0 {
			out = append(out, "No matching notes. c adds a note.")
		}
		out = append(out, strings.Repeat("─", max(0, width)))
		if n := m.selectedNote(); n != nil {
			body := workspaceTextLines(n.Body, width)
			available := max(1, height-len(out)-2)
			start := clamp(w.noteOffset, 0, max(0, len(body)-available))
			out = append(out, body[start:min(len(body), start+available)]...)
		}
		for len(out) < height-1 {
			out = append(out, "")
		}
		out = append(out, "c new · e edit · d delete · u restore · D deleted · / search · y copy")
		return out
	}
	return m.reportModalLines(width, height)
}

func (m *model) openNoteEditor() tea.Cmd {
	w := m.work
	if w.notePending || w.external {
		return nil
	}
	value := os.Getenv("VISUAL")
	if value == "" {
		value = os.Getenv("EDITOR")
	}
	if value == "" {
		value = "vi"
	}
	argv, err := editorArguments(value)
	if err != nil {
		w.noteErr = err.Error()
		return nil
	}
	w.external = true
	gen, body := w.noteGen, w.editor.Value()
	return func() tea.Msg {
		f, err := os.CreateTemp("", "lazymlflow-note-*.md")
		if err != nil {
			return editorPreparedMsg{gen: gen, err: err}
		}
		path := f.Name()
		_, err = f.WriteString(body)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(path)
			return editorPreparedMsg{gen: gen, err: err}
		}
		return editorPreparedMsg{gen, path, argv, nil}
	}
}
func (m *model) acceptEditorPrepared(v editorPreparedMsg) tea.Cmd {
	w := m.work
	if v.gen != w.noteGen || w.modal != "edit" {
		if v.path != "" {
			return func() tea.Msg { os.Remove(v.path); return nil }
		}
		return nil
	}
	if v.err != nil {
		w.external = false
		w.noteErr = v.err.Error()
		return nil
	}
	args := append(append([]string(nil), v.argv[1:]...), v.path)
	cmd := exec.CommandContext(m.ctx, v.argv[0], args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(v.path)
		if err != nil {
			return editorFinishedMsg{gen: v.gen, err: err}
		}
		data, readErr := os.ReadFile(v.path)
		if len(data) > 4*1024*1024 {
			readErr = errors.New("note exceeds 4 MiB")
		}
		return editorFinishedMsg{v.gen, string(data), readErr}
	})
}
func (m *model) acceptEditorFinished(v editorFinishedMsg) tea.Cmd {
	w := m.work
	if v.gen != w.noteGen || w.modal != "edit" {
		return nil
	}
	w.external = false
	if v.err != nil {
		w.noteErr = "Editor failed; original draft kept: " + v.err.Error()
	} else {
		w.editor.SetValue(v.body)
		w.noteErr = "Editor returned; Ctrl+S saves locally"
	}
	return w.editor.Focus()
}

// Parse common editor arguments without invoking a shell or expanding code.
func editorArguments(value string) ([]string, error) {
	var args []string
	var part strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range value {
		if escaped {
			part.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				part.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, part.String())
				part.Reset()
				started = false
			}
			continue
		}
		part.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("invalid quotes in VISUAL / EDITOR")
	}
	if started {
		args = append(args, part.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("empty editor command")
	}
	return args, nil
}
