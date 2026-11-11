// Package specialshop 特賣與升級（商店的第三個分頁）：溫室、每週特賣、稱號商店。只放規則與數值，不碰資料庫。
// 對應 Python app/game/special_shop.py。
//
//   - 數值（溫室價格與加成、每週特賣、稱號商店）可在管理網站調整（featurecfg 的 "special_shop"），一律經函式讀取。
//   - 溫室：分級永久升級，每一級讓之後澆水的作物生長時間再少 GreenhousePctPerLevel()%（與限時活動的加速相加）。
//   - 每週特賣：台灣時間每週一 00:00 換一批，用現有的種子；每人每週每項限購，購買量存在 weekly_shop_purchases。
//     商品由週次決定（不存資料庫），所有玩家同一週看到的一樣。
//   - 稱號商店：用金幣買稱號（放進稱號清單，可在「🎖️ 稱號」選用）；每個稱號只能買一次。
package specialshop

import (
	"fmt"
	"math"
	"sort"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/numfmt"
)

const (
	ConfigKey            = "special_shop"
	GreenhouseCode       = "greenhouse"
	GreenhouseLevelLimit = 10 // farms.greenhouse_level 的 CHECK 上限
	TitleShopCode        = "title"
	TitleMaxLen          = 20 // farm_titles.title 的長度
	TitleShopMax         = 20
)

// epoch 第 0 週的週一（台灣日期）。
var epoch = time.Date(2026, 1, 5, 0, 0, 0, 0, clock.TaipeiTZ)

func titleValue(title string, price int) map[string]any {
	return map[string]any{"title": title, "price": price}
}

func defaults() featurecfg.Value {
	return featurecfg.Value{
		"greenhouse_prices":        []int{5_000, 15_000, 40_000, 100_000, 250_000}, // 升到第 1、2…級的價格
		"greenhouse_pct_per_level": 5,
		"normal_deals":             2,   // 每週打折的一般種子數
		"normal_discount_pct":      30,  // 一般種子打 7 折
		"normal_limit":             50,  // 每人每週每項限購
		"rare_price_pct":           120, // 稀有種子（商店平常不賣）= 種子價 × 1.2
		"rare_limit":               3,
		"titles": []any{
			titleValue("黃金地主", 100_000),
			titleValue("農場貴族", 250_000),
			titleValue("傳奇農場主", 500_000),
			titleValue("億萬富農", 1_000_000),
		},
	}
}

// pyStr 等同 Python f"{value}"（None → "None"）。
func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	return fmt.Sprint(v)
}

// ValidateConfig 驗證並正規化特賣與升級設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	out := featurecfg.Value{}
	prices, err := featurecfg.IntList(raw["greenhouse_prices"], "溫室升級價格", 1, 1_000_000_000, 1, GreenhouseLevelLimit)
	if err != nil {
		return nil, err
	}
	out["greenhouse_prices"] = prices
	ints := []struct {
		key, label string
		low, high  int
	}{
		{"greenhouse_pct_per_level", "溫室每級加速", 0, 50},
		{"normal_deals", "每週打折種子數", 0, 10},
		{"normal_discount_pct", "一般種子折扣", 0, 90},
		{"normal_limit", "一般種子每週限購", 1, 9_999},
		{"rare_price_pct", "稀有種子價格", 1, 10_000},
		{"rare_limit", "稀有種子每週限購", 1, 9_999},
	}
	for _, f := range ints {
		n, err := featurecfg.IntValue(raw[f.key], f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		out[f.key] = n
	}
	top := len(prices) * out["greenhouse_pct_per_level"].(int)
	if top > 90 {
		return nil, gameerr.Validationf("溫室最高等級會讓生長時間減少 %d%%，不可超過 90%%。", top)
	}
	titles, ok := featurecfg.AsList(raw["titles"])
	if !ok || len(titles) > TitleShopMax {
		return nil, gameerr.Validationf("稱號商店最多 %d 個稱號。", TitleShopMax)
	}
	outTitles := make([]any, 0, len(titles))
	seen := map[string]bool{}
	dup := false
	for i, tv := range titles {
		t, ok := featurecfg.AsMap(tv)
		if !ok {
			return nil, gameerr.Validationf("第 %d 個商店稱號格式不正確。", i+1)
		}
		title, _, err := featurecfg.TitleValue(t["title"], fmt.Sprintf("第 %d 個商店稱號", i+1), TitleMaxLen, true)
		if err != nil {
			return nil, err
		}
		price, err := featurecfg.IntValue(t["price"], fmt.Sprintf("稱號「%s」的價格", pyStr(t["title"])), 1, 1_000_000_000)
		if err != nil {
			return nil, err
		}
		if seen[title] {
			dup = true
		}
		seen[title] = true
		outTitles = append(outTitles, titleValue(title, price))
	}
	if dup {
		return nil, gameerr.Validationf("稱號商店的稱號不可重複。")
	}
	out["titles"] = outTitles
	return out, nil
}

func init() {
	featurecfg.Register(ConfigKey, "特賣與升級", defaults, ValidateConfig)
}

func Config() featurecfg.Value { return featurecfg.Get(ConfigKey) }

// ConfigInt 讀取整數設定（例如 "normal_limit"、"rare_limit"）。
func ConfigInt(key string) int {
	n, _ := featurecfg.AsInt(Config()[key])
	return n
}

