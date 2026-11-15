package farmimage

// 庭院擺設（Go 版新增，2026-11-15）：有擺裝飾時，農田左右各加一條草地（點綴花叢），左邊放 1–3 號、右邊放 4–6 號裝飾；
// 上方一律加寵物帶高度的草地（有寵物時畫寵物），圖片接近正方形（16 格農田 704×602）。
// 沒有擺任何裝飾時不畫，圖片與之前完全相同（仍可使用預先產生的圖與快取）。

import (
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"

	"farmgm/internal/game/decor"
)

type decorSides struct {
	Width   int      `json:"width"`
	Tile    string   `json:"tile"`
	Flower  string   `json:"flower"`
	Size    int      `json:"size"`
	Rows    []int    `json:"rows"`    // 每側裝飾底部的 y（相對農田頂端）
	Flowers [][2]int `json:"flowers"` // 點綴花叢的位置（相對農田頂端；x 為負數時從圖片右邊算）
}

func hasDecor(codes []string) bool {
	return slices.ContainsFunc(codes, func(c string) bool { return c != "" })
}

// withDecor 農田左右加庭院、上方加草地帶，畫裝飾與寵物。
func withDecor(m *manifest, field *image.NRGBA, pets map[string]int, codes []string) (*image.NRGBA, error) {
	d := m.DecorSides
	top := m.Pets.BandHeight
	fw, fh := field.Rect.Dx(), field.Rect.Dy()
	w, h := fw+2*d.Width, fh+top
	canvas := newCanvas(w, h)
	tile, err := img(d.Tile)
	if err != nil {
		return nil, err
	}
	for x := 0; x < w; x += tile.Rect.Dx() {
		for y := 0; y < h; y += tile.Rect.Dy() {
			alphaComposite(canvas, tile, x, y)
		}
	}
	flower, err := img(d.Flower)
	if err != nil {
		return nil, err
	}
	for _, pos := range d.Flowers {
		x := pos[0]
		if x < 0 {
			x += w
		}
		alphaComposite(canvas, flower, x, top+pos[1])
	}
	alphaComposite(canvas, field, d.Width, top)
	per := len(d.Rows)
	for i, code := range codes[:min(len(codes), 2*per)] {
		if code == "" {
			continue
		}
		sprite, err := img(decor.ImagePath(code))
		if err != nil {
			return nil, err
		}
		x := (d.Width - sprite.Rect.Dx()) / 2
		if i >= per {
			x += w - d.Width
		}
		alphaComposite(canvas, sprite, x, top+d.Rows[i%per]-sprite.Rect.Dy())
	}
	// 寵物：和原本一樣站在農田左上、右上的草地上
	for _, s := range m.petSprites {
		if _, ok := pets[s.Code]; !ok {
			continue
		}
		sprite, err := img(s.File)
		if err != nil {
			return nil, err
		}
		x := d.Width + s.Margin
		if s.Anchor != "left" {
			x = d.Width + fw - sprite.Rect.Dx() - s.Margin
		}
		alphaComposite(canvas, sprite, x, top-sprite.Rect.Dy()+s.Bottom)
	}
	return canvas, nil
}

// decorCacheMax 預先產生的圖 + 庭院擺設最多快取幾張（每張約 100–200 KB）。
const decorCacheMax = 256

// FarmPNGWithDecor 農田圖片加上庭院擺設（codes：1–6 號位置的裝飾代碼，"" = 空）。
//
// 命中預先產生的農田圖（空地、全部同一種狀態）時，依「農田圖、有哪幾隻寵物、擺設」快取，之後同樣的組合直接回傳，
// 不必重拼（沒有快取時每次約 20 毫秒）；其他情況每次重拼（比沒有擺設多約 6 毫秒）。
func FarmPNGWithDecor(plots []Plot, pets map[string]int, codes []string) ([]byte, error) {
	if !hasDecor(codes) {
		return FarmPNG(plots, pets)
	}
	m, err := getManifest()
	if err != nil {
		return nil, err
	}
	field, cached, err := fieldCanvas(plots, nil)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		if len(plots) > m.Field.Slots {
			plots = plots[:m.Field.Slots]
		}
		hit, err := presetFor(m, plots)
		if err != nil {
			return nil, err
		}
		petCodes := slices.Sorted(maps.Keys(pets))
		key := fmt.Sprintf("%s|%v|%s", hit.rel, petCodes, strings.Join(codes, ","))
		mu.Lock()
		if len(decorCache) >= decorCacheMax {
			decorCache = map[string][]byte{}
		}
		mu.Unlock()
		return memo(&decorCache, key, func() ([]byte, error) {
			base, err := img(hit.rel)
			if err != nil {
				return nil, err
			}
			canvas, err := withDecor(m, clone(base), pets, codes)
			if err != nil {
				return nil, err
			}
			return encodePalette(canvas, PanelColors)
		})
	}
	canvas, err := withDecor(m, field, pets, codes)
	if err != nil {
		return nil, err
	}
	return encodePaletteDynamic(canvas, PanelColors)
}
