package services

// 對應 Python tests/test_new_catalog.py 與 tests/test_seasons.py 的服務層部分：
// 新作物、新魚與高級魚餌（Migration 0033），季節與時段限定（Migration 0034／0035）。
// 純規則在 game/seasons；管理 API 的限定與指定魚餌在 admin_test.go。
// test_seasons.py 的 test_migration_constants_match_rules 比對 Python Migration 檔案的常數，屬於 Python 專案（Go 不管理 schema），不移植。

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/seasons"
)

var newCrops = []string{"corn", "pumpkin", "grape", "watermelon"}

// taipei 台灣時間 2026 年 month/day hour:00。
func taipei(month, day, hour int) time.Time {
	return time.Date(2026, time.Month(month), day, hour, 0, 0, 0, time.UTC).Add(-8 * time.Hour)
}

// applySeasonDefaults 套用與正式環境相同的預設限定（setup 會把限定清空）。
func (e *env) applySeasonDefaults() {
	e.t.Helper()
	for code, s := range seasons.CropSeasons {
		data, _ := json.Marshal(s)
		e.exec("UPDATE crop_types SET seasons = $2::json WHERE code = $1", code, string(data))
	}
	for code, l := range seasons.FishLimits {
		var s, a, b any
		if len(l.Seasons) > 0 {
			data, _ := json.Marshal(l.Seasons)
			s = string(data)
		}
		if l.Hours != nil {
			a, b = l.Hours.Start, l.Hours.End
		}
		e.exec("UPDATE fish_types SET seasons = $2::json, hour_start = $3, hour_end = $4 WHERE code = $1", code, s, a, b)
	}
}

// fisherWith 等級 10、100,000 金幣，買竹竿、基本釣線與魚餌（Python setup_fisher）。
func (e *env) fisherWith(pid int64, bait string, qty int) {
	e.t.Helper()
	e.exec("UPDATE farms SET level = 10, gold = 100000 WHERE player_id = $1", pid)
	for _, code := range []string{"bamboo_rod", "basic_line"} {
		if _, err := e.a.BuyFishingGear(e.ctx, pid, code, 1); err != nil && !gameerr.Is(err, gameerr.AlreadyExists) {
			e.t.Fatal(err)
		}
	}
	e.ok(e.a.BuyFishingGear(e.ctx, pid, bait, qty))
}

// capturePools 攔截挑魚：記錄可以釣的魚，固定回傳第一條普通魚（Python monkeypatch fishing.pick_fish）。
func capturePools(t *testing.T, seen map[string]bool) {
	pickFish = func(pools map[string][]models.FishType, _ string, _ fishing.RNG) (models.FishType, bool) {
		for _, pool := range pools {
			for _, f := range pool {
				seen[f.Code] = true
			}
		}
		return pools["common"][0], true
	}
	t.Cleanup(func() { pickFish = fishing.PickFish[models.FishType] })
}

