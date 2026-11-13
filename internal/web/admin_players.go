package web

// 管理 API：新功能規則設定，以及調整單一玩家（金幣、等級、群組、土地、作物、建築、動物、溫室、寵物、稱號、簽到、
// 成就、圖鑑、每週特賣）（對應 Python app/api/admin.py 中段）。背包調整在 admin.go。
// Python 服務回傳 dict 的地方，Go 服務回傳 bool／string 等，這裡組成與 Python 相同的鍵。

import (
	"net/http"
	"regexp"
	"strings"

	"farmgm/internal/game/balance"
)

var (
	featureKeyRe = regexp.MustCompile(`^(checkin|pets|special_shop|achievements|exploration|mushroom|seedlab)$`)
	petCodeRe    = regexp.MustCompile(`^(cat|dog)$`)
	codexItemRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
)

func (a *App) adminPlayerRoutes() {
	ar := a.adminRoute
	// 新功能：規則設定
	ar("GET /api/admin/feature-settings", func(w http.ResponseWriter, r *http.Request, _ string) {
		v, err := a.admin.FeatureSettings(ctxOf(r))
		respondValue(w, r, v, err)
	})
	ar("PUT /api/admin/feature-settings/{key}", a.updateFeatureSettings)
	ar("DELETE /api/admin/feature-settings/{key}", a.resetFeatureSettings)
	// 新功能：單一玩家
	ar("GET /api/admin/players/{player_id}/progress", a.getPlayerProgress)
	ar("PUT /api/admin/players/{player_id}/greenhouse", a.setGreenhouse)
	ar("PUT /api/admin/players/{player_id}/pets/{pet_code}", a.setPet)
	ar("POST /api/admin/players/{player_id}/titles", a.grantTitle)
	ar("POST /api/admin/players/{player_id}/titles/revoke", a.revokeTitle)
	ar("PUT /api/admin/players/{player_id}/title", a.setActiveTitle)
	ar("PUT /api/admin/players/{player_id}/checkin", a.setCheckinStreak)
	ar("PUT /api/admin/players/{player_id}/achievements/{code}", a.setAchievement)
	ar("PUT /api/admin/players/{player_id}/codex/{item_code}", a.setCodex)
	ar("DELETE /api/admin/players/{player_id}/weekly-purchases", a.resetWeekly)
	// 金幣、等級、所屬群組、土地、作物、建築、動物
	ar("PUT /api/admin/players/{player_id}/farm", a.setFarmValues)
	ar("POST /api/admin/players/{player_id}/transfer-group", a.transferGroup)
	ar("POST /api/admin/players/{player_id}/plots", a.addPlots)
	ar("DELETE /api/admin/players/{player_id}/plots/{plot_number}", a.deletePlot)
	ar("PUT /api/admin/players/{player_id}/plots/{plot_number}/crop", a.plantForPlayer)
	ar("DELETE /api/admin/players/{player_id}/plots/{plot_number}/crop", a.clearPlot)
	ar("POST /api/admin/players/{player_id}/buildings", a.addBuilding)
	ar("DELETE /api/admin/players/{player_id}/buildings/{building_code}/{slot}", a.deleteBuilding)
	ar("POST /api/admin/players/{player_id}/animals", a.addAnimals)
	ar("DELETE /api/admin/players/{player_id}/animals/{animal_no}", a.deleteAnimal)
}

// playerID 路徑的 player_id；失敗時已回 422。
func playerID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p := newParams(w, r)
	id := p.pathID("player_id")
	return id, p.ok()
}

// ---------- 新功能：規則設定 ----------

func (a *App) updateFeatureSettings(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	key := p.pathMatch("key", featureKeyRe, 1, 32)
	if !p.ok() {
		return
	}
	var raw map[string]any // Python body: dict（內容由規則驗證）
	if !decodeBody(w, r, &raw) {
		return
	}
	if raw == nil {
		writeDetail(w, http.StatusUnprocessableEntity, "請求內容必須是 JSON 物件。")
		return
	}
	v, err := a.admin.UpdateFeatureSettings(ctxOf(r), actor, key, raw)
	respondValue(w, r, map[string]any{"key": key, "current": v}, err)
}

func (a *App) resetFeatureSettings(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	key := p.pathMatch("key", featureKeyRe, 1, 32)
	if !p.ok() {
		return
	}
	v, err := a.admin.ResetFeatureSettings(ctxOf(r), actor, key)
	respondValue(w, r, map[string]any{"key": key, "current": v}, err)
}

