package farmimage

// 調色盤 PNG：把拼好的畫面減到 n 色再編碼（Python image.quantize(n, method=FASTOCTREE, dither=NONE)）。
//
// 移植 Pillow 12.3 的 libImaging/QuantOctree.c（FASTOCTREE）與 Quant.c 的 RGBA 前處理：
//   - 細分顏色立方體（RGBA 為 R3 G4 B3 A3 位元）與粗略立方體（各 2 位元）；調色盤 = 粗略立方體用到的格子
//     ＋細分立方體中最常出現的格子，每格的顏色是格內所有像素的平均（C 以 float 單精度除法，這裡用 float32）。
//   - 每個像素直接查「對照立方體」得到調色盤編號（沒有最近色搜尋、不抖動），同一格的顏色一律對到同一個編號，
//     大片顏色相近的區域會變成同一色，PNG 壓縮效果好。
//   - 完全透明的像素：第一個之後的顏色改成第一個的顏色（Quant.c）。
//   - 與 Pillow 唯一可能的差異：C 的 qsort 不穩定，次數相同的格子順序依平台而定；這裡用穩定排序（次數相同時依格子位置）。
//
// 調色盤長度固定為 n（用不到的項目是 (0, 0, 0, 0)），與 Pillow 相同。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sort"
)

type octBucket struct {
	count      uint32 // 對照立方體中存放調色盤編號
	r, g, b, a uint64
}

type octCube struct {
	rBits, gBits, bBits, aBits         uint
	rWidth, gWidth, bWidth, aWidth     uint
	rOffset, gOffset, bOffset, aOffset uint
	buckets                            []octBucket
}

func newCube(r, g, b, a uint) *octCube {
	c := &octCube{rBits: r, gBits: g, bBits: b, aBits: a}
	c.rWidth, c.gWidth, c.bWidth, c.aWidth = 1<<r, 1<<g, 1<<b, 1<<a
	c.rOffset, c.gOffset, c.bOffset, c.aOffset = g+b+a, b+a, a, 0
	c.buckets = make([]octBucket, c.rWidth*c.gWidth*c.bWidth*c.aWidth)
	return c
}

func (c *octCube) offsetPos(r, g, b, a uint) uint {
	return r<<c.rOffset | g<<c.gOffset | b<<c.bOffset | a<<c.aOffset
}

func (c *octCube) bucketOf(p [4]uint8) *octBucket {
	return &c.buckets[c.offsetPos(uint(p[0])>>(8-c.rBits), uint(p[1])>>(8-c.gBits), uint(p[2])>>(8-c.bBits), uint(p[3])>>(8-c.aBits))]
}

func (c *octCube) add(p [4]uint8) {
	b := c.bucketOf(p)
	b.count++
	b.r += uint64(p[0])
	b.g += uint64(p[1])
	b.b += uint64(p[2])
	b.a += uint64(p[3])
}

func (c *octCube) used() int {
	n := 0
	for i := range c.buckets {
		if c.buckets[i].count > 0 {
			n++
		}
	}
	return n
}

func clip8f(v float32) uint8 {
	i := int(v) // C 的 (int) 轉換：向零截斷
	if i <= 0 {
		return 0
	}
	if i >= 255 {
		return 255
	}
	return uint8(i)
}

// avg Pillow avg_color_from_color_bucket（float 單精度除法後截斷）。
func avg(b octBucket) [4]uint8 {
	if b.count == 0 {
		return [4]uint8{}
	}
	n := float32(b.count)
	return [4]uint8{clip8f(float32(b.r) / n), clip8f(float32(b.g) / n), clip8f(float32(b.b) / n), clip8f(float32(b.a) / n)}
}

// sortedPalette Pillow create_sorted_color_palette：全部格子依次數由多到少（穩定排序）。
func (c *octCube) sortedPalette() []octBucket {
	out := append([]octBucket(nil), c.buckets...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].count > out[j].count })
	return out
}

// copyCube Pillow copy_color_cube：放大或縮小到指定位元數（格子的值相加）。
func copyCube(c *octCube, rBits, gBits, bBits, aBits uint) *octCube {
	res := newCube(rBits, gBits, bBits, aBits)
	var srcReduce, dstReduce, width [4]uint
	dims := [4][3]uint{{c.rBits, res.rBits, 0}, {c.gBits, res.gBits, 0}, {c.bBits, res.bBits, 0}, {c.aBits, res.aBits, 0}}
	widths := [4][2]uint{{c.rWidth, res.rWidth}, {c.gWidth, res.gWidth}, {c.bWidth, res.bWidth}, {c.aWidth, res.aWidth}}
	for i, d := range dims {
		if d[0] > d[1] {
			dstReduce[i] = d[0] - d[1]
			width[i] = widths[i][0]
		} else {
			srcReduce[i] = d[1] - d[0]
			width[i] = widths[i][1]
		}
	}
	for r := uint(0); r < width[0]; r++ {
		for g := uint(0); g < width[1]; g++ {
			for b := uint(0); b < width[2]; b++ {
				for a := uint(0); a < width[3]; a++ {
					src := &c.buckets[c.offsetPos(r>>srcReduce[0], g>>srcReduce[1], b>>srcReduce[2], a>>srcReduce[3])]
					dst := &res.buckets[res.offsetPos(r>>dstReduce[0], g>>dstReduce[1], b>>dstReduce[2], a>>dstReduce[3])]
					dst.count += src.count
					dst.r += src.r
					dst.g += src.g
					dst.b += src.b
					dst.a += src.a
				}
			}
		}
	}
	return res
}

