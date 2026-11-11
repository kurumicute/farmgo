package news

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"farmgm/internal/clock"
)

var day = time.Date(2026, 1, 1, 0, 0, 0, 0, clock.TaipeiTZ)

func TestPyRandomMatchesCPython(t *testing.T) {
	// 數值取自 CPython 3.12：random.Random("rumor-1-2026-01-01") 等
	r := newPyRandom("rumor-1-2026-01-01")
	if a, b, c, d := r.random(), r.randbelow(3), r.random(), r.randbelow(3); a != 0.34687568668141744 || b != 0 || c != 0.9371715480819546 || d != 1 {
		t.Fatal(a, b, c, d)
	}
	r = newPyRandom("2026-01-01-1")
	want := []int{2, 1, 2, 2, 731, 3}
	for i, n := range []int{3, 3, 3, 7, 1000, 5} {
		if got := r.randbelow(n); got != want[i] {
			t.Fatalf("randrange #%d: got %d want %d", i, got, want[i])
		}
	}
	if got := newPyRandom("").random(); got != 0.9602256525641875 {
		t.Fatal(got)
	}
}

func TestRulesDayRange(t *testing.T) {
	if got := NewsDay(time.Date(2026, 1, 2, 0, 1, 0, 0, clock.TaipeiTZ)); !got.Equal(day) {
		t.Fatal(got)
	}
	start, end := DayRange(day)
	if !start.Equal(day) || end.Sub(start) != 24*time.Hour {
		t.Fatal(start, end)
	}
}

func mainData() DayData {
	return DayData{
		Events: []Event{{1, "sell", 3, 30, 900}, {2, "fish_sell", 1, 1, 100}, {2, "stolen", 3, 3, 0}, {1, "steal", 3, 3, 0},
			{1, "harvest", 2, 40, 0}, {2, "defended", 1, 0, 0}, {1, "gamble", 4, 4, 0}},
		Special:   []Special{{2, "wheat", "legendary", "golden", 2}},
		Fish:      &Fish{1, "黃金鯉魚", "傳說", 3500},
		Names:     map[int64]string{1: "**粗體** @everyone", 2: "小華"},
		ItemNames: map[string]string{"wheat": "小麥"},
		Ruins:     &Ruins{Donated: 12, Percent: 40},
	}
}

func TestBuildHeadlinesAndEscapedNames(t *testing.T) {
	n := Build(day, mainData(), "1")
	text := Render(n)
	if n.Empty() || len(n.Headlines) != MaxHeadlines || utf8.RuneCountInString(text) > FieldLimit {
		t.Fatal(n)
	}
	for _, s := range []string{"\\*\\*粗體\\*\\*", "@\u200beveryone", "**900** 金", "傳說品質、黃金作物的小麥", "被偷 **3** 次"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in %s", s, text)
		}
	}
	if strings.Contains(text, "@everyone") || !strings.HasSuffix(text, n.Summary) ||
		!strings.Contains(n.Summary, "收成 40 個作物") || !strings.Contains(n.Summary, "銷售 1,000 金") {
		t.Fatal(text)
	}
	again := Build(day, mainData(), "1")
	if strings.Join(again.Headlines, "\n") != strings.Join(n.Headlines, "\n") {
		t.Fatal("同一天同一群組措辭應固定")
	}
}

func TestEmptyAndQuietDays(t *testing.T) {
	if !Build(day, DayData{}, "").Empty() {
		t.Fatal("should be empty")
	}
	quiet := Build(day, DayData{Events: []Event{{1, "harvest", 1, 3, 0}}, Names: map[int64]string{1: "阿明"}}, "")
	if quiet.Empty() || !strings.Contains(Render(quiet), "默默耕耘") {
		t.Fatal(Render(quiet))
	}
}

func TestRenderRespectsFieldLimit(t *testing.T) {
	long := News{Day: day, Headlines: []string{"🌟 " + strings.Repeat("很長", 600), "短的頭條"}, Summary: "摘要"}
	text := Render(long)
	if utf8.RuneCountInString(text) > FieldLimit || strings.Contains(text, "很長") || !strings.Contains(text, "短的頭條") || !strings.Contains(text, "摘要") {
		t.Fatal(text)
	}
}

