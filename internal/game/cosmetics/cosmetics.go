// Package cosmetics 純外觀的收藏品（Go 版新增，2026-11-19，金幣回收第二波）：名牌、農場主題、寵物帽子與顏色。
// 擁有的外觀存在 farm_cosmetics（Migration 0046，kind = badge／theme／hat／color），裝備中的名牌存在 farms.badge。
// 價格可在管理網站「外觀收藏」調整（featurecfg key = ConfigKey）；價格 0 表示不在商店販售（只能從聲望或拍賣取得）。
package cosmetics

import (
	"fmt"
	"slices"
	"sync"

	"farmgm/internal/game/featurecfg"
)

const ConfigKey = "cosmetics"

// Kind farm_cosmetics.kind。
const (
	KindBadge = "badge"
	KindTheme = "theme"
	KindHat   = "hat"
	KindColor = "color"
)

// 取得方式（farm_cosmetics.source）。
const (
	SourceShop       = "shop"
	SourceReputation = "reputation"
	SourceAuction    = "auction"
	SourceAdmin      = "admin"
)

// SourceNames 取得方式的顯示名稱。
var SourceNames = map[string]string{SourceShop: "商店", SourceReputation: "農會聲望", SourceAuction: "拍賣", SourceAdmin: "管理員"}

// Badge 名牌：排行榜、日報、世界 Boss 排行裡名字前面的 emoji。
type Badge struct {
	Code, Emoji, Name string
	DefaultPrice      int    // 0 = 不在商店販售
	How               string // 不販售時的取得方式說明
}

// Badges 所有名牌（商店顯示順序）。聲望獎勵的名牌見 game/reputation。
var Badges = []Badge{
	{"clover", "🍀", "幸運草", 30_000, ""},
	{"sprout", "🌱", "新芽", 30_000, ""},
	{"fire", "🔥", "火焰", 60_000, ""},
	{"star", "⭐", "星星", 100_000, ""},
	{"diamond", "💎", "鑽石", 150_000, ""},
	{"crown", "👑", "皇冠", 300_000, ""},
	{"wheat", "🌾", "金穗", 0, "農會聲望 Lv.10"},
	{"temple", "🏛️", "農會徽章", 0, "農會聲望 Lv.25"},
	{"sparkle", "🌟", "閃耀之星", 0, "農會聲望 Lv.40"},
	{"dragon", "🐉", "神龍", 0, "每週拍賣"},
	{"unicorn", "🦄", "獨角獸", 0, "每週拍賣"},
}

// FindBadge 依代碼找名牌。
func FindBadge(code string) (Badge, bool) {
	i := slices.IndexFunc(Badges, func(b Badge) bool { return b.Code == code })
	if i < 0 {
		return Badge{}, false
	}
	return Badges[i], true
}

// BadgeEmoji 名牌的 emoji（沒有這個名牌時是 ""）。
func BadgeEmoji(code string) string {
	b, _ := FindBadge(code)
	return b.Emoji
}

// Item 主題、帽子、顏色共用的商品資料。
type Item struct {
	Code, Emoji, Name string
	DefaultPrice      int
}

// Theme 農場外觀主題（Go 版新增，2026-11-19）：換掉農田外圍的地面磚與點綴，夜景另外整張套濾鏡。
type Theme struct {
	Item
	Tile   string    // 地面磚（無縫）
	Props  [2]string // 欄杆外圍的點綴
	Night  bool      // 整張圖變暗、偏藍
	Filter [3]int    // 夜景濾鏡：RGB 各乘以幾 %（Night 時使用）
}

// Themes 所有主題（顯示順序）。
var Themes = []Theme{
	{Item{"snow", "❄️", "雪地", 100_000}, "themes/theme_snow_tile.png",
		[2]string{"themes/theme_snow_pile.png", "themes/theme_snow_bush.png"}, false, [3]int{}},
	{Item{"sakura", "🌸", "櫻花", 80_000}, "themes/theme_sakura_tile.png",
		[2]string{"themes/theme_sakura_shrub.png", "themes/theme_sakura_petals.png"}, false, [3]int{}},
	{Item{"night", "🌙", "夜景", 150_000}, "themes/theme_night_tile.png",
		[2]string{"themes/theme_night_lantern.png", "themes/theme_night_fireflies.png"}, true, [3]int{62, 70, 95}},
}

// Hats 寵物帽子（圖片 pets/hats/<code>.png，32×32；每隻寵物戴的位置在 manifest.json 的 pets.sprites[*].hat）。
var Hats = []Item{
	{"straw_hat", "👒", "草帽", 20_000},
	{"bow", "🎀", "蝴蝶結", 20_000},
	{"flower_crown", "💐", "花圈", 30_000},
	{"santa_hat", "🎅", "聖誕帽", 40_000},
	{"wizard_hat", "🧙", "巫師帽", 60_000},
	{"crown", "👑", "皇冠", 80_000},
}

