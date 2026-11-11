package services

// 服務層測試的共用夾具（對應 Python tests/conftest.py 的 clean_db、service、home_group、player_id 等）。

import (
	"context"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/services/svctest"
)

// baselineGenetics 100% 普通、不突變、無等級加成（svctest.BaselineGenetics）。
func baselineGenetics() balance.GeneticsSettings { return svctest.BaselineGenetics() }

type env struct {
	t     *testing.T
	ctx   context.Context
	d     *db.DB
	clock *clock.Manual
	s     *GameService
	r     *RanchService
	x     *ExplorationService
	f     *FishingService
	so    *SocialService
	co    *CommunityService
	rk    *RankingService
	wb    *WorldBossService
	n     *NewsService
	p     *ProgressService
	a     *AdminService // 最上層的服務（Python 測試的 AdminService）；p、n、wb、rk、co、so、f、x、r、s 是它內嵌的下層服務
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// setup 清空資料庫、寫入基準品質設定、重設作物與動物目錄，並建立服務（沒有測試資料庫時 Skip）。
func setup(t *testing.T) *env {
	t.Helper()
	d := svctest.Prepare(t, ClearNewsCache)
	c := clock.NewManual(time.Time{})
	as := NewAdminService(d, Options{Clock: c})
	ps := as.ProgressService
	ns := ps.NewsService
	wbs := ns.WorldBossService
	rk := wbs.RankingService
	co := rk.CommunityService
	so := co.SocialService
	f := so.FishingService
	x := f.ExplorationService
	return &env{t: t, ctx: context.Background(), d: d, clock: c, s: x.GameService, r: x.RanchService, x: x, f: f, so: so, co: co, rk: rk, wb: wbs, n: ns, p: ps, a: as}
}

// group 建立群組（Python service.create_group）。
func (e *env) group(name string) int64 {
	e.t.Helper()
	return must[GroupInfo](e.t)(e.so.CreateGroup(e.ctx, name, nil)).ID
}

// farmer 建立玩家與農場，農場主要所屬群組為 groupID。
func (e *env) farmer(name string, groupID int64) int64 {
	e.t.Helper()
	p := must[PlayerInfo](e.t)(e.s.CreatePlayer(e.ctx, name, nil, nil))
	must[FarmStatus](e.t)(e.s.CreateFarm(e.ctx, p.ID, "", &groupID))
	return p.ID
}

// player 對應 Python 的 player_id 夾具：「主群組」中的「阿明」。
func (e *env) player() int64 { return e.farmer("阿明", e.group("主群組")) }

// exec 直接執行 SQL（測試用）。
func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// scalar 查詢單一整數值（測試用）。
func (e *env) scalar(sql string, args ...any) int {
	e.t.Helper()
	var n int
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&n)
	})
	return n
}

func (e *env) qty(pid int64, code string) int {
	e.t.Helper()
	inv := must[Inventory](e.t)(e.s.GetInventory(e.ctx, pid))
	for _, i := range inv.Items {
		if i.ItemCode == code {
			return i.Quantity
		}
	}
	return 0
}

func (e *env) status(pid int64) FarmStatus {
	e.t.Helper()
	return must[FarmStatus](e.t)(e.s.GetFarmStatus(e.ctx, pid))
}

// ok 操作必須成功。
func (e *env) ok(r ActionResult, err error) ActionResult {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) plantWatered(pid int64, plot int, crop string) (ActionResult, error) {
	if _, err := e.s.PlantCrop(e.ctx, pid, plot, crop); err != nil {
		return ActionResult{}, err
	}
	return e.s.WaterCrop(e.ctx, pid, plot)
}

// grow 買種子、種植並澆水（呼叫端再自行推進時鐘讓作物成熟）。
func (e *env) grow(pid int64, plot int, crop string) {
	e.t.Helper()
	e.ok(e.s.BuySeeds(e.ctx, pid, crop, 1))
	e.ok(e.plantWatered(pid, plot, crop))
}

// give 直接放入庫存（Python 測試的 give：inventory_repo.add）。
func (e *env) give(pid int64, code string, qty int) {
	e.t.Helper()
	e.exec(`INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity)
		SELECT id, $2, 'normal', 'none', $3 FROM farms WHERE player_id = $1
		ON CONFLICT ON CONSTRAINT uq_inventory_items_farm_item DO UPDATE SET quantity = inventory_items.quantity + $3`,
		pid, code, qty)
}

// setGold 直接設定金幣（不寫交易紀錄）。
func (e *env) setGold(pid int64, gold int) {
	e.t.Helper()
	e.exec("UPDATE farms SET gold = $2 WHERE player_id = $1", pid, gold)
}

// setGoldRecorded 設定金幣並寫 admin_adjust 交易紀錄（Python 測試的 admin.set_farm_values(actor, pid, gold=…)）。
func (e *env) setGoldRecorded(pid int64, gold int) {
	e.t.Helper()
	if _, err := e.a.SetFarmValues(e.ctx, "root", pid, &gold, nil); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) gold(pid int64) int { return e.status(pid).Gold }
