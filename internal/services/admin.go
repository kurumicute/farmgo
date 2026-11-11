package services

// 管理員操作：作物／動物種類、魚種與釣具、釣魚設定、限時事件，以及調整單一玩家的金幣、等級、背包、土地、建築、動物與作物
// （對應 Python app/services/admin_service.py）。新功能（簽到、寵物、特賣與升級、成就、稱號、圖鑑）的規則設定與玩家調整在
// progress_admin.go。
//
//   - 每個方法都是一筆資料庫交易，並寫入管理操作紀錄（admin_actions）。
//   - 調整玩家資料時另外寫入一筆 admin_adjust 交易紀錄，玩家查詢紀錄時可看到；不計入任務或競賽。
//   - 代碼建立後不可修改（背包、交易紀錄與動物都以代碼參照）。
//   - 已被使用過的作物／動物種類只能停用；從未使用的才能刪除。
//
// 與 Python 不同：
//   - 管理網站送來的資料（Python dict）在 Go 是 map[string]any；整數可以是 int 或 json.Number（featurecfg.AsInt），
//     bool 與浮點數不算整數（與 Python isinstance(int) 相同）。玩家調整的參數直接用 Go 型別。
//   - Python 回傳 dict 的方法改成 struct（CropView、InventoryChange、FarmValues 等）；刪除類方法 Python 回傳
//     {"deleted": …}，Go 只回傳 error。
//   - 目錄寫入後的快取清除由 db.Tx 依 SQL 自動判斷（同 Python catalog_cache 的 do_orm_execute 監聽）。

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	eventrules "farmgm/internal/game/events"
	"farmgm/internal/game/featurecfg"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/community"
	"farmgm/internal/repositories/crops"
	eventsrepo "farmgm/internal/repositories/events"
	"farmgm/internal/repositories/farms"
	fishrepo "farmgm/internal/repositories/fishing"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/ranch"
)

var catalogCodeRe = regexp.MustCompile(balance.CatalogCodePattern)

// ProduceMutations 一般農產品可以設定的突變（珍稀變種另有獨立物品）。
var ProduceMutations = []string{"none", "giant", "golden", "fast"}

// ---------- 輸入驗證 ----------

// adminInt Python admin_service._int：訊息中的數字不加千分位。
func adminInt(value any, label string, low, high int) (int, error) {
	n, ok := featurecfg.AsInt(value)
	if !ok || n < low || n > high {
		return 0, gameerr.Validationf("%s必須是 %d–%d 之間的整數。", label, low, high)
	}
	return n, nil
}

func adminName(value any, label string) (string, error) {
	s, _ := value.(string)
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > balance.CatalogNameMaxLen {
		return "", gameerr.Validationf("%s長度必須為 1–%d 字。", label, balance.CatalogNameMaxLen)
	}
	return s, nil
}

func adminCode(value any, label string) (string, error) {
	s, ok := value.(string)
	if !ok || !catalogCodeRe.MatchString(s) || strings.HasPrefix(s, "seed_") {
		return "", gameerr.Validationf("%s只能使用小寫英文、數字與底線，2–32 字，以英文字母開頭，且不可用 seed_ 開頭。", label)
	}
	return s, nil
}

func adminText(value any, label string, maxLen int, required bool) (string, error) {
	s, _ := value.(string)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLen || (required && s == "") {
		req := ""
		if required {
			req = "必填，"
		}
		return "", gameerr.Validationf("%s%s長度不可超過 %d 字。", label, req, maxLen)
	}
	return s, nil
}

// getOr Python data.get(key, def)：鍵不存在時回傳 def（鍵存在但值為 nil 時回傳 nil）。
func getOr(data map[string]any, key string, def any) any {
	if v, ok := data[key]; ok {
		return v
	}
	return def
}

// limitValues 季節／時段限定。
type limitValues struct {
	Seasons   []string // nil = 不限（NULL）
	HourStart *int
	HourEnd   *int
}

// parseLimits limits 沒送（nil）時回傳 nil（不變更）；送 {} 或 {"seasons": [], ...} 就是取消限制。
func parseLimits(data map[string]any) (*limitValues, error) {
	raw := data["limits"]
	if raw == nil {
		return nil, nil
	}
	m, ok := featurecfg.AsMap(raw)
	if !ok {
		return nil, gameerr.Validationf("季節／時段限定的格式不正確。")
	}
	s, start, end, err := seasons.Validate(m["seasons"], m["hour_start"], m["hour_end"])
	if err != nil {
		return nil, err
	}
	return &limitValues{s, start, end}, nil
}

func (l *limitValues) cols() []db.Col {
	if l == nil {
		return nil
	}
	return []db.Col{db.C("seasons", l.Seasons), db.C("hour_start", l.HourStart), db.C("hour_end", l.HourEnd)}
}

func insertReturning[T any](ctx context.Context, t *db.Tx, table string, cols ...db.Col) (*T, error) {
	sql, args := db.InsertSQL(table, cols, "*")
	return db.QueryStruct[T](ctx, t, sql, args...)
}

func exists(ctx context.Context, t *db.Tx, sql string, args ...any) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, sql+" LIMIT 1", args...)
	return ok, err
}

// ---------- 回傳資料 ----------

// CropView 管理網站的作物資料（Python crop_view）。
type CropView struct {
	ID              int64    `json:"id"`
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	SeedPrice       int      `json:"seed_price"`
	GrowSeconds     int      `json:"grow_seconds"`
	HarvestQuantity int      `json:"harvest_quantity"`
	SellPrice       int      `json:"sell_price"`
	VariantCode     string   `json:"variant_code"`
	VariantName     string   `json:"variant_name"`
	Active          bool     `json:"active"`
	Seasons         []string `json:"seasons"`
	HourStart       *int     `json:"hour_start"`
	HourEnd         *int     `json:"hour_end"`
}

func NewCropView(c models.CropType) CropView {
	return CropView{c.ID, c.Code, c.Name, c.SeedPrice, c.GrowSeconds, c.HarvestQuantity, c.SellPrice, c.VariantCode,
		c.VariantName, c.Active, append([]string{}, c.Seasons...), c.HourStart, c.HourEnd}
}

func cropSnapshot(c *models.CropType) map[string]any {
	return map[string]any{"name": c.Name, "seed_price": c.SeedPrice, "grow_seconds": c.GrowSeconds,
		"harvest_quantity": c.HarvestQuantity, "sell_price": c.SellPrice, "variant_name": c.VariantName,
		"active": c.Active, "seasons": c.Seasons, "hour_start": c.HourStart, "hour_end": c.HourEnd}
}

func animalSnapshot(a *models.AnimalType) map[string]any {
	return map[string]any{"name": a.Name, "price": a.Price, "feed_cost": a.FeedCost, "cycle_seconds": a.CycleSeconds,
		"product_name": a.ProductName, "product_quantity": a.ProductQuantity, "product_price": a.ProductPrice,
		"active": a.Active}
}

func fishSnapshot(f *models.FishType) map[string]any {
	return map[string]any{"name": f.Name, "emoji": f.Emoji, "rarity": f.Rarity, "min_price": f.MinPrice,
		"max_price": f.MaxPrice, "min_weight_g": f.MinWeightG, "max_weight_g": f.MaxWeightG, "difficulty": f.Difficulty,
		"description": f.Description, "sort_order": f.SortOrder, "active": f.Active, "required_baits": f.RequiredBaits,
		"seasons": f.Seasons, "hour_start": f.HourStart, "hour_end": f.HourEnd}
}

func gearSnapshot(g *models.FishingGear) map[string]any {
	return map[string]any{"name": g.Name, "emoji": g.Emoji, "description": g.Description, "price": g.Price,
		"required_level": g.RequiredLevel, "stock": g.Stock, "effects": g.Effects, "sort_order": g.SortOrder,
		"active": g.Active}
}

// InventoryChange SetInventory 的結果。
type InventoryChange struct {
	ItemCode string `json:"item_code"`
	Quantity int    `json:"quantity"`
	Changed  bool   `json:"changed"`
}

// FarmValues SetFarmValues 的結果。
type FarmValues struct {
	Gold    int  `json:"gold"`
	Level   int  `json:"level"`
	Changed bool `json:"changed"`
}

// AdminTransferResult AdminTransferFarm 的結果；LastGroupTransferAt 是 Python isoformat() 的字串。
type AdminTransferResult struct {
	HomeGroupID         int64  `json:"home_group_id"`
	LastGroupTransferAt string `json:"last_group_transfer_at"`
	Changed             bool   `json:"changed"`
	CooldownBypassed    bool   `json:"cooldown_bypassed"`
}

type PlotsAdded struct {
	Added []int `json:"added"`
	Total int   `json:"total"`
}

type PlantedForPlayer struct {
	PlotNumber int    `json:"plot_number"`
	Crop       string `json:"crop"`
	Quantity   int    `json:"quantity"`
	State      string `json:"state"`
}

