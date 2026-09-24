package core

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var attributes = []string{"name", "status", "start_time", "end_time", "duration", "run_id", "experiment_id", "user", "source", "version", "description", "models", "parent_id"}

func ColumnID(c ColumnSpec) string {
	id := c.Kind + ":" + c.Key
	if c.DatasetContext != "" {
		id += "@" + c.DatasetContext
	}
	return id
}
func ColumnLabel(c ColumnSpec) string {
	if c.Label != "" {
		return c.Label
	}
	if c.Kind == "attribute" {
		labels := map[string]string{"name": "Run name", "status": "Status", "start_time": "Created", "end_time": "Ended", "duration": "Duration", "run_id": "Run ID", "experiment_id": "Experiment", "user": "User", "source": "Source", "version": "Version", "description": "Description", "models": "Models", "parent_id": "Parent run"}
		if label, ok := labels[c.Key]; ok {
			return label
		}
	}
	if c.Kind == "dataset" {
		s := "Dataset " + c.Key
		if c.DatasetContext != "" {
			s += " (" + c.DatasetContext + ")"
		}
		return s
	}
	return c.Key
}
func ParseColumn(s string) (ColumnSpec, error) {
	kind, key, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		kind = "attribute"
		key = strings.TrimSpace(s)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	key = strings.TrimSpace(key)
	switch kind {
	case "attr", "attributes":
		kind = "attribute"
	case "metrics":
		kind = "metric"
	case "params", "parameter", "parameters":
		kind = "param"
	case "tags":
		kind = "tag"
	case "datasets":
		kind = "dataset"
	}
	c := ColumnSpec{Kind: kind, Key: key, Width: 16}
	if kind == "attribute" {
		switch key {
		case "run_name":
			c.Key = "name"
		case "id":
			c.Key = "run_id"
		case "created":
			c.Key = "start_time"
		}
	}
	if kind == "dataset" {
		if k, context, ok := strings.Cut(key, "@"); ok {
			c.Key = k
			c.DatasetContext = context
		}
	}
	if err := validateColumn(c); err != nil {
		return c, err
	}
	return c, nil
}
func ParseSort(s string) (SortSpec, error) {
	field, dir := strings.TrimSpace(s), "asc"
	if i := strings.LastIndex(field, "="); i >= 0 {
		dir = strings.ToLower(strings.TrimSpace(field[i+1:]))
		field = field[:i]
	} else {
		upper := strings.ToUpper(field)
		for _, suffix := range []string{" DESC", " ASC"} {
			if strings.HasSuffix(upper, suffix) {
				dir = strings.ToLower(strings.TrimSpace(suffix))
				field = strings.TrimSpace(field[:len(field)-len(suffix)])
				break
			}
		}
	}
	if dir != "asc" && dir != "desc" {
		return SortSpec{}, fmt.Errorf("sort direction must be asc or desc (column=direction)")
	}
	if prefix, key, ok := strings.Cut(field, "."); ok {
		switch prefix {
		case "attributes", "metrics", "params", "tags":
			if len(key) >= 2 && ((key[0] == '`' && key[len(key)-1] == '`') || (key[0] == '"' && key[len(key)-1] == '"')) {
				key = key[1 : len(key)-1]
			}
			field = prefix + ":" + key
		}
	}
	c, err := ParseColumn(field)
	return SortSpec{Column: c, Desc: dir == "desc"}, err
}
func validateColumn(c ColumnSpec) error {
	if c.Key == "" || strings.ContainsAny(c.Key, "\x00\r\n") {
		return fmt.Errorf("column needs a nonempty key without control characters")
	}
	switch c.Kind {
	case "attribute":
		found := false
		for _, key := range attributes {
			if c.Key == key {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unknown attribute %q", c.Key)
		}
	case "dataset":
		if c.Key != "name" && c.Key != "digest" && c.Key != "context" && c.Key != "source_type" && c.Key != "source" {
			return fmt.Errorf("unknown dataset field %q", c.Key)
		}
	case "metric", "param", "tag":
	default:
		return fmt.Errorf("unknown column kind %q (attribute, metric, param, tag, dataset)", c.Kind)
	}
	if c.Width < 0 || c.Width > 1000 {
		return fmt.Errorf("column width must be between 0 and 1000")
	}
	return nil
}
func NormalizeView(v ExperimentView) ExperimentView {
	if v.Mode == "" {
		v.Mode = "auto"
	}
	if v.Expansion == "" {
		v.Expansion = "first"
	}
	if v.Visibility == "" {
		v.Visibility = "normal"
	}
	if len(v.Sort) == 0 {
		v.Sort = DefaultView(nil, nil).Sort
	}
	if len(v.Columns) == 0 {
		v.Columns = DefaultView(nil, nil).Columns
	}
	// Run name anchors the identity of a row while dynamic fields scroll. Copy
	// before reordering so loading/normalizing does not mutate another saved view.
	nameIndex := -1
	for i, c := range v.Columns {
		if c.Kind == "attribute" && c.Key == "name" {
			nameIndex = i
			break
		}
	}
	if nameIndex != 0 {
		name := ColumnSpec{Kind: "attribute", Key: "name", Width: 28}
		if nameIndex >= 0 {
			name = v.Columns[nameIndex]
		}
		columns := make([]ColumnSpec, 0, len(v.Columns)+1)
		columns = append(columns, name)
		for i, c := range v.Columns {
			if i != nameIndex {
				columns = append(columns, c)
			}
		}
		v.Columns = columns
	}
	return v
}
func ValidateView(v ExperimentView) error {
	if err := ValidateMetricPins(v.MetricPins); err != nil {
		return err
	}
	if v.Activity != nil {
		if err := ValidateActivityPolicy(*v.Activity); err != nil {
			return err
		}
	}
	for _, c := range v.Columns {
		if err := validateColumn(c); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, c := range v.Columns {
		key := ColumnID(c)
		if seen[key] {
			return fmt.Errorf("duplicate column %q", key)
		}
		seen[key] = true
	}
	for _, s := range v.Sort {
		if err := validateColumn(s.Column); err != nil {
			return err
		}
	}
	for _, k := range v.GroupBy {
		if k == "" || strings.ContainsAny(k, "\x00\r\n") {
			return fmt.Errorf("group-by requires a parameter key")
		}
	}
	if v.Mode != "" && v.Mode != "auto" && v.Mode != "flat" && v.Mode != "tree" && v.Mode != "grouped" {
		return fmt.Errorf("mode must be auto, flat, tree or grouped")
	}
	if v.Expansion != "" && v.Expansion != "collapsed" && v.Expansion != "first" && v.Expansion != "all" {
		return fmt.Errorf("expansion must be collapsed, first or all")
	}
	if v.Visibility != "" && v.Visibility != "normal" && v.Visibility != "hidden" && v.Visibility != "archived" && v.Visibility != "all" {
		return fmt.Errorf("visibility must be normal, hidden, archived or all")
	}
	return nil
}
func lookupValue(kv []KeyValue, key string) (string, bool) {
	for _, p := range kv {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}
func (r Run) Tag(key string) (string, bool)   { return lookupValue(r.Data.Tags, key) }
func (r Run) Param(key string) (string, bool) { return lookupValue(r.Data.Params, key) }
func (d DatasetInput) Context() string        { v, _ := lookupValue(d.Tags, "mlflow.data.context"); return v }

func ColumnValue(r Run, c ColumnSpec) (string, bool) {
	switch c.Kind {
	case "metric":
		v, ok := r.Metric(c.Key)
		return v.String(), ok
	case "param":
		return r.Param(c.Key)
	case "tag":
		return r.Tag(c.Key)
	case "dataset":
		var values []string
		seen := map[string]bool{}
		for _, d := range r.Inputs.DatasetInputs {
			if c.DatasetContext != "" && d.Context() != c.DatasetContext {
				continue
			}
			var v string
			switch c.Key {
			case "name":
				v = d.Dataset.Name
			case "digest":
				v = d.Dataset.Digest
			case "context":
				v = d.Context()
			case "source_type":
				v = d.Dataset.SourceType
			case "source":
				v = d.Dataset.Source
			default:
				return "", false
			}
			if !seen[v] {
				values = append(values, v)
				seen[v] = true
			}
		}
		sort.Strings(values)
		return strings.Join(values, "; "), len(values) > 0
	case "attribute":
		switch c.Key {
		case "name":
			return r.Name(), true
		case "run_id":
			return r.ID(), r.ID() != ""
		case "experiment_id":
			return r.Info.ExperimentID, r.Info.ExperimentID != ""
		case "status":
			return r.Info.Status, r.Info.Status != ""
		case "start_time":
			if r.Info.StartTime == 0 {
				return "", false
			}
			return time.UnixMilli(r.Info.StartTime).Local().Format("2006-01-02 15:04"), true
		case "end_time":
			if r.Info.EndTime == 0 {
				return "", false
			}
			return time.UnixMilli(r.Info.EndTime).Local().Format("2006-01-02 15:04"), true
		case "duration":
			if r.Info.StartTime == 0 || r.Info.EndTime == 0 {
				return "", false
			}
			return strconv.FormatFloat(float64(r.Info.EndTime-r.Info.StartTime)/1000, 'g', -1, 64) + "s", true
		case "user":
			if v, ok := r.Tag("mlflow.user"); ok {
				return v, true
			}
			return r.Info.UserID, r.Info.UserID != ""
		case "source":
			return r.Tag("mlflow.source.name")
		case "version":
			return r.Tag("mlflow.source.git.commit")
		case "description":
			return r.Tag("mlflow.note.content")
		case "parent_id":
			return r.ParentID(), r.ParentID() != ""
		case "models":
			values := []string{}
			for _, m := range r.Outputs.ModelOutputs {
				values = append(values, m.ModelID)
			}
			sort.Strings(values)
			return strings.Join(values, "; "), len(values) > 0
		}
	}
	return "", false
}
func DisplayValue(r Run, c ColumnSpec) string {
	v, ok := ColumnValue(r, c)
	if !ok {
		return "—"
	}
	return v
}

func DiscoverColumns(runs []Run, saved []ColumnSpec) []ColumnSpec {
	items := map[string]ColumnSpec{}
	add := func(c ColumnSpec) {
		if c.Width == 0 {
			c.Width = 16
		}
		items[ColumnID(c)] = c
	}
	for _, key := range attributes {
		add(ColumnSpec{Kind: "attribute", Key: key})
	}
	for _, key := range []string{"name", "digest", "context", "source_type"} {
		add(ColumnSpec{Kind: "dataset", Key: key})
	}
	for _, r := range runs {
		for _, v := range r.Data.Metrics {
			add(ColumnSpec{Kind: "metric", Key: v.Key})
		}
		for _, v := range r.Data.Params {
			add(ColumnSpec{Kind: "param", Key: v.Key})
		}
		for _, v := range r.Data.Tags {
			add(ColumnSpec{Kind: "tag", Key: v.Key})
		}
		for _, d := range r.Inputs.DatasetInputs {
			if d.Context() != "" {
				for _, key := range []string{"name", "digest"} {
					add(ColumnSpec{Kind: "dataset", Key: key, DatasetContext: d.Context()})
				}
			}
		}
	}
	for _, c := range saved {
		add(c)
	}
	out := make([]ColumnSpec, 0, len(items))
	for _, c := range items {
		out = append(out, c)
	}
	rank := map[string]int{"attribute": 0, "dataset": 1, "param": 2, "metric": 3, "tag": 4}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return ColumnID(out[i]) < ColumnID(out[j])
	})
	return out
}

type sortValue struct {
	text    string
	number  float64
	numeric bool
	rank    int
}

func valueForSort(r Run, c ColumnSpec) sortValue {
	// Numeric fields already have their full-precision value. Formatting for the
	// terminal and then parsing during sorting adds allocations without changing
	// ordering, and dates need no display text at all.
	if c.Kind == "metric" {
		number, ok := r.Metric(c.Key)
		if !ok {
			return sortValue{rank: 2}
		}
		if math.IsNaN(float64(number)) {
			return sortValue{text: "NaN", numeric: true, rank: 1}
		}
		return sortValue{number: float64(number), numeric: true}
	}
	if c.Kind == "attribute" {
		switch c.Key {
		case "start_time":
			if r.Info.StartTime == 0 {
				return sortValue{rank: 2}
			}
			return sortValue{number: float64(r.Info.StartTime), numeric: true}
		case "end_time":
			if r.Info.EndTime == 0 {
				return sortValue{rank: 2}
			}
			return sortValue{number: float64(r.Info.EndTime), numeric: true}
		case "duration":
			if r.Info.StartTime == 0 || r.Info.EndTime == 0 {
				return sortValue{rank: 2}
			}
			return sortValue{number: float64(r.Info.EndTime - r.Info.StartTime), numeric: true}
		}
	}
	text, ok := ColumnValue(r, c)
	if !ok {
		return sortValue{rank: 2}
	}
	v := sortValue{text: text, numeric: c.Numeric}

	if c.Kind == "dataset" {
		// A JSON tuple preserves the boundaries between multiple values.
		var values []string
		for _, d := range r.Inputs.DatasetInputs {
			if c.DatasetContext != "" && d.Context() != c.DatasetContext {
				continue
			}
			item := Run{Inputs: RunInputs{DatasetInputs: []DatasetInput{d}}}
			s, _ := ColumnValue(item, c)
			values = append(values, s)
		}
		sort.Strings(values)
		v.text = fmt.Sprintf("%q", values)
	}
	if v.numeric {
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(n) {
			v.rank = 1
		} else {
			v.number = n
		}
	}
	return v
}
func compareSortValues(x, y sortValue, desc bool) int {
	if x.rank < y.rank {
		return -1
	}
	if x.rank > y.rank {
		return 1
	}
	cmp := 0
	if x.numeric && x.rank == 0 {
		if x.number < y.number {
			cmp = -1
		} else if x.number > y.number {
			cmp = 1
		}
	} else {
		cmp = strings.Compare(x.text, y.text)
	}
	if desc && x.rank == 0 {
		return -cmp
	}
	return cmp
}
func SortRuns(runs []Run, order []SortSpec) []Run {
	if len(runs) < 2 {
		return append([]Run(nil), runs...)
	}
	if len(order) == 0 {
		order = DefaultView(nil, nil).Sort
	}
	// Cache each row/column value once. Sorting indices avoids repeatedly moving
	// large run structs and does not mutate the server snapshot or its metadata.
	width := len(order)
	keys := make([]sortValue, len(runs)*width)
	indices := make([]int, len(runs))
	for i, r := range runs {
		indices[i] = i
		for j, s := range order {
			keys[i*width+j] = valueForSort(r, s.Column)
		}
	}
	slices.SortStableFunc(indices, func(a, b int) int {
		for j, s := range order {
			if cmp := compareSortValues(keys[a*width+j], keys[b*width+j], s.Desc); cmp != 0 {
				return cmp
			}
		}
		return strings.Compare(runs[a].ID(), runs[b].ID())
	})
	out := make([]Run, len(runs))
	for i, index := range indices {
		out[i] = runs[index]
	}
	return out
}

func ServerOrder(order []SortSpec) ([]string, bool) {
	if len(order) == 0 {
		order = DefaultView(nil, nil).Sort
	}
	var out []string
	for _, s := range order {
		c := s.Column
		field := ""
		if c.Numeric && c.Kind != "metric" {
			return nil, false
		}
		switch c.Kind {
		case "metric", "param", "tag":
			if strings.ContainsAny(c.Key, "`\r\n\x00") {
				return nil, false
			}
			prefix := map[string]string{"metric": "metrics", "param": "params", "tag": "tags"}[c.Kind]
			field = prefix + ".`" + c.Key + "`"
		case "attribute":
			switch c.Key {
			case "name":
				field = "attributes.run_name"
			case "status", "start_time", "end_time", "run_id":
				field = "attributes." + c.Key
			case "user":
				field = "tags.`mlflow.user`"
			case "source":
				field = "tags.`mlflow.source.name`"
			case "version":
				field = "tags.`mlflow.source.git.commit`"
			case "description":
				field = "tags.`mlflow.note.content`"
			case "parent_id":
				field = "tags.`mlflow.parentRunId`"
			default:
				return nil, false
			}
		default:
			return nil, false
		}
		direction := " ASC"
		if s.Desc {
			direction = " DESC"
		}
		out = append(out, field+direction)
	}
	hasID := false
	for _, v := range out {
		if strings.HasPrefix(v, "attributes.run_id ") {
			hasID = true
		}
	}
	if !hasID {
		out = append(out, "attributes.run_id ASC")
	}
	return out, true
}
