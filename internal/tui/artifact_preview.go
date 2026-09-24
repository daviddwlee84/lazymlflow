package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/artifactpreview"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

type artifactPreviewState struct {
	Source, RunID, Path                               string
	Gen                                               uint64
	Options                                           artifactpreview.Options
	Document                                          artifactpreview.Document
	HasDocument, Pending, PagerPending, External, Raw bool
	Approval                                          *core.PreviewApprovalError
	Confirm, PressedChoice                            int
	Err                                               string
	Lines                                             []string
	Offset, Horizontal                                int
	Search                                            textinput.Model
	Searching                                         bool
	Query, BeforeQuery                                string
	BeforeOffset                                      int
	Matches                                           []int
	Match                                             int
}

type artifactPreviewMsg struct {
	source, runID, path string
	gen                 uint64
	document            artifactpreview.Document
	err                 error
}

type artifactPagerPreparedMsg struct {
	source string
	gen    uint64
	file   artifactpreview.PagerFile
	argv   []string
	err    error
	ctx    context.Context
}

type artifactPagerFinishedMsg struct {
	source string
	gen    uint64
	err    error
}

func (m *model) artifactPreviewActions() []action {
	if m.focus == 2 && m.tab == 4 && !m.compare && m.run() != nil {
		return []action{act("artifact-preview", "p", "Preview selected artifact file")}
	}
	return nil
}

func (m *model) openArtifactPreview(pager bool) tea.Cmd {
	artifacts, run, state := m.artifacts(), m.run(), m.state()
	if artifacts == nil || run == nil || state == nil || state.Session == nil || len(artifacts.Rows) == 0 {
		return nil
	}
	file := artifacts.Rows[clamp(artifacts.Index, 0, len(artifacts.Rows)-1)]
	if file.IsDir {
		m.status = "Enter opens a directory; select a file to preview"
		return nil
	}
	m.stopArtifactPreview()
	search := textinput.New()
	search.Prompt = "Search: "
	search.CharLimit = 1024
	search.SetWidth(max(1, m.width-12))
	limit := m.opts.PreviewMaxBytes
	if limit == 0 {
		limit = core.DefaultPreviewBytes
	}
	m.preview = &artifactPreviewState{Source: core.SourceKey(m.target()), RunID: run.ID(), Path: file.Path, Options: artifactpreview.Options{MaxBytes: limit}, Search: search, Match: -1, PressedChoice: -1}
	m.overlay = "artifact-preview"
	m.mousePressed = ""
	m.cancelUnusedHistories()
	// Initial entry always shows the built-in document; b explicitly hands the
	// already bounded buffer to an external pager afterward.
	_ = pager
	return m.readArtifactPreview()
}

func (m *model) readArtifactPreview() tea.Cmd {
	p, s := m.preview, m.state()
	if p == nil || s == nil || s.Session == nil {
		return nil
	}
	ctx, gen := m.operation("artifact-preview")
	p.Gen, p.Pending, p.Err = gen, true, ""
	p.Approval, p.Confirm = nil, 0
	source, runID, path, options, backend := p.Source, p.RunID, p.Path, p.Options, s.Session.Backend
	return func() tea.Msg {
		document, err := artifactpreview.Read(ctx, backend, runID, path, options)
		return artifactPreviewMsg{source, runID, path, gen, document, err}
	}
}

func (m *model) stopArtifactPreview() {
	for _, key := range []string{"artifact-preview", "artifact-pager"} {
		if cancel := m.cancel[key]; cancel != nil {
			cancel()
			delete(m.cancel, key)
		}
	}
	m.preview = nil
	if m.overlay == "artifact-preview" {
		m.overlay = ""
	}
	m.mousePressed = ""
}

