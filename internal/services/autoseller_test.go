package services

// 自動販售機的販售項目（2026-11-15）。

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

func TestAutoSellerItems(t *testing.T) {
	e := setup(t)
	pid := e.rich()
	_, err := e.s.SetAutoSellerItems(e.ctx, pid, []string{"carrot"})
	wantKind(t, err, gameerr.NotFound)
	if v := must[AutoSellerView](t)(e.s.AutoSeller(e.ctx, pid)); v.Owned {
		t.Fatalf("%+v", v)
	}
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoSeller, 1))
	v := must[AutoSellerView](t)(e.s.AutoSeller(e.ctx, pid))
	if !v.Owned || !v.All || len(v.Options) < 3 || v.Options[len(v.Options)-1].Code != AutoSellFish ||
		v.Options[len(v.Options)-2].Code != AutoSellHybrid {
		t.Fatalf("%+v", v)
	}
	for _, o := range v.Options {
		if !o.Selected || o.Code == "golden_grain" {
			t.Fatalf("預設全選、雜交作物不個別列出：%+v", o)
		}
	}
	_, err = e.s.SetAutoSellerItems(e.ctx, pid, []string{"ufo"})
	wantMsg(t, err, gameerr.Validation, "至少勾選一項")

	// 只賣胡蘿蔔：小麥收成放進倉庫
	r := e.ok(e.s.SetAutoSellerItems(e.ctx, pid, []string{"carrot", "ufo"}))
	if !strings.Contains(r.Message, "只賣：胡蘿蔔") {
		t.Fatal(r.Message)
	}
	v = must[AutoSellerView](t)(e.s.AutoSeller(e.ctx, pid))
	if v.All {
		t.Fatalf("%+v", v)
	}
	for _, o := range v.Options {
		if o.Selected != (o.Code == "carrot") {
			t.Fatalf("%+v", o)
		}
	}
	e.grow(pid, 1, "carrot")
	e.grow(pid, 2, "wheat")
	e.clock.Advance(180)
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)); !strings.Contains(r.Message, "自動販售機賣出") {
		t.Fatal(r.Message)
	}
	if r := e.ok(e.s.HarvestCrop(e.ctx, pid, 2)); strings.Contains(r.Message, "自動販售機") {
		t.Fatal(r.Message)
	}
	if bp := must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)); len(bp.Produce) != 1 || bp.Produce[0].Code != "wheat" {
		t.Fatalf("%+v", bp.Produce)
	}
	// 全部勾選：改回沒有限制
	var all []string
	for _, o := range v.Options {
		all = append(all, o.Code)
	}
	if r := e.ok(e.s.SetAutoSellerItems(e.ctx, pid, all)); !strings.Contains(r.Message, "全部都賣") {
		t.Fatal(r.Message)
	}
	if v := must[AutoSellerView](t)(e.s.AutoSeller(e.ctx, pid)); !v.All {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}

// 沒勾選魚時，自動釣魚機的魚放進魚簍。
func TestAutoSellerWithoutFish(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.s.AutoFishRng = func() float64 { return 0.3 }
	e.autoBait(pid, 1000)
	e.setGoldRecorded(pid, 300_000)
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoFisher, 1))
	e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolAutoSeller, 1))
	e.ok(e.s.SetAutoSellerItems(e.ctx, pid, []string{"wheat"}))
	e.clock.Advance((10 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Caught != 2 || st.Sold != 0 || st.AutoSell || e.basketCount(pid) != 2 {
		t.Fatalf("%+v", st)
	}
	// 改成勾選魚：先結算到現在，之後釣到的直接賣
	e.clock.Advance((5 * time.Minute).Seconds())
	r := e.ok(e.s.SetAutoSellerItems(e.ctx, pid, []string{"wheat", AutoSellFish}))
	if !strings.Contains(r.Message, "放進魚簍") && e.basketCount(pid) != 3 {
		t.Fatal(r.Message, e.basketCount(pid))
	}
	e.clock.Advance((5 * time.Minute).Seconds())
	if st := e.autoFish(pid); st.Sold != 1 || e.basketCount(pid) != 3 {
		t.Fatalf("%+v %d", st, e.basketCount(pid))
	}
	e.invariant()
}
