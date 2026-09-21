<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
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
  updateEventStatus,
  type Alert,
  type EventDetail,
  type EventStats,
  type OpsEvent,
  type User
} from '@/api'
import { useUserStore } from '@/stores/user'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'


const store = useUserStore()

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
  status: '状态变更'
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
  refreshDetail()
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
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
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
  </div>
</template>
