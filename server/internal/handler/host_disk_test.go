package handler

// 磁盘占用分析的解析与结论。
//
// 这一组测试守的是两件容易写错的事：
//  1. du 的最后一行是「根目录合计」而不是一个子项 —— 当成子项会让百分比全部错掉，
//     而且列表里会多出一条 100% 的自己；
//  2. df 与 du 的差额只有在「分析的就是挂载点本身」时才可比。拿 /var/log 的 du
//     去和整个 / 的 df 比，差额当然巨大 —— 那种「解释」是误导，比不解释更糟。

import (
	"strings"
	"testing"
)

// 一份真实输出（WSL 上跑出来的形状，做了裁剪并补上 deleted 段）
const sampleDiskOutput = `__OPS_STATE__=ok
__OPS_DF__
1055762868 17679876 984379520 /
__OPS_DIRS__
D 4	/data/rc5.d
D 1400	/data/apparmor.d
E du: cannot read directory '/data/private': Permission denied
D 20971520	/data/mysql
D 5242880	/data/backup
D 31457280	/data
__OPS_FILES__
D 10485760	/data/backup/dump-2026-09-01.sql
E find: '/data/secret': Permission denied
D 5242880	/data/mysql/ibdata1
__OPS_DELETED__
21474836480 1234 /var/lib/mysql/ibtmp1 (deleted)
1073741824 5678 /tmp/journal.log (deleted)
__OPS_END__
`

func TestParseDiskOutput(t *testing.T) {
	a := parseDiskOutput(sampleDiskOutput, "/data")

	if a.State != "ok" {
		t.Fatalf("状态应为 ok，实际 %q", a.State)
	}
	if a.FSTotalKB != 1055762868 || a.FSUsedKB != 17679876 || a.Mount != "/" {
		t.Fatalf("df 解析不对: %+v", a)
	}

	// 根目录合计要被挑出来，不能留在子目录列表里
	if a.RootKB != 31457280 {
		t.Fatalf("根目录合计应为 31457280，实际 %d", a.RootKB)
	}
	for _, dir := range a.Dirs {
		if dir.Path == "/data" {
			t.Fatal("根目录自己不该出现在子目录列表里（会多出一条 100%）")
		}
	}

	// 排序与百分比
	if len(a.Dirs) != 4 {
		t.Fatalf("应有 4 个子目录，实际 %d: %+v", len(a.Dirs), a.Dirs)
	}
	if a.Dirs[0].Path != "/data/mysql" || a.Dirs[0].SizeKB != 20971520 {
		t.Fatalf("应按占用倒序，第一名不对: %+v", a.Dirs[0])
	}
	if a.Dirs[0].Percent < 66 || a.Dirs[0].Percent > 67 {
		t.Fatalf("百分比应约 66.7%%，实际 %v", a.Dirs[0].Percent)
	}

	// 读不了的条数要数出来：合计偏小多少靠它解释
	if a.DeniedDirs != 1 || a.DeniedFiles != 1 {
		t.Fatalf("跳过计数不对: 目录 %d 文件 %d", a.DeniedDirs, a.DeniedFiles)
	}

	// 大文件
	if len(a.Files) != 2 || a.Files[0].Path != "/data/backup/dump-2026-09-01.sql" {
		t.Fatalf("大文件解析不对: %+v", a.Files)
	}

	// 已删除仍被持有：字节转 KB，(deleted) 后缀去掉，PID 留着
	if a.DeletedCount != 2 {
		t.Fatalf("应查到 2 个被持有的已删除文件，实际 %d", a.DeletedCount)
	}
	if a.Deleted[0].PID != "1234" || a.Deleted[0].Path != "/var/lib/mysql/ibtmp1" {
		t.Fatalf("第一条解析不对: %+v", a.Deleted[0])
	}
	if a.Deleted[0].SizeKB != 20971520 {
		t.Fatalf("大小应换算成 KB: %d", a.Deleted[0].SizeKB)
	}
	if a.DeletedKB != 20971520+1048576 {
		t.Fatalf("合计不对: %d", a.DeletedKB)
	}
}

// 路径不存在时照实说，而不是返回一堆 0
func TestParseDiskOutputMissing(t *testing.T) {
	a := parseDiskOutput("__OPS_STATE__=missing\n", "/nope")
	if a.State != "missing" {
		t.Fatalf("状态应为 missing，实际 %q", a.State)
	}
	if len(a.Dirs) != 0 || a.RootKB != 0 {
		t.Fatalf("不存在的路径不该有数据: %+v", a)
	}
}

// 缺段不能让整次失败：容器里没有 /proc 的情况很常见
func TestParseDiskOutputTolerance(t *testing.T) {
	minimal := "__OPS_STATE__=ok\n__OPS_DIRS__\nD 100\t/x/a\nD 200\t/x\n__OPS_END__\n"
	a := parseDiskOutput(minimal, "/x")
	if a.State != "ok" || a.RootKB != 200 || len(a.Dirs) != 1 {
		t.Fatalf("只有目录段也该解析成功: %+v", a)
	}
	if a.FSUsedKB != 0 || a.DeletedCount != 0 {
		t.Fatalf("缺失的段应为零值: %+v", a)
	}
	// df 缺失时不给差额结论，而不是拿 0 去比
	if note := diskGapNote(a, "/x"); strings.Contains(note, "差了") {
		t.Fatalf("没有 df 数据时不该给差额结论: %q", note)
	}
}

