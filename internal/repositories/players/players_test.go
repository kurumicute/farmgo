package players_test

import (
	"context"
	"testing"

	"farmgm/internal/db"
	"farmgm/internal/repositories/players"
	"farmgm/internal/repositories/repotest"
)

func setup(t *testing.T) *db.DB { return repotest.Setup(t) }

func TestPlayers(t *testing.T) {
	d := setup(t)
	f := repotest.MakeFarm(t, d, "有農場", 0, 1)
	repotest.Tx(t, d, func(ctx context.Context, tx *db.Tx) error {
		plat, uid := "discord", "42"
		p, err := players.Create(ctx, tx, "100%_玩家", &plat, &uid)
		if err != nil {
			return err
		}
		if g, _ := players.GetByExternal(ctx, tx, "discord", "42"); g == nil || g.ID != p.ID {
			t.Fatal("external")
		}
		if g, _ := players.Get(ctx, tx, f.PlayerID, true); g == nil {
			t.Fatal("get")
		}
		rows, total, err := players.Search(ctx, tx, "%_", 10, 0) // % 與 _ 視為一般字元
		if err != nil || total != 1 || rows[0].Player.ID != p.ID || rows[0].HasFarm {
			t.Fatal(rows, total, err)
		}
		if _, total, _ = players.Search(ctx, tx, "1", 10, 0); total != 2 { // 名稱含 1 或 ID = 1
			t.Fatal(total)
		}
		rows, total, _ = players.Search(ctx, tx, "", 1, 1)
		if total != 2 || len(rows) != 1 || rows[0].Player.ID != p.ID {
			t.Fatal(rows, total)
		}
		if rows, _, _ = players.Search(ctx, tx, "有農場", 10, 0); len(rows) != 1 || !rows[0].HasFarm {
			t.Fatal(rows)
		}
		if all, _ := players.ListAll(ctx, tx, 100); len(all) != 2 {
			t.Fatal(all)
		}
		return nil
	})
}
