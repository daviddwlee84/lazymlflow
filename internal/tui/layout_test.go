package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func mouseClick(m *model, x, y int) tea.Cmd {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return cmd
}
func locateHit(t *testing.T, m *model, want string) (int, int) {
	t.Helper()
	for y := 0; y < m.height; y++ {
		for x := 0; x < m.width; x++ {
			if m.hitAt(x, y) == want {
				return x, y
			}
		}
	}
	t.Fatalf("hit %q unavailable", want)
	return 0, 0
}
func TestPaneShortcutsResizeAndInputOwnership(t *testing.T) {
	m, _ := readyModel()
	key(m, "2")
	if m.focus != 1 {
		t.Fatal("2 did not focus runs")
	}
	key(m, "3")
	if m.focus != 2 {
		t.Fatal("3 did not focus details")
	}
	key(m, "z")
	if !m.zoom || m.geometry().Split {
		t.Fatal("zoom did not use single pane")
	}
	key(m, "z")
	m.perform("resize")
	key(m, "l")
	key(m, "k")
	key(m, "enter")
	if m.resizing || m.layout.LeftRatio <= core.DefaultLayout().LeftRatio || m.layout.TopRatio >= .55 {
		t.Fatal("resize bindings failed")
	}
	m.focus = 1
	key(m, "/")
	for _, r := range "123zM" {
		key(m, string(r))
	}
	if m.focus != 1 || m.zoom || m.input.Value() != "123zM" || !m.layout.Mouse {
		t.Fatal("typing dispatched shortcuts")
	}
	key(m, "esc")
	key(m, "M")
	if m.layout.Mouse || m.View().MouseMode != tea.MouseModeNone {
		t.Fatal("mouse toggle failed")
	}
}
func TestLayoutAndHitsAgreeAfterResizeAndZoom(t *testing.T) {
	m, _ := readyModel()
	for _, size := range [][2]int{{90, 24}, {140, 40}, {80, 24}, {160, 55}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, ratio := range []float64{.22, .45, .65} {
			m.layout.LeftRatio = ratio
			m.focus = 0
			m.zoom = false
			x, y := locateHit(t, m, "experiment:e2")
			mouseClick(m, x, y)
			if m.state().Selected != "e2" {
				t.Fatal("resized row hit selected wrong experiment")
			}
			m.selectExperiment(0)
			m.focus = 1
			if m.geometry().Split {
				cx, cy := locateHit(t, m, "action:columns")
				mouseClick(m, cx, cy)
				if m.overlay != "columns" {
					t.Fatal("toolbar hit did not open columns")
				}
				key(m, "esc")
			}
			m.zoom = true
			m.focus = 0
			x, y = locateHit(t, m, "experiment:e2")
			mouseClick(m, x, y)
			if m.state().Selected != "e2" {
				t.Fatal("zoom row hit failed")
			}
			m.selectExperiment(0)
		}
	}
}
func TestMouseGuardAndModalConsumeEvents(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	x, y := locateHit(t, m, "run:r2")
	x3, y3 := locateHit(t, m, "run:r3")
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x3, Y: y3, Button: tea.MouseLeft})
	if m.run().ID() != "r1" {
		t.Fatal("different release target activated")
	}
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	key(m, "?")
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.run().ID() != "r1" || m.overlay != "help" {
		t.Fatal("modal leaked mouse input")
	}
	key(m, "esc")
	m.layout.Mouse = false
	mouseClick(m, x, y)
	if m.run().ID() != "r1" {
		t.Fatal("disabled mouse selected a row")
	}
}
func TestDividerDragUpdatesPureGeometry(t *testing.T) {
	m, _ := readyModel()
	m.width = 150
	m.height = 40
	before := m.geometry()
	x, y := before.Vertical.X, 10
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: 70, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 70, Y: y, Button: tea.MouseLeft})
	if m.geometry().Panes[0].W != 70 || m.drag != "" {
		t.Fatalf("divider mismatch: %#v", m.geometry())
	}
	x, y = locateHit(t, m, "experiment:e2")
	mouseClick(m, x, y)
	if m.state().Selected != "e2" {
		t.Fatal("post-drag geometry stale")
	}
}
func TestPickerSearchOwnsTextAndSavesExperimentColumns(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	key(m, "v")
	for _, r := range "123jq" {
		key(m, string(r))
	}
	if m.overlay != "columns" || m.pickerSearch.Value() != "123jq" || m.focus != 1 {
		t.Fatal("picker search dispatched navigation")
	}
	m.pickerSearch.SetValue("loss")
	key(m, "enter")
	key(m, "space")
	for _, c := range m.runs().View.Columns {
		if c.Kind == "metric" && c.Key == "loss" {
			t.Fatal("checkbox did not remove column")
		}
	}
	key(m, "space")
	found := false
	for _, c := range m.runs().View.Columns {
		found = found || c.Kind == "metric" && c.Key == "loss"
	}
	if !found {
		t.Fatal("checkbox did not add column")
	}
	key(m, "esc")
	m.selectExperiment(1)
	if m.runs().View.Columns[0].Width != 28 {
		t.Fatal("other experiment view corrupted")
	}
}
func TestDatasetDetailAndLongExperimentInfo(t *testing.T) {
	m, _ := readyModel()
	m.run().Inputs.DatasetInputs = []core.DatasetInput{{Dataset: core.Dataset{Name: "train", Digest: "a"}}, {Dataset: core.Dataset{Name: "valid", Digest: "b"}}}
	m.tab = 5
	if text := strings.Join(m.detailLines(100, 20), "\n"); !strings.Contains(text, "train") || !strings.Contains(text, "valid") {
		t.Fatal("dataset inputs dropped")
	}
	m.focus = 0
	m.state().Experiments[0].Name = strings.Repeat("long 專案 ", 30)
	key(m, "i")
	m.width = 60
	m.height = 30
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "long 專案") {
		t.Fatal("full experiment info absent")
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 60 {
			t.Fatal("info exceeds terminal")
		}
	}
}

