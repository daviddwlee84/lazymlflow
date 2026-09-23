package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/daviddwlee84/lazymlflow/internal/fileuri"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/tui"
)

type fakeBackend struct {
	experiment func(context.Context, core.ExperimentQuery) (core.ExperimentPage, error)
	runs       func(context.Context, core.RunQuery) (core.RunPage, error)
	getRun     func(context.Context, string) (core.Run, error)
	download   func(context.Context, core.DownloadRequest) (core.DownloadResult, error)
}

func (f *fakeBackend) SearchExperiments(c context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
	if f.experiment != nil {
		return f.experiment(c, q)
	}
	return core.ExperimentPage{Experiments: []core.Experiment{}}, nil
}
func (f *fakeBackend) GetExperiment(_ context.Context, id string) (core.Experiment, error) {
	return core.Experiment{ID: id, Name: "Experiment"}, nil
}
func (f *fakeBackend) SearchRuns(c context.Context, q core.RunQuery) (core.RunPage, error) {
	if f.runs != nil {
		return f.runs(c, q)
	}
	return core.RunPage{Runs: []core.Run{}}, nil
}
func (f *fakeBackend) GetRun(c context.Context, id string) (core.Run, error) {
	if f.getRun != nil {
		return f.getRun(c, id)
	}
	return core.Run{Info: core.RunInfo{RunID: id, ExperimentID: "2", RunName: "run"}}, nil
}
func (f *fakeBackend) MetricHistory(context.Context, string, string) ([]core.Metric, error) {
	return []core.Metric{{Key: "loss", Step: 1, Value: core.Number(math.Inf(1))}}, nil
}
func (f *fakeBackend) ListArtifacts(context.Context, string, string) (core.ArtifactPage, error) {
	return core.ArtifactPage{}, nil
}
func (f *fakeBackend) DownloadArtifact(c context.Context, r core.DownloadRequest, _ func(core.Progress)) (core.DownloadResult, error) {
	if f.download != nil {
		return f.download(c, r)
	}
	return core.DownloadResult{Path: r.Destination, Files: 1, Bytes: 12}, nil
}

type fakeConnector struct {
	backend       core.Backend
	opened        []core.Target
	closed        int
	sessionClosed int
	local         bool
}

func (f *fakeConnector) Open(_ context.Context, t core.Target) (*core.Session, error) {
	f.opened = append(f.opened, t)
	return &core.Session{Backend: f.backend, Target: t, BaseURL: t.TrackingURI, WebURL: t.WebURL, Local: f.local, CloseFunc: func() error { f.sessionClosed++; return nil }}, nil
}
func (f *fakeConnector) Close() error { f.closed++; return nil }

func testOptions(t *testing.T) (Options, *fakeConnector, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("LAZYMLFLOW_TARGET", "")
	t.Setenv("MLFLOW_TRACKING_URI", "")
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	f := &fakeConnector{backend: &fakeBackend{}}
	cfg := &config.Config{DefaultTarget: "first", Targets: []core.Target{{ID: "first", TrackingURI: "http://first:5000"}, {ID: "second", TrackingURI: "https://second/prefix"}}}
	return Options{In: strings.NewReader(""), Out: out, Err: stderr, Connector: f, IsTerminal: func() bool { return false }, LoadConfig: func(string) (*config.Config, error) {
		copy := *cfg
		copy.Targets = append([]core.Target(nil), cfg.Targets...)
		return &copy, nil
	}}, f, out, stderr
}

func TestStaticCommandsDoNotLoadConfiguration(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"version"}, {"--version"}, {"completion", "bash"}, {}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			opts, connector, out, stderr := testOptions(t)
			opts.LoadConfig = func(string) (*config.Config, error) { t.Fatal("static command loaded configuration"); return nil, nil }
			if status := Execute(context.Background(), args, opts); status != 0 {
				t.Fatalf("status %d: %s", status, stderr)
			}
			if len(connector.opened) > 0 || out.Len() == 0 {
				t.Fatalf("unexpected result: %s", out)
			}
		})
	}
}

func TestVersionFlagHonorsJSONWithoutConfig(t *testing.T) {
	opts, _, out, stderr := testOptions(t)
	opts.Version = "v1.2.3"
	opts.LoadConfig = func(string) (*config.Config, error) { t.Fatal("version loaded configuration"); return nil, nil }
	if status := Execute(context.Background(), []string{"--version", "--json"}, opts); status != 0 {
		t.Fatalf("status=%d %s", status, stderr)
	}
	var version map[string]string
	if err := json.Unmarshal(out.Bytes(), &version); err != nil || version["version"] != opts.Version {
		t.Fatalf("wrong JSON: %s (%v)", out, err)
	}
}

