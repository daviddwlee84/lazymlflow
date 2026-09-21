package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/models"
	"github.com/daviddwlee84/lazymlflow/internal/serverform"
)

type modelBackendFixture struct{ fakeBackend }

func (*modelBackendFixture) SearchRegisteredModels(context.Context, models.Query) (models.RegisteredPage, error) {
	return models.RegisteredPage{Models: []models.RegisteredModel{{Name: "example"}}}, nil
}

func TestModelEmptyWheelAndBackInvalidatePendingInspection(t *testing.T) {
	m, _ := readyModel()
	m.state().Session.Backend = &modelBackendFixture{}
	cmd := m.openModels("registry", "")
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m.Update(cmd())
	if m.extensions.index != 0 {
		t.Fatal("empty list wheel left an invalid index")
	}
	cmd = key(m, "enter")
	m.Update(cmd())
	cmd = key(m, "enter")
	late := cmd()
	key(m, "b")
	m.Update(late)
	if m.extensions.mode != "models" || m.extensions.pending || m.extensions.inspection != nil {
		t.Fatal("late inspection survived Back")
	}
	key(m, "e")
	if m.extensions.inputKind != "" {
		t.Fatal("hidden inspection could be exported")
	}
}

func TestSetupFailurePreservesDraft(t *testing.T) {
	m, _ := readyModel()
	m.openServerSetup()
	draft := m.extensions.form
	r := serverform.Result{Directory: t.TempDir(), Spec: draft.Result.Spec}
	cmd := m.applyServerSetup(r)
	m.Update(cmd())
	if m.extensions.form != draft || m.extensions.form.Done || m.extensions.form.Err == nil {
		t.Fatal("failed generation lost the editable draft")
	}
}
func (*modelBackendFixture) SearchModelVersions(context.Context, models.Query) (models.VersionPage, error) {
	return models.VersionPage{Versions: []models.ModelVersion{{Name: "example", Version: "1", Status: "READY"}}}, nil
}
func (*modelBackendFixture) GetModelVersion(context.Context, string, string) (models.ModelVersion, error) {
	return models.ModelVersion{Name: "example", Version: "1", Status: "READY"}, nil
}
func (*modelBackendFixture) GetModelVersionByAlias(context.Context, string, string) (models.ModelVersion, error) {
	return models.ModelVersion{}, errors.New("unused")
}
func (*modelBackendFixture) GetModelVersionDownloadURI(context.Context, string, string) (string, error) {
	return "mlflow-artifacts:/model", nil
}
func (*modelBackendFixture) GetLoggedModel(context.Context, string) (models.LoggedModel, error) {
	return models.LoggedModel{}, errors.New("unused")
}
func (*modelBackendFixture) ListModelArtifacts(context.Context, models.ArtifactLocation) (core.ArtifactPage, error) {
	return core.ArtifactPage{Files: []core.Artifact{}}, nil
}
func (*modelBackendFixture) DownloadModelArtifacts(context.Context, models.ArtifactRequest, func(core.Progress)) (core.DownloadResult, error) {
	return core.DownloadResult{}, errors.New("unused")
}
func (*modelBackendFixture) ReadModelArtifact(context.Context, models.ArtifactLocation, int64) ([]byte, error) {
	return nil, errors.New("unused")
}

