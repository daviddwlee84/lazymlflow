package tui

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type runPinState struct {
	Items                       map[string]core.RunPin
	RunErrors                   map[string]string
	Edits                       map[string]uint64
	FetchedAt                   map[string]int64
	Loaded, Loading, Refreshing bool
	Gen, Revision, RefreshGen   uint64
	Err                         string
	CheckedAt                   int64
}

type runPinsLoadedMsg struct {
	source        string
	gen, revision uint64
	pins          []core.RunPin
	refresh       bool
	err           error
}

type pinnedRunResult struct {
	run       core.Run
	startedAt int64
	revision  uint64
	pinnedAt  int64
	err       error
}

type runPinsRefreshedMsg struct {
	source  string
	gen     uint64
	results map[string]pinnedRunResult
	err     error
}

func (m *model) runPinStore() core.RunPinStore {
	store, _ := m.opts.State.(core.RunPinStore)
	return store
}

func (m *model) currentRunPins() *runPinState {
	if a := m.activityCurrent(); a != nil {
		return a.Pins
	}
	return nil
}

func (m *model) runPinState() *runPinState {
	if m.runPinStore() == nil {
		return nil
	}
	a := m.activityState()
	if a == nil {
		return nil
	}
	if a.Pins == nil {
		a.Pins = &runPinState{Items: map[string]core.RunPin{}, RunErrors: map[string]string{}, Edits: map[string]uint64{}, FetchedAt: map[string]int64{}}
	}
	return a.Pins
}

func (m *model) runPinned(id string) bool {
	if pins := m.currentRunPins(); pins != nil {
		_, ok := pins.Items[id]
		return ok
	}
	return false
}

func (m *model) runPinActions() []action {
	if m.runPinStore() == nil || !m.activityAvailable() {
		return nil
	}
	actions := []action{act("open-pinned", "", "Open pinned runs across experiments")}
	if m.focus == 1 && !m.compare && m.run() != nil {
		label := "Pin this run across experiments"
		if m.runPinned(m.run().ID()) {
			label = "Unpin this run"
		}
		actions = append(actions, act("toggle-run-pin", "*", label))
	}
	return actions
}

func (m *model) loadRunPins(force, refresh bool) tea.Cmd {
	pins := m.runPinState()
	if pins == nil || pins.Loading || pins.Loaded && !force {
		return nil
	}
	ctx, gen := m.operation("pins:load")
	pins.Loading, pins.Gen = true, gen
	source, revision, store, writer := core.SourceKey(m.target()), pins.Revision, m.runPinStore(), m.writer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// A reload must not race our own queued pin/unpin writes. Other
		// processes still edit independent rows rather than a shared snapshot.
		if err := writer.flush(ctx); err != nil {
			return runPinsLoadedMsg{source: source, gen: gen, revision: revision, refresh: refresh, err: err}
		}
		values, err := store.LoadRunPins(ctx, source)
		return runPinsLoadedMsg{source, gen, revision, values, refresh, err}
	}
}

func (m *model) toggleRunPin() tea.Cmd {
	run, pins := m.run(), m.runPinState()
	if m.focus != 1 || m.compare || run == nil || pins == nil {
		return nil
	}
	if !pins.Loaded {
		m.status = "Loading saved pins; press * again when ready"
		return m.loadRunPins(false, false)
	}
	source, id, store := core.SourceKey(m.target()), run.ID(), m.runPinStore()
	job := "run-pin/" + source + "/" + id
	if m.runPinned(id) {
		delete(pins.Items, id)
		delete(pins.RunErrors, id)
		delete(pins.FetchedAt, id)
		if a := m.activityCurrent(); a.RunPending == id {
			if cancel := m.cancel["activity:run"]; cancel != nil {
				cancel()
			}
			m.seq++
			a.RunGen, a.RunPending = m.seq, ""
		}
		if a := m.activityCurrent(); a.Inspect != nil && a.Inspect.ID() == id && a.Scope == scopePinned {
			a.Inspect = nil
		}
		m.writer.add(job, func(ctx context.Context) error { return store.DeleteRunPin(ctx, source, id) })
		m.status = "Run unpinned"
	} else {
		now := time.Now().UnixMilli()
		pin := core.RunPin{RunID: id, ExperimentID: run.Info.ExperimentID, RunName: run.Name(), ExperimentName: m.activityExperimentName(run.Info.ExperimentID), Status: run.Info.Status, StartTime: run.Info.StartTime, EndTime: run.Info.EndTime, LifecycleStage: run.Info.LifecycleStage, PinnedAt: now, ObservedAt: now}
		if err := core.ValidateRunPin(pin); err != nil {
			m.status = "Could not pin run: " + err.Error()
			return nil
		}
		pins.Items[id] = pin
		m.writer.add(job, func(ctx context.Context) error { return store.SaveRunPin(ctx, source, pin) })
		m.status = "Run pinned · available in Pinned across experiments"
	}
	pins.Revision++
	pins.Edits[id] = pins.Revision
	m.rebuildActivityViews()
	return tea.Batch(m.saveEffect(), m.ensureDetails())
}

