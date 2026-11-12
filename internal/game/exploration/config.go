package exploration

// 可在管理網站調整的探險設定（Go 版新增，2026-11-12；featurecfg key = ConfigKey，存在 feature_settings，不需要 Migration）。
//
//   - 可調整：各區域的名稱、說明、解鎖等級、探險時間、每次抽獎次數、高級探索等級、等級說明、木屑數量、
//     四種掉落池（物品、需要等級、權重、數量）、特殊事件（名稱、說明、需要等級、權重、假幣、額外抽獎），
//     以及普通／高級探索的獎勵類型機率、遺跡額外抽獎份數、素材背包上限、假幣兌換金額。
//   - 固定：區域代碼與順序、區域 emoji、古代遺跡要群組修復、素材種類與機器（Migration 與其他功能依賴這些代碼）。
//   - 沒有自訂設定時直接使用本套件的變數（Areas、RewardTypeBP…），行為與調整前完全相同（測試會直接修改這些變數）。
//   - 有自訂設定時，依 featurecfg.Version() 快取解析結果；遊戲與網站一律經 Current() 讀取。

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const ConfigKey = "exploration"

// 驗證的上限
const (
	cfgMaxLevel       = 100
	cfgMaxDuration    = 7 * 24 * 3600
	cfgMinDuration    = 60
	cfgMaxRolls       = 20
	cfgMaxDrops       = 30
	cfgMaxEvents      = 10
	cfgMaxBonus       = 5
	cfgMaxNotes       = 20
	cfgMaxWeight      = 10_000
	cfgMaxQty         = 999
	cfgMaxSawdust     = 50
	cfgMaxExtraRolls  = 20
	cfgMaxBagCapacity = 100_000
	cfgMaxFakeCoin    = 100_000
	cfgNameLen        = 20
	cfgTextLen        = 200
)

var eventCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// Settings 目前生效的探險設定（唯讀；呼叫端不可修改）。
type Settings struct {
	Areas         map[string]Area
	RewardTypeBP  map[string]map[string]int
	SawdustDrop   map[string][2]int
	RuinsExtra    [2]int
	BagCapacity   int
	FakeCoinValue int
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Settings
)

// defaultSettings 本套件變數組成的設定（不複製；只讀）。
func defaultSettings() *Settings {
	return &Settings{Areas: Areas, RewardTypeBP: RewardTypeBP, SawdustDrop: SawdustDrop, RuinsExtra: RuinsExtraRolls,
		BagCapacity: MaterialBagCapacity, FakeCoinValue: FakeCoinValue}
}

// Current 目前生效的設定。
func Current() *Settings {
	if featurecfg.IsDefault(ConfigKey) {
		return defaultSettings()
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		s, err := parseConfig(featurecfg.Get(ConfigKey))
		if err != nil { // featurecfg.Apply 已驗證過，理論上不會發生；保險起見用預設值
			s = defaultSettings()
		}
		cfgCache, cfgVersion = s, v
	}
	return cfgCache
}

// AreaOf 目前設定的區域（找不到時回傳零值）。
func AreaOf(code string) Area { return Current().Areas[code] }

// BagCapacity 素材背包上限。
func BagCapacity() int { return Current().BagCapacity }

// FakeCoinRate 1 個假幣兌換多少金幣。
func FakeCoinRate() int { return Current().FakeCoinValue }

// RuinsExtra 遺跡探索每次額外多抽的份數範圍 [low, high]。
func RuinsExtra() [2]int { return Current().RuinsExtra }

// ---------- 預設值 ----------

func intPair(p [2]int) []any { return []any{p[0], p[1]} }

func encodeArea(a Area, sawdust [2]int) map[string]any {
	pools := map[string]any{}
	for _, kind := range PoolKinds {
		list := []any{}
		for _, d := range a.Pools[kind] {
			list = append(list, map[string]any{"item": d.Item, "min_level": d.MinLevel, "weight": d.Weight,
				"qty": intPair([2]int{d.QtyMin, d.QtyMax})})
		}
		pools[kind] = list
	}
	events := []any{}
	for _, e := range a.Events {
		bonus := []any{}
		for _, b := range e.Bonus {
			bonus = append(bonus, b)
		}
		events = append(events, map[string]any{"code": e.Code, "name": e.Name, "description": e.Description,
			"min_level": e.MinLevel, "weight": e.Weight, "fake_coins": intPair(e.FakeCoins), "bonus": bonus})
	}
	notes := []any{}
	for _, n := range a.LevelNotes {
		notes = append(notes, map[string]any{"level": n.Level, "text": n.Text})
	}
	return map[string]any{"name": a.Name, "description": a.Description, "unlock_level": a.UnlockLevel,
		"duration_seconds": a.DurationSeconds, "rolls": a.Rolls, "advanced_level": a.AdvancedLevel,
		"sawdust": intPair(sawdust), "level_notes": notes, "pools": pools, "events": events}
}

