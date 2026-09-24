// Package activity discovers cross-experiment activity using the public MLflow
// API. It never loads metric histories or mutates the tracking server.
package activity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type Options struct {
	Settings   core.ActivitySettings
	Alerts     core.AlertSettings
	UnreadDays int
	Full       bool
	// ExperimentIDs restricts only a full status-population scan. Fast activity
	// discovery always covers every accessible active experiment.
	ExperimentIDs []string
	RecentLimit   int
	PageSize      int
	Policy        func(context.Context, string) (core.ActivityPolicy, error)
	Now           func() time.Time
	// Progress runs serially, after each successful durable commit. A caller may
	// forward it into its event loop; it must not mutate the supplied snapshot.
	Progress func(core.ActivitySnapshot)
}

const overlap = time.Minute

func Refresh(ctx context.Context, backend core.Backend, store core.ActivityStore, source string, options Options) (core.ActivitySnapshot, error) {
	result := core.NewActivitySnapshot(source)
	if backend == nil || store == nil || strings.TrimSpace(source) == "" {
		return result, errors.New("activity refresh requires a backend, local activity store and source")
	}
	if options.Settings == (core.ActivitySettings{}) {
		options.Settings = core.DefaultActivitySettings()
	}
	if err := core.ValidateActivitySettings(options.Settings); err != nil {
		return result, err
	}
	if err := core.ValidateAlertSettings(options.Alerts); err != nil {
		return result, err
	}
	if options.UnreadDays < 0 {
		return result, errors.New("unread days must be nonnegative")
	}
	if options.PageSize <= 0 {
		options.PageSize = 100
	}
	if options.RecentLimit == 0 {
		options.RecentLimit = 100
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	started := options.Now().UnixMilli()
	initialSince := max(int64(1), started-int64(options.Settings.InitialUnreadDays)*int64(24*time.Hour/time.Millisecond))
	forceSince := int64(0)
	if options.UnreadDays > 0 {
		forceSince = started - int64(options.UnreadDays)*int64(24*time.Hour/time.Millisecond)
	}
	var err error
	result, err = store.LoadActivity(ctx, source)
	if err != nil {
		return core.ActivitySnapshot{}, err
	}
	if result.InitialUnreadSince == 0 {
		// Fix the first-use window before any server request. A failed page,
		// disconnection or restart must not move the user's unread baseline.
		updated, err := store.ObserveActivity(ctx, source, core.ActivityBatch{StartedAt: started, ObservedAt: started, InitialUnreadSince: initialSince, Complete: result.Complete, Errors: result.Errors})
		if err != nil {
			return result, err
		}
		result = updated
	}
	initialSince = result.InitialUnreadSince
	// Resolve policies for retained records even when server discovery fails.
	// This lets an alert toggle affect cached historical alerts without extra
	// GetRun calls or pretending those old values were freshly fetched.
	policies := map[string]core.ActivityPolicy{}
	resolvePolicy := func(id string) error {
		if _, ok := policies[id]; ok {
			return nil
		}
		override := core.ActivityPolicy{}
		if options.Policy != nil {
			var err error
			override, err = options.Policy(ctx, id)
			if err != nil {
				return fmt.Errorf("load activity policy for experiment %s: %w", id, err)
			}
		}
		policy := core.EffectiveActivityPolicy(options.Settings, options.Alerts, override)
		if err := core.ValidateActivityPolicy(policy); err != nil {
			return err
		}
		policies[id] = policy
		return nil
	}
	for _, record := range result.Records {
		if err := resolvePolicy(record.ExperimentID); err != nil {
			return failed(ctx, store, source, result, started, err)
		}
	}
	since := initialSince
	if result.Initialized {
		since = max(int64(0), result.Checkpoint-int64(overlap/time.Millisecond))
	}
	if forceSince > 0 && forceSince < since {
		since = forceSince
	}
	experiments, err := collectExperiments(ctx, backend, options.PageSize)
	if err != nil {
		return failed(ctx, store, source, result, started, err, policies)
	}
	ids := make([]string, 0, len(experiments))
	names := map[string]string{}
	for _, experiment := range experiments {
		ids = append(ids, experiment.ID)
		names[experiment.ID] = experiment.Name
		if err := resolvePolicy(experiment.ID); err != nil {
			return failed(ctx, store, source, result, started, err)
		}
	}
	observed := map[string]core.Run{}
	running := map[string]bool{}
	var scanErr error
	if len(ids) > 0 {
		queries := []struct {
			filter  string
			order   []string
			limit   int
			running bool
		}{
			{"attributes.status = 'RUNNING'", []string{"attributes.start_time DESC", "attributes.run_id ASC"}, -1, true},
			{fmt.Sprintf("attributes.start_time >= %d", max(int64(0), since)), []string{"attributes.start_time DESC", "attributes.run_id ASC"}, -1, false},
			{fmt.Sprintf("attributes.end_time >= %d", max(int64(1), since)), []string{"attributes.end_time DESC", "attributes.run_id ASC"}, -1, false},
			{"attributes.end_time > 0", []string{"attributes.end_time DESC", "attributes.run_id ASC"}, options.RecentLimit, false},
		}
		for _, query := range queries {
			rows, e := collectRuns(ctx, backend, core.RunQuery{ExperimentIDs: ids, Filter: query.filter, OrderBy: query.order, ViewType: "ACTIVE_ONLY", MaxResults: options.PageSize}, query.limit)
			for _, run := range rows {
				observed[run.ID()] = run
				if query.running {
					running[run.ID()] = true
				}
			}
			if e != nil {
				scanErr = e
				break
			}
		}
	}
	if scanErr == nil {
		// A known running run can disappear from active searches after deletion,
		// experiment lifecycle changes, or an unusual/backdated completion time.
		knownIDs := make([]string, 0, len(result.Records))
		for id, record := range result.Records {
			if record.Status == "RUNNING" && record.LifecycleStage != "deleted" && !running[id] {
				knownIDs = append(knownIDs, id)
			}
		}
		sort.Strings(knownIDs)
		for _, id := range knownIDs {
			if run, ok := observed[id]; ok && run.Info.Status != "RUNNING" {
				continue
			}
			run, e := backend.GetRun(ctx, id)
			if e != nil {
				scanErr = fmt.Errorf("refresh previously running run %s: %w", id, e)
				break
			}
			if run.ID() != id {
				scanErr = fmt.Errorf("get run %s returned a different or missing ID", id)
				break
			}
			observed[id] = run
			if _, ok := names[run.Info.ExperimentID]; !ok {
				names[run.Info.ExperimentID] = result.Records[id].ExperimentName
				policies[run.Info.ExperimentID] = core.EffectiveActivityPolicy(options.Settings, options.Alerts, core.ActivityPolicy{})
			}
		}
	}
	batch := core.ActivityBatch{Experiments: experiments, ReplaceExperiments: scanErr == nil, StartedAt: started, ObservedAt: started, InitialUnreadSince: initialSince, ForceUnreadSince: forceSince, Complete: scanErr == nil, PolicyUpdates: policies}
	if scanErr == nil {
		batch.Checkpoint = started
	} else {
		batch.Errors = []string{scanErr.Error()}
	}
	for _, id := range sortedRunIDs(observed) {
		run := observed[id]
		// Recent display rows outside the discovery window establish a read
		// baseline rather than generating a historical new-run notification.
		cacheOnly := run.Info.Status != "RUNNING" && run.Info.StartTime < since && run.Info.EndTime < since
		batch.Observations = append(batch.Observations, core.ActivityObservation{Run: run, ExperimentName: names[run.Info.ExperimentID], Policy: policies[run.Info.ExperimentID], CacheOnly: cacheOnly})
	}
	// Recent display rows must remain available even when they are older than
	// the notification window. CacheOnly controls notifications, not this set.
	for i := range batch.Observations {
		batch.Observations[i].Retain = true
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	updated, err := store.ObserveActivity(ctx, source, batch)
	if err != nil {
		return result, err
	}
	result = updated
	if options.Progress != nil {
		options.Progress(result)
	}
	if scanErr != nil {
		return result, scanErr
	}
	if !options.Full {
		return result, nil
	}
	return fullScan(ctx, backend, store, source, options, result, experiments, names, policies, initialSince)
}

func failed(ctx context.Context, store core.ActivityStore, source string, previous core.ActivitySnapshot, at int64, failure error, policies ...map[string]core.ActivityPolicy) (core.ActivitySnapshot, error) {
	if ctx.Err() != nil {
		return previous, ctx.Err()
	}
	batch := core.ActivityBatch{StartedAt: at, ObservedAt: at, Errors: []string{failure.Error()}}
	if len(policies) > 0 {
		batch.PolicyUpdates = policies[0]
	}
	result, err := store.ObserveActivity(ctx, source, batch)
	if err != nil {
		return previous, errors.Join(failure, err)
	}
	return result, failure
}

func sortedRunIDs(runs map[string]core.Run) []string {
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func collectExperiments(ctx context.Context, backend core.Backend, pageSize int) ([]core.Experiment, error) {
	result := []core.Experiment{}
	ids, tokens := map[string]bool{}, map[string]bool{}
	q := core.ExperimentQuery{ViewType: "ACTIVE_ONLY", OrderBy: []string{"name ASC"}, MaxResults: pageSize}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := backend.SearchExperiments(ctx, q)
		if err != nil {
			return result, err
		}
		for _, experiment := range page.Experiments {
			if experiment.ID == "" {
				return result, errors.New("experiment search returned a missing ID")
			}
			if !ids[experiment.ID] {
				result = append(result, experiment)
				ids[experiment.ID] = true
			}
		}
		if page.NextPageToken == "" {
			return result, nil
		}
		if tokens[page.NextPageToken] {
			return result, errors.New("server repeated an experiment page token during activity discovery")
		}
		tokens[page.NextPageToken], q.PageToken = true, page.NextPageToken
	}
}

func collectRuns(ctx context.Context, backend core.Backend, q core.RunQuery, limit int) ([]core.Run, error) {
	result := []core.Run{}
	ids, tokens := map[string]bool{}, map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if limit > 0 {
			q.MaxResults = min(q.MaxResults, limit-len(result))
		}
		page, err := backend.SearchRuns(ctx, q)
		if err != nil {
			return result, err
		}
		for _, run := range page.Runs {
			if run.ID() == "" || run.Info.ExperimentID == "" {
				return result, errors.New("run search returned a missing run or experiment ID")
			}
			if !ids[run.ID()] {
				result = append(result, run)
				ids[run.ID()] = true
			}
			if limit > 0 && len(result) >= limit {
				return result, nil
			}
		}
		if page.NextPageToken == "" {
			return result, nil
		}
		if tokens[page.NextPageToken] {
			return result, errors.New("server repeated a run page token during activity discovery")
		}
		tokens[page.NextPageToken], q.PageToken = true, page.NextPageToken
	}
}

type experimentResult struct {
	id      string
	started int64
	runs    []core.Run
	err     error
}

func fullScan(ctx context.Context, backend core.Backend, store core.ActivityStore, source string, options Options, snapshot core.ActivitySnapshot, experiments []core.Experiment, names map[string]string, policies map[string]core.ActivityPolicy, initialSince int64) (core.ActivitySnapshot, error) {
	var ids []string
	if len(options.ExperimentIDs) == 0 {
		for _, e := range experiments {
			ids = append(ids, e.ID)
		}
	} else {
		seen := map[string]bool{}
		for _, id := range options.ExperimentIDs {
			if _, exists := names[id]; !exists {
				return snapshot, errors.New("full activity scan requested an unknown or inactive experiment")
			}
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	results := make(chan experimentResult, 2)
	var workers sync.WaitGroup
	for range min(2, len(ids)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				started := options.Now().UnixMilli()
				runs, err := collectRuns(ctx, backend, core.RunQuery{ExperimentIDs: []string{id}, ViewType: "ACTIVE_ONLY", MaxResults: options.PageSize, OrderBy: []string{"attributes.start_time DESC", "attributes.run_id ASC"}}, -1)
				select {
				case results <- experimentResult{id, started, runs, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, id := range ids {
			select {
			case jobs <- id:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	var failures []error
	for result := range results {
		if result.err != nil {
			failures = append(failures, fmt.Errorf("scan experiment %s: %w", result.id, result.err))
			continue
		}
		batch := core.ActivityBatch{StartedAt: result.started, ObservedAt: result.started, Complete: true, CompleteExperiments: []string{result.id}, InitialUnreadSince: initialSince}
		for _, run := range result.runs {
			cacheOnly := run.Info.Status != "RUNNING" && run.Info.StartTime < initialSince && run.Info.EndTime < initialSince
			batch.Observations = append(batch.Observations, core.ActivityObservation{Run: run, ExperimentName: names[result.id], Policy: policies[result.id], CacheOnly: cacheOnly})
		}
		var err error
		updated, err := store.ObserveActivity(ctx, source, batch)
		if err != nil {
			cancel()
			return snapshot, err
		}
		snapshot = updated
		if options.Progress != nil {
			options.Progress(snapshot)
		}
	}
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	at := options.Now().UnixMilli()
	final := core.ActivityBatch{StartedAt: at, ObservedAt: at, Complete: len(failures) == 0, FullScanComplete: len(failures) == 0}
	for _, failure := range failures {
		final.Errors = append(final.Errors, failure.Error())
	}
	updated, err := store.ObserveActivity(ctx, source, final)
	if err != nil {
		return snapshot, errors.Join(append(failures, err)...)
	}
	if options.Progress != nil {
		options.Progress(updated)
	}
	return updated, errors.Join(failures...)
}
