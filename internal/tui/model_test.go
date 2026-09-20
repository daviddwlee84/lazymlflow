package tui

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type fakeBackend struct {
	runQuery core.RunQuery
	expQuery core.ExperimentQuery
	history  []core.Metric
	calls    int
}

func (b *fakeBackend) SearchExperiments(_ context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
	b.expQuery = q
	b.calls++
	return core.ExperimentPage{}, nil
}
func (b *fakeBackend) GetExperiment(context.Context, string) (core.Experiment, error) {
	return core.Experiment{}, nil
}
func (b *fakeBackend) SearchRuns(_ context.Context, q core.RunQuery) (core.RunPage, error) {
	b.runQuery = q
	b.calls++
	return core.RunPage{}, nil
}
func (b *fakeBackend) GetRun(context.Context, string) (core.Run, error) { return core.Run{}, nil }
func (b *fakeBackend) MetricHistory(context.Context, string, string) ([]core.Metric, error) {
	return b.history, nil
}
func (b *fakeBackend) ListArtifacts(context.Context, string, string) (core.ArtifactPage, error) {
	b.calls++
	return core.ArtifactPage{}, nil
}
func (b *fakeBackend) DownloadArtifact(context.Context, core.DownloadRequest, func(core.Progress)) (core.DownloadResult, error) {
	return core.DownloadResult{}, nil
}

type delayedConnector struct {
	called  chan struct{}
	release chan struct{}
}
type connectorFunc func(context.Context, core.Target) (*core.Session, error)

func (f connectorFunc) Open(ctx context.Context, t core.Target) (*core.Session, error) {
	return f(ctx, t)
}
func (f connectorFunc) Close() error { return nil }

func (c *delayedConnector) Open(ctx context.Context, t core.Target) (*core.Session, error) {
	close(c.called)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return &core.Session{Target: t, Backend: &fakeBackend{}}, nil
	}
}
func (c *delayedConnector) Close() error { return nil }
func sampleRun(id string) core.Run {
	return core.Run{Info: core.RunInfo{RunID: id, RunName: "專案 é 👩🏽‍💻 " + id, ExperimentID: "e1", Status: "RUNNING", StartTime: 1000}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: 0.25, Step: 1}}, Params: []core.KeyValue{{Key: "lr", Value: "0.1"}}}}
}
func readyModel() (*model, *fakeBackend) {
	m := newModel(context.Background(), Options{Targets: []core.Target{{ID: "a", Name: "Alpha", TrackingURI: "http://localhost:1"}, {ID: "b", Name: "Beta", TrackingURI: "http://localhost:2"}}, MetricColumns: []string{"loss"}, ParameterColumns: []string{"lr"}})
	b := &fakeBackend{}
	s := m.state()
	s.Session = &core.Session{Backend: b, BaseURL: "http://localhost:1"}
	s.Experiments = []core.Experiment{{ID: "e1", Name: "First"}, {ID: "e2", Name: "Second"}}
	m.selectExperiment(0)
	s.Runs["e1"].Rows = []core.Run{sampleRun("r1"), sampleRun("r2"), sampleRun("r3")}
	m.selectRun(0)
	return m, b
}
func key(m *model, k string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		msg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		msg = tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		msg = tea.KeyPressMsg{Code: tea.KeyRight}
	case "space":
		msg = tea.KeyPressMsg{Code: ' ', Text: " "}
	default:
		rs := []rune(k)
		msg = tea.KeyPressMsg{Code: rs[0], Text: k}
	}
	_, cmd := m.Update(msg)
	return cmd
}

