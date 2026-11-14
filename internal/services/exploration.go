package services

// 山洞冒險：區域探索、古代遺跡（群組修復與守護者）、機器製作、假幣兌換、種子製造機、稀有作物培育器與自動收割／加工
// （對應 Python app/services/exploration_service.py）。
//
// 探險：出發時決定獎勵，探險時間結束後領取才放進背包，一次只能去一個區域。
// 所有會改變資產的操作都先鎖定農場資料列，等級、進行中的探險、素材與倉庫空間都在同一筆交易中檢查，
// 重複點擊或並行請求不會重複發獎或重複扣料。
//
// 與 Python 不同：exploration_logs.rewards 是 json 欄位（保留鍵順序），Go 的 map 沒有順序，
// 所以寫入用 AddLogOrdered、讀取用 RewardOrders 取回順序，訊息中的物品順序與 Python 相同。

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/ruins"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/codex"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/ranch"
	"farmgm/internal/repositories/titles"

	explrepo "farmgm/internal/repositories/exploration"
	ruinsrepo "farmgm/internal/repositories/ruins"
)

const incubator = "rare_incubator"

// ExplorationService = RanchService + 山洞冒險。
type ExplorationService struct {
	*RanchService
}

func NewExplorationService(d *db.DB, opt Options) *ExplorationService {
	return &ExplorationService{NewRanchService(d, opt)}
}

func (x *ExplorationService) InGroupContext(groupID *int64) *ExplorationService {
	return &ExplorationService{x.RanchService.InGroupContext(groupID)}
}

// autoHarvestHook 測試用：自動收割後呼叫，回傳錯誤模擬機器故障（對應 Python 測試的 monkeypatch）。
var autoHarvestHook func() error

func (x *ExplorationService) names(ctx context.Context, t *db.Tx, codes []string) (map[string]string, error) {
	all, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(codes))
	for _, c := range codes {
		if n, ok := all[c]; ok {
			out[c] = n
		} else {
			out[c] = exploration.MaterialName(c)
		}
	}
	return out, nil
}

// joinItems 「名稱 ×數量」依 order 的順序。
func joinItems(order []string, items map[string]int, names map[string]string) string {
	parts := make([]string, 0, len(order))
	for _, c := range order {
		n, ok := names[c]
		if !ok {
			n = c
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", n, items[c]))
	}
	return strings.Join(parts, "、")
}

// FindMachine 依代碼或名稱找機器；找不到時回傳 ""。
func FindMachine(key string) string {
	key = strings.TrimSpace(key)
	for _, code := range exploration.MachineOrder {
		if strings.ToLower(key) == code || key == exploration.Machines[code].Name {
			return code
		}
	}
	return ""
}

func FindIncubatorRecipe(key string) string {
	key = strings.TrimSpace(key)
	for _, code := range exploration.IncubatorRecipeOrder {
		r := exploration.IncubatorRecipes[code]
		if strings.ToLower(key) == code || key == r.Name || slices.Contains(r.Aliases, key) {
			return code
		}
	}
	return ""
}

// homeGroupOf 只讀農場的所屬群組欄位、不載入農場（之後 FOR UPDATE 載入時才是鎖定後的最新值）。
// 沒有農場時 found = false。
func homeGroupOf(ctx context.Context, t *db.Tx, playerID int64) (group *int64, found bool, err error) {
	return db.QueryValue[*int64](ctx, t, "SELECT home_group_id FROM farms WHERE player_id = $1", playerID)
}

// ---------- 回傳資料（Python 以 dict 回傳） ----------

type TripView struct {
	Area             string
	AreaName         string
	Emoji            string
	StartedAt        time.Time
	EndsAt           time.Time
	RemainingSeconds int
	Finished         bool
}

type AreaEventView struct {
	Name     string
	MinLevel int
}

type AreaView struct {
	Code            string
	Emoji           string
	Name            string
	Description     string
	UnlockLevel     int
	Unlocked        bool
	NeedsRuins      bool
	RuinsOpen       bool
	DurationSeconds int
	Ready           bool
	Exploring       bool
	NextAt          *time.Time
	Advanced        bool
	AdvancedLevel   int
	Rolls           int
	ExploreCount    int
	Common          []string
	Rare            []string
	Seeds           []string
	Events          []AreaEventView
	LevelNotes      []exploration.LevelNote
}

type ExplorationOverview struct {
	PlayerName string
	Level      int
	Areas      []AreaView
	Trip       *TripView
	Now        time.Time
}

// ExploreTrip 出發探險的結果（一般區域）。
type ExploreTrip struct {
	LogID           int64
	Area            string
	AreaName        string
	Emoji           string
	Advanced        bool
	Level           int
	StartedAt       time.Time
	EndsAt          time.Time
	DurationSeconds int
}

type ExploreItem struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Rarity   string `json:"rarity"`
}

// ExploreReward 領取探險獎勵的結果；古代遺跡（立即領取）另外填 Instant／NextAt／ExtraRolls／BonusRolls。
type ExploreReward struct {
	LogID        int64
	Area         string
	AreaName     string
	Emoji        string
	Advanced     bool
	Level        int
	Items        []ExploreItem
	Events       []string
	Seeds        map[string]int // 稀有種子名稱 → 數量
	FakeCoins    int
	StartedAt    time.Time
	EndsAt       time.Time
	GuardianNote string

	Instant    bool
	NextAt     *time.Time
	ExtraRolls int
	BonusRolls int
}

// ExploreOutcome Explore 的結果：一般區域只有 Trip（之後領取），古代遺跡只有 Reward（立即領取）。
type ExploreOutcome struct {
	Trip   *ExploreTrip
	Reward *ExploreReward
}

type NamedQty struct {
	Name     string
	Quantity int
}

type ExplorationLogView struct {
	Area            string
	AreaName        string
	Emoji           string
	Advanced        bool
	Level           int
	Rewards         []NamedQty // 依掉落順序
	Events          []string
	ExploredAt      time.Time
	NextAvailableAt time.Time
	ClaimedAt       *time.Time
}

type RuinsMaterial struct {
	Code    string
	Name    string
	Need    int
	Donated int
	Have    int
}

type GuardianWeapon struct {
	Code   string
	Name   string
	Have   int
	Damage int
}

type GuardianView struct {
	Name             string
	Active           bool
	AppearsAt        time.Time
	EndsAt           time.Time
	HPMax            int
	Damage           int
	Defeated         bool
	Mine             int
	Top              []ruinsrepo.Hit
	Weapons          []GuardianWeapon
	ExpeditionDamage int
	BonusRolls       int
}

type RuinsStatus struct {
	GroupID          *int64
	GroupName        *string
	Stage            int
	Stages           int
	Completed        bool
	CompletedAt      *time.Time
	StageName        *string
	StageDescription *string
	Percent          int
	Materials        []RuinsMaterial
	Mine             int
	Top              []ruinsrepo.Contributor
	Title            string
	StageNames       []string
	CollapsedAt      *time.Time
	Guardian         *GuardianView
}

type MachineMaterial struct {
	Name string
	Need int
	Have int
}

type MachineView struct {
	Code        string
	Emoji       string
	Name        string
	Description string
	UnlockLevel int
	Unlocked    bool
	Owned       bool
	Enabled     bool
	Settings    map[string]any
	Materials   []MachineMaterial
	Missing     map[string]int // 名稱 → 還差幾個
}

type MachinesStatus struct {
	Level    int
	Machines []MachineView
}

type IncubatorRecipeView struct {
	Code           string
	Name           string
	Inputs         map[string]int // 名稱 → 數量
	InputOrder     []string       // Inputs 的顯示順序（名稱）
	OutputName     string
	OutputQuantity int
	Seconds        int
}

