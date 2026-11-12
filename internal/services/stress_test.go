package services

// 遊戲服務的並發壓力測試（Go 版新增，Python 沒有）：模擬多名玩家在同一群組同時種植、澆水、收成、偷菜與販售，
// 量測各操作的延遲與死結重試次數，最後檢查金幣、庫存與農場統計的一致性。
//
// 預設略過；要執行時設 FARMGM_STRESS=1（並設好 FARMGM_TEST_DATABASE_URL）：
//
//	FARMGM_STRESS=1 go test -p 1 -run TestStress -v ./internal/services
//
// 可調參數：FARMGM_STRESS_PLAYERS（預設 40）、FARMGM_STRESS_ROUNDS（預設 5）、FARMGM_STRESS_STEALS（每人每輪偷菜次數，預設 3）。
// FARMGM_STRESS_WORKERS 模擬 Bot 同時處理的工作上限（BOT_WORKER_THREADS，0 = 不限制）；連線池大小用 FARMGM_TEST_DB_MAX_CONNS（預設 20）。
//
// 目錄快取比照正式環境：沒有 Redis 時保存 cachebus.DefaultTTL（10 秒）；設 FARMGM_STRESS_REDIS_URL 時啟用 cachebus
// （保存 cachebus.Fallback），並另開一條 Redis 連線模擬管理網站，在壓測期間每 FARMGM_STRESS_PUBLISH_MS（預設 200）毫秒
// 發布一次目錄失效通知，讓快取在負載下被反覆清空。

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"farmgm/internal/cachebus"
	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/gameerr"
)

var stressPlayerName = regexp.MustCompile(`壓測玩家[0-9]+`)

func stressEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type stressStats struct {
	mu        sync.Mutex
	latency   map[string][]time.Duration
	gameErrs  map[string]int
	fatalErrs []error
	retries   atomic.Int64
}

func (s *stressStats) record(op string, d time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency[op] = append(s.latency[op], d)
	if err == nil {
		return
	}
	if ge, ok := gameerr.As(err); ok { // 遊戲規則拒絕（偷光、上限、沒有成熟作物…）是合法結果
		s.gameErrs[op+"："+stressPlayerName.ReplaceAllString(ge.Error(), "壓測玩家N")]++
		return
	}
	s.fatalErrs = append(s.fatalErrs, fmt.Errorf("%s：%w", op, err))
}

func stressPct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	return ds[min(len(ds)-1, int(float64(len(ds))*p))]
}