type BuildingSlot struct {
	Building string `json:"building"`
	Slot     int    `json:"slot"`
}

// ---------- 服務 ----------

type AdminService struct {
	*ProgressService
}

func NewAdminService(d *db.DB, opt Options) *AdminService {
	return &AdminService{NewProgressService(d, opt)}
}

func (a *AdminService) InGroupContext(groupID *int64) *AdminService {
	return &AdminService{a.ProgressService.InGroupContext(groupID)}
}

func (a *AdminService) audit(ctx context.Context, t *db.Tx, actor, action, targetType string, targetID *int64,
	details map[string]any) error {
	return community.AddAdminAction(ctx, t, actor, action, targetType, targetID, details, a.now())
}

// usedNames 作物、變種、動物、產品、商品與道具的名稱（玩家以名稱下指令，名稱不可重複）。
func usedNames(ctx context.Context, t *db.Tx, excludeCrop, excludeAnimal string) (map[string]bool, error) {
	names := map[string]bool{}
	cropList, err := crops.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, c := range cropList {
		if c.Code != excludeCrop {
			names[c.Name], names[c.VariantName] = true, true
		}
	}
	animalList, err := animals.ListAll(ctx, t, false)
	if err != nil {
		return nil, err
	}
	for _, an := range animalList {
		if an.Code != excludeAnimal {
			names[an.Name], names[an.ProductName] = true, true
		}
	}
	for _, g := range balance.Goods {
		names[g.Name] = true
	}
	for _, it := range balance.Items {
		names[it.Name] = true
	}
	return names, nil
}

func checkNames(used map[string]bool, names ...string) error {
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if len(seen) != len(names) {
		return gameerr.Validationf("名稱不可彼此相同。")
	}
	for _, n := range names {
		if used[n] {
			return gameerr.New(gameerr.AlreadyExists, "名稱「%s」已被其他作物、動物、商品或道具使用。", n)
		}
	}
	return nil
}

func checkReserved(ctx context.Context, t *db.Tx, codes ...string) error {
	reserved, err := catalog.ReservedCodes(ctx, t)
	if err != nil {
		return err
	}
	for _, c := range codes {
		if reserved[c] {
			return gameerr.New(gameerr.AlreadyExists, "代碼「%s」已被使用。", c)
		}
	}
	return nil
}

// ---------- 作物種類 ----------

type cropValues struct {
	Name, VariantName                                  string
	SeedPrice, GrowSeconds, HarvestQuantity, SellPrice int
	Limits                                             *limitValues
}

func parseCropValues(data map[string]any) (cropValues, error) {
	var v cropValues
	var err error
	if v.Name, err = adminName(data["name"], "作物名稱"); err != nil {
		return v, err
	}
	if v.VariantName, err = adminName(data["variant_name"], "珍稀變種名稱"); err != nil {
		return v, err
	}
	r := balance.CropSeedPriceRange
	if v.SeedPrice, err = adminInt(data["seed_price"], "種子價格", r.Low, r.High); err != nil {
		return v, err
	}
	r = balance.CropGrowSecondsRange
	if v.GrowSeconds, err = adminInt(data["grow_seconds"], "生長秒數", r.Low, r.High); err != nil {
		return v, err
	}
	r = balance.CropHarvestQuantityRange
	if v.HarvestQuantity, err = adminInt(data["harvest_quantity"], "收成數量", r.Low, r.High); err != nil {
		return v, err
	}
	r = balance.CropSellPriceRange
	if v.SellPrice, err = adminInt(data["sell_price"], "售價", r.Low, r.High); err != nil {
		return v, err
	}
	v.Limits, err = parseLimits(data)
	return v, err
}

func (v cropValues) cols() []db.Col {
	return append([]db.Col{db.C("name", v.Name), db.C("variant_name", v.VariantName), db.C("seed_price", v.SeedPrice),
		db.C("grow_seconds", v.GrowSeconds), db.C("harvest_quantity", v.HarvestQuantity), db.C("sell_price", v.SellPrice)},
		v.Limits.cols()...)
}

func (v cropValues) apply(c *models.CropType) {
	c.Name, c.VariantName, c.SeedPrice, c.GrowSeconds = v.Name, v.VariantName, v.SeedPrice, v.GrowSeconds
	c.HarvestQuantity, c.SellPrice = v.HarvestQuantity, v.SellPrice
	if v.Limits != nil {
		c.Seasons, c.HourStart, c.HourEnd = v.Limits.Seasons, v.Limits.HourStart, v.Limits.HourEnd
	}
}

func (a *AdminService) CreateCrop(ctx context.Context, actor string, data map[string]any) (CropView, error) {
	code, err := adminCode(data["code"], "作物代碼")
	if err != nil {
		return CropView{}, err
	}
	variantCode, err := adminCode(data["variant_code"], "珍稀變種代碼")
	if err != nil {
		return CropView{}, err
	}
	values, err := parseCropValues(data)
	if err != nil {
		return CropView{}, err
	}
	if code == variantCode {
		return CropView{}, gameerr.Validationf("作物代碼與珍稀變種代碼不可相同。")
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (CropView, error) {
		if err := checkReserved(ctx, t, code, variantCode); err != nil {
			return CropView{}, err
		}
		used, err := usedNames(ctx, t, "", "")
		if err != nil {
			return CropView{}, err
		}
		if err := checkNames(used, values.Name, values.VariantName); err != nil {
			return CropView{}, err
		}
		cols := append([]db.Col{db.C("code", code), db.C("variant_code", variantCode), db.C("active", true)}, values.cols()...)
		crop, err := insertReturning[models.CropType](ctx, t, models.TableCropType, cols...)
		if err != nil {
			return CropView{}, err
		}
		view := NewCropView(*crop)
		return view, a.audit(ctx, t, actor, "create_crop", "crop", &crop.ID, toMap(view))
	})
}

func cropForUpdate(ctx context.Context, t *db.Tx, code string) (*models.CropType, error) {
	crop, err := crops.ByCode(ctx, t, code, true)
	if err != nil {
		return nil, err
	}
	if crop == nil {
		return nil, gameerr.NotFoundf("找不到作物「%s」。", code)
	}
	return crop, nil
}

// UpdateCrop 修改名稱、價格、生長時間與收成量；已澆水的作物成熟時間不變，已種下的收成量不變。
func (a *AdminService) UpdateCrop(ctx context.Context, actor, code string, data map[string]any) (CropView, error) {
	values, err := parseCropValues(data)
	if err != nil {
		return CropView{}, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (CropView, error) {
		crop, err := cropForUpdate(ctx, t, code)
		if err != nil {
			return CropView{}, err
		}
		used, err := usedNames(ctx, t, code, "")
		if err != nil {
			return CropView{}, err
		}
		if err := checkNames(used, values.Name, values.VariantName); err != nil {
			return CropView{}, err
		}
		before := cropSnapshot(crop)
		values.apply(crop)
		if _, err := db.UpdateByID(ctx, t, models.TableCropType, crop.ID, values.cols()...); err != nil {
			return CropView{}, err
		}
		return NewCropView(*crop), a.audit(ctx, t, actor, "update_crop", "crop", &crop.ID,
			map[string]any{"crop": code, "before": before, "after": cropSnapshot(crop)})
	})
}

func (a *AdminService) SetCropActive(ctx context.Context, actor, code string, active bool) (CropView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (CropView, error) {
		crop, err := cropForUpdate(ctx, t, code)
		if err != nil {
			return CropView{}, err
		}
		if crop.Active != active {
			crop.Active = active
			if _, err := db.UpdateByID(ctx, t, models.TableCropType, crop.ID, db.C("active", active)); err != nil {
				return CropView{}, err
			}
			action := "disable_crop"
			if active {
				action = "enable_crop"
			}
			if err := a.audit(ctx, t, actor, action, "crop", &crop.ID, map[string]any{"crop": code}); err != nil {
				return CropView{}, err
			}
		}
		return NewCropView(*crop), nil
	})
}

// cropInUse 作物被哪裡使用（"" = 沒有）。
func cropInUse(ctx context.Context, t *db.Tx, crop *models.CropType) (string, error) {
	codes := []string{crop.Code, crop.VariantCode, rules.SeedItemCode(crop.Code)}
	checks := []struct {
		label, sql string
		arg        any
	}{
		{"土地", "SELECT id FROM farm_plots WHERE crop_type_id = $1", crop.ID},
		{"背包", "SELECT id FROM inventory_items WHERE item_code = ANY($1)", codes},
		{"交易紀錄", "SELECT id FROM game_transactions WHERE item_code = ANY($1)", codes},
		{"偷菜紀錄", "SELECT id FROM steal_records WHERE crop_type_id = $1", crop.ID},
		{"限時事件", "SELECT id FROM game_events WHERE crop_type_id = $1", crop.ID},
	}
	for _, c := range checks {
		ok, err := exists(ctx, t, c.sql, c.arg)
		if err != nil || ok {
			return c.label, err
		}
	}
	for _, code := range balance.RecipeOrder {
		recipe := balance.Recipes[code]
		if _, ok := recipe.Inputs[crop.Code]; ok || recipe.Output == crop.Code {
			return "加工配方", nil
		}
	}
	return "", nil
}

