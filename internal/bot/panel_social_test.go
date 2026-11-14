package bot

// 對應 Python tests/test_discord_panel.py 的偷菜、排行榜、群組轉移、山洞、機器、錯誤畫面與 test_every_page_renders
// （第 5 階段第 6 批）。

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"farmgm/internal/services"
)

const carol = "900000000000000003"

// allPages Python PAGES（test_every_page_renders）。
var allPages = []string{"main", "plots", "backpack", "shop", "prices", "genetics", "sell", "animals", "craft", "build", "casino",
	"cave", "cave_area", "cave_log", "machines", "defense", "steal",
	"board", "rewards", "quests", "history", "group", "log", "help",
	"fishing", "fish_shop", "fish_basket", "fish_codex", "fish_rank",
	"progress", "codex", "achievements", "titles", "shop_special", "pets", "ruins"}

func TestEveryPageRenders(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	for _, page := range allPages {
		p.goPage(page)
		if strings.Contains(p.Text, "無法顯示") {
			t.Fatal(page, p.Text)
		}
		items := p.panel.View.Items()
		if len(items) == 0 || len(items) > 25 {
			t.Fatal(page, len(items))
		}
		for _, it := range items {
			if it.ItemRow() > 4 {
				t.Fatal(page, it.ItemRow())
			}
		}
	}
}

func (e *botEnv) waterWheat(p *player) {
	e.t.Helper()
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	p.click("全部澆水")
}

func TestStealFromPanelOnlySameGuild(t *testing.T) {
	e := setup(t)
	a := e.makePlayer(alice, guildA, "Alice")
	b := e.makePlayer(bob, guildA, "Bob")
	e.waterWheat(b)
	e.clock.Advance(61)
	a.goPage("steal")
	targets := a.options("選擇要偷的作物")
	if len(targets) == 0 {
		t.Fatal("沒有可偷的作物")
	}
	for _, x := range targets {
		if !strings.HasPrefix(x, fmt64(b.panel.playerID)+"|") {
			t.Fatal(targets)
		}
	}
	a.choose("選擇要偷的作物", targets[0])
	notContains(t, a.Text, "無法完成")
	other := e.open(alice, guildB, "Alice") // 在 Bob 不在的伺服器：看不到 Bob 的作物
	other.goPage("steal")
	if got := other.options("選擇要偷的作物"); len(got) != 0 {
		t.Fatal(got)
	}
	e.checkLedger()
}

func TestStealByChoosingPlayerAndPlot(t *testing.T) {
	e := setup(t)
	a := e.makePlayer(alice, guildA, "Alice")
	b := e.makePlayer(bob, guildA, "Bob")
	e.makePlayer(carol, guildB, "Carol") // 不同伺服器：不會出現在選單
	e.waterWheat(b)
	e.clock.Advance(61)
	a.goPage("steal")
	if got := a.options("選擇玩家"); !slices.Equal(got, []string{fmt64(b.panel.playerID)}) {
		t.Fatal(got)
	}
	opened := a.choose("選擇玩家", fmt64(b.panel.playerID))
	contains(t, a.Text, "Bob 的農田", "有 **4** 格可以偷", "可偷 ×1")
	if names := attachmentNames(lastEdit(t, opened)); !slices.Equal(names, []string{FarmImage}) { // 顯示對方的農田圖
		t.Fatal(names)
	}
	if got := a.options("選擇要偷哪一格"); !slices.Equal(got, []string{"1|1", "2|1", "3|1", "4|1"}) { // 小麥每次 1 個
		t.Fatal(got)
	}
	a.choose("選擇要偷哪一格", "3|1")
	contains(t, a.Text, "偷走", "Bob 的農田", "今天已經偷過 Bob 的田了") // 偷完留在對方的農田畫面
	if got := a.options("選擇要偷哪一格"); len(got) != 0 {
		t.Fatal(got)
	}
	a.click("換一位玩家")
	first := a.panel.View.Selects()[0].Options
	if !strings.HasPrefix(first[0].Description, "今天已經偷過了") {
		t.Fatal(first[0].Description)
	}
	e.checkLedger()
}

