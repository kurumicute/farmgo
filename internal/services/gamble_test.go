package services

// 對應 Python tests/test_gamble.py：比大小（×2）、猜數字（×5）、只限稀有以上或特殊突變的作物、獎品存放與賣出。

import (
	"slices"
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
)

// casino 固定擲出的數字（Python 測試的 AdminService(dice=lambda: roll)）。
func (e *env) casino(roll int) { e.s.dice = func() int { return roll } }

func (e *env) gamble(pid int64, code, quality, mutation string, qty int, mode string, guess *int) (GambleResult, error) {
	return e.a.Gamble(e.ctx, pid, code, quality, mutation, qty, mode, guess)
}

func (e *env) prizes(pid int64) []PrizeView {
	e.t.Helper()
	return must[[]PrizeView](e.t)(e.a.ListPrizes(e.ctx, pid))
}

func TestGambleOnlyRareOrSpecialCrops(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setInv(pid, "wheat", "normal", "none", 5)
	e.setInv(pid, "wheat", "good", "none", 5)
	e.setInv(pid, "carrot", "normal", "golden", 2)
	e.setInv(pid, "amber_wheat", "normal", "variant", 1)
	var names []string
	for _, o := range must[[]GambleOption](t)(e.a.GambleOptions(e.ctx, pid)) {
		names = append(names, o.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"琥珀小麥", "胡蘿蔔【黃金作物】"}) {
		t.Fatal(names)
	}
	e.casino(7)
	_, err := e.gamble(pid, "wheat", "good", "none", 1, "big", nil)
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "只能用") {
		t.Fatal(err)
	}
}

func TestGambleBigWinMovesCropsToPrizes(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setInv(pid, "wheat", "rare", "none", 5)
	used := must[Backpack](t)(e.a.GetBackpack(e.ctx, pid)).Used
	e.casino(7)
	r := must[GambleResult](t)(e.gamble(pid, "wheat", "rare", "none", 3, "big", nil))
	if !r.Won || r.Roll != 7 || r.Multiplier != 2 {
		t.Fatalf("%+v", r)
	}
	bp := must[Backpack](t)(e.a.GetBackpack(e.ctx, pid))
	if len(bp.Produce) != 1 || bp.Produce[0].Quantity != 2 || bp.Produce[0].Quality != "rare" {
		t.Fatalf("%+v", bp.Produce)
	}
	if len(bp.Prizes) != 1 || bp.Prizes[0].Quantity != 3 || bp.Prizes[0].Multiplier != 2 || bp.Prizes[0].UnitPrice != 22*2 {
		t.Fatalf("%+v", bp.Prizes)
	}
	if bp.Used != used { // 獎品仍佔倉庫空間，總數不變
		t.Fatal(bp.Used, used)
	}
	if e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'gamble'") != 1 { // Python .one()
		t.Fatal("賭場交易紀錄應只有一筆")
	}
	desc := e.text("SELECT description FROM game_transactions WHERE event_type = 'gamble'")
	if !strings.Contains(desc, "擲出 7") || e.scalar("SELECT gold_delta FROM game_transactions WHERE event_type = 'gamble'") != 0 {
		t.Fatal(desc)
	}
}

func TestGambleSmallLossTakesCrops(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setInv(pid, "wheat", "rare", "none", 5)
	e.casino(7)
	r := must[GambleResult](t)(e.gamble(pid, "wheat", "rare", "none", 5, "small", nil))
	if r.Won || !strings.Contains(r.Message, "輸了") {
		t.Fatalf("%+v", r)
	}
	bp := must[Backpack](t)(e.a.GetBackpack(e.ctx, pid))
	if len(bp.Produce) != 0 || len(bp.Prizes) != 0 {
		t.Fatalf("%+v %+v", bp.Produce, bp.Prizes)
	}
	e.invariant()
}

func TestGambleExactGuessPaysFiveTimesAndValidates(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.setInv(pid, "carrot", "normal", "golden", 2)
	e.casino(3)
	g := func(n int) *int { return &n }
	_, err := e.gamble(pid, "carrot", "normal", "golden", 1, "exact", g(11))
	wantKind(t, err, gameerr.Validation)
	if r := must[GambleResult](t)(e.gamble(pid, "carrot", "normal", "golden", 1, "exact", g(4))); r.Won {
		t.Fatalf("%+v", r)
	}
	if r := must[GambleResult](t)(e.gamble(pid, "carrot", "normal", "golden", 1, "exact", g(3))); r.Multiplier != 5 {
		t.Fatalf("%+v", r)
	}
	if p := e.prizes(pid)[0]; p.Multiplier != 5 || p.Quantity != 1 {
		t.Fatalf("%+v", p)
	}
	_, err = e.gamble(pid, "carrot", "normal", "golden", 1, "big", nil)
	wantKind(t, err, gameerr.InsufficientItems)
}

func TestSellPrizePriceAndOwnership(t *testing.T) {
	e := setup(t)
	pid := e.player()
	home := e.home(pid).ID
	e.setInv(pid, "wheat", "rare", "none", 4)
	e.casino(9)
	must[GambleResult](t)(e.gamble(pid, "wheat", "rare", "none", 4, "big", nil))
	prize := e.prizes(pid)[0]
	other := e.farmer("別人", home)
	_, err := e.a.SellPrize(e.ctx, other, prize.ID, 1) // 不能賣別人的獎品
	wantKind(t, err, gameerr.NotFound)
	gold := e.gold(pid)
	r := e.ok(e.a.SellPrize(e.ctx, pid, prize.ID, 3))
	if r.GoldDelta != 3*44 || e.gold(pid) != gold+132 {
		t.Fatalf("%+v", r)
	}
	if q := e.prizes(pid)[0].Quantity; q != 1 {
		t.Fatal(q)
	}
	_, err = e.a.SellPrize(e.ctx, pid, prize.ID, 2)
	wantKind(t, err, gameerr.InsufficientItems)
	e.invariant()
}
