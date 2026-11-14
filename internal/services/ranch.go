package services

// 動物養殖、建築、食品加工與寵物（對應 Python app/services/ranch_service.py）。
//
// 所有會改變資產的操作都先鎖定農場資料列，並在單一資料庫交易中完成：
// 扣款與動物狀態、原料扣除與訂單建立、成品發放與訂單狀態都一起提交或一起回滾。

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/genetics"
	"farmgm/internal/game/rules"
	"farmgm/internal/numfmt"
	"farmgm/internal/repositories/animals"
	"farmgm/internal/repositories/catalog"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/pets"
	"farmgm/internal/repositories/ranch"

	petrules "farmgm/internal/game/pets"
)

// RanchService = GameService + 牧場、加工、寵物（Python 的繼承以嵌入表示）。
type RanchService struct {
	*GameService
}

func NewRanchService(d *db.DB, opt Options) *RanchService {
	return &RanchService{NewGameService(d, opt)}
}

// InGroupContext 見 GameService.InGroupContext。
func (r *RanchService) InGroupContext(groupID *int64) *RanchService {
	return &RanchService{r.GameService.InGroupContext(groupID)}
}

// addOrder 建立加工訂單（測試可替換以模擬失敗）。
var addOrder = ranch.AddOrder

// findCode Python _find：代碼（不分大小寫）、名稱或別名。
func findCode(order []string, name func(string) string, aliases func(string) []string, key string) string {
	key = strings.TrimSpace(key)
	lower := strings.ToLower(key)
	for _, code := range order {
		a := aliases(code)
		if lower == code || key == name(code) || slices.Contains(a, key) || slices.Contains(a, lower) {
			return code
		}
	}
	return ""
}

var cropNames = func() map[string]string {
	m := map[string]string{}
	for _, c := range balance.InitialCrops {
		m[c.Code] = c.Name
	}
	return m
}()

// ItemName 物品代碼的顯示名稱；names 為資料庫目錄（catalog.ItemNames），nil 時只用程式內建名稱。
func ItemName(code string, names map[string]string) string {
	if n, ok := names[code]; ok {
		return n
	}
	if g, ok := balance.Goods[code]; ok {
		return g.Name
	}
	if it, ok := balance.Items[code]; ok {
		return it.Name
	}
	if m, ok := exploration.Materials[code]; ok {
		return m.Name
	}
	if n, ok := cropNames[code]; ok {
		return n
	}
	return code
}

// FacilityName 加工設施名稱：建築（磨坊等）或機器（稀有作物培育器）。
func FacilityName(code string) string {
	if b, ok := balance.Buildings[code]; ok {
		return b.Name
	}
	if m, ok := exploration.Machines[code]; ok {
		return m.Name
	}
	return code
}

func RecipeName(code string) string {
	if r, ok := balance.Recipes[code]; ok {
		return r.Name
	}
	if r, ok := exploration.IncubatorRecipes[code]; ok {
		return r.Name
	}
	return code
}

// ---------- 回傳資料（Python 以 dict 回傳） ----------

type AnimalView struct {
	AnimalNo         int        `json:"animal_no"`
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	Active           bool       `json:"active"`
	State            string     `json:"state"`
	StateLabel       string     `json:"state_label"`
	FedAt            *time.Time `json:"fed_at"`
	ReadyAt          *time.Time `json:"ready_at"`
	RemainingSeconds int        `json:"remaining_seconds"`
	FeedCost         int        `json:"feed_cost"`
	CycleSeconds     int        `json:"cycle_seconds"`
	ProductCode      string     `json:"product_code"`
	ProductName      string     `json:"product_name"`
	ProductQuantity  int        `json:"product_quantity"`
	CyclesCompleted  int        `json:"cycles_completed"`
}

// NewAnimalView at 為該動物的種類（呼叫端以 animals.ByCode 一次取得）。
func NewAnimalView(a models.FarmAnimal, now time.Time, at models.AnimalType) AnimalView {
	state := rules.AnimalStateAt(a.FedAt, a.ReadyAt, now)
	v := AnimalView{AnimalNo: a.AnimalNo, Code: a.AnimalCode, Name: at.Name, Active: at.Active, State: string(state),
		StateLabel: rules.AnimalStateLabels[state], FedAt: a.FedAt, ReadyAt: a.ReadyAt, FeedCost: at.FeedCost,
		CycleSeconds: at.CycleSeconds, ProductCode: at.ProductCode, ProductName: at.ProductName,
		ProductQuantity: at.ProductQuantity, CyclesCompleted: a.CyclesCompleted}
	if state == rules.AnimalProducing {
		v.RemainingSeconds = rules.RemainingSeconds(a.ReadyAt, now)
	}
	return v
}

type AnimalTypeView struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Price           int    `json:"price"`
	FeedCost        int    `json:"feed_cost"`
	CycleSeconds    int    `json:"cycle_seconds"`
	ProductCode     string `json:"product_code"`
	ProductName     string `json:"product_name"`
	ProductQuantity int    `json:"product_quantity"`
	ProductPrice    int    `json:"product_price"`
	Active          bool   `json:"active"`
}

func NewAnimalTypeView(a models.AnimalType) AnimalTypeView {
	return AnimalTypeView{a.Code, a.Name, a.Price, a.FeedCost, a.CycleSeconds, a.ProductCode, a.ProductName,
		a.ProductQuantity, a.ProductPrice, a.Active}
}

// OrderView 加工訂單。Inputs 為「名稱 → 數量」，InputCodes 為「代碼 → 數量」（Go map 沒有順序）。
type OrderView struct {
	OrderID          int64          `json:"order_id"`
	RecipeCode       string         `json:"recipe_code"`
	RecipeName       string         `json:"recipe_name"`
	BuildingCode     string         `json:"building_code"`
	BuildingName     string         `json:"building_name"`
	Quantity         int            `json:"quantity"`
	Inputs           map[string]int `json:"inputs"`
	InputCodes       map[string]int `json:"input_codes"`
	OutputCode       string         `json:"output_code"`
	OutputName       string         `json:"output_name"`
	OutputQuantity   int            `json:"output_quantity"`
	Status           string         `json:"status"`
	State            string         `json:"state"`
	StateLabel       string         `json:"state_label"`
	StartedAt        time.Time      `json:"started_at"`
	ReadyAt          time.Time      `json:"ready_at"`
	CollectedAt      *time.Time     `json:"collected_at"`
	RemainingSeconds int            `json:"remaining_seconds"`
}

