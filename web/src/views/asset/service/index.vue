<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  adoptHostServices,
  checkHostServices,
  deleteHostService,
  discoverHostServices,
  getHostServiceStats,
  listHostServiceActions,
  listHostServices,
  listHosts,
  listUsers,
  operateHostService,
  updateHostService,
  type DiscoveredUnit,
  type Host,
  type HostService,
  type HostServiceAction,
  type HostServiceStats,
  type User
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

const loading = ref(false)
const rows = ref<HostService[]>([])
const total = ref(0)
const stats = ref<HostServiceStats | null>(null)
const hosts = ref<Host[]>([])
const users = ref<User[]>([])
const query = reactive({ page: 1, pageSize: 20, hostId: '', drift: '', critical: '', owner: '', keyword: '' })

const tab = ref<'services' | 'actions'>('services')
const actions = ref<HostServiceAction[]>([])
const actionTotal = ref(0)
const actionQuery = reactive({ page: 1, pageSize: 20, status: '' })

const driftMeta: Record<string, { text: string; type: 'success' | 'danger' | 'warning' | 'info' }> = {
  ok: { text: '一致', type: 'success' },
  inactive: { text: '该跑没跑', type: 'danger' },
  unexpected: { text: '该停在跑', type: 'warning' },
  disabled: { text: '该自启没自启', type: 'warning' },
  missing: { text: 'unit 不存在', type: 'danger' },
  error: { text: '巡检失败', type: 'warning' },
  unknown: { text: '未巡检', type: 'info' }
}

const activeMeta: Record<string, 'success' | 'danger' | 'warning' | 'info'> = {
  active: 'success',
  failed: 'danger',
  inactive: 'info',
  activating: 'warning',
  deactivating: 'warning'
}

async function load() {
  loading.value = true
  try {
    const data = await listHostServices(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  stats.value = await getHostServiceStats()
}

async function loadActions() {
  const data = await listHostServiceActions(actionQuery)
  actions.value = data.list || []
  actionTotal.value = data.total
}

const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `纳管 ${s?.total ?? 0} 个 · ${s?.hosts ?? 0} 台主机`, static: true },
    {
      key: 'drift:problem',
      label: '有问题',
      count: (s?.inactive ?? 0) + (s?.unexpected ?? 0) + (s?.disabled ?? 0) + (s?.missing ?? 0) + (s?.error ?? 0),
      hint: '与期望态不一致',
      tone: 'danger'
    },
    { key: 'drift:inactive', label: '该跑没跑', count: s?.inactive ?? 0, tone: 'danger' },
    { key: 'drift:disabled', label: '该自启没自启', count: s?.disabled ?? 0, hint: '重启后不会自动拉起', tone: 'warning' },
    { key: 'drift:unexpected', label: '该停在跑', count: s?.unexpected ?? 0, tone: 'warning' },
    { key: 'drift:missing', label: 'unit 不存在', count: s?.missing ?? 0, tone: 'danger' },
    { key: 'drift:ok', label: '一致', count: s?.ok ?? 0, tone: 'success' },
    {
      key: 'critical',
      label: `关键服务 ${s?.critical ?? 0}`,
      hint: (s?.criticalDrift ?? 0) > 0 ? `其中 ${s?.criticalDrift} 个正在漂移` : '全部正常',
      tone: (s?.criticalDrift ?? 0) > 0 ? 'danger' : undefined
    },
    { key: 'drift:unknown', label: '未巡检', count: s?.unknown ?? 0, static: true }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.drift) return `drift:${query.drift}`
  if (query.critical === '1') return 'critical'
  return null
})

function onChipSelect(key: string) {
  if (key === 'info') return
  const wasActive = activeChipKey.value === key
  query.drift = ''
  query.critical = ''
  if (!wasActive && key === 'critical') query.critical = '1'
  else if (!wasActive && key.startsWith('drift:')) query.drift = key.split(':')[1]
  query.page = 1
  load()
}

// ---------- 发现与纳管 ----------
const discoverVisible = ref(false)
const discoverLoading = ref(false)
const discoverHost = ref<number | ''>('')
const discovered = ref<DiscoveredUnit[]>([])
const discoverNote = ref('')
const orphanPorts = ref<number[]>([])
const orphanNote = ref('')
const portsUnavailable = ref(false)
const discoverKeyword = ref('')
const picked = ref<string[]>([])
const adoptForm = reactive({ critical: false, owner: '', remark: '', expectEnabled: true })

