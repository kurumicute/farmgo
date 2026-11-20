package questboard

import (
	"testing"
	"time"

	"farmgm/i18n"
	"farmgm/internal/clock"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/events"
)

func TestPickOnePerTierStable(t *testing.T) {
	week := WeekStart(time.Date(2026, 11, 20, 12, 0, 0, 0, clock.TaipeiTZ))
	if week.Weekday() != time.Monday || week.Day() != 16 {
		t.Fatal(week)
	}
	a, b := Pick(7, week), Pick(7, week.Add(48*time.Hour))
	if len(a) != 3 {
		t.Fatal(a)
	}
	for i, tier := range Tiers {
		if a[i].Tier != tier || a[i] != b[i] {
			t.Fatalf("%v %v", a, b)
		}
		if _, ok := balance.QuestTypes[a[i].QuestType]; !ok {
			t.Fatal(a[i].QuestType)
		}
	}
}

func TestTemplatesValid(t *testing.T) {
	for tier, list := range Templates {
		for _, tp := range list {
			qt, ok := balance.QuestTypes[tp.QuestType]
			if !ok || tp.Tier != tier {
				t.Fatalf("%+v", tp)
			}
			found := false
			for _, m := range qt.Metrics {
				found = found || m == tp.Metric
			}
			if !found {
				t.Fatalf("%s 不支援 %s", tp.QuestType, tp.Metric)
			}
			if tp.Target(0) != tp.PerPlayer || tp.Pool(10) != tp.PoolPerPlayer*10 || tp.Target(1<<30) != MaxValue {
				t.Fatalf("%+v", tp)
			}
		}
	}
}

func TestTierAndBuffs(t *testing.T) {
	if tier, ok := TierOf("questboard:hard"); !ok || tier != "hard" {
		t.Fatal(tier)
	}
	if _, ok := TierOf("admin"); ok {
		t.Fatal("管理員建立的任務不是委託")
	}
	if Weighted(10, NewbieLevel) != 20 || Weighted(10, NewbieLevel+1) != 10 {
		t.Fatal("新手加倍")
	}
	evs := []*events.Event{Buffs["easy"].Event("easy"), Buffs["normal"].Event("normal"), Buffs["hard"].Event("hard")}
	if events.BonusOf(evs, "grow_reduction_pct", nil).Value != 5 || events.BonusOf(evs, "sell_price_pct", nil).Value != 5 ||
		events.BonusOf(evs, "mutation_bonus_bp", nil).Value != 50 {
		t.Fatal("增益數值")
	}
}

// 委託標題是組合字串（玩家看到的任務名稱），英文表要有完整的一句；增益說明也要翻。
func TestTitlesTranslated(t *testing.T) {
	en := i18n.Texts(i18n.English)
	for _, tier := range Tiers {
		for _, tp := range Templates[tier] {
			if _, ok := en[tp.Title()]; !ok {
				t.Errorf("en.text.json 缺少 %q", tp.Title())
			}
		}
		if _, ok := en[Buffs[tier].Text]; !ok {
			t.Errorf("en.text.json 缺少 %q", Buffs[tier].Text)
		}
	}
}
