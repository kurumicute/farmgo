package web

// 管理 API（寫入；對應 Python app/api/admin.py）。所有寫入都會記錄在管理操作紀錄（admin_actions）；
// 調整玩家資料另外寫入玩家的交易紀錄（admin_adjust）。群組建立與成員管理由 Discord 端處理，網站不提供。
//
// 防護（與 Python router dependencies 相同，依序檢查）：
//  1. 必須帶自訂標頭 X-Farmgm-Admin，且 Origin（若有）須與 Host 相同（防 CSRF）→ 403。
//  2. 必須設定管理員帳號（否則 503）並以管理員登入（HttpOnly session cookie）→ 401。
//  3. 僅接受本機 Host（由 App.ServeHTTP 檢查，防 DNS rebinding）。
//
// 之後才驗證路徑參數與請求本文（422）。

import (
	"encoding/json"
	"net/http"
	"regexp"

	"farmgm/internal/game/balance"
)

var (
	catalogCodeRe = regexp.MustCompile(balance.CatalogCodePattern)
	itemCodeRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

var (
	qualities = []string{"normal", "good", "rare", "legendary"}
	mutations = []string{"none", "giant", "golden", "fast", "variant"}
)

// adminRoute 註冊管理 API：先檢查 CSRF 標頭，再確認登入，通過後以管理員帳號（actor）呼叫 h。
func (a *App) adminRoute(pattern string, h func(w http.ResponseWriter, r *http.Request, actor string)) {
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if !checkCSRF(w, r) {
			return
		}
		actor := a.requireSession(w, r)
		if actor == "" {
			return
		}
		h(w, r, actor)
	})
}

func (a *App) adminRoutes() {
	a.adminRoute("PUT /api/admin/crops/{crop_code}/active", a.setCropActive)
	a.adminRoute("DELETE /api/admin/events/{event_id}", a.deleteEvent)
	a.adminRoute("PUT /api/admin/players/{player_id}/inventory", a.setInventory)
	a.adminCatalogRoutes()
	a.adminPlayerRoutes()
	a.adminBossRoutes()
}

// pathCode 目錄代碼（作物、動物、產品等；Python CatalogCode）。
func (p *params) pathCode(name string) string {
	v := p.r.PathValue(name)
	if !catalogCodeRe.MatchString(v) {
		p.fail(badParam("參數 %s 必須是 2–32 個小寫英文、數字或底線，並以英文開頭。", name))
	}
	return v
}

// ---------- 作物種類 ----------

type activeBody struct {
	Active *bool `json:"active"`
}

func (a *App) setCropActive(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("crop_code")
	if !p.ok() {
		return
	}
	var body activeBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Active == nil {
		writeDetail(w, http.StatusUnprocessableEntity, "缺少 active。")
		return
	}
	v, err := a.admin.SetCropActive(ctxOf(r), actor, code, *body.Active)
	respondValue(w, r, v, err)
}

// ---------- 限時事件 ----------

func (a *App) deleteEvent(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("event_id")
	if !p.ok() {
		return
	}
	err := a.admin.DeleteEvent(ctxOf(r), actor, id)
	respondValue(w, r, map[string]int64{"deleted": id}, err)
}

// ---------- 調整玩家：背包 ----------

type inventoryBody struct {
	ItemCode *string     `json:"item_code"`
	Quality  *string     `json:"quality"`
	Mutation *string     `json:"mutation"`
	Quantity json.Number `json:"quantity"`
}

func (a *App) setInventory(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	if !p.ok() {
		return
	}
	var body inventoryBody
	if !decodeBody(w, r, &body) {
		return
	}
	v := newBodyCheck()
	code := v.str("item_code", body.ItemCode, 1, 64, itemCodeRe)
	quality := v.enum("quality", body.Quality, "normal", qualities)
	mutation := v.enum("mutation", body.Mutation, "none", mutations)
	qty := v.int("quantity", body.Quantity, true, 0, balance.AdminMaxItemQuantity) // 0 = 刪除這筆庫存
	if !v.ok(w) {
		return
	}
	res, err := a.admin.SetInventory(ctxOf(r), actor, pid, code, quality, mutation, int(qty))
	respondValue(w, r, res, err)
}
