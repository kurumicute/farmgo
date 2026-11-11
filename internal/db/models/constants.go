// Package models 資料表結構（對應 Python app/db/models.py）。只有資料結構，不含查詢。
//
// 資料表結構由 Python 專案的 Alembic Migration 管理，Go 版不建立或修改資料表。
//
// 約定：
//   - 每個 struct 的欄位以 `db` tag 對應實際欄位名稱，供 pgx.RowToStructByName 掃描；欄位順序同 Python。
//   - 主鍵／外鍵與 ID 欄位用 int64，其他整數用 int（64 位元平台上與 BIGINT 相容）。
//   - 可為 NULL 的欄位用指標；JSON 欄位依內容選具體型別，內容由規則套件解析時用 json.RawMessage。
//   - DATE 欄位以 time.Time 表示，pgx 讀回時是 UTC 00:00，repository 要轉成台灣日期（clock.TaipeiTZ 的 00:00）。
//   - CHECK 約束名稱依 Python 的 naming convention：ck_<表名>_<約束名>；UNIQUE 欄位（unique=True）
//     為 uq_<表名>_<欄位名>；其餘 UniqueConstraint／Index 名稱照 Python 寫的原樣。
//   - relationship 不轉成欄位，只在註解標出。
package models

// EventTypes 交易紀錄事件類型（game_transactions.event_type 的 CHECK 約束，順序同 Python EVENT_TYPES）。
var EventTypes = []string{
	"create_farm",
	"buy_seed",
	"plant",
	"harvest",
	"sell",
	"upgrade",
	"buy_item",
	"use_item",
	"water",
	"clear",
	"steal",
	"stolen",
	"bind_group",
	"steal_blocked",
	"defended",
	"defense_place",
	"build",
	"buy_animal",
	"feed_animal",
	"collect_animal",
	"craft_start",
	"craft_collect",
	"quest_reward",
	"competition_reward",
	"admin_adjust",
	"ranking_reward",
	"transfer_group",
	"gamble",
	"explore",
	"build_machine",
	"exchange",
	"make_seed",
	"fishing_buy",
	"fish_sell",
	"release_animal",
	"boss_donate",
	"boss_reward",
	"rename_farm",
	"checkin_reward",
	"achievement_reward",
	"ruins_donate",
	"ruins_guardian",
	"rumor_treasure",
}

// QualityCodes 作物品質代碼（inventory_items／farm_plots 等的 quality CHECK）。
var QualityCodes = []string{"normal", "good", "rare", "legendary"}

// MutationCodes 作物突變代碼（mutation CHECK）。
var MutationCodes = []string{"none", "giant", "golden", "fast", "variant"}

// PlotCropConsistency farm_plots 的 crop_consistency CHECK 條件。
const PlotCropConsistency = "(crop_type_id IS NULL AND planted_at IS NULL AND watered_at IS NULL AND ready_at IS NULL " +
	"AND remaining_quantity IS NULL AND NOT fertilized) OR " +
	"(crop_type_id IS NOT NULL AND planted_at IS NOT NULL AND remaining_quantity IS NOT NULL " +
	"AND (watered_at IS NULL) = (ready_at IS NULL) " +
	"AND (watered_at IS NULL OR (watered_at >= planted_at AND ready_at > watered_at)))"

// EventSchedules 限時事件的排程類型。
var EventSchedules = []string{"once", "daily", "weekly"}

// FishRarities 魚的稀有度（FISH_RARITY_SQL）。
var FishRarities = []string{"common", "rare", "epic", "legendary"}

// FishingStatuses 釣魚 Session 的狀態（FISHING_STATUSES_SQL）；casting／fighting 為進行中。
var FishingStatuses = []string{
	"casting", "fighting", "landed", "escaped", "line_broken", "slipped", "missed", "timeout", "abandoned",
}

// Table 每個模型都提供資料表名稱。
type Table interface {
	TableName() string
}
