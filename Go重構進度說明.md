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
  - **種植**：特殊種子種到空地後，生長基因每級 -5% 生長時間（澆水時套用）、產量基因每 2 級 +1 收成、品質基因每級 +1% 品質加成、突變基因每級 +0.5% 突變機率；種子的突變直接成為作物的突變。收成時 30% 取回 1 顆同基因的種子（傳承）。
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
- **音樂改寫成 Go（`internal/music`）**：Lavalink v4（REST＋WebSocket，30 秒看門狗自動重連；2026-11-15 起開啟 Session 續接 120 秒，WebSocket 斷線重連時接回原本的播放器，不再中斷播放）、Spotify 連結解析、16 個斜線指令（名稱與參數與 Python 相同）、播放控制按鈕與佇列分頁；各伺服器音量與前綴沿用 `data/music_guild_settings.json`。設定 `LAVALINK_URI`、`LAVALINK_PASSWORD`；`MUSIC_ENABLED` 且沒有 `MUSIC_BOT_TOKEN` 時在農場 Bot 內執行。只支援斜線指令（前綴指令需要 Message Content 特權 Intent）。實機測試：`TestRealLavalink`（讀 `FARMGM_TEST_LAVALINK_URL`／`PASSWORD`）。

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
| `cmd/farmgm-bot` | `app/discord_bot/__main__.py` | Discord Bot 的程式進入點（只有農場，不含音樂） |
| `Dockerfile`、`deploy/` | Python `Dockerfile`、`docker-compose.yml` | Go 版映像與分階段上線的 compose 覆寫檔 |
| `internal/web` | `app/main.py`、`app/api/monitor.py`、`app/api/auth.py`、`app/api/admin.py` | 網站：Host 檢查、安全標頭、監控 API、WebSocket、登入、管理 API 與本文驗證（`schema.go`）（`static/monitor` 是 Python `app/web` 的原樣複製，以 `go:embed` 打包） |

## 設計原則

- **資料庫 schema 不變**：與 Python 版共用同一個 PostgreSQL。Migration 仍由 Python 專案的 Alembic 管理。Go 只檢查 `alembic_version` 是否等於 `db.AlembicHead`（0038），不會建表或改結構。Python 新增 Migration 時，Go 要同步修改。2026-11-13 起為 0039（種子實驗室，Go 版新增的 Migration）。
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
    - **bot 面板第 7 批 25 個（`panel_progress_test.go`、`panel_boss_test.go`、`panel_fishing_test.go`）**：`test_special_shop.py` 的 test_panel_special_shop；`test_achievements.py` 的 test_panel_claim_and_choose_title（另加改回不顯示稱號）；`test_checkin.py` 的 test_panel_auto_checkin_once；`test_feedback.py` 的 test_panel_shows_codex_hint_in_gold；`test_discord_panel.py` 的 test_ruins_page_donate_with_buttons、test_ruins_guardian_attack_from_panel；`test_world_boss.py` 的 test_panel_donate_flow_and_double_click、test_panel_other_guild_cannot_see_event、test_panel_boss_image_only_in_intro_area、test_panel_concurrent_double_click_no_error、test_panel_fish_multi_select_donates_all；`test_discord_fishing.py` 全部 11 個（test_fishing_pages_render_within_component_limits 合成一個測試跑 5 頁）；`test_fishing_guard.py` 的 test_panel_shows_check_and_fixed_fight_buttons；Go 版：圖鑑與成就分類切換、Python float 顯示（`pyFloat`）、釣魚畫面計時器到期時實際自動更新。test_every_page_renders 改為 Python PAGES 全部 36 頁（`TestEveryPageRenders`）。`internal/bot/...` 另外在 Linux 容器（golang:1.27）以 `-race` 跑過，沒有資料競爭。
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
- **拉扯數值**：Go 的 `FishingView` 以 float64 保存體力、距離、張力；Python 的狀態都是整數，面板以不帶小數點的方式顯示（`num`）。世界 Boss 的 `hp_pct`、`ratio_pct`、`attack_mult`、`uptime_pct` 在 Python 是 float（例如 `100.0`），Go 以 `pyFloat` 顯示成相同格式。
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

共 310 列（2026-11-15 更新；不含本說明檔、`.env` 與 `farmgmdiscordb-old/`；農田圖片素材 `internal/bot/farmimage/farm/` 合併成一列：各檔案「相對路徑:SHA-256」依路徑排序、以換行連接後的 SHA-256）。

