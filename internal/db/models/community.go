package models

import (
	"encoding/json"
	"time"
)

const (
	TableAdminAction             = "admin_actions"
	TableQuest                   = "quests"
	TableQuestContribution       = "quest_contributions"
	TableQuestClaim              = "quest_claims"
	TableCompetition             = "competitions"
	TableCompetitionContribution = "competition_contributions"
	TableCompetitionResult       = "competition_results"
	TableCompetitionAward        = "competition_awards"
	TableCropGeneticsSetting     = "crop_genetics_settings"
	TableGameEvent               = "game_events"
	TableRankingRewardRule       = "ranking_reward_rules"
	TableRankingSettlement       = "ranking_settlements"
	TableRankingAward            = "ranking_awards"
)

// AdminAction 管理員操作紀錄（建立、取消、結算任務與競賽等）。只新增，不修改或刪除。
//
// 約束：ix_admin_actions_created_at。
type AdminAction struct {
	ID         int64          `db:"id"`
	Actor      string         `db:"actor"`
	Action     string         `db:"action"`
	TargetType string         `db:"target_type"`
	TargetID   *int64         `db:"target_id"`
	Details    map[string]any `db:"details"` // JSON
	CreatedAt  time.Time      `db:"created_at"`
}

func (AdminAction) TableName() string { return TableAdminAction }

// Quest 群組共同任務。目標、期間與獎勵條件建立後不可修改（資料庫觸發器保護）。
//
// 約束：ck_quests_type_valid（harvest／sell／build／animal／defense）、ck_quests_metric_valid（quantity／amount）、
// ck_quests_amount_only_for_sell、ck_quests_filter_only_for_animal、ck_quests_target_positive、
// ck_quests_reward_mode_valid（fixed／proportional）、ck_quests_reward_amount_positive、ck_quests_ends_after_start、
// ck_quests_status_valid（active／completed／cancelled）、ck_quests_progress_non_negative、
// ck_quests_completed_consistency（completed ⇔ completed_at IS NOT NULL）、
// ck_quests_cancelled_consistency（cancelled ⇔ cancelled_at、cancel_reason 都不為 NULL）、ix_quests_group_status。
type Quest struct {
	ID           int64      `db:"id"`
	GroupID      int64      `db:"group_id"`
	Name         string     `db:"name"`
	Description  string     `db:"description"` // server_default ''
	QuestType    string     `db:"quest_type"`
	Metric       string     `db:"metric"`
	ItemFilter   *string    `db:"item_filter"`
	Target       int        `db:"target"`
	RewardMode   string     `db:"reward_mode"`
	RewardType   string     `db:"reward_type"`
	RewardAmount int        `db:"reward_amount"`
	StartsAt     time.Time  `db:"starts_at"`
	EndsAt       time.Time  `db:"ends_at"`
	Status       string     `db:"status"`   // server_default 'active'
	Progress     int        `db:"progress"` // server_default 0
	CompletedAt  *time.Time `db:"completed_at"`
	CancelReason *string    `db:"cancel_reason"`
	CancelledAt  *time.Time `db:"cancelled_at"`
	CreatedBy    string     `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
}

func (Quest) TableName() string { return TableQuest }

// QuestContribution 任務貢獻。
//
// 約束：uq_quest_contributions_once（quest_id, transaction_id：同一筆遊戲操作對同一任務只計算一次）、
// ck_quest_contributions_amount_positive、ix_quest_contributions_quest_player。
type QuestContribution struct {
	ID            int64     `db:"id"`
	QuestID       int64     `db:"quest_id"`
	PlayerID      int64     `db:"player_id"`
	TransactionID int64     `db:"transaction_id"`
	Amount        int       `db:"amount"`
	CreatedAt     time.Time `db:"created_at"`
}

func (QuestContribution) TableName() string { return TableQuestContribution }

// QuestClaim 任務獎勵領取。約束：uq_quest_claims_once（quest_id, player_id）、ck_quest_claims_amount_positive。
type QuestClaim struct {
	ID         int64     `db:"id"`
	QuestID    int64     `db:"quest_id"`
	PlayerID   int64     `db:"player_id"`
	RewardType string    `db:"reward_type"`
	Amount     int       `db:"amount"`
	ClaimedAt  time.Time `db:"claimed_at"`
}

func (QuestClaim) TableName() string { return TableQuestClaim }

// Competition 群組競賽。類型、期間與獎勵建立後不可修改（資料庫觸發器保護）。
//
// 約束：ck_competitions_type_valid（harvest／sales／development／defense）、ck_competitions_ends_after_start、
// ck_competitions_status_valid（active／settled／cancelled）、ck_competitions_settled_consistency、
// ck_competitions_cancelled_consistency、ix_competitions_status_ends。
type Competition struct {
	ID           int64      `db:"id"`
	Name         string     `db:"name"`
	Description  string     `db:"description"` // server_default ''
	CompType     string     `db:"comp_type"`
	Rewards      []int      `db:"rewards"` // JSON 各名次每位成員的金幣獎勵
	StartsAt     time.Time  `db:"starts_at"`
	EndsAt       time.Time  `db:"ends_at"`
	Status       string     `db:"status"` // server_default 'active'
	SettledAt    *time.Time `db:"settled_at"`
	CancelReason *string    `db:"cancel_reason"`
	CancelledAt  *time.Time `db:"cancelled_at"`
	CreatedBy    string     `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
}

