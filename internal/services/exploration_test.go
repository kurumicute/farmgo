package services

// 對應 Python tests/test_exploration.py 的服務部分（純規則測試已在 game/exploration 移植）。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
)

const hour = 3600

// explorer Python 的 explorer 夾具：亂數固定為 0（每次都抽到普通素材的第一項，數量取最小值）。
func (e *env) explorer() int64 {
	e.s.exploreRng = func() float64 { return 0 }
	return e.player()
}

func (e *env) setFarm(pid int64, col string, v any) {
	e.t.Helper()
	e.exec("UPDATE farms SET "+col+" = $2 WHERE player_id = $1", pid, v)
}

func (e *env) giveQ(pid int64, code string, n int, quality string) {
	e.t.Helper()
	e.exec(`INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity)
		SELECT id, $2, $4, 'none', $3 FROM farms WHERE player_id = $1
		ON CONFLICT ON CONSTRAINT uq_inventory_items_farm_item DO UPDATE SET quantity = inventory_items.quantity + $3`,
		pid, code, n, quality)
}

// bpQty 背包中普通品質的數量；種子用 seed_<作物> 代碼查詢。
func (e *env) bpQty(pid int64, code string) int {
	e.t.Helper()
	bp := must[Backpack](e.t)(e.s.GetBackpack(e.ctx, pid))
	if strings.HasPrefix(code, "seed_") {
		for _, x := range bp.Seeds {
			if "seed_"+x.Code == code {
				return x.Quantity
			}
		}
		return 0
	}
	for _, l := range slices.Concat(bp.Materials, bp.Produce, bp.Goods) {
		if l.Code == code && l.Quality == "normal" && l.Mutation == "none" {
			return l.Quantity
		}
	}
	return 0
}

func (e *env) giveMachineMaterials(pid int64, code string) {
	m := exploration.Machines[code]
	for _, c := range m.MaterialOrder {
		e.give(pid, c, m.Materials[c])
	}
}

func (e *env) ownMachine(pid int64, code string) {
	e.t.Helper()
	e.setFarm(pid, "level", 16)
	e.giveMachineMaterials(pid, code)
	e.ok(e.x.BuildMachine(e.ctx, pid, code))
}

func (e *env) trip(pid int64, area string) ExploreReward {
	e.t.Helper()
	must[ExploreOutcome](e.t)(e.x.Explore(e.ctx, pid, area))
	e.clock.Advance(float64(exploration.Areas[area].DurationSeconds))
	return must[ExploreReward](e.t)(e.x.ClaimExploration(e.ctx, pid))
}

func (e *env) overview(pid int64) ExplorationOverview {
	e.t.Helper()
	return must[ExplorationOverview](e.t)(e.x.ExplorationOverview(e.ctx, pid))
}

// ---------- 探險 ----------

func TestLevel1CanOnlyExploreGrassland(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	r := e.trip(pid, "grassland")
	if r.Area != "grassland" || len(r.Items) == 0 || r.Level != 1 || e.bpQty(pid, "wood") != 6 { // 3 份獎勵都是木材 ×2
		t.Fatalf("%+v", r)
	}
	for _, area := range []string{"forest", "古老森林", "mine", "神秘礦坑"} {
		_, err := e.x.Explore(e.ctx, pid, area)
		wantMsg(t, err, gameerr.Validation, "Lv")
	}
	_, err := e.x.Explore(e.ctx, pid, "火山")
	wantKind(t, err, gameerr.NotFound)
}

func TestLevelUnlocks(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.setFarm(pid, "level", 5)
	if o := must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "forest")); o.Trip.Area != "forest" {
		t.Fatal(o.Trip)
	}
	e.clock.Advance(16 * hour)
	must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	_, err := e.x.Explore(e.ctx, pid, "mine")
	wantMsg(t, err, gameerr.Validation, "Lv.10")
	e.setFarm(pid, "level", 10)
	if o := must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "mine")); o.Trip.Area != "mine" {
		t.Fatal(o.Trip)
	}
}

