package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

type catalogRow struct {
	ID, Name string
	Entry    int
	Group    bool
}
type catalogState struct {
	Value                                            core.DatasetCatalog
	Progress                                         core.DatasetCatalog
	Stale                                            bool
	Rows                                             []catalogRow
	Index, RunIndex, DetailIndex, Tab                int
	Selected, Query, RunQuery, SchemaQuery, ViewType string
	Collapsed                                        map[string]bool
	ShowHidden                                       bool
	Pending                                          bool
	Gen                                              uint64
	RunGen                                           uint64
	Err                                              string
	Events                                           <-chan catalogMsg
	SchemaSort                                       bool
	Expanded                                         map[string]bool
	InspectionRunID, InspectionExperimentID          string
}
type catalogMsg struct {
	source string
	gen    uint64
	value  core.DatasetCatalog
	done   bool
	err    error
}
type catalogRunMsg struct {
	source     string
	gen        uint64
	run        core.Run
	experiment core.Experiment
	err        error
}

func (m *model) catalogState() *catalogState {
	if m.work == nil {
		return nil
	}
	return m.work.catalogs[core.SourceKey(m.target())]
}
func (m *model) openCatalog() tea.Cmd {
	w := m.work
	if w.catalog {
		m.closeCatalog()
		return m.ensureDetails()
	}
	w.returnFocus = m.focus
	w.returnZoom = m.zoom
	w.catalog = true
	m.cancelUnusedHistories()
	m.focus = 0
	m.zoom = false
	source := core.SourceKey(m.target())
	m.clearCatalogInspection()
	if w.catalogs[source] == nil {
		var runs []core.Run
		var experiments []core.Experiment
		if s := m.state(); s != nil {
			experiments = append(experiments, s.Experiments...)
			ids := make([]string, 0, len(s.Runs))
			for id := range s.Runs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				runs = append(runs, s.Runs[id].Rows...)
			}
		}
		c := &catalogState{Value: core.CatalogFromRuns(source, experiments, runs), ViewType: "ACTIVE_ONLY", Collapsed: map[string]bool{}, Expanded: map[string]bool{}}
		w.catalogs[source] = c
		m.rebuildCatalogRows(c)
		if len(c.Rows) > 1 {
			c.Index = 1
			c.Selected = c.Rows[1].ID
		}
	} else if c := w.catalogs[source]; c.Value.Scope == "loaded_runs" {
		// A partial seed follows newly loaded experiments when reopening. Full
		// scans remain stable until the user explicitly refreshes them.
		if s := m.state(); s != nil {
			var runs []core.Run
			ids := make([]string, 0, len(s.Runs))
			for id := range s.Runs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				runs = append(runs, s.Runs[id].Rows...)
			}
			c.Value = core.CatalogFromRuns(source, s.Experiments, runs)
			m.rebuildCatalogRows(c)
		}
	}
	m.status = m.catalogProgress(w.catalogs[source]) + " · A scans all accessible experiments"
	return nil
}
func (m *model) closeCatalog() {
	w := m.work
	w.catalog = false
	w.typing = false
	w.query = ""
	w.search.Blur()
	m.focus = w.returnFocus
	m.zoom = w.returnZoom
	m.resizing = false
	m.drag = ""
	if stop := m.cancel["workspace:catalog-run"]; stop != nil {
		stop()
	}
	if c := m.catalogState(); c != nil {
		c.RunGen++
	}
	if c := m.catalogState(); c != nil && c.Pending {
		m.cancelCatalog()
	}
}
func (m *model) rebuildCatalogRows(c *catalogState) {
	selected := c.Selected
	var rows []catalogRow
	last := ""
	q := strings.ToLower(c.Query)
	for i, d := range c.Value.Entries {
		if !strings.Contains(strings.ToLower(d.Dataset.Name+" "+d.Dataset.Digest+" "+d.Dataset.SourceType+" "+d.Dataset.Source), q) {
			continue
		}
		if d.Dataset.Name != last || len(rows) == 0 {
			last = d.Dataset.Name
			rows = append(rows, catalogRow{ID: "name:" + last, Name: last, Entry: -1, Group: true})
		}
		if !c.Collapsed[last] {
			rows = append(rows, catalogRow{ID: d.ID, Name: d.Dataset.Digest, Entry: i})
		}
	}
	c.Rows = rows
	c.Index = clamp(c.Index, 0, len(rows)-1)
	for i, r := range rows {
		if r.ID == selected {
			c.Index = i
			break
		}
	}
	if len(rows) > 0 {
		c.Selected = rows[c.Index].ID
	} else {
		c.Selected = ""
	}
	c.RunIndex = clamp(c.RunIndex, 0, len(m.catalogUsesFor(c))-1)
}
func (m *model) catalogEntry() *core.DatasetEntry {
	return catalogEntryFor(m.catalogState())
}
func catalogEntryFor(c *catalogState) *core.DatasetEntry {
	if c == nil || len(c.Rows) == 0 {
		return nil
	}
	r := c.Rows[clamp(c.Index, 0, len(c.Rows)-1)]
	if r.Group || r.Entry < 0 || r.Entry >= len(c.Value.Entries) {
		return nil
	}
	return &c.Value.Entries[r.Entry]
}
func (m *model) catalogUses() []core.DatasetUse {
	return m.catalogUsesFor(m.catalogState())
}
func (m *model) catalogUsesFor(c *catalogState) []core.DatasetUse {
	d := catalogEntryFor(c)
	if d == nil || c == nil {
		return nil
	}
	var out []core.DatasetUse
	seen := map[string]int{}
	var visibility map[string]core.Visibility
	for _, target := range m.targets {
		if core.SourceKey(target) == c.Value.Source {
			if s := m.states[target.ID]; s != nil {
				visibility = s.Visibility
			}
			break
		}
	}
	for _, u := range d.Uses {
		if !c.ShowHidden {
			if visibility[core.VisibilityKey("run", u.RunID)] != "" || visibility[core.VisibilityKey("experiment", u.ExperimentID)] != "" {
				continue
			}
		}
		if !strings.Contains(strings.ToLower(u.RunName+" "+u.RunID+" "+u.ExperimentName+" "+u.ExperimentID+" "+u.Context), strings.ToLower(c.RunQuery)) {
			continue
		}
		if i, ok := seen[u.RunID]; ok {
			found := false
			for _, existing := range strings.Split(out[i].Context, " / ") {
				found = found || existing == u.Context
			}
			if u.Context != "" && !found {
				if out[i].Context != "" {
					out[i].Context += " / "
				}
				out[i].Context += u.Context
			}
			continue
		}
		seen[u.RunID] = len(out)
		out = append(out, u)
	}
	return out
}
func (m *model) catalogUse() *core.DatasetUse {
	r := m.catalogUses()
	if len(r) == 0 {
		return nil
	}
	u := r[clamp(m.catalogState().RunIndex, 0, len(r)-1)]
	return &u
}
func (m *model) scanCatalog() tea.Cmd {
	c := m.catalogState()
	s := m.state()
	if c == nil || s == nil || s.Session == nil {
		m.status = "Connect a target before scanning datasets"
		return nil
	}
	ctx, gen := m.workspaceContext("catalog")
	source := core.SourceKey(m.target())
	backend := s.Session.Backend
	c.Gen = gen
	c.Pending = true
	c.Err = ""
	c.Progress = core.DatasetCatalog{}
	view := c.ViewType
	events := make(chan catalogMsg, 1)
	c.Events = events
	return func() tea.Msg {
		go func() {
			defer close(events)
			value, err := core.ScanDatasets(ctx, backend, source, core.DatasetScanOptions{ViewType: view}, func(value core.DatasetCatalog) {
				select {
				case events <- catalogMsg{source, gen, value, false, nil}:
				case <-ctx.Done():
				}
			})
			select {
			case events <- catalogMsg{source, gen, value, true, err}:
			case <-ctx.Done():
			}
		}()
		return waitCatalog(events)()
	}
}
func waitCatalog(events <-chan catalogMsg) tea.Cmd {
	return func() tea.Msg {
		v, ok := <-events
		if !ok {
			return nil
		}
		return v
	}
}
func (m *model) acceptCatalog(v catalogMsg) tea.Cmd {
	c := m.work.catalogs[v.source]
	if c == nil || v.gen != c.Gen {
		return nil
	}
	selectedRun := ""
	if uses := m.catalogUsesFor(c); len(uses) > 0 {
		selectedRun = uses[clamp(c.RunIndex, 0, len(uses)-1)].RunID
	}
	c.Progress = v.value
	// Empty progress is not an authoritative empty catalog. Retain useful rows
	// through discovery failures, but clear them after a complete empty scan.
	keepPrevious := len(v.value.Entries) == 0 && len(c.Value.Entries) > 0 && !v.value.Complete && c.Value.ViewType == v.value.ViewType
	c.Stale = keepPrevious
	if keepPrevious {
		c.Value.Complete = false
		c.Value.Errors = v.value.Errors
		c.Value.Cancelled = v.value.Cancelled
	} else {
		c.Value = v.value
	}
	c.Pending = !v.done
	if v.err != nil {
		c.Err = v.err.Error()
	}
	m.rebuildCatalogRows(c)
	for i, use := range m.catalogUsesFor(c) {
		if use.RunID == selectedRun {
			c.RunIndex = i
			break
		}
	}
	if v.source == core.SourceKey(m.target()) && m.work.catalog {
		m.status = m.catalogProgress(c)
	}
	if !v.done {
		return waitCatalog(c.Events)
	}
	return nil
}
func (m *model) cancelCatalog() {
	if stop := m.cancel["workspace:catalog"]; stop != nil {
		stop()
	}
	if c := m.catalogState(); c != nil {
		c.Gen++
		c.Pending = false
		c.Value.Cancelled = true
		c.Value.Complete = false
		c.Err = "Scan cancelled; partial results retained"
	}
	m.status = "Dataset scan cancelled; partial results retained"
}
func (m *model) catalogProgress(c *catalogState) string {
	state := "Partial: loaded runs"
	if c.Pending {
		state = "Scanning"
	} else if c.Value.Complete {
		state = "Complete as of " + timestamp(c.Value.UpdatedAt)
	} else if c.Value.Scope != "loaded_runs" {
		state = "Partial scan"
	}
	if c.Err != "" {
		state += ": " + clean(c.Err)
	}
	counts := c.Value
	if (c.Pending || c.Stale) && c.Progress.Source != "" {
		counts = c.Progress
	}
	if c.Stale {
		state += " · previous rows shown (" + timestamp(c.Value.UpdatedAt) + ")"
	}
	return fmt.Sprintf("%s · %d/%d experiments · %d runs · %d dataset variants · %s", state, counts.ExperimentsScanned, len(counts.ExperimentIDs), counts.RunsScanned, len(counts.Entries), c.ViewType)
}
func (m *model) catalogMove(delta int) {
	c := m.catalogState()
	if c == nil {
		return
	}
	switch m.focus {
	case 0:
		c.Index = clamp(c.Index+delta, 0, len(c.Rows)-1)
		if len(c.Rows) > 0 {
			c.Selected = c.Rows[c.Index].ID
		}
		c.RunIndex = 0
		c.DetailIndex = 0
	case 1:
		c.RunIndex = clamp(c.RunIndex+delta, 0, len(m.catalogUses())-1)
	case 2:
		limit := 0
		if c.Tab == 0 {
			limit = len(m.catalogFields()) - 1
		}
		if c.Tab == 1 {
			if d := m.catalogEntry(); d != nil {
				limit = len(m.catalogMetadata(d, max(10, m.width))) - 1
			}
		}
		c.DetailIndex = clamp(c.DetailIndex+delta, 0, limit)
	}
}
func (m *model) catalogKey(msg tea.Msg, key string) tea.Cmd {
	w := m.work
	c := m.catalogState()
	if c == nil {
		return nil
	}
	if w.typing {
		return m.workspaceSearchKey(msg, key)
	}
	if m.resizing {
		return m.resizeKey(key)
	}
	switch key {
	case "q", "ctrl+c":
		m.stopAll()
		return tea.Quit
	case "B", "esc":
		m.closeCatalog()
		return m.ensureDetails()
	case "A", "r":
		return m.scanCatalog()
	case "ctrl+x":
		m.cancelCatalog()
	case "1", "2", "3":
		m.focus = int(key[0] - '1')
	case "tab":
		m.focus = (m.focus + 1) % 3
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
	case "z":
		m.zoom = !m.zoom
	case "M":
		m.layout.Mouse = !m.layout.Mouse
		return m.saveLayout()
	case "ctrl+w":
		m.resizing = true
	case "up", "k":
		m.catalogMove(-1)
	case "down", "j":
		m.catalogMove(1)
	case "home", "g":
		m.catalogMove(-1 << 30)
	case "end", "G":
		m.catalogMove(1 << 30)
	case "pgup":
		m.catalogMove(-10)
	case "pgdown":
		m.catalogMove(10)
	case "/":
		value := c.Query
		if m.focus == 1 {
			value = c.RunQuery
		}
		if m.focus == 2 {
			value = c.SchemaQuery
		}
		return m.startWorkspaceSearch(value)
	case "[":
		c.Tab = (c.Tab + 2) % 3
		c.DetailIndex = 0
	case "]":
		c.Tab = (c.Tab + 1) % 3
		c.DetailIndex = 0
	case "s":
		c.SchemaSort = !c.SchemaSort
		c.DetailIndex = 0
	case "V":
		m.cancelCatalog()
		views := []string{"ACTIVE_ONLY", "ALL", "DELETED_ONLY"}
		for i, v := range views {
			if c.ViewType == v {
				c.ViewType = views[(i+1)%len(views)]
				break
			}
		}
		return m.scanCatalog()
	case "H":
		c.ShowHidden = !c.ShowHidden
		c.RunIndex = 0
	case "N":
		if s, ok := m.currentSubject(); ok {
			return m.openNotes(s)
		}
	case "S":
		return m.openSummary()
	case "t":
		m.closeCatalog()
		return m.perform("targets")
	case "?":
		w.modal = "workspace-help"
	case "y":
		if m.focus == 1 {
			if u := m.catalogUse(); u != nil {
				return m.copyWorkspace(u.RunID, "Run ID")
			}
		} else if d := m.catalogEntry(); d != nil {
			return m.copyWorkspace(d.ID, "Dataset identity")
		}
	case "o":
		if u := m.catalogUse(); u != nil {
			if s := m.state(); s != nil && s.Session != nil {
				url := platform.ResourceURL(s.Session.WebURL, u.ExperimentID, u.RunID)
				return func() tea.Msg { return resultMsg{err: platform.OpenURL(m.ctx, url), text: "Opened run"} }
			}
		}
	case "left", "h":
		if m.focus == 2 && c.Tab == 0 {
			if fields := m.catalogFields(); len(fields) > 0 {
				f := fields[clamp(c.DetailIndex, 0, len(fields)-1)]
				if len(f.Children) > 0 {
					c.Expanded[f.Path] = false
					return nil
				}
			}
			m.focus = 1
		} else if m.focus == 0 && len(c.Rows) > 0 {
			row := c.Rows[c.Index]
			if !row.Group {
				row = catalogRow{Name: m.catalogEntry().Dataset.Name}
			}
			c.Collapsed[row.Name] = true
			c.Selected = "name:" + row.Name
			m.rebuildCatalogRows(c)
		} else {
			m.focus = max(0, m.focus-1)
		}
	case "right", "l", "enter":
		if m.focus == 0 && len(c.Rows) > 0 {
			r := c.Rows[c.Index]
			if r.Group {
				c.Collapsed[r.Name] = !c.Collapsed[r.Name]
				m.rebuildCatalogRows(c)
			} else {
				m.focus = 1
			}
			return nil
		}
		if m.focus == 1 && (key == "enter" || key == "l" || key == "right") {
			return m.jumpCatalogRun()
		}
		if m.focus == 2 {
			if c.Tab == 0 && key != "enter" {
				if fields := m.catalogFields(); len(fields) > 0 {
					f := fields[clamp(c.DetailIndex, 0, len(fields)-1)]
					if len(f.Children) > 0 {
						c.Expanded[f.Path] = true
						return nil
					}
				}
			}
			if c.Tab == 2 {
				if d := m.catalogEntry(); d != nil {
					return m.openNotes(core.Subject{Source: core.SourceKey(m.target()), Kind: "dataset", ID: d.ID, Label: d.Dataset.Name})
				}
			} else {
				w.modal = "field"
				w.reportText = m.catalogFieldText()
			}
		}
	}
	return nil
}
func (m *model) jumpCatalogRun() tea.Cmd {
	u := m.catalogUse()
	s := m.state()
	c := m.catalogState()
	if u == nil || s == nil || s.Session == nil {
		return nil
	}
	ctx, gen := m.workspaceContext("catalog-run")
	c.RunGen = gen
	source := core.SourceKey(m.target())
	b := s.Session.Backend
	use := *u
	return func() tea.Msg {
		r, err := b.GetRun(ctx, use.RunID)
		var e core.Experiment
		if err == nil {
			e, err = b.GetExperiment(ctx, use.ExperimentID)
		}
		return catalogRunMsg{source, gen, r, e, err}
	}
}
func (m *model) acceptCatalogRun(v catalogRunMsg) tea.Cmd {
	c := m.catalogState()
	if c == nil || v.source != core.SourceKey(m.target()) || c.RunGen != v.gen || !m.work.catalog {
		return nil
	}
	if v.err != nil {
		m.status = v.err.Error()
		return nil
	}
	s := m.state()
	if s == nil {
		return nil
	}
	found := false
	for _, e := range s.Experiments {
		found = found || e.ID == v.experiment.ID
	}
	if !found {
		s.Experiments = append(s.Experiments, v.experiment)
	}
	m.closeCatalog()
	m.leaveActivity()
	s.Selected = v.run.Info.ExperimentID
	if s.Selected == "" {
		s.Selected = v.experiment.ID
	}
	// Reuse the experiment's view and loaded rows; a jump must not erase local
	// filters, pending view choices, or sibling runs that were already fetched.
	rs := s.Runs[s.Selected]
	if rs == nil {
		rs = &runState{listState: listState{Order: "attributes.start_time DESC"}, View: core.DefaultView(m.metricColumns, m.paramColumns)}
		rs.View.Mode = "flat"
		s.Runs[s.Selected] = rs
	}
	if stop := m.cancel["runs"]; stop != nil {
		stop()
	}
	m.seq++
	rs.Gen = m.seq
	rs.Pending = false
	rs.FirstPagePending = false
	rs.LoadingAll = false
	found = false
	for i := range rs.Rows {
		if rs.Rows[i].ID() == v.run.ID() {
			rs.Rows[i] = v.run
			found = true
			break
		}
	}
	if !found {
		rs.Rows = append(rs.Rows, v.run)
	}
	rs.RowsVersion++
	rs.Selected = v.run.ID()
	c.InspectionRunID = v.run.ID()
	c.InspectionExperimentID = s.Selected
	for i, e := range m.experimentsVisible() {
		if e.ID == s.Selected {
			s.Index = i
			break
		}
	}
	m.refreshRowCache()
	for i, row := range m.runRows() {
		if row.ID == rs.Selected {
			rs.Index = i
			break
		}
	}
	m.focus = 2
	m.tab = 0
	m.detailOffset = 0
	m.status = "Related run loaded · r loads the experiment · B returns to datasets"
	return m.ensureDetails()
}