// ---------- 冒險總覽與探險 ----------

func tripView(log *models.ExplorationLog, now time.Time) *TripView {
	if log == nil {
		return nil
	}
	area := exploration.AreaOf(log.AreaCode)
	next := log.NextAvailableAt
	remaining := rules.RemainingSeconds(&next, now)
	return &TripView{log.AreaCode, area.Name, area.Emoji, log.ExploredAt, log.NextAvailableAt, remaining, remaining == 0}
}

func ruinsReady(row *models.FarmExploration, now time.Time) bool {
	return row == nil || !now.Before(row.NextAvailableAt)
}

func (x *ExplorationService) ExplorationOverview(ctx context.Context, playerID int64) (ExplorationOverview, error) {
	now := x.now()
	return inTx(ctx, x.GameService, func(t *db.Tx) (ExplorationOverview, error) {
		farm, err := x.farmOf(ctx, t, playerID)
		if err != nil {
			return ExplorationOverview{}, err
		}
		player, err := players.Get(ctx, t, farm.PlayerID, false)
		if err != nil {
			return ExplorationOverview{}, err
		}
		counts, err := explrepo.ListCooldowns(ctx, t, farm.ID)
		if err != nil {
			return ExplorationOverview{}, err
		}
		active, err := explrepo.GetActive(ctx, t, farm.ID, false)
		if err != nil {
			return ExplorationOverview{}, err
		}
		trip := tripView(active, now)
		codes := []string{exploration.FakeCoin}
		for _, a := range exploration.Current().Areas {
			for _, pool := range a.Pools {
				for _, d := range pool {
					codes = append(codes, d.Item)
				}
			}
		}
		names, err := x.names(ctx, t, codes)
		if err != nil {
			return ExplorationOverview{}, err
		}
		if err := x.settleGuardian(ctx, t, farm.HomeGroupID); err != nil {
			return ExplorationOverview{}, err
		}
		ruinsOpen, err := ruinsrepo.IsCompleted(ctx, t, farm.HomeGroupID)
		if err != nil {
			return ExplorationOverview{}, err
		}
		ov := ExplorationOverview{PlayerName: player.DisplayName, Level: farm.Level, Trip: trip, Now: now}
		for _, code := range exploration.AreaOrder {
			area := exploration.AreaOf(code)
			row, hasRow := counts[code]
			unlocked := farm.Level >= area.UnlockLevel && (ruinsOpen || !area.NeedsRuins)
			pool := func(kind string) []string {
				out := []string{}
				for _, d := range area.Pools[kind] {
					if n := names[d.Item]; !slices.Contains(out, n) {
						out = append(out, n)
					}
				}
				return out
			}
			v := AreaView{Code: code, Emoji: area.Emoji, Name: area.Name, Description: area.Description,
				UnlockLevel: area.UnlockLevel, Unlocked: unlocked, NeedsRuins: area.NeedsRuins, RuinsOpen: ruinsOpen,
				DurationSeconds: area.DurationSeconds,
				Exploring:       trip != nil && trip.Area == code,
				Advanced:        exploration.IsAdvanced(area, farm.Level), AdvancedLevel: area.AdvancedLevel,
				Rolls: area.Rolls, Common: pool("common"), Rare: pool("rare"), Seeds: pool("seed"),
				LevelNotes: slices.Clone(area.LevelNotes)}
			// 一次只能進行一趟探險：有進行中或待領取的探險時，三個一般區域都不能出發；古代遺跡是額外的探索，只看自己的冷卻
			var rowPtr *models.FarmExploration
			if hasRow {
				rowPtr = &row
				v.ExploreCount = row.ExploreCount
			}
			if area.NeedsRuins {
				v.Ready = unlocked && ruinsReady(rowPtr, now)
				if hasRow {
					next := row.NextAvailableAt
					v.NextAt = &next
				}
			} else {
				v.Ready = unlocked && trip == nil
			}
			for _, e := range area.Events {
				v.Events = append(v.Events, AreaEventView{e.Name, e.MinLevel})
			}
			ov.Areas = append(ov.Areas, v)
		}
		return ov, nil
	})
}

// Explore 出發探險：獎勵在出發時決定並記錄，探險時間結束後用 ClaimExploration 領取。一次只能去一個區域。
// 古代遺跡另外處理（ExploreRuins）：不佔名額、立即拿到獎勵，回傳的是領取結果。
func (x *ExplorationService) Explore(ctx context.Context, playerID int64, areaKey string) (ExploreOutcome, error) {
	code := exploration.FindArea(areaKey)
	if code == "" {
		names := make([]string, len(exploration.AreaOrder))
		for i, c := range exploration.AreaOrder {
			names[i] = exploration.AreaOf(c).Name
		}
		return ExploreOutcome{}, gameerr.NotFoundf("沒有「%s」這個探索區域。可探索區域：%s", areaKey, strings.Join(names, "、"))
	}
	area := exploration.AreaOf(code)
	if area.NeedsRuins {
		r, err := x.ExploreRuins(ctx, playerID)
		if err != nil {
			return ExploreOutcome{}, err
		}
		return ExploreOutcome{Reward: &r}, nil
	}
	trip, err := inTx(ctx, x.GameService, func(t *db.Tx) (ExploreTrip, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ExploreTrip{}, err
		}
		if farm.Level < area.UnlockLevel {
			return ExploreTrip{}, gameerr.Validationf("%s需要農場 Lv.%d 才能探索（目前 Lv.%d）。", area.Name, area.UnlockLevel, farm.Level)
		}
		now := x.now()
		active, err := explrepo.GetActive(ctx, t, farm.ID, true)
		if err != nil {
			return ExploreTrip{}, err
		}
		if active != nil {
			busy := exploration.AreaOf(active.AreaCode).Name
			if !now.Before(active.NextAvailableAt) {
				return ExploreTrip{}, gameerr.Validationf("你從%s探險回來了，請先領取上一次的探險獎勵再出發。", busy)
			}
			next := active.NextAvailableAt
			left := rules.FormatDuration(rules.RemainingSeconds(&next, now))
			return ExploreTrip{}, gameerr.Validationf("你正在%s探險，還需要 %s才會回來；一次只能去一個區域。", busy, left)
		}
		res := exploration.Explore(code, farm.Level, x.exploreRng, 0)
		exploration.RollSawdust(res, x.exploreRng) // Go 版新增：每次探險另外帶回木屑
		until := now.Add(time.Duration(area.DurationSeconds) * time.Second)
		row, err := explrepo.GetCooldown(ctx, t, farm.ID, code, true)
		if err != nil {
			return ExploreTrip{}, err
		}
		if _, err := explrepo.SetCooldown(ctx, t, row, farm.ID, code, now, until); err != nil {
			return ExploreTrip{}, err
		}
		// (farm_id) WHERE claimed_at IS NULL 唯一：並行出發時第二筆在這裡失敗並回滾
		log, err := explrepo.AddLogOrdered(ctx, t, models.ExplorationLog{FarmID: farm.ID, AreaCode: code, FarmLevel: farm.Level,
			Advanced: res.Advanced, Rewards: res.Items, Events: res.Events, ExploredAt: now, NextAvailableAt: until}, res.ItemOrder)
		if err != nil {
			return ExploreTrip{}, err
		}
		return ExploreTrip{log.ID, code, area.Name, area.Emoji, res.Advanced, farm.Level, now, until, area.DurationSeconds}, nil
	})
	if err != nil {
		return ExploreOutcome{}, err
	}
	return ExploreOutcome{Trip: &trip}, nil
}

