package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type fixtureBackend struct {
	get       func(context.Context, string) (core.Run, error)
	search    func(context.Context, core.RunQuery) (core.RunPage, error)
	history   func(context.Context, string, string) ([]core.Metric, error)
	artifacts func(context.Context, string, string) (core.ArtifactPage, error)
}

func (*fixtureBackend) SearchExperiments(context.Context, core.ExperimentQuery) (core.ExperimentPage, error) {
	return core.ExperimentPage{}, nil
}
func (*fixtureBackend) GetExperiment(_ context.Context, id string) (core.Experiment, error) {
	return core.Experiment{ID: id, Name: "experiment", LifecycleStage: "active"}, nil
}
func (b *fixtureBackend) SearchRuns(ctx context.Context, q core.RunQuery) (core.RunPage, error) {
	if b.search != nil {
		return b.search(ctx, q)
	}
	return core.RunPage{}, nil
}
func (b *fixtureBackend) GetRun(ctx context.Context, id string) (core.Run, error) {
	if b.get != nil {
		return b.get(ctx, id)
	}
	return fixtureRun(id), nil
}
func (b *fixtureBackend) MetricHistory(ctx context.Context, id, key string) ([]core.Metric, error) {
	if b.history != nil {
		return b.history(ctx, id, key)
	}
	return []core.Metric{{Key: key, Step: 0, Timestamp: 100, Value: 3}, {Key: key, Step: 1, Timestamp: 200, Value: 1}, {Key: key, Step: 2, Timestamp: 300, Value: 2}}, nil
}
func (b *fixtureBackend) ListArtifacts(ctx context.Context, id, path string) (core.ArtifactPage, error) {
	if b.artifacts != nil {
		return b.artifacts(ctx, id, path)
	}
	return core.ArtifactPage{RootURI: "mlflow-artifacts:/root", Files: []core.Artifact{{Path: "z.txt", FileSize: 1}, {Path: "a", IsDir: true}}}, nil
}
func (*fixtureBackend) DownloadArtifact(context.Context, core.DownloadRequest, func(core.Progress)) (core.DownloadResult, error) {
	return core.DownloadResult{}, errors.New("collector must never download artifacts")
}

type fixtureNotes struct {
	err      error
	mu       sync.Mutex
	subjects []core.Subject
}

func (n *fixtureNotes) ListNotes(_ context.Context, s core.Subject, _ bool) ([]core.Note, error) {
	n.mu.Lock()
	n.subjects = append(n.subjects, s)
	n.mu.Unlock()
	if n.err != nil {
		return nil, n.err
	}
	return []core.Note{{ID: "note", Subject: s, Body: "observed only", CreatedAt: 100, UpdatedAt: 100, Revision: 1}, {ID: "deleted", DeletedAt: 1}}, nil
}
func (*fixtureNotes) SaveNote(context.Context, core.Note, int) (core.Note, error) {
	panic("write forbidden")
}
func (*fixtureNotes) DeleteNote(context.Context, core.Subject, string, int, bool) (core.Note, error) {
	panic("write forbidden")
}
func fixtureRun(id string) core.Run {
	return core.Run{Info: core.RunInfo{RunID: id, ExperimentID: "e", RunName: "Run " + id, Status: "FINISHED", StartTime: 1000, EndTime: 2000}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: 2, Step: 2, Timestamp: 300}, {Key: "system/cpu", Value: 10}}, Params: []core.KeyValue{{Key: "batch size", Value: "32"}}, Tags: []core.KeyValue{{Key: "description", Value: "test"}}}, Inputs: core.RunInputs{DatasetInputs: []core.DatasetInput{{Dataset: core.Dataset{Name: "data", Digest: "abc", Schema: `[{"type":"double","name":"x"}]`, Profile: `{"num_rows":23}`}, Tags: []core.KeyValue{{Key: "mlflow.data.context", Value: "training"}}}}}}
}
func collector(b core.Backend, notes core.NoteStore) *Collector {
	c := NewCollector(b, core.Target{ID: "lab", Name: "Lab", TrackingURI: "https://user:password@tracking.example/base?token=secret", TokenEnv: "ENV_REF"}, notes)
	c.Now = func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }
	return c
}

