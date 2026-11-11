package farmimage

// 與 Pillow 相同結果的影像運算（移植 Pillow 的 C 程式：AlphaComposite.c、Resample.c、Convert.c 的 RGBa 轉換），
// 讓拼好的畫面（上傳前）與 Python 逐像素相同。影像一律是 *image.NRGBA（非預乘，對應 Pillow 的 RGBA）。

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// toNRGBA 轉成 NRGBA（Pillow convert("RGBA")）。調色盤（含 tRNS）與 NRGBA 直接取值，不經預乘，半透明像素不失真。
func toNRGBA(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	switch s := src.(type) {
	case *image.Paletted:
		pal := make([]color.NRGBA, len(s.Palette))
		for i, c := range s.Palette {
			pal[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				c := pal[s.ColorIndexAt(b.Min.X+x, b.Min.Y+y)]
				i := out.PixOffset(x, y)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = c.R, c.G, c.B, c.A
			}
		}
	default:
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
				i := out.PixOffset(x, y)
				out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = c.R, c.G, c.B, c.A
			}
		}
	}
	return out
}

func clone(src *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(src.Rect)
	copy(out.Pix, src.Pix)
	return out
}

func shiftForDiv255(a uint32) uint32 { return ((a >> 8) + a) >> 8 }

// alphaComposite Pillow Image.alpha_composite(src, dest=(x, y))：把 src 疊在 dst 上（超出 dst 的部分忽略）。
func alphaComposite(dst, src *image.NRGBA, x0, y0 int) {
	const precisionBits = 7
	r := image.Rect(x0, y0, x0+src.Rect.Dx(), y0+src.Rect.Dy()).Intersect(dst.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			s := src.PixOffset(x-x0, y-y0)
			d := dst.PixOffset(x, y)
			sa := uint32(src.Pix[s+3])
			if sa == 0 {
				continue // Pillow：來源完全透明時保留原像素
			}
			da := uint32(dst.Pix[d+3])
			blend := da * (255 - sa)
			outa255 := sa*255 + blend
			coef1 := sa * 255 * 255 * (1 << precisionBits) / outa255
			coef2 := 255*(1<<precisionBits) - coef1
			for c := 0; c < 3; c++ {
				tmp := uint32(src.Pix[s+c])*coef1 + uint32(dst.Pix[d+c])*coef2
				dst.Pix[d+c] = uint8(shiftForDiv255(tmp+(0x80<<precisionBits)) >> precisionBits)
			}
			dst.Pix[d+3] = uint8(shiftForDiv255(outa255 + 0x80))
		}
	}
}

// flipH Pillow transpose(FLIP_LEFT_RIGHT)。
func flipH(src *image.NRGBA) *image.NRGBA {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copy(out.Pix[out.PixOffset(w-1-x, y):out.PixOffset(w-1-x, y)+4], src.Pix[src.PixOffset(x, y):src.PixOffset(x, y)+4])
		}
	}
	return out
}

// ---------- 縮放（Pillow Resample.c） ----------

type filter struct {
	support float64
	fn      func(float64) float64
}

var (
	bicubic = filter{2, func(x float64) float64 {
		const a = -0.5
		if x < 0 {
			x = -x
		}
		if x < 1 {
			return ((a+2)*x-(a+3))*x*x + 1
		}
		if x < 2 {
			return (((x-5)*x+8)*x - 4) * a
		}
		return 0
	}}
	lanczos = filter{3, func(x float64) float64 {
		if -3 <= x && x < 3 {
			return sinc(x) * sinc(x/3)
		}
		return 0
	}}
)

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	x *= math.Pi
	return math.Sin(x) / x
}

const resamplePrecision = 32 - 8 - 2

// coeffs Pillow precompute_coeffs + normalize_coeffs_8bpc：每個輸出位置的起點、數量與定點係數。
func coeffs(inSize, outSize int, f filter) (bounds []int, kk []int32, ksize int) {
	scale := float64(inSize) / float64(outSize)
	filterscale := max(scale, 1.0)
	support := f.support * filterscale
	ksize = int(math.Ceil(support))*2 + 1
	pre := make([]float64, outSize*ksize)
	bounds = make([]int, outSize*2)
	for xx := 0; xx < outSize; xx++ {
		center := (float64(xx) + 0.5) * scale
		ww := 0.0
		ss := 1.0 / filterscale
		xmin := max(int(center-support+0.5), 0)
		xmax := min(int(center+support+0.5), inSize) - xmin
		k := pre[xx*ksize : (xx+1)*ksize]
		for x := 0; x < xmax; x++ {
			w := f.fn((float64(x+xmin) - center + 0.5) * ss)
			k[x] = w
			ww += w
		}
		for x := 0; x < xmax; x++ {
			if ww != 0 {
				k[x] /= ww
			}
		}
		bounds[xx*2], bounds[xx*2+1] = xmin, xmax
	}
	kk = make([]int32, len(pre))
	for i, v := range pre {
		if v < 0 {
			kk[i] = int32(-0.5 + v*(1<<resamplePrecision))
		} else {
			kk[i] = int32(0.5 + v*(1<<resamplePrecision))
		}
	}
	return bounds, kk, ksize
}