func TestTripLastsAreaDurationAndRewardsOnClaim(t *testing.T) {
	for _, c := range []struct {
		area  string
		hours int
	}{{"grassland", 8}, {"forest", 16}, {"mine", 24}} {
		t.Run(c.area, func(t *testing.T) {
			e := setup(t)
			pid := e.explorer()
			e.setFarm(pid, "level", 10)
			start := e.clock.Now()
			trip := must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, c.area)).Trip
			if trip.EndsAt.Sub(start) != time.Duration(c.hours)*time.Hour || trip.DurationSeconds != c.hours*hour {
				t.Fatalf("%+v", trip)
			}
			if e.bpQty(pid, "wood")+e.bpQty(pid, "stone") != 0 { // 出發時不發獎勵
				t.Fatal("出發時就發獎勵")
			}
			e.clock.Advance(float64(c.hours*hour - 1))
			_, err := e.x.ClaimExploration(e.ctx, pid)
			wantMsg(t, err, gameerr.Validation, "探險中")
			tv := e.overview(pid).Trip
			if tv.Area != c.area || tv.RemainingSeconds != 1 || tv.Finished {
				t.Fatalf("%+v", tv)
			}
			e.clock.Advance(1)
			if !e.overview(pid).Trip.Finished {
				t.Fatal("沒有回來")
			}
			got := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
			total := 0
			for _, i := range got.Items {
				total += i.Quantity
			}
			if got.Area != c.area || total == 0 || e.overview(pid).Trip != nil {
				t.Fatalf("%+v", got)
			}
			_, err = e.x.ClaimExploration(e.ctx, pid) // 不能重複領取
			wantMsg(t, err, gameerr.Validation, "沒有可以領取")
		})
	}
}

func TestOnlyOneAreaAtATime(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.setFarm(pid, "level", 10)
	e.setFarm(pid, "warehouse_capacity", 300)
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "grassland"))
	ready := func() []string {
		var out []string
		for _, a := range e.overview(pid).Areas {
			if a.Ready {
				out = append(out, a.Code)
			}
		}
		return out
	}
	if len(ready()) != 0 {
		t.Fatal(ready())
	}
	normal := []string{"grassland", "forest", "mine"} // 古代遺跡是額外的探索
	for _, area := range normal {                     // 探險中：哪個區域都不能出發，包含同一個區域
		_, err := e.x.Explore(e.ctx, pid, area)
		wantMsg(t, err, gameerr.Validation, "一次只能去一個區域")
	}
	e.clock.Advance(8 * hour)
	for _, area := range normal { // 回來了但還沒領取：也不能出發
		_, err := e.x.Explore(e.ctx, pid, area)
		wantMsg(t, err, gameerr.Validation, "先領取")
	}
	must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	if !slices.Equal(ready(), normal) { // 古代遺跡要所屬群組修好才能出發
		t.Fatal(ready())
	}
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "mine")) // 領取後可以立刻去另一個區域
	e.clock.Advance(24 * hour)
	must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "grassland")) // 也可以立刻再去同一個區域
	var counts []int
	for _, a := range e.overview(pid).Areas {
		counts = append(counts, a.ExploreCount)
	}
	if !slices.Equal(counts, []int{2, 0, 1, 0}) {
		t.Fatal(counts)
	}
}

func TestExploreRecordsLogTransactionAndInventory(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "grassland"))
	// Go 版新增：每次探險另外帶回木屑（亂數 0 → 草原最少 2 個），排在原本的掉落後面
	if e.scalar(`SELECT count(*) FROM exploration_logs WHERE rewards::text = '{"wood": 6, "sawdust": 2}' AND claimed_at IS NULL`) != 1 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'explore'") != 0 {
		t.Fatal("獎勵應在出發時決定、還沒放進背包")
	}
	if logs := must[[]ExplorationLogView](t)(e.x.ExplorationLogs(e.ctx, pid, 10)); len(logs) != 0 { // 還沒領取的不顯示
		t.Fatal(logs)
	}
	e.clock.Advance(8 * hour)
	r := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	if e.scalar("SELECT count(*) FROM exploration_logs WHERE area_code = 'grassland' AND farm_level = 1 AND claimed_at = $1",
		e.clock.Now()) != 1 {
		t.Fatal("領取時間不對")
	}
	if e.scalar(`SELECT count(*) FROM game_transactions WHERE event_type = 'explore' AND quantity = 8 AND gold_delta = 0
		AND description LIKE '%木材 ×6、木屑 ×2%'`) != 1 {
		t.Fatal("交易紀錄不對")
	}
	logs := must[[]ExplorationLogView](t)(e.x.ExplorationLogs(e.ctx, pid, 10))
	if len(logs[0].Rewards) != 2 || logs[0].Rewards[0] != (NamedQty{"木材", 6}) || logs[0].Rewards[1] != (NamedQty{"木屑", 2}) ||
		logs[0].AreaName != "青青草原" {
		t.Fatalf("%+v", logs[0])
	}
	if len(r.Items) != 2 || r.Items[0] != (ExploreItem{"wood", "木材", 6, "普通"}) || r.Items[1] != (ExploreItem{"sawdust", "木屑", 2, "普通"}) {
		t.Fatalf("%+v", r.Items)
	}
}

