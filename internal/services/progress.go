package services

// 玩家長期進度：每日簽到、圖鑑、成就與稱號，以及商店的「特賣與升級」（溫室、每週特賣、稱號商店）
// （對應 Python app/services/progress_service.py）。
//
//   - 成就進度由既有資料即時計算（歷史進度也算），只有「已領取」存在 farm_achievements。
//   - 簽到與成就的獎勵都經 grantReward：道具放不進倉庫時改發等值金幣，所有資產變動都寫交易紀錄。
//
// 與 Python 不同：獎勵道具（JSON 物件）在 Go 是 map，發放與顯示順序依道具商店順序（checkin.SortedItemCodes）；
// Python 依 JSON 中的順序。倉庫只放得下部分道具時，哪一項改發金幣可能因此不同（預設獎勵每次只有一種道具，不受影響）。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/codex"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/feedback"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/titles"
)

// CodexCategories 圖鑑分類：(代碼, 圖示, 名稱)。
var CodexCategories = []struct{ Key, Emoji, Name string }{
	{"crop", "🌾", "作物"},
	{"animal", "🐔", "動物產品"},
	{"goods", "🏭", "加工品"},
	{"material", "⛏️", "山洞素材"},
	{"fish", "🐟", "魚類"},
}

// TitleSources 稱號來源的顯示名稱（成就以外）。
var TitleSources = map[string]string{"world_boss": "世界 Boss", "shop": "稱號商店", "admin": "管理員"}

// AwayMinSeconds 離開超過 30 分鐘才顯示「你不在的時候」。
const AwayMinSeconds = 1800

// qualityName 圖鑑的最佳品質（0–3）顯示名稱：普通、優良、稀有、傳說。
func qualityName(rank int) string {
	if rank < 0 || rank >= len(balance.QualityOrder) {
		return ""
	}
	return balance.Qualities[balance.QualityOrder[rank]]
}

// TitleSource 稱號來源的顯示名稱：成就名稱、世界 Boss、稱號商店或管理員。
func TitleSource(source string) string {
	if code, ok := strings.CutPrefix(source, "achievement:"); ok {
		if a, ok := achievements.BaseByCode[code]; ok {
			return a.Name
		}
		return source
	}
	if name, ok := TitleSources[source]; ok {
		return name
	}
	return source
}

func pct(value, total int) int {
	if total <= 0 {
		return 0
	}
	return value * 100 / total
}

// ---------- 回傳資料 ----------

// CheckinResult 每日簽到的結果；New 為 false 時只有 NextAt。
type CheckinResult struct {
	New        bool
	Streak     int
	RewardText string
	Converted  bool
	NextAt     time.Time
}

// CheckinStatus 今天是否已簽到、目前連續天數、下一次簽到的獎勵。
type CheckinStatus struct {
	CheckedToday   bool      `json:"checked_today"`
	Streak         int       `json:"streak"`
	NextRewardText string    `json:"next_reward_text"`
	NextAt         time.Time `json:"next_at"`
}

// CodexEntry 圖鑑項目；BestQuality 為 "" 表示沒有（Python None）。Variant 只有作物會用到。
type CodexEntry struct {
	Code        string
	Name        string
	Got         bool
	Quantity    int
	BestQuality string
	Variant     bool
}

type CodexCategory struct {
	Key, Emoji, Name  string
	Entries           []CodexEntry
	Discovered, Total int
}

type CodexView struct {
	Categories        []CodexCategory
	Discovered, Total int
}

