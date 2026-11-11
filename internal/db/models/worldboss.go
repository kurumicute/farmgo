package models

import (
	"encoding/json"
	"time"
)

// ---------- 世界 Boss（Migration 0019） ----------

const (
	TableWorldBossSchedule     = "world_boss_schedules"
	TableWorldBossEvent        = "world_boss_events"
	TableWorldBossSupply       = "world_boss_supplies"
	TableWorldBossContribution = "world_boss_contributions"
	TableWorldBossRewardClaim  = "world_boss_reward_claims"
)

// WorldBossSchedule 固定週期出現的世界 Boss（例如每 2 天晚上 9 點的深淵魔龍）。group_id NULL 表示全服。
// 系統在上一場結束後自動建立下一場（world_boss_events，含 slot_at），管理員可在出現前個別修改那一場的時間與獎勵；
// settings／rewards 是之後每一場的範本。
//
// 約束：ck_world_boss_schedules_name_len（1–40）、ck_world_boss_schedules_interval_range（1–30）、
// ck_world_boss_schedules_time_range、ck_world_boss_schedules_duration_range（10–10080）、
// ck_world_boss_schedules_max_hp_positive。
type WorldBossSchedule struct {
	ID              int64           `db:"id"`
	Name            string          `db:"name"`
	GroupID         *int64          `db:"group_id"`
	IntervalDays    int             `db:"interval_days"`
	SpawnHour       int             `db:"spawn_hour"` // 台灣時間
	SpawnMinute     int             `db:"spawn_minute"`
	AnchorDate      time.Time       `db:"anchor_date"` // DATE：從這一天（台灣日期）起每 interval_days 天出現；讀回是 UTC 00:00
	DurationMinutes int             `db:"duration_minutes"`
	MaxHP           int             `db:"max_hp"`
	Settings        json.RawMessage `db:"settings"` // JSON（worldboss.Settings）
	Rewards         json.RawMessage `db:"rewards"`  // JSON（worldboss.Rewards）
	Enabled         bool            `db:"enabled"`  // server_default true
	CreatedBy       string          `db:"created_by"`
	CreatedAt       time.Time       `db:"created_at"`
	UpdatedAt       time.Time       `db:"updated_at"`
}

func (WorldBossSchedule) TableName() string { return TableWorldBossSchedule }

// WorldBossEvent 一場世界 Boss 活動：group_id 為所屬 Discord 群組，NULL 表示全服（所有玩家都能參加）。
// HP、時間、狀態與設定都存在這裡；status：active（未開始或進行中，依 starts_at 判斷）、paused、ended。
// 手動建立的活動：同一範圍（全服或同一群組）最多一場未結束（部分唯一索引）。
// 排程產生的活動：記錄 schedule_id 與排定時間 slot_at，(schedule_id, slot_at) 唯一，每個排程同時最多一場未結束。
// settings／rewards 是建立時的快照，管理員可在結束前修改；結束後保留作為歷史紀錄。
//
// 約束：ck_world_boss_events_status_valid、ck_world_boss_events_hp_range、ck_world_boss_events_level_positive、
// ck_world_boss_events_ends_after_start、ck_world_boss_events_name_len、ck_world_boss_events_ended_consistent、
// ck_world_boss_events_end_reason_valid（defeated／timeout／admin）、ck_world_boss_events_defeated_consistent、
// uq_world_boss_events_scope_open（COALESCE(group_id, 0) WHERE status IN ('active', 'paused') AND schedule_id IS NULL）、
// uq_world_boss_events_schedule_open（schedule_id WHERE status IN ('active', 'paused') AND schedule_id IS NOT NULL）、
// uq_world_boss_events_schedule_slot（schedule_id, slot_at）、ix_world_boss_events_group_created、
// ix_world_boss_events_status_ends。
type WorldBossEvent struct {
	ID         int64      `db:"id"`
	GroupID    *int64     `db:"group_id"`
	ScheduleID *int64     `db:"schedule_id"`
	SlotAt     *time.Time `db:"slot_at"`
	Name       string     `db:"name"`
	Level      int        `db:"level"` // 目前介面不顯示（保留欄位）
	MaxHP      int        `db:"max_hp"`
	HP         int        `db:"hp"`
	Status     string     `db:"status"`
	StartsAt   time.Time  `db:"starts_at"`
	EndsAt     time.Time  `db:"ends_at"`
	// 戰鬥模擬已推進到的時間（之後的傷害依當下補給計算）
	LastTickAt time.Time  `db:"last_tick_at"`
	PausedAt   *time.Time `db:"paused_at"`
	DefeatedAt *time.Time `db:"defeated_at"`
	EndedAt    *time.Time `db:"ended_at"`
	EndReason  *string    `db:"end_reason"`
	// 系統確認活動結束（獎勵開放領取）的時間
	SettledAt *time.Time      `db:"settled_at"`
	Settings  json.RawMessage `db:"settings"` // JSON（worldboss.Settings）
	Rewards   json.RawMessage `db:"rewards"`  // JSON（worldboss.Rewards）
	CreatedBy string          `db:"created_by"`
	CreatedAt time.Time       `db:"created_at"`
	UpdatedAt time.Time       `db:"updated_at"`
}

