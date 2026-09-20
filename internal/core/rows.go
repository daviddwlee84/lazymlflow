package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

func Visible(state Visibility, filter string) bool {
	if state == "" {
		state = VisibilityNormal
	}
	if filter == "" {
		filter = "normal"
	}
	return filter == "all" || string(state) == filter
}
func expanded(v ExperimentView, id string, depth int) bool {
	if value, ok := v.Expanded[id]; ok {
		return value
	}
	return v.Expansion == "all" || (v.Expansion == "first" && depth == 0)
}

func BuildRunRows(runs []Run, v ExperimentView, visibility map[string]Visibility) []RunRow {
	v = NormalizeView(v)
	var visible []Run
	for _, r := range runs {
		if Visible(visibility[VisibilityKey("run", r.ID())], v.Visibility) {
			visible = append(visible, r)
		}
	}
	ordered := SortRuns(visible, v.Sort)
	mode := v.Mode
	if mode == "auto" {
		mode = "flat"
		for _, r := range ordered {
			if r.ParentID() != "" {
				mode = "tree"
				break
			}
		}
	}
	if mode == "grouped" && len(v.GroupBy) > 0 {
		return groupedRows(ordered, v)
	}
	if mode == "tree" {
		return treeRows(ordered, v, visibility)
	}
	rows := make([]RunRow, 0, len(ordered))
	for i := range ordered {
		r := &ordered[i]
		rows = append(rows, RunRow{ID: r.ID(), Kind: "run", Run: r, Label: r.Name(), ParentID: r.ParentID()})
	}
	return rows
}
func groupedRows(runs []Run, v ExperimentView) []RunRow {
	type group struct {
		id, label string
		runs      []Run
		values    []string
	}
	groups := map[string]*group{}
	for _, r := range runs {
		var key []any
		var labels, values []string
		for _, field := range v.GroupBy {
			value, ok := r.Param(field)
			if ok {
				key = append(key, value)
				values = append(values, "0"+value)
			} else {
				key = append(key, nil)
				values = append(values, "1")
				value = "—"
			}
			labels = append(labels, field+"="+value)
		}
		b, _ := json.Marshal(struct {
			Keys   []string
			Values []any
		}{v.GroupBy, key})
		hash := sha256.Sum256(b)
		id := "group:" + hex.EncodeToString(hash[:12])
		g := groups[id]
		if g == nil {
			g = &group{id: id, label: strings.Join(labels, " · "), values: values}
			groups[id] = g
		}
		g.runs = append(g.runs, r)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := groups[keys[i]], groups[keys[j]]
		for n := range a.values {
			if a.values[n] != b.values[n] {
				return a.values[n] < b.values[n]
			}
		}
		return a.id < b.id
	})
	var rows []RunRow
	for _, key := range keys {
		g := groups[key]
		open := expanded(v, key, 0)
		rows = append(rows, RunRow{ID: key, Kind: "group", Label: g.label, Expandable: true, Expanded: open, Count: len(g.runs)})
		if open {
			for i := range g.runs {
				r := &g.runs[i]
				rows = append(rows, RunRow{ID: r.ID(), Kind: "run", Run: r, Label: r.Name(), Depth: 1, ParentID: key})
			}
		}
	}
	return rows
}

func treeRows(runs []Run, v ExperimentView, visibility map[string]Visibility) []RunRow {
	byID := map[string]*Run{}
	for i := range runs {
		byID[runs[i].ID()] = &runs[i]
	}
	children := map[string][]*Run{}
	var roots []*Run
	var contexts []string
	seenContext := map[string]bool{}
	notes := map[string]string{}
	for i := range runs {
		r := &runs[i]
		parent := r.ParentID()
		if parent == "" {
			roots = append(roots, r)
			continue
		}
		if parent == r.ID() {
			roots = append(roots, r)
			notes[r.ID()] = "self-parent relation"
			continue
		}
		children[parent] = append(children[parent], r)
		if byID[parent] == nil && !seenContext[parent] {
			contexts = append(contexts, parent)
			seenContext[parent] = true
		}
	}
	var rows []RunRow
	visited := map[string]bool{}
	var appendRun func(*Run, int, map[string]bool)
	appendRun = func(r *Run, depth int, ancestors map[string]bool) {
		if visited[r.ID()] {
			return
		}
		visited[r.ID()] = true
		kids := children[r.ID()]
		open := expanded(v, r.ID(), depth)
		row := RunRow{ID: r.ID(), Kind: "run", Run: r, Label: r.Name(), Depth: depth, ParentID: r.ParentID(), Expandable: len(kids) > 0, Expanded: open, Count: len(kids), Note: notes[r.ID()]}
		rows = append(rows, row)
		if !open {
			var mark func(*Run)
			mark = func(n *Run) {
				for _, child := range children[n.ID()] {
					if !visited[child.ID()] {
						visited[child.ID()] = true
						mark(child)
					}
				}
			}
			mark(r)
			return
		}
		ancestors[r.ID()] = true
		for _, child := range kids {
			if ancestors[child.ID()] {
				rows[len(rows)-1].Note = "cyclic parent relation"
				continue
			}
			appendRun(child, depth+1, ancestors)
		}
		delete(ancestors, r.ID())
	}
	for _, r := range roots {
		appendRun(r, 0, map[string]bool{})
	}
	// Missing parents are context only. Their children remain visible even when
	// that parent is hidden, filtered out, deleted, or outside the loaded pages.
	for _, parent := range contexts {
		id := "context:" + parent
		note := "parent not in loaded results"
		if state := visibility[VisibilityKey("run", parent)]; state == VisibilityHidden || state == VisibilityArchived {
			note = "parent locally " + string(state)
		}
		open := expanded(v, id, 0)
		rows = append(rows, RunRow{ID: id, Kind: "context", Label: parent, ParentID: parent, Expandable: true, Expanded: open, Count: len(children[parent]), Note: note})
		for _, r := range children[parent] {
			if open {
				appendRun(r, 1, map[string]bool{})
			} else {
				var mark func(*Run)
				mark = func(n *Run) {
					if visited[n.ID()] {
						return
					}
					visited[n.ID()] = true
					for _, kid := range children[n.ID()] {
						mark(kid)
					}
				}
				mark(r)
			}
		}
	}
	// Closed cycles have no root. Break at the deterministic first run, retain
	// every entity, and annotate the invalid relation instead of recursing forever.
	for i := range runs {
		r := &runs[i]
		if !visited[r.ID()] {
			notes[r.ID()] = "cyclic parent relation"
			appendRun(r, 0, map[string]bool{})
		}
	}
	return rows
}
