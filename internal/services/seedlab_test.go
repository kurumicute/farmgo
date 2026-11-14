package services

// 種子實驗室（Go 版新增，2026-11-13，Python 沒有）：Lv.5 建造、鑑定、雜交（同品種改良／雜交作物／失敗）、突變研究、
// 特殊種子的種植與收成（基因效果、突變直接帶到作物、水晶、傳承），以及分享的每日上限。

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seedlab"
)

func (e *env) lab(pid int64) SeedLabView {
	e.t.Helper()
	return must[SeedLabView](e.t)(e.s.SeedLab(e.ctx, pid))
}

// labOwner 建立 Lv.5、有實驗室與足夠金幣的玩家。
func (e *env) labOwner(name string, groupID int64) int64 {
	e.t.Helper()
	pid := e.farmer(name, groupID)
	e.setGoldRecorded(pid, 1_000_000)
	e.exec("UPDATE farms SET level = 5 WHERE player_id = $1", pid)
	e.ok(e.s.BuildSeedLab(e.ctx, pid))
	return pid
}

// labSeed 在種子庫找出指定作物的種子（最新的一顆）。
func (e *env) labSeed(pid int64, crop string) LabSeedView {
	e.t.Helper()
	v := e.lab(pid)
	for i := len(v.Seeds) - 1; i >= 0; i-- {
		if v.Seeds[i].CropCode == crop && !v.Seeds[i].Busy {
			return v.Seeds[i]
		}
	}
	e.t.Fatalf("種子庫沒有 %s：%+v", crop, v.Seeds)
	return LabSeedView{}
}

// identify 給一顆一般種子並鑑定。
func (e *env) identify(pid int64, crop string) LabSeedView {
	e.t.Helper()
	e.give(pid, "seed_"+crop, 1)
	e.ok(e.s.IdentifySeed(e.ctx, pid, crop))
	return e.labSeed(pid, crop)
}

