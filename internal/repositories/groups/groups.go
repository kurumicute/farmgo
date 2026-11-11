// Package groups 群組與成員（對應 Python app/repositories/groups.py）。
package groups

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/repositories/inventory"
)

// Create 建立群組；discordGuildID 為 nil 表示 None。
func Create(ctx context.Context, t *db.Tx, name string, discordGuildID *string) (*models.Group, error) {
	return db.QueryStruct[models.Group](ctx, t,
		"INSERT INTO groups (name, discord_guild_id) VALUES ($1, $2) RETURNING *", name, discordGuildID)
}

// Get 依 ID 取得。
func Get(ctx context.Context, t *db.Tx, groupID int64) (*models.Group, error) {
	return db.QueryStruct[models.Group](ctx, t, "SELECT * FROM groups WHERE id = $1", groupID)
}

// GetByGuild 依 Discord 伺服器 ID 取得。
func GetByGuild(ctx context.Context, t *db.Tx, guildID string) (*models.Group, error) {
	return db.QueryStruct[models.Group](ctx, t, "SELECT * FROM groups WHERE discord_guild_id = $1 LIMIT 1", guildID)
}

// GroupCount (群組, 成員數, 指定玩家是否為成員)。
type GroupCount struct {
	Group       models.Group
	MemberCount int
	IsMember    bool
}

// ListWithCounts 回傳 (群組, 成員數, 指定玩家是否為成員)；playerID 為 nil 時 IsMember 一律 false。
func ListWithCounts(ctx context.Context, t *db.Tx, playerID *int64) ([]GroupCount, error) {
	rows, err := t.Query(ctx, `
		SELECT g.id, g.name, g.discord_guild_id, g.created_at, count(m.player_id),
		       coalesce(bool_or(m.player_id = $1), false)
		FROM groups g LEFT JOIN group_members m ON m.group_id = g.id
		GROUP BY g.id ORDER BY g.id`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupCount
	for rows.Next() {
		var r GroupCount
		g := &r.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.DiscordGuildID, &g.CreatedAt, &r.MemberCount, &r.IsMember); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// IsMember 是否為成員。
func IsMember(ctx context.Context, t *db.Tx, groupID, playerID int64) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t,
		"SELECT player_id FROM group_members WHERE group_id = $1 AND player_id = $2", groupID, playerID)
	return ok, err
}

// AddMember 加入成員；已是成員時不變並回傳 false（以 RETURNING 判斷是否真的新增）。
func AddMember(ctx context.Context, t *db.Tx, groupID, playerID int64) (bool, error) {
	_, ok, err := db.QueryValue[int64](ctx, t, `
		INSERT INTO group_members (group_id, player_id) VALUES ($1, $2)
		ON CONFLICT (group_id, player_id) DO NOTHING RETURNING player_id`, groupID, playerID)
	return ok, err
}

// RemoveMember 只移除成員關係，不影響玩家、農場或任何紀錄。
func RemoveMember(ctx context.Context, t *db.Tx, groupID, playerID int64) (bool, error) {
	tag, err := t.Exec(ctx, "DELETE FROM group_members WHERE group_id = $1 AND player_id = $2", groupID, playerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MemberFarm 成員的農場摘要（JSON 欄位名稱同 Python dict）。
type MemberFarm struct {
	FarmID            int64  `json:"farm_id"`
	FarmName          string `json:"farm_name"`
	Level             int    `json:"level"`
	Gold              int    `json:"gold"`
	WarehouseCapacity int    `json:"warehouse_capacity"`
	WarehouseUsed     int    `json:"warehouse_used"`
	Plots             int    `json:"plots"`
	Empty             int    `json:"empty"`
	WaitingWater      int    `json:"waiting_water"`
	Growing           int    `json:"growing"`
	Ready             int    `json:"ready"`
	Withered          int    `json:"withered"`
	HomeGroupID       *int64 `json:"home_group_id"`
	// 主要所屬群組就是此群組時才列入群組排行
	RankedInGroup bool `json:"ranked_in_group"`
}

// Member 群組成員與農場（沒有農場時 Farm 為 nil）。
type Member struct {
	PlayerID   int64       `json:"player_id"`
	PlayerName string      `json:"player_name"`
	JoinedAt   time.Time   `json:"joined_at"`
	Farm       *MemberFarm `json:"farm"`
}

// MembersWithFarms 成員與農場狀態統計（有農場的排前面，再依玩家 ID）。
func MembersWithFarms(ctx context.Context, t *db.Tx, groupID int64, now, witherCutoff time.Time) ([]Member, error) {
	rows, err := t.Query(ctx, `
		WITH plot_stats AS (
			SELECT farm_id,
			       count(*) AS plots,
			       count(*) FILTER (WHERE crop_type_id IS NULL) AS empty,
			       count(*) FILTER (WHERE crop_type_id IS NOT NULL AND watered_at IS NULL) AS waiting,
			       count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at > $2) AS growing,
			       count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at <= $2 AND ready_at > $3) AS ready,
			       count(*) FILTER (WHERE watered_at IS NOT NULL AND ready_at <= $3) AS withered
			FROM farm_plots GROUP BY farm_id
		)
		SELECT p.id, p.display_name, m.joined_at, f.id, f.name, f.level, f.gold, f.warehouse_capacity,
		       coalesce(inv.used, 0), ps.plots, ps.empty, ps.waiting, ps.growing, ps.ready, ps.withered, f.home_group_id
		FROM group_members m
		JOIN players p ON p.id = m.player_id
		LEFT JOIN farms f ON f.player_id = p.id
		LEFT JOIN plot_stats ps ON ps.farm_id = f.id
		LEFT JOIN `+inventory.WarehouseUsedByFarmSQL("$4")+` inv ON inv.farm_id = f.id
		WHERE m.group_id = $1
		ORDER BY CASE WHEN f.id IS NULL THEN 1 ELSE 0 END, p.id`,
		groupID, now, witherCutoff, inventory.BagCodes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var farmID, homeGroup *int64
		var farmName *string
		var level, gold, capacity *int
		var used int
		var plots, empty, waiting, growing, ready, withered *int
		if err := rows.Scan(&m.PlayerID, &m.PlayerName, &m.JoinedAt, &farmID, &farmName, &level, &gold, &capacity,
			&used, &plots, &empty, &waiting, &growing, &ready, &withered, &homeGroup); err != nil {
			return nil, err
		}
		if farmID != nil {
			m.Farm = &MemberFarm{
				FarmID: *farmID, FarmName: *farmName, Level: *level, Gold: *gold, WarehouseCapacity: *capacity,
				WarehouseUsed: used, Plots: deref(plots), Empty: deref(empty), WaitingWater: deref(waiting),
				Growing: deref(growing), Ready: deref(ready), Withered: deref(withered), HomeGroupID: homeGroup,
				RankedInGroup: homeGroup != nil && *homeGroup == groupID,
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
