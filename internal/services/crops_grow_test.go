package services

import (
	"testing"

	"farmgm/internal/db"
)

// 種植選單的生長時間（2026-11-19）：和澆水後實際的成熟時間一致（溫室等加速都算進去）。
func TestGrowTimesMatchWatering(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setGoldRecorded(pid, 1_000_000)
	e.exec("UPDATE crop_types SET grow_seconds = 36000 WHERE code = 'wheat'") // 測試資料的小麥只要 60 秒，碰到最短時間下限
	db.CatalogClear()
	base := must[map[string]int](t)(e.s.GrowTimes(e.ctx, pid))["wheat"]
	e.ok(e.s.UpgradeGreenhouse(e.ctx, pid))
	e.ok(e.s.UpgradeGreenhouse(e.ctx, pid))
	want := must[map[string]int](t)(e.s.GrowTimes(e.ctx, pid))["wheat"]
	if want >= base {
		t.Fatal("溫室要讓時間變短", base, want)
	}
	e.ok(e.s.BuySeeds(e.ctx, pid, "wheat", 1))
	e.ok(e.s.PlantCrop(e.ctx, pid, 1, "wheat"))
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	if got := int(e.plot(pid, 1).ReadyAt.Sub(e.clock.Now()).Seconds()); got != want {
		t.Fatal(got, want)
	}
}

// TestLabSeedGrowTimeMatchesWatering 農田頁的特殊種子選單顯示的時間（2026-11-19）：含生長基因，和澆水後實際的成熟時間相同。
func TestLabSeedGrowTimeMatchesWatering(t *testing.T) {
	e := setup(t)
	pid := e.labOwner("阿明", e.group("主群組"))
	e.exec("UPDATE crop_types SET grow_seconds = 36000 WHERE code = 'wheat'")
	db.CatalogClear()
	seed := e.identify(pid, "wheat")
	e.exec("UPDATE lab_seeds SET gene_growth = 5, mutation = 'none' WHERE id = $1", seed.ID)
	normal := must[map[string]int](t)(e.s.GrowTimes(e.ctx, pid))["wheat"]
	var got int
	for _, v := range must[[]LabSeedView](t)(e.s.PlantableLabSeeds(e.ctx, pid)) {
		if v.ID == seed.ID {
			got = v.GrowSeconds
		}
	}
	if got <= 0 || got >= normal {
		t.Fatal("生長基因要讓時間比一般種子短", got, normal)
	}
	e.ok(e.s.PlantLabSeeds(e.ctx, pid, []int64{seed.ID}))
	e.ok(e.s.WaterCrop(e.ctx, pid, 1))
	if ready := int(e.plot(pid, 1).ReadyAt.Sub(e.clock.Now()).Seconds()); ready != got {
		t.Fatal(ready, got)
	}
}
