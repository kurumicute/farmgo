package web

// 對應 Python tests/test_monitor_api.py（管理 API 的寫入防護在登入那一批移植）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"farmgm/internal/auth"
	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/services"
	"farmgm/internal/services/svctest"
)

type webEnv struct {
	t     *testing.T
	ctx   context.Context
	d     *db.DB
	clock *clock.Manual
	svc   *services.AdminService
	app   *App
	home  int64
	pid   int64 // 「主群組」中的「阿明」（Python player_id 夾具）

	cookies map[string]string // 簡易 cookie jar（對應 TestClient 保留登入狀態）
}

func setupWeb(t *testing.T, refresh float64) *webEnv {
	t.Helper()
	return setupWebAuth(t, refresh, testAuth())
}

// setupWebAuth 指定管理員登入設定（nil：未設定帳號）。
func setupWebAuth(t *testing.T, refresh float64, a *auth.AdminAuth) *webEnv {
	t.Helper()
	d := svctest.Prepare(t, services.ClearNewsCache)
	c := clock.NewManual(time.Time{})
	e := &webEnv{t: t, ctx: context.Background(), d: d, clock: c, svc: services.NewAdminService(d, services.Options{Clock: c})}
	e.app = New(Options{DB: d, Clock: c, RefreshSeconds: refresh, AllowedHosts: []string{"testserver", "127.0.0.1"}, Auth: a})
	t.Cleanup(e.app.Close)
	return e
}

// setupPlayer setupWeb 加上「主群組」中的「阿明」（Python player_id 夾具）。
func setupPlayer(t *testing.T, refresh float64) *webEnv {
	t.Helper()
	e := setupWeb(t, refresh)
	e.home = e.group("主群組")
	e.pid = e.farmer("阿明", e.home)
	return e
}

func itoa(n int) string { return strconv.Itoa(n) }

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func (e *webEnv) group(name string) int64 {
	e.t.Helper()
	return must[services.GroupInfo](e.t)(e.svc.CreateGroup(e.ctx, name, nil)).ID
}

func (e *webEnv) farmer(name string, groupID int64) int64 {
	e.t.Helper()
	p := must[services.PlayerInfo](e.t)(e.svc.CreatePlayer(e.ctx, name, nil, nil))
	must[services.FarmStatus](e.t)(e.svc.CreateFarm(e.ctx, p.ID, "", &groupID))
	return p.ID
}

func (e *webEnv) ok(_ services.ActionResult, err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *webEnv) grow(pid int64, plot int, crop string) {
	e.t.Helper()
	e.ok(e.svc.BuySeeds(e.ctx, pid, crop, 1))
	e.ok(e.svc.PlantCrop(e.ctx, pid, plot, crop))
	e.ok(e.svc.WaterCrop(e.ctx, pid, plot))
}

// request 以 Host: testserver 呼叫網站（對應 FastAPI TestClient）。
func (e *webEnv) request(method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Host = "testserver"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range e.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	e.app.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if e.cookies == nil {
			e.cookies = map[string]string{}
		}
		if c.MaxAge < 0 {
			delete(e.cookies, c.Name)
		} else {
			e.cookies[c.Name] = c.Value
		}
	}
	return rec
}

func (e *webEnv) get(target string, params map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		target += "?" + q.Encode()
	}
	return e.request(http.MethodGet, target, nil, nil)
}

// getJSON GET 並解析 JSON（狀態必須是 200）。
func (e *webEnv) getJSON(target string, params map[string]string, out any) {
	e.t.Helper()
	rec := e.get(target, params)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET %s：%d %s", target, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		e.t.Fatal(err)
	}
}

type obj = map[string]any

func num(v any) int { return int(v.(float64)) }

// setupFarm Python _setup：小麥兩格可收成、胡蘿蔔一格生長中，剩 1 顆小麥種子、450 金幣。
func (e *webEnv) setupFarm() {
	e.t.Helper()
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "wheat", 3))
	for _, n := range []int{1, 2} {
		e.ok(e.svc.PlantCrop(e.ctx, e.pid, n, "wheat"))
	}
	for _, n := range []int{1, 2} {
		e.ok(e.svc.WaterCrop(e.ctx, e.pid, n))
	}
	e.clock.Advance(60)
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "carrot", 1))
	e.ok(e.svc.PlantCrop(e.ctx, e.pid, 3, "carrot"))
	e.ok(e.svc.WaterCrop(e.ctx, e.pid, 3))
}

