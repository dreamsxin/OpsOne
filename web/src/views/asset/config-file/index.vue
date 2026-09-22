<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  applyConfigFile,
  captureConfigFile,
  checkConfigFiles,
  createConfigFile,
  deleteConfigFile,
  diffConfigFile,
  editConfigVersion,
  getConfigFile,
  getConfigStats,
  getConfigVersion,
  listConfigApplies,
  listConfigFiles,
  listHosts,
  listUsers,
  rollbackConfigFile,
  updateConfigFile,
  type ConfigApply,
  type ConfigDiff,
  type ConfigFile,
  type ConfigStats,
  type ConfigVersion,
  type Host,
  type User
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

const loading = ref(false)
const rows = ref<ConfigFile[]>([])
const total = ref(0)
const stats = ref<ConfigStats | null>(null)
const hosts = ref<Host[]>([])
const users = ref<User[]>([])
const query = reactive({ page: 1, pageSize: 20, hostId: '', drift: '', critical: '', keyword: '' })

const tab = ref<'files' | 'applies'>('files')
const applies = ref<ConfigApply[]>([])
const applyTotal = ref(0)
const applyQuery = reactive({ page: 1, pageSize: 20, action: '', status: '' })

const driftMeta: Record<string, { text: string; type: 'success' | 'danger' | 'warning' | 'info' }> = {
  ok: { text: '一致', type: 'success' },
  drift: { text: '内容不一致', type: 'danger' },
  missing: { text: '文件不存在', type: 'danger' },
  'no-desired': { text: '还没定基线', type: 'warning' },
  'too-large': { text: '超出可管大小', type: 'warning' },
  binary: { text: '不是文本文件', type: 'warning' },
  error: { text: '读取失败', type: 'warning' },
  unknown: { text: '未巡检', type: 'info' }
}

const sourceLabels: Record<string, string> = {
  captured: '真机抓取',
  edited: '平台编辑',
  'pre-apply': '下发前快照',
  applied: '下发后回读'
}

async function load() {
  loading.value = true
  try {
    const data = await listConfigFiles(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  stats.value = await getConfigStats()
}

async function loadApplies() {
  const data = await listConfigApplies(applyQuery)
  applies.value = data.list || []
  applyTotal.value = data.total
}

const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `登记 ${s?.total ?? 0} 个 · ${s?.hosts ?? 0} 台主机 · ${s?.versions ?? 0} 个版本`, static: true },
    {
      key: 'drift:problem',
      label: '有问题',
      count: (s?.drift ?? 0) + (s?.missing ?? 0) + (s?.error ?? 0),
      hint: '与基线不一致或读不到',
      tone: 'danger'
    },
    { key: 'drift:drift', label: '被改过', count: s?.drift ?? 0, hint: '真机内容与基线不一致', tone: 'danger' },
    { key: 'drift:missing', label: '文件不存在', count: s?.missing ?? 0, tone: 'danger' },
    { key: 'drift:no-desired', label: '还没定基线', count: s?.noDesired ?? 0, hint: '不判漂移也不能下发', tone: 'warning' },
    { key: 'drift:ok', label: '一致', count: s?.ok ?? 0, tone: 'success' },
    {
      key: 'critical',
      label: `关键配置 ${s?.critical ?? 0}`,
      hint: (s?.criticalDrift ?? 0) > 0 ? `其中 ${s?.criticalDrift} 个正在漂移` : '全部一致',
      tone: (s?.criticalDrift ?? 0) > 0 ? 'danger' : undefined
    },
    {
      key: 'noReload',
      label: `没登记 reload ${s?.noReload ?? 0}`,
      hint: '改完可能不生效，要人自己处理',
      static: true,
      tone: (s?.noReload ?? 0) > 0 ? 'warning' : undefined
    }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.drift) return `drift:${query.drift}`
  if (query.critical === '1') return 'critical'
  return null
})

function onChipSelect(key: string) {
  if (key === 'info' || key === 'noReload') return
  const wasActive = activeChipKey.value === key
  query.drift = ''
  query.critical = ''
  if (!wasActive && key === 'critical') query.critical = '1'
  else if (!wasActive && key.startsWith('drift:')) query.drift = key.split(':')[1]
  query.page = 1
  load()
}

