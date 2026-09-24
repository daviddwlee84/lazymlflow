package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
)

type runPinCLIBackend struct {
	*fakeBackend
	getExperiment func(context.Context, string) (core.Experiment, error)
}

func (b *runPinCLIBackend) GetExperiment(ctx context.Context, id string) (core.Experiment, error) {
	return b.getExperiment(ctx, id)
}

func TestRunPinCommandFetchesMetadataAndLocalCommandsWorkOffline(t *testing.T) {
	path := isolateLocalState(t)
	ctx := context.Background()
	for _, runID := range []string{"one", "two"} {
		opts, connector, out, stderr := testOptions(t)
		getCalls, experimentCalls := 0, 0
		connector.backend = &runPinCLIBackend{
			fakeBackend: &fakeBackend{getRun: func(_ context.Context, id string) (core.Run, error) {
				getCalls++
				if id != runID {
					t.Fatalf("wrong run requested: %q", id)
				}
				return core.Run{
					Info: core.RunInfo{RunID: id, ExperimentID: "experiment-" + id, RunName: "Run " + id, Status: "FINISHED", StartTime: 100, EndTime: 200, LifecycleStage: "active", ArtifactURI: "s3://private/DO-NOT-PERSIST"},
					Data: core.RunData{Params: []core.KeyValue{{Key: "password", Value: "DO-NOT-PERSIST"}}, Tags: []core.KeyValue{{Key: "private", Value: "DO-NOT-PERSIST"}}, Metrics: []core.Metric{{Key: "DO-NOT-PERSIST", Value: 1}}},
				}, nil
			}},
			getExperiment: func(_ context.Context, id string) (core.Experiment, error) {
				experimentCalls++
				if id != "experiment-"+runID {
					t.Fatalf("wrong experiment requested: %q", id)
				}
				return core.Experiment{ID: id, Name: "Experiment " + runID, ArtifactLocation: "DO-NOT-PERSIST"}, nil
			},
		}
		if status := Execute(ctx, []string{"runs", "pin", runID, "--json"}, opts); status != 0 {
			t.Fatalf("pin %s: %d %s", runID, status, stderr)
		}
		var result struct {
			RunID     string `json:"run_id"`
			Pinned    bool   `json:"pinned"`
			LocalOnly bool   `json:"local_only"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.RunID != runID || !result.Pinned || !result.LocalOnly {
			t.Fatalf("pin result %s (%v)", out, err)
		}
		if getCalls != 1 || experimentCalls != 1 || len(connector.opened) != 1 || connector.sessionClosed != 1 || connector.closed != 1 {
			t.Fatalf("lookup/cleanup calls: runs=%d experiments=%d connector=%+v", getCalls, experimentCalls, connector)
		}
	}
	state := localstate.New(path)
	pins, err := state.LoadRunPins(ctx, firstSource())
	state.Close()
	if err != nil || len(pins) != 2 {
		t.Fatalf("persistent pins: %+v %v", pins, err)
	}
	for _, pin := range pins {
		if pin.ExperimentID != "experiment-"+pin.RunID || pin.RunName != "Run "+pin.RunID || pin.ExperimentName != "Experiment "+pin.RunID || pin.Status != "FINISHED" || pin.StartTime != 100 || pin.EndTime != 200 || pin.PinnedAt <= 0 || pin.ObservedAt <= 0 {
			t.Fatalf("lost metadata: %+v", pin)
		}
	}
	if pins[0].PinnedAt < pins[1].PinnedAt || (pins[0].PinnedAt == pins[1].PinnedAt && pins[0].RunID > pins[1].RunID) {
		t.Fatalf("pins not deterministically newest first: %+v", pins)
	}

	for _, jsonOutput := range []bool{false, true} {
		opts, connector, out, stderr := testOptions(t)
		args := []string{"runs", "pinned"}
		if jsonOutput {
			args = append(args, "--json")
		}
		if status := Execute(ctx, args, opts); status != 0 {
			t.Fatalf("pinned: %d %s", status, stderr)
		}
		if len(connector.opened) != 0 || strings.Contains(out.String(), "DO-NOT-PERSIST") {
			t.Fatalf("cached list connected or exposed full metadata: %s", out)
		}
		if jsonOutput {
			var result struct {
				Source    string        `json:"source"`
				Pins      []core.RunPin `json:"pins"`
				Cached    bool          `json:"cached"`
				LocalOnly bool          `json:"local_only"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Source != firstSource() || len(result.Pins) != 2 || !result.Cached || !result.LocalOnly {
				t.Fatalf("cached list: %s (%v)", out, err)
			}
		} else if !strings.Contains(out.String(), "cached metadata") || !strings.Contains(out.String(), "Experiment one") || !strings.Contains(out.String(), "Experiment two") {
			t.Fatalf("text list missing cached identity: %s", out)
		}
	}

	// Another target has an independent pin namespace, including for removal.
	for _, args := range [][]string{{"runs", "unpin", "one", "--target", "second", "--json"}, {"runs", "pinned", "--target", "second", "--json"}} {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(ctx, args, opts); status != 0 || len(connector.opened) != 0 {
			t.Fatalf("offline operation: %d %s", status, stderr)
		}
		if args[1] == "pinned" && !strings.Contains(out.String(), `"pins": []`) {
			t.Fatalf("source leaked pins: %s", out)
		}
	}
	// Removing the same run twice is safe and never opens the remote target.
	for range 2 {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(ctx, []string{"runs", "unpin", "one", "--json"}, opts); status != 0 || len(connector.opened) != 0 || !strings.Contains(out.String(), `"pinned": false`) {
			t.Fatalf("unpin: %d %s %s", status, out, stderr)
		}
	}
	state = localstate.New(path)
	defer state.Close()
	pins, err = state.LoadRunPins(ctx, firstSource())
	if err != nil || len(pins) != 1 || pins[0].RunID != "two" {
		t.Fatalf("unpin changed another run: %+v %v", pins, err)
	}
}