// ExploreRuins 探索古代遺跡（所屬群組修好後的額外探索）：不受一次一趟限制，立即拿到獎勵，冷卻 1 天。
// 掉落用古老森林模板再多抽 1–2 份；本週守護者已被打倒時再多抽 DefeatBonusRolls 份，並對守護者造成傷害。
func (x *ExplorationService) ExploreRuins(ctx context.Context, playerID int64) (ExploreReward, error) {
	area := exploration.AreaOf("ruins")
	return inTx(ctx, x.GameService, func(t *db.Tx) (ExploreReward, error) {
		group, _, err := homeGroupOf(ctx, t, playerID)
		if err != nil {
			return ExploreReward{}, err
		}
		if err := x.settleGuardian(ctx, t, group); err != nil { // 會鎖遺跡列，必須在鎖農場列之前
			return ExploreReward{}, err
		}
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ExploreReward{}, err
		}
		done, err := ruinsrepo.IsCompleted(ctx, t, farm.HomeGroupID)
		if err != nil {
			return ExploreReward{}, err
		}
		if !done {
			return ExploreReward{}, gameerr.Validationf("%s還沒修好：要所屬群組的成員一起捐素材修復完成後才能進入"+
				"（「⛰️ 山洞」→「🏛️ 修復遺跡」）。", area.Name)
		}
		if farm.Level < area.UnlockLevel {
			return ExploreReward{}, gameerr.Validationf("%s需要農場 Lv.%d 才能探索（目前 Lv.%d）。", area.Name, area.UnlockLevel, farm.Level)
		}
		now := x.now()
		row, err := explrepo.GetCooldown(ctx, t, farm.ID, "ruins", true)
		if err != nil {
			return ExploreReward{}, err
		}
		if !ruinsReady(row, now) {
			next := row.NextAvailableAt
			left := rules.FormatDuration(rules.RemainingSeconds(&next, now))
			return ExploreReward{}, gameerr.Validationf("今天已經探索過%s了，%s後才能再探索。", area.Name, left)
		}
		bonus := 0
		g, err := ruinsrepo.GetGuardian(ctx, t, *farm.HomeGroupID, ruins.WeekStart(now), false)
		if err != nil {
			return ExploreReward{}, err
		}
		if g != nil && g.DefeatedAt != nil {
			bonus = ruins.DefeatBonusRolls
		}
		span := exploration.RuinsExtra()
		low, high := span[0], span[1]
		extra := low + min(high-low, int(x.exploreRng()*float64(high-low+1)))
		res := exploration.Explore("ruins", farm.Level, x.exploreRng, extra+bonus)
		exploration.RollSawdust(res, x.exploreRng)
		until := now.Add(time.Duration(area.DurationSeconds) * time.Second)
		if _, err := explrepo.SetCooldown(ctx, t, row, farm.ID, "ruins", now, until); err != nil {
			return ExploreReward{}, err
		}
		// 建立時就標記已領取：不會和一般探險「每座農場最多一筆未領取」的唯一索引衝突
		claimed := now
		log, err := explrepo.AddLogOrdered(ctx, t, models.ExplorationLog{FarmID: farm.ID, AreaCode: "ruins", FarmLevel: farm.Level,
			Advanced: res.Advanced, Rewards: res.Items, Events: res.Events, ExploredAt: now, NextAvailableAt: until,
			ClaimedAt: &claimed}, res.ItemOrder)
		if err != nil {
			return ExploreReward{}, err
		}
		out, err := x.grantLog(ctx, t, farm, log, res.ItemOrder, now)
		if err != nil {
			return ExploreReward{}, err
		}
		out.Instant, out.NextAt, out.ExtraRolls, out.BonusRolls = true, &until, extra, bonus
		return out, nil
	})
}

// ClaimExploration 領取已回來的探險獎勵：素材、假幣與稀有種子都放進素材背包（不佔倉庫）；放不下時不領取。
func (x *ExplorationService) ClaimExploration(ctx context.Context, playerID int64) (ExploreReward, error) {
	return inTx(ctx, x.GameService, func(t *db.Tx) (ExploreReward, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ExploreReward{}, err
		}
		now := x.now()
		log, err := explrepo.GetActive(ctx, t, farm.ID, true)
		if err != nil {
			return ExploreReward{}, err
		}
		if log == nil {
			return ExploreReward{}, gameerr.Validationf("目前沒有可以領取的探險，先到山洞選一個區域出發吧。")
		}
		if now.Before(log.NextAvailableAt) {
			next := log.NextAvailableAt
			left := rules.FormatDuration(rules.RemainingSeconds(&next, now))
			return ExploreReward{}, gameerr.Validationf("還在%s探險中，%s後才會回來。", exploration.AreaOf(log.AreaCode).Name, left)
		}
		orders, err := explrepo.RewardOrders(ctx, t, []int64{log.ID})
		if err != nil {
			return ExploreReward{}, err
		}
		return x.grantLog(ctx, t, farm, log, orders[log.ID], now)
	})
}

// grantLog 把探險紀錄的獎勵放進背包、寫交易紀錄並回傳結果（呼叫端已鎖農場列）；放不下時整筆不領取。
// order 為 rewards 的鍵順序。
func (x *ExplorationService) grantLog(ctx context.Context, t *db.Tx, farm *models.Farm, log *models.ExplorationLog,
	order []string, now time.Time) (ExploreReward, error) {
	area := exploration.AreaOf(log.AreaCode)
	items := log.Rewards
	total, materials := 0, 0
	for _, c := range order {
		total += items[c]
		if inventory.IsMaterial(c) {
			materials += items[c]
		}
	}
	err := x.ensureSpace(ctx, t, farm, total-materials) // 一般物品放倉庫（目前掉落表沒有）
	if err == nil {
		err = x.ensureMaterialSpace(ctx, t, farm, materials) // 素材、假幣與稀有種子放素材背包
	}
	if gameerr.Is(err, gameerr.WarehouseFull) {
		return ExploreReward{}, gameerr.New(gameerr.WarehouseFull, "%s 探險帶回來的物品放不下，清出空間後再領取。", err.Error())
	}
	if err != nil {
		return ExploreReward{}, err
	}
	for _, c := range order {
		if err := inventory.Add(ctx, t, farm.ID, c, items[c], "normal", "none"); err != nil {
			return ExploreReward{}, err
		}
		if err := codex.Register(ctx, t, farm.ID, c, items[c], "", now); err != nil { // 山洞素材圖鑑
			return ExploreReward{}, err
		}
	}
	if log.ClaimedAt == nil {
		if err := explrepo.MarkClaimed(ctx, t, log.ID, now); err != nil {
			return ExploreReward{}, err
		}
		log.ClaimedAt = &now
	}
	names, err := x.names(ctx, t, order)
	if err != nil {
		return ExploreReward{}, err
	}
	joined := joinItems(order, items, names)
	if joined == "" {
		joined = "一無所獲"
	}
	text := fmt.Sprintf("探索%s：%s", area.Name, joined)
	if len(log.Events) > 0 {
		text += "（" + strings.Join(log.Events, "、") + "）"
	}
	if err := x.record(ctx, t, farm, rules.EvExplore, truncRunes(text, 200), rec{Quantity: total}); err != nil {
		return ExploreReward{}, err
	}
	note := ""
	if area.NeedsRuins && farm.HomeGroupID != nil {
		dealt, defeated, ok, err := x.hitGuardian(ctx, t, *farm.HomeGroupID, farm.ID, ruins.ExpeditionDamage)
		if err != nil {
			return ExploreReward{}, err
		}
		if ok && dealt > 0 {
			note = fmt.Sprintf("⚔️ 在遺跡裡和%s交手，造成 %d 點傷害！", ruins.GuardianName, dealt)
			if defeated {
				note += fmt.Sprintf("🎉 %s倒下了！", ruins.GuardianName)
			}
		}
	}
	out := ExploreReward{LogID: log.ID, Area: log.AreaCode, AreaName: area.Name, Emoji: area.Emoji, Advanced: log.Advanced,
		Level: log.FarmLevel, Items: []ExploreItem{}, Events: slices.Clone(log.Events), Seeds: map[string]int{},
		FakeCoins: items[exploration.FakeCoin], StartedAt: log.ExploredAt, EndsAt: log.NextAvailableAt, GuardianNote: note}
	for _, c := range order {
		rarity := "稀有種子"
		if m, ok := exploration.Materials[c]; ok {
			rarity = m.Rarity
		}
		out.Items = append(out.Items, ExploreItem{c, names[c], items[c], rarity})
		if c == exploration.AmberSeed || c == exploration.MoonSeed {
			out.Seeds[names[c]] = items[c]
		}
	}
	return out, nil
}

