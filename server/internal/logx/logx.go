// Package logx 日志输出：可选落文件、按大小轮转、可选 JSON。
//
// # 现状与这一轮改了什么
//
// 平台有 **228 处 `log.Printf`**，全部写 stderr，没有结构、没有轮转。
// 在 systemd 或 docker 下这其实是对的（journald / log driver 负责收集与轮转），
// 但有两种情况会出问题：直接 `nohup` 起进程时日志无处可去、
// 想把日志喂给 Loki 时那堆文本行没有可切分的字段。
//
// 所以这一轮做的是**可选**的一层：`OPS_LOG_FILE` 一设才落文件，不设则与以前完全一样。
//
// # 两条刻意不做的
//
//  1. **不做日志级别**。那 228 处调用里没有任何级别信息，按文本里有没有「失败」
//     去猜一个 level 会造出一个看起来精确、实则不准的字段 ——
//     与指标那一轮拒绝给内置任务猜 ok/failed 是同一条原则。
//     JSON 里只有 `time` / `module` / `msg`，没有 `level`。
//     真要级别得改调用点，那是另一件事。
//  2. **不改 228 处调用点**。`log.SetOutput` 把标准库的输出接过来即可；
//     `module` 字段从现成的 `[scheduler]` / `[instance]` 前缀里**机械提取** ——
//     这个前缀本来就是全仓统一的约定，提取它不需要任何猜测。
//
// # 自带轮转 vs 交给外面
//
// 有 systemd/journald 或 docker log driver 的话**别用文件模式** —— 外面的轮转
// 在「切文件时不丢行」「按时间切」「压缩归档」上都做得更好。这里的轮转是给
// 「裸进程 + 没有 logrotate」那种最小部署兜底的，只做一件事：按大小切、保留 N 份。
//
// 另外它能自愈一种情况：文件被 logrotate 之类的东西**在进程外挪走或删掉**之后，
// 下一次写入会发现 fd 指向的文件已经不在，于是重新创建 —— 不这么做的话
// 日志会静默写进一个没人能看到的已删除 inode，而「以为在记日志其实没记」
// 比不记日志更糟。
package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options 日志配置
type Options struct {
	// File 落地文件路径。空字符串表示只写 stderr（默认，行为与以前一致）
	File string
	// JSON 是否输出 JSON 行
	JSON bool
	// MaxMB 单文件大小上限，超过就切
	MaxMB int
	// Keep 保留多少份历史文件（不含当前文件）
	Keep int
	// AlsoStderr 落文件的同时继续写 stderr。
	// 默认开：起进程的人通常在看终端，日志突然「不见了」是个糟糕的体验
	AlsoStderr bool
}

// Writer 实现 io.Writer，交给 log.SetOutput 与 gin 用。
type Writer struct {
	opts Options

	mu      sync.Mutex
	file    *os.File
	size    int64
	stderr  io.Writer
	nowFunc func() time.Time
}

// modulePrefix 匹配全仓统一的 `[模块] 正文` 前缀
var modulePrefix = regexp.MustCompile(`^\[([a-zA-Z0-9_\-]{1,24})\]\s*`)

