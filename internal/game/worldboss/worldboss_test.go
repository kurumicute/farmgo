package worldboss

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCombatStatsCapAndPythonValues(t *testing.T) {
	// 戰鬥補給有上限：超過目標 150% 後不再增加傷害
	stats := Combat(map[string]int{"food": 0, "medical": 0, "combat": 1_000_000_000, "equipment": 0}, DefaultSettings())
	if !approx(stats.AttackMult, 2.5) {
		t.Fatal(stats.AttackMult)
	}
	// 數值取自 Python 版
	s := AbyssDragon().Settings
	st := Combat(map[string]int{"food": 500, "medical": 300, "combat": 1000, "equipment": 100}, s)
	if !approx(st.AttackMult, 2.491071428571429) || !approx(st.Uptime, 0.7374999999999999) || !approx(st.DamagePerHour, 2755.7477678571427) {
		t.Fatal(st)
	}
	if got := InstantDamage(77, s, st.AttackMult); got != 383 {
		t.Fatal(got)
	}
	if PricePoints(25, 10) != 3 || PricePoints(0, 10) != 1 {
		t.Fatal("price points")
	}
}

func TestBar(t *testing.T) {
	for _, c := range []struct {
		v, total float64
		w        int
		want     string
	}{{5, 20, 10, "██░░░░░░░░"}, {1, 8, 10, "█░░░░░░░░░"}, {3, 4, 5, "████░"}, {1, 0, 3, "░░░"}} {
		if got := Bar(c.v, c.total, c.w); got != c.want {
			t.Errorf("Bar(%v,%v,%d) = %s want %s", c.v, c.total, c.w, got, c.want)
		}
	}
}

func TestSimulateKeepsFractionalDamage(t *testing.T) {
	hp, tick := 1000, t0
	for i := 1; i <= 10; i++ { // 每秒讀取一次，每小時 1800 傷害 = 每秒 0.5
		r := Simulate(hp, tick, t0, t0.Add(time.Hour), 1800, t0.Add(time.Duration(i)*time.Second))
		hp, tick = r.HP, r.LastTickAt
	}
	if hp != 995 {
		t.Fatal(hp)
	}
}

func TestSimulateMatchesPython(t *testing.T) {
	r := Simulate(100, t0, t0, t0.Add(3*time.Hour), 777.7, t0.Add(time.Hour))
	want := time.Date(2026, 1, 1, 0, 7, 42, 903433000, time.UTC)
	if r.HP != 0 || r.DefeatedAt == nil || !r.DefeatedAt.Equal(want) || !r.LastTickAt.Equal(want) || r.Expired {
		t.Fatal(r)
	}
	r = Simulate(1000, t0, t0, t0.Add(3*time.Hour), 777.7, t0.Add(4000*time.Second))
	if r.HP != 136 || !r.LastTickAt.Equal(time.Date(2026, 1, 1, 1, 6, 39, 485663000, time.UTC)) || r.DefeatedAt != nil {
		t.Fatal(r)
	}
}

func TestComposeRewardsMinPoints(t *testing.T) {
	r := DefaultRewards()
	r.MinPoints = 30
	got := ComposeRewards(r, true, 29, 1)
	if !reflect.DeepEqual(got, Composed{Items: map[string]int{}, Parts: []string{}}) {
		t.Fatal(got)
	}
	if ComposeRewards(r, true, 30, 1).Gold <= 0 {
		t.Fatal("should get gold")
	}
	old := r
	old.MinPoints = 0 // 0022 以前結束的活動：沒有門檻
	if got := ComposeRewards(old, false, 1, 5).Gold; got != old.Participation.Gold {
		t.Fatal(got)
	}
	v, err := ValidateRewards(map[string]any{}, func(string) bool { return true })
	if err != nil || v.MinPoints != DefaultRewards().MinPoints {
		t.Fatal(v, err)
	}
	if AbyssDragon().Rewards.MinPoints != 30 {
		t.Fatal("abyss min points")
	}
	full := ComposeRewards(r, true, 500, 1)
	if full.Title != "屠龍後勤英雄" || full.Gold != 800+300+1500 || strings.Join(full.Parts, ",") != "勝利獎勵,貢獻達標獎勵,第 1 名獎勵" {
		t.Fatal(full)
	}
}

