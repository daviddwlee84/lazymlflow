package scoopupgrade

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type CommandOptions struct {
	Before     func(*cobra.Command) error
	Promptless bool
	WriteJSON  func(*cobra.Command, Report) error
	UsageError func(string) error
	Prepare    func(context.Context) (Plan, error)
}

// Wrap adds Scoop dispatch ahead of the existing installation-specific updater.
// Non-Scoop installations keep their original implementation and policy.
func Wrap(command *cobra.Command, product Product, options CommandOptions) *cobra.Command {
	previous := command.RunE
	command.Long = strings.TrimSpace(command.Long + "\n\nWindows Scoop upgrades exit this process and finish in a separate helper. --check is read-only. During an active update, use the returned status_command to query without starting the package Scoop is replacing; afterwards upgrade --status OPERATION also reads the result. Only this CLI is updated, not its backend or data.")
	var status string
	command.Flags().StringVar(&status, "status", "", "Read a previous Scoop upgrade operation without making changes")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if options.Before != nil {
			if err := options.Before(cmd); err != nil {
				return err
			}
		}
		isJSON, _ := cmd.Flags().GetBool("json")
		write := func(report Report) error {
			if isJSON {
				if options.WriteJSON != nil {
					return options.WriteJSON(cmd, report)
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			return WriteHuman(cmd.OutOrStdout(), report)
		}
		usage := func(message string) error {
			if options.UsageError != nil {
				return options.UsageError(message)
			}
			return errors.New(message)
		}
		if status != "" {
			for _, name := range []string{"check", "force", "yes", "dry-run"} {
				if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
					return usage("--status cannot be combined with --" + name)
				}
			}
			report, err := Status(product, status)
			if err != nil {
				return err
			}
			return write(report)
		}
		prepare := options.Prepare
		if prepare == nil {
			prepare = func(ctx context.Context) (Plan, error) {
				exe, err := os.Executable()
				if err != nil {
					return Plan{}, err
				}
				return Prepare(ctx, exe, product, Options{})
			}
		}
		plan, err := prepare(cmd.Context())
		if errors.Is(err, ErrNotManaged) {
			if previous == nil {
				return errors.New("this installation has no supported upgrade owner")
			}
			return previous(cmd, args)
		}
		if err != nil {
			return err
		}
		check, _ := cmd.Flags().GetBool("check")
		dry, _ := cmd.Flags().GetBool("dry-run")
		if check || dry {
			return write(plan.Check())
		}
		yes, _ := cmd.Flags().GetBool("yes")
		interactive := IsTerminal(cmd)
		if !options.Promptless && !yes {
			if isJSON || !interactive {
				return usage("upgrade requires confirmation; inspect with --check, then pass --yes")
			}
			if err := Confirm(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), "Exit this process and let Scoop update "+plan.Bucket+"/"+plan.Package+" in a separate progress window?"); err != nil {
				return err
			}
		}
		report, err := plan.Handoff(cmd.Context(), interactive && !isJSON)
		if err != nil {
			return err
		}
		return write(report)
	}
	return command
}

func IsTerminal(cmd *cobra.Command) bool {
	in, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(in.Fd())) {
		return false
	}
	out, ok := cmd.OutOrStdout().(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(out.Fd()))
}
func Confirm(ctx context.Context, in io.Reader, out io.Writer, message string) error {
	if _, err := fmt.Fprint(out, message+" [y/N] "); err != nil {
		return err
	}
	type answer struct {
		line string
		err  error
	}
	done := make(chan answer, 1)
	go func() { line, err := bufio.NewReader(in).ReadString('\n'); done <- answer{line, err} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a := <-done:
		if a.err != nil && a.err != io.EOF {
			return a.err
		}
		if value := strings.ToLower(strings.TrimSpace(a.line)); value != "y" && value != "yes" {
			return errors.New("upgrade declined; nothing changed")
		}
		return nil
	}
}
func WriteHuman(out io.Writer, r Report) error {
	if r.Status == "checked" {
		_, err := fmt.Fprintf(out, "Owner: Scoop (%s/%s)\nVersion: %s\nExecutable: %s\nCommand: %s\nApply exits this tool, then updates in an independent helper.\n", r.Bucket, r.Package, r.CurrentVersion, r.Path, displayCommand(r.Command))
		return err
	}
	_, err := fmt.Fprintf(out, "Upgrade: %s\n", r.Status)
	if err != nil {
		return err
	}
	if r.Status == "handed-off" {
		fmt.Fprintln(out, "The update is scheduled, not complete. This process now exits so Scoop can update it.")
	}
	if r.OperationID != "" {
		fmt.Fprintln(out, "Operation:", r.OperationID)
		fmt.Fprintln(out, "Inspect without starting the package being updated:", displayCommand(r.StatusCommand))
	}
	if r.Version != "" {
		fmt.Fprintln(out, "Installed version:", r.Version)
	}
	if r.Reason != "" {
		fmt.Fprintln(out, r.Reason)
	}
	if r.ResultPath != "" {
		fmt.Fprintln(out, "Result:", r.ResultPath)
		fmt.Fprintln(out, "Log:", r.LogPath)
	}
	return nil
}
func displayCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return "& " + strings.Join(parts, " ")
}
