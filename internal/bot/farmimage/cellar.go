package farmimage

// 地下菇房畫面（Go 版新增，2026-11-12）：底圖 mushroom/cellar.png（768×512，4 個木框菇床），
// 依菇床的生長進度在床上畫菇（越接近長好越大），長好的菇床加上「可收成」泡泡、生長中的加上沙漏。
// 畫出的菇種依上方農田的養分比例（與投入的木屑）示意，實際收成時才擲骰。

import (
	"fmt"
	"strings"

	"farmgm/internal/game/mushroom"
	"farmgm/internal/services"
)

const cellarBase = "mushroom/cellar.png"

// cellarBeds 4 個菇床的土壤範圍（底圖座標，x0, y0, x1, y1），依菇床編號。
var cellarBeds = [mushroom.BedCount]rect{
	{242, 184, 354, 240},
	{404, 184, 516, 240},
	{242, 306, 354, 362},
	{404, 306, 516, 362},
}

// CellarBed 一個菇床要畫的內容。
type CellarBed struct {
	Ready   bool
	Size    int      // 菇的高度（像素）
	Species []string // 由左到右要畫的菇（最多 3 朵）
}

const cellarSlots = 3

// CellarBedsFromView 由菇房畫面資料決定每個菇床要畫的菇：普通菇依養分比例分配到 3 個位置，
// 投入的木屑越多，換成稀有菇的位置越多（2 個以上 1 朵、投滿 2 朵）。
func CellarBedsFromView(v services.MushroomCellarView) []CellarBed {
	// 依養分比例分配 3 個位置（最大餘數法，NutrientOrder 順序）
	var slots []services.MushroomNutrientView
	total := 0
	for _, n := range v.Nutrients {
		total += n.Plots
	}
	if total > 0 {
		type share struct {
			n    services.MushroomNutrientView
			k    int
			frac int
		}
		shares := make([]share, len(v.Nutrients))
		used := 0
		for i, n := range v.Nutrients {
			k := n.Plots * cellarSlots / total
			shares[i] = share{n, k, n.Plots*cellarSlots - k*total}
			used += k
		}
		for used < cellarSlots {
			best := 0
			for i := range shares {
				if shares[i].frac > shares[best].frac {
					best = i
				}
			}
			shares[best].k++
			shares[best].frac = -1
			used++
		}
		for _, s := range shares {
			for range s.k {
				slots = append(slots, s.n)
			}
		}
	}
	out := make([]CellarBed, 0, len(v.Beds))
	for _, b := range v.Beds {
		bed := CellarBed{Ready: b.Ready, Size: cellarSize(b.Progress)}
		rare := 0
		switch {
		case b.Sawdust >= mushroom.MaxSawdust:
			rare = 2
		case b.Sawdust >= 2:
			rare = 1
		}
		for i := range cellarSlots {
			n := mushroom.Nutrients[mushroom.DefaultNutrient]
			if i < len(slots) {
				n = mushroom.Nutrients[slots[i].Code]
			}
			code := n.Common
			// 稀有菇放在中間與右邊
			if (rare >= 1 && i == 1) || (rare >= 2 && i == 2) {
				code = n.Rare
			}
			bed.Species = append(bed.Species, code)
		}
		out = append(out, bed)
	}
	return out
}

func cellarKey(beds []CellarBed) string {
	var sb strings.Builder
	for _, b := range beds {
		fmt.Fprintf(&sb, "%t/%d/%s;", b.Ready, b.Size, strings.Join(b.Species, ","))
	}
	return sb.String()
}

var cellarMemo = map[string][]byte{}

// cellarMemoMax 菇房圖片快取上限：畫面組合很多，超過時整個清空，避免記憶體無限成長。
const cellarMemoMax = 256

// cellarSize 生長大小只分 4 段（剛開始、三分之一、三分之二、長好），讓相同畫面能共用快取。
func cellarSize(progress float64) int {
	step := min(3, int(progress*3))
	return 18 + step*10
}

// CellarPNG 地下菇房圖片。相同畫面（生長大小、菇種與狀態都相同）只產生一次。
func CellarPNG(beds []CellarBed) ([]byte, error) {
	if len(beds) == 0 {
		return FilePNG(cellarBase)
	}
	mu.Lock()
	if len(cellarMemo) >= cellarMemoMax {
		cellarMemo = map[string][]byte{}
	}
	mu.Unlock()
	return memo(&cellarMemo, cellarKey(beds), func() ([]byte, error) {
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		base, err := img(cellarBase)
		if err != nil {
			return nil, err
		}
		canvas := clone(base)
		for i, b := range beds {
			if i >= len(cellarBeds) {
				break
			}
			r := cellarBeds[i]
			w := r[2] - r[0]
			for j, code := range b.Species {
				src, err := img("mushroom/" + code + ".png")
				if err != nil {
					return nil, err
				}
				size := b.Size
				if j == 1 { // 中間那朵稍大，看起來比較自然
					size += size / 6
				}
				sw := max(1, src.Bounds().Dx()*size/src.Bounds().Dy())
				sprite := resize(src, sw, size, lanczos)
				cx := r[0] + w*(2*j+1)/(2*len(b.Species))
				alphaComposite(canvas, sprite, cx-sw/2, r[3]-4-size)
			}
			icon := m.Badges.Timer
			if b.Ready {
				icon = m.Badges.Ready
			}
			if err := drawBubble(m, canvas, [2]int{r[2] - 14, r[1] - 26}, icon); err != nil {
				return nil, err
			}
		}
		return encodePaletteDynamic(canvas, PanelColors)
	})
}
