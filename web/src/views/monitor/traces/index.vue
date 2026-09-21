<script setup lang="ts">
import { computed, onActivated, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  checkTraceSource,
  createTraceSource,
  deleteTraceSource,
  getTraceDetail,
  listTraceServices,
  listTraceSources,
  searchTraces,
  updateTraceSource,
  type TraceSource,
  type TraceSpan,
  type TraceSummary
} from '@/api'

const sources = ref<TraceSource[]>([])
const services = ref<string[]>([])
const operations = ref<string[]>([])

const query = reactive({
  sourceId: 0,
  service: '',
  operation: '',
  rangeSec: 3600,
  minDurationMs: 0,
  limit: 20,
  errorOnly: false,
  loading: false
})
const traces = ref<TraceSummary[]>([])
const meta = ref<{ total: number; costMs: number; start: string; end: string } | null>(null)
const errorMsg = ref('')

const rangeOptions = [
  { label: '最近 15 分钟', value: 900 },
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

// ---------- trace 详情 ----------
const detail = reactive({
  visible: false,
  loading: false,
  traceId: '',
  summary: null as TraceSummary | null,
  spans: [] as TraceSpan[],
  truncated: false,
  selected: null as TraceSpan | null
})
const traceIdInput = ref('')

function rangeParams() {
  const end = Math.floor(Date.now() / 1000)
  return { start: end - query.rangeSec, end }
}

async function loadSources() {
  sources.value = await listTraceSources()
  if (!query.sourceId) {
    const preferred = sources.value.find((s) => s.isDefault && s.enabled) ||
      sources.value.find((s) => s.enabled)
    query.sourceId = preferred?.id || 0
  }
  if (query.sourceId) loadServices()
}

async function loadServices() {
  services.value = []
  operations.value = []
  query.service = ''
  query.operation = ''
  if (!query.sourceId) return
  try {
    const res = await listTraceServices(query.sourceId)
    services.value = res.values || []
    if (services.value.length === 1) {
      query.service = services.value[0]
      loadOperations()
    }
  } catch {
    // 服务列表拉不到不影响按 TraceID 查
  }
}

async function loadOperations() {
  operations.value = []
  query.operation = ''
  if (!query.service) return
  try {
    const res = await listTraceServices(query.sourceId, query.service)
    operations.value = res.values || []
  } catch {
    // 操作名可选
  }
}

async function run() {
  if (!query.sourceId) {
    ElMessage.warning('请先选择数据源')
    return
  }
  if (!query.service) {
    ElMessage.warning('请选择服务')
    return
  }
  query.loading = true
  errorMsg.value = ''
  try {
    const res = await searchTraces({
      sourceId: query.sourceId,
      service: query.service,
      ...(query.operation ? { operation: query.operation } : {}),
      ...(query.minDurationMs ? { minDurationMs: query.minDurationMs } : {}),
      ...(query.errorOnly ? { errorOnly: 'true' } : {}),
      limit: query.limit,
      ...rangeParams()
    })
    traces.value = res.items || []
    meta.value = { total: res.total, costMs: res.costMs, start: res.start, end: res.end }
  } catch (err: any) {
    traces.value = []
    meta.value = null
    errorMsg.value = err?.message || '查询失败'
  } finally {
    query.loading = false
  }
}

async function openTrace(traceId: string) {
  if (!traceId.trim()) {
    ElMessage.warning('请填写 TraceID')
    return
  }
  detail.visible = true
  detail.loading = true
  detail.traceId = traceId.trim()
  detail.summary = null
  detail.spans = []
  detail.selected = null
  try {
    const res = await getTraceDetail(detail.traceId, query.sourceId)
    detail.summary = res.summary
    detail.spans = res.spans || []
    detail.truncated = res.truncated
  } catch (err: any) {
    ElMessage.error(err?.message || '读取链路失败')
    detail.visible = false
  } finally {
    detail.loading = false
  }
}

// 瀑布图按整条 trace 的跨度换算百分比
const traceSpan = computed(() => Math.max(detail.summary?.durationMs || 1, 0.001))
function barStyle(span: TraceSpan) {
  const left = (span.offsetMs / traceSpan.value) * 100
  const width = Math.max((span.durationMs / traceSpan.value) * 100, 0.4)
  return {
    marginLeft: `${Math.min(left, 99.6)}%`,
    width: `${Math.min(width, 100 - Math.min(left, 99.6))}%`
  }
}

function formatTime(at: number) {
  const d = new Date(at)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// ---------- 数据源管理 ----------
const manageVisible = ref(false)
const sourceDialog = reactive({
  visible: false,
  editingId: null as number | null,
  form: {
    name: '',
    baseUrl: '',
    headerKey: '',
    headerValue: '',
    timeoutSec: 20,
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
    timeoutSec: 20,
    isDefault: sources.value.length === 0,
    enabled: true,
    remark: ''
  })
  sourceDialog.visible = true
}

function openSourceEdit(row: TraceSource) {
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
      await updateTraceSource(sourceDialog.editingId, { ...sourceDialog.form })
      ElMessage.success('已保存')
    } else {
      const res = await createTraceSource({ ...sourceDialog.form })
      if (res.check?.status === 'healthy') ElMessage.success(`已接入：${res.check.detail}`)
      else ElMessage.warning(`已保存，但连不上：${res.check?.detail}`)
    }
    sourceDialog.visible = false
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function check(row: TraceSource) {
  try {
    const res = await checkTraceSource(row.id)
    if (res.status === 'healthy') ElMessage.success(res.detail)
    else ElMessage.error(res.detail)
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '检查失败')
  }
}

async function removeSource(row: TraceSource) {
  try {
    await ElMessageBox.confirm(`删除链路数据源「${row.name}」？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await deleteTraceSource(row.id)
  ElMessage.success('已删除')
  if (query.sourceId === row.id) query.sourceId = 0
  loadSources()
}

// 告警详情的「在 Jaeger 中查看 Trace」带 ?traceId=xxx 跳进来：数据源就绪后直接打开详情。
// 本页会被页签工作台缓存，再次跳进来不重新挂载，所以 activate 时也认一次。
const route = useRoute()
let appliedTraceId = ''
function applyDeepLink() {
  const id = String(route.query.traceId ?? '')
  if (!id || id === appliedTraceId) return
  appliedTraceId = id
  traceIdInput.value = id
  openTrace(id)
}
onMounted(async () => {
  await loadSources()
  applyDeepLink()
})
onActivated(applyDeepLink)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="查询已有的 Jaeger：按服务/操作/耗时筛 trace，点进去看 span 瀑布图与标签。日志里看到 trace_id 也可以直接粘进右边的框查。平台不接收上报、不存链路数据"
      />

      <div class="page-toolbar">
        <el-select
          v-model="query.sourceId"
          placeholder="选择数据源"
          style="width: 190px"
          @change="loadServices"
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
          <span v-if="currentSource.serviceCount">· {{ currentSource.serviceCount }} 个服务</span>
        </el-tag>
        <div class="grow"></div>
        <el-input
          v-model="traceIdInput"
          placeholder="粘 TraceID 直接查"
          style="width: 260px"
          @keyup.enter="openTrace(traceIdInput)"
        >
          <template #append>
            <el-button @click="openTrace(traceIdInput)">查</el-button>
          </template>
        </el-input>
        <el-button v-perm="'trace:manage'" @click="manageVisible = true">数据源管理</el-button>
      </div>

      <div class="page-toolbar">
        <el-select
          v-model="query.service"
          filterable
          clearable
          placeholder="服务"
          style="width: 200px"
          @change="loadOperations"
        >
          <el-option v-for="name in services" :key="name" :label="name" :value="name" />
        </el-select>
        <el-select
          v-model="query.operation"
          filterable
          clearable
          :disabled="!query.service"
          placeholder="操作（可选）"
          style="width: 220px"
        >
          <el-option v-for="name in operations" :key="name" :label="name" :value="name" />
        </el-select>
        <el-select v-model="query.rangeSec" style="width: 130px">
          <el-option v-for="item in rangeOptions" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
        <el-input-number
          v-model="query.minDurationMs"
          :min="0"
          :max="600000"
          :step="100"
          controls-position="right"
          style="width: 130px"
        />
        <span class="hint">ms 起（0 不限）</span>
        <el-select v-model="query.limit" style="width: 100px">
          <el-option :value="20" label="20 条" />
          <el-option :value="50" label="50 条" />
          <el-option :value="200" label="200 条" />
        </el-select>
        <el-checkbox v-model="query.errorOnly">只看有出错 span 的</el-checkbox>
        <el-button type="primary" :loading="query.loading" @click="run">查询</el-button>
      </div>

      <el-alert v-if="errorMsg" type="error" :closable="false" :title="errorMsg" style="margin: 12px 0" />

      <div v-if="meta" class="meta">
        {{ meta.total }} 条链路 · {{ meta.costMs }}ms ·
        {{ meta.start.slice(5, 19).replace('T', ' ') }} ~ {{ meta.end.slice(11, 19) }}
      </div>

      <el-table v-loading="query.loading" :data="traces" border stripe>
        <el-table-column label="TraceID" min-width="180">
          <template #default="{ row }">
            <el-button link type="primary" @click="openTrace(row.traceId)">
              {{ row.traceId.slice(0, 16) }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column label="入口" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ row.rootService }} · {{ row.rootOperation }}</template>
        </el-table-column>
        <el-table-column label="耗时" width="110">
          <template #default="{ row }">{{ row.durationMs.toFixed(1) }} ms</template>
        </el-table-column>
        <el-table-column label="Span" width="90">
          <template #default="{ row }">{{ row.spanCount }}</template>
        </el-table-column>
        <el-table-column label="服务" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">{{ row.services.join(', ') }}</template>
        </el-table-column>
        <el-table-column label="出错" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.errorCount" size="small" type="danger">{{ row.errorCount }}</el-tag>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="开始时间" width="150">
          <template #default="{ row }">{{ formatTime(row.startAt) }}</template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-drawer v-model="detail.visible" size="78%" :title="`链路 ${detail.traceId.slice(0, 24)}`">
      <div v-loading="detail.loading">
        <div v-if="detail.summary" class="meta">
          {{ detail.summary.rootService }} · {{ detail.summary.rootOperation }} ·
          {{ detail.summary.durationMs.toFixed(1) }} ms ·
          {{ detail.summary.spanCount }} span / {{ detail.summary.serviceCount }} 服务
          <span v-if="detail.summary.errorCount" style="color: var(--el-color-danger)">
            · {{ detail.summary.errorCount }} 个 span 出错
          </span>
        </div>
        <el-alert
          v-if="detail.truncated"
          type="warning"
          :closable="false"
          style="margin-bottom: 8px"
          :title="`这条链路的 span 太多，只画了前 ${detail.spans.length} 个`"
        />

        <div class="waterfall">
          <div
            v-for="span in detail.spans"
            :key="span.spanId"
            class="span-row"
            :class="{ active: detail.selected?.spanId === span.spanId }"
            @click="detail.selected = span"
          >
            <div class="span-name" :style="{ paddingLeft: span.depth * 14 + 'px' }">
              <span :class="{ err: span.error }">{{ span.service }}</span>
              <span class="op">{{ span.operation }}</span>
            </div>
            <div class="span-track">
              <div class="span-bar" :class="{ err: span.error }" :style="barStyle(span)">
                <span class="span-dur">{{ span.durationMs.toFixed(1) }}ms</span>
              </div>
            </div>
          </div>
        </div>

        <div v-if="detail.selected" class="span-detail">
          <div class="meta">
            {{ detail.selected.service }} · {{ detail.selected.operation }} ·
            起点 +{{ detail.selected.offsetMs.toFixed(1) }}ms ·
            耗时 {{ detail.selected.durationMs.toFixed(1) }}ms · spanId {{ detail.selected.spanId }}
          </div>
          <el-table
            :data="Object.entries(detail.selected.tags).map(([k, v]) => ({ k, v }))"
            border
            stripe
            size="small"
            max-height="240"
          >
            <el-table-column prop="k" label="标签" width="240" />
            <el-table-column prop="v" label="值" show-overflow-tooltip />
          </el-table>
        </div>
        <el-empty v-else-if="detail.spans.length" description="点一个 span 看它的标签" />
      </div>
    </el-drawer>

    <el-drawer v-model="manageVisible" size="60%" title="链路数据源">
      <div class="page-toolbar">
        <el-button v-perm="'trace:manage'" type="primary" @click="openSourceCreate">新增数据源</el-button>
      </div>
      <el-table :data="sources" border stripe size="small">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column prop="baseUrl" label="地址" min-width="200" show-overflow-tooltip />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="服务数" width="90">
          <template #default="{ row }">{{ row.serviceCount || '—' }}</template>
        </el-table-column>
        <el-table-column label="默认" width="70">
          <template #default="{ row }">
            <el-tag v-if="row.isDefault" size="small" type="success">默认</el-tag>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastError" label="最近错误" min-width="150" show-overflow-tooltip />
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'trace:manage'" link type="primary" @click="check(row)">检查</el-button>
            <el-button v-perm="'trace:manage'" link type="primary" @click="openSourceEdit(row)">编辑</el-button>
            <el-button v-perm="'trace:manage'" link type="danger" @click="removeSource(row)">删除</el-button>
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
          <el-input v-model="sourceDialog.form.name" placeholder="例如 prod-jaeger" />
        </el-form-item>
        <el-form-item label="地址">
          <el-input v-model="sourceDialog.form.baseUrl" placeholder="http://jaeger:16686" />
          <div class="hint">Jaeger Query 的根地址，不要带 /api/...</div>
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
.meta {
  margin: 8px 0;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.waterfall {
  max-height: calc(100vh - 420px);
  overflow: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
}
.span-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 8px;
  cursor: pointer;
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
}
.span-row:hover,
.span-row.active {
  background: var(--el-fill-color-light);
}
.span-name {
  flex: none;
  width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.span-name .op {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
}
.span-name .err {
  color: var(--el-color-danger);
  font-weight: 600;
}
.span-track {
  flex: 1;
  min-width: 200px;
}
.span-bar {
  height: 14px;
  border-radius: 2px;
  background: var(--el-color-primary-light-3);
  position: relative;
}
.span-bar.err {
  background: var(--el-color-danger-light-3);
}
.span-dur {
  position: absolute;
  left: 100%;
  margin-left: 4px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
}
.span-detail {
  margin-top: 12px;
}
</style>
