// Package farmimage 農場圖片（對應 Python app/discord_bot/farm_image.py）：依狀態拼出農田（4～16 格）、建築畫面與
// 固定場景（商店、賭場、山洞、世界 Boss）的 PNG。素材與規則見 farm/manifest.json（Python app/discord_bot/farm 的原樣複製，
// 不含說明檔），以 go:embed 打包。
//
// 素材只讀一次並快取；預先產生的圖或底圖命中時沿用（第一次轉成 PanelColors 色後快取，之後回傳同一份位元組）。
// 拼圖結果（減色前）與 Pillow 逐像素相同（pillow.go）；減色演算法不同，見 quantize.go。可同時被多個 goroutine 使用。
package farmimage

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strconv"
	"sync"

	"farmgm/internal/services"
)

//go:embed all:farm
var embedded embed.FS

// PanelColors 農田與建築畫面上傳時的色數：64 色比 256 色小約 3～5 成，像素風畫面肉眼幾乎看不出差別。
// 固定場景圖不受影響（256 色）。
const PanelColors = 64

var stateBadge = map[string]string{"seeds": "need_water", "sprout": "timer", "young": "timer", "unripe": "timer", "ready": "ready"}

// Assets 素材檔案系統（測試可換成其他位置）；改變後要呼叫 ResetCache。
var Assets fs.FS = mustSub(embedded, "farm")

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}

// ---------- manifest ----------

type rect [4]int

type manifest struct {
	Layering struct {
		Thresholds struct {
			Sprout float64 `json:"sprout"`
			Young  float64 `json:"young"`
		} `json:"growing_thresholds"`
	} `json:"layering"`
	Crops  map[string]map[string]json.RawMessage `json:"crops"`
	Badges struct {
		NeedWater string            `json:"need_water"`
		Timer     string            `json:"timer"`
		Ready     string            `json:"ready"`
		Hungry    string            `json:"hungry"`
		Bubble    string            `json:"bubble"`
		Quality   map[string]string `json:"quality"`
		Mutation  map[string]string `json:"mutation"`
	} `json:"badges"`
	Field struct {
		Slots      int                                   `json:"slots"`
		MinPlots   int                                   `json:"min_plots"`
		Cells      []struct{ Rect rect }                 `json:"cells"`
		Bases      map[string]string                     `json:"bases"`
		WetOverlay string                                `json:"wet_overlay"`
		Presets    map[string]map[string]json.RawMessage `json:"presets"`
	} `json:"field"`
	Pets struct {
		BandHeight int             `json:"band_height"`
		BandTile   string          `json:"band_tile"`
		Sprites    json.RawMessage `json:"sprites"`
	} `json:"pets"`
	Yard struct {
		Size         [2]int                `json:"size"`
		MachineOrder []string              `json:"machine_order"`
		BarnSlots    []struct{ Rect rect } `json:"barn_slots"`
		Machines     json.RawMessage       `json:"machines"`
		AnimalSize   int                   `json:"animal_size"`
		AnimalSpots  [][][3]int            `json:"animal_spots"`
		Bases        map[string]string     `json:"bases"`
	} `json:"yard"`
	Animals   map[string]string `json:"animals"`
	Products  map[string]string `json:"products"`
	Goods     map[string]string `json:"goods"`
	Buildings map[string]string `json:"buildings"`
	Scenes    map[string]string `json:"scenes"`

	// 依 JSON 順序展開（Python dict 的走訪順序）
	petSprites []petSprite
	machines   []machine
}

type petSprite struct {
	Code   string
	File   string `json:"file"`
	Anchor string `json:"anchor"`
	Margin int    `json:"margin"`
	Bottom int    `json:"bottom"`
}

type machine struct {
	Code   string
	Rect   rect   `json:"rect"`
	Bubble [2]int `json:"bubble"`
}

// orderedObject 依 JSON 鍵順序解碼物件的每個值。
func orderedObject(raw json.RawMessage, each func(key string, value json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("不是 JSON 物件")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := each(tok.(string), v); err != nil {
			return err
		}
	}
	return nil
}

var (
	mu        sync.Mutex
	loaded    *manifest
	images    = map[string]*image.NRGBA{}
	fileCache = map[string][]byte{}
	petCache  = map[string][]byte{}
	sceneMemo = map[string][]byte{}
)

