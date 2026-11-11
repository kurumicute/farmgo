package services

// 群組共同任務與群組競賽（對應 Python app/services/community_service.py）。
//
//   - 任務與競賽只能由管理員建立／取消／結算，所有管理操作都寫入 admin_actions。
//   - 玩家操作任務時必須明確傳入群組，且必須是成員。
//   - 計分由 activity.go 在遊戲操作的同一筆交易中完成。
//   - 競賽在截止並超過緩衝時間後結算：保存群組排名快照並發放獎勵，結算只會發生一次。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
)

const (
	communityNameMax = 60
	communityDescMax = 300
	reasonMin        = 2
	reasonMax        = 200
)

// CommunityService = SocialService + 任務與競賽。
type CommunityService struct {
	*SocialService
}

func NewCommunityService(d *db.DB, opt Options) *CommunityService {
	return &CommunityService{NewSocialService(d, opt)}
}

func (c *CommunityService) InGroupContext(groupID *int64) *CommunityService {
	return &CommunityService{c.SocialService.InGroupContext(groupID)}
}

func RewardLabel(rewardType string, amount int) string {
	if rewardType == "gold" {
		return fmt.Sprintf("%d 金幣", amount)
	}
	return fmt.Sprintf("%d 個%s", amount, ItemName(rewardType, nil))
}

// QuestView 任務（Python quest_view 的 dict）。
type QuestView struct {
	QuestID      int64      `json:"quest_id"`
	GroupID      int64      `json:"group_id"`
	GroupName    string     `json:"group_name"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	QuestType    string     `json:"quest_type"`
	TypeName     string     `json:"type_name"`
	Metric       string     `json:"metric"`
	ItemFilter   *string    `json:"item_filter"`
	GoalText     string     `json:"goal_text"`
	Target       int        `json:"target"`
	Progress     int        `json:"progress"`
	Percent      int        `json:"percent"`
	State        string     `json:"state"`
	StateLabel   string     `json:"state_label"`
	RewardMode   string     `json:"reward_mode"`
	RewardType   string     `json:"reward_type"`
	RewardAmount int        `json:"reward_amount"`
	RewardText   string     `json:"reward_text"`
	StartsAt     time.Time  `json:"starts_at"`
	EndsAt       time.Time  `json:"ends_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CancelReason *string    `json:"cancel_reason"`
	CancelledAt  *time.Time `json:"cancelled_at"`
	CreatedBy    string     `json:"created_by"`

	// QuestStatus／PlayerQuests 另外填入
	Contributors   []community.Contributor `json:"-"`
	MyContribution int                     `json:"-"`
	MyClaim        *models.QuestClaim      `json:"-"`
	MyReward       int                     `json:"-"`
}

func NewQuestView(q models.Quest, groupName string, now time.Time, names map[string]string) QuestView {
	state := rules.EventState(q.Status, q.StartsAt, q.EndsAt, now)
	typeName := balance.QuestTypes[q.QuestType].Name
	unit := "個"
	switch {
	case q.Metric == "amount":
		unit = "金幣"
	case q.QuestType == "build" || q.QuestType == "defense":
		unit = "次"
	}
	goal := ""
	if q.ItemFilter != nil && *q.ItemFilter != "" {
		goal = "（" + ItemName(*q.ItemFilter, names) + "）"
	}
	reward := balance.QuestRewardModes[q.RewardMode] + "：" + RewardLabel(q.RewardType, q.RewardAmount)
	if q.RewardMode == "proportional" {
		reward += "（獎池）"
	} else {
		reward += "／人"
	}
	return QuestView{QuestID: q.ID, GroupID: q.GroupID, GroupName: groupName, Name: q.Name, Description: q.Description,
		QuestType: q.QuestType, TypeName: typeName, Metric: q.Metric, ItemFilter: q.ItemFilter,
		GoalText: fmt.Sprintf("%s%s 達到 %d %s", typeName, goal, q.Target, unit), Target: q.Target, Progress: q.Progress,
		Percent: min(100, q.Progress*100/q.Target), State: state, StateLabel: rules.EventStateLabels[state],
		RewardMode: q.RewardMode, RewardType: q.RewardType, RewardAmount: q.RewardAmount, RewardText: reward,
		StartsAt: q.StartsAt, EndsAt: q.EndsAt, CompletedAt: q.CompletedAt, CancelReason: q.CancelReason,
		CancelledAt: q.CancelledAt, CreatedBy: q.CreatedBy}
}

