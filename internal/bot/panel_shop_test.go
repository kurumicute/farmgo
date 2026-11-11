package bot

// 對應 Python tests/test_discord_panel.py 的商店、作物價格、品質與販售頁測試（第 5 階段第 4 批），
// 以及 tests/test_latency.py 的 test_panel_records_open_goto_and_act。

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"farmgm/internal/bot/latency"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/services"
)

// chooseMany 可複選的選單一次選多項（Python choose 傳入 list）。
func (p *player) chooseMany(placeholder string, values []string) {
	p.e.t.Helper()
	s := p.selectMenu(placeholder)
	if s == nil {
		p.e.t.Fatalf("找不到選單「%s」", placeholder)
	}
	it := p.interaction()
	if !p.e.cmds.Registry.DispatchComponent(p.e.ctx, it, s.CustomID(), values) {
		p.e.t.Fatalf("選單「%s」沒有登記", placeholder)
	}
	p.update(it)
}

// defaults 所有選單中顯示為已選取的選項。
func (p *player) defaults(valueOf func(ui.SelectOption) string) []string {
	var out []string
	for _, s := range p.panel.View.Selects() {
		for _, o := range s.Options {
			if o.Default {
				out = append(out, valueOf(o))
			}
		}
	}
	return out
}

func optLabel(o ui.SelectOption) string { return o.Label }

func (e *botEnv) setInventory(pid int64, code string, qty int) {
	e.t.Helper()
	if _, err := e.admin.SetInventory(e.ctx, "root", pid, code, "normal", "none", qty); err != nil {
		e.t.Fatal(err)
	}
}

func (e *botEnv) gold(pid int64) int {
	e.t.Helper()
	return must[services.FarmStatus](e.t)(e.admin.GetFarmStatus(e.ctx, pid)).Gold
}

func (e *botEnv) checkLedger() {
	e.t.Helper()
	if gold, delta := e.scalar("SELECT sum(gold)::bigint FROM farms"), e.scalar("SELECT sum(gold_delta)::bigint FROM game_transactions"); gold != delta {
		e.t.Fatal(gold, delta)
	}
}

func TestShopQuantityPickerAndCustomAmount(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	p.choose("選擇要買的商品", "seed:carrot")
	contains(t, p.Text, "已選擇\n**🥕 胡蘿蔔種子**")
	p.click("買 5")
	contains(t, p.Text, "已購買 5 顆胡蘿蔔種子")
	p.choose("選擇要買的商品", "seed:wheat")
	p.modal("自訂數量", "3")
	contains(t, p.Text, "已購買 3 顆小麥種子")
	p.choose("選擇要買的商品", "item:auto_waterer")
	if strings.Contains(p.Text, "[失敗]") {
		t.Fatal(p.Text)
	}
}

func TestSellPickerAndErrorsShowInline(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	p.click("全部澆水")
	e.clock.Advance(61)
	p.click("全部收成")
	p.goPage("sell")
	key := p.options("選擇要賣的東西")[0]
	p.choose("選擇要賣的東西", key)
	p.click("賣 1")
	contains(t, p.Text, "已出售 1 個小麥")
	p.goPage("plots")
	p.click("擴建")
	contains(t, p.Text, "再按一次")
	p.click("確認擴建")
	contains(t, p.Text, "無法完成", "金幣不足") // 錯誤改為獨立畫面
}

func TestSellTickedItems(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setInventory(pid, "wheat", 5)
	e.setInventory(pid, "carrot", 3)
	e.setInventory(pid, "tomato", 2)
	p.goPage("sell")
	gold := e.gold(pid)
	keys := p.options("勾選多項")
	pickKey := func(sub string) string {
		i := slices.IndexFunc(keys, func(k string) bool { return strings.Contains(k, sub) })
		if i < 0 {
			t.Fatal(sub, keys)
		}
		return keys[i]
	}
	wheat, carrot := pickKey("wheat"), pickKey("carrot")
	hasSellButton := func() bool {
		return slices.ContainsFunc(p.labels(), func(l string) bool { return strings.HasPrefix(l, "賣 ") })
	}
	p.choose("選擇要賣的東西", wheat) // 勾選會清掉單項的數量按鈕，單項選擇也會清掉勾選
	if !hasSellButton() {
		t.Fatal(p.labels())
	}
	p.chooseMany("勾選多項", []string{wheat, carrot})
	if hasSellButton() {
		t.Fatal(p.labels())
	}
	contains(t, p.Text, "已勾選", "共 **2** 項")
	if e.gold(pid) != gold { // 確認前不會賣
		t.Fatal("確認前就賣出了")
	}
	// 勾選在重繪後仍顯示為已選取
	var ticked []string
	for _, o := range p.selectMenu("勾選多項").Options {
		if o.Default {
			ticked = append(ticked, o.Value)
		}
	}
	slices.Sort(ticked)
	want := []string{wheat, carrot}
	slices.Sort(want)
	if !slices.Equal(ticked, want) {
		t.Fatal(ticked)
	}
	p.click("取消勾選")
	if strings.Contains(p.Text, "已勾選") {
		t.Fatal(p.Text)
	}
	p.chooseMany("勾選多項", []string{wheat, carrot})
	p.click("確認賣出 2 項")
	contains(t, p.Text, "賣出 8 個商品")
	if strings.Contains(p.Text, "已勾選") || e.gold(pid) <= gold {
		t.Fatal(p.Text)
	}
	left := strings.Join(p.options("勾選多項"), " ")
	if !strings.Contains(left, "tomato") || strings.Contains(left, "wheat") || strings.Contains(left, "carrot") { // 沒勾的留著
		t.Fatal(left)
	}
	e.checkLedger()
}

