package services

// 管理網站：Go 版新增功能的唯讀檢視（2026-11-16）。
//
// 種子實驗室、地下菇房、山洞探險、機器、庭院擺設、魚簍與自動釣魚機，以及群組的古代遺跡與日報。
// 這些畫面的讀取方法和 Bot 面板共用，讀取時會順便結算自動化（例如自動釣魚機補算、遺跡守護者結算），
// 所以一律在 preview 裡執行：結果和玩家下次打開面板看到的一樣，但交易最後回滾，不會寫入資料庫。

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/decor"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/repositories/groups"
	ruinsrepo "farmgm/internal/repositories/ruins"
)

var errPreviewRollback = errors.New("preview rollback")

// previewKey 標記 ctx 在 preview 中：交易最後會回滾，算出來的結果不可以寫進程序內的快取（例如日報）。
type previewKey struct{}

// inPreview ctx 是否在 preview（最後一定回滾的交易）中。
func inPreview(ctx context.Context) bool { v, _ := ctx.Value(previewKey{}).(bool); return v }

// preview 在一筆最後一定回滾的共用交易（RunBatch）中執行 fn（fn 用傳入的 ctx 呼叫服務方法，所有方法共用這筆交易）。
// 2026-11-16：所有區塊共用一筆交易（以前每個區塊各開一筆、各鎖一次農場），死結或序列化失敗時整批重試。
func (s *GameService) preview(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx = context.WithValue(ctx, previewKey{}, true)
	err := Retrier{}.Do(ctx, "admin_preview", func(ctx context.Context) error {
		return s.RunBatch(ctx, func(ctx context.Context) error {
			if err := fn(ctx); err != nil {
				return err
			}
			return errPreviewRollback
		})
	})
	if errors.Is(err, errPreviewRollback) {
		return nil
	}
	return err
}

// previewSection 在 preview 的共用交易中以 SAVEPOINT 讀取一個區塊；失敗時只回滾這一段，
// 回傳給畫面顯示的錯誤訊息（不影響其他區塊）。
func previewSection[T any](ctx context.Context, s *GameService, read func(ctx context.Context) (T, error)) (T, string) {
	var out T
	err := s.Isolated(ctx, func(ctx context.Context) error {
		v, err := read(ctx)
		out = v
		return err
	})
	if err != nil {
		var zero T
		if ge, ok := gameerr.As(err); ok {
			return zero, ge.Error()
		}
		slog.Error("管理網站讀取新功能資料失敗", "error", err.Error())
		return zero, "讀取失敗（詳情記錄在伺服器日誌），請稍後再試。"
	}
	return out, ""
}

// ---------- 單一玩家 ----------

type AdminLabSeed struct {
	ID           int64  `json:"id"`
	CropName     string `json:"crop_name"`
	Genes        string `json:"genes"`
	GeneTotal    int    `json:"gene_total"`
	Generation   int    `json:"generation"`
	MutationName string `json:"mutation_name"`
	Busy         bool   `json:"busy"`
	Hybrid       bool   `json:"hybrid"`
}

type AdminLabJob struct {
	Slot       int       `json:"slot"`
	Kind       string    `json:"kind"` // cross／mutate／clone
	Mother     string    `json:"mother"`
	Father     string    `json:"father"`
	ResultName string    `json:"result_name"`
	Batches    int       `json:"batches"`
	ReadyAt    time.Time `json:"ready_at"`
	Ready      bool      `json:"ready"`
}

type AdminLab struct {
	Unlocked     bool           `json:"unlocked"`
	Built        bool           `json:"built"`
	UnlockLevel  int            `json:"unlock_level"`
	LabLevel     int            `json:"lab_level"`
	Exp          int            `json:"exp"`
	NextExp      int            `json:"next_exp"`
	Slots        int            `json:"slots"`
	FreeSlots    int            `json:"free_slots"`
	ExtraSlots   int            `json:"extra_slots"`
	LibraryLevel int            `json:"library_level"`
	Capacity     int            `json:"capacity"`
	SentToday    int            `json:"sent_today"`
	Jobs         []AdminLabJob  `json:"jobs"`
	Seeds        []AdminLabSeed `json:"seeds"`
}

type AdminCellarBed struct {
	No      int       `json:"no"`
	ReadyAt time.Time `json:"ready_at"`
	Sawdust int       `json:"sawdust"`
	Ready   bool      `json:"ready"`
}