func encodeConfig(s *Settings) featurecfg.Value {
	areas := map[string]any{}
	for _, code := range AreaOrder {
		areas[code] = encodeArea(s.Areas[code], s.SawdustDrop[code])
	}
	bp := map[string]any{}
	for _, key := range []string{"normal", "advanced"} {
		m := map[string]any{}
		for _, t := range RewardTypes {
			m[t] = s.RewardTypeBP[key][t]
		}
		bp[key] = m
	}
	return featurecfg.Value{"material_bag_capacity": s.BagCapacity, "fake_coin_value": s.FakeCoinValue,
		"ruins_extra_rolls": intPair(s.RuinsExtra), "reward_types": bp, "areas": areas}
}

func configDefaults() featurecfg.Value { return encodeConfig(defaultSettings()) }

// ValidateConfig 驗證並正規化探險設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	s, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return encodeConfig(s), nil
}

func init() {
	featurecfg.Register(ConfigKey, "探險", configDefaults, ValidateConfig)
}

// ---------- 解析與驗證 ----------

func pairValue(v any, label string, low, high int) ([2]int, error) {
	list, err := featurecfg.IntList(v, label, low, high, 2, 2)
	if err != nil {
		return [2]int{}, err
	}
	if list[0] > list[1] {
		return [2]int{}, gameerr.Validationf("%s的最小值不可大於最大值。", label)
	}
	return [2]int{list[0], list[1]}, nil
}

func textValue(v any, label string, maxLen int, required bool) (string, error) {
	s, ok := v.(string)
	if v != nil && !ok {
		return "", gameerr.Validationf("%s格式不正確。", label)
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", gameerr.Validationf("%s不可空白。", label)
	}
	if utf8.RuneCountInString(s) > maxLen {
		return "", gameerr.Validationf("%s最多 %d 字。", label, maxLen)
	}
	return s, nil
}

// DropItems 掉落池可以放的物品：素材（含木屑）、稀有種子與假幣。
func DropItems() []string {
	out := append([]string(nil), MaterialOrder...)
	return append(out, RareSeedCodes()...)
}

func validDropItem(code string) bool {
	for _, c := range DropItems() {
		if c == code {
			return true
		}
	}
	return false
}