// ---------- 登记 ----------
const createVisible = ref(false)
const createForm = reactive({
  hostId: '' as number | '',
  path: '',
  name: '',
  category: '',
  owner: '',
  critical: false,
  reloadUnit: '',
  reloadAction: 'reload',
  remark: '',
  capture: true
})

function openCreate() {
  Object.assign(createForm, {
    hostId: query.hostId ? Number(query.hostId) : hosts.value[0]?.id || '',
    path: '',
    name: '',
    category: '',
    owner: '',
    critical: false,
    reloadUnit: '',
    reloadAction: 'reload',
    remark: '',
    capture: true
  })
  createVisible.value = true
}

async function submitCreate() {
  if (!createForm.hostId) {
    ElMessage.warning('请选择主机')
    return
  }
  if (!createForm.path.trim()) {
    ElMessage.warning('请填写文件的绝对路径')
    return
  }
  const res = await createConfigFile({ ...createForm, hostId: Number(createForm.hostId) })
  if (res.baselineVersion) {
    ElMessage.success(`已登记，并抓取真机内容作为基线 v${res.baselineVersion}`)
  } else {
    ElMessage.warning(res.note || '已登记')
  }
  createVisible.value = false
  load()
  loadStats()
}

// ---------- 详情 ----------
const detailVisible = ref(false)
const detail = ref<{ file: ConfigFile; versions: ConfigVersion[]; applies: ConfigApply[] } | null>(null)
const detailPane = ref('diff')
const diffResult = ref<ConfigDiff | null>(null)
const diffLoading = ref(false)
const viewingVersion = ref<ConfigVersion | null>(null)

async function openDetail(file: ConfigFile) {
  detail.value = await getConfigFile(file.id)
  diffResult.value = null
  viewingVersion.value = null
  detailPane.value = 'diff'
  detailVisible.value = true
  if (file.drift !== 'no-desired') loadDiff()
}

async function refreshDetail() {
  if (!detail.value) return
  detail.value = await getConfigFile(detail.value.file.id)
  load()
  loadStats()
}

async function loadDiff() {
  if (!detail.value) return
  diffLoading.value = true
  diffResult.value = null
  try {
    diffResult.value = await diffConfigFile(detail.value.file.id)
  } catch (err: any) {
    ElMessage.warning(String(err?.msg || err?.message || '取 diff 失败'))
  } finally {
    diffLoading.value = false
  }
}

async function showVersion(v: ConfigVersion) {
  viewingVersion.value = await getConfigVersion(v.id)
  detailPane.value = 'content'
}

async function capture(setDesired: boolean) {
  if (!detail.value) return
  if (setDesired) {
    await ElMessageBox.confirm(
      '会把真机当前内容存成新版本并设为基线。之后「一致」的含义就是「和现在的真机一样」。',
      '抓取并设为基线',
      { type: 'warning' }
    )
  }
  const res = await captureConfigFile(detail.value.file.id, setDesired)
  ElMessage.success(`已抓取为 v${res.version}` + (setDesired ? '，并设为基线' : '（没有改基线）'))
  await refreshDetail()
  loadDiff()
}

// ---------- 编辑 ----------
const editVisible = ref(false)
const editForm = reactive({ content: '', note: '', setDesired: true })

async function openEdit() {
  if (!detail.value) return
  const file = detail.value.file
  // 默认拿当前基线内容打底；没基线就拿真机现状
  let base = ''
  if (file.desiredVersionId) {
    const v = await getConfigVersion(file.desiredVersionId)
    base = v.content || ''
  } else if (diffResult.value) {
    base = ''
  }
  Object.assign(editForm, { content: base, note: '', setDesired: true })
  editVisible.value = true
}

async function submitEdit() {
  if (!detail.value) return
  const res = await editConfigVersion(detail.value.file.id, { ...editForm })
  ElMessage.success(`已存为 v${res.version}。${res.note}`)
  editVisible.value = false
  await refreshDetail()
  loadDiff()
}

