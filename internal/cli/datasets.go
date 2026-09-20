package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

func datasetInput(r core.Run, index int) (core.DatasetInput, error) {
	if index < 1 || index > len(r.Inputs.DatasetInputs) {
		return core.DatasetInput{}, usagef("dataset --index %d is outside 1..%d for run %s", index, len(r.Inputs.DatasetInputs), r.ID())
	}
	return r.Inputs.DatasetInputs[index-1], nil
}

func (a *app) datasetsCommand() *cobra.Command {
	group := a.group("datasets", "Discover datasets across experiments and inspect logged feature schemas")
	var all bool
	var experiments []string
	var view string
	list := &cobra.Command{Use: "list (--all | --experiment ID)", Short: "Scan all pages for dataset variants and run associations", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if all == (len(experiments) > 0) {
			return usagef("specify exactly one of --all or --experiment ID (repeatable)")
		}
		for _, id := range experiments {
			if strings.TrimSpace(id) == "" {
				return usagef("--experiment cannot be empty")
			}
		}
		lifecycle, err := (queryFlags{view: view, limit: 100}).validate()
		if err != nil {
			return err
		}
		return a.session(cmd.Context(), func(s *core.Session) error {
			catalog, scanErr := core.ScanDatasets(cmd.Context(), s.Backend, core.SourceKey(s.Target), core.DatasetScanOptions{ExperimentIDs: experiments, ViewType: lifecycle}, nil)
			if err := a.printDatasetCatalog(cmd, catalog); err != nil {
				return err
			}
			return scanErr
		})
	}}
	list.Flags().BoolVar(&all, "all", false, "Scan all experiments on this target")
	list.Flags().StringSliceVar(&experiments, "experiment", nil, "Scan an experiment; repeat or use comma-separated IDs")
	list.Flags().StringVar(&view, "view", "active", "Lifecycle: active, deleted, all")
	var schemaIndex int
	schema := &cobra.Command{Use: "schema RUN_ID", Short: "Show a logged dataset schema; no original dataset files are read", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if schemaIndex < 1 {
			return usagef("--index must be positive")
		}
		return a.session(cmd.Context(), func(s *core.Session) error {
			r, err := s.Backend.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			input, err := datasetInput(r, schemaIndex)
			if err != nil {
				return err
			}
			parsed := core.ParseDatasetSchema(input.Dataset.Schema)
			profile := core.ParseDatasetProfile(input.Dataset.Profile)
			id := core.DatasetIdentity(core.SourceKey(s.Target), input.Dataset)
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"run_id": r.ID(), "input_index": schemaIndex, "dataset_id": id, "dataset": input.Dataset, "context": core.DatasetContext(input), "schema": parsed, "profile": profile})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Dataset: %s (%s)\nIdentity: %s\nContext: %s\nSchema: %s; %d columns; %d leaves\n", cell(input.Dataset.Name), cell(input.Dataset.Digest), id, cell(core.DatasetContext(input)), parsed.Kind, parsed.Columns, parsed.Leaves)
			if profile.Rows != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Logged rows: %d\n", *profile.Rows)
			}
			if parsed.Error != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Schema unavailable: %s\nRaw: %s\n", cell(parsed.Error), cell(parsed.Raw))
				return nil
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "#\tPATH\tTYPE\tREQUIRED\tSHAPE\tFEATURE DIMENSIONS")
			for _, f := range core.FlattenSchemaFields(parsed) {
				required := "—"
				if f.Required != nil {
					required = fmt.Sprint(*f.Required)
				}
				shape := "—"
				if f.Shape != nil {
					shape = fmt.Sprint(f.Shape)
				}
				dim := "—"
				if f.Dimensions != nil {
					dim = fmt.Sprint(*f.Dimensions)
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", f.Index, cell(f.Path), cell(f.Type), required, shape, dim)
			}
			return w.Flush()
		})
	}}
	schema.Flags().IntVar(&schemaIndex, "index", 1, "One-based dataset input number on the run")
	var runIndex int
	var runExperiments []string
	var runView string
	runs := &cobra.Command{Use: "runs RUN_ID", Short: "Find runs sharing the exact dataset variant, across experiments", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if runIndex < 1 {
			return usagef("--index must be positive")
		}
		for _, id := range runExperiments {
			if strings.TrimSpace(id) == "" {
				return usagef("--experiment cannot be empty")
			}
		}
		lifecycle, err := (queryFlags{view: runView, limit: 100}).validate()
		if err != nil {
			return err
		}
		return a.session(cmd.Context(), func(s *core.Session) error {
			r, err := s.Backend.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			input, err := datasetInput(r, runIndex)
			if err != nil {
				return err
			}
			id := core.DatasetIdentity(core.SourceKey(s.Target), input.Dataset)
			catalog, scanErr := core.ScanDatasets(cmd.Context(), s.Backend, core.SourceKey(s.Target), core.DatasetScanOptions{ExperimentIDs: runExperiments, ViewType: lifecycle}, nil)
			uses := []core.DatasetUse{}
			for _, entry := range catalog.Entries {
				if entry.ID == id {
					uses = entry.Uses
					break
				}
			}
			if a.jsonOutput {
				if err := a.output(cmd, map[string]any{"dataset_id": id, "dataset": input.Dataset, "runs": uses, "scope": catalog.Scope, "experiment_ids": catalog.ExperimentIDs, "view_type": catalog.ViewType, "complete": catalog.Complete, "cancelled": catalog.Cancelled, "errors": catalog.Errors, "updated_at": catalog.UpdatedAt}); err != nil {
					return err
				}
				return scanErr
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "EXPERIMENT\tRUN ID\tRUN NAME\tSTATUS\tCONTEXT\tINPUT")
			for _, use := range uses {
				fmt.Fprintf(w, "%s (%s)\t%s\t%s\t%s\t%s\t%d\n", cell(use.ExperimentName), cell(use.ExperimentID), cell(use.RunID), cell(use.RunName), cell(use.Status), cell(use.Context), use.InputIndex)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Dataset %s: %d uses; complete=%t\n", id, len(uses), catalog.Complete)
			return scanErr
		})
	}}
	runs.Flags().IntVar(&runIndex, "index", 1, "One-based dataset input number on the seed run")
	runs.Flags().StringSliceVar(&runExperiments, "experiment", nil, "Limit to experiment IDs; default scans the target")
	runs.Flags().StringVar(&runView, "view", "active", "Lifecycle: active, deleted, all")
	group.AddCommand(list, schema, runs)
	return group
}

func (a *app) printDatasetCatalog(cmd *cobra.Command, c core.DatasetCatalog) error {
	if a.jsonOutput {
		return a.output(cmd, c)
	}
	w := table(cmd.OutOrStdout())
	fmt.Fprintln(w, "DATASET\tDIGEST\tSOURCE TYPE\tCOLUMNS\tUSES\tIDENTITY")
	for _, d := range c.Entries {
		cols := "—"
		if d.Schema.Kind == "tabular" && d.Schema.Error == "" {
			cols = fmt.Sprint(d.Schema.Columns)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", cell(d.Dataset.Name), cell(d.Dataset.Digest), cell(d.Dataset.SourceType), cols, len(d.Uses), d.ID)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "%d dataset variants, %d runs, %d experiments; complete=%t\n", len(c.Entries), c.RunsScanned, c.ExperimentsScanned, c.Complete)
	return err
}
