package services

// 對應 Python tests/test_world_boss.py 的服務部分（Discord 面板、管理／介紹網站、成就圖鑑的稱號選用等對應階段再補；
// 純規則 test_simulate_keeps_fractional_damage、test_compose_rewards_min_points、test_slot_after_uses_taipei_time 在 game/worldboss）。

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"farmgm/internal/game/gameerr"

	wb "farmgm/internal/game/worldboss"
)

var reqSeq int64
var reqMu sync.Mutex

func newReq() string {
	reqMu.Lock()
	defer reqMu.Unlock()
	reqSeq++
	return fmt.Sprintf("req-%012d", reqSeq)
}

func simpleRewards() map[string]any {
	return toRawValue(wb.Rewards{MinPoints: 0, Victory: wb.Reward{Gold: 100, Items: map[string]int{}},
		Participation: wb.Reward{Gold: 10, Items: map[string]int{}}, BonusPoints: 10,
		Bonus: wb.Reward{Gold: 5, Items: map[string]int{"wood": 1}}, Rank: []wb.Reward{{Gold: 50, Items: map[string]int{}}},
		Title: "屠龍英雄", TitleTop: 1})
}

type bossOpt struct {
	hp, hours, startIn int
	rewards, settings  map[string]any
	global             bool
}

func (e *env) boss(groupID int64, o bossOpt) int64 {
	e.t.Helper()
	if o.hp == 0 {
		o.hp = 100_000
	}
	if o.hours == 0 {
		o.hours = 72
	}
	now := e.clock.Now()
	starts, ends := now.Add(time.Duration(o.startIn)*time.Second), now.Add(time.Duration(o.hours)*time.Hour)
	in := BossInput{Name: "深淵魔龍", Level: 50, MaxHP: o.hp, StartsAt: &starts, EndsAt: &ends, Rewards: o.rewards, Settings: o.settings}
	if !o.global {
		in.GroupID = &groupID
	} else {
		in.Name = "全服魔龍"
	}
	return must[BossView](e.t)(e.wb.CreateWorldBoss(e.ctx, "admin", in)).EventID
}

func (e *env) donate(gid *int64, pid, eid int64, category, code string, qty int) (BossDonateResult, error) {
	return e.wb.BossDonate(e.ctx, gid, pid, eid, category, code, qty, newReq(), "normal", "none")
}

func (e *env) mustDonate(gid *int64, pid, eid int64, category, code string, qty int) BossDonateResult {
	e.t.Helper()
	return must[BossDonateResult](e.t)(e.donate(gid, pid, eid, category, code, qty))
}

func (e *env) bossView(gid *int64, pid int64, eid *int64) *BossView {
	e.t.Helper()
	return must[*BossView](e.t)(e.wb.BossCurrent(e.ctx, gid, pid, eid))
}

func supplyRow(v *BossView, code string) BossSupplyView {
	for _, s := range v.Supplies {
		if s.Code == code {
			return s
		}
	}
	return BossSupplyView{}
}

func (e *env) produceQty(pid int64) int {
	e.t.Helper()
	return must[Backpack](e.t)(e.s.GetBackpack(e.ctx, pid)).Produce[0].Quantity
}

func defaultBossSettings() map[string]any { return toRawValue(wb.DefaultSettings()) }

// ---------- 1. 查看 ----------

func TestPlayerCanViewBoss(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	if e.bossView(&home, pid, nil) != nil {
		t.Fatal("沒有活動時應為 nil")
	}
	eid := e.boss(home, bossOpt{})
	v := e.bossView(&home, pid, nil)
	var codes []string
	for _, s := range v.Supplies {
		codes = append(codes, s.Code)
	}
	if v.EventID != eid || v.Name != "深淵魔龍" || v.HP != 100_000 || v.MaxHP != 100_000 || v.State != "active" || v.StateLabel != "進行中" ||
		!slices.Equal(codes, wb.Supplies) || supplyRow(v, "food").Target != wb.DefaultSettings().SupplyTargets["food"] ||
		v.Me.Points != 0 || v.Me.Rank != nil || v.Me.ClaimState != "pending" {
		t.Fatalf("%+v", v)
	}
}

func TestScheduledEventCannotReceiveDonations(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{startIn: 3600})
	e.give(pid, "carrot", 5)
	if e.bossView(&home, pid, nil).State != "scheduled" {
		t.Fatal("應為尚未開始")
	}
	_, err := e.donate(&home, pid, eid, "crop", "carrot", 1)
	wantMsg(t, err, gameerr.Validation, "尚未開始")
	e.clock.Advance(3600)
	if r := e.mustDonate(&home, pid, eid, "crop", "carrot", 1); r.Points != 3 {
		t.Fatal(r.Points)
	}
}

// ---------- 2–4. 捐獻農作物、漁獲、素材 ----------

