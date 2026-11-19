// Package auction 每週系統拍賣的規則（Go 版新增，2026-11-19，金幣回收第二波）。資料在 auctions、auction_bids（Migration 0046）。
//
//   - 每週一 00:00（台灣時間）開一場，週日 EndHour 點結標，拍 1 件限定商品（Items 依週次輪流；流標時下週換下一件，起標價不變）。
//   - 出價時金幣先凍結（直接從出價者扣除），被超過時全數退回；自己加價只扣差額。得標金額全數回收，不給任何玩家。
//   - 結標前 ExtendMinutes 分鐘內有人出價，結標時間延後 ExtendMinutes 分鐘（防止最後一秒搶標）。
//   - 數值可在管理網站「每週拍賣」調整（featurecfg key = ConfigKey）。
package auction

import (
	"strings"
	"sync"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const ConfigKey = "auction"

// 商品種類（auctions.item_kind）。
const (
	KindDecor = "decor"
	KindBadge = "badge"
	KindTitle = "title"
)

// Item 一件拍賣品。
type Item struct {
	Kind, Code string
}

func (it Item) String() string { return it.Kind + ":" + it.Code }

// Config 目前生效的設定（唯讀）。
type Config struct {
	StartPrice      int
	MinIncrementPct int
	ExtendMinutes   int
	EndHour         int // 週日幾點結標（台灣時間）
	Items           []Item
}

// DefaultItems 預設的輪替順序。
var DefaultItems = []string{"decor:golden_windmill", "badge:dragon", "decor:crystal_fountain", "title:拍賣之王",
	"decor:rainbow_arch", "badge:unicorn", "decor:dragon_statue"}

func defaults() featurecfg.Value {
	items := make([]any, len(DefaultItems))
	for i, x := range DefaultItems {
		items[i] = x
	}
	return featurecfg.Value{"start_price": 50_000, "min_increment_pct": 5, "extend_minutes": 5, "end_hour": 21, "items": items}
}

// ItemValidator 由服務層提供：檢查商品存在並回傳名稱（避免規則套件依賴裝飾、名牌等目錄的細節）。
var ItemValidator func(kind, code string) (name string, ok bool)

// ParseItem "kind:code" → Item。
func ParseItem(s string) (Item, error) {
	kind, code, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || code == "" || (kind != KindDecor && kind != KindBadge && kind != KindTitle) {
		return Item{}, gameerr.Validationf("拍賣品「%s」的格式不正確，請用 decor:代碼、badge:代碼 或 title:稱號。", s)
	}
	if kind == KindTitle && len([]rune(code)) > 20 {
		return Item{}, gameerr.Validationf("拍賣稱號「%s」最多 20 個字。", code)
	}
	if ItemValidator != nil {
		if _, ok := ItemValidator(kind, code); !ok {
			return Item{}, gameerr.Validationf("拍賣品「%s」不存在（只能拍賣拍賣限定裝飾、名牌或稱號）。", s)
		}
	}
	return Item{kind, code}, nil
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{}
	ints := []struct {
		key, label string
		low, high  int
		ptr        *int
	}{
		{"start_price", "起標價", 1, 1_000_000_000, &c.StartPrice},
		{"min_increment_pct", "每次最少加價（%）", 1, 100, &c.MinIncrementPct},
		{"extend_minutes", "結標前出價延長（分鐘）", 1, 60, &c.ExtendMinutes},
		{"end_hour", "週日幾點結標", 0, 23, &c.EndHour},
	}
	for _, f := range ints {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr = n
	}
	list, ok := featurecfg.AsList(raw["items"])
	if !ok || len(list) == 0 || len(list) > 50 {
		return nil, gameerr.Validationf("拍賣品要有 1～50 件。")
	}
	for _, v := range list {
		s, _ := v.(string)
		it, err := ParseItem(s)
		if err != nil {
			return nil, err
		}
		c.Items = append(c.Items, it)
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	items := make([]any, len(c.Items))
	for i, it := range c.Items {
		items[i] = it.String()
	}
	return featurecfg.Value{"start_price": c.StartPrice, "min_increment_pct": c.MinIncrementPct,
		"extend_minutes": c.ExtendMinutes, "end_hour": c.EndHour, "items": items}, nil
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Config
)

// Current 目前生效的設定。
func Current() *Config {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		c, err := parse(featurecfg.Get(ConfigKey))
		if err != nil {
			c, _ = parse(defaults())
		}
		cfgCache, cfgVersion = c, v
	}
	return cfgCache
}

// epoch 第 0 週的週一（台灣日期）。
var epoch = time.Date(2026, 1, 5, 0, 0, 0, 0, clock.TaipeiTZ)

// WeekStart 這一週的週一 00:00（台灣時間）。
func WeekStart(now time.Time) time.Time {
	t := now.In(clock.TaipeiTZ)
	y, m, d := t.Date()
	return time.Date(y, m, d-(int(t.Weekday())+6)%7, 0, 0, 0, 0, clock.TaipeiTZ)
}

// EndsAt 這一週原本的結標時間（週日 EndHour 點）。
func EndsAt(weekStart time.Time) time.Time {
	return weekStart.AddDate(0, 0, 6).Add(time.Duration(Current().EndHour) * time.Hour)
}

// ItemOf 這一週拍的商品（依週次輪流）。
func ItemOf(weekStart time.Time) Item {
	items := Current().Items
	weeks := int(weekStart.Sub(epoch).Hours()+12) / (24 * 7) // +12 小時容忍夏令時間（台灣沒有，保險）
	n := len(items)
	return items[((weeks%n)+n)%n]
}

// MinNextBid 下一次出價至少要多少（還沒有人出價時是起標價）。
func MinNextBid(startPrice int, top *int, pct int) int {
	if top == nil {
		return startPrice
	}
	inc := (*top*pct + 99) / 100
	return *top + max(1, inc)
}

// Describe 商品種類的中文。
func Describe(kind string) string {
	return map[string]string{KindDecor: "拍賣限定裝飾", KindBadge: "稀有名牌", KindTitle: "限定稱號"}[kind]
}

func init() {
	featurecfg.Register(ConfigKey, "每週拍賣", defaults, ValidateConfig)
}