// ---------- 溫室 ----------

func GreenhousePrices() []int {
	switch v := Config()["greenhouse_prices"].(type) {
	case []int:
		return v
	default:
		list, _ := featurecfg.AsList(v)
		out := make([]int, 0, len(list))
		for _, x := range list {
			n, _ := featurecfg.AsInt(x)
			out = append(out, n)
		}
		return out
	}
}

func GreenhouseMaxLevel() int { return len(GreenhousePrices()) }

func GreenhousePctPerLevel() int { return ConfigInt("greenhouse_pct_per_level") }

// GreenhousePct 溫室讓生長時間減少的百分比（等級超過目前上限時以上限計算）。
func GreenhousePct(level int) int {
	return max(0, min(level, GreenhouseMaxLevel())) * GreenhousePctPerLevel()
}

// GreenhousePrice 從目前等級升到下一級的價格；已滿級時 ok = false。
func GreenhousePrice(level int) (price int, ok bool) {
	prices := GreenhousePrices()
	if level >= 0 && level < len(prices) {
		return prices[level], true
	}
	return 0, false
}

// ---------- 稱號商店 ----------

// TitleOffer 稱號商店的商品。
type TitleOffer struct {
	Title string
	Price int
}

// TitleShop (稱號, 價格)，依商店顯示順序。
func TitleShop() []TitleOffer {
	list, _ := featurecfg.AsList(Config()["titles"])
	out := make([]TitleOffer, 0, len(list))
	for _, v := range list {
		m, _ := featurecfg.AsMap(v)
		title, _ := m["title"].(string)
		price, _ := featurecfg.AsInt(m["price"])
		out = append(out, TitleOffer{title, price})
	}
	return out
}

// TitlePrice 稱號價格；商店沒有這個稱號時 ok = false。
func TitlePrice(title string) (price int, ok bool) {
	for _, t := range TitleShop() {
		if t.Title == title {
			price, ok = t.Price, true // 與 Python dict(...) 相同：重複時以最後一個為準
		}
	}
	return price, ok
}

// CropRef 選商品需要的作物資料（服務層由 crop_types 轉成這個）。
type CropRef struct {
	Code        string
	Name        string
	SeedPrice   int
	ShopBuyable bool
}

// Offer 每週特賣商品。
type Offer struct {
	CropCode      string
	CropName      string
	Price         int
	OriginalPrice int
	Limit         int
	Rare          bool
}

// WeekStart 包含 now 的那一週的週一（台灣日期 00:00）。
func WeekStart(now time.Time) time.Time {
	t := now.In(clock.TaipeiTZ)
	y, m, d := t.Date()
	weekday := (int(t.Weekday()) + 6) % 7 // 週一 = 0
	return time.Date(y, m, d-weekday, 0, 0, 0, 0, clock.TaipeiTZ)
}

// WeekEndsAt 下一次換商品的時間（下週一 00:00 台灣時間）。
func WeekEndsAt(start time.Time) time.Time {
	y, m, d := start.Date()
	return time.Date(y, m, d+7, 0, 0, 0, 0, clock.TaipeiTZ)
}

// daysBetween 兩個日期（只看年月日）相差的天數 a - b。
func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(ay, am, ad, 0, 0, 0, 0, time.UTC).Sub(time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC)).Hours() / 24)
}

func sortCrops(crops []CropRef) {
	sort.SliceStable(crops, func(i, j int) bool {
		if crops[i].SeedPrice != crops[j].SeedPrice {
			return crops[i].SeedPrice < crops[j].SeedPrice
		}
		return crops[i].Code < crops[j].Code
	})
}

// WeeklyOffers 這一週的特賣商品：依週次輪流挑一般種子（打折）與一種稀有種子。crops 只放啟用中的作物。
func WeeklyOffers(start time.Time, crops []CropRef) []Offer {
	deals, discount := ConfigInt("normal_deals"), ConfigInt("normal_discount_pct")
	weekNo := numfmt.FloorDiv(daysBetween(start, epoch), 7)
	var normal, rare []CropRef
	for _, c := range crops {
		if c.ShopBuyable {
			normal = append(normal, c)
		} else {
			rare = append(rare, c)
		}
	}
	sortCrops(normal)
	sortCrops(rare)
	var offers []Offer
	if len(normal) > 0 {
		var picks []CropRef
		for i := 0; i < min(deals, len(normal)); i++ {
			c := normal[numfmt.Mod(weekNo*deals+i, len(normal))]
			found := false
			for _, p := range picks {
				if p == c {
					found = true
					break
				}
			}
			if !found {
				picks = append(picks, c)
			}
		}
		for _, c := range picks {
			price := max(1, int(math.Ceil(float64(c.SeedPrice*(100-discount))/100)))
			offers = append(offers, Offer{c.Code, c.Name, price, c.SeedPrice, ConfigInt("normal_limit"), false})
		}
	}
	if len(rare) > 0 {
		c := rare[numfmt.Mod(weekNo, len(rare))]
		price := int(math.Ceil(float64(c.SeedPrice*ConfigInt("rare_price_pct")) / 100))
		offers = append(offers, Offer{c.Code, c.Name, price, c.SeedPrice, ConfigInt("rare_limit"), true})
	}
	return offers
}
