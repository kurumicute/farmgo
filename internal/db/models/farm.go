package models

import "time"

const (
	TablePlayer          = "players"
	TableCropType        = "crop_types"
	TableAnimalType      = "animal_types"
	TableFarm            = "farms"
	TableFarmStats       = "farm_stats"
	TableFarmDailyStats  = "farm_daily_stats"
	TableFarmPlot        = "farm_plots"
	TableInventoryItem   = "inventory_items"
	TableGamblePrize     = "gamble_prizes"
	TableGameTransaction = "game_transactions"
	TableGroup           = "groups"
	TableGroupMember     = "group_members"
	TableFarmTool        = "farm_tools"
	TableFarmExploration = "farm_explorations"
	TableExplorationLog  = "exploration_logs"
	TableFarmEffect      = "farm_effects"
	TableStealRecord     = "steal_records"
	TableFarmDefense     = "farm_defenses"
	TableFarmBuilding    = "farm_buildings"
	TableFarmAnimal      = "farm_animals"
	TableCraftOrder      = "craft_orders"
)

// Player 玩家。
//
// 約束：ck_players_display_name_len（length(btrim(display_name)) BETWEEN 1 AND 32）、
// ck_players_external_pair（外部平台與其使用者 ID 必須同時存在或同時為空）、
// uq_players_external_identity（external_platform, external_user_id；WHERE external_platform IS NOT NULL，唯一索引）。
// relationship：farm（Farm.player_id，一對一）。
type Player struct {
	ID               int64     `db:"id"`
	DisplayName      string    `db:"display_name"`
	ExternalPlatform *string   `db:"external_platform"`
	ExternalUserID   *string   `db:"external_user_id"`
	CreatedAt        time.Time `db:"created_at"` // server_default now()
	UpdatedAt        time.Time `db:"updated_at"` // server_default now()；Python onupdate=now()，Go 更新時要自行設定
}

func (Player) TableName() string { return TablePlayer }

// CropType 作物種類。
//
// 約束：uq_crop_types_code、uq_crop_types_variant_code、ck_crop_types_seed_price_positive（seed_price > 0）、
// ck_crop_types_grow_seconds_positive、ck_crop_types_harvest_quantity_positive、ck_crop_types_sell_price_non_negative、
// ck_crop_types_hours_valid（hour_start／hour_end 同時為 NULL，或都在 0–23 且不相等）。
type CropType struct {
	ID              int64  `db:"id"`
	Code            string `db:"code"`
	Name            string `db:"name"`
	SeedPrice       int    `db:"seed_price"`
	GrowSeconds     int    `db:"grow_seconds"`
	HarvestQuantity int    `db:"harvest_quantity"`
	SellPrice       int    `db:"sell_price"`
	// 珍稀變種（收成後的獨立農產品代碼與名稱）；代碼建立後不可修改
	VariantCode string `db:"variant_code"`
	VariantName string `db:"variant_name"`
	// 停用：不能購買種子或種植；已種植、已持有的仍可收成、販售（server_default true）
	Active bool `db:"active"`
	// false：商店不販售種子（稀有作物，只能探索或培育取得），仍可種植已持有的種子（server_default true）
	ShopBuyable bool `db:"shop_buyable"`
	// 季節與時段限定（game/seasons；NULL = 不限，Migration 0034）
	Seasons   []string `db:"seasons"` // JSON，nil = NULL
	HourStart *int     `db:"hour_start"`
	HourEnd   *int     `db:"hour_end"`
}

func (CropType) TableName() string { return TableCropType }

// AnimalType 動物種類與其產品（產品代碼即背包物品代碼）。停用後不能購買，已飼養的仍可餵食、收集與販售產品。
//
// 約束：uq_animal_types_code、uq_animal_types_product_code、ck_animal_types_price_positive、
// ck_animal_types_feed_cost_non_negative、ck_animal_types_cycle_seconds_positive、
// ck_animal_types_product_quantity_positive、ck_animal_types_product_price_non_negative。
type AnimalType struct {
	ID              int64  `db:"id"`
	Code            string `db:"code"`
	Name            string `db:"name"`
	Price           int    `db:"price"`
	FeedCost        int    `db:"feed_cost"`
	CycleSeconds    int    `db:"cycle_seconds"`
	ProductCode     string `db:"product_code"`
	ProductName     string `db:"product_name"`
	ProductQuantity int    `db:"product_quantity"`
	ProductPrice    int    `db:"product_price"`
	Active          bool   `db:"active"` // server_default true
}

