package handler

// 磁盘占用分析：磁盘满了，到底是谁占的。
//
// 指标采集只告诉你「/data 用了 87%」，接下来要回答的问题是「里面是什么」。
// 在这之前平台只对 /var/log 做过 du（主机日志巡检里），其它目录没有入口 ——
// 人还是得自己 SSH 上去敲 du。
//
// # 为什么是按需触发，不做定时任务
//
// du 要遍历整棵目录树。一个几百万文件的数据盘跑一次 du 可能要几分钟，
// 期间 inode 缓存被冲掉，对业务是有影响的。定时对所有生产机跑 du 是个坏主意，
// 所以这一页只有手动「开始分析」，且带超时。
//
// # 这一页最有价值的一条：df 与 du 对不上
//
// 「df 说满了，du 加起来却少了几十 G」是运维最常遇到又最容易卡住的现象。
// 原因几乎总是**已删除但仍被进程持有的文件**：文件从目录树里消失了（du 看不到），
// 但只要还有进程持着 fd，空间就不会释放（df 照旧算着）。
// 平台把这两个数摆在一起，差额显著时直接给出这个解释，并且**真的去查**：
// 扫 /proc/<pid>/fd 找 (deleted) 链接，顺带用 stat 取大小。
// 不装 lsof —— /proc 里就有，装依赖只是为了少写十行 shell。
//
// # 照实说的限制
//
//   - du 报的是**磁盘占用**，不是文件大小之和：稀疏文件会偏小，硬链接只算一次
//   - 读不了的子目录会被跳过，所以合计是**偏小**的；跳过多少会数出来告诉你
//   - -x 不跨文件系统：否则挂载在下面的别的盘会被算进父目录，那个数没有意义
//   - 非 root 只能看到自己进程的 fd，所以「已删除仍被持有」那一段可能不完整

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

const (
	// diskAnalyzeTimeout du 的超时。比日志巡检宽一些：大目录本来就慢，
	// 但也不能无限等 —— 超时返回「这个目录太大，换个更深的路径再试」比挂在那里好
	diskAnalyzeTimeout = 120 * 1000 * 1000 * 1000 // 120s（纳秒）
	// diskTopDirs / diskTopFiles 返回多少条
	diskTopDirs  = 30
	diskTopFiles = 20
	// diskDeletedSamples 已删除仍被持有的文件最多列几条
	diskDeletedSamples = 10
	// diskGapThresholdKB df 与 du 的差额超过这个值才提示（1 GiB）
	diskGapThresholdKB = 1024 * 1024
)

type diskAnalyzeReq struct {
	HostID uint   `json:"hostId" binding:"required"`
	Path   string `json:"path"`
	// MinFileMB 只列大于这个大小的文件，默认 100MB。
	// 不给「列出所有文件」的选项：那等于把一台机器的目录树拉回平台
	MinFileMB int `json:"minFileMB"`
}

// buildDiskCommand 拼只读分析命令。
//
// 全部输出用 __OPS_xxx__ 分段，每段里再自己解析 —— 与主机日志巡检同一个套路。
// stderr 合到 stdout 但打上 E 前缀：这样「有多少子目录读不了」能数出来，
// 而不是被 2>/dev/null 吞掉（吞掉的后果是合计偏小却没人知道）。
func buildDiskCommand(path string, minFileMB int) string {
	q := shellQuote(path)
	return fmt.Sprintf(`p=%s
if [ ! -d "$p" ]; then echo '__OPS_STATE__=missing'; exit 0; fi
echo '__OPS_STATE__=ok'
echo '__OPS_DF__'
df -kP -- "$p" 2>/dev/null | awk 'NR==2 {print $2" "$3" "$4" "$6}'
echo '__OPS_DIRS__'
du -xk -d 1 -- "$p" 2>&1 | awk '/^[0-9]+[ \t]/ {print "D "$0; next} {print "E "$0}'
echo '__OPS_FILES__'
find "$p" -xdev -type f -size +%dM -exec du -k -- {} + 2>&1 | head -n 500 | awk '/^[0-9]+[ \t]/ {print "D "$0; next} {print "E "$0}'
echo '__OPS_DELETED__'
for f in /proc/[0-9]*/fd/*; do
  t=$(readlink "$f" 2>/dev/null) || continue
  case "$t" in
    *'(deleted)')
      sz=$(stat -Lc %%s "$f" 2>/dev/null || echo 0)
      pid=$(echo "$f" | cut -d/ -f3)
      echo "$sz $pid $t"
      ;;
  esac
done | sort -rn | head -n 64
echo '__OPS_END__'
exit 0
`, q, minFileMB)
}