func (m *model) updateArtifactPreview(msg tea.Msg) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case artifactPreviewMsg:
		p := m.preview
		if p == nil || p.Source != v.source || core.SourceKey(m.target()) != v.source || p.Gen != v.gen || p.RunID != v.runID || p.Path != v.path {
			return nil, true
		}
		p.Pending = false
		if v.err != nil {
			var approval *core.PreviewApprovalError
			if errors.As(v.err, &approval) {
				p.Approval, p.Confirm, p.Err = approval, 0, ""
			} else if !errors.Is(v.err, context.Canceled) {
				p.Err = v.err.Error()
			}
			return nil, true
		}
		p.Document, p.HasDocument, p.Err = v.document, true, ""
		p.Raw, p.Offset, p.Horizontal = false, 0, 0
		p.rebuildLines()
		return nil, true
	case artifactPagerPreparedMsg:
		p := m.preview
		if p == nil || p.Source != v.source || core.SourceKey(m.target()) != v.source || p.Gen != v.gen {
			return func() tea.Msg { _ = v.file.Cleanup(); return nil }, true
		}
		p.PagerPending = false
		if v.err != nil {
			if cancel := m.cancel["artifact-pager"]; cancel != nil {
				cancel()
				delete(m.cancel, "artifact-pager")
			}
			p.Err = v.err.Error()
			return nil, true
		}
		p.External = true
		ctx := v.ctx
		if ctx == nil {
			ctx = m.ctx
		}
		command := &artifactPagerExec{ctx: ctx, argv: v.argv, file: v.file}
		return tea.Exec(command, func(err error) tea.Msg { return artifactPagerFinishedMsg{v.source, v.gen, err} }), true
	case artifactPagerFinishedMsg:
		p := m.preview
		if p == nil || p.Source != v.source || p.Gen != v.gen {
			return nil, true
		}
		p.External = false
		if cancel := m.cancel["artifact-pager"]; cancel != nil {
			cancel()
			delete(m.cancel, "artifact-pager")
		}
		if v.err != nil {
			p.Err = "Pager returned: " + v.err.Error()
		} else {
			p.Err = ""
		}
		return nil, true
	}
	p := m.preview
	if p == nil {
		return nil, false
	}
	if p.Source != core.SourceKey(m.target()) {
		m.stopArtifactPreview()
		return nil, false
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		p.Search.SetWidth(max(1, m.width-12))
		p.PressedChoice = -1
		if p.Approval != nil && !p.approvalFits(m.width, m.height) {
			p.Confirm = 0
		}
		p.clampOffset(m.height)
		return nil, false
	case tea.KeyPressMsg:
		key := v.String()
		if key == "ctrl+c" {
			m.stopAll()
			return tea.Quit, true
		}
		if p.Searching {
			switch key {
			case "esc":
				p.Searching = false
				p.Query = p.BeforeQuery
				p.Search.SetValue(p.Query)
				p.Search.Blur()
				p.find(false)
				p.Offset = p.BeforeOffset
			case "enter":
				p.Searching = false
				p.Search.Blur()
			default:
				var cmd tea.Cmd
				p.Search, cmd = p.Search.Update(msg)
				p.Query = p.Search.Value()
				p.find(true)
				return cmd, true
			}
			return nil, true
		}
		if p.Approval != nil {
			if !p.approvalFits(m.width, m.height) {
				if key == "esc" || key == "q" || key == "n" || key == "enter" {
					m.stopArtifactPreview()
				}
				return nil, true
			}
			switch key {
			case "esc", "q", "n":
				m.stopArtifactPreview()
			case "left", "h", "up", "k":
				p.Confirm = 0
			case "right", "l", "down", "j":
				p.Confirm = 1
			case "tab":
				p.Confirm = 1 - p.Confirm
			case "enter":
				if p.Confirm == 0 {
					m.stopArtifactPreview()
					return nil, true
				}
				p.Options.AllowLarge, p.Options.AllowUnknown, p.Options.AllowWholeFetch = true, true, true
				return m.readArtifactPreview(), true
			}
			return nil, true
		}
		switch key {
		case "esc", "q":
			m.stopArtifactPreview()
		case "up", "k":
			p.Offset--
		case "down", "j":
			p.Offset++
		case "pgup", "ctrl+u":
			p.Offset -= max(1, m.height-8)
		case "pgdown", "ctrl+d", "space":
			p.Offset += max(1, m.height-8)
		case "home", "g":
			p.Offset = 0
		case "end", "G":
			p.Offset = len(p.Lines) - 1
		case "left", "h":
			p.Horizontal = max(0, p.Horizontal-8)
		case "right", "l":
			p.Horizontal += 8
		case "/":
			p.BeforeQuery, p.BeforeOffset, p.Searching = p.Query, p.Offset, true
			p.Search.SetValue(p.Query)
			return p.Search.Focus(), true
		case "n":
			p.nextMatch(1)
		case "N":
			p.nextMatch(-1)
		case "v":
			if p.HasDocument && p.Document.Formatted {
				p.Raw = !p.Raw
				p.rebuildLines()
			}
		case "r":
			if !p.Pending && !p.PagerPending && !p.External {
				return m.readArtifactPreview(), true
			}
		case "b":
			return m.openArtifactPager(), true
		}
		if m.preview != nil {
			p.clampOffset(m.height)
		}
		return nil, true
	case tea.MouseWheelMsg:
		if v.Button == tea.MouseWheelUp {
			p.Offset -= 3
		} else if v.Button == tea.MouseWheelDown {
			p.Offset += 3
		}
		p.clampOffset(m.height)
		return nil, true
	case tea.MouseClickMsg:
		if v.Button == tea.MouseLeft {
			p.PressedChoice = p.choiceAt(v.X, v.Y, m.width, m.height)
			if p.PressedChoice >= 0 {
				p.Confirm = p.PressedChoice
			}
		}
		return nil, true
	case tea.MouseReleaseMsg:
		choice := p.choiceAt(v.X, v.Y, m.width, m.height)
		pressed := p.PressedChoice
		p.PressedChoice = -1
		if pressed >= 0 && choice == pressed {
			if choice == 0 {
				m.stopArtifactPreview()
				return nil, true
			}
			p.Options.AllowLarge, p.Options.AllowUnknown, p.Options.AllowWholeFetch = true, true, true
			return m.readArtifactPreview(), true
		}
		return nil, true
	case tea.MouseMotionMsg:
		// The modal owns pointer input too; no clicks reach underlying artifacts.
		return nil, true
	}
	return nil, false
}

