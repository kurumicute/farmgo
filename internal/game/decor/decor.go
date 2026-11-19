// Package decor 農場裝飾（Go 版新增，2026-11-15）：只改外觀、沒有加成。買一次永久擁有，擺在農田圖左右兩側的庭院
// （最多 Slots 個位置）。目錄固定在程式裡（使用者指定裝飾不需要在管理網站調整）；圖片在 bot/farmimage/farm/decor/<代碼>.png。
package decor

import "slices"

// Slots 庭院擺設的位置數（farm_decorations.slot 的 CHECK 是 1–6）：1–3 在農田左邊（上、中、下），4–6 在右邊。
const Slots = 6

var slotNames = [Slots]string{"左上", "左中", "左下", "右上", "右中", "右下"}

// SlotName 位置名稱（1–6 → 左上…右下）。
func SlotName(slot int) string {
	if slot < 1 || slot > Slots {
		return ""
	}
	return slotNames[slot-1]
}

// Tier 等級。
type Tier struct {
	Code, Name, Emoji string
}

var (
	Common   = Tier{"common", "普通", "⚪"}
	Fine     = Tier{"fine", "精緻", "🔵"}
	Rare     = Tier{"rare", "稀有", "🟡"}
	Festival = Tier{"festival", "節慶", "🎉"}
	// Seasonal 季節限定（2026-11-19）：只在 Season 那一季（game/seasons 的 14 天輪替）買得到，換季下架；買過的永久保留。
	Seasonal = Tier{"seasonal", "季節限定", "🗓️"}
	// Auction 拍賣限定（2026-11-19）：商店不賣，只能在每週系統拍賣得標。
	Auction = Tier{"auction", "拍賣限定", "🔨"}
)

// Tiers 商店顯示的等級順序。
var Tiers = []Tier{Seasonal, Common, Fine, Rare, Festival, Auction}

// Item 一種裝飾。Season 不是 "" 時是季節限定（spring／summer／autumn／winter）。
type Item struct {
	Code, Name, Emoji string
	Tier              Tier
	Price             int
	Season            string
}

// SeasonalPrice 季節限定裝飾的價格（介於精緻與稀有之間，跟節慶一樣）。
const SeasonalPrice = 50_000

// OnSale 現在的季節買不買得到（一般裝飾一律可以買）。
func (it Item) OnSale(season string) bool {
	return it.Tier != Auction && (it.Season == "" || it.Season == season)
}

// Catalog 所有裝飾（商店的顯示順序）。
var Catalog = []Item{
	{"barrel", "木桶", "🛢️", Common, 5_000, ""},
	{"crate", "木箱", "📦", Common, 5_000, ""},
	{"bush", "灌木", "🌳", Common, 5_000, ""},
	{"berry_bush", "莓果叢", "🫐", Common, 5_000, ""},
	{"flowerbed", "花圃", "🌼", Common, 5_000, ""},
	{"planter", "花盆", "🪴", Common, 5_000, ""},
	{"tree", "大樹", "🌳", Common, 5_000, ""},
	{"pine", "松樹", "🌲", Common, 5_000, ""},
	{"sakura", "櫻花樹", "🌸", Common, 5_000, ""},
	{"lamp", "路燈", "🏮", Common, 5_000, ""},
	{"signpost", "路牌", "🪧", Common, 5_000, ""},
	{"scarecrow", "稻草人", "🧑‍🌾", Fine, 20_000, ""},
	{"windmill", "小風車", "🌬️", Fine, 20_000, ""},
	{"swing", "鞦韆", "🪢", Fine, 20_000, ""},
	{"doghouse", "狗屋", "🐶", Fine, 20_000, ""},
	{"birdhouse", "鳥屋", "🐦", Fine, 20_000, ""},
	{"pumpkins", "南瓜堆", "🎃", Fine, 20_000, ""},
	{"fountain", "噴水池", "⛲", Rare, 100_000, ""},
	{"gold_cat_statue", "金色貓咪雕像", "🐱", Rare, 100_000, ""},
	{"gazebo", "小涼亭", "⛩️", Rare, 100_000, ""},
	{"wishing_well", "許願井", "🪣", Rare, 100_000, ""},
	{"jack_o_lantern", "南瓜燈", "🎃", Festival, 50_000, ""},
	{"snowman", "雪人", "⛄", Festival, 50_000, ""},
	{"xmas_tree", "聖誕樹", "🎄", Festival, 50_000, ""},
	// 季節限定：每季 2 件
	{"sakura_arch", "櫻花拱門", "🌸", Seasonal, SeasonalPrice, "spring"},
	{"tulip_cart", "鬱金香推車", "🌷", Seasonal, SeasonalPrice, "spring"},
	{"watermelon_stand", "西瓜攤", "🍉", Seasonal, SeasonalPrice, "summer"},
	{"beach_umbrella", "海灘傘", "⛱️", Seasonal, SeasonalPrice, "summer"},
	{"maple_tree", "楓樹", "🍁", Seasonal, SeasonalPrice, "autumn"},
	{"harvest_cart", "豐收推車", "🌽", Seasonal, SeasonalPrice, "autumn"},
	{"ice_sculpture", "冰雕天鵝", "🦢", Seasonal, SeasonalPrice, "winter"},
	{"snow_lantern", "雪燈籠", "🏮", Seasonal, SeasonalPrice, "winter"},
	// 拍賣限定：價格 0，商店不賣
	{"golden_windmill", "黃金風車", "🌬️", Auction, 0, ""},
	{"crystal_fountain", "水晶噴泉", "⛲", Auction, 0, ""},
	{"rainbow_arch", "彩虹拱門", "🌈", Auction, 0, ""},
	{"dragon_statue", "玉龍雕像", "🐉", Auction, 0, ""},
}

// Find 依代碼找裝飾。
func Find(code string) (Item, bool) {
	i := slices.IndexFunc(Catalog, func(x Item) bool { return x.Code == code })
	if i < 0 {
		return Item{}, false
	}
	return Catalog[i], true
}

// ImagePath 裝飾圖片在 farmimage 素材裡的路徑。
func ImagePath(code string) string { return "decor/" + code + ".png" }
