// Package community 群組任務、競賽與管理紀錄的資料存取（對應 Python app/repositories/community.py）。
package community

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// ---------- 管理紀錄 ----------

// AddAdminAction details 為 nil 時寫入 {}。
func AddAdminAction(ctx context.Context, t *db.Tx, actor, action, targetType string, targetID *int64,
	details map[string]any, now time.Time) error {
	if details == nil {
		details = map[string]any{}
	}
	return db.Insert(ctx, t, models.TableAdminAction,
		db.C("actor", actor), db.C("action", action), db.C("target_type", targetType), db.C("target_id", targetID),
		db.C("details", details), db.C("created_at", now))
}

// ListAdminActions limit 預設 50。
func ListAdminActions(ctx context.Context, t *db.Tx, limit int) ([]models.AdminAction, error) {
	return db.QueryStructs[models.AdminAction](ctx, t,
		`SELECT * FROM admin_actions ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
}

// ---------- 任務 ----------

// GetQuest forUpdate 預設 false。
func GetQuest(ctx context.Context, t *db.Tx, questID int64, forUpdate bool) (*models.Quest, error) {
	sql := `SELECT * FROM quests WHERE id = $1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.Quest](ctx, t, sql, questID)
}

// OpenQuestsForEvent 進行中、且事件時間落在期間內的任務；鎖定以序列化進度與完成判定。
func OpenQuestsForEvent(ctx context.Context, t *db.Tx, groupID int64, now time.Time) ([]models.Quest, error) {
	return db.QueryStructs[models.Quest](ctx, t, `
		SELECT * FROM quests WHERE group_id = $1 AND status = 'active' AND starts_at <= $2 AND ends_at > $2
		ORDER BY id FOR UPDATE`, groupID, now)
}

// AddQuestContribution 同一筆操作對同一任務只會寫入一次；回傳是否新增。
func AddQuestContribution(ctx context.Context, t *db.Tx, questID, playerID, transactionID int64, amount int, now time.Time) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, `
		INSERT INTO quest_contributions (quest_id, player_id, transaction_id, amount, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT ON CONSTRAINT uq_quest_contributions_once DO NOTHING RETURNING id`,
		questID, playerID, transactionID, amount, now)
	return ok, err
}

// QuestWithGroup 任務與群組名稱。
type QuestWithGroup struct {
	models.Quest
	GroupName string `db:"group_name"`
}

// ListQuests groupIDs 為 nil 時不篩選群組；statuses 為 nil 時不篩選狀態；limit 預設 50。
func ListQuests(ctx context.Context, t *db.Tx, groupIDs []int64, statuses []string, limit int) ([]QuestWithGroup, error) {
	return db.QueryStructs[QuestWithGroup](ctx, t, `
		SELECT q.*, g.name AS group_name FROM quests q JOIN groups g ON g.id = q.group_id
		WHERE ($1::bigint[] IS NULL OR q.group_id = ANY($1)) AND ($2::text[] IS NULL OR q.status = ANY($2))
		ORDER BY q.id DESC LIMIT $3`, groupIDs, statuses, limit)
}

// Contributor 任務貢獻者：(玩家ID, 名稱, 貢獻總量)。
type Contributor struct {
	PlayerID   int64  `db:"player_id"`
	PlayerName string `db:"player_name"`
	Total      int    `db:"total"`
}

// QuestContributors 依貢獻由高至低。
func QuestContributors(ctx context.Context, t *db.Tx, questID int64) ([]Contributor, error) {
	return db.QueryStructs[Contributor](ctx, t, `
		SELECT p.id AS player_id, p.display_name AS player_name, sum(c.amount) AS total
		FROM quest_contributions c JOIN players p ON p.id = c.player_id
		WHERE c.quest_id = $1
		GROUP BY p.id, p.display_name
		ORDER BY sum(c.amount) DESC, p.id`, questID)
}

func PlayerQuestContribution(ctx context.Context, t *db.Tx, questID, playerID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t,
		`SELECT coalesce(sum(amount), 0) FROM quest_contributions WHERE quest_id = $1 AND player_id = $2`, questID, playerID)
	return n, err
}

func QuestTotalContribution(ctx context.Context, t *db.Tx, questID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t,
		`SELECT coalesce(sum(amount), 0) FROM quest_contributions WHERE quest_id = $1`, questID)
	return n, err
}