func TestRewardOrderPreservedLikePython(t *testing.T) {
	// 寶箱事件：先抽到假幣、再抽到素材；json 欄位保留寫入順序，訊息也依掉落順序
	e := setup(t)
	pid := e.explorer()
	calls := 0
	e.s.exploreRng = func() float64 { // 第一次抽類型落在 event 區間，之後都是 0
		calls++
		if calls == 1 {
			return 0.99
		}
		return 0
	}
	r := e.trip(pid, "grassland")
	if len(r.Items) < 2 || r.Items[0].Code != exploration.FakeCoin {
		t.Fatalf("%+v", r.Items)
	}
	desc := ""
	for _, s := range e.descriptions("explore") {
		desc = s
	}
	if !strings.HasPrefix(desc, "探索青青草原：假幣 ×") {
		t.Fatal(desc)
	}
}

func (e *env) descriptions(event string) []string {
	e.t.Helper()
	var out []string
	repotestQuery(e.t, e, "SELECT description FROM game_transactions WHERE event_type = '"+event+"' ORDER BY id", &out)
	return out
}

func TestMaterialsGoToMaterialBagNotWarehouse(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.setFarm(pid, "warehouse_capacity", 3)
	e.give(pid, "wheat", 3)  // 倉庫全滿
	e.trip(pid, "grassland") // 素材放素材背包，倉庫滿了也能領
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if bp.Used != 3 || bp.MaterialUsed != 8 || bp.MaterialCapacity != exploration.MaterialBagCapacity ||
		e.status(pid).WarehouseUsed != 3 {
		t.Fatalf("%+v", bp)
	}
	_, err := e.s.BuySeeds(e.ctx, pid, "wheat", 1) // 倉庫本身仍然是滿的
	wantMsg(t, err, gameerr.WarehouseFull, "倉庫空間不足")
}

func setBag(t *testing.T, n int) {
	old := exploration.MaterialBagCapacity
	exploration.MaterialBagCapacity = n
	t.Cleanup(func() { exploration.MaterialBagCapacity = old })
}

func TestFullMaterialBagKeepsRewardsUntilClaimed(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	setBag(t, 3)
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "grassland"))
	e.clock.Advance(8 * hour)
	_, err := e.x.ClaimExploration(e.ctx, pid)
	wantMsg(t, err, gameerr.WarehouseFull, "素材背包空間不足")
	if !strings.Contains(err.Error(), "清出空間後再領取") {
		t.Fatal(err)
	}
	_, err = e.x.Explore(e.ctx, pid, "grassland")
	wantMsg(t, err, gameerr.Validation, "先領取")
	if e.bpQty(pid, "wood") != 0 {
		t.Fatal("放不下卻發了")
	}
	exploration.MaterialBagCapacity = 500
	if r := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid)); r.Items[0].Quantity != 6 || e.bpQty(pid, "wood") != 6 {
		t.Fatalf("%+v", r)
	}
}

// race 同時執行 n 次；成功的結果數與 Validation 錯誤數（其他錯誤讓測試失敗）。
func race[T any](t *testing.T, n int, fn func(i int) (T, error)) (oks, errs int) {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	var other []error
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := fn(i)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				oks++
			case gameerr.Is(err, gameerr.Generic):
				errs++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(other) > 0 {
		t.Fatalf("非預期的錯誤：%v", errors.Join(other...))
	}
	return oks, errs
}