func NewOrderView(o models.CraftOrder, now time.Time, names map[string]string) OrderView {
	state := rules.CraftStateAt(o.Status, o.ReadyAt, now)
	inputs, codes := map[string]int{}, map[string]int{}
	for k, v := range o.Inputs {
		inputs[ItemName(k, names)] = v
		codes[k] = v
	}
	v := OrderView{OrderID: o.ID, RecipeCode: o.RecipeCode, RecipeName: RecipeName(o.RecipeCode),
		BuildingCode: o.BuildingCode, BuildingName: FacilityName(o.BuildingCode), Quantity: o.Quantity,
		Inputs: inputs, InputCodes: codes, OutputCode: o.OutputCode, OutputName: ItemName(o.OutputCode, names),
		OutputQuantity: o.OutputQuantity, Status: o.Status, State: string(state), StateLabel: rules.CraftStateLabels[state],
		StartedAt: o.StartedAt, ReadyAt: o.ReadyAt, CollectedAt: o.CollectedAt}
	if state == rules.CraftInProgress {
		ready := o.ReadyAt
		v.RemainingSeconds = rules.RemainingSeconds(&ready, now)
	}
	return v
}

type WorkshopStatus struct {
	Counts   map[string]int        // 建築代碼 → 座數
	Busy     map[string]*OrderView // 建築代碼 → 加工中的訂單（閒置為 nil）
	Queued   map[string]int        // 建築代碼 → 排在加工中訂單之後的張數
	Capacity int
	Animals  int
	Gold     int
}

type AnimalStatus struct {
	Capacity    int
	Used        int
	Counts      map[string]int // hungry／producing／ready → 數量
	FeedCostAll int
	NextReadyAt *time.Time
	Gold        int
}

type RecipeView struct {
	Code           string
	Name           string
	BuildingCode   string
	BuildingName   string
	Built          bool
	Inputs         map[string]int // 名稱 → 數量
	InputOrder     []string       // Inputs 的顯示順序（名稱）
	OutputName     string
	OutputQuantity int
	Seconds        int
	SellPrice      *int
	CanMake        *int // 現有原料最多能做幾份（單張訂單上限內）；沒有指定玩家時為 nil
}

type PetCropOdds struct {
	Code     string
	Name     string
	Quantity int
	Text     string
}

type PetOdds struct {
	Pct     int
	Current bool
	Crops   []PetCropOdds
}

type PetView struct {
	Code      string
	Name      string
	Emoji     string
	Role      string
	Owned     bool
	Level     *int
	MaxLevel  int
	Pct       int
	Effect    string
	NextPrice *int // 已滿級時 nil
	NextPct   int
	Odds      *PetOdds // 只有貓
}

// ---------- 查詢名稱 ----------

func FindBuilding(key string) string {
	return findCode(balance.BuildingOrder, func(c string) string { return balance.Buildings[c].Name },
		func(c string) []string { return balance.Buildings[c].Aliases }, key)
}

func FindRecipe(key string) string {
	return findCode(balance.RecipeOrder, func(c string) string { return balance.Recipes[c].Name },
		func(c string) []string { return balance.Recipes[c].Aliases }, key)
}

type goodsInfo struct {
	code, name string
	price      int
}

// goods 可販售商品：加工品（程式內建）或動物產品（資料庫）；不是商品時回傳 nil。
func (r *RanchService) goods(ctx context.Context, t *db.Tx, key string) (*goodsInfo, error) {
	code := findCode(balance.GoodOrder, func(c string) string { return balance.Goods[c].Name },
		func(c string) []string { return balance.Goods[c].Aliases }, key)
	if code != "" {
		g := balance.Goods[code]
		return &goodsInfo{code, g.Name, g.SellPrice}, nil
	}
	a, err := animals.FindProduct(ctx, t, key)
	if err != nil || a == nil {
		return nil, err
	}
	return &goodsInfo{a.ProductCode, a.ProductName, a.ProductPrice}, nil
}

// AnimalTypes activeOnly 預設 true。
func (r *RanchService) AnimalTypes(ctx context.Context, activeOnly bool) ([]AnimalTypeView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]AnimalTypeView, error) {
		list, err := animals.ListAll(ctx, t, activeOnly)
		if err != nil {
			return nil, err
		}
		out := make([]AnimalTypeView, len(list))
		for i, a := range list {
			out[i] = NewAnimalTypeView(a)
		}
		return out, nil
	})
}

// ---------- 建築 ----------

func (r *RanchService) Build(ctx context.Context, playerID int64, key string) (ActionResult, error) {
	code := FindBuilding(key)
	if code == "" {
		names := make([]string, len(balance.BuildingOrder))
		for i, c := range balance.BuildingOrder {
			names[i] = balance.Buildings[c].Name
		}
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種設施。可建造：%s", key, strings.Join(names, "、"))
	}
	b := balance.Buildings[code]
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		list, err := ranch.ListBuildings(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		owned := map[int]bool{}
		for _, x := range list {
			if x.BuildingCode == code {
				owned[x.Slot] = true
			}
		}
		if len(owned) >= b.MaxPerFarm {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "%s每座農場最多 %d 座，你已經有 %d 座。", b.Name, b.MaxPerFarm, len(owned))
		}
		if farm.Gold < b.Price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s需要 %d，目前只有 %d。", b.Name, b.Price, farm.Gold)
		}
		slot := 1
		for owned[slot] {
			slot++
		}
		if _, err := ranch.AddBuilding(ctx, t, farm.ID, code, slot, r.now()); err != nil {
			return ActionResult{}, err
		}
		farm.Gold -= b.Price
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		if err := r.record(ctx, t, farm, rules.EvBuild, fmt.Sprintf("建造%s（第 %d 座），金幣 -%d", b.Name, slot, b.Price),
			rec{ItemCode: code, Quantity: 1, GoldDelta: -b.Price}); err != nil {
			return ActionResult{}, err
		}
		extra := ""
		if code == "barn" {
			extra = fmt.Sprintf("，養殖容量 +%d", balance.BarnSlots)
		}
		return result(fmt.Sprintf("已建造%s（第 %d 座），花費 %d 金幣%s。", b.Name, slot, b.Price, extra), farm.Gold, -b.Price, code, 1), nil
	})
}

func (r *RanchService) capacity(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, err := ranch.CountBuildings(ctx, t, farmID, "barn")
	return n * balance.BarnSlots, err
}

