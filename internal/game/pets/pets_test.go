package pets

import (
	"testing"

	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
)

func ip(v int) *int { return &v }

func TestRules(t *testing.T) {
	featurecfg.Reset()
	want := []int{0, 5, 10, 15, 20, 25}
	for i, lv := range []*int{nil, ip(0), ip(1), ip(2), ip(3), ip(4)} {
		if BonusPct(lv) != want[i] {
			t.Fatalf("bonus %d", i)
		}
	}
	for lv, w := range []int{10_000, 20_000, 30_000, 40_000} {
		if p, ok := UpgradePrice(lv); !ok || p != w {
			t.Fatal(lv)
		}
	}
	if _, ok := UpgradePrice(4); ok {
		t.Fatal("max level")
	}
	if BonusQuantity(5, 10, 0.49) != 1 || BonusQuantity(5, 10, 0.5) != 0 { // 0.5 個 → 50% 多 1 個
		t.Fatal("bonus quantity 0.5")
	}
	if BonusQuantity(20, 20, 0.99) != 4 || BonusQuantity(3, 0, 0.0) != 0 {
		t.Fatal("bonus quantity")
	}
	if BlockerName(DogBlocker) != "狗狗" || BlockerName("scarecrow") != "稻草人" {
		t.Fatal("blocker")
	}
}

func TestOddsAndEffectText(t *testing.T) {
	featurecfg.Reset()
	cases := map[[2]int]string{
		{2, 25}: "50% 機率 +1",
		{5, 25}: "固定 +1，另有 25% 機率再 +1",
		{4, 25}: "固定 +1",
		{0, 25}: "不會多",
		{3, 5}:  "15% 機率 +1",
		{1, 15}: "15% 機率 +1",
	}
	for in, want := range cases {
		if got := OddsText(in[0], in[1]); got != want {
			t.Errorf("%v: %q", in, got)
		}
	}
	if got := EffectText(Cat, ip(4)); got != "收成數量 +25%" {
		t.Fatal(got)
	}
}

func TestSettings(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	bad, _ := featurecfg.Decode([]byte(`{"buy_price":-1}`))
	featurecfg.Apply(map[string]featurecfg.Value{ConfigKey: bad}) // 不合法：退回預設值
	if BuyPrice() != 5_000 {
		t.Fatal(BuyPrice())
	}
	over, _ := featurecfg.Decode([]byte(`{"buy_price":1,"upgrade_prices":[1,2,3,4,5,6,7,8,9,10],"base_pct":5,"pct_per_level":10}`))
	if _, err := featurecfg.Validate(ConfigKey, over); !gameerr.Is(err, gameerr.Validation) {
		t.Fatal(err)
	}
	ok, _ := featurecfg.Decode([]byte(`{"buy_price":9,"upgrade_prices":[1,2],"base_pct":0,"pct_per_level":50}`))
	featurecfg.Apply(map[string]featurecfg.Value{ConfigKey: ok})
	if BuyPrice() != 9 || MaxLevel() != 2 || BonusPct(ip(5)) != 100 {
		t.Fatal(Config())
	}
}
