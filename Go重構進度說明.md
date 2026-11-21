# 農場 Go 重構：進度說明（2026-11-11）

來源：Python 專案 `farmgmdiscordb-feature-exploration-update`（Alembic head 0038；2026-11-11 由 0036 同步，見「與 Python 新版同步」）。
目標：把**農場相關**的 Python 程式改寫成 Go。音樂（`app/music`、Lavalink）原本不移植，之後已改寫成 Go（`internal/music`，見「Go 版新增的功能」）。

目前完成 **第 1～3 階段**（遊戲規則、資料庫層、服務層）：11 個服務與 `RetryingService`（`Retrier`）都已移植，Python 服務層測試也都已移植或歸到之後的階段。第 4 階段（網站）也已完成：監控 API 全部 31 個端點、WebSocket、管理員登入與管理 API 防護、管理 API 全部 64 個端點、程式進入點（`cmd/farmgm-web`、`cmd/farmgm-hash-password`），以及介紹網站（`internal/site`、`cmd/farmgm-site`）。Discord 面板與 Bot 的程式已全部移植（第 5 階段第 1～8 批：所有面板頁面、discordgo 轉接層、`cmd/farmgm-bot`），CMD 決定不移植。**Discord Bot 還沒有在真實 Discord 上操作過，也還沒有取代 Python 版**（見「第 5 階段」的實機測試與切換前的決定）；網站部分可以考慮先上線（見「可以考慮的上線方式」）。

> **暫停點（2026-11-11）**：第 4 階段完成並同步到 Python 0038 後，使用者請我依建議決定：Discord 函式庫用 `github.com/bwmarrin/discordgo`、採「分階段上線」（網站先上線）。已完成上線準備（`Dockerfile`、`deploy/docker-compose.go-web.yml`，實際建置與啟動過）、第 5 階段第 1 批（`internal/bot` 基礎）與第 2 批（農田圖片 `internal/bot/farmimage`，拼圖與減色結果都與 Python 逐像素相同）、第 3 批（面板框架、`/farm`、`/help` 與歡迎、總覽、農田、背包、說明頁）、第 4 批（商店、作物價格、品質與突變、販售頁；畫面與 Python 逐字比對相同）、第 5 批（動物、寵物、加工、建造設施、防守、賭場頁，含巡查報告與防守紀錄 2 處 CMD 文字的 Go 函式）、第 6 批（偷菜、排行榜與排名獎勵、任務與競賽、群組、山洞、機器、操作紀錄；其餘 2 處 CMD 文字也改寫成 Go 函式）、第 7 批（子面板：特賣與升級、成就與圖鑑、古代遺跡、世界 Boss、釣魚，含釣魚畫面的自動更新）、第 8 批（Bot 本體與 discordgo 轉接層 `internal/bot/discordio`、`cmd/farmgm-bot`、Docker 映像與 `deploy/docker-compose.go-bot.yml`；以本機假 Discord API 測試）。下次從「第 5 階段」標示「從這裡繼續」的實機測試開始：需要使用者提供測試用 Bot 的 Token 與測試伺服器，並決定音樂的處理方式（Go 版 Bot 沒有音樂）。**正式環境還沒有切換**（要使用者確認後才做）。

## 進度

| 階段 | 內容 | 狀態 |
| --- | --- | --- |
| 1 | 遊戲規則（對應 `app/game/` 全部 18 個模組）、設定、時鐘 | ✅ 完成 |
| 2 | 資料庫核心、60 張表的模型、29 個 repositories | ✅ 完成 |
| 3 | services（交易、鎖定、`_record` 交易紀錄不變式、一鍵操作；約 8,200 行 Python） | ✅ 完成（2026-11-10） |
| 4 | 管理／監控 API、介紹網站（前端靜態檔原樣沿用） | ✅ 完成（2026-11-11）：監控 API 31 個端點、WebSocket、登入與管理 API 防護、管理 API 64 個端點、程式進入點、介紹網站 |
| 5 | Discord 農場面板（含 Pillow 農田圖的改寫）與音樂（Lavalink） | ✅ 完成；2026-11-12 正式環境已切換成 Go 版 |
| 6 | CMD | ❌ 不移植（2026-11-10 決定；Python 的 `app/cli` 與 `test_cli.py` 不移植，CMD 相關的測試段落也不移植） |


## 整體進度與剩餘工作

以要移植的 Python 程式碼行數估算（不含音樂 1,445 行與 CMD 1,203 行；2026-11-10 統計）：

| 部分 | Python 行數 | 狀態 |
| --- | --- | --- |
| 遊戲規則 `app/game`、資料庫 `app/db`、資料存取 `app/repositories`、服務 `app/services`、設定與時鐘 | 約 18,100 | ✅ 完成（含監控查詢 `monitor_service.py`） |
| 網站：`app/api`、`app/auth.py`、`app/main.py`、`app/site` | 約 2,200 | ✅ 完成 |
| Discord 面板 `app/discord_bot`（含 Pillow 農田圖與 `farm/` 圖片素材） | 約 5,600 | 程式完成（第 1～8 批）；剩實機測試 |
| **合計** | **約 25,900** | **程式全部移植完成；剩 Discord 實機測試與切換** |

- Go 版目前約 7.1 萬行（含測試），`go test` 共 691 個測試。
- **以工作量估計剩下約 4～5 成**：Discord 面板要換成 Go 的 Discord 函式庫（按鈕、選單、互動逾時都要重新對應），農田圖片要用 Go 的 `image` 套件重寫並比對輸出，面板互動也難以完全自動化測試，最後需要在測試用的 Discord 伺服器上實際操作確認。
- **粗估批數**：第 4 階段已完成；第 5 階段共 8 批，全部完成；剩實機測試（需要測試用 Bot Token 與 Discord 伺服器）。

### 可以考慮的上線方式

- **全部完成再切換**：第 4、5 階段都完成後，Go 版同時取代網站與 Discord Bot。
- **分階段上線**：第 4 階段完成後，先讓 Go 版接手網站（監控、管理、介紹網站），Discord Bot 繼續使用 Python 版。兩版共用同一個資料庫，交易紀錄、鎖定順序、背景結算的 advisory lock 都已與 Python 對照，可以並行運作。切換前仍要在測試環境實際跑過，並確認只有一邊執行背景結算（`BACKGROUND_TASKS_RUNNER`）。

**2026-11-11 決定：分階段上線**（使用者請我依建議決定）。準備好的檔案：

- `Dockerfile`：多階段建置（golang:1.27-alpine → alpine:3.22），靜態執行檔 `farmgm-web`、`farmgm-site`、`farmgm-hash-password`，以非 root（uid 10001）執行，映像內沒有任何機密（`.dockerignore` 只送 `go.mod`、`go.sum`、`cmd`、`internal`）。
- `deploy/docker-compose.go-web.yml`：Python 專案 `docker-compose.yml` 的覆寫檔，只把 `web`、`site` 換成 Go 版（`bot`、`music` 不變，Port 仍只綁本機，設定沿用 `.env`）。在 Python 專案目錄執行 `docker compose -f docker-compose.yml -f ../fmgonew/deploy/docker-compose.go-web.yml up -d --build web site`；改回 Python 版時不加覆寫檔再 `up -d --build web site`。
- 已實際驗證：映像建置成功；以測試資料庫啟動兩個容器，healthcheck（wget）通過，`docker stop` 約 1 秒優雅關機；`docker compose config` 確認覆寫後只有 web、site 改用 Go 映像。
- **切換前要做（需要使用者確認）**：正式資料庫是 0038；用真實瀏覽器操作監控頁、管理頁、介紹網站；確認 `BACKGROUND_TASKS_RUNNER`（預設 web：由 Go 網站結算，Python Bot 不結算）；先備份。
- **Discord Bot（第 8 批準備好，尚未切換）**：映像加入 `farmgm-bot`；`deploy/docker-compose.go-bot.yml` 只把 `bot` 換成 Go 版（可和 go-web 覆寫檔一起用）。切換前要使用者決定：Python 的 `bot` 服務在沒有 `MUSIC_BOT_TOKEN` 時內建音樂，**Go 版沒有音樂**，切換後要另建音樂 Bot 應用程式、設定 `MUSIC_BOT_TOKEN` 並以 `--profile music` 啟動 Python 的 `app.music`；Go 版同步指令時整批覆寫，只留 `/farm`、`/help`（原本內建音樂的 `/play` 等會從農場 Bot 移除）。Python 與 Go 版的 Bot 不能同時用同一個 Token 執行。

## Go 版新增的功能（Python 沒有，2026-11-11 使用者要求）

- **果醬工坊新增四種果醬**（`balance.Recipes`、`balance.Goods`）：番茄醬（番茄 ×3、45 分、195 金）、葡萄果醬（葡萄 ×3、75 分、395 金）、南瓜果醬（南瓜 ×3、90 分、485 金）、西瓜果醬（西瓜 ×3、120 分、1,145 金）。售價約為原料市價的 2.9 倍，與草莓果醬（草莓 ×3、60 分、300 金）相同比例；加工時間依作物價值遞增。世界 Boss 的**預設設定**也收這四種果醬（醫療補給，20／40／49／115 分，1 分約 10 金售價，與草莓果醬相同）；已建立的活動沿用建立當時的設定。商品不寫資料庫（與其他加工品相同），所以不需要 Migration；圖鑑、自動加工機、販售、介紹網站的配方表會自動列出。測試：`services/jam_test.go`。
  - **相容性（重要）**：Python 版不認得這四種代碼（`balance.RECIPES[code]`、`balance.GOODS[code]` 直接查表）。只要有玩家用 Go 版 Bot 做過新果醬，就**不能再切回 Python 版 Bot**（背包、販售、自動加工會出錯）；只換網站、Bot 仍是 Python 版時不受影響（新果醬只能在 Go 版 Bot 加工）。
- **Redis（選用，`internal/cachebus`）**：網站與 Bot 是不同程序，遊戲目錄（作物、動物、魚種、品質設定）與新功能規則設定各自快取 10 秒。設定 `REDIS_URL` 時以 Redis Pub/Sub 通知：寫入目錄的交易**提交後**、管理員儲存或還原規則設定後發布一則通知，其他程序立即清空快取（自己發布的不重複處理；訂閱重新連上時也清空一次，避免斷線期間漏掉通知），快取保存時間從 10 秒拉長到 60 秒（只是漏掉通知時的安全網），減少資料庫查詢。Redis 只傳通知、不保存遊戲資料；沒有設定或連不上時照常啟動，回到每 10 秒重新讀取。`farmgm-web`、`farmgm-bot` 使用（介紹網站另有自己的回應快取，不需要）。部署：`deploy/docker-compose.redis.yml`（Redis 不開 Port、不寫磁碟、32 MB 上限）。依賴 `github.com/redis/go-redis/v9` v9.7.3（相容 Go 1.23；最新版要求 Go 1.26，所以固定這個版本）。測試讀 `FARMGM_TEST_REDIS_URL`（沒有設定時略過），也在 Linux 容器以 `-race` 跑過。
- **手動測試用資料庫 `farmgm_play`**：`go test` 的初始化（`svctest`）為了讓測試不受季節與時間影響，會把 `farmgm_test` 的作物、魚的季節清空、生長時間改短，所以用 `farmgm_test` 手動測試時看不到四季。手動測試改用另外建立的 `farmgm_play`：以 Python 專案的 `alembic upgrade head`（用 Python 映像掛入目前的原始碼執行）從空資料庫建到 0038，季節、價格、生長時間與正式環境相同，`go test` 不會碰它。
- **市場價格與商店種子隨季節變動**（`seasons.SellPrice`）：作物在非當季時市場售價 +30%（`OffSeasonSellPct = 130`，背包、販售、價格頁一致）；商店只販售當季可以種的種子，買非當季種子會被拒絕。測試：`services/go_features_test.go`、`bot/panel_go_features_test.go`。
- **取消特定加工單**（`RanchService.CancelCraft`）：牧場加工頁新增「❌ 取消加工訂單」選單與確認按鈕，退回原料（檢查倉庫／素材空間），同一建築後面排隊的訂單往前遞補；以「取消加工訂單 #編號」寫入交易紀錄。不需要 Migration。
- **地下菇房（2026-11-12，`game/mushroom`、`services/mushroom.go`、`bot/pages_cellar.go`、`farmimage/cellar.go`）**：農場 **Lv.4** 開放，不用建造（已經 Lv.4 以上的第一次打開就能用）；入口在「🌱 農田」頁的「🍄 地下菇房」（總覽的按鈕已滿 25 個）。4 個菇床不用種植、不用澆水，每 24 小時長好一批（每床 2 朵），收成後立即開始下一批。**長什麼菇由上方農田目前種的作物（養分）決定**：穀物（小麥、玉米，以及不在名單上的作物／沒有作物）→ 香菇 200／✨靈芝 520；蔬菜（胡蘿蔔、番茄、南瓜）→ 洋菇 230／✨松露 700；水果（草莓、葡萄、西瓜、琥珀瓜、月光花）→ 金針菇 260／✨發光菇 900。每一朵依各養分的農田格數加權擲骰，收成當下才擲。**木屑**（新素材 `sawdust`，放素材背包）每次探險（含古代遺跡）在原本掉落之外固定帶回：古老森林 3–4 個，草原、礦坑、遺跡 2–3 個；每床每批最多投入 4 個，稀有機率 0／1／2／3／4 個 = 5／15／25／35／50%，投滿 4 個時保證一半以上是稀有菇；收成後木屑歸零。倉庫放不下時依菇床編號收成放得下的部分。菇類可以直接賣，也可以在**烘焙坊**做成料理：香菇麵包 560、洋菇濃湯 620、金針菇煎餅 680（菇 ×2＋麵粉 ×1）、靈芝養生糕 1,350、松露麵包 1,800、發光菇派 2,300（菇 ×2＋麵粉 ×2），售價約為原料的 1.2 倍。
  - **不需要 Migration**：菇床狀態存在 `farm_tools`（`tool_code = mushroom_cellar`）的 `settings` JSON；投入木屑記為 `craft_start`、收成記為 `craft_collect`（每種菇一筆，會登記圖鑑）。圖片底圖與 6 種菇的素材取自專案根目錄的 `b.png`、`a.png`（縮小後放在 `farmimage/farm/mushroom/`）。
  - **相容性**：與新果醬相同，Python 版不認得這些菇、料理與木屑代碼；有玩家取得後就不能再切回 Python 版 Bot。
  - 測試：`game/mushroom`（養分對應、稀有機率隨木屑增加、投滿保證一半稀有、設定讀寫）、`services/mushroom_test.go`（Lv.3 鎖定、Lv.4 自動建立、投入上限、依作物擲骰、倉庫不足部分收成、販售、烘焙坊料理、探險帶回木屑）、`bot/panel_go_features_test.go`（從農田頁進入、投入木屑、收成）、`farmimage/cellar_test.go`。原本的探險測試改為預期多一項木屑。
- **加工與種子庫微調（2026-11-13，使用者要求）**：加工頁的配方清單與「開始加工」選單只列出現在做得出來的配方（設施已建造、原料至少 1 份），其餘以一行統計帶過；巨型作物（只是收成數量 ×2、售價與普通相同）也能當加工原料（`services.CraftMutations`：普通品質的無突變與巨型，先扣無突變；自動加工機與培育器也適用；取消訂單退回時一律退成無突變）。種子庫上限可在管理網站調到 200（原本最多 25），超過 25 顆時種子庫頁與雜交頁的母本／父本選單分頁顯示（`pageOf`、`pageButtons`）。
- **所有種子改放素材背包、素材背包上限 1,000（2026-11-12，使用者要求）**：商店種子、種子製造機做的種子、特賣種子都和稀有種子一樣放素材背包，不佔倉庫（`inventory.IsMaterial`／`InWarehouseSQL`／`MaterialQuantity` 以 `seed_` 前綴判斷，新增的作物也適用）；購買種子改為檢查素材背包空間。素材背包預設上限從 500 改為 1,000（`exploration.MaterialBagCapacity`；管理網站「探險」設定可再調整）。已持有的種子不用搬移，立即改算在素材背包；超過上限的玩家不會遺失物品，只是要先用掉一些才能再放入。**與 Python 版不同**：Python 版的種子仍佔倉庫。
- **管理網站可調整探險與地下菇房（2026-11-12，`game/exploration/config.go`、`game/mushroom/config.go`）**：「新功能設定」新增「探險」與「地下菇房」兩項，存在既有的 `feature_settings`（key `exploration`、`mushroom`，**不需要 Migration**），儲存後網站立即生效、Bot 最晚 10 秒內套用，每次儲存寫管理操作紀錄。
  - 探險可調整：各區域（草原、森林、礦坑、古代遺跡）的名稱、說明、解鎖等級、探險／冷卻時間、每次抽獎次數、高級探索等級、等級說明、每次帶回的木屑數量、四種掉落池（物品、需要等級、權重、數量範圍）、特殊事件（名稱、說明、需要等級、權重、假幣、額外抽獎），以及普通／高級探索的獎勵類型機率（合計必須 100%）、遺跡額外抽獎份數、素材背包上限、假幣匯率。區域代碼與 emoji、素材種類、機器與培育配方固定。
  - 地下菇房可調整：開放等級、每批生長時間（立即套用到正在生長的菇床）、每床每批收成朵數、投入 0–4 個木屑的稀有機率、投滿是否保證一半稀有、各養分對應的作物。菇床數（圖片版面）、每床木屑上限與菇／料理售價固定。
  - 沒有自訂設定時直接用原本的程式常數，行為與調整前完全相同；`exploration.Current()`／`mushroom.Current()` 依 `featurecfg.Version()` 快取解析結果。遊戲、Discord 說明頁（山洞、菇房的等級與時間）與介紹網站都讀目前設定。
  - 介紹網站：手冊新增「🍄 地下菇房」（養分與作物、稀有機率、各區域帶回的木屑）、農場卡片與速查表一列；山洞的區域表加上木屑。
  - 測試：`game/exploration/config_test.go`、`game/mushroom/config_test.go`（預設值來回、設定生效與還原、驗證錯誤）、`services/progress_admin_test.go` 的 `TestExplorationAndMushroomSettings`。也以本機 `farmgm_play` 實際啟動網站，在管理網站儲存兩項設定並確認介紹網站跟著更新（之後已還原）。
- **種子實驗室（2026-11-13，Migration 0039，`game/seedlab`、`repositories/seedlab`、`services/seedlab.go`、`bot/pages_lab.go`）**：設計與流程圖見 `種子實驗室流程圖.md`。農場 **Lv.5** 花 5,000 金幣建造（不需要素材），入口在「🌱 農田」頁的「🧪 種子實驗室」。**所有數值都可以在管理網站「新功能設定 → 種子實驗室」調整**（`game/seedlab/config.go`，存在 `feature_settings` key `seedlab`，不需要 Migration）：開放等級、建造費用、種子庫上限、鑑定費、培育與突變研究時間、各種機率、基因效果、分享上限、研究經驗與每個雜交組合的成功率與時間（設定卡同時列出每種雜交作物的目前售價與公式售價；售價在「作物目錄」調整）。
  - **2026-11-14 平衡與配方隱藏（玩家回饋）**：下面各項的數值是當初的設定；新的預設值見本節最後的「基因強化平衡」。
  - **鑑定**：背包的一般種子 ×1＋鑑定費（種子價格 ×5，至少 50）→ 有 4 個基因（生長、產量、品質、突變，1–5 級）的特殊種子；一般作物 1–2 級、稀有作物 2–3 級。種子庫上限 25 顆。
  - **雜交**：同品種一定可以配對（改良基因，2 小時）；不同品種只有 12 組可以雜交出雜交作物（黃金穀物、彩虹蘿蔔、紫莓、蜜糖瓜、燈籠瓜、星夜花、紅寶玉米、櫻花麥、夕陽瓜、翡翠葡萄、曙光瓜、赤月花；成功率 40–70%、1–3 小時）；雜交作物的種子價格與售價 = 雙親的總和 × 1.5（Migration 依資料庫目前的雙親價格計算）。每個基因 50% 繼承母本或父本，再 15% 升級、10% 退化；世代 = 較大的世代 + 1。選用材料：樹脂（成功率 +15%）、藥草（時間 -30%）、水晶（指定基因升級機率 +20%）。失敗時得到母本品種的種子並退回材料。
  - **突變研究**：水晶 ×2（必要）＋古代核心（選用，成功率加倍），2 小時；成功率 30% + 突變基因每級 5%（上限 90%）。新增三種實驗室限定突變：夜光（售價 200%）、豐收（收成 +50%，至少 +1）、水晶（收成時額外 1–2 個水晶）；一般種植不會抽到。突變寫在種子上，會隨雜交（50%）與傳承保留。
  - **種植**：特殊種子種到空地後，生長基因每級 -5% 生長時間（澆水時套用）、產量基因每 2 級 +1 收成、品質基因每級 +1% 品質加成、突變基因每級 +0.5% 突變機率；種子的突變直接成為作物的突變。收成時 30% 取回 1 顆同基因的種子（傳承）。2026-11-15 起「🌱 農田」頁也有「🧬 勾選特殊種子」多選選單（第 4 列；蚯蚓按鈕移到第 2 列）：列出種子庫裡現在能種的種子（不含培育中、停用或不在季節的，最多 25 顆），勾選的每顆依序種到一格空地（`GameService.PlantLabSeeds`，空地不夠時全部不種）；沒有可種的特殊種子或沒有空地時不顯示。
  - **分享**：可以分享給同伺服器（群組）有農場的玩家，**對方沒有實驗室（或未滿 Lv.5）也能收**，並在實驗室頁的「種子庫」種下；每人每天最多送出 5 顆，每人每天只能收 1 位玩家的分享（台灣日期）。先依 farm_id 由小到大鎖定雙方農場再檢查上限，並行時也不會超過。
  - **研究等級**：鑑定 +2、雜交 +10（失敗 +15）、突變研究 +8 經驗；Lv.2（100）2 個研究槽、Lv.3（300）3 個。
  - **圖鑑**：新增「🍄 菇類」（6 種菇，原本列在加工品，已移出）與「🧪 特殊種子」（取得過哪些作物的特殊種子：鑑定、雜交、收成取回、收到分享時登記，`farm_codex.item_code = labseed_<作物>`）兩個分類；實驗室頁有「特殊種子圖鑑」「菇類圖鑑」按鈕。「全部圖鑑」的總數因此變多。
  - **成就**：新分類「🧪 實驗室與菇房」10 個：實驗室落成、育種學徒（雜交 10）、育種專家（雜交 100，稱號）、新品種誕生、雜交大師（12 種雜交作物，稱號）、突變研究員（稱號）、慷慨育種家（分享 10 顆，稱號）、種子收藏家（特殊種子圖鑑 100%，稱號）、採菇人（菇 100 朵）、菇類達人（菇類圖鑑 100%，稱號）。
  - **介紹網站**：新增卡片與「🧪 種子實驗室」說明（規則與數值依目前設定），**不列出雜交組合與雜交作物**；作物表、每週特賣也不包含雜交作物（每週特賣原本會把所有商店不賣的作物當成稀有種子輪流販售，已排除雜交作物）。
  - **資料庫（Migration 0039，在 Python 專案）**：新表 `seed_labs`、`lab_seeds`、`lab_jobs`、`lab_records`、`lab_seed_shares`；`farm_plots.lab_seed_id`；突變 CHECK 加入 glow、bountiful、crystal；事件類型加入 `seed_lab`、`seed_share`；`crop_types` 新增 12 種雜交作物（`shop_buyable = false`，種子製造機也不能做）。`db.AlembicHead` 改為 0039。降版會刪除新表、把新突變改回 none、事件改成 use_item。
  - 農田圖片：12 種雜交作物各 4 張（未成熟、成熟、乾枯、變種成熟），由使用者提供的 `example.png` 切割、去背，做成 **76×76**（比現有作物的 64×64 大，植物最高 72 像素，在農田上比較顯眼；農田圖照圖檔原尺寸貼上、底部對齊），放在 `farmimage/farm/crops/<代碼>/`。實驗室頁面不產生圖片（點擊頻繁，避免卡頓）。
  - 測試：`game/seedlab`（雜交表、繼承、突變研究、鑑定與基因效果）、`services/seedlab_test.go`（建造、鑑定、雜交成功與失敗、突變研究、特殊種子的種植與收成、分享上限）、`bot/panel_lab_test.go`（從農田頁進入到分享的完整流程）。
  - **配方隱藏（2026-11-14，使用者要求）**：雜交頁的配方表只列出**已發現**的配方（特殊種子圖鑑有該雜交作物＝雜交成功、收成取回或收到分享過，`services.knownHybrids`），其餘顯示「❔ 還有 N 種配方沒有發現」。父本選單改列出種子庫所有其他種子（不再只列相容的），任兩個品種都可以「試驗雜交」：不在雜交表中的組合一定得到母本品種、材料退回（和雜交失敗相同）；沒發現的配方在培育前、培育中都不顯示結果與成功率（`LabJobView.Unknown`）；雜交失敗但這個組合其實有配方時提示「好像有反應」；第一次成功時顯示「📖 發現新配方」。介紹網站本來就不列出組合。管理網站仍顯示全部組合。
  - **基因強化平衡（2026-11-14，玩家回饋「成本比收入高、不如種基本種子」）**：依正式環境的作物價格試算（含鑑定費與取回種子的機率），原本剛鑑定的種子每小時收益只有一般種子的 0.2–0.8 倍（小麥甚至虧錢），品質、突變基因換算成售價不到 +2%。新的預設值：鑑定費 種子價 ×1、最低 10；收成取回種子 60%；生長每級 -6%；產量改為**每級 +原收成數量的 15%（無條件進位）**（新設定 `yield_pct_per_level` 取代 `yield_levels_per_bonus`，`seedlab.YieldBonus(level, baseQuantity)`）；品質每級 +5%；突變每級 +1.5%；基因升級 25%、退化 5%；同品種改良 30 分鐘。試算後剛鑑定的種子約為一般種子的 1.2–1.6 倍，全部 5 級約 2.5–3.9 倍。`parseConfig` 改為缺少的設定項目用預設值（之前存的快照少了新 key 時不會整份失效），已移除的 key 在下次儲存時丟掉。**正式環境已經存過設定快照，改預設值不會生效，要在管理網站「新功能設定 → 種子實驗室」改成上面的數值後儲存。**
  - **培育減負（2026-11-15，玩家回饋「雜交 2 換 1 要金字塔堆疊、弱親本 50% 拖累、種滿 11 格要 22 顆種子」，Migration 0040）**：
    - **雜交只消耗父本**：母本完成後放回種子庫（`services.collectCross`），種子數量不再每代減半；領取訊息與雜交頁說明會寫「母本已放回種子庫」。
    - **優勢遺傳**（`seedlab.Inherit`，設定 `dominant_bp` 預設 70%）：每個基因以一次亂數決定來源，u < 70% 取較高的親本，其餘區間前半母本、後半父本（設 0% 時與原本的 50%／50% 相同）。3333 × 2222 得到全部 ≥3 級約 13% → 57%。
    - **育苗（新功能）**（`services.StartClone`／`collectClone`，研究槽工作 `kind = clone`）：在「🌰 種子庫」選種子按「🌱 育苗」，付金幣（`seedlab.CloneFee`；2026-11-16 起 = 作物售價 × 一次收成數量（含產量基因，不含突變）× `clone_fee_pct`%（預設 50%），無條件進位，例如售價 300、收成 3 個 → 450；原本是 300 + 20 ×（基因總和 − 4）²）與藥草 ×1，1 小時後複製一顆**同基因、同世代**的種子，**不複製突變**（避免水晶突變等無限複製），來源種子培育中暫停使用、完成後放回種子庫；種子庫要有空位（含其他還沒領取的育苗），研究經驗 +3。設定：`clone_fee_base`、`clone_fee_per_level`、`clone_herb`、`clone_minutes`、`exp_clone`。
    - **數值調整**（預設值與正式環境的設定一起改）：收成取回種子 60% → 85%、基因退化 5% → 2%、水晶催化 +20% → +30%。
    - **Migration 0040**（Python 專案）：`lab_jobs.kind`、`lab_records.kind` 的 CHECK 加入 `clone`；降版刪除育苗的工作與紀錄、來源種子改回種子庫。`db.AlembicHead` 改為 0040。
    - 管理網站設定卡新增「🌱 育苗」群組與優勢遺傳、育苗經驗；介紹網站的實驗室說明、Discord 種子庫／雜交頁的說明同步更新（`/api/guide` 新增 `dominant_pct`、`clone_herb`、`clone_minutes`、`clone_fees`、`exp.clone`）。
    - 測試：`game/seedlab` 的 `TestInheritDominant`、`TestCloneFee`（舊的繼承測試以 `noDominant` 關閉優勢遺傳）、`services/seedlab_test.go` 的 `TestSeedLabClone` 與 `TestSeedLabCross`（只消耗父本）、`bot/panel_lab_test.go` 的 `TestLabClonePanel`。
  - **育苗改版與化肥複選（2026-11-15，使用者要求）**：
    - **只要金幣就能育苗**，藥草改成選用；**每次 1–3 顆**（`seedlab.RollCloneCopies`：2 顆以上 30%、3 顆 10%；加藥草各 +20% → 50%、30%，每次用 `clone_herb` 個藥草）。
    - **排程**：一次 1–10 次（`clone_max_batches`），費用、藥草、時間 × 次數，全部完成後一起領取（研究槽工作的材料記 `batches`；之前建立的育苗算 1 次、有用藥草的算加藥草）。種子庫要能放下每次至少 1 顆（含其他還沒領取的育苗），多出來的照樣放進種子庫並提示超過上限。研究經驗 = 育苗經驗 × 次數。培育紀錄記下得到的顆數（`labrepo.Snapshot.Copies`）。
    - 新頁面「🌱 育苗」（`bot/pages_lab.go` 的 `buildLabClone`，種子庫選種子後按「育苗」進入）：每次與這次排程的費用、時間、顆數機率，選次數的選單、「加藥草」切換與「開始育苗」。
    - 新設定：`clone_two_bp`、`clone_three_bp`、`clone_herb_bonus_bp`、`clone_max_batches`（3 顆的機率不能高於 2 顆以上）；`clone_herb` 改成加藥草時每次用掉的數量（1–100）。介紹網站 `/api/guide` 新增 `clone_max_batches`、`clone_two_pct`、`clone_three_pct`、`clone_herb_two_pct`、`clone_herb_three_pct`。
    - **化肥複選**：農田頁的「🧪 施化肥」選單改成可以勾選多格（最多持有的化肥數量，每格顯示目前還要多久），`services.UseFertilizerPlots` 在同一筆交易施肥，任何一格不能施時全部不施；重複的格子只算一次。原本的 `UseFertilizer` 改成共用 `fertilizePlot`，訊息不變。
    - 測試：`game/seedlab` 的 `TestCloneFee`（顆數機率）、`services/seedlab_test.go` 的 `TestSeedLabClone`（排程、加藥草、顆數、種子庫空位）、`services/fertilizer_test.go` 的 `TestUseFertilizerPlots`、`bot/panel_lab_test.go` 的 `TestLabClonePanel` 與 `TestFertilizerMultiSelect`。
