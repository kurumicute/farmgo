package web

// 監控端點第三批：釣魚、排名獎勵、世界 Boss。
//   - tests/test_fishing_api.py：test_monitor_endpoints_reflect_game_actions、test_player_list_paginates。
//   - tests/test_ranking.py test_ranking_api 的監控部分（規則的建立、修改與結算改經服務；管理 API 在管理 API 那一批）。
//   - tests/test_world_boss.py：test_monitor_projection_is_read_only，以及 test_admin_website_manages_boss、
//     test_admin_website_schedules 的監控部分（活動與排程的建立改經服務）。
//   - Go 版自己的形狀檢查：表單依鍵順序建立的物件、列表沒有 rewards、明細的空清單輸出 []。

import (
	"encoding/json"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	"farmgm/internal/services"

	wb "farmgm/internal/game/worldboss"
)

// jsonDetail 錯誤回應的本文是否為 {"detail": want}。
func jsonDetail(body []byte, want string) bool {
	var v struct {
		Detail string `json:"detail"`
	}
	return json.Unmarshal(body, &v) == nil && v.Detail == want
}

// ---------- 釣魚 ----------

var bambooRod = fishing.Effects{"reel_power": 10, "control_pct": 0, "reaction_bonus_ms": 0}

func (e *webEnv) fishingSettings(change func(*fishing.Settings)) {
	e.t.Helper()
	s := fishing.DefaultSettings()
	change(&s)
	data, _ := json.Marshal(s)
	raw, err := featurecfg.Decode(data)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.UpdateFishingSettings(e.ctx, "test", raw); err != nil {
		e.t.Fatal(err)
	}
}

// catchOne Python catch_one：拋竿、在咬鉤時拉竿，之後依 smart_action 拉扯到結束。
func (e *webEnv) catchOne(pid int64) services.FishingView {
	e.t.Helper()
	v := must[services.FishingView](e.t)(e.svc.StartFishing(e.ctx, pid))
	e.clock.Set(v.BiteAt)
	v = must[services.FishingView](e.t)(e.svc.HookFish(e.ctx, pid, v.ID))
	for range 300 {
		if v.Phase != "fighting" {
			return v
		}
		v = must[services.FishingView](e.t)(e.svc.FishingAction(e.ctx, pid, v.ID,
			fishing.SmartAction(v.PolicyState(), fishing.Difficulty{}, bambooRod), v.Turn))
	}
	e.t.Fatal("拉扯沒有結束")
	return v
}