func (AnimalType) TableName() string { return TableAnimalType }

// Farm 農場。
//
// 約束：uq_farms_player_id、ck_farms_greenhouse_level_valid（0–10）、ck_farms_gold_non_negative、
// ck_farms_level_positive、ck_farms_warehouse_capacity_positive、ck_farms_name_len（1–40）、
// ix_farms_gold_rank（gold DESC, player_id）、ix_farms_home_group_id。
// 觸發器 farms_home_group_immutable（Migration 0010）保護 home_group_id 的變更。
// relationship：player（Player）。
type Farm struct {
	ID int64 `db:"id"`
	// MVP：一位玩家只擁有一座農場
	PlayerID int64 `db:"player_id"`
	// 主要所屬群組：建立時設定；之後只能透過帶 7 天冷卻的轉移變更（資料庫觸發器保護）；舊資料可為空，允許一次性設定
	HomeGroupID *int64 `db:"home_group_id"`
	// 最近一次成功轉移所屬群組的時間；NULL 表示從未轉移（首次轉移不需等待）
	LastGroupTransferAt *time.Time `db:"last_group_transfer_at"`
	Name                string     `db:"name"`
	Level               int        `db:"level"`
	Gold                int        `db:"gold"`
	WarehouseCapacity   int        `db:"warehouse_capacity"`
	Title               *string    `db:"title"` // 目前選用的稱號（必須在 farm_titles 中），NULL 不顯示
	// 溫室等級（0 = 沒有溫室）：每一級讓之後澆水的作物生長時間減少 specialshop.GreenhousePctPerLevel()%（server_default 0）
	GreenhouseLevel int       `db:"greenhouse_level"`
	CreatedAt       time.Time `db:"created_at"` // server_default now()
	UpdatedAt       time.Time `db:"updated_at"` // server_default now()；Python onupdate=now()
}

func (Farm) TableName() string { return TableFarm }

// FarmStats 每座農場的累積統計（全部期間排行用），在販售等交易的同一筆資料庫交易中更新。
//
// 可由 game_transactions 重算（repositories/farm_stats.rebuild），並以 check 比對一致性。
// non_operating_gold：建立農場、管理員調整與各種獎勵的金幣加總；全部期間淨賺金幣 = 目前金幣 - 此值。
// steal_count／defended_count／gamble_count：偷菜成功、防守成功、賭場的次數（成就進度用，不必掃描交易紀錄）。
//
// 約束：主鍵 farm_id；uq_farm_stats_player_id；ix_farm_stats_crop_rank、ix_farm_stats_goods_rank。
// 所有統計欄位 server_default 0。
type FarmStats struct {
	FarmID            int64 `db:"farm_id"`
	PlayerID          int64 `db:"player_id"`
	CropSoldQuantity  int   `db:"crop_sold_quantity"`
	CropSalesIncome   int   `db:"crop_sales_income"`
	GoodsSoldQuantity int   `db:"goods_sold_quantity"`
	GoodsSalesIncome  int   `db:"goods_sales_income"`
	NonOperatingGold  int   `db:"non_operating_gold"`
	StealCount        int   `db:"steal_count"`
	DefendedCount     int   `db:"defended_count"`
	GambleCount       int   `db:"gamble_count"`
}

func (FarmStats) TableName() string { return TableFarmStats }

