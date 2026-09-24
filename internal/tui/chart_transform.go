package tui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type chartDerived struct {
	Key                          string
	Step, Time, RawStep, RawTime []curvePoint
	Values                       []float64 // canonical raw-history indices; only display values
	Bytes                        int
}
type chartTransformedMsg struct {
	Source, Key string
	Ticket      uint64
	Derived     *chartDerived
	Err         error
}
type chartDistance struct {
	Point     core.Metric
	Steps     uint64
	Samples   int
	Elapsed   time.Duration
	ElapsedMS int64
	TimeKnown bool
	Valid     bool
}
type chartExtrema struct{ Min, Max chartDistance }

func deriveExtrema(raw []core.Metric, summary core.MetricSummary) chartExtrema {
	distance := func(point *core.Metric) chartDistance {
		if point == nil {
			return chartDistance{}
		}
		index := -1
		for i, m := range raw {
			if sameHistorySample(m, *point) {
				index = i
				break
			}
		}
		if index < 0 {
			return chartDistance{}
		}
		d := chartDistance{Point: *point, Steps: uint64(summary.MaxStep) - uint64(point.Step), Samples: len(raw) - index - 1, Valid: true}
		d.TimeKnown = point.Timestamp > 0 && summary.EndTimestamp > 0
		if d.TimeKnown {
			d.ElapsedMS = summary.EndTimestamp - point.Timestamp
			if d.ElapsedMS <= math.MaxInt64/int64(time.Millisecond) {
				d.Elapsed = time.Duration(d.ElapsedMS) * time.Millisecond
			}
		}
		return d
	}
	return chartExtrema{distance(summary.Min), distance(summary.Max)}
}

func chartTransformKey(revision uint64, p chartProfile) string {
	span := 0
	if p.Smooth {
		span = p.span()
	}
	return fmt.Sprintf("%d/%d/%t", revision, span, p.Normalize)
}

func displayChartValue(value float64, summary core.MetricSummary, normalize bool) float64 {
	if !normalize || math.IsNaN(value) || math.IsInf(value, 0) || summary.Min == nil {
		return value
	}
	lo, hi := float64(summary.Min.Value), float64(summary.Max.Value)
	if lo == hi {
		return .5
	}
	return curveFraction(value, lo, hi)
}

// Smoothing always follows step/timestamp order, before display sampling.
// Changing the X axis only reorders existing derived values. Nonfinite records
// remain gaps and reset the EMA rather than carrying state across a gap.
func transformChart(ctx context.Context, raw []core.Metric, summary core.MetricSummary, start int64, profile chartProfile) (*chartDerived, error) {
	transformed := append([]core.Metric(nil), raw...)
	values := make([]float64, len(raw))
	alpha := 2 / float64(profile.span()+1)
	previous, havePrevious := 0.0, false
	for i, metric := range raw {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		value := float64(metric.Value)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			havePrevious = false
		} else if profile.Smooth {
			if havePrevious {
				value = alpha*value + (1-alpha)*previous
			}
			previous, havePrevious = value, true
		}
		value = displayChartValue(value, summary, profile.Normalize)
		values[i] = value
		transformed[i].Value = core.Number(value)
	}
	order := make([]int, len(raw))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return raw[order[i]].Timestamp < raw[order[j]].Timestamp })
	sampled := core.SampleHistory(transformed, 2400)
	result := &chartDerived{Values: values}
	result.Step = prepareSampledCurve(transformed, sampled, nil, start, false)
	result.Time = prepareSampledCurve(transformed, sampled, order, start, true)
	rawSample := core.SampleHistory(raw, 2400)
	result.RawStep = prepareSampledCurve(raw, rawSample, nil, start, false)
	result.RawTime = prepareSampledCurve(raw, rawSample, order, start, true)
	for _, points := range [][]curvePoint{result.RawStep, result.RawTime} {
		for i := range points {
			points[i].Y = displayChartValue(points[i].Y, summary, profile.Normalize)
		}
	}
	result.Bytes = len(values)*8 + (len(result.Step)+len(result.Time)+len(result.RawStep)+len(result.RawTime))*24
	return result, nil
}

func (m *model) ensureChartTransforms() tea.Cmd {
	if m.inspect == nil {
		return nil
	}
	profile := m.currentChartProfile()
	desired := m.desiredHistories()
	var cmds []tea.Cmd
	for key, e := range m.inspect.Histories {
		_, active := desired[key]
		want := chartTransformKey(e.Revision, profile)
		needed := active && (profile.Smooth || profile.Normalize) && !e.Updated.IsZero()
		if e.TransformPending && (!needed || e.TransformKey != want) {
			if e.TransformCancel != nil {
				e.TransformCancel()
			}
			e.TransformTicket++
			e.TransformPending = false
		}
		if !needed || e.TransformPending || e.Derived != nil && e.Derived.Key == want {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		e.TransformCancel = cancel
		e.TransformPending = true
		e.TransformKey = want
		e.TransformTicket++
		ticket, source, raw, summary, start, slots := e.TransformTicket, m.historyNamespace(), e.History, e.Summary, e.StartTime, m.inspect.Slots
		cmds = append(cmds, func() tea.Msg {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return chartTransformedMsg{Source: source, Key: key, Ticket: ticket, Err: ctx.Err()}
			}
			derived, err := transformChart(ctx, raw, summary, start, profile)
			if derived != nil {
				derived.Key = want
			}
			return chartTransformedMsg{source, key, ticket, derived, err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) acceptChartTransform(v chartTransformedMsg) tea.Cmd {
	e := m.inspect.Histories[v.Key]
	if e == nil || !e.TransformPending || e.TransformTicket != v.Ticket || v.Source != m.historyNamespace() {
		return nil
	}
	e.TransformPending = false
	if e.TransformCancel != nil {
		e.TransformCancel()
		e.TransformCancel = nil
	}
	if v.Err != nil {
		if v.Err != context.Canceled {
			m.status = "Chart transform failed: " + v.Err.Error()
		}
		return nil
	}
	e.Derived = v.Derived
	m.evictHistories(m.desiredHistories())
	return nil
}

func distanceText(label string, d chartDistance) string {
	if !d.Valid {
		return label + " unavailable"
	}
	elapsed := "—"
	if d.TimeKnown {
		elapsed = d.Elapsed.Truncate(time.Millisecond).String()
		if d.ElapsedMS > math.MaxInt64/int64(time.Millisecond) {
			elapsed = fmt.Sprintf("%dd %dh", d.ElapsedMS/86400000, (d.ElapsedMS%86400000)/3600000)
		}
	}
	return fmt.Sprintf("%s %s @%d · Δ%d steps / %d samples / %s", label, d.Point.Value.String(), d.Point.Step, d.Steps, d.Samples, elapsed)
}
