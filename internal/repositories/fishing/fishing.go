// Package fishing 釣魚資料存取：魚種、釣具、玩家釣具、設定版本、釣魚 Session、魚簍、分享與統計（全部參數化查詢）。
// 對應 Python app/repositories/fishing.py。
package fishing

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/fishing"
	"farmgm/internal/numfmt"
)

const (
	fishTypeCols = "id, code, name, emoji, rarity, min_price, max_price, min_weight_g, max_weight_g, difficulty, " +
		"description, sort_order, active, required_baits, seasons, hour_start, hour_end"
	gearCols    = "id, code, kind, name, emoji, description, price, required_level, stock, effects, sort_order, active"
	ownedCols   = "id, farm_id, gear_code, kind, quantity, equipped, acquired_at"
	sessionCols = "id, farm_id, status, settings_version, settings, gear, fish_code, rarity, weight_g, price, state, turns, " +
		"started_at, bite_at, react_deadline, action_deadline, ended_at, context_group_id"
	fishCols  = "id, farm_id, session_id, fish_code, rarity, weight_g, price, caught_at, sold_at, sold_price, donated_at"
	guardCols = "farm_id, casts_since_check, check_after, suspicion, challenge, challenge_expires_at, locked_until, " +
		"consecutive_failures, failed_total, passed_total"
)

// prefixed 把 "a, b" 轉成 "x.a, x.b"。
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ", ")
}

// ---------- 設定 ----------

// CurrentSettings 回傳 (版本, 設定 JSON)；資料庫沒有設定時使用預設值（版本 0）。設定由呼叫端以 fishing.ValidateSettings 解析。
func CurrentSettings(ctx context.Context, t *db.Tx) (int, json.RawMessage, error) {
	row, err := db.QueryStruct[models.FishingSetting](ctx, t,
		"SELECT version, settings, created_by, created_at FROM fishing_settings ORDER BY version DESC LIMIT 1")
	if err != nil {
		return 0, nil, err
	}
	if row == nil {
		data, err := json.Marshal(fishing.DefaultSettings())
		return 0, data, err
	}
	return row.Version, row.Settings, nil
}

// AddSettingsVersion 新增設定版本（最新版本 + 1）。settings 可為任何可轉成 JSON 的值。
func AddSettingsVersion(ctx context.Context, t *db.Tx, settings any, actor string, now time.Time) (int, error) {
	latest, _, err := db.QueryValue[int](ctx, t, "SELECT coalesce(max(version), 0) FROM fishing_settings")
	if err != nil {
		return 0, err
	}
	version := latest + 1
	_, err = t.Exec(ctx, "INSERT INTO fishing_settings (version, settings, created_by, created_at) VALUES ($1, $2, $3, $4)",
		version, settings, actor, now)
	return version, err
}

// SettingsHistory limit 預設 10。
func SettingsHistory(ctx context.Context, t *db.Tx, limit int) ([]models.FishingSetting, error) {
	return db.QueryStructs[models.FishingSetting](ctx, t,
		"SELECT version, settings, created_by, created_at FROM fishing_settings ORDER BY version DESC LIMIT $1", limit)
}

// ---------- 魚種與釣具目錄 ----------

// ListFishTypes 魚種目錄（經程序內目錄快取，回傳的資料列唯讀）。activeOnly 預設 false。
func ListFishTypes(ctx context.Context, t *db.Tx, activeOnly bool) ([]models.FishType, error) {
	key := "fish:all"
	where := ""
	if activeOnly {
		key, where = "fish:active", " WHERE active"
	}
	return db.CatalogRows(t, key, func() ([]models.FishType, error) {
		return db.QueryStructs[models.FishType](ctx, t, "SELECT "+fishTypeCols+" FROM fish_types"+where+" ORDER BY sort_order, id")
	})
}

// GetGuard 呼叫端必須已鎖定農場列（同一農場的防腳本狀態依序更新）。
func GetGuard(ctx context.Context, t *db.Tx, farmID int64) (*models.FishingGuard, error) {
	return db.QueryStruct[models.FishingGuard](ctx, t, "SELECT "+guardCols+" FROM fishing_guards WHERE farm_id = $1", farmID)
}

// GetFishType forUpdate 預設 false。
func GetFishType(ctx context.Context, t *db.Tx, code string, forUpdate bool) (*models.FishType, error) {
	sql := "SELECT " + fishTypeCols + " FROM fish_types WHERE code = $1 LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FishType](ctx, t, sql, code)
}

