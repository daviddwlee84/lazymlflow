package localstate

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

var _ core.RunPinStore = (*Store)(nil)

func runPinPrefix(source string) string {
	return "run-pin/v1/" + base64.RawURLEncoding.EncodeToString([]byte(source)) + "/"
}

func runPinKey(source, runID string) string {
	return runPinPrefix(source) + base64.RawURLEncoding.EncodeToString([]byte(runID))
}

func decodeRunPin(raw, source, key string) (core.RunPin, error) {
	var pin core.RunPin
	if err := json.Unmarshal([]byte(raw), &pin); err != nil {
		return pin, fmt.Errorf("invalid saved run pin (database preserved): %w", err)
	}
	if err := core.ValidateRunPin(pin); err != nil {
		return pin, fmt.Errorf("invalid saved run pin (database preserved): %w", err)
	}
	if runPinKey(source, pin.RunID) != key {
		return pin, errors.New("saved run pin identity does not match its local key (database preserved)")
	}
	return pin, nil
}

func (s *Store) LoadRunPins(ctx context.Context, source string) ([]core.RunPin, error) {
	result := []core.RunPin{}
	if strings.TrimSpace(source) == "" {
		return result, errors.New("run pin source is required")
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return result, err
	}
	prefix := runPinPrefix(source)
	// The slash-delimited base64 namespace has an exact lexicographic range;
	// unlike LIKE it cannot interpret '_' or '%' in an identity as wildcards.
	end := prefix[:len(prefix)-1] + "0"
	err = retry(ctx, func() error {
		rows, err := db.QueryContext(ctx, `SELECT key,value FROM preferences WHERE key>=? AND key<?`, prefix, end)
		if err != nil {
			return err
		}
		defer rows.Close()
		pins := []core.RunPin{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return err
			}
			pin, err := decodeRunPin(value, source, key)
			if err != nil {
				return err
			}
			pins = append(pins, pin)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		result = pins
		return nil
	})
	if err != nil {
		return []core.RunPin{}, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PinnedAt != result[j].PinnedAt {
			return result[i].PinnedAt > result[j].PinnedAt
		}
		return result[i].RunID < result[j].RunID
	})
	return result, nil
}

func (s *Store) SaveRunPin(ctx context.Context, source string, pin core.RunPin) error {
	if err := identity(source, pin.RunID); err != nil {
		return err
	}
	if err := core.ValidateRunPin(pin); err != nil {
		return err
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return err
	}
	key := runPinKey(source, pin.RunID)
	return retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT value FROM preferences WHERE key=?`, key).Scan(&raw)
		saved := pin
		if err == nil {
			previous, err := decodeRunPin(raw, source, key)
			if err != nil {
				return err
			}
			saved.PinnedAt = previous.PinnedAt
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if saved.PinnedAt == 0 {
			saved.PinnedAt = time.Now().UnixMilli()
		}
		value, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(value)); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) DeleteRunPin(ctx context.Context, source, runID string) error {
	if err := identity(source, runID); err != nil {
		return err
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return err
	}
	return retry(ctx, func() error {
		_, err := db.ExecContext(ctx, `DELETE FROM preferences WHERE key=?`, runPinKey(source, runID))
		return err
	})
}
