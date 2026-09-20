package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
	"github.com/daviddwlee84/lazymlflow/internal/tui"
)

func isolateLocalState(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	return filepath.Join(base, "lazymlflow", "state.db")
}
func firstSource() string {
	return core.SourceKey(core.Target{ID: "first", TrackingURI: "http://first:5000"})
}
func TestViewCommandsAreLocalAndRoundTrip(t *testing.T) {
	path := isolateLocalState(t)
	cases := [][]string{
		{"view", "set", "11", "--column", "dataset:name", "--column", "param:learning rate", "--column", "metric:loss", "--sort", "metric:loss ASC", "--group-by", "optimizer", "--group-by", "learning rate", "--json"},
		{"view", "hide", "--run", "child", "--json"}, {"view", "archive", "--experiment", "11", "--json"},
	}
	for _, args := range cases {
		opts, c, out, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v: %d %s", args, status, stderr)
		}
		if len(c.opened) > 0 {
			t.Fatal("local operation connected")
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("invalid JSON: %s", out)
		}
	}
	state := localstate.New(path)
	v, found, err := state.LoadView(context.Background(), firstSource(), "11")
	if err != nil || !found || v.Mode != "grouped" || !reflect.DeepEqual(v.GroupBy, []string{"optimizer", "learning rate"}) || v.Columns[0].Key != "name" || v.Columns[2].Key != "learning rate" {
		t.Fatalf("%#v %v %v", v, found, err)
	}
	values, err := state.ListVisibility(context.Background(), firstSource())
	if err != nil || values["run/child"] != core.VisibilityHidden || values["experiment/11"] != core.VisibilityArchived {
		t.Fatalf("%v %v", values, err)
	}
	state.Close()
	for _, args := range [][]string{{"view", "restore", "--run", "child"}, {"view", "reset", "11"}} {
		opts, c, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%d %s", status, stderr)
		}
		if len(c.opened) > 0 {
			t.Fatal("restore connected")
		}
	}
	state = localstate.New(path)
	defer state.Close()
	if _, found, err := state.LoadView(context.Background(), firstSource(), "11"); err != nil || found {
		t.Fatalf("reset failed: %v %v", found, err)
	}
	values, err = state.ListVisibility(context.Background(), firstSource())
	if err != nil || len(values) != 1 {
		t.Fatalf("restore changed other resource: %v %v", values, err)
	}
}
func TestViewShowAndStaticCommandsDoNotCreateState(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{{"view", "show", "11", "--json"}, {"--help"}, {"view", "--help"}, {"version", "--json"}, {"experiments", "list", "--json"}, {"runs", "list", "11", "--json"}} {
		opts, _, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v %d %s", args, status, stderr)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("unexpected state directory: %v", err)
	}
}
func TestViewUsageErrorsDoNotConnectOrCreate(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{{"view", "hide"}, {"view", "hide", "--run", "a", "--experiment", "1"}, {"view", "set", "1"}, {"view", "set", "1", "--column", "unknown:key"}, {"runs", "list", "--use-view"}, {"runs", "list", "1", "2", "--use-view"}, {"runs", "list", "1", "--visibility", "hidden"}, {"view", "set", "1", "--mode", "oops"}} {
		opts, c, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 2 {
			t.Fatalf("%v %d %s", args, status, stderr)
		}
		if len(c.opened) > 0 {
			t.Fatal("usage error connected")
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("bad flags wrote state: %v", err)
	}
}
func TestRunQueriesIgnorePersonalVisibilityUnlessRequested(t *testing.T) {
	path := isolateLocalState(t)
	state := localstate.New(path)
	ctx := context.Background()
	v := core.DefaultView([]string{"loss"}, nil)
	v.Filter = "metrics.loss < 10"
	v.Sort = []core.SortSpec{{Column: core.ColumnSpec{Kind: "param", Key: "batch_size", Numeric: true}}}
	if err := state.SaveView(ctx, firstSource(), "11", v); err != nil {
		t.Fatal(err)
	}
	if err := state.SetVisibility(ctx, firstSource(), "run", "hidden", core.VisibilityHidden); err != nil {
		t.Fatal(err)
	}
	state.Close()
	for _, useView := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "view"}[useView], func(t *testing.T) {
			opts, c, out, stderr := testOptions(t)
			calls := 0
			c.backend = &fakeBackend{runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
				calls++
				if useView && q.Filter != v.Filter {
					t.Fatalf("saved filter lost: %#v", q)
				}
				if !useView && q.Filter != "" {
					t.Fatal("default used saved filter")
				}
				return core.RunPage{Runs: []core.Run{
					{Info: core.RunInfo{RunID: "hundred", ExperimentID: "11"}, Data: core.RunData{Params: []core.KeyValue{{Key: "batch_size", Value: "100"}}}},
					{Info: core.RunInfo{RunID: "hidden", ExperimentID: "11"}},
					{Info: core.RunInfo{RunID: "ten", ExperimentID: "11"}, Data: core.RunData{Params: []core.KeyValue{{Key: "batch_size", Value: "10"}}}},
				}, NextPageToken: "more"}, nil
			}}
			args := []string{"runs", "list", "11", "--json"}
			if useView {
				args = append(args, "--use-view")
			}
			if status := Execute(ctx, args, opts); status != 0 {
				t.Fatalf("%d %s", status, stderr)
			}
			var result struct {
				Runs      []core.Run `json:"runs"`
				SortScope string     `json:"sort_scope"`
				Complete  bool       `json:"complete"`
				Next      string     `json:"next_page_token"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Next != "more" {
				t.Fatalf("loaded beyond requested page: calls=%d result=%+v", calls, result)
			}
			if useView {
				if len(result.Runs) != 2 || result.Runs[0].ID() != "ten" || result.SortScope != "loaded_rows" || result.Complete {
					t.Fatalf("wrong local view: %s", out)
				}
			} else if len(result.Runs) != 3 {
				t.Fatalf("default hid data: %s", out)
			}
		})
	}
}
func TestExperimentUseViewOnlyFiltersLocalVisibility(t *testing.T) {
	path := isolateLocalState(t)
	state := localstate.New(path)
	if err := state.SetVisibility(context.Background(), firstSource(), "experiment", "1", core.VisibilityHidden); err != nil {
		t.Fatal(err)
	}
	state.Close()
	for _, use := range []bool{false, true} {
		opts, c, out, stderr := testOptions(t)
		c.backend = &fakeBackend{experiment: func(context.Context, core.ExperimentQuery) (core.ExperimentPage, error) {
			return core.ExperimentPage{Experiments: []core.Experiment{{ID: "1"}, {ID: "2"}}}, nil
		}}
		args := []string{"experiments", "list", "--json"}
		if use {
			args = append(args, "--use-view")
		}
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%d %s", status, stderr)
		}
		var p core.ExperimentPage
		json.Unmarshal(out.Bytes(), &p)
		want := 2
		if use {
			want = 1
		}
		if len(p.Experiments) != want {
			t.Fatalf("%s", out)
		}
	}
}
func TestMixedColumnTextAndExactGroupKeys(t *testing.T) {
	isolateLocalState(t)
	opts, c, out, stderr := testOptions(t)
	c.backend = &fakeBackend{runs: func(context.Context, core.RunQuery) (core.RunPage, error) {
		return core.RunPage{Runs: []core.Run{{Info: core.RunInfo{RunID: "a", RunName: "run"}, Data: core.RunData{Params: []core.KeyValue{{Key: "learning rate", Value: "0.1"}}, Tags: []core.KeyValue{{Key: "task", Value: "forecast"}}}}}}, nil
	}}
	if status := Execute(context.Background(), []string{"runs", "list", "11", "--column", "tag:task", "--column", "param:learning rate", "--group-by", "learning rate"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	if !strings.Contains(out.String(), "forecast") || !strings.Contains(out.String(), "learning rate") || !strings.Contains(out.String(), "0.1") {
		t.Fatalf("%s", out)
	}
}
func TestDashboardMouseExplicitFalseAndLazyState(t *testing.T) {
	path := isolateLocalState(t)
	for _, arg := range []string{"", "--mouse=false"} {
		opts, _, _, stderr := testOptions(t)
		opts.IsTerminal = func() bool { return true }
		opts.Dashboard = func(_ context.Context, o tui.Options) error {
			if o.State == nil {
				t.Fatal("missing state")
			}
			if arg == "" && o.Mouse != nil {
				t.Fatal("default overrode saved mouse")
			}
			if arg != "" && (o.Mouse == nil || *o.Mouse) {
				t.Fatal("explicit false lost")
			}
			return nil
		}
		args := []string{}
		if arg != "" {
			args = append(args, arg)
		}
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%d %s", status, stderr)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dashboard injection eagerly created database: %v", err)
	}
}
func TestSSHTargetFlagsAndPrintLifetime(t *testing.T) {
	isolateLocalState(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	opts, _, out, stderr := testOptions(t)
	opts.LoadConfig = config.Load
	if status := Execute(context.Background(), []string{"--config", cfgPath, "targets", "add", "remote", "--uri", "http://127.0.0.1:8000", "--ssh-host", "david_ubuntu", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var target core.Target
	if err := json.Unmarshal(out.Bytes(), &target); err != nil || target.SSHHost != "david_ubuntu" {
		t.Fatalf("%s %v", out, err)
	}
	opts, c, _, stderr := testOptions(t)
	opts.LoadConfig = config.Load
	if status := Execute(context.Background(), []string{"--config", cfgPath, "open", "--print"}, opts); status != 2 || len(c.opened) != 0 {
		t.Fatalf("ephemeral tunnel URL returned: %d %s", status, stderr)
	}
}

func TestSavedHiddenViewAndExplicitOrderRemainConsistent(t *testing.T) {
	path := isolateLocalState(t)
	state := localstate.New(path)
	ctx := context.Background()
	v := core.DefaultView([]string{"loss"}, nil)
	v.Mode = "flat"
	v.Visibility = "hidden"
	if err := state.SaveView(ctx, firstSource(), "11", v); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if err := state.SetVisibility(ctx, firstSource(), "run", id, core.VisibilityHidden); err != nil {
			t.Fatal(err)
		}
	}
	state.Close()
	opts, c, out, stderr := testOptions(t)
	c.backend = &fakeBackend{runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		if !reflect.DeepEqual(q.OrderBy, []string{"metrics.`learning rate` ASC"}) {
			t.Fatalf("explicit order replaced: %v", q.OrderBy)
		}
		return core.RunPage{Runs: []core.Run{
			{Info: core.RunInfo{RunID: "b", StartTime: 10}, Data: core.RunData{Metrics: []core.Metric{{Key: "learning rate", Value: 1}}}},
			{Info: core.RunInfo{RunID: "a", StartTime: 20}, Data: core.RunData{Metrics: []core.Metric{{Key: "learning rate", Value: 2}}}},
			{Info: core.RunInfo{RunID: "normal", StartTime: 30}},
		}}, nil
	}}
	if status := Execute(ctx, []string{"runs", "list", "11", "--use-view", "--order-by", "metrics.`learning rate` ASC", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var result struct {
		Runs []core.Run    `json:"runs"`
		Rows []core.RunRow `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 2 || len(result.Rows) != 2 || result.Rows[0].ID != "b" {
		t.Fatalf("saved hidden/explicit ordering inconsistent: %s", out)
	}
}
func TestNumericColumnPreferenceAppliesToLaterSort(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{
		{"view", "set", "11", "--column", "param:batch size", "--numeric", "param:batch size"},
		{"view", "set", "11", "--sort", "param:batch size ASC"},
	} {
		opts, _, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v: %d %s", args, status, stderr)
		}
	}
	state := localstate.New(path)
	defer state.Close()
	v, _, err := state.LoadView(context.Background(), firstSource(), "11")
	if err != nil || !v.Sort[0].Column.Numeric {
		t.Fatalf("numeric preference lost: %+v %v", v, err)
	}
}

