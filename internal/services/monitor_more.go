package services

// 監控資料服務（續）：動物養殖與加工、群組任務與競賽、管理操作紀錄、作物品質與突變、目錄、單一玩家的管理資料。
// 對應 Python app/services/monitor_service.py 的同名方法，回傳值的鍵與 Python dict 相同。

import (
	"context"
	"slices"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	eventrules "farmgm/internal/game/events"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/events"
	"farmgm/internal/repositories/farms"
	generepo "farmgm/internal/repositories/genetics"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/ranch"
)

// ---------- 動物養殖與加工（唯讀） ----------

type RanchStateCounts struct {
	Hungry    int `json:"hungry"`
	Producing int `json:"producing"`
	Ready     int `json:"ready"`
}

type RanchRow struct {
	FarmID         int64            `json:"farm_id"`
	FarmName       string           `json:"farm_name"`
	PlayerID       int64            `json:"player_id"`
	PlayerName     string           `json:"player_name"`
	Gold           int              `json:"gold"`
	Buildings      *OrderedMap      `json:"buildings"` // 建築名稱 → 數量（依建築順序，只列有的）
	Capacity       int              `json:"capacity"`
	AnimalCount    int              `json:"animal_count"`
	StateCounts    RanchStateCounts `json:"state_counts"`
	FeedCostHungry int              `json:"feed_cost_hungry"`
	NextReadyAt    *time.Time       `json:"next_ready_at"`
	Animals        []AnimalView     `json:"animals"`
}

func (m *MonitorService) Ranch(ctx context.Context) ([]RanchRow, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]RanchRow, error) {
		types, err := animals.ByCode(ctx, t)
		if err != nil {
			return nil, err
		}
		list, err := ranch.FarmsWithRanch(ctx, t)
		if err != nil {
			return nil, err
		}
		rows := []RanchRow{}
		for _, f := range list {
			buildings, err := ranch.ListBuildings(ctx, t, f.ID)
			if err != nil {
				return nil, err
			}
			counts := map[string]int{}
			for _, b := range buildings {
				counts[b.BuildingCode]++
			}
			row := RanchRow{FarmID: f.ID, FarmName: f.Name, PlayerID: f.PlayerID, PlayerName: f.PlayerName, Gold: f.Gold,
				Buildings: NewOrderedMap(), Capacity: counts["barn"] * balance.BarnSlots, Animals: []AnimalView{}}
			for _, code := range balance.BuildingOrder {
				if n := counts[code]; n != 0 {
					row.Buildings.Set(balance.Buildings[code].Name, n)
				}
			}
			list, err := ranch.ListAnimals(ctx, t, f.ID, false)
			if err != nil {
				return nil, err
			}
			for _, a := range list {
				v := NewAnimalView(a, now, types[a.AnimalCode])
				row.Animals = append(row.Animals, v)
				switch v.State {
				case "hungry":
					row.StateCounts.Hungry++
					row.FeedCostHungry += v.FeedCost
				case "producing":
					row.StateCounts.Producing++
					if v.ReadyAt != nil && (row.NextReadyAt == nil || v.ReadyAt.Before(*row.NextReadyAt)) {
						row.NextReadyAt = v.ReadyAt
					}
				case "ready":
					row.StateCounts.Ready++
				}
			}
			row.AnimalCount = len(row.Animals)
			rows = append(rows, row)
		}
		return rows, nil
	})
}

// CraftOrderRow 加工訂單（order_view 加上農場與玩家）。
type CraftOrderRow struct {
	OrderView
	FarmName   string `json:"farm_name"`
	PlayerID   int64  `json:"player_id"`
	PlayerName string `json:"player_name"`
}

func (m *MonitorService) CraftOrders(ctx context.Context, includeCollected bool, limit int) ([]CraftOrderRow, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]CraftOrderRow, error) {
		rows, err := ranch.ListOrders(ctx, t, nil, includeCollected, limit)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		out := []CraftOrderRow{}
		for _, r := range rows {
			out = append(out, CraftOrderRow{NewOrderView(r.CraftOrder, now, names), r.FarmName, r.PlayerID, r.PlayerName})
		}
		return out, nil
	})
}

// ---------- 群組任務與競賽（唯讀；結算由遊戲服務或管理員觸發） ----------