// FarmDailyStats 每座農場每個台灣日期的販售與淨賺金幣，供本日／本週／本月排行與排名獎勵結算。
//
// 約束：主鍵 (farm_id, day)；ix_farm_daily_stats_day。統計欄位 server_default 0。
type FarmDailyStats struct {
	FarmID            int64     `db:"farm_id"`
	Day               time.Time `db:"day"` // DATE（台灣日期）；讀回是 UTC 00:00
	CropSoldQuantity  int       `db:"crop_sold_quantity"`
	CropSalesIncome   int       `db:"crop_sales_income"`
	GoodsSoldQuantity int       `db:"goods_sold_quantity"`
	GoodsSalesIncome  int       `db:"goods_sales_income"`
	NetGold           int       `db:"net_gold"`
}

func (FarmDailyStats) TableName() string { return TableFarmDailyStats }

// FarmPlot 農田土地。
//
// 約束：uq_farm_plots_farm_plot_number（farm_id, plot_number）、ck_farm_plots_plot_number_positive、
// ck_farm_plots_remaining_quantity_positive、ck_farm_plots_planting_no_non_negative、
// ck_farm_plots_crop_consistency（PlotCropConsistency：空地時作物相關欄位全部為空；有作物時必有播種時間與剩餘數量，
// 澆水時間與成熟時間同時存在）、ck_farm_plots_genetics_consistency（有作物 ⇔ quality、mutation 不為空）、
// ck_farm_plots_quality_valid、ck_farm_plots_mutation_valid、ix_farm_plots_ready_at（WHERE crop_type_id IS NOT NULL）、
// ix_farm_plots_crop_type_id。
// relationship：crop_type（CropType，crop_type_id）。
type FarmPlot struct {
	ID                int64      `db:"id"`
	FarmID            int64      `db:"farm_id"`
	PlotNumber        int        `db:"plot_number"`
	CropTypeID        *int64     `db:"crop_type_id"`
	PlantedAt         *time.Time `db:"planted_at"`
	WateredAt         *time.Time `db:"watered_at"`
	ReadyAt           *time.Time `db:"ready_at"`
	RemainingQuantity *int       `db:"remaining_quantity"`
	Fertilized        bool       `db:"fertilized"` // server_default false
	// 播種時決定、之後固定不變的品質與主要突變
	Quality  *string `db:"quality"`
	Mutation *string `db:"mutation"`
	// 每次種植加 1，用來識別「同一株作物」（偷菜每人每株限一次）（server_default 0）
	PlantingNo int       `db:"planting_no"`
	CreatedAt  time.Time `db:"created_at"` // server_default now()
	UpdatedAt  time.Time `db:"updated_at"` // server_default now()；Python onupdate=now()
	// 種下的特殊種子（種子實驗室，Migration 0039）；一般種子為 NULL。收成或清除時改成 NULL，種子標記為已消耗
	LabSeedID *int64 `db:"lab_seed_id"`
}

func (FarmPlot) TableName() string { return TableFarmPlot }

// InventoryItem 背包物品。同一種物品的不同品質或突變分開存放，不會混成同一筆庫存。
//
// 約束：uq_inventory_items_farm_item（farm_id, item_code, quality, mutation）、ck_inventory_items_quality_valid、
// ck_inventory_items_mutation_valid、ck_inventory_items_quantity_non_negative。
type InventoryItem struct {
	ID        int64     `db:"id"`
	FarmID    int64     `db:"farm_id"`
	ItemCode  string    `db:"item_code"`
	Quality   string    `db:"quality"`  // server_default 'normal'
	Mutation  string    `db:"mutation"` // server_default 'none'
	Quantity  int       `db:"quantity"`
	UpdatedAt time.Time `db:"updated_at"` // server_default now()；Python onupdate=now()
}

func (InventoryItem) TableName() string { return TableInventoryItem }

// GamblePrize 賭場獎品：贏得的作物以「價值倍率」存放在倉庫（佔倉庫空間），可以賣出或保留展示。
//
// 約束：uq_gamble_prizes_farm_stack（farm_id, item_code, quality, mutation, multiplier）、
// ck_gamble_prizes_quality_valid、ck_gamble_prizes_mutation_valid、ck_gamble_prizes_multiplier_valid（2 或 5）、
// ck_gamble_prizes_quantity_non_negative。
type GamblePrize struct {
	ID         int64     `db:"id"`
	FarmID     int64     `db:"farm_id"`
	ItemCode   string    `db:"item_code"`
	Quality    string    `db:"quality"`
	Mutation   string    `db:"mutation"`
	Multiplier int       `db:"multiplier"`
	Quantity   int       `db:"quantity"`
	UpdatedAt  time.Time `db:"updated_at"` // server_default now()；Python onupdate=now()
}