| 檔案 | SHA-256 |
| --- | --- |
| `.dockerignore` | `9673bd4ad874d1f1c456084c962705ff7c7aba5fbcf90ca3a5057e9ae465b51b` |
| `.gitattributes` | `63ca547940a6eb8d6d98f5cdaf558d8b1ca507683405a68162bcc98a0399bafc` |
| `.gitignore` | `ed067cba1c2cd0c55afd8095173c8430273d1ab5645a95e4861da299abfcd289` |
| `Dockerfile` | `4270ab723221f778fa88450725719b39c5a0d527aa997068b1ceb2dc9f92cff1` |
| `README.md` | `e0e14861e72bdc32f6ad7944aaaeacd1ad79634cdfff0850df4833811a48503b` |
| `cmd/farmgm-bot/main.go` | `f022462d9640374d3a3aae0fe9114ad23aa368b3dc783e1e14c25f831d6182ba` |
| `cmd/farmgm-bot/main_test.go` | `857646c725d083c0075aade2272e386f86ac915f61e37c75bb3c9e8849b9ccc6` |
| `cmd/farmgm-hash-password/echo_linux.go` | `c5bf2bd0fe6941167625dd003fdabf2a4225cbe1b784f00a50dbd6994d0a53a0` |
| `cmd/farmgm-hash-password/echo_other.go` | `46c82871cd3164f40583a5a855fd1a6a189597c58508d8997d340ed0bff0594c` |
| `cmd/farmgm-hash-password/echo_windows.go` | `2b3f6e8fdd1b0f67de6876f24e61bdf3ec407e1c7f46c26d5fb5e6e5eb1cf1fe` |
| `cmd/farmgm-hash-password/main.go` | `c8a575338665560d21e7c1f35958806e0cdfa54202cff316a189c61a3b0d3569` |
| `cmd/farmgm-hash-password/main_test.go` | `73c4c45e4e63da6179d55a8ce2be03639f5829b0ca09685b68ad96a609bed64f` |
| `cmd/farmgm-site/main.go` | `40d69f68d21d71918fa2ae76be6b77cd278f54131ada2961a3f4116c78086d7a` |
| `cmd/farmgm-site/main_test.go` | `33876694f04134175628c59d76dc2f75b73bf4f950d1ccd9d2bd45849bd3f1d6` |
| `cmd/farmgm-web/main.go` | `eb24567e4ae00fbd3a4c5f92777481631f57a46a01ae198efd58a033cd486ff9` |
| `cmd/farmgm-web/main_test.go` | `e0307c4968dd7ccb9147632e7bc8a11ca91d81b36b073dc3fa47871a4661f1e0` |
| `deploy/docker-compose.go-bot.yml` | `7b2a3564635d9f3d850d4ab66187bb85987c2970951ece4eace2bca7ba617cc7` |
| `deploy/docker-compose.go-web.yml` | `cf2be41ec525a8c9749f6fb0a0d270ddd11df46d6f22e68632de8abb725ae670` |
| `deploy/docker-compose.redis.yml` | `882004a889b94771454968991767d7edc26b9f882eb6948c6f885f49e69360c7` |
| `example.png` | `4e5518d164aa819abcbec3f33e62731e93c1591781957cf65fcae54f69d58938` |
| `go.mod` | `67c14ae38330d1f710c4a22724f17e7ebcc2511d70f27f6992bbc75b3f7b16b2` |
| `go.sum` | `57fff2606d2e1ed7ed9f91d4933e6be9a516a7f713d459f85cf6c3c46153e3ed` |
| `internal/auth/auth.go` | `55bc1edd9db87504cf98860cc91194cee22f0fe6f3a1efcba35e5907c4b88605` |
| `internal/auth/auth_test.go` | `5fceb18103f2cb1ec8ced5bf79854cccaedbd732fac5de4fb8e0f1bd507c192b` |
| `internal/bot/commands.go` | `763ce129d6c160ce0ffe6364140280dbcad44c2be60fe584772b68a301d035c0` |
| `internal/bot/discordio/bot.go` | `958153fbd2017b367cf4d9bfee9f0842de7d31103994d6c7174d5962c6e5b0a0` |
| `internal/bot/discordio/bot_test.go` | `19fc4e759501aa7e9d9010094f710e0eea340aad16981b7ee7b13eef6731de7c` |
| `internal/bot/discordio/discordio_test.go` | `7bc593556d6bd7ae71c0cd2251d6d2aa3dbd0f7ead31f880c5baf335c746655f` |
| `internal/bot/discordio/interaction.go` | `bd23896158938f25316df85a41f7f8cc2719599e4814a61a303201447b78cdd4` |
| `internal/bot/discordio/music.go` | `6677c41d1848917e7718d67384739753069bdb3f57d82d17464414229184a1ea` |
| `internal/bot/discordio/wire.go` | `77678a836b80bd6fb95faafd2fdc82661eba818da178d750942ba1d1ef12d04b` |
| `internal/bot/farmimage/cellar.go` | `e10745fcd7fe90db8c12fb2b266eae249859c983720f98671afead9a46766e42` |
| `internal/bot/farmimage/cellar_test.go` | `99925428cc76f070969820f22ff1fb25823847f234dee6bdd0a8d132456135e0` |
| `internal/bot/farmimage/farm/` | `b7111f2e1eff56a27441f4f2c4b2318069c180c36431ff02328eb55d61d25b2e` |
| `internal/bot/farmimage/farmimage.go` | `5fcb21b503daeedbf381ad2bf2a3b25c775892adeebf376ed82a9561ff0b645a` |
| `internal/bot/farmimage/farmimage_test.go` | `9480b8963ecd1b09955b2dcf8bcf27a858e35ba6adbddac968a3195b40954a20` |
| `internal/bot/farmimage/memo_test.go` | `268df8771eb003325caa13707a788b02faf7601b3b33fe1a2bc31b45c28a6463` |
| `internal/bot/farmimage/pillow.go` | `47755d2852d9e0649decab39b8cfdee7d2ed3ab7ac0a1676ca2b118f85fb420a` |
| `internal/bot/farmimage/quantize.go` | `d625e299ea9b28c0575da204e2dc58ed0f3fc5f83c878cf69d01fbdae77126f1` |
| `internal/bot/format.go` | `045daadab12e907634a9fd5c175485463f8839fa198452db47108f47674ec362` |
| `internal/bot/latency/latency.go` | `22b590b542029a0f8ff74b33c71e4fad3ddfa537aa1e1f4fcb6ef77867685b0d` |
| `internal/bot/latency/latency_test.go` | `dd8f6b4595bcbd3a147f84410d74893bb1a87d39b16472a0e717ec8f002dd78a` |
| `internal/bot/pages_boss.go` | `3a47be7d88598ca6ad95b4ac19db5fc8d7747bf472cff15d158831e065476e97` |
| `internal/bot/pages_cave.go` | `4bfc528ff4696b2b01aab7d242da396f9d73b8a791beb4969ff4ff6a82427ab0` |
| `internal/bot/pages_cellar.go` | `3d10b021a490c6833bcd53a7fade5ec03fe2160458efa079ecaead9d4a7c11b0` |
| `internal/bot/pages_fishing.go` | `9aae084b3f95e58b717a1b63568eec68c4e4215d657b5a0512d2fcc49c105950` |
| `internal/bot/pages_lab.go` | `10237f4ada73c356859f402974a937080bb72fc8771a60cbb462b2b0414a87a7` |
| `internal/bot/pages_main.go` | `2289ef581486102cd4ad4b043d047b761be2ee2f1ab709e78bc52a0007fdfea3` |
| `internal/bot/pages_progress.go` | `9fcb999b900410e1cbecbc6338339ceee091eadf044e89544438f53163eba3d3` |
| `internal/bot/pages_ranch.go` | `d73f2349bfe889062b690597506e8810f85356bda716e1bff51a831e4a90d13f` |
| `internal/bot/pages_seedmaker.go` | `ec97b02255f6b0f4ee97b24a15e632284d1c8210b95c594591a80399a5b8e1d8` |
| `internal/bot/pages_shop.go` | `2daf13b39c153c0bcfc54958311ac16892bbfd1883816f1a8feb5916ec089d1d` |
| `internal/bot/pages_social.go` | `0214ea03a784de2dc76313581597585a965dd3eff21a7ff5024b68557b3100f5` |
| `internal/bot/panel.go` | `cfd4556c37d78a1c3e6e701e1cdc58099defb71d3e96babc301caa3ffcaa440a` |
| `internal/bot/panel_boss_test.go` | `6192b5d578654b227ad685479629206a1bbc17d5bfe4ef6c87f43f3bf4d821d5` |
| `internal/bot/panel_fishing_test.go` | `1d1e60f880aad39d6c7d29c1619cd953f7bca4f1708db412a4bb1e37d5c07b66` |
| `internal/bot/panel_go_features_test.go` | `cfdd4a2ac687a8863e287f65b33fd0207f7b3909fa4ba0f16e08919b9a618d91` |
| `internal/bot/panel_lab_test.go` | `7fd494500cf349c95828fb09afc725419adf05dfecde47df8b08c8fe422bbc0e` |
| `internal/bot/panel_progress_test.go` | `f2b834cd3f0f1faaa97a44e86b5dd116f90a32010738ad113ba918e4e4ac5bd0` |
| `internal/bot/panel_ranch_test.go` | `eba6ddacfe5a0eae2696a5491ce0a90d7d5b04a514613655146e4937514c0ccd` |
| `internal/bot/panel_shop_test.go` | `5a59caec9abe42e6a0c01a8612757d8728bb9146a8b9b487ef9b75efa0b19551` |
| `internal/bot/panel_social_test.go` | `b6bbbc4b1b0fad3ab3cd64c9cdc1f2540281a0e60eac133c3234c82f4e0222f4` |
| `internal/bot/panel_test.go` | `6f2df7038bb9e5b30ac385cbd6b8c993194780660717856465f3c118fa84a54a` |
| `internal/bot/querycount_test.go` | `92d9d9017ea2bb0c3bfd821ce54eafbdaeca24d201e1a12877742ae2c7cedf7d` |
| `internal/bot/render/render.go` | `3ae595db2c272fff5578b2977e68c8a0950f9797c85b4c87ca58a5bc6476a1ca` |
| `internal/bot/render/render_test.go` | `6ede2961d1547a3b6fe7538bdf6f3c9a41118f5446b9156aa377e2b437871fb8` |
| `internal/bot/runner.go` | `bb15b19e49d19344906ce84bd520ed94cadaa1dde0afa15292f58c6138cce907` |
| `internal/bot/runner_test.go` | `615c7b85dd2d538d5cffdb9e6a392fb2a236d5fbced66896742498f27ea00973` |
| `internal/bot/stress_cpu_other_test.go` | `78522fc600e79e09ea6054fc2f5b78ce2994ea89392a2aa8812c1f3069b6d134` |
| `internal/bot/stress_cpu_windows_test.go` | `e2b3c8c04414cfcea83bb195f204174de8fdbf75c1de14f377a351e7cf548225` |
| `internal/bot/stress_test.go` | `e183381e9f324a2dffa319f119880fd558fb782e1b635a00dec8cf87caf9fbcf` |
| `internal/bot/ui/ui.go` | `b28896ca48f68425ccab28404f85b908bc572119d67affe2256419a0aa9b96e4` |
| `internal/bot/ui/ui_test.go` | `15949b62a68d3ff0c16ca8bf88ef51518f095632471836514c1d606475f680f1` |
| `internal/bot/ui/uitest/uitest.go` | `35c9b49a14d7f2573911a4b8cd0b4667418c1e49a7d9436f1fbbb2477613bed1` |
| `internal/bot/ui/view.go` | `03a49adb45e886fbb12744c3795783481198f9a361829b905ef2ac4bf2d482f2` |
| `internal/cachebus/cachebus.go` | `6d5d4b825383dc1d20509d859e7ad5cf6b9f7f9aa2cfc82899c655646193053a` |
| `internal/cachebus/cachebus_test.go` | `b33d1ad0c0783797170a18aba19508056bc9229745c6ee3ab600c8d345d2a1da` |
| `internal/clock/clock.go` | `0017235591ecf1d508aecc3c361e4d7c4a51258fea9c78d7e1049e61e8a4c7be` |
| `internal/config/config.go` | `41cf3d24d28f9c41f752fb26cbef35d626ea97e0624d4cfdd708bf77a9dae88d` |
| `internal/config/config_test.go` | `1b6ad60f0ea2703b97ed3afdaf171d4fbbb6c9058e587734a54e4435d3c49989` |
| `internal/db/catalog.go` | `911a747fb253112b8f2363225e2dad82997330ed40a407edabe39f26a11c5d57` |
| `internal/db/check.go` | `abd43ee75b7c67ea966acbf7d1a26f30bf772852ef651289f5cf16eb911207b8` |
| `internal/db/db.go` | `7498451f242a1afb51dd89051550b69ac7d87205f2f2cb9b700c6b592235a326` |
| `internal/db/db_test.go` | `bf473f1a9340324c4ba48da4104b59b728bf689c9b7400a81f41fa1f31fd6543` |
| `internal/db/models/community.go` | `f7ad97323c057ea24f4f2edc59769f822794cfa38664bc2f89eb8007620415c9` |
| `internal/db/models/constants.go` | `265dc1ded98bdf6e194ccaa8c6c13ba0327ec3b81fd2007f0acf9ff9c5c68209` |
| `internal/db/models/farm.go` | `1411782b8e24b556969a57f192c256e9e000d406f33169658681a9d883f8c98b` |
| `internal/db/models/fishing.go` | `d26a44170a22956373cba1f9f934e8a3173eae62f4e7451afa0dc5117ee76243` |
| `internal/db/models/models_test.go` | `0990560e5427540bd28e77496ac1aa7b8123f9bce305da320490140e02a9669b` |
| `internal/db/models/progress.go` | `173e7aff9e5d1174ce5cf09294060462a3c9ee48c7cfe9b41cf832509654e786` |
| `internal/db/models/seedlab.go` | `2ae4b3da128ebab20cb69fc8f3094ba2c38dd24a8d0aaa0165db4f8a7254cb0a` |
| `internal/db/models/worldboss.go` | `1f2db2b61d9e2e8db289a7bc1ce0a83a27556475c599307f57750125de51acd7` |
| `internal/db/sqlutil.go` | `f7dcffee8eea287b7a0c182547300fcf6917f9ba7cb8e4d9e86eb3be2f3496b0` |
| `internal/db/txcache.go` | `9979cd0bf7e3275bcc55139caee976ee08e7ca2d61b24b2390c75ed9e4120965` |
| `internal/dbtest/dbtest.go` | `fc184bde830d3ad1df377c1c86dc8ed34512c92a3491625b1cc3846db91415ed` |
| `internal/dbtest/reset.go` | `cd8cb6650db60ec8c2870ec5a50f159d05ca7e99be04b82741ae63ffee183fdd` |
| `internal/dbtest/reset_test.go` | `db536239c940c4a4a00a0f8d75be184c8083ef52c2e77851bf14b2f86972260c` |
| `internal/dbtest/sqlcounter.go` | `beb71ca22e3ee4dae728622aa5718563b57638bdb03eb28cdf3de29fcbabb6b2` |
| `internal/game/achievements/achievements.go` | `5c9352d9c9cec8601444ae7b370aa8fe2c4773a40909d71a9e7ffc2fdc66d572` |
| `internal/game/achievements/achievements_test.go` | `36502ffa34d3f21d796e46f863b0d392ff82e3d4d1c7590b5c82c86fc6be243b` |
| `internal/game/balance/balance.go` | `bd5dc18685f0669a98daca04b0de9d7b37f2b83804f6f9f9522c81bb126c5426` |
| `internal/game/checkin/checkin.go` | `7f48d4457a07a7cea8f8ca487c385bd9a8719e833345cb8236ed76506e740d17` |
| `internal/game/checkin/checkin_test.go` | `0ca089b008745912db964774e892ac603c79494cda262f90196b07c23d72c860` |
| `internal/game/events/events.go` | `74a1ec9ec051beee22911273766377de756dbe22ccf64e9a687d3c3d7647a870` |
| `internal/game/events/events_test.go` | `06a236f059e6ed8c5896225e683fabf0865d738f7ae7afcaef752d7b1ac6ee70` |
| `internal/game/exploration/config.go` | `b17a06906169d5245c102bd8e999be7fe94e5f2a21070de5a45c74acb7b10259` |
| `internal/game/exploration/config_test.go` | `d4c0e93e51686fda42fe5bc53dc61a2ba1203ee7479dbcf189030a63d45cfb87` |
| `internal/game/exploration/exploration.go` | `118cea9a8c8320dd17085cc3b6d68084e97486b56fcae0cac011ccd1371ed9ce` |
| `internal/game/exploration/exploration_test.go` | `92c45a2caf2192c93bab90a1b9e3c66d2d1d2b709983cc7856d52bfe744a6c5e` |
| `internal/game/featurecfg/featurecfg.go` | `9df670e9c365358bf783f89ea724663a69053f6390cb846ad8c80af317b28c80` |
| `internal/game/fishing/fishing.go` | `919745950c8e31f4849dc55e09b634427552a0826eb4bcd5431f95dac5b03eca` |
| `internal/game/fishing/fishing_test.go` | `f14fd8ebcee3a92b186c95b34ab1108faf7a3555a7c35fadca6f99b723ed1338` |
| `internal/game/fishing/json_test.go` | `7613db59ac20e6f4336bbe131c006a6acbaa7289e43ed46d41072f2dcc5ff470` |
| `internal/game/gameerr/errors.go` | `a1a295c2f0e0f9ac1b431f18eea6594cf663607864234d3cae3911c234a72264` |
| `internal/game/genetics/genetics.go` | `fae958a5ab0e297960ed8d814804d97fcbe8cf0ff192bf03d0811bb7bc03dd0c` |
| `internal/game/genetics/genetics_test.go` | `6d5915b5412a42e2f6941200ffa827c026b175ff24f361dce19d659433192eca` |
| `internal/game/mushroom/config.go` | `f0e38fc0d82368f0b0d3da8b1cfa7ca281490ae5c9dd2dea7fdd56e4e1fac0b1` |
| `internal/game/mushroom/config_test.go` | `7b7c8d91c598802069508db4c51b57c40af59f29d89bc88fbad5ecdec48ddd7c` |
| `internal/game/mushroom/mushroom.go` | `eef8004f2801bd1cb80b6533a2fbcd347164f864f25bac56baa5baa2c14d9c0a` |
| `internal/game/mushroom/mushroom_test.go` | `b1ad19c7a8b70e338d9b29ee91359fbd5af38fa797cf4dff6ff864e72b04b788` |
| `internal/game/news/news.go` | `0ed4d661bd9147bb528a30ead5bca82b628129d92ffba6134dd9eef8d5ab3fea` |
| `internal/game/news/news_test.go` | `560577fc7d61efc97ab45f69444a995d5177258a2277f890d6ce3b600a894a15` |
| `internal/game/news/pyrandom.go` | `d42860357a6401ee709b1dc6a98c40c21d81be1428f9776b8ffb91e1130a82fe` |
| `internal/game/news/testdata/python_reference.json` | `77d34f8afea0ffe52d7609883be0791e185e197c38f2be509bd2ae926e53d5ef` |
| `internal/game/pets/pets.go` | `ff6b707dc8ae1ef0bc3f76fd707dd764cb522311ce0169e33058c1965b3922ae` |
| `internal/game/pets/pets_test.go` | `31e5e5f5ea0a7344adf6a09ee0da4c27e8baa60c33af3115cd9cc1ed52deb8d3` |
| `internal/game/ranking/ranking.go` | `44463a83831bf19ccf83d0c33157b354bca4539f8b81910d6996fe8deb329f6b` |
| `internal/game/ranking/ranking_test.go` | `4d8b0cb1e2a1aa23a212531655100e0f135e0ffc8ad4ef81836d72f999fde97e` |
| `internal/game/ruins/ruins.go` | `da90bf8b9728818c3cdbf52951d79f7e1c0674d5549572848ce8595229e116c1` |
| `internal/game/ruins/ruins_test.go` | `cd832a43e62d3d3263eb281a0718362780b62714f932f54e5d7f24bad6daf3c9` |
| `internal/game/rules/rules.go` | `097bd7a424898b6925d537d5e6e31f42e33420f35cbf8f4f393e1b336b1f5605` |
| `internal/game/rules/rules_test.go` | `15a50ff32878f023b6ef40aba46da086e51defd5ad01d0b2e95dc1a8c7af76e2` |
| `internal/game/seasons/seasons.go` | `6f1d57e7d00a32cd3204044359c9f30909e9ad8196b7979ee7e771f5a72a64ec` |
| `internal/game/seasons/seasons_test.go` | `0969bac440644fd687fe7d97ee07858c258aea572611bc18881cffff7de2db8d` |
| `internal/game/seedlab/config.go` | `970dc02379809d1c406c4fdeb9884b07037bb02cb29f49998e0be6f15bc95732` |
| `internal/game/seedlab/seedlab.go` | `0d3d38bf3bbb4901ef54a9c0f6dd6e87ef842fead1ba9111638ec145d0dc8b46` |
| `internal/game/seedlab/seedlab_test.go` | `9239908cb5c1c21610c371f1d85d8ef3e6eb1e9c1db846e197b944d17450ebf1` |
| `internal/game/specialshop/specialshop.go` | `2fec3d9bb244583b748a884e9c8566837bed59ba9d889323b10d241ce3e88120` |
| `internal/game/specialshop/specialshop_test.go` | `b7dbccb2994dfdb8f2a49731b417eaa79c6249edad3a45fd0d480d2e81276590` |
| `internal/game/worldboss/worldboss.go` | `bf48640f807809086b67a7788de6cf194c4192bb2f711adaf182f5324c5523dc` |
| `internal/game/worldboss/worldboss_test.go` | `ecf564a6af0ad5184b6787a03e4a06651374fc86316473e216dbc78a22bcaec2` |
| `internal/music/commands.go` | `247f82de1ca87ae5428c1becdbd69e369f0d8711c519cc07ed3ad64af4eeeeae` |
| `internal/music/format.go` | `e13108c2306f10d9d5b194aa5c618ceb7c9183650485d6ac1e47b6a5e8636ba0` |
| `internal/music/lavalink.go` | `6287e01748898a9671e38f2c9bef34f5a532411d761a5c727f65fba3e4125bb5` |
| `internal/music/lavalink_test.go` | `2a9245988e6e6d554f3a5f69457e4370550d3879f929ffcfbd5a5a47e0c5ef8d` |
| `internal/music/music.go` | `0db721a7b213663a2dedaeb1ac58cd7eb4cfa71fed9f49d8dfc89a05cd7a4251` |
| `internal/music/music_test.go` | `c3ddb2175800cf276672ac41a76f35b0804a06f7f8562e0a74dbba35e7e597e2` |
| `internal/music/spotify.go` | `e9fd065a5162df93269849e5a7eadd033f1d1e90fa4364e444563cf532a60a42` |
| `internal/music/stress_cpu_other_test.go` | `f9ed9dafd9c7f33ca0fab9dec10376a4c1141ccff461741f154bcee8f4f0646d` |
| `internal/music/stress_cpu_windows_test.go` | `f732255002c997712229f6159e351cb104fb6c09581b9191f14ea9ad6c9412ff` |
| `internal/music/stress_test.go` | `46dad0c6fd8fbdcffae29906be78f05f8996d43da0869cadd1ceea4e5ba2cee3` |
| `internal/numfmt/numfmt.go` | `792645f7d8b56d8dd1650b4daff3d696bce1a7af2c6c6fc4a8982fa0cdfe5202` |
| `internal/repositories/animals/animals.go` | `12d48b0012c10a7905e4090480a7e7328e885fb18686d2b492b685c724c6c2fc` |
| `internal/repositories/animals/animals_test.go` | `60a62222f56db2a60d505504255c9f86d1754daa10cd58058b307be00c986b0a` |
| `internal/repositories/catalog/catalog.go` | `55d5ae064eca7c9c05424e2c149f9394582fbb6c96851c40f0d48d1af04bb116` |
| `internal/repositories/catalog/catalog_test.go` | `ee05bc9ccfe990a416cc9a38269161474eca1b6bf8972d0c2bae3fc84a9655c1` |
| `internal/repositories/codex/codex.go` | `4d95da61f821ac820cb7be49af43e9dd659afcb4c487c9b70dea98f7014ee6aa` |
| `internal/repositories/codex/codex_test.go` | `31cfed5a5286323ce4a1adbe3c36e02d39818172c53d5b65528ba821be976637` |
| `internal/repositories/codex/helpers_test.go` | `f1c916ec4707690601a670c6b8a327e22babafb258cf13c07ad721126e3e8000` |
| `internal/repositories/community/community.go` | `f3343f90c52a9dcc49efb5b328188df76bd686e5b3125dc9eaaf5ec53d9c2d21` |
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
| `internal/repositories/feedback/feedback.go` | `a6b0a650e46791c1929bd58c230c8f0e56e6018242f270113710fcee5979be3a` |
| `internal/repositories/feedback/feedback_test.go` | `f01231153814146d916e7e7bef0087293c81ccea02f52620a81c7a2dc3ea108d` |
| `internal/repositories/feedback/helpers_test.go` | `c933b0883ca1d868383c0178069d7edf113ccdc4493e4302f13a88a5a1e3e576` |
| `internal/repositories/fishing/fishing.go` | `783712a0c1d3570c708afe667c3a6e52eb4bd9b8230afeaaef201b1a3bffbd99` |
| `internal/repositories/fishing/fishing_test.go` | `468ebd9680ceeed64a0ed6628784c342a1ceca96cbb67539c5526b3ff6f9a5df` |
| `internal/repositories/fishing/helpers_test.go` | `5b57f1575ebac144edca42fe126c61025a96de2c12b418a63a51b8aa59f275c5` |
| `internal/repositories/genetics/genetics.go` | `de17b8d1184a90ad58cf25f664bb47d793fb6ec30fcd22564b8d6a9ddac9de2b` |
| `internal/repositories/genetics/genetics_test.go` | `9e90d4abccbf911468979d4f7d7ec8541d960cb1c696b3b416974b1f435ebcfb` |
| `internal/repositories/groups/groups.go` | `69cdf1a4f04858500051377a8dfec7b89bc2c471689bbda5ccc1d82b766b205c` |
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
| `internal/repositories/pets/pets.go` | `d7cbaa8130832ae7fa2873a1fcc4a032a35ba8c28a1040881ce7157a512559de` |
| `internal/repositories/pets/pets_test.go` | `dfeb57cdb5ca49ceeaeede01cddaf16c8fbeee794e28c5bc4381846436b8a5df` |
| `internal/repositories/players/players.go` | `3d1af057d288516c0784f1b7c20abf031f4eb64a717b270fe9d9422042ec2a92` |
| `internal/repositories/players/players_test.go` | `3acaa8cc176bf1189217df29d78c25e0109dead2b0729d52d9c66fa482d94f62` |
| `internal/repositories/ranch/ranch.go` | `6a3f78fea6ee6510fac8ce22c22f3cf7bae5f7e518b23522cb20f8429c0dea2c` |
| `internal/repositories/ranch/ranch_test.go` | `797788b5a094fecb56fa1778b7d155fb60dac442ccf9f69bc596ec5e1fe84171` |
| `internal/repositories/repotest/repotest.go` | `a601b0968995a101a7b76ddb9d9b218a2a2b302c5970aa2bb179fd68f6d205e8` |
| `internal/repositories/ruins/helpers_test.go` | `30c3557b5a85fd1ccad49cc42cea448a3cfd932c56f9b22c22eb4d71e94a851d` |
| `internal/repositories/ruins/ruins.go` | `fb6216228ac5705e54af8800335ddcf67dcb4db8a28e39af065261baeaf9866c` |
| `internal/repositories/ruins/ruins_test.go` | `80c7cb737ada998023ea59a24f6f3ebe25de0719d975e4ff55f6d2d28c9a8210` |
| `internal/repositories/rumors/helpers_test.go` | `62b82f519c632c6742e984f60bfd20014395e035d0860e4ccaf5c072f18d25e5` |
| `internal/repositories/rumors/rumors.go` | `0c36135543fc82dad324e5aa03e06b0f6cceb1a66f99f5028f488e1aa6c27ff6` |
| `internal/repositories/rumors/rumors_test.go` | `cfde7b1848d056e72383940b55fe8f7d689eee30ba55eb15f7a104728188e58e` |
| `internal/repositories/seedlab/seedlab.go` | `8a7677c845ad334f4a7134a4bbdb5aaf4c5effd753b6d74e9ee7cc1e7bbb1db9` |
| `internal/repositories/steals/steals.go` | `97073442f679954790cd7b1da67fbedf0bab375027775d5559b7d715557e659c` |
| `internal/repositories/steals/steals_test.go` | `744eb5944b865e72c4fe9a10b9bdb3c5484e81dfa4ff34629c963f2afa84b18d` |
| `internal/repositories/titles/helpers_test.go` | `177beadee36177f47abad097d60f225d6f964274b36630711a401d14ef97964a` |
| `internal/repositories/titles/titles.go` | `36acc0704bf9b1d16a5e0d50f5903e03d9808525126114ff08e7f2a0699389f0` |
| `internal/repositories/titles/titles_test.go` | `5ab3a8f591e7dc7aea3f5df77b81ef51b6741a3821f9938fb4b67ee1122d7a08` |
| `internal/repositories/transactions/transactions.go` | `df76708abd94cc6a215a9593e54ede986ea48ed3da506015eb536ba5aec1abec` |
| `internal/repositories/transactions/transactions_test.go` | `f95ae56b518fcfbb28012ce301c4cd1519e9505ccc932bb84cc6583bfaf135fc` |
| `internal/services/activity.go` | `23e413d6e742b94fe8aff2ceab67d5b9df7395efee71439ed52547665b33e7b2` |
| `internal/services/admin.go` | `f84034bf22e6e33b997bc2a97f5e4ac10b7ffe9dafb6a556fed80a01eea9d48e` |
| `internal/services/admin_test.go` | `d38f2678a219857f1f69494a95d732b3477908988beb5e369ae3dc7a2e04df47` |
| `internal/services/backpack.go` | `a07a017ee7cdd22266d0cbe3fb257f3f06ca83cfc87da743df0343bd0036297d` |
| `internal/services/batch_test.go` | `9e04362497997f348edce7bade3a8d600e5f4a8521e2f34322bf6ff343b1bd49` |
| `internal/services/bulk_test.go` | `5245508d3035337a6d3833b589793430aeeaacd70931fc232186695912162265` |
| `internal/services/catalog_seasons_test.go` | `15103cb7e8a7eec205de723be1670ff34bf0aa8687f5213c67fac13b225ccb23` |
| `internal/services/community.go` | `17a8ad9f38168b633a8f095e84a968b723dde561820ab8f8446997e6c1261239` |
| `internal/services/community_test.go` | `fab57c9a96c7cab7bef3ceaa413cecf0ace86b9089f9a6256aaa5f1d77317eb2` |
| `internal/services/crops.go` | `093dd50dc9a97861e173ca3cb34a0d83df213c49b416f6796b9a68a2bd0bda74` |
| `internal/services/database_test.go` | `7f9087209119e70b912f4ac1a70e43014276d3a8b211db9b6645982d201f83c3` |
| `internal/services/defense_test.go` | `64f1a9fe08a69da86ea97cb31b7c4b8fa1e501ea093184d019ec8099a740fb1e` |
| `internal/services/dto.go` | `f629192c9cf3ae18bae272345d94894ae85b720c77b33cbc36af432be4d7a416` |
| `internal/services/exploration.go` | `131a0d34eb7a0f656fdcd3bd5b9f4a87cb0408ea766e2f39677287dcb321c07f` |
| `internal/services/exploration_test.go` | `669399889a2b161a3117ba54d25e16cad0894220b4badde4a294f4e324d6e553` |
| `internal/services/farmstats_test.go` | `a15446358f01b77a334540f6511072af77a2c0681298e0479957c3118c89d819` |
| `internal/services/feedback_test.go` | `a9554d2d3afd0e0d815cafa637231c329695a9b5f3ead025cc20f23ddc5febb1` |
| `internal/services/fishing.go` | `cb722313aaac083ee873a2cc58a01bb9dd2bb7185d9bc146fc7a794155cd983f` |
| `internal/services/fishing_test.go` | `2ad6fb060efa7a8253b5ca0850aeff4bd07c11188f07b815ce1893d5ccddf738` |
| `internal/services/format.go` | `bd7dce0cad0765f8cb5f4198af888e52848e0ee2fadef9c0bf688bcecde559fa` |
| `internal/services/gamble.go` | `190167383b01061c0055528b0fc7d0a793b95abee6c965cbf639f066d2b9802d` |
| `internal/services/gamble_test.go` | `88a175c8b93c4d5d390bfbd1c48f66737dddfd199dc7d0ed7159e5217d21715e` |
| `internal/services/game_service_test.go` | `636fdb77e647d96d10ff9f9d5d021701584f1d9f052a152cf6fe6b4439663008` |
| `internal/services/genetics_test.go` | `2e50fe222ddbcce44620c4f727c6d21190ce1016b1b64c8cbd3d434849c884bc` |
| `internal/services/go_features_test.go` | `ec4572d6da91a8c946c9750877edee87dab4e6fa5f4688b5a19aa766690eacc3` |
| `internal/services/group_test.go` | `d3ef99ebc8ac4ec2929755c137f76a8f2d2840e9765861badb2e06107acb3f3a` |
| `internal/services/helpers_test.go` | `51bdcba9ae26e2bffc8ac309dadcdc8f4ae05880bb4ad7a8565227c87888165c` |
| `internal/services/items.go` | `2ba822ebe09c4ca3a86b32118354ccf3e0823890fac9a0d1bec133248e261cf3` |
| `internal/services/items_test.go` | `98f56e61be399dcf272d170ae972d81f3615920f6f697cdbcd106be5702b1d25` |
| `internal/services/jam_test.go` | `5a225d924a0fee9a7a790f5d82777570a14b00aa2ddc7ee31d851be7eb5a10c8` |
| `internal/services/luxury_test.go` | `2730e693f4f50c37a8929a708e70921a85ee27e8aa137266ff4bfb7c6bc2dfad` |
| `internal/services/monitor.go` | `3d31620cad46b9d3383a0ca3833fa10d3383b5d923ad80d3f531de7c182b1a6f` |
| `internal/services/monitor_more.go` | `295e3a6404296592391953e903fc48d9996abf7e015184a8ef6994b554761461` |
| `internal/services/monitor_more2.go` | `9bcf88fce251713494c1d377204e4b29b682653037582a7b0fe50d725e77beb2` |
| `internal/services/monitor_test.go` | `9490a74814411c190cbd075b5bfc181f4c3d63aaf1b862320b653fd5453eb5d9` |
| `internal/services/mushroom.go` | `989d2e2c69396db9ebce8f10dbfaf394f87aec50f5f4a6e4963592a908504ef7` |
| `internal/services/mushroom_test.go` | `54ecd06992061843c036e439b1c80f33f499e39d50885357f0e371414bad22b7` |
| `internal/services/news.go` | `f3ab1d22c1b940dc00f6cac9209b192f3f0d57c9f01aae3ee788a7a3c449d7b2` |
| `internal/services/news_test.go` | `5914ac31c9b81dfcc389854c20493863c7020e45b428b76b33116fc924c41b25` |
| `internal/services/ordered.go` | `24ab9b4f5a9d0146fd4e36e5bbc55f6d8b96d0af61817c5a4c8b96ecc6558df3` |
| `internal/services/perf_test.go` | `9c6939c77bc3728f3d14f7b1527ebb7b50376f3c670d2d1aea97e5cf66c76852` |
| `internal/services/pets_test.go` | `e76226dc5f9e29202606aba4ca93dfef6f7efdd25e8191fc0d629ba1b42ac311` |
| `internal/services/players.go` | `c8071cb54eb672932a79f51ca7f2fa3d932088a82c4da487c914af87a8d335ea` |
| `internal/services/progress.go` | `b42270688252fd7b3dadc42cd4be97e3eacd4c7c48279e61ca7f13c09f2ce237` |
| `internal/services/progress_admin.go` | `7c356b9f00b3f9285afc6b300925299d61161e796aed8b3f80fcf1bc2a922ec6` |
| `internal/services/progress_admin_test.go` | `3b72e56f82031e428f9cf994200db7621a177cf8bfb938e742f32c7523d0ea07` |
| `internal/services/progress_test.go` | `79973fb534b38f9fccf50c5841711127da18f5030d4e89eccabb0edbb204d540` |
| `internal/services/ranch.go` | `821b681e9f01543a39aecd231c6998ea1d1fc6aaefce2acc23222c39064bf8fb` |
| `internal/services/ranch_test.go` | `1876e456d3a126ab5d523b7dd5c9b9b96d4911206136af75ac1a21632803bd5e` |
| `internal/services/ranking.go` | `72de8253cab387d6ea8ac7b09cfe9b8d069536e0eab788714c6219ef9cf0bda5` |
| `internal/services/ranking_test.go` | `0701621de69f1f78ef54108dc123efa9c5f917f9875463da86f74801f01b8c94` |
| `internal/services/retry.go` | `ba146e2ae2ceca83016f2af28ee78a44af39f946325459e372dc994c267c5385` |
| `internal/services/ruins_test.go` | `80565e5b64a40c7d283bb7ecfaad5cafd3c4a1ac34853a216d38d609618d40d6` |
| `internal/services/seedlab.go` | `2a21f0486a6ff3af14a2d2d4159e5d7ce9b7f9d4ba9a6bf33796d484fd7ffe56` |
| `internal/services/seedlab_test.go` | `3d35f87dc73bc096e6b7082a42c21666813ca8d7e707edbdb61fa1663fbc9f71` |
| `internal/services/service.go` | `06198d84336364d22dfd85805600d22d8cca76e3a20fc413ae612af36f8e9187` |
| `internal/services/site.go` | `534b9ecf318a955775d7049acc6445726601378cd91001b931e2cf5a9f8e7262` |
| `internal/services/social.go` | `d3e32dcf63cba29196947bbc7a8b640c99baf56c820d719614fb9c5028221709` |
| `internal/services/social_test.go` | `65f6371c11236d0837f6144494da0fad58fa23d8db38c6b6ac28144821a755b0` |
| `internal/services/stress_test.go` | `a4aa847f169673c1d8af3656a9508f25c226415f50f465f141f546e025ec2e8f` |
| `internal/services/svctest/svctest.go` | `e4072ad8b251f44494dcb81366a8cd78ffc4c73071984385239b5d49b1f07ebd` |
| `internal/services/worldboss.go` | `7eba30ea237b67f0925b667ad91ef482d4e5a2f93dc191249ce030aa2c2c014e` |
| `internal/services/worldboss_test.go` | `6d67a18555fe0fc42247a4c06a121167f83e89b1af776d1fdf89fe08313adaca` |
| `internal/site/site.go` | `0a9e3ca033e155902bed4bebf92dd2bc1ad0d7950d8869941e6a34f5947a70d2` |
| `internal/site/site_test.go` | `d32d811fab425aec97b03c61863775249c64c2b8d544bd1683d439b109aea457` |
| `internal/site/static/favicon.svg` | `bba4123b033483f9ca309a1031c36a6459679843def75245bca83d74575bc292` |
| `internal/site/static/index.html` | `bb6108f2eb58f9fbc221f5473adfc4bc5e7143c632fbe80999d6f1cd094f2d60` |
| `internal/site/static/site.css` | `b6542200f867362330ba84dbb417878761f75a886b3af7c18681a2300d2b4ee7` |
| `internal/site/static/site.js` | `e5a0a33e6290715da656ea02ef3687d2130b9ccf817871d2af44282a69b06cb0` |
| `internal/web/admin.go` | `fff4ac767c289753fdbe578ecfd142d6a345498e7f5dced26b14c3f41ed1c050` |
| `internal/web/admin_boss.go` | `a588cb805d2739bcaa5fefc6cb5c5abc7d066d70bc6abdb14ba94efa2513924f` |
| `internal/web/admin_boss_test.go` | `749a86128ae3d09ad2b41ab37cd127a35e56cd5b4ec6bed8edd98649f35e9919` |
| `internal/web/admin_catalog.go` | `140c32bfc5f42765d2f8d0b4b0c84284fd932263160c918a290cdaedb2ea17a8` |
| `internal/web/admin_players.go` | `ed208822dde3d3747b8a20c25828c07e480d01bebe67dacdd51641072b40ec33` |
| `internal/web/admin_test.go` | `b5f15ff103662029a1aee46b47e51477b5b00bf15598674a217a9b9ce3bd13c8` |
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
| `internal/web/static/monitor/app.css` | `1ea9816a5f7f3b24d2b05704e6624ef2c60be627d593845f1e9fa7e53188ed5a` |
| `internal/web/static/monitor/app.js` | `8b15e899794dec0ce0dcccfd7d1cda375044afdff5fc9e92cda686c168df5ae4` |
| `internal/web/static/monitor/index.html` | `900ccf024e89c1a89ab3c92b742bb5a6240e19cadd5993a7b2fd7f45711e5428` |
| `internal/web/web.go` | `2eaef4fc10ef26c31b7a977e5b1d411cbc6edb4c0c753e1215f028a95fadfd22` |
| `internal/web/ws.go` | `dda2e1fd64b2f7f6adb5d31c531feebfb0e0956b3ea5a2ed7e93863c39bd8b9c` |