// ExplorationLogs 已領取的探險紀錄；limit 預設 10（1–50）。
func (x *ExplorationService) ExplorationLogs(ctx context.Context, playerID int64, limit int) ([]ExplorationLogView, error) {
	limit = max(1, min(limit, 50))
	return inTx(ctx, x.GameService, func(t *db.Tx) ([]ExplorationLogView, error) {
		farm, err := x.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		logs, err := explrepo.ListLogs(ctx, t, farm.ID, limit)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, len(logs))
		var codes []string
		for i, l := range logs {
			ids[i] = l.ID
			for c := range l.Rewards {
				codes = append(codes, c)
			}
		}
		orders, err := explrepo.RewardOrders(ctx, t, ids)
		if err != nil {
			return nil, err
		}
		names, err := x.names(ctx, t, codes)
		if err != nil {
			return nil, err
		}
		out := []ExplorationLogView{}
		for _, l := range logs {
			area := exploration.AreaOf(l.AreaCode)
			v := ExplorationLogView{Area: l.AreaCode, AreaName: area.Name, Emoji: area.Emoji, Advanced: l.Advanced,
				Level: l.FarmLevel, Rewards: []NamedQty{}, Events: slices.Clone(l.Events), ExploredAt: l.ExploredAt,
				NextAvailableAt: l.NextAvailableAt, ClaimedAt: l.ClaimedAt}
			for _, c := range orders[l.ID] {
				v.Rewards = append(v.Rewards, NamedQty{names[c], l.Rewards[c]})
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// ---------- 古代遺跡（群組修復） ----------

// RuinsStatus 所屬群組的遺跡修復進度、目前階段還缺的素材、我的背包數量、捐獻排行與本週守護者。
// 讀取前先結算已結束的守護者週（沒打倒時遺跡崩壞）。
func (x *ExplorationService) RuinsStatus(ctx context.Context, playerID int64) (RuinsStatus, error) {
	return inTx(ctx, x.GameService, func(t *db.Tx) (RuinsStatus, error) {
		farm, err := x.farmOf(ctx, t, playerID)
		if err != nil {
			return RuinsStatus{}, err
		}
		gid := farm.HomeGroupID
		if err := x.settleGuardian(ctx, t, gid); err != nil {
			return RuinsStatus{}, err
		}
		st := RuinsStatus{GroupID: gid, Stages: len(ruins.Stages), Title: ruins.Title, Materials: []RuinsMaterial{},
			Top: []ruinsrepo.Contributor{}}
		var row *models.GroupRuins
		if gid != nil {
			g, err := groups.Get(ctx, t, *gid)
			if err != nil {
				return RuinsStatus{}, err
			}
			if g != nil {
				st.GroupName = &g.Name
			}
			if row, err = ruinsrepo.Get(ctx, t, *gid, false); err != nil {
				return RuinsStatus{}, err
			}
		}
		progress := map[string]int{}
		if row != nil {
			st.Stage, progress, st.CompletedAt, st.CollapsedAt = row.Stage, row.Progress, row.CompletedAt, row.CollapsedAt
		}
		info := ruins.StageInfo(st.Stage)
		st.Completed = info == nil
		st.Percent = ruins.Percent(st.Stage, progress)
		for _, s := range ruins.Stages {
			st.StageNames = append(st.StageNames, s.Name)
		}
		if info != nil {
			st.StageName, st.StageDescription = &info.Name, &info.Description
			names, err := x.names(ctx, t, info.MaterialOrder)
			if err != nil {
				return RuinsStatus{}, err
			}
			for _, c := range info.MaterialOrder {
				need := info.Materials[c]
				have, err := inventory.GetQuantity(ctx, t, farm.ID, c, "normal", "none")
				if err != nil {
					return RuinsStatus{}, err
				}
				st.Materials = append(st.Materials, RuinsMaterial{c, names[c], need, min(progress[c], need), have})
			}
		}
		if gid != nil {
			if st.Mine, err = ruinsrepo.Contribution(ctx, t, *gid, farm.ID); err != nil {
				return RuinsStatus{}, err
			}
			if st.Top, err = ruinsrepo.TopContributors(ctx, t, *gid, 10); err != nil {
				return RuinsStatus{}, err
			}
		}
		if row != nil && row.CompletedAt != nil {
			g, err := x.guardianView(ctx, t, row, farm)
			if err != nil {
				return RuinsStatus{}, err
			}
			st.Guardian = &g
		}
		return st, nil
	})
}

func (x *ExplorationService) guardianView(ctx context.Context, t *db.Tx, row *models.GroupRuins, farm *models.Farm) (GuardianView, error) {
	now := x.now()
	week := ruins.WeekStart(now)
	active := ruins.GuardianActive(row.CompletedAt, now)
	var g *models.RuinsGuardian
	var err error
	if active {
		if g, err = ruinsrepo.GetGuardian(ctx, t, row.GroupID, week, false); err != nil {
			return GuardianView{}, err
		}
	}
	first := ruins.FirstGuardianWeek(*row.CompletedAt)
	v := GuardianView{Name: ruins.GuardianName, Active: active, AppearsAt: ruins.WeekEnd(first.AddDate(0, 0, -7)),
		EndsAt: ruins.WeekEnd(week), Weapons: []GuardianWeapon{}, Top: []ruinsrepo.Hit{},
		ExpeditionDamage: ruins.ExpeditionDamage, BonusRolls: ruins.DefeatBonusRolls}
	if g != nil {
		v.HPMax, v.Damage, v.Defeated = g.HPMax, min(g.Damage, g.HPMax), g.DefeatedAt != nil
	} else {
		members, err := ruinsrepo.MemberFarms(ctx, t, row.GroupID)
		if err != nil {
			return GuardianView{}, err
		}
		v.HPMax = ruins.GuardianHP(members)
	}
	var weapons []string
	for _, c := range exploration.MaterialOrder {
		if d, ok := ruins.MaterialDamage(c); ok && d > 0 {
			weapons = append(weapons, c)
		}
	}
	names, err := x.names(ctx, t, weapons)
	if err != nil {
		return GuardianView{}, err
	}
	for _, c := range weapons {
		have, err := inventory.GetQuantity(ctx, t, farm.ID, c, "normal", "none")
		if err != nil {
			return GuardianView{}, err
		}
		if have > 0 {
			d, _ := ruins.MaterialDamage(c)
			v.Weapons = append(v.Weapons, GuardianWeapon{c, names[c], have, d})
		}
	}
	if active {
		if v.Mine, err = ruinsrepo.MyHits(ctx, t, row.GroupID, week, farm.ID); err != nil {
			return GuardianView{}, err
		}
		if v.Top, err = ruinsrepo.TopHits(ctx, t, row.GroupID, week, 5); err != nil {
			return GuardianView{}, err
		}
	}
	return v, nil
}

// settleGuardian 結算已經結束的守護者週：該週沒打倒（或根本沒人攻擊）時遺跡崩壞回第三階段。
// 沒有要結算的週時只讀一列、不加鎖；呼叫端不可已持有農場列的鎖（順序：遺跡列 → 農場列 → 守護者列）。
func (x *ExplorationService) settleGuardian(ctx context.Context, t *db.Tx, groupID *int64) error {
	if groupID == nil {
		return nil
	}
	now := x.now()
	row, err := ruinsrepo.Get(ctx, t, *groupID, false)
	if err != nil || row == nil || row.CompletedAt == nil ||
		len(ruins.WeeksToSettle(*row.CompletedAt, row.GuardianSettled, now)) == 0 {
		return err
	}
	if row, err = ruinsrepo.Get(ctx, t, *groupID, true); err != nil || row.CompletedAt == nil {
		return err
	}
	for _, week := range ruins.WeeksToSettle(*row.CompletedAt, row.GuardianSettled, now) {
		g, err := ruinsrepo.GetGuardian(ctx, t, *groupID, week, false)
		if err != nil {
			return err
		}
		w := week
		row.GuardianSettled = &w
		if g == nil || g.DefeatedAt == nil {
			collapsed := ruins.WeekEnd(week)
			row.Stage, row.Progress, row.CompletedAt = ruins.Completed-1, map[string]int{}, nil
			row.CollapsedAt, row.UpdatedAt = &collapsed, now
			slog.Info("古代遺跡因守護者沒被打倒而崩壞", "group_id", *groupID, "week", week.Format("2006-01-02"))
			break
		}
	}
	return ruinsrepo.Save(ctx, t, row)
}

// hitGuardian 對本週守護者造成傷害（呼叫端已鎖農場列）。回傳 (實際傷害, 這次打倒, 守護者已出現)。
func (x *ExplorationService) hitGuardian(ctx context.Context, t *db.Tx, groupID, farmID int64, damage int) (int, bool, bool, error) {
	now := x.now()
	row, err := ruinsrepo.Get(ctx, t, groupID, false)
	if err != nil || row == nil || !ruins.GuardianActive(row.CompletedAt, now) {
		return 0, false, false, err
	}
	week := ruins.WeekStart(now)
	members, err := ruinsrepo.MemberFarms(ctx, t, groupID)
	if err != nil {
		return 0, false, false, err
	}
	g, err := ruinsrepo.EnsureGuardian(ctx, t, groupID, week, ruins.GuardianHP(members))
	if err != nil {
		return 0, false, false, err
	}
	if g.DefeatedAt != nil {
		return 0, false, true, nil
	}
	dealt := min(damage, g.HPMax-g.Damage)
	g.Damage += dealt
	if err := ruinsrepo.AddHit(ctx, t, groupID, week, farmID, dealt); err != nil {
		return 0, false, false, err
	}
	defeated := g.Damage >= g.HPMax
	if defeated {
		g.DefeatedAt = &now
	}
	return dealt, defeated, true, ruinsrepo.SaveGuardian(ctx, t, g)
}

// AttackGuardian 捐素材攻擊所屬群組本週的遺跡守護者；超過剩餘 HP 所需的數量不會扣。
func (x *ExplorationService) AttackGuardian(ctx context.Context, playerID int64, itemKey string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	if quantity > ruins.AttackMax {
		return ActionResult{}, gameerr.Validationf("一次最多用 %d 個素材。", ruins.AttackMax)
	}
	code := strings.TrimSpace(itemKey)
	per, ok := ruins.MaterialDamage(code)
	if !ok {
		return ActionResult{}, gameerr.Validationf("只能用山洞素材攻擊守護者（假幣不行）。")
	}
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		now := x.now()
		gid, found, err := homeGroupOf(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if !found {
			_, err := x.lockedFarm(ctx, t, playerID)
			return ActionResult{}, err
		}
		if err := x.settleGuardian(ctx, t, gid); err != nil { // 先結算（鎖遺跡列），再鎖農場列
			return ActionResult{}, err
		}
		var row *models.GroupRuins
		if gid != nil {
			if row, err = ruinsrepo.Get(ctx, t, *gid, false); err != nil {
				return ActionResult{}, err
			}
		}
		if row == nil || row.CompletedAt == nil {
			return ActionResult{}, gameerr.Validationf("古代遺跡還沒修好（或已經崩壞），現在沒有守護者。")
		}
		if !ruins.GuardianActive(row.CompletedAt, now) {
			return ActionResult{}, gameerr.Validationf("守護者還沒出現：遺跡修好後的下一個週一（台灣時間）才會出現。")
		}
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.HomeGroupID == nil || *farm.HomeGroupID != *gid {
			return ActionResult{}, gameerr.Validationf("農場的所屬群組剛剛變更了，請重新開啟遺跡頁面。")
		}
		week := ruins.WeekStart(now)
		members, err := ruinsrepo.MemberFarms(ctx, t, *gid)
		if err != nil {
			return ActionResult{}, err
		}
		g, err := ruinsrepo.EnsureGuardian(ctx, t, *gid, week, ruins.GuardianHP(members))
		if err != nil {
			return ActionResult{}, err
		}
		if g.DefeatedAt != nil {
			return ActionResult{}, gameerr.Validationf("本週的%s已經被打倒了，下週一再來！", ruins.GuardianName)
		}
		qty := min(quantity, (g.HPMax-g.Damage+per-1)/per) // 剩餘 HP 需要幾個（無條件進位）
		names, err := x.names(ctx, t, []string{code})
		if err != nil {
			return ActionResult{}, err
		}
		name := names[code]
		removed, err := inventory.Remove(ctx, t, farm.ID, code, qty, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !removed {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, code, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s不足：想用 %d 個，背包只有 %d 個。", name, qty, have)
		}
		dealt, defeated, _, err := x.hitGuardian(ctx, t, *gid, farm.ID, qty*per)
		if err != nil {
			return ActionResult{}, err
		}
		if err := x.record(ctx, t, farm, rules.EvRuinsGuardian,
			fmt.Sprintf("用 %s ×%d 攻擊%s，造成 %d 傷害", name, qty, ruins.GuardianName, dealt),
			rec{ItemCode: code, Quantity: qty}); err != nil {
			return ActionResult{}, err
		}
		text := fmt.Sprintf("⚔️ 用 %s ×%d 攻擊%s，造成 **%d** 點傷害！", name, qty, ruins.GuardianName, dealt)
		if qty < quantity {
			text += "（剩餘 HP 只需要這些，其餘留在背包）"
		}
		if defeated {
			text += fmt.Sprintf("\n🎉 %s倒下了！本週古代遺跡探索每次多 %d 份獎勵，遺跡也安全度過這一週。",
				ruins.GuardianName, ruins.DefeatBonusRolls)
		}
		return result(text, farm.Gold, 0, code, qty), nil
	})
}

// DonateRuins 捐素材修復所屬群組的遺跡；超過目前階段還缺的數量時只捐需要的部分。階段湊齊就進入下一階段，
// 三個階段都完成時，所有捐過素材的成員獲得稱號。先鎖遺跡列再鎖農場列（和世界 Boss 相同順序）。
func (x *ExplorationService) DonateRuins(ctx context.Context, playerID int64, itemKey string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	if quantity > ruins.DonateMax {
		return ActionResult{}, gameerr.Validationf("一次最多捐 %d 個。", ruins.DonateMax)
	}
	code := strings.TrimSpace(itemKey)
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		now := x.now()
		gid, found, err := homeGroupOf(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if !found {
			_, err := x.lockedFarm(ctx, t, playerID) // 產生正確的錯誤訊息
			return ActionResult{}, err
		}
		if gid == nil {
			return ActionResult{}, gameerr.Validationf("你的農場還沒有所屬群組，無法修復遺跡。")
		}
		if err := x.settleGuardian(ctx, t, gid); err != nil { // 遺跡可能剛因守護者沒被打倒而崩壞
			return ActionResult{}, err
		}
		row, err := ruinsrepo.GetOrCreate(ctx, t, *gid, now)
		if err != nil {
			return ActionResult{}, err
		}
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if farm.HomeGroupID == nil || *farm.HomeGroupID != *gid { // 讀取後到鎖定前剛好轉移了所屬群組
			return ActionResult{}, gameerr.Validationf("農場的所屬群組剛剛變更了，請重新開啟遺跡頁面。")
		}
		info := ruins.StageInfo(row.Stage)
		if info == nil {
			return ActionResult{}, gameerr.Validationf("古代遺跡已經修復完成了，直接到山洞出發探險吧！")
		}
		need := ruins.Remaining(row.Stage, row.Progress)
		if _, ok := info.Materials[code]; !ok {
			names, err := x.names(ctx, t, info.MaterialOrder)
			if err != nil {
				return ActionResult{}, err
			}
			list := make([]string, len(info.MaterialOrder))
			for i, c := range info.MaterialOrder {
				list[i] = names[c]
			}
			return ActionResult{}, gameerr.Validationf("「%s」階段只需要：%s。", info.Name, strings.Join(list, "、"))
		}
		names, err := x.names(ctx, t, []string{code})
		if err != nil {
			return ActionResult{}, err
		}
		name := names[code]
		if _, ok := need[code]; !ok {
			return ActionResult{}, gameerr.Validationf("%s已經夠了，捐其他還缺的素材吧。", name)
		}
		qty := min(quantity, need[code])
		removed, err := inventory.Remove(ctx, t, farm.ID, code, qty, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !removed {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, code, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s不足：想捐 %d 個，背包只有 %d 個。", name, qty, have)
		}
		progress := make(map[string]int, len(row.Progress)+1)
		for k, v := range row.Progress {
			progress[k] = v
		}
		progress[code] += qty
		row.Progress, row.UpdatedAt = progress, now
		if err := ruinsrepo.AddContribution(ctx, t, *gid, farm.ID, qty, now); err != nil {
			return ActionResult{}, err
		}
		if err := x.record(ctx, t, farm, rules.EvRuinsDonate, fmt.Sprintf("捐 %s ×%d 修復古代遺跡（%s）", name, qty, info.Name),
			rec{ItemCode: code, Quantity: qty}); err != nil {
			return ActionResult{}, err
		}
		text := fmt.Sprintf("🏛️ 捐出 %s ×%d，幫忙%s。", name, qty, info.Name)
		if qty < quantity {
			text += fmt.Sprintf("（這個階段只差 %d 個，其餘留在背包）", qty)
		}
		if ruins.StageDone(row.Stage, row.Progress) {
			row.Stage++
			row.Progress = map[string]int{}
			if row.Stage >= ruins.Completed {
				row.CompletedAt = &now
				farmIDs, err := ruinsrepo.ContributorFarms(ctx, t, *gid)
				if err != nil {
					return ActionResult{}, err
				}
				for _, id := range farmIDs {
					if err := titles.Grant(ctx, t, id, ruins.Title, ruins.TitleSource, now); err != nil {
						return ActionResult{}, err
					}
				}
				text += fmt.Sprintf("\n🎉 古代遺跡修復完成！整個群組都能到山洞探索「🏛️ 古代遺跡」，所有出過力的成員獲得稱號「%s」。", ruins.Title)
			} else {
				text += fmt.Sprintf("\n✨ 「%s」完成！下一階段：「%s」。", info.Name, ruins.Stages[row.Stage].Name)
			}
		}
		if err := ruinsrepo.Save(ctx, t, row); err != nil {
			return ActionResult{}, err
		}
		return result(text, farm.Gold, 0, code, qty), nil
	})
}

// ---------- 假幣 ----------

func (x *ExplorationService) ExchangeFakeCoins(ctx context.Context, playerID int64, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	name := exploration.Materials[exploration.FakeCoin].Name
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, exploration.FakeCoin, quantity, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, exploration.FakeCoin, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s不足：想兌換 %d 個，背包只有 %d 個。", name, quantity, have)
		}
		gold := quantity * exploration.FakeCoinRate()
		farm.Gold += gold
		if err := x.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := x.record(ctx, t, farm, rules.EvExchange, fmt.Sprintf("兌換 %d 個%s，金幣 +%d", quantity, name, gold),
			rec{ItemCode: exploration.FakeCoin, Quantity: quantity, GoldDelta: gold}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已兌換 %d 個%s，獲得 %d 金幣。", quantity, name, gold), farm.Gold, gold, exploration.FakeCoin, quantity), nil
	})
}

// ---------- 機器 ----------

func (x *ExplorationService) MachinesStatus(ctx context.Context, playerID int64) (MachinesStatus, error) {
	return inTx(ctx, x.GameService, func(t *db.Tx) (MachinesStatus, error) {
		farm, err := x.farmOf(ctx, t, playerID)
		if err != nil {
			return MachinesStatus{}, err
		}
		list, err := items.ListTools(ctx, t, farm.ID)
		if err != nil {
			return MachinesStatus{}, err
		}
		tools := map[string]*models.FarmTool{}
		for i := range list {
			tools[list[i].ToolCode] = &list[i]
		}
		var codes []string
		for _, m := range exploration.Machines {
			codes = append(codes, m.MaterialOrder...)
		}
		have := map[string]int{}
		for _, c := range codes {
			if have[c], err = inventory.GetQuantity(ctx, t, farm.ID, c, "normal", "none"); err != nil {
				return MachinesStatus{}, err
			}
		}
		names, err := x.names(ctx, t, codes)
		if err != nil {
			return MachinesStatus{}, err
		}
		out := MachinesStatus{Level: farm.Level}
		for _, code := range exploration.MachineOrder {
			m := exploration.Machines[code]
			tool := tools[code]
			v := MachineView{Code: code, Emoji: m.Emoji, Name: m.Name, Description: m.Description, UnlockLevel: m.UnlockLevel,
				Unlocked: farm.Level >= m.UnlockLevel, Owned: tool != nil, Settings: map[string]any{}, Missing: map[string]int{}}
			if tool != nil {
				v.Enabled = tool.Enabled
				for k, val := range tool.Settings {
					v.Settings[k] = val
				}
			}
			for _, c := range m.MaterialOrder {
				v.Materials = append(v.Materials, MachineMaterial{names[c], m.Materials[c], have[c]})
			}
			for c, n := range exploration.MissingMaterials(m, have) {
				v.Missing[names[c]] = n
			}
			out.Machines = append(out.Machines, v)
		}
		return out, nil
	})
}

func (x *ExplorationService) BuildMachine(ctx context.Context, playerID int64, key string) (ActionResult, error) {
	code := FindMachine(key)
	if code == "" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種機器。", key)
	}
	m := exploration.Machines[code]
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		// 農場已鎖定，加上 farm_tools 唯一限制，重複點擊或並行請求都不會重複扣料或重複建立
		tool, err := items.GetTool(ctx, t, farm.ID, code)
		if err != nil {
			return ActionResult{}, err
		}
		if tool != nil {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有%s了。", m.Name)
		}
		if farm.Level < m.UnlockLevel {
			return ActionResult{}, gameerr.Validationf("%s需要農場 Lv.%d（目前 Lv.%d）。", m.Name, m.UnlockLevel, farm.Level)
		}
		have := map[string]int{}
		for _, c := range m.MaterialOrder {
			if have[c], err = inventory.GetQuantity(ctx, t, farm.ID, c, "normal", "none"); err != nil {
				return ActionResult{}, err
			}
		}
		missing := exploration.MissingMaterials(m, have)
		names, err := x.names(ctx, t, m.MaterialOrder)
		if err != nil {
			return ActionResult{}, err
		}
		if len(missing) > 0 {
			var order []string
			for _, c := range m.MaterialOrder {
				if _, ok := missing[c]; ok {
					order = append(order, c)
				}
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "材料不足，還缺：%s。", joinItems(order, missing, names))
		}
		for _, c := range m.MaterialOrder {
			ok, err := inventory.Remove(ctx, t, farm.ID, c, m.Materials[c], "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			if !ok {
				return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "材料不足：%s。", names[c])
			}
		}
		if _, err := items.AddTool(ctx, t, farm.ID, code, x.now()); err != nil {
			return ActionResult{}, err
		}
		used := joinItems(m.MaterialOrder, m.Materials, names)
		if err := x.record(ctx, t, farm, rules.EvBuildMachine, fmt.Sprintf("製作%s，消耗 %s", m.Name, used),
			rec{ItemCode: code, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("%s %s製作完成！消耗 %s。", m.Emoji, m.Name, used), farm.Gold, 0, code, 1), nil
	})
}

