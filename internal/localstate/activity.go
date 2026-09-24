package localstate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

var _ core.ActivityStore = (*Store)(nil)

type activityQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadActivity(ctx context.Context, db activityQuerier, source string) (core.ActivitySnapshot, error) {
	snapshot := core.NewActivitySnapshot(source)
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM activity_sources WHERE source=?`, source).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			return snapshot, fmt.Errorf("invalid saved activity source: %w", err)
		}
	}
	snapshot.Source = source
	snapshot.Records = map[string]core.ActivityRecord{}
	snapshot.Counts = map[string]core.ExperimentActivityCounts{}
	rows, err := db.QueryContext(ctx, `SELECT run_id,value FROM activity_records WHERE source=?`, source)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var id, value string
		if err = rows.Scan(&id, &value); err != nil {
			break
		}
		var record core.ActivityRecord
		if err = json.Unmarshal([]byte(value), &record); err != nil {
			break
		}
		snapshot.Records[id] = record
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return snapshot, fmt.Errorf("read activity records: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT experiment,value FROM activity_count_cache WHERE source=?`, source)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var id, value string
		if err = rows.Scan(&id, &value); err != nil {
			break
		}
		var count core.ExperimentActivityCounts
		if err = json.Unmarshal([]byte(value), &count); err != nil {
			break
		}
		count.ExperimentID, count.Statuses = id, map[string]int{}
		count.Total, count.Running, count.Finished, count.Failed, count.Killed, count.Other, count.Unread, count.Alerts = 0, 0, 0, 0, 0, 0, 0, 0
		snapshot.Counts[id] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return snapshot, fmt.Errorf("read activity counts: %w", err)
	}
	for _, e := range snapshot.Experiments {
		if _, ok := snapshot.Counts[e.ID]; !ok {
			snapshot.Counts[e.ID] = core.ExperimentActivityCounts{ExperimentID: e.ID, Statuses: map[string]int{}}
		}
	}
	rows, err = db.QueryContext(ctx, `SELECT experiment,status,COUNT(*) FROM activity_run_cache WHERE source=? GROUP BY experiment,status`, source)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var id, status string
		var n int
		if err = rows.Scan(&id, &status, &n); err != nil {
			break
		}
		count := snapshot.Counts[id]
		count.ExperimentID = id
		if count.Statuses == nil {
			count.Statuses = map[string]int{}
		}
		count.Statuses[status] = n
		count.Total += n
		switch status {
		case "RUNNING":
			count.Running += n
		case "FINISHED":
			count.Finished += n
		case "FAILED":
			count.Failed += n
		case "KILLED":
			count.Killed += n
		default:
			count.Other += n
		}
		snapshot.Counts[id] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return snapshot, err
	}
	activeExperiments := map[string]bool{}
	for _, e := range snapshot.Experiments {
		activeExperiments[e.ID] = true
	}
	for _, record := range snapshot.Records {
		if record.LifecycleStage == "deleted" || snapshot.Initialized && !activeExperiments[record.ExperimentID] {
			continue
		}
		count := snapshot.Counts[record.ExperimentID]
		count.ExperimentID = record.ExperimentID
		if count.Statuses == nil {
			count.Statuses = map[string]int{}
		}
		if record.Unread() {
			count.Unread++
		}
		if record.HasAlerts() {
			count.Alerts++
		}
		snapshot.Counts[record.ExperimentID] = count
	}
	return snapshot, nil
}

func (s *Store) LoadActivity(ctx context.Context, source string) (core.ActivitySnapshot, error) {
	snapshot := core.NewActivitySnapshot(source)
	if strings.TrimSpace(source) == "" {
		return snapshot, errors.New("activity source is required")
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return snapshot, err
	}
	err = retry(ctx, func() error {
		tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return e
		}
		defer tx.Rollback()
		snapshot, e = loadActivity(ctx, tx, source)
		if e != nil {
			return e
		}
		return tx.Commit()
	})
	return snapshot, err
}

func saveActivityRecord(ctx context.Context, tx *sql.Tx, source string, record core.ActivityRecord) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activity_records(source,run_id,value) VALUES(?,?,?) ON CONFLICT(source,run_id) DO UPDATE SET value=excluded.value`, source, record.RunID, string(b))
	return err
}

func saveActivitySource(ctx context.Context, tx *sql.Tx, snapshot core.ActivitySnapshot) error {
	snapshot.Records, snapshot.Counts = nil, nil
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activity_sources(source,value) VALUES(?,?) ON CONFLICT(source) DO UPDATE SET value=excluded.value`, snapshot.Source, string(b))
	return err
}

