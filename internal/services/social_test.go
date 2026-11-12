package services

// 對應 Python tests/test_social.py。

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/leaderboard"
)

// world 農場主要群組：a、b、c 屬 g1；d 屬 g2；e 屬 g3。另外 a 也加入 g2（但農場仍屬 g1）。
type world struct {
	a, b, c, d, e, g1, g2, g3 int64
}

func (e *env) world() world {
	e.t.Helper()
	w := world{g1: e.group("一號群"), g2: e.group("二號群"), g3: e.group("三號群")}
	w.a, w.b, w.c = e.farmer("a", w.g1), e.farmer("b", w.g1), e.farmer("c", w.g1)
	w.d, w.e = e.farmer("d", w.g2), e.farmer("e", w.g3)
	must[bool](e.t)(e.so.AddMember(e.ctx, w.g2, w.a))
	return w
}

func (e *env) plot(pid int64, n int) PlotInfo {
	e.t.Helper()
	return e.status(pid).Plots[n-1]
}

func (e *env) steal(g, thief, victim int64, plot, n int) ActionResult {
	e.t.Helper()
	return e.ok(e.so.StealCrop(e.ctx, g, thief, victim, plot, n))
}

func (e *env) targets(g, pid int64) []StealTarget {
	e.t.Helper()
	return must[[]StealTarget](e.t)(e.so.StealTargets(e.ctx, g, pid))
}

type pp struct {
	pid  int64
	plot int
}

func targetSet(ts []StealTarget) []pp {
	var out []pp
	for _, t := range ts {
		out = append(out, pp{t.PlayerID, t.PlotNumber})
	}
	return out
}

func (e *env) stealRow(g, thief, victim int64) StealPlayer {
	e.t.Helper()
	for _, r := range must[[]StealPlayer](e.t)(e.so.StealPlayers(e.ctx, g, thief)) {
		if r.PlayerID == victim {
			return r
		}
	}
	e.t.Fatal("名單中沒有這位玩家")
	return StealPlayer{}
}

func (e *env) farmView(g, thief, victim int64) StealFarmView {
	e.t.Helper()
	return must[StealFarmView](e.t)(e.so.StealFarmView(e.ctx, g, thief, victim))
}

// ---------- 群組 ----------

func TestMultipleGroupsAndPlayerInManyGroups(t *testing.T) {
	e := setup(t)
	w := e.world()
	var got []string
	for _, g := range must[[]GroupInfo](t)(e.so.ListGroups(e.ctx, &w.a)) {
		got = append(got, g.Name+":"+strconv.Itoa(g.MemberCount)+":"+boolStr(g.IsMember))
	}
	if !slices.Equal(got, []string{"一號群:3:true", "二號群:2:true", "三號群:1:false"}) {
		t.Fatal(got)
	}
	var mem []bool
	for _, g := range must[[]GroupInfo](t)(e.so.ListGroups(e.ctx, &w.b)) {
		mem = append(mem, g.IsMember)
	}
	if !slices.Equal(mem, []bool{true, false, false}) {
		t.Fatal(mem)
	}
	if added := must[bool](t)(e.so.AddMember(e.ctx, w.g1, w.a)); added { // 重複加入不會重複建立
		t.Fatal("重複加入")
	}
	members := must[[]groups.Member](t)(e.so.GroupMembers(e.ctx, w.g1))
	if len(members) != 3 || members[0].PlayerName != "a" || members[2].PlayerName != "c" || members[0].Farm.Gold != 500 {
		t.Fatalf("%+v", members)
	}
}