func (r *RanchService) WorkshopStatus(ctx context.Context, playerID int64) (WorkshopStatus, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) (WorkshopStatus, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return WorkshopStatus{}, err
		}
		list, err := ranch.ListBuildings(ctx, t, farm.ID)
		if err != nil {
			return WorkshopStatus{}, err
		}
		st := WorkshopStatus{Counts: map[string]int{}, Busy: map[string]*OrderView{}, Queued: map[string]int{}, Gold: farm.Gold}
		for _, code := range balance.BuildingOrder {
			st.Counts[code] = 0
		}
		for _, b := range list {
			if _, ok := balance.Buildings[b.BuildingCode]; ok {
				st.Counts[b.BuildingCode]++
			}
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return WorkshopStatus{}, err
		}
		for _, code := range balance.BuildingOrder {
			orders, err := ranch.ActiveOrders(ctx, t, farm.ID, code, now)
			if err != nil {
				return WorkshopStatus{}, err
			}
			st.Busy[code] = nil
			if len(orders) > 0 {
				v := NewOrderView(orders[0], now, names)
				st.Busy[code] = &v
			}
			st.Queued[code] = max(0, len(orders)-1)
		}
		if st.Capacity, err = r.capacity(ctx, t, farm.ID); err != nil {
			return WorkshopStatus{}, err
		}
		st.Animals, err = ranch.CountAnimals(ctx, t, farm.ID)
		return st, err
	})
}

// ---------- 動物 ----------

func (r *RanchService) BuyAnimals(ctx context.Context, playerID int64, key string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	if quantity > balance.AnimalMaxBuy {
		return ActionResult{}, gameerr.Validationf("一次最多購買 %d 隻。", balance.AnimalMaxBuy)
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		at, err := animals.Find(ctx, t, key)
		if err != nil {
			return ActionResult{}, err
		}
		if at == nil || !at.Active {
			list, err := animals.ListAll(ctx, t, true)
			if err != nil {
				return ActionResult{}, err
			}
			names := make([]string, len(list))
			for i, a := range list {
				names[i] = a.Name
			}
			joined := strings.Join(names, "、")
			if joined == "" {
				joined = "（目前沒有）"
			}
			reason := "這種動物不存在"
			if at != nil {
				reason = "已停用，不能購買"
			}
			return ActionResult{}, gameerr.NotFoundf("「%s」%s。可購買：%s", key, reason, joined)
		}
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		capacity, err := r.capacity(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if capacity == 0 {
			return ActionResult{}, gameerr.Validationf("動物必須安置在畜舍中，請先建造畜舍（workshop build 畜舍）。")
		}
		used, err := ranch.CountAnimals(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		// 先檢查容量與金幣，兩者都通過才扣款
		if used+quantity > capacity {
			return ActionResult{}, gameerr.Validationf("養殖容量不足：目前 %d/%d，無法再安置 %d 隻。", used, capacity, quantity)
		}
		cost := at.Price * quantity
		if farm.Gold < cost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：需要 %d，目前只有 %d。", cost, farm.Gold)
		}
		numbers, err := ranch.AddAnimals(ctx, t, farm.ID, at.Code, quantity, r.now())
		if err != nil {
			return ActionResult{}, err
		}
		farm.Gold -= cost
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		first, last := numbers[0], numbers[len(numbers)-1]
		if err := r.record(ctx, t, farm, rules.EvBuyAnimal,
			fmt.Sprintf("購買 %d 隻%s（編號 %d–%d），金幣 -%d", quantity, at.Name, first, last, cost),
			rec{ItemCode: at.Code, Quantity: quantity, GoldDelta: -cost}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已購買 %d 隻%s（編號 %d–%d），花費 %d 金幣。記得餵食（animal feed <編號>）才會開始生產。",
			quantity, at.Name, first, last, cost), farm.Gold, -cost, at.Code, quantity), nil
	})
}

func (r *RanchService) ListAnimals(ctx context.Context, playerID int64) ([]AnimalView, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]AnimalView, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		types, err := animals.ByCode(ctx, t)
		if err != nil {
			return nil, err
		}
		list, err := ranch.ListAnimals(ctx, t, farm.ID, false)
		if err != nil {
			return nil, err
		}
		out := make([]AnimalView, len(list))
		for i, a := range list {
			out[i] = NewAnimalView(a, now, types[a.AnimalCode])
		}
		return out, nil
	})
}

func (r *RanchService) AnimalStatus(ctx context.Context, playerID int64) (AnimalStatus, error) {
	list, err := r.ListAnimals(ctx, playerID)
	if err != nil {
		return AnimalStatus{}, err
	}
	var gold, capacity int
	if err := r.transaction(ctx, func(t *db.Tx) error {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return err
		}
		gold = farm.Gold
		capacity, err = r.capacity(ctx, t, farm.ID)
		return err
	}); err != nil {
		return AnimalStatus{}, err
	}
	st := AnimalStatus{Capacity: capacity, Used: len(list), Gold: gold, Counts: map[string]int{
		string(rules.AnimalHungry): 0, string(rules.AnimalProducing): 0, string(rules.AnimalReady): 0}}
	for _, a := range list {
		st.Counts[a.State]++
		switch a.State {
		case string(rules.AnimalHungry):
			st.FeedCostAll += a.FeedCost
		case string(rules.AnimalProducing):
			if st.NextReadyAt == nil || a.ReadyAt.Before(*st.NextReadyAt) {
				st.NextReadyAt = a.ReadyAt
			}
		}
	}
	return st, nil
}

func (r *RanchService) feed(ctx context.Context, t *db.Tx, farm *models.Farm, a *models.FarmAnimal, now time.Time,
	at models.AnimalType) (int, error) {
	farm.Gold -= at.FeedCost
	if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
		return 0, err
	}
	ready := now.Add(time.Duration(at.CycleSeconds) * time.Second)
	a.FedAt, a.ReadyAt = &now, &ready
	if _, err := db.UpdateByID(ctx, t, models.TableFarmAnimal, a.ID, db.C("fed_at", now), db.C("ready_at", ready)); err != nil {
		return 0, err
	}
	if err := r.record(ctx, t, farm, rules.EvFeedAnimal, fmt.Sprintf("餵食 %s#%d，金幣 -%d", at.Name, a.AnimalNo, at.FeedCost),
		rec{ItemCode: a.AnimalCode, Quantity: 1, GoldDelta: -at.FeedCost}); err != nil {
		return 0, err
	}
	return at.FeedCost, nil
}