func (x *ExplorationService) machine(ctx context.Context, t *db.Tx, farm *models.Farm, code string, requireEnabled bool) (*models.FarmTool, error) {
	m := exploration.Machines[code]
	tool, err := items.GetTool(ctx, t, farm.ID, code)
	if err != nil {
		return nil, err
	}
	if tool == nil {
		return nil, gameerr.Validationf("需要先製作%s（山洞探索取得素材，在「🔧 機器」製作）。", m.Name)
	}
	if requireEnabled && !tool.Enabled {
		return nil, gameerr.Validationf("%s已停用，請先啟用。", m.Name)
	}
	return tool, nil
}

func (x *ExplorationService) SetMachineEnabled(ctx context.Context, playerID int64, key string, enabled bool) (ActionResult, error) {
	code := FindMachine(key)
	if code == "" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種機器。", key)
	}
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, err := x.machine(ctx, t, farm, code, false)
		if err != nil {
			return ActionResult{}, err
		}
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("enabled", enabled)); err != nil {
			return ActionResult{}, err
		}
		word := "停用"
		if enabled {
			word = "啟用"
		}
		return result(fmt.Sprintf("%s已%s。", exploration.Machines[code].Name, word), farm.Gold, 0, "", 0), nil
	})
}

// SetProcessorRecipe 設定自動加工機的加工項目；"" 表示不自動開單（仍會自動領取完成的訂單）。
func (x *ExplorationService) SetProcessorRecipe(ctx context.Context, playerID int64, recipeKey string) (ActionResult, error) {
	code := ""
	if recipeKey != "" {
		if code = FindRecipe(recipeKey); code == "" {
			return ActionResult{}, gameerr.NotFoundf("沒有「%s」這個加工配方。", recipeKey)
		}
	}
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		tool, err := x.machine(ctx, t, farm, "auto_processor", false)
		if err != nil {
			return ActionResult{}, err
		}
		settings := map[string]any{}
		label := "不自動開單"
		if code != "" {
			settings["recipe"] = code
			label = balance.Recipes[code].Name
		}
		if _, err := db.UpdateByID(ctx, t, models.TableFarmTool, tool.ID, db.C("settings", settings)); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("自動加工機的加工項目設為：%s。", label), farm.Gold, 0, "", 0), nil
	})
}