func (a *AdminService) DeleteCrop(ctx context.Context, actor, code string) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		crop, err := cropForUpdate(ctx, t, code)
		if err != nil {
			return err
		}
		used, err := cropInUse(ctx, t, crop)
		if err != nil {
			return err
		}
		if used != "" {
			return gameerr.Validationf("%s已有%s使用，不能刪除，請改為停用（保留歷史資料）。", crop.Name, used)
		}
		snapshot := toMap(NewCropView(*crop))
		if _, err := t.Exec(ctx, "DELETE FROM crop_types WHERE id = $1", crop.ID); err != nil {
			return err
		}
		return a.audit(ctx, t, actor, "delete_crop", "crop", &crop.ID, snapshot)
	})
}

// ---------- 動物種類 ----------

type animalValues struct {
	Name, ProductName                                            string
	Price, FeedCost, CycleSeconds, ProductQuantity, ProductPrice int
}

func parseAnimalValues(data map[string]any) (animalValues, error) {
	var v animalValues
	var err error
	if v.Name, err = adminName(data["name"], "動物名稱"); err != nil {
		return v, err
	}
	if v.ProductName, err = adminName(data["product_name"], "產品名稱"); err != nil {
		return v, err
	}
	for _, f := range []struct {
		dst   *int
		key   string
		label string
		r     balance.Range
	}{
		{&v.Price, "price", "購買價格", balance.AnimalPriceRange},
		{&v.FeedCost, "feed_cost", "餵食費用", balance.AnimalFeedRange},
		{&v.CycleSeconds, "cycle_seconds", "生產週期秒數", balance.AnimalCycleRange},
		{&v.ProductQuantity, "product_quantity", "每次產量", balance.AnimalProductQuantityRange},
		{&v.ProductPrice, "product_price", "產品售價", balance.AnimalProductPriceRange},
	} {
		if *f.dst, err = adminInt(data[f.key], f.label, f.r.Low, f.r.High); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (v animalValues) cols() []db.Col {
	return []db.Col{db.C("name", v.Name), db.C("product_name", v.ProductName), db.C("price", v.Price),
		db.C("feed_cost", v.FeedCost), db.C("cycle_seconds", v.CycleSeconds), db.C("product_quantity", v.ProductQuantity),
		db.C("product_price", v.ProductPrice)}
}

func (a *AdminService) CreateAnimalType(ctx context.Context, actor string, data map[string]any) (AnimalTypeView, error) {
	code, err := adminCode(data["code"], "動物代碼")
	if err != nil {
		return AnimalTypeView{}, err
	}
	productCode, err := adminCode(data["product_code"], "產品代碼")
	if err != nil {
		return AnimalTypeView{}, err
	}
	values, err := parseAnimalValues(data)
	if err != nil {
		return AnimalTypeView{}, err
	}
	if code == productCode {
		return AnimalTypeView{}, gameerr.Validationf("動物代碼與產品代碼不可相同。")
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (AnimalTypeView, error) {
		if err := checkReserved(ctx, t, code, productCode); err != nil {
			return AnimalTypeView{}, err
		}
		used, err := usedNames(ctx, t, "", "")
		if err != nil {
			return AnimalTypeView{}, err
		}
		if err := checkNames(used, values.Name, values.ProductName); err != nil {
			return AnimalTypeView{}, err
		}
		cols := append([]db.Col{db.C("code", code), db.C("product_code", productCode), db.C("active", true)}, values.cols()...)
		animal, err := insertReturning[models.AnimalType](ctx, t, models.TableAnimalType, cols...)
		if err != nil {
			return AnimalTypeView{}, err
		}
		view := NewAnimalTypeView(*animal)
		return view, a.audit(ctx, t, actor, "create_animal_type", "animal_type", &animal.ID, toMap(view))
	})
}

func animalForUpdate(ctx context.Context, t *db.Tx, code string) (*models.AnimalType, error) {
	animal, err := animals.Get(ctx, t, code, true)
	if err != nil {
		return nil, err
	}
	if animal == nil {
		return nil, gameerr.NotFoundf("找不到動物「%s」。", code)
	}
	return animal, nil
}

// UpdateAnimalType 修改後立即生效；已在生產中的動物完成時間不變，下次餵食起使用新週期。
func (a *AdminService) UpdateAnimalType(ctx context.Context, actor, code string, data map[string]any) (AnimalTypeView, error) {
	values, err := parseAnimalValues(data)
	if err != nil {
		return AnimalTypeView{}, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (AnimalTypeView, error) {
		animal, err := animalForUpdate(ctx, t, code)
		if err != nil {
			return AnimalTypeView{}, err
		}
		used, err := usedNames(ctx, t, "", code)
		if err != nil {
			return AnimalTypeView{}, err
		}
		if err := checkNames(used, values.Name, values.ProductName); err != nil {
			return AnimalTypeView{}, err
		}
		before := animalSnapshot(animal)
		animal.Name, animal.ProductName, animal.Price, animal.FeedCost = values.Name, values.ProductName, values.Price, values.FeedCost
		animal.CycleSeconds, animal.ProductQuantity, animal.ProductPrice = values.CycleSeconds, values.ProductQuantity, values.ProductPrice
		if _, err := db.UpdateByID(ctx, t, models.TableAnimalType, animal.ID, values.cols()...); err != nil {
			return AnimalTypeView{}, err
		}
		return NewAnimalTypeView(*animal), a.audit(ctx, t, actor, "update_animal_type", "animal_type", &animal.ID,
			map[string]any{"animal": code, "before": before, "after": animalSnapshot(animal)})
	})
}

func (a *AdminService) SetAnimalTypeActive(ctx context.Context, actor, code string, active bool) (AnimalTypeView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (AnimalTypeView, error) {
		animal, err := animalForUpdate(ctx, t, code)
		if err != nil {
			return AnimalTypeView{}, err
		}
		if animal.Active != active {
			animal.Active = active
			if _, err := db.UpdateByID(ctx, t, models.TableAnimalType, animal.ID, db.C("active", active)); err != nil {
				return AnimalTypeView{}, err
			}
			action := "disable_animal_type"
			if active {
				action = "enable_animal_type"
			}
			if err := a.audit(ctx, t, actor, action, "animal_type", &animal.ID, map[string]any{"animal": code}); err != nil {
				return AnimalTypeView{}, err
			}
		}
		return NewAnimalTypeView(*animal), nil
	})
}

func (a *AdminService) DeleteAnimalType(ctx context.Context, actor, code string) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		animal, err := animalForUpdate(ctx, t, code)
		if err != nil {
			return err
		}
		codes := []string{animal.Code, animal.ProductCode}
		checks := []struct {
			label, sql string
			arg        any
		}{
			{"農場動物", "SELECT id FROM farm_animals WHERE animal_code = $1", animal.Code},
			{"背包", "SELECT id FROM inventory_items WHERE item_code = $1", animal.ProductCode},
			{"交易紀錄", "SELECT id FROM game_transactions WHERE item_code = ANY($1)", codes},
			{"群組任務", "SELECT id FROM quests WHERE item_filter = $1", animal.ProductCode},
		}
		for _, c := range checks {
			ok, err := exists(ctx, t, c.sql, c.arg)
			if err != nil {
				return err
			}
			if ok {
				return gameerr.Validationf("%s已有%s使用，不能刪除，請改為停用（保留歷史資料）。", animal.Name, c.label)
			}
		}
		snapshot := toMap(NewAnimalTypeView(*animal))
		if _, err := t.Exec(ctx, "DELETE FROM animal_types WHERE id = $1", animal.ID); err != nil {
			return err
		}
		return a.audit(ctx, t, actor, "delete_animal_type", "animal_type", &animal.ID, snapshot)
	})
}

// ---------- 釣魚：魚種、釣具與設定 ----------

type fishValues struct {
	fishing.FishValues
	Name, Emoji, Description string
	SortOrder                int
	BaitsSet                 bool     // 有送 required_baits（沒送時不變更）
	Baits                    []string // nil = 任何魚餌都可以
	Limits                   *limitValues
}

