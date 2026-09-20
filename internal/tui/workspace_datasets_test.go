package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func datasetWorkspaceRun(id, experiment string) core.Run {
	r := sampleRun(id)
	r.Info.ExperimentID = experiment
	r.Inputs.DatasetInputs = []core.DatasetInput{{Dataset: core.Dataset{Name: "Features", Digest: "digest", SourceType: "local", Schema: `{"mlflow_colspec":[{"name":"obj","type":"object","properties":{"x":{"type":"double"},"y":{"type":"long"}}},{"name":"flat","type":"string"}]}`, Profile: `{"num_rows":4558}`}, Tags: []core.KeyValue{{Key: "mlflow.data.context", Value: "training"}}}}
	return r
}
func datasetWorkspaceModel() *model {
	m, _ := readyModel()
	m.width = 120
	m.height = 40
	m.initWorkspace()
	m.state().Runs["e1"].Rows = []core.Run{datasetWorkspaceRun("a", "e1"), datasetWorkspaceRun("b", "e1")}
	m.openCatalog()
	return m
}

func TestCatalogProgressSelectionAndSourceIsolation(t *testing.T) {
	m := datasetWorkspaceModel()
	c := m.catalogState()
	source := core.SourceKey(m.target())
	if !strings.Contains(m.catalogProgress(c), "Partial: loaded runs") {
		t.Fatal(m.catalogProgress(c))
	}
	c.RunIndex = 1
	c.Gen = 42
	updated := core.CatalogFromRuns(source, m.state().Experiments, []core.Run{datasetWorkspaceRun("b", "e1"), datasetWorkspaceRun("a", "e1")})
	m.acceptCatalog(catalogMsg{source: source, gen: 42, value: updated, done: true})
	if use := m.catalogUse(); use == nil || use.RunID != "b" || c.RunIndex != 0 {
		t.Fatalf("selection moved %+v", use)
	}
	otherSource := core.SourceKey(m.targets[1])
	other := &catalogState{Value: core.CatalogFromRuns(otherSource, []core.Experiment{{ID: "e2"}}, []core.Run{datasetWorkspaceRun("other1", "e2"), datasetWorkspaceRun("other2", "e2"), datasetWorkspaceRun("other3", "e2")}), Collapsed: map[string]bool{}, Index: 1, RunIndex: 2}
	m.work.catalogs[otherSource] = other
	m.rebuildCatalogRows(other)
	if other.RunIndex != 2 {
		t.Fatalf("other source clamped against active source: %d", other.RunIndex)
	}
	old := c.Value
	m.acceptCatalog(catalogMsg{source: source, gen: 41, value: core.DatasetCatalog{}, done: true})
	if !reflect.DeepEqual(c.Value, old) {
		t.Fatal("stale catalog accepted")
	}
}

func TestCatalogFailedEmptyRefreshKeepsPreviousRowsButSuccessClears(t *testing.T) {
	m := datasetWorkspaceModel()
	c := m.catalogState()
	c.Gen = 7
	source := core.SourceKey(m.target())
	empty := core.DatasetCatalog{Source: source, Scope: "target", ViewType: "ACTIVE_ONLY"}
	before := c.Value.Entries[0].ID
	m.acceptCatalog(catalogMsg{source: source, gen: 7, value: empty, done: true, err: errors.New("offline")})
	if !c.Stale || len(c.Value.Entries) != 1 || c.Value.Entries[0].ID != before || !strings.Contains(m.catalogProgress(c), "previous rows shown") {
		t.Fatal("failed refresh erased the usable snapshot")
	}
	empty.Complete = true
	c.Gen = 8
	m.acceptCatalog(catalogMsg{source: source, gen: 8, value: empty, done: true})
	if c.Stale || len(c.Value.Entries) != 0 {
		t.Fatal("successful empty scan retained obsolete rows")
	}
}

func TestCatalogRefreshLoadedSeedsContextsAndSchemaNavigation(t *testing.T) {
	m := datasetWorkspaceModel()
	c := m.catalogState()
	m.focus = 2
	if fields := m.catalogFields(); len(fields) != 4 {
		t.Fatal(fields)
	}
	m.catalogKey(nil, "h")
	if fields := m.catalogFields(); len(fields) != 2 {
		t.Fatalf("not collapsed %+v", fields)
	}
	c.SchemaQuery = "obj.x"
	if fields := m.catalogFields(); len(fields) != 1 || fields[0].Path != "obj.x" {
		t.Fatalf("search did not traverse collapsed branch %+v", fields)
	}
	c.SchemaQuery = ""
	m.catalogKey(nil, "l")
	if len(m.catalogFields()) != 4 {
		t.Fatal("not expanded")
	}
	m.catalogMove(99999)
	if c.DetailIndex != 3 {
		t.Fatal(c.DetailIndex)
	}
	u := m.catalogUse()
	if u == nil {
		t.Fatal("no use")
	}
	metadata := strings.Join(m.catalogMetadata(m.catalogEntry(), 200), "\n")
	if !strings.Contains(metadata, "Selected use:") || !strings.Contains(metadata, "input 1") || !strings.Contains(metadata, "4558") {
		t.Fatal(metadata)
	}
	m.closeCatalog()
	r := datasetWorkspaceRun("new", "e2")
	r.Inputs.DatasetInputs[0].Dataset.Digest = "new"
	m.state().Runs["e2"] = &runState{Rows: []core.Run{r}}
	m.openCatalog()
	if len(m.catalogState().Value.Entries) != 2 {
		t.Fatal("reopened seed ignores newly loaded runs")
	}
	// Similar context names remain distinct and both uses stay available in metadata.
	d := m.catalogEntry()
	base := d.Uses[0]
	base.Context = "train"
	d.Uses = append(d.Uses, base)
	if got := m.catalogUses()[0].Context; !strings.Contains(got, "training / train") {
		t.Fatal(got)
	}
}