// ResetCache 清除已讀取的 manifest 與素材（改變 Assets 後使用）。
func ResetCache() {
	mu.Lock()
	defer mu.Unlock()
	loaded = nil
	images = map[string]*image.NRGBA{}
	fileCache = map[string][]byte{}
	petCache = map[string][]byte{}
	sceneMemo = map[string][]byte{}
	cellarMemo = map[string][]byte{}
}

func getManifest() (*manifest, error) {
	mu.Lock()
	defer mu.Unlock()
	if loaded != nil {
		return loaded, nil
	}
	data, err := fs.ReadFile(Assets, "manifest.json")
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	err = orderedObject(m.Pets.Sprites, func(k string, v json.RawMessage) error {
		s := petSprite{Code: k}
		m.petSprites = append(m.petSprites, s)
		return json.Unmarshal(v, &m.petSprites[len(m.petSprites)-1])
	})
	if err != nil {
		return nil, err
	}
	err = orderedObject(m.Yard.Machines, func(k string, v json.RawMessage) error {
		m.machines = append(m.machines, machine{Code: k})
		return json.Unmarshal(v, &m.machines[len(m.machines)-1])
	})
	if err != nil {
		return nil, err
	}
	loaded = &m
	return loaded, nil
}

// img 讀取素材（快取；呼叫端不可修改，要畫的話先 clone）。
func img(rel string) (*image.NRGBA, error) {
	mu.Lock()
	cached, ok := images[rel]
	mu.Unlock()
	if ok {
		return cached, nil
	}
	data, err := fs.ReadFile(Assets, rel)
	if err != nil {
		return nil, err
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("素材 %s：%w", rel, err)
	}
	out := toNRGBA(decoded)
	mu.Lock()
	images[rel] = out
	mu.Unlock()
	return out, nil
}

// memoCall 產生中的快取項目：同時要同一張圖的其他請求等它完成，不重複產生。
type memoCall struct {
	done chan struct{}
	v    []byte
	err  error
}

var (
	inflight        = map[string]*memoCall{}
	errMemoPanicked = errors.New("圖片產生失敗（panic）")
)

// memo 以 key 快取產生結果。同一張圖同時只產生一次：冷啟動時大量玩家同時開同一頁，原本每個請求都各自壓縮一次
// （2026-11-12 面板壓測：500 人同時操作時約佔 Bot CPU 的 4 成）。失敗不快取，下一次重新產生。
func memo(cache *map[string][]byte, key string, produce func() ([]byte, error)) ([]byte, error) {
	mu.Lock()
	if v, ok := (*cache)[key]; ok {
		mu.Unlock()
		return v, nil
	}
	k := fmt.Sprintf("%p\x00%s", cache, key)
	if c, ok := inflight[k]; ok {
		mu.Unlock()
		<-c.done
		return c.v, c.err
	}
	c := &memoCall{done: make(chan struct{}), err: errMemoPanicked}
	inflight[k] = c
	mu.Unlock()
	defer func() { // produce panic 時等待中的請求也會收到錯誤，不會永遠卡住
		mu.Lock()
		delete(inflight, k)
		if c.err == nil {
			(*cache)[key] = c.v
		}
		mu.Unlock()
		close(c.done)
	}()
	c.v, c.err = produce()
	if c.err != nil {
		c.v = nil
	}
	return c.v, c.err
}

// FilePNG 預先產生的農田圖／底圖（素材檔為 256 色）轉成 PanelColors 色後快取；每個檔案只轉一次。
func FilePNG(rel string) ([]byte, error) {
	return memo(&fileCache, rel, func() ([]byte, error) {
		src, err := img(rel)
		if err != nil {
			return nil, err
		}
		return encodePalette(src, PanelColors)
	})
}

// ---------- 農田 ----------

