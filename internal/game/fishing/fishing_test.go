package fishing

// 對應 Python tests/test_fishing_rules.py：釣魚純規則與模擬（稀有度機率、各稀有度的整體成功率、
// 裝備對難度的影響、價格與設定驗證）。tests/test_fishing.py、test_fishing_guard.py 全部需要資料庫，留待服務層移植。
// Python 用 random.Random(seed)，Go 用 math/rand/v2 的 PCG；亂數序列不同，但門檻是統計性的，應同樣成立。

import (
	"hash/fnv"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
)

var (
	starterRod  = Effects{"reel_power": 10, "control_pct": 0, "reaction_bonus_ms": 0}
	starterLine = Effects{"max_tension": 100}
	midRod      = Effects{"reel_power": 13, "control_pct": 10, "reaction_bonus_ms": 1000}
	midLine     = Effects{"max_tension": 115}
	bestRod     = Effects{"reel_power": 18, "control_pct": 25, "reaction_bonus_ms": 2000}
	bestLine    = Effects{"max_tension": 130}
)

type gear struct {
	name      string
	rod, line Effects
}

var gears = []gear{{"starter", starterRod, starterLine}, {"mid", midRod, midLine}, {"best", bestRod, bestLine}}

func seeded(seed string) RNG {
	h := fnv.New64a()
	h.Write([]byte(seed))
	r := rand.New(rand.NewPCG(h.Sum64(), 0x9e3779b97f4a7c15))
	return r.Float64
}

func noFate() Settings {
	s := DefaultSettings()
	s.BaseSuccessPct = 100
	return s
}

func trials(n int) int {
	if testing.Short() {
		return max(200, n/5)
	}
	return n
}

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestRarityDistributionMatchesSettings(t *testing.T) {
	s := DefaultSettings()
	rng := seeded("1")
	n := 200_000
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		counts[RollRarity(s.RarityBP, Effects{}, rng)]++
	}
	for r, bp := range s.RarityBP {
		if got := float64(counts[r]) / float64(n); !approx(got, float64(bp)/10000, 0.004) {
			t.Errorf("%s: %v", r, got)
		}
	}
}

func TestBaitShiftsRarityFromCommon(t *testing.T) {
	c := RarityChances(DefaultSettings().RarityBP, Effects{"rare_bp": 500, "epic_bp": 300, "legendary_bp": 150})
	if !approx(c["common"], 70.5, 1e-9) || !approx(c["legendary"], 2.5, 1e-9) {
		t.Fatal(c)
	}
	sum := 0.0
	for _, v := range c {
		sum += v
	}
	if !approx(sum, 100, 1e-9) {
		t.Fatal(sum)
	}
}

func TestSensiblePlaySuccessIsCloseToBaseRateForEveryRarity(t *testing.T) {
	s := DefaultSettings()
	for _, r := range Rarities {
		for _, g := range gears {
			rate, turns := SuccessRate(s, r, g.rod, g.line, SmartAction, seeded(r+"-"+g.name), trials(4000), 3)
			tol := 0.03
			if testing.Short() {
				tol = 0.05
			}
			if !approx(rate, 0.90, tol) || turns < 2 || turns > 25 {
				t.Errorf("%s/%s: rate %v turns %v", r, g.name, rate, turns)
			}
		}
	}
}

func TestFightItselfIsWinnableWithStarterGear(t *testing.T) {
	for _, r := range Rarities {
		rate, _ := SuccessRate(noFate(), r, starterRod, starterLine, SmartAction, seeded(r), trials(3000), 3)
		if rate < 0.97 {
			t.Errorf("%s: %v", r, rate)
		}
	}
}

