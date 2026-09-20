package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

func journalModel(t *testing.T) (*model, *localstate.Store, core.Subject) {
	t.Helper()
	m, _ := readyModel()
	store := localstate.New(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(func() { store.Close() })
	m.opts.State = store
	m.focus = 1
	subject, ok := m.currentSubject()
	if !ok {
		t.Fatal("missing subject")
	}
	return m, store, subject
}
func deliverWorkspace(t *testing.T, m *model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		return nil
	}
	_, next := m.Update(cmd())
	return next
}

func TestJournalComposeLiteralInputSaveAndRestore(t *testing.T) {
	m, store, subject := journalModel(t)
	deliverWorkspace(t, m, m.openNotes(subject))
	m.editJournal(nil, "")
	for _, text := range []string{"q", "j", "k", "1", "/", "B", "N", "S"} {
		key(m, text)
	}
	m.Update(tea.PasteMsg{Content: "\n訓練觀察 é 👩🏽‍💻\nvalid_loss rose after step 3"})
	if !strings.HasPrefix(m.work.editor.Value(), "qjk1/BNS\n") || m.work.modal != "edit" || m.work.catalog {
		t.Fatalf("typing escaped modal: %q", m.work.editor.Value())
	}
	before := m.work.editor.Value()
	save := m.saveJournal()
	if !m.work.notePending {
		t.Fatal("missing save pending")
	}
	m.workspaceModalKey(nil, "esc")
	if m.work.modal != "edit" {
		t.Fatal("left a pending write")
	}
	next := deliverWorkspace(t, m, save)
	deliverWorkspace(t, m, next)
	notes, err := store.ListNotes(context.Background(), subject, false)
	if err != nil || len(notes) != 1 || notes[0].Body != before {
		t.Fatalf("saved notes: %#v %v", notes, err)
	}
	if m.work.modal != "notes" || m.work.notePending {
		t.Fatal("save acknowledgement missing")
	}
	next = deliverWorkspace(t, m, m.deleteJournal(false))
	deliverWorkspace(t, m, next)
	notes, _ = store.ListNotes(context.Background(), subject, false)
	if len(notes) != 0 {
		t.Fatal("delete failed")
	}
	m.work.showDeleted = true
	deliverWorkspace(t, m, m.loadJournal())
	next = deliverWorkspace(t, m, m.deleteJournal(true))
	deliverWorkspace(t, m, next)
	notes, _ = store.ListNotes(context.Background(), subject, false)
	if len(notes) != 1 {
		t.Fatal("restore failed")
	}
}

func TestJournalConflictRetainsDraftAndStaleReadsCannotReplaceSubject(t *testing.T) {
	m, store, subject := journalModel(t)
	note, err := store.SaveNote(context.Background(), core.Note{Subject: subject, Body: "initial"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	deliverWorkspace(t, m, m.openNotes(subject))
	m.editJournal(&note, "")
	m.work.editor.SetValue("my changed draft")
	external := note
	external.Body = "another process"
	if _, err = store.SaveNote(context.Background(), external, note.Revision); err != nil {
		t.Fatal(err)
	}
	deliverWorkspace(t, m, m.saveJournal())
	if m.work.modal != "edit" || m.work.editor.Value() != "my changed draft" || m.work.noteErr == "" {
		t.Fatal("conflicting write lost draft or error")
	}
	m.workspaceModalKey(nil, "esc")
	if m.work.modal != "discard" {
		t.Fatal("dirty cancellation not guarded")
	}
	m.workspaceModalKey(nil, "k")
	if m.work.modal != "edit" {
		t.Fatal("cannot keep editing")
	}
	old := m.work.noteGen
	other := subject
	other.ID = "other-run"
	m.openNotes(other)
	m.acceptJournal(journalLoadedMsg{gen: old, subject: subject, notes: []core.Note{note}})
	if !subjectEqual(m.work.subject, other) || len(m.work.notes) != 0 {
		t.Fatal("stale journal contaminated new selection")
	}
}

func TestWorkspaceModalDoesNotEatNetworkMessagesOrClickThrough(t *testing.T) {
	m, _, subject := journalModel(t)
	deliverWorkspace(t, m, m.openNotes(subject))
	m.editJournal(nil, "")
	if _, handled := m.updateWorkspace(runsMsg{target: m.active}); handled {
		t.Fatal("editor swallowed remote run update")
	}
	if _, handled := m.updateWorkspace(metricLoadedMsg{}); handled {
		t.Fatal("editor swallowed chart history")
	}
	before := m.focus
	m.workspaceMouse(tea.MouseClickMsg{X: 1, Y: 3, Button: tea.MouseLeft})
	m.workspaceMouse(tea.MouseReleaseMsg{X: 1, Y: 3, Button: tea.MouseLeft})
	if m.focus != before || m.work.modal != "edit" {
		t.Fatal("modal click-through")
	}
	m.work.editor.SetValue("draft")
	for _, size := range [][2]int{{140, 42}, {80, 24}, {38, 12}, {12, 4}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View().Content
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("modal exceeds width %d: %q", size[0], line)
			}
		}
	}
}