func TestDonateCropsUpdatesSupplyPointsHPAndLog(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.give(pid, "carrot", 10)
	opts := must[[]BossDonationOption](t)(e.wb.BossDonationOptions(e.ctx, &home, pid, eid, "crop"))
	if len(opts) != 1 || opts[0].Code != "carrot" || opts[0].Held != 10 || opts[0].PointsMin != 3 || opts[0].Supply != "food" {
		t.Fatalf("%+v", opts)
	}
	r := e.mustDonate(&home, pid, eid, "crop", "carrot", 4)
	// 胡蘿蔔售價 25 → 每個 3 分；立即傷害 = 12 分 × 2 × 攻擊倍率 1
	if r.Points != 12 || r.Damage != 24 || r.Duplicate {
		t.Fatalf("%+v", r)
	}
	v := e.bossView(&home, pid, nil)
	if supplyRow(v, "food").Progress != 12 || v.HP != 100_000-24 || v.Me.Points != 12 || v.Me.Quantity != 4 || *v.Me.Rank != 1 ||
		e.produceQty(pid) != 6 {
		t.Fatalf("%+v", v.Me)
	}
	if e.scalar("SELECT count(*) FROM game_transactions WHERE event_type = 'boss_donate' AND item_code = 'carrot' AND quantity = 4 AND gold_delta = 0") != 1 ||
		e.scalar("SELECT count(*) FROM world_boss_contributions") != 1 {
		t.Fatal("紀錄不對")
	}
}

func TestDonateQualityCropScoresByPrice(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.giveQ(pid, "carrot", 2, "legendary")
	opt := must[[]BossDonationOption](t)(e.wb.BossDonationOptions(e.ctx, &home, pid, eid, "crop"))[0]
	if opt.Quality != "legendary" || opt.PointsMin <= 3 {
		t.Fatalf("%+v", opt)
	}
	r := must[BossDonateResult](t)(e.wb.BossDonate(e.ctx, &home, pid, eid, "crop", "carrot", 2, newReq(), "legendary", "none"))
	if r.Points != opt.PointsMin*2 {
		t.Fatal(r.Points)
	}
}

func TestDonateFish(t *testing.T) {
	e := setup(t)
	e.fisher(9)
	home := e.group("主群組")
	e.setFishingSettings(successPct(100))
	pid := e.farmer("釣魚高手", home)
	e.equipStarter(pid, 5)
	landed := e.fight(pid, e.castAndHook(pid))
	if landed.Status != "landed" {
		t.Fatal(landed.Status)
	}
	eid := e.boss(home, bossOpt{})
	opts := must[[]BossDonationOption](t)(e.wb.BossDonationOptions(e.ctx, &home, pid, eid, "fish"))
	if len(opts) != 1 || opts[0].Held != 1 || opts[0].Supply != "combat" {
		t.Fatalf("%+v", opts)
	}
	r := e.mustDonate(&home, pid, eid, "fish", landed.Fish.Code, 1)
	if r.Points != wb.PricePoints(landed.Fish.Price, 10) || supplyRow(e.bossView(&home, pid, nil), "combat").Progress != r.Points ||
		e.scalar("SELECT count(*) FROM farm_fish WHERE donated_at IS NOT NULL") != 1 {
		t.Fatalf("%+v", r)
	}
	// 捐出的魚離開魚簍，不能再賣
	if o := must[[]BossDonationOption](t)(e.wb.BossDonationOptions(e.ctx, &home, pid, eid, "fish")); len(o) != 0 {
		t.Fatal(o)
	}
	_, err := e.f.SellFish(e.ctx, pid, nil)
	wantKind(t, err, gameerr.Validation)
}

func TestDonateMaterialsGoodsAndSpecial(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	for code, n := range map[string]int{"wood": 5, "herb": 2, "crystal": 1, "bread": 1} {
		e.give(pid, code, n)
	}
	for _, c := range []struct {
		cat, code string
		n, want   int
	}{{"material", "wood", 5, 10}, {"material", "herb", 2, 12}, {"special", "crystal", 1, 15}, {"goods", "bread", 1, 20}} {
		if r := e.mustDonate(&home, pid, eid, c.cat, c.code, c.n); r.Points != c.want {
			t.Fatal(c.code, r.Points)
		}
	}
	v := e.bossView(&home, pid, nil)
	if supplyRow(v, "equipment").Progress != 25 || supplyRow(v, "medical").Progress != 12 || supplyRow(v, "combat").Progress != 20 {
		t.Fatalf("%+v", v.Supplies)
	}
	_, err := e.donate(&home, pid, eid, "goods", "wood", 1) // 類別不符
	wantMsg(t, err, gameerr.Validation, "不能用這個類別")
	e.give(pid, "fake_coin", 3)
	_, err = e.donate(&home, pid, eid, "material", "fake_coin", 1) // 假幣不能捐
	wantKind(t, err, gameerr.Validation)
}

// ---------- 5–6. 背包不足、重複提交 ----------