type AdminCellar struct {
	Unlocked    bool             `json:"unlocked"`
	UnlockLevel int              `json:"unlock_level"`
	Sawdust     int              `json:"sawdust"`
	Hourglasses int              `json:"hourglasses"`
	Beds        []AdminCellarBed `json:"beds"`
}

type AdminTrip struct {
	AreaName string    `json:"area_name"`
	Emoji    string    `json:"emoji"`
	Started  time.Time `json:"started_at"`
	EndsAt   time.Time `json:"ends_at"`
	Finished bool      `json:"finished"`
}

type AdminArea struct {
	Name         string     `json:"name"`
	Emoji        string     `json:"emoji"`
	Unlocked     bool       `json:"unlocked"`
	UnlockLevel  int        `json:"unlock_level"`
	ExploreCount int        `json:"explore_count"`
	NextAt       *time.Time `json:"next_at"`
}

type AdminExploration struct {
	Trip  *AdminTrip  `json:"trip"`
	Areas []AdminArea `json:"areas"`
}

type AdminMachine struct {
	Name     string `json:"name"`
	Emoji    string `json:"emoji"`
	Unlocked bool   `json:"unlocked"`
	Owned    bool   `json:"owned"`
	Enabled  bool   `json:"enabled"`
}

type AdminDecor struct {
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	Tier  string `json:"tier"`
	Slot  string `json:"slot"` // 擺放位置（左上…右下）；收起來時為 ""
}

type AdminAutoFish struct {
	Owned      bool       `json:"owned"`
	Enabled    bool       `json:"enabled"`
	AutoSell   bool       `json:"auto_sell"`
	Paused     bool       `json:"paused"`
	NoBait     bool       `json:"no_bait"`
	NextAt     *time.Time `json:"next_at"`
	SpeedLevel int        `json:"speed_level"`
	BaitName   string     `json:"bait_name"`
	BaitLeft   int        `json:"bait_left"` // -1 = 不消耗魚餌
	Caught     int        `json:"caught"`    // 這次讀取補算的魚（尚未寫入資料庫）
}

type AdminFishBasket struct {
	Count      int           `json:"count"`
	Capacity   int           `json:"capacity"`
	TotalValue int           `json:"total_value"`
	AutoFish   AdminAutoFish `json:"auto_fish"`
}

// AdminGoFeaturesView 每個區塊各自讀取；讀取失敗時該區塊為 nil、錯誤訊息放在 Errors（區塊代碼 → 訊息）。
type AdminGoFeaturesView struct {
	Lab         *AdminLab         `json:"lab"`
	Cellar      *AdminCellar      `json:"cellar"`
	Exploration *AdminExploration `json:"exploration"`
	Machines    []AdminMachine    `json:"machines"`
	Decor       []AdminDecor      `json:"decor"` // 擁有的裝飾
	FishBasket  *AdminFishBasket  `json:"fish_basket"`
	Errors      map[string]string `json:"errors"`
	ReadAt      time.Time         `json:"read_at"`
}

func adminLabSeed(s LabSeedView) AdminLabSeed {
	return AdminLabSeed{ID: s.ID, CropName: s.CropName, Genes: s.Genes.Text(), GeneTotal: s.Genes.Total(), Generation: s.Generation,
		MutationName: s.MutationName, Busy: s.Busy, Hybrid: s.Hybrid}
}