func (Competition) TableName() string { return TableCompetition }

// CompetitionContribution 競賽得分。
//
// 約束：uq_competition_contributions_once（competition_id, transaction_id）、ck_competition_contributions_points_positive、
// ix_competition_contributions_comp_group。
type CompetitionContribution struct {
	ID            int64     `db:"id"`
	CompetitionID int64     `db:"competition_id"`
	GroupID       int64     `db:"group_id"`
	PlayerID      int64     `db:"player_id"`
	TransactionID int64     `db:"transaction_id"`
	Points        int       `db:"points"`
	CreatedAt     time.Time `db:"created_at"`
}

func (CompetitionContribution) TableName() string { return TableCompetitionContribution }

// CompetitionResult 結算時的群組排名快照（保存群組名稱，之後成員或名稱變動不影響歷史成績）。
// 約束：主鍵 (competition_id, group_id)。
type CompetitionResult struct {
	CompetitionID int64  `db:"competition_id"`
	GroupID       int64  `db:"group_id"`
	GroupName     string `db:"group_name"`
	Rank          int    `db:"rank"`
	Score         int    `db:"score"`
	Contributors  int    `db:"contributors"`
}

func (CompetitionResult) TableName() string { return TableCompetitionResult }

// CompetitionAward 結算獎勵發放紀錄；每位玩家每場競賽最多一筆。
// 約束：主鍵 (competition_id, player_id)、ck_competition_awards_gold_positive。
type CompetitionAward struct {
	CompetitionID int64     `db:"competition_id"`
	PlayerID      int64     `db:"player_id"`
	GroupID       int64     `db:"group_id"`
	PlayerName    string    `db:"player_name"`
	Rank          int       `db:"rank"`
	Gold          int       `db:"gold"`
	CreatedAt     time.Time `db:"created_at"`
}

func (CompetitionAward) TableName() string { return TableCompetitionAward }

// CropGeneticsSetting 品質與突變設定的版本紀錄；最新版本生效，舊版本保留供稽核（只新增，不修改）。
// 約束：主鍵 version、ck_crop_genetics_settings_version_positive。
type CropGeneticsSetting struct {
	Version   int             `db:"version"`
	Settings  json.RawMessage `db:"settings"` // JSON，以 genetics.ValidateSettings 解析
	CreatedBy string          `db:"created_by"`
	CreatedAt time.Time       `db:"created_at"`
}

func (CropGeneticsSetting) TableName() string { return TableCropGeneticsSetting }

