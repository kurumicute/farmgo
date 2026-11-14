package services

// 介紹網站的唯讀查詢（對應 Python app/site/server.py 的 StatsCache.get 與 build_guide）。
//
// 只輸出彙總數字與遊戲規則，不含任何玩家名稱、ID、農場、群組或伺服器名稱。查詢都在唯讀交易中執行；
// 快取由網站層（internal/site）負責。JSON 鍵名與 Python dict 相同；前端以 Object.entries 顯示的物件
// （守護者傷害、季節月份）用 OrderedMap 保留 Python 的順序。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	eventrules "farmgm/internal/game/events"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/news"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/ranking"
	"farmgm/internal/game/ruins"
	"farmgm/internal/game/seasons"
	"farmgm/internal/game/seedlab"
	"farmgm/internal/game/specialshop"
	wb "farmgm/internal/game/worldboss"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/events"
	"farmgm/internal/repositories/featuresettings"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/genetics"
)

// SiteService 介紹網站的唯讀查詢。
type SiteService struct {
	db    *db.DB
	clock clock.Clock
}

// NewSiteService clk 為 nil 時使用系統時鐘（Python GuideCache 用 datetime.now(UTC)）。
func NewSiteService(d *db.DB, clk clock.Clock) *SiteService {
	if clk == nil {
		clk = clock.System{}
	}
	return &SiteService{db: d, clock: clk}
}

