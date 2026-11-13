# farmgm（Go 版）

農場經營遊戲的 Go 重構，來源是 Python 專案 `farmgmdiscordb-feature-exploration-update`。範圍是農場相關功能與音樂（`internal/music`，Lavalink）；CMD（`app/cli`）決定不移植。

詳細進度、與 Python 版的差異、測試方式與檔案雜湊，請看 [`Go重構進度說明.md`](Go重構進度說明.md)。

## 目前狀態（2026-11-13）

**程式已全部移植**（網站、介紹網站、Discord 面板與 Bot、音樂），**2026-11-12 正式環境已切換成 Go 版**（web、site、bot、redis）。切換方式與改回 Python 的條件見下方「Docker／正式環境」。

**種子實驗室（2026-11-13，Go 版新增）**：農場 Lv.5 花 5,000 金建造，鑑定、雜交（12 種雜交作物，售價 = 雙親 × 1.5）、突變研究、培育紀錄、分享種子（沒有實驗室也能收）、菇類與特殊種子圖鑑、10 個成就；所有數值可在管理網站「新功能設定 → 種子實驗室」調整。設計與流程圖見 [`種子實驗室流程圖.md`](種子實驗室流程圖.md)。**需要 Migration 0039**（在 Python 專案），正式環境要先升級資料庫，Go 版才會通過啟動檢查。

| 階段 | 內容 | 狀態 |
| --- | --- | --- |
| 1 | 遊戲規則（`app/game/`）、設定、時鐘 | ✅ 完成 |
| 2 | 資料庫核心、60 張表的模型、29 個 repositories（另加種子實驗室 5 張表） | ✅ 完成 |
| 3 | 11 個服務（交易、鎖定、交易紀錄、一鍵操作）與死結重試 | ✅ 完成 |
| 4 | 監控／管理網站、介紹網站 | ✅ 完成 |
| 5 | Discord 農場面板（含農田圖片合成）、Bot 與音樂 | ✅ 完成；2026-11-12 正式環境已切換成 Go 版 |
| 6 | CMD | ❌ 不移植 |

## Go 版新增（Python 沒有）

- 果醬工坊多了番茄醬、葡萄果醬、南瓜果醬、西瓜果醬。**用 Go 版 Bot 做過新果醬後就不能再切回 Python 版 Bot**（Python 不認得這些代碼）。
- 地下菇房（農場 Lv.4 開放，入口在「🌱 農田」頁）：菇種由上方農田的作物決定，投入探險帶回的木屑提高稀有菇機率；菇可以直接賣或在烘焙坊做料理。不需要 Migration，但和新果醬一樣，有玩家取得後就不能切回 Python 版 Bot。
- 選用的 Redis（`REDIS_URL`、`deploy/docker-compose.redis.yml`）：管理網站修改目錄或規則設定後，Bot 立即套用。
- 市場價格與商店種子隨季節變動：非當季作物售價 +30%，商店只賣當季種子。
- 牧場加工可以取消單一訂單（退回原料，後面的訂單往前遞補）。
- 所有種子改放素材背包（不佔倉庫），素材背包預設上限 1,000。**與 Python 版不同**。
- 管理網站「新功能設定」可以調整探險與地下菇房（存在 `feature_settings`，不需要 Migration）。
- 偷菜名單不列出沒有成長中或成熟作物的玩家。
- 種子實驗室（Migration 0039）：特殊種子的基因、雜交作物、夜光／豐收／水晶突變、分享種子。
- 音樂改寫成 Go（`internal/music`，Lavalink v4、16 個斜線指令）。
- 細節見進度說明的「Go 版新增的功能」。

## 原則

- **資料庫 schema**：與 Python 版共用同一個 PostgreSQL，Migration 仍由 Python 專案的 Alembic 管理（目前 head 0039；0039 是種子實驗室，只有 Go 版使用）。Go 版只檢查 `alembic_version`，不自己建表。
- **逐行對照 Python**：錯誤訊息、常數、計算結果與 Python 相同（Python `round()`、`//`、`%` 用 `internal/numfmt`；字串長度以字元計算；dict 順序有意義時另外保留順序）。
- **安全**：SQL 一律參數化；機密不寫入程式或日誌；網站只接受設定的 Host、管理 API 需要 CSRF 標頭與登入；管理員密碼雜湊與 Python 版互通。
- **設定**：系統環境變數優先於 `.env`，空值視為未設定，未知設定會被忽略。機密欄位不會出現在 `Settings.String()`。

## 目錄