func TestSellTickedSellsShownQuantityAndReportsFailures(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setInventory(pid, "wheat", 5)
	e.setInventory(pid, "carrot", 3)
	p.goPage("sell")
	p.chooseMany("勾選多項", p.options("勾選多項"))
	e.setInventory(pid, "wheat", 9)  // 確認前多出來的不賣
	e.setInventory(pid, "carrot", 1) // 確認前變少的不會賣成負數
	p.click("確認賣出 2 項")
	contains(t, p.Text, "賣出 5 個商品", "未賣出", "胡蘿蔔")
	bp := must[services.Backpack](t)(e.admin.GetBackpack(e.ctx, pid))
	got := map[string]int{}
	for _, x := range bp.Produce {
		got[x.Code] = x.Quantity
	}
	if len(got) != 2 || got["wheat"] != 4 || got["carrot"] != 1 {
		t.Fatal(got)
	}
}

func TestShopKeepsSelectionAndShowsHoldings(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	contains(t, p.Text, "持有", "🧪", "✦")
	p.choose("選擇要買的商品", "seed:carrot")
	if got := p.defaults(optLabel); !slices.Equal(got, []string{"胡蘿蔔種子"}) {
		t.Fatal(got)
	}
	contains(t, p.Text, "已選擇")
	p.click("買 5")
	contains(t, p.Text, "已購買 5 顆胡蘿蔔種子", "現在持有 **5**")
	// 買完後仍停在胡蘿蔔，可以直接再買
	if got := p.defaults(optLabel); !slices.Equal(got, []string{"胡蘿蔔種子"}) {
		t.Fatal(got)
	}
	p.click("買 1")
	contains(t, p.Text, "現在持有 **6**")
}

func TestShopSingleSelectAndReadablePrices(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	selects := p.panel.View.Selects()
	if len(selects) != 1 {
		t.Fatal(len(selects))
	}
	values := p.options("選擇要買的商品")
	if !slices.Contains(values, "seed:wheat") || !slices.Contains(values, "item:fertilizer") {
		t.Fatal(values)
	}
	p.choose("選擇要買的商品", "item:fertilizer")
	p.click("買 1")
	contains(t, p.Text, "化肥", "現在持有 **1**")
	p.goPage("prices")
	contains(t, p.Text, "黃金作物", "珍稀變種")
	if strings.Contains(p.Text, "```") {
		t.Fatal(p.Text)
	}
	p.goPage("genetics")
	contains(t, p.Text, "傳說", "巨型作物")
	if strings.Contains(p.Text, "```") {
		t.Fatal(p.Text)
	}
}

func TestShopLuxuryConfirmAndWarehouse(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	gold := 2_000_000
	must[services.FarmValues](t)(e.admin.SetFarmValues(e.ctx, "root", p.panel.playerID, &gold, nil))
	p.goPage("shop")
	contains(t, p.Text, "收成 ×2 🌾小麥", "🎒 持有 **0**", "🔒 未擁有")
	p.choose("選擇要買的商品", "item:auto_seller")
	price := balance.Items[balance.ToolAutoSeller].Price
	if got := p.labels()[0]; got != fmt.Sprintf("確認購買（%s 金）", money(price)) {
		t.Fatal(got)
	}
	p.click("確認購買")
	contains(t, p.Text, "已購買 1 個自動販售機")
	if slices.Contains(p.options("選擇要買的商品"), "item:auto_seller") { // 已擁有的工具不再出現在選單
		t.Fatal(p.options("選擇要買的商品"))
	}
	p.choose("選擇要買的商品", "special:warehouse")
	p.click("確認購買")
	contains(t, p.Text, "倉庫擴大為 100 格", "20,000")
}

func TestPanelRecordsOpenGotoAndAct(t *testing.T) {
	// Python test_latency.py 的 test_panel_records_open_goto_and_act：開啟面板、換頁與操作（含失敗）都有記錄。
	e := setup(t)
	e.cmds.Latency = latency.New()
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	p.click("回總覽")  // 一般按鈕也是經 goto
	p.click("全部收成") // 沒有成熟的作物：操作失敗也要記錄（按鈕停用，直接觸發處理函式）
	names := map[string]int{}
	for _, r := range e.cmds.Latency.Summary() {
		names[r.Name] = r.Count
	}
	if names["開啟面板"] != 1 || names["換頁 shop"] != 1 || names["換頁 main"] != 1 {
		t.Fatal(names)
	}
	acted := false
	for n := range names {
		acted = acted || strings.HasPrefix(n, "操作 ") || strings.HasPrefix(n, "操作失敗 ")
	}
	if !acted {
		t.Fatal(names)
	}
}
