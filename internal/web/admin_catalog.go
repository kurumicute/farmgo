package web

// 管理 API：群組任務與競賽、作物品質設定、作物／動物種類、魚種與釣具、釣魚設定、限時事件（對應 Python app/api/admin.py 前半）。
// 本文以 schema.go 驗證型別與格式（422）；數值範圍與名稱、代碼重複等業務檢查在服務層（400／404）。

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"farmgm/internal/services"
)

var (
	growCropCodeRe = regexp.MustCompile(`^[a-z_]+$`)
	timeRe         = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	endTimeRe      = regexp.MustCompile(`^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$`)
)

var (
	questTypes  = []string{"harvest", "sell", "build", "animal", "defense"}
	rewardTypes = []string{"gold", "fertilizer", "earthworm", "patrol_ticket"}
	compTypes   = []string{"harvest", "sales", "development", "defense"}
	rarities    = []string{"common", "rare", "epic", "legendary"}
	gearKinds   = []string{"rod", "line", "bait"}
)

// respondCreated 建立成功回 201（FastAPI status_code=201）。
func respondCreated[T any](w http.ResponseWriter, r *http.Request, v T, err error) {
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// pathInt 路徑中的整數（lo–hi）。
func (p *params) pathInt(name string, lo, hi int64) int64 {
	v, err := parseInt(name, p.r.PathValue(name), lo, hi)
	if err != nil {
		p.fail(err)
	}
	return v
}

// pathMatch 路徑中的字串（長度 lo–hi 個字元且符合 re）。
func (p *params) pathMatch(name string, re *regexp.Regexp, lo, hi int) string {
	v := p.r.PathValue(name)
	if !strLen(v, lo, hi) || !re.MatchString(v) {
		p.fail(badParam("參數 %s 的格式不正確。", name))
	}
	return v
}

func (a *App) adminCatalogRoutes() {
	ar := a.adminRoute
	// 群組任務與競賽
	ar("POST /api/admin/quests", a.createQuest)
	ar("POST /api/admin/quests/{quest_id}/cancel", a.cancelQuest)
	ar("POST /api/admin/competitions", a.createCompetition)
	ar("POST /api/admin/competitions/{competition_id}/cancel", a.cancelCompetition)
	ar("POST /api/admin/competitions/settle-due", func(w http.ResponseWriter, r *http.Request, actor string) {
		ids, err := a.admin.SettleDueCompetitions(ctxOf(r), actor)
		respondValue(w, r, map[string][]int64{"settled": nonNilIDs(ids)}, err)
	})
	// 作物品質、突變與生長時間
	ar("PUT /api/admin/genetics", a.updateGenetics)
	ar("PUT /api/admin/crops/{crop_code}/grow-seconds", a.updateCropGrowSeconds)
	// 作物種類（啟用／停用在 admin.go）
	ar("POST /api/admin/crops", a.createCrop)
	ar("PUT /api/admin/crops/{crop_code}", a.updateCrop)
	ar("DELETE /api/admin/crops/{crop_code}", a.deleteCrop)
	// 動物種類
	ar("POST /api/admin/animal-types", a.createAnimalType)
	ar("PUT /api/admin/animal-types/{animal_code}", a.updateAnimalType)
	ar("PUT /api/admin/animal-types/{animal_code}/active", a.setAnimalTypeActive)
	ar("DELETE /api/admin/animal-types/{animal_code}", a.deleteAnimalType)
	// 釣魚
	ar("POST /api/admin/fish-types", a.createFishType)
	ar("PUT /api/admin/fish-types/{fish_code}", a.updateFishType)
	ar("PUT /api/admin/fish-types/{fish_code}/active", a.setFishTypeActive)
	ar("POST /api/admin/fishing-gear", a.createFishingGear)
	ar("PUT /api/admin/fishing-gear/{gear_code}", a.updateFishingGear)
	ar("PUT /api/admin/fishing-gear/{gear_code}/active", a.setFishingGearActive)
	ar("PUT /api/admin/fishing-settings", a.updateFishingSettings)
	// 限時事件（刪除在 admin.go）
	ar("POST /api/admin/events", a.createEvent)
	ar("PUT /api/admin/events/{event_id}", a.updateEvent)
	ar("PUT /api/admin/events/{event_id}/enabled", a.setEventEnabled)
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// ---------- 群組任務與競賽 ----------

func (a *App) createQuest(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r,
		integer("group_id").rng(1, MaxBigint),
		str("name").length(1, 60),
		str("description").opt("").length(0, 300),
		enum("quest_type", questTypes...),
		enum("metric", "quantity", "amount").opt("quantity"),
		str("item_filter").opt(nil).null().pattern(catalogCodeRe), // 動物產品代碼（服務層確認存在）
		integer("target").rng(1, 1_000_000),
		enum("reward_mode", "fixed", "proportional"),
		enum("reward_type", rewardTypes...),
		integer("reward_amount").rng(1, 1_000_000),
		datetime("starts_at"),
		datetime("ends_at"),
	)
	if !ok {
		return
	}
	q := services.NewQuest{GroupID: int64(b["group_id"].(int)), Name: b["name"].(string), QuestType: b["quest_type"].(string),
		Target: b["target"].(int), RewardMode: b["reward_mode"].(string), RewardType: b["reward_type"].(string),
		RewardAmount: b["reward_amount"].(int), StartsAt: b["starts_at"].(time.Time), EndsAt: b["ends_at"].(time.Time),
		Description: b["description"].(string), Metric: b["metric"].(string)}
	if f, ok := b["item_filter"].(string); ok {
		q.ItemFilter = &f
	}
	v, err := a.admin.CreateQuest(ctxOf(r), actor, q)
	respondCreated(w, r, v, err)
}

func cancelReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, ok := parseBody(w, r, str("reason").length(2, 200))
	if !ok {
		return "", false
	}
	return b["reason"].(string), true
}

