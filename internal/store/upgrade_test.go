package store

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestOpen_UpgradeOldSchema 旧库升级（2026-10-18 桌面端打不开的回归锚）：
// 无 game_id 列的旧 suggestions 库打开时自动 ALTER 补列并建索引，历史数据保留。
// 教训：Open 的迁移逻辑只有"旧库升级"路径会走到，全部测试用 TempDir 全新库
// 根本覆盖不到——本用例手工构造旧 schema 库钉死该路径，此后任何 schema 改动
// 必须同步维护这里的旧形态。
func TestOpen_UpgradeOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	// 旧 schema（2026-10-17 前的形态）：无 game_id、无 adoptions 表、无 game 索引
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE IF NOT EXISTS suggestions (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		ts        TEXT    NOT NULL,
		session   TEXT    NOT NULL,
		source    TEXT    NOT NULL,
		fen       TEXT    NOT NULL,
		best_move TEXT    NOT NULL,
		score_cp  INTEGER,
		score_mate INTEGER,
		pv        TEXT    NOT NULL,
		depth     INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`insert into suggestions(ts,session,source,fen,best_move,pv,depth)
		values('2026-10-01T12:00:00.000+08:00','','engine','fenOld','h2e2','h2e2',20)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	// 新版 Open：必须成功（此前在此处报"建表失败: no such column: game_id"退出）
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatalf("旧库升级失败: %v", err)
	}
	defer st.Close()

	// 历史数据保留 + 新列可查
	var best string
	var gid string
	if err := st.db.QueryRow("select best_move, game_id from suggestions where fen='fenOld'").Scan(&best, &gid); err != nil {
		t.Fatalf("旧记录读取失败: %v", err)
	}
	if best != "h2e2" || gid != "" {
		t.Fatalf("旧记录不符: best=%q game_id=%q", best, gid)
	}
	// game 索引已建（迁移后单独建的那个）
	var n int
	if err := st.db.QueryRow("select count(*) from sqlite_master where type='index' and name='idx_suggestions_game'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("idx_suggestions_game 未建: n=%d err=%v", n, err)
	}
	// 采纳事件表已建
	if err := st.db.QueryRow("select count(*) from adoptions").Scan(&n); err != nil {
		t.Fatalf("adoptions 表缺失: %v", err)
	}
}
