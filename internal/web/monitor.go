package web

// 唯讀監控 API（對應 Python app/api/monitor.py）。不提供任何修改遊戲資料的端點（寫入請見 admin.go）；
// 只註冊 GET，其他方法由 ServeMux 回 405。

import (
	"net/http"
	"strings"

	"farmgm/internal/services"
)

func (a *App) monitorRoutes() {
	m := a.mux
	m.HandleFunc("GET /api/monitor/summary", a.getSummary)
	m.HandleFunc("GET /api/monitor/farms", a.listFarms)
	m.HandleFunc("GET /api/monitor/farms/{farm_id}", a.getFarm)
	m.HandleFunc("GET /api/monitor/transactions", a.listTransactions)
	m.HandleFunc("GET /api/monitor/charts", a.getCharts)
	m.HandleFunc("GET /api/monitor/players", a.listPlayers)
	m.HandleFunc("GET /api/monitor/groups", a.listGroups)
	m.HandleFunc("GET /api/monitor/groups/{group_id}", a.getGroup)
	m.HandleFunc("GET /api/monitor/steals", a.listSteals)
	m.HandleFunc("GET /api/monitor/defenses", a.listDefenses)
	m.HandleFunc("GET /api/monitor/leaderboard", a.getLeaderboard)
	m.HandleFunc("GET /api/monitor/players/{player_id}/backpack", a.getBackpack)
	a.monitorRoutes2()
	a.monitorRoutes3()
}

func (a *App) getSummary(w http.ResponseWriter, r *http.Request) {
	v, err := a.monitor.Summary(ctxOf(r))
	respondValue(w, r, v, err)
}

func (a *App) listFarms(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	q := p.queryStr("q", 40)
	limit := p.queryInt("limit", 50, 1, 200)
	offset := p.queryInt("offset", 0, 0, 100_000)
	if !p.ok() {
		return
	}
	v, err := a.monitor.Farms(ctxOf(r), strings.TrimSpace(q), int(limit), int(offset))
	respondValue(w, r, v, err)
}

func (a *App) getFarm(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	id := p.pathID("farm_id")
	if !p.ok() {
		return
	}
	v, err := a.monitor.FarmDetail(ctxOf(r), id)
	respond(w, r, v, err, "找不到這座農場。")
}

func (a *App) listTransactions(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	limit := p.queryInt("limit", 20, 1, 100)
	if !p.ok() {
		return
	}
	v, err := a.monitor.Transactions(ctxOf(r), int(limit))
	respondValue(w, r, v, err)
}

func (a *App) getCharts(w http.ResponseWriter, r *http.Request) {
	v, err := a.monitor.Charts(ctxOf(r))
	respondValue(w, r, v, err)
}

// ---------- 群組、偷菜、排行榜、背包（唯讀） ----------

func (a *App) listPlayers(w http.ResponseWriter, r *http.Request) {
	v, err := a.monitor.Players(ctxOf(r))
	respondValue(w, r, v, err)
}

func (a *App) listGroups(w http.ResponseWriter, r *http.Request) {
	v, err := a.monitor.Groups(ctxOf(r))
	respondValue(w, r, v, err)
}

func (a *App) getGroup(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	id := p.pathID("group_id")
	if !p.ok() {
		return
	}
	v, err := a.monitor.GroupDetail(ctxOf(r), id)
	respond(w, r, v, err, "找不到這個群組。")
}

func (a *App) listSteals(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	q := services.StealQuery{
		GroupID:  p.queryOptInt("group_id", 1, MaxBigint),
		PlayerID: p.queryOptInt("player_id", 1, MaxBigint),
		Outcome:  p.queryEnum("outcome", "", "success", "blocked"),
		Limit:    int(p.queryInt("limit", 50, 1, 200)),
	}
	// 日期以台灣時間解讀；date_to 包含當天
	from, to := p.queryDate("date_from"), p.queryDate("date_to")
	if !p.ok() {
		return
	}
	if from != nil && to != nil && from.After(*to) {
		writeDetail(w, http.StatusUnprocessableEntity, "開始日期不可晚於結束日期。")
		return
	}
	q.Since = from
	if to != nil {
		until := to.AddDate(0, 0, 1)
		q.Until = &until
	}
	v, err := a.monitor.Steals(ctxOf(r), q)
	respondValue(w, r, v, err)
}

func (a *App) listDefenses(w http.ResponseWriter, r *http.Request) {
	v, err := a.monitor.Defenses(ctxOf(r))
	respondValue(w, r, v, err)
}