func (a *App) cancelQuest(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("quest_id")
	if !p.ok() {
		return
	}
	reason, ok := cancelReason(w, r)
	if !ok {
		return
	}
	v, err := a.admin.CancelQuest(ctxOf(r), actor, id, reason)
	respondValue(w, r, v, err)
}

func (a *App) createCompetition(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r,
		str("name").length(1, 60),
		str("description").opt("").length(0, 300),
		enum("comp_type", compTypes...),
		intList("rewards").length(1, 3),
		datetime("starts_at"),
		datetime("ends_at"),
	)
	if !ok {
		return
	}
	var rewards []int
	for _, x := range b["rewards"].([]any) {
		rewards = append(rewards, x.(int))
	}
	v, err := a.admin.CreateCompetition(ctxOf(r), actor, services.NewCompetition{Name: b["name"].(string),
		CompType: b["comp_type"].(string), Rewards: rewards, StartsAt: b["starts_at"].(time.Time),
		EndsAt: b["ends_at"].(time.Time), Description: b["description"].(string)})
	respondCreated(w, r, v, err)
}

func (a *App) cancelCompetition(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("competition_id")
	if !p.ok() {
		return
	}
	reason, ok := cancelReason(w, r)
	if !ok {
		return
	}
	v, err := a.admin.CancelCompetition(ctxOf(r), actor, id, reason)
	respondValue(w, r, v, err)
}

// ---------- 作物品質、突變與生長時間 ----------

func (a *App) updateGenetics(w http.ResponseWriter, r *http.Request, actor string) {
	// 範圍與總和的完整檢查在服務層（genetics.ValidateSettings）
	b, ok := parseBody(w, r, intMap("quality_bp"), intMap("quality_price_pct"), intMap("mutation_bp"),
		integer("giant_multiplier"), integer("golden_price_pct"), integer("fast_reduction_seconds"),
		integer("variant_price_pct"), integer("level_bonus_bp"), integer("level_bonus_max_bp"))
	if !ok {
		return
	}
	version, err := a.admin.UpdateGenetics(ctxOf(r), actor, b)
	respondValue(w, r, map[string]int{"version": version}, err)
}

func (a *App) updateCropGrowSeconds(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathMatch("crop_code", growCropCodeRe, 1, 32)
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, integer("grow_seconds"))
	if !ok {
		return
	}
	v, err := a.admin.UpdateCropGrowSeconds(ctxOf(r), actor, code, b["grow_seconds"].(int))
	respondValue(w, r, v, err)
}

// ---------- 作物種類 ----------