// Plot 拼圖用的一格土地。Quality、Mutation 為 "" 表示沒有（Python None）。
type Plot struct {
	Crop     string // "" = 空地
	State    string // empty／waiting_water／growing／ready／withered
	Progress float64
	Quality  string
	Mutation string
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// PlotsFromStatus 把服務層的 FarmStatus 轉成拼圖用的資料，依 PlotNumber 排序；State 直接用服務層算好的結果。
func PlotsFromStatus(st services.FarmStatus) []Plot {
	plots := slices.Clone(st.Plots)
	sort.SliceStable(plots, func(i, j int) bool { return plots[i].PlotNumber < plots[j].PlotNumber })
	out := make([]Plot, 0, len(plots))
	for _, p := range plots {
		progress := 0.0
		if p.WateredAt != nil && p.ReadyAt != nil && p.ReadyAt.After(*p.WateredAt) {
			progress = float64(st.Now.Sub(*p.WateredAt)) / float64(p.ReadyAt.Sub(*p.WateredAt))
		}
		out = append(out, Plot{Crop: deref(p.CropCode), State: string(p.State), Progress: min(1.0, max(0.0, progress)),
			Quality: deref(p.Quality), Mutation: deref(p.Mutation)})
	}
	return out
}

// StageOf 畫面階段：empty／seeds／sprout／young／unripe／ready／withered。
func StageOf(p Plot) (string, error) {
	switch p.State {
	case "waiting_water":
		return "seeds", nil
	case "growing":
		m, err := getManifest()
		if err != nil {
			return "", err
		}
		th := m.Layering.Thresholds
		switch {
		case p.Progress < th.Sprout:
			return "sprout", nil
		case p.Progress < th.Young:
			return "young", nil
		}
		return "unripe", nil
	}
	return p.State, nil
}

func cropFile(entry map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := entry[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// PlantLayer 植物圖層（"" 表示沒有）。網站新增、沒有專屬素材的作物用 _generic。
func PlantLayer(p Plot, stage string) (string, error) {
	m, err := getManifest()
	if err != nil {
		return "", err
	}
	generic := m.Crops["_generic"]
	crop, ok := m.Crops[p.Crop]
	if !ok {
		crop = generic
	}
	switch stage {
	case "empty":
		return "", nil
	case "seeds", "sprout", "young": // 有專屬幼苗圖的作物用自己的，其他共用
		if f, ok := cropFile(crop, stage); ok {
			return f, nil
		}
		f, _ := cropFile(generic, stage)
		return f, nil
	}
	if stage == "ready" && p.Mutation == "variant" {
		if raw, ok := crop["variant"]; ok {
			var v struct {
				Ready string `json:"ready"`
			}
			if json.Unmarshal(raw, &v) == nil {
				return v.Ready, nil
			}
		}
	}
	f, ok := cropFile(crop, stage)
	if !ok {
		return "", fmt.Errorf("作物 %q 沒有 %s 階段的素材", p.Crop, stage)
	}
	return f, nil
}

// attrBadges 品質與突變標記（只有可收成時才畫）。
func attrBadges(m *manifest, p Plot) []string {
	var out []string
	if f, ok := m.Badges.Quality[p.Quality]; ok {
		out = append(out, f)
	}
	if f, ok := m.Badges.Mutation[p.Mutation]; ok {
		out = append(out, f)
	}
	return out
}

func drawAttrBadges(m *manifest, canvas *image.NRGBA, r rect, p Plot) error {
	for i, rel := range attrBadges(m, p) {
		b, err := img(rel)
		if err != nil {
			return err
		}
		alphaComposite(canvas, b, r[0]+2+i*22, r[1]+2)
	}
	return nil
}

func drawPlot(m *manifest, canvas *image.NRGBA, r rect, p Plot) error {
	x0, y0, x1, y1 := r[0], r[1], r[2], r[3]
	w, h := x1-x0, y1-y0
	stage, err := StageOf(p)
	if err != nil {
		return err
	}
	if p.State == "growing" || p.State == "ready" { // 1 濕土
		wet, err := img(m.Field.WetOverlay)
		if err != nil {
			return err
		}
		alphaComposite(canvas, resize(wet, w, h, bicubic), x0, y0)
	}
	layer, err := PlantLayer(p, stage) // 2 植物
	if err != nil {
		return err
	}
	if layer != "" {
		sprite, err := img(layer)
		if err != nil {
			return err
		}
		alphaComposite(canvas, sprite, x0+floorDiv(w-sprite.Rect.Dx(), 2), y1-sprite.Rect.Dy()-2)
	}
	if badge, ok := stateBadge[stage]; ok { // 3 右上：狀態
		rel := map[string]string{"need_water": m.Badges.NeedWater, "timer": m.Badges.Timer, "ready": m.Badges.Ready}[badge]
		b, err := img(rel)
		if err != nil {
			return err
		}
		alphaComposite(canvas, b, x1-24-2, y0+2)
	}
	if stage == "ready" { // 4 左上：品質、突變
		return drawAttrBadges(m, canvas, r, p)
	}
	return nil
}

// floorDiv Python //（負數向下取整）。
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

type presetHit struct {
	rel    string
	badges bool // 還要補品質／突變標記
}

// presetFor 全部同作物、同階段時，回傳預先產生的圖。
func presetFor(m *manifest, plots []Plot) (*presetHit, error) {
	presets, ok := m.Field.Presets[strconv.Itoa(len(plots))]
	if !ok {
		return nil, nil
	}
	stages := map[string]bool{}
	for _, p := range plots {
		s, err := StageOf(p)
		if err != nil {
			return nil, err
		}
		stages[s] = true
	}
	if len(stages) != 1 {
		return nil, nil
	}
	stage := slices.Collect(maps.Keys(stages))[0]
	if stage == "empty" {
		f, _ := cropFile(presets, "empty")
		return &presetHit{f, false}, nil
	}
	crops := map[string]bool{}
	for _, p := range plots {
		crops[p.Crop] = true
	}
	if len(crops) != 1 {
		return nil, nil
	}
	crop := plots[0].Crop
	raw, ok := presets[crop]
	if !ok {
		return nil, nil
	}
	if stage == "ready" {
		for _, p := range plots {
			if p.Mutation == "variant" {
				return nil, nil // 變種外觀不同，要即時拼
			}
		}
	}
	var stagesOf map[string]string
	if err := json.Unmarshal(raw, &stagesOf); err != nil {
		return nil, err
	}
	rel, ok := stagesOf[stage]
	if !ok {
		return nil, fmt.Errorf("預先產生的圖缺少 %s／%s", crop, stage)
	}
	badges := false
	if stage == "ready" {
		for _, p := range plots {
			if len(attrBadges(m, p)) > 0 {
				badges = true
			}
		}
	}
	return &presetHit{rel, badges}, nil
}

// fieldCanvas 拼好、減色前的農田畫面（Python farm_png 中 encode 之前的 canvas）；cached 不為 nil 時表示可以直接
// 回傳的快取 PNG（預先產生的圖，不必重拼）。
func fieldCanvas(plots []Plot, pets map[string]int) (canvas *image.NRGBA, cached func() ([]byte, error), err error) {
	m, err := getManifest()
	if err != nil {
		return nil, nil, err
	}
	if len(plots) > m.Field.Slots {
		plots = plots[:m.Field.Slots] // 最多畫 16 格（超過上限的舊農場只畫前 16 格）
	}
	hit, err := presetFor(m, plots)
	if err != nil {
		return nil, nil, err
	}
	if hit != nil && !hit.badges {
		// 預先產生的圖：沒有寵物直接用快取；有寵物時依「有哪幾隻」快取（圖上不畫等級）
		if len(pets) > 0 {
			codes := slices.Sorted(maps.Keys(pets))
			return nil, func() ([]byte, error) { return presetWithPetsPNG(hit.rel, codes) }, nil
		}
		return nil, func() ([]byte, error) { return FilePNG(hit.rel) }, nil
	}
	if hit != nil { // 預先產生的圖 + 補標記
		base, err := img(hit.rel)
		if err != nil {
			return nil, nil, err
		}
		canvas = clone(base)
		for k, p := range plots {
			if err := drawAttrBadges(m, canvas, m.Field.Cells[k].Rect, p); err != nil {
				return nil, nil, err
			}
		}
	} else {
		n := min(max(len(plots), m.Field.MinPlots), m.Field.Slots) // 底圖 4～16
		base, err := img(m.Field.Bases[strconv.Itoa(n)])
		if err != nil {
			return nil, nil, err
		}
		canvas = clone(base)
		for k, p := range plots { // 第 k 塊土地放第 k 格（plot_number 可能不連續）
			if err := drawPlot(m, canvas, m.Field.Cells[k].Rect, p); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(pets) > 0 {
		if canvas, err = withPets(m, canvas, pets); err != nil {
			return nil, nil, err
		}
	}
	return canvas, nil, nil
}

// FarmPNG plots 依 PlotNumber 排序（PlotsFromStatus 的結果）；pets：寵物代碼 → 等級（有寵物時畫在上方草地）。
func FarmPNG(plots []Plot, pets map[string]int) ([]byte, error) {
	canvas, cached, err := fieldCanvas(plots, pets)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return cached()
	}
	return encodePaletteDynamic(canvas, PanelColors)
}

// FarmPNGFromStatus 農田圖片。
func FarmPNGFromStatus(st services.FarmStatus) ([]byte, error) {
	return FarmPNG(PlotsFromStatus(st), st.Pets)
}

// presetWithPetsPNG 預先產生的農田圖加上寵物（寵物組合只有幾種，快取後不必每次重新合成）。
func presetWithPetsPNG(rel string, codes []string) ([]byte, error) {
	key := rel + "|" + fmt.Sprint(codes)
	return memo(&petCache, key, func() ([]byte, error) {
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		base, err := img(rel)
		if err != nil {
			return nil, err
		}
		pets := map[string]int{}
		for _, c := range codes {
			pets[c] = 0
		}
		canvas, err := withPets(m, clone(base), pets)
		if err != nil {
			return nil, err
		}
		return encodePalette(canvas, PanelColors)
	})
}

// withPets 在農田圖上方加一條草地，狗在左上、貓在右上（站在欄杆外的草地上，不壓到欄杆與農田）。
func withPets(m *manifest, field *image.NRGBA, pets map[string]int) (*image.NRGBA, error) {
	band := m.Pets.BandHeight
	w, h := field.Rect.Dx(), field.Rect.Dy()
	canvas := newCanvas(w, h+band)
	tile, err := img(m.Pets.BandTile)
	if err != nil {
		return nil, err
	}
	for x := 0; x < w; x += tile.Rect.Dx() {
		for y := 0; y < band; y += tile.Rect.Dy() {
			alphaComposite(canvas, tile, x, y)
		}
	}
	alphaComposite(canvas, field, 0, band)
	for _, s := range m.petSprites {
		if _, ok := pets[s.Code]; !ok {
			continue
		}
		sprite, err := img(s.File)
		if err != nil {
			return nil, err
		}
		x := s.Margin
		if s.Anchor != "left" {
			x = w - sprite.Rect.Dx() - s.Margin
		}
		alphaComposite(canvas, sprite, x, band-sprite.Rect.Dy()+s.Bottom)
	}
	return canvas, nil
}

// ---------- 建築畫面 ----------

// YardKey 建築數量 → 底圖 key，例如 {"barn": 2, "mill": 1, "bakery": 0, "jam_workshop": 1} → "2-101"。
func YardKey(counts map[string]int) (string, error) {
	m, err := getManifest()
	if err != nil {
		return "", err
	}
	barns := min(counts["barn"], len(m.Yard.BarnSlots))
	key := strconv.Itoa(barns) + "-"
	for _, code := range m.Yard.MachineOrder {
		if counts[code] != 0 {
			key += "1"
		} else {
			key += "0"
		}
	}
	return key, nil
}

// drawBubble 32×32 對話泡泡＋中央 20×20 圖示。
func drawBubble(m *manifest, canvas *image.NRGBA, pos [2]int, iconRel string) error {
	b, err := img(m.Badges.Bubble)
	if err != nil {
		return err
	}
	icon, err := img(iconRel)
	if err != nil {
		return err
	}
	bubble := clone(b)
	alphaComposite(bubble, resize(icon, 20, 20, lanczos), 6, 5)
	alphaComposite(canvas, bubble, pos[0], pos[1])
	return nil
}

func lookup(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}

// yardCanvas 拼好、減色前的建築畫面；沒有任何東西要畫時 canvas 為 nil（直接用底圖 base）。
func yardCanvas(ws services.WorkshopStatus, animals []services.AnimalView) (canvas *image.NRGBA, base string, err error) {
	m, err := getManifest()
	if err != nil {
		return nil, "", err
	}
	key, err := YardKey(ws.Counts)
	if err != nil {
		return nil, "", err
	}
	base = m.Yard.Bases[key]
	target := func() (*image.NRGBA, error) {
		if canvas == nil {
			b, err := img(base)
			if err != nil {
				return nil, err
			}
			canvas = clone(b)
		}
		return canvas, nil
	}
	// 加工機器：加工中 → 沙漏；可領取 → 成品圖示
	for _, mc := range m.machines {
		order := ws.Busy[mc.Code]
		if ws.Counts[mc.Code] == 0 || order == nil {
			continue
		}
		icon := ""
		switch order.State {
		case "in_progress":
			icon = m.Badges.Timer
		case "ready":
			icon = lookup(m.Goods, order.OutputCode, m.Badges.Ready)
		default:
			continue
		}
		c, err := target()
		if err != nil {
			return nil, "", err
		}
		if err := drawBubble(m, c, mc.Bubble, icon); err != nil {
			return nil, "", err
		}
	}
	// 動物：依 animal_no 排序，第 i 隻站在第 i / 4 座畜舍旁的第 i % 4 個固定位置，超過容量的不畫；
	// 由後往前畫，泡泡最後畫才不會被其他動物蓋住
	barns := min(ws.Counts["barn"], len(m.Yard.BarnSlots))
	size := m.Yard.AnimalSize
	sorted := slices.Clone(animals)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].AnimalNo < sorted[j].AnimalNo })
	type spot struct {
		cx, by int
		flip   bool
		a      services.AnimalView
	}
	var placed []spot
	for i, a := range sorted {
		if i >= barns*4 {
			break
		}
		s := m.Yard.AnimalSpots[i/4][i%4]
		placed = append(placed, spot{s[0], s[1], s[2] != 0, a})
	}
	sort.SliceStable(placed, func(i, j int) bool { return placed[i].by < placed[j].by })
	for _, p := range placed {
		src, err := img(lookup(m.Animals, p.a.Code, m.Animals["_generic"]))
		if err != nil {
			return nil, "", err
		}
		sprite := resize(src, size, size, lanczos)
		if p.flip {
			sprite = flipH(sprite)
		}
		c, err := target()
		if err != nil {
			return nil, "", err
		}
		alphaComposite(c, sprite, p.cx-floorDiv(size, 2), p.by-size)
	}
	for _, p := range placed {
		bubble := [2]int{p.cx + floorDiv(size, 2) - 20, p.by - size - 12} // 頭部右上方，盡量不蓋到其他動物
		icon := ""
		switch p.a.State {
		case "hungry":
			icon = m.Badges.Hungry
		case "ready":
			icon = lookup(m.Products, p.a.ProductCode, m.Badges.Ready)
		default:
			continue
		}
		c, err := target()
		if err != nil {
			return nil, "", err
		}
		if err := drawBubble(m, c, bubble, icon); err != nil {
			return nil, "", err
		}
	}
	return canvas, base, nil
}

// YardPNG 建築畫面（ws = WorkshopStatus、animals = ListAnimals）。
func YardPNG(ws services.WorkshopStatus, animals []services.AnimalView) ([]byte, error) {
	canvas, base, err := yardCanvas(ws, animals)
	if err != nil {
		return nil, err
	}
	if canvas == nil {
		return FilePNG(base) // 沒有動物、機器都閒置：直接用底圖
	}
	return encodePaletteDynamic(canvas, PanelColors)
}

// ---------- 固定場景 ----------

// ScenePNG 固定場景圖（manifest 的 scenes）：第一次轉成 256 色並壓縮後快取；轉換後沒有比較小時沿用原檔。
func ScenePNG(name string) ([]byte, error) {
	return memo(&sceneMemo, name, func() ([]byte, error) {
		m, err := getManifest()
		if err != nil {
			return nil, err
		}
		rel, ok := m.Scenes[name]
		if !ok {
			return nil, fmt.Errorf("沒有場景圖 %q", name)
		}
		raw, err := fs.ReadFile(Assets, rel)
		if err != nil {
			return nil, err
		}
		src, err := img(rel)
		if err != nil {
			return nil, err
		}
		out, err := encodePalette(src, 256)
		if err != nil {
			return nil, err
		}
		if len(out) < len(raw) {
			return out, nil
		}
		return raw, nil
	})
}

func ShopPNG() ([]byte, error)      { return ScenePNG("shop") }
func CasinoPNG() ([]byte, error)    { return ScenePNG("casino") }
func WorldBossPNG() ([]byte, error) { return ScenePNG("world_boss") }

// ExplorePNG 山洞冒險區域圖；沒有對應圖片的區域回傳 nil（面板只顯示文字）。
func ExplorePNG(area string) ([]byte, error) {
	m, err := getManifest()
	if err != nil {
		return nil, err
	}
	if _, ok := m.Scenes["explore_"+area]; !ok {
		return nil, nil
	}
	return ScenePNG("explore_" + area)
}