func parseFishValues(data map[string]any) (fishValues, error) {
	var v fishValues
	var err error
	if v.FishValues, err = fishing.ValidateFish(data); err != nil {
		return v, err
	}
	if v.Name, err = adminName(data["name"], "魚類名稱"); err != nil {
		return v, err
	}
	if v.Emoji, err = adminText(data["emoji"], "圖示", 16, true); err != nil {
		return v, err
	}
	if v.Description, err = adminText(data["description"], "說明", 200, false); err != nil {
		return v, err
	}
	if v.SortOrder, err = adminInt(getOr(data, "sort_order", 0), "排序", 0, 100_000); err != nil {
		return v, err
	}
	// 指定魚餌：沒有送（nil）時不變更；空清單 = 任何魚餌都可以
	if raw := data["required_baits"]; raw != nil {
		list, ok := featurecfg.AsList(raw)
		if !ok || len(list) > 10 {
			return v, gameerr.Validationf("指定魚餌必須是魚餌代碼清單（最多 10 個）。")
		}
		for _, b := range list {
			s, ok := b.(string)
			if !ok {
				return v, gameerr.Validationf("指定魚餌必須是魚餌代碼清單（最多 10 個）。")
			}
			if !slices.Contains(v.Baits, s) {
				v.Baits = append(v.Baits, s)
			}
		}
		v.BaitsSet = true
	}
	v.Limits, err = parseLimits(data)
	return v, err
}

func (v fishValues) cols() []db.Col {
	cols := []db.Col{db.C("rarity", v.Rarity), db.C("min_price", v.MinPrice), db.C("max_price", v.MaxPrice),
		db.C("min_weight_g", v.MinWeightG), db.C("max_weight_g", v.MaxWeightG), db.C("difficulty", v.Difficulty),
		db.C("name", v.Name), db.C("emoji", v.Emoji), db.C("description", v.Description), db.C("sort_order", v.SortOrder)}
	if v.BaitsSet {
		cols = append(cols, db.C("required_baits", v.Baits))
	}
	return append(cols, v.Limits.cols()...)
}

func (v fishValues) apply(f *models.FishType) {
	f.Rarity, f.MinPrice, f.MaxPrice, f.MinWeightG, f.MaxWeightG = v.Rarity, v.MinPrice, v.MaxPrice, v.MinWeightG, v.MaxWeightG
	f.Difficulty, f.Name, f.Emoji, f.Description, f.SortOrder = v.Difficulty, v.Name, v.Emoji, v.Description, v.SortOrder
	if v.BaitsSet {
		f.RequiredBaits = v.Baits
	}
	if v.Limits != nil {
		f.Seasons, f.HourStart, f.HourEnd = v.Limits.Seasons, v.Limits.HourStart, v.Limits.HourEnd
	}
}

func checkBaits(ctx context.Context, t *db.Tx, v fishValues) error {
	for _, code := range v.Baits {
		gear, err := fishrepo.GetGear(ctx, t, code, false)
		if err != nil {
			return err
		}
		if gear == nil || gear.Kind != "bait" {
			return gameerr.Validationf("「%s」不是魚餌代碼。", code)
		}
	}
	return nil
}

func (a *AdminService) CreateFishType(ctx context.Context, actor string, data map[string]any) (FishTypeView, error) {
	code, err := adminCode(data["code"], "魚類代碼")
	if err != nil {
		return FishTypeView{}, err
	}
	values, err := parseFishValues(data)
	if err != nil {
		return FishTypeView{}, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (FishTypeView, error) {
		if err := checkBaits(ctx, t, values); err != nil {
			return FishTypeView{}, err
		}
		old, err := fishrepo.GetFishType(ctx, t, code, false)
		if err != nil {
			return FishTypeView{}, err
		}
		if old != nil {
			return FishTypeView{}, gameerr.New(gameerr.AlreadyExists, "代碼「%s」已被使用。", code)
		}
		names, err := fishrepo.FishTypeNames(ctx, t)
		if err != nil {
			return FishTypeView{}, err
		}
		if names[values.Name] {
			return FishTypeView{}, gameerr.New(gameerr.AlreadyExists, "魚類名稱「%s」已被使用。", values.Name)
		}
		cols := append([]db.Col{db.C("code", code), db.C("active", true)}, values.cols()...)
		fish, err := insertReturning[models.FishType](ctx, t, models.TableFishType, cols...)
		if err != nil {
			return FishTypeView{}, err
		}
		view := NewFishTypeView(*fish)
		return view, a.audit(ctx, t, actor, "create_fish_type", "fish_type", &fish.ID, toMap(view))
	})
}

func fishTypeForUpdate(ctx context.Context, t *db.Tx, code string) (*models.FishType, error) {
	fish, err := fishrepo.GetFishType(ctx, t, code, true)
	if err != nil {
		return nil, err
	}
	if fish == nil {
		return nil, gameerr.NotFoundf("找不到魚類「%s」。", code)
	}
	return fish, nil
}

// UpdateFishType 修改後立即生效；已釣到的魚保留捕獲當下的稀有度與價值，進行中的釣魚也不受影響。
func (a *AdminService) UpdateFishType(ctx context.Context, actor, code string, data map[string]any) (FishTypeView, error) {
	values, err := parseFishValues(data)
	if err != nil {
		return FishTypeView{}, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (FishTypeView, error) {
		if err := checkBaits(ctx, t, values); err != nil {
			return FishTypeView{}, err
		}
		fish, err := fishTypeForUpdate(ctx, t, code)
		if err != nil {
			return FishTypeView{}, err
		}
		if values.Name != fish.Name {
			names, err := fishrepo.FishTypeNames(ctx, t)
			if err != nil {
				return FishTypeView{}, err
			}
			if names[values.Name] {
				return FishTypeView{}, gameerr.New(gameerr.AlreadyExists, "魚類名稱「%s」已被使用。", values.Name)
			}
		}
		before := fishSnapshot(fish)
		values.apply(fish)
		if _, err := db.UpdateByID(ctx, t, models.TableFishType, fish.ID, values.cols()...); err != nil {
			return FishTypeView{}, err
		}
		return NewFishTypeView(*fish), a.audit(ctx, t, actor, "update_fish_type", "fish_type", &fish.ID,
			map[string]any{"fish": code, "before": before, "after": fishSnapshot(fish)})
	})
}

func (a *AdminService) SetFishTypeActive(ctx context.Context, actor, code string, active bool) (FishTypeView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (FishTypeView, error) {
		fish, err := fishTypeForUpdate(ctx, t, code)
		if err != nil {
			return FishTypeView{}, err
		}
		if fish.Active != active {
			fish.Active = active
			if _, err := db.UpdateByID(ctx, t, models.TableFishType, fish.ID, db.C("active", active)); err != nil {
				return FishTypeView{}, err
			}
			action := "disable_fish_type"
			if active {
				action = "enable_fish_type"
			}
			if err := a.audit(ctx, t, actor, action, "fish_type", &fish.ID, map[string]any{"fish": code}); err != nil {
				return FishTypeView{}, err
			}
		}
		return NewFishTypeView(*fish), nil
	})
}

type gearValues struct {
	Name, Emoji, Description string
	Price, RequiredLevel     int
	Stock                    *int
	Effects                  fishing.Effects
	SortOrder                int
}

func parseGearValues(kind string, data map[string]any) (gearValues, error) {
	var v gearValues
	var err error
	if v.Name, err = adminName(data["name"], "釣具名稱"); err != nil {
		return v, err
	}
	if v.Emoji, err = adminText(data["emoji"], "圖示", 16, true); err != nil {
		return v, err
	}
	if v.Description, err = adminText(data["description"], "說明", 200, false); err != nil {
		return v, err
	}
	if v.Price, err = adminInt(data["price"], "價格", fishing.GearPriceRange[0], fishing.GearPriceRange[1]); err != nil {
		return v, err
	}
	if v.RequiredLevel, err = adminInt(data["required_level"], "需要等級", fishing.GearLevelRange[0], fishing.GearLevelRange[1]); err != nil {
		return v, err
	}
	if stock := data["stock"]; stock != nil {
		n, err := adminInt(stock, "庫存", fishing.GearStockRange[0], fishing.GearStockRange[1])
		if err != nil {
			return v, err
		}
		v.Stock = &n
	}
	effects, _ := featurecfg.AsMap(data["effects"])
	if v.Effects, err = fishing.ValidateGearEffects(kind, effects); err != nil {
		return v, err
	}
	v.SortOrder, err = adminInt(getOr(data, "sort_order", 0), "排序", 0, 100_000)
	return v, err
}

func (v gearValues) cols() []db.Col {
	return []db.Col{db.C("name", v.Name), db.C("emoji", v.Emoji), db.C("description", v.Description), db.C("price", v.Price),
		db.C("required_level", v.RequiredLevel), db.C("stock", v.Stock), db.C("effects", v.Effects),
		db.C("sort_order", v.SortOrder)}
}

func gearNameUsed(ctx context.Context, t *db.Tx, name string) (bool, error) {
	list, err := fishrepo.ListGear(ctx, t, false)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(list, func(g models.FishingGear) bool { return g.Name == name }), nil
}