func (GamblePrize) TableName() string { return TableGamblePrize }

// AppliedEvent 交易套用的限時事件（game_transactions.applied_events 的元素）。
type AppliedEvent struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GameTransaction 交易紀錄（所有資產變動）。
//
// 約束：ck_game_transactions_event_type_valid（event_type IN EventTypes）、ck_game_transactions_quantity_non_negative、
// ix_game_transactions_farm_created（farm_id, created_at）、ix_game_transactions_created_at、
// ix_game_transactions_sell_farm（farm_id WHERE event_type = 'sell'）、ix_game_transactions_context_group_id。
type GameTransaction struct {
	ID          int64     `db:"id"`
	FarmID      int64     `db:"farm_id"`
	EventType   string    `db:"event_type"`
	ItemCode    *string   `db:"item_code"`
	Quantity    int       `db:"quantity"`   // server_default 0
	GoldDelta   int       `db:"gold_delta"` // server_default 0
	Description string    `db:"description"`
	CreatedAt   time.Time `db:"created_at"`
	// 作物相關操作的品質與突變；販售時保存實際成交單價（總收入為 gold_delta），歷史紀錄不會被改寫
	Quality   *string `db:"quality"`
	Mutation  *string `db:"mutation"`
	UnitPrice *int    `db:"unit_price"`
	// 操作實際發生時所在的群組（群組任務／競賽計分）；不影響農場的 home_group_id 與排行
	ContextGroupID *int64 `db:"context_group_id"`
	// 這次操作套用的限時事件 [{"id", "name"}]；沒有套用時為 NULL（nil）
	AppliedEvents []AppliedEvent `db:"applied_events"`
}

func (GameTransaction) TableName() string { return TableGameTransaction }

// Group 群組（CMD 模擬群組，或對應 Discord Guild）。
//
// 約束：ck_groups_name_len（1–40）、uq_groups_discord_guild_id。
type Group struct {
	ID             int64     `db:"id"`
	Name           string    `db:"name"`
	DiscordGuildID *string   `db:"discord_guild_id"`
	CreatedAt      time.Time `db:"created_at"` // server_default now()
}

func (Group) TableName() string { return TableGroup }

// GroupMember 群組成員。約束：主鍵 (group_id, player_id)；ix_group_members_player_id。
type GroupMember struct {
	GroupID  int64     `db:"group_id"`
	PlayerID int64     `db:"player_id"`
	JoinedAt time.Time `db:"joined_at"` // server_default now()
}

func (GroupMember) TableName() string { return TableGroupMember }

// FarmTool 永久工具（例如自動澆水器）與機器。
//
// 約束：uq_farm_tools_farm_tool（farm_id, tool_code）。
type FarmTool struct {
	ID         int64     `db:"id"`
	FarmID     int64     `db:"farm_id"`
	ToolCode   string    `db:"tool_code"`
	Enabled    bool      `db:"enabled"` // server_default true
	AcquiredAt time.Time `db:"acquired_at"`
	// 機器設定（例如自動加工機的加工項目 {"recipe": "flour"}）；一般工具為 NULL（nil）
	Settings map[string]any `db:"settings"`
}

func (FarmTool) TableName() string { return TableFarmTool }