func TestSeedLabUnlockAndBuild(t *testing.T) {
	e := setup(t)
	pid := e.farmer("阿明", e.group("主群組"))
	e.setGoldRecorded(pid, 100_000)

	e.exec("UPDATE farms SET level = 4 WHERE player_id = $1", pid)
	if v := e.lab(pid); v.Unlocked || v.UnlockLevel != 5 {
		t.Fatalf("%+v", v)
	}
	_, err := e.s.BuildSeedLab(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "Lv.5")

	e.exec("UPDATE farms SET level = 5 WHERE player_id = $1", pid)
	if v := e.lab(pid); !v.Unlocked || v.Built || v.BuildGold != 5000 {
		t.Fatalf("%+v", v)
	}
	_, err = e.s.IdentifySeed(e.ctx, pid, "wheat")
	wantMsg(t, err, gameerr.Validation, "還沒有建造")
	e.setGoldRecorded(pid, 4_999)
	_, err = e.s.BuildSeedLab(e.ctx, pid)
	wantKind(t, err, gameerr.InsufficientGold)

	// 只要金幣（預設 5,000），不需要素材
	e.setGoldRecorded(pid, 100_000)
	e.ok(e.s.BuildSeedLab(e.ctx, pid))
	if e.gold(pid) != 100_000-5000 {
		t.Fatal(e.gold(pid))
	}
	_, err = e.s.BuildSeedLab(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "已經建造")
	v := e.lab(pid)
	if !v.Built || v.LabLevel != 1 || v.Slots != 1 || v.FreeSlots != 1 || len(v.Seeds) != 0 {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}

func TestSeedLabIdentify(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	gold := e.gold(pid)

	e.s.LabRng = func() float64 { return 0.1 } // u < 0.5：每個基因都多 1 級
	_, err := e.s.IdentifySeed(e.ctx, pid, "wheat")
	wantKind(t, err, gameerr.InsufficientItems)
	wheat := e.identify(pid, "wheat")
	if wheat.Genes != (seedlab.Genes{Growth: 2, Yield: 2, Quality: 2, Mutation: 2}) || wheat.Generation != 1 || wheat.Mutation != "none" {
		t.Fatalf("%+v", wheat)
	}
	moon := e.identify(pid, "moonflower") // 稀有作物：2–3 級
	if moon.Genes != (seedlab.Genes{Growth: 3, Yield: 3, Quality: 3, Mutation: 3}) {
		t.Fatalf("%+v", moon)
	}
	fee := seedlab.IdentifyFee(5) + seedlab.IdentifyFee(500) // 小麥 5 金、月光花 500 金
	if e.gold(pid) != gold-fee || e.qty(pid, "seed_wheat") != 0 {
		t.Fatal(e.gold(pid), gold-fee)
	}
	v := e.lab(pid)
	if v.Exp != 2*seedlab.Current().ExpIdentify || len(v.Seeds) != 2 {
		t.Fatalf("%+v", v)
	}
	// 種子庫上限
	e.exec(`INSERT INTO lab_seeds (farm_id, breeder_farm_id, crop_code, gene_growth, gene_yield, gene_quality, gene_mutation)
		SELECT id, id, 'wheat', 1, 1, 1, 1 FROM farms, generate_series(1, $2) WHERE player_id = $1`, pid, seedlab.Current().LibraryCapacity-2)
	e.give(pid, "seed_wheat", 1)
	_, err = e.s.IdentifySeed(e.ctx, pid, "wheat")
	wantMsg(t, err, gameerr.Validation, "種子庫已滿")
	e.invariant()
}

func TestSeedLabCross(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	e.s.LabRng = func() float64 { return 0.9 }
	wheat := e.identify(pid, "wheat")
	corn := e.identify(pid, "corn")
	grape := e.identify(pid, "grape")

	_, err := e.s.StartCross(e.ctx, pid, wheat.ID, corn.ID, true, false, "")
	wantMsg(t, err, gameerr.InsufficientItems, "樹脂")

	// 小麥 × 玉米 → 黃金穀物（用樹脂與水晶催化產量基因）。配方隱藏（2026-11-14）：還沒發現的配方不顯示結果與成功率
	e.give(pid, seedlab.MatResin, 1)
	e.give(pid, seedlab.MatCrystal, 1)
	r := e.ok(e.s.StartCross(e.ctx, pid, wheat.ID, corn.ID, true, false, seedlab.GeneYield))
	if !strings.Contains(r.Message, "試驗雜交") || strings.Contains(r.Message, "黃金穀物") || strings.Contains(r.Message, "成功率") {
		t.Fatal(r.Message)
	}
	v := e.lab(pid)
	if len(v.Jobs) != 1 || v.FreeSlots != 0 || !v.Jobs[0].Unknown || v.Jobs[0].ResultName != "❔ 結果未知" || v.ReadyCount != 0 ||
		len(v.KnownHybrids) != 0 {
		t.Fatalf("%+v", v.Jobs)
	}
	_, err = e.s.StartCross(e.ctx, pid, grape.ID, grape.ID, false, false, "")
	wantMsg(t, err, gameerr.Validation, "不同的兩顆")
	_, err = e.s.StartMutation(e.ctx, pid, grape.ID, false)
	wantMsg(t, err, gameerr.Validation, "研究槽已滿")
	_, err = e.s.CollectLab(e.ctx, pid)
	wantMsg(t, err, gameerr.Validation, "還沒完成")
	_, err = e.s.PlantLabSeed(e.ctx, pid, wheat.ID, 1)
	wantMsg(t, err, gameerr.Validation, "培育中")

	// 成功（0 → 成功；每個基因都繼承母本並升級；沒有突變的雙親依突變基因擲出 giant）
	e.s.LabRng = func() float64 { return 0 }
	e.clock.Advance((2 * time.Hour).Seconds())
	r = e.ok(e.s.CollectLab(e.ctx, pid))
	if !strings.Contains(r.Message, "雜交成功") || !strings.Contains(r.Message, "📖 發現新配方：小麥 × 玉米 → 黃金穀物") {
		t.Fatal(r.Message)
	}
	gg := e.labSeed(pid, "golden_grain")
	if gg.Generation != 2 || gg.Genes != (seedlab.Genes{Growth: 2, Yield: 2, Quality: 2, Mutation: 2}) || gg.Mutation != "giant" || !gg.Hybrid {
		t.Fatalf("%+v", gg)
	}
	if n := e.scalar("SELECT count(*)::int FROM lab_seeds WHERE status = 'used'"); n != 2 {
		t.Fatal(n)
	}
	if e.qty(pid, seedlab.MatResin) != 0 || e.qty(pid, seedlab.MatCrystal) != 0 {
		t.Fatal("材料應該已經用掉")
	}
	v = e.lab(pid)
	if v.Exp != 3*seedlab.Current().ExpIdentify+seedlab.Current().ExpCross || len(v.Jobs) != 0 {
		t.Fatalf("%+v", v)
	}

	// 失敗：得到母本品種（葡萄），材料退回
	wm := e.identify(pid, "watermelon")
	e.give(pid, seedlab.MatHerb, 1)
	e.ok(e.s.StartCross(e.ctx, pid, grape.ID, wm.ID, false, true, ""))
	e.s.LabRng = func() float64 { return 0.99 }
	e.clock.Advance((5 * time.Hour).Seconds())
	r = e.ok(e.s.CollectLab(e.ctx, pid))
	if !strings.Contains(r.Message, "雜交失敗") || !strings.Contains(r.Message, "好像有反應") { // 有這個配方但還沒發現：給提示
		t.Fatal(r.Message)
	}
	if g := e.labSeed(pid, "grape"); g.Generation != 2 {
		t.Fatalf("%+v", g)
	}
	if e.qty(pid, seedlab.MatHerb) != 1 {
		t.Fatal("失敗時材料要退回")
	}
	recs := must[[]LabRecordView](t)(e.s.LabRecords(e.ctx, pid, 10))
	if len(recs) != 6 || recs[0].Kind != "cross" || recs[0].Success || !recs[1].Success {
		t.Fatalf("%+v", recs)
	}

	// 已發現的配方：顯示結果與成功率；失敗時不再給提示
	w2, c2 := e.identify(pid, "wheat"), e.identify(pid, "corn")
	r = e.ok(e.s.StartCross(e.ctx, pid, w2.ID, c2.ID, false, false, ""))
	if !strings.Contains(r.Message, "小麥 × 玉米 → 黃金穀物（成功率 70%）") {
		t.Fatal(r.Message)
	}
	if v := e.lab(pid); !v.KnownHybrids["golden_grain"] || v.Jobs[0].Unknown || v.Jobs[0].ResultName != "黃金穀物" {
		t.Fatalf("%+v %+v", v.KnownHybrids, v.Jobs)
	}
	e.clock.Advance((2 * time.Hour).Seconds())
	if r = e.ok(e.s.CollectLab(e.ctx, pid)); !strings.Contains(r.Message, "雜交失敗") || strings.Contains(r.Message, "好像有反應") {
		t.Fatal(r.Message)
	}

	// 不在雜交表中的組合（小麥 × 葡萄）也能試驗：一定得到母本品種，材料退回
	w3, g3 := e.identify(pid, "wheat"), e.identify(pid, "grape")
	e.give(pid, seedlab.MatResin, 1)
	r = e.ok(e.s.StartCross(e.ctx, pid, w3.ID, g3.ID, true, false, ""))
	if !strings.Contains(r.Message, "試驗雜交：小麥 × 葡萄 → ❔ 結果未知") {
		t.Fatal(r.Message)
	}
	e.s.LabRng = func() float64 { return 0 } // 擲骰成功也不會產生新品種
	e.clock.Advance(float64(seedlab.Current().SameSpeciesMinutes * 60))
	if r = e.ok(e.s.CollectLab(e.ctx, pid)); !strings.Contains(r.Message, "沒有產生新品種，得到小麥種子") {
		t.Fatal(r.Message)
	}
	if e.qty(pid, seedlab.MatResin) != 1 {
		t.Fatal("試驗沒有結果時材料要退回")
	}
	e.invariant()
}

func TestSeedLabMutationAndPlant(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	e.s.LabRng = func() float64 { return 0.1 }
	wheat := e.identify(pid, "wheat") // 基因都是 2 級

	_, err := e.s.StartMutation(e.ctx, pid, wheat.ID, false)
	wantMsg(t, err, gameerr.InsufficientItems, "水晶")
	e.give(pid, seedlab.MatCrystal, seedlab.Current().MutateCrystal)
	e.ok(e.s.StartMutation(e.ctx, pid, wheat.ID, false))
	// 成功率 30% + 5%（突變基因 2 級）= 35%：u = 0.99 → 失敗，種子放回種子庫
	e.s.LabRng = func() float64 { return 0.99 }
	e.clock.Advance((seedlab.MutateDuration()).Seconds())
	r := e.ok(e.s.CollectLab(e.ctx, pid))
	if !strings.Contains(r.Message, "沒有成功") {
		t.Fatal(r.Message)
	}
	e.give(pid, seedlab.MatCrystal, seedlab.Current().MutateCrystal)
	e.ok(e.s.StartMutation(e.ctx, pid, wheat.ID, false))
	seq := []float64{0, 6.5 / 7} // 先擲成功（0 < 35%），再挑選突變：int(6.5) = 第 7 個（crystal）
	i := 0
	e.s.LabRng = func() float64 { v := seq[min(i, len(seq)-1)]; i++; return v }
	e.clock.Advance((seedlab.MutateDuration()).Seconds())
	r = e.ok(e.s.CollectLab(e.ctx, pid))
	if !strings.Contains(r.Message, "水晶作物") {
		t.Fatal(r.Message)
	}
	seed := e.labSeed(pid, "wheat")
	if seed.Mutation != "crystal" || seed.Busy {
		t.Fatalf("%+v", seed)
	}

	// 種下：突變直接帶到作物上
	r = e.ok(e.s.PlantLabSeed(e.ctx, pid, seed.ID, 1))
	if !strings.Contains(r.Message, "特殊小麥種子") {
		t.Fatal(r.Message)
	}
	if m := e.scalar("SELECT count(*)::int FROM farm_plots WHERE mutation = 'crystal' AND lab_seed_id IS NOT NULL"); m != 1 {
		t.Fatal(m)
	}
	_, err = e.s.PlantLabSeed(e.ctx, pid, seed.ID, 2)
	wantKind(t, err, gameerr.NotFound) // 已經種下
	// 澆水：生長基因 2 級 → -6%
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	if n := e.scalar("SELECT count(*)::int FROM game_transactions WHERE event_type = 'water' AND description LIKE '%生長基因 -6%'"); n != 1 {
		t.Fatal(n)
	}
	// 收成：水晶作物額外給水晶；u = 0 → 取回 1 顆同基因的種子
	e.s.LabRng = func() float64 { return 0 }
	e.clock.Advance(120)
	r = e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if !strings.Contains(r.Message, "水晶 +1") || !strings.Contains(r.Message, "取回 1 顆") {
		t.Fatal(r.Message)
	}
	if e.qty(pid, seedlab.MatCrystal) != 1 {
		t.Fatal(e.qty(pid, seedlab.MatCrystal))
	}
	back := e.labSeed(pid, "wheat")
	if back.ID == seed.ID || back.Genes != seed.Genes || back.Mutation != "crystal" || back.Generation != seed.Generation {
		t.Fatalf("%+v", back)
	}
	if n := e.scalar("SELECT count(*)::int FROM lab_seeds WHERE id = $1 AND status = 'used'", seed.ID); n != 1 {
		t.Fatal("收成後原本的種子要標記為已消耗")
	}
	if n := e.scalar("SELECT count(*)::int FROM farm_plots WHERE lab_seed_id IS NOT NULL"); n != 0 {
		t.Fatal(n)
	}
	e.invariant()
}

func TestSeedLabYieldAndGlow(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	e.s.LabRng = func() float64 { return 0.9 }
	seed := e.identify(pid, "wheat")
	e.exec("UPDATE lab_seeds SET gene_yield = 5, mutation = 'glow' WHERE id = $1", seed.ID)
	e.ok(e.s.PlantLabSeed(e.ctx, pid, seed.ID, 1))
	// 小麥每次收成 2 個 + 產量基因 5 級：2 × 15% × 4 = 1.2，無條件進位 +2
	if n := e.scalar("SELECT remaining_quantity FROM farm_plots WHERE plot_number = 1 AND lab_seed_id IS NOT NULL"); n != 4 {
		t.Fatal(n)
	}
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	e.s.LabRng = func() float64 { return 0.99 } // 不取回種子
	e.clock.Advance(120)
	r := e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	if !strings.Contains(r.Message, "夜光作物") || strings.Contains(r.Message, "取回") {
		t.Fatal(r.Message)
	}
	// 夜光作物售價 200%
	gold := e.gold(pid)
	e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 4, "normal", "glow"))
	if got := e.gold(pid) - gold; got != 4*11*2 && got != 4*15*2 {
		t.Fatal(got)
	}
	e.invariant()
}

