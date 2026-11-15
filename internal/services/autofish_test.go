package services

// 自動釣魚機與魚簍升級（Go 版新增，2026-11-15）。

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/autofish"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	fishrepo "farmgm/internal/repositories/fishing"
)

func (e *env) autoFish(pid int64) AutoFishStatus {
	e.t.Helper()
	return must[AutoFishStatus](e.t)(e.s.AutoFishing(e.ctx, pid))
}

func (e *env) basketCount(pid int64) int {
	e.t.Helper()
	return e.scalar("SELECT count(*)::int FROM farm_fish f JOIN farms fa ON fa.id = f.farm_id WHERE fa.player_id = $1 "+
		"AND f.sold_at IS NULL AND f.donated_at IS NULL", pid)
}

func TestAutoFisherFillsBasketAndPauses(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.s.AutoFishRng = func() float64 { return 0.3 }
	e.setGoldRecorded(pid, 200_000)
	// 價格讀設定（預設 10 萬）
	if balance.ItemPrice(balance.ToolAutoFisher) != 100_000 {
		t.Fatal(balance.ItemPrice(balance.ToolAutoFisher))
	}
	r := e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoFisher, 1))
	if r.GoldDelta != -100_000 || e.gold(pid) != 100_000 {
		t.Fatalf("%+v", r)
	}
	if st := e.autoFish(pid); !st.Owned || !st.Enabled || st.Caught != 0 || st.NextAt == nil {
		t.Fatalf("%+v", st)
	}
	// 12 分鐘：每 5 分鐘 1 條 → 2 條，剩下的 2 分鐘留到下一次
	e.clock.Advance((12 * time.Minute).Seconds())
	st := e.autoFish(pid)
	if st.Caught != 2 || st.Sold != 0 || !strings.Contains(st.Note(), "釣到 2 條魚，已放進魚簍") || e.basketCount(pid) != 2 {
		t.Fatalf("%+v", st)
	}
	if n := e.scalar("SELECT count(*)::int FROM farm_fish WHERE auto AND session_id IS NULL"); n != 2 {
		t.Fatal(n)
	}
	e.clock.Advance((3 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 1 {
		t.Fatalf("剩下的時間要累計：%+v", st)
	}
	// 魚簍 20 格：再過很久只會補滿，之後暫停、不累積
	e.clock.Advance((10 * time.Hour).Seconds())
	st = e.autoFish(pid)
	if st.Caught != 17 || !st.Paused || st.NextAt != nil || e.basketCount(pid) != 20 {
		t.Fatalf("%+v", st)
	}
	ov := must[FishingOverview](t)(e.f.FishingOverview(e.ctx, pid))
	if ov.Basket != 20 || ov.BasketCapacity != 20 || !ov.AutoFish.Paused {
		t.Fatalf("%+v", ov)
	}
	e.clock.Advance((time.Hour).Seconds())
	if st := e.autoFish(pid); st.Caught != 0 {
		t.Fatalf("魚簍滿的時候不累積：%+v", st)
	}
	// 賣掉 5 條後從現在重新計時
	ids := []int64{}
	for _, f := range must[FishBasket](t)(e.f.FishBasket(e.ctx, pid)).Fish[:5] {
		ids = append(ids, f.ID)
	}
	e.ok(e.f.SellFish(e.ctx, pid, ids))
	e.clock.Advance((5 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 1 || st.Paused {
		t.Fatalf("%+v", st)
	}
	e.invariant()
}

func TestAutoFisherWithAutoSellerAndToggle(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.s.AutoFishRng = func() float64 { return 0.3 }
	e.setGoldRecorded(pid, 300_000)
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoFisher, 1))
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoSeller, 1))
	gold := e.gold(pid)
	e.clock.Advance((10 * time.Minute).Seconds())
	st := e.autoFish(pid)
	if st.Caught != 2 || st.Sold != 2 || st.SoldGold <= 0 || !st.AutoSell || e.basketCount(pid) != 0 || e.gold(pid) != gold+st.SoldGold {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(st.Note(), "自動販售機賣出") {
		t.Fatal(st.Note())
	}
	// 自動販售最多補算 24 小時（288 條）
	e.clock.Advance((48 * time.Hour).Seconds())
	if st := e.autoFish(pid); st.Sold != 24*60/5 {
		t.Fatalf("%+v", st)
	}
	// 停用期間不累積；重新啟用從啟用的時間開始算
	e.ok(e.s.SetToolEnabled(e.ctx, pid, "自動釣魚機", false))
	e.clock.Advance((time.Hour).Seconds())
	if st := e.autoFish(pid); st.Caught != 0 || st.Enabled {
		t.Fatalf("%+v", st)
	}
	e.ok(e.s.SetToolEnabled(e.ctx, pid, "自動釣魚機", true))
	e.clock.Advance((4 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 0 {
		t.Fatalf("%+v", st)
	}
	e.clock.Advance((time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 1 {
		t.Fatalf("%+v", st)
	}
	// 停用自動販售機：之後釣到的放進魚簍
	e.ok(e.s.SetToolEnabled(e.ctx, pid, "自動販售機", false))
	e.clock.Advance((5 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 1 || st.Sold != 0 || e.basketCount(pid) != 1 {
		t.Fatalf("%+v", st)
	}
	// 排行榜只算自己釣的魚
	lb := must[fishrepo.Leaderboard](t)(e.f.FishingLeaderboard(e.ctx, nil))
	if len(lb.Catches) != 0 || len(lb.Best) != 0 {
		t.Fatalf("%+v", lb)
	}
	e.invariant()
}

func TestUpgradeFishBasket(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 29_999)
	_, err := e.s.UpgradeFishBasket(e.ctx, pid)
	wantKind(t, err, gameerr.InsufficientGold)
	e.setGoldRecorded(pid, 1_000_000)
	for i, want := range []struct{ capacity, price int }{{40, 30_000}, {60, 50_000}, {80, 100_000}, {100, 200_000}} {
		b := must[FishBasket](t)(e.f.FishBasket(e.ctx, pid))
		if b.NextCapacity != want.capacity || b.NextPrice != want.price {
			t.Fatalf("第 %d 次：%+v", i+1, b)
		}
		gold := e.gold(pid)
		r := e.ok(e.s.UpgradeFishBasket(e.ctx, pid))
		if !strings.Contains(r.Message, fmt.Sprintf("→ **%d** 格", want.capacity)) || e.gold(pid) != gold-want.price {
			t.Fatal(r.Message)
		}
		if b := must[FishBasket](t)(e.f.FishBasket(e.ctx, pid)); b.Capacity != want.capacity {
			t.Fatal(b.Capacity)
		}
	}
	if b := must[FishBasket](t)(e.f.FishBasket(e.ctx, pid)); b.NextCapacity != 0 {
		t.Fatalf("%+v", b)
	}
	_, err = e.s.UpgradeFishBasket(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經是最大的 100 格")
	if c := autofish.BasketCapacity(4, 20); c != 100 {
		t.Fatal(c)
	}
	e.invariant()
}
