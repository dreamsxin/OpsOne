package handler

import (
	"strings"
	"testing"
)

// 一份真实采集输出（取自 Ubuntu 26.04，做了裁剪）
const sampleMetricOutput = `cores=12
cpu1=1000 800
cpu2=1200 900
mem=16292484 12000000 4194304 4194304
load=0.52 0.61 0.58
uptime=1234.56
procs=187
tcp=42
disk=31 /
disk=87 /data
disk=5 /boot
`

func TestParseHostMetrics(t *testing.T) {
	sample, err := parseHostMetrics(sampleMetricOutput)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// CPU：两次差值 total=200、idle=100，忙占一半
	if got := round2(sample.CPUPercent); got != 50 {
		t.Fatalf("CPU 应为 50%%，实际 %v", got)
	}
	if sample.CPUCores != 12 {
		t.Fatalf("核数应为 12，实际 %d", sample.CPUCores)
	}
	// 内存：(16292484-12000000)/16292484 ≈ 26.35%
	if got := round2(sample.MemPercent); got < 26.3 || got > 26.4 {
		t.Fatalf("内存占比应约 26.35%%，实际 %v", got)
	}
	if sample.MemTotalMB != 15910 {
		t.Fatalf("总内存应为 15910MB，实际 %d", sample.MemTotalMB)
	}
	// swap 全空闲
	if sample.SwapPercent != 0 {
		t.Fatalf("swap 未使用时应为 0，实际 %v", sample.SwapPercent)
	}
	// 磁盘只留最满的那个
	if sample.DiskMaxPercent != 87 || sample.DiskMaxMount != "/data" {
		t.Fatalf("应取最满的 /data 87%%，实际 %v %s", sample.DiskMaxPercent, sample.DiskMaxMount)
	}
	if sample.Load1 != 0.52 || sample.Load15 != 0.58 {
		t.Fatalf("负载解析不对: %+v", sample)
	}
	if sample.ProcCount != 187 || sample.TCPConn != 42 || sample.UptimeSec != 1234 {
		t.Fatalf("进程/连接/运行时长解析不对: %+v", sample)
	}
}

func TestParseHostMetricsTolerance(t *testing.T) {
	// 老内核没有 MemAvailable、容器里读不到 /proc/net/tcp：缺项归 0，不让整次失败
	minimal := "cpu1=100 50\ncpu2=200 100\n"
	sample, err := parseHostMetrics(minimal)
	if err != nil {
		t.Fatalf("只有 CPU 也应该解析成功: %v", err)
	}
	if sample.MemPercent != 0 || sample.DiskMaxPercent != 0 || sample.TCPConn != 0 {
		t.Fatalf("缺失项应为 0，实际 %+v", sample)
	}
	// 核数缺失时兜底为 1，避免除零
	if sample.CPUCores != 1 {
		t.Fatalf("核数缺失应兜底为 1，实际 %d", sample.CPUCores)
	}

	// 读不到 /proc/stat 才算彻底失败
	if _, err := parseHostMetrics("cores=4\nmem=100 50 0 0\n"); err == nil {
		t.Fatal("没有 CPU 采样应该报错")
	} else if !strings.Contains(err.Error(), "/proc/stat") {
		t.Fatalf("错误信息应指明是 /proc/stat 读不到，实际 %v", err)
	}
}

func TestParseHostMetricsClampsCPU(t *testing.T) {
	// 采样期间机器重启，/proc/stat 计数回退，差值为负 —— 不能算出负数或超 100
	backwards := "cpu1=1000 900\ncpu2=900 800\n"
	sample, err := parseHostMetrics(backwards)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if sample.CPUPercent != 0 {
		t.Fatalf("总时间没有前进时 CPU 应为 0，实际 %v", sample.CPUPercent)
	}

	// idle 比 total 前进得少很多，忙占比会超过 100，要夹到 100
	overflow := "cpu1=1000 1000\ncpu2=1100 900\n"
	sample, err = parseHostMetrics(overflow)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if sample.CPUPercent > 100 {
		t.Fatalf("CPU 不应超过 100，实际 %v", sample.CPUPercent)
	}
}
