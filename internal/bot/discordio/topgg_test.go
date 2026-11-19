package discordio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type topggCall struct {
	method, path, auth string
	count              int
}

// fakeTopGG 假的 top.gg：v1 依 v1Status 回應，v0 依 v0Status 回應，記下每次呼叫。
func fakeTopGG(t *testing.T, v1Status, v0Status int) (*httptest.Server, *[]topggCall) {
	t.Helper()
	var calls []topggCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]int
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, topggCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), body["server_count"]})
		status := v0Status
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			status = v1Status
		}
		w.WriteHeader(status)
		if status/100 != 2 {
			_, _ = w.Write([]byte(`{"title":"Unauthorized"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestPostTopGGStatsV1 新版 Token：只呼叫 v1（PATCH、Bearer）。
func TestPostTopGGStatsV1(t *testing.T) {
	srv, calls := fakeTopGG(t, http.StatusNoContent, http.StatusOK)
	if err := postTopGGStats(context.Background(), srv.Client(), srv.URL, "751056003906142279", "tok", 36); err != nil {
		t.Fatal(err)
	}
	want := []topggCall{{http.MethodPatch, "/v1/projects/@me/metrics", "Bearer tok", 36}}
	if len(*calls) != 1 || (*calls)[0] != want[0] {
		t.Fatal(*calls)
	}
}

// TestPostTopGGStatsLegacy 舊版 Token：v1 回 401 時改用 v0（POST、不加 Bearer）。
func TestPostTopGGStatsLegacy(t *testing.T) {
	srv, calls := fakeTopGG(t, http.StatusUnauthorized, http.StatusOK)
	if err := postTopGGStats(context.Background(), srv.Client(), srv.URL, "751056003906142279", "tok", 36); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[1] != (topggCall{http.MethodPost, "/bots/751056003906142279/stats", "tok", 36}) {
		t.Fatal(*calls)
	}
}

// TestPostTopGGStatsError 兩邊都失敗時回傳錯誤，訊息不含 Token；v1 不是認證錯誤（例如 500）時不改用 v0。
func TestPostTopGGStatsError(t *testing.T) {
	srv, _ := fakeTopGG(t, http.StatusUnauthorized, http.StatusUnauthorized)
	err := postTopGGStats(context.Background(), srv.Client(), srv.URL, "1", "secret-token", 1)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
	srv2, calls := fakeTopGG(t, http.StatusInternalServerError, http.StatusOK)
	if err := postTopGGStats(context.Background(), srv2.Client(), srv2.URL, "1", "tok", 1); err == nil || len(*calls) != 1 {
		t.Fatal(err, *calls)
	}
}