func TestMonitorFishingReflectsGameActions(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.svc.FishingRng = rand.New(rand.NewPCG(9, 0)).Float64
	home := e.group("主群組")
	e.fishingSettings(func(s *fishing.Settings) { s.BaseSuccessPct = 100 })
	pid := e.farmer("釣魚高手", home)
	e.setGold(pid, 5000)
	for _, g := range []struct {
		code string
		qty  int
	}{{"bamboo_rod", 1}, {"basic_line", 1}, {"worm_bait", 5}} {
		e.ok(e.svc.BuyFishingGear(e.ctx, pid, g.code, g.qty))
	}
	landed := e.catchOne(pid)
	if landed.Status != "landed" {
		t.Fatal(landed.Status)
	}
	must[services.SharedFish](t)(e.svc.InGroupContext(&home).ShareFish(e.ctx, pid, landed.Fish.ID))
	e.ok(e.svc.SellFish(e.ctx, pid, nil))

	var s obj
	e.getJSON("/api/monitor/fishing/summary", nil, &s)
	if num(s["sessions"]) != 1 || num(s["landed"]) != 1 || num(s["failed"]) != 0 || num(s["players"]) != 1 ||
		num(s["sell_gold"]) != landed.Fish.Price || num(s["shares"]) != 1 || num(s["base_success_pct"]) != 100 ||
		num(s["settings_version"]) != 1 || s["server_time"] == nil {
		t.Fatal(s)
	}
	var players struct {
		Total int   `json:"total"`
		Items []obj `json:"items"`
	}
	e.getJSON("/api/monitor/fishing/players", map[string]string{"q": "高手"}, &players)
	if players.Total != 1 {
		t.Fatal(players)
	}
	row := players.Items[0]
	equipped := row["equipped"].(obj)
	if num(row["landed"]) != 1 || num(row["sold"]) != 1 || num(row["shares"]) != 1 || len(equipped) != 3 ||
		equipped["rod"] != "竹製魚竿" || equipped["line"] != "普通魚線" || equipped["bait"] != "普通蚯蚓×4" {
		t.Fatal(row)
	}
	e.getJSON("/api/monitor/fishing/players", map[string]string{"q": "沒有這個人"}, &players)
	if players.Total != 0 || players.Items == nil {
		t.Fatal(players)
	}
	var detail struct {
		Summary      obj   `json:"summary"`
		Catches      []obj `json:"catches"`
		Shares       []obj `json:"shares"`
		Transactions []obj `json:"transactions"`
	}
	e.getJSON("/api/monitor/fishing/players/"+itoa(int(pid)), nil, &detail)
	types := map[string]bool{}
	for _, tx := range detail.Transactions {
		types[tx["event_type"].(string)] = true
	}
	if num(detail.Catches[0]["sold_price"]) != landed.Fish.Price || detail.Shares[0]["group_name"] != "主群組" ||
		len(types) != 2 || !types["fish_sell"] || !types["fishing_buy"] || num(detail.Summary["player_id"]) != int(pid) {
		t.Fatalf("%+v", detail)
	}
	if rec := e.get("/api/monitor/fishing/players/999999", nil); rec.Code != 404 || !jsonDetail(rec.Body.Bytes(), "找不到這位玩家的農場。") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var cat struct {
		Fish            []obj `json:"fish"`
		Gear            []obj `json:"gear"`
		SettingsVersion int   `json:"settings_version"`
		History         []obj `json:"history"`
	}
	e.getJSON("/api/monitor/fishing/catalog", nil, &cat)
	if len(cat.Fish) != len(fishing.InitialFish) || cat.SettingsVersion != 1 || len(cat.History) != 1 ||
		cat.History[0]["created_by"] != "test" {
		t.Fatalf("%+v", cat)
	}
	owners := -1
	for _, g := range cat.Gear {
		if g["code"] == "bamboo_rod" {
			owners = num(g["owners"])
		}
	}
	caught := 0
	for _, f := range cat.Fish {
		caught += num(f["caught"])
	}
	if owners != 1 || caught != 1 {
		t.Fatal(owners, caught)
	}
}

func TestMonitorFishingPlayerListPaginates(t *testing.T) {
	e := setupWeb(t, 0.2)
	home := e.group("主群組")
	for i := range 5 {
		pid := e.farmer("釣客"+itoa(i), home)
		e.setGold(pid, 500)
		e.ok(e.svc.BuyFishingGear(e.ctx, pid, "bamboo_rod", 1))
	}
	var page1, page3 struct {
		Total  int   `json:"total"`
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
		Items  []obj `json:"items"`
	}
	e.getJSON("/api/monitor/fishing/players", map[string]string{"limit": "2", "offset": "0"}, &page1)
	e.getJSON("/api/monitor/fishing/players", map[string]string{"limit": "2", "offset": "4"}, &page3)
	if page1.Total != 5 || len(page1.Items) != 2 || len(page3.Items) != 1 || page3.Limit != 2 || page3.Offset != 4 {
		t.Fatal(page1, page3)
	}
	for _, params := range []map[string]string{{"limit": "1000"}, {"offset": "-1"}, {"q": string(make([]byte, 41))}} {
		if rec := e.get("/api/monitor/fishing/players", params); rec.Code != 422 {
			t.Fatal(params, rec.Code)
		}
	}
}

