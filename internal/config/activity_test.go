package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestActivityDefaultsAndExplicitDisabledValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, tc := range []struct {
		text          string
		refresh, days int
		readOn        string
	}{
		{"# no activity settings\n", 30, 7, "open"},
		{"[activity]\nrefresh_seconds = 0\ninitial_unread_days = 0\nread_on = 'select'\n", 0, 0, "select"},
	} {
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Activity.RefreshSeconds != tc.refresh || cfg.Activity.InitialUnreadDays != tc.days || cfg.Activity.ReadOn != tc.readOn {
			t.Fatalf("defaults or explicit zero lost: %+v", cfg.Activity)
		}
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != tc.text {
			t.Fatalf("unchanged save rewrote settings:\n%s", body)
		}
	}
}

func TestActivitySavePreservesCommentsUnknownAndExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	before := `# settings header
[activity]
refresh_seconds = 30 # polling
read_on = 'open' # read behavior
future_activity = 'keep me'
[alerts]
failed = true # failures
include_system = true
future_alert = ['keep']
[future]
unchanged = 123
`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f := false
	cfg.Activity.RefreshSeconds = 0
	cfg.Activity.ReadOn = "select"
	cfg.Activity.MetricUpdates = true
	cfg.Alerts.Failed, cfg.Alerts.IncludeSystem = &f, &f
	cfg.Alerts.ExcludeMetrics = []string{"debug/optional"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	for _, expected := range []string{"# settings header", "# polling", "# read behavior", "# failures", "future_activity = 'keep me'", "future_alert = ['keep']", "[future]\nunchanged = 123"} {
		if !strings.Contains(string(body), expected) {
			t.Fatalf("lost %q:\n%s", expected, body)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Activity.RefreshSeconds != 0 || loaded.Activity.ReadOn != "select" || !loaded.Activity.MetricUpdates || loaded.Alerts.Failed == nil || *loaded.Alerts.Failed || loaded.Alerts.IncludeSystem == nil || *loaded.Alerts.IncludeSystem {
		t.Fatalf("settings did not round-trip: %+v %+v", loaded.Activity, loaded.Alerts)
	}
	// Removing an explicit boolean restores inheritance without dropping its comment.
	cfg.Alerts.Failed = nil
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.Alerts.Failed != nil {
		t.Fatalf("inheritance reset failed: %+v %v", loaded, err)
	}
	body, _ = os.ReadFile(path)
	if !strings.Contains(string(body), "# failures") {
		t.Fatal("reset lost comment")
	}
}

func TestActivitySaveAddsSectionsWithoutRewritingOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	before := "[tui]\nmetric_columns = ['corr'] # user's choice\n[future]\nvalue = 9\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Activity.InitialUnreadDays = 3
	f := false
	cfg.Alerts.NonFinite = &f
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), before) {
		t.Fatalf("unrelated settings changed:\n%s", body)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Activity.InitialUnreadDays != 3 || loaded.Activity.RefreshSeconds != 30 || loaded.Alerts.NonFinite == nil || *loaded.Alerts.NonFinite {
		t.Fatalf("added settings lost: %+v %v", loaded, err)
	}
}

func TestInvalidActivityConfiguration(t *testing.T) {
	for _, text := range []string{
		"[activity]\nrefresh_seconds=-1\n", "[activity]\ninitial_unread_days=-1\n", "[activity]\nread_on='hover'\n", "[alerts]\ninclude_metrics=['']\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
	cfg := New(filepath.Join(t.TempDir(), "config.toml"))
	if cfg.Activity != core.DefaultActivitySettings() {
		t.Fatal("new config lacks defaults")
	}
}
