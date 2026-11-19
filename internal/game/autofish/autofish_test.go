package autofish

import (
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

func TestDefaultsAndBasket(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	c := Current()
	if c.MachinePrice != 100_000 || c.IntervalMinutes != 5 || !c.AutoSellFish || len(c.BasketCapacities) != MaxBasketLevel {
		t.Fatalf("%+v", c)
	}
	if balance.ItemPrice(balance.ToolAutoFisher) != 100_000 || balance.ItemPrice(balance.ToolAutoSeller) != 150_000 {
		t.Fatal("自動釣魚機的價格要讀設定，其他道具不變")
	}
	for _, x := range []struct{ level, want int }{{0, 20}, {1, 40}, {2, 60}, {4, 100}, {9, 200}, {10, 220}, {12, 220}} {
		if got := BasketCapacity(x.level, 20); got != x.want {
			t.Errorf("Lv.%d：%d，應該是 %d", x.level, got, x.want)
		}
	}
	if capacity, price, ok := NextBasket(0); !ok || capacity != 40 || price != 30_000 {
		t.Fatal(capacity, price, ok)
	}
	if capacity, price, ok := NextBasket(4); !ok || capacity != 120 || price != 400_000 {
		t.Fatal(capacity, price, ok)
	}
	if _, _, ok := NextBasket(MaxBasketLevel); ok {
		t.Fatal("滿級")
	}
	raw, _ := featurecfg.DeepCopy(defaults())
	raw["machine_price"] = 250_000
	raw["basket_capacities"] = []any{30}
	raw["basket_prices"] = []any{1000}
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if balance.ItemPrice(balance.ToolAutoFisher) != 250_000 || MaxBasket() != 1 || BasketCapacity(3, 20) != 30 {
		t.Fatal("設定要生效")
	}
	// 加速：預設 5 → 4 → 3 分鐘
	featurecfg.Reset()
	if Interval(0) != 5*time.Minute || Interval(1) != 4*time.Minute || Interval(2) != 3*time.Minute || Interval(9) != 3*time.Minute {
		t.Fatal(Interval(0), Interval(1), Interval(2))
	}
	if m, p, ok := NextSpeed(1); !ok || m != 3 || p != 120_000 {
		t.Fatal(m, p, ok)
	}
	if !Current().ConsumeBait || Current().BaitPerCatch != 1 {
		t.Fatal("預設每釣 1 條消耗 1 個魚餌")
	}
	// 舊快照沒有第二版的設定：用預設值
	old, _ := featurecfg.DeepCopy(defaults())
	delete(old, "speed_intervals")
	delete(old, "speed_prices")
	delete(old, "consume_bait")
	if v, err := featurecfg.Validate(ConfigKey, old); err != nil || v["consume_bait"] != 1 {
		t.Fatal(v, err)
	}
	for _, bad := range []func(featurecfg.Value){
		func(v featurecfg.Value) { v["speed_intervals"] = []any{5} },                 // 沒有比基本間隔短
		func(v featurecfg.Value) { v["speed_intervals"] = []any{4, 4} },              // 沒有一級比一級短
		func(v featurecfg.Value) { v["speed_prices"] = []any{1} },                    // 數量不同
		func(v featurecfg.Value) { v["basket_prices"] = []any{1, 2} },                // 數量不同
		func(v featurecfg.Value) { v["basket_capacities"] = []any{40, 40, 80, 100} }, // 沒有變大
		func(v featurecfg.Value) { v["interval_minutes"] = 0 },
		func(v featurecfg.Value) { v["auto_sell_fish"] = 2 },
	} {
		raw, _ := featurecfg.DeepCopy(defaults())
		bad(raw)
		if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
			t.Errorf("應該拒絕：%v", raw)
		}
	}
}