func (s *SiteService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// SiteStats 首頁的彙總統計（不含任何個別資料）。
type SiteStats struct {
	Players      int        `json:"players"`
	Farms        int        `json:"farms"`
	Servers      int        `json:"servers"`
	Growing      int        `json:"growing"`
	ActionsToday int        `json:"actions_today"`
	LastActivity *time.Time `json:"last_activity"`
}

// Stats 「今天」依資料庫的實際日期（台灣時間）計算，與 Python 相同。
func (s *SiteService) Stats(ctx context.Context) (SiteStats, error) {
	var out SiteStats
	err := s.db.InTx(ctx, db.TxOptions{ReadOnly: true}, func(t *db.Tx) error {
		return t.QueryRow(ctx, `
			WITH since AS (SELECT date_trunc('day', now() AT TIME ZONE 'Asia/Taipei') AT TIME ZONE 'Asia/Taipei' AS at)
			SELECT (SELECT count(*) FROM players),
				(SELECT count(*) FROM farms),
				(SELECT count(*) FROM groups WHERE discord_guild_id IS NOT NULL),
				(SELECT count(*) FROM farm_plots WHERE crop_type_id IS NOT NULL),
				(SELECT count(*) FROM game_transactions WHERE created_at >= (SELECT at FROM since)),
				(SELECT max(created_at) FROM game_transactions)`).
			Scan(&out.Players, &out.Farms, &out.Servers, &out.Growing, &out.ActionsToday, &out.LastActivity)
	})
	return out, err
}

// Guide 操作手冊需要的遊戲設定（Python build_guide）。只輸出規則與數值，不含任何玩家、農場或群組資料。
func (s *SiteService) Guide(ctx context.Context) (*OrderedMap, error) {
	var out *OrderedMap
	err := s.db.InTx(ctx, db.TxOptions{ReadOnly: true}, func(t *db.Tx) error {
		g, err := buildGuide(ctx, t, s.now(), nil)
		out = g
		return err
	})
	return out, err
}

// ErrUnknownGuidePart 沒有這個手冊部分（GuideParts 沒有的名稱）。
var ErrUnknownGuidePart = fmt.Errorf("沒有這個手冊部分")

// GuidePart 操作手冊的其中一部分（GuideParts），只查詢這部分需要的資料。
func (s *SiteService) GuidePart(ctx context.Context, part string) (*OrderedMap, error) {
	keys, ok := GuideParts[part]
	if !ok {
		return nil, ErrUnknownGuidePart
	}
	var out *OrderedMap
	err := s.db.InTx(ctx, db.TxOptions{ReadOnly: true}, func(t *db.Tx) error {
		g, err := buildGuide(ctx, t, s.now(), keys)
		out = g
		return err
	})
	return out, err
}

func bpPct(bp int) float64 { return numfmt.RoundN(float64(bp)/100, 2) }

// siteDuration Python _duration：「X 小時 Y 分鐘」，不到 1 分鐘時為「N 秒」。
func siteDuration(seconds int) string {
	hours, minutes := seconds/60/60, seconds/60%60
	var parts []string
	if hours != 0 {
		parts = append(parts, fmt.Sprintf("%d 小時", hours))
	}
	if minutes != 0 {
		parts = append(parts, fmt.Sprintf("%d 分鐘", minutes))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	return strings.Join(parts, " ")
}

type siteNameQty struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type nameLevel struct {
	Name     string `json:"name"`
	MinLevel int    `json:"min_level"`
}

type nameChance struct {
	Name      string  `json:"name"`
	ChancePct float64 `json:"chance_pct"`
}

// guideEnv 建立手冊資料時共用的查詢結果（只在需要時才查，同一次請求內只查一次）。
type guideEnv struct {
	ctx context.Context
	t   *db.Tx
	now time.Time

	cropsLoaded bool
	allCrops    []models.CropType
	activeCrops []models.CropType // 啟用中、而且不是雜交作物
	names       map[string]string // 作物與加工品代碼 → 名稱

	itemNames map[string]string

	geneticsLoaded  bool
	geneticsVersion int
	gs              balance.GeneticsSettings
}

func (e *guideEnv) loadCrops() error {
	if e.cropsLoaded {
		return nil
	}
	all, err := crops.ListAll(e.ctx, e.t, false)
	if err != nil {
		return err
	}
	e.allCrops, e.names = all, map[string]string{}
	for _, c := range all {
		e.names[c.Code] = c.Name
		// 雜交作物不在介紹網站列出（只介紹「可以雜交出新作物」，不公開組合與品種），也不放進特賣
		if c.Active && !seedlab.IsHybrid(c.Code) {
			e.activeCrops = append(e.activeCrops, c)
		}
	}
	for code, g := range balance.Goods {
		e.names[code] = g.Name
	}
	e.cropsLoaded = true
	return nil
}

func (e *guideEnv) loadItemNames() error {
	if e.itemNames != nil {
		return nil
	}
	n, err := catalog.ItemNames(e.ctx, e.t)
	e.itemNames = n
	return err
}

func (e *guideEnv) loadGenetics() error {
	if e.geneticsLoaded {
		return nil
	}
	v, gs, err := genetics.Current(e.ctx, e.t)
	if err != nil {
		return err
	}
	e.geneticsVersion, e.gs, e.geneticsLoaded = v, gs, true
	return nil
}

// rareSeedCrops 商店不賣種子的稀有作物（不含雜交作物）。
func (e *guideEnv) rareSeedCrops() []string {
	out := []string{}
	for _, c := range e.activeCrops {
		if !c.ShopBuyable {
			out = append(out, c.Name)
		}
	}
	return out
}

// guideKey 手冊的一個頂層欄位與它的建立函式。
type guideKey struct {
	key   string
	needs []func(*guideEnv) error
	build func(e *guideEnv) (any, error)
}

func needCrops(e *guideEnv) error    { return e.loadCrops() }
func needItems(e *guideEnv) error    { return e.loadItemNames() }
func needGenetics(e *guideEnv) error { return e.loadGenetics() }
func guideOK(v any) (any, error)     { return v, nil }
func guideStart() map[string]any {
	return map[string]any{
		"gold": balance.InitialGold, "plots": balance.InitialPlots, "warehouse": balance.InitialWarehouseCapacity,
		"rename_cost": balance.RenameFarmCost, "upgrade_prices": balance.UpgradePrices,
		"upgrade_plots": balance.UpgradePlotsAdded, "max_plots": balance.MaxPlots,
		"wither_minutes": balance.WitherSeconds / 60, "steal_max": balance.StealMaxPerAction,
		"steal_plots_per_day": balance.StealPlotsPerVictimPerDay, "steal_thieves_per_day": balance.StealThievesPerVictimPerDay,
		"transfer_cooldown_days": balance.GroupTransferCooldownSeconds / 86400,
		"max_block_pct":          numfmt.Round(balance.DefenseMaxBlockChance * 100), "patrol_days": balance.PatrolDays,
		"barn_slots": balance.BarnSlots, "craft_max": balance.CraftMaxQuantity,
		"warehouse_step": balance.WarehouseExpansionStep, "warehouse_prices": balance.WarehouseExpansionPrices,
		"warehouse_max": balance.WarehouseMaxCapacity,
		// 所有種子和探險素材一樣放素材背包（Go 版新增）
		"material_bag_capacity": exploration.BagCapacity(),
	}
}

// guideKeys 手冊所有頂層欄位（依 Python build_guide 的順序；Go 版新增的欄位放在後面）。
var guideKeys = []guideKey{
	{"genetics_version", []func(*guideEnv) error{needGenetics}, func(e *guideEnv) (any, error) { return guideOK(e.geneticsVersion) }},
	{"start", nil, func(e *guideEnv) (any, error) { return guideOK(guideStart()) }},
	{"crops", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) {
		rows := []map[string]any{}
		for _, c := range e.activeCrops {
			rows = append(rows, map[string]any{
				"name": c.Name, "seed_price": c.SeedPrice, "grow_seconds": c.GrowSeconds, "harvest_quantity": c.HarvestQuantity,
				"sell_price": c.SellPrice, "variant_name": c.VariantName, "shop_buyable": c.ShopBuyable,
				"season_text": seasons.Describe(c.Seasons, c.HourStart, c.HourEnd),
				"in_season":   seasons.Available(c.Seasons, c.HourStart, c.HourEnd, e.now),
			})
		}
		return rows, nil
	}},
	{"exploration", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) { return guideOK(buildExploration(e.names)) }},
	{"ruins", []func(*guideEnv) error{needItems}, func(e *guideEnv) (any, error) { return guideOK(buildRuins(e.itemNames)) }},
	{"news", nil, func(e *guideEnv) (any, error) { return guideOK(buildNews()) }},
	{"seasons", nil, func(e *guideEnv) (any, error) { return guideOK(buildSeasons(e.now)) }},
	{"fishing", nil, func(e *guideEnv) (any, error) { return buildFishing(e.ctx, e.t) }},
	{"world_boss", []func(*guideEnv) error{needItems}, func(e *guideEnv) (any, error) {
		return buildWorldBoss(e.ctx, e.t, e.itemNames, e.now)
	}},
	{"progress", nil, func(e *guideEnv) (any, error) { return guideOK(buildProgress()) }},
	{"special_shop", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) {
		return guideOK(buildSpecialShop(e.activeCrops, e.now))
	}},
	{"pets", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) { return guideOK(buildPets(e.activeCrops)) }},
	{"mushroom", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) { return guideOK(buildMushroom(e.names)) }},
	{"seedlab", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) { return guideOK(buildSeedLab(e.rareSeedCrops())) }},
	{"qualities", []func(*guideEnv) error{needGenetics}, func(e *guideEnv) (any, error) {
		qualities := []map[string]any{}
		for _, code := range balance.QualityOrder {
			qualities = append(qualities, map[string]any{"name": balance.Qualities[code],
				"chance_pct": bpPct(e.gs.QualityBP[code]), "price_mul": float64(e.gs.QualityPricePct[code]) / 100})
		}
		return qualities, nil
	}},
	{"level_bonus_pct", []func(*guideEnv) error{needGenetics}, func(e *guideEnv) (any, error) { return guideOK(bpPct(e.gs.LevelBonusBP)) }},
	{"level_bonus_max_pct", []func(*guideEnv) error{needGenetics}, func(e *guideEnv) (any, error) {
		return guideOK(bpPct(e.gs.LevelBonusMaxBP))
	}},
	{"mutations", []func(*guideEnv) error{needGenetics}, func(e *guideEnv) (any, error) {
		gs := e.gs
		effects := map[string]string{
			"giant":  fmt.Sprintf("收成數量 ×%d", gs.GiantMultiplier),
			"golden": fmt.Sprintf("售價 ×%s", pyG(float64(gs.GoldenPricePct)/100)),
			"fast": fmt.Sprintf("生長時間減少 %s（最短 %s，仍需澆水）", siteDuration(gs.FastReductionSeconds),
				siteDuration(balance.MinGrowSeconds)),
			"variant": fmt.Sprintf("長成獨立的稀有作物，售價 ×%s", pyG(float64(gs.VariantPricePct)/100)),
		}
		mutations := []map[string]any{}
		for _, code := range []string{"giant", "golden", "fast", "variant"} {
			mutations = append(mutations, map[string]any{"name": balance.Mutations[code],
				"chance_pct": bpPct(gs.MutationBP[code]), "effect": effects[code]})
		}
		return mutations, nil
	}},
	{"items", nil, func(e *guideEnv) (any, error) {
		items := []map[string]any{}
		for _, code := range balance.ItemOrder {
			it := balance.Items[code]
			items = append(items, map[string]any{"name": it.Name, "price": it.Price, "kind": it.Kind, "description": it.Desc})
		}
		return items, nil
	}},
	{"defenses", nil, func(e *guideEnv) (any, error) {
		defenses := []map[string]any{}
		for _, code := range balance.DefenseOrder {
			d := balance.Defenses[code]
			defenses = append(defenses, map[string]any{"name": d.Name, "price": d.Price, "max": d.MaxPerFarm,
				"chance_pct": numfmt.Round(d.BlockChance * 100), "range": d.Range})
		}
		return defenses, nil
	}},
	{"buildings", nil, func(e *guideEnv) (any, error) {
		buildings := []map[string]any{}
		for _, code := range balance.BuildingOrder {
			b := balance.Buildings[code]
			buildings = append(buildings, map[string]any{"name": b.Name, "price": b.Price, "max": b.MaxPerFarm})
		}
		return buildings, nil
	}},
	{"animals", nil, func(e *guideEnv) (any, error) {
		allAnimals, err := animals.ListAll(e.ctx, e.t, false)
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for _, a := range allAnimals {
			if !a.Active {
				continue
			}
			rows = append(rows, map[string]any{"name": a.Name, "price": a.Price, "feed_cost": a.FeedCost,
				"cycle_seconds": a.CycleSeconds, "product_name": a.ProductName, "product_quantity": a.ProductQuantity,
				"product_price": a.ProductPrice})
		}
		return rows, nil
	}},
	{"recipes", []func(*guideEnv) error{needCrops}, func(e *guideEnv) (any, error) {
		recipes := []map[string]any{}
		for _, code := range balance.RecipeOrder {
			rc := balance.Recipes[code]
			inputs := []siteNameQty{}
			for _, item := range rc.InputOrder {
				inputs = append(inputs, siteNameQty{nameOr(e.names, item), rc.Inputs[item]})
			}
			recipes = append(recipes, map[string]any{"name": rc.Name, "building": balance.Buildings[rc.Building].Name,
				"building_price": balance.Buildings[rc.Building].Price, "inputs": inputs, "output_quantity": rc.OutputQty,
				"seconds": rc.Seconds, "sell_price": balance.Goods[rc.Output].SellPrice})
		}
		return recipes, nil
	}},
	{"gamble", nil, func(e *guideEnv) (any, error) {
		gambleQ, gambleM := []string{}, []string{}
		for _, q := range balance.GambleQualities {
			gambleQ = append(gambleQ, balance.Qualities[q])
		}
		for _, m := range balance.GambleMutations {
			gambleM = append(gambleM, balance.Mutations[m])
		}
		return map[string]any{"min": balance.GambleMin, "max": balance.GambleMax, "big_from": balance.GambleBigFrom,
			"hilo_multiplier": balance.GambleHiloMultiplier, "exact_multiplier": balance.GambleExactMultiplier,
			"qualities": gambleQ, "mutations": gambleM}, nil
	}},
	{"events", nil, func(e *guideEnv) (any, error) {
		// 只列啟用中的活動（名稱、效果與時段本來就會在 Discord 面板公開顯示）
		evs, err := events.ListAll(e.ctx, e.t)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(evs, func(a, b models.GameEvent) int { return int(a.ID - b.ID) })
		rows := []map[string]any{}
		for i := range evs {
			if !evs[i].Enabled {
				continue
			}
			r := events.ToRule(&evs[i])
			rows = append(rows, map[string]any{"name": r.Name, "description": r.Description,
				"effects_text": eventrules.EffectsText(r), "schedule_text": eventrules.ScheduleText(r),
				"active_now": eventrules.IsActive(r, e.now)})
		}
		return rows, nil
	}},
	{"rankings", nil, func(e *guideEnv) (any, error) {
		// 只列啟用中的排名獎勵規則（規則本身會在 Discord「🎁 排名獎勵」公開顯示，不含任何得獎紀錄）
		rules, err := db.QueryStructs[models.RankingRewardRule](e.ctx, e.t, "SELECT * FROM ranking_reward_rules WHERE enabled ORDER BY id")
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for _, r := range rules {
			texts := []string{}
			for _, x := range r.Rewards {
				texts = append(texts, ranking.RewardText(ranking.Reward{RewardType: x.RewardType, Amount: x.Amount}))
			}
			rows = append(rows, map[string]any{"name": r.Name, "scope": r.Scope, "metric": ranking.Metrics[r.Metric],
				"period": ranking.Periods[r.Period], "rewards": texts})
		}
		return map[string]any{"settle_grace_seconds": balance.RankingSettleGraceSeconds, "rules": rows}, nil
	}},
	// ---------- Go 版新增 ----------
	{"craft", nil, func(e *guideEnv) (any, error) { return guideOK(buildCraft()) }},
	{"groups", nil, func(e *guideEnv) (any, error) { return guideOK(buildGroups()) }},
	{"cards", []func(*guideEnv) error{needCrops}, buildCards},
}

