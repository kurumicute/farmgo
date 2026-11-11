package services

// 對應 Python tests/test_batch.py：RunBatch（面板一次讀取整頁資料時，多個服務呼叫共用同一筆交易），
// 以及 tests/test_discord_bot.py 中 RetryingService 不需要 Discord 的部分。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/game/gameerr"
)

func dbError(state string) error { return &pgconn.PgError{Code: state} }

// begins 目前為止開始的交易數（Python 測試監聽 engine 的 begin 事件；Go 以連線池的取得次數計算，每筆交易取得一次連線）。
func (e *env) begins() int64 { return e.d.Pool.Stat().AcquireCount() }

var noSleep = Retrier{Sleep: func(time.Duration) {}}

func TestBatchUsesOneTransaction(t *testing.T) {
	e := setup(t)
	pid := e.player()
	before := e.begins()
	must[FarmStatus](t)(e.s.GetFarmStatus(e.ctx, pid))
	must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if n := e.begins() - before; n != 2 { // 不在共用交易中：各開一筆
		t.Fatal(n)
	}
	group := int64(1)
	var status FarmStatus
	var prices []CropInfo
	before = e.begins()
	if err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		var err error
		if status, err = e.s.GetFarmStatus(ctx, pid); err != nil {
			return err
		}
		if _, err = e.s.GetBackpack(ctx, pid); err != nil {
			return err
		}
		if _, err = e.s.ActiveEvents(ctx); err != nil {
			return err
		}
		prices, err = e.s.InGroupContext(&group).GetMarketPrices(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.begins() - before; n != 1 || len(status.Plots) == 0 || len(prices) == 0 {
		t.Fatal(n, len(status.Plots), len(prices))
	}
	if e.s.InBatch(e.ctx) {
		t.Fatal("RunBatch 結束後不應仍在共用交易中")
	}
}

func TestBatchErrorRollsBackEverything(t *testing.T) {
	e := setup(t)
	pid := e.player()
	gold := e.gold(pid)
	err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		if _, err := e.s.BuySeeds(ctx, pid, "wheat", 1); err != nil {
			return err
		}
		return gameerr.Validationf("中途失敗")
	})
	wantKind(t, err, gameerr.Validation)
	if e.gold(pid) != gold { // 前面的購買也一起回滾
		t.Fatal(e.gold(pid), gold)
	}
}

func TestBatchSwallowedErrorIsNotCommitted(t *testing.T) {
	e := setup(t)
	pid := e.player()
	gold := e.gold(pid)
	err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		if _, err := e.s.BuySeeds(ctx, pid, "wheat", 1); err != nil {
			return err
		}
		if _, err := e.s.HarvestCrop(ctx, pid, 1); err == nil { // 空地：在交易中才發現錯誤
			t.Fatal("空地不應能收成")
		}
		return nil // 吃掉錯誤
	})
	// Python 拋出 RuntimeError（非遊戲錯誤）
	if _, isGame := gameerr.As(err); err == nil || isGame {
		t.Fatal(err)
	}
	if e.gold(pid) != gold {
		t.Fatal(e.gold(pid), gold)
	}
}

func TestBatchRetriesWholeFunctionOnDeadlock(t *testing.T) {
	e := setup(t)
	pid := e.player()
	calls := 0
	err := noSleep.Do(e.ctx, "RunBatch", func(ctx context.Context) error {
		return e.s.RunBatch(ctx, func(ctx context.Context) error {
			calls++
			// 共用交易中：內層不自行重試
			if _, err := Retry(ctx, noSleep, "GetFarmStatus", func(ctx context.Context) (FarmStatus, error) {
				return e.s.GetFarmStatus(ctx, pid)
			}); err != nil {
				return err
			}
			if calls == 1 {
				return dbError("40P01")
			}
			return nil
		})
	})
	if err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}

// 在共用交易中，內層的 Retrier 遇到死結不重試，直接交給外層。
func TestRetrierInsideBatchDoesNotRetry(t *testing.T) {
	e := setup(t)
	inner := 0
	err := e.s.RunBatch(e.ctx, func(ctx context.Context) error {
		return noSleep.Do(ctx, "x", func(context.Context) error {
			inner++
			return dbError("40P01")
		})
	})
	if sqlState(err) != "40P01" || inner != 1 {
		t.Fatal(err, inner)
	}
}

// 對應 test_discord_bot.py 的 RetryingService 部分（不需要資料庫）。
func TestRetrierRetriesOnlyDeadlockAndSerialization(t *testing.T) {
	ctx := context.Background()
	calls := 0
	got, err := Retry(ctx, noSleep, "deadlocked", func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", dbError("40P01")
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls != 3 {
		t.Fatal(got, err, calls)
	}
	calls = 0
	if err := noSleep.Do(ctx, "broken", func(context.Context) error { calls++; return dbError("23505") }); sqlState(err) != "23505" || calls != 1 {
		t.Fatal(err, calls)
	}
	calls = 0
	if err := noSleep.Do(ctx, "serialization", func(context.Context) error { calls++; return dbError("40001") }); sqlState(err) != "40001" || calls != RetryAttempts {
		t.Fatal(err, calls)
	}
	// 遊戲規則錯誤不重試
	calls = 0
	if err := noSleep.Do(ctx, "game", func(context.Context) error { calls++; return gameerr.Validationf("x") }); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
	// 預設等待可被 ctx 取消
	cctx, cancel := context.WithCancel(ctx)
	calls = 0
	err = Retrier{}.Do(cctx, "cancel", func(context.Context) error { calls++; cancel(); return dbError("40P01") })
	if !errors.Is(err, context.Canceled) || calls != 1 || !strings.Contains(err.Error(), "canceled") {
		t.Fatal(err, calls)
	}
}
