package web

// 管理 API 第三批（排名獎勵規則、世界 Boss 活動與排程）的網站測試：
//   - tests/test_ranking.py：test_ranking_api（完整，包含管理 API）。
//   - tests/test_world_boss.py：test_admin_website_manages_boss、test_admin_website_schedules（完整）。
//   - Go 版自己的：巢狀本文（設定、獎勵）的型別驗證、date 欄位的格式。

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	wb "farmgm/internal/game/worldboss"
)

var simpleBossRewards = map[string]any{
	"min_points": 0, "victory": map[string]any{"gold": 100, "items": map[string]any{}},
	"participation": map[string]any{"gold": 10, "items": map[string]any{}}, "bonus_points": 10,
	"bonus": map[string]any{"gold": 5, "items": map[string]any{"wood": 1}},
	"rank":  []any{map[string]any{"gold": 50, "items": map[string]any{}}}, "title": "屠龍英雄", "title_top": 1,
}

// bossSettingsJSON 預設設定轉成 JSON 物件（Python {**wb.DEFAULT_SETTINGS, ...}）。
func bossSettingsJSON(t *testing.T, change func(*wb.Settings)) map[string]any {
	t.Helper()
	s := wb.DefaultSettings()
	if change != nil {
		change(&s)
	}
	data, _ := json.Marshal(s)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rankRuleBody(kv ...any) map[string]any {
	base := map[string]any{"name": "每週群組金幣王", "scope": "group", "metric": "sales", "period": "weekly",
		"rewards": []any{map[string]any{"reward_type": "gold", "amount": 2000}}}
	return with(base, kv...)
}

// ---------- tests/test_ranking.py test_ranking_api ----------

func TestAdminRankingAPI(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.rankWorld()
	e.login()
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
	body := rankRuleBody("rewards", []any{map[string]any{"reward_type": "gold", "amount": 2000},
		map[string]any{"reward_type": "patrol_ticket", "amount": 1}})
	code, v := e.call("POST", "/api/admin/ranking-rules", body)
	if code != 201 {
		t.Fatal(code, v)
	}
	rid := itoa(num(v["id"]))
	if e.status("POST", "/api/admin/ranking-rules", with(body, "rewards", []any{})) != 422 {
		t.Fatal("至少一名獎勵")
	}
	bad := with(body, "rewards", []any{map[string]any{"reward_type": "gold", "amount": 0}})
	if e.status("POST", "/api/admin/ranking-rules", bad) != 400 {
		t.Fatal("獎勵數量範圍")
	}
	if _, v := e.call("PUT", "/api/admin/ranking-rules/"+rid, with(body, "period", "monthly")); v["period"] != "monthly" {
		t.Fatal(v)
	}
	e.call("PUT", "/api/admin/ranking-rules/"+rid, body)
	e.clock.Advance(rankWeekEnd + rankGrace)
	_, v = e.call("POST", "/api/admin/ranking-rules/settle-due", nil)
	settled := v["settled"].([]any)
	// 改成每月又改回每週：從修改時間起算，本週在修改之後才結束，所以仍會結算
	if len(settled) != 1 {
		t.Fatal(v)
	}
	var data struct {
		Rules       []obj `json:"rules"`
		Settlements []obj `json:"settlements"`
	}
	e.getJSON("/api/monitor/ranking-rewards", nil, &data)
	if data.Rules[0]["rewards_text"] != "第 1 名 2000 金幣；第 2 名 1 個巡查券" || num(data.Settlements[0]["award_count"]) != 3 {
		t.Fatalf("%+v", data)
	}
	var detail struct {
		Awards []obj `json:"awards"`
	}
	e.getJSON("/api/monitor/ranking-settlements/"+itoa(num(settled[0])), nil, &detail)
	var ranks []int
	for _, a := range detail.Awards {
		ranks = append(ranks, num(a["rank"]))
	}
	if !slices.Equal(ranks, []int{1, 1, 2}) {
		t.Fatal(ranks)
	}
	if e.get("/api/monitor/ranking-settlements/999", nil).Code != 404 {
		t.Fatal("找不到結算")
	}
	if _, v := e.call("PUT", "/api/admin/ranking-rules/"+rid+"/enabled", map[string]any{"enabled": false}); v["enabled"] != false {
		t.Fatal(v)
	}
	if code, v := e.call("DELETE", "/api/admin/ranking-rules/"+rid, nil); code != 200 || itoa(num(v["deleted"])) != rid {
		t.Fatal(code, v)
	}
}

// ---------- tests/test_world_boss.py ----------

func TestAdminWebsiteManagesBoss(t *testing.T) {
	e := setupPlayer(t, 0.2)
	now := e.clock.Now()
	body := map[string]any{"group_id": e.home, "name": "深淵魔龍", "level": 50, "max_hp": 5000,
		"starts_at": now.Format(time.RFC3339Nano), "ends_at": now.Add(24 * time.Hour).Format(time.RFC3339Nano)}
	if e.status("POST", "/api/admin/world-boss", body) != 401 { // 未登入
		t.Fatal("未登入")
	}
	e.login()
	data, _ := json.Marshal(body)
	if res := e.send("POST", "/api/admin/world-boss", string(data), nil); res.StatusCode != 403 { // 缺少 CSRF 標頭
		t.Fatal(res.StatusCode)
	}
	code, v := e.call("POST", "/api/admin/world-boss", body)
	if code != 201 {
		t.Fatal(code, v)
	}
	eid := itoa(num(v["event_id"]))
	if e.status("POST", "/api/admin/world-boss", body) != 400 { // 同群組只能一場
		t.Fatal("同群組只能一場")
	}
	e.give(e.pid, "carrot", 3)
	if _, err := e.svc.BossDonate(e.ctx, &e.home, e.pid, int64(num(v["event_id"])), "crop", "carrot", 3, "req-000000000001",
		"normal", "none"); err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Events   []obj `json:"events"`
		Defaults struct {
			Donatable []obj `json:"donatable"`
		} `json:"defaults"`
	}
	e.getJSON("/api/monitor/world-boss", nil, &listing)
	if itoa(num(listing.Events[0]["event_id"])) != eid || num(listing.Events[0]["participants"]) != 1 {
		t.Fatal(listing.Events[0])
	}
	donatable := map[string]string{}
	for _, d := range listing.Defaults.Donatable {
		donatable[d["code"].(string)] = d["category"].(string)
	}
	if donatable["carrot"] != "crop" || donatable["crystal"] != "special" || donatable["fake_coin"] != "" {
		t.Fatal(donatable)
	}
	var detail struct {
		TotalPoints   int   `json:"total_points"`
		Ranking       []obj `json:"ranking"`
		Contributions []obj `json:"contributions"`
		ClaimRows     []obj `json:"claim_rows"`
	}
	e.getJSON("/api/monitor/world-boss/"+eid, nil, &detail)
	if detail.Ranking[0]["player_name"] != "阿明" || num(detail.Contributions[0]["quantity"]) != 3 || detail.TotalPoints != 9 ||
		detail.ClaimRows == nil || len(detail.ClaimRows) != 0 {
		t.Fatalf("%+v", detail)
	}
	settings := bossSettingsJSON(t, func(s *wb.Settings) {
		s.SupplyTargets = map[string]int{"food": 10, "medical": 10, "combat": 10, "equipment": 10}
	})
	code, v = e.call("PUT", "/api/admin/world-boss/"+eid+"/settings", settings)
	if code != 200 || num(v["supplies"].([]any)[0].(obj)["target"]) != 10 {
		t.Fatal(code, v)
	}
	if e.status("PUT", "/api/admin/world-boss/"+eid+"/rewards", simpleBossRewards) != 200 {
		t.Fatal("修改獎勵")
	}
	bad := with(simpleBossRewards, "victory", map[string]any{"gold": 1, "items": map[string]any{"no_such_item": 1}})
	if e.status("PUT", "/api/admin/world-boss/"+eid+"/rewards", bad) != 400 {
		t.Fatal("不存在的物品")
	}
	for _, c := range []struct{ action, state string }{{"pause", "paused"}, {"resume", "active"}, {"end", "ended"}} {
		if _, v := e.call("POST", "/api/admin/world-boss/"+eid+"/"+c.action, nil); v["state"] != c.state {
			t.Fatal(c.action, v)
		}
	}
	if e.status("POST", "/api/admin/world-boss/"+eid+"/explode", nil) != 422 {
		t.Fatal("不支援的動作")
	}
	if e.get("/api/monitor/world-boss/999999", nil).Code != 404 {
		t.Fatal("找不到活動")
	}
	page := e.get("/", nil).Body.String()
	if !containsAll(page, `data-tab="worldboss"`, `data-tab-panel="worldboss"`) {
		t.Fatal("首頁缺少世界 Boss 分頁")
	}
}