func (e *webEnv) scalar(sql string, args ...any) int {
	e.t.Helper()
	var n int
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error { return tx.QueryRow(ctx, sql, args...).Scan(&n) })
	return n
}

func TestSummaryMatchesDatabase(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.farmer("小華", e.home)
	e.setupFarm()
	var data obj
	e.getJSON("/api/monitor/summary", nil, &data)
	if num(data["total_gold"]) != e.scalar("SELECT sum(gold) FROM farms") || num(data["total_gold"]) != 450+500 {
		t.Fatal(data)
	}
	if num(data["transactions_today"]) != e.scalar("SELECT count(*) FROM game_transactions") {
		t.Fatal(data)
	}
	if num(data["players"]) != 2 || num(data["farms"]) != 2 || num(data["plots"]) != 8 || num(data["growing"]) != 1 ||
		num(data["ready"]) != 2 || num(data["inventory_items"]) != 1 { // 剩 1 顆小麥種子
		t.Fatal(data)
	}
}

func TestFarmListAndDetail(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.setupFarm()
	bob := must[services.PlayerInfo](t)(e.svc.CreatePlayer(e.ctx, "Bob", nil, nil))
	must[services.FarmStatus](t)(e.svc.CreateFarm(e.ctx, bob.ID, "藍莓園", &e.home))
	var farms struct {
		Items []obj `json:"items"`
		Total int   `json:"total"`
	}
	e.getJSON("/api/monitor/farms", nil, &farms)
	row := func() obj {
		for _, f := range farms.Items {
			if f["player_name"] == "阿明" {
				return f
			}
		}
		t.Fatal(farms)
		return nil
	}
	r := row()
	if farms.Total != 2 || num(r["plots"]) != 4 || num(r["empty"]) != 1 || num(r["growing"]) != 1 || num(r["ready"]) != 2 ||
		num(r["gold"]) != 450 || num(r["warehouse_used"]) != 0 { // 剩下的 1 顆種子放素材背包，不佔倉庫
		t.Fatal(r)
	}

	// 賭場獎品佔倉庫空間、素材背包不佔：監控網站與群組成員列表都和遊戲內的倉庫用量一致
	farmID := num(r["farm_id"])
	repotest.Tx(t, e.d, func(ctx context.Context, tx *db.Tx) error {
		if err := inventory.AddPrize(ctx, tx, int64(farmID), "strawberry", "rare", "none", 2, 3); err != nil {
			return err
		}
		if err := inventory.Add(ctx, tx, int64(farmID), "wood", 7, "normal", "none"); err != nil {
			return err
		}
		return inventory.Add(ctx, tx, int64(farmID), "seed_amber_melon", 2, "normal", "none")
	})
	gameUsed := must[services.FarmStatus](t)(e.svc.GetFarmStatus(e.ctx, e.pid)).WarehouseUsed
	if gameUsed != 3 { // 只有賭場獎品；種子與素材都放素材背包
		t.Fatal(gameUsed)
	}
	e.getJSON("/api/monitor/farms", nil, &farms)
	if r = row(); num(r["warehouse_used"]) != gameUsed {
		t.Fatal(r)
	}
	var detail obj
	e.getJSON("/api/monitor/farms/"+itoa(farmID), nil, &detail)
	if num(detail["warehouse_used"]) != gameUsed {
		t.Fatal(detail["warehouse_used"])
	}
	for _, m := range must[[]groups.Member](t)(e.svc.GroupMembers(e.ctx, e.home)) {
		if m.PlayerID == e.pid && m.Farm.WarehouseUsed != gameUsed {
			t.Fatal(m.Farm)
		}
	}

	var found struct {
		Items []obj `json:"items"`
		Total int   `json:"total"`
	}
	e.getJSON("/api/monitor/farms", map[string]string{"q": "藍莓"}, &found)
	if len(found.Items) != 1 || found.Items[0]["farm_name"] != "藍莓園" {
		t.Fatal(found)
	}
	e.getJSON("/api/monitor/farms", map[string]string{"q": "%"}, &found)
	if found.Total != 0 { // LIKE 萬用字元被跳脫
		t.Fatal(found)
	}

	plots := map[int]obj{}
	for _, p := range detail["plots"].([]any) {
		plots[num(p.(obj)["plot_number"])] = p.(obj)
	}
	if plots[1]["state"] != "ready" || plots[1]["state_label"] != "可收成" || plots[3]["state"] != "growing" ||
		num(plots[3]["remaining_seconds"]) != 180 || plots[4]["state"] != "empty" || plots[4]["crop_name"] != nil {
		t.Fatal(plots)
	}
	if tx := detail["transactions"].([]any)[0].(obj); tx["event_type"] != "water" {
		t.Fatal(tx)
	}
}

