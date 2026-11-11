package bot

// 子面板第一部分（第 5 階段第 7 批）：tests/test_special_shop.py 的 test_panel_special_shop、
// tests/test_achievements.py 的 test_panel_claim_and_choose_title、tests/test_checkin.py 的 test_panel_auto_checkin_once、
// tests/test_feedback.py 的 test_panel_shows_codex_hint_in_gold、tests/test_discord_panel.py 的遺跡兩個測試。

import (
	"strings"
	"testing"

	"farmgm/internal/bot/ui"

	"farmgm/internal/game/ruins"
	"farmgm/internal/game/specialshop"
)

func TestPanelSpecialShop(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.setGold(pid, 2_000_000)
	p.goPage("shop")
	p.click("特賣與升級")
	contains(t, p.Text, "特賣與升級", "溫室", "本週特賣", "稱號商店")
	p.choose("選擇要買的商品", "greenhouse")
	p.click("確認購買")
	contains(t, p.Text, "溫室升到 1 級")
	title := specialshop.TitleShop()[0].Title
	p.choose("選擇要買的商品", "title:"+title)
	p.click("確認購買")
	contains(t, p.Text, "買到稱號「"+title+"」")
	var crop string
	for _, v := range p.options("選擇要買的商品") {
		if len(v) > 7 && v[:7] == "weekly:" {
			crop = v
			break
		}
	}
	if crop == "" {
		t.Fatal(p.options("選擇要買的商品"))
	}
	p.choose("選擇要買的商品", crop)
	p.click("買 3")
	contains(t, p.Text, "買到 3 顆")
	if n := e.scalar("SELECT greenhouse_level FROM farms WHERE player_id = $1", pid); n != int32(1) && n != int64(1) {
		t.Fatalf("%T %v", n, n)
	}
	e.checkLedger()
}

func TestPanelClaimAndChooseTitle(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	e.exec("UPDATE farms SET gold = 100000, warehouse_capacity = 10000 WHERE player_id = $1", pid)
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	p.click("全部澆水")
	e.exec("UPDATE farm_plots SET quality = 'legendary' WHERE crop_type_id IS NOT NULL")
	e.clock.Advance(600)
	p.goPage("main")
	p.click("全部收成")
	p.click("成就圖鑑")
	contains(t, p.Text, "成就與圖鑑", "可領取")
	p.click("成就")
	p.choose("選擇成就分類", "farm")
	contains(t, p.Text, "傳說農夫", "可以領取")
	p.click("領取全部")
	contains(t, p.Text, "領取", "傳說農夫")
	p.click("稱號")
	p.choose("選擇要顯示的稱號", "傳說農夫")
	contains(t, p.Text, "已設定稱號")
	p.goPage("main")
	contains(t, p.Text, "〔傳說農夫〕")
	p.goPage("board")
	contains(t, p.Text, "〔傳說農夫〕")
	// 改回不顯示稱號
	p.goPage("titles")
	p.choose("選擇要顯示的稱號", noTitle)
	contains(t, p.Text, "目前顯示：未設定")
}

func TestPanelAutoCheckinOnce(t *testing.T) {
	e := setup(t)
	e.cmds.AutoCheckin = true
	p := e.makePlayer(alice, guildA, "玩家")
	contains(t, p.Text, "每日簽到成功", "連續第 **1** 天")
	_, after, _ := strings.Cut(p.Text, "每日簽到成功")
	notContains(t, after, "每日簽到") // 總覽不再有簽到欄位（移到成就圖鑑）
	p.goPage("plots")
	p.goPage("main")
	notContains(t, p.Text, "每日簽到成功")
	p.click("成就圖鑑")
	contains(t, p.Text, "今天已簽到", "連續 **1** 天")
	if n := e.scalar("SELECT count(*) FROM daily_checkins"); n != int64(1) {
		t.Fatal(n)
	}
}

func TestPanelShowsCodexHintInGold(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("plots")
	p.choose("選擇作物", "wheat")
	p.click("全部澆水")
	e.clock.Advance(61)
	p.goPage("main")
	it := p.click("全部收成")
	contains(t, p.Text, "> ✅ 🧺 收成 4 格", "📖 新圖鑑：小麥")
	if c := lastEdit(t, it).Message.Embed.Color; c != Gold {
		t.Fatalf("%#x", c)
	}
}