// TestMonitorFishingCatalogShape Go 版補充：表單依鍵的順序建立欄位，ranges 的順序要與 Python dict 相同；沒有設定時版本為 0。
func TestMonitorFishingCatalogShape(t *testing.T) {
	e := setupWeb(t, 0.2)
	var cat struct {
		Settings        json.RawMessage `json:"settings"`
		SettingsVersion int             `json:"settings_version"`
		History         []obj           `json:"history"`
		Defaults        obj             `json:"defaults"`
		Labels          map[string]obj  `json:"labels"`
		Ranges          struct {
			FishPrice  []int           `json:"fish_price"`
			Settings   json.RawMessage `json:"settings"`
			Difficulty json.RawMessage `json:"difficulty"`
			Effects    json.RawMessage `json:"effects"`
		} `json:"ranges"`
	}
	rec := e.get("/api/monitor/fishing/catalog", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
		t.Fatal(err)
	}
	if cat.SettingsVersion != 0 || cat.History == nil || len(cat.History) != 0 || num(cat.Defaults["base_success_pct"]) != 90 ||
		cat.Labels["statuses"]["landed"] != "成功釣起" || cat.Labels["kinds"]["rod"] != "魚竿" {
		t.Fatalf("%+v", cat)
	}
	limitKeys := func(ls []fishing.Limit) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Key)
		}
		return out
	}
	if got := keysOf(t, cat.Ranges.Settings); !slices.Equal(got, limitKeys(fishing.SettingLimits)) {
		t.Fatal(got)
	}
	if got := keysOf(t, cat.Ranges.Difficulty); !slices.Equal(got, limitKeys(fishing.DifficultyLimits)) {
		t.Fatal(got)
	}
	if got := keysOf(t, cat.Ranges.Effects); !slices.Equal(got, fishing.GearKinds) {
		t.Fatal(got)
	}
	var effects map[string]json.RawMessage
	_ = json.Unmarshal(cat.Ranges.Effects, &effects)
	for _, kind := range fishing.GearKinds {
		if got := keysOf(t, effects[kind]); !slices.Equal(got, limitKeys(fishing.GearEffectLimits[kind])) {
			t.Fatal(kind, got)
		}
	}
	var rod obj
	_ = json.Unmarshal(effects["rod"], &rod)
	if r := rod["reel_power"].([]any); num(r[0]) != 8 || num(r[1]) != 30 {
		t.Fatal(rod)
	}
	if len(cat.Ranges.FishPrice) != 2 || cat.Ranges.FishPrice[1] != fishing.PriceMax {
		t.Fatal(cat.Ranges.FishPrice)
	}
}

// ---------- 排名獎勵 ----------

const (
	rankWeekEnd = 3*86400 + 16*3600 // 從測試時鐘起點到本週結束的秒數（台灣時間）
	rankGrace   = balance.RankingSettleGraceSeconds + 1
)

// rankWorld Python test_ranking.py 的 world：北區（甲、乙）各賣 5 個草莓，南區（丙）賣 6 個小麥。
func (e *webEnv) rankWorld() {
	e.t.Helper()
	north, south := e.group("北區"), e.group("南區")
	a, b, c := e.farmer("甲", north), e.farmer("乙", north), e.farmer("丙", south)
	for _, pid := range []int64{a, b} {
		e.grow(pid, 1, "strawberry")
	}
	for plot := 1; plot <= 3; plot++ {
		e.grow(c, plot, "wheat")
	}
	e.clock.Advance(600)
	for _, pid := range []int64{a, b} {
		e.ok(e.svc.HarvestCrop(e.ctx, pid, 1))
		e.ok(e.svc.SellCrop(e.ctx, pid, "strawberry", 5, "normal", "none"))
	}
	for plot := 1; plot <= 3; plot++ {
		e.ok(e.svc.HarvestCrop(e.ctx, c, plot))
	}
	e.ok(e.svc.SellCrop(e.ctx, c, "wheat", 6, "normal", "none"))
}