func TestMachineErrorsAndUsage(t *testing.T) {
	cases := [][]string{{"--json"}, {"--json", "runs"}, {"--json", "--no-such-flag"}, {"--json", "experiments", "get"}, {"--json", "runs", "list", "--limit", "0"}, {"--json", "runs", "list", "--view", "unknown"}, {"--json", "--interactive", "targets", "add"}, {"--json", "--target", "first", "--tracking-uri", "https://other", "runs", "list"}, {"--json", "--target=", "runs", "list"}, {"--json", "open"}, {"--json", "targets", "add", "partial"}, {"--json", "nonsense"}}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			opts, connector, out, stderr := testOptions(t)
			if status := Execute(context.Background(), args, opts); status != 2 {
				t.Fatalf("status %d: %s", status, stderr)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout polluted: %s", out)
			}
			var v struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &v); err != nil || v.Error.Code != "usage_error" {
				t.Fatalf("invalid error: %s (%v)", stderr, err)
			}
			if len(connector.opened) > 0 {
				t.Fatal("usage error opened connection")
			}
		})
	}
}

func TestTargetPrecedence(t *testing.T) {
	cases := []struct {
		name, targetEnv, uriEnv string
		flags                   []string
		want                    string
	}{{"default", "", "", nil, "first"}, {"app env", "second", "https://env", nil, "second"}, {"mlflow env", "", "https://env", nil, "temporary"}, {"target flag", "first", "https://env", []string{"--target", "second"}, "second"}, {"uri flag", "second", "https://env", []string{"--tracking-uri", "https://flag"}, "temporary"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, connector, _, stderr := testOptions(t)
			t.Setenv("LAZYMLFLOW_TARGET", tc.targetEnv)
			t.Setenv("MLFLOW_TRACKING_URI", tc.uriEnv)
			args := append([]string{"experiments", "list", "--json"}, tc.flags...)
			if status := Execute(context.Background(), args, opts); status != 0 {
				t.Fatalf("%d: %s", status, stderr)
			}
			if len(connector.opened) != 1 || connector.opened[0].ID != tc.want {
				t.Fatalf("opened %#v", connector.opened)
			}
			if connector.closed != 1 || connector.sessionClosed != 1 {
				t.Fatalf("cleanup manager=%d session=%d", connector.closed, connector.sessionClosed)
			}
		})
	}
}

func TestExplicitMissingTargetNeverFallsBack(t *testing.T) {
	opts, connector, _, stderr := testOptions(t)
	status := Execute(context.Background(), []string{"runs", "list", "--target", "missing", "--json"}, opts)
	if status != 2 || len(connector.opened) != 0 || !strings.Contains(stderr.String(), "missing") {
		t.Fatalf("%d %s", status, stderr)
	}
}

