package mushroom

import (
	"encoding/json"
	"testing"
	"time"

	"farmgm/internal/game/featurecfg"
)

func cfgValue(t *testing.T) featurecfg.Value {
	t.Helper()
	data, _ := json.Marshal(featurecfg.Defaults(ConfigKey))
	v, err := featurecfg.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMushroomConfig(t *testing.T) {
	featurecfg.Reset()
	t.Cleanup(featurecfg.Reset)
	if _, err := featurecfg.Validate(ConfigKey, cfgValue(t)); err != nil {
		t.Fatalf("預設值驗證失敗：%v", err)
	}
	v := cfgValue(t)
	v["grow_seconds"] = 3600
	v["yield_per_bed"] = 3
	v["unlock_level"] = 2
	v["rare_pct"] = []any{100, 100, 100, 100, 100}
	v["full_guarantee"] = false
	v["nutrient_crops"] = map[string]any{"grain": []any{}, "veggie": []any{"wheat"}, "fruit": []any{}}
	norm, err := featurecfg.Validate(ConfigKey, v)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, norm)
	if Unlock() != 2 || Yield() != 3 || GrowHoursText() != "1 小時" {
		t.Fatalf("設定沒有生效：%d %d %s", Unlock(), Yield(), GrowHoursText())
	}
	if NutrientOf("wheat") != "veggie" || NutrientOf("strawberry") != DefaultNutrient {
		t.Fatal("養分對應沒有套用設定")
	}
	got := Roll(Mix([]string{"wheat"}), 0, Yield(), func() float64 { return 0.5 })
	if len(got) != 3 || got[0] != "truffle" {
		t.Fatalf("擲骰結果不符合設定：%v", got)
	}
	if GuaranteedRare(MaxSawdust, 2) != 0 {
		t.Fatal("關閉保證後不應保證稀有")
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !(Bed{StartedAt: start}).Ready(start.Add(time.Hour)) {
		t.Fatal("生長時間沒有套用設定")
	}
	featurecfg.SetCurrent(ConfigKey, nil)
	if Unlock() != UnlockLevel || NutrientOf("wheat") != "grain" {
		t.Fatal("還原預設值後沒有回到預設")
	}
	dup := cfgValue(t)
	dup["nutrient_crops"] = map[string]any{"grain": []any{"wheat"}, "veggie": []any{"wheat"}, "fruit": []any{}}
	if _, err := featurecfg.Validate(ConfigKey, dup); err == nil {
		t.Fatal("同一作物出現在兩種養分應該驗證失敗")
	}
}
