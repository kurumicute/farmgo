package bot

// 種子實驗室（Go 版新增，2026-11-13）的面板測試：從農田頁進入、建造、鑑定、雜交、領取、種植與分享。

import (
	"fmt"
	"strings"
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
	p.chooseModal("鑑定一般種子", "wheat", "1") // 有 2 顆可以鑑定：跳出數量輸入框（2026-11-19 批量鑑定）
	contains(t, p.Text, "鑑定完成：小麥種子")
	p.choose("鑑定一般種子", "wheat") // 只剩 1 顆：直接鑑定
	p.choose("鑑定一般種子", "corn")
	contains(t, p.Text, "種子庫 3／25 顆")
	p.button("種子庫（3）")

	// 雜交：配方隱藏（2026-11-14），還沒發現的配方只顯示數量；父本列出其他所有種子（小麥、玉米）
	p.click("雜交培育")
	contains(t, p.Text, "雜交配方（已發現 0/12", "還有 **12** 種配方沒有發現")
	if strings.Contains(p.Text, "黃金穀物") {
		t.Fatal("沒發現的配方不應該顯示：" + p.Text)
	}
	// 兩種以上作物時，選單第一項是「🔎 篩選作物」（2026-11-17）
	mothers := p.options("選擇母本")
	if len(mothers) != 4 || mothers[0] != cropFilterPrefix {
		t.Fatal(mothers)
	}
	mothers = mothers[1:]
	// 篩選：母本選單換成作物清單，選玉米後只列玉米
	p.choose("選擇母本", cropFilterPrefix)
	p.choose("先選擇要看的作物", cropFilterPrefix+"corn")
	if got := p.options("選擇母本（只看玉米）"); len(got) != 2 || got[0] != cropFilterPrefix || got[1] != mothers[2] {
		t.Fatal(got)
	}
	p.choose("選擇母本", cropFilterPrefix)
	p.choose("先選擇要看的作物", cropFilterPrefix+cropFilterAll)
	p.choose("選擇母本", mothers[0])
	fathers := p.options("選擇父本")
	if len(fathers) != 3 || fathers[0] != cropFilterPrefix {
		t.Fatal(fathers)
	}
	p.choose("選擇父本", mothers[2]) // 玉米
	contains(t, p.Text, "➡️ 試驗雜交：**❔ 結果未知**")
	p.click("開始培育")
	contains(t, p.Text, "開始試驗雜交：小麥 × 玉米 → ❔ 結果未知", "研究槽")
	if strings.Contains(p.Text, "黃金穀物") {
		t.Fatal("培育中也不應該洩漏結果：" + p.Text)
	}
	if !p.button("領取（0）").Disabled {
		t.Fatal("還沒完成時領取按鈕應停用")
	}

	e.admin.LabRng = func() float64 { return 0 }
	e.clock.Advance((3 * time.Hour).Seconds())
	p.goPage("lab")
	p.click("領取（1）")
	contains(t, p.Text, "雜交成功", "黃金穀物", "📖 發現新配方：小麥 × 玉米 → 黃金穀物")
	// 發現後配方表就看得到
	p.goPage("lab_cross")
	contains(t, p.Text, "雜交配方（已發現 1/12", "小麥 × 玉米 → **黃金穀物**", "還有 **11** 種配方沒有發現")
	p.goPage("lab")

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

// TestLabClonePanel 育苗頁（2026-11-15）：種子庫按「育苗」進入，選次數、加不加藥草；只要金幣就能育苗，領取後每次得到 1–3 顆。
func TestLabClonePanel(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 5)
	e.setGold(pid, 100_000)
	p.goPage("lab")
	p.click("建造實驗室")
	p.click("確認建造")
	e.admin.LabRng = func() float64 { return 0.9 } // 基因都是 1 級：小麥售價 15 × 收成 2 個 × 50% = 每次育苗 15 金；0.9 → 每次 1 顆
	e.setInventory(pid, "seed_wheat", 1)
	p.goPage("lab")
	p.choose("鑑定一般種子", "wheat")
	p.goPage("lab_seeds")
	p.choose("選擇種子", p.options("選擇種子")[0])
	contains(t, p.Text, "🌱 育苗：15 金", "得到 1–3 顆")
	p.click("育苗")
	contains(t, p.Text, "✦ 育苗 ✦", "1 顆 70%、2 顆 20%、3 顆 10%", "1 顆 50%、2 顆 20%、3 顆 30%", "排程 **1** 次：15 金幣")
	if !p.button("加藥草（需要 1，持有 0）").Disabled {
		t.Fatal("沒有藥草時不能選加藥草")
	}
	p.choose("選擇育苗次數", "3")
	contains(t, p.Text, "排程 **3** 次：45 金幣", "預計得到 **3–9** 顆")
	p.click("開始育苗（45 金）")
	contains(t, p.Text, "開始育苗 ×3 次：小麥種子 第 1 代", "🌱 育苗 ×3 次：小麥 第 1 代")
	if e.gold(pid) != 100_000-5000-45-10 { // 建造 5,000、鑑定 10
		t.Fatal(e.gold(pid))
	}
	e.clock.Advance((3 * time.Hour).Seconds())
	p.goPage("lab")
	p.click("領取（1）")
	contains(t, p.Text, "育苗完成（3 次：1、1、1）", "種子庫 4／")
	e.checkLedger()
}