// GuideParts 介紹網站分段讀取（2026-11-14）：首頁只讀 summary（功能卡片），操作手冊的每個欄位打開時才讀對應的部分。
// 值是這個部分包含的頂層欄位（與完整 /api/guide 相同的鍵與格式）。
var GuideParts = map[string][]string{
	"summary":      {"cards"},
	"farm":         {"start", "crops", "seasons"},
	"genetics":     {"genetics_version", "qualities", "level_bonus_pct", "level_bonus_max_pct", "mutations"},
	"shop":         {"start", "items"},
	"ranch":        {"start", "buildings", "animals"},
	"craft":        {"start", "recipes", "craft"},
	"fishing":      {"fishing"},
	"exploration":  {"exploration"},
	"mushroom":     {"mushroom"},
	"seedlab":      {"seedlab"},
	"ruins":        {"ruins"},
	"news":         {"news"},
	"steal":        {"start", "defenses"},
	"gamble":       {"gamble"},
	"activities":   {"events", "groups"},
	"world_boss":   {"world_boss"},
	"pets":         {"pets"},
	"special_shop": {"special_shop"},
	"progress":     {"progress"},
	"rankings":     {"rankings"},
}

// buildGuide keys 為 nil 時建立全部欄位（完整 /api/guide），否則只建立指定的欄位（依 guideKeys 的順序）。
func buildGuide(ctx context.Context, t *db.Tx, now time.Time, keys []string) (*OrderedMap, error) {
	if err := featuresettings.Refresh(ctx, t, false); err != nil { // 管理網站調整過的簽到、寵物、特賣與成就數值
		return nil, err
	}
	e := &guideEnv{ctx: ctx, t: t, now: now}
	g := NewOrderedMap()
	for _, k := range guideKeys {
		if keys != nil && !slices.Contains(keys, k.key) {
			continue
		}
		for _, need := range k.needs {
			if err := need(e); err != nil {
				return nil, err
			}
		}
		v, err := k.build(e)
		if err != nil {
			return nil, err
		}
		g.Set(k.key, v)
	}
	return g, nil
}

