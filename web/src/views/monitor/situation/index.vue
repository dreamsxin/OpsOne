<script setup lang="ts">
import { onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts'
import { getAlertSituation, type AlertSituation } from '@/api'

const loading = ref(false)
const range = ref('24h')
const data = ref<AlertSituation | null>(null)

const trendBox = ref<HTMLDivElement>()
const severityBox = ref<HTMLDivElement>()
const sourceBox = ref<HTMLDivElement>()

let trendChart: echarts.ECharts | null = null
let severityChart: echarts.ECharts | null = null
let sourceChart: echarts.ECharts | null = null

const severityColor: Record<string, string> = {
  critical: '#dc2626',
  warning: '#d97706',
  info: '#2563eb'
}

const severityLabel: Record<string, string> = {
  critical: '严重',
  warning: '警告',
  info: '提示'
}

const statusLabel: Record<string, string> = {
  firing: '触发中',
  acked: '已确认',
  resolved: '已恢复'
}

async function load() {
  loading.value = true
  try {
    data.value = await getAlertSituation(range.value)
    renderCharts()
  } finally {
    loading.value = false
  }
}

function renderCharts() {
  if (!data.value) return

  trendChart?.setOption(
    {
      tooltip: { trigger: 'axis' },
      legend: { data: ['严重', '警告', '提示'] },
      grid: { left: 40, right: 20, top: 40, bottom: 30 },
      xAxis: { type: 'category', data: data.value.trend.map((t) => t.time) },
      yAxis: { type: 'value', minInterval: 1 },
      series: [
        {
          name: '严重',
          type: 'line',
          stack: 'total',
          areaStyle: {},
          itemStyle: { color: severityColor.critical },
          data: data.value.trend.map((t) => t.critical)
        },
        {
          name: '警告',
          type: 'line',
          stack: 'total',
          areaStyle: {},
          itemStyle: { color: severityColor.warning },
          data: data.value.trend.map((t) => t.warning)
        },
        {
          name: '提示',
          type: 'line',
          stack: 'total',
          areaStyle: {},
          itemStyle: { color: severityColor.info },
          data: data.value.trend.map((t) => t.info)
        }
      ]
    },
    true
  )

  severityChart?.setOption(
    {
      tooltip: { trigger: 'item' },
      series: [
        {
          type: 'pie',
          radius: ['45%', '70%'],
          data: data.value.bySeverity.map((item) => ({
            name: severityLabel[item.name] || item.name,
            value: item.count,
            itemStyle: { color: severityColor[item.name] }
          }))
        }
      ]
    },
    true
  )

  sourceChart?.setOption(
    {
      tooltip: { trigger: 'axis' },
      grid: { left: 90, right: 20, top: 20, bottom: 30 },
      xAxis: { type: 'value', minInterval: 1 },
      yAxis: { type: 'category', data: data.value.bySource.map((item) => item.name) },
      series: [
        {
          type: 'bar',
          barMaxWidth: 20,
          itemStyle: { color: '#2563eb' },
          data: data.value.bySource.map((item) => item.count)
        }
      ]
    },
    true
  )
}

function formatDuration(sec: number) {
  if (!sec) return '-'
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`
  return `${Math.floor(sec / 3600)}h${Math.floor((sec % 3600) / 60)}m`
}

function handleResize() {
  trendChart?.resize()
  severityChart?.resize()
  sourceChart?.resize()
}

watch(range, load)

onMounted(() => {
  trendChart = echarts.init(trendBox.value!)
  severityChart = echarts.init(severityBox.value!)
  sourceChart = echarts.init(sourceBox.value!)
  window.addEventListener('resize', handleResize)
  load()
})

// 页签工作台用 keep-alive 缓存页面：切走时组件不卸载，DOM 被挪进离屏容器。
// 此时 resize 事件仍会打进来，echarts 从 clientWidth 拿到 0 就把画布缩成 0×0，
// 切回来是空白图。所以监听跟着 activate/deactivate 走，回来时补一次 resize。
onActivated(() => {
  window.addEventListener('resize', handleResize)
  handleResize()
})
onDeactivated(() => window.removeEventListener('resize', handleResize))

onBeforeUnmount(() => {
  window.removeEventListener('resize', handleResize)
  trendChart?.dispose()
  severityChart?.dispose()
  sourceChart?.dispose()
})
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <div class="page-toolbar">
        <el-radio-group v-model="range">
          <el-radio-button value="24h">近 24 小时</el-radio-button>
          <el-radio-button value="7d">近 7 天</el-radio-button>
          <el-radio-button value="30d">近 30 天</el-radio-button>
        </el-radio-group>
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <span style="color: #6b7280">窗口内新增告警 {{ data?.total ?? 0 }} 条</span>
      </div>

      <el-row :gutter="12">
        <el-col :span="6">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ data?.handleStats.ackedNum ?? 0 }}</div>
            <div class="label">已确认告警数</div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ formatDuration(data?.handleStats.avgAckSec ?? 0) }}</div>
            <div class="label">平均确认时长</div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ data?.handleStats.resolvedNum ?? 0 }}</div>
            <div class="label">已恢复告警数</div>
          </el-card>
        </el-col>
        <el-col :span="6">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ formatDuration(data?.handleStats.avgResolveSec ?? 0) }}</div>
            <div class="label">平均恢复时长</div>
          </el-card>
        </el-col>
      </el-row>

      <el-divider content-position="left">告警趋势</el-divider>
      <div ref="trendBox" style="height: 300px"></div>
    </el-card>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="8">
        <el-card header="级别分布">
          <div ref="severityBox" style="height: 240px"></div>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card header="来源分布">
          <div ref="sourceBox" style="height: 240px"></div>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card header="Top 标签">
          <el-empty v-if="!data?.topLabels?.length" description="窗口内没有带标签的告警" :image-size="60" />
          <div v-else>
            <div
              v-for="item in data.topLabels"
              :key="item.name"
              style="display: flex; justify-content: space-between; padding: 5px 0"
            >
              <span style="font-family: Consolas, monospace; font-size: 13px">{{ item.name }}</span>
              <el-tag size="small">{{ item.count }}</el-tag>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px" header="状态分布">
      <el-empty v-if="!data?.byStatus?.length" description="窗口内没有告警" :image-size="60" />
      <el-row v-else :gutter="12">
        <el-col v-for="item in data.byStatus" :key="item.name" :span="6">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ item.count }}</div>
            <div class="label">{{ statusLabel[item.name] || item.name }}</div>
          </el-card>
        </el-col>
      </el-row>
    </el-card>
  </div>
</template>
