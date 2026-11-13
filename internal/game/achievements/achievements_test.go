package achievements

import (
	"strings"
	"testing"

	"farmgm/internal/game/featurecfg"
)

func TestDefinitionsAreConsistent(t *testing.T) {
	featurecfg.Reset()
	if n := len(Active()); n < 30 || n > 50 { // Python 33 個＋實驗室與菇房 10 個
		t.Fatal(n)
	}
	for _, a := range Active() {
		if a.Description() == "" || RewardText(a) == "無" {
			t.Fatal(a.Code)
		}
	}
	a := ByCode()["crops_10000"]
	if a.Description() != "取得作物達到 10,000 個" || RewardText(a) != "2,000 金幣、人工養殖蚯蚓 ×3、稱號「農業大亨」" {
		t.Fatal(a.Description(), RewardText(a))
	}
	if ByCode()["legendary_crop"].Description() != "取得傳說品質作物" {
		t.Fatal("legendary description")
	}
	if !Reached(a, map[string]int{"crops": 10_000}) || Reached(a, map[string]int{}) {
		t.Fatal("reached")
	}
}

// 對應 tests/test_progress_admin.py::test_achievement_overrides 的設定部分（不經資料庫）。
func TestAchievementOverrides(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	raw := featurecfg.Value{"items": map[string]any{
		"crops_100": map[string]any{"target": 1, "gold": 77, "items": map[string]any{}, "title": "第一顆"},
		"steal_20":  Editable(BaseByCode["steal_20"]), // 和預設一樣：不存
	}}
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := featurecfg.AsMap(v["items"])
	if len(stored) != 1 || stored["crops_100"] == nil {
		t.Fatal(stored)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	a := ByCode()["crops_100"]
	if a.Target != 1 || a.Gold != 77 || a.Title != "第一顆" || !Titles()["第一顆"] {
		t.Fatal(a)
	}
	for js, want := range map[string]string{
		`{"items":{"crops_100":{"title":"神偷"}}}`:    "不可重複",
		`{"items":{"codex_all":{"target":101}}}`:    "門檻",
		`{"items":{"nope":{}}}`:                     "沒有「nope」",
		`{"items":[]}`:                              "格式不正確",
		`{"items":{"crops_100":{"items":{"x":1}}}}`: "沒有「x」這種道具",
	} {
		r, _ := featurecfg.Decode([]byte(js))
		if _, err := featurecfg.Validate(ConfigKey, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	// 只改部分欄位：其餘沿用預設；清空稱號
	r, _ := featurecfg.Decode([]byte(`{"items":{"crops_1000":{"title":""}}}`))
	v, err = featurecfg.Validate(ConfigKey, r)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	b := ByCode()["crops_1000"]
	if b.Title != "" || b.Target != 1_000 || b.Items["fertilizer"] != 10 {
		t.Fatal(b)
	}
}
