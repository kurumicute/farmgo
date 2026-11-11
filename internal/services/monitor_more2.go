package services

// 監控資料服務第三部分（對應 Python app/services/monitor_service.py 的釣魚、排名獎勵、世界 Boss）：全部在唯讀交易中查詢。

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/fishing"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/farms"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/groups"

	wb "farmgm/internal/game/worldboss"
)

// ---------- 釣魚（唯讀） ----------

// FishingSummary 釣魚總覽（Python fishing_summary：overview 加上目前設定的版本、成功率與稀有度機率）。
type FishingSummary struct {
	fishrepo.Overview
	SettingsVersion int             `json:"settings_version"`
	BaseSuccessPct  int             `json:"base_success_pct"`
	RarityBP        json.RawMessage `json:"rarity_bp"` // 設定中的原始 JSON（Python 直接輸出 settings["rarity_bp"]）
	ServerTime      time.Time       `json:"server_time"`
}

func (m *MonitorService) FishingSummary(ctx context.Context) (*FishingSummary, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*FishingSummary, error) {
		data, err := fishrepo.OverviewOf(ctx, t, now)
		if err != nil {
			return nil, err
		}
		version, raw, err := fishrepo.CurrentSettings(ctx, t)
		if err != nil {
			return nil, err
		}
		var s struct {
			BaseSuccessPct int             `json:"base_success_pct"`
			RarityBP       json.RawMessage `json:"rarity_bp"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return &FishingSummary{data, version, s.BaseSuccessPct, s.RarityBP, now}, nil
	})
}

// FishingPlayerList 玩家釣魚統計的一頁。
type FishingPlayerList struct {
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
	Items  []fishrepo.PlayerRow `json:"items"`
}

// FishingPlayers q 為 "" 表示不搜尋。
func (m *MonitorService) FishingPlayers(ctx context.Context, q string, limit, offset int) (FishingPlayerList, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (FishingPlayerList, error) {
		total, items, err := fishrepo.PlayerRows(ctx, t, q, limit, offset)
		return FishingPlayerList{total, limit, offset, items}, err
	})
}

// FishingShareRow 分享紀錄加上群組名稱（群組不存在時為 nil）。
type FishingShareRow struct {
	fishrepo.DetailShare
	GroupName *string `json:"group_name"`
}

// FishingPlayerView 單一玩家的釣魚紀錄。Summary 為 nil 表示統計列表中沒有這位玩家（沒有釣魚紀錄也沒有釣具）。
type FishingPlayerView struct {
	PlayerID     int64                        `json:"player_id"`
	FarmID       int64                        `json:"farm_id"`
	Summary      *fishrepo.PlayerRow          `json:"summary"`
	Catches      []fishrepo.DetailCatch       `json:"catches"`
	Sessions     []fishrepo.DetailSession     `json:"sessions"`
	Transactions []fishrepo.DetailTransaction `json:"transactions"`
	Shares       []FishingShareRow            `json:"shares"`
	Gear         []fishrepo.DetailGear        `json:"gear"`
}

// FishingPlayer 找不到農場時回傳 nil。
func (m *MonitorService) FishingPlayer(ctx context.Context, playerID int64) (*FishingPlayerView, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (*FishingPlayerView, error) {
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil || farm == nil {
			return nil, err
		}
		// 與 Python 相同：以「#ID」搜尋第一筆，ID 不同時沒有統計
		_, rows, err := fishrepo.PlayerRows(ctx, t, "#"+strconv.FormatInt(playerID, 10), 1, 0)
		if err != nil {
			return nil, err
		}
		v := &FishingPlayerView{PlayerID: playerID, FarmID: farm.ID, Shares: []FishingShareRow{}}
		for i := range rows {
			if rows[i].PlayerID == playerID {
				v.Summary = &rows[i]
				break
			}
		}
		detail, err := fishrepo.PlayerDetail(ctx, t, farm.ID, 30)
		if err != nil {
			return nil, err
		}
		v.Catches, v.Sessions, v.Transactions, v.Gear = detail.Catches, detail.Sessions, detail.Transactions, detail.Gear
		names := map[int64]*string{}
		for _, s := range detail.Shares {
			row := FishingShareRow{DetailShare: s}
			if s.GroupID != nil {
				name, ok := names[*s.GroupID]
				if !ok {
					g, err := groups.Get(ctx, t, *s.GroupID)
					if err != nil {
						return nil, err
					}
					if g != nil {
						name = &g.Name
					}
					names[*s.GroupID] = name
				}
				row.GroupName = name
			}
			v.Shares = append(v.Shares, row)
		}
		return v, nil
	})
}

type FishingCatalogFish struct {
	FishTypeView
	Caught int `json:"caught"`
}

type FishingCatalogGear struct {
	GearView
	Owners int `json:"owners"`
}

type FishingSettingsHistory struct {
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type FishingLabels struct {
	Rarities    map[string]string `json:"rarities"`
	RarityEmoji map[string]string `json:"rarity_emoji"`
	Kinds       map[string]string `json:"kinds"`
	Settings    map[string]string `json:"settings"`
	Difficulty  map[string]string `json:"difficulty"`
	Effects     map[string]string `json:"effects"`
	Statuses    map[string]string `json:"statuses"`
}

// FishingRanges 管理表單的欄位範圍；Settings、Difficulty、Effects 前端依鍵的順序建立表單，所以用 OrderedMap。
type FishingRanges struct {
	FishPrice      [2]int      `json:"fish_price"`
	Settings       *OrderedMap `json:"settings"`
	Difficulty     *OrderedMap `json:"difficulty"`
	Effects        *OrderedMap `json:"effects"`
	WeightG        [2]int      `json:"weight_g"`
	FishDifficulty [2]int      `json:"fish_difficulty"`
	GearPrice      [2]int      `json:"gear_price"`
	GearLevel      [2]int      `json:"gear_level"`
	GearStock      [2]int      `json:"gear_stock"`
}

type FishingCatalog struct {
	Fish            []FishingCatalogFish     `json:"fish"`
	Gear            []FishingCatalogGear     `json:"gear"`
	Settings        json.RawMessage          `json:"settings"`
	SettingsVersion int                      `json:"settings_version"`
	History         []FishingSettingsHistory `json:"history"`
	Defaults        fishing.Settings         `json:"defaults"`
	Labels          FishingLabels            `json:"labels"`
	Ranges          FishingRanges            `json:"ranges"`
}

// limitRanges 依 Python dict 的順序輸出 {鍵: [下限, 上限]}。
func limitRanges(limits []fishing.Limit) *OrderedMap {
	out := NewOrderedMap()
	for _, l := range limits {
		out.Set(l.Key, [2]int{l.Low, l.High})
	}
	return out
}

func (m *MonitorService) FishingCatalog(ctx context.Context) (*FishingCatalog, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (*FishingCatalog, error) {
		version, settings, err := fishrepo.CurrentSettings(ctx, t)
		if err != nil {
			return nil, err
		}
		caught, err := fishrepo.FishCounts(ctx, t)
		if err != nil {
			return nil, err
		}
		owners, err := fishrepo.GearUsage(ctx, t)
		if err != nil {
			return nil, err
		}
		effects := NewOrderedMap()
		for _, kind := range fishing.GearKinds {
			effects.Set(kind, limitRanges(fishing.GearEffectLimits[kind]))
		}
		c := &FishingCatalog{Fish: []FishingCatalogFish{}, Gear: []FishingCatalogGear{}, Settings: settings,
			SettingsVersion: version, History: []FishingSettingsHistory{}, Defaults: fishing.DefaultSettings(),
			Labels: FishingLabels{fishing.RarityNames, fishing.RarityEmoji, fishing.GearKindNames, fishing.SettingLabels,
				fishing.DifficultyLabels, fishing.GearEffectNames, fishing.StatusNames},
			Ranges: FishingRanges{[2]int{fishing.PriceMin, fishing.PriceMax}, limitRanges(fishing.SettingLimits),
				limitRanges(fishing.DifficultyLimits), effects, fishing.FishWeightRangeG, fishing.FishDifficultyRng,
				fishing.GearPriceRange, fishing.GearLevelRange, fishing.GearStockRange}}
		fish, err := fishrepo.ListFishTypes(ctx, t, false)
		if err != nil {
			return nil, err
		}
		for _, f := range fish {
			c.Fish = append(c.Fish, FishingCatalogFish{NewFishTypeView(f), caught[f.Code]})
		}
		gear, err := fishrepo.ListGear(ctx, t, false)
		if err != nil {
			return nil, err
		}
		for _, g := range gear {
			c.Gear = append(c.Gear, FishingCatalogGear{NewGearView(g, nil, nil), owners[g.Code]})
		}
		history, err := fishrepo.SettingsHistory(ctx, t, 10)
		if err != nil {
			return nil, err
		}
		for _, h := range history {
			c.History = append(c.History, FishingSettingsHistory{h.Version, h.CreatedBy, h.CreatedAt})
		}
		return c, nil
	})
}

// ---------- 排名獎勵（唯讀） ----------

type RankingRewardsView struct {
	Rules       []RuleView       `json:"rules"`
	Settlements []SettlementView `json:"settlements"`
	ServerTime  time.Time        `json:"server_time"`
}

func (m *MonitorService) RankingRewards(ctx context.Context, limit int) (*RankingRewardsView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*RankingRewardsView, error) {
		list, err := db.QueryStructs[models.RankingRewardRule](ctx, t, "SELECT * FROM ranking_reward_rules ORDER BY id")
		if err != nil {
			return nil, err
		}
		v := &RankingRewardsView{Rules: []RuleView{}, ServerTime: now}
		for _, rule := range list {
			v.Rules = append(v.Rules, NewRuleView(rule, now))
		}
		if v.Settlements, err = listSettlements(ctx, t, limit); err != nil {
			return nil, err
		}
		v.Settlements = nonNil(v.Settlements)
		return v, nil
	})
}

// RankingSettlement 找不到時回傳 nil。
func (m *MonitorService) RankingSettlement(ctx context.Context, settlementID int64) (*SettlementDetail, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (*SettlementDetail, error) {
		v, err := settlementDetail(ctx, t, settlementID)
		if err != nil || v == nil {
			return nil, err
		}
		d := v.Detail()
		return &d, nil
	})
}

// ---------- 世界 Boss（唯讀：HP 依同一公式即時推算，不寫回資料庫） ----------

type BossSupplyLabel struct {
	Emoji       string `json:"emoji"`
	Name        string `json:"name"`
	Effect      string `json:"effect"`
	Description string `json:"description"`
}

type BossCategoryLabel struct {
	Emoji string `json:"emoji"`
	Name  string `json:"name"`
}

type BossItemOption struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"` // reward_items 沒有這個鍵
}

// BossDefaults 管理網站表單用（Python defaults_view）：預設值、補給與類別說明、可捐獻物品與可發放的獎勵物品。
type BossDefaults struct {
	Name          string           `json:"name"`
	MaxHP         int              `json:"max_hp"`
	Schedule      *OrderedMap      `json:"schedule"`
	DurationHours int              `json:"duration_hours"`
	Settings      *OrderedMap      `json:"settings"`
	Rewards       wb.Rewards       `json:"rewards"`
	Supplies      *OrderedMap      `json:"supplies"`
	Categories    *OrderedMap      `json:"categories"`
	Donatable     []BossItemOption `json:"donatable"`
	RewardItems   []BossItemOption `json:"reward_items"`
}

// orderedBossSettings 依 Python DEFAULT_SETTINGS 的鍵順序輸出設定；items 依 wb.DefaultItemOrder（其他代碼排在後面，依代碼排序），
// 前端以 Object.entries 把 items 填入表單。
func orderedBossSettings(s wb.Settings) *OrderedMap {
	targets := NewOrderedMap()
	for _, code := range wb.Supplies {
		if v, ok := s.SupplyTargets[code]; ok {
			targets.Set(code, v)
		}
	}
	items := NewOrderedMap()
	for _, code := range wb.DefaultItemOrder {
		if v, ok := s.Items[code]; ok {
			items.Set(code, v)
		}
	}
	rest := make([]string, 0, len(s.Items))
	for code := range s.Items {
		if _, ok := items.Get(code); !ok {
			rest = append(rest, code)
		}
	}
	slices.Sort(rest)
	for _, code := range rest {
		items.Set(code, s.Items[code])
	}
	return NewOrderedMap().Set("base_damage_per_hour", s.BaseDamagePerHour).
		Set("instant_damage_per_point", s.InstantDamagePerPoint).Set("gold_per_point", s.GoldPerPoint).
		Set("crop_supply", s.CropSupply).Set("fish_supply", s.FishSupply).Set("supply_targets", targets).
		Set("effects", s.Effects).Set("limits", s.Limits).Set("items", items)
}

// BossDefaultsView 只依規則與目錄產生（Python defaults_view）。
func BossDefaultsView(ctx context.Context, t *db.Tx) (*BossDefaults, error) {
	kinds, err := catalog.ListItems(ctx, t)
	if err != nil {
		return nil, err
	}
	dragon := wb.AbyssDragon()
	d := &BossDefaults{Name: wb.DefaultName, MaxHP: wb.DefaultMaxHP, DurationHours: wb.DefaultDurationHours,
		Schedule: NewOrderedMap().Set("name", dragon.Name).Set("interval_days", dragon.IntervalDays).
			Set("spawn_hour", dragon.SpawnHour).Set("spawn_minute", dragon.SpawnMinute).
			Set("anchor_date", dragon.AnchorDate.Format(time.DateOnly)).Set("duration_minutes", dragon.DurationMinutes).
			Set("max_hp", dragon.MaxHP).Set("settings", orderedBossSettings(dragon.Settings)).Set("rewards", dragon.Rewards),
		Settings: orderedBossSettings(wb.DefaultSettings()), Rewards: wb.DefaultRewards(),
		Supplies: NewOrderedMap(), Categories: NewOrderedMap(), Donatable: []BossItemOption{}, RewardItems: []BossItemOption{}}
	for _, code := range wb.Supplies {
		info := wb.SupplyInfos[code]
		d.Supplies.Set(code, BossSupplyLabel{info.Emoji, info.Name, info.Effect, info.Desc})
	}
	for _, code := range wb.Categories {
		info := wb.CategoryInfos[code]
		d.Categories.Set(code, BossCategoryLabel{info.Emoji, info.Name})
	}
	for _, code := range kinds.Order {
		e := kinds.Map[code]
		if cat := ItemCategory(e.Kind, code); cat != "" {
			d.Donatable = append(d.Donatable, BossItemOption{code, e.Name, cat})
		}
		d.RewardItems = append(d.RewardItems, BossItemOption{Code: code, Name: e.Name})
	}
	slices.SortStableFunc(d.Donatable, func(a, b BossItemOption) int {
		if c := slices.Index(wb.Categories, a.Category) - slices.Index(wb.Categories, b.Category); c != 0 {
			return c
		}
		return strings.Compare(a.Code, b.Code)
	})
	fish, err := fishrepo.ListFishTypes(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, f := range fish {
		d.Donatable = append(d.Donatable, BossItemOption{f.Code, f.Name, "fish"})
	}
	return d, nil
}

type WorldBossList struct {
	Events     []BossView         `json:"events"`
	Schedules  []BossScheduleView `json:"schedules"`
	Defaults   *BossDefaults      `json:"defaults"`
	ServerTime time.Time          `json:"server_time"`
}

// WorldBoss 活動列表（未結束的在前；列表不含 rewards）、排程與管理表單的預設值。
func (m *MonitorService) WorldBoss(ctx context.Context, limit int) (*WorldBossList, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*WorldBossList, error) {
		events, err := db.QueryStructs[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events "+
			"ORDER BY (status <> 'ended') DESC, created_at DESC, id DESC LIMIT $1", limit)
		if err != nil {
			return nil, err
		}
		out := &WorldBossList{Events: []BossView{}, Schedules: []BossScheduleView{}, ServerTime: now}
		for i := range events {
			v, err := EventView(ctx, t, &events[i], now, nil)
			if err != nil {
				return nil, err
			}
			v.RawRewards = nil // Python view.pop("rewards")
			out.Events = append(out.Events, v)
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		schedules, err := db.QueryStructs[models.WorldBossSchedule](ctx, t, "SELECT * FROM world_boss_schedules ORDER BY id")
		if err != nil {
			return nil, err
		}
		for i := range schedules {
			v, err := ScheduleView(ctx, t, &schedules[i], now, names)
			if err != nil {
				return nil, err
			}
			out.Schedules = append(out.Schedules, v)
		}
		out.Defaults, err = BossDefaultsView(ctx, t)
		return out, err
	})
}

// WorldBossDetail 找不到時回傳 nil。
func (m *MonitorService) WorldBossDetail(ctx context.Context, eventID int64) (*BossDetail, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*BossDetail, error) {
		ev, err := db.QueryStruct[models.WorldBossEvent](ctx, t, "SELECT * FROM world_boss_events WHERE id = $1", eventID)
		if err != nil || ev == nil {
			return nil, err
		}
		v, err := DetailView(ctx, t, ev, now)
		if err != nil {
			return nil, err
		}
		d := v.Detail()
		return &d, nil
	})
}
