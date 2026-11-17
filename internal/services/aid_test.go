package services

import (
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
)

// 送禮與互助（Go 版新增，2026-11-17）：門檻、每日上限、作物轉移、雙方獎勵與幫忙澆水。
func TestAidGiftAndWater(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	a, b, c := e.farmer("阿明", g), e.farmer("小美", g), e.farmer("小華", g)
	e.give(a, "wheat", 30)

	// 等級門檻（預設 Lv.5）與農場天數（預設 3 天）
	_, err := e.s.SendGift(e.ctx, g, a, b, "wheat", 5)
	wantMsg(t, err, gameerr.Validation, "Lv.5")
	e.exec("UPDATE farms SET level = 5")
	_, err = e.s.SendGift(e.ctx, g, a, b, "wheat", 5)
	wantMsg(t, err, gameerr.Validation, "建立滿 3 天")
	e.exec("UPDATE farms SET created_at = '2025-01-01'")

	v := must[AidView](t)(e.s.AidOverview(e.ctx, g, a))
	if v.Blocked != "" || len(v.Targets) != 2 || len(v.Items) != 1 || v.Items[0].Code != "wheat" || v.Items[0].Limit != 10 {
		t.Fatalf("%+v", v)
	}
	_, err = e.s.SendGift(e.ctx, g, a, b, "wheat", 11)
	wantMsg(t, err, gameerr.Validation, "最多送 10 個")
	goldA, goldB := e.gold(a), e.gold(b)
	if r := e.ok(e.s.SendGift(e.ctx, g, a, b, "wheat", 10)); !strings.Contains(r.Message, "雙方各得 30 金幣") {
		t.Fatal(r.Message)
	}
	if e.qty(a, "wheat") != 20 || e.qty(b, "wheat") != 10 || e.gold(a) != goldA+30 || e.gold(b) != goldB+30 {
		t.Fatal(e.qty(a, "wheat"), e.qty(b, "wheat"), e.gold(a), e.gold(b))
	}
	// 每天只能送 1 份
	_, err = e.s.SendGift(e.ctx, g, a, c, "wheat", 1)
	wantMsg(t, err, gameerr.Validation, "今天已經送過 1 份")
	// 不能送自己、不能送有突變的
	_, err = e.s.SendGift(e.ctx, g, a, a, "wheat", 1)
	wantMsg(t, err, gameerr.Validation, "自己")

	// 幫忙澆水：把對方所有等待澆水的土地澆好，算一次
	e.ok(e.s.BuySeeds(e.ctx, b, "wheat", 2))
	e.ok(e.s.PlantCrop(e.ctx, b, 1, "wheat"))
	e.ok(e.s.PlantCrop(e.ctx, b, 2, "wheat"))
	if r := e.ok(e.s.HelpWater(e.ctx, g, a, b)); !strings.Contains(r.Message, "澆了 2 格") || !strings.Contains(r.Message, "各得 20 金幣") {
		t.Fatal(r.Message)
	}
	if e.plot(b, 1).State != rules.PlotGrowing || e.plot(b, 2).State != rules.PlotGrowing {
		t.Fatal("沒有澆到水")
	}
	_, err = e.s.HelpWater(e.ctx, g, c, b)
	wantMsg(t, err, gameerr.Validation, "沒有需要澆水")
	e.ok(e.s.BuySeeds(e.ctx, b, "wheat", 1))
	e.ok(e.s.PlantCrop(e.ctx, b, 3, "wheat"))
	_, err = e.s.HelpWater(e.ctx, g, a, b)
	wantMsg(t, err, gameerr.Validation, "今天已經幫 小美 澆過水")
	e.ok(e.s.HelpWater(e.ctx, g, c, b))

	// 隔天（台灣日期）重新計算
	e.clock.Advance(24 * 3600)
	e.ok(e.s.SendGift(e.ctx, g, a, c, "wheat", 3))
	if n := e.scalar("SELECT count(*) FROM game_transactions WHERE event_type IN ('aid_gift', 'aid_water')"); n != 8 {
		t.Fatal(n)
	}
	e.invariant()
}

// 收禮上限：每人每天最多收 2 份（預設）。
func TestAidGiftReceiveLimit(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	a, b, c, d := e.farmer("阿明", g), e.farmer("小美", g), e.farmer("小華", g), e.farmer("小強", g)
	e.exec("UPDATE farms SET level = 5, created_at = '2025-01-01'")
	for _, p := range []int64{a, b, c} {
		e.give(p, "wheat", 5)
	}
	e.ok(e.s.SendGift(e.ctx, g, a, d, "wheat", 1))
	e.ok(e.s.SendGift(e.ctx, g, b, d, "wheat", 1))
	_, err := e.s.SendGift(e.ctx, g, c, d, "wheat", 1)
	wantMsg(t, err, gameerr.Validation, "已經收到 2 份")
}
