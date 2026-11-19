package services

// 加速沙漏（Go 版新增，2026-11-15；規則與數值在 game/hourglass）：消耗品，在種子實驗室的研究槽（雜交、突變研究、育苗）
// 或地下菇房使用，剩餘時間減少 CutPct%（預設減半）。剩餘時間太短（MinSeconds）時不能用，沙漏不扣。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/hourglass"
	"farmgm/internal/game/mushroom"
	"farmgm/internal/game/rules"
	"farmgm/internal/repositories/inventory"
	labrepo "farmgm/internal/repositories/seedlab"
)

// HourglassTarget 可以使用沙漏的工作（Key：lab:<研究槽>、cellar）。
type HourglassTarget struct {
	Key       string
	Label     string
	Remaining time.Duration
}

// HourglassTargets 正在計時、可以用沙漏的工作，與持有的沙漏數量。
func (s *GameService) HourglassTargets(ctx context.Context, playerID int64) ([]HourglassTarget, int, error) {
	type out struct {
		targets []HourglassTarget
		have    int
	}
	o, err := inTx(ctx, s, func(t *db.Tx) (out, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return out{}, err
		}
		have, err := inventory.GetQuantity(ctx, t, farm.ID, balance.ItemHourglass, "normal", "none")
		if err != nil {
			return out{}, err
		}
		targets, err := s.hourglassTargets(ctx, t, farm.ID, s.now(), false)
		return out{targets, have}, err
	})
	return o.targets, o.have, err
}

func labJobName(ctx context.Context, t *db.Tx, s *GameService, j models.LabJob) string {
	name := ""
	if seed, err := labrepo.GetSeed(ctx, t, j.MotherSeedID, false); err == nil && seed != nil {
		name = s.cropName(ctx, t, seed.CropCode)
	}
	switch j.Kind {
	case "cross":
		return "🧬 雜交 " + name
	case "clone":
		return "🌱 育苗 " + name
	}
	return "☢️ 突變研究 " + name
}

func (s *GameService) hourglassTargets(ctx context.Context, t *db.Tx, farmID int64, now time.Time, lock bool) ([]HourglassTarget, error) {
	var out []HourglassTarget
	jobs, err := labrepo.OpenJobs(ctx, t, farmID)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if left := j.ReadyAt.Sub(now); hourglass.Usable(left) {
			out = append(out, HourglassTarget{Key: fmt.Sprintf("lab:%d", j.Slot),
				Label: txf(ctx, "研究槽 #%d %s", j.Slot, labJobName(ctx, t, s, j)), Remaining: left})
		}
	}
	tool, err := cellarTool(ctx, t, farmID, lock)
	if err != nil {
		return nil, err
	}
	if tool != nil {
		beds, _ := mushroom.ParseBeds(tool.Settings, now)
		var longest time.Duration
		for _, b := range beds {
			if left := b.ReadyAt().Sub(now); hourglass.Usable(left) {
				longest = max(longest, left)
			}
		}
		if longest > 0 {
			out = append(out, HourglassTarget{Key: "cellar", Label: txt(ctx, "🍄 地下菇房（所有生長中的菇床）"), Remaining: longest})
		}
	}
	return out, nil
}

// UseHourglass 對一個工作使用 1 個加速沙漏：剩餘時間減少 CutPct%。
func (s *GameService) UseHourglass(ctx context.Context, playerID int64, key string) (ActionResult, error) {
	name := balance.Items[balance.ItemHourglass].Name
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.lockedFarm(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		now := s.now()
		var line string
		switch {
		case strings.HasPrefix(key, "lab:"):
			slot, err := strconv.Atoi(strings.TrimPrefix(key, "lab:"))
			if err != nil {
				return ActionResult{}, gameerr.Validationf("沒有這個研究槽。")
			}
			jobs, err := labrepo.OpenJobs(ctx, t, farm.ID)
			if err != nil {
				return ActionResult{}, err
			}
			var job *models.LabJob
			for i := range jobs {
				if jobs[i].Slot == slot {
					job = &jobs[i]
				}
			}
			if job == nil {
				return ActionResult{}, gameerr.Validationf("研究槽 #%d 是空的。", slot)
			}
			left := job.ReadyAt.Sub(now)
			if !hourglass.Usable(left) {
				return ActionResult{}, gameerr.Validationf("研究槽 #%d 已經完成或快完成了，不需要%s。", slot, name)
			}
			after := hourglass.Cut(left)
			if _, err := t.Exec(ctx, "UPDATE lab_jobs SET ready_at = $2 WHERE id = $1", job.ID, now.Add(after)); err != nil {
				return ActionResult{}, err
			}
			line = txf(ctx, "研究槽 #%d %s：剩餘 %s → %s", slot, labJobName(ctx, t, s, *job),
				rules.FormatDuration(int(left.Seconds())), rules.FormatDuration(int(after.Seconds())))
		case key == "cellar":
			tool, err := cellarTool(ctx, t, farm.ID, true)
			if err != nil {
				return ActionResult{}, err
			}
			if tool == nil {
				return ActionResult{}, gameerr.Validationf("還沒有地下菇房。")
			}
			beds, _ := mushroom.ParseBeds(tool.Settings, now)
			n, longest, longestAfter := 0, time.Duration(0), time.Duration(0)
			for i, b := range beds {
				left := b.ReadyAt().Sub(now)
				if !hourglass.Usable(left) {
					continue
				}
				after := hourglass.Cut(left)
				beds[i].StartedAt = b.StartedAt.Add(-(left - after)) // 完成時間 = 開始時間 + 生長時間：開始時間往前移
				n++
				if left > longest {
					longest, longestAfter = left, after
				}
			}
			if n == 0 {
				return ActionResult{}, gameerr.Validationf("菇床都已經長好或快長好了，不需要%s。", name)
			}
			if err := saveBeds(ctx, t, tool, beds); err != nil {
				return ActionResult{}, err
			}
			line = txf(ctx, "地下菇房 %d 個菇床：最久的剩餘 %s → %s", n,
				rules.FormatDuration(int(longest.Seconds())), rules.FormatDuration(int(longestAfter.Seconds())))
		default:
			return ActionResult{}, gameerr.Validationf("沒有這個工作。")
		}
		ok, err := inventory.Remove(ctx, t, farm.ID, balance.ItemHourglass, 1, "normal", "none")
		if err != nil {
			return ActionResult{}, err
		}
		if !ok {
			return ActionResult{}, gameerr.New(gameerr.InsufficientItems, "沒有%s，請先到商店購買。", name)
		}
		if err := s.record(ctx, t, farm, rules.EvUseItem, truncRunes("使用"+name+"："+line, 200),
			rec{ItemCode: balance.ItemHourglass, Quantity: 1}); err != nil {
			return ActionResult{}, err
		}
		return result(txf(ctx, "⏳ 使用%s，%s。", name, line), farm.Gold, 0, balance.ItemHourglass, 1), nil
	})
}
