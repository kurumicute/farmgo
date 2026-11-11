package latency

// 對應 Python tests/test_latency.py 的 test_percentile_and_summary（面板的記錄點在面板那一批移植）。

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestPercentileAndSummary(t *testing.T) {
	ten := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if Percentile(ten, 0.5) != 5 || Percentile(ten, 0.95) != 10 || Percentile([]float64{7}, 0.95) != 7 {
		t.Fatal("percentile")
	}
	var buf bytes.Buffer
	s := New()
	s.Log = slog.New(slog.NewTextHandler(&buf, nil))
	if s.Summary() != nil {
		t.Fatal("空的統計")
	}
	for ms := 1; ms <= 100; ms++ {
		s.Record("換頁 main", float64(ms), false)
	}
	s.Record("操作 plots", 50, true)
	rows := s.Summary()
	if len(rows) != 3 || rows[0].Name != "全部" || rows[1].Name != "換頁 main" || rows[2].Name != "操作 plots" {
		t.Fatal(rows)
	}
	if rows[0].Count != 101 || rows[0].Deferred != 1 || rows[0].Max != 100 {
		t.Fatalf("%+v", rows[0])
	}
	if m := rows[1]; m.Avg != 50.5 || m.P50 != 50 || m.P95 != 95 {
		t.Fatalf("%+v", m)
	}
	s.Record("操作 plots", SlowMS+1, false)
	if !strings.Contains(buf.String(), "面板延遲偏高") {
		t.Fatal(buf.String())
	}
	s.LogAndReset()
	if out := buf.String(); !strings.Contains(out, "面板延遲（最近") || !strings.Contains(out, "換頁 main：100 次") {
		t.Fatal(out)
	}
	if s.Summary() != nil {
		t.Fatal("應已歸零")
	}
}
