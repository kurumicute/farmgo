package farmimage

import (
	"bytes"
	"image/png"
	"os"
	"testing"

	"farmgm/internal/game/decor"
)

// TestDecorBand 有擺裝飾時圖片下方多一條庭院擺飾區；沒有擺時和原本完全相同；所有裝飾都有圖片。
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
	if b.Bounds().Dx() != a.Bounds().Dx() || b.Bounds().Dy() != a.Bounds().Dy()+m.DecorBand.Height+m.Pets.BandHeight {
		t.Fatal(a.Bounds(), b.Bounds())
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
