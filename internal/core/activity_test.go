package core

import (
	"math"
	"testing"
)

func activityRun(status string, metrics ...Metric) Run {
	return Run{Info: RunInfo{RunID: "run", ExperimentID: "experiment", Status: status, StartTime: 100, LifecycleStage: "active"}, Data: RunData{Metrics: metrics}}
}

func observeActivity(previous ActivityRecord, run Run, policy ActivityPolicy, now int64) ActivityRecord {
	return ObserveActivityRecord(previous, ActivityObservation{Run: run, Policy: policy}, now, 200, 0, previous.RunID != "")
}

func TestActivityInitialWindowRunningAndBaseline(t *testing.T) {
	old := observeActivity(ActivityRecord{}, activityRun("FINISHED"), ActivityPolicy{}, 300)
	if old.Unread() {
		t.Fatal("old ended run became unread")
	}
	active := observeActivity(ActivityRecord{}, activityRun("RUNNING"), ActivityPolicy{}, 300)
	if !active.Unread() {
		t.Fatal("old running run missing from initial unread")
	}
	run := activityRun("FINISHED")
	run.Info.EndTime = 250
	recent := observeActivity(ActivityRecord{}, run, ActivityPolicy{}, 300)
	if !recent.Unread() {
		t.Fatal("recent completion of old run missing from initial unread")
	}
}

func TestActivitySubscriptionsIgnoreOrderAndValueOnlySamples(t *testing.T) {
	policy := ActivityPolicy{Subscriptions: []MetricSubscription{{Key: "best/corr", Mode: "value"}, {Key: "loss", Mode: "sample"}, {Key: "later", Mode: "value"}}}
	run := activityRun("RUNNING", Metric{Key: "best/corr", Value: .8, Step: 1}, Metric{Key: "loss", Value: .2, Step: 1})
	a := observeActivity(ActivityRecord{}, run, policy, 300)
	a.ReadRevision = a.Revision
	run.Data.Metrics = []Metric{{Key: "loss", Value: .2, Step: 1}, {Key: "best/corr", Value: .8, Step: 2, Timestamp: 500}}
	b := observeActivity(a, run, policy, 500)
	if b.Unread() {
		t.Fatalf("reordered/unchanged subscribed values created event: %+v", b.Reasons)
	}
	run.Data.Metrics[0].Step = 2
	c := observeActivity(b, run, policy, 600)
	if !c.Unread() || !containsExact(c.Reasons, "metric:loss") {
		t.Fatalf("new sample did not notify: %+v", c)
	}
	c.ReadRevision = c.Revision
	run.Data.Metrics = append(run.Data.Metrics, Metric{Key: "later", Value: 1})
	d := observeActivity(c, run, policy, 700)
	if !d.Unread() || !containsExact(d.Reasons, "metric:later") {
		t.Fatal("first appearance after missing baseline did not notify")
	}
}

func TestActivityDefaultMetricUpdatesSkipSystemAndNaNIsStable(t *testing.T) {
	yes := true
	policy := ActivityPolicy{MetricUpdates: &yes}
	run := activityRun("RUNNING", Metric{Key: "loss", Value: Number(math.NaN()), Step: 1}, Metric{Key: "system/cpu", Value: 10})
	a := observeActivity(ActivityRecord{}, run, policy, 300)
	a.ReadRevision = a.Revision
	run.Data.Metrics[1].Value = 20
	b := observeActivity(a, run, policy, 400)
	if b.Unread() || b.Revision != a.Revision {
		t.Fatal("system-only update or stable NaN created unread")
	}
	run.Data.Metrics[0].Step++
	c := observeActivity(b, run, policy, 500)
	if !c.Unread() || !containsExact(c.Reasons, "metrics") {
		t.Fatal("new model metric sample was not detected")
	}
}

func TestActivityAlertEpisodesSurviveMissingDisabledAndRecur(t *testing.T) {
	run := activityRun("RUNNING", Metric{Key: "corr", Value: Number(math.NaN())})
	a := observeActivity(ActivityRecord{}, run, ActivityPolicy{}, 300)
	a.Alerts[0].Acknowledged = true
	a.ReadRevision = a.Revision
	run.Data.Metrics = nil
	b := observeActivity(a, run, ActivityPolicy{}, 400)
	if !b.Alerts[0].Active || !b.Alerts[0].Acknowledged {
		t.Fatal("missing metric falsely recovered the alert")
	}
	no := false
	run.Data.Metrics = []Metric{{Key: "corr", Value: Number(math.NaN())}}
	c := observeActivity(b, run, ActivityPolicy{Alerts: &AlertSettings{NonFinite: &no}}, 500)
	if !c.Alerts[0].Active || !c.Alerts[0].Suppressed || c.HasAlerts() {
		t.Fatal("disabled rule did not preserve and suppress episode")
	}
	d := observeActivity(c, run, ActivityPolicy{}, 600)
	if !d.Alerts[0].Acknowledged || d.Alerts[0].Suppressed || d.Unread() {
		t.Fatal("reenabling unchanged episode created an event")
	}
	run.Data.Metrics[0].Value = .5
	e := observeActivity(d, run, ActivityPolicy{}, 700)
	if e.Alerts[0].Active {
		t.Fatal("finite observation failed to recover")
	}
	run.Data.Metrics[0].Value = Number(math.Inf(1))
	f := observeActivity(e, run, ActivityPolicy{}, 800)
	if !f.HasAlerts() || !f.Unread() || f.Alerts[0].Episode <= a.Alerts[0].Episode {
		t.Fatal("recurrence reused acknowledgement")
	}
}

func TestActivityFailedNewEndTimeIsNewEpisode(t *testing.T) {
	run := activityRun("FAILED")
	run.Info.EndTime = 250
	a := observeActivity(ActivityRecord{}, run, ActivityPolicy{}, 300)
	a.Alerts[0].Acknowledged = true
	a.ReadRevision = a.Revision
	run.Info.EndTime = 450
	b := observeActivity(a, run, ActivityPolicy{}, 500)
	if !b.HasAlerts() || b.Alerts[0].Episode <= a.Alerts[0].Episode {
		t.Fatal("new failed completion inherited old acknowledgement")
	}
}

func TestActivityViewsExcludeDeletedRunsAndInactiveExperiments(t *testing.T) {
	s := NewActivitySnapshot("source")
	s.Initialized = true
	s.Experiments = []Experiment{{ID: "active"}}
	for _, r := range []ActivityRecord{
		{RunID: "active", ExperimentID: "active", Status: "FAILED", Revision: 2, EndTime: 100, Alerts: []ActivityAlert{{Active: true}}},
		{RunID: "deleted", ExperimentID: "active", Status: "FAILED", LifecycleStage: "deleted", Revision: 2, EndTime: 100, Alerts: []ActivityAlert{{Active: true}}},
		{RunID: "inactive", ExperimentID: "other", Status: "FAILED", Revision: 2, EndTime: 100, Alerts: []ActivityAlert{{Active: true}}},
	} {
		s.Records[r.RunID] = r
	}
	for _, view := range []string{"all", "unread", "recent", "alerts"} {
		rows := ActivityRecords(s, view)
		if len(rows) != 1 || rows[0].RunID != "active" {
			t.Fatalf("%s included inactive/deleted rows: %+v", view, rows)
		}
	}
	if len(s.Records) != 3 {
		t.Fatal("filtering erased durable receipts")
	}
}
