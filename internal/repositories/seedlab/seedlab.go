// Package seedlab 種子實驗室的資料存取（Go 版新增，Migration 0039）。呼叫端負責鎖定農場列與交易。
package seedlab

import (
	"context"
	"encoding/json"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/db/models"
	rules "farmgm/internal/game/seedlab"
)

// Get 取得農場的實驗室；還沒建造時回傳 nil。forUpdate 時鎖定該列。
func Get(ctx context.Context, t *db.Tx, farmID int64, forUpdate bool) (*models.SeedLab, error) {
	sql := "SELECT * FROM seed_labs WHERE farm_id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.SeedLab](ctx, t, sql, farmID)
}

// Create 建立實驗室（Lv.1）。
func Create(ctx context.Context, t *db.Tx, farmID int64, now time.Time) error {
	return db.Insert(ctx, t, models.TableSeedLab, db.C("farm_id", farmID), db.C("built_at", now))
}

// AddExp 增加研究經驗並依經驗更新等級；回傳新的等級。
func AddExp(ctx context.Context, t *db.Tx, lab *models.SeedLab, exp int) (int, error) {
	lab.Exp += exp
	lab.Level = max(lab.Level, rules.LevelFor(lab.Exp))
	_, err := t.Exec(ctx, "UPDATE seed_labs SET exp = $2, level = $3 WHERE farm_id = $1", lab.FarmID, lab.Exp, lab.Level)
	return lab.Level, err
}

// ---------- 特殊種子 ----------

// NewSeed 新增特殊種子的內容。
type NewSeed struct {
	FarmID        int64
	BreederFarmID int64
	CropCode      string
	Genes         rules.Genes
	Generation    int
	Mutation      string
	Status        string
}

// AddSeed 新增一顆特殊種子，回傳 ID。
func AddSeed(ctx context.Context, t *db.Tx, s NewSeed, now time.Time) (int64, error) {
	status := s.Status
	if status == "" {
		status = models.LabSeedStored
	}
	mutation := s.Mutation
	if mutation == "" {
		mutation = "none"
	}
	return db.InsertReturningID(ctx, t, models.TableLabSeed,
		db.C("farm_id", s.FarmID), db.C("breeder_farm_id", s.BreederFarmID), db.C("crop_code", s.CropCode),
		db.C("gene_growth", s.Genes.Growth), db.C("gene_yield", s.Genes.Yield), db.C("gene_quality", s.Genes.Quality),
		db.C("gene_mutation", s.Genes.Mutation), db.C("generation", max(1, s.Generation)), db.C("mutation", mutation),
		db.C("status", status), db.C("created_at", now), db.C("updated_at", now))
}

// GetSeed 依 ID 取得種子（不限擁有者；呼叫端要檢查 FarmID）。forUpdate 時鎖定該列。
func GetSeed(ctx context.Context, t *db.Tx, id int64, forUpdate bool) (*models.LabSeed, error) {
	sql := "SELECT * FROM lab_seeds WHERE id = $1"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	return db.QueryStruct[models.LabSeed](ctx, t, sql, id)
}

// ListSeeds 農場在種子庫與培育中的種子（依 ID）。
func ListSeeds(ctx context.Context, t *db.Tx, farmID int64) ([]models.LabSeed, error) {
	return db.QueryStructs[models.LabSeed](ctx, t,
		"SELECT * FROM lab_seeds WHERE farm_id = $1 AND status IN ('stored', 'busy') ORDER BY id", farmID)
}

// CountLibrary 種子庫（含培育中）的數量，用來檢查上限。
func CountLibrary(ctx context.Context, t *db.Tx, farmID int64) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t,
		"SELECT count(*)::int FROM lab_seeds WHERE farm_id = $1 AND status IN ('stored', 'busy')", farmID)
	return n, err
}

// SetStatus 更新種子狀態。
func SetStatus(ctx context.Context, t *db.Tx, id int64, status string, now time.Time) error {
	_, err := t.Exec(ctx, "UPDATE lab_seeds SET status = $2, updated_at = $3 WHERE id = $1", id, status, now)
	return err
}

// SetMutation 更新種子的突變。
func SetMutation(ctx context.Context, t *db.Tx, id int64, mutation string, now time.Time) error {
	_, err := t.Exec(ctx, "UPDATE lab_seeds SET mutation = $2, updated_at = $3 WHERE id = $1", id, mutation, now)
	return err
}

// Transfer 把種子轉給另一座農場（分享）。
func Transfer(ctx context.Context, t *db.Tx, id, toFarmID int64, now time.Time) error {
	_, err := t.Exec(ctx, "UPDATE lab_seeds SET farm_id = $2, updated_at = $3 WHERE id = $1", id, toFarmID, now)
	return err
}

// GenesOf 種子的基因。
func GenesOf(s *models.LabSeed) rules.Genes {
	return rules.Genes{Growth: s.GeneGrowth, Yield: s.GeneYield, Quality: s.GeneQuality, Mutation: s.GeneMutation}
}

// ---------- 研究槽 ----------

// OpenJobs 還沒領取的工作（依研究槽）。
func OpenJobs(ctx context.Context, t *db.Tx, farmID int64) ([]models.LabJob, error) {
	return db.QueryStructs[models.LabJob](ctx, t,
		"SELECT * FROM lab_jobs WHERE farm_id = $1 AND collected_at IS NULL ORDER BY slot", farmID)
}