// diskEntry 一个目录或文件的占用
type diskEntry struct {
	Path   string `json:"path"`
	SizeKB int64  `json:"sizeKb"`
	// Percent 占本次分析根目录的比例，只在目录段里有意义
	Percent float64 `json:"percent"`
}

type deletedHeldFile struct {
	PID    string `json:"pid"`
	Path   string `json:"path"`
	SizeKB int64  `json:"sizeKb"`
}

type diskAnalysis struct {
	State string
	// FSTotalKB / FSUsedKB / FSAvailKB / Mount 来自 df
	FSTotalKB int64
	FSUsedKB  int64
	FSAvailKB int64
	Mount     string
	// RootKB 分析根目录自己的合计（du 的最后一行）
	RootKB    int64
	Dirs      []diskEntry
	Files     []diskEntry
	Deleted   []deletedHeldFile
	DeletedKB int64
	// DeniedDirs / DeniedFiles 读不了的条数：合计偏小多少就靠它解释
	DeniedDirs  int
	DeniedFiles int
	// TotalFdScanned 扫到的 (deleted) 句柄总数（可能多于返回的样例数）
	DeletedCount int
}

// parseDiskOutput 解析分析输出。
//
// 解析刻意宽容：任何一段缺失只让那一段为空，不让整次失败 ——
// 一台机器没有 /proc（容器里常见）不该让磁盘分析整个报错。
func parseDiskOutput(raw string, rootPath string) diskAnalysis {
	out := diskAnalysis{State: "unknown"}
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "__OPS_STATE__="):
			out.State = strings.TrimPrefix(trimmed, "__OPS_STATE__=")
			continue
		case trimmed == "__OPS_DF__", trimmed == "__OPS_DIRS__",
			trimmed == "__OPS_FILES__", trimmed == "__OPS_DELETED__", trimmed == "__OPS_END__":
			section = trimmed
			continue
		}
		if trimmed == "" {
			continue
		}

		switch section {
		case "__OPS_DF__":
			fields := strings.Fields(trimmed)
			if len(fields) >= 4 {
				out.FSTotalKB = parseInt64(fields[0])
				out.FSUsedKB = parseInt64(fields[1])
				out.FSAvailKB = parseInt64(fields[2])
				out.Mount = fields[3]
			}
		case "__OPS_DIRS__":
			if entry, ok, denied := parseTaggedEntry(trimmed); denied {
				out.DeniedDirs++
			} else if ok {
				// du 把根目录自己也列出来（一般是最后一行），它是合计不是子项
				if entry.Path == rootPath || entry.Path == strings.TrimSuffix(rootPath, "/") {
					out.RootKB = entry.SizeKB
					continue
				}
				out.Dirs = append(out.Dirs, entry)
			}
		case "__OPS_FILES__":
			if entry, ok, denied := parseTaggedEntry(trimmed); denied {
				out.DeniedFiles++
			} else if ok {
				out.Files = append(out.Files, entry)
			}
		case "__OPS_DELETED__":
			fields := strings.Fields(trimmed)
			if len(fields) < 3 {
				continue
			}
			out.DeletedCount++
			sizeKB := parseInt64(fields[0]) / 1024
			out.DeletedKB += sizeKB
			if len(out.Deleted) < diskDeletedSamples {
				out.Deleted = append(out.Deleted, deletedHeldFile{
					PID:    fields[1],
					Path:   strings.TrimSuffix(strings.Join(fields[2:], " "), " (deleted)"),
					SizeKB: sizeKB,
				})
			}
		}
	}

	sort.SliceStable(out.Dirs, func(i, j int) bool { return out.Dirs[i].SizeKB > out.Dirs[j].SizeKB })
	sort.SliceStable(out.Files, func(i, j int) bool { return out.Files[i].SizeKB > out.Files[j].SizeKB })
	if len(out.Dirs) > diskTopDirs {
		out.Dirs = out.Dirs[:diskTopDirs]
	}
	if len(out.Files) > diskTopFiles {
		out.Files = out.Files[:diskTopFiles]
	}
	// 比例按根目录合计算；根目录读不到时不给比例，而不是拿总量凑一个假的
	if out.RootKB > 0 {
		for i := range out.Dirs {
			out.Dirs[i].Percent = round2(float64(out.Dirs[i].SizeKB) * 100 / float64(out.RootKB))
		}
	}
	return out
}