func TestAdminWebsiteSchedules(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.login()
	body := map[string]any{"name": "深淵魔龍", "group_id": nil, "interval_days": 2, "spawn_hour": 21, "spawn_minute": 0,
		"anchor_date": "2026-01-01", "duration_minutes": 180, "max_hp": 15000, "enabled": true}
	code, v := e.call("POST", "/api/admin/world-boss-schedules", body)
	if code != 201 {
		t.Fatal(code, v)
	}
	sid := itoa(num(v["schedule_id"]))
	var listing struct {
		Events    []obj `json:"events"`
		Schedules []obj `json:"schedules"`
	}
	e.getJSON("/api/monitor/world-boss", nil, &listing)
	sch := listing.Schedules[0]
	if sch["name"] != "深淵魔龍" || sch["next_event"] == nil || len(sch["upcoming_slots"].([]any)) != wb.SchedulePreview ||
		sch["rewards"].(obj)["title"] != "降龍勇士" { // 沒有指定時套用深淵魔龍的預設獎勵
		t.Fatal(sch)
	}
	if listing.Events[0]["name"] != "深淵魔龍" || itoa(num(listing.Events[0]["schedule_id"])) != sid {
		t.Fatal(listing.Events[0])
	}
	code, v = e.call("PUT", "/api/admin/world-boss-schedules/"+sid, with(body, "max_hp", 18000, "rewards", simpleBossRewards))
	if code != 200 || num(v["max_hp"]) != 18000 || v["rewards"].(obj)["title"] != "屠龍英雄" {
		t.Fatal(code, v)
	}
	if _, v := e.call("PUT", "/api/admin/world-boss-schedules/"+sid+"/enabled", map[string]any{"enabled": false}); v["enabled"] != false {
		t.Fatal(v)
	}
	if e.status("PUT", "/api/admin/world-boss-schedules/"+sid, with(body, "spawn_hour", 25)) != 422 {
		t.Fatal("時間範圍")
	}
	if code, v := e.call("DELETE", "/api/admin/world-boss-schedules/"+sid, nil); code != 200 || itoa(num(v["deleted"])) != sid {
		t.Fatal(code, v)
	}
	e.getJSON("/api/monitor/world-boss", nil, &listing)
	if listing.Schedules == nil || len(listing.Schedules) != 0 {
		t.Fatal(listing.Schedules)
	}
	page := e.get("/", nil).Body.String()
	if !containsAll(page, `id="wbsch-rows"`, `id="wsc-name"`) || containsAll(page, "wbf-level") {
		t.Fatal("首頁的排程表單")
	}
}

