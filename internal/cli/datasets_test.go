package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func cliDatasetRun(id, experiment string) core.Run {
	return core.Run{Info: core.RunInfo{RunID: id, RunName: id, ExperimentID: experiment}, Inputs: core.RunInputs{DatasetInputs: []core.DatasetInput{{Dataset: core.Dataset{Name: "features", Digest: "digest", SourceType: "local", Source: `{"uri":"same"}`, Schema: `{"mlflow_colspec":[{"type":"double","name":"特徵","required":true}]}`, Profile: `{"num_rows":4558}`}, Tags: []core.KeyValue{{Key: "mlflow.data.context", Value: "training"}}}}}}
}

func TestDatasetCommandsShareSchemaAndDiscoverAcrossExperiments(t *testing.T) {
	for _, args := range [][]string{{"datasets", "schema", "seed", "--json"}, {"datasets", "list", "--all", "--json"}, {"datasets", "runs", "seed", "--json"}} {
		opts, c, out, stderr := testOptions(t)
		c.backend = &fakeBackend{getRun: func(_ context.Context, id string) (core.Run, error) { return cliDatasetRun(id, "one"), nil }, experiment: func(context.Context, core.ExperimentQuery) (core.ExperimentPage, error) {
			return core.ExperimentPage{Experiments: []core.Experiment{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}}, nil
		}, runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
			return core.RunPage{Runs: []core.Run{cliDatasetRun(q.ExperimentIDs[0], q.ExperimentIDs[0])}}, nil
		}}
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v status %d %s", args, status, stderr)
		}
		var data map[string]any
		if err := json.Unmarshal(out.Bytes(), &data); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		switch args[1] {
		case "schema":
			schema := data["schema"].(map[string]any)
			if schema["columns"] != float64(1) || data["profile"].(map[string]any)["rows"] != float64(4558) {
				t.Fatal(data)
			}
		case "list":
			entries := data["entries"].([]any)
			if len(entries) != 1 || len(entries[0].(map[string]any)["uses"].([]any)) != 2 || data["complete"] != true {
				t.Fatal(data)
			}
		case "runs":
			if len(data["runs"].([]any)) != 2 || data["complete"] != true {
				t.Fatal(data)
			}
		}
		if c.closed != 1 || c.sessionClosed != 1 {
			t.Fatalf("cleanup %+v", c)
		}
	}
}

func TestDatasetCommandUsageNeverConnects(t *testing.T) {
	for _, args := range [][]string{{"datasets", "list"}, {"datasets", "list", "--all", "--experiment", "1"}, {"datasets", "list", "--all", "--view", "bad"}, {"datasets", "schema", "run", "--index", "0"}, {"datasets", "runs", "run", "--index", "-1"}, {"datasets", "list", "--experiment", " "}} {
		opts, c, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 2 {
			t.Fatalf("%v status %d %s", args, status, stderr)
		}
		if len(c.opened) > 0 {
			t.Fatal("invalid args connected")
		}
	}
}

func TestDatasetPartialJSONIsAvailableOnFailure(t *testing.T) {
	opts, c, out, stderr := testOptions(t)
	c.backend = &fakeBackend{experiment: func(context.Context, core.ExperimentQuery) (core.ExperimentPage, error) {
		return core.ExperimentPage{Experiments: []core.Experiment{{ID: "one"}, {ID: "fail"}}}, nil
	}, runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		if q.ExperimentIDs[0] == "fail" {
			return core.RunPage{}, errors.New("offline")
		}
		return core.RunPage{Runs: []core.Run{cliDatasetRun("one", "one")}}, nil
	}}
	if status := Execute(context.Background(), []string{"datasets", "list", "--all", "--json"}, opts); status != 1 {
		t.Fatalf("status %d %s", status, stderr)
	}
	var catalog core.DatasetCatalog
	if err := json.Unmarshal(out.Bytes(), &catalog); err != nil || catalog.Complete || len(catalog.Entries) != 1 || len(catalog.Errors) != 1 {
		t.Fatalf("%s %v", out, err)
	}
	if !strings.Contains(stderr.String(), "incomplete") {
		t.Fatalf("missing diagnostic %s", stderr)
	}
}