// lockedAnimal 已鎖定農場中的一隻動物與其種類。
func (r *RanchService) lockedAnimal(ctx context.Context, t *db.Tx, farm *models.Farm, animalNo int) (*models.FarmAnimal, *models.AnimalType, error) {
	a, err := ranch.GetAnimal(ctx, t, farm.ID, animalNo, true)
	if err != nil {
		return nil, nil, err
	}
	if a == nil {
		return nil, nil, gameerr.NotFoundf("沒有編號 %d 的動物。", animalNo)
	}
	at, err := animals.Get(ctx, t, a.AnimalCode, false)
	return a, at, err
}

func (r *RanchService) FeedAnimal(ctx context.Context, playerID int64, animalNo int) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		a, at, err := r.lockedAnimal(ctx, t, farm, animalNo)
		if err != nil {
			return ActionResult{}, err
		}
		now := r.now()
		switch rules.AnimalStateAt(a.FedAt, a.ReadyAt, now) {
		case rules.AnimalProducing:
			left := rules.FormatDuration(rules.RemainingSeconds(a.ReadyAt, now))
			return ActionResult{}, gameerr.Validationf("%s#%d 已經餵過，正在生產中（還需要 %s）。", at.Name, animalNo, left)
		case rules.AnimalReady:
			return ActionResult{}, gameerr.Validationf("%s#%d 的產品還沒收集，請先收集（animal collect %d）。", at.Name, animalNo, animalNo)
		}
		if farm.Gold < at.FeedCost {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：餵食%s需要 %d，目前只有 %d。", at.Name, at.FeedCost, farm.Gold)
		}
		if _, err := r.feed(ctx, t, farm, a, now, *at); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已餵食 %s#%d，花費 %d 金幣，%s後可收集。", at.Name, animalNo, at.FeedCost,
			rules.FormatDuration(at.CycleSeconds)), farm.Gold, -at.FeedCost, "", 0), nil
	})
}

// FeedAll 依編號順序餵食所有待餵食的動物，餵到金幣不足為止；只對實際餵到的動物扣款。
func (r *RanchService) FeedAll(ctx context.Context, playerID int64) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := r.now()
		list, err := ranch.ListAnimals(ctx, t, farm.ID, true)
		if err != nil {
			return ActionResult{}, err
		}
		var hungry []*models.FarmAnimal
		for i := range list {
			if rules.AnimalStateAt(list[i].FedAt, list[i].ReadyAt, now) == rules.AnimalHungry {
				hungry = append(hungry, &list[i])
			}
		}
		if len(hungry) == 0 {
			return result("目前沒有需要餵食的動物。", farm.Gold, 0, "", 0), nil
		}
		types, err := animals.ByCode(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		var fed, skipped []*models.FarmAnimal
		spent := 0
		for _, a := range hungry {
			if len(skipped) > 0 || farm.Gold < types[a.AnimalCode].FeedCost {
				skipped = append(skipped, a)
				continue
			}
			n, err := r.feed(ctx, t, farm, a, now, types[a.AnimalCode])
			if err != nil {
				return ActionResult{}, err
			}
			spent += n
			fed = append(fed, a)
		}
		label := func(a *models.FarmAnimal) string { return fmt.Sprintf("%s#%d", types[a.AnimalCode].Name, a.AnimalNo) }
		labels := func(list []*models.FarmAnimal) string {
			parts := make([]string, len(list))
			for i, a := range list {
				parts[i] = label(a)
			}
			return strings.Join(parts, "、")
		}
		if len(fed) == 0 {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足，沒有餵食任何動物（下一隻%s需要 %d，目前只有 %d）。",
				label(skipped[0]), types[skipped[0].AnimalCode].FeedCost, farm.Gold)
		}
		msg := fmt.Sprintf("已餵食 %d 隻（%s），花費 %d 金幣。", len(fed), labels(fed), spent)
		if len(skipped) > 0 {
			msg += fmt.Sprintf(" 金幣不足，未餵食：%s。", labels(skipped))
		}
		return result(msg, farm.Gold, -spent, "", len(fed)), nil
	})
}

func (r *RanchService) CollectAnimal(ctx context.Context, playerID int64, animalNo int) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		a, at, err := r.lockedAnimal(ctx, t, farm, animalNo)
		if err != nil {
			return ActionResult{}, err
		}
		now := r.now()
		switch rules.AnimalStateAt(a.FedAt, a.ReadyAt, now) {
		case rules.AnimalHungry:
			return ActionResult{}, gameerr.Validationf("%s#%d 尚未餵食，沒有產品可收集（animal feed %d）。", at.Name, animalNo, animalNo)
		case rules.AnimalProducing:
			left := rules.FormatDuration(rules.RemainingSeconds(a.ReadyAt, now))
			return ActionResult{}, gameerr.Validationf("%s#%d 還在生產中，還需要 %s。", at.Name, animalNo, left)
		}
		qty := at.ProductQuantity
		used, err := inventory.TotalQuantity(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		if used+qty > farm.WarehouseCapacity {
			// 產品保留在動物身上，不會遺失
			return ActionResult{}, gameerr.New(gameerr.WarehouseFull,
				"背包空間不足：目前 %d/%d，需要 %d 格才能收集%s。請先販售或使用物品清出空間，產品會保留到你領取為止。",
				used, farm.WarehouseCapacity, qty, at.ProductName)
		}
		if err := inventory.Add(ctx, t, farm.ID, at.ProductCode, qty, "normal", "none"); err != nil {
			return ActionResult{}, err
		}
		a.FedAt, a.ReadyAt = nil, nil
		a.CyclesCompleted++
		if _, err := db.UpdateByID(ctx, t, models.TableFarmAnimal, a.ID, db.C("fed_at", nil), db.C("ready_at", nil),
			db.C("cycles_completed", a.CyclesCompleted)); err != nil {
			return ActionResult{}, err
		}
		if err := r.record(ctx, t, farm, rules.EvCollectAnimal,
			fmt.Sprintf("從%s#%d 收集 %d 個%s", at.Name, animalNo, qty, at.ProductName),
			rec{ItemCode: at.ProductCode, Quantity: qty}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("從%s#%d 收集了 %d 個%s。記得再次餵食以開始下一個週期。", at.Name, animalNo, qty, at.ProductName),
			farm.Gold, 0, at.ProductCode, qty), nil
	})
}