const discoverRows = computed(() => {
  const kw = discoverKeyword.value.trim().toLowerCase()
  if (!kw) return discovered.value
  return discovered.value.filter(
    (u) => u.unit.toLowerCase().includes(kw) || (u.description || '').toLowerCase().includes(kw)
  )
})

async function openDiscover() {
  if (!discoverHost.value) {
    discoverHost.value = query.hostId ? Number(query.hostId) : hosts.value[0]?.id || ''
  }
  discovered.value = []
  picked.value = []
  discoverKeyword.value = ''
  discoverVisible.value = true
  if (discoverHost.value) runDiscover()
}

async function runDiscover() {
  if (!discoverHost.value) {
    ElMessage.warning('请选择主机')
    return
  }
  discoverLoading.value = true
  try {
    const data = await discoverHostServices(Number(discoverHost.value))
    discovered.value = data.units || []
    discoverNote.value = data.note
    orphanPorts.value = data.orphanPorts || []
    orphanNote.value = data.orphanNote
    portsUnavailable.value = data.portsUnavailable
    picked.value = []
  } finally {
    discoverLoading.value = false
  }
}

async function submitAdopt() {
  if (!picked.value.length) {
    ElMessage.warning('请勾选要纳管的服务')
    return
  }
  const res = await adoptHostServices({
    hostId: Number(discoverHost.value),
    units: picked.value,
    critical: adoptForm.critical,
    owner: adoptForm.owner,
    remark: adoptForm.remark,
    expectEnabled: adoptForm.expectEnabled
  })
  ElMessage.success(
    `已纳管 ${res.created} 个` + (res.skipped ? `，跳过 ${res.skipped} 个（已纳管过）` : '')
  )
  if (res.note) ElMessage.warning(res.note)
  discoverVisible.value = false
  load()
  loadStats()
}

// ---------- 巡检 ----------
async function runCheck() {
  const hostId = query.hostId ? Number(query.hostId) : discoverHost.value
  if (!hostId) {
    ElMessage.warning('请先在筛选里选一台主机，巡检是按主机做的')
    return
  }
  const res = await checkHostServices(Number(hostId))
  if (res.total === 0) {
    ElMessage.info('这台主机还没有纳管的服务')
  } else if (res.drifted > 0) {
    ElMessage.warning(`巡检完成：${res.total} 个服务，其中 ${res.drifted} 个与期望态不一致`)
  } else {
    ElMessage.success(`巡检完成：${res.total} 个服务全部一致`)
  }
  load()
  loadStats()
}

// ---------- 编辑期望态 ----------
const editVisible = ref(false)
const editing = ref<HostService | null>(null)
const editForm = reactive({
  name: '',
  expectActive: true,
  expectEnabled: true,
  critical: false,
  alertEnabled: true,
  owner: '',
  remark: ''
})

function openEdit(svc: HostService) {
  editing.value = svc
  Object.assign(editForm, {
    name: svc.name,
    expectActive: svc.expectActive,
    expectEnabled: svc.expectEnabled,
    critical: svc.critical,
    alertEnabled: svc.alertEnabled,
    owner: svc.owner,
    remark: svc.remark
  })
  editVisible.value = true
}

async function submitEdit() {
  if (!editing.value) return
  const res = await updateHostService(editing.value.id, { ...editForm })
  ElMessage.success(`已保存，当前判定：${driftMeta[res.drift]?.text || res.drift}`)
  editVisible.value = false
  load()
  loadStats()
}

async function removeService(svc: HostService) {
  await ElMessageBox.confirm(
    `取消纳管「${svc.unit}@${svc.hostName}」？机器上的服务不会被动，只是平台不再盯它`,
    '提示',
    { type: 'warning' }
  )
  await deleteHostService(svc.id)
  ElMessage.success('已取消纳管')
  load()
  loadStats()
}

// ---------- 启停 ----------
const actionLabels: Record<string, string> = {
  start: '启动',
  stop: '停止',
  restart: '重启',
  reload: '重载配置',
  enable: '设开机自启',
  disable: '取消开机自启'
}
const riskyActions = ['stop', 'restart', 'disable']

