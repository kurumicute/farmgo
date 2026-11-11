package bot

// 對應 Python tests/test_discord_panel.py 的動物、加工、賭場頁測試與 tests/test_pets.py 的 test_panel_adopt_and_upgrade
// （第 5 階段第 5 批）；防守頁（巡查、防守紀錄、守護範圍）另外補上面板測試（Python 只有 CMD 的測試）。

import (
	"slices"
	"strings"
	"testing"

	"farmgm/internal/bot/ui"
	"farmgm/internal/bot/ui/uitest"
	"farmgm/internal/services"
)

// withDice 換成固定擲出 n 的服務（Python AdminService(..., dice=lambda: n)）。
func (e *botEnv) withDice(n int) { e.withOptions(services.Options{Dice: func() int { return n }}) }

// withOptions 換成指定亂數來源的服務（時鐘沿用測試時鐘）。
func (e *botEnv) withOptions(opt services.Options) {
	opt.Clock = e.clock
	e.admin = services.NewAdminService(e.d, opt)
	reg := ui.NewRegistry()
	reg.DirectReply = -1
	e.cmds = NewCommands(e.admin, NewRunner(2), reg)
	e.cmds.AutoCheckin = false
	e.cmds.onPanel = func(p *Panel) { e.last = p }
}

// submitModal 送出 opener（點按鈕或選選單）開啟的輸入框。
func (p *player) submitModal(opener *uitest.Interaction, value string) *uitest.Interaction {
	p.e.t.Helper()
	var m *ui.Modal
	for _, en := range opener.Entries() {
		if en.Kind == "modal" {
			m = en.Modal
		}
	}
	if m == nil {
		p.e.t.Fatalf("沒有開啟輸入框：%v", opener.Kinds())
	}
	it := p.interaction()
	p.e.cmds.Registry.DispatchModal(p.e.ctx, it, m.CustomID(), []string{value})
	return p.update(it)
}

func (e *botEnv) setGold(pid int64, gold int) {
	e.t.Helper()
	must[services.FarmValues](e.t)(e.admin.SetFarmValues(e.ctx, "root", pid, &gold, nil))
}

func notContains(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Fatalf("不應出現 %q：%s", u, text)
		}
	}
}

func TestAnimalsAndCraftPages(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.setGold(p.panel.playerID, 50_000)
	p.goPage("animals")
	p.click("建造畜舍")
	contains(t, p.Text, "建造")
	notContains(t, p.Text, "無法完成")
	p.choose("購買動物", "chicken")
	p.click("買 2")
	notContains(t, p.Text, "無法完成")
	p.click("全部餵食")
	contains(t, p.Text, "餵食", "生產中")
	p.choose("放生動物", "2")
	contains(t, p.Text, "確定要放生", "餵食不會退還")
	p.click("確認放生 #2")
	contains(t, p.Text, "已放生")
	if slices.Contains(p.options("放生動物"), "2") {
		t.Fatal(p.options("放生動物"))
	}
	p.goPage("craft")
	p.goPage("build")
	p.choose("選擇要建造的設施", "mill")
	p.goPage("craft")
	contains(t, p.Text, "閒置中")
	notContains(t, p.Text, "無法完成")
}

func TestCollectAllAnimals(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.setGold(p.panel.playerID, 50_000)
	p.goPage("animals")
	if !p.button("收集全部").Disabled {
		t.Fatal("沒有可收集的產品時應停用")
	}
	p.click("建造畜舍")
	p.choose("購買動物", "chicken")
	p.click("買 2")
	p.click("全部餵食")
	e.clock.Advance(7 * 24 * 3600)
	p.goPage("animals")
	contains(t, p.Text, "✅ 可收集")
	p.click("收集全部")
	contains(t, p.Text, "收集 2 隻動物的產品。", "待餵食")
	notContains(t, p.Text, "未完成")
	e.checkLedger()
}

func TestCraftButtonsFollowIngredientsAndQueue(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 50_000)
	p.goPage("build")
	p.choose("選擇要建造的設施", "mill")
	e.setInventory(pid, "wheat", 21) // 夠做 7 份
	p.goPage("craft")
	contains(t, p.Text, "現有原料可做 **7** 份")
	p.choose("開始加工", "flour")
	var made []string
	for _, l := range p.labels() {
		if strings.HasPrefix(l, "做 ") {
			made = append(made, l)
		}
	}
	if !slices.Equal(made, []string{"做 1", "做 5", "做 7"}) {
		t.Fatal(made)
	}
	p.click("做 7")
	contains(t, p.Text, "✅", "7 份麵粉")
	notContains(t, p.Text, "排在")
	e.setInventory(pid, "wheat", 6)
	p.choose("開始加工", "flour")
	p.click("做 2")
	contains(t, p.Text, "排在訂單 #1 之後", "⏳ 排隊 1 張")
}

func TestCraftCollectFinishedOrders(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 50_000)
	p.goPage("build")
	p.choose("選擇要建造的設施", "mill")
	e.setInventory(pid, "wheat", 3)
	p.goPage("craft")
	p.choose("開始加工", "flour")
	contains(t, p.Text, "現有原料最多可做 **1** 份")
	p.click("做 1")
	if !p.button("領取完成品（0）").Disabled {
		t.Fatal("沒有完成品時應停用")
	}
	p.choose("開始加工", "flour")
	contains(t, p.Text, "⚠️ 原料不足，一份都做不了")
	e.clock.Advance(24 * 3600)
	p.goPage("craft")
	p.click("領取完成品（1）")
	contains(t, p.Text, "✅", "麵粉")
	notContains(t, p.Text, "無法完成")
	e.checkLedger()
}

