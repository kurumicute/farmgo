package transactions_test

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/repositories/transactions"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func ptr[T any](v T) *T { return &v }

func TestAddAndDefer(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "甲", 0, 1)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		rec, err := transactions.Add(ctx, tx, transactions.AddParams{FarmID: f.FarmID, EventType: "create_farm",
			Description: "建立", CreatedAt: now, GoldDelta: 500})
		if err != nil || rec.ID == nil || *rec.ID != 1 {
			t.Fatal(rec, err)
		}
		err = tx.Bulk(ctx, func() error {
			for i := 0; i < 3; i++ {
				r, err := transactions.Add(ctx, tx, transactions.AddParams{Defer: true, FarmID: f.FarmID, EventType: "harvest",
					Description: "收成", CreatedAt: now.Add(time.Duration(i) * time.Second), ItemCode: ptr("wheat"), Quantity: 2,
					Quality: ptr("normal"), Mutation: ptr("none"),
					AppliedEvents: []models.AppliedEvent{{ID: 1, Name: "豐收"}}})
				if err != nil {
					return err
				}
				if r.ID != nil {
					t.Fatal("deferred record has id")
				}
			}
			// 不延後的寫入：先送出之前累積的（ID 順序不變）
			r, err := transactions.Add(ctx, tx, transactions.AddParams{FarmID: f.FarmID, EventType: "sell",
				Description: "賣", CreatedAt: now.Add(time.Minute), ItemCode: ptr("wheat"), Quantity: 1, GoldDelta: 11,
				UnitPrice: ptr(11)})
			if err != nil || *r.ID != 5 {
				t.Fatal(r, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		recent, err := transactions.RecentForFarm(ctx, tx, f.FarmID, 20)
		if err != nil || len(recent) != 5 || recent[0].EventType != "sell" || recent[4].EventType != "create_farm" {
			t.Fatal(recent, err)
		}
		h := recent[1]
		if h.ID != 4 || len(h.AppliedEvents) != 1 || h.AppliedEvents[0].Name != "豐收" || *h.Quality != "normal" {
			t.Fatalf("%+v", h)
		}
		if recent[4].AppliedEvents != nil {
			t.Fatal("NULL applied_events")
		}
		return nil
	})
}
