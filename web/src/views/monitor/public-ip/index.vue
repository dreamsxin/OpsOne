<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import {
  createExposureTarget,
  deleteExposureTarget,
  listExposureScans,
  listExposureTargets,
  scanExposureTarget,
  updateExposureTarget,
  type ExposureScan,
  type ExposureTarget
} from '@/api'

const loading = ref(false)
const rows = ref<ExposureTarget[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  address: '',
  ports: '22,80,443,3306,6379,8080',
  baseline: '',
  timeoutMs: 800,
  alertEnabled: true,
  enabled: true,
  remark: ''
})
const rules: FormRules = {
  name: [{ required: true, message: '请填写名称', trigger: 'blur' }],
  address: [{ required: true, message: '请填写 IP 或域名', trigger: 'blur' }],
  ports: [{ required: true, message: '请填写要扫的端口', trigger: 'blur' }]
}

const scansVisible = ref(false)
const current = ref<ExposureTarget | null>(null)
const scans = ref<ExposureScan[]>([])

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  ok: { text: '与登记一致', type: 'success' },
  unexpected: { text: '有未登记端口', type: 'danger' },
  failed: { text: '扫描失败', type: 'warning' },
  unknown: { text: '未扫描', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    rows.value = await listExposureTargets()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    address: '',
    ports: '22,80,443,3306,6379,8080',
    baseline: '',
    timeoutMs: 800,
    alertEnabled: true,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: ExposureTarget) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    address: row.address,
    ports: row.ports,
    baseline: row.baseline,
    timeoutMs: row.timeoutMs,
    alertEnabled: row.alertEnabled,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  if (!formRef.value) return
  await formRef.value.validate()
  try {
    if (editingId.value) {
      await updateExposureTarget(editingId.value, { ...form })
      ElMessage.success('已保存')
    } else {
      const res = await createExposureTarget({ ...form })
      ElMessage.success(`已创建并扫描：${describeResult(res.result)}`)
    }
    dialogVisible.value = false
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

function describeResult(result: { status: string; open: string; unexpected: string; error: string }) {
  if (result.status === 'failed') return `扫描失败 · ${result.error}`
  if (result.status === 'unexpected') return `未登记端口 ${result.unexpected}`
  return `开放端口 ${result.open || '无'}，与登记一致`
}

async function scan(row: ExposureTarget) {
  try {
    const result = await scanExposureTarget(row.id)
    if (result.status === 'unexpected') {
      ElMessage.warning(describeResult(result))
    } else if (result.status === 'failed') {
      ElMessage.error(describeResult(result))
    } else {
      ElMessage.success(describeResult(result))
    }
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '扫描失败')
  }
}

