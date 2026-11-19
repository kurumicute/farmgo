package services

// 介面語言（2026-11-19 中英文切換）：服務層組訊息時，從 ctx 取得面板的語言（i18n.WithLang），
// 字串放在專案根目錄的 i18n/<語言>.json。繁體中文的文字和原本完全相同，沒有指定語言時維持繁體中文。

import (
	"context"
	"fmt"
	"strings"

	"farmgm/i18n"
	"farmgm/internal/db"
	"farmgm/internal/db/models"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/rules"
)

// tr 依 ctx 的語言取文字。
func tr(ctx context.Context, key string, args ...any) string { return i18n.Tc(ctx, key, args...) }

// isZh ctx 的語言是不是繁體中文（預設）。
func isZh(ctx context.Context) bool { return i18n.LangOf(ctx) == i18n.Default }

// CropName 作物名稱：英文時用 i18n 的 crop.<代碼>，沒有翻譯或繁體中文時用資料庫的名稱。
func CropName(lang, code, zh string) string { return i18n.Name(lang, "crop."+code, zh) }

// ProduceNameLang 收成物的名稱（珍稀變種用變種名稱）。
func ProduceNameLang(lang, code, name, variantCode, variantName, mutation string) string {
	if mutation == "variant" {
		return CropName(lang, variantCode, variantName)
	}
	return CropName(lang, code, name)
}

// GeneticsLabelLang GeneticsLabel 的多語版本：繁中「【稀有・黃金作物】」，英文「 [Rare · Golden]」。
func GeneticsLabelLang(lang, quality, mutation string) string {
	if lang == i18n.Default {
		return GeneticsLabel(quality, mutation)
	}
	var parts []string
	if quality != "" && quality != "normal" {
		parts = append(parts, i18n.T(lang, "quality."+quality))
	}
	if mutation != "" && mutation != "none" && mutation != "variant" {
		parts = append(parts, i18n.T(lang, "mutation."+mutation))
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " · ") + "]"
}

// DurationLang 時間長度：繁中沿用原本的格式；英文用精簡格式（2d 3h 20m、5m 3s）。
func DurationLang(lang string, seconds int, long bool) string {
	if lang == i18n.Default {
		if long {
			return LongDuration(seconds)
		}
		return rules.FormatDuration(seconds)
	}
	seconds = max(0, seconds)
	d, h, m, s := seconds/86400, seconds%86400/3600, seconds%3600/60, seconds%60
	var parts []string
	switch {
	case seconds >= 3600 && long:
		if d > 0 {
			parts = append(parts, fmt.Sprintf("%dd", d))
		}
		if h > 0 {
			parts = append(parts, fmt.Sprintf("%dh", h))
		}
		if m > 0 || len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("%dm", m))
		}
	case seconds >= 3600:
		parts = append(parts, fmt.Sprintf("%dh", d*24+h), fmt.Sprintf("%dm", m), fmt.Sprintf("%ds", s))
	case seconds >= 60:
		parts = append(parts, fmt.Sprintf("%dm", m), fmt.Sprintf("%ds", s))
	default:
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return strings.Join(parts, " ")
}

// cropNameCtx 作物在 ctx 語言的名稱。
func cropNameCtx(ctx context.Context, crop *models.CropType) string {
	return CropName(i18n.LangOf(ctx), crop.Code, crop.Name)
}

// SetLanguage 記住玩家選的介面語言（players.language，Migration 0047）；lang 為 "" 時改回跟著 Discord 用戶端。
// 回覆訊息用新的語言。
func (s *GameService) SetLanguage(ctx context.Context, playerID int64, lang string) (ActionResult, error) {
	var value any
	if lang != "" {
		if !i18n.Supported(lang) {
			return ActionResult{}, gameerr.Validationf("不支援的語言：%s", lang)
		}
		value = lang
	}
	return inTx(ctx, s, func(t *db.Tx) (ActionResult, error) {
		farm, err := s.farmOf(ctx, t, playerID)
		if err != nil {
			return ActionResult{}, err
		}
		if _, err := t.Exec(ctx, "UPDATE players SET language = $2, updated_at = $3 WHERE id = $1", playerID, value, s.now()); err != nil {
			return ActionResult{}, err
		}
		shown := lang
		if shown == "" {
			shown = i18n.Default
		}
		return result(i18n.T(shown, "ui.lang.switched"), farm.Gold, 0, "", 0), nil
	})
}

// txt 中文原文在 ctx 語言的翻譯（i18n 文字表；繁中或沒有翻譯時回傳原文）。
func txt(ctx context.Context, zh string) string { return i18n.Text(i18n.LangOf(ctx), zh) }

// txf 翻譯格式字串後代入參數（給玩家看的訊息用；交易紀錄、管理日誌仍用 fmt.Sprintf 保持中文）。
func txf(ctx context.Context, format string, args ...any) string {
	return i18n.Sprintf(i18n.LangOf(ctx), format, args...)
}