// Quests groupID 為 nil 時列出全部群組。
func (m *MonitorService) Quests(ctx context.Context, groupID *int64, limit int) ([]QuestView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]QuestView, error) {
		var ids []int64
		if groupID != nil {
			ids = []int64{*groupID}
		}
		rows, err := community.ListQuests(ctx, t, ids, nil, limit)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		out := []QuestView{}
		for _, r := range rows {
			out = append(out, NewQuestView(r.Quest, r.GroupName, now, names))
		}
		return out, nil
	})
}

type QuestContributorJSON struct {
	PlayerID   int64  `json:"player_id"`
	PlayerName string `json:"player_name"`
	Amount     int    `json:"amount"`
}

type QuestClaimJSON struct {
	PlayerName string    `json:"player_name"`
	RewardType string    `json:"reward_type"`
	Amount     int       `json:"amount"`
	ClaimedAt  time.Time `json:"claimed_at"`
}

type MonitorQuestDetail struct {
	QuestView
	Contributors []QuestContributorJSON `json:"contributors"`
	Claims       []QuestClaimJSON       `json:"claims"`
}

// QuestDetail 不存在時回傳 nil。
func (m *MonitorService) QuestDetail(ctx context.Context, questID int64) (*MonitorQuestDetail, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*MonitorQuestDetail, error) {
		q, err := community.GetQuest(ctx, t, questID, false)
		if err != nil || q == nil {
			return nil, err
		}
		g, err := groups.Get(ctx, t, q.GroupID)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		groupName := ""
		if g != nil {
			groupName = g.Name
		}
		v := &MonitorQuestDetail{QuestView: NewQuestView(*q, groupName, now, names),
			Contributors: []QuestContributorJSON{}, Claims: []QuestClaimJSON{}}
		contributors, err := community.QuestContributors(ctx, t, questID)
		if err != nil {
			return nil, err
		}
		for _, c := range contributors {
			v.Contributors = append(v.Contributors, QuestContributorJSON{c.PlayerID, c.PlayerName, c.Total})
		}
		claims, err := community.QuestClaims(ctx, t, questID)
		for _, c := range claims {
			v.Claims = append(v.Claims, QuestClaimJSON{c.PlayerName, c.RewardType, c.Amount, c.ClaimedAt})
		}
		return v, err
	})
}

func (m *MonitorService) Competitions(ctx context.Context, limit int) ([]CompetitionView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]CompetitionView, error) {
		rows, err := community.ListCompetitions(ctx, t, nil, limit)
		out := []CompetitionView{}
		for _, c := range rows {
			out = append(out, NewCompetitionView(c, now))
		}
		return out, err
	})
}

type CompetitionAwardJSON struct {
	PlayerName string `json:"player_name"`
	GroupID    int64  `json:"group_id"`
	Rank       int    `json:"rank"`
	Gold       int    `json:"gold"`
}

type MonitorCompetitionDetail struct {
	CompetitionView
	Final      bool                     `json:"final"`
	Ranking    []community.RankingRow   `json:"ranking"`
	TopPlayers []community.PlayerPoints `json:"top_players"`
	Awards     []CompetitionAwardJSON   `json:"awards"`
}

// CompetitionDetail 不存在時回傳 nil。已結算用最終排名，進行中用即時排名。
func (m *MonitorService) CompetitionDetail(ctx context.Context, competitionID int64) (*MonitorCompetitionDetail, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*MonitorCompetitionDetail, error) {
		c, err := community.GetCompetition(ctx, t, competitionID, false)
		if err != nil || c == nil {
			return nil, err
		}
		v := &MonitorCompetitionDetail{CompetitionView: NewCompetitionView(*c, now), Final: c.Status == "settled",
			Awards: []CompetitionAwardJSON{}}
		if v.Final {
			v.Ranking, err = community.FinalRanking(ctx, t, competitionID)
		} else {
			v.Ranking, err = community.LiveRanking(ctx, t, competitionID)
		}
		if err != nil {
			return nil, err
		}
		v.Ranking = nonNil(v.Ranking)
		top, err := community.CompetitionPlayerPoints(ctx, t, competitionID, 20)
		if err != nil {
			return nil, err
		}
		v.TopPlayers = nonNil(top)
		awards, err := community.CompetitionAwards(ctx, t, competitionID)
		for _, a := range awards {
			v.Awards = append(v.Awards, CompetitionAwardJSON{a.PlayerName, a.GroupID, a.Rank, a.Gold})
		}
		return v, err
	})
}