// parseTaggedEntry 解析一行 "D <KB>\t<path>" 或 "E <错误文本>"。
// 返回 (条目, 是否有效条目, 是否权限错误)
func parseTaggedEntry(line string) (diskEntry, bool, bool) {
	if strings.HasPrefix(line, "E ") {
		// 只把权限类错误算成「跳过」；其它错误（如 find 自己的用法错误）不该混进来
		lower := strings.ToLower(line)
		if strings.Contains(lower, "permission denied") || strings.Contains(lower, "denied") {
			return diskEntry{}, false, true
		}
		return diskEntry{}, false, false
	}
	body := strings.TrimPrefix(line, "D ")
	fields := strings.Fields(body)
	if len(fields) < 2 {
		return diskEntry{}, false, false
	}
	size := parseInt64(fields[0])
	path := strings.Join(fields[1:], " ")
	if size <= 0 || path == "" {
		return diskEntry{}, false, false
	}
	return diskEntry{Path: path, SizeKB: size}, true, false
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// diskGapNote df 与 du 的差额说明。
//
// 这是整页最有用的一句话，所以它必须准确：只有分析的就是挂载点本身时，
// 两个数才可比 —— 分析 /var/log 却拿整个 / 的 df 去比，差额当然巨大，
// 那种「解释」是误导。
func diskGapNote(a diskAnalysis, rootPath string) string {
	if a.RootKB <= 0 || a.FSUsedKB <= 0 {
		return ""
	}
	atMount := a.Mount != "" && (rootPath == a.Mount || strings.TrimSuffix(rootPath, "/") == a.Mount)
	if !atMount {
		return fmt.Sprintf("分析的是 %s，而 df 统计的是整个挂载点 %s —— 两个数不可比，"+
			"这里不做差额判断", rootPath, a.Mount)
	}

	gap := a.FSUsedKB - a.RootKB
	if gap < diskGapThresholdKB {
		return fmt.Sprintf("df 已用 %s，du 合计 %s，基本对得上",
			formatKB(a.FSUsedKB), formatKB(a.RootKB))
	}
	note := fmt.Sprintf("**df 已用 %s，但 du 只加出 %s，差了 %s**。",
		formatKB(a.FSUsedKB), formatKB(a.RootKB), formatKB(gap))
	if a.DeletedKB > 0 {
		note += fmt.Sprintf("已经查到 %d 个「已删除但仍被进程持有」的文件，合计 %s ——"+
			"这类文件从目录树里消失了（du 看不到），但只要进程还持着 fd，空间就不会释放（df 照旧算着）。"+
			"重启或让持有它的进程关掉 fd 即可回收。", a.DeletedCount, formatKB(a.DeletedKB))
	} else if a.DeniedDirs > 0 {
		note += fmt.Sprintf("有 %d 个目录读不了，合计本来就偏小；"+
			"没查到已删除仍被持有的文件（非 root 只能看到自己进程的 fd，这一段可能不完整）。", a.DeniedDirs)
	} else {
		note += "没查到已删除仍被持有的文件，也没有读不了的目录 ——" +
			"剩下的可能是稀疏文件、预留块（ext4 默认给 root 留 5%），或者非 root 看不到的进程句柄。"
	}
	return note
}

// formatKB 把 KB 说成人话
func formatKB(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GB", float64(kb)/1024/1024)
	case kb >= 1024:
		return fmt.Sprintf("%.1f MB", float64(kb)/1024)
	default:
		return fmt.Sprintf("%d KB", kb)
	}
}