- **自動釣魚機與魚簍升級（2026-11-15，使用者要求，Migration 0041；`game/autofish`、`services/autofish.go`）**：
  - **自動釣魚機**（`balance.ToolAutoFisher` = `auto_fisher`，奢侈品工具，預設 10 萬金幣）：啟用時每 5 分鐘釣 1 條魚放進魚簍，魚簍滿了就暫停（滿的期間不累積）。不用背景排程：打開釣魚中心、魚簍或拋竿時（`settleAutoFish`）依上次結算的時間補算，結算時間存在 `farm_tools.settings.last_at`；停用前先補算、重新啟用從啟用的時間開始算。
  - 釣到的魚：不需要特定魚餌、當下（每條各自的釣到時間）季節與時段有的魚，稀有度用釣魚設定的基礎機率（不受魚餌加成）；`farm_fish.session_id` 為 NULL、`auto = true`，**不算進釣魚排行榜**（`fishrepo.LeaderboardOf` 排除），魚類圖鑑照算。亂數用 `GameService.AutoFishRng`。
  - **自動販售機賣魚**：自動販售機也啟用（且設定允許）時，自動釣到的魚直接賣掉入帳（交易紀錄 `fish_sell`），最多補算 24 小時；切換自動販售機前先結算到現在。手動釣的魚不會自動賣。
  - **魚簍升級**（`farms.fish_basket_level`，0–10）：魚簍頁的「升級魚簍」（按兩次確認），預設 40 格 30,000、60 格 50,000、80 格 100,000、100 格 200,000；0 級用釣魚設定的 `basket_capacity`。拋竿、釣魚中心、魚簍、結算畫面都用升級後的容量（`autofish.BasketCapacity`）。
  - **管理網站「新功能設定 → 自動釣魚與魚簍」**（featurecfg key `autofish`）：自動釣魚機價格、間隔（分鐘）、自動販售機是否賣魚、自動販售最多補算幾小時、魚簍各級容量與價格（容量要一級比一級大）。自動釣魚機的價格經 `balance.PriceOverride`／`balance.ItemPrice`，商店、背包、購買與介紹網站都讀目前的價格。
  - 介紹網站釣魚說明新增魚簍升級與自動釣魚機（`/api/guide` 的 `fishing.auto_fish`）；釣魚中心與魚簍顯示自動釣魚機的狀態（運作中／下一條的時間、魚簍滿了暫停、已停用）與這次補算的結果。
  - **Migration 0041**（Python 專案）：`farms.fish_basket_level`（CHECK 0–10）、`farm_fish.session_id` 改成可以是 NULL、`farm_fish.auto`；降版刪除自動釣到的魚。`db.AlembicHead` 改為 0041。
  - 測試：`game/autofish/autofish_test.go`、`services/autofish_test.go`（補滿後暫停、賣魚後重新計時、自動販售與 24 小時上限、停用與重新啟用、排行榜不算、魚簍四級升級）、`bot/panel_autofish_test.go`。
- **付費道具：自動釣魚機吃魚餌與加速、加速沙漏、實驗室擴充、農場裝飾（2026-11-15，使用者要求，Migration 0042；設計與流程圖見 `付費道具流程圖.md`）**：
  - 目的是回收有錢、愛自動化的玩家的金幣（2026-11-15 正式環境 7 天：收入約 142 萬、支出約 127 萬，中位數金幣 587，沒有通膨），全部都是選用的，不碰新手一定會付的費用。
  - **自動釣魚機消耗魚餌**（`autofish.ConsumeBait`、`BaitPerCatch`，預設每條 1 個）：每釣 1 條扣裝備中的魚餌，沒有魚餌就暫停（不累積）；釣到的魚改成跟手動釣魚一樣依魚餌決定（稀有度加成、特定魚餌的魚）。買釣具、換裝備、賣魚之前先結算（`settleAutoFish`），避免沒魚餌或魚簍滿的期間被補算。
  - **自動釣魚機加速**（`farm_tools.settings.speed_level`；`autofish.SpeedIntervals`／`SpeedPrices`，預設 4 分鐘 50,000、3 分鐘 120,000）：釣魚中心的「⚡ 釣魚機加速」（按兩次確認），加速前先用原本的速度結算。
  - **⏳ 加速沙漏**（`balance.ItemHourglass`，新設定 key `hourglass`：價格 500、剩餘時間 -50%、剩不到 60 秒不能用）：實驗室主頁的「使用加速沙漏」選單（研究槽的雜交、突變研究、育苗）與地下菇房的「加速沙漏」按鈕（所有生長中的菇床，開始時間往前移）。道具價格可調的做法改成 `balance.RegisterPrice`／`ItemPrice`（自動釣魚機也改用這個）。
  - **種子庫擴充**（`seed_labs.library_level`；`seedlab.LibraryStep` 10 格、`LibraryPrices` 10,000／20,000／40,000／80,000）：種子庫頁的「種子庫 +10 格」；容量一律經 `seedlab.LibraryCap`／`GameService.libraryCap`。
  - **研究槽擴充**（`seed_labs.extra_slots`；`seedlab.ExtraSlotPrices` 預設 100,000 一個，最多 5 個）：研究 Lv.3 後實驗室主頁出現「研究槽擴充」；研究槽數一律經 `seedlab.SlotsFor`。
  - **🎨 農場裝飾**（`game/decor`，24 種：普通 5,000（原本素材裡沒用到的 11 種）、精緻 20,000、稀有 100,000、節慶 50,000；價格固定在程式裡）：新表 `farm_decorations`，「🌱 農田」→「🎨 庭院擺設」購買與擺放（6 個位置：左上、左中、左下、右上、右中、右下，新買的自動擺到第一個空位）。農田圖有擺裝飾時，左右各加 96 像素的草地（點綴花叢），左邊放 1–3 號、右邊放 4–6 號，上方一律加寵物帶高度的草地，圖片接近正方形（16 格 704×602；`farmimage.FarmPNGWithDecor`，版面在 manifest 的 `decor_sides`）。沒有擺時圖片與之前完全相同。13 種新裝飾由使用者提供的 `農場花園像素裝飾素材表.png` 裁切（背景本來就是透明的，透明度 < 128 的光暈去掉），縮成 64×64（底部對齊）。（同日第一版是畫在農田下方的一條，使用者覺得位置抽象、圖片太長，改成左右兩側。）效能（i5-12400F 實測）：有擺設時每張農田圖多約 6 毫秒（約 18 → 24 毫秒），PNG 約 70 → 120 KB；命中預先產生的農田圖時依「農田圖、寵物、擺設」快取（`decorCache`，最多 256 張，超過清空），第二次起約 0.006 毫秒。
  - 農場狀態多讀一次庭院擺設，面板 SQL 次數上限 145 → 150（`bot/querycount_test.go`）。
  - **Migration 0042**（Python 專案）：`seed_labs.library_level`（0–10）、`seed_labs.extra_slots`（0–5）、新表 `farm_decorations`。`db.AlembicHead` 改為 0042。
  - 管理網站：「種子實驗室」卡片新增擴充格數與兩個價格清單、「自動釣魚與魚簍」卡片新增消耗魚餌與加速、新卡片「加速沙漏」。介紹網站的商店、釣魚與實驗室說明同步更新。
  - 測試：`game/hourglass`、`game/autofish`（加速、舊快照）、`game/seedlab` 的 `TestExpansions`、`bot/farmimage/decor_test.go`、`services/autofish_test.go` 的 `TestAutoFisherBaitAndSpeed`、`services/expansions_test.go`、`bot/panel_expansions_test.go`、`bot/panel_autofish_test.go`。
- **價目表、紀錄分頁與快速生長可加工（2026-11-14，玩家回饋）**：
  - 「📈 作物價格」依單價由高到低排序、每頁 8 種分頁（作物變多後原本超過 Embed 4,000 字，後面的高價作物被截掉）；商店不賣種子的作物不顯示種子價格，改寫「🧬 雜交作物（種子只能在種子實驗室培育）」或「🔒 商店不賣種子（山洞探險、每週特賣、稀有作物培育器）」。
  - 「🧾 紀錄」與實驗室「📜 培育紀錄」改為一次讀最近 100 筆、分頁顯示（每頁 10／15 筆），從別頁進入時回到第 1 頁（`pageSlice`、`navFirstPage`）。
  - 快速生長作物也能當加工原料（`services.CraftMutations` = 無突變 → 巨型 → 快速生長）。
  - 實驗室頁（含未建造、未開放的畫面）新增「特殊種子怎麼來」：鑑定、雜交／改良、收成取回、朋友分享，以及稀有種子（琥珀瓜、月光花）來自山洞、每週特賣與稀有作物培育器。
  - 測試：`bot/panel_go_features_test.go` 的 `TestPricesPagedAndHideUnbuyableSeedPrice`、`bot/panel_social_test.go` 的 `TestLogPageListsRecentTransactions`（翻頁）、`services/go_features_test.go` 的 `TestCraftUsesFastCrops`、`services/seedlab_test.go`（配方隱藏、試驗雜交）、`game/seedlab`（新預設值、缺少 key 的舊快照）。
- **介紹網站補齊 Go 版新增的玩法、數值改由 API 提供、分段讀取（2026-11-14，使用者要求）**：
  - **分段讀取**：新增 `/api/guide/{part}`（`services.GuideParts`：summary、farm、genetics、shop、ranch、craft、fishing、exploration、mushroom、seedlab、ruins、news、steal、gamble、activities、world_boss、pets、special_shop、progress、rankings），每個部分各自快取 60 秒，只查詢這部分需要的資料（`buildGuide` 改成每個頂層欄位一個建立函式，作物、品質設定等共用資料在同一次請求內只查一次）。首頁只讀 `summary`（功能卡片用的名稱與數字，約 1 KB；完整手冊約 50 KB）；手冊每個 `<details data-part="…">` 第一次打開時才讀對應部分（多個欄位共用的部分只讀一次），讀取中與失敗會在欄位上方提示，失敗後重新打開會再試。「全部展開」會讀全部；網址的 `#fishing` 等錨點會直接打開該欄位；每分鐘只重新讀取已經讀過的部分。完整的 `/api/guide` 保留（內容不變，另外多了下面的新欄位）。
  - 頁面上最大的幾張預設表格（成就、等級解鎖、山洞素材、釣具、作物、貓咪加成）改成「打開欄位時讀取最新資料…」，`index.html` 從 100 KB 減到約 88 KB。
  - **新增到手冊的內容**：非當季作物售價 +X% 與商店只賣當季種子（`seasons.off_season_sell_pct`）；所有種子放素材背包（`start.material_bag_capacity`）；加工排隊上限、可當原料的突變與取消加工訂單（新欄位 `craft`）；價目表分頁；種子實驗室的配方隱藏與試驗雜交、特殊種子的來源、基因升級／退化與突變遺傳機率、研究經驗與等級、雜交作物售價倍率（`seedlab` 新增欄位）；偷菜名單只列出有作物的玩家；伺服器共同任務與競賽的種類、計分、獎勵與最長期間（新欄位 `groups`）；圖鑑分類改讀 `services.CodexCategories`（含菇類、特殊種子）；紀錄可翻閱 100 筆；速查表新增「取消加工單」「找新的雜交配方」。
  - **原本寫死在頁面的數字改由 API 填入**：釣魚各稀有度要拉幾下、魚逃走的距離（`fishing.escape_distance`）、狗狗不受限的防守上限（`pets.defense_cap_pct`）、貓咪加成表頭與舉例、每週特賣的種類數／折扣／限購（`special_shop.weekly`）、簽到幾天一輪、季節月份、雜交作物售價倍率；首頁卡片的簽到天數、圖鑑分類。
  - 測試：`site/site_test.go` 的 `TestGuidePartsMatchFullGuide`（每個部分只有列出的欄位、內容和完整手冊相同、沒有的部分 404、沒有資料庫 503）、`TestPageWiringForLazyGuide`（summary 的內容、每個 `data-part` 都存在、`site.js` 會填的每個欄位頁面上都有、新玩法的說明都在）。也以本機 `farmgm_play` 實際啟動網站，用瀏覽器確認首頁只讀 summary、打開欄位才讀、`#fishing` 直接打開、「全部展開」後沒有殘留的讀取提示、主控台沒有錯誤。
- **種子製造機改版、Boss 獎勵登記圖鑑（2026-11-14，玩家回饋）**：
  - 種子製造機原本只能在「🔧 機器」的「⚙️ 使用機器」選單使用，而且只列普通品質、沒有突變的作物（玩家收成大多是優良以上，選單常常是空的）；機器頁的「啟用／停用」只決定能不能手動使用，啟用後不會自動做任何事，玩家以為沒有生效。
  - 新增「🌱 種子製造」頁（`bot/pages_seedmaker.go`），從「🌱 農田」頁（有機器時才顯示）或「🔧 機器」頁進入：列出背包裡可以做成種子的作物（依品質、突變分開，超過 25 種時分頁），選作物後按 1／5／10／全部（持有超過 10 個時）／自訂數量。`Backpack.SeedMaker` 表示已製作機器（和背包的工具清單同一次查詢）。
  - **普通、優良品質的作物都能做，突變不限**（`exploration.SeedMakerQualities`／`SeedMakerMutations`），做出來的一律是普通種子；稀有、傳說品質與稀有作物仍不能做（`SeedMakerRareCrops`）。當天先開放所有品質，之後使用者決定改成只到優良（2026-11-14）。
  - 種子製造機拿掉啟用／停用開關，`MakeSeeds` 不再檢查啟用狀態（舊資料停用的機器也能用）；機器頁的「⚙️ 使用機器」不再列種子製造，`loadMachines` 少讀一次背包。介紹網站的說明同步修改。
  - 世界 Boss 領獎發放的物品登記圖鑑（`codex.Register`，和領取探險相同），Boss 獎勵的素材也會算進山洞素材圖鑑。**之前已經領過的 Boss 素材不會補登記**。
  - 測試：`services/exploration_test.go` 的 `TestSeedMakerTwoSeedsPerCrop`（任何品質、停用的機器也能用）、`services/worldboss_test.go` 的 `TestBossRewardsClaimedOnce`（圖鑑）、`bot/panel_social_test.go` 的 `TestMachinesPageBuildAndUseSeedMaker`、`TestSeedMakerPageFromPlotsAcceptsAnyQuality`。
- **偷菜名單隱藏全是空地的玩家**（`SocialService.StealPlayers`）；2026-11-12 起只有空地與乾枯作物的玩家也不列出（有成長中或成熟的作物才列出）。
- **音樂改寫成 Go（`internal/music`）**：Lavalink v4（REST＋WebSocket，30 秒看門狗自動重連；2026-11-15 起開啟 Session 續接 120 秒，WebSocket 斷線重連時接回原本的播放器，不再中斷播放）。2026-11-15 起預設音量 100%（`MUSIC_VOLUME` 預設由 0.1 改為 1.0）：Lavalink 音量 100 且沒有濾鏡時直接轉送 Opus，不重新編碼、Spotify 連結解析、16 個斜線指令（名稱與參數與 Python 相同）、播放控制按鈕與佇列分頁；各伺服器音量與前綴沿用 `data/music_guild_settings.json`。設定 `LAVALINK_URI`、`LAVALINK_PASSWORD`；`MUSIC_ENABLED` 且沒有 `MUSIC_BOT_TOKEN` 時在農場 Bot 內執行。只支援斜線指令（前綴指令需要 Message Content 特權 Intent）。實機測試：`TestRealLavalink`（讀 `FARMGM_TEST_LAVALINK_URL`／`PASSWORD`）。
- **音樂介面調整（2026-11-16，與 Python 版不同）**：指令錯誤改成只給下指令的人看（join／play 先刪掉公開的「思考中」再私密回覆），加入語音失敗不再顯示英文錯誤原文（原文只記日誌）；`/volume` 一律當百分比（`/volume 2` = 2%，不再把 0～2 當倍率）；控制按鈕加上文字，「停止」要在 10 秒內再按一次才會清空清單；「正在播放」的狀態、音量、循環改放 footer；`/loop` 選項顯示中文；`/skip` 回覆改為「已跳過這首歌」；`/music` 改成私密、精簡，不再列出 Go 版不支援的前綴簡寫。
- **管理網站（2026-11-16）**：
  - 交易類型補上 `seed_lab`（種子實驗室）、`seed_share`（分享種子）的中文名稱。
  - 「調整玩家」新增 Go 版新功能的唯讀檢視（`GET /api/admin/players/{id}/go-features`）：種子實驗室、地下菇房、山洞探險、機器、庭院擺設、魚簍與自動釣魚機。
  - 群組詳情新增古代遺跡與昨天的日報（`GET /api/admin/groups/{id}/extras`）。
  - 這兩個 API 共用 Bot 面板的讀取方法：會先模擬自動化結算（例如自動釣魚機補算、守護者結算），再整筆回滾，不寫入資料庫（`services/admin_features.go` 的 `preview`）。因為用的是可寫入的交易，和其他寫入 API 一樣需要登入。
  - 金幣與物品可以「設為」或「增減」（`gold_delta`、`delta`）。「設為」時附上畫面上的數值（`expect_gold`、`expect_level`、`expect_quantity`），和資料庫不同時拒絕，避免蓋掉玩家這段期間的收支。
  - 調整原因（`reason`，最多 200 字）記在 `admin_actions.details`。網站要求必填，API 為選填。
  - 調整玩家區有未儲存的修改時，切換分頁、換玩家、重新載入前都會先確認。
  - 編輯活動在儲存前列出修改前後的值。
  - 庭院擺設的價格仍然固定在程式裡（見 `game/decor` 的說明），不在「新功能設定」。
- **介紹網站（2026-11-16）**：
  - 說明總覽改版：第 2 列五個入口，其他功能在「更多功能」選單，最近用過的 3 個會變成按鈕。新玩家建立農場後直接到農田頁。
  - 販售頁改成單一選單，選 1 項時選數量、選多項時一起賣出。
  - 山洞可以一鍵「出發：區域名稱」。
  - 庭院擺設的等級與價格改由 `/api/guide/shop` 的 `decor` 帶入。
  - Go 版音樂只有斜線指令：介紹網站一律不顯示前綴簡寫（`MUSIC_PREFIX_COMMANDS` 不再使用），`/prefix` 註明設定會保存但暫時沒有作用。
- **Discord 面板（2026-11-16）**：建立農場的提示改成「農場建立完成：「名稱」！」，因為直接到農田頁後，畫面上沒有農場名稱。另外補上今天早上面板改版後沒有跟著更新的 7 個面板測試和 1 個 discordio 測試（之前沒有測試資料庫，一直被略過）。
- **特殊種子選單分頁（2026-11-16）**：農田頁「勾選特殊種子」以前只列前 25 顆，後面的種子種不到；改成和雜交頁一樣在選單內翻頁（第一項「上一頁」、最後一項「下一頁」，同時勾了種子和翻頁選項時只種種子）。選單內翻頁選項的文字改成「上一頁：翻到第 N 頁（目前第 X/Y 頁）」；雜交頁選好母本後（選單只顯示已選的種子、看不到頁碼），說明最下方提示母本選單共幾頁、怎麼改選其他頁的種子。
- **審查修正（2026-11-16）**：footer 不再顯示名稱的跳脫反斜線（`plainText` 還原 `escapeMD` 的跳脫序列；`escapeMD` 改用日報的 `news.SafeName`，`@` 不會變成提及）；商店、釣具商店、特賣頁保留選取的商品每次重繪都重建，買完後「金幣不足」、可買數量會跟著更新（特賣買到上限時顯示「本週已達限購數量」）；世界 Boss 一鍵捐獻失敗時「返回」保留選取的物品；音樂「停止」確認時間過了按鈕自動改回「停止」；返回類按鈕改成明確標記（`btn.back`，`nav` 依標籤判斷，動作按鈕不受影響）；管理網站的新功能預覽所有區塊共用一筆交易（各區塊 SAVEPOINT、死結時整批重試），預覽中算出的日報不寫入快取；介紹網站 `Music.Prefix` 被註解吃掉的問題。
- **育苗費用改版（2026-11-16，使用者要求）**：每次育苗的費用改成「作物售價 × 一次收成數量（含產量基因加成）× 50%」，無條件進位（例如售價 300、一次收成 3 個 → 450 金幣）；原本依基因總和（300 + 20 ×（總和 − 4）²）。管理網站的設定改成 `clone_fee_pct`（一次收成售價的 %），舊的 `clone_fee_base`、`clone_fee_per_level` 不再使用（正式環境存過的快照沒有新 key，會用預設 50%）。種子庫、育苗頁顯示的費用由服務層依作物計算（`LabSeedView.CloneFee`）；介紹網站改顯示公式與範例。
- **釣魚提示改寫（2026-11-16，與 Python 版不同）**：拉扯中的 💬 提示（`fishing.IntentText`、`DodgeText`）原本只描述魚的狀態，改成用 ⭐ 直接寫出該按的按鈕（例如「⭐ 按「🎣 收線」最划算！」「⬅️⬅️ 魚往【左邊】竄！⭐ 只能按「⬅️ 左壓」，按其他鍵都會被扯！」）。魚竄向一邊時，魚的狀態那行拿掉 ⭐ 建議、改寫「（先處理竄動）」，避免兩行提示互相矛盾。
- **2026-11-17（使用者要求）**：
  - **釣魚拉扯不再逾時（與 Python 版不同）**：以前拉扯中 20 秒（`action_timeout_seconds`）沒操作就算「操作逾時」，互動失敗時魚餌等於白白被吃掉。現在拉扯中不會因為沒操作而失敗（`expire`／`needsExpire`、釣魚中心的「進行中」判斷都拿掉這個條件），畫面不再顯示期限、也不再在期限到時自動更新；重新開啟面板就能接著拉。`action_timeout_seconds` 只剩操作節奏偵測（人機確認）用來推算上一步的時間。單次釣魚最長時間（避免殘留）從 15 分鐘放寬到 60 分鐘。咬鉤後要及時拉竿的反應時間不變。
  - **減少「交互失敗」**：
    - `discordio.Interaction.Show` 尚未回應時，如果要上傳圖片，或互動建立已超過 2 秒，先 defer 再編輯原訊息。直接回應（type 7）必須在 3 秒內連同圖片傳完，正式環境「換頁 plots」曾到 5.6 秒。
    - 按到已失效的元件（Bot 重新啟動、面板閒置超過 15 分鐘）以前完全不回應。現在私密的農場面板會在同一則訊息直接重開（`Commands.ReopenPanel`，釣魚進行中時直接回到釣魚畫面），其他訊息則私密回覆「這個按鈕已經失效了」。
  - **特殊種子依作物篩選**：種子庫、農田頁、雜交頁的種子改成同作物排在一起（`sortSeedsByCrop`）。
    - 種子庫：沒選種子時第 2 列是「🔎 篩選作物」選單（全部／只看某作物，超過 25 項時選單內翻頁）。選了種子後這一列讓給「種下」選單，按「🔎 取消選取／篩選」回來。
    - 農田頁：「🧬」選單照常直接列出全部種子可以勾選；種子有兩種以上作物時，第一項「🔎 篩選作物」把這一列換成作物清單，選了作物只列那種作物的種子（第一項變成「🔎 改選其他作物」）。原本一定要先選作物才能種，使用者反映不能直接種，當天改回。
    - 雜交頁：母本、父本選單同樣在第一項放「🔎 篩選作物」（`Panel.seedSelect`），選了把那個選單換成作物清單，選好作物只列那種作物的種子（選單標題顯示「只看X」）；母本、父本各自篩選。這一頁的元件列已經滿了，所以篩選放在選單裡，不另外佔一列。
  - **加工設施升級（Migration 0043：`farm_buildings.level` 0–3）**：磨坊、烘焙坊、果醬工坊在「🏗️ 建造設施」頁各可升 3 級（`balance.BuildingUpgradePrices` 15,000／50,000／100,000 金），加工時間 -15%／-40%／-60%（`balance.CraftSeconds`，整張訂單的秒數無條件進位）。
    - 選了設施後要再按「確認升級」才扣錢。升級記為 `build` 交易，計入「農場建設」任務與發展點數。
    - 升級後新開的訂單（含自動加工機開的訂單）才縮短，已經排隊的訂單時間不變。加工頁的設施狀態與配方時間依等級顯示。畜舍不能升級。
  - **實驗室限定突變補上說明**：夜光、豐收、水晶作物的效果以前只在介紹網站，遊戲內沒寫。現在「🧬 品質與突變」頁多一段「🔬 實驗室限定突變」，實驗室說明頁也列出效果，種子庫選到有這三種突變的種子時會顯示效果（`labMutationEffect`，數值取自 `balance.GlowPricePct` 等常數）。
  - **每日天氣（Go 版新增，不需要 Migration）**：`internal/game/weather`。每天（台灣時間 0 點換）全服抽一種天氣，只由日期決定（SHA-256("weather-YYYY-MM-DD")，同日報傳聞的做法），不寫資料庫。
    - ☀️ 晴天（35%）：澆水後生長時間 -10%。☁️ 陰天（30%）：沒有效果。🌧️ 雨天（25%）：種下的作物自動澆水，釣魚稀有度 稀有 +3%、史詩 +1%、傳說 +0.3%（從普通扣除，手動釣魚、自動釣魚機與釣魚中心的機率顯示都適用）。⛈️ 暴風（10%）：收成時每格少 1 個（至少留 1 個；使用者要求，原本 2 個），每種守護這格的防守設施（稻草人、警戒犬、防盜圍欄，同種重疊算一次）擋下 1 個，所以有防守設施守護的格子不會損失。
    - 天氣做成虛擬事件（`weather.EventID` = -1，名稱「今日天氣・晴天」等），由 `GameService.activeEvents` 加進生效事件清單，所以走 `events.BonusOf`，與管理員開的活動相加、共用上限，交易紀錄的 `applied_events` 也會記上。防守設施只減輕天氣造成的部分（`weatherLoss`／`plotGuards`）。「現在進行中的活動」與監控網站的活動清單不列天氣。
    - 總覽頁 footer 第一行顯示今日天氣與效果（最底部，緊貼按鈕）。
    - 測試：`svctest.Prepare` 把天氣固定為陰天（`weather.Fixed`），結果不隨測試日期改變；天氣的測試自己指定。
  - **送禮與互助（Go 版新增，Migration 0044）**：用正面互動平衡偷菜。規則與數值在 `internal/game/aid`，可在管理網站「新功能設定 → 🤝 送禮與互助」調整（featurecfg key `aid`）；服務在 `services/aid.go`，面板是「更多功能 → 🤝 送禮互助」（偷菜頁也有入口，`pages_aid.go`）。
    - 🎁 送禮：送同伺服器的玩家一份作物，只能送**普通品質、沒有突變**的作物，一份最多 10 個、總價值（作物基本售價 × 數量）最多 1,500 金（`aid.GiftLimit`；面板直接送一份的上限數量）。對方倉庫放不下時不能送。雙方各得 30 金。
    - 💧 幫忙澆水：把對方所有等待澆水的土地一次澆好（算一次；澆水紀錄寫在對方農場，套用對方的溫室、天氣等加成），雙方各得 20 金。
    - 防止小號洗錢：雙方都要 Lv.5 以上、農場建立滿 3 天；每天送出 1 份、收到最多 2 份；每天最多幫 3 位玩家澆水、最多被 3 位玩家幫忙；同一對玩家同一種互助一天只能一次（台灣日期）。獎勵的交易類型 `aid_gift`／`aid_water` 加進 `ranking.NetGoldExcluded`，不算「淨賺金幣」（Go 版新增，Python 沒有這兩種類型）。
    - Migration 0044：交易類型新增 `aid_gift`、`aid_water`（雙方各一筆，說明寫明對象與內容）；新表 `aid_logs`（kind gift／water、送出與收到的農場、群組、物品與數量、`aid_day` 台灣日期）計算每日上限。降版刪除 `aid_logs`，這兩種交易改記成 `admin_adjust`。
    - 雙方農場依 farm_id 由小到大鎖定（同分享種子），避免互相送禮時死結。
  - 實驗室主頁本來就有「🔄 重新整理」按鈕。研究完成後「領取」按不了的情況，其實是面板閒置超過 15 分鐘後失效，現在會自動重開面板。