func TestMigrationInsertedCatalog(t *testing.T) {
	e := setup(t)
	crops := map[string][]any{}
	for _, r := range e.rowsOf(`SELECT code, name, seed_price, harvest_quantity, sell_price, variant_code, variant_name, active, shop_buyable
		FROM crop_types WHERE code = ANY($1)`, newCrops) {
		crops[r[0].(string)] = r
	}
	if len(crops) != len(newCrops) {
		t.Fatal(crops)
	}
	for _, c := range balance.InitialCrops[4:] {
		r := crops[c.Code]
		v := balance.InitialVariants[c.Code]
		want := fmt.Sprint(c.Code, c.Name, c.SeedPrice, c.HarvestQty, c.SellPrice, v.Code, v.Name, true, true)
		if fmt.Sprint(r...) != want {
			t.Fatalf("%v，應為 %v", r, want)
		}
	}
	newFish := fishing.InitialFish[15:35] // Migration 0033 的 20 種（0035 的特殊魚另外測）
	rarities := map[string]int{}
	for _, f := range newFish {
		rarities[f.Rarity]++
	}
	if len(newFish) != 20 || len(fishing.InitialFish) != 45 ||
		!maps.Equal(rarities, map[string]int{"common": 6, "rare": 6, "epic": 5, "legendary": 3}) {
		t.Fatal(rarities)
	}
	type fishRow struct {
		rarity             string
		required           []string
		minPrice, maxPrice int
	}
	fish := map[string]fishRow{}
	for _, r := range e.rowsOf("SELECT code, rarity, coalesce(required_baits::text, 'null'), min_price, max_price FROM fish_types") {
		var req []string
		if err := json.Unmarshal([]byte(r[2].(string)), &req); err != nil {
			t.Fatal(err)
		}
		fish[r[0].(string)] = fishRow{r[1].(string), req, int(r[3].(int32)), int(r[4].(int32))}
	}
	if len(fish) != 45 {
		t.Fatal(len(fish))
	}
	// Migration 0037、0038 各把魚價調高 1.5 倍：資料庫的售價要等於 InitialFish（Go 版的初始值）
	for _, f := range fishing.InitialFish {
		if got := fish[f.Code]; got.minPrice != f.MinPrice || got.maxPrice != f.MaxPrice {
			t.Fatalf("%s：資料庫 %d–%d，InitialFish %d–%d", f.Code, got.minPrice, got.maxPrice, f.MinPrice, f.MaxPrice)
		}
	}
	baits := map[string]int{}
	for _, r := range e.rowsOf("SELECT code, price FROM fishing_gear WHERE kind = 'bait'") {
		baits[r[0].(string)] = int(r[1].(int32))
	}
	for code, required := range fishing.FishRequiredBaits {
		f := fish[code]
		if (f.rarity != "epic" && f.rarity != "legendary") || !slices.Equal(f.required, required) {
			t.Fatalf("%s %+v", code, f)
		}
		for _, b := range required {
			if _, ok := baits[b]; !ok {
				t.Fatal(b)
			}
		}
	}
	for code, f := range fish {
		if _, locked := fishing.FishRequiredBaits[code]; !locked && f.required != nil {
			t.Fatalf("%s %+v", code, f)
		}
	}
	if !(baits["dragon_bait"] > baits["deep_sea_bait"] && baits["deep_sea_bait"] > baits["special_bait"]) {
		t.Fatal(baits)
	}
	first := map[string]bool{}
	for _, f := range fishing.InitialFish[:35] {
		first[f.Code] = true
	}
	for code, f := range fish { // 0033：要特殊魚餌的魚比同稀有度的一般魚貴
		if len(f.required) == 0 || !first[code] || f.rarity == "legendary" {
			continue
		}
		for c, x := range fish {
			if x.rarity == f.rarity && len(x.required) == 0 && first[c] && f.minPrice <= x.maxPrice {
				t.Fatalf("%s 最低價 %d 不高於 %s 最高價 %d", code, f.minPrice, c, x.maxPrice)
			}
		}
	}
}

func TestNewCropCanBeBoughtPlantedAndHarvested(t *testing.T) {
	e := setup(t)
	pid := e.player()
	gold := 10_000
	must[FarmValues](t)(e.a.SetFarmValues(e.ctx, "test", pid, &gold, nil))
	for _, code := range newCrops {
		e.ok(e.a.BuySeeds(e.ctx, pid, code, 1))
	}
	e.ok(e.a.PlantCrop(e.ctx, pid, 1, "玉米"))
	e.ok(e.a.WaterCrop(e.ctx, pid, 1))
	e.clock.Advance(120)
	if r := e.ok(e.a.HarvestCrop(e.ctx, pid, 1)); r.Quantity != 3 || !strings.Contains(r.Message, "玉米") {
		t.Fatalf("%+v", r)
	}
	e.grow(pid, 2, "watermelon")
}

func TestBaitDecidesWhichLockedFishCanAppear(t *testing.T) {
	deepSea := map[string]bool{}
	all := map[string]bool{}
	for c, b := range fishing.FishRequiredBaits {
		all[c] = true
		if slices.Contains(b, "deep_sea_bait") {
			deepSea[c] = true
		}
	}
	cases := []struct {
		bait    string
		allowed map[string]bool
	}{{"worm_bait", map[string]bool{}}, {"special_bait", map[string]bool{}}, {"deep_sea_bait", deepSea}, {"dragon_bait", all}}
	for _, c := range cases {
		t.Run(c.bait, func(t *testing.T) {
			e := setup(t)
			pid := e.player()
			e.fisherWith(pid, c.bait, 5)
			seen := map[string]bool{}
			capturePools(t, seen)
			must[FishingView](t)(e.a.StartFishing(e.ctx, pid))
			got := map[string]bool{}
			for code := range all {
				if seen[code] {
					got[code] = true
				}
			}
			if !maps.Equal(got, c.allowed) {
				t.Fatalf("%v，應為 %v", got, c.allowed)
			}
			for _, code := range []string{"formosan_salmon", "golden_koi", "oarfish", "salmon"} { // 原本的魚任何魚餌都釣得到
				if !seen[code] {
					t.Fatal(code)
				}
			}
		})
	}
}

