package discordio

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"farmgm/internal/bot/ui"
)

// 互動 API 失敗時記下 Discord 錯誤碼、耗時與互動年齡，但不記錄含權杖的網址。
func TestInteractionAPIErrorIsLoggedWithCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Unknown interaction","code":10062}`))
	}))
	old := API
	API = srv.URL + "/api/v10/"
	t.Cleanup(func() { API = old; srv.Close() })

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	i := &discordgo.Interaction{ID: snowflake(time.Now().Add(-3200 * time.Millisecond)), AppID: "app", Token: "secret-token-xyz",
		Type: discordgo.InteractionMessageComponent, Data: discordgo.MessageComponentInteractionData{CustomID: "c1"}}
	it := NewInteraction(session(t), i, ui.NewRegistry())
	content := "hi"
	if err := it.Show(context.Background(), ui.Message{Content: &content}); err == nil {
		t.Fatal("應該失敗")
	}
	out := buf.String()
	for _, want := range []string{"互動 API 失敗", "op=callback:defer_update", "status=404", "code=10062", "age_ms=", "took_ms="} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q：%s", want, out)
		}
	}
	if strings.Contains(out, "secret-token-xyz") {
		t.Fatalf("日誌含權杖：%s", out)
	}
}

// 成功但偏慢的請求記錄各階段時間點。
func TestSlowInteractionAPIIsTraced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	old, oldSlow := API, SlowAPI
	API, SlowAPI = srv.URL+"/api/v10/", 50*time.Millisecond
	t.Cleanup(func() { API, SlowAPI = old, oldSlow; srv.Close() })

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	i := &discordgo.Interaction{ID: snowflake(time.Now()), AppID: "app", Token: "secret-token-xyz",
		Type: discordgo.InteractionMessageComponent, Data: discordgo.MessageComponentInteractionData{CustomID: "c1"}}
	it := NewInteraction(session(t), i, ui.NewRegistry())
	if err := it.DeferUpdateIfPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"互動 API 偏慢", "op=callback:defer_update", "got_conn=", "wrote=", "first_byte=", "reused="} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q：%s", want, out)
		}
	}
	if strings.Contains(out, "first_byte=-1") || strings.Contains(out, "secret-token-xyz") {
		t.Fatal(out)
	}
}
