package models

import "time"

// TableAidLog 送禮與互助紀錄（Go 版新增，Migration 0044）。
const TableAidLog = "aid_logs"

// AidLog 一次送禮或幫忙澆水；aid_day 是台灣日期（讀回是 UTC 00:00），用來計算每日上限。
//
// 約束：ck_aid_logs_kind_valid（gift／water）、ck_aid_logs_not_self、ck_aid_logs_quantity_non_negative；
// ix_aid_logs_sender_day、ix_aid_logs_receiver_day。
type AidLog struct {
	ID             int64     `db:"id"`
	Kind           string    `db:"kind"`
	SenderFarmID   int64     `db:"sender_farm_id"`
	ReceiverFarmID int64     `db:"receiver_farm_id"`
	GroupID        *int64    `db:"group_id"`
	ItemCode       *string   `db:"item_code"` // 送禮的物品；幫忙澆水為 NULL
	Quantity       int       `db:"quantity"`  // 送禮的數量；幫忙澆水是澆了幾格
	AidDay         time.Time `db:"aid_day"`
	CreatedAt      time.Time `db:"created_at"`
}

func (AidLog) TableName() string { return TableAidLog }