func TestPullsNeededFollowRarity(t *testing.T) {
	// 依提示操作時：普通 2–4 下、稀有 3–5、史詩 4–6、傳說 5–7；好裝備幾乎不用多等。
	for _, r := range Rarities {
		low, high := Pulls[r][0], Pulls[r][1]
		rng := seeded("pulls-" + r)
		needs := map[int]bool{}
		for range 300 {
			needs[NewFight(noFate(), r, 3, bestLine, rng).Need] = true
		}
		for n := low; n <= high; n++ {
			if !needs[n] {
				t.Errorf("%s: need %d never rolled (%v)", r, n, needs)
			}
		}
		if len(needs) != high-low+1 {
			t.Errorf("%s: needs %v", r, needs)
		}
		n := trials(2000)
		within := 0
		for range n {
			result, turns := Simulate(noFate(), r, bestRod, bestLine, SmartAction, rng, 3, 200)
			if result != "landed" || turns < low {
				t.Fatalf("%s: %s after %d turns", r, result, turns)
			}
			if turns <= high {
				within++
			}
		}
		if float64(within)/float64(n) < 0.85 {
			t.Errorf("%s: only %d/%d within %d turns", r, within, n, high)
		}
		// Python 兩次共用同一個亂數序列；普通魚兩種裝備的平均次數幾乎相同，Go 的亂數序列不同時比較結果取決於運氣，
		// 所以兩次都從同一個種子開始（相同的魚與事件），比較的才是裝備的差異
		_, starter := SuccessRate(noFate(), r, starterRod, starterLine, SmartAction, seeded("turns-"+r), trials(1000), 3)
		_, best := SuccessRate(noFate(), r, bestRod, bestLine, SmartAction, seeded("turns-"+r), trials(1000), 3)
		if !(best <= starter && starter <= float64(high+4)) { // 入門裝備要多等幾下，但不會拖太久
			t.Errorf("%s: best %v starter %v", r, best, starter)
		}
	}
}

func TestScriptsWithoutReadingTheScreenNeverLand(t *testing.T) {
	// 只按收線（或固定節奏、不看方向）一定釣不到：每場都有魚竄向一邊，只有壓同方向才過得去。
	policies := map[string]Policy{"reel_only": ReelOnlyAction, "alternate": AlternateAction, "naive": NaiveAction}
	for _, r := range Rarities {
		for name, p := range policies {
			if rate, _ := SuccessRate(noFate(), r, bestRod, bestLine, p, seeded("spam-"+r+"-"+name), trials(1000), 3); rate != 0 {
				t.Errorf("%s/%s: %v", r, name, rate)
			}
		}
	}
}

func TestEveryFightHasForcedDodgesWithinItsPulls(t *testing.T) {
	rng := seeded("dodges")
	for _, r := range Rarities {
		for range 200 {
			s := NewFight(DefaultSettings(), r, 3, starterLine, rng)
			if len(s.DodgeAt) != ForcedDodges[r] {
				t.Fatalf("%s: dodge_at %v", r, s.DodgeAt)
			}
			for _, x := range s.DodgeAt {
				if x < 0 || x >= s.Need {
					t.Fatalf("%s: dodge_at %v need %d", r, s.DodgeAt, s.Need)
				}
			}
			if (s.Dodge != nil) != slices.Contains(s.DodgeAt, 0) {
				t.Fatalf("%s: dodge %v dodge_at %v", r, s.Dodge, s.DodgeAt)
			}
		}
	}
}

// fightState 對應 Python fight_state：need 10、沒有竄動。
func fightState(mod func(*State)) State {
	s := State{Stamina: 40, MaxStamina: 40, Distance: 90, StartDistance: 90, Tension: 0, MaxTension: 100, Turn: 0,
		Intent: "calm", Need: 10, Progress: 0, DodgeAt: []int{}, Slack: 0}
	if mod != nil {
		mod(&s)
	}
	return s
}

func noDodge() float64 { return 0.99 } // 固定亂數

func commonDiff() Difficulty { return DefaultSettings().Difficulty["common"] }