// ReleaseAnimal 放生自己的一隻動物，空出畜舍欄位；不退款。產品還沒收集時不能放生（避免產品遺失）。
func (r *RanchService) ReleaseAnimal(ctx context.Context, playerID int64, animalNo int) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		a, at, err := r.lockedAnimal(ctx, t, farm, animalNo)
		if err != nil {
			return ActionResult{}, err
		}
		state := rules.AnimalStateAt(a.FedAt, a.ReadyAt, r.now())
		if state == rules.AnimalReady {
			return ActionResult{}, gameerr.Validationf("%s#%d 還有%s沒收集，請先收集（animal collect %d）再放生。",
				at.Name, animalNo, at.ProductName, animalNo)
		}
		if _, err := t.Exec(ctx, "DELETE FROM farm_animals WHERE id = $1", a.ID); err != nil {
			return ActionResult{}, err
		}
		if err := r.record(ctx, t, farm, rules.EvReleaseAnimal, fmt.Sprintf("放生 %s#%d", at.Name, animalNo),
			rec{ItemCode: a.AnimalCode, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		note := ""
		if state == rules.AnimalProducing {
			note = "（正在生產中，這次的餵食不會退還）"
		}
		capacity, err := r.capacity(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		used, err := ranch.CountAnimals(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已放生 %s#%d%s，畜舍欄位 %d/%d，可以再買其他動物了。", at.Name, animalNo, note, used, capacity),
			farm.Gold, 0, a.AnimalCode, 1), nil
	})
}

// ---------- 加工 ----------

// Recipes playerID 為 nil 時不標示是否已建造設施。
func (r *RanchService) Recipes(ctx context.Context, playerID *int64) ([]RecipeView, error) {
	counts, have := map[string]int{}, map[string]int{}
	names, err := inTx(ctx, r.GameService, func(t *db.Tx) (map[string]string, error) {
		names, err := catalog.ItemNames(ctx, t)
		if err != nil || playerID == nil {
			return names, err
		}
		farm, err := r.farmOf(ctx, t, *playerID)
		if err != nil {
			return nil, err
		}
		list, err := ranch.ListBuildings(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			counts[b.BuildingCode]++
		}
		for _, code := range balance.RecipeOrder {
			for _, item := range balance.Recipes[code].InputOrder {
				if _, ok := have[item]; ok {
					continue
				}
				if have[item], err = craftStock(ctx, t, farm.ID, item); err != nil {
					return nil, err
				}
			}
		}
		return names, nil
	})
	if err != nil {
		return nil, err
	}
	rows := make([]RecipeView, 0, len(balance.RecipeOrder))
	for _, code := range balance.RecipeOrder {
		rc := balance.Recipes[code]
		v := RecipeView{Code: code, Name: rc.Name, BuildingCode: rc.Building, BuildingName: balance.Buildings[rc.Building].Name,
			Built: counts[rc.Building] > 0, Inputs: map[string]int{}, OutputName: ItemName(rc.Output, names),
			OutputQuantity: rc.OutputQty, Seconds: rc.Seconds}
		for _, item := range rc.InputOrder {
			n := ItemName(item, names)
			v.Inputs[n] = rc.Inputs[item]
			v.InputOrder = append(v.InputOrder, n)
		}
		if g, ok := balance.Goods[rc.Output]; ok {
			price := g.SellPrice
			v.SellPrice = &price
		}
		if playerID != nil {
			can := balance.CraftMaxQuantity
			for _, item := range rc.InputOrder {
				can = min(can, have[item]/rc.Inputs[item])
			}
			v.CanMake = &can
		}
		rows = append(rows, v)
	}
	return rows, nil
}

func (r *RanchService) StartCraft(ctx context.Context, playerID int64, key string, quantity int) (ActionResult, error) {
	code := FindRecipe(key)
	if code == "" {
		names := make([]string, len(balance.RecipeOrder))
		for i, c := range balance.RecipeOrder {
			names[i] = balance.Recipes[c].Name
		}
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這個配方。可用配方：%s", key, strings.Join(names, "、"))
	}
	if quantity <= 0 {
		return ActionResult{}, gameerr.Validationf("份數必須是正整數。")
	}
	if quantity > balance.CraftMaxQuantity {
		return ActionResult{}, gameerr.Validationf("單張訂單最多 %d 份。", balance.CraftMaxQuantity)
	}
	rc := balance.Recipes[code]
	buildingName := balance.Buildings[rc.Building].Name
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		n, err := ranch.CountBuildings(ctx, t, farm.ID, rc.Building)
		if err != nil {
			return ActionResult{}, err
		}
		if n == 0 {
			return ActionResult{}, gameerr.Validationf("需要先建造%s才能加工%s（workshop build %s）。", buildingName, rc.Name, buildingName)
		}
		return r.startOrder(ctx, t, farm, craftSpec{code, rc.Name, rc.Building, rc.Inputs, rc.InputOrder, rc.Output,
			rc.OutputQty, rc.Seconds}, quantity, "")
	})
}

// craftSpec 一張加工訂單的配方（加工配方或培育器配方）。
type craftSpec struct {
	Code, Name, Building string
	Inputs               map[string]int
	InputOrder           []string
	Output               string
	OutputQty, Seconds   int
}

