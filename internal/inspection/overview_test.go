package inspection

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestExperimentOverviewIncludesMetadataOutsideFirstTwentyDetails(t *testing.T) {
	metadata := make([]core.Run, 27)
	for i := range metadata {
		r := fixtureRun(fmt.Sprintf("r%02d", i))
		r.Data.Metrics = []core.Metric{{Key: "loss", Value: core.Number(i), Step: int64(i), Timestamp: int64(i * 100)}}
		r.Data.Params = []core.KeyValue{{Key: "optimizer", Value: "sgd"}}
		if i >= 20 {
			r.Info.Status = "FAILED"
			r.Data.Metrics = append(r.Data.Metrics, core.Metric{Key: "tail_only", Value: 7})
			r.Data.Params = []core.KeyValue{{Key: "optimizer", Value: "adam"}, {Key: "tail_parameter", Value: "0.001"}}
			r.Inputs.DatasetInputs[0].Dataset.Digest = "tail-version"
			extra := r.Inputs.DatasetInputs[0]
			extra.Tags = []core.KeyValue{{Key: "mlflow.data.context", Value: "testing"}}
			r.Inputs.DatasetInputs = append(r.Inputs.DatasetInputs, extra, extra)
		}
		switch i {
		case 20:
			r.Data.Metrics[0].Value = core.Number(math.NaN())
		case 21:
			r.Data.Metrics[0].Value = core.Number(math.Inf(1))
		case 22:
			r.Data.Metrics = r.Data.Metrics[1:]
		case 23:
			r.Data.Metrics[0].Value = 0
		case 24:
			r.Data.Metrics[0].Value = -5
		case 25:
			r.Data.Metrics[0].Value = 10
		case 26:
			r.Data.Metrics[0].Value = 20
		}
		metadata[i] = r
	}
	b := &fixtureBackend{search: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		if q.PageToken == "" {
			return core.RunPage{Runs: metadata[:19], NextPageToken: "remaining"}, nil
		}
		return core.RunPage{Runs: metadata[19:]}, nil
	}, get: func(_ context.Context, id string) (core.Run, error) {
		for _, r := range metadata {
			if r.ID() == id {
				return r, nil
			}
		}
		return core.Run{}, fmt.Errorf("missing fixture run")
	}}
	s, err := collector(b, &fixtureNotes{}).CollectExperiment(context.Background(), "e", ExperimentOptions{Options: Options{History: HistoryNone}})
	if err != nil {
		t.Fatal(err)
	}
	overview := s.Experiment.Overview
	if len(s.Runs) != 20 || overview.RunCount != 27 {
		t.Fatal("overview used detailed run count")
	}
	if !reflect.DeepEqual(overview.Statuses, []StatusCount{{Status: "FAILED", Runs: 7}, {Status: "FINISHED", Runs: 20}}) {
		t.Fatalf("status coverage %+v", overview.Statuses)
	}
	metrics := map[string]MetricCoverage{}
	for _, m := range overview.Metrics {
		metrics[m.Key] = m
	}
	loss := metrics["loss"]
	if loss.LoggedRuns != 26 || loss.MissingRuns != 1 || loss.FiniteRuns != 24 || loss.NonFiniteRuns != 2 || loss.LatestMin.Value != -5 || loss.LatestMin.RunID != "r24" || loss.LatestMax.Value != 20 || loss.LatestMax.RunID != "r26" {
		t.Fatalf("all-population metric coverage %+v", loss)
	}
	if m := metrics["tail_only"]; m.LoggedRuns != 7 || m.MissingRuns != 20 {
		t.Fatal("tail-only metric omitted", m)
	}
	params := map[string]ParameterCoverage{}
	for _, p := range overview.Parameters {
		params[p.Key] = p
	}
	if p := params["optimizer"]; p.LoggedRuns != 27 || p.DistinctValues != 2 || !reflect.DeepEqual(p.Examples, []string{"adam", "sgd"}) {
		t.Fatal("tail parameter values omitted", p)
	}
	if p := params["tail_parameter"]; p.LoggedRuns != 7 || p.MissingRuns != 20 {
		t.Fatal("tail-only parameter omitted", p)
	}
	if len(overview.Datasets) != 2 {
		t.Fatal("tail-only dataset variant omitted")
	}
	for _, d := range overview.Datasets {
		if d.Digest == "tail-version" && (d.Runs != 7 || !reflect.DeepEqual(d.Contexts, []string{"testing", "training"})) {
			t.Fatalf("duplicate dataset inputs counted as additional runs/contexts: %+v", d)
		}
	}
	markdown := RenderMarkdown(s)
	for _, want := range []string{"all 27 matching runs", "FAILED | 7", "tail\\_only | 7 | 20", "tail-version", "r24; step 24"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("overview Markdown missing %q", want)
		}
	}
	prompt, err := RenderPrompt("experiment-summary", s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "\"run_count\": 27") || !strings.Contains(prompt, "\"tail_only\"") {
		t.Fatal("prompt does not contain complete overview")
	}
}

