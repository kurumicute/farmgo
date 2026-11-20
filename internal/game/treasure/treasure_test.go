package treasure

import (
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/game/exploration"
)

func TestWeekSiteStableWithinWeek(t *testing.T) {
	mon := time.Date(2026, 11, 16, 0, 0, 0, 0, clock.TaipeiTZ) // 週一
	if !WeekStart(mon.Add(6*24*time.Hour + 23*time.Hour)).Equal(mon) {
		t.Fatal("週日 23 點應屬於同一週")
	}
	if WeekSite(mon).Code != WeekSite(mon.Add(6*24*time.Hour)).Code {
		t.Fatal("同一週的地點要相同")
	}
	seen := map[string]bool{}
	for w := range 40 {
		seen[WeekSite(mon.AddDate(0, 0, 7*w)).Code] = true
	}
	if len(seen) != len(SiteOrder) {
		t.Fatalf("40 週只出現 %v", seen)
	}
}

func TestDroppedAndRainbow(t *testing.T) {
	if !Dropped(SourceExplore, false, 0.0799) || Dropped(SourceExplore, false, 0.08) {
		t.Fatal("探險 8%")
	}
	if !Dropped(SourceExplore, true, 0.15) || Dropped(SourceFish, false, 0.01) {
		t.Fatal("彩虹日加倍、釣魚 1%")
	}
	if RandomPiece(0) != "n" || RandomPiece(0.999) != "w" {
		t.Fatal("方位")
	}
}

func TestRollUsesSiteTable(t *testing.T) {
	for _, code := range SiteOrder {
		site := Sites[code]
		allowed := map[string]bool{}
		for _, d := range site.Drops {
			allowed[d.Item] = true
			if _, ok := exploration.Materials[d.Item]; !ok && d.Item != exploration.AmberSeed && d.Item != exploration.MoonSeed {
				t.Fatalf("%s 的 %s 不是素材背包物品", code, d.Item)
			}
		}
		for _, u := range []float64{0, 0.5, 0.999} {
			r := Roll(site, func() float64 { return u })
			if r.Gold < site.Gold[0] || r.Gold > site.Gold[1] || len(r.Items) == 0 {
				t.Fatalf("%s %+v", code, r)
			}
			for item := range r.Items {
				if !allowed[item] {
					t.Fatalf("%s 掉了 %s", code, item)
				}
			}
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	end := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	s := State{Pieces: map[string]int{"n": 2, "e": 1, "s": 1, "w": 1}, HuntSite: "oak", HuntEnd: &end,
		Found: []string{"mine"}, RainbowDay: "2026-11-20"}
	// 模擬存進資料庫再讀出（數字變成 float64、陣列變成 []any）
	raw := map[string]any{"pieces": map[string]any{"n": 2.0, "e": 1.0, "s": 1.0, "w": 1.0},
		"hunt_site": "oak", "hunt_end": s.Encode()["hunt_end"], "found": []any{"mine"}, "rainbow_day": "2026-11-20"}
	got := Parse(raw)
	if !got.Complete() || got.Total() != 5 || got.HuntSite != "oak" || !got.HuntEnd.Equal(end) || got.Found[0] != "mine" ||
		got.RainbowDay != s.RainbowDay {
		t.Fatalf("%+v", got)
	}
	if !got.HuntDone(end) || got.HuntDone(end.Add(-time.Second)) {
		t.Fatal("HuntDone")
	}
	if Parse(nil).Hunting() || Parse(map[string]any{"hunt_site": "nowhere", "hunt_end": "x"}).Hunting() {
		t.Fatal("壞資料不能當成尋寶中")
	}
}
