# farmgm（Go 版）

農場經營遊戲的 Go 重構，來源是 Python 專案 `farmgmdiscordb-feature-exploration-update`。範圍只包含農場相關功能：音樂（`app/music`、Lavalink）不移植，繼續使用 Python 版；CMD（`app/cli`）決定不移植。

詳細進度、與 Python 版的差異、測試方式與檔案雜湊，請看 [`Go重構進度說明.md`](Go重構進度說明.md)。

## 目前狀態（2026-11-11）

**程式已全部移植**（網站、Discord 面板與 Bot），但 **Discord Bot 還沒有在真實 Discord 上操作過，也還沒有取代 Python 版**。Go 版 Bot 只有農場，沒有音樂（音樂要以 Python 的獨立音樂 Bot 執行）。

| 階段 | 內容 | 狀態 |
| --- | --- | --- |
| 1 | 遊戲規則（`app/game/`）、設定、時鐘 | ✅ 完成 |
| 2 | 資料庫核心、60 張表的模型、29 個 repositories | ✅ 完成 |
| 3 | 11 個服務（交易、鎖定、交易紀錄、一鍵操作）與死結重試 | ✅ 完成 |
| 4 | 監控／管理網站、介紹網站 | ✅ 完成 |
| 5 | Discord 農場面板（含農田圖片合成）與 Bot | ✅ 程式完成（所有面板頁面、農田圖片與 Python 逐像素相同、discordgo 轉接層）；🚧 待實機測試 |
| 6 | CMD | ❌ 不移植 |

## Go 版新增（Python 沒有）

- 果醬工坊多了番茄醬、葡萄果醬、南瓜果醬、西瓜果醬。**用 Go 版 Bot 做過新果醬後就不能再切回 Python 版 Bot**（Python 不認得這些代碼）。
- 選用的 Redis（`REDIS_URL`、`deploy/docker-compose.redis.yml`）：管理網站修改目錄或規則設定後，Bot 立即套用。
- 細節見進度說明的「Go 版新增的功能」。

## 原則

- **資料庫 schema 不變**：與 Python 版共用同一個 PostgreSQL，Migration 仍由 Python 專案的 Alembic 管理（目前 head 0038）。Go 版只檢查 `alembic_version`，不自己建表。兩版可以同時連同一個資料庫。
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
| `internal/game/*` | 純遊戲規則（17 個套件） |
| `internal/db`、`db/models`、`repositories/*` | 資料庫連線、交易與快取、資料表模型、資料存取 |
| `internal/services` | 遊戲服務（GameService → … → AdminService）、監控查詢、死結重試 |
| `internal/auth` | 管理員登入（scrypt、session、失敗鎖定） |
| `internal/web` | 監控／管理網站（`static/monitor` 是 Python 前端的原樣複製） |
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

## Docker（分階段上線：網站先換成 Go 版）

```bash
docker build -t farmgm-go .        # 映像內有 farmgm-web、farmgm-site、farmgm-bot、farmgm-hash-password，不含任何機密
# 在 Python 專案目錄：web、site 改用 Go 版，Discord Bot 仍是 Python 版
docker compose -f docker-compose.yml -f ../fmgonew/deploy/docker-compose.go-web.yml up -d --build web site
```

切換前要確認資料庫已是 Go 版對應的 Migration 版本（目前 0038），並先備份；步驟與注意事項見進度說明的「可以考慮的上線方式」。

Discord Bot 也準備好覆寫檔 `deploy/docker-compose.go-bot.yml`，但**還沒有實機測試**；切換前要先決定音樂的處理方式（Go 版沒有音樂，同步指令時會移除農場 Bot 上原本內建的音樂指令），見覆寫檔開頭的說明。

## 依賴

- Go 1.23 以上
- `github.com/jackc/pgx/v5`（PostgreSQL）
- `github.com/gorilla/websocket`（監控頁即時更新）
- `golang.org/x/crypto`（scrypt；原本就是 pgx 的依賴）
- `github.com/bwmarrin/discordgo` v0.29.0（Discord 連線與 REST 請求）
- `github.com/redis/go-redis/v9` v9.7.3（選用的 Redis 快取通知）
