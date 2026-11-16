package web

// 管理網站 2026-11-16：Go 版新增功能的唯讀檢視、群組遺跡與日報、金幣與物品的增減／數值檢查／原因。

import "testing"

func TestAdminGoFeaturesAndGroupExtrasAPI(t *testing.T) {
	e := setupPlayer(t, 0.2)
	features := "/api/admin/players/" + itoa(int(e.pid)) + "/go-features"
	extras := "/api/admin/groups/" + itoa(int(e.home)) + "/extras"
	// 會模擬自動化結算，需要登入（和寫入一樣）
	if e.status("GET", features, nil) != 401 || e.status("GET", extras, nil) != 401 {
		t.Fatal("未登入應該 401")
	}
	e.login()
	code, v := e.call("GET", features, nil)
	if code != 200 || v["lab"] == nil || v["cellar"] == nil || v["fish_basket"] == nil || len(v["errors"].(map[string]any)) != 0 {
		t.Fatal(code, v)
	}
	if e.status("GET", "/api/admin/players/999999/go-features", nil) != 404 {
		t.Fatal("找不到玩家")
	}
	code, v = e.call("GET", extras, nil)
	if code != 200 || v["ruins"] == nil || v["news"] == nil {
		t.Fatal(code, v)
	}
	if e.status("GET", "/api/admin/groups/999999/extras", nil) != 404 || e.status("GET", "/api/admin/groups/abc/extras", nil) != 422 {
		t.Fatal("群組參數")
	}
}

func TestAdminGoldAndInventoryDeltaAPI(t *testing.T) {
	e := setupPlayer(t, 0.2)
	e.login()
	base := "/api/admin/players/" + itoa(int(e.pid))
	_, v := e.call("PUT", base+"/farm", map[string]any{"gold": 1000})
	if num(v["gold"]) != 1000 {
		t.Fatal(v)
	}
	if _, v := e.call("PUT", base+"/farm", map[string]any{"gold_delta": -300, "reason": "退還"}); num(v["gold"]) != 700 {
		t.Fatal(v)
	}
	// 畫面上的數值過期 → 400，不修改
	if code, v := e.call("PUT", base+"/farm", map[string]any{"gold": 5, "expect_gold": 1000}); code != 400 || num(mustGold(e, base)) != 700 {
		t.Fatal(code, v)
	}
	if e.status("PUT", base+"/farm", map[string]any{"gold": 5, "gold_delta": 1}) != 400 {
		t.Fatal("設為與增減擇一")
	}
	long := make([]rune, 201)
	for i := range long {
		long[i] = '字'
	}
	if e.status("PUT", base+"/farm", map[string]any{"gold_delta": 1, "reason": string(long)}) != 422 {
		t.Fatal("原因過長")
	}
	inv := func(body map[string]any) (int, obj) {
		return e.call("PUT", base+"/inventory", with(map[string]any{"item_code": "fertilizer"}, flat(body)...))
	}
	if code, v := inv(map[string]any{"delta": 3, "reason": "補發"}); code != 200 || num(v["quantity"]) != 3 {
		t.Fatal(code, v)
	}
	if code, _ := inv(map[string]any{"quantity": 1, "delta": 1}); code != 422 {
		t.Fatal("quantity 與 delta 擇一", code)
	}
	if code, _ := inv(map[string]any{}); code != 422 {
		t.Fatal("quantity 與 delta 至少一個", code)
	}
	if code, _ := inv(map[string]any{"quantity": 9, "expect_quantity": 0}); code != 400 {
		t.Fatal("數量過期", code)
	}
	if code, v := inv(map[string]any{"quantity": 9, "expect_quantity": 3}); code != 200 || num(v["quantity"]) != 9 {
		t.Fatal(code, v)
	}
}

func mustGold(e *webEnv, base string) any {
	e.t.Helper()
	_, v := e.call("GET", "/api/monitor/players/"+base[len("/api/admin/players/"):]+"/admin-farm", nil)
	return v["gold"]
}

func flat(m map[string]any) []any {
	out := make([]any, 0, len(m)*2)
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}