func GetClaim(ctx context.Context, t *db.Tx, questID, playerID int64) (*models.QuestClaim, error) {
	return db.QueryStruct[models.QuestClaim](ctx, t,
		`SELECT * FROM quest_claims WHERE quest_id = $1 AND player_id = $2 LIMIT 1`, questID, playerID)
}

// ClaimWithPlayer 領獎紀錄與玩家名稱。
type ClaimWithPlayer struct {
	models.QuestClaim
	PlayerName string `db:"player_name"`
}

func QuestClaims(ctx context.Context, t *db.Tx, questID int64) ([]ClaimWithPlayer, error) {
	return db.QueryStructs[ClaimWithPlayer](ctx, t, `
		SELECT c.*, p.display_name AS player_name FROM quest_claims c JOIN players p ON p.id = c.player_id
		WHERE c.quest_id = $1 ORDER BY c.claimed_at, c.id`, questID)
}

// ---------- 競賽 ----------

// GetCompetition forUpdate 預設 false。
func GetCompetition(ctx context.Context, t *db.Tx, competitionID int64, forUpdate bool) (*models.Competition, error) {
	sql := `SELECT * FROM competitions WHERE id = $1`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.Competition](ctx, t, sql, competitionID)
}

// OpenCompetitionsForEvent 進行中且 now 在期間內的競賽（順序不保證，同 Python）。
func OpenCompetitionsForEvent(ctx context.Context, t *db.Tx, now time.Time) ([]models.Competition, error) {
	return db.QueryStructs[models.Competition](ctx, t,
		`SELECT * FROM competitions WHERE status = 'active' AND starts_at <= $1 AND ends_at > $1`, now)
}

func AddCompetitionContribution(ctx context.Context, t *db.Tx, competitionID, groupID, playerID, transactionID int64,
	points int, now time.Time) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, `
		INSERT INTO competition_contributions (competition_id, group_id, player_id, transaction_id, points, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT ON CONSTRAINT uq_competition_contributions_once DO NOTHING RETURNING id`,
		competitionID, groupID, playerID, transactionID, points, now)
	return ok, err
}

// ListCompetitions statuses 為 nil 時不篩選；limit 預設 50。
func ListCompetitions(ctx context.Context, t *db.Tx, statuses []string, limit int) ([]models.Competition, error) {
	return db.QueryStructs[models.Competition](ctx, t, `
		SELECT * FROM competitions WHERE ($1::text[] IS NULL OR status = ANY($1)) ORDER BY id DESC LIMIT $2`,
		statuses, limit)
}

// DueCompetitions 已截止且超過結算緩衝時間、尚未結算的競賽 ID。
func DueCompetitions(ctx context.Context, t *db.Tx, cutoff time.Time) ([]int64, error) {
	return db.QueryValues[int64](ctx, t,
		`SELECT id FROM competitions WHERE status = 'active' AND ends_at <= $1`, cutoff)
}

// RankingRow 群組排名（JSON 欄位名稱與 Python dict 相同）。
type RankingRow struct {
	Rank         int    `db:"rank" json:"rank"`
	GroupID      int64  `db:"group_id" json:"group_id"`
	GroupName    string `db:"group_name" json:"group_name"`
	Score        int    `db:"score" json:"score"`
	Contributors int    `db:"contributors" json:"contributors"`
}

// LiveRanking 依貢獻計算的即時群組排名（同分依群組 ID）。
func LiveRanking(ctx context.Context, t *db.Tx, competitionID int64) ([]RankingRow, error) {
	return db.QueryStructs[RankingRow](ctx, t, `
		SELECT (row_number() OVER (ORDER BY sum(c.points) DESC, g.id))::int AS rank,
			g.id AS group_id, g.name AS group_name, sum(c.points) AS score,
			count(DISTINCT c.player_id) AS contributors
		FROM competition_contributions c JOIN groups g ON g.id = c.group_id
		WHERE c.competition_id = $1
		GROUP BY g.id, g.name
		ORDER BY sum(c.points) DESC, g.id`, competitionID)
}