func TestRunPaginationPreservesServerQuery(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	var queries []core.RunQuery
	connector.backend = &fakeBackend{runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		queries = append(queries, q)
		if q.PageToken == "" {
			return core.RunPage{Runs: []core.Run{{Info: core.RunInfo{RunID: "one"}}}, NextPageToken: "next"}, nil
		}
		return core.RunPage{Runs: []core.Run{{Info: core.RunInfo{RunID: "two"}}}}, nil
	}}
	args := []string{"runs", "list", "7", "8", "--json", "--all", "--limit", "1", "--view", "all", "--filter", "metrics.loss < 1", "--order-by", "metrics.loss ASC"}
	if status := Execute(context.Background(), args, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var page core.RunPage
	if err := json.Unmarshal(out.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 2 || page.NextPageToken != "" {
		t.Fatalf("page %#v", page)
	}
	if len(queries) != 2 || queries[1].PageToken != "next" {
		t.Fatalf("queries %#v", queries)
	}
	for _, q := range queries {
		if !reflect.DeepEqual(q.ExperimentIDs, []string{"7", "8"}) || q.Filter != "metrics.loss < 1" || q.ViewType != "ALL" || q.MaxResults != 1 || !reflect.DeepEqual(q.OrderBy, []string{"metrics.loss ASC"}) {
			t.Fatalf("query changed: %#v", q)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON diagnostics: %s", stderr)
	}
}

func TestRunListDiscoversAllExperimentPages(t *testing.T) {
	opts, connector, _, stderr := testOptions(t)
	pages := 0
	connector.backend = &fakeBackend{experiment: func(_ context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
		pages++
		if q.PageToken == "" {
			return core.ExperimentPage{Experiments: []core.Experiment{{ID: "a"}}, NextPageToken: "more"}, nil
		}
		return core.ExperimentPage{Experiments: []core.Experiment{{ID: "b"}}}, nil
	}, runs: func(_ context.Context, q core.RunQuery) (core.RunPage, error) {
		if !reflect.DeepEqual(q.ExperimentIDs, []string{"a", "b"}) {
			t.Fatalf("experiment IDs %#v", q.ExperimentIDs)
		}
		return core.RunPage{}, nil
	}}
	if status := Execute(context.Background(), []string{"runs", "list", "--json"}, opts); status != 0 || pages != 2 {
		t.Fatalf("status=%d pages=%d %s", status, pages, stderr)
	}
}

func TestRepeatedPageTokenFailsWithoutPartialJSON(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	connector.backend = &fakeBackend{runs: func(context.Context, core.RunQuery) (core.RunPage, error) {
		return core.RunPage{NextPageToken: "repeat"}, nil
	}}
	if status := Execute(context.Background(), []string{"runs", "list", "1", "--all", "--json"}, opts); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "repeated") {
		t.Fatalf("status=%d out=%s err=%s", status, out, stderr)
	}
}

func TestCancellationClosesSession(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	connector.backend = &fakeBackend{runs: func(context.Context, core.RunQuery) (core.RunPage, error) { return core.RunPage{}, context.Canceled }}
	if status := Execute(context.Background(), []string{"runs", "list", "1", "--json"}, opts); status != 130 || out.Len() != 0 || !strings.Contains(stderr.String(), "canceled") {
		t.Fatalf("status=%d out=%s err=%s", status, out, stderr)
	}
	if connector.closed != 1 || connector.sessionClosed != 1 {
		t.Fatal("canceled operation leaked connection")
	}
}

func TestComparisonPreservesMissingAndPrecision(t *testing.T) {
	runs := []core.Run{{Info: core.RunInfo{RunID: "a"}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: 1.00000001}, {Key: "optional", Value: 0}, {Key: "infinite", Value: core.Number(math.Inf(1))}}}}, {Info: core.RunInfo{RunID: "b"}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: 1.00000002}, {Key: "infinite", Value: core.Number(math.Inf(1))}}}}}
	result := comparison(runs, true)
	if len(result.Rows) != 2 {
		t.Fatalf("different rows %#v", result.Rows)
	}
	for _, row := range result.Rows {
		if row.Field == "metric:optional" && (row.Values[0] == nil || *row.Values[0] != "0" || row.Values[1] != nil) {
			t.Fatalf("missing treated as zero: %#v", row)
		}
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("nonfinite metric broke JSON: %v", err)
	}
}

func TestArtifactDestinationIsExact(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	connector.backend = &fakeBackend{download: func(_ context.Context, r core.DownloadRequest) (core.DownloadResult, error) {
		if r.Destination != "./model-copy" || r.Path != "model" || !r.Overwrite {
			t.Fatalf("request %#v", r)
		}
		return core.DownloadResult{Path: r.Destination, Files: 2}, nil
	}}
	if status := Execute(context.Background(), []string{"artifacts", "download", "run", "model", "--dest", "./model-copy", "--overwrite", "--json"}, opts); status != 0 || !strings.Contains(out.String(), "model-copy") {
		t.Fatalf("status=%d %s", status, stderr)
	}
}

func TestOpenPrintDoesNotLaunchBrowser(t *testing.T) {
	opts, _, out, stderr := testOptions(t)
	opts.OpenURL = func(context.Context, string) error { t.Fatal("print opened browser"); return nil }
	if status := Execute(context.Background(), []string{"open", "run-id", "--target", "second", "--print", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	if !strings.Contains(out.String(), "https://second/prefix#/experiments/2/runs/run-id") {
		t.Fatalf("wrong URL: %s", out)
	}
}

func TestLocalOpenPrintDoesNotPrepareRuntime(t *testing.T) {
	opts, connector, _, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"open", "--tracking-uri", "sqlite:///some.db", "--print", "--json"}, opts); status != 2 || len(connector.opened) != 0 {
		t.Fatalf("status=%d %s", status, stderr)
	}
}