func (p *artifactPreviewState) rebuildLines() {
	text := p.Document.Text
	if p.Raw {
		text = p.Document.RawText
	}
	p.Lines = strings.Split(text, "\n")
	p.Offset, p.Horizontal = 0, 0
	p.find(false)
}
func (p *artifactPreviewState) find(jump bool) {
	p.Matches, p.Match = nil, -1
	if p.Query == "" {
		return
	}
	query := strings.ToLower(p.Query)
	for i, line := range p.Lines {
		if strings.Contains(strings.ToLower(line), query) {
			p.Matches = append(p.Matches, i)
		}
	}
	if len(p.Matches) > 0 {
		p.Match = 0
		if jump {
			p.Offset = p.Matches[0]
		}
	}
}
func (p *artifactPreviewState) nextMatch(delta int) {
	if len(p.Matches) == 0 {
		return
	}
	p.Match = (p.Match + delta + len(p.Matches)) % len(p.Matches)
	p.Offset = p.Matches[p.Match]
}
func (p *artifactPreviewState) clampOffset(height int) {
	p.Offset = clamp(p.Offset, 0, max(0, len(p.Lines)-max(1, height-7)))
}

func (p *artifactPreviewState) approvalLines(width int) []string {
	if p.Approval == nil {
		return nil
	}
	size := "unknown"
	if p.Approval.Info.Size != nil {
		size = fmt.Sprintf("%d bytes", *p.Approval.Info.Size)
	}
	messages := []string{clean(p.Path), "Artifact size: " + size, fmt.Sprintf("Display at most %d bytes of the file.", p.Options.MaxBytes)}
	if p.Approval.Info.MayDownloadWhole || p.Approval.WholeFetch {
		messages = append(messages, "This MLflow server may fetch the whole remote object before serving the prefix.")
	}
	var lines []string
	for _, message := range messages {
		lines = append(lines, wrapText(message, max(1, width-2))...)
	}
	return append(lines, "", row("Cancel", p.Confirm == 0, max(1, width-2)), row("Read bounded preview", p.Confirm == 1, max(1, width-2)))
}
func (p *artifactPreviewState) approvalFits(width, height int) bool {
	return width >= 24 && height >= len(p.approvalLines(width))+3
}
func (p *artifactPreviewState) choiceAt(x, y, width, height int) int {
	if p.Approval == nil || !p.approvalFits(width, height) || x <= 0 || x >= width-1 {
		return -1
	}
	base := len(p.approvalLines(width)) - 1
	if y == base {
		return 0
	}
	if y == base+1 {
		return 1
	}
	return -1
}

