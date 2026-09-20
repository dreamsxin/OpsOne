<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as echarts from 'echarts'
import {
  checkMetricSource,
  createMetricSource,
  createSavedMetricQuery,
  deleteMetricSource,
  deleteSavedMetricQuery,
  listMetricNames,
  listMetricSources,
  listSavedMetricQueries,
  queryMetricInstant,
  queryMetricRange,
  updateMetricSource,
  type MetricQueryResult,
  type MetricSource,
  type SavedMetricQuery
} from '@/api'

const sources = ref<MetricSource[]>([])
const saved = ref<SavedMetricQuery[]>([])
const metricNames = ref<string[]>([])

const query = reactive({
  sourceId: 0,
  expr: '',
  rangeMode: true,
  rangeSec: 3600,
  loading: false
})
const result = ref<MetricQueryResult | null>(null)
const errorMsg = ref('')

const rangeOptions = [
  { label: '最近 5 分钟', value: 300 },
  { label: '最近 30 分钟', value: 1800 },
  { label: '最近 1 小时', value: 3600 },
  { label: '最近 6 小时', value: 21600 },
  { label: '最近 24 小时', value: 86400 },
  { label: '最近 7 天', value: 604800 }
]

const currentSource = computed(() => sources.value.find((s) => s.id === query.sourceId))
const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  healthy: { text: '可用', type: 'success' },
  error: { text: '不可用', type: 'danger' },
  unknown: { text: '未检查', type: 'info' }
}

const chartBox = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

async function loadSources() {
  sources.value = await listMetricSources()
  if (!query.sourceId) {
    const preferred = sources.value.find((s) => s.isDefault && s.enabled) ||
      sources.value.find((s) => s.enabled)
    query.sourceId = preferred?.id || 0
  }
  if (query.sourceId) loadNames()
}

async function loadSaved() {
  saved.value = await listSavedMetricQueries()
}

// 指标名只在切数据源时拉一次，本地做过滤：Prometheus 那边可能有几万个名字
async function loadNames() {
  metricNames.value = []
  if (!query.sourceId) return
  try {
    const res = await listMetricNames(query.sourceId)
    metricNames.value = res.names || []
  } catch {
    // 拉不到指标名不影响手写 PromQL
  }
}

function onSourceChange() {
  result.value = null
  errorMsg.value = ''
  loadNames()
}

function pickName(name: string) {
  query.expr = query.expr ? `${query.expr}${name}` : name
}

async function run() {
  if (!query.sourceId) {
    ElMessage.warning('请先选择数据源')
    return
  }
  if (!query.expr.trim()) {
    ElMessage.warning('请填写 PromQL 表达式')
    return
  }
  query.loading = true
  errorMsg.value = ''
  try {
    if (query.rangeMode) {
      const end = Math.floor(Date.now() / 1000)
      result.value = await queryMetricRange({
        sourceId: query.sourceId,
        query: query.expr,
        start: end - query.rangeSec,
        end
      })
      await nextTick()
      drawChart()
    } else {
      result.value = await queryMetricInstant({ sourceId: query.sourceId, query: query.expr })
      disposeChart()
    }
  } catch (err: any) {
    result.value = null
    errorMsg.value = err?.message || '查询失败'
  } finally {
    query.loading = false
  }
}

function disposeChart() {
  chart?.dispose()
  chart = null
}

function drawChart() {
  const series = result.value?.series || []
  if (!series.length || !chartBox.value) {
    disposeChart()
    return
  }
  if (!chart) chart = echarts.init(chartBox.value)
  chart.setOption(
    {
      tooltip: { trigger: 'axis' },
      legend: { type: 'scroll', top: 0, data: series.map((s) => s.name) },
      grid: { left: 60, right: 30, top: 40, bottom: 40 },
      xAxis: { type: 'time' },
      yAxis: { type: 'value', scale: true },
      series: series.map((s) => ({
        name: s.name,
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: s.points.map((p) => [p.at, Number(p.value)])
      }))
    },
    true
  )
  chart.resize()
}

// ---------- 常用查询 ----------
function applySaved(item: SavedMetricQuery) {
  query.expr = item.expr
  query.rangeMode = item.rangeMode
  if (item.sourceId && sources.value.some((s) => s.id === item.sourceId)) {
    query.sourceId = item.sourceId
  }
  run()
}

async function saveCurrent() {
  if (!query.expr.trim()) {
    ElMessage.warning('先写一条表达式再保存')
    return
  }
  try {
    const { value } = await ElMessageBox.prompt('给这条查询起个名字', '保存为常用查询', {
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputValidator: (v: string) => (v && v.trim() ? true : '名称不能为空')
    })
    await createSavedMetricQuery({
      name: value.trim(),
      sourceId: query.sourceId,
      expr: query.expr,
      rangeMode: query.rangeMode
    })
    ElMessage.success('已保存')
    loadSaved()
  } catch {
    // 取消
  }
}

