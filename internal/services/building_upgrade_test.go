package services

import (
	"testing"

	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/upgrades"
)

// 加工設施升級（2026-11-17）：15,000／50,000／100,000 金，加工時間 -15%／-40%／-60%，最多 3 級；畜舍不能升級。
func TestUpgradeBuildingCutsCraftTime(t *testing.T) {
	e := setup(t)
	f := e.rancher() // 100,000 金
	_, err := e.r.UpgradeBuilding(e.ctx, f, "mill")
	wantKind(t, err, gameerr.Validation) // 還沒建造
	_, err = e.r.UpgradeBuilding(e.ctx, f, "barn")
	wantKind(t, err, gameerr.Validation)
	e.ok(e.r.Build(e.ctx, f, "磨坊")) // -2,000
	e.ok(e.r.UpgradeBuilding(e.ctx, f, "mill"))
	if st := must[WorkshopStatus](t)(e.r.WorkshopStatus(e.ctx, f)); st.Levels["mill"] != 1 || st.Gold != 100_000-2_000-15_000 {
		t.Fatalf("%+v", st)
	}
	pid := f
	for _, r := range must[[]RecipeView](t)(e.r.Recipes(e.ctx, &pid)) {
		if r.Code == "flour" && r.Seconds != 30*60*85/100 { // 麵粉 30 分鐘 -15%
			t.Fatal(r.Seconds)
		}
	}
	e.give(f, "wheat", 6)
	e.ok(e.r.StartCraft(e.ctx, f, "flour", 2)) // 2 份 60 分鐘 → 51 分鐘
	if o := e.orders(f, false)[0]; o.ReadyAt.Sub(o.StartedAt).Seconds() != 3600*85/100 {
		t.Fatal(o.ReadyAt.Sub(o.StartedAt))
	}
	_, err = e.r.UpgradeBuilding(e.ctx, f, "mill") // 剩 83,000：升到 2 級（50,000）夠，3 級（100,000）不夠
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.r.UpgradeBuilding(e.ctx, f, "mill")
	wantKind(t, err, gameerr.InsufficientGold)
	e.setGold(f, 200_000)
	e.ok(e.r.UpgradeBuilding(e.ctx, f, "mill"))
	_, err = e.r.UpgradeBuilding(e.ctx, f, "mill")
	wantKind(t, err, gameerr.AlreadyExists)
	if got := upgrades.CraftSeconds(3600, 3); got != 1440 {
		t.Fatal(got)
	}
	if got := upgrades.CraftSeconds(1, 3); got != 1 {
		t.Fatal(got)
	}
}