func (m *model) pinnedRecords() []core.ActivityRecord {
	a, pins := m.activityCurrent(), m.currentRunPins()
	if a == nil || pins == nil {
		return nil
	}
	ordered := make([]core.RunPin, 0, len(pins.Items))
	for _, pin := range pins.Items {
		ordered = append(ordered, pin)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].PinnedAt != ordered[j].PinnedAt {
			return ordered[i].PinnedAt > ordered[j].PinnedAt
		}
		return ordered[i].RunID < ordered[j].RunID
	})
	rows := make([]core.ActivityRecord, 0, len(ordered))
	for _, pin := range ordered {
		record := pin.Record()
		if observed, ok := a.Snapshot.Records[pin.RunID]; ok && observed.ObservedAt >= pin.ObservedAt {
			record = observed
		}
		rows = append(rows, record)
	}
	return rows
}

func (m *model) refreshPinnedMetadata() tea.Cmd {
	a, pins, state := m.activityCurrent(), m.currentRunPins(), m.state()
	if a == nil || pins == nil || state == nil || state.Session == nil || pins.Refreshing {
		return nil
	}
	ids := make([]string, 0, len(pins.Items))
	revisions := make(map[string]uint64, len(pins.Items))
	pinnedAt := make(map[string]int64, len(pins.Items))
	for id := range pins.Items {
		ids = append(ids, id)
		revisions[id] = pins.Edits[id]
		pinnedAt[id] = pins.Items[id].PinnedAt
	}
	if len(ids) == 0 {
		return nil
	}
	ctx, gen := m.operation("pins:refresh")
	pins.Refreshing, pins.RefreshGen, pins.Err = true, gen, ""
	source, backend := core.SourceKey(m.target()), state.Session.Backend
	return func() tea.Msg {
		jobs := make(chan string, len(ids))
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)
		results := map[string]pinnedRunResult{}
		var mu sync.Mutex
		var workers sync.WaitGroup
		for range min(4, len(ids)) {
			workers.Go(func() {
				for id := range jobs {
					if ctx.Err() != nil {
						return
					}
					at := time.Now().UnixMilli()
					run, err := backend.GetRun(ctx, id)
					if err == nil && run.ID() != id {
						err = fmt.Errorf("run lookup returned an unexpected ID")
					}
					mu.Lock()
					results[id] = pinnedRunResult{run, at, revisions[id], pinnedAt[id], err}
					mu.Unlock()
				}
			})
		}
		workers.Wait()
		return runPinsRefreshedMsg{source, gen, results, ctx.Err()}
	}
}

func (m *model) updateRunPins(msg tea.Msg) (tea.Cmd, bool) {
	source, a, pins := core.SourceKey(m.target()), m.activityCurrent(), m.currentRunPins()
	switch v := msg.(type) {
	case runPinsLoadedMsg:
		if a == nil || pins == nil || source != v.source || pins.Gen != v.gen {
			return nil, true
		}
		pins.Loading = false
		if v.err != nil {
			pins.Err = v.err.Error()
			m.status = "Could not load pins: " + pins.Err
			return nil, true
		}
		if pins.Revision != v.revision {
			return m.loadRunPins(true, v.refresh), true
		}
		pins.Loaded, pins.Err = true, ""
		pins.Items = map[string]core.RunPin{}
		for _, pin := range v.pins {
			pins.Items[pin.RunID] = pin
		}
		for id := range pins.RunErrors {
			if !m.runPinned(id) {
				delete(pins.RunErrors, id)
			}
		}
		m.rebuildActivityViews()
		if v.refresh {
			return m.refreshPinnedMetadata(), true
		}
		return m.ensureDetails(), true
	case runPinsRefreshedMsg:
		if a == nil || pins == nil || source != v.source || pins.RefreshGen != v.gen {
			return nil, true
		}
		pins.Refreshing = false
		if v.err != nil {
			pins.Err = v.err.Error()
			return nil, true
		}
		for id, result := range v.results {
			if !m.runPinned(id) || pins.Edits[id] != result.revision || pins.Items[id].PinnedAt != result.pinnedAt || pins.FetchedAt[id] > result.startedAt {
				continue
			}
			if result.err != nil {
				pins.RunErrors[id] = result.err.Error()
				continue
			}
			delete(pins.RunErrors, id)
			copy := result.run
			readAt := result.startedAt
			if record, ok := a.Snapshot.Records[id]; ok && record.ObservedAt > readAt {
				copy = mergeActivityMetadata(copy, record)
				readAt = record.ObservedAt
			}
			a.Runs[id], a.MetadataAt[id] = &copy, readAt
			pins.FetchedAt[id] = result.startedAt
			if a.Inspect != nil && a.Inspect.ID() == id {
				a.Inspect = &copy
			}
		}
		pins.CheckedAt = time.Now().UnixMilli()
		m.rebuildActivityViews()
		return m.ensureDetails(), true
	}
	return nil, false
}

func (m *model) pinnedDescription() string {
	pins := m.currentRunPins()
	if pins == nil || !pins.Loaded {
		return "Pinned · loading saved runs…"
	}
	if pins.Refreshing || pins.Loading {
		return "Pinned · refreshing · cached rows retained · Ctrl+X cancel"
	}
	if pins.CheckedAt > 0 {
		return "Pinned · checked " + timestamp(pins.CheckedAt) + " · * unpin · i full name"
	}
	return "Pinned · local bookmarks / cached metadata · r refresh · i full name"
}
