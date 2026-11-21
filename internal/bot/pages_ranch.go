package bot

// 面板頁面：動物、寵物、加工、建造設施、防守、賭場（對應 Python panels.py 的同名區段：_build_animals、_build_pets、
// _build_craft、_build_build、_build_defense、_report、_build_casino）。

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"farmgm/internal/bot/farmimage"
	"farmgm/internal/bot/ui"
	"farmgm/internal/game/balance"
	"farmgm/internal/game/gameerr"
	"farmgm/internal/game/pets"
	"farmgm/internal/game/upgrades"
	"farmgm/internal/repositories/steals"
	"farmgm/internal/services"
)

// casinoBet 賭場：已選的作物與下注數量（Python self.bet；換頁時保留）。
type casinoBet struct {
	key string
	qty int
}

// petLine 寵物一行說明（動物頁與寵物頁共用）。
func petLine(x services.PetView) string {
	if !x.Owned {
		return fmt.Sprintf("%s **%s**　🔒 還沒有領養", x.Emoji, x.Name)
	}
	return fmt.Sprintf("%s **%s**　Lv **%d/%d**　✨ %s", x.Emoji, x.Name, *x.Level, x.MaxLevel, x.Effect)
}

// percent Python f"{x:.0%}"。
func percent(x float64) string { return fmt.Sprintf("%.0f%%", x*100) }

func animalEmoji(code string) string { return lookupOr(AnimalEmoji, code, "🐾") }

// ======================================================================
// 動物
// ======================================================================

type animalsData struct {
	animals  []services.AnimalView
	status   services.AnimalStatus
	types    []services.AnimalTypeView
	workshop services.WorkshopStatus
	pets     []services.PetView
}

func (p *Panel) loadAnimals(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	var d animalsData
	var err error
	if d.animals, err = svc.ListAnimals(ctx, pid); err != nil {
		return nil, err
	}
	if d.status, err = svc.AnimalStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.types, err = svc.AnimalTypes(ctx, true); err != nil {
		return nil, err
	}
	if d.workshop, err = svc.WorkshopStatus(ctx, pid); err != nil {
		return nil, err
	}
	if d.pets, err = svc.PetStatus(ctx, pid); err != nil {
		return nil, err
	}
	return d, nil
}