type AdminActionJSON struct {
	CreatedAt  time.Time      `json:"created_at"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   *int64         `json:"target_id"`
	Details    map[string]any `json:"details"`
}

func (m *MonitorService) AdminActions(ctx context.Context, limit int) ([]AdminActionJSON, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]AdminActionJSON, error) {
		rows, err := community.ListAdminActions(ctx, t, limit)
		out := []AdminActionJSON{}
		for _, a := range rows {
			out = append(out, AdminActionJSON{a.CreatedAt, a.Actor, a.Action, a.TargetType, a.TargetID, a.Details})
		}
		return out, err
	})
}

// ---------- 作物品質與突變（唯讀） ----------

type GeneticsLabels struct {
	Qualities *OrderedMap `json:"qualities"`
	Mutations *OrderedMap `json:"mutations"`
}

func geneticsLabels() GeneticsLabels {
	return GeneticsLabels{orderedStrings(balance.QualityOrder, balance.Qualities), orderedStrings(balance.MutationOrder, balance.Mutations)}
}

type CodeName struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type GeneticsCrop struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	GrowSeconds int    `json:"grow_seconds"`
	SellPrice   int    `json:"sell_price"`
}

type VersionRow struct {
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type GeneticsView struct {
	Version          int                      `json:"version"`
	Settings         balance.GeneticsSettings `json:"settings"`
	Labels           GeneticsLabels           `json:"labels"`
	Variants         *OrderedMap              `json:"variants"` // 作物代碼 → {code, name}
	Crops            []GeneticsCrop           `json:"crops"`
	GrowSecondsRange [2]int                   `json:"grow_seconds_range"`
	MinGrowSeconds   int                      `json:"min_grow_seconds"`
	History          []VersionRow             `json:"history"`
}

func (m *MonitorService) Genetics(ctx context.Context) (GeneticsView, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (GeneticsView, error) {
		version, settings, err := generepo.Current(ctx, t)
		if err != nil {
			return GeneticsView{}, err
		}
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return GeneticsView{}, err
		}
		r := balance.CropGrowSecondsRange
		v := GeneticsView{Version: version, Settings: settings, Labels: geneticsLabels(), Variants: NewOrderedMap(),
			Crops: []GeneticsCrop{}, GrowSecondsRange: [2]int{r.Low, r.High}, MinGrowSeconds: balance.MinGrowSeconds,
			History: []VersionRow{}}
		for _, c := range all {
			v.Crops = append(v.Crops, GeneticsCrop{c.Code, c.Name, c.GrowSeconds, c.SellPrice})
			v.Variants.Set(c.Code, CodeName{c.VariantCode, c.VariantName})
		}
		history, err := generepo.History(ctx, t, 20)
		for _, h := range history {
			v.History = append(v.History, VersionRow{h.Version, h.CreatedBy, h.CreatedAt})
		}
		return v, err
	})
}

func (m *MonitorService) GeneticsStats(ctx context.Context) (*generepo.StatsResult, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (*generepo.StatsResult, error) { return generepo.Stats(ctx, t) })
}

// ---------- 作物、動物、事件目錄與單一玩家的管理資料（唯讀） ----------

type CatalogItem struct {
	Code string `json:"code"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type CatalogBuilding struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Max  int    `json:"max"`
}

type CatalogView struct {
	Crops          []CropView        `json:"crops"`
	Animals        []AnimalTypeView  `json:"animals"`
	Events         []map[string]any  `json:"events"`
	Items          []CatalogItem     `json:"items"`
	Buildings      []CatalogBuilding `json:"buildings"`
	Labels         GeneticsLabels    `json:"labels"`
	Ranges         *OrderedMap       `json:"ranges"`
	EventCaps      *OrderedMap       `json:"event_caps"`
	ActiveEventIDs []int64           `json:"active_event_ids"`
	ServerTime     time.Time         `json:"server_time"`
}

func rangeOf(r balance.Range) [2]int { return [2]int{r.Low, r.High} }

