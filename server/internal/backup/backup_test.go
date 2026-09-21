package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type demoRow struct {
	ID   uint
	Name string
}

func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	// Windows 上没关句柄就删不掉文件，测试结束前必须收掉连接
	t.Cleanup(func() { closeDB(g) })
	return g
}

func closeDB(g *gorm.DB) {
	if sqlDB, err := g.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// 一次完整的「备份 → 删库 → 恢复」，验证数据与录像都能回来
func TestBackupAndRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ops.db")
	recordDir := filepath.Join(dir, "recordings")
	outDir := filepath.Join(dir, "backups")

	g := openDB(t, dbPath)
	if err := g.AutoMigrate(&demoRow{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := g.Create(&demoRow{Name: "web-api-01"}).Error; err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	castPath := filepath.Join(recordDir, "session-1.cast")
	if err := os.WriteFile(castPath, []byte(`{"version":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(g, recordDir, outDir, 7, time.Time{})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if res.DBBytes == 0 {
		t.Error("快照文件大小为 0")
	}
	if res.RecordingsPath == "" {
		t.Fatal("录像目录里有文件，却没有生成归档")
	}

	// 模拟灾难：库和录像都没了
	closeDB(g)
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(recordDir); err != nil {
		t.Fatal(err)
	}

	moved, err := Restore(res.DBPath, dbPath, res.RecordingsPath, recordDir)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if moved != "" {
		t.Errorf("原库已删除，不该有改名文件，却得到 %s", moved)
	}

	restored := openDB(t, dbPath)
	var rows []demoRow
	if err := restored.Find(&rows).Error; err != nil {
		t.Fatalf("读恢复后的库失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "web-api-01" {
		t.Fatalf("恢复后的数据不对: %+v", rows)
	}
	if _, err := os.Stat(castPath); err != nil {
		t.Fatalf("录像没恢复回来: %v", err)
	}
}

// 原库还在时不能被直接覆盖：要先改名留一份，给「恢复错了」留退路
func TestRestoreKeepsExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ops.db")
	outDir := filepath.Join(dir, "backups")

	g := openDB(t, dbPath)
	if err := g.AutoMigrate(&demoRow{}); err != nil {
		t.Fatal(err)
	}
	res, err := Run(g, "", outDir, 7, time.Time{})
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	// 恢复前要先停服（这里就是关连接），否则 Windows 上文件被占着改不了名
	closeDB(g)

	moved, err := Restore(res.DBPath, dbPath, "", "")
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if moved == "" {
		t.Fatal("原库存在时应当改名保留")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("改名后的文件不见了: %v", err)
	}
}

// 保留份数：超出的按时间从旧到新删，连带删掉对应的录像包
func TestPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ops.db")
	recordDir := filepath.Join(dir, "recordings")
	outDir := filepath.Join(dir, "backups")

	g := openDB(t, dbPath)
	if err := g.AutoMigrate(&demoRow{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "session-1.cast"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 21, 3, 0, 0, 0, time.Local)
	var last *Result
	for i := 0; i < 4; i++ {
		res, err := Run(g, recordDir, outDir, 2, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatalf("第 %d 次备份失败: %v", i, err)
		}
		last = res
	}
	if len(last.Pruned) == 0 {
		t.Error("超出保留份数却没有清理记录")
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var dbs, tars int
	for _, e := range entries {
		switch {
		case filepath.Ext(e.Name()) == ".db":
			dbs++
		case filepath.Ext(e.Name()) == ".gz":
			tars++
		}
	}
	if dbs != 2 {
		t.Errorf("保留 2 份，实际留下 %d 份快照", dbs)
	}
	if tars != 2 {
		t.Errorf("录像包也该只留 2 份，实际 %d 份", tars)
	}
}

// 归档里带 ../ 的路径必须被拒，不能写到目标目录外面去
func TestExtractRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.tar.gz")
	if err := writeEvilTar(evil); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(evil, filepath.Join(dir, "out")); err == nil {
		t.Fatal("带 ../ 的归档竟然解开了")
	}
}
