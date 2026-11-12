package exploration

import (
	"encoding/json"
	"reflect"
	"testing"

	"farmgm/internal/game/featurecfg"
)

func roundTrip(t *testing.T, v featurecfg.Value) featurecfg.Value {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := featurecfg.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConfigDefaultsRoundTrip(t *testing.T) {
	featurecfg.Reset()
	t.Cleanup(featurecfg.Reset)
	def := featurecfg.Defaults(ConfigKey)
	got, err := featurecfg.Validate(ConfigKey, roundTrip(t, def))
	if err != nil {
		t.Fatalf("預設值驗證失敗：%v", err)
	}
	if !reflect.DeepEqual(roundTrip(t, got), roundTrip(t, def)) {
		t.Fatal("預設值驗證後內容改變")
	}
	s, err := parseConfig(roundTrip(t, def))
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range AreaOrder {
		if !reflect.DeepEqual(s.Areas[code].Pools, Areas[code].Pools) || s.Areas[code].Rolls != Areas[code].Rolls ||
			len(s.Areas[code].Events) != len(Areas[code].Events) || s.SawdustDrop[code] != SawdustDrop[code] {
			t.Fatalf("%s 解析後與預設不同", code)
		}
	}
}

func TestConfigApplied(t *testing.T) {
	featurecfg.Reset()
	t.Cleanup(featurecfg.Reset)
	v := roundTrip(t, featurecfg.Defaults(ConfigKey))
	areas := v["areas"].(map[string]any)
	g := areas["grassland"].(map[string]any)
	g["rolls"] = 7
	g["sawdust"] = []any{0, 0}
	g["pools"] = map[string]any{"common": []any{map[string]any{"item": "crystal", "min_level": 1, "weight": 5, "qty": []any{9, 9}}}}
	v["fake_coin_value"] = 250
	v["reward_types"].(map[string]any)["normal"] = map[string]any{"common": 10000, "rare": 0, "seed": 0, "fake_coin": 0, "event": 0}
	norm, err := featurecfg.Validate(ConfigKey, roundTrip(t, v))
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, norm)
	if FakeCoinRate() != 250 || AreaOf("grassland").Rolls != 7 {
		t.Fatalf("設定沒有生效：%d %d", FakeCoinRate(), AreaOf("grassland").Rolls)
	}
	r := Explore("grassland", 1, func() float64 { return 0.5 }, 0)
	RollSawdust(r, func() float64 { return 0.5 })
	if r.Items["crystal"] != 63 || r.Items[Sawdust] != 0 || len(r.Items) != 1 {
		t.Fatalf("探索結果不符合設定：%v", r.Items)
	}
	if PossibleItems(AreaOf("grassland"), nil)[Sawdust] {
		t.Fatal("木屑為 0 時不應列為可能取得")
	}
	featurecfg.SetCurrent(ConfigKey, nil)
	if FakeCoinRate() != FakeCoinValue || AreaOf("grassland").Rolls != Areas["grassland"].Rolls {
		t.Fatal("還原預設值後沒有回到預設")
	}
}

func TestConfigValidation(t *testing.T) {
	featurecfg.Reset()
	t.Cleanup(featurecfg.Reset)
	cases := map[string]func(v featurecfg.Value){
		"機率合計不是 100%": func(v featurecfg.Value) {
			v["reward_types"].(map[string]any)["advanced"].(map[string]any)["common"] = 1
		},
		"未知物品": func(v featurecfg.Value) {
			v["areas"].(map[string]any)["mine"].(map[string]any)["pools"].(map[string]any)["rare"] =
				[]any{map[string]any{"item": "gold_bar", "min_level": 1, "weight": 1, "qty": []any{1, 1}}}
		},
		"數量範圍顛倒": func(v featurecfg.Value) {
			v["areas"].(map[string]any)["forest"].(map[string]any)["sawdust"] = []any{4, 3}
		},
		"缺少區域": func(v featurecfg.Value) { delete(v["areas"].(map[string]any), "ruins") },
		"額外抽獎不能是事件": func(v featurecfg.Value) {
			ev := v["areas"].(map[string]any)["grassland"].(map[string]any)["events"].([]any)[0].(map[string]any)
			ev["bonus"] = []any{"event"}
		},
	}
	for name, mutate := range cases {
		v := roundTrip(t, featurecfg.Defaults(ConfigKey))
		mutate(v)
		if _, err := featurecfg.Validate(ConfigKey, v); err == nil {
			t.Errorf("%s：應該驗證失敗", name)
		}
	}
}