func TestPanelAdoptAndUpgradePet(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 100_000)
	p.goPage("animals")
	contains(t, p.Text, "寵物", "還沒有領養")
	p.click("寵物（貓狗）")
	contains(t, p.Text, "🐾", "領養", "每格實際多收", "領養後（+5%）", "小麥（每格 2 個）：10% 機率 +1")
	p.click("領養狗狗")
	contains(t, p.Text, "再按一次")
	p.click("確認領養狗狗")
	contains(t, p.Text, "領養了狗狗")
	if len(p.panel.files) == 0 { // 寵物頁附上有寵物的農場圖
		t.Fatal("寵物頁沒有圖片")
	}
	p.click("升級 Lv1狗狗")
	p.click("確認升級 Lv1狗狗")
	contains(t, p.Text, "Lv1", "10%")
	st := must[services.FarmStatus](t)(e.admin.GetFarmStatus(e.ctx, pid))
	if len(st.Pets) != 1 || st.Pets["dog"] != 1 {
		t.Fatal(st.Pets)
	}
	e.checkLedger()
}

func TestDefensePageShopPatrolAndLog(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 50_000)
	p.goPage("defense")
	contains(t, p.Text, "還沒有防守設施。", "還沒有狗狗", "稻草人", "巡查券：**0** 張")
	if !p.button("巡查（消耗 1 張，持有 0）").Disabled {
		t.Fatal("沒有巡查券時應停用")
	}
	p.choose("購買防守設施或巡查券", "patrol_ticket")
	contains(t, p.Text, "巡查券：**1** 張")
	p.choose("購買防守設施或巡查券", "scarecrow")
	contains(t, p.Text, "稻草人 #1", "守護第 1–3 格", "`#1` 25%", "`#4` 0%")

	// 調整守護範圍：選設施後填起始農田編號
	p.submitModal(p.choose("調整稻草人／圍欄的守護範圍", "scarecrow|1"), "2")
	contains(t, p.Text, "第 2–4 格")

	// 防守紀錄與巡查以私密訊息回覆；巡查消耗 1 張後重繪面板
	it := p.click("被阻止的偷菜紀錄")
	contains(t, followupText(t, it), "目前沒有被阻止的偷菜紀錄。")
	it = p.click("巡查（消耗 1 張，持有 1）")
	contains(t, followupText(t, it), "🔦 巡查報告", "已消耗 1 張巡查券", "近期沒有任何偷菜嘗試。")
	contains(t, p.Text, "巡查券：**0** 張")
	e.checkLedger()
}

// followupText 最後一則私密 followup 的內容（文字或 Embed）。
func followupText(t *testing.T, it *uitest.Interaction) string {
	t.Helper()
	text := ""
	for _, en := range it.Entries() {
		if en.Kind == "followup" {
			if !en.Message.Ephemeral {
				t.Fatal("報告應是私密訊息")
			}
			text = en.Text()
		}
	}
	if text == "" {
		t.Fatal("沒有 followup")
	}
	return text
}

func TestCasinoBetFromPanel(t *testing.T) {
	e := setup(t)
	e.withDice(8)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	if _, err := e.admin.SetInventory(e.ctx, "root", pid, "wheat", "rare", "none", 4); err != nil {
		t.Fatal(err)
	}
	e.setInventory(pid, "carrot", 9) // 普通作物不能下注
	p.goPage("casino")
	contains(t, p.Text, "1–10", "×2", "×5")
	if got := p.options("選擇要下注的作物"); !slices.Equal(got, []string{"wheat|rare|none"}) {
		t.Fatal(got)
	}
	p.choose("選擇要下注的作物", "wheat|rare|none")
	p.click("全部（4）")
	contains(t, p.Text, "小麥【稀有】 ×4")
	p.click("比大") // 擲出 8：贏
	contains(t, p.Text, "擲出 **8**", "你贏了", "🏆 我的賭場獎品", "×2")
	notContains(t, p.Text, "無法完成")
	p.goPage("sell")
	contains(t, p.Text, "賭場獎品")
	var key string
	for _, v := range p.options("選擇要賣的東西") {
		if strings.HasPrefix(v, "z|") {
			key = v
		}
	}
	p.choose("選擇要賣的東西", key)
	p.click("賣 1")
	contains(t, p.Text, "已出售 1 個小麥【稀有】（賭場 ×2")
	// 獎品要明確勾選才會賣（「全部賣出」不賣獎品）
	p.chooseMany("勾選多項", []string{key})
	p.click("確認賣出 1 項")
	contains(t, p.Text, "賣出 3 個商品")
	for _, v := range p.options("選擇要賣的東西") {
		if strings.HasPrefix(v, "z|") {
			t.Fatal(v)
		}
	}
	e.checkLedger()
}

func TestCasinoLossAndCustomQuantity(t *testing.T) {
	e := setup(t)
	e.withDice(8)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	if _, err := e.admin.SetInventory(e.ctx, "root", pid, "carrot", "normal", "golden", 3); err != nil {
		t.Fatal(err)
	}
	p.goPage("casino")
	p.choose("選擇要下注的作物", "carrot|normal|golden")
	p.modal("自訂數量…", "2")
	contains(t, p.Text, "×2**（價值")
	p.choose("猜一個數字", "3") // 擲出 8：沒猜中
	contains(t, p.Text, "你輸了")
	if lines := strings.Split(p.Text, "\n"); len(lines) < 2 || strings.Contains(lines[1], "✅") {
		t.Fatal("輸了不顯示成功符號：", p.Text)
	}
	// 還剩 1 個：下注數量自動降到持有數量
	contains(t, p.Text, "×1**（價值")
	p.choose("猜一個數字", "3")
	if got := p.options("選擇要下注的作物"); len(got) != 0 {
		t.Fatal(got)
	}
	e.checkLedger()
}
