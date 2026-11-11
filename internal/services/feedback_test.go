package services

// 對應 Python tests/test_feedback.py 的服務層測試：收成摘要與個人紀錄、販售紀錄與金幣變化、升級前後對比
// （歡迎回來、圖鑑與成就標記在 progress_test.go；quote 與面板測試屬於第 5 階段）。

import (
	"strings"
	"testing"

	"farmgm/internal/numfmt"
)

func TestHarvestAllSummaryAndRecord(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(61)
	first := e.ok(e.s.HarvestAll(e.ctx, pid)).Message
	for _, want := range []string{"🧺 收成 4 格，共 8 個作物。", "小麥 ×8", "約值"} {
		if !strings.Contains(first, want) {
			t.Fatalf("缺少 %q：%s", want, first)
		}
	}
	if strings.Contains(first, "🏆") {
		t.Fatal("第一次只記錄，不顯示新紀錄：" + first)
	}
	e.setGoldRecorded(pid, 1_000_000)
	e.ok(e.s.UpgradeFarm(e.ctx, pid)) // 5 格
	e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
	e.ok(e.s.WaterAll(e.ctx, pid))
	e.clock.Advance(61)
	if second := e.ok(e.s.HarvestAll(e.ctx, pid)).Message; !strings.Contains(second, "🏆 新紀錄！一次收成 10 個作物（舊紀錄 8）") {
		t.Fatal(second)
	}
}

func TestSaleRecordAndGoldChange(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.grow(pid, 1, "wheat")
	e.clock.Advance(61)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	gold := e.gold(pid)
	r := e.ok(e.r.Sell(e.ctx, pid, "wheat", 1, "normal", "none"))
	if want := "💰 金幣 " + numfmt.Comma(gold) + " → " + numfmt.Comma(gold+r.GoldDelta); !strings.Contains(r.Message, want) {
		t.Fatalf("缺少 %q：%s", want, r.Message)
	}
	note := func(income int) string {
		t.Helper()
		s, err := e.s.NoteSale(e.ctx, pid, income)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := note(r.GoldDelta); s != "" {
		t.Fatal("第一次只記錄：" + s)
	}
	if s := note(r.GoldDelta * 2); !strings.Contains(s, "🏆 新紀錄！一次販售") {
		t.Fatal(s)
	}
	if s := note(1); s != "" {
		t.Fatal("沒有破紀錄：" + s)
	}
}

func TestUpgradeMessagesShowBeforeAndAfter(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000_000)
	if m := e.ok(e.s.UpgradeFarm(e.ctx, pid)).Message; !strings.Contains(m, "🌱 農田 4 → 5 格｜⭐ 等級 1 → 2") {
		t.Fatal(m)
	}
	if m := e.ok(e.s.UpgradeGreenhouse(e.ctx, pid)).Message; !strings.Contains(m, "⏱️ 生長時間 -0% → -") {
		t.Fatal(m)
	}
	e.ok(e.r.BuyPet(e.ctx, pid, "cat"))
	if m := e.ok(e.r.UpgradePet(e.ctx, pid, "cat")).Message; !strings.Contains(m, "📈 Lv0 → Lv1：5% → 10%") {
		t.Fatal(m)
	}
}