func TestOverviewPerRunKeyCountsAndEmptyParameterValue(t *testing.T) {
	r := fixtureRun("r")
	r.Data.Metrics = []core.Metric{{Key: "zero", Value: 0}, {Key: "zero", Value: 5}}
	r.Data.Params = []core.KeyValue{{Key: "empty", Value: ""}, {Key: "empty", Value: "duplicate"}}
	other := core.Run{Info: core.RunInfo{RunID: "other"}}
	summary := summarizeExperimentMetadata("source", []core.Run{r, other})
	metric := summary.Metrics[0]
	if metric.LoggedRuns != 1 || metric.MissingRuns != 1 || metric.LatestMin == nil || metric.LatestMin.Value != 0 || metric.LatestMax.Value != 0 {
		t.Fatalf("duplicate metrics or zero handled incorrectly %+v", metric)
	}
	param := summary.Parameters[0]
	if param.LoggedRuns != 1 || param.MissingRuns != 1 || param.DistinctValues != 1 || !reflect.DeepEqual(param.Examples, []string{""}) {
		t.Fatal("empty confused with missing", param)
	}
}

func TestHistoryNumericObservationsAreFiniteAndObjective(t *testing.T) {
	cases := []struct {
		name            string
		values          []core.Number
		delta, min, max *core.Number
		unavailable     bool
	}{
		{name: "ordinary", values: []core.Number{3, 1, 2}, delta: number(-1), min: number(1), max: number(-1)},
		{name: "finite zero", values: []core.Number{0, 0}, delta: number(0), min: number(0), max: number(0)},
		{name: "overflow", values: []core.Number{-math.MaxFloat64, math.MaxFloat64}, max: number(0), unavailable: true},
		{name: "no finite data", values: []core.Number{core.Number(math.NaN()), core.Number(math.Inf(1))}, unavailable: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h := make([]core.Metric, len(test.values))
			for i, value := range test.values {
				h[i] = core.Metric{Key: "loss", Value: value, Step: int64(i + 3), Timestamp: int64(i + 100)}
			}
			summary := core.SummarizeHistory(h)
			observed := historyObservations(summary)
			if !equalNumber(observed.FirstToLast.Value, test.delta) || !equalNumber(observed.LastMinusMinimum.Value, test.min) || !equalNumber(observed.LastMinusMaximum.Value, test.max) {
				t.Fatalf("numeric differences %+v", observed)
			}
			if test.unavailable && observed.FirstToLast.Unavailable == "" {
				t.Fatal("unavailable/overflow difference not explicit")
			}
			c := collector(&fixtureBackend{history: func(context.Context, string, string) ([]core.Metric, error) { return h, nil }}, &fixtureNotes{})
			snapshot, err := c.CollectRuns(context.Background(), []string{"r"}, Options{History: HistorySampled, SampleLimit: 1})
			if err != nil {
				t.Fatal(err)
			}
			got := snapshot.Runs[0].Histories[0]
			if got.Observations == nil || !equalNumber(got.Observations.FirstToLast.Value, test.delta) {
				t.Fatal("collector computed observations from sampled points")
			}
			markdown := RenderMarkdown(snapshot)
			for _, want := range []string{"no optimization direction implied", "Last − first", "Last − minimum", "From step / timestamp"} {
				if !strings.Contains(markdown, want) {
					t.Errorf("numeric evidence missing %q", want)
				}
			}
			if strings.Contains(markdown, "converged") || strings.Contains(markdown, "improved") {
				t.Fatal("numeric observations asserted model quality")
			}
		})
	}
}
func number(value core.Number) *core.Number { return &value }
func equalNumber(a, b *core.Number) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
