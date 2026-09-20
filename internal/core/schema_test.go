package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTabularDatasetSchemaCountsAndProfile(t *testing.T) {
	cols := make([]map[string]any, 146)
	for i := range cols {
		cols[i] = map[string]any{"name": fmt.Sprintf("feature_%03d", i), "type": "double", "required": true}
	}
	raw, _ := json.Marshal(map[string]any{"mlflow_colspec": cols})
	s := ParseDatasetSchema(string(raw))
	if s.Error != "" || s.Kind != "tabular" || s.Columns != 146 || s.Leaves != 146 || len(s.Fields) != 146 {
		t.Fatalf("%+v", s)
	}
	if s.Fields[145].Path != "feature_145" || s.Fields[145].Required == nil || !*s.Fields[145].Required {
		t.Fatalf("%+v", s.Fields[145])
	}
	p := ParseDatasetProfile(`{"num_rows":4558,"num_elements":665468}`)
	if p.Rows == nil || *p.Rows != 4558 || p.Error != "" {
		t.Fatalf("%+v", p)
	}
	if s.Columns != 146 {
		t.Fatal("schema was changed by profile")
	}
}

func TestNestedSchemaAndFlattening(t *testing.T) {
	raw := `{"mlflow_colspec":[{"name":"特徵","type":"object","properties":{"xy":{"type":"array","items":{"type":"double"}},"map":{"type":"map","values":{"type":"object","properties":{"label":{"type":"string","required":false}}}}},"required":true},{"name":"scalar","type":"long"}]}`
	s := ParseDatasetSchema(raw)
	if s.Error != "" || s.Columns != 2 || s.Leaves != 3 {
		t.Fatalf("%+v", s)
	}
	fields := FlattenSchemaFields(s)
	paths := []string{}
	for _, f := range fields {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "特徵,特徵.map,特徵.map{},特徵.map{}.label,特徵.xy,特徵.xy[],scalar" {
		t.Fatalf("paths %v", paths)
	}
	if fields[3].Depth != 3 || fields[3].Required == nil || *fields[3].Required {
		t.Fatalf("%+v", fields[3])
	}
}

func TestTensorDatasetDoubleEncodingAndUnknownDimensions(t *testing.T) {
	features := `[{"type":"tensor","tensor-spec":{"dtype":"float32","shape":[-1,146]}}]`
	targets := `[{"name":"labels","type":"tensor","tensor-spec":{"dtype":"int64","shape":[-1]}}]`
	raw, _ := json.Marshal(map[string]any{"mlflow_tensorspec": map[string]any{"features": features, "targets": targets}})
	s := ParseDatasetSchema(string(raw))
	if s.Error != "" || s.Kind != "tensor" || s.Columns != 0 || s.Leaves != 2 {
		t.Fatalf("%+v", s)
	}
	f := s.Fields[0]
	if f.Path != "features" || f.Rank != 2 || f.Dimensions == nil || *f.Dimensions != 146 {
		t.Fatalf("%+v", f)
	}
	if s.Fields[1].Path != "targets.labels" || s.Fields[1].Dimensions != nil {
		t.Fatalf("%+v", s.Fields[1])
	}
	for _, shape := range []string{`[-1,-1,3]`, `[-1,9223372036854775807,2]`} {
		got := ParseDatasetSchema(`[{"type":"tensor","tensor-spec":{"dtype":"float32","shape":` + shape + `}}]`)
		if got.Error != "" || got.Fields[0].Dimensions != nil {
			t.Fatalf("unknown shape got %+v", got)
		}
	}
}

func TestSchemaRawFallback(t *testing.T) {
	for _, raw := range []string{`bad json`, `{"something_new":[]}`, `[{"name":"bad"}]`, `[{"type":"tensor","tensor-spec":{"shape":[1.5]}}]`, `[] []`} {
		s := ParseDatasetSchema(raw)
		if s.Error == "" || s.Raw != raw {
			t.Fatalf("missing raw fallback: %+v", s)
		}
	}
	if s := ParseDatasetSchema(""); s.Kind != "missing" || len(s.Fields) != 0 {
		t.Fatalf("%+v", s)
	}
	if p := ParseDatasetProfile(`{"num_rows":-2}`); p.Rows != nil {
		t.Fatal("negative count accepted")
	}
}

func TestDatasetIdentityCanonicalAndVariantIsolation(t *testing.T) {
	a := Dataset{Name: "features", Digest: "same", SourceType: "local", Source: `{"uri":"/data","nested":{"b":2,"a":1}}`, Schema: `{"mlflow_colspec":[{"type":"double","name":"x"}]}`, Profile: `{"num_rows":2}`}
	b := a
	b.Source = ` { "nested": { "a": 1, "b": 2 }, "uri": "/data" } `
	b.Schema = ` { "mlflow_colspec": [ {"name":"x", "type":"double"} ] } `
	b.Profile = `{"num_rows":3}`
	id := DatasetIdentity("source", a)
	if got := DatasetIdentity("source", b); got != id {
		t.Fatalf("format/profile changed identity: %s != %s", got, id)
	}
	if DatasetIdentity("other", a) == id {
		t.Fatal("source leaked")
	}
	variants := []Dataset{a, a, a, a}
	variants[0].Name = "renamed"
	variants[1].Digest = "new"
	variants[2].Source = `{"uri":"other"}`
	variants[3].Schema = `{"mlflow_colspec":[]}`
	for _, d := range variants {
		if DatasetIdentity("source", d) == id {
			t.Fatalf("variant collapsed %+v", d)
		}
	}
}

func TestDatasetIdentityPreservesLiteralSourceAndSchemaTypes(t *testing.T) {
	a := Dataset{Name: "features", Digest: "same", SourceType: "local", Source: `{"uri":"{\"x\":1}"}`, Schema: `{"mlflow_colspec":[{"name":"{\"x\":1}","type":"double"}]}`}
	for _, source := range []string{`{"uri":{"x":1}}`, `{"uri":"{ \"x\": 1 }"}`} {
		b := a
		b.Source = source
		if DatasetIdentity("source", a) == DatasetIdentity("source", b) {
			t.Fatalf("literal source collapsed: %s", source)
		}
	}
	for _, schema := range []string{
		`{"mlflow_colspec":[{"name":{"x":1},"type":"double"}]}`,
		`{"mlflow_colspec":[{"name":"{ \"x\": 1 }","type":"double"}]}`,
		`{"mlflow_colspec":"[{\"name\":\"{\\\"x\\\":1}\",\"type\":\"double\"}]"}`,
	} {
		b := a
		b.Schema = schema
		if DatasetIdentity("source", a) == DatasetIdentity("source", b) {
			t.Fatalf("literal schema type/name collapsed: %s", schema)
		}
	}
}

func TestDatasetIdentityCanonicalTensorEncodingPreservesStrings(t *testing.T) {
	a := Dataset{Name: "tensor", Digest: "same", Schema: `{"mlflow_tensorspec":{"features":"[{\"type\":\"tensor\",\"name\":\"{\\\"x\\\":1}\",\"tensor-spec\":{\"dtype\":\"float32\",\"shape\":[-1,9007199254740993]}}]","targets":null}}`}
	b := a
	b.Schema = `{"mlflow_tensorspec":{"targets":null,"features":" [ { \"tensor-spec\": { \"shape\": [-1,9007199254740993], \"dtype\": \"float32\" }, \"name\":\"{\\\"x\\\":1}\", \"type\": \"tensor\" } ] "}}`
	if DatasetIdentity("source", a) != DatasetIdentity("source", b) {
		t.Fatal("known encoded tensor whitespace changed identity")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(a.Schema), &decoded); err != nil {
		t.Fatal(err)
	}
	tensor := decoded["mlflow_tensorspec"].(map[string]any)
	fields, err := decodeJSON(tensor["features"].(string))
	if err != nil {
		t.Fatal(err)
	}
	tensor["features"] = fields
	raw, _ := json.Marshal(decoded)
	b.Schema = string(raw)
	if DatasetIdentity("source", a) == DatasetIdentity("source", b) {
		t.Fatal("encoded schema string collapsed into array")
	}
	b = a
	b.Schema = strings.Replace(a.Schema, "9007199254740993", "9007199254740992", 1)
	if DatasetIdentity("source", a) == DatasetIdentity("source", b) {
		t.Fatal("numeric precision was lost")
	}
	c := a
	c.Schema = strings.Replace(a.Schema, "[-1,9007199254740993]", "[9007199254740993,-1]", 1)
	if DatasetIdentity("source", a) == DatasetIdentity("source", c) {
		t.Fatal("schema array order was lost")
	}
}
