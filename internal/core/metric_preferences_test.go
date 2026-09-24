package core

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestMetricPinsValidationAndViewRoundTrip(t *testing.T) {
	for _, keys := range [][]string{{""}, {"loss", "loss"}, {"bad\nkey"}, {"bad\x00key"}} {
		v := DefaultView(nil, nil)
		v.MetricPins = keys
		if err := ValidateView(v); err == nil {
			t.Fatalf("accepted invalid metric pins: %q", keys)
		}
	}
	v := DefaultView(nil, nil)
	v.MetricPins = []string{"Inference 2026/valid_corr", "valid_loss", "system/cpu"}
	if err := ValidateView(v); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExperimentView
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(NormalizeView(decoded).MetricPins, v.MetricPins) {
		t.Fatal("view round trip changed exact metric keys or pin order")
	}
	var old ExperimentView
	if err := json.Unmarshal([]byte(`{"columns":[],"sort":[]}`), &old); err != nil || len(NormalizeView(old).MetricPins) != 0 {
		t.Fatal("existing views require metric pins migration")
	}
}