func (a *AdminService) CreateFishingGear(ctx context.Context, actor string, data map[string]any) (GearView, error) {
	code, err := adminCode(data["code"], "釣具代碼")
	if err != nil {
		return GearView{}, err
	}
	kind, _ := data["kind"].(string)
	if !slices.Contains(fishing.GearKinds, kind) {
		return GearView{}, gameerr.Validationf("釣具類型必須是魚竿、魚線或魚餌。")
	}
	values, err := parseGearValues(kind, data)
	if err != nil {
		return GearView{}, err
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (GearView, error) {
		old, err := fishrepo.GetGear(ctx, t, code, false)
		if err != nil {
			return GearView{}, err
		}
		if old != nil {
			return GearView{}, gameerr.New(gameerr.AlreadyExists, "代碼「%s」已被使用。", code)
		}
		if used, err := gearNameUsed(ctx, t, values.Name); err != nil || used {
			if err == nil {
				err = gameerr.New(gameerr.AlreadyExists, "釣具名稱「%s」已被使用。", values.Name)
			}
			return GearView{}, err
		}
		cols := append([]db.Col{db.C("code", code), db.C("kind", kind), db.C("active", true)}, values.cols()...)
		gear, err := insertReturning[models.FishingGear](ctx, t, models.TableFishingGear, cols...)
		if err != nil {
			return GearView{}, err
		}
		view := NewGearView(*gear, nil, nil)
		return view, a.audit(ctx, t, actor, "create_fishing_gear", "fishing_gear", &gear.ID, toMap(view))
	})
}

func gearForUpdate(ctx context.Context, t *db.Tx, code string) (*models.FishingGear, error) {
	gear, err := fishrepo.GetGear(ctx, t, code, true)
	if err != nil {
		return nil, err
	}
	if gear == nil {
		return nil, gameerr.NotFoundf("找不到釣具「%s」。", code)
	}
	return gear, nil
}

// UpdateFishingGear 修改價格、效果與庫存；效果在下一次拋竿時生效（進行中的釣魚使用拋竿當下的裝備效果）。類型不可修改。
func (a *AdminService) UpdateFishingGear(ctx context.Context, actor, code string, data map[string]any) (GearView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (GearView, error) {
		gear, err := gearForUpdate(ctx, t, code)
		if err != nil {
			return GearView{}, err
		}
		values, err := parseGearValues(gear.Kind, data)
		if err != nil {
			return GearView{}, err
		}
		if values.Name != gear.Name {
			if used, err := gearNameUsed(ctx, t, values.Name); err != nil || used {
				if err == nil {
					err = gameerr.New(gameerr.AlreadyExists, "釣具名稱「%s」已被使用。", values.Name)
				}
				return GearView{}, err
			}
		}
		before := gearSnapshot(gear)
		gear.Name, gear.Emoji, gear.Description, gear.Price = values.Name, values.Emoji, values.Description, values.Price
		gear.RequiredLevel, gear.Stock, gear.Effects, gear.SortOrder = values.RequiredLevel, values.Stock, values.Effects, values.SortOrder
		if _, err := db.UpdateByID(ctx, t, models.TableFishingGear, gear.ID, values.cols()...); err != nil {
			return GearView{}, err
		}
		return NewGearView(*gear, nil, nil), a.audit(ctx, t, actor, "update_fishing_gear", "fishing_gear", &gear.ID,
			map[string]any{"gear": code, "before": before, "after": gearSnapshot(gear)})
	})
}

// SetFishingGearActive 停用後商店不再販售；玩家已持有的仍可裝備使用。
func (a *AdminService) SetFishingGearActive(ctx context.Context, actor, code string, active bool) (GearView, error) {
	return inTx(ctx, a.GameService, func(t *db.Tx) (GearView, error) {
		gear, err := gearForUpdate(ctx, t, code)
		if err != nil {
			return GearView{}, err
		}
		if gear.Active != active {
			gear.Active = active
			if _, err := db.UpdateByID(ctx, t, models.TableFishingGear, gear.ID, db.C("active", active)); err != nil {
				return GearView{}, err
			}
			action := "disable_fishing_gear"
			if active {
				action = "enable_fishing_gear"
			}
			if err := a.audit(ctx, t, actor, action, "fishing_gear", &gear.ID, map[string]any{"gear": code}); err != nil {
				return GearView{}, err
			}
		}
		return NewGearView(*gear, nil, nil), nil
	})
}

// UpdateFishingSettings 新增一個釣魚設定版本（稀有度機率合計必須 100%）；只影響之後開始的釣魚。
func (a *AdminService) UpdateFishingSettings(ctx context.Context, actor string, raw map[string]any) (int, error) {
	settings, err := fishing.ValidateSettings(raw)
	if err != nil {
		return 0, err
	}
	now := a.now()
	return inTx(ctx, a.GameService, func(t *db.Tx) (int, error) {
		oldVersion, old, err := fishrepo.CurrentSettings(ctx, t)
		if err != nil {
			return 0, err
		}
		version, err := fishrepo.AddSettingsVersion(ctx, t, settings, actor, now)
		if err != nil {
			return 0, err
		}
		target := int64(version)
		return version, a.audit(ctx, t, actor, "update_fishing_settings", "fishing_settings", &target,
			map[string]any{"from_version": oldVersion, "before": old, "after": settings})
	})
}

// ---------- 限時事件 ----------

func eventValues(ctx context.Context, t *db.Tx, raw map[string]any) (eventrules.Settings, *int64, error) {
	values, err := eventrules.Validate(raw)
	if err != nil {
		return values, nil, err
	}
	if values.CropCode == "" {
		return values, nil, nil
	}
	crop, err := crops.ByCode(ctx, t, values.CropCode, false)
	if err != nil {
		return values, nil, err
	}
	if crop == nil {
		return values, nil, gameerr.NotFoundf("找不到作物「%s」。", values.CropCode)
	}
	return values, &crop.ID, nil
}

func eventCols(v eventrules.Settings, cropTypeID *int64) []db.Col {
	cols := []db.Col{db.C("name", v.Name), db.C("description", v.Description), db.C("crop_type_id", cropTypeID)}
	for _, k := range eventrules.EffectOrder {
		cols = append(cols, db.C(k, v.Effects[k]))
	}
	return append(cols, db.C("schedule_type", v.ScheduleType), db.C("starts_at", v.StartsAt), db.C("ends_at", v.EndsAt),
		db.C("start_minute", v.StartMinute), db.C("end_minute", v.EndMinute), db.C("weekdays", v.Weekdays),
		db.C("enabled", v.Enabled))
}

func applyEvent(e *models.GameEvent, v eventrules.Settings, cropTypeID *int64) {
	e.Name, e.Description, e.CropTypeID = v.Name, v.Description, cropTypeID
	e.SellPricePct, e.GrowReductionPct = v.Effects["sell_price_pct"], v.Effects["grow_reduction_pct"]
	e.QualityBonusBP, e.MutationBonusBP = v.Effects["quality_bonus_bp"], v.Effects["mutation_bonus_bp"]
	e.HarvestBonusPct, e.FreeWater, e.HarvestLoss = v.Effects["harvest_bonus_pct"], v.Effects["free_water"], v.Effects["harvest_loss"]
	e.ScheduleType, e.StartsAt, e.EndsAt = v.ScheduleType, v.StartsAt, v.EndsAt
	e.StartMinute, e.EndMinute, e.Weekdays, e.Enabled = v.StartMinute, v.EndMinute, v.Weekdays, v.Enabled
}

func eventAudit(e *models.GameEvent) map[string]any {
	rule := eventsrepo.ToRule(e)
	data := map[string]any{"name": e.Name, "description": e.Description, "crop_type_id": e.CropTypeID}
	for _, k := range eventrules.EffectOrder {
		data[k] = rule.EffectValue(k)
	}
	data["schedule"] = eventrules.ScheduleText(rule)
	data["schedule_type"] = e.ScheduleType
	data["enabled"] = e.Enabled
	return data
}

// CreateEvent 建立限時事件；回傳管理網站顯示用的資料（events.View）。
func (a *AdminService) CreateEvent(ctx context.Context, actor string, raw map[string]any) (map[string]any, error) {
	now := a.now()
	return inTx(ctx, a.GameService, func(t *db.Tx) (map[string]any, error) {
		values, cropTypeID, err := eventValues(ctx, t, raw)
		if err != nil {
			return nil, err
		}
		cols := append(eventCols(values, cropTypeID), db.C("created_by", actor), db.C("created_at", now), db.C("updated_at", now))
		event, err := insertReturning[models.GameEvent](ctx, t, models.TableGameEvent, cols...)
		if err != nil {
			return nil, err
		}
		if err := a.audit(ctx, t, actor, "create_event", "event", &event.ID, eventAudit(event)); err != nil {
			return nil, err
		}
		return eventrules.View(eventsrepo.ToRule(event), now), nil
	})
}

