package services

// 監控資料服務（對應 Python app/services/monitor_service.py）：只讀取 PostgreSQL，所有查詢都在唯讀交易中執行
// （SET TRANSACTION READ ONLY：資料庫層級保證監控端不會寫入任何遊戲資料）。
//
// 回傳值是給網站 JSON 用的 struct／map，欄位名稱與 Python dict 的鍵相同（json tag）。

import (
	"context"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/defenses"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/leaderboard"
	"farmgm/internal/repositories/monitor"
	"farmgm/internal/repositories/steals"
)

// ActivityHours 圖表的活動小時數（Python ACTIVITY_HOURS）。
const ActivityHours = 12

// MonitorService 監控網站的唯讀查詢。
type MonitorService struct {
	db    *db.DB
	clock clock.Clock
}

// NewMonitorService clk 為 nil 時使用系統時鐘。
func NewMonitorService(d *db.DB, clk clock.Clock) *MonitorService {
	if clk == nil {
		clk = clock.System{}
	}
	return &MonitorService{db: d, clock: clk}
}

// Now 目前時間（截到微秒，與 Python datetime 相同）。
func (m *MonitorService) Now() time.Time { return m.clock.Now().UTC().Truncate(time.Microsecond) }

func (m *MonitorService) readOnly(ctx context.Context, fn func(t *db.Tx) error) error {
	return m.db.InTx(ctx, db.TxOptions{ReadOnly: true}, fn)
}

func readOnlyValue[T any](ctx context.Context, m *MonitorService, fn func(t *db.Tx) (T, error)) (T, error) {
	var out T
	err := m.readOnly(ctx, func(t *db.Tx) error {
		v, err := fn(t)
		out = v
		return err
	})
	return out, err
}

// ---------- 個別查詢 ----------

func (m *MonitorService) Summary(ctx context.Context) (*monitor.Summary, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*monitor.Summary, error) {
		return monitor.GetSummary(ctx, t, now, taipeiDayStart(now), witherCutoff(now))
	})
}

