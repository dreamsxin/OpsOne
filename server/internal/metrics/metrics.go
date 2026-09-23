// Package metrics 平台自身的 Prometheus 指标。
//
// # 为什么自己写而不引 client_golang
//
// 与「不引 client-go 手写 K8s 客户端」「socks5 靠标准库的 Transport.Proxy」同一个口径：
// 需要的只是**四类指标 + 一种文本格式**，而 client_golang 会带进来
// protobuf、expvar 桥接、进程/Go collector 一整套。这里全部加起来两百多行，
// 依赖是零。代价要说清：**没有 exemplar、没有 native histogram、没有 push gateway**，
// 也不做多进程聚合（平台本来就是单实例跑调度）。
//
// # 只暴露答得出问题的指标
//
// 每一个指标都对着一个具体问题：
//
//   - 接口是不是变慢了 / 在报错：`opsone_http_requests_total`、`opsone_http_request_duration_seconds`
//   - 定时任务有没有在跑、有没有一直失败：`opsone_fixed_task_runs_total`、`opsone_cron_jobs`
//   - 调度到底在哪个进程上：`opsone_instance_role`、`opsone_instances`
//   - 告警积压了没有：`opsone_alerts_firing`
//   - 库会不会撑爆磁盘：`opsone_db_size_bytes`
//   - 会话/隧道有没有泄漏：`opsone_goroutines`、`opsone_active_sessions`
//
// 刻意**不**做的：每个接口一个 histogram 的高基数玩法（按路由模板聚合，
// 未匹配的路径统一成 `unmatched`，否则 `/hosts/123` 这种会把时序数量打爆）。
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry 一组指标。进程级单例见 Default()。
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*counterVec
	histograms map[string]*histogramVec
	// gauges 是「抓取时现算」的：库里的行数、goroutine 数这类东西没必要常驻内存，
	// 而且常驻就意味着要有人负责更新它 —— 忘了更新的 gauge 比没有更糟
	gauges []gaugeSource
}

type gaugeSource struct {
	Name   string
	Help   string
	Sample func() []Sample
}

// Sample 一个带标签的取值
type Sample struct {
	Labels map[string]string
	Value  float64
}

type counterVec struct {
	help string
	mu   sync.RWMutex
	vals map[string]*atomic.Int64
	keys map[string]map[string]string
}

type histogramVec struct {
	help    string
	buckets []float64
	mu      sync.RWMutex
	rows    map[string]*histogramRow
	keys    map[string]map[string]string
}

type histogramRow struct {
	counts []atomic.Int64
	sum    atomic.Uint64 // 存的是 float64 的 bits
	total  atomic.Int64
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default 进程级注册表
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewRegistry() })
	return defaultReg
}

func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]*counterVec{},
		histograms: map[string]*histogramVec{},
	}
}

// RegisterCounter 登记一个计数器族。重复登记同名视为同一个（幂等），
// 否则测试里反复构造 Handler 会 panic
func (r *Registry) RegisterCounter(name, help string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.counters[name]; ok {
		return
	}
	r.counters[name] = &counterVec{
		help: help,
		vals: map[string]*atomic.Int64{},
		keys: map[string]map[string]string{},
	}
}

// RegisterHistogram buckets 必须升序，且不含 +Inf（写出时自动补）
func (r *Registry) RegisterHistogram(name, help string, buckets []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.histograms[name]; ok {
		return
	}
	sorted := make([]float64, len(buckets))
	copy(sorted, buckets)
	sort.Float64s(sorted)
	r.histograms[name] = &histogramVec{
		help: help, buckets: sorted,
		rows: map[string]*histogramRow{},
		keys: map[string]map[string]string{},
	}
}

// RegisterGauge 登记一个「抓取时现算」的指标。
//
// Sample 会在每次 /metrics 被抓时调用，所以它必须是**便宜的** ——
// 里面放一条全表扫描的 SQL 就等于给自己加了个定时压测。
func (r *Registry) RegisterGauge(name, help string, sample func() []Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = append(r.gauges, gaugeSource{Name: name, Help: help, Sample: sample})
}

// AddCounter 累加。指标没登记过就直接忽略 —— 打点代码散在各处，
// 为了一个拼错的名字让请求 panic 不值得
func (r *Registry) AddCounter(name string, labels map[string]string, delta int64) {
	r.mu.RLock()
	vec := r.counters[name]
	r.mu.RUnlock()
	if vec == nil {
		return
	}
	key := labelKey(labels)
	vec.mu.RLock()
	cell := vec.vals[key]
	vec.mu.RUnlock()
	if cell == nil {
		vec.mu.Lock()
		if cell = vec.vals[key]; cell == nil {
			cell = &atomic.Int64{}
			vec.vals[key] = cell
			vec.keys[key] = copyLabels(labels)
		}
		vec.mu.Unlock()
	}
	cell.Add(delta)
}