// AddJob 新增工作。materials 為可序列化成 JSON 的 map。
func AddJob(ctx context.Context, t *db.Tx, farmID int64, slot int, kind string, motherID int64, fatherID *int64,
	materials map[string]any, startedAt, readyAt time.Time) (int64, error) {
	return db.InsertReturningID(ctx, t, models.TableLabJob,
		db.C("farm_id", farmID), db.C("slot", slot), db.C("kind", kind), db.C("mother_seed_id", motherID),
		db.C("father_seed_id", fatherID), db.C("materials", materials), db.C("started_at", startedAt), db.C("ready_at", readyAt))
}

// CollectJob 標記工作已領取。
func CollectJob(ctx context.Context, t *db.Tx, id int64, now time.Time) error {
	_, err := t.Exec(ctx, "UPDATE lab_jobs SET collected_at = $2 WHERE id = $1 AND collected_at IS NULL", id, now)
	return err
}

// ---------- 培育紀錄 ----------

// Snapshot 種子快照（培育紀錄的 mother／father）。
type Snapshot struct {
	ID         int64       `json:"id"`
	CropCode   string      `json:"crop_code"`
	Genes      rules.Genes `json:"genes"`
	Generation int         `json:"generation"`
	Mutation   string      `json:"mutation"`
}

// SnapshotOf 種子的快照。
func SnapshotOf(s *models.LabSeed) Snapshot {
	return Snapshot{s.ID, s.CropCode, GenesOf(s), s.Generation, s.Mutation}
}

// NewRecord 新增培育紀錄的內容。
type NewRecord struct {
	FarmID       int64
	Kind         string // identify／cross／mutate
	Mother       *Snapshot
	Father       *Snapshot
	ResultSeedID *int64
	CropCode     string
	Genes        rules.Genes
	Generation   int
	Mutation     string
	Success      bool
}

func jsonOrNil(v *Snapshot) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	return json.RawMessage(b), err
}

// AddRecord 新增培育紀錄。
func AddRecord(ctx context.Context, t *db.Tx, r NewRecord, now time.Time) error {
	mother, err := jsonOrNil(r.Mother)
	if err != nil {
		return err
	}
	father, err := jsonOrNil(r.Father)
	if err != nil {
		return err
	}
	genes, err := json.Marshal(r.Genes)
	if err != nil {
		return err
	}
	return db.Insert(ctx, t, models.TableLabRecord,
		db.C("farm_id", r.FarmID), db.C("kind", r.Kind), db.C("mother", mother), db.C("father", father),
		db.C("result_seed_id", r.ResultSeedID), db.C("crop_code", r.CropCode), db.C("genes", json.RawMessage(genes)),
		db.C("generation", max(1, r.Generation)), db.C("mutation", r.Mutation), db.C("success", r.Success),
		db.C("created_at", now))
}

// Records 最近的培育紀錄（新的在前）。
func Records(ctx context.Context, t *db.Tx, farmID int64, limit int) ([]models.LabRecord, error) {
	return db.QueryStructs[models.LabRecord](ctx, t,
		"SELECT * FROM lab_records WHERE farm_id = $1 ORDER BY id DESC LIMIT $2", farmID, limit)
}

// ---------- 分享 ----------

// SentCount 某天送出的數量。
func SentCount(ctx context.Context, t *db.Tx, farmID int64, day time.Time) (int, error) {
	n, _, err := db.QueryValue[int](ctx, t,
		"SELECT count(*)::int FROM lab_seed_shares WHERE sender_farm_id = $1 AND share_day = $2", farmID, db.DateParam(day))
	return n, err
}

// SendersTo 某天分享給這座農場的玩家（農場 ID，不重複）。
func SendersTo(ctx context.Context, t *db.Tx, farmID int64, day time.Time) ([]int64, error) {
	return db.QueryValues[int64](ctx, t,
		"SELECT DISTINCT sender_farm_id FROM lab_seed_shares WHERE receiver_farm_id = $1 AND share_day = $2 ORDER BY 1",
		farmID, db.DateParam(day))
}

// AddShare 新增分享紀錄。
func AddShare(ctx context.Context, t *db.Tx, senderFarmID, receiverFarmID, seedID int64, day, now time.Time) error {
	return db.Insert(ctx, t, models.TableLabSeedShare,
		db.C("sender_farm_id", senderFarmID), db.C("receiver_farm_id", receiverFarmID), db.C("lab_seed_id", seedID),
		db.C("share_day", db.DateParam(day)), db.C("created_at", now))
}

// ShareTarget 可以分享的對象（同群組、有農場；沒有實驗室也能收）。
type ShareTarget struct {
	PlayerID   int64  `db:"player_id"`
	PlayerName string `db:"display_name"`
	FarmID     int64  `db:"farm_id"`
	FarmName   string `db:"farm_name"`
}

// ShareTargets 群組中有農場的其他玩家（依玩家名稱）。
func ShareTargets(ctx context.Context, t *db.Tx, groupID, exceptPlayerID int64) ([]ShareTarget, error) {
	return db.QueryStructs[ShareTarget](ctx, t, `
		SELECT p.id AS player_id, p.display_name, f.id AS farm_id, f.name AS farm_name
		FROM group_members m
		JOIN players p ON p.id = m.player_id
		JOIN farms f ON f.player_id = p.id
		WHERE m.group_id = $1 AND m.player_id <> $2
		ORDER BY p.display_name, p.id`, groupID, exceptPlayerID)
}
