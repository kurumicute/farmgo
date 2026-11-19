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
