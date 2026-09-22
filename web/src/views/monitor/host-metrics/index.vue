<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import * as echarts from 'echarts'
import {
  collectAllHostMetrics,
  collectHostMetric,
  listHostMetricHistory,
  listHostMetrics,
  type HostMetricRow
} from '@/api'

const loading = ref(false)
const collecting = ref(false)
const rows = ref<HostMetricRow[]>([])
const env = ref('')
const summary = reactive({ total: 0, collected: 0, failed: 0, staleMinutes: 30, spec: '' })

const trend = reactive({
  visible: false,
  title: '',
  hostId: 0,
  hours: 6,
  loading: false,
  empty: false
})
const chartBox = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

async function load() {
  loading.value = true
  try {
    const data = await listHostMetrics(env.value || undefined)
    rows.value = data.items || []
    summary.total = data.total
    summary.collected = data.collected
    summary.failed = data.failed
    summary.staleMinutes = data.staleMinutes
    summary.spec = data.spec
  } finally {
    loading.value = false
  }
}

async function collectAll() {
  collecting.value = true
  try {
    const res = await collectAllHostMetrics()
    ElMessage({ type: res.failed ? 'warning' : 'success', message: res.detail })
    await load()
  } catch (err: any) {
    ElMessage.error(err?.message || '采集失败')
  } finally {
    collecting.value = false
  }
}

async function collectOne(row: HostMetricRow) {
  try {
    const res = await collectHostMetric(row.hostId)
    ElMessage({ type: res.metric.status === 'ok' ? 'success' : 'warning', message: res.detail })
    await load()
  } catch (err: any) {
    ElMessage.error(err?.message || '采集失败')
  }
}

async function openTrend(row: HostMetricRow) {
  trend.visible = true
  trend.title = `${row.hostName} 的指标趋势`
  trend.hostId = row.hostId
  await loadTrend()
}

async function loadTrend() {
  trend.loading = true
  try {
    const data = await listHostMetricHistory(trend.hostId, trend.hours)
    const items = (data.items || []).filter((m) => m.status === 'ok')
    trend.empty = items.length === 0
    await nextTick()
    if (trend.empty || !chartBox.value) return
    if (!chart) chart = echarts.init(chartBox.value)
    chart.setOption({
      tooltip: { trigger: 'axis' },
      legend: { data: ['CPU %', '内存 %', '磁盘 %', '单核负载'] },
      grid: { left: 45, right: 45, top: 40, bottom: 30 },
      xAxis: {
        type: 'category',
        data: items.map((m) => m.createdAt.slice(11, 19))
      },
      yAxis: [
        { type: 'value', name: '%', max: 100 },
        { type: 'value', name: '负载' }
      ],
      series: [
        { name: 'CPU %', type: 'line', smooth: true, data: items.map((m) => m.cpuPercent) },
        { name: '内存 %', type: 'line', smooth: true, data: items.map((m) => m.memPercent) },
        { name: '磁盘 %', type: 'line', smooth: true, data: items.map((m) => m.diskMaxPercent) },
        {
          name: '单核负载',
          type: 'line',
          smooth: true,
          yAxisIndex: 1,
          data: items.map((m) => (m.cpuCores > 0 ? +(m.load1 / m.cpuCores).toFixed(2) : m.load1))
        }
      ]
    })
    chart.resize()
  } catch (err: any) {
    ElMessage.error(err?.message || '读取趋势失败')
  } finally {
    trend.loading = false
  }
}

/** 使用率配色：越满越红，和磁盘/内存的直觉一致 */
function usageColor(value: number) {
  if (value >= 90) return 'exception'
  if (value >= 75) return 'warning'
  return 'success'
}