func eventForUpdate(ctx context.Context, t *db.Tx, eventID int64) (*models.GameEvent, error) {
	event, err := eventsrepo.Get(ctx, t, eventID, true)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, gameerr.NotFoundf("找不到事件 #%d。", eventID)
	}
	return event, nil
}

// UpdateEvent 修改只影響之後的操作；已播種、已澆水或已販售的結果不會被重算。
func (a *AdminService) UpdateEvent(ctx context.Context, actor string, eventID int64, raw map[string]any) (map[string]any, error) {
	now := a.now()
	return inTx(ctx, a.GameService, func(t *db.Tx) (map[string]any, error) {
		event, err := eventForUpdate(ctx, t, eventID)
		if err != nil {
			return nil, err
		}
		before := eventAudit(event)
		values, cropTypeID, err := eventValues(ctx, t, raw)
		if err != nil {
			return nil, err
		}
		applyEvent(event, values, cropTypeID)
		event.UpdatedAt = now
		cols := append(eventCols(values, cropTypeID), db.C("updated_at", now))
		if _, err := db.UpdateByID(ctx, t, models.TableGameEvent, event.ID, cols...); err != nil {
			return nil, err
		}
		if err := a.audit(ctx, t, actor, "update_event", "event", &event.ID,
			map[string]any{"before": before, "after": eventAudit(event)}); err != nil {
			return nil, err
		}
		return eventrules.View(eventsrepo.ToRule(event), now), nil
	})
}

func (a *AdminService) SetEventEnabled(ctx context.Context, actor string, eventID int64, enabled bool) (map[string]any, error) {
	now := a.now()
	return inTx(ctx, a.GameService, func(t *db.Tx) (map[string]any, error) {
		event, err := eventForUpdate(ctx, t, eventID)
		if err != nil {
			return nil, err
		}
		if event.Enabled != enabled {
			event.Enabled, event.UpdatedAt = enabled, now
			if _, err := db.UpdateByID(ctx, t, models.TableGameEvent, event.ID, db.C("enabled", enabled),
				db.C("updated_at", now)); err != nil {
				return nil, err
			}
			action := "disable_event"
			if enabled {
				action = "enable_event"
			}
			if err := a.audit(ctx, t, actor, action, "event", &event.ID, map[string]any{"name": event.Name}); err != nil {
				return nil, err
			}
		}
		return eventrules.View(eventsrepo.ToRule(event), now), nil
	})
}

// DeleteEvent 交易紀錄中保存的是事件名稱快照，刪除事件不影響歷史紀錄。
func (a *AdminService) DeleteEvent(ctx context.Context, actor string, eventID int64) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		event, err := eventForUpdate(ctx, t, eventID)
		if err != nil {
			return err
		}
		snapshot := eventAudit(event)
		if _, err := t.Exec(ctx, "DELETE FROM game_events WHERE id = $1", event.ID); err != nil {
			return err
		}
		return a.audit(ctx, t, actor, "delete_event", "event", &eventID, snapshot)
	})
}

// ---------- 玩家：共用 ----------

// adjust 寫一筆 admin_adjust 交易紀錄（玩家看得到）與管理操作紀錄。
func (a *AdminService) adjust(ctx context.Context, t *db.Tx, farm *models.Farm, actor, action, description string,
	details map[string]any, r rec) error {
	if err := a.record(ctx, t, farm, rules.EvAdminAdjust, truncRunes("管理員調整："+description, 200), r); err != nil {
		return err
	}
	all := map[string]any{"farm_id": farm.ID}
	for k, v := range details {
		all[k] = v
	}
	return a.audit(ctx, t, actor, action, "player", &farm.PlayerID, all)
}

// ---------- 玩家：背包 ----------

// SetInventory 把某一筆庫存（物品＋品質＋突變）設為指定數量；0 表示刪除。增加時不可超過倉庫容量。
func (a *AdminService) SetInventory(ctx context.Context, actor string, playerID int64, itemCode, quality, mutation string,
	quantity int) (InventoryChange, error) {
	if quantity < 0 || quantity > balance.AdminMaxItemQuantity {
		return InventoryChange{}, gameerr.Validationf("數量必須是 0–%d 之間的整數。", balance.AdminMaxItemQuantity)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (InventoryChange, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return InventoryChange{}, err
		}
		known, err := catalog.ListItems(ctx, t)
		if err != nil {
			return InventoryChange{}, err
		}
		entry, ok := known.Map[itemCode]
		if !ok {
			return InventoryChange{}, gameerr.NotFoundf("沒有「%s」這種物品。", itemCode)
		}
		_, okQ := balance.Qualities[quality]
		_, okM := balance.Mutations[mutation]
		if !okQ || !okM {
			return InventoryChange{}, gameerr.Validationf("品質或突變不正確。")
		}
		switch {
		case entry.Kind == catalog.ProduceKind:
			if !slices.Contains(ProduceMutations, mutation) {
				return InventoryChange{}, gameerr.Validationf("一般農產品的突變只能是 無／巨型／黃金／快速生長；珍稀變種請選變種物品。")
			}
		case entry.Kind == catalog.Variant:
			if mutation != "variant" {
				return InventoryChange{}, gameerr.Validationf("珍稀變種物品的突變固定為「珍稀變種」。")
			}
		case quality != "normal" || mutation != "none":
			return InventoryChange{}, gameerr.Validationf("%s沒有品質或突變。", entry.Name)
		}
		before, err := inventory.GetQuantity(ctx, t, farm.ID, itemCode, quality, mutation)
		if err != nil {
			return InventoryChange{}, err
		}
		delta := quantity - before
		if delta == 0 {
			return InventoryChange{ItemCode: itemCode, Quantity: quantity}, nil
		}
		if delta > 0 {
			if inventory.IsMaterial(itemCode) {
				if err := a.ensureMaterialSpace(ctx, t, farm, delta); err != nil {
					return InventoryChange{}, err
				}
			} else {
				used, err := inventory.TotalQuantity(ctx, t, farm.ID)
				if err != nil {
					return InventoryChange{}, err
				}
				if used+delta > farm.WarehouseCapacity {
					return InventoryChange{}, gameerr.New(gameerr.WarehouseFull, "倉庫空間不足：目前 %d/%d，無法再放入 %d 個。",
						used, farm.WarehouseCapacity, delta)
				}
			}
			if err := inventory.Add(ctx, t, farm.ID, itemCode, delta, quality, mutation); err != nil {
				return InventoryChange{}, err
			}
		} else {
			if _, err := inventory.Remove(ctx, t, farm.ID, itemCode, -delta, quality, mutation); err != nil {
				return InventoryChange{}, err
			}
			if quantity == 0 {
				if _, err := t.Exec(ctx, "DELETE FROM inventory_items WHERE farm_id = $1 AND item_code = $2 AND quality = $3 "+
					"AND mutation = $4", farm.ID, itemCode, quality, mutation); err != nil {
					return InventoryChange{}, err
				}
			}
		}
		label := entry.Name + GeneticsLabel(quality, mutation)
		err = a.adjust(ctx, t, farm, actor, "set_inventory", fmt.Sprintf("背包 %s %d → %d", label, before, quantity),
			map[string]any{"item_code": itemCode, "quality": quality, "mutation": mutation, "before": before, "after": quantity},
			rec{ItemCode: itemCode, Quantity: max(delta, -delta), Quality: quality, Mutation: mutation})
		return InventoryChange{ItemCode: itemCode, Quantity: quantity, Changed: true}, err
	})
}

// ---------- 玩家：金幣與農場等級 ----------

// SetFarmValues 直接設定金幣與農場等級（不增減土地；nil = 不修改）。金幣差額記入交易紀錄，維持「gold_delta 加總 = 全服金幣」。
func (a *AdminService) SetFarmValues(ctx context.Context, actor string, playerID int64, gold, level *int) (FarmValues, error) {
	if gold == nil && level == nil {
		return FarmValues{}, gameerr.Validationf("請至少修改金幣或農場等級其中一項。")
	}
	if gold != nil && (*gold < 0 || *gold > balance.AdminMaxGold) {
		return FarmValues{}, gameerr.Validationf("金幣必須是 0–%d 之間的整數。", balance.AdminMaxGold)
	}
	if level != nil && (*level < 1 || *level > balance.AdminMaxLevel) {
		return FarmValues{}, gameerr.Validationf("農場等級必須是 1–%d 之間的整數。", balance.AdminMaxLevel)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (FarmValues, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return FarmValues{}, err
		}
		before := map[string]any{"gold": farm.Gold, "level": farm.Level}
		var parts []string
		var cols []db.Col
		delta := 0
		if gold != nil && *gold != farm.Gold {
			delta = *gold - farm.Gold
			parts = append(parts, fmt.Sprintf("金幣 %d → %d", farm.Gold, *gold))
			farm.Gold = *gold
			cols = append(cols, db.C("gold", farm.Gold))
		}
		if level != nil && *level != farm.Level {
			parts = append(parts, fmt.Sprintf("農場等級 %d → %d", farm.Level, *level))
			farm.Level = *level
			cols = append(cols, db.C("level", farm.Level))
		}
		if len(parts) == 0 {
			return FarmValues{Gold: farm.Gold, Level: farm.Level}, nil
		}
		if err := a.saveFarm(ctx, t, farm, cols...); err != nil {
			return FarmValues{}, err
		}
		after := map[string]any{"gold": farm.Gold, "level": farm.Level}
		err = a.adjust(ctx, t, farm, actor, "set_farm_values", strings.Join(parts, "、"),
			map[string]any{"before": before, "after": after}, rec{GoldDelta: delta})
		return FarmValues{Gold: farm.Gold, Level: farm.Level, Changed: true}, err
	})
}

