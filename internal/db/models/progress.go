package models

import (
	"encoding/json"
	"time"
)

const (
	TableDailyCheckin       = "daily_checkins"
	TableFarmCodexEntry     = "farm_codex"
	TableFarmAchievement    = "farm_achievements"
	TableFarmTitle          = "farm_titles"
	TableWeeklyShopPurchase = "weekly_shop_purchases"
	TableFarmPet            = "farm_pets"
	TableFeatureSetting     = "feature_settings"
	TableFarmFeedback       = "farm_feedback"
	TableGroupRuins         = "group_ruins"
	TableRuinsContribution  = "ruins_contributions"
	TableRuinsGuardian      = "ruins_guardians"
	TableRuinsGuardianHit   = "ruins_guardian_hits"
	TableRumorDig           = "rumor_digs"
)

// DailyCheckin 每日簽到紀錄（台灣日期）；(farm_id, day) 唯一，每座農場每天只能簽到一次。保存連續天數與發放內容。
//
// 約束：uq_daily_checkins_farm_day（farm_id, day）、ck_daily_checkins_streak_positive、ck_daily_checkins_gold_non_negative。
type DailyCheckin struct {
	ID        int64          `db:"id"`
	FarmID    int64          `db:"farm_id"`
	Day       time.Time      `db:"day"` // DATE（台灣日期）；讀回是 UTC 00:00
	Streak    int            `db:"streak"`
	Gold      int            `db:"gold"`
	Items     map[string]int `db:"items"` // JSON {道具代碼: 數量}
	CreatedAt time.Time      `db:"created_at"`
}

func (DailyCheckin) TableName() string { return TableDailyCheckin }

// FarmCodexEntry 圖鑑：每座農場第一次取得某物品的時間、累計取得數量與最高品質（0 普通～3 傳說）。
//
// 收成、偷菜、收集動物產品、領取加工品時由 GameService._record 登記，山洞素材在領取探險時登記；魚類圖鑑用 farm_fish。
// 約束：主鍵 (farm_id, item_code)、ck_farm_codex_quantity_non_negative、ck_farm_codex_best_quality_valid（0–3）。
type FarmCodexEntry struct {
	FarmID      int64     `db:"farm_id"`
	ItemCode    string    `db:"item_code"`
	Quantity    int       `db:"quantity"`
	BestQuality int       `db:"best_quality"`
	FirstAt     time.Time `db:"first_at"`
}

func (FarmCodexEntry) TableName() string { return TableFarmCodexEntry }

// FarmAchievement 已領取的成就；(farm_id, code) 為主鍵，每個成就只能領一次。成就進度由既有資料即時計算，不另外存。
// 約束：ck_farm_achievements_gold_non_negative。
type FarmAchievement struct {
	FarmID    int64          `db:"farm_id"`
	Code      string         `db:"code"`
	Gold      int            `db:"gold"`
	Items     map[string]int `db:"items"` // JSON {道具代碼: 數量}
	ClaimedAt time.Time      `db:"claimed_at"`
}

func (FarmAchievement) TableName() string { return TableFarmAchievement }

// FarmTitle 玩家擁有的稱號；source 是來源（成就代碼 achievement:<code>、world_boss、shop…）。
// 約束：主鍵 (farm_id, title)。
type FarmTitle struct {
	FarmID     int64     `db:"farm_id"`
	Title      string    `db:"title"`
	Source     string    `db:"source"`
	ObtainedAt time.Time `db:"obtained_at"`
}

func (FarmTitle) TableName() string { return TableFarmTitle }

// WeeklyShopPurchase 每週特賣的購買量（每人每週每項限購）；week_start 是台灣時間的週一。
// 約束：主鍵 (farm_id, week_start, item_code)、ck_weekly_shop_purchases_quantity_positive。
type WeeklyShopPurchase struct {
	FarmID    int64     `db:"farm_id"`
	WeekStart time.Time `db:"week_start"` // DATE（台灣週一）；讀回是 UTC 00:00
	ItemCode  string    `db:"item_code"`
	Quantity  int       `db:"quantity"`
}

func (WeeklyShopPurchase) TableName() string { return TableWeeklyShopPurchase }

// FarmPet 農場的寵物（貓、狗）：購買後 Lv0，最高 Lv4；每種寵物每座農場一隻。數值在 game/pets。
// 約束：主鍵 (farm_id, pet_code)、ck_farm_pets_pet_code_valid（cat／dog）、ck_farm_pets_level_valid（0–10）。
type FarmPet struct {
	FarmID   int64     `db:"farm_id"`
	PetCode  string    `db:"pet_code"`
	Level    int       `db:"level"`
	BoughtAt time.Time `db:"bought_at"`
}

func (FarmPet) TableName() string { return TableFarmPet }

