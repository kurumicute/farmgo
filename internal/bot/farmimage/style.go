package farmimage

// 農場外觀主題與寵物造型（Go 版新增，2026-11-19，金幣回收第二波；規則與商品在 game/cosmetics）。
//
//   - 主題：農田本身不變，只換外圍（上方寵物草地帶、左右庭院）的地面磚，庭院的花叢位置換成主題點綴；
//     夜景另外對整張圖套偏藍、變暗的濾鏡。有主題時一律加上外圍（即使沒有擺裝飾）。
//   - 寵物造型：先依顏色旋轉寵物圖的色相（描邊與黑白像素不變），再疊上帽子（位置在 manifest pets.sprites[*].hat）。
//   - 有任何造型時不使用預先產生的圖與快取，每次重拼（約 20～30 毫秒）。

import (
	"fmt"
	"image"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"

	"farmgm/internal/game/cosmetics"
)

// PetSkin 一隻寵物的造型（"" = 沒有）。
type PetSkin struct {
	Hat, Color string
}

// Look 農田圖的外觀設定。
type Look struct {
	Theme string             // 主題代碼（"" = 預設）
	Skins map[string]PetSkin // 寵物代碼 → 造型
}

// Empty 沒有任何主題或造型。
func (l Look) Empty() bool {
	if l.Theme != "" {
		return false
	}
	for _, s := range l.Skins {
		if s != (PetSkin{}) {
			return false
		}
	}
	return true
}

// Key 快取用的字串。
func (l Look) Key() string {
	parts := []string{l.Theme}
	for _, code := range slices.Sorted(maps.Keys(l.Skins)) {
		s := l.Skins[code]
		parts = append(parts, code+"="+s.Hat+"/"+s.Color)
	}
	return strings.Join(parts, ",")
}

var (
	recolorMu    sync.Mutex
	recolorCache = map[string]*image.NRGBA{}
)

// petImage 寵物圖（套用顏色）。換色的結果依「圖檔＋顏色」快取。
func petImage(file, color string) (*image.NRGBA, error) {
	sprite, err := img(file)
	if err != nil {
		return nil, err
	}
	c, ok := cosmetics.FindColor(color)
	if !ok {
		return sprite, nil
	}
	key := file + "|" + color
	recolorMu.Lock()
	defer recolorMu.Unlock()
	if out, ok := recolorCache[key]; ok {
		return out, nil
	}
	out := recolor(sprite, c.Hue, c.SatPct)
	recolorCache[key] = out
	return out, nil
}

// drawPet 畫一隻寵物（含造型）：x、y 是寵物圖左上角。
func drawPet(canvas *image.NRGBA, s petSprite, skin PetSkin, x, y int) error {
	sprite, err := petImage(s.File, skin.Color)
	if err != nil {
		return err
	}
	alphaComposite(canvas, sprite, x, y)
	if _, ok := cosmetics.FindHat(skin.Hat); ok {
		hat, err := img(cosmetics.HatImage(skin.Hat))
		if err != nil {
			return err
		}
		alphaComposite(canvas, hat, x+s.Hat[0], y+s.Hat[1])
	}
	return nil
}

// recolor 色相旋轉 hue 度、飽和度 × satPct%。透明、太暗（描邊）與接近白色的像素不變；灰色像素先當成淡棕色再旋轉。
func recolor(src *image.NRGBA, hue, satPct int) *image.NRGBA {
	out := clone(src)
	for i := 0; i+3 < len(out.Pix); i += 4 {
		if out.Pix[i+3] == 0 {
			continue
		}
		h, s, v := rgbToHSV(out.Pix[i], out.Pix[i+1], out.Pix[i+2])
		if v < 0.25 || (s < 0.15 && v > 0.9) { // 描邊與白色（眼睛反光、白毛）不變
			continue
		}
		if s < 0.15 { // 灰色（虎斑貓）：當成淡淡的棕色再旋轉，換色才看得出來
			h, s = 30, 0.3
		}
		h = math.Mod(h+float64(hue), 360)
		s = math.Min(1, s*float64(satPct)/100)
		out.Pix[i], out.Pix[i+1], out.Pix[i+2] = hsvToRGB(h, s, v)
	}
	return out
}

func rgbToHSV(r8, g8, b8 uint8) (h, s, v float64) {
	r, g, b := float64(r8)/255, float64(g8)/255, float64(b8)/255
	hi, lo := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	v = hi
	d := hi - lo
	if hi == 0 || d == 0 {
		return 0, 0, v
	}
	s = d / hi
	switch hi {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, v
}

func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	to := func(f float64) uint8 { return uint8(math.Round(math.Min(1, math.Max(0, f+m)) * 255)) }
	return to(r), to(g), to(b)
}

// nightFilter 夜景：RGB 各乘以 pct%。
func nightFilter(canvas *image.NRGBA, pct [3]int) {
	for i := 0; i+3 < len(canvas.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			canvas.Pix[i+c] = uint8(int(canvas.Pix[i+c]) * pct[c] / 100)
		}
	}
}

// FarmPNGStyled 農田圖片＋庭院擺設＋主題與寵物造型；沒有主題與造型時等同 FarmPNGWithDecor（可以用快取）。
func FarmPNGStyled(plots []Plot, pets map[string]int, codes []string, look Look) ([]byte, error) {
	if look.Empty() {
		return FarmPNGWithDecor(plots, pets, codes)
	}
	m, err := getManifest()
	if err != nil {
		return nil, err
	}
	field, cached, err := fieldCanvas(plots, nil)
	if err != nil {
		return nil, err
	}
	if cached != nil { // 預先產生的圖：複製一份來畫
		hit, err := presetFor(m, plots[:min(len(plots), m.Field.Slots)])
		if err != nil {
			return nil, err
		}
		if hit == nil {
			return nil, fmt.Errorf("預先產生的圖不見了")
		}
		base, err := img(hit.rel)
		if err != nil {
			return nil, err
		}
		field = clone(base)
	}
	canvas, err := withDecorLook(m, field, pets, codes, look)
	if err != nil {
		return nil, err
	}
	return encodePaletteDynamic(canvas, PanelColors)
}