func (m *MonitorService) Catalog(ctx context.Context) (CatalogView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (CatalogView, error) {
		all, err := crops.ListAll(ctx, t, false)
		if err != nil {
			return CatalogView{}, err
		}
		v := CatalogView{Crops: []CropView{}, Animals: []AnimalTypeView{}, Events: []map[string]any{}, Items: []CatalogItem{},
			Buildings: []CatalogBuilding{}, Labels: geneticsLabels(), Ranges: NewOrderedMap(), EventCaps: NewOrderedMap(),
			ActiveEventIDs: []int64{}, ServerTime: now}
		cropCodes := map[int64]string{}
		for _, c := range all {
			cropCodes[c.ID] = c.Code
			v.Crops = append(v.Crops, NewCropView(c))
		}
		animalList, err := animals.ListAll(ctx, t, false)
		if err != nil {
			return CatalogView{}, err
		}
		for _, a := range animalList {
			v.Animals = append(v.Animals, NewAnimalTypeView(a))
		}
		eventList, err := events.ListAll(ctx, t)
		if err != nil {
			return CatalogView{}, err
		}
		for i := range eventList {
			e := eventrules.View(events.ToRule(&eventList[i]), now)
			var code any
			if id := eventList[i].CropTypeID; id != nil {
				if c, ok := cropCodes[*id]; ok {
					code = c
				}
			}
			e["crop_code"] = code
			v.Events = append(v.Events, e)
		}
		items, err := catalog.ListItems(ctx, t)
		if err != nil {
			return CatalogView{}, err
		}
		for _, code := range items.Order {
			e := items.Map[code]
			v.Items = append(v.Items, CatalogItem{code, e.Kind, e.Name})
		}
		for _, code := range balance.BuildingOrder {
			b := balance.Buildings[code]
			v.Buildings = append(v.Buildings, CatalogBuilding{code, b.Name, b.MaxPerFarm})
		}
		v.Ranges.Set("grow_seconds", rangeOf(balance.CropGrowSecondsRange)).
			Set("seed_price", rangeOf(balance.CropSeedPriceRange)).
			Set("harvest_quantity", rangeOf(balance.CropHarvestQuantityRange)).
			Set("sell_price", rangeOf(balance.CropSellPriceRange)).
			Set("animal_price", rangeOf(balance.AnimalPriceRange)).
			Set("feed_cost", rangeOf(balance.AnimalFeedRange)).
			Set("cycle_seconds", rangeOf(balance.AnimalCycleRange)).
			Set("product_quantity", rangeOf(balance.AnimalProductQuantityRange)).
			Set("product_price", rangeOf(balance.AnimalProductPriceRange))
		for _, k := range eventrules.EffectOrder {
			v.Ranges.Set(k, rangeOf(eventrules.Effects[k].Range))
			v.EventCaps.Set(k, eventrules.Effects[k].Cap)
		}
		active, err := events.Active(ctx, t, now)
		for _, e := range active {
			v.ActiveEventIDs = append(v.ActiveEventIDs, e.ID)
		}
		return v, err
	})
}

type AdminFarmPlot struct {
	PlotNumber        int        `json:"plot_number"`
	State             string     `json:"state"`
	StateLabel        string     `json:"state_label"`
	CropCode          *string    `json:"crop_code"`
	CropName          *string    `json:"crop_name"`
	Quality           *string    `json:"quality"`
	Mutation          *string    `json:"mutation"`
	RemainingQuantity *int       `json:"remaining_quantity"`
	ReadyAt           *time.Time `json:"ready_at"`
}

type AdminFarmBuilding struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Slot int    `json:"slot"`
}

type AdminFarmItem struct {
	ItemCode string `json:"item_code"`
	Name     string `json:"name"`
	Quality  string `json:"quality"`
	Mutation string `json:"mutation"`
	Quantity int    `json:"quantity"`
}

// AdminFarmView 管理員調整單一玩家時需要的完整資料：土地、建築、動物、背包庫存（含品質與突變）。
type AdminFarmView struct {
	PlayerID            int64               `json:"player_id"`
	PlayerName          string              `json:"player_name"`
	ExternalUserID      *string             `json:"external_user_id"` // Discord ID 一律為字串
	FarmName            string              `json:"farm_name"`
	HomeGroupID         *int64              `json:"home_group_id"`
	HomeGroupName       *string             `json:"home_group_name"`
	LastGroupTransferAt *time.Time          `json:"last_group_transfer_at"`
	NextGroupTransferAt *time.Time          `json:"next_group_transfer_at"`
	Gold                int                 `json:"gold"`
	Level               int                 `json:"level"`
	Used                int                 `json:"used"`
	Capacity            int                 `json:"capacity"`
	Plots               []AdminFarmPlot     `json:"plots"`
	Buildings           []AdminFarmBuilding `json:"buildings"`
	BusyBuildings       []string            `json:"busy_buildings"`
	AnimalCapacity      int                 `json:"animal_capacity"`
	Animals             []AnimalView        `json:"animals"`
	Inventory           []AdminFarmItem     `json:"inventory"`
	ServerTime          time.Time           `json:"server_time"`
}

