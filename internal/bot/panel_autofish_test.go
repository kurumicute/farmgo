package bot

// 自動釣魚機的狀態與魚簍升級（Go 版新增，2026-11-15）。

import (
	"testing"
	"time"

	"farmgm/internal/game/balance"
)

func TestAutoFisherAndBasketUpgradePanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.admin.AutoFishRng = func() float64 { return 0.3 }
	e.setGold(pid, 300_000)
	if _, err := e.admin.BuyItem(e.ctx, pid, balance.ToolAutoFisher, 1); err != nil {
		t.Fatal(err)
	}
	p.goPage("fishing")
	contains(t, p.Text, "🤖 自動釣魚機（每 5 分鐘 1 條）：⏸️ 沒有裝備魚餌，暫停中")
	for _, g := range []struct {
		code string
		qty  int
	}{{"bamboo_rod", 1}, {"basic_line", 1}, {"worm_bait", 20}} {
		if _, err := e.admin.BuyFishingGear(e.ctx, pid, g.code, g.qty); err != nil {
			t.Fatal(err)
		}
	}
	p.goPage("fishing")
	contains(t, p.Text, "🤖 自動釣魚機（每 5 分鐘 1 條）：運作中，下一條", "還能釣 **20** 條", "🧺 魚簍：**0 / 20**")
	e.clock.Advance((10 * time.Minute).Seconds())
	p.goPage("fishing")
	contains(t, p.Text, "🤖 自動釣魚機釣到 2 條魚（用掉 2 個魚餌），已放進魚簍。", "🧺 魚簍：**2 / 20**", "還能釣 **18** 條")
	// 加速：按兩次確認
	p.click("釣魚機加速（4 分鐘，50,000 金）")
	contains(t, p.Text, "再按一次「確認加速」")
	p.click("確認加速")
	contains(t, p.Text, "每 5 分鐘 → 每 **4** 分鐘")
	p.goPage("fishing")
	contains(t, p.Text, "🤖 自動釣魚機（每 4 分鐘 1 條）")

	p.goPage("fish_basket")
	contains(t, p.Text, "🧺 **2 / 20** 隻", "🤖 自動釣魚機")
	p.click("升級魚簍（40 格，30,000 金）")
	contains(t, p.Text, "再按一次「確認升級魚簍」")
	p.click("確認升級魚簍")
	contains(t, p.Text, "魚簍升級：20 → **40** 格")
	p.goPage("fish_basket")
	contains(t, p.Text, "🧺 **2 / 40** 隻")
	if p.button("升級魚簍（60 格，50,000 金）") == nil {
		t.Fatal("下一級的按鈕")
	}
	e.checkLedger()
}
