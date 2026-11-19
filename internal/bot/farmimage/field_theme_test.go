package farmimage

import (
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"farmgm/internal/game/cosmetics"
)

// 產生農田主題遮罩（設定 FARMGM_FIELD_MASK_GEN=1 時覆寫 farm/field/masks/，手動修過的遮罩會被蓋掉）。
func TestGenerateFieldMasks(t *testing.T) {
	if os.Getenv("FARMGM_FIELD_MASK_GEN") == "" {
		t.Skip("設定 FARMGM_FIELD_MASK_GEN=1 才產生")
	}
	ResetCache()
	m, err := getManifest()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("farm", "field", "masks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, base := range m.Field.Bases {
		src, err := img(base)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join("farm", filepath.FromSlash(fieldMaskPath(base))))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, autoFieldMask(src, cellRects(m))); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func cellRects(m *manifest) []rect {
	var out []rect
	for _, c := range m.Field.Cells {
		out = append(out, c.Rect)
	}
	return out
}

// 每張底圖都要有遮罩，每個主題都能畫（設定 FARMGM_STYLE_PREVIEW=資料夾 時輸出 4～16 格與 32 格的預覽）。
func TestThemedField(t *testing.T) {
	ResetCache()
	m, err := getManifest()
	if err != nil {
		t.Fatal(err)
	}
	for n, base := range m.Field.Bases {
		src, _ := img(base)
		mask, err := img(fieldMaskPath(base))
		if err != nil {
			t.Fatalf("%s 沒有遮罩：%v", n, err)
		}
		if mask.Rect != src.Rect {
			t.Fatalf("%s 遮罩大小 %v 和底圖 %v 不同", n, mask.Rect, src.Rect)
		}
	}
	for _, theme := range cosmetics.Themes { // 目前的主題都有完整的細節圖（新主題可以沒有）
		for _, kind := range []string{"plot", "rail", "cap", "dot"} {
			if sprite, err := themeDetail(theme, kind); err != nil || sprite == nil {
				t.Fatalf("%s 缺少細節圖 %s：%v", theme.Code, kind, err)
			}
		}
	}
	// 已經下架的主題（櫻花）當成預設外觀
	if _, err := FarmPNGStyled(repeat(plot("wheat", "growing", 0.5, "", ""), 6), nil, nil, Look{Theme: "sakura"}); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("FARMGM_STYLE_PREVIEW")
	for _, theme := range cosmetics.Themes {
		for _, n := range []int{4, 6, 9, 12, 16, 32} {
			plots := repeat(plot("wheat", "growing", 0.5, "", ""), n)
			data, err := FarmPNGStyled(plots, map[string]int{"dog": 1}, nil, Look{Theme: theme.Code})
			if err != nil || len(data) == 0 {
				t.Fatal(theme.Code, n, err)
			}
			if dir != "" {
				_ = os.WriteFile(filepath.Join(dir, "field_"+theme.Code+"_"+strconv.Itoa(n)+".png"), data, 0o644)
			}
		}
	}
}