func TestCannotDonateMoreThanHeld(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.give(pid, "carrot", 3)
	_, err := e.donate(&home, pid, eid, "crop", "carrot", 4)
	wantKind(t, err, gameerr.InsufficientItems)
	_, err = e.donate(&home, pid, eid, "material", "wood", 1)
	wantKind(t, err, gameerr.InsufficientItems)
	if v := e.bossView(&home, pid, nil); v.TotalPoints != 0 || v.HP != v.MaxHP || e.produceQty(pid) != 3 {
		t.Fatalf("%+v", v)
	}
}

func TestPerItemLimit(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	s := defaultBossSettings()
	s["limits"] = map[string]any{"per_item_max": 5, "per_action_max": 99}
	eid := e.boss(home, bossOpt{settings: s})
	e.give(pid, "carrot", 20)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 4)
	_, err := e.donate(&home, pid, eid, "crop", "carrot", 2)
	wantMsg(t, err, gameerr.Validation, "還可以捐 1 個")
	if o := must[[]BossDonationOption](t)(e.wb.BossDonationOptions(e.ctx, &home, pid, eid, "crop")); o[0].Remaining != 1 {
		t.Fatalf("%+v", o)
	}
}

func TestDuplicateRequestIsProcessedOnce(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.give(pid, "carrot", 10)
	first := must[BossDonateResult](t)(e.wb.BossDonate(e.ctx, &home, pid, eid, "crop", "carrot", 3, "double-click-0001", "normal", "none"))
	again := must[BossDonateResult](t)(e.wb.BossDonate(e.ctx, &home, pid, eid, "crop", "carrot", 3, "double-click-0001", "normal", "none"))
	if first.Duplicate || !again.Duplicate {
		t.Fatal("重複請求沒擋住")
	}
	// 同時送出相同請求：活動列鎖 + 唯一限制保證只扣一次
	var mu sync.Mutex
	var wg sync.WaitGroup
	fresh := 0
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.wb.BossDonate(e.ctx, &home, pid, eid, "crop", "carrot", 2, "concurrent-0001", "normal", "none")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
			} else if !r.Duplicate {
				fresh++
			}
		}()
	}
	wg.Wait()
	if fresh != 1 || e.produceQty(pid) != 10-3-2 || e.bossView(&home, pid, nil).Me.Quantity != 5 {
		t.Fatal(fresh)
	}
	_, err := e.wb.BossDonate(e.ctx, &home, pid, eid, "crop", "carrot", 1, "bad id!", "normal", "none")
	wantKind(t, err, gameerr.Validation)
}

// ---------- 7. 群組隔離 ----------

func TestBossGroupsAreIsolated(t *testing.T) {
	e := setup(t)
	home, other := e.group("主群組"), e.group("另一個伺服器")
	alice, bob := e.farmer("阿明", home), e.farmer("小華", other)
	eid := e.boss(home, bossOpt{})
	e.give(bob, "carrot", 5)
	if e.bossView(&other, bob, nil) != nil { // 另一群組看不到這場活動
		t.Fatal("看到其他群組的活動")
	}
	_, err := e.donate(&other, bob, eid, "crop", "carrot", 1)
	wantKind(t, err, gameerr.NotFound)
	_, err = e.wb.BossCurrent(e.ctx, &home, bob, nil) // 不是成員
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.donate(&home, bob, eid, "crop", "carrot", 1)
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.wb.BossLeaderboard(e.ctx, &other, bob, eid, 20)
	wantKind(t, err, gameerr.NotFound)
	otherEid := e.boss(other, bossOpt{})
	e.give(alice, "carrot", 2)
	e.mustDonate(&home, alice, eid, "crop", "carrot", 2)
	if v := e.bossView(&other, bob, nil); v.EventID != otherEid || v.TotalPoints != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestOneOpenEventPerGroup(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	e.boss(home, bossOpt{})
	now := e.clock.Now()
	ends := now.Add(time.Hour)
	_, err := e.wb.CreateWorldBoss(e.ctx, "admin", BossInput{Name: "x", MaxHP: 100, StartsAt: &now, EndsAt: &ends, GroupID: &home})
	wantKind(t, err, gameerr.AlreadyExists)
}

// ---------- 8. 排行榜 ----------

func TestBossLeaderboardValues(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	ids := []int64{e.farmer("阿明", home), e.farmer("小華", home), e.farmer("阿美", home)}
	eid := e.boss(home, bossOpt{})
	for _, pid := range ids {
		e.give(pid, "carrot", 20)
		e.give(pid, "wood", 20)
	}
	e.mustDonate(&home, ids[1], eid, "crop", "carrot", 5) // 15 分
	e.clock.Advance(1)
	e.mustDonate(&home, ids[0], eid, "material", "wood", 4) // 8 分
	e.clock.Advance(1)
	e.mustDonate(&home, ids[2], eid, "crop", "carrot", 5) // 15 分（同分，較晚）
	e.mustDonate(&home, ids[0], eid, "crop", "carrot", 1) // 阿明合計 11 分
	b := must[BossLeaderboard](t)(e.wb.BossLeaderboard(e.ctx, &home, ids[0], eid, 20))
	var got []string
	for _, r := range b.Rows {
		got = append(got, fmt.Sprintf("%d:%s:%d:%d", r.Rank, r.PlayerName, r.Points, r.Quantity))
	}
	if !slices.Equal(got, []string{"1:小華:15:5", "2:阿美:15:5", "3:阿明:11:5"}) || b.Me.Rank != 3 || b.Participants != 3 {
		t.Fatal(got)
	}
}

// ---------- 9. HP 與補給 ----------

func TestHPDecreasesOverTimeBySupplies(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.clock.Advance(3600)
	v := e.bossView(&home, pid, nil)
	// 沒有補給：1500 × 攻擊 1 × 出勤率 30% = 450／小時
	if v.DamagePerHour != 450 || v.HP != 100_000-450 || v.UptimePct != 30.0 {
		t.Fatalf("%+v", v)
	}
	// 補滿糧食（2000 分）→ 出勤率 100%
	s := defaultBossSettings()
	items := s["items"].(map[string]any)
	items["carrot"] = map[string]any{"supply": "food", "points": 100}
	must[BossView](t)(e.wb.UpdateWorldBossSettings(e.ctx, "admin", eid, s))
	e.give(pid, "carrot", 20)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 20)
	v = e.bossView(&home, pid, nil)
	if supplyRow(v, "food").RatioPct != 100.0 || v.UptimePct != 100.0 || v.DamagePerHour != 1500 {
		t.Fatalf("%+v", v)
	}
	hp := v.HP
	e.clock.Advance(1800)
	if got := e.bossView(&home, pid, nil).HP; got != hp-750 {
		t.Fatal(got)
	}
	// 戰鬥補給有上限：超過目標 150% 後不再增加傷害
	if st := wb.Combat(map[string]int{"combat": 1_000_000_000}, wb.DefaultSettings()); st.AttackMult < 2.4999 || st.AttackMult > 2.5001 {
		t.Fatal(st.AttackMult)
	}
}