// buildCraft 加工的規則（Go 版新增的欄位）：排隊上限、可以當原料的突變、取消訂單。
func buildCraft() map[string]any {
	mutations := []string{}
	for _, m := range CraftMutations {
		if m != "none" {
			mutations = append(mutations, balance.Mutations[m])
		}
	}
	return map[string]any{"queue_max": balance.CraftQueueMax, "mutations": mutations, "max_quantity": balance.CraftMaxQuantity}
}

// buildGroups 群組共同任務與競賽的種類（任務與競賽本身由管理員建立，這裡只有種類與規則）。
func buildGroups() map[string]any {
	metricNames := map[string]string{"quantity": "數量", "amount": "金額"}
	rewardNames := map[string]string{"gold": "金幣"}
	for _, code := range balance.QuestRewardTypes {
		if it, ok := balance.Items[code]; ok {
			rewardNames[code] = it.Name
		}
	}
	quests := []map[string]any{}
	for _, code := range balance.QuestTypeOrder {
		q := balance.QuestTypes[code]
		metrics := []string{}
		for _, m := range q.Metrics {
			metrics = append(metrics, metricNames[m])
		}
		quests = append(quests, map[string]any{"name": q.Name, "metrics": metrics})
	}
	rewards := []string{}
	for _, code := range balance.QuestRewardTypes {
		rewards = append(rewards, nameOr(rewardNames, code))
	}
	modes := []string{}
	for _, code := range balance.QuestRewardModeOrder {
		modes = append(modes, balance.QuestRewardModes[code])
	}
	comps := []map[string]any{}
	for _, code := range balance.CompetitionTypeOrder {
		c := balance.CompetitionTypes[code]
		comps = append(comps, map[string]any{"name": c.Name, "scoring": c.Desc})
	}
	return map[string]any{"quests": quests, "quest_rewards": rewards, "quest_reward_modes": modes,
		"competitions": comps, "competition_reward_ranks": balance.CompetitionRewardRanks,
		"max_days": balance.EventMaxDays, "transfer_cooldown_days": balance.GroupTransferCooldownSeconds / 86400}
}

// buildCards 首頁功能卡片需要的名稱與數字（一次載入，資料量小；詳細數值在操作手冊打開時才讀）。
func buildCards(e *guideEnv) (any, error) {
	names := func(order []string, name func(string) string) []string {
		out := []string{}
		for _, c := range order {
			out = append(out, name(c))
		}
		return out
	}
	allAnimals, err := animals.ListAll(e.ctx, e.t, false)
	if err != nil {
		return nil, err
	}
	animalNames := []string{}
	for _, a := range allAnimals {
		if a.Active {
			animalNames = append(animalNames, a.Name)
		}
	}
	buildings := []string{}
	for _, code := range balance.RecipeOrder {
		if n := balance.Buildings[balance.Recipes[code].Building].Name; !slices.Contains(buildings, n) {
			buildings = append(buildings, n)
		}
	}
	tools := []string{}
	for _, code := range balance.ItemOrder {
		if it := balance.Items[code]; it.Kind == "tool" {
			tools = append(tools, it.Name)
		}
	}
	cropTimes := []map[string]any{}
	for _, c := range e.activeCrops {
		cropTimes = append(cropTimes, map[string]any{"name": c.Name, "grow_seconds": c.GrowSeconds})
	}
	fishRarities := names(fishing.Rarities, func(r string) string { return fishing.RarityNames[r] })
	// 首頁的世界 Boss 出現時間：第一個啟用中的全服排程（沒有時用第一個排程）
	var boss any
	scheds, err := db.QueryStructs[models.WorldBossSchedule](e.ctx, e.t,
		"SELECT * FROM world_boss_schedules WHERE enabled ORDER BY (group_id IS NOT NULL), id LIMIT 1")
	if err != nil {
		return nil, err
	}
	if len(scheds) > 0 {
		sc := scheds[0]
		boss = map[string]any{"name": sc.Name, "interval_days": sc.IntervalDays,
			"time": fmt.Sprintf("%02d:%02d", sc.SpawnHour, sc.SpawnMinute)}
	}
	return map[string]any{
		"qualities":     names(balance.QualityOrder, func(q string) string { return balance.Qualities[q] }),
		"mutations":     names([]string{"giant", "golden", "fast", "variant"}, func(m string) string { return balance.Mutations[m] }),
		"animals":       animalNames,
		"buildings":     buildings,
		"recipes":       names(balance.RecipeOrder, func(c string) string { return balance.Recipes[c].Name }),
		"defenses":      names(balance.DefenseOrder, func(c string) string { return balance.Defenses[c].Name }),
		"tools":         tools,
		"areas":         names(exploration.AreaOrder, func(c string) string { return exploration.AreaOf(c).Name }),
		"machines":      names(exploration.MachineOrder, func(c string) string { return exploration.Machines[c].Name }),
		"rare_seeds":    e.rareSeedCrops(),
		"crop_times":    cropTimes,
		"fish_rarities": fishRarities,
		"boss":          boss,
		"hilo":          balance.GambleHiloMultiplier, "exact": balance.GambleExactMultiplier,
		"transfer_days": balance.GroupTransferCooldownSeconds / 86400,
		"achievements":  len(achievements.Active()),
		"checkin_days":  len(checkin.Rewards()),
		"codex":         codexNames(),
		"rename_cost":   balance.RenameFarmCost,
		"cellar_level":  mushroom.Current().UnlockLevel,
		"lab_level":     seedlab.Unlock(),
	}, nil
}

// codexNames 圖鑑分類名稱（與 Discord「📖 圖鑑」相同）。
func codexNames() []string {
	out := []string{}
	for _, c := range CodexCategories {
		out = append(out, c.Name)
	}
	return out
}

func nameOr(names map[string]string, code string) string {
	if n, ok := names[code]; ok {
		return n
	}
	return code
}

