package ranking

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

func tp(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, clock.TaipeiTZ) }

func bounds(t *testing.T, period string, at time.Time) (time.Time, time.Time) {
	t.Helper()
	s, e, err := PeriodBounds(period, at)
	if err != nil {
		t.Fatal(err)
	}
	return s, e
}

func TestPeriodBoundsTaipei(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // 台灣 1/1（四）08:00
	if s, e := bounds(t, "daily", at); !s.Equal(tp(2026, 1, 1)) || !e.Equal(tp(2026, 1, 2)) {
		t.Fatal(s, e)
	}
	if s, e := bounds(t, "weekly", at); !s.Equal(tp(2025, 12, 29)) || !e.Equal(tp(2026, 1, 5)) {
		t.Fatal(s, e)
	}
	// 台灣已是 1/1 04:00
	if s, e := bounds(t, "monthly", time.Date(2025, 12, 31, 20, 0, 0, 0, time.UTC)); !s.Equal(tp(2026, 1, 1)) || !e.Equal(tp(2026, 2, 1)) {
		t.Fatal(s, e)
	}
	if _, e := bounds(t, "monthly", time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)); !e.Equal(tp(2026, 1, 1)) {
		t.Fatal(e)
	}
	if _, _, err := PeriodBounds("yearly", at); err == nil {
		t.Fatal("expected error")
	}
}

func rule(overrides map[string]any) map[string]any {
	r := map[string]any{
		"name": "每週群組金幣", "scope": "group", "metric": "money", "period": "weekly",
		"rewards": []any{map[string]any{"reward_type": "gold", "amount": 100}},
	}
	for k, v := range overrides {
		r[k] = v
	}
	return r
}

func expectErr(t *testing.T, raw map[string]any, want string) {
	t.Helper()
	_, err := ValidateRule(raw)
	if !gameerr.Is(err, gameerr.Validation) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
}

func TestRuleValidation(t *testing.T) {
	expectErr(t, rule(map[string]any{"rewards": []any{}}), "獎勵名次")
	expectErr(t, rule(map[string]any{"rewards": []any{map[string]any{"reward_type": "fertilizer", "amount": 1000}}}), "第 1 名的獎勵數量")
	expectErr(t, rule(map[string]any{"period": "yearly"}), "結算週期")
	many := make([]any, balance.RankingMaxRanks)
	for i := range many {
		many[i] = map[string]any{"reward_type": "gold", "amount": 1}
	}
	ok, err := ValidateRule(rule(map[string]any{"rewards": many}))
	if err != nil || len(ok.Rewards) != balance.RankingMaxRanks || !ok.Enabled {
		t.Fatal(ok, err)
	}
	if r, _ := ValidateRule(rule(map[string]any{"enabled": false})); r.Enabled {
		t.Fatal("enabled should be false")
	}
}

func TestPeriodLabelAndRewardText(t *testing.T) {
	if got := PeriodLabel("weekly", tp(2025, 12, 29)); got != "2025-12-29～01-04（週）" {
		t.Fatal(got)
	}
	if got := PeriodLabel("monthly", tp(2026, 1, 1)); got != "2026-01（月）" {
		t.Fatal(got)
	}
	if RewardText(Reward{"gold", 5}) != "5 金幣" || RewardText(Reward{"fertilizer", 2}) != "2 個化肥" {
		t.Fatal(RewardText(Reward{"fertilizer", 2}))
	}
}
