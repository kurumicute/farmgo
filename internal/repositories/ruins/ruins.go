// Package ruins 古代遺跡：群組的修復進度（group_ruins）、成員的捐獻統計（ruins_contributions）與每週的守護者
// （對應 Python app/repositories/ruins.py）。
//
// 日期參數（week）是台灣日期（game 套件慣例，台灣 00:00 的 time.Time）；讀回的 week_start 也轉成台灣日期。
package ruins

import (
	"context"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
)

const ruinsCols = "group_id, stage, progress, completed_at, updated_at, guardian_settled, collapsed_at"

const guardianCols = "group_id, week_start, hp_max, damage, defeated_at"

func normRuins(r *models.GroupRuins) *models.GroupRuins {
	if r != nil {
		r.GuardianSettled = db.TaipeiDatePtr(r.GuardianSettled)
		if r.Progress == nil {
			r.Progress = map[string]int{}
		}
	}
	return r
}

// Get forUpdate 預設 false（Python 加鎖時用 populate_existing 覆蓋先前讀過的物件；Go 每次都是新查詢）。
func Get(ctx context.Context, t *db.Tx, groupID int64, forUpdate bool) (*models.GroupRuins, error) {
	sql := "SELECT " + ruinsCols + " FROM group_ruins WHERE group_id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	r, err := db.QueryStruct[models.GroupRuins](ctx, t, sql, groupID)
	return normRuins(r), err
}

// GetOrCreate 取得並鎖定群組的遺跡列（第一次捐獻時建立；並行建立只會成功一筆）。
func GetOrCreate(ctx context.Context, t *db.Tx, groupID int64, now time.Time) (*models.GroupRuins, error) {
	if _, err := t.Exec(ctx, "INSERT INTO group_ruins (group_id, stage, progress, updated_at) VALUES ($1, 0, '{}', $2) "+
		"ON CONFLICT (group_id) DO NOTHING", groupID, now); err != nil {
		return nil, err
	}
	return Get(ctx, t, groupID, true)
}

// Save 寫回遺跡列的可變欄位（Python 由服務層修改 ORM 物件後 flush）。
func Save(ctx context.Context, t *db.Tx, r *models.GroupRuins) error {
	progress := r.Progress
	if progress == nil {
		progress = map[string]int{}
	}
	_, err := db.UpdateWhere(ctx, t, models.TableGroupRuins, []db.Col{
		db.C("stage", r.Stage), db.C("progress", progress), db.C("completed_at", r.CompletedAt),
		db.C("updated_at", r.UpdatedAt), db.C("guardian_settled", dateParamPtr(r.GuardianSettled)),
		db.C("collapsed_at", r.CollapsedAt),
	}, "group_id", r.GroupID)
	return err
}

func dateParamPtr(d *time.Time) *time.Time {
	if d == nil {
		return nil
	}
	v := db.DateParam(*d)
	return &v
}

// IsCompleted groupID 為 nil 時 false。
func IsCompleted(ctx context.Context, t *db.Tx, groupID *int64) (bool, error) {
	if groupID == nil {
		return false, nil
	}
	v, ok, err := db.QueryValue[*time.Time](ctx, t, "SELECT completed_at FROM group_ruins WHERE group_id = $1", *groupID)
	return ok && v != nil, err
}

// AddContribution 累加捐獻數量（upsert）。
func AddContribution(ctx context.Context, t *db.Tx, groupID, farmID int64, quantity int, now time.Time) error {
	_, err := t.Exec(ctx, "INSERT INTO ruins_contributions (group_id, farm_id, quantity, updated_at) VALUES ($1, $2, $3, $4) "+
		"ON CONFLICT (group_id, farm_id) DO UPDATE SET quantity = ruins_contributions.quantity + EXCLUDED.quantity, "+
		"updated_at = EXCLUDED.updated_at", groupID, farmID, quantity, now)
	return err
}

func Contribution(ctx context.Context, t *db.Tx, groupID, farmID int64) (int, error) {
	v, _, err := db.QueryValue[int](ctx, t,
		"SELECT quantity FROM ruins_contributions WHERE group_id = $1 AND farm_id = $2", groupID, farmID)
	return v, err
}

// Contributor 捐獻排行的一列。
type Contributor struct {
	Name     string  `db:"name"`
	Title    *string `db:"title"`
	Quantity int     `db:"quantity"`
}

