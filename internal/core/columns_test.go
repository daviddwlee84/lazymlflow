package core

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func fieldRun(id string, metric *float64, param string) Run {
	r := Run{Info: RunInfo{RunID: id, RunName: "same name", StartTime: 1000}, Data: RunData{Params: []KeyValue{{Key: "learning rate", Value: param}}}}
	if metric != nil {
		r.Data.Metrics = []Metric{{Key: "loss", Value: Number(*metric)}}
	}
	return r
}
func number(n float64) *float64 { return &n }
func runIDs(r []Run) []string {
	var ids []string
	for _, v := range r {
		ids = append(ids, v.ID())
	}
	return ids
}
func TestInputsAndColumnDiscovery(t *testing.T) {
	var r Run
	err := json.Unmarshal([]byte(`{"info":{"run_id":"r"},"inputs":{"dataset_inputs":[{"dataset":{"name":"train","digest":"abc","source_type":"code","source":"{}","schema":"{columns:[]}","profile":"{}"},"tags":[{"key":"mlflow.data.context","value":"training"}]},{"dataset":{"name":"valid","digest":"def"},"tags":[{"key":"mlflow.data.context","value":"validation"}]}]},"outputs":{"model_outputs":[{"model_id":"m-123","step":2}]}}`), &r)
	if err != nil {
		t.Fatal(err)
	}
	if r.Inputs.DatasetInputs[0].Dataset.Schema != "{columns:[]}" {
		t.Fatal("lost input metadata")
	}
	if got := DisplayValue(r, ColumnSpec{Kind: "dataset", Key: "name"}); got != "train; valid" {
		t.Fatal(got)
	}
	if got := DisplayValue(r, ColumnSpec{Kind: "dataset", Key: "digest", DatasetContext: "validation"}); got != "def" {
		t.Fatal(got)
	}
	if got := DisplayValue(r, ColumnSpec{Kind: "attribute", Key: "models"}); got != "m-123" {
		t.Fatal(got)
	}
	if _, ok := ColumnValue(r, ColumnSpec{Kind: "dataset", Key: "name", DatasetContext: "test"}); ok {
		t.Fatal("invented dataset context")
	}
	columns := DiscoverColumns([]Run{r}, []ColumnSpec{{Kind: "param", Key: "not yet loaded", Width: 23}})
	found := map[string]ColumnSpec{}
	for _, c := range columns {
		found[ColumnID(c)] = c
	}
	if found["param:not yet loaded"].Width != 23 || found["dataset:name@validation"].Key != "name" {
		t.Fatal(found)
	}
}
func TestColumnMissingDifferentFromEmptyAndZero(t *testing.T) {
	r := fieldRun("r", number(0), "")
	if s, ok := ColumnValue(r, ColumnSpec{Kind: "param", Key: "learning rate"}); !ok || s != "" {
		t.Fatal(s, ok)
	}
	if s, ok := ColumnValue(r, ColumnSpec{Kind: "metric", Key: "loss"}); !ok || s != "0" {
		t.Fatal(s, ok)
	}
	if got := DisplayValue(r, ColumnSpec{Kind: "tag", Key: "no"}); got != "—" {
		t.Fatal(got)
	}
}
func TestSortParsingAndServerCapabilities(t *testing.T) {
	for _, input := range []string{"param:learning rate=desc", "param:learning rate DESC", "params.`learning rate` DESC"} {
		s, err := ParseSort(input)
		if err != nil || !s.Desc || s.Column.Kind != "param" || s.Column.Key != "learning rate" {
			t.Fatal(s, err)
		}
		order, ok := ServerOrder([]SortSpec{s})
		if !ok || order[0] != "params.`learning rate` DESC" {
			t.Fatal(order, ok)
		}
	}
	for _, s := range []SortSpec{{Column: ColumnSpec{Kind: "dataset", Key: "name"}}, {Column: ColumnSpec{Kind: "param", Key: "learning rate", Numeric: true}}, {Column: ColumnSpec{Kind: "attribute", Key: "duration"}}, {Column: ColumnSpec{Kind: "tag", Key: "contains`tick"}}} {
		if _, ok := ServerOrder([]SortSpec{s}); ok {
			t.Fatalf("unsupported order advertised as server sortable: %+v", s)
		}
	}
	if _, err := ParseSort("metric:loss=sideways"); err == nil {
		t.Fatal("accepted invalid direction")
	}
	if _, err := ParseColumn("dataset:unknown"); err == nil {
		t.Fatal("accepted invalid dataset field")
	}
}
func TestSortPrecisionNonfiniteMissingAndNumericParameters(t *testing.T) {
	runs := []Run{fieldRun("missing", nil, "auto"), fieldRun("nan", number(math.NaN()), ""), fieldRun("higher", number(1.0000002), "10"), fieldRun("lower", number(1.0000001), "2"), fieldRun("zero", number(0), "1")}
	sorted := SortRuns(runs, []SortSpec{{Column: ColumnSpec{Kind: "metric", Key: "loss"}, Desc: true}})
	if got := runIDs(sorted); !reflect.DeepEqual(got, []string{"higher", "lower", "zero", "nan", "missing"}) {
		t.Fatal(got)
	}
	if runs[0].ID() != "missing" {
		t.Fatal("sort mutated server snapshot")
	}
	text := SortRuns(runs[2:], []SortSpec{{Column: ColumnSpec{Kind: "param", Key: "learning rate"}}})
	numeric := SortRuns(runs[2:], []SortSpec{{Column: ColumnSpec{Kind: "param", Key: "learning rate", Numeric: true}}})
	if !reflect.DeepEqual(runIDs(text), []string{"zero", "higher", "lower"}) || !reflect.DeepEqual(runIDs(numeric), []string{"zero", "lower", "higher"}) {
		t.Fatal(runIDs(text), runIDs(numeric))
	}
}
func TestSourceIdentityIgnoresRuntimeAndDisplayButNotBackend(t *testing.T) {
	a := Target{ID: "server", TrackingURI: "http://127.0.0.1:8000", SSHHost: "host"}
	b := a
	b.Name = "Renamed"
	b.Python = "/new/venv"
	if SourceKey(a) != SourceKey(b) {
		t.Fatal("lost preferences on display/runtime change")
	}
	b.TrackingURI = "http://other:8000"
	if SourceKey(a) == SourceKey(b) {
		t.Fatal("mixed distinct stores")
	}
	if strings.Contains(SourceKey(a), "host") {
		t.Fatal("identity should be opaque")
	}
}
