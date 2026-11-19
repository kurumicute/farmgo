# i18n：介面語言

中英文切換用的字串都放在這裡，程式之後一律從這裡取用。內容來自 `英文翻譯用語表_已填寫.md`（2026-11-19）。

| 檔案 | 內容 |
|---|---|
| `zh-TW.json` | 繁體中文（預設語言，其他語言缺少的字串會退回這裡） |
| `en.json` | 英文 |
| `i18n.go` | 讀取用的 Go 套件：`i18n.T("en", "core.gold")` → `Gold` |
| `i18n_test.go` | 檢查各語言的 key 和繁中一致、參數相同、沒有空白 |

## 怎麼改

格式是「分類 → key → 文字」兩層：

```json
{
  "core": {
    "gold": "Gold",
    "plot_number": "Plot #{n}"
  }
}
```

- **改翻譯**：直接改 `en.json` 右邊的文字。**左邊的 key 不要改**，程式是用 key 找文字的。
- **新增字串**：`zh-TW.json` 和 `en.json` 要**同時**加同一個 key。
- **`{n}`、`{h}` 這類大括號**：是參數，程式會代入數字。翻譯時要保留，位置可以調整，例如「第 {n} 格」→「Plot #{n}」。
- **key 的命名**：作物、魚、道具、建築、突變、品質、探險地區、成就的 key 直接用程式代碼（例如 `crop.ruby_corn`、`fish.tilapia`、`item.thief_glove`、`achievement.crops_100`），方便之後對應。
- **改完之後**：
  1. 跑 `go test ./i18n` 檢查有沒有漏、參數有沒有打錯。
  2. 重新編譯。JSON 是打包進執行檔的，不重新編譯不會生效。
- 存檔請用 UTF-8。用記事本編輯沒問題；用 Excel 開 JSON 會弄壞格式，不要這樣做。

## 翻譯原則（照用語表第 13 節）

- 美式英文，語氣輕鬆、親切，有溫暖的農場遊戲感（Stardew Valley 風格）。
- 同一個遊戲概念固定用同一個英文詞：`Plot` 是單一農田格、`Storage` 是倉庫、`Inventory` 是玩家背包。
- 按鈕用簡短的動詞片語，例如 `Harvest All`、`Water All`。
- 時間用精簡格式（`3h 20m`），金額寫成 `1,000 Gold`。
- 自創的地名、作物、魚和稱號用自然的英文遊戲命名，**不在括號裡附中文**。
- 日報、事件、幽默文案改寫成英文玩家看得懂的說法，不逐字直譯。
- 作物品質（Normal / Good / Rare / Legendary）和魚的稀有度（Common / Rare / Epic / Legendary）是兩套獨立分級。

## 目前的範圍

這裡現在只放用語表定好的 269 個詞，還沒有涵蓋 Bot 的 3,400 個介面句子。之後翻整句時，會照這些詞新增 key，例如 `bot.plots.harvest_done`。

## 介紹網站（site/en.json）

- `site/en.json`：介紹網站（`internal/site/static` 的 index.html 與 site.js）的英文，格式是「中文原文 → 英文」。
  - HTML 的文字以「前後空白去掉、中間空白壓成一個」的原文為 key；JS 是字串字面值的原文（樣板字串含 `${…}`，翻譯要保留同樣的 `${…}`）。
  - 有些是被 `<b>`、`<code>` 切開的片段，改的時候注意前後的空格與標點要接得起來。
  - 這裡找不到的會再查 Bot 的 `en.text.json`；網站上的遊戲資料（作物名稱、說明）也用 Bot 的文字表翻譯。
- 改了網頁的中文文字，要在這裡補上英文：`go test ./internal/site -run TestSiteFullyTranslated` 會列出英文頁面還留著的中文。
- 網站語言：網址 `?lang=` → Cookie `farmgo_lang` → 瀏覽器語言（中文 → 繁中，其他一律英文）；右上角 🌐 中文 | English 切換。