// startOrder 在已鎖定的農場開一張加工訂單（設施檢查由呼叫端負責）；原料扣除與訂單建立在同一筆交易中。
func (r *RanchService) startOrder(ctx context.Context, t *db.Tx, farm *models.Farm, sp craftSpec, quantity int, source string) (ActionResult, error) {
	buildingName := FacilityName(sp.Building)
	now := r.now()
	// 設施忙碌時新訂單排在最後一張之後（開始時間 = 前一張的完成時間），最多 CraftQueueMax 張未完成的訂單
	active, err := ranch.ActiveOrders(ctx, t, farm.ID, sp.Building, now)
	if err != nil {
		return ActionResult{}, err
	}
	if len(active) >= balance.CraftQueueMax {
		ready := active[0].ReadyAt
		left := rules.FormatDuration(rules.RemainingSeconds(&ready, now))
		return ActionResult{}, gameerr.Validationf("%s的排隊訂單已滿（%d 張），訂單 #%d 還需要 %s完成，之後才能再開單。",
			buildingName, balance.CraftQueueMax, active[0].ID, left)
	}
	start := now
	if len(active) > 0 {
		start = active[len(active)-1].ReadyAt
	}
	names, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return ActionResult{}, err
	}
	needed := map[string]int{}
	var lacking []string
	for _, item := range sp.InputOrder {
		amount := sp.Inputs[item] * quantity
		needed[item] = amount
		have, err := craftStock(ctx, t, farm.ID, item) // 普通品質（無突變＋巨型、快速生長作物）
		if err != nil {
			return ActionResult{}, err
		}
		if have < amount {
			lacking = append(lacking, fmt.Sprintf("%s 需要 %d、只有 %d", ItemName(item, names), amount, have))
		}
	}
	if len(lacking) > 0 {
		return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "原料不足：%s。", strings.Join(lacking, "；"))
	}
	// 扣除原料與建立訂單在同一筆交易中完成
	for _, item := range sp.InputOrder {
		ok, err := removeCraftStock(ctx, t, farm.ID, item, needed[item])
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "原料不足：%s。", ItemName(item, names))
		}
	}
	out := sp.OutputQty * quantity
	order, err := addOrder(ctx, t, ranch.NewOrder{FarmID: farm.ID, RecipeCode: sp.Code, BuildingCode: sp.Building,
		Quantity: quantity, Inputs: needed, OutputCode: sp.Output, OutputQuantity: out, Status: "in_progress",
		StartedAt: start, ReadyAt: start.Add(time.Duration(sp.Seconds*quantity) * time.Second)})
	if err != nil {
		return ActionResult{}, err
	}
	parts := make([]string, len(sp.InputOrder))
	for i, item := range sp.InputOrder {
		parts[i] = fmt.Sprintf("%s x%d", ItemName(item, names), needed[item])
	}
	used := strings.Join(parts, "、")
	if err := r.record(ctx, t, farm, rules.EvCraftStart,
		fmt.Sprintf("%s訂單 #%d：以 %s 開始加工 %d 份%s", source, order.ID, used, out, sp.Name),
		rec{ItemCode: sp.Output, Quantity: out}); err != nil {
		return ActionResult{}, err
	}
	wait := ""
	if len(active) > 0 {
		wait = fmt.Sprintf("排在訂單 #%d 之後，", active[len(active)-1].ID)
	}
	return result(fmt.Sprintf("已建立加工訂單 #%d：%s加工 %d 份%s，扣除原料 %s，%s%s後完成。", order.ID, buildingName, out, sp.Name,
		used, wait, rules.FormatDuration(rules.RemainingSeconds(&order.ReadyAt, now))), farm.Gold, 0, sp.Output, out), nil
}

// CraftOrders includeCollected 預設 false，limit 預設 20。
func (r *RanchService) CraftOrders(ctx context.Context, playerID int64, includeCollected bool, limit int) ([]OrderView, error) {
	now := r.now()
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]OrderView, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		rows, err := ranch.ListOrders(ctx, t, &farm.ID, includeCollected, limit)
		if err != nil {
			return nil, err
		}
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return nil, err
		}
		out := make([]OrderView, len(rows))
		for i, o := range rows {
			out[i] = NewOrderView(o.CraftOrder, now, names)
		}
		return out, nil
	})
}

func (r *RanchService) CollectCraft(ctx context.Context, playerID, orderID int64) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		order, err := ranch.GetOrder(ctx, t, farm.ID, orderID, true)
		if err != nil {
			return ActionResult{}, err
		}
		if order == nil {
			return ActionResult{}, gameerr.NotFoundf("找不到你的加工訂單 #%d。", orderID)
		}
		return r.collectOrder(ctx, t, farm, order, "")
	})
}

// CancelCraft 取消一張還沒完成的加工訂單（加工中或排隊中；Go 版新增，Python 沒有）：退回全部原料、刪除訂單，
// 同一座設施排在它後面的訂單依序往前遞補（開始與完成時間提早被取消訂單剩下的時間）。已完成待領取的訂單不能取消。
// 資料表的狀態只有 in_progress／collected，所以取消是刪除訂單，另寫一筆交易紀錄（種類沿用 craft_start，說明為取消）。
func (r *RanchService) CancelCraft(ctx context.Context, playerID, orderID int64) (ActionResult, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		order, err := ranch.GetOrder(ctx, t, farm.ID, orderID, true)
		if err != nil {
			return ActionResult{}, err
		}
		if order == nil {
			return ActionResult{}, gameerr.NotFoundf("找不到你的加工訂單 #%d。", orderID)
		}
		now := r.now()
		names, err := catalog.ItemNames(ctx, t)
		if err != nil {
			return ActionResult{}, err
		}
		outName := ItemName(order.OutputCode, names)
		switch rules.CraftStateAt(order.Status, order.ReadyAt, now) {
		case rules.CraftCollected:
			return ActionResult{}, gameerr.Validationf("訂單 #%d 的%s已經領取過了，不能取消。", order.ID, outName)
		case rules.CraftReady:
			return ActionResult{}, gameerr.Validationf("訂單 #%d 的%s已經完成，請直接領取。", order.ID, outName)
		}
		// 原料放回倉庫（素材放回素材背包）；放不下時不取消
		codes := make([]string, 0, len(order.Inputs))
		for code := range order.Inputs {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		warehouse, material := 0, 0
		for _, code := range codes {
			if inventory.IsMaterial(code) {
				material += order.Inputs[code]
			} else {
				warehouse += order.Inputs[code]
			}
		}
		if warehouse > 0 {
			if err := r.ensureSpace(ctx, t, farm, warehouse); err != nil {
				return ActionResult{}, err
			}
		}
		if material > 0 {
			if err := r.ensureMaterialSpace(ctx, t, farm, material); err != nil {
				return ActionResult{}, err
			}
		}
		parts := make([]string, len(codes))
		for i, code := range codes {
			if err := inventory.Add(ctx, t, farm.ID, code, order.Inputs[code], "normal", "none"); err != nil {
				return ActionResult{}, err
			}
			parts[i] = fmt.Sprintf("%s x%d", ItemName(code, names), order.Inputs[code])
		}
		// 後面的訂單往前遞補：提早的時間 = 這張訂單還沒用掉的加工時間
		shift := order.ReadyAt.Sub(maxTime(order.StartedAt, now))
		if _, err := t.Exec(ctx, `UPDATE craft_orders SET started_at = started_at - $1::interval, ready_at = ready_at - $1::interval
			WHERE farm_id = $2 AND building_code = $3 AND status = 'in_progress' AND started_at >= $4 AND id <> $5`,
			fmt.Sprintf("%d microseconds", shift.Microseconds()), farm.ID, order.BuildingCode, order.ReadyAt, order.ID); err != nil {
			return ActionResult{}, err
		}
		if _, err := t.Exec(ctx, "DELETE FROM craft_orders WHERE id = $1", order.ID); err != nil {
			return ActionResult{}, err
		}
		returned := strings.Join(parts, "、")
		if err := r.record(ctx, t, farm, rules.EvCraftStart,
			fmt.Sprintf("取消加工訂單 #%d（%d 份%s），退回原料 %s", order.ID, order.OutputQuantity, outName, returned),
			rec{ItemCode: order.OutputCode}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已取消加工訂單 #%d（%d 份%s），退回原料 %s。", order.ID, order.OutputQuantity, outName, returned),
			farm.Gold, 0, order.OutputCode, 0), nil
	})
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// collectOrder 領取已鎖定農場的一張訂單；倉庫不足時在修改任何資料前回傳 WarehouseFull 錯誤。
func (r *RanchService) collectOrder(ctx context.Context, t *db.Tx, farm *models.Farm, order *models.CraftOrder, source string) (ActionResult, error) {
	now := r.now()
	state := rules.CraftStateAt(order.Status, order.ReadyAt, now)
	names, err := catalog.ItemNames(ctx, t)
	if err != nil {
		return ActionResult{}, err
	}
	outName := ItemName(order.OutputCode, names)
	switch state {
	case rules.CraftCollected:
		return ActionResult{}, gameerr.Validationf("訂單 #%d 的%s已經領取過了。", order.ID, outName)
	case rules.CraftInProgress:
		ready := order.ReadyAt
		return ActionResult{}, gameerr.Validationf("訂單 #%d 還在加工中，還需要 %s。", order.ID,
			rules.FormatDuration(rules.RemainingSeconds(&ready, now)))
	}
	// 培育器的稀有種子放素材背包，其餘成品放倉庫
	if err := r.ensureItemSpace(ctx, t, farm, order.OutputCode, order.OutputQuantity); err != nil {
		if gameerr.Is(err, gameerr.WarehouseFull) {
			return ActionResult{}, gameerr.New(gameerr.WarehouseFull, "%s成品會保留在訂單 #%d（待領取），清出空間後再領取。", err.Error(), order.ID)
		}
		return ActionResult{}, err
	}
	if err := inventory.Add(ctx, t, farm.ID, order.OutputCode, order.OutputQuantity, "normal", "none"); err != nil {
		return ActionResult{}, err
	}
	order.Status, order.CollectedAt = "collected", &now
	if _, err := db.UpdateByID(ctx, t, models.TableCraftOrder, order.ID, db.C("status", order.Status),
		db.C("collected_at", now)); err != nil {
		return ActionResult{}, err
	}
	if err := r.record(ctx, t, farm, rules.EvCraftCollect,
		fmt.Sprintf("%s領取訂單 #%d：%d 份%s", source, order.ID, order.OutputQuantity, outName),
		rec{ItemCode: order.OutputCode, Quantity: order.OutputQuantity}); err != nil {
		return ActionResult{}, err
	}
	return result(fmt.Sprintf("已領取訂單 #%d：%d 份%s。", order.ID, order.OutputQuantity, outName),
		farm.Gold, 0, order.OutputCode, order.OutputQuantity), nil
}