// 分析的不是挂载点本身时，明确说「两个数不可比」
func TestDiskGapNoteRefusesMountMismatch(t *testing.T) {
	a := parseDiskOutput(sampleDiskOutput, "/data")
	note := diskGapNote(a, "/data")
	if !strings.Contains(note, "不可比") {
		t.Fatalf("分析 /data 而 df 是 / 时应说明不可比: %q", note)
	}
	if strings.Contains(note, "已删除但仍被进程持有") {
		t.Fatal("不可比的时候不该顺手给出「已删除仍被持有」这种结论")
	}
}

// 差额显著 + 查到被持有的已删除文件 → 给出那个经典解释
func TestDiskGapNoteExplainsDeletedHeld(t *testing.T) {
	raw := `__OPS_STATE__=ok
__OPS_DF__
104857600 52428800 52428800 /data
__OPS_DIRS__
D 1048576	/data/a
D 10485760	/data
__OPS_DELETED__
32212254720 999 /data/big.tmp (deleted)
__OPS_END__
`
	a := parseDiskOutput(raw, "/data")
	note := diskGapNote(a, "/data")
	if !strings.Contains(note, "差了") {
		t.Fatalf("差额显著时要点明: %q", note)
	}
	if !strings.Contains(note, "已删除但仍被进程持有") {
		t.Fatalf("应给出已删除仍被持有的解释: %q", note)
	}
	if !strings.Contains(note, "30.0 GB") {
		t.Fatalf("应带上被持有的合计大小: %q", note)
	}
	if !strings.Contains(note, "重启或让持有它的进程关掉 fd") {
		t.Fatalf("要告诉人怎么回收: %q", note)
	}
}

// 差额显著但查不到被持有的文件时，不能装作知道原因
func TestDiskGapNoteHonestWhenUnexplained(t *testing.T) {
	raw := `__OPS_STATE__=ok
__OPS_DF__
104857600 52428800 52428800 /data
__OPS_DIRS__
D 10485760	/data
__OPS_END__
`
	a := parseDiskOutput(raw, "/data")
	note := diskGapNote(a, "/data")
	if strings.Contains(note, "已删除但仍被进程持有") {
		t.Fatalf("没查到就不该给这个结论: %q", note)
	}
	if !strings.Contains(note, "稀疏文件") || !strings.Contains(note, "预留块") {
		t.Fatalf("要把剩下的可能性列出来: %q", note)
	}
}

// 对得上的时候就说对得上，别制造焦虑
func TestDiskGapNoteWhenConsistent(t *testing.T) {
	raw := `__OPS_STATE__=ok
__OPS_DF__
104857600 10500000 94357600 /data
__OPS_DIRS__
D 10485760	/data
__OPS_END__
`
	a := parseDiskOutput(raw, "/data")
	note := diskGapNote(a, "/data")
	if !strings.Contains(note, "基本对得上") {
		t.Fatalf("差额不大时应说对得上: %q", note)
	}
}

// 命令要引号包路径、限定同一文件系统、按参数过滤文件大小
func TestBuildDiskCommand(t *testing.T) {
	cmd := buildDiskCommand("/data/my logs", 250)
	if !strings.Contains(cmd, `'/data/my logs'`) {
		t.Fatalf("带空格的路径必须被引号包住: %s", cmd)
	}
	if !strings.Contains(cmd, "du -xk -d 1") {
		t.Fatal("du 要带 -x：不然挂在下面的别的盘会被算进父目录")
	}
	if !strings.Contains(cmd, "-xdev") {
		t.Fatal("find 要带 -xdev，理由同上")
	}
	if !strings.Contains(cmd, "-size +250M") {
		t.Fatalf("大文件阈值没带进去: %s", cmd)
	}
	if !strings.Contains(cmd, "(deleted)") {
		t.Fatal("要扫 /proc 下的 (deleted) 句柄")
	}
	// 注入防护：路径里的单引号不能把命令截断
	evil := buildDiskCommand("/data/'; rm -rf /; echo '", 1)
	if strings.Contains(evil, "; rm -rf /; echo") && !strings.Contains(evil, `'\''`) {
		t.Fatalf("单引号没有被转义，命令可被截断: %s", evil)
	}
}

func TestFormatKB(t *testing.T) {
	cases := []struct {
		kb   int64
		want string
	}{
		{512, "512 KB"},
		{2048, "2.0 MB"},
		{1048576, "1.0 GB"},
		{31457280, "30.0 GB"},
	}
	for _, tc := range cases {
		if got := formatKB(tc.kb); got != tc.want {
			t.Errorf("formatKB(%d) = %q, want %q", tc.kb, got, tc.want)
		}
	}
}
