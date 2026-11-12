package mushroom

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"
)

func seq(vals ...float64) func() float64 {
	i := 0
	return func() float64 {
		v := vals[i%len(vals)]
		i++
		return v
	}
}

func TestMixAndNutrient(t *testing.T) {
	if got := Mix(nil); len(got) != 1 || got[DefaultNutrient] != 1 {
		t.Fatalf("沒有作物時應為預設養分：%v", got)
	}
	got := Mix([]string{"wheat", "corn", "carrot", "grape", "unknown_crop"})
	if got["grain"] != 3 || got["veggie"] != 1 || got["fruit"] != 1 {
		t.Fatalf("養分格數錯誤：%v", got)
	}
	if NutrientOf("moonflower") != "fruit" || NutrientOf("pumpkin") != "veggie" {
		t.Fatal("作物對應的養分錯誤")
	}
}

func TestRollFollowsCrops(t *testing.T) {
	// 只種水果：只會長金針菇或發光菇
	mix := Mix([]string{"strawberry", "grape"})
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 500; i++ {
		for _, c := range Roll(mix, 0, YieldPerBed, r.Float64) {
			if c != "enoki" && c != "glowcap" {
				t.Fatalf("只種水果卻長出 %s", c)
			}
		}
	}
}

func TestRollRareChance(t *testing.T) {
	// 0 個木屑：u = 0.9 → 普通菇；u = 0.01 → 稀有菇（5%）
	mix := Mix([]string{"wheat"})
	if got := Roll(mix, 0, 1, seq(0.5, 0.9)); got[0] != "shiitake" {
		t.Fatalf("應為香菇：%v", got)
	}
	if got := Roll(mix, 0, 1, seq(0.5, 0.01)); got[0] != "lingzhi" {
		t.Fatalf("應為靈芝：%v", got)
	}
	// 木屑超出範圍時夾在 0..MaxSawdust
	if got := Roll(mix, 99, 2, seq(0.5, 0.99)); got[0] != "lingzhi" {
		t.Fatalf("投入上限時第一朵保證稀有：%v", got)
	}
}

func TestMaxSawdustGuaranteesHalfRare(t *testing.T) {
	mix := Mix([]string{"wheat", "carrot", "grape"})
	r := rand.New(rand.NewPCG(7, 9))
	for yield := 1; yield <= 6; yield++ {
		for i := 0; i < 300; i++ {
			got := Roll(mix, MaxSawdust, yield, r.Float64)
			rare := 0
			for _, c := range got {
				if SpeciesMap[c].Rare {
					rare++
				}
			}
			if rare*2 < yield {
				t.Fatalf("投入 %d 個木屑、收成 %d 朵卻只有 %d 朵稀有：%v", MaxSawdust, yield, rare, got)
			}
		}
	}
}

func TestRareRateIncreasesWithSawdust(t *testing.T) {
	mix := Mix([]string{"wheat"})
	r := rand.New(rand.NewPCG(3, 4))
	prev := -1.0
	for s := 0; s <= MaxSawdust; s++ {
		rare, total := 0, 0
		for i := 0; i < 20000; i++ {
			for _, c := range Roll(mix, s, YieldPerBed, r.Float64) {
				total++
				if SpeciesMap[c].Rare {
					rare++
				}
			}
		}
		rate := float64(rare) / float64(total)
		if rate <= prev {
			t.Fatalf("木屑 %d 個的稀有率 %.3f 沒有比較高（前一級 %.3f）", s, rate, prev)
		}
		prev = rate
	}
}

func TestBedsRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 12, 8, 0, 0, 123000, time.UTC)
	beds := NewBeds(now.Add(-25 * time.Hour))
	beds[1].Sawdust = 3
	parsed, fixed := ParseBeds(roundTripJSON(t, EncodeBeds(beds)), now)
	if fixed {
		t.Fatal("正常資料不應修補")
	}
	if !parsed[0].StartedAt.Equal(beds[0].StartedAt) || parsed[1].Sawdust != 3 {
		t.Fatalf("讀回錯誤：%+v", parsed)
	}
	if !parsed[0].Ready(now) || parsed[0].Progress(now) != 1 {
		t.Fatal("超過生長時間應可收成")
	}
	// 壞資料：缺菇床、時間在未來 → 從 now 開始
	bad := map[string]any{"beds": []any{map[string]any{"started_at": now.Add(time.Hour).Format(time.RFC3339Nano)}}}
	parsed, fixed = ParseBeds(bad, now)
	if !fixed || len(parsed) != BedCount || !parsed[0].StartedAt.Equal(now) {
		t.Fatalf("壞資料應修補：%v %+v", fixed, parsed)
	}
	if parsed, fixed = ParseBeds(nil, now); !fixed || len(parsed) != BedCount {
		t.Fatal("沒有設定時應建立菇床")
	}
}

// roundTripJSON 模擬資料庫 JSON 欄位寫入再讀回（數字會變成 float64）。
func roundTripJSON(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
