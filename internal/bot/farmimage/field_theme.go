package farmimage

// 農田本身套用主題（Go 版新增，2026-11-19）：原本主題只換外圍，柵欄裡的草地還是綠色。
//
//   - 每張農田底圖（field/base_NN.png）配一張遮罩（field/masks/base_NN.png，灰階，白色 = 草地），
//     有主題時把遮罩白色的部分換成主題的地面磚；格子裡（含鎖住的格子與障礙物）不換。
//   - 遮罩用 TestGenerateFieldMasks 自動產生（設定 FARMGM_FIELD_MASK_GEN=1），產生後要看預覽、必要時手動修圖。
//   - 主題細節（2026-11-19，都可以沒有）：themes/theme_<代碼>_plot.png 疊在每塊土地邊緣（濕土之上、作物之下）、_rail（兩端帶樁）蓋住上下兩排
//     柵欄、_cap 疊在左右兩排柵欄樁頂、_dot／_dot2 撒在換過的地面上。除了 _plot，都和換地面一起畫進底圖並快取。
//   - 有主題時不使用預先產生的農田圖（presets 的底是綠草），每次在換好地面的底圖上畫作物。

import (
	"errors"
	"image"
	"image/color"
	"io/fs"
	"path"
	"strconv"

	"farmgm/internal/game/cosmetics"
)

// fieldMaskPath 底圖對應的遮罩路徑。
func fieldMaskPath(base string) string { return "field/masks/" + path.Base(base) }

// tiled 用地面磚鋪滿 w×h。
func tiled(tile *image.NRGBA, w, h int) *image.NRGBA {
	canvas := newCanvas(w, h)
	for x := 0; x < w; x += tile.Rect.Dx() {
		for y := 0; y < h; y += tile.Rect.Dy() {
			alphaComposite(canvas, tile, x, y)
		}
	}
	return canvas
}

