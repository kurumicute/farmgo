package models

import (
	"reflect"
	"sort"
	"testing"

	"farmgm/internal/game/rules"
)

func TestEventTypesMatchRules(t *testing.T) {
	if len(EventTypes) != len(rules.EventTypeOrder) {
		t.Fatalf("數量不同：models %d、rules %d", len(EventTypes), len(rules.EventTypeOrder))
	}
	a := append([]string(nil), EventTypes...)
	b := make([]string, len(rules.EventTypeOrder))
	for i, e := range rules.EventTypeOrder {
		b[i] = string(e)
	}
	sort.Strings(a)
	sort.Strings(b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("事件類型不一致：\n%v\n%v", a, b)
	}
	for _, e := range rules.EventTypeOrder {
		if _, ok := rules.EventTypeLabels[e]; !ok {
			t.Errorf("rules.EventTypeLabels 缺少 %s", e)
		}
	}
}

// allModels 全部 65 個資料表模型（Python 60 個＋種子實驗室 5 個）。
var allModels = []Table{
	Player{}, CropType{}, AnimalType{}, Farm{}, FarmStats{}, FarmDailyStats{}, FarmPlot{}, InventoryItem{},
	GamblePrize{}, GameTransaction{}, Group{}, GroupMember{}, FarmTool{}, FarmExploration{}, ExplorationLog{},
	FarmEffect{}, StealRecord{}, FarmDefense{}, FarmBuilding{}, FarmAnimal{}, CraftOrder{},
	AdminAction{}, Quest{}, QuestContribution{}, QuestClaim{}, Competition{}, CompetitionContribution{},
	CompetitionResult{}, CompetitionAward{}, CropGeneticsSetting{}, GameEvent{}, RankingRewardRule{},
	RankingSettlement{}, RankingAward{},
	FishType{}, FishingGear{}, FarmFishingGear{}, FishingSetting{}, FishingSession{}, FarmFish{}, FishShare{},
	FishingGuard{},
	WorldBossSchedule{}, WorldBossEvent{}, WorldBossSupply{}, WorldBossContribution{}, WorldBossRewardClaim{},
	DailyCheckin{}, FarmCodexEntry{}, FarmAchievement{}, FarmTitle{}, WeeklyShopPurchase{}, FarmPet{},
	FeatureSetting{}, FarmFeedback{}, GroupRuins{}, RuinsContribution{}, RuinsGuardian{}, RuinsGuardianHit{},
	RumorDig{},
	SeedLab{}, LabSeed{}, LabJob{}, LabRecord{}, LabSeedShare{},
}

func TestEveryFieldHasDBTagAndTablesUnique(t *testing.T) {
	if len(allModels) != 65 {
		t.Fatalf("模型數量 %d，應該有 65 個資料表（Python 60 個＋種子實驗室 5 個）", len(allModels))
	}
	seen := map[string]bool{}
	for _, m := range allModels {
		name := m.TableName()
		if seen[name] {
			t.Errorf("重複的資料表 %s", name)
		}
		seen[name] = true
		typ := reflect.TypeOf(m)
		cols := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("db")
			if tag == "" {
				t.Errorf("%s.%s 沒有 db tag", typ.Name(), typ.Field(i).Name)
			}
			if cols[tag] {
				t.Errorf("%s 重複的欄位 %s", typ.Name(), tag)
			}
			cols[tag] = true
		}
	}
}