// collectAll 收集所有可收集的動物產品；遊戲規則錯誤列在「未完成」，其他錯誤直接回傳。
func (p *Panel) collectAll(ctx context.Context) (string, error) {
	list, err := Call(ctx, p.cmds.Runner, "list_animals", func(ctx context.Context) ([]services.AnimalView, error) {
		return p.svc().ListAnimals(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	var ready []int
	for _, a := range list {
		if a.State == "ready" {
			ready = append(ready, a.AnimalNo)
		}
	}
	if len(ready) == 0 {
		return "", gameerr.Validationf("目前沒有可以收集的動物產品。")
	}
	ok := 0
	var failed []string
	for _, n := range ready {
		_, err := Call(ctx, p.cmds.Runner, "collect_animal", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().CollectAnimal(ctx, p.playerID, n)
		})
		if err != nil {
			if ge, isGame := gameerr.As(err); isGame {
				failed = append(failed, p.f("#%d：%s", n, ge.Error()))
				continue
			}
			return "", err
		}
		ok++
	}
	text := p.f("收集 %d 隻動物的產品。", ok)
	if len(failed) > 0 {
		text += p.s(" 未完成：") + strings.Join(failed[:min(3, len(failed))], p.s("；"))
	}
	return text, nil
}

func (p *Panel) buildAnimals(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(animalsData)
	st := d.status
	var lines []string
	for _, a := range d.animals {
		var tail string
		switch a.State {
		case "producing":
			tail = p.f("⏳ 生產中，%s完成", ts(a.ReadyAt))
		case "ready":
			tail = p.f("✅ 可收集 ×%d %s", a.ProductQuantity, p.s(a.ProductName))
		default:
			tail = p.f("🍽️ 待餵食（%s 金）", money(a.FeedCost))
		}
		lines = append(lines, p.f("%s `#%2d` **%s**　%s", animalEmoji(a.Code), a.AnimalNo, p.s(a.Name), tail))
	}
	barns := d.workshop.Counts["barn"]
	barn := balance.BuildingOf("barn")
	var shop []string
	for _, t := range d.types {
		shop = append(shop, p.f("%s **%s**　💰 **%s** 金\n　🍽️ 餵食 %s 金　⏱️ %s　🥚 產 ×%d %s（%s 金／個）",
			animalEmoji(t.Code), p.s(t.Name), money(t.Price), money(t.FeedCost), p.dur(true, t.CycleSeconds), t.ProductQuantity,
			p.s(t.ProductName), money(t.ProductPrice)))
	}
	var mine any = p.s("還沒有動物，從下方選單購買。")
	if barns == 0 {
		mine = p.s("還沒有動物。先建造畜舍，再購買動物。")
	}
	if len(lines) > 0 {
		mine = Section{Title: p.s("我的動物"), Lines: lines}
	}
	petLines := make([]string, len(d.pets))
	for i, x := range d.pets {
		petLines[i] = petLine(x)
	}
	e := &ui.Embed{Title: p.f("🐔 ✦ 動物（%d/%d）✦", st.Used, st.Capacity), Color: Green, Description: sections(
		p.f("🏚️ %s **%d/%d** 座（每座 %d 個欄位，%s 金）", p.s(barn.Name), barns, barn.MaxPerFarm, balance.BarnSlots, money(barn.Price)),
		mine,
		Section{Title: p.s("🐾 寵物（顯示在農場上，不佔畜舍）"), Lines: petLines},
		Section{Title: p.s("🛒 動物商店"), Lines: shop, Sep: "\n\n"},
	)}
	if p.picker != nil {
		e.AddField(p.s("🛒 已選擇"), p.f("**%s**\n👇 按下方的數量按鈕購買", p.picker.Label), false)
	}
	c := st.Counts

	p.button(p.f("全部餵食（%s 金）", money(st.FeedCostAll)), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("feed_all", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().FeedAll(ctx, p.playerID)
		}), actOpts{})
		return nil
	}, btn{emoji: "🍽️", row: 0, disabled: c["hungry"] == 0, style: ui.Primary})
	p.button(p.s("收集全部"), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.collectAll, actOpts{})
		return nil
	}, btn{emoji: "🧺", row: 0, disabled: c["ready"] == 0, style: ui.Success})
	p.button(p.f("建造%s（%s 金）", p.s(barn.Name), money(barn.Price)), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("build", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().Build(ctx, p.playerID, "barn")
		}), actOpts{})
		return nil
	}, btn{emoji: "🏚️", row: 0, disabled: barns >= barn.MaxPerFarm})
	p.nav(p.s("寵物"), "pets", btn{emoji: "🐾", row: 0, style: ui.Primary})

	pick := func(ctx context.Context, it ui.Interaction, code string) error {
		i := slices.IndexFunc(d.types, func(t services.AnimalTypeView) bool { return t.Code == code })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個商品。")
		}
		t := d.types[i]
		free := max(1, st.Capacity-st.Used)
		// 沒有空欄位時不顯示「買 1」（按了才看到錯誤），直接說明要先做什麼
		short := ""
		switch {
		case barns == 0:
			short = p.s("還沒有畜舍，先按「建造") + p.s(barn.Name) + p.s("」")
		case st.Capacity-st.Used <= 0:
			short = p.s("欄位已滿，先建造") + p.s(barn.Name) + p.s("或放生動物")
		}
		p.picker = &Picker{Key: code, Label: p.f("%s %s（每隻 %s 金，剩餘欄位 %d）", animalEmoji(code), p.s(t.Name), money(t.Price), st.Capacity-st.Used),
			Verb: p.s("買"), Max: min(balance.AnimalMaxBuy, free), Presets: []int{1, 2, 4}, Short: short,
			Run: p.callQty("buy_animals", func(ctx context.Context, q int) (services.ActionResult, error) {
				return p.svc().BuyAnimals(ctx, p.playerID, code, q)
			})}
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}
	options := make([]option, len(d.types))
	for i, t := range d.types {
		options[i] = option{p.s(t.Name), t.Code, p.f("💰 %s 金｜產%s", money(t.Price), p.s(t.ProductName)), animalEmoji(t.Code)}
	}
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	p.selectMenu(p.s("🛒 購買動物…"), options, pick, 1, selected)
	p.quantityButtons(2)

	// 放生：先選動物，再按一次「確認放生」（不退款，不能復原）
	releasing, isReleasing := strings.CutPrefix(p.confirm, "release:")
	pickRelease := func(ctx context.Context, it ui.Interaction, value string) error {
		i := slices.IndexFunc(d.animals, func(a services.AnimalView) bool { return fmt.Sprint(a.AnimalNo) == value })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個項目。")
		}
		a := d.animals[i]
		p.picker, p.confirm = nil, "release:"+value
		warn := ""
		if a.State == "producing" {
			warn = p.s("，這次的餵食不會退還")
		}
		p.refresh(ctx, it, p.f("⚠️ 確定要放生 %s#%s 嗎？放生後不會退款%s。再按一次「確認放生」。", p.s(a.Name), value, warn), nil, p.s("重繪"), time.Time{})
		return nil
	}
	stateText := map[string]string{"producing": p.s("生產中"), "ready": p.s("產品未收集，要先收集"), "hungry": p.s("待餵食")}
	var release []option
	for _, a := range d.animals[:min(len(d.animals), ui.MaxOptions)] {
		release = append(release, option{fmt.Sprintf("#%d %s", a.AnimalNo, p.s(a.Name)), fmt.Sprint(a.AnimalNo),
			p.f("🕊️ 放生（%s）", stateText[a.State]), animalEmoji(a.Code)})
	}
	p.selectMenu(p.s("🕊️ 放生動物（空出欄位買其他動物）…"), release, pickRelease, 3, releasing)
	if isReleasing {
		no, err := parseAnimalNo(releasing)
		if err != nil {
			return nil, err
		}
		p.button(p.s("確認放生 #")+releasing, func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("release_animal", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().ReleaseAnimal(ctx, p.playerID, no)
			}), actOpts{})
			return nil
		}, btn{emoji: "🕊️", row: 4, style: ui.Danger})
	}
	p.navBack(4)
	p.attachImage(e, p.yardImage(d.workshop, d.animals)) // 圖片在交易外產生
	return e, nil
}

func parseAnimalNo(s string) (int, error) {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		return 0, gameerr.Validationf("動物編號不正確。")
	}
	return n, nil
}

// ======================================================================
// 寵物（貓、狗、兔子、小鳥、刺蝟）：動物頁的子頁，購買與升級都要再按一次確認
// ======================================================================

type petsData struct {
	pets   []services.PetView
	gold   int
	status services.FarmStatus // 圖片在組畫面時（交易外）產生
	treat  services.BoostView  // 寵物零食（2026-11-18）
}

