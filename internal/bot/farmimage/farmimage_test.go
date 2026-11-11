package farmimage

// 對應 Python tests/test_farm_image.py（預先產生的圖命中、即時拼圖、建築畫面與場景圖）。另有 Go 版的
// TestCanvasMatchesPython：拼好、減色前的畫面與 Python 逐像素相同（以 Python 實際輸出的 SHA-256 比對）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"farmgm/internal/services"
)

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func sizeOf(t *testing.T, data []byte) [2]int {
	b := decode(t, data).Bounds()
	return [2]int{b.Dx(), b.Dy()}
}

// colorsOf 調色盤 PNG 實際用到的顏色數。
func colorsOf(t *testing.T, data []byte) int {
	t.Helper()
	p, ok := decode(t, data).(*image.Paletted)
	if !ok {
		t.Fatal("應為調色盤 PNG")
	}
	used := map[uint8]bool{}
	for _, i := range p.Pix {
		used[i] = true
	}
	return len(used)
}

func fileSize(t *testing.T, rel string) int {
	data, err := fs.ReadFile(Assets, rel)
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

// preset 預先產生的圖：沿用檔案內容，但上傳前轉成 PanelColors 色（第一次轉換後快取）。
func preset(t *testing.T, rel string) []byte {
	t.Helper()
	out := must(t)(FilePNG(rel))
	again := must(t)(FilePNG(rel))
	if &out[0] != &again[0] { // 快取：同一個檔案只轉一次
		t.Fatal("沒有快取")
	}
	if colorsOf(t, out) > PanelColors || len(out) >= fileSize(t, rel) { // 比原本 256 色的素材檔小
		t.Fatalf("%s：%d 色、%d bytes（原檔 %d）", rel, colorsOf(t, out), len(out), fileSize(t, rel))
	}
	return out
}

func must(t *testing.T) func([]byte, error) []byte {
	return func(b []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

func plot(crop, state string, progress float64, quality, mutation string) Plot {
	if quality == "" {
		quality = "normal"
	}
	if mutation == "" {
		mutation = "none"
	}
	return Plot{crop, state, progress, quality, mutation}
}

func repeat(p Plot, n int) []Plot {
	out := make([]Plot, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func TestEmptyFieldUsesBaseFile(t *testing.T) {
	got := must(t)(FarmPNG(repeat(plot("", "empty", 0, "", ""), 4), nil))
	if !bytes.Equal(got, preset(t, "field/base_04.png")) {
		t.Fatal("空地應使用底圖")
	}
}

func TestSameCropSameStageUsesPreset(t *testing.T) {
	if got := must(t)(FarmPNG(repeat(plot("wheat", "ready", 0, "", ""), 4), nil)); !bytes.Equal(got, preset(t, "field/presets/04_wheat_ready.png")) {
		t.Fatal("04_wheat_ready")
	}
	if got := must(t)(FarmPNG(repeat(plot("carrot", "growing", 0.1, "", ""), 16), nil)); !bytes.Equal(got, preset(t, "field/presets/16_sprout.png")) {
		t.Fatal("16_sprout")
	}
}

func TestCropHasItsOwnGrowthStages(t *testing.T) {
	m, err := getManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, crop := range []string{"amber_melon", "moonflower", "tomato", "pumpkin", "grape", "watermelon"} {
		layers := map[string]bool{}
		for _, c := range []struct {
			stage, state string
			progress     float64
		}{{"sprout", "growing", 0.1}, {"young", "growing", 0.5}, {"unripe", "growing", 0.9}, {"ready", "ready", 1}, {"withered", "withered", 1}} {
			rel, err := PlantLayer(plot(crop, c.state, c.progress, "", ""), c.stage)
			if err != nil || rel != "crops/"+crop+"/"+c.stage+".png" {
				t.Fatal(crop, c.stage, rel, err)
			}
			data, _ := fs.ReadFile(Assets, rel)
			im := decode(t, data)
			if _, ok := im.(*image.NRGBA); !ok || im.Bounds().Dx() != 64 || im.Bounds().Dy() != 64 {
				t.Fatalf("%s：%T %v", rel, im, im.Bounds())
			}
			if !hasPixels(toNRGBA(im)) {
				t.Fatal(rel, "是空白圖")
			}
			layers[string(data)] = true
		}
		if len(layers) != 5 { // 每個階段的圖都不一樣
			t.Fatal(crop, len(layers))
		}
		// 種滿同一種時也要畫出專屬的圖，不能命中舊的預先產生圖
		if hit, err := presetFor(m, repeat(plot(crop, "growing", 0.1, "", ""), 4)); hit != nil || err != nil {
			t.Fatal(crop, hit, err)
		}
		if s := sizeOf(t, must(t)(FarmPNG(repeat(plot(crop, "ready", 0, "", ""), 16), nil))); s != [2]int{512, 502} {
			t.Fatal(s)
		}
	}
}

func hasPixels(im *image.NRGBA) bool {
	for i := 3; i < len(im.Pix); i += 4 {
		if im.Pix[i] != 0 {
			return true
		}
	}
	return false
}

func TestCropWithoutOwnArtFallsBackToGeneric(t *testing.T) {
	cases := []struct {
		p           Plot
		stage, want string
	}{
		{plot("corn", "growing", 0.1, "", ""), "sprout", "crops/_generic/sprout.png"},
		{plot("corn", "ready", 0, "", ""), "ready", "crops/_generic/ready.png"},
		{plot("tomato", "waiting_water", 0, "", ""), "seeds", "crops/_generic/seeds.png"},
		{plot("tomato", "ready", 0, "", "variant"), "ready", "crops/tomato/variant_ready.png"},
		{plot("grape", "ready", 0, "", "variant"), "ready", "crops/grape/ready.png"},
	}
	for _, c := range cases {
		if got, err := PlantLayer(c.p, c.stage); err != nil || got != c.want {
			t.Errorf("%+v：%s %v", c.p, got, err)
		}
	}
}

func TestPresetWithQualityBadgesIsRedrawn(t *testing.T) {
	plots := append([]Plot{plot("wheat", "ready", 0, "legendary", "")}, repeat(plot("wheat", "ready", 0, "", ""), 3)...)
	got := must(t)(FarmPNG(plots, nil))
	if bytes.Equal(got, preset(t, "field/presets/04_wheat_ready.png")) || sizeOf(t, got) != [2]int{512, 502} || colorsOf(t, got) > PanelColors {
		t.Fatal("應重新畫上標記")
	}
}

func mixedPlots() []Plot {
	return []Plot{plot("wheat", "waiting_water", 0, "", ""), plot("carrot", "growing", 0.5, "", ""),
		plot("tomato", "growing", 0.9, "", ""), plot("strawberry", "ready", 1, "rare", "variant"),
		plot("dragonfruit", "ready", 1, "good", "giant"), plot("dragonfruit", "withered", 0, "", ""), plot("", "empty", 0, "", "")}
}

func TestMixedAndUnknownCropsRender(t *testing.T) {
	if s := sizeOf(t, must(t)(FarmPNG(mixedPlots(), nil))); s != [2]int{512, 502} {
		t.Fatal(s)
	}
}

func TestMoreThanSixteenPlotsDrawsSixteen(t *testing.T) {
	var plots []Plot
	for range 20 {
		plots = append(plots, plot("wheat", "growing", 0.5, "", ""), plot("carrot", "ready", 0, "", ""))
	}
	if s := sizeOf(t, must(t)(FarmPNG(plots, nil))); s != [2]int{512, 502} {
		t.Fatal(s)
	}
}

func TestStageThresholds(t *testing.T) {
	for _, c := range []struct {
		p    Plot
		want string
	}{{plot("wheat", "waiting_water", 0, "", ""), "seeds"}, {plot("wheat", "growing", 0.39, "", ""), "sprout"},
		{plot("wheat", "growing", 0.4, "", ""), "young"}, {plot("wheat", "growing", 0.75, "", ""), "unripe"}} {
		if got, _ := StageOf(c.p); got != c.want {
			t.Errorf("%+v：%s", c.p, got)
		}
	}
}

func workshop(barn, mill, bakery, jam int, busy map[string]*services.OrderView) services.WorkshopStatus {
	return services.WorkshopStatus{Counts: map[string]int{"barn": barn, "mill": mill, "bakery": bakery, "jam_workshop": jam}, Busy: busy}
}

func TestIdleYardUsesBaseFile(t *testing.T) {
	if !bytes.Equal(must(t)(YardPNG(workshop(0, 0, 0, 0, nil), nil)), preset(t, "yard/base_0_000.png")) {
		t.Fatal("base_0_000")
	}
	if !bytes.Equal(must(t)(YardPNG(workshop(2, 1, 0, 1, nil), nil)), preset(t, "yard/base_2_101.png")) {
		t.Fatal("base_2_101")
	}
}

func fullYard() (map[string]*services.OrderView, []services.AnimalView) {
	busy := map[string]*services.OrderView{"mill": {State: "in_progress", OutputCode: "flour"},
		"bakery": {State: "ready", OutputCode: "bread"}, "jam_workshop": {State: "ready", OutputCode: "unknown_goods"}}
	var animals []services.AnimalView // 超過 3 座畜舍的容量 12 隻，未知動物用剪影
	for i := range 15 {
		animals = append(animals, services.AnimalView{AnimalNo: i, Code: []string{"chicken", "cow", "sheep", "dragon"}[i%4],
			State: []string{"hungry", "ready", "producing"}[i%3], ProductCode: []string{"egg", "milk", "wool"}[i%3]})
	}
	return busy, animals
}

func TestFullYardWithAnimalsAndOrders(t *testing.T) {
	busy, animals := fullYard()
	if s := sizeOf(t, must(t)(YardPNG(workshop(3, 1, 1, 1, busy), animals))); s != [2]int{640, 640} {
		t.Fatal(s)
	}
	if s := sizeOf(t, must(t)(YardPNG(workshop(1, 0, 0, 0, nil), animals))); s != [2]int{640, 640} { // 只畫前 4 隻
		t.Fatal(s)
	}
}

// sameScene 固定場景圖：尺寸與原檔相同，且上傳的內容不會比原檔大。
func sameScene(t *testing.T, data []byte, rel string) bool {
	raw, _ := fs.ReadFile(Assets, rel)
	return sizeOf(t, data) == sizeOf(t, raw) && len(data) <= len(raw)
}

func TestShopIsFixedFile(t *testing.T) {
	a, b := must(t)(ShopPNG()), must(t)(ShopPNG())
	if !sameScene(t, a, "scenes/shop.png") || &a[0] != &b[0] { // 只轉換一次（快取）
		t.Fatal("shop")
	}
}

func TestCasinoAndExploreScenes(t *testing.T) {
	casino := must(t)(CasinoPNG())
	if !sameScene(t, casino, "scenes/casino.png") || len(casino) >= fileSize(t, "scenes/casino.png") { // 原檔沒有壓縮好
		t.Fatal("casino", len(casino), fileSize(t, "scenes/casino.png"))
	}
	for _, code := range []string{"grassland", "forest", "mine", "ruins"} { // 每個探險區域都有自己的圖
		if !sameScene(t, must(t)(ExplorePNG(code)), "scenes/explore_"+code+".png") {
			t.Fatal(code)
		}
	}
	if got := must(t)(ExplorePNG("unknown_area")); got != nil {
		t.Fatal("未知區域應沒有圖")
	}
	if !sameScene(t, must(t)(WorldBossPNG()), "scenes/world_boss.png") {
		t.Fatal("world_boss")
	}
}

func solid(im *image.NRGBA) map[[2]int]bool {
	out := map[[2]int]bool{}
	for y := 0; y < im.Rect.Dy(); y++ {
		for x := 0; x < im.Rect.Dx(); x++ {
			if im.Pix[im.PixOffset(x, y)+3] > 40 {
				out[[2]int{x, y}] = true
			}
		}
	}
	return out
}

func TestAnimalSpotsDoNotOverlapBuildingsOrEachOther(t *testing.T) {
	// 動物固定站在畜舍旁：12 個位置都不能和任何建築或其他動物的實心像素重疊（避免看起來像穿模）。
	m, err := getManifest()
	if err != nil {
		t.Fatal(err)
	}
	size := m.Yard.AnimalSize
	taken := map[[2]int]bool{}
	type placedRect struct {
		r    rect
		code string
	}
	var rects []placedRect
	for _, s := range m.Yard.BarnSlots {
		rects = append(rects, placedRect{s.Rect, "barn"})
	}
	for _, mc := range m.machines {
		rects = append(rects, placedRect{mc.Rect, mc.Code})
	}
	for _, pr := range rects {
		src, err := img(m.Buildings[pr.code])
		if err != nil {
			t.Fatal(err)
		}
		for p := range solid(resize(src, pr.r[2]-pr.r[0], pr.r[3]-pr.r[1], bicubic)) {
			taken[[2]int{pr.r[0] + p[0], pr.r[1] + p[1]}] = true
		}
	}
	var spots [][3]int
	for _, barn := range m.Yard.AnimalSpots {
		spots = append(spots, barn...)
	}
	if len(spots) != len(m.Yard.BarnSlots)*4 {
		t.Fatal(len(spots))
	}
	for _, code := range []string{"chicken", "cow", "sheep", "_generic"} {
		src, err := img(m.Animals[code])
		if err != nil {
			t.Fatal(err)
		}
		sprite := resize(src, size, size, lanczos)
		normal, flipped := solid(sprite), solid(flipH(sprite))
		occupied := map[[2]int]bool{}
		for p := range taken {
			occupied[p] = true
		}
		for _, s := range spots {
			px := normal
			if s[2] != 0 {
				px = flipped
			}
			for p := range px {
				q := [2]int{s[0] - size/2 + p[0], s[1] - size + p[1]}
				if occupied[q] {
					t.Fatalf("%s 在 (%d, %d) 與其他像素重疊", code, s[0], s[1])
				}
				if q[0] < 0 || q[0] >= m.Yard.Size[0] || q[1] < 0 || q[1] >= m.Yard.Size[1] {
					t.Fatalf("%s 超出畫面", code)
				}
			}
			for p := range px {
				occupied[[2]int{s[0] - size/2 + p[0], s[1] - size + p[1]}] = true
			}
		}
	}
}

func TestMissingAssetsRaise(t *testing.T) {
	old := Assets
	Assets = fstest.MapFS{}
	ResetCache()
	defer func() { Assets = old; ResetCache() }()
	if _, err := FarmPNG(repeat(plot("", "empty", 0, "", ""), 4), nil); err == nil {
		t.Fatal("缺少素材應回傳錯誤")
	}
}

// TestCanvasMatchesPython 拼好、減色前的畫面與 Python farm_image 逐像素相同（雜湊為 Python 12.3 Pillow 的實際輸出：
// 拼圖前把 encode 換成擷取畫面）。涵蓋濕土縮放（bicubic）、植物、狀態與品質標記、變種、寵物草地、建築泡泡（lanczos）與動物翻轉。
func TestCanvasMatchesPython(t *testing.T) {
	want := map[string]string{
		"mixed":   "6b8170e3f2c79b87a5f2c05a8086846eee1e8ebeabde9e41e771307e6fedab08",
		"badges":  "dbb3e8d530d01f2569d5022b77a0b83d71ffc5c97c6215e17c883b4726573d22",
		"pets":    "49450d355d6d0a740d0b3cbdd665f3dadd25595293d8bb4ee58f43f12e31bcb6",
		"sixteen": "34ec2457b21bf362ea8d24dd3f946701c4d247c5cc434cc0dc2df5c10b5111be",
		"variant": "f114bbe9c64ac28f6b2cdc381422eacf6db002e2e67b8ab03aed9d73f2af7fcb",
		"yard":    "0e32183123f67e702bf5aaaaed07c7e1a25e8125b350217feb5c2d9ca10680f1",
	}
	field := func(plots []Plot, pets map[string]int) *image.NRGBA {
		c, cached, err := fieldCanvas(plots, pets)
		if err != nil || cached != nil {
			t.Fatal(err, cached != nil)
		}
		return c
	}
	var sixteen []Plot
	for range 20 {
		sixteen = append(sixteen, plot("wheat", "growing", 0.5, "", ""), plot("carrot", "ready", 0, "", ""))
	}
	busy, animals := fullYard()
	yard, _, err := yardCanvas(workshop(3, 1, 1, 1, busy), animals)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*image.NRGBA{
		"mixed":   field(mixedPlots(), nil),
		"badges":  field(append([]Plot{plot("wheat", "ready", 0, "legendary", "")}, repeat(plot("wheat", "ready", 0, "", ""), 3)...), nil),
		"pets":    field(mixedPlots(), map[string]int{"dog": 1, "cat": 3}),
		"sixteen": field(sixteen, nil),
		"variant": field([]Plot{plot("tomato", "ready", 1, "legendary", "variant"), plot("amber_melon", "growing", 0.2, "", ""),
			plot("grape", "ready", 1, "good", "golden"), plot("pumpkin", "withered", 0, "", ""), plot("watermelon", "growing", 0.6, "", "")}, nil),
		"yard": yard,
	}
	for name, c := range got {
		sum := sha256.Sum256(c.Pix)
		if h := hex.EncodeToString(sum[:]); h != want[name] {
			t.Errorf("%s：畫面與 Python 不同（%s）", name, h)
		}
	}
}

func TestPlotsFromStatus(t *testing.T) {
	// Go 版：依 PlotNumber 排序、進度限制在 0–1、None 轉成空字串
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	watered, ready := now.Add(-30*time.Second), now.Add(30*time.Second)
	late := now.Add(-2 * time.Minute)
	wheat, good := "wheat", "good"
	st := services.FarmStatus{Now: now, Plots: []services.PlotInfo{
		{PlotNumber: 3, State: "growing", CropCode: &wheat, WateredAt: &watered, ReadyAt: &ready},
		{PlotNumber: 1, State: "empty"},
		{PlotNumber: 2, State: "ready", CropCode: &wheat, WateredAt: &late, ReadyAt: &watered, Quality: &good},
	}}
	got := PlotsFromStatus(st)
	if len(got) != 3 || got[0].State != "empty" || got[0].Crop != "" || got[1].Progress != 1 || got[1].Quality != "good" ||
		got[2].Progress != 0.5 {
		t.Fatalf("%+v", got)
	}
}

// TestQuantizedColorsMatchPillow 減色後每個像素的顏色與 Pillow FASTOCTREE 相同（雜湊為 Pillow 12.3 輸出再轉回 RGBA）。
// 調色盤的順序可能不同（C qsort 不穩定，次數相同的格子順序依平台而定），所以比對解碼後的顏色而不是 PNG 位元組。
func TestQuantizedColorsMatchPillow(t *testing.T) {
	busy, animals := fullYard()
	yard, _, err := yardCanvas(workshop(3, 1, 1, 1, busy), animals)
	if err != nil {
		t.Fatal(err)
	}
	field := func(pets map[string]int) *image.NRGBA {
		c, _, err := fieldCanvas(mixedPlots(), pets)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	casino, err := img("scenes/casino.png")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		src  *image.NRGBA
		n    int
		want string
	}{
		{"mixed", field(nil), 64, "6133a92a4a020d534240a8a78d152fd601fd9a4b1ad7ef66168476c7c6d50bf5"},
		{"yard", yard, 64, "865255ded122d17cdc4fe5dc1c702eb1c4754ad6314f35fa32e55f30e2c5048b"},
		{"pets", field(map[string]int{"dog": 1, "cat": 3}), 64, "cc491905d4b2e5546b7d9aa66c0decefddaa3b4777bbbaa62e3c2013a486ce8a"},
		{"casino", casino, 256, "61a1d2fa8d5a815134d3eaeaa16c70061d7ec93a49896e9c0759771cfa4dcdc8"},
	}
	for _, c := range cases {
		sum := sha256.Sum256(toNRGBA(quantize(c.src, c.n)).Pix)
		if h := hex.EncodeToString(sum[:]); h != c.want {
			t.Errorf("%s：減色結果與 Pillow 不同（%s）", c.name, h)
		}
	}
}
