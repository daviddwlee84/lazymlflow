package core

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNumberJSON(t *testing.T) {
	for _, s := range []string{`"NaN"`, `"Infinity"`, `"-Infinity"`, `1.5`} {
		var n Number
		if err := json.Unmarshal([]byte(s), &n); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(n)
		if err != nil || string(b) != s {
			t.Fatal(string(b), err)
		}
	}
	var n Number
	if err := json.Unmarshal([]byte(`"bad"`), &n); err == nil {
		t.Fatal("accepted invalid metric")
	}
	if !math.IsNaN(float64(Number(math.NaN()))) {
		t.Fatal("not NaN")
	}
}