// ---------- 下发与回滚 ----------
async function doApply(versionId?: number, isRollback = false) {
  if (!detail.value) return
  const file = detail.value.file
  const label = isRollback ? '回滚' : '下发'

  let confirm = ''
  if (file.critical) {
    const input = await ElMessageBox.prompt(
      `「${file.name}」是关键配置。要${label}请把路径抄一遍确认：${file.path}`,
      `关键配置${label}确认`,
      { inputPlaceholder: file.path, inputValidator: (v) => (v === file.path ? true : '路径不一致') }
    ).catch(() => null)
    if (!input) return
    confirm = input.value
  } else {
    const reloadHint = file.reloadUnit
      ? `下发后会执行 ${file.reloadAction} ${file.reloadUnit} 让它生效。`
      : '这个文件没有登记 reload 服务，改完可能要你自己让它生效。'
    await ElMessageBox.confirm(
      `${label}前会先把真机现状存成回滚点，并在机器上留一份 .bak 备份；替换用临时文件 + mv 原子完成，写完回读校验 hash。\n${reloadHint}`,
      `确认${label}`,
      { type: 'warning' }
    )
  }

  const payload: Record<string, any> = { versionId, confirm, confirmProd: false }
  const run = isRollback ? rollbackConfigFile : applyConfigFile
  try {
    const res = await run(file.id, payload)
    reportApply(res, label)
  } catch (err: any) {
    const msg = String(err?.msg || err?.message || '')
    if (!msg.includes('生产')) throw err
    await ElMessageBox.confirm(msg + '\n\n确认继续？', '生产环境确认', { type: 'warning' })
    const res = await run(file.id, { ...payload, confirmProd: true })
    reportApply(res, label)
  }
  await refreshDetail()
  loadDiff()
}

function reportApply(res: Record<string, any>, label: string) {
  if (res.changed === false) {
    ElMessage.info(res.note || '真机内容已经和目标一致，没有下发任何东西')
    return
  }
  ElMessage.success(`${label}成功：v${res.version}，备份 ${res.backupPath}`)
  if (res.reloadStatus === 'failed') {
    ElMessageBox.alert(
      `配置已经落到机器上，但让它生效的那一步失败了：\n${res.reloadDetail}\n\n请手动处理，否则改动还没有生效。`,
      'reload 失败',
      { type: 'error' }
    )
  } else if (res.reloadStatus === 'success') {
    ElMessage.success(res.reloadDetail)
  } else if (res.reloadDetail) {
    ElMessage.warning(res.reloadDetail)
  }
}

// ---------- 维护与巡检 ----------
const metaVisible = ref(false)
const metaForm = reactive({
  name: '',
  category: '',
  owner: '',
  critical: false,
  alertEnabled: true,
  reloadUnit: '',
  reloadAction: 'reload',
  remark: ''
})

function openMeta(file: ConfigFile) {
  detail.value = detail.value && detail.value.file.id === file.id ? detail.value : null
  Object.assign(metaForm, {
    name: file.name,
    category: file.category,
    owner: file.owner,
    critical: file.critical,
    alertEnabled: file.alertEnabled,
    reloadUnit: file.reloadUnit,
    reloadAction: file.reloadAction || 'reload',
    remark: file.remark
  })
  metaTargetId.value = file.id
  metaVisible.value = true
}
const metaTargetId = ref(0)

async function submitMeta() {
  await updateConfigFile(metaTargetId.value, { ...metaForm })
  ElMessage.success('已保存')
  metaVisible.value = false
  load()
  loadStats()
  if (detail.value?.file.id === metaTargetId.value) refreshDetail()
}

async function removeFile(file: ConfigFile) {
  await ElMessageBox.confirm(
    `取消登记「${file.path}@${file.hostName}」？机器上的文件不会被动，但历史版本与下发留痕会一起删除，之后无法回滚。`,
    '提示',
    { type: 'warning' }
  )
  try {
    await deleteConfigFile(file.id)
  } catch (err: any) {
    const msg = String(err?.msg || err?.message || '')
    if (!msg.includes('force')) throw err
    await ElMessageBox.confirm(msg, '确认强制删除', { type: 'error' })
    await deleteConfigFile(file.id, true)
  }
  ElMessage.success('已取消登记')
  detailVisible.value = false
  load()
  loadStats()
}