func mustStep(t *testing.T, s State, action string, diff Difficulty, rod Effects, rng RNG) (State, string) {
	t.Helper()
	next, result, err := Step(s, action, diff, rod, rng)
	if err != nil {
		t.Fatal(err)
	}
	return next, result
}

func TestConsecutiveReelsRaiseTensionAndCountAsPulls(t *testing.T) {
	state := fightState(nil)
	var added []float64
	for i := 0; i < 3; i++ {
		before := state.Tension
		var result string
		state, result = mustStep(t, state, "reel", commonDiff(), starterRod, noDodge)
		if result != "" || state.Progress != i+1 {
			t.Fatal(result, state.Progress)
		}
		added = append(added, state.Tension-before+6) // 魚平靜時張力 -6
	}
	if !slices.Equal(added, []float64{14, 18, 22}) { // 每多連續一次多 4（最大張力 100 的 4%）
		t.Fatal(added)
	}
	strong := fightState(func(s *State) { s.MaxTension = 130; s.ReelStreak = 2 })
	if !approx(ReelTension(strong), 14+2*130*0.04, 1e-9) {
		t.Fatal(ReelTension(strong))
	}
	if state.Stamina != 28 || state.Distance != 63 { // 跟著進度下降
		t.Fatal(state.Stamina, state.Distance)
	}
}

func TestWaitAndReleaseCoolDownWithoutProgress(t *testing.T) {
	waited, _ := mustStep(t, fightState(func(s *State) { s.ReelStreak = 3; s.Tension = 60; s.Intent = "strong" }),
		"wait", commonDiff(), starterRod, noDodge)
	if waited.ReelStreak != 1 || waited.Tension != 60+WaitTension || waited.Progress != 0 {
		t.Fatalf("%+v", waited)
	}
	released, _ := mustStep(t, fightState(func(s *State) { s.ReelStreak = 2; s.Tension = 60 }), "release", commonDiff(), starterRod, noDodge)
	if released.ReelStreak != 0 || released.Tension != 30 || released.Distance != 96 {
		t.Fatalf("%+v", released)
	}
}

func TestLastPullLandsButNotWhenItSnapsTheLine(t *testing.T) {
	s := fightState(func(s *State) { s.Progress = 9; s.Tension = 90; s.ReelStreak = 3 })
	if _, result := mustStep(t, s, "reel", commonDiff(), bestRod, noDodge); result != "line_broken" {
		t.Fatal(result)
	}
	landed, result := mustStep(t, fightState(func(s *State) { s.Progress = 9; s.Tension = 20 }), "reel", commonDiff(), bestRod, noDodge)
	if result != "landed" || landed.Distance != 0 {
		t.Fatal(result, landed.Distance)
	}
}

func TestDodgeMustBeMatchedAndKeepsComingBack(t *testing.T) {
	base := fightState(func(s *State) { s.Tension = 40; s.Dodge = strPtr("left"); s.DodgeAt = []int{0} })
	hit, result := mustStep(t, base, "left", commonDiff(), starterRod, noDodge)
	if result != "" || hit.Tension != 25 || hit.Progress != 1 || hit.Dodge != nil || len(hit.DodgeAt) != 0 {
		t.Fatalf("%+v %s", hit, result)
	}
	for _, action := range []string{"right", "reel", "wait"} {
		missed, _ := mustStep(t, base, action, commonDiff(), starterRod, noDodge)
		if missed.Progress != 0 || !slices.Contains(SteerActions, missed.DodgeDir()) { // 魚繼續竄
			t.Fatalf("%s: %+v", action, missed)
		}
		if !strings.Contains(missed.Last.Text, "沒壓對方向") {
			t.Fatal(missed.Last.Text)
		}
	}
	reeled, _ := mustStep(t, base, "reel", commonDiff(), starterRod, noDodge)
	if reeled.Tension != 40+14+DodgeMiss.Tension || reeled.Distance != 98 {
		t.Fatal(reeled.Tension, reeled.Distance)
	}
	idle, _ := mustStep(t, fightState(func(s *State) { s.Tension = 40 }), "left", commonDiff(), starterRod, noDodge)
	if idle.Tension != 40+SteerIdleTension-6 || idle.Progress != 0 {
		t.Fatal(idle.Tension, idle.Progress)
	}
	// 進度到 dodge_at 時魚竄出來
	before, _ := mustStep(t, fightState(func(s *State) { s.DodgeAt = []int{1} }), "reel", commonDiff(), starterRod, noDodge)
	if before.Progress != 1 || !slices.Contains(SteerActions, before.DodgeDir()) {
		t.Fatalf("%+v", before)
	}
	if base.DodgeDir() != "left" || len(base.DodgeAt) != 1 {
		t.Fatal("Step must not mutate the input state")
	}
}