func TestInputValidation(t *testing.T) {
	e := setupWeb(t, 0.2)
	cases := []struct {
		target string
		params map[string]string
		want   int
	}{
		{"/api/monitor/farms/0", nil, 422},
		{"/api/monitor/farms/abc", nil, 422},
		{"/api/monitor/farms/999", nil, 404},
		{"/api/monitor/farms", map[string]string{"limit": "1000"}, 422},
		{"/api/monitor/farms", map[string]string{"q": strings.Repeat("x", 41)}, 422},
		{"/api/monitor/transactions", map[string]string{"limit": "0"}, 422},
	}
	for _, c := range cases {
		if rec := e.get(c.target, c.params); rec.Code != c.want {
			t.Errorf("%s %v：%d %s", c.target, c.params, rec.Code, rec.Body.String())
		}
	}
	// 錯誤格式與 FastAPI HTTPException 相同：{"detail": "..."}（前端顯示 detail 字串）
	var body obj
	_ = json.Unmarshal(e.get("/api/monitor/farms/999", nil).Body.Bytes(), &body)
	if body["detail"] != "找不到這座農場。" {
		t.Fatal(body)
	}
}

func TestTransactionsAndUpdateAfterGameAction(t *testing.T) {
	e := setupPlayer(t, 0.2)
	var before, after obj
	e.getJSON("/api/monitor/summary", nil, &before)
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "wheat", 2))
	e.getJSON("/api/monitor/summary", nil, &after)
	if num(after["total_gold"]) != num(before["total_gold"])-20 || num(after["inventory_items"]) != num(before["inventory_items"])+2 {
		t.Fatal(before, after)
	}
	var txs []obj
	e.getJSON("/api/monitor/transactions", nil, &txs)
	if txs[0]["event_type"] != "buy_seed" || num(txs[0]["gold_delta"]) != -20 || txs[0]["player_name"] != "阿明" ||
		!strings.Contains(txs[0]["description"].(string), "購買 2 顆小麥種子") {
		t.Fatal(txs[0])
	}
}

func TestChartsFromRealData(t *testing.T) {
	e := setupPlayer(t, 0.2)
	var charts struct {
		Crops      []obj `json:"crops"`
		Activity   []obj `json:"activity"`
		HasHistory bool  `json:"has_history"`
	}
	e.getJSON("/api/monitor/charts", nil, &charts)
	if !charts.HasHistory || num(charts.Activity[len(charts.Activity)-1]["total_gold"]) != 500 {
		t.Fatal(charts)
	}
	e.setupFarm()
	e.getJSON("/api/monitor/charts", nil, &charts)
	var wheat obj
	for _, c := range charts.Crops {
		if c["code"] == "wheat" {
			wheat = c
		}
	}
	total := 0
	for _, a := range charts.Activity {
		total += num(a["transactions"])
	}
	if num(wheat["growing"]) != 0 || num(wheat["ready"]) != 2 || num(charts.Activity[len(charts.Activity)-1]["total_gold"]) != 450 || total != 9 {
		t.Fatal(wheat, charts.Activity, total)
	}
}

// ---------- WebSocket ----------

func (e *webEnv) wsDial(srv *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/monitor", h)
}

type wsMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Data    *struct {
		Summary      obj   `json:"summary"`
		Transactions []obj `json:"transactions"`
	} `json:"data"`
}

