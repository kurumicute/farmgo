package services

// 農場裝飾（Go 版新增，2026-11-15；目錄在 game/decor）：買一次永久擁有，擺在農田圖左右兩側的庭院（6 個位置），
// 只改外觀、沒有加成。資料在 farm_decorations（Migration 0042）。

import (
	"context"
	"fmt"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/decor"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/seasons"
	"farmgm/internal/numfmt"
)

// DecorItemView 一種裝飾與玩家的擁有狀態。
type DecorItemView struct {
	decor.Item
	Owned  bool
	Slot   int  // 擺放的位置（1–6）；0 = 收起來或沒有
	OnSale bool // 現在買得到（季節限定只在那一季）
}

// DecorView 庭院擺設頁。
type DecorView struct {
	Gold         int
	Items        []DecorItemView     // 依目錄順序；不在本季的限定裝飾只列出已擁有的（已絕版）
	Placed       [decor.Slots]string // 每個位置擺的裝飾代碼（"" = 空）
	Season       string              // 目前的季節（seasons.SeasonOf）
	SeasonEndsAt time.Time           // 本季限定下架的時間（下一次換季）
}

func farmDecorations(ctx context.Context, t *db.Tx, farmID int64) ([]models.FarmDecoration, error) {
	return db.QueryStructs[models.FarmDecoration](ctx, t, "SELECT * FROM farm_decorations WHERE farm_id = $1 ORDER BY id", farmID)
}

// placedDecor 每個位置擺的裝飾代碼（農田圖用；沒有擺任何東西時回傳 nil）。
func placedDecor(ctx context.Context, t *db.Tx, farmID int64) ([]string, error) {
	rows, err := db.QueryStructs[models.FarmDecoration](ctx, t,
		"SELECT * FROM farm_decorations WHERE farm_id = $1 AND slot IS NOT NULL", farmID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make([]string, decor.Slots)
	for _, r := range rows {
		if r.Slot != nil && *r.Slot >= 1 && *r.Slot <= decor.Slots {
			out[*r.Slot-1] = r.DecorCode
		}
	}
	return out, nil
}

// Decorations 庭院擺設頁的資料。
func (s *GameService) Decorations(ctx context.Context, playerID int64) (DecorView, error) {
	return inTx(ctx, s, func(t *db.Tx) (DecorView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return DecorView{}, err
		}
		rows, err := farmDecorations(ctx, t, farm.ID)
		if err != nil {
			return DecorView{}, err
		}
		owned := map[string]*models.FarmDecoration{}
		for i := range rows {
			owned[rows[i].DecorCode] = &rows[i]
		}
		now := s.now()
		v := DecorView{Gold: farm.Gold, Season: seasons.SeasonOf(now), SeasonEndsAt: seasons.NextChange(now)}
		for _, it := range decor.Catalog {
			iv := DecorItemView{Item: it, OnSale: it.OnSale(v.Season)}
			if _, has := owned[it.Code]; !has && !iv.OnSale {
				continue // 不在本季的限定裝飾：沒有的人看不到
			}
			if r, ok := owned[it.Code]; ok {
				iv.Owned = true
				if r.Slot != nil {
					iv.Slot = *r.Slot
					v.Placed[*r.Slot-1] = it.Code
				}
			}
			v.Items = append(v.Items, iv)
		}
		return v, nil
	})
}