func TestOldSessionsWithoutNewFieldsStillWork(t *testing.T) {
	// 升級時進行中的釣魚：舊 state 沒有 need 等欄位，再成功拉 2 下就上岸。
	raw := `{"stamina": 40, "max_stamina": 40, "distance": 90, "tension": 0, "max_tension": 100, "turn": 0,
		"slip_turn": null, "intent": "calm", "last": null}`
	var s State
	if err := jsonUnmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	next, result := mustStep(t, s, "reel", commonDiff(), starterRod, noDodge)
	if result != "" || next.ReelStreak != 1 || next.Progress != 1 {
		t.Fatal(result, next.ReelStreak, next.Progress)
	}
	if _, result = mustStep(t, next, "reel", commonDiff(), starterRod, noDodge); result != "landed" {
		t.Fatal(result)
	}
}

func TestStateJSONRoundTripMatchesPythonShape(t *testing.T) {
	s := fightState(func(s *State) { s.Tension = 27.5 })
	data, err := jsonMarshal(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`"stamina":40`, `"tension":27.5`, `"slip_turn":null`, `"dodge":null`, `"last":null`,
		`"reel_streak":0`, `"start_distance":90`, `"need":10`, `"progress":0`, `"dodge_at":[]`, `"slack":0`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "gaps") {
		t.Errorf("gaps should be omitted: %s", got)
	}
	// 舊 Session 經 Step 寫回時 dodge_at 是 []（Python setdefault），不是 null
	var old State
	if err := jsonUnmarshal([]byte(`{"stamina": 40, "max_stamina": 40, "distance": 90, "tension": 0, "max_tension": 100}`), &old); err != nil {
		t.Fatal(err)
	}
	next, _ := mustStep(t, old, "wait", commonDiff(), starterRod, noDodge)
	if data, _ := jsonMarshal(next); !strings.Contains(string(data), `"dodge_at":[]`) || !strings.Contains(string(data), `"need":2`) {
		t.Errorf("legacy upgrade: %s", data)
	}
}

func TestRegularRhythmDetection(t *testing.T) {
	if RegularRhythm([]float64{1, 1, 1, 1, 1}) { // 樣本不足
		t.Fatal("too few samples")
	}
	if !RegularRhythm([]float64{1.0, 1.02, 0.98, 1.01, 0.99, 1.0}) {
		t.Fatal("should be regular")
	}
	if RegularRhythm([]float64{0.9, 2.5, 1.2, 4.0, 1.5, 3.1}) {
		t.Fatal("should not be regular")
	}
}

func TestGuardLockDoublesAndIsCapped(t *testing.T) {
	want := []int{5, 10, 20, GuardLockMaxMinutes}
	for i, n := range []int{1, 2, 3, 9} {
		if got := GuardLockMinutesFor(n); got != want[i] {
			t.Errorf("%d: %d", n, got)
		}
	}
	if GuardLockMinutesFor(0) != 5 {
		t.Fatal(GuardLockMinutesFor(0))
	}
}

