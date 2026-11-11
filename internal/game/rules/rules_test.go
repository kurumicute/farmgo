package rules

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func TestPlotState(t *testing.T) {
	ready := now.Add(time.Second)
	id := ptr(int64(1))
	cases := []struct {
		got, want PlotState
	}{
		{PlotStateAt(nil, nil, nil, now), PlotEmpty},
		{PlotStateAt(id, nil, nil, now), PlotWaitingWater},
		{PlotStateAt(id, &now, &ready, now), PlotGrowing},
		{PlotStateAt(id, &now, &now, now), PlotReady},
	}
	wither := now.Add(balance.WitherSeconds * time.Second)
	cases = append(cases,
		struct{ got, want PlotState }{PlotStateAt(id, &now, &now, wither.Add(-time.Microsecond)), PlotReady},
		struct{ got, want PlotState }{PlotStateAt(id, &now, &now, wither), PlotWithered},
	)
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %s want %s", i, c.got, c.want)
		}
	}
}

func TestEarthwormMultiplier(t *testing.T) {
	if got := GrowSecondsWithEffects(600, false); got != 600 {
		t.Fatal(got)
	}
	if got := GrowSecondsWithEffects(600, true); got != 510 {
		t.Fatal(got)
	}
}

func TestRemainingSecondsRoundsUpAndNeverNegative(t *testing.T) {
	if got := RemainingSeconds(ptr(now.Add(200*time.Millisecond)), now); got != 1 {
		t.Fatal(got)
	}
	if got := RemainingSeconds(ptr(now.Add(-5*time.Second)), now); got != 0 {
		t.Fatal(got)
	}
}

func TestValidateQuantityRejects(t *testing.T) {
	for _, bad := range []int{0, -1, 10_000} {
		if _, err := ValidateQuantity(bad); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("%d: expected validation error, got %v", bad, err)
		}
	}
}

func TestValidateNameRejects(t *testing.T) {
	for _, bad := range []string{"", "   ", strings.Repeat("x", 33), "a\x00b"} {
		if _, err := ValidateName(bad, "名稱", 32); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("%q: expected validation error, got %v", bad, err)
		}
	}
	if got, err := ValidateName("  中文名稱  ", "名稱", 4); err != nil || got != "中文名稱" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestItemCodes(t *testing.T) {
	if SeedItemCode("wheat") != "seed_wheat" || ProduceItemCode("wheat") != "wheat" {
		t.Fatal("item codes")
	}
}

func TestResolveDefense(t *testing.T) {
	active := []ActiveDefense{{"scarecrow", 0.25}, {"guard_dog", 0.30}, {"fence", 0.20}}
	by, total := ResolveDefense(active, 0.1, balance.DefenseMaxBlockChance)
	if by != "scarecrow" {
		t.Fatal(by)
	}
	// 0.25 + 0.75*0.3 + 0.525*0.2 = 0.58
	if total < 0.5799 || total > 0.5801 {
		t.Fatal(total)
	}
	by, _ = ResolveDefense(active, 0.99, balance.DefenseMaxBlockChance)
	if by != "" {
		t.Fatal(by)
	}
}

func TestFormatDuration(t *testing.T) {
	if FormatDuration(3725) != "1 小時 2 分 5 秒" || FormatDuration(65) != "1 分 5 秒" || FormatDuration(-3) != "0 秒" {
		t.Fatal(FormatDuration(3725))
	}
}
