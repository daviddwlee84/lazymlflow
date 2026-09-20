package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

type noteSubjectFlags struct {
	run, experiment, dataset, datasetRun, label string
	index                                       int
}

func (f *noteSubjectFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.run, "run", "", "Run ID (local subject, no server connection)")
	cmd.Flags().StringVar(&f.experiment, "experiment", "", "Experiment ID (local subject)")
	cmd.Flags().StringVar(&f.dataset, "dataset", "", "Dataset variant identity from datasets list/schema (local subject)")
	cmd.Flags().StringVar(&f.datasetRun, "dataset-run", "", "Resolve dataset identity from this run (connects to the server)")
	cmd.Flags().IntVar(&f.index, "index", 1, "Dataset input number with --dataset-run, starting at 1")
	cmd.Flags().StringVar(&f.label, "label", "", "Optional human-readable subject label")
}
func (f noteSubjectFlags) validate(cmd *cobra.Command) error {
	count := 0
	for _, name := range []string{"run", "experiment", "dataset", "dataset-run"} {
		if cmd.Flags().Changed(name) {
			value, _ := cmd.Flags().GetString(name)
			if strings.TrimSpace(value) == "" {
				return usagef("--%s cannot be empty", name)
			}
			count++
		}
	}
	if count != 1 {
		return usagef("specify one subject: --run ID, --experiment ID, --dataset ID, or --dataset-run RUN_ID")
	}
	if f.index < 1 {
		return usagef("--index must be positive")
	}
	if cmd.Flags().Changed("index") && f.datasetRun == "" {
		return usagef("--index requires --dataset-run")
	}
	return nil
}
func (a *app) noteSubject(cmd *cobra.Command, f noteSubjectFlags) (core.Subject, error) {
	target, err := a.resolve()
	if err != nil {
		return core.Subject{}, err
	}
	subject := core.Subject{Source: core.SourceKey(target), Label: f.label}
	switch {
	case f.run != "":
		subject.Kind = "run"
		subject.ID = f.run
	case f.experiment != "":
		subject.Kind = "experiment"
		subject.ID = f.experiment
	case f.dataset != "":
		subject.Kind = "dataset"
		subject.ID = f.dataset
	default:
		err = a.withSession(cmd.Context(), target, func(s *core.Session) error {
			r, err := s.Backend.GetRun(cmd.Context(), f.datasetRun)
			if err != nil {
				return err
			}
			input, err := datasetInput(r, f.index)
			if err != nil {
				return err
			}
			subject.Kind = "dataset"
			subject.ID = core.DatasetIdentity(subject.Source, input.Dataset)
			if subject.Label == "" {
				subject.Label = input.Dataset.Name
			}
			return nil
		})
	}
	return subject, err
}
func (a *app) withNotes(cmd *cobra.Command, subject core.Subject, fn func(core.NoteStore) error) error {
	state := a.state()
	defer state.Close()
	notes, ok := state.(core.NoteStore)
	if !ok {
		return errors.New("the configured local state store does not support notes")
	}
	return fn(notes)
}