// themedBase 換好地面、畫好主題細節的農田底圖（n = 開放的格數；快取在 images；呼叫端不可修改，要畫的話先 clone）。
func themedBase(m *manifest, n int, theme cosmetics.Theme) (*image.NRGBA, error) {
	base := m.Field.Bases[strconv.Itoa(n)]
	key := "themed:" + theme.Code + ":" + base
	mu.Lock()
	cached, ok := images[key]
	mu.Unlock()
	if ok {
		return cached, nil
	}
	src, err := img(base)
	if err != nil {
		return nil, err
	}
	mask, err := img(fieldMaskPath(base))
	if err != nil {
		return nil, err
	}
	tile, err := img(theme.Tile)
	if err != nil {
		return nil, err
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	ground := tiled(tile, w, h)
	out := clone(src)
	for y := range h {
		for x := range w {
			a := int(mask.Pix[mask.PixOffset(x, y)]) // 遮罩是灰階，取 R
			if a == 0 {
				continue
			}
			i, j := out.PixOffset(x, y), ground.PixOffset(x, y)
			for c := range 4 {
				out.Pix[i+c] = uint8((int(ground.Pix[j+c])*a + int(out.Pix[i+c])*(255-a)) / 255)
			}
		}
	}
	if err := drawThemeDetails(out, mask, n, theme); err != nil {
		return nil, err
	}
	mu.Lock()
	images[key] = out
	mu.Unlock()
	return out, nil
}

// 柵欄樁頂（木頭截面）的中心 x 與底部 y；13 張底圖的柵欄位置相同（差距 2px 以內）。
var fencePosts = [][2]int{
	{27, 17}, {120, 16}, {254, 16}, {387, 16}, {482, 16}, // 上排
	{28, 456}, {120, 461}, {254, 462}, {387, 461}, {483, 457}, // 下排
	{23, 79}, {26, 149}, {26, 246}, {25, 323}, {26, 411}, // 左邊
	{484, 84}, {484, 155}, {484, 246}, {484, 323}, {484, 411}, // 右邊
}

// 上下兩排柵欄疊圖的頂端 y。柵欄圖兩端各有一根樁（寬 railEnd），兩端的樁對齊相鄰兩根柵欄樁的中心，中間的橫木拉長。
const (
	railTopY    = 0
	railBottomY = 446
	railEnd     = 16
)

// themeDetail 主題細節圖（沒有這張圖時回傳 nil）。
func themeDetail(theme cosmetics.Theme, kind string) (*image.NRGBA, error) {
	sprite, err := img("themes/theme_" + theme.Code + "_" + kind + ".png")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return sprite, err
}

// drawThemeDetails 上下兩排柵欄、左右兩排樁頂、地面小點綴（依序疊上）。泥土格邊框要蓋在濕土上面，在 themedPlainField 每格畫。
func drawThemeDetails(out, mask *image.NRGBA, n int, theme cosmetics.Theme) error {
	rail, err := themeDetail(theme, "rail")
	if err != nil {
		return err
	}
	if rail != nil {
		for _, row := range [][][2]int{fencePosts[0:5], fencePosts[5:10]} {
			y := railTopY
			if row[0][1] > 200 {
				y = railBottomY
			}
			for i := 0; i+1 < len(row); i++ {
				x0, x1 := row[i][0]-railEnd/2, row[i+1][0]+railEnd/2
				alphaComposite(out, stretchMiddle(rail, x1-x0, railEnd), x0, y)
			}
		}
	}
	capSprite, err := themeDetail(theme, "cap")
	if err != nil {
		return err
	}
	if capSprite != nil {
		posts := fencePosts[10:] // 上下兩排的樁已經包含在柵欄圖裡
		if rail == nil {
			posts = fencePosts
		}
		for _, p := range posts {
			alphaComposite(out, capSprite, p[0]-capSprite.Rect.Dx()/2, p[1]-capSprite.Rect.Dy()+8)
		}
	}
	// 地面小點綴：固定的偽隨機位置（每次結果相同），整個點綴都在換過的地面上才放；數量少，不要搶眼
	var dots []*image.NRGBA
	for _, kind := range []string{"dot", "dot2"} {
		d, err := themeDetail(theme, kind)
		if err != nil {
			return err
		}
		if d != nil {
			dots = append(dots, d)
		}
	}
	if len(dots) == 0 {
		return nil
	}
	const step = 40
	k := 0
	for y := 0; y < out.Rect.Dy(); y += step {
		for x := 0; x < out.Rect.Dx(); x += step {
			k++
			h := uint32(k)*2654435761 + uint32(n)*97
			if h%4 != 0 { // 約四分之一的位置放
				continue
			}
			dot := dots[int(h>>4)%len(dots)]
			dx, dy := x+int(h>>8%24), y+int(h>>16%24)
			if dx+dot.Rect.Dx() > out.Rect.Dx() || dy+dot.Rect.Dy() > out.Rect.Dy() || !onGround(mask, dot, dx, dy) {
				continue
			}
			alphaComposite(out, dot, dx, dy)
		}
	}
	return nil
}

// stretchMiddle 橫向拉長：左右各 end px 不變，中間最近鄰拉伸成 w 寬。
func stretchMiddle(src *image.NRGBA, w, end int) *image.NRGBA {
	sw, h := src.Rect.Dx(), src.Rect.Dy()
	if w == sw || w < 2*end || sw <= 2*end {
		return nearest(src, w, h)
	}
	out := newCanvas(w, h)
	mid, smid := w-2*end, sw-2*end
	for y := range h {
		for x := range w {
			sx := x
			switch {
			case x >= w-end:
				sx = sw - (w - x)
			case x >= end:
				sx = end + (x-end)*smid/mid
			}
			copy(out.Pix[out.PixOffset(x, y):out.PixOffset(x, y)+4], src.Pix[src.PixOffset(sx, y):])
		}
	}
	return out
}

// onGround 點綴不透明的像素是否全部落在遮罩白色（換過的地面）上。
func onGround(mask, sprite *image.NRGBA, x0, y0 int) bool {
	for y := range sprite.Rect.Dy() {
		for x := range sprite.Rect.Dx() {
			if sprite.Pix[sprite.PixOffset(x, y)+3] == 0 {
				continue
			}
			if mask.Pix[mask.PixOffset(x0+x, y0+y)] < 255 {
				return false
			}
		}
	}
	return true
}

// scaledRim 縮放好的泥土格邊框（每格大小差 1～2px，依大小快取在 images）。
func scaledRim(theme cosmetics.Theme, rim *image.NRGBA, w, h int) *image.NRGBA {
	key := "rim:" + theme.Code + ":" + strconv.Itoa(w) + "x" + strconv.Itoa(h)
	mu.Lock()
	defer mu.Unlock()
	if out, ok := images[key]; ok {
		return out
	}
	out := nearest(rim, w, h)
	images[key] = out
	return out
}

// nearest 最近鄰縮放（像素風不要糊）。
func nearest(src *image.NRGBA, w, h int) *image.NRGBA {
	if src.Rect.Dx() == w && src.Rect.Dy() == h {
		return src
	}
	out := newCanvas(w, h)
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	for y := range h {
		for x := range w {
			copy(out.Pix[out.PixOffset(x, y):out.PixOffset(x, y)+4], src.Pix[src.PixOffset(x*sw/w, y*sh/h):])
		}
	}
	return out
}

// themedPlainField plainField 的主題版：底圖換地面，作物一律即時畫（不使用預先產生的圖）。
func themedPlainField(m *manifest, plots []Plot, theme cosmetics.Theme) (*image.NRGBA, error) {
	n := min(max(len(plots), m.Field.MinPlots), m.Field.Slots)
	base, err := themedBase(m, n, theme)
	if err != nil {
		return nil, err
	}
	rim, err := themeDetail(theme, "plot")
	if err != nil {
		return nil, err
	}
	canvas := clone(base)
	for k, p := range plots {
		r := m.Field.Cells[k].Rect
		var scaled *image.NRGBA
		if rim != nil {
			scaled = scaledRim(theme, rim, r[2]-r[0]+6, r[3]-r[1]+6)
		}
		if err := drawPlotRim(m, canvas, r, p, scaled); err != nil {
			return nil, err
		}
	}
	return canvas, nil
}

// themedFieldCanvas fieldCanvas 的主題版（不含寵物；寵物由 withDecorLook 畫在上方）。
func themedFieldCanvas(m *manifest, plots []Plot, theme cosmetics.Theme) (*image.NRGBA, error) {
	if len(plots) <= m.Field.Slots {
		return themedPlainField(m, plots, theme)
	}
	first, err := themedPlainField(m, plots[:m.Field.Slots], theme)
	if err != nil {
		return nil, err
	}
	second, err := themedPlainField(m, plots[m.Field.Slots:min(len(plots), 2*m.Field.Slots)], theme)
	if err != nil {
		return nil, err
	}
	return stackFieldsTile(first, second, theme.Tile)
}

// grassLike 遮罩產生用：像草地的顏色（黃綠色；樹叢的葉子偏藍綠，不算）。
func grassLike(c color.NRGBA) bool {
	if c.A < 128 {
		return false
	}
	h, s, v := hsv(c)
	return h > 0.15 && h < 0.36 && s > 0.3 && v > 0.28
}

// hsv 0～1 的色相、飽和度、明度。
func hsv(c color.NRGBA) (h, s, v float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	mx, mn := max(r, g, b), min(r, g, b)
	v = mx
	if mx == 0 {
		return 0, 0, 0
	}
	s = (mx - mn) / mx
	if mx == mn {
		return 0, s, v
	}
	d := mx - mn
	switch mx {
	case r:
		h = (g - b) / d
		if h < 0 {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, v
}

// components 4 連通的區塊（in[i] 為 true 的像素），回傳每個像素的區塊編號（0 = 不在任何區塊）與各區塊大小。
func components(in []bool, w, h int) ([]int, []int) {
	label := make([]int, len(in))
	sizes := []int{0}
	stack := []int{}
	for start := range in {
		if !in[start] || label[start] != 0 {
			continue
		}
		id := len(sizes)
		label[start] = id
		stack = append(stack[:0], start)
		size := 0
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			size++
			x, y := p%w, p/w
			for _, q := range [4][2]int{{x + 1, y}, {x - 1, y}, {x, y + 1}, {x, y - 1}} {
				if q[0] < 0 || q[0] >= w || q[1] < 0 || q[1] >= h {
					continue
				}
				n := q[1]*w + q[0]
				if in[n] && label[n] == 0 {
					label[n] = id
					stack = append(stack, n)
				}
			}
		}
		sizes = append(sizes, size)
	}
	return label, sizes
}

// 遮罩產生的參數：草地是大片相連的（小於 minGrass 的綠色區塊是葉子、青苔）；
// 被草地包圍、小於 maxHole 的區塊（白花、小石子、草的陰影）一起換掉。
const (
	minGrass = 600
	maxHole  = 80
)

// autoFieldMask 自動產生一張底圖的遮罩（白色 = 草地）。
func autoFieldMask(src *image.NRGBA, cells []rect) *image.Gray {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	grass := make([]bool, w*h)
	for y := range h {
		for x := range w {
			i := src.PixOffset(x, y)
			p := src.Pix[i : i+4]
			grass[y*w+x] = grassLike(color.NRGBA{p[0], p[1], p[2], p[3]})
		}
	}
	label, sizes := components(grass, w, h)
	mask := make([]bool, w*h)
	for i, l := range label {
		mask[i] = l != 0 && sizes[l] >= minGrass
	}
	rest := make([]bool, w*h)
	for i := range mask {
		rest[i] = !mask[i]
	}
	label, sizes = components(rest, w, h)
	// 格子中央的小區塊不補：有青苔的石頭會被青苔切成小塊，補掉的話石頭就不見了（青苔換成主題地面，像積雪）
	sumX, sumY := make([]int, len(sizes)), make([]int, len(sizes))
	for i, l := range label {
		sumX[l] += i % w
		sumY[l] += i / w
	}
	inCellCenter := func(l int) bool {
		x, y := sumX[l]/sizes[l], sumY[l]/sizes[l]
		for _, c := range cells {
			mx, my := (c[2]-c[0])/6, (c[3]-c[1])/6
			if x >= c[0]+mx && x < c[2]-mx && y >= c[1]+my && y < c[3]-my {
				return true
			}
		}
		return false
	}
	for i, l := range label {
		if l != 0 && sizes[l] < maxHole && !inCellCenter(l) {
			mask[i] = true
		}
	}
	for _, c := range cells { // 格子裡（含鎖住的格子）保留原圖，主題只換柵欄裡的外圈
		for y := c[1]; y < c[3]; y++ {
			for x := c[0]; x < c[2]; x++ {
				mask[y*w+x] = false
			}
		}
	}
	out := image.NewGray(image.Rect(0, 0, w, h))
	for i, on := range mask {
		if on {
			out.Pix[i] = 255
		}
	}
	return out
}