// FarmExploration 各區域的探索次數與最近一次探險（出發時間、回來時間）：每座農場每個區域一列。
// 一次只能進行一趟探險，由 exploration_logs 的未領取紀錄判斷（Migration 0015）。
//
// 約束：uq_farm_explorations_farm_area（farm_id, area_code）、
// ck_farm_explorations_area_code_valid（grassland／forest／mine／ruins）、
// ck_farm_explorations_next_after_last（next_available_at > last_explored_at）、ck_farm_explorations_explore_count_positive。
type FarmExploration struct {
	ID              int64     `db:"id"`
	FarmID          int64     `db:"farm_id"`
	AreaCode        string    `db:"area_code"`
	LastExploredAt  time.Time `db:"last_explored_at"`
	NextAvailableAt time.Time `db:"next_available_at"`
	ExploreCount    int       `db:"explore_count"`
}

func (FarmExploration) TableName() string { return TableFarmExploration }

// ExplorationLog 探險紀錄：出發時決定獎勵明細，回來（next_available_at）後領取才放進背包（claimed_at）。
// claimed_at 為 NULL 的紀錄就是進行中或待領取的探險，每座農場最多一筆（部分唯一索引）。
//
// 約束：ck_exploration_logs_area_code_valid、ix_exploration_logs_farm_explored（farm_id, explored_at）、
// uq_exploration_logs_farm_unclaimed（farm_id WHERE claimed_at IS NULL，唯一索引）。
type ExplorationLog struct {
	ID              int64          `db:"id"`
	FarmID          int64          `db:"farm_id"`
	AreaCode        string         `db:"area_code"`
	FarmLevel       int            `db:"farm_level"`
	Advanced        bool           `db:"advanced"`
	Rewards         map[string]int `db:"rewards"`           // JSON {物品代碼: 數量}
	Events          []string       `db:"events"`            // JSON 觸發的特殊事件名稱
	ExploredAt      time.Time      `db:"explored_at"`       // 出發時間
	NextAvailableAt time.Time      `db:"next_available_at"` // 回來時間
	ClaimedAt       *time.Time     `db:"claimed_at"`        // 領取獎勵時間
}

func (ExplorationLog) TableName() string { return TableExplorationLog }

// FarmEffect 限時效果（例如人工養殖蚯蚓），以到期時間判斷是否有效。
//
// 約束：uq_farm_effects_farm_effect（farm_id, effect_code）、ck_farm_effects_expires_after_start、
// ck_farm_effects_multiplier_positive。
type FarmEffect struct {
	ID         int64     `db:"id"`
	FarmID     int64     `db:"farm_id"`
	EffectCode string    `db:"effect_code"`
	Multiplier float64   `db:"multiplier"`
	StartedAt  time.Time `db:"started_at"`
	ExpiresAt  time.Time `db:"expires_at"`
}

func (FarmEffect) TableName() string { return TableFarmEffect }

// StealRecord 偷菜紀錄（成功與被阻止）。
//
// 約束：uq_steal_records_once_per_planting（plot_id, planting_no, thief_player_id：同一位偷菜者對同一株作物只能偷一次）、
// ck_steal_records_quantity_range（1–2）、ck_steal_records_not_self、
// ck_steal_records_outcome_consistency（success ⇔ blocked_by IS NULL；blocked ⇔ blocked_by IS NOT NULL）、
// ck_steal_records_block_chance_range（NULL 或 0–1）、ix_steal_records_victim_created、ix_steal_records_created_at、
// ix_steal_records_group_created、ix_steal_records_thief、ix_steal_records_victim。
type StealRecord struct {
	ID             int64 `db:"id"`
	GroupID        int64 `db:"group_id"`
	ThiefPlayerID  int64 `db:"thief_player_id"`
	VictimPlayerID int64 `db:"victim_player_id"`
	// 土地被管理員刪除時保留偷菜紀錄（plot_id 設為 NULL，plot_number 仍保留）
	PlotID     *int64    `db:"plot_id"`
	PlotNumber int       `db:"plot_number"`
	PlantingNo int       `db:"planting_no"`
	CropTypeID int64     `db:"crop_type_id"`
	Quantity   int       `db:"quantity"`
	CreatedAt  time.Time `db:"created_at"`
	// success：成功偷走 quantity 個；blocked：被 blocked_by 設施阻止，雙方物品都不變（quantity 為嘗試數量）
	Outcome     string   `db:"outcome"` // server_default 'success'
	BlockedBy   *string  `db:"blocked_by"`
	BlockChance *float64 `db:"block_chance"` // 當次整體阻止機率（無防守時為 NULL）
	Roll        *float64 `db:"roll"`         // 當次亂數值，供稽核
}

