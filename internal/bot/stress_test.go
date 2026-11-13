package bot

// Discord 面板壓力測試（對應 Python scripts/bot_stresstest.py）：模擬大量玩家同時用 /farm 面板操作，
// 經過與正式環境相同的 Runner、Registry（1.5 秒內直接回覆、否則自動 defer）與連線池大小。
//
// 每位玩家：開面板 → 選作物種滿農田 → 全部澆水 → 回總覽 → 商店選種子 → 買 5 → 排行榜 → 賭場（與 Python 相同）。
// 量測每次互動「完成畫面更新」的時間、首次回應時間、錯誤畫面、資料一致性與要送到 Discord 的資料量。
//
// 只能對 _test 資料庫執行，會清空其中的遊戲資料（與 Python 腳本相同的 TRUNCATE）。預設略過：
//
//	FARMGM_STRESS=1 go test -p 1 -run TestStressBotPanel -v ./internal/bot
//
// 可調參數：FARMGM_STRESS_USERS（同時玩家數，預設 100）、FARMGM_STRESS_WORKERS（BOT_WORKER_THREADS，預設 16）。
// FARMGM_STRESS_LAB=1（選用，Go 版新增）：每位玩家在原本 10 次互動後再操作種子實驗室 8 次（實驗室頁 → 建造 → 確認建造 →
// 鑑定 ×2 → 雜交頁 → 選母本 → 選父本 → 開始培育）；開始前以管理員調整（有交易紀錄）把每位玩家設為 Lv.5、10 萬金幣，鑑定的是前面在商店買的胡蘿蔔種子。
// FARMGM_STRESS_REDIS_URL（選用）：啟用 cachebus，目錄快取比照正式環境從 10 秒拉長到 cachebus.Fallback。
// 連線池 = 工作數 + 2 + 5（正式環境 PoolSize(workers+2) 的預設值，與 Python pool_size=WORKERS+2、max_overflow=5 相同）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/cachebus"
	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/services"
)

// stressInteraction 計時用的模擬互動：記錄首次回應與畫面更新完成的時間，以及要上傳的資料量。
type stressInteraction struct {
	s           *stressStats
	reg         *ui.Registry
	user, guild string
	attached    []string

	mu          sync.Mutex
	responded   bool
	start       time.Time
	first, done time.Time
	embed       *ui.Embed
}

func (f *stressInteraction) UserID() string               { return f.user }
func (f *stressInteraction) UserName() string             { return "玩家" + f.user[len(f.user)-5:] }
func (f *stressInteraction) AvatarURL() string            { return "" }
func (f *stressInteraction) GuildID() string              { return f.guild }
func (f *stressInteraction) GuildName() string            { return "壓測伺服器" + f.guild[len(f.guild)-2:] }
func (f *stressInteraction) MessageAttachments() []string { return f.attached }

func (f *stressInteraction) Responded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.responded
}

func (f *stressInteraction) markFirst() {
	f.responded = true
	if f.first.IsZero() {
		f.first = time.Now()
	}
}

func (f *stressInteraction) finish(m ui.Message) {
	f.done = time.Now()
	if m.Embed != nil {
		f.embed = m.Embed
	}
	if m.View != nil {
		f.reg.Bind(m.View)
	}
	f.s.upload(m)
}

func (f *stressInteraction) SendMessage(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	f.finish(m)
	return nil
}

func (f *stressInteraction) SendModal(ctx context.Context, m *ui.Modal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	f.reg.BindModal(ctx, m)
	return nil
}

func (f *stressInteraction) Defer(context.Context, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	return nil
}

func (f *stressInteraction) DeferUpdateIfPending(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	return nil
}

func (f *stressInteraction) EditMessage(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	f.finish(m)
	return nil
}

func (f *stressInteraction) Show(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFirst()
	f.finish(m)
	return nil
}

func (f *stressInteraction) EditOriginal(_ context.Context, m ui.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish(m)
	return nil
}

func (f *stressInteraction) DeleteOriginal(context.Context) error { return nil }

func (f *stressInteraction) Followup(_ context.Context, m ui.Message) (ui.SentMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish(m)
	return stressSent{}, nil
}

type stressSent struct{}

func (stressSent) Edit(context.Context, ui.Message) error { return nil }