// Observe 记一次观测值（秒）
func (r *Registry) Observe(name string, labels map[string]string, value float64) {
	r.mu.RLock()
	vec := r.histograms[name]
	r.mu.RUnlock()
	if vec == nil {
		return
	}
	key := labelKey(labels)
	vec.mu.RLock()
	row := vec.rows[key]
	vec.mu.RUnlock()
	if row == nil {
		vec.mu.Lock()
		if row = vec.rows[key]; row == nil {
			row = &histogramRow{counts: make([]atomic.Int64, len(vec.buckets)+1)}
			vec.rows[key] = row
			vec.keys[key] = copyLabels(labels)
		}
		vec.mu.Unlock()
	}

	// Prometheus 的桶是「小于等于上界」，SearchFloat64s 返回第一个 >= value 的下标，
	// 正好就是 value 所属的那个桶；等于 len(buckets) 时落进 +Inf
	idx := sort.SearchFloat64s(vec.buckets, value)
	row.counts[idx].Add(1)
	row.total.Add(1)
	addFloat(&row.sum, value)
}

// Write 输出 Prometheus 文本暴露格式（0.0.4）。
//
// 顺序是固定的（名字排序、标签排序）：抓取端不关心顺序，但**人会 diff 两次抓取的结果**，
// 顺序抖动会让 diff 没法看。
func (r *Registry) Write(w *strings.Builder) {
	r.mu.RLock()
	counterNames := make([]string, 0, len(r.counters))
	for name := range r.counters {
		counterNames = append(counterNames, name)
	}
	histNames := make([]string, 0, len(r.histograms))
	for name := range r.histograms {
		histNames = append(histNames, name)
	}
	gauges := make([]gaugeSource, len(r.gauges))
	copy(gauges, r.gauges)
	counters := r.counters
	histograms := r.histograms
	r.mu.RUnlock()

	sort.Strings(counterNames)
	sort.Strings(histNames)
	sort.Slice(gauges, func(i, j int) bool { return gauges[i].Name < gauges[j].Name })

	for _, name := range counterNames {
		vec := counters[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, vec.help, name)
		vec.mu.RLock()
		keys := sortedKeys(vec.keys)
		for _, key := range keys {
			fmt.Fprintf(w, "%s%s %d\n", name, renderLabels(vec.keys[key]), vec.vals[key].Load())
		}
		vec.mu.RUnlock()
	}

	for _, g := range gauges {
		samples := g.Sample()
		if samples == nil {
			// 取不到就整族不输出：写一个 0 出去会被读成「真的是 0」
			continue
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.Name, g.Help, g.Name)
		sort.Slice(samples, func(i, j int) bool {
			return renderLabels(samples[i].Labels) < renderLabels(samples[j].Labels)
		})
		for _, s := range samples {
			fmt.Fprintf(w, "%s%s %s\n", g.Name, renderLabels(s.Labels), formatFloat(s.Value))
		}
	}

	for _, name := range histNames {
		vec := histograms[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, vec.help, name)
		vec.mu.RLock()
		for _, key := range sortedKeys(vec.keys) {
			row := vec.rows[key]
			labels := vec.keys[key]
			var cumulative int64
			for i, bound := range vec.buckets {
				cumulative += row.counts[i].Load()
				fmt.Fprintf(w, "%s_bucket%s %d\n", name,
					renderLabelsWith(labels, "le", formatFloat(bound)), cumulative)
			}
			cumulative += row.counts[len(vec.buckets)].Load()
			fmt.Fprintf(w, "%s_bucket%s %d\n", name,
				renderLabelsWith(labels, "le", "+Inf"), cumulative)
			fmt.Fprintf(w, "%s_sum%s %s\n", name, renderLabels(labels),
				formatFloat(loadFloat(&row.sum)))
			fmt.Fprintf(w, "%s_count%s %d\n", name, renderLabels(labels), row.total.Load())
		}
		vec.mu.RUnlock()
	}
}

func sortedKeys(m map[string]map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// labelKey 标签的稳定序列化，用作 map 主键
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(1) // 用不可见分隔符，避免标签值里的逗号造成歧义
		b.WriteString(labels[name])
		b.WriteByte(2)
	}
	return b.String()
}

func copyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func renderLabels(labels map[string]string) string {
	return renderLabelsWith(labels, "", "")
}

// renderLabelsWith 渲染标签，extraName 非空时额外追加一个（给 histogram 的 le 用）
func renderLabelsWith(labels map[string]string, extraName, extraValue string) string {
	if len(labels) == 0 && extraName == "" {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names)+1)
	for _, name := range names {
		// %q 的转义规则正好覆盖 Prometheus 文本格式要求的三处（双引号、反斜杠、换行），
		// 不要再自己转一遍 —— 那会变成双重转义，抓取端看到的是 `a\\"b`
		parts = append(parts, fmt.Sprintf("%s=%q", name, labels[name]))
	}
	if extraName != "" {
		parts = append(parts, fmt.Sprintf("%s=%q", extraName, extraValue))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func addFloat(target *atomic.Uint64, delta float64) {
	for {
		old := target.Load()
		next := math.Float64frombits(old) + delta
		if target.CompareAndSwap(old, math.Float64bits(next)) {
			return
		}
	}
}

func loadFloat(target *atomic.Uint64) float64 {
	return math.Float64frombits(target.Load())
}
