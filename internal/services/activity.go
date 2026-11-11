package services

// 把成功的遊戲操作計入群組任務與競賽（對應 Python app/services/activity.py）。
//
// 由 record 在寫入交易紀錄後、於同一筆資料庫交易中呼叫，因此：
//   - 只有成功提交的操作才會計分；操作失敗回滾時，貢獻也一起回滾。
//   - 每筆貢獻都以 (任務或競賽, 交易紀錄 ID) 唯一，同一筆操作不會重複計分，重啟後也不會重算。
//
// 歸屬規則：操作發生時的群組（context_group_id）必須等於農場的主要所屬群組（home_group_id），
// 且玩家為該群組成員（record 只在玩家是成員時才記錄 context）。因此每筆操作最多計入一個群組。

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/transactions"
)

// scoringEvents 可能影響任務或競賽的事件類型；其他事件直接略過，不做額外查詢
var scoringEvents = map[string]bool{
	"harvest": true, "sell": true, "upgrade": true, "build": true, "collect_animal": true, "defended": true,
}

// addQuestContribution 寫入任務貢獻（測試可替換以模擬失敗）。
var addQuestContribution = community.AddQuestContribution

type (
	questsKey       struct{ groupID int64 }
	competitionsKey struct{}
)

func scoringGroup(farm *models.Farm, groupID *int64) *int64 {
	if groupID == nil || farm.HomeGroupID == nil || *farm.HomeGroupID != *groupID {
		return nil
	}
	return groupID
}

// openQuests 快取的清單（Bulk 期間）由 onEvent 直接修改進度與狀態，與 Python 的 ORM 物件相同。
func openQuests(ctx context.Context, t *db.Tx, groupID int64, now time.Time) ([]models.Quest, error) {
	return db.Memo(t, questsKey{groupID}, func() ([]models.Quest, error) {
		return community.OpenQuestsForEvent(ctx, t, groupID, now)
	})
}

func openCompetitions(ctx context.Context, t *db.Tx, now time.Time) ([]models.Competition, error) {
	return db.Memo(t, competitionsKey{}, func() ([]models.Competition, error) {
		return community.OpenCompetitionsForEvent(ctx, t, now)
	})
}

// mayScore 這筆交易可能計入任務或競賽嗎？（計分要交易紀錄的 ID，可能時不能延後寫入；查詢結果與 onEvent 共用快取。）
func mayScore(ctx context.Context, t *db.Tx, farm *models.Farm, eventType string, groupID *int64, now time.Time) (bool, error) {
	if !scoringEvents[eventType] || scoringGroup(farm, groupID) == nil {
		return false, nil
	}
	quests, err := openQuests(ctx, t, *groupID, now)
	if err != nil || len(quests) > 0 {
		return len(quests) > 0, err
	}
	comps, err := openCompetitions(ctx, t, now)
	return len(comps) > 0, err
}

func onEvent(ctx context.Context, t *db.Tx, farm *models.Farm, tx transactions.TxRecord) error {
	if !scoringEvents[tx.EventType] {
		return nil
	}
	gid := scoringGroup(farm, tx.ContextGroupID)
	if gid == nil {
		return nil
	}
	groupID, now := *gid, tx.CreatedAt
	itemCode := ""
	if tx.ItemCode != nil {
		itemCode = *tx.ItemCode
	}

	quests, err := openQuests(ctx, t, groupID, now)
	if err != nil {
		return err
	}
	for i := range quests {
		q := &quests[i]
		// 快取的清單：迴圈中已達標完成、或時間已過的任務不再計入（與重新查詢的結果相同）
		if q.Status != "active" || now.Before(q.StartsAt) || !now.Before(q.EndsAt) {
			continue
		}
		filter := ""
		if q.ItemFilter != nil {
			filter = *q.ItemFilter
		}
		amount := rules.QuestAmount(q.QuestType, q.Metric, filter, tx.EventType, itemCode, tx.Quantity, tx.GoldDelta)
		if amount <= 0 {
			continue
		}
		added, err := addQuestContribution(ctx, t, q.ID, farm.PlayerID, *tx.ID, amount, now)
		if err != nil {
			return err
		}
		if !added {
			continue
		}
		q.Progress += amount
		cols := []db.Col{db.C("progress", q.Progress)}
		if q.Progress >= q.Target {
			// 達標即完成；之後的操作不再計入此任務
			q.Status = "completed"
			completed := now
			q.CompletedAt = &completed
			cols = append(cols, db.C("status", q.Status), db.C("completed_at", q.CompletedAt))
		}
		if _, err := db.UpdateByID(ctx, t, "quests", q.ID, cols...); err != nil {
			return err
		}
	}

	comps, err := openCompetitions(ctx, t, now)
	if err != nil {
		return err
	}
	for _, c := range comps {
		if c.Status != "active" || now.Before(c.StartsAt) || !now.Before(c.EndsAt) {
			continue
		}
		points := rules.CompetitionPoints(c.CompType, tx.EventType, tx.Quantity, tx.GoldDelta)
		if points > 0 {
			if _, err := community.AddCompetitionContribution(ctx, t, c.ID, groupID, farm.PlayerID, *tx.ID, points, now); err != nil {
				return err
			}
		}
	}
	return nil
}