func TestConcurrentExploreAndClaimRewardOnce(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.setFarm(pid, "level", 10)
	areas := []string{"grassland", "forest", "mine", "grassland", "forest", "mine"}
	// 同時出發到不同區域：只有一趟成功（其他看到進行中的探險，或撞到唯一索引）
	oks := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.x.Explore(e.ctx, pid, areas[i])
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				oks++
			} else if !gameerr.Is(err, gameerr.Validation) && !isIntegrity(err) {
				t.Errorf("非預期的錯誤：%v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if oks != 1 {
		t.Fatal(oks)
	}
	e.clock.Advance(24 * hour)
	if ok, errs := race(t, 6, func(int) (ExploreReward, error) { return e.x.ClaimExploration(e.ctx, pid) }); ok != 1 || errs != 5 {
		t.Fatal(ok, errs)
	}
	if e.scalar("SELECT count(*) FROM exploration_logs") != 1 ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'explore'") != 1 {
		t.Fatal("重複發獎")
	}
}

func TestPlayersHaveSeparateTrips(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	e.s.exploreRng = func() float64 { return 0 }
	pid := e.farmer("阿明", home)
	other := e.farmer("阿華", home)
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, pid, "grassland"))
	must[ExploreOutcome](t)(e.x.Explore(e.ctx, other, "grassland"))
	_, err := e.x.Explore(e.ctx, pid, "grassland")
	wantKind(t, err, gameerr.Validation)
}

// ---------- 假幣 ----------

