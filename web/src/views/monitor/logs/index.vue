<script setup lang="ts">
import { computed, onActivated, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  checkLogSource,
  createLogSource,
  deleteLogSource,
  listLogLabels,
  listLogSources,
  queryLogs,
  updateLogSource,
  type LogQueryResult,
  type LogSource
} from '@/api'

const sources = ref<LogSource[]>([])
const labelNames = ref<string[]>([])
const labelValues = ref<string[]>([])

const query = reactive({
  sourceId: 0,
  expr: '',
  rangeSec: 3600,
  limit: 200,
  direction: 'backward' as 'backward' | 'forward',
  loading: false
})
const picker = reactive({ label: '', value: '' })
const result = ref<LogQueryResult | null>(null)
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

function rangeParams() {
  const end = Math.floor(Date.now() / 1000)
  return { start: end - query.rangeSec, end }
}

async function loadSources() {
  sources.value = await listLogSources()
  if (!query.sourceId) {
    const preferred = sources.value.find((s) => s.isDefault && s.enabled) ||
      sources.value.find((s) => s.enabled)
    query.sourceId = preferred?.id || 0
  }
  if (query.sourceId) loadLabelNames()
}

async function loadLabelNames() {
  labelNames.value = []
  labelValues.value = []
  picker.label = ''
  picker.value = ''
  if (!query.sourceId) return
  try {
    const res = await listLogLabels({ sourceId: query.sourceId, ...rangeParams() })
    labelNames.value = res.values || []
  } catch {
    // 标签拉不到不影响手写 LogQL
  }
}

async function onLabelPick(name: string) {
  picker.label = name
  picker.value = ''
  labelValues.value = []
  if (!name) return
  try {
    const res = await listLogLabels({ sourceId: query.sourceId, label: name, ...rangeParams() })
    labelValues.value = res.values || []
  } catch (err: any) {
    ElMessage.error(err?.message || '读取标签值失败')
  }
}

// 选完标签值就把选择器拼成 {k="v"} 塞进表达式，省得手打引号
function onValuePick(value: string) {
  picker.value = value
  if (!picker.label || !value) return
  const pair = `${picker.label}="${value}"`
  const expr = query.expr.trim()
  if (!expr) {
    query.expr = `{${pair}}`
    return
  }
  const closing = expr.indexOf('}')
  if (expr.startsWith('{') && closing > 0) {
    // 已经有选择器就往里追加一个条件
    const inner = expr.slice(1, closing).trim()
    query.expr = `{${inner ? inner + ', ' : ''}${pair}}` + expr.slice(closing + 1)
  } else {
    query.expr = `{${pair}} ${expr}`
  }
}

async function run() {
  if (!query.sourceId) {
    ElMessage.warning('请先选择数据源')
    return
  }
  if (!query.expr.trim()) {
    ElMessage.warning('请填写 LogQL，例如 {app="web"} |= "error"')
    return
  }
  query.loading = true
  errorMsg.value = ''
  try {
    result.value = await queryLogs({
      sourceId: query.sourceId,
      query: query.expr,
      limit: query.limit,
      direction: query.direction,
      ...rangeParams()
    })
  } catch (err: any) {
    result.value = null
    errorMsg.value = err?.message || '查询失败'
  } finally {
    query.loading = false
  }
}

function formatTime(at: number) {
  const d = new Date(at)
  const pad = (n: number, w = 2) => String(n).padStart(w, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes()
  )}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}