func (s *Store) ObserveActivity(ctx context.Context, source string, batch core.ActivityBatch) (core.ActivitySnapshot, error) {
	result := core.NewActivitySnapshot(source)
	if strings.TrimSpace(source) == "" {
		return result, errors.New("activity source is required")
	}
	if batch.ObservedAt <= 0 {
		return result, errors.New("activity observation time is required")
	}
	for _, o := range batch.Observations {
		if o.Run.ID() == "" || o.Run.Info.ExperimentID == "" {
			return result, errors.New("activity observations require run and experiment IDs")
		}
		if err := core.ValidateActivityPolicy(o.Policy); err != nil {
			return result, err
		}
	}
	for _, policy := range batch.PolicyUpdates {
		if err := core.ValidateActivityPolicy(policy); err != nil {
			return result, err
		}
	}
	db, err := s.open(ctx, true)
	if err != nil {
		return result, err
	}
	err = retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		current, err := loadActivity(ctx, tx, source)
		if err != nil {
			return err
		}
		if current.InitialUnreadSince == 0 && batch.InitialUnreadSince > 0 {
			current.InitialUnreadSince = batch.InitialUnreadSince
		}
		// A late whole scan may contain older metadata. Keep both notification
		// baselines and the discovered experiment set from the newer scan.
		if batch.ReplaceExperiments && batch.ObservedAt >= current.UpdatedAt {
			current.Experiments = append([]core.Experiment(nil), batch.Experiments...)
		}
		for _, id := range batch.CompleteExperiments {
			if _, err := tx.ExecContext(ctx, `DELETE FROM activity_run_cache WHERE source=? AND experiment=? AND observed_at<=?`, source, id, batch.StartedAt); err != nil {
				return err
			}
		}
		seenRuns := map[string]bool{}
		for _, o := range batch.Observations {
			run := o.Run
			seenRuns[run.ID()] = true
			if run.Info.LifecycleStage == "deleted" {
				if _, err := tx.ExecContext(ctx, `DELETE FROM activity_run_cache WHERE source=? AND run_id=? AND observed_at<=?`, source, run.ID(), batch.ObservedAt); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `INSERT INTO activity_run_cache(source,run_id,experiment,status,start_time,end_time,observed_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(source,run_id) DO UPDATE SET experiment=excluded.experiment,status=excluded.status,start_time=excluded.start_time,end_time=excluded.end_time,observed_at=excluded.observed_at WHERE excluded.observed_at>=activity_run_cache.observed_at`, source, run.ID(), run.Info.ExperimentID, run.Info.Status, run.Info.StartTime, run.Info.EndTime, batch.ObservedAt); err != nil {
					return err
				}
			}
			previous, exists := current.Records[run.ID()]
			settings := core.AlertSettings{}
			if o.Policy.Alerts != nil {
				settings = *o.Policy.Alerts
			}
			if o.CacheOnly && !o.Retain && !exists && len(core.DetectActivityAlerts(run, settings)) == 0 {
				continue
			}
			next := core.ObserveActivityRecord(previous, o, batch.ObservedAt, current.InitialUnreadSince, batch.ForceUnreadSince, current.Initialized)
			if err := saveActivityRecord(ctx, tx, source, next); err != nil {
				return err
			}
			current.Records[run.ID()] = next
		}
		// A successful complete active-population scan is evidence that an
		// absent older row is no longer active. Keep its receipts and episodes,
		// but stop presenting a deleted run as a current alert or recent result.
		completed := map[string]bool{}
		for _, id := range batch.CompleteExperiments {
			completed[id] = true
		}
		for id, record := range current.Records {
			if completed[record.ExperimentID] && !seenRuns[id] && record.ObservedAt <= batch.StartedAt {
				record.LifecycleStage, record.ObservedAt = "deleted", batch.ObservedAt
				if err := saveActivityRecord(ctx, tx, source, record); err != nil {
					return err
				}
				current.Records[id] = record
			}
		}
		if batch.ObservedAt >= current.UpdatedAt {
			for id, previous := range current.Records {
				policy, ok := batch.PolicyUpdates[previous.ExperimentID]
				if !ok || previous.LifecycleStage == "deleted" {
					continue
				}
				next := core.ObserveActivityRecord(previous, core.ActivityObservation{Run: previous.Run(), ExperimentName: previous.ExperimentName, Policy: policy, CacheOnly: true, Retain: true}, batch.ObservedAt, 0, 0, true)
				// A policy update uses cached evidence. Do not let it prevent a
				// concurrent full scan from reconciling the actual observation.
				next.ObservedAt = previous.ObservedAt
				for i := range next.Alerts {
					for _, before := range previous.Alerts {
						if next.Alerts[i].ID == before.ID && next.Alerts[i].Episode == before.Episode {
							next.Alerts[i].LastSeen = before.LastSeen
						}
					}
				}
				beforeJSON, _ := json.Marshal(previous)
				afterJSON, _ := json.Marshal(next)
				if bytes.Equal(beforeJSON, afterJSON) {
					continue
				}
				if err := saveActivityRecord(ctx, tx, source, next); err != nil {
					return err
				}
				current.Records[id] = next
			}
		}
		for _, id := range batch.CompleteExperiments {
			count := core.ExperimentActivityCounts{ExperimentID: id, Complete: true, ScannedAt: batch.ObservedAt}
			if old := current.Counts[id]; old.ScannedAt > count.ScannedAt {
				continue
			}
			value, _ := json.Marshal(count)
			if _, err := tx.ExecContext(ctx, `INSERT INTO activity_count_cache(source,experiment,value) VALUES(?,?,?) ON CONFLICT(source,experiment) DO UPDATE SET value=excluded.value`, source, id, string(value)); err != nil {
				return err
			}
		}
		if batch.Checkpoint > current.Checkpoint && batch.Complete {
			current.Checkpoint = batch.Checkpoint
			current.Initialized = true
		}
		if batch.FullScanComplete && batch.ObservedAt > current.LastFullScan {
			current.LastFullScan = batch.ObservedAt
		}
		if batch.ObservedAt >= current.UpdatedAt {
			current.UpdatedAt = batch.ObservedAt
			current.Complete = batch.Complete
			current.Errors = append([]string{}, batch.Errors...)
		}
		current.Version++
		if err := saveActivitySource(ctx, tx, current); err != nil {
			return err
		}
		result, err = loadActivity(ctx, tx, source)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return result, err
}

