// Package gameerr 遊戲規則錯誤。訊息可直接顯示給玩家（CMD、Discord 等）。
// 對應 Python app/game/errors.py：Python 的各個子類別在 Go 以 Kind 區分。
package gameerr

import (
	"errors"
	"fmt"
)

// Kind 錯誤種類（對應 Python 的 GameError 子類別）。
type Kind int

const (
	Generic Kind = iota // GameError
	NotFound
	Validation
	InsufficientGold
	InsufficientItems
	WarehouseFull
	PlotOccupied
	PlotNotReady
	PlotEmpty
	AlreadyExists
	PlotWithered
	PlotNotWatered
	NotGroupMember
	AlreadyStolen
	HomeGroupLocked // 農場主要所屬群組不可任意更改（一次性設定已完成，或群組轉移冷卻中）
)

// Error 所有可預期的遊戲操作失敗。
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// New 建立指定種類的錯誤；args 為空時 format 原樣使用。
func New(kind Kind, format string, args ...any) *Error {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	return &Error{Kind: kind, Msg: msg}
}

// Validationf 等同 raise ValidationError(...)。
func Validationf(format string, args ...any) *Error { return New(Validation, format, args...) }

// NotFoundf 等同 raise NotFoundError(...)。
func NotFoundf(format string, args ...any) *Error { return New(NotFound, format, args...) }

// As 取出 *Error。
func As(err error) (*Error, bool) {
	var ge *Error
	if errors.As(err, &ge) {
		return ge, true
	}
	return nil, false
}

// Is 判斷 err 是否為指定種類的遊戲錯誤（Generic 表示任何遊戲錯誤）。
func Is(err error, kind Kind) bool {
	ge, ok := As(err)
	if !ok {
		return false
	}
	return kind == Generic || ge.Kind == kind
}
