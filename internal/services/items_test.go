package services

// 對應 Python tests/test_items.py：澆水、乾枯、道具、永久工具、限時效果與背包。

import (
	"strings"
	"sync"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/dbtest"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
)

func plotOn(t *testing.T, s *GameService, e *env, pid int64, n int) PlotInfo {
	t.Helper()
	return must[FarmStatus](t)(s.GetFarmStatus(e.ctx, pid)).Plots[n-1]
}

func growOn(t *testing.T, s *GameService, e *env, pid int64, plot int, crop string) {
	t.Helper()
	e.ok(s.BuySeeds(e.ctx, pid, crop, 1))
	e.ok(s.PlantCrop(e.ctx, pid, plot, crop))
	e.ok(s.WaterCrop(e.ctx, pid, plot))
}

// restart 模擬程式重新啟動：全新的連線池與服務實例（Python make_engine + SocialService）。
func (e *env) restart() *SocialService {
	e.t.Helper()
	d, err := db.Open(e.ctx, dbtest.URL(), db.PoolOptions{MaxConns: 2})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(d.Close)
	return NewSocialService(d, Options{Clock: e.clock})
}

func intp(n int) *int { return &n }

func TestWitheredCropCannotBeHarvestedAndCanBeCleared(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(60 + balance.WitherSeconds)
	if e.plot(pid, 1).State != rules.PlotWithered {
		t.Fatal(e.plot(pid, 1).State)
	}
	_, err := e.s.HarvestCrop(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotWithered)
	if m := e.ok(e.s.ClearWithered(e.ctx, pid, 1)).Message; !strings.Contains(m, "恢復為空地") {
		t.Fatal(m)
	}
	if e.plot(pid, 1).State != rules.PlotEmpty {
		t.Fatal(e.plot(pid, 1).State)
	}
	_, err = e.s.ClearWithered(e.ctx, pid, 1)
	wantKind(t, err, gameerr.Validation)
}

func TestFertilizerReducesTimeOnce(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.Buy(e.ctx, pid, "化肥", 2))
	if g := e.gold(pid); g != 500-2*balance.Items[balance.ItemFertilizer].Price {
		t.Fatal(g)
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "strawberry", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "strawberry"))
	_, err := e.s.UseItem(e.ctx, pid, "fertilizer", intp(1)) // 等待澆水時不能施肥
	wantKind(t, err, gameerr.Validation)
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	e.clock.Advance(200) // 剩 400 秒
	e.ok(e.s.UseItem(e.ctx, pid, "fertilizer", intp(1)))
	if p := e.plot(pid, 1); p.RemainingSeconds != 340 || !p.Fertilized { // 剩餘時間減少 15%
		t.Fatalf("%+v", p)
	}
	_, err = e.s.UseItem(e.ctx, pid, "fertilizer", intp(1)) // 同一株不可重複施肥
	wantKind(t, err, gameerr.Validation)
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if len(bp.Consumables) != 1 || bp.Consumables[0].Code != "fertilizer" || bp.Consumables[0].Quantity != 1 {
		t.Fatalf("%+v", bp.Consumables)
	}
	e.clock.Advance(339)
	if e.plot(pid, 1).State != rules.PlotGrowing {
		t.Fatal(e.plot(pid, 1).State)
	}
	e.clock.Advance(1)
	if e.plot(pid, 1).State != rules.PlotReady {
		t.Fatal(e.plot(pid, 1).State)
	}
}

func TestFertilizerRequiresItem(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	_, err := e.s.UseItem(e.ctx, pid, "fertilizer", intp(1))
	wantKind(t, err, gameerr.InsufficientItems)
}

