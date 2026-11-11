package services

// Go 版新增功能（2026-11-12，Python 沒有）的服務測試：季節價格與當季種子、取消加工訂單、偷菜名單不列全是空地的農場。

import (
	"sort"
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seasons"
)

// offSeason 現在不是的那個季節（測試時鐘的日期決定）。
func (e *env) offSeason() string {
	for _, s := range []string{"spring", "summer", "autumn", "winter"} {
		if s != seasons.SeasonOf(e.clock.Now()) {
			return s
		}
	}
	return ""
}

func (e *env) setCropSeason(code, season string) {
	e.t.Helper()
	if season == "" {
		e.exec("UPDATE crop_types SET seasons = NULL WHERE code = $1", code)
		return
	}
	e.exec(`UPDATE crop_types SET seasons = json_build_array($2::text) WHERE code = $1`, code, season)
}

func TestSeasonSellPct(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) // 台灣 11 月：秋季
	if seasons.SellPct(nil, now) != 100 || seasons.SellPct([]string{"autumn", "winter"}, now) != 100 ||
		seasons.SellPct([]string{"spring"}, now) != seasons.OffSeasonSellPct {
		t.Fatal("季節倍率不對")
	}
	if seasons.SellPrice(15, []string{"spring"}, now) != 15*seasons.OffSeasonSellPct/100 || seasons.SellPrice(15, nil, now) != 15 {
		t.Fatal("季節售價不對")
	}
}

func TestShopSellsOnlyInSeasonSeeds(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGold(pid, 10_000)
	e.setCropSeason("carrot", e.offSeason())
	_, err := e.s.BuySeeds(e.ctx, pid, "carrot", 1)
	wantMsg(t, err, gameerr.Validation, "商店沒有販售胡蘿蔔種子")
	e.setCropSeason("carrot", seasons.SeasonOf(e.clock.Now()))
	e.ok(e.s.BuySeeds(e.ctx, pid, "carrot", 1)) // 當季可以買
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))  // 不限季節的作物一直有賣
	for _, c := range must[[]CropInfo](t)(e.s.GetMarketPrices(e.ctx)) {
		if c.Code == "carrot" && (!c.InSeason || c.SeasonSellPct != 100) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestOffSeasonProduceSellsHigher(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.give(pid, "carrot", 2)
	inSeason := e.ok(e.s.SellCrop(e.ctx, pid, "carrot", 1, "normal", "none")).GoldDelta
	e.setCropSeason("carrot", e.offSeason())
	off := e.ok(e.s.SellCrop(e.ctx, pid, "carrot", 1, "normal", "none")).GoldDelta
	if off != inSeason*seasons.OffSeasonSellPct/100 || off <= inSeason {
		t.Fatal(inSeason, off)
	}
	for _, c := range must[[]CropInfo](t)(e.s.GetMarketPrices(e.ctx)) { // 價目表顯示目前的價格
		if c.Code == "carrot" && (c.SeasonSellPct != seasons.OffSeasonSellPct || c.SellPrice != inSeason*seasons.OffSeasonSellPct/100) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestCancelCraftRefundsAndShiftsQueue(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 9)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1)) // 30 分
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1)) // 排隊
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 1)) // 排隊
	if e.qty(f, "wheat") != 0 {
		t.Fatal(e.qty(f, "wheat"))
	}
	e.clock.Advance(600) // 第一張做了 10 分鐘
	orders := e.orders(f, false)
	sort.Slice(orders, func(i, j int) bool { return orders[i].OrderID < orders[j].OrderID })
	first, second, third := orders[0], orders[1], orders[2]
	// 取消加工中的第一張：退回原料，第二張從現在開始，第三張跟著提早 20 分鐘
	r := e.ok(e.r.CancelCraft(e.ctx, f, first.OrderID))
	if !strings.Contains(r.Message, "已取消加工訂單") || !strings.Contains(r.Message, "小麥 x3") || e.qty(f, "wheat") != 3 {
		t.Fatal(r.Message, e.qty(f, "wheat"))
	}
	now := e.clock.Now()
	left := e.orders(f, false)
	sort.Slice(left, func(i, j int) bool { return left[i].OrderID < left[j].OrderID })
	if len(left) != 2 || left[0].OrderID != second.OrderID || !left[0].StartedAt.Equal(now) ||
		!left[0].ReadyAt.Equal(now.Add(30*time.Minute)) || !left[1].ReadyAt.Equal(third.ReadyAt.Add(-20*time.Minute)) {
		t.Fatalf("%+v", left)
	}
	// 取消排隊中的最後一張：前面的不受影響
	e.ok(e.r.CancelCraft(e.ctx, f, third.OrderID))
	if l := e.orders(f, false); len(l) != 1 || !l[0].ReadyAt.Equal(now.Add(30*time.Minute)) || e.qty(f, "wheat") != 6 {
		t.Fatalf("%+v", l)
	}
	// 已完成的不能取消；別人的、不存在的訂單也不行
	e.clock.Advance(1800)
	_, err := e.r.CancelCraft(e.ctx, f, second.OrderID)
	wantMsg(t, err, gameerr.Validation, "已經完成")
	_, err = e.r.CancelCraft(e.ctx, f, 999999)
	wantKind(t, err, gameerr.NotFound)
	other := e.farmer("別人", e.group("另一群"))
	_, err = e.r.CancelCraft(e.ctx, other, second.OrderID)
	wantKind(t, err, gameerr.NotFound)
}

func TestCancelCraftNeedsWarehouseSpace(t *testing.T) {
	e := setup(t)
	f := e.rancher()
	e.ok(e.r.Build(e.ctx, f, "磨坊"))
	e.give(f, "wheat", 3)
	order := e.ok(e.r.StartCraft(e.ctx, f, "flour", 1))
	_ = order
	e.exec("UPDATE farms SET warehouse_capacity = 1 WHERE player_id = $1", f)
	e.give(f, "carrot", 1)
	id := e.orders(f, false)[0].OrderID
	_, err := e.r.CancelCraft(e.ctx, f, id)
	wantKind(t, err, gameerr.WarehouseFull)
	if len(e.orders(f, false)) != 1 { // 放不下時不取消
		t.Fatal("不應取消")
	}
}

func TestStealPlayersSkipAllEmptyFarms(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	thief := e.farmer("小偷", g)
	planted := e.farmer("有種東西", g)
	e.farmer("全是空地", g)
	e.grow(planted, 1, "wheat") // 還沒成熟：沒有可偷的，但不是空地，仍然列出
	rows := must[[]StealPlayer](t)(e.so.StealPlayers(e.ctx, g, thief))
	if len(rows) != 1 || rows[0].PlayerID != planted || rows[0].Stealable != 0 {
		t.Fatalf("%+v", rows)
	}
}
