// Package latency 面板延遲統計（對應 Python app/discord_bot/latency.py）：每次開啟面板、換頁、操作從 Bot 收到互動到
// 畫面送出的耗時，定期彙總寫入日誌。只存在記憶體（不寫資料庫），Bot 重啟即歸零。
// Go 的處理函式在多個 goroutine 執行，所以加鎖（Python 只在事件迴圈中呼叫，不需要鎖）。
package latency

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SlowMS             = 3000 // 單次超過就立刻記一筆警告（玩家明顯感覺卡）
	MaxSamples         = 5000 // 每個項目每段期間最多保留的樣本；超過只累計次數、總和與最大值
	Top                = 15   // 彙總時列出次數最多的項目數
	LogIntervalSeconds = 600  // 每 10 分鐘寫一次彙總（期間沒有操作就不寫）
)

// Percentile 最近排名法；values 需已排序且不可為空。
func Percentile(values []float64, q float64) float64 {
	k := max(0, min(len(values)-1, int(math.Ceil(q*float64(len(values))))-1))
	return values[k]
}

type entry struct {
	samples         []float64
	count, deferred int
	total, max      float64
}

// Row 彙總的一列。
type Row struct {
	Name               string
	Count, Deferred    int
	Avg, P50, P95, Max float64
}

// Stats 延遲統計。可同時被多個 goroutine 使用。
type Stats struct {
	mu    sync.Mutex
	data  map[string]*entry
	order []string // 項目第一次出現的順序（Python dict）
	since time.Time
	Log   *slog.Logger // nil：slog.Default()
}

// New 建立統計。
func New() *Stats { return &Stats{data: map[string]*entry{}, since: time.Now()} }

func (s *Stats) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Record 記錄一次耗時（毫秒）；deferred 表示這次先 defer 才更新畫面。
func (s *Stats) Record(name string, ms float64, deferred bool) {
	s.mu.Lock()
	d, ok := s.data[name]
	if !ok {
		d = &entry{}
		s.data[name] = d
		s.order = append(s.order, name)
	}
	d.count++
	d.total += ms
	d.max = max(d.max, ms)
	if deferred {
		d.deferred++
	}
	if len(d.samples) < MaxSamples {
		d.samples = append(d.samples, ms)
	}
	s.mu.Unlock()
	if ms >= SlowMS {
		note := ""
		if deferred {
			note = "，已先 defer"
		}
		s.log().Warn(fmt.Sprintf("面板延遲偏高 %.0f ms（%s%s）", ms, name, note))
	}
}

// Summary 各項目的次數、平均、P50、P95、最大與 defer 次數，依次數由多到少；最前面一列是全部合計。
func (s *Stats) Summary() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaryLocked()
}

func (s *Stats) summaryLocked() []Row {
	if len(s.order) == 0 {
		return nil
	}
	var rows []Row
	var everything []float64
	total := Row{Name: "全部"}
	sum := 0.0
	for _, name := range s.order {
		d := s.data[name]
		values := append([]float64(nil), d.samples...)
		sort.Float64s(values)
		everything = append(everything, values...)
		rows = append(rows, Row{Name: name, Count: d.count, Deferred: d.deferred, Avg: d.total / float64(d.count),
			P50: Percentile(values, 0.5), P95: Percentile(values, 0.95), Max: d.max})
		total.Count += d.count
		total.Deferred += d.deferred
		sum += d.total
		total.Max = max(total.Max, d.max)
	}
	sort.Float64s(everything)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	total.Avg = sum / float64(total.Count)
	total.P50, total.P95 = Percentile(everything, 0.5), Percentile(everything, 0.95)
	return append([]Row{total}, rows...)
}

// LogAndReset 寫入彙總並歸零（期間沒有操作就不寫）。
func (s *Stats) LogAndReset() {
	s.mu.Lock()
	rows := s.summaryLocked()
	minutes := time.Since(s.since).Minutes()
	s.data, s.order, s.since = map[string]*entry{}, nil, time.Now()
	s.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	lines := []string{fmt.Sprintf("面板延遲（最近 %.0f 分鐘，單位 ms；次數 平均 P50 P95 最大 先defer）：", minutes)}
	for _, r := range rows[:min(len(rows), Top+1)] {
		lines = append(lines, fmt.Sprintf("  %s：%d 次 %.0f %.0f %.0f %.0f defer %d", r.Name, r.Count, r.Avg, r.P50, r.P95, r.Max, r.Deferred))
	}
	if len(rows) > Top+1 {
		lines = append(lines, fmt.Sprintf("  （另有 %d 項較少的）", len(rows)-Top-1))
	}
	s.log().Info(strings.Join(lines, "\n"))
}
