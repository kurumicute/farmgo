package bot

// 子面板第三部分（第 5 階段第 7 批）：tests/test_discord_fishing.py 全部、tests/test_fishing_guard.py 的
// test_panel_shows_check_and_fixed_fight_buttons，以及 tests/test_world_boss.py 的 test_panel_fish_multi_select_donates_all。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/services"
)

var bambooRod = fishing.Effects{"reel_power": 10, "control_pct": 0}

// allIn Python ALL_IN：基礎成功率 100%、反應時間 15 秒。
func (e *botEnv) allIn() {
	e.t.Helper()
	s := fishing.DefaultSettings()
	s.BaseSuccessPct, s.ReactionSeconds = 100, 15
	data, _ := json.Marshal(s)
	raw, err := featurecfg.Decode(data)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.admin.UpdateFishingSettings(e.ctx, "test", raw); err != nil {
		e.t.Fatal(err)
	}
}

// fisher Python fisher：金幣 50,000、農場 Lv10；測試不等真實時間（關閉自動更新）。
func (e *botEnv) fisher(user, name string) *player {
	e.t.Helper()
	p := e.makePlayer(user, guildA, name)
	p.panel.fishTimersEnabled = false
	gold, level := 50_000, 10
	must[services.FarmValues](e.t)(e.admin.SetFarmValues(e.ctx, "test", p.panel.playerID, &gold, &level))
	return p
}

func (p *player) buyGear(code string, quantity int) {
	p.e.t.Helper()
	p.goPage("fish_shop")
	p.choose("選擇要買的釣具", "fish:"+code)
	if quantity == 0 {
		p.click("確認購買")
	} else {
		p.click(fmt.Sprintf("買 %d", quantity))
	}
	notContains(p.e.t, p.Text, "❌", "無法完成")
}

func (p *player) readyToFish() {
	p.buyGear("bamboo_rod", 0)
	p.buyGear("basic_line", 0)
	p.buyGear("worm_bait", 10)
}

func (e *botEnv) fishState(pid int64) services.FishingView {
	e.t.Helper()
	v := must[*services.FishingView](e.t)(e.admin.FishingState(e.ctx, pid))
	if v == nil {
		e.t.Fatal("沒有釣魚狀態")
	}
	return *v
}

// fightUntilDone 依畫面的 💬 提示操作，直到釣魚結束。
func (e *botEnv) fightUntilDone(p *player) services.FishingView {
	e.t.Helper()
	for range 200 {
		v := e.fishState(p.panel.playerID)
		if v.Phase != "fighting" {
			return v
		}
		p.click(fishing.ActionNames[fishing.SmartAction(v.PolicyState(), fishing.Difficulty{}, bambooRod)])
		e.clock.Advance(1)
	}
	e.t.Fatal("沒有結束")
	return services.FishingView{}
}

