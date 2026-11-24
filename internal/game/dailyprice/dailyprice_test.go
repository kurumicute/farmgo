package dailyprice

import (
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

func TestDefaultsEscalate(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	// 加速沙漏：原價 500，前 5 個原價，之後每個 +10%，最多 300%
	h := balance.ItemHourglass
	for _, x := range []struct{ nth, want int }{{1, 500}, {5, 500}, {6, 550}, {7, 600}, {24, 1450}, {25, 1500}, {40, 1500}} {
		if got := UnitPrice(h, 500, x.nth); got != x.want {
			t.Errorf("第 %d 個：%d，應該是 %d", x.nth, got, x.want)
		}
	}
	if got := Total(h, 500, 3, 4); got != 500+500+550+600 {
		t.Fatalf("已買 3 個再買 4 個：%d", got)
	}
	if got := Next(h, 500, 5); got != 550 {
		t.Fatalf("已買 5 個，下一個：%d", got)
	}
	// 1,100 金：已買 4 個，第 5 個 500、第 6 個 550（合計 1,050）→ 買得起 2 個；1,000 金只買得起 1 個
	if got := Affordable(h, 500, 4, 1_100, 99); got != 2 {
		t.Fatalf("可買數量：%d", got)
	}
	if got := Affordable(h, 500, 4, 1_000, 99); got != 1 {
		t.Fatalf("可買數量：%d", got)
	}
	if got := Affordable(h, 500, 0, 1_000_000, 7); got != 7 {
		t.Fatalf("上限：%d", got)
	}
	// 魚飼料不在清單裡：不加價
	if Applies(balance.ItemFishFeed) || UnitPrice(balance.ItemFishFeed, 3, 1_000) != 3 || FreeOf(balance.ItemFishFeed) != -1 {
		t.Fatal("魚飼料不應該加價")
	}
	if !Applies(balance.ItemFertilizer) || FreeOf(balance.ItemFertilizer) != 20 {
		t.Fatal("化肥前 20 個原價")
	}
}

func TestValidate(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	raw, _ := featurecfg.DeepCopy(defaults())
	raw["free"] = map[string]any{balance.ItemHourglass: 1}
	raw["step_pct"] = 50
	raw["max_pct"] = 200
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if Applies(balance.ItemFertilizer) {
		t.Fatal("清單裡拿掉的道具不再加價")
	}
	if got := UnitPrice(balance.ItemHourglass, 500, 2); got != 750 {
		t.Fatalf("第 2 個：%d", got)
	}
	if got := UnitPrice(balance.ItemHourglass, 500, 10); got != 1000 {
		t.Fatalf("上限 200%%：%d", got)
	}
	for _, bad := range []map[string]any{
		{"step_pct": 10, "max_pct": 50, "free": map[string]any{}},                           // 上限低於原價
		{"step_pct": 10, "max_pct": 300, "free": map[string]any{"auto_fisher": 1}},          // 永久工具
		{"step_pct": 10, "max_pct": 300, "free": map[string]any{balance.ItemHourglass: -1}}, // 負數
		{"step_pct": -1, "max_pct": 300, "free": map[string]any{}},
	} {
		if _, err := featurecfg.Validate(ConfigKey, bad); err == nil {
			t.Errorf("應該拒絕：%v", bad)
		}
	}
}
