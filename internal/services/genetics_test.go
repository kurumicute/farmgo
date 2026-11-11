package services

// 對應 Python tests/test_genetics.py 的服務層部分：作物品質與突變。
// 純規則 4 個在 game/genetics；Migration 測試由 Python Alembic 負責（Go 不管理 schema）；CMD 不移植；網站 API 在第 4 階段。

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/repotest"
)

// geneSettings 預設設定（Python settings(**overrides)），f 可修改。
func geneSettings(f func(m map[string]any)) map[string]any {
	m := genetics.SettingsMap(balance.DefaultGenetics())
	if f != nil {
		f(m)
	}
	return m
}

// breeder 對應 Python farmer 夾具：套用預設機率（版本 2）、建立「育種家」並給 100,000 金幣。
func (e *env) breeder() (pid, home int64) {
	e.t.Helper()
	must[int](e.t)(e.s.UpdateGenetics(e.ctx, "admin", geneSettings(nil)))
	home = e.group("主群組")
	pid = e.farmer("育種家", home)
	e.setGold(pid, 100_000)
	return pid, home
}

// rolls 之後播種的品質與突變亂數依序為 values（Python svc(session_factory, clock, *rolls)）。
func (e *env) rolls(values ...float64) { e.s.geneticsRng = newDice(values...).roll }

// plantOne 買一顆種子、種下，water 時並澆水（Python plant）。
func (e *env) plantOne(s *GameService, pid int64, plot int, crop string, water bool) {
	e.t.Helper()
	e.ok(s.BuySeeds(e.ctx, pid, crop, 1))
	e.ok(s.PlantCrop(e.ctx, pid, plot, crop))
	if water {
		e.ok(s.WaterCrop(e.ctx, pid, plot))
	}
}

type plotGenes struct {
	quality, mutation string
	remaining         *int
}

func (e *env) plotRow(pid int64, n int) plotGenes {
	e.t.Helper()
	var r plotGenes
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, `SELECT p.quality, p.mutation, p.remaining_quantity FROM farm_plots p JOIN farms f ON f.id = p.farm_id
			WHERE f.player_id = $1 AND p.plot_number = $2`, pid, n).Scan(&r.quality, &r.mutation, &r.remaining)
	})
	return r
}

// stacks 非種子的庫存（代碼、品質、突變、數量），排序後回傳。
func (e *env) stacks(pid int64) []string {
	e.t.Helper()
	var out []string
	for _, row := range e.rowsOf(`SELECT i.item_code, i.quality, i.mutation, i.quantity FROM inventory_items i
		JOIN farms f ON f.id = i.farm_id WHERE f.player_id = $1 AND i.quantity > 0 AND i.item_code NOT LIKE 'seed\_%'`, pid) {
		out = append(out, fmt.Sprintf("%v/%v/%v/%v", row...))
	}
	slices.Sort(out)
	return out
}

func wantStacks(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("庫存 %v，應為 %v", got, want)
	}
}

func TestQualityFixedAtPlantingAndSurvivesRestart(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.95, 0.0) // 稀有、無突變
	e.plantOne(e.s, pid, 1, "wheat", true)
	if r := e.plotRow(pid, 1); r.quality != "rare" || r.mutation != "none" {
		t.Fatalf("%+v", r)
	}
	// 重新查詢、程式重啟都不重新抽取（新服務的亂數若被使用會得到傳說）
	rs := e.restart()
	rs.geneticsRng = newDice(0.999, 0.999).roll
	e.clock.Advance(60)
	for range 3 {
		p := must[FarmStatus](t)(rs.GetFarmStatus(e.ctx, pid)).Plots[0]
		if p.Quality == nil || *p.Quality != "rare" || p.Mutation == nil || *p.Mutation != "none" {
			t.Fatalf("%+v", p)
		}
	}
	if m := e.ok(rs.HarvestCrop(e.ctx, pid, 1)).Message; !strings.Contains(m, "稀有") {
		t.Fatal(m)
	}
	wantStacks(t, e.stacks(pid), "wheat/rare/none/2")
	_, err := e.s.HarvestCrop(e.ctx, pid, 1) // 不能重複收成
	wantKind(t, err, gameerr.PlotEmpty)
	wantStacks(t, e.stacks(pid), "wheat/rare/none/2")
}

