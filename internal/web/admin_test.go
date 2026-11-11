package web

// 管理 API 第一、二批（目錄、活動、品質設定、新功能設定與玩家調整）的網站測試：
//   - tests/test_admin.py：test_admin_api_end_to_end、test_admin_transfer_group_api。
//   - tests/test_community.py：test_admin_api_and_monitor。
//   - tests/test_genetics.py：test_genetics_web_api。
//   - tests/test_fishing_api.py：test_admin_writes_require_login_and_header、test_admin_gear_and_settings。
//   - tests/test_progress_admin.py：test_api_requires_login_and_works。
//   - Go 版自己的：本文型別驗證、日期輸出格式、建立端點回 201。

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/pets"
	"farmgm/internal/services"
)

// login 以測試管理員登入（cookie 由 webEnv 保留）。
func (e *webEnv) login() {
	e.t.Helper()
	if res := e.send("POST", "/api/auth/login", loginJSON(testAdminUser, testAdminPassword), adminHeader); res.StatusCode != 200 {
		e.t.Fatal("登入失敗", res.StatusCode)
	}
}

// call 帶管理標頭送出 JSON 本文（body 為 nil 時不送本文），回傳狀態碼與解析後的本文。
func (e *webEnv) call(method, target string, body any) (int, obj) {
	e.t.Helper()
	code, raw := e.callRaw(method, target, body)
	var out obj
	_ = json.Unmarshal(raw, &out)
	return code, out
}

func (e *webEnv) callRaw(method, target string, body any) (int, []byte) {
	e.t.Helper()
	s := ""
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		s = string(data)
	}
	rec := e.request(method, target, strings.NewReader(s), adminHeader)
	return rec.Code, rec.Body.Bytes()
}

// status 只要狀態碼。
func (e *webEnv) status(method, target string, body any) int {
	e.t.Helper()
	code, _ := e.callRaw(method, target, body)
	return code
}