- **2026-11-18（移植包 `farmgo_移植包_20261118.zip`，全部是 Go 版新增，Migration 0045）**：整檔覆蓋 42 個檔案、新增 9 個程式檔與 33 張圖片；基準是 2026-11-17 的 `d4b21e5`，套用前逐檔比對過，沒有蓋掉之後的修改。
  - **乳品工坊**（`dairy`，3,000 金，可升級 3 次，`farm_buildings.building_code` 沒有 CHECK，免 Migration）：起司（牛奶 ×3）、奶油（牛奶 ×2）、美乃滋（雞蛋 ×3）。烘焙坊新增蛋糕（麵粉 ×2＋雞蛋 ×2＋牛奶）、草莓蛋糕（蛋糕＋草莓果醬）、寵物零食；果醬工坊新增綜合果醬（草莓 ×2＋葡萄 ×2）；磨坊新增幸運香包（藥草 ×3＋羊毛）。農場圖的建築底圖改成 4 位元（多了乳品工坊），`yard/base_*_XXXX.png` 新增 32 張，原本 3 位元的底圖仍用在沒有乳品工坊時。
  - **限時增益道具**（`balance/boosts.go`、`services/boosts.go`、`bot/pages_boosts.go`，背包「✨ 增益道具」使用，記在 `farm_effects`，效果期間再用會延長）：🧥 雨衣稻草人（300 金，24 小時暴風不吹壞作物）、🚿 灑水器（400 金，24 小時非晴天也有晴天的生長加速）、🧧 幸運香包（2 小時種植突變 +3%、菇房稀有菇 +15 個百分點）、🦴 寵物零食（6 小時寵物效果 ×1.5，最多 100%）。
  - **3 隻新寵物**（Migration 0045 放寬 `farm_pets.pet_code` 的 CHECK；降版刪除這三種寵物）：🐰 兔子（菇房每床收成 +X%）、🐦 小鳥（釣魚含自動釣魚機稀有以上機率提高，`pets.BirdFishBP`）、🦔 刺蝟（清除乾枯作物時救回 X%，`salvageWithered`）。購買與升級沿用 `buy_item`。兔子、小鳥、刺蝟還沒有農場圖素材，暫時不畫。
  - **面板**：總覽待辦區（`services/todo.go`：菇房、實驗室、魚簍的狀態，只讀不鎖）、損益顯示、物品用途查詢（`bot/itemusage.go`）、圖鑑進度、「更多功能」選單分組、一鍵收成並重種（`HarvestAndReplant`）／收齊、離線摘要（`awayActivity`、面板逾時時 `MarkSeen`）。
  - **新手引導成就**（分類 `guide`，5 個：第一次收成、第一筆生意、農場升到 2 級、第一份動物產品、第一件加工品）；總覽 footer 依 `GuideOrder` 提示下一步。老玩家會立刻達成，可領 400 金、肥料 ×3、蚯蚓 ×1。
  - 新測試：`services/boosts_test.go`、`dairy_test.go`、`oneclick_test.go`、`away_test.go`。這次的修改還沒在 Discord 上實機測試過。
- **2026-11-19（使用者要求，Go 版新增）**：
  - **加工品售價表**：加工頁新增「📋 售價表」按鈕（`bot/pages_craftprices.go`，頁面 `craft_prices`），依設施列出所有加工品（含還沒建造的設施）的售價、原料、時間，以及和原料直接賣掉相比的損益 %。
  - **待辦區顯示探險**：總覽待辦區列出探險隊的區域與回來時間；回來後提示「可以領取獎勵」並有「⛰️ 領探險獎勵」按鈕。查詢併進 `TodoSummary` 原本那一條 SQL（`LEFT JOIN exploration_logs`），總覽頁 SQL 次數不變。
  - **釣魚結算畫面**加「🛒 釣具商店」按鈕，結算後可以直接補魚餌。
  - **分享種子每天可收 3 位玩家**：玩家反映送不出特殊種子，原因是對方當天已經收過另一位玩家的分享（每人每天只能收 1 位）。`seedlab` 預設的 `share_receive_senders` 改成 3，正式環境存過的設定也改成 3。分享選單會標出「🚫 今天已收滿 N 位玩家的分享」的對象（`ShareTargets` 多算對方當天收過幾位其他玩家、自己是否已送過；`LabShareTargetView.Full`）。
- **2026-11-19（移植包 `farmgo_移植包_20261119.zip`，全部是 Go 版新增，Migration 0046）**：整檔覆蓋 73 個檔案、新增 51 個（程式、圖片與設計文件）；基準是 2026-11-19 08:38 的版本（已含 `129960c`），套用前逐檔確認 `129960c` 的修改都還在（唯一改掉的是售價表測試的「📈」，因為售價改成浮動）。設計與流程圖見 `金幣回收_外觀與擴充流程圖.md`。
  - **退出伺服器移出群組**（`discordio/members.go`）：收到成員離開事件就移出該伺服器的群組（要 `DISCORD_MEMBERS_INTENT=true` 並在 Developer Portal 開啟 Server Members Intent）；沒開也會在 Bot 啟動時與每天 00:00 比對一次。
  - **好友助力改成拔草**、**加工品浮動售價**（`game/craftprice`、`services/craftprice.go`，每 3 天換價，售價表顯示下次換價）、**兔子／小鳥／刺蝟畫在農田圖上**。
  - **金幣回收第二波**：改名費逐次加價（以前付過 300 金改名的也算次數）；稱號每月限定（可設定販售期間）；季節限定裝飾 12 件；魚簍 Lv.5～10、倉庫後段（最多 600 格）、素材背包擴充（`farms.material_bag_level`）；土地第 17～32 格（第二塊農田畫在農田圖下方，農場等級最高 29）；名牌（排行榜、日報、Boss 排行名字前的 emoji）；農會聲望（捐金幣，1 金 = 1 點）；農場主題（雪地、櫻花、夜景）、寵物帽子與換色；每週系統拍賣（出價先凍結金幣、被超過退回、結標回收）。
  - **種植選單**顯示種下澆水後實際的生長時間（所有加速都算進去）；**種子實驗室**可一次鑑定多顆種子。
  - **可在管理網站調整的設定**新增 4 個 key：`expansion`（改名與擴充）、`cosmetics`（外觀收藏）、`reputation`（農會聲望）、`auction`（每週拍賣）。
  - **Migration 0046**（Python 專案）：`farms` 新增 `badge`、`theme`、`material_bag_level`（0～10）、`reputation`；`farm_pets` 新增 `hat`、`color`；新表 `farm_cosmetics`、`auctions`、`auction_bids`；交易類型新增 `reputation_donate`、`auction_bid`、`auction_refund`、`auction_win`（都加進 `ranking.NetGoldExcluded`）。降版刪除新表與欄位，四種交易改記成 `admin_adjust`；進行中拍賣凍結的金幣不會自動退回。`db.AlembicHead` 改為 0046。
  - 新測試：`services/auction_test.go`、`goldsink_test.go`、`craftprice_test.go`、`crops_grow_test.go`、`expansion_test.go`、`discordio/members_test.go` 等。注意：正式環境若存過「自動釣魚與魚簍」設定，魚簍 Lv.5～10 要到管理網站補上。
- **2026-11-19 晚（移植包 `farmgo_移植包_20261119_perf.zip`，累積包，全部是 Go 版新增，Migration 0047）**：內含上一包＋中英文切換（`_i18n`）＋農田主題改版（`_theme`）＋壓測後的效能修正。套用前逐檔比對：上一包的檔案在這包裡都是同一份或更新的版本，0046 Migration 完全相同。
  - **中英文切換**：新套件 `i18n`（專案根目錄，見目錄結構）。Bot 所有面板有英文版，玩家在「🧭 更多功能」切換（存在 `players.language`），沒選過時跟著 Discord 用戶端語言（繁／簡中 → 繁中，其他 → 英文）。介紹網站右上角 🌐 切換（`?lang=` → Cookie `farmgo_lang` → 瀏覽器語言）。交易紀錄與管理紀錄維持中文。
  - **Migration 0047**（Python 專案）：`players.language`（可為 NULL，CHECK zh-TW／en）。降版刪除欄位，玩家選過的語言消失。`db.AlembicHead` 改為 0047。
  - **農田主題改版**（`farmimage/field_theme.go`、`farm/field/masks/`）：柵欄內側的草、泥土格邊框、柵欄與樁頂都換成主題素材；主題改為雪地、夜景、海灘（100,000）、城堡（200,000），**櫻花移除**（已套用的農場顯示預設外觀，不退費）。舊的 6 張主題素材已刪除。
  - **效能修正**：英文的逐詞補翻譯原本是 2111 組詞的大型正規表示式，英文玩家多時吃滿 CPU（壓測 1000 人只有 191 次／秒），改用 `strings.Replacer`（結果相同）後 1,031 次／秒、P95 1.5 秒；主題農田圖的庭院背景依主題與尺寸快取。新增壓測 `bot/stress_test.go`（`FARMGM_STRESS_TODAY=1`）、`site/stress_test.go`。
  - **套用時修正**：`.dockerignore` 原本只放行 `cmd/`、`internal/`，移植包的 `Dockerfile` 新增的 `COPY i18n ./i18n` 會建置失敗，補上 `!i18n/`。
- **2026-11-19 晚（使用者要求，Go 版新增）**：
  - **特殊種子顯示生長時間**：農田頁的特殊種子選單原本只顯示基因星等，現在和一般作物一樣在說明欄最前面顯示「⏱️ 種下澆水後實際的時間」（`LabSeedView.GrowSeconds`，只有 `PlantableLabSeeds` 會填：所有加速＋生長基因＋種子本身的突變，和澆水時的 `growBoosts.seconds` 同一個算法）。測試 `TestLabSeedGrowTimeMatchesWatering` 確認顯示的時間等於澆水後實際的成熟時間。
  - **上傳伺服器數到 top.gg**（`discordio/topgg.go`）：設定 `TOPGG_TOKEN` 時，Bot 重開（登入）時與每天台灣時間 00:00 各上傳一次所在的伺服器數。先用 v1 `PATCH /api/v1/projects/@me/metrics`（`Bearer`），認證失敗（401／403）再改用 legacy v0 `POST /api/bots/{bot_id}/stats`；失敗隔 1 分鐘重試，最多 3 次。Token 不寫進日誌，沒設定時不執行。
  - **種子實驗室加進總覽的「更多功能」選單**：放在【生產】組的「加工」後面（`moreEntries`、`ui.more.lab`），也會出現在最近用過的入口；農田頁原本的「🧪 種子實驗室」按鈕保留。
- **2026-11-20（移植包 `farmgo_移植包_20261120.zip`，全部是 Go 版新增，Migration 0048）**：新玩法六項，整檔覆蓋 35 個檔案、新增 49 個（程式、圖片與設計文件 `新玩法六項_流程圖.md`）。基準是 2026-11-20 09:07 的版本（已含 `41c39ed`），套用前逐檔確認 `5ee3559`、`41c39ed` 的修改都還在。
  - **天氣奇遇**（`game/weather/luck.go`、`services/weatherluck.go`）：暴風隔天的雷擊種子（必定突變）、乾旱時遺跡出現枯井、毛毛雨菇房加產、彩虹日藏寶圖碎片掉率加倍＋可撿一片；總覽 footer 顯示天氣預報。
  - **藏寶圖碎片**（`game/treasure`、`services/treasure.go`）：探險、釣魚、偷菜、NPC 訂單會掉碎片，集滿 4 片到本週地點挖寶，碎片可以 3 換 1。
  - **NPC 限時訂單**（`game/npcorder`、`services/npcorder.go`、`bot/pages_npc.go`）：6 位客人、3 個訂單欄位，報酬比市價高，可付費換單，連續交貨有獎勵。
  - **蜂箱**（`game/beehive`、`services/beehive.go`、`bot/pages_beehive.go`）：農場 Lv.3 開放，放在一格空地（那一格不能種）；周圍作物突變率提高、產蜂蜜、叮小偷。新物品 `honey`、加工品 `honey_cake`（麵包坊）、`honey_milk`（乳品工坊）。
  - **伺服器委託板**（`game/questboard`、`services/questboard.go`）：沿用群組任務（`quests.created_by = 'questboard:<難度>'`），每週由背景結算自動出三張，完成後全群組 24 小時增益，Lv.3 以下的貢獻加倍。
  - **品評會**（`game/fair`、`services/fair.go`、`bot/pages_fair.go`）：每個伺服器每週一場，評審分數＋人氣投票，前三名分獎池。
  - **Migration 0048**（Python 專案 `0048_fair.py`）：新表 `fair_entries`、`fair_votes`（已加進 `dbtest.GameTables`）；交易類型新增 `fair_entry`、`fair_reward`。降版刪除兩張表、兩種交易改記成 `admin_adjust`；還沒結算的品評會報名費與作品不會自動退回。`db.AlembicHead` 改為 0048。其他五項不需要 Migration（資料存在 `farm_tools.settings`，沿用既有交易類型）。
  - **套用時修正的測試**：`TestQuestBoard` 收成時沒有帶群組上下文，任務不會計分，改用 `InGroupContext`；`TestDairyIsUpgradableAndListsRecipes` 的乳品工坊配方數從 3 改成 4（多了蜂蜜牛奶）。蜂箱叮小偷沒有自動測試。
- **2026-11-20（使用者要求，Go 版新增）**：**丟棄特殊種子**。種子庫多一個「🗑️ 丟棄種子」按鈕（沒選種子時在第 3 列；選了種子時在最後一列，進去會預先勾選那一顆），新頁面 `lab_discard`（`buildLabDiscard`）用複選選單勾選要丟的種子，可以翻頁、篩選作物，勾選跨頁保留，按兩次「確認丟棄」才執行。服務 `DiscardLabSeeds`：一次最多 25 顆、只能丟自己種子庫裡的種子（培育中、已種下的不行，任一顆不符整筆不生效），種子標成 `used`，不退任何東西；記一筆 `seed_lab` 交易（不動金幣）。沒有實驗室也能丟朋友分享的種子。不需要 Migration。測試 `TestLabDiscardSeeds`。
- **2026-11-20（使用者要求，Go 版新增）**：**拆除蜂箱**。蜂箱頁多一個「🪓 拆除蜂箱」按鈕（按兩次確認），服務 `RemoveBeehive`：先結算蜂蜜並全部收進倉庫（記成 `craft_collect`；倉庫放不下全部時不拆，請玩家先清空間），再把 `farm_tools.settings` 清成空的，那一格變回空地。不退費、等級歸零，重新放置要再付放置費。拆除記一筆 `buy_item`（不動金幣）。測試 `TestRemoveBeehive`、`TestBeehiveRemovePanel`。
- **2026-11-21（使用者回報，修正）**：任務競賽頁領完群組任務獎勵後，任務還一直列在「🎁 領取任務獎勵」選單、也還算在「有 N 個任務獎勵可以領」裡（Python 版也是這樣：`PlayerQuests` 沒有查領獎紀錄）。改成 `PlayerQuests` 一次查出玩家的領獎紀錄（`community.PlayerClaims`）填進 `MyClaim`，領過的任務顯示「✅ 已領取：…」、不再列進領取選單。另外蜂箱頁的花類作物說明把「緋月花」改成正確的作物名稱「赤月花」（`crimson_moon_flower`）。測試 `TestClaimedQuestLeavesClaimMenu`。
- **2026-11-21（使用者要求，Go 版新增）**：**販售頁直接顯示加工品的浮動售價**，不用切到加工品售價表。`BuildBackpack` 的加工品帶出本期的總成本、範圍與下次換價時間（`BackpackLine.PriceCost／PriceLow／PriceHigh／PriceUntil`；監控網站的背包 JSON 多 `price_cost`、`price_low`、`price_high`）。販售頁每一項加一行「📈／📉 浮動售價：比成本 N 金 ±X%（範圍 低～高），某時換價」，選單選項的說明加「比成本 ±X%」，選了之後的確認欄位也有同一行。不浮動的商品（菇類、動物產品）不顯示。測試 `TestSellShowsFloatingCraftPrice`。
- **2026-11-21（使用者要求，Go 版新增）**：**水晶作物也能當加工原料**。`CraftMutations` 加上 `crystal`（扣除順序：無突變 → 巨型 → 快速生長 → 水晶），水晶作物的售價和普通的一樣（只在收成時多給水晶），所以和巨型、快速生長同樣處理；仍只收普通品質。NPC 訂單交貨（`orderMutations`）、自動加工機、介紹網站的說明也跟著改。夜光、豐收作物不收。測試 `TestCraftUsesCrystalCrops`。
- **2026-11-21（使用者要求，刪除）**：**刪除天氣奇遇的「⚡ 雷擊種子」**（暴風隔天在第一格空地種下保證突變的作物）。刪掉 `weather.LuckLightning`、`LightningMutations`、`LuckToolCode`、`services/weatherluck.go`（`PickLightningSeed`、`lightningReady`）、`FarmStatus.LightningReady`、`plantForced`（併回 `plantWith`）、農田圖的雷擊點與素材 `field/lightning_spot.png`、總覽待辦與農田種植選單的「撿起雷擊種子」、相關翻譯與測試。暴風隔天不再有奇遇；其他三種奇遇（枯井、毛毛雨、彩虹日）不變。已經種下的雷擊作物照常生長；舊的撿取紀錄（`farm_tools` 的 `weather_luck`）留在資料庫，不再讀取。
- **2026-11-21（使用者要求，調整）**：**蜂蜜改版**。蜂蜜售價 80 → **300**；產蜜改成固定**每 12 小時 2 罐、每個蜂箱最多存 10 罐**，不分等級，天氣與周圍的花類作物都不再影響（刪掉 `beehive.BaseHoney`、`Capacity` 陣列、`WeatherPct`、`FlowerCrops`、`PerInterval` 與 `hiveFlowers`；`Accrue` 不再需要花的格數）。蜂箱等級只影響授粉突變與叮小偷。舊規則下 Lv.2、Lv.3 存超過 10 罐的蜂蜜會保留，收走之前不再增加。總覽只要蜂箱裡有蜂蜜，就有「🍯 收蜂蜜」按鈕，**按一下直接全部收進倉庫**（不用進蜂箱頁；`todoGo.collectHoney`）。蜂蜜蛋糕、蜂蜜牛奶的浮動售價依原料成本計算，會跟著蜂蜜漲價。測試 `TestAccrue`、`TestOverviewCollectsHoney`。
- **2026-11-21（使用者要求，調整）**：**蜂箱最高等級 Lv.3 → Lv.5**。新增兩級（`beehive.MaxLevel = 5`）：升到 Lv.4 花 100,000 金幣（突變 +2.5%、叮小偷 30%），升到 Lv.5 花 200,000 金幣（突變 +3%、叮小偷 35%）；Lv.1～3 不變。產蜜不分等級。農田圖的蜂箱素材只有 3 張，Lv.4、Lv.5 沿用 Lv.3 的圖。測試 `TestBeehive` 升到 Lv.5 並確認不能再升。
- **2026-11-21（使用者要求，Go 版新增）**：**升級價格、擴充格數與道具價格都可以在管理網站調整**，不需要 Migration，預設值和原本寫在程式裡的數值相同。
  - **「改名與擴充」（key `expansion`）多三項**：土地第 5～16 格每格的價格（`land_prices`，預設 `balance.UpgradePrices`）、倉庫 300 格以內每次擴充的價格（`warehouse_prices`，預設 `balance.WarehouseExpansionPrices`）、倉庫每次擴充的格數（`warehouse_step`，預設 50）。清單比級數短時之後都用最後一個價格。原本「倉庫上限要對齊每 50 格」的處理拿掉（擴充時本來就以上限截斷）。素材背包的價格與每級格數本來就在這張卡片。讀取一律經 `expansion.LandPrice`／`WarehousePrice`／`WarehouseStep()`；商店的倉庫說明、介紹網站 `/api/guide` 的 `upgrade_prices`、`warehouse_step`、`warehouse_prices` 都改讀設定。
  - **新設定「設施與道具價格」（key `upgrades`，新套件 `game/upgrades`）**：加工設施升級各級價格與縮短的加工時間 %（最多 3 級，因為 `farm_buildings.level` 的 CHECK 是 0～3）；蜂箱各級價格（第 1 項是放置費）、突變加成、叮小偷機率（萬分比，最多 10 級）、搬家費、產蜜間隔（小時）、每次幾罐、存量上限；所有道具（自動釣魚機、加速沙漏除外，它們有自己的設定）、防守設施、建築的價格。降低等級數時，已超過的設施／蜂箱以新的最高等級計算效果，等級不收回（`beehive.Parse` 接受到 Lv.10）。
  - **程式的改動**：`balance.BuildingUpgradePrices`、`BuildingUpgradeCutPct`、`BuildingMaxLevel`、`CraftSeconds` 移到 `upgrades`（`BuildingUpgradePrice`、`BuildingCutPct`、`BuildingMaxLevel`、`CraftSeconds`）；`beehive.MaxLevel`、`Prices`、`MutationBP`、`StingBP`、`MoveFee`、`IntervalSeconds`、`HoneyPerInterval`、`Capacity` 改成函式。道具價格沿用 `balance.RegisterPrice`；防守設施與建築的價格改經新的 `balance.DefenseOf`／`BuildingOf`（`upgrades` 以 `SetPriceHooks` 登記），原本直接讀 `balance.Defenses[...]`／`Buildings[...]` 的地方都換掉。防守頁的巡查券價格原本讀 `balance.Items`，改成 `ItemOf`。
  - **修正**：管理網站存設定的 `featureKeyRe` 原本是手寫清單，漏了 `expansion`、`cosmetics`、`reputation`、`auction`，這四張卡片按儲存會失敗（參數格式不正確）。改成由 `services.FeatureKeys` 產生。
  - 監控網站：「改名與擴充」卡片多兩個價格清單與倉庫每次格數；新卡片「設施與道具價格」（`upgradesCard`，`FeatureSettingsView` 多 `defenses`、`buildings` 清單）。
  - 測試：`game/upgrades/upgrades_test.go`、`game/beehive` 的 `TestConfigurable`、`game/expansion` 的 `TestEarlyPricesConfigurable`、`web/admin_test.go`（從管理網站存 `upgrades`、`expansion`）。


### 2026-11-15：自動販售機販售項目、季節輪替、商店分類

- **自動販售機販售項目**：「🎒 背包」有自動販售機時多一個複選選單（第 4 列），勾選要自動賣的東西：每種一般作物、「雜交作物（全部）」（不個別列出，避免洩漏還沒發現的配方）、「自動釣魚機的魚」。設定記在 `farm_tools.settings.sell_only`（自動販售機那一列），沒有這個設定或全部勾選時全部都賣；沒勾的作物收成時放進倉庫（倉庫滿了不能收成），沒勾魚時自動釣魚機的魚放進魚簍。改設定前先結算自動釣魚。`services/autoseller.go`（`AutoSeller`、`SetAutoSellerItems`、`autoSells`）。
- **季節輪替**：不再依台灣月份，改成從 2026-11-15 00:00（台灣時間）的冬季開始，每 14 天依序冬 → 春 → 夏 → 秋（`seasons.Epoch`、`RotationDays`、`Rotation`、`NextChange`）。`NowText` 加上下次換季的日期；網站 `seasons` 改成 `rotation_days`、`order`、`next_at`、`next_text`（拿掉 `months`）。
- **商店分類**：農場商店分成「🌰 種子」「🧪 道具」「💎 工具與倉庫」三個分類（第 4 列按鈕），種子每頁 6 種、可以翻頁（第 3 列），選單只列目前分類／頁面的商品；換分類時取消選取的商品。
- **排行榜顯示農場名稱**：個人／伺服器排行、釣魚排行、世界王與競賽的個人貢獻改顯示農場名稱（`players.display_name` 是註冊時的名稱，改農場名稱後不會更新）；後台監控仍顯示玩家名稱。

## 正式環境切換（2026-11-12）

- VPS `/opt/fmgonew` 為本專案（GitHub `kurumicute/farmgo`），在 `/opt/farmgm`（Python 專案）以覆寫檔啟動：
  `docker compose -f docker-compose.yml -f ../fmgonew/deploy/docker-compose.go-web.yml -f ../fmgonew/deploy/docker-compose.go-bot.yml -f ../fmgonew/deploy/docker-compose.redis.yml up -d --wait redis web site bot`
  **之後在 VPS 執行任何 `docker compose` 都要帶這四個 `-f`**，否則會被換回 Python 版。更新 Go 版：`cd /opt/fmgonew && git pull && docker build -t farmgm-go:latest .`，再執行上面的指令。
- `.env` 沿用 VPS 原本的檔案；PostgreSQL 容器與資料卷 `farmgm_farmgm_pgdata` 沒有重建。切換前備份 `/opt/backup/pre_go_switch_20261111_235922.dump`（本機另存 `farmgm\backups\`）。
- 切換後確認：玩家 50、農場 50、Migration 0038，交易紀錄持續增加；web、site、bot、redis 健康，介紹網站對外正常；Bot 同步 18 個全域指令，Lavalink 已連線。
- 改回 Python：只要沒有玩家做過新果醬，在 `/opt/farmgm` 執行 `docker compose up -d --build web site bot`（不加覆寫檔），再 `docker compose stop redis`。

## 目錄結構

| 路徑 | 對應 Python | 說明 |
| --- | --- | --- |
| `cmd/farmgm-web` | `app/main.py` 的 `run()` 與 lifespan | 監控／管理網站的程式進入點：讀設定、啟動檢查、管理員登入、背景結算、優雅關機 |
| `cmd/farmgm-hash-password` | `app/hash_password.py` | 產生 `ADMIN_PASSWORD_HASH`（隱藏輸入；Windows 與 Linux 以標準庫關閉回顯，不新增依賴） |
| `internal/clock` | `app/clock.py` | `Clock` 介面、系統時鐘、測試用 `Manual` 時鐘（起點 2026-01-01 UTC）、`TaipeiTZ` |
| `internal/config` | `app/config.py` | 讀 `.env`＋環境變數（環境變數優先、空值視為未設定、未知設定忽略），只保留農場設定；`String()` 遮蔽機密 |
| `internal/numfmt` | — | 與 Python 一致的千分位、`round()`（四捨六入五成雙）、`//`、`%` |
| `internal/game/*` | `app/game/*` | 17 個規則套件：gameerr、balance、rules、featurecfg、genetics、events、seasons、checkin、pets、specialshop、achievements、exploration、ruins、fishing、worldboss、news、ranking |
| `internal/db` | `app/db/session.py`、`txcache.py`、`catalog_cache.py`、`check.py` | pgx 連線池、`Tx`（對應 Session，支援 SAVEPOINT）、一鍵操作的快取與延後寫入、讀取快取、目錄快取、Migration 版本檢查 |
| `internal/db/models` | `app/db/models.py` | 60 張表的 struct（`db` tag），約束與 server_default 寫在註解 |
| `internal/repositories/*` | `app/repositories/*` | 29 個 repository；`repotest` 是測試專用夾具 |
| `internal/services` | `app/services/game_service.py`、`ranch_service.py`、`exploration_service.py`、`fishing_service.py`、`social_service.py`、`community_service.py`、`ranking_service.py`、`world_boss_service.py`、`news_service.py`、`progress_service.py`、`admin_service.py`、`progress_admin.py`、`activity.py`、`dto.py`、`monitor_service.py` | GameService（玩家、農場、群組轉移、種植／收成／一鍵操作、販售、擴建、賭場、道具、背包、品質設定）、RanchService（建築、動物、加工訂單、商品販售、寵物）、ExplorationService（山洞探險、古代遺跡修復與守護者、機器、假幣、種子製造機、培育器、自動收割／加工）、FishingService（釣具商店與裝備、拋竿／拉竿／拉扯、防腳本、魚簍、販售、分享、圖鑑、排行榜）、SocialService（群組、Discord 伺服器與帳號對應、偷菜、防守設施、巡查券、傳聞挖寶、個人排行榜）、CommunityService（群組任務、群組競賽的建立／取消／結算與領獎）、RankingService（群組總排行、期間排行、排名獎勵規則與每日／每週／每月結算、背景結算鎖）、WorldBossService（世界 Boss 查看、捐獻、排行、領獎、背景推進與排程產生、管理員建立／修改／暫停／結束、固定週期排程）、NewsService（農場日報與快取）、ProgressService（每日簽到、圖鑑、成就與稱號、歡迎回來摘要、溫室／每週特賣／稱號商店）、AdminService（作物／動物種類、魚種與釣具、釣魚設定、限時事件、新功能規則設定，以及調整單一玩家的金幣、等級、背包、土地、作物、建築、動物、所屬群組、溫室、寵物、稱號、簽到、成就、圖鑑、每週特賣）、任務與競賽計分、DTO；MonitorService（監控網站的唯讀查詢）、`Retrier`（死結重試）、`OrderedMap`（保留順序的 JSON 物件） |
| `internal/dbtest` | `tests/conftest.py`（部分） | 測試資料庫連線與每個測試前的資料清空（依外鍵順序 DELETE、序號歸 1）；`dbtest.SQL` SQL 計數器 |
| `internal/services/svctest` | `tests/conftest.py`（部分） | 服務層與網站測試共用的夾具：基準品質設定、測試用作物生長時間、目錄重設 |
| `internal/auth` | `app/auth.py` | 管理員密碼雜湊（scrypt，與 Python 雜湊互通）、記憶體 session、登入失敗鎖定 |
| `internal/site` | `app/site/server.py` | 介紹網站：彙總統計、操作手冊資料（`/api/guide`）、結果快取、靜態檔（`static` 是 Python `app/site/static` 的原樣複製，以 `go:embed` 打包，帶 ETag） |
| `cmd/farmgm-site` | `app/site/__main__.py` | 介紹網站的程式進入點（預設 Port 24444） |
| `internal/bot` | `app/discord_bot/runner.py`、`commands.py`、`panels.py` | Discord Bot：`PlayerMessage`（錯誤 → 玩家訊息）、`Runner`（同時處理中的工作上限）與 `Call`（每個服務呼叫的死結重試）、`Commands`（/farm、/help）、`Panel`（面板框架 `panel.go`、顯示工具 `format.go`、頁面 `pages_*.go`） |
| `internal/bot/ui`、`ui/uitest` | discord.py 的 Embed、View、Button、Select、Modal、Interaction | 互動抽象層：面板只依賴它，正式環境由 discordgo 轉接、測試用 `uitest` 模擬（Python FakeInteraction） |
| `internal/bot/latency`、`internal/bot/render` | `latency.py`、`render.py` | 面板延遲統計；文字訊息分段（程式碼區塊、2000 字限制、太長改附件） |
| `internal/bot/farmimage` | `app/discord_bot/farm_image.py`、`farm/` | 農田、建築畫面與固定場景的 PNG；`farm/` 是 Python 素材的原樣複製（不含說明檔），以 `go:embed all:farm` 打包（約 21 MB，Bot 的執行檔會變大）；`pillow.go` 移植 Pillow 的合成與縮放，`quantize.go` 移植 FASTOCTREE 減色 |
| `internal/bot/discordio` | `app/discord_bot/bot.py` 與 discord.py | discordgo 轉接層：`ui.Interaction` 的實作、訊息與元件的 JSON、Bot 本體（連線、同步指令、伺服器對應群組、互動分派、背景結算與延遲彙總） |
| `internal/cachebus` | （Go 版新增） | 選用的 Redis Pub/Sub：各程序同步清空目錄與規則設定的快取 |
| `i18n` | （Go 版新增） | 中英文切換：`T`／`Tc`（以 key 取字串）、`Text`（以中文原文查 `en.text.json`）、`Fallback`（送出前用 `strings.Replacer` 逐詞補翻譯）、`Resolve`（玩家選的語言 → Discord 用戶端語言）；語言經 `ctx`（`WithLang`／`LangOf`）傳到服務層。JSON 以 `go:embed` 打包；放在專案根目錄方便直接編輯，所以 `.dockerignore`、`Dockerfile` 都要列出 `i18n/` |
| `cmd/farmgm-bot` | `app/discord_bot/__main__.py` | Discord Bot 的程式進入點（只有農場，不含音樂） |
| `Dockerfile`、`deploy/` | Python `Dockerfile`、`docker-compose.yml` | Go 版映像與分階段上線的 compose 覆寫檔 |
| `internal/web` | `app/main.py`、`app/api/monitor.py`、`app/api/auth.py`、`app/api/admin.py` | 網站：Host 檢查、安全標頭、監控 API、WebSocket、登入、管理 API 與本文驗證（`schema.go`）（`static/monitor` 是 Python `app/web` 的原樣複製，以 `go:embed` 打包） |