func TestSetupCancelKeepsTargetAndSelection(t *testing.T) {
	m, _ := readyModel()
	m.overlay = "targets"
	selected, active := m.state().Selected, m.active
	m.openServerSetup()
	key(m, "q")
	if m.extensions == nil || m.extensions.form == nil || m.extensions.form.Cancelled {
		t.Fatal("q in stack name navigated")
	}
	key(m, "esc")
	if m.extensions != nil || m.overlay != "targets" || m.active != active || m.state().Selected != selected {
		t.Fatal("cancel changed previous target context")
	}
}
func TestEnvironmentTextIsSecretFreeAndSSHHasLifetimeHint(t *testing.T) {
	m, _ := readyModel()
	t.Setenv("TEST_ENV_PASSWORD", "never-print-this")
	m.openTargetEnvironment(core.Target{ID: "example", TrackingURI: "https://example.test", PasswordEnv: "TEST_ENV_PASSWORD", UsernameEnv: "TEST_ENV_USER"})
	if strings.Contains(m.extensions.body, "never-print-this") || !strings.Contains(m.extensions.body, "TEST_ENV_PASSWORD") {
		t.Fatal(m.extensions.body)
	}
	m.closeExtension()
	m.openTargetEnvironment(core.Target{ID: "ssh", TrackingURI: "http://localhost:8000", SSHHost: "lab"})
	if !strings.Contains(m.extensions.body, "targets exec 'ssh'") {
		t.Fatal(m.extensions.body)
	}
}
func TestModelNavigationAndLateResultIsolation(t *testing.T) {
	m, _ := readyModel()
	m.state().Session.Backend = &modelBackendFixture{}
	cmd := m.openModels("registry", "")
	m.Update(cmd())
	if len(m.extensions.rows) != 1 {
		t.Fatal(m.extensions)
	}
	cmd = key(m, "enter")
	m.Update(cmd())
	if m.extensions.kind != "versions" || len(m.extensions.rows) != 1 {
		t.Fatal(m.extensions)
	}
	cmd = key(m, "enter")
	msg := cmd()
	m.Update(msg)
	if m.extensions.inspection == nil || m.extensions.inspection.Resolution.Version != "1" {
		t.Fatal(m.extensions)
	}
	key(m, "e")
	key(m, "q")
	if m.extensions == nil || !strings.Contains(m.extensions.input.Value(), "q") {
		t.Fatal("export input q closed view")
	}
	key(m, "esc")
	key(m, "esc")
	m.openTargetEnvironment(m.target())
	m.Update(msg)
	if m.extensions.mode != "text" || m.extensions.inspection != nil {
		t.Fatal("late result replaced newer view")
	}
}
func TestExtensionsConsumeMouseAndKeepNarrowResize(t *testing.T) {
	m, _ := readyModel()
	m.openTargetEnvironment(m.target())
	before := m.state().Selected
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 8, Y: 4})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 8, Y: 4})
	if before != m.state().Selected {
		t.Fatal("mouse clicked through environment view")
	}
	for _, size := range [][2]int{{80, 24}, {38, 12}, {12, 4}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if m.View().Content == "" {
			t.Fatal("blank view")
		}
	}
}

func TestModelMouseRowsUseVisibleFrameCoordinates(t *testing.T) {
	m, _ := readyModel()
	e := m.newExtension("models")
	e.rows = []modelChoice{{label: "first"}, {label: "second"}}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 2})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 4, Y: 2})
	if e.index != 1 {
		t.Fatal("second visible row did not select second model")
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 1})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 4, Y: 1})
	if e.index != 0 {
		t.Fatal("first row was unreachable")
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: 2})
	m.Update(tea.WindowSizeMsg{Width: 38, Height: 12})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 4, Y: 2})
	if e.index != 0 {
		t.Fatal("resize did not cancel pending click")
	}
}

func TestModelFailedRefreshRetainsRowsWithoutMixingContexts(t *testing.T) {
	m, _ := readyModel()
	m.state().Session.Backend = &modelBackendFixture{}
	cmd := m.openModels("registry", "")
	m.Update(cmd())
	m.loadModelRows(false)
	e := m.extensions
	m.Update(modelRowsMsg{gen: e.gen, target: m.active, kind: "registry", err: errors.New("offline")})
	if len(e.rows) != 1 || e.err != "offline" {
		t.Fatal("failed refresh discarded useful rows")
	}
	e.kind = "versions"
	e.name = "example"
	m.loadModelRows(false)
	if len(e.rows) != 0 {
		t.Fatal("registry rows leaked into version view")
	}
}
