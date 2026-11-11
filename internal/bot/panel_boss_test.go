package bot

// 子面板第二部分（第 5 階段第 7 批）：tests/test_world_boss.py 的面板測試（test_panel_donate_flow_and_double_click、
// test_panel_other_guild_cannot_see_event、test_panel_boss_image_only_in_intro_area、
// test_panel_concurrent_double_click_no_error）。漁獲勾選捐獻（test_panel_fish_multi_select_donates_all）在釣魚測試。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"farmgm/internal/bot/ui"
	"farmgm/internal/bot/ui/uitest"
	"farmgm/internal/game/featurecfg"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/services"
)

func (e *botEnv) groupOf(guild string) int64 {
	e.t.Helper()
	g := must[*services.GroupInfo](e.t)(e.admin.GetGroupByGuild(e.ctx, guild))
	if g == nil {
		e.t.Fatal("沒有群組", guild)
	}
	return g.ID
}

// makeBoss Python make_boss：伺服器活動「深淵魔龍」，HP 100,000、72 小時；minPointsZero 時只捐少量也能領獎。
func (e *botEnv) makeBoss(groupID int64, minPointsZero bool) int64 {
	e.t.Helper()
	now := e.clock.Now()
	ends := now.Add(72 * time.Hour)
	in := services.BossInput{Name: "深淵魔龍", Level: 50, MaxHP: 100_000, StartsAt: &now, EndsAt: &ends, GroupID: &groupID}
	if minPointsZero {
		raw, _ := json.Marshal(wb.DefaultRewards())
		rewards, err := featurecfg.Decode(raw) // 數字保持整數（與服務層 toRawValue 相同）
		if err != nil {
			e.t.Fatal(err)
		}
		rewards["min_points"] = 0
		in.Rewards = rewards
	}
	return must[services.BossView](e.t)(e.admin.CreateWorldBoss(e.ctx, "admin", in)).EventID
}

func (e *botEnv) produceQty(pid int64) int {
	e.t.Helper()
	return must[services.Backpack](e.t)(e.admin.GetBackpack(e.ctx, pid)).Produce[0].Quantity
}

func TestPanelBossDonateFlowAndDoubleClick(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "阿明")
	pid := p.panel.playerID
	gid := e.groupOf(guildA)
	p.click("世界Boss")
	contains(t, p.Text, "目前沒有世界 Boss 活動")
	eid := e.makeBoss(gid, true)
	e.setInventory(pid, "carrot", 10)
	p.click("更新狀態")
	// 介紹區：Boss 簡介、我的參與與入口；補給細節在戰鬥區
	contains(t, p.Text, "深淵魔龍", "HP", "前往戰場", "我的參與", "（100.0%）")
	notContains(t, p.Text, "後勤補給進度")
	for _, l := range []string{"進入深淵魔龍", "上一場排行榜", "領取獎勵", "活動說明", "更新狀態"} {
		p.button(l)
	}
	p.click("進入深淵魔龍")
	contains(t, p.Text, "戰鬥區", "群組後勤補給進度", "糧食補給", "醫療補給", "戰鬥補給", "裝備補給", "前線戰況", "個人貢獻", "HP",
		"（0.0%）")
	for _, l := range []string{"捐獻物資", "領取獎勵", "更新狀態", "回介紹區", "目前排行"} {
		p.button(l)
	}
	p.click("捐獻物資")
	p.choose("捐獻類別", "crop")
	p.choose("選擇物品", "0")
	contains(t, p.Text, "胡蘿蔔", "持有 10")
	p.click("× 5")
	contains(t, p.Text, "確認捐獻")
	p.click("取消")
	contains(t, p.Text, "沒有扣除任何物品")
	if q := e.produceQty(pid); q != 10 {
		t.Fatal(q)
	}
	p.choose("選擇物品", "0")
	p.click("× 5")
	confirm := p.button("確認捐獻")
	first, second := p.interaction(), p.interaction()
	if !e.cmds.Registry.DispatchComponent(e.ctx, first, confirm.CustomID(), nil) {
		t.Fatal("確認沒有執行")
	}
	e.cmds.Registry.DispatchComponent(e.ctx, second, confirm.CustomID(), nil) // 舊畫面上的同一個按鈕再按一次
	p.update(first)
	contains(t, p.Text, "獲得 15 積分")
	if q := e.produceQty(pid); q != 5 {
		t.Fatal(q)
	}
	if v := must[*services.BossView](t)(e.admin.BossCurrent(e.ctx, &gid, pid, nil)); v.Me.Quantity != 5 {
		t.Fatal(v.Me.Quantity)
	}
	p.click("回戰鬥區")
	contains(t, p.Text, "戰鬥區", "貢獻積分 **15**")
	p.click("目前排行") // 戰鬥區：這一場目前的排行
	contains(t, p.Text, "目前貢獻排行", "阿明", "15 分")
	p.click("回戰鬥區")
	p.click("回介紹區")
	p.click("上一場排行榜") // 介紹區：上一場（還沒有已結束的活動）
	contains(t, p.Text, "還沒有已結束")
	notContains(t, p.Text, "阿明")
	p.click("回介紹區")
	p.click("活動說明")
	contains(t, p.Text, "補給類型與效果", "獎勵制度")
	p.click("回介紹區")
	must[services.BossView](t)(e.admin.WorldBossAction(e.ctx, "admin", eid, "end"))
	p.click("更新狀態")
	contains(t, p.Text, "已結束")
	p.click("上一場排行榜") // 這一場結束後就是「上一場」
	contains(t, p.Text, "上一場貢獻排行", "阿明", "15 分")
	p.click("回介紹區")
	p.click("領取獎勵")
	contains(t, p.Text, "領取「深淵魔龍」", "已領取")
	e.checkLedger()
}