func TestEarthwormEffectLifecycleAndRestart(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 10_000)
	e.ok(e.s.Buy(e.ctx, pid, "蚯蚓", 2))
	if eff := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Effects[0]; eff.Status != "inactive" {
		t.Fatalf("%+v", eff)
	}
	e.ok(e.s.UseItem(e.ctx, pid, "earthworm", nil))
	start := e.clock.Now()
	e.grow(pid, 1, "wheat")
	if r := e.plot(pid, 1).RemainingSeconds; r != 51 { // 60 × 0.85
		t.Fatal(r)
	}

	// 程式重新啟動：新的連線池與服務，效果期限不重設
	e.clock.Advance(600)
	rs := e.restart().GameService
	effect := func() EffectStatus { return must[Backpack](t)(rs.GetBackpack(e.ctx, pid)).Effects[0] }
	if eff := effect(); eff.Status != "active" || eff.StartedAt == nil || !eff.StartedAt.Equal(start) ||
		eff.RemainingSeconds != balance.EarthwormDurationSeconds-600 {
		t.Fatalf("%+v", eff)
	}
	e.ok(rs.UseItem(e.ctx, pid, "earthworm", nil)) // 有效期間再次使用：延長
	if eff := effect(); eff.RemainingSeconds != 2*balance.EarthwormDurationSeconds-600 {
		t.Fatalf("%+v", eff)
	}
	e.clock.Advance(2 * balance.EarthwormDurationSeconds)
	if eff := effect(); eff.Status != "expired" || eff.RemainingSeconds != 0 {
		t.Fatalf("%+v", eff)
	}
	e.ok(rs.ClearWithered(e.ctx, pid, 1)) // 這麼久沒收成，小麥已乾枯
	growOn(t, rs, e, pid, 1, "wheat")
	if r := plotOn(t, rs, e, pid, 1).RemainingSeconds; r != 60 { // 到期後不再加速
		t.Fatal(r)
	}
}

func TestEarthwormSpeedsUpCropsAlreadyGrowing(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 10_000)
	e.ok(e.s.Buy(e.ctx, pid, "蚯蚓", 2))
	e.grow(pid, 1, "wheat") // 60 秒
	e.clock.Advance(20)
	if m := e.ok(e.s.UseItem(e.ctx, pid, "earthworm", nil)).Message; !strings.Contains(m, "正在生長的 1 格") {
		t.Fatal(m)
	}
	if r := e.plot(pid, 1).RemainingSeconds; r != 34 { // 剩 40 秒 × 0.85
		t.Fatal(r)
	}
	e.ok(e.s.UseItem(e.ctx, pid, "earthworm", nil)) // 有效期間延長：不重複加速
	if r := e.plot(pid, 1).RemainingSeconds; r != 34 {
		t.Fatal(r)
	}
	// 效果結束後重新啟用：只加速上次效果結束後才澆水的作物
	e.clock.Advance(2 * balance.EarthwormDurationSeconds)
	e.ok(e.s.ClearWithered(e.ctx, pid, 1))
	e.grow(pid, 1, "wheat")
	e.grow(pid, 2, "wheat")
	e.clock.Advance(10)
	e.ok(e.s.Buy(e.ctx, pid, "蚯蚓", 1))
	if m := e.ok(e.s.UseItem(e.ctx, pid, "earthworm", nil)).Message; !strings.Contains(m, "正在生長的 2 格") {
		t.Fatal(m)
	}
	if r := e.plot(pid, 1).RemainingSeconds; r != 43 { // 剩 50 秒 × 0.85 = 42.5（無條件進位）
		t.Fatal(r)
	}
	if n := e.scalar("SELECT count(*) FROM game_transactions WHERE item_code = 'earthworm' AND description LIKE '%，生長中的 2 格一起加速'"); n != 1 {
		t.Fatal(n)
	}
}