func TestLockedLegendaryIsReallyCaughtWithDragonBait(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.a.FishingService.FishingRng = func() float64 { return 0.9999 } // 一定抽到傳說、挑池子最後一條
	e.fisherWith(pid, "dragon_bait", 5)
	must[FishingView](t)(e.a.StartFishing(e.ctx, pid))
	code := e.text("SELECT fish_code FROM fishing_sessions ORDER BY id DESC LIMIT 1")
	legendary := false
	for _, f := range fishing.InitialFish {
		legendary = legendary || (f.Code == code && f.Rarity == "legendary")
	}
	if b, locked := fishing.FishRequiredBaits[code]; !legendary || (locked && !slices.Contains(b, "dragon_bait")) {
		t.Fatal(code)
	}
}

func TestCropOnlyPlantedInSeason(t *testing.T) {
	e := setup(t)
	e.applySeasonDefaults()
	pid := e.player()
	gold := 10_000
	must[FarmValues](t)(e.a.SetFarmValues(e.ctx, "test", pid, &gold, nil))
	e.ok(e.a.BuySeeds(e.ctx, pid, "corn", 3)) // 種子隨時可以買
	info := map[string]CropInfo{}
	for _, c := range must[[]CropInfo](t)(e.a.GetMarketPrices(e.ctx)) {
		info[c.Code] = c
	}
	if info["corn"].InSeason || info["corn"].SeasonText != "🌸☀️春、夏季限定" || !info["wheat"].InSeason {
		t.Fatalf("%+v %+v", info["corn"], info["wheat"])
	}
	if !info["strawberry"].InSeason || !info["carrot"].InSeason { // 現在（1 月）是冬天
		t.Fatalf("%+v %+v", info["strawberry"], info["carrot"])
	}
	wantSeason := func(err error) {
		t.Helper()
		wantKind(t, err, gameerr.Validation)
		if !strings.Contains(err.Error(), "春、夏季限定") {
			t.Fatal(err)
		}
	}
	_, err := e.a.PlantCrop(e.ctx, pid, 1, "corn")
	wantSeason(err)
	_, err = e.a.PlantAll(e.ctx, pid, "玉米")
	wantSeason(err)
	e.clock.Set(taipei(3, 1, 9))
	e.ok(e.a.PlantCrop(e.ctx, pid, 2, "corn"))   // 春天可以種
	e.clock.Set(taipei(9, 1, 0).Add(-time.Hour)) // 8/31 23:00（夏天）種下
	e.ok(e.a.PlantCrop(e.ctx, pid, 3, "corn"))
	e.ok(e.a.WaterCrop(e.ctx, pid, 3))
	e.clock.Set(taipei(9, 1, 0)) // 過了夏天（秋天）：不能再種，但已經種下的照樣收成
	_, err = e.a.PlantCrop(e.ctx, pid, 1, "corn")
	wantSeason(err)
	if r := e.ok(e.a.HarvestCrop(e.ctx, pid, 3)); r.Quantity != 3 {
		t.Fatalf("%+v", r)
	}
}

// fishable 目前可以釣到的魚（Python fishable）。
func (e *env) fishable(pid int64) map[string]bool {
	e.t.Helper()
	e.fisherWith(pid, "dragon_bait", 1)
	seen := map[string]bool{}
	capturePools(e.t, seen)
	view := must[FishingView](e.t)(e.a.StartFishing(e.ctx, pid))
	must[FishingView](e.t)(e.a.GiveUpFishing(e.ctx, pid, view.ID))
	return seen
}

func TestFishOnlyInSeasonAndHours(t *testing.T) {
	e := setup(t)
	e.applySeasonDefaults()
	pid := e.player()
	check := func(seen map[string]bool, have, not []string) {
		t.Helper()
		for _, c := range have {
			if !seen[c] {
				t.Fatalf("應可釣到 %s", c)
			}
		}
		for _, c := range not {
			if seen[c] {
				t.Fatalf("不應釣到 %s", c)
			}
		}
	}
	e.clock.Set(taipei(1, 10, 9)) // 冬天早上 9 點
	check(e.fishable(pid), []string{"pond_smelt", "mullet", "bluegill"},
		[]string{"eel", "snakehead", "moon_ray", "mackerel", "swordfish", "yellowfin_tuna", "dragon_king_carp"})
	e.clock.Set(taipei(4, 10, 21)) // 春天晚上 9 點
	check(e.fishable(pid), []string{"eel", "snakehead", "moon_ray", "dragon_king_carp"}, []string{"bluegill", "pond_smelt", "mullet"})
	codex := map[string]FishCodexEntry{}
	for _, x := range must[FishCodex](t)(e.a.FishCodex(e.ctx, pid)).Entries {
		codex[x.Code] = x
	}
	if codex["eel"].LimitText != "🕒 18:00–06:00" || codex["mullet"].LimitText != "❄️冬季限定" {
		t.Fatal(codex["eel"].LimitText, codex["mullet"].LimitText)
	}
}