func FinalRanking(ctx context.Context, t *db.Tx, competitionID int64) ([]RankingRow, error) {
	return db.QueryStructs[RankingRow](ctx, t, `
		SELECT rank, group_id, group_name, score, contributors FROM competition_results
		WHERE competition_id = $1 ORDER BY rank`, competitionID)
}

// PlayerPoints 競賽中玩家的得分（JSON 欄位名稱與 Python dict 相同）。
type PlayerPoints struct {
	PlayerID   int64  `db:"player_id" json:"player_id"`
	PlayerName string `db:"player_name" json:"player_name"`
	GroupID    int64  `db:"group_id" json:"group_id"`
	GroupName  string `db:"group_name" json:"group_name"`
	Points     int    `db:"points" json:"points"`
}

// CompetitionPlayerPoints limit 預設 20。
func CompetitionPlayerPoints(ctx context.Context, t *db.Tx, competitionID int64, limit int) ([]PlayerPoints, error) {
	return db.QueryStructs[PlayerPoints](ctx, t, `
		SELECT p.id AS player_id, p.display_name AS player_name, g.id AS group_id, g.name AS group_name,
			sum(c.points) AS points
		FROM competition_contributions c
		JOIN players p ON p.id = c.player_id
		JOIN groups g ON g.id = c.group_id
		WHERE c.competition_id = $1
		GROUP BY p.id, p.display_name, g.id, g.name
		ORDER BY sum(c.points) DESC, p.id LIMIT $2`, competitionID, limit)
}

func PlayerCompetitionPoints(ctx context.Context, t *db.Tx, competitionID, playerID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t, `
		SELECT coalesce(sum(points), 0) FROM competition_contributions WHERE competition_id = $1 AND player_id = $2`,
		competitionID, playerID)
	return n, err
}

// FarmWithPlayer 農場與玩家名稱。
type FarmWithPlayer struct {
	models.Farm
	PlayerName string `db:"player_name"`
}

// AwardRecipients 結算當下的群組成員中，農場主要所屬群組就是該群組的玩家（每人只屬於一個主要群組，不會重複領獎）。
// 鎖定農場列（FOR UPDATE OF farms）。
func AwardRecipients(ctx context.Context, t *db.Tx, groupID int64) ([]FarmWithPlayer, error) {
	return db.QueryStructs[FarmWithPlayer](ctx, t, `
		SELECT f.*, p.display_name AS player_name
		FROM farms f JOIN players p ON p.id = f.player_id JOIN group_members gm ON gm.player_id = p.id
		WHERE gm.group_id = $1 AND f.home_group_id = $1
		ORDER BY f.id FOR UPDATE OF f`, groupID)
}

func AddResults(ctx context.Context, t *db.Tx, competitionID int64, ranking []RankingRow) error {
	for _, r := range ranking {
		if err := db.Insert(ctx, t, models.TableCompetitionResult,
			db.C("competition_id", competitionID), db.C("group_id", r.GroupID), db.C("group_name", r.GroupName),
			db.C("rank", r.Rank), db.C("score", r.Score), db.C("contributors", r.Contributors)); err != nil {
			return err
		}
	}
	return nil
}

// AddAward 新增競賽獎勵（(competition_id, player_id) 已存在時不新增）；回傳是否新增。
func AddAward(ctx context.Context, t *db.Tx, v models.CompetitionAward) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, `
		INSERT INTO competition_awards (competition_id, player_id, group_id, player_name, rank, gold, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (competition_id, player_id) DO NOTHING RETURNING player_id`,
		v.CompetitionID, v.PlayerID, v.GroupID, v.PlayerName, v.Rank, v.Gold, v.CreatedAt)
	return ok, err
}

func CompetitionAwards(ctx context.Context, t *db.Tx, competitionID int64) ([]models.CompetitionAward, error) {
	return db.QueryStructs[models.CompetitionAward](ctx, t,
		`SELECT * FROM competition_awards WHERE competition_id = $1 ORDER BY rank, player_id`, competitionID)
}

func PlayerAward(ctx context.Context, t *db.Tx, competitionID, playerID int64) (*models.CompetitionAward, error) {
	return db.QueryStruct[models.CompetitionAward](ctx, t,
		`SELECT * FROM competition_awards WHERE competition_id = $1 AND player_id = $2`, competitionID, playerID)
}