func (p *Panel) loadPets(ctx context.Context) (any, error) {
	status, err := p.svc().GetFarmStatus(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	list, err := p.svc().PetStatus(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	boosts, err := p.svc().Boosts(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	d := petsData{pets: list, gold: status.Gold, status: status}
	for _, b := range boosts {
		if b.Code == balance.ItemPetTreat {
			d.treat = b
		}
	}
	return d, nil
}

func (p *Panel) buildPets(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(petsData)
	var blocks []string
	for _, x := range d.pets {
		var step string
		switch {
		case x.NextPrice == nil:
			step = p.s("✅ 已達最高等級")
		case !x.Owned:
			step = p.f("🛒 領養 **%s** 金（Lv0 就有 %d%%）", money(*x.NextPrice), x.NextPct)
		default:
			step = p.f("⬆️ 升到 Lv%d：**%s** 金 → %d%%", *x.Level+1, money(*x.NextPrice), x.NextPct)
		}
		block := p.f("%s\n　📝 %s\n　%s", petLine(x), p.s(x.Role), step)
		if o := x.Odds; o != nil {
			when := p.f("領養後（+%d%%）", o.Pct)
			if o.Current {
				when = p.f("目前 +%d%%", o.Pct)
			}
			// 2026-11-16 加成明細只列前 3 種作物（原本每種作物一行，字太多）
			shown := o.Crops[:min(3, len(o.Crops))]
			crops := make([]string, len(shown))
			for i, c := range shown {
				crops[i] = p.f("　　%s %s（每格 %d 個）：%s", cropEmoji(c.Code), p.s(c.Name), c.Quantity, c.Text)
			}
			more := ""
			if len(o.Crops) > len(shown) {
				more = p.f("\n　　…另外 %d 種作物", len(o.Crops)-len(shown))
			}
			block += p.f("\n　🎲 **每格實際多收**（%s）：\n", when) + strings.Join(crops, "\n") + more
		}
		blocks = append(blocks, block)
	}
	// 寵物零食（2026-11-18）：在烘焙坊製作，餵了 6 小時內所有寵物效果 ×1.5
	treat := p.f("🦴 寵物零食：持有 **%d** 個（%s；%s）", d.treat.Have, p.s(d.treat.Desc), d.treat.Recipe)
	if d.treat.ActiveUntil != nil {
		treat += p.f("\n　✨ 零食效果中，%s結束", ts(d.treat.ActiveUntil))
	}
	owned := slices.ContainsFunc(d.pets, func(x services.PetView) bool { return x.Owned })
	// 價格階梯與加成規則已寫在每隻寵物與說明頁，不再整段重複；金幣放最後，貼近領養／升級按鈕
	e := &ui.Embed{Title: p.s("🐾 ✦ 寵物 ✦"), Color: Green, Description: sections(
		Section{Lines: blocks, Sep: "\n\n"},
		p.s("寵物會出現在農場圖上方的草地，不佔畜舍欄位。"),
		treat,
		p.f("💰 你的金幣：**%s**", money(d.gold)),
	)}
	pending, hasPending := strings.CutPrefix(p.confirm, "pet:")
	for _, x := range d.pets {
		if x.NextPrice == nil {
			continue
		}
		verb := p.s("領養")
		if x.Owned {
			verb = p.f("升級 Lv%d", *x.Level+1)
		}
		price := money(*x.NextPrice)
		if hasPending && pending == x.Code {
			p.button(p.f("確認%s%s（%s 金）", verb, p.s(x.Name), price), func(ctx context.Context, it ui.Interaction) error {
				pid, code := p.playerID, x.Code
				if x.Owned {
					p.act(ctx, it, p.call("upgrade_pet", func(ctx context.Context) (services.ActionResult, error) {
						return p.svc().UpgradePet(ctx, pid, code)
					}), actOpts{})
				} else {
					p.act(ctx, it, p.call("buy_pet", func(ctx context.Context) (services.ActionResult, error) {
						return p.svc().BuyPet(ctx, pid, code)
					}), actOpts{})
				}
				return nil
			}, btn{emoji: "✅", row: 0, style: ui.Success})
		} else {
			p.button(p.f("%s%s（%s 金）", verb, p.s(x.Name), price), func(ctx context.Context, it ui.Interaction) error {
				p.confirm = "pet:" + x.Code
				p.refresh(ctx, it, p.f("%s 確定要花 %s 金幣%s%s嗎？再按一次「確認」。", x.Emoji, price, verb, p.s(x.Name)), nil, p.s("重繪"), time.Time{})
				return nil
			}, btn{emoji: x.Emoji, row: 0, disabled: d.gold < *x.NextPrice})
		}
	}
	p.button(p.f("餵寵物零食（持有 %d）", d.treat.Have), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.call("use_boost", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().UseBoost(ctx, p.playerID, balance.ItemPetTreat)
		}), actOpts{})
		return nil
	}, btn{emoji: "🦴", row: 1, style: ui.Primary, disabled: d.treat.Have == 0 || !owned})
	p.nav(p.s("回動物"), "animals", btn{emoji: "🐔", row: 1})
	p.navBack(1)
	p.attachImage(e, p.farmImage(d.status)) // 圖片在交易外產生
	return e, nil
}

// ======================================================================
// 加工（開始加工）與建造設施（分開兩頁）
// ======================================================================

type craftData struct {
	workshop services.WorkshopStatus
	recipes  []services.RecipeView
	orders   []services.OrderView
	animals  []services.AnimalView // 建築畫面在組畫面時（交易外）產生
}

// loadWorkshop 加工與建造頁共用：設施、配方與建築畫面（orders 只有加工頁需要）。
func (p *Panel) loadWorkshop(ctx context.Context, orders bool) (craftData, error) {
	svc, pid := p.svc(), p.playerID
	var d craftData
	var err error
	if d.workshop, err = svc.WorkshopStatus(ctx, pid); err != nil {
		return d, err
	}
	if d.recipes, err = svc.Recipes(ctx, &pid); err != nil {
		return d, err
	}
	if orders {
		if d.orders, err = svc.CraftOrders(ctx, pid, false, 20); err != nil {
			return d, err
		}
	}
	if d.animals, err = svc.ListAnimals(ctx, pid); err != nil {
		return d, err
	}
	return d, nil
}

func (p *Panel) loadCraft(ctx context.Context) (any, error) { return p.loadWorkshop(ctx, true) }
func (p *Panel) loadBuild(ctx context.Context) (any, error) { return p.loadWorkshop(ctx, false) }

// collectCrafts 領取所有已完成的加工訂單。
func (p *Panel) collectCrafts(ctx context.Context) (string, error) {
	orders, err := Call(ctx, p.cmds.Runner, "craft_orders", func(ctx context.Context) ([]services.OrderView, error) {
		return p.svc().CraftOrders(ctx, p.playerID, false, 20)
	})
	if err != nil {
		return "", err
	}
	var msgs []string
	for _, o := range orders {
		if o.State != "ready" {
			continue
		}
		r, err := Call(ctx, p.cmds.Runner, "collect_craft", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().CollectCraft(ctx, p.playerID, o.OrderID)
		})
		if err != nil {
			return "", err
		}
		msgs = append(msgs, r.Message)
	}
	if len(msgs) == 0 {
		return "", gameerr.Validationf("沒有已完成的加工訂單。")
	}
	return strings.Join(msgs, " "), nil
}

// craftProfit 每份加工的損益（2026-11-18）：原料直接賣掉的價值 vs 成品售價；原料或成品沒有售價時不顯示。
func craftProfit(r services.RecipeView) string {
	if r.SellPrice == nil || *r.SellPrice == 0 || r.InputValue == 0 {
		return ""
	}
	out := *r.SellPrice * r.OutputQuantity
	diff := out - r.InputValue
	mark := "📈"
	if diff < 0 {
		mark = "📉"
	}
	return fmt.Sprintf("%s 每份：原料直接賣約 %s 金 → 成品 %s 金（%s 金，%+d%%）", mark, money(r.InputValue), money(out),
		signedMoney(diff), diff*100/r.InputValue)
}