func TestNewGuardChallenge(t *testing.T) {
	c := NewGuardChallenge(func(seq []string, n int) []string { return seq[:n] }, func(s []string) {
		s[0], s[len(s)-1] = s[len(s)-1], s[0]
	})
	if c.Answer != "puffer" || len(c.Options) != GuardOptions || c.Options[len(c.Options)-1] != "puffer" {
		t.Fatalf("%+v", c)
	}
}

func TestEstimatedPlayTimeIs20To60Seconds(t *testing.T) {
	s := DefaultSettings()
	for _, r := range Rarities {
		for _, g := range []gear{gears[0], gears[2]} {
			_, turns := SuccessRate(s, r, g.rod, g.line, SmartAction, seeded("5"), trials(1000), 3)
			if est := EstimatedSeconds(s, turns, 2.5); est < 15 || est > 60 {
				t.Errorf("%s/%s: %v", r, g.name, est)
			}
		}
	}
}

func TestPriceAndWeightStayInFishRange(t *testing.T) {
	rng := seeded("3")
	for _, f := range InitialFish {
		if !(0 <= f.MinPrice && f.MinPrice <= f.MaxPrice) {
			t.Fatal(f.Code)
		}
		for i := 0; i < 200; i++ {
			w := RollWeight(f.MinWeightG, f.MaxWeightG, rng)
			if w < f.MinWeightG || w > f.MaxWeightG {
				t.Fatal(f.Code, w)
			}
			if p := FishPrice(f.MinPrice, f.MaxPrice, f.MinWeightG, f.MaxWeightG, w); p < f.MinPrice || p > f.MaxPrice {
				t.Fatal(f.Code, p)
			}
		}
	}
}

func fishRaw(mod map[string]any) map[string]any {
	base := map[string]any{"rarity": "common", "min_price": 1, "max_price": 20, "min_weight_g": 10,
		"max_weight_g": 100, "difficulty": 3}
	for k, v := range mod {
		base[k] = v
	}
	return base
}

