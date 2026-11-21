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
		if BonusPct(Cat, lv) != want[i] {
			t.Fatalf("bonus %d", i)
		}
	}
	for lv, w := range []int{10_000, 20_000, 30_000, 40_000} {
		if p, ok := UpgradePrice(Cat, lv); !ok || p != w {
			t.Fatal(lv)
		}
	}
	if _, ok := UpgradePrice(Cat, 4); ok {
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
	if BuyPrice(Cat) != 5_000 {
		t.Fatal(BuyPrice(Cat))
	}
	over, _ := featurecfg.Decode([]byte(`{"buy_price":1,"upgrade_prices":[1,2,3,4,5,6,7,8,9,10],"base_pct":5,"pct_per_level":10}`))
	if _, err := featurecfg.Validate(ConfigKey, over); !gameerr.Is(err, gameerr.Validation) {
		t.Fatal(err)
	}
	ok, _ := featurecfg.Decode([]byte(`{"buy_price":9,"upgrade_prices":[1,2],"base_pct":0,"pct_per_level":50}`))
	featurecfg.Apply(map[string]featurecfg.Value{ConfigKey: ok})
	if BuyPrice(Cat) != 9 || MaxLevel(Dog) != 2 || BonusPct(Bird, ip(5)) != 100 { // 沒有個別設定：每種都用最上層的
		t.Fatal(Config())
	}
	// 2026-11-22 每種寵物分開設定：沒列的欄位用最上層的，沒列的寵物整組用最上層的
	per, _ := featurecfg.Decode([]byte(`{"buy_price":9,"upgrade_prices":[1,2],"base_pct":0,"pct_per_level":50,
		"per_pet":{"dog":{"buy_price":7000,"upgrade_prices":[100,200,300],"base_pct":10,"pct_per_level":10},"hedgehog":{"base_pct":0,"pct_per_level":30}}}`))
	norm, err := featurecfg.Validate(ConfigKey, per)
	if err != nil {
		t.Fatal(err)
	}
	if pm, _ := featurecfg.AsMap(norm["per_pet"]); len(pm) != len(PetOrder) {
		t.Fatal(norm)
	}
	featurecfg.Apply(map[string]featurecfg.Value{ConfigKey: norm})
	if BuyPrice(Dog) != 7000 || MaxLevel(Dog) != 3 || BonusPct(Dog, ip(3)) != 40 || BonusPct(Dog, nil) != 0 {
		t.Fatal(Of(Dog))
	}
	if p, ok := UpgradePrice(Dog, 2); !ok || p != 300 {
		t.Fatal(p)
	}
	if BuyPrice(Cat) != 9 || BonusPct(Cat, ip(1)) != 50 || BuyPrice(Hedgehog) != 9 || BonusPct(Hedgehog, ip(1)) != 30 {
		t.Fatal(Of(Cat), Of(Hedgehog))
	}
	if got := EffectText(Dog, ip(3)); got != "防守設施沒擋住時，再有 40% 機率擋下偷菜（所有農田）" {
		t.Fatal(got)
	}
	// 每種寵物各自檢查 100% 上限；不認識的寵物拒絕
	for _, raw := range []string{`{"per_pet":{"cat":{"base_pct":60,"pct_per_level":20}}}`, `{"per_pet":{"ufo":{}}}`, `{"per_pet":5}`} {
		v, _ := featurecfg.Decode([]byte(raw))
		if _, err := featurecfg.Validate(ConfigKey, v); !gameerr.Is(err, gameerr.Validation) {
			t.Fatal(raw, err)
		}
	}
}
