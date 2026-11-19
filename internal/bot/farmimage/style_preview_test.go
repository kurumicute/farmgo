package farmimage

import (
	"os"
	"path/filepath"
	"testing"
)

// 預覽主題與寵物造型（設定 FARMGM_STYLE_PREVIEW=資料夾 時輸出 PNG，平常只檢查能產生）。
func TestStyledPreview(t *testing.T) {
	ResetCache()
	pets := map[string]int{"dog": 1, "cat": 1, "rabbit": 1, "bird": 1, "hedgehog": 1}
	plots := repeat(plot("wheat", "growing", 0.5, "", ""), 9)
	cases := map[string]Look{
		"snow":  {Theme: "snow", Skins: map[string]PetSkin{"dog": {Hat: "santa_hat"}, "cat": {Hat: "crown", Color: "pink"}}},
		"beach": {Theme: "beach", Skins: map[string]PetSkin{"rabbit": {Hat: "flower_crown"}, "bird": {Hat: "bow", Color: "blue"}}},
		"night": {Theme: "night", Skins: map[string]PetSkin{"hedgehog": {Hat: "wizard_hat", Color: "gold"}, "dog": {Hat: "straw_hat", Color: "mint"}}},
	}
	dir := os.Getenv("FARMGM_STYLE_PREVIEW")
	for name, look := range cases {
		data, err := FarmPNGStyled(plots, pets, []string{"fountain", "", "", "", "", "maple_tree"}, look)
		if err != nil || len(data) == 0 {
			t.Fatal(name, err)
		}
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), data, 0o644)
		}
	}
	// 沒有主題、只有造型（沒有擺裝飾）也要能產生
	if _, err := FarmPNGStyled(plots, map[string]int{"cat": 1}, nil, Look{Skins: map[string]PetSkin{"cat": {Hat: "bow"}}}); err != nil {
		t.Fatal(err)
	}
}