func with(base map[string]any, kv ...any) map[string]any {
	out := make(map[string]any, len(base)+len(kv)/2)
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

var newCrop = map[string]any{"code": "eggplant", "name": "茄子", "seed_price": 30, "grow_seconds": 120,
	"harvest_quantity": 2, "sell_price": 40, "variant_code": "violet_eggplant", "variant_name": "紫晶茄子"}

var newAnimal = map[string]any{"code": "duck", "name": "鴨", "price": 300, "feed_cost": 10, "cycle_seconds": 3600,
	"product_code": "duck_egg", "product_name": "鴨蛋", "product_quantity": 3, "product_price": 15}

// ---------- tests/test_admin.py ----------

func TestAdminAPIEndToEnd(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.login()
	farmer := e.pid
	if code, v := e.call("POST", "/api/admin/crops", newCrop); code != 201 || v["code"] != "eggplant" {
		t.Fatal(code, v)
	}
	if e.status("POST", "/api/admin/crops", newCrop) != 400 {
		t.Fatal("重複代碼")
	}
	if e.status("POST", "/api/admin/crops", with(newCrop, "code", "Bad Code")) != 422 {
		t.Fatal("代碼格式")
	}
	if _, v := e.call("PUT", "/api/admin/crops/eggplant/active", map[string]any{"active": false}); v["active"] != false {
		t.Fatal(v)
	}
	if code, v := e.call("DELETE", "/api/admin/crops/eggplant", nil); code != 200 || v["deleted"] != "eggplant" {
		t.Fatal(code, v)
	}
	if e.status("POST", "/api/admin/animal-types", newAnimal) != 201 {
		t.Fatal("新增動物")
	}

	event := map[string]any{"name": "日間特賣", "schedule_type": "daily", "start_time": "08:00", "end_time": "24:00",
		"sell_price_pct": 120, "crop_code": "wheat"}
	code, v := e.call("POST", "/api/admin/events", event)
	if code != 201 || v["active_now"] != true { // 測試時鐘為台灣時間 08:00
		t.Fatal(code, v)
	}
	evID := itoa(num(v["id"]))
	if e.status("POST", "/api/admin/events", with(event, "end_time", "25:00")) != 422 {
		t.Fatal("時刻格式")
	}
	if e.status("POST", "/api/admin/events", with(event, "end_time", "08:00")) != 400 {
		t.Fatal("結束時刻須晚於開始")
	}
	weekly := with(event, "schedule_type", "weekly", "weekdays", []int{5, 6})
	if _, v := e.call("PUT", "/api/admin/events/"+evID, weekly); num(v["weekdays"]) != 0b1100000 {
		t.Fatal(v)
	}
	if e.status("PUT", "/api/admin/events/"+evID+"/enabled", map[string]any{"enabled": false}) != 200 {
		t.Fatal("停用事件")
	}

	base := "/api/admin/players/" + itoa(int(farmer))
	if _, v := e.call("PUT", base+"/farm", map[string]any{"gold": 777, "level": 3}); num(v["gold"]) != 777 {
		t.Fatal(v)
	}
	if e.status("PUT", base+"/farm", map[string]any{"gold": -5}) != 422 {
		t.Fatal("金幣範圍")
	}
	if e.status("PUT", base+"/inventory", map[string]any{"item_code": "duck_egg", "quantity": 4}) != 200 {
		t.Fatal("背包")
	}
	if code, v := e.call("POST", base+"/plots", map[string]any{"count": 2}); code != 201 || num(v["total"]) != 6 {
		t.Fatal(code, v)
	}
	if e.status("DELETE", base+"/plots/6", nil) != 200 {
		t.Fatal("刪除土地")
	}
	plant := map[string]any{"crop_code": "tomato", "quality": "good", "ready_now": true}
	if e.status("PUT", base+"/plots/5/crop", plant) != 200 || e.status("PUT", base+"/plots/5/crop", plant) != 400 {
		t.Fatal("代種")
	}
	if e.status("POST", base+"/buildings", map[string]any{"building_code": "barn"}) != 201 {
		t.Fatal("新增建築")
	}
	if code, v := e.call("POST", base+"/animals", map[string]any{"animal_code": "duck", "quantity": 2}); code != 201 ||
		len(v["added"].([]any)) != 2 {
		t.Fatal(code, v)
	}
	if e.status("DELETE", base+"/animals/2", nil) != 200 {
		t.Fatal("刪除動物")
	}
	if e.status("DELETE", base+"/buildings/barn/1", nil) != 400 { // 還有 1 隻動物
		t.Fatal("有動物時不能拆畜舍")
	}
	if e.status("DELETE", "/api/admin/players/999/plots/1", nil) != 404 {
		t.Fatal("找不到玩家")
	}

	var farm struct {
		Plots          []obj `json:"plots"`
		Gold           int   `json:"gold"`
		Level          int   `json:"level"`
		AnimalCapacity int   `json:"animal_capacity"`
		Animals        []obj `json:"animals"`
		Inventory      []obj `json:"inventory"`
	}
	e.getJSON("/api/monitor/players/"+itoa(int(farmer))+"/admin-farm", nil, &farm)
	var plots []int
	for _, p := range farm.Plots {
		plots = append(plots, num(p["plot_number"]))
	}
	if !slices.Equal(plots, []int{1, 2, 3, 4, 5}) || farm.Plots[4]["state"] != "ready" || farm.Plots[4]["quality"] != "good" ||
		farm.Gold != 777 || farm.Level != 3 || farm.AnimalCapacity != 4 || len(farm.Animals) != 1 || farm.Animals[0]["name"] != "鴨" ||
		len(farm.Inventory) != 1 || farm.Inventory[0]["name"] != "鴨蛋" || num(farm.Inventory[0]["quantity"]) != 4 {
		t.Fatalf("%+v", farm)
	}
	var catalog struct {
		Animals []obj `json:"animals"`
		Events  []obj `json:"events"`
		Items   []obj `json:"items"`
	}
	e.getJSON("/api/monitor/catalog", nil, &catalog)
	hasDuck := slices.ContainsFunc(catalog.Animals, func(a obj) bool { return a["code"] == "duck" })
	hasEgg := slices.ContainsFunc(catalog.Items, func(i obj) bool {
		return i["code"] == "duck_egg" && i["kind"] == "animal_product" && i["name"] == "鴨蛋"
	})
	if !hasDuck || !hasEgg || catalog.Events[0]["crop_code"] != "wheat" || catalog.Events[0]["enabled"] != false {
		t.Fatalf("%+v", catalog)
	}
	if code, v := e.call("DELETE", "/api/admin/events/"+evID, nil); code != 200 || itoa(num(v["deleted"])) != evID {
		t.Fatal(code, v)
	}
}

func TestAdminTransferGroupAPI(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.login()
	guild := "123456789012345678"
	a := must[services.GroupInfo](t)(e.svc.CreateGroup(e.ctx, "A 群", &guild)).ID
	b := e.group("B 群")
	pid := must[int64](t)(e.svc.EnsureExternalPlayer(e.ctx, "discord", "987654321098765432", "Discord 玩家"))
	must[services.FarmStatus](t)(e.svc.CreateFarm(e.ctx, pid, "", &a))
	base := "/api/admin/players/" + itoa(int(pid))
	if e.status("POST", base+"/transfer-group", map[string]any{"group_id": b, "reason": ""}) != 422 {
		t.Fatal("原因必填")
	}
	if res := e.send("POST", base+"/transfer-group", `{"group_id": 1, "reason": "x"}`, nil); res.StatusCode != 403 { // 缺少標頭
		t.Fatal(res.StatusCode)
	}
	if code, v := e.call("POST", base+"/transfer-group", map[string]any{"group_id": b, "reason": "客服"}); code != 200 ||
		num(v["home_group_id"]) != int(b) {
		t.Fatal(code, v)
	}
	if code, v := e.call("POST", base+"/transfer-group", map[string]any{"group_id": a, "reason": "再轉"}); code != 400 ||
		!strings.Contains(v["detail"].(string), "冷卻中") {
		t.Fatal(code, v)
	}
	body := map[string]any{"group_id": a, "reason": "再轉", "skip_cooldown": true}
	if _, v := e.call("POST", base+"/transfer-group", body); v["cooldown_bypassed"] != true {
		t.Fatal(v)
	}
	var farm obj
	e.getJSON("/api/monitor/players/"+itoa(int(pid))+"/admin-farm", nil, &farm)
	if num(farm["home_group_id"]) != int(a) || farm["next_group_transfer_at"] == nil ||
		farm["external_user_id"] != "987654321098765432" { // Discord ID 以字串輸出，不會失去精度
		t.Fatal(farm)
	}
	var groups []obj
	e.getJSON("/api/monitor/groups", nil, &groups)
	if !slices.ContainsFunc(groups, func(g obj) bool { return g["discord_guild_id"] == guild }) {
		t.Fatal(groups)
	}
}

// ---------- tests/test_community.py ----------

func TestAdminQuestAndCompetitionAPI(t *testing.T) {
	e := setupWeb(t, 0.2)
	groupA, groupB := e.group("A 群"), e.group("B 群")
	alice := e.farmer("alice", groupA)
	e.farmer("bob", groupB)
	e.setGold(alice, 10_000)
	now := e.clock.Now()
	day := now.Add(24 * time.Hour)
	body := map[string]any{"group_id": groupA, "name": "網頁任務", "quest_type": "harvest", "target": 2,
		"reward_mode": "fixed", "reward_type": "gold", "reward_amount": 50,
		"starts_at": now.Format(time.RFC3339Nano), "ends_at": day.Format(time.RFC3339Nano)}
	if e.status("POST", "/api/admin/quests", body) != 401 { // 未登入
		t.Fatal("未登入")
	}
	e.login()
	code, v := e.call("POST", "/api/admin/quests", body)
	if code != 201 {
		t.Fatal(code, v)
	}
	qid := itoa(num(v["quest_id"]))
	if e.status("POST", "/api/admin/quests", with(body, "quest_type", "magic")) != 422 {
		t.Fatal("任務類型")
	}
	if e.status("POST", "/api/admin/quests", with(body, "metric", "amount")) != 400 {
		t.Fatal("收成任務不能以金額計")
	}
	comp := map[string]any{"name": "網頁競賽", "comp_type": "sales", "rewards": []int{300, 200, 100},
		"starts_at": now.Format(time.RFC3339Nano), "ends_at": day.Format(time.RFC3339Nano)}
	code, v = e.call("POST", "/api/admin/competitions", comp)
	if code != 201 {
		t.Fatal(code, v)
	}
	cid := itoa(num(v["competition_id"]))

	ctxA := e.svc.InGroupContext(&groupA)
	e.ok(ctxA.BuySeeds(e.ctx, alice, "wheat", 1))
	e.ok(ctxA.PlantCrop(e.ctx, alice, 1, "wheat"))
	e.ok(ctxA.WaterCrop(e.ctx, alice, 1))
	e.clock.Advance(600)
	e.ok(ctxA.HarvestCrop(e.ctx, alice, 1))

	var detail struct {
		State        string `json:"state"`
		Contributors []obj  `json:"contributors"`
	}
	e.getJSON("/api/monitor/quests/"+qid, nil, &detail)
	if detail.State != "completed" || detail.Contributors[0]["player_name"] != "alice" {
		t.Fatalf("%+v", detail)
	}
	var cd obj
	e.getJSON("/api/monitor/competitions/"+cid, nil, &cd)
	if cd["final"] != false {
		t.Fatal(cd)
	}
	if e.status("POST", "/api/admin/competitions/"+cid+"/cancel", map[string]any{}) != 422 { // 須有原因
		t.Fatal("取消原因")
	}
	if e.status("POST", "/api/admin/competitions/"+cid+"/cancel", map[string]any{"reason": "測試取消"}) != 200 {
		t.Fatal("取消競賽")
	}
	var actions []obj
	e.getJSON("/api/monitor/admin-actions", nil, &actions)
	var names []string
	for _, a := range actions[:3] {
		names = append(names, a["action"].(string))
	}
	if !slices.Equal(names, []string{"cancel_competition", "create_competition", "create_quest"}) ||
		actions[0]["actor"] != "admin" || actions[0]["details"].(obj)["reason"] != "測試取消" {
		t.Fatal(actions[:3])
	}
	if code, raw := e.callRaw("POST", "/api/admin/competitions/settle-due", nil); code != 200 || string(raw) != `{"settled":[]}` {
		t.Fatal(code, string(raw))
	}
	if res := e.send("POST", "/api/monitor/quests", "{}", nil); res.StatusCode != 405 { // 監控端點唯讀
		t.Fatal(res.StatusCode)
	}
	var quests []obj
	e.getJSON("/api/monitor/quests", map[string]string{"group_id": itoa(int(groupA))}, &quests)
	if quests[0]["name"] != "網頁任務" {
		t.Fatal(quests)
	}
}

// ---------- tests/test_genetics.py ----------

func TestAdminGeneticsAPI(t *testing.T) {
	e := setupWeb(t, 0.2)
	settings := func(change func(map[string]any)) map[string]any {
		m := genetics.SettingsMap(balance.DefaultGenetics())
		if change != nil {
			change(m)
		}
		return m
	}
	must[int](t)(e.svc.UpdateGenetics(e.ctx, "admin", settings(nil))) // 使用預設機率（版本 2）
	home := e.group("主群組")
	farmer := e.farmer("育種家", home)
	e.setGold(farmer, 100_000)

	var stats obj
	e.getJSON("/api/monitor/genetics/stats", nil, &stats)
	for _, k := range []string{"planted", "harvested", "stolen", "sold", "holdings"} {
		if l, ok := stats[k].([]any); !ok || len(l) != 0 { // 沒有紀錄時不造假
			t.Fatal(stats)
		}
	}
	var data struct {
		Version  int `json:"version"`
		Settings struct {
			QualityBP map[string]int `json:"quality_bp"`
		} `json:"settings"`
	}
	e.getJSON("/api/monitor/genetics", nil, &data)
	if data.Version != 2 || data.Settings.QualityBP["normal"] != 7000 {
		t.Fatalf("%+v", data)
	}
	if e.status("PUT", "/api/admin/genetics", settings(nil)) != 401 {
		t.Fatal("未登入")
	}
	e.login()
	bad := settings(func(m map[string]any) {
		m["quality_bp"] = map[string]any{"normal": 7000, "good": 2000, "rare": 800, "legendary": 300}
	})
	if code, v := e.call("PUT", "/api/admin/genetics", bad); code != 400 || !strings.Contains(v["detail"].(string), "100%") {
		t.Fatal(code, v)
	}
	e.getJSON("/api/monitor/genetics", nil, &data)
	if data.Version != 2 {
		t.Fatal(data.Version)
	}
	if code, raw := e.callRaw("PUT", "/api/admin/genetics", settings(func(m map[string]any) { m["giant_multiplier"] = 3 })); code != 200 ||
		string(raw) != `{"version":3}` {
		t.Fatal(code, string(raw))
	}
	if e.status("PUT", "/api/admin/crops/wheat/grow-seconds", map[string]any{"grow_seconds": 5}) != 400 {
		t.Fatal("生長秒數下限")
	}
	if code, v := e.call("PUT", "/api/admin/crops/wheat/grow-seconds", map[string]any{"grow_seconds": 3600}); code != 200 ||
		num(v["grow_seconds"]) != 3600 {
		t.Fatal(code, v)
	}

	rolls := []float64{0.95, 0.93} // 稀有、巨型（×3）
	s := services.NewAdminService(e.d, services.Options{Clock: e.clock, GeneticsRng: func() float64 {
		r := rolls[0]
		rolls = rolls[1:]
		return r
	}})
	e.ok(s.BuySeeds(e.ctx, farmer, "wheat", 1))
	e.ok(s.PlantCrop(e.ctx, farmer, 1, "wheat"))
	e.ok(s.WaterCrop(e.ctx, farmer, 1))
	var planted struct {
		Planted []obj `json:"planted"`
	}
	e.getJSON("/api/monitor/genetics/stats", nil, &planted)
	if len(planted.Planted) != 1 || planted.Planted[0]["quality"] != "rare" || planted.Planted[0]["mutation"] != "giant" ||
		num(planted.Planted[0]["plots"]) != 1 {
		t.Fatal(planted)
	}
	e.clock.Advance(3600)
	e.ok(s.HarvestCrop(e.ctx, farmer, 1))
	e.ok(e.svc.SellCrop(e.ctx, farmer, "wheat", 6, "rare", "giant"))
	var after struct {
		Harvested []obj `json:"harvested"`
		Sold      []obj `json:"sold"`
		Holdings  []obj `json:"holdings"`
		Planted   []obj `json:"planted"`
	}
	e.getJSON("/api/monitor/genetics/stats", nil, &after)
	if len(after.Harvested) != 1 || num(after.Harvested[0]["count"]) != 1 || num(after.Harvested[0]["quantity"]) != 6 ||
		len(after.Sold) != 1 || num(after.Sold[0]["income"]) != 132 || len(after.Holdings) != 0 || len(after.Planted) != 0 {
		t.Fatalf("%+v", after)
	}
	if res := e.send("POST", "/api/monitor/genetics", "{}", nil); res.StatusCode != 405 {
		t.Fatal(res.StatusCode)
	}
	if n := e.scalar("SELECT count(*) FROM admin_actions"); n != 3 { // 夾具 1 次 + 測試 2 次
		t.Fatal(n)
	}
}

// ---------- tests/test_fishing_api.py ----------

var testFishBody = map[string]any{"code": "test_fish", "name": "測試魚", "emoji": "🐟", "rarity": "rare", "min_price": 21,
	"max_price": 30, "min_weight_g": 100, "max_weight_g": 500, "difficulty": 3}

func TestAdminFishingWritesRequireLoginAndHeader(t *testing.T) {
	e := setupWeb(t, 0.2)
	if e.status("POST", "/api/admin/fish-types", testFishBody) != 401 {
		t.Fatal("未登入")
	}
	e.login()
	data, _ := json.Marshal(testFishBody)
	if res := e.send("POST", "/api/admin/fish-types", string(data), nil); res.StatusCode != 403 { // 沒有 CSRF 標頭
		t.Fatal(res.StatusCode)
	}
	if code, v := e.call("POST", "/api/admin/fish-types", testFishBody); code != 201 || v["rarity_name"] != "稀有" {
		t.Fatal(code, v)
	}
	bad := with(testFishBody, "code", "bad_fish", "name", "壞魚", "min_price", 31) // 最低高於最高
	if e.status("POST", "/api/admin/fish-types", bad) != 400 {
		t.Fatal("售價區間")
	}
	free := with(testFishBody, "code", "rich_fish", "name", "富魚", "rarity", "common", "min_price", 5000, "max_price", 90000)
	if e.status("POST", "/api/admin/fish-types", free) != 201 { // 售價不受稀有度限制
		t.Fatal("售價不受稀有度限制")
	}
	upd := with(testFishBody, "name", "改名魚", "rarity", "epic", "min_price", 51, "max_price", 70)
	delete(upd, "code")
	if _, v := e.call("PUT", "/api/admin/fish-types/test_fish", upd); v["rarity"] != "epic" {
		t.Fatal(v)
	}
	if _, v := e.call("PUT", "/api/admin/fish-types/test_fish/active", map[string]any{"active": false}); v["active"] != false {
		t.Fatal(v)
	}
	var actions []obj
	e.getJSON("/api/monitor/admin-actions", nil, &actions)
	got := map[string]bool{}
	for _, a := range actions {
		got[a["action"].(string)] = true
	}
	for _, want := range []string{"create_fish_type", "update_fish_type", "disable_fish_type"} {
		if !got[want] {
			t.Fatal(want, got)
		}
	}
}

func TestAdminFishingGearAndSettings(t *testing.T) {
	e := setupWeb(t, 0.2)
	e.login()
	gear := map[string]any{"code": "gold_rod", "kind": "rod", "name": "黃金魚竿", "emoji": "🎣", "price": 9999,
		"required_level": 5, "stock": 3, "effects": map[string]any{"reel_power": 16, "control_pct": 20, "reaction_bonus_ms": 1000}}
	if e.status("POST", "/api/admin/fishing-gear", gear) != 201 {
		t.Fatal("新增釣具")
	}
	bad := with(gear, "code", "bad_rod", "name", "壞魚竿", "effects", map[string]any{"max_tension": 120})
	if e.status("POST", "/api/admin/fishing-gear", bad) != 400 {
		t.Fatal("效果欄位")
	}
	upd := with(gear, "price", 5000, "stock", nil)
	delete(upd, "code")
	delete(upd, "kind")
	if _, v := e.call("PUT", "/api/admin/fishing-gear/gold_rod", upd); num(v["price"]) != 5000 || v["stock"] != nil {
		t.Fatal(v)
	}
	if _, v := e.call("PUT", "/api/admin/fishing-gear/gold_rod/active", map[string]any{"active": false}); v["active"] != false {
		t.Fatal(v)
	}
	s := fishing.DefaultSettings()
	s.RarityBP = map[string]int{"common": 7000, "rare": 2000, "epic": 800, "legendary": 200}
	if code, raw := e.callRaw("PUT", "/api/admin/fishing-settings", s); code != 200 || string(raw) != `{"version":1}` {
		t.Fatal(code, string(raw))
	}
	s.RarityBP["legendary"] = 100
	if code, v := e.call("PUT", "/api/admin/fishing-settings", s); code != 400 || !strings.Contains(v["detail"].(string), "100%") {
		t.Fatal(code, v)
	}
	var cat struct {
		Settings struct {
			RarityBP map[string]int `json:"rarity_bp"`
		} `json:"settings"`
		SettingsVersion int `json:"settings_version"`
	}
	e.getJSON("/api/monitor/fishing/catalog", nil, &cat)
	if cat.Settings.RarityBP["legendary"] != 200 || cat.SettingsVersion != 1 {
		t.Fatalf("%+v", cat)
	}
}

// ---------- tests/test_progress_admin.py ----------

func TestAdminProgressAPIRequiresLoginAndWorks(t *testing.T) {
	e := setupWeb(t, 0.2)
	home := e.group("主群組")
	pid := e.farmer("網站玩家", home)
	if e.status("GET", "/api/admin/feature-settings", nil) != 401 {
		t.Fatal("未登入")
	}
	e.login()
	var view struct {
		Keys []struct {
			Key     string `json:"key"`
			Current obj    `json:"current"`
		} `json:"keys"`
	}
	_, raw := e.callRaw("GET", "/api/admin/feature-settings", nil)
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	var cfg obj
	for _, k := range view.Keys {
		if k.Key == "pets" {
			cfg = k.Current
		}
	}
	if code, v := e.call("PUT", "/api/admin/feature-settings/pets", with(cfg, "buy_price", 4_321)); code != 200 ||
		pets.BuyPrice() != 4_321 || v["key"] != "pets" || num(v["current"].(obj)["buy_price"]) != 4_321 {
		t.Fatal(code, v, pets.BuyPrice())
	}
	if code, v := e.call("PUT", "/api/admin/feature-settings/pets", with(cfg, "base_pct", 999)); code != 400 ||
		!strings.Contains(v["detail"].(string), "Lv0 加成") {
		t.Fatal(code, v)
	}
	if e.status("PUT", "/api/admin/feature-settings/nope", map[string]any{}) != 422 {
		t.Fatal("未知的設定")
	}
	if e.status("DELETE", "/api/admin/feature-settings/pets", nil) != 200 || pets.BuyPrice() != 5_000 {
		t.Fatal("還原預設", pets.BuyPrice())
	}
	base := "/api/admin/players/" + itoa(int(pid))
	for _, c := range []struct {
		method, target string
		body           any
		want           int
	}{
		{"PUT", base + "/pets/dog", map[string]any{"level": 1}, 200},
		{"PUT", base + "/greenhouse", map[string]any{"level": 1}, 200},
		{"POST", base + "/titles", map[string]any{"title": "網站稱號"}, 201},
		{"PUT", base + "/title", map[string]any{"title": "網站稱號"}, 200},
		{"PUT", base + "/checkin", map[string]any{"streak": 3}, 200},
		{"PUT", base + "/achievements/crops_100", map[string]any{"claimed": true}, 200},
		{"PUT", base + "/codex/carrot", map[string]any{"quantity": 3, "best_quality": 1}, 200},
	} {
		if code, v := e.call(c.method, c.target, c.body); code != c.want {
			t.Fatal(c.target, code, v)
		}
	}
	var progress struct {
		Titles struct {
			Current string `json:"current"`
		} `json:"titles"`
		Greenhouse struct {
			Level int `json:"level"`
		} `json:"greenhouse"`
		Checkin obj `json:"checkin"`
		Weekly  obj `json:"weekly"`
	}
	_, raw = e.callRaw("GET", base+"/progress", nil)
	if err := json.Unmarshal(raw, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Titles.Current != "網站稱號" || progress.Greenhouse.Level != 1 || num(progress.Checkin["streak"]) != 3 {
		t.Fatal(string(raw))
	}
	// Go 版補充：日期與 Python date 相同輸出 YYYY-MM-DD（前端以字串比較 last_day >= today）
	isDate := func(v any) bool {
		s, ok := v.(string)
		_, err := time.Parse(time.DateOnly, s)
		return ok && err == nil
	}
	if !isDate(progress.Checkin["today"]) || !isDate(progress.Checkin["last_day"]) || !isDate(progress.Weekly["week_start"]) ||
		progress.Checkin["last_day"].(string) > progress.Checkin["today"].(string) {
		t.Fatal(progress.Checkin, progress.Weekly)
	}
	if e.status("POST", base+"/titles/revoke", map[string]any{"title": "網站稱號"}) != 200 {
		t.Fatal("移除稱號")
	}
	if code, raw := e.callRaw("DELETE", base+"/weekly-purchases", nil); code != 200 || string(raw) != `{"cleared":0}` {
		t.Fatal(code, string(raw))
	}
}

// ---------- Go 版補充 ----------

// TestAdminBodyTypeValidation 型別、必填、長度與格式錯誤回 422，服務層不會被呼叫（不寫入管理紀錄）。
func TestAdminBodyTypeValidation(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.login()
	base := "/api/admin/players/" + itoa(int(e.pid))
	cases := []struct {
		method, target string
		body           any
	}{
		{"POST", "/api/admin/crops", with(newCrop, "seed_price", "30")},    // 字串不是整數
		{"POST", "/api/admin/crops", with(newCrop, "seed_price", 30.5)},    // 小數
		{"POST", "/api/admin/crops", with(newCrop, "name", "")},            // 太短
		{"POST", "/api/admin/crops", with(newCrop, "limits", []int{1})},    // 不是物件
		{"POST", "/api/admin/crops", map[string]any{"code": "eggplant"}},   // 缺欄位
		{"POST", "/api/admin/crops", []int{1, 2}},                          // 不是物件
		{"POST", "/api/admin/animal-types", with(newAnimal, "price", nil)}, // null
		{"POST", "/api/admin/fish-types", with(testFishBody, "rarity", "mythic")},
		{"POST", "/api/admin/fish-types", with(testFishBody, "required_baits", []any{"a", 1})},
		{"PUT", "/api/admin/fishing-settings", map[string]any{"rarity_bp": map[string]any{"common": "x"}}},
		{"POST", "/api/admin/events", map[string]any{"name": "e", "schedule_type": "daily", "weekdays": []int{7}}},
		{"POST", "/api/admin/events", map[string]any{"name": "e", "schedule_type": "once", "starts_at": "明天"}},
		{"POST", "/api/admin/competitions", map[string]any{"name": "c", "comp_type": "sales", "rewards": []int{1, 2, 3, 4},
			"starts_at": "2026-01-01T08:00", "ends_at": "2026-01-02T08:00"}},
		{"PUT", base + "/farm", map[string]any{"level": 0}},
		{"PUT", base + "/greenhouse", map[string]any{}}, // 「請填寫溫室等級。」
		{"PUT", base + "/pets/bird", map[string]any{"level": 1}},
		{"PUT", base + "/codex/X", map[string]any{"quantity": 1, "best_quality": 0}},
		{"PUT", base + "/checkin", map[string]any{"streak": true}},
		{"POST", base + "/plots", map[string]any{"count": balance.AdminMaxPlots + 1}},
		{"DELETE", base + "/plots/0", nil},
		{"DELETE", base + "/buildings/barn/101", nil},
		{"POST", base + "/titles", map[string]any{"title": strings.Repeat("長", 41)}},
	}
	for _, c := range cases {
		if code, v := e.call(c.method, c.target, c.body); code != 422 {
			t.Errorf("%s %s %v：%d %v", c.method, c.target, c.body, code, v)
		}
	}
	if _, v := e.call("PUT", base+"/greenhouse", map[string]any{}); v["detail"] != "請填寫溫室等級。" {
		t.Fatal(v)
	}
	if n := e.scalar("SELECT count(*) FROM admin_actions"); n != 0 {
		t.Fatal("驗證失敗不應寫入管理紀錄", n)
	}
}

// TestAdminDatetimeIsTaipeiWhenNaive 網頁 datetime-local 沒有時區，視為台灣時間（Python _aware）。
func TestAdminDatetimeIsTaipeiWhenNaive(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Time
	}{
		{"2026-01-01T08:00", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-01-01T08:00:30", time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)},
		{"2026-01-01 08:00:00.123456", time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)},
		{"2026-01-01T00:00:00Z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-01-01T09:00:00+01:00", time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)},
		{"2026-01-01T09:00+01:00", time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)},
		{"2026-01-01", time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC)}, // 只有日期：台灣時間 00:00
	} {
		got, ok := parseDatetime(c.in)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s → %v %v", c.in, got, ok)
		}
	}
	for _, bad := range []string{"", "明天", "2026-13-01T00:00", "2026-01-32"} {
		if _, ok := parseDatetime(bad); ok {
			t.Errorf("%q 應為格式錯誤", bad)
		}
	}
}