func TestFakeCoinExchange(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	_, err := e.x.ExchangeFakeCoins(e.ctx, pid, 1)
	wantKind(t, err, gameerr.InsufficientItems)
	e.give(pid, "fake_coin", 3)
	if r := e.ok(e.x.ExchangeFakeCoins(e.ctx, pid, 2)); r.Gold != 700 || r.GoldDelta != 200 {
		t.Fatalf("%+v", r)
	}
	_, err = e.x.ExchangeFakeCoins(e.ctx, pid, 2)
	wantMsg(t, err, gameerr.InsufficientItems, "只有 1")
	if e.gold(pid) != 700 || e.bpQty(pid, "fake_coin") != 1 {
		t.Fatal("兌換結果不對")
	}
	_, err = e.x.ExchangeFakeCoins(e.ctx, pid, 0)
	wantKind(t, err, gameerr.Validation)
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestConcurrentFakeCoinExchange(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.give(pid, "fake_coin", 1)
	if ok, errs := race(t, 5, func(int) (ActionResult, error) { return e.x.ExchangeFakeCoins(e.ctx, pid, 1) }); ok != 1 || errs != 4 {
		t.Fatal(ok, errs)
	}
	if e.gold(pid) != 600 {
		t.Fatal(e.gold(pid))
	}
}

// ---------- 機器製作 ----------

func TestBuildMachineChecksLevelAndMaterials(t *testing.T) {
	for _, code := range exploration.MachineOrder {
		t.Run(code, func(t *testing.T) {
			e := setup(t)
			pid := e.explorer()
			m := exploration.Machines[code]
			e.giveMachineMaterials(pid, code)
			if m.UnlockLevel > 1 {
				_, err := e.x.BuildMachine(e.ctx, pid, code)
				wantMsg(t, err, gameerr.Validation, fmt.Sprintf("Lv.%d", m.UnlockLevel))
			}
			e.setFarm(pid, "level", m.UnlockLevel)
			first := m.MaterialOrder[0]
			e.exec(`UPDATE inventory_items SET quantity = quantity - 2 WHERE item_code = $1`, first)
			_, err := e.x.BuildMachine(e.ctx, pid, m.Name)
			wantMsg(t, err, gameerr.InsufficientItems, exploration.MaterialName(first)+" ×2")
			e.give(pid, first, 2)
			if r := e.ok(e.x.BuildMachine(e.ctx, pid, m.Name)); !strings.Contains(r.Message, m.Name) {
				t.Fatal(r.Message)
			}
			for _, c := range m.MaterialOrder {
				if e.bpQty(pid, c) != 0 {
					t.Fatal(c)
				}
			}
			_, err = e.x.BuildMachine(e.ctx, pid, code)
			wantKind(t, err, gameerr.AlreadyExists)
			for _, v := range must[MachinesStatus](t)(e.x.MachinesStatus(e.ctx, pid)).Machines {
				if v.Code == code && (!v.Owned || !v.Enabled) {
					t.Fatalf("%+v", v)
				}
			}
		})
	}
}

func TestConcurrentBuildMachineChargesOnce(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.setFarm(pid, "level", 3)
	e.giveMachineMaterials(pid, "seed_maker")
	e.giveMachineMaterials(pid, "seed_maker")
	if ok, errs := race(t, 4, func(int) (ActionResult, error) { return e.x.BuildMachine(e.ctx, pid, "seed_maker") }); ok != 1 || errs != 3 {
		t.Fatal(ok, errs)
	}
	if e.bpQty(pid, "wood") != 10 || e.scalar("SELECT count(*) FROM farm_tools") != 1 {
		t.Fatal("重複扣料")
	}
}

// ---------- 種子製造機 ----------

func TestSeedMakerTwoSeedsPerCrop(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	_, err := e.x.MakeSeeds(e.ctx, pid, "wheat", 1, "normal", "none")
	wantMsg(t, err, gameerr.Validation, "種子製造機")
	e.ownMachine(pid, "seed_maker")
	e.give(pid, "wheat", 3)
	if r := e.ok(e.x.MakeSeeds(e.ctx, pid, "小麥", 3, "normal", "none")); r.Quantity != 6 || e.bpQty(pid, "wheat") != 0 ||
		e.bpQty(pid, "seed_wheat") != 6 {
		t.Fatalf("%+v", r)
	}
	_, err = e.x.MakeSeeds(e.ctx, pid, "wheat", 1, "normal", "none")
	wantKind(t, err, gameerr.InsufficientItems)
	e.giveQ(pid, "wheat", 2, "rare")
	_, err = e.x.MakeSeeds(e.ctx, pid, "wheat", 1, "rare", "none")
	wantMsg(t, err, gameerr.Validation, "普通品質")
	e.give(pid, "amber_melon", 1)
	_, err = e.x.MakeSeeds(e.ctx, pid, "琥珀瓜", 1, "normal", "none")
	wantMsg(t, err, gameerr.Validation, "稀有作物")
	e.ok(e.x.SetMachineEnabled(e.ctx, pid, "seed_maker", false))
	_, err = e.x.MakeSeeds(e.ctx, pid, "wheat", 1, "normal", "none")
	wantMsg(t, err, gameerr.Validation, "停用")
}

func TestSeedMakerRareCropRuleIsConfigurable(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.ownMachine(pid, "seed_maker")
	e.give(pid, "amber_melon", 1)
	exploration.SeedMakerRareCrops = true
	t.Cleanup(func() { exploration.SeedMakerRareCrops = false })
	if r := e.ok(e.x.MakeSeeds(e.ctx, pid, "amber_melon", 1, "normal", "none")); r.Quantity != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestSeedMakerRollsBackWhenWarehouseFull(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.ownMachine(pid, "seed_maker")
	e.give(pid, "wheat", 30)
	e.setFarm(pid, "warehouse_capacity", 40)
	_, err := e.x.MakeSeeds(e.ctx, pid, "wheat", 30, "normal", "none")
	wantKind(t, err, gameerr.WarehouseFull)
	if e.bpQty(pid, "wheat") != 30 { // 扣作物與加種子同一筆交易，失敗時一起回滾
		t.Fatal(e.bpQty(pid, "wheat"))
	}
}

// ---------- 稀有種子作物與培育器 ----------

func TestRareSeedsNotSoldButPlantable(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	_, err := e.s.BuySeeds(e.ctx, pid, "琥珀瓜", 1)
	wantMsg(t, err, gameerr.Validation, "商店不販售")
	for _, c := range must[[]CropInfo](t)(e.s.GetMarketPrices(e.ctx)) {
		if c.Code == "amber_melon" && c.ShopBuyable {
			t.Fatal("琥珀瓜可以購買")
		}
	}
	_, err = e.s.PlantAll(e.ctx, pid, "amber_melon")
	wantMsg(t, err, gameerr.InsufficientItems, "商店不販售")
	e.give(pid, exploration.AmberSeed, 2)
	r := e.ok(e.s.PlantAll(e.ctx, pid, "amber_melon"))
	if !strings.Contains(r.Message, "在 2 格種下琥珀瓜") || !strings.Contains(r.Message, "種子不夠") || e.gold(pid) != 500 { // 沒有自動購買
		t.Fatal(r.Message)
	}
}

func TestIncubatorCreatesRareSeedOrder(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	_, err := e.x.StartIncubation(e.ctx, pid, "incubate_amber", 1)
	wantMsg(t, err, gameerr.Validation, "稀有作物培育器")
	e.ownMachine(pid, "rare_incubator")
	e.give(pid, "wheat", 10)
	e.give(pid, "herb", 3)
	if r := e.ok(e.x.StartIncubation(e.ctx, pid, "琥珀瓜", 1)); !strings.Contains(r.Message, "稀有作物培育器") {
		t.Fatal(r.Message)
	}
	if e.bpQty(pid, "wheat") != 0 || e.bpQty(pid, "herb") != 0 {
		t.Fatal("原料沒扣")
	}
	o := e.orders(pid, false)[0]
	if o.BuildingName != "稀有作物培育器" || o.RecipeName != "琥珀瓜種子培育" {
		t.Fatalf("%+v", o)
	}
	e.clock.Advance(4 * hour)
	e.setFarm(pid, "warehouse_capacity", 1)
	e.give(pid, "wheat", 1) // 倉庫全滿：稀有種子放素材背包，照樣可以領
	e.ok(e.r.CollectCraft(e.ctx, pid, o.OrderID))
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if e.bpQty(pid, exploration.AmberSeed) != 1 || bp.Used != 1 || bp.MaterialUsed != 1 {
		t.Fatalf("%+v", bp)
	}
	if v := must[[]IncubatorRecipeView](t)(e.x.IncubatorRecipes(e.ctx)); v[0].Inputs["小麥"] != 10 || v[0].InputOrder[0] != "小麥" {
		t.Fatalf("%+v", v[0])
	}
}

func TestRareSeedsUseMaterialBag(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	setBag(t, 2)
	e.give(pid, "seed_moonflower", 2) // 素材背包已滿
	if bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)); bp.Used != 0 || bp.MaterialUsed != 2 {
		t.Fatalf("%+v", bp)
	}
	check := func(code string) error {
		return e.s.transaction(e.ctx, func(tx *dbTx) error {
			farm, err := e.s.lockedFarm(e.ctx, tx, pid)
			if err != nil {
				return err
			}
			return e.s.ensureItemSpace(e.ctx, tx, farm, code, 1)
		})
	}
	wantMsg(t, check("seed_amber_melon"), gameerr.WarehouseFull, "素材背包空間不足")
	if err := check("seed_wheat"); err != nil { // 一般種子仍放倉庫
		t.Fatal(err)
	}
}

