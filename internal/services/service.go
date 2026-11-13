// Package services 遊戲操作服務（對應 Python app/services/）。
//
// 每個公開方法都是一個完整的資料庫交易：成功時資料與交易紀錄一起提交，失敗時（包含遊戲規則錯誤）全部回滾。
// 方法只接受基本型別參數，不依賴 CMD、HTTP 或 Discord，未來的介面可直接呼叫。
//
// Python 以繼承串起各服務（GameService → RanchService → …）；Go 版以組合（嵌入）表示，本檔是最底層的 GameService。
//
// 與 Python 不同：Go 沒有 ORM autoflush。Python 修改 ORM 物件屬性靠 flush 寫入的地方，這裡一律寫明確的 UPDATE／INSERT
// （農場欄位見 saveFarm），並同步修改記憶體中的 struct。
package services

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"farmgm/internal/clock"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/exploration"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/codex"
	"farmgm/internal/repositories/crops"
	"farmgm/internal/repositories/events"
	"farmgm/internal/repositories/farms"
	"farmgm/internal/repositories/farmstats"
	"farmgm/internal/repositories/featuresettings"
	"farmgm/internal/repositories/genetics"
	"farmgm/internal/repositories/groups"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/transactions"

	eventrules "farmgm/internal/game/events"
)

// Options 可注入的時鐘與亂數來源（nil 表示使用預設值）。亂數函式回傳 [0, 1)。
type Options struct {
	Clock       clock.Clock
	Rng         func() float64 // 防守判定
	GeneticsRng func() float64 // 播種時品質與突變判定（與防守分開，互不影響）
	Dice        func() int     // 賭場擲數字（1–10）
	ExploreRng  func() float64 // 山洞探索掉落判定
	FishingRng  func() float64 // 釣魚的魚種、重量、脫鉤與魚的動作判定（FishingService）
}

// GameService 遊戲操作服務。可安全地被多個 goroutine 同時使用（每次呼叫各自一筆交易）。
type GameService struct {
	db    *db.DB
	clock clock.Clock

	rng         func() float64
	geneticsRng func() float64
	// 自動播種機挑選種子：回傳 [0, n) 的索引
	Pick       func(n int) int
	dice       func() int
	exploreRng func() float64
	// 貓的收成加成：小數部分是否多 1 個；測試可直接指定
	PetRng func() float64
	// 農場傳聞挖寶（是否挖到、寶藏金額）；和防守判定分開，測試可直接指定
	RumorRng func() float64
	// 地下菇房的擲骰（Go 版新增）；測試可直接指定
	MushroomRng func() float64
	// 種子實驗室的擲骰（鑑定、雜交、突變研究、收成傳承；Go 版新增）；測試可直接指定
	LabRng func() float64

	contextGroupID *int64
}

