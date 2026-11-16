package seedlab

import (
	"slices"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

// seq 依序回傳指定的亂數（用完後重複最後一個）。
func seq(vals ...float64) func() float64 {
	i := 0
	return func() float64 {
		v := vals[min(i, len(vals)-1)]
		i++
		return v
	}
}

func TestHybridTable(t *testing.T) {
	if len(Hybrids) != 12 {
		t.Fatalf("雜交作物 %d 種", len(Hybrids))
	}
	seenCode := map[string]bool{}
	seenPair := map[[2]string]bool{}
	for _, h := range CurrentHybrids() {
		if seenCode[h.Code] {
			t.Errorf("重複的雜交作物 %s", h.Code)
		}
		seenCode[h.Code] = true
		pair := [2]string{min(h.A, h.B), max(h.A, h.B)}
		if seenPair[pair] || h.A == h.B {
			t.Errorf("重複或同品種的組合 %v", pair)
		}
		seenPair[pair] = true
		if h.SuccessBP <= 0 || h.SuccessBP > 10_000 || h.Minutes < 60 || h.Minutes > 180 { // 預設 1–3 小時
			t.Errorf("%s 數值不合理：%+v", h.Code, h)
		}
		// 雙親都是現有作物（不是雜交作物）
		if IsHybrid(h.A) || IsHybrid(h.B) {
			t.Errorf("%s 的雙親不能是雜交作物", h.Code)
		}
		if FindHybrid(h.B, h.A) == nil || FindHybrid(h.A, h.B).Code != h.Code {
			t.Errorf("%s 雙親順序不應影響結果", h.Code)
		}
	}
	// 10 種現有作物都至少出現一次
	for _, c := range []string{"wheat", "carrot", "tomato", "strawberry", "corn", "pumpkin", "grape", "watermelon",
		"amber_melon", "moonflower"} {
		if !slices.ContainsFunc(Hybrids, func(h Hybrid) bool { return h.A == c || h.B == c }) {
			t.Errorf("%s 沒有出現在雜交表", c)
		}
	}
	if !Compatible("wheat", "wheat") || !Compatible("purple_berry", "purple_berry") || Compatible("wheat", "grape") ||
		Compatible("purple_berry", "wheat") {
		t.Fatal("相容判斷錯誤")
	}
}

func TestHybridCropsMatchTable(t *testing.T) {
	if len(HybridCrops) != len(Hybrids) {
		t.Fatal(len(HybridCrops))
	}
	for i, c := range HybridCrops {
		if c.Code != Hybrids[i].Code || c.VariantCode == "" || c.GrowSeconds <= 0 || c.HarvestQty <= 0 {
			t.Errorf("%d: %+v", i, c)
		}
	}
	// 雙親價格總和 × 1.5，四捨五入
	if HybridPrice(11, 20) != 47 || HybridPrice(10, 20) != 45 || HybridPrice(1, 0) != 2 {
		t.Fatal(HybridPrice(11, 20), HybridPrice(10, 20))
	}
}

func TestConfigDefaultsAndOverrides(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	if v, err := ValidateConfig(DefaultValue()); err != nil || v["build_gold"] != 5000 || v["unlock_level"] != 5 {
		t.Fatal(v, err)
	}
	raw, _ := featurecfg.DeepCopy(DefaultValue())
	raw["build_gold"] = 8000
	raw["same_species_minutes"] = 45
	raw["hybrids"].(map[string]any)["golden_grain"] = map[string]any{"success_bp": 9900, "minutes": 10}
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if c := Current(); c.BuildGold != 8000 || c.SameSpeciesMinutes != 45 {
		t.Fatalf("%+v", c)
	}
	if h := FindHybrid("corn", "wheat"); h.SuccessBP != 9900 || h.Minutes != 10 {
		t.Fatalf("%+v", h)
	}
	if d := (CrossPlan{Mother: "wheat", Father: "wheat"}).Duration(); d != 45*time.Minute {
		t.Fatal(d)
	}
	// 舊快照沒有新增的設定項目（例如 yield_pct_per_level）、還留著已移除的 key：缺的用預設值，整份設定照樣生效
	old, _ := featurecfg.DeepCopy(DefaultValue())
	delete(old, "yield_pct_per_level")
	old["yield_levels_per_bonus"] = 2
	old["identify_fee_multiplier"] = 2
	if v, err := featurecfg.Validate(ConfigKey, old); err != nil || v["yield_pct_per_level"] != 15 || v["identify_fee_multiplier"] != 2 {
		t.Fatal(v, err)
	} else if _, stale := v["yield_levels_per_bonus"]; stale {
		t.Fatal("已移除的 key 應該被丟掉")
	}
	for _, bad := range []func(featurecfg.Value){
		func(v featurecfg.Value) { v["gene_up_bp"], v["gene_down_bp"] = 6000, 5000 },
		func(v featurecfg.Value) { v["level3_exp"] = 50 },
		func(v featurecfg.Value) { v["clone_two_bp"], v["clone_three_bp"] = 1000, 2000 },
		func(v featurecfg.Value) { v["clone_max_batches"] = 26 },
		func(v featurecfg.Value) { v["library_capacity"] = 201 },
		func(v featurecfg.Value) { delete(v["hybrids"].(map[string]any), "dawn_melon") },
		func(v featurecfg.Value) { v["build_gold"] = 1.5 },
	} {
		raw, _ := featurecfg.DeepCopy(DefaultValue())
		bad(raw)
		if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
			t.Errorf("應該拒絕：%v", raw)
		}
	}
}

