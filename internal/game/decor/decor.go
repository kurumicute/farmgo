// Package decor 農場裝飾（Go 版新增，2026-11-15）：只改外觀、沒有加成。買一次永久擁有，擺在農田圖下方的庭院擺飾區
// （最多 Slots 個位置）。目錄固定在程式裡（使用者指定裝飾不需要在管理網站調整）；圖片在 bot/farmimage/farm/decor/<代碼>.png。
package decor

import "slices"

// Slots 庭院擺飾區的位置數（farm_decorations.slot 的 CHECK 是 1–6）。
const Slots = 6

// Tier 等級。
type Tier struct {
	Code, Name, Emoji string
}

var (
	Common   = Tier{"common", "普通", "⚪"}
	Fine     = Tier{"fine", "精緻", "🔵"}
	Rare     = Tier{"rare", "稀有", "🟡"}
	Festival = Tier{"festival", "節慶", "🎉"}
)

// Item 一種裝飾。
type Item struct {
	Code, Name, Emoji string
	Tier              Tier
	Price             int
}

// Catalog 所有裝飾（商店的顯示順序）。
var Catalog = []Item{
	{"barrel", "木桶", "🛢️", Common, 5_000},
	{"crate", "木箱", "📦", Common, 5_000},
	{"bush", "灌木", "🌳", Common, 5_000},
	{"berry_bush", "莓果叢", "🫐", Common, 5_000},
	{"flowerbed", "花圃", "🌼", Common, 5_000},
	{"planter", "花盆", "🪴", Common, 5_000},
	{"tree", "大樹", "🌳", Common, 5_000},
	{"pine", "松樹", "🌲", Common, 5_000},
	{"sakura", "櫻花樹", "🌸", Common, 5_000},
	{"lamp", "路燈", "🏮", Common, 5_000},
	{"signpost", "路牌", "🪧", Common, 5_000},
	{"scarecrow", "稻草人", "🧑‍🌾", Fine, 20_000},
	{"windmill", "小風車", "🌬️", Fine, 20_000},
	{"swing", "鞦韆", "🪢", Fine, 20_000},
	{"doghouse", "狗屋", "🐶", Fine, 20_000},
	{"birdhouse", "鳥屋", "🐦", Fine, 20_000},
	{"pumpkins", "南瓜堆", "🎃", Fine, 20_000},
	{"fountain", "噴水池", "⛲", Rare, 100_000},
	{"gold_cat_statue", "金色貓咪雕像", "🐱", Rare, 100_000},
	{"gazebo", "小涼亭", "⛩️", Rare, 100_000},
	{"wishing_well", "許願井", "🪣", Rare, 100_000},
	{"jack_o_lantern", "南瓜燈", "🎃", Festival, 50_000},
	{"snowman", "雪人", "⛄", Festival, 50_000},
	{"xmas_tree", "聖誕樹", "🎄", Festival, 50_000},
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
