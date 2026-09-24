package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func boolSetting(v *bool) bool { return v == nil || *v }
func overrideLabel(v *bool) string {
	if v == nil {
		return "inherit"
	}
	if *v {
		return "on"
	}
	return "off"
}
func cycleOverride(v *bool) *bool {
	if v == nil {
		x := true
		return &x
	}
	if *v {
		x := false
		return &x
	}
	return nil
}
func flipSetting(v *bool) *bool { x := !boolSetting(v); return &x }

func (m *model) activityPreferenceExperiment() string {
	if m.focus == 0 && m.activityScope() == scopeExperiment {
		return m.selectedExperiment()
	}
	if r := m.run(); r != nil {
		return r.Info.ExperimentID
	}
	return m.selectedExperiment()
}
func (m *model) activityExperimentPolicy() core.ActivityPolicy {
	if s := m.state(); s != nil {
		if r := s.Runs[m.activityPreferenceExperiment()]; r != nil && r.View.Activity != nil {
			return *core.CloneActivityPolicy(r.View.Activity)
		}
	}
	return core.ActivityPolicy{}
}
func (m *model) activityPickerItems() []pickerItem {
	a := m.activityState()
	if a == nil {
		return nil
	}
	if a.PolicyLoading {
		return []pickerItem{{ID: "loading", Label: "Loading experiment preferences…"}}
	}
	experiment := m.activityPreferenceExperiment()
	policy := m.activityExperimentPolicy()
	if m.overlay == "activity-subscriptions" {
		keys := map[string]bool{}
		if run := m.run(); run != nil && run.Info.ExperimentID == experiment {
			for _, metric := range run.Data.Metrics {
				keys[metric.Key] = true
			}
		}
		for _, record := range a.Snapshot.Records {
			if record.ExperimentID == experiment {
				for _, metric := range record.Metrics {
					if !strings.HasPrefix(metric.Key, "system/") {
						keys[metric.Key] = true
					}
				}
			}
		}
		for _, sub := range policy.Subscriptions {
			keys[sub.Key] = true
		}
		if r := m.state().Runs[experiment]; r != nil {
			for _, pin := range r.View.MetricPins {
				keys[pin] = true
			}
		}
		names := make([]string, 0, len(keys))
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		var items []pickerItem
		for _, key := range names {
			mode := "off"
			for _, sub := range policy.Subscriptions {
				if sub.Key == key {
					mode = sub.Mode
				}
			}
			items = append(items, pickerItem{ID: key, Label: fmt.Sprintf("[%s] %s", mode, key)})
		}
		return items
	}
	refresh := fmt.Sprintf("%ds", m.opts.Activity.RefreshSeconds)
	if m.opts.Activity.RefreshSeconds == 0 {
		refresh = "paused"
	}
	items := []pickerItem{
		{ID: "refresh", Label: "Global · activity refresh: " + refresh + " (cycle)"},
		{ID: "read-on", Label: "Global · mark read on: " + m.opts.Activity.ReadOn},
		{ID: "metrics", Label: fmt.Sprintf("Global · all model metric samples notify: %t", m.opts.Activity.MetricUpdates)},
		{ID: "failed", Label: fmt.Sprintf("Global · FAILED alerts: %t", boolSetting(m.opts.Alerts.Failed))},
		{ID: "nonfinite", Label: fmt.Sprintf("Global · NaN / ±Inf alerts: %t", boolSetting(m.opts.Alerts.NonFinite))},
		{ID: "system", Label: fmt.Sprintf("Global · include system metrics in alerts: %t", boolSetting(m.opts.Alerts.IncludeSystem))},
	}
	if experiment != "" {
		read := policy.ReadOn
		if read == "" {
			read = "inherit"
		}
		items = append(items, pickerItem{ID: "experiment-metrics", Label: "Experiment " + experiment + " · all model metric samples: " + overrideLabel(policy.MetricUpdates)}, pickerItem{ID: "experiment-read", Label: "Experiment " + experiment + " · mark read on: " + read}, pickerItem{ID: "subscriptions", Label: fmt.Sprintf("Experiment %s · metric subscriptions (%d) →", experiment, len(policy.Subscriptions))})
		var failed, nonfinite *bool
		if policy.Alerts != nil {
			failed = policy.Alerts.Failed
			nonfinite = policy.Alerts.NonFinite
		}
		items = append(items, pickerItem{ID: "experiment-failed", Label: "Experiment · FAILED alerts: " + overrideLabel(failed)}, pickerItem{ID: "experiment-nonfinite", Label: "Experiment · NaN / ±Inf alerts: " + overrideLabel(nonfinite)})
	}
	items = append(items, pickerItem{ID: "alert-filter", Label: "Alerts view: " + a.AlertMode + " (cycle)"}, pickerItem{ID: "acknowledge-all", Label: "Acknowledge all matching active alert episodes"}, pickerItem{ID: "restore-alerts", Label: "Restore selected run's current alerts"}, pickerItem{ID: "full", Label: "Rebuild full history counts / alerts"})
	return items
}
func (m *model) chooseActivityPicker(id, key string) tea.Cmd {
	if id == "loading" || key != "enter" && key != "space" {
		return nil
	}
	policy := m.activityExperimentPolicy()
	if m.overlay == "activity-subscriptions" {
		found := -1
		for i, sub := range policy.Subscriptions {
			if sub.Key == id {
				found = i
				break
			}
		}
		if found < 0 {
			policy.Subscriptions = append(policy.Subscriptions, core.MetricSubscription{Key: id, Mode: "value"})
		} else if policy.Subscriptions[found].Mode == "value" {
			policy.Subscriptions[found].Mode = "sample"
		} else {
			policy.Subscriptions = append(policy.Subscriptions[:found], policy.Subscriptions[found+1:]...)
		}
		return m.saveActivityPolicy(policy)
	}
	switch id {
	case "refresh":
		choices := []int{0, 5, 15, 30, 60, 120}
		next := 30
		for i, n := range choices {
			if n == m.opts.Activity.RefreshSeconds {
				next = choices[(i+1)%len(choices)]
				break
			}
		}
		m.opts.Activity.RefreshSeconds = next
	case "read-on":
		if m.opts.Activity.ReadOn == "select" {
			m.opts.Activity.ReadOn = "open"
		} else {
			m.opts.Activity.ReadOn = "select"
		}
	case "metrics":
		m.opts.Activity.MetricUpdates = !m.opts.Activity.MetricUpdates
	case "failed":
		m.opts.Alerts.Failed = flipSetting(m.opts.Alerts.Failed)
	case "nonfinite":
		m.opts.Alerts.NonFinite = flipSetting(m.opts.Alerts.NonFinite)
	case "system":
		m.opts.Alerts.IncludeSystem = flipSetting(m.opts.Alerts.IncludeSystem)
	case "experiment-metrics":
		policy.MetricUpdates = cycleOverride(policy.MetricUpdates)
		return m.saveActivityPolicy(policy)
	case "experiment-read":
		switch policy.ReadOn {
		case "":
			policy.ReadOn = "open"
		case "open":
			policy.ReadOn = "select"
		default:
			policy.ReadOn = ""
		}
		return m.saveActivityPolicy(policy)
	case "experiment-failed", "experiment-nonfinite":
		if policy.Alerts == nil {
			policy.Alerts = &core.AlertSettings{}
		}
		if id == "experiment-failed" {
			policy.Alerts.Failed = cycleOverride(policy.Alerts.Failed)
		} else {
			policy.Alerts.NonFinite = cycleOverride(policy.Alerts.NonFinite)
		}
		return m.saveActivityPolicy(policy)
	case "subscriptions":
		return m.openPicker("activity-subscriptions")
	case "alert-filter":
		cmd, _ := m.performActivity(id)
		return cmd
	case "acknowledge-all", "restore-alerts":
		return m.changeActivity(id)
	case "full":
		m.overlay = ""
		return m.refreshActivity(true, nil)
	default:
		return nil
	}
	return m.saveActivitySettings()
}
func (m *model) saveActivitySettings() tea.Cmd {
	m.invalidateActivityPolicies()
	m.activityTickGen++
	settings, alerts, save := m.opts.Activity, m.opts.Alerts, m.opts.SaveActivity
	var effect tea.Cmd
	if save != nil {
		m.writer.add("activity-settings", func(context.Context) error { return save(settings, alerts) })
		effect = m.flushActivityPreferences()
	} else {
		m.status = "Activity preferences apply for this session"
		effect = m.refreshActivity(false, nil)
	}
	return tea.Batch(effect, m.activityTick())
}
func (m *model) saveActivityPolicy(policy core.ActivityPolicy) tea.Cmd {
	m.invalidateActivityPolicies()
	id, s := m.activityPreferenceExperiment(), m.state()
	if id == "" || s == nil {
		return nil
	}
	if s.Runs[id] == nil {
		s.Runs[id] = &runState{View: core.DefaultView(m.metricColumns, m.paramColumns)}
	}
	r := s.Runs[id]
	r.View.Activity = core.CloneActivityPolicy(&policy)
	r.ViewRevision++
	if a := m.activityState(); a != nil {
		if a.PolicyEdits == nil {
			a.PolicyEdits = map[string]core.ActivityPolicy{}
		}
		a.PolicyEdits[id] = *core.CloneActivityPolicy(&policy)
	}
	source, store := core.SourceKey(m.target()), m.opts.State
	copy := core.CloneActivityPolicy(&policy)
	metrics, params := append([]string(nil), m.metricColumns...), append([]string(nil), m.paramColumns...)
	m.writer.add("activity-policy/"+source+"/"+id, func(ctx context.Context) error {
		v, found, err := store.LoadView(ctx, source, id)
		if err != nil {
			return err
		}
		if !found {
			v = core.DefaultView(metrics, params)
		}
		v.Activity = core.CloneActivityPolicy(copy)
		return store.SaveView(ctx, source, id, v)
	})
	return m.flushActivityPreferences()
}

