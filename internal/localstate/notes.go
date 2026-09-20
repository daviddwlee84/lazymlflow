package localstate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

var _ core.NoteStore = (*Store)(nil)

type rowScanner interface{ Scan(...any) error }

func scanNote(row rowScanner) (core.Note, error) {
	var n core.Note
	err := row.Scan(&n.ID, &n.Subject.Source, &n.Subject.Kind, &n.Subject.ID, &n.Subject.Label, &n.Body, &n.CreatedAt, &n.UpdatedAt, &n.DeletedAt, &n.Revision)
	return n, err
}

const noteColumns = `id,source,kind,subject_id,label,body,created_at,updated_at,deleted_at,revision`

func (s *Store) ListNotes(ctx context.Context, subject core.Subject, includeDeleted bool) ([]core.Note, error) {
	result := []core.Note{}
	if err := subject.Validate(); err != nil {
		return result, err
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return result, err
	}
	query := `SELECT ` + noteColumns + ` FROM notes WHERE source=? AND kind=? AND subject_id=?`
	if !includeDeleted {
		query += ` AND deleted_at=0`
	}
	query += ` ORDER BY created_at DESC,id ASC`
	err = retry(ctx, func() error {
		rows, err := db.QueryContext(ctx, query, subject.Source, subject.Kind, subject.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		result = []core.Note{}
		for rows.Next() {
			n, err := scanNote(rows)
			if err != nil {
				return err
			}
			result = append(result, n)
		}
		return rows.Err()
	})
	return result, err
}

func newNoteID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (s *Store) SaveNote(ctx context.Context, note core.Note, expectedRevision int) (core.Note, error) {
	if err := note.Subject.Validate(); err != nil {
		return core.Note{}, err
	}
	if strings.TrimSpace(note.Body) == "" {
		return core.Note{}, errors.New("note body cannot be empty")
	}
	if expectedRevision < 0 {
		return core.Note{}, errors.New("note revision cannot be negative")
	}
	if expectedRevision > 0 && note.ID == "" {
		return core.Note{}, errors.New("note ID is required for editing")
	}
	if note.ID == "" {
		var err error
		note.ID, err = newNoteID()
		if err != nil {
			return core.Note{}, err
		}
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return core.Note{}, err
	}
	var result core.Note
	err = retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		existing, e := scanNote(tx.QueryRowContext(ctx, `SELECT `+noteColumns+` FROM notes WHERE id=?`, note.ID))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now := time.Now().UnixMilli()
		if expectedRevision == 0 {
			if e == nil {
				return core.ErrNoteConflict
			}
			result = note
			result.CreatedAt = now
			result.UpdatedAt = now
			result.DeletedAt = 0
			result.Revision = 1
			_, err = tx.ExecContext(ctx, `INSERT INTO notes (`+noteColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`, result.ID, result.Subject.Source, result.Subject.Kind, result.Subject.ID, result.Subject.Label, result.Body, result.CreatedAt, result.UpdatedAt, 0, 1)
		} else {
			if errors.Is(e, sql.ErrNoRows) || existing.Subject.Source != note.Subject.Source || existing.Subject.Kind != note.Subject.Kind || existing.Subject.ID != note.Subject.ID {
				return core.ErrNoteNotFound
			}
			if existing.Revision != expectedRevision || existing.DeletedAt != 0 {
				return core.ErrNoteConflict
			}
			result = existing
			result.Body = note.Body
			result.Subject.Label = note.Subject.Label
			result.UpdatedAt = now
			result.Revision++
			_, err = tx.ExecContext(ctx, `UPDATE notes SET label=?,body=?,updated_at=?,revision=? WHERE id=? AND revision=?`, result.Subject.Label, result.Body, result.UpdatedAt, result.Revision, result.ID, expectedRevision)
		}
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return core.Note{}, err
	}
	return result, nil
}

func (s *Store) DeleteNote(ctx context.Context, subject core.Subject, id string, expectedRevision int, restore bool) (core.Note, error) {
	if err := subject.Validate(); err != nil {
		return core.Note{}, err
	}
	if strings.TrimSpace(id) == "" || expectedRevision < 1 {
		return core.Note{}, errors.New("note ID and positive revision are required")
	}
	db, err := s.open(ctx, false)
	if err != nil {
		return core.Note{}, err
	}
	if db == nil {
		return core.Note{}, core.ErrNoteNotFound
	}
	var result core.Note
	err = retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err = scanNote(tx.QueryRowContext(ctx, `SELECT `+noteColumns+` FROM notes WHERE id=? AND source=? AND kind=? AND subject_id=?`, id, subject.Source, subject.Kind, subject.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return core.ErrNoteNotFound
		}
		if err != nil {
			return err
		}
		if result.Revision != expectedRevision {
			return core.ErrNoteConflict
		}
		if (restore && result.DeletedAt == 0) || (!restore && result.DeletedAt != 0) {
			return nil
		}
		result.UpdatedAt = time.Now().UnixMilli()
		result.Revision++
		result.DeletedAt = result.UpdatedAt
		if restore {
			result.DeletedAt = 0
		}
		if _, err = tx.ExecContext(ctx, `UPDATE notes SET deleted_at=?,updated_at=?,revision=? WHERE id=? AND revision=?`, result.DeletedAt, result.UpdatedAt, result.Revision, id, expectedRevision); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return core.Note{}, err
	}
	return result, nil
}