func TestValidateFishPriceIsNotBoundToRarity(t *testing.T) {
	if v, err := ValidateFish(fishRaw(nil)); err != nil || v.MaxPrice != 20 {
		t.Fatal(v, err)
	}
	if v, err := ValidateFish(fishRaw(map[string]any{"min_price": 0, "max_price": 500_000})); err != nil || v.MaxPrice != 500_000 {
		t.Fatal(v, err)
	}
	if v, err := ValidateFish(fishRaw(map[string]any{"rarity": "legendary"})); err != nil || v.Rarity != "legendary" {
		t.Fatal(v, err) // 1–20 的傳說魚也可以
	}
	for _, bad := range []map[string]any{{"min_price": 30}, {"min_price": -1}, {"max_price": PriceMax + 1}} {
		if _, err := ValidateFish(fishRaw(bad)); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestPriceSpanPerRarity(t *testing.T) {
	fish := []PriceRow{{"rare", 5, 9}, {"rare", 3, 400}}
	if lo, hi, ok := PriceSpan(fish, "rare"); !ok || lo != 3 || hi != 400 {
		t.Fatal(lo, hi, ok)
	}
	if _, _, ok := PriceSpan(fish, "epic"); ok {
		t.Fatal("epic should be empty")
	}
}

func TestValidateSettingsAndGearEffects(t *testing.T) {
	s := DefaultSettings()
	v, err := ValidateSettings(s.Raw())
	if err != nil || v.BaseSuccessPct != 90 {
		t.Fatal(v, err)
	}
	bad := s.Raw()
	bad["bite_wait_min_seconds"], bad["bite_wait_max_seconds"] = 20, 10
	if _, err := ValidateSettings(bad); !gameerr.Is(err, gameerr.Validation) {
		t.Fatal(err)
	}
	s2 := DefaultSettings()
	epic := s2.Difficulty["epic"]
	epic.SurgePct = 90
	s2.Difficulty["epic"] = epic
	if _, err := ValidateSettings(s2.Raw()); err == nil || !strings.Contains(err.Error(), "合計") {
		t.Fatal(err)
	}
	if _, err := ValidateGearEffects("rod", map[string]any{"reel_power": 99, "control_pct": 0, "reaction_bonus_ms": 0}); err == nil {
		t.Fatal("reel_power 99 should fail")
	}
	if _, err := ValidateGearEffects("line", map[string]any{"max_tension": 100, "reel_power": 10}); err == nil {
		t.Fatal("unknown field should fail")
	}
	rarity := s.Raw()
	rarity["rarity_bp"] = map[string]any{"common": 5000, "rare": 1500, "epic": 400, "legendary": 100}
	if _, err := ValidateSettings(rarity); err == nil || !strings.Contains(err.Error(), "目前 70%") {
		t.Fatal(err)
	}
}

func TestStepOutcomes(t *testing.T) {
	diff, rod := commonDiff(), starterRod
	state := fightState(func(s *State) { s.Progress = 9; s.Tension = 20; s.Distance = 5 })
	landed, result := mustStep(t, state, "reel", diff, rod, seeded("1"))
	if result != "landed" || landed.Distance != 0 {
		t.Fatal(result, landed.Distance)
	}
	tight := fightState(func(s *State) { s.Tension = 95; s.Intent = "strong" })
	if _, result = mustStep(t, tight, "reel", diff, rod, seeded("1")); result != "line_broken" {
		t.Fatal(result)
	}
	loose := tight
	loose.Tension = 60
	if _, result = mustStep(t, loose, "release", diff, rod, seeded("1")); result != "" {
		t.Fatal(result) // 衝刺前放線就不會斷
	}
	far := fightState(func(s *State) { s.Slack = EscapeDistance - 5 })
	if _, result = mustStep(t, far, "release", diff, rod, seeded("1")); result != "escaped" {
		t.Fatal(result)
	}
	one := 1
	slip := fightState(func(s *State) { s.SlipTurn = &one })
	if _, result = mustStep(t, slip, "reel", diff, rod, seeded("1")); result != "slipped" {
		t.Fatal(result)
	}
	if _, _, err := Step(state, "jump", diff, rod, seeded("1")); !gameerr.Is(err, gameerr.Validation) {
		t.Fatal(err)
	}
}

func TestPickFishFallsBackToLowerRarity(t *testing.T) {
	pool := map[string][]string{"common": {"a"}, "legendary": {"z"}}
	if f, ok := PickFish(pool, "epic", seeded("x")); !ok || f != "a" {
		t.Fatal(f, ok)
	}
	if f, ok := PickFish(map[string][]string{"legendary": {"z"}}, "rare", seeded("x")); !ok || f != "z" {
		t.Fatal(f, ok)
	}
	if !BaitAllows(nil, "") || BaitAllows(FishRequiredBaits["coelacanth"], "deep_sea_bait") || !BaitAllows(FishRequiredBaits["coelacanth"], "dragon_bait") {
		t.Fatal("bait allows")
	}
}

func TestGearEffectsText(t *testing.T) {
	if got := GearEffectsText("rod", Effects{"reel_power": 13, "control_pct": 10, "reaction_bonus_ms": 1500}); got != "收線力 13、魚的衝擊張力 -10%、拉竿時間 +1.5 秒" {
		t.Fatal(got)
	}
	if got := GearEffectsText("bait", Effects{"rare_bp": 300, "epic_bp": 0, "legendary_bp": 30, "bite_speed_pct": 20}); got != "🔵稀有 +3%、🟡傳說 +0.3%、上鉤快 20%" {
		t.Fatal(got)
	}
	if GearEffectsText("bait", Effects{}) != "一般魚餌，沒有額外效果" || GearEffectsText("line", Effects{}) != "最大張力 100" {
		t.Fatal("defaults")
	}
	if WeightText(1234) != "1.23 kg" || WeightText(999) != "999 g" {
		t.Fatal(WeightText(1234))
	}
}