// TestExpansions 種子庫與研究槽擴充（2026-11-15）。
func TestExpansions(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	base := Current().LibraryCapacity
	if LibraryCap(0) != base || LibraryCap(2) != base+20 || LibraryCap(99) != base+40 {
		t.Fatal(LibraryCap(0), LibraryCap(2), LibraryCap(99))
	}
	if p, ok := NextLibrary(3); !ok || p != 80_000 {
		t.Fatal(p, ok)
	}
	if _, ok := NextLibrary(4); ok {
		t.Fatal("滿級")
	}
	if SlotsFor(3, 0) != 3 || SlotsFor(3, 1) != 4 || SlotsFor(3, 5) != 4 || SlotsFor(1, 1) != 2 {
		t.Fatal(SlotsFor(3, 1))
	}
	raw, _ := featurecfg.DeepCopy(DefaultValue())
	delete(raw, "library_prices") // 舊快照沒有：用預設值
	raw["extra_slot_prices"] = []any{50_000, 200_000}
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if p, ok := NextExtraSlot(1); !ok || p != 200_000 || SlotsFor(3, 2) != 5 || len(Current().LibraryPrices) != 4 {
		t.Fatal(p, ok)
	}
	raw["extra_slot_prices"] = []any{1, 2, 3, 4, 5, 6}
	if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
		t.Fatal("研究槽最多擴充 5 個")
	}
}

func TestCrossPlan(t *testing.T) {
	same := CrossPlan{Mother: "wheat", Father: "wheat"}
	if code, bp, ok := same.Result(); !ok || code != "wheat" || bp != 10_000 {
		t.Fatal(code, bp, ok)
	}
	if same.Duration() != 30*time.Minute {
		t.Fatal(same.Duration())
	}
	p := CrossPlan{Mother: "strawberry", Father: "moonflower", Resin: true, Herb: true, Catalyst: GeneYield}
	code, bp, ok := p.Result()
	if !ok || code != "starry_night_flower" || bp != 4000+Current().ResinSuccessBP {
		t.Fatal(code, bp, ok)
	}
	if p.Duration() != 3*time.Hour*70/100 {
		t.Fatal(p.Duration())
	}
	mats := p.Materials()
	if mats[MatResin] != 1 || mats[MatHerb] != 1 || mats[MatCrystal] != 1 || len(mats) != 3 {
		t.Fatal(mats)
	}
	if _, _, ok := (CrossPlan{Mother: "wheat", Father: "grape"}).Result(); ok {
		t.Fatal("不相容的組合不能雜交")
	}
	if len((CrossPlan{Mother: "wheat", Father: "wheat"}).Materials()) != 0 {
		t.Fatal("沒有材料")
	}
}

// noDominant 暫時關閉優勢遺傳（亂數 < 0.5 母本、其餘父本，與 2026-11-15 之前相同）。
func noDominant(t *testing.T) {
	old := cfgDefault.DominantBP
	cfgDefault.DominantBP = 0
	t.Cleanup(func() { cfgDefault.DominantBP = old })
}