func parseArea(code string, raw any) (Area, [2]int, error) {
	base := Areas[code]
	m, ok := featurecfg.AsMap(raw)
	if !ok {
		return Area{}, [2]int{}, gameerr.Validationf("「%s」的設定格式不正確。", base.Name)
	}
	label := base.Name
	a := Area{Code: code, Emoji: base.Emoji, NeedsRuins: base.NeedsRuins, Pools: map[string][]Drop{}}
	var err error
	if a.Name, err = textValue(m["name"], label+"的名稱", cfgNameLen, true); err != nil {
		return Area{}, [2]int{}, err
	}
	label = a.Name
	if a.Description, err = textValue(m["description"], label+"的說明", cfgTextLen, false); err != nil {
		return Area{}, [2]int{}, err
	}
	if a.UnlockLevel, err = featurecfg.IntValue(m["unlock_level"], label+"的解鎖等級", 1, cfgMaxLevel); err != nil {
		return Area{}, [2]int{}, err
	}
	durLabel := label + "的探險時間（秒）"
	if a.NeedsRuins {
		durLabel = label + "的冷卻時間（秒）"
	}
	if a.DurationSeconds, err = featurecfg.IntValue(m["duration_seconds"], durLabel, cfgMinDuration, cfgMaxDuration); err != nil {
		return Area{}, [2]int{}, err
	}
	if a.Rolls, err = featurecfg.IntValue(m["rolls"], label+"的每次抽獎次數", 1, cfgMaxRolls); err != nil {
		return Area{}, [2]int{}, err
	}
	if a.AdvancedLevel, err = featurecfg.IntValue(m["advanced_level"], label+"的高級探索等級", 1, cfgMaxLevel+1); err != nil {
		return Area{}, [2]int{}, err
	}
	sawdust, err := pairValue(m["sawdust"], label+"的木屑數量", 0, cfgMaxSawdust)
	if err != nil {
		return Area{}, [2]int{}, err
	}
	if m["level_notes"] != nil {
		notes, ok := featurecfg.AsList(m["level_notes"])
		if !ok || len(notes) > cfgMaxNotes {
			return Area{}, [2]int{}, gameerr.Validationf("%s的等級說明最多 %d 條。", label, cfgMaxNotes)
		}
		for i, n := range notes {
			nm, ok := featurecfg.AsMap(n)
			if !ok {
				return Area{}, [2]int{}, gameerr.Validationf("%s第 %d 條等級說明格式不正確。", label, i+1)
			}
			lv, err := featurecfg.IntValue(nm["level"], fmt.Sprintf("%s第 %d 條等級說明的等級", label, i+1), 1, cfgMaxLevel)
			if err != nil {
				return Area{}, [2]int{}, err
			}
			text, err := textValue(nm["text"], fmt.Sprintf("%s第 %d 條等級說明", label, i+1), cfgTextLen, true)
			if err != nil {
				return Area{}, [2]int{}, err
			}
			a.LevelNotes = append(a.LevelNotes, LevelNote{lv, text})
		}
	}
	pools, ok := featurecfg.AsMap(m["pools"])
	if !ok {
		return Area{}, [2]int{}, gameerr.Validationf("%s的掉落池格式不正確。", label)
	}
	total := 0
	for _, kind := range PoolKinds {
		kl := fmt.Sprintf("%s的「%s」", label, RewardTypeLabels[kind])
		var list []any
		if pools[kind] != nil {
			if list, ok = featurecfg.AsList(pools[kind]); !ok || len(list) > cfgMaxDrops {
				return Area{}, [2]int{}, gameerr.Validationf("%s最多 %d 項。", kl, cfgMaxDrops)
			}
		}
		drops := []Drop{}
		for i, d := range list {
			dm, ok := featurecfg.AsMap(d)
			if !ok {
				return Area{}, [2]int{}, gameerr.Validationf("%s第 %d 項格式不正確。", kl, i+1)
			}
			il := fmt.Sprintf("%s第 %d 項", kl, i+1)
			item, _ := dm["item"].(string)
			if !validDropItem(item) {
				return Area{}, [2]int{}, gameerr.Validationf("%s：沒有「%s」這種素材或稀有種子。", il, item)
			}
			lv, err := featurecfg.IntValue(dm["min_level"], il+"的需要等級", 1, cfgMaxLevel)
			if err != nil {
				return Area{}, [2]int{}, err
			}
			w, err := featurecfg.IntValue(dm["weight"], il+"的權重", 0, cfgMaxWeight)
			if err != nil {
				return Area{}, [2]int{}, err
			}
			q, err := pairValue(dm["qty"], il+"的數量", 1, cfgMaxQty)
			if err != nil {
				return Area{}, [2]int{}, err
			}
			drops = append(drops, Drop{item, lv, w, q[0], q[1]})
			if w > 0 {
				total++
			}
		}
		a.Pools[kind] = drops
	}
	if total == 0 {
		return Area{}, [2]int{}, gameerr.Validationf("%s至少要有一項權重大於 0 的掉落物。", label)
	}
	if m["events"] != nil {
		events, ok := featurecfg.AsList(m["events"])
		if !ok || len(events) > cfgMaxEvents {
			return Area{}, [2]int{}, gameerr.Validationf("%s的特殊事件最多 %d 個。", label, cfgMaxEvents)
		}
		for i, e := range events {
			em, ok := featurecfg.AsMap(e)
			el := fmt.Sprintf("%s第 %d 個特殊事件", label, i+1)
			if !ok {
				return Area{}, [2]int{}, gameerr.Validationf("%s格式不正確。", el)
			}
			ev := Event{}
			ev.Code, _ = em["code"].(string)
			if ev.Code == "" {
				ev.Code = fmt.Sprintf("event_%d", i+1)
			}
			if !eventCodeRe.MatchString(ev.Code) {
				return Area{}, [2]int{}, gameerr.Validationf("%s的代碼格式不正確。", el)
			}
			if ev.Name, err = textValue(em["name"], el+"的名稱", cfgNameLen, true); err != nil {
				return Area{}, [2]int{}, err
			}
			if ev.Description, err = textValue(em["description"], el+"的說明", cfgTextLen, false); err != nil {
				return Area{}, [2]int{}, err
			}
			if ev.MinLevel, err = featurecfg.IntValue(em["min_level"], el+"的需要等級", 1, cfgMaxLevel); err != nil {
				return Area{}, [2]int{}, err
			}
			if ev.Weight, err = featurecfg.IntValue(em["weight"], el+"的權重", 0, cfgMaxWeight); err != nil {
				return Area{}, [2]int{}, err
			}
			if ev.FakeCoins, err = pairValue(em["fake_coins"], el+"的假幣", 0, cfgMaxQty); err != nil {
				return Area{}, [2]int{}, err
			}
			var bonus []any
			if em["bonus"] != nil {
				if bonus, ok = featurecfg.AsList(em["bonus"]); !ok || len(bonus) > cfgMaxBonus {
					return Area{}, [2]int{}, gameerr.Validationf("%s的額外抽獎最多 %d 次。", el, cfgMaxBonus)
				}
			}
			ev.Bonus = []string{}
			for _, b := range bonus {
				kind, _ := b.(string)
				if _, ok := Fallback[kind]; !ok || kind == "event" {
					return Area{}, [2]int{}, gameerr.Validationf("%s的額外抽獎只能是普通素材、稀有素材、稀有種子或假幣。", el)
				}
				ev.Bonus = append(ev.Bonus, kind)
			}
			a.Events = append(a.Events, ev)
		}
	}
	return a, sawdust, nil
}