func TestPanelBossOtherGuildCannotSeeEvent(t *testing.T) {
	e := setup(t)
	a := e.makePlayer(alice, guildA, "玩家")
	e.makeBoss(e.groupOf(guildA), false)
	b := e.makePlayer(bob, guildB, "玩家")
	b.click("世界Boss")
	contains(t, b.Text, "沒有世界 Boss 活動")
	a.click("世界Boss")
	contains(t, a.Text, "深淵魔龍")
}

func TestPanelBossImageOnlyInIntroArea(t *testing.T) {
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.makeBoss(e.groupOf(guildA), false)
	onMessage := []string{FarmImage}
	att := func(page string) *ui.Attachments {
		t.Helper()
		en := lastEdit(t, p.goWith(page, onMessage))
		return en.Message.Attachments
	}
	en := lastEdit(t, p.goWith("boss", onMessage))
	if a := en.Message.Attachments; a.Keep || len(a.Files) != 1 || en.Message.Embed.Image != "attachment://"+FarmImage { // 從總覽換過來：上傳一次
		t.Fatalf("%+v", a)
	}
	for _, page := range []string{"boss", "boss_board", "boss_help", "boss"} {
		if a := att(page); !a.Keep { // 介紹區同一張圖：沿用，不重新上傳
			t.Fatal(page, a)
		}
	}
	for _, page := range []string{"boss_battle", "boss_donate", "boss_live_board", "boss_battle"} {
		if a := att(page); a.Keep || len(a.Files) != 0 || len(p.panel.files) != 0 { // 戰鬥區：不附圖片
			t.Fatal(page, a)
		}
	}
	if a := att("boss"); a.Keep || len(a.Files) != 1 { // 從戰鬥區回介紹區：重新附上圖片
		t.Fatalf("%+v", a)
	}
	if a := att("backpack"); a.Keep || len(a.Files) != 0 { // 離開世界 Boss 頁：移除
		t.Fatalf("%+v", a)
	}
}

func TestPanelBossConcurrentDoubleClickNoError(t *testing.T) {
	// Python：discord.py 同時處理連點的兩次互動。Go 的同一面板回呼依序執行，第二次點擊舊按鈕時 custom_id 已失效；
	// 確認兩次點擊都不會顯示錯誤畫面，捐獻也只算一次。
	e := setup(t)
	p := e.makePlayer(alice, guildA, "玩家")
	e.makeBoss(e.groupOf(guildA), false)
	e.setInventory(p.panel.playerID, "carrot", 10)
	p.click("世界Boss")
	p.click("進入深淵魔龍")
	p.click("捐獻物資")
	p.choose("捐獻類別", "crop")
	p.choose("選擇物品", "0")
	p.click("× 5")
	confirm := p.button("確認捐獻")
	a, b := p.interaction(), p.interaction()
	e.cmds.Registry.DispatchComponent(e.ctx, a, confirm.CustomID(), nil)
	e.cmds.Registry.DispatchComponent(e.ctx, b, confirm.CustomID(), nil)
	for _, it := range []*uitest.Interaction{a, b} {
		for _, en := range it.Entries() {
			if en.Kind == "edit" && en.Message.Embed != nil && strings.HasPrefix(en.Message.Embed.Title, "⚠️") {
				t.Fatal(en.Message.Embed.Description)
			}
		}
	}
	if n := e.scalar("SELECT count(*) FROM world_boss_contributions"); n != int64(1) {
		t.Fatal(n)
	}
	if q := e.produceQty(p.panel.playerID); q != 5 {
		t.Fatal(q)
	}
	p.update(a)
	p.click("回戰鬥區")
	refresh := p.button("更新狀態")
	c, d := p.interaction(), p.interaction()
	e.cmds.Registry.DispatchComponent(e.ctx, c, refresh.CustomID(), nil)
	e.cmds.Registry.DispatchComponent(e.ctx, d, refresh.CustomID(), nil)
	for _, it := range []*uitest.Interaction{c, d} {
		for _, en := range it.Entries() {
			if en.Kind == "edit" && en.Message.Embed != nil && strings.HasPrefix(en.Message.Embed.Title, "⚠️") {
				t.Fatal(en.Message.Embed.Description)
			}
		}
	}
}

func TestPyFloat(t *testing.T) {
	for x, want := range map[float64]string{100: "100.0", 87.5: "87.5", 0: "0.0", 1.25: "1.25", 0.3: "0.3"} {
		if got := pyFloat(x); got != want {
			t.Fatal(x, got)
		}
	}
}
