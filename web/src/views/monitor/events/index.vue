<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  addEventNote,
  assignEvent,
  createEvent,
  deleteEvent,
  getEvent,
  getEventStats,
  listAlerts,
  listEvents,
  listUsers,
  matchRunbooks,
  updateEventStatus,
  useRunbook,
  type Alert,
  type EventDetail,
  type EventStats,
  type OpsEvent,
  type RunbookMatch,
  type RunbookMatchResult,
  type User
} from '@/api'
import { useUserStore } from '@/stores/user'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'


const store = useUserStore()
const router = useRouter()

const loading = ref(false)
const rows = ref<OpsEvent[]>([])
const total = ref(0)
const stats = ref<EventStats | null>(null)
const query = reactive({ page: 1, pageSize: 20, status: '', severity: '', assignee: '', keyword: '' })

const users = ref<User[]>([])

const createVisible = ref(false)
const alertOptions = ref<Alert[]>([])
const createForm = reactive({ title: '', severity: '', summary: '', alertIds: [] as number[], assignee: '' })

const detailVisible = ref(false)
const detail = ref<EventDetail | null>(null)
const noteContent = ref('')
const assignTo = ref('')

const statusMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'success' | 'info' }> = {
  open: { text: '待处理', type: 'danger' },
  processing: { text: '处理中', type: 'warning' },
  resolved: { text: '已解决', type: 'success' },
  closed: { text: '已关闭', type: 'info' }
}

const severityMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '警告', type: 'warning' },
  info: { text: '提示', type: 'info' }
}

const actionText: Record<string, string> = {
  create: '建单',
  assign: '指派',
  note: '处置记录',
  status: '状态变更',
  review: '复盘',
  runbook: '剧本处置'
}

// 允许的状态流转，和后端保持一致；前端只用来控制按钮显示
const statusFlow: Record<string, string[]> = {
  open: ['processing', 'resolved', 'closed'],
  processing: ['resolved', 'closed'],
  resolved: ['processing', 'closed'],
  closed: ['open']
}

async function load() {
  loading.value = true
  try {
    const [data, s] = await Promise.all([listEvents(query), getEventStats()])
    rows.value = data.list || []
    total.value = data.total
    stats.value = s
  } finally {
    loading.value = false
  }
}

/** 快筛 chips：状态 + 责任维度。「未指派 / 今日」后端暂不支持独立筛选，只做计数展示 */
const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `当前 ${total.value} 条 · 本页 ${rows.value.length}`, static: true },
    { key: 'status:open', label: '待处理', count: s?.open ?? 0, hint: '需要认领', tone: 'danger' },
    { key: 'status:processing', label: '处理中', count: s?.processing ?? 0, hint: '已认领', tone: 'warning' },
    { key: 'status:resolved', label: '已解决', count: s?.resolved ?? 0, hint: '仅回溯', tone: 'success' },
    { key: 'mine', label: '我负责', count: s?.mine ?? 0, hint: '指派给我的' },
    { key: 'unassigned', label: '未指派', count: s?.unassigned ?? 0, hint: '待处理+处理中', static: true },
    { key: 'today', label: '今日新增', count: s?.today ?? 0, static: true }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.status) return `status:${query.status}`
  if (query.assignee && query.assignee === store.profile?.username) return 'mine'
  return null
})

function onChipSelect(key: string) {
  if (key === 'info' || key === 'unassigned' || key === 'today') return
  const wasActive = activeChipKey.value === key
  query.status = ''
  query.assignee = ''
  if (!wasActive && key === 'mine') query.assignee = store.profile?.username || ''
  else if (!wasActive && key.startsWith('status:')) query.status = key.split(':')[1]
  query.page = 1
  load()
}

function resetFilters() {
  query.status = ''
  query.severity = ''
  query.assignee = ''
  query.keyword = ''
  query.page = 1
  load()
}

async function openCreate() {
  const data = await listAlerts({ page: 1, pageSize: 100, status: 'firing' })
  alertOptions.value = data.list || []
  Object.assign(createForm, { title: '', severity: '', summary: '', alertIds: [], assignee: '' })
  createVisible.value = true
}

