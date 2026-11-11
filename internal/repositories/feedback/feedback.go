// Package feedback 操作回饋用的玩家資料：上次打開面板的時間（「你不在的時候」摘要）與個人紀錄
// （對應 Python app/repositories/feedback.py）。
package feedback

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

const touchSQL = "WITH old AS (SELECT last_seen_at FROM farm_feedback WHERE farm_id = $1) " +
	"INSERT INTO farm_feedback (farm_id, last_seen_at, records) VALUES ($1, $2, '{}') " +
	"ON CONFLICT (farm_id) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at " +
	"RETURNING (SELECT last_seen_at FROM old)"

// TouchSeen 記錄這次打開面板的時間，回傳上一次的時間（第一次為 nil）。
func TouchSeen(ctx context.Context, t *db.Tx, farmID int64, now time.Time) (*time.Time, error) {
	var prev *time.Time
	err := t.QueryRow(ctx, touchSQL, farmID, now).Scan(&prev)
	return prev, err
}

// BumpRecord value 超過目前紀錄時更新並回傳舊紀錄（第一次記錄回傳 nil）；沒有破紀錄回傳 nil。
//
// 呼叫端必須已鎖定這座農場（同一座農場的紀錄更新依序執行）。
func BumpRecord(ctx context.Context, t *db.Tx, farmID int64, key string, value int) (*int, error) {
	var records map[string]int
	err := t.QueryRow(ctx, "SELECT records FROM farm_feedback WHERE farm_id = $1 FOR UPDATE", farmID).Scan(&records)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = t.Exec(ctx, "INSERT INTO farm_feedback (farm_id, records) VALUES ($1, $2)", farmID, map[string]int{key: value})
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	old, ok := records[key]
	if ok && value <= old {
		return nil, nil
	}
	next := make(map[string]int, len(records)+1)
	for k, v := range records {
		next[k] = v
	}
	next[key] = value
	if _, err := db.UpdateWhere(ctx, t, models.TableFarmFeedback, []db.Col{db.C("records", next)}, "farm_id", farmID); err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &old, nil
}

// StealSummary since 之後這位玩家被偷的情況。
type StealSummary struct {
	StolenTimes    int            // 成功次數
	StolenQuantity int            // 成功被偷的數量
	Blocked        map[string]int // 被擋下的次數（設施或寵物代碼 → 次數；blocked_by 為 NULL 時鍵為 ""）
	Thieves        []string       // 偷最多的偷菜者名稱（最多 3 位）
}

// StealsAgainst since 之後這位玩家被偷的情況：成功次數與數量、偷菜者名稱、被擋下的次數（狗狗與防守設施分開）。
func StealsAgainst(ctx context.Context, t *db.Tx, victimPlayerID int64, since time.Time) (StealSummary, error) {
	out := StealSummary{Blocked: map[string]int{}, Thieves: []string{}}
	rows, err := t.Query(ctx, "SELECT outcome, blocked_by, count(*), sum(quantity) FROM steal_records "+
		"WHERE victim_player_id = $1 AND created_at > $2 GROUP BY outcome, blocked_by", victimPlayerID, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var outcome string
		var blockedBy *string
		var count int
		var qty *int64
		if err := rows.Scan(&outcome, &blockedBy, &count, &qty); err != nil {
			rows.Close()
			return out, err
		}
		if outcome == "success" {
			out.StolenTimes += count
			if qty != nil {
				out.StolenQuantity += int(*qty)
			}
		} else {
			key := ""
			if blockedBy != nil {
				key = *blockedBy
			}
			out.Blocked[key] += count
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.StolenTimes > 0 {
		names, err := db.QueryValues[string](ctx, t, "SELECT p.display_name FROM players p "+
			"JOIN steal_records s ON s.thief_player_id = p.id "+
			"WHERE s.victim_player_id = $1 AND s.created_at > $2 AND s.outcome = 'success' "+
			"GROUP BY p.display_name ORDER BY sum(s.quantity) DESC LIMIT 3", victimPlayerID, since)
		if err != nil {
			return out, err
		}
		out.Thieves = names
	}
	return out, nil
}
