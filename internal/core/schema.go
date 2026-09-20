package core

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

type SchemaField struct {
	Index      int           `json:"index"`
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Type       string        `json:"type"`
	Required   *bool         `json:"required,omitempty"`
	Shape      []int64       `json:"shape,omitempty"`
	Rank       int           `json:"rank,omitempty"`
	Dimensions *int64        `json:"dimensions,omitempty"`
	Depth      int           `json:"depth,omitempty"`
	Children   []SchemaField `json:"children,omitempty"`
}

type DatasetSchema struct {
	Kind    string        `json:"kind"`
	Columns int           `json:"columns"`
	Leaves  int           `json:"leaves"`
	Fields  []SchemaField `json:"fields"`
	Raw     string        `json:"raw,omitempty"`
	Error   string        `json:"error,omitempty"`
}

type DatasetProfile struct {
	Rows   *int64         `json:"rows,omitempty"`
	Values map[string]any `json:"values,omitempty"`
	Raw    string         `json:"raw,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// decodeJSON preserves JSON value types and exact numeric literals.
func decodeJSON(raw string) (any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return v, nil
}

func decodeSchemaJSON(raw string) (any, error) {
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	// TensorDatasetSchema embeds JSON strings produced by Schema.to_json().
	for depth := 0; depth < 4; depth++ {
		s, ok := v.(string)
		if !ok {
			return v, nil
		}
		v, err = decodeJSON(s)
		if err != nil {
			return nil, err
		}
	}
	return v, nil
}

func ParseDatasetProfile(raw string) DatasetProfile {
	p := DatasetProfile{Raw: raw}
	if strings.TrimSpace(raw) == "" {
		return p
	}
	v, err := decodeSchemaJSON(raw)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Values, _ = v.(map[string]any)
	if p.Values == nil {
		p.Error = "profile is not a JSON object"
		return p
	}
	for _, key := range []string{"num_rows", "row_count"} {
		if n, ok := integerValue(p.Values[key]); ok && n >= 0 {
			p.Rows = &n
			break
		}
	}
	return p
}

// ParseDatasetSchema retains raw content on unknown formats. Columns counts
// top-level tabular columns; Leaves counts nested endpoints, not tensor rank.
func ParseDatasetSchema(raw string) DatasetSchema {
	s := DatasetSchema{Kind: "unknown", Fields: []SchemaField{}, Raw: raw}
	if strings.TrimSpace(raw) == "" {
		s.Kind = "missing"
		return s
	}
	v, err := decodeSchemaJSON(raw)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	if object, ok := v.(map[string]any); ok {
		if tensor, exists := object["mlflow_tensorspec"]; exists {
			var valid bool
			object, valid = tensor.(map[string]any)
			if !valid {
				s.Error = "invalid mlflow_tensorspec object"
				return s
			}
		}
		if cols, ok := object["mlflow_colspec"]; ok {
			v = cols
		} else if _, ok := object["features"]; ok {
			s.Kind = "tensor"
			for _, name := range []string{"features", "targets"} {
				if value, exists := object[name]; exists && value != nil {
					fields, e := parseSchemaList(value, name)
					if e != nil {
						s.Error = e.Error()
						return s
					}
					s.Fields = append(s.Fields, fields...)
				}
			}
			if len(s.Fields) == 0 {
				s.Error = "tensor schema has no fields"
			}
			s.Leaves = countLeaves(s.Fields)
			return s
		} else {
			s.Error = "unrecognized dataset schema object"
			return s
		}
	}
	fields, err := parseSchemaList(v, "")
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Fields = fields
	s.Kind = "tabular"
	s.Columns = len(fields)
	for _, f := range fields {
		if strings.HasPrefix(f.Type, "tensor") {
			s.Kind = "tensor"
			s.Columns = 0
			break
		}
	}
	s.Leaves = countLeaves(fields)
	return s
}

func parseSchemaList(v any, prefix string) ([]SchemaField, error) {
	if raw, ok := v.(string); ok {
		var err error
		v, err = decodeSchemaJSON(raw)
		if err != nil {
			return nil, err
		}
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("schema fields must be a JSON array")
	}
	fields := make([]SchemaField, 0, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema field %d must be an object", i+1)
		}
		name, _ := obj["name"].(string)
		if name == "" {
			if prefix != "" && len(list) == 1 {
				name = prefix
			} else {
				name = strconv.Itoa(i)
			}
		}
		path := name
		if prefix != "" && name != prefix {
			path = prefix + "." + name
		}
		f, err := parseSchemaField(obj, name, path, 0)
		if err != nil {
			return nil, err
		}
		f.Index = i + 1
		fields = append(fields, f)
	}
	return fields, nil
}

func parseSchemaField(obj map[string]any, name, path string, depth int) (SchemaField, error) {
	f := SchemaField{Name: name, Path: path, Depth: depth}
	if required, ok := obj["required"].(bool); ok {
		f.Required = &required
	}
	typeValue := obj["type"]
	if nested, ok := typeValue.(map[string]any); ok {
		obj = nested
		typeValue = nested["type"]
	}
	f.Type, _ = typeValue.(string)
	if f.Type == "" {
		return f, fmt.Errorf("field %s has no type", path)
	}
	if f.Type == "tensor" {
		spec, _ := obj["tensor-spec"].(map[string]any)
		if spec == nil {
			spec = obj
		}
		if dtype, ok := spec["dtype"].(string); ok {
			f.Type = "tensor[" + dtype + "]"
		}
		shape, ok := spec["shape"].([]any)
		if !ok {
			return f, fmt.Errorf("tensor %s has no shape", path)
		}
		product := int64(1)
		known := true
		for i, value := range shape {
			n, ok := integerValue(value)
			if !ok {
				return f, fmt.Errorf("invalid tensor dimension for %s", path)
			}
			f.Shape = append(f.Shape, n)
			// The first axis is the observation count, never a feature dimension.
			if i > 0 {
				if n < 0 || n != 0 && product > math.MaxInt64/n {
					known = false
				} else {
					product *= n
				}
			}
		}
		f.Rank = len(shape)
		if known && len(shape) > 1 {
			f.Dimensions = &product
		}
		return f, nil
	}
	if depth >= 64 {
		return f, fmt.Errorf("schema nesting exceeds 64 levels")
	}
	switch f.Type {
	case "object":
		properties, ok := obj["properties"].(map[string]any)
		if !ok {
			return f, fmt.Errorf("object %s has invalid properties", path)
		}
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, childName := range names {
			child, ok := properties[childName].(map[string]any)
			if !ok {
				return f, fmt.Errorf("property %s has invalid type", childName)
			}
			cf, err := parseSchemaField(child, childName, path+"."+childName, depth+1)
			if err != nil {
				return f, err
			}
			f.Children = append(f.Children, cf)
		}
	case "array", "map":
		key, suffix := "items", "[]"
		if f.Type == "map" {
			key, suffix = "values", "{}"
		}
		child := obj[key]
		if child != nil {
			co, ok := child.(map[string]any)
			if !ok {
				co = map[string]any{"type": child}
			}
			cf, err := parseSchemaField(co, suffix, path+suffix, depth+1)
			if err != nil {
				return f, err
			}
			f.Children = []SchemaField{cf}
		}
	}
	return f, nil
}

func integerValue(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < math.MinInt64 || n >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), n == math.Trunc(n)
	case int64:
		return n, true
	}
	return 0, false
}

func countLeaves(fields []SchemaField) int {
	n := 0
	for _, f := range fields {
		if len(f.Children) == 0 {
			n++
		} else {
			n += countLeaves(f.Children)
		}
	}
	return n
}

func FlattenSchemaFields(schema DatasetSchema) []SchemaField {
	result := []SchemaField{}
	var walk func([]SchemaField, int)
	walk = func(fields []SchemaField, depth int) {
		for _, f := range fields {
			f.Depth = depth
			f.Index = len(result) + 1
			result = append(result, f)
			walk(f.Children, depth+1)
		}
	}
	walk(schema.Fields, 0)
	return result
}

// canonicalSourceJSON only normalizes JSON formatting. In particular, source
// URIs and arbitrary string values remain literal strings, including whitespace.
func canonicalSourceJSON(raw string) string {
	v, err := decodeJSON(raw)
	if err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

// canonicalSchemaJSON normalizes only documented MLflow encoded schema fields.
// Encoded strings stay strings: a string containing JSON is not an object or
// array, and literal field names and metadata are never recursively interpreted.
func canonicalSchemaJSON(raw string) string {
	v, err := decodeJSON(raw)
	if err != nil {
		return raw
	}
	var normalizeRoot func(any, int) any
	normalizeRoot = func(v any, depth int) any {
		if depth >= 4 {
			return v
		}
		if encoded, ok := v.(string); ok {
			inner, err := decodeJSON(encoded)
			if err != nil {
				return v
			}
			canonical, err := json.Marshal(normalizeRoot(inner, depth+1))
			if err == nil {
				return string(canonical)
			}
			return v
		}
		object, ok := v.(map[string]any)
		if !ok {
			return v
		}
		// Some stored column schemas also encode their array as a JSON string.
		if encoded, ok := object["mlflow_colspec"].(string); ok {
			object["mlflow_colspec"] = canonicalSourceJSON(encoded)
		}
		if tensor, ok := object["mlflow_tensorspec"].(map[string]any); ok {
			for _, name := range []string{"features", "targets"} {
				if encoded, ok := tensor[name].(string); ok {
					tensor[name] = canonicalSourceJSON(encoded)
				}
			}
		}
		return object
	}
	b, err := json.Marshal(normalizeRoot(v, 0))
	if err != nil {
		return raw
	}
	return string(b)
}
