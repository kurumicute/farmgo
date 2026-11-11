package models

import (
	"encoding/json"
	"time"
)

// ---------- 釣魚（Migration 0016） ----------

const (
	TableFishType        = "fish_types"
	TableFishingGear     = "fishing_gear"
	TableFarmFishingGear = "farm_fishing_gear"
	TableFishingSetting  = "fishing_settings"
	TableFishingSession  = "fishing_sessions"
	TableFarmFish        = "farm_fish"
	TableFishShare       = "fish_shares"
	TableFishingGuard    = "fishing_guards"
)

// FishType 魚種（管理網站可新增、修改、停用）。代碼建立後不可修改；停用後不會再被釣到，已釣到的魚仍可販售。
//
// 約束：uq_fish_types_code、uq_fish_types_name、ck_fish_types_rarity_valid（FishRarities）、ck_fish_types_price_order、
// ck_fish_types_price_non_negative（售價不受稀有度限制，Migration 0017）、ck_fish_types_weight_order、
// ck_fish_types_difficulty_range（1–5）、ck_fish_types_hours_valid。
type FishType struct {
	ID          int64  `db:"id"`
	Code        string `db:"code"`
	Name        string `db:"name"`
	Emoji       string `db:"emoji"` // server_default '🐟'
	Rarity      string `db:"rarity"`
	MinPrice    int    `db:"min_price"`
	MaxPrice    int    `db:"max_price"`
	MinWeightG  int    `db:"min_weight_g"`
	MaxWeightG  int    `db:"max_weight_g"`
	Difficulty  int    `db:"difficulty"`  // server_default 3；掙扎難度（體力倍率）
	Description string `db:"description"` // server_default ''
	SortOrder   int    `db:"sort_order"`  // server_default 0
	Active      bool   `db:"active"`      // server_default true
	// 只有裝備這些魚餌（fishing_gear.code 清單）時才釣得到；NULL（nil）= 任何魚餌都可以（Migration 0033）
	RequiredBaits []string `db:"required_baits"`
	// 季節與時段限定（game/seasons；NULL = 不限，Migration 0034）
	Seasons   []string `db:"seasons"`
	HourStart *int     `db:"hour_start"`
	HourEnd   *int     `db:"hour_end"`
}

func (FishType) TableName() string { return TableFishType }

// FishingGear 釣具商品：魚竿、魚線、魚餌。effects 依類型（fishing.GEAR_EFFECT_LIMITS）；stock NULL 表示不限量。
//
// 約束：uq_fishing_gear_code、uq_fishing_gear_name、ck_fishing_gear_kind_valid（rod／line／bait）、
// ck_fishing_gear_price_positive、ck_fishing_gear_required_level_positive、ck_fishing_gear_stock_non_negative。
type FishingGear struct {
	ID            int64          `db:"id"`
	Code          string         `db:"code"`
	Kind          string         `db:"kind"`
	Name          string         `db:"name"`
	Emoji         string         `db:"emoji"`       // server_default '🎣'
	Description   string         `db:"description"` // server_default ''
	Price         int            `db:"price"`
	RequiredLevel int            `db:"required_level"` // server_default 1
	Stock         *int           `db:"stock"`
	Effects       map[string]int `db:"effects"`    // JSON（fishing.Effects）
	SortOrder     int            `db:"sort_order"` // server_default 0
	Active        bool           `db:"active"`     // server_default true
}

func (FishingGear) TableName() string { return TableFishingGear }

// FarmFishingGear 玩家持有的釣具：魚竿、魚線數量固定 1；魚餌記數量。每種類型最多裝備一件（部分唯一索引）。
//
// 約束：uq_farm_fishing_gear_farm_gear（farm_id, gear_code）、ck_farm_fishing_gear_kind_valid、
// ck_farm_fishing_gear_quantity_non_negative、uq_farm_fishing_gear_equipped（farm_id, kind WHERE equipped，唯一索引）。
// 外鍵：gear_code → fishing_gear.code。
type FarmFishingGear struct {
	ID         int64     `db:"id"`
	FarmID     int64     `db:"farm_id"`
	GearCode   string    `db:"gear_code"`
	Kind       string    `db:"kind"`
	Quantity   int       `db:"quantity"`
	Equipped   bool      `db:"equipped"` // server_default false
	AcquiredAt time.Time `db:"acquired_at"`
}

func (FarmFishingGear) TableName() string { return TableFarmFishingGear }

// FishingSetting 釣魚設定的版本紀錄（最新版本生效，只新增不修改）；拋竿時把當時的設定存進 Session。
// 約束：主鍵 version、ck_fishing_settings_version_positive。
type FishingSetting struct {
	Version   int             `db:"version"`
	Settings  json.RawMessage `db:"settings"` // JSON，以 fishing.ValidateSettings 解析
	CreatedBy string          `db:"created_by"`
	CreatedAt time.Time       `db:"created_at"`
}

func (FishingSetting) TableName() string { return TableFishingSetting }

