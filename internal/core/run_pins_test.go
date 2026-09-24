package core

import "testing"

func TestRunPinHelpersPreserveIdentityWithoutNotificationState(t *testing.T) {
	pin := RunPin{RunID: "run", ExperimentID: "e", RunName: "Name", ExperimentName: "Experiment", Status: "FAILED", StartTime: 10, EndTime: 20, LifecycleStage: "deleted", PinnedAt: 30, ObservedAt: 40}
	run, record := pin.Run(), pin.Record()
	if run.ID() != pin.RunID || run.Name() != pin.RunName || run.Info.ExperimentID != pin.ExperimentID || run.Info.EndTime != 20 || run.Info.LifecycleStage != "deleted" {
		t.Fatalf("run identity lost: %+v", run)
	}
	if record.RunID != pin.RunID || record.ExperimentName != pin.ExperimentName || record.ObservedAt != 40 || record.Unread() || record.HasAlerts() {
		t.Fatalf("bookmark invented activity state: %+v", record)
	}
	if len(run.Data.Params)+len(run.Data.Tags)+len(run.Data.Metrics) != 0 || run.Info.ArtifactURI != "" {
		t.Fatal("bookmark contained full run data")
	}
}
