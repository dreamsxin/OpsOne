<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createAlertSilence,
  deleteAlertSilence,
  endAlertSilence,
  listAlertSilenceHits,
  listAlertSilences,
  previewAlertSilence,
  updateAlertSilence,
  type AlertSilence,
  type AlertSilencePreview
} from '@/api'

const loading = ref(false)
const rows = ref<AlertSilence[]>([])
const summary = ref<Record<string, number>>({})
const filters = reactive({ status: '', kind: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const submitting = ref(false)
const form = reactive({
  name: '',
  kind: 'maintenance',
  severityList: [] as string[],
  matchSource: '',
  matchTitle: '',
  matchLabels: '',
  matchAll: false,
  range: [] as string[],
  reason: '',
  enabled: true
})
const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }]
}

const preview = ref<AlertSilencePreview | null>(null)
const previewLoading = ref(false)

const hitsVisible = ref(false)
const hitsRows = ref<any[]>([])
const hitsSilence = ref<AlertSilence | null>(null)

const severityText: Record<string, string> = {
  critical: '严重',
  warning: '警告',
  info: '提示'
}
const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'warning' | 'danger' }> = {
  active: { text: '生效中', type: 'success' },
  pending: { text: '未开始', type: 'info' },
  expired: { text: '已过期', type: 'info' },
  ended: { text: '已提前结束', type: 'warning' },
  disabled: { text: '已停用', type: 'danger' }
}

const activeCount = computed(() => summary.value.active || 0)

function kindText(row: AlertSilence) {
  return row.kind === 'maintenance' ? '维护窗口' : '临时静默'
}

function conditionText(row: AlertSilence) {
  if (row.matchAll) return '全部告警'
  const parts: string[] = []
  if (row.matchSeverity) {
    parts.push(
      '级别 ' +
        row.matchSeverity
          .split(',')
          .map((s) => severityText[s] || s)
          .join('/')
    )
  }
  if (row.matchSource) parts.push('来源 ' + row.matchSource)
  if (row.matchTitle) parts.push('标题含「' + row.matchTitle + '」')
  if (row.matchLabels && row.matchLabels !== '{}') parts.push('标签 ' + row.matchLabels)
  return parts.join(' + ') || '—'
}

function remainText(row: AlertSilence) {
  if (row.status !== 'active') return '—'
  const mins = Math.max(0, Math.round(row.remainSeconds / 60))
  if (mins >= 60) return `剩余 ${Math.floor(mins / 60)} 小时 ${mins % 60} 分`
  return `剩余 ${mins} 分`
}

async function load() {
  loading.value = true
  try {
    const params: Record<string, any> = {}
    if (filters.status) params.status = filters.status
    if (filters.kind) params.kind = filters.kind
    const data = await listAlertSilences(params)
    rows.value = data.list || []
    summary.value = data.summary || {}
  } finally {
    loading.value = false
  }
}

function resetForm() {
  form.name = ''
  form.kind = 'maintenance'
  form.severityList = []
  form.matchSource = ''
  form.matchTitle = ''
  form.matchLabels = ''
  form.matchAll = false
  form.reason = ''
  form.enabled = true
  preview.value = null
  // 默认给一个「从现在起两小时」的窗口，最常见的场景是临时发布
  const now = new Date()
  const later = new Date(now.getTime() + 2 * 3600 * 1000)
  form.range = [fmt(now), fmt(later)]
}