// ---------- 玩家：轉移農場所屬群組 ----------

// AdminTransferFarm 管理員轉移農場所屬群組：必填原因並寫入 admin_actions；可選擇略過冷卻（同樣留下紀錄）。
//
// 轉移後會更新 last_group_transfer_at（玩家的下一次轉移重新計算 7 天冷卻），並確保玩家是目標群組成員。
func (a *AdminService) AdminTransferFarm(ctx context.Context, actor string, playerID, groupID int64, reason string,
	skipCooldown bool) (AdminTransferResult, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > balance.GroupTransferReasonMaxLen {
		return AdminTransferResult{}, gameerr.Validationf("轉移原因必填，長度 1–%d 字。", balance.GroupTransferReasonMaxLen)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (AdminTransferResult, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return AdminTransferResult{}, err
		}
		if farm.HomeGroupID != nil && *farm.HomeGroupID == groupID {
			return AdminTransferResult{}, gameerr.Validationf("農場已經屬於這個群組。")
		}
		group, err := groups.Get(ctx, t, groupID)
		if err != nil {
			return AdminTransferResult{}, err
		}
		if group == nil {
			return AdminTransferResult{}, gameerr.NotFoundf("找不到群組 #%d。", groupID)
		}
		view, err := a.transferView(ctx, t, farm, &groupID)
		if err != nil {
			return AdminTransferResult{}, err
		}
		if !view.CanTransfer && !skipCooldown {
			return AdminTransferResult{}, cooldownError(view)
		}
		if skipCooldown {
			// 只在本交易內允許資料庫觸發器略過冷卻檢查；交易結束後自動失效
			if _, err := t.Exec(ctx, "SET LOCAL farmgm.admin_group_transfer = 'on'"); err != nil {
				return AdminTransferResult{}, err
			}
		}
		if _, err := groups.AddMember(ctx, t, groupID, playerID); err != nil {
			return AdminTransferResult{}, err
		}
		var lastBefore any
		if farm.LastGroupTransferAt != nil {
			lastBefore = isoTime(*farm.LastGroupTransferAt)
		}
		before := map[string]any{"home_group_id": farm.HomeGroupID, "last_group_transfer_at": lastBefore}
		now := a.now()
		bypass := ""
		if skipCooldown && !view.CanTransfer {
			bypass = "（略過冷卻）"
		}
		homeName := "未設定"
		if view.HomeGroupName != nil && *view.HomeGroupName != "" {
			homeName = *view.HomeGroupName
		}
		if _, err := a.applyTransfer(ctx, t, farm, group, now,
			fmt.Sprintf("管理員轉移所屬群組：「%s」→「%s」%s；原因：%s", homeName, group.Name, bypass, reason)); err != nil {
			return AdminTransferResult{}, err
		}
		after := map[string]any{"home_group_id": group.ID, "last_group_transfer_at": isoTime(now)}
		if err := a.audit(ctx, t, actor, "transfer_farm_group", "player", &playerID, map[string]any{
			"farm_id": farm.ID, "before": before, "after": after, "reason": reason,
			"skip_cooldown": skipCooldown, "cooldown_bypassed": bypass != "",
		}); err != nil {
			return AdminTransferResult{}, err
		}
		return AdminTransferResult{HomeGroupID: group.ID, LastGroupTransferAt: isoTime(now), Changed: true,
			CooldownBypassed: bypass != ""}, nil
	})
}

// ---------- 玩家：土地與作物 ----------

func (a *AdminService) AddPlots(ctx context.Context, actor string, playerID int64, count int) (PlotsAdded, error) {
	if count < 1 || count > balance.AdminMaxPlots {
		return PlotsAdded{}, gameerr.Validationf("新增土地數量必須是 1–%d 之間的整數。", balance.AdminMaxPlots)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (PlotsAdded, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return PlotsAdded{}, err
		}
		total, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return PlotsAdded{}, err
		}
		if total+count > balance.AdminMaxPlots {
			return PlotsAdded{}, gameerr.Validationf("每座農場最多 %d 格土地（目前 %d 格）。", balance.AdminMaxPlots, total)
		}
		numbers, err := farms.AddPlots(ctx, t, farm.ID, count)
		if err != nil {
			return PlotsAdded{}, err
		}
		err = a.adjust(ctx, t, farm, actor, "add_plots", fmt.Sprintf("新增土地第 %d–%d 格", numbers[0], numbers[len(numbers)-1]),
			map[string]any{"plots": numbers}, rec{Quantity: count})
		return PlotsAdded{Added: numbers, Total: total + count}, err
	})
}

func plotForAdmin(ctx context.Context, t *db.Tx, farm *models.Farm, plotNumber int) (*models.FarmPlot, error) {
	plot, err := farms.GetPlot(ctx, t, farm.ID, plotNumber, true)
	if err != nil {
		return nil, err
	}
	if plot == nil {
		return nil, gameerr.NotFoundf("沒有第 %d 格土地。", plotNumber)
	}
	return plot, nil
}

func (a *AdminService) DeletePlot(ctx context.Context, actor string, playerID int64, plotNumber int) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		plot, err := plotForAdmin(ctx, t, farm, plotNumber)
		if err != nil {
			return err
		}
		if plot.CropTypeID != nil {
			return gameerr.Validationf("第 %d 格還有作物，請先清除作物再刪除土地。", plotNumber)
		}
		total, err := farms.CountPlots(ctx, t, farm.ID)
		if err != nil {
			return err
		}
		if total <= 1 {
			return gameerr.Validationf("農場至少要保留 1 格土地。")
		}
		// 偷菜紀錄的 plot_id 會設為 NULL（外鍵 ON DELETE SET NULL），保留歷史
		if _, err := t.Exec(ctx, "DELETE FROM farm_plots WHERE id = $1", plot.ID); err != nil {
			return err
		}
		return a.adjust(ctx, t, farm, actor, "delete_plot", fmt.Sprintf("刪除第 %d 格土地", plotNumber),
			map[string]any{"plot": plotNumber}, rec{})
	})
}

// PlantForPlayer 在空地直接種下指定作物（不扣種子、不抽品質）。readyNow 表示立即成熟可收成。
func (a *AdminService) PlantForPlayer(ctx context.Context, actor string, playerID int64, plotNumber int, cropCode,
	quality, mutation string, watered, readyNow bool) (PlantedForPlayer, error) {
	_, okQ := balance.Qualities[quality]
	_, okM := balance.Mutations[mutation]
	if !okQ || !okM {
		return PlantedForPlayer{}, gameerr.Validationf("品質或突變不正確。")
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (PlantedForPlayer, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return PlantedForPlayer{}, err
		}
		crop, err := crops.ByCode(ctx, t, cropCode, false)
		if err != nil {
			return PlantedForPlayer{}, err
		}
		if crop == nil {
			return PlantedForPlayer{}, gameerr.NotFoundf("找不到作物「%s」。", cropCode)
		}
		if !crop.Active {
			return PlantedForPlayer{}, gameerr.Validationf("%s已停用，不能種植。", crop.Name)
		}
		plot, err := plotForAdmin(ctx, t, farm, plotNumber)
		if err != nil {
			return PlantedForPlayer{}, err
		}
		if plot.CropTypeID != nil {
			return PlantedForPlayer{}, gameerr.Validationf("第 %d 格已經有作物，請先清除。", plotNumber)
		}
		now := a.now()
		settings, err := a.geneticsSettings(ctx, t)
		if err != nil {
			return PlantedForPlayer{}, err
		}
		quantity := genetics.HarvestQuantity(crop.HarvestQuantity, mutation, settings, 0)
		var state string
		if readyNow {
			// 種植與澆水時間設在 1 秒前、成熟時間為現在：狀態立即為「可收成」
			start := now.Add(-time.Second)
			if err := farms.Plant(ctx, t, plot, crop.ID, quantity, start, quality, mutation); err != nil {
				return PlantedForPlayer{}, err
			}
			if err := farms.Water(ctx, t, plot, start, now); err != nil {
				return PlantedForPlayer{}, err
			}
			state = "可收成"
		} else {
			if err := farms.Plant(ctx, t, plot, crop.ID, quantity, now, quality, mutation); err != nil {
				return PlantedForPlayer{}, err
			}
			if watered {
				seconds := genetics.GrowSeconds(crop.GrowSeconds, mutation, false, settings, 0)
				if err := farms.Water(ctx, t, plot, now, rules.ReadyTime(now, seconds)); err != nil {
					return PlantedForPlayer{}, err
				}
				state = fmt.Sprintf("已澆水，%s後成熟", rules.FormatDuration(seconds))
			} else {
				state = "等待澆水"
			}
		}
		label := genetics.ProduceName(crop.Name, crop.VariantName, mutation) + GeneticsLabel(quality, mutation)
		err = a.adjust(ctx, t, farm, actor, "plant_for_player", fmt.Sprintf("在第 %d 格種下%s（%s）", plotNumber, label, state),
			map[string]any{"plot": plotNumber, "crop": crop.Code, "quality": quality, "mutation": mutation,
				"watered": watered, "ready_now": readyNow},
			rec{ItemCode: crop.Code, Quantity: quantity, Quality: quality, Mutation: mutation})
		return PlantedForPlayer{PlotNumber: plotNumber, Crop: crop.Code, Quantity: quantity, State: state}, err
	})
}