func TestLeaderboardSwitches(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "Alice")
	p.goPage("board")
	contains(t, p.Text, "Alice", "🥇")
	p.choose("範圍", "groups")
	contains(t, p.Text, "群組總排行")
	p.choose("期間", "week")
	contains(t, p.Text, "本週")
	p.choose("範圍", "group")
	contains(t, p.Text, "本伺服器排行")
	if p.selectMenu("期間") != nil {
		t.Fatal("本伺服器排行不應有期間選單")
	}
	p.click("排名獎勵")
	contains(t, p.Text, "===== 排名獎勵 =====", "----- 我最近獲得的排名獎勵 -----", "（尚未獲得）")
}

func TestQuestsAndCompetitionPages(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("quests")
	contains(t, p.Text, "📋 目前沒有任務。", "⚔️ 目前沒有進行中的競賽。")
	p.click("已結算的競賽")
	contains(t, p.Text, "目前沒有已結算的競賽。")
	p.goPage("competition") // 沒有選擇競賽
	contains(t, p.Text, "無法顯示", "請先在任務競賽頁選擇一個競賽。")
}

func TestGroupTransferFromPanel(t *testing.T) {
	e := setup(t)
	e.makePlayer(alice, guildA, "玩家")
	p := e.open(alice, guildB, "玩家")
	p.goPage("group")
	contains(t, p.Text, "可以轉移到這裡")
	p.click("把農場轉移到這個伺服器")
	contains(t, p.Text, "再按一次「確認轉移」")
	p.click("確認轉移")
	contains(t, p.Text, "轉移到", "你的農場屬於這個伺服器")
	notContains(t, p.Text, "無法完成")
	back := e.open(alice, guildA, "玩家")
	back.goPage("group")
	contains(t, back.Text, "轉移冷卻中")
	if !back.button("把農場轉移到這個伺服器").Disabled {
		t.Fatal("冷卻中應停用")
	}
}

func TestErrorScreenHidesPageAndReturnsWithSelection(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.setGold(p.panel.playerID, 50_000)
	p.goPage("craft")
	contains(t, p.Text, "還沒有任何加工設施")
	if len(p.options("開始加工")) != 0 {
		t.Fatal(p.options("開始加工"))
	}
	p.goPage("build")
	p.choose("選擇要建造的設施", "mill")
	p.goPage("craft")
	if len(p.options("開始加工")) != 0 { // 沒有小麥：做不出來的配方不列出
		t.Fatal(p.options("開始加工"))
	}
	contains(t, p.Text, "1 個原料不足")
	pid := p.panel.playerID
	e.setInventory(pid, "wheat", 3)
	p.goPage("craft")
	p.choose("開始加工", "flour")
	contains(t, p.Text, "現有原料最多可做 **1** 份")
	e.setInventory(pid, "wheat", 0) // 選好之後小麥不見了
	p.modal("自訂數量", "1")            // 沒有小麥：失敗
	if !strings.HasPrefix(p.Text, "⚠️ 無法完成") || strings.Contains(strings.SplitN(p.Text, "\n", 2)[0], "加工") {
		t.Fatal(p.Text)
	}
	notContains(t, p.Text, "workshop", "(") // 不顯示 CMD 指令提示
	if got := p.labels(); !slices.Equal(got, []string{"返回"}) || len(p.panel.View.Items()) != 1 {
		t.Fatal(got)
	}
	e.setInventory(pid, "wheat", 3) // 補回原料，回到加工頁時麵粉會出現在選單
	p.click("返回")
	contains(t, p.Text, "🏭 ✦ 加工 ✦", "已選擇") // 回到加工頁，保留剛才選的麵粉
	if got := p.defaults(optLabel); !slices.Equal(got, []string{"麵粉"}) {
		t.Fatal(got)
	}
}

// fixedExplore 每份獎勵都是木材 ×2（Python admin._explore_rng = lambda: 0.0）。
func fixedExplore(e *botEnv) {
	e.withOptions(services.Options{ExploreRng: func() float64 { return 0 }})
}

func (e *botEnv) setLevel(pid int64, level int) {
	e.t.Helper()
	must[services.FarmValues](e.t)(e.admin.SetFarmValues(e.ctx, "root", pid, nil, &level))
}