func TestAutoWatererPermanentNoDoubleCharge(t *testing.T) {
	e := setup(t)
	pid := e.player()
	_, err := e.s.Buy(e.ctx, pid, "auto_waterer", 1)
	wantKind(t, err, gameerr.InsufficientGold)
	e.setGold(pid, balance.Items[balance.ToolAutoWaterer].Price+200)
	_, err = e.s.Buy(e.ctx, pid, "auto_waterer", 2)
	wantKind(t, err, gameerr.Validation)
	e.ok(e.s.Buy(e.ctx, pid, "自動澆水器", 1))
	if e.gold(pid) != 200 {
		t.Fatal(e.gold(pid))
	}
	_, err = e.s.Buy(e.ctx, pid, "auto_waterer", 1)
	wantKind(t, err, gameerr.AlreadyExists)
	if e.gold(pid) != 200 {
		t.Fatal(e.gold(pid))
	}

	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 2))
	if m := e.ok(e.s.PlantCrop(e.ctx, pid, 1, "wheat")).Message; !strings.Contains(m, "已自動澆水") {
		t.Fatal(m)
	}
	if e.plot(pid, 1).State != rules.PlotGrowing {
		t.Fatal(e.plot(pid, 1).State)
	}

	rs := e.restart().GameService
	if tool := must[Backpack](t)(rs.GetBackpack(e.ctx, pid)).Tools[0]; !tool.Owned || !tool.Enabled {
		t.Fatalf("%+v", tool)
	}
	e.ok(rs.SetToolEnabled(e.ctx, pid, "auto_waterer", false))
	e.ok(rs.PlantCrop(e.ctx, pid, 2, "wheat"))
	if s := plotOn(t, rs, e, pid, 2).State; s != rules.PlotWaitingWater {
		t.Fatal(s)
	}
	if e.gold(pid) != 180 { // 重啟與查詢都沒有額外扣款
		t.Fatal(e.gold(pid))
	}
}

func TestConcurrentToolPurchaseChargesOnce(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 5000)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = e.s.Buy(e.ctx, pid, "auto_waterer", 1)
		}()
	}
	close(start)
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
			wantKind(t, err, gameerr.AlreadyExists)
		}
	}
	if failed != 4 {
		t.Fatal(errs)
	}
	if e.gold(pid) != 5000-balance.Items[balance.ToolAutoWaterer].Price {
		t.Fatal(e.gold(pid))
	}
	if e.scalar("SELECT count(*) FROM farm_tools") != 1 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'buy_item'") != 1 {
		t.Fatal("工具或交易紀錄重複")
	}
}

func TestBackpackContents(t *testing.T) {
	e := setup(t)
	pid := e.player()
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if !bp.IsEmpty() || bp.Used != 0 {
		t.Fatalf("%+v", bp)
	}
	var tools []string
	for _, x := range bp.Tools {
		if x.Owned {
			t.Fatalf("%+v", x)
		}
		tools = append(tools, x.Name)
	}
	if strings.Join(tools, ",") != "自動澆水器,自動播種機,自動販售機,傳說扒手手套" {
		t.Fatal(tools)
	}

	e.setGold(pid, 10_000)
	e.ok(e.s.BuySeeds(e.ctx, pid, "carrot", 3))
	e.ok(e.s.Buy(e.ctx, pid, "fertilizer", 2))
	e.ok(e.s.Buy(e.ctx, pid, "auto_waterer", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "carrot")) // 自動澆水
	e.clock.Advance(180)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))

	bp = must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	type nqp struct {
		name       string
		qty, price int
	}
	lines := func(ls []BackpackLine) []nqp {
		var out []nqp
		for _, l := range ls {
			out = append(out, nqp{l.Name, l.Quantity, l.Price})
		}
		return out
	}
	if bp.IsEmpty() {
		t.Fatal("背包不應是空的")
	}
	if s := lines(bp.Seeds); len(s) != 1 || s[0] != (nqp{"胡蘿蔔種子", 2, 20}) {
		t.Fatal(s)
	}
	if p := lines(bp.Produce); len(p) != 1 || p[0] != (nqp{"胡蘿蔔", 3, 25}) {
		t.Fatal(p)
	}
	if c := bp.Consumables; len(c) != 1 || c[0].Name != "化肥" || c[0].Quantity != 2 || !strings.Contains(c[0].Description, "減少 15%") {
		t.Fatalf("%+v", c)
	}
	if !bp.Tools[0].Owned || !bp.Tools[0].Enabled || bp.Used != 7 {
		t.Fatalf("%+v %d", bp.Tools[0], bp.Used)
	}
}
