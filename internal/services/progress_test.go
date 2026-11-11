package services

// 對應 Python tests/test_checkin.py、test_achievements.py、test_codex.py、test_feedback.py（歡迎回來、圖鑑與成就標記）、
// test_special_shop.py 的服務層測試（規則測試在 game/checkin、game/achievements、game/specialshop；面板在第 4 階段）。

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/repotest"
)

const dayS = 24 * 3600

// ---------- 每日簽到 ----------

func (e *env) checkin(pid int64) *CheckinResult {
	e.t.Helper()
	r, err := e.p.DailyCheckin(e.ctx, pid)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) checkinStatus(pid int64) CheckinStatus {
	e.t.Helper()
	return must[CheckinStatus](e.t)(e.p.CheckinStatus(e.ctx, pid))
}

func TestFirstCheckinAndOncePerDay(t *testing.T) {
	e := setup(t)
	pid := e.player()
	r := e.checkin(pid)
	if !r.New || r.Streak != 1 || r.RewardText != "100 金幣" || e.gold(pid) != balance.InitialGold+100 {
		t.Fatalf("%+v gold=%d", r, e.gold(pid))
	}
	// 同一天（台灣時間 08:00 → 23:59）再呼叫不會重複發獎
	e.clock.Advance(15*3600 + 59*60)
	again := e.checkin(pid)
	if again.New || !again.NextAt.Equal(r.NextAt) || e.gold(pid) != balance.InitialGold+100 {
		t.Fatalf("%+v", again)
	}
	st := e.checkinStatus(pid)
	if !st.CheckedToday || st.Streak != 1 || st.NextRewardText != checkin.RewardText(checkin.Rewards()[1]) {
		t.Fatalf("%+v", st)
	}
	// 台灣時間跨過 00:00 就是新的一天
	e.clock.Advance(60)
	if e.checkinStatus(pid).CheckedToday {
		t.Fatal("新的一天還沒簽到")
	}
	if r2 := e.checkin(pid); !r2.New || r2.Streak != 2 {
		t.Fatalf("%+v", r2)
	}
	if e.scalar("SELECT count(*) FROM daily_checkins") != 2 || e.qty(pid, balance.ItemFertilizer) != 3 {
		t.Fatal("第 2 天：化肥 ×3")
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestStreakResetsAfterMissingADayAndCycles(t *testing.T) {
	e := setup(t)
	pid := e.player()
	for day := 1; day <= 8; day++ {
		r := e.checkin(pid)
		if r.Streak != day || r.RewardText != checkin.RewardText(checkin.Rewards()[(day-1)%7]) {
			t.Fatalf("day %d: %+v", day, r)
		}
		e.clock.Advance(dayS)
	}
	e.clock.Advance(dayS) // 中斷一天
	if e.checkinStatus(pid).Streak != 0 || e.checkin(pid).Streak != 1 {
		t.Fatal("中斷後重新計算")
	}
}

func TestCheckinFullWarehouseConvertsItemsToGold(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.checkin(pid)
	e.clock.Advance(dayS)
	e.exec("UPDATE farms SET warehouse_capacity = 1 WHERE player_id = $1", pid)
	before := e.gold(pid)
	r := e.checkin(pid) // 第 2 天：化肥 ×3 放不下 → 3 × 化肥價格
	want := 3 * checkin.ItemGoldValue(balance.ItemFertilizer)
	if !r.Converted || r.RewardText != strconv.Itoa(want)+" 金幣" || e.gold(pid) != before+want {
		t.Fatalf("%+v", r)
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestCheckinNoFarmReturnsNil(t *testing.T) {
	e := setup(t)
	p := must[PlayerInfo](t)(e.s.CreatePlayer(e.ctx, "沒農場", nil, nil))
	if e.checkin(p.ID) != nil {
		t.Fatal("沒有農場時回傳 nil")
	}
}

// ---------- 成就與稱號 ----------

// achiever 成就測試的玩家：金幣與倉庫都足夠。
func (e *env) achiever(home int64) int64 {
	p := e.farmer("成就玩家", home)
	e.exec("UPDATE farms SET gold = 100000, warehouse_capacity = 10000 WHERE player_id = $1", p)
	return p
}

func (e *env) harvestWheat(pid int64, quality string, rounds int) {
	e.t.Helper()
	for range rounds {
		e.ok(e.s.PlantAll(e.ctx, pid, "wheat"))
		e.ok(e.s.WaterAll(e.ctx, pid))
		e.exec("UPDATE farm_plots SET quality = $1 WHERE crop_type_id IS NOT NULL", quality)
		e.clock.Advance(600)
		e.ok(e.s.HarvestAll(e.ctx, pid))
	}
}

func (e *env) achievement(pid int64, code string) AchievementRow {
	e.t.Helper()
	for _, r := range must[AchievementView](e.t)(e.p.Achievements(e.ctx, pid)).Rows {
		if r.Code == code {
			return r
		}
	}
	e.t.Fatalf("沒有成就 %s", code)
	return AchievementRow{}
}

// rewardGold 成就獎勵寫入交易紀錄的金幣合計（測試前置直接改了金幣，所以比對這次增加的部分）。
func (e *env) rewardGold() int {
	return e.scalar("SELECT coalesce(sum(gold_delta), 0) FROM game_transactions WHERE event_type = 'achievement_reward'")
}

func TestAchievementProgressCountsHistoryAndClaimOnce(t *testing.T) {
	e := setup(t)
	pid := e.achiever(e.group("主群組"))
	if e.achievement(pid, "crops_100").State != "progress" {
		t.Fatal("一開始是進行中")
	}
	e.harvestWheat(pid, "normal", 3)
	if v := e.achievement(pid, "crops_100").Value; v < 100 { // 收成量依作物設定，保險起見補到 100
		e.harvestWheat(pid, "normal", (100-v+3)/4)
	}
	if e.achievement(pid, "crops_100").State != "ready" || e.achievement(pid, "level_5").State != "progress" {
		t.Fatal("crops_100 可領取、level_5 進行中")
	}
	data := must[AchievementView](t)(e.p.Achievements(e.ctx, pid))
	if data.Ready < 1 || data.Claimed != 0 {
		t.Fatalf("%+v", data)
	}
	before := e.gold(pid)
	r := e.ok(e.p.ClaimAchievements(e.ctx, pid))
	if !strings.Contains(r.Message, "初出茅廬") || e.gold(pid) != before+200 || e.rewardGold() != 200 {
		t.Fatalf("%s gold=%d", r.Message, e.gold(pid))
	}
	if e.achievement(pid, "crops_100").State != "claimed" {
		t.Fatal("已領取")
	}
	_, err := e.p.ClaimAchievements(e.ctx, pid)
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "沒有可以領取") {
		t.Fatal(err)
	}
	if e.scalar("SELECT count(*) FROM farm_achievements") != data.Ready {
		t.Fatal("每個成就領取一次")
	}
}

func TestTitleFromAchievementAndDisplay(t *testing.T) {
	e := setup(t)
	pid := e.achiever(e.group("主群組"))
	e.harvestWheat(pid, "legendary", 1)
	if e.achievement(pid, "legendary_crop").State != "ready" {
		t.Fatal("傳說品質作物可領取")
	}
	e.ok(e.p.ClaimAchievements(e.ctx, pid))
	found := false
	for _, o := range must[TitlesView](t)(e.p.Titles(e.ctx, pid)).Owned {
		if o.Title == "傳說農夫" {
			found = o.Source == "傳說農夫"
		}
	}
	if !found || e.qty(pid, balance.ItemPatrolTicket) != 3 {
		t.Fatal("稱號與巡查券 ×3")
	}
	_, err := e.p.SetTitle(e.ctx, pid, "不存在的稱號")
	wantKind(t, err, gameerr.NotFound)
	e.ok(e.p.SetTitle(e.ctx, pid, "傳說農夫"))
	if st := e.status(pid); st.Title == nil || *st.Title != "傳說農夫" {
		t.Fatal("已設定稱號")
	}
	if m := must[map[int64]string](t)(e.p.PlayerTitles(e.ctx, []int64{pid})); m[pid] != "傳說農夫" || len(m) != 1 {
		t.Fatal(m)
	}
	e.ok(e.p.SetTitle(e.ctx, pid, ""))
	if e.status(pid).Title != nil || len(must[map[int64]string](t)(e.p.PlayerTitles(e.ctx, []int64{pid}))) != 0 {
		t.Fatal("已取消稱號")
	}
}

func TestFullWarehouseConvertsAchievementItems(t *testing.T) {
	e := setup(t)
	pid := e.achiever(e.group("主群組"))
	e.harvestWheat(pid, "legendary", 1)
	e.exec("UPDATE farms SET warehouse_capacity = 1 WHERE player_id = $1", pid)
	before := e.gold(pid)
	r := e.ok(e.p.ClaimAchievements(e.ctx, pid))
	gained := 3 * balance.Items[balance.ItemPatrolTicket].Price
	if !strings.Contains(r.Message, "改發等值金幣") || e.gold(pid) != before+gained || e.rewardGold() != gained {
		t.Fatalf("%s gold=%d", r.Message, e.gold(pid))
	}
}

func TestCheckinFeedsStreakAchievement(t *testing.T) {
	e := setup(t)
	pid := e.player()
	e.checkin(pid)
	for range 6 {
		e.clock.Advance(dayS)
		e.checkin(pid)
	}
	if e.achievement(pid, "streak_7").State != "ready" {
		t.Fatal("連續簽到 7 天")
	}
}

func TestActionCountsComeFromFarmStats(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	e.s.dice = func() int { return 7 }
	owner, thief := e.farmer("農夫", home), e.farmer("小偷", home)
	e.grow(owner, 1, "carrot")
	e.clock.Advance(600)
	e.steal(home, thief, owner, 1, 1)
	e.exec(`INSERT INTO inventory_items (farm_id, item_code, quality, mutation, quantity)
		SELECT id, 'wheat', 'rare', 'none', 5 FROM farms WHERE player_id = $1`, thief)
	for _, mode := range []string{"big", "small"} {
		if _, err := e.s.Gamble(e.ctx, thief, "wheat", "rare", "none", 1, mode, nil); err != nil {
			t.Fatal(err)
		}
	}
	if e.achievement(thief, "steal_20").Value != 1 || e.achievement(thief, "gamble_50").Value != 2 {
		t.Fatal("偷菜 1 次、賭場 2 次")
	}
	repotest.Tx(t, e.d, func(ctx2 context.Context, tx *db.Tx) error {
		r, err := farmstats.Check(ctx2, tx, 5)
		if err == nil && r.FarmStatsMismatches != 0 {
			t.Fatalf("%+v", r)
		}
		return err
	})
}

// ---------- 圖鑑 ----------

func (e *env) codexCategory(pid int64, key string) CodexCategory {
	e.t.Helper()
	for _, c := range must[CodexView](e.t)(e.p.Codex(e.ctx, pid)).Categories {
		if c.Key == key {
			return c
		}
	}
	e.t.Fatalf("沒有分類 %s", key)
	return CodexCategory{}
}

func (e *env) codexEntry(pid int64, key, code string) CodexEntry {
	e.t.Helper()
	for _, x := range e.codexCategory(pid, key).Entries {
		if x.Code == code {
			return x
		}
	}
	e.t.Fatalf("沒有圖鑑項目 %s:%s", key, code)
	return CodexEntry{}
}

func (e *env) collector(home int64) int64 {
	p := e.farmer("收藏家", home)
	e.exec("UPDATE farms SET gold = 100000 WHERE player_id = $1", p)
	return p
}

func TestEmptyCodexListsEverythingHidden(t *testing.T) {
	e := setup(t)
	pid := e.collector(e.group("主群組"))
	c := must[CodexView](t)(e.p.Codex(e.ctx, pid))
	if c.Discovered != 0 || c.Total <= 0 {
		t.Fatalf("%d/%d", c.Discovered, c.Total)
	}
	var keys []string
	for _, x := range c.Categories {
		keys = append(keys, x.Key)
	}
	if strings.Join(keys, ",") != "crop,animal,goods,material,fish" {
		t.Fatal(keys)
	}
	codes := map[string]bool{}
	for _, x := range e.codexCategory(pid, "crop").Entries {
		codes[x.Code] = true
	}
	if !codes["wheat"] || !codes["amber_wheat"] || e.codexCategory(pid, "material").Total != len(exploration.Materials) {
		t.Fatal("作物與珍稀變種各一項、素材全部列出")
	}
}

func TestHarvestRegistersQuantityAndBestQuality(t *testing.T) {
	e := setup(t)
	pid := e.collector(e.group("主群組"))
	e.grow(pid, 1, "wheat")
	e.clock.Advance(600)
	q1 := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)).Quantity
	if x := e.codexEntry(pid, "crop", "wheat"); !x.Got || x.Quantity != q1 || x.BestQuality != "普通" {
		t.Fatalf("%+v", x)
	}
	// 下一批是傳說品質：最高品質更新、數量累加
	e.grow(pid, 1, "wheat")
	e.exec("UPDATE farm_plots SET quality = 'legendary' WHERE plot_number = 1")
	e.clock.Advance(600)
	q2 := e.ok(e.s.HarvestCrop(e.ctx, pid, 1)).Quantity
	if x := e.codexEntry(pid, "crop", "wheat"); x.Quantity != q1+q2 || x.BestQuality != "傳說" {
		t.Fatalf("%+v", x)
	}
	if e.codexCategory(pid, "crop").Discovered != 1 {
		t.Fatal("只發現小麥")
	}
}

func TestAnimalAndExplorationRegister(t *testing.T) {
	e := setup(t)
	e.s.exploreRng = func() float64 { return 0 }
	pid := e.collector(e.group("主群組"))
	e.ok(e.r.Build(e.ctx, pid, "畜舍"))
	e.ok(e.r.BuyAnimals(e.ctx, pid, "雞", 1))
	e.ok(e.r.FeedAnimal(e.ctx, pid, 1))
	e.clock.Advance(dayS)
	e.ok(e.r.CollectAnimal(e.ctx, pid, 1))
	if !e.codexEntry(pid, "animal", "egg").Got {
		t.Fatal("雞蛋登記圖鑑")
	}
	if _, err := e.x.Explore(e.ctx, pid, "grassland"); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(float64(exploration.Areas["grassland"].DurationSeconds))
	reward := must[ExploreReward](t)(e.x.ClaimExploration(e.ctx, pid))
	for _, item := range reward.Items {
		if _, ok := exploration.Materials[item.Code]; ok {
			if got := e.codexEntry(pid, "material", item.Code).Quantity; got != item.Quantity {
				t.Fatalf("%s: %d != %d", item.Code, got, item.Quantity)
			}
		}
	}
}

func TestStealRegistersCodexForThief(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.collector(home)
	thief := e.farmer("小偷", home)
	e.grow(pid, 1, "carrot")
	e.clock.Advance(600)
	e.steal(home, thief, pid, 1, 1)
	if !e.codexEntry(thief, "crop", "carrot").Got || e.codexEntry(pid, "crop", "carrot").Got {
		t.Fatal("偷到的人登記圖鑑，被偷的人沒有")
	}
}

// ---------- 歡迎回來、圖鑑與成就標記 ----------

func TestWelcomeBackSummary(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	owner, thief := e.farmer("主人", home), e.farmer("小偷", home)
	first := must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, owner))
	if len(first.Lines) != 0 || len(first.Marks.Codex) != 0 {
		t.Fatalf("%+v", first)
	}
	e.grow(owner, 1, "wheat")
	e.clock.Advance(3600)
	r := e.steal(home, thief, owner, 1, 1)
	if !strings.Contains(r.Message, "成功從 主人 的第 1 格") || !strings.Contains(r.Message, "主人 下次打開農場時會知道是你偷的") {
		t.Fatal(r.Message)
	}
	lines := must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, owner)).Lines
	text := strings.Join(lines, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "🌙 歡迎回來！你離開了 1 小時") ||
		!strings.Contains(text, "被偷了 1 次，共 1 個作物（小偷）") || !strings.Contains(text, "1 格作物成熟了") {
		t.Fatal(text)
	}
	e.clock.Advance(60)
	if l := must[WelcomeBack](t)(e.p.WelcomeBack(e.ctx, owner)).Lines; len(l) != 0 { // 剛看過，不再顯示
		t.Fatal(l)
	}
}

