<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  addSecurityEventNote,
  blockSecurityEventSource,
  collectSecurityEvents,
  getSecurityEvent,
  getSecurityEventStats,
  listHosts,
  listSecurityEvents,
  listSecurityMutes,
  deleteSecurityMute,
  listUsers,
  triageSecurityEvents,
  type SecurityEvent,
  type SecurityEventDetail,
  type SecurityEventLog,
  type SecurityEventMute,
  type SecurityEventStats,
  type User
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

const loading = ref(false)
const rows = ref<SecurityEvent[]>([])
const total = ref(0)
const stats = ref<SecurityEventStats | null>(null)
const query = reactive({ page: 1, pageSize: 20, status: '', source: '', severity: '', actorIp: '', keyword: '', rehit: '' })
const users = ref<User[]>([])
const selection = ref<SecurityEvent[]>([])

const detailVisible = ref(false)
const detail = ref<SecurityEventDetail | null>(null)
const noteContent = ref('')

// 白名单标签页
const muteTab = ref(false)
const muteRows = ref<SecurityEventMute[]>([])
const muteTotal = ref(0)
const mutePage = reactive({ page: 1, pageSize: 20 })

const severityMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '警告', type: 'warning' },
  info: { text: '提示', type: 'info' }
}

const statusMeta: Record<string, { text: string; type: '' | 'success' | 'warning' | 'danger' | 'info' }> = {
  new: { text: '待研判', type: 'danger' },
  investigating: { text: '研判中', type: 'warning' },
  confirmed: { text: '确认威胁', type: 'danger' },
  'false-positive': { text: '误报', type: 'info' },
  ignored: { text: '忽略', type: 'info' },
  handled: { text: '已处置', type: 'success' }
}

const sourceMeta: Record<string, string> = {
  'exec-guard': '下发闸门', terminal: 'Web 终端', exposure: '暴露面扫描', authz: '越权拦截'
}

const actionLabels: Record<string, string> = {
  collect: '采集', status: '研判', note: '处置记录', respond: '处置动作', rehit: '结案后又命中'
}

async function load() {
  loading.value = true
  try {
    const [data, s] = await Promise.all([listSecurityEvents(query), getSecurityEventStats()])
    rows.value = data.list || []
    total.value = data.total
    stats.value = s
  } finally {
    loading.value = false
  }
}

const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `共 ${total.value} 条 · 本页 ${rows.value.length}`, static: true },
    { key: 'status:new', label: '待研判', count: s?.new ?? 0, hint: '没人看过', tone: 'danger' },
    { key: 'status:investigating', label: '研判中', count: s?.investigating ?? 0, tone: 'warning' },
    { key: 'status:confirmed', label: '确认威胁', count: s?.confirmed ?? 0, tone: 'danger' },
    { key: 'severity:critical', label: '严重待办', count: s?.critical ?? 0, hint: '严重且未结案' },
    { key: 'rehit', label: '结案后又命中', count: s?.rehit ?? 0, hint: '判错了还是还在继续', tone: 'warning' },
    { key: 'status:handled', label: '已处置', count: s?.handled ?? 0, tone: 'success' }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.rehit === '1') return 'rehit'
  // severity 要先判：「严重待办」这个 chip 会同时写 severity 与 status，
  // 先判 status 的话它算出来是 status:open，跟任何 chip 都对不上，永远不高亮
  if (query.severity) return `severity:${query.severity}`
  if (query.status) return `status:${query.status}`
  return null
})

function onChipSelect(key: string) {
  if (key === 'info') return
  const wasActive = activeChipKey.value === key
  query.status = ''
  query.severity = ''
  query.rehit = ''
  if (wasActive) {
    query.page = 1
    load()
    return
  }
  if (key === 'rehit') {
    query.rehit = '1'
  } else if (key.startsWith('status:')) {
    query.status = key.split(':')[1]
  } else if (key === 'severity:critical') {
    query.severity = 'critical'
    query.status = 'open'
  }
  query.page = 1
  load()
}

function resetFilters() {
  Object.assign(query, { status: '', source: '', severity: '', actorIp: '', keyword: '', rehit: '', page: 1 })
  load()
}