func TestDashboardNormalizesAndDoesNotPersistTransient(t *testing.T) {
	opts, _, _, stderr := testOptions(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("default_target = 'temporary'\n[[targets]]\nid = 'temporary'\ntracking_uri = './mlruns'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.LoadConfig = config.Load
	opts.IsTerminal = func() bool { return true }
	opts.Dashboard = func(_ context.Context, o tui.Options) error {
		if o.InitialTarget != "temporary-2" || len(o.Targets) != 2 {
			t.Fatalf("dashboard options %#v", o)
		}
		if o.Targets[0].TrackingURI != (&url.URL{Scheme: "file", Path: fileuri.Path(filepath.Join(dir, "mlruns"))}).String() {
			t.Fatalf("relative path not resolved: %#v", o.Targets[0])
		}
		return o.SaveTargets(o.Targets, o.InitialTarget)
	}
	if status := Execute(context.Background(), []string{"--config", path, "--tracking-uri", "https://remote"}, opts); status != 0 {
		t.Fatalf("status=%d %s", status, stderr)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 1 || cfg.DefaultTarget != "temporary" {
		t.Fatalf("transient persisted or default lost: %#v", cfg)
	}
}

func TestTargetEditsPreserveOmittedAndClearExplicit(t *testing.T) {
	opts, _, out, stderr := testOptions(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	opts.LoadConfig = config.Load
	if status := Execute(context.Background(), []string{"targets", "add", "local", "--uri", "./mlruns", "--name", "Local", "--token-env", "MLFLOW_TOKEN", "--default", "--json"}, opts); status != 0 {
		t.Fatalf("add=%d %s", status, stderr)
	}
	out.Reset()
	stderr.Reset()
	if status := Execute(context.Background(), []string{"targets", "edit", "local", "--name=", "--json"}, opts); status != 0 {
		t.Fatalf("edit=%d %s", status, stderr)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "" || cfg.Targets[0].TokenEnv != "MLFLOW_TOKEN" || cfg.DefaultTarget != "local" {
		t.Fatalf("wrong edit %#v", cfg)
	}
}

func TestAddCanCreateExplicitConfigButReadsCannot(t *testing.T) {
	opts, _, out, stderr := testOptions(t)
	opts.LoadConfig = config.Load
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if status := Execute(context.Background(), []string{"--config", path, "targets", "list", "--json"}, opts); status != 2 {
		t.Fatalf("read status=%d %s", status, stderr)
	}
	out.Reset()
	stderr.Reset()
	if status := Execute(context.Background(), []string{"--config", path, "targets", "add", "test", "--uri", "https://example.invalid", "--json"}, opts); status != 0 {
		t.Fatalf("add status=%d %s", status, stderr)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestFormTypingAndCancelOwnKeys(t *testing.T) {
	m := newTargetForm(core.Target{}, false, "/tmp/config.toml")
	m.Init()
	for _, letter := range "jq/" {
		next, _ := m.Update(tea.KeyPressMsg{Code: letter, Text: string(letter)})
		m = next.(targetForm)
	}
	if !strings.Contains(m.child.View(80, 24), "jq/") || m.child.Cancelled || m.child.Done {
		t.Fatalf("typing triggered navigation: %#v", m)
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(targetForm)
	if !m.child.Cancelled {
		t.Fatal("escape did not cancel")
	}
}

func TestFormHostValidationRetainsDraft(t *testing.T) {
	m := newTargetForm(core.Target{ID: "duplicate", TrackingURI: "https://example.invalid"}, false, "/tmp/config.toml")
	m.child.Done = true
	m.validate = func(core.Target) error { return errors.New("ID already exists") }
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(targetForm)
	if m.child.Done || m.child.Cancelled || m.child.Target.ID != "duplicate" || m.child.Err == nil {
		t.Fatalf("draft lost on invalid submit: %#v", m.child)
	}
}

func TestRuntimeFailureCleanJSON(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	connector.backend = &fakeBackend{getRun: func(context.Context, string) (core.Run, error) { return core.Run{}, errors.New("server unavailable") }}
	if status := Execute(context.Background(), []string{"runs", "get", "run", "--json"}, opts); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "runtime_error") {
		t.Fatalf("status=%d out=%s err=%s", status, out, stderr)
	}
}