// FeatureSetting 可在管理網站調整的新功能規則（每日簽到、寵物、特賣與升級、成就）：每個 key 一列 JSON。
// 沒有資料列的 key 使用程式內的預設值；驗證與預設值在 game/featurecfg 與各規則套件。
// 約束：主鍵 key。
type FeatureSetting struct {
	Key       string          `db:"key"`
	Value     json.RawMessage `db:"value"` // JSON 物件；以 featurecfg.Decode（UseNumber）解析
	UpdatedAt time.Time       `db:"updated_at"`
	UpdatedBy string          `db:"updated_by"`
}

func (FeatureSetting) TableName() string { return TableFeatureSetting }

// FarmFeedback 操作回饋用的玩家資料（只用於顯示）：上次打開 Discord 面板的時間與個人紀錄（{紀錄代碼: 數值}）。
// 約束：主鍵 farm_id。
type FarmFeedback struct {
	FarmID     int64          `db:"farm_id"`
	LastSeenAt *time.Time     `db:"last_seen_at"`
	Records    map[string]int `db:"records"` // JSON；Python 的 default=dict（INSERT 時要給 '{}'）
}

func (FarmFeedback) TableName() string { return TableFarmFeedback }

// GroupRuins 群組的古代遺跡：修復階段（0–3，3 = 修復完成）與目前階段已捐的素材 {代碼: 數量}。規則在 game/ruins。
//
// 約束：主鍵 group_id、ck_group_ruins_stage_valid（0–3）、ck_group_ruins_completed_consistency（stage = 3 ⇔ completed_at 不為 NULL）。
type GroupRuins struct {
	GroupID     int64          `db:"group_id"`
	Stage       int            `db:"stage"`    // server_default 0
	Progress    map[string]int `db:"progress"` // JSON；Python 的 default=dict（INSERT 時要給 '{}'）
	CompletedAt *time.Time     `db:"completed_at"`
	UpdatedAt   time.Time      `db:"updated_at"`
	// DATE：守護者已結算到哪一週（台灣週一）；讀回是 UTC 00:00
	GuardianSettled *time.Time `db:"guardian_settled"`
	CollapsedAt     *time.Time `db:"collapsed_at"` // 最近一次崩壞的時間
}

func (GroupRuins) TableName() string { return TableGroupRuins }

// RuinsContribution 每座農場對群組遺跡累計捐獻的素材數量（修復貢獻排行、修復完成時發稱號）。
// 約束：主鍵 (group_id, farm_id)、ck_ruins_contributions_quantity_non_negative。
type RuinsContribution struct {
	GroupID   int64     `db:"group_id"`
	FarmID    int64     `db:"farm_id"`
	Quantity  int       `db:"quantity"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (RuinsContribution) TableName() string { return TableRuinsContribution }

// RuinsGuardian 群組每週一隻的遺跡守護者（week_start 是台灣時間週一）：HP、累計傷害與被打倒的時間。
// 約束：主鍵 (group_id, week_start)、ck_ruins_guardians_hp_positive、ck_ruins_guardians_damage_non_negative。
type RuinsGuardian struct {
	GroupID    int64      `db:"group_id"`
	WeekStart  time.Time  `db:"week_start"` // DATE（台灣週一）；讀回是 UTC 00:00
	HPMax      int        `db:"hp_max"`
	Damage     int        `db:"damage"` // server_default 0
	DefeatedAt *time.Time `db:"defeated_at"`
}

func (RuinsGuardian) TableName() string { return TableRuinsGuardian }

// RuinsGuardianHit 每座農場每週對守護者造成的傷害（排行用）。
// 約束：主鍵 (group_id, week_start, farm_id)、ck_ruins_guardian_hits_damage_non_negative。
type RuinsGuardianHit struct {
	GroupID   int64     `db:"group_id"`
	WeekStart time.Time `db:"week_start"` // DATE（台灣週一）；讀回是 UTC 00:00
	FarmID    int64     `db:"farm_id"`
	Damage    int       `db:"damage"`
}

func (RuinsGuardianHit) TableName() string { return TableRuinsGuardianHit }

// RumorDig 在日報傳聞的農場挖寶：每座農場每天（傳聞所屬的日報日期）只能挖一次；寶藏每個群組每天只會被挖到一次。
//
// 約束：主鍵 (farm_id, day)、ck_rumor_digs_gold_valid（gold >= 0 AND (found OR gold = 0)）、
// uq_rumor_digs_group_day_found（group_id, day WHERE found，唯一索引）。
type RumorDig struct {
	FarmID       int64     `db:"farm_id"`
	Day          time.Time `db:"day"` // DATE（日報日期）；讀回是 UTC 00:00
	GroupID      int64     `db:"group_id"`
	TargetFarmID int64     `db:"target_farm_id"`
	Found        bool      `db:"found"`
	Gold         int       `db:"gold"`
	CreatedAt    time.Time `db:"created_at"`
}

func (RumorDig) TableName() string { return TableRumorDig }
