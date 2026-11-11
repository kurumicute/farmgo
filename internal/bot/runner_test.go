package bot

// 對應 Python tests/test_discord_bot.py 的 test_retrying_service_only_retries_deadlocks 與 player_message 的斷言；
// test_database_outage_reports_safe_message 以實際連不上的資料庫確認訊息不含連線資訊。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/db"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/services"
)

func pgErr(code string) error { return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code}) }

func TestRunnerOnlyRetriesDeadlocks(t *testing.T) {
	r := NewRunner(2)
	r.Retrier = services.Retrier{Sleep: func(time.Duration) {}}
	calls := 0
	got, err := Call(context.Background(), r, "deadlocked", func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", pgErr("40P01")
		}
		return "ok", nil
	})
	if err != nil || got != "ok" || calls != 3 {
		t.Fatal(got, err, calls)
	}
	calls = 0
	if _, err := Call(context.Background(), r, "broken", func(context.Context) (int, error) { calls++; return 0, pgErr("23505") }); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
	calls = 0
	if _, err := Call(context.Background(), r, "serialization", func(context.Context) (int, error) { calls++; return 0, pgErr("40001") }); err == nil || calls != services.RetryAttempts {
		t.Fatal(err, calls)
	}
}

func TestPlayerMessage(t *testing.T) {
	cases := map[error]string{
		gameerr.Validationf("金幣不足。"):                     "金幣不足。",
		pgErr("40P01"):                                   MsgBusy,
		fmt.Errorf("x: %w", db.ErrPoolTimeout):           MsgBusy,
		&pgconn.ConnectError{}:                           MsgDBDown,
		pgErr("08006"):                                   MsgDBDown,
		pgErr("57P01"):                                   MsgDBDown,
		errors.New("SELECT secret FROM internal"):        MsgUnexpected,
		fmt.Errorf("boom: %w", errors.New("password=x")): MsgUnexpected,
	}
	for err, want := range cases {
		if got := PlayerMessage(err); got != want {
			t.Errorf("%v：%q，應為 %q", err, got, want)
		}
	}
}

func TestDatabaseOutageReportsSafeMessage(t *testing.T) {
	// 連不上的資料庫（Python make_engine("postgresql+psycopg://farmgm:x@0.0.0.0:1/none_test")）
	ctx := context.Background()
	d, err := db.Open(ctx, "postgresql://farmgm:x@127.0.0.1:1/none_test?connect_timeout=2", db.PoolOptions{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	err = d.InTx(ctx, db.TxOptions{}, func(*db.Tx) error { return nil })
	msg := PlayerMessage(err)
	if msg != MsgDBDown || strings.Contains(msg, "127.0.0.1") {
		t.Fatal(err, msg)
	}
}

func TestRunnerLimitsConcurrency(t *testing.T) {
	r := NewRunner(2)
	var running, peak atomic.Int32
	done := make(chan struct{})
	for range 6 {
		go func() {
			_ = r.Do(context.Background(), func(context.Context) error {
				n := running.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				running.Add(-1)
				return nil
			})
			done <- struct{}{}
		}()
	}
	for range 6 {
		<-done
	}
	if peak.Load() != 2 {
		t.Fatal(peak.Load())
	}
	// 等待空位時 ctx 取消
	block := make(chan struct{})
	for range 2 {
		go func() { _ = r.Do(context.Background(), func(context.Context) error { <-block; return nil }) }()
	}
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Do(ctx, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(block)
}
