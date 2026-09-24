package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/inspection"
)

type reportMsg struct {
	source           string
	gen              uint64
	snapshot         inspection.Snapshot
	markdown, prompt string
	subjects         []core.Subject
	err              error
}
type reportExportedMsg struct {
	gen  uint64
	path string
	err  error
}

func (m *model) openSummary() tea.Cmd {
	w := m.work
	s := m.state()
	if s == nil || s.Session == nil {
		m.status = "Connect a target before collecting a summary"
		return nil
	}
	if w.catalog && m.focus != 1 {
		m.status = "Select a related run in pane 2 to summarize"
		return nil
	}
	var ids []string
	experiment := ""
	recipe := "run-summary"
	if w.catalog {
		if u := m.catalogUse(); u != nil {
			ids = []string{u.RunID}
		}
	} else if m.compare {
		for _, r := range m.selectedRuns() {
			ids = append(ids, r.ID())
		}
		recipe = "compare-runs"
	} else if m.focus == 0 {
		if m.activityScope() != scopeExperiment {
			m.status = "Select an Activity run to summarize"
			return nil
		}
		experiment = s.Selected
		recipe = "experiment-summary"
	} else if r := m.run(); r != nil {
		ids = []string{r.ID()}
	}
	if len(ids) == 0 && experiment == "" {
		m.status = "Select a run or experiment to summarize"
		return nil
	}
	ctx, gen := m.workspaceContext("report")
	w.reportGen = gen
	w.modal = "report"
	w.reportPending = true
	w.reportErr = ""
	w.reportOffset = 0
	w.reportText = ""
	w.promptText = ""
	w.reportPrompt = false
	w.reportSubjects = nil
	m.cancelUnusedHistories()
	target := m.target()
	source := core.SourceKey(target)
	collector := inspection.NewCollector(s.Session.Backend, target, m.noteStore())
	options := inspection.DefaultOptions()
	query := core.RunQuery{ExperimentIDs: []string{experiment}, OrderBy: []string{"attributes.start_time DESC"}, ViewType: "ACTIVE_ONLY"}
	if r := m.runs(); r != nil && experiment != "" {
		query.Filter = r.Filter
		if r.Order != "" {
			query.OrderBy = splitOrder(r.Order)
		}
	}
	return func() tea.Msg {
		var snapshot inspection.Snapshot
		var err error
		if experiment != "" {
			snapshot, err = collector.CollectExperiment(ctx, experiment, inspection.ExperimentOptions{Options: options, Query: query, DetailLimit: 20})
		} else {
			snapshot, err = collector.CollectRuns(ctx, ids, options)
		}
		if err != nil {
			return reportMsg{source: source, gen: gen, err: err}
		}
		markdown := inspection.RenderMarkdown(snapshot)
		prompt, promptErr := inspection.RenderPrompt(recipe, snapshot)
		var subjects []core.Subject
		if experiment != "" {
			subjects = append(subjects, core.Subject{Source: source, Kind: "experiment", ID: experiment, Label: experiment})
		} else {
			for _, r := range snapshot.Runs {
				subjects = append(subjects, core.Subject{Source: source, Kind: "run", ID: r.Run.ID(), Label: r.Run.Name()})
			}
		}
		return reportMsg{source, gen, snapshot, markdown, prompt, subjects, promptErr}
	}
}
func (m *model) acceptReport(v reportMsg) tea.Cmd {
	w := m.work
	if v.gen != w.reportGen || v.source != core.SourceKey(m.target()) {
		return nil
	}
	w.reportPending = false
	if v.err != nil {
		w.reportErr = v.err.Error()
		return nil
	}
	w.report = v.snapshot
	w.reportText = v.markdown
	w.promptText = v.prompt
	w.reportSubjects = v.subjects
	m.status = "Summary collected · p agent prompt · y copy · e export · n save as local note"
	return nil
}
func (m *model) reportBody() string {
	if m.work.reportPrompt {
		return m.work.promptText
	}
	return m.work.reportText
}
func (m *model) reportModalKey(msg tea.Msg, key string) tea.Cmd {
	w := m.work
	if w.modal == "export" {
		switch key {
		case "esc":
			w.modal = "report"
			w.export.Blur()
			return nil
		case "enter":
			return m.exportReport()
		}
		var cmd tea.Cmd
		w.export, cmd = w.export.Update(msg)
		return cmd
	}
	if w.modal == "report-subject" {
		switch key {
		case "esc":
			w.modal = "report"
		case "up", "k":
			w.subjectIndex = clamp(w.subjectIndex-1, 0, len(w.reportSubjects)-1)
		case "down", "j":
			w.subjectIndex = clamp(w.subjectIndex+1, 0, len(w.reportSubjects)-1)
		case "enter":
			if len(w.reportSubjects) > 0 {
				w.subject = w.reportSubjects[w.subjectIndex]
				return m.editJournal(nil, w.reportText)
			}
		}
		return nil
	}
	switch key {
	case "esc":
		if w.reportPending {
			if c := m.cancel["workspace:report"]; c != nil {
				c()
			}
			w.reportPending = false
		}
		m.closeWorkspaceModal()
		return m.ensureDetails()
	case "ctrl+x":
		if c := m.cancel["workspace:report"]; c != nil {
			c()
		}
		w.reportGen++
		w.reportPending = false
		w.reportErr = "Collection cancelled"
	case "j", "down":
		w.reportOffset++
	case "k", "up":
		w.reportOffset = max(0, w.reportOffset-1)
	case "pgdown", "ctrl+d":
		w.reportOffset += 10
	case "pgup", "ctrl+u":
		w.reportOffset = max(0, w.reportOffset-10)
	case "home", "g":
		w.reportOffset = 0
	case "end", "G":
		w.reportOffset = 1 << 30
	case "p":
		if w.modal == "report" {
			w.reportPrompt = !w.reportPrompt
			w.reportOffset = 0
		}
	case "y":
		if text := m.reportBody(); text != "" {
			return m.copyWorkspace(text, "Summary")
		}
	case "e":
		if w.modal == "report" && w.reportText != "" {
			w.modal = "export"
			w.export.SetValue("run-summary.md")
			w.export.CursorEnd()
			return w.export.Focus()
		}
	case "n":
		if w.modal == "report" && w.reportText != "" && len(w.reportSubjects) > 0 {
			if len(w.reportSubjects) > 1 {
				w.modal = "report-subject"
				w.subjectIndex = 0
				return nil
			}
			w.subject = w.reportSubjects[0]
			return m.editJournal(nil, w.reportText)
		}
	}
	return nil
}
func (m *model) exportReport() tea.Cmd {
	w := m.work
	path := strings.TrimSpace(w.export.Value())
	if path == "" {
		m.status = "Enter an output path"
		return nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	gen, body := w.reportGen, m.reportBody()
	return func() tea.Msg {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.WriteString(body)
			if e := f.Close(); err == nil {
				err = e
			}
			if err != nil {
				os.Remove(path)
			}
		}
		return reportExportedMsg{gen, path, err}
	}
}
func (m *model) reportModalLines(width, height int) []string {
	w := m.work
	switch w.modal {
	case "export":
		return []string{"Write Markdown to a new file (existing files are preserved).", "", w.export.View(), "", "Enter export · Esc back", clean(m.status)}
	case "report-subject":
		lines := []string{"Choose which local journal receives this comparison:"}
		for i, s := range w.reportSubjects {
			lines = append(lines, row(subjectLabel(s), i == w.subjectIndex, width))
		}
		return lines
	case "workspace-help":
		return workspaceTextLines("Dataset catalog is scoped to the current tracking target.\n\nA: scan all accessible experiments in the selected lifecycle\nCtrl+X: cancel scan and keep partial results\nV: active / all / deleted\nH: include locally hidden or archived relationships\n/: search the focused pane\nh/l: collapse/expand dataset names\nEnter: inspect a version or open a related run\n[/]: schema / metadata / notes tabs\nN: open local journal; c: compose a note\nS: summarize the selected related run\ns: schema name / original order\ny: copy dataset or run ID\nB / Esc: return to experiments\n1/2/3, Tab, z, Ctrl+W and mouse retain their usual meanings.", width)
	case "field":
		lines := workspaceTextLines(w.reportText, width)
		start := clamp(w.reportOffset, 0, max(0, len(lines)-height))
		return lines[start:min(len(lines), start+height)]
	}
	if w.reportPending {
		return []string{"Collecting metadata, complete metric histories, datasets and local notes…", "Navigation remains available · Ctrl+X cancel · Esc back"}
	}
	if w.reportErr != "" {
		return workspaceTextLines("Could not collect summary: "+w.reportErr+"\nEsc back", width)
	}
	mode := "Markdown report"
	if w.reportPrompt {
		mode = "Agent prompt · external execution"
	}
	out := []string{mode + fmt.Sprintf(" · %d warnings", len(w.report.Warnings))}
	lines := workspaceTextLines(m.reportBody(), width)
	available := max(1, height-3)
	start := clamp(w.reportOffset, 0, max(0, len(lines)-available))
	out = append(out, lines[start:min(len(lines), start+available)]...)
	for len(out) < height-1 {
		out = append(out, "")
	}
	out = append(out, "p report/prompt · y copy · e export · n local note · PgUp/PgDn scroll · Esc back")
	return out
}
