package services

// 群組總排行與排名獎勵（每日／每週／每月結算）（對應 Python app/services/ranking_service.py）。
//
// 結算規則：
//   - 只結算「已結束超過緩衝時間」且結束時間晚於規則啟用時間（active_since）的期間；停用期間或修改週期前的期間不回溯補發。
//   - 每個期間在單一交易中完成：鎖定規則、寫入結算快照與每筆獎勵、發放金幣或道具、寫入交易紀錄。
//     (rule_id, period_start) 與 (settlement_id, player_id) 唯一，重複呼叫或並行結算都不會重複發獎。
//   - 群組排名的獎勵發給結算當下「主要所屬群組為該群組且仍是成員」的所有玩家。
//   - 只有分數大於 0 才能得獎。道具獎勵直接放入背包，不受倉庫容量限制（避免自動結算時遺失獎勵）。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/players"

	rankrules "farmgm/internal/game/ranking"
)

const (
	leaderboardLimit = 100
	// SettlementLockKey 背景結算的 PostgreSQL advisory lock 鍵（任意固定值；所有程序相同，與 Python 版共用）
	SettlementLockKey int64 = 0x4641524D_0001
)

// RankingService = CommunityService + 群組總排行與排名獎勵。
type RankingService struct {
	*CommunityService
}

func NewRankingService(d *db.DB, opt Options) *RankingService {
	return &RankingService{NewCommunityService(d, opt)}
}

func (r *RankingService) InGroupContext(groupID *int64) *RankingService {
	return &RankingService{r.CommunityService.InGroupContext(groupID)}
}

// ---------- 回傳資料 ----------

