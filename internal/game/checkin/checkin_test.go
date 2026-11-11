package checkin

import (
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

func TestDefaultsAndCycle(t *testing.T) {
	featurecfg.Reset()
	if CycleDays() != 7 {
		t.Fatal(CycleDays())
	}
	if RewardFor(1).Gold != 100 || RewardFor(8).Gold != 100 || RewardFor(0).Gold != 100 {
		t.Fatal("cycle")
	}
	if got := RewardText(RewardFor(7)); got != "500 金幣、人工養殖蚯蚓 ×1" {
		t.Fatal(got)
	}
	if got := RewardText(Reward{}); got != "無" {
		t.Fatal(got)
	}
	if ItemGoldValue(balance.ItemFertilizer) != 30 {
		t.Fatal("gold value")
	}
}

func TestNextStreak(t *testing.T) {
	today := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	older := time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)
	if NextStreak(&yesterday, 4, today) != 5 || NextStreak(&older, 4, today) != 1 || NextStreak(nil, 4, today) != 1 {
		t.Fatal("streak")
	}
}

// 對應 tests/test_progress_admin.py 的設定套用（不經資料庫）。
func TestRewardsFollowSettings(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	raw, _ := featurecfg.Decode([]byte(`{"rewards":[{"gold":7,"items":{}},{"gold":0,"items":{"fertilizer":2}}]}`))
	featurecfg.Apply(map[string]featurecfg.Value{ConfigKey: raw})
	if CycleDays() != 2 || RewardFor(3).Gold != 7 || RewardFor(2).Items[balance.ItemFertilizer] != 2 {
		t.Fatal(Rewards())
	}
}

func TestValidateRejects(t *testing.T) {
	featurecfg.Reset()
	for _, js := range []string{`{"rewards":[]}`, `{"rewards":[1]}`, `{"rewards":[{"gold":-1}]}`,
		`{"rewards":[{"gold":1.5}]}`, `{"rewards":[{"items":{"nope":1}}]}`, `{"rewards":[{"items":{"fertilizer":101}}]}`} {
		raw, _ := featurecfg.Decode([]byte(js))
		if _, err := featurecfg.Validate(ConfigKey, raw); !gameerr.Is(err, gameerr.Validation) {
			t.Errorf("%s: %v", js, err)
		}
	}
}
