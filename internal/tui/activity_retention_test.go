package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func runningRetentionModel(t *testing.T, count int) (*model, *activityState, []core.ActivityRecord) {
	t.Helper()
	m, a := activityReviewModel(t)
	a.Scope = scopeRunning
	records := []core.ActivityRecord{{RunID: "reading", RunName: "reading", ExperimentID: "e1", Status: "RUNNING", StartTime: 2000, ObservedAt: 10, Revision: 2, ReadRevision: 1}}
	if count > 1 {
		records = append(records, core.ActivityRecord{RunID: "other", RunName: "other", ExperimentID: "e2", Status: "RUNNING", StartTime: 1000, ObservedAt: 10, Revision: 2, ReadRevision: 1})
	}
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = 1
	m.acceptActivitySnapshot(snapshot)
	if m.run() == nil || m.run().ID() != "reading" {
		t.Fatal("incorrect initial selection")
	}
	return m, a, records
}
func finishRetainedRun(m *model, records []core.ActivityRecord, version uint64) {
	records[0].Status = "FINISHED"
	records[0].EndTime = 3000
	records[0].ObservedAt = int64(version) * 10
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = version
	m.acceptActivitySnapshot(snapshot)
}

func TestRunningCompletionKeepsVisibleSelectionInEveryPane(t *testing.T) {
	for _, focus := range []int{0, 1, 2} {
		t.Run(string(rune('0'+focus)), func(t *testing.T) {
			m, a, records := runningRetentionModel(t, 2)
			m.focus = focus
			finishRetainedRun(m, records, 2)
			if a.Scope != scopeRunning || a.RetainedRun != "reading" || m.run() == nil || m.run().ID() != "reading" || m.run().Info.Status != "FINISHED" {
				t.Fatalf("reading was interrupted: %+v", m.run())
			}
			if len(m.runRows()) != 2 || a.ScopeCounts[scopeRunning] != 1 {
				t.Fatal("retained row inflated running count or disappeared")
			}
			if !strings.Contains(m.runRows()[0].Note, "kept while reading") {
				t.Fatal("retained row lacks explanation")
			}
			for _, size := range [][2]int{{80, 24}, {160, 45}} {
				m.width, m.height = size[0], size[1]
				m.View()
			}
			m.focus = 2
			m.tab = 0
			m.refresh()
			if a.RetainedRun != "reading" {
				t.Fatal("detail refresh removed retained selection")
			}
			finishRetainedRun(m, records, 3)
			if len(m.runRows()) != 2 {
				t.Fatal("repeated poll duplicated retained row")
			}
		})
	}
}

func TestRunningRetentionReleasesOnNavigationAndSearch(t *testing.T) {
	m, a, records := runningRetentionModel(t, 2)
	finishRetainedRun(m, records, 2)
	m.selectRun(1)
	if a.RetainedRun != "" || m.run() == nil || m.run().ID() != "other" || m.runs().Index != 0 || len(m.runRows()) != 1 {
		t.Fatal("navigation did not retire old row and preserve next identity")
	}
	finishRetainedRun(m, records, 3)
	if len(m.runRows()) != 1 {
		t.Fatal("later poll resurrected an old retained row")
	}
	m, a, records = runningRetentionModel(t, 1)
	m.runs().Local = "RUNNING"
	finishRetainedRun(m, records, 2)
	if len(m.runRows()) != 1 {
		t.Fatal("an unchanged query interrupted a status transition")
	}
	m.setLocal("reading")
	if a.RetainedRun != "" || m.run() != nil || len(m.runRows()) != 0 || a.Scope != scopeRunning {
		t.Fatal("changing search did not clear last retained row")
	}
}

func TestRunningRetentionSurvivesSortingButNotScopeChange(t *testing.T) {
	m, a, records := runningRetentionModel(t, 2)
	finishRetainedRun(m, records, 2)
	m.runs().View.Sort = []core.SortSpec{{Column: core.ColumnSpec{Kind: "attribute", Key: "name"}}}
	m.rebuildActivityViews()
	if m.run() == nil || m.run().ID() != "reading" || m.runs().Index != 1 {
		t.Fatal("sorting lost retained identity")
	}
	m.openActivity(scopeRecent, true)
	if a.RetainedRun != "" {
		t.Fatal("scope change retained old row")
	}
	m.openActivity(scopeRunning, true)
	if m.run() == nil || m.run().ID() != "other" || len(m.runRows()) != 1 {
		t.Fatal("returning to Running resurrected old selection")
	}
}