// ---------- 新功能：單一玩家 ----------

func (a *App) getPlayerProgress(w http.ResponseWriter, r *http.Request, _ string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	v, err := a.admin.AdminProgress(ctxOf(r), pid)
	respondValue(w, r, v, err)
}

// levelBody Python LevelBody：level 為 0–100 或 null（寵物：null = 移除）。
func levelBody(w http.ResponseWriter, r *http.Request) (*int, bool) {
	b, ok := parseBody(w, r, integer("level").opt(nil).null().rng(0, 100))
	if !ok {
		return nil, false
	}
	if n, ok := b["level"].(int); ok {
		return &n, true
	}
	return nil, true
}

func (a *App) setGreenhouse(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	level, ok := levelBody(w, r)
	if !ok {
		return
	}
	if level == nil {
		writeDetail(w, http.StatusUnprocessableEntity, "請填寫溫室等級。")
		return
	}
	changed, err := a.admin.SetGreenhouseLevel(ctxOf(r), actor, pid, *level)
	respondValue(w, r, map[string]any{"level": *level, "changed": changed}, err)
}

func (a *App) setPet(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	pet := p.pathMatch("pet_code", petCodeRe, 3, 3)
	if !p.ok() {
		return
	}
	level, ok := levelBody(w, r)
	if !ok {
		return
	}
	changed, err := a.admin.SetPetLevel(ctxOf(r), actor, pid, pet, level)
	respondValue(w, r, map[string]any{"level": level, "changed": changed}, err)
}

// titleBody Python TitleBody：title 最多 40 字或 null。null 以 nil 回傳。
func titleBody(w http.ResponseWriter, r *http.Request) (*string, bool) {
	b, ok := parseBody(w, r, str("title").opt(nil).null().length(0, 40))
	if !ok {
		return nil, false
	}
	if s, ok := b["title"].(string); ok {
		return &s, true
	}
	return nil, true
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *App) grantTitle(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	title, ok := titleBody(w, r)
	if !ok {
		return
	}
	got, err := a.admin.GrantTitle(ctxOf(r), actor, pid, orEmpty(title)) // null 與空白一樣是「稱號不可空白」
	respondCreated(w, r, map[string]string{"title": got}, err)
}

func (a *App) revokeTitle(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	title, ok := titleBody(w, r)
	if !ok {
		return
	}
	t := orEmpty(title) // Python body.title or ""
	err := a.admin.RevokeTitle(ctxOf(r), actor, pid, t)
	respondValue(w, r, map[string]string{"title": t}, err)
}

func (a *App) setActiveTitle(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	title, ok := titleBody(w, r)
	if !ok {
		return
	}
	t := strings.TrimSpace(orEmpty(title))
	changed, err := a.admin.SetActiveTitle(ctxOf(r), actor, pid, t)
	var out *string // Python (title or "").strip() or None
	if t != "" {
		out = &t
	}
	respondValue(w, r, map[string]any{"title": out, "changed": changed}, err)
}

func (a *App) setCheckinStreak(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, integer("streak").rng(1, 10_000))
	if !ok {
		return
	}
	streak := b["streak"].(int)
	err := a.admin.SetCheckinStreak(ctxOf(r), actor, pid, streak)
	respondValue(w, r, map[string]int{"streak": streak}, err)
}

func (a *App) setAchievement(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	code := p.pathCode("code")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, boolean("claimed"))
	if !ok {
		return
	}
	claimed := b["claimed"].(bool)
	changed, err := a.admin.SetAchievementClaimed(ctxOf(r), actor, pid, code, claimed)
	respondValue(w, r, map[string]any{"code": code, "claimed": claimed, "changed": changed}, err)
}

func (a *App) setCodex(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	code := p.pathMatch("item_code", codexItemRe, 2, 64)
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, integer("quantity").rng(0, 1_000_000_000_000), integer("best_quality").rng(0, 3))
	if !ok {
		return
	}
	changed, err := a.admin.SetCodexEntry(ctxOf(r), actor, pid, code, b["quantity"].(int), b["best_quality"].(int))
	respondValue(w, r, map[string]any{"item_code": code, "changed": changed}, err)
}

func (a *App) resetWeekly(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	n, err := a.admin.ResetWeeklyPurchases(ctxOf(r), actor, pid)
	respondValue(w, r, map[string]int{"cleared": n}, err)
}

// ---------- 金幣、等級、所屬群組 ----------