type RuleView struct {
	ID                 int64               `json:"id"`
	Name               string              `json:"name"`
	Scope              string              `json:"scope"`
	ScopeLabel         string              `json:"scope_label"`
	Metric             string              `json:"metric"`
	MetricLabel        string              `json:"metric_label"`
	Period             string              `json:"period"`
	PeriodLabel        string              `json:"period_label"`
	Rewards            []models.RankReward `json:"rewards"`
	RewardsText        string              `json:"rewards_text"`
	Enabled            bool                `json:"enabled"`
	ActiveSince        time.Time           `json:"active_since"`
	CurrentPeriodStart time.Time           `json:"current_period_start"`
	CurrentPeriodEnd   time.Time           `json:"current_period_end"`
	NextSettleAt       time.Time           `json:"next_settle_at"`
	CreatedBy          string              `json:"created_by"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

func rewardText(r models.RankReward) string {
	return rankrules.RewardText(rankrules.Reward{RewardType: r.RewardType, Amount: r.Amount})
}

func NewRuleView(rule models.RankingRewardRule, now time.Time) RuleView {
	start, end, _ := rankrules.PeriodBounds(rule.Period, now)
	scope := "個人"
	if rule.Scope == "group" {
		scope = "群組"
	}
	parts := make([]string, len(rule.Rewards))
	for i, r := range rule.Rewards {
		parts[i] = fmt.Sprintf("第 %d 名 %s", i+1, rewardText(r))
	}
	return RuleView{rule.ID, rule.Name, rule.Scope, scope, rule.Metric, rankrules.Metrics[rule.Metric], rule.Period,
		rankrules.Periods[rule.Period], rule.Rewards, strings.Join(parts, "；"), rule.Enabled, rule.ActiveSince, start, end,
		end.Add(balance.RankingSettleGraceSeconds * time.Second), rule.CreatedBy, rule.UpdatedAt}
}

// ruleSnapshot 結算快照與管理紀錄用的規則內容（Python _rule_audit）。
type ruleSnapshot struct {
	Name    string              `json:"name"`
	Scope   string              `json:"scope"`
	Metric  string              `json:"metric"`
	Period  string              `json:"period"`
	Rewards []models.RankReward `json:"rewards"`
	Enabled bool                `json:"enabled"`
}

func snapshotOf(r models.RankingRewardRule) ruleSnapshot {
	return ruleSnapshot{r.Name, r.Scope, r.Metric, r.Period, r.Rewards, r.Enabled}
}

func toMap(v any) map[string]any {
	data, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

type SettlementView struct {
	ID          int64             `json:"id"`
	RuleID      *int64            `json:"rule_id"`
	RuleName    string            `json:"rule_name"`
	Scope       string            `json:"scope"`
	Metric      string            `json:"metric"`
	Period      string            `json:"period"`
	Summary     string            `json:"summary"`
	PeriodStart time.Time         `json:"period_start"`
	PeriodEnd   time.Time         `json:"period_end"`
	Ranking     json.RawMessage   `json:"ranking"` // 結算當下的排名快照（JSON 陣列）
	SettledBy   string            `json:"settled_by"`
	SettledAt   time.Time         `json:"settled_at"`
	AwardCount  int               `json:"award_count"`
	Awards      []SettlementAward `json:"-"` // 只有 RankingSettlementDetail 會填入（JSON 輸出見 SettlementDetail）
}

// SettlementDetail 結算明細的 JSON（Python settlement_detail：settlement_view 加上 awards；列表沒有 awards 鍵）。
type SettlementDetail struct {
	SettlementView
	Awards []SettlementAward `json:"awards"`
}

// Detail 轉成明細 JSON（awards 為 nil 時輸出 []）。
func (v SettlementView) Detail() SettlementDetail {
	return SettlementDetail{v, nonNil(v.Awards)}
}

type SettlementAward struct {
	PlayerID   int64  `json:"player_id"`
	PlayerName string `json:"player_name"`
	GroupID    *int64 `json:"group_id"`
	Rank       int    `json:"rank"`
	RewardText string `json:"reward_text"`
}

func NewSettlementView(s models.RankingSettlement, awardCount int) SettlementView {
	var snap ruleSnapshot
	_ = json.Unmarshal(s.RuleSnapshot, &snap)
	scope := "個人"
	if snap.Scope == "group" {
		scope = "群組"
	}
	return SettlementView{ID: s.ID, RuleID: s.RuleID, RuleName: snap.Name, Scope: snap.Scope, Metric: snap.Metric,
		Period: snap.Period, Summary: scope + "｜" + rankrules.Metrics[snap.Metric] + "｜" + rankrules.PeriodLabel(snap.Period, s.PeriodStart),
		PeriodStart: s.PeriodStart, PeriodEnd: s.PeriodEnd, Ranking: s.Ranking, SettledBy: s.SettledBy, SettledAt: s.SettledAt,
		AwardCount: awardCount}
}

type PlayerRankingAward struct {
	CreatedAt  time.Time
	RuleName   string
	Period     string
	Rank       int
	RewardText string
}

// ---------- 排行榜（含群組總排行與期間） ----------

// ViewBounds viewPeriod 為 all／day／week／month；all 時回傳 nil, nil。
func ViewBounds(viewPeriod string, now time.Time) (*time.Time, *time.Time, error) {
	if viewPeriod == "all" {
		return nil, nil, nil
	}
	p, ok := rankrules.ViewToPeriod[viewPeriod]
	if !ok {
		return nil, nil, gameerr.Validationf("期間只能是 all、day、week 或 month。")
	}
	s, e, err := rankrules.PeriodBounds(p, now)
	return &s, &e, err
}

func validKind(kind string) error {
	if !slices.Contains(leaderboard.Kinds, kind) {
		return gameerr.Validationf("排行榜種類只能是 money、sales 或 goods。")
	}
	return nil
}

// GroupLeaderboard 群組總排行：各群組成員加總後排名（所有玩家都可查看）。viewPeriod 預設 "all"，limit 預設 10。
func (r *RankingService) GroupLeaderboard(ctx context.Context, kind, viewPeriod string, limit int) ([]leaderboard.GroupRow, error) {
	if err := validKind(kind); err != nil {
		return nil, err
	}
	since, until, err := ViewBounds(viewPeriod, r.now())
	if err != nil {
		return nil, err
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]leaderboard.GroupRow, error) {
		return leaderboard.GroupRanking(ctx, t, kind, max(1, min(limit, leaderboardLimit)), since, until)
	})
}

// PeriodLeaderboard 個人總排行（可指定本日／本週／本月）。limit 預設 10。
func (r *RankingService) PeriodLeaderboard(ctx context.Context, kind, viewPeriod string, limit int) ([]leaderboard.Row, error) {
	if err := validKind(kind); err != nil {
		return nil, err
	}
	since, until, err := ViewBounds(viewPeriod, r.now())
	if err != nil {
		return nil, err
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]leaderboard.Row, error) {
		return leaderboard.Ranking(ctx, t, kind, nil, max(1, min(limit, leaderboardLimit)), since, until)
	})
}

// ---------- 規則管理（由管理 API 呼叫） ----------

func ruleForUpdate(ctx context.Context, t *db.Tx, ruleID int64) (*models.RankingRewardRule, error) {
	rule, err := db.QueryStruct[models.RankingRewardRule](ctx, t, "SELECT * FROM ranking_reward_rules WHERE id = $1 FOR UPDATE", ruleID)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, gameerr.NotFoundf("找不到排名獎勵規則 #%d。", ruleID)
	}
	return rule, nil
}

func (r *RankingService) ListRankingRules(ctx context.Context) ([]RuleView, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]RuleView, error) {
		list, err := db.QueryStructs[models.RankingRewardRule](ctx, t, "SELECT * FROM ranking_reward_rules ORDER BY id")
		if err != nil {
			return nil, err
		}
		out := make([]RuleView, len(list))
		for i, rule := range list {
			out[i] = NewRuleView(rule, now)
		}
		return out, nil
	})
}

func rewardsJSON(rewards []rankrules.Reward) (string, error) {
	data, err := json.Marshal(rewards)
	return string(data), err
}

func (r *RankingService) CreateRankingRule(ctx context.Context, actor string, raw map[string]any) (RuleView, error) {
	v, err := rankrules.ValidateRule(raw)
	if err != nil {
		return RuleView{}, err
	}
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (RuleView, error) {
		rewards, err := rewardsJSON(v.Rewards)
		if err != nil {
			return RuleView{}, err
		}
		rule, err := db.QueryStruct[models.RankingRewardRule](ctx, t, `INSERT INTO ranking_reward_rules
			(name, scope, metric, period, rewards, enabled, active_since, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5::json, $6, $7, $8, $7, $7) RETURNING *`,
			v.Name, v.Scope, v.Metric, v.Period, rewards, v.Enabled, now, actor)
		if err != nil {
			return RuleView{}, err
		}
		return NewRuleView(*rule, now), community.AddAdminAction(ctx, t, actor, "create_ranking_rule", "ranking_rule", &rule.ID,
			toMap(snapshotOf(*rule)), now)
	})
}

// UpdateRankingRule 修改後的獎勵用於之後結算的期間；變更週期或從停用改為啟用時，從現在起算，不回溯補發。
func (r *RankingService) UpdateRankingRule(ctx context.Context, actor string, ruleID int64, raw map[string]any) (RuleView, error) {
	v, err := rankrules.ValidateRule(raw)
	if err != nil {
		return RuleView{}, err
	}
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (RuleView, error) {
		rule, err := ruleForUpdate(ctx, t, ruleID)
		if err != nil {
			return RuleView{}, err
		}
		before := snapshotOf(*rule)
		if v.Period != rule.Period || (v.Enabled && !rule.Enabled) {
			rule.ActiveSince = now
		}
		rule.Name, rule.Scope, rule.Metric, rule.Period, rule.Enabled = v.Name, v.Scope, v.Metric, v.Period, v.Enabled
		rule.Rewards = make([]models.RankReward, len(v.Rewards))
		for i, x := range v.Rewards {
			rule.Rewards[i] = models.RankReward{RewardType: x.RewardType, Amount: x.Amount}
		}
		rule.UpdatedAt = now
		rewards, err := rewardsJSON(v.Rewards)
		if err != nil {
			return RuleView{}, err
		}
		if _, err := t.Exec(ctx, `UPDATE ranking_reward_rules SET name = $2, scope = $3, metric = $4, period = $5,
			rewards = $6::json, enabled = $7, active_since = $8, updated_at = $9 WHERE id = $1`,
			rule.ID, rule.Name, rule.Scope, rule.Metric, rule.Period, rewards, rule.Enabled, rule.ActiveSince, now); err != nil {
			return RuleView{}, err
		}
		return NewRuleView(*rule, now), community.AddAdminAction(ctx, t, actor, "update_ranking_rule", "ranking_rule", &rule.ID,
			map[string]any{"before": toMap(before), "after": toMap(snapshotOf(*rule))}, now)
	})
}

func (r *RankingService) SetRankingRuleEnabled(ctx context.Context, actor string, ruleID int64, enabled bool) (RuleView, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (RuleView, error) {
		rule, err := ruleForUpdate(ctx, t, ruleID)
		if err != nil {
			return RuleView{}, err
		}
		if rule.Enabled != enabled {
			rule.Enabled, rule.UpdatedAt = enabled, now
			if enabled {
				rule.ActiveSince = now // 停用期間的期間不補發
			}
			if _, err := db.UpdateByID(ctx, t, "ranking_reward_rules", rule.ID, db.C("enabled", enabled),
				db.C("updated_at", now), db.C("active_since", rule.ActiveSince)); err != nil {
				return RuleView{}, err
			}
			action := "disable_ranking_rule"
			if enabled {
				action = "enable_ranking_rule"
			}
			if err := community.AddAdminAction(ctx, t, actor, action, "ranking_rule", &rule.ID,
				map[string]any{"name": rule.Name}, now); err != nil {
				return RuleView{}, err
			}
		}
		return NewRuleView(*rule, now), nil
	})
}

// DeleteRankingRule 刪除規則；已結算的歷史與發獎紀錄保留（以規則快照顯示）。回傳刪除的規則 ID。
func (r *RankingService) DeleteRankingRule(ctx context.Context, actor string, ruleID int64) (int64, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (int64, error) {
		rule, err := ruleForUpdate(ctx, t, ruleID)
		if err != nil {
			return 0, err
		}
		if _, err := t.Exec(ctx, "DELETE FROM ranking_reward_rules WHERE id = $1", ruleID); err != nil {
			return 0, err
		}
		return ruleID, community.AddAdminAction(ctx, t, actor, "delete_ranking_rule", "ranking_rule", &ruleID,
			toMap(snapshotOf(*rule)), now)
	})
}

// ---------- 結算 ----------

type period struct{ start, end time.Time }

// duePeriods 規則啟用後已結束（且過了緩衝時間）但尚未結算的期間，由舊到新。settled 以 UnixMicro 為鍵。
func duePeriods(rule models.RankingRewardRule, cutoff time.Time, settled map[int64]bool) []period {
	var out []period
	start, end, err := rankrules.PeriodBounds(rule.Period, rule.ActiveSince)
	if err != nil {
		return nil
	}
	for range balance.RankingMaxCatchupPeriods {
		if end.After(cutoff) {
			break
		}
		if !settled[start.UnixMicro()] && end.After(rule.ActiveSince) {
			out = append(out, period{start, end})
		}
		start, end, _ = rankrules.PeriodBounds(rule.Period, end)
	}
	return out
}

// BackgroundSettlement 背景結算的結果。
type BackgroundSettlement struct {
	Rankings     []int64
	Competitions []int64
}

// RunBackgroundSettlement 背景結算排名獎勵與競賽。以 session 層級 advisory lock 確保同一時間只有一個程序在結算
// （與 Python 版共用同一個鍵）；取不到鎖時回傳 nil（另一個程序正在結算）。即使沒有鎖，唯一限制也保證不會重複發獎。
func (r *RankingService) RunBackgroundSettlement(ctx context.Context, actor string) (*BackgroundSettlement, error) {
	// 使用獨立的連線持有鎖；程序中斷或連線斷開時 PostgreSQL 會自動釋放鎖
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", SettlementLockKey).Scan(&got); err != nil {
		return nil, err
	}
	if !got {
		return nil, nil
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", SettlementLockKey)
	}()
	rankings, err := r.SettleDueRankings(ctx, actor)
	if err != nil {
		return nil, err
	}
	comps, err := r.SettleDueCompetitions(ctx, actor)
	if err != nil {
		return nil, err
	}
	return &BackgroundSettlement{rankings, comps}, nil
}

type planItem struct {
	ruleID int64
	p      period
}

// SettleDueRankings 結算所有到期的期間，回傳新建立的結算 ID；可重複呼叫，不會重複發獎。actor 預設 "system"。
func (r *RankingService) SettleDueRankings(ctx context.Context, actor string) ([]int64, error) {
	cutoff := r.now().Add(-balance.RankingSettleGraceSeconds * time.Second)
	plan, err := inTx(ctx, r.GameService, func(t *db.Tx) ([]planItem, error) {
		list, err := db.QueryStructs[models.RankingRewardRule](ctx, t, "SELECT * FROM ranking_reward_rules WHERE enabled ORDER BY id")
		if err != nil {
			return nil, err
		}
		var plan []planItem
		for _, rule := range list {
			starts, err := db.QueryValues[time.Time](ctx, t, "SELECT period_start FROM ranking_settlements WHERE rule_id = $1", rule.ID)
			if err != nil {
				return nil, err
			}
			settled := map[int64]bool{}
			for _, s := range starts {
				settled[s.UnixMicro()] = true
			}
			for _, p := range duePeriods(rule, cutoff, settled) {
				plan = append(plan, planItem{rule.ID, p})
			}
		}
		return plan, nil
	})
	if err != nil {
		return nil, err
	}
	created := []int64{}
	for _, it := range plan {
		id, err := r.settlePeriod(ctx, it.ruleID, it.p, actor)
		if err != nil {
			return nil, err
		}
		if id != nil {
			created = append(created, *id)
		}
	}
	return created, nil
}

// rankEntry 排名中的一列（個人或群組）。
type rankEntry struct {
	Rank        int
	Score       int
	GroupID     *int64
	PlayerID    int64
	PlayerName  string
	HomeGroupID *int64
}

func (r *RankingService) settlePeriod(ctx context.Context, ruleID int64, p period, actor string) (*int64, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (*int64, error) {
		rule, err := db.QueryStruct[models.RankingRewardRule](ctx, t, "SELECT * FROM ranking_reward_rules WHERE id = $1 FOR UPDATE", ruleID)
		if err != nil || rule == nil || !rule.Enabled {
			return nil, err
		}
		_, exists, err := db.QueryValue[int64](ctx, t, "SELECT id FROM ranking_settlements WHERE rule_id = $1 AND period_start = $2",
			ruleID, p.start)
		if err != nil || exists {
			return nil, err
		}
		limit := max(len(rule.Rewards), 10)
		var snapshot any
		var entries []rankEntry
		if rule.Scope == "group" {
			rows, err := leaderboard.GroupRanking(ctx, t, rule.Metric, limit, &p.start, &p.end)
			if err != nil {
				return nil, err
			}
			snapshot = rows
			for _, x := range rows {
				gid := x.GroupID
				entries = append(entries, rankEntry{Rank: x.Rank, Score: x.Score, GroupID: &gid})
			}
		} else {
			rows, err := leaderboard.Ranking(ctx, t, rule.Metric, nil, limit, &p.start, &p.end)
			if err != nil {
				return nil, err
			}
			snapshot = rows
			for _, x := range rows {
				entries = append(entries, rankEntry{Rank: x.Rank, Score: x.Score, PlayerID: x.PlayerID, PlayerName: x.PlayerName,
					HomeGroupID: x.HomeGroupID})
			}
		}
		if len(entries) == 0 {
			snapshot = []any{} // 空排名寫成 []，不是 null
		}
		rankingJSON, err := json.Marshal(snapshot)
		if err != nil {
			return nil, err
		}
		snapJSON, err := json.Marshal(snapshotOf(*rule))
		if err != nil {
			return nil, err
		}
		var settlementID int64
		if err := t.QueryRow(ctx, `INSERT INTO ranking_settlements (rule_id, rule_snapshot, period_start, period_end, ranking,
			settled_by, settled_at) VALUES ($1, $2::json, $3, $4, $5::json, $6, $7) RETURNING id`,
			rule.ID, string(snapJSON), p.start, p.end, string(rankingJSON), actor, now).Scan(&settlementID); err != nil {
			return nil, err
		}
		label := fmt.Sprintf("%s（%s）", rule.Name, rankrules.PeriodLabel(rule.Period, p.start))
		paid := 0
		// 分數由高到低排序，只有分數 > 0 的前幾名得獎
		for _, row := range entries[:min(len(entries), len(rule.Rewards))] {
			if row.Score <= 0 {
				break
			}
			reward := rule.Rewards[row.Rank-1]
			type recipient struct {
				farm *models.Farm
				name string
			}
			var recipients []recipient
			var groupID, ctxGroupID *int64
			if rule.Scope == "group" {
				list, err := community.AwardRecipients(ctx, t, *row.GroupID)
				if err != nil {
					return nil, err
				}
				for i := range list {
					recipients = append(recipients, recipient{&list[i].Farm, list[i].PlayerName})
				}
				groupID, ctxGroupID = row.GroupID, row.GroupID
			} else {
				farm, err := farms.GetByPlayer(ctx, t, row.PlayerID, true)
				if err != nil {
					return nil, err
				}
				if farm != nil {
					recipients = append(recipients, recipient{farm, row.PlayerName})
				}
				groupID = row.HomeGroupID
			}
			for _, rc := range recipients {
				if err := r.pay(ctx, t, settlementID, rc.farm, rc.name, groupID, ctxGroupID, row.Rank, reward, label, now); err != nil {
					return nil, err
				}
				paid++
			}
		}
		if err := community.AddAdminAction(ctx, t, actor, "settle_ranking", "ranking_settlement", &settlementID,
			map[string]any{"rule_id": rule.ID, "period_start": p.start.Format("2006-01-02T15:04:05-07:00"), "awards": paid}, now); err != nil {
			return nil, err
		}
		return &settlementID, nil
	})
}

func (r *RankingService) pay(ctx context.Context, t *db.Tx, settlementID int64, farm *models.Farm, playerName string,
	groupID, contextGroup *int64, rank int, reward models.RankReward, label string, now time.Time) error {
	if err := db.Insert(ctx, t, "ranking_awards", db.C("settlement_id", settlementID), db.C("player_id", farm.PlayerID),
		db.C("group_id", groupID), db.C("player_name", playerName), db.C("rank", rank), db.C("reward_type", reward.RewardType),
		db.C("amount", reward.Amount), db.C("created_at", now)); err != nil {
		return err
	}
	rc := rec{SetContext: true, ContextGroupID: contextGroup}
	if reward.RewardType == "gold" {
		farm.Gold += reward.Amount
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return err
		}
		rc.GoldDelta = reward.Amount
	} else {
		if err := inventory.Add(ctx, t, farm.ID, reward.RewardType, reward.Amount, "normal", "none"); err != nil {
			return err
		}
		rc.ItemCode, rc.Quantity = reward.RewardType, reward.Amount
	}
	return r.record(ctx, t, farm, rules.EvRankingReward, fmt.Sprintf("排名獎勵：%s第 %d 名，獲得 %s", label, rank, rewardText(reward)), rc)
}

// ---------- 查詢結算紀錄 ----------

// RankingSettlements limit 預設 30（1–200）。
func (r *RankingService) RankingSettlements(ctx context.Context, limit int) ([]SettlementView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]SettlementView, error) {
		return listSettlements(ctx, t, max(1, min(limit, 200)))
	})
}

// listSettlements 最近的結算（監控網站與服務共用）。
func listSettlements(ctx context.Context, t *db.Tx, limit int) ([]SettlementView, error) {
	counts := map[int64]int{}
	rows, err := t.Query(ctx, "SELECT settlement_id, count(*) FROM ranking_awards GROUP BY settlement_id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		counts[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list, err := db.QueryStructs[models.RankingSettlement](ctx, t,
		"SELECT * FROM ranking_settlements ORDER BY settled_at DESC, id DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	out := make([]SettlementView, len(list))
	for i, s := range list {
		out[i] = NewSettlementView(s, counts[s.ID])
	}
	return out, nil
}

func (r *RankingService) RankingSettlementDetail(ctx context.Context, settlementID int64) (SettlementView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (SettlementView, error) {
		v, err := settlementDetail(ctx, t, settlementID)
		if err != nil {
			return SettlementView{}, err
		}
		if v == nil {
			return SettlementView{}, gameerr.NotFoundf("找不到結算 #%d。", settlementID)
		}
		return *v, nil
	})
}

// settlementDetail 結算與每筆獎勵（Python settlement_detail）；找不到時回傳 nil。
func settlementDetail(ctx context.Context, t *db.Tx, settlementID int64) (*SettlementView, error) {
	s, err := db.QueryStruct[models.RankingSettlement](ctx, t, "SELECT * FROM ranking_settlements WHERE id = $1", settlementID)
	if err != nil || s == nil {
		return nil, err
	}
	awards, err := db.QueryStructs[models.RankingAward](ctx, t,
		"SELECT * FROM ranking_awards WHERE settlement_id = $1 ORDER BY rank, player_id", settlementID)
	if err != nil {
		return nil, err
	}
	v := NewSettlementView(*s, len(awards))
	v.Awards = []SettlementAward{}
	for _, a := range awards {
		v.Awards = append(v.Awards, SettlementAward{a.PlayerID, a.PlayerName, a.GroupID, a.Rank,
			rewardText(models.RankReward{RewardType: a.RewardType, Amount: a.Amount})})
	}
	return &v, nil
}

// PlayerRankingAwards limit 預設 10。
func (r *RankingService) PlayerRankingAwards(ctx context.Context, playerID int64, limit int) ([]PlayerRankingAward, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]PlayerRankingAward, error) {
		p, err := players.Get(ctx, t, playerID, false)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, gameerr.NotFoundf("找不到玩家 #%d。", playerID)
		}
		type row struct {
			models.RankingAward
			Snapshot    json.RawMessage `db:"rule_snapshot"`
			PeriodStart time.Time       `db:"period_start"`
		}
		rows, err := db.QueryStructs[row](ctx, t, `SELECT a.*, s.rule_snapshot, s.period_start FROM ranking_awards a
			JOIN ranking_settlements s ON s.id = a.settlement_id WHERE a.player_id = $1
			ORDER BY a.created_at DESC, a.id DESC LIMIT $2`, playerID, limit)
		if err != nil {
			return nil, err
		}
		out := []PlayerRankingAward{}
		for _, x := range rows {
			var snap ruleSnapshot
			_ = json.Unmarshal(x.Snapshot, &snap)
			out = append(out, PlayerRankingAward{x.CreatedAt, snap.Name, rankrules.PeriodLabel(snap.Period, x.PeriodStart), x.Rank,
				rewardText(models.RankReward{RewardType: x.RewardType, Amount: x.Amount})})
		}
		return out, nil
	})
}
