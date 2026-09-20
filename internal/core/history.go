package core

import (
	"math"
	"sort"
)

// MetricSummary describes the complete logged history, never the screen sample.
// First and Last use step/timestamp order; Latest remains a separate server value.
// Non-finite records count as samples but do not participate in numeric statistics.
type MetricSummary struct {
	Count          int     `json:"count"`
	FiniteCount    int     `json:"finite_count"`
	NonFiniteCount int     `json:"non_finite_count"`
	First          *Metric `json:"first,omitempty"`
	Last           *Metric `json:"last,omitempty"`
	Min            *Metric `json:"min,omitempty"`
	Max            *Metric `json:"max,omitempty"`
	MinStep        int64   `json:"min_step"`
	MaxStep        int64   `json:"max_step"`
	StartTimestamp int64   `json:"start_timestamp"`
	EndTimestamp   int64   `json:"end_timestamp"`
}

func metricEarlier(a, b Metric) bool {
	return a.Step < b.Step || a.Step == b.Step && a.Timestamp < b.Timestamp
}
func finiteMetric(m Metric) bool {
	return !math.IsNaN(float64(m.Value)) && !math.IsInf(float64(m.Value), 0)
}
func SummarizeHistory(history []Metric) MetricSummary {
	s := MetricSummary{Count: len(history)}
	for i, m := range history {
		if i == 0 || m.Step < s.MinStep {
			s.MinStep = m.Step
		}
		if i == 0 || m.Step > s.MaxStep {
			s.MaxStep = m.Step
		}
		if i == 0 || m.Timestamp < s.StartTimestamp {
			s.StartTimestamp = m.Timestamp
		}
		if i == 0 || m.Timestamp > s.EndTimestamp {
			s.EndTimestamp = m.Timestamp
		}
		if !finiteMetric(m) {
			s.NonFiniteCount++
			continue
		}
		s.FiniteCount++
		if s.First == nil || metricEarlier(m, *s.First) {
			v := m
			s.First = &v
		}
		if s.Last == nil || !metricEarlier(m, *s.Last) {
			v := m
			s.Last = &v
		}
		if s.Min == nil || m.Value < s.Min.Value {
			v := m
			s.Min = &v
		}
		if s.Max == nil || m.Value > s.Max.Value {
			v := m
			s.Max = &v
		}
	}
	return s
}

// SampleHistory returns a stable step/timestamp-ordered copy. It preserves the
// endpoints and global finite extrema when limit permits, then bucket extrema
// and non-finite markers. It never changes or aggregates the original samples.
func SampleHistory(history []Metric, limit int) []Metric {
	ordered := append([]Metric(nil), history...)
	sort.SliceStable(ordered, func(i, j int) bool { return metricEarlier(ordered[i], ordered[j]) })
	if limit <= 0 || len(ordered) <= limit {
		return ordered
	}
	if limit == 1 {
		return []Metric{ordered[len(ordered)-1]}
	}
	chosen := map[int]bool{0: true, len(ordered) - 1: true}
	add := func(i int) {
		if i >= 0 && len(chosen) < limit {
			chosen[i] = true
		}
	}
	minIdx, maxIdx := -1, -1
	for i, m := range ordered {
		if !finiteMetric(m) {
			continue
		}
		if minIdx < 0 || m.Value < ordered[minIdx].Value {
			minIdx = i
		}
		if maxIdx < 0 || m.Value > ordered[maxIdx].Value {
			maxIdx = i
		}
	}
	add(minIdx)
	add(maxIdx)
	buckets := max(1, (limit-len(chosen))/3)
	for b := 0; b < buckets; b++ {
		lo := b * len(ordered) / buckets
		hi := (b + 1) * len(ordered) / buckets
		mi, ma, gap := -1, -1, -1
		for i := lo; i < hi; i++ {
			m := ordered[i]
			if !finiteMetric(m) {
				if gap < 0 {
					gap = i
				}
				continue
			}
			if mi < 0 || m.Value < ordered[mi].Value {
				mi = i
			}
			if ma < 0 || m.Value > ordered[ma].Value {
				ma = i
			}
		}
		add(gap)
		add(mi)
		add(ma)
	}
	for i := 1; len(chosen) < limit && i < limit; i++ {
		add(i * (len(ordered) - 1) / (limit - 1))
	}
	for i := 0; len(chosen) < limit && i < len(ordered); i++ {
		add(i)
	}
	indices := make([]int, 0, len(chosen))
	for i := range chosen {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]Metric, 0, len(indices))
	for _, i := range indices {
		out = append(out, ordered[i])
	}
	return out
}
