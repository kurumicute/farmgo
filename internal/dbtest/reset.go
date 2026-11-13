package dbtest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"farmgm/internal/db"
)

// GameTables 每個測試前清空的遊戲資料表（對應 Python tests/conftest.py 的 GAME_TABLES，另加 crop_genetics_settings）。
// 新增資料表時記得加入。
var GameTables = []string{
	"world_boss_reward_claims", "world_boss_contributions", "world_boss_supplies", "world_boss_events",
	"lab_seed_shares", "lab_records", "lab_jobs", "seed_labs", "lab_seeds", // 種子實驗室（Migration 0039）
	"rumor_digs", "ruins_guardian_hits", "ruins_guardians", "ruins_contributions", "group_ruins",
	"world_boss_schedules", "daily_checkins", "farm_codex", "farm_achievements", "farm_titles", "weekly_shop_purchases",
	"ranking_awards", "ranking_settlements", "ranking_reward_rules", "game_events",
	"competition_awards", "competition_results", "competition_contributions", "competitions",
	"quest_claims", "quest_contributions", "quests", "admin_actions",
	"exploration_logs", "farm_explorations",
	"fish_shares", "farm_fish", "fishing_sessions", "farm_fishing_gear", "fishing_settings", "fishing_guards",
	"craft_orders", "farm_animals", "farm_buildings", "farm_defenses", "steal_records", "group_members", "groups",
	"gamble_prizes", "farm_daily_stats", "farm_stats", "farm_effects", "farm_tools", "game_transactions", "inventory_items",
	"farm_feedback", "farm_plots", "farms",
	"players", "feature_settings", "crop_genetics_settings",
}

var (
	resetOnce sync.Once
	resetSQL  string
	resetErr  error
)

// buildResetSQL 依資料庫的外鍵決定 DELETE 順序（被參照的表最後刪），再把序號歸 1（等同 TRUNCATE … RESTART IDENTITY，但快很多）。
func buildResetSQL(ctx context.Context, d *db.DB) (string, error) {
	in := map[string]bool{}
	for _, t := range GameTables {
		in[t] = true
	}
	rows, err := d.Pool.Query(ctx, `
		SELECT c.conrelid::regclass::text, c.confrelid::regclass::text
		FROM pg_constraint c WHERE c.contype = 'f' AND c.connamespace = current_schema()::regnamespace`)
	if err != nil {
		return "", err
	}
	refs := map[string][]string{} // 子表 → 被參照的表
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return "", err
		}
		child, parent = strings.Trim(child, `"`), strings.Trim(parent, `"`)
		if in[child] && in[parent] && child != parent {
			refs[child] = append(refs[child], parent)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	// 拓撲排序：沒有被其他未刪除表參照的先刪
	remaining := map[string]bool{}
	for t := range in {
		remaining[t] = true
	}
	var order []string
	for len(remaining) > 0 {
		var ready []string
		for t := range remaining {
			referenced := false
			for child := range remaining {
				for _, p := range refs[child] {
					if p == t {
						referenced = true
					}
				}
			}
			if !referenced {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			return "", fmt.Errorf("外鍵有循環，無法決定清空順序")
		}
		sort.Strings(ready)
		for _, t := range ready {
			order = append(order, t)
			delete(remaining, t)
		}
	}
	seqs, err := db.QueryValues[string](ctx, d.Pool, `
		SELECT pg_get_serial_sequence(quote_ident(table_name), column_name) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ANY($1)
		AND (is_identity = 'YES' OR column_default LIKE 'nextval%')`, GameTables)
	if err != nil {
		return "", err
	}
	stmts := make([]string, 0, len(order)+1)
	for _, t := range order {
		stmts = append(stmts, "DELETE FROM "+t)
	}
	if len(seqs) > 0 {
		parts := make([]string, len(seqs))
		for i, s := range seqs {
			parts[i] = fmt.Sprintf("setval('%s', 1, false)", s)
		}
		stmts = append(stmts, "SELECT "+strings.Join(parts, ", "))
	}
	return strings.Join(stmts, "; "), nil
}

// Reset 清空遊戲資料表並清空目錄快取、關閉目錄快取（測試預設）。作物／動物等目錄的重設在服務層測試夾具處理。
func Reset(t testing.TB, d *db.DB) {
	t.Helper()
	ctx := context.Background()
	resetOnce.Do(func() { resetSQL, resetErr = buildResetSQL(ctx, d) })
	if resetErr != nil {
		t.Fatal(resetErr)
	}
	if _, err := d.Pool.Exec(ctx, resetSQL); err != nil {
		t.Fatal(err)
	}
	db.CatalogTTL = 0
	db.CatalogClear()
}
