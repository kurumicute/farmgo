package bot

// 庭院擺設、加速沙漏、種子庫與研究槽擴充的面板（Go 版新增，2026-11-15）。

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDecorPanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setGold(pid, 300_000)
	p.goPage("plots")
	p.click("庭院擺設")
	contains(t, p.Text, "✦ 庭院擺設 ✦", "左上：（空）", "已擁有 0／26", "🟡 **稀有**（100,000 金）", "本季限定") // 24 件＋本季限定 2 件
	p.choose("購買裝飾", "fountain")
	contains(t, p.Text, "確認購買", "噴水池")
	p.click("確認購買（100,000 金）")
	contains(t, p.Text, "買下⛲ 噴水池", "擺在庭院的左上")
	p.goPage("decor")
	contains(t, p.Text, "左上：⛲ 噴水池", "已擁有 1／26")
	// 搬到 3 號位置
	p.choose("選擇庭院位置", "3")
	p.choose("擺到左下", "fountain")
	contains(t, p.Text, "擺到庭院的左下")
	p.goPage("decor")
	contains(t, p.Text, "左上：（空）", "左下：⛲ 噴水池")
	// 農田圖片下方多了庭院擺飾區
	p.goPage("plots")
	if len(p.panel.files) == 0 {
		t.Fatal("農田頁要有圖片")
	}
	e.checkLedger()
}

func TestHourglassAndLabExpansionPanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 5)
	e.setGold(pid, 1_000_000)
	p.goPage("lab")
	p.click("建造實驗室")
	p.click("確認建造")
	e.admin.LabRng = func() float64 { return 0.9 }
	e.setInventory(pid, "seed_wheat", 1)
	e.setInventory(pid, "hourglass", 2)
	p.goPage("lab")
	p.choose("鑑定一般種子", "wheat")
	// 育苗後用沙漏
	p.goPage("lab_seeds")
	p.choose("選擇種子", p.options("選擇種子")[0])
	p.click("育苗")
	p.click("開始育苗（15 金）")
	p.goPage("lab")
	if got := p.options("使用加速沙漏"); len(got) != 1 || got[0] != "lab:1" {
		t.Fatal(got)
	}
	p.choose("使用加速沙漏", "lab:1")
	contains(t, p.Text, "使用加速沙漏，研究槽 #1", "→ 30 分 0 秒")
	e.clock.Advance((30 * time.Minute).Seconds())
	p.goPage("lab")
	p.click("領取（1）")
	contains(t, p.Text, "育苗完成")

	// 種子庫擴充（按兩次確認）
	p.goPage("lab_seeds")
	p.click("種子庫 +10 格（10,000 金）")
	contains(t, p.Text, "再按一次「確認擴充種子庫」")
	p.click("確認擴充種子庫")
	contains(t, p.Text, "種子庫擴充：25 → **35** 格")
	// 研究槽擴充：研究 Lv.3 才出現
	p.goPage("lab")
	if slices.ContainsFunc(p.labels(), func(l string) bool { return strings.Contains(l, "研究槽擴充") }) {
		t.Fatal("研究 Lv.1 不能買研究槽")
	}
	e.exec("UPDATE seed_labs SET level = 3")
	p.goPage("lab")
	p.click("研究槽擴充（100,000 金）")
	p.click("確認擴充研究槽")
	contains(t, p.Text, "研究槽擴充：3 → **4** 個")
	e.checkLedger()
}

// 名牌與農會（2026-11-19）：從成就與圖鑑進入；買名牌後排行榜名字前面顯示；捐款要確認。
func TestBadgeAndReputationPanels(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setGold(pid, 500_000)
	p.goPage("progress")
	p.click("名牌")
	contains(t, p.Text, "✦ 名牌 ✦", "🍀 **幸運草**", "農會聲望 Lv.10")
	p.choose("購買名牌", "clover")
	p.click("確認購買（30,000 金）")
	contains(t, p.Text, "買下名牌 🍀 幸運草")
	p.click("農會")
	contains(t, p.Text, "✦ 農會 ✦", "Lv.0")
	p.click("捐 10,000")
	contains(t, p.Text, "確認捐款")
	p.click("確認捐 10,000 金")
	contains(t, p.Text, "聲望升到 **Lv.1**", "🍀 阿明")
	p.goPage("board") // 排行榜名字前面顯示名牌
	contains(t, p.Text, "🍀 阿明")
	e.checkLedger()
}

// 農場外觀頁（2026-11-19）：從庭院擺設進入，買主題後農田圖照常產生。
func TestLooksPanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setGold(pid, 500_000)
	p.goPage("decor")
	p.click("農場外觀")
	contains(t, p.Text, "✦ 農場外觀 ✦", "❄️ **雪地**", "還沒有寵物")
	p.choose("選擇主題", "night")
	contains(t, p.Text, "確認購買")
	p.click("確認購買（150,000 金）")
	contains(t, p.Text, "買下主題 🌙 夜景")
	p.goPage("plots")
	if len(p.panel.files) == 0 {
		t.Fatal("套用主題後農田頁要有圖片")
	}
	e.checkLedger()
}

// 每週拍賣頁（2026-11-19）：出最低價要確認，成功後顯示自己是最高出價者。
func TestAuctionPanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	e.setGold(p.panel.playerID, 500_000)
	p.goPage("progress")
	p.click("每週拍賣")
	contains(t, p.Text, "✦ 每週拍賣 ✦", "本週拍賣品", "還沒有人出價")
	p.click("出最低價（50,000）")
	contains(t, p.Text, "確認出價")
	p.click("確認出價 50,000 金")
	contains(t, p.Text, "目前最高價", "你是目前的最高出價者")
	e.checkLedger()
}

// 中英文切換（2026-11-19）：從「更多功能」切換成英文，總覽、農田頁與種植／澆水的回應都變成英文；
// 選擇記在玩家設定，重新開面板仍是英文；再切一次回到繁體中文。
func TestLanguageToggle(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	p.choose("更多功能", langToggle)
	contains(t, p.Text, "Plots (4)", "Home Server", "To-do")
	contains(t, strings.Join(p.labels(), " "), "Water All", "Harvest All", "Inventory")
	p.goPage("plots")
	contains(t, p.Text, "Plots (4)", "Empty")
	p.choose("Choose a crop", "wheat")
	contains(t, p.Text, "Planted Wheat in 4 plots")
	if strings.Contains(p.Text, "種下") {
		t.Fatal("英文模式不應出現中文回應：" + p.Text)
	}
	// 重新開面板：沿用玩家設定
	again := e.open(alice, guildA, "阿明")
	contains(t, again.Text, "Plots (4)")
	again.choose("More", langToggle)
	contains(t, again.Text, "農田（4 格）", "介面語言已切換為繁體中文")
}