// AnalyzeHostDisk 按需分析一台主机上某个目录的占用。
func (h *Handler) AnalyzeHostDisk(c *gin.Context) {
	var req diskAnalyzeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定主机")
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		response.BadRequest(c, "路径必须是绝对路径")
		return
	}
	minFileMB := req.MinFileMB
	if minFileMB <= 0 {
		minFileMB = 100
	}

	// 复用主机的数据范围与资源授权：看不到这台主机的人也分析不了它的磁盘。
	// 用 ActionFile 而不是 ActionExec —— 这件事的性质是「读文件系统」
	host, ok := h.loadHostForAction(c, model.ActionFile)
	if !ok {
		return
	}
	if host.ID != req.HostID {
		response.BadRequest(c, "主机不一致")
		return
	}

	command := buildDiskCommand(path, minFileMB)
	// 与主机日志同一个口径：命令是平台拼的、路径已 shellQuote，但仍过一次规则预检
	if status, hits := h.precheckScript(command); status == "blocked" {
		reason := "命令规则拦截"
		if len(hits) > 0 {
			reason = fmt.Sprintf("命令规则拦截：%s（%s）", hits[0].Description, hits[0].Pattern)
		}
		response.BadRequest(c, reason)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), diskAnalyzeTimeout)
	defer cancel()
	res := sshx.Run(ctx, h.target(host), command)

	if res.Status != "success" && res.Stdout == "" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = res.Status
		}
		if res.Status == "timeout" {
			detail = fmt.Sprintf("超时（%ds）—— 这个目录太大，du 跑不完。"+
				"换一个更深的子目录再试，或者先看下面的一级目录占用", int(diskAnalyzeTimeout/1e9))
		}
		response.Error(c, "分析失败: "+truncate(detail, 300))
		return
	}

	analysis := parseDiskOutput(res.Stdout, path)
	if analysis.State == "missing" {
		response.BadRequest(c, path+" 不存在，或者登录用户看不到它")
		return
	}

	response.OK(c, gin.H{
		"host":   gin.H{"id": host.ID, "name": host.Name, "address": host.Address},
		"path":   path,
		"costMs": res.CostMs,
		"filesystem": gin.H{
			"mount": analysis.Mount, "totalKb": analysis.FSTotalKB,
			"usedKb": analysis.FSUsedKB, "availKb": analysis.FSAvailKB,
			"usedText": formatKB(analysis.FSUsedKB), "totalText": formatKB(analysis.FSTotalKB),
		},
		"rootKb":       analysis.RootKB,
		"rootText":     formatKB(analysis.RootKB),
		"dirs":         analysis.Dirs,
		"files":        analysis.Files,
		"minFileMB":    minFileMB,
		"deleted":      analysis.Deleted,
		"deletedCount": analysis.DeletedCount,
		"deletedKb":    analysis.DeletedKB,
		"deniedDirs":   analysis.DeniedDirs,
		"deniedFiles":  analysis.DeniedFiles,
		"gapNote":      diskGapNote(analysis, path),
		"notes": []string{
			"这一页是**按需触发**的：du 要遍历整棵目录树，几百万文件的盘跑一次要几分钟，" +
				"期间还会冲掉 inode 缓存 —— 所以不做定时任务，也不建议在业务高峰对生产机跑",
			"du 报的是**磁盘占用**而不是文件大小之和：稀疏文件会偏小，硬链接只算一次",
			fmt.Sprintf("读不了的目录会被跳过，所以合计是**偏小**的；本次跳过目录 %d 个、文件 %d 个",
				analysis.DeniedDirs, analysis.DeniedFiles),
			"只统计同一个文件系统（du -x）：挂在下面的别的盘不会被算进父目录，否则那个数没有意义",
			"「已删除但仍被进程持有」靠扫 /proc/<pid>/fd 找 (deleted) 链接得到，不装 lsof；" +
				"**非 root 只能看到自己进程的 fd**，所以这一段可能不完整",
			fmt.Sprintf("大文件只列大于 %d MB 的前 %d 个 —— 不提供「列出所有文件」，"+
				"那等于把一台机器的目录树拉回平台", minFileMB, diskTopFiles),
		},
	})
}