func TestRunPinsMissingStateAndUsageDoNotCreateOrConnect(t *testing.T) {
	path := isolateLocalState(t)
	for _, args := range [][]string{{"runs", "pinned", "--json"}, {"runs", "pinned"}, {"runs", "unpin", "missing", "--json"}} {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 0 || len(connector.opened) != 0 || out.Len() == 0 {
			t.Fatalf("missing state %v: %d %s", args, status, stderr)
		}
	}
	for _, args := range [][]string{{"runs", "pin"}, {"runs", "unpin"}, {"runs", "pin", " "}, {"runs", "unpin", ""}, {"runs", "pinned", "extra"}} {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(context.Background(), append(args, "--json"), opts); status != 2 || len(connector.opened) != 0 || out.Len() != 0 {
			t.Fatalf("usage %v: %d %s %s", args, status, out, stderr)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("read/unpin/invalid pin created local state: %v", err)
	}
}

func TestRunPinLookupFailuresLeaveNoPin(t *testing.T) {
	for _, failure := range []string{"run error", "wrong run", "no experiment", "experiment error", "wrong experiment", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			path := isolateLocalState(t)
			opts, connector, out, stderr := testOptions(t)
			connector.backend = &runPinCLIBackend{
				fakeBackend: &fakeBackend{getRun: func(_ context.Context, id string) (core.Run, error) {
					run := core.Run{Info: core.RunInfo{RunID: id, ExperimentID: "experiment"}}
					switch failure {
					case "run error":
						return core.Run{}, errors.New("run unavailable")
					case "wrong run":
						run.Info.RunID = "different"
					case "no experiment":
						run.Info.ExperimentID = ""
					case "cancel":
						return core.Run{}, context.Canceled
					}
					return run, nil
				}},
				getExperiment: func(_ context.Context, id string) (core.Experiment, error) {
					if failure == "experiment error" {
						return core.Experiment{}, errors.New("experiment unavailable")
					}
					if failure == "wrong experiment" {
						id = "different"
					}
					return core.Experiment{ID: id}, nil
				},
			}
			wantStatus := 1
			if failure == "cancel" {
				wantStatus = 130
			}
			if status := Execute(context.Background(), []string{"runs", "pin", "run", "--json"}, opts); status != wantStatus || out.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("status=%d out=%s stderr=%s", status, out, stderr)
			}
			if connector.sessionClosed != 1 || connector.closed != 1 {
				t.Fatal("failed lookup leaked session")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failed lookup persisted a pin", err)
			}
		})
	}
}