// AdminFarm 玩家沒有農場時回傳 nil。
func (m *MonitorService) AdminFarm(ctx context.Context, playerID int64) (*AdminFarmView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*AdminFarmView, error) {
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil || farm == nil {
			return nil, err
		}
		p, err := players.Get(ctx, t, playerID, false)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		types, err := animals.ByCode(ctx, t)
		if err != nil {
			return nil, err
		}
		v := &AdminFarmView{PlayerID: playerID, FarmName: farm.Name, HomeGroupID: farm.HomeGroupID,
			LastGroupTransferAt: farm.LastGroupTransferAt, Gold: farm.Gold, Level: farm.Level, Capacity: farm.WarehouseCapacity,
			Plots: []AdminFarmPlot{}, Buildings: []AdminFarmBuilding{}, BusyBuildings: []string{}, Animals: []AnimalView{},
			Inventory: []AdminFarmItem{}, ServerTime: now}
		if p != nil {
			v.PlayerName, v.ExternalUserID = p.DisplayName, p.ExternalUserID
		}
		plots, err := farms.ListPlots(ctx, t, farm.ID, false)
		if err != nil {
			return nil, err
		}
		for _, pc := range plots {
			state := rules.PlotStateAt(pc.CropTypeID, pc.WateredAt, pc.ReadyAt, now)
			row := AdminFarmPlot{PlotNumber: pc.PlotNumber, State: string(state), StateLabel: rules.PlotStateLabels[state],
				Quality: pc.Quality, Mutation: pc.Mutation, RemainingQuantity: pc.RemainingQuantity, ReadyAt: pc.ReadyAt}
			if pc.Crop != nil {
				row.CropCode, row.CropName = &pc.Crop.Code, &pc.Crop.Name
			}
			v.Plots = append(v.Plots, row)
		}
		farmID := farm.ID
		orders, err := ranch.ListOrders(ctx, t, &farmID, false, 50)
		if err != nil {
			return nil, err
		}
		for _, o := range orders {
			if !slices.Contains(v.BusyBuildings, o.BuildingCode) {
				v.BusyBuildings = append(v.BusyBuildings, o.BuildingCode)
			}
		}
		slices.Sort(v.BusyBuildings)
		if farm.HomeGroupID != nil {
			g, err := groups.Get(ctx, t, *farm.HomeGroupID)
			if err != nil {
				return nil, err
			}
			if g != nil {
				v.HomeGroupName = &g.Name
			}
		}
		if last := farm.LastGroupTransferAt; last != nil {
			if next := last.Add(balance.GroupTransferCooldownSeconds * time.Second); next.After(now) {
				v.NextGroupTransferAt = &next
			}
		}
		if v.Used, err = inventory.TotalQuantity(ctx, t, farm.ID); err != nil {
			return nil, err
		}
		buildings, err := ranch.ListBuildings(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		for _, b := range buildings {
			v.Buildings = append(v.Buildings, AdminFarmBuilding{b.BuildingCode, balance.Buildings[b.BuildingCode].Name, b.Slot})
		}
		barns, err := ranch.CountBuildings(ctx, t, farm.ID, "barn")
		if err != nil {
			return nil, err
		}
		v.AnimalCapacity = barns * balance.BarnSlots
		animalList, err := ranch.ListAnimals(ctx, t, farm.ID, false)
		if err != nil {
			return nil, err
		}
		for _, a := range animalList {
			v.Animals = append(v.Animals, NewAnimalView(a, now, types[a.AnimalCode]))
		}
		items, err := inventory.ListItems(ctx, t, farm.ID)
		for _, i := range items {
			name, ok := names[i.ItemCode]
			if !ok {
				name = i.ItemCode
			}
			v.Inventory = append(v.Inventory, AdminFarmItem{i.ItemCode, name, i.Quality, i.Mutation, i.Quantity})
		}
		return v, err
	})
}
