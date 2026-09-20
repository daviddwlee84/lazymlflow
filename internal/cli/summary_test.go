package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/inspection"
)

type summaryBackend struct {
	*fakeBackend
	history   func(context.Context, string, string) ([]core.Metric, error)
	artifacts func(context.Context, string, string) (core.ArtifactPage, error)
}

func (b *summaryBackend) MetricHistory(ctx context.Context, id, key string) ([]core.Metric, error) {
	if b.history != nil {
		return b.history(ctx, id, key)
	}
	return b.fakeBackend.MetricHistory(ctx, id, key)
}
func (b *summaryBackend) ListArtifacts(ctx context.Context, id, path string) (core.ArtifactPage, error) {
	if b.artifacts != nil {
		return b.artifacts(ctx, id, path)
	}
	return b.fakeBackend.ListArtifacts(ctx, id, path)
}
func summaryRun(id string) core.Run {
	return core.Run{Info: core.RunInfo{RunID: id, ExperimentID: "e", RunName: "run " + id, Status: "FINISHED"}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: 3}, {Key: "system/cpu", Value: 5}}}}
}

func TestRunsSummaryMarkdownJSONAndHistoryFlags(t *testing.T) {
	for _, mode := range []string{"markdown", "json"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			opts, connector, out, stderr := testOptions(t)
			var mu sync.Mutex
			keys := []string{}
			connector.backend = &summaryBackend{fakeBackend: &fakeBackend{getRun: func(_ context.Context, id string) (core.Run, error) { return summaryRun(id), nil }}, history: func(_ context.Context, id, key string) ([]core.Metric, error) {
				mu.Lock()
				keys = append(keys, key)
				mu.Unlock()
				return []core.Metric{{Key: key, Value: 1, Step: 1}, {Key: key, Value: 3, Step: 2}}, nil
			}}
			args := []string{"runs", "summary", "r1", "r2", "--metric", "loss", "--history", "sampled"}
			if mode == "json" {
				args = append(args, "--json")
			}
			if status := Execute(context.Background(), args, opts); status != 0 {
				t.Fatalf("status %d: %s", status, stderr)
			}
			if len(keys) != 2 || keys[0] != "loss" || keys[1] != "loss" {
				t.Fatal("explicit metric selection changed", keys)
			}
			if stderr.Len() != 0 || strings.Contains(out.String(), "\x1b") {
				t.Fatal("summary output contains diagnostics/ANSI")
			}
			if mode == "json" {
				var s inspection.Snapshot
				if err := json.Unmarshal(out.Bytes(), &s); err != nil {
					t.Fatal(err)
				}
				if s.Version != 1 || len(s.Runs) != 2 || s.Options.SampleLimit != 200 || len(s.Runs[0].Run.Data.Metrics) != 2 {
					t.Fatal("invalid context")
				}
			} else if !strings.HasPrefix(out.String(), "# Run comparison summary") || !strings.Contains(out.String(), "Latest metric comparison") {
				t.Fatal("missing deterministic Markdown summary")
			}
		})
	}
}
func TestSummaryPartialOptionalFailureAndRequiredFailure(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			opts, connector, out, stderr := testOptions(t)
			connector.backend = &summaryBackend{fakeBackend: &fakeBackend{getRun: func(_ context.Context, id string) (core.Run, error) {
				if required {
					return core.Run{}, errors.New("run denied")
				}
				return summaryRun(id), nil
			}}, history: func(context.Context, string, string) ([]core.Metric, error) {
				return nil, errors.New("history unavailable")
			}}
			status := Execute(context.Background(), []string{"runs", "summary", "r", "--json"}, opts)
			if required {
				if status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "run denied") {
					t.Fatal("required failure not surfaced", status, out, stderr)
				}
				return
			}
			var s inspection.Snapshot
			if status != 0 || json.Unmarshal(out.Bytes(), &s) != nil || len(s.Warnings) != 1 || s.Warnings[0].Section != "history:loss" {
				t.Fatalf("partial context incorrect %d %s %s", status, out, stderr)
			}
		})
	}
}
func TestExperimentSummaryAlwaysScansAllMetadataAndLimitsDetails(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			opts, connector, out, stderr := testOptions(t)
			pages := 0
			var detailedMu sync.Mutex
			details := 0
			connector.backend = &fakeBackend{runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
				pages++
				if q.Filter != "params.task = 'train'" || q.OrderBy[0] != "attributes.start_time DESC" || q.ViewType != "ALL" {
					t.Error("query flags lost", q)
				}
				start, end, next := 0, 15, "next"
				if q.PageToken == "next" {
					start, end, next = 15, 27, ""
				}
				rows := []core.Run{}
				for i := start; i < end; i++ {
					rows = append(rows, summaryRun(fmt.Sprintf("r%d", i)))
				}
				return core.RunPage{Runs: rows, NextPageToken: next}, nil
			}, getRun: func(_ context.Context, id string) (core.Run, error) {
				detailedMu.Lock()
				details++
				detailedMu.Unlock()
				return summaryRun(id), nil
			}}
			args := []string{"experiments", "summary", "e", "--filter", "params.task = 'train'", "--view", "all", "--history", "none", "--json"}
			if all {
				args = append(args, "--all-details")
			}
			if status := Execute(context.Background(), args, opts); status != 0 {
				t.Fatalf("status %d: %s", status, stderr)
			}
			var s inspection.Snapshot
			if err := json.Unmarshal(out.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			want := 20
			if all {
				want = 27
			}
			if pages != 2 || details != want || len(s.Experiment.MatchingRuns) != 27 || len(s.Runs) != want {
				t.Fatalf("page/detail scope wrong pages=%d details=%d", pages, details)
			}
		})
	}
}
func TestSummaryUsageValidatesBeforeConnecting(t *testing.T) {
	cases := [][]string{{"runs", "summary"}, {"runs", "summary", "r", "--history", "bad"}, {"runs", "summary", "r", "--metric="}, {"experiments", "summary", "e", "--detail-limit", "0"}, {"experiments", "summary", "e", "--view", "bad"}, {"prompt", "render", "compare-runs", "r", "r"}, {"prompt", "render", "run-summary", "r1", "r2"}}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			opts, connector, out, stderr := testOptions(t)
			if status := Execute(context.Background(), args, opts); status != 2 {
				t.Fatal("usage status", status, stderr)
			}
			if len(connector.opened) != 0 || out.Len() != 0 {
				t.Fatal("invalid flags caused I/O/output")
			}
		})
	}
}