func readWS(t *testing.T, c *websocket.Conn) wsMsg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var m wsMsg
	if err := c.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebsocketPushesFreshSnapshots(t *testing.T) {
	e := setupPlayer(t, 0.2)
	srv := httptest.NewServer(e.app)
	defer srv.Close()
	c, _, err := e.wsDial(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first := readWS(t, c)
	if first.Type != "snapshot" || num(first.Data.Summary["total_gold"]) != 500 {
		t.Fatalf("%+v", first)
	}
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "wheat", 1))
	second := readWS(t, c) // 下一次定時快照
	if num(second.Data.Summary["total_gold"]) != 490 || second.Data.Transactions[0]["event_type"] != "buy_seed" {
		t.Fatalf("%+v", second)
	}
}

// 多個分頁同時連線：同一段時間只查詢一次資料庫；送出 refresh 時仍會拿到最新資料。
func TestWebsocketConnectionsShareOneSnapshot(t *testing.T) {
	e := setupPlayer(t, 5)
	srv := httptest.NewServer(e.app)
	defer srv.Close()
	a, _, err := e.wsDial(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if readWS(t, a).Type != "snapshot" {
		t.Fatal("a")
	}
	b, _, err := e.wsDial(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if readWS(t, b).Type != "snapshot" {
		t.Fatal("b")
	}
	e.app.snapshot.mu.Lock()
	builds := e.app.snapshot.builds
	e.app.snapshot.mu.Unlock()
	if builds != 1 { // 第二個分頁沿用第一個查到的快照
		t.Fatal(builds)
	}
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "wheat", 1))
	time.Sleep(ForceMaxAge + 100*time.Millisecond)
	if err := a.WriteMessage(websocket.TextMessage, []byte("refresh")); err != nil {
		t.Fatal(err)
	}
	if fresh := readWS(t, a); num(fresh.Data.Summary["total_gold"]) != 490 {
		t.Fatalf("%+v", fresh)
	}
}

func TestWebsocketRejectsCrossOrigin(t *testing.T) {
	e := setupWeb(t, 0.2)
	srv := httptest.NewServer(e.app)
	defer srv.Close()
	c, resp, err := e.wsDial(srv, "http://evil.example")
	if err == nil {
		c.Close()
		t.Fatal("跨來源的 WebSocket 應被拒絕")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp, err)
	}
	// 同源可以連
	c, _, err = e.wsDial(srv, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestMonitorIsReadOnly(t *testing.T) {
	e := setupPlayer(t, 0.2)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, target := range []string{"/api/monitor/farms/1", "/api/monitor/summary", "/api/monitor/players/1/backpack"} {
			if rec := e.request(method, target, nil, nil); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s：%d", method, target, rec.Code)
			}
		}
	}
	if e.scalar("SELECT gold FROM farms") != 500 {
		t.Fatal("監控 API 不可修改資料")
	}
}