func (StealRecord) TableName() string { return TableStealRecord }

// FarmDefense 永久防守設施。slot 為同種設施的編號（1..上限），由服務層在鎖定農場後分配。
//
// 約束：uq_farm_defenses_farm_device_slot（farm_id, device_code, slot）、ck_farm_defenses_slot_positive、
// ck_farm_defenses_start_plot_positive。
type FarmDefense struct {
	ID         int64  `db:"id"`
	FarmID     int64  `db:"farm_id"`
	DeviceCode string `db:"device_code"`
	Slot       int    `db:"slot"`
	// 守護範圍起點（連續 N 格）；守護全部土地的設施為 NULL
	StartPlot  *int      `db:"start_plot"`
	AcquiredAt time.Time `db:"acquired_at"`
}

func (FarmDefense) TableName() string { return TableFarmDefense }

// FarmBuilding 畜舍與加工設施。slot 為同種建築編號（1..上限），由服務層在鎖定農場後分配。
//
// 約束：uq_farm_buildings_farm_building_slot（farm_id, building_code, slot）、ck_farm_buildings_slot_positive。
type FarmBuilding struct {
	ID           int64     `db:"id"`
	FarmID       int64     `db:"farm_id"`
	BuildingCode string    `db:"building_code"`
	Slot         int       `db:"slot"`
	BuiltAt      time.Time `db:"built_at"`
}

func (FarmBuilding) TableName() string { return TableFarmBuilding }

// FarmAnimal 動物：fed_at／ready_at 同時為空表示待餵食；ready_at 到了才能收集，收集後清空。
//
// 約束：uq_farm_animals_farm_animal_no（farm_id, animal_no）、ck_farm_animals_animal_no_positive、
// ck_farm_animals_cycle_consistency（同時為 NULL，或 ready_at > fed_at）、ck_farm_animals_cycles_non_negative。
// 外鍵：animal_code → animal_types.code。
type FarmAnimal struct {
	ID              int64      `db:"id"`
	FarmID          int64      `db:"farm_id"`
	AnimalNo        int        `db:"animal_no"` // 農場內的動物編號
	AnimalCode      string     `db:"animal_code"`
	AcquiredAt      time.Time  `db:"acquired_at"`
	FedAt           *time.Time `db:"fed_at"`
	ReadyAt         *time.Time `db:"ready_at"`
	CyclesCompleted int        `db:"cycles_completed"` // server_default 0
}

func (FarmAnimal) TableName() string { return TableFarmAnimal }

// CraftOrder 加工訂單：開單時已扣除原料（記錄在 inputs）；status 只有 in_progress／collected，完成與否依 ready_at 判斷。
//
// 約束：ck_craft_orders_status_valid、ck_craft_orders_quantity_positive、ck_craft_orders_output_quantity_positive、
// ck_craft_orders_ready_after_start、ck_craft_orders_collected_consistency（collected ⇔ collected_at IS NOT NULL）、
// ix_craft_orders_farm_status、ix_craft_orders_started_at。
type CraftOrder struct {
	ID             int64          `db:"id"`
	FarmID         int64          `db:"farm_id"`
	RecipeCode     string         `db:"recipe_code"`
	BuildingCode   string         `db:"building_code"`
	Quantity       int            `db:"quantity"` // 份數
	Inputs         map[string]int `db:"inputs"`   // JSON 已扣除的原料 {代碼: 數量}
	OutputCode     string         `db:"output_code"`
	OutputQuantity int            `db:"output_quantity"` // 成品總數
	Status         string         `db:"status"`          // server_default 'in_progress'
	StartedAt      time.Time      `db:"started_at"`
	ReadyAt        time.Time      `db:"ready_at"`
	CollectedAt    *time.Time     `db:"collected_at"`
}

func (CraftOrder) TableName() string { return TableCraftOrder }
