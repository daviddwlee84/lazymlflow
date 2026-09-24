package tui

import (
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestActivityReceiptSnapshotPreservesNewerFetchedMetadata(t *testing.T) {
	m, a := activityReviewModel(t)
	record := core.ActivityRecord{RunID: "r1", RunName: "run", ExperimentID: "e1", Status: "RUNNING", ObservedAt: 100, Revision: 2, ReadRevision: 1, Metrics: []core.Metric{{Key: "loss", Value: 1}}}
	initial := activityReviewSnapshot(m, record)
	initial.Version = 1
	m.acceptActivitySnapshot(initial)
	fresh := record.Run()
	fresh.Info.Status = "FINISHED"
	fresh.Data.Metrics[0].Value = .5
	m.rememberActivityMetadata([]core.Run{fresh})
	a.Inspect = a.Runs["r1"]
	record.ReadRevision = 2
	receipt := activityReviewSnapshot(m, record)
	receipt.Version = 2
	m.acceptActivitySnapshot(receipt)
	if a.Runs["r1"].Info.Status != "FINISHED" || a.Inspect.Data.Metrics[0].Value != .5 {
		t.Fatal("personal receipt reverted newer remote metadata")
	}
}

func TestActivitySourceVersionsOrderAcknowledgementChanges(t *testing.T) {
	m, a := activityReviewModel(t)
	record := core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100, Alerts: []core.ActivityAlert{{ID: "failed", Episode: 2, Active: true, Acknowledged: true}}}
	latest := activityReviewSnapshot(m, record)
	latest.Version = 3
	m.acceptActivitySnapshot(latest)
	old := activityReviewSnapshot(m, record)
	old.Version = 2
	old.Records["r1"] = core.ActivityRecord{RunID: "r1", ExperimentID: "e1", Revision: 2, ReadRevision: 1, ObservedAt: 100}
	m.acceptActivitySnapshot(old)
	if a.Snapshot.Version != 3 || !a.Snapshot.Records["r1"].Alerts[0].Acknowledged {
		t.Fatal("late snapshot replaced acknowledged state")
	}
	record.Alerts = append([]core.ActivityAlert(nil), record.Alerts...)
	record.Alerts[0].Acknowledged = false
	restored := activityReviewSnapshot(m, record)
	restored.Version = 4
	m.acceptActivitySnapshot(restored)
	if !a.Snapshot.Records["r1"].HasAlerts() {
		t.Fatal("newer restore was hidden by an old receipt")
	}
}
