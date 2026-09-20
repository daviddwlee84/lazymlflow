package core

import (
	"reflect"
	"testing"
)

func child(id, parent string) Run {
	r := fieldRun(id, number(1), "0.01")
	if parent != "" {
		r.Data.Tags = []KeyValue{{Key: "mlflow.parentRunId", Value: parent}}
	}
	return r
}
func rowIDs(rows []RunRow) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}
func leafIDs(rows []RunRow) []string {
	var ids []string
	for _, r := range rows {
		if r.Run != nil {
			ids = append(ids, r.Run.ID())
		}
	}
	return ids
}
func TestTreePreservesMissingHiddenAndCycleRelations(t *testing.T) {
	v := DefaultView(nil, nil)
	v.Expansion = "all"
	runs := []Run{child("child", "parent"), child("grandchild", "child"), child("parent", "")}
	rows := BuildRunRows(runs, v, nil)
	if !reflect.DeepEqual(leafIDs(rows), []string{"parent", "child", "grandchild"}) || rows[2].Depth != 2 {
		t.Fatal(rows)
	}
	visibility := map[string]Visibility{VisibilityKey("run", "parent"): VisibilityHidden}
	rows = BuildRunRows(runs, v, visibility)
	if rows[0].Kind != "context" || !reflect.DeepEqual(leafIDs(rows), []string{"child", "grandchild"}) {
		t.Fatal(rows)
	}
	rows = BuildRunRows([]Run{child("a", "b"), child("b", "a"), child("c", "c")}, v, nil)
	seen := map[string]int{}
	for _, r := range rows {
		if r.Run != nil {
			seen[r.ID]++
		}
	}
	if len(seen) != 3 || seen["a"] != 1 || seen["b"] != 1 || seen["c"] != 1 {
		t.Fatal(rows)
	}
}
func TestExplicitCollapseAndDefaultFirstLevel(t *testing.T) {
	v := DefaultView(nil, nil)
	runs := []Run{child("p", ""), child("c", "p"), child("g", "c")}
	if ids := leafIDs(BuildRunRows(runs, v, nil)); !reflect.DeepEqual(ids, []string{"p", "c"}) {
		t.Fatal(ids)
	}
	v.Expanded = map[string]bool{"p": false}
	if ids := leafIDs(BuildRunRows(runs, v, nil)); !reflect.DeepEqual(ids, []string{"p"}) {
		t.Fatal(ids)
	}
	v.Mode = "flat"
	if len(BuildRunRows(runs, v, nil)) != 3 {
		t.Fatal("flat mode lost collapsed descendants")
	}
}
func TestGroupingRawCombinationsAndMissingValues(t *testing.T) {
	v := DefaultView(nil, nil)
	v.Mode = "grouped"
	v.GroupBy = []string{"learning rate"}
	v.Sort = []SortSpec{{Column: ColumnSpec{Kind: "metric", Key: "loss"}}}
	runs := []Run{fieldRun("a", number(2), "1e-3"), fieldRun("b", number(1), "1e-3"), fieldRun("c", number(0), "0.001"), fieldRun("empty", nil, "")}
	missing := fieldRun("missing", nil, "")
	missing.Data.Params = nil
	runs = append(runs, missing)
	rows := BuildRunRows(runs, v, nil)
	groups := []RunRow{}
	for _, r := range rows {
		if r.Kind == "group" {
			groups = append(groups, r)
		}
	}
	if len(groups) != 4 {
		t.Fatal("collapsed missing/empty or normalized distinct raw values", rows)
	}
	for _, g := range groups {
		if g.Count == 2 {
			v.Expanded = map[string]bool{g.ID: false}
			collapsed := BuildRunRows(runs, v, nil)
			if len(collapsed) != len(rows)-2 {
				t.Fatal(collapsed)
			}
			return
		}
	}
	t.Fatal("missing two-run group")
}
