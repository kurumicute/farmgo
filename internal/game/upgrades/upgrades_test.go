package upgrades

import (
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

func TestDefaultsMatchOriginal(t *testing.T) {
	featurecfg.Reset()
	if BuildingMaxLevel() != 3 || BuildingUpgradePrice(0) != 15_000 || BuildingUpgradePrice(3) != 0 {
		t.Fatal(BuildingMaxLevel(), BuildingUpgradePrice(0))
	}
	if BuildingCutPct(0) != 0 || BuildingCutPct(3) != 60 || CraftSeconds(3600, 3) != 1440 || CraftSeconds(1, 3) != 1 {
		t.Fatal(BuildingCutPct(3), CraftSeconds(3600, 3))
	}
	for _, code := range ItemCodes() {
		if balance.ItemPrice(code) != balance.Items[code].Price {
			t.Fatal(code)
		}
	}
	if balance.DefenseOf("guard_dog").Price != 1200 || balance.BuildingOf("bakery").Price != 3500 {
		t.Fatal("預設價格")
	}
}

func TestConfigOverrides(t *testing.T) {
	defer featurecfg.Reset()
	v, err := ValidateConfig(featurecfg.Value{
		"building_prices": []any{100, 200}, "building_cut_pct": []any{10, 20},
		"item_prices":    map[string]any{balance.ItemFertilizer: 99},
		"defense_prices": map[string]any{"fence": 1},
		"building_build": map[string]any{"mill": 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if BuildingMaxLevel() != 2 || BuildingUpgradePrice(1) != 200 || BuildingCutPct(3) != 20 { // 超過新的最高等級以最高等級計算
		t.Fatal(BuildingMaxLevel(), BuildingCutPct(3))
	}
	if balance.ItemPrice(balance.ItemFertilizer) != 99 || balance.ItemOf(balance.ItemFertilizer).Price != 99 ||
		balance.ItemPrice(balance.ItemEarthworm) != balance.Items[balance.ItemEarthworm].Price {
		t.Fatal("道具價格")
	}
	if balance.DefenseOf("fence").Price != 1 || balance.BuildingOf("mill").Price != 7 || balance.BuildingOf("barn").Price != 1000 {
		t.Fatal("設施價格")
	}
	bad := []featurecfg.Value{
		{"building_prices": []any{1, 2, 3, 4}, "building_cut_pct": []any{1, 2, 3, 4}}, // 超過 farm_buildings.level 的 CHECK
		{"building_prices": []any{1, 2}, "building_cut_pct": []any{1}},
		{"beehive_prices": []any{1, 2}},
		{"item_prices": map[string]any{"nope": 1}},
		{"item_prices": map[string]any{balance.ToolAutoFisher: 1}}, // 自動釣魚機在 autofish 設定
		{"defense_prices": map[string]any{"fence": 0}},
	}
	for i, raw := range bad {
		if _, err := ValidateConfig(raw); err == nil {
			t.Fatal(i)
		}
	}
}
