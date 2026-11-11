package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"farmgm/internal/clock"
)

// QueryStructs 查詢多列並依 `db` tag 對應到 struct（欄位名稱必須與 SELECT 的欄位相同）。
func QueryStructs[T any](ctx context.Context, q Q, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[T])
}

// QueryStruct 查詢單列；沒有資料時回傳 nil, nil。
func QueryStruct[T any](ctx context.Context, q Q, sql string, args ...any) (*T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	v, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByName[T])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// QueryValues 查詢單一欄位的多列。
func QueryValues[T any](ctx context.Context, q Q, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[T])
}

// QueryValue 查詢單一值；沒有資料時 ok = false。
func QueryValue[T any](ctx context.Context, q Q, sql string, args ...any) (v T, ok bool, err error) {
	err = q.QueryRow(ctx, sql, args...).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

// Col 欄位與值（欄位名稱只能是程式中的常數，不可來自使用者輸入）。
type Col struct {
	Name  string
	Value any
}

// C 建立 Col。
func C(name string, value any) Col { return Col{name, value} }

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func ident(name string) string {
	if !identRe.MatchString(name) {
		panic(fmt.Sprintf("db: 不合法的識別名稱 %q", name))
	}
	return name
}

// UpdateByID UPDATE table SET … WHERE id = $n；回傳影響的列數。
func UpdateByID(ctx context.Context, q Q, table string, id int64, cols ...Col) (int64, error) {
	return UpdateWhere(ctx, q, table, cols, "id", id)
}

// UpdateWhere UPDATE table SET … WHERE keyCol = $n。
func UpdateWhere(ctx context.Context, q Q, table string, cols []Col, keyCol string, key any) (int64, error) {
	if len(cols) == 0 {
		return 0, nil
	}
	var b strings.Builder
	args := make([]any, 0, len(cols)+1)
	fmt.Fprintf(&b, "UPDATE %s SET ", ident(table))
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		args = append(args, c.Value)
		fmt.Fprintf(&b, "%s = $%d", ident(c.Name), len(args))
	}
	args = append(args, key)
	fmt.Fprintf(&b, " WHERE %s = $%d", ident(keyCol), len(args))
	tag, err := q.Exec(ctx, b.String(), args...)
	return tag.RowsAffected(), err
}

// InsertSQL 產生 INSERT INTO table (…) VALUES (…) [RETURNING …] 與參數。
func InsertSQL(table string, cols []Col, returning string) (string, []any) {
	var names, ph []string
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		names = append(names, ident(c.Name))
		args = append(args, c.Value)
		ph = append(ph, fmt.Sprintf("$%d", len(args)))
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", ident(table), strings.Join(names, ", "), strings.Join(ph, ", "))
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// InsertReturningID INSERT … RETURNING id。
func InsertReturningID(ctx context.Context, q Q, table string, cols ...Col) (int64, error) {
	sql, args := InsertSQL(table, cols, "id")
	var id int64
	err := q.QueryRow(ctx, sql, args...).Scan(&id)
	return id, err
}

// Insert INSERT（不回傳）。
func Insert(ctx context.Context, q Q, table string, cols ...Col) error {
	sql, args := InsertSQL(table, cols, "")
	_, err := q.Exec(ctx, sql, args...)
	return err
}

// TaipeiDate 把 DATE 欄位讀回的值（UTC 00:00）轉成台灣時間 00:00 的同一天（game 套件的日期慣例）。
func TaipeiDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, clock.TaipeiTZ)
}

// TaipeiDatePtr 同 TaipeiDate，nil 保持 nil。
func TaipeiDatePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := TaipeiDate(*t)
	return &d
}

// DateParam 把台灣日期轉成寫入 DATE 參數用的值（以 UTC 00:00 表示同一個年月日，避免時區換算改變日期）。
func DateParam(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