func TestCaveEntryAreasAndExplore(t *testing.T) {
	e := setup(t)
	fixedExplore(e)
	p := e.makePlayer(alice, guildA, "玩家")
	if !slices.Contains(p.labels(), "山洞") {
		t.Fatal(p.labels())
	}
	for _, area := range []string{"青青草原", "古老森林", "神秘礦坑"} { // 主選單不直接顯示探索區域
		if slices.Contains(p.labels(), area) {
			t.Fatal(p.labels())
		}
	}
	p.click("山洞")
	contains(t, p.Text, "山洞冒險", "Lv 1", "一次只能去一個區域", "🔒 農場 Lv.5 解鎖", "🔒 農場 Lv.10 解鎖", "探險 8 小時")
	for _, l := range []string{"青青草原", "古老森林", "神秘礦坑", "探索紀錄", "返回主選單"} {
		if !slices.Contains(p.labels(), l) {
			t.Fatal(l, p.labels())
		}
	}
	p.click("古老森林")
	contains(t, p.Text, "古老森林")
	if !p.button("尚未解鎖").Disabled {
		t.Fatal("未解鎖應停用")
	}
	p.click("返回冒險介面")
	p.click("青青草原")
	contains(t, p.Text, "✅ 可以出發", "琥珀瓜種子")
	started := p.click("出發探險")
	contains(t, p.Text, "出發前往青青草原", "🧭 探險中")
	if slices.Contains(p.labels(), "領取探險獎勵") {
		t.Fatal(p.labels())
	}
	if names := attachmentNames(lastEdit(t, started)); !slices.Equal(names, []string{FarmImage}) { // 探險中顯示所在區域的圖
		t.Fatal(names)
	}
	p.click("青青草原")
	if !p.button("探險中").Disabled {
		t.Fatal("探險中應停用")
	}
	// 直接再呼叫一次出發（模擬重送的互動）會被擋下，也不會先發獎
	it := p.interaction()
	p.panel.act(e.ctx, it, func(ctx context.Context) (string, error) { return p.panel.explore(ctx, "grassland") }, actOpts{})
	p.goPage("backpack")
	notContains(t, p.Text, "木材")
	e.clock.Advance(8 * 3600)
	p.goPage("cave")
	contains(t, p.Text, "探險回來了")
	claimed := p.click("領取探險獎勵")
	if names := attachmentNames(lastEdit(t, claimed)); !slices.Equal(names, []string{FarmImage}) {
		t.Fatal(names)
	}
	contains(t, p.Text, "從青青草原探險回來了", "探索結果", "木材", "×**6**")
	p.click("返回冒險介面")
	contains(t, p.Text, "✅ 可以出發")
	if slices.Contains(p.labels(), "領取探險獎勵") {
		t.Fatal(p.labels())
	}
	p.goPage("backpack")
	contains(t, p.Text, "木材　×**6**")
	p.goPage("cave_log")
	contains(t, p.Text, "青青草原", "木材 ×6")
	p.click("返回冒險介面")
	p.click("返回主選單")
	contains(t, p.Text, "農田")
	if !slices.Contains(p.labels(), "山洞") {
		t.Fatal(p.labels())
	}
}

func TestCaveOtherAreaBlockedWhileExploring(t *testing.T) {
	e := setup(t)
	fixedExplore(e)
	p := e.makePlayer(alice, guildA, "玩家")
	e.setLevel(p.panel.playerID, 5)
	p.goPage("cave")
	p.click("青青草原")
	p.click("出發探險")
	p.click("古老森林")
	contains(t, p.Text, "正在青青草原探險")
	if !p.button("正在其他區域探險").Disabled {
		t.Fatal("應停用")
	}
	e.clock.Advance(8 * 3600)
	p.click("返回冒險介面")
	p.click("青青草原")
	p.click("領取探險獎勵") // 區域頁也可以直接領取
	contains(t, p.Text, "探索結果")
	p.click("返回冒險介面")
	p.click("古老森林")
	p.click("出發探險")
	contains(t, p.Text, "出發前往古老森林", "探險 16 小時")
}