// ---------- 種子製造機 ----------

// MakeSeeds quality 預設 "normal"，mutation 預設 "none"。
func (x *ExplorationService) MakeSeeds(ctx context.Context, playerID int64, cropKey string, quantity int, quality, mutation string) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	if quantity > exploration.SeedMakerMax {
		return ActionResult{}, gameerr.Validationf("一次最多使用 %d 個作物。", exploration.SeedMakerMax)
	}
	quality, mutation = genetics.ParseQuality(quality), genetics.ParseMutation(mutation)
	if !slices.Contains(exploration.SeedMakerQualities, quality) || !slices.Contains(exploration.SeedMakerMutations, mutation) {
		return ActionResult{}, gameerr.Validationf("種子製造機只能使用普通品質、沒有突變的作物。")
	}
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if _, err := x.machine(ctx, t, farm, "seed_maker", true); err != nil {
			return ActionResult{}, err
		}
		crop, err := x.crop(ctx, t, cropKey, true)
		if err != nil {
			return ActionResult{}, err
		}
		if !crop.ShopBuyable && !exploration.SeedMakerRareCrops {
			return ActionResult{}, gameerr.Validationf("%s是稀有作物，種子製造機無法製造它的種子。", crop.Name)
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, crop.Code, quantity, quality, mutation)
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, crop.Code, quality, mutation)
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s不足：需要 %d 個，背包只有 %d 個。", crop.Name, quantity, have)
		}
		seeds := quantity * exploration.SeedMakerRatio
		item := rules.SeedItemCode(crop.Code)
		// 扣作物後再檢查空間（稀有種子放素材背包）
		if err := x.ensureItemSpace(ctx, t, farm, item, seeds); err != nil {
			return ActionResult{}, err
		}
		if err := inventory.Add(ctx, t, farm.ID, item, seeds, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		if err := x.record(ctx, t, farm, rules.EvMakeSeed,
			fmt.Sprintf("種子製造機：%d 個%s → %d 顆%s種子", quantity, crop.Name, seeds, crop.Name),
			rec{ItemCode: item, Quantity: seeds}); err != nil {
			return ActionResult{}, err
		}
		auto, err := x.autoPlant(ctx, t, farm)
		if err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🌱 用 %d 個%s做出 %d 顆%s種子。%s", quantity, crop.Name, seeds, crop.Name, auto), farm.Gold, 0, item, seeds), nil
	})
}

