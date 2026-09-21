// Package backup 负责数据库快照与录像目录归档。
//
// 为什么需要它：平台的数据留存清理是真删且不可逆（见 docs/SECURITY.md），
// 主机凭据、审计、录像都只在这一个 SQLite 文件和一个目录里。没有备份的话，
// 一次误配的保留天数就能把历史抹平。
//
// 数据库用 SQLite 的 `VACUUM INTO`：它在事务里生成一个干净的副本，
// 不需要停服，也不会像直接 cp 文件那样可能拷到写了一半的页。
package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// filePrefix 备份文件名前缀，同时用于清理旧备份时识别自己的文件
const filePrefix = "opsone-"

// Result 一次备份的结果
type Result struct {
	DBPath          string
	DBBytes         int64
	RecordingsPath  string
	RecordingsBytes int64
	// RecordNote 没打出录像包时的原因（目录不存在 / 目录为空），别让人以为录像已备份
	RecordNote string
	// Pruned 本次清理掉的旧备份文件名
	Pruned []string
	TookMs int64
}

// Summary 给日志与界面用的一行说明
func (r *Result) Summary() string {
	parts := []string{fmt.Sprintf("数据库 %s（%.1f MB）", filepath.Base(r.DBPath), mb(r.DBBytes))}
	switch {
	case r.RecordingsPath != "":
		parts = append(parts, fmt.Sprintf("录像 %s（%.1f MB）", filepath.Base(r.RecordingsPath), mb(r.RecordingsBytes)))
	case r.RecordNote != "":
		parts = append(parts, r.RecordNote)
	}
	if len(r.Pruned) > 0 {
		parts = append(parts, fmt.Sprintf("清理旧备份 %d 份", len(r.Pruned)))
	}
	parts = append(parts, fmt.Sprintf("耗时 %d ms", r.TookMs))
	return strings.Join(parts, "，")
}

func mb(n int64) float64 { return float64(n) / 1024 / 1024 }

// SanityCheck 确认这个库长得像 OpsOne 的库。
// 为什么需要：备份命令要是没读到配置（sudo / cron 会清环境变量），DSN 会退回相对路径
// 的 ops.db，而 SQLite 打不开就现场建一个空库 —— 于是「备份成功」出一个空文件，
// 真出事时才发现没东西可恢复。宁可当场报错。
func SanityCheck(g *gorm.DB) error {
	var n int64
	if err := g.Raw("SELECT count(*) FROM users").Scan(&n).Error; err != nil {
		return fmt.Errorf("这个库里没有 users 表，不像是 OpsOne 的数据库: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("这个库里连内置 admin 都没有，像是刚被创建的空库")
	}
	return nil
}

// Run 生成一份备份：数据库快照 + 录像目录 tar.gz，并按 keep 保留份数清理旧的。
// stamp 允许调用方传入时间戳（测试用），传零值则取当前时间。
func Run(g *gorm.DB, recordDir, outDir string, keep int, stamp time.Time) (*Result, error) {
	if g == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	if strings.TrimSpace(outDir) == "" {
		return nil, fmt.Errorf("备份目录不能为空")
	}
	if keep < 1 {
		keep = 1
	}
	if stamp.IsZero() {
		stamp = time.Now()
	}
	started := time.Now()

	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建备份目录失败: %w", err)
	}

	name := filePrefix + stamp.Format("20060102-150405")
	dbPath := filepath.Join(outDir, name+".db")
	if _, err := os.Stat(dbPath); err == nil {
		return nil, fmt.Errorf("备份文件 %s 已存在，未覆盖", dbPath)
	}

	// VACUUM INTO 要求目标文件不存在；参数化绑定在这里不被 SQLite 接受，
	// 只能拼进语句，所以文件名是自己按时间戳生成的、不来自外部输入。
	if err := g.Exec(fmt.Sprintf("VACUUM INTO %s", quote(dbPath))).Error; err != nil {
		return nil, fmt.Errorf("数据库快照失败: %w", err)
	}
	res := &Result{DBPath: dbPath}
	if st, err := os.Stat(dbPath); err == nil {
		res.DBBytes = st.Size()
	}

	// 录像目录可能不存在或为空（没人用过 Web 终端），这不是错误，
	// 但要在结果里说清楚，免得「录像也备份了」是个误会
	if recordDir == "" {
		res.RecordNote = "未配置录像目录，未打包"
	} else if st, err := os.Stat(recordDir); err != nil || !st.IsDir() {
		res.RecordNote = fmt.Sprintf("录像目录 %s 不存在，未打包", recordDir)
	} else {
		tarPath := filepath.Join(outDir, name+"-recordings.tar.gz")
		n, err := archiveDir(recordDir, tarPath)
		if err != nil {
			return res, fmt.Errorf("录像归档失败: %w", err)
		}
		if n > 0 {
			res.RecordingsPath = tarPath
			if st, err := os.Stat(tarPath); err == nil {
				res.RecordingsBytes = st.Size()
			}
		} else {
			_ = os.Remove(tarPath)
			res.RecordNote = "录像目录为空，未打包"
		}
	}

	pruned, err := prune(outDir, keep)
	if err != nil {
		return res, fmt.Errorf("清理旧备份失败: %w", err)
	}
	res.Pruned = pruned
	res.TookMs = time.Since(started).Milliseconds()
	return res, nil
}