func TestRemoveMemberKeepsFarmAndHistory(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.c, 1, "wheat")
	if !must[bool](t)(e.so.RemoveMember(e.ctx, w.g1, w.c)) {
		t.Fatal("沒有移除")
	}
	st := e.status(w.c)
	if st.Gold != 490 || st.Plots[0].State != rules.PlotGrowing ||
		e.scalar("SELECT count(*) FROM game_transactions WHERE farm_id = $1", st.FarmID) != 4 {
		t.Fatalf("%+v", st)
	}
	names := func(g *int64, p *int64) []string {
		var out []string
		for _, r := range must[[]leaderboard.Row](t)(e.so.Leaderboard(e.ctx, "money", g, p, 10)) {
			out = append(out, r.PlayerName)
		}
		return out
	}
	if slices.Contains(names(&w.g1, &w.a), "c") || !slices.Contains(names(nil, nil), "c") {
		t.Fatal("排行不對")
	}
}

func TestDiscordGuildMapping(t *testing.T) {
	e := setup(t)
	guild := "123456789012345678"
	g := must[GroupInfo](t)(e.so.CreateGroup(e.ctx, "Discord 伺服器", &guild))
	if got := must[*GroupInfo](t)(e.so.GetGroupByGuild(e.ctx, guild)); got.ID != g.ID {
		t.Fatal(got)
	}
	if got := must[*GroupInfo](t)(e.so.GetGroupByGuild(e.ctx, "999")); got != nil {
		t.Fatal(got)
	}
	_, err := e.so.CreateGroup(e.ctx, "重複", &guild)
	wantKind(t, err, gameerr.AlreadyExists)
	bad := "abc"
	_, err = e.so.CreateGroup(e.ctx, "非法", &bad)
	wantKind(t, err, gameerr.Validation)
}

func TestExternalPlayerAndFarm(t *testing.T) {
	// Go 版補充：外部平台建立玩家、群組與農場（Python 在 Discord 測試中驗證）
	e := setup(t)
	fs := must[FarmStatus](t)(e.so.StartExternalFarm(e.ctx, "discord", "1234567890", "  小\x01明  ", "42", "我的伺服器", ""))
	if fs.PlayerName != "小明" || fs.Name != "小明的農場" || fs.HomeGroupID == nil {
		t.Fatalf("%+v", fs)
	}
	again := must[int64](t)(e.so.EnsureExternalPlayer(e.ctx, "discord", "1234567890", "別的名字"))
	if again != fs.PlayerID {
		t.Fatal("重複建立玩家")
	}
	guild := "42"
	ec := must[ExternalContext](t)(e.so.ExternalContext(e.ctx, "discord", "1234567890", &guild, "改名的伺服器"))
	if *ec.GroupID != *fs.HomeGroupID || *ec.GroupName != "改名的伺服器" || !ec.HasFarm || *ec.PlayerID != fs.PlayerID {
		t.Fatalf("%+v", ec)
	}
	if n := must[int64](t)(e.so.EnsureExternalPlayer(e.ctx, "discord", "999888777666", "")); n == 0 ||
		must[PlayerInfo](t)(e.s.GetPlayer(e.ctx, n)).DisplayName != "玩家777666" {
		t.Fatal("預設名稱不對")
	}
}

// ---------- 偷菜目標 ----------

func TestStealTargetsOnlyValidPlotsInCurrentGroup(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.c, 1, "strawberry")
	e.clock.Advance(600 + balance.WitherSeconds) // c 的草莓已乾枯
	e.grow(w.b, 1, "wheat")                      // 成熟
	e.grow(w.a, 1, "wheat")                      // 自己的
	e.grow(w.d, 1, "wheat")                      // 其他群組
	e.grow(w.c, 2, "wheat")                      // 之後收成 → 空地
	e.clock.Advance(60)
	e.grow(w.b, 2, "carrot") // 生長中
	e.ok(e.s.BuySeeds(e.ctx, w.b, "tomato", 1))
	e.ok(e.s.PlantCrop(e.ctx, w.b, 3, "tomato")) // 等待澆水
	e.ok(e.s.HarvestCrop(e.ctx, w.c, 2))
	if e.plot(w.c, 1).State != rules.PlotWithered {
		t.Fatal("沒有乾枯")
	}
	ts := e.targets(w.g1, w.a)
	if len(ts) != 1 || ts[0] != (StealTarget{w.b, "b", "b的農場", 1, "小麥", 2, 1}) { // 小麥每次最多偷 1 個
		t.Fatalf("%+v", ts)
	}
	if got := targetSet(e.targets(w.g2, w.a)); !slices.Equal(got, []pp{{w.d, 1}}) { // 另一個群組只看得到該群組成員
		t.Fatal(got)
	}
	_, err := e.so.StealTargets(e.ctx, w.g1, w.e) // 非成員不能查看
	wantKind(t, err, gameerr.NotGroupMember)
}