func TestCaveDoubleClickStartsOnce(t *testing.T) {
	e := setup(t)
	fixedExplore(e)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("cave")
	p.click("青青草原")
	b := p.button("出發探險")
	// 同一個按鈕連點兩次：第一次完成後畫面已重繪，舊的 custom_id 失效，第二次不會執行
	first, second := p.interaction(), p.interaction()
	if !e.cmds.Registry.DispatchComponent(e.ctx, first, b.CustomID(), nil) {
		t.Fatal("第一次點擊沒有執行")
	}
	e.cmds.Registry.DispatchComponent(e.ctx, second, b.CustomID(), nil)
	if n := e.scalar("SELECT count(*) FROM exploration_logs"); n != int64(1) {
		t.Fatal(n)
	}
}

func TestCavePlayersAreIndependent(t *testing.T) {
	e := setup(t)
	fixedExplore(e)
	a := e.makePlayer(alice, guildA, "玩家")
	b := e.makePlayer(bob, guildA, "小明")
	a.goPage("cave")
	a.click("青青草原")
	a.click("出發探險")
	b.goPage("cave")
	contains(t, b.Text, "✅ 可以出發") // Alice 的探險不影響 Bob
}

func TestMachinesPageBuildAndUseSeedMaker(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setLevel(pid, 3)
	for item, n := range map[string]int{"wood": 10, "stone": 10, "copper_ore": 5, "gear": 2, "wheat": 2} {
		e.setInventory(pid, item, n)
	}
	p.goPage("craft")
	p.click("機器")
	contains(t, p.Text, "種子製造機", "❌齒輪 2/3")
	p.choose("製作機器", "seed_maker")
	contains(t, p.Text, "材料不足，還缺：齒輪 ×1")
	e.setInventory(pid, "gear", 3)
	p.click("返回")
	p.choose("製作機器", "seed_maker")
	contains(t, p.Text, "種子製造機製作完成", "從「🌱 農田」→「🌱 種子製造」使用")
	if slices.Contains(p.labels(), "停用種子製造機") { // 種子製造機沒有啟用／停用開關
		t.Fatal(p.labels())
	}
	p.click("種子製造")
	contains(t, p.Text, "種子製造機", "小麥")
	p.choose("選擇要做成種子的作物", "seed:wheat:normal:none")
	p.click("用 1")
	contains(t, p.Text, "做出 2 顆小麥種子")
	e.checkLedger()
}

func TestSeedMakerPageFromPlotsAcceptsAnyQuality(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	p.goPage("plots")
	if slices.Contains(p.labels(), "種子製造") { // 沒有機器時不顯示入口
		t.Fatal(p.labels())
	}
	e.setLevel(pid, 3)
	for item, n := range map[string]int{"wood": 10, "stone": 10, "copper_ore": 5, "gear": 3} {
		e.setInventory(pid, item, n)
	}
	p.goPage("machines")
	p.choose("製作機器", "seed_maker")
	if _, err := e.admin.SetInventory(e.ctx, "root", pid, "carrot", "good", "none", 12); err != nil {
		t.Fatal(err)
	}
	p.goPage("plots")
	p.click("種子製造")
	contains(t, p.Text, "胡蘿蔔【優良】", "×**12**")
	p.choose("選擇要做成種子的作物", "seed:carrot:good:none")
	if !slices.Contains(p.labels(), "用 12") { // 超過 10 個時多一個「全部」
		t.Fatal(p.labels())
	}
	p.click("用 5")
	contains(t, p.Text, "做出 10 顆胡蘿蔔種子")
	e.checkLedger()
}

func TestLogPageListsRecentTransactions(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("shop")
	p.choose("選擇要買的商品", "seed:wheat")
	p.click("買 1")
	p.goPage("log")
	contains(t, p.Text, "🧾 ✦ 最近的操作紀錄 ✦", "`-")

	// 超過一頁（10 筆）時分頁，可以往回翻（2026-11-14：原本只顯示 15 筆，一下就被洗掉）
	for i := 0; i < 11; i++ {
		p.goPage("shop")
		p.choose("選擇要買的商品", "seed:wheat")
		p.click("買 1")
	}
	p.goPage("main")
	p.click("紀錄")
	p.button("上一頁（1/2）") // 翻頁按鈕標示目前頁數
	p.click("下一頁")
	p.button("上一頁（2/2）")
	p.goPage("main")
	p.click("紀錄") // 從總覽進來時回到第 1 頁
	p.button("上一頁（1/2）")
}

func fmt64(n int64) string { return fmt.Sprint(n) }