// FarmList Python farms() 的回傳。
type FarmList struct {
	Items  []monitor.FarmRow `json:"items"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// Farms q 為 "" 表示不搜尋。
func (m *MonitorService) Farms(ctx context.Context, q string, limit, offset int) (FarmList, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (FarmList, error) {
		rows, total, err := monitor.Farms(ctx, t, now, witherCutoff(now), q, limit, offset)
		return FarmList{nonNil(rows), total, limit, offset}, err
	})
}

func (m *MonitorService) Transactions(ctx context.Context, limit int) ([]monitor.TransactionRow, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]monitor.TransactionRow, error) {
		rows, err := monitor.Transactions(ctx, t, limit, nil)
		return nonNil(rows), err
	})
}

// MonitorPlot 農場明細的一格土地。
type MonitorPlot struct {
	PlotNumber        int        `json:"plot_number"`
	CropCode          *string    `json:"crop_code"`
	CropName          *string    `json:"crop_name"`
	PlantedAt         *time.Time `json:"planted_at"`
	WateredAt         *time.Time `json:"watered_at"`
	ReadyAt           *time.Time `json:"ready_at"`
	WitherAt          *time.Time `json:"wither_at"`
	RemainingQuantity *int       `json:"remaining_quantity"`
	Fertilized        bool       `json:"fertilized"`
	RemainingSeconds  int        `json:"remaining_seconds"`
	State             string     `json:"state"`
	StateLabel        string     `json:"state_label"`
}

type MonitorItem struct {
	ItemCode string `json:"item_code"`
	Quantity int    `json:"quantity"`
}

// FarmDetailView Python farm_detail() 的回傳。
type FarmDetailView struct {
	FarmID            int64                    `json:"farm_id"`
	PlayerID          int64                    `json:"player_id"`
	PlayerName        string                   `json:"player_name"`
	FarmName          string                   `json:"farm_name"`
	Level             int                      `json:"level"`
	Gold              int                      `json:"gold"`
	WarehouseUsed     int                      `json:"warehouse_used"` // 含賭場獎品、不含素材背包
	WarehouseCapacity int                      `json:"warehouse_capacity"`
	UpdatedAt         time.Time                `json:"updated_at"`
	Plots             []MonitorPlot            `json:"plots"`
	Inventory         []MonitorItem            `json:"inventory"`
	Transactions      []monitor.TransactionRow `json:"transactions"`
	ServerTime        time.Time                `json:"server_time"`
}

// FarmDetail 不存在時回傳 nil。
func (m *MonitorService) FarmDetail(ctx context.Context, farmID int64) (*FarmDetailView, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*FarmDetailView, error) {
		found, err := monitor.FarmDetail(ctx, t, farmID)
		if err != nil || found == nil {
			return nil, err
		}
		plots, err := monitor.Plots(ctx, t, farmID)
		if err != nil {
			return nil, err
		}
		v := &FarmDetailView{FarmID: found.ID, PlayerID: found.PlayerID, PlayerName: found.PlayerName,
			FarmName: found.Name, Level: found.Level, Gold: found.Gold, WarehouseCapacity: found.WarehouseCapacity,
			UpdatedAt: found.UpdatedAt, Plots: []MonitorPlot{}, Inventory: []MonitorItem{}, ServerTime: now}
		for _, pc := range plots {
			p := pc.Plot
			state := rules.PlotStateAt(p.CropTypeID, p.WateredAt, p.ReadyAt, now)
			row := MonitorPlot{PlotNumber: p.PlotNumber, PlantedAt: p.PlantedAt, WateredAt: p.WateredAt, ReadyAt: p.ReadyAt,
				RemainingQuantity: p.RemainingQuantity, Fertilized: p.Fertilized,
				RemainingSeconds: rules.RemainingSeconds(p.ReadyAt, now), State: string(state), StateLabel: rules.PlotStateLabels[state]}
			if pc.Crop != nil {
				row.CropCode, row.CropName = &pc.Crop.Code, &pc.Crop.Name
			}
			if p.ReadyAt != nil {
				w := rules.WitherTime(*p.ReadyAt)
				row.WitherAt = &w
			}
			v.Plots = append(v.Plots, row)
		}
		items, err := monitor.Inventory(ctx, t, farmID)
		if err != nil {
			return nil, err
		}
		for _, i := range items {
			v.Inventory = append(v.Inventory, MonitorItem{i.ItemCode, i.Quantity})
		}
		if v.WarehouseUsed, err = inventory.TotalQuantity(ctx, t, farmID); err != nil {
			return nil, err
		}
		id := found.ID
		txs, err := monitor.Transactions(ctx, t, 20, &id)
		v.Transactions = nonNil(txs)
		return v, err
	})
}

type ActivityPoint struct {
	Hour         time.Time `json:"hour"`
	Transactions int       `json:"transactions"`
	TotalGold    int       `json:"total_gold"`
}

type Charts struct {
	Crops      []monitor.CropDist `json:"crops"`
	Activity   []ActivityPoint    `json:"activity"`
	HasHistory bool               `json:"has_history"`
}

func (m *MonitorService) Charts(ctx context.Context) (Charts, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (Charts, error) { return charts(ctx, t, now) })
}

func charts(ctx context.Context, t *db.Tx, now time.Time) (Charts, error) {
	currentHour := now.Truncate(time.Hour)
	since := currentHour.Add(-(ActivityHours - 1) * time.Hour)
	hours, err := monitor.HourlyActivity(ctx, t, since)
	if err != nil {
		return Charts{}, err
	}
	byHour := map[int64]monitor.HourActivity{}
	recent := 0
	for _, h := range hours {
		byHour[h.Hour.Unix()] = h
		recent += h.Gold
	}
	// 金幣趨勢：所有交易的 gold_delta 加總 = 全服金幣，因此由目前 SUM(farms.gold) 往回扣除近 12 小時的變化即可
	total, err := monitor.TotalGold(ctx, t)
	if err != nil {
		return Charts{}, err
	}
	running := total - recent
	activity := make([]ActivityPoint, 0, ActivityHours)
	for i := range ActivityHours {
		hour := since.Add(time.Duration(i) * time.Hour)
		h := byHour[hour.Unix()]
		running += h.Gold
		activity = append(activity, ActivityPoint{hour, h.Count, running})
	}
	crops, err := monitor.CropDistribution(ctx, t, now, witherCutoff(now))
	if err != nil {
		return Charts{}, err
	}
	has, err := monitor.HasTransactions(ctx, t)
	return Charts{nonNil(crops), activity, has}, err
}

// ---------- WebSocket 快照 ----------

// DataVersion 資料有沒有變的指標（幾個主鍵索引的最大值；NULL 為 0）：遊戲交易、偷菜、管理操作、釣魚。
type DataVersion [4]int64

func (m *MonitorService) DataVersion(ctx context.Context) (DataVersion, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) (DataVersion, error) {
		var v DataVersion
		err := t.QueryRow(ctx, `SELECT coalesce((SELECT max(id) FROM game_transactions), 0), coalesce((SELECT max(id) FROM steal_records), 0),
			coalesce((SELECT max(id) FROM admin_actions), 0), coalesce((SELECT max(id) FROM fishing_sessions), 0)`).
			Scan(&v[0], &v[1], &v[2], &v[3])
		return v, err
	})
}

type SnapshotFarms struct {
	Items []monitor.FarmRow `json:"items"`
	Total int               `json:"total"`
}

// Snapshot WebSocket 推送的監控快照。
type Snapshot struct {
	ServerTime   time.Time                `json:"server_time"`
	GeneratedAt  time.Time                `json:"generated_at"` // 實際查詢資料庫的時間（沿用時不變，前端據此判斷要不要重繪）
	Summary      *monitor.Summary         `json:"summary"`
	Farms        SnapshotFarms            `json:"farms"`
	Transactions []monitor.TransactionRow `json:"transactions"`
	Charts       Charts                   `json:"charts"`
}

func (m *MonitorService) Snapshot(ctx context.Context, farmLimit int) (*Snapshot, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*Snapshot, error) {
		rows, total, err := monitor.Farms(ctx, t, now, witherCutoff(now), "", farmLimit, 0)
		if err != nil {
			return nil, err
		}
		s := &Snapshot{ServerTime: now, GeneratedAt: now, Farms: SnapshotFarms{nonNil(rows), total}}
		if s.Summary, err = monitor.GetSummary(ctx, t, now, taipeiDayStart(now), witherCutoff(now)); err != nil {
			return nil, err
		}
		txs, err := monitor.Transactions(ctx, t, 20, nil)
		if err != nil {
			return nil, err
		}
		s.Transactions = nonNil(txs)
		s.Charts, err = charts(ctx, t, now)
		return s, err
	})
}

// ---------- 群組、偷菜、排行榜、背包（唯讀） ----------

func (m *MonitorService) Players(ctx context.Context) ([]monitor.PlayerRow, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]monitor.PlayerRow, error) {
		rows, err := monitor.Players(ctx, t, 500)
		return nonNil(rows), err
	})
}

type MonitorGroup struct {
	GroupID        int64     `json:"group_id"`
	Name           string    `json:"name"`
	DiscordGuildID *string   `json:"discord_guild_id"`
	MemberCount    int       `json:"member_count"`
	CreatedAt      time.Time `json:"created_at"`
}

func (m *MonitorService) Groups(ctx context.Context) ([]MonitorGroup, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]MonitorGroup, error) {
		rows, err := groups.ListWithCounts(ctx, t, nil)
		out := []MonitorGroup{}
		for _, g := range rows {
			out = append(out, MonitorGroup{g.Group.ID, g.Group.Name, g.Group.DiscordGuildID, g.MemberCount, g.Group.CreatedAt})
		}
		return out, err
	})
}

type MonitorGroupDetail struct {
	GroupID        int64           `json:"group_id"`
	Name           string          `json:"name"`
	DiscordGuildID *string         `json:"discord_guild_id"`
	CreatedAt      time.Time       `json:"created_at"`
	Members        []groups.Member `json:"members"`
}

// GroupDetail 不存在時回傳 nil。
func (m *MonitorService) GroupDetail(ctx context.Context, groupID int64) (*MonitorGroupDetail, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*MonitorGroupDetail, error) {
		g, err := groups.Get(ctx, t, groupID)
		if err != nil || g == nil {
			return nil, err
		}
		members, err := groups.MembersWithFarms(ctx, t, groupID, now, witherCutoff(now))
		return &MonitorGroupDetail{g.ID, g.Name, g.DiscordGuildID, g.CreatedAt, nonNil(members)}, err
	})
}

// StealView 偷菜紀錄（機率與亂數只留在資料庫供稽核，不輸出 block_chance）。
type StealView struct {
	ID             int64     `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	GroupID        int64     `json:"group_id"`
	GroupName      string    `json:"group_name"`
	ThiefPlayerID  int64     `json:"thief_player_id"`
	ThiefName      string    `json:"thief_name"`
	VictimPlayerID int64     `json:"victim_player_id"`
	VictimName     string    `json:"victim_name"`
	CropName       string    `json:"crop_name"`
	CropCode       string    `json:"crop_code"`
	Quantity       int       `json:"quantity"`
	PlotNumber     int       `json:"plot_number"`
	Outcome        string    `json:"outcome"`
	BlockedBy      *string   `json:"blocked_by"`
	BlockedByName  *string   `json:"blocked_by_name"`
}