// buildExploration 山洞冒險的規則（區域、機率、機器、培育配方、等級解鎖），全部來自 game/exploration。
func buildExploration(names map[string]string) map[string]any {
	name := func(code string) string {
		if m, ok := exploration.Materials[code]; ok {
			return m.Name
		}
		if crop, ok := strings.CutPrefix(code, "seed_"); ok {
			return nameOr(names, crop) + "種子"
		}
		return nameOr(names, code)
	}
	// 同一物品可能在不同等級各列一筆（提高機率），手冊只列最早可取得的等級
	items := func(a exploration.Area, kind string) []nameLevel {
		var order []string
		first := map[string]int{}
		for _, d := range a.Pools[kind] {
			if lv, ok := first[d.Item]; ok {
				first[d.Item] = min(lv, d.MinLevel)
				continue
			}
			order = append(order, d.Item)
			first[d.Item] = d.MinLevel
		}
		out := []nameLevel{}
		for _, code := range order {
			out = append(out, nameLevel{name(code), first[code]})
		}
		return out
	}
	typePct := func(key string) []nameChance {
		out := []nameChance{}
		for _, tp := range exploration.RewardTypes {
			out = append(out, nameChance{exploration.RewardTypeLabels[tp], bpPct(exploration.Current().RewardTypeBP[key][tp])})
		}
		return out
	}
	areas := []map[string]any{}
	for _, code := range exploration.AreaOrder {
		a := exploration.AreaOf(code)
		evs := []nameLevel{}
		for _, e := range a.Events {
			evs = append(evs, nameLevel{e.Name, e.MinLevel})
		}
		areas = append(areas, map[string]any{"emoji": a.Emoji, "name": a.Name, "description": a.Description,
			"unlock_level": a.UnlockLevel, "duration_seconds": a.DurationSeconds, "rolls": a.Rolls,
			"advanced_level": a.AdvancedLevel, "needs_ruins": a.NeedsRuins, "common": items(a, "common"),
			"rare": items(a, "rare"), "seeds": items(a, "seed"), "events": evs,
			"sawdust": []int{exploration.Current().SawdustDrop[code][0], exploration.Current().SawdustDrop[code][1]}})
	}
	materials := []map[string]any{}
	for _, code := range exploration.MaterialOrder {
		m := exploration.Materials[code]
		materials = append(materials, map[string]any{"name": m.Name, "rarity": m.Rarity, "usage": m.Usage})
	}
	machines := []map[string]any{}
	for _, code := range exploration.MachineOrder {
		m := exploration.Machines[code]
		mats := []siteNameQty{}
		for _, c := range m.MaterialOrder {
			mats = append(mats, siteNameQty{name(c), m.Materials[c]})
		}
		machines = append(machines, map[string]any{"emoji": m.Emoji, "name": m.Name, "unlock_level": m.UnlockLevel,
			"description": m.Description, "materials": mats})
	}
	incubator := []map[string]any{}
	for _, code := range exploration.IncubatorRecipeOrder {
		r := exploration.IncubatorRecipes[code]
		inputs := []siteNameQty{}
		for _, c := range r.InputOrder {
			inputs = append(inputs, siteNameQty{name(c), r.Inputs[c]})
		}
		incubator = append(incubator, map[string]any{"name": r.Name, "inputs": inputs, "output": name(r.Output),
			"output_quantity": r.OutputQty, "seconds": r.Seconds})
	}
	unlocks := []map[string]any{}
	for _, n := range exploration.LevelUnlocks {
		unlocks = append(unlocks, map[string]any{"level": n.Level, "text": n.Text})
	}
	return map[string]any{
		"max_level":             balance.InitialLevel + (balance.MaxPlots-balance.InitialPlots)/balance.UpgradePlotsAdded,
		"fake_coin_value":       exploration.FakeCoinRate(),
		"material_bag_capacity": exploration.BagCapacity(),
		"reward_types":          map[string]any{"normal": typePct("normal"), "advanced": typePct("advanced")},
		"areas":                 areas, "materials": materials, "machines": machines,
		"seed_maker_ratio": exploration.SeedMakerRatio, "incubator": incubator,
		"incubator_max": exploration.IncubatorMaxQuantity, "level_unlocks": unlocks,
	}
}

// buildRuins 古代遺跡（群組修復）、遺跡守護者與額外探索的規則（固定數值，沒有任何群組資料）。
func buildRuins(itemNames map[string]string) map[string]any {
	area := exploration.AreaOf("ruins")
	stages := []map[string]any{}
	for _, st := range ruins.Stages {
		mats := []siteNameQty{}
		for _, c := range st.MaterialOrder {
			n, ok := itemNames[c]
			if !ok {
				n = exploration.MaterialName(c)
			}
			mats = append(mats, siteNameQty{n, st.Materials[c]})
		}
		stages = append(stages, map[string]any{"name": st.Name, "description": st.Description, "materials": mats})
	}
	damage := NewOrderedMap() // 前端以 Object.entries 顯示：維持 Python GUARDIAN_DAMAGE 的順序
	for _, rarity := range []string{"普通", "稀有", "極稀有"} {
		damage.Set(rarity, ruins.GuardianDamage[rarity])
	}
	return map[string]any{
		"stages": stages, "title": ruins.Title, "unlock_level": area.UnlockLevel,
		"cooldown_seconds": area.DurationSeconds, "rolls": area.Rolls,
		"extra_rolls": []int{exploration.RuinsExtra()[0], exploration.RuinsExtra()[1]},
		"guardian": map[string]any{"name": ruins.GuardianName, "hp_base": ruins.GuardianHPBase,
			"hp_per_member": ruins.GuardianHPPerMember, "hp_max": ruins.GuardianHPMax, "damage": damage,
			"expedition_damage": ruins.ExpeditionDamage, "bonus_rolls": ruins.DefeatBonusRolls},
	}
}

// buildNews 總覽的農場日報與傳聞挖寶規則。
func buildNews() map[string]any {
	return map[string]any{
		"max_headlines": news.MaxHeadlines, "rumor_chance_pct": numfmt.Round(news.RumorChance * 100),
		"rumor_true_pct": numfmt.Round(news.RumorTrueChance * 100), "dig_chance_pct": numfmt.Round(news.DigChance * 100),
		"treasure_gold": []int{news.TreasureGoldLow, news.TreasureGoldHigh},
	}
}

func buildSeasons(now time.Time) map[string]any {
	months := NewOrderedMap()
	months.Set("春", "3–5 月")
	months.Set("夏", "6–8 月")
	months.Set("秋", "9–11 月")
	months.Set("冬", "12–2 月")
	return map[string]any{"now_text": seasons.NowText(now), "current": seasons.SeasonNames[seasons.SeasonOf(now)],
		"months": months,
		// 非當季的作物售價加成（Go 版新增；商店只賣當季可以種的種子）
		"off_season_sell_pct": seasons.OffSeasonSellPct - 100}
}

