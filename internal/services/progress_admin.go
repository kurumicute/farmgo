package services

// 管理員：新功能（每日簽到、寵物、特賣與升級、成就、稱號、圖鑑）的規則設定與單一玩家資料調整
// （對應 Python app/services/progress_admin.py；Python 以 mixin 由 AdminService 繼承，Go 直接是 AdminService 的方法）。
//
//   - 規則設定存在 feature_settings（見 game/featurecfg）；儲存後本程序立即生效，Bot 最晚 10 秒內套用。
//   - 調整玩家資料都不改金幣；每次調整寫一筆 admin_adjust 交易紀錄（金幣 0，玩家看得到）與管理操作紀錄。
//   - 數值錯誤訊息用 featurecfg.IntValue（數字加千分位，與 Python feature_config.int_value 相同）。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/achievements"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/checkin"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/specialshop"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/featuresettings"
	petsrepo "farmgm/internal/repositories/pets"
	"farmgm/internal/repositories/titles"
)

// FeatureKeys 管理網站可調整的規則（顯示順序）。
var FeatureKeys = []string{checkin.ConfigKey, pets.ConfigKey, specialshop.ConfigKey, achievements.ConfigKey}

// CodexEditable 可在管理網站調整的圖鑑分類（魚類圖鑑來自 farm_fish，不能在這裡改）。
var CodexEditable = []string{"crop", "animal", "goods", "material"}

const (
	AdminTitleSource = "admin"
	MaxStreak        = 10_000
)

// QualityNames 依 0–3 排列：普通、優良、稀有、傳說。
func QualityNames() []string {
	out := make([]string, len(balance.QualityOrder))
	for i, q := range balance.QualityOrder {
		out[i] = balance.Qualities[q]
	}
	return out
}

// ---------- 回傳資料 ----------

type FeatureKeyView struct {
	Key       string           `json:"key"`
	Label     string           `json:"label"`
	Current   featurecfg.Value `json:"current"`
	Defaults  featurecfg.Value `json:"defaults"`
	IsDefault bool             `json:"is_default"`
	UpdatedAt *time.Time       `json:"updated_at"`
	UpdatedBy *string          `json:"updated_by"`
}

type FeatureItemView struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Price int    `json:"price"`
}

type FeatureAchievementView struct {
	Code         string         `json:"code"`
	Category     string         `json:"category"`
	CategoryName string         `json:"category_name"`
	Emoji        string         `json:"emoji"`
	Name         string         `json:"name"`
	Metric       string         `json:"metric"`
	Unit         string         `json:"unit"`
	MaxTarget    int            `json:"max_target"`
	Default      map[string]any `json:"default"`
	Current      map[string]any `json:"current"`
}

type FeatureLimits struct {
	PetLevels        int `json:"pet_levels"`
	GreenhouseLevels int `json:"greenhouse_levels"`
	CheckinDays      int `json:"checkin_days"`
	TitleMaxLen      int `json:"title_max_len"`
}

// FeatureSettingsView 各項規則目前的設定、預設值與最後修改者，以及編輯畫面需要的道具與成就清單。
type FeatureSettingsView struct {
	Keys         []FeatureKeyView         `json:"keys"`
	Items        []FeatureItemView        `json:"items"`
	Achievements []FeatureAchievementView `json:"achievements"`
	Limits       FeatureLimits            `json:"limits"`
}

type AdminGreenhouse struct {
	Level int `json:"level"`
	Max   int `json:"max"`
	Pct   int `json:"pct"`
}

type AdminPet struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	Level *int   `json:"level"` // nil = 沒有這隻寵物
	Max   int    `json:"max"`
	Pct   int    `json:"pct"`
}

type AdminTitles struct {
	Current *string     `json:"current"`
	Owned   []TitleInfo `json:"owned"`
	Known   []string    `json:"known"`
}

type AdminCheckin struct {
	CheckinStatus
	LastDay    *time.Time // 台灣日期
	LastStreak *int
	Today      time.Time
}

type AdminCodexEntry struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Got         bool   `json:"got"`
	Quantity    int    `json:"quantity"`
	BestQuality int    `json:"best_quality"` // 0–3
}

