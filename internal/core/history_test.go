package core

import (
	"math"
	"testing"
)

func TestHistoryStatisticsAndSampling(t *testing.T) {
	h := []Metric{{Step: 3, Timestamp: 4, Value: 2}, {Step: 1, Timestamp: 1, Value: 0}, {Step: 2, Timestamp: 2, Value: Number(math.NaN())}, {Step: 3, Timestamp: 3, Value: 9}, {Step: 4, Timestamp: 5, Value: -1}}
	s := SummarizeHistory(h)
	if s.Count != 5 || s.FiniteCount != 4 || s.NonFiniteCount != 1 || s.First.Value != 0 || s.Last.Value != -1 || s.Min.Value != -1 || s.Max.Value != 9 {
		t.Fatalf("%+v", s)
	}
	sample := SampleHistory(h, 4)
	if len(sample) != 4 || sample[0].Step != 1 || sample[3].Step != 4 {
		t.Fatalf("%+v", sample)
	}
	if h[0].Step != 3 {
		t.Fatal("modified caller")
	}
	for i := 1; i < len(sample); i++ {
		if metricEarlier(sample[i], sample[i-1]) {
			t.Fatal("unordered")
		}
	}
}
func TestHistoryEmptyNonfiniteAndRepeatedSteps(t *testing.T) {
	if SummarizeHistory(nil).Min != nil {
		t.Fatal("empty min")
	}
	s := SummarizeHistory([]Metric{{Value: Number(math.Inf(1))}})
	if s.FiniteCount != 0 || s.Min != nil {
		t.Fatalf("%+v", s)
	}
	h := []Metric{{Step: 2, Timestamp: 9, Value: 2}, {Step: 2, Timestamp: 1, Value: 1}}
	if got := SampleHistory(h, 10); len(got) != 2 || got[0].Timestamp != 1 {
		t.Fatalf("%+v", got)
	}
}
