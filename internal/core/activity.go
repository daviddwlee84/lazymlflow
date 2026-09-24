package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ActivitySettings are defaults shared by the terminal and activity commands.
// Config readers should start with DefaultActivitySettings before decoding so
// an explicit zero refresh interval remains distinguishable from an omission.
type ActivitySettings struct {
	RefreshSeconds    int    `toml:"refresh_seconds" json:"refresh_seconds"`
	InitialUnreadDays int    `toml:"initial_unread_days" json:"initial_unread_days"`
	MetricUpdates     bool   `toml:"metric_updates" json:"metric_updates"`
	ReadOn            string `toml:"read_on" json:"read_on"`
}

func DefaultActivitySettings() ActivitySettings {
	return ActivitySettings{RefreshSeconds: 30, InitialUnreadDays: 7, ReadOn: "open"}
}

func ValidateActivitySettings(v ActivitySettings) error {
	if v.RefreshSeconds < 0 || v.InitialUnreadDays < 0 {
		return errors.New("activity refresh_seconds and initial_unread_days must be nonnegative")
	}
	if v.ReadOn != "" && v.ReadOn != "open" && v.ReadOn != "select" {
		return errors.New("activity read_on must be open or select")
	}
	return nil
}

// Nil booleans inherit the enabled default. Metric lists contain exact keys;
// an empty inclusion list means all keys. Exclusions take precedence.
type AlertSettings struct {
	Failed         *bool    `toml:"failed,omitempty" json:"failed,omitempty"`
	NonFinite      *bool    `toml:"non_finite,omitempty" json:"non_finite,omitempty"`
	IncludeSystem  *bool    `toml:"include_system,omitempty" json:"include_system,omitempty"`
	IncludeMetrics []string `toml:"include_metrics,omitempty" json:"include_metrics,omitempty"`
	ExcludeMetrics []string `toml:"exclude_metrics,omitempty" json:"exclude_metrics,omitempty"`
}

func ValidateAlertSettings(v AlertSettings) error {
	for _, keys := range [][]string{v.IncludeMetrics, v.ExcludeMetrics} {
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				return errors.New("alert metric keys cannot be empty")
			}
		}
	}
	return nil
}

type MetricSubscription struct {
	Key  string `toml:"key" json:"key"`
	Mode string `toml:"mode" json:"mode"` // value or sample
}