func TestSeedLabShareLimits(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	a := e.labOwner("阿明", g)
	b := e.labOwner("阿華", g)
	c := e.labOwner("阿強", g)
	outsider := e.labOwner("路人", e.group("別的群組"))
	nolab := e.farmer("新手", g)
	e.s.LabRng = func() float64 { return 0.1 }

	seeds := make([]LabSeedView, 0, 7)
	for range 7 {
		seeds = append(seeds, e.identify(a, "wheat"))
	}
	targets := must[[]LabShareTargetView](t)(e.s.LabShareTargets(e.ctx, g, a))
	if len(targets) != 3 { // 阿華、阿強、新手（沒有實驗室也能收；別的群組不列出）
		t.Fatalf("%+v", targets)
	}
	_, err := e.s.ShareLabSeed(e.ctx, g, a, seeds[0].ID, a)
	wantMsg(t, err, gameerr.Validation, "自己")
	_, err = e.s.ShareLabSeed(e.ctx, g, a, seeds[0].ID, outsider)
	wantMsg(t, err, gameerr.Validation, "不在")
	_, err = e.s.ShareLabSeed(e.ctx, g, b, seeds[0].ID, c)
	wantKind(t, err, gameerr.NotFound) // 不是自己的種子

	// 同一位玩家可以收同一個人的多顆；每人每天最多送出 5 顆
	for i := range 4 {
		e.ok(e.s.ShareLabSeed(e.ctx, g, a, seeds[i].ID, b))
	}
	e.ok(e.s.ShareLabSeed(e.ctx, g, a, seeds[4].ID, c))
	_, err = e.s.ShareLabSeed(e.ctx, g, a, seeds[5].ID, c)
	wantMsg(t, err, gameerr.Validation, "每天最多 5 顆")
	if v := e.lab(b); len(v.Seeds) != 4 || len(v.ReceivedFrom) != 1 {
		t.Fatalf("%+v", v)
	}
	if n := e.scalar("SELECT count(*)::int FROM lab_seeds WHERE farm_id = (SELECT id FROM farms WHERE player_id = $1)", b); n != 4 {
		t.Fatal(n)
	}

	// 阿華今天已收過阿明的分享：阿強不能再分享給阿華
	cs := e.identify(c, "corn")
	_, err = e.s.ShareLabSeed(e.ctx, g, c, cs.ID, b)
	wantMsg(t, err, gameerr.Validation, "已經收過其他玩家")
	// 隔天（台灣時間）重新計算
	e.clock.Advance((24 * time.Hour).Seconds())
	e.ok(e.s.ShareLabSeed(e.ctx, g, c, cs.ID, b))
	e.ok(e.s.ShareLabSeed(e.ctx, g, a, seeds[5].ID, c))

	// 沒有實驗室（還不到 Lv.5）的玩家也能收分享、在種子庫看到並種下
	e.ok(e.s.ShareLabSeed(e.ctx, g, a, seeds[6].ID, nolab))
	v := e.lab(nolab)
	if v.Built || v.Unlocked || len(v.Seeds) != 1 {
		t.Fatalf("%+v", v)
	}
	r := e.ok(e.s.PlantLabSeed(e.ctx, nolab, v.Seeds[0].ID, 1))
	if !strings.Contains(r.Message, "特殊小麥種子") {
		t.Fatal(r.Message)
	}
	if n := e.scalar("SELECT count(*)::int FROM game_transactions WHERE event_type = 'seed_share'"); n != 2*8 {
		t.Fatal(n)
	}
	e.invariant()
}

