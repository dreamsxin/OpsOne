package logx

// 日志输出的测试。
//
// 日志这东西坏掉的方式很特别：**它不影响业务，只让你在排查故障时手上没东西**。
// 所以这里每条都对着一个「以为在记日志、其实没记」的场景。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestWriter(t *testing.T, opts Options) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	if opts.File != "" {
		opts.File = filepath.Join(dir, opts.File)
	}
	w, err := New(opts)
	if err != nil {
		t.Fatalf("构造 Writer 失败: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, opts.File
}

// 不配文件时只写 stderr，不该在磁盘上留下任何东西
func TestStderrOnlyByDefault(t *testing.T) {
	w, _ := newTestWriter(t, Options{})
	var sink strings.Builder
	w.stderr = &sink
	if _, err := w.Write([]byte("[scheduler] 你好\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if !strings.Contains(sink.String(), "[scheduler] 你好") {
		t.Fatalf("应该原样写到 stderr，实际 %q", sink.String())
	}
	if !strings.Contains(w.Describe(), "stderr") {
		t.Fatalf("Describe 要说清去向，实际 %q", w.Describe())
	}
}

func TestWritesToFileAndStderr(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log", AlsoStderr: true})
	var sink strings.Builder
	w.stderr = &sink

	logger := log.New(w, "", log.LstdFlags)
	logger.Printf("[instance] 本实例是 leader")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读日志文件失败: %v", err)
	}
	if !strings.Contains(string(raw), "[instance] 本实例是 leader") {
		t.Fatalf("文件里应该有这行，实际 %q", raw)
	}
	if !strings.Contains(sink.String(), "[instance]") {
		t.Fatal("AlsoStderr 开着时终端也该看得到 —— 日志突然「不见了」是糟糕的体验")
	}
}

// JSON 模式：从 [module] 前缀提字段，剥掉标准库的时间前缀，但**不造 level**
func TestJSONFormatExtractsModuleWithoutLevel(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log", JSON: true})
	logger := log.New(w, "", log.LstdFlags)
	logger.Printf(`[scheduler] 任务 3 失败: 引号"与\反斜杠` + "\t制表符")
	logger.Printf("没有模块前缀的一行")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读日志文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("应该是两行 JSON，实际 %d 行: %q", len(lines), raw)
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("第一行不是合法 JSON（转义没做对）: %v\n%s", err, lines[0])
	}
	if first["module"] != "scheduler" {
		t.Fatalf("module 应该是 scheduler，实际 %v", first["module"])
	}
	msg, _ := first["msg"].(string)
	if strings.HasPrefix(msg, "[scheduler]") {
		t.Fatalf("msg 里不该再重复模块前缀，实际 %q", msg)
	}
	if !strings.Contains(msg, `引号"与\反斜杠`) {
		t.Fatalf("正文内容应该完整保留，实际 %q", msg)
	}
	if strings.Contains(msg, "2026/") || strings.Contains(msg, "20") && strings.HasPrefix(msg, "20") {
		t.Fatalf("标准库的时间前缀应该被剥掉，实际 %q", msg)
	}
	if _, ok := first["level"]; ok {
		t.Fatal("刻意不产出 level —— 228 处 log.Printf 不带级别信息，猜出来的字段是不准的")
	}
	if _, err := time.Parse(time.RFC3339, first["time"].(string)); err != nil {
		t.Fatalf("time 应该是 RFC3339: %v", err)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("第二行不是合法 JSON: %v", err)
	}
	if _, ok := second["module"]; ok {
		t.Fatal("没有前缀的行不该凭空造一个 module")
	}
}

// 超过大小就切，并且只保留 Keep 份历史
func TestRotateAndPrune(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log", MaxMB: 1, Keep: 2})
	// 1 MB 阈值，每行 100KB，写 40 行足够切好几次
	line := strings.Repeat("x", 100*1024) + "\n"
	for i := 0; i < 40; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("当前文件应该一直存在: %v", err)
	}
	history, err := filepath.Glob(path + ".*")
	if err != nil {
		t.Fatalf("列历史文件失败: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("保留份数应该是 2，实际 %d: %v", len(history), history)
	}
	info, _ := os.Stat(path)
	if info.Size() >= 1024*1024 {
		t.Fatalf("当前文件不该超过阈值太多，实际 %d", info.Size())
	}
}

// Keep=0：切完就删，一份历史都不留
func TestKeepZeroLeavesNoHistory(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log", MaxMB: 1, Keep: 0})
	line := strings.Repeat("y", 200*1024) + "\n"
	for i := 0; i < 20; i++ {
		_, _ = w.Write([]byte(line))
	}
	history, _ := filepath.Glob(path + ".*")
	if len(history) != 0 {
		t.Fatalf("Keep=0 时不该留历史，实际 %v", history)
	}
}

// 文件被外部删掉（logrotate / 人手动清）之后要能自己重建。
// 不这么做的话日志会静默写进一个没人看得到的已删除 inode ——
// 「以为在记日志其实没记」比不记日志更糟
func TestRecreatesFileAfterExternalDelete(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log"})
	if _, err := w.Write([]byte("第一行\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("删文件失败: %v", err)
	}
	// Close 之后重新打开一个 writer 模拟「运行中文件被删」
	w2, err := New(Options{File: path})
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer func() { _ = w2.Close() }()
	_ = os.Remove(path)
	if _, err := w2.Write([]byte("第二行\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("文件应该被重建: %v", err)
	}
	if !strings.Contains(string(raw), "第二行") {
		t.Fatalf("重建后的文件里应该有新行，实际 %q", raw)
	}
}

// 并发写不能把行切碎 —— 交错的日志行是没法解析的
func TestConcurrentWritesKeepLinesIntact(t *testing.T) {
	w, path := newTestWriter(t, Options{File: "ops.log"})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := strings.Repeat(string(rune('a'+n%26)), 2000)
			for j := 0; j < 20; j++ {
				_, _ = w.Write([]byte("[mod" + string(rune('0'+n%10)) + "] " + body + "\n"))
			}
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 400 {
		t.Fatalf("应该正好 400 行，实际 %d", len(lines))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "[mod") {
			t.Fatalf("第 %d 行被切碎了: %q", i+1, line[:min(40, len(line))])
		}
		if len(line) != len("[mod0] ")+2000 {
			t.Fatalf("第 %d 行长度不对（%d），说明有行交错", i+1, len(line))
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