// AchievementRow State：claimed（已領取）／ready（可領取）／progress（進行中）。
type AchievementRow struct {
	Code        string     `json:"code"`
	Category    string     `json:"category"`
	Emoji       string     `json:"emoji"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Value       int        `json:"value"`
	Target      int        `json:"target"`
	Unit        string     `json:"unit"`
	State       string     `json:"state"`
	RewardText  string     `json:"reward_text"`
	Title       string     `json:"title"` // "" = 沒有稱號（Python 為 None；管理網站不顯示這個欄位）
	ClaimedAt   *time.Time `json:"claimed_at"`
}

type AchievementView struct {
	Rows                  []AchievementRow
	Claimed, Ready, Total int
}

type TitleInfo struct {
	Title      string    `json:"title"`
	ObtainedAt time.Time `json:"obtained_at"`
	Source     string    `json:"source"`
}

type TitlesView struct {
	Current *string
	Owned   []TitleInfo
}

type ProgressOverview struct {
	Codex        CodexView
	Achievements AchievementView
	Checkin      CheckinStatus
	Titles       TitlesView
}

// ProgressMarks 已登記的圖鑑項目（"分類:代碼" → 名稱）與可領取的成就（代碼 → 「圖示 名稱」）。
type ProgressMarks struct {
	Codex      map[string]string
	CodexTotal int
	Ready      map[string]string
	// 加入的順序（Python dict 的順序；面板列出「新圖鑑」「成就達成」時依此順序）
	CodexOrder []string
	ReadyOrder []string
}

type WelcomeBack struct {
	Lines []string
	Marks ProgressMarks
}

type GreenhouseInfo struct {
	Level, Max, Pct int
	NextPrice       *int // nil = 已滿級
	NextPct         int
	PerLevel        int
}

type WeeklyOfferView struct {
	CropCode, Name       string
	Price, OriginalPrice int
	Limit                int
	Rare                 bool
	Bought               int
}

type WeeklyView struct {
	WeekStart, EndsAt time.Time
	Offers            []WeeklyOfferView
}

type TitleShopItem struct {
	Title string
	Price int
	Owned bool
}

type SpecialShopView struct {
	Gold       int
	Greenhouse GreenhouseInfo
	Weekly     WeeklyView
	Titles     []TitleShopItem
}

// ---------- 服務 ----------

type ProgressService struct {
	*NewsService
}

func NewProgressService(d *db.DB, opt Options) *ProgressService {
	return &ProgressService{NewNewsService(d, opt)}
}

func (p *ProgressService) InGroupContext(groupID *int64) *ProgressService {
	return &ProgressService{p.NewsService.InGroupContext(groupID)}
}

// ---------- 共用：發放獎勵 ----------

// grantReward 發放金幣與道具（呼叫端已鎖農場）；道具放不進倉庫時改發等值金幣（道具商店價格）。
// 回傳 (實際金幣, 實際道具, 是否有道具改發金幣)。
func (p *ProgressService) grantReward(ctx context.Context, t *db.Tx, farm *models.Farm, ev rules.EventType, label string,
	gold int, items map[string]int) (int, map[string]int, bool, error) {
	given, converted := map[string]int{}, false
	used, err := inventory.TotalQuantity(ctx, t, farm.ID)
	if err != nil {
		return 0, nil, false, err
	}
	space := farm.WarehouseCapacity - used
	codes := checkin.SortedItemCodes(items)
	var order []string
	for _, code := range codes {
		qty := items[code]
		if qty <= space {
			given[code] = qty
			order = append(order, code)
			space -= qty
		} else {
			converted = true
			gold += balance.Items[code].Price * qty
		}
	}
	for _, code := range order {
		qty := given[code]
		if err := inventory.Add(ctx, t, farm.ID, code, qty, "normal", "none"); err != nil {
			return 0, nil, false, err
		}
		desc := truncRunes(fmt.Sprintf("%s：獲得%s ×%d", label, balance.Items[code].Name, qty), 200)
		if err := p.record(ctx, t, farm, ev, desc, rec{ItemCode: code, Quantity: qty}); err != nil {
			return 0, nil, false, err
		}
	}
	if gold != 0 {
		farm.Gold += gold
		if err := p.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return 0, nil, false, err
		}
		note := ""
		if converted {
			note = "（倉庫已滿，道具改發金幣）"
		}
		if err := p.record(ctx, t, farm, ev, truncRunes(fmt.Sprintf("%s：金幣 +%d%s", label, gold, note), 200),
			rec{GoldDelta: gold}); err != nil {
			return 0, nil, false, err
		}
	}
	return gold, given, converted, nil
}

// ---------- 每日簽到 ----------

func (p *ProgressService) taipeiToday() time.Time {
	y, m, d := p.now().In(clock.TaipeiTZ).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, clock.TaipeiTZ)
}

// nextCheckinAt 下一次可以簽到的時間（台灣時間隔天 00:00）。
func nextCheckinAt(today time.Time) time.Time { return today.AddDate(0, 0, 1) }

func lastCheckin(ctx context.Context, t *db.Tx, farmID int64) (*models.DailyCheckin, error) {
	return db.QueryStruct[models.DailyCheckin](ctx, t,
		"SELECT * FROM daily_checkins WHERE farm_id = $1 ORDER BY day DESC LIMIT 1", farmID)
}

func farmIDOf(ctx context.Context, t *db.Tx, playerID int64) (*int64, error) {
	id, ok, err := db.QueryValue[int64](ctx, t, "SELECT id FROM farms WHERE player_id = $1", playerID)
	if err != nil || !ok {
		return nil, err
	}
	return &id, nil
}

// DailyCheckin 今天還沒簽到就自動簽到並發放獎勵；還沒有農場時回傳 nil。今天已簽到時不鎖農場。
func (p *ProgressService) DailyCheckin(ctx context.Context, playerID int64) (*CheckinResult, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (*CheckinResult, error) {
		today := p.taipeiToday()
		farmID, err := farmIDOf(ctx, t, playerID)
		if err != nil || farmID == nil {
			return nil, err
		}
		already := &CheckinResult{New: false, NextAt: nextCheckinAt(today)}
		done := func() (bool, error) {
			var n int
			err := t.QueryRow(ctx, "SELECT count(*) FROM daily_checkins WHERE farm_id = $1 AND day = $2",
				*farmID, db.DateParam(today)).Scan(&n)
			return n > 0, err
		}
		if ok, err := done(); err != nil || ok {
			return already, err
		}
		farm, err := p.lockedFarm(ctx, t, playerID) // 鎖定後再確認一次，同時開兩個面板也只會簽到一次
		if err != nil {
			return nil, err
		}
		if ok, err := done(); err != nil || ok {
			return already, err
		}
		last, err := lastCheckin(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		var lastDay *time.Time
		lastStreak := 0
		if last != nil {
			d := db.TaipeiDate(last.Day)
			lastDay, lastStreak = &d, last.Streak
		}
		streak := checkin.NextStreak(lastDay, lastStreak, today)
		reward := checkin.RewardFor(streak)
		gold, items, converted, err := p.grantReward(ctx, t, farm, rules.EvCheckinReward,
			fmt.Sprintf("每日簽到（連續第 %d 天）", streak), reward.Gold, reward.Items)
		if err != nil {
			return nil, err
		}
		itemsJSON, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		// (farm_id, day) 唯一：萬一重複在這裡失敗並整筆回滾
		if _, err := t.Exec(ctx, "INSERT INTO daily_checkins (farm_id, day, streak, gold, items, created_at) "+
			"VALUES ($1, $2, $3, $4, $5::json, $6)", farm.ID, db.DateParam(today), streak, gold, string(itemsJSON), p.now()); err != nil {
			return nil, err
		}
		return &CheckinResult{
			New: true, Streak: streak, RewardText: checkin.RewardText(checkin.Reward{Gold: gold, Items: items}),
			Converted: converted, NextAt: nextCheckinAt(today),
		}, nil
	})
}

// CheckinStatus 今天是否已簽到、目前連續天數、下一次簽到的獎勵。只讀取不鎖。
func (p *ProgressService) CheckinStatus(ctx context.Context, playerID int64) (CheckinStatus, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (CheckinStatus, error) {
		farmID, err := farmIDOf(ctx, t, playerID)
		if err != nil {
			return CheckinStatus{}, err
		}
		return p.checkinStatus(ctx, t, farmID)
	})
}

func (p *ProgressService) checkinStatus(ctx context.Context, t *db.Tx, farmID *int64) (CheckinStatus, error) {
	today := p.taipeiToday()
	var last *models.DailyCheckin
	if farmID != nil {
		var err error
		if last, err = lastCheckin(ctx, t, *farmID); err != nil {
			return CheckinStatus{}, err
		}
	}
	checked, alive := false, false
	if last != nil {
		day := db.TaipeiDate(last.Day)
		checked = day.Equal(today)
		alive = !day.Before(today.AddDate(0, 0, -1)) // 今天或昨天簽到過才算延續中
	}
	streak, nxt := 0, 1
	if alive {
		streak, nxt = last.Streak, last.Streak+1
	}
	return CheckinStatus{
		CheckedToday:   checked,
		Streak:         streak,
		NextRewardText: checkin.RewardText(checkin.RewardFor(nxt)),
		NextAt:         nextCheckinAt(today),
	}, nil
}

// ---------- 圖鑑 ----------

// codexCategories 各分類的圖鑑項目；停用的作物／動物／魚只在已取得時列出（與魚類圖鑑相同）。
func codexCategories(ctx context.Context, t *db.Tx, farmID int64) ([]CodexCategory, error) {
	got, err := codex.Entries(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	entry := func(code, name string, variant bool) CodexEntry {
		row, ok := got[code]
		if !ok {
			return CodexEntry{Code: code, Name: name, Variant: variant}
		}
		return CodexEntry{Code: code, Name: name, Got: true, Quantity: row.Quantity, BestQuality: qualityName(row.BestQuality), Variant: variant}
	}
	has := func(code string) bool { _, ok := got[code]; return ok }
	cropTypes, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	var cropList []CodexEntry
	for _, c := range cropTypes {
		for _, v := range []struct {
			code, name string
			variant    bool
		}{{c.Code, c.Name, false}, {c.VariantCode, c.VariantName, true}} {
			if c.Active || has(v.code) {
				cropList = append(cropList, entry(v.code, v.name, v.variant))
			}
		}
	}
	animalTypes, err := animals.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	var animalList []CodexEntry
	for _, a := range animalTypes {
		if a.Active || has(a.ProductCode) {
			animalList = append(animalList, entry(a.ProductCode, a.ProductName, false))
		}
	}
	var goods []CodexEntry
	for _, code := range balance.GoodOrder {
		goods = append(goods, entry(code, balance.Goods[code].Name, false))
	}
	var materials []CodexEntry
	for _, code := range exploration.MaterialOrder {
		materials = append(materials, entry(code, exploration.Materials[code].Name, false))
	}
	caught, err := fishrepo.Codex(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	fishTypes, err := fishrepo.ListFishTypes(ctx, t, false)
	if err != nil {
		return nil, err
	}
	var fish []CodexEntry
	for _, ft := range fishTypes {
		c, ok := caught[ft.Code]
		if ft.Active || ok {
			fish = append(fish, CodexEntry{Code: ft.Code, Name: ft.Name, Got: ok, Quantity: c.Count})
		}
	}
	lists := map[string][]CodexEntry{"crop": cropList, "animal": animalList, "goods": goods, "material": materials, "fish": fish}
	out := make([]CodexCategory, 0, len(CodexCategories))
	for _, c := range CodexCategories {
		entries := lists[c.Key]
		found := 0
		for _, e := range entries {
			if e.Got {
				found++
			}
		}
		out = append(out, CodexCategory{Key: c.Key, Emoji: c.Emoji, Name: c.Name, Entries: entries, Discovered: found, Total: len(entries)})
	}
	return out, nil
}

func codexView(cats []CodexCategory) CodexView {
	v := CodexView{Categories: cats}
	for _, c := range cats {
		v.Discovered += c.Discovered
		v.Total += c.Total
	}
	return v
}

// Codex 圖鑑：各分類已收集／總數與每個項目（未取得的名稱由介面隱藏）。只讀取不鎖。
func (p *ProgressService) Codex(ctx context.Context, playerID int64) (CodexView, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (CodexView, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return CodexView{}, err
		}
		cats, err := codexCategories(ctx, t, farm.ID)
		return codexView(cats), err
	})
}

// ---------- 成就 ----------

// achievementMetrics 成就指標（全部從既有資料計算，含成就上線前的歷史）。
// 每個查詢都以農場（或玩家）的索引取少量資料；次數類指標讀 farm_stats 的累計欄位，不掃描交易紀錄。
func achievementMetrics(ctx context.Context, t *db.Tx, farm *models.Farm, categories []CodexCategory) (map[string]int, error) {
	if categories == nil {
		var err error
		if categories, err = codexCategories(ctx, t, farm.ID); err != nil {
			return nil, err
		}
	}
	cats := map[string]CodexCategory{}
	allFound, allTotal := 0, 0
	for _, c := range categories {
		cats[c.Key] = c
		allFound += c.Discovered
		allTotal += c.Total
	}
	sumQty := func(entries []CodexEntry) int {
		n := 0
		for _, e := range entries {
			n += e.Quantity
		}
		return n
	}
	cropEntries := cats["crop"].Entries
	legendary, variants := 0, 0
	for _, e := range cropEntries {
		if e.BestQuality == qualityName(len(balance.QualityOrder)-1) {
			legendary = 1
		}
		if e.Got && e.Variant {
			variants++
		}
	}
	var sales, steals, defended, gambles int
	stats, err := db.QueryStruct[models.FarmStats](ctx, t, "SELECT * FROM farm_stats WHERE farm_id = $1", farm.ID)
	if err != nil {
		return nil, err
	}
	if stats != nil {
		sales, steals, defended, gambles = stats.CropSalesIncome, stats.StealCount, stats.DefendedCount, stats.GambleCount
	}
	var fishTotal, fishLegendary, bossJoined, bossWon, explorations, bestStreak int
	if err := t.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE rarity = 'legendary') FROM farm_fish WHERE farm_id = $1",
		farm.ID).Scan(&fishTotal, &fishLegendary); err != nil {
		return nil, err
	}
	if err := t.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE outcome = 'victory') FROM world_boss_reward_claims "+
		"WHERE player_id = $1", farm.PlayerID).Scan(&bossJoined, &bossWon); err != nil {
		return nil, err
	}
	if err := t.QueryRow(ctx, "SELECT count(*) FROM exploration_logs WHERE farm_id = $1 AND claimed_at IS NOT NULL",
		farm.ID).Scan(&explorations); err != nil {
		return nil, err
	}
	if err := t.QueryRow(ctx, "SELECT coalesce(max(streak), 0) FROM daily_checkins WHERE farm_id = $1",
		farm.ID).Scan(&bestStreak); err != nil {
		return nil, err
	}
	plots, err := farms.CountPlots(ctx, t, farm.ID)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"crops":           sumQty(cropEntries),
		"crop_sales":      sales,
		"level":           farm.Level,
		"plots":           plots,
		"gold":            farm.Gold,
		"legendary_crop":  legendary,
		"variant_crop":    variants,
		"animal_products": sumQty(cats["animal"].Entries),
		"goods":           sumQty(cats["goods"].Entries),
		"explorations":    explorations,
		"fish":            fishTotal,
		"legendary_fish":  fishLegendary,
		"steals":          steals,
		"defended":        defended,
		"gambles":         gambles,
		"boss_joined":     bossJoined,
		"boss_won":        bossWon,
		"best_streak":     bestStreak,
		"codex_crop_pct":  pct(cats["crop"].Discovered, cats["crop"].Total),
		"codex_fish_pct":  pct(cats["fish"].Discovered, cats["fish"].Total),
		"codex_pct":       pct(allFound, allTotal),
	}, nil
}

func claimedAchievements(ctx context.Context, t *db.Tx, farmID int64) (map[string]models.FarmAchievement, error) {
	rows, err := db.QueryStructs[models.FarmAchievement](ctx, t, "SELECT * FROM farm_achievements WHERE farm_id = $1", farmID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.FarmAchievement, len(rows))
	for _, r := range rows {
		out[r.Code] = r
	}
	return out, nil
}

// Achievements 成就清單與進度。只讀取不鎖。
func (p *ProgressService) Achievements(ctx context.Context, playerID int64) (AchievementView, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (AchievementView, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return AchievementView{}, err
		}
		return achievementView(ctx, t, farm, nil)
	})
}

// ProgressOverview 「成就圖鑑」總覽頁：圖鑑、成就、簽到與稱號一次讀取（圖鑑只計算一次）。只讀取不鎖。
func (p *ProgressService) ProgressOverview(ctx context.Context, playerID int64) (ProgressOverview, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (ProgressOverview, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return ProgressOverview{}, err
		}
		cats, err := codexCategories(ctx, t, farm.ID)
		if err != nil {
			return ProgressOverview{}, err
		}
		ach, err := achievementView(ctx, t, farm, cats)
		if err != nil {
			return ProgressOverview{}, err
		}
		status, err := p.checkinStatus(ctx, t, &farm.ID)
		if err != nil {
			return ProgressOverview{}, err
		}
		ts, err := titlesView(ctx, t, farm)
		return ProgressOverview{Codex: codexView(cats), Achievements: ach, Checkin: status, Titles: ts}, err
	})
}

func achievementView(ctx context.Context, t *db.Tx, farm *models.Farm, categories []CodexCategory) (AchievementView, error) {
	metrics, err := achievementMetrics(ctx, t, farm, categories)
	if err != nil {
		return AchievementView{}, err
	}
	claimed, err := claimedAchievements(ctx, t, farm.ID)
	if err != nil {
		return AchievementView{}, err
	}
	var v AchievementView
	for _, a := range achievements.Active() {
		value := metrics[a.Metric]
		state := "progress"
		var claimedAt *time.Time
		if c, ok := claimed[a.Code]; ok {
			state = "claimed"
			at := c.ClaimedAt
			claimedAt = &at
			v.Claimed++
		} else if value >= a.Target {
			state = "ready"
			v.Ready++
		}
		v.Rows = append(v.Rows, AchievementRow{
			Code: a.Code, Category: a.Category, Emoji: a.Emoji, Name: a.Name, Description: a.Description(),
			Value: min(value, a.Target), Target: a.Target, Unit: achievements.Metrics[a.Metric].Unit, State: state,
			RewardText: achievements.RewardText(a), Title: a.Title, ClaimedAt: claimedAt,
		})
	}
	v.Total = len(v.Rows)
	return v, nil
}

// ---------- 操作回饋：圖鑑與成就標記、歡迎回來摘要 ----------

// progressMarks 已登記的圖鑑項目與可領取的成就（面板比較操作前後，顯示「新圖鑑」「成就達成」）。
func progressMarks(ctx context.Context, t *db.Tx, farm *models.Farm) (ProgressMarks, error) {
	cats, err := codexCategories(ctx, t, farm.ID)
	if err != nil {
		return ProgressMarks{}, err
	}
	view, err := achievementView(ctx, t, farm, cats)
	if err != nil {
		return ProgressMarks{}, err
	}
	m := ProgressMarks{Codex: map[string]string{}, Ready: map[string]string{}}
	for _, c := range cats {
		m.CodexTotal += c.Total
		for _, e := range c.Entries {
			if e.Got {
				m.Codex[c.Key+":"+e.Code] = e.Name
				m.CodexOrder = append(m.CodexOrder, c.Key+":"+e.Code)
			}
		}
	}
	for _, r := range view.Rows {
		if r.State == "ready" {
			m.Ready[r.Code] = r.Emoji + " " + r.Name
			m.ReadyOrder = append(m.ReadyOrder, r.Code)
		}
	}
	return m, nil
}

// ProgressMarks 只讀取不鎖。
func (p *ProgressService) ProgressMarks(ctx context.Context, playerID int64) (ProgressMarks, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (ProgressMarks, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return ProgressMarks{}, err
		}
		return progressMarks(ctx, t, farm)
	})
}

// WelcomeBack 打開面板時呼叫：記錄這次的時間，離開夠久時回傳期間發生的事（Lines），並附上圖鑑與成就標記。
func (p *ProgressService) WelcomeBack(ctx context.Context, playerID int64) (WelcomeBack, error) {
	now := p.now()
	return inTx(ctx, p.GameService, func(t *db.Tx) (WelcomeBack, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return WelcomeBack{}, err
		}
		prev, err := feedback.TouchSeen(ctx, t, farm.ID, now)
		if err != nil {
			return WelcomeBack{}, err
		}
		marks, err := progressMarks(ctx, t, farm)
		if err != nil {
			return WelcomeBack{}, err
		}
		lines := []string{}
		if prev != nil && now.Sub(*prev) >= AwayMinSeconds*time.Second {
			if lines, err = awayLines(ctx, t, farm, *prev, now, marks); err != nil {
				return WelcomeBack{}, err
			}
		}
		return WelcomeBack{Lines: lines, Marks: marks}, nil
	})
}

func awayLines(ctx context.Context, t *db.Tx, farm *models.Farm, since, now time.Time, marks ProgressMarks) ([]string, error) {
	s, err := feedback.StealsAgainst(ctx, t, farm.PlayerID, since)
	if err != nil {
		return nil, err
	}
	var lines []string
	if s.StolenTimes != 0 {
		lines = append(lines, fmt.Sprintf("🥷 被偷了 %d 次，共 %d 個作物（%s）", s.StolenTimes, s.StolenQuantity, strings.Join(s.Thieves, "、")))
	}
	blocked := 0
	for code, n := range s.Blocked {
		if code != pets.DogBlocker {
			blocked += n
		}
	}
	if dog := s.Blocked[pets.DogBlocker]; dog != 0 {
		lines = append(lines, fmt.Sprintf("🐶 狗狗擋下了 %d 次偷菜", dog))
	}
	if blocked != 0 {
		lines = append(lines, fmt.Sprintf("🛡️ 防守設施擋下了 %d 次偷菜", blocked))
	}
	plots, err := farms.ListPlots(ctx, t, farm.ID, false)
	if err != nil {
		return nil, err
	}
	ready, withered := 0, 0
	for _, pl := range plots {
		switch rules.PlotStateAt(pl.CropTypeID, pl.WateredAt, pl.ReadyAt, now) {
		case rules.PlotReady:
			ready++
		case rules.PlotWithered:
			withered++
		}
	}
	if ready != 0 {
		lines = append(lines, fmt.Sprintf("🌾 %d 格作物成熟了，可以收成", ready))
	}
	if withered != 0 {
		lines = append(lines, fmt.Sprintf("🥀 %d 格作物乾枯了，記得清除", withered))
	}
	var back int
	if err := t.QueryRow(ctx, "SELECT count(*) FROM exploration_logs WHERE farm_id = $1 AND claimed_at IS NULL "+
		"AND next_available_at <= $2", farm.ID, now).Scan(&back); err != nil {
		return nil, err
	}
	if back != 0 {
		lines = append(lines, "⛰️ 探險隊回來了，到「山洞」領取戰利品")
	}
	if len(marks.Ready) != 0 {
		lines = append(lines, fmt.Sprintf("🏅 有 %d 個成就可以領取", len(marks.Ready)))
	}
	if len(lines) == 0 {
		return []string{}, nil
	}
	out := []string{fmt.Sprintf("🌙 歡迎回來！你離開了 %s，這段時間：", LongDuration(int(now.Sub(since).Seconds())))}
	for _, l := range lines {
		out = append(out, "・"+l)
	}
	return out, nil
}

// ClaimAchievements 一次領取所有已達成但還沒領的成就獎勵（金幣、道具、稱號）。
func (p *ProgressService) ClaimAchievements(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := p.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := p.now()
		metrics, err := achievementMetrics(ctx, t, farm, nil)
		if err != nil {
			return ActionResult{}, err
		}
		claimed, err := claimedAchievements(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		var ready []achievements.Achievement
		for _, a := range achievements.Active() {
			if _, done := claimed[a.Code]; !done && achievements.Reached(a, metrics) {
				ready = append(ready, a)
			}
		}
		if len(ready) == 0 {
			return ActionResult{}, gameerr.Validationf("目前沒有可以領取的成就獎勵。")
		}
		var lines []string
		gotTitle, convertedAny := false, false
		for _, a := range ready {
			reward := a.Reward()
			gold, items, converted, err := p.grantReward(ctx, t, farm, rules.EvAchievementReward,
				fmt.Sprintf("成就「%s」", a.Name), reward.Gold, reward.Items)
			if err != nil {
				return ActionResult{}, err
			}
			convertedAny = convertedAny || converted
			if a.Title != "" {
				if err := titles.Grant(ctx, t, farm.ID, a.Title, "achievement:"+a.Code, now); err != nil {
					return ActionResult{}, err
				}
				gotTitle = true
			}
			itemsJSON, err := json.Marshal(items)
			if err != nil {
				return ActionResult{}, err
			}
			// (farm_id, code) 為主鍵：重複領取在這裡失敗並整筆回滾
			if _, err := t.Exec(ctx, "INSERT INTO farm_achievements (farm_id, code, gold, items, claimed_at) "+
				"VALUES ($1, $2, $3, $4::json, $5)", farm.ID, a.Code, gold, string(itemsJSON), now); err != nil {
				return ActionResult{}, err
			}
			var parts []string
			if text := checkin.RewardText(checkin.Reward{Gold: gold, Items: items}); text != "無" {
				parts = append(parts, text)
			}
			if a.Title != "" {
				parts = append(parts, fmt.Sprintf("稱號「%s」", a.Title))
			}
			text := strings.Join(parts, "、")
			if text == "" {
				text = "—"
			}
			lines = append(lines, fmt.Sprintf("%s %s：%s", a.Emoji, a.Name, text))
		}
		message := fmt.Sprintf("🏅 領取 %d 個成就獎勵：\n%s", len(ready), strings.Join(lines, "\n"))
		if convertedAny {
			message += "\n（倉庫已滿，部分道具改發等值金幣）"
		}
		if gotTitle {
			message += "\n🎖️ 新稱號可以在下方「稱號」設定顯示。"
		}
		return result(message, farm.Gold, 0, "", 0), nil
	})
}

// ---------- 稱號 ----------

// Titles 擁有的稱號與目前選用的稱號。只讀取不鎖。
func (p *ProgressService) Titles(ctx context.Context, playerID int64) (TitlesView, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (TitlesView, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return TitlesView{}, err
		}
		return titlesView(ctx, t, farm)
	})
}

func titlesView(ctx context.Context, t *db.Tx, farm *models.Farm) (TitlesView, error) {
	owned, err := titles.Owned(ctx, t, farm.ID)
	if err != nil {
		return TitlesView{}, err
	}
	v := TitlesView{Current: farm.Title, Owned: make([]TitleInfo, 0, len(owned))}
	for _, o := range owned {
		v.Owned = append(v.Owned, TitleInfo{Title: o.Title, ObtainedAt: o.ObtainedAt, Source: TitleSource(o.Source)})
	}
	return v, nil
}

func ownsTitle(ctx context.Context, t *db.Tx, farmID int64, title string) (bool, error) {
	owned, err := titles.Owned(ctx, t, farmID)
	if err != nil {
		return false, err
	}
	for _, o := range owned {
		if o.Title == title {
			return true, nil
		}
	}
	return false, nil
}

// SetTitle 選用一個已擁有的稱號（空字串 = 不顯示）。
func (p *ProgressService) SetTitle(ctx context.Context, playerID int64, title string) (ActionResult, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := p.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		title = strings.TrimSpace(title)
		if title != "" {
			ok, err := ownsTitle(ctx, t, farm.ID, title)
			if err != nil {
				return ActionResult{}, err
			}
			if !ok {
				return ActionResult{}, gameerr.NotFoundf("你還沒有「%s」這個稱號。", title)
			}
		}
		// Python 只改 ORM 屬性：值沒變時 flush 不會 UPDATE（updated_at 不變）
		if deref(farm.Title) != title {
			farm.Title = strPtr(title)
			if err := p.saveFarm(ctx, t, farm, db.C("title", farm.Title)); err != nil {
				return ActionResult{}, err
			}
		}
		if title == "" {
			return result("已取消顯示稱號。", farm.Gold, 0, "", 0), nil
		}
		return result(fmt.Sprintf("已設定稱號「%s」。", title), farm.Gold, 0, "", 0), nil
	})
}

// PlayerTitles 排行榜顯示用：玩家 → 目前選用的稱號。
func (p *ProgressService) PlayerTitles(ctx context.Context, playerIDs []int64) (map[int64]string, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (map[int64]string, error) {
		return titles.SelectedByPlayer(ctx, t, playerIDs)
	})
}

// ---------- 特賣與升級：溫室、每週特賣、稱號商店 ----------

func weeklyOffers(ctx context.Context, t *db.Tx, start time.Time) ([]specialshop.Offer, error) {
	list, err := crops.ListAll(ctx, t, true)
	if err != nil {
		return nil, err
	}
	refs := make([]specialshop.CropRef, 0, len(list))
	for _, c := range list {
		refs = append(refs, specialshop.CropRef{Code: c.Code, Name: c.Name, SeedPrice: c.SeedPrice, ShopBuyable: c.ShopBuyable})
	}
	return specialshop.WeeklyOffers(start, refs), nil
}

func weeklyBought(ctx context.Context, t *db.Tx, farmID int64, start time.Time) (map[string]int, error) {
	rows, err := db.QueryStructs[models.WeeklyShopPurchase](ctx, t,
		"SELECT * FROM weekly_shop_purchases WHERE farm_id = $1 AND week_start = $2", farmID, db.DateParam(start))
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ItemCode] = r.Quantity
	}
	return out, nil
}

// SpecialShop 商店「特賣與升級」分頁：溫室等級與價格、本週特賣與已購買量、稱號商店。只讀取不鎖。
func (p *ProgressService) SpecialShop(ctx context.Context, playerID int64) (SpecialShopView, error) {
	return inTx(ctx, p.GameService, func(t *db.Tx) (SpecialShopView, error) {
		farm, err := p.farmOf(ctx, t, playerID)
		if err != nil {
			return SpecialShopView{}, err
		}
		start := specialshop.WeekStart(p.now())
		bought, err := weeklyBought(ctx, t, farm.ID, start)
		if err != nil {
			return SpecialShopView{}, err
		}
		owned, err := titles.Owned(ctx, t, farm.ID)
		if err != nil {
			return SpecialShopView{}, err
		}
		has := map[string]bool{}
		for _, o := range owned {
			has[o.Title] = true
		}
		offers, err := weeklyOffers(ctx, t, start)
		if err != nil {
			return SpecialShopView{}, err
		}
		level := farm.GreenhouseLevel
		gh := GreenhouseInfo{
			Level: level, Max: specialshop.GreenhouseMaxLevel(), Pct: specialshop.GreenhousePct(level),
			NextPct: specialshop.GreenhousePct(level + 1), PerLevel: specialshop.GreenhousePctPerLevel(),
		}
		if price, ok := specialshop.GreenhousePrice(level); ok {
			gh.NextPrice = &price
		}
		v := SpecialShopView{Gold: farm.Gold, Greenhouse: gh,
			Weekly: WeeklyView{WeekStart: start, EndsAt: specialshop.WeekEndsAt(start), Offers: []WeeklyOfferView{}}}
		for _, o := range offers {
			v.Weekly.Offers = append(v.Weekly.Offers, WeeklyOfferView{
				CropCode: o.CropCode, Name: o.CropName + "種子", Price: o.Price, OriginalPrice: o.OriginalPrice,
				Limit: o.Limit, Rare: o.Rare, Bought: bought[rules.SeedItemCode(o.CropCode)],
			})
		}
		for _, ts := range specialshop.TitleShop() {
			v.Titles = append(v.Titles, TitleShopItem{Title: ts.Title, Price: ts.Price, Owned: has[ts.Title]})
		}
		return v, nil
	})
}

// BuyWeeklyDeal 購買本週特賣的種子：每人每週每項限購；稀有種子放素材背包。
func (p *ProgressService) BuyWeeklyDeal(ctx context.Context, playerID int64, cropCode string, quantity int) (ActionResult, error) {
	if quantity <= 0 {
		return ActionResult{}, gameerr.Validationf("購買數量必須是正整數。")
	}
	return inTx(ctx, p.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := p.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		start := specialshop.WeekStart(p.now())
		offers, err := weeklyOffers(ctx, t, start)
		if err != nil {
			return ActionResult{}, err
		}
		var offer *specialshop.Offer
		for i := range offers {
			if offers[i].CropCode == cropCode {
				offer = &offers[i]
				break
			}
		}
		if offer == nil {
			return ActionResult{}, gameerr.NotFoundf("這項商品不在本週特賣中（每週一 00:00 換一批），請重新整理。")
		}
		item := rules.SeedItemCode(offer.CropCode)
		boughtMap, err := weeklyBought(ctx, t, farm.ID, start)
		if err != nil {
			return ActionResult{}, err
		}
		bought := boughtMap[item]
		if bought+quantity > offer.Limit {
			return ActionResult{}, gameerr.Validationf("%s種子本週每人限購 %d 顆，你還可以買 %d 顆。",
				offer.CropName, offer.Limit, max(0, offer.Limit-bought))
		}
		cost := offer.Price * quantity
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		if err := p.ensureItemSpace(ctx, t, farm, item, quantity); err != nil {
			return ActionResult{}, err
		}
		farm.Gold -= cost
		if err := p.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := inventory.Add(ctx, t, farm.ID, item, quantity, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		if _, err := t.Exec(ctx, "INSERT INTO weekly_shop_purchases (farm_id, week_start, item_code, quantity) "+
			"VALUES ($1, $2, $3, $4) ON CONFLICT (farm_id, week_start, item_code) "+
			"DO UPDATE SET quantity = weekly_shop_purchases.quantity + excluded.quantity",
			farm.ID, db.DateParam(start), item, quantity); err != nil {
			return ActionResult{}, err
		}
		if err := p.record(ctx, t, farm, rules.EvBuySeed,
			fmt.Sprintf("每週特賣：購買 %d 顆%s種子（每顆 %d），金幣 -%d", quantity, offer.CropName, offer.Price, cost),
			rec{ItemCode: item, Quantity: quantity, GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		left := offer.Limit - bought - quantity
		return result(fmt.Sprintf("🎁 買到 %d 顆%s種子，花費 %s 金幣（本週還能買 %d 顆）。", quantity, offer.CropName,
			numfmt.Comma(cost), left), farm.Gold, -cost, item, quantity), nil
	})
}

// BuyTitle 稱號商店：用金幣買稱號，買到後可以在「🎖️ 稱號」選用。每個稱號只能買一次。
func (p *ProgressService) BuyTitle(ctx context.Context, playerID int64, title string) (ActionResult, error) {
	price, ok := specialshop.TitlePrice(title)
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("稱號商店沒有「%s」。", title)
	}
	return inTx(ctx, p.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := p.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		owned, err := ownsTitle(ctx, t, farm.ID, title)
		if err != nil {
			return ActionResult{}, err
		}
		if owned {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經擁有稱號「%s」。", title)
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：稱號「%s」需要 %s，目前只有 %s。",
				title, numfmt.Comma(price), numfmt.Comma(farm.Gold))
		}
		farm.Gold -= price
		if err := p.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := titles.Grant(ctx, t, farm.ID, title, "shop", p.now()); err != nil {
			return ActionResult{}, err
		}
		if err := p.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("稱號商店：購買稱號「%s」，金幣 -%d", title, price),
			rec{ItemCode: specialshop.TitleShopCode, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🎖️ 買到稱號「%s」！到「🏅 成就圖鑑 → 🎖️ 稱號」選用。", title), farm.Gold, -price, "", 0), nil
	})
}
