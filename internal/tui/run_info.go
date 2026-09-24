package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

func (m *model) isInformationPicker() bool {
	return m.overlay == "info" || m.overlay == "run-info" || m.overlay == "parent-info"
}

func (m *model) informationLines() (string, []string) {
	switch m.overlay {
	case "run-info":
		r := m.run()
		if r == nil {
			return "Run information", []string{"No run selected."}
		}
		return "Run information", []string{
			"Name: " + r.Name(),
			"Run ID: " + r.ID(),
			"Experiment: " + m.activityExperimentName(r.Info.ExperimentID),
			"Experiment ID: " + r.Info.ExperimentID,
			"Status: " + r.Info.Status,
			"Lifecycle: " + r.Info.LifecycleStage,
			"Started: " + timestamp(r.Info.StartTime),
			"Ended: " + timestamp(r.Info.EndTime),
		}
	case "parent-info":
		if r := m.parentInfo; r != nil {
			lines := []string{"Name: " + r.Name(), "Run ID: " + r.ID(), "Experiment: " + m.activityExperimentName(r.Info.ExperimentID), "Experiment ID: " + r.Info.ExperimentID, "Status: " + r.Info.Status, "Parent: " + r.ParentID()}
			return "Parent run (context only)", append(lines, kvLines(r.Data.Params)...)
		}
		return "Parent run (context only)", []string{"Loading parent…"}
	default:
		var lines []string
		if s := m.state(); s != nil {
			for _, experiment := range s.Experiments {
				if experiment.ID == s.Selected {
					lines = []string{"Name: " + experiment.Name, "ID: " + experiment.ID, "Artifacts: " + experiment.ArtifactLocation, "Lifecycle: " + experiment.LifecycleStage}
					lines = append(lines, kvLines(experiment.Tags)...)
					break
				}
			}
			lines = append(lines, m.experimentActivityOverview(s.Selected)...)
		}
		return "Experiment information", lines
	}
}

func (m *model) informationName() (string, bool) {
	switch m.overlay {
	case "run-info":
		if run := m.run(); run != nil {
			return run.Name(), true
		}
	case "parent-info":
		if m.parentInfo != nil {
			return m.parentInfo.Name(), true
		}
	case "info":
		if state := m.state(); state != nil {
			for _, experiment := range state.Experiments {
				if experiment.ID == state.Selected {
					return experiment.Name, true
				}
			}
		}
	}
	return "", false
}

func (m *model) copyInformationName() tea.Cmd {
	value, ok := m.informationName()
	if !ok {
		m.status = "No name available to copy"
		return nil
	}
	ctx, target := m.ctx, m.active
	return func() tea.Msg { return resultMsg{target, "Copied full name", platform.Copy(ctx, value)} }
}

func (m *model) informationMaxOffset() int {
	_, lines := m.informationLines()
	geometry := m.geometry().Content
	wrapped := 0
	for _, line := range lines {
		wrapped += len(wrapText(clean(line), max(1, geometry.W-2)))
	}
	return max(0, wrapped-max(1, geometry.H-2))
}

func (m *model) scrollInformation(delta int) {
	m.menuIndex = clamp(m.menuIndex, 0, m.informationMaxOffset())
	m.menuIndex = clamp(m.menuIndex+delta, 0, m.informationMaxOffset())
}