var _ ui.Interaction = (*stressInteraction)(nil)

// stressStats 彙總結果（Python LAT、FIRST、ERRORS、UPLOAD）。
type stressStats struct {
	mu                                    sync.Mutex
	order                                 []string
	lat                                   map[string][]float64
	first                                 []float64
	errors                                []string
	messages, embedBytes, images, imgByte int
}

func (s *stressStats) upload(m ui.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages++
	if m.Embed != nil {
		data, _ := json.Marshal(m.Embed) // 欄位名稱與 Discord JSON 不同，大小只是估計（Python 用 embed.to_dict()）
		s.embedBytes += len(data)
	}
	files := m.Files
	if m.Attachments != nil {
		files = append(files, m.Attachments.Files...)
	}
	for _, f := range files {
		s.images++
		s.imgByte += len(f.Data)
	}
}

func (s *stressStats) record(step string, it *stressInteraction) {
	it.mu.Lock()
	end := it.done
	if end.IsZero() {
		end = time.Now()
	}
	first, embed := it.first, it.embed
	it.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lat[step]; !ok {
		s.order = append(s.order, step)
	}
	s.lat[step] = append(s.lat[step], float64(end.Sub(it.start).Microseconds())/1000)
	if !first.IsZero() {
		s.first = append(s.first, float64(first.Sub(it.start).Microseconds())/1000)
	}
	if embed != nil && strings.HasPrefix(embed.Title, "⚠️") {
		s.errors = append(s.errors, step+"："+embed.Description)
	}
}

func pctile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := slices.Clone(xs)
	slices.Sort(ys)
	return ys[min(len(ys)-1, int(float64(len(ys))*p))]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var t float64
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

