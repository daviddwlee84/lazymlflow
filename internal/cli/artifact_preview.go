package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/artifactpreview"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
	"github.com/spf13/cobra"
)

func (a *app) artifactPreviewCommand() *cobra.Command {
	var maxBytes int64
	var allowLarge, pager bool
	cmd := &cobra.Command{
		Use: "preview RUN_ID PATH", Short: "Preview a bounded text prefix without saving the full artifact", Args: exactArgs(2),
		Long: "Preview text, formatted JSON, or CSV using a bounded byte prefix. Large files\nand unknown sizes require confirmation or --allow-large. Older MLflow proxy\nservers may fetch a whole remote object before returning that prefix.\n--pager views the same bounded preview, never the full artifact.\nUse artifacts download when the complete file is required.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return usagef("run ID and artifact path cannot be empty")
			}
			if cmd.Flags().Changed("max-bytes") && (maxBytes < 1 || maxBytes > core.MaxPreviewBytes) {
				return usagef("--max-bytes must be between 1 and %d", core.MaxPreviewBytes)
			}
			if pager && (a.jsonOutput || !a.options.IsTerminal()) {
				return usagef("--pager requires an input/output terminal and cannot be combined with --json")
			}
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("max-bytes") {
				maxBytes = cfg.TUI.PreviewMaxBytes
			}
			if maxBytes == 0 {
				maxBytes = core.DefaultPreviewBytes
			}
			if maxBytes < 1 || maxBytes > core.MaxPreviewBytes {
				return usagef("preview_max_bytes must be between 1 and %d", core.MaxPreviewBytes)
			}
			target, err := config.Resolve(cfg, a.targetID, a.trackingURI)
			if err != nil {
				return usageError{err}
			}
			options := artifactpreview.Options{MaxBytes: maxBytes, AllowLarge: allowLarge, AllowUnknown: allowLarge, AllowWholeFetch: allowLarge}
			return a.withSession(cmd.Context(), target, func(session *core.Session) error {
				document, err := artifactpreview.Read(cmd.Context(), session.Backend, args[0], args[1], options)
				var approval *core.PreviewApprovalError
				if errors.As(err, &approval) {
					if a.jsonOutput || !a.options.IsTerminal() {
						hint := "pass --allow-large to permit the bounded preview"
						if approval.WholeFetch || approval.Info.MayDownloadWhole {
							hint += " (the server may fetch the whole object)"
						}
						return usagef("%s; %s", approval.Error(), hint)
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "%s\nAt most %d bytes will be displayed. Continue? [y/N] ", cell(approval.Error()), maxBytes)
					if err := confirmArtifactPreview(cmd.Context(), cmd.InOrStdin()); err != nil {
						return err
					}
					options.AllowLarge, options.AllowUnknown, options.AllowWholeFetch = true, true, true
					document, err = artifactpreview.Read(cmd.Context(), session.Backend, args[0], args[1], options)
				}
				if err != nil {
					return err
				}
				if a.jsonOutput {
					return a.output(cmd, document)
				}
				if document.Binary {
					return fmt.Errorf("artifact %q is binary and cannot be previewed; use artifacts download", args[1])
				}
				if document.Warning != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), cell(document.Warning))
				}
				if document.Truncated {
					fmt.Fprintln(cmd.ErrOrStderr(), "Preview shows a bounded prefix; artifacts download retrieves the complete file.")
				}
				if !pager {
					_, err = io.WriteString(cmd.OutOrStdout(), document.Text)
					if err == nil && !strings.HasSuffix(document.Text, "\n") {
						_, err = io.WriteString(cmd.OutOrStdout(), "\n")
					}
					return err
				}
				file, err := artifactpreview.PreparePager(document)
				if err != nil {
					return err
				}
				defer file.Cleanup()
				argv, err := artifactpreview.PagerCommand(file)
				if err != nil {
					return err
				}
				code, err := platform.RunAttached(cmd.Context(), argv, os.Environ(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				if code != 0 {
					return &ChildExitError{Code: code}
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", core.DefaultPreviewBytes, "Maximum preview bytes (default from tui.preview_max_bytes; hard limit 64 MiB)")
	cmd.Flags().BoolVar(&allowLarge, "allow-large", false, "Allow large/unknown artifacts and possible whole-object proxy fetch; display remains bounded")
	cmd.Flags().BoolVar(&pager, "pager", false, "View the same bounded preview with bat/PAGER/less/more (terminal only)")
	return cmd
}

func confirmArtifactPreview(ctx context.Context, input io.Reader) error {
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() { line, err := bufio.NewReader(input).ReadString('\n'); answers <- answer{line, err} }()
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
		return fmt.Errorf("preview cancelled; no artifact bytes were read: %w", context.Canceled)
	}
}