func TestBossDefeatedEndsEvent(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{hp: 500})
	e.clock.Advance(3600) // 450 傷害
	if e.bossView(&home, pid, nil).HP != 50 {
		t.Fatal("HP 不對")
	}
	e.give(pid, "bread", 2)
	r := e.mustDonate(&home, pid, eid, "goods", "bread", 2) // 40 分 × 2 × 倍率 > 50
	if !r.Defeated || r.Damage != 50 {
		t.Fatalf("%+v", r)
	}
	if v := e.bossView(&home, pid, nil); v.State != "defeated" || v.HP != 0 || v.Me.ClaimState != "ready" {
		t.Fatalf("%+v", v)
	}
}

// ---------- 10. 結束後不能捐獻 ----------

func TestNoDonationsAfterEnd(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{hours: 2})
	e.give(pid, "carrot", 5)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 1)
	e.clock.Advance(2*3600 + 1)
	_, err := e.donate(&home, pid, eid, "crop", "carrot", 1)
	wantMsg(t, err, gameerr.Validation, "已結束")
	if v := e.bossView(&home, pid, nil); v.State != "ended" || *v.EndReason != "timeout" || e.produceQty(pid) != 4 {
		t.Fatalf("%+v", v)
	}
}

func TestBossBackgroundSettlementFinalizes(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	eid := e.boss(home, bossOpt{hours: 1})
	e.clock.Advance(3601)
	if r := must[*WorldBossSettlement](t)(e.wb.RunBackgroundSettlement(e.ctx, "system")); !slices.Equal(r.WorldBoss, []int64{eid}) {
		t.Fatalf("%+v", r)
	}
	if r := must[*WorldBossSettlement](t)(e.wb.RunBackgroundSettlement(e.ctx, "system")); len(r.WorldBoss) != 0 {
		t.Fatalf("%+v", r)
	}
}

// ---------- 11. 獎勵 ----------