func TestProgressMarksShowNewCodex(t *testing.T) {
	e := setup(t)
	pid := e.player()
	before := must[ProgressMarks](t)(e.p.ProgressMarks(e.ctx, pid))
	e.grow(pid, 1, "wheat")
	e.clock.Advance(61)
	e.ok(e.s.HarvestCrop(e.ctx, pid, 1))
	after := must[ProgressMarks](t)(e.p.ProgressMarks(e.ctx, pid))
	if _, ok := before.Codex["crop:wheat"]; ok || after.Codex["crop:wheat"] != "小麥" || after.CodexTotal != before.CodexTotal {
		t.Fatalf("%+v", after.Codex)
	}
}

// ---------- 特賣與升級 ----------

func (e *env) buyer(home int64) int64 {
	p := e.farmer("買家", home)
	e.setGoldRecorded(p, 2_000_000) // 寫交易紀錄
	return p
}

func TestGreenhouseLevelsSpeedUpGrowth(t *testing.T) {
	e := setup(t)
	pid := e.buyer(e.group("主群組"))
	e.ok(e.s.BuySeeds(e.ctx, pid, "strawberry", 2))
	e.ok(e.plantWatered(pid, 1, "strawberry"))
	base := e.status(pid).Plots[0].RemainingSeconds
	for i, price := range specialshop.GreenhousePrices() {
		before := e.gold(pid)
		if r := e.ok(e.s.UpgradeGreenhouse(e.ctx, pid)); !strings.Contains(r.Message, strconv.Itoa(i+1)+" 級") || e.gold(pid) != before-price {
			t.Fatalf("%s gold=%d", r.Message, e.gold(pid))
		}
	}
	_, err := e.s.UpgradeGreenhouse(e.ctx, pid)
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "最高等級") {
		t.Fatal(err)
	}
	e.ok(e.plantWatered(pid, 2, "strawberry"))
	fast := e.status(pid).Plots[1].RemainingSeconds
	pctMax := specialshop.GreenhousePct(specialshop.GreenhouseMaxLevel())
	if fast != base*(100-pctMax)/100 {
		t.Fatalf("%d vs %d", fast, base)
	}
	if must[SpecialShopView](t)(e.p.SpecialShop(e.ctx, pid)).Greenhouse.NextPrice != nil || !e.goldMatchesTransactions() {
		t.Fatal("已滿級")
	}
}

