// Package clock 時間來源抽象：正式環境用系統 UTC 時間，測試可注入可控制的時鐘。
// 對應 Python app/clock.py。
package clock

import (
	"sync"
	"time"
)

// TaipeiTZ 台灣無日光節約時間，固定 UTC+8 即可，不需額外的時區資料。
var TaipeiTZ = time.FixedZone("Asia/Taipei", 8*3600)

// Clock 時間來源。
type Clock interface {
	Now() time.Time
}

// System 正式環境使用的系統時鐘（UTC）。
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Manual 測試用時鐘，可手動前進，避免真實等待作物生長。
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

// DefaultStart 測試時鐘的預設起點：2026-01-01 00:00 UTC（台灣週四 08:00）。
var DefaultStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// NewManual 建立手動時鐘；start 為零值時使用 DefaultStart。
func NewManual(start time.Time) *Manual {
	if start.IsZero() {
		start = DefaultStart
	}
	return &Manual{now: start.UTC()}
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Advance 前進指定秒數（可為小數）。
func (m *Manual) Advance(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(time.Duration(seconds * float64(time.Second)))
}

// Set 直接設定目前時間。
func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
}
