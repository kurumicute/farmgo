package farmimage

import (
	"bytes"
	"image/png"
	"os"
	"testing"
	"time"

	"farmgm/internal/game/mushroom"
	"farmgm/internal/services"
)

func TestCellarPNG(t *testing.T) {
	now := time.Now()
	v := services.MushroomCellarView{Unlocked: true, Nutrients: []services.MushroomNutrientView{
		{Code: "grain", Plots: 2}, {Code: "fruit", Plots: 1},
	}}
	for i, p := range []float64{0, 0.4, 0.8, 1} {
		v.Beds = append(v.Beds, services.MushroomBedView{No: i + 1, Progress: p, Ready: p >= 1, Sawdust: i + 1, ReadyAt: now})
	}
	beds := CellarBedsFromView(v)
	if len(beds) != mushroom.BedCount || beds[0].Species[0] != "shiitake" || beds[3].Species[2] != "glowcap" {
		t.Fatalf("%+v", beds)
	}
	data, err := CellarPNG(beds)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil || im.Bounds().Dx() != 768 || im.Bounds().Dy() != 512 {
		t.Fatal(err, im.Bounds())
	}
	if out := os.Getenv("FARMGM_CELLAR_PREVIEW"); out != "" { // 手動檢查畫面
		_ = os.WriteFile(out, data, 0o644)
	}
	// 沒有作物時全部是香菇（穀物養分）
	empty := CellarBedsFromView(services.MushroomCellarView{Beds: v.Beds[:1]})
	if empty[0].Species[0] != "shiitake" || empty[0].Species[2] != "shiitake" {
		t.Fatalf("%+v", empty)
	}
}
