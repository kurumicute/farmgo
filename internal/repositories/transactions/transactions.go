// Package transactions 交易紀錄 game_transactions（對應 Python app/repositories/transactions.py）。
package transactions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

// TxRecord 剛寫入的交易紀錄（排行統計、任務計分與圖鑑登記需要的欄位）。
type TxRecord struct {
	ID             *int64 // Defer=true 時尚未寫入，沒有 ID（任務／競賽計分需要 ID 時不會延後）
	FarmID         int64
	EventType      string
	ItemCode       *string
	Quantity       int
	GoldDelta      int
	ContextGroupID *int64
	Quality        *string
	Mutation       *string
	CreatedAt      time.Time
}

// AddParams 對應 Python add() 的 keyword 參數；可選欄位用 nil。
type AddParams struct {
	Defer          bool // 一鍵操作中累積，離開 Bulk 或下一次查詢前整批寫入，回傳的 ID 是 nil
	FarmID         int64
	EventType      string
	Description    string
	CreatedAt      time.Time
	ItemCode       *string
	Quantity       int
	GoldDelta      int
	ContextGroupID *int64
	Quality        *string
	Mutation       *string
	UnitPrice      *int
	AppliedEvents  []models.AppliedEvent // nil = NULL
}

const insertSQL = `INSERT INTO game_transactions (farm_id, context_group_id, quality, mutation, unit_price, applied_events,
	event_type, item_code, quantity, gold_delta, description, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

func (p *AddParams) args() ([]any, error) {
	var applied any // NULL
	if p.AppliedEvents != nil {
		data, err := json.Marshal(p.AppliedEvents)
		if err != nil {
			return nil, err
		}
		applied = string(data)
	}
	return []any{p.FarmID, p.ContextGroupID, p.Quality, p.Mutation, p.UnitPrice, applied,
		p.EventType, p.ItemCode, p.Quantity, p.GoldDelta, p.Description, p.CreatedAt}, nil
}

// pending 一鍵操作中累積的交易紀錄；一次送出（pgx.Batch），依累積順序寫入，ID 順序與逐筆寫入相同。
type pending struct{ rows [][]any }

func (p *pending) Flush(ctx context.Context, t *db.Tx) error {
	if len(p.rows) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, args := range p.rows {
		b.Queue(insertSQL, args...)
	}
	return t.SendBatchNoFlush(ctx, b)
}

type deferKey struct{}

// Add 寫入一筆交易紀錄（INSERT … RETURNING id；不先送出其他延後寫入以外的東西，見 ExecNoFlush）。
func Add(ctx context.Context, t *db.Tx, p AddParams) (TxRecord, error) {
	rec := TxRecord{FarmID: p.FarmID, EventType: p.EventType, ItemCode: p.ItemCode, Quantity: p.Quantity,
		GoldDelta: p.GoldDelta, ContextGroupID: p.ContextGroupID, Quality: p.Quality, Mutation: p.Mutation,
		CreatedAt: p.CreatedAt}
	args, err := p.args()
	if err != nil {
		return rec, err
	}
	if p.Defer {
		if buf, ok := db.Deferred(t, deferKey{}, func() *pending { return &pending{} }); ok {
			buf.rows = append(buf.rows, args)
			return rec, nil
		}
	}
	if err := t.FlushWrites(ctx); err != nil { // 維持寫入順序：之前累積的先寫
		return rec, err
	}
	var id int64
	if err := t.QueryRowNoFlush(ctx, insertSQL+" RETURNING id", args...).Scan(&id); err != nil {
		return rec, err
	}
	rec.ID = &id
	return rec, nil
}

// RecentForFarm 農場最近的交易紀錄（limit 預設 20）。
func RecentForFarm(ctx context.Context, t *db.Tx, farmID int64, limit int) ([]models.GameTransaction, error) {
	return db.QueryStructs[models.GameTransaction](ctx, t, `
		SELECT * FROM game_transactions WHERE farm_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, farmID, limit)
}