// 「登记为基线」：把这次扫到的开放端口原样写进基线，用于首次纳管时对齐现状
async function acceptBaseline(row: ExposureTarget) {
  try {
    await ElMessageBox.confirm(
      `把当前开放端口「${row.lastOpen || '无'}」登记为 ${row.name} 的基线？之后只有新出现的端口会告警。`,
      '登记为基线',
      { type: 'warning', confirmButtonText: '登记', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  try {
    await updateExposureTarget(row.id, {
      name: row.name,
      address: row.address,
      ports: row.ports,
      baseline: row.lastOpen,
      timeoutMs: row.timeoutMs,
      alertEnabled: row.alertEnabled,
      enabled: row.enabled,
      remark: row.remark
    })
    ElMessage.success('已登记，建议再扫一次确认')
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '登记失败')
  }
}

async function toggleEnabled(row: ExposureTarget) {
  try {
    await updateExposureTarget(row.id, {
      name: row.name,
      address: row.address,
      ports: row.ports,
      baseline: row.baseline,
      timeoutMs: row.timeoutMs,
      alertEnabled: row.alertEnabled,
      enabled: row.enabled,
      remark: row.remark
    })
  } catch (err: any) {
    row.enabled = !row.enabled
    ElMessage.error(err?.message || '保存失败')
  }
}

async function remove(row: ExposureTarget) {
  try {
    await ElMessageBox.confirm(`删除目标「${row.name}」及其历史扫描记录？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await deleteExposureTarget(row.id)
  ElMessage.success('已删除')
  load()
}

async function openScans(row: ExposureTarget) {
  current.value = row
  scansVisible.value = true
  const res = await listExposureScans({ targetId: row.id, page: 1, pageSize: 50 })
  scans.value = res.list || []
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
        title="登记每台对外机器该开哪些端口，平台定时从自己所在网络位置做 TCP 连接核对：开着但没登记的端口会告警。只做连接探测，不发探针载荷、不识别服务指纹，也不做全端口扫描。"
      />
      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <span class="grow" />
        <el-button v-perm="'exposure:manage'" type="primary" @click="openCreate">新增目标</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="address" label="地址" min-width="160" />
        <el-table-column prop="ports" label="扫描范围" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
              {{ statusMeta[row.lastStatus]?.text || row.lastStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="开放端口" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">{{ row.lastOpen || '—' }}</template>
        </el-table-column>
        <el-table-column label="未登记" min-width="130" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.lastUnexpected" style="color: var(--el-color-danger)">
              {{ row.lastUnexpected }}
            </span>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="登记未开" min-width="120" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.lastMissing" style="color: var(--el-color-warning)">
              {{ row.lastMissing }}
            </span>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="基线" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">{{ row.baseline || '未登记（任何开放端口都提示）' }}</template>
        </el-table-column>
        <el-table-column prop="lastScanAt" label="最近扫描" min-width="170" />
        <el-table-column label="定时" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="250" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'exposure:manage'" link type="primary" @click="scan(row)">扫描</el-button>
            <el-button
              v-if="row.lastUnexpected"
              v-perm="'exposure:manage'"
              link
              type="primary"
              @click="acceptBaseline(row)"
            >
              登记基线
            </el-button>
            <el-button link type="primary" @click="openScans(row)">历史</el-button>
            <el-button v-perm="'exposure:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'exposure:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑目标' : '新增目标'"
      width="560px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="130px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="例如 网关-公网" />
        </el-form-item>
        <el-form-item label="地址" prop="address">
          <el-input v-model="form.address" placeholder="IP 或域名，不带端口" />
        </el-form-item>
        <el-form-item label="扫描范围" prop="ports">
          <el-input v-model="form.ports" placeholder="22,80,443,8000-8010" />
          <div class="hint">逗号分隔，支持区间；一次最多 1024 个端口</div>
        </el-form-item>
        <el-form-item label="基线端口">
          <el-input v-model="form.baseline" placeholder="留空表示任何开放端口都提示" />
          <div class="hint">登记在册、允许开放的端口。扫到的端口不在这里就算「未登记」</div>
        </el-form-item>
        <el-form-item label="单端口超时">
          <el-input-number v-model="form.timeoutMs" :min="100" :max="10000" :step="100" />
          <span class="hint" style="margin-left: 8px">毫秒，公网目标建议 1500 以上</span>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="form.alertEnabled" />
          <span class="hint" style="margin-left: 8px">发现未登记端口或扫描失败时写告警</span>
        </el-form-item>
        <el-form-item label="参与定时扫描">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="scansVisible" size="55%" :title="`扫描记录 · ${current?.name}`">
      <el-table :data="scans" border stripe size="small">
        <el-table-column prop="createdAt" label="时间" min-width="170" />
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="scanned" label="扫描端口数" width="110" />
        <el-table-column prop="openPorts" label="开放" min-width="150" show-overflow-tooltip />
        <el-table-column prop="unexpected" label="未登记" min-width="120" show-overflow-tooltip />
        <el-table-column prop="missing" label="登记未开" min-width="120" show-overflow-tooltip />
        <el-table-column prop="costMs" label="耗时(ms)" width="100" />
        <el-table-column prop="operator" label="触发方" width="110" />
        <el-table-column prop="errorMsg" label="错误" min-width="160" show-overflow-tooltip />
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.grow {
  flex: 1;
}
</style>
