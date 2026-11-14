package services

// 服務層回傳的資料物件（對應 Python app/services/dto.py）：與 SQL、CMD、Web、Discord 均無耦合。
// Python 的 None 以 nil 指標表示。

import (
	"time"

	"farmgm/internal/game/rules"
)

type PlayerInfo struct {
	ID               int64
	DisplayName      string
	HasFarm          bool
	ExternalPlatform *string
	ExternalUserID   *string
}

type CropInfo struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	SeedPrice       int    `json:"seed_price"`
	GrowSeconds     int    `json:"grow_seconds"`
	HarvestQuantity int    `json:"harvest_quantity"`
	SellPrice       int    `json:"sell_price"`
	ShopBuyable     bool   `json:"shop_buyable"` // false：稀有作物，商店不販售種子
	SeasonText      string `json:"season_text"`  // 季節／時段限定的說明（沒有限制時為空字串）
	InSeason        bool   `json:"in_season"`    // 現在可不可以種
	// SeasonSellPct 目前的季節售價倍率（%，當季或不限季節 100；Go 版新增）；SellPrice 已套用
	SeasonSellPct int `json:"season_sell_pct"`
}

type PlotInfo struct {
	PlotNumber        int
	State             rules.PlotState
	CropCode          *string
	CropName          *string
	PlantedAt         *time.Time
	ReadyAt           *time.Time
	RemainingSeconds  int
	WateredAt         *time.Time
	RemainingQuantity *int
	Fertilized        bool
	Quality           *string
	Mutation          *string
}

type InventoryEntry struct {
	ItemCode    string
	DisplayName string
	Quantity    int
}

type Inventory struct {
	Items    []InventoryEntry
	Used     int
	Capacity int
}

type FarmStatus struct {
	FarmID            int64
	PlayerID          int64
	HomeGroupID       *int64
	PlayerName        string
	Name              string
	Level             int
	Gold              int
	WarehouseUsed     int
	WarehouseCapacity int
	Plots             []PlotInfo
	Now               time.Time
	Title             *string        // 目前選用的稱號
	Pets              map[string]int // 寵物代碼 → 等級（農場圖左上、右上顯示）
}

func (f FarmStatus) countState(state rules.PlotState) int {
	n := 0
	for _, p := range f.Plots {
		if p.State == state {
			n++
		}
	}
	return n
}

func (f FarmStatus) EmptyPlots() int   { return f.countState(rules.PlotEmpty) }
func (f FarmStatus) GrowingPlots() int { return f.countState(rules.PlotGrowing) }
func (f FarmStatus) ReadyPlots() int   { return f.countState(rules.PlotReady) }

// HarvestDetail 收成一格的明細（只用於產生操作結果訊息）。
type HarvestDetail struct {
	Name     string // 含品質／突變標籤，例如「小麥【稀有】」
	Quantity int
	Quality  string
	Mutation string
	CatBonus int
	Stolen   int
	Lost     int
	Value    int // 依目前售價估計的價值；自動販售時為實際收入
	Sold     bool
}

// ActionResult 一次成功的遊戲操作結果。ItemCode 為 "" 表示 None。
type ActionResult struct {
	Message   string
	Gold      int
	GoldDelta int
	ItemCode  string
	Quantity  int
	Success   bool // 例如偷菜被防守阻止時為 false（操作本身已完成並記錄）
}

// result 建立成功的 ActionResult（Python 的預設 success=True）。
func result(message string, gold, goldDelta int, itemCode string, quantity int) ActionResult {
	return ActionResult{Message: message, Gold: gold, GoldDelta: goldDelta, ItemCode: itemCode, Quantity: quantity, Success: true}
}

type TransactionInfo struct {
	CreatedAt   time.Time
	EventType   string
	ItemCode    *string
	Quantity    int
	GoldDelta   int
	Description string
}

// ---------- 背包 ----------

type BackpackLine struct {
	Code        string
	Name        string
	Quantity    int
	Price       int // 種子：商店售價；農產品：依品質與突變計算的單價；道具：商店售價
	Description string
	Quality     string // 預設 normal
	Mutation    string // 預設 none
}

func line(code, name string, quantity, price int, desc string) BackpackLine {
	return BackpackLine{code, name, quantity, price, desc, "normal", "none"}
}

type ToolStatus struct {
	Code        string
	Name        string
	Owned       bool
	Enabled     bool
	AcquiredAt  *time.Time
	Description string
}

type EffectStatus struct {
	Code             string
	Name             string
	Status           string // inactive / active / expired
	StartedAt        *time.Time
	ExpiresAt        *time.Time
	RemainingSeconds int
	Multiplier       *float64
	Description      string
}

var EffectStatusLabels = map[string]string{"inactive": "未啟用", "active": "有效中", "expired": "已過期"}

// PrizeView 賭場獎品（顯示用）。
type PrizeView struct {
	ID         int64  `json:"id"`
	ItemCode   string `json:"item_code"`
	CropCode   string `json:"crop_code"`
	Quality    string `json:"quality"`
	Mutation   string `json:"mutation"`
	Multiplier int    `json:"multiplier"`
	Quantity   int    `json:"quantity"`
	Name       string `json:"name"`
	UnitPrice  int    `json:"unit_price"` // 價目表單價 × 倍率（不含活動加成）
}