async function operate(svc: HostService, action: string) {
  let confirm = ''
  if (svc.critical && riskyActions.includes(action)) {
    const input = await ElMessageBox.prompt(
      `「${svc.name}」是关键服务。要${actionLabels[action]}它，请把 unit 名抄一遍确认：${svc.unit}`,
      '关键服务二次确认',
      { inputPlaceholder: svc.unit, inputValidator: (v) => (v === svc.unit ? true : 'unit 名不一致') }
    ).catch(() => null)
    if (!input) return
    confirm = input.value
  } else {
    await ElMessageBox.confirm(
      `确认对 ${svc.unit}@${svc.hostName} 执行${actionLabels[action]}？命令会走下发闸门（命令规则 + 生产主机确认）`,
      actionLabels[action],
      { type: 'warning' }
    )
  }

  try {
    const res = await operateHostService(svc.id, { action, confirm, confirmProd: false })
    ElMessage.success(
      `${actionLabels[action]}${res.status === 'success' ? '成功' : '执行完成但有失败'}：` +
        `${res.beforeState} → ${res.afterState || '未采到'}`
    )
    load()
    loadStats()
  } catch (err: any) {
    // 生产主机确认：后端闸门拒绝时再问一次
    const msg = String(err?.message || err?.msg || '')
    if (!msg.includes('生产')) throw err
    await ElMessageBox.confirm(msg + '\n\n确认继续？', '生产环境确认', { type: 'warning' })
    const res = await operateHostService(svc.id, { action, confirm, confirmProd: true })
    ElMessage.success(`${actionLabels[action]}完成：${res.beforeState} → ${res.afterState || '未采到'}`)
    load()
    loadStats()
  }
}

function switchTab(name: string | number) {
  if (name === 'actions') loadActions()
}