function formatBytes(n: number) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = n
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value.toFixed(i ? 1 : 0)} ${units[i]}`
}

function download() {
  const rows = result.value?.rows || []
  if (!rows.length) return
  const text = rows.map((r) => `${new Date(r.at).toISOString()} ${r.stream} ${r.line}`).join('\n')
  const link = document.createElement('a')
  link.href = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
  link.download = 'logs.log'
  link.click()
  URL.revokeObjectURL(link.href)
}

// ---------- 数据源管理 ----------
const manageVisible = ref(false)
const sourceDialog = reactive({
  visible: false,
  editingId: null as number | null,
  form: {
    name: '',
    baseUrl: '',
    tenant: '',
    headerKey: '',
    headerValue: '',
    timeoutSec: 30,
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
    tenant: '',
    headerKey: '',
    headerValue: '',
    timeoutSec: 30,
    isDefault: sources.value.length === 0,
    enabled: true,
    remark: ''
  })
  sourceDialog.visible = true
}

function openSourceEdit(row: LogSource) {
  sourceDialog.editingId = row.id
  Object.assign(sourceDialog.form, {
    name: row.name,
    baseUrl: row.baseUrl,
    tenant: row.tenant,
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
      await updateLogSource(sourceDialog.editingId, { ...sourceDialog.form })
      ElMessage.success('已保存')
    } else {
      const res = await createLogSource({ ...sourceDialog.form })
      if (res.check?.status === 'healthy') ElMessage.success(`已接入：${res.check.detail}`)
      else ElMessage.warning(`已保存，但连不上：${res.check?.detail}`)
    }
    sourceDialog.visible = false
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function check(row: LogSource) {
  try {
    const res = await checkLogSource(row.id)
    if (res.status === 'healthy') ElMessage.success(res.detail)
    else ElMessage.error(res.detail)
    loadSources()
  } catch (err: any) {
    ElMessage.error(err?.message || '检查失败')
  }
}

async function removeSource(row: LogSource) {
  try {
    await ElMessageBox.confirm(`删除日志数据源「${row.name}」？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await deleteLogSource(row.id)
  ElMessage.success('已删除')
  if (query.sourceId === row.id) query.sourceId = 0
  loadSources()
}