func TestSettingsChangeDoesNotAffectPlantedCrops(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.75, 0.0, 0.75, 0.0) // 依當時設定：優良
	e.plantOne(e.s, pid, 1, "wheat", true)
	must[int](t)(e.s.UpdateGenetics(e.ctx, "admin", geneSettings(func(m map[string]any) {
		m["quality_bp"] = map[string]any{"normal": 9000, "good": 500, "rare": 300, "legendary": 200}
	})))
	e.plantOne(e.s, pid, 2, "wheat", true) // 新設定下 0.75 → 普通
	if q1, q2 := e.plotRow(pid, 1).quality, e.plotRow(pid, 2).quality; q1 != "good" || q2 != "normal" {
		t.Fatal(q1, q2)
	}
}

func TestLevelBonusAppliesAtPlanting(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	level := 11
	must[FarmValues](t)(e.a.SetFarmValues(e.ctx, "admin", pid, nil, &level)) // 加成 5%：普通 65%
	e.rolls(0.66, 0.0)
	e.plantOne(e.s, pid, 1, "wheat", true)
	if q := e.plotRow(pid, 1).quality; q != "good" { // 等級 1 時 0.66 會是普通
		t.Fatal(q)
	}
	if bp := must[QualityTable](t)(e.s.QualityTable(e.ctx, &pid)).EffectiveBP["normal"]; bp != 6500 {
		t.Fatal(bp)
	}
}

func TestSalePriceRecordedAndHistoryKept(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.95, 0.0) // 稀有
	e.plantOne(e.s, pid, 1, "wheat", true)
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	_, err := e.a.Sell(e.ctx, pid, "wheat", 1, "normal", "none") // 預設賣普通，沒有普通庫存
	wantKind(t, err, gameerr.InsufficientItems)
	before := e.gold(pid)
	if r := e.ok(e.a.Sell(e.ctx, pid, "小麥", 2, "rare", "none")); r.GoldDelta != 44 || e.gold(pid) != before+44 { // 15×1.5=22.5→22，×2
		t.Fatalf("%+v", r)
	}
	e.exec("UPDATE crop_types SET sell_price = 999 WHERE code = 'wheat'")
	rows := e.rowsOf(`SELECT unit_price, gold_delta, quality, mutation, quantity FROM game_transactions WHERE event_type = 'sell'`)
	if len(rows) != 1 || fmt.Sprint(rows[0]...) != fmt.Sprint(22, 44, "rare", "none", 2) {
		t.Fatal(rows)
	}
	_, err = e.a.Sell(e.ctx, pid, "雞蛋", 1, "rare", "none") // 商品沒有品質
	wantKind(t, err, gameerr.Validation)
}

func TestGoldenAndLegendaryPrice(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.99, 0.955) // 傳說、黃金
	e.plantOne(e.s, pid, 1, "wheat", true)
	e.clock.Advance(60)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	wantStacks(t, e.stacks(pid), "wheat/legendary/golden/2")
	p := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Produce
	if len(p) != 1 || p[0].Name != "小麥" || p[0].Quality != "legendary" || p[0].Mutation != "golden" || p[0].Price != 90 {
		t.Fatalf("%+v", p)
	}
	if r := e.ok(e.a.Sell(e.ctx, pid, "wheat", 2, "傳說", "golden")); r.GoldDelta != 180 {
		t.Fatalf("%+v", r)
	}
}

