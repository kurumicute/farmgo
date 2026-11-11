package web

// 對應 Python tests/test_ranch.py 的 test_monitor_ranch_api、tests/test_defense.py 的 test_monitor_defense_api_read_only，
// 以及 Go 版自己的監控端點形狀檢查（鍵的順序、404、唯讀）。其餘端點的 Python 測試與管理 API 寫在一起，在管理 API 那一批移植。

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/repotest"
	"farmgm/internal/services"
)

func (e *webEnv) give(pid int64, code string, qty int) {
	e.t.Helper()
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		var farmID int64
		if err := tx.QueryRow(ctx, "SELECT id FROM farms WHERE player_id = $1", pid).Scan(&farmID); err != nil {
			return err
		}
		return inventory.Add(ctx, tx, farmID, code, qty, "normal", "none")
	})
}

func (e *webEnv) setGold(pid int64, gold int) {
	e.t.Helper()
	repotest.Tx(e.t, e.d, func(ctx context.Context, tx *db.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE farms SET gold = $2 WHERE player_id = $1", pid, gold)
		return err
	})
}

// keysOf JSON 物件的鍵（依原始順序）。
func keysOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatal(string(raw), err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestMonitorRanchAPI(t *testing.T) {
	e := setupWebAuth(t, 0.2, nil)
	home := e.group("主群組")
	pid := e.farmer("牧場主", home)
	e.setGold(pid, 100_000)
	e.ok(e.svc.Build(e.ctx, pid, "畜舍"))
	e.ok(e.svc.Build(e.ctx, pid, "磨坊"))
	e.ok(e.svc.BuyAnimals(e.ctx, pid, "雞", 2))
	e.ok(e.svc.FeedAnimal(e.ctx, pid, 1))
	e.give(pid, "wheat", 3)
	e.ok(e.svc.StartCraft(e.ctx, pid, "flour", 1))

	rec := e.get("/api/monitor/ranch", nil)
	var farms []struct {
		Capacity       int             `json:"capacity"`
		AnimalCount    int             `json:"animal_count"`
		StateCounts    map[string]int  `json:"state_counts"`
		FeedCostHungry int             `json:"feed_cost_hungry"`
		NextReadyAt    *string         `json:"next_ready_at"`
		Buildings      json.RawMessage `json:"buildings"`
		Animals        []obj           `json:"animals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &farms); err != nil || len(farms) != 1 {
		t.Fatal(rec.Body.String(), err)
	}
	f := farms[0]
	if f.Capacity != 4 || f.AnimalCount != 2 || f.FeedCostHungry != 20 || f.NextReadyAt == nil ||
		f.StateCounts["hungry"] != 1 || f.StateCounts["producing"] != 1 || f.StateCounts["ready"] != 0 {
		t.Fatalf("%+v", f)
	}
	if string(f.Buildings) != `{"畜舍":1,"磨坊":1}` {
		t.Fatal(string(f.Buildings))
	}
	if f.Animals[0]["state_label"] != "生產中" || f.Animals[1]["state_label"] != "待餵食" {
		t.Fatal(f.Animals)
	}
	var orders []obj
	e.getJSON("/api/monitor/craft-orders", nil, &orders)
	if len(orders) != 1 || orders[0]["player_name"] != "牧場主" || orders[0]["state_label"] != "加工中" ||
		num(orders[0]["remaining_seconds"]) != 1800 {
		t.Fatal(orders)
	}
	if inputs := orders[0]["inputs"].(obj); len(inputs) != 1 || num(inputs["小麥"]) != 3 {
		t.Fatal(inputs)
	}
	e.getJSON("/api/monitor/craft-orders", map[string]string{"include_collected": "false"}, &orders)
	if len(orders) != 1 {
		t.Fatal(orders)
	}
	if e.get("/api/monitor/craft-orders", map[string]string{"include_collected": "maybe"}).Code != 422 {
		t.Fatal("布林參數驗證")
	}
	for _, target := range []string{"/api/monitor/ranch", "/api/monitor/craft-orders"} {
		if res := e.send("POST", target, "{}", nil); res.StatusCode != 405 {
			t.Fatal(target, res.StatusCode)
		}
	}
	if e.get("/api/monitor/leaderboard", map[string]string{"kind": "goods"}).Code != 200 {
		t.Fatal("goods 排行")
	}
}

func TestMonitorDefenseAPIReadOnly(t *testing.T) {
	e := setupWebAuth(t, 0.2, nil)
	g := e.group("防守群")
	thief, thief2, owner := e.farmer("小偷", g), e.farmer("小偷二號", g), e.farmer("農夫", g)
	e.setGold(owner, 10_000)
	e.ok(e.svc.BuyDefense(e.ctx, owner, "稻草人"))
	e.ok(e.svc.BuyDefense(e.ctx, owner, "警戒犬"))
	e.grow(owner, 1, "strawberry")
	e.clock.Advance(600)
	withDice := func(v float64) *services.AdminService {
		return services.NewAdminService(e.d, services.Options{Clock: e.clock, Rng: func() float64 { return v }})
	}
	e.ok(withDice(0.0).StealCrop(e.ctx, g, thief, owner, 1, 2))
	e.ok(withDice(0.9).StealCrop(e.ctx, g, thief2, owner, 1, 1))

	var farms []struct {
		PlayerName string `json:"player_name"`
		Devices    []obj  `json:"devices"`
	}
	e.getJSON("/api/monitor/defenses", nil, &farms)
	if len(farms) != 1 || farms[0].PlayerName != "農夫" || len(farms[0].Devices) != 2 ||
		farms[0].Devices[0]["name"] != "警戒犬" || farms[0].Devices[0]["end_plot"] != nil ||
		farms[0].Devices[1]["name"] != "稻草人" || num(farms[0].Devices[1]["end_plot"]) != 3 {
		t.Fatalf("%+v", farms)
	}
	var rows []obj
	e.getJSON("/api/monitor/steals", nil, &rows)
	var got []string
	for _, r := range rows {
		blocked := "-"
		if r["blocked_by_name"] != nil {
			blocked = r["blocked_by_name"].(string)
		}
		got = append(got, strings.Join([]string{r["thief_name"].(string), r["outcome"].(string), blocked, itoa(num(r["quantity"]))}, "/"))
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"小偷/blocked/稻草人/2", "小偷二號/success/-/1"}) {
		t.Fatal(got)
	}
	e.getJSON("/api/monitor/steals", map[string]string{"outcome": "blocked"}, &rows)
	if len(rows) != 1 || e.get("/api/monitor/steals", map[string]string{"outcome": "hack"}).Code != 422 {
		t.Fatal("outcome 篩選")
	}
	var summary obj
	e.getJSON("/api/monitor/summary", nil, &summary)
	if num(summary["steals_today"]) != 1 || num(summary["blocks_today"]) != 1 {
		t.Fatal(summary)
	}
	if res := e.send("POST", "/api/monitor/defenses", "{}", nil); res.StatusCode != 405 {
		t.Fatal(res.StatusCode)
	}
}

// Go 版：前端以 Object.entries 顯示的物件依 Python dict 的順序輸出；其他端點的形狀、404 與空清單。
func TestMonitorCatalogShapesAndOrder(t *testing.T) {
	e := setupPlayer(t, 0.2)
	var genetics struct {
		Labels struct {
			Qualities json.RawMessage `json:"qualities"`
			Mutations json.RawMessage `json:"mutations"`
		} `json:"labels"`
		Version          int   `json:"version"`
		GrowSecondsRange []int `json:"grow_seconds_range"`
		History          []obj `json:"history"`
		Crops            []obj `json:"crops"`
	}
	e.getJSON("/api/monitor/genetics", nil, &genetics)
	if q := keysOf(t, genetics.Labels.Qualities); !slices.Equal(q, []string{"normal", "good", "rare", "legendary"}) {
		t.Fatal(q)
	}
	if m := keysOf(t, genetics.Labels.Mutations); !slices.Equal(m, []string{"none", "giant", "golden", "fast", "variant"}) {
		t.Fatal(m)
	}
	if genetics.Version != 1 || len(genetics.GrowSecondsRange) != 2 || len(genetics.History) != 1 || len(genetics.Crops) == 0 {
		t.Fatalf("%+v", genetics)
	}
	var catalog struct {
		Ranges    json.RawMessage `json:"ranges"`
		EventCaps json.RawMessage `json:"event_caps"`
		Items     []obj           `json:"items"`
		Buildings []obj           `json:"buildings"`
		Events    []obj           `json:"events"`
		Crops     []obj           `json:"crops"`
		Animals   []obj           `json:"animals"`
	}
	e.getJSON("/api/monitor/catalog", nil, &catalog)
	ranges := keysOf(t, catalog.Ranges)
	if len(ranges) < 9 || ranges[0] != "grow_seconds" || ranges[8] != "product_price" || ranges[9] != "sell_price_pct" {
		t.Fatal(ranges)
	}
	if catalog.Items[0]["code"] != "seed_wheat" || catalog.Buildings[0]["code"] != "barn" || len(catalog.Events) != 0 ||
		len(catalog.Crops) == 0 || len(catalog.Animals) == 0 {
		t.Fatalf("%+v", catalog.Items[:2])
	}
	var stats obj
	e.getJSON("/api/monitor/genetics/stats", nil, &stats)
	for _, k := range []string{"planted", "harvested", "stolen", "sold", "holdings"} {
		if v, ok := stats[k].([]any); !ok || len(v) != 0 { // 沒有紀錄時不造假
			t.Fatal(stats)
		}
	}
	var farm obj
	e.getJSON("/api/monitor/players/"+itoa(int(e.pid))+"/admin-farm", nil, &farm)
	if farm["player_name"] != "阿明" || farm["home_group_name"] != "主群組" || len(farm["plots"].([]any)) != 4 ||
		farm["next_group_transfer_at"] != nil || num(farm["capacity"]) == 0 {
		t.Fatal(farm)
	}
	for _, target := range []string{"/api/monitor/players/999/admin-farm", "/api/monitor/quests/999", "/api/monitor/competitions/999"} {
		if rec := e.get(target, nil); rec.Code != 404 {
			t.Fatal(target, rec.Code)
		}
	}
	for _, target := range []string{"/api/monitor/quests", "/api/monitor/competitions", "/api/monitor/ranch", "/api/monitor/craft-orders"} {
		if body := strings.TrimSpace(e.get(target, nil).Body.String()); body != "[]" {
			t.Fatal(target, body)
		}
	}
	must[services.CropView](t)(e.svc.SetCropActive(e.ctx, "root", "wheat", false))
	var actions []obj
	e.getJSON("/api/monitor/admin-actions", nil, &actions)
	if len(actions) != 1 || actions[0]["action"] != "disable_crop" || actions[0]["actor"] != "root" || actions[0]["details"] == nil {
		t.Fatal(actions)
	}
}