// ActivityPolicy is an optional per-experiment override. Subscriptions are
// exact keys, including producer-maintained best metrics such as best_corr.
type ActivityPolicy struct {
	MetricUpdates *bool                `toml:"metric_updates,omitempty" json:"metric_updates,omitempty"`
	ReadOn        string               `toml:"read_on,omitempty" json:"read_on,omitempty"`
	Alerts        *AlertSettings       `toml:"alerts,omitempty" json:"alerts,omitempty"`
	Subscriptions []MetricSubscription `toml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
}

func ValidateActivityPolicy(v ActivityPolicy) error {
	if err := ValidateActivitySettings(ActivitySettings{ReadOn: v.ReadOn}); err != nil {
		return err
	}
	if v.Alerts != nil {
		if err := ValidateAlertSettings(*v.Alerts); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, sub := range v.Subscriptions {
		if strings.TrimSpace(sub.Key) == "" || sub.Mode != "value" && sub.Mode != "sample" {
			return errors.New("metric subscriptions require a key and mode value or sample")
		}
		if seen[sub.Key] {
			return fmt.Errorf("duplicate metric subscription %q", sub.Key)
		}
		seen[sub.Key] = true
	}
	return nil
}

func EffectiveActivityPolicy(settings ActivitySettings, alerts AlertSettings, override ActivityPolicy) ActivityPolicy {
	metrics := settings.MetricUpdates
	readOn := settings.ReadOn
	if readOn == "" {
		readOn = "open"
	}
	if override.MetricUpdates != nil {
		metrics = *override.MetricUpdates
	}
	if override.ReadOn != "" {
		readOn = override.ReadOn
	}
	if a := override.Alerts; a != nil {
		if a.Failed != nil {
			alerts.Failed = a.Failed
		}
		if a.NonFinite != nil {
			alerts.NonFinite = a.NonFinite
		}
		if a.IncludeSystem != nil {
			alerts.IncludeSystem = a.IncludeSystem
		}
		if a.IncludeMetrics != nil {
			alerts.IncludeMetrics = a.IncludeMetrics
		}
		if a.ExcludeMetrics != nil {
			alerts.ExcludeMetrics = a.ExcludeMetrics
		}
	}
	return *CloneActivityPolicy(&ActivityPolicy{MetricUpdates: &metrics, ReadOn: readOn, Alerts: &alerts, Subscriptions: override.Subscriptions})
}

func CloneActivityPolicy(policy *ActivityPolicy) *ActivityPolicy {
	if policy == nil {
		return nil
	}
	v := *policy
	copyBool := func(p *bool) *bool {
		if p == nil {
			return nil
		}
		b := *p
		return &b
	}
	v.MetricUpdates = copyBool(v.MetricUpdates)
	v.Subscriptions = append([]MetricSubscription(nil), v.Subscriptions...)
	if v.Alerts != nil {
		a := *v.Alerts
		a.Failed, a.NonFinite, a.IncludeSystem = copyBool(a.Failed), copyBool(a.NonFinite), copyBool(a.IncludeSystem)
		if a.IncludeMetrics != nil {
			a.IncludeMetrics = append([]string{}, a.IncludeMetrics...)
		}
		if a.ExcludeMetrics != nil {
			a.ExcludeMetrics = append([]string{}, a.ExcludeMetrics...)
		}
		v.Alerts = &a
	}
	return &v
}

type ActivityAlert struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Metric       string `json:"metric,omitempty"`
	Value        string `json:"value,omitempty"`
	Episode      int64  `json:"episode"`
	Active       bool   `json:"active"`
	Acknowledged bool   `json:"acknowledged"`
	Suppressed   bool   `json:"suppressed,omitempty"`
	FirstSeen    int64  `json:"first_seen"`
	LastSeen     int64  `json:"last_seen"`
}

type ActivityRecord struct {
	RunID          string          `json:"run_id"`
	RunName        string          `json:"run_name"`
	ExperimentID   string          `json:"experiment_id"`
	ExperimentName string          `json:"experiment_name"`
	Status         string          `json:"status"`
	StartTime      int64           `json:"start_time"`
	EndTime        int64           `json:"end_time,omitempty"`
	LifecycleStage string          `json:"lifecycle_stage,omitempty"`
	Metrics        []Metric        `json:"metrics,omitempty"`
	Revision       int64           `json:"revision"`
	ReadRevision   int64           `json:"read_revision"`
	Reasons        []string        `json:"reasons"`
	Alerts         []ActivityAlert `json:"alerts"`
	ObservedAt     int64           `json:"observed_at"`
	UpdatedAt      int64           `json:"updated_at"`
	// Fingerprints are durable observation baselines, independent of the
	// disposable status index. They contain no parameters, tags or credentials.
	MetricFingerprint string            `json:"metric_fingerprint,omitempty"`
	Subscriptions     map[string]string `json:"subscription_fingerprints,omitempty"`
}

func (r ActivityRecord) Unread() bool { return r.Revision > r.ReadRevision }
func (r ActivityRecord) HasAlerts() bool {
	for _, a := range r.Alerts {
		if a.Active && !a.Acknowledged && !a.Suppressed {
			return true
		}
	}
	return false
}
func (r ActivityRecord) Run() Run {
	return Run{Info: RunInfo{RunID: r.RunID, ExperimentID: r.ExperimentID, RunName: r.RunName, Status: r.Status, StartTime: r.StartTime, EndTime: r.EndTime, LifecycleStage: r.LifecycleStage}, Data: RunData{Metrics: append([]Metric(nil), r.Metrics...)}}
}

type ExperimentActivityCounts struct {
	ExperimentID string         `json:"experiment_id"`
	Total        int            `json:"total"`
	Statuses     map[string]int `json:"statuses"`
	Running      int            `json:"running"`
	Finished     int            `json:"finished"`
	Failed       int            `json:"failed"`
	Killed       int            `json:"killed"`
	Other        int            `json:"other"`
	Unread       int            `json:"unread"`
	Alerts       int            `json:"alerts"`
	Complete     bool           `json:"complete"`
	Stale        bool           `json:"stale"`
	ScannedAt    int64          `json:"scanned_at"`
}

type ActivitySnapshot struct {
	Version            uint64                              `json:"version"`
	Source             string                              `json:"source"`
	Records            map[string]ActivityRecord           `json:"records"`
	Experiments        []Experiment                        `json:"experiments"`
	Counts             map[string]ExperimentActivityCounts `json:"counts"`
	Checkpoint         int64                               `json:"checkpoint"`
	Initialized        bool                                `json:"initialized"`
	InitialUnreadSince int64                               `json:"initial_unread_since"`
	LastFullScan       int64                               `json:"last_full_scan"`
	UpdatedAt          int64                               `json:"updated_at"`
	Complete           bool                                `json:"complete"`
	Errors             []string                            `json:"errors"`
}

func NewActivitySnapshot(source string) ActivitySnapshot {
	return ActivitySnapshot{Source: source, Records: map[string]ActivityRecord{}, Experiments: []Experiment{}, Counts: map[string]ExperimentActivityCounts{}, Errors: []string{}}
}

type ActivityReceipt struct {
	RunID    string `json:"run_id"`
	Revision int64  `json:"revision"`
}

type ActivityObservation struct {
	Run            Run
	ExperimentName string
	Policy         ActivityPolicy
	// CacheOnly allows all-history counts without turning every historical run
	// into durable inbox data. Existing observations and alerts remain tracked.
	CacheOnly bool
	// Retain keeps older recent-list rows available without notifying them.
	Retain bool
}

type ActivityBatch struct {
	Observations       []ActivityObservation
	Experiments        []Experiment
	ReplaceExperiments bool
	StartedAt          int64
	ObservedAt         int64
	Checkpoint         int64
	Complete           bool
	InitialUnreadSince int64
	ForceUnreadSince   int64
	// CompleteExperiments replaces their rebuildable status-index population.
	CompleteExperiments []string
	FullScanComplete    bool
	Errors              []string
	// PolicyUpdates re-evaluate cached evidence without presenting it as a
	// fresh server observation. The store applies them to its current records.
	PolicyUpdates map[string]ActivityPolicy
}

// ActivityStore is separate from StateStore so existing preference stores and
// test doubles remain compatible. Receipts acknowledge only observed revisions.
type ActivityStore interface {
	LoadActivity(context.Context, string) (ActivitySnapshot, error)
	ObserveActivity(context.Context, string, ActivityBatch) (ActivitySnapshot, error)
	MarkActivityRead(context.Context, string, []ActivityReceipt) error
	MarkActivityUnread(context.Context, string, []string) error
	AcknowledgeActivity(context.Context, string, []ActivityReceipt, bool) error
}

func enabled(v *bool) bool { return v == nil || *v }
func containsExact(values []string, key string) bool {
	for _, v := range values {
		if v == key {
			return true
		}
	}
	return false
}

func DetectActivityAlerts(run Run, settings AlertSettings) []ActivityAlert {
	alerts := []ActivityAlert{}
	if enabled(settings.Failed) && run.Info.Status == "FAILED" {
		alerts = append(alerts, ActivityAlert{ID: "failed", Kind: "failed", Value: "FAILED", Active: true})
	}
	if enabled(settings.NonFinite) {
		seen := map[string]bool{}
		for _, m := range run.Data.Metrics {
			if seen[m.Key] || !math.IsNaN(float64(m.Value)) && !math.IsInf(float64(m.Value), 0) {
				continue
			}
			if !enabled(settings.IncludeSystem) && strings.HasPrefix(m.Key, "system/") {
				continue
			}
			if len(settings.IncludeMetrics) > 0 && !containsExact(settings.IncludeMetrics, m.Key) || containsExact(settings.ExcludeMetrics, m.Key) {
				continue
			}
			seen[m.Key] = true
			alerts = append(alerts, ActivityAlert{ID: "non_finite:" + m.Key, Kind: "non_finite", Metric: m.Key, Value: m.Value.String(), Active: true})
		}
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].ID < alerts[j].ID })
	return alerts
}

func metricFingerprint(metrics []Metric, valueOnly bool) string {
	values := make([]string, 0, len(metrics))
	for _, m := range metrics {
		// String makes NaN stable and deliberately normalizes negative zero.
		value := m.Value.String()
		if m.Value == 0 {
			value = "0"
		}
		parts := []any{m.Key, value, m.ModelID, m.DatasetName, m.DatasetDigest}
		if !valueOnly {
			parts = append(parts, m.Step, m.Timestamp)
		}
		b, _ := json.Marshal(parts)
		values = append(values, string(b))
	}
	sort.Strings(values)
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ObserveActivityRecord is the deterministic reducer used inside the store's
// transaction. Reading a run and acknowledging an alert are separate actions.
func ObserveActivityRecord(previous ActivityRecord, observation ActivityObservation, now, initialSince, forceSince int64, initialized bool) ActivityRecord {
	run := observation.Run
	if previous.ObservedAt > now {
		return previous
	}
	exists := previous.RunID != ""
	next := previous
	next.RunID, next.RunName = run.ID(), run.Name()
	next.ExperimentID, next.ExperimentName = run.Info.ExperimentID, observation.ExperimentName
	next.Status, next.StartTime, next.EndTime = run.Info.Status, run.Info.StartTime, run.Info.EndTime
	next.LifecycleStage, next.ObservedAt = run.Info.LifecycleStage, now
	next.Metrics = append([]Metric(nil), run.Data.Metrics...)
	next.Reasons = append([]string(nil), previous.Reasons...)
	if next.Reasons == nil {
		next.Reasons = []string{}
	}
	if next.Revision == 0 {
		next.Revision, next.ReadRevision = 1, 1
	}
	reasons := []string{}
	if !exists {
		if !observation.CacheOnly && (initialized || run.Info.Status == "RUNNING" || initialSince > 0 && (run.Info.StartTime >= initialSince || run.Info.EndTime >= initialSince)) {
			reasons = append(reasons, "new_run")
		}
	} else if previous.Status != run.Info.Status || previous.EndTime != run.Info.EndTime {
		reasons = append(reasons, "status")
	}
	modelMetrics := []Metric{}
	for _, metric := range run.Data.Metrics {
		if !strings.HasPrefix(metric.Key, "system/") {
			modelMetrics = append(modelMetrics, metric)
		}
	}
	fingerprint := metricFingerprint(modelMetrics, false)
	if exists && observation.Policy.MetricUpdates != nil && *observation.Policy.MetricUpdates && previous.MetricFingerprint != "" && fingerprint != previous.MetricFingerprint {
		reasons = append(reasons, "metrics")
	}
	next.MetricFingerprint = fingerprint
	next.Subscriptions = map[string]string{}
	for _, sub := range observation.Policy.Subscriptions {
		var matching []Metric
		for _, metric := range run.Data.Metrics {
			if metric.Key == sub.Key {
				matching = append(matching, metric)
			}
		}
		key := sub.Mode + ":" + sub.Key
		if len(matching) == 0 {
			if before := previous.Subscriptions[key]; before != "" {
				next.Subscriptions[key] = before
			} else {
				next.Subscriptions[key] = "missing"
			}
			continue
		}
		fp := metricFingerprint(matching, sub.Mode == "value")
		next.Subscriptions[key] = fp
		if before := previous.Subscriptions[key]; exists && before != "" && before != fp {
			reasons = append(reasons, "metric:"+sub.Key)
		}
	}
	settings := AlertSettings{}
	if observation.Policy.Alerts != nil {
		settings = *observation.Policy.Alerts
	}
	detected := DetectActivityAlerts(run, settings)
	oldAlerts := map[string]ActivityAlert{}
	for _, a := range previous.Alerts {
		oldAlerts[a.ID] = a
	}
	next.Alerts = []ActivityAlert{}
	newIDs := map[string]bool{}
	for _, current := range detected {
		old, found := oldAlerts[current.ID]
		newFailure := current.Kind == "failed" && previous.EndTime != run.Info.EndTime && run.Info.EndTime > 0
		if found && old.Active && !newFailure {
			current.Episode, current.FirstSeen, current.Acknowledged = old.Episode, old.FirstSeen, old.Acknowledged
		} else {
			current.FirstSeen = now
			newIDs[current.ID] = true
			if exists || !observation.CacheOnly && initialized {
				reasons = append(reasons, "alert:"+current.ID)
			}
		}
		current.LastSeen = now
		next.Alerts = append(next.Alerts, current)
		delete(oldAlerts, current.ID)
	}
	for _, old := range oldAlerts {
		// Missing data and disabled rules are not evidence of recovery.
		suppressed := old.Kind == "failed" && !enabled(settings.Failed)
		if old.Kind == "non_finite" {
			suppressed = !enabled(settings.NonFinite) || !enabled(settings.IncludeSystem) && strings.HasPrefix(old.Metric, "system/") || len(settings.IncludeMetrics) > 0 && !containsExact(settings.IncludeMetrics, old.Metric) || containsExact(settings.ExcludeMetrics, old.Metric)
			finiteFound := false
			for _, metric := range run.Data.Metrics {
				if metric.Key == old.Metric && !math.IsNaN(float64(metric.Value)) && !math.IsInf(float64(metric.Value), 0) {
					finiteFound = true
				}
			}
			if !suppressed && finiteFound {
				old.Active = false
			}
		} else if !suppressed {
			old.Active = false
		}
		old.Suppressed = suppressed
		next.Alerts = append(next.Alerts, old)
	}
	if forceSince > 0 && (run.Info.StartTime >= forceSince || run.Info.EndTime >= forceSince) {
		reasons = append(reasons, "marked_unread")
	}
	if len(reasons) > 0 {
		next.Revision++
		next.UpdatedAt = now
		if !previous.Unread() {
			next.Reasons = []string{}
		}
		for _, reason := range reasons {
			if !containsExact(next.Reasons, reason) {
				next.Reasons = append(next.Reasons, reason)
			}
		}
	}
	if next.UpdatedAt == 0 {
		next.UpdatedAt = now
	}
	for i := range next.Alerts {
		if newIDs[next.Alerts[i].ID] {
			next.Alerts[i].Episode = next.Revision
		}
	}
	sort.Slice(next.Alerts, func(i, j int) bool { return next.Alerts[i].ID < next.Alerts[j].ID })
	return next
}

// ActivityRecords returns stable, newest-first rows for cached CLI/TUI views.
func ActivityRecords(snapshot ActivitySnapshot, view string) []ActivityRecord {
	rows := []ActivityRecord{}
	activeExperiments := map[string]bool{}
	for _, e := range snapshot.Experiments {
		activeExperiments[e.ID] = true
	}
	for _, r := range snapshot.Records {
		if r.LifecycleStage == "deleted" || snapshot.Initialized && !activeExperiments[r.ExperimentID] {
			continue
		}
		match := true
		switch view {
		case "running":
			match = r.Status == "RUNNING" && r.LifecycleStage != "deleted"
		case "recent", "ended":
			match = r.EndTime > 0 && r.Status != "RUNNING" && r.LifecycleStage != "deleted"
		case "unread":
			match = r.Unread()
		case "alerts", "problems":
			match = r.HasAlerts()
		case "acknowledged":
			match = false
			for _, a := range r.Alerts {
				if a.Active && a.Acknowledged && !a.Suppressed {
					match = true
				}
			}
		}
		if match {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].UpdatedAt, rows[j].UpdatedAt
		if view == "recent" || view == "ended" {
			a, b = rows[i].EndTime, rows[j].EndTime
		}
		if view == "running" {
			a, b = rows[i].StartTime, rows[j].StartTime
		}
		if a != b {
			return a > b
		}
		return rows[i].RunID < rows[j].RunID
	})
	return rows
}
