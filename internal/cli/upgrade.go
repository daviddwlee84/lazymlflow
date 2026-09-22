package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/brewupgrade"
	"github.com/spf13/cobra"
)

type upgradeReport struct {
	Status         string   `json:"status"`
	Manager        string   `json:"manager,omitempty"`
	Formula        string   `json:"formula,omitempty"`
	Path           string   `json:"path,omitempty"`
	ResolvedPath   string   `json:"resolved_path,omitempty"`
	CurrentVersion string   `json:"current_version,omitempty"`
	Version        string   `json:"version,omitempty"`
	Command        []string `json:"command,omitempty"`
	CanUpgrade     bool     `json:"can_upgrade"`
	Changed        bool     `json:"changed"`
	Reason         string   `json:"reason,omitempty"`
}

type managedUpgradePlan struct {
	report upgradeReport
	apply  func(context.Context, io.Writer) (brewupgrade.Outcome, error)
}

type upgradeOptions struct {
	json       *bool
	isTerminal func(*cobra.Command) bool
	prepare    func(context.Context) (managedUpgradePlan, error)
}

func newUpgradeCommand(options upgradeOptions) *cobra.Command {
	var check, yes, localJSON bool
	jsonMode := options.json
	if jsonMode == nil {
		jsonMode = &localJSON
	}
	prepare := options.prepare
	if prepare == nil {
		prepare = prepareManagedUpgrade
	}
	cmd := &cobra.Command{
		Use: "upgrade", Short: "Upgrade this lazymlflow installation through its verified Homebrew owner",
		Args: noArgs,
		Long: "Inspect the running executable and upgrade its verified Homebrew formula. Other installation methods receive instructions and are never overwritten. This command does not load MLflow configuration.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Context().Err(); err != nil {
				return err
			}
			plan, err := prepare(cmd.Context())
			if err != nil {
				return err
			}
			writeReport := func() error {
				if *jsonMode {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(plan.report)
				}
				if !plan.report.CanUpgrade {
					_, err := fmt.Fprintln(cmd.OutOrStdout(), plan.report.Reason)
					return err
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Installation: %s\nVersion: %s\nOwner: Homebrew (%s)\nCommand: %s\n", strconv.Quote(plan.report.ResolvedPath), plan.report.CurrentVersion, plan.report.Formula, displayUpgradeCommand(plan.report.Command))
				return err
			}
			if check {
				return writeReport()
			}
			if !plan.report.CanUpgrade || plan.apply == nil {
				if *jsonMode {
					if err := writeReport(); err != nil {
						return err
					}
				}
				return errors.New(plan.report.Reason)
			}
			if !yes {
				if *jsonMode || options.isTerminal == nil || !options.isTerminal(cmd) {
					return usagef("upgrade requires confirmation; inspect with --check, then pass --yes")
				}
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Run %s? [y/N] ", displayUpgradeCommand(plan.report.Command)); err != nil {
					return err
				}
				if err := confirmManagedUpgrade(cmd.Context(), cmd.InOrStdin()); err != nil {
					return err
				}
			}
			progress := cmd.ErrOrStderr()
			if *jsonMode {
				progress = io.Discard
			}
			outcome, applyErr := plan.apply(cmd.Context(), progress)
			plan.report.Status = "up-to-date"
			if outcome.Changed {
				plan.report.Status = "updated"
			}
			plan.report.Changed = outcome.Changed
			plan.report.Version = outcome.Version
			if outcome.Path != "" {
				plan.report.Path = outcome.Path
			}
			if outcome.ResolvedPath != "" {
				plan.report.ResolvedPath = outcome.ResolvedPath
			}
			if applyErr != nil {
				plan.report.Status = "failed"
			}
			if *jsonMode {
				if err := writeReport(); err != nil {
					return err
				}
			} else if applyErr == nil {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Homebrew %s: lazymlflow %s (%s). Start a new invocation to use the installed executable.\n", plan.report.Status, outcome.Version, strconv.Quote(outcome.Path))
				if err != nil {
					return err
				}
			}
			return applyErr
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Inspect ownership and the exact upgrade command without changing files")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Approve upgrading the inspected Homebrew formula")
	if options.json == nil {
		cmd.Flags().BoolVar(jsonMode, "json", false, "Write structured JSON without prompting")
	}
	return cmd
}

func prepareManagedUpgrade(ctx context.Context) (managedUpgradePlan, error) {
	executable, err := os.Executable()
	if err != nil {
		return managedUpgradePlan{}, fmt.Errorf("locate running executable: %w", err)
	}
	plan, err := brewupgrade.Prepare(ctx, executable, "lazymlflow", brewupgrade.Options{Inspect: inspectUpgradeBinary})
	if errors.Is(err, brewupgrade.ErrNotManaged) {
		return managedUpgradePlan{report: upgradeReport{
			Status: "unsupported", Path: executable,
			Reason: unsupportedUpgradeInstructions(executable),
		}}, nil
	}
	if err != nil {
		return managedUpgradePlan{}, err
	}
	return managedUpgradePlan{
		report: upgradeReport{
			Status: "checked", Manager: "homebrew", Formula: plan.Formula,
			Path: plan.StablePath, ResolvedPath: plan.CurrentPath,
			CurrentVersion: plan.CurrentVersion, Command: plan.Command(), CanUpgrade: true,
		},
		apply: plan.Apply,
	}, nil
}

func unsupportedUpgradeInstructions(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	path := filepath.ToSlash(executable)
	switch {
	case strings.HasPrefix(path, "/nix/store/"):
		return "Nix owns this installation; update the Nix profile or configuration that installed lazymlflow."
	case strings.Contains(path, "/mise/installs/"):
		return "This installation is managed by mise; run mise upgrade for its registered lazymlflow tool."
	default:
		return "Automatic upgrade requires a verified Homebrew installation. Use the manager that installed this executable. For a manual Go installation, run go install github.com/daviddwlee84/lazymlflow/cmd/lazymlflow@latest; for a standalone archive, download and verify a release from https://github.com/daviddwlee84/lazymlflow/releases."
	}
}

func displayUpgradeCommand(command []string) string {
	quoted := make([]string, len(command))
	for i, argument := range command {
		quoted[i] = strconv.Quote(argument)
	}
	return strings.Join(quoted, " ")
}

func confirmManagedUpgrade(ctx context.Context, input io.Reader) error {
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(input).ReadString('\n')
		answers <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case reply := <-answers:
		if reply.err != nil && !errors.Is(reply.err, io.EOF) {
			return reply.err
		}
		if value := strings.TrimSpace(reply.line); strings.EqualFold(value, "y") || strings.EqualFold(value, "yes") {
			return nil
		}
		return fmt.Errorf("cancelled; no upgrade was started: %w", context.Canceled)
	}
}