type AdminCodexCategory struct {
	Key        string            `json:"key"`
	Name       string            `json:"name"`
	Editable   bool              `json:"editable"`
	Discovered int               `json:"discovered"`
	Total      int               `json:"total"`
	Entries    []AdminCodexEntry `json:"entries"`
}

type AdminPurchase struct {
	ItemCode string `json:"item_code"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type AdminWeekly struct {
	WeekStart time.Time       `json:"week_start"`
	Purchases []AdminPurchase `json:"purchases"`
}

// pyDate Python date 的 JSON（isoformat：台灣日期 YYYY-MM-DD）；前端以字串比較日期。
func pyDate(t time.Time) string { return db.TaipeiDate(t).Format(time.DateOnly) }

// MarshalJSON Python {**checkin_status, "last_day", "last_streak", "today"}；日期輸出 YYYY-MM-DD。
func (c AdminCheckin) MarshalJSON() ([]byte, error) {
	var lastDay *string
	if c.LastDay != nil {
		d := pyDate(*c.LastDay)
		lastDay = &d
	}
	return json.Marshal(struct {
		CheckinStatus
		LastDay    *string `json:"last_day"`
		LastStreak *int    `json:"last_streak"`
		Today      string  `json:"today"`
	}{c.CheckinStatus, lastDay, c.LastStreak, pyDate(c.Today)})
}

// MarshalJSON week_start 是 Python date（YYYY-MM-DD）。
func (w AdminWeekly) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		WeekStart string          `json:"week_start"`
		Purchases []AdminPurchase `json:"purchases"`
	}{pyDate(w.WeekStart), nonNil(w.Purchases)})
}

// AdminProgressView 管理網站「玩家調整」的新功能資料。
type AdminProgressView struct {
	Greenhouse   AdminGreenhouse      `json:"greenhouse"`
	Pets         []AdminPet           `json:"pets"`
	Titles       AdminTitles          `json:"titles"`
	Checkin      AdminCheckin         `json:"checkin"`
	Achievements []AchievementRow     `json:"achievements"`
	Codex        []AdminCodexCategory `json:"codex"`
	QualityNames []string             `json:"quality_names"`
	Weekly       AdminWeekly          `json:"weekly"`
}

// ---------- 規則設定 ----------

// checkTitles 商店稱號不可和成就稱號重複（兩邊都可以改，存檔時以另一邊目前的設定檢查）。
func checkTitles(key string, value featurecfg.Value) error {
	clash := map[string]bool{}
	switch key {
	case specialshop.ConfigKey:
		achTitles := achievements.Titles()
		list, _ := featurecfg.AsList(value["titles"])
		for _, v := range list {
			m, _ := featurecfg.AsMap(v)
			if title, _ := m["title"].(string); achTitles[title] {
				clash[title] = true
			}
		}
	case achievements.ConfigKey:
		shop := map[string]bool{}
		for _, t := range specialshop.TitleShop() {
			shop[t.Title] = true
		}
		for _, a := range achievements.ApplyConfig(value) {
			if a.Title != "" && shop[a.Title] {
				clash[a.Title] = true
			}
		}
	default:
		return nil
	}
	if len(clash) == 0 {
		return nil
	}
	names := make([]string, 0, len(clash))
	for t := range clash {
		names = append(names, t)
	}
	sort.Strings(names)
	return gameerr.Validationf("商店稱號與成就稱號不可重複：%s。", strings.Join(names, "、"))
}

// FeatureSettings 各項規則目前的設定、預設值與最後修改者，以及編輯畫面需要的道具與成就清單。
func (a *AdminService) FeatureSettings(ctx context.Context) (FeatureSettingsView, error) {
	stored, err := inTx(ctx, a.GameService, func(t *db.Tx) (map[string]models.FeatureSetting, error) {
		if err := featuresettings.Refresh(ctx, t, true); err != nil {
			return nil, err
		}
		return featuresettings.Rows(ctx, t)
	})
	if err != nil {
		return FeatureSettingsView{}, err
	}
	var v FeatureSettingsView
	for _, key := range FeatureKeys {
		k := FeatureKeyView{Key: key, Label: featurecfg.Label(key), Current: featurecfg.Get(key),
			Defaults: featurecfg.Defaults(key), IsDefault: featurecfg.IsDefault(key)}
		if row, ok := stored[key]; ok {
			at, by := row.UpdatedAt, row.UpdatedBy
			k.UpdatedAt, k.UpdatedBy = &at, &by
		}
		v.Keys = append(v.Keys, k)
	}
	for _, code := range balance.ItemOrder {
		it := balance.Items[code]
		v.Items = append(v.Items, FeatureItemView{code, it.Name, it.Price})
	}
	categories := map[string]string{}
	for _, c := range achievements.Categories {
		categories[c.Code] = c.Name
	}
	current := achievements.ByCode()
	for _, base := range achievements.BaseAchievements {
		maxTarget := 1_000_000_000_000
		if achievements.PctMetrics[base.Metric] {
			maxTarget = 100
		}
		cur := base
		if c, ok := current[base.Code]; ok {
			cur = c
		}
		m := achievements.Metrics[base.Metric]
		v.Achievements = append(v.Achievements, FeatureAchievementView{
			Code: base.Code, Category: base.Category, CategoryName: categories[base.Category], Emoji: base.Emoji,
			Name: base.Name, Metric: m.Label, Unit: m.Unit, MaxTarget: maxTarget,
			Default: achievements.Editable(base), Current: achievements.Editable(cur),
		})
	}
	v.Limits = FeatureLimits{pets.LevelLimit, specialshop.GreenhouseLevelLimit, checkin.MaxCycleDays, specialshop.TitleMaxLen}
	return v, nil
}

func checkFeatureKey(key string) error {
	if !slices.Contains(FeatureKeys, key) {
		return gameerr.NotFoundf("沒有「%s」這項設定。", key)
	}
	return nil
}

// UpdateFeatureSettings 驗證並儲存一項規則設定；回傳正規化後的設定（Python 回傳 {"key", "current"}）。
func (a *AdminService) UpdateFeatureSettings(ctx context.Context, actor, key string, raw featurecfg.Value) (featurecfg.Value, error) {
	if err := checkFeatureKey(key); err != nil {
		return nil, err
	}
	value, err := featurecfg.Validate(key, raw)
	if err != nil {
		return nil, err
	}
	if err := checkTitles(key, value); err != nil {
		return nil, err
	}
	if err := a.transaction(ctx, func(t *db.Tx) error {
		before := featurecfg.Get(key)
		if err := featuresettings.Save(ctx, t, key, value, actor, a.now()); err != nil {
			return err
		}
		return a.audit(ctx, t, actor, "update_feature_settings", "feature_settings", nil,
			map[string]any{"key": key, "before": before, "after": value})
	}); err != nil {
		return nil, err
	}
	featurecfg.SetCurrent(key, value) // 提交後才在本程序生效
	featuresettings.NotifyChanged()   // 其他程序（Redis 設定時）立即重新讀取
	return value, nil
}

// ResetFeatureSettings 還原成程式內的預設值（刪除資料列）；回傳預設值。
func (a *AdminService) ResetFeatureSettings(ctx context.Context, actor, key string) (featurecfg.Value, error) {
	if err := checkFeatureKey(key); err != nil {
		return nil, err
	}
	defaults := featurecfg.Defaults(key)
	if err := checkTitles(key, defaults); err != nil {
		return nil, err
	}
	if err := a.transaction(ctx, func(t *db.Tx) error {
		before := featurecfg.Get(key)
		if err := featuresettings.Remove(ctx, t, key); err != nil {
			return err
		}
		return a.audit(ctx, t, actor, "reset_feature_settings", "feature_settings", nil,
			map[string]any{"key": key, "before": before})
	}); err != nil {
		return nil, err
	}
	featurecfg.SetCurrent(key, nil)
	featuresettings.NotifyChanged()
	return defaults, nil
}

// ---------- 單一玩家：讀取 ----------

// AdminProgress 管理網站「玩家調整」的新功能資料：溫室、寵物、稱號、簽到、成就、圖鑑與本週特賣購買量。
func (a *AdminService) AdminProgress(ctx context.Context, playerID int64) (AdminProgressView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (AdminProgressView, error) {
		var v AdminProgressView
		farm, err := a.farmOf(ctx, t, playerID)
		if err != nil {
			return v, err
		}
		cats, err := codexCategories(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		today := a.taipeiToday()
		last, err := lastCheckin(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		owned, err := petsrepo.Levels(ctx, t, farm.ID)
		if err != nil {
			return v, err
		}
		start := specialshop.WeekStart(a.now())
		bought, err := weeklyBought(ctx, t, farm.ID, start)
		if err != nil {
			return v, err
		}
		items, err := catalog.ListItems(ctx, t)
		if err != nil {
			return v, err
		}
		v.Greenhouse = AdminGreenhouse{farm.GreenhouseLevel, specialshop.GreenhouseMaxLevel(),
			specialshop.GreenhousePct(farm.GreenhouseLevel)}
		for _, code := range pets.PetOrder {
			p := pets.Pets[code]
			var level *int
			if lv, ok := owned[code]; ok {
				level = &lv
			}
			v.Pets = append(v.Pets, AdminPet{code, p.Name, p.Emoji, level, pets.MaxLevel(), pets.BonusPct(level)})
		}
		ts, err := titlesView(ctx, t, farm)
		if err != nil {
			return v, err
		}
		known := achievements.Titles()
		for _, s := range specialshop.TitleShop() {
			known[s.Title] = true
		}
		v.Titles = AdminTitles{Current: ts.Current, Owned: nonNil(ts.Owned), Known: make([]string, 0, len(known))}
		for title := range known {
			v.Titles.Known = append(v.Titles.Known, title)
		}
		sort.Strings(v.Titles.Known)
		status, err := a.checkinStatus(ctx, t, &farm.ID)
		if err != nil {
			return v, err
		}
		v.Checkin = AdminCheckin{CheckinStatus: status, Today: today}
		if last != nil {
			day, streak := db.TaipeiDate(last.Day), last.Streak
			v.Checkin.LastDay, v.Checkin.LastStreak = &day, &streak
		}
		ach, err := achievementView(ctx, t, farm, cats)
		if err != nil {
			return v, err
		}
		v.Achievements = nonNil(ach.Rows)
		v.QualityNames = QualityNames()
		v.Codex = []AdminCodexCategory{}
		for _, c := range cats {
			ac := AdminCodexCategory{Key: c.Key, Name: c.Name, Editable: slices.Contains(CodexEditable, c.Key),
				Discovered: c.Discovered, Total: c.Total, Entries: make([]AdminCodexEntry, 0, len(c.Entries))}
			for _, e := range c.Entries {
				best := 0
				if e.BestQuality != "" {
					best = max(0, slices.Index(v.QualityNames, e.BestQuality))
				}
				ac.Entries = append(ac.Entries, AdminCodexEntry{e.Code, e.Name, e.Got, e.Quantity, best})
			}
			v.Codex = append(v.Codex, ac)
		}
		v.Weekly = AdminWeekly{WeekStart: start, Purchases: []AdminPurchase{}}
		codes := make([]string, 0, len(bought))
		for code := range bought {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		for _, code := range codes {
			name := code
			if e, ok := items.Map[code]; ok {
				name = e.Name
			}
			v.Weekly.Purchases = append(v.Weekly.Purchases, AdminPurchase{code, name, bought[code]})
		}
		return v, nil
	})
}

// ---------- 單一玩家：調整 ----------

// SetGreenhouseLevel 回傳是否有變更。
func (a *AdminService) SetGreenhouseLevel(ctx context.Context, actor string, playerID int64, level int) (bool, error) {
	level, err := featurecfg.IntValue(level, "溫室等級", 0, specialshop.GreenhouseMaxLevel())
	if err != nil {
		return false, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (bool, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return false, err
		}
		before := farm.GreenhouseLevel
		if before == level {
			return false, nil
		}
		farm.GreenhouseLevel = level
		if err := a.saveFarm(ctx, t, farm, db.C("greenhouse_level", level)); err != nil {
			return false, err
		}
		return true, a.adjust(ctx, t, farm, actor, "set_greenhouse_level", fmt.Sprintf("溫室等級 %d → %d", before, level),
			map[string]any{"before": before, "after": level}, rec{})
	})
}

func petLevelText(level *int) string {
	if level == nil {
		return "沒有"
	}
	return fmt.Sprintf("Lv%d", *level)
}

// SetPetLevel 設定寵物等級；level 為 nil 表示移除這隻寵物（不退費）。回傳是否有變更。
func (a *AdminService) SetPetLevel(ctx context.Context, actor string, playerID int64, petCode string, level *int) (bool, error) {
	pet, ok := pets.Pets[petCode]
	if !ok {
		return false, gameerr.NotFoundf("沒有「%s」這種寵物。", petCode)
	}
	if level != nil {
		n, err := featurecfg.IntValue(*level, pet.Name+"等級", 0, pets.MaxLevel())
		if err != nil {
			return false, err
		}
		level = &n
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (bool, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return false, err
		}
		row, err := petsrepo.Get(ctx, t, farm.ID, petCode)
		if err != nil {
			return false, err
		}
		var before *int
		if row != nil {
			lv := row.Level
			before = &lv
		}
		if (before == nil && level == nil) || (before != nil && level != nil && *before == *level) {
			return false, nil
		}
		switch {
		case level == nil:
			_, err = t.Exec(ctx, "DELETE FROM farm_pets WHERE farm_id = $1 AND pet_code = $2", farm.ID, petCode)
		case row == nil:
			err = db.Insert(ctx, t, models.TableFarmPet, db.C("farm_id", farm.ID), db.C("pet_code", petCode),
				db.C("level", *level), db.C("bought_at", a.now()))
		default:
			_, err = t.Exec(ctx, "UPDATE farm_pets SET level = $3 WHERE farm_id = $1 AND pet_code = $2", farm.ID, petCode, *level)
		}
		if err != nil {
			return false, err
		}
		return true, a.adjust(ctx, t, farm, actor, "set_pet_level",
			fmt.Sprintf("%s %s → %s", pet.Name, petLevelText(before), petLevelText(level)),
			map[string]any{"pet": petCode, "before": before, "after": level}, rec{})
	})
}

func (a *AdminService) GrantTitle(ctx context.Context, actor string, playerID int64, title string) (string, error) {
	title, _, err := featurecfg.TitleValue(title, "稱號", specialshop.TitleMaxLen, true)
	if err != nil {
		return "", err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (string, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return "", err
		}
		owned, err := ownsTitle(ctx, t, farm.ID, title)
		if err != nil {
			return "", err
		}
		if owned {
			return "", gameerr.New(gameerr.AlreadyExists, "玩家已經擁有稱號「%s」。", title)
		}
		if err := titles.Grant(ctx, t, farm.ID, title, AdminTitleSource, a.now()); err != nil {
			return "", err
		}
		return title, a.adjust(ctx, t, farm, actor, "grant_title", fmt.Sprintf("給予稱號「%s」", title),
			map[string]any{"title": title}, rec{})
	})
}

func (a *AdminService) RevokeTitle(ctx context.Context, actor string, playerID int64, title string) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		found, err := db.QueryStruct[models.FarmTitle](ctx, t,
			"SELECT * FROM farm_titles WHERE farm_id = $1 AND title = $2", farm.ID, title)
		if err != nil {
			return err
		}
		if found == nil {
			return gameerr.NotFoundf("玩家沒有稱號「%s」。", title)
		}
		if _, err := t.Exec(ctx, "DELETE FROM farm_titles WHERE farm_id = $1 AND title = $2", farm.ID, title); err != nil {
			return err
		}
		if farm.Title != nil && *farm.Title == title {
			farm.Title = nil
			if err := a.saveFarm(ctx, t, farm, db.C("title", farm.Title)); err != nil {
				return err
			}
		}
		return a.adjust(ctx, t, farm, actor, "revoke_title", fmt.Sprintf("移除稱號「%s」", title),
			map[string]any{"title": title, "source": found.Source}, rec{})
	})
}

// SetActiveTitle 設定玩家目前顯示的稱號（必須已擁有；"" = 不顯示）。回傳是否有變更。
func (a *AdminService) SetActiveTitle(ctx context.Context, actor string, playerID int64, title string) (bool, error) {
	title = strings.TrimSpace(title)
	return inTx(ctx, a.GameService, func(t *db.Tx) (bool, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return false, err
		}
		if title != "" {
			ok, err := ownsTitle(ctx, t, farm.ID, title)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, gameerr.NotFoundf("玩家沒有稱號「%s」。", title)
			}
		}
		before := farm.Title
		if deref(before) == title {
			return false, nil
		}
		farm.Title = strPtr(title)
		if err := a.saveFarm(ctx, t, farm, db.C("title", farm.Title)); err != nil {
			return false, err
		}
		show := func(s string) string {
			if s == "" {
				return "無"
			}
			return s
		}
		return true, a.adjust(ctx, t, farm, actor, "set_active_title",
			fmt.Sprintf("顯示稱號「%s」→「%s」", show(deref(before)), show(title)),
			map[string]any{"before": before, "after": farm.Title}, rec{})
	})
}

// SetCheckinStreak 設定目前的連續簽到天數（例如 Bot 停機讓玩家斷簽時補回）。
//
// 最近一次簽到是今天或昨天：直接改那一筆；否則補一筆「昨天」的簽到（不發獎），玩家今天簽到時就會接續。
func (a *AdminService) SetCheckinStreak(ctx context.Context, actor string, playerID int64, streak int) error {
	streak, err := featurecfg.IntValue(streak, "連續天數", 1, MaxStreak)
	if err != nil {
		return err
	}
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		today := a.taipeiToday()
		last, err := lastCheckin(ctx, t, farm.ID)
		if err != nil {
			return err
		}
		yesterday := today.AddDate(0, 0, -1)
		var before int
		var where string
		if last != nil && !db.TaipeiDate(last.Day).Before(yesterday) {
			before = last.Streak
			if _, err := t.Exec(ctx, "UPDATE daily_checkins SET streak = $2 WHERE id = $1", last.ID, streak); err != nil {
				return err
			}
			where = "昨天"
			if db.TaipeiDate(last.Day).Equal(today) {
				where = "今天"
			}
		} else {
			if _, err := t.Exec(ctx, "INSERT INTO daily_checkins (farm_id, day, streak, gold, items, created_at) "+
				"VALUES ($1, $2, $3, 0, '{}'::json, $4)", farm.ID, db.DateParam(yesterday), streak, a.now()); err != nil {
				return err
			}
			where = "補登昨天"
		}
		return a.adjust(ctx, t, farm, actor, "set_checkin_streak", fmt.Sprintf("連續簽到 %d → %d 天（%s）", before, streak, where),
			map[string]any{"before": before, "after": streak}, rec{})
	})
}

// SetAchievementClaimed 標記成就為已領取（不發獎）或重設為未領取（玩家可以再領一次獎勵；已給的稱號不會收回）。
// 回傳是否有變更。
func (a *AdminService) SetAchievementClaimed(ctx context.Context, actor string, playerID int64, code string, claimed bool) (bool, error) {
	ach, ok := achievements.ByCode()[code]
	if !ok {
		return false, gameerr.NotFoundf("沒有「%s」這個成就。", code)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (bool, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return false, err
		}
		has, err := exists(ctx, t, "SELECT 1 FROM farm_achievements WHERE farm_id = $1 AND code = $2", farm.ID, code)
		if err != nil {
			return false, err
		}
		if has == claimed {
			return false, nil
		}
		var text string
		if claimed {
			if _, err := t.Exec(ctx, "INSERT INTO farm_achievements (farm_id, code, gold, items, claimed_at) "+
				"VALUES ($1, $2, 0, '{}'::json, $3)", farm.ID, code, a.now()); err != nil {
				return false, err
			}
			text = fmt.Sprintf("成就「%s」標記為已領取（不發獎）", ach.Name)
		} else {
			if _, err := t.Exec(ctx, "DELETE FROM farm_achievements WHERE farm_id = $1 AND code = $2", farm.ID, code); err != nil {
				return false, err
			}
			text = fmt.Sprintf("成就「%s」重設為未領取", ach.Name)
		}
		return true, a.adjust(ctx, t, farm, actor, "set_achievement_claimed", text,
			map[string]any{"code": code, "claimed": claimed}, rec{})
	})
}

// SetCodexEntry 設定圖鑑的累計數量與最高品質；數量 0 = 改回未取得。魚類圖鑑來自魚簍紀錄，不能在這裡改。回傳是否有變更。
func (a *AdminService) SetCodexEntry(ctx context.Context, actor string, playerID int64, itemCode string,
	quantity, bestQuality int) (bool, error) {
	quantity, err := featurecfg.IntValue(quantity, "累計數量", 0, 1_000_000_000_000)
	if err != nil {
		return false, err
	}
	qualityNames := QualityNames()
	if bestQuality, err = featurecfg.IntValue(bestQuality, "最高品質", 0, len(qualityNames)-1); err != nil {
		return false, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (bool, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return false, err
		}
		cats, err := codexCategories(ctx, t, farm.ID)
		if err != nil {
			return false, err
		}
		names := map[string]string{}
		for _, c := range cats {
			if slices.Contains(CodexEditable, c.Key) {
				for _, e := range c.Entries {
					names[e.Code] = e.Name
				}
			}
		}
		name, ok := names[itemCode]
		if !ok {
			return false, gameerr.NotFoundf("「%s」不是可調整的圖鑑項目。", itemCode)
		}
		row, err := db.QueryStruct[models.FarmCodexEntry](ctx, t,
			"SELECT * FROM farm_codex WHERE farm_id = $1 AND item_code = $2", farm.ID, itemCode)
		if err != nil {
			return false, err
		}
		before := []int{0, 0}
		if row != nil {
			before = []int{row.Quantity, row.BestQuality}
		}
		wantBest := 0
		if quantity != 0 {
			wantBest = bestQuality
		}
		if before[0] == quantity && before[1] == wantBest {
			return false, nil
		}
		switch {
		case quantity == 0:
			if row != nil {
				_, err = t.Exec(ctx, "DELETE FROM farm_codex WHERE farm_id = $1 AND item_code = $2", farm.ID, itemCode)
			}
		case row == nil:
			_, err = t.Exec(ctx, "INSERT INTO farm_codex (farm_id, item_code, quantity, best_quality, first_at) "+
				"VALUES ($1, $2, $3, $4, $5)", farm.ID, itemCode, quantity, bestQuality, a.now())
		default:
			_, err = t.Exec(ctx, "UPDATE farm_codex SET quantity = $3, best_quality = $4 WHERE farm_id = $1 "+
				"AND item_code = $2", farm.ID, itemCode, quantity, bestQuality)
		}
		if err != nil {
			return false, err
		}
		after := "未取得"
		if quantity != 0 {
			after = fmt.Sprintf("%d（%s）", quantity, qualityNames[bestQuality])
		}
		return true, a.adjust(ctx, t, farm, actor, "set_codex_entry", fmt.Sprintf("圖鑑 %s → %s", name, after),
			map[string]any{"item_code": itemCode, "before": before, "after": []int{quantity, bestQuality}}, rec{})
	})
}

// ResetWeeklyPurchases 清除玩家本週的每週特賣購買量（可以重新買到限購數量）；回傳清除的項目數。
func (a *AdminService) ResetWeeklyPurchases(ctx context.Context, actor string, playerID int64) (int, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (int, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return 0, err
		}
		start := specialshop.WeekStart(a.now())
		bought, err := weeklyBought(ctx, t, farm.ID, start)
		if err != nil {
			return 0, err
		}
		if len(bought) == 0 {
			return 0, nil
		}
		if _, err := t.Exec(ctx, "DELETE FROM weekly_shop_purchases WHERE farm_id = $1 AND week_start = $2",
			farm.ID, db.DateParam(start)); err != nil {
			return 0, err
		}
		return len(bought), a.adjust(ctx, t, farm, actor, "reset_weekly_purchases", "重設本週特賣購買量",
			map[string]any{"week_start": start.Format(time.DateOnly), "before": bought}, rec{})
	})
}
