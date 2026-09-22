<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  archiveEventReview,
  createActionItem,
  deleteActionItem,
  exportEventReview,
  finishActionItem,
  getEventEvidence,
  getEventReview,
  getReviewStats,
  listActionItems,
  listReviews,
  listUsers,
  reopenEventReview,
  saveEventReview,
  updateActionItem,
  type ActionItem,
  type EventEvidence,
  type EventReview,
  type EventReviewDetail,
  type ReviewStats,
  type User
} from '@/api'
import { useUserStore } from '@/stores/user'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

const route = useRoute()
const store = useUserStore()

const tab = ref<'reviews' | 'items'>('reviews')
const users = ref<User[]>([])
const stats = ref<ReviewStats | null>(null)

// ---------- 复盘看板 ----------
const loading = ref(false)
const rows = ref<EventReview[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, status: '', owner: '', keyword: '' })

// ---------- 改进项跟踪 ----------
const itemLoading = ref(false)
const items = ref<ActionItem[]>([])
const itemTotal = ref(0)
const itemQuery = reactive({ page: 1, pageSize: 20, status: '', owner: '', kind: '', overdue: '' })

const statusMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'success' | 'info' }> = {
  pending: { text: '待复盘', type: 'danger' },
  draft: { text: '草稿', type: 'warning' },
  reviewing: { text: '评审中', type: 'warning' },
  archived: { text: '已归档', type: 'success' }
}

const severityMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '警告', type: 'warning' },
  info: { text: '提示', type: 'info' }
}

const itemStatusMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'success' | 'info' }> = {
  open: { text: '待开始', type: 'danger' },
  doing: { text: '进行中', type: 'warning' },
  done: { text: '已完成', type: 'success' },
  dropped: { text: '不做了', type: 'info' }
}

const kindOptions = [
  { value: 'prevent', label: '防复发' },
  { value: 'detect', label: '提升发现能力' },
  { value: 'mitigate', label: '加快止血' },
  { value: 'process', label: '流程改进' }
]