func TestDatasetContextColumnsStayDistinct(t *testing.T) {
	path := isolateLocalState(t)
	opts, _, _, stderr := testOptions(t)
	args := []string{"view", "set", "11", "--column", "dataset:name@training", "--column", "dataset:name@validation", "--column", "dataset:name@training"}
	if status := Execute(context.Background(), args, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	state := localstate.New(path)
	defer state.Close()
	v, _, err := state.LoadView(context.Background(), firstSource(), "11")
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for _, c := range v.Columns {
		columns = append(columns, core.ColumnID(c))
	}
	if want := []string{"attribute:name", "dataset:name@training", "dataset:name@validation"}; !reflect.DeepEqual(columns, want) {
		t.Fatalf("dataset contexts dropped or repeated: %v", columns)
	}
	opts, _, out, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"runs", "list", "11", "--column", "dataset:name@training", "--column", "dataset:name@validation"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	if !strings.Contains(out.String(), "dataset:name@training") || !strings.Contains(out.String(), "dataset:name@validation") {
		t.Fatalf("dataset column headers are indistinguishable: %s", out)
	}
}
func TestViewQueryJSONScopeAndEffectiveFilter(t *testing.T) {
	path := isolateLocalState(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, kind, key, startToken, nextToken, scope string }{
		{"server-partial", "metric", "loss", "", "more", "all_matches"},
		{"local-partial", "dataset", "name", "", "more", "loaded_rows"},
		{"local-continuation", "dataset", "name", "last-page", "", "loaded_rows"},
		{"local-complete", "dataset", "name", "", "", "all_matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := localstate.New(path)
			v := core.DefaultView(nil, nil)
			v.Mode = "grouped"
			v.GroupBy = []string{"optimizer"}
			v.Filter = "metrics.loss > 9"
			v.Sort = []core.SortSpec{{Column: core.ColumnSpec{Kind: tc.kind, Key: tc.key}}}
			if err := state.SaveView(ctx, firstSource(), "11", v); err != nil {
				t.Fatal(err)
			}
			state.Close()
			opts, c, out, stderr := testOptions(t)
			calls := 0
			c.backend = &fakeBackend{runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
				calls++
				if q.Filter != "metrics.loss < 1" || q.PageToken != tc.startToken {
					t.Fatalf("effective query incorrect: %+v", q)
				}
				return core.RunPage{NextPageToken: tc.nextToken}, nil
			}}
			args := []string{"runs", "list", "11", "--use-view", "--filter", "metrics.loss < 1", "--page-token", tc.startToken, "--json"}
			if status := Execute(ctx, args, opts); status != 0 {
				t.Fatalf("%d %s", status, stderr)
			}
			var result struct {
				Runs     json.RawMessage     `json:"runs"`
				Rows     json.RawMessage     `json:"rows"`
				View     core.ExperimentView `json:"view"`
				Scope    string              `json:"sort_scope"`
				Complete bool                `json:"complete"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Scope != tc.scope || result.Complete != (tc.nextToken == "" && tc.startToken == "") || result.View.Filter != "metrics.loss < 1" || string(result.Runs) != "[]" || string(result.Rows) != "[]" {
				t.Fatalf("inconsistent JSON: %s", out)
			}
		})
	}
}
