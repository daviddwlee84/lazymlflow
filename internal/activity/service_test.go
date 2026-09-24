package activity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

type testBackend struct {
	core.Backend
	mu                             sync.Mutex
	experiments                    []core.Experiment
	runs                           []core.Run
	failFilter                     string
	failExperiments                bool
	repeat                         bool
	fullActive, fullMax, fullCalls int
	discoveryIDs                   int
	getCalls                       []string
}

func (b *testBackend) SearchExperiments(ctx context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
	if err := ctx.Err(); err != nil {
		return core.ExperimentPage{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failExperiments {
		return core.ExperimentPage{}, errors.New("discovery offline")
	}
	start, _ := strconv.Atoi(q.PageToken)
	end := min(start+q.MaxResults, len(b.experiments))
	p := core.ExperimentPage{Experiments: append([]core.Experiment(nil), b.experiments[start:end]...)}
	if end < len(b.experiments) {
		p.NextPageToken = strconv.Itoa(end)
	}
	return p, nil
}

func (b *testBackend) SearchRuns(ctx context.Context, q core.RunQuery) (core.RunPage, error) {
	if err := ctx.Err(); err != nil {
		return core.RunPage{}, err
	}
	b.mu.Lock()
	if b.failFilter != "" && strings.Contains(q.Filter, b.failFilter) {
		b.mu.Unlock()
		return core.RunPage{}, errors.New("server offline")
	}
	if b.repeat {
		b.mu.Unlock()
		return core.RunPage{NextPageToken: "loop"}, nil
	}
	full := q.Filter == ""
	if full {
		b.fullActive++
		b.fullCalls++
		b.fullMax = max(b.fullMax, b.fullActive)
	}
	if strings.Contains(q.Filter, "status") {
		b.discoveryIDs = max(b.discoveryIDs, len(q.ExperimentIDs))
	}
	rows := append([]core.Run(nil), b.runs...)
	b.mu.Unlock()
	if full {
		defer func() { b.mu.Lock(); b.fullActive--; b.mu.Unlock() }()
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return core.RunPage{}, ctx.Err()
		}
	}
	ids := map[string]bool{}
	for _, id := range q.ExperimentIDs {
		ids[id] = true
	}
	filtered := []core.Run{}
	for _, run := range rows {
		if !ids[run.Info.ExperimentID] || run.Info.LifecycleStage == "deleted" {
			continue
		}
		include := true
		switch {
		case strings.Contains(q.Filter, "status"):
			include = run.Info.Status == "RUNNING"
		case strings.HasPrefix(q.Filter, "attributes.start_time >= "):
			cutoff, _ := strconv.ParseInt(strings.TrimPrefix(q.Filter, "attributes.start_time >= "), 10, 64)
			include = run.Info.StartTime >= cutoff
		case strings.HasPrefix(q.Filter, "attributes.end_time >= "):
			cutoff, _ := strconv.ParseInt(strings.TrimPrefix(q.Filter, "attributes.end_time >= "), 10, 64)
			include = run.Info.EndTime >= cutoff
		case q.Filter == "attributes.end_time > 0":
			include = run.Info.EndTime > 0
		}
		if include {
			filtered = append(filtered, run)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		a, z := filtered[i].Info.StartTime, filtered[j].Info.StartTime
		if len(q.OrderBy) > 0 && strings.Contains(q.OrderBy[0], "end_time") {
			a, z = filtered[i].Info.EndTime, filtered[j].Info.EndTime
		}
		if a != z {
			return a > z
		}
		return filtered[i].ID() < filtered[j].ID()
	})
	start, _ := strconv.Atoi(q.PageToken)
	end := min(start+q.MaxResults, len(filtered))
	page := core.RunPage{Runs: filtered[start:end]}
	if end < len(filtered) {
		page.NextPageToken = strconv.Itoa(end)
	}
	return page, nil
}

func (b *testBackend) GetRun(ctx context.Context, id string) (core.Run, error) {
	if err := ctx.Err(); err != nil {
		return core.Run{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getCalls = append(b.getCalls, id)
	for _, run := range b.runs {
		if run.ID() == id {
			return run, nil
		}
	}
	return core.Run{}, errors.New("missing run")
}

func testRun(id, experiment, status string, start, end int64) core.Run {
	return core.Run{Info: core.RunInfo{RunID: id, RunName: id, ExperimentID: experiment, Status: status, StartTime: start, EndTime: end, LifecycleStage: "active"}}
}

func testStore(t *testing.T) *localstate.Store {
	t.Helper()
	store := localstate.New(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(func() { store.Close() })
	return store
}

func TestRefreshDiscoversAllExperimentsAndAllRunningPages(t *testing.T) {
	b := &testBackend{}
	now := time.Unix(1700000000, 0)
	for i := 0; i < 105; i++ {
		id := strconv.Itoa(i)
		b.experiments = append(b.experiments, core.Experiment{ID: id, Name: "Exp " + id})
		for j := 0; j < 2; j++ {
			b.runs = append(b.runs, testRun(fmt.Sprintf("%s-%d", id, j), id, "RUNNING", now.Add(-30*24*time.Hour).UnixMilli(), 0))
		}
	}
	result, err := Refresh(context.Background(), b, testStore(t), "source", Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if b.discoveryIDs != 105 || len(core.ActivityRecords(result, "running")) != 210 || len(core.ActivityRecords(result, "unread")) != 210 {
		t.Fatalf("limited discovery: ids=%d records=%d", b.discoveryIDs, len(result.Records))
	}
	if !result.Initialized || result.Checkpoint != now.UnixMilli() || result.Counts["104"].Complete {
		t.Fatal("fast scan claimed complete totals or lost checkpoint")
	}
}

func TestRefreshCatchesOfflineCompletionsAndRetainsOldUnread(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	initial := now
	b := &testBackend{experiments: []core.Experiment{{ID: "e", Name: "Experiment"}}, runs: []core.Run{
		testRun("long", "e", "RUNNING", now.Add(-30*24*time.Hour).UnixMilli(), 0),
		testRun("old", "e", "FINISHED", now.Add(-30*24*time.Hour).UnixMilli(), now.Add(-20*24*time.Hour).UnixMilli()),
		testRun("recent", "e", "FINISHED", now.Add(-10*24*time.Hour).UnixMilli(), now.Add(-2*24*time.Hour).UnixMilli()),
	}}
	options := Options{Now: func() time.Time { return now }}
	a, err := Refresh(ctx, b, store, "s", options)
	if err != nil {
		t.Fatal(err)
	}
	if a.Records["old"].Unread() || !a.Records["recent"].Unread() || !a.Records["long"].Unread() {
		t.Fatal("initial window not respected")
	}
	var receipts []core.ActivityReceipt
	for id, r := range a.Records {
		receipts = append(receipts, core.ActivityReceipt{RunID: id, Revision: r.Revision})
	}
	if err := store.MarkActivityRead(ctx, "s", receipts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * 24 * time.Hour)
	b.runs[0].Info.Status, b.runs[0].Info.EndTime = "FINISHED", initial.Add(24*time.Hour).UnixMilli()
	b.runs = append(b.runs, testRun("offline", "e", "FAILED", initial.Add(24*time.Hour).UnixMilli(), initial.Add(2*24*time.Hour).UnixMilli()))
	a, err = Refresh(ctx, b, store, "s", options)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Records["offline"].Unread() || !a.Records["long"].Unread() {
		t.Fatal("offline completions older than seven days were lost")
	}
	now = now.Add(20 * 24 * time.Hour)
	options.RecentLimit = 1
	a, err = Refresh(ctx, b, store, "s", options)
	if err != nil || !a.Records["long"].Unread() {
		t.Fatal("old unread entry aged out", err)
	}
	// Explicit replay makes recent versions unread without moving a discovery
	// checkpoint backwards to the replay's wider time window.
	options.UnreadDays = 70
	a, err = Refresh(ctx, b, store, "s", options)
	if err != nil || !a.Records["old"].Unread() || a.Checkpoint != now.UnixMilli() {
		t.Fatal("forced unread replay failed", err)
	}
}

func TestRefreshFullCountsBoundsConcurrencyAndDoesNotNotifyHistory(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	b := &testBackend{}
	for e := 0; e < 4; e++ {
		id := strconv.Itoa(e)
		b.experiments = append(b.experiments, core.Experiment{ID: id, Name: id})
		for j := 0; j < 15; j++ {
			run := testRun(fmt.Sprintf("%s-%d", id, j), id, "FINISHED", now.Add(-30*24*time.Hour).UnixMilli(), now.Add(-20*24*time.Hour).UnixMilli())
			if j == 0 {
				run.Data.Metrics = []core.Metric{{Key: "corr", Value: core.Number(math.NaN())}}
			}
			b.runs = append(b.runs, run)
		}
	}
	progress := 0
	result, err := Refresh(ctx, b, store, "s", Options{Full: true, PageSize: 7, RecentLimit: 1, Now: func() time.Time { return now }, Progress: func(core.ActivitySnapshot) { progress++ }})
	if err != nil {
		t.Fatal(err)
	}
	if b.fullMax != 2 || b.fullCalls != 12 || progress != 6 {
		t.Fatalf("unexpected scan concurrency/progress: max=%d calls=%d progress=%d", b.fullMax, b.fullCalls, progress)
	}
	for _, c := range result.Counts {
		if !c.Complete || c.Total != 15 || c.Finished != 15 {
			t.Fatalf("incorrect count %+v", c)
		}
	}
	if len(core.ActivityRecords(result, "unread")) != 0 || len(core.ActivityRecords(result, "alerts")) != 4 {
		t.Fatal("historical scan created unread or omitted historical anomaly")
	}
	if len(result.Records) > 5 {
		t.Fatal("full historical index retained entire raw run population")
	}
}

func TestRefreshFailurePreservesRowsCheckpointAndPolicyErrors(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	b := &testBackend{experiments: []core.Experiment{{ID: "e"}}, runs: []core.Run{testRun("run", "e", "RUNNING", now.UnixMilli(), 0)}}
	options := Options{Now: func() time.Time { return now }}
	a, err := Refresh(ctx, b, store, "s", options)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := a.Checkpoint
	now = now.Add(time.Hour)
	b.failFilter = "end_time"
	a, err = Refresh(ctx, b, store, "s", options)
	if err == nil || a.Checkpoint != checkpoint || len(a.Records) != 1 || a.Complete {
		t.Fatal("failed refresh lost useful state or advanced checkpoint")
	}
	b.failFilter = ""
	options.Policy = func(context.Context, string) (core.ActivityPolicy, error) {
		return core.ActivityPolicy{}, errors.New("invalid saved policy")
	}
	a, err = Refresh(ctx, b, store, "s", options)
	if err == nil || !strings.Contains(err.Error(), "invalid saved policy") || a.Checkpoint != checkpoint {
		t.Fatal("policy failure silently used defaults")
	}
}

func TestRefreshRepeatedTokenAndDeletedPreviouslyRunning(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	b := &testBackend{experiments: []core.Experiment{{ID: "e"}}, runs: []core.Run{testRun("run", "e", "RUNNING", 100, 0)}}
	options := Options{Now: func() time.Time { return now }}
	if _, err := Refresh(ctx, b, store, "s", options); err != nil {
		t.Fatal(err)
	}
	b.runs[0].Info.LifecycleStage = "deleted"
	now = now.Add(time.Hour)
	a, err := Refresh(ctx, b, store, "s", options)
	if err != nil || len(b.getCalls) != 1 || len(core.ActivityRecords(a, "running")) != 0 || a.Counts["e"].Running != 0 {
		t.Fatal("deleted previous running run was not reconciled", err)
	}
	b.repeat = true
	now = now.Add(time.Hour)
	_, err = Refresh(ctx, b, store, "s", options)
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatal("repeated pagination token accepted")
	}
}

func TestFullRefreshCancellationPreservesCompletedFastCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	b := &testBackend{experiments: []core.Experiment{{ID: "e"}}, runs: []core.Run{testRun("run", "e", "RUNNING", 100, 0)}}
	_, err := Refresh(ctx, b, store, "s", Options{Full: true, Now: func() time.Time { return now }, Progress: func(core.ActivitySnapshot) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation result %v", err)
	}
	saved, err := store.LoadActivity(context.Background(), "s")
	if err != nil || saved.Checkpoint != now.UnixMilli() || !saved.Initialized || saved.Counts["e"].Complete {
		t.Fatal("canceled full scan lost successful fast discovery or claimed complete totals", err)
	}
}

func TestRefreshReevaluatesCachedAlertsEvenWhenDiscoveryOffline(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	oldTime := now.Add(-30 * 24 * time.Hour).UnixMilli()
	run := testRun("old-failure", "e", "FAILED", oldTime-1000, oldTime)
	seed := core.ActivityBatch{StartedAt: oldTime, ObservedAt: oldTime, Checkpoint: oldTime, Complete: true, Experiments: []core.Experiment{{ID: "e"}}, ReplaceExperiments: true, Observations: []core.ActivityObservation{{Run: run, CacheOnly: true, Retain: true}}}
	initial, err := store.ObserveActivity(ctx, "source", seed)
	if err != nil || !initial.Records[run.ID()].HasAlerts() {
		t.Fatal("missing historical alert", err)
	}
	no := false
	b := &testBackend{failExperiments: true}
	options := Options{Now: func() time.Time { return now }, Alerts: core.AlertSettings{Failed: &no}}
	result, err := Refresh(ctx, b, store, "source", options)
	if err == nil || result.Records[run.ID()].HasAlerts() || !result.Records[run.ID()].Alerts[0].Suppressed || result.Records[run.ID()].ObservedAt != oldTime || len(b.getCalls) != 0 {
		t.Fatal("offline policy toggle failed or fetched/advanced cached evidence", err)
	}
	now = now.Add(time.Minute)
	options.Alerts = core.AlertSettings{}
	result, err = Refresh(ctx, b, store, "source", options)
	if err == nil || !result.Records[run.ID()].HasAlerts() || result.Records[run.ID()].Revision != initial.Records[run.ID()].Revision {
		t.Fatal("reenabling cached episode created an artificial recurrence", err)
	}
}

type failingObservationStore struct{ core.ActivityStore }

func (s failingObservationStore) ObserveActivity(context.Context, string, core.ActivityBatch) (core.ActivitySnapshot, error) {
	return core.NewActivitySnapshot("source"), errors.New("local database busy")
}

func TestRefreshStoreFailureReturnsPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	b := &testBackend{experiments: []core.Experiment{{ID: "e"}}, runs: []core.Run{testRun("run", "e", "RUNNING", 100, 0)}}
	opts := Options{Now: func() time.Time { return now }}
	previous, err := Refresh(ctx, b, store, "source", opts)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	result, err := Refresh(ctx, b, failingObservationStore{store}, "source", opts)
	if err == nil || result.Version != previous.Version || len(result.Records) != 1 || result.Checkpoint != previous.Checkpoint {
		t.Fatal("local save error discarded last usable snapshot", err)
	}
}

func TestFirstUnreadCutoffSurvivesFailedDiscoveryRestartAndCacheClear(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Unix(1700000000, 0)
	initial := now
	cutoff := initial.Add(-7 * 24 * time.Hour).UnixMilli()
	b := &testBackend{failExperiments: true, experiments: []core.Experiment{{ID: "e"}}}
	opts := Options{Now: func() time.Time { return now }}
	failed, err := Refresh(ctx, b, store, "source", opts)
	if err == nil || failed.Initialized || failed.Checkpoint != 0 || failed.InitialUnreadSince != cutoff {
		t.Fatalf("first failed request did not preserve its cutoff: %+v %v", failed, err)
	}
	if err := store.ClearActivityCache(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	store.Close()
	store = localstate.New(path)
	defer store.Close()
	retained, err := store.LoadActivity(ctx, "source")
	if err != nil || retained.InitialUnreadSince != cutoff || retained.Initialized {
		t.Fatal("restart or cache clear reset the initial cutoff", err)
	}
	now = now.Add(20 * 24 * time.Hour)
	b.failExperiments = false
	b.runs = []core.Run{testRun("initial-window", "e", "FINISHED", initial.Add(-8*24*time.Hour).UnixMilli(), initial.Add(-6*24*time.Hour).UnixMilli())}
	retried, err := Refresh(ctx, b, store, "source", opts)
	if err != nil || !retried.Records["initial-window"].Unread() || retried.InitialUnreadSince != cutoff || retried.Checkpoint != now.UnixMilli() {
		t.Fatal("later first-scan retry moved its baseline and lost an initially eligible run", err)
	}
}