// landByService 直接以服務釣起一條魚（Python 測試中的 start → hook → smart_action 迴圈）。
func (e *botEnv) landByService(pid int64) services.FishingView {
	e.t.Helper()
	v := must[services.FishingView](e.t)(e.admin.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	v = must[services.FishingView](e.t)(e.admin.HookFish(e.ctx, pid, v.ID))
	for v.Phase == "fighting" {
		v = must[services.FishingView](e.t)(e.admin.FishingAction(e.ctx, pid, v.ID,
			fishing.SmartAction(v.PolicyState(), fishing.Difficulty{}, bambooRod), v.Turn))
	}
	if v.Status != "landed" {
		e.t.Fatal(v.Status)
	}
	return v
}

func TestFishShopHasTwoTabsAndGearAffectsOverview(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	p.goPage("shop")
	p.button("農場商店")
	p.button("釣具商店")
	contains(t, p.Text, "種子") // 原本的商品仍在
	p.click("釣具商店")
	contains(t, p.Text, "竹製魚竿", "傳說魚竿")
	notContains(t, p.Text, "🔒") // Lv.10 全部解鎖
	p.readyToFish()
	p.goPage("fishing")
	contains(t, p.Text, "竹製魚竿", "普通魚線", "**10** 個", "準備釣魚")
	p.goPage("fish_shop")
	p.click("農場商店")
	contains(t, p.Text, "農場商店", "種子")
}

func TestMainMenuHasFishingEntry(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	p.goPage("main")
	p.click("釣魚")
	contains(t, p.Text, "釣魚中心", "尚未裝備")
}

func TestFullFishingFlowSellAndShare(t *testing.T) {
	e := setup(t)
	e.allIn()
	p := e.fisher(alice, "玩家")
	pid := p.panel.playerID
	p.readyToFish()
	p.goPage("fishing")
	p.click("開始釣魚")
	contains(t, p.Text, "你已經拋出魚竿", "水面很平靜")
	if !p.button("拉竿").Disabled {
		t.Fatal("等魚上鉤時拉竿應停用")
	}
	v := e.fishState(pid)
	e.clock.Set(v.BiteAt)
	e.clock.Advance(0.5)
	p.goPage("fish_game")
	contains(t, p.Text, "魚上鉤了")
	if p.button("拉竿").Disabled {
		t.Fatal("拉竿應可按")
	}
	p.click("拉竿")
	contains(t, p.Text, "正在與魚搏鬥", "魚的體力", "魚線張力")
	for _, l := range []string{"收線", "放線", "等待"} {
		p.button(l)
	}
	result := e.fightUntilDone(p)
	if result.Status != "landed" {
		t.Fatal(result.Status)
	}
	contains(t, p.Text, "釣到了", "已放進魚簍")
	// 結算畫面：剩餘魚餌與魚簍容量（這隻魚已算進魚簍）
	o := must[services.FishingOverview](t)(e.admin.FishingOverview(e.ctx, pid))
	if o.Basket != 1 {
		t.Fatal(o.Basket)
	}
	contains(t, p.Text, fmt.Sprintf("剩 **%d** 個", o.BaitQuantity), fmt.Sprintf("**1 / %d** 隻", o.BasketCapacity),
		fmt.Sprintf("還能放 **%d** 隻", o.BasketCapacity-1))
	// 分享到頻道：公開訊息，魚不會消失
	share := p.click("分享到群組")
	var card *uiEntryText
	for _, en := range share.Entries() {
		if en.Kind == "followup" {
			if en.Message.Ephemeral {
				t.Fatal("分享應是公開訊息")
			}
			card = &uiEntryText{en.Text()}
		}
	}
	if card == nil {
		t.Fatal("沒有分享訊息")
	}
	contains(t, card.text, "捕獲玩家", result.Fish.Name, "價值")
	contains(t, p.Text, "已分享到這個頻道")
	// 魚簍：單獨出售需要確認
	p.goPage("fish_basket")
	contains(t, p.Text, result.Fish.Name, "1 / 20")
	p.choose("選一隻魚", fmt.Sprint(result.Fish.ID))
	p.click("出售")
	contains(t, p.Text, "再按一次")
	p.button("確認出售")
	gold := e.gold(pid)
	p.click("確認出售")
	if g := e.gold(pid); g != gold+result.Fish.Price {
		t.Fatal(g, gold)
	}
	contains(t, p.Text, "0 / 20")
	e.checkLedger()
}

type uiEntryText struct{ text string }

func TestFishMultiSelectAndSellAllNeedConfirmation(t *testing.T) {
	e := setup(t)
	e.allIn()
	p := e.fisher(alice, "玩家")
	pid := p.panel.playerID
	p.readyToFish()
	for range 3 {
		e.landByService(pid)
	}
	basket := must[services.FishBasket](t)(e.admin.FishBasket(e.ctx, pid)).Fish
	if len(basket) != 3 {
		t.Fatal(len(basket))
	}
	p.goPage("fish_basket")
	p.chooseMany("勾選", []string{fmt.Sprint(basket[0].ID), fmt.Sprint(basket[1].ID)})
	contains(t, p.Text, "確認出售")
	if !slices.ContainsFunc(p.labels(), func(l string) bool { return strings.HasPrefix(l, "確認出售 2 隻") }) {
		t.Fatal(p.labels())
	}
	p.click("確認出售 2 隻")
	if n := len(must[services.FishBasket](t)(e.admin.FishBasket(e.ctx, pid)).Fish); n != 1 {
		t.Fatal(n)
	}
	p.click("全部出售")
	contains(t, p.Text, "再按一次")
	p.click("確認全部出售")
	if n := len(must[services.FishBasket](t)(e.admin.FishBasket(e.ctx, pid)).Fish); n != 0 {
		t.Fatal(n)
	}
	e.checkLedger()
}

func TestMissedBiteShowsFailure(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	p.readyToFish()
	p.goPage("fishing")
	p.click("開始釣魚")
	v := e.fishState(p.panel.playerID)
	e.clock.Set(v.ReactDeadline)
	e.clock.Advance(10)
	p.goPage("fish_game")
	contains(t, p.Text, "沒有及時拉竿", "魚餌", "魚簍") // 沒釣到也顯示剩餘資源
	p.button("再釣一次")
}

func TestFishDoubleClickDoesNotRepeatAction(t *testing.T) {
	e := setup(t)
	e.allIn()
	p := e.fisher(alice, "玩家")
	pid := p.panel.playerID
	p.readyToFish()
	v := must[services.FishingView](t)(e.admin.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	p.goPage("fish_game")
	p.click("拉竿")
	oldWait := p.button("等待")
	p.click("等待")
	if s := e.fishState(pid); s.Turn != 1 {
		t.Fatal(s.Turn)
	}
	// 舊畫面上的同一個按鈕再按一次：Go 重繪後舊的 custom_id 已失效，不會執行；
	// 另外直接以舊回合呼叫一次（Python 的情況：回呼仍在），服務層回傳 stale，畫面提示已處理過
	it := p.interaction()
	e.cmds.Registry.DispatchComponent(e.ctx, it, oldWait.CustomID(), nil)
	if err := oldWait.OnClick(e.ctx, it); err != nil {
		t.Fatal(err)
	}
	p.update(it)
	if s := e.fishState(pid); s.Turn != 1 {
		t.Fatal(s.Turn)
	}
	contains(t, p.Text, "已經處理過了")
}

func TestOtherPlayerCannotUseMyFishingPanel(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	p.goPage("fishing")
	intruder := &player{e: e, user: bob, guild: guildA, name: "壞人", panel: p.panel}
	if p.panel.interactionCheck(e.ctx, intruder.interaction()) {
		t.Fatal("別人不能操作")
	}
}

func TestFishingPagesRenderWithinComponentLimits(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	for _, page := range []string{"fishing", "fish_shop", "fish_basket", "fish_codex", "fish_rank"} {
		p.goPage(page)
		notContains(t, p.Text, "無法顯示")
		width := map[int]int{}
		for _, it := range p.panel.View.Items() {
			if it.ItemRow() > 4 {
				t.Fatal(page, it.ItemRow())
			}
			if _, ok := it.(*ui.Select); ok {
				width[it.ItemRow()] += 5
			} else {
				width[it.ItemRow()]++
			}
		}
		for row, w := range width {
			if w > 5 {
				t.Fatal(page, row, w)
			}
		}
	}
}

func TestFishCodexAndRankPages(t *testing.T) {
	e := setup(t)
	e.allIn()
	p := e.fisher(alice, "玩家")
	pid := p.panel.playerID
	p.readyToFish()
	v := must[services.FishingView](t)(e.admin.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	must[services.FishingView](t)(e.admin.HookFish(e.ctx, pid, v.ID))
	p.goPage("fish_game")
	result := e.fightUntilDone(p)
	p.goPage("fish_codex")
	contains(t, p.Text, "已發現 **1 /", result.Fish.Name, "❓")
	p.goPage("fish_rank")
	contains(t, p.Text, "本伺服器", "玩家")
	p.click("看全部玩家")
	contains(t, p.Text, "全部玩家")
}

func TestAutoRefreshDoesNotRepeatForSamePastDeadline(t *testing.T) {
	e := setup(t)
	p := e.fisher(alice, "玩家")
	panel := p.panel
	panel.fishTimersEnabled, panel.Page = true, "fish_game"
	past := time.Now().Add(-10 * time.Second)
	panel.fishDue = &fishDue{past, 0}
	panel.scheduleFishTimer(p.interaction())
	first := panel.fishTimer
	if first == nil {
		t.Fatal("第一次應排定自動更新")
	}
	first.Stop() // 測試不真的更新畫面
	panel.fishTimer = nil
	panel.fishDue = &fishDue{past, 0}
	panel.scheduleFishTimer(p.interaction())
	if panel.fishTimer != nil {
		t.Fatal("同一個過去的期限不應再排定")
	}
}

func TestAutoRefreshUpdatesFishGameWhenBiteArrives(t *testing.T) {
	// Go 版：計時器到期時經 Registry.RunFor 重繪釣魚畫面（真實時間，期限設在 50 毫秒後）
	e := setup(t)
	e.allIn()
	p := e.fisher(alice, "玩家")
	p.readyToFish()
	p.goPage("fishing")
	p.click("開始釣魚")
	panel := p.panel
	panel.fishTimersEnabled = true
	done := make(chan struct{})
	it := p.interaction()
	e.cmds.Registry.RunFor(e.ctx, it, panel.View, func(ctx context.Context) error {
		panel.fishDue = &fishDue{time.Now().Add(50 * time.Millisecond), 0}
		panel.scheduleFishTimer(it)
		return nil
	})
	go func() {
		for range 200 {
			time.Sleep(10 * time.Millisecond)
			n := 0
			for _, en := range it.Entries() {
				if en.Kind == "edit" {
					n++
				}
			}
			if n > 0 {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("沒有自動更新")
	}
	e.cmds.Registry.RunFor(e.ctx, p.interaction(), panel.View, func(context.Context) error {
		panel.cancelFishTimer()
		return nil
	})
}

func TestPanelShowsCheckAndFixedFightButtons(t *testing.T) {
	e := setup(t)
	old := fishing.GuardEnabled
	fishing.GuardEnabled = true // Python test_fishing_guard.py 的 fixture 開啟防腳本
	t.Cleanup(func() { fishing.GuardEnabled = old })
	p := e.makePlayer(alice, guildA, "玩家")
	p.panel.fishTimersEnabled = false
	pid := p.panel.playerID
	e.allIn()
	e.setGold(pid, 10_000)
	for _, g := range []struct {
		code string
		q    int
	}{{"bamboo_rod", 1}, {"basic_line", 1}, {"worm_bait", 30}} {
		must[services.ActionResult](t)(e.admin.BuyFishingGear(e.ctx, pid, g.code, g.q))
	}
	// 拉扯畫面：五個操作的位置固定，不會交換
	for range 12 {
		v := must[services.FishingView](t)(e.admin.StartFishing(e.ctx, pid))
		e.clock.Set(v.BiteAt)
		e.clock.Advance(1)
		must[services.FishingView](t)(e.admin.HookFish(e.ctx, pid, v.ID))
		p.goPage("fish_game")
		if got := p.labels()[:5]; !slices.Equal(got, []string{"收線", "放線", "等待", "左壓", "右壓"}) {
			t.Fatal(got)
		}
		must[services.FishingView](t)(e.admin.GiveUpFishing(e.ctx, pid, v.ID))
	}
	// 人機確認畫面：題目與 emoji 按鈕；按對就拋竿
	e.exec("UPDATE fishing_guards SET casts_since_check = check_after")
	p.goPage("fishing")
	p.click("開始釣魚")
	contains(t, p.Text, "人機確認")
	answer, _ := e.scalar("SELECT challenge->>'answer' FROM fishing_guards").(string)
	creature := fishing.GuardCreatures[answer]
	contains(t, p.Text, creature.Name)
	var target *ui.Button
	for _, b := range p.panel.View.Buttons() {
		if b.Emoji == creature.Emoji {
			target = b
		}
	}
	if target == nil {
		t.Fatal("找不到答案按鈕")
	}
	it := p.interaction()
	e.cmds.Registry.DispatchComponent(e.ctx, it, target.CustomID(), nil)
	p.update(it)
	contains(t, p.Text, "你已經拋出魚竿")
}

func TestPanelBossFishMultiSelectDonatesAll(t *testing.T) {
	// 漁獲用勾選：勾選的魚種全部捐出，一次確認；連點確認只捐一次，取消不扣。
	e := setup(t)
	e.allIn()
	p := e.makePlayer(alice, guildA, "玩家")
	p.panel.fishTimersEnabled = false
	pid := p.panel.playerID
	e.setGold(pid, 5000)
	for _, g := range []struct {
		code string
		q    int
	}{{"bamboo_rod", 1}, {"basic_line", 1}, {"worm_bait", 5}} {
		must[services.ActionResult](t)(e.admin.BuyFishingGear(e.ctx, pid, g.code, g.q))
	}
	var caught []services.FishingView
	for range 4 {
		caught = append(caught, e.landByService(pid))
	}
	gid := e.groupOf(guildA)
	eid := e.makeBoss(gid, false)
	p.click("世界Boss")
	p.click("進入深淵魔龍")
	p.click("捐獻物資")
	p.choose("捐獻類別", "fish")
	contains(t, p.Text, "勾選要捐的魚種")
	values := p.options("勾選要全部捐出的魚種")
	if len(values) == 0 {
		t.Fatal("沒有魚種")
	}
	p.chooseMany("勾選要全部捐出的魚種", values) // 全部勾選
	contains(t, p.Text, "確認捐獻以下", "共 4 隻")
	p.click("取消")
	contains(t, p.Text, "沒有扣除任何物品")
	if n := e.scalar("SELECT count(*) FROM farm_fish WHERE donated_at IS NOT NULL"); n != int64(0) {
		t.Fatal(n)
	}
	p.choose("捐獻類別", "fish")
	p.chooseMany("勾選要全部捐出的魚種", values)
	confirm := p.button("確認捐獻")
	first, second := p.interaction(), p.interaction()
	e.cmds.Registry.DispatchComponent(e.ctx, first, confirm.CustomID(), nil)
	e.cmds.Registry.DispatchComponent(e.ctx, second, confirm.CustomID(), nil) // 舊畫面上的同一個按鈕再按一次
	p.update(first)
	expected := 0
	for _, c := range caught {
		expected += wb.PricePoints(c.Fish.Price, 10)
	}
	if n := e.scalar("SELECT count(*) FROM farm_fish WHERE donated_at IS NOT NULL"); n != int64(4) {
		t.Fatal(n)
	}
	if n := e.scalar("SELECT count(*) FROM world_boss_contributions"); n != int64(len(values)) {
		t.Fatal(n)
	}
	if v := must[*services.BossView](t)(e.admin.BossCurrent(e.ctx, &gid, pid, &eid)); v.Me.Points != expected {
		t.Fatal(v.Me.Points, expected)
	}
	if opts := must[[]services.BossDonationOption](t)(e.admin.BossDonationOptions(e.ctx, &gid, pid, eid, "fish")); len(opts) != 0 {
		t.Fatal(opts)
	}
}