func TestStolenCropKeepsQualityAndMutation(t *testing.T) {
	e := setup(t)
	pid, home := e.breeder()
	thief := e.farmer("小偷", home)
	e.rolls(0.99, 0.955) // 傳說、黃金
	e.plantOne(e.s, pid, 1, "strawberry", true)
	e.clock.Advance(600)
	e.rolls(0.0, 0.0) // 若重新抽取會變成普通
	if r := e.ok(e.a.StealCrop(e.ctx, home, thief, pid, 1, 2)); !r.Success {
		t.Fatalf("%+v", r)
	}
	wantStacks(t, e.stacks(thief), "strawberry/legendary/golden/2")
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	wantStacks(t, e.stacks(pid), "strawberry/legendary/golden/3")
	rows := e.rowsOf("SELECT quality, mutation FROM game_transactions WHERE event_type = 'steal'")
	if len(rows) != 1 || rows[0][0] != "legendary" || rows[0][1] != "golden" {
		t.Fatal(rows)
	}
}

func TestGiantDoublesQuantityOnce(t *testing.T) {
	e := setup(t)
	pid, home := e.breeder()
	thief := e.farmer("小偷", home)
	e.rolls(0.0, 0.93) // 普通、巨型
	e.plantOne(e.s, pid, 1, "wheat", true)
	if r := e.plotRow(pid, 1).remaining; r == nil || *r != 4 {
		t.Fatal(r)
	}
	e.clock.Advance(60)
	e.ok(e.a.StealCrop(e.ctx, home, thief, pid, 1, 1)) // 小麥每次最多偷 1 個
	r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if r.Quantity != 3 || !strings.Contains(r.Message, "已被偷走 1 個") {
		t.Fatalf("%+v", r)
	}
	_, err := e.s.HarvestCrop(e.ctx, pid, 1)
	wantKind(t, err, gameerr.PlotEmpty)
	wantStacks(t, e.stacks(pid), "wheat/normal/giant/3")
	wantStacks(t, e.stacks(thief), "wheat/normal/giant/1")
}

func TestConcurrentHarvestOfGiantOnlyOnce(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.0, 0.93)
	e.plantOne(e.s, pid, 1, "wheat", true)
	e.clock.Advance(60)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := e.s.HarvestCrop(e.ctx, pid, 1)
			if err != nil && !gameerr.Is(err, gameerr.PlotEmpty) {
				t.Error(err)
			}
			results[i] = r.Quantity
		}()
	}
	close(start)
	wg.Wait()
	slices.Sort(results)
	if !slices.Equal(results, []int{0, 0, 0, 0, 4}) {
		t.Fatal(results)
	}
	wantStacks(t, e.stacks(pid), "wheat/normal/giant/4")
}

func TestFastGrowthNeedsWaterAndWithers(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	must[CropInfo](t)(e.s.UpdateCropGrowSeconds(e.ctx, "admin", "wheat", 7200))
	e.rolls(0.0, 0.97) // 普通、快速生長
	e.plantOne(e.s, pid, 1, "wheat", false)
	e.clock.Advance(3 * 3600)
	_, err := e.s.HarvestCrop(e.ctx, pid, 1) // 沒澆水就不會生長
	wantKind(t, err, gameerr.PlotNotWatered)
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	if r := e.plot(pid, 1).RemainingSeconds; r != balance.MinGrowSeconds { // 2 小時 − 2 小時 → 最短 60 秒
		t.Fatal(r)
	}
	e.clock.Advance(balance.MinGrowSeconds + balance.WitherSeconds)
	_, err = e.s.HarvestCrop(e.ctx, pid, 1) // 突變不能讓乾枯作物復活
	wantKind(t, err, gameerr.PlotWithered)
	e.ok(e.s.ClearWithered(e.ctx, pid, 1))
	wantStacks(t, e.stacks(pid)) // 乾枯作物不產生任何物品或品質獎勵
}

