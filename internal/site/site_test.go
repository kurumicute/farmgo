package site

// 對應 Python tests/test_site.py（介紹網站：頁面、邀請連結、只回傳彙總統計、快取與安全標頭）與
// tests/test_world_boss.py 的「13. 介紹網站」兩個測試。Python 的 TestClient 改用 httptest。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/news"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/ruins"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/specialshop"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/services"
	"farmgm/internal/services/svctest"
)

type siteEnv struct {
	t     *testing.T
	ctx   context.Context
	d     *db.DB
	clock *clock.Manual
	svc   *services.AdminService
}

func setup(t *testing.T) *siteEnv {
	t.Helper()
	d := svctest.Prepare(t, services.ClearNewsCache)
	c := clock.NewManual(time.Time{})
	return &siteEnv{t: t, ctx: context.Background(), d: d, clock: c, svc: services.NewAdminService(d, services.Options{Clock: c})}
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// app 新的網站（沒有快取）；opt.Service 沒指定時使用測試資料庫與手動時鐘。
func (e *siteEnv) app(opt Options) *App {
	if opt.Service == nil {
		opt.Service = services.NewSiteService(e.d, e.clock)
	}
	return New(opt)
}

func get(t *testing.T, a *App, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

type obj = map[string]any

func getJSON(t *testing.T, a *App, target string) obj {
	t.Helper()
	rec := get(t, a, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s：%d %s", target, rec.Code, rec.Body.String())
	}
	var out obj
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func list(v any) []any { return v.([]any) }
func m(v any) obj      { return v.(obj) }
func num(v any) float64 {
	return v.(float64)
}

func find(rows any, key, value string) obj {
	for _, r := range list(rows) {
		if m(r)[key] == value {
			return m(r)
		}
	}
	return nil
}

func (e *siteEnv) group(name string, guild *string) int64 {
	e.t.Helper()
	return must[services.GroupInfo](e.t)(e.svc.CreateGroup(e.ctx, name, guild)).ID
}

func (e *siteEnv) farmer(name string, groupID int64) int64 {
	e.t.Helper()
	p := must[services.PlayerInfo](e.t)(e.svc.CreatePlayer(e.ctx, name, nil, nil))
	must[services.FarmStatus](e.t)(e.svc.CreateFarm(e.ctx, p.ID, "", &groupID))
	return p.ID
}

func TestIndexAndSecurityHeaders(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	rec := get(t, a, "/")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "/farm") || !strings.Contains(body, "/play") {
		t.Fatal(rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") ||
		rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal(rec.Header())
	}
	if get(t, a, "/static/site.js").Code != 200 {
		t.Fatal("site.js")
	}
}

func TestInfoBuildsInviteURL(t *testing.T) {
	e := setup(t)
	info := getJSON(t, e.app(Options{ApplicationID: 1558428174814154822, SupportURL: "https://discord.gg/x"}), "/api/info")
	want := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=1558428174814154822&scope=bot+applications.commands&permissions=%d",
		InvitePermissions)
	if info["invite_url"] != want || info["support_url"] != "https://discord.gg/x" {
		t.Fatal(info)
	}
	if v := getJSON(t, e.app(Options{}), "/api/info")["invite_url"]; v != nil {
		t.Fatal(v)
	}
}

func TestStatsAreAggregateOnlyAndCached(t *testing.T) {
	e := setup(t)
	guild := "123456789012345678"
	g := e.group("伺服器", &guild)
	pid := e.farmer("秘密名字", g)
	if _, err := e.svc.BuySeeds(e.ctx, pid, "wheat", 1); err != nil {
		t.Fatal(err)
	}
	a := e.app(Options{})
	stats := getJSON(t, a, "/api/stats")
	if num(stats["players"]) != 1 || num(stats["farms"]) != 1 || num(stats["servers"]) != 1 {
		t.Fatal(stats)
	}
	if _, ok := stats["actions_today"].(float64); !ok || stats["last_activity"] == nil { // 「今天」依資料庫的實際日期
		t.Fatal(stats)
	}
	if num(stats["cached_seconds"]) != StatsCacheSeconds {
		t.Fatal(stats)
	}
	if body := get(t, a, "/api/stats").Body.String(); strings.Contains(body, "秘密名字") || strings.Contains(body, guild) {
		t.Fatal(body)
	}
	e.farmer("第二人", g)
	if num(getJSON(t, a, "/api/stats")["players"]) != 1 { // 30 秒快取內不重新查詢
		t.Fatal("快取")
	}
}

func TestStatsUnavailableWithoutDatabase(t *testing.T) {
	a := New(Options{})
	if rec := get(t, a, "/api/stats"); rec.Code != 503 || !strings.Contains(rec.Body.String(), statsUnavailable) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestMusicInviteAndNoCache(t *testing.T) {
	e := setup(t)
	music := DefaultMusic()
	music.ApplicationID = 2
	a := e.app(Options{ApplicationID: 1, Music: &music})
	info := getJSON(t, a, "/api/info")
	if !strings.Contains(info["music_invite_url"].(string), "client_id=2") || !strings.Contains(info["invite_url"].(string), "client_id=1") {
		t.Fatal(info)
	}
	if v := getJSON(t, e.app(Options{}), "/api/info")["music_invite_url"]; v != nil {
		t.Fatal(v)
	}
	page := get(t, a, "/")
	body := page.Body.String()
	if page.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(body, `id="guide"`) || !strings.Contains(body, "農場操作手冊") {
		t.Fatal(page.Header())
	}
	if !strings.Contains(body, `id="cave"`) || !strings.Contains(body, "山洞冒險") {
		t.Fatal("山洞冒險")
	}
}

func TestFeatureCardsHaveGuideSlots(t *testing.T) {
	// 首頁功能卡片的名稱與數字由 /api/guide 填入：頁面要有對應欄位，API 要有需要的資料。
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, slot := range []string{"c-animals", "c-defenses", "c-areas", "c-machines", "c-rare-seeds", "c-transfer", "g-steal-day", "g-area-example"} {
		if !strings.Contains(page, fmt.Sprintf(`id="%s"`, slot)) {
			t.Fatal(slot)
		}
	}
	guide := getJSON(t, a, "/api/guide")
	start := m(guide["start"])
	if num(start["steal_plots_per_day"]) != 1 || num(start["rename_cost"]) != balance.RenameFarmCost {
		t.Fatal(start)
	}
	if find(guide["items"], "kind", "tool") == nil {
		t.Fatal("tool")
	}
	rare := false
	for _, c := range list(guide["crops"]) {
		rare = rare || !m(c)["shop_buyable"].(bool) // 稀有種子（琥珀瓜、月光花）
	}
	if !rare {
		t.Fatal("沒有稀有種子")
	}
	if num(guide["cached_seconds"]) != GuideCacheSeconds {
		t.Fatal(guide["cached_seconds"])
	}
}

func TestGuideFollowsGameSettingsWithoutPlayerData(t *testing.T) {
	e := setup(t)
	guild := "123456789012345678"
	g := e.group("祕密伺服器", &guild)
	e.farmer("祕密農夫", g)
	must[services.CropView](t)(e.svc.UpdateCrop(e.ctx, "admin", "carrot", map[string]any{"name": "胡蘿蔔",
		"variant_name": "紫晶胡蘿蔔", "seed_price": 22, "grow_seconds": 14400, "harvest_quantity": 3, "sell_price": 25}))
	must[services.CropView](t)(e.svc.SetCropActive(e.ctx, "admin", "tomato", false))
	rec := get(t, e.app(Options{}), "/api/guide")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var guide obj
	if err := json.Unmarshal(rec.Body.Bytes(), &guide); err != nil {
		t.Fatal(err)
	}
	carrot := find(guide["crops"], "name", "胡蘿蔔")
	if num(carrot["grow_seconds"]) != 14400 || num(carrot["seed_price"]) != 22 { // 後台調整後網站跟著變
		t.Fatal(carrot)
	}
	if find(guide["crops"], "name", "番茄") != nil { // 停用的作物不列出
		t.Fatal("番茄")
	}
	var qnames []string
	sum := 0.0
	for _, q := range list(guide["qualities"]) {
		qnames = append(qnames, m(q)["name"].(string))
		sum += num(m(q)["chance_pct"])
	}
	if !slices.Equal(qnames, []string{"普通", "優良", "稀有", "傳說"}) || sum != 100 {
		t.Fatal(qnames, sum)
	}
	if len(list(guide["recipes"])) == 0 || len(list(guide["defenses"])) == 0 || len(list(guide["items"])) == 0 {
		t.Fatal("recipes／defenses／items")
	}
	x := m(guide["exploration"])
	var areas, machines []string
	for _, a := range list(x["areas"]) {
		areas = append(areas, m(a)["name"].(string))
	}
	for _, mc := range list(x["machines"]) {
		machines = append(machines, m(mc)["name"].(string))
	}
	slices.Sort(machines)
	if !slices.Equal(areas, []string{"青青草原", "古老森林", "神秘礦坑", "古代遺跡"}) ||
		!slices.Equal(machines, []string{"稀有作物培育器", "種子製造機", "自動加工機", "自動收割機"}) {
		t.Fatal(areas, machines)
	}
	for _, k := range []string{"normal", "advanced"} {
		total := 0.0
		for _, tp := range list(m(x["reward_types"])[k]) {
			total += num(m(tp)["chance_pct"])
		}
		if total != 100 {
			t.Fatal(k, total)
		}
	}
	seeds := list(m(list(x["areas"])[0])["seeds"])
	if len(seeds) != 1 || m(seeds[0])["name"] != "琥珀瓜種子" || num(m(seeds[0])["min_level"]) != 1 || num(x["max_level"]) != 13 {
		t.Fatal(seeds, x["max_level"])
	}
	if melon := find(guide["crops"], "name", "琥珀瓜"); melon["shop_buyable"] != false { // 稀有作物：商店不販售種子
		t.Fatal(melon)
	}
	body := rec.Body.String()
	for _, secret := range []string{"祕密農夫", "祕密伺服器", guild} {
		if strings.Contains(body, secret) {
			t.Fatal(secret)
		}
	}
}

func TestGuideUnavailableWithoutDatabase(t *testing.T) {
	if rec := get(t, New(Options{}), "/api/guide"); rec.Code != 503 || !strings.Contains(rec.Body.String(), guideUnavailable) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestInfoExposesMusicSettingsOnly(t *testing.T) {
	e := setup(t)
	music := Music{Enabled: true, PrefixCommands: true, Prefix: "?", PlaylistLimit: 30, IdleTimeout: 600, Volume: 0.25}
	info := getJSON(t, e.app(Options{Music: &music}), "/api/info")
	got, _ := json.Marshal(info["music"])
	if string(got) != `{"default_volume_pct":25,"idle_minutes":10,"playlist_limit":30,"prefix":"?","prefix_commands":true}` {
		t.Fatal(string(got))
	}
}

// fishSettings 預設釣魚設定改 base_success_pct（UpdateFishingSettings 的本文）。
func fishSettings(t *testing.T, baseSuccess int) map[string]any {
	s := fishing.DefaultSettings()
	s.BaseSuccessPct = baseSuccess
	return toBody(t, s)
}

// toBody 轉成管理 API 本文的形式（整數為 json.Number，與網站層 parseBody 的輸出相同）。
func toBody(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGuideHasFishingRulesAndFollowsAdminChanges(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, want := range []string{`id="fishing"`, "基礎釣魚成功率", "分享到群組"} {
		if !strings.Contains(page, want) {
			t.Fatal(want)
		}
	}
	f := m(getJSON(t, a, "/api/guide")["fishing"])
	var chances []float64
	for _, r := range list(f["rarities"]) {
		chances = append(chances, num(m(r)["chance_pct"]))
	}
	if !slices.Equal(chances, []float64{80, 15, 4, 1}) {
		t.Fatal(chances)
	}
	rows := make([]fishing.PriceRow, len(fishing.InitialFish))
	for i, x := range fishing.InitialFish {
		rows[i] = fishing.PriceRow{Rarity: x.Rarity, MinPrice: x.MinPrice, MaxPrice: x.MaxPrice}
	}
	for i, r := range fishing.Rarities { // 依啟用中魚種的實際售價
		lo, hi, _ := fishing.PriceSpan(rows, r)
		price := list(m(list(f["rarities"])[i])["price"])
		if num(price[0]) != float64(lo) || num(price[1]) != float64(hi) {
			t.Fatal(r, price, lo, hi)
		}
		pulls := list(m(list(f["rarities"])[i])["pulls"])
		if num(pulls[0]) != float64(fishing.Pulls[r][0]) || num(pulls[1]) != float64(fishing.Pulls[r][1]) {
			t.Fatal(r, pulls)
		}
	}
	if num(f["base_success_pct"]) != 90 || len(list(f["gear"])) != len(fishing.InitialGear) || len(list(f["fish"])) != len(fishing.InitialFish) {
		t.Fatal(f["base_success_pct"], len(list(f["gear"])), len(list(f["fish"])))
	}
	must[services.FishTypeView](t)(e.svc.SetFishTypeActive(e.ctx, "test", "golden_koi", false))
	must[int](t)(e.svc.UpdateFishingSettings(e.ctx, "test", fishSettings(t, 85)))
	f2 := m(getJSON(t, e.app(Options{}), "/api/guide")["fishing"]) // 新的網站（沒有快取）
	if num(f2["base_success_pct"]) != 85 || find(f2["fish"], "name", "黃金錦鯉") != nil {
		t.Fatal(f2["base_success_pct"])
	}
}

func TestGuideRarityPriceFollowsFishPrices(t *testing.T) {
	// 魚的售價不受稀有度限制：手冊各稀有度的售價範圍依啟用中魚種的實際售價計算。
	e := setup(t)
	var x fishing.FishSpec
	for _, f := range fishing.InitialFish {
		if f.Rarity == "common" {
			x = f
			break
		}
	}
	must[services.FishTypeView](t)(e.svc.UpdateFishType(e.ctx, "test", x.Code, map[string]any{
		"name": x.Name, "emoji": x.Emoji, "rarity": x.Rarity, "min_price": 0, "max_price": 123456,
		"min_weight_g": x.MinWeightG, "max_weight_g": x.MaxWeightG, "difficulty": x.Difficulty, "description": x.Desc,
		"sort_order": 0}))
	common := m(list(m(getJSON(t, e.app(Options{}), "/api/guide")["fishing"])["rarities"])[0])
	price := list(common["price"])
	if common["code"] != "common" || num(price[0]) != 0 || num(price[1]) != 123456 {
		t.Fatal(common)
	}
}

func TestGuideListsEnabledRankingRulesOnly(t *testing.T) {
	e := setup(t)
	must[services.RuleView](t)(e.svc.CreateRankingRule(e.ctx, "admin", map[string]any{
		"name": "每週群組販售王", "scope": "group", "metric": "sales", "period": "weekly",
		"rewards": []any{map[string]any{"reward_type": "gold", "amount": 2000}, map[string]any{"reward_type": "fertilizer", "amount": 3}},
	}))
	off := must[services.RuleView](t)(e.svc.CreateRankingRule(e.ctx, "admin", map[string]any{
		"name": "停用的規則", "scope": "individual", "metric": "money", "period": "daily",
		"rewards": []any{map[string]any{"reward_type": "gold", "amount": 100}},
	}))
	must[services.RuleView](t)(e.svc.SetRankingRuleEnabled(e.ctx, "admin", off.ID, false))
	r := m(getJSON(t, e.app(Options{}), "/api/guide")["rankings"])
	rules := list(r["rules"])
	if num(r["settle_grace_seconds"]) <= 0 || len(rules) != 1 || m(rules[0])["name"] != "每週群組販售王" { // 停用的不列出
		t.Fatal(r)
	}
	rule := m(rules[0])
	rewards := list(rule["rewards"])
	if rule["scope"] != "group" || rule["metric"] != "農產品販售" || rule["period"] != "每週" || rewards[0] != "2000 金幣" || len(rewards) != 2 {
		t.Fatal(rule)
	}
	page := get(t, e.app(Options{}), "/").Body.String()
	if !strings.Contains(page, `id="g-rankings"`) || !strings.Contains(page, "排名獎勵") {
		t.Fatal("g-rankings")
	}
}

func TestGuideHasCheckinAchievementsAndCodex(t *testing.T) {
	// 手冊的簽到、成就與圖鑑表格由 /api/guide 填入，數量與規則模組一致，頁面預設表格也完整。
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, slot := range []string{`id="progress"`, `id="g-checkin"`, `id="g-achievements"`, `id="g-codex-cats"`, `id="c-achievements"`} {
		if !strings.Contains(page, slot) {
			t.Fatal(slot)
		}
	}
	// 成就表格在欄位打開時才由 /api/guide/progress 填入（2026-11-14 分段讀取），頁面只放讀取中的提示
	if !strings.Contains(page, `<tbody id="g-achievements">`) || !strings.Contains(page, "打開欄位時讀取最新資料") {
		t.Fatal("成就表格")
	}
	p := m(getJSON(t, a, "/api/guide")["progress"])
	days := list(p["checkin"])
	if len(days) != checkin.CycleDays() {
		t.Fatal(len(days))
	}
	for i, d := range days {
		if num(m(d)["day"]) != float64(i+1) {
			t.Fatal(d)
		}
	}
	if len(list(p["achievements"])) != len(achievements.Active()) || find(p["achievements"], "title", "傳說農夫") == nil {
		t.Fatal(len(list(p["achievements"])))
	}
	if !slices.Contains(list(p["codex"]), any("🐟 魚類")) || !slices.Contains(list(p["codex"]), any("🧪 特殊種子")) {
		t.Fatal(p["codex"])
	}
}

func TestGuideHasSpecialShop(t *testing.T) {
	// 特賣與升級：溫室價格、本週特賣與稱號商店由 /api/guide 提供，頁面有對應欄位。
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, slot := range []string{`id="special-shop"`, `id="g-weekly"`, `id="g-title-shop"`, `id="g-gh-prices"`, "上一場排行榜"} {
		if !strings.Contains(page, slot) {
			t.Fatal(slot)
		}
	}
	sp := m(getJSON(t, a, "/api/guide")["special_shop"])
	var prices []int
	for _, p := range list(m(sp["greenhouse"])["prices"]) {
		prices = append(prices, int(num(p)))
	}
	if !slices.Equal(prices, specialshop.GreenhousePrices()) {
		t.Fatal(prices)
	}
	var titles []string
	for _, x := range list(sp["titles"]) {
		titles = append(titles, m(x)["title"].(string))
	}
	var want []string
	for _, x := range specialshop.TitleShop() {
		want = append(want, x.Title)
	}
	if !slices.Equal(titles, want) {
		t.Fatal(titles)
	}
	weekly := m(sp["weekly"])
	offers := list(weekly["offers"])
	rare := 0
	for _, o := range offers {
		if m(o)["rare"].(bool) {
			rare++
		}
	}
	if len(offers) != specialshop.ConfigInt("normal_deals")+1 || rare != 1 {
		t.Fatal(offers)
	}
	// 週一（台灣日期）與下週一 00:00 台灣時間（Python date／datetime.isoformat）
	if weekly["week_start"] != "2025-12-29" || weekly["ends_at"] != "2026-01-05T00:00:00+08:00" {
		t.Fatal(weekly["week_start"], weekly["ends_at"])
	}
}

func TestGuideHasPets(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, want := range []string{`id="g-pets"`, `id="g-pet-ladder"`, "寵物（貓狗）", `id="g-cat-odds"`, `id="g-cat-head"`, `id="g-cat-example"`} { // 表格在欄位打開時填入（表頭依最高等級產生）
		if !strings.Contains(page, want) {
			t.Fatal(want)
		}
	}
	p := m(getJSON(t, a, "/api/guide")["pets"])
	var ups []int
	for _, x := range list(p["upgrade_prices"]) {
		ups = append(ups, int(num(x)))
	}
	if num(p["buy_price"]) != float64(pets.BuyPrice()) || !slices.Equal(ups, pets.UpgradePrices()) || len(list(p["pets"])) != len(pets.Pets) {
		t.Fatal(p)
	}
	wheat := find(p["cat_odds"], "name", "小麥")
	levels := list(wheat["levels"])
	maxLevel := pets.MaxLevel()
	if len(levels) != maxLevel+1 || levels[len(levels)-1] != pets.OddsText(int(num(wheat["quantity"])), pets.BonusPct(&maxLevel)) {
		t.Fatal(levels)
	}
}

func TestGuideHasRuinsNewsSeasonsAndFishLimits(t *testing.T) {
	// 古代遺跡與守護者、日報傳聞、季節限定、魚的指定魚餌與時段：/api/guide 提供，頁面有對應區塊。
	e := setup(t)
	applySeasonDefaults(t, e.d)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	for _, slot := range []string{`id="ruins"`, `id="news"`, `id="g-ruins-stages"`, `id="g-guardian"`, `id="g-news"`,
		`id="g-season-now"`, "古代遺跡與守護者", "農場日報與傳聞"} {
		if !strings.Contains(page, slot) {
			t.Fatal(slot)
		}
	}
	rec := get(t, a, "/api/guide")
	var g obj
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	r := m(g["ruins"])
	var stages []string
	for _, s := range list(r["stages"]) {
		stages = append(stages, m(s)["name"].(string))
	}
	var want []string
	for _, s := range ruins.Stages {
		want = append(want, s.Name)
	}
	if !slices.Equal(stages, want) || num(m(r["guardian"])["hp_base"]) != ruins.GuardianHPBase || num(r["unlock_level"]) != 4 {
		t.Fatal(r)
	}
	// 守護者傷害依 Python dict 的順序（前端以 Object.entries 顯示）
	if !strings.Contains(rec.Body.String(), `"damage":{"普通":3,"稀有":15,"極稀有":80}`) {
		t.Fatal("damage 順序")
	}
	gold := list(m(g["news"])["treasure_gold"])
	if num(gold[0]) != news.TreasureGoldLow || num(gold[1]) != news.TreasureGoldHigh || !strings.Contains("春夏秋冬", m(g["seasons"])["current"].(string)) {
		t.Fatal(g["news"], g["seasons"])
	}
	if !strings.Contains(rec.Body.String(), `"months":{"春":"3–5 月","夏":"6–8 月","秋":"9–11 月","冬":"12–2 月"}`) {
		t.Fatal("months 順序")
	}
	if find(g["crops"], "name", "小麥")["season_text"] != "" || find(g["crops"], "name", "玉米")["season_text"] != "🌸☀️春、夏季限定" {
		t.Fatal("season_text")
	}
	fish := m(g["fishing"])["fish"]
	if find(fish, "name", "鰻魚")["limit_text"] != "🕒 18:00–06:00" || len(list(find(fish, "name", "鯽魚")["baits"])) != 0 {
		t.Fatal("鰻魚／鯽魚")
	}
	frost := find(fish, "name", "霜龍魚")
	baits := list(frost["baits"])
	if len(baits) != 1 || baits[0] != "🐉 龍涎香餌" || !strings.Contains(frost["limit_text"].(string), "冬季限定") {
		t.Fatal(frost)
	}
	if find(m(g["exploration"])["areas"], "name", "古代遺跡")["needs_ruins"] != true ||
		find(m(g["exploration"])["areas"], "name", "青青草原")["needs_ruins"] != false {
		t.Fatal("needs_ruins")
	}
}

// applySeasonDefaults 套用與正式環境相同的預設限定（Python test_seasons.apply_defaults；svctest.Prepare 會清空限定）。
func applySeasonDefaults(t *testing.T, d *db.DB) {
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		for code, s := range seasons.CropSeasons {
			data, _ := json.Marshal(s)
			if _, err := tx.Exec(ctx, "UPDATE crop_types SET seasons = $2::json WHERE code = $1", code, string(data)); err != nil {
				return err
			}
		}
		for code, l := range seasons.FishLimits {
			var s, a, b any
			if len(l.Seasons) > 0 {
				data, _ := json.Marshal(l.Seasons)
				s = string(data)
			}
			if l.Hours != nil {
				a, b = l.Hours.Start, l.Hours.End
			}
			if _, err := tx.Exec(ctx, "UPDATE fish_types SET seasons = $2::json, hour_start = $3, hour_end = $4 WHERE code = $1",
				code, s, a, b); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- test_world_boss.py：13. 介紹網站 ----------

func TestSiteShowsWorldBossGuide(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	guide := m(getJSON(t, a, "/api/guide")["world_boss"])
	var codes []string
	for _, s := range list(guide["supplies"]) {
		codes = append(codes, m(s)["code"].(string))
	}
	if !slices.Equal(codes, wb.Supplies) || num(guide["gold_per_point"]) != float64(wb.DefaultSettings().GoldPerPoint) {
		t.Fatal(codes, guide["gold_per_point"])
	}
	if len(list(guide["categories"])) != 5 || len(list(m(guide["rewards"])["rank"])) == 0 {
		t.Fatal(guide["categories"], guide["rewards"])
	}
	if m(guide["source"])["kind"] != "default" || guide["current"] != nil {
		t.Fatal(guide["source"], guide["current"])
	}
	// 預設的指定物品依 Python DEFAULT_SETTINGS["items"] 的順序
	var items []string
	for _, it := range list(guide["items"]) {
		items = append(items, m(it)["name"].(string))
	}
	if len(items) == 0 {
		t.Fatal("items")
	}
	page := get(t, a, "/").Body.String()
	for _, want := range []string{`id="world-boss"`, "世界 Boss", `id="g-boss-supplies"`, `id="c-boss"`, `id="g-boss-now"`} {
		if !strings.Contains(page, want) {
			t.Fatal(want)
		}
	}
}

func TestSiteGuideFollowsLiveGlobalBoss(t *testing.T) {
	// 介紹網站的世界 Boss 數值跟著目前的全服活動（與 Discord 活動說明一致），並顯示本場 HP；伺服器活動不公開。
	e := setup(t)
	home := e.group("主群組", nil)
	now := e.clock.Now()
	ends := now.Add(72 * time.Hour)
	rewards := toBody(t, wb.DefaultRewards())
	rewards["title"] = "伺服器限定"
	must[services.BossView](t)(e.svc.CreateWorldBoss(e.ctx, "admin", services.BossInput{GroupID: &home, Name: "深淵魔龍",
		Level: 50, MaxHP: 999, StartsAt: &now, EndsAt: &ends, Rewards: rewards}))

	settings := toBody(t, wb.DefaultSettings())
	settings["supply_targets"] = map[string]any{"food": json.Number("111"), "medical": json.Number("222"),
		"combat": json.Number("333"), "equipment": json.Number("444")}
	dragon := toBody(t, wb.AbyssDragon().Rewards)
	threeHours := now.Add(3 * time.Hour)
	eid := must[services.BossView](t)(e.svc.CreateWorldBoss(e.ctx, "admin", services.BossInput{Name: "全服測試龍",
		Level: 50, MaxHP: 5000, StartsAt: &now, EndsAt: &threeHours, Rewards: dragon, Settings: settings})).EventID
	pid := e.farmer("阿明", home)
	if _, err := e.svc.SetInventory(e.ctx, "test", pid, "carrot", "normal", "none", 2); err != nil {
		t.Fatal(err)
	}
	must[services.BossDonateResult](t)(e.svc.BossDonate(e.ctx, nil, pid, eid, "crop", "carrot", 2, "req-000000000001", "normal", "none"))

	rec := get(t, e.app(Options{}), "/api/guide")
	var g obj
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	guide := m(g["world_boss"])
	if src := m(guide["source"]); src["kind"] != "event" || src["name"] != "全服測試龍" {
		t.Fatal(src)
	}
	var targets []float64
	for _, s := range list(guide["supplies"]) {
		targets = append(targets, num(m(s)["target"]))
	}
	if !slices.Equal(targets, []float64{111, 222, 333, 444}) {
		t.Fatal(targets)
	}
	rw := m(guide["rewards"])
	if !strings.HasPrefix(rw["victory"].(string), "1,200 金幣") || rw["title"] != "降龍勇士" || num(guide["duration_minutes"]) != 180 {
		t.Fatal(rw, guide["duration_minutes"])
	}
	cur := m(guide["current"])
	if cur["name"] != "全服測試龍" || num(cur["max_hp"]) != 5000 || num(cur["hp"]) >= 5000 || num(cur["participants"]) != 1 {
		t.Fatal(cur)
	}
	curJSON, _ := json.Marshal(cur)
	if strings.Contains(strings.ToLower(string(curJSON)), "player") {
		t.Fatal(string(curJSON))
	}
	worldBossJSON, _ := json.Marshal(guide)
	if strings.Contains(string(worldBossJSON), "伺服器限定") { // 伺服器自己的活動不出現在介紹網站
		t.Fatal("伺服器限定")
	}
}

func TestStaticETagAndNotFound(t *testing.T) {
	// Go 版：靜態檔帶 ETag（Python StaticFiles／FileResponse 相同），If-None-Match 相符時回 304；不會讀到嵌入目錄以外。
	a := New(Options{})
	rec := get(t, a, "/static/site.css")
	etag := rec.Header().Get("ETag")
	if rec.Code != 200 || etag == "" || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatal(rec.Code, rec.Header())
	}
	req := httptest.NewRequest(http.MethodGet, "/static/site.css", nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	a.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Fatal(rec2.Code)
	}
	if fav := get(t, a, "/favicon.ico"); fav.Code != 200 || fav.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatal(fav.Code, fav.Header())
	}
	for _, p := range []string{"/site.go", "/static/nope.js", "/api/nope", "/static/"} {
		if code := get(t, a, p).Code; code != 404 {
			t.Errorf("%s：%d", p, code)
		}
	}
	// 含 .. 的路徑由 ServeMux 清理後轉址（307 到 /site.go，仍是 404）；Python 直接回 404
	if rec := get(t, a, "/static/../site.go"); rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/site.go" {
		t.Fatal(rec.Code, rec.Header())
	}
	post := httptest.NewRecorder()
	a.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/stats", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatal(post.Code)
	}
}

// 分段讀取（2026-11-14）：每個部分只回傳列出的欄位，內容與完整 /api/guide 相同；沒有的部分回 404。
func TestGuidePartsMatchFullGuide(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	full := getJSON(t, a, "/api/guide")
	for part, keys := range services.GuideParts {
		got := getJSON(t, a, "/api/guide/"+part)
		if num(got["cached_seconds"]) != GuideCacheSeconds {
			t.Fatal(part, got["cached_seconds"])
		}
		delete(got, "cached_seconds")
		if len(got) != len(keys) {
			t.Fatalf("%s：%d 個欄位，應該是 %v", part, len(got), keys)
		}
		for _, k := range keys {
			want, _ := json.Marshal(full[k])
			have, _ := json.Marshal(got[k])
			if !bytes.Equal(want, have) {
				t.Fatalf("%s.%s 和完整手冊不同：\n%s\n%s", part, k, have, want)
			}
		}
	}
	if rec := get(t, a, "/api/guide/nope"); rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}
	if rec := get(t, New(Options{}), "/api/guide/summary"); rec.Code != 503 {
		t.Fatal(rec.Code)
	}
}

// 首頁卡片只讀 summary；手冊每個有資料的欄位都標了存在的部分；site.js 填的欄位頁面上都有。
func TestPageWiringForLazyGuide(t *testing.T) {
	e := setup(t)
	a := e.app(Options{})
	page := get(t, a, "/").Body.String()
	sum := getJSON(t, a, "/api/guide/summary")
	cards := m(sum["cards"])
	for _, k := range []string{"qualities", "animals", "areas", "machines", "rare_seeds", "crop_times", "tools", "codex"} {
		if len(list(cards[k])) == 0 {
			t.Fatal(k)
		}
	}
	if num(cards["achievements"]) != float64(len(achievements.Active())) || num(cards["checkin_days"]) != float64(checkin.CycleDays()) {
		t.Fatal(cards)
	}
	tagged := 0
	for _, match := range regexp.MustCompile(`data-part="([a-z_ ]+)"`).FindAllStringSubmatch(page, -1) {
		for _, p := range strings.Fields(match[1]) {
			if _, ok := services.GuideParts[p]; !ok {
				t.Fatal("沒有這個部分：", p)
			}
		}
		tagged++
	}
	if tagged < 20 {
		t.Fatal(tagged)
	}
	js := get(t, a, "/static/site.js").Body.String()
	for _, match := range regexp.MustCompile(`(?:setText|fillRows|liList|setList)\("([a-z][a-z0-9-]*)"`).FindAllStringSubmatch(js, -1) {
		if !strings.Contains(page, fmt.Sprintf(`id="%s"`, match[1])) {
			t.Errorf("site.js 會填 #%s，但頁面上沒有這個欄位", match[1])
		}
	}
	// 新增到介紹網站的玩法（2026-11-14）
	for _, text := range []string{"試驗雜交", "發現新配方", "特殊種子怎麼來", "非當季", "取消加工訂單", "素材背包", "共同任務"} {
		if !strings.Contains(page, text) {
			t.Error(text)
		}
	}
	lab := m(getJSON(t, a, "/api/guide/seedlab")["seedlab"])
	if lab["exp"] == nil || len(list(lab["level_exp"])) != 2 || lab["gene_up_pct"] == nil {
		t.Fatal(lab)
	}
	groups := m(getJSON(t, a, "/api/guide/activities")["groups"])
	if len(list(groups["quests"])) != len(balance.QuestTypeOrder) || len(list(groups["competitions"])) != len(balance.CompetitionTypeOrder) {
		t.Fatal(groups)
	}
}