func TestStressBotPanel(t *testing.T) {
	if os.Getenv("FARMGM_STRESS") == "" {
		t.Skip("壓力測試：設 FARMGM_STRESS=1 才執行")
	}
	url := dbtest.URL()
	if url == "" {
		t.Skip(dbtest.EnvVar + " 未設定或資料庫名稱不是以 _test 結尾")
	}
	users, workers := envInt("FARMGM_STRESS_USERS", 100), envInt("FARMGM_STRESS_WORKERS", 16)
	const guilds = 20
	ctx := context.Background()
	d, err := db.Open(ctx, url, db.PoolOptions{MaxConns: workers + 2 + 5, PoolTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fmt.Printf("===== Discord 面板壓力測試（Go）：%d 位玩家同時操作，工作數 %d，連線池 %d =====\n", users, workers, workers+7)

	// 與 Python 腳本相同：清空遊戲資料、所有作物上架（不改目錄的其他設定，季節與生長時間維持 Migration 的值）
	if err := d.InTx(ctx, db.TxOptions{}, func(tx *db.Tx) error {
		if _, err := tx.Exec(ctx, "TRUNCATE ranking_awards, ranking_settlements, ranking_reward_rules, game_events, competition_awards, "+
			"competition_results, competition_contributions, competitions, quest_claims, quest_contributions, quests, "+
			"admin_actions, craft_orders, farm_animals, farm_buildings, farm_defenses, steal_records, group_members, "+
			"groups, gamble_prizes, farm_daily_stats, farm_stats, farm_effects, farm_tools, game_transactions, "+
			"inventory_items, farm_plots, farms, players RESTART IDENTITY CASCADE"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE crop_types SET active = true")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db.CatalogClear()

	// 目錄快取比照正式環境：沒有 Redis 時保存 10 秒；設 FARMGM_STRESS_REDIS_URL 時啟用 cachebus（保存 cachebus.Fallback）
	mode := fmt.Sprintf("無 Redis，目錄快取 %v", cachebus.DefaultTTL)
	if redisURL := os.Getenv("FARMGM_STRESS_REDIS_URL"); redisURL != "" {
		bus, err := cachebus.Start(ctx, redisURL, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		defer bus.Close()
		mode = fmt.Sprintf("Redis 啟用，目錄快取 %v", cachebus.Fallback)
	}
	fmt.Println(mode)

	svc := services.NewAdminService(d, services.Options{})
	t0 := time.Now()
	type who struct{ uid, gid string }
	list := make([]who, users)
	for i := range users {
		gid := strconv.FormatInt(100_000_000_000_000_000+int64(i%guilds), 10)
		uid := strconv.FormatInt(500_000_000_000_000_000+int64(i), 10)
		if _, err := svc.StartExternalFarm(ctx, Platform, uid, fmt.Sprintf("玩家%d", i), gid,
			fmt.Sprintf("壓測伺服器%d", i%guilds), ""); err != nil {
			t.Fatal(err)
		}
		list[i] = who{uid, gid}
	}
	fmt.Printf("建立 %d 位玩家：%.1f 秒\n", users, time.Since(t0).Seconds())
	lab := os.Getenv("FARMGM_STRESS_LAB") != ""
	if lab {
		t1 := time.Now()
		level, gold := 5, 100_000
		for _, w := range list {
			ec, err := svc.ExternalContext(ctx, Platform, w.uid, &w.gid, "")
			if err != nil || ec.PlayerID == nil {
				t.Fatal("找不到壓測玩家", err)
			}
			if _, err := svc.SetFarmValues(ctx, "stress", *ec.PlayerID, &gold, &level); err != nil {
				t.Fatal(err)
			}
		}
		fmt.Printf("種子實驗室情境：設定 Lv.5 與金幣 %.1f 秒\n", time.Since(t1).Seconds())
	}

	reg := ui.NewRegistry() // DirectReply 1.5 秒，與正式環境相同
	cmds := NewCommands(svc, NewRunner(workers), reg)
	var panels sync.Map
	cmds.onPanel = func(p *Panel) { panels.Store(p.who.UserID, p) }
	st := &stressStats{lat: map[string][]float64{}}

	newIt := func(w who, attached []string) *stressInteraction {
		return &stressInteraction{s: st, reg: reg, user: w.uid, guild: w.gid, attached: attached, start: time.Now()}
	}
	player := func(w who) {
		it := newIt(w, nil)
		cmds.OpenPanel(ctx, it)
		st.record("開啟面板", it)
		v, ok := panels.Load(w.uid)
		if !ok {
			st.mu.Lock()
			st.errors = append(st.errors, "開啟面板失敗")
			st.mu.Unlock()
			return
		}
		p := v.(*Panel)
		click := func(label, step string) {
			for _, b := range p.View.Buttons() {
				if strings.Contains(b.Label, label) {
					i := newIt(w, nil)
					reg.DispatchComponent(ctx, i, b.CustomID(), nil)
					st.record(step, i)
					return
				}
			}
		}
		choose := func(placeholder, value, step string) {
			for _, s := range p.View.Selects() {
				if strings.Contains(s.Placeholder, placeholder) {
					i := newIt(w, nil)
					reg.DispatchComponent(ctx, i, s.CustomID(), []string{value})
					st.record(step, i)
					return
				}
			}
		}
		goPage := func(page, step string) {
			i := newIt(w, nil)
			reg.RunFor(ctx, i, p.View, func(ctx context.Context) error {
				p.goTo(ctx, i, page, "")
				return nil
			})
			st.record(step, i)
		}
		goPage("plots", "切換頁面")
		choose("選擇作物", "wheat", "種滿農田（多筆交易）")
		click("全部澆水", "全部澆水（多筆交易）")
		goPage("main", "切換頁面")
		goPage("shop", "切換頁面")
		choose("選擇要買的商品", "seed:carrot", "選擇商品")
		click("買 5", "購買")
		goPage("board", "排行榜")
		goPage("casino", "切換頁面")
		if !lab {
			return
		}
		// 種子實驗室（Go 版新增）
		chooseNth := func(placeholder string, n int, step string) {
			for _, s := range p.View.Selects() {
				if strings.Contains(s.Placeholder, placeholder) && len(s.Options) > n {
					choose(placeholder, s.Options[n].Value, step)
					return
				}
			}
		}
		goPage("lab", "實驗室：切換頁面")
		click("建造實驗室", "實驗室：建造（第一次按）")
		click("確認建造", "實驗室：建造")
		choose("鑑定一般種子", "carrot", "實驗室：鑑定") // 前面在商店買的 5 顆胡蘿蔔種子
		choose("鑑定一般種子", "carrot", "實驗室：鑑定")
		goPage("lab_cross", "實驗室：切換頁面")
		chooseNth("選擇母本", 0, "實驗室：選種子")
		chooseNth("選擇父本", 0, "實驗室：選種子")
		click("開始培育", "實驗室：開始培育")
	}

	// CPU、記憶體取樣（每 0.5 秒；CPU 以本程序累計 CPU 時間計算，100% = 1 核）
	var cpuSamples, memSamples []float64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	mem0 := float64(ms.Sys) / (1 << 20)
	go func() {
		defer close(sampled)
		lastCPU, lastT := processCPU(), time.Now()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				c, now := processCPU(), time.Now()
				cpuSamples = append(cpuSamples, float64(c-lastCPU)/float64(now.Sub(lastT))*100)
				lastCPU, lastT = c, now
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				memSamples = append(memSamples, float64(m.Sys)/(1<<20))
			}
		}
	}()

	cpu0 := processCPU()
	t0 = time.Now()
	var wg sync.WaitGroup
	for _, w := range list {
		wg.Add(1)
		go func() { defer wg.Done(); player(w) }()
	}
	wg.Wait()
	elapsed := time.Since(t0)
	cpuTotal := processCPU() - cpu0
	close(stop)
	<-sampled

	total := 0
	for _, xs := range st.lat {
		total += len(xs)
	}
	fmt.Printf("\n%d 次互動，%.1f 秒（%.0f 次/秒）\n", total, elapsed.Seconds(), float64(total)/elapsed.Seconds())
	fmt.Printf("首次回應（defer）：P95 %.1f ms、最大 %.1f ms（Discord 限制 3000 ms）\n", pctile(st.first, .95), slices.Max(append(st.first, 0)))
	fmt.Println("各步驟「畫面更新完成」時間（ms）：")
	for _, step := range st.order {
		xs := st.lat[step]
		over := 0
		for _, x := range xs {
			if x > 3000 {
				over++
			}
		}
		fmt.Printf("  %-16s n=%-5d 平均 %7.0f  P50 %7.0f  P95 %7.0f  P99 %7.0f  最大 %7.0f  超過 3 秒 %d\n",
			step, len(xs), mean(xs), pctile(xs, .5), pctile(xs, .95), pctile(xs, .99), slices.Max(xs), over)
	}
	if len(st.errors) > 0 {
		fmt.Printf("錯誤畫面：%d（例如：%s）\n", len(st.errors), st.errors[0])
	} else {
		fmt.Println("錯誤畫面：0")
	}
	fmt.Printf("Bot 程序 CPU：平均 %.0f%%、峰值 %.0f%%（100%% = 1 核，本機 %d 核）；Go 執行期記憶體（Sys）：開始 %.0f MB、峰值 %.0f MB\n",
		float64(cpuTotal)/float64(elapsed)*100, slices.Max(append(cpuSamples, 0)), runtime.NumCPU(), mem0, slices.Max(append(memSamples, mem0)))
	mb := float64(st.embedBytes+st.imgByte) / (1 << 20)
	fmt.Printf("送往 Discord 的內容：%d 則訊息更新，Embed %.1f MB、圖片 %d 張 %.1f MB（平均 %.0f KB/張）；合計 %.1f MB，平均 %.1f Mbps 上傳\n",
		st.messages, float64(st.embedBytes)/(1<<20), st.images, float64(st.imgByte)/(1<<20),
		float64(st.imgByte)/float64(max(1, st.images))/1024, mb, mb*8/elapsed.Seconds())

	var gold, ledger, conns int64
	var check farmstats.CheckResult
	if err := d.InTx(ctx, db.TxOptions{ReadOnly: true}, func(tx *db.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT coalesce(sum(gold), 0) FROM farms").Scan(&gold); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT coalesce(sum(gold_delta), 0) FROM game_transactions").Scan(&ledger); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()").Scan(&conns); err != nil {
			return err
		}
		var err error
		check, err = farmstats.Check(ctx, tx, 20)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("一致性：金幣 = 交易紀錄 %v；統計表不一致 %d/%d；結束時資料庫連線 %d\n",
		gold == ledger, check.FarmStatsMismatches, check.FarmDailyStatsMismatches, conns)
	if gold != ledger || check.FarmStatsMismatches != 0 || check.FarmDailyStatsMismatches != 0 {
		t.Error("資料不一致")
	}
}
