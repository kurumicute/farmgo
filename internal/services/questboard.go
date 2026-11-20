package services

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	eventrules "farmgm/internal/game/events"
	"farmgm/internal/game/questboard"
	"farmgm/internal/game/treasure"
)

// 伺服器委託板（Go 版新增，2026-11-20，規則見 game/questboard）：沿用群組任務（quests），不需要 Migration。

// boardActiveDays 「活躍玩家」的認定：最近幾天有遊戲操作（farms.updated_at，每筆交易紀錄都會更新）。
const boardActiveDays = 7

type boardGroup struct {
	GroupID int64 `db:"group_id"`
	Players int   `db:"players"`
}

// EnsureWeeklyQuestBoards 替每個最近有活躍玩家的群組建立本週的 3 張委託（已經建立過的群組略過）；回傳新建立的任務數。
// 由背景結算定期呼叫（有 advisory lock）；另外以群組列鎖定與「本週是否已建立」的檢查保證不重複。
func (s *GameService) EnsureWeeklyQuestBoards(ctx context.Context) (int, error) {
	now := s.now()
	week := questboard.WeekStart(now)
	end := week.AddDate(0, 0, 7)
	list, err := inTx(ctx, s, func(t *db.Tx) ([]boardGroup, error) {
		return db.QueryStructs[boardGroup](ctx, t, `SELECT home_group_id AS group_id, count(*)::int AS players FROM farms
			WHERE home_group_id IS NOT NULL AND updated_at >= $1 GROUP BY home_group_id ORDER BY home_group_id`,
			now.AddDate(0, 0, -boardActiveDays))
	})
	if err != nil {
		return 0, err
	}
	created := 0
	for _, g := range list {
		n, err := inTx(ctx, s, func(t *db.Tx) (int, error) {
			if _, found, err := db.QueryValue[int64](ctx, t, "SELECT id FROM groups WHERE id = $1 FOR UPDATE", g.GroupID); err != nil || !found {
				return 0, err
			}
			exists, _, err := db.QueryValue[int](ctx, t, `SELECT count(*)::int FROM quests
				WHERE group_id = $1 AND created_by LIKE 'questboard:%' AND starts_at = $2`, g.GroupID, week)
			if err != nil || exists > 0 {
				return 0, err
			}
			for _, tp := range questboard.Pick(g.GroupID, week) {
				if _, err := t.Exec(ctx, `INSERT INTO quests (group_id, name, description, quest_type, metric, item_filter, target,
					reward_mode, reward_type, reward_amount, starts_at, ends_at, status, progress, created_by, created_at)
					VALUES ($1, $2, $3, $4, $5, NULL, $6, 'proportional', 'gold', $7, $8, $9, 'active', 0, $10, $11)`,
					g.GroupID, tp.Title(), tp.Description, tp.QuestType, tp.Metric, tp.Target(g.Players), tp.Pool(g.Players),
					week, end, tp.Creator(), now); err != nil {
					return 0, err
				}
			}
			return len(questboard.Tiers), nil
		})
		if err != nil {
			return created, err
		}
		created += n
	}
	return created, nil
}

type boardBuffKey struct{ groupID int64 }

// boardBuffs 群組最近 BuffHours 小時內完成的委託帶來的增益（虛擬活動）；同一筆交易的 Bulk 中只查一次。
func (s *GameService) boardBuffs(ctx context.Context, t *db.Tx, groupID int64) ([]*eventrules.Event, error) {
	return db.Memo(t, boardBuffKey{groupID}, func() ([]*eventrules.Event, error) {
		creators, err := db.QueryValues[string](ctx, t, `SELECT DISTINCT created_by FROM quests
			WHERE group_id = $1 AND status = 'completed' AND created_by LIKE 'questboard:%' AND completed_at > $2`,
			groupID, s.now().Add(-questboard.BuffHours*time.Hour))
		if err != nil {
			return nil, err
		}
		var out []*eventrules.Event
		for _, tier := range questboard.Tiers {
			for _, c := range creators {
				if got, ok := questboard.TierOf(c); ok && got == tier {
					out = append(out, questboard.Buffs[tier].Event(tier))
				}
			}
		}
		return out, nil
	})
}

// boardBonusPiece 領取委託獎勵時：本週三張委託都完成了，每位玩家每週一次多得 1 片藏寶圖碎片（farm 已鎖定）。
func (s *GameService) boardBonusPiece(ctx context.Context, t *db.Tx, farm *models.Farm, q *models.Quest) (string, error) {
	if _, ok := questboard.TierOf(q.CreatedBy); !ok {
		return "", nil
	}
	done, _, err := db.QueryValue[int](ctx, t, `SELECT count(*)::int FROM quests WHERE group_id = $1 AND status = 'completed'
		AND created_by LIKE 'questboard:%' AND starts_at = $2`, q.GroupID, q.StartsAt)
	if err != nil || done < len(questboard.Tiers) {
		return "", err
	}
	tool, st, err := lockedTreasure(ctx, t, farm.ID)
	if err != nil {
		return "", err
	}
	week := questboard.WeekStart(q.StartsAt).Format("2006-01-02")
	if st.BoardWeek == week {
		return "", nil
	}
	now := s.now()
	piece := treasure.RandomPiece(s.TreasureRng())
	st.Pieces[piece]++
	st.BoardWeek = week
	if err := saveSettingsTool(ctx, t, farm.ID, tool, treasure.ToolCode, st.Encode(), now); err != nil {
		return "", err
	}
	return txf(ctx, "\n📜 本週三張委託全部完成！額外得到 1 片藏寶圖碎片・%s。", pieceName(ctx, piece)), nil
}
