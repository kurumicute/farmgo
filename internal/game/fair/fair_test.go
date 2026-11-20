package fair

import (
	"slices"
	"testing"
	"time"

	"farmgm/internal/clock"
)

func TestScheduleAndRotation(t *testing.T) {
	mon := time.Date(2026, 11, 16, 0, 0, 0, 0, clock.TaipeiTZ)
	if !WeekStart(mon.Add(6*24*time.Hour+23*time.Hour)).Equal(mon) || WeekKey(mon) != "2026-11-16" {
		t.Fatal("WeekStart")
	}
	if !Open(mon.Add(5*24*time.Hour+23*time.Hour)) || Open(mon.Add(6*24*time.Hour)) {
		t.Fatal("週六開放、週日截止")
	}
	if !ResultsAt(mon.Add(time.Hour)).Equal(mon.Add(6*24*time.Hour + 20*time.Hour)) {
		t.Fatal("週日 20:00 公布")
	}
	seen := map[string]bool{}
	for w := range 3 {
		at := mon.AddDate(0, 0, 7*w)
		cat := Category(at)
		seen[cat] = true
		if Category(at.Add(100*time.Hour)) != cat {
			t.Fatal("同一週組別相同")
		}
		if !slices.Contains(WeekPref(at).Categories, cat) {
			t.Fatalf("%s 週的偏好 %s 不適用", cat, WeekPref(at).Code)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("三週要輪完三組：%v", seen)
	}
}

func TestJudgeScore(t *testing.T) {
	pref := Prefs[0] // giant +30%
	if JudgeScore(100, Item{Mutation: "giant"}, pref) != 130 || JudgeScore(100, Item{Mutation: "none"}, pref) != 100 {
		t.Fatal("偏好加成")
	}
}

func TestRank(t *testing.T) {
	t0 := time.Now()
	es := []Entry{
		{ID: 1, Judge: 500, Votes: 0, CreatedAt: t0},
		{ID: 2, Judge: 400, Votes: 5, CreatedAt: t0.Add(time.Second)},
		{ID: 3, Judge: 100, Votes: 9, CreatedAt: t0.Add(2 * time.Second)},
	}
	// 評審分：1→21、2→14、3→7；人氣分：3→9、2→6、1→3；合計 1=24、2=20、3=16
	if got := Rank(es); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatal(got)
	}
	// 同分時評審分數高的在前
	es2 := []Entry{{ID: 1, Judge: 100, Votes: 3, CreatedAt: t0}, {ID: 2, Judge: 200, Votes: 0, CreatedAt: t0}}
	// 1：評審 7＋人氣 6 = 13；2：評審 14＋人氣 3 = 17
	if got := Rank(es2); !slices.Equal(got, []int64{2, 1}) {
		t.Fatal(got)
	}
	// 評審全部同分：不因報名先後拿不同的分數，人氣高的贏；其餘同分時先報名的在前
	es3 := []Entry{{ID: 1, Judge: 30, Votes: 0, CreatedAt: t0}, {ID: 2, Judge: 30, Votes: 2, CreatedAt: t0.Add(time.Second)},
		{ID: 3, Judge: 30, Votes: 0, CreatedAt: t0.Add(2 * time.Second)}}
	if got := Rank(es3); !slices.Equal(got, []int64{2, 1, 3}) {
		t.Fatal(got)
	}
	if p := Prizes(600); !slices.Equal(p, []int{450, 270, 180}) {
		t.Fatal(p)
	}
}