func TestSeedLabCodexAndAchievements(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	a := e.labOwner("阿明", g)
	b := e.farmer("阿華", g)
	e.s.LabRng = func() float64 { return 0 } // 雜交一定成功
	wheat := e.identify(a, "wheat")
	corn := e.identify(a, "corn")
	e.ok(e.s.StartCross(e.ctx, a, wheat.ID, corn.ID, false, false, ""))
	e.clock.Advance(3 * 3600)
	e.ok(e.s.CollectLab(e.ctx, a))
	gg := e.labSeed(a, "golden_grain")
	e.ok(e.s.ShareLabSeed(e.ctx, g, a, gg.ID, b))

	codexOf := func(pid int64) map[string]CodexCategory {
		out := map[string]CodexCategory{}
		for _, c := range must[CodexView](t)(e.p.Codex(e.ctx, pid)).Categories {
			out[c.Key] = c
		}
		return out
	}
	ca, cb := codexOf(a), codexOf(b)
	if ca["lab"].Discovered != 3 || cb["lab"].Discovered != 1 || ca["lab"].Total != cb["lab"].Total || ca["lab"].Total < 22 {
		t.Fatalf("%+v %+v", ca["lab"], cb["lab"])
	}
	if ca["mushroom"].Total != 6 || ca["mushroom"].Discovered != 0 {
		t.Fatalf("%+v", ca["mushroom"])
	}
	for _, x := range ca["goods"].Entries {
		if x.Code == "shiitake" {
			t.Fatal("菇類不再列在加工品")
		}
	}
	ach := must[AchievementView](t)(e.p.Achievements(e.ctx, a))
	state := map[string]string{}
	for _, r := range ach.Rows {
		state[r.Code] = r.State
	}
	if state["lab_build"] != "ready" || state["lab_hybrid_1"] != "ready" || state["lab_share_10"] != "progress" ||
		state["mushroom_100"] != "progress" {
		t.Fatal(state)
	}
}