async function submitCreate() {
  if (!createForm.alertIds.length) {
    ElMessage.warning('请至少选择一条告警')
    return
  }
  const created = await createEvent({ ...createForm })
  ElMessage.success(`已建单 #${created.id}`)
  createVisible.value = false
  load()
  openDetail(created)
}

async function openDetail(row: OpsEvent) {
  detail.value = await getEvent(row.id)
  noteContent.value = ''
  assignTo.value = detail.value.event.assignee
  detailVisible.value = true
  loadRunbooks(row.id)
}

// ---------- 推荐剧本 ----------
const runbookResult = ref<RunbookMatchResult | null>(null)
const runbookLoading = ref(false)

async function loadRunbooks(eventId: number) {
  runbookLoading.value = true
  runbookResult.value = null
  try {
    runbookResult.value = await matchRunbooks({ eventId })
  } finally {
    runbookLoading.value = false
  }
}

/** 事件关联主机里已纳管的那些，用来给「去执行」预选目标 */
const contextHostIds = computed<number[]>(() =>
  (detail.value?.context.hosts || []).map((h) => h.id).filter((id): id is number => !!id)
)

/** 把某一步的命令带到批量执行页预填。只预填不执行：预检与生产确认还是要人点 */
function runStep(command: string, bookName: string) {
  router.push({
    path: '/execute/batch',
    query: {
      command,
      name: `剧本「${bookName}」`,
      hosts: contextHostIds.value.join(',')
    }
  })
}

const useDialog = ref(false)
const usingBook = ref<RunbookMatch | null>(null)
const useForm = reactive({ outcome: 'resolved', doneSteps: [] as number[], note: '' })

function openUseDialog(match: RunbookMatch) {
  usingBook.value = match
  Object.assign(useForm, {
    outcome: 'resolved',
    doneSteps: match.runbook.steps.map((_, idx) => idx),
    note: ''
  })
  useDialog.value = true
}

async function submitUse() {
  if (!usingBook.value || !detail.value) return
  await useRunbook(usingBook.value.runbook.id, {
    eventId: detail.value.event.id,
    outcome: useForm.outcome,
    doneSteps: useForm.doneSteps,
    note: useForm.note
  })
  ElMessage.success('已记录，处置时间线里能看到')
  useDialog.value = false
  refreshDetail()
  loadRunbooks(detail.value.event.id)
}

async function refreshDetail() {
  if (detail.value) {
    detail.value = await getEvent(detail.value.event.id)
  }
  load()
}

async function submitNote() {
  if (!detail.value || !noteContent.value.trim()) {
    ElMessage.warning('请输入处置记录')
    return
  }
  await addEventNote(detail.value.event.id, noteContent.value.trim())
  noteContent.value = ''
  ElMessage.success('已记录')
  refreshDetail()
}

async function submitAssign() {
  if (!detail.value || !assignTo.value) {
    ElMessage.warning('请选择负责人')
    return
  }
  await assignEvent(detail.value.event.id, { assignee: assignTo.value })
  ElMessage.success('已指派，并给负责人发了站内消息')
  refreshDetail()
}

async function changeStatus(target: string) {
  if (!detail.value) return
  const event = detail.value.event
  let resolveAlerts = false
  let note = ''

  if (target === 'resolved' && event.alertIds.length) {
    const choice = await ElMessageBox.confirm(
      `该事件关联 ${event.alertIds.length} 条告警，是否同时把它们标记为已恢复？`,
      '标记已解决',
      { confirmButtonText: '同时恢复告警', cancelButtonText: '只解决事件', type: 'warning', distinguishCancelAndClose: true }
    ).then(() => true).catch((action) => {
      if (action === 'cancel') return false
      return null
    })
    if (choice === null) return
    resolveAlerts = choice
  }

  if (target === 'closed') {
    const input = await ElMessageBox.prompt('关闭原因（会记入时间线）', '关闭事件', {
      inputPlaceholder: '例如：误报 / 已由其他事件覆盖'
    }).catch(() => null)
    if (!input) return
    note = input.value || ''
  }

  const res = await updateEventStatus(event.id, { status: target, note, resolveAlerts })
  ElMessage.success(
    res.resolvedAlerts ? `已更新状态，并恢复 ${res.resolvedAlerts} 条告警` : '已更新状态'
  )
  if (res.reviewCreated) {
    ElMessage.info('已自动创建复盘草稿，可在「事件复盘」里补齐根因与改进项')
  }
  refreshDetail()
}

