package npcorder

import (
	"encoding/json"
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

func TestGenerateOnlyEligible(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if _, ok := Generate(1, map[string]int{}, 1, now, seq(0.5)); ok {
		t.Fatal("沒有做得出來的東西時不能產生訂單")
	}
	eligible := map[string]int{"wheat": 10, "carrot": 20}
	for _, u := range []float64{0, 0.3, 0.6, 0.99} {
		o, ok := Generate(1, eligible, 1, now, seq(0.99, u, u, u, u, u))
		if !ok || len(o.Items) != 1 || eligible[o.Items[0].Code] == 0 || o.VIP() {
			t.Fatalf("%+v", o)
		}
		if o.NPC != "baker" && o.NPC != "fisher" {
			t.Fatalf("只有麵包師和老漁夫要小麥或胡蘿蔔：%s", o.NPC)
		}
		v := Value(o.Items, eligible)
		if o.Reward < v*MultMin/100 || o.Reward > v*MultMax/100 {
			t.Fatalf("報酬 %d、物品價值 %d", o.Reward, v)
		}
		if h := o.Expires.Sub(now).Hours(); h < MinHours || h > MaxHours {
			t.Fatal(h)
		}
	}
}

func TestGenerateVIP(t *testing.T) {
	now := time.Now()
	// 第一個亂數 0 → 抽中 VIP；有高價物品時才會是 VIP
	o, ok := Generate(1, map[string]int{"wheat": 10, "glowcap_pie": 2300}, 1, now, seq(0))
	if !ok || !o.VIP() || o.Items[0].Code != "glowcap_pie" || o.Reward != o.Items[0].Qty*2300*VIPMultPct/100 {
		t.Fatalf("%+v", o)
	}
	o, _ = Generate(1, map[string]int{"wheat": 10}, 1, now, seq(0))
	if o.VIP() {
		t.Fatal("沒有高價物品時不能是 VIP")
	}
}

func TestRefreshExpiresAndRefills(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	eligible := map[string]int{"wheat": 10}
	var s State
	if !s.Refresh(now, eligible, 1, seq(0.99)) || len(s.Orders) != Slots || s.NextID != Slots { // 0.99 → 期限 6 小時
		t.Fatalf("%+v", s)
	}
	s.Streak = 3
	s.Orders = s.Orders[1:] // 交了一張
	if s.Refresh(now.Add(time.Hour), eligible, 1, seq(0.5)) || len(s.Orders) != Slots-1 {
		t.Fatal("還沒到補單時間")
	}
	later := now.Add(RefillSeconds * time.Second)
	s.Orders[0].Expires = later.Add(-time.Minute) // 逾時
	s.Refresh(later, eligible, 1, seq(0.5))
	if len(s.Orders) != Slots || s.Streak != 0 || s.NextID != Slots+2 {
		t.Fatalf("%+v", s)
	}
}

func TestStateRoundTrip(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	var s State
	s.Refresh(now, map[string]int{"wheat": 10, "egg": 25}, 6, seq(0.7, 0.1, 0.9))
	s.RerollDay, s.Rerolls, s.Streak = "2026-11-20", 2, 4
	raw, err := json.Marshal(s.Encode())
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := Parse(back)
	if len(got.Orders) != len(s.Orders) || got.NextID != s.NextID || !got.RefillAt.Equal(s.RefillAt) ||
		got.Rerolls != 2 || got.Streak != 4 || got.RerollFeeToday("2026-11-20") != RerollFee || got.RerollFeeToday("2026-11-21") != 0 {
		t.Fatalf("%+v", got)
	}
	for i := range got.Orders {
		a, b := got.Orders[i], s.Orders[i]
		if a.ID != b.ID || a.NPC != b.NPC || a.Reward != b.Reward || !a.Expires.Equal(b.Expires) || len(a.Items) != len(b.Items) {
			t.Fatalf("%+v ≠ %+v", a, b)
		}
	}
	if got.Find(s.Orders[1].ID) != 1 || got.Find(999) != -1 {
		t.Fatal("Find")
	}
	if len(Parse(map[string]any{"orders": []any{map[string]any{"id": 1.0, "npc": "nobody"}}}).Orders) != 0 {
		t.Fatal("壞資料要略過")
	}
}