// StealQuery Steals 的篩選條件；nil／"" 表示不篩選。
type StealQuery struct {
	Limit             int
	GroupID, PlayerID *int64
	Outcome           string
	Since, Until      *time.Time
}

func (m *MonitorService) Steals(ctx context.Context, q StealQuery) ([]StealView, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]StealView, error) {
		rows, err := steals.Search(ctx, t, steals.SearchFilter{Limit: q.Limit, GroupID: q.GroupID, PlayerID: q.PlayerID,
			Outcome: q.Outcome, Since: q.Since, Until: q.Until})
		out := []StealView{}
		for _, r := range rows {
			v := StealView{r.ID, r.CreatedAt, r.GroupID, r.GroupName, r.ThiefPlayerID, r.ThiefName, r.VictimPlayerID,
				r.VictimName, r.CropName, r.CropCode, r.Quantity, r.PlotNumber, r.Outcome, r.BlockedBy, nil}
			if r.BlockedBy != nil && *r.BlockedBy != "" {
				name := pets.BlockerName(*r.BlockedBy)
				v.BlockedByName = &name
			}
			out = append(out, v)
		}
		return out, err
	})
}

type MonitorDefenseDevice struct {
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Slot       int       `json:"slot"`
	Chance     float64   `json:"chance"`
	StartPlot  *int      `json:"start_plot"`
	EndPlot    *int      `json:"end_plot"`
	AcquiredAt time.Time `json:"acquired_at"`
}

