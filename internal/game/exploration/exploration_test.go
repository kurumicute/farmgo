package exploration

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const hour = 3600

// seq 依序回傳固定亂數；用完後重複最後一個值。
func seq(values ...float64) Rng {
	i := 0
	return func() float64 {
		v := values[min(i, len(values)-1)]
		i++
		return v
	}
}

func intp(v int) *int { return &v }

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRewardProbabilitiesSumTo100Percent(t *testing.T) {
	for name, bp := range RewardTypeBP {
		sum := 0
		for _, rt := range RewardTypes {
			if _, ok := bp[rt]; !ok {
				t.Fatalf("%s missing %s", name, rt)
			}
			sum += bp[rt]
		}
		if len(bp) != len(RewardTypes) || sum != TotalBP {
			t.Fatalf("%s sum %d", name, sum)
		}
	}
}

func TestAreaSettingsMatchRequirements(t *testing.T) {
	want := [][2]int{{1, 8 * hour}, {5, 16 * hour}, {10, 24 * hour}, {4, 24 * hour}}
	for i, c := range AreaOrder {
		a := Areas[c]
		if a.UnlockLevel != want[i][0] || a.DurationSeconds != want[i][1] {
			t.Fatalf("%s: %d %d", c, a.UnlockLevel, a.DurationSeconds)
		}
	}
	for _, area := range Areas {
		for _, pool := range area.Pools {
			for _, d := range pool {
				if _, ok := Materials[d.Item]; !ok && d.Item != AmberSeed && d.Item != MoonSeed {
					t.Fatalf("unknown item %s", d.Item)
				}
				if !(1 <= d.QtyMin && d.QtyMin <= d.QtyMax && d.Weight > 0) {
					t.Fatalf("bad drop %+v", d)
				}
			}
		}
	}
	names := map[string]map[string]bool{}
	for c, a := range Areas {
		names[c] = map[string]bool{}
		for i := range PossibleItems(a, nil) {
			names[c][MaterialName(i)] = true
		}
	}
	has := func(c string, ns ...string) bool {
		for _, n := range ns {
			if !names[c][n] {
				return false
			}
		}
		return true
	}
	if !has("grassland", "木材", "石塊", "藥草", "假幣") || names["grassland"]["樹脂"] {
		t.Fatal("grassland")
	}
	if !has("forest", "木材", "樹脂", "藥草", "齒輪", "古木核心", "假幣") || names["forest"]["水晶"] {
		t.Fatal("forest")
	}
	if !has("mine", "石塊", "銅礦", "鐵礦", "齒輪", "水晶", "機械核心", "假幣") || names["mine"]["木材"] {
		t.Fatal("mine")
	}
	if !PossibleItems(Areas["forest"], nil)[MoonSeed] || !PossibleItems(Areas["grassland"], nil)[AmberSeed] {
		t.Fatal("seeds")
	}
	sets := map[string]bool{}
	for _, c := range []string{"grassland", "forest", "mine"} {
		sets[strings.Join(keys(PossibleItems(Areas[c], nil)), ",")] = true
	}
	if len(sets) != 3 {
		t.Fatal("pools should differ")
	}
}

func TestDropsOnlyFromAllowedPool(t *testing.T) {
	for _, area := range AreaOrder {
		for _, level := range []int{1, 4, 7, 10, 13, 16} {
			a := Areas[area]
			if level < a.UnlockLevel {
				continue
			}
			allowed := PossibleItems(a, intp(level))
			r := rand.New(rand.NewSource(int64(len(area)*100 + level)))
			seen := map[string]int{}
			totalTypes := 0
			for i := 0; i < 3000; i++ {
				result := Explore(area, level, r.Float64, 0)
				for item := range result.Items {
					if !allowed[item] {
						t.Fatalf("%s lv%d: unexpected %s", area, level, item)
					}
				}
				if result.Total() < 1 {
					t.Fatalf("%s lv%d: empty", area, level)
				}
				for _, ty := range result.Types {
					seen[ty]++
					totalTypes++
				}
			}
			share := float64(seen["common"]) / float64(totalTypes)
			if !(0.3 < share && share < 0.95) {
				t.Fatalf("%s lv%d: common share %v", area, level, share)
			}
		}
	}
}