// TestFertilizerMultiSelect 農田頁的化肥選單可以複選（2026-11-15）：勾選的每一格都施肥，最多選持有的數量。
func TestFertilizerMultiSelect(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.waterWheat(p)
	e.setInventory(pid, "fertilizer", 2)
	p.goPage("plots")
	s := p.selectMenu("勾選要施化肥的農田")
	if s == nil || s.MaxValues != 2 || len(s.Options) < 2 {
		t.Fatalf("%+v", s)
	}
	p.chooseMany("勾選要施化肥的農田", []string{"1", "2"})
	contains(t, p.Text, "已對第 1 格施化肥", "已對第 2 格施化肥")
	if n := e.scalar("SELECT count(*) FROM farm_plots p JOIN farms f ON f.id = p.farm_id WHERE f.player_id = $1 AND p.fertilized", pid); n != int64(2) {
		t.Fatal(n)
	}
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
	p.goPage("lab")
	p.chooseModal("鑑定一般種子", "wheat", "30") // 批量鑑定（2026-11-19）：一次鑑定 30 顆
	contains(t, p.Text, "鑑定完成：30 顆小麥種子")
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
	// 雜交頁：翻頁放在選單裡（每頁 23 顆，加上第一項「上一頁」、最後一項「下一頁」）
	p.goPage("lab_cross")
	if got := p.options("選擇母本"); len(got) != 25 || got[0] != pagePrev || got[24] != pageNext {
		t.Fatal(got)
	}
	p.choose("選擇母本", pageNext)
	mothers := p.options("選擇母本")
	if len(mothers) != 9 { // 30 − 23 = 7 顆＋2 個翻頁選項
		t.Fatal(len(mothers))
	}
	p.choose("選擇母本", mothers[1])
	if got := p.options("選擇父本"); len(got) != 25 { // 其他 29 顆都能配對，第一頁 23 顆＋2 個翻頁選項
		t.Fatal(len(got))
	}
	p.choose("選擇父本", pageNext)
	if got := p.options("選擇父本"); len(got) != 8 {
		t.Fatal(len(got))
	}
	// 選好母本後仍可把母本選單翻回第 1 頁改選
	p.choose("選擇母本", pagePrev)
	mothers = p.options("選擇母本")
	if len(mothers) != 25 {
		t.Fatal(len(mothers))
	}
	p.choose("選擇母本", mothers[1])
	contains(t, p.Text, "♀ 母本：", "母本選單共 2 頁（目前第 1 頁）")
	if got := p.options("選擇母本"); len(got) != 25 {
		t.Fatal(len(got))
	}
}

// 農田沒有空地時，種子庫仍顯示停用的「種下」選單與原因（以前選單會整個消失，玩家以為特殊種子不能種）。
func TestLabSeedNoEmptyPlot(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.setLevel(pid, 5)
	e.setGold(pid, 100_000)
	p.goPage("lab")
	p.click("建造實驗室")
	p.click("確認建造")
	e.setInventory(pid, "seed_wheat", 1)
	p.goPage("lab")
	p.choose("鑑定一般種子", "wheat")
	p.goPage("plots")
	p.choose("選擇作物，種到", "wheat") // 種滿所有空地

	p.goPage("lab_seeds")
	p.choose("選擇種子", p.options("選擇種子")[0])
	contains(t, p.Text, "農田目前沒有空地")
	s := p.selectMenu("農田沒有空地")
	if s == nil || !s.Disabled {
		t.Fatal("沒有空地時應顯示停用的種下選單")
	}
	if p.selectMenu("🌱 種下這顆種子（空地") != nil {
		t.Fatal("沒有空地時不應出現可用的種下選單")
	}
}