// AdminGoFeatures 玩家在 Go 版新增功能的狀態（唯讀；沒有農場時回傳 nil）。
func (a *AdminService) AdminGoFeatures(ctx context.Context, playerID int64) (*AdminGoFeaturesView, error) {
	exists := false
	if err := a.db.InTx(ctx, db.TxOptions{ReadOnly: true}, func(t *db.Tx) error {
		_, ok, err := db.QueryValue[int64](ctx, t, "SELECT id FROM farms WHERE player_id = $1", playerID)
		exists = ok
		return err
	}); err != nil || !exists {
		return nil, err
	}
	g := a.GameService
	v := &AdminGoFeaturesView{Errors: map[string]string{}, ReadAt: a.now(), Machines: []AdminMachine{}, Decor: []AdminDecor{}}
	fail := func(key, msg string) bool {
		if msg != "" {
			v.Errors[key] = msg
			return true
		}
		return false
	}

	// 所有區塊共用一筆 preview 交易（每個區塊各自 SAVEPOINT，失敗不影響其他區塊）
	if err := a.preview(ctx, func(ctx context.Context) error {
		if lab, msg := previewSection(ctx, g, func(ctx context.Context) (SeedLabView, error) { return a.SeedLab(ctx, playerID) }); !fail("lab", msg) {
			out := &AdminLab{Unlocked: lab.Unlocked, Built: lab.Built, UnlockLevel: lab.UnlockLevel, LabLevel: lab.LabLevel, Exp: lab.Exp,
				NextExp: lab.NextExp, Slots: lab.Slots, FreeSlots: lab.FreeSlots, ExtraSlots: lab.ExtraSlots, LibraryLevel: lab.LibraryLevel,
				Capacity: lab.Capacity, SentToday: lab.SentToday, Jobs: []AdminLabJob{}, Seeds: []AdminLabSeed{}}
			for _, j := range lab.Jobs {
				job := AdminLabJob{Slot: j.Slot, Kind: j.Kind, Mother: adminLabSeed(j.Mother).label(), ResultName: j.ResultName,
					Batches: j.Batches, ReadyAt: j.ReadyAt, Ready: j.Ready}
				if j.Father != nil {
					job.Father = adminLabSeed(*j.Father).label()
				}
				out.Jobs = append(out.Jobs, job)
			}
			for _, s := range lab.Seeds {
				out.Seeds = append(out.Seeds, adminLabSeed(s))
			}
			v.Lab = out
		}

		if c, msg := previewSection(ctx, g, func(ctx context.Context) (MushroomCellarView, error) {
			return a.MushroomCellar(ctx, playerID)
		}); !fail("cellar", msg) {
			out := &AdminCellar{Unlocked: c.Unlocked, UnlockLevel: c.UnlockLevel, Sawdust: c.Sawdust, Hourglasses: c.Hourglasses,
				Beds: []AdminCellarBed{}}
			for _, b := range c.Beds {
				out.Beds = append(out.Beds, AdminCellarBed{No: b.No, ReadyAt: b.ReadyAt, Sawdust: b.Sawdust, Ready: b.Ready})
			}
			v.Cellar = out
		}

		if ov, msg := previewSection(ctx, g, func(ctx context.Context) (ExplorationOverview, error) {
			return a.ExplorationOverview(ctx, playerID)
		}); !fail("exploration", msg) {
			out := &AdminExploration{Areas: []AdminArea{}}
			if tr := ov.Trip; tr != nil {
				out.Trip = &AdminTrip{AreaName: tr.AreaName, Emoji: tr.Emoji, Started: tr.StartedAt, EndsAt: tr.EndsAt, Finished: tr.Finished}
			}
			for _, ar := range ov.Areas {
				out.Areas = append(out.Areas, AdminArea{Name: ar.Name, Emoji: ar.Emoji, Unlocked: ar.Unlocked, UnlockLevel: ar.UnlockLevel,
					ExploreCount: ar.ExploreCount, NextAt: ar.NextAt})
			}
			v.Exploration = out
		}

		if ms, msg := previewSection(ctx, g, func(ctx context.Context) (MachinesStatus, error) {
			return a.MachinesStatus(ctx, playerID)
		}); !fail("machines", msg) {
			for _, m := range ms.Machines {
				v.Machines = append(v.Machines, AdminMachine{Name: m.Name, Emoji: m.Emoji, Unlocked: m.Unlocked, Owned: m.Owned, Enabled: m.Enabled})
			}
		}

		if d, msg := previewSection(ctx, g, func(ctx context.Context) (DecorView, error) { return a.Decorations(ctx, playerID) }); !fail("decor", msg) {
			for _, it := range d.Items {
				if it.Owned {
					v.Decor = append(v.Decor, AdminDecor{Name: it.Name, Emoji: it.Emoji, Tier: it.Tier.Emoji + " " + it.Tier.Name,
						Slot: decor.SlotName(it.Slot)})
				}
			}
		}

		if fb, msg := previewSection(ctx, g, func(ctx context.Context) (FishBasket, error) { return a.FishBasket(ctx, playerID) }); !fail("fish_basket", msg) {
			af := fb.AutoFish
			v.FishBasket = &AdminFishBasket{Count: len(fb.Fish), Capacity: fb.Capacity, TotalValue: fb.TotalValue,
				AutoFish: AdminAutoFish{Owned: af.Owned, Enabled: af.Enabled, AutoSell: af.AutoSell, Paused: af.Paused, NoBait: af.NoBait,
					NextAt: af.NextAt, SpeedLevel: af.SpeedLevel, BaitName: af.BaitName, BaitLeft: af.BaitLeft, Caught: af.Caught}}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return v, nil
}

// label 例如「小麥（生長 3／產量 5／品質 4／突變 2）」。
func (s AdminLabSeed) label() string {
	out := s.CropName + "（" + s.Genes + "）"
	if s.MutationName != "" {
		out += " " + s.MutationName
	}
	return out
}

// ---------- 群組：古代遺跡與日報 ----------

type AdminRuinsMaterial struct {
	Name    string `json:"name"`
	Need    int    `json:"need"`
	Donated int    `json:"donated"`
}

type AdminGuardian struct {
	Name     string          `json:"name"`
	Active   bool            `json:"active"`
	EndsAt   time.Time       `json:"ends_at"`
	HPMax    int             `json:"hp_max"`
	Damage   int             `json:"damage"`
	Defeated bool            `json:"defeated"`
	Top      []ruinsrepo.Hit `json:"top"`
}

type AdminRuins struct {
	Stage       int                     `json:"stage"`
	Stages      int                     `json:"stages"`
	StageName   *string                 `json:"stage_name"`
	Percent     int                     `json:"percent"`
	Completed   bool                    `json:"completed"`
	CompletedAt *time.Time              `json:"completed_at"`
	CollapsedAt *time.Time              `json:"collapsed_at"`
	Materials   []AdminRuinsMaterial    `json:"materials"`
	Top         []ruinsrepo.Contributor `json:"top"`
	Guardian    *AdminGuardian          `json:"guardian"`
}

type AdminNews struct {
	Day   time.Time `json:"day"`
	Empty bool      `json:"empty"`
	Text  string    `json:"text"`
}

// AdminGroupExtrasView 群組的古代遺跡與昨天的日報；沒有任何成員農場時 Ruins、News 為 nil 並在 Errors 說明。
type AdminGroupExtrasView struct {
	GroupID int64             `json:"group_id"`
	Ruins   *AdminRuins       `json:"ruins"`
	News    *AdminNews        `json:"news"`
	Errors  map[string]string `json:"errors"`
}

// AdminGroupExtras 不存在的群組回傳 nil。
func (a *AdminService) AdminGroupExtras(ctx context.Context, groupID int64) (*AdminGroupExtrasView, error) {
	var member int64
	found, hasMember := false, false
	if err := a.db.InTx(ctx, db.TxOptions{ReadOnly: true}, func(t *db.Tx) error {
		g, err := groups.Get(ctx, t, groupID)
		if err != nil || g == nil {
			return err
		}
		found = true
		// 遺跡的狀態以群組為單位，用任何一位主要群組是這個群組的玩家讀取即可（個人捐獻數量不顯示）
		member, hasMember, err = db.QueryValue[int64](ctx, t,
			"SELECT player_id FROM farms WHERE home_group_id = $1 ORDER BY id LIMIT 1", groupID)
		return err
	}); err != nil || !found {
		return nil, err
	}
	v := &AdminGroupExtrasView{GroupID: groupID, Errors: map[string]string{}}
	// 所有區塊共用一筆 preview 交易（每個區塊各自 SAVEPOINT，失敗不影響其他區塊）
	if err := a.preview(ctx, func(ctx context.Context) error {
		if !hasMember {
			v.Errors["ruins"] = "這個群組還沒有主要群組是它的農場，遺跡尚未開始。"
		} else if st, msg := previewSection(ctx, a.GameService, func(ctx context.Context) (RuinsStatus, error) {
			return a.RuinsStatus(ctx, member)
		}); msg != "" {
			v.Errors["ruins"] = msg
		} else {
			r := &AdminRuins{Stage: st.Stage, Stages: st.Stages, StageName: st.StageName, Percent: st.Percent, Completed: st.Completed,
				CompletedAt: st.CompletedAt, CollapsedAt: st.CollapsedAt, Materials: []AdminRuinsMaterial{}, Top: nonNil(st.Top)}
			for _, m := range st.Materials {
				r.Materials = append(r.Materials, AdminRuinsMaterial{Name: m.Name, Need: m.Need, Donated: m.Donated})
			}
			if gd := st.Guardian; gd != nil {
				r.Guardian = &AdminGuardian{Name: gd.Name, Active: gd.Active, EndsAt: gd.EndsAt, HPMax: gd.HPMax, Damage: gd.Damage,
					Defeated: gd.Defeated, Top: nonNil(gd.Top)}
			}
			v.Ruins = r
		}
		if n, msg := previewSection(ctx, a.GameService, func(ctx context.Context) (*DailyNews, error) {
			return a.DailyNews(ctx, groupID, time.Time{})
		}); msg != "" {
			v.Errors["news"] = msg
		} else if n != nil {
			v.News = &AdminNews{Day: n.Day, Empty: n.Empty, Text: n.Text}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return v, nil
}