/** 跳到复盘页并直接打开这条事件的复盘 */
function gotoReview(id: number) {
  router.push({ path: '/monitor/reviews', query: { event: String(id) } })
}

async function remove(row: OpsEvent) {
  await ElMessageBox.confirm(`确认删除事件「${row.title}」？关联告警不受影响`, '提示', { type: 'warning' })
  await deleteEvent(row.id)
  ElMessage.success('已删除')
  detailVisible.value = false
  load()
}

onMounted(async () => {
  const data = await listUsers({ page: 1, pageSize: 100 })
  users.value = data.list || []
  load()
})
</script>

<template>
  <div class="page">
    <PageHeader title="事件" subtitle="告警是机器发现的现象，事件是人要跟进的事：可指派负责人、留处置记录">
      <template #actions>
        <el-button v-perm="'event:manage'" type="primary" @click="openCreate">从告警建单</el-button>
      </template>
    </PageHeader>

    <el-card>
      <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

      <div class="page-toolbar" style="margin-top: 12px">
        <el-input
          v-model="query.keyword"
          placeholder="标题 / 摘要"
          style="width: 200px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 120px">
          <el-option v-for="(meta, key) in statusMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-select v-model="query.severity" placeholder="级别" clearable style="width: 110px">
          <el-option v-for="(meta, key) in severityMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-select v-model="query.assignee" placeholder="负责人" clearable filterable style="width: 140px">
          <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="resetFilters">重置</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有事件，可从告警或聚合桶建单">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip />
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="severityMeta[row.severity]?.type || 'info'">
              {{ severityMeta[row.severity]?.text || row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ row.statusLabel }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="关联告警" width="100">
          <template #default="{ row }">{{ row.alertIds.length }} 条</template>
        </el-table-column>
        <el-table-column label="负责人" width="110">
          <template #default="{ row }">
            <span v-if="row.assignee">{{ row.assignee }}</span>
            <el-tag v-else size="small" type="warning">未指派</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="110">
          <template #default="{ row }">
            {{ row.origin === 'bucket' ? '聚合桶' : '手动' }}
          </template>
        </el-table-column>
        <el-table-column prop="lastActivityAt" label="最近活动" min-width="180" />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <el-button
              v-if="row.status === 'resolved' || row.status === 'closed'"
              link
              type="warning"
              @click="gotoReview(row.id)"
            >
              复盘
            </el-button>
            <el-button v-perm="'event:manage'" link type="danger" @click="remove(row)">删除</el-button>
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

    <el-dialog v-model="createVisible" title="从告警建单" width="600px">
      <el-form label-width="100px">
        <el-form-item label="关联告警">
          <el-select v-model="createForm.alertIds" multiple filterable style="width: 100%" placeholder="选择触发中的告警">
            <el-option
              v-for="a in alertOptions"
              :key="a.id"
              :label="`#${a.id} [${severityMeta[a.severity]?.text}] ${a.title}`"
              :value="a.id"
            />
          </el-select>
          <div style="margin-top: 4px; color: #6b7280; font-size: 12px">
            只列出触发中的告警；级别留空时取所选告警里最高的那个
          </div>
        </el-form-item>
        <el-form-item label="标题">
          <el-input v-model="createForm.title" placeholder="留空则用首条告警标题自动生成" />
        </el-form-item>
        <el-form-item label="级别">
          <el-select v-model="createForm.severity" clearable placeholder="自动" style="width: 150px">
            <el-option v-for="(meta, key) in severityMeta" :key="key" :label="meta.text" :value="key" />
          </el-select>
        </el-form-item>
        <el-form-item label="负责人">
          <el-select v-model="createForm.assignee" clearable filterable style="width: 200px" placeholder="可稍后指派">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="createForm.summary" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" @click="submitCreate">建单</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="`事件 #${detail?.event.id ?? ''} · ${detail?.event.title ?? ''}`" size="62%">
      <template v-if="detail">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="状态">
            <el-tag size="small" :type="statusMeta[detail.event.status]?.type || 'info'">
              {{ detail.event.statusLabel }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="级别">
            <el-tag size="small" :type="severityMeta[detail.event.severity]?.type || 'info'">
              {{ severityMeta[detail.event.severity]?.text || detail.event.severity }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="负责人">{{ detail.event.assignee || '未指派' }}</el-descriptions-item>
          <el-descriptions-item label="建单人">{{ detail.event.createdByName }}</el-descriptions-item>
          <el-descriptions-item label="来源" :span="2">
            {{ detail.event.origin === 'bucket' ? '聚合桶' : '手动' }}
            <span v-if="detail.event.originNote" style="color: #6b7280">（{{ detail.event.originNote }}）</span>
          </el-descriptions-item>
          <el-descriptions-item label="说明" :span="2">{{ detail.event.summary || '-' }}</el-descriptions-item>
        </el-descriptions>

        <div v-perm="'event:manage'" style="margin: 12px 0; display: flex; gap: 8px; flex-wrap: wrap; align-items: center">
          <el-select v-model="assignTo" filterable placeholder="选择负责人" style="width: 160px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
          <el-button @click="submitAssign">指派</el-button>
          <el-divider direction="vertical" />
          <el-button
            v-for="target in statusFlow[detail.event.status] || []"
            :key="target"
            :type="target === 'resolved' ? 'success' : target === 'closed' ? 'info' : 'warning'"
            plain
            @click="changeStatus(target)"
          >
            标记{{ statusMeta[target]?.text }}
          </el-button>
          <el-divider direction="vertical" />
          <el-button type="primary" plain @click="gotoReview(detail.event.id)">去复盘</el-button>
        </div>

        <el-tabs>
          <el-tab-pane label="处置时间线">
            <div v-perm="'event:manage'" style="display: flex; gap: 8px; margin-bottom: 12px">
              <el-input v-model="noteContent" placeholder="记录排查过程与结论" @keyup.enter="submitNote" />
              <el-button type="primary" @click="submitNote">添加</el-button>
            </div>
            <el-timeline>
              <el-timeline-item
                v-for="log in detail.logs"
                :key="log.id"
                :timestamp="`${log.createdAt} · ${log.operator}`"
              >
                <el-tag size="small" style="margin-right: 6px">{{ actionText[log.action] || log.action }}</el-tag>
                {{ log.content }}
              </el-timeline-item>
            </el-timeline>
          </el-tab-pane>

          <el-tab-pane :label="`关联告警 (${detail.alerts.length})`">
            <el-table :data="detail.alerts" border size="small">
              <el-table-column prop="id" label="ID" width="70" />
              <el-table-column label="级别" width="80">
                <template #default="{ row }">
                  <el-tag size="small" :type="severityMeta[row.severity]?.type || 'info'">
                    {{ severityMeta[row.severity]?.text || row.severity }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="title" label="标题" min-width="180" show-overflow-tooltip />
              <el-table-column prop="status" label="状态" width="90" />
              <el-table-column prop="count" label="次数" width="70" />
              <el-table-column prop="suppressedBy" label="被抑制" min-width="120" show-overflow-tooltip />
              <el-table-column prop="lastSeenAt" label="最近出现" min-width="170" />
            </el-table>
          </el-tab-pane>

          <el-tab-pane :label="`推荐剧本 (${runbookResult?.matches.length ?? 0})`">
            <div v-loading="runbookLoading">
              <el-alert
                v-if="runbookResult"
                type="info"
                :closable="false"
                style="margin-bottom: 12px"
                :title="`按 ${runbookResult.target} 的标签与标题匹配。${runbookResult.note}`"
              />
              <div v-if="runbookResult" style="margin-bottom: 12px; font-size: 12px; color: #6b7280">
                参与匹配的标签：
                <el-tag
                  v-for="(v, k) in runbookResult.input.labels"
                  :key="k"
                  size="small"
                  style="margin-right: 4px"
                >
                  {{ k }}={{ v }}
                </el-tag>
                <span v-if="!Object.keys(runbookResult.input.labels).length">（关联告警没有标签）</span>
              </div>

              <el-empty
                v-if="runbookResult && !runbookResult.matches.length"
                description="没有匹配的剧本。可以去「处置剧本」建一本，或从这次的复盘沉淀一本"
                :image-size="70"
              />

              <el-card
                v-for="match in runbookResult?.matches || []"
                :key="match.runbook.id"
                shadow="never"
                style="margin-bottom: 12px"
              >
                <template #header>
                  <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap">
                    <span style="font-weight: 500">{{ match.runbook.name }}</span>
                    <el-tag size="small">匹配分 {{ match.score }}</el-tag>
                    <el-tag v-if="match.runbook.generic" size="small" type="info">通用兜底</el-tag>
                    <el-tag
                      v-if="match.runbook.useCount >= 3 && match.runbook.solveCount === 0"
                      size="small"
                      type="danger"
                    >
                      用过 {{ match.runbook.useCount }} 次从未解决问题
                    </el-tag>
                    <el-tag v-else-if="match.runbook.useCount" size="small" type="success">
                      用过 {{ match.runbook.useCount }} 次 · 解决 {{ match.runbook.solveCount }}
                    </el-tag>
                    <div style="flex: 1"></div>
                    <el-button v-perm="'runbook:manage'" size="small" @click="openUseDialog(match)">
                      记录使用
                    </el-button>
                  </div>
                </template>

                <div style="margin-bottom: 8px; font-size: 12px; color: #6b7280">
                  为什么推荐它：{{ match.reasons.join('；') }}
                </div>
                <div v-if="match.runbook.summary" style="margin-bottom: 8px">{{ match.runbook.summary }}</div>
                <el-alert
                  v-if="match.runbook.precheck"
                  type="warning"
                  :closable="false"
                  style="margin-bottom: 8px"
                  :title="`动手前：${match.runbook.precheck}`"
                />

                <div
                  v-for="(step, idx) in match.runbook.steps"
                  :key="idx"
                  style="padding: 6px 0; border-top: 1px solid #f3f4f6"
                >
                  <div style="display: flex; align-items: center; gap: 8px">
                    <el-tag size="small">{{ idx + 1 }}</el-tag>
                    <span style="flex: 1">{{ step.title }}</span>
                    <el-button
                      v-if="step.command"
                      link
                      type="primary"
                      size="small"
                      @click="runStep(step.command, match.runbook.name)"
                    >
                      去执行这一步
                    </el-button>
                  </div>
                  <div v-if="step.command" style="margin: 4px 0 0 32px">
                    <code style="background: #f3f4f6; padding: 2px 6px; border-radius: 4px; font-size: 12px">
                      {{ step.command }}
                    </code>
                  </div>
                  <div v-if="step.detail" style="margin: 2px 0 0 32px; color: #6b7280; font-size: 12px">
                    {{ step.detail }}
                  </div>
                </div>

                <div v-if="match.runbook.rollback" style="margin-top: 8px; color: #b45309; font-size: 12px">
                  回退办法：{{ match.runbook.rollback }}
                </div>
              </el-card>
            </div>
          </el-tab-pane>

          <el-tab-pane label="诊断上下文">
            <el-alert
              type="info"
              :closable="false"
              style="margin-bottom: 12px"
              title="从告警标签里能对上平台对象的才会列出（host/domain/target 对主机，probeId/certId/ruleId 对拨测、证书、规则），对不上的标签原样展示，不做猜测。"
            />
            <div v-if="detail.context.hosts.length">
              <div style="margin-bottom: 6px; font-weight: 500">相关主机</div>
              <el-table :data="detail.context.hosts" border size="small" style="margin-bottom: 12px">
                <el-table-column prop="name" label="名称" min-width="120" />
                <el-table-column prop="address" label="地址" min-width="140" />
                <el-table-column prop="env" label="环境" width="80" />
                <el-table-column prop="status" label="状态" width="100" />
                <el-table-column prop="checkedAt" label="最近探测" min-width="170" />
              </el-table>
            </div>
            <div v-if="detail.context.probes.length">
              <div style="margin-bottom: 6px; font-weight: 500">相关拨测</div>
              <el-table :data="detail.context.probes" border size="small" style="margin-bottom: 12px">
                <el-table-column prop="name" label="名称" min-width="120" />
                <el-table-column prop="target" label="目标" min-width="160" />
                <el-table-column prop="lastStatus" label="状态" width="90" />
                <el-table-column prop="lastError" label="最近错误" min-width="160" show-overflow-tooltip />
              </el-table>
            </div>
            <div v-if="detail.context.certificates.length">
              <div style="margin-bottom: 6px; font-weight: 500">相关证书</div>
              <el-table :data="detail.context.certificates" border size="small" style="margin-bottom: 12px">
                <el-table-column prop="name" label="名称" min-width="120" />
                <el-table-column prop="domain" label="域名" min-width="140" />
                <el-table-column prop="status" label="状态" width="90" />
                <el-table-column prop="daysLeft" label="剩余天数" width="100" />
              </el-table>
            </div>
            <div v-if="detail.context.rules.length">
              <div style="margin-bottom: 6px; font-weight: 500">相关告警规则</div>
              <el-table :data="detail.context.rules" border size="small" style="margin-bottom: 12px">
                <el-table-column prop="name" label="规则" min-width="120" />
                <el-table-column prop="metric" label="指标" min-width="140" />
                <el-table-column prop="lastStatus" label="状态" width="90" />
                <el-table-column prop="lastDetail" label="明细" min-width="160" show-overflow-tooltip />
              </el-table>
            </div>
            <div v-if="Object.keys(detail.context.otherLabels).length">
              <div style="margin-bottom: 6px; font-weight: 500">其他标签</div>
              <el-tag v-for="(v, k) in detail.context.otherLabels" :key="k" size="small" style="margin-right: 6px">
                {{ k }}={{ v }}
              </el-tag>
            </div>
            <el-empty
              v-if="!detail.context.hosts.length && !detail.context.probes.length && !detail.context.certificates.length && !detail.context.rules.length && !Object.keys(detail.context.otherLabels).length"
              description="关联告警没有可对上平台对象的标签"
            />
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-drawer>

    <el-dialog v-model="useDialog" :title="`记录剧本使用 · ${usingBook?.runbook.name ?? ''}`" width="560px">
      <el-form label-width="90px">
        <el-form-item label="处置结果">
          <el-radio-group v-model="useForm.outcome">
            <el-radio value="resolved">解决了</el-radio>
            <el-radio value="partial">部分有效</el-radio>
            <el-radio value="invalid">没用</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="做了哪几步">
          <el-checkbox-group v-model="useForm.doneSteps">
            <el-checkbox
              v-for="(step, idx) in usingBook?.runbook.steps || []"
              :key="idx"
              :value="idx"
              style="display: block"
            >
              {{ idx + 1 }}. {{ step.title }}
            </el-checkbox>
          </el-checkbox-group>
        </el-form-item>
        <el-form-item label="说明">
          <el-input
            v-model="useForm.note"
            type="textarea"
            :rows="3"
            placeholder="哪一步有效 / 哪一步不对；「没用」的话写清为什么，剧本才能改"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="useDialog = false">取消</el-button>
        <el-button type="primary" @click="submitUse">记录</el-button>
      </template>
    </el-dialog>
  </div>
</template>