// GameEvent 限時全服加成事件。時間視窗以台灣時間解讀；分鐘數 0–1440，不跨午夜。
//
// 約束：ck_game_events_schedule_type_valid、ck_game_events_once_has_period、ck_game_events_ends_after_start、
// ck_game_events_window_required、ck_game_events_window_range、ck_game_events_weekdays_valid（weekly 時 1–127）、
// ck_game_events_sell_price_pct_range（100–300）、ck_game_events_grow_reduction_pct_range（0–90）、
// ck_game_events_quality_bonus_bp_range（0–3000）、ck_game_events_mutation_bonus_bp_range（0–3000）、
// ck_game_events_harvest_bonus_pct_range（0–200）、ck_game_events_free_water_range（0–1）、
// ck_game_events_harvest_loss_range（0–10）、ix_game_events_crop_type_id。
type GameEvent struct {
	ID          int64  `db:"id"`
	Name        string `db:"name"`
	Description string `db:"description"` // server_default ''
	// 只套用到指定作物（含其珍稀變種）；NULL 表示全部作物
	CropTypeID       *int64     `db:"crop_type_id"`
	SellPricePct     int        `db:"sell_price_pct"`     // server_default 100
	GrowReductionPct int        `db:"grow_reduction_pct"` // server_default 0
	QualityBonusBP   int        `db:"quality_bonus_bp"`   // server_default 0
	MutationBonusBP  int        `db:"mutation_bonus_bp"`  // server_default 0
	HarvestBonusPct  int        `db:"harvest_bonus_pct"`  // server_default 0
	FreeWater        int        `db:"free_water"`         // server_default 0；1 = 種下立即澆水
	HarvestLoss      int        `db:"harvest_loss"`       // server_default 0；收成時每格少 N 個
	ScheduleType     string     `db:"schedule_type"`
	StartsAt         *time.Time `db:"starts_at"`
	EndsAt           *time.Time `db:"ends_at"`
	StartMinute      *int       `db:"start_minute"`
	EndMinute        *int       `db:"end_minute"`
	Weekdays         *int       `db:"weekdays"` // 位元遮罩：週一 = 1、週二 = 2 … 週日 = 64
	Enabled          bool       `db:"enabled"`  // server_default true
	CreatedBy        string     `db:"created_by"`
	CreatedAt        time.Time  `db:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at"`
}

func (GameEvent) TableName() string { return TableGameEvent }

// RankReward 排名獎勵規則中一個名次的獎勵（ranking_reward_rules.rewards 的元素）。
type RankReward struct {
	RewardType string `json:"reward_type"`
	Amount     int    `json:"amount"`
}

// RankingRewardRule 排名獎勵規則。active_since：本次啟用（或變更週期）的時間，只結算在此之後結束的期間，不會回溯補發。
//
// 約束：ck_ranking_reward_rules_scope_valid（group／individual）、ck_ranking_reward_rules_metric_valid（money／sales／goods）、
// ck_ranking_reward_rules_period_valid（daily／weekly／monthly）、ck_ranking_reward_rules_name_len（1–60）。
type RankingRewardRule struct {
	ID          int64        `db:"id"`
	Name        string       `db:"name"`
	Scope       string       `db:"scope"`
	Metric      string       `db:"metric"`
	Period      string       `db:"period"`
	Rewards     []RankReward `db:"rewards"` // JSON 依名次 [{"reward_type", "amount"}]
	Enabled     bool         `db:"enabled"` // server_default true
	ActiveSince time.Time    `db:"active_since"`
	CreatedBy   string       `db:"created_by"`
	CreatedAt   time.Time    `db:"created_at"`
	UpdatedAt   time.Time    `db:"updated_at"`
}

func (RankingRewardRule) TableName() string { return TableRankingRewardRule }

// RankingSettlement 一條規則在一個期間的結算結果（規則與排名快照）；(rule_id, period_start) 唯一，同一期間不會重複發獎。
//
// 約束：uq_ranking_settlements_rule_period（rule_id, period_start）、ck_ranking_settlements_period_valid、
// ix_ranking_settlements_settled_at。
type RankingSettlement struct {
	ID int64 `db:"id"`
	// 規則刪除後保留歷史結算（rule_id 設為 NULL，規則內容保存在 rule_snapshot）
	RuleID       *int64          `db:"rule_id"`
	RuleSnapshot json.RawMessage `db:"rule_snapshot"` // JSON
	PeriodStart  time.Time       `db:"period_start"`
	PeriodEnd    time.Time       `db:"period_end"`
	Ranking      json.RawMessage `db:"ranking"` // JSON 排名快照（list）
	SettledBy    string          `db:"settled_by"`
	SettledAt    time.Time       `db:"settled_at"`
}

func (RankingSettlement) TableName() string { return TableRankingSettlement }

// RankingAward 排名獎勵發放紀錄；每位玩家在同一次結算最多一筆。
//
// 約束：uq_ranking_awards_once（settlement_id, player_id）、ck_ranking_awards_amount_positive、
// ck_ranking_awards_rank_positive、ix_ranking_awards_player。
type RankingAward struct {
	ID           int64     `db:"id"`
	SettlementID int64     `db:"settlement_id"`
	PlayerID     int64     `db:"player_id"`
	GroupID      *int64    `db:"group_id"`
	PlayerName   string    `db:"player_name"`
	Rank         int       `db:"rank"`
	RewardType   string    `db:"reward_type"`
	Amount       int       `db:"amount"`
	CreatedAt    time.Time `db:"created_at"`
}

func (RankingAward) TableName() string { return TableRankingAward }