func TestBossRewardsClaimedOnce(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	a, b, c := e.farmer("阿明", home), e.farmer("小華", home), e.farmer("阿美", home)
	eid := e.boss(home, bossOpt{hp: 100, rewards: simpleRewards()})
	e.give(a, "carrot", 4)
	e.give(b, "carrot", 1)
	e.mustDonate(&home, a, eid, "crop", "carrot", 4) // 12 分：第 1 名、達標
	e.mustDonate(&home, b, eid, "crop", "carrot", 1) // 3 分
	_, err := e.wb.BossClaim(e.ctx, &home, a, eid)
	wantMsg(t, err, gameerr.Validation, "還沒結束")
	e.clock.Advance(3600) // 450 傷害 → 擊敗
	goldA := e.gold(a)
	r := e.ok(e.wb.BossClaim(e.ctx, &home, a, eid))
	if r.GoldDelta != 100+5+50 || !strings.Contains(r.Message, "屠龍英雄") || e.gold(a) != goldA+155 {
		t.Fatalf("%+v", r)
	}
	wood := false
	for _, m := range must[Backpack](t)(e.s.GetBackpack(e.ctx, a)).Materials {
		wood = wood || (m.Code == "wood" && m.Quantity == 1)
	}
	if !wood || *e.bossView(&home, a, nil).Me.Title != "屠龍英雄" ||
		e.scalar("SELECT count(*) FROM farm_titles WHERE title = '屠龍英雄' AND source = 'world_boss'") != 1 {
		t.Fatal("物品或稱號不對")
	}
	_, err = e.wb.BossClaim(e.ctx, &home, a, eid)
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.wb.BossClaim(e.ctx, &home, c, eid)
	wantMsg(t, err, gameerr.Validation, "沒有參與")
	res := runConcurrently(4, func() (ActionResult, error) { return e.wb.BossClaim(e.ctx, &home, b, eid) }) // 同時多次領取
	if n := countOK(t, res, gameerr.AlreadyExists); n != 1 {
		t.Fatal(n)
	}
	if e.scalar("SELECT count(*) FROM world_boss_reward_claims") != 2 ||
		e.scalar("SELECT sum(gold_delta) FROM game_transactions WHERE event_type = 'boss_reward'") != 255 {
		t.Fatal("獎勵紀錄不對")
	}
}

func TestParticipationRewardWhenNotDefeated(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	rw := simpleRewards()
	rw["bonus_points"], rw["rank"] = 0, []any{}
	eid := e.boss(home, bossOpt{hours: 1, rewards: rw})
	e.give(pid, "carrot", 1)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 1)
	e.clock.Advance(3601)
	r := e.ok(e.wb.BossClaim(e.ctx, &home, pid, eid))
	if r.GoldDelta != 10 || !strings.Contains(r.Message, "參與獎勵") || strings.Contains(r.Message, "稱號") {
		t.Fatalf("%+v", r)
	}
}

func TestBelowMinPointsGetsNoReward(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	a, b := e.farmer("阿明", home), e.farmer("小華", home)
	rw := simpleRewards()
	rank := toRawValue(wb.Reward{Gold: 50, Items: map[string]int{}})
	rw["min_points"], rw["rank"] = 12, []any{rank, rank}
	eid := e.boss(home, bossOpt{hp: 100, rewards: rw})
	e.give(a, "carrot", 4)
	e.give(b, "carrot", 1)
	if r := e.mustDonate(&home, a, eid, "crop", "carrot", 3); r.Points != 9 || e.bossView(&home, a, nil).Me.ClaimState != "pending" {
		t.Fatal(r.Points)
	}
	e.mustDonate(&home, b, eid, "crop", "carrot", 1) // 3 分：第 2 名，但未達門檻
	e.mustDonate(&home, a, eid, "crop", "carrot", 1) // 阿明 12 分：剛好達門檻
	e.clock.Advance(3600)
	if vb := e.bossView(&home, b, nil); vb.Me.ClaimState != "below_min" || vb.RewardsText.MinPoints != 12 {
		t.Fatalf("%+v", vb.Me)
	}
	goldB := e.gold(b)
	_, err := e.wb.BossClaim(e.ctx, &home, b, eid)
	wantMsg(t, err, gameerr.Validation, "未達參與門檻")
	if e.gold(b) != goldB || e.ok(e.wb.BossClaim(e.ctx, &home, a, eid)).GoldDelta != 100+5+50 ||
		e.scalar("SELECT count(*) FROM world_boss_reward_claims") != 1 {
		t.Fatal("門檻判斷不對")
	}
	e.boss(home, bossOpt{rewards: rw}) // 下一場的「可領取」清單不包含未達門檻的活動
	if v := e.bossView(&home, b, nil); len(v.Claimable) != 0 {
		t.Fatalf("%+v", v.Claimable)
	}
}

// ---------- 12. 管理功能（服務層） ----------