// ---------- 商品販售 ----------

func (r *RanchService) SellGoods(ctx context.Context, playerID int64, key string, quantity int) (ActionResult, error) {
	quantity, err := rules.ValidateQuantity(quantity)
	if err != nil {
		return ActionResult{}, err
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		g, err := r.goods(ctx, t, key)
		if err != nil {
			return ActionResult{}, err
		}
		if g == nil {
			return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種商品。", key)
		}
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, g.code, quantity, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			have, err := inventory.GetQuantity(ctx, t, farm.ID, g.code, "normal", "none")
			if err != nil {
				return ActionResult{}, err
			}
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "%s數量不足：想賣 %d 個，背包只有 %d 個。", g.name, quantity, have)
		}
		income := g.price * quantity
		farm.Gold += income
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		price := g.price
		if err := r.record(ctx, t, farm, rules.EvSell, fmt.Sprintf("出售 %d 個%s（單價 %d），金幣 +%d", quantity, g.name, price, income),
			rec{ItemCode: g.code, Quantity: quantity, GoldDelta: income, UnitPrice: &price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("已出售 %d 個%s，獲得 %d 金幣。%s", quantity, g.name, income, GoldChange(farm.Gold, income)),
			farm.Gold, income, g.code, quantity), nil
	})
}

// Sell 販售入口：作物（可指定品質與突變）或動物產品／加工品（只有普通品質）。quality 預設 "normal"，mutation 預設 "none"。
func (r *RanchService) Sell(ctx context.Context, playerID int64, key string, quantity int, quality, mutation string) (ActionResult, error) {
	isGoods, err := inTx(ctx, r.GameService, func(t *db.Tx) (bool, error) {
		g, err := r.goods(ctx, t, key)
		return g != nil, err
	})
	if err != nil {
		return ActionResult{}, err
	}
	if isGoods {
		if genetics.ParseQuality(quality) != "normal" || genetics.ParseMutation(mutation) != "none" {
			return ActionResult{}, gameerr.Validationf("動物產品與加工品沒有品質或突變，不需要指定。")
		}
		return r.SellGoods(ctx, playerID, key, quantity)
	}
	return r.SellCrop(ctx, playerID, key, quantity, quality, mutation)
}

// ---------- 寵物（貓、狗） ----------

func intPtr(n int) *int { return &n }