// Color 寵物換色：色相旋轉 Hue 度、飽和度乘以 SatPct%（只改有顏色的像素，描邊與黑白不變）。
type Color struct {
	Item
	Hue    int
	SatPct int
}

// Colors 寵物顏色。
var Colors = []Color{
	{Item{"pink", "🩷", "粉紅", 30_000}, 320, 120},
	{Item{"blue", "🩵", "天藍", 30_000}, 180, 110},
	{Item{"mint", "💚", "薄荷綠", 30_000}, 110, 100},
	{Item{"gold", "💛", "金色", 30_000}, 20, 160},
}

// HatImage 帽子圖片路徑。
func HatImage(code string) string { return "pets/hats/" + code + ".png" }

// FindTheme 依代碼找主題。
func FindTheme(code string) (Theme, bool) {
	i := slices.IndexFunc(Themes, func(t Theme) bool { return t.Code == code })
	if i < 0 {
		return Theme{}, false
	}
	return Themes[i], true
}

// FindHat 依代碼找帽子。
func FindHat(code string) (Item, bool) {
	i := slices.IndexFunc(Hats, func(h Item) bool { return h.Code == code })
	if i < 0 {
		return Item{}, false
	}
	return Hats[i], true
}

// FindColor 依代碼找顏色。
func FindColor(code string) (Color, bool) {
	i := slices.IndexFunc(Colors, func(c Color) bool { return c.Code == code })
	if i < 0 {
		return Color{}, false
	}
	return Colors[i], true
}

// priceKind 每一種外觀的價格設定鍵與預設價格。
type priceKind struct {
	key, label string
	items      func() []Item
}

var priceKinds = map[string]priceKind{
	KindBadge: {"badge_prices", "名牌", func() []Item {
		out := make([]Item, len(Badges))
		for i, b := range Badges {
			out[i] = Item{b.Code, b.Emoji, b.Name, b.DefaultPrice}
		}
		return out
	}},
	KindTheme: {"theme_prices", "主題", func() []Item {
		out := make([]Item, len(Themes))
		for i, t := range Themes {
			out[i] = t.Item
		}
		return out
	}},
	KindHat: {"hat_prices", "帽子", func() []Item { return Hats }},
	KindColor: {"color_prices", "顏色", func() []Item {
		out := make([]Item, len(Colors))
		for i, c := range Colors {
			out[i] = c.Item
		}
		return out
	}},
}

// KindOrder 設定與管理網站的顯示順序。
var KindOrder = []string{KindBadge, KindTheme, KindHat, KindColor}

// Items 某一種外觀的所有商品（含預設價格）。
func Items(kind string) []Item { return priceKinds[kind].items() }

func defaults() featurecfg.Value {
	out := featurecfg.Value{}
	for _, kind := range KindOrder {
		pk := priceKinds[kind]
		prices := map[string]any{}
		for _, it := range pk.items() {
			prices[it.Code] = it.DefaultPrice
		}
		out[pk.key] = prices
	}
	return out
}

// Config 目前生效的設定（唯讀）：種類 → 代碼 → 價格（0 = 不在商店販售）。
type Config struct {
	Prices map[string]map[string]int
}

func parse(raw featurecfg.Value) (*Config, error) {
	c := &Config{Prices: map[string]map[string]int{}}
	for _, kind := range KindOrder {
		pk := priceKinds[kind]
		m, _ := featurecfg.AsMap(raw[pk.key])
		c.Prices[kind] = map[string]int{}
		for _, it := range pk.items() {
			v, ok := m[it.Code]
			if !ok {
				v = it.DefaultPrice // 之後新增的商品：沿用預設價格
			}
			n, err := featurecfg.IntValue(v, fmt.Sprintf("%s「%s」的價格", pk.label, it.Name), 0, 1_000_000_000)
			if err != nil {
				return nil, err
			}
			c.Prices[kind][it.Code] = n
		}
	}
	return c, nil
}

// ValidateConfig 驗證並正規化設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parse(raw)
	if err != nil {
		return nil, err
	}
	out := featurecfg.Value{}
	for _, kind := range KindOrder {
		prices := map[string]any{}
		for k, v := range c.Prices[kind] {
			prices[k] = v
		}
		out[priceKinds[kind].key] = prices
	}
	return out, nil
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

// BadgePrice 名牌的商店價格；0 = 不販售。
func BadgePrice(code string) int { return Price(KindBadge, code) }

// Price 某一種外觀的商店價格；0 = 不販售。
func Price(kind, code string) int { return Current().Prices[kind][code] }

func init() {
	featurecfg.Register(ConfigKey, "外觀收藏", defaults, ValidateConfig)
}