function formatUptime(sec: number) {
  if (!sec) return '—'
  const days = Math.floor(sec / 86400)
  const hours = Math.floor((sec % 86400) / 3600)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时`
  // 刚重启的机器显示「0 小时」看不出刚起来，落到分钟
  return `${Math.max(1, Math.floor(sec / 60))} 分钟`
}

onUnmounted(() => {
  chart?.dispose()
  chart = null
})

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="env" placeholder="全部环境" clearable style="width: 140px" @change="load">
          <el-option label="dev" value="dev" />
          <el-option label="test" value="test" />
          <el-option label="prod" value="prod" />
        </el-select>
        <el-button
          v-perm="'host:check'"
          type="primary"
          :loading="collecting"
          @click="collectAll"
        >
          立即采集全部
        </el-button>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span
            >指标走已有的 SSH 通道采集（一条只读命令读 <code>/proc</code> 与
            <code>df</code>），<b>不装 agent、不依赖 Prometheus</b>；代价是采样间隔就是定时任务的节奏（<code>{{
              summary.spec || '未启用定时'
            }}</code
            >，<code>OPS_HOST_METRIC_SPEC</code> 可改），看不了秒级抖动。共
            {{ summary.total }} 台，{{ summary.collected }} 台有采样记录，其中 {{ summary.failed }}
            台最近一次采集失败；超过
            {{ summary.staleMinutes }} 分钟没采到会标为「已过期」，这类采样也不参与告警判定。阈值告警在「告警规则」里用
            <code>host.cpu_max</code> / <code>host.mem_max</code> / <code>host.disk_max</code> /
            <code>host.load_per_core_max</code> / <code>host.metric_failed</code> 这几个指标配。</span
          >
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="hostName" label="主机" min-width="130" />
        <el-table-column prop="address" label="地址" width="140" />
        <el-table-column label="环境" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.env === 'prod' ? 'danger' : 'info'">{{ row.env }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="CPU" width="150">
          <template #default="{ row }">
            <el-progress
              v-if="row.metric && row.metric.status === 'ok'"
              :percentage="Math.min(100, row.metric.cpuPercent)"
              :status="usageColor(row.metric.cpuPercent)"
              :stroke-width="12"
              text-inside
            />
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="内存" width="150">
          <template #default="{ row }">
            <el-progress
              v-if="row.metric && row.metric.status === 'ok'"
              :percentage="Math.min(100, row.metric.memPercent)"
              :status="usageColor(row.metric.memPercent)"
              :stroke-width="12"
              text-inside
            />
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="inode（最满）" width="170">
          <template #default="{ row }">
            <template v-if="row.metric && row.metric.status === 'ok' && row.metric.inodeRead">
              <el-progress
                :percentage="Math.min(100, row.metric.inodeMaxPercent)"
                :status="usageColor(row.metric.inodeMaxPercent)"
                :stroke-width="12"
                text-inside
              />
              <span style="color: var(--el-text-color-secondary); font-size: 12px">
                {{ row.metric.inodeMaxMount }}
              </span>
            </template>
            <el-tooltip
              v-else-if="row.metric && row.metric.status === 'ok'"
              content="这条采样没有 inode 数据（采集上线前的旧样本，或这台机器 df -i 读不到）——不是 inode 很空"
              placement="top"
            >
              <span style="color: var(--el-text-color-secondary)">未采集</span>
            </el-tooltip>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="磁盘（最满）" width="170">
          <template #default="{ row }">
            <template v-if="row.metric && row.metric.status === 'ok'">
              <el-progress
                :percentage="Math.min(100, row.metric.diskMaxPercent)"
                :status="usageColor(row.metric.diskMaxPercent)"
                :stroke-width="12"
                text-inside
              />
              <span style="color: var(--el-text-color-secondary); font-size: 12px">
                {{ row.metric.diskMaxMount }}
              </span>
            </template>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="负载" width="130">
          <template #default="{ row }">
            <template v-if="row.metric && row.metric.status === 'ok'">
              <div>{{ row.metric.load1 }} / {{ row.metric.load5 }} / {{ row.metric.load15 }}</div>
              <div
                :style="{
                  fontSize: '12px',
                  color: row.loadPerCore >= 1 ? 'var(--el-color-danger)' : 'var(--el-text-color-secondary)'
                }"
              >
                单核 {{ row.loadPerCore }}（{{ row.metric.cpuCores }} 核）
              </div>
            </template>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="内存容量" width="130">
          <template #default="{ row }">
            <span v-if="row.metric && row.metric.status === 'ok'">
              {{ row.metric.memUsedMB }} / {{ row.metric.memTotalMB }} MB
            </span>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="进程/连接" width="110">
          <template #default="{ row }">
            <span v-if="row.metric && row.metric.status === 'ok'">
              {{ row.metric.procCount }} / {{ row.metric.tcpConn }}
            </span>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="已运行" width="120">
          <template #default="{ row }">
            {{ row.metric && row.metric.status === 'ok' ? formatUptime(row.metric.uptimeSec) : '—' }}
          </template>
        </el-table-column>
        <el-table-column label="采样" min-width="200">
          <template #default="{ row }">
            <template v-if="row.metric">
              <div>
                {{ row.metric.createdAt.replace('T', ' ').slice(0, 19) }}
                <el-tag v-if="row.stale" size="small" type="warning" style="margin-left: 4px">
                  已过期
                </el-tag>
              </div>
              <div v-if="row.metric.status !== 'ok'" style="color: var(--el-color-danger)">
                采集失败：{{ row.metric.error }}
              </div>
            </template>
            <span v-else style="color: var(--el-text-color-secondary)">还没采过</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'host:check'" link type="primary" @click="collectOne(row)">
              采集
            </el-button>
            <el-button link type="primary" @click="openTrend(row)">趋势</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-drawer v-model="trend.visible" :title="trend.title" size="70%">
      <div class="page-toolbar">
        <el-radio-group v-model="trend.hours" @change="loadTrend">
          <el-radio-button :value="1">1 小时</el-radio-button>
          <el-radio-button :value="6">6 小时</el-radio-button>
          <el-radio-button :value="24">24 小时</el-radio-button>
          <el-radio-button :value="168">7 天</el-radio-button>
        </el-radio-group>
      </div>
      <el-empty v-if="trend.empty" description="这个时间范围内没有成功的采样" />
      <div v-else v-loading="trend.loading" ref="chartBox" style="height: 380px"></div>
    </el-drawer>
  </div>
</template>