// PetStatus 貓與狗的狀態：是否擁有、等級、目前效果與下一步（購買／升級）的價格。只讀取不鎖。
func (r *RanchService) PetStatus(ctx context.Context, playerID int64) ([]PetView, error) {
	return inTx(ctx, r.GameService, func(t *db.Tx) ([]PetView, error) {
		farm, err := r.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		owned, err := pets.Levels(ctx, t, farm.ID)
		if err != nil {
			return nil, err
		}
		var out []PetView
		for _, code := range petrules.PetOrder {
			pet := petrules.Pets[code]
			var level *int
			if lv, ok := owned[code]; ok {
				level = intPtr(lv)
			}
			v := PetView{Code: code, Name: pet.Name, Emoji: pet.Emoji, Role: pet.Role, Owned: level != nil, Level: level,
				MaxLevel: petrules.MaxLevel(), Pct: petrules.BonusPct(level), Effect: petrules.EffectText(code, level)}
			if level == nil {
				v.NextPrice = intPtr(petrules.BuyPrice())
				v.NextPct = petrules.BonusPct(intPtr(0))
			} else {
				if p, ok := petrules.UpgradePrice(*level); ok {
					v.NextPrice = intPtr(p)
				}
				v.NextPct = petrules.BonusPct(intPtr(*level + 1))
			}
			if code == petrules.Cat {
				// 每種作物每格實際多收幾個：已領養看目前等級，還沒領養看領養後的 Lv0
				shown := v.Pct
				if shown == 0 {
					shown = petrules.BonusPct(intPtr(0))
				}
				list, err := crops.ListAll(ctx, t, true)
				if err != nil {
					return nil, err
				}
				odds := &PetOdds{Pct: shown, Current: v.Pct > 0}
				for _, c := range list {
					odds.Crops = append(odds.Crops, PetCropOdds{c.Code, c.Name, c.HarvestQuantity,
						petrules.OddsText(c.HarvestQuantity, shown)})
				}
				v.Odds = odds
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// BuyPet 購買寵物（Lv0）；已經有了就要改用升級。
func (r *RanchService) BuyPet(ctx context.Context, playerID int64, petCode string) (ActionResult, error) {
	pet, ok := petrules.Pets[petCode]
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種寵物。", petCode)
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		row, err := pets.Get(ctx, t, farm.ID, petCode)
		if err != nil {
			return ActionResult{}, err
		}
		if row != nil {
			return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有%s了，可以幫牠升級。", pet.Name)
		}
		price := petrules.BuyPrice()
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s需要 %s，目前只有 %s。", pet.Name,
				numfmt.Comma(price), numfmt.Comma(farm.Gold))
		}
		farm.Gold -= price
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		// 主鍵 (farm_id, pet_code)：同時購買兩次在這裡失敗並整筆回滾
		if err := db.Insert(ctx, t, "farm_pets", db.C("farm_id", farm.ID), db.C("pet_code", petCode), db.C("level", 0),
			db.C("bought_at", r.now())); err != nil {
			return ActionResult{}, err
		}
		item := petrules.ItemCode(petCode)
		if err := r.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("領養寵物%s，金幣 -%d", pet.Name, price),
			rec{ItemCode: item, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		nxt, _ := petrules.UpgradePrice(0)
		return result(fmt.Sprintf("%s 領養了%s！牠會出現在農場上。現在是 Lv0：%s；升到 Lv1 需要 %s 金幣：%s。%s",
			pet.Emoji, pet.Name, petrules.EffectText(petCode, intPtr(0)), numfmt.Comma(nxt), petrules.EffectText(petCode, intPtr(1)),
			GoldChange(farm.Gold, -price)), farm.Gold, -price, item, 1), nil
	})
}

func (r *RanchService) UpgradePet(ctx context.Context, playerID int64, petCode string) (ActionResult, error) {
	pet, ok := petrules.Pets[petCode]
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種寵物。", petCode)
	}
	return inTx(ctx, r.GameService, func(t *db.Tx) (ActionResult, error) {
		farm, err := r.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		row, err := pets.Get(ctx, t, farm.ID, petCode)
		if err != nil {
			return ActionResult{}, err
		}
		if row == nil {
			return ActionResult{}, gameerr.NotFoundf("你還沒有%s，先領養（%s 金幣）。", pet.Name, numfmt.Comma(petrules.BuyPrice()))
		}
		price, ok := petrules.UpgradePrice(row.Level)
		if !ok {
			return ActionResult{}, gameerr.Validationf("%s已經是最高等級 Lv%d。", pet.Name, petrules.MaxLevel())
		}
		if farm.Gold < price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：升級需要 %s，目前只有 %s。",
				numfmt.Comma(price), numfmt.Comma(farm.Gold))
		}
		farm.Gold -= price
		if err := r.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		row.Level++
		if _, err := t.Exec(ctx, "UPDATE farm_pets SET level = $3 WHERE farm_id = $1 AND pet_code = $2",
			farm.ID, petCode, row.Level); err != nil {
			return ActionResult{}, err
		}
		lv := row.Level
		effect := petrules.EffectText(petCode, &lv)
		item := petrules.ItemCode(petCode)
		if err := r.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("%s升到 Lv%d（%s），金幣 -%d", pet.Name, lv, effect, price),
			rec{ItemCode: item, Quantity: 1, GoldDelta: -price}); err != nil {
			return ActionResult{}, err
		}
		tail := "已達最高等級。"
		if nxt, ok := petrules.UpgradePrice(lv); ok {
			tail = fmt.Sprintf("下一級需要 %s 金幣。", numfmt.Comma(nxt))
		}
		return result(fmt.Sprintf("%s %s升到 Lv%d：%s。\n📈 Lv%d → Lv%d：%d%% → %d%%%s\n%s", pet.Emoji, pet.Name, lv, effect,
			lv-1, lv, petrules.BonusPct(intPtr(lv-1)), petrules.BonusPct(&lv), GoldChange(farm.Gold, -price), tail),
			farm.Gold, -price, item, 1), nil
	})
}

// CraftMutations 可以當加工原料的突變（依扣除順序）：巨型作物只是收成數量 ×2、快速生長只縮短生長時間，
// 兩者售價都和普通的一樣，所以也能拿來加工
// （巨型 2026-11-12、快速生長 2026-11-14 使用者要求）。品質一律只收普通品質；退回原料時退成普通、無突變。
var CraftMutations = []string{"none", "giant", "fast"}

// craftStock 可以拿來加工的數量（普通品質、CraftMutations 的總和）。
func craftStock(ctx context.Context, t *db.Tx, farmID int64, item string) (int, error) {
	total := 0
	for _, m := range CraftMutations {
		n, err := inventory.GetQuantity(ctx, t, farmID, item, "normal", m)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// removeCraftStock 依 CraftMutations 的順序扣除加工原料（先扣無突變）；不夠時回傳 false（呼叫端應回滾）。
func removeCraftStock(ctx context.Context, t *db.Tx, farmID int64, item string, quantity int) (bool, error) {
	left := quantity
	for _, m := range CraftMutations {
		if left == 0 {
			break
		}
		n, err := inventory.GetQuantity(ctx, t, farmID, item, "normal", m)
		if err != nil {
			return false, err
		}
		take := min(n, left)
		if take == 0 {
			continue
		}
		ok, err := inventory.Remove(ctx, t, farmID, item, take, "normal", m)
		if err != nil || !ok {
			return false, err
		}
		left -= take
	}
	return left == 0, nil
}