func optIntOf(v any) *int {
	if n, ok := v.(int); ok {
		return &n
	}
	return nil
}

func (a *App) setFarmValues(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, integer("gold").opt(nil).null().rng(0, balance.AdminMaxGold),
		integer("level").opt(nil).null().rng(1, balance.AdminMaxLevel))
	if !ok {
		return
	}
	v, err := a.admin.SetFarmValues(ctxOf(r), actor, pid, optIntOf(b["gold"]), optIntOf(b["level"]))
	respondValue(w, r, v, err)
}

func (a *App) transferGroup(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, integer("group_id").rng(1, MaxBigint),
		str("reason").length(1, balance.GroupTransferReasonMaxLen), boolean("skip_cooldown").opt(false))
	if !ok {
		return
	}
	v, err := a.admin.AdminTransferFarm(ctxOf(r), actor, pid, int64(b["group_id"].(int)), b["reason"].(string),
		b["skip_cooldown"].(bool))
	respondValue(w, r, v, err)
}

// ---------- 土地與作物 ----------

func (a *App) addPlots(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, integer("count").rng(1, balance.AdminMaxPlots))
	if !ok {
		return
	}
	v, err := a.admin.AddPlots(ctxOf(r), actor, pid, b["count"].(int))
	respondCreated(w, r, v, err)
}

// plotPath 路徑的 player_id 與 plot_number（1–10000）。
func plotPath(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	plot := p.pathInt("plot_number", 1, 10_000)
	return pid, int(plot), p.ok()
}

func (a *App) deletePlot(w http.ResponseWriter, r *http.Request, actor string) {
	pid, plot, ok := plotPath(w, r)
	if !ok {
		return
	}
	err := a.admin.DeletePlot(ctxOf(r), actor, pid, plot)
	respondValue(w, r, map[string]int{"deleted": plot}, err)
}

func (a *App) plantForPlayer(w http.ResponseWriter, r *http.Request, actor string) {
	pid, plot, ok := plotPath(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, str("crop_code").pattern(catalogCodeRe), enum("quality", qualities...).opt("normal"),
		enum("mutation", mutations...).opt("none"), boolean("watered").opt(false), boolean("ready_now").opt(false))
	if !ok {
		return
	}
	v, err := a.admin.PlantForPlayer(ctxOf(r), actor, pid, plot, b["crop_code"].(string), b["quality"].(string),
		b["mutation"].(string), b["watered"].(bool), b["ready_now"].(bool))
	respondValue(w, r, v, err)
}

func (a *App) clearPlot(w http.ResponseWriter, r *http.Request, actor string) {
	pid, plot, ok := plotPath(w, r)
	if !ok {
		return
	}
	err := a.admin.ClearPlotForPlayer(ctxOf(r), actor, pid, plot)
	respondValue(w, r, map[string]int{"cleared": plot}, err)
}

// ---------- 建築與動物 ----------

func (a *App) addBuilding(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, str("building_code").pattern(catalogCodeRe))
	if !ok {
		return
	}
	v, err := a.admin.AddBuilding(ctxOf(r), actor, pid, b["building_code"].(string))
	respondCreated(w, r, v, err)
}

func (a *App) deleteBuilding(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	code := p.pathCode("building_code")
	slot := int(p.pathInt("slot", 1, 100))
	if !p.ok() {
		return
	}
	err := a.admin.DeleteBuilding(ctxOf(r), actor, pid, code, slot)
	respondValue(w, r, map[string]any{"deleted": map[string]any{"building": code, "slot": slot}}, err)
}

func (a *App) addAnimals(w http.ResponseWriter, r *http.Request, actor string) {
	pid, ok := playerID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, str("animal_code").pattern(catalogCodeRe), integer("quantity").rng(1, balance.AnimalMaxBuy))
	if !ok {
		return
	}
	numbers, err := a.admin.AddAnimalsForPlayer(ctxOf(r), actor, pid, b["animal_code"].(string), b["quantity"].(int))
	if numbers == nil {
		numbers = []int{}
	}
	respondCreated(w, r, map[string][]int{"added": numbers}, err)
}

func (a *App) deleteAnimal(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	pid := p.pathID("player_id")
	no := int(p.pathInt("animal_no", 1, 1_000_000))
	if !p.ok() {
		return
	}
	err := a.admin.DeleteAnimalForPlayer(ctxOf(r), actor, pid, no)
	respondValue(w, r, map[string]int{"deleted": no}, err)
}