// TestPlantLabSeedsFromPlots 農田頁的特殊種子選單（2026-11-15）：沒有特殊種子時不顯示；勾選的每顆種到一格空地。
func TestPlantLabSeedsFromPlots(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	p.goPage("plots")
	if p.selectMenu("勾選特殊種子") != nil {
		t.Fatal("沒有特殊種子時不顯示")
	}
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation)
		SELECT id, id, c, 3, 3, 3, 1 FROM farms, unnest(ARRAY['wheat', 'corn']) c WHERE player_id = $1`, pid)
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation, status)
		SELECT id, id, 'wheat', 1, 1, 1, 1, 'busy' FROM farms WHERE player_id = $1`, pid)
	p.goPage("plots")
	// 有兩種作物：直接列出全部種子，第一項可以切換成作物清單篩選（2026-11-17），培育中的種子不算
	p.chooseMany("勾選特殊種子", []string{cropFilterPrefix})
	if c := p.selectMenu("選擇要看的作物"); c == nil || len(c.Options) != 3 || !strings.Contains(c.Options[1].Label, "小麥（1 顆）") {
		t.Fatalf("%+v", c)
	}
	p.choose("選擇要看的作物", cropFilterPrefix+"corn")
	if s := p.selectMenu("勾選特殊種子"); s == nil || len(s.Options) != 2 || !strings.Contains(s.Options[0].Label, "目前只看玉米") {
		t.Fatalf("%+v", s)
	}
	p.chooseMany("勾選特殊種子", []string{cropFilterPrefix})
	p.choose("選擇要看的作物", cropFilterPrefix+cropFilterAll)
	s := p.selectMenu("勾選特殊種子")
	if s == nil || len(s.Options) != 3 || s.Options[0].Value != cropFilterPrefix || s.MaxValues != 3 {
		t.Fatalf("培育中的種子不列出：%+v", s)
	}
	p.chooseMany("勾選特殊種子", []string{s.Options[1].Value, s.Options[2].Value})
	contains(t, p.Text, "種下 2 顆特殊種子", "已在第 1 格種下特殊小麥種子", "已在第 2 格種下特殊玉米種子")
	p.goPage("plots")
	if p.selectMenu("勾選特殊種子") != nil {
		t.Fatal("種完後沒有可種的特殊種子時不顯示")
	}
	e.checkLedger()
}

// TestPlantLabSeedsFromPlotsPaged 特殊種子超過 25 顆時，農田頁的選單可以在選單內翻頁，種到後面頁的種子（2026-11-16）。
func TestPlantLabSeedsFromPlotsPaged(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation)
		SELECT id, id, 'wheat', 1 + g % 5, 1, 1, 1 FROM farms, generate_series(1, 30) g WHERE player_id = $1`, pid)
	p.goPage("plots")
	if got := p.options("勾選特殊種子"); len(got) != 25 || got[0] != pagePrev || got[24] != pageNext {
		t.Fatal(got)
	}
	contains(t, p.selectMenu("勾選特殊種子").Placeholder, "第 1/2 頁")
	p.chooseMany("勾選特殊種子", []string{pageNext})
	got := p.options("勾選特殊種子")
	if len(got) != 9 { // 30 − 23 = 7 顆＋2 個翻頁選項
		t.Fatal(len(got))
	}
	contains(t, p.selectMenu("勾選特殊種子").Placeholder, "第 2/2 頁")
	last := got[7]
	before := e.scalar(`SELECT status FROM lab_seeds WHERE id = $1`, last)
	p.chooseMany("勾選特殊種子", []string{last, pageNext}) // 同時勾翻頁選項時只種種子
	contains(t, p.Text, "已在第 1 格種下特殊小麥種子")
	if after := e.scalar(`SELECT status FROM lab_seeds WHERE id = $1`, last); after == before {
		t.Fatal("第 2 頁的種子應該已經種下")
	}
	e.checkLedger()
}

// TestLabSeedsCropFilter 種子庫依作物篩選（2026-11-17）：同作物排在一起，選了作物只顯示那種作物；選了種子後可以取消選取。
func TestLabSeedsCropFilter(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation)
		SELECT id, id, c, 1, 1, 1, 1 FROM farms, unnest(ARRAY['wheat', 'corn', 'wheat', 'corn', 'wheat']) c WHERE player_id = $1`, pid)
	p.goPage("lab_seeds")
	f := p.selectMenu("篩選作物")
	if f == nil || len(f.Options) != 3 || !strings.Contains(f.Options[0].Label, "全部作物（5 顆）") {
		t.Fatalf("%+v", f)
	}
	seeds := p.selectMenu("選擇種子")
	if seeds == nil || len(seeds.Options) != 5 || seeds.Options[0].Label != "小麥 第 1 代" || seeds.Options[3].Label != "玉米 第 1 代" {
		t.Fatalf("同作物應排在一起：%+v", seeds)
	}
	p.choose("篩選作物", cropFilterPrefix+"corn")
	contains(t, p.Text, "只看 玉米 2 顆")
	if s := p.selectMenu("選擇種子"); len(s.Options) != 2 || s.Options[0].Label != "玉米 第 1 代" {
		t.Fatalf("%+v", s)
	}
	p.choose("選擇種子", p.options("選擇種子")[0])
	if p.selectMenu("篩選作物") != nil {
		t.Fatal("選了種子後篩選選單讓位給種下選單")
	}
	p.click("取消選取")
	if p.selectMenu("篩選作物") == nil {
		t.Fatal("取消選取後回到篩選")
	}
}