## 設計原則

- **資料庫 schema 不變**：與 Python 版共用同一個 PostgreSQL。Migration 仍由 Python 專案的 Alembic 管理。Go 只檢查 `alembic_version` 是否等於 `db.AlembicHead`（0038），不會建表或改結構。Python 新增 Migration 時，Go 要同步修改。2026-11-13 起為 0039（種子實驗室，Go 版新增的 Migration），2026-11-15 起為 0040（種子實驗室育苗），同日再到 0041（自動釣魚機與魚簍升級）、0042（實驗室擴充與農場裝飾），2026-11-17 起為 0043（加工設施升級），同日再到 0044（送禮與互助），2026-11-18 起為 0045（兔子、小鳥、刺蝟），2026-11-19 起為 0046（金幣回收第二波），同日再到 0047（玩家介面語言），2026-11-20 起為 0048（品評會）。
- **逐行對照 Python**：
  - 錯誤訊息、常數、繁體中文註解照搬。
  - 數值運算用 `numfmt` 保持與 Python 相同的結果。
  - Python 的 `None` 以 nil 指標或 `""` 表示，在各函式的註解中說明。
  - dict 順序有意義時，用 `*Order` 切片保留。
- **SQL 一律參數化**：欄位名稱只能是程式常數（`db.UpdateByID` 等工具會檢查識別名稱）。
- **機密不寫入程式或日誌**：連線失敗只回報錯誤類型，不顯示連線字串。

## 與 Python 版行為不同之處（重要）

1. **沒有 ORM autoflush**：Python 修改物件屬性後靠 flush 寫入的地方，Go 一律寫明確的 UPDATE／INSERT。第 3 階段移植服務層時，這是最容易漏掉的地方。
2. **時間**：
   - `timestamptz` 一律讀回 UTC，比較時要用 `Equal`，不要用 `==`。
   - DATE 欄位讀回後用 `db.TaipeiDate` 轉成台灣日期，寫入時用 `db.DateParam`。
   - game 套件中的日期一律是「台灣時間 00:00 的 `time.Time`」。
3. **釣魚 Session 的 `state`**：資料層保留原始 JSON（`json.RawMessage`），未知欄位不會遺失。規則套件的 `fishing.State` 是固定欄位的 struct，服務層寫回時要注意不要丟失 Python 另外存的欄位。2026-11-10 拉扯改版新增的 `start_distance`、`need`、`progress`、`dodge_at`、`slack` 已加入；改版前的舊 Session（沒有 `need`）在 `Step` 開始時補上（Python `_upgrade_legacy`），寫回時 `dodge_at` 是 `[]`。
4. **讀取快取回傳的切片**：與快取共用，呼叫端不可修改。Python 回傳的是複本。
5. **map 沒有順序**：
   - 簽到獎勵文字同一天有多種道具時，改依道具商店的順序排列。
   - 成就、世界 Boss 設定同時有多個錯誤時，第一個回報的錯誤可能和 Python 不同。
6. **農場日報的亂數**：另外實作了與 CPython 相同的 Mersenne Twister。日報文字與傳聞結果已和 Python 實際輸出逐字比對，結果一致（`internal/game/news/testdata/python_reference.json`）。
7. **釣魚、山洞的測試亂數**：測試使用 Go 的亂數，序列與 Python 不同，但斷言與統計門檻相同。

## 測試

```powershell
go vet ./...
go test -p 1 ./...     # -short 會縮減釣魚模擬次數
```

- **最後一次實際結果**（2026-11-11）：
  - `go test -p 1 -count=1 ./...` 共 **691 個測試全部通過**，0 失敗、0 略過（`-short` 也通過；`go vet` 在 Windows 與 GOOS=linux 都無問題）。資料庫測試是對 PostgreSQL 16、schema 0038 執行的。各套件與對應的 Python 測試：
    - **game、config、db、repositories 等（第 1、2 階段）**：Python 不需要資料庫的純規則測試、資料層冒煙測試，以及 `test_database.py` 的啟動檢查 2 個（含連線失敗不洩漏密碼）。
    - **services 339 個（第 3 階段；另含 0038 同步新增的 2 個：`test_ranch.py` 的 test_craft_queue_limit_and_can_make、`test_items.py` 的 test_earthworm_speeds_up_crops_already_growing）**：
      - 服務：`test_game_service.py` 22、`test_ranch.py` 22（另 1 個監控 API 在 web）、`test_pets.py` 3＋狗擋偷菜 1、`test_exploration.py` 24、`test_ruins.py` 4、`test_fishing.py` 20、`test_fishing_guard.py` 6、`test_social.py` 19、`test_defense.py` 13（另 1 個監控 API 在 web）、`test_home_group.py` 7、`test_group_transfer.py` 8＋群組排行 1、`test_luxury_items.py` 9、`test_community.py` 14、`test_ranking.py` 7、`test_world_boss.py` 28、`test_news.py` 4、`test_checkin.py` 4、`test_achievements.py` 5、`test_codex.py` 4、`test_feedback.py` 5、`test_special_shop.py` 4、`test_admin.py` 15、`test_progress_admin.py` 11。
      - 收尾：`test_bulk_actions.py` 5、`test_gamble.py` 5、`test_batch.py` 4＋`test_discord_bot.py` 的 RetryingService 1、`test_farm_stats.py` 4（排行比對 12 組子測試）、`test_items.py` 7、`test_genetics.py` 13、`test_new_catalog.py` 4、`test_seasons.py` 2、`test_database.py` 4（約束 8 組子測試）、`test_perf_batching.py` 10、監控唯讀交易 1。
      - 管理功能改寫成服務層測試：`test_fishing_api.py` 2、`test_seasons.py` 1、`test_new_catalog.py` 1。另有 Go 版自己的測試約 15 個（RunBatch、`updated_at`、Python 數值行為、重試等）。
    - **auth 6 個（第 4 階段）**：`test_auth.py` 單元測試 4 個、與 Python 雜湊互通 1 個、成功登入清除失敗次數 1 個。
    - **web 35 個（第 4 階段）**：`test_monitor_api.py` 18 個、`test_auth.py` 的 API 測試 4 個、`test_ranch.py`／`test_defense.py` 的監控 API 各 1 個、Go 版的登入本文驗證、管理 API 驗證順序、監控端點形狀與鍵順序各 1 個；監控第三批 8 個（`monitor3_test.go`）：`test_fishing_api.py` 的監控測試 2 個、`test_ranking.py` test_ranking_api 的監控部分 1 個、`test_world_boss.py` 的 test_monitor_projection_is_read_only 與 test_admin_website_manages_boss／test_admin_website_schedules 的監控部分共 3 個（活動、排程、排名規則改經服務建立），以及 Go 版的釣魚目錄鍵順序、只接受 GET 與路徑參數驗證 2 個。
    - **web 管理 API 9 個（`admin_test.go`）**：`test_admin.py` 的 test_admin_api_end_to_end、test_admin_transfer_group_api，`test_community.py` 的 test_admin_api_and_monitor，`test_genetics.py` 的 test_genetics_web_api，`test_fishing_api.py` 的 test_admin_writes_require_login_and_header、test_admin_gear_and_settings，`test_progress_admin.py` 的 test_api_requires_login_and_works；Go 版的本文型別驗證（22 種錯誤都回 422 且不寫管理紀錄）與日期時間解析各 1 個。
    - **web 管理 API 第三批 4 個（`admin_boss_test.go`）**：`test_ranking.py` 的 test_ranking_api、`test_world_boss.py` 的 test_admin_website_manages_boss、test_admin_website_schedules（都是完整移植，含管理 API），以及 Go 版的巢狀本文（設定、獎勵）驗證與 date 欄位格式 1 個。web 合計 48 個。
    - **game/worldboss 新增 1 個**：`DefaultItemOrder` 與預設設定的指定物品一致。
    - **site 19 個（`internal/site`）**：`test_site.py` 全部 16 個、`test_world_boss.py` 的 test_site_shows_world_boss_guide、test_site_guide_follows_live_global_boss，以及 Go 版的靜態檔 ETag／304／404／405 1 個。另外以 Python `build_guide` 實際輸出比對 Go 的 `/api/guide`（4 種資料庫狀態），所有值都相同（比對腳本只在當時使用，沒有放進專案）。
    - **cmd/farmgm-site 2 個**：設定轉換（支援連結預設官方群組、只有設定 MUSIC_BOT_TOKEN 才有音樂 Bot 邀請連結），以及以測試資料庫實際啟動與關閉。config 新增 1 個（音樂設定的驗證，Token 不保存）。
    - **game/fishing 24 個**：依 Python 新版 `test_fishing_rules.py` 改寫（拉幾下、只按收線的腳本釣不到、強制竄動、連續收線、等待與放線、最後一下、竄動、舊 Session、結果）；移除 Python 已刪除的舊規則測試。
    - **bot 12 個（第 5 階段第 1 批）**：`test_latency.py` 的 test_percentile_and_summary、`test_discord_bot.py` 的 test_retrying_service_only_retries_deadlocks（含 player_message）、test_database_outage_reports_safe_message（以連不上的資料庫確認訊息）、test_long_output_is_split_or_attached 的分段部分；Go 版：`ui` 5 個（列與元件上限、custom_id 重新綁定與舊 ID 失效、擁有者檢查、同一面板的回呼依序執行與自動 defer、對話框只能送出一次、逾時與錯誤處理）、並行上限、訊息跳脫與分段。`ui` 另外在 Linux 容器中以 `-race` 跑過，沒有資料競爭（Windows 沒有 C 編譯器，無法用 -race）。
    - **bot/farmimage 17 個（第 5 階段第 2 批）**：`test_farm_image.py` 全部 14 個；Go 版：拼好的畫面與 Python 逐像素相同（6 種畫面：混合作物、預先產生圖補標記、寵物草地、16 格、變種與新作物、建築畫面含動物與泡泡；以 Python 實際輸出的 SHA-256 比對）、減色後每個像素的顏色與 Pillow 相同（4 種）、`PlotsFromStatus`。
    - **bot 面板 20 個（第 5 階段第 3 批，`panel_test.go`）**：`test_discord_panel.py` 的 test_default_registration_is_short、test_welcome_in_dm_and_named_creation、test_open_panel_expired_interaction_is_ignored、test_rename_farm_from_help_page、test_only_owner_can_use_panel、test_timeout_disables_panel、test_large_farm_fits_discord_limits（農田、總覽）、test_overview_lists_crops_and_times、test_pages_attach_images（已移植的頁面）、test_fast_pages_reply_directly_without_defer、test_slow_page_defers_first、test_slow_action_shares_reply_budget、test_unchanged_image_reuses_existing_attachment（以總覽／農田的同一張圖改寫）、test_open_panel_sends_farm_image、test_missing_assets_fall_back_to_text、test_upgrade_button_disabled_at_max_plots、test_upgrade_adds_one_plot…（登記順序的部分改為檢查送出時已登記）、test_help_has_official_guild_link、test_farming_loop_with_buttons（第 4 批補上販售部分）；Go 版：背包的假幣兌換（數量按鈕、超過持有的按鈕不顯示、自訂數量的範圍檢查、全形數字）。測試用的玩家操作工具（`player`：click、choose、modal、goPage）對應 Python 的 Player。
    - **bot 面板第 4 批 8 個（`panel_shop_test.go`）**：`test_discord_panel.py` 的 test_shop_quantity_picker_and_custom_amount、test_sell_picker_and_errors_show_inline、test_sell_ticked_items、test_sell_ticked_sells_shown_quantity_and_reports_failures、test_shop_keeps_selection_and_shows_holdings、test_shop_single_select_and_readable_prices、test_shop_luxury_confirm_and_warehouse，以及 `test_latency.py` 的 test_panel_records_open_goto_and_act；另外 test_farming_loop_with_buttons 補上販售部分、test_pages_attach_images 加上商店頁，`ui` 的元件測試加上複選選單。另以 Python 實際輸出比對同一情境 18 個步驟（商店、購買、奢侈品確認、倉庫擴大、價格、品質、販售、單項／勾選／全部賣出），畫面文字、按鈕與選單（標籤、emoji、列、停用、樣式、選項與預設值、複選上下限）全部相同（比對腳本只在當時使用，沒有放進專案）。
    - **bot 面板第 5 批 8 個（`panel_ranch_test.go`）**：`test_discord_panel.py` 的 test_animals_and_craft_pages、test_craft_buttons_follow_ingredients_and_queue、test_casino_bet_from_panel、test_casino_loss_and_overview_events 的賭場部分（活動顯示已由總覽測試涵蓋；另加自訂下注數量與數量自動降到持有數），`test_pets.py` 的 test_panel_adopt_and_upgrade；Go 版：收集全部動物、領取完成的加工品與原料不足提示、防守頁（購買設施與巡查券、調整守護範圍的對話框、防守紀錄與巡查的私密回覆，Python 只有 CMD 測試）。test_pages_attach_images 加上動物、加工、建造、賭場頁。
    - **bot 面板第 6 批 13 個（`panel_social_test.go`）**：`test_discord_panel.py` 的 test_every_page_renders（已移植的 25 頁；釣魚、進度、特賣、遺跡等子面板隨第 7 批加入）、test_steal_from_panel_only_same_guild、test_steal_by_choosing_player_and_plot、test_leaderboard_switches（另加排名獎勵頁）、test_group_transfer_from_panel、test_error_screen_hides_page_and_returns_with_selection、test_cave_entry_areas_and_explore、test_cave_other_area_blocked_while_exploring、test_cave_double_click_starts_once（改為同一按鈕連送兩次，見下方差異）、test_cave_players_are_independent（別人不能操作面板已由 test_only_owner_can_use_panel 涵蓋）、test_machines_page_build_and_fake_coin_exchange 的機器部分（假幣兌換已由背包測試涵蓋；另加停用／啟用機器）；Go 版：任務競賽頁（沒有任務、沒有已結算競賽、沒選競賽的錯誤畫面）、操作紀錄頁。
    - **bot 面板第 7 批 25 個（`panel_progress_test.go`、`panel_boss_test.go`、`panel_fishing_test.go`）**：`test_special_shop.py` 的 test_panel_special_shop；`test_achievements.py` 的 test_panel_claim_and_choose_title（另加改回不顯示稱號）；`test_checkin.py` 的 test_panel_auto_checkin_once；`test_feedback.py` 的 test_panel_shows_codex_hint_in_gold；`test_discord_panel.py` 的 test_ruins_page_donate_with_buttons、test_ruins_guardian_attack_from_panel；`test_world_boss.py` 的 test_panel_donate_flow_and_double_click、test_panel_other_guild_cannot_see_event、test_panel_boss_image_only_in_intro_area、test_panel_concurrent_double_click_no_error、test_panel_fish_multi_select_donates_all；`test_discord_fishing.py` 全部 11 個（test_fishing_pages_render_within_component_limits 合成一個測試跑 5 頁）；`test_fishing_guard.py` 的 test_panel_shows_check_and_fixed_fight_buttons；Go 版：圖鑑與成就分類切換、小數顯示（`pyFloat`；2026-11-16 起整數不帶「.0」）、釣魚畫面計時器到期時實際自動更新。test_every_page_renders 改為 Python PAGES 全部 36 頁（`TestEveryPageRenders`）。`internal/bot/...` 另外在 Linux 容器（golang:1.27）以 `-race` 跑過，沒有資料競爭。
    - **bot 第 8 批 14 個（`internal/bot/discordio`、`cmd/farmgm-bot`）**：`test_music.py` 的 test_intents_and_music_toggle 中農場的部分（只有 Guilds Intent）、test_slow_interaction_delivery_is_logged；Go 版：訊息轉換（只有 emoji 的按鈕不送 label、選項沒有說明時不送、複選上下限、連結按鈕、不 @ 任何人、私密旗標）、附件三種情況（沿用、取代、移除）、元件互動的直接回應／編輯原訊息／followup 與編輯、指令與元件的 defer 類型、對話框（custom_id、欄位、依序取回輸入值）、指令同步四種情況與 403 的說明、新加入伺服器的判斷、登入失敗訊息不含細節，以及**從 `/farm` 到點擊「建立農場」再到 `/help` 的端對端測試**（本機假 Discord API＋真的面板與測試資料庫；舊按鈕再按不回應）；`cmd/farmgm-bot` 沒有 Token 時結束、資料庫連不上時不洩漏密碼與 Token。`discordio` 也在 Linux 容器以 `-race` 跑過。
    - **Go 版新增功能 5 個**：`services/jam_test.go` 2 個（四種新果醬都能以名稱或別名在果醬工坊加工、排隊、領取、依售價賣出；世界 Boss 預設設定收新果醬）、`internal/cachebus` 3 個（沒有 Redis 時照常運作且訊息不含密碼、其他程序的通知清空對應快取而自己的不重複處理、管理員修改目錄與規則設定提交後才發布通知，失敗的修改不發布）。
    - **db 新增 1 個**：連線池耗盡時 `InTx` 在 `PoolTimeout` 後回傳 `db.ErrPoolTimeout`（見下方「已修正的問題」）。
    - **cmd 9 個（程式進入點；Python 沒有對應測試）**：`farmgm-web` 6 個（背景結算的重試與日誌不含錯誤內容、等待中可立即停止、管理員設定 fail closed 與 session 時間、伺服器逾時設定、秒數轉換，以及以測試資料庫實際啟動：監控 API、WebSocket、收到結束訊號後關閉 WebSocket 並正常結束）；`farmgm-hash-password` 3 個（產生的雜湊可驗證、太短／不一致／讀取失敗、以字元計算長度、去除 BOM 與換行）。
  - `go vet`、`gofmt` 無問題。
  - Go 檔案都是 UTF-8 無 BOM、LF。
- **尚未移植的 Python 測試**：Discord 面板（`test_discord_*.py`、`test_farm_image.py`、`test_latency.py` 與各檔案的面板段落）在第 5 階段。CMD 與 Migration 檔案的測試不移植。
- **測試資料庫**：需要資料庫的測試讀環境變數 `FARMGM_TEST_DATABASE_URL`。需要 Redis 的測試（`internal/cachebus`）另外讀 `FARMGM_TEST_REDIS_URL`（例如本機容器 `redis://127.0.0.1:6380/0`），沒有設定時略過。資料庫名稱必須以 `_test` 結尾，因為測試會清空遊戲資料表。沒有設定時，這些測試會自動略過。
- **不能同時執行**：所有測試共用同一個資料庫，必須加 `-p 1`，而且同一時間只能有一個 `go test` 程序。同時執行會互相清空資料、甚至卡住鎖。

### 建立測試資料庫（範例）

1. 開一個暫時的 PostgreSQL 容器（隨機密碼，只綁本機）：

   ```bash
   docker run -d --name fmgonew-test-pg -e POSTGRES_USER=farmgm -e POSTGRES_PASSWORD=<隨機> \
     -e POSTGRES_DB=farmgm_test -p 127.0.0.1:5544:5432 postgres:16
   ```

2. 在 Python 專案建立 schema。Python 環境需要 `requirements-dev.txt`：

   ```bash
   DATABASE_URL=postgresql+psycopg://farmgm:<隨機>@127.0.0.1:5544/farmgm_test python -m alembic upgrade head
   ```

3. 在 Go 專案執行測試：

   ```bash
   FARMGM_TEST_DATABASE_URL=postgresql://farmgm:<隨機>@127.0.0.1:5544/farmgm_test go test -p 1 ./...
   ```

4. 用完刪除：`docker rm -f -v fmgonew-test-pg`。

不要對 `farmgm` 專案的 Volume 或正式資料庫執行測試。

## 依賴

- Go 1.23 以上（開發時使用 1.27.0）
- `github.com/jackc/pgx/v5` v5.7.5（其餘為它的間接依賴，見 `go.sum`）
- `github.com/gorilla/websocket` v1.5.3（監控頁 WebSocket；2026-11-10 使用者選定）
- `golang.org/x/crypto` v0.37.0（scrypt 密碼雜湊；原本就是 pgx 的間接依賴，現在改為直接依賴）
- `github.com/bwmarrin/discordgo` v0.29.0（2026-11-11 決定，第 8 批加入；另帶入間接依賴 `golang.org/x/sys`）。只用它的連線（Gateway）、狀態快取與 REST 請求（速率限制、重試）；訊息與元件的 JSON 由 `internal/bot/discordio` 自己產生
- `github.com/redis/go-redis/v9` v9.7.3（選用的 Redis 快取通知；間接依賴 `cespare/xxhash`、`dgryski/go-rendezvous`）

## 下一步

### 第 3 階段（已完成，2026-11-10）

服務層測試已全部移植：`test_bulk_actions.py`、`test_gamble.py`、`test_batch.py`、`test_farm_stats.py`、`test_items.py`、`test_genetics.py`、`test_new_catalog.py`、`test_seasons.py`、`test_rules.py`（已在 game/rules）、`test_database.py`、`test_perf_batching.py`，以及 `RetryingService`（`services.Retrier`）。`test_latency.py` 測的是 `app/discord_bot/latency.py`，移到第 5 階段。

### 第 4 階段（已完成，2026-11-11）

架構：標準庫 `net/http`（Go 1.22 路由）、`gorilla/websocket`；安全機制照搬 Python（見「第 4 階段：網站與 Python 的差異」）。分批：

1. ✅ 網站骨架（Host 檢查、安全標頭、靜態檔、錯誤格式、參數驗證）、監控 API 第一批（summary、farms、farm 明細、transactions、charts、players、groups、group 明細、steals、defenses、leaderboard、backpack）、WebSocket `/ws/monitor` 與共用快照。
2. ✅ 登入（`internal/auth`、`web/auth.go`）、管理 API 防護（`adminRoute`：CSRF 標頭與 Origin → 登入）、本文驗證工具（`web/body.go`），以及測試用到的 3 個管理端點（作物啟用、刪除限時事件、調整背包）。
3. ✅ 監控端點第二批（ranch、craft-orders、quests、quests/{id}、competitions、competitions/{id}、admin-actions、genetics、genetics/stats、catalog、players/{id}/admin-farm）；前端以 `Object.entries` 顯示的物件用 `services.OrderedMap` 保留 Python dict 的順序。
4. ✅ 監控端點第三批（fishing/summary、fishing/players、fishing/players/{id}、fishing/catalog、ranking-rewards、ranking-settlements/{id}、world-boss、world-boss/{id}），監控 API 全部完成。`RuleView`、`SettlementView`、`BossView`、`BossScheduleView` 等補上 json tag；移植 `defaults_view`（`services.BossDefaultsView`）。
5. ✅ 管理 API 第一、二批（50 個端點，`web/admin_catalog.go`、`web/admin_players.go`）：群組任務與競賽、作物品質與生長時間、作物／動物種類、魚種與釣具、釣魚設定、限時事件、新功能規則設定、玩家調整（金幣、等級、群組、土地、作物、建築、動物、溫室、寵物、稱號、簽到、成就、圖鑑、每週特賣）。本文以 `web/schema.go` 宣告式驗證（對應 pydantic model）。
6. ✅ 管理 API 第三批（14 個端點，`web/admin_boss.go`）：排名獎勵規則（新增、立即結算、修改、啟用、刪除）、世界 Boss（新增、修改、設定、獎勵、start／pause／resume／end），以及世界 Boss 排程（新增、修改、啟用、刪除）。`web/schema.go` 加上巢狀 model（`model`、`listOf`、`mapOf`、dict 鍵的規則）與 `date`。管理 API 全部完成，路徑與方法已和 Python 逐一比對（64 個）。
7. ✅ 程式進入點：`cmd/farmgm-web`（讀設定、啟動檢查、依 `BACKGROUND_TASKS_RUNNER` 執行背景結算、優雅關機）、`cmd/farmgm-hash-password`。
8. ✅ 介紹網站（`internal/site`、`services/site.go`、`cmd/farmgm-site`）＋ `test_site.py`。前端 `app/site/static` 原樣複製。

### 與 Python 新版同步（已完成，2026-11-11，Alembic 0036 → 0038）

Python 專案在 Go 移植之後又更新了（2026-11-11 上午重新複製）。比對 `farmgmdiscordb-feature-exploration-update.zip`（0035）與目前版本的差異，扣掉 Go 已經有的部分（0036 防腳本、效能批次寫入、目錄快取），同步以下內容：

- **釣魚拉扯改版（2026-11-10）**：每條魚要成功拉幾下才上岸（`fishing.Pulls`：普通 2–4、稀有 3–5、史詩 4–6、傳說 5–7），每場一定有 `ForcedDodges` 次魚竄向一邊，只有同方向壓竿才算；新常數 `ReelStreakTensionPct` 4、`ReelSpikeMul`、`ReelStreakWaitDecay` 2、`ReleaseSlack`、`WaitTension`；`SmartAction` 改寫；移除 `NextDodge`、`DodgePct`、`LandGateDistance`；`LandStamina` 固定回傳 0（相容舊介面）。釣魚畫面加上 `Need`、`Progress`、`PullsLeft`，結算畫面加上 `BaitLeft`、`Basket`、`BasketCapacity`（Python `bait_left`、`basket`、`basket_capacity`，Discord 面板使用）。
- **Migration 0037、0038**：所有魚價各 ×1.5（.5 進位）。`fishing.InitialFish` 改成 Python 新值（45 種）；`db.AlembicHead` 改為 0038。服務層測試另外檢查資料庫的魚價等於 `InitialFish`。
- **加工排隊**：`balance.CraftQueueMax` 10；設施忙碌時新訂單排在最後一張之後（開始時間 = 前一張的完成時間）；`ranch.ActiveOrders`；工坊狀態加上 `Queued`；配方加上 `CanMake`（現有原料最多能做幾份）。
- **蚯蚓**：啟用時正在生長的作物剩餘時間也 ×0.85（重新啟用時只加速上次效果結束後才澆水的作物）；道具說明文字更新。
- **介紹網站**：手冊的釣魚稀有度加上 `pulls`。
- Discord 面板（`panels.py`、`fishing_panel.py`、`farm_image.py`）與 `scripts/build_crop_sprites.py` 的變更屬於第 5 階段或不移植。第 5 階段要注意：拉扯畫面的按鈕改為固定順序（`test_fishing_guard.py` 的 test_panel_shows_check_and_fixed_fight_buttons，原本是每回合打亂），並顯示「還要成功拉 N 下」（`pulls_left`、`progress`）。
- 之後使用者在專案目錄放了 `farmgmdiscordb-old/`（Python 0036 的舊版，Go 移植時的基準）。用它與目前的 Python 逐檔比對，確認上面就是 0036 之後的全部變更。這個目錄不屬於 Go 專案，附錄不列入，也沒有修改它。
- 本機測試資料庫已用 Python 專案的 `alembic upgrade head` 升級到 0038。**正式資料庫也必須是 0038**，Go 版才會通過啟動檢查。

### 第 5 階段（進行中）