// ---------- 自動收割機與自動加工機 ----------

func (e *env) run(pid int64) string {
	e.t.Helper()
	return must[string](e.t)(e.x.RunMachines(e.ctx, pid))
}

func (e *env) allPlots(pid int64, state rules.PlotState) int {
	n := 0
	for _, p := range e.status(pid).Plots {
		if p.State == state {
			n++
		}
	}
	return n
}

func TestAutoHarvesterCollectsReadyAndLateCrops(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	if e.run(pid) != "" { // 沒有機器：不做任何事
		t.Fatal("沒有機器卻有動作")
	}
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.ownMachine(pid, "auto_harvester")
	e.clock.Advance(60)
	if note := e.run(pid); !strings.Contains(note, "自動收割機收成 4 格") || e.bpQty(pid, "wheat") != 8 || e.allPlots(pid, rules.PlotEmpty) != 4 {
		t.Fatal(note)
	}
	// 擁有收割機期間成熟、之後才打開面板：已過乾枯時間也照樣收成
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(60 + balance.WitherSeconds + 10)
	if e.status(pid).Plots[0].State != rules.PlotWithered || !strings.Contains(e.run(pid), "收成 4 格") {
		t.Fatal("乾枯的沒收成")
	}
	// 停用後不再自動收成，手動收成仍正常
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.ok(e.x.SetMachineEnabled(e.ctx, pid, "auto_harvester", false))
	e.clock.Advance(60)
	if e.run(pid) != "" {
		t.Fatal("停用後仍收成")
	}
	if r := e.ok(e.s.HarvestAll(e.ctx, pid)); r.Quantity != 8 {
		t.Fatal(r.Quantity)
	}
}

