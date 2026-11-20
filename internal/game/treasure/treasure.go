// Package treasure 藏寶圖碎片（Go 版新增，2026-11-20，設計見「新玩法六項_流程圖.md」#9）。
//
//   - 碎片有北、東、南、西 4 種，探險、手動釣魚、偷菜成功、NPC 訂單都有機率掉落；彩虹日（雨後放晴）掉率加倍，另外可以免費領 1 片。
//   - 4 種各 1 片就能出發尋寶，去本週的寶藏地點（每週輪換，全服相同），HuntSeconds 後回來開箱。
//   - 3 片相同的碎片可以付 ExchangeFee 金換 1 片指定方位的碎片。
//   - 碎片、尋寶進度都存在 farm_tools（ToolCode）的 settings JSON，不是背包物品：不需要新的物品代碼與 Migration，
//     資料庫仍可切回 Python 版。
package treasure

import (
	"crypto/sha256"
	"encoding/binary"
	"slices"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/exploration"
)

const (
	ToolCode     = "treasure_map"
	HuntSeconds  = 3600 // 尋寶時間
	ExchangeCost = 3    // 幾片相同的碎片換 1 片
	ExchangeFee  = 300  // 換碎片的手續費（金幣）
	Rolls        = 2    // 開箱時抽幾次物品（另有金幣）
	RainbowMult  = 2    // 彩虹日掉率倍數
)

// 碎片方位。
var Pieces = []string{"n", "e", "s", "w"}

var PieceNames = map[string]string{"n": "北", "e": "東", "s": "南", "w": "西"}

// 掉落來源。
const (
	SourceExplore = "explore"
	SourceFish    = "fish"
	SourceSteal   = "steal"
	SourceOrder   = "order"
)

// DropBP 各來源每次掉落 1 片的機率（萬分比）。自動釣魚機不掉，避免掛機刷碎片。
var DropBP = map[string]int{SourceExplore: 800, SourceFish: 100, SourceSteal: 200, SourceOrder: 1500}

// Drop 開箱時可能抽到的物品（都是素材背包的物品）。
type Drop struct {
	Item     string
	Min, Max int
	Weight   int
}

// Site 寶藏地點。Scene 是 farmimage 的場景名稱。
type Site struct {
	Code, Emoji, Name, Description, Scene string
	Gold                                  [2]int
	Drops                                 []Drop
}

var SiteOrder = []string{"shipwreck", "oak", "mine", "altar"}

var Sites = map[string]Site{
	"shipwreck": {"shipwreck", "🏝️", "沉船灣", "海盜船擱淺的隱密海灣，沙灘下埋著船上的寶箱。", "treasure_shipwreck",
		[2]int{800, 2000}, []Drop{{exploration.FakeCoin, 3, 6, 4}, {"gear", 1, 3, 3}, {"iron_ore", 2, 4, 3},
			{exploration.AmberSeed, 1, 1, 2}, {"machine_core", 1, 1, 1}}},
	"oak": {"oak", "🌳", "老橡樹下", "千年橡樹的樹根下，有人埋了一個木箱。", "treasure_oak",
		[2]int{500, 1500}, []Drop{{"wood", 5, 10, 3}, {"resin", 3, 6, 3}, {"herb", 3, 6, 3},
			{exploration.Sawdust, 3, 6, 2}, {exploration.MoonSeed, 1, 1, 2}, {"ancient_core", 1, 1, 1}}},
	"mine": {"mine", "⛏️", "廢棄礦道", "坍塌的舊礦道深處，礦工留下了私藏。", "treasure_mine",
		[2]int{600, 1600}, []Drop{{"crystal", 1, 3, 4}, {"iron_ore", 3, 6, 3}, {"copper_ore", 3, 6, 3},
			{"machine_core", 1, 1, 1}}},
	"altar": {"altar", "🗿", "遺跡祭壇", "藤蔓纏繞的古代祭壇上，寶箱發著微光。", "treasure_altar",
		[2]int{1000, 2500}, []Drop{{"ancient_core", 1, 1, 3}, {"crystal", 2, 3, 3}, {exploration.FakeCoin, 4, 8, 3},
			{exploration.MoonSeed, 1, 1, 1}, {exploration.AmberSeed, 1, 1, 1}}},
}

