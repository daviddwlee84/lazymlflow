package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type Collector struct {
	backend core.Backend
	target  core.Target
	notes   core.NoteStore
	// Now is replaceable for deterministic integrations; set it before collecting.
	Now func() time.Time
}

func NewCollector(backend core.Backend, target core.Target, notes core.NoteStore) *Collector {
	return &Collector{backend: backend, target: target, notes: notes, Now: time.Now}
}
func (c *Collector) snapshot(kind string, o Options) Snapshot {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	return Snapshot{Version: ContextVersion, Kind: kind, CollectedAt: now().UTC(), Source: Source{Key: core.SourceKey(c.target), TargetID: c.target.ID, Name: c.target.Label(), TrackingURI: config.RedactURI(c.target.TrackingURI), SSHHost: c.target.SSHHost}, Options: o, Runs: []RunContext{}, Warnings: []Warning{}}
}

func (c *Collector) CollectRuns(ctx context.Context, ids []string, o Options) (Snapshot, error) {
	o, err := o.normalized()
	if err != nil {
		return Snapshot{}, err
	}
	ids = unique(ids)
	if len(ids) == 0 {
		return Snapshot{}, errors.New("at least one run ID is required")
	}
	for _, id := range ids {
		if id == "" {
			return Snapshot{}, errors.New("run IDs cannot be empty")
		}
	}
	snapshot := c.snapshot("runs", o)
	snapshot.Selection = Selection{Strategy: "explicit run IDs in caller order", RequestedRunIDs: append([]string(nil), ids...), DetailedRunIDs: append([]string(nil), ids...), MatchingRuns: len(ids), AllDetails: true}
	snapshot.Runs, err = c.collectRunDetails(ctx, ids, o)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Warnings = collectWarnings(snapshot)
	return detached(snapshot)
}