func TestMonitorRankingRewards(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.rankWorld()
	var board struct {
		Items []obj `json:"items"`
	}
	e.getJSON("/api/monitor/leaderboard", map[string]string{"scope": "groups", "kind": "sales", "period": "week"}, &board)
	if len(board.Items) != 2 || board.Items[0]["group_name"] != "北區" || board.Items[1]["group_name"] != "南區" {
		t.Fatal(board.Items)
	}
	if e.get("/api/monitor/leaderboard", map[string]string{"period": "year"}).Code != 422 {
		t.Fatal("期間驗證")
	}
	reward := func(rt string, n int) map[string]any { return map[string]any{"reward_type": rt, "amount": n} }
	body := map[string]any{"name": "每週群組金幣王", "scope": "group", "metric": "sales", "period": "weekly",
		"rewards": []any{reward("gold", 2000), reward("patrol_ticket", 1)}}
	rid := must[services.RuleView](t)(e.svc.CreateRankingRule(e.ctx, "admin", body)).ID
	e.clock.Advance(rankWeekEnd + rankGrace)
	settled := must[[]int64](t)(e.svc.SettleDueRankings(e.ctx, "system"))
	if len(settled) != 1 {
		t.Fatal(settled)
	}
	var data struct {
		Rules       []obj `json:"rules"`
		Settlements []obj `json:"settlements"`
	}
	rec := e.get("/api/monitor/ranking-rewards", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Rules[0]["rewards_text"] != "第 1 名 2000 金幣；第 2 名 1 個巡查券" || num(data.Rules[0]["id"]) != int(rid) ||
		num(data.Settlements[0]["award_count"]) != 3 { // 北區 2 人 + 南區 1 人
		t.Fatal(rec.Body.String())
	}
	if _, ok := data.Settlements[0]["awards"]; ok {
		t.Fatal("列表不含 awards")
	}
	if ranking, ok := data.Settlements[0]["ranking"].([]any); !ok || len(ranking) != 2 {
		t.Fatal(data.Settlements[0]["ranking"])
	}
	var detail struct {
		AwardCount int   `json:"award_count"`
		Awards     []obj `json:"awards"`
	}
	e.getJSON("/api/monitor/ranking-settlements/"+itoa(int(settled[0])), nil, &detail)
	var ranks []int
	for _, a := range detail.Awards {
		ranks = append(ranks, num(a["rank"]))
	}
	if !slices.Equal(ranks, []int{1, 1, 2}) || detail.AwardCount != 3 || detail.Awards[0]["reward_text"] != "2000 金幣" {
		t.Fatalf("%+v", detail)
	}
	if rec := e.get("/api/monitor/ranking-settlements/999", nil); rec.Code != 404 || !jsonDetail(rec.Body.Bytes(), "找不到這筆結算。") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if e.get("/api/monitor/ranking-rewards", map[string]string{"limit": "0"}).Code != 422 {
		t.Fatal("limit 驗證")
	}
}

// ---------- 世界 Boss ----------

func (e *webEnv) makeBoss(groupID int64, hp int, hours int) int64 {
	e.t.Helper()
	now := e.clock.Now()
	ends := now.Add(time.Duration(hours) * time.Hour)
	in := services.BossInput{GroupID: &groupID, Name: "深淵魔龍", Level: 50, MaxHP: hp, StartsAt: &now, EndsAt: &ends}
	return must[services.BossView](e.t)(e.svc.CreateWorldBoss(e.ctx, "admin", in)).EventID
}

func TestMonitorWorldBossListAndDetail(t *testing.T) {
	e := setupPlayer(t, 0.2)
	eid := e.makeBoss(e.home, 5000, 24)
	e.give(e.pid, "carrot", 3)
	must[services.BossDonateResult](t)(e.svc.BossDonate(e.ctx, &e.home, e.pid, eid, "crop", "carrot", 3, "req-000000000001", "normal", "none"))

	rec := e.get("/api/monitor/world-boss", nil)
	var listing struct {
		Events    []obj `json:"events"`
		Schedules []obj `json:"schedules"`
		Defaults  struct {
			Donatable   []obj           `json:"donatable"`
			RewardItems []obj           `json:"reward_items"`
			Schedule    json.RawMessage `json:"schedule"`
			Settings    json.RawMessage `json:"settings"`
			Supplies    json.RawMessage `json:"supplies"`
			Categories  json.RawMessage `json:"categories"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	ev := listing.Events[0]
	if num(ev["event_id"]) != int(eid) || num(ev["participants"]) != 1 || ev["rewards_text"] != nil || listing.Schedules == nil {
		t.Fatal(ev)
	}
	if _, ok := ev["rewards"]; ok {
		t.Fatal("列表不含 rewards")
	}
	if s := ev["settings"].(obj); num(s["gold_per_point"]) != wb.DefaultSettings().GoldPerPoint {
		t.Fatal(s)
	}
	donatable := map[string]string{}
	for _, d := range listing.Defaults.Donatable {
		donatable[d["code"].(string)] = d["category"].(string)
	}
	if donatable["carrot"] != "crop" || donatable["crystal"] != "special" || donatable["fake_coin"] != "" ||
		donatable[listing.Defaults.Donatable[len(listing.Defaults.Donatable)-1]["code"].(string)] != "fish" {
		t.Fatal(donatable)
	}
	// 可捐獻物品依類別順序排列（魚在最後）；獎勵物品沒有 category 鍵
	last := -1
	for _, d := range listing.Defaults.Donatable[:len(listing.Defaults.Donatable)-len(fishing.InitialFish)] {
		i := slices.Index(wb.Categories, d["category"].(string))
		if i < last {
			t.Fatal("類別順序", d)
		}
		last = i
	}
	if _, ok := listing.Defaults.RewardItems[0]["category"]; ok || len(listing.Defaults.RewardItems) == 0 {
		t.Fatal(listing.Defaults.RewardItems[0])
	}
	if got := keysOf(t, listing.Defaults.Schedule); !slices.Equal(got, []string{"name", "interval_days", "spawn_hour",
		"spawn_minute", "anchor_date", "duration_minutes", "max_hp", "settings", "rewards"}) {
		t.Fatal(got)
	}
	var sched struct {
		AnchorDate string          `json:"anchor_date"`
		Settings   json.RawMessage `json:"settings"`
	}
	_ = json.Unmarshal(listing.Defaults.Schedule, &sched)
	var settings struct {
		Items json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(sched.Settings, &settings)
	if sched.AnchorDate != "2026-11-04" || !slices.Equal(keysOf(t, settings.Items), wb.DefaultItemOrder) {
		t.Fatal(sched.AnchorDate, keysOf(t, settings.Items))
	}
	if got := keysOf(t, listing.Defaults.Settings); got[0] != "base_damage_per_hour" || got[len(got)-1] != "items" {
		t.Fatal(got)
	}
	if !slices.Equal(keysOf(t, listing.Defaults.Supplies), wb.Supplies) || !slices.Equal(keysOf(t, listing.Defaults.Categories), wb.Categories) {
		t.Fatal("補給與類別的順序")
	}

	var detail struct {
		TotalPoints   int   `json:"total_points"`
		Ranking       []obj `json:"ranking"`
		Contributions []obj `json:"contributions"`
		ClaimRows     []obj `json:"claim_rows"`
		Rewards       obj   `json:"rewards"`
		RewardsText   obj   `json:"rewards_text"`
	}
	rec = e.get("/api/monitor/world-boss/"+itoa(int(eid)), nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Ranking[0]["player_name"] != "阿明" || num(detail.Contributions[0]["quantity"]) != 3 || detail.TotalPoints != 9 ||
		detail.ClaimRows == nil || len(detail.ClaimRows) != 0 || detail.Rewards == nil || detail.RewardsText["victory"] == nil {
		t.Fatal(rec.Body.String())
	}
	if rec := e.get("/api/monitor/world-boss/999999", nil); rec.Code != 404 || !jsonDetail(rec.Body.Bytes(), "找不到這場世界 Boss 活動。") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if e.get("/api/monitor/world-boss", map[string]string{"limit": "201"}).Code != 422 {
		t.Fatal("limit 驗證")
	}
}

func TestMonitorWorldBossProjectionIsReadOnly(t *testing.T) {
	e := setupPlayer(t, 0.2)
	eid := e.makeBoss(e.home, 100_000, 72)
	e.clock.Advance(3600)
	var detail obj
	e.getJSON("/api/monitor/world-boss/"+itoa(int(eid)), nil, &detail)
	if num(detail["hp"]) != 100_000-450 { // 監控即時推算
		t.Fatal(detail["hp"])
	}
	if hp := e.scalar("SELECT hp FROM world_boss_events WHERE id = $1", eid); hp != 100_000 { // 但沒有寫回資料庫
		t.Fatal(hp)
	}
}

func TestMonitorWorldBossSchedules(t *testing.T) {
	e := setupWeb(t, 0.2)
	body := map[string]any{"name": "深淵魔龍", "group_id": nil, "interval_days": 2, "spawn_hour": 21, "spawn_minute": 0,
		"anchor_date": "2026-01-01", "duration_minutes": 180, "max_hp": 15000, "enabled": true}
	sid := must[services.BossScheduleView](t)(e.svc.CreateWorldBossSchedule(e.ctx, "admin", body)).ScheduleID
	rec := e.get("/api/monitor/world-boss", nil)
	var listing struct {
		Events    []obj             `json:"events"`
		Schedules []json.RawMessage `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	var sch struct {
		Name          string `json:"name"`
		AnchorDate    string `json:"anchor_date"`
		MaxHP         int    `json:"max_hp"`
		NextEvent     obj    `json:"next_event"`
		UpcomingSlots []any  `json:"upcoming_slots"`
		Rewards       obj    `json:"rewards"`
		RewardsText   obj    `json:"rewards_text"`
	}
	_ = json.Unmarshal(listing.Schedules[0], &sch)
	if sch.Name != "深淵魔龍" || sch.NextEvent == nil || len(sch.UpcomingSlots) != wb.SchedulePreview ||
		sch.Rewards["title"] != "降龍勇士" || // 沒有指定時套用深淵魔龍的預設獎勵
		sch.AnchorDate != "2026-01-01" || sch.MaxHP != 15000 || sch.RewardsText["title"] != "降龍勇士" {
		t.Fatal(string(listing.Schedules[0]))
	}
	// 時間欄位攤平在排程中，位置與 Python **schedule_values(s) 相同
	keys := keysOf(t, listing.Schedules[0])
	if i := slices.Index(keys, "scope_label"); i < 0 || !slices.Equal(keys[i+1:i+8], []string{"anchor_date", "interval_days",
		"spawn_hour", "spawn_minute", "duration_minutes", "max_hp", "enabled"}) {
		t.Fatal(keys)
	}
	if listing.Events[0]["name"] != "深淵魔龍" || num(listing.Events[0]["schedule_id"]) != int(sid) {
		t.Fatal(listing.Events[0])
	}
	must[int64](t)(e.svc.DeleteWorldBossSchedule(e.ctx, "admin", sid))
	e.getJSON("/api/monitor/world-boss", nil, &listing)
	if listing.Schedules == nil || len(listing.Schedules) != 0 {
		t.Fatal(listing.Schedules)
	}
}

// TestMonitorThirdBatchIsGetOnly 這批端點只接受 GET。
func TestMonitorThirdBatchIsGetOnly(t *testing.T) {
	e := setupWeb(t, 0.2)
	for _, target := range []string{"/api/monitor/fishing/summary", "/api/monitor/fishing/catalog", "/api/monitor/ranking-rewards",
		"/api/monitor/world-boss", "/api/monitor/world-boss/1"} {
		if res := e.send("POST", target, "{}", nil); res.StatusCode != 405 {
			t.Fatal(target, res.StatusCode)
		}
	}
	for _, target := range []string{"/api/monitor/fishing/players/0", "/api/monitor/ranking-settlements/abc", "/api/monitor/world-boss/0"} {
		if rec := e.get(target, nil); rec.Code != 422 {
			t.Fatal(target, rec.Code)
		}
	}
}