func TestStressConcurrentFarming(t *testing.T) {
	if os.Getenv("FARMGM_STRESS") == "" {
		t.Skip("壓力測試：設 FARMGM_STRESS=1 才執行")
	}
	players := stressEnvInt("FARMGM_STRESS_PLAYERS", 40)
	rounds := stressEnvInt("FARMGM_STRESS_ROUNDS", 5)
	steals := stressEnvInt("FARMGM_STRESS_STEALS", 3)

	e := setup(t)
	home := e.group("壓測群組")
	pids := make([]int64, players)
	for i := range pids {
		pids[i] = e.farmer(fmt.Sprintf("壓測玩家%d", i), home)
		e.setGoldRecorded(pids[i], 1_000_000)
	}
	plots := len(e.status(pids[0]).Plots)

	// 目錄快取（setup 預設關閉，Cleanup 會還原）
	db.CatalogTTL = cachebus.DefaultTTL
	db.CatalogClear()
	var published atomic.Int64
	stopPublish := func() {}
	redisURL := os.Getenv("FARMGM_STRESS_REDIS_URL")
	if redisURL != "" {
		bus, err := cachebus.Start(e.ctx, redisURL, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(bus.Close) // 先於 setup 的 Cleanup 執行
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			t.Fatal(err)
		}
		admin := redis.NewClient(opt) // 模擬另一個程序（管理網站）
		t.Cleanup(func() { _ = admin.Close() })
		every := time.Duration(stressEnvInt("FARMGM_STRESS_PUBLISH_MS", 200)) * time.Millisecond
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					if admin.Publish(e.ctx, cachebus.Channel, cachebus.KindCatalog+" stress-admin").Err() == nil {
						published.Add(1)
					}
				}
			}
		}()
		stopPublish = func() { close(stop); <-done }
	}
	dbtest.SQL.Start(t)

	st := &stressStats{latency: map[string][]time.Duration{}, gameErrs: map[string]int{}}
	retrier := Retrier{Sleep: func(d time.Duration) { st.retries.Add(1); time.Sleep(d) }}
	// FARMGM_STRESS_WORKERS > 0 時模擬 Bot 的 Runner（BOT_WORKER_THREADS）：同時最多這麼多個服務呼叫，其餘排隊。
	// 延遲含排隊時間（玩家實際感受到的），排隊時間另外統計。
	workers := stressEnvInt("FARMGM_STRESS_WORKERS", 0)
	var sem chan struct{}
	if workers > 0 {
		sem = make(chan struct{}, workers)
	}
	var waitMu sync.Mutex
	var waits []time.Duration
	call := func(ctx context.Context, op string, fn func(ctx context.Context) (ActionResult, error)) {
		start := time.Now()
		if sem != nil {
			sem <- struct{}{}
			defer func() { <-sem }()
			w := time.Since(start)
			waitMu.Lock()
			waits = append(waits, w)
			waitMu.Unlock()
		}
		_, err := Retry(ctx, retrier, op, fn)
		st.record(op, time.Since(start), err)
	}
	// phase 讓每名玩家各自的 goroutine 同時開始執行 fn
	phase := func(fn func(i int, pid int64)) time.Duration {
		begin := make(chan struct{})
		var wg sync.WaitGroup
		for i, pid := range pids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				fn(i, pid)
			}()
		}
		t0 := time.Now()
		close(begin)
		wg.Wait()
		return time.Since(t0)
	}

	var total time.Duration
	for round := range rounds {
		total += phase(func(_ int, pid int64) {
			call(e.ctx, "PlantAll", func(ctx context.Context) (ActionResult, error) { return e.a.PlantAll(ctx, pid, "wheat") })
			call(e.ctx, "WaterAll", func(ctx context.Context) (ActionResult, error) { return e.a.WaterAll(ctx, pid) })
		})
		e.clock.Advance(600)
		total += phase(func(i int, pid int64) {
			rng := rand.New(rand.NewPCG(uint64(round), uint64(i)))
			// 先偷別人（與受害者的 HarvestAll 互相競爭同一個農場列），再收自己的、賣掉
			for range steals {
				victim := pids[rng.IntN(players)]
				if victim == pid {
					continue
				}
				plot := rng.IntN(plots) + 1
				call(e.ctx, "StealCrop", func(ctx context.Context) (ActionResult, error) {
					return e.a.StealCrop(ctx, home, pid, victim, plot, 1)
				})
			}
			call(e.ctx, "HarvestAll", func(ctx context.Context) (ActionResult, error) { return e.a.HarvestAll(ctx, pid) })
			call(e.ctx, "SellCrop", func(ctx context.Context) (ActionResult, error) {
				return e.a.SellCrop(ctx, pid, "wheat", 1, "normal", "none")
			})
		})
		e.clock.Advance(24 * 3600) // 隔天，重置每日偷菜上限
	}
	stopPublish()
	catalogSQL := dbtest.SQL.CountContaining("FROM crop_types")
	allSQL := len(dbtest.SQL.Stop())

	// ---------- 報表 ----------
	ops := 0
	mode := fmt.Sprintf("無 Redis，目錄快取 %v", cachebus.DefaultTTL)
	if redisURL != "" {
		mode = fmt.Sprintf("Redis 啟用，目錄快取 %v，模擬管理網站發布失效通知 %d 次", cachebus.Fallback, published.Load())
	}
	t.Logf("%s", mode)
	if workers > 0 {
		slices.Sort(waits)
		t.Logf("Bot worker 上限 %d：排隊時間 p50 %v  p95 %v  max %v", workers, stressPct(waits, .5).Round(time.Millisecond),
			stressPct(waits, .95).Round(time.Millisecond), waits[len(waits)-1].Round(time.Millisecond))
	}
	t.Logf("玩家 %d、輪數 %d、每人每輪偷菜 %d、土地 %d 格；總耗時 %v", players, rounds, steals, plots, total.Round(time.Millisecond))
	t.Logf("SQL 共 %d 條，其中讀作物目錄 %d 條", allSQL, catalogSQL)
	names := make([]string, 0, len(st.latency))
	for op := range st.latency {
		names = append(names, op)
	}
	slices.Sort(names)
	for _, op := range names {
		ds := st.latency[op]
		slices.Sort(ds)
		ops += len(ds)
		t.Logf("%-10s 次數 %5d  p50 %8v  p95 %8v  p99 %8v  max %8v", op, len(ds),
			stressPct(ds, .5).Round(time.Microsecond), stressPct(ds, .95).Round(time.Microsecond),
			stressPct(ds, .99).Round(time.Microsecond), ds[len(ds)-1].Round(time.Microsecond))
	}
	t.Logf("吞吐量 %.1f ops/s；死結／序列化重試 %d 次", float64(ops)/total.Seconds(), st.retries.Load())
	for msg, n := range st.gameErrs {
		t.Logf("遊戲規則拒絕 ×%d：%s", n, msg)
	}

	// ---------- 一致性檢查 ----------
	if len(st.fatalErrs) > 0 {
		t.Fatalf("非遊戲規則的錯誤 %d 個，例如：%v", len(st.fatalErrs), st.fatalErrs[:min(5, len(st.fatalErrs))])
	}
	if !e.goldMatchesTransactions() {
		t.Error("金幣總和與交易紀錄的 gold_delta 不一致")
	}
	if n := e.scalar("SELECT count(*) FROM farms WHERE gold < 0"); n != 0 {
		t.Errorf("%d 個農場金幣為負", n)
	}
	if n := e.scalar("SELECT count(*) FROM inventory_items WHERE quantity < 0"); n != 0 {
		t.Errorf("%d 筆庫存為負", n)
	}
	sum := func(event string) int {
		return e.scalar("SELECT coalesce(sum(quantity), 0) FROM game_transactions WHERE event_type = $1 AND item_code = 'wheat'", event)
	}
	stock := e.scalar("SELECT coalesce(sum(quantity), 0) FROM inventory_items WHERE item_code = 'wheat'")
	if h, s, sold := sum("harvest"), sum("steal"), sum("sell"); h+s-sold != stock {
		t.Errorf("小麥不守恆：收成 %d + 偷 %d − 賣 %d ≠ 庫存 %d", h, s, sold, stock)
	}
	if c := e.statsCheck(); c.FarmStatsMismatches != 0 {
		t.Errorf("農場統計不一致：%+v", c)
	}
}
