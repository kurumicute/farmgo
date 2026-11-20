package farmimage

import (
	"image"
	"strings"

	"farmgm/internal/game/treasure"
)

// NPC 訂單與藏寶圖的圖片（Go 版新增，2026-11-20）。

const (
	portraitSize = 128 // npc/<代碼>.png
	portraitGap  = 8
)

// portraitMemo 客人頭像組合 → PNG（最多 6 位客人取 3 位，組合有限）。
var portraitMemo = map[string][]byte{}

// PortraitsPNG NPC 訂單頁：客人頭像依訂單順序排成一列（透明背景）；沒有客人時回傳 nil。
func PortraitsPNG(codes []string) ([]byte, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	return memo(&portraitMemo, strings.Join(codes, ","), func() ([]byte, error) {
		canvas := image.NewNRGBA(image.Rect(0, 0, len(codes)*portraitSize+(len(codes)-1)*portraitGap, portraitSize))
		for i, code := range codes {
			p, err := img("npc/" + code + ".png")
			if err != nil {
				return nil, err
			}
			alphaComposite(canvas, p, i*(portraitSize+portraitGap), 0)
		}
		return encodePalette(canvas, 256)
	})
}

// TreasurePNG 寶藏地點的場景圖（manifest 的 scenes）。
func TreasurePNG(site treasure.Site) ([]byte, error) { return ScenePNG(site.Scene) }
