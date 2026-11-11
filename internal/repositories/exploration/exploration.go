// Package exploration 山洞探險：各區域的探險次數、進行中的探險與探險紀錄（對應 Python app/repositories/exploration.py）。
package exploration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

const cooldownCols = "id, farm_id, area_code, last_explored_at, next_available_at, explore_count"

const logCols = "id, farm_id, area_code, farm_level, advanced, rewards, events, explored_at, next_available_at, claimed_at"

// ListCooldowns 區域代碼 → 探險次數列。
func ListCooldowns(ctx context.Context, t *db.Tx, farmID int64) (map[string]models.FarmExploration, error) {
	rows, err := db.QueryStructs[models.FarmExploration](ctx, t,
		"SELECT "+cooldownCols+" FROM farm_explorations WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.FarmExploration, len(rows))
	for _, r := range rows {
		out[r.AreaCode] = r
	}
	return out, nil
}

// GetCooldown forUpdate 預設 false。
func GetCooldown(ctx context.Context, t *db.Tx, farmID int64, areaCode string, forUpdate bool) (*models.FarmExploration, error) {
	sql := "SELECT " + cooldownCols + " FROM farm_explorations WHERE farm_id = $1 AND area_code = $2 LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmExploration](ctx, t, sql, farmID, areaCode)
}

// SetCooldown row 為 nil 時建立（explore_count = 1），否則更新時間並把次數加一。
// (farm_id, area_code) 唯一：並行時第二筆在這裡失敗。
func SetCooldown(ctx context.Context, t *db.Tx, row *models.FarmExploration, farmID int64, areaCode string,
	now, until time.Time) (*models.FarmExploration, error) {
	if row == nil {
		return db.QueryStruct[models.FarmExploration](ctx, t,
			"INSERT INTO farm_explorations (farm_id, area_code, last_explored_at, next_available_at, explore_count) "+
				"VALUES ($1, $2, $3, $4, 1) RETURNING "+cooldownCols, farmID, areaCode, now, until)
	}
	return db.QueryStruct[models.FarmExploration](ctx, t,
		"UPDATE farm_explorations SET last_explored_at = $2, next_available_at = $3, explore_count = explore_count + 1 "+
			"WHERE id = $1 RETURNING "+cooldownCols, row.ID, now, until)
}

// AddLog 新增探險紀錄（log.ID 會被忽略），回傳含 ID 的紀錄。Rewards／Events 為 nil 時寫入 {}／[]。
// rewards 以 Go map 編碼時鍵會被排序；需要保留 Python dict 順序時用 AddLogOrdered。
func AddLog(ctx context.Context, t *db.Tx, log models.ExplorationLog) (*models.ExplorationLog, error) {
	return AddLogOrdered(ctx, t, log, nil)
}

// AddLogOrdered 同 AddLog，但 rewards 依 order 的順序寫入 JSON（欄位是 json，會保留鍵的順序，與 Python dict 相同）。
// order 為 nil 時依 map 編碼（鍵排序）；order 必須涵蓋 Rewards 的所有鍵。
func AddLogOrdered(ctx context.Context, t *db.Tx, log models.ExplorationLog, order []string) (*models.ExplorationLog, error) {
	if log.Rewards == nil {
		log.Rewards = map[string]int{}
	}
	if log.Events == nil {
		log.Events = []string{}
	}
	var rewards any = log.Rewards
	if order != nil {
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range order {
			if i > 0 {
				b.WriteString(", ")
			}
			key, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "%s: %d", key, log.Rewards[k])
		}
		b.WriteByte('}')
		rewards = b.String()
	}
	return db.QueryStruct[models.ExplorationLog](ctx, t,
		"INSERT INTO exploration_logs (farm_id, area_code, farm_level, advanced, rewards, events, explored_at, "+
			"next_available_at, claimed_at) VALUES ($1, $2, $3, $4, $5::json, $6, $7, $8, $9) RETURNING "+logCols,
		log.FarmID, log.AreaCode, log.FarmLevel, log.Advanced, rewards, log.Events, log.ExploredAt,
		log.NextAvailableAt, log.ClaimedAt)
}

// RewardOrders 探險紀錄 rewards 的鍵順序（json 欄位保留寫入時的順序）：紀錄 ID → 物品代碼。
func RewardOrders(ctx context.Context, t *db.Tx, logIDs []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(logIDs) == 0 {
		return out, nil
	}
	rows, err := t.Query(ctx, `SELECT l.id, r.key FROM exploration_logs l, json_each(l.rewards) WITH ORDINALITY AS r(key, value, n)
		WHERE l.id = ANY($1) ORDER BY l.id, r.n`, logIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = append(out[id], key)
	}
	return out, rows.Err()
}

// GetActive 進行中或已回來但還沒領取的探險（每座農場最多一筆）。forUpdate 預設 false。
func GetActive(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.ExplorationLog, error) {
	sql := "SELECT " + logCols + " FROM exploration_logs WHERE farm_id = $1 AND claimed_at IS NULL LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.ExplorationLog](ctx, t, sql, farmID)
}

// ListLogs 已領取的探險紀錄（未領取的獎勵還沒揭曉，不列出）。
func ListLogs(ctx context.Context, t *db.Tx, farmID int64, limit int) ([]models.ExplorationLog, error) {
	return db.QueryStructs[models.ExplorationLog](ctx, t,
		"SELECT "+logCols+" FROM exploration_logs WHERE farm_id = $1 AND claimed_at IS NOT NULL "+
			"ORDER BY explored_at DESC, id DESC LIMIT $2", farmID, limit)
}

// MarkClaimed 領取探險（Python 由服務層設定 log.claimed_at 後 flush）。
func MarkClaimed(ctx context.Context, t *db.Tx, logID int64, now time.Time) error {
	_, err := db.UpdateByID(ctx, t, models.TableExplorationLog, logID, db.C("claimed_at", now))
	return err
}