function onSelectionChange(rows: SecurityEvent[]) {
  selection.value = rows
}

async function openDetail(row: SecurityEvent) {
  detail.value = await getSecurityEvent(row.id)
  noteContent.value = ''
  detailVisible.value = true
}

async function submitNote() {
  if (!detail.value || !noteContent.value.trim()) return
  await addSecurityEventNote(detail.value.event.id, noteContent.value.trim())
  ElMessage.success('已记录')
  detail.value = await getSecurityEvent(detail.value.event.id)
  noteContent.value = ''
  load()
}

const collecting = ref(false)

async function doCollect() {
  // 采集是「读游标 → 消费 → 推游标」，连点会让两轮采集抢同一个游标，这里挡住
  if (collecting.value) return
  collecting.value = true
  try {
    const res = await collectSecurityEvents()
    ElMessage.success(`采集完成：新建 ${res.created}，累加 ${res.updated}，结案后又命中 ${res.rehit}，白名单挡掉 ${res.muted}`)
    load()
  } finally {
    collecting.value = false
  }
}

// 研判（支持批量）
async function doTriage(status: string, targets?: SecurityEvent[]) {
  // 去重：详情页的按钮和列表勾选可能指向同一条，后端按「查回行数 != ids 长度」判存在性
  const ids = [...new Set((targets || selection.value).map((r) => r.id))]
  if (!ids.length) {
    ElMessage.warning('请先选择事件')
    return
  }

  const needsVerdict = ['false-positive', 'ignored', 'handled'].includes(status)
  let verdict = ''
  let owner = ''
  const label = statusMeta[status]?.text || status

  // 三种弹窗都要接住「取消」，否则 ElMessageBox 的 reject 会变成未处理的 rejection
  try {
    if (needsVerdict) {
      const { value } = await ElMessageBox.prompt(
        `将 ${ids.length} 条事件判为「${label}」。写清结论（必填）：`,
        '研判结论',
        { inputType: 'textarea', inputValidator: (v) => (v?.trim() ? true : '必须写结论') }
      )
      verdict = value
    } else if (status === 'investigating') {
      const { value } = await ElMessageBox.prompt(
        `将 ${ids.length} 条事件转为「研判中」。可指定负责人（选填）：`,
        '开始研判',
        { inputValue: '', inputValidator: () => true }
      )
      owner = value?.trim() || ''
    } else {
      await ElMessageBox.confirm(`确认将 ${ids.length} 条事件设为「${label}」？`, '批量研判')
    }
  } catch {
    return
  }

  const res = await triageSecurityEvents({ ids, status, verdict, owner })
  ElMessage.success(`已处理 ${res.changed} 条` + (res.muted ? `，${res.muted} 条加入白名单` : ''))
  selection.value = []
  if (detail.value && ids.includes(detail.value.event.id)) {
    detail.value = await getSecurityEvent(detail.value.event.id)
  }
  load()
}

// 白名单
async function loadMutes() {
  const data = await listSecurityMutes(mutePage)
  muteRows.value = data.list || []
  muteTotal.value = data.total
}

async function revokeMute(row: SecurityEventMute) {
  await ElMessageBox.confirm(
    `撤销这条白名单后，同样的事再发生会重新建事件。期间挡掉 ${row.hitCount} 次，确认撤销？`,
    '撤销白名单', { type: 'warning' }
  )
  const res = await deleteSecurityMute(row.id)
  ElMessage.success(`已撤销，期间挡掉 ${res.blockedHits} 次`)
  loadMutes()
  load()
}

// 封禁草稿
const blockVisible = ref(false)
const blockForm = reactive({ hostId: undefined as number | undefined, port: '' })
const hosts = ref<{ id: number; name: string; env: string }[]>([])

async function openBlock() {
  if (!detail.value) return
  if (!detail.value.event.actorIp) {
    ElMessage.warning('这条事件没有源地址，生不出封禁规则')
    return
  }
  if (!hosts.value.length) {
    const data = await listHosts({ page: 1, pageSize: 200 })
    hosts.value = (data.list || []).map((h: any) => ({ id: h.id, name: h.name, env: h.env }))
  }
  blockForm.hostId = undefined
  // 端口默认跟事件走：暴露面事件有自己的端口，终端/下发是 ssh 走 22
  blockForm.port = detail.value.event.port || (detail.value.event.protocol === 'ssh' ? '22' : '')
  blockVisible.value = true
}

