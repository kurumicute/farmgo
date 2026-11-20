package services

import (
	"testing"

	"farmgm/internal/game/fair"
	"farmgm/internal/game/gameerr"
)

// fairItem 依本週組別給一件可以報名的作品，回傳 (代碼, 品質, 突變)。
func (e *env) fairItem(pid int64) (string, string, string) {
	e.t.Helper()
	code := map[string]string{fair.CatCrop: "wheat", fair.CatGoods: "flour", fair.CatLab: "amber_melon"}[fair.Category(e.clock.Now())]
	e.give(pid, code, 1)
	return code, "normal", "none"
}

// 品評會（2026-11-20，Migration 0048）：報名、投票、結算名次與獎金、不足 3 件流會退費。
func TestFair(t *testing.T) {
	e := setup(t)
	g := e.group("主群組")
	a, b, c := e.farmer("阿明", g), e.farmer("小美", g), e.farmer("阿華", g)
	lone := e.farmer("阿德", e.group("第二群組"))
	for _, pid := range []int64{a, b, c, lone} {
		e.setGoldRecorded(pid, 10_000)
		code, q, m := e.fairItem(pid)
		e.ok(e.s.EnterFair(e.ctx, pid, code, q, m))
	}
	code, q, m := e.fairItem(a)
	_, err := e.s.EnterFair(e.ctx, a, code, q, m)
	wantKind(t, err, gameerr.AlreadyExists)

	v, err := e.s.Fair(e.ctx, a)
	if err != nil || len(v.Entries) != 3 || v.Mine == nil || v.VotesLeft != fair.VotesPerWeek {
		t.Fatalf("%+v %v", v, err)
	}
	var bEntry, aEntry int64
	for _, x := range v.Entries {
		if x.Mine {
			aEntry = x.ID
		} else if x.PlayerName == "小美" {
			bEntry = x.ID
		}
	}
	_, err = e.s.VoteFair(e.ctx, a, aEntry)
	wantMsg(t, err, gameerr.Validation, "自己")
	e.ok(e.s.VoteFair(e.ctx, a, bEntry))
	_, err = e.s.VoteFair(e.ctx, a, bEntry)
	wantKind(t, err, gameerr.AlreadyExists)
	e.ok(e.s.VoteFair(e.ctx, c, bEntry))

	// 還沒到公布時間不結算
	if n, err := e.s.SettleDueFairs(e.ctx); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	now := e.clock.Now()
	e.clock.Advance(fair.ResultsAt(now).Sub(now).Seconds() + 60)
	if n, err := e.s.SettleDueFairs(e.ctx); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	// 主群組：3 件、獎池 900 → 450／270／180，同分時評審分數相同就看人氣
	if n := e.scalar("SELECT count(*)::int FROM fair_entries WHERE status = 'settled' AND rank IS NOT NULL"); n != 3 {
		t.Fatal(n)
	}
	if r := e.scalar("SELECT reward FROM fair_entries WHERE id = $1", bEntry); r != 450 {
		t.Fatalf("得票最多（評審同分）應為第 1 名，獎金 %d", r)
	}
	// 第二群組只有 1 件：流會，退回報名費與作品
	if n := e.scalar("SELECT count(*)::int FROM fair_entries WHERE status = 'refunded'"); n != 1 || e.gold(lone) != 10_000 {
		t.Fatal(n, e.gold(lone))
	}
	if n, _ := e.s.SettleDueFairs(e.ctx); n != 0 {
		t.Fatal("不能重複結算")
	}
	e.invariant()
}
