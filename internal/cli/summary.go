package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/inspection"
	"github.com/spf13/cobra"
)

type summaryFlags struct {
	metrics       []string
	history       string
	includeSystem bool
	filter        string
	order         []string
	view          string
	detailLimit   int
	allDetails    bool
}

func (f *summaryFlags) bind(cmd *cobra.Command, experiment bool) {
	cmd.Flags().StringArrayVar(&f.metrics, "metric", nil, "Exact history metric key; repeat (default: all non-system latest metric keys)")
	cmd.Flags().StringVar(&f.history, "history", "sampled", "History points: none, sampled (up to 200 per metric), or full")
	cmd.Flags().BoolVar(&f.includeSystem, "include-system", false, "Include system/ metric histories in automatic selection")
	if experiment {
		cmd.Flags().StringVar(&f.filter, "filter", "", "MLflow server-side run filter for the complete metadata scan")
		cmd.Flags().StringSliceVar(&f.order, "order-by", []string{"attributes.start_time DESC"}, "Server ordering for the complete scan and detailed-run selection")
		cmd.Flags().StringVar(&f.view, "view", "active", "Lifecycle view: active, deleted, all")
		cmd.Flags().IntVar(&f.detailLimit, "detail-limit", 20, "Maximum matching runs with detailed histories, artifacts, and notes")
		cmd.Flags().BoolVar(&f.allDetails, "all-details", false, "Collect details for every matching run (metadata is always scanned completely)")
	}
}
func (f summaryFlags) options() (inspection.Options, error) {
	if f.history != "none" && f.history != "sampled" && f.history != "full" {
		return inspection.Options{}, usagef("--history must be none, sampled, or full")
	}
	for _, key := range f.metrics {
		if strings.TrimSpace(key) == "" {
			return inspection.Options{}, usagef("--metric cannot be empty")
		}
	}
	return inspection.Options{Metrics: f.metrics, History: inspection.HistoryMode(f.history), SampleLimit: 200, IncludeSystem: f.includeSystem}, nil
}
func (f summaryFlags) experimentOptions() (inspection.ExperimentOptions, error) {
	options, err := f.options()
	if err != nil {
		return inspection.ExperimentOptions{}, err
	}
	if f.detailLimit < 1 {
		return inspection.ExperimentOptions{}, usagef("--detail-limit must be positive")
	}
	view, err := (queryFlags{view: f.view, limit: 100}).validate()
	if err != nil {
		return inspection.ExperimentOptions{}, err
	}
	return inspection.ExperimentOptions{Options: options, Query: core.RunQuery{Filter: f.filter, OrderBy: f.order, ViewType: view, MaxResults: 100}, DetailLimit: f.detailLimit, AllDetails: f.allDetails}, nil
}
func (a *app) runsSummaryCommand() *cobra.Command {
	return a.runEvidenceCommand("summary RUN_ID...", "Collect a deterministic Markdown summary (JSON evidence with --json)", "", 1)
}
func (a *app) experimentsSummaryCommand() *cobra.Command {
	return a.experimentEvidenceCommand("summary EXPERIMENT_ID", "Summarize every matching metadata row and selected detailed runs", "")
}
func (a *app) runEvidenceCommand(use, short, recipe string, minRuns int) *cobra.Command {
	f := summaryFlags{}
	cmd := &cobra.Command{Use: use, Short: short, Args: func(cmd *cobra.Command, args []string) error {
		if len(unique(args)) < minRuns {
			return usagef("%s requires at least %d distinct run ID(s)", cmd.CommandPath(), minRuns)
		}
		if recipe == "run-summary" && len(unique(args)) != 1 {
			return usagef("run-summary requires exactly one run ID")
		}
		for _, id := range args {
			if strings.TrimSpace(id) == "" {
				return usagef("run IDs cannot be empty")
			}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		options, err := f.options()
		if err != nil {
			return err
		}
		return a.session(cmd.Context(), func(session *core.Session) error {
			state := a.state()
			defer state.Close()
			notes, _ := state.(core.NoteStore)
			snapshot, err := inspection.NewCollector(session.Backend, session.Target, notes).CollectRuns(cmd.Context(), args, options)
			if err != nil {
				return err
			}
			return a.writeEvidence(cmd, snapshot, recipe)
		})
	}}
	f.bind(cmd, false)
	return cmd
}
func (a *app) experimentEvidenceCommand(use, short, recipe string) *cobra.Command {
	f := summaryFlags{}
	cmd := &cobra.Command{Use: use, Short: short, Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		options, err := f.experimentOptions()
		if err != nil {
			return err
		}
		if strings.TrimSpace(args[0]) == "" {
			return usagef("experiment ID cannot be empty")
		}
		return a.session(cmd.Context(), func(session *core.Session) error {
			state := a.state()
			defer state.Close()
			notes, _ := state.(core.NoteStore)
			snapshot, err := inspection.NewCollector(session.Backend, session.Target, notes).CollectExperiment(cmd.Context(), args[0], options)
			if err != nil {
				return err
			}
			return a.writeEvidence(cmd, snapshot, recipe)
		})
	}}
	f.bind(cmd, true)
	return cmd
}
func (a *app) writeEvidence(cmd *cobra.Command, snapshot inspection.Snapshot, recipe string) error {
	if recipe != "" {
		prompt, err := inspection.RenderPrompt(recipe, snapshot)
		if err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, struct {
				Recipe         string `json:"recipe"`
				ContextVersion int    `json:"context_version"`
				Prompt         string `json:"prompt"`
			}{recipe, snapshot.Version, prompt})
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), prompt)
		return err
	}
	if a.jsonOutput {
		return a.output(cmd, snapshot)
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), inspection.RenderMarkdown(snapshot))
	return err
}