// signedMoney 帶正負號的金額，例如 +1,250、-30。
func signedMoney(n int) string {
	if n < 0 {
		return "-" + money(-n)
	}
	return "+" + money(n)
}

func canMake(r services.RecipeView) int {
	if r.CanMake == nil {
		return 0
	}
	return *r.CanMake
}

func (p *Panel) buildCraft(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(craftData)
	ws := d.workshop
	var facilities []string
	for _, code := range balance.BuildingOrder {
		if code == "barn" {
			continue
		}
		b := balance.BuildingOf(code)
		busy := ws.Busy[code]
		var state string
		switch {
		case ws.Counts[code] == 0:
			state = p.f("🔒 未建造（%s 金）", money(b.Price))
		case busy != nil && busy.State == "in_progress":
			ready := busy.ReadyAt
			state = p.f("⚙️ 加工中：%s ×%d，%s完成", p.s(busy.RecipeName), busy.OutputQuantity, ts(&ready))
			if q := ws.Queued[code]; q != 0 {
				state += p.f("\n　⏳ 排隊 %d 張（最多 %d 張）", q, balance.CraftQueueMax)
			}
		case busy != nil:
			state = p.f("✅ %s 完成，可以領取", p.s(busy.RecipeName))
		default:
			state = p.s("💤 閒置中")
		}
		facilities = append(facilities, p.f("🏭 **%s**%s　%s", p.s(b.Name), levelTag(ws, code), state))
	}
	// 只列出「設施已建造、現有原料至少做得出 1 份」的配方（配方很多，全部列出太長；2026-11-13 使用者要求）
	var recipes []string
	var built, makeable []services.RecipeView
	lacking, unbuilt := 0, 0
	for _, r := range d.recipes {
		if !r.Built {
			unbuilt++
			continue
		}
		built = append(built, r)
		if canMake(r) == 0 {
			lacking++
			continue
		}
		makeable = append(makeable, r)
		inputs := make([]string, len(r.InputOrder))
		for i, name := range r.InputOrder {
			inputs[i] = fmt.Sprintf("×%d %s", r.Inputs[name], name)
		}
		line := p.f("✅ **%s**（%s）\n　📥 %s　➡️ 📤 ×%d %s\n　⏱️ %s／份", p.s(r.Name), p.s(r.BuildingName),
			strings.Join(inputs, p.s("、")), r.OutputQuantity, p.s(r.OutputName), p.dur(true, r.Seconds))
		if r.SellPrice != nil && *r.SellPrice != 0 {
			line += p.f("　🏷️ 售價 %d", *r.SellPrice)
		}
		if t := craftProfit(r); t != "" {
			line += p.s("\n　") + t
		}
		line += p.f("\n　🧮 現有原料可做 **%d** 份", canMake(r))
		recipes = append(recipes, line)
	}
	var others []string
	if lacking > 0 {
		others = append(others, p.f("%d 個原料不足", lacking))
	}
	if unbuilt > 0 {
		others = append(others, p.f("%d 個需要先建造設施", unbuilt))
	}
	othersNote := ""
	if len(others) > 0 {
		othersNote = p.s("📋 另外還有 ") + strings.Join(others, p.s("、")) + p.s("的配方沒有列出（各設施能做的配方在「🏗️ 建造設施」頁）。巨型、快速生長、水晶作物也能當原料（只收普通品質）。")
	}
	if len(recipes) == 0 && len(built) > 0 {
		recipes = []string{p.s("目前的原料一份都做不了，先去收成或購買原料吧。")}
	}
	if p.picker != nil { // 每次重繪依最新的原料重算份數按鈕
		most := 0
		if i := slices.IndexFunc(built, func(r services.RecipeView) bool { return r.Code == p.picker.Key }); i >= 0 {
			most = canMake(built[i])
		}
		p.picker.Max, p.picker.Most = max(1, most), most
		p.picker.Presets = nil
		for _, q := range []int{1, 5, most} {
			if 1 <= q && q <= most && !slices.Contains(p.picker.Presets, q) {
				p.picker.Presets = append(p.picker.Presets, q)
			}
		}
		slices.Sort(p.picker.Presets)
	}
	hint := ""
	if len(built) == 0 {
		hint = p.s("💡 還沒有任何加工設施，請先按「🏗️ 建造設施」。")
	}
	e := &ui.Embed{Title: p.s("🏭 ✦ 加工 ✦"), Color: Blue, Description: sections(
		Section{Title: p.s("設施狀態"), Lines: facilities},
		Section{Title: p.s("📖 現在可以加工的配方"), Lines: recipes, Sep: "\n\n"},
		othersNote,
		hint,
	)}
	if p.picker != nil {
		most := p.picker.Most
		h := p.s("⚠️ 原料不足，一份都做不了")
		if most != 0 {
			h = p.f("現有原料最多可做 **%d** 份（單張上限 %d 份），👇 按下方的數量按鈕開始加工", most, balance.CraftMaxQuantity)
		}
		if p.picker.PriceText != "" {
			h = p.picker.PriceText + "\n" + h
		}
		e.AddField(p.s("🛒 已選擇"), p.f("**%s**\n%s\n設施忙碌時新訂單會排隊，完成後自動接著加工（每種設施最多 %d 張）",
			p.picker.Label, h, balance.CraftQueueMax), false)
	}
	ready := 0
	for _, o := range d.orders {
		if o.State == "ready" {
			ready++
		}
	}

	pick := func(ctx context.Context, it ui.Interaction, code string) error {
		i := slices.IndexFunc(d.recipes, func(r services.RecipeView) bool { return r.Code == code })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個項目。")
		}
		r := d.recipes[i]
		// 份數按鈕（Max／Most／Presets）在 buildCraft 依最新原料計算，只顯示做得出來的份數
		p.picker = &Picker{Key: code, Label: p.f("🏭 %s（%s）", p.s(r.Name), p.s(r.BuildingName)), Verb: p.s("做"), Max: 1, PriceText: craftProfit(r),
			Run: p.callQty("start_craft", func(ctx context.Context, q int) (services.ActionResult, error) {
				return p.svc().StartCraft(ctx, p.playerID, code, q)
			})}
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}
	options := make([]option, len(makeable))
	for i, r := range makeable {
		options[i] = option{p.s(r.Name), r.Code, p.f("%s｜%s／份", p.s(r.BuildingName), p.dur(true, r.Seconds)), "🏭"}
	}
	selected := ""
	if p.picker != nil {
		selected = p.picker.Key
	}
	p.selectMenu(p.s("🏭 開始加工（只列出現在做得出來的配方）…"), options, pick, 0, selected)
	p.quantityButtons(1)
	p.nav(p.s("建造設施"), "build", btn{emoji: "🏗️", row: 2, style: ui.Primary})
	p.button(p.f("領取完成品（%d）", ready), func(ctx context.Context, it ui.Interaction) error {
		p.act(ctx, it, p.collectCrafts, actOpts{})
		return nil
	}, btn{emoji: "📥", row: 2, disabled: ready == 0, style: ui.Success})
	p.nav(p.s("售價表"), "craft_prices", btn{emoji: "📋", row: 2}) // 2026-11-19：所有加工品的售價與損益
	p.nav(p.s("機器"), "machines", btn{emoji: "🔧", row: 2})
	p.cancelCraftControls(d.orders)
	p.navBack(4)
	p.attachImage(e, p.yardImage(d.workshop, d.animals)) // 圖片在交易外產生
	return e, nil
}

