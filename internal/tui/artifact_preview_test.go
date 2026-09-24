package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/artifactpreview"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type previewTUIBackend struct {
	*fakeBackend
	info     core.PreviewInfo
	data     []byte
	reads    int
	requests []core.PreviewRequest
	failure  error
}

func (b *previewTUIBackend) InspectArtifact(_ context.Context, run, path string) (core.PreviewInfo, error) {
	info := b.info
	info.RunID, info.Path = run, path
	return info, nil
}
func (b *previewTUIBackend) ReadArtifactPreview(ctx context.Context, request core.PreviewRequest) (core.PreviewResult, error) {
	b.requests = append(b.requests, request)
	info, _ := b.InspectArtifact(ctx, request.RunID, request.Path)
	if err := core.CheckPreviewApproval(info, request); err != nil {
		return core.PreviewResult{Info: info}, err
	}
	if b.failure != nil {
		return core.PreviewResult{}, b.failure
	}
	b.reads++
	n := min(int64(len(b.data)), request.MaxBytes)
	return core.PreviewResult{Info: info, Data: append([]byte(nil), b.data[:n]...), Truncated: int64(len(b.data)) > n, BytesRead: n}, nil
}
func previewTUIModel(t *testing.T, body string) (*model, *previewTUIBackend) {
	t.Helper()
	m, base := readyModel()
	size := int64(len(body))
	b := &previewTUIBackend{fakeBackend: base, info: core.PreviewInfo{Supported: true, Size: &size}, data: []byte(body)}
	m.state().Session.Backend = b
	m.opts.PreviewMaxBytes = core.DefaultPreviewBytes
	m.focus, m.tab = 2, 4
	m.state().Artifacts[m.artifactKey()] = &artifactState{listState: listState{Selected: "report.txt"}, Rows: []core.Artifact{{Path: "report.txt", FileSize: size}}}
	t.Cleanup(m.stopAll)
	return m, b
}
func applyPreviewCmd(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected preview effect")
	}
	m.Update(cmd())
}

func TestArtifactPreviewEnterAndDefaultCancel(t *testing.T) {
	m, b := previewTUIModel(t, "123456789")
	m.opts.PreviewMaxBytes = 4
	applyPreviewCmd(t, m, key(m, "enter"))
	if m.preview == nil || m.preview.Approval == nil || m.preview.Confirm != 0 || b.reads != 0 || m.inputMode != "" {
		t.Fatalf("entry did not request consent before transfer: %+v reads=%d", m.preview, b.reads)
	}
	key(m, "enter")
	if m.preview != nil || b.reads != 0 || m.run().ID() != "r1" || m.artifacts().Selected != "report.txt" {
		t.Fatal("default cancel changed context or downloaded bytes")
	}
	applyPreviewCmd(t, m, key(m, "p"))
	key(m, "right")
	applyPreviewCmd(t, m, key(m, "enter"))
	if m.preview == nil || !m.preview.HasDocument || m.preview.Document.Text != "1234" || !m.preview.Document.Truncated || b.reads != 1 {
		t.Fatalf("approved transfer was not bounded: %+v", m.preview)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "TRUNCATED PREFIX") {
		t.Fatal("truncation is not visible")
	}
	key(m, "esc")
	key(m, "d")
	if m.inputMode != "download" {
		t.Fatalf("explicit download shortcut changed: %q", m.inputMode)
	}
}

func TestArtifactPreviewSearchOwnsKeysAndResizes(t *testing.T) {
	m, _ := previewTUIModel(t, "first\nquery q\nlast\nquery q again\n")
	applyPreviewCmd(t, m, key(m, "p"))
	key(m, "/")
	key(m, "q")
	if m.preview == nil || !m.preview.Searching || m.preview.Query != "q" {
		t.Fatal("search typing q closed preview")
	}
	key(m, "enter")
	key(m, "n")
	if m.preview.Match != 1 {
		t.Fatalf("next search match not selected: %+v", m.preview)
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 1, Height: 1}, {Width: 40, Height: 8}, {Width: 160, Height: 45}} {
		m.Update(size)
		view := m.View()
		if strings.Count(view.Content, "\n") >= size.Height {
			t.Fatalf("preview exceeds terminal height %dx%d: %q", size.Width, size.Height, view.Content)
		}
	}
	key(m, "esc")
	if m.preview != nil || m.overlay != "" || m.focus != 2 || m.tab != 4 {
		t.Fatal("closing preview lost artifact context")
	}
}

func TestArtifactPreviewLateResponseCannotReopenClosedModal(t *testing.T) {
	m, _ := previewTUIModel(t, "hello")
	cmd := key(m, "p")
	key(m, "esc")
	m.Update(cmd())
	if m.preview != nil || m.overlay != "" {
		t.Fatal("late response reopened preview")
	}
}

func TestArtifactPreviewDirectoryEnterAndBinaryFallback(t *testing.T) {
	m, b := previewTUIModel(t, "\x00binary")
	applyPreviewCmd(t, m, key(m, "p"))
	if !m.preview.Document.Binary || !strings.Contains(m.View().Content, "Binary preview unavailable") || m.openArtifactPager() != nil {
		t.Fatal("binary file was treated as executable text")
	}
	key(m, "esc")
	a := m.artifacts()
	a.Rows = []core.Artifact{{Path: "directory", IsDir: true}}
	key(m, "enter")
	if m.preview != nil || m.currentPath() != "directory" || b.reads != 1 {
		t.Fatal("directory enter did not retain navigation behavior")
	}
}