func (m *model) artifactPreviewView() (tea.View, bool) {
	p := m.preview
	if p == nil {
		return tea.View{}, false
	}
	w, h := max(1, m.width), max(1, m.height)
	if h < 5 || w < 16 {
		view := tea.NewView(block([]string{"Artifact preview", clean(p.Path), "Resize · Esc back"}, w, h))
		view.AltScreen = true
		if m.layout.Mouse {
			view.MouseMode = tea.MouseModeCellMotion
		}
		return view, true
	}
	lines := []string{}
	title := "Artifact preview · " + clean(p.Path)
	footer := "↑↓/jk scroll · ←→ pan · / search · n/N matches · b bat/pager · r refresh · Esc back"
	if p.Approval != nil {
		title = "Confirm artifact preview"
		lines = p.approvalLines(w)
		footer = "↑↓/jk choose · Enter confirm · Esc cancel"
		if !p.approvalFits(w, h) {
			lines = []string{"Resize terminal to review this transfer.", "Esc cancels without reading artifact bytes."}
			footer = "Resize to confirm · Esc cancel"
		}
	} else {
		if p.Pending {
			lines = append(lines, "Reading bounded preview… · Esc cancels")
		}
		if p.Err != "" {
			lines = append(lines, "Preview: "+clean(p.Err))
		}
		if p.HasDocument {
			size := "unknown"
			if p.Document.Info.Size != nil {
				size = strconv.FormatInt(*p.Document.Info.Size, 10)
			}
			kind := "text"
			if p.Document.Formatted && !p.Raw {
				kind = "formatted"
			}
			status := fmt.Sprintf("%s · %d bytes read / %s total · %d lines", kind, p.Document.BytesRead, size, len(p.Lines))
			if p.Document.Truncated {
				status += " · TRUNCATED PREFIX"
			}
			lines = append(lines, status)
			if p.Document.Warning != "" {
				lines = append(lines, clean(p.Document.Warning))
			}
			if p.Document.Binary {
				lines = append(lines, "Binary preview unavailable. Esc returns; d downloads the selected artifact.")
			} else {
				capacity := max(1, h-4-len(lines))
				end := min(len(p.Lines), p.Offset+capacity)
				for i := p.Offset; i < end; i++ {
					prefix := fmt.Sprintf("%5d │ ", i+1)
					line := previewCells(p.Lines[i], p.Horizontal, max(1, w-2-textWidth(prefix)))
					if p.Query != "" && strings.Contains(strings.ToLower(p.Lines[i]), strings.ToLower(p.Query)) {
						line = selectedStyle.Render(line)
					}
					lines = append(lines, prefix+line)
				}
			}
			if p.Document.Formatted {
				footer += " · v raw/formatted"
			}
		}
		if p.PagerPending {
			footer = "Preparing bounded pager file… · Esc cancels"
		}
		if p.Searching {
			footer = p.Search.View() + " · Enter accept · Esc cancel"
		} else if p.Query != "" {
			footer = fmt.Sprintf("Search %q: %d matches · ", clean(p.Query), len(p.Matches)) + footer
		}
	}
	view := tea.NewView(frame(title, lines, w, max(1, h-1), true) + "\n" + textFit(footer, w))
	view.AltScreen = true
	if m.layout.Mouse {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view, true
}

func previewCells(line string, offset, width int) string {
	// Input is already sanitized by the shared formatter. Converting tabs here
	// keeps line numbers and horizontal offsets deterministic in the terminal.
	line = clean(strings.ReplaceAll(line, "\t", "    "))
	return ansi.Cut(line, offset, offset+width)
}

func (m *model) openArtifactPager() tea.Cmd {
	p := m.preview
	if p == nil || !p.HasDocument || p.Document.Binary || p.Pending || p.PagerPending || p.External {
		return nil
	}
	p.PagerPending = true
	ctx, _ := m.operation("artifact-pager")
	source, gen, document := p.Source, p.Gen, p.Document
	if p.Raw {
		document.Text = document.RawText
	}
	return func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return artifactPagerPreparedMsg{source: source, gen: gen, err: err, ctx: ctx}
		}
		file, err := artifactpreview.PreparePager(document)
		var argv []string
		if err == nil {
			// Own the temporary file even if the UI quits before this result is
			// delivered. Exec retains this same context until the pager returns.
			context.AfterFunc(ctx, func() { _ = file.Cleanup() })
			argv, err = artifactpreview.PagerCommand(file)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			_ = file.Cleanup()
		}
		return artifactPagerPreparedMsg{source: source, gen: gen, file: file, argv: argv, err: err, ctx: ctx}
	}
}

type artifactPagerExec struct {
	ctx         context.Context
	argv        []string
	file        artifactpreview.PagerFile
	in          io.Reader
	out, stderr io.Writer
}

func (p *artifactPagerExec) SetStdin(v io.Reader)  { p.in = v }
func (p *artifactPagerExec) SetStdout(v io.Writer) { p.out = v }
func (p *artifactPagerExec) SetStderr(v io.Writer) { p.stderr = v }
func (p *artifactPagerExec) Run() (err error) {
	defer func() {
		if cleanup := p.file.Cleanup(); err == nil {
			err = cleanup
		}
	}()
	code, err := platform.RunAttached(p.ctx, p.argv, os.Environ(), p.in, p.out, p.stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("pager exited with status %d", code)
	}
	return nil
}