func TestStealTargetMaxRespectsRemainingAndCapacity(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "wheat")
	e.clock.Advance(60)
	e.steal(w.g1, w.c, w.b, 1, 1)
	if ts := e.targets(w.g1, w.a); ts[0].MaxSteal != 1 { // 只剩 1 個
		t.Fatalf("%+v", ts)
	}
	e.grow(w.b, 2, "strawberry")
	e.clock.Advance(600)
	e.setGold(w.a, 10_000)
	e.setInv(w.a, "carrot", "normal", "none", 49) // 背包 49/50（種子放素材背包，改用農產品佔倉庫）
	var t2 StealTarget
	for _, tg := range e.targets(w.g1, w.a) {
		if tg.PlotNumber == 2 {
			t2 = tg
		}
	}
	if t2.RemainingQuantity != 5 || t2.MaxSteal != 1 {
		t.Fatalf("%+v", t2)
	}
	e.setInv(w.a, "carrot", "normal", "none", 50) // 背包滿
	if ts := e.targets(w.g1, w.a); len(ts) != 0 {
		t.Fatal(ts)
	}
}

func TestAlreadyStolenPlotNotListedAgain(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "strawberry")
	e.clock.Advance(600)
	e.steal(w.g1, w.a, w.b, 1, 2)
	if len(e.targets(w.g1, w.a)) != 0 || e.targets(w.g1, w.c)[0].RemainingQuantity != 3 {
		t.Fatal("偷過的土地仍列出")
	}
}

// ---------- 偷菜操作 ----------

func TestStealSuccessUpdatesBothSides(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "strawberry")
	e.clock.Advance(600)
	r := e.steal(w.g1, w.a, w.b, 1, 2)
	if r.Quantity != 2 || !strings.Contains(r.Message, "偷走 2 個草莓") || e.qty(w.a, "strawberry") != 2 ||
		*e.plot(w.b, 1).RemainingQuantity != 3 {
		t.Fatal(r.Message)
	}
	if e.scalar(`SELECT count(*) FROM steal_records WHERE group_id = $1 AND thief_player_id = $2 AND victim_player_id = $3
		AND plot_number = 1 AND quantity = 2 AND created_at = $4`, w.g1, w.a, w.b, e.clock.Now()) != 1 ||
		e.scalar("SELECT count(DISTINCT event_type) FROM game_transactions WHERE event_type IN ('steal', 'stolen')") != 2 {
		t.Fatal("紀錄不對")
	}
	if r := e.ok(e.s.HarvestCrop(e.ctx, w.b, 1)); r.Quantity != 3 { // 主人收成拿到剩下的 3 個
		t.Fatal(r.Quantity)
	}
}

func TestStealUntilEmptyResetsPlot(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "carrot") // 剩餘 3
	e.clock.Advance(180)
	e.steal(w.g1, w.c, w.b, 1, 1)
	if r := e.steal(w.g1, w.a, w.b, 1, 2); !strings.Contains(r.Message, "恢復為空地") || e.plot(w.b, 1).State != rules.PlotEmpty {
		t.Fatal(r.Message)
	}
	_, err := e.s.HarvestCrop(e.ctx, w.b, 1)
	wantKind(t, err, gameerr.PlotEmpty)
}