func TestArtifactPreviewErrorsKeepUsableDocument(t *testing.T) {
	m, b := previewTUIModel(t, "previous")
	applyPreviewCmd(t, m, key(m, "p"))
	b.failure = errors.New("temporary preview failure")
	applyPreviewCmd(t, m, key(m, "r"))
	if m.preview.Document.Text != "previous" || !strings.Contains(m.preview.Err, "temporary") {
		t.Fatal("refresh failure discarded readable preview")
	}
}

func TestArtifactPreviewPagerStalePreparationCleansFile(t *testing.T) {
	m, _ := previewTUIModel(t, "hello")
	applyPreviewCmd(t, m, key(m, "p"))
	source, gen := m.preview.Source, m.preview.Gen
	file, err := artifactpreview.PreparePager(m.preview.Document)
	if err != nil {
		t.Fatal(err)
	}
	key(m, "esc")
	_, cleanup := m.Update(artifactPagerPreparedMsg{source: source, gen: gen, file: file})
	if cleanup == nil {
		t.Fatal("stale pager file has no cleanup")
	}
	cleanup()
	if _, err := os.Stat(file.Path); !os.IsNotExist(err) {
		t.Fatalf("stale pager file survived: %v", err)
	}
}

func TestArtifactPreviewPagerRestoresAfterFailureAndCleansFile(t *testing.T) {
	m, _ := previewTUIModel(t, "hello")
	applyPreviewCmd(t, m, key(m, "p"))
	file, err := artifactpreview.PreparePager(m.preview.Document)
	if err != nil {
		t.Fatal(err)
	}
	pager := &artifactPagerExec{ctx: context.Background(), argv: []string{filepath.Join(t.TempDir(), "missing-pager")}, file: file}
	err = pager.Run()
	if err == nil {
		t.Fatal("missing pager succeeded")
	}
	m.preview.External = true
	m.Update(artifactPagerFinishedMsg{source: m.preview.Source, gen: m.preview.Gen, err: err})
	if m.preview.External || m.preview.Document.Text != "hello" || m.preview.Err == "" {
		t.Fatal("pager failure lost usable preview state")
	}
	if _, err := os.Stat(file.Path); !os.IsNotExist(err) {
		t.Fatalf("pager file survived failure: %v", err)
	}
}

func TestArtifactPreviewDroppedPagerPreparationStillCleansFile(t *testing.T) {
	for _, quit := range []bool{false, true} {
		m, _ := previewTUIModel(t, "bounded text")
		applyPreviewCmd(t, m, key(m, "p"))
		dir := t.TempDir()
		name := "bat"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not executed"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		cmd := m.openArtifactPager()
		if cmd == nil {
			t.Fatal("pager preparation missing")
		}
		prepared := cmd().(artifactPagerPreparedMsg)
		if prepared.err != nil {
			t.Fatal(prepared.err)
		}
		if quit {
			m.stopAll()
		} else {
			key(m, "esc")
		}
		// Deliberately drop prepared instead of passing it to Update, as when
		// the program stops before Bubble Tea consumes the command result.
		deadline := time.Now().Add(time.Second)
		for {
			_, err := os.Stat(prepared.file.Path)
			if os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("dropped pager result leaked %s (quit=%t)", prepared.file.Path, quit)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestArtifactPreviewTinyApprovalCannotAcceptHiddenChoice(t *testing.T) {
	m, b := previewTUIModel(t, "123456789")
	m.opts.PreviewMaxBytes = 4
	applyPreviewCmd(t, m, key(m, "p"))
	key(m, "j")
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 6})
	key(m, "j")
	key(m, "enter")
	if b.reads != 0 || m.preview != nil {
		t.Fatal("small confirmation accepted an invisible transfer action")
	}
}

func TestArtifactPreviewConfirmationMouseUsesVisibleChoice(t *testing.T) {
	m, b := previewTUIModel(t, "123456789")
	m.opts.PreviewMaxBytes = 4
	applyPreviewCmd(t, m, key(m, "p"))
	y := len(m.preview.approvalLines(m.width))
	m.Update(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: 4, Y: y, Button: tea.MouseLeft})
	applyPreviewCmd(t, m, cmd)
	if b.reads != 1 || !m.preview.Document.Truncated {
		t.Fatal("visible preview confirmation click did not start bounded read")
	}
}

func TestArtifactPreviewShortcutHasOneContextualOwner(t *testing.T) {
	m, _ := previewTUIModel(t, "hello")
	count := 0
	for _, action := range m.actions() {
		for _, binding := range action.Keys {
			if binding == "p" {
				count++
				if action.ID != "artifact-preview" {
					t.Fatalf("artifact p collision: %s", action.ID)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("artifact preview has %d shortcut bindings", count)
	}
	m.tab = 1
	for _, action := range m.actions() {
		if action.ID == "artifact-preview" {
			t.Fatal("artifact preview binding leaked into metrics")
		}
	}
}

func TestArtifactPreviewLabelsProbeAsBytesRead(t *testing.T) {
	m, _ := previewTUIModel(t, "123456789")
	applyPreviewCmd(t, m, key(m, "p"))
	m.preview.Document.BytesRead = 5
	m.preview.Document.Text = "1234"
	m.preview.Document.Truncated = true
	m.preview.rebuildLines()
	if text := ansi.Strip(m.View().Content); !strings.Contains(text, "5 bytes read / 9 total") {
		t.Fatalf("probe byte count is not clearly labelled: %s", text)
	}
}
