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
	contains(t, p.Text, "✦ 庭院擺設 ✦", "1 號：（空）", "已擁有 0／24", "🟡 **稀有**（100,000 金）")
	p.choose("購買裝飾", "fountain")
	contains(t, p.Text, "確認購買", "噴水池")
	p.click("確認購買（100,000 金）")
	contains(t, p.Text, "買下⛲ 噴水池", "1 號位置")
	p.goPage("decor")
	contains(t, p.Text, "1 號：⛲ 噴水池", "已擁有 1／24")
	// 搬到 3 號位置
	p.choose("選擇庭院位置", "3")
	p.choose("擺到 3 號位置", "fountain")
	contains(t, p.Text, "擺到庭院 3 號位置")
	p.goPage("decor")
	contains(t, p.Text, "1 號：（空）", "3 號：⛲ 噴水池")
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
	p.click("開始育苗（300 金）")
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
