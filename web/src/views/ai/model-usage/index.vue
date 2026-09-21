<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as echarts from 'echarts'
import {
  exportModelCallsCSV,
  getModelUsage,
  listModelUpstreams,
  type ModelPoolStat,
  type ModelUsageOverview
} from '@/api'

const loading = ref(false)
const data = ref<ModelUsageOverview | null>(null)
const pools = ref<ModelPoolStat[]>([])

// 快捷区间：算出的是「本地日期」，和后端按本地时区分桶保持一致
const range = ref('7d')
const query = reactive({
  start: '',
  end: '',
  bucket: 'day' as 'day' | 'hour',
  alias: '',
  caller: '',
  username: ''
})

const trendBox = ref<HTMLDivElement>()
let trendChart: echarts.ECharts | null = null

const aliasOptions = computed(() => pools.value.map((p) => p.alias))

function ymd(d: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

function applyRange() {
  const today = new Date()
  if (range.value === 'custom') return
  const days = range.value === 'today' ? 0 : range.value === '7d' ? 6 : 29
  const from = new Date(today)
  from.setDate(from.getDate() - days)
  query.start = ymd(from)
  query.end = ymd(today)
  // 只看今天时按小时看才有意义，跨天按天
  query.bucket = range.value === 'today' ? 'hour' : 'day'
}

async function load() {
  loading.value = true
  try {
    data.value = await getModelUsage(query)
    renderTrend()
  } catch (err: any) {
    ElMessage.error(err?.message || '统计失败')
  } finally {
    loading.value = false
  }
}

function renderTrend() {
  if (!trendChart || !data.value) return
  const trend = data.value.trend || []
  trendChart.setOption({
    tooltip: { trigger: 'axis' },
    legend: { data: ['调用次数', '失败', '成本(元)'] },
    grid: { left: 50, right: 60, top: 40, bottom: 40 },
    xAxis: { type: 'category', data: trend.map((x) => x.label) },
    yAxis: [
      { type: 'value', name: '次数' },
      { type: 'value', name: '成本(元)', position: 'right' }
    ],
    series: [
      {
        name: '调用次数',
        type: 'bar',
        data: trend.map((x) => x.calls),
        itemStyle: { color: '#409eff' }
      },
      {
        name: '失败',
        type: 'bar',
        stack: 'fail',
        data: trend.map((x) => x.failed),
        itemStyle: { color: '#f56c6c' }
      },
      {
        name: '成本(元)',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        data: trend.map((x) => x.cost),
        itemStyle: { color: '#e6a23c' }
      }
    ]
  })
}

async function exportCSV() {
  try {
    await exportModelCallsCSV(query)
    ElMessage.success('已开始下载，单次最多导出 10000 条')
  } catch (err: any) {
    ElMessage.error(err?.message || '导出失败')
  }
}

function money(value: number) {
  if (!value) return '0'
  return value < 0.01 ? value.toFixed(6) : value.toFixed(4)
}

function handleResize() {
  trendChart?.resize()
}

watch(range, () => {
  applyRange()
  load()
})

onMounted(async () => {
  applyRange()
  trendChart = echarts.init(trendBox.value!)
  window.addEventListener('resize', handleResize)
  try {
    pools.value = (await listModelUpstreams()).pools || []
  } catch {
    // 拿不到池子列表不影响看用量，筛选框留空即可
  }
  load()
})

// 缓存页切走后 DOM 在离屏容器里，此时 resize 会把 echarts 缩成 0×0、切回来空白，
// 所以监听跟着 activate/deactivate 走，回来补一次 resize
onActivated(() => {
  window.addEventListener('resize', handleResize)
  handleResize()
})
onDeactivated(() => window.removeEventListener('resize', handleResize))

onBeforeUnmount(() => {
  window.removeEventListener('resize', handleResize)
  trendChart?.dispose()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" show-icon class="notice">
        数据来自 AI 网关的调用流水（含失败与探活）。
        <strong>成本是按上游登记的单价算出的估值，不是供应商账单</strong>；token 只认上游返回的 usage，
        没返回的那些调用 token 记 0 并单独计数；<strong>只统计走网关的调用</strong>，绕开平台直连供应商的用量这里看不到。
      </el-alert>

      <div class="page-toolbar">
        <el-radio-group v-model="range">
          <el-radio-button value="today">今天</el-radio-button>
          <el-radio-button value="7d">近 7 天</el-radio-button>
          <el-radio-button value="30d">近 30 天</el-radio-button>
          <el-radio-button value="custom">自定义</el-radio-button>
        </el-radio-group>
        <template v-if="range === 'custom'">
          <el-date-picker
            v-model="query.start"
            type="date"
            value-format="YYYY-MM-DD"
            placeholder="开始日期"
            style="width: 150px"
          />
          <el-date-picker
            v-model="query.end"
            type="date"
            value-format="YYYY-MM-DD"
            placeholder="结束日期"
            style="width: 150px"
          />
          <el-select v-model="query.bucket" style="width: 110px">
            <el-option label="按天" value="day" />
            <el-option label="按小时" value="hour" />
          </el-select>
        </template>
        <el-select v-model="query.alias" placeholder="全部模型" clearable style="width: 150px">
          <el-option v-for="item in aliasOptions" :key="item" :label="item" :value="item" />
        </el-select>
        <el-select v-model="query.caller" placeholder="全部来源" clearable style="width: 140px">
          <el-option label="接口调用" value="api" />
          <el-option label="界面试调用" value="console" />
          <el-option label="探活检查" value="check" />
        </el-select>
        <el-input v-model="query.username" placeholder="调用人" clearable style="width: 130px" />
        <el-button @click="load">查询</el-button>
        <div class="flex-1" />
        <el-button @click="exportCSV">导出流水 CSV</el-button>
      </div>

      <div v-loading="loading">
        <div v-if="data" class="kpis">
          <el-card shadow="never" class="kpi">
            <div class="kpi-label">调用次数</div>
            <div class="kpi-value">{{ data.summary.calls }}</div>
            <div class="kpi-sub">
              失败 {{ data.summary.failed }} · 成功率 {{ data.summary.successRate }}%
            </div>
          </el-card>
          <el-card shadow="never" class="kpi">
            <div class="kpi-label">成本（元，估值）</div>
            <div class="kpi-value">{{ money(data.summary.cost) }}</div>
            <div class="kpi-sub">按上游登记单价算，不等于账单</div>
          </el-card>
          <el-card shadow="never" class="kpi">
            <div class="kpi-label">Token</div>
            <div class="kpi-value">{{ data.summary.tokens }}</div>
            <div class="kpi-sub">
              入 {{ data.summary.promptTokens }} / 出 {{ data.summary.completionTokens }}
            </div>
          </el-card>
          <el-card shadow="never" class="kpi">
            <div class="kpi-label">耗时</div>
            <div class="kpi-value">{{ data.summary.avgLatencyMs }} ms</div>
            <div class="kpi-sub">只算成功调用 · 最慢 {{ data.summary.maxLatencyMs }} ms</div>
          </el-card>
          <el-card shadow="never" class="kpi">
            <div class="kpi-label">需要留意</div>
            <div class="kpi-value">{{ data.summary.usageMissing }}</div>
            <div class="kpi-sub">
              上游没给 usage（token/成本按 0 计）· 切换重试 {{ data.summary.retried }} 次
            </div>
          </el-card>
        </div>

        <el-alert
          v-if="data?.truncated"
          type="warning"
          :closable="false"
          show-icon
          class="notice"
        >
          这个区间的流水超过 {{ data.rowsScanned }} 条，趋势图只按前 {{ data.rowsScanned }} 条画 ——
          把时间范围收窄再看。
        </el-alert>

        <div ref="trendBox" class="chart"></div>

        <div class="ranks">
          <el-card shadow="never">
            <template #header>按模型（Alias）· 成本从高到低</template>
            <el-table :data="data?.byAlias || []" size="small">
              <el-table-column label="模型" prop="name" min-width="120" />
              <el-table-column label="调用" width="90">
                <template #default="{ row }">
                  {{ row.calls }}<span v-if="row.failed" class="danger">（失败 {{ row.failed }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="token" prop="tokens" width="90" />
              <el-table-column label="成本(元)" width="110">
                <template #default="{ row }">{{ money(row.cost) }}</template>
              </el-table-column>
              <el-table-column label="平均耗时" width="100">
                <template #default="{ row }">{{ row.avgLatencyMs }} ms</template>
              </el-table-column>
            </el-table>
          </el-card>

          <el-card shadow="never">
            <template #header>按上游</template>
            <el-table :data="data?.byUpstream || []" size="small">
              <el-table-column label="上游" prop="name" min-width="120" />
              <el-table-column label="调用" width="90">
                <template #default="{ row }">
                  {{ row.calls }}<span v-if="row.failed" class="danger">（失败 {{ row.failed }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="token" prop="tokens" width="90" />
              <el-table-column label="成本(元)" width="110">
                <template #default="{ row }">{{ money(row.cost) }}</template>
              </el-table-column>
              <el-table-column label="平均耗时" width="100">
                <template #default="{ row }">{{ row.avgLatencyMs }} ms</template>
              </el-table-column>
            </el-table>
          </el-card>

          <el-card shadow="never">
            <template #header>按调用人</template>
            <el-table :data="data?.byUser || []" size="small">
              <el-table-column label="调用人" prop="name" min-width="120" />
              <el-table-column label="调用" width="90">
                <template #default="{ row }">
                  {{ row.calls }}<span v-if="row.failed" class="danger">（失败 {{ row.failed }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="token" prop="tokens" width="90" />
              <el-table-column label="成本(元)" width="110">
                <template #default="{ row }">{{ money(row.cost) }}</template>
              </el-table-column>
              <el-table-column label="平均耗时" width="100">
                <template #default="{ row }">{{ row.avgLatencyMs }} ms</template>
              </el-table-column>
            </el-table>
          </el-card>
        </div>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
}
.flex-1 {
  flex: 1;
}
.kpis {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 12px;
}
.kpi-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.kpi-value {
  font-size: 24px;
  font-weight: 600;
  margin: 4px 0;
}
.kpi-sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.4;
}
.chart {
  height: 300px;
  margin-bottom: 12px;
}
.ranks {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
  gap: 12px;
}
.danger {
  color: var(--el-color-danger);
  font-size: 12px;
}
</style>