// getLeaderboard scope=groups 為群組總排行；period 為台灣本日／本週／本月（money 改為期間作物販售收入）。
func (a *App) getLeaderboard(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	scope := p.queryEnum("scope", "global", "global", "group", "groups")
	kind := p.queryEnum("kind", "money", "money", "sales", "goods")
	groupID := p.queryOptInt("group_id", 1, MaxBigint)
	period := p.queryEnum("period", "all", "all", "day", "week", "month")
	limit := p.queryInt("limit", 20, 1, 100)
	if !p.ok() {
		return
	}
	if scope == "group" && groupID == nil {
		writeDetail(w, http.StatusUnprocessableEntity, "群組排行榜必須指定群組。")
		return
	}
	if scope != "group" {
		groupID = nil
	}
	v, err := a.monitor.Leaderboard(ctxOf(r), kind, groupID, int(limit), scope, period)
	respond(w, r, v, err, "找不到這個群組。")
}

func (a *App) getBackpack(w http.ResponseWriter, r *http.Request) {
	p := newParams(w, r)
	id := p.pathID("player_id")
	if !p.ok() {
		return
	}
	v, err := a.monitor.Backpack(ctxOf(r), id)
	respond(w, r, v, err, "找不到這位玩家的農場。")
}

// ---------- 動物養殖與加工、群組任務與競賽、管理紀錄（唯讀） ----------

func (a *App) monitorRoutes2() {
	m := a.mux
	m.HandleFunc("GET /api/monitor/ranch", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.Ranch(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/craft-orders", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		include := p.queryBool("include_collected", true)
		limit := p.queryInt("limit", 50, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.CraftOrders(ctxOf(r), include, int(limit))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/quests", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		groupID := p.queryOptInt("group_id", 1, MaxBigint)
		limit := p.queryInt("limit", 50, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.Quests(ctxOf(r), groupID, int(limit))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/quests/{quest_id}", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("quest_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.QuestDetail(ctxOf(r), id)
		respond(w, r, v, err, "找不到這個任務。")
	})
	m.HandleFunc("GET /api/monitor/competitions", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		limit := p.queryInt("limit", 50, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.Competitions(ctxOf(r), int(limit))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/competitions/{competition_id}", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("competition_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.CompetitionDetail(ctxOf(r), id)
		respond(w, r, v, err, "找不到這場競賽。")
	})
	m.HandleFunc("GET /api/monitor/admin-actions", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		limit := p.queryInt("limit", 50, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.AdminActions(ctxOf(r), int(limit))
		respondValue(w, r, v, err)
	})
	// 作物品質與突變、目錄、單一玩家的管理資料
	m.HandleFunc("GET /api/monitor/genetics", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.Genetics(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/genetics/stats", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.GeneticsStats(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/catalog", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.Catalog(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/players/{player_id}/admin-farm", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("player_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.AdminFarm(ctxOf(r), id)
		respond(w, r, v, err, "找不到這位玩家的農場。")
	})
}

// ---------- 釣魚、排名獎勵、世界 Boss（唯讀） ----------

func (a *App) monitorRoutes3() {
	m := a.mux
	m.HandleFunc("GET /api/monitor/fishing/summary", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.FishingSummary(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/fishing/players", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		q := p.queryStr("q", 40)
		limit := p.queryInt("limit", 20, 1, 100)
		offset := p.queryInt("offset", 0, 0, 100_000)
		if !p.ok() {
			return
		}
		v, err := a.monitor.FishingPlayers(ctxOf(r), strings.TrimSpace(q), int(limit), int(offset))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/fishing/players/{player_id}", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("player_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.FishingPlayer(ctxOf(r), id)
		respond(w, r, v, err, "找不到這位玩家的農場。")
	})
	m.HandleFunc("GET /api/monitor/fishing/catalog", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.monitor.FishingCatalog(ctxOf(r))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/ranking-rewards", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		limit := p.queryInt("limit", 30, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.RankingRewards(ctxOf(r), int(limit))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/ranking-settlements/{settlement_id}", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("settlement_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.RankingSettlement(ctxOf(r), id)
		respond(w, r, v, err, "找不到這筆結算。")
	})
	m.HandleFunc("GET /api/monitor/world-boss", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		limit := p.queryInt("limit", 50, 1, 200)
		if !p.ok() {
			return
		}
		v, err := a.monitor.WorldBoss(ctxOf(r), int(limit))
		respondValue(w, r, v, err)
	})
	m.HandleFunc("GET /api/monitor/world-boss/{event_id}", func(w http.ResponseWriter, r *http.Request) {
		p := newParams(w, r)
		id := p.pathID("event_id")
		if !p.ok() {
			return
		}
		v, err := a.monitor.WorldBossDetail(ctxOf(r), id)
		respond(w, r, v, err, "找不到這場世界 Boss 活動。")
	})
}
