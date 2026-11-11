package bot

// Go 版新增功能（2026-11-12）的面板測試：加工頁取消訂單、商店只列當季種子、價目表顯示非當季加價。

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"farmgm/internal/game/seasons"
)

func TestCraftPageCancelOrder(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 50_000)
	p.goPage("build")
	p.choose("選擇要建造的設施", "mill")
	e.setInventory(pid, "wheat", 6)
	p.goPage("craft")
	p.choose("開始加工", "flour")
	p.click("做 1")
	p.choose("開始加工", "flour")
	p.click("做 1")
	ids := p.options("取消加工訂單")
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	p.choose("取消加工訂單", ids[0])
	contains(t, p.Text, "確定要取消加工訂單", "再按一次「確認取消」")
	p.click("確認取消 #" + ids[0])
	contains(t, p.Text, "已取消加工訂單 #"+ids[0], "退回原料 小麥 x3")
	if got := p.options("取消加工訂單"); len(got) != 1 || slices.Contains(got, ids[0]) {
		t.Fatal(got)
	}
	e.checkLedger()
}

func TestShopListsOnlyInSeasonSeedsAndPricesShowSeason(t *testing.T) {
	e := setup(t)
	now := time.Now() // 面板以真實時間判斷季節（與服務層的季節判斷使用相同的月份規則）
	cur := seasons.SeasonOf(now)
	off := "spring"
	if cur == "spring" {
		off = "summer"
	}
	e.exec("UPDATE crop_types SET seasons = json_build_array($1::text) WHERE code = 'carrot'", off)
	e.exec("UPDATE crop_types SET seasons = json_build_array($1::text) WHERE code = 'tomato'", cur)
	e.clock.Set(now)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	opts := p.options("選擇要買的商品")
	if slices.Contains(opts, "seed:carrot") || !slices.Contains(opts, "seed:tomato") || !slices.Contains(opts, "seed:wheat") {
		t.Fatal(opts)
	}
	contains(t, p.Text, "商店只賣當季可以種的種子")
	p.goPage("prices")
	contains(t, p.Text, fmt.Sprintf("📈 非當季售價 +%d%%", seasons.OffSeasonSellPct-100), "非當季的作物售價")
}
