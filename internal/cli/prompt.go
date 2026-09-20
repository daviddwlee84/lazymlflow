package cli

import (
	"fmt"

	"github.com/daviddwlee84/lazymlflow/internal/inspection"
	"github.com/spf13/cobra"
)

func (a *app) promptCommand() *cobra.Command {
	group := a.group("prompt", "Render evidence-grounded review prompts without launching an agent")
	list := &cobra.Command{Use: "list", Short: "List built-in prompt recipes", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		recipes := inspection.Recipes()
		if a.jsonOutput {
			return a.output(cmd, recipes)
		}
		w := table(cmd.OutOrStdout())
		fmt.Fprintln(w, "RECIPE\tDESCRIPTION")
		for _, recipe := range recipes {
			fmt.Fprintf(w, "%s\t%s\n", recipe.ID, recipe.Description)
		}
		return w.Flush()
	}}
	render := a.group("render", "Collect a snapshot and render a reusable prompt")
	render.AddCommand(
		a.runEvidenceCommand("run-summary RUN_ID", "Render a prompt for one recorded run", "run-summary", 1),
		a.runEvidenceCommand("compare-runs RUN_ID RUN_ID...", "Render a prompt comparing explicitly selected runs", "compare-runs", 2),
		a.experimentEvidenceCommand("experiment-summary EXPERIMENT_ID", "Render a prompt for a complete experiment metadata scan", "experiment-summary"),
	)
	group.AddCommand(list, render)
	return group
}
