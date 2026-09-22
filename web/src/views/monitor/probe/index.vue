<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createProbe,
  deleteProbe,
  listEgressProxies,
  listProbeRecords,
  listProbes,
  runProbe,
  updateProbe,
  type EgressProxy,
  type Probe,
  type ProbeRecord
} from '@/api'

const loading = ref(false)
const rows = ref<Probe[]>([])
const proxies = ref<EgressProxy[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  type: 'http' as Probe['type'],
  target: '',
  method: 'GET',
  expectStatus: 200,
  expectKeyword: '',
  timeoutSec: 10,
  consecutiveFails: 1,
  proxyId: 0,
  alertEnabled: true,
  enabled: true,
  remark: ''
})
const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  target: [{ required: true, message: '请输入拨测目标', trigger: 'blur' }]
}

const recordsVisible = ref(false)
const current = ref<Probe | null>(null)
const records = ref<ProbeRecord[]>([])

const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  up: { text: '正常', type: 'success' },
  down: { text: '失败', type: 'danger' },
  unknown: { text: '未拨测', type: 'info' }
}

function availability(row: Probe) {
  if (!row.totalChecks) return '-'
  const rate = ((row.totalChecks - row.failChecks) / row.totalChecks) * 100
  return `${rate.toFixed(1)}%`
}

async function load() {
  loading.value = true
  try {
    rows.value = (await listProbes()) || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    type: 'http',
    target: '',
    method: 'GET',
    expectStatus: 200,
    expectKeyword: '',
    timeoutSec: 10,
    consecutiveFails: 1,
    proxyId: 0,
    alertEnabled: true,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: Probe) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    type: row.type,
    target: row.target,
    method: row.method || 'GET',
    expectStatus: row.expectStatus,
    expectKeyword: row.expectKeyword,
    timeoutSec: row.timeoutSec,
    consecutiveFails: row.consecutiveFails,
    proxyId: row.proxyId || 0,
    alertEnabled: row.alertEnabled,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

function onTypeChange() {
  // TCP 没有状态码与关键字的概念，切过去时清掉，避免保存了用不上的条件
  if (form.type === 'tcp') {
    form.expectStatus = 0
    form.expectKeyword = ''
    // HTTP 代理只能代理 HTTP(S)，TCP 拨测走代理测到的是代理的放行策略
    form.proxyId = 0
  } else if (form.expectStatus === 0) {
    form.expectStatus = 200
  }
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateProbe(editingId.value, { ...form })
    ElMessage.success('已更新，连续失败计数已重置')
  } else {
    const created = await createProbe({ ...form })
    const meta = statusMeta[created.lastStatus]
    ElMessage.success(`已创建并拨测一次：${meta?.text || created.lastStatus}`)
  }
  dialogVisible.value = false
  load()
}

async function run(row: Probe) {
  const res = await runProbe(row.id)
  if (res.status === 'up') {
    ElMessage.success(`拨测正常，耗时 ${res.costMs}ms${res.code ? `，状态码 ${res.code}` : ''}`)
  } else {
    ElMessage.error(`拨测失败（连续 ${res.failStreak}/${res.needStreak} 次）：${res.errorMsg}`)
  }
  load()
}

async function toggleEnabled(row: Probe) {
  await updateProbe(row.id, {
    name: row.name,
    type: row.type,
    target: row.target,
    method: row.method,
    expectStatus: row.expectStatus,
    expectKeyword: row.expectKeyword,
    timeoutSec: row.timeoutSec,
    consecutiveFails: row.consecutiveFails,
    proxyId: row.proxyId || 0,
    alertEnabled: row.alertEnabled,
    enabled: row.enabled,
    remark: row.remark
  })
  ElMessage.success(row.enabled ? '已启用定时拨测' : '已停用定时拨测')
  load()
}

async function remove(row: Probe) {
  await ElMessageBox.confirm(`确认删除拨测「${row.name}」？历史记录会一并删除`, '提示', {
    type: 'warning'
  })
  await deleteProbe(row.id)
  ElMessage.success('已删除')
  load()
}

async function openRecords(row: Probe) {
  current.value = row
  const data = await listProbeRecords({ probeId: row.id, page: 1, pageSize: 50 })
  records.value = data.list || []
  recordsVisible.value = true
}