func TestStealValidation(t *testing.T) {
	e := setup(t)
	w := e.world()
	a, b, d, g1 := w.a, w.b, w.d, w.g1
	e.grow(b, 1, "wheat")
	e.grow(d, 1, "strawberry")
	e.ok(e.s.BuySeeds(e.ctx, b, "wheat", 1))
	e.ok(e.s.PlantCrop(e.ctx, b, 2, "wheat")) // 等待澆水
	e.grow(b, 3, "tomato")
	e.clock.Advance(301)
	e.grow(b, 4, "carrot") // 生長中
	for _, bad := range []int{0, 3, -1} {
		_, err := e.so.StealCrop(e.ctx, g1, a, b, 1, bad)
		wantKind(t, err, gameerr.Validation)
	}
	_, err := e.so.StealCrop(e.ctx, g1, b, b, 1, 1)
	wantKind(t, err, gameerr.Validation)
	// 猜測其他群組玩家的 ID 與土地編號也無法繞過
	_, err = e.so.StealCrop(e.ctx, g1, a, d, 1, 1)
	wantKind(t, err, gameerr.NotGroupMember)
	if strings.Contains(err.Error(), "草莓") {
		t.Fatal(err)
	}
	_, err = e.so.StealCrop(e.ctx, g1, w.e, b, 3, 1)
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.so.StealCrop(e.ctx, g1, a, b, 2, 1)
	wantKind(t, err, gameerr.PlotNotWatered)
	_, err = e.so.StealCrop(e.ctx, g1, a, b, 4, 1)
	wantKind(t, err, gameerr.PlotNotReady)
	e.steal(g1, a, b, 3, 1)
	_, err = e.so.StealCrop(e.ctx, g1, a, b, 3, 1) // 重複指令
	wantKind(t, err, gameerr.AlreadyStolen)
	_, err = e.so.StealCrop(e.ctx, g1, a, b, 1, 1) // 今天已偷過 b 的 1 格田
	wantMsg(t, err, gameerr.AlreadyStolen, "每天只能偷同一位玩家 1 格田")
	// 背包滿（另一位偷菜者 c，今天還沒偷過 b）
	e.setGold(w.c, 10_000)
	e.give(w.c, "carrot", 50-must[Inventory](t)(e.s.GetInventory(e.ctx, w.c)).Used)
	_, err = e.so.StealCrop(e.ctx, g1, w.c, b, 1, 1)
	wantKind(t, err, gameerr.WarehouseFull)
	e.clock.Advance(600 + balance.WitherSeconds)
	_, err = e.so.StealCrop(e.ctx, g1, w.c, b, 1, 1)
	wantKind(t, err, gameerr.PlotWithered)
}

func TestStealOnePlotPerVictimPerDay(t *testing.T) {
	e := setup(t)
	w := e.world()
	g1, a, b, c := w.g1, w.a, w.b, w.c
	e.grow(b, 1, "wheat")
	e.grow(b, 2, "wheat")
	e.grow(c, 1, "wheat")
	e.clock.Advance(60)
	if got := targetSet(e.targets(g1, a)); !slices.Equal(got, []pp{{b, 1}, {b, 2}, {c, 1}}) {
		t.Fatal(got)
	}
	e.steal(g1, a, b, 1, 1)
	if got := targetSet(e.targets(g1, a)); !slices.Equal(got, []pp{{c, 1}}) { // b 的其他土地不再列出
		t.Fatal(got)
	}
	_, err := e.so.StealCrop(e.ctx, g1, a, b, 2, 1)
	wantKind(t, err, gameerr.AlreadyStolen)
	e.steal(g1, a, c, 1, 1)
	// 台灣時間隔天 0 點後重新計算（測試起點為台灣 08:00，再過 16 小時即隔天 0 點）
	e.clock.Advance(16*3600 - 60)
	e.grow(b, 3, "wheat")
	e.clock.Advance(60)
	if !e.steal(g1, a, b, 3, 1).Success {
		t.Fatal("隔天仍不能偷")
	}
}