// 告警详情等处的「在 Loki 中查看日志」带 ?q=LogQL 跳进来：等默认数据源就绪后直接执行。
// 页签工作台缓存本页，第二次跳进来不会重新 onMounted，所以 activate 时也认一次；
// appliedExpr 防止首屏挂载 + activate 查两遍。
const route = useRoute()
let appliedExpr = ''
function applyDeepLink() {
  const q = String(route.query.q ?? '')
  if (!q || q === appliedExpr) return
  appliedExpr = q
  query.expr = q
  run()
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
        title='查询已有的 Loki：写 LogQL（如 {app="web"} |= "error"），按时间范围取日志行。标签下拉可以直接拼出选择器。平台不存日志、不改写 LogQL，报错原样来自 Loki；聚合类表达式（count_over_time 等）返回的是指标，请去「指标查询」页'
      />

      <div class="page-toolbar">
        <el-select
          v-model="query.sourceId"
          placeholder="选择数据源"
          style="width: 200px"
          @change="loadLabelNames"
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
          <span v-if="currentSource.labelCount">· {{ currentSource.labelCount }} 个标签</span>
        </el-tag>
        <div class="grow"></div>
        <el-button v-perm="'log:manage'" @click="manageVisible = true">数据源管理</el-button>
      </div>

      <div class="query-row">
        <el-input
          v-model="query.expr"
          type="textarea"
          :rows="2"
          spellcheck="false"
          class="expr"
          placeholder='LogQL，例如 {app="web"} |= "error" != "healthcheck"'
        />
        <div class="query-side">
          <div class="picker">
            <el-select
              v-model="picker.label"
              filterable
              clearable
              placeholder="标签"
              style="width: 110px"
              @change="onLabelPick"
            >
              <el-option v-for="name in labelNames" :key="name" :label="name" :value="name" />
            </el-select>
            <el-select
              v-model="picker.value"
              filterable
              clearable
              :disabled="!picker.label"
              placeholder="取值"
              style="width: 120px"
              @change="onValuePick"
            >
              <el-option v-for="v in labelValues" :key="v" :label="v" :value="v" />
            </el-select>
          </div>
          <div class="picker">
            <el-select v-model="query.rangeSec" style="width: 120px">
              <el-option v-for="item in rangeOptions" :key="item.value" :label="item.label" :value="item.value" />
            </el-select>
            <el-select v-model="query.limit" style="width: 110px">
              <el-option :value="100" label="100 条" />
              <el-option :value="200" label="200 条" />
              <el-option :value="1000" label="1000 条" />
              <el-option :value="5000" label="5000 条" />
            </el-select>
          </div>
          <div class="picker">
            <el-radio-group v-model="query.direction" size="small">
              <el-radio-button value="backward">最新在前</el-radio-button>
              <el-radio-button value="forward">最旧在前</el-radio-button>
            </el-radio-group>
          </div>
          <div class="picker">
            <el-button type="primary" :loading="query.loading" @click="run">查询</el-button>
            <el-button :disabled="!result?.rows.length" @click="download">下载</el-button>
          </div>
        </div>
      </div>

      <el-alert v-if="errorMsg" type="error" :closable="false" :title="errorMsg" style="margin: 12px 0" />
      <el-alert
        v-if="result?.truncated"
        type="warning"
        :closable="false"
        style="margin: 12px 0"
        :title="`结果已达 ${result.limit} 条上限，可能还有更多日志：请缩小时间范围或加过滤条件`"
      />

      <template v-if="result">
        <div class="meta">
          {{ result.total }} 行 · {{ result.streams }} 个流 · {{ result.costMs }}ms
          <span v-if="result.linesProcessed">
            · Loki 扫了 {{ result.linesProcessed }} 行 / {{ formatBytes(result.bytesProcessed) }}
          </span>
          · {{ result.start.slice(5, 19).replace('T', ' ') }} ~ {{ result.end.slice(11, 19) }}
        </div>
        <div v-if="result.rows.length" class="log-box">
          <div v-for="(row, idx) in result.rows" :key="row.nano + idx" class="log-line">
            <span class="log-time">{{ formatTime(row.at) }}</span>
            <el-tooltip :content="row.stream" placement="top-start" :show-after="300">
              <span class="log-stream">{{ row.labels.app || row.labels.job || row.labels.filename || '—' }}</span>
            </el-tooltip>
            <span class="log-text">{{ row.line }}<span v-if="row.truncated" class="cut">（本行已截断）</span></span>
          </div>
        </div>
        <el-empty v-else description="这个时间范围内没有匹配的日志" />
      </template>
    </el-card>

    <el-drawer v-model="manageVisible" size="60%" title="日志数据源">
      <div class="page-toolbar">
        <el-button v-perm="'log:manage'" type="primary" @click="openSourceCreate">新增数据源</el-button>
      </div>
      <el-table :data="sources" border stripe size="small">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column prop="baseUrl" label="地址" min-width="180" show-overflow-tooltip />
        <el-table-column prop="tenant" label="租户" width="100">
          <template #default="{ row }">{{ row.tenant || '—' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="标签数" width="90">
          <template #default="{ row }">{{ row.labelCount || '—' }}</template>
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
            <el-button v-perm="'log:manage'" link type="primary" @click="check(row)">检查</el-button>
            <el-button v-perm="'log:manage'" link type="primary" @click="openSourceEdit(row)">编辑</el-button>
            <el-button v-perm="'log:manage'" link type="danger" @click="removeSource(row)">删除</el-button>
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
          <el-input v-model="sourceDialog.form.name" placeholder="例如 prod-loki" />
        </el-form-item>
        <el-form-item label="地址">
          <el-input v-model="sourceDialog.form.baseUrl" placeholder="http://loki:3100" />
          <div class="hint">只填根地址，不要带 /loki/api/...</div>
        </el-form-item>
        <el-form-item label="租户">
          <el-input v-model="sourceDialog.form.tenant" placeholder="多租户 Loki 的 X-Scope-OrgID，单机留空" />
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
          <span class="hint" style="margin-left: 8px">秒，日志查询比指标慢，别给太短</span>
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
  gap: 6px;
  width: 250px;
}
.picker {
  display: flex;
  gap: 6px;
}
.meta {
  margin: 8px 0;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.log-box {
  max-height: calc(100vh - 340px);
  overflow: auto;
  padding: 8px 10px;
  background: #1e1e1e;
  border-radius: 4px;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.7;
}
.log-line {
  display: flex;
  gap: 8px;
  color: #d4d4d4;
  white-space: pre-wrap;
  word-break: break-all;
}
.log-line:hover {
  background: #2a2a2a;
}
.log-time {
  flex: none;
  color: #6a9955;
}
.log-stream {
  flex: none;
  width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #569cd6;
  cursor: help;
}
.log-text {
  flex: 1;
}
.cut {
  color: #d7ba7d;
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