func TestHeaderSortAndWidthDragUseColumnIdentity(t *testing.T) {
	m, _ := readyModel()
	m.width = 180
	m.height = 40
	m.focus = 0
	id := columnID(m.runs().View.Columns[1])
	x, y := locateHit(t, m, "sort:"+id)
	mouseClick(m, x, y)
	if m.focus != 1 || columnID(m.runs().View.Sort[0].Column) != id {
		t.Fatal("column heading did not focus and sort runs")
	}
	x, y = locateHit(t, m, "width:"+id)
	before := columnWidth(m.runs().View.Columns[1])
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x + 8, Y: y, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: x + 8, Y: y, Button: tea.MouseLeft})
	if columnWidth(m.runs().View.Columns[1]) != before+8 {
		t.Fatal("header edge did not resize column")
	}
	// A preference response may replace the column under a pressed pointer. Its
	// coordinates cannot authorize sorting the replacement field on release.
	x, y = locateHit(t, m, "sort:"+id)
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	m.runs().View.Columns[1] = core.ColumnSpec{Kind: "param", Key: "replacement", Width: before + 8}
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if columnID(m.runs().View.Sort[0].Column) != id {
		t.Fatal("release activated a replacement column")
	}
}

func TestMouseClicksVisibleDetailTabs(t *testing.T) {
	m, _ := readyModel()
	m.width = 160
	m.height = 40
	m.focus = 1
	x, y := locateHit(t, m, "tab:5")
	mouseClick(m, x, y)
	if m.tab != 5 || m.focus != 2 {
		t.Fatal("dataset tab click did not focus details")
	}
	x, y = locateHit(t, m, "tab:2")
	mouseClick(m, x, y)
	if m.tab != 2 {
		t.Fatal("focused tab geometry drifted")
	}
}

func TestEmbeddedTargetFormMouseUsesInnerCoordinatesAfterResize(t *testing.T) {
	m, _ := readyModel()
	m.startTargetForm(core.Target{ID: "draft", TrackingURI: "http://localhost:5000"}, false)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Find actual rendered form labels; click the input row directly beneath the
	// display-name label. The outer header and frame must not shift the hit target.
	view := strings.Split(ansi.Strip(m.View().Content), "\n")
	nameY := -1
	for y, line := range view {
		if strings.Contains(line, "Display name (optional)") {
			nameY = y + 1
			break
		}
	}
	if nameY < 0 {
		t.Fatal("display name field not rendered")
	}
	mouseClick(m, 5, nameY)
	key(m, "MouseTarget")
	text := ansi.Strip(m.targetForm.View(98, 24))
	if !strings.Contains(text, "> Display name (optional)") || !strings.Contains(text, "MouseTarget") {
		t.Fatalf("form mouse coordinates selected wrong field: %s", text)
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	view = strings.Split(ansi.Strip(m.View().Content), "\n")
	cancelX, cancelY := -1, -1
	for y, line := range view {
		if x := strings.Index(line, "Esc cancel"); x >= 0 && y < m.height-3 {
			cancelX = ansi.StringWidth(line[:x])
			cancelY = y
			break
		}
	}
	if cancelY < 0 {
		t.Fatalf("form cancel not visible after resize: %v", view)
	}
	mouseClick(m, cancelX, cancelY)
	if m.targetForm != nil {
		t.Fatal("resized embedded form cancel hit used outer dimensions")
	}
}
