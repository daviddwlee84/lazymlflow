package tui

import (
	"fmt"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"testing"
)

func BenchmarkDashboardLoadedRuns(b *testing.B) {
	m, _ := readyModel()
	m.width = 160
	m.height = 45
	m.focus = 1
	r := m.runs()
	r.Rows = make([]core.Run, 10000)
	for i := range r.Rows {
		r.Rows[i] = sampleRun(fmt.Sprintf("run-%05d", i))
		r.Rows[i].Info.StartTime = int64(i)
	}
	m.selectRun(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.View()
	}
}