async function removeSaved(item: SavedMetricQuery) {
  try {
    await ElMessageBox.confirm(`删除常用查询「${item.name}」？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await deleteSavedMetricQuery(item.id)
  ElMessage.success('已删除')
  loadSaved()
}

// ---------- 数据源管理 ----------
const sourceDialog = reactive({
  visible: false,
  editingId: null as number | null,
  form: {
    name: '',
    baseUrl: '',
    headerKey: '',
    headerValue: '',
    timeoutSec: 15,
    isDefault: false,
    enabled: true,
    remark: ''
  }
})

function openSourceCreate() {
  sourceDialog.editingId = null
  Object.assign(sourceDialog.form, {
    name: '',
    baseUrl: '',
    headerKey: '',
    headerValue: '',
    timeoutSec: 15,
    isDefault: sources.value.length === 0,
    enabled: true,
    remark: ''
  })
  sourceDialog.visible = true
}

function openSourceEdit(row: MetricSource) {
  sourceDialog.editingId = row.id
  Object.assign(sourceDialog.form, {
    name: row.name,
    baseUrl: row.baseUrl,
    headerKey: row.headerKey,
    headerValue: '',
    timeoutSec: row.timeoutSec,
    isDefault: row.isDefault,
    enabled: row.enabled,
    remark: row.remark
  })
  sourceDialog.visible = true
}

async function submitSource() {
  try {
    if (sourceDialog.editingId) {
      await updateMetricSource(sourceDialog.editingId, { ...sourceDialog.form })
      ElMessage.success('已保存')
    } else {
      const res = await createMetricSource({ ...sourceDialog.form })
      if (res.check?.status === 'healthy') {
        ElMessage.success(`已接入：${res.check.detail}`)
      } else {
        ElMessage.warning(`已保存，但连不上：${res.check?.detail}`)
      }
    }
    sourceDialog.visible = false
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function checkSource(row: MetricSource) {
  try {
    const res = await checkMetricSource(row.id)
    if (res.status === 'healthy') ElMessage.success(res.detail)
    else ElMessage.error(res.detail)
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '检查失败')
  }
}

async function removeSource(row: MetricSource) {
  try {
    await ElMessageBox.confirm(`删除数据源「${row.name}」？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  try {
    await deleteMetricSource(row.id)
    ElMessage.success('已删除')
    if (query.sourceId === row.id) query.sourceId = 0
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '删除失败')
  }
}

const manageVisible = ref(false)

function onResize() {
  chart?.resize()
}

onMounted(async () => {
  await loadSources()
  await loadSaved()
  window.addEventListener('resize', onResize)
})
onUnmounted(() => {
  window.removeEventListener('resize', onResize)
  disposeChart()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="直接查询已有的 Prometheus：即时查询看当前值（表格），范围查询看趋势（折线图，步长按时间范围自动算）。平台不存时序数据，也不改写 PromQL，报错原样来自 Prometheus"
      />

      <div class="page-toolbar">
        <el-select
          v-model="query.sourceId"
          placeholder="选择数据源"
          style="width: 200px"
          @change="onSourceChange"
        >
          <el-option
            v-for="item in sources"
            :key="item.id"
            :label="item.name + (item.enabled ? '' : '（已停用）')"
            :value="item.id"
            :disabled="!item.enabled"
          />
        </el-select>
        <el-tag v-if="currentSource" size="small" :type="statusMeta[currentSource.status]?.type || 'info'">
          {{ statusMeta[currentSource.status]?.text }}
          <span v-if="currentSource.version">· {{ currentSource.version }}</span>
        </el-tag>
        <div class="grow"></div>
        <el-button v-perm="'metric:manage'" @click="manageVisible = true">数据源管理</el-button>
      </div>

      <div class="query-row">
        <el-input
          v-model="query.expr"
          type="textarea"
          :rows="2"
          spellcheck="false"
          class="expr"
          placeholder='PromQL，例如 up 或 rate(prometheus_http_requests_total[5m])'
        />
        <div class="query-side">
          <el-select
            filterable
            :model-value="''"
            placeholder="插入指标名"
            style="width: 220px"
            @change="pickName"
          >
            <el-option v-for="name in metricNames" :key="name" :label="name" :value="name" />
          </el-select>
          <el-radio-group v-model="query.rangeMode">
            <el-radio-button :value="false">即时</el-radio-button>
            <el-radio-button :value="true">范围</el-radio-button>
          </el-radio-group>
          <el-select v-if="query.rangeMode" v-model="query.rangeSec" style="width: 140px">
            <el-option v-for="item in rangeOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
          <el-button type="primary" :loading="query.loading" @click="run">查询</el-button>
          <el-button v-perm="'metric:manage'" @click="saveCurrent">存为常用</el-button>
        </div>
      </div>

      <div v-if="saved.length" class="saved-row">
        <span class="hint">常用查询：</span>
        <el-tag
          v-for="item in saved"
          :key="item.id"
          class="saved-tag"
          :closable="true"
          @click="applySaved(item)"
          @close="removeSaved(item)"
        >
          {{ item.name }}
        </el-tag>
      </div>

      <el-alert v-if="errorMsg" type="error" :closable="false" :title="errorMsg" style="margin: 12px 0" />
      <el-alert
        v-if="result?.warnings?.length"
        type="warning"
        :closable="false"
        style="margin: 12px 0"
        :title="`Prometheus 警告：${result.warnings.join('；')}`"
      />
      <el-alert
        v-if="result?.truncated"
        type="warning"
        :closable="false"
        style="margin: 12px 0"
        :title="`结果共 ${result.total} 条曲线，只显示前 60 条，请把查询收窄（加标签过滤或 topk）`"
      />

      <template v-if="result">
        <div class="meta">
          {{ result.resultType }} · {{ result.total }} 条曲线 · {{ result.costMs }}ms
          <span v-if="result.step">· 步长 {{ result.step }}s</span>
          <span v-if="result.start">· {{ result.start.slice(11, 19) }} ~ {{ result.end?.slice(11, 19) }}</span>
          <span v-if="result.queriedAt">· 时刻 {{ result.queriedAt.slice(11, 19) }}</span>
        </div>
        <div v-show="query.rangeMode && result.series.length" ref="chartBox" class="chart"></div>
        <el-table :data="result.series" border stripe size="small" max-height="420">
          <el-table-column prop="name" label="序列" min-width="320" show-overflow-tooltip />
          <el-table-column prop="value" label="当前值" width="180" />
          <el-table-column v-if="query.rangeMode" label="点数" width="90">
            <template #default="{ row }">{{ row.points.length }}</template>
          </el-table-column>
        </el-table>
        <el-empty v-if="!result.series.length" description="没有数据：表达式没匹配到任何序列，或该时间范围内没有采样" />
      </template>
    </el-card>

    <el-drawer v-model="manageVisible" size="60%" title="指标数据源">
      <div class="page-toolbar">
        <el-button v-perm="'metric:manage'" type="primary" @click="openSourceCreate">新增数据源</el-button>
      </div>
      <el-table :data="sources" border stripe size="small">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column prop="baseUrl" label="地址" min-width="200" show-overflow-tooltip />
        <el-table-column label="状态" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text }}
            </el-tag>
            <span v-if="row.version" class="hint" style="margin-left: 4px">{{ row.version }}</span>
          </template>
        </el-table-column>
        <el-table-column label="序列数" width="100">
          <template #default="{ row }">{{ row.seriesCount || '—' }}</template>
        </el-table-column>
        <el-table-column label="默认" width="70">
          <template #default="{ row }">
            <el-tag v-if="row.isDefault" size="small" type="success">默认</el-tag>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastError" label="最近错误" min-width="160" show-overflow-tooltip />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'metric:manage'" link type="primary" @click="checkSource(row)">检查</el-button>
            <el-button v-perm="'metric:manage'" link type="primary" @click="openSourceEdit(row)">编辑</el-button>
            <el-button v-perm="'metric:manage'" link type="danger" @click="removeSource(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <el-dialog
      v-model="sourceDialog.visible"
      :title="sourceDialog.editingId ? '编辑数据源' : '新增数据源'"
      width="520px"
    >
      <el-form :model="sourceDialog.form" label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="sourceDialog.form.name" placeholder="例如 prod-prometheus" />
        </el-form-item>
        <el-form-item label="地址">
          <el-input v-model="sourceDialog.form.baseUrl" placeholder="http://prometheus:9090" />
          <div class="hint">只填根地址，不要带 /api/v1</div>
        </el-form-item>
        <el-form-item label="鉴权头名">
          <el-input v-model="sourceDialog.form.headerKey" placeholder="可选，如 Authorization" />
        </el-form-item>
        <el-form-item label="鉴权头值">
          <el-input
            v-model="sourceDialog.form.headerValue"
            type="password"
            show-password
            :placeholder="sourceDialog.editingId ? '留空表示不修改' : '可选'"
          />
        </el-form-item>
        <el-form-item label="超时">
          <el-input-number v-model="sourceDialog.form.timeoutSec" :min="1" :max="120" />
          <span class="hint" style="margin-left: 8px">秒</span>
        </el-form-item>
        <el-form-item label="设为默认">
          <el-switch v-model="sourceDialog.form.isDefault" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="sourceDialog.form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="sourceDialog.form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="sourceDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="submitSource">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.grow {
  flex: 1;
}
.query-row {
  display: flex;
  gap: 12px;
  align-items: flex-start;
  margin-bottom: 8px;
}
.expr {
  flex: 1;
}
.expr :deep(textarea) {
  font-family: Consolas, Monaco, monospace;
  font-size: 13px;
}
.query-side {
  display: flex;
  flex-direction: column;
  gap: 8px;
  width: 230px;
}
.saved-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
}
.saved-tag {
  cursor: pointer;
}
.meta {
  margin: 8px 0;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.chart {
  height: 320px;
  margin-bottom: 12px;
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