// ---------- Go 版補充 ----------

// TestAdminBossNestedBodyValidation 巢狀 model 的型別、鍵格式與長度錯誤回 422（不寫管理紀錄）；
// date 欄位接受時間為 00:00 的日期時間（同 pydantic）。
func TestAdminBossNestedBodyValidation(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.login()
	eid := itoa(int(e.makeBoss(e.home, 5000, 24)))
	before := e.scalar("SELECT count(*) FROM admin_actions")
	settings := bossSettingsJSON(t, nil)
	rewardsWith := func(kv ...any) map[string]any { return with(simpleBossRewards, kv...) }
	effects := settings["effects"].(map[string]any)
	cases := []struct {
		target string
		body   any
	}{
		{"/settings", with(settings, "crop_supply", "magic")},
		{"/settings", with(settings, "effects", with(effects, "ratio_cap_pct", -1))},
		{"/settings", with(settings, "supply_targets", map[string]any{"food": 1})}, // 缺少其他補給
		{"/settings", with(settings, "items", map[string]any{"X1": map[string]any{"supply": "food", "points": 1}})},
		{"/settings", with(settings, "items", map[string]any{"herb": map[string]any{"supply": "food"}})},
		{"/settings", with(settings, "limits", nil)},
		{"/rewards", rewardsWith("victory", map[string]any{"items": map[string]any{"a": 1}})},
		{"/rewards", rewardsWith("victory", map[string]any{"items": map[string]any{"i1": 1, "i2": 1, "i3": 1, "i4": 1, "i5": 1, "i6": 1}})},
		{"/rewards", rewardsWith("rank", []any{map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{},
			map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}})},
		{"/rewards", rewardsWith("title", "長長長長長長長長長長長長長長長長長長長長長")},
		{"/rewards", rewardsWith("victory", nil)},
	}
	for _, c := range cases {
		if code, v := e.call("PUT", "/api/admin/world-boss/"+eid+c.target, c.body); code != 422 {
			t.Errorf("%s %v：%d %v", c.target, c.body, code, v)
		}
	}
	// 獎勵的 gold、items、rank、title 可以省略（預設 0、{}、[]、""）
	minimal := map[string]any{"min_points": 0, "victory": map[string]any{}, "participation": map[string]any{"gold": 1},
		"bonus_points": 0, "bonus": map[string]any{}}
	if code, v := e.call("PUT", "/api/admin/world-boss/"+eid+"/rewards", minimal); code != 200 {
		t.Fatal(code, v)
	}
	if n := e.scalar("SELECT count(*) FROM admin_actions"); n != before+1 {
		t.Fatal("驗證失敗不應寫入管理紀錄", n, before)
	}
	schedule := func(anchor any) map[string]any {
		return map[string]any{"name": "排程", "interval_days": 2, "spawn_hour": 21, "spawn_minute": 0,
			"anchor_date": anchor, "duration_minutes": 180, "max_hp": 15000}
	}
	for _, bad := range []any{"2026-01-03T08:00", "明天", 20260101} {
		if code, v := e.call("POST", "/api/admin/world-boss-schedules", schedule(bad)); code != 422 {
			t.Errorf("%v：%d %v", bad, code, v)
		}
	}
	if code, v := e.call("POST", "/api/admin/world-boss-schedules", schedule("2026-01-02T00:00:00")); code != 201 ||
		v["anchor_date"] != "2026-01-02" {
		t.Fatal(code, v)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