// FishTypeNames 所有魚種名稱（集合）。
func FishTypeNames(ctx context.Context, t *db.Tx) (map[string]bool, error) {
	names, err := db.QueryValues[string](ctx, t, "SELECT name FROM fish_types")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// ListGear 依種類（竿、線、餌）、排序、ID 排列。activeOnly 預設 false。
func ListGear(ctx context.Context, t *db.Tx, activeOnly bool) ([]models.FishingGear, error) {
	where := ""
	if activeOnly {
		where = " WHERE active"
	}
	return db.QueryStructs[models.FishingGear](ctx, t, "SELECT "+gearCols+" FROM fishing_gear"+where+
		" ORDER BY CASE kind WHEN 'rod' THEN 1 WHEN 'line' THEN 2 WHEN 'bait' THEN 3 END, sort_order, id")
}

// GetGear forUpdate 預設 false。
func GetGear(ctx context.Context, t *db.Tx, code string, forUpdate bool) (*models.FishingGear, error) {
	sql := "SELECT " + gearCols + " FROM fishing_gear WHERE code = $1 LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FishingGear](ctx, t, sql, code)
}

// ---------- 玩家釣具 ----------

func OwnedGear(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmFishingGear, error) {
	return db.QueryStructs[models.FarmFishingGear](ctx, t, "SELECT "+ownedCols+" FROM farm_fishing_gear WHERE farm_id = $1", farmID)
}

// GetOwned forUpdate 預設 false。
func GetOwned(ctx context.Context, t *db.Tx, farmID int64, code string, forUpdate bool) (*models.FarmFishingGear, error) {
	sql := "SELECT " + ownedCols + " FROM farm_fishing_gear WHERE farm_id = $1 AND gear_code = $2 LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FarmFishingGear](ctx, t, sql, farmID, code)
}

func AddOwned(ctx context.Context, t *db.Tx, farmID int64, gear models.FishingGear, quantity int, now time.Time,
	equipped bool) (*models.FarmFishingGear, error) {
	return db.QueryStruct[models.FarmFishingGear](ctx, t,
		"INSERT INTO farm_fishing_gear (farm_id, gear_code, kind, quantity, equipped, acquired_at) "+
			"VALUES ($1, $2, $3, $4, $5, $6) RETURNING "+ownedCols, farmID, gear.Code, gear.Kind, quantity, equipped, now)
}

func UnequipKind(ctx context.Context, t *db.Tx, farmID int64, kind string) error {
	_, err := t.Exec(ctx, "UPDATE farm_fishing_gear SET equipped = false WHERE farm_id = $1 AND kind = $2 AND equipped",
		farmID, kind)
	return err
}

// Equipped 裝備中的釣具與其目錄資料。
type Equipped struct {
	Owned models.FarmFishingGear
	Gear  models.FishingGear
}

// EquippedGear 種類 → (玩家釣具, 釣具目錄)。
func EquippedGear(ctx context.Context, t *db.Tx, farmID int64) (map[string]Equipped, error) {
	rows, err := t.Query(ctx, "SELECT "+prefixed("o", ownedCols)+", "+prefixed("g", gearCols)+
		" FROM farm_fishing_gear o JOIN fishing_gear g ON g.code = o.gear_code WHERE o.farm_id = $1 AND o.equipped", farmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Equipped{}
	for rows.Next() {
		var e Equipped
		o, g := &e.Owned, &e.Gear
		if err := rows.Scan(&o.ID, &o.FarmID, &o.GearCode, &o.Kind, &o.Quantity, &o.Equipped, &o.AcquiredAt,
			&g.ID, &g.Code, &g.Kind, &g.Name, &g.Emoji, &g.Description, &g.Price, &g.RequiredLevel, &g.Stock,
			&g.Effects, &g.SortOrder, &g.Active); err != nil {
			return nil, err
		}
		out[o.Kind] = e
	}
	return out, rows.Err()
}

// ---------- 釣魚 Session ----------

// ActiveSession forUpdate 預設 false。
func ActiveSession(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.FishingSession, error) {
	sql := "SELECT " + sessionCols + " FROM fishing_sessions WHERE farm_id = $1 AND status = ANY($2) LIMIT 1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.FishingSession](ctx, t, sql, farmID, fishing.ActiveStatuses)
}

func LastSession(ctx context.Context, t *db.Tx, farmID int64) (*models.FishingSession, error) {
	return db.QueryStruct[models.FishingSession](ctx, t, "SELECT "+sessionCols+
		" FROM fishing_sessions WHERE farm_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1", farmID)
}

// StaleActiveSessions 已超過期限仍是進行中狀態的 Session（Bot 重啟後清理用），加鎖並略過已被鎖定的列。limit 預設 500。
func StaleActiveSessions(ctx context.Context, t *db.Tx, before time.Time, limit int) ([]models.FishingSession, error) {
	return db.QueryStructs[models.FishingSession](ctx, t, "SELECT "+sessionCols+" FROM fishing_sessions "+
		"WHERE status = ANY($1) AND ((status = 'casting' AND react_deadline < $2) "+
		"OR (status = 'fighting' AND action_deadline < $2)) ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED",
		fishing.ActiveStatuses, before, limit)
}

func SessionFish(ctx context.Context, t *db.Tx, sessionID int64) (*models.FarmFish, error) {
	return db.QueryStruct[models.FarmFish](ctx, t, "SELECT "+fishCols+" FROM farm_fish WHERE session_id = $1 LIMIT 1", sessionID)
}

// SessionStats 玩家的釣魚 Session 統計。
type SessionStats struct {
	Sessions  int `json:"sessions"`
	Landed    int `json:"landed"`
	Failed    int `json:"failed"`
	Legendary int `json:"legendary"`
}

func PlayerSessionStats(ctx context.Context, t *db.Tx, farmID int64) (SessionStats, error) {
	var s SessionStats
	err := t.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE status = 'landed'), "+
		"count(*) FILTER (WHERE status = ANY($2)), count(*) FILTER (WHERE status = 'landed' AND rarity = 'legendary') "+
		"FROM fishing_sessions WHERE farm_id = $1", farmID, fishing.FailStatuses).
		Scan(&s.Sessions, &s.Landed, &s.Failed, &s.Legendary)
	return s, err
}