func TestPanelCodexAndAchievementCategories(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	p.goPage("codex")
	contains(t, p.Text, "📖 ✦ 圖鑑｜")
	keys := p.options("選擇圖鑑分類")
	if len(keys) < 2 {
		t.Fatal(keys)
	}
	p.choose("選擇圖鑑分類", keys[1])
	if got := p.defaults(func(o ui.SelectOption) string { return o.Value }); len(got) != 1 || got[0] != keys[1] {
		t.Fatal(got)
	}
	p.goPage("achievements")
	p.choose("選擇成就分類", "boss")
	contains(t, p.Text, "🏅 ✦ 成就｜🐉 世界 Boss ✦")
	if !p.button("領取全部（0）").Disabled {
		t.Fatal("沒有可領取時應停用")
	}
}

// fastRuins 把遺跡三個階段的需求改小（Python monkeypatch ruins.STAGES）。
func fastRuins(t *testing.T, first map[string]int) {
	var firstCode string
	for k := range first {
		firstCode = k
	}
	old := ruins.Stages
	ruins.Stages = []ruins.Stage{
		{Name: "清除碎石", Description: "搬開碎石。", Materials: first, MaterialOrder: []string{firstCode}},
		{Name: "修補石柱", Materials: map[string]int{"wood": 1}, MaterialOrder: []string{"wood"}},
		{Name: "喚醒遺跡核心", Materials: map[string]int{"gear": 1}, MaterialOrder: []string{"gear"}},
	}
	t.Cleanup(func() { ruins.Stages = old })
}

func TestRuinsPageDonateWithButtons(t *testing.T) {
	e := setup(t)
	fastRuins(t, map[string]int{"stone": 20})
	p := e.makePlayer(alice, guildA, "玩家")
	e.setInventory(p.panel.playerID, "stone", 30)
	p.click("山洞")
	contains(t, p.Text, "古代遺跡", "所屬群組修復遺跡後開放")
	p.click("修復遺跡")
	contains(t, p.Text, "古代遺跡", "清除碎石", "0/20", "（背包 30）")
	p.choose("選擇要捐的素材", "stone")
	contains(t, p.Text, "捐 石塊（最多 20）")
	p.click("捐 10")
	contains(t, p.Text, "捐出 石塊 ×10", "10/20", "累計捐了 **10**")
	p.choose("選擇要捐的素材", "stone")
	p.click("捐 10")
	contains(t, p.Text, "下一階段", "修補石柱")
}

func TestRuinsGuardianAttackFromPanel(t *testing.T) {
	e := setup(t)
	fastRuins(t, map[string]int{"stone": 1})
	p := e.makePlayer(alice, guildA, "玩家")
	pid := p.panel.playerID
	for item, n := range map[string]int{"stone": 1, "wood": 1, "gear": 1, "ancient_core": 30} {
		e.setInventory(pid, item, n)
	}
	for _, item := range []string{"stone", "wood", "gear"} {
		if _, err := e.admin.DonateRuins(e.ctx, pid, item, 1); err != nil {
			t.Fatal(err)
		}
	}
	p.goPage("ruins")
	contains(t, p.Text, "修復完成", "石像守護者", "會在")
	e.clock.Advance(4 * 24 * 3600) // 台灣週一：守護者出現
	p.goPage("ruins")
	contains(t, p.Text, "本週石像守護者", "4,000/4,000")
	p.choose("選擇攻擊守護者的素材", "ancient_core")
	contains(t, p.Text, "每個 80 傷害，最多 30")
	p.click("用 10")
	contains(t, p.Text, "造成 **800** 點傷害", "3,200/4,000")
	e.setLevel(pid, 4)
	p.goPage("cave") // 遺跡修好後可以額外探索，立即顯示結果
	contains(t, p.Text, "可以探索（不佔一般探險")
	p.click("古代遺跡")
	p.click("探索遺跡")
	contains(t, p.Text, "探索結果", "遺跡裡多找到", "可再探索遺跡")
	e.checkLedger()
}