async function submitBlock() {
  if (!detail.value || !blockForm.hostId) { ElMessage.warning('请选择主机'); return }
  const res = await blockSecurityEventSource(detail.value.event.id, {
    hostId: blockForm.hostId,
    port: blockForm.port.trim()
  })
  ElMessage.success(res.message)
  blockVisible.value = false
  detail.value = await getSecurityEvent(detail.value.event.id)
}

function toggleMuteTab() {
  muteTab.value = !muteTab.value
  if (muteTab.value) loadMutes()
  else load()
}

onMounted(async () => {
  const data = await listUsers({ page: 1, pageSize: 100 })
  users.value = data.list || []
  load()
})
</script>

<template>
  <div class="page">
    <PageHeader title="安全事件" subtitle="从下发闸门/终端拦截/暴露面扫描/越权拦截自动采集，不提供手工新建">
      <template #actions>
        <el-button @click="toggleMuteTab">{{ muteTab ? '回到事件' : '误报白名单' }}</el-button>
        <el-button v-perm="'secevent:manage'" :loading="collecting" @click="doCollect">立即采集</el-button>
      </template>
    </PageHeader>

    <!-- 白名单视图 -->
    <el-card v-if="muteTab">
      <el-table v-loading="loading" :data="muteRows" border stripe empty-text="没有白名单条目">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="sourceLabel" label="来源" width="110" />
        <el-table-column prop="title" label="原事件标题" min-width="200" show-overflow-tooltip />
        <el-table-column prop="reason" label="判断理由" min-width="200" show-overflow-tooltip />
        <el-table-column label="挡掉次数" width="110">
          <template #default="{ row }">
            <span :style="{ color: row.hitCount > 50 ? '#dc2626' : row.hitCount > 10 ? '#d97706' : '' }">
              {{ row.hitCount }}
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="lastHitAt" label="最近挡掉" min-width="170" />
        <el-table-column prop="operator" label="创建人" width="100" />
        <el-table-column prop="createdAt" label="创建时间" min-width="170" />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'secevent:manage'" size="small" type="danger" plain @click="revokeMute(row)">撤销</el-button>
          </template>
        </el-table-column>
      </el-table>
      <Pagination v-model:current-page="mutePage.page" v-model:page-size="mutePage.pageSize" :total="muteTotal" @change="loadMutes" />
    </el-card>

    <!-- 事件列表 -->
    <el-card v-else>
      <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

      <div class="page-toolbar" style="margin-top: 12px">
        <el-input v-model="query.keyword" placeholder="标题/发起方/目标/证据" style="width: 200px" clearable
          @keyup.enter="((query.page = 1), load())" />
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 120px">
          <el-option label="待办（未结案）" value="open" />
          <el-option v-for="(meta, key) in statusMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-select v-model="query.source" placeholder="来源" clearable style="width: 130px">
          <el-option v-for="(label, key) in sourceMeta" :key="key" :label="label" :value="key" />
        </el-select>
        <el-select v-model="query.severity" placeholder="级别" clearable style="width: 110px">
          <el-option v-for="(meta, key) in severityMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-input v-model="query.actorIp" placeholder="源地址" style="width: 140px" clearable
          @keyup.enter="((query.page = 1), load())" />
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="resetFilters">重置</el-button>
      </div>

      <!-- 批量操作栏 -->
      <div v-if="selection.length" class="page-toolbar" style="margin-top: 8px">
        <span style="color: #6b7280">已选 {{ selection.length }} 条</span>
        <el-button v-perm="'secevent:manage'" size="small" @click="doTriage('investigating')">转研判中</el-button>
        <el-button v-perm="'secevent:manage'" size="small" type="success" @click="doTriage('handled')">已处置</el-button>
        <el-button v-perm="'secevent:manage'" size="small" type="info" @click="doTriage('false-positive')">误报</el-button>
        <el-button v-perm="'secevent:manage'" size="small" type="info" @click="doTriage('ignored')">忽略</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe
        empty-text="还没有安全事件。点「立即采集」或等定时任务从已有流水中自动生成"
        @selection-change="onSelectionChange">
        <el-table-column type="selection" width="42"
          :selectable="(row: SecurityEvent) => !['false-positive','ignored','handled'].includes(row.status)" />
        <el-table-column prop="id" label="ID" width="65" />
        <el-table-column label="来源" width="110">
          <template #default="{ row }">{{ row.sourceLabel }}</template>
        </el-table-column>
        <el-table-column prop="title" label="标题" min-width="220" show-overflow-tooltip />
        <el-table-column label="级别" width="85">
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
            <div v-if="row.hitsAfterClose > 0" style="color: #dc2626; font-size: 11px; margin-top: 2px">
              结案后又命中 {{ row.hitsAfterClose }} 次
            </div>
          </template>
        </el-table-column>
        <el-table-column label="发起方" width="130">
          <template #default="{ row }">
            <div v-if="row.actor">{{ row.actor }}</div>
            <div v-if="row.actorIp" style="color: #6b7280; font-size: 12px">{{ row.actorIp }}</div>
            <span v-if="!row.actor && !row.actorIp" style="color: #9ca3af">—</span>
          </template>
        </el-table-column>
        <el-table-column label="目标" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.target || '—' }}
            <span v-if="row.port" style="color: #6b7280">:{{ row.port }}</span>
          </template>
        </el-table-column>
        <el-table-column label="命中" width="70">
          <template #default="{ row }">{{ row.hitCount }}</template>
        </el-table-column>
        <el-table-column prop="lastSeenAt" label="最近命中" min-width="170" />
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" link @click="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>
      <Pagination v-model:current-page="query.page" v-model:page-size="query.pageSize" :total="total" @change="load" />
    </el-card>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detailVisible"
      :title="`安全事件 #${detail?.event.id ?? ''} · ${detail?.event.title ?? ''}`" size="62%">
      <template v-if="detail">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="状态">
            <el-tag size="small" :type="statusMeta[detail.event.status]?.type || 'info'">
              {{ detail.event.statusLabel }}
            </el-tag>
            <span v-if="detail.event.hitsAfterClose > 0" style="color: #dc2626; margin-left: 8px">
              结案后又命中 {{ detail.event.hitsAfterClose }} 次
            </span>
          </el-descriptions-item>
          <el-descriptions-item label="级别">
            <el-tag size="small" :type="severityMeta[detail.event.severity]?.type || 'info'">
              {{ severityMeta[detail.event.severity]?.text || detail.event.severity }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="来源">{{ detail.event.sourceLabel }}</el-descriptions-item>
          <el-descriptions-item label="命中次数">{{ detail.event.hitCount }}</el-descriptions-item>
          <el-descriptions-item label="发起方">{{ detail.event.actor || '—' }}</el-descriptions-item>
          <el-descriptions-item label="源地址">{{ detail.event.actorIp || '—' }}</el-descriptions-item>
          <el-descriptions-item label="目标">{{ detail.event.target || '—' }}</el-descriptions-item>
          <el-descriptions-item label="端口/协议">{{ detail.event.port || '—' }} / {{ detail.event.protocol || '—' }}</el-descriptions-item>
          <el-descriptions-item label="首次/最近" :span="2">
            {{ detail.event.firstSeenAt }} — {{ detail.event.lastSeenAt }}
          </el-descriptions-item>
          <el-descriptions-item label="研判结论" :span="2">{{ detail.event.verdict || '—' }}</el-descriptions-item>
          <el-descriptions-item label="负责人">{{ detail.event.owner || '—' }}</el-descriptions-item>
          <el-descriptions-item label="溯源">{{ detail.event.refTable }}#{{ detail.event.refId }}</el-descriptions-item>
        </el-descriptions>

        <el-alert v-if="detail.muted" type="info" :closable="false" style="margin-top: 10px"
          :title="`这条指纹已在误报白名单中，之后采集器直接跳过（已挡掉 ${detail.muteHits} 次）`" />

        <!-- 证据 -->
        <el-card style="margin-top: 12px">
          <template #header>证据摘要</template>
          <pre style="white-space: pre-wrap; word-break: break-all; font-size: 13px; margin: 0">{{ detail.evidence }}</pre>
        </el-card>

        <!-- 研判操作 -->
        <div v-perm="'secevent:manage'" style="margin: 12px 0; display: flex; gap: 8px; flex-wrap: wrap; align-items: center">
          <el-button v-perm="'secevent:respond'" size="small" type="danger" @click="openBlock">封禁源地址</el-button>
          <el-button size="small" @click="doTriage('investigating', [detail.event])">转研判中</el-button>
          <el-button size="small" type="danger" @click="doTriage('confirmed', [detail.event])">确认威胁</el-button>
          <el-button size="small" type="success" @click="doTriage('handled', [detail.event])">已处置</el-button>
          <el-button size="small" type="info" @click="doTriage('false-positive', [detail.event])">误报</el-button>
          <el-button size="small" type="info" @click="doTriage('ignored', [detail.event])">忽略</el-button>
        </div>

        <!-- 处置记录 -->
        <div style="margin: 12px 0; display: flex; gap: 8px">
          <el-input v-model="noteContent" placeholder="写一条处置记录" @keyup.enter="submitNote" />
          <el-button v-perm="'secevent:manage'" type="primary" :disabled="!noteContent.trim()" @click="submitNote">记录</el-button>
        </div>

        <!-- 处置链路 -->
        <el-timeline style="margin-top: 12px">
          <el-timeline-item v-for="log in detail.logs" :key="log.id"
            :timestamp="`${log.createdAt} · ${log.operator}`" placement="top">
            <div style="font-size: 12px; color: #6b7280">{{ actionLabels[log.action] || log.action }}</div>
            <div>{{ log.content }}</div>
          </el-timeline-item>
        </el-timeline>

        <!-- 同源地址 -->
        <el-card v-if="detail.related.length" style="margin-top: 12px">
          <template #header>同一源地址（{{ detail.event.actorIp }}）的其它事件</template>
          <el-table :data="detail.related" border stripe size="small">
            <el-table-column prop="id" label="ID" width="65" />
            <el-table-column prop="sourceLabel" label="来源" width="100" />
            <el-table-column prop="title" label="标题" min-width="180" show-overflow-tooltip />
            <el-table-column prop="statusLabel" label="状态" width="90" />
            <el-table-column prop="lastSeenAt" label="最近命中" min-width="170" />
          </el-table>
        </el-card>

        <!-- 口径说明 -->
        <el-alert type="info" :closable="false" style="margin-top: 12px">
          <div v-for="note in detail.notes || []" :key="note" style="font-size: 12px">· {{ note }}</div>
        </el-alert>
      </template>
    </el-drawer>

    <!-- 封禁草稿对话框。必须挂在根节点下，不能塞进表格单元格的 #default 里 ——
         那样每一行都会实例化一份，点一次会同时弹出一屏。 -->
    <el-dialog v-model="blockVisible" title="生成封禁规则草稿" width="480px">
      <el-alert
        type="warning"
        :closable="false"
        style="margin-bottom: 10px"
        title="只登记规则草稿，不会直接动真机。到「防火墙策略」核对后再下发，下发走命令规则与生产确认"
      />
      <el-form label-width="80px">
        <el-form-item label="源地址">
          <el-input :model-value="detail?.event.actorIp" disabled />
        </el-form-item>
        <el-form-item label="端口">
          <el-input v-model="blockForm.port" placeholder="必填（底层规则模型要求 tcp 必须带端口）" />
        </el-form-item>
        <el-form-item label="挂到主机">
          <el-select v-model="blockForm.hostId" filterable placeholder="选择主机" style="width: 100%">
            <el-option v-for="h in hosts" :key="h.id" :label="`${h.name} (${h.env})`" :value="h.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="blockVisible = false">取消</el-button>
        <el-button type="primary" @click="submitBlock">生成草稿</el-button>
      </template>
    </el-dialog>
  </div>
</template>