function fmt(d: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(
    d.getMinutes()
  )}:${pad(d.getSeconds())}`
}

function openCreate() {
  editingId.value = null
  resetForm()
  dialogVisible.value = true
}

function openEdit(row: AlertSilence) {
  editingId.value = row.id
  form.name = row.name
  form.kind = row.kind
  form.severityList = row.matchSeverity ? row.matchSeverity.split(',') : []
  form.matchSource = row.matchSource
  form.matchTitle = row.matchTitle
  form.matchLabels = row.matchLabels
  form.matchAll = row.matchAll
  form.reason = row.reason
  form.enabled = row.enabled
  form.range = [row.startAt.slice(0, 19).replace('T', ' '), row.endAt.slice(0, 19).replace('T', ' ')]
  preview.value = null
  dialogVisible.value = true
}

function payload() {
  return {
    name: form.name,
    kind: form.kind,
    matchSeverity: form.severityList.join(','),
    matchSource: form.matchSource,
    matchTitle: form.matchTitle,
    matchLabels: form.matchLabels,
    matchAll: form.matchAll,
    startAt: form.range?.[0] || '',
    endAt: form.range?.[1] || '',
    reason: form.reason,
    enabled: form.enabled
  }
}

async function runPreview() {
  previewLoading.value = true
  try {
    preview.value = await previewAlertSilence(payload())
  } finally {
    previewLoading.value = false
  }
}

async function submit() {
  await formRef.value?.validate()
  submitting.value = true
  try {
    if (editingId.value) {
      await updateAlertSilence(editingId.value, payload())
      ElMessage.success('已保存')
    } else {
      await createAlertSilence(payload())
      ElMessage.success('已创建')
    }
    dialogVisible.value = false
    load()
  } finally {
    submitting.value = false
  }
}

async function endNow(row: AlertSilence) {
  await ElMessageBox.confirm(
    `提前结束「${row.name}」？结束后新产生的告警会立刻恢复外发通知。`,
    '提前结束',
    { type: 'warning' }
  )
  await endAlertSilence(row.id)
  ElMessage.success('已结束')
  load()
}

async function remove(row: AlertSilence) {
  await ElMessageBox.confirm(`确认删除「${row.name}」？`, '提示', { type: 'warning' })
  await deleteAlertSilence(row.id)
  ElMessage.success('已删除')
  load()
}

async function openHits(row: AlertSilence) {
  const data = await listAlertSilenceHits(row.id)
  hitsSilence.value = data.silence
  hitsRows.value = data.list || []
  hitsVisible.value = true
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="静默只拦「外发通知」，不拦入库：窗口期内的告警照常出现在告警列表里，并记下是哪条静默拦的，事后能回答「为什么没收到通知」。窗口结束后新产生的告警自动恢复外发，不需要手工解除；值班升级也按当下时间重新判定，窗口里不叫人。"
      />

      <div class="page-toolbar">
        <el-select v-model="filters.status" placeholder="全部状态" clearable style="width: 140px" @change="load">
          <el-option label="生效中" value="active" />
          <el-option label="未开始" value="pending" />
          <el-option label="已过期" value="expired" />
          <el-option label="已提前结束" value="ended" />
          <el-option label="已停用" value="disabled" />
        </el-select>
        <el-select v-model="filters.kind" placeholder="全部类型" clearable style="width: 140px" @change="load">
          <el-option label="维护窗口" value="maintenance" />
          <el-option label="临时静默" value="silence" />
        </el-select>
        <el-button @click="load">刷新</el-button>
        <el-tag v-if="activeCount > 0" type="success">当前有 {{ activeCount }} 条生效中</el-tag>
        <div class="grow"></div>
        <el-button v-perm="'silence:manage'" type="primary" @click="openCreate">新建窗口</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有静默或维护窗口">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.kind === 'maintenance' ? 'warning' : 'info'">
              {{ kindText(row) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="匹配条件" min-width="240">
          <template #default="{ row }">
            <el-tag v-if="row.matchAll" size="small" type="danger">全部告警</el-tag>
            <span v-else>{{ conditionText(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="窗口" min-width="200">
          <template #default="{ row }">
            {{ row.startAt?.slice(0, 19).replace('T', ' ') }}
            →
            {{ row.endAt?.slice(0, 19).replace('T', ' ') }}
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="剩余" width="150">
          <template #default="{ row }">{{ remainText(row) }}</template>
        </el-table-column>
        <el-table-column label="已拦下" width="100">
          <template #default="{ row }">
            <el-link v-if="row.hitCount > 0" type="primary" @click="openHits(row)">
              {{ row.hitCount }} 条
            </el-link>
            <span v-else>0</span>
          </template>
        </el-table-column>
        <el-table-column prop="creatorName" label="创建人" width="110" />
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'silence:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button
              v-if="row.status === 'active' || row.status === 'pending'"
              v-perm="'silence:manage'"
              link
              type="warning"
              @click="endNow(row)"
            >
              提前结束
            </el-button>
            <el-button
              v-if="row.hitCount === 0"
              v-perm="'silence:manage'"
              link
              type="danger"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑窗口' : '新建静默 / 维护窗口'"
      width="680px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="例如：订单服务发布窗口" maxlength="64" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.kind">
            <el-radio label="maintenance">维护窗口</el-radio>
            <el-radio label="silence">临时静默</el-radio>
          </el-radio-group>
          <div class="hint">只影响展示与筛选，判定逻辑完全一样</div>
        </el-form-item>
        <el-form-item label="时间窗">
          <el-date-picker
            v-model="form.range"
            type="datetimerange"
            value-format="YYYY-MM-DD HH:mm:ss"
            start-placeholder="开始"
            end-placeholder="结束"
            style="width: 100%"
          />
          <div class="hint">右开区间：结束时间到点即失效，单条最长 30 天</div>
        </el-form-item>

        <el-divider content-position="left">匹配条件（填了的都要命中）</el-divider>
        <el-form-item label="级别">
          <el-select v-model="form.severityList" multiple placeholder="不限" style="width: 100%">
            <el-option label="严重" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="提示" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="来源">
          <el-input v-model="form.matchSource" placeholder="告警来源名，多个用逗号分隔；留空不限" />
        </el-form-item>
        <el-form-item label="标题关键字">
          <el-input v-model="form.matchTitle" placeholder="标题里包含这段文字才拦；留空不限" />
        </el-form-item>
        <el-form-item label="标签条件">
          <el-input
            v-model="form.matchLabels"
            type="textarea"
            :rows="2"
            placeholder='{"service":"web-api","env":"prod"} —— 每一项都要与告警标签相等'
          />
        </el-form-item>
        <el-form-item label="匹配全部告警">
          <el-switch v-model="form.matchAll" />
          <div class="hint">危险：打开后窗口期内所有告警都不外发。不填条件时必须显式打开这个开关</div>
        </el-form-item>
        <el-form-item label="原因">
          <el-input v-model="form.reason" placeholder="写清为什么静默，事后好回溯" maxlength="255" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>

        <el-form-item label="试算">
          <el-button :loading="previewLoading" @click="runPreview">按当前条件试算</el-button>
          <div v-if="preview" class="preview">
            <div>
              扫描最近 {{ preview.scanned }} 条未恢复告警，其中
              <b>{{ preview.matched }}</b> 条会被这套条件拦下
            </div>
            <el-table v-if="preview.samples.length" :data="preview.samples" size="small" border>
              <el-table-column prop="id" label="ID" width="70" />
              <el-table-column prop="severity" label="级别" width="80" />
              <el-table-column prop="title" label="标题" min-width="200" />
              <el-table-column prop="sourceName" label="来源" width="140" />
            </el-table>
            <div v-else class="hint">当前没有告警会被拦下（试算只看匹配条件，不看时间窗）</div>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="hitsVisible" title="拦下过的告警" width="760px">
      <el-alert
        v-if="hitsSilence"
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        :title="`「${hitsSilence.name}」共拦下 ${hitsSilence.hitCount} 条告警的通知，这些告警都在告警列表里，只是没有外发`"
      />
      <el-table :data="hitsRows" border stripe size="small" empty-text="还没有拦下过告警">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="severity" label="级别" width="80" />
        <el-table-column prop="title" label="标题" min-width="220" />
        <el-table-column prop="sourceName" label="来源" width="150" />
        <el-table-column label="最近出现" width="180">
          <template #default="{ row }">{{ row.lastSeenAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}
.grow {
  flex: 1;
}
.hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.preview {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
