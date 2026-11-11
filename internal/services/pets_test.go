package services

// 對應 Python tests/test_pets.py 的領養／升級與貓的收成加成
// （狗擋下偷菜屬於 SocialService、農場圖與面板屬於 Discord，移植對應部分時再補）。

import (
	"slices"
	"strings"
	"testing"

	"farmgm/internal/game/gameerr"
	petrules "farmgm/internal/game/pets"
)

// petOwner Python 的 pid 夾具：「飼主」，金幣 1,000,000（並寫 admin_adjust 交易紀錄）。
func (e *env) petOwner(home int64) int64 {
	pid := e.farmer("飼主", home)
	e.setGoldRecorded(pid, 1_000_000)
	return pid
}

func (e *env) goldMatchesTransactions() bool {
	return e.scalar("SELECT coalesce(sum(gold_delta), 0) FROM game_transactions") == e.scalar("SELECT coalesce(sum(gold), 0) FROM farms")
}

func TestPetRules(t *testing.T) {
	var pcts []int
	pcts = append(pcts, petrules.BonusPct(nil))
	for lv := 0; lv <= 4; lv++ {
		pcts = append(pcts, petrules.BonusPct(intPtr(lv)))
	}
	if !slices.Equal(pcts, []int{0, 5, 10, 15, 20, 25}) {
		t.Fatal(pcts)
	}
	for lv, want := range []int{10_000, 20_000, 30_000, 40_000, -1} {
		p, ok := petrules.UpgradePrice(lv)
		if !ok {
			p = -1
		}
		if p != want {
			t.Fatal(lv, p)
		}
	}
	if petrules.BonusQuantity(5, 10, 0.49) != 1 || petrules.BonusQuantity(5, 10, 0.5) != 0 ||
		petrules.BonusQuantity(20, 20, 0.99) != 4 || petrules.BonusQuantity(3, 0, 0) != 0 {
		t.Fatal("BonusQuantity")
	}
}

func TestBuyAndUpgradePet(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.petOwner(home)
	_, err := e.r.UpgradePet(e.ctx, pid, petrules.Cat) // 還沒領養
	wantKind(t, err, gameerr.NotFound)
	before := e.gold(pid)
	if r := e.ok(e.r.BuyPet(e.ctx, pid, petrules.Cat)); !strings.Contains(r.Message, "領養了貓咪") ||
		e.gold(pid) != before-petrules.BuyPrice() {
		t.Fatal(r.Message)
	}
	_, err = e.r.BuyPet(e.ctx, pid, petrules.Cat)
	wantKind(t, err, gameerr.AlreadyExists)
	for i, price := range petrules.UpgradePrices() {
		before := e.gold(pid)
		r := e.ok(e.r.UpgradePet(e.ctx, pid, petrules.Cat))
		if !strings.Contains(r.Message, "Lv"+string(rune('1'+i))) || e.gold(pid) != before-price {
			t.Fatal(r.Message)
		}
	}
	_, err = e.r.UpgradePet(e.ctx, pid, petrules.Cat)
	wantMsg(t, err, gameerr.Validation, "最高等級")
	status := map[string]PetView{}
	for _, v := range must[[]PetView](t)(e.r.PetStatus(e.ctx, pid)) {
		status[v.Code] = v
	}
	cat, dog := status["cat"], status["dog"]
	if *cat.Level != 4 || cat.Pct != 25 || cat.NextPrice != nil || dog.Owned || *dog.NextPrice != petrules.BuyPrice() {
		t.Fatalf("%+v %+v", cat, dog)
	}
	if cat.Odds == nil || !cat.Odds.Current || cat.Odds.Pct != 25 || dog.Odds != nil {
		t.Fatalf("%+v", cat.Odds)
	}
	if pets := e.status(pid).Pets; len(pets) != 1 || pets["cat"] != 4 {
		t.Fatal(pets)
	}
	_, err = e.r.BuyPet(e.ctx, pid, "hamster")
	wantKind(t, err, gameerr.NotFound)
	poor := e.farmer("窮人", home)
	_, err = e.r.BuyPet(e.ctx, poor, petrules.Dog)
	wantKind(t, err, gameerr.InsufficientGold)
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestCatAddsHarvest(t *testing.T) {
	e := setup(t)
	pid := e.petOwner(e.group("主群組"))
	harvest := func() ActionResult {
		e.grow(pid, 1, "strawberry") // 草莓收成 5 個
		e.clock.Advance(600)
		return e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	}
	if r := harvest(); r.Quantity != 5 { // 沒有貓
		t.Fatal(r.Quantity)
	}
	e.ok(e.r.BuyPet(e.ctx, pid, petrules.Cat))
	e.s.PetRng = func() float64 { return 0 }
	if r := harvest(); r.Quantity != 6 { // Lv0：+5% → 5 × 5% = 0.25 個
		t.Fatal(r.Quantity)
	}
	e.ok(e.r.UpgradePet(e.ctx, pid, petrules.Cat))
	e.ok(e.r.UpgradePet(e.ctx, pid, petrules.Cat)) // Lv2：+15% → 0.75 個
	if r := harvest(); r.Quantity != 6 || !strings.Contains(r.Message, "貓咪趕走老鼠 +1") {
		t.Fatal(r.Message)
	}
	e.s.PetRng = func() float64 { return 0.99 }
	if r := harvest(); r.Quantity != 5 {
		t.Fatal(r.Quantity)
	}
}