func TestRunContextCapturesFullEvidenceAndDetachesMetadata(t *testing.T) {
	run := fixtureRun("r")
	b := &fixtureBackend{get: func(context.Context, string) (core.Run, error) { return run, nil }}
	notes := &fixtureNotes{}
	snapshot, err := collector(b, notes).CollectRuns(context.Background(), []string{"r", "r"}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 1 || snapshot.Version != 1 || len(snapshot.Warnings) != 0 {
		t.Fatalf("snapshot %+v", snapshot)
	}
	r := snapshot.Runs[0]
	if !r.MetadataComplete || len(r.Run.Data.Metrics) != 2 || !reflect.DeepEqual(r.SelectedMetrics, []string{"loss"}) {
		t.Fatal("latest/system/selected metric evidence lost")
	}
	if r.Histories[0].Summary.Count != 3 || r.Histories[0].Summary.Min.Value != 1 || r.Run.Data.Metrics[0].Value != 2 {
		t.Fatal("latest and history extrema conflated")
	}
	if r.Run.Inputs.DatasetInputs[0].Dataset.Schema != run.Inputs.DatasetInputs[0].Dataset.Schema || len(r.DatasetNotes) != 1 || len(r.Notes.Notes) != 1 {
		t.Fatal("dataset/notes evidence lost")
	}
	if r.Artifacts.Scope == "" || r.Artifacts.Files[0].Path != "a" {
		t.Fatal("artifact scope or order missing")
	}
	encoded, _ := ContextJSON(snapshot)
	if strings.Contains(encoded, "password") || strings.Contains(encoded, "token=secret") || strings.Contains(encoded, "ENV_REF") {
		t.Fatal("source contains authentication information")
	}
	run.Data.Params[0].Value = "changed"
	run.Inputs.DatasetInputs[0].Dataset.Schema = "changed"
	if snapshot.Runs[0].Run.Data.Params[0].Value != "32" || snapshot.Runs[0].Run.Inputs.DatasetInputs[0].Dataset.Schema == "changed" {
		t.Fatal("snapshot shares mutable backend state")
	}
}
func TestHistorySamplingStatisticsNonfiniteAndOmission(t *testing.T) {
	history := make([]core.Metric, 1000)
	for i := range history {
		history[i] = core.Metric{Key: "loss", Value: core.Number(i), Step: int64(i), Timestamp: int64(i * 100)}
	}
	history[500].Value = core.Number(math.NaN())
	history[700].Value = -10
	var calls atomic.Int32
	b := &fixtureBackend{history: func(context.Context, string, string) ([]core.Metric, error) { calls.Add(1); return history, nil }}
	c := collector(b, &fixtureNotes{})
	s, err := c.CollectRuns(context.Background(), []string{"r"}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := s.Runs[0].Histories[0]
	if len(h.Samples) != 200 || !h.Sampled || h.Summary.Count != 1000 || h.Summary.NonFiniteCount != 1 || h.Summary.Min.Step != 700 {
		t.Fatalf("wrong history summary/sample %+v", h.Summary)
	}
	o := DefaultOptions()
	o.History = HistoryFull
	s, err = c.CollectRuns(context.Background(), []string{"r"}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Runs[0].Histories[0].Samples) != 1000 || s.Runs[0].Histories[0].Sampled {
		t.Fatal("full history sampled")
	}
	o.History = HistoryNone
	s, err = c.CollectRuns(context.Background(), []string{"r"}, o)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || s.Runs[0].Histories[0].Status != "omitted" {
		t.Fatal("none still fetched history")
	}
}
func TestExplicitMetricAndSystemSelection(t *testing.T) {
	b := &fixtureBackend{}
	for _, test := range []struct {
		o    Options
		want []string
	}{{Options{Metrics: []string{"system/cpu", "missing"}}, []string{"system/cpu", "missing"}}, {Options{IncludeSystem: true}, []string{"loss", "system/cpu"}}} {
		s, err := collector(b, &fixtureNotes{}).CollectRuns(context.Background(), []string{"r"}, test.o)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(s.Runs[0].SelectedMetrics, test.want) {
			t.Fatalf("selected %v want %v", s.Runs[0].SelectedMetrics, test.want)
		}
	}
}
func TestOptionalFailuresRemainExplicitAndRequiredFailureReturnsError(t *testing.T) {
	b := &fixtureBackend{history: func(context.Context, string, string) ([]core.Metric, error) { return nil, errors.New("history failed") }, artifacts: func(context.Context, string, string) (core.ArtifactPage, error) {
		return core.ArtifactPage{}, errors.New("artifact failed")
	}}
	s, err := collector(b, &fixtureNotes{err: errors.New("notes locked")}).CollectRuns(context.Background(), []string{"r"}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 4 || s.Runs[0].Histories[0].Status != "error" || s.Runs[0].Artifacts.Status != "error" {
		t.Fatalf("optional errors missing: %+v", s.Warnings)
	}
	b.get = func(context.Context, string) (core.Run, error) { return core.Run{}, errors.New("run inaccessible") }
	if _, err = collector(b, nil).CollectRuns(context.Background(), []string{"r"}, DefaultOptions()); err == nil || !strings.Contains(err.Error(), "run inaccessible") {
		t.Fatal("required failure swallowed", err)
	}
}
func TestExperimentScansAllPagesAndDetailsFirstTwenty(t *testing.T) {
	var queries []core.RunQuery
	var mu sync.Mutex
	var detailed []string
	b := &fixtureBackend{search: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		queries = append(queries, q)
		start, end, token := 0, 17, "page2"
		if q.PageToken == "page2" {
			start, end, token = 17, 25, ""
		}
		runs := []core.Run{}
		for i := start; i < end; i++ {
			runs = append(runs, fixtureRun(fmt.Sprintf("r%02d", i)))
		}
		return core.RunPage{Runs: runs, NextPageToken: token}, nil
	}, get: func(_ context.Context, id string) (core.Run, error) {
		mu.Lock()
		detailed = append(detailed, id)
		mu.Unlock()
		return fixtureRun(id), nil
	}}
	c := collector(b, &fixtureNotes{})
	s, err := c.CollectExperiment(context.Background(), "e", ExperimentOptions{Options: Options{History: HistoryNone}, Query: core.RunQuery{Filter: "params.task = 'x'"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || len(s.Experiment.MatchingRuns) != 25 || len(s.Runs) != 20 || len(detailed) != 20 || s.Selection.MatchingRuns != 25 {
		t.Fatalf("scan/detail counts wrong %+v", s.Selection)
	}
	if s.Runs[0].Run.ID() != "r00" || s.Runs[19].Run.ID() != "r19" || s.Selection.AllDetails {
		t.Fatal("detail selection wrong")
	}
	for _, q := range queries {
		if q.Filter != "params.task = 'x'" || q.OrderBy[0] != "attributes.start_time DESC" || q.ExperimentIDs[0] != "e" {
			t.Fatal("query changed", q)
		}
	}
	detailed = nil
	queries = nil
	s, err = c.CollectExperiment(context.Background(), "e", ExperimentOptions{AllDetails: true, Options: Options{History: HistoryNone}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Runs) != 25 || !s.Selection.AllDetails {
		t.Fatal("all details ignored")
	}
}
func TestExperimentPaginationFailureDoesNotClaimComplete(t *testing.T) {
	b := &fixtureBackend{search: func(context.Context, core.RunQuery) (core.RunPage, error) {
		return core.RunPage{Runs: []core.Run{fixtureRun("r")}, NextPageToken: "repeated"}, nil
	}}
	if _, err := collector(b, nil).CollectExperiment(context.Background(), "e", ExperimentOptions{}); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatal("page cycle ignored", err)
	}
}
func TestCollectorBoundedConcurrencyAndCancellation(t *testing.T) {
	var active, maxActive atomic.Int32
	gate := make(chan struct{})
	b := &fixtureBackend{get: func(ctx context.Context, id string) (core.Run, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); now > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, now) {
				break
			}
		}
		select {
		case <-gate:
			return fixtureRun(id), nil
		case <-ctx.Done():
			return core.Run{}, ctx.Err()
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := collector(b, nil).CollectRuns(ctx, []string{"1", "2", "3", "4", "5", "6", "7", "8"}, DefaultOptions())
		done <- err
	}()
	deadline := time.After(time.Second)
	for maxActive.Load() < 4 {
		select {
		case <-deadline:
			t.Fatal("workers did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation swallowed", err)
	}
	if maxActive.Load() > 4 || active.Load() != 0 {
		t.Fatal("unbounded/leaked I/O")
	}
}
func TestJSONRoundTripPreservesNonfinite(t *testing.T) {
	b := &fixtureBackend{history: func(context.Context, string, string) ([]core.Metric, error) {
		return []core.Metric{{Key: "loss", Value: core.Number(math.Inf(1))}}, nil
	}}
	snapshot, err := collector(b, &fixtureNotes{}).CollectRuns(context.Background(), []string{"r"}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ContextJSON(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Snapshot
	if err = json.Unmarshal([]byte(raw), &recovered); err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(float64(recovered.Runs[0].Histories[0].Samples[0].Value), 1) {
		t.Fatal("nonfinite value changed")
	}
}