// ---------- 稀有作物培育器（以加工訂單執行） ----------

func (x *ExplorationService) IncubatorRecipes(ctx context.Context) ([]IncubatorRecipeView, error) {
	var codes []string
	for _, r := range exploration.IncubatorRecipes {
		codes = append(codes, r.InputOrder...)
		codes = append(codes, r.Output)
	}
	names, err := inTx(ctx, x.GameService, func(t *db.Tx) (map[string]string, error) { return x.names(ctx, t, codes) })
	if err != nil {
		return nil, err
	}
	var out []IncubatorRecipeView
	for _, code := range exploration.IncubatorRecipeOrder {
		r := exploration.IncubatorRecipes[code]
		v := IncubatorRecipeView{Code: code, Name: r.Name, Inputs: map[string]int{}, OutputName: names[r.Output],
			OutputQuantity: r.OutputQty, Seconds: r.Seconds}
		for _, c := range r.InputOrder {
			v.Inputs[names[c]] = r.Inputs[c]
			v.InputOrder = append(v.InputOrder, names[c])
		}
		out = append(out, v)
	}
	return out, nil
}

// StartIncubation quantity 預設 1（1–IncubatorMaxQuantity）。
func (x *ExplorationService) StartIncubation(ctx context.Context, playerID int64, key string, quantity int) (ActionResult, error) {
	code := FindIncubatorRecipe(key)
	if code == "" {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這個培育配方。", key)
	}
	if quantity < 1 || quantity > exploration.IncubatorMaxQuantity {
		return ActionResult{}, gameerr.Validationf("培育份數必須是 1–%d。", exploration.IncubatorMaxQuantity)
	}
	r := exploration.IncubatorRecipes[code]
	return inTx(ctx, x.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := x.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if _, err := x.machine(ctx, t, farm, incubator, true); err != nil {
			return ActionResult{}, err
		}
		return x.startOrder(ctx, t, farm, craftSpec{code, r.Name, incubator, r.Inputs, r.InputOrder, r.Output, r.OutputQty, r.Seconds},
			quantity, "")
	})
}