// buildFishing 釣魚手冊：稀有度機率與售價、基礎成功率、時間設定、啟用中的釣具與魚種（只有規則與目錄，沒有玩家資料）。
func buildFishing(ctx context.Context, t *db.Tx) (map[string]any, error) {
	version, raw, err := fishrepo.CurrentSettings(ctx, t)
	if err != nil {
		return nil, err
	}
	s, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	fishTypes, err := fishrepo.ListFishTypes(ctx, t, true)
	if err != nil {
		return nil, err
	}
	allGear, err := fishrepo.ListGear(ctx, t, false)
	if err != nil {
		return nil, err
	}
	gearNames := map[string]string{}
	for _, gr := range allGear {
		gearNames[gr.Code] = gr.Emoji + " " + gr.Name
	}
	prices := make([]fishing.PriceRow, len(fishTypes))
	for i, f := range fishTypes {
		prices[i] = fishing.PriceRow{Rarity: f.Rarity, MinPrice: f.MinPrice, MaxPrice: f.MaxPrice}
	}
	rarities := []map[string]any{}
	for _, r := range fishing.Rarities {
		var span []int // 沒有魚時為 null（Python None）
		if lo, hi, ok := fishing.PriceSpan(prices, r); ok {
			span = []int{lo, hi}
		}
		d := s.Difficulty[r]
		rarities = append(rarities, map[string]any{"code": r, "name": fishing.RarityNames[r], "emoji": fishing.RarityEmoji[r],
			"chance_pct": bpPct(s.RarityBP[r]), "price": span, "stamina": d.Stamina, "surge_pct": d.SurgePct,
			"pulls": []int{fishing.Pulls[r][0], fishing.Pulls[r][1]}, "strong_pct": d.StrongPct, "chain_pct": d.ChainPct})
	}
	activeGear, err := fishrepo.ListGear(ctx, t, true)
	if err != nil {
		return nil, err
	}
	gear := []map[string]any{}
	for _, gr := range activeGear {
		gear = append(gear, map[string]any{"kind": gr.Kind, "kind_name": fishing.GearKindNames[gr.Kind], "name": gr.Name,
			"emoji": gr.Emoji, "price": gr.Price, "required_level": gr.RequiredLevel, "description": gr.Description,
			"effects_text": fishing.GearEffectsText(gr.Kind, gr.Effects)})
	}
	fish := []map[string]any{}
	for _, f := range fishTypes {
		baits := []string{}
		for _, b := range f.RequiredBaits {
			baits = append(baits, nameOr(gearNames, b))
		}
		fish = append(fish, map[string]any{"name": f.Name, "emoji": f.Emoji, "rarity": f.Rarity,
			"rarity_name": fishing.RarityNames[f.Rarity], "rarity_emoji": fishing.RarityEmoji[f.Rarity],
			"price": []int{f.MinPrice, f.MaxPrice}, "weight": []string{fishing.WeightText(f.MinWeightG), fishing.WeightText(f.MaxWeightG)},
			"description": f.Description, "limit_text": seasons.Describe(f.Seasons, f.HourStart, f.HourEnd), "baits": baits})
	}
	return map[string]any{
		"settings_version": version, "base_success_pct": s.BaseSuccessPct,
		"bite_wait": []int{s.BiteWaitMinSeconds, s.BiteWaitMaxSeconds}, "reaction_seconds": s.ReactionSeconds,
		"action_timeout_seconds": s.ActionTimeoutSeconds, "basket_capacity": s.BasketCapacity,
		"escape_distance": fishing.EscapeDistance, // 距離達到這個值魚就逃走
		"rarities":        rarities, "gear": gear, "fish": fish,
	}, nil
}

// bossSource 手冊數值的來源（Python _boss_source）：未結束的全服活動（進行中優先，其次最早開始的）
// → 第一個啟用的全服排程 → 程式預設值。只看全服（group_id NULL）：伺服器自己的活動不公開。
// itemOrder 是 settings.items 的鍵順序（資料庫中的 JSON 順序；預設值依 wb.DefaultItemOrder）。
type bossSource struct {
	settings  wb.Settings
	itemOrder []string
	rewards   wb.Rewards
	minutes   int
	current   map[string]any // nil = 沒有進行中的全服活動
	source    map[string]any
}

func loadBossSource(ctx context.Context, t *db.Tx, now time.Time) (bossSource, error) {
	evs, err := db.QueryStructs[models.WorldBossEvent](ctx, t,
		"SELECT * FROM world_boss_events WHERE group_id IS NULL AND status <> 'ended' ORDER BY starts_at, id")
	if err != nil {
		return bossSource{}, err
	}
	var views, live []BossView
	for i := range evs {
		v, err := EventView(ctx, t, &evs[i], now, nil)
		if err != nil {
			return bossSource{}, err
		}
		views = append(views, v)
		if v.State != "ended" && v.State != "defeated" {
			live = append(live, v)
		}
	}
	if len(live) > 0 {
		views = live
	}
	if len(views) > 0 {
		v := views[0]
		for _, x := range views {
			if x.State == "active" {
				v = x
				break
			}
		}
		supplies := []map[string]any{}
		for _, sp := range v.Supplies {
			supplies = append(supplies, map[string]any{"name": sp.Name, "emoji": sp.Emoji, "progress": sp.Progress, "target": sp.Target})
		}
		current := map[string]any{"name": v.Name, "state": v.State, "state_label": v.StateLabel, "hp": v.HP,
			"max_hp": v.MaxHP, "hp_pct": v.HPPct, "starts_at": v.StartsAt, "ends_at": v.EndsAt,
			"participants": v.Participants, "supplies": supplies}
		return bossSource{settings: v.Settings, itemOrder: jsonKeyOrder(v.RawSettings, "items"), rewards: v.Rewards,
			minutes: int(v.EndsAt.Sub(v.StartsAt).Seconds()) / 60, current: current,
			source: map[string]any{"kind": "event", "name": v.Name}}, nil
	}
	sc, err := db.QueryStruct[models.WorldBossSchedule](ctx, t,
		"SELECT * FROM world_boss_schedules WHERE enabled AND group_id IS NULL ORDER BY id LIMIT 1")
	if err != nil {
		return bossSource{}, err
	}
	if sc != nil {
		return bossSource{settings: decodeBossSettings(sc.Settings), itemOrder: jsonKeyOrder(sc.Settings, "items"),
			rewards: decodeBossRewards(sc.Rewards), minutes: sc.DurationMinutes,
			source: map[string]any{"kind": "schedule", "name": sc.Name}}, nil
	}
	return bossSource{settings: wb.DefaultSettings(), itemOrder: wb.DefaultItemOrder, rewards: wb.DefaultRewards(),
		minutes: wb.DefaultDurationHours * 60, source: map[string]any{"kind": "default", "name": nil}}, nil
}