async function runCheck() {
  const hostId = query.hostId ? Number(query.hostId) : undefined
  const res = await checkConfigFiles(hostId)
  if (res.total === 0) {
    ElMessage.info(res.note || '没有匹配的登记文件')
  } else if (res.drifted > 0) {
    ElMessage.warning(`巡检完成：${res.total} 个文件，其中 ${res.drifted} 个与基线不一致`)
  } else {
    ElMessage.success(`巡检完成：${res.total} 个文件全部一致`)
  }
  load()
  loadStats()
}

function switchTab(name: string | number) {
  if (name === 'applies') loadApplies()
}

function sizeText(n: number) {
  if (!n) return '-'
  if (n < 1024) return `${n} B`
  return `${(n / 1024).toFixed(1)} KB`
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
      title="配置文件"
      subtitle="基线从真机抓，不让人从零写；下发前先存回滚点、机器上留备份，替换用原子 mv，写完回读校验 hash"
    >
      <template #actions>
        <el-button v-perm="'configfile:manage'" @click="runCheck">巡检</el-button>
        <el-button v-perm="'configfile:manage'" type="primary" @click="openCreate">登记配置文件</el-button>
      </template>
    </PageHeader>

    <el-tabs v-model="tab" @tab-change="switchTab">
      <el-tab-pane label="登记的文件" name="files">
        <el-card>
          <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

          <div class="page-toolbar" style="margin-top: 12px">
            <el-select v-model="query.hostId" placeholder="主机" clearable filterable style="width: 180px">
              <el-option
                v-for="hst in hosts"
                :key="hst.id"
                :label="`${hst.name} (${hst.address})`"
                :value="String(hst.id)"
              />
            </el-select>
            <el-input
              v-model="query.keyword"
              placeholder="路径 / 名称"
              style="width: 200px"
              clearable
              @keyup.enter="((query.page = 1), load())"
            />
            <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
            <el-button
              @click="((query.hostId = ''), (query.keyword = ''), (query.drift = ''), (query.critical = ''), (query.page = 1), load())"
            >
              重置
            </el-button>
          </div>

          <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记配置文件">
            <el-table-column label="文件" min-width="230" show-overflow-tooltip>
              <template #default="{ row }">
                <el-tag v-if="row.critical" size="small" type="danger" style="margin-right: 4px">关键</el-tag>
                {{ row.path }}
                <div v-if="row.name && row.name !== row.path" style="color: #6b7280; font-size: 12px">
                  {{ row.name }}
                </div>
              </template>
            </el-table-column>
            <el-table-column prop="hostName" label="主机" width="130" show-overflow-tooltip />
            <el-table-column label="基线" width="110">
              <template #default="{ row }">
                <span v-if="row.desiredVersion">v{{ row.desiredVersion }}</span>
                <el-tag v-else size="small" type="warning">未定</el-tag>
                <div style="color: #6b7280; font-size: 12px">共 {{ row.versionSeq }} 版</div>
              </template>
            </el-table-column>
            <el-table-column label="判定" min-width="200">
              <template #default="{ row }">
                <el-tag size="small" :type="driftMeta[row.drift]?.type || 'info'">
                  {{ row.driftLabel || row.drift }}
                </el-tag>
                <span v-if="row.diffLines > 0" style="margin-left: 4px; color: #dc2626">
                  差 {{ row.diffLines }} 行
                </span>
                <div v-if="row.driftDetail" style="color: #6b7280; font-size: 12px">{{ row.driftDetail }}</div>
                <div v-if="row.lastError" style="color: #dc2626; font-size: 12px">{{ row.lastError }}</div>
              </template>
            </el-table-column>
            <el-table-column label="生效方式" width="150">
              <template #default="{ row }">
                <span v-if="row.reloadUnit">{{ row.reloadAction }} {{ row.reloadUnit }}</span>
                <el-tag v-else size="small" type="warning">未登记</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="大小 / 权限" width="120">
              <template #default="{ row }">
                {{ sizeText(row.actualSize) }}
                <div v-if="row.actualMode" style="color: #6b7280; font-size: 12px">{{ row.actualMode }}</div>
              </template>
            </el-table-column>
            <el-table-column prop="lastCheckAt" label="最近巡检" min-width="170" />
            <el-table-column label="操作" width="160" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDetail(row)">详情 / diff</el-button>
                <el-button v-perm="'configfile:manage'" link @click="openMeta(row)">设置</el-button>
                <el-button v-perm="'configfile:manage'" link type="danger" @click="removeFile(row)">删除</el-button>
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

      <el-tab-pane label="下发留痕" name="applies">
        <el-card>
          <el-alert
            type="info"
            :closable="false"
            style="margin-bottom: 12px"
            title="每次抓取 / 下发 / 回滚都留一条，含回滚点版本、远端备份路径、回读校验 hash 与 reload 结果。被闸门拦下的尝试也在这里。"
          />
          <div class="page-toolbar">
            <el-select v-model="applyQuery.action" placeholder="动作" clearable style="width: 130px">
              <el-option label="抓基线" value="capture" />
              <el-option label="下发" value="apply" />
              <el-option label="回滚" value="rollback" />
            </el-select>
            <el-select v-model="applyQuery.status" placeholder="结果" clearable style="width: 130px">
              <el-option label="成功" value="success" />
              <el-option label="失败" value="failed" />
              <el-option label="被拦下" value="blocked" />
            </el-select>
            <el-button type="primary" @click="((applyQuery.page = 1), loadApplies())">查询</el-button>
          </div>
          <el-table :data="applies" border stripe empty-text="还没有记录">
            <el-table-column prop="path" label="文件" min-width="200" show-overflow-tooltip />
            <el-table-column prop="hostName" label="主机" width="130" />
            <el-table-column label="动作" width="90">
              <template #default="{ row }">
                {{ row.action === 'capture' ? '抓基线' : row.action === 'apply' ? '下发' : '回滚' }}
              </template>
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
            <el-table-column label="生效" width="100">
              <template #default="{ row }">
                <el-tag
                  v-if="row.reloadStatus && row.reloadStatus !== 'skipped'"
                  size="small"
                  :type="row.reloadStatus === 'success' ? 'success' : 'danger'"
                >
                  {{ row.reloadStatus === 'success' ? 'reload 成功' : 'reload 失败' }}
                </el-tag>
                <span v-else style="color: #9ca3af">-</span>
              </template>
            </el-table-column>
            <el-table-column prop="backupPath" label="远端备份" min-width="200" show-overflow-tooltip />
            <el-table-column prop="detail" label="说明" min-width="220" show-overflow-tooltip />
            <el-table-column prop="operator" label="操作人" width="100" />
            <el-table-column prop="createdAt" label="时间" min-width="170" />
          </el-table>
          <Pagination
            v-model:current-page="applyQuery.page"
            v-model:page-size="applyQuery.pageSize"
            :total="applyTotal"
            @change="loadApplies"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <el-drawer
      v-model="detailVisible"
      :title="`配置 · ${detail?.file.path ?? ''} @ ${detail?.file.hostName ?? ''}`"
      size="74%"
    >
      <template v-if="detail">
        <div style="display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-bottom: 12px">
          <el-tag :type="driftMeta[detail.file.drift]?.type || 'info'">
            {{ detail.file.driftLabel }}
          </el-tag>
          <span v-if="detail.file.desiredVersion" style="color: #6b7280">
            基线 v{{ detail.file.desiredVersion }} · 共 {{ detail.file.versionSeq }} 版
          </span>
          <div style="flex: 1"></div>
          <el-button v-perm="'configfile:manage'" @click="loadDiff">重新比对真机</el-button>
          <el-button v-perm="'configfile:manage'" @click="capture(false)">抓一份存档</el-button>
          <el-button v-perm="'configfile:manage'" @click="capture(true)">抓取并设为基线</el-button>
          <el-button v-perm="'configfile:manage'" type="primary" @click="openEdit">编辑新版本</el-button>
          <el-button
            v-perm="'configfile:apply'"
            type="warning"
            :disabled="!detail.file.desiredVersionId"
            @click="doApply(undefined, false)"
          >
            下发基线
          </el-button>
        </div>

        <el-alert
          v-if="!detail.file.desiredVersionId"
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
          title="还没定基线。点「抓取并设为基线」把真机当前内容存成 v1 —— 让人从零写基线，第一次下发就会把机器打坏。"
        />
        <el-alert
          v-if="!detail.file.reloadUnit"
          type="info"
          :closable="false"
          style="margin-bottom: 12px"
          title="没有登记 reload 服务：下发之后平台不会帮你让它生效。点右上「设置」登记要 reload 的 systemd unit。"
        />

        <el-tabs v-model="detailPane">
          <el-tab-pane label="基线 vs 真机" name="diff">
            <div v-loading="diffLoading">
              <template v-if="diffResult">
                <div style="margin-bottom: 8px">
                  <el-tag v-if="diffResult.same" type="success">内容一致</el-tag>
                  <el-tag v-else type="danger">差 {{ diffResult.diffLines }} 行</el-tag>
                  <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
                    {{ diffResult.left.label }}（{{ diffResult.left.hash.slice(0, 12) }}）
                    vs {{ diffResult.right.label }}（{{ diffResult.right.hash.slice(0, 12) }}）
                    <span v-if="diffResult.note"> · {{ diffResult.note }}</span>
                  </span>
                </div>
                <pre class="diff-box">{{ diffResult.diff }}</pre>
                <el-button
                  v-if="!diffResult.same"
                  v-perm="'configfile:apply'"
                  type="warning"
                  style="margin-top: 8px"
                  @click="doApply(undefined, false)"
                >
                  把基线推回机器（覆盖真机改动）
                </el-button>
                <el-button
                  v-if="!diffResult.same"
                  v-perm="'configfile:manage'"
                  style="margin-top: 8px"
                  @click="capture(true)"
                >
                  接受真机改动（设为新基线）
                </el-button>
              </template>
              <el-empty v-else description="点上方「重新比对真机」现读一次" :image-size="70" />
            </div>
          </el-tab-pane>

          <el-tab-pane :label="`版本 (${detail.versions.length})`" name="versions">
            <el-alert
              type="info"
              :closable="false"
              style="margin-bottom: 12px"
              title="版本只追加不修改，它是回滚的唯一依据。下发前的真机现状会自动存成一版（pre-apply），那就是回滚点。"
            />
            <el-table :data="detail.versions" border size="small">
              <el-table-column label="版本" width="80">
                <template #default="{ row }">
                  v{{ row.version }}
                  <el-tag v-if="row.id === detail.file.desiredVersionId" size="small" type="success">基线</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="来源" width="110">
                <template #default="{ row }">{{ sourceLabels[row.source] || row.source }}</template>
              </el-table-column>
              <el-table-column label="大小" width="90">
                <template #default="{ row }">{{ sizeText(row.size) }}</template>
              </el-table-column>
              <el-table-column label="hash" width="120">
                <template #default="{ row }">{{ row.hash.slice(0, 12) }}</template>
              </el-table-column>
              <el-table-column prop="mode" label="权限" width="80" />
              <el-table-column prop="note" label="说明" min-width="180" show-overflow-tooltip />
              <el-table-column prop="operator" label="操作人" width="100" />
              <el-table-column prop="createdAt" label="时间" min-width="170" />
              <el-table-column label="操作" width="150" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="showVersion(row)">看内容</el-button>
                  <el-button v-perm="'configfile:apply'" link type="warning" @click="doApply(row.id, true)">
                    回滚到这版
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="内容" name="content">
            <template v-if="viewingVersion">
              <div style="margin-bottom: 8px; color: #6b7280; font-size: 12px">
                v{{ viewingVersion.version }} · {{ sourceLabels[viewingVersion.source] || viewingVersion.source }}
                · {{ sizeText(viewingVersion.size) }} · {{ viewingVersion.hash.slice(0, 12) }}
                · {{ viewingVersion.operator }} {{ viewingVersion.createdAt }}
              </div>
              <pre class="diff-box">{{ viewingVersion.content }}</pre>
            </template>
            <el-empty v-else description="在「版本」页签点「看内容」" :image-size="70" />
          </el-tab-pane>

          <el-tab-pane :label="`下发留痕 (${detail.applies.length})`" name="applies">
            <el-table :data="detail.applies" border size="small">
              <el-table-column label="动作" width="90">
                <template #default="{ row }">
                  {{ row.action === 'capture' ? '抓基线' : row.action === 'apply' ? '下发' : '回滚' }}
                </template>
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
              <el-table-column prop="backupPath" label="远端备份" min-width="200" show-overflow-tooltip />
              <el-table-column prop="detail" label="说明" min-width="220" show-overflow-tooltip />
              <el-table-column prop="reloadDetail" label="生效" min-width="160" show-overflow-tooltip />
              <el-table-column prop="operator" label="操作人" width="100" />
              <el-table-column prop="createdAt" label="时间" min-width="170" />
            </el-table>
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-drawer>

    <el-dialog v-model="createVisible" title="登记配置文件" width="620px">
      <el-form label-width="110px">
        <el-form-item label="主机" required>
          <el-select v-model="createForm.hostId" filterable style="width: 260px">
            <el-option v-for="hst in hosts" :key="hst.id" :label="`${hst.name} (${hst.address})`" :value="hst.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="绝对路径" required>
          <el-input v-model="createForm.path" placeholder="/etc/nginx/nginx.conf" />
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="createForm.name" placeholder="留空取文件名" style="width: 200px" />
          <el-input v-model="createForm.category" placeholder="分类" style="width: 140px; margin-left: 10px" />
        </el-form-item>
        <el-form-item label="改完怎么生效">
          <el-select v-model="createForm.reloadAction" style="width: 110px">
            <el-option label="reload" value="reload" />
            <el-option label="restart" value="restart" />
          </el-select>
          <el-input
            v-model="createForm.reloadUnit"
            placeholder="systemd unit，如 nginx"
            style="width: 220px; margin-left: 10px"
          />
        </el-form-item>
        <el-form-item label="负责人">
          <el-select v-model="createForm.owner" clearable filterable style="width: 180px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
          <el-switch v-model="createForm.critical" active-text="关键配置" style="margin-left: 16px" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="createForm.remark" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item label="立刻抓基线">
          <el-switch v-model="createForm.capture" />
          <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
            从真机读一份当 v1。关掉就没有基线，不判漂移也不能下发
          </span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" @click="submitCreate">登记</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editVisible" title="编辑新版本（不下发）" width="820px" top="5vh">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="只会存成一个新版本，不会碰机器。存完先看 diff，确认无误再点「下发基线」。"
      />
      <el-form label-width="90px">
        <el-form-item label="内容">
          <el-input v-model="editForm.content" type="textarea" :rows="18" style="font-family: monospace" />
        </el-form-item>
        <el-form-item label="改动说明">
          <el-input v-model="editForm.note" placeholder="改了什么、为什么" />
        </el-form-item>
        <el-form-item label="设为基线">
          <el-switch v-model="editForm.setDesired" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" @click="submitEdit">存为新版本</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="metaVisible" title="配置文件设置" width="560px">
      <el-form label-width="110px">
        <el-form-item label="显示名">
          <el-input v-model="metaForm.name" style="width: 200px" />
          <el-input v-model="metaForm.category" placeholder="分类" style="width: 140px; margin-left: 10px" />
        </el-form-item>
        <el-form-item label="改完怎么生效">
          <el-select v-model="metaForm.reloadAction" style="width: 110px">
            <el-option label="reload" value="reload" />
            <el-option label="restart" value="restart" />
          </el-select>
          <el-input v-model="metaForm.reloadUnit" placeholder="systemd unit" style="width: 200px; margin-left: 10px" />
        </el-form-item>
        <el-form-item label="负责人">
          <el-select v-model="metaForm.owner" clearable filterable style="width: 180px">
            <el-option v-for="u in users" :key="u.id" :label="u.username" :value="u.username" />
          </el-select>
        </el-form-item>
        <el-form-item label="关键配置">
          <el-switch v-model="metaForm.critical" />
          <span style="margin-left: 10px; color: #6b7280; font-size: 12px">
            漂移产 critical 告警；下发/回滚要抄路径确认
          </span>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="metaForm.alertEnabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="metaForm.remark" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="metaVisible = false">取消</el-button>
        <el-button type="primary" @click="submitMeta">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.diff-box {
  margin: 0;
  padding: 10px;
  background: #f8fafc;
  border: 1px solid #e5e7eb;
  border-radius: 4px;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.6;
  max-height: 46vh;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