func parseConfig(raw featurecfg.Value) (*Settings, error) {
	s := &Settings{Areas: map[string]Area{}, RewardTypeBP: map[string]map[string]int{}, SawdustDrop: map[string][2]int{}}
	var err error
	if s.BagCapacity, err = featurecfg.IntValue(raw["material_bag_capacity"], "素材背包上限", 1, cfgMaxBagCapacity); err != nil {
		return nil, err
	}
	if s.FakeCoinValue, err = featurecfg.IntValue(raw["fake_coin_value"], "假幣兌換金額", 1, cfgMaxFakeCoin); err != nil {
		return nil, err
	}
	if s.RuinsExtra, err = pairValue(raw["ruins_extra_rolls"], "遺跡額外抽獎份數", 0, cfgMaxExtraRolls); err != nil {
		return nil, err
	}
	bp, ok := featurecfg.AsMap(raw["reward_types"])
	if !ok {
		return nil, gameerr.Validationf("獎勵類型機率格式不正確。")
	}
	for _, key := range []string{"normal", "advanced"} {
		label := map[string]string{"normal": "普通探索", "advanced": "高級探索"}[key]
		m, ok := featurecfg.AsMap(bp[key])
		if !ok {
			return nil, gameerr.Validationf("%s的獎勵類型機率格式不正確。", label)
		}
		out := map[string]int{}
		sum := 0
		for _, t := range RewardTypes {
			n, err := featurecfg.IntValue(m[t], fmt.Sprintf("%s的「%s」機率（萬分比）", label, RewardTypeLabels[t]), 0, TotalBP)
			if err != nil {
				return nil, err
			}
			out[t] = n
			sum += n
		}
		if sum != TotalBP {
			return nil, gameerr.Validationf("%s的獎勵類型機率合計必須是 100%%（目前 %.2f%%）。", label, float64(sum)/100)
		}
		s.RewardTypeBP[key] = out
	}
	areas, ok := featurecfg.AsMap(raw["areas"])
	if !ok {
		return nil, gameerr.Validationf("區域設定格式不正確。")
	}
	for _, code := range AreaOrder {
		if areas[code] == nil {
			return nil, gameerr.Validationf("缺少「%s」的設定。", Areas[code].Name)
		}
		a, sawdust, err := parseArea(code, areas[code])
		if err != nil {
			return nil, err
		}
		s.Areas[code] = a
		s.SawdustDrop[code] = sawdust
	}
	return s, nil
}