onMounted(async () => {
  proxies.value = (await listEgressProxies()).list
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="从平台所在网络位置去访问目标：HTTP 校验状态码与响应体关键字，TCP 只看端口能否连通。所有启用的拨测共用一个节奏（OPS_PROBE_SPEC，默认每 5 分钟），不支持每条单独周期；连续失败达到设定次数才告警，恢复后自动关闭。历史记录保留 7 天。"
      />

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'probe:manage'" type="primary" @click="openCreate">新建拨测</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有拨测目标">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag size="small">{{ row.type.toUpperCase() }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="target" label="目标" min-width="200" show-overflow-tooltip />
        <el-table-column label="判定" min-width="150">
          <template #default="{ row }">
            <span v-if="row.type === 'tcp'">端口可连通</span>
            <span v-else>
              状态码 {{ row.expectStatus || '非 4xx/5xx' }}
              <span v-if="row.expectKeyword">＋含「{{ row.expectKeyword }}」</span>
            </span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
              {{ statusMeta[row.lastStatus]?.text || row.lastStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="响应" width="110">
          <template #default="{ row }">
            <span v-if="row.lastCheckAt">
              {{ row.lastCostMs }}ms
              <span v-if="row.lastCode" style="color: #6b7280">/{{ row.lastCode }}</span>
            </span>
            <span v-else style="color: #6b7280">-</span>
          </template>
        </el-table-column>
        <el-table-column label="可用率" width="100">
          <template #default="{ row }">
            {{ availability(row) }}
            <span v-if="row.totalChecks" style="color: #6b7280">({{ row.totalChecks }})</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastError" label="最近错误" min-width="180" show-overflow-tooltip />
        <el-table-column prop="lastCheckAt" label="最近拨测" min-width="180" />
        <el-table-column label="定时" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="210" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'probe:manage'" link type="primary" @click="run(row)">立即拨测</el-button>
            <el-button link type="primary" @click="openRecords(row)">记录</el-button>
            <el-button v-perm="'probe:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'probe:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑拨测' : '新建拨测'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type" @change="onTypeChange">
            <el-radio label="http">HTTP</el-radio>
            <el-radio label="tcp">TCP</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="目标" prop="target">
          <el-input
            v-model="form.target"
            :placeholder="form.type === 'http' ? 'http://127.0.0.1:8080/healthz' : '127.0.0.1:3306'"
          />
        </el-form-item>
        <template v-if="form.type === 'http'">
          <el-form-item label="请求方法">
            <el-select v-model="form.method" style="width: 120px">
              <el-option label="GET" value="GET" />
              <el-option label="HEAD" value="HEAD" />
              <el-option label="POST" value="POST" />
            </el-select>
          </el-form-item>
          <el-form-item label="期望状态码">
            <el-input-number v-model="form.expectStatus" :min="0" :max="599" />
            <span style="margin-left: 8px; color: #6b7280">0 表示只要不是 4xx/5xx 就算通</span>
          </el-form-item>
          <el-form-item label="响应体关键字">
            <el-input v-model="form.expectKeyword" placeholder="留空表示不校验内容" />
          </el-form-item>
          <el-form-item label="出口代理">
            <el-select v-model="form.proxyId" style="width: 100%">
              <el-option :value="0" label="直连（不走代理）" />
              <el-option
                v-for="p in proxies.filter((x) => x.enabled)"
                :key="p.id"
                :value="p.id"
                :label="`${p.name}（${p.endpoint}）`"
              />
            </el-select>
            <span style="color: #6b7280">
              在「配置中心 → 代理检测」登记。代理被停用或删除时这条拨测会直接失败并点名原因，不会静默改成直连
            </span>
          </el-form-item>
        </template>
        <el-form-item label="超时">
          <el-input-number v-model="form.timeoutSec" :min="1" :max="60" />
          <span style="margin-left: 8px; color: #6b7280">秒</span>
        </el-form-item>
        <el-form-item label="连续失败">
          <el-input-number v-model="form.consecutiveFails" :min="1" :max="10" />
          <span style="margin-left: 8px; color: #6b7280">次才告警</span>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="form.alertEnabled" />
        </el-form-item>
        <el-form-item label="参与定时拨测">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="recordsVisible" :title="`拨测记录 · ${current?.name ?? ''}`" size="50%">
      <el-table :data="records" border stripe size="small" empty-text="暂无记录">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="code" label="状态码" width="90" />
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ row.costMs }}ms</template>
        </el-table-column>
        <el-table-column prop="operator" label="触发方" width="110" />
        <el-table-column prop="errorMsg" label="错误" min-width="180" show-overflow-tooltip />
        <el-table-column prop="createdAt" label="时间" min-width="180" />
      </el-table>
    </el-drawer>
  </div>
</template>