// ---------- 魚簍 ----------

// InBasket SQL 條件：還在魚簍中的魚（沒有賣掉，也沒有捐給世界 Boss）。alias 是 farm_fish 的別名（"" 表示不加前綴）。
func InBasket(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return p + "sold_at IS NULL AND " + p + "donated_at IS NULL"
}

// FishWithType 魚與其魚種。
type FishWithType struct {
	Fish models.FarmFish
	Type models.FishType
}

func scanFishWithType(ctx context.Context, t *db.Tx, where string, args ...any) ([]FishWithType, error) {
	rows, err := t.Query(ctx, "SELECT "+prefixed("f", fishCols)+", "+prefixed("ft", fishTypeCols)+
		" FROM farm_fish f JOIN fish_types ft ON ft.code = f.fish_code "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FishWithType
	for rows.Next() {
		var x FishWithType
		f, ft := &x.Fish, &x.Type
		if err := rows.Scan(&f.ID, &f.FarmID, &f.SessionID, &f.FishCode, &f.Rarity, &f.WeightG, &f.Price, &f.CaughtAt,
			&f.SoldAt, &f.SoldPrice, &f.DonatedAt,
			&ft.ID, &ft.Code, &ft.Name, &ft.Emoji, &ft.Rarity, &ft.MinPrice, &ft.MaxPrice, &ft.MinWeightG, &ft.MaxWeightG,
			&ft.Difficulty, &ft.Description, &ft.SortOrder, &ft.Active, &ft.RequiredBaits, &ft.Seasons, &ft.HourStart,
			&ft.HourEnd); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Basket 魚簍中的魚，依價值、重量、ID 排序。
func Basket(ctx context.Context, t *db.Tx, farmID int64) ([]FishWithType, error) {
	return scanFishWithType(ctx, t, "WHERE f.farm_id = $1 AND "+InBasket("f")+" ORDER BY f.price DESC, f.weight_g DESC, f.id", farmID)
}

func BasketCount(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	v, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM farm_fish WHERE farm_id = $1 AND "+InBasket(""), farmID)
	return v, err
}

// GetFarmFish 只回傳這座農場自己的魚（防止查詢或操作別人的魚）；沒有時回傳 nil。
func GetFarmFish(ctx context.Context, t *db.Tx, farmID, fishID int64) (*FishWithType, error) {
	rows, err := scanFishWithType(ctx, t, "WHERE f.id = $1 AND f.farm_id = $2 LIMIT 1", fishID, farmID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// LockUnsold 鎖定這座農場尚未販售的魚；fishIDs 為 nil 表示全部。
func LockUnsold(ctx context.Context, t *db.Tx, farmID int64, fishIDs []int64) ([]models.FarmFish, error) {
	sql := "SELECT " + fishCols + " FROM farm_fish WHERE farm_id = $1 AND " + InBasket("")
	args := []any{farmID}
	if fishIDs != nil {
		sql += " AND id = ANY($2)"
		args = append(args, fishIDs)
	}
	return db.QueryStructs[models.FarmFish](ctx, t, sql+" ORDER BY id FOR UPDATE", args...)
}

// BestWeights 這座農場每種魚的最大捕獲重量（含已販售的魚）。
func BestWeights(ctx context.Context, t *db.Tx, farmID int64) (map[string]int, error) {
	rows, err := t.Query(ctx, "SELECT fish_code, max(weight_g) FROM farm_fish WHERE farm_id = $1 GROUP BY fish_code", farmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code string
		var w int
		if err := rows.Scan(&code, &w); err != nil {
			return nil, err
		}
		out[code] = w
	}
	return out, rows.Err()
}

// CodexEntry 魚類圖鑑的一種魚。
type CodexEntry struct {
	Count         int       `json:"count"`
	MaxWeightG    int       `json:"max_weight_g"`
	MaxPrice      int       `json:"max_price"`
	FirstCaughtAt time.Time `json:"first_caught_at"`
}

// Codex 魚種代碼 → 圖鑑資料。
func Codex(ctx context.Context, t *db.Tx, farmID int64) (map[string]CodexEntry, error) {
	rows, err := t.Query(ctx, "SELECT fish_code, count(*), max(weight_g), max(price), min(caught_at) FROM farm_fish "+
		"WHERE farm_id = $1 GROUP BY fish_code", farmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CodexEntry{}
	for rows.Next() {
		var code string
		var e CodexEntry
		if err := rows.Scan(&code, &e.Count, &e.MaxWeightG, &e.MaxPrice, &e.FirstCaughtAt); err != nil {
			return nil, err
		}
		out[code] = e
	}
	return out, rows.Err()
}

// AddShare groupID 為 nil 表示私訊分享。
func AddShare(ctx context.Context, t *db.Tx, fishID, farmID int64, groupID *int64, now time.Time) (*models.FishShare, error) {
	return db.QueryStruct[models.FishShare](ctx, t, "INSERT INTO fish_shares (fish_id, farm_id, group_id, shared_at) "+
		"VALUES ($1, $2, $3, $4) RETURNING id, fish_id, farm_id, group_id, shared_at", fishID, farmID, groupID, now)
}

// LastShareAt 沒有分享過時回傳 nil。
func LastShareAt(ctx context.Context, t *db.Tx, fishID int64) (*time.Time, error) {
	v, _, err := db.QueryValue[*time.Time](ctx, t, "SELECT max(shared_at) FROM fish_shares WHERE fish_id = $1", fishID)
	return v, err
}

// ---------- 排行榜 ----------

// CatchRow 捕獲數排行的一列。
type CatchRow struct {
	PlayerName string `json:"player_name"`
	Count      int    `json:"count"`
	Legendary  int    `json:"legendary"`
}

// BestRow 最高價值魚排行的一列。
type BestRow struct {
	PlayerName string `json:"player_name"`
	FishName   string `json:"fish_name"`
	Emoji      string `json:"emoji"`
	Rarity     string `json:"rarity"`
	WeightG    int    `json:"weight_g"`
	Price      int    `json:"price"`
}

// Leaderboard 釣魚排行榜。
type Leaderboard struct {
	Catches []CatchRow `json:"catches"`
	Best    []BestRow  `json:"best"`
}

// LeaderboardOf 依捕獲數與最高價值的魚排名；groupID 有值時只列該群組成員。limit 預設 10。（Python 名稱 leaderboard）
func LeaderboardOf(ctx context.Context, t *db.Tx, groupID *int64, limit int) (Leaderboard, error) {
	out := Leaderboard{Catches: []CatchRow{}, Best: []BestRow{}}
	scope, args := "", []any{limit}
	if groupID != nil {
		scope = " JOIN group_members gm ON gm.player_id = fa.player_id AND gm.group_id = $2"
		args = append(args, *groupID)
	}
	rows, err := t.Query(ctx, "SELECT p.display_name, count(f.id), count(f.id) FILTER (WHERE f.rarity = 'legendary') "+
		"FROM farm_fish f JOIN farms fa ON fa.id = f.farm_id JOIN players p ON p.id = fa.player_id"+scope+
		" GROUP BY p.id, p.display_name ORDER BY count(f.id) DESC, p.id LIMIT $1", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var r CatchRow
		if err := rows.Scan(&r.PlayerName, &r.Count, &r.Legendary); err != nil {
			rows.Close()
			return out, err
		}
		out.Catches = append(out.Catches, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = t.Query(ctx, "SELECT p.display_name, ft.name, ft.emoji, r.rarity, r.weight_g, r.price FROM ("+
		"SELECT farm_id, fish_code, weight_g, price, rarity, row_number() OVER (PARTITION BY farm_id "+
		"ORDER BY price DESC, weight_g DESC, id) AS rn FROM farm_fish) r "+
		"JOIN farms fa ON fa.id = r.farm_id JOIN players p ON p.id = fa.player_id "+
		"JOIN fish_types ft ON ft.code = r.fish_code"+scope+
		" WHERE r.rn = 1 ORDER BY r.price DESC, r.weight_g DESC LIMIT $1", args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r BestRow
		if err := rows.Scan(&r.PlayerName, &r.FishName, &r.Emoji, &r.Rarity, &r.WeightG, &r.Price); err != nil {
			return out, err
		}
		out.Best = append(out.Best, r)
	}
	return out, rows.Err()
}

// ---------- 監控統計 ----------

// RarityStats 依稀有度的結束 Session 統計。
type RarityStats struct {
	Sessions int `json:"sessions"`
	Landed   int `json:"landed"`
}

// Overview 監控網站的釣魚總覽。SuccessRate 為 nil 表示沒有已結束的 Session。
type Overview struct {
	Sessions         int                    `json:"sessions"`
	Landed           int                    `json:"landed"`
	Failed           int                    `json:"failed"`
	Active           int                    `json:"active"`
	Players          int                    `json:"players"`
	SuccessRate      *float64               `json:"success_rate"`
	LegendaryCaught  int                    `json:"legendary_caught"`
	FishSold         int                    `json:"fish_sold"`
	SellTransactions int                    `json:"sell_transactions"`
	SellGold         int                    `json:"sell_gold"`
	GearSpent        int                    `json:"gear_spent"`
	Shares           int                    `json:"shares"`
	InBaskets        int                    `json:"in_baskets"`
	ByRarity         map[string]RarityStats `json:"by_rarity"`
	ByStatus         map[string]int         `json:"by_status"`
}

// OverviewOf 釣魚總覽（Python 名稱 overview；now 參數未使用，保留以對應原簽名）。
func OverviewOf(ctx context.Context, t *db.Tx, now time.Time) (Overview, error) {
	_ = now
	o := Overview{ByRarity: map[string]RarityStats{}, ByStatus: map[string]int{}}
	err := t.QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE status = 'landed'), "+
		"count(*) FILTER (WHERE status = ANY($1)), count(*) FILTER (WHERE status = ANY($2)), count(DISTINCT farm_id) "+
		"FROM fishing_sessions", fishing.FailStatuses, fishing.ActiveStatuses).
		Scan(&o.Sessions, &o.Landed, &o.Failed, &o.Active, &o.Players)
	if err != nil {
		return o, err
	}
	rows, err := t.Query(ctx, "SELECT rarity, count(*), count(*) FILTER (WHERE status = 'landed') FROM fishing_sessions "+
		"WHERE status <> ALL($1) GROUP BY rarity", fishing.ActiveStatuses)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var r string
		var s RarityStats
		if err := rows.Scan(&r, &s.Sessions, &s.Landed); err != nil {
			rows.Close()
			return o, err
		}
		o.ByRarity[r] = s
	}
	rows.Close()
	rows, err = t.Query(ctx, "SELECT status, count(*) FROM fishing_sessions GROUP BY status")
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return o, err
		}
		o.ByStatus[st] = n
	}
	rows.Close()
	if err := t.QueryRow(ctx, "SELECT count(*), coalesce(sum(gold_delta), 0)::bigint, coalesce(sum(quantity), 0)::bigint "+
		"FROM game_transactions WHERE event_type = 'fish_sell'").Scan(&o.SellTransactions, &o.SellGold, &o.FishSold); err != nil {
		return o, err
	}
	var gearCount int
	if err := t.QueryRow(ctx, "SELECT count(*), coalesce(sum(-gold_delta), 0)::bigint FROM game_transactions "+
		"WHERE event_type = 'fishing_buy'").Scan(&gearCount, &o.GearSpent); err != nil {
		return o, err
	}
	if err := t.QueryRow(ctx, "SELECT (SELECT count(*) FROM farm_fish WHERE rarity = 'legendary'), "+
		"(SELECT count(*) FROM fish_shares), (SELECT count(*) FROM farm_fish WHERE "+InBasket("")+")").
		Scan(&o.LegendaryCaught, &o.Shares, &o.InBaskets); err != nil {
		return o, err
	}
	if done := o.Sessions - o.Active; done != 0 {
		rate := numfmt.RoundN(float64(o.Landed)*100/float64(done), 1)
		o.SuccessRate = &rate
	}
	return o, nil
}

// PlayerRow 監控網站的玩家釣魚統計。DiscordUserID 只有 Discord 玩家才有值。
type PlayerRow struct {
	PlayerID      int64             `json:"player_id"`
	PlayerName    string            `json:"player_name"`
	DiscordUserID *string           `json:"discord_user_id"`
	FarmID        int64             `json:"farm_id"`
	Sessions      int               `json:"sessions"`
	Landed        int               `json:"landed"`
	Failed        int               `json:"failed"`
	Legendary     int               `json:"legendary"`
	Sold          int               `json:"sold"`
	SoldGold      int               `json:"sold_gold"`
	Basket        int               `json:"basket"`
	Shares        int               `json:"shares"`
	LastAt        *time.Time        `json:"last_at"`
	Equipped      map[string]string `json:"equipped"`
}

// PlayerRows 玩家釣魚統計（有釣魚紀錄或持有釣具的玩家），支援名稱／ID 搜尋與分頁。q 為 "" 表示不搜尋。
func PlayerRows(ctx context.Context, t *db.Tx, q string, limit, offset int) (int, []PlayerRow, error) {
	base := "SELECT p.id, p.display_name, p.external_platform, p.external_user_id, fa.id AS farm_id, " +
		"coalesce(s.sessions, 0) AS sessions, coalesce(s.landed, 0), coalesce(s.failed, 0), coalesce(fi.legendary, 0), " +
		"coalesce(fi.sold, 0), coalesce(fi.sold_gold, 0), coalesce(fi.basket, 0), coalesce(sh.shares, 0), s.last_at " +
		"FROM farms fa JOIN players p ON p.id = fa.player_id " +
		"LEFT JOIN (SELECT farm_id, count(*) AS sessions, count(*) FILTER (WHERE status = 'landed') AS landed, " +
		"count(*) FILTER (WHERE status = ANY($1)) AS failed, max(started_at) AS last_at FROM fishing_sessions " +
		"GROUP BY farm_id) s ON s.farm_id = fa.id " +
		"LEFT JOIN (SELECT farm_id, count(*) FILTER (WHERE rarity = 'legendary') AS legendary, " +
		"count(*) FILTER (WHERE sold_at IS NOT NULL) AS sold, coalesce(sum(sold_price), 0)::bigint AS sold_gold, " +
		"count(*) FILTER (WHERE " + InBasket("") + ") AS basket FROM farm_fish GROUP BY farm_id) fi ON fi.farm_id = fa.id " +
		"LEFT JOIN (SELECT farm_id, count(*) AS shares FROM fish_shares GROUP BY farm_id) sh ON sh.farm_id = fa.id " +
		"WHERE (s.farm_id IS NOT NULL OR fa.id IN (SELECT DISTINCT farm_id FROM farm_fishing_gear))"
	args := []any{fishing.FailStatuses}
	if q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		cond := "p.display_name ILIKE $2 ESCAPE '\\'"
		digits := strings.TrimLeft(q, "#")
		if isDigits(digits) && len(digits) <= 18 {
			id, _ := strconv.ParseInt(digits, 10, 64)
			args = append(args, id, digits)
			cond = "(" + cond + " OR p.id = $3 OR p.external_user_id = $4)"
		}
		base += " AND " + cond
	}
	total, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM ("+base+") sub", args...)
	if err != nil {
		return 0, nil, err
	}
	n := len(args)
	rows, err := t.Query(ctx, base+" ORDER BY coalesce(s.sessions, 0) DESC, p.id LIMIT $"+strconv.Itoa(n+1)+
		" OFFSET $"+strconv.Itoa(n+2), append(args, limit, offset)...)
	if err != nil {
		return 0, nil, err
	}
	out := []PlayerRow{}
	var farmIDs []int64
	for rows.Next() {
		var r PlayerRow
		var platform, extID *string
		if err := rows.Scan(&r.PlayerID, &r.PlayerName, &platform, &extID, &r.FarmID, &r.Sessions, &r.Landed, &r.Failed,
			&r.Legendary, &r.Sold, &r.SoldGold, &r.Basket, &r.Shares, &r.LastAt); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if platform != nil && *platform == "discord" {
			r.DiscordUserID = extID
		}
		r.Equipped = map[string]string{}
		out = append(out, r)
		farmIDs = append(farmIDs, r.FarmID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if len(farmIDs) > 0 {
		equipped := map[int64]map[string]string{}
		rows, err := t.Query(ctx, "SELECT o.farm_id, o.kind, g.name, o.quantity FROM farm_fishing_gear o "+
			"JOIN fishing_gear g ON g.code = o.gear_code WHERE o.farm_id = ANY($1) AND o.equipped", farmIDs)
		if err != nil {
			return 0, nil, err
		}
		for rows.Next() {
			var farmID int64
			var kind, name string
			var qty int
			if err := rows.Scan(&farmID, &kind, &name, &qty); err != nil {
				rows.Close()
				return 0, nil, err
			}
			if equipped[farmID] == nil {
				equipped[farmID] = map[string]string{}
			}
			if kind == "bait" {
				equipped[farmID][kind] = name + "×" + strconv.Itoa(qty)
			} else {
				equipped[farmID][kind] = name
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, nil, err
		}
		for i := range out {
			if e, ok := equipped[out[i].FarmID]; ok {
				out[i].Equipped = e
			}
		}
	}
	return total, out, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func escapeLike(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// DetailCatch 玩家詳情：捕獲紀錄。
type DetailCatch struct {
	ID        int64      `db:"id" json:"id"`
	FishCode  string     `db:"fish_code" json:"fish_code"`
	Name      string     `db:"name" json:"name"`
	Emoji     string     `db:"emoji" json:"emoji"`
	Rarity    string     `db:"rarity" json:"rarity"`
	WeightG   int        `db:"weight_g" json:"weight_g"`
	Price     int        `db:"price" json:"price"`
	CaughtAt  time.Time  `db:"caught_at" json:"caught_at"`
	SoldAt    *time.Time `db:"sold_at" json:"sold_at"`
	SoldPrice *int       `db:"sold_price" json:"sold_price"`
}

// DetailSession 玩家詳情：Session 紀錄。Rod／Line／Bait 為 nil 表示沒有裝備。
type DetailSession struct {
	ID              int64      `json:"id"`
	Status          string     `json:"status"`
	StatusName      string     `json:"status_name"`
	Rarity          string     `json:"rarity"`
	FishCode        string     `json:"fish_code"`
	Turns           int        `json:"turns"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	SettingsVersion int        `json:"settings_version"`
	Rod             *string    `json:"rod"`
	Line            *string    `json:"line"`
	Bait            *string    `json:"bait"`
}

// DetailTransaction 玩家詳情：釣魚相關交易紀錄。
type DetailTransaction struct {
	EventType   string    `db:"event_type" json:"event_type"`
	Description string    `db:"description" json:"description"`
	GoldDelta   int       `db:"gold_delta" json:"gold_delta"`
	Quantity    int       `db:"quantity" json:"quantity"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

// DetailShare 玩家詳情：分享紀錄。
type DetailShare struct {
	SharedAt time.Time `db:"shared_at" json:"shared_at"`
	GroupID  *int64    `db:"group_id" json:"group_id"`
	FishName string    `db:"fish_name" json:"fish_name"`
	WeightG  int       `db:"weight_g" json:"weight_g"`
	Rarity   string    `db:"rarity" json:"rarity"`
}

// DetailGear 玩家詳情：持有釣具。
type DetailGear struct {
	Code     string `db:"code" json:"code"`
	Name     string `db:"name" json:"name"`
	Kind     string `db:"kind" json:"kind"`
	Quantity int    `db:"quantity" json:"quantity"`
	Equipped bool   `db:"equipped" json:"equipped"`
}

// PlayerDetailData 監控網站的玩家釣魚詳情。
type PlayerDetailData struct {
	Catches      []DetailCatch       `json:"catches"`
	Sessions     []DetailSession     `json:"sessions"`
	Transactions []DetailTransaction `json:"transactions"`
	Shares       []DetailShare       `json:"shares"`
	Gear         []DetailGear        `json:"gear"`
}

// gearName 從 Session 的 gear 快照取出某種釣具的名稱（Python：(s.gear.get(kind) or {}).get("name")）。
func gearName(gear map[string]map[string]any, kind string) *string {
	if g := gear[kind]; g != nil {
		if name, ok := g["name"].(string); ok {
			return &name
		}
	}
	return nil
}

// PlayerDetail limit 預設 30。
func PlayerDetail(ctx context.Context, t *db.Tx, farmID int64, limit int) (PlayerDetailData, error) {
	out := PlayerDetailData{Catches: []DetailCatch{}, Sessions: []DetailSession{}, Transactions: []DetailTransaction{},
		Shares: []DetailShare{}, Gear: []DetailGear{}}
	var err error
	if out.Catches, err = queryList[DetailCatch](ctx, t, "SELECT f.id, f.fish_code, ft.name, ft.emoji, f.rarity, "+
		"f.weight_g, f.price, f.caught_at, f.sold_at, f.sold_price FROM farm_fish f JOIN fish_types ft ON ft.code = f.fish_code "+
		"WHERE f.farm_id = $1 ORDER BY f.caught_at DESC, f.id DESC LIMIT $2", farmID, limit); err != nil {
		return out, err
	}
	sessions, err := db.QueryStructs[models.FishingSession](ctx, t, "SELECT "+sessionCols+
		" FROM fishing_sessions WHERE farm_id = $1 ORDER BY started_at DESC, id DESC LIMIT $2", farmID, limit)
	if err != nil {
		return out, err
	}
	for _, s := range sessions {
		var gear map[string]map[string]any
		_ = json.Unmarshal(s.Gear, &gear)
		name, ok := fishing.StatusNames[s.Status]
		if !ok {
			name = s.Status
		}
		out.Sessions = append(out.Sessions, DetailSession{ID: s.ID, Status: s.Status, StatusName: name, Rarity: s.Rarity,
			FishCode: s.FishCode, Turns: s.Turns, StartedAt: s.StartedAt, EndedAt: s.EndedAt,
			SettingsVersion: s.SettingsVersion, Rod: gearName(gear, "rod"), Line: gearName(gear, "line"),
			Bait: gearName(gear, "bait")})
	}
	if out.Transactions, err = queryList[DetailTransaction](ctx, t, "SELECT event_type, description, gold_delta, "+
		"quantity, created_at FROM game_transactions WHERE farm_id = $1 AND event_type IN ('fish_sell', 'fishing_buy') "+
		"ORDER BY created_at DESC, id DESC LIMIT $2", farmID, limit); err != nil {
		return out, err
	}
	if out.Shares, err = queryList[DetailShare](ctx, t, "SELECT s.shared_at, s.group_id, ft.name AS fish_name, "+
		"f.weight_g, f.rarity FROM fish_shares s JOIN farm_fish f ON f.id = s.fish_id JOIN fish_types ft ON ft.code = f.fish_code "+
		"WHERE s.farm_id = $1 ORDER BY s.shared_at DESC LIMIT $2", farmID, limit); err != nil {
		return out, err
	}
	out.Gear, err = queryList[DetailGear](ctx, t, "SELECT o.gear_code AS code, g.name, o.kind, o.quantity, o.equipped "+
		"FROM farm_fishing_gear o JOIN fishing_gear g ON g.code = o.gear_code WHERE o.farm_id = $1 ORDER BY o.kind, o.id", farmID)
	return out, err
}

// GearUsage 每種釣具被多少座農場持有（管理網站顯示用）。
func GearUsage(ctx context.Context, t *db.Tx) (map[string]int, error) {
	return countBy(ctx, t, "SELECT gear_code, count(*) FROM farm_fishing_gear GROUP BY gear_code")
}

// FishCounts 每種魚被捕獲的數量。
func FishCounts(ctx context.Context, t *db.Tx) (map[string]int, error) {
	return countBy(ctx, t, "SELECT fish_code, count(*) FROM farm_fish GROUP BY fish_code")
}

func countBy(ctx context.Context, t *db.Tx, sql string) (map[string]int, error) {
	rows, err := t.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// queryList 同 db.QueryStructs，但沒有資料時回傳空切片（JSON 輸出為 []，與 Python 一致）。
func queryList[T any](ctx context.Context, t *db.Tx, sql string, args ...any) ([]T, error) {
	rows, err := db.QueryStructs[T](ctx, t, sql, args...)
	if rows == nil {
		rows = []T{}
	}
	return rows, err
}
