package config

import (
	"github.com/daviddwlee84/lazymlflow/internal/fileuri"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestDefaultPathAndMissingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "relative")
	want := filepath.Join(home, ".config", "lazymlflow", "config.toml")
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		want = filepath.Join(home, "AppData", "Roaming", "lazymlflow", "config.toml")
	}
	if DefaultPath() != want {
		t.Fatalf("default path %s", DefaultPath())
	}
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.SourcePath() != want {
		t.Fatal(c.SourcePath())
	}
	if _, err = os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
		t.Fatal("read created config directory")
	}
	if _, err = Load(want); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if DefaultPath() != filepath.Join(xdg, "lazymlflow", "config.toml") {
		t.Fatal(DefaultPath())
	}
}

func TestResolvePrecedence(t *testing.T) {
	c := &Config{DefaultTarget: "second", Targets: []core.Target{{ID: "first", TrackingURI: "https://first.test"}, {ID: "second", TrackingURI: "https://second.test"}}}
	t.Setenv("LAZYMLFLOW_TARGET", "")
	t.Setenv("MLFLOW_TRACKING_URI", "")
	check := func(id, uri, want string, transient bool) {
		t.Helper()
		got, err := Resolve(c, id, uri)
		if err != nil {
			t.Fatal(err)
		}
		if got.TrackingURI != want || got.Transient != transient {
			t.Fatalf("got %+v", got)
		}
	}
	check("", "", "https://second.test", false)
	t.Setenv("MLFLOW_TRACKING_URI", "https://mlflow-env.test")
	check("", "", "https://mlflow-env.test", true)
	t.Setenv("LAZYMLFLOW_TARGET", "first")
	check("", "", "https://first.test", false)
	check("second", "", "https://second.test", false)
	check("", "https://flag.test", "https://flag.test", true)
	if _, err := Resolve(c, "first", "https://both.test"); err == nil {
		t.Fatal("conflicting flags accepted")
	}
	t.Setenv("LAZYMLFLOW_TARGET", "missing")
	if _, err := Resolve(c, "", ""); err == nil {
		t.Fatal("unknown profile fell back")
	}
}

func TestNormalizeTarget(t *testing.T) {
	base := t.TempDir()
	for _, test := range []struct{ uri, want string }{{"mlruns", (&url.URL{Scheme: "file", Path: fileuri.Path(filepath.Join(base, "mlruns"))}).String()}, {"file:mlruns", (&url.URL{Scheme: "file", Path: fileuri.Path(filepath.Join(base, "mlruns"))}).String()}, {"sqlite:///mlflow.db", "sqlite:///" + filepath.ToSlash(filepath.Join(base, "mlflow.db"))}, {"https://host.test/prefix/", "https://host.test/prefix"}} {
		target, err := NormalizeTarget(core.Target{ID: "main", TrackingURI: test.uri}, base)
		if err != nil {
			t.Fatal(err)
		}
		if target.TrackingURI != test.want {
			t.Errorf("%s => %s (want %s)", test.uri, target.TrackingURI, test.want)
		}
	}
	for _, uri := range []string{"sqlite:///:memory:", "sqlite:///x?mode=rw", "file://other.test/store", "postgresql://localhost/db", "https://user:pass@host.test", "https://host.test?secret=abc"} {
		if _, err := NormalizeTarget(core.Target{ID: "main", TrackingURI: uri}, base); err == nil {
			t.Errorf("accepted %s", uri)
		}
	}
	target, err := NormalizeTarget(core.Target{ID: "main", TrackingURI: "mlruns", WorkingDir: "project", Python: ".venv", CAFile: "ca.pem", ArtifactsDestination: "artifacts"}, base)
	if err != nil {
		t.Fatal(err)
	}
	if target.WorkingDir != filepath.Join(base, "project") || target.Python != filepath.Join(base, "project", ".venv") || target.ArtifactsDestination != filepath.Join(base, "project", "artifacts") {
		t.Fatalf("paths %+v", target)
	}
}

func TestConfigSavePreservesUnknownAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `# Header stays
default_target = "local" # profile selection
future_option = "untouched"

[tui]
metric_columns = ["accuracy"] # visible columns
future_view = true

[[targets]] # local notes
id = "local"
name = "Before" # friendly name
tracking_uri = "https://local.test"
unknown = "target option"
[targets.env]
AWS_PROFILE = "OLD_AWS"

[future]
keep = ["a", "b"]

[[targets]]
id = "other"
tracking_uri = "https://other.test"
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.DefaultTarget = "other"
	c.Targets[0].Name = "After"
	c.Targets[0].TokenEnv = "MY_TOKEN"
	c.Targets[0].Env = map[string]string{"AWS_PROFILE": "NEW_AWS", "AWS_REGION": "MY_REGION"}
	c.TUI.MetricColumns = []string{"loss", "accuracy"}
	if err = c.Save(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# Header stays", "# profile selection", "future_option = \"untouched\"", "# visible columns", "future_view = true", "# local notes", "# friendly name", "unknown = \"target option\"", "[future]\nkeep = [\"a\", \"b\"]"} {
		if !strings.Contains(string(b), text) {
			t.Errorf("lost %q\n%s", text, b)
		}
	}
	reread, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reread.DefaultTarget != "other" || reread.Targets[0].Name != "After" || reread.Targets[0].Env["AWS_PROFILE"] != "NEW_AWS" {
		t.Fatalf("unexpected config %+v", reread)
	}
	if err = c.Save(path); err != nil {
		t.Fatal("repeat save:", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("permissions changed", err)
	}
}

func TestConfigSaveAddRemoveAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := New(path)
	c.Targets = []core.Target{{ID: "one", TrackingURI: "http://one.test"}, {ID: "two", TrackingURI: "http://two.test", Env: map[string]string{"AWS_PROFILE": "PROFILE"}}}
	c.DefaultTarget = "one"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	c.Targets = c.Targets[1:]
	c.DefaultTarget = "two"
	c.Targets = append(c.Targets, core.Target{ID: "three", TrackingURI: "http://three.test"})
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	reread, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Targets) != 2 || reread.Targets[0].ID != "two" {
		t.Fatalf("removal failed %+v", reread)
	}
	current, _ := os.ReadFile(path)
	if err = os.WriteFile(path, append(current, []byte("\n# edit from elsewhere\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	c.Targets[0].Name = "changed"
	if err = c.Save(path); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatal("failed to detect external edit", err)
	}
}

func TestConfigSavedInlineEnvAndMultiLineFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	text := `[tui]
metric_columns = [
  "old", # a comment inside a changed field
  "other",
]
[[targets]]
id = "one"
tracking_uri = "http://one.test"
name = """multi
line""" # name comment
env = { AWS_PROFILE = "OLD" } # environment comment
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.TUI.MetricColumns = []string{"new"}
	c.Targets[0].Name = "single"
	c.Targets[0].Env = map[string]string{"AWS_PROFILE": "NEW"}
	if err = c.Save(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, comment := range []string{"# name comment", "# environment comment"} {
		if !strings.Contains(string(b), comment) {
			t.Fatal("lost comment", string(b))
		}
	}
}

func TestConfigValidationAndRedaction(t *testing.T) {
	c := &Config{DefaultTarget: "missing", Targets: []core.Target{{ID: "one", TrackingURI: "https://one.test"}}}
	if c.Validate() == nil {
		t.Fatal("missing default accepted")
	}
	c.DefaultTarget = "one"
	c.Targets = append(c.Targets, c.Targets[0])
	if c.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
	u := RedactURI("https://user:pass@host.test/path?token=secret&region=x")
	if strings.Contains(u, "pass") || strings.Contains(u, "secret") || !strings.Contains(u, "region=x") {
		t.Fatal(u)
	}
}

func TestRemoveFinalTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := New(path)
	c.Targets = []core.Target{{ID: "only", TrackingURI: "http://example.test"}}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	c.Targets = c.Targets[:0]
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	read, err := Load(path)
	if err != nil || len(read.Targets) != 0 {
		t.Fatal(read, err)
	}
}

func TestSavePreservesConfigSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(real, []byte("[[targets]]\nid = 'main'\ntracking_uri = 'https://example.test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	c, err := Load(link)
	if err != nil {
		t.Fatal(err)
	}
	c.Targets[0].Name = "Updated"
	if err = c.Save(link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration symlink replaced", err)
	}
	if c.SourcePath() != link {
		t.Fatal("selected config path changed")
	}
	reread, err := Load(real)
	if err != nil || reread.Targets[0].Name != "Updated" {
		t.Fatal(reread, err)
	}
}

func TestSSHTargetValidationAndCommentPreservingSave(t *testing.T) {
	for _, tc := range []struct {
		host, uri string
		valid     bool
	}{
		{"remote-lab", "http://127.0.0.1:8000", true},
		{"user@remote-lab", "https://mlflow.internal/tracking", true},
		{"[::1]", "http://127.0.0.1:8000", true},
		{"-Fbad", "http://localhost:8000", false},
		{"host -p 22", "http://localhost:8000", false},
		{"host;command", "http://localhost:8000", false},
		{"remote-lab", "./mlruns", false},
		{"remote-lab", "sqlite:///mlflow.db", false},
	} {
		_, err := NormalizeTarget(core.Target{ID: "ssh", SSHHost: tc.host, TrackingURI: tc.uri}, "")
		if (err == nil) != tc.valid {
			t.Errorf("%q %q: err=%v", tc.host, tc.uri, err)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "# personal settings\n[[targets]]\nid = 'lab'\ntracking_uri = 'http://127.0.0.1:8000' # reached remotely\nssh_host = 'old-alias' # existing SSH policy\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Targets[0].SSHHost = "new-alias"
	if err = c.Save(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# personal settings", "# reached remotely", "# existing SSH policy", "new-alias"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("lost %q: %s", want, b)
		}
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.Targets[0].SSHHost != "new-alias" {
		t.Fatal("SSH not persisted", err)
	}
}
