package services

// 對應 Python tests/test_news.py 的服務層測試（規則測試在 game/news；面板總覽在第 4 階段）。

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/clock"
	newsrules "farmgm/internal/game/news"
)

var newsDay = time.Date(2026, 1, 1, 0, 0, 0, 0, clock.TaipeiTZ)

func (e *env) news(g int64, day time.Time) *DailyNews {
	e.t.Helper()
	d, err := e.n.DailyNews(e.ctx, g, day)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *env) farmAndSell(pids ...int64) {
	e.t.Helper()
	for _, pid := range pids {
		e.grow(pid, 1, "wheat")
	}
	e.clock.Advance(600)
	for _, pid := range pids {
		e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
		e.ok(e.s.SellCrop(e.ctx, pid, "wheat", 1, "normal", "none"))
	}
}

func TestDailyNewsOnlyReportsHomeGroup(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	a := e.farmer("阿明", home)
	c := e.farmer("隔壁老王", e.group("隔壁"))
	e.farmAndSell(a, c)
	if !e.news(home, time.Time{}).Empty {
		t.Fatal("今天的事要明天才上日報")
	}
	e.clock.Advance(24 * 3600)
	d := e.news(home, time.Time{})
	if !d.Day.Equal(newsDay) || d.Empty {
		t.Fatalf("%+v", d)
	}
	if !strings.Contains(d.Text, "阿明的農場") || strings.Contains(d.Text, "隔壁老王") {
		t.Fatal(d.Text)
	}
	if !e.news(home, newsDay.AddDate(0, 0, 1)).Empty {
		t.Fatal("今天還沒有動靜")
	}
}

func TestPastDaysAreCachedTodayIsNot(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	e.farmAndSell(e.farmer("阿明", home))
	today := e.news(home, newsDay) // 還沒結束的日子不快取
	key := newsKey{home, "2026-01-01"}
	newsCacheMu.Lock()
	_, cached := newsCache[key]
	newsCacheMu.Unlock()
	if cached || today.Empty {
		t.Fatalf("cached=%v %+v", cached, today)
	}
	e.clock.Advance(24 * 3600)
	first := e.news(home, time.Time{})
	newsCacheMu.Lock()
	got := newsCache[key]
	newsCacheMu.Unlock()
	if got != first || e.news(home, time.Time{}) != first {
		t.Fatal("已結束的日子要快取")
	}
}

// setupRumor 固定傳聞的主角是「地主」的農場，並讓地主的兩塊田成熟。
func setupRumor(t *testing.T, e *env, real bool) (int64, int64, int64, int64) {
	home := e.group("主群組")
	owner := e.farmer("地主", home)
	a, b := e.farmer("小偷甲", home), e.farmer("小偷乙", home)
	target := int64(e.scalar("SELECT id FROM farms WHERE player_id = $1", owner))
	old := makeRumor
	makeRumor = func(int64, time.Time, []int64) *newsrules.Rumor {
		return &newsrules.Rumor{Target: target, Real: real, Template: 0}
	}
	t.Cleanup(func() { makeRumor = old })
	e.grow(owner, 1, "carrot")
	e.grow(owner, 2, "carrot")
	e.clock.Advance(600)
	e.s.RumorRng = func() float64 { return 0 } // 一定挖到、金額取最小值
	return home, owner, a, b
}

func TestRumorTreasureFirstDiggerOnly(t *testing.T) {
	e := setup(t)
	home, owner, a, b := setupRumor(t, e, true)
	gold := e.gold(a)
	r := e.steal(home, a, owner, 1, 1)
	if !strings.Contains(r.Message, "傳聞是真的") || e.gold(a) != gold+newsrules.TreasureGoldLow {
		t.Fatalf("%s gold=%d", r.Message, e.gold(a))
	}
	r = e.steal(home, b, owner, 2, 1)
	if !strings.Contains(r.Message, "已經被別人拿走") {
		t.Fatal(r.Message)
	}
	if e.scalar("SELECT sum(gold) FROM farms") != e.scalar("SELECT sum(gold_delta) FROM game_transactions") {
		t.Fatal("金幣總和要等於交易紀錄總和")
	}
	e.clock.Advance(24 * 3600) // 隔天的日報報導挖到寶藏
	text := e.news(home, time.Time{}).Text
	if !strings.Contains(text, "挖到寶藏") || !strings.Contains(text, "小偷甲的農場") {
		t.Fatal(text)
	}
}

func TestFakeRumorFindsNothing(t *testing.T) {
	e := setup(t)
	home, owner, a, _ := setupRumor(t, e, false)
	gold := e.gold(a)
	r := e.steal(home, a, owner, 1, 1)
	if !strings.Contains(r.Message, "什麼都沒有") || e.gold(a) != gold {
		t.Fatalf("%s gold=%d", r.Message, e.gold(a))
	}
}