// New 构造 Writer。File 为空时只写 stderr。
func New(opts Options) (*Writer, error) {
	if opts.MaxMB <= 0 {
		opts.MaxMB = 64
	}
	if opts.Keep < 0 {
		opts.Keep = 0
	}
	w := &Writer{opts: opts, stderr: os.Stderr, nowFunc: time.Now}
	if opts.File == "" {
		return w, nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.File), 0o750); err != nil {
		return nil, fmt.Errorf("建日志目录失败: %w", err)
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write 接收标准库 log 打好的一整行（含结尾换行）。
//
// 标准库 log 每次 Output 调一次 Write，所以这里可以假设「一次调用就是一行」；
// 加锁保证多个 goroutine 的行不会互相插进去 —— 交错的日志行是没法解析的。
func (w *Writer) Write(p []byte) (int, error) {
	line := w.format(p)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.opts.AlsoStderr || w.file == nil {
		_, _ = w.stderr.Write(line)
	}
	if w.file == nil {
		return len(p), nil
	}
	if err := w.ensureFile(); err != nil {
		// 落文件失败不能让业务受影响，也不能静默：往 stderr 喊一声
		fmt.Fprintf(w.stderr, "[logx] 写日志文件失败，本行只写了 stderr: %v\n", err)
		if !w.opts.AlsoStderr {
			_, _ = w.stderr.Write(line)
		}
		return len(p), nil
	}
	n, err := w.file.Write(line)
	w.size += int64(n)
	if w.size >= int64(w.opts.MaxMB)*1024*1024 {
		if rotateErr := w.rotate(); rotateErr != nil {
			fmt.Fprintf(w.stderr, "[logx] 日志轮转失败: %v\n", rotateErr)
		}
	}
	// 返回原始长度：调用方（标准库 log）只关心「有没有全写进去」，
	// 而格式化之后的长度跟它给的不一样
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// format 文本模式原样输出；JSON 模式把 `[module] 正文` 拆成字段。
func (w *Writer) format(p []byte) []byte {
	if !w.opts.JSON {
		return p
	}
	raw := strings.TrimRight(string(p), "\r\n")
	// 标准库 log 的默认前缀是 "2006/01/02 15:04:05 "，剥掉它用自己的时间格式；
	// 剥不掉就整行当正文 —— 宁可多一点噪音，也不要把正文切错
	msg := raw
	if len(raw) > 20 && raw[4] == '/' && raw[7] == '/' && raw[10] == ' ' {
		msg = raw[20:]
	}
	entry := map[string]string{
		"time": w.nowFunc().Format(time.RFC3339),
		"msg":  msg,
	}
	if m := modulePrefix.FindStringSubmatch(msg); m != nil {
		entry["module"] = m[1]
		entry["msg"] = strings.TrimPrefix(msg, m[0])
	}
	// json.Marshal 负责转义：日志正文里有引号、换行、控制字符是常事
	// （命令输出、报错信息都可能带），自己拼字符串迟早产出非法 JSON
	out, err := json.Marshal(entry)
	if err != nil {
		return p
	}
	return append(out, '\n')
}

// ensureFile 确认 fd 还指向真实存在的文件。
//
// 文件被外部挪走/删掉时（logrotate、人手动清），fd 仍然可写，但写进去的东西
// 没人看得到。所以每次写前 Stat 一下当前路径：不存在就重新创建。
// 这个 Stat 在本地盘上是很便宜的调用，日志量再大也不至于成为瓶颈。
func (w *Writer) ensureFile() error {
	if _, err := os.Stat(w.opts.File); err == nil {
		return nil
	}
	_ = w.file.Close()
	w.file = nil
	return w.open()
}

func (w *Writer) open() error {
	file, err := os.OpenFile(w.opts.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	w.file = file
	w.size = 0
	if info, statErr := file.Stat(); statErr == nil {
		w.size = info.Size()
	}
	return nil
}

// rotate 切文件：**先关再改名再重开**。
//
// 顺序是必须的：Windows 上重命名一个仍被打开的文件会失败
// （Go 的 os.OpenFile 没有设 FILE_SHARE_DELETE），所以不能像 Unix 那样
// 先 rename 再 reopen。调用方已持锁。
func (w *Writer) rotate() error {
	if w.file == nil {
		return nil
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil

	stamp := w.nowFunc().Format("20060102-150405")
	target := fmt.Sprintf("%s.%s", w.opts.File, stamp)
	// 同一秒内切两次（极端情况）会撞名，加序号避免覆盖掉刚切出来的那份
	for i := 1; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = fmt.Sprintf("%s.%s-%d", w.opts.File, stamp, i)
		if i > 100 {
			break
		}
	}
	if err := os.Rename(w.opts.File, target); err != nil {
		// 改名失败时也要把文件重开回来，否则后面所有日志都没地方写
		_ = w.open()
		return err
	}
	if err := w.open(); err != nil {
		return err
	}
	return w.prune()
}

// prune 只保留最近 Keep 份历史。Keep 为 0 表示不保留历史（切完就删）。
func (w *Writer) prune() error {
	matches, err := filepath.Glob(w.opts.File + ".*")
	if err != nil {
		return err
	}
	if len(matches) <= w.opts.Keep {
		return nil
	}
	// 文件名里的时间戳是定长的，字典序等于时间序
	sort.Strings(matches)
	for _, path := range matches[:len(matches)-w.opts.Keep] {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭文件。进程退出前调用，把缓冲落盘。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Describe 一句话说明当前的日志去向，给启动日志用
func (w *Writer) Describe() string {
	if w.opts.File == "" {
		return "只写 stderr（由 systemd / docker 负责收集与轮转）"
	}
	format := "文本"
	if w.opts.JSON {
		format = "JSON"
	}
	extra := ""
	if w.opts.AlsoStderr {
		extra = "，同时写 stderr"
	}
	return fmt.Sprintf("%s → %s（%s，单文件 %d MB，保留 %d 份%s）",
		format, w.opts.File, format, w.opts.MaxMB, w.opts.Keep, extra)
}