func TestGreenhouseNeedsGold(t *testing.T) {
	e := setup(t)
	_, err := e.s.UpgradeGreenhouse(e.ctx, e.farmer("窮人", e.group("主群組")))
	wantKind(t, err, gameerr.InsufficientGold)
}

func TestWeeklyDealLimitResetsNextWeek(t *testing.T) {
	e := setup(t)
	pid := e.buyer(e.group("主群組"))
	view := must[SpecialShopView](t)(e.p.SpecialShop(e.ctx, pid)).Weekly
	var normal, rare WeeklyOfferView
	for _, o := range view.Offers {
		if o.Rare && rare.CropCode == "" {
			rare = o
		} else if !o.Rare && normal.CropCode == "" {
			normal = o
		}
	}
	_, err := e.p.BuyWeeklyDeal(e.ctx, pid, "no_such_crop", 1)
	wantKind(t, err, gameerr.NotFound)
	before := e.gold(pid)
	e.ok(e.p.BuyWeeklyDeal(e.ctx, pid, normal.CropCode, normal.Limit))
	if e.gold(pid) != before-normal.Price*normal.Limit {
		t.Fatal("扣款")
	}
	_, err = e.p.BuyWeeklyDeal(e.ctx, pid, normal.CropCode, 1)
	wantKind(t, err, gameerr.Validation)
	if !strings.Contains(err.Error(), "限購") {
		t.Fatal(err)
	}
	e.ok(e.p.BuyWeeklyDeal(e.ctx, pid, rare.CropCode, 2))
	_, err = e.p.BuyWeeklyDeal(e.ctx, pid, rare.CropCode, 2)
	if err == nil || !strings.Contains(err.Error(), "還可以買 1 顆") {
		t.Fatal(err)
	}
	found := false
	for _, s := range must[Backpack](t)(e.s.GetBackpack(e.ctx, pid)).Seeds {
		found = found || (s.Code == normal.CropCode && s.Quantity == normal.Limit)
	}
	if !found {
		t.Fatal("一般種子放進背包")
	}
	offers := map[string]WeeklyOfferView{}
	for _, o := range must[SpecialShopView](t)(e.p.SpecialShop(e.ctx, pid)).Weekly.Offers {
		offers[o.CropCode] = o
	}
	if offers[normal.CropCode].Bought != normal.Limit || offers[rare.CropCode].Bought != 2 {
		t.Fatalf("%+v", offers)
	}
	// 下一週換商品，購買量重新計算
	e.clock.Advance(7 * dayS)
	nxt := must[SpecialShopView](t)(e.p.SpecialShop(e.ctx, pid)).Weekly
	if nxt.WeekStart.Equal(view.WeekStart) {
		t.Fatal("換週")
	}
	for _, o := range nxt.Offers {
		if o.Bought != 0 {
			t.Fatalf("%+v", o)
		}
	}
	if !e.goldMatchesTransactions() {
		t.Fatal("金幣與交易紀錄不一致")
	}
}

