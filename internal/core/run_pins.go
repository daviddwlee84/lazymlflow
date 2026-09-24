package core

import (
	"context"
	"errors"
	"strings"
)

// RunPin is a personal bookmark with enough cached metadata to find a run
// without connecting. It deliberately excludes metrics, parameters, tags,
// artifact locations and connection credentials.
type RunPin struct {
	RunID          string `json:"run_id"`
	ExperimentID   string `json:"experiment_id"`
	RunName        string `json:"run_name"`
	ExperimentName string `json:"experiment_name"`
	Status         string `json:"status"`
	StartTime      int64  `json:"start_time"`
	EndTime        int64  `json:"end_time,omitempty"`
	LifecycleStage string `json:"lifecycle_stage,omitempty"`
	PinnedAt       int64  `json:"pinned_at"`
	ObservedAt     int64  `json:"observed_at"`
}

func ValidateRunPin(pin RunPin) error {
	if strings.TrimSpace(pin.RunID) == "" || strings.TrimSpace(pin.ExperimentID) == "" {
		return errors.New("run pins require run and experiment IDs")
	}
	if pin.StartTime < 0 || pin.EndTime < 0 || pin.PinnedAt < 0 || pin.ObservedAt < 0 {
		return errors.New("run pin timestamps must be nonnegative")
	}
	return nil
}

func (p RunPin) Run() Run {
	return Run{Info: RunInfo{RunID: p.RunID, ExperimentID: p.ExperimentID, RunName: p.RunName, Status: p.Status, StartTime: p.StartTime, EndTime: p.EndTime, LifecycleStage: p.LifecycleStage}}
}

func (p RunPin) Record() ActivityRecord {
	return ActivityRecord{RunID: p.RunID, RunName: p.RunName, ExperimentID: p.ExperimentID, ExperimentName: p.ExperimentName, Status: p.Status, StartTime: p.StartTime, EndTime: p.EndTime, LifecycleStage: p.LifecycleStage, ObservedAt: p.ObservedAt, UpdatedAt: p.ObservedAt}
}

// RunPinStore is optional so existing StateStore implementations remain valid.
// Each pin is independent; metadata upserts preserve its original PinnedAt.
type RunPinStore interface {
	LoadRunPins(context.Context, string) ([]RunPin, error)
	SaveRunPin(context.Context, string, RunPin) error
	DeleteRunPin(context.Context, string, string) error
}