Discord 面板（`app/discord_bot`，約 5,600 行）。函式庫：`github.com/bwmarrin/discordgo`（2026-11-11 決定）。每個服務呼叫經 `bot.Runner`（`services.Retrier`）。對應測試：`test_discord_*.py`、`test_farm_image.py`、`test_latency.py` 與各測試檔的面板段落。

架構：面板與指令只依賴 `internal/bot/ui`（Embed、View、按鈕、下拉選單、對話框、Interaction），與 Python 測試相同以模擬互動測試；discordgo 只出現在轉接層。與 discord.py 的差異：

- 同一面板的回呼依序執行（`ui.Registry` 對每個 View 加鎖）：Go 的處理函式在不同 goroutine，面板狀態不能同時修改；discord.py 在單一事件迴圈中，等待資料庫時其他點擊可以先改頁面狀態。效果相同：連點時依序處理。
- 自動 defer 由 `ui.Registry` 統一處理（元件互動在 1.5 秒內沒有回應就先 defer；等待前一個回呼的時間也算在內），對應 Python 的 `_in_time`。因此面板一律用 `Interaction.Show` 更新畫面（原子判斷直接回應或編輯原訊息）。少見的情況：前一個操作很久時，這次點「自訂數量」已先 defer，無法再開對話框（Discord 限制）。
- 元件的 custom_id 每次重繪都換新，舊訊息上的元件失效（discord.py 相同）。一列超過 5 個元件時 `View.Add` panic `ui.ErrRowFull`（discord.py 的 ValueError），面板重繪時要接住並顯示錯誤畫面。
- **CMD 文字（決定 2026-11-11）**：Python 的 `commands.py`、`panels.py` 重用 CMD（`app/cli/commands.CommandHandler`）的文字輸出。CMD 決定不移植，因此：進階斜線指令（`DISCORD_ADVANCED_COMMANDS`，預設關閉，`.env` 沒有開啟；約 40 個指令與 `/farm-cmd`）**不移植**；面板用到的 4 處 CMD 文字（防守紀錄報告、排名獎勵、競賽排行／狀態、競賽歷史）改寫成 Go 函式，在面板那幾批與 Python 輸出比對。

分批：

1. ✅ 基礎（`internal/bot`）：`ui` 互動抽象層與 `uitest` 模擬、`latency` 延遲統計、`render` 訊息分段、`PlayerMessage`、`Runner`。
2. ✅ 農田圖片（`internal/bot/farmimage`）＋ `test_farm_image.py`。與 Python 的比較見下方「農田圖片與 Python 的差異」。
3. ✅ 指令與面板框架（`commands.go`、`panel.go`、`format.go`、`pages_main.go`）：`/farm`、`/help`；面板的重繪、操作（`act`：操作＋讀取新畫面一次排隊）、錯誤畫面與「返回」、圖片沿用、數量選擇、對話框（數量、取名、改名）、每日簽到／機器／歡迎回來與圖鑑成就提示、逾時、只有擁有者能操作；頁面：歡迎、總覽（含分享到頻道）、農田、背包、說明。`commands.py` 中只有進階指令使用的部分（回覆流程 respond、群組轉移確認 TransferView、文字偷菜、CMD 對應）不移植（見上方 CMD 文字的說明）；`test_discord_bot.py` 大多是進階指令的測試，相同的行為（身分以字串對應、私訊不能建立農場、資料庫中斷的安全訊息、死結重試、長文字分段）已由面板與 runner 的測試涵蓋。
4. ✅ 商店（`pages_shop.go`：種子、道具、倉庫擴大卷、工具與奢侈品的確認購買）、作物價格、品質與突變、販售（單項、勾選多項、全部賣出的二次確認、賭場獎品）＋對應測試。溫室、每週特賣、稱號在「特賣與升級」（`shop_special`，第 6 批的子面板），商店只有換頁按鈕。
5. ✅ 動物（`pages_ranch.go`：餵食、收集全部、建造畜舍、購買、放生的二次確認）、寵物（領養與升級的二次確認、貓的每格多收機率）、加工（份數按鈕依現有原料重算、排隊、領取完成品）、建造設施、防守（購買、守護範圍、巡查報告、防守紀錄）、賭場（選作物、下注數量、比大小、猜數字）＋對應測試。
6. ✅ 偷菜（`pages_social.go`：作物與玩家選單分頁、查看對方農田、偷完留在對方畫面）、排行榜（範圍／項目／期間）與排名獎勵、任務與競賽（領取獎勵、競賽排名、已結算的競賽）、群組（成員、轉移的二次確認），以及 `pages_cave.go` 的山洞（總覽、區域、出發與領取、遺跡立即探索、探索結果、紀錄）、機器（製作、種子製造機、培育器、自動加工設定、停用／啟用）、操作紀錄＋對應測試。主面板的頁面全部完成。
7. ✅ 子面板（`pages_progress.go`：特賣與升級、成就與圖鑑（總覽、成就、稱號、圖鑑）、古代遺跡（捐獻與守護者）；`pages_boss.go`：世界 Boss 介紹區、戰鬥區、捐獻流程（單項與漁獲複選、request_id 防重複）、排行榜與說明；`pages_fishing.go`：釣魚中心、釣魚過程（人機確認、等待、上鉤、拉扯固定按鈕順序與「還要成功拉 N 下」、結算與剩餘資源）、魚簍（單項／勾選／全部出售的確認、分享）、圖鑑、排行榜、釣具商店）＋對應測試。所有面板頁面完成。
8. ✅ Bot 本體與轉接層（`internal/bot/discordio`：`wire.go` 訊息與元件的 JSON、`interaction.go` 的 `ui.Interaction` 實作、`bot.go` 的連線、同步指令、加入伺服器時建立群組、互動分派、送達延遲警告、背景結算與延遲彙總、啟動時清理逾時的釣魚；`cmd/farmgm-bot`；Docker 映像與 `deploy/docker-compose.go-bot.yml`）＋對應測試。
9. **下一步（從這裡繼續）：實機測試**（需要使用者）：準備測試用的 Discord 應用程式（Bot Token）與測試伺服器，在 `.env` 設定 `DISCORD_BOT_TOKEN`、`DISCORD_DEV_GUILD_ID`（指令只同步到測試伺服器、立即生效），連到測試資料庫（不要用正式資料庫）後執行 `go run ./cmd/farmgm-bot`。逐頁操作確認：`/farm` 開面板與建立農場、圖片上傳與沿用、數量對話框、複選選單（販售勾選、魚簍勾選、世界 Boss 漁獲）、只有 emoji 的人機確認按鈕、釣魚畫面的自動更新、分享到頻道（公開、不 @ 任何人）、面板 15 分鐘逾時、舊按鈕、私訊中開面板。確認後再由使用者決定音樂的處理方式與切換時間。

#### Bot 與 Python 的差異（第 8 批）

- **只有農場**：Python 的 `FarmBot` 在沒有 `MUSIC_BOT_TOKEN` 時內建音樂（Lavalink、語音、`/play` 等、前綴指令）；音樂不移植，Go 版 Bot 只註冊 `/farm`、`/help`，Intent 只有 Guilds。沒有設定 `MUSIC_BOT_TOKEN` 而 `MUSIC_ENABLED` 為 true 時啟動會記錄一行警告。
- **訊息格式**：Discord 的 JSON 由 `discordio` 產生（不用 discordgo 的元件型別：它會送空字串的 label 與 description）。所有訊息 `allowed_mentions` 都是不 @ 任何人（Python Bot 的全域設定）；附件以 `attachments` 陣列明確指定（沿用時不送、取代時列出新檔案、移除時送空陣列，與 discord.py 的 `attachments=` 相同）。
- **加入伺服器**：discordgo 在登入時也會對每個已加入的伺服器送 GUILD_CREATE；Go 以登入（READY）時的伺服器清單判斷，登入後才收到的才是新加入（Python `on_guild_join`）。
- **指令同步**：在 READY 之後同步一次（Python 在 setup_hook）；應用程式 ID 優先用 `DISCORD_APPLICATION_ID`，沒有時用 Bot 的使用者 ID。以 PUT 整批覆寫（discord.py `tree.sync` 相同）。
- **找不到元件**（舊訊息、已逾時）：不回應，Discord 顯示互動失敗（discord.py 相同）。
- **面板逾時**：Registry 每 15 秒檢查一次（discord.py 在逾時時間到時立即觸發），停用元件最多晚 15 秒。
- **工作執行緒**：Python 以 `BOT_WORKER_THREADS` 個執行緒執行服務呼叫；Go 的 `Runner` 以同樣的數量限制同時執行的服務呼叫，連線池同樣至少 `BOT_WORKER_THREADS + 2`。

#### 面板與 Python 的差異（第 7 批）

- **釣魚畫面自動更新**：Python 以 asyncio task 在上鉤、反應期限、拉扯逾時的時間點重繪；Go 以 `time.AfterFunc` 排定，到期時經 `Registry.RunFor` 執行，與玩家的點擊依序處理（不會同時改面板狀態）。每次 `refresh` 結束後排定（送出失敗也照常，與 Python 相同），換頁、面板逾時時取消；同一個已過去的期限只自動更新一次。延遲以真實時間計算（Python 也用 `datetime.now`）。自動更新經 `RunFor` 會更新面板的最後使用時間（Python 的 View 逾時只看玩家互動）；計時只在期限到時觸發一次，影響很小。`ui.View` 加上 `Stopped()`（discord.py `is_finished`）。
- **拉扯數值**：Go 的 `FishingView` 以 float64 保存體力、距離、張力；Python 的狀態都是整數，面板以不帶小數點的方式顯示（`num`）。世界 Boss 的 `hp_pct`、`ratio_pct`、`attack_mult`、`uptime_pct` 在 Python 是 float（例如 `100.0`），Go 的 `pyFloat` 原本顯示成相同格式；**2026-11-16 起整數不帶小數點（`100.0` → `100`，與 Python 不同）**，避免玩家看到像程式輸出的「.0」。
- **連點**：世界 Boss 確認捐獻、釣魚操作、山洞出發：Go 第一次點擊完成後舊按鈕失效，第二次不會執行；服務層的 request_id／回合數保護不變。釣魚的 stale 提示另以直接呼叫舊按鈕回呼的方式測試。
- **世界 Boss 捐獻的 request_id**：Python `uuid4().hex`，Go 以 crypto/rand 產生 16 bytes 的十六進位（同樣 32 字元）。
- **分享魚類**：公開訊息；Python 指定 `AllowedMentions.none()`，Go 的 `ui.Message` 沒有這個欄位，第 8 批轉接層要讓 followup 一律不 @ 任何人。

#### 面板與 Python 的差異（第 6 批）

- **CMD 文字**：排名獎勵（`leaderboard rewards`，先結算到期的排名獎勵）、競賽狀態與排名（`competition status／leaderboard`）、競賽歷史（`competition history`）改寫成 `rankingRewardsText`、`competitionStatusText`、`competitionBoardText`、`competitionHistoryText`，格式與 Python CMD 逐字相同（欄寬以字元計算，Go `fmt` 的寬度同樣以字元計算）。Python 競賽頁呼叫兩次 `competition_detail`（狀態、排名各一次），Go 只讀一次再排兩段。CMD 只剩這 4 處加上第 5 批的 2 處，都已改寫完成。
- **排行榜的列**：Python 個人與群組排行都是 dict（以 `"member_count" in r` 區分），Go 轉成同一個 `boardRow`。
- **山洞連點**：Python 以 `self.exploring` 忽略同一面板的後續點擊；Go 的回呼依序執行，第一次完成後畫面已重繪、舊按鈕的 custom_id 失效，第二次點擊不會執行（後端另有一次一趟的保護）。測試以同一個按鈕連送兩次確認只有一筆探索紀錄。
- **探索結果的稀有種子**：Python `seeds` dict 依掉落順序；Go 的 `ExploreReward.Seeds` 是 map，面板依 `Items` 的順序列出。機器缺少的素材同樣依材料清單的順序列出（Go `Missing` 是 map）。
- **偷菜農田圖**：Python 直接把 `StealFarmView` 交給 `farm_png_from_status`（沒有 pets 屬性）；Go 以 `FarmStatus{Plots, Now}` 產生，結果相同（不畫寵物）。
- **競賽頁沒有選擇競賽**：Python `self.detail` 為 None 時 TypeError；Go 顯示「請先在任務競賽頁選擇一個競賽。」（只有直接換頁才會發生）。

#### 面板與 Python 的差異（第 5 批）

- **CMD 文字**：巡查報告（`patrol`）與防守紀錄（`defense log`）改寫成 `patrolText`、`defenseLogText`，格式與 Python CMD 逐字相同（時間為台灣時間「月-日 時:分:秒」）。Python 經 `CommandHandler.execute` 呼叫服務，Go 直接呼叫 `Patrol`、`DefenseLog`（上限 20 筆，同 Python 預設）。
- **選到已不存在的項目**（舊選項）：動物、放生、配方與第 4 批相同，回傳「找不到這個商品／項目。」經 `OnError` 回報（Python 的 `next()` 丟出 StopIteration）。
- **賭場結果不加「✅ 」**：`actOpts.plain`（Python `prefix=""`）。下注的作物與數量（`Panel.bet`）與 Python 相同，換頁時保留。
- **加工份數**：`Picker.Most`（Python picker 的 `most`），每次重繪依最新原料重算 `Max` 與份數按鈕（1、5、最多份數中做得出來的）。

#### 面板與 Python 的差異（第 4 批）

- **複選選單**：`ui.Select` 加上 `MinValues`、`MaxValues` 與 `OnSelectMany`（discord.py `min_values`／`max_values`）；販售頁的「勾選多項一起賣出」使用。轉接層要依這兩個欄位送出。
- **多項販售**：與 Python 相同，每項一筆交易（各自死結重試），遊戲規則錯誤列在「未賣出」，其他錯誤直接顯示錯誤畫面（已賣出的項目不還原）。
- **選到已不存在的商品**（舊選項）：Python `next()` 丟出 StopIteration（由 on_error 回報），Go 回傳「找不到這個商品／項目。」，同樣經 `OnError` 回報。

#### 面板與 Python 的差異（第 3 批）

- **服務呼叫與重試**：Python 的 `RetryingService` 自動包裝每個服務方法；Go 由面板以 `bot.Call` 包住每個服務呼叫（死結重試），整個操作在 `Runner.Do` 的空位中執行（只限制並行數，不整體重試，避免重複已提交的步驟）。讀取整頁的 `RunBatch` 整批重試。
- **頁面**：Python 以 `getattr(self, f"_load_{page}")` 取得頁面；Go 以 `registerPages` 的對照表。還沒移植的頁面點進去會顯示「⚠️ 無法顯示：這個頁面還沒有完成。」與回總覽按鈕（第 5 階段完成後不會再出現）。
- **建立元件超過一列 5 個**：Python 是 ValueError；Go 的 `View.Add` panic，`render` 接住後顯示錯誤畫面（記錄錯誤）。
- **數量輸入**：與 Python `str.isdigit()` 相同接受全形等 Unicode 十進位數字。
- **圖片雜湊**：Python 用 blake2b（16 bytes）判斷是否沿用附件，Go 用 SHA-256 前 16 bytes（只在程序內比較，不影響行為）。
- **`ProgressMarks`** 加上 `CodexOrder`、`ReadyOrder`（Python dict 的順序），「新圖鑑」「成就達成」依相同順序列出。

#### 農田圖片與 Python 的差異（第 2 批）

- **拼圖**：`pillow.go` 移植 Pillow 12.3 的 C 程式（`AlphaComposite.c` 的定點運算、`Resample.c` 的 bicubic／Lanczos 縮放與係數、RGBA↔RGBa 預乘轉換、`FLIP_LEFT_RIGHT`）。以 Python 實際輸出比對 6 種畫面，**拼好的畫面逐像素相同**（測試以 SHA-256 固定下來）。
- **減色**：`quantize.go` 移植 Pillow 12.3 的 `QuantOctree.c`（FASTOCTREE）與 `Quant.c` 的 RGBA 前處理（原始碼取自 GitHub python-pillow/Pillow 12.3.x 分支）。減色後**每個像素的顏色與 Pillow 相同**（9 種情況全部相同）；唯一差異是調色盤的順序：C 的 `qsort` 不穩定，次數相同的格子順序依平台而定，Go 用穩定排序。所以 PNG 位元組不同，解碼後的畫面相同。檔案大小與 Python 相近（Go 的 zlib 最高壓縮略小一點）。
- 一開始用自己的中位切割減色時，64 色農田圖比 Python 大約 55%（102 KB 對 66 KB），而且賭場圖無法比原檔小；改為移植 FASTOCTREE 後兩者都與 Python 相同，因此最後沒有保留中位切割。
- PNG 以 `image/png` 的 `BestCompression` 編碼（Python 的場景圖用 `optimize=True`）。快取規則相同：預先產生的圖、寵物組合、場景圖第一次轉換後快取，之後回傳同一份位元組。
- **效能優化（2026-11-12，Go 版與 Python 不同之處，畫面像素不變）**：
  - 每次重新產生、不快取的動態農田圖與建築畫面改用 `DefaultCompression`（檔案約大 3%）；會快取的圖仍用 `BestCompression`。
  - `memo` 同一張圖同時只產生一次，其他請求等待結果（原本冷啟動時大量玩家同時開同一頁會各自壓縮一次）；失敗不快取，產生時 panic 也會讓等待中的請求收到錯誤。
  - 動態圖片改在組畫面（`build`）時產生，不在讀取資料的交易與 `Runner` 名額內；同時產生圖片的數量以 CPU 核心數為上限（`Commands.images`）。
  - 壓測結果見 `效能比較.md` 的「優化結果」。
- **減少查詢次數（2026-11-12，Go 版與 Python 不同之處，結果相同）**：
  - 一鍵操作中土地的更新（種植、澆水、清除）也延後到整批送出（`farms.plotWrite`），交易紀錄的合併寫入因此生效。
  - 購買後的持有數量（`Picker.After`）與操作後的圖鑑成就比較，改在重新讀取頁面的同一筆交易中、自動機器之前讀取，以 SAVEPOINT 隔離（`GameService.Isolated`，失敗不影響頁面）；`GetFarmStatus`、`GetBackpack` 在共用交易中只讀一次。購買後讀取持有數量失敗時改為省略那一行（Python 會讓整個操作顯示錯誤，但購買其實已完成）。
  - 不加鎖的 `ListPlots`（只用於顯示）改從作物目錄快取取得作物。
  - 每一步的 SQL 次數由 `internal/bot/querycount_test.go` 檢查上限。
- 錯誤：素材缺漏時回傳 error（Python 丟出 OSError），由面板退回純文字。
- 修正過程中發現的錯誤：manifest 的 `barn_slots` 一開始少了 json tag（Go 的 JSON 比對不忽略底線），畜舍數變成 0；已由逐像素比對發現並修正。

### 第 3 階段服務移植順序（已完成）

依 Python 的服務繼承鏈，由下往上分批移植。Go 不用繼承，改成組合多個 service struct：

1. GameService ✅（2026-11-10）
2. RanchService ✅（2026-11-10）
3. ExplorationService ✅（2026-11-10）
4. FishingService ✅（2026-11-10）
5. SocialService ✅（2026-11-10）
6. CommunityService ✅（2026-11-10）
7. RankingService ✅（2026-11-10）
8. WorldBossService ✅（2026-11-10）
9. NewsService ✅（2026-11-10）
10. ProgressService ✅（2026-11-10）
11. AdminService ✅（2026-11-10，含 `progress_admin.py`）

每一批都要同時移植對應的 Python 整合測試，並和 Python 的結果比對。需要特別注意：

- `_record()`：所有資產變動都要寫交易紀錄，`gold_delta` 加總必須等於全服金幣。
- 鎖定順序：例如遺跡列 → 農場列 → 守護者列。
- `RetryingService`：遇到 40P01／40001 時重試。
- 面板讀取整頁的共用交易（`run_batch`）。

## 第 3 階段：服務層與 Python 的差異

- **組合取代繼承**：`services.GameService` 是最底層；之後的 RanchService 等以嵌入 `*GameService` 組合。
- **建構**：`NewGameService(db, Options{Clock, Rng, GeneticsRng, Dice, ExploreRng})`，nil 用預設值（`math/rand/v2`）。`Pick`、`PetRng`、`RumorRng` 是公開欄位，測試可直接指定（對應 Python 的 `_pick`／`_pet_rng`／`_rumor_rng`）。
- **共用交易（run_batch）**：Python 用 thread-local；Go 放在 `context.Context`。`RunBatch(ctx, func(ctx) error)` 內必須用傳入的 ctx 呼叫服務方法。失敗與「吃掉錯誤」的行為和 Python 相同。
- **`in_group_context`**：`InGroupContext(*int64)` 回傳副本，與原服務共用同一個 DB 與共用交易。
- **農場欄位寫入**：Python 改 ORM 屬性後由 flush 寫入；Go 在修改處呼叫 `saveFarm` 立即 UPDATE。`updated_at` 規則與 Python 相同：交易中第一次更新設為時鐘時間，之後設為資料庫 `now()`。群組轉移的觸發器錯誤（完整性錯誤）在 UPDATE 當下轉成 `HomeGroupLocked`。
- **其他 ORM 寫入**：任務進度／完成、賭場獎品數量、蚯蚓效果、工具開關、化肥成熟時間，都改成明確的 UPDATE／INSERT。
- **時間精度**：服務內的「現在」截到微秒，與 Python datetime 及 timestamptz 相同。化肥的 `timedelta * 0.85` 以精確有理數運算再四捨六入五成雙（`mulDuration`）。
- **回傳型別**：Python 回傳 dict 的方法改成 struct（`TransferView`、`GambleResult`、`PrizeView`、`CropCatalogEntry`、`QualityTable`、`MutationRow`、`ActiveEventView`）。`None` 參數改用 nil 指標（例如 `CreateFarm` 的 `homeGroupID`、`Gamble` 的 `guess`）；字串參數的 `""` 表示 None。
- **`IntegrityError`**：以 SQLSTATE 類別 23 判斷（`isIntegrity`）。
- **測試替換點**：`addTransaction`、`addOrder`（套件變數）取代 Python 測試的 `monkeypatch.setattr(tx_repo, "add", …)`、`ranch_repo.add_order`。
- **RanchService**：以 `RanchService{*GameService}` 嵌入；`InGroupContext` 每一層各自提供，回傳同一層的型別。動物、訂單、寵物的狀態變更都改成明確的 UPDATE／DELETE／INSERT。Python 回傳 dict 的 `animal_view`、`order_view` 等改成 `AnimalView`、`OrderView`、`WorkshopStatus`、`AnimalStatus`、`RecipeView`、`PetView`。訂單的 `inputs` 在 Go 是 map（沒有順序），訊息中的原料順序則依配方的 `InputOrder`。
- **ExplorationService**：`exploration_logs.rewards` 是 `json` 欄位（保留鍵順序，Python dict 讀回也是這個順序），Go 的 map 沒有順序，所以寫入改用 `explrepo.AddLogOrdered`（依掉落順序），讀取用 `explrepo.RewardOrders`（`json_each WITH ORDINALITY`），訊息與回傳的物品順序和 Python 相同。`exploration.MaterialBagCapacity` 由常數改成變數（Python 是可變的模組常數，測試會調整）。遺跡、守護者的 ORM 變更改成 `ruins.Save`／`SaveGuardian`。`Explore` 一般區域與古代遺跡回傳不同內容，Go 以 `ExploreOutcome{Trip, Reward}` 表示（兩者只有一個非 nil）。面板共用交易中的 `RunMachines` 用 `Tx.Savepoint`；`autoHarvestHook` 是測試替換點。
- **FishingService**：Session 的 `state` 是 JSON，規則套件的 `fishing.State` 是固定欄位的 struct；服務層寫回時用 `mergeState` 把 struct 欄位覆蓋到原始 JSON，只改 `last` 時用 `withLast`，Python 另外存的未知欄位不會遺失（有測試）。Session 的 `settings`、`gear` 快照原樣保存原始 JSON。拋竿、人機確認、答錯的回傳統一為 `FishingView`（以 `Phase` 區分 waiting／bite／fighting／ended／check／check_failed）；`PolicyState()` 對應 Python 測試的 `{k: view[k] for k in POLICY_KEYS}`。`timedelta(seconds=x)` 以 `secondsDur`（四捨六入五成雙到微秒）換算。`Options.FishingRng`／`FishingService.FishingRng` 是釣魚亂數；防腳本的題目與確認間隔用 `math/rand/v2`，不影響釣魚亂數序列。
- **SocialService**：Python 用 `insert … on_conflict_do_nothing` 的地方（Discord 伺服器對應群組、外部帳號建立玩家）改成同樣的 `INSERT … ON CONFLICT DO NOTHING` SQL。偷菜依農場 ID 順序鎖定雙方，與 Python 相同；剩餘數量、防守設施位置、伺服器改名都改成明確 UPDATE。`steal_players` 等回傳 dict 的方法改成 `StealPlayer`、`DefenseStatus`、`DefenseLogRow`（不含偷菜者欄位）等 struct；`Leaderboard` 回傳 `leaderboard.Row`。測試夾具的群組改用 `CreateGroup` 建立。
- **CommunityService**：任務、競賽、領取紀錄的建立與狀態變更都是明確的 INSERT／UPDATE；建立參數用 `NewQuest`、`NewCompetition` struct（取代 Python 的 keyword 參數）。管理紀錄 details 的時間欄位轉成 Python `str(datetime)` 的格式（`pyDatetime`，例如 `2026-01-01 00:00:00+00:00`），與 Python 寫入的資料一致。Python 檢查 datetime 是否帶時區，Go 的 `time.Time` 一定有時區，這項檢查省略。`addQuestContribution` 是測試替換點。測試夾具的 `setGoldRecorded` 改為經 `record` 寫 admin_adjust，排行統計也會一起更新。
- **RankingService**：結算快照（`ranking`、`rule_snapshot`）的 JSON 欄位名稱與 Python 相同（`leaderboard.Row`／`GroupRow` 的 json tag 已比對）；Go 的 struct 序列化鍵順序與 Python dict 可能不同，但內容一致。管理紀錄的 `period_start` 用 Python `isoformat()` 的格式（`2026-01-05T00:00:00+08:00`）。背景結算的 advisory lock 鍵與 Python 相同（`SettlementLockKey`），兩版同時執行時也不會同時結算；鎖用連線池中獨立的一條連線持有。已結算期間以 `UnixMicro` 比對（資料庫讀回 UTC，期間開始是台灣時間）。
- **WorldBossService**：Python 沒有世界 Boss 的 repository，查詢寫在服務檔案中，Go 也寫在 `services/worldboss.go`（全部參數化）。ORM 修改改成明確的 `saveEvent`、補給進度與目標的 UPDATE。活動的設定與獎勵 JSON 由排程原樣複製（`json.RawMessage`）；驗證後的設定以 struct 序列化，欄位名稱與 Python 相同。獎勵物品在 Go 是 map，顯示文字與發放、交易紀錄的順序依物品代碼排序（Python 依 JSON 中的順序），只影響文字順序。捐獻與領獎的唯一限制用 `Tx.Savepoint` 包住（對應 Python `begin_nested`）。`event_view`、`detail_view`、`schedule_view` 是公開函式 `EventView`、`DetailView`、`ScheduleView`，第 4 階段的監控網站可直接使用；`defaults_view`（管理網站表單）在第 4 階段移植為 `BossDefaultsView`（`services/monitor_more2.go`）。背景結算回傳 `WorldBossSettlement`（排名、競賽之外加上世界 Boss 結束與產生的活動 ID）。管理 API 的輸入用 `BossInput` struct 與 `map[string]any`（排程、設定、獎勵仍由規則套件驗證）。
- **NewsService**：日報快取是套件層級的 map（對應 Python 模組層級 `_cache`），鍵為 (群組, 台灣日期字串)；`ClearNewsCache` 在測試重設資料庫後呼叫。回傳 `*DailyNews`（快取共用，呼叫端不可修改），`day` 為零值表示「台灣時間昨天」。`makeRumor`（套件變數）是測試替換點，日報與偷菜挖寶共用，對應 Python 測試 monkeypatch `news.rumor`。
- **ProgressService**：簽到、成就領取、每週特賣購買紀錄都改成明確 INSERT（每週特賣用與 Python 相同的 `ON CONFLICT … DO UPDATE`）。Python 回傳 dict 的方法改成 `CheckinResult`、`CheckinStatus`、`CodexView`、`AchievementView`、`TitlesView`、`ProgressOverview`、`ProgressMarks`、`WelcomeBack`、`SpecialShopView`；`DailyCheckin` 沒有農場時回傳 nil。獎勵道具在 Go 是 map，發放與顯示順序依道具商店順序（`checkin.SortedItemCodes`），`daily_checkins.items`／`farm_achievements.items` 的 JSON 鍵依字母排序；Python 依 JSON 中的順序。倉庫只放得下部分道具時，改發金幣的道具可能不同（預設獎勵每次只有一種道具，不受影響）。`SetTitle` 稱號沒變時不寫入（與 ORM 屬性值不變時不 UPDATE 相同）。Python 模組載入時檢查「商店稱號與成就稱號不重複」，Go 改為測試 `TestShopTitlesDoNotOverlapAchievementTitles`。`progress_admin.py`（管理員調整）在 AdminService 一起移植。
- **AdminService**：`AdminService{*ProgressService}` 是最上層服務；Python 的 `ProgressAdmin` mixin 改成同一個 struct 的方法（`progress_admin.go`）。
  - 管理網站送來的資料（Python dict）在 Go 是 `map[string]any`，整數接受 `int` 或 `json.Number`（`featurecfg.AsInt`；bool、浮點數不算整數，與 Python `isinstance(int)` 相同）；第 4 階段解析 JSON 時要用 `featurecfg.Decode`（UseNumber）。玩家調整的參數直接用 Go 型別（`SetFarmValues` 的金幣與等級、`SetPetLevel` 的等級用 nil 指標表示 None）。
  - 錯誤訊息的數字格式與 Python 相同：`admin_service._int` 不加千分位（`adminInt`），`progress_admin` 用 `feature_config.int_value` 加千分位（`featurecfg.IntValue`）。
  - 目錄、事件、魚種、釣具的修改都是明確的 INSERT … RETURNING */UPDATE/DELETE；寫入目錄後的快取清除由 `db.Tx` 依 SQL 自動判斷（同 Python catalog_cache 的 `do_orm_execute` 監聽）。`seasons`、`required_baits` 為 nil 時寫入 SQL NULL（pgx 把 nil 切片當成 NULL），有測試從資料庫讀回確認。
  - Python 回傳 dict 的方法改成 struct（`CropView`、`InventoryChange`、`FarmValues`、`AdminTransferResult`、`PlotsAdded`、`PlantedForPlayer`、`BuildingSlot`、`FeatureSettingsView`、`AdminProgressView`）或 bool（是否有變更）；刪除類方法 Python 回傳 `{"deleted": …}`，Go 只回傳 error；限時事件沿用 `events.View` 的 `map[string]any`。
  - 管理紀錄的 details 鍵與 Python 相同：`GearView`、`FishTypeView` 補上 json tag；`last_group_transfer_at` 用 Python `isoformat()` 格式（`isoTime`，已修正為與 Python 一樣有微秒時固定 6 位）。
  - `PlantForPlayer` 的「立即成熟」直接以 1 秒前為種植時間呼叫 `farms.Plant`（Python 先種下再改 `planted_at`，結果相同）。
  - 測試夾具的 `setGoldRecorded` 改為呼叫 `AdminService.SetFarmValues`（與 Python 測試相同）；釣魚設定、釣具與限時活動的測試也改經 AdminService，不再直接寫資料表。
