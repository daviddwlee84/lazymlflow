package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
)

type catalogBackend struct {
	Backend
	experiments func(context.Context, ExperimentQuery) (ExperimentPage, error)
	runs        func(context.Context, RunQuery) (RunPage, error)
}

func (b catalogBackend) SearchExperiments(ctx context.Context, q ExperimentQuery) (ExperimentPage, error) {
	return b.experiments(ctx, q)
}
func (b catalogBackend) SearchRuns(ctx context.Context, q RunQuery) (RunPage, error) {
	return b.runs(ctx, q)
}
func (b catalogBackend) GetExperiment(_ context.Context, id string) (Experiment, error) {
	return Experiment{ID: id, Name: "Experiment " + id}, nil
}

func catalogRun(id, name string) Run {
	return Run{Info: RunInfo{RunID: id, ExperimentID: "exp", RunName: id}, Inputs: RunInputs{DatasetInputs: []DatasetInput{{Dataset: Dataset{Name: name, Digest: "digest", SourceType: "local", Schema: `{"mlflow_colspec":[{"name":"x","type":"double","required":true}]}`, Profile: `{"num_rows":5}`}, Tags: []KeyValue{{Key: "mlflow.data.context", Value: "training"}}}}}}
}

func TestDatasetCatalogScansBeyondUIBoundsAndOwnsProgress(t *testing.T) {
	b := catalogBackend{experiments: func(_ context.Context, q ExperimentQuery) (ExperimentPage, error) {
		if q.MaxResults != 100 {
			t.Fatalf("page size %d", q.MaxResults)
		}
		start, _ := strconv.Atoi(q.PageToken)
		end := min(start+10, 25)
		p := ExperimentPage{}
		for i := start; i < end; i++ {
			p.Experiments = append(p.Experiments, Experiment{ID: fmt.Sprint(i), Name: fmt.Sprint(i)})
		}
		if end < 25 {
			p.NextPageToken = fmt.Sprint(end)
		}
		return p, nil
	}, runs: func(_ context.Context, q RunQuery) (RunPage, error) {
		if len(q.ExperimentIDs) != 1 || q.MaxResults != 100 || q.ViewType != "ACTIVE_ONLY" {
			t.Fatalf("query %+v", q)
		}
		start, _ := strconv.Atoi(q.PageToken)
		end := min(start+30, 45)
		p := RunPage{}
		for i := start; i < end; i++ {
			id := q.ExperimentIDs[0] + "/" + fmt.Sprint(i)
			r := catalogRun(id, id)
			r.Info.ExperimentID = q.ExperimentIDs[0]
			p.Runs = append(p.Runs, r)
		}
		if end < 45 {
			p.NextPageToken = fmt.Sprint(end)
		}
		return p, nil
	}}
	progress := 0
	c, err := ScanDatasets(context.Background(), b, "source", DatasetScanOptions{}, func(c DatasetCatalog) {
		progress++
		if len(c.Entries) > 0 {
			c.Entries[0].Uses[0].Tags[0].Value = "mutated"
			c.Entries[0].Schema.Fields[0].Name = "mutated"
			*c.Entries[0].Schema.Fields[0].Required = false
		}
	})
	if err != nil || !c.Complete || len(c.Entries) != 1125 || c.RunsScanned != 1125 || c.ExperimentsScanned != 25 || progress < 25 {
		t.Fatalf("entries=%d runs=%d exp=%d progress=%d err=%v", len(c.Entries), c.RunsScanned, c.ExperimentsScanned, progress, err)
	}
	// The final callback and returned result are also independent ownership.
	for _, entry := range c.Entries {
		if entry.Uses[0].Tags[0].Value != "training" || entry.Schema.Fields[0].Name != "x" || !*entry.Schema.Fields[0].Required {
			t.Fatal("progress mutated scan result")
		}
	}
}

func TestDatasetCatalogCrossExperimentVariantsAndInputContext(t *testing.T) {
	a := catalogRun("a", "data")
	a.Info.ExperimentID = "one"
	b := a
	b.Info.RunID = "b"
	b.Info.ExperimentID = "two"
	b.Inputs.DatasetInputs = append([]DatasetInput(nil), a.Inputs.DatasetInputs...)
	b.Inputs.DatasetInputs[0].Tags = []KeyValue{{Key: "mlflow.data.context", Value: "validation"}}
	other := b.Inputs.DatasetInputs[0]
	other.Dataset.Source = "different source"
	b.Inputs.DatasetInputs = append(b.Inputs.DatasetInputs, other)
	c := CatalogFromRuns("source", []Experiment{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}, []Run{a, b})
	if c.Complete || c.Scope != "loaded_runs" || len(c.Entries) != 2 {
		t.Fatalf("%+v", c)
	}
	uses := 0
	for _, entry := range c.Entries {
		uses += len(entry.Uses)
		if len(entry.Uses) == 2 {
			if entry.Uses[0].ExperimentName != "One" || entry.Uses[1].Context != "validation" {
				t.Fatalf("%+v", entry)
			}
		}
	}
	if uses != 3 {
		t.Fatal(uses)
	}
}

func TestCatalogPartialFailuresCyclesAndCancellation(t *testing.T) {
	b := catalogBackend{experiments: func(context.Context, ExperimentQuery) (ExperimentPage, error) {
		return ExperimentPage{Experiments: []Experiment{{ID: "fail"}, {ID: "cycle"}, {ID: "ok"}}}, nil
	}, runs: func(_ context.Context, q RunQuery) (RunPage, error) {
		switch q.ExperimentIDs[0] {
		case "fail":
			return RunPage{}, errors.New("offline")
		case "cycle":
			return RunPage{Runs: []Run{catalogRun("cycle", "data")}, NextPageToken: "same"}, nil
		default:
			return RunPage{Runs: []Run{catalogRun("ok", "data")}}, nil
		}
	}}
	c, err := ScanDatasets(context.Background(), b, "source", DatasetScanOptions{}, nil)
	if err == nil || c.Complete || c.Cancelled || len(c.Errors) != 2 || c.RunsScanned != 2 || len(c.Entries[0].Uses) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c, err = ScanDatasets(ctx, b, "source", DatasetScanOptions{ExperimentIDs: []string{"ok", "next"}}, func(c DatasetCatalog) {
		if c.RunsScanned == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !c.Cancelled || c.Complete || c.RunsScanned != 1 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestDeletedDatasetDiscoveryIncludesActiveExperiments(t *testing.T) {
	b := catalogBackend{experiments: func(_ context.Context, q ExperimentQuery) (ExperimentPage, error) {
		if q.ViewType != "ALL" {
			t.Fatalf("misses deleted runs in active experiments: %+v", q)
		}
		return ExperimentPage{Experiments: []Experiment{{ID: "active"}}}, nil
	}, runs: func(_ context.Context, q RunQuery) (RunPage, error) {
		if q.ViewType != "DELETED_ONLY" {
			t.Fatal(q)
		}
		return RunPage{}, nil
	}}
	if _, err := ScanDatasets(context.Background(), b, "source", DatasetScanOptions{ViewType: "DELETED_ONLY"}, nil); err != nil {
		t.Fatal(err)
	}
}