| 路徑 | 內容 |
| --- | --- |
| `cmd/farmgm-web` | 監控／管理網站的程式進入點 |
| `cmd/farmgm-hash-password` | 產生管理員密碼雜湊 |
| `cmd/farmgm-site` | 介紹網站的程式進入點 |
| `cmd/farmgm-bot` | Discord Bot（只有農場）的程式進入點 |
| `internal/site` | 介紹網站（`static` 是 Python 前端的原樣複製） |
| `internal/clock`、`config`、`numfmt` | 時鐘、設定、與 Python 一致的數值運算 |
| `internal/game/*` | 純遊戲規則（18 個套件，含種子實驗室 `seedlab`） |
| `internal/db`、`db/models`、`repositories/*` | 資料庫連線、交易與快取、資料表模型、資料存取 |
| `internal/services` | 遊戲服務（GameService → … → AdminService）、監控查詢、死結重試 |
| `internal/auth` | 管理員登入（scrypt、session、失敗鎖定） |
| `internal/web` | 監控／管理網站（`static/monitor` 是 Python 前端的原樣複製） |
| `internal/music` | 音樂（Lavalink v4、Spotify 連結解析、斜線指令） |
| `internal/bot/*` | Discord Bot：面板與指令（`bot`）、互動抽象層 `ui`、discordgo 轉接層 `discordio`、延遲統計、訊息分段、服務呼叫上限、農田圖片 `farmimage`（素材是 Python 的原樣複製） |
| `internal/cachebus` | 選用的 Redis：網站與 Bot 的快取同步失效（Go 版新增） |
| `Dockerfile`、`deploy/` | Go 版映像；分階段上線的 compose 覆寫檔（`go-web`：網站，`go-bot`：Discord Bot） |
| `internal/dbtest`、`services/svctest`、`repositories/repotest` | 測試夾具 |

## 指令

```powershell
go run ./cmd/farmgm-web             # 監控／管理網站（讀工作目錄的 .env；預設 http://127.0.0.1:8000）
go run ./cmd/farmgm-hash-password   # 產生 ADMIN_PASSWORD_HASH（隱藏輸入）
go run ./cmd/farmgm-site             # 介紹網站（預設 http://127.0.0.1:24444）
go run ./cmd/farmgm-bot              # Discord Bot（需要 DISCORD_BOT_TOKEN；測試時設 DISCORD_DEV_GUILD_ID）
go vet ./...
go test -p 1 ./...     # -short 會縮減釣魚模擬次數
```

- 需要 Redis 的測試讀 `FARMGM_TEST_REDIS_URL`，沒有設定時略過。
- 需要資料庫的測試讀環境變數 `FARMGM_TEST_DATABASE_URL`；資料庫名稱必須以 `_test` 結尾，因為測試會清空遊戲資料表。沒有設定時，這些測試會自動略過。
- 測試資料庫的 schema 用 Python 專案的 `alembic upgrade head` 建立（步驟見進度說明的「建立測試資料庫」）。
- 所有測試共用同一個資料庫，一律加 `-p 1`，同一時間也只能有一個 `go test` 程序。
- 不要對正式資料庫或 `farmgm` 專案的 Volume 執行測試。

## Docker／正式環境

```bash
docker build -t farmgm-go .        # 映像內有 farmgm-web、farmgm-site、farmgm-bot、farmgm-hash-password，不含任何機密
```

2026-11-12 起正式環境的 web、site、bot、redis 都是 Go 版，在 Python 專案目錄以四個覆寫檔啟動：

```bash
docker compose -f docker-compose.yml -f ../fmgonew/deploy/docker-compose.go-web.yml   -f ../fmgonew/deploy/docker-compose.go-bot.yml -f ../fmgonew/deploy/docker-compose.redis.yml up -d --wait redis web site bot
```

- **之後執行任何 `docker compose` 都要帶這四個 `-f`**，否則會被換回 Python 版。
- 資料庫 Migration 仍由 Python 專案的 Alembic 管理（目前 0039）。
- 只要有玩家用過 Go 版新增的內容（新果醬、地下菇房的菇與料理、木屑、種子實驗室），就**不能再切回 Python 版 Bot**。
- 詳細步驟、備份位置與改回 Python 的方法，見進度說明的「正式環境切換（2026-11-12）」。

## 依賴

- Go 1.23 以上
- `github.com/jackc/pgx/v5`（PostgreSQL）
- `github.com/gorilla/websocket`（監控頁即時更新）
- `golang.org/x/crypto`（scrypt；原本就是 pgx 的依賴）
- `github.com/bwmarrin/discordgo` v0.29.0（Discord 連線與 REST 請求）
- `github.com/redis/go-redis/v9` v9.7.3（選用的 Redis 快取通知）