func (WorldBossEvent) TableName() string { return TableWorldBossEvent }

// WorldBossSupply 各補給類型的進度（積分）與目標；建立活動時建立四列，捐獻時在同一筆交易中累加。
//
// 約束：主鍵 (event_id, supply_code)、ck_world_boss_supplies_supply_valid（food／medical／combat／equipment）、
// ck_world_boss_supplies_target_positive、ck_world_boss_supplies_progress_non_negative。
type WorldBossSupply struct {
	EventID    int64  `db:"event_id"`
	SupplyCode string `db:"supply_code"`
	Target     int    `db:"target"`
	Progress   int    `db:"progress"` // server_default 0
}

func (WorldBossSupply) TableName() string { return TableWorldBossSupply }

// WorldBossContribution 捐獻紀錄（只新增）。request_id 由面板產生，(event_id, player_id, request_id) 唯一：重複點擊不會重複扣物品。
//
// 約束：uq_world_boss_contributions_request（event_id, player_id, request_id）、
// ck_world_boss_contributions_category_valid（crop／fish／material／goods／special）、
// ck_world_boss_contributions_supply_valid、ck_world_boss_contributions_quantity_positive、
// ck_world_boss_contributions_points_positive、ck_world_boss_contributions_damage_non_negative、
// ix_world_boss_contributions_event_player、ix_world_boss_contributions_event_created。
type WorldBossContribution struct {
	ID         int64     `db:"id"`
	EventID    int64     `db:"event_id"`
	PlayerID   int64     `db:"player_id"`
	FarmID     int64     `db:"farm_id"`
	RequestID  string    `db:"request_id"`
	Category   string    `db:"category"`
	SupplyCode string    `db:"supply_code"`
	ItemCode   string    `db:"item_code"`
	Quality    *string   `db:"quality"`
	Mutation   *string   `db:"mutation"`
	Quantity   int       `db:"quantity"`
	Points     int       `db:"points"`
	Damage     int       `db:"damage"` // server_default 0；捐獻時立即造成的傷害
	CreatedAt  time.Time `db:"created_at"`
}

func (WorldBossContribution) TableName() string { return TableWorldBossContribution }

// WorldBossRewardClaim 獎勵領取紀錄；(event_id, player_id) 唯一，每位玩家每場活動只能領一次。保存當時的名次、積分與發放內容。
//
// 約束：uq_world_boss_reward_claims_once（event_id, player_id）、ck_world_boss_reward_claims_outcome_valid
// （victory／participation）、ck_world_boss_reward_claims_gold_non_negative、ck_world_boss_reward_claims_rank_positive、
// ix_world_boss_reward_claims_player。
type WorldBossRewardClaim struct {
	ID         int64          `db:"id"`
	EventID    int64          `db:"event_id"`
	PlayerID   int64          `db:"player_id"`
	PlayerName string         `db:"player_name"`
	Rank       int            `db:"rank"`
	Points     int            `db:"points"`
	Outcome    string         `db:"outcome"`
	Gold       int            `db:"gold"`
	Items      map[string]int `db:"items"` // JSON {物品代碼: 數量}
	Title      *string        `db:"title"`
	ClaimedAt  time.Time      `db:"claimed_at"`
}

func (WorldBossRewardClaim) TableName() string { return TableWorldBossRewardClaim }