// BuyDecor 購買一種裝飾（每種只能買一次）；有空位時自動擺到第一個空位。
func (s *GameService) BuyDecor(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	it, ok := decor.Find(code)
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有這種裝飾。")
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		rows, err := farmDecorations(ctx, t, farm.ID)
		if err != nil {
			return ActionResult{}, err
		}
		used := map[int]bool{}
		for _, r := range rows {
			if r.DecorCode == code {
				return ActionResult{}, gameerr.New(gameerr.AlreadyExists, "你已經有%s了。", it.Name)
			}
			if r.Slot != nil {
				used[*r.Slot] = true
			}
		}
		if season := seasons.SeasonOf(s.now()); !it.OnSale(season) { // 購買當下再檢查一次季節
			return ActionResult{}, gameerr.Validationf("%s是%s季限定，已經下架了，下次%s季再來。", it.Name,
				seasons.SeasonNames[it.Season], seasons.SeasonNames[it.Season])
		}
		if farm.Gold < it.Price {
			return ActionResult{}, gameerr.New(gameerr.InsufficientGold, "金幣不足：%s需要 %d，目前只有 %d。", it.Name, it.Price, farm.Gold)
		}
		farm.Gold -= it.Price
		if err := s.saveFarm(ctx, t, farm, db.C("gold", farm.Gold)); err != nil {
			return ActionResult{}, err
		}
		var slot any
		where := "，已收進庭院擺設（擺飾區滿了，到「🎨 庭院擺設」換位置）"
		for n := 1; n <= decor.Slots; n++ {
			if !used[n] {
				slot, where = n, fmt.Sprintf("，擺在庭院的%s", decor.SlotName(n))
				break
			}
		}
		if err := db.Insert(ctx, t, "farm_decorations", db.C("farm_id", farm.ID), db.C("decor_code", code), db.C("slot", slot),
			db.C("acquired_at", s.now())); err != nil {
			return ActionResult{}, err
		}
		kind := "裝飾"
		if it.Season != "" {
			kind = "限定裝飾"
		}
		if err := s.record(ctx, t, farm, rules.EvBuyItem, fmt.Sprintf("購買%s%s，金幣 -%d", kind, it.Name, it.Price),
			rec{ItemCode: "decor_" + code, Quantity: 1, GoldDelta: -it.Price}); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🎨 買下%s %s，花費 %s 金幣%s。", it.Emoji, it.Name, numfmt.Comma(it.Price), where),
			farm.Gold, -it.Price, "", 0), nil
	})
}

// PlaceDecor 把裝飾擺到某個位置（原本在那裡的裝飾收起來；這個裝飾原本在別的位置就搬過來）；code 為 "" 時把那個位置清空。
func (s *GameService) PlaceDecor(ctx context.Context, playerID int64, slot int, code string) (ActionResult, error) {
	if slot < 1 || slot > decor.Slots {
		return ActionResult{}, gameerr.Validationf("庭院只有 %d 個位置（左上、左中、左下、右上、右中、右下）。", decor.Slots)
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if code != "" {
			it, ok := decor.Find(code)
			if !ok {
				return ActionResult{}, gameerr.NotFoundf("沒有這種裝飾。")
			}
			var has bool
			if err := t.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM farm_decorations WHERE farm_id = $1 AND decor_code = $2)",
				farm.ID, code).Scan(&has); err != nil {
				return ActionResult{}, err
			}
			if !has {
				return ActionResult{}, gameerr.Validationf("你還沒有%s，先買下來才能擺。", it.Name)
			}
			if _, err := t.Exec(ctx, "UPDATE farm_decorations SET slot = NULL WHERE farm_id = $1 AND (slot = $2 OR decor_code = $3)",
				farm.ID, slot, code); err != nil {
				return ActionResult{}, err
			}
			if _, err := t.Exec(ctx, "UPDATE farm_decorations SET slot = $2 WHERE farm_id = $1 AND decor_code = $3",
				farm.ID, slot, code); err != nil {
				return ActionResult{}, err
			}
			return result(fmt.Sprintf("🎨 把%s %s擺到庭院的%s。", it.Emoji, it.Name, decor.SlotName(slot)), farm.Gold, 0, "", 0), nil
		}
		if _, err := t.Exec(ctx, "UPDATE farm_decorations SET slot = NULL WHERE farm_id = $1 AND slot = $2", farm.ID, slot); err != nil {
			return ActionResult{}, err
		}
		return result(fmt.Sprintf("🎨 庭院的%s已清空。", decor.SlotName(slot)), farm.Gold, 0, "", 0), nil
	})
}