func (a *app) notesCommand() *cobra.Command {
	group := a.group("notes", "Manage local Markdown journal notes without modifying MLflow")
	var subject noteSubjectFlags
	var includeDeleted bool
	var search string
	list := &cobra.Command{Use: "list (--run ID | --experiment ID | --dataset ID)", Short: "List a subject's notes, newest first", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := subject.validate(cmd); err != nil {
			return err
		}
		sub, err := a.noteSubject(cmd, subject)
		if err != nil {
			return err
		}
		return a.withNotes(cmd, sub, func(store core.NoteStore) error {
			notes, err := store.ListNotes(cmd.Context(), sub, includeDeleted)
			if err != nil {
				return err
			}
			filtered := []core.Note{}
			needle := strings.ToLower(search)
			for _, n := range notes {
				if needle == "" || strings.Contains(strings.ToLower(n.Body+"\n"+n.Subject.Label), needle) {
					filtered = append(filtered, n)
				}
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"subject": sub, "notes": filtered, "local_only": true})
			}
			for i, n := range filtered {
				if i > 0 {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				if err := a.printNote(cmd, n); err != nil {
					return err
				}
			}
			if len(filtered) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No local notes.")
			}
			return err
		})
	}}
	subject.bind(list)
	list.Flags().BoolVar(&includeDeleted, "include-deleted", false, "Include notes that can be restored")
	list.Flags().StringVar(&search, "search", "", "Search note text and subject labels")
	group.AddCommand(list, a.noteSaveCommand(false), a.noteSaveCommand(true))
	for _, restore := range []bool{false, true} {
		verb := "delete"
		if restore {
			verb = "restore"
		}
		var flags noteSubjectFlags
		var revision int
		cmd := &cobra.Command{Use: verb + " NOTE_ID", Short: strings.ToUpper(verb[:1]) + verb[1:] + " a local note using its current revision", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.validate(cmd); err != nil {
				return err
			}
			if revision < 1 {
				return usagef("--revision is required and must be positive; inspect notes list --json first")
			}
			sub, err := a.noteSubject(cmd, flags)
			if err != nil {
				return err
			}
			return a.withNotes(cmd, sub, func(store core.NoteStore) error {
				note, err := store.DeleteNote(cmd.Context(), sub, args[0], revision, restore)
				if err != nil {
					return err
				}
				return a.printNote(cmd, note)
			})
		}}
		flags.bind(cmd)
		cmd.Flags().IntVar(&revision, "revision", 0, "Expected note revision (required; prevents overwriting newer changes)")
		group.AddCommand(cmd)
	}
	return group
}

func (a *app) noteSaveCommand(edit bool) *cobra.Command {
	verb := "add"
	args := noArgs
	use := "add"
	if edit {
		verb = "edit"
		use = "edit NOTE_ID"
		args = exactArgs(1)
	}
	var flags noteSubjectFlags
	var body, file string
	var revision int
	cmd := &cobra.Command{Use: use, Short: strings.ToUpper(verb[:1]) + verb[1:] + " a local Markdown note", Args: args, Example: "  lazymlflow notes add --run RUN_ID --file - < observation.md\n  lazymlflow notes edit NOTE_ID --run RUN_ID --revision 1 --body 'Updated observation'", RunE: func(cmd *cobra.Command, args []string) error {
		if err := flags.validate(cmd); err != nil {
			return err
		}
		if cmd.Flags().Changed("body") == cmd.Flags().Changed("file") {
			return usagef("specify exactly one of --body TEXT or --file PATH (use - for stdin)")
		}
		if edit && revision < 1 {
			return usagef("--revision is required and must be positive; inspect notes list --json first")
		}
		text := body
		if cmd.Flags().Changed("file") {
			if file == "" {
				return usagef("--file cannot be empty")
			}
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(cmd.InOrStdin())
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return err
			}
			text = string(data)
		}
		if strings.TrimSpace(text) == "" {
			return usagef("note body cannot be empty")
		}
		sub, err := a.noteSubject(cmd, flags)
		if err != nil {
			return err
		}
		return a.withNotes(cmd, sub, func(store core.NoteStore) error {
			n := core.Note{Subject: sub, Body: text}
			if edit {
				n.ID = args[0]
			}
			saved, err := store.SaveNote(cmd.Context(), n, revision)
			if err != nil {
				return err
			}
			return a.printNote(cmd, saved)
		})
	}}
	flags.bind(cmd)
	cmd.Flags().StringVar(&body, "body", "", "Markdown note body")
	cmd.Flags().StringVar(&file, "file", "", "Read note body from a file, or - for stdin")
	if edit {
		cmd.Flags().IntVar(&revision, "revision", 0, "Expected note revision (required)")
	}
	return cmd
}
func (a *app) printNote(cmd *cobra.Command, note core.Note) error {
	if a.jsonOutput {
		return a.output(cmd, note)
	}
	deleted := ""
	if note.DeletedAt != 0 {
		deleted = " [deleted]"
	}
	lines := strings.Split(note.Body, "\n")
	for i := range lines {
		lines[i] = cell(lines[i])
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s · revision %d · %s%s\n%s\n", note.ID, note.Revision, timestamp(note.UpdatedAt), deleted, strings.Join(lines, "\n"))
	return err
}