func TestTitleShop(t *testing.T) {
	e := setup(t)
	home := e.group("主群組")
	pid := e.buyer(home)
	offer := specialshop.TitleShop()[0]
	before := e.gold(pid)
	if r := e.ok(e.p.BuyTitle(e.ctx, pid, offer.Title)); !strings.Contains(r.Message, offer.Title) || e.gold(pid) != before-offer.Price {
		t.Fatal(r.Message)
	}
	source := ""
	for _, o := range must[TitlesView](t)(e.p.Titles(e.ctx, pid)).Owned {
		if o.Title == offer.Title {
			source = o.Source
		}
	}
	if source != "稱號商店" {
		t.Fatal(source)
	}
	_, err := e.p.BuyTitle(e.ctx, pid, offer.Title)
	wantKind(t, err, gameerr.AlreadyExists)
	_, err = e.p.BuyTitle(e.ctx, pid, "免費稱號")
	wantKind(t, err, gameerr.NotFound)
	_, err = e.p.BuyTitle(e.ctx, e.farmer("窮人", home), offer.Title)
	wantKind(t, err, gameerr.InsufficientGold)
	e.ok(e.p.SetTitle(e.ctx, pid, offer.Title))
	if st := e.status(pid); st.Title == nil || *st.Title != offer.Title || !e.goldMatchesTransactions() {
		t.Fatal("選用買到的稱號")
	}
}

// 對應 Python progress_service.py 模組載入時的 assert：商店稱號與成就稱號不可重複。
func TestShopTitlesDoNotOverlapAchievementTitles(t *testing.T) {
	shop := map[string]bool{}
	for _, o := range specialshop.TitleShop() {
		shop[o.Title] = true
	}
	for _, a := range achievements.BaseAchievements {
		if a.Title != "" && shop[a.Title] {
			t.Fatalf("商店稱號與成就稱號重複：%s", a.Title)
		}
	}
}