func TestVictimStolenByAtMostThreePeoplePerDay(t *testing.T) {
	e := setup(t)
	w := e.world()
	g1, victim := w.g1, w.b
	thieves := []int64{w.a, w.c, e.farmer("t3", g1), e.farmer("t4", g1)}
	for plot := 1; plot <= 4; plot++ {
		e.grow(victim, plot, "wheat")
	}
	e.clock.Advance(60)
	for i := range 3 {
		if !e.steal(g1, thieves[i], victim, i+1, 1).Success {
			t.Fatal(i)
		}
	}
	fourth := thieves[3]
	for _, tg := range e.targets(g1, fourth) {
		if tg.PlayerID == victim {
			t.Fatal("清單仍列出")
		}
	}
	if row := e.stealRow(g1, fourth, victim); !row.VictimFull || row.LimitReached || row.Stealable != 0 {
		t.Fatalf("%+v", row)
	}
	if v := e.farmView(g1, fourth, victim); !v.VictimFull || v.LimitReached || len(v.Targets) != 0 {
		t.Fatalf("%+v", v)
	}
	_, err := e.so.StealCrop(e.ctx, g1, fourth, victim, 4, 1)
	wantMsg(t, err, gameerr.AlreadyStolen, "最多被 3 人偷")
	if e.farmView(g1, thieves[0], victim).VictimFull { // 已經偷過的人看到的是自己的每日上限
		t.Fatal("已偷過的人不該看到人數已滿")
	}
	e.clock.Advance(16*3600 - 60)
	if !e.steal(g1, fourth, victim, 4, 1).Success {
		t.Fatal("隔天仍不能偷")
	}
}

func TestStealPlayersAndFarmView(t *testing.T) {
	e := setup(t)
	w := e.world()
	g1, a, b, d := w.g1, w.a, w.b, w.d
	e.grow(b, 1, "wheat")
	e.grow(b, 2, "wheat")
	e.clock.Advance(60)
	rows := must[[]StealPlayer](t)(e.so.StealPlayers(e.ctx, g1, a))
	// Go 版：農田全是空地的玩家（c）不列出（Python 會列出 c，可偷 0 格）
	if len(rows) != 1 || rows[0].PlayerID != b || rows[0].Stealable != 2 || rows[0].LimitReached {
		t.Fatalf("%+v", rows)
	}
	v := e.farmView(g1, a, b)
	if !slices.Equal(targetSet(v.Targets), []pp{{b, 1}, {b, 2}}) || len(v.Plots) != 4 || v.LimitReached {
		t.Fatalf("%+v", v)
	}
	e.steal(g1, a, b, 1, 1)
	if v := e.farmView(g1, a, b); !v.LimitReached || len(v.Targets) != 0 || !e.stealRow(g1, a, b).LimitReached {
		t.Fatalf("%+v", v)
	}
	_, err := e.so.StealFarmView(e.ctx, g1, a, d) // 只能看目前群組的成員
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.so.StealFarmView(e.ctx, w.g3, a, b)
	wantKind(t, err, gameerr.NotGroupMember)
	_, err = e.so.StealFarmView(e.ctx, g1, a, a) // 不能看自己
	wantKind(t, err, gameerr.Validation)
}

func TestStealMoreThanRemaining(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "carrot")
	e.clock.Advance(180)
	e.steal(w.g1, w.c, w.b, 1, 2)
	_, err := e.so.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 2)
	wantKind(t, err, gameerr.InsufficientItems)
	if *e.plot(w.b, 1).RemainingQuantity != 1 {
		t.Fatal("剩餘數量被改")
	}
}

func TestWheatCanOnlyBeStolenOneAtATime(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "wheat")
	e.grow(w.b, 2, "carrot")
	e.clock.Advance(180)
	caps := map[int]int{}
	for _, tg := range e.targets(w.g1, w.a) {
		caps[tg.PlotNumber] = tg.MaxSteal
	}
	if caps[1] != 1 || caps[2] != 2 || len(caps) != 2 {
		t.Fatal(caps)
	}
	_, err := e.so.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 2)
	wantMsg(t, err, gameerr.Validation, "小麥每次最多只能偷 1 個")
	if e.steal(w.g1, w.a, w.b, 1, 1).Quantity != 1 {
		t.Fatal("偷不到")
	}
}