// quote 把路径包成 SQLite 字符串字面量
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// archiveDir 把目录打成 tar.gz，返回打进去的文件数
func archiveDir(dir, dst string) (int, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return 0, nil // 目录不存在就当没有录像
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	count := 0
	walkErr := filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		// tar 里统一用正斜杠，Windows 上打的包在 Linux 上也能解
		hdr.Name = filepath.ToSlash(rel)
		if fi.IsDir() {
			hdr.Name += "/"
			return tw.WriteHeader(hdr)
		}
		if !fi.Mode().IsRegular() {
			return nil // 软链等一律跳过，避免把目录外的东西带进来
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		if _, err := io.Copy(tw, src); err != nil {
			return err
		}
		count++
		return nil
	})
	if walkErr != nil {
		tw.Close()
		gz.Close()
		return 0, walkErr
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	return count, nil
}

// prune 只保留最近 keep 份（按文件名里的时间戳排序），连带删掉对应的录像包
func prune(outDir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return nil, err
	}
	var snapshots []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, filePrefix) && strings.HasSuffix(n, ".db") {
			snapshots = append(snapshots, n)
		}
	}
	sort.Strings(snapshots) // 名字里是 20060102-150405，字典序即时间序
	if len(snapshots) <= keep {
		return nil, nil
	}
	var pruned []string
	for _, n := range snapshots[:len(snapshots)-keep] {
		base := strings.TrimSuffix(n, ".db")
		if err := os.Remove(filepath.Join(outDir, n)); err != nil {
			return pruned, err
		}
		pruned = append(pruned, n)
		rec := filepath.Join(outDir, base+"-recordings.tar.gz")
		if _, err := os.Stat(rec); err == nil {
			if err := os.Remove(rec); err != nil {
				return pruned, err
			}
			pruned = append(pruned, filepath.Base(rec))
		}
	}
	return pruned, nil
}

// Restore 把快照恢复回去。必须在平台停机时执行：正在跑的进程握着旧文件的句柄，
// 换掉文件它也不会重新加载。原库不会被直接删掉，而是改名留一份。
func Restore(snapshot, targetDSN, recordingsTar, recordDir string) (moved string, err error) {
	if strings.TrimSpace(snapshot) == "" || strings.TrimSpace(targetDSN) == "" {
		return "", fmt.Errorf("快照路径与目标数据库路径都不能为空")
	}
	st, err := os.Stat(snapshot)
	if err != nil {
		return "", fmt.Errorf("快照不可读: %w", err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s 是目录，请指定 .db 快照文件", snapshot)
	}

	if _, err := os.Stat(targetDSN); err == nil {
		moved = targetDSN + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := os.Rename(targetDSN, moved); err != nil {
			return "", fmt.Errorf("原数据库改名失败: %w", err)
		}
	}
	// SQLite 的 -wal / -shm 残留不清掉会和新文件对不上
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(targetDSN + suffix)
	}
	if err := copyFile(snapshot, targetDSN); err != nil {
		return moved, fmt.Errorf("写入目标数据库失败: %w", err)
	}

	if strings.TrimSpace(recordingsTar) != "" {
		if err := extractTarGz(recordingsTar, recordDir); err != nil {
			return moved, fmt.Errorf("录像解包失败: %w", err)
		}
	}
	return moved, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func extractTarGz(src, dstDir string) error {
	if strings.TrimSpace(dstDir) == "" {
		return fmt.Errorf("录像目录不能为空")
	}
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// 包里的路径一律当相对路径处理，挡掉 ../ 这类越界写入
		clean := filepath.Clean(filepath.FromSlash(hdr.Name))
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("归档里有非法路径 %q，已中止", hdr.Name)
		}
		target := filepath.Join(dstDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// 软链、设备文件之类不恢复
		}
	}
}