// subtract Pillow subtract_color_buckets。
func (c *octCube) subtract(buckets []octBucket) {
	for _, s := range buckets {
		if s.count == 0 {
			continue
		}
		m := c.bucketOf(avg(s))
		m.count -= s.count
		m.r -= s.r
		m.g -= s.g
		m.b -= s.b
		m.a -= s.a
	}
}

// addLookup Pillow add_lookup_buckets：由後往前把調色盤編號寫入對照立方體。
func (c *octCube) addLookup(palette []octBucket, n, offset int) {
	for i := offset + n - 1; i >= offset; i-- {
		c.bucketOf(avg(palette[i])).count = uint32(i)
	}
}

var (
	cubeLevels      = [8]uint{4, 4, 4, 0, 2, 2, 2, 0}
	cubeLevelsAlpha = [8]uint{3, 4, 3, 3, 2, 2, 2, 2}
)

// quantizeOctree Pillow quantize_octree：回傳調色盤（長度 n）與每個像素的編號。
func quantizeOctree(pixels [][4]uint8, n int, withAlpha bool) ([][4]uint8, []uint8) {
	bits := cubeLevels
	if withAlpha {
		bits = cubeLevelsAlpha
	}
	fine := newCube(bits[0], bits[1], bits[2], bits[3])
	for _, p := range pixels {
		fine.add(p)
	}
	coarse := copyCube(fine, bits[4], bits[5], bits[6], bits[7])
	nCoarse := min(coarse.used(), n)
	nFine := n - nCoarse
	fineSorted := fine.sortedPalette()
	coarse.subtract(fineSorted[:nFine])
	for nCoarse > coarse.used() { // 減掉之後有粗略格子空出來：讓給細分顏色
		already := nFine
		nCoarse = coarse.used()
		nFine = n - nCoarse
		coarse.subtract(fineSorted[already:nFine])
	}
	palette := append(append([]octBucket(nil), coarse.sortedPalette()[:nCoarse]...), fineSorted[:nFine]...)
	coarseLookup := newCube(bits[4], bits[5], bits[6], bits[7])
	coarseLookup.addLookup(palette, nCoarse, 0)
	lookup := copyCube(coarseLookup, bits[0], bits[1], bits[2], bits[3])
	lookup.addLookup(palette, nFine, nCoarse)
	idx := make([]uint8, len(pixels))
	for i, p := range pixels {
		idx[i] = uint8(lookup.bucketOf(p).count)
	}
	pal := make([][4]uint8, n)
	for i := range pal {
		if i < len(palette) {
			pal[i] = avg(palette[i])
		}
	}
	return pal, idx
}

// quantize 減到 n 色（n ≤ 256）的調色盤影像（Python image.quantize(n, FASTOCTREE)；RGBA 影像，含透明度）。
func quantize(img *image.NRGBA, n int) *image.Paletted {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	pixels := make([][4]uint8, 0, w*h)
	transparency := false
	var tr, tg, tb uint8
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			p := [4]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
			if p[3] == 0 {
				if !transparency {
					transparency, tr, tg, tb = true, p[0], p[1], p[2]
				} else { // 之後的完全透明像素改成第一個的顏色
					p[0], p[1], p[2] = tr, tg, tb
				}
			}
			pixels = append(pixels, p)
		}
	}
	pal, idx := quantizeOctree(pixels, n, true)
	cp := make(color.Palette, len(pal))
	for i, c := range pal {
		cp[i] = color.NRGBA{c[0], c[1], c[2], c[3]}
	}
	out := image.NewPaletted(image.Rect(0, 0, w, h), cp)
	copy(out.Pix, idx)
	return out
}

var encoder = png.Encoder{CompressionLevel: png.BestCompression}

// encodePalette 減到 n 色並編碼成 PNG。
func encodePalette(img *image.NRGBA, n int) ([]byte, error) {
	var buf bytes.Buffer
	if err := encoder.Encode(&buf, quantize(img, n)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