func (a *AdminService) ClearPlotForPlayer(ctx context.Context, actor string, playerID int64, plotNumber int) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		plot, err := plotForAdmin(ctx, t, farm, plotNumber)
		if err != nil {
			return err
		}
		if plot.CropTypeID == nil {
			return gameerr.Validationf("第 %d 格沒有作物。", plotNumber)
		}
		crop, err := crops.Get(ctx, t, *plot.CropTypeID)
		if err != nil {
			return err
		}
		before := map[string]any{"crop": crop.Code, "quality": plot.Quality, "mutation": plot.Mutation,
			"remaining_quantity": plot.RemainingQuantity}
		if err := farms.Clear(ctx, t, plot); err != nil {
			return err
		}
		return a.adjust(ctx, t, farm, actor, "clear_plot", fmt.Sprintf("清除第 %d 格的%s", plotNumber, crop.Name),
			map[string]any{"plot": plotNumber, "before": before}, rec{ItemCode: crop.Code})
	})
}

// ---------- 玩家：建築與動物 ----------

func (a *AdminService) AddBuilding(ctx context.Context, actor string, playerID int64, buildingCode string) (BuildingSlot, error) {
	b, ok := balance.Buildings[buildingCode]
	if !ok {
		return BuildingSlot{}, gameerr.NotFoundf("沒有「%s」這種設施。", buildingCode)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) (BuildingSlot, error) {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return BuildingSlot{}, err
		}
		list, err := ranch.ListBuildings(ctx, t, farm.ID)
		if err != nil {
			return BuildingSlot{}, err
		}
		owned := map[int]bool{}
		for _, x := range list {
			if x.BuildingCode == buildingCode {
				owned[x.Slot] = true
			}
		}
		if len(owned) >= b.MaxPerFarm {
			return BuildingSlot{}, gameerr.New(gameerr.AlreadyExists, "%s每座農場最多 %d 座。", b.Name, b.MaxPerFarm)
		}
		slot := 1
		for owned[slot] {
			slot++
		}
		if _, err := ranch.AddBuilding(ctx, t, farm.ID, buildingCode, slot, a.now()); err != nil {
			return BuildingSlot{}, err
		}
		err = a.adjust(ctx, t, farm, actor, "add_building", fmt.Sprintf("新增%s（第 %d 座）", b.Name, slot),
			map[string]any{"building": buildingCode, "slot": slot}, rec{ItemCode: buildingCode, Quantity: 1})
		return BuildingSlot{Building: buildingCode, Slot: slot}, err
	})
}

func (a *AdminService) DeleteBuilding(ctx context.Context, actor string, playerID int64, buildingCode string, slot int) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		building, err := db.QueryStruct[models.FarmBuilding](ctx, t,
			"SELECT * FROM farm_buildings WHERE farm_id = $1 AND building_code = $2 AND slot = $3 LIMIT 1",
			farm.ID, buildingCode, slot)
		if err != nil {
			return err
		}
		if building == nil {
			return gameerr.NotFoundf("找不到這座設施。")
		}
		name := balance.Buildings[buildingCode].Name
		count, err := ranch.CountBuildings(ctx, t, farm.ID, buildingCode)
		if err != nil {
			return err
		}
		remaining := count - 1
		if buildingCode == "barn" {
			n, err := ranch.CountAnimals(ctx, t, farm.ID)
			if err != nil {
				return err
			}
			if n > remaining*balance.BarnSlots {
				return gameerr.Validationf("刪除後養殖容量只剩 %d，但目前有 %d 隻動物，請先刪除動物。", remaining*balance.BarnSlots, n)
			}
		} else if remaining == 0 {
			pending, err := exists(ctx, t, "SELECT id FROM craft_orders WHERE farm_id = $1 AND building_code = $2 "+
				"AND status = 'in_progress'", farm.ID, buildingCode)
			if err != nil {
				return err
			}
			if pending {
				return gameerr.Validationf("%s還有未領取的加工訂單，請等玩家領取後再刪除。", name)
			}
		}
		if _, err := t.Exec(ctx, "DELETE FROM farm_buildings WHERE id = $1", building.ID); err != nil {
			return err
		}
		return a.adjust(ctx, t, farm, actor, "delete_building", fmt.Sprintf("刪除%s（第 %d 座）", name, slot),
			map[string]any{"building": buildingCode, "slot": slot}, rec{ItemCode: buildingCode})
	})
}

// AddAnimalsForPlayer 回傳新增的動物編號。
func (a *AdminService) AddAnimalsForPlayer(ctx context.Context, actor string, playerID int64, animalCode string,
	quantity int) ([]int, error) {
	if quantity < 1 || quantity > balance.AnimalMaxBuy {
		return nil, gameerr.Validationf("數量必須是 1–%d 之間的整數。", balance.AnimalMaxBuy)
	}
	return inTx(ctx, a.GameService, func(t *db.Tx) ([]int, error) {
		animal, err := animals.Get(ctx, t, animalCode, false)
		if err != nil {
			return nil, err
		}
		if animal == nil {
			return nil, gameerr.NotFoundf("找不到動物「%s」。", animalCode)
		}
		if !animal.Active {
			return nil, gameerr.Validationf("%s已停用，不能新增。", animal.Name)
		}
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		barns, err := ranch.CountBuildings(ctx, t, farm.ID, "barn")
		if err != nil {
			return nil, err
		}
		capacity := barns * balance.BarnSlots
		used, err := ranch.CountAnimals(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		if used+quantity > capacity {
			return nil, gameerr.Validationf("養殖容量不足：目前 %d/%d，請先新增畜舍。", used, capacity)
		}
		numbers, err := ranch.AddAnimals(ctx, t, farm.ID, animal.Code, quantity, a.now())
		if err != nil {
			return nil, err
		}
		err = a.adjust(ctx, t, farm, actor, "add_animals",
			fmt.Sprintf("新增 %d 隻%s（編號 %d–%d）", quantity, animal.Name, numbers[0], numbers[len(numbers)-1]),
			map[string]any{"animal": animal.Code, "numbers": numbers}, rec{ItemCode: animal.Code, Quantity: quantity})
		return numbers, err
	})
}

// DeleteAnimalForPlayer 刪除動物；尚未收集的產品會一併消失。
func (a *AdminService) DeleteAnimalForPlayer(ctx context.Context, actor string, playerID int64, animalNo int) error {
	return a.transaction(ctx, func(t *db.Tx) error {
		farm, err := a.lockedFarm(ctx, t, playerID)
		if err != nil {
			return err
		}
		animal, err := ranch.GetAnimal(ctx, t, farm.ID, animalNo, true)
		if err != nil {
			return err
		}
		if animal == nil {
			return gameerr.NotFoundf("沒有編號 %d 的動物。", animalNo)
		}
		animalType, err := animals.Get(ctx, t, animal.AnimalCode, false)
		if err != nil {
			return err
		}
		name := animal.AnimalCode
		if animalType != nil {
			name = animalType.Name
		}
		if _, err := t.Exec(ctx, "DELETE FROM farm_animals WHERE id = $1", animal.ID); err != nil {
			return err
		}
		return a.adjust(ctx, t, farm, actor, "delete_animal", fmt.Sprintf("刪除%s#%d", name, animalNo),
			map[string]any{"animal": animal.AnimalCode, "animal_no": animalNo}, rec{ItemCode: animal.AnimalCode, Quantity: 1})
	})
}