- **尚未移植的 Python 測試段落**：`test_ranch.py` 的排行榜（改為直接檢查 `farm_stats`；監控 API 已在第 4 階段移植）；`test_pets.py` 的狗擋偷菜、農場圖、面板；`test_exploration.py` 的「自動收割機只查一次土地」（以 SQLAlchemy 事件計數 SQL，Go 沒有對應機制，實作上是一次 `ListPlots ... FOR UPDATE`）；`test_fishing_guard.py` 的 Discord 面板；`test_community.py` 的網站管理 API。各檔案中 CMD 的測試段落不移植。釣魚測試以 Go 的亂數執行（序列與 Python 不同），斷言只依賴不受序列影響的性質。對應服務移植時補上。
- **RetryingService**：Python 以 `__getattr__` 自動包裝服務的每個公開方法；Go 沒有動態代理，改成 `services.Retrier`（`Do`／泛型 `Retry`），由第 4、5 階段的呼叫端（網站、Discord 面板）把每個服務呼叫包起來。重試條件（40P01／40001，`db.IsRetryable`）、次數（`RetryAttempts` = 3）、等待（0.05 秒 × 第幾次）與 Python 相同；在 `RunBatch` 共用交易中不重試，由包住整個 `RunBatch` 的外層整批重試。預設等待可被 ctx 取消。
- **不移植的 Migration 測試**：`test_genetics.py` 的降版／升版 Migration 測試、`test_seasons.py` 比對 Python Migration 檔案常數的測試，都屬於 Python 專案（schema 由 Alembic 管理，Go 不讀 Migration 檔案）。Go 以 `TestMigrationInsertedCatalog`、`TestInsertAndQuery` 檢查資料庫內容與 Go 常數一致。
- **挑魚的測試替換點**：`services.pickFish`（套件變數，預設 `fishing.PickFish[models.FishType]`），對應 Python 測試 monkeypatch `fishing.pick_fish`。
- **延後寫入的兩處修正（`test_perf_batching.py` 發現）**：移植前 Go 的一鍵收成每格都把累積的寫入先送出（16 格就各寫 16 次），合併沒有生效。原因與修正：
  - `crops.Get` 每格查一次作物（Python `session.get` 走 identity map，不查資料庫）→ 改成 Bulk 期間以 `db.Memo` 快取（回傳的作物不可修改）。
  - `farms.Clear` 用 `Exec`（先送出延後寫入）→ 改成 `ExecNoFlush`：只改這格土地，不依賴尚未送出的庫存、圖鑑、交易紀錄（Python 只改 ORM 屬性，提交前才寫入）。
  - 修正後交易紀錄、庫存、圖鑑、排行統計與並行偷菜的守恆測試都通過（並行測試重複 5 次）。
- **SQL 計數**：`db.PoolOptions.Tracer`（選用的 pgx `QueryTracer`，正式程式不設定）；測試連線池安裝 `dbtest.SQL`（`Start`／`Stop`／`Count`／`CountContaining`）。一個 `pgx.Batch` 只算一條，對應 SQLAlchemy executemany。Go 的查詢寫法與 SQLAlchemy 不同（例如 `SELECT * FROM crop_types`），目錄快取測試改用 `CountContaining("FROM crop_types")`。目錄快取的 TTL 測試以短 TTL＋等待取代 Python monkeypatch `time.monotonic`。
- **測試計數方式**：`test_batch.py` 以 SQLAlchemy `begin` 事件計算交易數，Go 改用連線池的 `AcquireCount`（每筆交易取得一次連線）。`test_farm_stats.py` monkeypatch `leaderboard._stats`，Go 改用 `leaderboard.ForceTxStatsForTest`（只供測試，正式程式不呼叫）。`test_items.py` 的直接改金幣改用 `env.setGold`（不寫交易紀錄，與 Python 相同）。
- **測試夾具**：`services/helpers_test.go` 對應 conftest 的清空、基準品質設定、測試用作物生長時間；`env.a` 是最上層的 AdminService。

## 第 4 階段：網站與 Python 的差異

- **架構**：`web.New(Options)` 回傳 `http.Handler`；`MonitorService`（`services/monitor.go`）在唯讀交易中查詢，回傳帶 json tag 的 struct，鍵名與 Python dict 相同；空清單輸出 `[]`（`nonNil`）。
- **時間格式**：Go 輸出 RFC 3339（`2026-01-01T00:00:00Z`），Python 是 `isoformat()`（`2026-01-01T00:00:00+00:00`）。前端一律以 `new Date(...)` 解析，兩者結果相同，因此不模仿 Python 的字串。
- **422 錯誤格式**：FastAPI 的參數驗證錯誤回 `{"detail": [ ... ]}` 陣列；前端只在 `detail` 是字串時顯示內容，Go 一律回中文字串（例如「參數 limit 必須在 1–200 之間。」），狀態碼相同。
- **404／405 本文**：未知路徑與不允許的方法由 `ServeMux` 回應（純文字），狀態碼與 FastAPI 相同；前端不依賴這兩者的本文。
- **資料庫錯誤**：服務回傳的非遊戲錯誤一律回 503 `{"detail": "資料庫暫時無法連線，無法取得監控資料。"}`，只記錄錯誤類型（對應 Python 的 SQLAlchemyError 處理）。遊戲錯誤：NotFound → 404、其他 → 400。
- **Host 檢查**：對應 Starlette TrustedHostMiddleware（比對不含 Port 的 Host，支援 `*` 與 `*.example.com`），不符回 400 `Invalid host header`。
- **靜態檔**：以 `go:embed` 打包，Content-Type 由程式指定（不依賴 Windows 登錄檔的 MIME 設定），只讀嵌入的檔案，不會讀到目錄以外的檔案。
- **WebSocket**：gorilla 不支援並行寫入，每條連線由處理函式單獨寫入，另一個 goroutine 只負責讀取（偵測「重新整理」與斷線）。跨來源連線在握手時回 403（Python 是 close 1008，用戶端都視為連線失敗）。快照快取 `SnapshotCache` 的規則（`maxAge`、資料版本、`ForceMaxAge` 0.5 秒、`StaleMax` 60 秒）與 Python 相同；`App.Close()` 關閉所有連線。
- **登入（`internal/auth`）**：雜湊格式、scrypt 參數、session 有效時間（8 小時）、失敗 5 次鎖定 5 分鐘、`Retry-After`、帳號不存在時仍計算一次雜湊（固定時間比對）都與 Python 相同。已實際驗證兩個方向都互通：Go 能驗證 Python 產生的雜湊（測試內有 Python 實際輸出），Python 也能驗證 Go 產生的雜湊，現有 `.env` 的 `ADMIN_PASSWORD_HASH` 可直接沿用。驗證時另外限制 scrypt 記憶體上限 32 MiB（對應 Python `hashlib.scrypt` 的 maxmem），設定錯誤的雜湊不會拖垮服務。
- **管理 API 防護順序**：與 Python router dependencies 相同，先 CSRF（`X-Farmgm-Admin: 1`＋Origin 與 Host 相同，否則 403），再檢查登入（未設定帳號 503、未登入 401），最後才驗證路徑與本文（422）；未登入時不會透露驗證細節。Cookie：HttpOnly、SameSite=Strict、Path=/api，HTTPS 時加 Secure。
- **請求本文**：Go 版加上 1 MiB 上限（超過回 413；Python 沒有限制）。型別比 pydantic 寬鬆模式嚴格：例如布林欄位不接受 `"true"` 字串、字串欄位不接受數字、整數欄位不接受布林與數字字串（前端都送正確型別）；整數仍接受小數部分為 0 的數字。未知欄位忽略（與 pydantic 預設相同）。
- **管理 API 的本文驗證（`web/schema.go`）**：每個端點以欄位清單宣告 pydantic model 的規則（必填、預設值、可否為 null、長度、格式、列舉、整數範圍），通過後輸出與 Python `model_dump()` 相同鍵的 map 交給服務層（沒送的欄位填入預設值，例如 `description: ""`、`sort_order: 0`、`limits: None`）。數值範圍、名稱重複等業務檢查仍由服務層回 400／404，與 Python 相同。422 的 `detail` 是中文字串（同監控 API）。
- **日期時間本文**：接受 ISO 8601（`T` 或空白分隔，秒與小數可省略，可帶時區）；沒有時區時視為台灣時間（Python `_aware`）；結果截到微秒。只有日期（`2026-01-01`）視為該日 00:00（已用 Python 專案的 pydantic 實際確認同樣接受）。前端送的是 `toISOString()`，不受影響。
- **服務回傳 bool／string 的管理 API**：Python 服務回傳 dict（例如 `{"level": 1, "changed": true}`、`{"deleted": 5}`、`{"cleared": 0}`），Go 服務只回傳是否變更等值，由網站層組成相同的鍵。`set_active_title` 回傳的 `title` 與 Python 相同為去掉空白後的字串或 null。
- **玩家進度（`/players/{id}/progress`）**：`checkin.last_day`、`checkin.today`、`weekly.week_start` 是 Python `date`，輸出 `YYYY-MM-DD`（前端以字串比較日期；`AdminCheckin`、`AdminWeekly` 自訂 MarshalJSON）。空清單輸出 `[]`。成就列的 `title` 沒有稱號時 Go 輸出 `""`（Python 為 null；管理網站沒有顯示這個欄位）。
- **新增類的端點回 201**（FastAPI `status_code=201`）：任務、競賽、作物、動物、魚種、釣具、限時事件、稱號、土地、建築、動物、排名規則、世界 Boss、世界 Boss 排程。
- **巢狀本文（世界 Boss 設定與獎勵、排名獎勵清單）**：與 pydantic 相同，逐層檢查必填、型別、範圍（`BossInt` 0–1,000,000,000）、dict 鍵的格式（`ItemCode`）與項目數上限（指定物品 60、每份獎勵物品 5、排名獎勵 10），並填入預設值（`gold: 0`、`items: {}`、`rank: []`、`title: ""`、`title_top: 0`）後交給 `game/worldboss` 驗證。錯誤訊息標出欄位路徑（例如 `settings.effects.ratio_cap_pct`）。這些規則已用 Python 專案的 pydantic 實際確認。
- **date 欄位（排程的 `anchor_date`）**：接受 `YYYY-MM-DD` 或時間為 00:00 的日期時間（pydantic 相同，已實際確認），其他時間回 422。
- **世界 Boss 動作**：`/world-boss/{event_id}/{action}` 只接受 start、pause、resume、end，其他回 422（同 FastAPI Literal）。
- **JSON 物件的鍵順序**：Python dict 保留插入順序，Go map 輸出時依鍵排序。前端以 `Object.entries`／`Object.keys` 顯示的物件（品質與突變名稱、牧場的建築數量、目錄的欄位範圍與事件上限、變種對照）用 `services.OrderedMap` 依 Python 的順序輸出，有測試檢查。只以鍵存取的物件（例如品質設定的數值、限時事件 view）不受影響，維持 map。
- **仍依鍵排序的物件（只影響顯示順序）**：加工訂單的 `inputs`（Python 依配方順序；目前配方都只有一種原料，實際不受影響）、管理紀錄的 `details`（前端只顯示內容）。
- **死結重試**：網站的處理函式直接呼叫服務，沒有包 `Retrier`，與 Python 相同（Python 只有 Discord Bot 使用 RetryingService）；死結時回 503。
- **監控第三批（釣魚、排名獎勵、世界 Boss）**：
  - 世界 Boss 活動與排程的 `settings`、`rewards` 直接輸出資料庫中的原始 JSON（`RawSettings`／`RawRewards`），與 Python 輸出 `ev.settings` 相同，鍵順序不變；監控列表把 `RawRewards` 設為 nil（對應 `view.pop("rewards")`）。
  - Python 列表與明細是同一個 dict 加上額外的鍵；Go 以包一層的 struct 表示：`SettlementDetail`（加 `awards`）、`BossDetail`（加 `ranking`、`contributions`、`claim_rows`），空清單輸出 `[]`。
  - 排程的時間欄位攤平在 JSON 中（`anchor_date` 為台灣日期字串 `2026-01-01`），位置與 Python `**schedule_values(s)` 相同。
  - `defaults_view` 的 `schedule`、`settings`、`supplies`、`categories` 用 `OrderedMap`；`settings.items` 依新增的 `wb.DefaultItemOrder`（Python `DEFAULT_SETTINGS["items"]` 的順序），前端以 `Object.entries` 填入表單。獎勵中同一份獎勵有多種物品時（例如排名第 1 名），物品依代碼排序（Python 依定義順序），只影響表單列的順序。
  - 釣魚目錄的 `ranges.settings`、`ranges.difficulty`、`ranges.effects`（每種釣具）用 `OrderedMap`；`labels` 只以鍵存取，維持 map。`defaults`（預設釣魚設定）的 `rarity_bp`、`difficulty` 依鍵排序，前端只以鍵存取。
  - `fishing/players/{id}` 的 `summary` 與 Python 相同：以「#ID」搜尋第一筆，ID 不同時為 null（名稱中含「#ID」的其他玩家釣魚次數較多時會發生；照搬不修正）。
  - 新的服務方法在 `services/monitor_more2.go`；排名結算的查詢抽成 `listSettlements`、`settlementDetail`，RankingService 與監控共用。
- **程式進入點（`cmd/farmgm-web`）**：
  - 啟動：`go run ./cmd/farmgm-web`（或 `go build` 後執行），讀工作目錄的 `.env` 與環境變數；`DATABASE_URL` 可直接沿用 Python 的 `postgresql+psycopg://…`（自動轉成 pgx 形式）。
  - 設定錯誤、資料庫無法連線或 Migration 版本不符時印出「[啟動失敗] …」並以結束碼 1 結束，訊息不含連線字串（已實際確認錯誤密碼時不會印出密碼）。Python 設定錯誤時是 pydantic 的例外訊息。
  - 連線池大小為 `DB_POOL_SIZE + DB_MAX_OVERFLOW`（pgx 沒有 overflow 的概念）；取得連線逾時 `DB_POOL_TIMEOUT`。
  - 背景結算（`BACKGROUND_TASKS_RUNNER=web`）：啟動後立即執行一次，之後每 `RANKING_SETTLE_INTERVAL_SECONDS` 秒一次；有結算排名、競賽或世界 Boss 時記錄 INFO，錯誤只記錄類型（同 Python `_auto_settle`）。
  - 收到 SIGINT／SIGTERM（Windows 的 Ctrl+C）時：停止背景結算、通知所有 WebSocket 關閉（1001 Going Away）、等待進行中的請求最多 10 秒，再關閉連線池。Python 由 uvicorn 處理。
  - HTTP 伺服器設定 `ReadHeaderTimeout` 10 秒、`IdleTimeout` 120 秒、標頭上限 64 KiB；不設定 `ReadTimeout`／`WriteTimeout`，因為會套用到升級後的 WebSocket（請求本文另有 1 MiB 上限）。
  - 日誌使用 `log/slog` 的預設格式（Python 是 uvicorn 的 logging 格式）；只輸出到 stderr，不寫入密碼、雜湊或連線字串。
- **密碼雜湊工具（`cmd/farmgm-hash-password`）**：提示輸出到 stderr，stdout 只有 `ADMIN_PASSWORD_HASH=…` 一行；長度以字元計算（Python `len()`）；去除所有 BOM（PowerShell 管線）。終端機輸入時關閉回顯，按 Ctrl+C 會先恢復回顯；Windows 與 Linux 以標準庫 `syscall` 實作，其他平台會警告輸入會顯示（同 Python getpass 的 GetPassWarning）。管線輸入時逐行讀取，已實際確認帶 BOM 的管線輸入產生的雜湊正確。
- **介紹網站（`internal/site`）**：
  - 端點與 Python 相同：`/`、`/static/*`、`/favicon.ico`、`/api/info`、`/api/guide`、`/api/stats`，只接受 GET（含 HEAD）。2026-11-14 起另有 Go 版新增的 `/api/guide/{part}`（分段讀取，見「Go 版新增的功能」）。公開網站，不做 Host 檢查（Python 也沒有）。安全標頭相同（CSP 允許 Google Fonts），所有回應 `Cache-Control: no-cache`。
  - 快取：統計 30 秒、手冊 60 秒；查詢時持有鎖，同時間只有一個請求查資料庫（Python `threading.Lock`）；失敗不快取。快取的是 JSON 本文，回應時不再重新序列化。查詢不受用戶端中斷影響（結果仍會快取）。
  - 錯誤：資料庫錯誤一律回 503 `{"detail": "統計資料暫時無法取得"}`／`遊戲設定暫時無法取得`，只記錄錯誤類型（Python 只攔 SQLAlchemyError，其他例外是 500）。
  - 靜態檔：以 SHA-256 產生 ETag，`If-None-Match` 相符回 304；沒有 `Last-Modified`（嵌入檔案沒有修改時間；Python StaticFiles 兩者都有）。含 `..` 的路徑由 `ServeMux` 清理後 307 轉址（Python 回 404），仍然讀不到嵌入目錄以外的檔案。
  - `/api/guide` 的內容已與 Python `build_guide` 的實際輸出逐值比對（4 種資料庫狀態），全部相同。差異只有：時間是 RFC 3339（本場世界 Boss 的 `starts_at`／`ends_at`；`week_start` 為 `YYYY-MM-DD`、`ends_at` 為 `+08:00`，與 Python 相同）；一般物件的鍵依字母排序（前端以 Object.entries 顯示的守護者傷害、季節月份用 `OrderedMap` 保留 Python 順序）；世界 Boss 獎勵中同一份獎勵有多種物品時依代碼排序（同監控 API）。指定物品依資料庫 JSON 的順序（預設值依 `wb.DefaultItemOrder`），與 Python 相同。
  - 時間：手冊的「現在」用 `SiteService` 的時鐘（正式為系統時鐘，Python `datetime.now(UTC)`）；統計的「今天」依資料庫的 `now()`（台灣日期），與 Python 相同。
  - 音樂設定：音樂仍由 Python 執行，Go 的 `config` 只讀取介紹網站顯示用的 `MUSIC_ENABLED`、`MUSIC_APPLICATION_ID`、`MUSIC_PREFIX_COMMANDS`、`MUSIC_PREFIX`（1–5 字元）、`MUSIC_PLAYLIST_LIMIT`、`MUSIC_IDLE_TIMEOUT`、`MUSIC_VOLUME`（範圍與 Python 相同）；`MUSIC_BOT_TOKEN` 只記錄有沒有設定，不保存 Token。
  - 程式進入點（`cmd/farmgm-site`）：連線池 4 條、取得連線逾時 5 秒（Python pool_size 2 + max_overflow 2）。與 Python 相同，啟動時資料庫無法連線也照常啟動（API 回 503）；Go 版另外檢查 Migration 版本，不符時只記錄警告。HTTP 伺服器另設讀寫逾時（沒有 WebSocket）。優雅關機同 `farmgm-web`。
- **測試**：`svctest.Prepare` 由 services 的 `setup` 抽出（行為不變），web 測試共用。Python 測試計算 `MonitorService.snapshot` 的呼叫次數，Go 改用 `SnapshotCache.builds`。

## 已知風險

- 第 1～4 階段已完成、第 5 階段的程式完成（第 1～8 批），Discord Bot 還沒有實機測試、也還沒有取代 Python 版。Python 專案之後若再更新（新的 Migration 或規則變更），Go 版要再同步一次，否則啟動檢查會因為版本不符而拒絕啟動。
- `Retrier`（死結重試）已移植，但服務方法本身不會自動重試；第 5 階段 Discord 面板的每個服務呼叫都必須用 `Retrier` 包住，否則死結會直接回傳錯誤給玩家。
- 資料層與服務層已以移植的 Python 測試驗證；網站已完成的部分以 `test_monitor_api.py`、`test_auth.py` 等驗證，程式進入點也已實際啟動並以 HTTP 檢查（首頁、監控 API、安全標頭、Host 檢查、靜態檔、管理員登入），介紹網站的 `/api/guide` 已與 Python 實際輸出比對，但還沒有用真實瀏覽器實際操作過前端（監控頁的圖表與 WebSocket、管理頁的各表單、介紹網站的手冊），上線前要確認。
- Docker：Go 版映像與分階段上線的 compose 覆寫檔已建立並實際啟動過；正式環境還沒有切換。
- 農田圖片的素材約 21 MB 打包進 Bot 執行檔（網站的執行檔不受影響）。
- **已修正的問題（2026-11-11）**：`db.PoolOptions.PoolTimeout`（`DB_POOL_TIMEOUT`）之前沒有生效，連線池耗盡時請求會一直等。現在 `InTx` 以 `DB.Acquire` 取得連線，逾時回傳 `db.ErrPoolTimeout`（Python `pool_timeout`，Bot 顯示「伺服器忙碌中」）；排名背景結算的 advisory lock 連線也一樣。
- 新增的依賴 `gorilla/websocket` 不支援並行寫入；目前每條連線只由一個 goroutine 寫入，之後修改 `web/ws.go` 時要維持這個前提。
- Discord 函式庫從 discord.py 換成 discordgo；Pillow 圖片合成要改用 Go 的 `image` 套件。這兩部分的行為差異要另外驗證（圖片與 Python 輸出比對；面板在測試用 Discord 伺服器實際操作）。

## 附錄：檔案清單與 SHA-256

共 427 列（2026-11-21 更新；不含本說明檔、`.env` 與 `farmgmdiscordb-old/`；農田圖片素材 `internal/bot/farmimage/farm/` 合併成一列：各檔案「相對路徑:SHA-256」依路徑排序、以換行連接後的 SHA-256）。

