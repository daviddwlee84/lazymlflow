package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Subject is a local journal identity. Label is presentation only: changing a
// run or experiment name must not disconnect its notes.
type Subject struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
}

func (s Subject) Validate() error {
	if strings.TrimSpace(s.Source) == "" || strings.TrimSpace(s.ID) == "" {
		return errors.New("note source and subject identity are required")
	}
	switch s.Kind {
	case "run", "experiment", "dataset":
		return nil
	default:
		return fmt.Errorf("invalid note subject kind %q: use run, experiment, or dataset", s.Kind)
	}
}

type Note struct {
	ID        string  `json:"id"`
	Subject   Subject `json:"subject"`
	Body      string  `json:"body"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
	DeletedAt int64   `json:"deleted_at,omitempty"`
	Revision  int     `json:"revision"`
}

var ErrNoteConflict = errors.New("note changed; reload its current revision before saving")
var ErrNoteNotFound = errors.New("note not found for this subject")

// NoteStore is separate from StateStore so old preference stores and test
// doubles remain compatible. expectedRevision=0 creates; edits compare and swap.
type NoteStore interface {
	ListNotes(context.Context, Subject, bool) ([]Note, error)
	SaveNote(context.Context, Note, int) (Note, error)
	DeleteNote(context.Context, Subject, string, int, bool) (Note, error)
}