func (m *model) invalidateActivityPolicies() {
	if a := m.activityCurrent(); a != nil {
		for _, experiment := range a.Snapshot.Experiments {
			if !a.Snapshot.Counts[experiment.ID].Complete {
				delete(a.FullAttempted, experiment.ID)
			}
		}
	}
	m.stopActivity()
}
func (m *model) flushActivityPreferences() tea.Cmd {
	writer := m.writer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return activitySettingsSavedMsg{err: writer.flush(ctx)}
	}
}

type activityPolicyLoadedMsg struct {
	source, experiment string
	value              core.ExperimentView
	found              bool
	revision           uint64
	err                error
}

func (m *model) loadActivityPolicy() tea.Cmd {
	id := m.activityPreferenceExperiment()
	a := m.activityState()
	if id == "" || a == nil {
		return nil
	}
	s := m.state()
	if s.Runs[id] == nil {
		s.Runs[id] = &runState{View: core.DefaultView(m.metricColumns, m.paramColumns)}
	}
	a.PolicyLoading = true
	source, store, revision := core.SourceKey(m.target()), m.opts.State, s.Runs[id].ViewRevision
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		defer cancel()
		v, ok, err := store.LoadView(ctx, source, id)
		return activityPolicyLoadedMsg{source, id, v, ok, revision, err}
	}
}
