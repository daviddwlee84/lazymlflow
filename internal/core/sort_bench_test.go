package core

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

var sortedBenchmarkRuns []Run

// Ten thousand loaded rows exercise the same sort used when clicking a column
// header; the fixture includes metric precision, ties, datasets and missing data.
func benchmarkSortRuns() []Run {
	runs := make([]Run, 10000)
	for i := range runs {
		n := (i * 7919) % 10000
		runs[i] = Run{Info: RunInfo{RunID: fmt.Sprintf("run-%05d", i), StartTime: 1700000000000 + int64(n)*1000, EndTime: 1700000030000 + int64(n)*1000}, Data: RunData{Metrics: []Metric{{Key: "loss", Value: Number(float64(n%997) / 997)}, {Key: "accuracy", Value: Number(float64(n%101) / 101)}}}}
		if i%17 != 0 {
			runs[i].Inputs.DatasetInputs = []DatasetInput{
				{Dataset: Dataset{Name: fmt.Sprintf("training-%02d", n%43), Digest: fmt.Sprintf("digest-%02d", n%23)}, Tags: []KeyValue{{Key: "mlflow.data.context", Value: "training"}}},
				{Dataset: Dataset{Name: fmt.Sprintf("validation-%02d", n%31), Digest: fmt.Sprintf("digest-%02d", n%13)}, Tags: []KeyValue{{Key: "mlflow.data.context", Value: "validation"}}},
			}
		}
	}
	return runs
}
func BenchmarkSortRuns10000(b *testing.B) {
	runs := benchmarkSortRuns()
	for _, tc := range []struct {
		name  string
		order []SortSpec
	}{
		{"metrics", []SortSpec{{Column: ColumnSpec{Kind: "metric", Key: "loss"}, Desc: true}, {Column: ColumnSpec{Kind: "attribute", Key: "start_time"}}}},
		{"datasets", []SortSpec{{Column: ColumnSpec{Kind: "dataset", Key: "name"}}, {Column: ColumnSpec{Kind: "metric", Key: "accuracy"}, Desc: true}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sortedBenchmarkRuns = SortRuns(runs, tc.order)
			}
		})
	}
}
func TestSortRunsCachedKeysPreserveNumericEdgesAndSource(t *testing.T) {
	// Missing always follows NaN in either direction; signed infinity is a number.
	runs := []Run{
		{Info: RunInfo{RunID: "missing"}},
		{Info: RunInfo{RunID: "nan"}, Data: RunData{Metrics: []Metric{{Key: "score", Value: Number(math.NaN())}}}},
		{Info: RunInfo{RunID: "positive-infinity"}, Data: RunData{Metrics: []Metric{{Key: "score", Value: Number(math.Inf(1))}}}},
		{Info: RunInfo{RunID: "negative-infinity"}, Data: RunData{Metrics: []Metric{{Key: "score", Value: Number(math.Inf(-1))}}}},
		{Info: RunInfo{RunID: "zero-b"}, Data: RunData{Metrics: []Metric{{Key: "score", Value: 0}}}},
		{Info: RunInfo{RunID: "zero-a"}, Data: RunData{Metrics: []Metric{{Key: "score", Value: Number(math.Copysign(0, -1))}}}},
	}
	originalIDs := runIDs(runs)
	for _, tc := range []struct {
		desc bool
		want []string
	}{
		{false, []string{"negative-infinity", "zero-a", "zero-b", "positive-infinity", "nan", "missing"}},
		{true, []string{"positive-infinity", "zero-a", "zero-b", "negative-infinity", "nan", "missing"}},
	} {
		sorted := SortRuns(runs, []SortSpec{{Column: ColumnSpec{Kind: "metric", Key: "score"}, Desc: tc.desc}})
		if !reflect.DeepEqual(runIDs(sorted), tc.want) {
			t.Fatalf("desc=%v: %v", tc.desc, runIDs(sorted))
		}
		if !reflect.DeepEqual(runIDs(runs), originalIDs) {
			t.Fatal("source slice mutated")
		}
		sorted[0].Info.RunID = "changed"
		if !reflect.DeepEqual(runIDs(runs), originalIDs) {
			t.Fatal("result aliases source structs")
		}
	}
}
func TestSortRunsDatasetTupleAndNumericTimeOrdering(t *testing.T) {
	data := func(id string, names ...string) Run {
		r := Run{Info: RunInfo{RunID: id}}
		for _, name := range names {
			r.Inputs.DatasetInputs = append(r.Inputs.DatasetInputs, DatasetInput{Dataset: Dataset{Name: name}})
		}
		return r
	}
	runs := []Run{data("one", "a; b"), data("two", "b", "a"), data("empty", ""), data("missing")}
	sorted := SortRuns(runs, []SortSpec{{Column: ColumnSpec{Kind: "dataset", Key: "name"}}})
	if want := []string{"empty", "two", "one", "missing"}; !reflect.DeepEqual(runIDs(sorted), want) {
		t.Fatalf("dataset boundary/missing semantics: %v", runIDs(sorted))
	}
	times := []Run{{Info: RunInfo{RunID: "missing", EndTime: 1}}, {Info: RunInfo{RunID: "earlier", StartTime: 1000, EndTime: 5000}}, {Info: RunInfo{RunID: "later", StartTime: 2000, EndTime: 3000}}}
	for _, tc := range []struct {
		key  string
		want []string
	}{{"start_time", []string{"earlier", "later", "missing"}}, {"end_time", []string{"missing", "later", "earlier"}}, {"duration", []string{"later", "earlier", "missing"}}} {
		sorted := SortRuns(times, []SortSpec{{Column: ColumnSpec{Kind: "attribute", Key: tc.key}}})
		if !reflect.DeepEqual(runIDs(sorted), tc.want) {
			t.Fatalf("%s: %v", tc.key, runIDs(sorted))
		}
	}
}

func TestNormalizeViewPinsRunNameWithoutMutatingSavedOrder(t *testing.T) {
	original := []ColumnSpec{{Kind: "metric", Key: "loss"}, {Kind: "attribute", Key: "name", Width: 39, Label: "Training run"}, {Kind: "param", Key: "optimizer"}}
	view := NormalizeView(ExperimentView{Columns: original})
	if view.Columns[0].Key != "name" || view.Columns[0].Width != 39 || view.Columns[0].Label != "Training run" || view.Columns[1].Key != "loss" {
		t.Fatalf("name anchor/width lost: %#v", view.Columns)
	}
	if original[0].Key != "loss" || original[1].Key != "name" {
		t.Fatal("normalization modified the caller's saved slice")
	}
	view = NormalizeView(ExperimentView{Columns: []ColumnSpec{{Kind: "metric", Key: "loss"}}})
	if len(view.Columns) != 2 || view.Columns[0].Key != "name" || view.Columns[1].Key != "loss" {
		t.Fatalf("missing name anchor: %#v", view.Columns)
	}
}