func TestDatabaseDownReturnsErrorNotFakeData(t *testing.T) {
	d, err := db.Open(context.Background(), "postgresql://u:p@0.0.0.0:1/nope_test", db.PoolOptions{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	app := New(Options{DB: d, RefreshSeconds: 0.2, AllowedHosts: []string{"testserver", "127.0.0.1"}})
	defer app.Close()
	e := &webEnv{t: t, app: app}
	rec := e.get("/api/monitor/summary", nil)
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"detail":"資料庫暫時無法連線，無法取得監控資料。"}` {
		t.Fatal(rec.Code, rec.Body.String())
	}
	srv := httptest.NewServer(app)
	defer srv.Close()
	c, _, err := e.wsDial(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg obj
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if _, has := msg["data"]; msg["type"] != "error" || has {
		t.Fatal(msg)
	}
}

func TestIndexServedWithSecurityHeaders(t *testing.T) {
	e := setupWeb(t, 0.2)
	rec := e.get("/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "農場即時監控") ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal(rec.Code, rec.Header())
	}
	if e.get("/docs", nil).Code != http.StatusNotFound {
		t.Fatal("不應提供 API 文件頁面")
	}
	js := e.get("/static/app.js", nil)
	if js.Code != 200 || js.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
		js.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(js.Code, js.Header())
	}
	if e.get("/static/../web.go", nil).Code == 200 || e.get("/static/", nil).Code == 200 {
		t.Fatal("不可讀取靜態目錄以外的檔案")
	}
	if e.get("/api/monitor/summary", nil).Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API 應為 no-store")
	}
}

// ---------- 群組、偷菜、排行榜、背包 ----------

type socialWorld struct{ a, b, c, g1, g2 int64 }

func (e *webEnv) socialWorld() socialWorld {
	e.t.Helper()
	w := socialWorld{g1: e.group("一號群"), g2: e.group("二號群")}
	w.a, w.b, w.c = e.farmer("甲", w.g1), e.farmer("乙", w.g1), e.farmer("丙", w.g2)
	must[bool](e.t)(e.svc.AddMember(e.ctx, w.g2, w.a))
	e.grow(w.b, 1, "strawberry")
	e.grow(w.c, 1, "strawberry")
	e.clock.Advance(600)
	e.ok(e.svc.StealCrop(e.ctx, w.g1, w.a, w.b, 1, 2))
	e.clock.Advance(86_400) // 隔天
	return w
}

// setupSocial Python 的 social_world 夾具（沒有 player_id 的阿明）。
func setupSocial(t *testing.T) (*webEnv, socialWorld) {
	e := setupWeb(t, 0.2)
	return e, e.socialWorld()
}

func TestGroupsEndpoints(t *testing.T) {
	e, w := setupSocial(t)
	var groups []obj
	e.getJSON("/api/monitor/groups", nil, &groups)
	var got []string
	for _, g := range groups {
		got = append(got, g["name"].(string)+":"+itoa(num(g["member_count"])))
	}
	if !slices.Equal(got, []string{"一號群:2", "二號群:2"}) {
		t.Fatal(got)
	}
	var detail struct {
		Members []struct {
			PlayerName string `json:"player_name"`
			Farm       obj    `json:"farm"`
		} `json:"members"`
	}
	e.getJSON("/api/monitor/groups/"+itoa(int(w.g1)), nil, &detail)
	if len(detail.Members) != 2 || detail.Members[0].PlayerName != "甲" || detail.Members[1].PlayerName != "乙" {
		t.Fatal(detail)
	}
	// 乙的草莓被偷後一直沒收成，隔天已乾枯（狀態依時間即時結算）
	if num(detail.Members[1].Farm["withered"]) != 1 {
		t.Fatal(detail.Members[1].Farm)
	}
	if e.get("/api/monitor/groups/999", nil).Code != 404 {
		t.Fatal("不存在的群組應回 404")
	}
}

func TestStealsEndpointFilters(t *testing.T) {
	e, w := setupSocial(t)
	e.grow(w.c, 2, "wheat")
	e.clock.Advance(60)
	e.ok(e.svc.StealCrop(e.ctx, w.g2, w.a, w.c, 2, 1))
	var rows []obj
	e.getJSON("/api/monitor/steals", nil, &rows)
	var got []string
	for _, r := range rows {
		got = append(got, strings.Join([]string{r["group_name"].(string), r["thief_name"].(string), r["victim_name"].(string),
			r["crop_name"].(string), itoa(num(r["quantity"])), itoa(num(r["plot_number"]))}, "/"))
		if _, has := r["block_chance"]; has {
			t.Fatal("不應輸出 block_chance", r)
		}
	}
	if !slices.Equal(got, []string{"二號群/甲/丙/小麥/1/2", "一號群/甲/乙/草莓/2/1"}) {
		t.Fatal(got)
	}
	count := func(params map[string]string) int {
		var rows []obj
		e.getJSON("/api/monitor/steals", params, &rows)
		return len(rows)
	}
	if count(map[string]string{"group_id": itoa(int(w.g1))}) != 1 || count(map[string]string{"player_id": itoa(int(w.b))}) != 1 ||
		count(map[string]string{"player_id": itoa(int(w.a))}) != 2 {
		t.Fatal("篩選結果不符")
	}
	// 時鐘起點 2026-01-01 00:00 UTC = 台灣 08:00；第二筆在隔天
	if count(map[string]string{"date_from": "2026-01-01", "date_to": "2026-01-01"}) != 1 || count(map[string]string{"date_from": "2026-01-02"}) != 1 {
		t.Fatal("日期篩選結果不符")
	}
	if e.get("/api/monitor/steals", map[string]string{"date_from": "2026-01-03", "date_to": "2026-01-01"}).Code != 422 ||
		e.get("/api/monitor/steals", map[string]string{"date_from": "not-a-date"}).Code != 422 {
		t.Fatal("日期驗證")
	}
}

func TestLeaderboardEndpoint(t *testing.T) {
	e, w := setupSocial(t)
	e.ok(e.svc.SellCrop(e.ctx, w.a, "strawberry", 2, "normal", "none"))
	var data struct {
		Items []obj `json:"items"`
	}
	e.getJSON("/api/monitor/leaderboard", map[string]string{"scope": "global", "kind": "sales"}, &data)
	if len(data.Items) != 3 || data.Items[0]["player_name"] != "甲" || num(data.Items[0]["sold_quantity"]) != 2 {
		t.Fatal(data)
	}
	e.getJSON("/api/monitor/leaderboard", map[string]string{"scope": "group", "group_id": itoa(int(w.g1)), "kind": "money"}, &data)
	if len(data.Items) != 2 || data.Items[0]["player_name"] != "甲" || data.Items[1]["player_name"] != "乙" {
		t.Fatal(data)
	}
	if e.get("/api/monitor/leaderboard", map[string]string{"scope": "group"}).Code != 422 ||
		e.get("/api/monitor/leaderboard", map[string]string{"scope": "group", "group_id": "999"}).Code != 404 ||
		e.get("/api/monitor/leaderboard", map[string]string{"kind": "hack"}).Code != 422 {
		t.Fatal("參數驗證")
	}
}

func TestBackpackEndpoint(t *testing.T) {
	e, w := setupSocial(t)
	e.ok(e.svc.Buy(e.ctx, w.a, "fertilizer", 1))
	var bp struct {
		Produce     []obj `json:"produce"`
		Consumables []obj `json:"consumables"`
		Tools       []obj `json:"tools"`
		Effects     []obj `json:"effects"`
	}
	e.getJSON("/api/monitor/players/"+itoa(int(w.a))+"/backpack", nil, &bp)
	if len(bp.Produce) != 1 || bp.Produce[0]["name"] != "草莓" || num(bp.Produce[0]["quantity"]) != 2 || num(bp.Produce[0]["price"]) != 25 {
		t.Fatal(bp.Produce)
	}
	if len(bp.Consumables) != 1 || bp.Consumables[0]["name"] != "化肥" || bp.Tools[0]["owned"] != false || bp.Effects[0]["status"] != "inactive" {
		t.Fatal(bp)
	}
	if e.get("/api/monitor/players/999/backpack", nil).Code != 404 {
		t.Fatal("404")
	}
}

func TestUntrustedHostRejected(t *testing.T) {
	e := setupWeb(t, 0.2)
	req := httptest.NewRequest(http.MethodGet, "/api/monitor/summary", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	e.app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	for _, h := range []string{"testserver:8000", "127.0.0.1:1234"} {
		if !e.app.hostAllowed(h) {
			t.Fatal(h)
		}
	}
	if e.app.hostAllowed("testserver.evil.example") {
		t.Fatal("不可接受相似的 Host")
	}
}

// 沒有任何遊戲操作時沿用上一份快照（不重新掃描），有操作或超過最長沿用時間才重新查詢。
func TestSnapshotReusedUntilDataChanges(t *testing.T) {
	e := setupPlayer(t, 0.2)
	m := services.NewMonitorService(e.d, e.clock)
	c := &SnapshotCache{}
	first := must[*services.Snapshot](t)(c.Get(e.ctx, m, 0, false))
	e.clock.Advance(5)
	again := must[*services.Snapshot](t)(c.Get(e.ctx, m, 0, false))
	if c.builds != 1 || !again.GeneratedAt.Equal(first.GeneratedAt) || again.ServerTime.Equal(first.ServerTime) { // 伺服器時間仍然更新
		t.Fatal(c.builds, first.GeneratedAt, again.GeneratedAt)
	}
	e.ok(e.svc.BuySeeds(e.ctx, e.pid, "wheat", 1))
	changed := must[*services.Snapshot](t)(c.Get(e.ctx, m, 0, false))
	if c.builds != 2 || changed.Summary.TotalGold != 490 {
		t.Fatal(c.builds, changed.Summary)
	}
	must[*services.Snapshot](t)(c.Get(e.ctx, m, 0, true)) // 使用者按重新整理：一定重新查詢
	if c.builds != 3 {
		t.Fatal(c.builds)
	}
	c.builtAt = c.builtAt.Add(-StaleMax) // 超過最長沿用時間
	must[*services.Snapshot](t)(c.Get(e.ctx, m, 0, false))
	if c.builds != 4 {
		t.Fatal(c.builds)
	}
}
