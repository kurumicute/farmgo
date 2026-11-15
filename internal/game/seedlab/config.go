package seedlab

// 可在管理網站調整的種子實驗室設定（featurecfg key = ConfigKey，存在 feature_settings，不需要 Migration）。
//
//   - 可調整：開放等級、建造費用、種子庫上限、鑑定費、培育與突變研究時間、各種機率、基因效果、分享上限、研究經驗、
//     各雜交組合的成功率與培育時間。
//   - 固定：雜交組合（哪兩種作物 → 哪種雜交作物）、基因種類與 1–5 級、突變種類、研究等級上限 3（研究槽 1–3 個）。
//   - 雜交作物的售價等目錄資料在管理網站的「作物目錄」調整（crop_types）。
//   - 機率以萬分比（bp）存放；管理網站以「%」顯示與輸入。

import (
	"sync"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

const ConfigKey = "seedlab"

// HybridSetting 一個雜交組合可調整的數值。
type HybridSetting struct {
	SuccessBP int // 基礎成功率
	Minutes   int // 培育時間
}

// Config 目前生效的設定（唯讀）。
type Config struct {
	UnlockLevel           int
	BuildGold             int
	LibraryCapacity       int
	IdentifyFeeMultiplier int
	IdentifyFeeMin        int
	SameSpeciesMinutes    int
	MutateMinutes         int
	ResinSuccessBP        int
	HerbTimePct           int
	CatalystUpBP          int
	GeneUpBP              int
	GeneDownBP            int
	InheritMutationBP     int // 雙親有突變時，子代遺傳突變的機率
	CrossMutationPerLvBP  int // 雙親都沒有突變時，子代依突變基因每級的自然突變機率
	MutateCrystal         int
	MutateBaseBP          int
	MutatePerLevelBP      int
	MutateMaxBP           int
	InheritBP             int // 收成特殊種子時取回同基因種子的機率
	DominantBP            int // 優勢遺傳：每個基因直接繼承雙親中較高等級的機率（其餘機率才是母本、父本各半）
	CloneFeeBase          int // 育苗的基本費用（金幣）
	CloneFeePerLevel      int // 育苗費用：每多 (基因總和 − 4)² 的金幣
	CloneHerb             int // 育苗加藥草（選用）時每次用掉的藥草
	CloneMinutes          int // 每次育苗的時間
	CloneTwoBP            int // 每次育苗得到 2 顆以上的機率
	CloneThreeBP          int // 每次育苗得到 3 顆的機率
	CloneHerbBonusBP      int // 加藥草時，2 顆以上與 3 顆的機率各增加
	CloneMaxBatches       int // 一次最多排程幾次育苗
	ShareSendPerDay       int
	ShareReceiveSenders   int
	GrowthPctPerLevel     int
	YieldPctPerLevel      int // 產量基因每級增加的收成：作物原本收成數量的 %（無條件進位，所以 2 級起至少 +1）
	QualityBPPerLevel     int
	MutationBPPerLevel    int
	ExpIdentify           int
	ExpCross              int
	ExpCrossFail          int
	ExpMutate             int
	ExpClone              int
	LevelExp              [MaxLevel + 1]int // 升到該等級需要的累積經驗（只用 index 2..MaxLevel）
	Hybrids               map[string]HybridSetting
}

// defaultHybrids 各雜交組合的預設成功率與培育時間（1–3 小時）。
var defaultHybrids = map[string]HybridSetting{
	"golden_grain":        {7000, 60},
	"rainbow_carrot":      {6500, 90},
	"purple_berry":        {6000, 120},
	"honey_melon":         {5500, 150},
	"lantern_gourd":       {5000, 150},
	"starry_night_flower": {4000, 180},
	"ruby_corn":           {6500, 90},
	"sakura_wheat":        {6000, 90},
	"sunset_squash":       {6000, 120},
	"jade_grape":          {5000, 150},
	"dawn_melon":          {4500, 180},
	"crimson_moon_flower": {4000, 180},
}

// DefaultConfig 預設設定（每次回傳新的副本）。
//
// 2026-11-14 基因強化平衡（玩家回饋：鑑定後的種子收益比一般種子還低、改良基因太慢）：
//   - 鑑定費 種子價 ×5（正式環境 ×2）、最低 50 → ×1、最低 10：原本小麥鑑定費是種子價的 10 倍、比兩次收成還貴。
//   - 收成取回種子 30% → 60%：一顆特殊種子平均能種 2.5 次（原本 1.4 次），投資攤得開。
//   - 生長 每級 -5% → -6%；產量 每 2 級 +1 → 每級 +15%（無條件進位，鑑定出的 2 級就 +1）；
//     品質 每級 +1% → +5%；突變 每級 +0.5% → +1.5%。原本品質、突變加成換算成售價不到 +2%，幾乎沒有感覺。
//   - 基因升級 15% → 25%、退化 10% → 5%；同品種改良 60 → 30 分鐘：改良到高等級的時間大約減半。
//   - 結果（依正式環境作物價格試算，含鑑定費與取回機率）：剛鑑定的種子每小時收益約為一般種子的 1.2–1.6 倍
//     （原本 0.2–0.8 倍，小麥甚至虧錢）；全部 5 級約 2.5–3.9 倍，是長期培育的目標。
//
// 2026-11-15 培育減負（玩家回饋：雜交 2 顆換 1 顆，要「金字塔」堆疊、種子庫不夠放；弱親本 50% 拖累；種滿農田要大量種子）：
//   - 雜交只消耗父本，母本放回種子庫（services.collectCross）：種子數量不再每代減半。
//   - 優勢遺傳 70%：每個基因 70% 直接繼承較高的那個親本，其餘 30% 才是母本、父本各半。
//     3333 × 2222 得到全部 ≥3 級的機率約 13% → 約 57%（含新的升降機率）。
//   - 育苗（新功能）：付金幣與藥草，在研究槽複製一顆同基因的種子（不複製突變），用來種滿農田；
//     費用 300 + 20 ×（基因總和 − 4）²：2222 620、3333 1,580、4444 3,180、5555 5,420 金幣。
//   - 收成取回種子 60% → 85%、基因退化 5% → 2%、水晶催化 +20% → +30%。
//
// 2026-11-15 育苗改版（使用者要求）：只要金幣就能育苗，藥草改成選用；每次得到 1–3 顆（2 顆以上 30%、3 顆 10%，
// 加藥草各 +20% → 50%、30%；平均 1.4 顆 → 1.8 顆）；一次可以排程 1–10 次（費用、時間 × 次數，最後一起領取）。
func DefaultConfig() *Config {
	c := &Config{
		UnlockLevel: 5, BuildGold: 5000, LibraryCapacity: 25,
		IdentifyFeeMultiplier: 1, IdentifyFeeMin: 10,
		SameSpeciesMinutes: 30, MutateMinutes: 120,
		ResinSuccessBP: 1500, HerbTimePct: 30, CatalystUpBP: 3000,
		GeneUpBP: 2500, GeneDownBP: 200, InheritMutationBP: 5000, CrossMutationPerLvBP: 100,
		MutateCrystal: 2, MutateBaseBP: 3000, MutatePerLevelBP: 500, MutateMaxBP: 9000,
		InheritBP: 8500, ShareSendPerDay: 5, ShareReceiveSenders: 1,
		DominantBP: 7000, CloneFeeBase: 300, CloneFeePerLevel: 20, CloneHerb: 1, CloneMinutes: 60,
		CloneTwoBP: 3000, CloneThreeBP: 1000, CloneHerbBonusBP: 2000, CloneMaxBatches: 10,
		GrowthPctPerLevel: 6, YieldPctPerLevel: 15, QualityBPPerLevel: 500, MutationBPPerLevel: 150,
		ExpIdentify: 2, ExpCross: 10, ExpCrossFail: 15, ExpMutate: 8, ExpClone: 3,
		LevelExp: [MaxLevel + 1]int{0, 0, 100, 300},
		Hybrids:  map[string]HybridSetting{},
	}
	for code, h := range defaultHybrids {
		c.Hybrids[code] = h
	}
	return c
}

var (
	cfgMu      sync.Mutex
	cfgVersion = -1
	cfgCache   *Config
	cfgDefault = DefaultConfig()
)

// Current 目前生效的設定。
func Current() *Config {
	if featurecfg.IsDefault(ConfigKey) {
		return cfgDefault
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if v := featurecfg.Version(); cfgCache == nil || cfgVersion != v {
		c, err := parseConfig(featurecfg.Get(ConfigKey))
		if err != nil {
			c = cfgDefault
		}
		cfgCache, cfgVersion = c, v
	}
	return cfgCache
}

type intField struct {
	key, label string
	low, high  int
	ptr        func(c *Config) *int
}

// intFields 所有整數設定（JSON key、名稱、範圍）。機率（bp）的範圍是 0–10000。
var intFields = []intField{
	{"unlock_level", "開放等級", 1, 100, func(c *Config) *int { return &c.UnlockLevel }},
	{"build_gold", "建造費用", 0, 100_000_000, func(c *Config) *int { return &c.BuildGold }},
	{"library_capacity", "種子庫上限", 1, 200, func(c *Config) *int { return &c.LibraryCapacity }}, // 超過 25 顆時 Discord 分頁顯示
	{"identify_fee_multiplier", "鑑定費（種子價格的倍數）", 0, 100, func(c *Config) *int { return &c.IdentifyFeeMultiplier }},
	{"identify_fee_min", "最低鑑定費", 0, 1_000_000, func(c *Config) *int { return &c.IdentifyFeeMin }},
	{"same_species_minutes", "同品種培育時間（分鐘）", 1, 1440, func(c *Config) *int { return &c.SameSpeciesMinutes }},
	{"mutate_minutes", "突變研究時間（分鐘）", 1, 1440, func(c *Config) *int { return &c.MutateMinutes }},
	{"resin_success_bp", "樹脂提高的成功率", 0, 10_000, func(c *Config) *int { return &c.ResinSuccessBP }},
	{"herb_time_pct", "藥草縮短的時間（%）", 0, 90, func(c *Config) *int { return &c.HerbTimePct }},
	{"catalyst_up_bp", "水晶催化提高的升級機率", 0, 10_000, func(c *Config) *int { return &c.CatalystUpBP }},
	{"gene_up_bp", "基因升級機率", 0, 10_000, func(c *Config) *int { return &c.GeneUpBP }},
	{"gene_down_bp", "基因退化機率", 0, 10_000, func(c *Config) *int { return &c.GeneDownBP }},
	{"inherit_mutation_bp", "雙親有突變時遺傳的機率", 0, 10_000, func(c *Config) *int { return &c.InheritMutationBP }},
	{"cross_mutation_per_level_bp", "雜交時每級突變基因的自然突變機率", 0, 2_000, func(c *Config) *int { return &c.CrossMutationPerLvBP }},
	{"mutate_crystal", "突變研究需要的水晶", 0, 100, func(c *Config) *int { return &c.MutateCrystal }},
	{"mutate_base_bp", "突變研究基礎成功率", 0, 10_000, func(c *Config) *int { return &c.MutateBaseBP }},
	{"mutate_per_level_bp", "突變基因每級增加的成功率", 0, 10_000, func(c *Config) *int { return &c.MutatePerLevelBP }},
	{"mutate_max_bp", "突變研究成功率上限", 0, 10_000, func(c *Config) *int { return &c.MutateMaxBP }},
	{"inherit_bp", "收成時取回種子的機率", 0, 10_000, func(c *Config) *int { return &c.InheritBP }},
	{"dominant_bp", "優勢遺傳（繼承較高親本）的機率", 0, 10_000, func(c *Config) *int { return &c.DominantBP }},
	{"clone_fee_base", "育苗基本費用", 0, 10_000_000, func(c *Config) *int { return &c.CloneFeeBase }},
	{"clone_fee_per_level", "育苗費用（每 (基因總和−4)²）", 0, 1_000_000, func(c *Config) *int { return &c.CloneFeePerLevel }},
	{"clone_herb", "育苗加藥草時每次用掉的藥草", 1, 100, func(c *Config) *int { return &c.CloneHerb }},
	{"clone_minutes", "每次育苗時間（分鐘）", 1, 1440, func(c *Config) *int { return &c.CloneMinutes }},
	{"clone_two_bp", "育苗得到 2 顆以上的機率", 0, 10_000, func(c *Config) *int { return &c.CloneTwoBP }},
	{"clone_three_bp", "育苗得到 3 顆的機率", 0, 10_000, func(c *Config) *int { return &c.CloneThreeBP }},
	{"clone_herb_bonus_bp", "育苗加藥草增加的機率", 0, 10_000, func(c *Config) *int { return &c.CloneHerbBonusBP }},
	{"clone_max_batches", "一次最多排程幾次育苗", 1, 25, func(c *Config) *int { return &c.CloneMaxBatches }},
	{"share_send_per_day", "每天最多送出的種子", 0, 100, func(c *Config) *int { return &c.ShareSendPerDay }},
	{"share_receive_senders", "每天最多收幾位玩家的分享", 1, 100, func(c *Config) *int { return &c.ShareReceiveSenders }},
	{"growth_pct_per_level", "生長基因每級縮短的時間（%）", 0, 20, func(c *Config) *int { return &c.GrowthPctPerLevel }},
	{"yield_pct_per_level", "產量基因每級增加的收成（%）", 0, 50, func(c *Config) *int { return &c.YieldPctPerLevel }},
	{"quality_bp_per_level", "品質基因每級的品質加成", 0, 1_000, func(c *Config) *int { return &c.QualityBPPerLevel }},
	{"mutation_bp_per_level", "突變基因每級的突變加成", 0, 1_000, func(c *Config) *int { return &c.MutationBPPerLevel }},
	{"exp_identify", "鑑定的研究經驗", 0, 10_000, func(c *Config) *int { return &c.ExpIdentify }},
	{"exp_cross", "雜交成功的研究經驗", 0, 10_000, func(c *Config) *int { return &c.ExpCross }},
	{"exp_cross_fail", "雜交失敗的研究經驗", 0, 10_000, func(c *Config) *int { return &c.ExpCrossFail }},
	{"exp_mutate", "突變研究的研究經驗", 0, 10_000, func(c *Config) *int { return &c.ExpMutate }},
	{"exp_clone", "育苗的研究經驗", 0, 10_000, func(c *Config) *int { return &c.ExpClone }},
	{"level2_exp", "研究 Lv.2 需要的經驗", 1, 1_000_000, func(c *Config) *int { return &c.LevelExp[2] }},
	{"level3_exp", "研究 Lv.3 需要的經驗", 1, 1_000_000, func(c *Config) *int { return &c.LevelExp[3] }},
}

func encodeConfig(c *Config) featurecfg.Value {
	v := featurecfg.Value{}
	for _, f := range intFields {
		v[f.key] = *f.ptr(c)
	}
	hybrids := map[string]any{}
	for _, h := range Hybrids {
		s := c.Hybrids[h.Code]
		hybrids[h.Code] = map[string]any{"success_bp": s.SuccessBP, "minutes": s.Minutes}
	}
	v["hybrids"] = hybrids
	return v
}

func parseConfig(raw featurecfg.Value) (*Config, error) {
	c := &Config{Hybrids: map[string]HybridSetting{}}
	defaults := DefaultConfig()
	for _, f := range intFields {
		v, ok := raw[f.key]
		if !ok { // 新增的設定項目：之前存的快照沒有這個 key 時用預設值（不讓整份設定失效）
			*f.ptr(c) = *f.ptr(defaults)
			continue
		}
		n, err := featurecfg.IntValue(v, f.label, f.low, f.high)
		if err != nil {
			return nil, err
		}
		*f.ptr(c) = n
	}
	if c.GeneUpBP+c.GeneDownBP > 10_000 {
		return nil, gameerr.Validationf("基因升級與退化機率加起來不能超過 100%%。")
	}
	if c.CloneThreeBP > c.CloneTwoBP {
		return nil, gameerr.Validationf("育苗得到 3 顆的機率不能高於 2 顆以上的機率。")
	}
	if c.MutateBaseBP > c.MutateMaxBP {
		return nil, gameerr.Validationf("突變研究基礎成功率不能高於上限。")
	}
	if c.LevelExp[3] <= c.LevelExp[2] {
		return nil, gameerr.Validationf("研究 Lv.3 需要的經驗必須大於 Lv.2。")
	}
	hybrids, ok := featurecfg.AsMap(raw["hybrids"])
	if !ok {
		return nil, gameerr.Validationf("雜交組合的設定格式不正確。")
	}
	for _, h := range Hybrids {
		m, ok := featurecfg.AsMap(hybrids[h.Code])
		if !ok {
			return nil, gameerr.Validationf("缺少雜交作物「%s」的設定。", h.Code)
		}
		success, err := featurecfg.IntValue(m["success_bp"], h.Code+" 的成功率", 0, 10_000)
		if err != nil {
			return nil, err
		}
		minutes, err := featurecfg.IntValue(m["minutes"], h.Code+" 的培育時間（分鐘）", 1, 1440)
		if err != nil {
			return nil, err
		}
		c.Hybrids[h.Code] = HybridSetting{success, minutes}
	}
	return c, nil
}

// ValidateConfig 驗證並正規化種子實驗室設定。
func ValidateConfig(raw featurecfg.Value) (featurecfg.Value, error) {
	c, err := parseConfig(raw)
	if err != nil {
		return nil, err
	}
	return encodeConfig(c), nil
}

// DefaultValue 預設設定的 JSON 形狀（測試與管理網站用）。
func DefaultValue() featurecfg.Value { return encodeConfig(DefaultConfig()) }

func init() {
	featurecfg.Register(ConfigKey, "種子實驗室", DefaultValue, ValidateConfig)
}