// levelTag 加工設施的等級標示（已建造且升級過才顯示），例如「 Lv.2（加工時間 -40%）」。
func levelTag(ws services.WorkshopStatus, code string) string {
	lv := ws.Levels[code]
	if ws.Counts[code] == 0 || lv == 0 {
		return ""
	}
	return fmt.Sprintf(" Lv.%d（加工時間 -%d%%）", lv, upgrades.BuildingCutPct(lv))
}

func (p *Panel) buildBuild(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(craftData)
	ws := d.workshop
	var lines []string
	var options, ups []option
	for _, code := range balance.BuildingOrder {
		b := balance.BuildingOf(code)
		var use string
		if code == "barn" {
			use = p.f("安置動物（每座 %d 隻）", balance.BarnSlots)
		} else {
			var names []string
			for _, r := range d.recipes {
				if r.BuildingCode == code {
					names = append(names, p.s(r.Name))
				}
			}
			use = p.s("可加工：") + strings.Join(names, p.s("、"))
		}
		owned := ws.Counts[code]
		state := p.f("🏗️ 可建造（%d/%d）", owned, b.MaxPerFarm)
		if owned >= b.MaxPerFarm {
			state = p.s("✅ 已達上限")
		} else {
			options = append(options, option{p.f("%s（%s 金）", p.s(b.Name), money(b.Price)), code, p.f("已有 %d/%d", owned, b.MaxPerFarm), "🏗️"})
		}
		if balance.Upgradable(code) && owned > 0 {
			lv := ws.Levels[code]
			if lv >= upgrades.BuildingMaxLevel() {
				use += p.f("\n　⬆️ Lv.%d 已滿級：加工時間 -%d%%", lv, upgrades.BuildingCutPct(lv))
			} else {
				use += p.f("\n　⬆️ Lv.%d → Lv.%d：加工時間 -%d%% → -%d%%（%s 金）", lv, lv+1,
					upgrades.BuildingCutPct(lv), upgrades.BuildingCutPct(lv+1), money(upgrades.BuildingUpgradePrice(lv)))
				ups = append(ups, option{p.f("升級%s到 Lv.%d（%s 金）", p.s(b.Name), lv+1, money(upgrades.BuildingUpgradePrice(lv))),
					code, p.f("加工時間 -%d%% → -%d%%", upgrades.BuildingCutPct(lv), upgrades.BuildingCutPct(lv+1)), "⬆️"})
			}
		}
		lines = append(lines, p.f("🏭 **%s**%s　💰 **%s** 金　%s\n　📝 %s", p.s(b.Name), levelTag(ws, code), money(b.Price), state, use))
	}
	e := &ui.Embed{Title: p.s("🏗️ ✦ 建造設施 ✦"), Color: Blue, Description: sections(
		p.f("💰 你的金幣：**%s**", money(ws.Gold)),
		Section{Title: p.s("設施"), Lines: lines, Sep: "\n\n"},
	)}
	p.selectMenu(p.s("🏗️ 選擇要建造的設施…"), options, func(ctx context.Context, it ui.Interaction, code string) error {
		p.act(ctx, it, p.call("build", func(ctx context.Context) (services.ActionResult, error) {
			return p.svc().Build(ctx, p.playerID, code)
		}), actOpts{})
		return nil
	}, 0, "")
	// 加工設施升級（2026-11-17）：選了先顯示確認按鈕，再按一次才扣錢
	upgrading := strings.TrimPrefix(p.confirm, "upgrade:")
	if !strings.HasPrefix(p.confirm, "upgrade:") || !slices.ContainsFunc(ups, func(o option) bool { return o.value == upgrading }) {
		upgrading = ""
	}
	if len(ups) > 0 {
		p.selectMenu(p.s("⬆️ 升級加工設施（縮短加工時間）…"), ups, func(ctx context.Context, it ui.Interaction, code string) error {
			p.confirm = "upgrade:" + code
			lv := ws.Levels[code]
			p.refresh(ctx, it, p.f("⚠️ %s升到 Lv.%d 需要 %s 金幣，加工時間 -%d%%（已排隊的訂單不變）。按「確認升級」完成。",
				p.s(balance.BuildingOf(code).Name), lv+1, money(upgrades.BuildingUpgradePrice(lv)), upgrades.BuildingCutPct(lv+1)), nil, p.s("重繪"), time.Time{})
			return nil
		}, 1, upgrading)
	}
	if upgrading != "" {
		code, lv := upgrading, ws.Levels[upgrading]
		price := upgrades.BuildingUpgradePrice(lv)
		p.button(p.f("確認升級%s（%s 金）", p.s(balance.BuildingOf(code).Name), money(price)), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("upgrade_building", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().UpgradeBuilding(ctx, p.playerID, code)
			}), actOpts{})
			return nil
		}, btn{emoji: "⬆️", row: 2, style: ui.Danger, disabled: ws.Gold < price})
	}
	p.nav(p.s("回加工"), "craft", btn{emoji: "↩️", row: 2})
	p.navBack(2)
	p.attachImage(e, p.yardImage(d.workshop, d.animals)) // 圖片在交易外產生
	return e, nil
}