func (c *Collector) CollectExperiment(ctx context.Context, id string, o ExperimentOptions) (Snapshot, error) {
	options, err := o.Options.normalized()
	if err != nil {
		return Snapshot{}, err
	}
	if id == "" {
		return Snapshot{}, errors.New("experiment ID is required")
	}
	if o.DetailLimit == 0 {
		o.DetailLimit = 20
	}
	if o.DetailLimit < 1 {
		return Snapshot{}, errors.New("detail limit must be positive")
	}
	if err = ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	experiment, err := c.backend.GetExperiment(ctx, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("get experiment %s: %w", id, err)
	}
	if experiment.ID != id {
		return Snapshot{}, fmt.Errorf("get experiment %s returned a different or missing experiment ID", id)
	}
	q := o.Query
	q.ExperimentIDs = []string{id}
	q.PageToken = ""
	if len(q.OrderBy) == 0 {
		q.OrderBy = []string{"attributes.start_time DESC"}
	}
	if q.ViewType == "" {
		q.ViewType = "ACTIVE_ONLY"
	}
	if q.MaxResults == 0 {
		q.MaxResults = 100
	}
	metadata := []core.Run{}
	tokens := map[string]bool{"": true}
	seenRuns := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		page, e := c.backend.SearchRuns(ctx, q)
		if e != nil {
			return Snapshot{}, fmt.Errorf("scan experiment %s metadata: %w", id, e)
		}
		for _, run := range page.Runs {
			if run.ID() == "" {
				return Snapshot{}, errors.New("experiment search returned a run without an ID")
			}
			if !seenRuns[run.ID()] {
				seenRuns[run.ID()] = true
				metadata = append(metadata, run)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		if tokens[page.NextPageToken] {
			return Snapshot{}, errors.New("server repeated a run page token while collecting experiment summary")
		}
		tokens[page.NextPageToken] = true
		q.PageToken = page.NextPageToken
	}
	q.PageToken = ""
	ids := []string{}
	for _, run := range metadata {
		if !o.AllDetails && len(ids) >= o.DetailLimit {
			break
		}
		ids = append(ids, run.ID())
	}
	snapshot := c.snapshot("experiment", options)
	snapshot.Selection = Selection{Strategy: "first matching runs in server query order", DetailedRunIDs: ids, MatchingRuns: len(metadata), DetailLimit: o.DetailLimit, AllDetails: o.AllDetails}
	if o.AllDetails {
		snapshot.Selection.Strategy = "all matching runs in server query order"
	}
	snapshot.Experiment = &ExperimentContext{Overview: summarizeExperimentMetadata(snapshot.Source.Key, metadata), Experiment: experiment, Query: q, MetadataComplete: true, MatchingRuns: metadata}
	snapshot.Runs, err = c.collectRunDetails(ctx, ids, options)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Experiment.Notes = c.collectNotes(ctx, core.Subject{Source: snapshot.Source.Key, Kind: "experiment", ID: id, Label: experiment.Name})
	if err = ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	snapshot.Warnings = collectWarnings(snapshot)
	return detached(snapshot)
}

func (c *Collector) collectRunDetails(ctx context.Context, ids []string, o Options) ([]RunContext, error) {
	out := make([]RunContext, len(ids))
	err := parallel(ctx, len(ids), func(ctx context.Context, i int) error {
		r, err := c.backend.GetRun(ctx, ids[i])
		if err != nil {
			return fmt.Errorf("get run %s: %w", ids[i], err)
		}
		if r.ID() != ids[i] {
			return fmt.Errorf("get run %s returned a different or missing run ID", ids[i])
		}
		out[i] = RunContext{Run: r, MetadataComplete: true, SelectedMetrics: selectedMetrics(r, o), Histories: []HistoryContext{}, DatasetNotes: []NotesContext{}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	jobs := []func(context.Context){}
	source := core.SourceKey(c.target)
	for i := range out {
		i := i
		r := out[i].Run
		out[i].Histories = make([]HistoryContext, len(out[i].SelectedMetrics))
		for j, key := range out[i].SelectedMetrics {
			j, key := j, key
			out[i].Histories[j] = HistoryContext{Key: key, Status: "omitted", Samples: []core.Metric{}}
			if o.History == HistoryNone {
				continue
			}
			jobs = append(jobs, func(ctx context.Context) {
				history, err := c.backend.MetricHistory(ctx, r.ID(), key)
				h := HistoryContext{Key: key, Status: "complete", Samples: []core.Metric{}}
				if err != nil {
					h.Status = "error"
					h.Error = err.Error()
				} else {
					h.Summary = core.SummarizeHistory(history)
					h.Observations = historyObservations(h.Summary)
					limit := 0
					if o.History == HistorySampled {
						limit = o.SampleLimit
					}
					h.Samples = core.SampleHistory(history, limit)
					h.Sampled = len(h.Samples) < len(history)
				}
				out[i].Histories[j] = h
			})
		}
		jobs = append(jobs, func(ctx context.Context) {
			page, err := c.backend.ListArtifacts(ctx, r.ID(), "")
			a := ArtifactsContext{Status: "complete", Scope: "root directory only; artifact contents are not fetched", Files: []core.Artifact{}, RootURI: config.RedactURI(page.RootURI)}
			if err != nil {
				a.Status = "error"
				a.Error = err.Error()
			} else {
				a.Files = append(a.Files, page.Files...)
				sort.SliceStable(a.Files, func(i, j int) bool { return a.Files[i].Path < a.Files[j].Path })
				if page.NextPageToken != "" {
					a.Status = "partial"
					a.Error = "backend returned an additional artifact page; only returned root entries are included"
				}
			}
			out[i].Artifacts = a
		})
		jobs = append(jobs, func(ctx context.Context) {
			out[i].Notes = c.collectNotes(ctx, core.Subject{Source: source, Kind: "run", ID: r.ID(), Label: r.Name()})
		})
		seenDatasets := map[string]bool{}
		for _, input := range r.Inputs.DatasetInputs {
			input := input
			id := core.DatasetIdentity(source, input.Dataset)
			if seenDatasets[id] {
				continue
			}
			seenDatasets[id] = true
			j := len(out[i].DatasetNotes)
			out[i].DatasetNotes = append(out[i].DatasetNotes, NotesContext{})
			jobs = append(jobs, func(ctx context.Context) {
				out[i].DatasetNotes[j] = c.collectNotes(ctx, core.Subject{Source: source, Kind: "dataset", ID: id, Label: input.Dataset.Name})
			})
		}
	}
	err = parallel(ctx, len(jobs), func(ctx context.Context, i int) error { jobs[i](ctx); return ctx.Err() })
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Collector) collectNotes(ctx context.Context, subject core.Subject) NotesContext {
	out := NotesContext{Subject: subject, Status: "complete", Notes: []core.Note{}}
	if c.notes == nil {
		out.Status = "unavailable"
		out.Error = "local notes store is unavailable"
		return out
	}
	notes, err := c.notes.ListNotes(ctx, subject, false)
	if err != nil {
		out.Status = "error"
		out.Error = err.Error()
		return out
	}
	for _, note := range notes {
		if note.DeletedAt == 0 {
			out.Notes = append(out.Notes, note)
		}
	}
	sort.SliceStable(out.Notes, func(i, j int) bool {
		if out.Notes[i].CreatedAt != out.Notes[j].CreatedAt {
			return out.Notes[i].CreatedAt < out.Notes[j].CreatedAt
		}
		return out.Notes[i].ID < out.Notes[j].ID
	})
	return out
}

func selectedMetrics(run core.Run, o Options) []string {
	if len(o.Metrics) > 0 {
		return append([]string(nil), o.Metrics...)
	}
	keys := []string{}
	for _, metric := range run.Data.Metrics {
		if o.IncludeSystem || !strings.HasPrefix(metric.Key, "system/") {
			keys = append(keys, metric.Key)
		}
	}
	keys = unique(keys)
	sort.Strings(keys)
	return keys
}
func unique(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
func parallel(ctx context.Context, n int, fn func(context.Context, int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < min(4, n); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if child.Err() != nil {
					continue
				}
				if err := fn(child, j); err != nil {
					errs[j] = err
					cancel()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-child.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
func collectWarnings(snapshot Snapshot) []Warning {
	warnings := []Warning{}
	addNotes := func(n NotesContext) {
		if n.Status != "complete" {
			warnings = append(warnings, Warning{Subject: n.Subject.Kind + ":" + n.Subject.ID, Section: "notes", Message: n.Error})
		}
	}
	if snapshot.Experiment != nil {
		addNotes(snapshot.Experiment.Notes)
	}
	for _, run := range snapshot.Runs {
		for _, h := range run.Histories {
			if h.Status == "error" {
				warnings = append(warnings, Warning{Subject: "run:" + run.Run.ID(), Section: "history:" + h.Key, Message: h.Error})
			}
		}
		if run.Artifacts.Status != "complete" {
			warnings = append(warnings, Warning{Subject: "run:" + run.Run.ID(), Section: "artifacts", Message: run.Artifacts.Error})
		}
		addNotes(run.Notes)
		for _, n := range run.DatasetNotes {
			addNotes(n)
		}
	}
	return warnings
}

// detached prevents returned evidence from sharing mutable slices/maps with a
// backend's caches, the caller's options, or local note objects.
func detached(snapshot Snapshot) (Snapshot, error) {
	b, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	var out Snapshot
	err = json.Unmarshal(b, &out)
	return out, err
}
