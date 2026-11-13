package models

// 種子實驗室（Go 版新增，Migration 0039；Python 版沒有對應的模型）。

import (
	"encoding/json"
	"time"
)

const (
	TableSeedLab      = "seed_labs"
	TableLabSeed      = "lab_seeds"
	TableLabJob       = "lab_jobs"
	TableLabRecord    = "lab_records"
	TableLabSeedShare = "lab_seed_shares"
)

// 特殊種子的狀態（lab_seeds.status）。
const (
	LabSeedStored  = "stored"  // 在種子庫
	LabSeedBusy    = "busy"    // 培育中（雜交雙親或突變研究）
	LabSeedPlanted = "planted" // 已種到農田
	LabSeedUsed    = "used"    // 已消耗（雜交完成、收成或清除後）
)

// SeedLab 種子實驗室：每座農場一列（主鍵 farm_id）。
//
// 約束：ck_seed_labs_level_valid（1–10）、ck_seed_labs_exp_non_negative。
type SeedLab struct {
	FarmID  int64     `db:"farm_id"`
	Level   int       `db:"level"` // server_default 1
	Exp     int       `db:"exp"`   // server_default 0
	BuiltAt time.Time `db:"built_at"`
}

func (SeedLab) TableName() string { return TableSeedLab }

// LabSeed 特殊種子：每顆有 4 個基因（1–5 級）、世代與突變。farm_id 是目前擁有者，breeder_farm_id 是原育種者。
//
// 約束：ck_lab_seeds_genes_valid、ck_lab_seeds_generation_positive、ck_lab_seeds_mutation_valid、
// ck_lab_seeds_status_valid；ix_lab_seeds_farm_status（farm_id, status）。
type LabSeed struct {
	ID            int64     `db:"id"`
	FarmID        int64     `db:"farm_id"`
	BreederFarmID *int64    `db:"breeder_farm_id"`
	CropCode      string    `db:"crop_code"`
	GeneGrowth    int       `db:"gene_growth"`
	GeneYield     int       `db:"gene_yield"`
	GeneQuality   int       `db:"gene_quality"`
	GeneMutation  int       `db:"gene_mutation"`
	Generation    int       `db:"generation"`
	Mutation      string    `db:"mutation"`
	Status        string    `db:"status"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

func (LabSeed) TableName() string { return TableLabSeed }

// LabJob 研究槽中的工作：cross（雜交，有父本）或 mutate（突變研究，沒有父本）。collected_at 為 NULL 表示還沒領取。
//
// 約束：ck_lab_jobs_kind_valid、ck_lab_jobs_slot_positive、ck_lab_jobs_father_consistency；
// uq_lab_jobs_farm_slot_open（farm_id, slot；WHERE collected_at IS NULL，唯一索引）。
type LabJob struct {
	ID           int64          `db:"id"`
	FarmID       int64          `db:"farm_id"`
	Slot         int            `db:"slot"`
	Kind         string         `db:"kind"`
	MotherSeedID int64          `db:"mother_seed_id"`
	FatherSeedID *int64         `db:"father_seed_id"`
	Materials    map[string]any `db:"materials"` // JSON：{"resin": 1, "herb": 1, "catalyst": "yield", "ancient_core": 1}
	StartedAt    time.Time      `db:"started_at"`
	ReadyAt      time.Time      `db:"ready_at"`
	CollectedAt  *time.Time     `db:"collected_at"`
}

func (LabJob) TableName() string { return TableLabJob }

// LabRecord 培育紀錄（鑑定、雜交、突變研究）。mother／father 是當時的種子快照 JSON。
//
// 約束：ck_lab_records_kind_valid；ix_lab_records_farm_id（farm_id, id）。
type LabRecord struct {
	ID           int64           `db:"id"`
	FarmID       int64           `db:"farm_id"`
	Kind         string          `db:"kind"`
	Mother       json.RawMessage `db:"mother"` // NULL = nil
	Father       json.RawMessage `db:"father"` // NULL = nil
	ResultSeedID *int64          `db:"result_seed_id"`
	CropCode     string          `db:"crop_code"`
	Genes        json.RawMessage `db:"genes"`
	Generation   int             `db:"generation"`
	Mutation     string          `db:"mutation"`
	Success      bool            `db:"success"`
	CreatedAt    time.Time       `db:"created_at"`
}

func (LabRecord) TableName() string { return TableLabRecord }

// LabSeedShare 分享種子紀錄；share_day 是台灣日期（讀回是 UTC 00:00）。
//
// 約束：ck_lab_seed_shares_not_self；ix_lab_seed_shares_sender_day、ix_lab_seed_shares_receiver_day。
type LabSeedShare struct {
	ID             int64     `db:"id"`
	SenderFarmID   int64     `db:"sender_farm_id"`
	ReceiverFarmID int64     `db:"receiver_farm_id"`
	LabSeedID      *int64    `db:"lab_seed_id"`
	ShareDay       time.Time `db:"share_day"`
	CreatedAt      time.Time `db:"created_at"`
}

func (LabSeedShare) TableName() string { return TableLabSeedShare }