func TestRareVariantIsSeparateCrop(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	e.rolls(0.75, 0.995) // 優良、珍稀變種
	e.plantOne(e.s, pid, 1, "wheat", true)
	e.clock.Advance(60)
	if m := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)).Message; !strings.Contains(m, "琥珀小麥") {
		t.Fatal(m)
	}
	wantStacks(t, e.stacks(pid), "amber_wheat/good/variant/2")
	_, err := e.a.Sell(e.ctx, pid, "wheat", 1, "good", "variant") // 須用變種名稱
	wantKind(t, err, gameerr.Validation)
	if r := e.ok(e.a.Sell(e.ctx, pid, "琥珀小麥", 2, "優良", "none")); r.GoldDelta != 2*(15*4*120/100) { // 基本售價 60 × 1.2 = 72
		t.Fatalf("%+v", r)
	}
	found := false
	for _, r := range must[[]leaderboard.Row](t)(e.a.Leaderboard(e.ctx, "sales", nil, nil, 10)) {
		if r.PlayerName == "育種家" {
			found = true
			if r.SoldQuantity != 2 || r.SalesIncome != 144 { // 計入作物販售，一次販售只算一次
				t.Fatalf("%+v", r)
			}
		}
	}
	if !found {
		t.Fatal("排行沒有育種家")
	}
}

func TestInventoryStacksKeptSeparate(t *testing.T) {
	e := setup(t)
	pid, _ := e.breeder()
	for i, r := range [][2]float64{{0.0, 0.0}, {0.95, 0.0}, {0.95, 0.955}} {
		e.rolls(r[0], r[1])
		e.plantOne(e.s, pid, i+1, "wheat", true)
	}
	e.clock.Advance(60)
	for n := 1; n <= 3; n++ {
		e.ok(e.s.HarvestCrop(e.ctx, pid, n))
	}
	wantStacks(t, e.stacks(pid), "wheat/normal/none/2", "wheat/rare/golden/2", "wheat/rare/none/2")
	if u := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Used; u != 6 {
		t.Fatal(u)
	}
}

func TestAdminGeneticsSettingsAndGrowTime(t *testing.T) {
	e := setup(t)
	e.breeder()
	_, err := e.s.UpdateGenetics(e.ctx, "admin", geneSettings(func(m map[string]any) {
		m["quality_bp"] = map[string]any{"normal": 7000, "good": 2000, "rare": 800, "legendary": 100}
	}))
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "100%") {
		t.Fatal(err)
	}
	if v, _, _ := e.s.GeneticsSettings(e.ctx); v != 2 { // 無效設定沒有套用
		t.Fatal(v)
	}
	v := must[int](t)(e.s.UpdateGenetics(e.ctx, "admin", geneSettings(func(m map[string]any) { m["golden_price_pct"] = 500 })))
	if _, s, _ := e.s.GeneticsSettings(e.ctx); v != 3 || s.GoldenPricePct != 500 {
		t.Fatal(v, s)
	}
	_, err = e.s.UpdateCropGrowSeconds(e.ctx, "admin", "wheat", 10)
	wantKind(t, err, gameerr.Validation)
	must[CropInfo](t)(e.s.UpdateCropGrowSeconds(e.ctx, "admin", "番茄", 3600))
	if g := must[CropCatalogEntry](t)(e.s.FindCropInfo(e.ctx, "tomato")).GrowSeconds; g != 3600 {
		t.Fatal(g)
	}
	if a := e.auditActions(); !slices.Equal(a, []string{"update_genetics", "update_genetics", "update_crop_grow_time"}) {
		t.Fatal(a)
	}
}

func TestBaselineGeneticsUsedByOtherTests(t *testing.T) {
	e := setup(t)
	v, s, err := e.s.GeneticsSettings(e.ctx)
	if err != nil || v != 1 || !reflect.DeepEqual(s, baselineGenetics()) {
		t.Fatal(v, s, err)
	}
}

// rowsOf 查詢多列（測試用），每列為各欄位的值。
func (e *env) rowsOf(sql string, args ...any) [][]any {
	e.t.Helper()
	var out [][]any
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := rows.Values()
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out
}