type FarmDefenses struct {
	FarmID     int64                  `json:"farm_id"`
	FarmName   string                 `json:"farm_name"`
	PlayerID   int64                  `json:"player_id"`
	PlayerName string                 `json:"player_name"`
	Plots      int                    `json:"plots"`
	Devices    []MonitorDefenseDevice `json:"devices"`
}

// Defenses 各農場持有的防守設施與守護範圍。
func (m *MonitorService) Defenses(ctx context.Context) ([]*FarmDefenses, error) {
	return readOnlyValue(ctx, m, func(t *db.Tx) ([]*FarmDefenses, error) {
		rows, err := defenses.AllWithOwners(ctx, t)
		if err != nil {
			return nil, err
		}
		out := []*FarmDefenses{}
		byFarm := map[int64]*FarmDefenses{}
		for _, r := range rows {
			entry := byFarm[r.Farm.ID]
			if entry == nil {
				total, err := farms.CountPlots(ctx, t, r.Farm.ID)
				if err != nil {
					return nil, err
				}
				entry = &FarmDefenses{r.Farm.ID, r.Farm.Name, r.Farm.PlayerID, r.PlayerName, total, []MonitorDefenseDevice{}}
				byFarm[r.Farm.ID] = entry
				out = append(out, entry)
			}
			spec := balance.Defenses[r.Defense.DeviceCode]
			d := MonitorDefenseDevice{r.Defense.DeviceCode, spec.Name, r.Defense.Slot, spec.BlockChance, r.Defense.StartPlot, nil,
				r.Defense.AcquiredAt}
			if spec.Range != nil && r.Defense.StartPlot != nil {
				end := min(*r.Defense.StartPlot+*spec.Range-1, entry.Plots)
				d.EndPlot = &end
			}
			entry.Devices = append(entry.Devices, d)
		}
		return out, nil
	})
}

type LeaderboardView struct {
	Scope       string     `json:"scope"`
	GroupID     *int64     `json:"group_id"`
	Kind        string     `json:"kind"`
	Period      string     `json:"period"`
	PeriodStart *time.Time `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end"`
	Items       any        `json:"items"` // []leaderboard.Row 或 []leaderboard.GroupRow
}