func cropFields(create bool) []field {
	fs := []field{
		str("name").length(1, 32), str("variant_name").length(1, 32),
		integer("seed_price"), integer("grow_seconds"), integer("harvest_quantity"), integer("sell_price"),
		// 季節／時段限定；沒送 = 不變更
		object("limits").opt(nil).null(),
	}
	if create {
		fs = append(fs, str("code").pattern(catalogCodeRe), str("variant_code").pattern(catalogCodeRe))
	}
	return fs
}

func (a *App) createCrop(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, cropFields(true)...)
	if !ok {
		return
	}
	v, err := a.admin.CreateCrop(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func (a *App) updateCrop(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("crop_code")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, cropFields(false)...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateCrop(ctxOf(r), actor, code, b)
	respondValue(w, r, v, err)
}

func (a *App) deleteCrop(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("crop_code")
	if !p.ok() {
		return
	}
	err := a.admin.DeleteCrop(ctxOf(r), actor, code)
	respondValue(w, r, map[string]string{"deleted": code}, err)
}

// ---------- 動物種類 ----------

func animalFields(create bool) []field {
	fs := []field{
		str("name").length(1, 32), str("product_name").length(1, 32),
		integer("price"), integer("feed_cost"), integer("cycle_seconds"), integer("product_quantity"), integer("product_price"),
	}
	if create {
		fs = append(fs, str("code").pattern(catalogCodeRe), str("product_code").pattern(catalogCodeRe))
	}
	return fs
}

func (a *App) createAnimalType(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, animalFields(true)...)
	if !ok {
		return
	}
	v, err := a.admin.CreateAnimalType(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func (a *App) updateAnimalType(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("animal_code")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, animalFields(false)...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateAnimalType(ctxOf(r), actor, code, b)
	respondValue(w, r, v, err)
}

// activeOf 路徑代碼與 {"active": bool} 本文。
func activeOf(w http.ResponseWriter, r *http.Request, name string) (string, bool, bool) {
	p := newParams(w, r)
	code := p.pathCode(name)
	if !p.ok() {
		return "", false, false
	}
	b, ok := parseBody(w, r, boolean("active"))
	if !ok {
		return "", false, false
	}
	return code, b["active"].(bool), true
}

func (a *App) setAnimalTypeActive(w http.ResponseWriter, r *http.Request, actor string) {
	code, active, ok := activeOf(w, r, "animal_code")
	if !ok {
		return
	}
	v, err := a.admin.SetAnimalTypeActive(ctxOf(r), actor, code, active)
	respondValue(w, r, v, err)
}

func (a *App) deleteAnimalType(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("animal_code")
	if !p.ok() {
		return
	}
	err := a.admin.DeleteAnimalType(ctxOf(r), actor, code)
	respondValue(w, r, map[string]string{"deleted": code}, err)
}

// ---------- 釣魚：魚種、釣具與設定 ----------

func fishFields(create bool) []field {
	fs := []field{
		str("name").length(1, 32), str("emoji").length(1, 16), enum("rarity", rarities...),
		integer("min_price"), integer("max_price"), integer("min_weight_g"), integer("max_weight_g"), integer("difficulty"),
		str("description").opt("").length(0, 200), integer("sort_order").opt(0),
		strList("required_baits").opt(nil).null().length(0, 10), // 沒送 = 不變更；[] = 任何魚餌
		object("limits").opt(nil).null(),
	}
	if create {
		fs = append(fs, str("code").pattern(catalogCodeRe))
	}
	return fs
}

func (a *App) createFishType(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, fishFields(true)...)
	if !ok {
		return
	}
	v, err := a.admin.CreateFishType(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func (a *App) updateFishType(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("fish_code")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, fishFields(false)...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateFishType(ctxOf(r), actor, code, b)
	respondValue(w, r, v, err)
}

func (a *App) setFishTypeActive(w http.ResponseWriter, r *http.Request, actor string) {
	code, active, ok := activeOf(w, r, "fish_code")
	if !ok {
		return
	}
	v, err := a.admin.SetFishTypeActive(ctxOf(r), actor, code, active)
	respondValue(w, r, v, err)
}

func gearFields(create bool) []field {
	fs := []field{
		str("name").length(1, 32), str("emoji").length(1, 16), str("description").opt("").length(0, 200),
		integer("price"), integer("required_level"), integer("stock").opt(nil).null(), // None = 不限量
		intMap("effects"), integer("sort_order").opt(0),
	}
	if create {
		fs = append(fs, str("code").pattern(catalogCodeRe), enum("kind", gearKinds...))
	}
	return fs
}

func (a *App) createFishingGear(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, gearFields(true)...)
	if !ok {
		return
	}
	v, err := a.admin.CreateFishingGear(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func (a *App) updateFishingGear(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	code := p.pathCode("gear_code")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, gearFields(false)...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateFishingGear(ctxOf(r), actor, code, b)
	respondValue(w, r, v, err)
}

func (a *App) setFishingGearActive(w http.ResponseWriter, r *http.Request, actor string) {
	code, active, ok := activeOf(w, r, "gear_code")
	if !ok {
		return
	}
	v, err := a.admin.SetFishingGearActive(ctxOf(r), actor, code, active)
	respondValue(w, r, v, err)
}

func (a *App) updateFishingSettings(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, intMap("rarity_bp"), integer("base_success_pct"), integer("bite_wait_min_seconds"),
		integer("bite_wait_max_seconds"), integer("reaction_seconds"), integer("action_timeout_seconds"),
		integer("basket_capacity"), intMapMap("difficulty"))
	if !ok {
		return
	}
	version, err := a.admin.UpdateFishingSettings(ctxOf(r), actor, b)
	respondValue(w, r, map[string]int{"version": version}, err)
}

// ---------- 限時事件 ----------

// eventData Python _event_data：start_time／end_time 轉成分鐘，weekdays（0 = 週一）轉成位元遮罩（沒有選擇時為 None）。
func eventData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	b, ok := parseBody(w, r,
		str("name").length(1, 60),
		str("description").opt("").length(0, 300),
		str("crop_code").opt(nil).null().pattern(catalogCodeRe), // 不指定 = 全部作物
		integer("sell_price_pct").opt(100),
		integer("grow_reduction_pct").opt(0),
		integer("quality_bonus_bp").opt(0),
		integer("mutation_bonus_bp").opt(0),
		integer("harvest_bonus_pct").opt(0),
		integer("free_water").opt(0),   // 1 = 免澆水
		integer("harvest_loss").opt(0), // 大風颳走：收成時每格少 N 個
		enum("schedule_type", "once", "daily", "weekly"),
		datetime("starts_at").opt(nil).null(),
		datetime("ends_at").opt(nil).null(),
		str("start_time").opt(nil).null().pattern(timeRe), // 台灣時間 HH:MM
		str("end_time").opt(nil).null().pattern(endTimeRe),
		intList("weekdays").opt([]any{}).length(0, 7).rng(0, 6),
		boolean("enabled").opt(true),
	)
	if !ok {
		return nil, false
	}
	minutes := func(v any) any {
		s, ok := v.(string)
		if !ok {
			return nil
		}
		h, m, _ := strings.Cut(s, ":")
		return atoiDigits(h)*60 + atoiDigits(m)
	}
	b["start_minute"], b["end_minute"] = minutes(b["start_time"]), minutes(b["end_time"])
	mask := 0
	for _, d := range b["weekdays"].([]any) {
		mask |= 1 << d.(int)
	}
	b["weekdays"] = nil
	if mask != 0 {
		b["weekdays"] = mask
	}
	delete(b, "start_time")
	delete(b, "end_time")
	return b, true
}

// atoiDigits 已由格式檢查保證只有數字。
func atoiDigits(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func (a *App) createEvent(w http.ResponseWriter, r *http.Request, actor string) {
	data, ok := eventData(w, r)
	if !ok {
		return
	}
	v, err := a.admin.CreateEvent(ctxOf(r), actor, data)
	respondCreated(w, r, v, err)
}

func (a *App) updateEvent(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("event_id")
	if !p.ok() {
		return
	}
	data, ok := eventData(w, r)
	if !ok {
		return
	}
	v, err := a.admin.UpdateEvent(ctxOf(r), actor, id, data)
	respondValue(w, r, v, err)
}

func (a *App) setEventEnabled(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("event_id")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, boolean("enabled"))
	if !ok {
		return
	}
	v, err := a.admin.SetEventEnabled(ctxOf(r), actor, id, b["enabled"].(bool))
	respondValue(w, r, v, err)
}
