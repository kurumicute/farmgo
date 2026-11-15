package farmimage

// 庭院擺飾區（Go 版新增，2026-11-15）：農田圖下方加一條草地，依位置畫玩家擺的裝飾（game/decor）。
// 沒有擺任何裝飾時不畫，圖片與之前完全相同（仍可使用預先產生的圖與快取）。

import (
	"image"
	"slices"

	"farmgm/internal/game/decor"
)

type decorBand struct {
	Height int    `json:"height"`
	Strip  string `json:"strip"`
	Tile   string `json:"tile"`
	Slots  int    `json:"slots"`
	Size   int    `json:"size"`
	Bottom int    `json:"bottom"`
}

func hasDecor(codes []string) bool {
	return slices.ContainsFunc(codes, func(c string) bool { return c != "" })
}

// fullFieldCanvas 農田畫面（含寵物）；命中預先產生的圖時也拼成畫布（要在下方再加擺飾區）。
func fullFieldCanvas(plots []Plot, pets map[string]int) (*image.NRGBA, error) {
	canvas, cached, err := fieldCanvas(plots, pets)
	if err != nil || cached == nil {
		return canvas, err
	}
	m, err := getManifest()
	if err != nil {
		return nil, err
	}
	if len(plots) > m.Field.Slots {
		plots = plots[:m.Field.Slots]
	}
	hit, err := presetFor(m, plots)
	if err != nil {
		return nil, err
	}
	base, err := img(hit.rel)
	if err != nil {
		return nil, err
	}
	canvas = clone(base)
	if len(pets) > 0 {
		return withPets(m, canvas, pets)
	}
	return canvas, nil
}

// withDecor 在畫面下方加庭院擺飾區：草地底（往上多鋪 8 像素，接住農田圖底部的透明邊）、柵欄草地條，再依位置畫裝飾。
func withDecor(m *manifest, field *image.NRGBA, codes []string) (*image.NRGBA, error) {
	b := m.DecorBand
	w, h := field.Rect.Dx(), field.Rect.Dy()
	canvas := newCanvas(w, h+b.Height)
	tile, err := img(b.Tile)
	if err != nil {
		return nil, err
	}
	for x := 0; x < w; x += tile.Rect.Dx() {
		for y := h - 8; y < h+b.Height; y += tile.Rect.Dy() {
			alphaComposite(canvas, tile, x, y)
		}
	}
	alphaComposite(canvas, field, 0, 0)
	strip, err := img(b.Strip)
	if err != nil {
		return nil, err
	}
	alphaComposite(canvas, strip, 0, h)
	gap := (w - b.Slots*b.Size) / (b.Slots + 1)
	for i, code := range codes[:min(len(codes), b.Slots)] {
		if code == "" {
			continue
		}
		sprite, err := img(decor.ImagePath(code))
		if err != nil {
			return nil, err
		}
		x := gap + i*(b.Size+gap) + (b.Size-sprite.Rect.Dx())/2
		alphaComposite(canvas, sprite, x, h+b.Height-sprite.Rect.Dy()-b.Bottom)
	}
	return canvas, nil
}

// FarmPNGWithDecor 農田圖片加上庭院擺飾區（codes：1–6 號位置的裝飾代碼，"" = 空）。
func FarmPNGWithDecor(plots []Plot, pets map[string]int, codes []string) ([]byte, error) {
	if !hasDecor(codes) {
		return FarmPNG(plots, pets)
	}
	m, err := getManifest()
	if err != nil {
		return nil, err
	}
	canvas, err := fullFieldCanvas(plots, pets)
	if err != nil {
		return nil, err
	}
	if canvas, err = withDecor(m, canvas, codes); err != nil {
		return nil, err
	}
	return encodePaletteDynamic(canvas, PanelColors)
}