// FishingSession 一次釣魚：拋竿時扣魚餌並決定魚種、重量、價格與脫鉤回合，之後依時間與操作推進。
// casting／fighting 為進行中（每座農場最多一筆，部分唯一索引）；其餘為結束狀態。
// settings／gear 是拋竿當下的快照，管理員之後修改設定不影響進行中的釣魚。
//
// 約束：ck_fishing_sessions_status_valid（FishingStatuses）、ck_fishing_sessions_rarity_valid、
// ck_fishing_sessions_time_order（react_deadline > bite_at > started_at）、
// ck_fishing_sessions_ended_consistent（進行中 ⇔ ended_at IS NULL）、ck_fishing_sessions_turns_non_negative、
// uq_fishing_sessions_farm_active（farm_id WHERE status IN ('casting', 'fighting')，唯一索引）、
// ix_fishing_sessions_farm_started、ix_fishing_sessions_started_at。
type FishingSession struct {
	ID              int64           `db:"id"`
	FarmID          int64           `db:"farm_id"`
	Status          string          `db:"status"`
	SettingsVersion int             `db:"settings_version"`
	Settings        json.RawMessage `db:"settings"` // JSON 設定快照
	Gear            json.RawMessage `db:"gear"`     // JSON {"rod": {...}, "line": {...}, "bait": {...}}
	FishCode        string          `db:"fish_code"`
	Rarity          string          `db:"rarity"`
	WeightG         int             `db:"weight_g"`
	Price           int             `db:"price"`
	State           json.RawMessage `db:"state"` // JSON 拉扯狀態（fishing.State；保留原始 JSON 以免遺失未知欄位）
	Turns           int             `db:"turns"` // server_default 0
	StartedAt       time.Time       `db:"started_at"`
	BiteAt          time.Time       `db:"bite_at"`
	ReactDeadline   time.Time       `db:"react_deadline"`
	ActionDeadline  *time.Time      `db:"action_deadline"`
	EndedAt         *time.Time      `db:"ended_at"`
	ContextGroupID  *int64          `db:"context_group_id"`
}

func (FishingSession) TableName() string { return TableFishingSession }

// FarmFish 釣到的魚（每隻一列，含重量與售價）。sold_at 有值表示已販售（保留作為捕獲紀錄與圖鑑）。
// session_id 唯一：同一次釣魚不會發放兩隻魚。
//
// 約束：uq_farm_fish_session_id、ck_farm_fish_rarity_valid、ck_farm_fish_price_non_negative（Migration 0021）、
// ck_farm_fish_weight_positive、ck_farm_fish_sold_consistent（sold_at 與 sold_price 同時為 NULL）、
// ck_farm_fish_sold_or_donated、ix_farm_fish_farm_unsold（farm_id WHERE sold_at IS NULL）、ix_farm_fish_farm_caught。
// 外鍵：fish_code → fish_types.code。
type FarmFish struct {
	ID        int64      `db:"id"`
	FarmID    int64      `db:"farm_id"`
	SessionID int64      `db:"session_id"`
	FishCode  string     `db:"fish_code"`
	Rarity    string     `db:"rarity"` // 捕獲當下的稀有度與價值（之後不變）
	WeightG   int        `db:"weight_g"`
	Price     int        `db:"price"`
	CaughtAt  time.Time  `db:"caught_at"`
	SoldAt    *time.Time `db:"sold_at"`
	SoldPrice *int       `db:"sold_price"`
	// 捐獻給世界 Boss 的時間（Migration 0019）；有值時不在魚簍中，也不能販售或分享
	DonatedAt *time.Time `db:"donated_at"`
}

func (FarmFish) TableName() string { return TableFarmFish }

// FishShare 魚類分享紀錄（分享不轉移、不消耗魚，也沒有獎勵）。
// 約束：ix_fish_shares_farm（farm_id, shared_at）、ix_fish_shares_fish。
type FishShare struct {
	ID       int64     `db:"id"`
	FishID   int64     `db:"fish_id"`
	FarmID   int64     `db:"farm_id"`
	GroupID  *int64    `db:"group_id"`
	SharedAt time.Time `db:"shared_at"`
}

func (FishShare) TableName() string { return TableFishShare }

// FishingGuard 釣魚防腳本狀態（每座農場一列，第一次拋竿時建立）：距離上次人機確認的拋竿次數、可疑分數、題目與暫停時間。
// 規則在 game/fishing（Guard*）；只影響能不能拋竿，不影響資產。
//
// 約束：主鍵 farm_id、ck_fishing_guards_counters_valid（casts_since_check >= 0 AND check_after > 0 AND suspicion >= 0）。
type FishingGuard struct {
	FarmID              int64           `db:"farm_id"`
	CastsSinceCheck     int             `db:"casts_since_check"` // server_default 0
	CheckAfter          int             `db:"check_after"`
	Suspicion           int             `db:"suspicion"`            // server_default 0
	Challenge           json.RawMessage `db:"challenge"`            // JSON {"answer": 代碼, "options": [代碼, …]}；NULL 為 nil
	ChallengeExpiresAt  *time.Time      `db:"challenge_expires_at"` //
	LockedUntil         *time.Time      `db:"locked_until"`
	ConsecutiveFailures int             `db:"consecutive_failures"` // server_default 0
	FailedTotal         int             `db:"failed_total"`         // server_default 0
	PassedTotal         int             `db:"passed_total"`         // server_default 0
}

func (FishingGuard) TableName() string { return TableFishingGuard }