func TestRumorHeadlineIsLastAndDeterministic(t *testing.T) {
	if MakeRumor(1, day, nil) != nil {
		t.Fatal("no candidates → nil")
	}
	var none, real, fake bool
	for i := 0; i < 40; i++ {
		p := MakeRumor(1, day.AddDate(0, 0, i), []int64{5, 3, 9})
		switch {
		case p == nil:
			none = true
		case p.Real:
			real = true
		default:
			fake = true
		}
		if p != nil && p.Target != 3 && p.Target != 5 && p.Target != 9 {
			t.Fatal(p)
		}
	}
	if !none || !real || !fake {
		t.Fatal(none, real, fake)
	}
	a, b := MakeRumor(1, day, []int64{9, 5, 3}), MakeRumor(1, day, []int64{3, 5, 9})
	if (a == nil) != (b == nil) || (a != nil && *a != *b) {
		t.Fatal(a, b)
	}
	n := Build(day, DayData{Events: []Event{{1, "sell", 1, 1, 50}}, Names: map[int64]string{1: "阿明"}, Rumor: &Rumor{1, true, 0}}, "")
	last := n.Headlines[len(n.Headlines)-1]
	if !strings.HasPrefix(last, "🗺️ 傳聞") || !strings.Contains(last, "**阿明**") {
		t.Fatal(last)
	}
}

// TestMatchesPythonReference 與 testdata/python_reference.json（由 Python 版實際執行產生）逐字比對。
func TestMatchesPythonReference(t *testing.T) {
	raw, err := os.ReadFile("testdata/python_reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Builds []struct {
			Name      string `json:"name"`
			DayOffset int    `json:"day_offset"`
			Seed      string `json:"seed"`
			Text      string `json:"text"`
		} `json:"builds"`
		Rumors []struct {
			Group     int64 `json:"group"`
			DayOffset int   `json:"day_offset"`
			Rumor     []any `json:"rumor"`
		} `json:"rumors"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	datasets := map[string]DayData{
		"main": mainData(),
		"second": {Events: []Event{{1, "gamble", 2, 2, 0}, {3, "steal", 4, 4, 0}, {1, "rumor_treasure", 1, 0, 512}, {2, "sell", 1, 1, 1234567}},
			Names: map[int64]string{1: "A", 2: "B"}, Fish: &Fish{2, "鯉", "普通", 1250}},
		"third": {Events: []Event{{4, "defended", 2, 0, 0}, {5, "gamble", 9, 9, 0}, {4, "ruins_donate", 1, 5, 0}},
			Names: map[int64]string{4: "甲"}, Fish: &Fish{4, "鱒", "稀有", 1250},
			Ruins: &Ruins{Donated: 5, Percent: 66, GuardianDefeatedToday: true}, Rumor: &Rumor{4, false, 2}},
	}
	for _, c := range ref.Builds {
		got := Render(Build(day.AddDate(0, 0, c.DayOffset), datasets[c.Name], c.Seed))
		if got != c.Text {
			t.Errorf("%s/%s:\n got  %q\n want %q", c.Name, c.Seed, got, c.Text)
		}
	}
	for _, c := range ref.Rumors {
		got := MakeRumor(c.Group, day.AddDate(0, 0, c.DayOffset), []int64{5, 3, 9, 11})
		if c.Rumor == nil {
			if got != nil {
				t.Errorf("group %d day %d: got %v want nil", c.Group, c.DayOffset, got)
			}
			continue
		}
		want := Rumor{int64(c.Rumor[0].(float64)), c.Rumor[1].(bool), int(c.Rumor[2].(float64))}
		if got == nil || *got != want {
			t.Errorf("group %d day %d: got %v want %v", c.Group, c.DayOffset, got, want)
		}
	}
}

func TestTreasureGold(t *testing.T) {
	if TreasureGold(0) != 300 || TreasureGold(0.999999) != 800 {
		t.Fatal(TreasureGold(0.999999))
	}
}