// NewGameService 建立服務。正式環境的亂數使用 math/rand/v2 的全域來源（ChaCha8，自動以系統亂數播種）。
func NewGameService(d *db.DB, opt Options) *GameService {
	s := &GameService{
		db:          d,
		clock:       opt.Clock,
		rng:         opt.Rng,
		geneticsRng: opt.GeneticsRng,
		dice:        opt.Dice,
		exploreRng:  opt.ExploreRng,
		Pick:        rand.IntN,
		PetRng:      rand.Float64,
		RumorRng:    rand.Float64,
		MushroomRng: rand.Float64,
		LabRng:      rand.Float64,
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.rng == nil {
		s.rng = rand.Float64
	}
	if s.geneticsRng == nil {
		s.geneticsRng = rand.Float64
	}
	if s.dice == nil {
		s.dice = func() int { return balance.GambleMin + rand.IntN(balance.GambleMax-balance.GambleMin+1) }
	}
	if s.exploreRng == nil {
		s.exploreRng = rand.Float64
	}
	return s
}

// InGroupContext 回傳記錄操作所在群組（context_group_id）的服務副本；只做紀錄，不影響農場歸屬或排行。nil 表示不記錄。
func (s *GameService) InGroupContext(groupID *int64) *GameService {
	clone := *s
	clone.contextGroupID = groupID
	return &clone
}

// now 目前時間，截到微秒（與 Python datetime 及資料庫 timestamptz 的精度相同，讀回後比較才會相等）。
func (s *GameService) now() time.Time { return s.clock.Now().Truncate(time.Microsecond) }

// ---------- 交易與共用交易（run_batch） ----------

type batchKey struct{}

// batch 進行中的共用交易；以 *db.DB 區分（服務副本 InGroupContext 共用同一筆）。
type batch struct {
	db     *db.DB
	tx     *db.Tx
	failed bool
}

func (s *GameService) currentBatch(ctx context.Context) *batch {
	b, _ := ctx.Value(batchKey{}).(*batch)
	if b != nil && b.db == s.db {
		return b
	}
	return nil
}

// InBatch 是否在 RunBatch 的共用交易中。
func (s *GameService) InBatch(ctx context.Context) bool { return s.currentBatch(ctx) != nil }

// transaction 一個公開方法的交易：在 RunBatch 中沿用共用交易，否則開新交易（開始時讀取管理網站調整的新功能規則）。
func (s *GameService) transaction(ctx context.Context, fn func(t *db.Tx) error) (err error) {
	b := s.currentBatch(ctx)
	if b == nil {
		return s.db.InTx(ctx, db.TxOptions{}, func(t *db.Tx) error {
			if err := featuresettings.Refresh(ctx, t, false); err != nil { // 每 10 秒最多讀一次
				return err
			}
			return fn(t)
		})
	}
	// 在 RunBatch 中：由 RunBatch 統一提交或回滾
	if b.failed {
		return errors.New("共用交易中先前的操作已失敗，不能繼續使用。")
	}
	defer func() {
		if p := recover(); p != nil {
			b.failed = true
			panic(p)
		}
	}()
	if err = fn(b.tx); err != nil {
		b.failed = true // 這次操作的部分寫入無法單獨回滾，整筆交易作廢
	}
	return err
}

// inTx transaction 的回傳值版本。
func inTx[T any](ctx context.Context, s *GameService, fn func(t *db.Tx) (T, error)) (T, error) {
	var out T
	err := s.transaction(ctx, func(t *db.Tx) error {
		v, err := fn(t)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// RunBatch 在同一筆交易中執行 fn 裡的多個服務呼叫（例如面板一次讀取整頁資料），減少連線與提交的往返。
//
// fn 必須使用傳入的 ctx 呼叫服務方法。fn 回傳錯誤時回滾整筆交易；已在共用交易中時直接執行。
// 死結重試由外層（RetryingService）對整個 fn 進行。
func (s *GameService) RunBatch(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.InBatch(ctx) {
		return fn(ctx)
	}
	return s.db.InTx(ctx, db.TxOptions{}, func(t *db.Tx) error {
		return t.ReadScope(func() error {
			if err := featuresettings.Refresh(ctx, t, false); err != nil {
				return err
			}
			b := &batch{db: s.db, tx: t}
			if err := fn(context.WithValue(ctx, batchKey{}, b)); err != nil {
				return err
			}
			if b.failed { // fn 自己吃掉了錯誤：交易狀態不可靠，不提交
				return errors.New("共用交易中的操作失敗，已回滾。")
			}
			return nil
		})
	})
}

var errIsolatedFailed = errors.New("共用交易中的附加讀取失敗，已回滾這一段。")

// Isolated 在共用交易（RunBatch）中以 SAVEPOINT 執行 fn：fn 失敗（包含 fn 自己吃掉錯誤）時只回滾這一段，
// 共用交易繼續可用，例如操作後的附加讀取失敗不影響頁面讀取。不在共用交易中時直接執行。
func (s *GameService) Isolated(ctx context.Context, fn func(ctx context.Context) error) error {
	b := s.currentBatch(ctx)
	if b == nil {
		return fn(ctx)
	}
	if b.failed {
		return errors.New("共用交易中先前的操作已失敗，不能繼續使用。")
	}
	err := b.tx.Savepoint(ctx, func() error {
		if err := fn(ctx); err != nil {
			return err
		}
		if b.failed {
			return errIsolatedFailed
		}
		return nil
	})
	if err != nil {
		b.failed = false // 已回滾到 SAVEPOINT，交易狀態可靠
	}
	return err
}

// isIntegrity 違反資料庫完整性限制（SQLSTATE 類別 23，對應 SQLAlchemy IntegrityError）。
func isIntegrity(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && len(pg.Code) == 5 && pg.Code[:2] == "23"
}

// ---------- 共用的內部方法 ----------

func (s *GameService) lockedFarm(ctx context.Context, t *db.Tx, playerID int64) (*models.Farm, error) {
	farm, err := farms.GetByPlayer(ctx, t, playerID, true)
	if err != nil || farm != nil {
		return farm, err
	}
	return nil, s.missingFarm(ctx, t, playerID)
}

// missingFarm 找不到農場時的正確錯誤訊息（Python 在無鎖讀取找不到農場時呼叫 _locked_farm 產生錯誤）。
func (s *GameService) missingFarm(ctx context.Context, t *db.Tx, playerID int64) error {
	p, err := players.Get(ctx, t, playerID, false)
	if err != nil {
		return err
	}
	if p == nil {
		return gameerr.NotFoundf("找不到玩家 #%d。", playerID)
	}
	return gameerr.NotFoundf("這位玩家還沒有農場，請先建立農場（farm start）。")
}

// farmOf 不加鎖讀取農場；沒有農場時回傳錯誤。
func (s *GameService) farmOf(ctx context.Context, t *db.Tx, playerID int64) (*models.Farm, error) {
	farm, err := farms.GetByPlayer(ctx, t, playerID, false)
	if err != nil || farm != nil {
		return farm, err
	}
	return nil, s.missingFarm(ctx, t, playerID)
}

func (s *GameService) crop(ctx context.Context, t *db.Tx, key string, requireActive bool) (*models.CropType, error) {
	crop, err := crops.Find(ctx, t, key)
	if err != nil {
		return nil, err
	}
	if crop == nil {
		all, err := crops.ListAll(ctx, t, true)
		if err != nil {
			return nil, err
		}
		names := make([]string, len(all))
		for i, c := range all {
			names[i] = fmt.Sprintf("%s（%s）", c.Code, c.Name)
		}
		return nil, gameerr.NotFoundf("沒有「%s」這種作物。可用作物：%s", key, joinStr(names, "、"))
	}
	if requireActive && !crop.Active {
		return nil, gameerr.Validationf("%s已停用，不能購買種子或種植（已持有的作物仍可收成、販售）。", crop.Name)
	}
	return crop, nil
}

// ---------- 一鍵操作的交易內快取（說明見 db/txcache.go） ----------

type (
	eventsKey   struct{}
	geneticsKey struct{}
	toolKey     struct {
		farmID int64
		code   string
	}
	effectKey struct {
		farmID int64
		code   string
	}
	memberKey struct{ groupID, playerID int64 }
	petKey    struct {
		farmID int64
		code   string
	}
	stolenKey        struct{ farmID int64 }
	warehouseUsedKey struct{ farmID int64 }
)

func (s *GameService) activeEvents(ctx context.Context, t *db.Tx) ([]*eventrules.Event, error) {
	return db.Memo(t, eventsKey{}, func() ([]*eventrules.Event, error) { return events.Active(ctx, t, s.now()) })
}

// geneticsSettings 目前生效的品質與突變設定（最新版本）。
func (s *GameService) geneticsSettings(ctx context.Context, t *db.Tx) (balance.GeneticsSettings, error) {
	return db.Memo(t, geneticsKey{}, func() (balance.GeneticsSettings, error) {
		_, settings, err := genetics.Current(ctx, t)
		return settings, err
	})
}

func (s *GameService) toolEnabled(ctx context.Context, t *db.Tx, farmID int64, code string) (bool, error) {
	return db.Memo(t, toolKey{farmID, code}, func() (bool, error) { return items.ToolEnabled(ctx, t, farmID, code) })
}

func (s *GameService) ensureSpace(ctx context.Context, t *db.Tx, farm *models.Farm, incoming int) error {
	// 一鍵收成時只查一次，之後由 harvestLocked 以 Adjust 累加
	used, err := db.Memo(t, warehouseUsedKey{farm.ID}, func() (int, error) { return inventory.TotalQuantity(ctx, t, farm.ID) })
	if err != nil {
		return err
	}
	if used+incoming > farm.WarehouseCapacity {
		return gameerr.New(gameerr.WarehouseFull, "倉庫空間不足：目前 %d/%d，需要再放入 %d 個。", used, farm.WarehouseCapacity, incoming)
	}
	return nil
}

// ensureMaterialSpace 素材背包（山洞素材、假幣與所有種子，不佔倉庫）是否放得下。
func (s *GameService) ensureMaterialSpace(ctx context.Context, t *db.Tx, farm *models.Farm, incoming int) error {
	used, err := inventory.MaterialQuantity(ctx, t, farm.ID)
	if err != nil {
		return err
	}
	capacity := exploration.BagCapacity()
	if incoming > 0 && used+incoming > capacity {
		return gameerr.New(gameerr.WarehouseFull, "素材背包空間不足：目前 %d/%d，需要再放入 %d 個。", used, capacity, incoming)
	}
	return nil
}

// ensureItemSpace 依物品放的位置檢查倉庫或素材背包。
func (s *GameService) ensureItemSpace(ctx context.Context, t *db.Tx, farm *models.Farm, itemCode string, incoming int) error {
	if inventory.IsMaterial(itemCode) {
		return s.ensureMaterialSpace(ctx, t, farm, incoming)
	}
	return s.ensureSpace(ctx, t, farm, incoming)
}

// ---------- 農場欄位的寫入（取代 Python 的 ORM flush） ----------

const touchedKey = "touched_farms"

func touched(t *db.Tx) map[int64]bool {
	m, _ := t.Info[touchedKey].(map[int64]bool)
	if m == nil {
		m = map[int64]bool{}
		t.Info[touchedKey] = m
	}
	return m
}

// saveFarm 寫入呼叫端已修改的農場欄位。
//
// updated_at 與 Python 相同：這筆交易第一次更新這座農場時設為時鐘時間（Python 在 _record 中設定），
// 之後的更新設為資料庫的 now()（Python onupdate=now()）。
func (s *GameService) saveFarm(ctx context.Context, t *db.Tx, farm *models.Farm, cols ...db.Col) error {
	m := touched(t)
	if !m[farm.ID] {
		m[farm.ID] = true
		farm.UpdatedAt = s.now()
		cols = append(cols, db.C("updated_at", farm.UpdatedAt))
		_, err := db.UpdateByID(ctx, t, models.TableFarm, farm.ID, cols...)
		return err
	}
	set, args := "", make([]any, 0, len(cols)+1)
	for _, c := range cols {
		args = append(args, c.Value)
		set += fmt.Sprintf("%s = $%d, ", c.Name, len(args))
	}
	args = append(args, farm.ID)
	_, err := t.Exec(ctx, fmt.Sprintf("UPDATE farms SET %supdated_at = now() WHERE id = $%d", set, len(args)), args...)
	return err
}

// ---------- 交易紀錄 ----------

// addTransaction 寫入交易紀錄（測試可替換以模擬寫入失敗，對應 Python 測試的 monkeypatch）。
var addTransaction = transactions.Add

// rec 交易紀錄的可選欄位（Python _record 的 keyword 參數）。
type rec struct {
	ItemCode  string // "" = None
	Quantity  int
	GoldDelta int
	Quality   string // "" = None
	Mutation  string // "" = None
	UnitPrice *int
	Applied   []eventrules.Applied // nil = None
	// SetContext 為 true 時使用 ContextGroupID（可為 nil），不依服務的群組上下文判斷
	SetContext     bool
	ContextGroupID *int64
}

func ctxGroup(id int64) rec { return rec{SetContext: true, ContextGroupID: &id} }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// record 寫入交易紀錄，並在同一筆交易中更新排行統計、群組任務與競賽、圖鑑。
func (s *GameService) record(ctx context.Context, t *db.Tx, farm *models.Farm, ev rules.EventType, desc string, r rec) error {
	now := s.now()
	// 每筆交易只更新一次農場的 updated_at（每次都設會讓逐格操作每格多一條 UPDATE farms）
	if m := touched(t); !m[farm.ID] {
		m[farm.ID] = true
		farm.UpdatedAt = now
		if _, err := db.UpdateByID(ctx, t, models.TableFarm, farm.ID, db.C("updated_at", now)); err != nil {
			return err
		}
	}
	groupID := r.ContextGroupID
	if !r.SetContext {
		groupID = nil
		// 只有玩家確實是該群組成員時才記錄上下文，避免把操作歸到無關的群組
		if c := s.contextGroupID; c != nil {
			member, err := db.Memo(t, memberKey{*c, farm.PlayerID}, func() (bool, error) {
				return groups.IsMember(ctx, t, *c, farm.PlayerID)
			})
			if err != nil {
				return err
			}
			if member {
				groupID = c
			}
		}
	}
	// 一鍵操作中，不會計入任務或競賽的交易紀錄整批寫入（計分需要交易紀錄的 ID，才逐筆寫）
	deferWrite := false
	if t.InBulk() {
		may, err := mayScore(ctx, t, farm, string(ev), groupID, now)
		if err != nil {
			return err
		}
		deferWrite = !may
	}
	var applied []models.AppliedEvent
	if r.Applied != nil {
		applied = make([]models.AppliedEvent, len(r.Applied))
		for i, a := range r.Applied {
			applied[i] = models.AppliedEvent{ID: a.ID, Name: a.Name}
		}
	}
	tx, err := addTransaction(ctx, t, transactions.AddParams{
		Defer: deferWrite, FarmID: farm.ID, EventType: string(ev), Description: desc, CreatedAt: now,
		ItemCode: strPtr(r.ItemCode), Quantity: r.Quantity, GoldDelta: r.GoldDelta, ContextGroupID: groupID,
		Quality: strPtr(r.Quality), Mutation: strPtr(r.Mutation), UnitPrice: r.UnitPrice, AppliedEvents: applied,
	})
	if err != nil {
		return err
	}
	// 同一筆交易中更新排行用的累積統計，並計入群組任務與競賽（只有成功提交的操作才會計分）
	if err := farmstats.OnTransaction(ctx, t, farm, tx); err != nil {
		return err
	}
	if err := onEvent(ctx, t, farm, tx); err != nil {
		return err
	}
	if codex.CodexEvents[tx.EventType] && r.ItemCode != "" { // 取得物品時登記圖鑑
		return codex.Register(ctx, t, farm.ID, r.ItemCode, tx.Quantity, r.Quality, now)
	}
	return nil
}