func TestBossAdminPauseResumeEndAndValidation(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.give(pid, "carrot", 5)
	if v := must[BossView](t)(e.wb.WorldBossAction(e.ctx, "admin", eid, "pause")); v.State != "paused" {
		t.Fatal(v.State)
	}
	_, err := e.donate(&home, pid, eid, "crop", "carrot", 1)
	wantMsg(t, err, gameerr.Validation, "已暫停")
	e.clock.Advance(3600)
	if v := must[BossView](t)(e.wb.WorldBossAction(e.ctx, "admin", eid, "resume")); v.HP != 100_000 { // 暫停期間不扣 HP
		t.Fatal(v.HP)
	}
	v := must[BossView](t)(e.wb.UpdateWorldBoss(e.ctx, "admin", eid, BossInput{Name: "冰霜巨龍", Level: 60, MaxHP: 120_000}))
	if v.Name != "冰霜巨龍" || v.MaxHP != 120_000 || v.HP != 120_000 {
		t.Fatalf("%+v", v)
	}
	bad := defaultBossSettings()
	bad["items"] = map[string]any{"seed_wheat": map[string]any{"supply": "food", "points": 1}}
	_, err = e.wb.UpdateWorldBossSettings(e.ctx, "admin", eid, bad)
	wantKind(t, err, gameerr.Validation)
	if v := must[BossView](t)(e.wb.WorldBossAction(e.ctx, "admin", eid, "end")); v.State != "ended" {
		t.Fatal(v.State)
	}
	_, err = e.wb.WorldBossAction(e.ctx, "admin", eid, "resume")
	wantMsg(t, err, gameerr.Validation, "已經結束")
	var actions []string
	repotestQuery(t, e, "SELECT action FROM admin_actions WHERE target_type = 'world_boss' ORDER BY id", &actions)
	if !slices.Equal(actions, []string{"world_boss_create", "world_boss_pause", "world_boss_resume", "world_boss_update", "world_boss_end"}) {
		t.Fatal(actions)
	}
	e.boss(home, bossOpt{}) // 結束後可以再開新的一場
}

// ---------- 14. 重啟後資料仍存在 ----------

func TestBossDataSurvivesRestart(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	eid := e.boss(home, bossOpt{})
	e.give(pid, "carrot", 5)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 5)
	before := e.bossView(&home, pid, nil)
	restarted := &WorldBossService{&RankingService{&CommunityService{&SocialService{&FishingService{&ExplorationService{restartedRanch(t, e)}, e.f.FishingRng}}}}}
	after := must[*BossView](t)(restarted.BossCurrent(e.ctx, &home, pid, nil))
	if after.HP != before.HP || after.Me.Points != 15 || supplyRow(after, "food").Progress != 15 {
		t.Fatalf("%+v", after)
	}
}

// ---------- 全服活動 ----------

func TestGlobalEventSharedByAllGroups(t *testing.T) {
	e := setup(t)
	home, other := e.group("主群組"), e.group("另一個伺服器")
	alice, bob := e.farmer("阿明", home), e.farmer("小華", other)
	eid := e.boss(0, bossOpt{global: true})
	e.give(alice, "carrot", 5)
	e.give(bob, "carrot", 5)
	va, vb := e.bossView(&home, alice, nil), e.bossView(&other, bob, nil)
	if va.EventID != eid || vb.EventID != eid || va.ScopeLabel != "🌏 全服" {
		t.Fatal("全服活動沒有共用")
	}
	e.mustDonate(&home, alice, eid, "crop", "carrot", 5)
	e.mustDonate(&other, bob, eid, "crop", "carrot", 2)
	if e.bossView(nil, bob, nil).EventID != eid { // 私訊也看得到全服活動
		t.Fatal("私訊看不到")
	}
	board := must[BossLeaderboard](t)(e.wb.BossLeaderboard(e.ctx, &other, bob, eid, 20))
	if len(board.Rows) != 2 || board.Rows[0].PlayerName != "阿明" || board.Rows[1].PlayerName != "小華" {
		t.Fatalf("%+v", board.Rows)
	}
	// 全服與伺服器活動同時存在：都看得到，可以切換；別的伺服器的活動仍然看不到
	local := e.boss(home, bossOpt{})
	choices := func(v *BossView) []int64 {
		var ids []int64
		for _, c := range v.Choices {
			ids = append(ids, c.EventID)
		}
		slices.Sort(ids)
		return ids
	}
	want := []int64{eid, local}
	slices.Sort(want)
	if !slices.Equal(choices(e.bossView(&home, alice, nil)), want) || e.bossView(&home, alice, &local).EventID != local ||
		!slices.Equal(choices(e.bossView(&other, bob, nil)), []int64{eid}) || e.bossView(&other, bob, &local).EventID != eid {
		t.Fatal("切換不對")
	}
	_, err := e.donate(&other, bob, local, "crop", "carrot", 1)
	wantKind(t, err, gameerr.NotFound)
	now := e.clock.Now()
	ends := now.Add(time.Hour)
	_, err = e.wb.CreateWorldBoss(e.ctx, "admin", BossInput{Name: "x", MaxHP: 100, StartsAt: &now, EndsAt: &ends}) // 手動全服活動同時只能一場
	wantKind(t, err, gameerr.AlreadyExists)
}

// ---------- 固定出現的世界 Boss（排程） ----------

func scheduleData(change func(map[string]any)) map[string]any {
	p := wb.AbyssDragon()
	raw := map[string]any{"name": p.Name, "anchor_date": "2026-01-01", "interval_days": p.IntervalDays, "spawn_hour": p.SpawnHour,
		"spawn_minute": p.SpawnMinute, "duration_minutes": p.DurationMinutes, "max_hp": p.MaxHP,
		"settings": toRawValue(p.Settings), "rewards": toRawValue(p.Rewards), "group_id": nil}
	if change != nil {
		change(raw)
	}
	return raw
}