func TestManualRunningRefreshSupersedesAutoAndOnlySuccessClears(t *testing.T) {
	m, a, records := runningRetentionModel(t, 1)
	finishRetainedRun(m, records, 2)
	ctx, gen := m.operation("activity:fast")
	a.Gen = gen
	a.Pending = true
	cmd := m.refreshActivityManually()
	if cmd == nil || ctx.Err() == nil || a.ManualRefreshGen != a.Gen {
		t.Fatal("manual refresh was ignored instead of replacing automatic request")
	}
	manual := a.Gen
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = 3
	m.Update(activityScanMsg{source: core.SourceKey(m.target()), gen: manual, done: true, snapshot: snapshot, err: errors.New("offline")})
	if a.RetainedRun != "reading" || m.run() == nil {
		t.Fatal("failed refresh erased useful reading context")
	}
	m.refreshActivityManually()
	manual = a.Gen
	snapshot.Version = 4
	m.Update(activityScanMsg{source: core.SourceKey(m.target()), gen: manual, done: true, snapshot: snapshot})
	if a.RetainedRun != "" || m.run() != nil || len(m.runRows()) != 0 || a.Scope != scopeRunning {
		t.Fatal("successful refresh did not leave the empty Running view")
	}
	m.Update(activityScanMsg{source: core.SourceKey(m.target()), gen: gen, done: true, snapshot: snapshot})
	if m.run() != nil {
		t.Fatal("late automatic response resurrected dismissed run")
	}
}

func TestCancellingScanPreservesRetainedReading(t *testing.T) {
	m, a, records := runningRetentionModel(t, 1)
	finishRetainedRun(m, records, 2)
	a.Pending = true
	m.perform("activity-cancel")
	m.rebuildActivityViews()
	if a.RetainedRun != "reading" || m.run() == nil {
		t.Fatal("cancelling network work interrupted reading")
	}
}

func TestRunningRetentionWhenHydrationObservesCompletionFirst(t *testing.T) {
	m, a, records := runningRetentionModel(t, 1)
	finished := records[0].Run()
	finished.Info.Status = "FINISHED"
	finished.Info.EndTime = 3000
	a.Runs[finished.ID()] = &finished
	m.rebuildActivityViews()
	if m.run() == nil || m.run().Info.Status != "FINISHED" {
		t.Fatal("setup did not hydrate terminal metadata")
	}
	finishRetainedRun(m, records, 2)
	if a.RetainedRun != "reading" || m.run() == nil || len(m.runRows()) != 1 {
		t.Fatal("terminal hydration prevented later retention of selected member")
	}
}

func TestRunningRetentionRespectsVisibilityAndExperimentLifecycle(t *testing.T) {
	m, a, records := runningRetentionModel(t, 1)
	finishRetainedRun(m, records, 2)
	m.state().Visibility[core.VisibilityKey("experiment", "e1")] = core.VisibilityHidden
	m.rebuildActivityViews()
	if a.RetainedRun != "" || m.run() != nil {
		t.Fatal("hidden experiment leaked through retention")
	}
	m, a, records = runningRetentionModel(t, 1)
	finishRetainedRun(m, records, 2)
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = 3
	snapshot.Initialized = true
	snapshot.Experiments = []core.Experiment{{ID: "e2"}}
	m.acceptActivitySnapshot(snapshot)
	if a.RetainedRun != "" || m.run() != nil {
		t.Fatal("inactive experiment leaked through retention")
	}
}

func TestRunningResumedRunStopsBeingRetained(t *testing.T) {
	m, a, records := runningRetentionModel(t, 1)
	finishRetainedRun(m, records, 2)
	records[0].Status = "RUNNING"
	records[0].EndTime = 0
	records[0].ObservedAt = 40
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = 4
	m.acceptActivitySnapshot(snapshot)
	if a.RetainedRun != "" || m.run() == nil || a.ScopeCounts[scopeRunning] != 1 {
		t.Fatal("resumed run retained a false completion marker")
	}
}

func TestPendingManualRefreshDoesNotDismissNewReadingSelection(t *testing.T) {
	m, a, records := runningRetentionModel(t, 2)
	finishRetainedRun(m, records, 2)
	m.refreshActivityManually()
	gen := a.Gen
	m.selectRun(1)
	if m.run() == nil || m.run().ID() != "other" {
		t.Fatal("navigation setup failed")
	}
	records[1].Status = "FINISHED"
	records[1].EndTime = 4000
	records[1].ObservedAt = 30
	snapshot := activityReviewSnapshot(m, records...)
	snapshot.Version = 3
	m.Update(activityScanMsg{source: core.SourceKey(m.target()), gen: gen, done: true, snapshot: snapshot})
	if a.RetainedRun != "other" || m.run() == nil || m.run().ID() != "other" {
		t.Fatal("old manual refresh disrupted the new reading selection")
	}
}