function minutesText(v: number | null | undefined) {
  if (v === null || v === undefined) return '-'
  if (v < 60) return `${v} 分钟`
  const hours = Math.floor(v / 60)
  const rest = v % 60
  if (hours < 24) return rest ? `${hours} 小时 ${rest} 分` : `${hours} 小时`
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时`
}

async function loadStats() {
  stats.value = await getReviewStats()
}

async function load() {
  loading.value = true
  try {
    const data = await listReviews(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadItems() {
  itemLoading.value = true
  try {
    const data = await listActionItems(itemQuery)
    items.value = data.list || []
    itemTotal.value = data.total
  } finally {
    itemLoading.value = false
  }
}

const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `当前 ${total.value} 条 · 本页 ${rows.value.length}`, static: true },
    { key: 'status:pending', label: '待复盘', count: s?.pending ?? 0, hint: '已解决但还没建复盘', tone: 'danger' },
    { key: 'status:draft', label: '草稿', count: s?.draft ?? 0, hint: '建了还没写完', tone: 'warning' },
    { key: 'status:reviewing', label: '评审中', count: s?.reviewing ?? 0, hint: '内容齐了待归档', tone: 'warning' },
    { key: 'status:archived', label: '已归档', count: s?.archived ?? 0, hint: '可检索回溯', tone: 'success' },
    { key: 'mine', label: '我负责的复盘', count: 0, hint: '按复盘负责人筛' },
    {
      key: 'overdue',
      label: `改进项逾期 ${s?.overdueItems ?? 0}`,
      hint: `未完成 ${s?.openItems ?? 0} 条`,
      static: true,
      tone: (s?.overdueItems ?? 0) > 0 ? 'danger' : undefined
    },
    {
      key: 'mttr',
      label: s?.avgRecoverMinutes === null || s?.avgRecoverMinutes === undefined
        ? '平均恢复时长 数据不足'
        : `平均恢复时长 ${minutesText(s.avgRecoverMinutes)}`,
      hint: `已归档样本 ${s?.recoverSamples ?? 0} 个`,
      static: true
    }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.status) return `status:${query.status}`
  if (query.owner && query.owner === store.profile?.username) return 'mine'
  return null
})

function onChipSelect(key: string) {
  if (key === 'info' || key === 'overdue' || key === 'mttr') return
  const wasActive = activeChipKey.value === key
  query.status = ''
  query.owner = ''
  if (!wasActive && key === 'mine') query.owner = store.profile?.username || ''
  else if (!wasActive && key.startsWith('status:')) query.status = key.split(':')[1]
  query.page = 1
  load()
}

// ---------- 复盘抽屉 ----------
const drawerVisible = ref(false)
const detail = ref<EventReviewDetail | null>(null)
const evidence = ref<EventEvidence | null>(null)
const evidenceLoading = ref(false)
const activePane = ref('review')

const form = reactive({
  owner: '',
  happenedAt: '' as string | null,
  detectedAt: '' as string | null,
  respondedAt: '' as string | null,
  mitigatedAt: '' as string | null,
  recoveredAt: '' as string | null,
  impact: '',
  rootCause: '',
  trigger: '',
  detectGap: '',
  mitigation: '',
  lesson: '',
  status: 'draft'
})

const archived = computed(() => detail.value?.review?.status === 'archived')

/** 表单里的里程碑一改，耗时就跟着变；不用先保存再看结果 */
const formDurations = computed(() => {
  const at = (v: string | null) => (v ? new Date(v).getTime() : NaN)
  const diff = (from: string | null, to: string | null) => {
    const a = at(from)
    const b = at(to)
    if (Number.isNaN(a) || Number.isNaN(b) || b < a) return null
    return Math.round((b - a) / 60000)
  }
  return {
    detectMinutes: diff(form.happenedAt, form.detectedAt),
    ackMinutes: diff(form.detectedAt, form.respondedAt),
    mitigateMinutes: diff(form.respondedAt, form.mitigatedAt),
    recoverMinutes: diff(form.happenedAt, form.recoveredAt)
  }
})

const milestoneFields = [
  { key: 'happenedAt', label: '故障开始' },
  { key: 'detectedAt', label: '被发现' },
  { key: 'respondedAt', label: '开始响应' },
  { key: 'mitigatedAt', label: '止血完成' },
  { key: 'recoveredAt', label: '完全恢复' }
] as const

async function openDrawer(eventId: number) {
  detail.value = await getEventReview(eventId)
  evidence.value = null
  activePane.value = 'review'
  const review = detail.value.review
  const suggestion = detail.value.suggestions
  Object.assign(form, {
    owner: review?.owner || detail.value.event.assignee || '',
    happenedAt: review?.happenedAt ?? suggestion.happenedAt?.at ?? '',
    detectedAt: review?.detectedAt ?? suggestion.detectedAt?.at ?? '',
    respondedAt: review?.respondedAt ?? suggestion.respondedAt?.at ?? '',
    mitigatedAt: review?.mitigatedAt ?? '',
    recoveredAt: review?.recoveredAt ?? suggestion.recoveredAt?.at ?? '',
    impact: review?.impact || '',
    rootCause: review?.rootCause || '',
    trigger: review?.trigger || '',
    detectGap: review?.detectGap || '',
    mitigation: review?.mitigation || '',
    lesson: review?.lesson || '',
    status: review?.status === 'archived' ? 'reviewing' : review?.status || 'draft'
  })
  drawerVisible.value = true
}

async function refreshDrawer() {
  if (detail.value) await openDrawer(detail.value.event.id)
  load()
  loadStats()
}

async function submitReview(nextStatus?: string) {
  if (!detail.value) return
  const payload = { ...form, status: nextStatus || form.status }
  await saveEventReview(detail.value.event.id, payload)
  ElMessage.success('已保存')
  await refreshDrawer()
}

async function archive() {
  if (!detail.value) return
  await submitReview('reviewing')
  const res = await archiveEventReview(detail.value.event.id)
  ElMessage.success(`已归档，${res.actionItems} 条改进项进入跟踪`)
  await refreshDrawer()
}

async function reopen() {
  if (!detail.value) return
  const input = await ElMessageBox.prompt('重新打开的原因（会记入事件时间线）', '重新打开复盘', {
    inputPlaceholder: '例如：根因判断有误，需要修正'
  }).catch(() => null)
  if (!input) return
  await reopenEventReview(detail.value.event.id, input.value || '')
  ElMessage.success('已重新打开')
  await refreshDrawer()
}

async function exportMarkdown() {
  if (!detail.value) return
  const res = await exportEventReview(detail.value.event.id)
  const blob = new Blob([res.markdown], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = res.filename
  a.click()
  URL.revokeObjectURL(url)
}

async function loadEvidence() {
  if (!detail.value || evidence.value) return
  evidenceLoading.value = true
  try {
    evidence.value = await getEventEvidence(detail.value.event.id)
  } finally {
    evidenceLoading.value = false
  }
}

function onPaneChange(name: string | number) {
  if (name === 'evidence') loadEvidence()
}

// ---------- 改进项编辑 ----------
const itemDialog = ref(false)
const editingItem = ref<ActionItem | null>(null)
const itemForm = reactive({
  title: '',
  detail: '',
  kind: 'prevent',
  owner: '',
  dueDate: '' as string | null,
  status: 'open'
})

function openItemDialog(item?: ActionItem) {
  editingItem.value = item || null
  Object.assign(itemForm, {
    title: item?.title || '',
    detail: item?.detail || '',
    kind: item?.kind || 'prevent',
    owner: item?.owner || form.owner || store.profile?.username || '',
    dueDate: item?.dueDate || '',
    status: item?.status || 'open'
  })
  itemDialog.value = true
}

async function submitItem() {
  if (!itemForm.title.trim()) {
    ElMessage.warning('请填写改进项标题')
    return
  }
  if (!itemForm.owner) {
    ElMessage.warning('请指定负责人')
    return
  }
  const payload = { ...itemForm, dueDate: itemForm.dueDate || null }
  if (editingItem.value) {
    await updateActionItem(editingItem.value.id, payload)
  } else {
    if (!detail.value) return
    await createActionItem(detail.value.event.id, payload)
  }
  ElMessage.success('已保存')
  itemDialog.value = false
  if (drawerVisible.value) await refreshDrawer()
  if (tab.value === 'items') loadItems()
  loadStats()
}

async function finishItem(item: ActionItem) {
  const input = await ElMessageBox.prompt('完成说明（会记入事件时间线）', `完成「${item.title}」`, {
    inputPlaceholder: '例如：已上线监控规则 #12'
  }).catch(() => null)
  if (!input) return
  await finishActionItem(item.id, input.value || '')
  ElMessage.success('已标记完成')
  if (drawerVisible.value) await refreshDrawer()
  if (tab.value === 'items') loadItems()
  loadStats()
}

async function removeItem(item: ActionItem) {
  await ElMessageBox.confirm(`确认删除改进项「${item.title}」？`, '提示', { type: 'warning' })
  await deleteActionItem(item.id)
  ElMessage.success('已删除')
  if (drawerVisible.value) await refreshDrawer()
  if (tab.value === 'items') loadItems()
  loadStats()
}

function switchTab(name: string | number) {
  if (name === 'items') loadItems()
}

onMounted(async () => {
  const data = await listUsers({ page: 1, pageSize: 100 })
  users.value = data.list || []
  await Promise.all([load(), loadStats()])
  // 事件页「去复盘」跳过来时直接开抽屉
  const eventId = Number(route.query.event)
  if (eventId) openDrawer(eventId)
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="事件复盘"
      subtitle="复盘不是补作业：里程碑时间由平台给建议值、证据自动汇聚，改进项逾期会催办"
    />

    <el-tabs v-model="tab" @tab-change="switchTab">
      <el-tab-pane label="复盘看板" name="reviews">
        <el-card>
          <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

          <div class="page-toolbar" style="margin-top: 12px">
            <el-input
              v-model="query.keyword"
              placeholder="事件标题 / 根因"
              style="width: 220px"
              clearable
              @keyup.enter="((query.page = 1), load())"
            />
            <el-select v-model="query.status" placeholder="复盘状态" clearable style="width: 130px">
              <el-option label="待复盘" value="pending" />
              <el-option label="草稿" value="draft" />
              <el-option label="评审中" value="reviewing" />
              <el-option label="已归档" value="archived" />
            </el-select>
            <el-select v-model="query.owner" placeholder="复盘负责人" clearable filterable style="width: 150px">
              <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
            </el-select>
            <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
            <el-button @click="((query.status = ''), (query.owner = ''), (query.keyword = ''), (query.page = 1), load())">
              重置
            </el-button>
          </div>

          <el-alert
            v-if="query.status === 'pending'"
            type="warning"
            :closable="false"
            style="margin-bottom: 12px"
            title="这些事件已经解决/关闭但还没有复盘记录。点「开始复盘」会带着平台算出的里程碑建议值建草稿。"
          />

          <el-table v-loading="loading" :data="rows" border stripe empty-text="没有符合条件的复盘">
            <el-table-column label="事件" min-width="220" show-overflow-tooltip>
              <template #default="{ row }">
                <span style="color: #6b7280">#{{ row.eventId }}</span>
                {{ row.eventTitle }}
              </template>
            </el-table-column>
            <el-table-column label="级别" width="85">
              <template #default="{ row }">
                <el-tag v-if="row.eventSeverity" size="small" :type="severityMeta[row.eventSeverity]?.type || 'info'">
                  {{ severityMeta[row.eventSeverity]?.text || row.eventSeverity }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="复盘状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
                  {{ row.statusLabel }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="负责人" width="110">
              <template #default="{ row }">
                <span v-if="row.owner">{{ row.owner }}</span>
                <el-tag v-else size="small" type="warning">未指定</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="恢复时长" width="130">
              <template #default="{ row }">
                {{ minutesText(row.durations?.recoverMinutes) }}
              </template>
            </el-table-column>
            <el-table-column label="改进项" width="140">
              <template #default="{ row }">
                <template v-if="row.totalItems">
                  {{ row.totalItems }} 条 · 未完成 {{ row.openItems }}
                  <el-tag v-if="row.overdueItems" size="small" type="danger" style="margin-left: 4px">
                    逾期 {{ row.overdueItems }}
                  </el-tag>
                </template>
                <span v-else style="color: #9ca3af">无</span>
              </template>
            </el-table-column>
            <el-table-column label="根因" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">
                <span v-if="row.rootCause">{{ row.rootCause }}</span>
                <span v-else style="color: #9ca3af">还没写</span>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="110" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDrawer(row.eventId)">
                  {{ row.status === 'pending' ? '开始复盘' : '打开' }}
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <Pagination
            v-model:current-page="query.page"
            v-model:page-size="query.pageSize"
            :total="total"
            @change="load"
          />
        </el-card>
      </el-tab-pane>

      <el-tab-pane :label="`改进项跟踪${stats?.overdueItems ? ' (' + stats.overdueItems + ' 逾期)' : ''}`" name="items">
        <el-card>
          <div class="page-toolbar">
            <el-select v-model="itemQuery.status" placeholder="状态" clearable style="width: 120px">
              <el-option v-for="(meta, key) in itemStatusMeta" :key="key" :label="meta.text" :value="key" />
            </el-select>
            <el-select v-model="itemQuery.kind" placeholder="类型" clearable style="width: 150px">
              <el-option v-for="k in kindOptions" :key="k.value" :label="k.label" :value="k.value" />
            </el-select>
            <el-select v-model="itemQuery.owner" placeholder="负责人" clearable filterable style="width: 150px">
              <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
            </el-select>
            <el-select v-model="itemQuery.overdue" placeholder="是否逾期" clearable style="width: 120px">
              <el-option label="只看逾期" value="1" />
            </el-select>
            <el-button type="primary" @click="((itemQuery.page = 1), loadItems())">查询</el-button>
            <el-button
              @click="((itemQuery.status = ''), (itemQuery.kind = ''), (itemQuery.owner = ''), (itemQuery.overdue = ''), (itemQuery.page = 1), loadItems())"
            >
              重置
            </el-button>
            <el-button @click="((itemQuery.owner = store.profile?.username || ''), (itemQuery.page = 1), loadItems())">
              只看我的
            </el-button>
          </div>

          <el-table v-loading="itemLoading" :data="items" border stripe empty-text="还没有改进项">
            <el-table-column prop="title" label="改进项" min-width="220" show-overflow-tooltip />
            <el-table-column label="来自事件" min-width="180" show-overflow-tooltip>
              <template #default="{ row }">
                <span style="color: #6b7280">#{{ row.eventId }}</span> {{ row.eventTitle }}
              </template>
            </el-table-column>
            <el-table-column prop="kindLabel" label="类型" width="130" />
            <el-table-column prop="owner" label="负责人" width="110" />
            <el-table-column label="截止" width="120">
              <template #default="{ row }">
                <span v-if="!row.dueDate" style="color: #9ca3af">未设</span>
                <span v-else :style="row.overdue ? 'color:#dc2626;font-weight:500' : ''">
                  {{ String(row.dueDate).slice(0, 10) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="itemStatusMeta[row.status]?.type || 'info'">
                  {{ row.statusLabel }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="180" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDrawer(row.eventId)">看复盘</el-button>
                <el-button
                  v-if="row.status !== 'done' && row.status !== 'dropped'"
                  v-perm="'review:manage'"
                  link
                  type="success"
                  @click="finishItem(row)"
                >
                  完成
                </el-button>
                <el-button v-perm="'review:manage'" link @click="openItemDialog(row)">编辑</el-button>
              </template>
            </el-table-column>
          </el-table>

          <Pagination
            v-model:current-page="itemQuery.page"
            v-model:page-size="itemQuery.pageSize"
            :total="itemTotal"
            @change="loadItems"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <el-drawer
      v-model="drawerVisible"
      :title="`复盘 · 事件 #${detail?.event.id ?? ''} ${detail?.event.title ?? ''}`"
      size="70%"
    >
      <template v-if="detail">
        <div style="display: flex; gap: 8px; align-items: center; margin-bottom: 12px; flex-wrap: wrap">
          <el-tag :type="statusMeta[detail.review?.status || 'pending']?.type || 'info'">
            {{ detail.review?.statusLabel || '待复盘' }}
          </el-tag>
          <span style="color: #6b7280">事件状态：{{ detail.event.statusLabel }}</span>
          <div style="flex: 1"></div>
          <el-button v-if="detail.review" @click="exportMarkdown">导出 Markdown</el-button>
          <template v-if="!archived">
            <el-button v-perm="'review:manage'" @click="submitReview('draft')">存草稿</el-button>
            <el-button v-perm="'review:manage'" type="primary" @click="submitReview('reviewing')">保存并转评审</el-button>
            <el-button v-perm="'review:manage'" type="success" @click="archive">归档</el-button>
          </template>
          <el-button v-else v-perm="'review:manage'" type="warning" @click="reopen">重新打开</el-button>
        </div>

        <el-alert
          v-if="archived"
          type="success"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`已由 ${detail.review?.archivedBy} 于 ${detail.review?.archivedAt} 归档，字段已锁定；要修改请先「重新打开」`"
        />

        <el-tabs v-model="activePane" @tab-change="onPaneChange">
          <el-tab-pane label="复盘内容" name="review">
            <el-form label-width="96px" :disabled="archived">
              <el-form-item label="复盘负责人">
                <el-select v-model="form.owner" filterable clearable style="width: 200px">
                  <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
                </el-select>
              </el-form-item>

              <el-divider content-position="left">时间里程碑</el-divider>
              <el-alert
                type="info"
                :closable="false"
                style="margin-bottom: 12px"
                title="下面每一项都给了平台算出的建议值和它的出处。平台只知道告警什么时候响的，不知道故障什么时候真正开始，所以「故障开始」通常要往前改。"
              />
              <el-form-item v-for="field in milestoneFields" :key="field.key" :label="field.label">
                <el-date-picker
                  v-model="form[field.key]"
                  type="datetime"
                  value-format="YYYY-MM-DDTHH:mm:ssZ"
                  placeholder="选择时间"
                  style="width: 220px"
                />
                <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
                  {{ detail.suggestions[field.key]?.source || '无建议值' }}
                </span>
              </el-form-item>

              <el-descriptions :column="4" border size="small" style="margin: 8px 0 16px">
                <el-descriptions-item label="发现耗时">
                  {{ minutesText(formDurations.detectMinutes) }}
                </el-descriptions-item>
                <el-descriptions-item label="响应耗时">
                  {{ minutesText(formDurations.ackMinutes) }}
                </el-descriptions-item>
                <el-descriptions-item label="止血耗时">
                  {{ minutesText(formDurations.mitigateMinutes) }}
                </el-descriptions-item>
                <el-descriptions-item label="总恢复时长">
                  {{ minutesText(formDurations.recoverMinutes) }}
                </el-descriptions-item>
              </el-descriptions>

              <el-divider content-position="left">内容</el-divider>
              <el-form-item label="影响面" required>
                <el-input v-model="form.impact" type="textarea" :rows="2" placeholder="谁受影响、影响多久、有没有数据损失" />
              </el-form-item>
              <el-form-item label="根因" required>
                <el-input v-model="form.rootCause" type="textarea" :rows="3" placeholder="为什么会发生，追到能被修掉的那一层" />
              </el-form-item>
              <el-form-item label="诱因">
                <el-input v-model="form.trigger" type="textarea" :rows="2" placeholder="什么变更/事件点了火：发布、配置改动、流量突增…" />
              </el-form-item>
              <el-form-item label="发现环节">
                <el-input v-model="form.detectGap" type="textarea" :rows="2" placeholder="为什么没更早发现：没有监控？阈值不对？通知没到人？" />
              </el-form-item>
              <el-form-item label="止血过程">
                <el-input v-model="form.mitigation" type="textarea" :rows="2" placeholder="做了哪些动作让业务先恢复" />
              </el-form-item>
              <el-form-item label="经验教训">
                <el-input v-model="form.lesson" type="textarea" :rows="2" />
              </el-form-item>
            </el-form>
          </el-tab-pane>

          <el-tab-pane :label="`改进项 (${detail.actionItems.length})`" name="items">
            <el-alert
              type="info"
              :closable="false"
              style="margin-bottom: 12px"
              title="归档要求至少一条改进项。设了截止日期的，逾期后会按天给负责人发站内消息催办（受 OPS_REVIEW_SPEC 控制）。"
            />
            <el-button
              v-if="!archived"
              v-perm="'review:manage'"
              type="primary"
              style="margin-bottom: 12px"
              @click="openItemDialog()"
            >
              新增改进项
            </el-button>
            <el-table :data="detail.actionItems" border size="small" empty-text="还没有改进项">
              <el-table-column prop="title" label="改进项" min-width="200" show-overflow-tooltip />
              <el-table-column prop="kindLabel" label="类型" width="130" />
              <el-table-column prop="owner" label="负责人" width="100" />
              <el-table-column label="截止" width="110">
                <template #default="{ row }">
                  <span v-if="!row.dueDate" style="color: #9ca3af">未设</span>
                  <span v-else :style="row.overdue ? 'color:#dc2626;font-weight:500' : ''">
                    {{ String(row.dueDate).slice(0, 10) }}
                  </span>
                </template>
              </el-table-column>
              <el-table-column label="状态" width="95">
                <template #default="{ row }">
                  <el-tag size="small" :type="itemStatusMeta[row.status]?.type || 'info'">
                    {{ row.statusLabel }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="170" fixed="right">
                <template #default="{ row }">
                  <el-button
                    v-if="row.status !== 'done' && row.status !== 'dropped'"
                    v-perm="'review:manage'"
                    link
                    type="success"
                    @click="finishItem(row)"
                  >
                    完成
                  </el-button>
                  <el-button v-perm="'review:manage'" link @click="openItemDialog(row)">编辑</el-button>
                  <el-button v-if="!archived" v-perm="'review:manage'" link type="danger" @click="removeItem(row)">
                    删除
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="证据" name="evidence">
            <div v-loading="evidenceLoading">
              <template v-if="evidence">
                <el-alert type="info" :closable="false" style="margin-bottom: 12px" :title="evidence.window.note" />
                <div style="margin-bottom: 12px; color: #6b7280; font-size: 12px">
                  时间窗 {{ evidence.window.from }} ~ {{ evidence.window.to }}
                  <div v-for="src in evidence.sources" :key="src">· {{ src }}</div>
                </div>

                <el-divider content-position="left">通知投递 ({{ evidence.notifyRecords.length }})</el-divider>
                <el-table :data="evidence.notifyRecords" border size="small" empty-text="没有投递记录，说明当时没有发出任何通知">
                  <el-table-column prop="channelName" label="渠道" min-width="120" />
                  <el-table-column prop="routeName" label="路由" min-width="120" />
                  <el-table-column prop="status" label="结果" width="80" />
                  <el-table-column prop="httpStatus" label="HTTP" width="80" />
                  <el-table-column prop="errorMsg" label="错误" min-width="160" show-overflow-tooltip />
                  <el-table-column prop="createdAt" label="时间" min-width="170" />
                </el-table>

                <el-divider content-position="left">叫人记录 ({{ evidence.escalations.length }})</el-divider>
                <el-table :data="evidence.escalations" border size="small" empty-text="没有叫人记录">
                  <el-table-column prop="level" label="级别" width="70" />
                  <el-table-column prop="userName" label="被叫" min-width="110" />
                  <el-table-column prop="source" label="来源" width="90" />
                  <el-table-column prop="channel" label="渠道" width="90" />
                  <el-table-column prop="status" label="结果" width="80" />
                  <el-table-column prop="createdAt" label="时间" min-width="170" />
                </el-table>

                <el-divider content-position="left">
                  为什么没收到通知（静默 {{ evidence.silences.length }} · 抑制 {{ evidence.suppressed.length }}）
                </el-divider>
                <el-table
                  v-if="evidence.silences.length"
                  :data="evidence.silences"
                  border
                  size="small"
                  style="margin-bottom: 8px"
                >
                  <el-table-column prop="name" label="静默规则" min-width="150" />
                  <el-table-column prop="startAt" label="开始" min-width="170" />
                  <el-table-column prop="endAt" label="结束" min-width="170" />
                  <el-table-column prop="hitCount" label="命中" width="80" />
                </el-table>
                <el-table v-if="evidence.suppressed.length" :data="evidence.suppressed" border size="small">
                  <el-table-column prop="alertId" label="告警" width="80" />
                  <el-table-column prop="title" label="标题" min-width="180" show-overflow-tooltip />
                  <el-table-column prop="suppressedBy" label="被哪条聚合策略抑制" min-width="160" />
                </el-table>
                <el-empty
                  v-if="!evidence.silences.length && !evidence.suppressed.length"
                  description="没有静默或聚合抑制，通知路径上没有被挡"
                  :image-size="60"
                />

                <el-divider content-position="left">时间窗内的下发拦截 ({{ evidence.execGuardLogs.length }})</el-divider>
                <el-table :data="evidence.execGuardLogs" border size="small" empty-text="时间窗内没有命令被规则拦住">
                  <el-table-column prop="username" label="操作人" width="110" />
                  <el-table-column prop="status" label="结果" width="80" />
                  <el-table-column prop="command" label="命令" min-width="200" show-overflow-tooltip />
                  <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
                  <el-table-column prop="createdAt" label="时间" min-width="170" />
                </el-table>

                <el-divider content-position="left">相关主机指标 ({{ evidence.hostMetrics.length }})</el-divider>
                <el-table :data="evidence.hostMetrics" border size="small" empty-text="没有采到指标（告警标签对不上主机，或当时没有采集）">
                  <el-table-column prop="hostId" label="主机" width="80" />
                  <el-table-column label="CPU" width="90">
                    <template #default="{ row }">{{ row.cpuPercent?.toFixed?.(1) ?? row.cpuPercent }}%</template>
                  </el-table-column>
                  <el-table-column label="内存" width="90">
                    <template #default="{ row }">{{ row.memPercent?.toFixed?.(1) ?? row.memPercent }}%</template>
                  </el-table-column>
                  <el-table-column label="磁盘" width="110">
                    <template #default="{ row }">
                      {{ row.diskMaxPercent?.toFixed?.(1) ?? row.diskMaxPercent }}% {{ row.diskMaxMount }}
                    </template>
                  </el-table-column>
                  <el-table-column prop="load1" label="Load1" width="90" />
                  <el-table-column prop="createdAt" label="时间" min-width="170" />
                </el-table>

                <el-divider content-position="left">相关拨测记录 ({{ evidence.probeRecords.length }})</el-divider>
                <el-table :data="evidence.probeRecords" border size="small" empty-text="没有拨测记录">
                  <el-table-column prop="probeId" label="拨测" width="80" />
                  <el-table-column prop="status" label="结果" width="80" />
                  <el-table-column prop="code" label="状态码" width="90" />
                  <el-table-column prop="costMs" label="耗时(ms)" width="100" />
                  <el-table-column prop="errorMsg" label="错误" min-width="160" show-overflow-tooltip />
                  <el-table-column prop="createdAt" label="时间" min-width="170" />
                </el-table>
              </template>
            </div>
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-drawer>

    <el-dialog v-model="itemDialog" :title="editingItem ? '编辑改进项' : '新增改进项'" width="560px">
      <el-form label-width="90px">
        <el-form-item label="标题" required>
          <el-input v-model="itemForm.title" placeholder="要做什么，一句话说清" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="itemForm.kind" style="width: 180px">
            <el-option v-for="k in kindOptions" :key="k.value" :label="k.label" :value="k.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="负责人" required>
          <el-select v-model="itemForm.owner" filterable style="width: 180px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item label="截止日期">
          <el-date-picker
            v-model="itemForm.dueDate"
            type="date"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            placeholder="不设则不会催办"
            style="width: 180px"
          />
        </el-form-item>
        <el-form-item label="状态">
          <el-select v-model="itemForm.status" style="width: 140px">
            <el-option v-for="(meta, key) in itemStatusMeta" :key="key" :label="meta.text" :value="key" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="itemForm.detail" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="itemDialog = false">取消</el-button>
        <el-button type="primary" @click="submitItem">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
