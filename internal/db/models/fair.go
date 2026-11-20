package models

import "time"

// 品評會（Go 版新增，Migration 0048，2026-11-20）。
const (
	TableFairEntry = "fair_entries"
	TableFairVote  = "fair_votes"
)

// FairEntry 一件參賽作品（每位玩家每個群組每週一件）。week_start 是台灣週一（讀回是 UTC 00:00）。
//
// 約束：uq_fair_entries_group_week_player（group_id, week_start, player_id）、
// ck_fair_entries_category_valid（crop／goods／lab）、ck_fair_entries_status_valid（active／settled／refunded）、
// ck_fair_entries_values_non_negative（base_value、judge_score、votes、fee、reward 都 >= 0）、ck_fair_entries_rank_positive（NULL 或 >= 1）；
// ix_fair_entries_group_week、ix_fair_entries_status_week。
type FairEntry struct {
	ID         int64      `db:"id"`
	GroupID    int64      `db:"group_id"`
	WeekStart  time.Time  `db:"week_start"`
	PlayerID   int64      `db:"player_id"`
	FarmID     int64      `db:"farm_id"`
	Category   string     `db:"category"`
	ItemCode   string     `db:"item_code"`
	ItemName   string     `db:"item_name"` // 報名時的名稱快照
	Quality    string     `db:"quality"`
	Mutation   string     `db:"mutation"`
	BaseValue  int        `db:"base_value"`
	JudgeScore int        `db:"judge_score"`
	Votes      int        `db:"votes"`
	Fee        int        `db:"fee"`
	Rank       *int       `db:"rank"`
	Reward     int        `db:"reward"`
	Status     string     `db:"status"`
	CreatedAt  time.Time  `db:"created_at"`
	SettledAt  *time.Time `db:"settled_at"`
}

func (FairEntry) TableName() string { return TableFairEntry }

// FairVote 一張人氣票。約束：uq_fair_votes_entry_voter（entry_id, voter_player_id）；ix_fair_votes_voter_week。
type FairVote struct {
	ID            int64     `db:"id"`
	EntryID       int64     `db:"entry_id"`
	GroupID       int64     `db:"group_id"`
	WeekStart     time.Time `db:"week_start"`
	VoterPlayerID int64     `db:"voter_player_id"`
	CreatedAt     time.Time `db:"created_at"`
}

func (FairVote) TableName() string { return TableFairVote }
