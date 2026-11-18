package balance

// 限時增益道具（Go 版新增，2026-11-18）：使用後在 farm_effects 記一筆限時效果（effect_code = 道具代碼），
// 有效期間再次使用會延長。雨衣稻草人、灑水器在道具商店購買；幸運香包、寵物零食用加工做出來。

const (
	ItemStormGuard  = "storm_guard"  // 雨衣稻草人：暴風不會吹壞作物
	ItemSprinkler   = "sprinkler"    // 灑水器：不是晴天也有晴天的生長加速
	ItemLuckySachet = "lucky_sachet" // 幸運香包：種下的作物突變機率提高、地下菇房稀有菇機率提高
	ItemPetTreat    = "pet_treat"    // 寵物零食：所有寵物的效果 ×1.5

	SachetMutationBP = 300 // 幸運香包：種植時突變機率 +3%（萬分比，加在限時活動的突變加成上）
	SachetRarePct    = 15  // 幸運香包：菇房收成時每朵稀有菇的機率 +15 個百分點
	PetTreatPct      = 150 // 寵物零食：寵物效果 ×1.5
)

// Boost 限時增益道具：效果持續時數與說明。
type Boost struct {
	Code  string
	Hours int
	Emoji string
	Desc  string
}

// BoostOrder 增益道具頁的顯示順序。
var BoostOrder = []string{ItemStormGuard, ItemSprinkler, ItemLuckySachet, ItemPetTreat}

var Boosts = map[string]Boost{
	ItemStormGuard:  {ItemStormGuard, 24, "🧥", "24 小時內暴風不會吹壞作物（收成不會少）"},
	ItemSprinkler:   {ItemSprinkler, 24, "🚿", "24 小時內不是晴天時，澆水的作物也有晴天的生長加速"},
	ItemLuckySachet: {ItemLuckySachet, 2, "🧧", "2 小時內種下的作物突變機率 +3%，地下菇房收成時稀有菇機率 +15%"},
	ItemPetTreat:    {ItemPetTreat, 6, "🦴", "6 小時內所有寵物的效果 ×1.5"},
}

// BoostName 增益道具名稱（商店道具或加工品）。
func BoostName(code string) string {
	if it, ok := Items[code]; ok {
		return it.Name
	}
	if g, ok := Goods[code]; ok {
		return g.Name
	}
	return code
}

// PetTreatBoost 寵物零食生效時的寵物加成 %（無條件捨去，最多 100%）。
func PetTreatBoost(pct int) int { return min(100, pct*PetTreatPct/100) }