func TestValidateDefaultsRoundTrip(t *testing.T) {
	s, err := ValidateSettings(map[string]any{}, func(string) (string, bool) { return "x", true })
	if err != nil || !reflect.DeepEqual(s, DefaultSettings()) {
		t.Fatal(s, err)
	}
	r, err := ValidateRewards(map[string]any{}, func(string) bool { return true })
	if err != nil || !reflect.DeepEqual(r, DefaultRewards()) {
		t.Fatal(r, err)
	}
	_, err = ValidateSettings(map[string]any{"effects": map[string]any{"ratio_cap_pct": 50}}, func(string) (string, bool) { return "x", true })
	if !gameerr.Is(err, gameerr.Validation) || err.Error() != "補給加成上限必須介於 100 到 500。" {
		t.Fatal(err)
	}
	_, err = ValidateRewards(map[string]any{"victory": map[string]any{"gold": true}}, func(string) bool { return true })
	if err == nil || err.Error() != "勝利獎勵金幣必須是整數。" {
		t.Fatal(err)
	}
	_, err = ValidateSettings(map[string]any{}, func(code string) (string, bool) { return "", code != "herb" })
	if err == nil || !strings.Contains(err.Error(), "「herb」不是可以捐獻的物品") {
		t.Fatal(err)
	}
}

func TestSlotAfterUsesTaipeiTime(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before := t0 // 台灣 08:00
	at13 := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	jan3 := time.Date(2026, 1, 3, 13, 0, 0, 0, time.UTC)
	if got := SlotAfter(anchor, 21, 0, 2, 180, before, nil); !got.Equal(at13) {
		t.Fatal(got)
	}
	during := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC) // 台灣 22:00，還在 21:00–24:00 之間
	if got := SlotAfter(anchor, 21, 0, 2, 180, during, nil); !got.Equal(at13) {
		t.Fatal(got)
	}
	after := time.Date(2026, 1, 1, 16, 0, 0, 0, time.UTC) // 已結束 → 兩天後
	if got := SlotAfter(anchor, 21, 0, 2, 180, after, nil); !got.Equal(jan3) {
		t.Fatal(got)
	}
	if got := SlotAfter(anchor, 21, 0, 2, 180, before, &at13); !got.Equal(jan3) {
		t.Fatal(got)
	}
	slots := UpcomingSlots(AbyssDragon().ScheduleTimes, before, nil, 3)
	if len(slots) != 3 || slots[1].Sub(slots[0]) != 48*time.Hour {
		t.Fatal(slots)
	}
}

func TestValidateSchedule(t *testing.T) {
	ok := map[string]any{"anchor_date": "2026-11-04", "interval_days": 2, "spawn_hour": 21, "spawn_minute": 0,
		"duration_minutes": 180, "max_hp": 15000}
	s, err := ValidateSchedule(ok)
	if err != nil || s.AnchorDate.Day() != 4 || s.MaxHP != 15000 {
		t.Fatal(s, err)
	}
	ok["anchor_date"] = "bad"
	if _, err := ValidateSchedule(ok); err == nil || err.Error() != "起始日期格式不正確。" {
		t.Fatal(err)
	}
	ok["anchor_date"] = "2026-11-04"
	ok["spawn_hour"] = 24
	if _, err := ValidateSchedule(ok); err == nil || err.Error() != "出現時間（時）必須介於 0 到 23。" {
		t.Fatal(err)
	}
}

func TestDisplayState(t *testing.T) {
	if DisplayState("ended", true, t0, t0) != "defeated" || DisplayState("active", false, t0.Add(time.Hour), t0) != "scheduled" ||
		DisplayState("active", false, t0, t0) != "active" || DisplayState("paused", false, t0, t0) != "paused" {
		t.Fatal("display state")
	}
}

// DefaultItemOrder 必須剛好列出預設設定的所有指定物品（管理網站表單依此順序顯示）。
func TestDefaultItemOrderCoversDefaultItems(t *testing.T) {
	items := DefaultSettings().Items
	seen := map[string]bool{}
	for _, code := range DefaultItemOrder {
		if _, ok := items[code]; !ok || seen[code] {
			t.Fatalf("%s 不在預設設定中或重複", code)
		}
		seen[code] = true
	}
	if len(seen) != len(items) {
		t.Fatalf("順序 %d 個、預設設定 %d 個", len(seen), len(items))
	}
}