func TestScheduleSpawnsEachOccurrenceOnce(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	sched := must[BossScheduleView](t)(e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(nil)))
	if sched.GroupName != "全服" || sched.NextEvent == nil {
		t.Fatalf("%+v", sched)
	}
	first := *sched.NextEvent
	if taipeiFmt(first.StartsAt, "01-02 15:04") != "01-01 21:00" || first.EndsAt.Sub(first.StartsAt) != 3*time.Hour {
		t.Fatalf("%+v", first)
	}
	if ids := must[[]int64](t)(e.wb.SpawnDueWorldBosses(e.ctx)); len(ids) != 0 { // 同一場不會重複建立
		t.Fatal(ids)
	}
	if v := e.bossView(&home, pid, nil); v.EventID != first.EventID || v.State != "scheduled" || v.Name != "深淵魔龍" {
		t.Fatalf("%+v", v)
	}
	// 出現前個別修改這一場的時間與獎勵
	newStart := first.StartsAt.Add(-time.Hour)
	must[BossView](t)(e.wb.UpdateWorldBoss(e.ctx, "admin", first.EventID, BossInput{Name: "深淵魔龍", MaxHP: 20_000, StartsAt: &newStart, EndsAt: &first.EndsAt}))
	must[BossView](t)(e.wb.UpdateWorldBossRewards(e.ctx, "admin", first.EventID, simpleRewards()))
	if v := e.bossView(&home, pid, nil); !v.StartsAt.Equal(newStart) || v.MaxHP != 20_000 || v.Rewards.Title != "屠龍英雄" {
		t.Fatalf("%+v", v)
	}
	// 這一場結束後建立下一場（兩天後 21:00），且套用排程的原本設定
	e.clock.Set(first.EndsAt.Add(time.Second))
	r := must[*WorldBossSettlement](t)(e.wb.RunBackgroundSettlement(e.ctx, "system"))
	if !slices.Equal(r.WorldBoss, []int64{first.EventID}) || len(r.WorldBossSpawned) != 1 {
		t.Fatalf("%+v", r)
	}
	nxt := e.bossView(&home, pid, nil)
	if nxt.State != "scheduled" || nxt.MaxHP != 15_000 || nxt.StartsAt.Sub(first.StartsAt) != 48*time.Hour || nxt.Rewards.Title != "降龍勇士" {
		t.Fatalf("%+v", nxt)
	}
	// 停用排程：結束現在這一場後不再產生新的
	must[BossScheduleView](t)(e.wb.SetWorldBossScheduleEnabled(e.ctx, "admin", sched.ScheduleID, false))
	must[BossView](t)(e.wb.WorldBossAction(e.ctx, "admin", nxt.EventID, "end"))
	if ids := must[[]int64](t)(e.wb.SpawnDueWorldBosses(e.ctx)); len(ids) != 0 {
		t.Fatal(ids)
	}
}

func TestScheduleValidationAndDelete(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	_, err := e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(func(r map[string]any) { r["interval_days"] = 0 }))
	wantKind(t, err, gameerr.Validation)
	_, err = e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(func(r map[string]any) { r["spawn_hour"] = 24 }))
	wantKind(t, err, gameerr.Validation)
	_, err = e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(func(r map[string]any) { r["group_id"] = 999999 }))
	wantKind(t, err, gameerr.NotFound)
	sched := must[BossScheduleView](t)(e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(func(r map[string]any) { r["group_id"] = home })))
	if *sched.GroupID != home || sched.NextEvent == nil {
		t.Fatalf("%+v", sched)
	}
	must[int64](t)(e.wb.DeleteWorldBossSchedule(e.ctx, "admin", sched.ScheduleID))
	pid := e.farmer("阿明", home)
	if v := e.bossView(&home, pid, nil); v.State != "ended" || v.ScheduleID != nil { // 活動紀錄保留，尚未結束的那一場一併結束
		t.Fatalf("%+v", v)
	}
}

// dragonBatch 每位玩家一次的捐獻：糧食 80、醫療 60、戰鬥 70、裝備 80 積分
var dragonBatch = []struct {
	cat, code string
	n         int
}{{"crop", "carrot", 20}, {"crop", "tomato", 10}, {"goods", "egg", 10}, {"material", "herb", 5},
	{"goods", "bread", 2}, {"goods", "flour", 6}, {"material", "wood", 20}, {"material", "stone", 20}}