// ======================================================================
// 防守
// ======================================================================

type defenseData struct {
	status  services.DefenseStatus
	tickets int
	dog     services.PetView
}

func (p *Panel) loadDefense(ctx context.Context) (any, error) {
	svc, pid := p.svc(), p.playerID
	bp, err := svc.GetBackpack(ctx, pid)
	if err != nil {
		return nil, err
	}
	list, err := svc.PetStatus(ctx, pid)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(list, func(x services.PetView) bool { return x.Code == pets.Dog })
	if i < 0 {
		return nil, fmt.Errorf("寵物狀態缺少狗狗")
	}
	st, err := svc.DefenseStatus(ctx, pid)
	if err != nil {
		return nil, err
	}
	return defenseData{st, lineQty(bp.Consumables, balance.ItemPatrolTicket), list[i]}, nil
}

func (p *Panel) buildDefense(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(defenseData)
	st := d.status
	var devices []string
	for _, x := range st.Devices {
		where := p.s("全部農田")
		if x.EndPlot != nil {
			where = p.f("第 %d–%d 格", *x.StartPlot, *x.EndPlot)
		}
		devices = append(devices, p.f("🛡️ **%s #%d**　守護%s", p.s(x.Name), x.Slot, where))
	}
	chances := make([]string, len(st.Plots))
	for i, pl := range st.Plots {
		chances[i] = fmt.Sprintf("`#%d` %s", pl.PlotNumber, percent(pl.Chance))
	}
	var shop []string
	for _, code := range balance.DefenseOrder {
		x := balance.DefenseOf(code)
		where := p.s("守護全部農田")
		if x.Range != nil {
			where = p.f("守護連續 %d 格（可調整）", *x.Range)
		}
		shop = append(shop, p.f("🛡️ **%s**　💰 **%s** 金　上限 %d\n　🚫 阻止機率 %s　📍 %s", p.s(x.Name), money(x.Price), x.MaxPerFarm, percent(x.BlockChance), where))
	}
	var installed any = p.s("還沒有防守設施。")
	if len(devices) > 0 {
		installed = Section{Title: p.s("已設置的設施"), Lines: devices}
	}
	dog := p.s("還沒有狗狗。到「🐔 動物 → 🐾 寵物」領養，升級後能在設施沒擋住時再擋一次。")
	if d.dog.Owned {
		dog = p.f("Lv%d：設施沒擋住時，再有 **%d%%** 機率擋下（所有農田，不受設施整體上限限制）", *d.dog.Level, d.dog.Pct)
	}
	e := &ui.Embed{Title: p.s("🛡️ ✦ 防守 ✦"), Color: Blue, Description: sections(
		installed,
		Section{Title: p.s("各農田阻止機率"), Lines: []string{strings.Join(chances, p.s("　"))}},
		Section{Title: p.s("🐶 狗狗"), Lines: []string{dog}},
		Section{Title: p.s("🛒 防守商店"), Lines: shop, Sep: "\n\n"},
		p.f("🔦 巡查券：**%d** 張（可查看近 %d 天誰來偷過菜）", d.tickets, balance.PatrolDays),
	)}

	ticket := balance.ItemOf(balance.ItemPatrolTicket)
	var options []option
	for _, code := range balance.DefenseOrder {
		x := balance.DefenseOf(code)
		options = append(options, option{p.f("%s（%s 金）", p.s(x.Name), money(x.Price)), code, p.f("持有 %d/%d", st.Owned[code], x.MaxPerFarm), "🛡️"})
	}
	options = append(options, option{p.f("%s（%s 金）", p.s(ticket.Name), money(ticket.Price)), balance.ItemPatrolTicket, p.s("買 1 張"), "🔦"})
	p.selectMenu(p.s("🛒 購買防守設施或巡查券…"), options, func(ctx context.Context, it ui.Interaction, code string) error {
		if code == balance.ItemPatrolTicket {
			p.act(ctx, it, p.call("buy_item", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyItem(ctx, p.playerID, code, 1)
			}), actOpts{})
		} else {
			p.act(ctx, it, p.call("buy_defense", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().BuyDefense(ctx, p.playerID, code)
			}), actOpts{})
		}
		return nil
	}, 0, "")

	var placeable []option
	for _, x := range st.Devices {
		if x.EndPlot != nil {
			placeable = append(placeable, option{fmt.Sprintf("%s #%d", p.s(x.Name), x.Slot), fmt.Sprintf("%s|%d", x.Code, x.Slot),
				p.f("目前第 %d–%d 格", *x.StartPlot, *x.EndPlot), "📍"})
		}
	}
	p.selectMenu(p.s("📍 調整稻草人／圍欄的守護範圍…"), placeable, func(ctx context.Context, it ui.Interaction, value string) error {
		code, rawSlot, _ := strings.Cut(value, "|")
		var slot int
		if _, err := fmt.Sscan(rawSlot, &slot); err != nil {
			return gameerr.Validationf("找不到這個項目。")
		}
		return it.SendModal(ctx, quantityModal(p.s("調整守護範圍"), p.s("起始農田編號"), max(1, len(st.Plots)), func(ctx context.Context, mit ui.Interaction, start int) {
			p.act(ctx, mit, p.call("place_defense", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().PlaceDefense(ctx, p.playerID, code, start, slot)
			}), actOpts{})
		}))
	}, 1, "")

	p.button(p.f("巡查（消耗 1 張，持有 %d）", d.tickets), func(ctx context.Context, it ui.Interaction) error {
		p.report(ctx, it, p.s("🔦 巡查報告"), true)
		return nil
	}, btn{emoji: "🔦", row: 2, disabled: d.tickets == 0})
	p.button(p.s("被阻止的偷菜紀錄"), func(ctx context.Context, it ui.Interaction) error {
		p.report(ctx, it, p.s("📋 防守紀錄"), false)
		return nil
	}, btn{emoji: "📋", row: 2})
	p.navBack(4)
	return e, nil
}

