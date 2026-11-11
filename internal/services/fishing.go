package services

// 釣魚小遊戲：釣具商店、裝備、拋竿、咬鉤反應、拉扯博弈、魚簍、販售、分享、圖鑑與排行榜
// （對應 Python app/services/fishing_service.py）。
//
// 規則與數值在 game/fishing；這裡負責驗證與交易：
//   - 每個會改變資產或釣魚狀態的方法都先鎖定農場資料列，同一位玩家的操作依序執行。
//   - 拋竿時在同一筆交易扣魚餌並建立 Session（每座農場最多一筆進行中的 Session，部分唯一索引），不會重複扣餌。
//   - 拉扯操作帶入畫面上的回合數（turn），重複點擊或過期畫面送出的操作不會被執行兩次。
//   - 釣起時在同一筆交易把 Session 改為 landed 並新增魚（farm_fish.session_id 唯一），不會重複發魚。
//   - 販售以「鎖定 + sold_at IS NULL」標記，金幣經 record 寫入交易紀錄（fish_sell），不會重複發錢。
//   - 分享只寫分享紀錄，不轉移、不消耗魚；只能分享自己的魚。
//   - 時間到期在下一次讀取或操作時判定，不靠排程；Bot 啟動時另外清理殘留的 Session。
//   - 防腳本：定期或操作時間異常時，StartFishing 回傳人機確認題目（Phase = check）而不拋竿；答錯暫停釣魚。
//
// 與 Python 不同：Session 的 state 在資料庫是 JSON（Python dict），規則套件的 fishing.State 是固定欄位的 struct。
// 寫回時以 mergeState 把 struct 的欄位覆蓋到原始 JSON 上，Python 另外存的未知欄位不會遺失。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/players"

	fishrepo "farmgm/internal/repositories/fishing"
)

// FishingService = ExplorationService + 釣魚。
type FishingService struct {
	*ExplorationService
	// FishingRng 釣魚的魚種、重量、脫鉤與魚的動作判定（回傳 [0, 1)）；測試可注入固定序列
	FishingRng func() float64
}

func NewFishingService(d *db.DB, opt Options) *FishingService {
	f := &FishingService{ExplorationService: NewExplorationService(d, opt), FishingRng: opt.FishingRng}
	if f.FishingRng == nil {
		f.FishingRng = rand.Float64
	}
	return f
}

func (f *FishingService) InGroupContext(groupID *int64) *FishingService {
	return &FishingService{f.ExplorationService.InGroupContext(groupID), f.FishingRng}
}

// pickFish 拋竿時挑魚（測試替換點，對應 Python 測試 monkeypatch fishing.pick_fish）。
var pickFish = fishing.PickFish[models.FishType]

// 防腳本的題目與確認間隔：不可預測，也不影響 FishingRng 的序列
func guardSample(list []string, n int) []string {
	out := slices.Clone(list)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out[:n]
}

func guardShuffle(list []string) {
	rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
}

func nextCheckAfter() int {
	lo, hi := fishing.GuardCheckEveryCasts[0], fishing.GuardCheckEveryCasts[1]
	return lo + rand.IntN(hi-lo+1)
}

// secondsDur Python timedelta(seconds=x)：四捨六入五成雙到微秒。
func secondsDur(x float64) time.Duration {
	return time.Duration(math.RoundToEven(x*1e6)) * time.Microsecond
}

// ---------- state JSON ----------

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// mergeState 把 s 的欄位覆蓋到原始 JSON 上（保留原始 JSON 的其他欄位）。
func mergeState(raw json.RawMessage, s fishing.State) (json.RawMessage, error) {
	m, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for k, v := range fields {
		m[k] = v
	}
	return json.Marshal(m)
}

// withLast Python state["last"] = {"text": text}。
func withLast(raw json.RawMessage, text string) (json.RawMessage, error) {
	m, err := rawObject(raw)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(fishing.Last{Text: text})
	if err != nil {
		return nil, err
	}
	m["last"] = data
	return json.Marshal(m)
}

func decodeState(raw json.RawMessage) (fishing.State, map[string]json.RawMessage, error) {
	var s fishing.State
	m, err := rawObject(raw)
	if err != nil {
		return s, nil, err
	}
	if len(m) > 0 {
		err = json.Unmarshal(raw, &s)
	}
	return s, m, err
}

type gearSnap struct {
	Code    string          `json:"code"`
	Name    string          `json:"name"`
	Effects fishing.Effects `json:"effects"`
}

func decodeGear(raw json.RawMessage) map[string]gearSnap {
	out := map[string]gearSnap{}
	_ = json.Unmarshal(raw, &out) // 格式不對時當作沒有釣具（只用於顯示與效果）
	return out
}

func decodeSettings(raw json.RawMessage) (fishing.Settings, error) {
	var s fishing.Settings
	err := json.Unmarshal(raw, &s)
	return s, err
}

// ---------- 回傳資料（Python 以 dict 回傳） ----------

type GearView struct {
	Code          string          `json:"code"`
	Kind          string          `json:"kind"`
	KindName      string          `json:"kind_name"`
	Name          string          `json:"name"`
	Emoji         string          `json:"emoji"`
	Description   string          `json:"description"`
	Price         int             `json:"price"`
	RequiredLevel int             `json:"required_level"`
	Stock         *int            `json:"stock"`
	Effects       fishing.Effects `json:"effects"`
	EffectsText   string          `json:"effects_text"`
	SortOrder     int             `json:"sort_order"`
	Active        bool            `json:"active"`
	Owned         bool            `json:"owned"`
	Quantity      int             `json:"quantity"`
	Equipped      bool            `json:"equipped"`
	Unlocked      bool            `json:"unlocked"`
}

// NewGearView owned 為 nil 表示沒有持有；level 為 nil 表示不判斷等級。
func NewGearView(g models.FishingGear, owned *models.FarmFishingGear, level *int) GearView {
	effects := fishing.Effects{}
	for k, v := range g.Effects {
		effects[k] = v
	}
	v := GearView{Code: g.Code, Kind: g.Kind, KindName: fishing.GearKindNames[g.Kind], Name: g.Name, Emoji: g.Emoji,
		Description: g.Description, Price: g.Price, RequiredLevel: g.RequiredLevel, Stock: g.Stock, Effects: effects,
		EffectsText: fishing.GearEffectsText(g.Kind, g.Effects), SortOrder: g.SortOrder, Active: g.Active,
		Unlocked: level == nil || *level >= g.RequiredLevel}
	if owned != nil {
		v.Owned = g.Kind != "bait" || owned.Quantity > 0
		v.Quantity, v.Equipped = owned.Quantity, owned.Equipped
	}
	return v
}

