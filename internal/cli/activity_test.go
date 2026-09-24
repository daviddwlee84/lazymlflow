package cli

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
	"github.com/daviddwlee84/lazymlflow/internal/tui"
)

func seedCLIActivity(t *testing.T, path string) {
	t.Helper()
	state := localstate.New(path)
	defer state.Close()
	now := time.Now().UnixMilli()
	policy := core.EffectiveActivityPolicy(core.DefaultActivitySettings(), core.AlertSettings{}, core.ActivityPolicy{})
	observations := []core.ActivityObservation{}
	for _, r := range []core.Run{
		{Info: core.RunInfo{RunID: "running", RunName: "Training", ExperimentID: "one", Status: "RUNNING", StartTime: now - 1000}},
		{Info: core.RunInfo{RunID: "recent", RunName: "Done", ExperimentID: "two", Status: "FINISHED", StartTime: now - 10000, EndTime: now - 500}},
		{Info: core.RunInfo{RunID: "failed", RunName: "Old failure", ExperimentID: "one", Status: "FAILED", StartTime: now - int64(30*24*time.Hour/time.Millisecond), EndTime: now - int64(29*24*time.Hour/time.Millisecond)}, Data: core.RunData{Metrics: []core.Metric{{Key: "corr", Value: core.Number(math.NaN())}}}},
	} {
		observations = append(observations, core.ActivityObservation{Run: r, ExperimentName: "Experiment " + r.Info.ExperimentID, Policy: policy})
	}
	_, err := state.ObserveActivity(context.Background(), firstSource(), core.ActivityBatch{Observations: observations, Experiments: []core.Experiment{{ID: "one", Name: "Experiment one"}, {ID: "two", Name: "Experiment two"}}, ReplaceExperiments: true, ObservedAt: now, StartedAt: now, InitialUnreadSince: now - int64(7*24*time.Hour/time.Millisecond), Complete: true, Checkpoint: now})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActivityCachedViewsAndLocalReadAcknowledgement(t *testing.T) {
	path := isolateLocalState(t)
	seedCLIActivity(t, path)
	run := func(args ...string) []byte {
		t.Helper()
		opts, connector, out, stderr := testOptions(t)
		args = append(args, "--json")
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%v: %d %s", args, status, stderr)
		}
		if len(connector.opened) != 0 {
			t.Fatal("local activity action connected")
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("invalid output %s", out)
		}
		return out.Bytes()
	}
	var list struct {
		Runs []struct {
			RunID  string `json:"run_id"`
			Unread bool   `json:"unread"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(run("activity", "list", "--view", "recent"), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Runs) != 2 || list.Runs[0].RunID != "recent" || list.Runs[1].Unread {
		t.Fatalf("global end ordering or historical unread incorrect: %+v", list)
	}
	run("activity", "read", "running")
	run("activity", "acknowledge", "failed")
	state := localstate.New(path)
	snapshot, err := state.LoadActivity(context.Background(), firstSource())
	state.Close()
	if err != nil || snapshot.Records["running"].Unread() || snapshot.Records["failed"].HasAlerts() || snapshot.Records["failed"].Unread() {
		t.Fatalf("read/ack state incorrect: %+v %v", snapshot.Records, err)
	}
	run("activity", "acknowledge", "failed", "--restore")
	run("activity", "unread", "failed")
	run("activity", "read", "--all")
	state = localstate.New(path)
	defer state.Close()
	snapshot, err = state.LoadActivity(context.Background(), firstSource())
	if err != nil || len(core.ActivityRecords(snapshot, "unread")) != 0 || !snapshot.Records["failed"].HasAlerts() {
		t.Fatalf("read-all changed acknowledgement: %+v %v", snapshot.Records, err)
	}
}

func TestActivityUsageAndEmptyCacheDoNotConnectOrCreate(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{
		{"activity", "list", "--view", "wrong"}, {"activity", "list", "--limit", "0"}, {"activity", "read"}, {"activity", "read", "id", "--all"}, {"activity", "unread", "--since", "0d"}, {"activity", "unread", "--since", "7h"}, {"activity", "acknowledge", "missing"},
	} {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(context.Background(), append(args, "--json"), opts); status != 2 {
			t.Fatalf("%v: %d %s", args, status, stderr)
		}
		if len(connector.opened) != 0 || out.Len() != 0 {
			t.Fatal("invalid command connected or polluted stdout")
		}
	}
	opts, connector, out, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"activity", "list", "--json"}, opts); status != 0 {
		t.Fatalf("empty list: %d %s", status, stderr)
	}
	if len(connector.opened) != 0 || !strings.Contains(out.String(), `"runs": []`) {
		t.Fatalf("empty cache output: %s", out)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("read-only operation created state: %v", err)
	}
}

func TestActivitySubscriptionsAndPinsAreScriptableAndLocal(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{
		{"view", "set", "one", "--metric-pin", "loss", "--metric-pin", "best_valid_corr", "--notify-metric", "best_valid_corr", "--notify-metric", "a=b=sample", "--metric-updates", "false", "--read-on", "select"},
	} {
		opts, connector, _, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 {
			t.Fatalf("%d %s", status, stderr)
		}
		if len(connector.opened) != 0 {
			t.Fatal("view setting connected")
		}
	}
	state := localstate.New(path)
	view, _, err := state.LoadView(context.Background(), firstSource(), "one")
	state.Close()
	if err != nil || !reflect.DeepEqual(view.MetricPins, []string{"loss", "best_valid_corr"}) || view.Activity == nil || view.Activity.MetricUpdates == nil || *view.Activity.MetricUpdates || view.Activity.ReadOn != "select" || !reflect.DeepEqual(view.Activity.Subscriptions, []core.MetricSubscription{{Key: "best_valid_corr", Mode: "value"}, {Key: "a=b", Mode: "sample"}}) {
		t.Fatalf("view round-trip: %+v %v", view, err)
	}
	opts, _, _, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"view", "set", "one", "--metric-pin=", "--notify-metric=", "--metric-updates", "inherit", "--read-on", "inherit"}, opts); status != 0 {
		t.Fatalf("clear: %d %s", status, stderr)
	}
	state = localstate.New(path)
	defer state.Close()
	view, _, err = state.LoadView(context.Background(), firstSource(), "one")
	if err != nil || len(view.MetricPins) != 0 || len(view.Activity.Subscriptions) != 0 || view.Activity.MetricUpdates != nil || view.Activity.ReadOn != "" {
		t.Fatalf("clearing lost inheritance: %+v %v", view, err)
	}
}

func TestActivityViewFlagValidationBeforeState(t *testing.T) {
	path := isolateLocalState(t)
	for _, flags := range [][]string{{"--metric-pin", "loss", "--metric-pin", "loss"}, {"--notify-metric", "loss=unknown"}, {"--notify-metric", "=value"}, {"--notify-metric", "loss", "--notify-metric", "loss=sample"}, {"--metric-updates", "maybe"}, {"--read-on", "hover"}} {
		opts, connector, _, stderr := testOptions(t)
		if status := Execute(context.Background(), append([]string{"view", "set", "one"}, flags...), opts); status != 2 {
			t.Fatalf("%v: %d %s", flags, status, stderr)
		}
		if len(connector.opened) > 0 {
			t.Fatal("invalid view connected")
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid flags created state: %v", err)
	}
}

func TestDashboardActivityOptionsAndSavePreserveConfiguration(t *testing.T) {
	isolateLocalState(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "# custom settings\n[activity]\nrefresh_seconds=0 # intentionally off\n[future]\nkeep=true\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	opts, _, _, stderr := testOptions(t)
	opts.LoadConfig = config.Load
	opts.IsTerminal = func() bool { return true }
	opts.Dashboard = func(_ context.Context, o tui.Options) error {
		if o.Activity.RefreshSeconds != 0 || o.Activity.InitialUnreadDays != 7 || o.Activity.ReadOn != "open" || o.SaveActivity == nil {
			t.Fatalf("wrong options %+v", o.Activity)
		}
		s := o.Activity
		s.ReadOn = "select"
		f := false
		return o.SaveActivity(s, core.AlertSettings{Failed: &f})
	}
	if status := Execute(context.Background(), []string{"--config", path}, opts); status != 0 {
		t.Fatalf("dashboard: %d %s", status, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Activity.ReadOn != "select" || cfg.Activity.RefreshSeconds != 0 || cfg.Alerts.Failed == nil || *cfg.Alerts.Failed {
		t.Fatalf("save: %+v %v", cfg, err)
	}
	stored, _ := os.ReadFile(path)
	if !strings.Contains(string(stored), "# intentionally off") || !strings.Contains(string(stored), "[future]\nkeep=true") {
		t.Fatalf("comments/unknown section lost: %s", stored)
	}
}

func TestActivityRefreshDiscoversExperimentsAndUsesSavedSubscriptions(t *testing.T) {
	path := isolateLocalState(t)
	state := localstate.New(path)
	view := core.DefaultView(nil, nil)
	view.Activity = &core.ActivityPolicy{Subscriptions: []core.MetricSubscription{{Key: "best_valid_corr", Mode: "value"}}}
	if err := state.SaveView(context.Background(), firstSource(), "one", view); err != nil {
		t.Fatal(err)
	}
	state.Close()
	now := time.Now().UnixMilli()
	metric := core.Metric{Key: "best_valid_corr", Value: 0.2, Step: 1, Timestamp: now}
	backend := &fakeBackend{
		experiment: func(_ context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
			if q.PageToken == "" {
				return core.ExperimentPage{Experiments: []core.Experiment{{ID: "one", Name: "First"}}, NextPageToken: "second"}, nil
			}
			return core.ExperimentPage{Experiments: []core.Experiment{{ID: "two", Name: "Second"}}}, nil
		},
		runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
			if q.Filter != "" && !reflect.DeepEqual(q.ExperimentIDs, []string{"one", "two"}) {
				t.Errorf("poll missed an experiment page: %#v", q.ExperimentIDs)
			}
			if strings.Contains(q.Filter, "end_time") || len(q.ExperimentIDs) == 1 && q.ExperimentIDs[0] == "two" {
				return core.RunPage{}, nil
			}
			return core.RunPage{Runs: []core.Run{{Info: core.RunInfo{RunID: "subscribed", ExperimentID: "one", RunName: "Subscribed", Status: "RUNNING", StartTime: now}, Data: core.RunData{Metrics: []core.Metric{metric}}}}}, nil
		},
	}
	run := func(args ...string) []byte {
		t.Helper()
		opts, connector, out, stderr := testOptions(t)
		connector.backend = backend
		if status := Execute(context.Background(), append(args, "--json"), opts); status != 0 {
			t.Fatalf("%v: %d %s", args, status, stderr)
		}
		return append([]byte(nil), out.Bytes()...)
	}
	run("activity", "list", "--refresh", "--view", "running")
	run("activity", "read", "subscribed")
	metric.Step++
	metric.Timestamp++
	run("activity", "refresh")
	state = localstate.New(path)
	snapshot, err := state.LoadActivity(context.Background(), firstSource())
	state.Close()
	if err != nil || snapshot.Records["subscribed"].Unread() {
		t.Fatalf("value subscription reacted to sample-only update: %+v %v", snapshot.Records["subscribed"], err)
	}
	metric.Value = 0.3
	run("activity", "refresh", "--full")
	state = localstate.New(path)
	defer state.Close()
	snapshot, err = state.LoadActivity(context.Background(), firstSource())
	if err != nil || !snapshot.Records["subscribed"].Unread() || snapshot.LastFullScan == 0 || len(snapshot.Experiments) != 2 {
		t.Fatalf("subscription/full refresh result: %+v %v", snapshot, err)
	}
}

func TestActivityHelpDoesNotLoadConfiguration(t *testing.T) {
	for _, command := range []string{"list", "refresh", "read", "unread", "acknowledge"} {
		opts, connector, out, stderr := testOptions(t)
		opts.LoadConfig = func(string) (*config.Config, error) { t.Fatal("help loaded configuration"); return nil, nil }
		if status := Execute(context.Background(), []string{"activity", command, "--help"}, opts); status != 0 || out.Len() == 0 || len(connector.opened) != 0 {
			t.Fatalf("%s help: %d %s", command, status, stderr)
		}
	}
}
