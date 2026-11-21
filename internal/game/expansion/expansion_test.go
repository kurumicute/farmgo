package expansion

import (
	"testing"
	"time"

	"farmgm/internal/game/featurecfg"
)

func TestRenameLevel(t *testing.T) {
	featurecfg.Reset()
	day := 24 * time.Hour
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if l := RenameLevel(nil, t0); l != 0 || RenamePrice(l) != 0 {
		t.Fatal(l)
	}
	one := []time.Time{t0}
	if l := RenameLevel(one, t0.Add(day)); l != 1 || RenamePrice(l) != 1000 {
		t.Fatal(l)
	}
	three := []time.Time{t0, t0.Add(day), t0.Add(2 * day)}
	if l := RenameLevel(three, t0.Add(3*day)); l != 3 || RenamePrice(l) != 4000 {
		t.Fatal(l)
	}
	// 30 天沒改名降一級，最低 1 級
	if l := RenameLevel(three, t0.Add(2*day+30*day)); l != 2 {
		t.Fatal(l)
	}
	if l := RenameLevel(three, t0.Add(400*day)); l != 1 {
		t.Fatal(l)
	}
	if RenamePrice(30) != 100_000 || RenamePrice(1000) != 100_000 {
		t.Fatal(RenamePrice(30))
	}
}

func TestLandAndWarehousePrice(t *testing.T) {
	featurecfg.Reset()
	if p, ok := LandPrice(15); !ok || p != 180_000 {
		t.Fatal(p)
	}
	if p, ok := LandPrice(16); !ok || p != 300_000 {
		t.Fatal(p)
	}
	if p, ok := LandPrice(17); !ok || p != 390_000 {
		t.Fatal(p)
	}
	if _, ok := LandPrice(32); ok {
		t.Fatal("32 格是上限")
	}
	if p, ok := WarehousePrice(250); !ok || p != 200_000 {
		t.Fatal(p)
	}
	if p, ok := WarehousePrice(300); !ok || p != 400_000 {
		t.Fatal(p)
	}
	if p, ok := WarehousePrice(350); !ok || p != 640_000 {
		t.Fatal(p)
	}
	if _, ok := WarehousePrice(600); ok {
		t.Fatal("600 格是上限")
	}
	if _, err := ValidateConfig(featurecfg.Value{"rename_first_free": 1, "rename_base": 1000, "rename_max": 1, "rename_decay_days": 30,
		"land_max": 40, "land_base": 1, "land_growth_pct": 130, "warehouse_max": 600, "warehouse_late_base": 1, "warehouse_late_growth_pct": 160}); err == nil {
		t.Fatal("土地上限不能超過 32")
	}
}

// 2026-11-21：土地前 16 格、倉庫 300 格以內的價格與倉庫每次擴充格數也可以調整；舊的設定快照沒有這些欄位時用預設值。
func TestEarlyPricesConfigurable(t *testing.T) {
	defer featurecfg.Reset()
	old := featurecfg.Value{"rename_first_free": 1, "rename_base": 1000, "rename_max": 100000, "rename_decay_days": 30,
		"land_max": 32, "land_base": 300000, "land_growth_pct": 130, "warehouse_max": 600, "warehouse_late_base": 400000,
		"warehouse_late_growth_pct": 160}
	v, err := ValidateConfig(old)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if p, _ := LandPrice(4); p != 500 || WarehouseStep() != 50 {
		t.Fatal(p, WarehouseStep())
	}
	if p, _ := WarehousePrice(50); p != 4_000 {
		t.Fatal(p)
	}
	cfg := featurecfg.Value{}
	for k, x := range old {
		cfg[k] = x
	}
	cfg["land_prices"] = []any{100, 200}
	cfg["warehouse_prices"] = []any{1_000, 2_000, 3_000}
	cfg["warehouse_step"] = 100
	v, err = ValidateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if p, _ := LandPrice(4); p != 100 {
		t.Fatal(p)
	}
	if p, _ := LandPrice(10); p != 200 { // 清單比級數短：之後用最後一項
		t.Fatal(p)
	}
	if p, _ := WarehousePrice(150); p != 2_000 || WarehouseStep() != 100 {
		t.Fatal(p)
	}
	if p, _ := WarehousePrice(300); p != 400_000 { // 後段不受影響
		t.Fatal(p)
	}
	cfg["land_prices"] = []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	if _, err := ValidateConfig(cfg); err == nil {
		t.Fatal("土地前段最多 12 個價格")
	}
}
