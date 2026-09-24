package localstate

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func activityObservation(id, status string, value core.Number) core.ActivityObservation {
	return core.ActivityObservation{Run: core.Run{Info: core.RunInfo{RunID: id, ExperimentID: "e", Status: status, StartTime: 100, LifecycleStage: "active"}, Data: core.RunData{Metrics: []core.Metric{{Key: "loss", Value: value}}}}}
}

func activityBatch(at int64, observations ...core.ActivityObservation) core.ActivityBatch {
	return core.ActivityBatch{Observations: observations, StartedAt: at, ObservedAt: at, Checkpoint: at, Complete: true, InitialUnreadSince: 50, Experiments: []core.Experiment{{ID: "e", Name: "experiment"}}, ReplaceExperiments: true}
}

func TestActivityReadReceiptAndEpisodeAcknowledgementRaces(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	observation := activityObservation("run", "RUNNING", core.Number(math.NaN()))
	a, err := s.ObserveActivity(ctx, "source", activityBatch(200, observation))
	if err != nil {
		t.Fatal(err)
	}
	receipt := core.ActivityReceipt{RunID: "run", Revision: a.Records["run"].Revision}
	if err := s.AcknowledgeActivity(ctx, "source", []core.ActivityReceipt{receipt}, true); err != nil {
		t.Fatal(err)
	}
	observation.Run.Data.Metrics[0].Value = .5
	if _, err := s.ObserveActivity(ctx, "source", activityBatch(300, observation)); err != nil {
		t.Fatal(err)
	}
	observation.Run.Data.Metrics[0].Value = core.Number(math.Inf(1))
	if _, err := s.ObserveActivity(ctx, "source", activityBatch(400, observation)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkActivityRead(ctx, "source", []core.ActivityReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeActivity(ctx, "source", []core.ActivityReceipt{receipt}, true); err != nil {
		t.Fatal(err)
	}
	b, err := s.LoadActivity(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	current := b.Records["run"]
	if !current.Unread() || !current.HasAlerts() || current.Alerts[0].Episode <= receipt.Revision {
		t.Fatalf("old receipt consumed new event: %+v", current)
	}
	newReceipt := core.ActivityReceipt{RunID: "run", Revision: current.Revision}
	if err := s.AcknowledgeActivity(ctx, "source", []core.ActivityReceipt{newReceipt}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeActivity(ctx, "source", []core.ActivityReceipt{newReceipt}, false); err != nil {
		t.Fatal(err)
	}
	b, _ = s.LoadActivity(ctx, "source")
	if !b.Records["run"].HasAlerts() {
		t.Fatal("restore acknowledgement failed")
	}
	// A stale observation must not overwrite the new episode or run status.
	observation.Run.Info.Status = "FINISHED"
	observation.Run.Data.Metrics[0].Value = 1
	if _, err := s.ObserveActivity(ctx, "source", activityBatch(250, observation)); err != nil {
		t.Fatal(err)
	}
	b, _ = s.LoadActivity(ctx, "source")
	if b.Records["run"].Status != "RUNNING" || !b.Records["run"].HasAlerts() || b.Checkpoint != 400 {
		t.Fatal("late observation overwrote newer state")
	}
}

func TestActivityCacheRebuildAndRestartPreservePersonalState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s := New(path)
	observation := activityObservation("run", "RUNNING", .3)
	observation.Policy.Subscriptions = []core.MetricSubscription{{Key: "loss", Mode: "value"}}
	a, err := s.ObserveActivity(ctx, "source", activityBatch(200, observation))
	if err != nil {
		t.Fatal(err)
	}
	receipt := core.ActivityReceipt{RunID: "run", Revision: a.Records["run"].Revision}
	if err := s.MarkActivityRead(ctx, "source", []core.ActivityReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearActivityCache(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = New(path)
	defer s.Close()
	a, err = s.LoadActivity(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	if a.Records["run"].Unread() || a.Checkpoint != 200 || a.InitialUnreadSince != 50 || a.Counts["e"].Complete {
		t.Fatal("cache clear discarded receipts or checkpoint")
	}
	batch := activityBatch(300, observation)
	batch.CompleteExperiments = []string{"e"}
	batch.Observations[0].CacheOnly = true
	a, err = s.ObserveActivity(ctx, "source", batch)
	if err != nil {
		t.Fatal(err)
	}
	if a.Records["run"].Unread() || !a.Counts["e"].Complete || a.Counts["e"].Running != 1 {
		t.Fatal("rebuild created events or lost counts")
	}
	observation.Run.Data.Metrics[0].Value = .2
	a, err = s.ObserveActivity(ctx, "source", activityBatch(400, observation))
	if err != nil || !a.Records["run"].Unread() {
		t.Fatal("subscription baseline was lost on restart/cache rebuild", err)
	}
	other, err := s.LoadActivity(ctx, "other")
	if err != nil || len(other.Records) != 0 || other.Initialized {
		t.Fatal("source identities leaked")
	}
}

func TestActivityCountsReplaceOnlyCompletedExperimentAndProtectNewerRows(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	a := activityObservation("a", "RUNNING", .5)
	b := activityObservation("b", "FINISHED", .4)
	batch := activityBatch(200, a, b)
	batch.CompleteExperiments = []string{"e"}
	if _, err := s.ObserveActivity(ctx, "s", batch); err != nil {
		t.Fatal(err)
	}
	newer := activityObservation("new", "RUNNING", .1)
	if _, err := s.ObserveActivity(ctx, "s", activityBatch(400, newer)); err != nil {
		t.Fatal(err)
	}
	// This earlier full scan saw b removed but must not remove a run inserted
	// by a more recent fast scan while the full request was in flight.
	batch = activityBatch(300, a)
	batch.CompleteExperiments = []string{"e"}
	result, err := s.ObserveActivity(ctx, "s", batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts["e"].Total != 2 || result.Counts["e"].Running != 2 {
		t.Fatalf("full replacement damaged newer rows: %+v", result.Counts["e"])
	}
	if _, ok := result.Records["b"]; !ok {
		t.Fatal("index pruning deleted personal inbox state")
	}
	if result.Records["b"].LifecycleStage != "deleted" || result.Records["new"].LifecycleStage == "deleted" {
		t.Fatal("full reconciliation failed to tombstone an absent older row or deleted a newer observation")
	}
	failed := core.ActivityBatch{StartedAt: 500, ObservedAt: 500, Errors: []string{"offline"}}
	result, err = s.ObserveActivity(ctx, "s", failed)
	if err != nil || result.Checkpoint != 400 || result.Complete || result.Counts["e"].Total != 2 {
		t.Fatal("failed scan advanced checkpoint or erased counts")
	}
}

func TestActivitySnapshotVersionOrdersPersonalChanges(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	first, err := s.ObserveActivity(ctx, "source", activityBatch(200, activityObservation("run", "FAILED", 1)))
	if err != nil {
		t.Fatal(err)
	}
	receipts := []core.ActivityReceipt{{RunID: "run", Revision: first.Records["run"].Revision}}
	prior := first.Version
	if prior == 0 {
		t.Fatal("first observation has no source version")
	}
	for _, change := range []func() error{
		func() error { return s.MarkActivityRead(ctx, "source", receipts) },
		func() error { return s.AcknowledgeActivity(ctx, "source", receipts, true) },
		func() error { return s.AcknowledgeActivity(ctx, "source", receipts, false) },
		func() error { return s.MarkActivityUnread(ctx, "source", []string{"run"}) },
		func() error { return s.ClearActivityCache(ctx, "source") },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		current, err := s.LoadActivity(ctx, "source")
		if err != nil || current.Version <= prior || current.UpdatedAt != first.UpdatedAt {
			t.Fatalf("personal change not ordered independently of server observation: %+v %v", current, err)
		}
		prior = current.Version
	}
}

func TestActivityCachedPolicyUpdatePreservesObservationAndAcknowledgement(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "state.db"))
	defer s.Close()
	observation := activityObservation("run", "FAILED", core.Number(math.NaN()))
	first, err := s.ObserveActivity(ctx, "s", activityBatch(200, observation))
	if err != nil {
		t.Fatal(err)
	}
	receipts := []core.ActivityReceipt{{RunID: "run", Revision: first.Records["run"].Revision}}
	if err := s.AcknowledgeActivity(ctx, "s", receipts, true); err != nil {
		t.Fatal(err)
	}
	no := false
	policy := core.ActivityPolicy{Alerts: &core.AlertSettings{Failed: &no, NonFinite: &no}}
	updated, err := s.ObserveActivity(ctx, "s", core.ActivityBatch{ObservedAt: 300, StartedAt: 300, Complete: true, PolicyUpdates: map[string]core.ActivityPolicy{"e": policy}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Records["run"].HasAlerts() || updated.Records["run"].ObservedAt != 200 {
		t.Fatal("cached policy applied as new remote evidence")
	}
	updated, err = s.ObserveActivity(ctx, "s", core.ActivityBatch{ObservedAt: 400, StartedAt: 400, Complete: true, PolicyUpdates: map[string]core.ActivityPolicy{"e": {}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range updated.Records["run"].Alerts {
		if !a.Active || a.Suppressed || !a.Acknowledged || a.LastSeen != 200 {
			t.Fatalf("policy replay changed episode/evidence: %+v", a)
		}
	}
	// A full scan that began before the policy toggle can still reconcile the
	// old actual observation, since cached evaluation did not advance it.
	updated, err = s.ObserveActivity(ctx, "s", core.ActivityBatch{StartedAt: 250, ObservedAt: 250, Complete: true, CompleteExperiments: []string{"e"}})
	if err != nil || updated.Records["run"].LifecycleStage != "deleted" || len(core.ActivityRecords(updated, "alerts")) != 0 {
		t.Fatal("cached policy prevented full reconciliation", err)
	}
}

func TestActivityMissingStoreReadDoesNotCreateAndV2MigrationPreservesNotes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	s := New(path)
	if _, err := s.LoadActivity(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("read created state directory")
	}
	view := core.DefaultView([]string{"loss"}, nil)
	if err := s.SaveView(ctx, "s", "e", view); err != nil {
		t.Fatal(err)
	}
	note, err := s.SaveNote(ctx, core.Note{Subject: core.Subject{Source: "s", Kind: "run", ID: "r"}, Body: "preserve me"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TABLE activity_sources`, `DROP TABLE activity_records`, `DROP TABLE activity_run_cache`, `DROP TABLE activity_count_cache`, `PRAGMA user_version=2`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s = New(path)
	defer s.Close()
	if _, err := s.ObserveActivity(ctx, "s", activityBatch(200, activityObservation("r", "RUNNING", 1))); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.LoadView(ctx, "s", "e")
	if err != nil || !found || got.Columns[3].Key != "loss" {
		t.Fatal("migration lost view")
	}
	notes, err := s.ListNotes(ctx, note.Subject, false)
	if err != nil || len(notes) != 1 || notes[0].Body != "preserve me" {
		t.Fatal("migration lost notes")
	}
}
