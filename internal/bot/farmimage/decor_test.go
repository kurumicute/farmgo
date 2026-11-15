package farmimage

import (
	"bytes"
	"image/png"
	"os"
	"testing"

	"farmgm/internal/game/decor"
)

// TestDecorBand 有擺裝飾時農田左右加庭院、上方加草地帶；沒有擺時和原本完全相同；所有裝飾都有圖片。
func TestDecorBand(t *testing.T) {
	plots := mixedPlots()
	plain, err := FarmPNG(plots, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, err := FarmPNGWithDecor(plots, nil, []string{"", "", "", "", "", ""})
	if err != nil || !bytes.Equal(plain, same) {
		t.Fatal("沒有擺裝飾時圖片要和原本相同", err)
	}
	data, err := FarmPNGWithDecor(plots, map[string]int{"cat": 1}, []string{"scarecrow", "", "fountain", "gold_cat_statue", "", "xmas_tree"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := png.Decode(bytes.NewReader(plain))
	b, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := getManifest()
	// 左右各加一條庭院、上方加草地帶：16 格農田 512×502 → 704×602，接近正方形
	if b.Bounds().Dx() != a.Bounds().Dx()+2*m.DecorSides.Width || b.Bounds().Dy() != a.Bounds().Dy()+m.Pets.BandHeight {
		t.Fatal(a.Bounds(), b.Bounds())
	}
	// 沒有寵物也一樣加上方草地帶
	noPet, err := FarmPNGWithDecor(plots, nil, []string{"barrel"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := png.Decode(bytes.NewReader(noPet)); c.Bounds() != b.Bounds() {
		t.Fatal(c.Bounds(), b.Bounds())
	}
	for _, it := range decor.Catalog {
		im, err := img(decor.ImagePath(it.Code))
		if err != nil || im.Rect.Dx() != 64 || im.Rect.Dy() != 64 {
			t.Errorf("%s：%v", it.Code, err)
		}
	}
	if dir := os.Getenv("FARMGM_DECOR_PREVIEW"); dir != "" {
		_ = os.WriteFile(dir, data, 0o644)
	}
}
