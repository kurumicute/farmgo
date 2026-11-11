package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"farmgm/internal/game/gameerr"
)

// DBErrorMessage 資料庫錯誤時回給前端的訊息（Python DB_ERROR_MESSAGE；不含 SQL 或連線資訊）。
const DBErrorMessage = "資料庫暫時無法連線，無法取得監控資料。"

// writeJSON 輸出 JSON（不跳脫 HTML 字元；Content-Type 與 FastAPI 相同）。
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		slog.Error("網站輸出 JSON 失敗", "type", fmt.Sprintf("%T", err))
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(`{"detail":"伺服器發生未預期的錯誤。"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

// writeDetail 錯誤回應 {"detail": "..."}（FastAPI HTTPException 的格式；前端顯示 detail 字串）。
func writeDetail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

// writeServiceError 服務層錯誤：NotFound → 404、其他遊戲規則錯誤 → 400、其餘（資料庫）→ 503（只記錄錯誤類型）。
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if ge, ok := gameerr.As(err); ok {
		status := http.StatusBadRequest
		if ge.Kind == gameerr.NotFound {
			status = http.StatusNotFound
		}
		writeDetail(w, status, ge.Error())
		return
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // 用戶端已離開
	}
	slog.Warn("網站 API 資料庫錯誤", "path", r.URL.Path, "type", fmt.Sprintf("%T", err))
	writeDetail(w, http.StatusServiceUnavailable, DBErrorMessage)
}

// respond 服務呼叫的結果：錯誤依 writeServiceError；v 為 nil 指標時回 404 notFound。
func respond[T any](w http.ResponseWriter, r *http.Request, v *T, err error, notFound string) {
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if v == nil {
		writeDetail(w, http.StatusNotFound, notFound)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// respondValue 不會是「找不到」的結果。
func respondValue[T any](w http.ResponseWriter, r *http.Request, v T, err error) {
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