// WeekStart now 所在的台灣週一 00:00。
func WeekStart(now time.Time) time.Time {
	local := now.In(clock.TaipeiTZ)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// WeekSite 本週的寶藏地點：SHA-256("treasure-<週一日期>") 取餘數，全服相同。
func WeekSite(now time.Time) Site {
	sum := sha256.Sum256([]byte("treasure-" + WeekStart(now).Format("2006-01-02")))
	return Sites[SiteOrder[binary.BigEndian.Uint64(sum[:8])%uint64(len(SiteOrder))]]
}

// Dropped 這次是否掉落碎片（u ∈ [0, 1)）。
func Dropped(source string, rainbow bool, u float64) bool {
	bp := DropBP[source]
	if rainbow {
		bp *= RainbowMult
	}
	return int(u*10_000) < bp
}

// RandomPiece 隨機方位（u ∈ [0, 1)）。
func RandomPiece(u float64) string { return Pieces[min(len(Pieces)-1, int(u*float64(len(Pieces))))] }

// Reward 開箱獎勵。
type Reward struct {
	Gold  int
	Items map[string]int
	Order []string
}

// Roll 依地點抽獎勵：金幣一份、物品 Rolls 次。
func Roll(site Site, rng func() float64) Reward {
	r := Reward{Gold: between(site.Gold, rng()), Items: map[string]int{}}
	total := 0
	for _, d := range site.Drops {
		total += d.Weight
	}
	for range Rolls {
		point := int(rng() * float64(total))
		for _, d := range site.Drops {
			if point < d.Weight {
				if _, ok := r.Items[d.Item]; !ok {
					r.Order = append(r.Order, d.Item)
				}
				r.Items[d.Item] += between([2]int{d.Min, d.Max}, rng())
				break
			}
			point -= d.Weight
		}
	}
	return r
}

func between(span [2]int, u float64) int {
	return span[0] + min(span[1]-span[0], int(u*float64(span[1]-span[0]+1)))
}

// State 一座農場的藏寶圖狀態（farm_tools.settings）。
type State struct {
	Pieces     map[string]int
	HuntSite   string     // 尋寶中的地點（"" = 沒有在尋寶）
	HuntEnd    *time.Time // 尋寶回來的時間
	Found      []string   // 去過的地點（成就用）
	RainbowDay string     // 最近一次領彩虹碎片的台灣日期
	BoardWeek  string     // 最近一次因「委託板三張全部完成」拿到碎片的那一週（週一日期）
}

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

// Parse 讀出狀態；格式錯誤的欄位當成沒有。
func Parse(settings map[string]any) State {
	s := State{Pieces: map[string]int{}}
	if raw, ok := settings["pieces"].(map[string]any); ok {
		for _, p := range Pieces {
			s.Pieces[p] = max(0, num(raw[p]))
		}
	}
	if site, _ := settings["hunt_site"].(string); site != "" {
		if _, ok := Sites[site]; ok {
			if end, err := time.Parse(time.RFC3339Nano, stringOf(settings["hunt_end"])); err == nil {
				s.HuntSite, s.HuntEnd = site, &end
			}
		}
	}
	if raw, ok := settings["found"].([]any); ok {
		for _, v := range raw {
			if c, _ := v.(string); c != "" && !slices.Contains(s.Found, c) {
				s.Found = append(s.Found, c)
			}
		}
	}
	s.RainbowDay = stringOf(settings["rainbow_day"])
	s.BoardWeek = stringOf(settings["board_week"])
	return s
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// Encode 寫回 settings 的格式。
func (s State) Encode() map[string]any {
	pieces := map[string]any{}
	for _, p := range Pieces {
		pieces[p] = s.Pieces[p]
	}
	out := map[string]any{"pieces": pieces, "found": append([]string{}, s.Found...), "rainbow_day": s.RainbowDay,
		"board_week": s.BoardWeek}
	if s.HuntSite != "" && s.HuntEnd != nil {
		out["hunt_site"] = s.HuntSite
		out["hunt_end"] = s.HuntEnd.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// Complete 4 種碎片都至少 1 片。
func (s State) Complete() bool {
	for _, p := range Pieces {
		if s.Pieces[p] < 1 {
			return false
		}
	}
	return true
}

// Total 碎片總數。
func (s State) Total() int {
	n := 0
	for _, p := range Pieces {
		n += s.Pieces[p]
	}
	return n
}

// Hunting 正在尋寶（還沒開箱）。
func (s State) Hunting() bool { return s.HuntSite != "" && s.HuntEnd != nil }

// HuntDone 尋寶隊已經回來、可以開箱。
func (s State) HuntDone(now time.Time) bool { return s.Hunting() && !now.Before(*s.HuntEnd) }
