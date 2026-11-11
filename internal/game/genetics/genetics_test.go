package genetics

import (
	"reflect"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

func defaults(t *testing.T) balance.GeneticsSettings {
	t.Helper()
	s, err := ValidateSettings(SettingsMap(balance.DefaultGenetics()))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaultProbabilitiesSumTo100(t *testing.T) {
	s := defaults(t)
	if sumValues(s.QualityBP) != 10_000 {
		t.Fatal(s.QualityBP)
	}
	want := map[string]int{"normal": 7000, "good": 2000, "rare": 800, "legendary": 200}
	if !reflect.DeepEqual(s.QualityBP, want) {
		t.Fatal(s.QualityBP)
	}
	wantPct := map[string]int{"normal": 100, "good": 120, "rare": 150, "legendary": 200}
	if !reflect.DeepEqual(s.QualityPricePct, wantPct) {
		t.Fatal(s.QualityPricePct)
	}
}

func TestInvalidSettingsRejected(t *testing.T) {
	mod := func(f func(m map[string]any)) map[string]any {
		m := SettingsMap(balance.DefaultGenetics())
		f(m)
		return m
	}
	bads := []map[string]any{
		mod(func(m map[string]any) {
			m["quality_bp"] = map[string]any{"normal": 6900, "good": 2000, "rare": 800, "legendary": 200}
		}),
		mod(func(m map[string]any) { m["quality_bp"] = map[string]any{"normal": 7000, "good": 2000, "rare": 800} }),
		mod(func(m map[string]any) {
			m["quality_price_pct"] = map[string]any{"normal": 100, "good": 120, "rare": 150, "legendary": 5000}
		}),
		mod(func(m map[string]any) {
			m["mutation_bp"] = map[string]any{"giant": 2000, "golden": 2000, "fast": 2000, "variant": 0}
		}),
		mod(func(m map[string]any) { m["giant_multiplier"] = 0 }),
		mod(func(m map[string]any) { m["fast_reduction_seconds"] = -1 }),
		mod(func(m map[string]any) { m["level_bonus_max_bp"] = 7500 }),
		mod(func(m map[string]any) {
			m["quality_bp"] = map[string]any{"normal": 300, "good": 7500, "rare": 2000, "legendary": 200}
		}),
		mod(func(m map[string]any) { m["golden_price_pct"] = 2.5 }),
	}
	for i, bad := range bads {
		if _, err := ValidateSettings(bad); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("case %d: expected validation error, got %v", i, err)
		}
	}
	_, err := ValidateSettings(bads[0])
	if err.Error() != "設定無效：品質機率總和必須為 100%（目前 99%）。" {
		t.Fatal(err)
	}
}

func TestLevelBonusCappedAndSumsTo100(t *testing.T) {
	s := defaults(t)
	if !reflect.DeepEqual(EffectiveQualityBP(s, 1, 0), s.QualityBP) {
		t.Fatal("level 1")
	}
	for _, level := range []int{2, 5, 11, 50} {
		bp := EffectiveQualityBP(s, level, 0)
		if sumValues(bp) != 10_000 || bp["normal"] < s.QualityBP["normal"]-s.LevelBonusMaxBP {
			t.Fatal(level, bp)
		}
	}
	if !reflect.DeepEqual(EffectiveQualityBP(s, 50, 0), EffectiveQualityBP(s, 11, 0)) {
		t.Fatal("cap")
	}
	if EffectiveQualityBP(s, 11, 0)["normal"] != 6500 {
		t.Fatal("6500")
	}
}

func TestRollsAndFormulas(t *testing.T) {
	s := defaults(t)
	var qs []string
	for _, u := range []float64{0, 0.6999, 0.7, 0.9, 0.98, 0.9999} {
		qs = append(qs, RollQuality(s.QualityBP, u))
	}
	if !reflect.DeepEqual(qs, []string{"normal", "normal", "good", "rare", "legendary", "legendary"}) {
		t.Fatal(qs)
	}
	var ms []string
	for _, u := range []float64{0, 0.9199, 0.92, 0.95, 0.96, 0.99} {
		ms = append(ms, RollMutation(s, u, 0))
	}
	if !reflect.DeepEqual(ms, []string{"none", "none", "giant", "golden", "fast", "variant"}) {
		t.Fatal(ms)
	}
	checks := []struct{ got, want int }{
		{UnitPrice(15, "rare", "none", s), 22},
		{UnitPrice(15, "legendary", "golden", s), 90},
		{UnitPrice(15, "normal", "giant", s), 15},
		{UnitPrice(15, "normal", "fast", s), 15},
		{UnitPrice(25, "good", "variant", s), 120},
		{HarvestQuantity(2, "giant", s, 0), 4},
		{HarvestQuantity(2, "golden", s, 0), 2},
		{GrowSeconds(7200, "fast", false, s, 0), balance.MinGrowSeconds},
		{GrowSeconds(10_000, "fast", false, s, 0), 2800},
		{GrowSeconds(7200, "fast", true, s, 0), balance.MinGrowSeconds},
		{GrowSeconds(600, "none", true, s, 0), 510},
	}
	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("check %d: got %d want %d", i, c.got, c.want)
		}
	}
}

// 移植自 tests/test_admin.py::test_genetics_event_formulas
func TestGeneticsEventFormulas(t *testing.T) {
	s := balance.DefaultGenetics()
	bp := EffectiveMutationBP(s, 800)
	if sumValues(bp) != sumValues(s.MutationBP)+800 {
		t.Fatal(bp)
	}
	zero := s
	zero.MutationBP = map[string]int{"giant": 0, "golden": 0, "fast": 0, "variant": 0}
	if EffectiveMutationBP(zero, 800)["giant"] != 0 {
		t.Fatal("zero")
	}
	q := EffectiveQualityBP(s, 1, 3000)
	if sumValues(q) != 10_000 || q["normal"] != 4000 {
		t.Fatal(q)
	}
	if EffectiveQualityBP(s, 1, 99_999)["normal"] != 0 {
		t.Fatal("negative")
	}
	checks := []struct{ got, want int }{
		{HarvestQuantity(3, "giant", s, 50), 9},
		{GrowSeconds(7200, "none", false, s, 50), 3600},
		{GrowSeconds(7200, "fast", false, s, 50), balance.MinGrowSeconds},
		{GrowSeconds(60, "none", true, s, 50), 51},
	}
	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("check %d: got %d want %d", i, c.got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	if ParseQuality(" Rare ") != "rare" || ParseQuality("稀有") != "rare" || ParseQuality("x") != "" {
		t.Fatal("quality")
	}
	if ParseMutation("黃金") != "golden" || ParseMutation("黃金作物") != "golden" || ParseMutation("GIANT") != "giant" {
		t.Fatal("mutation")
	}
}