func (e *env) dragonFight(players int) *BossView {
	e.t.Helper()
	gids := []int64{e.group("伺服器0"), e.group("伺服器1"), e.group("伺服器2")}
	ev := *must[BossScheduleView](e.t)(e.wb.CreateWorldBossSchedule(e.ctx, "admin", scheduleData(nil))).NextEvent
	e.clock.Set(ev.StartsAt)
	type gp struct{ gid, pid int64 }
	var list []gp
	for i := range players {
		g := gids[i%3]
		pid := e.farmer(fmt.Sprintf("玩家%d", i), g)
		for _, b := range dragonBatch {
			e.give(pid, b.code, b.n)
		}
		list = append(list, gp{g, pid})
	}
	var wg sync.WaitGroup
	for _, x := range list { // 玩家同時操作
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, b := range dragonBatch {
				if _, err := e.donate(&x.gid, x.pid, ev.EventID, b.cat, b.code, b.n); err != nil {
					e.t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	e.clock.Set(ev.EndsAt.Add(time.Second))
	return e.bossView(&list[0].gid, list[0].pid, &ev.EventID)
}

func TestAbyssDragonDefeatedByTenPlayers(t *testing.T) {
	e := setup(t)
	v := e.dragonFight(10)
	if v.State != "defeated" || v.Participants != 10 || v.HP != 0 || v.TotalPoints != 10*290 || !v.DefeatedAt.Before(v.EndsAt) {
		t.Fatalf("%+v", v)
	}
}

func TestAbyssDragonSurvivesSmallGroups(t *testing.T) {
	for _, n := range []int{1, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := setup(t)
			if v := e.dragonFight(n); v.State != "ended" || v.HP <= 0 {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestViewingDoesNotLockOrWrite(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.farmer("阿明", home)
	rw := toRawValue(wb.DefaultRewards())
	rw["min_points"] = 0
	eid := e.boss(home, bossOpt{hours: 2, rewards: rw})
	e.clock.Advance(3600)
	if e.bossView(&home, pid, nil).HP != 100_000-450 || must[BossLeaderboard](t)(e.wb.BossLeaderboard(e.ctx, &home, pid, eid, 20)).Participants != 0 ||
		e.scalar("SELECT hp FROM world_boss_events WHERE id = $1", eid) != 100_000 { // 查看沒有寫入
		t.Fatal("查看寫入了資料庫")
	}
	e.give(pid, "carrot", 1)
	e.mustDonate(&home, pid, eid, "crop", "carrot", 1)
	if e.scalar("SELECT hp FROM world_boss_events WHERE id = $1", eid) != 100_000-450-6 { // 捐獻時把時間傷害一起寫回
		t.Fatal("捐獻沒有寫回時間傷害")
	}
	e.clock.Advance(3601)
	v := e.bossView(&home, pid, nil)
	if v.State != "ended" || *v.EndReason != "timeout" || !v.EndedAt.Equal(v.EndsAt) || v.Me.ClaimState != "ready" ||
		e.scalar("SELECT count(*) FROM world_boss_events WHERE id = $1 AND status = 'active'", eid) != 1 {
		t.Fatalf("%+v", v)
	}
	if r := e.ok(e.wb.BossClaim(e.ctx, &home, pid, eid)); r.GoldDelta <= 0 ||
		e.scalar("SELECT count(*) FROM world_boss_events WHERE id = $1 AND status = 'ended'", eid) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestLastLeaderboardIsPreviousFinishedEvent(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	a, b := e.farmer("阿明", home), e.farmer("小華", home)
	first := e.boss(home, bossOpt{hours: 1})
	e.give(a, "carrot", 4)
	e.mustDonate(&home, a, first, "crop", "carrot", 4)
	if l := must[*BossLeaderboard](t)(e.wb.BossLastLeaderboard(e.ctx, &home, a, first, 20)); l != nil { // 第一場還在進行：沒有上一場
		t.Fatalf("%+v", l)
	}
	e.clock.Advance(3600) // 時間到（背景結算前也算已結束）
	last := must[*BossLeaderboard](t)(e.wb.BossLastLeaderboard(e.ctx, &home, a, first, 20))
	if last.EventID != first || !last.IsCurrent || last.Defeated || len(last.Rows) != 1 || last.Rows[0].PlayerName != "阿明" {
		t.Fatalf("%+v", last)
	}
	must[[]int64](t)(e.wb.FinalizeDueWorldBosses(e.ctx))
	second := e.boss(home, bossOpt{})
	e.give(b, "carrot", 2)
	e.mustDonate(&home, b, second, "crop", "carrot", 2)
	prev := must[*BossLeaderboard](t)(e.wb.BossLastLeaderboard(e.ctx, &home, b, second, 20))
	if prev.EventID != first || prev.IsCurrent || prev.Rows[0].PlayerName != "阿明" || prev.Me != nil {
		t.Fatalf("%+v", prev)
	}
	if cur := must[BossLeaderboard](t)(e.wb.BossLeaderboard(e.ctx, &home, b, second, 20)); cur.Rows[0].PlayerName != "小華" { // 戰鬥區的目前排行
		t.Fatalf("%+v", cur)
	}
}