func TestInspectionSearchAllowsUnrelatedAsyncResults(t *testing.T) {
	m, _ := readyModel()
	m.focus, m.tab = 2, 1
	m.syncInspection()
	m.overlay, m.inspect.Typing = "inspect-search", true
	for _, msg := range []tea.Msg{runsMsg{}, layoutLoadedMsg{}, resultMsg{}, preferencesSavedMsg{}} {
		if _, handled := m.updateInspection(msg); handled {
			t.Fatalf("search consumed async result %T", msg)
		}
	}
	if _, handled := m.updateInspection(tea.PasteMsg{Content: "qjk"}); !handled {
		t.Fatal("search did not own pasted text")
	}
}

func TestEditorArgumentsAndDraftRoundTrip(t *testing.T) {
	args, err := editorArguments(`"/Applications/My Editor/bin/edit" --wait 'a b' '$(touch nope)'`)
	if err != nil || !reflect.DeepEqual(args, []string{"/Applications/My Editor/bin/edit", "--wait", "a b", "$(touch nope)"}) {
		t.Fatalf("argv: %#v %v", args, err)
	}
	if _, err := editorArguments(`vi "broken`); err == nil {
		t.Fatal("accepted bad quotes")
	}
	m, _, subject := journalModel(t)
	deliverWorkspace(t, m, m.openNotes(subject))
	m.editJournal(nil, "old draft")
	m.work.external = true
	m.acceptEditorFinished(editorFinishedMsg{gen: m.work.noteGen, body: "new\nMarkdown"})
	if m.work.editor.Value() != "new\nMarkdown" || m.work.modal != "edit" || m.work.notePending {
		t.Fatal("editor must return an unsaved draft")
	}
}

func TestSummaryExportPreservesExistingFileAndSelectsNoteSubject(t *testing.T) {
	m, _, subject := journalModel(t)
	w := m.work
	w.modal = "report"
	w.reportText = "# Summary\nEvidence"
	w.reportSubjects = []core.Subject{subject, {Source: subject.Source, Kind: "run", ID: "r2"}}
	m.reportModalKey(nil, "n")
	if w.modal != "report-subject" {
		t.Fatal("comparison note did not ask for subject")
	}
	m.reportModalKey(nil, "down")
	m.reportModalKey(nil, "enter")
	if w.subject.ID != "r2" || w.editor.Value() != w.reportText {
		t.Fatal("comparison note got wrong subject/content")
	}
	path := filepath.Join(t.TempDir(), "report.md")
	w.modal = "export"
	w.export.SetValue(path)
	deliverWorkspace(t, m, m.exportReport())
	data, err := os.ReadFile(path)
	if err != nil || string(data) != w.reportText {
		t.Fatalf("export failed: %s %v", data, err)
	}
	w.reportText = "replacement"
	w.modal = "export"
	deliverWorkspace(t, m, m.exportReport())
	data, _ = os.ReadFile(path)
	if string(data) == "replacement" || !strings.Contains(m.status, "Export failed") {
		t.Fatal("export overwrote existing file")
	}
}