func (s *Store) changeActivity(ctx context.Context, source string, receipts []core.ActivityReceipt, change func(*core.ActivityRecord, core.ActivityReceipt)) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("activity source is required")
	}
	for _, receipt := range receipts {
		if receipt.RunID == "" || receipt.Revision < 0 {
			return errors.New("activity receipts require a run ID and nonnegative revision")
		}
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return err
	}
	return retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var sourceRaw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM activity_sources WHERE source=?`, source).Scan(&sourceRaw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		sourceState := core.NewActivitySnapshot(source)
		if err := json.Unmarshal([]byte(sourceRaw), &sourceState); err != nil {
			return err
		}
		for _, receipt := range receipts {
			var raw string
			err := tx.QueryRowContext(ctx, `SELECT value FROM activity_records WHERE source=? AND run_id=?`, source, receipt.RunID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			var record core.ActivityRecord
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				return err
			}
			change(&record, receipt)
			if err := saveActivityRecord(ctx, tx, source, record); err != nil {
				return err
			}
		}
		sourceState.Version++
		if err := saveActivitySource(ctx, tx, sourceState); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func (s *Store) MarkActivityRead(ctx context.Context, source string, receipts []core.ActivityReceipt) error {
	return s.changeActivity(ctx, source, receipts, func(r *core.ActivityRecord, receipt core.ActivityReceipt) {
		r.ReadRevision = max(r.ReadRevision, min(receipt.Revision, r.Revision))
		if !r.Unread() {
			r.Reasons = []string{}
		}
	})
}

func (s *Store) MarkActivityUnread(ctx context.Context, source string, ids []string) error {
	receipts := make([]core.ActivityReceipt, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			receipts = append(receipts, core.ActivityReceipt{RunID: id})
			seen[id] = true
		}
	}
	return s.changeActivity(ctx, source, receipts, func(r *core.ActivityRecord, _ core.ActivityReceipt) {
		r.Revision++
		r.UpdatedAt = time.Now().UnixMilli()
		for _, reason := range r.Reasons {
			if reason == "marked_unread" {
				return
			}
		}
		r.Reasons = append(r.Reasons, "marked_unread")
	})
}

func (s *Store) AcknowledgeActivity(ctx context.Context, source string, receipts []core.ActivityReceipt, acknowledged bool) error {
	return s.changeActivity(ctx, source, receipts, func(r *core.ActivityRecord, receipt core.ActivityReceipt) {
		for i := range r.Alerts {
			if r.Alerts[i].Episode <= receipt.Revision {
				r.Alerts[i].Acknowledged = acknowledged
			}
		}
	})
}

// ClearActivityCache discards only derived population/count information. Read
// receipts, alert episodes, subscription baselines and checkpoints survive.
func (s *Store) ClearActivityCache(ctx context.Context, source string) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("activity source is required")
	}
	db, err := s.open(ctx, false)
	if err != nil || db == nil {
		return err
	}
	return retry(ctx, func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, table := range []string{"activity_run_cache", "activity_count_cache"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE source=?`, source); err != nil {
				return err
			}
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM activity_sources WHERE source=?`, source).Scan(&raw); err == nil {
			snapshot := core.NewActivitySnapshot(source)
			if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
				return err
			}
			snapshot.Version++
			if err := saveActivitySource(ctx, tx, snapshot); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return tx.Commit()
	})
}