func TestCatalogJumpPreservesCachedViewsAndRejectsClosedRequest(t *testing.T) {
	m := datasetWorkspaceModel()
	c := m.catalogState()
	s := m.state()
	source := core.SourceKey(m.target())
	existing := &runState{listState: listState{Selected: "sibling", Local: "does not match", Filter: "metrics.loss > 1", Gen: 99, Pending: true}, Rows: []core.Run{datasetWorkspaceRun("sibling", "e2")}, View: core.DefaultView([]string{"loss"}, []string{"lr"})}
	existing.View.Mode = "grouped"
	existing.View.GroupBy = []string{"lr"}
	existing.View.Expansion = "collapsed"
	s.Runs["e2"] = existing
	s.Visibility[core.VisibilityKey("run", "related")] = core.VisibilityHidden
	s.Visibility[core.VisibilityKey("experiment", "e2")] = core.VisibilityArchived
	originalView := existing.View
	c.RunGen = 7
	message := catalogRunMsg{source: source, gen: 7, run: datasetWorkspaceRun("related", "e2"), experiment: core.Experiment{ID: "e2", Name: "Second"}}
	m.acceptCatalogRun(message)
	if s.Runs["e2"] != existing || len(existing.Rows) != 2 || !reflect.DeepEqual(existing.View, originalView) || existing.Local != "does not match" || existing.Filter != "metrics.loss > 1" {
		t.Fatal("jump destroyed existing state")
	}
	if existing.Pending || existing.Gen == 99 || m.run() == nil || m.run().ID() != "related" || m.focus != 2 {
		t.Fatal("jump not selected or old query still active")
	}
	m.acceptRuns(runsMsg{target: m.active, experiment: "e2", gen: 99, page: core.RunPage{Runs: []core.Run{sampleRun("stale")}}})
	if m.run() == nil || m.run().ID() != "related" || len(existing.Rows) != 2 {
		t.Fatal("late pre-jump query overwrote inspection")
	}
	rows := m.catalogInspectionRows(core.BuildRunRows(existing.Rows, existing.View, s.Visibility))
	found := false
	for _, row := range rows {
		if row.ID == "related" && row.Run != nil && row.Note == "inspection context" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit hidden run not available")
	}
	if visible := m.runRows(); len(visible) != 1 || visible[0].ID != "related" {
		t.Fatal("inspection hook did not bypass local filters and hidden visibility")
	}
	if visible := m.experimentsVisible(); len(visible) != 2 || visible[s.Index].ID != "e2" {
		t.Fatal("experiment index does not match explicit inspection")
	}
	if visible := m.catalogInspectionExperiments([]core.Experiment{{ID: "e1"}}); len(visible) != 2 {
		t.Fatal("explicit hidden experiment not available")
	}
	m.openCatalog()
	if c.InspectionRunID != "" {
		t.Fatal("inspection context leaked after returning")
	}
	c.RunGen = 20
	m.closeCatalog()
	m.openCatalog()
	message.gen = 20
	m.acceptCatalogRun(message)
	if !m.work.catalog {
		t.Fatal("closed asynchronous jump accepted after reopening")
	}
}

type datasetScanBackend struct {
	core.Backend
	release chan struct{}
}

func (b *datasetScanBackend) SearchExperiments(context.Context, core.ExperimentQuery) (core.ExperimentPage, error) {
	return core.ExperimentPage{Experiments: []core.Experiment{{ID: "e1", Name: "First"}}}, nil
}
func (b *datasetScanBackend) SearchRuns(ctx context.Context, _ core.RunQuery) (core.RunPage, error) {
	select {
	case <-b.release:
		return core.RunPage{Runs: []core.Run{datasetWorkspaceRun("scanned", "e1")}}, nil
	case <-ctx.Done():
		return core.RunPage{}, ctx.Err()
	}
}

func TestCatalogScanDeferredCancellationRetainsSnapshot(t *testing.T) {
	m := datasetWorkspaceModel()
	b := &datasetScanBackend{release: make(chan struct{})}
	m.state().Session.Backend = b
	cmd := m.scanCatalog()
	if !m.catalogState().Pending || cmd == nil {
		t.Fatal("scan not started")
	}
	message := cmd().(catalogMsg)
	next := m.acceptCatalog(message)
	if next == nil {
		t.Fatal("scan did not stream progress")
	}
	m.cancelCatalog()
	if m.catalogState().Pending || !m.catalogState().Value.Cancelled {
		t.Fatal("cancel not reflected immediately")
	}
	closed := make(chan struct{})
	go func() {
		for next != nil {
			value := next()
			if value == nil {
				break
			}
			next = m.acceptCatalog(value.(catalogMsg))
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancelled scan channel did not close")
	}
	if !strings.Contains(m.catalogProgress(m.catalogState()), "cancelled") {
		t.Fatal("cancel reason not visible")
	}
	prior := m.catalogState().Value
	m.acceptCatalog(catalogMsg{source: core.SourceKey(m.target()), gen: m.catalogState().Gen - 1, value: core.DatasetCatalog{}, done: true, err: errors.New("late")})
	if !reflect.DeepEqual(prior, m.catalogState().Value) {
		t.Fatal("cancelled scan overwrote retained snapshot")
	}
}
