package discordio

// 上傳伺服器數到 top.gg（Go 版新增，2026-11-19）：設定 TOPGG_TOKEN 時，Bot 登入（重開）時與每天台灣時間 00:00 各上傳一次 Bot 所在的伺服器數。
// API（docs.top.gg，2026-11-19 查過）：先用 v1 的 PATCH /v1/projects/@me/metrics（Authorization: Bearer <Token>），
// 認證失敗（401／403，舊版 Token 不能用在 v1）時改用 legacy v0 的 POST /bots/{bot_id}/stats（Authorization: <Token>）；
// 本文都是 {"server_count": N}。限制每分鐘 60 次，每天只送一次不會碰到。
// 失敗時隔一段時間重試（最多 topggAttempts 次），仍失敗就等隔天；Token 不會寫進日誌。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	topggBaseURL  = "https://top.gg/api"
	topggAttempts = 3
	topggRetry    = time.Minute
	topggTimeout  = 15 * time.Second
)

// topggLoop 等 Bot 登入後先上傳一次，之後每天台灣時間 00:00 上傳伺服器數；沒有設定 TOPGG_TOKEN 時不執行。
func (b *Bot) topggLoop(ctx context.Context) {
	if b.Settings.TopGGToken == "" {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-b.readyCh:
	}
	client := &http.Client{Timeout: topggTimeout}
	b.uploadTopGG(ctx, client, topggBaseURL) // Bot 重開（登入）時先上傳一次（使用者要求，2026-11-19）
	for {
		now := time.Now()
		if !sleep(ctx, nextTaipeiMidnight(now).Sub(now)) {
			return
		}
		b.uploadTopGG(ctx, client, topggBaseURL)
	}
}

// uploadTopGG 上傳一次（含重試）。
func (b *Bot) uploadTopGG(ctx context.Context, client *http.Client, baseURL string) {
	botID, count := b.guildCount()
	if botID == "" {
		b.Log.Warn("上傳 top.gg 伺服器數失敗：還不知道 Bot 的使用者 ID")
		return
	}
	var err error
	for attempt := 1; attempt <= topggAttempts; attempt++ {
		if err = postTopGGStats(ctx, client, baseURL, botID, b.Settings.TopGGToken, count); err == nil {
			b.Log.Info("已上傳 top.gg 伺服器數", "server_count", count)
			return
		}
		if attempt < topggAttempts && !sleep(ctx, topggRetry) {
			return
		}
	}
	b.Log.Warn("上傳 top.gg 伺服器數失敗", "server_count", count, "error", err.Error())
}

// guildCount Bot 的使用者 ID 與目前所在的伺服器數（discordgo 的 State）。
func (b *Bot) guildCount() (string, int) {
	st := b.S.State
	st.RLock()
	defer st.RUnlock()
	if st.User == nil {
		return "", 0
	}
	return st.User.ID, len(st.Guilds)
}

// postTopGGStats 上傳伺服器數：先試 v1，認證失敗再試 v0。錯誤訊息只含 HTTP 狀態碼與回應的前 200 字，不含 Token。
func postTopGGStats(ctx context.Context, client *http.Client, baseURL, botID, token string, count int) error {
	body, err := json.Marshal(map[string]int{"server_count": count})
	if err != nil {
		return err
	}
	status, err := topggRequest(ctx, client, http.MethodPatch, baseURL+"/v1/projects/@me/metrics", "Bearer "+token, body)
	if err == nil || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return err
	}
	_, err = topggRequest(ctx, client, http.MethodPost, fmt.Sprintf("%s/bots/%s/stats", baseURL, botID), token, body)
	return err
}

// topggRequest 送出一次請求，回傳 HTTP 狀態碼（連線失敗時為 0）。
func topggRequest(ctx context.Context, client *http.Client, method, url, auth string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("連線失敗（%T）", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return resp.StatusCode, fmt.Errorf("%s HTTP %d：%s", method, resp.StatusCode, bytes.TrimSpace(msg))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
