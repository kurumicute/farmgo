package web

// 管理 API：排名獎勵規則、世界 Boss 活動與排程（對應 Python app/api/admin.py 後半）。
// 本文型別以 schema.go 檢查（422）；詳細範圍、物品是否存在、時間先後等由服務層與 game/worldboss 檢查（400／404）。

import (
	"net/http"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/services"
)

var (
	supplies    = []string{"food", "medical", "combat", "equipment"}
	bossActions = []string{"start", "pause", "resume", "end"}
)

func (a *App) adminBossRoutes() {
	ar := a.adminRoute
	// 排名獎勵規則
	ar("POST /api/admin/ranking-rules", a.createRankingRule)
	ar("POST /api/admin/ranking-rules/settle-due", func(w http.ResponseWriter, r *http.Request, actor string) {
		ids, err := a.admin.SettleDueRankings(ctxOf(r), actor)
		respondValue(w, r, map[string][]int64{"settled": nonNilIDs(ids)}, err)
	})
	ar("PUT /api/admin/ranking-rules/{rule_id}", a.updateRankingRule)
	ar("PUT /api/admin/ranking-rules/{rule_id}/enabled", a.setRankingRuleEnabled)
	ar("DELETE /api/admin/ranking-rules/{rule_id}", a.deleteRankingRule)
	// 世界 Boss
	ar("POST /api/admin/world-boss", a.createWorldBoss)
	ar("PUT /api/admin/world-boss/{event_id}", a.updateWorldBoss)
	ar("PUT /api/admin/world-boss/{event_id}/settings", a.updateWorldBossSettings)
	ar("PUT /api/admin/world-boss/{event_id}/rewards", a.updateWorldBossRewards)
	ar("POST /api/admin/world-boss/{event_id}/{action}", a.worldBossAction)
	// 世界 Boss 排程
	ar("POST /api/admin/world-boss-schedules", a.createWorldBossSchedule)
	ar("PUT /api/admin/world-boss-schedules/{schedule_id}", a.updateWorldBossSchedule)
	ar("PUT /api/admin/world-boss-schedules/{schedule_id}/enabled", a.setWorldBossScheduleEnabled)
	ar("DELETE /api/admin/world-boss-schedules/{schedule_id}", a.deleteWorldBossSchedule)
}

// ---------- 排名獎勵規則 ----------

func rankingRuleFields() []field {
	return []field{
		str("name").length(1, 60),
		enum("scope", "group", "individual"),
		enum("metric", "money", "sales", "goods"),
		enum("period", "daily", "weekly", "monthly"),
		// amount 的範圍由服務層依獎勵類型檢查
		listOf("rewards", enum("reward_type", rewardTypes...), integer("amount")).length(1, balance.RankingMaxRanks),
		boolean("enabled").opt(true),
	}
}

func (a *App) createRankingRule(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, rankingRuleFields()...)
	if !ok {
		return
	}
	v, err := a.admin.CreateRankingRule(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func (a *App) updateRankingRule(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("rule_id")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, rankingRuleFields()...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateRankingRule(ctxOf(r), actor, id, b)
	respondValue(w, r, v, err)
}

func (a *App) setRankingRuleEnabled(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("rule_id")
	if !p.ok() {
		return
	}
	b, ok := parseBody(w, r, boolean("enabled"))
	if !ok {
		return
	}
	v, err := a.admin.SetRankingRuleEnabled(ctxOf(r), actor, id, b["enabled"].(bool))
	respondValue(w, r, v, err)
}

func (a *App) deleteRankingRule(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("rule_id")
	if !p.ok() {
		return
	}
	deleted, err := a.admin.DeleteRankingRule(ctxOf(r), actor, id)
	respondValue(w, r, map[string]int64{"deleted": deleted}, err)
}

// ---------- 世界 Boss：本文 model ----------

// bossInt Python BossInt（0–1,000,000,000；詳細範圍由 game/worldboss 檢查）。
func bossInt(name string) field { return integer(name).rng(0, 1_000_000_000) }

// bossItemCode Python ItemCode（dict 的鍵）。
var bossItemCode = str("鍵").length(2, 64).pattern(catalogCodeRe)

// bossSettingsFields Python BossSettingsBody。
func bossSettingsFields() []field {
	return []field{
		bossInt("base_damage_per_hour"), bossInt("instant_damage_per_point"), bossInt("gold_per_point"),
		enum("crop_supply", supplies...), enum("fish_supply", supplies...),
		model("supply_targets", bossInt("food"), bossInt("medical"), bossInt("combat"), bossInt("equipment")),
		model("effects", bossInt("ratio_cap_pct"), bossInt("base_uptime_pct"), bossInt("medical_recovery_pct"),
			bossInt("combat_attack_pct"), bossInt("equipment_attack_pct")),
		model("limits", bossInt("per_item_max"), bossInt("per_action_max")),
		mapOf("items", enum("supply", supplies...), bossInt("points")).keys(bossItemCode).length(0, 60).opt(map[string]any{}),
	}
}

// bossRewardPart Python BossRewardPart（gold 預設 0、items 預設 {}）。
func bossRewardPart() []field {
	return []field{bossInt("gold").opt(0),
		intMap("items").keys(bossItemCode).length(0, 5).rng(0, 1_000_000_000).opt(map[string]any{})}
}

// bossRewardsFields Python BossRewardsBody。
func bossRewardsFields() []field {
	return []field{
		bossInt("min_points"), // 參與門檻：積分未達此值沒有任何獎勵
		model("victory", bossRewardPart()...), model("participation", bossRewardPart()...),
		bossInt("bonus_points"), model("bonus", bossRewardPart()...),
		listOf("rank", bossRewardPart()...).length(0, 10).opt([]any{}),
		str("title").opt("").length(0, 20), bossInt("title_top").opt(0),
	}
}

// bossBasicFields Python BossBasic（create 時另有 group_id、settings、rewards）。
func bossBasicFields(create bool) []field {
	fs := []field{
		str("name").length(1, 40),
		integer("level").opt(nil).null().rng(1, 999), // 介面不顯示等級，保留相容
		integer("max_hp").rng(1, 1_000_000_000),
		datetime("starts_at"),
		datetime("ends_at"),
	}
	if create {
		fs = append(fs, integer("group_id").opt(nil).null().rng(1, MaxBigint), // None = 全服（所有玩家）
			model("settings", bossSettingsFields()...).opt(nil).null(),
			model("rewards", bossRewardsFields()...).opt(nil).null())
	}
	return fs
}

func bossInput(b map[string]any) services.BossInput {
	starts, ends := b["starts_at"].(time.Time), b["ends_at"].(time.Time)
	in := services.BossInput{Name: b["name"].(string), Level: b["level"], MaxHP: b["max_hp"], StartsAt: &starts, EndsAt: &ends}
	if g, ok := b["group_id"].(int); ok {
		id := int64(g)
		in.GroupID = &id
	}
	in.Settings, _ = b["settings"].(map[string]any)
	in.Rewards, _ = b["rewards"].(map[string]any)
	return in
}

// ---------- 世界 Boss：活動 ----------

func (a *App) createWorldBoss(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, bossBasicFields(true)...)
	if !ok {
		return
	}
	v, err := a.admin.CreateWorldBoss(ctxOf(r), actor, bossInput(b))
	respondCreated(w, r, v, err)
}

func bossEventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p := newParams(w, r)
	id := p.pathID("event_id")
	return id, p.ok()
}

func (a *App) updateWorldBoss(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := bossEventID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, bossBasicFields(false)...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateWorldBoss(ctxOf(r), actor, id, bossInput(b))
	respondValue(w, r, v, err)
}

func (a *App) updateWorldBossSettings(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := bossEventID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, bossSettingsFields()...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateWorldBossSettings(ctxOf(r), actor, id, b)
	respondValue(w, r, v, err)
}

func (a *App) updateWorldBossRewards(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := bossEventID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, bossRewardsFields()...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateWorldBossRewards(ctxOf(r), actor, id, b)
	respondValue(w, r, v, err)
}

func (a *App) worldBossAction(w http.ResponseWriter, r *http.Request, actor string) {
	p := newParams(w, r)
	id := p.pathID("event_id")
	action := r.PathValue("action")
	if !slicesContains(bossActions, action) {
		p.fail(badParam("參數 action 只能是 start、pause、resume 或 end。"))
	}
	if !p.ok() {
		return
	}
	v, err := a.admin.WorldBossAction(ctxOf(r), actor, id, action)
	respondValue(w, r, v, err)
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------- 世界 Boss：排程 ----------

// bossScheduleFields Python BossScheduleBody。
func bossScheduleFields() []field {
	return []field{
		str("name").length(1, 40),
		integer("group_id").opt(nil).null().rng(1, MaxBigint), // None = 全服
		integer("interval_days").rng(1, 30),
		integer("spawn_hour").rng(0, 23),
		integer("spawn_minute").rng(0, 59),
		date("anchor_date"),
		integer("duration_minutes").rng(10, 10080),
		integer("max_hp").rng(1, 1_000_000_000),
		model("settings", bossSettingsFields()...).opt(nil).null(),
		model("rewards", bossRewardsFields()...).opt(nil).null(),
		boolean("enabled").opt(true),
	}
}

func (a *App) createWorldBossSchedule(w http.ResponseWriter, r *http.Request, actor string) {
	b, ok := parseBody(w, r, bossScheduleFields()...)
	if !ok {
		return
	}
	v, err := a.admin.CreateWorldBossSchedule(ctxOf(r), actor, b)
	respondCreated(w, r, v, err)
}

func scheduleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	p := newParams(w, r)
	id := p.pathID("schedule_id")
	return id, p.ok()
}

func (a *App) updateWorldBossSchedule(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, bossScheduleFields()...)
	if !ok {
		return
	}
	v, err := a.admin.UpdateWorldBossSchedule(ctxOf(r), actor, id, b)
	respondValue(w, r, v, err)
}

func (a *App) setWorldBossScheduleEnabled(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	b, ok := parseBody(w, r, boolean("enabled"))
	if !ok {
		return
	}
	v, err := a.admin.SetWorldBossScheduleEnabled(ctxOf(r), actor, id, b["enabled"].(bool))
	respondValue(w, r, v, err)
}

func (a *App) deleteWorldBossSchedule(w http.ResponseWriter, r *http.Request, actor string) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	deleted, err := a.admin.DeleteWorldBossSchedule(ctxOf(r), actor, id)
	respondValue(w, r, map[string]int64{"deleted": deleted}, err)
}