// CompetitionView 競賽（Python competition_view 的 dict）。
type CompetitionView struct {
	CompetitionID int64      `json:"competition_id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	CompType      string     `json:"comp_type"`
	TypeName      string     `json:"type_name"`
	ScoringRule   string     `json:"scoring_rule"`
	Rewards       []int      `json:"rewards"`
	StartsAt      time.Time  `json:"starts_at"`
	EndsAt        time.Time  `json:"ends_at"`
	State         string     `json:"state"`
	StateLabel    string     `json:"state_label"`
	SettledAt     *time.Time `json:"settled_at"`
	CancelReason  *string    `json:"cancel_reason"`
	CancelledAt   *time.Time `json:"cancelled_at"`
	CreatedBy     string     `json:"created_by"`
}

func NewCompetitionView(c models.Competition, now time.Time) CompetitionView {
	state := rules.EventState(c.Status, c.StartsAt, c.EndsAt, now)
	label := rules.EventStateLabels[state]
	if state == "expired" {
		label = "待結算"
	}
	t := balance.CompetitionTypes[c.CompType]
	return CompetitionView{c.ID, c.Name, c.Description, c.CompType, t.Name, t.Desc, append([]int{}, c.Rewards...),
		c.StartsAt, c.EndsAt, state, label, c.SettledAt, c.CancelReason, c.CancelledAt, c.CreatedBy}
}

type CompetitionAwardView struct {
	PlayerID   int64
	PlayerName string
	GroupID    int64
	Rank       int
	Gold       int
}

// CompetitionDetail 競賽詳情；指定玩家時另外填入 My* 欄位。
type CompetitionDetail struct {
	CompetitionView
	Final         bool
	Ranking       []community.RankingRow
	TopPlayers    []community.PlayerPoints
	Awards        []CompetitionAwardView
	MyPoints      int
	MyHomeGroupID *int64
	MyAward       *CompetitionAwardView
}

// pyDatetime Python str(datetime)：「2026-01-01 00:00:00+00:00」（有微秒時加上 .ffffff）。
func pyDatetime(t time.Time) string {
	layout := "2006-01-02 15:04:05"
	if t.Nanosecond()/1000 != 0 {
		layout += ".000000"
	}
	return t.Format(layout) + t.Format("-07:00")
}

// viewDetails 管理紀錄的 details：view 的欄位，時間轉成 Python str(datetime)（Python {k: str(v) if datetime}）。
func viewDetails(v any, times map[string]*time.Time) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	for k, t := range times {
		if t != nil {
			out[k] = pyDatetime(*t)
		}
	}
	return out, nil
}

func validateText(value, label string, maxLen int, required bool) (string, error) {
	text := strings.TrimSpace(value)
	if required && text == "" {
		return "", gameerr.Validationf("%s不可為空白。", label)
	}
	if utf8.RuneCountInString(text) > maxLen {
		return "", gameerr.Validationf("%s最多 %d 個字元。", label, maxLen)
	}
	for _, ch := range text {
		if ch < 32 && ch != '\n' {
			return "", gameerr.Validationf("%s不可包含控制字元。", label)
		}
	}
	return text, nil
}

func validatePeriod(startsAt, endsAt, now time.Time) error {
	if !endsAt.After(startsAt) {
		return gameerr.Validationf("截止時間必須晚於開始時間。")
	}
	if !endsAt.After(now) {
		return gameerr.Validationf("截止時間必須在未來。")
	}
	if endsAt.Sub(startsAt) > time.Duration(balance.EventMaxDays)*24*time.Hour {
		return gameerr.Validationf("期間最長 %d 天。", balance.EventMaxDays)
	}
	return nil
}

func validateReason(reason string) (string, error) {
	reason, err := validateText(reason, "取消原因", reasonMax, true)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(reason) < reasonMin {
		return "", gameerr.Validationf("取消原因至少 %d 個字。", reasonMin)
	}
	return reason, nil
}

// ---------- 管理：任務 ----------

// NewQuest 建立任務的參數（Python create_quest 的 keyword 參數）。Metric 為 "" 時使用 quantity；ItemFilter 為 nil 表示不限產品。
type NewQuest struct {
	GroupID      int64
	Name         string
	QuestType    string
	Target       int
	RewardMode   string
	RewardType   string
	RewardAmount int
	StartsAt     time.Time
	EndsAt       time.Time
	Description  string
	Metric       string
	ItemFilter   *string
}

func (c *CommunityService) CreateQuest(ctx context.Context, actor string, q NewQuest) (QuestView, error) {
	now := c.now()
	name, err := validateText(q.Name, "任務名稱", communityNameMax, true)
	if err != nil {
		return QuestView{}, err
	}
	desc, err := validateText(q.Description, "任務說明", communityDescMax, false)
	if err != nil {
		return QuestView{}, err
	}
	if q.Metric == "" {
		q.Metric = "quantity"
	}
	qt, ok := balance.QuestTypes[q.QuestType]
	if !ok {
		return QuestView{}, gameerr.Validationf("任務類型不正確。")
	}
	if !slices.Contains(qt.Metrics, q.Metric) {
		return QuestView{}, gameerr.Validationf("此任務類型不支援該計量方式（只有共同販售可依金額計算）。")
	}
	if q.ItemFilter != nil && q.QuestType != "animal" {
		return QuestView{}, gameerr.Validationf("只有動物生產任務可指定產品。")
	}
	if q.Target < 1 || q.Target > balance.QuestMaxTarget {
		return QuestView{}, gameerr.Validationf("目標必須在 1–%d 之間。", balance.QuestMaxTarget)
	}
	if _, ok := balance.QuestRewardModes[q.RewardMode]; !ok {
		return QuestView{}, gameerr.Validationf("獎勵模式不正確。")
	}
	if !slices.Contains(balance.QuestRewardTypes, q.RewardType) {
		return QuestView{}, gameerr.Validationf("獎勵類型只能是金幣、化肥、蚯蚓或巡查券。")
	}
	limit := balance.QuestMaxItemReward
	if q.RewardType == "gold" {
		limit = balance.QuestMaxGoldReward
	}
	if q.RewardAmount < 1 || q.RewardAmount > limit {
		return QuestView{}, gameerr.Validationf("獎勵數量必須在 1–%d 之間。", limit)
	}
	if err := validatePeriod(q.StartsAt, q.EndsAt, now); err != nil {
		return QuestView{}, err
	}
	return inTx(ctx, c.GameService, func(t *db.Tx) (QuestView, error) {
		g, err := groups.Get(ctx, t, q.GroupID)
		if err != nil {
			return QuestView{}, err
		}
		if g == nil {
			return QuestView{}, gameerr.NotFoundf("找不到群組 #%d。", q.GroupID)
		}
		filter := q.ItemFilter
		if filter != nil {
			product, err := animals.FindProduct(ctx, t, *filter)
			if err != nil {
				return QuestView{}, err
			}
			if product == nil {
				return QuestView{}, gameerr.Validationf("指定的動物產品不存在。")
			}
			filter = &product.ProductCode
		}
		row, err := db.QueryStruct[models.Quest](ctx, t, `INSERT INTO quests (group_id, name, description, quest_type, metric,
			item_filter, target, reward_mode, reward_type, reward_amount, starts_at, ends_at, status, progress, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'active', 0, $13, $14) RETURNING *`,
			q.GroupID, name, desc, q.QuestType, q.Metric, filter, q.Target, q.RewardMode, q.RewardType, q.RewardAmount,
			q.StartsAt, q.EndsAt, actor, now)
		if err != nil {
			return QuestView{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return QuestView{}, err
		}
		view := NewQuestView(*row, g.Name, now, names)
		details, err := viewDetails(view, map[string]*time.Time{"starts_at": &view.StartsAt, "ends_at": &view.EndsAt,
			"completed_at": view.CompletedAt, "cancelled_at": view.CancelledAt})
		if err != nil {
			return QuestView{}, err
		}
		return view, community.AddAdminAction(ctx, t, actor, "create_quest", "quest", &row.ID, details, now)
	})
}

func (c *CommunityService) CancelQuest(ctx context.Context, actor string, questID int64, reason string) (QuestView, error) {
	reason, err := validateReason(reason)
	if err != nil {
		return QuestView{}, err
	}
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (QuestView, error) {
		q, err := community.GetQuest(ctx, t, questID, true)
		if err != nil {
			return QuestView{}, err
		}
		if q == nil {
			return QuestView{}, gameerr.NotFoundf("找不到任務 #%d。", questID)
		}
		if q.Status != "active" {
			return QuestView{}, gameerr.Validationf("任務 #%d 目前為「%s」，不能取消。", questID, rules.EventStateLabels[q.Status])
		}
		q.Status, q.CancelledAt, q.CancelReason = "cancelled", &now, &reason
		if _, err := db.UpdateByID(ctx, t, "quests", q.ID, db.C("status", q.Status), db.C("cancelled_at", now),
			db.C("cancel_reason", reason)); err != nil {
			return QuestView{}, err
		}
		if err := community.AddAdminAction(ctx, t, actor, "cancel_quest", "quest", &questID,
			map[string]any{"reason": reason, "progress": q.Progress}, now); err != nil {
			return QuestView{}, err
		}
		g, err := groups.Get(ctx, t, q.GroupID)
		if err != nil {
			return QuestView{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		return NewQuestView(*q, g.Name, now, names), err
	})
}

// ---------- 管理：競賽 ----------

// NewCompetition 建立競賽的參數。
type NewCompetition struct {
	Name        string
	CompType    string
	Rewards     []int
	StartsAt    time.Time
	EndsAt      time.Time
	Description string
}

func (c *CommunityService) CreateCompetition(ctx context.Context, actor string, v NewCompetition) (CompetitionView, error) {
	now := c.now()
	name, err := validateText(v.Name, "競賽名稱", communityNameMax, true)
	if err != nil {
		return CompetitionView{}, err
	}
	desc, err := validateText(v.Description, "競賽說明", communityDescMax, false)
	if err != nil {
		return CompetitionView{}, err
	}
	if _, ok := balance.CompetitionTypes[v.CompType]; !ok {
		return CompetitionView{}, gameerr.Validationf("競賽類型不正確。")
	}
	if len(v.Rewards) < 1 || len(v.Rewards) > balance.CompetitionRewardRanks {
		return CompetitionView{}, gameerr.Validationf("請設定 1–%d 個名次的獎勵。", balance.CompetitionRewardRanks)
	}
	for _, r := range v.Rewards {
		if r < 0 || r > balance.CompetitionMaxReward {
			return CompetitionView{}, gameerr.Validationf("每個名次的獎勵必須在 0–%d 金幣之間。", balance.CompetitionMaxReward)
		}
	}
	if err := validatePeriod(v.StartsAt, v.EndsAt, now); err != nil {
		return CompetitionView{}, err
	}
	return inTx(ctx, c.GameService, func(t *db.Tx) (CompetitionView, error) {
		rewards, err := json.Marshal(v.Rewards)
		if err != nil {
			return CompetitionView{}, err
		}
		row, err := db.QueryStruct[models.Competition](ctx, t, `INSERT INTO competitions (name, description, comp_type, rewards,
			starts_at, ends_at, status, created_by, created_at) VALUES ($1, $2, $3, $4::json, $5, $6, 'active', $7, $8) RETURNING *`,
			name, desc, v.CompType, string(rewards), v.StartsAt, v.EndsAt, actor, now)
		if err != nil {
			return CompetitionView{}, err
		}
		view := NewCompetitionView(*row, now)
		details, err := viewDetails(view, map[string]*time.Time{"starts_at": &view.StartsAt, "ends_at": &view.EndsAt,
			"settled_at": view.SettledAt, "cancelled_at": view.CancelledAt})
		if err != nil {
			return CompetitionView{}, err
		}
		return view, community.AddAdminAction(ctx, t, actor, "create_competition", "competition", &row.ID, details, now)
	})
}

func (c *CommunityService) CancelCompetition(ctx context.Context, actor string, competitionID int64, reason string) (CompetitionView, error) {
	reason, err := validateReason(reason)
	if err != nil {
		return CompetitionView{}, err
	}
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (CompetitionView, error) {
		comp, err := community.GetCompetition(ctx, t, competitionID, true)
		if err != nil {
			return CompetitionView{}, err
		}
		if comp == nil {
			return CompetitionView{}, gameerr.NotFoundf("找不到競賽 #%d。", competitionID)
		}
		if comp.Status != "active" {
			return CompetitionView{}, gameerr.Validationf("競賽 #%d 目前為「%s」，不能取消。", competitionID, rules.EventStateLabels[comp.Status])
		}
		comp.Status, comp.CancelledAt, comp.CancelReason = "cancelled", &now, &reason
		if _, err := db.UpdateByID(ctx, t, "competitions", comp.ID, db.C("status", comp.Status), db.C("cancelled_at", now),
			db.C("cancel_reason", reason)); err != nil {
			return CompetitionView{}, err
		}
		return NewCompetitionView(*comp, now), community.AddAdminAction(ctx, t, actor, "cancel_competition", "competition",
			&competitionID, map[string]any{"reason": reason}, now)
	})
}

// SettleDueCompetitions 結算所有已截止並超過緩衝時間的競賽；actor 預設 "system"。回傳這次結算的競賽 ID。
func (c *CommunityService) SettleDueCompetitions(ctx context.Context, actor string) ([]int64, error) {
	cutoff := c.now().Add(-balance.CompetitionSettleGraceSeconds * time.Second)
	due, err := inTx(ctx, c.GameService, func(t *db.Tx) ([]int64, error) { return community.DueCompetitions(ctx, t, cutoff) })
	if err != nil {
		return nil, err
	}
	out := []int64{}
	for _, id := range due {
		ok, err := c.SettleCompetition(ctx, id, actor)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// SettleCompetition 結算一場競賽；已結算或已取消時回傳 false（重複呼叫不會重複發獎）。
func (c *CommunityService) SettleCompetition(ctx context.Context, competitionID int64, actor string) (bool, error) {
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (bool, error) {
		comp, err := community.GetCompetition(ctx, t, competitionID, true)
		if err != nil {
			return false, err
		}
		if comp == nil {
			return false, gameerr.NotFoundf("找不到競賽 #%d。", competitionID)
		}
		if comp.Status != "active" {
			return false, nil
		}
		if now.Before(comp.EndsAt.Add(balance.CompetitionSettleGraceSeconds * time.Second)) {
			return false, gameerr.Validationf("競賽尚未截止（或仍在截止後的結算緩衝時間內），不能結算。")
		}
		ranking, err := community.LiveRanking(ctx, t, competitionID)
		if err != nil {
			return false, err
		}
		if err := community.AddResults(ctx, t, competitionID, ranking); err != nil {
			return false, err
		}
		paid := 0
		for _, row := range ranking[:min(len(ranking), len(comp.Rewards))] {
			gold := comp.Rewards[row.Rank-1]
			if gold <= 0 {
				continue
			}
			recipients, err := community.AwardRecipients(ctx, t, row.GroupID)
			if err != nil {
				return false, err
			}
			for i := range recipients {
				farm := &recipients[i].Farm
				added, err := community.AddAward(ctx, t, models.CompetitionAward{CompetitionID: competitionID,
					PlayerID: farm.PlayerID, GroupID: row.GroupID, PlayerName: recipients[i].PlayerName, Rank: row.Rank,
					Gold: gold, CreatedAt: now})
				if err != nil {
					return false, err
				}
				if !added {
					continue
				}
				farm.Gold += gold
				if err := c.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
					return false, err
				}
				paid++
				r := ctxGroup(row.GroupID)
				r.GoldDelta = gold
				if err := c.record(ctx, t, farm, rules.EvCompetitionReward,
					fmt.Sprintf("競賽「%s」群組第 %d 名獎勵，金幣 +%d", comp.Name, row.Rank, gold), r); err != nil {
					return false, err
				}
			}
		}
		if _, err := db.UpdateByID(ctx, t, "competitions", comp.ID, db.C("status", "settled"), db.C("settled_at", now)); err != nil {
			return false, err
		}
		return true, community.AddAdminAction(ctx, t, actor, "settle_competition", "competition", &competitionID,
			map[string]any{"groups": len(ranking), "awards": paid}, now)
	})
}

// ---------- 玩家：任務 ----------

func memberQuest(ctx context.Context, t *db.Tx, groupID, playerID, questID int64) (*models.Quest, error) {
	if err := requireMember(ctx, t, groupID, playerID); err != nil {
		return nil, err
	}
	q, err := community.GetQuest(ctx, t, questID, false)
	if err != nil {
		return nil, err
	}
	if q == nil || q.GroupID != groupID { // 不透露其他群組的任務
		return nil, gameerr.NotFoundf("目前群組沒有任務 #%d。", questID)
	}
	return q, nil
}

// PlayerQuests 玩家所屬各群組中進行中的任務（只查詢，不需選擇群組）。
func (c *CommunityService) PlayerQuests(ctx context.Context, playerID int64) ([]QuestView, error) {
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) ([]QuestView, error) {
		list, err := groups.ListWithCounts(ctx, t, &playerID)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, g := range list {
			if g.IsMember {
				ids = append(ids, g.Group.ID)
			}
		}
		out := []QuestView{}
		if len(ids) == 0 {
			return out, nil
		}
		rows, err := community.ListQuests(ctx, t, ids, []string{"active", "completed"}, 50)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			v := NewQuestView(r.Quest, r.GroupName, now, names)
			if v.MyContribution, err = community.PlayerQuestContribution(ctx, t, v.QuestID, playerID); err != nil {
				return nil, err
			}
			if v.State == "active" || v.State == "scheduled" || v.State == "completed" {
				out = append(out, v)
			}
		}
		return out, nil
	})
}

func (c *CommunityService) GroupQuests(ctx context.Context, groupID, playerID int64) ([]QuestView, error) {
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) ([]QuestView, error) {
		if err := requireMember(ctx, t, groupID, playerID); err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		rows, err := community.ListQuests(ctx, t, []int64{groupID}, nil, 50)
		if err != nil {
			return nil, err
		}
		out := make([]QuestView, len(rows))
		for i, r := range rows {
			out[i] = NewQuestView(r.Quest, r.GroupName, now, names)
		}
		return out, nil
	})
}

func (c *CommunityService) QuestStatus(ctx context.Context, groupID, playerID, questID int64) (QuestView, error) {
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (QuestView, error) {
		q, err := memberQuest(ctx, t, groupID, playerID, questID)
		if err != nil {
			return QuestView{}, err
		}
		g, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return QuestView{}, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return QuestView{}, err
		}
		v := NewQuestView(*q, g.Name, now, names)
		if v.Contributors, err = community.QuestContributors(ctx, t, questID); err != nil {
			return QuestView{}, err
		}
		if v.MyContribution, err = community.PlayerQuestContribution(ctx, t, questID, playerID); err != nil {
			return QuestView{}, err
		}
		if v.MyClaim, err = community.GetClaim(ctx, t, questID, playerID); err != nil {
			return QuestView{}, err
		}
		v.MyReward, err = rewardFor(ctx, t, q, playerID)
		return v, err
	})
}

func rewardFor(ctx context.Context, t *db.Tx, q *models.Quest, playerID int64) (int, error) {
	mine, err := community.PlayerQuestContribution(ctx, t, q.ID, playerID)
	if err != nil || mine <= 0 {
		return 0, err
	}
	if q.RewardMode == "fixed" {
		return q.RewardAmount, nil
	}
	total, err := community.QuestTotalContribution(ctx, t, q.ID)
	return rules.ProportionalShare(q.RewardAmount, mine, total), err
}

func (c *CommunityService) ClaimQuest(ctx context.Context, groupID, playerID, questID int64) (ActionResult, error) {
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (ActionResult, error) {
		q, err := memberQuest(ctx, t, groupID, playerID, questID)
		if err != nil {
			return ActionResult{}, err
		}
		state := rules.EventState(q.Status, q.StartsAt, q.EndsAt, now)
		if state == "cancelled" {
			return ActionResult{}, gameerr.Validationf("任務 #%d 已取消（原因：%s），無法領取獎勵。", questID, pyStr(q.CancelReason))
		}
		if state != "completed" {
			hint := "尚未完成"
			if state == "expired" {
				hint = "已截止且未達成目標"
			}
			return ActionResult{}, gameerr.Validationf("任務 #%d %s，無法領取獎勵。", questID, hint)
		}
		// 鎖定農場後再檢查是否領過：重複指令或並行請求只會成功一次（另有唯一限制保護）
		farm, err := c.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		claim, err := community.GetClaim(ctx, t, questID, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if claim != nil {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經領取過任務 #%d 的獎勵。", questID)
		}
		amount, err := rewardFor(ctx, t, q, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		mine, err := community.PlayerQuestContribution(ctx, t, questID, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if mine <= 0 {
			return ActionResult{}, gameerr.Validationf("你沒有參與這個任務，無法領取獎勵。")
		}
		if amount <= 0 {
			return ActionResult{}, gameerr.Validationf("依貢獻比例分配後你的獎勵為 0。")
		}
		r := ctxGroup(groupID)
		if q.RewardType == "gold" {
			farm.Gold += amount
			if err := c.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
				return ActionResult{}, err
			}
			r.GoldDelta = amount
		} else {
			if err := c.ensureItemSpace(ctx, t, farm, q.RewardType, amount); err != nil {
				if gameerr.Is(err, gameerr.WarehouseFull) {
					return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "%s獎勵會保留，清出空間後再領取。", err.Error())
				}
				return ActionResult{}, err
			}
			if err := inventory.Add(ctx, t, farm.ID, q.RewardType, amount, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			r.ItemCode, r.Quantity = q.RewardType, amount
		}
		if err := db.Insert(ctx, t, "quest_claims", db.C("quest_id", questID), db.C("player_id", playerID),
			db.C("reward_type", q.RewardType), db.C("amount", amount), db.C("claimed_at", now)); err != nil {
			return ActionResult{}, err
		}
		label := RewardLabel(q.RewardType, amount)
		if err := c.record(ctx, t, farm, rules.EvQuestReward, fmt.Sprintf("領取任務「%s」獎勵：%s", q.Name, label), r); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已領取任務 #%d「%s」的獎勵：%s。", questID, q.Name, label), farm.Gold, 0, "", 0), nil
	})
}

// ---------- 玩家：競賽 ----------

// Competitions includeFinished 預設 true。先結算已到期的競賽。
func (c *CommunityService) Competitions(ctx context.Context, includeFinished bool) ([]CompetitionView, error) {
	if _, err := c.SettleDueCompetitions(ctx, "system"); err != nil {
		return nil, err
	}
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) ([]CompetitionView, error) {
		var statuses []string
		if !includeFinished {
			statuses = []string{"active"}
		}
		list, err := community.ListCompetitions(ctx, t, statuses, 50)
		if err != nil {
			return nil, err
		}
		out := make([]CompetitionView, len(list))
		for i, comp := range list {
			out[i] = NewCompetitionView(comp, now)
		}
		return out, nil
	})
}

// CompetitionDetail playerID 為 nil 時不填 My* 欄位。先結算已到期的競賽。
func (c *CommunityService) CompetitionDetail(ctx context.Context, competitionID int64, playerID *int64) (CompetitionDetail, error) {
	if _, err := c.SettleDueCompetitions(ctx, "system"); err != nil {
		return CompetitionDetail{}, err
	}
	now := c.now()
	return inTx(ctx, c.GameService, func(t *db.Tx) (CompetitionDetail, error) {
		comp, err := community.GetCompetition(ctx, t, competitionID, false)
		if err != nil {
			return CompetitionDetail{}, err
		}
		if comp == nil {
			return CompetitionDetail{}, gameerr.NotFoundf("找不到競賽 #%d。", competitionID)
		}
		d := CompetitionDetail{CompetitionView: NewCompetitionView(*comp, now), Final: comp.Status == "settled"}
		if d.Final {
			d.Ranking, err = community.FinalRanking(ctx, t, competitionID)
		} else {
			d.Ranking, err = community.LiveRanking(ctx, t, competitionID)
		}
		if err != nil {
			return CompetitionDetail{}, err
		}
		if d.TopPlayers, err = community.CompetitionPlayerPoints(ctx, t, competitionID, 20); err != nil {
			return CompetitionDetail{}, err
		}
		awards, err := community.CompetitionAwards(ctx, t, competitionID)
		if err != nil {
			return CompetitionDetail{}, err
		}
		for _, a := range awards {
			d.Awards = append(d.Awards, CompetitionAwardView{a.PlayerID, a.PlayerName, a.GroupID, a.Rank, a.Gold})
		}
		if playerID != nil {
			farm, err := farms.GetByPlayer(ctx, t, *playerID, false)
			if err != nil {
				return CompetitionDetail{}, err
			}
			if d.MyPoints, err = community.PlayerCompetitionPoints(ctx, t, competitionID, *playerID); err != nil {
				return CompetitionDetail{}, err
			}
			if farm != nil {
				d.MyHomeGroupID = farm.HomeGroupID
			}
			award, err := community.PlayerAward(ctx, t, competitionID, *playerID)
			if err != nil {
				return CompetitionDetail{}, err
			}
			if award != nil {
				d.MyAward = &CompetitionAwardView{award.PlayerID, award.PlayerName, award.GroupID, award.Rank, award.Gold}
			}
		}
		return d, nil
	})
}

// CompetitionHistory limit 預設 10。
func (c *CommunityService) CompetitionHistory(ctx context.Context, playerID *int64, limit int) ([]CompetitionDetail, error) {
	if _, err := c.SettleDueCompetitions(ctx, "system"); err != nil {
		return nil, err
	}
	list, err := inTx(ctx, c.GameService, func(t *db.Tx) ([]models.Competition, error) {
		return community.ListCompetitions(ctx, t, []string{"settled"}, limit)
	})
	if err != nil {
		return nil, err
	}
	out := []CompetitionDetail{}
	for _, comp := range list {
		d, err := c.CompetitionDetail(ctx, comp.ID, playerID)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