func TestAutoHarvesterStopsWhenWarehouseFull(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.ownMachine(pid, "auto_harvester")
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.setFarm(pid, "warehouse_capacity", 5)
	e.clock.Advance(60)
	if note := e.run(pid); !strings.Contains(note, "收成 2 格") || !strings.Contains(note, "倉庫已滿") || e.allPlots(pid, rules.PlotReady) != 2 {
		t.Fatal(note)
	}
}

func TestAutoProcessorCollectsAndStartsOrders(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.ownMachine(pid, "auto_processor")
	e.ok(e.x.SetProcessorRecipe(e.ctx, pid, "麵粉"))
	if note := e.run(pid); !strings.Contains(note, "需要先建造磨坊") {
		t.Fatal(note)
	}
	e.setGold(pid, 10_000)
	e.ok(e.r.Build(e.ctx, pid, "磨坊"))
	e.give(pid, "wheat", 7)
	if note := e.run(pid); !strings.Contains(note, "開始加工 2 份麵粉") || e.bpQty(pid, "wheat") != 1 {
		t.Fatal(note)
	}
	if note := e.run(pid); note != "" { // 加工中：不重複開單
		t.Fatal(note)
	}
	e.clock.Advance(2 * 30 * 60)
	if note := e.run(pid); !strings.Contains(note, "領取 1 張訂單") || e.bpQty(pid, "flour") != 2 {
		t.Fatal(note)
	}
	// 原料不夠 1 份時不開單；手動加工仍可使用
	e.give(pid, "wheat", 2)
	if r := e.ok(e.r.StartCraft(e.ctx, pid, "麵粉", 1)); r.Quantity != 1 {
		t.Fatal(r.Quantity)
	}
	_, err := e.x.SetProcessorRecipe(e.ctx, pid, "火箭")
	wantKind(t, err, gameerr.NotFound)
}

func TestBackpackListsMaterials(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.give(pid, "gear", 2)
	e.give(pid, "fake_coin", 1)
	bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid))
	if len(bp.Materials) != 2 || bp.Materials[0].Name != "齒輪" || bp.Materials[0].Quantity != 2 || bp.Materials[0].Price != 0 ||
		bp.Materials[1].Name != "假幣" || bp.Materials[1].Price != 100 || bp.Used != 0 || bp.MaterialUsed != 3 || bp.IsEmpty() {
		t.Fatalf("%+v", bp)
	}
}

// TestMachinesInPageBatchUseSavepoint 面板讀取整頁時機器在同一筆交易的 SAVEPOINT 中執行：成功時頁面看得到結果；
// 失敗時只回滾機器的部分，頁面照常讀取。
func TestMachinesInPageBatchUseSavepoint(t *testing.T) {
	e := setup(t)
	pid := e.explorer()
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.ownMachine(pid, "auto_harvester")
	e.clock.Advance(60)
	var note string
	var st FarmStatus
	page := func(ctx ctxT) error {
		var err error
		if note, err = e.x.RunMachines(ctx, pid); err != nil {
			return err
		}
		st, err = e.s.GetFarmStatus(ctx, pid)
		return err
	}
	if err := e.s.RunBatch(e.ctx, page); err != nil {
		t.Fatal(err)
	}
	empty := 0
	for _, p := range st.Plots {
		if p.State == rules.PlotEmpty {
			empty++
		}
	}
	if !strings.Contains(note, "自動收割機收成 4 格") || empty != 4 || e.bpQty(pid, "wheat") != 8 { // 已提交
		t.Fatal(note)
	}

	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(60)
	autoHarvestHook = func() error { return errors.New("機器故障") } // 先收成（寫入），再失敗：SAVEPOINT 要把這些寫入回滾
	t.Cleanup(func() { autoHarvestHook = nil })
	if err := e.s.RunBatch(e.ctx, page); err != nil {
		t.Fatal(err)
	}
	ready := 0
	for _, p := range st.Plots {
		if p.State == rules.PlotReady {
			ready++
		}
	}
	if note != "" || ready != 4 || e.bpQty(pid, "wheat") != 8 { // 頁面照常、作物還在、沒有多收
		t.Fatal(note, ready)
	}
}

type (
	dbTx = db.Tx
	ctxT = context.Context
)