// report 巡查報告或防守紀錄，以私密訊息回覆（不改變面板）；巡查消耗巡查券，之後重繪面板（Python _report）。
func (p *Panel) report(ctx context.Context, it ui.Interaction, title string, patrol bool) {
	_ = it.DeferUpdateIfPending(ctx)
	var text string
	err := p.cmds.Runner.Do(ctx, func(ctx context.Context) error {
		var err error
		if patrol {
			text, err = p.patrolText(ctx)
		} else {
			text, err = p.defenseLogText(ctx)
		}
		return err
	})
	msg := ui.Message{Ephemeral: true}
	if err != nil {
		msg.Content = ptr("❌ " + cleanMessage(p.msg(err)))
	} else {
		msg.Embed = &ui.Embed{Title: title, Color: Blue, Description: cut(text, 4096)}
	}
	if _, ferr := it.Followup(ctx, msg); ferr != nil {
		logUnexpected(ferr, "送出報告失敗", "player", p.playerID)
	}
	if patrol {
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
	}
}

func blockerName(code *string) string {
	if code == nil {
		return pets.BlockerName("")
	}
	return pets.BlockerName(*code)
}

// defenseLogText 防守紀錄（Python CMD「defense log」的輸出）。
func (p *Panel) defenseLogText(ctx context.Context) (string, error) {
	rows, err := Call(ctx, p.cmds.Runner, "defense_log", func(ctx context.Context) ([]services.DefenseLogRow, error) {
		return p.svc().DefenseLog(ctx, p.playerID, 20)
	})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return p.s("目前沒有被阻止的偷菜紀錄。"), nil
	}
	lines := []string{p.s("**🛡️ 被阻止的偷菜**（想知道是誰，請使用巡查券）")}
	for _, r := range rows {
		lines = append(lines, p.f("• %s　第 %d 格 %s：有人想偷 %d 個，被**%s**阻止",
			ts(&r.CreatedAt), r.PlotNumber, p.s(r.CropName), r.Quantity, blockerName(r.BlockedBy)))
	}
	return strings.Join(lines, "\n"), nil
}

// patrolText 巡查報告（Python CMD「patrol」的輸出）：消耗 1 張巡查券。
func (p *Panel) patrolText(ctx context.Context) (string, error) {
	rows, err := Call(ctx, p.cmds.Runner, "patrol", func(ctx context.Context) ([]steals.SearchRow, error) {
		return p.svc().Patrol(ctx, p.playerID)
	})
	if err != nil {
		return "", err
	}
	lines := []string{p.f("**近 %d 天的偷菜紀錄**（已消耗 1 張巡查券）", balance.PatrolDays)}
	if len(rows) == 0 {
		lines = append(lines, p.s("近期沒有任何偷菜嘗試。"))
	}
	for _, r := range rows {
		result := p.f("🥷 偷走 %d 個", r.Quantity)
		if r.Outcome != "success" {
			result = p.f("🛡️ 想偷 %d 個，被%s阻止", r.Quantity, blockerName(r.BlockedBy))
		}
		lines = append(lines, p.f("• %s　**%s**（%s）第 %d 格 %s：%s",
			ts(&r.CreatedAt), escapeMD(r.ThiefName), escapeMD(r.GroupName), r.PlotNumber, p.s(r.CropName), result))
	}
	return strings.Join(lines, "\n"), nil
}

// ======================================================================
// 賭場
// ======================================================================

type casinoData struct {
	options []services.GambleOption
	prizes  []services.PrizeView
	image   []byte
}