func TestInheritGenes(t *testing.T) {
	noDominant(t)
	mother := Parent{Genes{2, 2, 2, 2}, 1, "none"}
	father := Parent{Genes{5, 5, 5, 5}, 3, "none"}
	// 每個基因：先選親（<0.5 母本），再擲升降（0.99 = 維持）；最後擲突變（0.99 = 不突變）
	c := Inherit(mother, father, "", seq(0.1, 0.99, 0.9, 0.99, 0.1, 0.99, 0.9, 0.99, 0.99))
	if c.Genes != (Genes{2, 5, 2, 5}) || c.Generation != 4 || c.Mutation != "none" {
		t.Fatalf("%+v", c)
	}
	// 升級（u < 25%）與上限 5；退化（25%–27%）與下限 1
	c = Inherit(Parent{Genes{1, 1, 1, 1}, 1, "none"}, Parent{Genes{5, 5, 5, 5}, 1, "none"}, "",
		seq(0.9, 0.0, 0.1, 0.26, 0.9, 0.26, 0.1, 0.0, 0.99))
	if c.Genes != (Genes{5, 1, 4, 2}) {
		t.Fatalf("%+v", c.Genes)
	}
	// 催化劑：u = 0.3 一般是維持，指定的基因升級（25% + 30% = 55%）
	c = Inherit(mother, mother, GeneQuality, seq(0.1, 0.3, 0.1, 0.3, 0.1, 0.3, 0.1, 0.3, 0.99))
	if c.Genes != (Genes{2, 2, 3, 2}) {
		t.Fatalf("%+v", c.Genes)
	}
}

// TestInheritDominant 優勢遺傳 70%：u < 0.7 取較高的親本；其餘 0.7–0.85 母本、0.85–1 父本。
func TestInheritDominant(t *testing.T) {
	mother := Parent{Genes{3, 1, 3, 2}, 2, "none"}
	father := Parent{Genes{2, 3, 1, 2}, 5, "none"}
	c := Inherit(mother, father, "", seq(0.69, 0.99, 0.1, 0.99, 0.75, 0.99, 0.9, 0.99, 0.99))
	if c.Genes != (Genes{3, 3, 3, 2}) || c.Generation != 6 {
		t.Fatalf("%+v", c)
	}
	// 沒中優勢遺傳時才可能拿到較低的那個
	c = Inherit(mother, father, "", seq(0.8, 0.99, 0.8, 0.99, 0.9, 0.99, 0.99, 0.99, 0.99))
	if c.Genes != (Genes{3, 1, 1, 2}) {
		t.Fatalf("%+v", c.Genes)
	}
	// 3333 × 2222：每個基因 ≥3 級的機率 0.85×98% + 0.15×25%，四個都 ≥3 約 57%（原本 50% 繼承時約 13%）
	hi, lo := Parent{Genes{3, 3, 3, 3}, 1, "none"}, Parent{Genes{2, 2, 2, 2}, 1, "none"}
	n, ok := 0, 0
	for i := 0; i < 40; i++ {
		for j := 0; j < 40; j++ {
			for k := 0; k < 4; k++ { // 每次只看一個基因：來源亂數 i/40、升降亂數 j/40
				n++
				vals := []float64{0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99}
				vals[2*k], vals[2*k+1] = (float64(i)+0.5)/40, (float64(j)+0.5)/40
				if Inherit(hi, lo, "", seq(vals...)).Genes.Get(GeneOrder[k]) >= 3 {
					ok++
				}
			}
		}
	}
	if p := float64(ok) / float64(n); p < 0.85 || p > 0.89 {
		t.Fatal("每個基因 ≥3 級的機率", p)
	}
}

func TestCloneFee(t *testing.T) {
	// 售價 × 一次收成數量（含產量基因）× 50%，無條件進位：售價 300、收成 3 個 → 450
	for _, c := range []struct {
		g           Genes
		price, base int
		want        int
	}{
		{Genes{1, 1, 1, 1}, 300, 3, 450},
		{Genes{5, 1, 5, 5}, 300, 3, 450}, // 只有產量基因影響費用
		{Genes{1, 3, 1, 1}, 300, 3, 600}, // 產量 3 級：3 × 30% = 0.9 → 進位 +1 → 4 個
		{Genes{1, 1, 1, 1}, 25, 1, 13},   // 12.5 → 13
		{Genes{1, 1, 1, 1}, 0, 3, 0},
	} {
		if got := CloneFee(c.g, c.price, c.base); got != c.want {
			t.Errorf("%+v 售價 %d 收成 %d：%d，應該是 %d", c.g, c.price, c.base, got, c.want)
		}
	}
	if CloneDuration(1) != time.Hour || CloneDuration(3) != 3*time.Hour {
		t.Fatal(CloneDuration(1), CloneDuration(3))
	}
	// 不加藥草：3 顆 10%、2 顆以上 30%；加藥草各 +20%
	for _, c := range []struct {
		herb bool
		u    float64
		want int
	}{{false, 0.09, 3}, {false, 0.1, 2}, {false, 0.29, 2}, {false, 0.3, 1}, {true, 0.29, 3}, {true, 0.3, 2}, {true, 0.49, 2}, {true, 0.5, 1}} {
		if got := RollCloneCopies(c.herb, c.u); got != c.want {
			t.Errorf("藥草 %v、u=%v：%d 顆，應該是 %d", c.herb, c.u, got, c.want)
		}
	}
}