// ---------- 自動收割機與自動加工機 ----------

// RunMachines 執行已啟用的自動收割機與自動加工機（開啟面板時呼叫）；沒有機器時不鎖定農場、不寫入。回傳結果說明。
func (x *ExplorationService) RunMachines(ctx context.Context, playerID int64) (string, error) {
	return inTx(ctx, x.GameService, func(t *db.Tx) (string, error) {
		farm, err := farms.GetByPlayer(ctx, t, playerID, false)
		if err != nil || farm == nil {
			return "", err
		}
		list, err := items.ListTools(ctx, t, farm.ID)
		if err != nil {
			return "", err
		}
		tools := map[string]*models.FarmTool{}
		for i := range list {
			if list[i].Enabled {
				tools[list[i].ToolCode] = &list[i]
			}
		}
		if tools["auto_harvester"] == nil && tools["auto_processor"] == nil {
			return "", nil
		}
		if !x.InBatch(ctx) {
			return x.runMachineTools(ctx, t, playerID, tools)
		}
		// 在面板讀取整頁的共用交易中：機器在 SAVEPOINT 裡執行，失敗只回滾機器的部分，頁面照常讀取
		// （與原本「機器是獨立交易、失敗不影響面板」相同；下次開啟或換頁時再試）
		var note string
		err = t.Savepoint(ctx, func() error {
			var err error
			note, err = x.runMachineTools(ctx, t, playerID, tools)
			return err
		})
		if err != nil {
			if !gameerr.Is(err, gameerr.Generic) {
				slog.Error("自動機器執行失敗", "player_id", playerID, "error", err)
			}
			return "", nil
		}
		return note, nil
	})
}

func (x *ExplorationService) runMachineTools(ctx context.Context, t *db.Tx, playerID int64, tools map[string]*models.FarmTool) (string, error) {
	farm, err := x.lockedFarm(ctx, t, playerID)
	if err != nil {
		return "", err
	}
	var notes []string
	if tool := tools["auto_harvester"]; tool != nil {
		n, err := x.autoHarvest(ctx, t, farm, tool)
		if err != nil {
			return "", err
		}
		notes = append(notes, n)
		if autoHarvestHook != nil {
			if err := autoHarvestHook(); err != nil {
				return "", err
			}
		}
	}
	if tool := tools["auto_processor"]; tool != nil {
		n, err := x.autoProcess(ctx, t, farm, tool)
		if err != nil {
			return "", err
		}
		notes = append(notes, n)
	}
	var kept []string
	for _, n := range notes {
		if n != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, " "), nil
}

// autoHarvest 收成成熟的作物；擁有收割機時才成熟的作物，即使已過乾枯時間也照樣收成（收割機會在成熟時處理）。
func (x *ExplorationService) autoHarvest(ctx context.Context, t *db.Tx, farm *models.Farm, tool *models.FarmTool) (string, error) {
	now := x.now()
	// 一次查詢並鎖定所有土地
	all, err := farms.ListPlots(ctx, t, farm.ID, true)
	if err != nil {
		return "", err
	}
	var targets []*farms.PlotWithCrop
	for i := range all {
		p := &all[i]
		state := rules.PlotStateAt(p.CropTypeID, p.WateredAt, p.ReadyAt, now)
		if state == rules.PlotReady || (state == rules.PlotWithered && !p.ReadyAt.Before(tool.AcquiredAt)) {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		return "", nil
	}
	done, total, stopped := 0, 0, ""
	var details []HarvestDetail
	for _, p := range targets {
		_, qty, _, err := x.harvestLocked(ctx, t, farm, &p.FarmPlot, p.Crop, &details)
		if gameerr.Is(err, gameerr.WarehouseFull) {
			stopped = fmt.Sprintf("倉庫已滿，還有 %d 格沒收。", len(targets)-done)
			break
		}
		if err != nil {
			return "", err
		}
		done++
		total += qty
	}
	text := ""
	if done > 0 {
		text = HarvestHighlight(details) + fmt.Sprintf("🚜 自動收割機收成 %d 格，共 %d 個作物。", done, total)
	}
	text += stopped
	if done > 0 {
		auto, err := x.autoPlant(ctx, t, farm)
		if err != nil {
			return "", err
		}
		text += auto
	}
	return strings.TrimSpace(text), nil
}

// autoProcess 領取所有完成的加工訂單，再以背包原料開始設定的加工項目（能做幾份就做幾份，最多單張上限）。
func (x *ExplorationService) autoProcess(ctx context.Context, t *db.Tx, farm *models.Farm, tool *models.FarmTool) (string, error) {
	now := x.now()
	var notes []string
	collected := 0
	rows, err := ranch.ListOrders(ctx, t, &farm.ID, false, 50)
	if err != nil {
		return "", err
	}
	for _, o := range rows {
		if rules.CraftStateAt(o.Status, o.ReadyAt, now) != rules.CraftReady {
			continue
		}
		locked, err := ranch.GetOrder(ctx, t, farm.ID, o.ID, true)
		if err != nil {
			return "", err
		}
		if _, err := x.collectOrder(ctx, t, farm, locked, "自動加工機："); err != nil {
			if gameerr.Is(err, gameerr.WarehouseFull) {
				notes = append(notes, "倉庫已滿，部分加工品還沒領取。")
				break
			}
			return "", err
		}
		collected++
	}
	if collected > 0 {
		notes = append([]string{fmt.Sprintf("🏭 自動加工機領取 %d 張訂單。", collected)}, notes...)
	}
	code, _ := tool.Settings["recipe"].(string)
	if rc, ok := balance.Recipes[code]; ok {
		n, err := ranch.CountBuildings(ctx, t, farm.ID, rc.Building)
		if err != nil {
			return "", err
		}
		busy, err := ranch.BusyOrder(ctx, t, farm.ID, rc.Building, now)
		if err != nil {
			return "", err
		}
		switch {
		case n == 0:
			notes = append(notes, fmt.Sprintf("自動加工機：需要先建造%s。", balance.Buildings[rc.Building].Name))
		case busy == nil:
			quantity := balance.CraftMaxQuantity
			for _, item := range rc.InputOrder {
				have, err := craftStock(ctx, t, farm.ID, item) // 巨型、快速生長作物也能加工
				if err != nil {
					return "", err
				}
				quantity = min(quantity, have/rc.Inputs[item])
			}
			if quantity >= 1 {
				if _, err := x.startOrder(ctx, t, farm, craftSpec{code, rc.Name, rc.Building, rc.Inputs, rc.InputOrder,
					rc.Output, rc.OutputQty, rc.Seconds}, quantity, "自動加工機："); err != nil {
					return "", err
				}
				notes = append(notes, fmt.Sprintf("🏭 自動加工機開始加工 %d 份%s。", rc.OutputQty*quantity, rc.Name))
			}
		}
	}
	return strings.Join(notes, " "), nil
}
