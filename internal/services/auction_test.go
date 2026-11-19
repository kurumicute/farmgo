package services

import (
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/achievements"
	"farmgm/internal/game/auction"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/reputation"
	"farmgm/internal/game/specialshop"
)

// 每週拍賣（2026-11-19）：出價凍結、被超過退回、自己加價只扣差額、結標前延長、結標發獎不重複、日報新聞。
func TestWeeklyAuction(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	a, b := e.farmer("阿明", home), e.farmer("小美", home)
	e.setGoldRecorded(a, 1_000_000)
	e.setGoldRecorded(b, 1_000_000)
	v := must[AuctionView](t)(e.s.Auction(e.ctx, a))
	if !v.Open || v.MinBid != 50_000 || v.TopBid != nil {
		t.Fatalf("%+v", v)
	}
	e.ok(e.s.PlaceBid(e.ctx, a, 50_000))
	_, err := e.s.PlaceBid(e.ctx, b, 52_499)
	wantMsg(t, err, gameerr.Validation, "至少要 52,500")
	e.ok(e.s.PlaceBid(e.ctx, b, 52_500))
	if e.gold(a) != 1_000_000 || e.gold(b) != 1_000_000-52_500 {
		t.Fatal("被超過要全數退回", e.gold(a), e.gold(b))
	}
	if v := must[AuctionView](t)(e.s.Auction(e.ctx, a)); v.Outbid != 50_000 || v.Mine {
		t.Fatalf("%+v", v)
	}
	e.ok(e.s.PlaceBid(e.ctx, a, 60_000))
	if r := e.ok(e.s.PlaceBid(e.ctx, a, 70_000)); r.GoldDelta != -10_000 { // 自己加價只扣差額
		t.Fatal(r.GoldDelta)
	}
	if e.gold(a) != 1_000_000-70_000 || e.gold(b) != 1_000_000 {
		t.Fatal(e.gold(a), e.gold(b))
	}
	// 結標前 2 分鐘出價：延後 5 分鐘
	v = must[AuctionView](t)(e.s.Auction(e.ctx, b))
	end := v.EndsAt
	e.clock.Advance(end.Sub(e.clock.Now()).Seconds() - 120)
	if r := e.ok(e.s.PlaceBid(e.ctx, b, 80_000)); !strings.Contains(r.Message, "延後") {
		t.Fatal(r.Message)
	}
	if v := must[AuctionView](t)(e.s.Auction(e.ctx, b)); !v.EndsAt.Equal(end.Add(5*time.Minute)) || !v.Mine {
		t.Fatalf("%+v", v)
	}
	e.clock.Advance(4 * 60) // 原本的結標時間過了，但延長了：還不能結算
	if n := must[int](t)(e.s.SettleAuctions(e.ctx)); n != 0 {
		t.Fatal(n)
	}
	e.clock.Advance(200) // 延長後的結標時間（原本 +5 分鐘）也過了
	if n := must[int](t)(e.s.SettleAuctions(e.ctx)); n != 1 {
		t.Fatal(n)
	}
	if n := must[int](t)(e.s.SettleAuctions(e.ctx)); n != 0 {
		t.Fatal("不能重複結算", n)
	}
	item := auction.ItemOf(auction.WeekStart(end))
	if has := e.scalar("SELECT count(*) FROM auction_bids"); has != 5 {
		t.Fatal(has)
	}
	if n := e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'auction_win' AND gold_delta = 0"); n != 1 {
		t.Fatal(n)
	}
	if e.gold(b) != 1_000_000-80_000 || e.gold(a) != 1_000_000 {
		t.Fatal("得標金額回收、落標的全數退回", e.gold(a), e.gold(b))
	}
	// 得標者擁有拍賣品；日報有新聞
	var owned int
	switch item.Kind {
	case auction.KindDecor:
		owned = e.scalar("SELECT count(*) FROM farm_decorations d JOIN farms f ON f.id = d.farm_id WHERE f.player_id = $1 AND d.decor_code = $2", b, item.Code)
	case auction.KindBadge:
		owned = e.scalar("SELECT count(*) FROM farm_cosmetics c JOIN farms f ON f.id = c.farm_id WHERE f.player_id = $1 AND c.code = $2", b, item.Code)
	default:
		owned = e.scalar("SELECT count(*) FROM farm_titles c JOIN farms f ON f.id = c.farm_id WHERE f.player_id = $1 AND c.title = $2", b, item.Code)
	}
	if owned != 1 {
		t.Fatal("得標者要拿到拍賣品", item)
	}
	if v := must[AuctionView](t)(e.s.Auction(e.ctx, a)); v.Open || v.LastWin != "小美的農場" || v.LastPrice != 80_000 {
		t.Fatalf("%+v", v)
	}
	e.clock.Advance(24 * 3600) // 週一：日報＋新的一場
	if d := e.news(home, time.Time{}); !strings.Contains(d.Text, "以 **80,000** 金標下本週拍賣品") {
		t.Fatal(d.Text)
	}
	if v := must[AuctionView](t)(e.s.Auction(e.ctx, a)); !v.Open || v.TopBid != nil || v.MinBid != 50_000 {
		t.Fatalf("%+v", v)
	}
	e.invariant()
}

// 沒人出價就流標；拍賣稱號不可和其他稱號重複。
func TestAuctionUnsoldAndTitles(t *testing.T) {
	e := setup(t)
	pid := e.player()
	v := must[AuctionView](t)(e.s.Auction(e.ctx, pid))
	e.clock.Advance(v.EndsAt.Sub(e.clock.Now()).Seconds() + 1)
	if n := must[int](t)(e.s.SettleAuctions(e.ctx)); n != 1 {
		t.Fatal(n)
	}
	if s := e.scalar("SELECT count(*) FROM auctions WHERE status = 'unsold'"); s != 1 {
		t.Fatal(s)
	}
	_, err := e.s.PlaceBid(e.ctx, pid, 100_000)
	wantMsg(t, err, gameerr.Validation, "目前沒有進行中的拍賣")
	used := map[string]bool{}
	for _, o := range specialshop.TitleShop() {
		used[o.Title] = true
	}
	for _, a := range achievements.BaseAchievements {
		used[a.Title] = true
	}
	for _, title := range reputation.Titles() {
		used[title] = true
	}
	for _, it := range auction.Current().Items {
		if _, ok := auctionItemName(it.Kind, it.Code); !ok || (it.Kind == auction.KindTitle && used[it.Code]) {
			t.Fatal(it)
		}
	}
}