// TopContributors (農場名稱, 稱號, 捐獻數量)，依數量排序。limit 預設 10。
func TopContributors(ctx context.Context, t *db.Tx, groupID int64, limit int) ([]Contributor, error) {
	return db.QueryStructs[Contributor](ctx, t,
		"SELECT f.name, f.title, c.quantity FROM ruins_contributions c JOIN farms f ON f.id = c.farm_id "+
			"WHERE c.group_id = $1 AND c.quantity > 0 ORDER BY c.quantity DESC, f.id LIMIT $2", groupID, limit)
}

func ContributorFarms(ctx context.Context, t *db.Tx, groupID int64) ([]int64, error) {
	return db.QueryValues[int64](ctx, t,
		"SELECT farm_id FROM ruins_contributions WHERE group_id = $1 AND quantity > 0", groupID)
}

// ---------- 守護者 ----------

// MemberFarms 所屬群組是 groupID 的農場數（守護者 HP 用）。
func MemberFarms(ctx context.Context, t *db.Tx, groupID int64) (int, error) {
	v, _, err := db.QueryValue[int](ctx, t, "SELECT count(*) FROM farms WHERE home_group_id = $1", groupID)
	return v, err
}

func normGuardian(g *models.RuinsGuardian) *models.RuinsGuardian {
	if g != nil {
		g.WeekStart = db.TaipeiDate(g.WeekStart)
	}
	return g
}

// GetGuardian forUpdate 預設 false。
func GetGuardian(ctx context.Context, t *db.Tx, groupID int64, week time.Time, forUpdate bool) (*models.RuinsGuardian, error) {
	sql := "SELECT " + guardianCols + " FROM ruins_guardians WHERE group_id = $1 AND week_start = $2"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	g, err := db.QueryStruct[models.RuinsGuardian](ctx, t, sql, groupID, db.DateParam(week))
	return normGuardian(g), err
}

// EnsureGuardian 取得並鎖定這週的守護者（第一次攻擊時建立，HP 以當下的成員數決定；並行建立只會成功一筆）。
func EnsureGuardian(ctx context.Context, t *db.Tx, groupID int64, week time.Time, hpMax int) (*models.RuinsGuardian, error) {
	if _, err := t.Exec(ctx, "INSERT INTO ruins_guardians (group_id, week_start, hp_max, damage) VALUES ($1, $2, $3, 0) "+
		"ON CONFLICT (group_id, week_start) DO NOTHING", groupID, db.DateParam(week), hpMax); err != nil {
		return nil, err
	}
	return GetGuardian(ctx, t, groupID, week, true)
}

// SaveGuardian 寫回守護者的傷害與打倒時間（Python 由服務層修改 ORM 物件後 flush）。
func SaveGuardian(ctx context.Context, t *db.Tx, g *models.RuinsGuardian) error {
	_, err := t.Exec(ctx, "UPDATE ruins_guardians SET damage = $3, defeated_at = $4 WHERE group_id = $1 AND week_start = $2",
		g.GroupID, db.DateParam(g.WeekStart), g.Damage, g.DefeatedAt)
	return err
}

// AddHit 累加這座農場這週對守護者的傷害（upsert）。
func AddHit(ctx context.Context, t *db.Tx, groupID int64, week time.Time, farmID int64, damage int) error {
	_, err := t.Exec(ctx, "INSERT INTO ruins_guardian_hits (group_id, week_start, farm_id, damage) VALUES ($1, $2, $3, $4) "+
		"ON CONFLICT (group_id, week_start, farm_id) DO UPDATE SET damage = ruins_guardian_hits.damage + EXCLUDED.damage",
		groupID, db.DateParam(week), farmID, damage)
	return err
}

func MyHits(ctx context.Context, t *db.Tx, groupID int64, week time.Time, farmID int64) (int, error) {
	v, _, err := db.QueryValue[int](ctx, t,
		"SELECT damage FROM ruins_guardian_hits WHERE group_id = $1 AND week_start = $2 AND farm_id = $3",
		groupID, db.DateParam(week), farmID)
	return v, err
}

// Hit 守護者傷害排行的一列。
type Hit struct {
	Name   string `db:"name"`
	Damage int    `db:"damage"`
}

// TopHits limit 預設 5。
func TopHits(ctx context.Context, t *db.Tx, groupID int64, week time.Time, limit int) ([]Hit, error) {
	return db.QueryStructs[Hit](ctx, t,
		"SELECT f.name, h.damage FROM ruins_guardian_hits h JOIN farms f ON f.id = h.farm_id "+
			"WHERE h.group_id = $1 AND h.week_start = $2 ORDER BY h.damage DESC, f.id LIMIT $3",
		groupID, db.DateParam(week), limit)
}
