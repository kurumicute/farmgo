package featuresettings

import (
	"testing"
	"time"

	"farmgm/internal/db"
	"farmgm/internal/game/featurecfg"
)

func TestSaveRefreshRemove(t *testing.T) {
	d := setup(t)
	featurecfg.Register("go_test_key", "測試", func() featurecfg.Value { return featurecfg.Value{"n": 1} },
		func(v featurecfg.Value) (featurecfg.Value, error) {
			n, err := featurecfg.IntValue(v["n"], "n", 0, 10)
			return featurecfg.Value{"n": n}, err
		})
	t.Cleanup(func() { featurecfg.Reset(); Invalidate() })
	n := func() int {
		v, _ := featurecfg.AsInt(featurecfg.Get("go_test_key")["n"])
		return v
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inTx(t, d, func(tx *db.Tx) error {
		if err := Save(ctx, tx, "go_test_key", featurecfg.Value{"n": 3}, "admin", now); err != nil {
			return err
		}
		if err := Save(ctx, tx, "go_test_key", featurecfg.Value{"n": 4}, "admin2", now); err != nil {
			return err
		}
		rows, err := Rows(ctx, tx)
		if err != nil || rows["go_test_key"].UpdatedBy != "admin2" {
			t.Fatal(rows, err)
		}
		Invalidate()
		if err := Refresh(ctx, tx, false); err != nil {
			return err
		}
		if n() != 4 {
			t.Fatal(featurecfg.Get("go_test_key"))
		}
		// 節流：RefreshInterval 內不重新讀取
		_ = Save(ctx, tx, "go_test_key", featurecfg.Value{"n": 5}, "admin", now)
		_ = Refresh(ctx, tx, false)
		if n() != 4 {
			t.Fatal("refresh not throttled")
		}
		_ = Refresh(ctx, tx, true)
		if n() != 5 {
			t.Fatal("force refresh")
		}
		_ = Save(ctx, tx, "go_test_key", featurecfg.Value{"n": 99}, "admin", now) // 不合法：用預設值
		_ = Refresh(ctx, tx, true)
		if !featurecfg.IsDefault("go_test_key") || n() != 1 {
			t.Fatal("invalid setting applied")
		}
		if err := Remove(ctx, tx, "go_test_key"); err != nil {
			return err
		}
		rows, _ = Rows(ctx, tx)
		if _, ok := rows["go_test_key"]; ok {
			t.Fatal("not removed")
		}
		return nil
	})
}
