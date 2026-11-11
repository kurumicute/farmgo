// Package codex 圖鑑登記：第一次取得物品的時間、累計數量與最高品質（對應 Python app/repositories/codex.py）。
package codex

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// CodexEvents 會登記到圖鑑的交易類型（取得物品的那一方）；山洞素材另外在領取探險時登記。
var CodexEvents = map[string]bool{"harvest": true, "steal": true, "collect_animal": true, "craft_collect": true}

// QualityRank 品質代碼 → 等級（normal 0 … legendary 3）。
var QualityRank = func() map[string]int {
	out := map[string]int{}
	for i, q := range models.QualityCodes {
		out[q] = i
	}
	return out
}()

type pendingKey struct {
	farmID   int64
	itemCode string
}

type pendingVal struct {
	quantity int
	rank     int
	firstAt  time.Time
}

// pending 一鍵操作中累積的圖鑑登記（依第一次累積的順序送出）。
type pending struct {
	order []pendingKey
	vals  map[pendingKey]pendingVal
}

func (p *pending) Flush(ctx context.Context, t *db.Tx) error {
	for _, k := range p.order {
		v := p.vals[k]
		if err := upsert(ctx, t, k.farmID, k.itemCode, v.quantity, v.rank, v.firstAt); err != nil {
			return err
		}
	}
	return nil
}

type deferredKey struct{}

// Register 登記圖鑑。一鍵操作中相同的物品合併成一次寫入（數量相加、品質取最高，db.Deferred）。
// quality 為 "" 表示 None（視為 normal）。
func Register(ctx context.Context, t *db.Tx, farmID int64, itemCode string, quantity int, quality string, now time.Time) error {
	if quantity <= 0 {
		return nil
	}
	if quality == "" {
		quality = "normal"
	}
	rank := QualityRank[quality]
	p, ok := db.Deferred(t, deferredKey{}, func() *pending { return &pending{vals: map[pendingKey]pendingVal{}} })
	if !ok {
		return upsert(ctx, t, farmID, itemCode, quantity, rank, now)
	}
	k := pendingKey{farmID, itemCode}
	old, exists := p.vals[k]
	if !exists {
		p.order = append(p.order, k)
		p.vals[k] = pendingVal{quantity, rank, now}
		return nil
	}
	first := old.firstAt
	if now.Before(first) {
		first = now
	}
	p.vals[k] = pendingVal{old.quantity + quantity, max(old.rank, rank), first}
	return nil
}

// upsert 不先送出延後寫入（對應 core_execute）。
func upsert(ctx context.Context, t *db.Tx, farmID int64, itemCode string, quantity, rank int, now time.Time) error {
	_, err := t.ExecNoFlush(ctx, "INSERT INTO farm_codex (farm_id, item_code, quantity, best_quality, first_at) "+
		"VALUES ($1, $2, $3, $4, $5) ON CONFLICT (farm_id, item_code) DO UPDATE SET "+
		"quantity = farm_codex.quantity + EXCLUDED.quantity, "+
		"best_quality = greatest(farm_codex.best_quality, EXCLUDED.best_quality)",
		farmID, itemCode, quantity, rank, now)
	return err
}

// Entries 物品代碼 → 圖鑑資料。
func Entries(ctx context.Context, t *db.Tx, farmID int64) (map[string]models.FarmCodexEntry, error) {
	rows, err := db.QueryStructs[models.FarmCodexEntry](ctx, t,
		"SELECT farm_id, item_code, quantity, best_quality, first_at FROM farm_codex WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.FarmCodexEntry, len(rows))
	for _, r := range rows {
		out[r.ItemCode] = r
	}
	return out, nil
}