func TestInheritMutation(t *testing.T) {
	noDominant(t)
	g := Genes{1, 1, 1, 1}
	keep := []float64{0.1, 0.99, 0.1, 0.99, 0.1, 0.99, 0.1, 0.99}
	// 母本有夜光突變：u < 50% 遺傳
	c := Inherit(Parent{g, 1, "glow"}, Parent{g, 1, "none"}, "", seq(append(slices.Clone(keep), 0.2, 0.0)...))
	if c.Mutation != "glow" {
		t.Fatal(c.Mutation)
	}
	c = Inherit(Parent{g, 1, "glow"}, Parent{g, 1, "none"}, "", seq(append(slices.Clone(keep), 0.6)...))
	if c.Mutation != "none" {
		t.Fatal(c.Mutation)
	}
	// 雙親沒有突變：突變基因 1 級時只有 1% 自然突變，而且只會是一般的四種
	c = Inherit(Parent{g, 1, "none"}, Parent{g, 1, "none"}, "", seq(append(slices.Clone(keep), 0.005, 0.99)...))
	if c.Mutation != "variant" {
		t.Fatal(c.Mutation)
	}
	c = Inherit(Parent{g, 1, "none"}, Parent{g, 1, "none"}, "", seq(append(slices.Clone(keep), 0.02)...))
	if c.Mutation != "none" {
		t.Fatal(c.Mutation)
	}
}

func TestIdentifyAndEffects(t *testing.T) {
	if g := RollIdentify(false, seq(0.1, 0.9, 0.1, 0.9)); g != (Genes{2, 1, 2, 1}) {
		t.Fatalf("%+v", g)
	}
	if g := RollIdentify(true, seq(0.9)); g != (Genes{2, 2, 2, 2}) {
		t.Fatalf("%+v", g)
	}
	// 產量：收成數量 × 15% × (等級 − 1)，無條件進位（2 收成 2 級 0.3 → 1；3 收成 5 級 1.8 → 2；6 收成 5 級 3.6 → 4）
	if GrowthReductionPct(1) != 0 || GrowthReductionPct(5) != 24 || YieldBonus(1, 3) != 0 || YieldBonus(2, 2) != 1 ||
		YieldBonus(5, 3) != 2 || YieldBonus(5, 6) != 4 || YieldBonus(3, 0) != 0 || QualityBonusBP(5) != 2000 || MutationBonusBP(5) != 600 {
		t.Fatal("基因效果")
	}
	if IdentifyFee(5) != 10 || IdentifyFee(500) != 500 {
		t.Fatal("鑑定費用")
	}
	if Slots(1) != 1 || Slots(3) != 3 || Slots(9) != 3 || LevelFor(0) != 1 || LevelFor(100) != 2 || LevelFor(999) != 3 ||
		NextLevelExp(1) != 100 || NextLevelExp(3) != 0 {
		t.Fatal("等級")
	}
	if (Genes{3, 5, 4, 2}).Text() != "生長 3／產量 5／品質 4／突變 2" {
		t.Fatal((Genes{3, 5, 4, 2}).Text())
	}
}

func TestMutationResearch(t *testing.T) {
	if MutateChanceBP(1, false) != 3000 || MutateChanceBP(5, false) != 5000 || MutateChanceBP(5, true) != 9000 ||
		MutateChanceBP(1, true) != 6000 {
		t.Fatal("成功率")
	}
	for _, cur := range append([]string{"none"}, LabMutations...) {
		for _, u := range []float64{0, 0.5, 0.9999} {
			m := RollLabMutation(cur, u)
			if m == cur || !slices.Contains(LabMutations, m) {
				t.Fatal(cur, u, m)
			}
		}
	}
	// 突變研究的突變都是已登記的突變代碼
	for _, m := range LabMutations {
		if _, ok := balance.Mutations[m]; !ok || !slices.Contains(balance.MutationOrder, m) {
			t.Errorf("突變 %s 沒有登記在 balance", m)
		}
	}
}