func TestLevelGatesInsidePool(t *testing.T) {
	grass := Areas["grassland"]
	if PossibleItems(grass, intp(1))["copper_ore"] || !PossibleItems(grass, intp(2))["copper_ore"] {
		t.Fatal("copper")
	}
	if PossibleItems(grass, intp(3))["iron_ore"] || !PossibleItems(grass, intp(4))["iron_ore"] {
		t.Fatal("iron")
	}
	forest := Areas["forest"]
	if PossibleItems(forest, intp(6))["ancient_core"] || !PossibleItems(forest, intp(7))["ancient_core"] {
		t.Fatal("ancient_core")
	}
	if IsAdvanced(forest, 8) || !IsAdvanced(forest, 9) {
		t.Fatal("advanced")
	}
	for _, a := range Areas {
		if !IsAdvanced(a, 16) {
			t.Fatal(a.Code)
		}
	}
	for _, e := range grass.Events {
		if e.Code == "ancient_ruins" && e.MinLevel <= 14 {
			t.Fatal("ancient_ruins level")
		}
	}
}

func TestEventRollGivesFakeCoinsAndBonus(t *testing.T) {
	vals := append([]float64{0.99}, make([]float64, 11)...)
	result := Explore("grassland", 1, seq(vals...), 0)
	if !reflect.DeepEqual(result.Events, []string{"🎁 特殊寶箱"}) {
		t.Fatal(result.Events)
	}
	if result.Items[FakeCoin] != 2 {
		t.Fatal(result.Items)
	}
	if result.Items["wood"] < 2 {
		t.Fatal(result.Items)
	}
}

func TestMachineRecipesMatchRequirements(t *testing.T) {
	m := Machines
	check := func(code string, level int, mats map[string]int) {
		if m[code].UnlockLevel != level || !reflect.DeepEqual(m[code].Materials, mats) {
			t.Fatalf("%s", code)
		}
		if len(m[code].MaterialOrder) != len(mats) {
			t.Fatalf("%s order", code)
		}
	}
	check("auto_harvester", 5, map[string]int{"wood": 20, "iron_ore": 10, "gear": 5})
	check("auto_processor", 8, map[string]int{"stone": 15, "copper_ore": 15, "iron_ore": 10, "machine_core": 1})
	check("rare_incubator", 12, map[string]int{"wood": 20, "herb": 15, "crystal": 5, "ancient_core": 1})
	check("seed_maker", 3, map[string]int{"wood": 10, "stone": 10, "copper_ore": 5, "gear": 3})
}

func TestRuinsAreaUsesForestTemplate(t *testing.T) {
	area, forest := Areas["ruins"], Areas["forest"]
	if !area.NeedsRuins {
		t.Fatal("needs ruins")
	}
	for c, a := range Areas {
		if c != "ruins" && a.NeedsRuins {
			t.Fatal(c)
		}
	}
	if area.UnlockLevel != 4 || area.DurationSeconds != 24*hour || area.Rolls != forest.Rolls {
		t.Fatal("ruins area")
	}
	if !reflect.DeepEqual(PossibleItems(area, nil), PossibleItems(forest, nil)) {
		t.Fatal("same pool")
	}
	if !reflect.DeepEqual(PossibleItems(area, intp(4)), PossibleItems(forest, intp(5))) {
		t.Fatal("level shift")
	}
	if len(PossibleItems(area, intp(4))) == 0 {
		t.Fatal("empty at lv4")
	}
	r := Explore("ruins", 4, seq(make([]float64, 50)...), 2)
	if len(r.Types) != forest.Rolls+2 {
		t.Fatal(len(r.Types))
	}
	if area.LevelNotes[1].Text != strings.ReplaceAll(forest.LevelNotes[0].Text, "森林", "遺跡") {
		t.Fatal(fmt.Sprint(area.LevelNotes))
	}
}

func TestFindAreaAndRareCrops(t *testing.T) {
	if FindArea(" Forest ") != "forest" || FindArea("神秘礦坑") != "mine" || FindArea("x") != "" {
		t.Fatal("find area")
	}
	RareCrops = RareCrops[:1]
	if len(RareSeedCodes()) != 1 {
		t.Fatal("set")
	}
	ResetRareCrops()
	if !reflect.DeepEqual(RareSeedCodes(), []string{AmberSeed, MoonSeed}) {
		t.Fatal(RareSeedCodes())
	}
}
