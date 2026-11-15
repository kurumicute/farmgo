package hourglass

import (
	"testing"
	"time"

	"farmgm/internal/game/balance"
	"farmgm/internal/game/featurecfg"
)

func TestHourglassConfig(t *testing.T) {
	featurecfg.Reset()
	defer featurecfg.Reset()
	if balance.ItemPrice(balance.ItemHourglass) != 500 || Cut(time.Hour) != 30*time.Minute || Usable(59*time.Second) || !Usable(time.Minute) {
		t.Fatal("預設：500 金、剩餘時間減半、剩不到 1 分鐘不能用")
	}
	raw, _ := featurecfg.DeepCopy(defaults())
	raw["price"], raw["cut_pct"], raw["min_seconds"] = 800, 25, 0
	v, err := featurecfg.Validate(ConfigKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	featurecfg.SetCurrent(ConfigKey, v)
	if balance.ItemPrice(balance.ItemHourglass) != 800 || Cut(time.Hour) != 45*time.Minute || !Usable(time.Second) {
		t.Fatal("設定要生效")
	}
	for _, bad := range []func(featurecfg.Value){
		func(v featurecfg.Value) { v["price"] = 0 },
		func(v featurecfg.Value) { v["cut_pct"] = 100 },
	} {
		raw, _ := featurecfg.DeepCopy(defaults())
		bad(raw)
		if _, err := featurecfg.Validate(ConfigKey, raw); err == nil {
			t.Errorf("應該拒絕：%v", raw)
		}
	}
}