type FishTypeView struct {
	ID            int64    `json:"id"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Emoji         string   `json:"emoji"`
	Rarity        string   `json:"rarity"`
	RarityName    string   `json:"rarity_name"`
	RarityEmoji   string   `json:"rarity_emoji"`
	MinPrice      int      `json:"min_price"`
	MaxPrice      int      `json:"max_price"`
	MinWeightG    int      `json:"min_weight_g"`
	MaxWeightG    int      `json:"max_weight_g"`
	Difficulty    int      `json:"difficulty"`
	Description   string   `json:"description"`
	SortOrder     int      `json:"sort_order"`
	Active        bool     `json:"active"`
	RequiredBaits []string `json:"required_baits"`
	Seasons       []string `json:"seasons"`
	HourStart     *int     `json:"hour_start"`
	HourEnd       *int     `json:"hour_end"`
	LimitText     string   `json:"limit_text"`
}

func NewFishTypeView(f models.FishType) FishTypeView {
	return FishTypeView{f.ID, f.Code, f.Name, f.Emoji, f.Rarity, fishing.RarityNames[f.Rarity], fishing.RarityEmoji[f.Rarity],
		f.MinPrice, f.MaxPrice, f.MinWeightG, f.MaxWeightG, f.Difficulty, f.Description, f.SortOrder, f.Active,
		append([]string{}, f.RequiredBaits...), append([]string{}, f.Seasons...), f.HourStart, f.HourEnd,
		seasons.Describe(f.Seasons, f.HourStart, f.HourEnd)}
}

type FishView struct {
	ID          int64
	Code        string
	Name        string
	Emoji       string
	Rarity      string
	RarityName  string
	RarityEmoji string
	WeightG     int
	WeightText  string
	Price       int
	CaughtAt    time.Time
	SoldAt      *time.Time
	DonatedAt   *time.Time
}

func NewFishView(fish models.FarmFish, t models.FishType) FishView {
	return FishView{fish.ID, fish.FishCode, t.Name, t.Emoji, fish.Rarity, fishing.RarityNames[fish.Rarity],
		fishing.RarityEmoji[fish.Rarity], fish.WeightG, fishing.WeightText(fish.WeightG), fish.Price, fish.CaughtAt,
		fish.SoldAt, fish.DonatedAt}
}

// RecordFlags 這隻魚是不是同種魚／全部魚中最重的。
type RecordFlags struct {
	Species bool
	Overall bool
}

type GuardOption struct {
	Code  string
	Emoji string
}

// FishingView 釣魚畫面：一般是 Session 的狀態；人機確認時 Phase = check（Target／Options／ExpiresAt／Note），
// 答錯時 Phase = check_failed（Minutes／LockedUntil）。
type FishingView struct {
	ID              int64
	Status          string
	StatusName      string
	Phase           string // waiting／bite／fighting／ended／check／check_failed
	Turn            int
	StartedAt       time.Time
	BiteAt          time.Time
	ReactDeadline   time.Time
	ActionDeadline  *time.Time
	EndedAt         *time.Time
	Rarity          string
	RarityName      string
	RarityEmoji     string
	Rod, Line, Bait *string
	Stamina         float64
	MaxStamina      float64
	Distance        float64
	EscapeDistance  int
	Tension         float64
	MaxTension      float64
	Intent          string // "" = None
	IntentText      string
	LastText        *string
	Fish            *FishView
	Record          *RecordFlags
	Dodge           string // "" = None
	DodgeText       string
	ReelStreak      float64
	NextReelTension int
	LandStamina     int
	// 結算畫面（Phase "ended"）才有：目前裝備的魚餌還剩多少（nil = 沒有裝備魚餌）、魚簍數量與容量
	Ended          bool
	BaitLeft       *BaitLeft
	Basket         int
	BasketCapacity int
	Need           int  // 要成功拉幾下（舊 Session 為 0）
	Progress       int  // 已成功拉幾下
	PullsLeft      int  // 還要拉幾下
	Stale          bool // 畫面已經更新過（重複點擊），這次操作沒有執行

	// 人機確認
	Note        string
	ExpiresAt   *time.Time
	Target      string
	Options     []GuardOption
	Minutes     int
	LockedUntil *time.Time
}

// PolicyState 畫面上的拉扯狀態（Python {k: view[k] for k in POLICY_KEYS}），供策略函式使用。
func (v FishingView) PolicyState() fishing.State {
	s := fishing.State{Intent: v.Intent, Tension: v.Tension, Distance: v.Distance, Stamina: v.Stamina,
		MaxStamina: v.MaxStamina, MaxTension: v.MaxTension, ReelStreak: v.ReelStreak, Turn: v.Turn}
	if v.Dodge != "" {
		d := v.Dodge
		s.Dodge = &d
	}
	return s
}

type FishingOverview struct {
	PlayerName      string
	Level           int
	Gold            int
	Rod, Line, Bait *GearView
	BaitQuantity    int
	Basket          int
	BasketCapacity  int
	Active          bool
	Stats           fishrepo.SessionStats
	SettingsVersion int
	BaseSuccessPct  int
	Chances         map[string]float64
}

type FishingShop struct {
	Gold  int
	Level int
	Items []GearView
}

type BasketFish struct {
	FishView
	Record bool // 同種魚中最重
}

type FishBasket struct {
	Fish       []BasketFish
	Capacity   int
	Gold       int
	TotalValue int
}

// SharedFish 分享的魚（展示用）。
type SharedFish struct {
	FishView
	PlayerName string
	Record     RecordFlags
}

type FishCodexEntry struct {
	FishTypeView
	Caught     bool
	Count      int
	BestWeight *int // Python max_weight_g（捕獲的最大重量；沒捕獲過時 nil）
	BaitNames  []string
}

type FishCodex struct {
	Entries    []FishCodexEntry
	Discovered int
	Total      int
}

// ---------- 內部工具 ----------

func (f *FishingService) contextGroup(ctx context.Context, t *db.Tx, farm *models.Farm) (*int64, error) {
	c := f.contextGroupID
	if c == nil {
		return nil, nil
	}
	member, err := groups.IsMember(ctx, t, *c, farm.PlayerID)
	if err != nil || !member {
		return nil, err
	}
	return c, nil
}

func finish(row *models.FishingSession, status string, at time.Time) {
	row.Status = status
	row.EndedAt = &at
	row.ActionDeadline = nil
}

func saveSession(ctx context.Context, t *db.Tx, row *models.FishingSession) error {
	_, err := db.UpdateByID(ctx, t, "fishing_sessions", row.ID, db.C("status", row.Status), db.C("state", string(row.State)),
		db.C("turns", row.Turns), db.C("action_deadline", row.ActionDeadline), db.C("ended_at", row.EndedAt))
	return err
}

func grace() time.Duration { return secondsDur(fishing.ReactionGraceSeconds) }

// expire 依時間判定進行中的 Session 是否已失敗（沒有及時拉竿、操作逾時、超過最長時間），有變更時寫回。
// 回傳仍進行中的 Session（已結束時 nil）。
func expire(ctx context.Context, t *db.Tx, row *models.FishingSession, now time.Time) (*models.FishingSession, error) {
	if row == nil || !slices.Contains(fishing.ActiveStatuses, row.Status) {
		return row, nil
	}
	var err error
	changed := true
	switch {
	case now.After(row.StartedAt.Add(fishing.SessionMaxSeconds * time.Second)):
		finish(row, "abandoned", now)
	case row.Status == "casting" && now.After(row.ReactDeadline.Add(grace())):
		if row.State, err = withLast(row.State, "🐟 你沒有及時拉竿，魚把餌吃掉跑了…"); err != nil {
			return nil, err
		}
		finish(row, "missed", row.ReactDeadline)
	case row.Status == "fighting" && row.ActionDeadline != nil && now.After(row.ActionDeadline.Add(grace())):
		if row.State, err = withLast(row.State, "⌛ 太久沒有操作，魚趁機掙脫逃走了…"); err != nil {
			return nil, err
		}
		finish(row, "timeout", *row.ActionDeadline)
	default:
		changed = false
	}
	if changed {
		if err := saveSession(ctx, t, row); err != nil {
			return nil, err
		}
	}
	if slices.Contains(fishing.ActiveStatuses, row.Status) {
		return row, nil
	}
	return nil, nil
}

func needsExpire(row *models.FishingSession, now time.Time) bool {
	if now.After(row.StartedAt.Add(fishing.SessionMaxSeconds * time.Second)) {
		return true
	}
	if row.Status == "casting" {
		return now.After(row.ReactDeadline.Add(grace()))
	}
	return row.ActionDeadline != nil && now.After(row.ActionDeadline.Add(grace()))
}

func (f *FishingService) sessionView(ctx context.Context, t *db.Tx, row *models.FishingSession, now time.Time) (FishingView, error) {
	state, keys, err := decodeState(row.State)
	if err != nil {
		return FishingView{}, err
	}
	phase := "ended"
	switch row.Status {
	case "casting":
		phase = "waiting"
		if !now.Before(row.BiteAt) {
			phase = "bite"
		}
	case "fighting":
		phase = "fighting"
	}
	gear := decodeGear(row.Gear)
	name := func(kind string) *string {
		if g, ok := gear[kind]; ok && g.Name != "" {
			n := g.Name
			return &n
		}
		return nil
	}
	statusName, ok := fishing.StatusNames[row.Status]
	if !ok {
		statusName = row.Status
	}
	v := FishingView{ID: row.ID, Status: row.Status, StatusName: statusName, Phase: phase, Turn: state.Turn,
		StartedAt: row.StartedAt, BiteAt: row.BiteAt, ReactDeadline: row.ReactDeadline, ActionDeadline: row.ActionDeadline,
		EndedAt: row.EndedAt, Rarity: row.Rarity, RarityName: fishing.RarityNames[row.Rarity],
		RarityEmoji: fishing.RarityEmoji[row.Rarity], Rod: name("rod"), Line: name("line"), Bait: name("bait"),
		Stamina: state.Stamina, MaxStamina: state.MaxStamina, Distance: state.Distance, EscapeDistance: fishing.EscapeDistance,
		Tension: state.Tension, MaxTension: fishing.BaseMaxTension, Intent: state.Intent,
		IntentText: fishing.IntentText[state.Intent], Dodge: state.DodgeDir(), DodgeText: fishing.DodgeText[state.DodgeDir()],
		ReelStreak: state.ReelStreak, Need: state.Need, Progress: state.Progress, PullsLeft: fishing.PullsLeft(state)}
	if _, ok := keys["max_tension"]; ok {
		v.MaxTension = state.MaxTension
		v.NextReelTension = numfmt.Round(fishing.ReelTension(state))
	}
	if _, ok := keys["max_stamina"]; ok {
		v.LandStamina = fishing.LandStamina(state)
	}
	if state.Last != nil {
		text := state.Last.Text
		v.LastText = &text
	}
	if row.Status == "landed" {
		sf, err := fishrepo.SessionFish(ctx, t, row.ID)
		if err != nil || sf == nil {
			return v, err
		}
		got, err := fishrepo.GetFarmFish(ctx, t, row.FarmID, sf.ID)
		if err != nil || got == nil {
			return v, err
		}
		fv := NewFishView(got.Fish, got.Type)
		v.Fish = &fv
		flags, err := recordFlags(ctx, t, row.FarmID, got.Fish)
		if err != nil {
			return v, err
		}
		v.Record = &flags
	}
	if phase == "ended" { // 結算畫面：目前裝備的魚餌還剩多少、魚簍還有多少空間
		v.Ended = true
		equipped, err := fishrepo.EquippedGear(ctx, t, row.FarmID)
		if err != nil {
			return v, err
		}
		if e, ok := equipped["bait"]; ok {
			v.BaitLeft = &BaitLeft{Name: e.Gear.Name, Quantity: e.Owned.Quantity}
		}
		if v.Basket, err = fishrepo.BasketCount(ctx, t, row.FarmID); err != nil {
			return v, err
		}
		_, raw, err := fishrepo.CurrentSettings(ctx, t)
		if err != nil {
			return v, err
		}
		settings, err := decodeSettings(raw)
		if err != nil {
			return v, err
		}
		v.BasketCapacity = settings.BasketCapacity
	}
	return v, nil
}

// BaitLeft 結算畫面上目前裝備的魚餌（Python view["bait_left"]）。
type BaitLeft struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

func recordFlags(ctx context.Context, t *db.Tx, farmID int64, fish models.FarmFish) (RecordFlags, error) {
	best, err := fishrepo.BestWeights(ctx, t, farmID)
	if err != nil {
		return RecordFlags{}, err
	}
	top := 0
	for _, w := range best {
		top = max(top, w)
	}
	w, ok := best[fish.FishCode]
	return RecordFlags{Species: ok && w == fish.WeightG, Overall: top == fish.WeightG}, nil
}

func (f *FishingService) settings(ctx context.Context, t *db.Tx) (int, json.RawMessage, fishing.Settings, error) {
	version, raw, err := fishrepo.CurrentSettings(ctx, t)
	if err != nil {
		return 0, nil, fishing.Settings{}, err
	}
	s, err := decodeSettings(raw)
	return version, raw, s, err
}

// ---------- 釣魚中心 ----------

func (f *FishingService) FishingOverview(ctx context.Context, playerID int64) (FishingOverview, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingOverview, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return FishingOverview{}, err
		}
		player, err := players.Get(ctx, t, farm.PlayerID, false)
		if err != nil {
			return FishingOverview{}, err
		}
		version, _, settings, err := f.settings(ctx, t)
		if err != nil {
			return FishingOverview{}, err
		}
		equipped, err := fishrepo.EquippedGear(ctx, t, farm.ID)
		if err != nil {
			return FishingOverview{}, err
		}
		active, err := fishrepo.ActiveSession(ctx, t, farm.ID, false)
		if err != nil {
			return FishingOverview{}, err
		}
		expired := false
		if active != nil {
			if active.Status == "casting" {
				expired = now.After(active.ReactDeadline)
			} else {
				expired = active.ActionDeadline != nil && now.After(*active.ActionDeadline)
			}
		}
		ov := FishingOverview{PlayerName: player.DisplayName, Level: farm.Level, Gold: farm.Gold,
			BasketCapacity: settings.BasketCapacity, Active: active != nil && !expired, SettingsVersion: version,
			BaseSuccessPct: settings.BaseSuccessPct}
		view := func(kind string) *GearView {
			e, ok := equipped[kind]
			if !ok {
				return nil
			}
			v := NewGearView(e.Gear, &e.Owned, nil)
			return &v
		}
		ov.Rod, ov.Line, ov.Bait = view("rod"), view("line"), view("bait")
		baitEffects := fishing.Effects{}
		if b, ok := equipped["bait"]; ok {
			ov.BaitQuantity, baitEffects = b.Owned.Quantity, b.Gear.Effects
		}
		ov.Chances = fishing.RarityChances(settings.RarityBP, baitEffects)
		if ov.Basket, err = fishrepo.BasketCount(ctx, t, farm.ID); err != nil {
			return FishingOverview{}, err
		}
		ov.Stats, err = fishrepo.PlayerSessionStats(ctx, t, farm.ID)
		return ov, err
	})
}

// ---------- 釣具商店與裝備 ----------

func (f *FishingService) FishingShop(ctx context.Context, playerID int64) (FishingShop, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingShop, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return FishingShop{}, err
		}
		ownedList, err := fishrepo.OwnedGear(ctx, t, farm.ID)
		if err != nil {
			return FishingShop{}, err
		}
		owned := map[string]*models.FarmFishingGear{}
		for i := range ownedList {
			owned[ownedList[i].GearCode] = &ownedList[i]
		}
		gear, err := fishrepo.ListGear(ctx, t, false)
		if err != nil {
			return FishingShop{}, err
		}
		shop := FishingShop{Gold: farm.Gold, Level: farm.Level, Items: []GearView{}}
		for _, g := range gear {
			if o := owned[g.Code]; g.Active || o != nil {
				shop.Items = append(shop.Items, NewGearView(g, o, &farm.Level))
			}
		}
		return shop, nil
	})
}

func validInt(value int, label string, low, high int) (int, error) {
	if value < low || value > high {
		return 0, gameerr.Validationf("%s必須是 %d–%d 的整數。", label, low, high)
	}
	return value, nil
}

// BuyFishingGear quantity 預設 1。
func (f *FishingService) BuyFishingGear(ctx context.Context, playerID int64, code string, quantity int) (ActionResult, error) {
	quantity, err := validInt(quantity, "購買數量", 1, fishing.BaitMaxBuy)
	if err != nil {
		return ActionResult{}, err
	}
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		gear, err := fishrepo.GetGear(ctx, t, code, true) // 鎖定商品列，庫存不會被超賣
		if err != nil {
			return ActionResult{}, err
		}
		if gear == nil || !gear.Active {
			return ActionResult{}, gameerr.NotFoundf("釣具商店沒有這個商品。")
		}
		if farm.Level < gear.RequiredLevel {
			return ActionResult{}, gameerr.Validationf("%s需要農場 Lv.%d 才能購買（目前 Lv.%d）。", gear.Name, gear.RequiredLevel, farm.Level)
		}
		owned, err := fishrepo.GetOwned(ctx, t, farm.ID, gear.Code, true)
		if err != nil {
			return ActionResult{}, err
		}
		if gear.Kind != "bait" {
			if quantity != 1 {
				return ActionResult{}, gameerr.Validationf("%s一次只能買 1 個。", gear.Name)
			}
			if owned != nil {
				return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有%s了。", gear.Name)
			}
		} else {
			have := 0
			if owned != nil {
				have = owned.Quantity
			}
			if have+quantity > fishing.BaitMaxHold {
				return ActionResult{}, gameerr.Validationf("魚餌最多持有 %d 個。", fishing.BaitMaxHold)
			}
		}
		if gear.Stock != nil && *gear.Stock < quantity {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s庫存不足（剩 %d 個）。", gear.Name, *gear.Stock)
		}
		cost := gear.Price * quantity
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		farm.Gold -= cost
		if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if gear.Stock != nil {
			stock := *gear.Stock - quantity
			gear.Stock = &stock
			if _, err := db.UpdateByID(ctx, t, "fishing_gear", gear.ID, db.C("stock", stock)); err != nil {
				return ActionResult{}, err
			}
		}
		now := f.now()
		equipped, err := fishrepo.EquippedGear(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		_, has := equipped[gear.Kind]
		autoEquip := !has
		if owned == nil {
			if _, err := fishrepo.AddOwned(ctx, t, farm.ID, *gear, quantity, now, autoEquip); err != nil {
				return ActionResult{}, err
			}
		} else {
			owned.Quantity += quantity
			owned.Equipped = owned.Equipped || autoEquip
			if _, err := db.UpdateByID(ctx, t, "farm_fishing_gear", owned.ID, db.C("quantity", owned.Quantity),
				db.C("equipped", owned.Equipped)); err != nil {
				return ActionResult{}, err
			}
		}
		price := gear.Price
		if err := f.record(ctx, t, farm, rules.EvFishingBuy, fmt.Sprintf("購買%s ×%d，花費 %d 金幣", gear.Name, quantity, cost),
			rec{ItemCode: gear.Code, Quantity: quantity, GoldDelta: -cost, UnitPrice: &price}); err != nil {
			return ActionResult{}, err
		}
		note := ""
		if autoEquip {
			note = "，已自動裝備"
		}
		return result(fmt.Sprintf("%s 買了 %s ×%d，花費 %d 金幣%s。", gear.Emoji, gear.Name, quantity, cost, note),
			farm.Gold, -cost, gear.Code, quantity), nil
	})
}

func (f *FishingService) EquipFishingGear(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		owned, err := fishrepo.GetOwned(ctx, t, farm.ID, code, true)
		if err != nil {
			return ActionResult{}, err
		}
		gear, err := fishrepo.GetGear(ctx, t, code, false)
		if err != nil {
			return ActionResult{}, err
		}
		if owned == nil || gear == nil {
			return ActionResult{}, gameerr.NotFoundf("你沒有這個釣具，請先到釣具商店購買。")
		}
		if gear.Kind == "bait" && owned.Quantity <= 0 {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s已經用完了，請先購買。", gear.Name)
		}
		if owned.Equipped {
			return result(fmt.Sprintf("%s已經裝備中。", gear.Name), farm.Gold, 0, "", 0), nil
		}
		if err := fishrepo.UnequipKind(ctx, t, farm.ID, gear.Kind); err != nil {
			return ActionResult{}, err
		}
		if _, err := db.UpdateByID(ctx, t, "farm_fishing_gear", owned.ID, db.C("equipped", true)); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("%s 已裝備%s：%s。", gear.Emoji, fishing.GearKindNames[gear.Kind], gear.Name), farm.Gold, 0, "", 0), nil
	})
}

// ---------- 釣魚 ----------

// FishingState 目前進行中的釣魚，或最近一次釣魚的結果；先依時間判定逾時。沒有任何紀錄時回傳 nil。
func (f *FishingService) FishingState(ctx context.Context, playerID int64) (*FishingView, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (*FishingView, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		row, err := fishrepo.ActiveSession(ctx, t, farm.ID, false)
		if err != nil {
			return nil, err
		}
		if row != nil {
			if needsExpire(row, now) {
				if farm, err = f.lockedFarm(ctx, t, playerID); err != nil {
					return nil, err
				}
				if row, err = fishrepo.ActiveSession(ctx, t, farm.ID, true); err != nil {
					return nil, err
				}
				if row != nil {
					if _, err := expire(ctx, t, row, now); err != nil {
						return nil, err
					}
				}
			}
		}
		if row == nil {
			if row, err = fishrepo.LastSession(ctx, t, farm.ID); err != nil || row == nil {
				return nil, err
			}
		}
		v, err := f.sessionView(ctx, t, row, now)
		return &v, err
	})
}

// StartFishing 拋竿：檢查裝備、魚餌與魚簍空間，扣 1 個魚餌，決定魚種與上鉤時間。
// 需要人機確認時不拋竿，回傳題目（Phase = check），由 AnswerFishingCheck 作答。
func (f *FishingService) StartFishing(ctx context.Context, playerID int64) (FishingView, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FishingView{}, err
		}
		if err := f.ensureNotFishing(ctx, t, farm, now); err != nil {
			return FishingView{}, err
		}
		if fishing.GuardEnabled {
			guard, err := f.guardRow(ctx, t, farm)
			if err != nil {
				return FishingView{}, err
			}
			if guard.LockedUntil != nil && now.Before(*guard.LockedUntil) {
				left := max(1, int(math.Floor(guard.LockedUntil.Sub(now).Seconds()/60))+1)
				return FishingView{}, gameerr.Validationf("人機確認沒有通過，釣魚暫停中，約 %d 分鐘後才能再拋竿。", left)
			}
			if guardNeedsCheck(guard) {
				return guardCheckView(ctx, t, guard, now, "")
			}
			guard.CastsSinceCheck++
			if err := saveGuard(ctx, t, guard); err != nil {
				return FishingView{}, err
			}
		}
		return f.cast(ctx, t, farm, now)
	})
}

func (f *FishingService) ensureNotFishing(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) error {
	row, err := fishrepo.ActiveSession(ctx, t, farm.ID, true)
	if err != nil {
		return err
	}
	if row, err = expire(ctx, t, row, now); err != nil {
		return err
	}
	if row != nil {
		return gameerr.Validationf("你已經在釣魚了，請先完成這一次。")
	}
	return nil
}

func (f *FishingService) cast(ctx context.Context, t *db.Tx, farm *models.Farm, now time.Time) (FishingView, error) {
	rng := fishing.RNG(f.FishingRng)
	version, rawSettings, settings, err := f.settings(ctx, t)
	if err != nil {
		return FishingView{}, err
	}
	equipped, err := fishrepo.EquippedGear(ctx, t, farm.ID)
	if err != nil {
		return FishingView{}, err
	}
	var missing []string
	for _, k := range fishing.GearKinds {
		if _, ok := equipped[k]; !ok {
			missing = append(missing, fishing.GearKindNames[k])
		}
	}
	if len(missing) > 0 {
		return FishingView{}, gameerr.Validationf("還沒有裝備%s，請先到「🛒 釣具商店」購買並裝備。", strings.Join(missing, "、"))
	}
	bait := equipped["bait"]
	if bait.Owned.Quantity <= 0 {
		return FishingView{}, gameerr.New(gameerr.InsufficientItems, "%s用完了，請到釣具商店補充魚餌。", bait.Gear.Name)
	}
	capacity := settings.BasketCapacity
	n, err := fishrepo.BasketCount(ctx, t, farm.ID)
	if err != nil {
		return FishingView{}, err
	}
	if n >= capacity {
		return FishingView{}, gameerr.Validationf("魚簍已滿（%d 隻），請先賣掉一些魚再來釣。", capacity)
	}
	types, err := fishrepo.ListFishTypes(ctx, t, true)
	if err != nil {
		return FishingView{}, err
	}
	pools := map[string][]models.FishType{}
	for _, ft := range types { // 只放目前魚餌、季節與時段釣得到的魚
		if fishing.BaitAllows(ft.RequiredBaits, bait.Gear.Code) && seasons.Available(ft.Seasons, ft.HourStart, ft.HourEnd, now) {
			pools[ft.Rarity] = append(pools[ft.Rarity], ft)
		}
	}
	rarity := fishing.RollRarity(settings.RarityBP, bait.Gear.Effects, rng)
	ftype, ok := pickFish(pools, rarity, rng)
	if !ok {
		return FishingView{}, gameerr.Validationf("目前沒有可以釣的魚，請稍後再試。")
	}
	weight := fishing.RollWeight(ftype.MinWeightG, ftype.MaxWeightG, rng)
	price := fishing.FishPrice(ftype.MinPrice, ftype.MaxPrice, ftype.MinWeightG, ftype.MaxWeightG, weight)
	state := fishing.NewFight(settings, ftype.Rarity, ftype.Difficulty, equipped["line"].Gear.Effects, rng)
	biteAt := now.Add(secondsDur(fishing.BiteWaitSeconds(settings, bait.Gear.Effects, rng)))
	reactDeadline := biteAt.Add(secondsDur(fishing.ReactionSeconds(settings, equipped["rod"].Gear.Effects)))
	// 扣魚餌與建立 Session 在同一筆交易
	if _, err := db.UpdateByID(ctx, t, "farm_fishing_gear", bait.Owned.ID, db.C("quantity", bait.Owned.Quantity-1)); err != nil {
		return FishingView{}, err
	}
	snap := map[string]gearSnap{}
	for k, e := range equipped {
		snap[k] = gearSnap{e.Gear.Code, e.Gear.Name, e.Gear.Effects}
	}
	gearJSON, err := json.Marshal(snap)
	if err != nil {
		return FishingView{}, err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return FishingView{}, err
	}
	groupID, err := f.contextGroup(ctx, t, farm)
	if err != nil {
		return FishingView{}, err
	}
	// 部分唯一索引：並行拋竿時第二筆在這裡失敗並回滾（魚餌也不會被扣）
	var id int64
	if err := t.QueryRow(ctx, `INSERT INTO fishing_sessions (farm_id, status, settings_version, settings, gear, fish_code, rarity,
		weight_g, price, state, turns, started_at, bite_at, react_deadline, context_group_id)
		VALUES ($1, 'casting', $2, $3::json, $4::json, $5, $6, $7, $8, $9::json, 0, $10, $11, $12, $13) RETURNING id`,
		farm.ID, version, string(rawSettings), string(gearJSON), ftype.Code, ftype.Rarity, weight, price, string(stateJSON),
		now, biteAt, reactDeadline, groupID).Scan(&id); err != nil {
		return FishingView{}, err
	}
	row := &models.FishingSession{ID: id, FarmID: farm.ID, Status: "casting", SettingsVersion: version, Settings: rawSettings,
		Gear: gearJSON, FishCode: ftype.Code, Rarity: ftype.Rarity, WeightG: weight, Price: price, State: stateJSON,
		StartedAt: now, BiteAt: biteAt, ReactDeadline: reactDeadline, ContextGroupID: groupID}
	return f.sessionView(ctx, t, row, now)
}

// ---------- 防腳本（呼叫端都已鎖定農場列） ----------

func (f *FishingService) guardRow(ctx context.Context, t *db.Tx, farm *models.Farm) (*models.FishingGuard, error) {
	g, err := fishrepo.GetGuard(ctx, t, farm.ID)
	if err != nil || g != nil {
		return g, err
	}
	g = &models.FishingGuard{FarmID: farm.ID, CheckAfter: nextCheckAfter()}
	err = db.Insert(ctx, t, "fishing_guards", db.C("farm_id", g.FarmID), db.C("casts_since_check", 0),
		db.C("check_after", g.CheckAfter), db.C("suspicion", 0), db.C("consecutive_failures", 0), db.C("failed_total", 0),
		db.C("passed_total", 0))
	return g, err
}

func saveGuard(ctx context.Context, t *db.Tx, g *models.FishingGuard) error {
	var challenge any // NULL
	if len(g.Challenge) > 0 && string(g.Challenge) != "null" {
		challenge = string(g.Challenge)
	}
	_, err := t.Exec(ctx, `UPDATE fishing_guards SET casts_since_check = $2, check_after = $3, suspicion = $4,
		challenge = $5::json, challenge_expires_at = $6, locked_until = $7, consecutive_failures = $8, failed_total = $9,
		passed_total = $10 WHERE farm_id = $1`, g.FarmID, g.CastsSinceCheck, g.CheckAfter, g.Suspicion, challenge,
		g.ChallengeExpiresAt, g.LockedUntil, g.ConsecutiveFailures, g.FailedTotal, g.PassedTotal)
	return err
}

func guardChallenge(g *models.FishingGuard) *fishing.GuardChallenge {
	if len(g.Challenge) == 0 || string(g.Challenge) == "null" {
		return nil
	}
	var c fishing.GuardChallenge
	if json.Unmarshal(g.Challenge, &c) != nil {
		return nil
	}
	return &c
}

func guardNeedsCheck(g *models.FishingGuard) bool {
	return guardChallenge(g) != nil || g.CastsSinceCheck >= g.CheckAfter || g.Suspicion >= fishing.GuardSuspicionLimit
}

// guardCheckView 出題或沿用還沒過期的題目（寫回資料庫）；回傳的內容只有題目名稱與選項 emoji。
func guardCheckView(ctx context.Context, t *db.Tx, g *models.FishingGuard, now time.Time, note string) (FishingView, error) {
	c := guardChallenge(g)
	if c == nil || g.ChallengeExpiresAt == nil || !now.Before(*g.ChallengeExpiresAt) {
		nc := fishing.NewGuardChallenge(guardSample, guardShuffle)
		c = &nc
		data, err := json.Marshal(nc)
		if err != nil {
			return FishingView{}, err
		}
		expires := now.Add(fishing.GuardChallengeSeconds * time.Second)
		g.Challenge, g.ChallengeExpiresAt = data, &expires
		if err := saveGuard(ctx, t, g); err != nil {
			return FishingView{}, err
		}
	}
	v := FishingView{Phase: "check", Status: "check", Note: note, ExpiresAt: g.ChallengeExpiresAt,
		Target: fishing.GuardCreatures[c.Answer].Name}
	for _, code := range c.Options {
		v.Options = append(v.Options, GuardOption{code, fishing.GuardCreatures[code].Emoji})
	}
	return v, nil
}

// AnswerFishingCheck 回答人機確認：答對接著拋竿；答錯暫停釣魚（連續答錯加倍）；題目過期換一題、不算答錯。
func (f *FishingService) AnswerFishingCheck(ctx context.Context, playerID int64, code string) (FishingView, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FishingView{}, err
		}
		if err := f.ensureNotFishing(ctx, t, farm, now); err != nil {
			return FishingView{}, err
		}
		g, err := f.guardRow(ctx, t, farm)
		if err != nil {
			return FishingView{}, err
		}
		c := guardChallenge(g)
		if c == nil { // 重複點擊：題目已經答過，照一般拋竿流程走（暫停中會被擋下）
			if g.LockedUntil != nil && now.Before(*g.LockedUntil) {
				return FishingView{}, gameerr.Validationf("人機確認沒有通過，釣魚暫停中，請稍後再試。")
			}
			g.CastsSinceCheck++
			if err := saveGuard(ctx, t, g); err != nil {
				return FishingView{}, err
			}
			return f.cast(ctx, t, farm, now)
		}
		if g.ChallengeExpiresAt == nil || !now.Before(*g.ChallengeExpiresAt) {
			return guardCheckView(ctx, t, g, now, "⌛ 題目過期了，換一題。")
		}
		passed := code == c.Answer
		g.Challenge, g.ChallengeExpiresAt = nil, nil
		if !passed {
			g.ConsecutiveFailures++
			g.FailedTotal++
			minutes := fishing.GuardLockMinutesFor(g.ConsecutiveFailures)
			until := now.Add(time.Duration(minutes) * time.Minute)
			g.LockedUntil = &until
			if err := saveGuard(ctx, t, g); err != nil {
				return FishingView{}, err
			}
			slog.Warn("釣魚人機確認答錯", "farm_id", farm.ID, "consecutive", g.ConsecutiveFailures, "total", g.FailedTotal,
				"minutes", minutes)
			return FishingView{Phase: "check_failed", Status: "check_failed", Minutes: minutes, LockedUntil: g.LockedUntil}, nil
		}
		g.ConsecutiveFailures = 0
		g.PassedTotal++
		g.CastsSinceCheck = 1 // 這次答對後的拋竿也算一次
		g.CheckAfter = nextCheckAfter()
		g.Suspicion = 0
		g.LockedUntil = nil
		if err := saveGuard(ctx, t, g); err != nil {
			return FishingView{}, err
		}
		return f.cast(ctx, t, farm, now)
	})
}

// guardFlag 累積可疑分數：只會讓下一次拋竿要確認，誤判的代價只有多按一題。
func (f *FishingService) guardFlag(ctx context.Context, t *db.Tx, farm *models.Farm, reason string) error {
	g, err := f.guardRow(ctx, t, farm)
	if err != nil {
		return err
	}
	g.Suspicion += fishing.GuardScore[reason]
	if g.Suspicion >= fishing.GuardSuspicionLimit {
		slog.Info("釣魚操作時間異常", "farm_id", farm.ID, "reason", reason, "suspicion", g.Suspicion)
	}
	return saveGuard(ctx, t, g)
}

// activeForAction 進行中的 Session；這次釣魚已經結束（例如重複點擊）時回傳 nil（呼叫端回傳最新狀態）。
func (f *FishingService) activeForAction(ctx context.Context, t *db.Tx, farm *models.Farm, sessionID int64) (*models.FishingSession, error) {
	row, err := fishrepo.ActiveSession(ctx, t, farm.ID, true)
	if err != nil {
		return nil, err
	}
	if row == nil || row.ID != sessionID {
		last, err := fishrepo.LastSession(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		if last != nil && last.ID == sessionID {
			return nil, nil
		}
		return nil, gameerr.NotFoundf("找不到這次釣魚，請重新開始。")
	}
	return row, nil
}

// target Python `row or last_session(...)`。
func (f *FishingService) target(ctx context.Context, t *db.Tx, farm *models.Farm, row *models.FishingSession, now time.Time) (FishingView, error) {
	if row == nil {
		var err error
		if row, err = fishrepo.LastSession(ctx, t, farm.ID); err != nil {
			return FishingView{}, err
		}
	}
	return f.sessionView(ctx, t, row, now)
}

// HookFish 拉竿：魚上鉤後、反應時間內拉竿才會進入拉扯；太早拉竿不會有任何效果。
func (f *FishingService) HookFish(ctx context.Context, playerID, sessionID int64) (FishingView, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FishingView{}, err
		}
		row, err := f.activeForAction(ctx, t, farm, sessionID)
		if err != nil {
			return FishingView{}, err
		}
		if row != nil {
			active, err := expire(ctx, t, row, now)
			if err != nil {
				return FishingView{}, err
			}
			if active != nil && row.Status == "casting" { // 已經在拉扯中（重複點擊拉竿）時不做任何事
				if now.Before(row.BiteAt) {
					return FishingView{}, gameerr.Validationf("魚還沒上鉤，再等一下！")
				}
				if fishing.GuardEnabled && now.Sub(row.BiteAt).Seconds() < fishing.GuardFastHookSeconds {
					if err := f.guardFlag(ctx, t, farm, "fast_hook"); err != nil {
						return FishingView{}, err
					}
				}
				settings, err := decodeSettings(row.Settings)
				if err != nil {
					return FishingView{}, err
				}
				row.Status = "fighting"
				deadline := now.Add(time.Duration(settings.ActionTimeoutSeconds) * time.Second)
				row.ActionDeadline = &deadline
				if row.State, err = withLast(row.State, "‼️ 拉竿成功，魚上鉤了！開始拉扯！"); err != nil {
					return FishingView{}, err
				}
				if err := saveSession(ctx, t, row); err != nil {
					return FishingView{}, err
				}
			}
		}
		return f.target(ctx, t, farm, row, now)
	})
}

// FishingAction 拉扯操作（收線／放線／等待／左壓／右壓）。turn 必須等於畫面上的回合數，避免重複點擊或過期畫面重複執行。
func (f *FishingService) FishingAction(ctx context.Context, playerID, sessionID int64, action string, turn int) (FishingView, error) {
	if !slices.Contains(fishing.Actions, action) {
		return FishingView{}, gameerr.Validationf("沒有這個操作。")
	}
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FishingView{}, err
		}
		row, err := f.activeForAction(ctx, t, farm, sessionID)
		if err != nil {
			return FishingView{}, err
		}
		stale := false
		if row != nil {
			active, err := expire(ctx, t, row, now)
			if err != nil {
				return FishingView{}, err
			}
			if active != nil {
				if row.Status != "fighting" {
					return FishingView{}, gameerr.Validationf("請先拉竿讓魚上鉤。")
				}
				state, _, err := decodeState(row.State)
				if err != nil {
					return FishingView{}, err
				}
				if state.Turn != turn {
					stale = true // 畫面已經更新過（重複點擊），不重複執行
				} else if err := f.applyAction(ctx, t, farm, row, state, action, now); err != nil {
					return FishingView{}, err
				}
			}
		}
		v, err := f.target(ctx, t, farm, row, now)
		v.Stale = stale
		return v, err
	})
}

func (f *FishingService) applyAction(ctx context.Context, t *db.Tx, farm *models.Farm, row *models.FishingSession,
	state fishing.State, action string, now time.Time) error {
	settings, err := decodeSettings(row.Settings)
	if err != nil {
		return err
	}
	diff := settings.Difficulty[row.Rarity]
	rod := decodeGear(row.Gear)["rod"].Effects
	if rod == nil {
		rod = fishing.Effects{}
	}
	next, res, err := fishing.Step(state, action, diff, rod, fishing.RNG(f.FishingRng))
	if err != nil {
		return err
	}
	timeout := time.Duration(settings.ActionTimeoutSeconds) * time.Second
	if fishing.GuardEnabled && row.ActionDeadline != nil {
		// 記錄這次操作距離上一步多久：太快或整場節奏規律得不像人時累積可疑分數
		gap := now.Sub(row.ActionDeadline.Add(-timeout)).Seconds()
		if gap < fishing.GuardFastActionSeconds {
			if err := f.guardFlag(ctx, t, farm, "fast_action"); err != nil {
				return err
			}
		}
		gaps := append(slices.Clone(next.Gaps), numfmt.RoundN(gap, 3))
		if len(gaps) > fishing.GuardGapsKept {
			gaps = gaps[len(gaps)-fishing.GuardGapsKept:]
		}
		next.Gaps = gaps
		if res != "" && fishing.RegularRhythm(gaps) {
			if err := f.guardFlag(ctx, t, farm, "regular"); err != nil {
				return err
			}
		}
	}
	if row.State, err = mergeState(row.State, next); err != nil {
		return err
	}
	row.Turns = next.Turn
	if res == "" {
		deadline := now.Add(timeout)
		row.ActionDeadline = &deadline
	} else {
		finish(row, res, now)
	}
	if err := saveSession(ctx, t, row); err != nil {
		return err
	}
	if res == "landed" { // session_id 唯一：同一次釣魚不會發兩隻魚
		return db.Insert(ctx, t, "farm_fish", db.C("farm_id", farm.ID), db.C("session_id", row.ID),
			db.C("fish_code", row.FishCode), db.C("rarity", row.Rarity), db.C("weight_g", row.WeightG),
			db.C("price", row.Price), db.C("caught_at", now))
	}
	return nil
}

// GiveUpFishing 收竿放棄（已扣的魚餌不退還）。
func (f *FishingService) GiveUpFishing(ctx context.Context, playerID, sessionID int64) (FishingView, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishingView, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FishingView{}, err
		}
		row, err := f.activeForAction(ctx, t, farm, sessionID)
		if err != nil {
			return FishingView{}, err
		}
		if row != nil {
			active, err := expire(ctx, t, row, now)
			if err != nil {
				return FishingView{}, err
			}
			if active != nil {
				if row.State, err = withLast(row.State, "🎣 你收起魚竿，這次先不釣了。"); err != nil {
					return FishingView{}, err
				}
				finish(row, "abandoned", now)
				if err := saveSession(ctx, t, row); err != nil {
					return FishingView{}, err
				}
			}
		}
		return f.target(ctx, t, farm, row, now)
	})
}

// CleanupFishingSessions 把已超過期限仍標記為進行中的 Session 判定為失敗（Bot 啟動時呼叫）；回傳處理筆數。
func (f *FishingService) CleanupFishingSessions(ctx context.Context) (int, error) {
	now := f.now()
	return inTx(ctx, f.GameService, func(t *db.Tx) (int, error) {
		rows, err := fishrepo.StaleActiveSessions(ctx, t, now.Add(-grace()), 500)
		if err != nil {
			return 0, err
		}
		for i := range rows {
			if _, err := expire(ctx, t, &rows[i], now); err != nil {
				return 0, err
			}
		}
		return len(rows), nil
	})
}

// ---------- 魚簍與販售 ----------

func (f *FishingService) FishBasket(ctx context.Context, playerID int64) (FishBasket, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishBasket, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return FishBasket{}, err
		}
		_, _, settings, err := f.settings(ctx, t)
		if err != nil {
			return FishBasket{}, err
		}
		best, err := fishrepo.BestWeights(ctx, t, farm.ID)
		if err != nil {
			return FishBasket{}, err
		}
		rows, err := fishrepo.Basket(ctx, t, farm.ID)
		if err != nil {
			return FishBasket{}, err
		}
		out := FishBasket{Fish: []BasketFish{}, Capacity: settings.BasketCapacity, Gold: farm.Gold}
		for _, r := range rows {
			w, ok := best[r.Fish.FishCode]
			out.Fish = append(out.Fish, BasketFish{NewFishView(r.Fish, r.Type), ok && w == r.Fish.WeightG})
			out.TotalValue += r.Fish.Price
		}
		return out, nil
	})
}

// SellFish 販售指定的魚（fishIDs 為 nil 表示全部）。只能賣自己魚簍中尚未賣出的魚。
func (f *FishingService) SellFish(ctx context.Context, playerID int64, fishIDs []int64) (ActionResult, error) {
	if fishIDs != nil {
		if len(fishIDs) == 0 {
			return ActionResult{}, gameerr.Validationf("請選擇要賣的魚。")
		}
		if len(fishIDs) > fishing.SellMaxPerAction {
			return ActionResult{}, gameerr.Validationf("一次最多賣 %d 隻魚。", fishing.SellMaxPerAction)
		}
		for _, id := range fishIDs {
			if id <= 0 {
				return ActionResult{}, gameerr.Validationf("魚的編號不正確。")
			}
		}
		fishIDs = slices.Compact(slices.Sorted(slices.Values(fishIDs)))
	}
	return inTx(ctx, f.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		rows, err := fishrepo.LockUnsold(ctx, t, farm.ID, fishIDs)
		if err != nil {
			return ActionResult{}, err
		}
		if len(rows) == 0 {
			return ActionResult{}, gameerr.Validationf("魚簍裡沒有可以賣的魚。")
		}
		if fishIDs != nil && len(rows) != len(fishIDs) {
			return ActionResult{}, gameerr.Validationf("有些魚已經賣掉或不在你的魚簍裡，請重新整理後再試。")
		}
		now := f.now()
		total := 0
		ids := make([]int64, len(rows))
		codes := map[string]bool{}
		for i, r := range rows {
			ids[i] = r.ID
			total += r.Price
			codes[r.FishCode] = true
		}
		if _, err := t.Exec(ctx, "UPDATE farm_fish SET sold_at = $2, sold_price = price WHERE id = ANY($1)", ids, now); err != nil {
			return ActionResult{}, err
		}
		farm.Gold += total
		if err := f.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		types, err := fishrepo.ListFishTypes(ctx, t, false)
		if err != nil {
			return ActionResult{}, err
		}
		names := map[string]string{}
		for _, ft := range types {
			if codes[ft.Code] {
				names[ft.Code] = ft.Name
			}
		}
		seen := map[string]bool{}
		var detail []string
		for _, r := range rows {
			n, ok := names[r.FishCode]
			if !ok {
				n = r.FishCode
			}
			if !seen[n] {
				seen[n] = true
				detail = append(detail, n)
			}
		}
		sort.Strings(detail)
		r := rec{Quantity: len(rows), GoldDelta: total}
		if len(codes) == 1 {
			r.ItemCode = rows[0].FishCode
		}
		if len(rows) == 1 {
			price := rows[0].Price
			r.UnitPrice = &price
		}
		if err := f.record(ctx, t, farm, rules.EvFishSell,
			truncRunes(fmt.Sprintf("販售 %d 隻魚（%s），獲得 %d 金幣", len(rows), strings.Join(detail, "、"), total), 200), r); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("💰 賣出 %d 隻魚，獲得 %d 金幣。", len(rows), total), farm.Gold, total, "", len(rows)), nil
	})
}

// ---------- 分享與展示 ----------

// ShareFish 記錄分享並回傳展示用資料。分享不消耗、不轉移魚，也沒有獎勵；只能分享自己魚簍中的魚。
func (f *FishingService) ShareFish(ctx context.Context, playerID, fishID int64) (SharedFish, error) {
	if fishID <= 0 {
		return SharedFish{}, gameerr.Validationf("魚的編號不正確。")
	}
	return inTx(ctx, f.GameService, func(t *db.Tx) (SharedFish, error) {
		farm, err := f.lockedFarm(ctx, t, playerID)
		if err != nil {
			return SharedFish{}, err
		}
		groupID, err := f.contextGroup(ctx, t, farm)
		if err != nil {
			return SharedFish{}, err
		}
		if groupID == nil {
			return SharedFish{}, gameerr.Validationf("分享魚類需要在你所在的 Discord 伺服器中操作。")
		}
		got, err := fishrepo.GetFarmFish(ctx, t, farm.ID, fishID)
		if err != nil {
			return SharedFish{}, err
		}
		if got == nil || got.Fish.SoldAt != nil || got.Fish.DonatedAt != nil {
			return SharedFish{}, gameerr.NotFoundf("找不到這隻魚（可能已經賣掉了）。")
		}
		now := f.now()
		last, err := fishrepo.LastShareAt(ctx, t, got.Fish.ID)
		if err != nil {
			return SharedFish{}, err
		}
		if last != nil && now.Sub(*last).Seconds() < fishing.ShareCooldownSeconds {
			return SharedFish{}, gameerr.Validationf("剛剛已經分享過這隻魚了，稍等一下再分享。")
		}
		if _, err := fishrepo.AddShare(ctx, t, got.Fish.ID, farm.ID, groupID, now); err != nil {
			return SharedFish{}, err
		}
		player, err := players.Get(ctx, t, farm.PlayerID, false)
		if err != nil {
			return SharedFish{}, err
		}
		flags, err := recordFlags(ctx, t, farm.ID, got.Fish)
		return SharedFish{NewFishView(got.Fish, got.Type), player.DisplayName, flags}, err
	})
}

// BestFish 魚簍中最珍貴的魚（價值最高，同價取最重）；魚簍是空的時回傳 nil。
func (f *FishingService) BestFish(ctx context.Context, playerID int64) (*FishView, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (*FishView, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		rows, err := fishrepo.Basket(ctx, t, farm.ID)
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		v := NewFishView(rows[0].Fish, rows[0].Type)
		return &v, nil
	})
}

// ---------- 圖鑑與排行榜 ----------

func (f *FishingService) FishCodex(ctx context.Context, playerID int64) (FishCodex, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (FishCodex, error) {
		farm, err := f.farmOf(ctx, t, playerID)
		if err != nil {
			return FishCodex{}, err
		}
		caught, err := fishrepo.Codex(ctx, t, farm.ID)
		if err != nil {
			return FishCodex{}, err
		}
		gear, err := fishrepo.ListGear(ctx, t, false)
		if err != nil {
			return FishCodex{}, err
		}
		gearNames := map[string]string{}
		for _, g := range gear {
			gearNames[g.Code] = g.Name
		}
		types, err := fishrepo.ListFishTypes(ctx, t, false)
		if err != nil {
			return FishCodex{}, err
		}
		out := FishCodex{Entries: []FishCodexEntry{}}
		for _, ft := range types {
			c, ok := caught[ft.Code]
			if !ft.Active && !ok {
				continue
			}
			e := FishCodexEntry{FishTypeView: NewFishTypeView(ft), Caught: ok, BaitNames: []string{}}
			if ok {
				e.Count = c.Count
				w := c.MaxWeightG
				e.BestWeight = &w
				out.Discovered++
			}
			for _, b := range ft.RequiredBaits {
				n, ok := gearNames[b]
				if !ok {
					n = b
				}
				e.BaitNames = append(e.BaitNames, n)
			}
			out.Entries = append(out.Entries, e)
		}
		out.Total = len(out.Entries)
		return out, nil
	})
}

// FishingLeaderboard groupID 為 nil 時是全服排行。
func (f *FishingService) FishingLeaderboard(ctx context.Context, groupID *int64) (fishrepo.Leaderboard, error) {
	return inTx(ctx, f.GameService, func(t *db.Tx) (fishrepo.Leaderboard, error) {
		return fishrepo.LeaderboardOf(ctx, t, groupID, 10)
	})
}