onMounted(async () => {
  const [hostData, userData] = await Promise.all([
    listHosts({ page: 1, pageSize: 200 }),
    listUsers({ page: 1, pageSize: 100 })
  ])
  hosts.value = hostData.list || []
  users.value = userData.list || []
  load()
  loadStats()
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="主机服务"
      subtitle="登记「这台机器上哪些服务该在跑」，巡检拿真机状态对照；启停走下发闸门，动作前后各读一次状态"
    >
      <template #actions>
        <el-button v-perm="'service:manage'" @click="runCheck">巡检选中主机</el-button>
        <el-button v-perm="'service:manage'" type="primary" @click="openDiscover">发现并纳管服务</el-button>
      </template>
    </PageHeader>

    <el-tabs v-model="tab" @tab-change="switchTab">
      <el-tab-pane label="纳管服务" name="services">
        <el-card>
          <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

          <div class="page-toolbar" style="margin-top: 12px">
            <el-select v-model="query.hostId" placeholder="主机" clearable filterable style="width: 180px">
              <el-option v-for="hst in hosts" :key="hst.id" :label="`${hst.name} (${hst.address})`" :value="String(hst.id)" />
            </el-select>
            <el-input
              v-model="query.keyword"
              placeholder="unit / 名称 / 描述"
              style="width: 200px"
              clearable
              @keyup.enter="((query.page = 1), load())"
            />
            <el-select v-model="query.owner" placeholder="负责人" clearable filterable style="width: 140px">
              <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
            </el-select>
            <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
            <el-button
              @click="((query.hostId = ''), (query.keyword = ''), (query.owner = ''), (query.drift = ''), (query.critical = ''), (query.page = 1), load())"
            >
              重置
            </el-button>
          </div>

          <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有纳管任何服务，点右上角「发现并纳管服务」">
            <el-table-column label="服务" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">
                <el-tag v-if="row.critical" size="small" type="danger" style="margin-right: 4px">关键</el-tag>
                {{ row.unit }}
                <div v-if="row.description" style="color: #6b7280; font-size: 12px">{{ row.description }}</div>
              </template>
            </el-table-column>
            <el-table-column prop="hostName" label="主机" width="130" show-overflow-tooltip />
            <el-table-column label="期望" width="130">
              <template #default="{ row }">
                <span>{{ row.expectActive ? '在跑' : '停着' }}</span>
                <span style="color: #6b7280"> · {{ row.expectEnabled ? '自启' : '不自启' }}</span>
              </template>
            </el-table-column>
            <el-table-column label="实际" width="170">
              <template #default="{ row }">
                <el-tag v-if="row.activeState" size="small" :type="activeMeta[row.activeState] || 'info'">
                  {{ row.activeState }}/{{ row.subState }}
                </el-tag>
                <span v-else style="color: #9ca3af">未采到</span>
                <div v-if="row.enableState" style="color: #6b7280; font-size: 12px">{{ row.enableState }}</div>
              </template>
            </el-table-column>
            <el-table-column label="监听端口" width="130">
              <template #default="{ row }">
                <el-tag v-for="p in row.ports" :key="p" size="small" style="margin-right: 4px">{{ p }}</el-tag>
                <span v-if="!row.ports.length" style="color: #9ca3af">-</span>
              </template>
            </el-table-column>
            <el-table-column label="判定" min-width="180">
              <template #default="{ row }">
                <el-tag size="small" :type="driftMeta[row.drift]?.type || 'info'">
                  {{ row.driftLabel || row.drift }}
                </el-tag>
                <div v-if="row.driftDetail" style="color: #6b7280; font-size: 12px">{{ row.driftDetail }}</div>
                <div v-if="row.lastError" style="color: #dc2626; font-size: 12px">{{ row.lastError }}</div>
              </template>
            </el-table-column>
            <el-table-column prop="owner" label="负责人" width="100">
              <template #default="{ row }">
                <span v-if="row.owner">{{ row.owner }}</span>
                <span v-else style="color: #9ca3af">-</span>
              </template>
            </el-table-column>
            <el-table-column prop="lastCheckAt" label="最近巡检" min-width="170" />
            <el-table-column label="操作" width="230" fixed="right">
              <template #default="{ row }">
                <el-dropdown v-perm="'service:control'" style="margin-right: 8px">
                  <el-button link type="primary">启停<el-icon><arrow-down /></el-icon></el-button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item @click="operate(row, 'start')">启动</el-dropdown-item>
                      <el-dropdown-item @click="operate(row, 'restart')">重启</el-dropdown-item>
                      <el-dropdown-item @click="operate(row, 'reload')">重载配置</el-dropdown-item>
                      <el-dropdown-item divided @click="operate(row, 'stop')">停止</el-dropdown-item>
                      <el-dropdown-item @click="operate(row, 'enable')">设开机自启</el-dropdown-item>
                      <el-dropdown-item @click="operate(row, 'disable')">取消开机自启</el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
                <el-button v-perm="'service:manage'" link @click="openEdit(row)">期望态</el-button>
                <el-button v-perm="'service:manage'" link type="danger" @click="removeService(row)">取消纳管</el-button>
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

      <el-tab-pane label="启停留痕" name="actions">
        <el-card>
          <el-alert
            type="info"
            :closable="false"
            style="margin-bottom: 12px"
            title="每次启停都记「动之前是什么样、动之后是什么样」——光记「执行了 restart」回答不了「到底起来了没有」。被下发闸门拦下的尝试也在这里。"
          />
          <div class="page-toolbar">
            <el-select v-model="actionQuery.status" placeholder="结果" clearable style="width: 130px">
              <el-option label="成功" value="success" />
              <el-option label="失败" value="failed" />
              <el-option label="被拦下" value="blocked" />
            </el-select>
            <el-button type="primary" @click="((actionQuery.page = 1), loadActions())">查询</el-button>
          </div>
          <el-table :data="actions" border stripe empty-text="还没有启停记录">
            <el-table-column prop="unit" label="服务" min-width="180" show-overflow-tooltip />
            <el-table-column prop="hostName" label="主机" width="130" />
            <el-table-column label="动作" width="110">
              <template #default="{ row }">{{ actionLabels[row.action] || row.action }}</template>
            </el-table-column>
            <el-table-column label="结果" width="90">
              <template #default="{ row }">
                <el-tag
                  size="small"
                  :type="row.status === 'success' ? 'success' : row.status === 'blocked' ? 'warning' : 'danger'"
                >
                  {{ row.status === 'success' ? '成功' : row.status === 'blocked' ? '被拦下' : '失败' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="状态变化" min-width="200">
              <template #default="{ row }">
                <span style="color: #6b7280">{{ row.beforeState || '-' }}</span>
                <span> → </span>
                <span>{{ row.afterState || '未采到' }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="detail" label="输出" min-width="180" show-overflow-tooltip />
            <el-table-column prop="operator" label="操作人" width="100" />
            <el-table-column prop="createdAt" label="时间" min-width="170" />
          </el-table>
          <Pagination
            v-model:current-page="actionQuery.page"
            v-model:page-size="actionQuery.pageSize"
            :total="actionTotal"
            @change="loadActions"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <el-drawer v-model="discoverVisible" title="发现并纳管服务" size="66%">
      <div class="page-toolbar">
        <el-select v-model="discoverHost" placeholder="选择主机" filterable style="width: 220px">
          <el-option v-for="hst in hosts" :key="hst.id" :label="`${hst.name} (${hst.address})`" :value="hst.id" />
        </el-select>
        <el-button type="primary" :loading="discoverLoading" @click="runDiscover">读取服务清单</el-button>
        <el-input v-model="discoverKeyword" placeholder="过滤 unit / 描述" style="width: 200px" clearable />
      </div>
      <el-alert
        v-if="discoverNote"
        type="info"
        :closable="false"
        style="margin: 8px 0 12px"
        :title="discoverNote"
      />
      <el-alert
        v-if="portsUnavailable"
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="端口拿不到进程归属：给主机账号配免密 sudo（systemctl 与 ss）或改用 root 账号后才会归到服务上"
      />
      <el-alert v-if="orphanPorts.length" type="warning" :closable="false" style="margin-bottom: 12px">
        <div>
          无归属端口：
          <el-tag v-for="p in orphanPorts" :key="p" size="small" type="warning" style="margin-right: 4px">
            {{ p }}
          </el-tag>
        </div>
        <div style="font-size: 12px; margin-top: 4px">{{ orphanNote }}</div>
      </el-alert>

      <el-form v-if="discovered.length" inline style="margin-bottom: 8px">
        <el-form-item label="标为关键服务">
          <el-switch v-model="adoptForm.critical" />
        </el-form-item>
        <el-form-item label="期望开机自启">
          <el-switch v-model="adoptForm.expectEnabled" />
        </el-form-item>
        <el-form-item label="负责人">
          <el-select v-model="adoptForm.owner" clearable filterable style="width: 150px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :disabled="!picked.length" @click="submitAdopt">
            纳管选中的 {{ picked.length }} 个
          </el-button>
        </el-form-item>
      </el-form>

      <el-table
        v-loading="discoverLoading"
        :data="discoverRows"
        border
        size="small"
        height="calc(100vh - 320px)"
        empty-text="点「读取服务清单」实时读取目标机器"
      >
        <el-table-column width="46">
          <template #default="{ row }">
            <el-checkbox
              :model-value="picked.includes(row.unit)"
              :disabled="row.managed"
              @change="
                (v: any) =>
                  v ? picked.push(row.unit) : (picked = picked.filter((x) => x !== row.unit))
              "
            />
          </template>
        </el-table-column>
        <el-table-column prop="unit" label="unit" min-width="190" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.unit }}
            <el-tag v-if="row.managed" size="small" type="info">已纳管</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="activeMeta[row.activeState] || 'info'">
              {{ row.activeState }}/{{ row.subState }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="enableState" label="自启" width="110" />
        <el-table-column label="端口" width="140">
          <template #default="{ row }">
            <el-tag v-for="p in row.ports" :key="p" size="small" style="margin-right: 4px">{{ p }}</el-tag>
            <span v-if="!row.ports.length" style="color: #9ca3af">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="描述" min-width="200" show-overflow-tooltip />
      </el-table>
    </el-drawer>

    <el-dialog v-model="editVisible" :title="`期望态 · ${editing?.unit ?? ''}`" width="560px">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="期望态是「这台机器上这个服务本来该是什么样」。改完会立刻用上次采到的实际态重新判一次漂移，不会重新连机器。"
      />
      <el-form label-width="110px">
        <el-form-item label="显示名">
          <el-input v-model="editForm.name" style="width: 220px" />
        </el-form-item>
        <el-form-item label="期望在运行">
          <el-switch v-model="editForm.expectActive" />
          <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
            关掉表示「这个服务就该停着」，它在跑反而算漂移
          </span>
        </el-form-item>
        <el-form-item label="期望开机自启">
          <el-switch v-model="editForm.expectEnabled" />
          <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
            static / indirect 这类本来不能 enable 的 unit 不会被判漂移
          </span>
        </el-form-item>
        <el-form-item label="关键服务">
          <el-switch v-model="editForm.critical" />
          <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
            漂移时产 critical 告警；停止 / 重启 / 取消自启需要抄 unit 名确认
          </span>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="editForm.alertEnabled" />
        </el-form-item>
        <el-form-item label="负责人">
          <el-select v-model="editForm.owner" clearable filterable style="width: 180px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="editForm.remark" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" @click="submitEdit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
