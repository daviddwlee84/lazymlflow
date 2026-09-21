package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/connection"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
	"github.com/spf13/cobra"
)

// ChildExitError is an ordinary child status, not a lazymlflow diagnostic.
// Execute returns Code directly without writing another message to stderr.
type ChildExitError struct{ Code int }

func (e *ChildExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.Code) }

func (a *app) environmentTarget(id string) (core.Target, error) {
	if id == "" {
		return a.resolve()
	}
	if a.trackingURI != "" || (a.targetID != "" && a.targetID != id) {
		return core.Target{}, usagef("a positional target ID cannot conflict with --target or --tracking-uri")
	}
	cfg, err := a.load()
	if err != nil {
		return core.Target{}, err
	}
	t, err := config.Resolve(cfg, id, "")
	if err != nil {
		return t, usageError{err}
	}
	return t, nil
}

func (a *app) targetsEnvironmentCommand() *cobra.Command {
	var shell string
	cmd := &cobra.Command{
		Use: "env [ID]", Short: "Print secret-free experiment environment exports for a direct HTTP(S) target",
		Long:    "Print POSIX sh exports (also usable by bash/zsh), or a JSON recipe with literal values,\nenvironment references, and variables to unset. Credentials remain references. No connection\nis opened. SSH targets require targets exec; local-store browsing adapters are not writable servers.",
		Args:    rangeArgs(0, 1),
		Example: "  lazymlflow targets env lab\n  eval \"$(lazymlflow targets env lab --shell sh)\"\n  lazymlflow --target lab targets env --json",
	}
	cmd.Flags().StringVar(&shell, "shell", "sh", "Export syntax (sh; compatible with bash and zsh)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if shell != "sh" {
			return usagef("unsupported shell %q; use --shell sh for sh, bash or zsh", shell)
		}
		if a.jsonOutput && cmd.Flags().Changed("shell") {
			return usagef("--json and --shell select different output formats; choose one")
		}
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		t, err := a.environmentTarget(id)
		if err != nil {
			return err
		}
		plan, err := connection.ClientEnvironmentPlan(t)
		if err != nil {
			return usageError{err}
		}
		if a.jsonOutput {
			return a.output(cmd, plan)
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), plan.RenderSH())
		return err
	}
	return cmd
}

func (a *app) targetsExecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "exec [ID] -- COMMAND [ARG...]", Short: "Run an experiment command with target settings and an owned SSH tunnel when needed",
		Long:    "Run COMMAND directly, with inherited terminal streams and the selected target's client\nenvironment. SSH forwarding lives until the child exits. The command's ordinary exit code is\npreserved. No shell evaluates arguments. Local file/SQLite browsing adapters are rejected.",
		Example: "  lazymlflow targets exec lab -- python train.py\n  lazymlflow --target remote targets exec -- uv run train.py",
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		n := cmd.ArgsLenAtDash()
		if n < 0 || n > 1 || n == len(args) || args[n] == "" {
			return usagef("use targets exec [ID] -- COMMAND [ARG...]")
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if a.jsonOutput {
			return usagef("targets exec does not support --json; stdout and stderr belong to the command")
		}
		n := cmd.ArgsLenAtDash()
		id := ""
		if n == 1 {
			id = args[0]
		}
		t, err := a.environmentTarget(id)
		if err != nil {
			return err
		}
		if err := connection.ValidateExperimentTarget(t); err != nil {
			return usageError{err}
		}
		inherited := os.Environ()
		// Validate before any SSH operation, retaining an original snapshot for
		// self/cyclic source references and consistent child construction.
		if _, err := connection.ExecClientEnvironment(t, t.TrackingURI, inherited); err != nil {
			return usageError{err}
		}
		if err := connection.CheckClientCredentialFile(t); err != nil {
			return usageError{err}
		}
		run := func(ctx context.Context, endpoint string) error {
			env, err := connection.ExecClientEnvironment(t, endpoint, inherited)
			if err != nil {
				return usageError{err}
			}
			code, err := platform.RunAttached(ctx, args[n:], env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if code != 0 {
				return &ChildExitError{Code: code}
			}
			return nil
		}
		if t.SSHHost == "" {
			return run(cmd.Context(), t.TrackingURI)
		}
		return a.withSession(cmd.Context(), t, func(s *core.Session) error {
			if s.Local {
				return usagef("experiment commands cannot use a readonly local-store session")
			}
			return run(cmd.Context(), s.BaseURL)
		})
	}
	return cmd
}
