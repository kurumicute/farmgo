package services

// 限時增益道具與新寵物的共用規則（Go 版新增，2026-11-18）：
//   - 雨衣稻草人、灑水器、幸運香包、寵物零食：使用後在 farm_effects 記一筆限時效果（effect_code = 道具代碼），
//     有效期間再次使用會延長；各效果在對應的規則中判斷（暴風、澆水、播種與菇房、寵物加成）。
//   - 寵物加成：等級加成（pets.BonusPct），寵物零食生效時 ×1.5。

import (
	"context"
	"fmt"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/fishing"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
	"farmgm/internal/game/weather"
	"farmgm/internal/repositories/inventory"
	"farmgm/internal/repositories/items"
	"farmgm/internal/repositories/pets"

	petrules "farmgm/internal/game/pets"
)

// BoostView 增益道具頁的一項。
type BoostView struct {
	Code, Name, Emoji, Desc string
	Have                    int
	Price                   int        // 道具商店的價格；用加工做的為 0
	Recipe                  string     // 用加工做的：在哪裡、用什麼做（商店販售的為空）
	ActiveUntil             *time.Time // 效果中時的結束時間
}

// Boosts 增益道具的持有數量與效果狀態（只讀取不鎖）。
func (s *GameService) Boosts(ctx context.Context, playerID int64) ([]BoostView, error) {
	now := s.now()
	return inTx(ctx, s, func(t *db.Tx) ([]BoostView, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return nil, err
		}
		out := make([]BoostView, 0, len(balance.BoostOrder))
		for _, code := range balance.BoostOrder {
			b := balance.Boosts[code]
			v := BoostView{Code: code, Name: balance.BoostName(code), Emoji: b.Emoji, Desc: b.Desc}
			if v.Have, err = inventory.GetQuantity(ctx, t, farm.ID, code, "normal", "none"); err != nil {
				return nil, err
			}
			if _, ok := balance.Items[code]; ok {
				v.Price = balance.ItemPrice(code)
			} else if rc, ok := balance.Recipes[code]; ok {
				v.Recipe = txf(ctx, "在%s用%s製作", balance.Buildings[rc.Building].Name, recipeInputsText(rc))
			}
			e, err := items.GetEffect(ctx, t, farm.ID, code)
			if err != nil {
				return nil, err
			}
			if e != nil && !now.Before(e.StartedAt) && now.Before(e.ExpiresAt) {
				until := e.ExpiresAt
				v.ActiveUntil = &until
			}
			out = append(out, v)
		}
		return out, nil
	})
}

func recipeInputsText(rc balance.Recipe) string {
	text := ""
	for i, c := range rc.InputOrder {
		if i > 0 {
			text += "＋"
		}
		text += fmt.Sprintf("%s ×%d", ItemName(c, nil), rc.Inputs[c])
	}
	return text
}

// UseBoost 使用一個增益道具：扣 1 個，開始或延長效果。
func (s *GameService) UseBoost(ctx context.Context, playerID int64, code string) (ActionResult, error) {
	b, ok := balance.Boosts[code]
	if !ok {
		return ActionResult{}, gameerr.NotFoundf("沒有「%s」這種增益道具。", code)
	}
	name := balance.BoostName(code)
	duration := time.Duration(b.Hours) * time.Hour
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, code, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s。", name)
		}
		now := s.now()
		e, err := items.GetEffect(ctx, t, farm.ID, code)
		if err != nil {
			return ActionResult{}, err
		}
		action := "啟用"
		switch {
		case e != nil && !now.Before(e.StartedAt) && now.Before(e.ExpiresAt):
			e.ExpiresAt = e.ExpiresAt.Add(duration)
			action = "延長"
			_, err = db.UpdateByID(ctx, t, models.TableFarmEffect, e.ID, db.C("expires_at", e.ExpiresAt))
		case e != nil:
			e.StartedAt, e.ExpiresAt = now, now.Add(duration)
			_, err = db.UpdateByID(ctx, t, models.TableFarmEffect, e.ID, db.C("started_at", e.StartedAt), db.C("expires_at", e.ExpiresAt))
		default:
			e = &models.FarmEffect{FarmID: farm.ID, EffectCode: code, Multiplier: 1, StartedAt: now, ExpiresAt: now.Add(duration)}
			e.ID, err = db.InsertReturningID(ctx, t, models.TableFarmEffect, db.C("farm_id", e.FarmID), db.C("effect_code", e.EffectCode),
				db.C("multiplier", e.Multiplier), db.C("started_at", e.StartedAt), db.C("expires_at", e.ExpiresAt))
		}
		if err != nil {
			return ActionResult{}, err
		}
		until := taipeiFmt(e.ExpiresAt, "01-02 15:04")
		if err := s.record(ctx, t, farm, rules.EvUseItem, fmt.Sprintf("使用%s（%s），效果到 %s", name, action, until),
			rec{ItemCode: code, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		left := LongDuration(rules.RemainingSeconds(&e.ExpiresAt, now))
		return result(txf(ctx, "已%s%s：%s。效果剩餘 %s（到 %s）。", action, name, b.Desc, left, until), farm.Gold, 0, code, 1), nil
	})
}

// boostActive 增益道具的效果是否生效中（同一筆交易的逐格迴圈中只查一次）。
func (s *GameService) boostActive(ctx context.Context, t *db.Tx, farmID int64, code string, now time.Time) (bool, error) {
	return db.Memo(t, effectKey{farmID, code}, func() (bool, error) {
		return items.EffectActive(ctx, t, farmID, code, now)
	})
}

// petPct 寵物目前的加成 %（沒有這隻寵物時為 0）：等級加成，寵物零食生效時 ×1.5（最多 100%）。
func (s *GameService) petPct(ctx context.Context, t *db.Tx, farmID int64, code string) (int, error) {
	level, err := db.Memo(t, petKey{farmID, code}, func() (*int, error) { return pets.Level(ctx, t, farmID, code) })
	if err != nil || level == nil {
		return 0, err
	}
	pct := petrules.BonusPct(level)
	treat, err := s.boostActive(ctx, t, farmID, balance.ItemPetTreat, s.now())
	if err != nil {
		return 0, err
	}
	if treat {
		pct = balance.PetTreatBoost(pct)
	}
	return pct, nil
}

// fishEffects 魚餌效果加上當天天氣與小鳥的稀有度加成（2026-11-18；小鳥加成受寵物零食影響）。
func (s *GameService) fishEffects(ctx context.Context, t *db.Tx, farmID int64, at time.Time, bait fishing.Effects) (fishing.Effects, error) {
	pct, err := s.petPct(ctx, t, farmID, petrules.Bird)
	if err != nil {
		return nil, err
	}
	return withBird(weather.Today(at).FishEffects(bait), pct), nil
}

// withBird 加上小鳥的稀有度加成（回傳新的 map；pct 為 0 時原樣回傳）。
func withBird(effects fishing.Effects, pct int) fishing.Effects {
	if pct <= 0 {
		return effects
	}
	out := make(fishing.Effects, len(effects)+3)
	for k, v := range effects {
		out[k] = v
	}
	for r, bp := range petrules.BirdFishBP(pct) {
		out[r+"_bp"] += bp
	}
	return out
}