func (e *env) giveGlove(pid int64) {
	e.t.Helper()
	e.exec("UPDATE farms SET gold = gold + 500000 WHERE player_id = $1", pid)
	if r := e.ok(e.s.BuyItem(e.ctx, pid, balance.ToolThiefGlove, 1)); r.GoldDelta != -500_000 {
		e.t.Fatal(r.GoldDelta)
	}
}

func TestThiefGloveRemovesDailyLimits(t *testing.T) {
	e := setup(t)
	w := e.world()
	g1, a, b, c := w.g1, w.a, w.b, w.c
	if balance.Items[balance.ToolThiefGlove].Price != 500_000 || balance.Items[balance.ToolAutoPlanter].Price != 30_000 ||
		balance.Items[balance.ToolAutoSeller].Price != 150_000 {
		t.Fatal("價格不對")
	}
	extra := []int64{e.farmer("t3", g1), e.farmer("t4", g1)}
	for plot := 1; plot <= 4; plot++ {
		e.grow(b, plot, "carrot")
	}
	e.grow(c, 1, "carrot")
	e.clock.Advance(180)
	for i, thief := range []int64{c, extra[0], extra[1]} { // b 先被 3 個人偷滿
		if !e.steal(g1, thief, b, i+1, 1).Success {
			t.Fatal(i)
		}
	}
	e.giveGlove(a)
	// 有手套：不受人數上限，同一位玩家的每一格都能偷（每格 2 個）
	v := e.farmView(g1, a, b)
	if !v.Glove || v.VictimFull || !slices.Equal(targetSet(v.Targets), []pp{{b, 1}, {b, 2}, {b, 3}, {b, 4}}) {
		t.Fatalf("%+v", v)
	}
	if row := e.stealRow(g1, a, b); row.Stealable != 4 || row.LimitReached || row.VictimFull {
		t.Fatalf("%+v", row)
	}
	for plot := 1; plot <= 4; plot++ {
		n := 1
		if plot == 4 {
			n = 2
		}
		if !e.steal(g1, a, b, plot, n).Success {
			t.Fatal(plot)
		}
	}
	if !e.steal(g1, a, c, 1, 2).Success {
		t.Fatal("偷不到 c")
	}
	_, err := e.so.StealCrop(e.ctx, g1, a, b, 4, 1) // 每株作物仍只能偷一次
	wantMsg(t, err, gameerr.AlreadyStolen, "每株作物每人只能偷一次")
	if len(e.farmView(g1, a, b).Targets) != 0 {
		t.Fatal("仍列出")
	}
	// 停用手套後恢復每日限制
	e.ok(e.s.SetToolEnabled(e.ctx, a, "扒手手套", false))
	e.grow(c, 2, "carrot")
	e.clock.Advance(180)
	_, err = e.so.StealCrop(e.ctx, g1, a, c, 2, 1)
	wantMsg(t, err, gameerr.AlreadyStolen, "每天只能偷同一位玩家")
}

func concurrently(fns ...func() (ActionResult, error)) []outcome {
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make([]outcome, len(fns))
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := fn()
			out[i] = outcome{r, err}
		}()
	}
	close(start)
	wg.Wait()
	return out
}

func TestConcurrentStealsNeverDuplicate(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "carrot") // 剩餘 3
	e.clock.Advance(180)
	e.steal(w.g1, e.farmer("t3", w.g1), w.b, 1, 1) // 剩餘 2
	res := concurrently(
		func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 2) },
		func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g1, w.c, w.b, 1, 2) })
	if n := countOK(t, res, gameerr.PlotEmpty); n != 1 {
		t.Fatal(n)
	}
	if e.qty(w.a, "carrot")+e.qty(w.c, "carrot") != 2 || e.plot(w.b, 1).State != rules.PlotEmpty {
		t.Fatal("偷到的數量不對")
	}
}