// jsonKeyOrder JSON 物件 raw[key] 的鍵順序（資料庫的 JSON 欄位保留寫入時的順序，Python 讀出的 dict 也是這個順序）。
func jsonKeyOrder(raw json.RawMessage, key string) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(top[key]))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

// buildWorldBoss 世界 Boss 手冊：補給類型與效果、捐獻類別、積分規則、限制、獎勵、本場全服活動狀態，以及啟用中的
// 固定出現排程（名稱、全服或伺服器、出現時間與獎勵；不含伺服器名稱或任何玩家資料）。
func buildWorldBoss(ctx context.Context, t *db.Tx, names map[string]string, now time.Time) (map[string]any, error) {
	src, err := loadBossSource(ctx, t, now)
	if err != nil {
		return nil, err
	}
	s := src.settings
	scheds, err := db.QueryStructs[models.WorldBossSchedule](ctx, t, "SELECT * FROM world_boss_schedules WHERE enabled ORDER BY id")
	if err != nil {
		return nil, err
	}
	schedules := []map[string]any{}
	for _, sc := range scheds {
		scope := "全服"
		if sc.GroupID != nil {
			scope = "指定伺服器"
		}
		schedules = append(schedules, map[string]any{"name": sc.Name, "scope": scope, "interval_days": sc.IntervalDays,
			"time": fmt.Sprintf("%02d:%02d", sc.SpawnHour, sc.SpawnMinute), "duration_minutes": sc.DurationMinutes,
			"max_hp": sc.MaxHP, "rewards": NewBossRewardsText(decodeBossRewards(sc.Rewards), names)})
	}
	eff := s.Effects
	effectText := map[string]string{
		"food":      fmt.Sprintf("沒有糧食時出勤率 %d%%，補滿時 100%%", eff.BaseUptimePct),
		"medical":   fmt.Sprintf("補滿時救回 %d%% 的缺勤時間", eff.MedicalRecoveryPct),
		"combat":    fmt.Sprintf("補滿時傷害 +%d%%", eff.CombatAttackPct),
		"equipment": fmt.Sprintf("補滿時傷害 +%d%%", eff.EquipmentAttackPct),
	}
	supplies := []map[string]any{}
	for _, code := range wb.Supplies {
		info := wb.SupplyInfos[code]
		supplies = append(supplies, map[string]any{"code": code, "emoji": info.Emoji, "name": info.Name, "effect": info.Effect,
			"description": info.Desc, "effect_text": effectText[code], "target": s.SupplyTargets[code]})
	}
	categories := []map[string]any{}
	for _, code := range wb.Categories {
		c := wb.CategoryInfos[code]
		categories = append(categories, map[string]any{"emoji": c.Emoji, "name": c.Name})
	}
	order := src.itemOrder
	if len(order) != len(s.Items) { // 順序資訊不完整時（例如 JSON 格式不符）依代碼排序
		order = slices.Sorted(maps.Keys(s.Items))
	}
	items := []map[string]any{}
	for _, code := range order {
		v, ok := s.Items[code]
		if !ok || v.Points <= 0 {
			continue
		}
		items = append(items, map[string]any{"name": nameOr(names, code), "supply": wb.SupplyInfos[v.Supply].Name, "points": v.Points})
	}
	var current any
	if src.current != nil {
		current = src.current
	}
	return map[string]any{
		"source": src.source, "current": current, "supplies": supplies, "categories": categories, "items": items,
		"gold_per_point": s.GoldPerPoint, "base_damage_per_hour": s.BaseDamagePerHour,
		"instant_damage_per_point": s.InstantDamagePerPoint, "ratio_cap_pct": eff.RatioCapPct, "limits": s.Limits,
		"duration_hours":   wb.DefaultDurationHours, // 管理員手動建立活動的預設時長
		"duration_minutes": src.minutes,             // 本場（或排程）的持續時間
		"rewards":          NewBossRewardsText(src.rewards, names), "schedules": schedules,
	}, nil
}

// buildProgress 每日簽到、成就與圖鑑的規則（只有固定設定，沒有玩家資料）。
func buildProgress() map[string]any {
	cats := map[string]achievements.Category{}
	for _, c := range achievements.Categories {
		cats[c.Code] = c
	}
	days := []map[string]any{}
	for i, r := range checkin.Rewards() {
		days = append(days, map[string]any{"day": i + 1, "reward": checkin.RewardText(r)})
	}
	list := []map[string]any{}
	for _, a := range achievements.Active() {
		c := cats[a.Category]
		var title any // 沒有稱號時為 null（Python None）
		if a.Title != "" {
			title = a.Title
		}
		list = append(list, map[string]any{"category": c.Emoji + " " + c.Name, "name": a.Emoji + " " + a.Name,
			"description": a.Description(), "reward": achievements.RewardText(a), "title": title})
	}
	codex := []string{}
	for _, c := range CodexCategories { // 與 Discord「📖 圖鑑」的分類相同（含 Go 版新增的菇類、特殊種子）
		name := c.Name
		if c.Key == "crop" {
			name += "（含珍稀變種）"
		}
		codex = append(codex, c.Emoji+" "+name)
	}
	return map[string]any{"checkin": days, "achievements": list, "codex": codex}
}

// buildPets 寵物規則；cat_odds：每種啟用中的作物在貓咪 Lv0～最高級時，每格實際多收多少（與遊戲相同算法）。
func buildPets(activeCrops []models.CropType) map[string]any {
	maxLevel := pets.MaxLevel()
	odds := []map[string]any{}
	for _, c := range activeCrops {
		levels := []string{}
		for lv := 0; lv <= maxLevel; lv++ {
			levels = append(levels, pets.OddsText(c.HarvestQuantity, pets.BonusPct(&lv)))
		}
		odds = append(odds, map[string]any{"name": c.Name, "quantity": c.HarvestQuantity, "levels": levels})
	}
	list := []map[string]any{}
	for _, code := range pets.PetOrder {
		p := pets.Pets[code]
		list = append(list, map[string]any{"name": p.Emoji + " " + p.Name, "role": p.Role,
			"effect": strings.ReplaceAll(p.Effect, "{pct}", "X")})
	}
	return map[string]any{"cat_odds": odds, "buy_price": pets.BuyPrice(), "upgrade_prices": pets.UpgradePrices(),
		"base_pct": pets.BasePct(), "pct_per_level": pets.PctPerLevel(), "max_level": maxLevel,
		"max_pct": pets.BonusPct(&maxLevel), "pets": list,
		// 狗狗不受防守設施整體阻止機率上限的限制
		"defense_cap_pct": numfmt.Round(balance.DefenseMaxBlockChance * 100)}
}

