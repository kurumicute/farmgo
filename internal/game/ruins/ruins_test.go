package ruins

import (
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"farmgm/internal/game/exploration"
)

func TestRulesProgressAndRemaining(t *testing.T) {
	if Completed != 3 || StageInfo(3) != nil {
		t.Fatal("completed")
	}
	if Percent(0, nil) != 0 || Percent(3, nil) != 100 {
		t.Fatal("percent")
	}
	if !reflect.DeepEqual(Remaining(0, map[string]int{"stone": 100}), map[string]int{"stone": 50, "wood": 150}) {
		t.Fatal(Remaining(0, map[string]int{"stone": 100}))
	}
	if Percent(1, nil) != 33 || StageDone(0, map[string]int{"stone": 150}) {
		t.Fatal("percent 1")
	}
	if !StageDone(0, map[string]int{"stone": 150, "wood": 999}) {
		t.Fatal("stage done")
	}
	if utf8.RuneCountInString(Title) > 20 {
		t.Fatal("title")
	}
	for _, st := range Stages {
		for m := range st.Materials {
			if _, ok := exploration.Materials[m]; !ok || m == exploration.FakeCoin {
				t.Fatal(m)
			}
		}
		if len(st.MaterialOrder) != len(st.Materials) {
			t.Fatal("order")
		}
	}
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestGuardianRules(t *testing.T) {
	done := utc(2026, 1, 1, 0, 0) // 台灣週四
	if !WeekStart(done).Equal(Date(2025, 12, 29)) || !FirstGuardianWeek(done).Equal(Date(2026, 1, 5)) {
		t.Fatal(WeekStart(done), FirstGuardianWeek(done))
	}
	if GuardianActive(&done, utc(2026, 1, 4, 15, 59)) { // 台灣週日 23:59
		t.Fatal("active too early")
	}
	if !GuardianActive(&done, utc(2026, 1, 4, 16, 0)) {
		t.Fatal("not active")
	}
	if GuardianActive(nil, utc(2026, 2, 1, 0, 0)) {
		t.Fatal("nil completed")
	}
	got := WeeksToSettle(done, nil, utc(2026, 1, 12, 0, 0))
	if len(got) != 1 || !got[0].Equal(Date(2026, 1, 5)) {
		t.Fatal(got)
	}
	settled := Date(2026, 1, 5)
	got = WeeksToSettle(done, &settled, utc(2026, 1, 19, 0, 0))
	if len(got) != 1 || !got[0].Equal(Date(2026, 1, 12)) {
		t.Fatal(got)
	}
	// 資料庫 DATE 讀回時可能是 UTC 00:00，結果應相同
	settledUTC := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	got = WeeksToSettle(done, &settledUTC, utc(2026, 1, 19, 0, 0))
	if len(got) != 1 || !got[0].Equal(Date(2026, 1, 12)) {
		t.Fatal(got)
	}
	if GuardianHP(1) != 4000 || GuardianHP(10_000) != GuardianHPMax || GuardianHPMax != 40000 {
		t.Fatal("hp")
	}
	if d, ok := MaterialDamage("stone"); !ok || d != 3 {
		t.Fatal("stone")
	}
	if d, ok := MaterialDamage("machine_core"); !ok || d != 80 {
		t.Fatal("machine_core")
	}
	if _, ok := MaterialDamage(exploration.FakeCoin); ok {
		t.Fatal("fake coin")
	}
	if _, ok := MaterialDamage("wheat"); ok {
		t.Fatal("wheat")
	}
	if !WeekEnd(Date(2026, 1, 5)).Equal(Date(2026, 1, 12)) {
		t.Fatal("week end")
	}
}
