package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ColumnSpec identifies the actual logged field, independently of its label.
type ColumnSpec struct {
	Kind           string `json:"kind"` // attribute, metric, param, tag, dataset
	Key            string `json:"key"`
	Label          string `json:"label,omitempty"`
	Width          int    `json:"width,omitempty"`
	Numeric        bool   `json:"numeric,omitempty"`
	DatasetContext string `json:"dataset_context,omitempty"`
}
type SortSpec struct {
	Column ColumnSpec `json:"column"`
	Desc   bool       `json:"desc"`
}
type ExperimentView struct {
	Columns    []ColumnSpec    `json:"columns"`
	Sort       []SortSpec      `json:"sort"`
	GroupBy    []string        `json:"group_by,omitempty"` // exact parameter keys
	Mode       string          `json:"mode"`               // auto, flat, tree, grouped
	Expansion  string          `json:"expansion"`          // collapsed, first, all
	Expanded   map[string]bool `json:"expanded,omitempty"`
	Visibility string          `json:"visibility"` // normal, hidden, archived, all
	Filter     string          `json:"filter,omitempty"`
}
type LayoutPreferences struct {
	LeftRatio            float64 `json:"left_ratio"`
	TopRatio             float64 `json:"top_ratio"`
	Mouse                bool    `json:"mouse"`
	ExperimentVisibility string  `json:"experiment_visibility"`
}

func DefaultLayout() LayoutPreferences {
	return LayoutPreferences{LeftRatio: .30, TopRatio: .55, Mouse: true, ExperimentVisibility: "normal"}
}
func DefaultView(metrics, params []string) ExperimentView {
	v := ExperimentView{Columns: []ColumnSpec{{Kind: "attribute", Key: "name", Width: 28}, {Kind: "attribute", Key: "status", Width: 12}, {Kind: "attribute", Key: "start_time", Width: 18}}, Sort: []SortSpec{{Column: ColumnSpec{Kind: "attribute", Key: "start_time"}, Desc: true}}, Mode: "auto", Expansion: "first", Visibility: "normal"}
	for _, k := range metrics {
		v.Columns = append(v.Columns, ColumnSpec{Kind: "metric", Key: k, Width: 15})
	}
	for _, k := range params {
		v.Columns = append(v.Columns, ColumnSpec{Kind: "param", Key: k, Width: 15})
	}
	return v
}

type Visibility string

const (
	VisibilityNormal   Visibility = "normal"
	VisibilityHidden   Visibility = "hidden"
	VisibilityArchived Visibility = "archived"
)

func VisibilityKey(kind, id string) string { return kind + "/" + id }

// SourceKey uses the configured origin, never a temporary tunnel or local server
// address. Changing only a target's display name/runtime preserves its views.
func SourceKey(t Target) string {
	id := t.ID
	if t.Transient {
		id = ""
	}
	b, _ := json.Marshal([]string{id, t.TrackingURI, t.SSHHost})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// StateStore is deliberately synchronous: callers run these small transactions
// in effects, never in the terminal event/render loop. New stores open lazily.
type StateStore interface {
	LoadView(context.Context, string, string) (ExperimentView, bool, error)
	SaveView(context.Context, string, string, ExperimentView) error
	ResetView(context.Context, string, string) error
	LoadLayout(context.Context) (LayoutPreferences, bool, error)
	SaveLayout(context.Context, LayoutPreferences) error
	ListVisibility(context.Context, string) (map[string]Visibility, error)
	SetVisibility(context.Context, string, string, string, Visibility) error
	Close() error
}

type RunRow struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // run, group, context
	Run        *Run   `json:"run,omitempty"`
	Label      string `json:"label"`
	Depth      int    `json:"depth"`
	ParentID   string `json:"parent_id,omitempty"`
	Expandable bool   `json:"expandable"`
	Expanded   bool   `json:"expanded"`
	Count      int    `json:"count,omitempty"`
	Note       string `json:"note,omitempty"`
}