type Backpack struct {
	PlayerID         int64
	Seeds            []BackpackLine
	Produce          []BackpackLine
	Consumables      []BackpackLine
	Tools            []ToolStatus
	Effects          []EffectStatus
	Used             int
	Capacity         int
	Now              time.Time
	Goods            []BackpackLine // 動物產品與加工品
	Prizes           []PrizeView    // 賭場獎品（價值倍率 ×2／×5）
	Materials        []BackpackLine // 山洞探索素材與假幣（Price 為假幣兌換價，素材為 0）
	MaterialUsed     int            // 素材背包（不佔倉庫）
	MaterialCapacity int
	SeedMaker        bool // 已製作種子製造機（農田頁顯示「🌱 種子製造」入口）
}

func (b Backpack) IsEmpty() bool {
	if len(b.Seeds) > 0 || len(b.Produce) > 0 || len(b.Consumables) > 0 || len(b.Goods) > 0 ||
		len(b.Prizes) > 0 || len(b.Materials) > 0 {
		return false
	}
	for _, t := range b.Tools {
		if t.Owned {
			return false
		}
	}
	for _, e := range b.Effects {
		if e.Status != "inactive" {
			return false
		}
	}
	return true
}

// ---------- 群組與偷菜 ----------

type GroupInfo struct {
	ID             int64
	Name           string
	DiscordGuildID *string
	MemberCount    int
	CreatedAt      time.Time
	IsMember       bool
}

type StealTarget struct {
	PlayerID          int64
	PlayerName        string
	FarmName          string
	PlotNumber        int
	CropName          string
	RemainingQuantity int
	MaxSteal          int
}

// StealFarmView 偷菜時查看同群組成員的農田：只有土地狀態，不含金幣、倉庫等資訊。
type StealFarmView struct {
	PlayerID      int64
	PlayerName    string
	FarmName      string
	Plots         []PlotInfo
	Now           time.Time
	Targets       []StealTarget // 這次可以偷的土地
	LimitReached  bool          // 今天已經偷過這位玩家（每日土地上限）
	WarehouseFull bool          // 偷菜者的倉庫已滿
	VictimFull    bool          // 這位玩家今天被偷的人數已達上限（每日人數上限）
	Glove         bool          // 偷菜者啟用了傳說扒手手套（不受每日土地與人數上限）
}

// ExternalContext 外部平台（Discord）一次互動對應的群組與玩家；ID 皆由平台互動來源取得，不接受玩家輸入。
type ExternalContext struct {
	GroupID    *int64
	GroupName  *string
	PlayerID   *int64
	PlayerName *string
	HasFarm    bool
}

// ---------- GameService 其他回傳（Python 以 dict 回傳） ----------

// TransferView 群組轉移狀態（Python _transfer_view 的 dict）。
type TransferView struct {
	FarmID           int64
	PlayerID         int64
	HomeGroupID      *int64
	HomeGroupName    *string
	TargetGroupID    *int64
	TargetGroupName  *string
	IsMember         bool
	LastTransferAt   *time.Time
	NextTransferAt   *time.Time // 冷卻中才有值
	RemainingSeconds int
	CanTransfer      bool
	Now              time.Time
}

// GambleOption 背包中可以下注的作物。
type GambleOption struct {
	ItemCode  string
	CropCode  string
	Quality   string
	Mutation  string
	Quantity  int
	Name      string
	UnitPrice int
}

// GambleResult 一次下注的結果。
type GambleResult struct {
	Roll       int
	Won        bool
	Multiplier int
	Quantity   int
	Name       string
	Message    string
}

// CropCatalogEntry 作物的基本資料、珍稀變種與各品質／突變的單價（Python crop_catalog 的 dict）。
type CropCatalogEntry struct {
	CropInfo
	Active          bool                      `json:"active"`
	VariantCode     string                    `json:"variant_code"`
	VariantName     string                    `json:"variant_name"`
	Prices          map[string]map[string]int `json:"prices"` // 品質 → 突變（none／golden／variant）→ 單價
	FastGrowSeconds int                       `json:"fast_grow_seconds"`
	GiantQuantity   int                       `json:"giant_quantity"`
}

// QualityTable 品質機率與售價倍率。
type QualityTable struct {
	BaseBP          map[string]int
	PricePct        map[string]int
	Level           *int           // 指定玩家時的農場等級
	EffectiveBP     map[string]int // 依農場等級加成後的實際機率；未指定玩家時為 nil
	LevelBonusBP    int
	LevelBonusMaxBP int
}

// MutationRow 突變機率與效果。
type MutationRow struct {
	Code   string
	Name   string
	BP     int
	Effect string
}

// ActiveEventView 生效中的限時活動（events.View 再加上限定作物名稱）。
type ActiveEventView struct {
	View     map[string]any
	CropName *string
}