// Leaderboard scope：global（個人總排行）、group（單一群組內）、groups（群組總排行）；scope 為 "" 時依 groupID 決定。
// period：all／day／week／month。scope 為 group 且群組不存在時回傳 nil。
func (m *MonitorService) Leaderboard(ctx context.Context, kind string, groupID *int64, limit int, scope, period string) (*LeaderboardView, error) {
	if scope == "" {
		scope = "global"
		if groupID != nil {
			scope = "group"
		}
	}
	since, until, err := ViewBounds(period, m.Now())
	if err != nil {
		return nil, err
	}
	return readOnlyValue(ctx, m, func(t *db.Tx) (*LeaderboardView, error) {
		v := &LeaderboardView{Scope: scope, Kind: kind, Period: period, PeriodStart: since, PeriodEnd: until}
		if scope == "group" {
			if groupID == nil {
				return nil, nil
			}
			g, err := groups.Get(ctx, t, *groupID)
			if err != nil || g == nil {
				return nil, err
			}
			v.GroupID = groupID
		}
		if scope == "groups" {
			rows, err := leaderboard.GroupRanking(ctx, t, kind, limit, since, until)
			v.Items = nonNil(rows)
			return v, err
		}
		rows, err := leaderboard.Ranking(ctx, t, kind, v.GroupID, limit, since, until)
		v.Items = nonNil(rows)
		return v, err
	})
}

// BackpackJSON 背包的 JSON（Python vars(dataclass) 的鍵）。
type BackpackLineJSON struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Quantity    int    `json:"quantity"`
	Price       int    `json:"price"`
	Description string `json:"description"`
	Quality     string `json:"quality"`
	Mutation    string `json:"mutation"`
}

type ToolJSON struct {
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	Owned       bool       `json:"owned"`
	Enabled     bool       `json:"enabled"`
	AcquiredAt  *time.Time `json:"acquired_at"`
	Description string     `json:"description"`
}

type EffectJSON struct {
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	Status           string     `json:"status"`
	StartedAt        *time.Time `json:"started_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RemainingSeconds int        `json:"remaining_seconds"`
	Multiplier       *float64   `json:"multiplier"`
	Description      string     `json:"description"`
}

type MonitorBackpack struct {
	PlayerID    int64              `json:"player_id"`
	FarmName    string             `json:"farm_name"`
	Used        int                `json:"used"`
	Capacity    int                `json:"capacity"`
	IsEmpty     bool               `json:"is_empty"`
	Seeds       []BackpackLineJSON `json:"seeds"`
	Produce     []BackpackLineJSON `json:"produce"`
	Consumables []BackpackLineJSON `json:"consumables"`
	Goods       []BackpackLineJSON `json:"goods"`
	Materials   []BackpackLineJSON `json:"materials"`
	Tools       []ToolJSON         `json:"tools"`
	Effects     []EffectJSON       `json:"effects"`
	ServerTime  time.Time          `json:"server_time"`
}

func linesJSON(ls []BackpackLine) []BackpackLineJSON {
	out := make([]BackpackLineJSON, len(ls))
	for i, l := range ls {
		out[i] = BackpackLineJSON(l)
	}
	return out
}

// Backpack 玩家沒有農場時回傳 nil。
func (m *MonitorService) Backpack(ctx context.Context, playerID int64) (*MonitorBackpack, error) {
	now := m.Now()
	return readOnlyValue(ctx, m, func(t *db.Tx) (*MonitorBackpack, error) {
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil || farm == nil {
			return nil, err
		}
		bp, err := BuildBackpack(ctx, t, farm, now)
		if err != nil {
			return nil, err
		}
		v := &MonitorBackpack{PlayerID: playerID, FarmName: farm.Name, Used: bp.Used, Capacity: bp.Capacity, IsEmpty: bp.IsEmpty(),
			Seeds: linesJSON(bp.Seeds), Produce: linesJSON(bp.Produce), Consumables: linesJSON(bp.Consumables),
			Goods: linesJSON(bp.Goods), Materials: linesJSON(bp.Materials), Tools: []ToolJSON{}, Effects: []EffectJSON{}, ServerTime: now}
		for _, x := range bp.Tools {
			v.Tools = append(v.Tools, ToolJSON(x))
		}
		for _, x := range bp.Effects {
			v.Effects = append(v.Effects, EffectJSON(x))
		}
		return v, nil
	})
}

// nonNil 空切片輸出 JSON 時是 []（Python 的空 list），不是 null。
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