// buildSpecialShop 商店「特賣與升級」：溫室價格、本週特賣（所有玩家相同）與稱號商店（沒有玩家資料）。
func buildSpecialShop(activeCrops []models.CropType, now time.Time) map[string]any {
	start := specialshop.WeekStart(now)
	refs := make([]specialshop.CropRef, len(activeCrops))
	for i, c := range activeCrops {
		refs[i] = specialshop.CropRef{Code: c.Code, Name: c.Name, SeedPrice: c.SeedPrice, ShopBuyable: c.ShopBuyable}
	}
	offers := []map[string]any{}
	for _, o := range specialshop.WeeklyOffers(start, refs) {
		offers = append(offers, map[string]any{"name": o.CropName + "種子", "price": o.Price,
			"original_price": o.OriginalPrice, "limit": o.Limit, "rare": o.Rare})
	}
	titles := []map[string]any{}
	for _, x := range specialshop.TitleShop() {
		titles = append(titles, map[string]any{"title": x.Title, "price": x.Price})
	}
	return map[string]any{
		"greenhouse": map[string]any{"per_level": specialshop.GreenhousePctPerLevel(), "prices": specialshop.GreenhousePrices()},
		"weekly": map[string]any{"week_start": start.Format("2006-01-02"),
			"ends_at": specialshop.WeekEndsAt(start).In(clock.TaipeiTZ).Format(time.RFC3339), "offers": offers,
			// 每週特賣的規則（管理網站可調整）
			"normal_deals": specialshop.ConfigInt("normal_deals"), "normal_discount_pct": specialshop.ConfigInt("normal_discount_pct"),
			"normal_limit": specialshop.ConfigInt("normal_limit"), "rare_limit": specialshop.ConfigInt("rare_limit")},
		"titles": titles,
	}
}

// buildSeedLab 種子實驗室的規則（不列出雜交組合與雜交作物）。rareSeeds：商店不賣種子的稀有作物（特殊種子的來源說明）。
func buildSeedLab(rareSeeds []string) map[string]any {
	c := seedlab.Current()
	minMin, maxMin := c.SameSpeciesMinutes, c.SameSpeciesMinutes
	for _, h := range seedlab.CurrentHybrids() {
		minMin, maxMin = min(minMin, h.Minutes), max(maxMin, h.Minutes)
	}
	mutations := []map[string]any{}
	for _, m := range []struct{ code, effect string }{
		{"glow", fmt.Sprintf("售價 ×%s", pyG(float64(balance.GlowPricePct)/100))},
		{"bountiful", fmt.Sprintf("收成數量 +%d%%（至少 +1）", balance.BountifulBonusPct)},
		{"crystal", fmt.Sprintf("收成時額外得到 %d–%d 個水晶", balance.CrystalDropMin, balance.CrystalDropMax)},
	} {
		mutations = append(mutations, map[string]any{"name": balance.Mutations[m.code], "effect": m.effect})
	}
	return map[string]any{
		"unlock_level": c.UnlockLevel, "build_gold": c.BuildGold, "capacity": c.LibraryCapacity,
		"identify_multiplier": c.IdentifyFeeMultiplier, "identify_min": c.IdentifyFeeMin,
		"min_minutes": minMin, "max_minutes": maxMin, "mutate_minutes": c.MutateMinutes, "mutate_crystal": c.MutateCrystal,
		"mutate_base_pct": bpPct(c.MutateBaseBP), "mutate_level_pct": bpPct(c.MutatePerLevelBP), "mutate_max_pct": bpPct(c.MutateMaxBP),
		"growth_pct": c.GrowthPctPerLevel, "yield_pct": c.YieldPctPerLevel, "quality_pct": bpPct(c.QualityBPPerLevel),
		"mutation_pct": bpPct(c.MutationBPPerLevel), "inherit_pct": bpPct(c.InheritBP),
		"resin_pct": bpPct(c.ResinSuccessBP), "herb_pct": c.HerbTimePct, "catalyst_pct": bpPct(c.CatalystUpBP),
		"share_send": c.ShareSendPerDay, "share_receive": c.ShareReceiveSenders, "max_slots": seedlab.MaxLevel,
		"hybrid_count": len(seedlab.Hybrids), "lab_mutations": mutations,
		// 2026-11-14 起：基因升降、突變遺傳、研究經驗與等級、雜交作物售價、稀有種子來源
		"gene_up_pct": bpPct(c.GeneUpBP), "gene_down_pct": bpPct(c.GeneDownBP),
		"inherit_mutation_pct": bpPct(c.InheritMutationBP), "hybrid_price_mul": 1.5,
		"exp":       map[string]any{"identify": c.ExpIdentify, "cross": c.ExpCross, "cross_fail": c.ExpCrossFail, "mutate": c.ExpMutate},
		"level_exp": []int{c.LevelExp[2], c.LevelExp[3]}, "rare_seeds": rareSeeds,
	}
}

// buildMushroom 地下菇房（Go 版新增）：開放等級、生長時間、養分對應的作物與菇、木屑的稀有機率、菇與料理的售價。
func buildMushroom(names map[string]string) map[string]any {
	cfg := mushroom.Current()
	nutrients := []map[string]any{}
	for _, code := range mushroom.NutrientOrder {
		n := mushroom.Nutrients[code]
		crops := []string{}
		for _, c := range cfg.NutrientCrops[code] {
			crops = append(crops, nameOr(names, c))
		}
		nutrients = append(nutrients, map[string]any{"name": n.Name, "crops": crops, "default": code == mushroom.DefaultNutrient,
			"common": mushroom.SpeciesName(n.Common), "common_price": balance.Goods[n.Common].SellPrice,
			"rare": mushroom.SpeciesName(n.Rare), "rare_price": balance.Goods[n.Rare].SellPrice})
	}
	rare := []map[string]any{}
	for i, p := range cfg.RarePct {
		rare = append(rare, map[string]any{"sawdust": i, "pct": p, "guaranteed": mushroom.GuaranteedRare(i, cfg.YieldPerBed)})
	}
	sawdust := []map[string]any{}
	for _, code := range exploration.AreaOrder {
		a := exploration.AreaOf(code)
		d := exploration.Current().SawdustDrop[code]
		sawdust = append(sawdust, map[string]any{"area": a.Emoji + " " + a.Name, "min": d[0], "max": d[1]})
	}
	return map[string]any{"unlock_level": cfg.UnlockLevel, "beds": mushroom.BedCount, "grow_seconds": cfg.GrowSeconds,
		"yield_per_bed": cfg.YieldPerBed, "max_sawdust": mushroom.MaxSawdust, "nutrients": nutrients, "rare": rare,
		"sawdust_drop": sawdust}
}
