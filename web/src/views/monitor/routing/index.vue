<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createNotifyRoute,
  deleteNotifyRoute,
  getRouteSnapshot,
  listNotifyChannels,
  listNotifyRoutes,
  listRouteSnapshots,
  rollbackRouteSnapshot,
  testNotifyRoute,
  updateNotifyRoute,
  type NotifyChannel,
  type NotifyRoute,
  type RouteSnapshot
} from '@/api'

const loading = ref(false)
const rows = ref<NotifyRoute[]>([])
const channels = ref<NotifyChannel[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  priority: 100,
  matchSeverity: [] as string[],
  matchLabels: '',
  channelIds: [] as number[],
  isDefault: false,
  enabled: true
})

const testForm = reactive({ severity: 'critical', labels: '{"env":"prod"}' })
const testResult = ref('')

const rules = {
  name: [{ required: true, message: '请输入路由名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listNotifyRoutes()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    priority: 100,
    matchSeverity: [],
    matchLabels: '',
    channelIds: [],
    isDefault: false,
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: NotifyRoute) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    priority: row.priority,
    matchSeverity: row.matchSeverity ? row.matchSeverity.split(',') : [],
    matchLabels: row.matchLabels,
    channelIds: row.channelIds || [],
    isDefault: row.isDefault,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

function buildPayload() {
  return {
    name: form.name,
    priority: form.priority,
    matchSeverity: form.matchSeverity.join(','),
    matchLabels: form.matchLabels,
    channelIds: form.channelIds,
    isDefault: form.isDefault,
    enabled: form.enabled
  }
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.channelIds.length) {
    ElMessage.warning('请至少选择一个通知渠道')
    return
  }

  if (editingId.value) {
    await updateNotifyRoute(editingId.value, buildPayload())
    ElMessage.success('已更新')
  } else {
    await createNotifyRoute(buildPayload())
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: NotifyRoute) {
  await updateNotifyRoute(row.id, {
    name: row.name,
    priority: row.priority,
    matchSeverity: row.matchSeverity,
    matchLabels: row.matchLabels,
    channelIds: row.channelIds,
    isDefault: row.isDefault,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
  load()
}

async function remove(row: NotifyRoute) {
  await ElMessageBox.confirm(`确认删除路由「${row.name}」？`, '提示', { type: 'warning' })
  await deleteNotifyRoute(row.id)
  ElMessage.success('已删除')
  load()
}

async function runTest() {
  let labels: Record<string, string> = {}
  if (testForm.labels.trim()) {
    try {
      labels = JSON.parse(testForm.labels)
    } catch {
      ElMessage.error('标签必须是合法 JSON 对象')
      return
    }
  }

  const res = await testNotifyRoute({ severity: testForm.severity, labels })
  if (!res.matched) {
    testResult.value = '未命中任何路由，也没有兜底路由，这类告警不会产生通知'
    return
  }
  const names = (res.channelIds || []).map((id) => channelName(id)).join('、')
  testResult.value = `${res.fallback ? '回落到兜底路由' : '命中路由'}「${res.routeName}」，投递渠道：${names}`
}

function channelName(id: number) {
  const channel = channels.value.find((c) => c.id === id)
  return channel ? channel.name : `#${id}`
}

// ---------- 路由版本快照 ----------
// 每次增删改（含回滚）后端都存一份整表快照；误删兜底路由、改乱优先级时一键还原。

const versionsVisible = ref(false)
const versions = ref<RouteSnapshot[]>([])
const versionsLoading = ref(false)
const versionDetailVisible = ref(false)
const versionDetailLoading = ref(false)
const versionDetail = ref<RouteSnapshot | null>(null)
const snapshotRoutes = ref<NotifyRoute[]>([])
const rollbackLoading = ref(false)

const opLabel: Record<string, string> = {
  create: '新增后',
  update: '修改后',
  delete: '删除后',
  rollback: '回滚后'
}

async function openVersions() {
  versionsVisible.value = true
  versionsLoading.value = true
  try {
    versions.value = await listRouteSnapshots()
  } finally {
    versionsLoading.value = false
  }
}

type DiffRow = {
  id: number
  name: string
  status: 'same' | 'changed' | 'only-snapshot' | 'only-current'
  snapshot?: NotifyRoute
  current?: NotifyRoute
}

function routeFingerprint(r: NotifyRoute) {
  return JSON.stringify([
    r.name, r.priority, r.matchSeverity, r.matchLabels, r.channelIds, r.isDefault, r.enabled
  ])
}

/** 快照 vs 当前路由表：逐条对齐（按 ID），多出来的标「仅快照 / 仅当前」 */
const diffRows = computed<DiffRow[]>(() => {
  const currentById = new Map(rows.value.map((r) => [r.id, r]))
  const snapById = new Map(snapshotRoutes.value.map((r) => [r.id, r]))
  const out: DiffRow[] = []
  for (const snap of snapshotRoutes.value) {
    const cur = currentById.get(snap.id)
    if (!cur) out.push({ id: snap.id, name: snap.name, status: 'only-snapshot', snapshot: snap })
    else if (routeFingerprint(snap) !== routeFingerprint(cur)) {
      out.push({ id: snap.id, name: snap.name, status: 'changed', snapshot: snap, current: cur })
    } else out.push({ id: snap.id, name: snap.name, status: 'same', snapshot: snap, current: cur })
  }
  for (const cur of rows.value) {
    if (!snapById.has(cur.id)) {
      out.push({ id: cur.id, name: cur.name, status: 'only-current', current: cur })
    }
  }
  return out
})

const diffCount = computed(() => diffRows.value.filter((r) => r.status !== 'same').length)

const diffStatusLabel: Record<DiffRow['status'], { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  same: { text: '一致', type: 'success' },
  changed: { text: '有差异', type: 'warning' },
  'only-snapshot': { text: '快照有 · 当前已删', type: 'danger' },
  'only-current': { text: '当前新增', type: 'info' }
}

async function openVersionDetail(snap: RouteSnapshot) {
  versionDetail.value = snap
  versionDetailVisible.value = true
  versionDetailLoading.value = true
  try {
    const res = await getRouteSnapshot(snap.id)
    snapshotRoutes.value = res.routes || []
  } finally {
    versionDetailLoading.value = false
  }
}

function showDiff(a?: NotifyRoute, b?: NotifyRoute): string {
  const fmt = (r?: NotifyRoute) =>
    r ? `优先级 ${r.priority} · 级别 ${r.matchSeverity || '不限'} · 标签 ${r.matchLabels || '不限'} · 渠道 ${(r.channelIds || []).map(channelName).join('、')}${r.isDefault ? ' · 兜底' : ''}${r.enabled ? '' : ' · 已停用'}` : '（无）'
  if (!a || !b) return fmt(a || b)
  if (routeFingerprint(a) === routeFingerprint(b)) return fmt(a)
  return `快照：${fmt(a)}\n当前：${fmt(b)}`
}

async function doRollback() {
  if (!versionDetail.value) return
  await ElMessageBox.confirm(
    `确认把整张路由表还原到「${opLabel[versionDetail.value.operation]} ${versionDetail.value.createdAt.slice(0, 19).replace('T', ' ')}」的状态？当前 ${rows.value.length} 条路由会被快照里的 ${versionDetail.value.routeCount} 条整表替换。回滚本身也会留一份快照，可以再回滚回来。`,
    '回滚路由表',
    { type: 'warning', confirmButtonText: '回滚', cancelButtonText: '取消' }
  )
  rollbackLoading.value = true
  try {
    const res = await rollbackRouteSnapshot(versionDetail.value.id)
    ElMessage.success(`已还原 ${res.restored} 条路由`)
    versionDetailVisible.value = false
    versionsVisible.value = false
    load()
  } finally {
    rollbackLoading.value = false
  }
}

onMounted(async () => {
  channels.value = await listNotifyChannels()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          按优先级升序取<strong>第一条命中</strong>的路由（级别为空表示不限，标签条件需全部命中）；
          都没命中时回落到兜底路由；没有兜底路由则该告警不产生通知。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="testForm.severity" style="width: 110px">
          <el-option label="严重" value="critical" />
          <el-option label="警告" value="warning" />
          <el-option label="提示" value="info" />
        </el-select>
        <el-input v-model="testForm.labels" placeholder='样例标签 JSON，如 {"env":"prod"}' style="width: 240px" />
        <el-button @click="runTest">选路预演</el-button>
        <span style="color: #6b7280">{{ testResult }}</span>
        <div class="grow"></div>
        <el-button @click="openVersions">历史版本</el-button>
        <el-button v-perm="'route:manage'" type="primary" @click="openCreate">新增路由</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="priority" label="优先级" width="90" />
        <el-table-column prop="name" label="路由" min-width="140">
          <template #default="{ row }">
            {{ row.name }}
            <el-tag v-if="row.isDefault" size="small" type="warning" style="margin-left: 4px">兜底</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="级别条件" min-width="140">
          <template #default="{ row }">{{ row.matchSeverity || '不限' }}</template>
        </el-table-column>
        <el-table-column label="标签条件" min-width="180">
          <template #default="{ row }">{{ row.matchLabels || '不限' }}</template>
        </el-table-column>
        <el-table-column label="渠道" min-width="180">
          <template #default="{ row }">
            <el-tag v-for="id in row.channelIds" :key="id" size="small" style="margin-right: 4px">
              {{ channelName(id) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'route:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'route:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑路由' : '新增路由'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="路由名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="优先级">
          <el-input-number v-model="form.priority" :min="1" :max="9999" />
          <span style="margin-left: 8px; color: #6b7280">数字越小越先匹配</span>
        </el-form-item>
        <el-form-item label="级别条件">
          <el-select v-model="form.matchSeverity" multiple placeholder="不限" style="width: 100%">
            <el-option label="严重" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="提示" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="标签条件">
          <el-input v-model="form.matchLabels" type="textarea" :rows="2" placeholder='JSON 对象，如 {"env":"prod","service":"nginx"}' />
        </el-form-item>
        <el-form-item label="通知渠道">
          <el-select v-model="form.channelIds" multiple style="width: 100%">
            <el-option
              v-for="channel in channels"
              :key="channel.id"
              :label="`${channel.name}（${channel.type}）`"
              :value="channel.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="作为兜底">
          <el-switch v-model="form.isDefault" />
          <span style="margin-left: 8px; color: #6b7280">兜底路由只允许一条，设置后会自动取消其他</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="versionsVisible" title="路由历史版本" size="52%">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="每次增删改（含回滚）都会存一份整表快照，保留最近 20 份。误删兜底路由、改乱优先级时选一个版本整表还原；回滚本身也留快照，可以再回滚回来。"
      />
      <el-table v-loading="versionsLoading" :data="versions" border stripe size="small" empty-text="还没有版本：第一次改动路由后这里就有了">
        <el-table-column prop="id" label="版本" width="70">
          <template #default="{ row }">#{{ row.id }}</template>
        </el-table-column>
        <el-table-column label="动作" width="90">
          <template #default="{ row }">{{ opLabel[row.operation] || row.operation }}</template>
        </el-table-column>
        <el-table-column prop="routeCount" label="路由数" width="80" />
        <el-table-column prop="username" label="操作人" width="110" />
        <el-table-column prop="createdAt" label="时间" min-width="170" />
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openVersionDetail(row)">查看 / 回滚</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <el-drawer
      v-model="versionDetailVisible"
      :title="`版本 #${versionDetail?.id ?? ''} · ${opLabel[versionDetail?.operation || ''] || ''}`"
      size="62%"
    >
      <div class="diff-bar">
        <span>
          与当前路由表对比：<strong :style="{ color: diffCount ? 'var(--el-color-warning)' : 'var(--el-color-success)' }">
            {{ diffCount ? `${diffCount} 处差异` : '完全一致' }}
          </strong>
        </span>
        <el-button
          v-perm="'route:manage'"
          type="warning"
          size="small"
          :loading="rollbackLoading"
          :disabled="!diffCount"
          @click="doRollback"
        >
          回滚到此版本
        </el-button>
      </div>
      <el-table v-loading="versionDetailLoading" :data="diffRows" border stripe size="small">
        <el-table-column prop="id" label="ID" width="60" />
        <el-table-column prop="name" label="路由" min-width="120" />
        <el-table-column label="状态" width="140">
          <template #default="{ row }">
            <el-tag size="small" :type="diffStatusLabel[row.status as DiffRow['status']].type">
              {{ diffStatusLabel[row.status as DiffRow['status']].text }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="差异内容" min-width="320">
          <template #default="{ row }">
            <pre class="diff-cell">{{ showDiff(row.snapshot, row.current) }}</pre>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.diff-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.diff-cell {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  line-height: 1.6;
  color: var(--ops-text-secondary, #6b7280);
}
</style>