func (p *Panel) loadCasino(ctx context.Context) (any, error) {
	svc := p.svc()
	options, err := svc.GambleOptions(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	prizes, err := svc.ListPrizes(ctx, p.playerID)
	if err != nil {
		return nil, err
	}
	return casinoData{options, prizes, p.image(p.s("賭場"), farmimage.CasinoPNG)}, nil
}

func gambleKey(o services.GambleOption) string {
	return o.ItemCode + "|" + o.Quality + "|" + o.Mutation
}

func (p *Panel) buildCasino(_ context.Context, data any) (*ui.Embed, error) {
	d := data.(casinoData)
	// Python dict：同一個鍵保留第一次出現的位置、最後一次的內容
	var keys []string
	rows := map[string]services.GambleOption{}
	for _, o := range d.options {
		k := gambleKey(o)
		if _, ok := rows[k]; !ok {
			keys = append(keys, k)
		}
		rows[k] = o
	}
	if p.bet != nil {
		if _, ok := rows[p.bet.key]; !ok {
			p.bet = nil // 已經全部押完或賣掉
		}
	}
	bet := p.bet
	if bet != nil {
		bet.qty = max(1, min(bet.qty, rows[bet.key].Quantity))
	}
	var lines []string
	for _, k := range keys {
		o := rows[k]
		mark := ""
		if bet != nil && bet.key == k {
			mark = "▶ "
		}
		lines = append(lines, p.f("%s%s **%s**　×**%d**\n　🏷️ 價目表 %s 金／個　🎲 比大小贏了 %d　🎯 猜中 %d",
			mark, cropEmoji(o.CropCode), p.s(o.Name), o.Quantity, money(o.UnitPrice), o.UnitPrice*balance.GambleHiloMultiplier,
			o.UnitPrice*balance.GambleExactMultiplier))
	}
	prizes := make([]string, len(d.prizes))
	for i, x := range d.prizes {
		prizes[i] = p.f("🏆 %s %s　×**%d**　（×%d，%s 金／個）", cropEmoji(x.CropCode), p.s(x.Name), x.Quantity, x.Multiplier, money(x.UnitPrice))
	}
	rulesText := p.f("🎲 系統從 **%d–%d** 擲一個數字\n"+
		"⬆️ **比大**（%d–%d）／⬇️ **比小**（%d–%d）：贏了價值 **×%d**（機率 50%%）\n"+
		"🎯 **猜數字**：猜中價值 **×%d**（機率 10%%）\n"+
		"💀 輸了：下注的作物會被沒收\n"+
		"🏆 贏得的作物存進倉庫的「賭場獎品」，可以在「💰 販售」賣出或留著展示\n"+
		"✅ 只能押：稀有、傳說品質，或巨型作物、黃金作物、珍稀變種",
		balance.GambleMin, balance.GambleMax, balance.GambleBigFrom, balance.GambleMax, balance.GambleMin, balance.GambleBigFrom-1,
		balance.GambleHiloMultiplier, balance.GambleExactMultiplier)
	var list any = p.s("背包裡沒有可以下注的作物（需要稀有以上品質或特殊突變）。")
	if len(lines) > 0 {
		list = Section{Title: p.s("可以下注的作物"), Lines: lines, Sep: "\n\n"}
	}
	e := &ui.Embed{Title: p.s("🎰 ✦ 幸運賭場 ✦"), Color: Purple, Description: sections(
		Section{Title: p.s("規則"), Lines: []string{rulesText}},
		list,
		Section{Title: p.s("🏆 我的賭場獎品"), Lines: prizes},
	)}
	selected := ""
	if bet != nil {
		o := rows[bet.key]
		selected = bet.key
		e.AddField(p.s("🎲 這次下注"), p.f("**%s ×%d**（價值 %s 金）\n👇 選擇下注數量，然後按「比大」「比小」或從選單猜一個數字",
			p.s(o.Name), bet.qty, money(o.UnitPrice*bet.qty)), false)
	} else {
		e.Footer = p.s("👇 先從選單選一種作物")
	}

	options := make([]option, len(keys))
	for i, k := range keys {
		o := rows[k]
		options[i] = option{fmt.Sprintf("%s ×%d", p.s(o.Name), o.Quantity), k, p.f("🏷️ %s 金／個", money(o.UnitPrice)), cropEmoji(o.CropCode)}
	}
	p.selectMenu(p.s("🎰 選擇要下注的作物…"), options, func(ctx context.Context, it ui.Interaction, key string) error {
		p.bet = &casinoBet{key: key, qty: 1}
		p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
		return nil
	}, 0, selected)
	if bet != nil {
		have := rows[bet.key].Quantity
		for _, preset := range []struct {
			label string
			qty   int
		}{{"×1", 1}, {p.s("一半"), max(1, have/2)}, {p.s("全部"), have}} {
			style := ui.Secondary
			if preset.qty == bet.qty {
				style = ui.Primary
			}
			p.button(p.f("%s（%d）", preset.label, preset.qty), func(ctx context.Context, it ui.Interaction) error {
				bet.qty = preset.qty
				p.refresh(ctx, it, "", nil, p.s("重繪"), time.Time{})
				return nil
			}, btn{row: 1, style: style})
		}
		p.button(p.s("自訂數量…"), func(ctx context.Context, it ui.Interaction) error {
			return it.SendModal(ctx, quantityModal(p.s("下注數量"), p.s("數量"), have, func(ctx context.Context, mit ui.Interaction, q int) {
				bet.qty = q
				p.refresh(ctx, mit, "", nil, p.s("重繪"), time.Time{})
			}))
		}, btn{emoji: "✏️", row: 1})
		o := rows[bet.key]
		play := func(mode string, guess *int) ui.Handler {
			return func(ctx context.Context, it ui.Interaction) error {
				qty := bet.qty
				p.act(ctx, it, func(ctx context.Context) (string, error) {
					r, err := Call(ctx, p.cmds.Runner, "gamble", func(ctx context.Context) (services.GambleResult, error) {
						return p.svc().Gamble(ctx, p.playerID, o.ItemCode, o.Quality, o.Mutation, qty, mode, guess)
					})
					return r.Message, err
				}, actOpts{plain: true})
				return nil
			}
		}
		p.button(p.f("比大（%d–%d）", balance.GambleBigFrom, balance.GambleMax), play("big", nil),
			btn{emoji: "⬆️", row: 2, style: ui.Success})
		p.button(p.f("比小（%d–%d）", balance.GambleMin, balance.GambleBigFrom-1), play("small", nil),
			btn{emoji: "⬇️", row: 2, style: ui.Danger})
		var guesses []option
		for n := balance.GambleMin; n <= balance.GambleMax; n++ {
			guesses = append(guesses, option{p.f("猜 %d", n), fmt.Sprint(n), "", "🎯"})
		}
		p.selectMenu(p.f("🎯 猜一個數字（中了 ×%d）…", balance.GambleExactMultiplier), guesses,
			func(ctx context.Context, it ui.Interaction, value string) error {
				var n int
				if _, err := fmt.Sscan(value, &n); err != nil {
					return gameerr.Validationf("請選擇 %d–%d 的數字。", balance.GambleMin, balance.GambleMax)
				}
				return play("exact", &n)(ctx, it)
			}, 3, "")
	}
	p.nav(p.s("賣獎品"), "sell", btn{emoji: "🏆", row: 4})
	p.navBack(4)
	p.attachImage(e, d.image)
	return e, nil
}

// cancelCraftControls 取消加工訂單（Go 版新增）：從選單選一張加工中或排隊中的訂單，再按一次「確認取消」才取消，
// 退回原料，後面排隊的訂單往前遞補。
func (p *Panel) cancelCraftControls(orders []services.OrderView) {
	pending, hasPending := strings.CutPrefix(p.confirm, "cancel_craft:")
	var options []option
	for _, o := range orders {
		if o.State != "in_progress" {
			continue
		}
		options = append(options, option{fmt.Sprintf("%s ×%d", p.s(o.RecipeName), o.OutputQuantity), fmt.Sprint(o.OrderID),
			p.f("%s｜還要 %s｜取消後退回原料", p.s(o.BuildingName), p.dur(true, o.RemainingSeconds)), "❌"})
	}
	p.selectMenu(p.s("❌ 取消加工訂單（退回原料）…"), options, func(ctx context.Context, it ui.Interaction, value string) error {
		i := slices.IndexFunc(orders, func(o services.OrderView) bool { return fmt.Sprint(o.OrderID) == value })
		if i < 0 {
			return gameerr.NotFoundf("找不到這個項目。")
		}
		o := orders[i]
		p.picker, p.confirm = nil, "cancel_craft:"+value
		p.refresh(ctx, it, p.f("⚠️ 確定要取消加工訂單（%d 份%s）嗎？原料會全部退回，後面排隊的訂單會往前遞補。再按一次「確認取消」。",
			o.OutputQuantity, p.s(o.RecipeName)), nil, p.s("重繪"), time.Time{})
		return nil
	}, 3, pending)
	if hasPending {
		id, err := strconv.ParseInt(pending, 10, 64)
		if err != nil {
			return
		}
		p.button(p.s("確認取消"), func(ctx context.Context, it ui.Interaction) error {
			p.act(ctx, it, p.call("cancel_craft", func(ctx context.Context) (services.ActionResult, error) {
				return p.svc().CancelCraft(ctx, p.playerID, id)
			}), actOpts{})
			return nil
		}, btn{emoji: "❌", row: 4, style: ui.Danger})
	}
}