func TestSameThiefConcurrentRepeatsOnlyOnce(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "strawberry")
	e.clock.Advance(600)
	res := runConcurrently(6, func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 1) })
	if n := countOK(t, res, gameerr.AlreadyStolen); n != 1 {
		t.Fatal(n)
	}
	if e.qty(w.a, "strawberry") != 1 || *e.plot(w.b, 1).RemainingQuantity != 4 || e.scalar("SELECT count(*) FROM steal_records") != 1 {
		t.Fatal("重複偷菜")
	}
}

func TestStealAndOwnerHarvestRace(t *testing.T) {
	e := setup(t)
	w := e.world()
	e.grow(w.b, 1, "strawberry")
	e.clock.Advance(600)
	concurrently(
		func() (ActionResult, error) { return e.so.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 2) },
		func() (ActionResult, error) { return e.s.HarvestCrop(e.ctx, w.b, 1) })
	// 不論誰先，總量都等於 5，不會多也不會少
	if total := e.qty(w.a, "strawberry") + e.qty(w.b, "strawberry"); total != 5 {
		t.Fatal(total)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---------- 排行榜 ----------

func TestLeaderboards(t *testing.T) {
	e := setup(t)
	w := e.world()
	a, b, c, d, g1 := w.a, w.b, w.c, w.d, w.g1
	e.grow(a, 1, "wheat")      // a: 490
	e.grow(b, 1, "strawberry") // b: 440
	e.clock.Advance(600)
	e.ok(e.s.HarvestCrop(e.ctx, a, 1))
	e.ok(e.s.SellCrop(e.ctx, a, "wheat", 2, "normal", "none")) // a: 520，販售 2 個 / 30
	e.steal(g1, c, b, 1, 2)                                    // c 偷 2 個草莓，尚未販售
	e.ok(e.s.HarvestCrop(e.ctx, b, 1))                         // b 收成 3 個，未販售
	rows := func(kind string, g, p *int64) []leaderboard.Row {
		return must[[]leaderboard.Row](t)(e.so.Leaderboard(e.ctx, kind, g, p, 10))
	}
	sales := rows("sales", nil, nil)
	if sales[0].PlayerName != "a" || sales[0].SoldQuantity != 2 || sales[0].SalesIncome != 30 {
		t.Fatalf("%+v", sales[0])
	}
	for _, r := range sales[1:] { // 偷菜、收成、購買都不算販售
		if r.SoldQuantity != 0 {
			t.Fatalf("%+v", r)
		}
	}
	e.ok(e.s.SellCrop(e.ctx, c, "strawberry", 2, "normal", "none")) // 偷來的成功出售後才計入：2 個 / 50
	sales = rows("sales", nil, nil)
	if sales[0].Rank != 1 || sales[0].PlayerName != "c" || sales[0].SalesIncome != 50 ||
		sales[1].Rank != 2 || sales[1].PlayerName != "a" || sales[1].SalesIncome != 30 { // 數量相同時比較收入
		t.Fatalf("%+v", sales[:2])
	}
	var rest []string
	for _, r := range sales[2:] { // 其餘 0 販售者依玩家 ID 排序
		rest = append(rest, r.PlayerName)
	}
	if !slices.Equal(rest, []string{"b", "d", "e"}) {
		t.Fatal(rest)
	}
	var money []string
	for _, r := range rows("money", nil, nil) {
		money = append(money, r.PlayerName+":"+strconv.Itoa(r.Gold))
	}
	if !slices.Equal(money, []string{"c:550", "a:520", "d:500", "e:500", "b:440"}) || rows("money", nil, nil)[0].Level != 1 {
		t.Fatal(money)
	}
	var group []string
	for _, r := range rows("money", &g1, &a) { // 群組排行榜只含目前群組成員
		group = append(group, r.PlayerName)
	}
	if !slices.Equal(group, []string{"c", "a", "b"}) {
		t.Fatal(group)
	}
	// a 雖然也是 g2 成員，但農場主要群組是 g1，因此不列入 g2 排行
	if g2 := rows("sales", &w.g2, &d); len(g2) != 1 || g2[0].PlayerName != "d" {
		t.Fatalf("%+v", g2)
	}
}