func TestStartupIsDeferredAndCanBeCancelled(t *testing.T) {
	c := &delayedConnector{make(chan struct{}), make(chan struct{})}
	m := newModel(context.Background(), Options{Targets: []core.Target{{ID: "slow"}}, Connector: c})
	cmd := m.connect()
	select {
	case <-c.called:
		t.Fatal("connect performed I/O before effect executed")
	default:
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "connecting") {
		t.Fatal("first frame did not display connecting state")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-c.called
	key(m, "?")
	if m.overlay != "help" {
		t.Fatal("help blocked during connection")
	}
	key(m, "esc")
	key(m, "esc")
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("startup cancellation did not reach connector")
	}
	if m.state().Session != nil || m.state().ConnectPending {
		t.Fatal("cancelled connection was accepted")
	}
}
func TestTypingOwnsPrintableKeysAndPaste(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	key(m, "/")
	for _, r := range "jkhql/?" {
		key(m, string(r))
	}
	if m.input.Value() != "jkhql/?" || m.overlay != "" || m.focus != 1 {
		t.Fatalf("typing dispatched navigation: %q %s %d", m.input.Value(), m.overlay, m.focus)
	}
	key(m, "enter")
	if m.inputMode != "" || m.focus != 1 {
		t.Fatal("Enter must accept local filter without opening details")
	}
	key(m, "/")
	m.Update(tea.PasteMsg{Content: "q\nj/"})
	if m.inputMode != "local" {
		t.Fatal("paste submitted or quit")
	}
	if m.runs().Local != m.input.Value() {
		t.Fatal("pasted local filter not applied")
	}
	key(m, "esc")
	if m.runs().Local != "" {
		t.Fatal("Escape did not clear local filter")
	}
}
func TestInputCursorMovementDoesNotResetSelection(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.startInput("local", "Search", "")
	key(m, "down")
	id := m.runs().Selected
	key(m, "left")
	if m.runs().Selected != id {
		t.Fatal("cursor-only input reset selected run")
	}
	key(m, "j")
	if m.input.Value() != "j" {
		t.Fatal("j should type in filter")
	}
}
func TestRunGenerationsRejectLateSuccessAndError(t *testing.T) {
	m, _ := readyModel()
	m.loadRuns(false)
	old := m.runs().Gen
	m.loadRuns(false)
	latest := m.runs().Gen
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: latest, page: core.RunPage{Runs: []core.Run{sampleRun("fresh")}}})
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: old, page: core.RunPage{Runs: []core.Run{sampleRun("stale")}}})
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: old, err: errors.New("old error")})
	if m.run().ID() != "fresh" || m.runs().Err != "" {
		t.Fatal("stale completion changed current view")
	}
}
func TestRefreshFollowsIdentityAndKeepsRowsOnFailure(t *testing.T) {
	m, _ := readyModel()
	m.selectRun(1)
	m.loadRuns(false)
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: m.runs().Gen, page: core.RunPage{Runs: []core.Run{sampleRun("r2"), sampleRun("r1")}}})
	// Equal start times now use the stable run ID tie-breaker. Selection
	// follows identity even though the server response is in a different order.
	if m.runs().Index != 1 || m.run().ID() != "r2" {
		t.Fatal("refresh selected by position instead of identity")
	}
	m.loadRuns(false)
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: m.runs().Gen, err: errors.New("offline")})
	if len(m.runs().Rows) != 2 || m.run().ID() != "r2" || m.runs().Err != "offline" {
		t.Fatal("failed refresh erased useful state")
	}
	m.loadRuns(false)
	m.acceptRuns(runsMsg{target: "a", experiment: "e1", gen: m.runs().Gen, page: core.RunPage{}})
	if len(m.runs().Rows) != 0 || m.run() != nil {
		t.Fatal("successful empty refresh kept obsolete rows")
	}
}
func TestServerQueryAndPagination(t *testing.T) {
	m, b := readyModel()
	r := m.runs()
	r.Filter = "metrics.loss < 0.5"
	r.Order = "metrics.loss ASC, attributes.start_time DESC"
	r.Next = "next-page"
	cmd := m.loadRuns(true)
	cmd()
	if b.runQuery.PageToken != "next-page" || b.runQuery.Filter != r.Filter || !reflect.DeepEqual(b.runQuery.OrderBy, []string{"metrics.loss ASC", "attributes.start_time DESC"}) {
		t.Fatalf("incorrect full query: %#v", b.runQuery)
	}
}
func TestTargetSwitchPreservesFilterSelectionAndBasket(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.selectRun(1)
	m.runs().Local = "專案"
	m.runs().Filter = "metrics.loss < 1"
	m.perform("basket")
	m.overlay = "targets"
	m.menuIndex = 1
	key(m, "enter")
	if m.active != "b" {
		t.Fatal("target not switched")
	}
	m.overlay = "targets"
	m.menuIndex = 0
	key(m, "enter")
	if m.run().ID() != "r2" || m.runs().Local != "專案" || len(m.state().Basket) != 1 {
		t.Fatal("target context lost")
	}
}
func TestViewDimensionsAndNoStateMutation(t *testing.T) {
	m, _ := readyModel()
	for _, size := range [][2]int{{1, 1}, {8, 3}, {20, 5}, {40, 12}, {80, 24}, {100, 30}, {160, 45}} {
		for _, focus := range []int{0, 1, 2} {
			m.width, m.height = size[0], size[1]
			m.focus = focus
			for _, overlay := range []string{"", "help", "targets"} {
				m.overlay = overlay
				view := m.View().Content
				lines := strings.Split(view, "\n")
				if len(lines) > m.height {
					t.Fatalf("%dx%d got %d rows", m.width, m.height, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > m.width {
						t.Fatalf("%dx%d line too wide: %q", m.width, m.height, line)
					}
				}
			}
		}
	}
	m.overlay = ""
	m.width = 20
	m.height = 5
	m.startInput("filter", "filter", "👩🏽‍💻")
	if len(strings.Split(m.View().Content, "\n")) > 5 {
		t.Fatal("input exceeded tiny terminal")
	}
	m.closeInput()
	m.focus = 2
	m.tab = 4
	before := len(m.state().Artifacts)
	m.View()
	if len(m.state().Artifacts) != before {
		t.Fatal("View mutated artifact cache")
	}
}
func TestUntrustedTerminalControlsAreRemoved(t *testing.T) {
	value := "hello\x1b]52;c;secret\x07\x1b[2J\nworld"
	safe := clean(value)
	if strings.ContainsAny(safe, "\x1b\x07\n") {
		t.Fatalf("unsafe display: %q", safe)
	}
}
func TestComparisonMissingIsDistinctFromZeroAndEmpty(t *testing.T) {
	a, b := sampleRun("a"), sampleRun("b")
	a.Data.Metrics = []core.Metric{{Key: "zero", Value: 0}, {Key: "nan", Value: core.Number(math.NaN())}}
	b.Data.Metrics = []core.Metric{{Key: "nan", Value: core.Number(math.NaN())}}
	a.Data.Params = []core.KeyValue{{Key: "empty", Value: ""}}
	b.Data.Params = nil
	rows := comparisonRows([]core.Run{a, b}, true)
	values := map[string][]string{}
	for _, r := range rows {
		values[r.Key] = r.Values
	}
	if !reflect.DeepEqual(values["metric/zero"], []string{"0", "—"}) {
		t.Fatalf("missing metric treated as zero: %#v", values)
	}
	if !reflect.DeepEqual(values["param/empty"], []string{"", "—"}) {
		t.Fatal("missing parameter treated as empty")
	}
	if _, ok := values["metric/nan"]; ok {
		t.Fatal("equal NaN display values marked different")
	}
}
func TestHistoryPreservesDuplicatesAndElapsedAxis(t *testing.T) {
	r := sampleRun("a")
	h := []core.Metric{{Step: 2, Timestamp: 3000, Value: 3}, {Step: 1, Timestamp: 2000, Value: 1}, {Step: 1, Timestamp: 2500, Value: 2}, {Step: 4, Timestamp: 5000, Value: core.Number(math.NaN())}, {Step: 5, Timestamp: 6000, Value: core.Number(math.Inf(1))}}
	before := append([]core.Metric(nil), h...)
	s := historySeries(r, h, false)
	if s.Total != 5 || s.Nonfinite != 2 || s.DuplicateX != 1 || len(s.Points) != 3 {
		t.Fatalf("history lost data: %#v", s)
	}
	if s.Points[0].Y != 1 || s.Points[1].Y != 2 {
		t.Fatal("duplicate steps lost stable ordering")
	}
	elapsed := historySeries(r, h, true)
	if elapsed.Points[0].X != 1 || elapsed.Points[1].X != 1.5 {
		t.Fatal("elapsed axis not relative to run start")
	}
	for i := range h {
		if h[i].Step != before[i].Step {
			t.Fatal("plot mutated raw history")
		}
	}
	if len(plot([]plotSeries{s}, 1, 1)) == 0 {
		t.Fatal("tiny plot failed")
	}
}
func TestArtifactNavigationIgnoresStaleDirectoryResponse(t *testing.T) {
	m, _ := readyModel()
	m.focus = 2
	m.tab = 4
	m.loadArtifacts()
	oldKey, oldGen := m.artifactKey(), m.artifacts().Gen
	m.artifacts().Rows = []core.Artifact{{Path: "nested", IsDir: true}}
	m.artifactEnter()
	newGen := m.artifacts().Gen
	m.acceptArtifacts(artifactsMsg{target: "a", key: m.artifactKey(), gen: newGen, page: core.ArtifactPage{Files: []core.Artifact{{Path: "nested/file.txt"}}}})
	m.acceptArtifacts(artifactsMsg{target: "a", key: oldKey, gen: oldGen, page: core.ArtifactPage{Files: []core.Artifact{{Path: "wrong.txt"}}}})
	if m.currentPath() != "nested" || m.artifacts().Rows[0].Path != "nested/file.txt" {
		t.Fatal("stale artifact result replaced current directory")
	}
	m.artifactParent()
	if m.currentPath() != "" {
		t.Fatal("parent did not return to root")
	}
}

func TestCancelledDownloadCannotOpenOverwriteOnAnotherTarget(t *testing.T) {
	m, _ := readyModel()
	m.downloadPending = true
	m.downloadGen = 10
	m.seq = 10
	m.stopAll()
	m.active = "b"
	m.acceptDownload(downloadedMsg{target: "a", gen: 10, exists: true})
	if m.overlay != "" || m.downloadPending {
		t.Fatal("cancelled download affected new target")
	}
}

func TestAutoRefreshDoesNotRestartPendingConnection(t *testing.T) {
	m, _ := readyModel()
	m.state().ConnectPending = true
	m.state().ConnectGen = 8
	m.Update(refreshMsg{})
	if m.state().ConnectGen != 8 {
		t.Fatal("auto refresh restarted pending connection")
	}
}

func TestComparisonDoesNotRoundAwayCloseMetricDifferences(t *testing.T) {
	a, b := sampleRun("a"), sampleRun("b")
	a.Data.Metrics = []core.Metric{{Key: "score", Value: 0.1234567801}}
	b.Data.Metrics = []core.Metric{{Key: "score", Value: 0.1234567802}}
	rows := comparisonRows([]core.Run{a, b}, true)
	if len(rows) != 1 || rows[0].Key != "metric/score" || rows[0].Values[0] == rows[0].Values[1] {
		t.Fatal("close but distinct metric values were treated as equal")
	}
}

func TestDashboardEmbedsSharedRuntimeForm(t *testing.T) {
	m, _ := readyModel()
	m.opts.ConfigPath = "/tmp/dashboard-config.toml"
	m.startTargetForm(core.Target{ID: "new", TrackingURI: "http://example.com", Python: "/project/.venv"}, false)
	if m.targetForm == nil {
		t.Fatal("dashboard did not embed shared form")
	}
	if !strings.Contains(m.View().Content, "Tracking target configuration") {
		t.Fatal("form not rendered inside dashboard")
	}
	key(m, "q")
	if m.targetForm.Cancelled {
		t.Fatal("typing q cancelled dashboard form")
	}
	key(m, "esc")
	if m.targetForm != nil {
		t.Fatal("cancelled form remained mounted")
	}
}

func TestCompareRefreshPreservesBasketAndRejectsLateErrors(t *testing.T) {
	m, _ := readyModel()
	m.focus = 1
	m.perform("basket")
	m.selectRun(1)
	m.perform("basket")
	m.perform("compare")
	old := m.state().CompareGen
	m.loadComparison()
	latest := m.state().CompareGen
	run := sampleRun("r1")
	run.Data.Metrics[0].Value = 0.75
	m.acceptCompared(comparedMsg{target: "a", gen: latest, runs: []core.Run{run, sampleRun("r2")}})
	m.acceptCompared(comparedMsg{target: "a", gen: old, err: errors.New("old error")})
	if m.state().Basket["r1"].Data.Metrics[0].Value != 0.75 || m.state().CompareErr != "" {
		t.Fatal("comparison refresh was lost or stale failure accepted")
	}
	m.loadComparison()
	m.acceptCompared(comparedMsg{target: "a", gen: m.state().CompareGen, err: errors.New("offline")})
	if len(m.state().Basket) != 2 || m.state().CompareErr != "offline" {
		t.Fatal("comparison failure erased selected runs")
	}
}

func TestComparisonIdentifiesDuplicateNamesAndExperiments(t *testing.T) {
	m, _ := readyModel()
	a, b := sampleRun("abcdefgh11111111"), sampleRun("ijklmnop22222222")
	a.Info.RunName = "same-name"
	b.Info.RunName = "same-name"
	a.Info.ExperimentID = "123456789012345678"
	b.Info.ExperimentID = "987654321098765432"
	s := m.state()
	s.Basket = map[string]core.Run{a.ID(): a, b.ID(): b}
	s.BasketOrder = []string{a.ID(), b.ID()}
	m.compare = true
	view := strings.Join(m.compareLines(100, 25), "\n")
	for _, want := range []string{"abcdefgh", "ijklmnop", a.Info.ExperimentID, b.Info.ExperimentID} {
		if !strings.Contains(view, want) {
			t.Fatalf("comparison omitted identity %q", want)
		}
	}
	m.metric = "loss"
	s.Histories[a.ID()+"\x00loss"] = []core.Metric{{Value: 0.25}}
	s.Histories[b.ID()+"\x00loss"] = []core.Metric{{Value: 0.5}}
	chart := strings.Join(m.chartLines(120, 25), "\n")
	if !strings.Contains(chart, "abcdefgh") || !strings.Contains(chart, "ijklmnop") {
		t.Fatal("chart legend omitted run IDs")
	}
}

func TestSingleRunHistoryIgnoresComparisonBasketAndPlotsFiniteSamples(t *testing.T) {
	m, _ := readyModel()
	s := m.state()
	s.Basket["other"] = sampleRun("other")
	s.BasketOrder = []string{"other"}
	m.compare = false
	m.selectRun(1)
	m.metric = "loss"
	s.Histories["r2\x00loss"] = []core.Metric{{Step: 0, Value: core.Number(math.NaN())}, {Step: 1, Value: core.Number(math.Inf(1))}, {Step: 2, Value: 0.125}}
	runs := m.selectedRuns()
	if len(runs) != 1 || runs[0].ID() != "r2" {
		t.Fatal("single-run history used comparison basket")
	}
	chart := strings.Join(m.chartLines(100, 20), "\n")
	if !strings.Contains(chart, "0.125") || !strings.Contains(chart, "2 nonfinite") || strings.Contains(chart, "other") {
		t.Fatalf("incorrect finite history chart: %s", chart)
	}
}

func TestEditingTargetClosesPriorSessionBeforeOpeningReplacement(t *testing.T) {
	m, b := readyModel()
	var events []string
	old := m.state().Session
	old.CloseFunc = func() error { events = append(events, "close"); return nil }
	m.opts.Connector = connectorFunc(func(_ context.Context, target core.Target) (*core.Session, error) {
		events = append(events, "open")
		return &core.Session{Backend: b, Target: target}, nil
	})
	cmd := m.acceptSaved(savedMsg{targets: m.targets, selected: "a"})
	if len(events) != 0 {
		t.Fatal("editing target performed lifecycle I/O in Update")
	}
	if cmd == nil {
		t.Fatal("replacement connection effect missing")
	}
	m.Update(cmd())
	if !reflect.DeepEqual(events, []string{"close", "open"}) {
		t.Fatalf("replacement lifecycle order: %v", events)
	}
	if m.state().Session == old || m.state().Retiring != nil {
		t.Fatal("old target session retained after replacement")
	}
}

func TestSaveFailureKeepsSessionAndFormControlCCancelsOnlyForm(t *testing.T) {
	m, _ := readyModel()
	old := m.state().Session
	closed := false
	old.CloseFunc = func() error { closed = true; return nil }
	m.draft = m.targets[0]
	m.draftEdit = true
	m.acceptSaved(savedMsg{err: errors.New("read-only config")})
	if closed || m.state().Session != old || m.targetForm == nil {
		t.Fatal("failed save retired current session")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("form Ctrl+C quit dashboard")
		}
	}
	if m.targetForm != nil || m.state().Session != old || closed {
		t.Fatal("form cancellation changed dashboard connection")
	}
}