| 檔案 | SHA-256 |
| --- | --- |
| `.dockerignore` | `2c4d08e384b50727c6e2625aaf05baf0e2dab0673745fb76cf08cb86e138a3a0` |
| `.gitattributes` | `63ca547940a6eb8d6d98f5cdaf558d8b1ca507683405a68162bcc98a0399bafc` |
| `.gitignore` | `ed067cba1c2cd0c55afd8095173c8430273d1ab5645a95e4861da299abfcd289` |
| `Dockerfile` | `e183be482a0e542f490e216f581bdeda06b5909d1e4c209794b62277fe53bc15` |
| `README.md` | `4f77fcc43e0221df28d40f3e0a2399908a71db689814b807fbb247caf8c1dd15` |
| `cmd/farmgm-bot/main.go` | `f022462d9640374d3a3aae0fe9114ad23aa368b3dc783e1e14c25f831d6182ba` |
| `cmd/farmgm-bot/main_test.go` | `857646c725d083c0075aade2272e386f86ac915f61e37c75bb3c9e8849b9ccc6` |
| `cmd/farmgm-hash-password/echo_linux.go` | `c5bf2bd0fe6941167625dd003fdabf2a4225cbe1b784f00a50dbd6994d0a53a0` |
| `cmd/farmgm-hash-password/echo_other.go` | `46c82871cd3164f40583a5a855fd1a6a189597c58508d8997d340ed0bff0594c` |
| `cmd/farmgm-hash-password/echo_windows.go` | `2b3f6e8fdd1b0f67de6876f24e61bdf3ec407e1c7f46c26d5fb5e6e5eb1cf1fe` |
| `cmd/farmgm-hash-password/main.go` | `c8a575338665560d21e7c1f35958806e0cdfa54202cff316a189c61a3b0d3569` |
| `cmd/farmgm-hash-password/main_test.go` | `73c4c45e4e63da6179d55a8ce2be03639f5829b0ca09685b68ad96a609bed64f` |
| `cmd/farmgm-site/main.go` | `3a809c5af8a0d84397e23389e0fcec789632835fbb5ea36196b55ed709791b18` |
| `cmd/farmgm-site/main_test.go` | `33876694f04134175628c59d76dc2f75b73bf4f950d1ccd9d2bd45849bd3f1d6` |
| `cmd/farmgm-web/main.go` | `eb24567e4ae00fbd3a4c5f92777481631f57a46a01ae198efd58a033cd486ff9` |
| `cmd/farmgm-web/main_test.go` | `e0307c4968dd7ccb9147632e7bc8a11ca91d81b36b073dc3fa47871a4661f1e0` |
| `deploy/docker-compose.go-bot.yml` | `7b2a3564635d9f3d850d4ab66187bb85987c2970951ece4eace2bca7ba617cc7` |
| `deploy/docker-compose.go-web.yml` | `cf2be41ec525a8c9749f6fb0a0d270ddd11df46d6f22e68632de8abb725ae670` |
| `deploy/docker-compose.redis.yml` | `882004a889b94771454968991767d7edc26b9f882eb6948c6f885f49e69360c7` |
| `example.png` | `4e5518d164aa819abcbec3f33e62731e93c1591781957cf65fcae54f69d58938` |
| `go.mod` | `67c14ae38330d1f710c4a22724f17e7ebcc2511d70f27f6992bbc75b3f7b16b2` |
| `go.sum` | `57fff2606d2e1ed7ed9f91d4933e6be9a516a7f713d459f85cf6c3c46153e3ed` |
| `i18n/README.md` | `5804b3be07f09e59ea5ac2e9a3939e09765eb418d5ed9965ba0b74abd70bfedc` |
| `i18n/en.json` | `baecb10e424a2b370f1dccab875e535f13f831a12adcf16a041584010808d32a` |
| `i18n/en.text.json` | `9fdb278d8eb4eecdaf66db8e7717b6662a6ba1300ab814c1ca6182837510da29` |
| `i18n/i18n.go` | `9607e661181898dfaa80baafd02f6a5863597b1cc00ca34013dea211a3dd23e4` |
| `i18n/i18n_test.go` | `c30f905a93d9aeab3427254876b1b037a537203ba30088723304fa8c22cb8840` |
| `i18n/site/en.json` | `f4ef6624bcc125e1879ef178e76da4001adc56a556f3080871328c35fe841cfd` |
| `i18n/zh-TW.json` | `3d2db9a9533bcb917dd0527ab573a1122deaa6d2546a571ec103c6bd970578ff` |
| `internal/auth/auth.go` | `55bc1edd9db87504cf98860cc91194cee22f0fe6f3a1efcba35e5907c4b88605` |
| `internal/auth/auth_test.go` | `5fceb18103f2cb1ec8ced5bf79854cccaedbd732fac5de4fb8e0f1bd507c192b` |
| `internal/bot/commands.go` | `4fdd7649574635f58c7e054eed8a6a7fe0a7087a9e0c023e3b861f1d570d4517` |
| `internal/bot/discordio/bot.go` | `3c16e3cbca0905bd378a2642b22dfed156817834c91e1b60756da48033e49050` |
| `internal/bot/discordio/bot_test.go` | `19fc4e759501aa7e9d9010094f710e0eea340aad16981b7ee7b13eef6731de7c` |
| `internal/bot/discordio/discordio_test.go` | `d0c08b322d8d1bd5625d5ce65e2c597c2bb0ef35e09f77a18df0d867c93a4108` |
| `internal/bot/discordio/interaction.go` | `3bc05d14ea19228ef9ea24d82dc25fc0abda6d78405e1bcdc8a5914e48c33780` |
| `internal/bot/discordio/members.go` | `54bb6c9f1d635694ef5e6947759f0c10eaf9659a747d99cbde604ca2f64c5cc1` |
| `internal/bot/discordio/members_test.go` | `a0567a8f1ad18ce1f95a6f210aee92aad92990b4c0c8248c560d6ebf52600f75` |
| `internal/bot/discordio/music.go` | `1a9e9d3d4ae3a55a450492fd2367caf9f1a0a7b5a7b8d31208e50c76c6da2d2d` |
| `internal/bot/discordio/topgg.go` | `3e5fce617f94d8542344bab51d659278bc1d86023c96f38ddbf77a2331653617` |
| `internal/bot/discordio/topgg_test.go` | `86ebff0ea66e401f3da172db1f0489d3d5c77b4a815f2843c7b69bb7c40e216a` |
| `internal/bot/discordio/wire.go` | `77678a836b80bd6fb95faafd2fdc82661eba818da178d750942ba1d1ef12d04b` |
| `internal/bot/farmimage/cellar.go` | `e10745fcd7fe90db8c12fb2b266eae249859c983720f98671afead9a46766e42` |
| `internal/bot/farmimage/cellar_test.go` | `99925428cc76f070969820f22ff1fb25823847f234dee6bdd0a8d132456135e0` |
| `internal/bot/farmimage/decor.go` | `e847e97f839448de56d5bb25a2f9646368f8aa744653368f85d369bdb34b545b` |
| `internal/bot/farmimage/decor_test.go` | `11ca61b171f62046a8334fce8a34145091b567923d5931b841396cb2bd21d36c` |
| `internal/bot/farmimage/farm/` | `ebce745135ece4d21fc10dde815194d6d1ed44608b5bd069643be152eaff2ffe` |
| `internal/bot/farmimage/farmimage.go` | `3acecec3012a9557bab46ee3e3872e9d983bcd8d5c0d78a8462129c1e697ba85` |
| `internal/bot/farmimage/farmimage_test.go` | `65d360308373204577fcd4b235efdb8da89ba62a4406045262b0246df21c6536` |
| `internal/bot/farmimage/field_theme.go` | `530db9cace46b74abc50851f36082ee77ac62c457e5b139cdd51623c5fa4920b` |
| `internal/bot/farmimage/field_theme_test.go` | `97366765ba61bdb8acbccaca7715da91edfcb7c4a8e7c0f66e745a510bde4302` |
| `internal/bot/farmimage/memo_test.go` | `268df8771eb003325caa13707a788b02faf7601b3b33fe1a2bc31b45c28a6463` |
| `internal/bot/farmimage/npc.go` | `78e8b952bf3244d5f3ce8dfc8c457f3dbee421bba2955e626ea8d8b7d56bac09` |
| `internal/bot/farmimage/pillow.go` | `47755d2852d9e0649decab39b8cfdee7d2ed3ab7ac0a1676ca2b118f85fb420a` |
| `internal/bot/farmimage/quantize.go` | `d625e299ea9b28c0575da204e2dc58ed0f3fc5f83c878cf69d01fbdae77126f1` |
| `internal/bot/farmimage/style.go` | `ae5301f6062be8ac7eef512c12d02199dcae72e4cb5657b867c11d1a5a8afdac` |
| `internal/bot/farmimage/style_preview_test.go` | `4dac620eb95703399930e50ebb4b2f9871b762701f18a29118e7672d56aeda68` |
| `internal/bot/format.go` | `38341c00364c9dbc9ad0780f489b04f1766fb1ab115e71271d23ec699eda1f7e` |
| `internal/bot/format_md_test.go` | `be55bb6fbe583159b017f98f36b9389a82c88eca9a686df4e5ece22fd021e092` |
| `internal/bot/itemusage.go` | `e04958458e8759a304eda4af62e0ed0be3a31553299d9cf4d1c68cd0767bcbb5` |
| `internal/bot/latency/latency.go` | `22b590b542029a0f8ff74b33c71e4fad3ddfa537aa1e1f4fcb6ef77867685b0d` |
| `internal/bot/latency/latency_test.go` | `dd8f6b4595bcbd3a147f84410d74893bb1a87d39b16472a0e717ec8f002dd78a` |
| `internal/bot/pages_aid.go` | `e6e8b1a04f74c0d9a0d4ffa9c73aba24e2081252d3748a2edad7cb406cbfcb2d` |
| `internal/bot/pages_beehive.go` | `b82bdc73fe99df63e754dd2b989fe58c48c2eb381243a3734efd207a62ae3181` |
| `internal/bot/pages_boosts.go` | `ebb856b3100491679fa00b3cbab0a79e104668cb355d2e45df67af5fca1b02b3` |
| `internal/bot/pages_boss.go` | `c5a5a8909d2c0404e7492fbe50ecf97c20419623d58c3df4a230b064d8db899a` |
| `internal/bot/pages_cave.go` | `a46e5730eabd3ac09e97f60832041ebc6d74b3f454d612166fc78332c50bdaa7` |
| `internal/bot/pages_cellar.go` | `a3103f5649b7673299dc146b415eeaea33b5f2d45b80a0d0c90c8b480adc722e` |
| `internal/bot/pages_craftprices.go` | `b5165f28b38c1a2bea678a24ec9ef50e46a9f22a82363a4614fcb06bd669b6e0` |
| `internal/bot/pages_decor.go` | `6a9edb218404dd3721287ff3f5de404109a65647c2962df0d40da0e606622e49` |
| `internal/bot/pages_fair.go` | `b932ca3a597d0000e4609fb71e61889d3e3bdf20921e68ef299845eb8d257154` |
| `internal/bot/pages_fishing.go` | `6252a0228112bf2ee55556495ddbab44c6fa1bd029d21462cd590088c4bd15e9` |
| `internal/bot/pages_goldsink.go` | `cd668c59605874f98c71ca4933ca1bc25a1eecd2c04fde9111e553c2c465d125` |
| `internal/bot/pages_lab.go` | `34b4f61b2766faf4fa8b87471afd49edae9f28c2da9bc009ea158fba87e30237` |
| `internal/bot/pages_main.go` | `e46c6b1475000b548d418e0d34268ba6bd06e56b7874b49a964c55a70efdb38f` |
| `internal/bot/pages_npc.go` | `d0be2eeed78671475994a0f5f6354a175552ae81c431bac81c21be47d10c4d4d` |
| `internal/bot/pages_progress.go` | `fadc91f0ab16b968d2c0bc739ebeefa997c0fcbb07775ff748d73d4b5f1f0ba1` |
| `internal/bot/pages_ranch.go` | `85f29b50c0a2024422e7023e46f457be8ceae4765211be83e43071ff0d891d8f` |
| `internal/bot/pages_seedmaker.go` | `cc7b54eecc2b5987b27529b060185f92957edd1c931685bb683d5539d8d4e341` |
| `internal/bot/pages_shop.go` | `b2d9fa4871711ff67b347f6693de3d038711c1609a421dd17b0d2bd92b20d0bd` |
| `internal/bot/pages_social.go` | `5cf07fa477b3b1bd93eca2c56d4785be75720034713f9c9a5b3ecf90cd7d5bce` |
| `internal/bot/panel.go` | `874c2d86c247cba4e14cf14cbc3b5ee47602068e3d4487f259b64cadf84385ba` |
| `internal/bot/panel_aid_test.go` | `1a4c0cd9fe8e99f2a2100d3f2aa1cea070c5db582eb0dad212e12cc51673322d` |
| `internal/bot/panel_autofish_test.go` | `f8204b17908932e84a9a869f876bd292203f84a7c5ccaa2ad8b1b4bab16ac36d` |
| `internal/bot/panel_autoseller_test.go` | `55eb73ccf2b77df8ed030d4f9066c41cb1e80d86635ce49fa3f4c81d9254e11b` |
| `internal/bot/panel_beehive_test.go` | `ff84689fa32f8ca19215a0acbab8920e29292e6b14c006900c8e26e1c7edcf18` |
| `internal/bot/panel_boss_test.go` | `d94aac119d7868055d446b6070b25871c8924e818c91381cf9165029d5d30e26` |
| `internal/bot/panel_expansions_test.go` | `56627ca1b54c8adf4365018a266cf1fd211b92504cf80778ceca03f3ebf6325e` |
| `internal/bot/panel_fishing_test.go` | `7a1e1ee28c345c5f1b6aba3103d9fdff5b901c6b3d9fc4e8c9b7cbe3efd69228` |
| `internal/bot/panel_go_features_test.go` | `5e2ae2a3a752f6b9b35541372f799e01706fe7592292b0a2ea511040b6de261f` |
| `internal/bot/panel_lab_test.go` | `3b5ce35a1d895a9fc33fe004eb111fae8f22ef09f37ac292041adb54d07871b9` |
| `internal/bot/panel_progress_test.go` | `f8738ee2532b93da6ec75847d1f7c0114388a4606bf2d2122c86a6ed10ea4f6c` |
| `internal/bot/panel_ranch_test.go` | `980736dbf5c5726a45135c13279dd3478365434f503504495be95a5042cb826d` |
| `internal/bot/panel_shop_test.go` | `7ac65cbe3837c238433e007532c4ca1e7168b309f07837c3eb0a57eb79a4768f` |
| `internal/bot/panel_social_test.go` | `7c744677595442d64fd0c18632cbdecf1ef3199a9f4b4964e41d9deb1bd83052` |
| `internal/bot/panel_test.go` | `e4066d172401da6bf113e08685f89e68d421d86c4eeef623ba3c5ebb71e2e122` |
| `internal/bot/panel_upgrade_test.go` | `5dee0df268c9c265b203ac94482d8ab3f8a71598275758eda3ca896cc284d4bd` |
| `internal/bot/panel_weather_test.go` | `1e1beb4dcb3dad56bfc77798bac6041dfb5b3080857500e91ccb0954b0a8e3b5` |
| `internal/bot/querycount_test.go` | `d80fda263bd0122e5e91340f017fb0a700a842cdc6d9df9ab7b8ee4485e9931e` |
| `internal/bot/render/render.go` | `3ae595db2c272fff5578b2977e68c8a0950f9797c85b4c87ca58a5bc6476a1ca` |
| `internal/bot/render/render_test.go` | `6ede2961d1547a3b6fe7538bdf6f3c9a41118f5446b9156aa377e2b437871fb8` |
| `internal/bot/runner.go` | `cdc4ac56fde053cc18a49912202f6b805180e2930059794d810b6361758e4d54` |
| `internal/bot/runner_test.go` | `615c7b85dd2d538d5cffdb9e6a392fb2a236d5fbced66896742498f27ea00973` |
| `internal/bot/stress_cpu_other_test.go` | `78522fc600e79e09ea6054fc2f5b78ce2994ea89392a2aa8812c1f3069b6d134` |
| `internal/bot/stress_cpu_windows_test.go` | `e2b3c8c04414cfcea83bb195f204174de8fdbf75c1de14f377a351e7cf548225` |
| `internal/bot/stress_test.go` | `cd6554893036e3ce7b5cb130061611f550281ac19bc0847ed7f859db02876e3d` |
| `internal/bot/ui/ui.go` | `a6385e0f8387b3ddc88f664b3171a934e5a899e6cc516aa8819f8b5b3983ae6e` |
| `internal/bot/ui/ui_test.go` | `15949b62a68d3ff0c16ca8bf88ef51518f095632471836514c1d606475f680f1` |
| `internal/bot/ui/uitest/uitest.go` | `f1541fd91108e4dce6bb183803f7dcab7a3ae8ac0763e393c58878ca4b775965` |
| `internal/bot/ui/view.go` | `f572c02b1ea49adb490df92a4c2a047c505c8045b02fcc100c4930a6bdd12923` |
| `internal/cachebus/cachebus.go` | `6d5d4b825383dc1d20509d859e7ad5cf6b9f7f9aa2cfc82899c655646193053a` |
| `internal/cachebus/cachebus_test.go` | `b33d1ad0c0783797170a18aba19508056bc9229745c6ee3ab600c8d345d2a1da` |
| `internal/clock/clock.go` | `0017235591ecf1d508aecc3c361e4d7c4a51258fea9c78d7e1049e61e8a4c7be` |
| `internal/config/config.go` | `13addd779cd625d59d61763b461d55de62fd74afa054e4104d2382029789fcc3` |
| `internal/config/config_test.go` | `1b6ad60f0ea2703b97ed3afdaf171d4fbbb6c9058e587734a54e4435d3c49989` |
| `internal/db/catalog.go` | `911a747fb253112b8f2363225e2dad82997330ed40a407edabe39f26a11c5d57` |
| `internal/db/check.go` | `caab0b34938244ecb0faffdf878fa778cc515226736bb01545443a2f4ca8ad5b` |
| `internal/db/db.go` | `7498451f242a1afb51dd89051550b69ac7d87205f2f2cb9b700c6b592235a326` |
| `internal/db/db_test.go` | `bf473f1a9340324c4ba48da4104b59b728bf689c9b7400a81f41fa1f31fd6543` |
| `internal/db/models/aid.go` | `948a4ef30db3cd35f13cf53441b0d8f07eb8a9e5ce139107050ba694ab4a724a` |
| `internal/db/models/community.go` | `f7ad97323c057ea24f4f2edc59769f822794cfa38664bc2f89eb8007620415c9` |
| `internal/db/models/constants.go` | `8d08db5c4a42b6d6f2779cb25400099a664ccdc37d29e4cc772a618292440998` |
| `internal/db/models/fair.go` | `d17535e24c2d781de2bc463a66dc0c779d2316a3bcd2ea8356a5454fbbc41777` |
| `internal/db/models/farm.go` | `8c4806cea019a1d294a41eada6341810528630bc748e2681a44b7bc3674efb08` |
| `internal/db/models/fishing.go` | `e267a9ab5c36757a88e05c861b8320060a3be7e08629959d4a3dbfe4010c1c94` |
| `internal/db/models/models_test.go` | `0990560e5427540bd28e77496ac1aa7b8123f9bce305da320490140e02a9669b` |
| `internal/db/models/progress.go` | `cb00f6be2b603ca016c0aa8249be88bb3e4315e744ef270a76c7a1da1c0f8c68` |
| `internal/db/models/seedlab.go` | `b9d35f2ef387089396acefd10149bef3c0a923d1afd53ee91f150baffdf2746d` |
| `internal/db/models/worldboss.go` | `1f2db2b61d9e2e8db289a7bc1ce0a83a27556475c599307f57750125de51acd7` |
| `internal/db/sqlutil.go` | `f7dcffee8eea287b7a0c182547300fcf6917f9ba7cb8e4d9e86eb3be2f3496b0` |
| `internal/db/txcache.go` | `9979cd0bf7e3275bcc55139caee976ee08e7ca2d61b24b2390c75ed9e4120965` |
| `internal/dbtest/dbtest.go` | `fc184bde830d3ad1df377c1c86dc8ed34512c92a3491625b1cc3846db91415ed` |
| `internal/dbtest/reset.go` | `af3bc9a331e4afa8c09ca7102cc5dc807c50dd539e675e49c114e5f64a76e371` |
| `internal/dbtest/reset_test.go` | `db536239c940c4a4a00a0f8d75be184c8083ef52c2e77851bf14b2f86972260c` |
| `internal/dbtest/sqlcounter.go` | `beb71ca22e3ee4dae728622aa5718563b57638bdb03eb28cdf3de29fcbabb6b2` |
| `internal/game/achievements/achievements.go` | `71f2055030137faf5aad7772df31c2b82dde0a1629448f1b58b4b921d8477ad2` |
| `internal/game/achievements/achievements_test.go` | `36502ffa34d3f21d796e46f863b0d392ff82e3d4d1c7590b5c82c86fc6be243b` |
| `internal/game/aid/aid.go` | `9df48d970c49ecb4ca61b26a7c30016de67db29ba323ebc400bf46f51df0c0f2` |
| `internal/game/auction/auction.go` | `09cc4372d2d16986bc9405fa48f582242ee349aa9d59d12044edc709ff78ba10` |
| `internal/game/autofish/autofish.go` | `c5225a81ea3dae402bae92b16c266cdfd846acad2b05061136327a2b52955fdb` |
| `internal/game/autofish/autofish_test.go` | `3638f72d743d34fa270d8a1f46b33334d83051c326b62e04e80881ad37f10dd4` |
| `internal/game/balance/balance.go` | `af1b6381e5ce91d0da961ede5a34ee38de8c7c5191fe2bad90dd95b6959f95c6` |
| `internal/game/balance/boosts.go` | `18df16718981775972e113b633858c6e5d73f58d889b242ceb211d2cd5ccd88c` |
| `internal/game/beehive/beehive.go` | `afc081b7d5f74a26abf4065bc8e774483594f31951e6047c23a4781a83f36ff6` |
| `internal/game/beehive/beehive_test.go` | `73db5200e68925e4850738ba3f49319cc077762e1d394e96a1ade6c75f454963` |
| `internal/game/checkin/checkin.go` | `7f48d4457a07a7cea8f8ca487c385bd9a8719e833345cb8236ed76506e740d17` |
| `internal/game/checkin/checkin_test.go` | `0ca089b008745912db964774e892ac603c79494cda262f90196b07c23d72c860` |
| `internal/game/cosmetics/cosmetics.go` | `5b559c8cc09619d004e14ea52d3a36cd862b74bbd1db951bf94ef2613f528e34` |
| `internal/game/craftprice/craftprice.go` | `30cac7bce4b2370076e0492cfb6e53d1320dba40189a2159afba6081297b62e7` |
| `internal/game/craftprice/craftprice_test.go` | `896124957284dc8abd745d7d443f21f7dae1857fd7ceec7e46ec73bfbcba3e30` |
| `internal/game/decor/decor.go` | `2b5ee814ab630f996e95bda377ad0f0050b4317d85982c92406470b7355153b1` |
| `internal/game/events/events.go` | `74a1ec9ec051beee22911273766377de756dbe22ccf64e9a687d3c3d7647a870` |
| `internal/game/events/events_test.go` | `06a236f059e6ed8c5896225e683fabf0865d738f7ae7afcaef752d7b1ac6ee70` |
| `internal/game/expansion/expansion.go` | `46244d43a2bc16345fadcb9070009efbc10a0eb2f6018604bf1c83ee063cc12c` |
| `internal/game/expansion/expansion_test.go` | `9335ec528e3729a40b8b385eef92a9e577ea4ac293c19ecc9bc54099fe190ee3` |
| `internal/game/exploration/config.go` | `b17a06906169d5245c102bd8e999be7fe94e5f2a21070de5a45c74acb7b10259` |
| `internal/game/exploration/config_test.go` | `d4c0e93e51686fda42fe5bc53dc61a2ba1203ee7479dbcf189030a63d45cfb87` |
| `internal/game/exploration/exploration.go` | `2c895584644636e474c7a0c17e3986bcd59fe54f2e5b3280197240bed7f94eca` |
| `internal/game/exploration/exploration_test.go` | `76966f97eb4edc2e75896cd892774c884e4abb46770e94c843049c7224636e37` |
| `internal/game/fair/fair.go` | `8a0222a865a872a191da86ee1b2fa6579b4ddd7f9769e15156e695fe3e57c561` |
| `internal/game/fair/fair_test.go` | `484e5238142b1843909540d9c89f372ab263969eade6e49d8e747151bd7069bf` |
| `internal/game/featurecfg/featurecfg.go` | `9df670e9c365358bf783f89ea724663a69053f6390cb846ad8c80af317b28c80` |
| `internal/game/fishing/fishing.go` | `ccf6d406c80a53e116c03f88a405502e9b6e52f835a0f9c1fdb5a864fd94d385` |
| `internal/game/fishing/fishing_test.go` | `f14fd8ebcee3a92b186c95b34ab1108faf7a3555a7c35fadca6f99b723ed1338` |
| `internal/game/fishing/json_test.go` | `7613db59ac20e6f4336bbe131c006a6acbaa7289e43ed46d41072f2dcc5ff470` |
| `internal/game/gameerr/errors.go` | `8ef4a9f3fbd7885be1b743753fa0747b3b8d63b059b0396a66e5cae77cb6d873` |
| `internal/game/genetics/genetics.go` | `fae958a5ab0e297960ed8d814804d97fcbe8cf0ff192bf03d0811bb7bc03dd0c` |
| `internal/game/genetics/genetics_test.go` | `6d5915b5412a42e2f6941200ffa827c026b175ff24f361dce19d659433192eca` |
| `internal/game/hourglass/hourglass.go` | `d7020ab1d5f2ab608fae82b74d600d08f95891f30d3e51f626706c573d85d0ab` |
| `internal/game/hourglass/hourglass_test.go` | `a21c516ec37e0c4847d7a71d5ced841cb9edea886af4466bc3d75343f9af9872` |
| `internal/game/mushroom/config.go` | `f0e38fc0d82368f0b0d3da8b1cfa7ca281490ae5c9dd2dea7fdd56e4e1fac0b1` |
| `internal/game/mushroom/config_test.go` | `7b7c8d91c598802069508db4c51b57c40af59f29d89bc88fbad5ecdec48ddd7c` |
| `internal/game/mushroom/mushroom.go` | `0970f996880a126c8ef700d1c881af11dc46d69ba1c7563ad2900d6e3e1c6af7` |
| `internal/game/mushroom/mushroom_test.go` | `b1ad19c7a8b70e338d9b29ee91359fbd5af38fa797cf4dff6ff864e72b04b788` |
| `internal/game/news/news.go` | `33d449d637516c6a1c06dc3dbc4b87a5e032c2340c86ee5db9088b14aa065ac6` |
| `internal/game/news/news_test.go` | `560577fc7d61efc97ab45f69444a995d5177258a2277f890d6ce3b600a894a15` |
| `internal/game/news/pyrandom.go` | `d42860357a6401ee709b1dc6a98c40c21d81be1428f9776b8ffb91e1130a82fe` |
| `internal/game/news/testdata/python_reference.json` | `77d34f8afea0ffe52d7609883be0791e185e197c38f2be509bd2ae926e53d5ef` |
| `internal/game/npcorder/npcorder.go` | `b6faa5cc927fd007893e56498e40f7b00ea69991283f08e1dadbf7e82fa0805f` |
| `internal/game/npcorder/npcorder_test.go` | `e16cbc910cfadf27fc31112f15d4c5f2771d430e280e02e56306f03f96acfd3e` |
| `internal/game/pets/pets.go` | `739d02183860dd99d9dd6751bc18406fec74adb5a287a6cb4ee6784bafce598d` |
| `internal/game/pets/pets_test.go` | `31e5e5f5ea0a7344adf6a09ee0da4c27e8baa60c33af3115cd9cc1ed52deb8d3` |
| `internal/game/questboard/questboard.go` | `b7cbea8647d564385368ff4a0db2a8cdddbb1328aad72c7473136421ad85f59b` |
| `internal/game/questboard/questboard_test.go` | `55d468cf0eb399957180b6ab8c8f809e2aa907882031666afc7eae2407419240` |
| `internal/game/ranking/ranking.go` | `13beb5e7fa09d66d0aea9ca2b6add07557068472e7d4be65eeec5cf95d36e59e` |
| `internal/game/ranking/ranking_test.go` | `4d8b0cb1e2a1aa23a212531655100e0f135e0ffc8ad4ef81836d72f999fde97e` |
| `internal/game/reputation/reputation.go` | `6bb7ae41acedc0eb3b59e017f4455a1a7f4a8c4c2290eae69fd707f8af9a40cb` |
| `internal/game/ruins/ruins.go` | `da90bf8b9728818c3cdbf52951d79f7e1c0674d5549572848ce8595229e116c1` |
| `internal/game/ruins/ruins_test.go` | `cd832a43e62d3d3263eb281a0718362780b62714f932f54e5d7f24bad6daf3c9` |
| `internal/game/rules/rules.go` | `4c9620f6d180e5a0bddffec7282baadc76028e8f2efee4f4c2c217f22e449012` |
| `internal/game/rules/rules_test.go` | `15a50ff32878f023b6ef40aba46da086e51defd5ad01d0b2e95dc1a8c7af76e2` |
| `internal/game/seasons/seasons.go` | `2581153d954715629f19303427397ecbff227b536980859d662a8717ad2ed62f` |
| `internal/game/seasons/seasons_test.go` | `2c73b7e80242a2d8da08b16ab7a3bafb72d5a1e3c456b43b2a44ff4e110a5f32` |
| `internal/game/seedlab/config.go` | `2b2fcfb7cd750efe12595e4d9ec8f49fe772c3363317c72ea00f6533890c1315` |
| `internal/game/seedlab/seedlab.go` | `e28c5cffe2c7f6b497fb15eb1821eefdd7be44df763d5934b16f1b13e92c6a47` |
| `internal/game/seedlab/seedlab_test.go` | `3bb0cc86a078bbb03dddc96a405399c39601ab8435d3530d60c7c5f7a8c7d6ec` |
| `internal/game/specialshop/specialshop.go` | `6b7038704070b38e78c482cd588f820d2aeb609ccd2d73a1ca658277e17d8ec9` |
| `internal/game/specialshop/specialshop_test.go` | `b7dbccb2994dfdb8f2a49731b417eaa79c6249edad3a45fd0d480d2e81276590` |
| `internal/game/treasure/treasure.go` | `f6b1ac6ad64ce38224bf0b13f8aa131673ced445cf5514c3a8fc27d3e420f077` |
| `internal/game/treasure/treasure_test.go` | `9be49cfcd9b411bf328b2e08d08999bb5bcb0f180f0126d206238093abbb91a4` |
| `internal/game/upgrades/upgrades.go` | `4928af23ac67f14379b8272bc00c1904c1d9fcb0849ecb6d57d0247d46b784fa` |
| `internal/game/upgrades/upgrades_test.go` | `b561158114d08fc0cd7e469d1418d17e704f0db2380fc101c0b5dc939782cea7` |
| `internal/game/weather/luck.go` | `bb36f43074de8cdb5f196492205d0cccf564448aca59329a296d2f847548ecf9` |
| `internal/game/weather/luck_test.go` | `4594ec86c2b7335362f6ef7f6ba1af557d6e5572e39119fc155f5548fad5a13d` |
| `internal/game/weather/weather.go` | `9ef8ade48ef3ef708f6f995d18b00eee1003b2d2f009be57b98cea9361132802` |
| `internal/game/weather/weather_test.go` | `60de641e9a012f625b6596e84a61dced40f0bbdd5ea89db6ab5b3c2278586790` |
| `internal/game/worldboss/worldboss.go` | `bf48640f807809086b67a7788de6cf194c4192bb2f711adaf182f5324c5523dc` |
| `internal/game/worldboss/worldboss_test.go` | `ecf564a6af0ad5184b6787a03e4a06651374fc86316473e216dbc78a22bcaec2` |
| `internal/music/commands.go` | `0f077ace95795aefe14d71330c09f8e0b27f4a398aa9fcbca1b1b07451489485` |
| `internal/music/format.go` | `e13108c2306f10d9d5b194aa5c618ceb7c9183650485d6ac1e47b6a5e8636ba0` |
| `internal/music/lavalink.go` | `6287e01748898a9671e38f2c9bef34f5a532411d761a5c727f65fba3e4125bb5` |
| `internal/music/lavalink_test.go` | `2a9245988e6e6d554f3a5f69457e4370550d3879f929ffcfbd5a5a47e0c5ef8d` |
| `internal/music/music.go` | `9ebf655dc736687a8ff3fb679bd4fa3c0f7ae5e4fc682a5058a36a487c75caea` |
| `internal/music/music_test.go` | `8e05b6ec37222f2eca778bff3016eb1aeb32e23fc899d97e1f9a978e1a38d713` |
| `internal/music/spotify.go` | `e9fd065a5162df93269849e5a7eadd033f1d1e90fa4364e444563cf532a60a42` |
| `internal/music/stress_cpu_other_test.go` | `f9ed9dafd9c7f33ca0fab9dec10376a4c1141ccff461741f154bcee8f4f0646d` |
| `internal/music/stress_cpu_windows_test.go` | `f732255002c997712229f6159e351cb104fb6c09581b9191f14ea9ad6c9412ff` |
| `internal/music/stress_test.go` | `46dad0c6fd8fbdcffae29906be78f05f8996d43da0869cadd1ceea4e5ba2cee3` |
| `internal/numfmt/numfmt.go` | `792645f7d8b56d8dd1650b4daff3d696bce1a7af2c6c6fc4a8982fa0cdfe5202` |
| `internal/repositories/aid/aid.go` | `1fd1313ac1c48824cd32dd0e278638a8b3d0eb63b687314e17b4685e74f8d031` |
| `internal/repositories/animals/animals.go` | `12d48b0012c10a7905e4090480a7e7328e885fb18686d2b492b685c724c6c2fc` |
| `internal/repositories/animals/animals_test.go` | `60a62222f56db2a60d505504255c9f86d1754daa10cd58058b307be00c986b0a` |
| `internal/repositories/catalog/catalog.go` | `55d5ae064eca7c9c05424e2c149f9394582fbb6c96851c40f0d48d1af04bb116` |
| `internal/repositories/catalog/catalog_test.go` | `ee05bc9ccfe990a416cc9a38269161474eca1b6bf8972d0c2bae3fc84a9655c1` |
| `internal/repositories/codex/codex.go` | `4d95da61f821ac820cb7be49af43e9dd659afcb4c487c9b70dea98f7014ee6aa` |
| `internal/repositories/codex/codex_test.go` | `31cfed5a5286323ce4a1adbe3c36e02d39818172c53d5b65528ba821be976637` |
| `internal/repositories/codex/helpers_test.go` | `f1c916ec4707690601a670c6b8a327e22babafb258cf13c07ad721126e3e8000` |
| `internal/repositories/community/community.go` | `7ad3bd048dee7f03b6fb6ac6b8d4d5d0e80182089b4a54c26c72ca88f7881e9a` |
| `internal/repositories/community/community_test.go` | `6a2f6cf1fa19842c9c77b56207703ab4254e874bde8e41252467300bd0e10dfa` |
| `internal/repositories/crops/crops.go` | `53ec3cd852d1200fcbcd1f021d696c93f4451b14ff6d8f1eb0a3b0e68e0ff452` |
| `internal/repositories/crops/crops_test.go` | `bd1d7d8e35b87d3ada06f709aab5aceddc9f512073de058517e19871f12ea2f2` |
| `internal/repositories/defenses/defenses.go` | `4ad45a97e3feabe4d3247b79418b05adfa78b9ac8abead16831abd056cb04b27` |
| `internal/repositories/defenses/defenses_test.go` | `80aeb6a3651affdf584edb6cdd705bca765b04ad8dbf3a417ffd283dde91fe1a` |
| `internal/repositories/events/events.go` | `7507607512de2c7860e5ddfdc0ecaff2fcbea6dc1eba4c50c56eee57a1ff3df2` |
| `internal/repositories/events/events_test.go` | `015f565b7a1aeaa8635f3f4e80f4a653094b4c5b53a698a6e87a076d2e57a395` |
| `internal/repositories/exploration/exploration.go` | `c22416f9a8816a417de89f5783e2edbdc3eae68024e266339270346e90674f8b` |
| `internal/repositories/exploration/exploration_test.go` | `bacae59f6bccfed9a353d54291d170c89562ff5e40514cba449002650afffbfb` |
| `internal/repositories/exploration/helpers_test.go` | `ec9daecb81de6491aea7860246cc4117255b5a0bbed99087d8c70c861fad8981` |
| `internal/repositories/farms/farms.go` | `aae9489e2acf0655fd6f39cd83cefa0c3bf077b22f516f5ea08eade4784ce9c4` |
| `internal/repositories/farms/farms_test.go` | `f49ceaa960d2e1804f4168099669dda45ef268f264b69a27ce762a0a0b95c2da` |
| `internal/repositories/farmstats/farmstats.go` | `347da44c9f1bdbe89d192cf511f2757a6a4569773ddf0a2191c3ffed599f7b44` |
| `internal/repositories/farmstats/farmstats_test.go` | `41231774dfee33d665c13670d392d8371ef41724650ff7193a550707552ce430` |
| `internal/repositories/featuresettings/featuresettings.go` | `1452b1466edaa655b85b35f45b93689bb87e0305fe3952c425bf8a02a8f22f36` |
| `internal/repositories/featuresettings/featuresettings_test.go` | `b0ca5ed9c665c4b5ace5f70552b91d0303860df61ffb4d884b8d3017c558e7b7` |
| `internal/repositories/featuresettings/helpers_test.go` | `e1d46d25725f5251132f9cf5a2322c7db302c9e4a03bab25ef5ac1f098f567f9` |
| `internal/repositories/feedback/feedback.go` | `8bb36741b37c675fef9baca0ba7ab6f0b8f7d6352dee8f20206712c5d7803731` |
| `internal/repositories/feedback/feedback_test.go` | `f01231153814146d916e7e7bef0087293c81ccea02f52620a81c7a2dc3ea108d` |
| `internal/repositories/feedback/helpers_test.go` | `c933b0883ca1d868383c0178069d7edf113ccdc4493e4302f13a88a5a1e3e576` |
| `internal/repositories/fishing/fishing.go` | `c28f204e92c6380a20d1912c0980d2193e3ca87269521649a3ba0c1991191efd` |
| `internal/repositories/fishing/fishing_test.go` | `468ebd9680ceeed64a0ed6628784c342a1ceca96cbb67539c5526b3ff6f9a5df` |
| `internal/repositories/fishing/helpers_test.go` | `5b57f1575ebac144edca42fe126c61025a96de2c12b418a63a51b8aa59f275c5` |
| `internal/repositories/genetics/genetics.go` | `de17b8d1184a90ad58cf25f664bb47d793fb6ec30fcd22564b8d6a9ddac9de2b` |
| `internal/repositories/genetics/genetics_test.go` | `9e90d4abccbf911468979d4f7d7ec8541d960cb1c696b3b416974b1f435ebcfb` |
| `internal/repositories/groups/groups.go` | `7e0850e68553685c94629d643a044749350aa3d869c4a105fc2fe25d921389d4` |
| `internal/repositories/groups/groups_test.go` | `241be3fe3232a34e9741ee21660125977a7757359086a6e018c6bf058eab781d` |
| `internal/repositories/inventory/inventory.go` | `01deb231873e741ff22d86ae8185bd2f89728ce88cbaeb5185c61e625cf6c68a` |
| `internal/repositories/inventory/inventory_test.go` | `c99f0959cbad4e4ba9da61e8c000d27e67bd12795a16e8ab0c8999023f56c72a` |
| `internal/repositories/items/items.go` | `564a7a937e3772a68463baf7a4622a2fbc64acce43f4c20eb59849b6003645b5` |
| `internal/repositories/items/items_test.go` | `7d1ef0a77871fa1708c1c8dcb8114bebfb7f7ff36b0ca860e4991b3658c57a47` |
| `internal/repositories/leaderboard/leaderboard.go` | `3642566995893772fec7d86ec27935465618c2e019b86dab5eae7d5b21a8e991` |
| `internal/repositories/leaderboard/leaderboard_test.go` | `e62b42ae3e88f281bc3cd4fc5417057f87ca0d44d96b48eff43cd824cb3250b6` |
| `internal/repositories/monitor/monitor.go` | `b72f1114115ced43aff8f530199b5dd29a5df04d3fb31068d63dc5b121c949f3` |
| `internal/repositories/monitor/monitor_test.go` | `0a1baaa2eebc6459cea72a7baaadfa51d2000d81b038a03ce4fdc8eefd3db4c0` |
| `internal/repositories/news/helpers_test.go` | `db70a30e5f2ea20619b4e414c76a42f81569bd8a006eb2812ff11a7db5541f6b` |
| `internal/repositories/news/news.go` | `b239a07621fb612c4c7851927e0d8780d0f272bf25fa4abbebff0986acd5060d` |
| `internal/repositories/news/news_test.go` | `1d94f2df16795e90e363305c80b52597e85265879ea7129eb422ce7659a252e2` |
| `internal/repositories/pets/helpers_test.go` | `7f355d682814405a7e0163b489b525f2ac870c641d9da50f98ccfcc149ed8a7b` |
| `internal/repositories/pets/pets.go` | `3269fb6f66f13d7b14f299b3db245b8f6a9f851ac60535f92ea0b94b4d6e41f6` |
| `internal/repositories/pets/pets_test.go` | `dfeb57cdb5ca49ceeaeede01cddaf16c8fbeee794e28c5bc4381846436b8a5df` |
| `internal/repositories/players/players.go` | `3d1af057d288516c0784f1b7c20abf031f4eb64a717b270fe9d9422042ec2a92` |
| `internal/repositories/players/players_test.go` | `3acaa8cc176bf1189217df29d78c25e0109dead2b0729d52d9c66fa482d94f62` |
| `internal/repositories/ranch/ranch.go` | `083372340cfa40025d58726fc2c2d5dcfaf27f5c33deb08444e25bc53c8efe99` |
| `internal/repositories/ranch/ranch_test.go` | `797788b5a094fecb56fa1778b7d155fb60dac442ccf9f69bc596ec5e1fe84171` |
| `internal/repositories/repotest/repotest.go` | `a601b0968995a101a7b76ddb9d9b218a2a2b302c5970aa2bb179fd68f6d205e8` |
| `internal/repositories/ruins/helpers_test.go` | `30c3557b5a85fd1ccad49cc42cea448a3cfd932c56f9b22c22eb4d71e94a851d` |
| `internal/repositories/ruins/ruins.go` | `fb6216228ac5705e54af8800335ddcf67dcb4db8a28e39af065261baeaf9866c` |
| `internal/repositories/ruins/ruins_test.go` | `80c7cb737ada998023ea59a24f6f3ebe25de0719d975e4ff55f6d2d28c9a8210` |
| `internal/repositories/rumors/helpers_test.go` | `62b82f519c632c6742e984f60bfd20014395e035d0860e4ccaf5c072f18d25e5` |
| `internal/repositories/rumors/rumors.go` | `0c36135543fc82dad324e5aa03e06b0f6cceb1a66f99f5028f488e1aa6c27ff6` |
| `internal/repositories/rumors/rumors_test.go` | `cfde7b1848d056e72383940b55fe8f7d689eee30ba55eb15f7a104728188e58e` |
| `internal/repositories/seedlab/seedlab.go` | `3832cc0903e97e38a21c3a0a6e1222deac03ab88db5d55f86daab8617fe55dc4` |
| `internal/repositories/steals/steals.go` | `97073442f679954790cd7b1da67fbedf0bab375027775d5559b7d715557e659c` |
| `internal/repositories/steals/steals_test.go` | `744eb5944b865e72c4fe9a10b9bdb3c5484e81dfa4ff34629c963f2afa84b18d` |
| `internal/repositories/titles/helpers_test.go` | `177beadee36177f47abad097d60f225d6f964274b36630711a401d14ef97964a` |
| `internal/repositories/titles/titles.go` | `36acc0704bf9b1d16a5e0d50f5903e03d9808525126114ff08e7f2a0699389f0` |
| `internal/repositories/titles/titles_test.go` | `5ab3a8f591e7dc7aea3f5df77b81ef51b6741a3821f9938fb4b67ee1122d7a08` |
| `internal/repositories/transactions/transactions.go` | `df76708abd94cc6a215a9593e54ede986ea48ed3da506015eb536ba5aec1abec` |
| `internal/repositories/transactions/transactions_test.go` | `f95ae56b518fcfbb28012ce301c4cd1519e9505ccc932bb84cc6583bfaf135fc` |
| `internal/services/activity.go` | `f221dad36640bcaaaea3d561d5e50897e76c221f5d60d0a354940c45fe5377b5` |
| `internal/services/admin.go` | `4d229709b4270a9350064d9b6e5733be71b90d3a919f4d86b07014773a84ba6c` |
| `internal/services/admin_features.go` | `c9713e90a31101387f43a0479ac4e3fab58e2afe5166e5bedaabd15f7be8361f` |
| `internal/services/admin_features_test.go` | `2af1827054d3eb8a0cd7d393a1b750b8cbe9f1e71ef699f961c7b28820540cc7` |
| `internal/services/admin_test.go` | `d38f2678a219857f1f69494a95d732b3477908988beb5e369ae3dc7a2e04df47` |
| `internal/services/aid.go` | `909c70153573ebeec4f382a1a0e0a9e243c7f66cc26140b9589b4cbb5368009e` |
| `internal/services/aid_test.go` | `26a6cee0247a04c7522e8fbb0ea7065e22464d13dca49f15c0d29d4623a1e9d1` |
| `internal/services/auction.go` | `c1c2a3895dfdd5a7aee83486c7ad2bc3303bda0777ad02cd5a0aca2bce71f766` |
| `internal/services/auction_test.go` | `f37fda488b71eebf7220be038898f09b4b14267a8c588070a0a2dc933d24799e` |
| `internal/services/autofish.go` | `cefffbfe3fa24d580e017658a3c5445798bdddb69e5c3f32160378f42d53f956` |
| `internal/services/autofish_test.go` | `7f5e2cf847542b32bd3b1a938a089db9be5d20bd8510b09ee8d864dfaa8b6012` |
| `internal/services/autoseller.go` | `49e5cafae5d72b6e917606879cab54c85ccadc8593a8e6e3d706f619e50bbff6` |
| `internal/services/autoseller_test.go` | `970fa39a39fdd2418d15f765e1c6770e529043fa1ce12a68c273be45aec206e7` |
| `internal/services/away_test.go` | `35ab2172ea393d71579dc438506697abcb850324c264316c157cb00ce6e0ee40` |
| `internal/services/backpack.go` | `b38d95f32acf529f9fb3734466479934fa83b8e659b81546c65959d12212ecaf` |
| `internal/services/batch_test.go` | `9e04362497997f348edce7bade3a8d600e5f4a8521e2f34322bf6ff343b1bd49` |
| `internal/services/beehive.go` | `86828ca9e2be4ddbf3bdd8d8a99475993547a10807b9b50b01d48b8b35c24d36` |
| `internal/services/beehive_test.go` | `aa0dc0c60e857326d97fdc733c11547d58b8fcfe0704fdcfaaf94dc870d69aba` |
| `internal/services/boosts.go` | `6f84449b160013431866a32529e05c0101df1ba47ee5600df7da23c433c64f2b` |
| `internal/services/boosts_test.go` | `d3cee9be8e59281912bfc4e4cea32e73c2f187e4e85e2f9047203514722fcaf1` |
| `internal/services/building_upgrade_test.go` | `5cc2fbc1f5b3988c634d059b2b0cdcc7c26a6c3d7d9d1af18a2bd1c74b849d4d` |
| `internal/services/bulk_test.go` | `5245508d3035337a6d3833b589793430aeeaacd70931fc232186695912162265` |
| `internal/services/catalog_seasons_test.go` | `8cc950229ddd4da7e4966b6db8188a283de8405025f76bf4e3aea79bfb95a046` |
| `internal/services/community.go` | `4c3dc0d3b4d8b5759adc80aa398859a33c7f8286e7095287111de57717e1de64` |
| `internal/services/community_test.go` | `fab57c9a96c7cab7bef3ceaa413cecf0ace86b9089f9a6256aaa5f1d77317eb2` |
| `internal/services/craftprice.go` | `7ff1bd7ca7b6f44adb75bb7a2ab831cfba2adeb556346e585355167b5bb895d4` |
| `internal/services/craftprice_test.go` | `9da8799ec1702f914c9fd9fd223bf8e8e494f3656b00ca90b587c365985a5a35` |
| `internal/services/crops.go` | `ca440415a6d31a1cb7657688a774061a04015fd79da5ad5f5aa262a4d2029025` |
| `internal/services/crops_grow_test.go` | `ef34a6cae2f8a31df2b005d6e5e194e52868dfd01dbadc9aeeacd413820837fc` |
| `internal/services/dairy_test.go` | `3c6d950f72daa794a01b11a83843fd0f8cf950d3bfb2cad786d0babeb9eeb524` |
| `internal/services/database_test.go` | `7f9087209119e70b912f4ac1a70e43014276d3a8b211db9b6645982d201f83c3` |
| `internal/services/decor.go` | `d3b91e298de58f960935da20175512e2253f09d0c485eeeda53da446ae3e3e73` |
| `internal/services/defense_test.go` | `bb8646010681fb0b5e2888b9bb60b7022ef6092974e63ecdfe07c19b27d4ab69` |
| `internal/services/dto.go` | `797816fcc50057c5d7c4ef1c2a80045eb589a752ec15d4acf73affaee3b6f677` |
| `internal/services/expansion_test.go` | `3c3470294b254770a2d4d4c4bac1dfda3797c10e91516d7eddf931a9fa1bb075` |
| `internal/services/expansions_test.go` | `a519a050543749029c2e19bbf3bd5dc7af3d6abab46a916c135139c6cb2c2df4` |
| `internal/services/exploration.go` | `8d348affaf5f4c4bc12bbab2d878f489c669c85eb65d8a5fb19cc1e0700202fe` |
| `internal/services/exploration_test.go` | `669399889a2b161a3117ba54d25e16cad0894220b4badde4a294f4e324d6e553` |
| `internal/services/fair.go` | `4c43a580ce6d90f619ef7d68bf00fe91827be68420fa6ddc4f84530952f1f9c7` |
| `internal/services/fair_test.go` | `385b5b38efbf140e7dcdb8791c76a2102fa39c81104ae8810e9471834d86d722` |
| `internal/services/farmstats_test.go` | `a15446358f01b77a334540f6511072af77a2c0681298e0479957c3118c89d819` |
| `internal/services/feedback_test.go` | `a9554d2d3afd0e0d815cafa637231c329695a9b5f3ead025cc20f23ddc5febb1` |
| `internal/services/fertilizer_test.go` | `e3e1684720caf7460607048f9f99bd2ddb61b5ea1da35077e881bd9dd3414cf1` |
| `internal/services/fishing.go` | `e967fa6b233f979318dac05d8fe4e9042b73b384e14e7f874200f1a75c4dd1f3` |
| `internal/services/fishing_test.go` | `4588b68cec67c90e5d94da9b6d047a409423b66e6919e55783c0f685e8d454fc` |
| `internal/services/format.go` | `7b229179005e0daa8314f74ca5963b42527e3cabca0e7a9716186438feed5254` |
| `internal/services/gamble.go` | `e185b201a03f0e2d90f8b413e8f733c1532ed189f7f98058385fa080ed2535b3` |
| `internal/services/gamble_test.go` | `88a175c8b93c4d5d390bfbd1c48f66737dddfd199dc7d0ed7159e5217d21715e` |
| `internal/services/game_service_test.go` | `4f9c1ec172c1342a01b4533728b6031de2c4056d5f119f8802be9fcb707bf94f` |
| `internal/services/genetics_test.go` | `2e50fe222ddbcce44620c4f727c6d21190ce1016b1b64c8cbd3d434849c884bc` |
| `internal/services/go_features_test.go` | `df1be39a398eb70992d18c81c2f282801f9f87dc39392122e662b44e36d25b42` |
| `internal/services/goldsink.go` | `2e6d6b39c73048866df9546f78cba13fcd617b29289385e4fca60c6ef9e1addd` |
| `internal/services/goldsink_test.go` | `7812da64f0c260c62159cae05abc5a8e0ca01f409bfb704b00cae06cd9e9f473` |
| `internal/services/group_test.go` | `d3ef99ebc8ac4ec2929755c137f76a8f2d2840e9765861badb2e06107acb3f3a` |
| `internal/services/helpers_test.go` | `51bdcba9ae26e2bffc8ac309dadcdc8f4ae05880bb4ad7a8565227c87888165c` |
| `internal/services/hourglass.go` | `a739b997c9a74ee7a0494dc0473497836a511844e228a49edfce9f19a5673e18` |
| `internal/services/items.go` | `19eb32dadbcfea45bbde0e7d828f8671fb70648668860ed198b1687a7a5b0488` |
| `internal/services/items_test.go` | `c26fb4921ce08d652836b14c337fc6b07c060763524a610130c2abdb1b109a8c` |
| `internal/services/jam_test.go` | `067d5b83180d9dcc95c3cd0488eaf7f1404ac332f794b8e71a1ca1197ec8dd2a` |
| `internal/services/lang.go` | `10111be3a61cc1936aeb94241f6d6127221bf97870aeaaab57a5764895d174fb` |
| `internal/services/luxury_test.go` | `91fd0f13d9c784d3f1bab6de2cabcf59da34a4ffd6154580f07920803b932a0f` |
| `internal/services/monitor.go` | `dbdadaf77d5ad6cdaa9705f334af4edebf0712515651c541a466e6a46ab33be8` |
| `internal/services/monitor_more.go` | `d89d6730e28862b0781e69f254eeb6f5fcea3ca4fbdb24abb5925541e26d6697` |
| `internal/services/monitor_more2.go` | `9bcf88fce251713494c1d377204e4b29b682653037582a7b0fe50d725e77beb2` |
| `internal/services/monitor_test.go` | `9490a74814411c190cbd075b5bfc181f4c3d63aaf1b862320b653fd5453eb5d9` |
| `internal/services/mushroom.go` | `a8ea3655d204a6730b6109acf7471f8aa9f9c2ee6d1cf555f30f20a2b6a9478a` |
| `internal/services/mushroom_test.go` | `680ebbeac62cfe371c380b3f962155692b32858c918f3f05082abbcd438e8d23` |
| `internal/services/news.go` | `28b9d74081457ce6a64dca457c2997e017b7848ab97cbb5a136ed7b93b165398` |
| `internal/services/news_test.go` | `5914ac31c9b81dfcc389854c20493863c7020e45b428b76b33116fc924c41b25` |
| `internal/services/npcorder.go` | `960073f78e8b7098d09912d5d1e77a6bb9724d01187b4a1d97e828956bdc784e` |
| `internal/services/oneclick_test.go` | `c9d6013f6c6c63ecfda1dbf1ea40aa70a6e83ea20d1b56a884e92bf075b0b9cc` |
| `internal/services/ordered.go` | `24ab9b4f5a9d0146fd4e36e5bbc55f6d8b96d0af61817c5a4c8b96ecc6558df3` |
| `internal/services/perf_test.go` | `9c6939c77bc3728f3d14f7b1527ebb7b50376f3c670d2d1aea97e5cf66c76852` |
| `internal/services/pets_test.go` | `e76226dc5f9e29202606aba4ca93dfef6f7efdd25e8191fc0d629ba1b42ac311` |
| `internal/services/players.go` | `a465c8946be5e97bf2dc0c95892be3ee9ce5be7db62001518ee9d46dfc999579` |
| `internal/services/progress.go` | `07f7582b4aedb915761630f1a216a4122411dee2007e0ad54bee7d9369517553` |
| `internal/services/progress_admin.go` | `7b73137e30b8906aba7896bfc0b0786f001274ae0a824ba560b43bd80b427e3f` |
| `internal/services/progress_admin_test.go` | `a5d88bc69eec9ab80386d07792f5ea1726d726d41252379d2d8edb6522ae9e7d` |
| `internal/services/progress_test.go` | `a5873ad8c37e6cdf7d8f18299fb76081ee36872e94f1a6ce7558b14ee281a1ce` |
| `internal/services/questboard.go` | `810c97be1d021b00fb27751caf33e69d7f1f72d26f349eeafa2dedc25366b1d3` |
| `internal/services/questboard_test.go` | `f1e640205e6d659345aa6d81daf19ec1195f4f202257d8f1a5511184beaa0624` |
| `internal/services/ranch.go` | `3c035ced57021c55669c52fb1abe473c0844b81a27a90bd4cacfa76619644d19` |
| `internal/services/ranch_test.go` | `57376bbc85b88637f34bc488b540507034b59668ea7af284c21ded13a434a357` |
| `internal/services/ranking.go` | `683f0f163b248105c3611136732f2076acd1b4ae180832e55adea38f86fa0b51` |
| `internal/services/ranking_test.go` | `0701621de69f1f78ef54108dc123efa9c5f917f9875463da86f74801f01b8c94` |
| `internal/services/retry.go` | `ba146e2ae2ceca83016f2af28ee78a44af39f946325459e372dc994c267c5385` |
| `internal/services/ruins_test.go` | `80565e5b64a40c7d283bb7ecfaad5cafd3c4a1ac34853a216d38d609618d40d6` |
| `internal/services/seedlab.go` | `0548d953e51b78d6336e0a7ab9c3156bde24d7e1d0943f19cf8752ba1cbb13c9` |
| `internal/services/seedlab_test.go` | `52625b90459e861daf0f40189c9e96e41d43816d9f764adfb7d527effff3a55b` |
| `internal/services/service.go` | `47553473911be31669390a5ad062b60d32e882b239c40eca76cb6ddc4e1724c5` |
| `internal/services/site.go` | `cbd27e4091678e27d5904ade4202288cf1e377ce1472951eb494de874f022329` |
| `internal/services/social.go` | `f906f89c1464b922844d9df1f2f3f24614a0e5452e4e0cca57823cd90feca522` |
| `internal/services/social_test.go` | `4121c18ce6bdf8ceaa0fbae387f73a3190ee2bbc544cf81091b836a93012a51a` |
| `internal/services/stress_test.go` | `a4aa847f169673c1d8af3656a9508f25c226415f50f465f141f546e025ec2e8f` |
| `internal/services/svctest/svctest.go` | `8f67dba5c5a8ac1022ab1bc24cc6feed1e7a636a9d5c7f2cc3f23e607f3a2a1b` |
| `internal/services/todo.go` | `1fac408e274a7a4278f9a1852181e410ffb3721015a64aca36308b6c88cd7c43` |
| `internal/services/treasure.go` | `1b34acddf14a55cfad82eab19d322bb4d3438f48eb7c3e39884e2565809ff754` |
| `internal/services/treasure_test.go` | `cdcca57827a0a620e5d0d84be4f545856fa564f68bcc7a141edd33a7f1079325` |
| `internal/services/weather_test.go` | `ca98ebbc02be0aab4b3f63ab790f467fe397b9a3465eb7d016d95f30b6ede8db` |
| `internal/services/worldboss.go` | `841468cc2fef723b811cb2e3dd181c18141d7f6d69a3e67ec92b063cb4fe1a75` |
| `internal/services/worldboss_test.go` | `6d67a18555fe0fc42247a4c06a121167f83e89b1af776d1fdf89fe08313adaca` |
| `internal/site/lang.go` | `638b8a19b15f23ad1f54edc209f4e06e43f0fed86417fbfc57b0daab16b7ea6d` |
| `internal/site/lang_test.go` | `cadfcf39f169b75a30c98835d5ef303bdad7851fb0c5f09b776175d1bcf65743` |
| `internal/site/site.go` | `dbbdf1a341fcc084c9b3e59b9232ff04f6af68d913a5d55779503f232ca1acaf` |
| `internal/site/site_test.go` | `58da9ff9cec0527697d072123eec186ee32ff290510bf86ba7c8aea1c244854a` |
| `internal/site/static/favicon.svg` | `bba4123b033483f9ca309a1031c36a6459679843def75245bca83d74575bc292` |
| `internal/site/static/index.html` | `847b1f247ac77a41dd055f52c6276ed92a16e8750839fb5fc7df71833db73484` |
| `internal/site/static/site.css` | `129120fa179426847b9a31930fe991eef0a7bf538f110c9e8a0363548e3b1729` |
| `internal/site/static/site.js` | `9e9aa859e4067241ec69660c3d38228fd42cd9577a45695c460aa3da8210018e` |
| `internal/site/stress_test.go` | `25fccaf291acf0f92aa6fbeffecca0353e06f4cec62a57d27df104bd922050c6` |
| `internal/web/admin.go` | `8179879a5db82198ab600bac39e9d5aacc3be855ae61b4bb1cfaa176ea924080` |
| `internal/web/admin_boss.go` | `a588cb805d2739bcaa5fefc6cb5c5abc7d066d70bc6abdb14ba94efa2513924f` |
| `internal/web/admin_boss_test.go` | `749a86128ae3d09ad2b41ab37cd127a35e56cd5b4ec6bed8edd98649f35e9919` |
| `internal/web/admin_catalog.go` | `140c32bfc5f42765d2f8d0b4b0c84284fd932263160c918a290cdaedb2ea17a8` |
| `internal/web/admin_features_test.go` | `50c8b774513adb85c0666183f62b9139fb45727a1f52216cbf9b3e14e5174a82` |
| `internal/web/admin_players.go` | `41b1c7bf03220602446b4e1bf31327755da89f8330197eaf45846cb3c855bf6d` |
| `internal/web/admin_test.go` | `e958c9798c08bd60d75a5c9a6b4e40aacc561018e99fe8d0acd9d0d57452f6dd` |
| `internal/web/auth.go` | `ba5da1c32cb1f97eb075f1f140646a0cc7e43b27e9902e3e046bb6406a0b387f` |
| `internal/web/auth_test.go` | `415a195330d07f74b0b4c9d686b7029e577c22afcec83d35e106ecf89faa6573` |
| `internal/web/body.go` | `6aa064d410d57d90f2549cd5d3828c375c8e3a58a08ffaa3334e14461afc64a3` |
| `internal/web/monitor.go` | `a2da4535241a8282a7443fb1e534f5eefbc5aae95197f0820a02a3bf2b4ead1a` |
| `internal/web/monitor2_test.go` | `373963a779e25e0334f1a07113bb9091c4951ae4c05d39d1b561fc4b7f01c2d0` |
| `internal/web/monitor3_test.go` | `570e0e3dee7aefcd86a3508fc57458fe754659736a6700aea74c308e4e576a63` |
| `internal/web/monitor_test.go` | `a8d5370a6862de881bb52ed9dcc9b4101f722e2f2c9bd2446d50532e60db782d` |
| `internal/web/params.go` | `34b8405d750761c6280cc542b0919b265122369fc254f2bc91e89a9e26f1b745` |
| `internal/web/respond.go` | `869011e0448845d1d9106ff5d2ecaaf8c815d043a9b5e84b51c24f33d364d67b` |
| `internal/web/schema.go` | `f0b0107ada37ebd88f1b71f73c1d9f3c2c48afcc69eae25203c24bffaf41b6a3` |
| `internal/web/static/monitor/app.css` | `7b14f1ca62408340284c3b895d69b6d4dbbf6aaf2720d2da313f5906e1101664` |
| `internal/web/static/monitor/app.js` | `3a1a9f51ffc8981d8df9196a1168dcc4b8c1fec0272a30af68ab6fdb8b38bb91` |
| `internal/web/static/monitor/index.html` | `bca83af5db8644fcbb83f03b857cb7c19699959023985770fef70fa10e9c9e58` |
| `internal/web/web.go` | `2eaef4fc10ef26c31b7a977e5b1d411cbc6edb4c0c753e1215f028a95fadfd22` |
| `internal/web/ws.go` | `dda2e1fd64b2f7f6adb5d31c531feebfb0e0956b3ea5a2ed7e93863c39bd8b9c` |
| `付費道具流程圖.md` | `1bc4a3b87580cee6cd49dcfe6934e7d8e0e0658531172991921f0ff51b3378bc` |
| `效能比較.md` | `5ae92c8fc2ef392bc332f241ef9a87432565549cbe82673114752a5a6e6c66f3` |
| `新玩法六項_流程圖.md` | `519bfade24e9449804cf7dbe174e81a1e30344ecf5e8fa308c79274bb2e2b1c7` |
| `種子實驗室_作物素材需求.md` | `3706778fdc011485721ab039fdad9ddd1fa01a473f5628bc79177f1f9a03bfe2` |
| `種子實驗室流程圖.md` | `f569ca792e01927818396db4d4a710f1bfd7b7a20a6649a879c1b0548c8b02b8` |
| `英文翻譯用語表_已填寫.md` | `fb6d67284a5a5ad70be2a0b2e152f22a2e51bd9db6b9cce8843edf055e9a4aa5` |
| `金幣回收_外觀與擴充流程圖.md` | `642c2b938bb6205f2b745c5632d42ab92ebbc919ddababab53ebc32646424537` |