// Explicit relation jumps are temporary presentation exceptions. They reveal
// the requested run/experiment without changing saved filters or visibility.
func (m *model) catalogInspectionKey() string {
	c := m.catalogState()
	if c == nil {
		return ""
	}
	return c.InspectionExperimentID + "\x00" + c.InspectionRunID
}
func (m *model) clearCatalogInspection() {
	if c := m.catalogState(); c != nil {
		c.InspectionRunID = ""
		c.InspectionExperimentID = ""
	}
}
func (m *model) catalogInspectionRows(rows []core.RunRow) []core.RunRow {
	c, s := m.catalogState(), m.state()
	if c == nil || s == nil || c.InspectionRunID == "" || s.Selected != c.InspectionExperimentID {
		return rows
	}
	r := m.runs()
	if r == nil {
		return rows
	}
	var inspected *core.Run
	for i := range r.Rows {
		if r.Rows[i].ID() == c.InspectionRunID {
			inspected = &r.Rows[i]
			break
		}
	}
	if inspected == nil {
		return rows
	}
	for i := range rows {
		if rows[i].ID == inspected.ID() {
			rows[i].Note = "inspection context"
			rows[i].Run = inspected
			rows[i].Kind = "run"
			return rows
		}
	}
	return append(rows, core.RunRow{ID: inspected.ID(), Kind: "run", Run: inspected, Label: inspected.Name(), Note: "inspection context"})
}
func (m *model) catalogInspectionExperiments(rows []core.Experiment) []core.Experiment {
	c, s := m.catalogState(), m.state()
	if c == nil || s == nil || c.InspectionExperimentID == "" || s.Selected != c.InspectionExperimentID {
		return rows
	}
	for _, e := range rows {
		if e.ID == c.InspectionExperimentID {
			return rows
		}
	}
	for _, e := range s.Experiments {
		if e.ID == c.InspectionExperimentID {
			return append(rows, e)
		}
	}
	return rows
}
func (m *model) catalogDetailTitle() string {
	c := m.catalogState()
	if c == nil {
		return "Schema"
	}
	names := []string{"Schema", "Metadata", "Notes"}
	names[c.Tab] = "[" + names[c.Tab] + "]"
	return strings.Join(names, " ")
}
func (m *model) catalogPane(pane, width, height int) paneContent {
	var p paneContent
	c := m.catalogState()
	if c == nil {
		return p
	}
	switch pane {
	case 0:
		p.add(m.catalogProgress(c))
		if m.work.typing && m.focus == 0 {
			p.add(m.work.search.View())
		} else if c.Query != "" {
			p.add("Search: " + clean(c.Query))
		}
		cap := max(1, height-len(p.Lines))
		start := listStart(c.Index, len(c.Rows), cap)
		for i := start; i < min(len(c.Rows), start+cap); i++ {
			r := c.Rows[i]
			label := r.Name
			id := "catalog:dataset:" + r.ID
			if r.Group {
				symbol := "▾ "
				if c.Collapsed[r.Name] {
					symbol = "▸ "
				}
				label = symbol + r.Name
				id = "catalog:group:" + r.Name
			} else {
				d := c.Value.Entries[r.Entry]
				label = "  " + d.Dataset.Digest + " · " + d.Dataset.SourceType + fmt.Sprintf(" · %d uses", len(d.Uses))
			}
			p.addHit(row(label, i == c.Index, width), id, width)
		}
		if len(c.Rows) == 0 {
			p.add("No datasets in this scope. A scans all.")
		}
	case 1:
		uses := m.catalogUses()
		total := 0
		if d := m.catalogEntry(); d != nil {
			total = len(d.Uses)
		}
		p.add(fmt.Sprintf("%d visible runs · %d logged uses · hidden=%t", len(uses), total, c.ShowHidden))
		if m.work.typing && m.focus == 1 {
			p.add(m.work.search.View())
		}
		p.add(fit("Experiment", max(12, width/4)) + "Run / context")
		cap := max(1, height-len(p.Lines))
		start := listStart(c.RunIndex, len(uses), cap)
		for i := start; i < min(len(uses), start+cap); i++ {
			u := uses[i]
			p.addHit(row(fit(u.ExperimentName, max(10, width/4))+u.RunName+" · "+u.Context+" · "+u.Status, i == c.RunIndex, width), "catalog:run:"+u.RunID, width)
		}
		if len(uses) == 0 {
			p.add("Choose a dataset version; A discovers more relationships.")
		}
	case 2:
		d := m.catalogEntry()
		if d == nil {
			p.add("Select a dataset version to inspect schema and notes.")
			break
		}
		if c.Tab == 2 {
			p.add("Local journal for " + clean(d.Dataset.Name))
			p.add("N / Enter opens notes · c creates a new entry there")
			p.add("Dataset ID: " + d.ID)
			break
		}
		if c.Tab == 1 {
			lines := m.catalogMetadata(d, width)
			start := clamp(c.DetailIndex, 0, max(0, len(lines)-height))
			p.Lines = append(p.Lines, lines[start:min(len(lines), start+height)]...)
			break
		}
		p.add(fmt.Sprintf("%d columns · %d leaf paths · %s · %s", d.Schema.Columns, d.Schema.Leaves, d.Schema.Kind, d.Dataset.Digest))
		if use := m.catalogUse(); use != nil {
			if profile := core.ParseDatasetProfile(use.Profile); profile.Rows != nil {
				p.add(fmt.Sprintf("%d logged rows · %s · input %d", *profile.Rows, clean(use.RunName), use.InputIndex))
			}
		}
		if d.Schema.Error != "" {
			p.add(clean(d.Schema.Error) + " · Enter shows raw schema")
		}
		if m.work.typing && m.focus == 2 {
			p.add(m.work.search.View())
		} else if c.SchemaQuery != "" {
			p.add("Search: " + clean(c.SchemaQuery))
		}
		p.add(fit("#", 5) + fit("Name / path", max(12, width/2)) + fit("Type", 12) + "Required / shape")
		fields := m.catalogFields()
		cap := max(1, height-len(p.Lines))
		start := listStart(c.DetailIndex, len(fields), cap)
		for i := start; i < min(len(fields), start+cap); i++ {
			f := fields[i]
			required := "—"
			if f.Required != nil {
				required = strconv.FormatBool(*f.Required)
			}
			shape := ""
			if len(f.Shape) > 0 {
				shape = fmt.Sprint(f.Shape)
			}
			path := f.Path
			if len(f.Children) > 0 {
				symbol := "▾ "
				if expanded, set := c.Expanded[f.Path]; set && !expanded {
					symbol = "▸ "
				}
				path = symbol + path
			}
			path = strings.Repeat(" ", min(f.Depth, 10)) + path
			p.addHit(row(fit(strconv.Itoa(f.Index), 5)+fit(path, max(12, width/2))+fit(f.Type, 12)+required+" "+shape, i == c.DetailIndex, width), "catalog:field:"+strconv.Itoa(i), width)
		}
		if len(fields) == 0 {
			p.add("No matching schema fields · Enter shows raw schema")
		}
	}
	return p
}
func (m *model) catalogFields() []core.SchemaField {
	d := m.catalogEntry()
	c := m.catalogState()
	if d == nil || c == nil {
		return nil
	}
	var out []core.SchemaField
	collapsedDepth := -1
	for _, f := range core.FlattenSchemaFields(d.Schema) {
		if c.SchemaQuery == "" {
			if collapsedDepth >= 0 && f.Depth > collapsedDepth {
				continue
			}
			collapsedDepth = -1
			if expanded, set := c.Expanded[f.Path]; set && !expanded && len(f.Children) > 0 {
				collapsedDepth = f.Depth
			}
		}
		if strings.Contains(strings.ToLower(f.Path+" "+f.Name+" "+f.Type), strings.ToLower(c.SchemaQuery)) {
			out = append(out, f)
		}
	}
	if c.SchemaSort {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	}
	return out
}
func (m *model) catalogMetadata(d *core.DatasetEntry, width int) []string {
	lines := workspaceTextLines("Name: "+d.Dataset.Name+"\nDigest: "+d.Dataset.Digest+"\nVariant: "+d.ID+"\nSource type: "+d.Dataset.SourceType+"\nSource: "+d.Dataset.Source, width)
	if selected := m.catalogUse(); selected != nil {
		lines = append(lines, workspaceTextLines(fmt.Sprintf("Selected use: %s / %s (%s), input %d, context %s\nLogged profile: %s", selected.ExperimentName, selected.RunName, selected.RunID, selected.InputIndex, selected.Context, selected.Profile), width)...)
	}
	profiles := map[string]bool{}
	for _, u := range d.Uses {
		profile := u.Context + " · " + u.Profile
		if !profiles[profile] {
			profiles[profile] = true
			lines = append(lines, workspaceTextLines(fmt.Sprintf("Logged profile (%s / %s, input %d): %s", u.ExperimentName, u.RunName, u.InputIndex, profile), width)...)
		}
	}
	return lines
}
func (m *model) catalogFieldText() string {
	d := m.catalogEntry()
	if d == nil {
		return ""
	}
	fields := m.catalogFields()
	if len(fields) == 0 {
		return d.Dataset.Schema
	}
	f := fields[clamp(m.catalogState().DetailIndex, 0, len(fields)-1)]
	b, _ := json.MarshalIndent(f, "", "  ")
	return string(b)
}
