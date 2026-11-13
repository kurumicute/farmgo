package bot

// 種子實驗室（Go 版新增，2026-11-13）的面板測試：從農田頁進入、建造、鑑定、雜交、領取、種植與分享。

import (
	"fmt"
	"testing"
	"time"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/seedlab"
)

func TestLabPageFlow(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID

	p.goPage("plots")
	p.click("種子實驗室")
	contains(t, p.Text, "🔒 種子實驗室在農場 **Lv.5** 開放")

	e.setLevel(pid, 5)
	e.setGold(pid, 100_000)
	e.setGold(pid, 4_000)
	p.goPage("lab")
	contains(t, p.Text, "建造需要", "❌ 💰 金幣 4,000／5,000")
	if !p.button("建造實驗室").Disabled {
		t.Fatal("金幣不足時建造按鈕應停用")
	}
	e.setGold(pid, 100_000)
	p.goPage("lab")
	p.click("建造實驗室")
	contains(t, p.Text, "再按一次「確認建造」")
	p.click("確認建造")
	contains(t, p.Text, "種子實驗室建造完成", "研究等級 **Lv.1**", "#1　空的")
	if len(p.panel.files) != 0 {
		t.Fatal("實驗室頁面不產生圖片")
	}

	// 鑑定兩顆小麥、一顆玉米
	e.admin.LabRng = func() float64 { return 0.9 }
	e.setInventory(pid, "seed_wheat", 2)
	e.setInventory(pid, "seed_corn", 1)
	p.goPage("lab")
	p.choose("鑑定一般種子", "wheat")
	contains(t, p.Text, "鑑定完成：小麥種子")
	p.choose("鑑定一般種子", "wheat")
	p.choose("鑑定一般種子", "corn")
	contains(t, p.Text, "種子庫 3／25 顆")
	p.button("種子庫（3）")

	// 雜交：選小麥當母本，父本只列出相容的（小麥、玉米）
	p.click("雜交培育")
	contains(t, p.Text, "雜交表", "小麥 × 玉米 → **黃金穀物**")
	mothers := p.options("選擇母本")
	if len(mothers) != 3 {
		t.Fatal(mothers)
	}
	p.choose("選擇母本", mothers[0])
	fathers := p.options("選擇父本")
	if len(fathers) != 2 {
		t.Fatal(fathers)
	}
	p.choose("選擇父本", mothers[2]) // 玉米
	contains(t, p.Text, "➡️ 雜交：**黃金穀物**　成功率 70%")
	p.click("開始培育")
	contains(t, p.Text, "開始雜交：小麥 × 玉米 → 黃金穀物", "研究槽")
	if !p.button("領取（0）").Disabled {
		t.Fatal("還沒完成時領取按鈕應停用")
	}

	e.admin.LabRng = func() float64 { return 0 }
	e.clock.Advance((3 * time.Hour).Seconds())
	p.goPage("lab")
	p.click("領取（1）")
	contains(t, p.Text, "雜交成功", "黃金穀物")

	// 種子庫：選黃金穀物種到第 1 格
	p.click("種子庫")
	var gg string
	for _, id := range p.options("選擇種子") {
		var crop string
		if v := e.scalar("SELECT crop_code FROM lab_seeds WHERE id = $1", mustInt(t, id)); v != nil {
			crop = v.(string)
		}
		if crop == "golden_grain" {
			gg = id
		}
	}
	if gg == "" {
		t.Fatal(p.options("選擇種子"))
	}
	p.choose("選擇種子", gg)
	contains(t, p.Text, "選取的種子", "雜交作物")
	p.choose("種下這顆種子", "1")
	contains(t, p.Text, "種下特殊黃金穀物種子")
	p.goPage("plots")
	contains(t, p.Text, "黃金穀物")

	// 分享：同伺服器、已建造實驗室的玩家才會出現
	q := e.makePlayer(bob, guildA, "阿華")
	qid := q.panel.playerID
	p.goPage("lab_seeds")
	seeds := p.options("選擇種子")
	p.choose("選擇種子", seeds[0])
	if got := p.options("分享給同伺服器的玩家"); len(got) != 1 || got[0] != fmt.Sprint(qid) {
		t.Fatal(got)
	}
	p.choose("分享給同伺服器的玩家", fmt.Sprint(qid))
	contains(t, p.Text, "分享給", "今天已分享 1／5 顆")
	// 阿華沒有實驗室（Lv.1）：實驗室頁顯示朋友分享的種子，可以進種子庫種下
	q.goPage("lab")
	contains(t, q.Text, "🔒 種子實驗室在農場 **Lv.5** 開放", "種子庫裡有 **1** 顆朋友分享的特殊種子")
	q.click("種子庫（1）")
	q.choose("選擇種子", q.options("選擇種子")[0])
	q.choose("種下這顆種子", "1")
	contains(t, q.Text, "種下特殊")

	// 實驗室頁的圖鑑按鈕
	p.goPage("lab")
	p.click("特殊種子圖鑑")
	contains(t, p.Text, "圖鑑｜🧪 特殊種子", "黃金穀物種子")
	p.click("回實驗室")
	p.click("菇類圖鑑")
	contains(t, p.Text, "圖鑑｜🍄 菇類")
	e.checkLedger()
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 種子庫上限超過 25 顆時（管理網站可調到 200），種子庫與雜交頁的選單分頁顯示（2026-11-13 使用者要求）。
func TestLabSeedPages(t *testing.T) {
	e := setup(t)
	cfg, _ := featurecfg.DeepCopy(seedlab.DefaultValue())
	cfg["library_capacity"] = 30
	if _, err := e.admin.UpdateFeatureSettings(e.ctx, "root", seedlab.ConfigKey, cfg); err != nil { // 和管理網站一樣存進資料庫
		t.Fatal(err)
	}
	t.Cleanup(featurecfg.Reset)

	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 5)
	e.setGold(pid, 1_000_000)
	p.goPage("lab")
	p.click("建造實驗室")
	p.click("確認建造")
	e.setInventory(pid, "seed_wheat", 30)
	for i := 0; i < 30; i++ {
		p.goPage("lab")
		p.choose("鑑定一般種子", "wheat")
	}
	p.goPage("lab_seeds")
	contains(t, p.Text, "特殊種子 30／30（第 1/2 頁）")
	if got := p.options("選擇種子"); len(got) != 25 {
		t.Fatal(len(got))
	}
	p.click("上一頁（1/2）")
	contains(t, p.Text, "（第 2/2 頁）")
	if got := p.options("選擇種子"); len(got) != 5 {
		t.Fatal(len(got))
	}
	p.goPage("lab_cross")
	if got := p.options("選擇母本"); len(got) != 25 {
		t.Fatal(len(got))
	}
	p.click("母本下一頁")
	mothers := p.options("選擇母本")
	if len(mothers) != 5 {
		t.Fatal(len(mothers))
	}
	p.choose("選擇母本", mothers[0])
	if got := p.options("選擇父本"); len(got) != 25 { // 其他 29 顆都能配對，第一頁 25 顆
		t.Fatal(len(got))
	}
	p.click("父本下一頁")
	if got := p.options("選擇父本"); len(got) != 4 {
		t.Fatal(len(got))
	}
}