func clip8(in int32) uint8 {
	if in >= 1<<resamplePrecision<<8 {
		return 255
	}
	if in <= 0 {
		return 0
	}
	return uint8(in >> resamplePrecision)
}

// premultiplied Pillow 的 RGBa 影像（預乘 alpha），4 個 uint8 一組。
type premultiplied struct {
	w, h int
	pix  []uint8
}

func toRGBa(src *image.NRGBA) premultiplied {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	out := premultiplied{w, h, make([]uint8, w*h*4)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := src.PixOffset(x, y)
			d := (y*w + x) * 4
			a := uint32(src.Pix[s+3])
			for c := 0; c < 3; c++ {
				tmp := uint32(src.Pix[s+c])*a + 128
				out.pix[d+c] = uint8(((tmp >> 8) + tmp) >> 8)
			}
			out.pix[d+3] = uint8(a)
		}
	}
	return out
}

func fromRGBa(p premultiplied) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, p.w, p.h))
	for i := 0; i < len(p.pix); i += 4 {
		a := uint32(p.pix[i+3])
		for c := 0; c < 3; c++ {
			v := uint32(p.pix[i+c])
			if a != 255 && a != 0 {
				v = min(255, 255*v/a)
			}
			out.Pix[i+c] = uint8(v)
		}
		out.Pix[i+3] = uint8(a)
	}
	return out
}

func resampleHorizontal(in premultiplied, outW int, f filter) premultiplied {
	bounds, kk, ksize := coeffs(in.w, outW, f)
	out := premultiplied{outW, in.h, make([]uint8, outW*in.h*4)}
	for y := 0; y < in.h; y++ {
		for xx := 0; xx < outW; xx++ {
			xmin, xmax := bounds[xx*2], bounds[xx*2+1]
			k := kk[xx*ksize:]
			var ss [4]int32
			for c := range ss {
				ss[c] = 1 << (resamplePrecision - 1)
			}
			for x := 0; x < xmax; x++ {
				p := (y*in.w + x + xmin) * 4
				for c := 0; c < 4; c++ {
					ss[c] += int32(in.pix[p+c]) * k[x]
				}
			}
			d := (y*outW + xx) * 4
			for c := 0; c < 4; c++ {
				out.pix[d+c] = clip8(ss[c])
			}
		}
	}
	return out
}

func resampleVertical(in premultiplied, outH int, f filter) premultiplied {
	bounds, kk, ksize := coeffs(in.h, outH, f)
	out := premultiplied{in.w, outH, make([]uint8, in.w*outH*4)}
	for yy := 0; yy < outH; yy++ {
		ymin, ymax := bounds[yy*2], bounds[yy*2+1]
		k := kk[yy*ksize:]
		for x := 0; x < in.w; x++ {
			var ss [4]int32
			for c := range ss {
				ss[c] = 1 << (resamplePrecision - 1)
			}
			for y := 0; y < ymax; y++ {
				p := ((y+ymin)*in.w + x) * 4
				for c := 0; c < 4; c++ {
					ss[c] += int32(in.pix[p+c]) * k[y]
				}
			}
			d := (yy*in.w + x) * 4
			for c := 0; c < 4; c++ {
				out.pix[d+c] = clip8(ss[c])
			}
		}
	}
	return out
}

// resize Pillow Image.resize(size, filter)（RGBA：先轉預乘、縮放後再轉回；尺寸相同時回傳複本）。
func resize(src *image.NRGBA, w, h int, f filter) *image.NRGBA {
	if src.Rect.Dx() == w && src.Rect.Dy() == h {
		return clone(src)
	}
	p := toRGBa(src)
	if p.w != w {
		p = resampleHorizontal(p, w, f)
	}
	if p.h != h {
		p = resampleVertical(p, h, f)
	}
	return fromRGBa(p)
}

// newCanvas Pillow Image.new("RGBA", size)：全透明。
func newCanvas(w, h int) *image.NRGBA {
	c := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(c, c.Rect, image.Transparent, image.Point{}, draw.Src)
	return c
}
