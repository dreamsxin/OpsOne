<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  checkEgressProxy,
  deleteEgressProxy,
  getEgressPolicy,
  listEgressProxies,
  saveEgressPolicy,
  saveEgressProxy,
  type EgressPolicy,
  type EgressProxy,
  type ProxyCheckResult
} from '@/api'

const loading = ref(false)
const rows = ref<EgressProxy[]>([])
const schemes = ref<{ code: string; label: string }[]>([])
const globalTestUrl = ref('')
const notes = ref<string[]>([])

const policy = ref<EgressPolicy | null>(null)
const savingEgress = ref(false)
const egressForm = reactive({ proxyId: 0, bypass: '' })

async function loadPolicy() {
  policy.value = await getEgressPolicy()
  egressForm.proxyId = policy.value.proxyId || 0
  egressForm.bypass = (policy.value.bypass || []).join(',')
}

async function saveEgress() {
  savingEgress.value = true
  try {
    const result = await saveEgressPolicy({
      proxyId: egressForm.proxyId,
      bypass: egressForm.bypass
    })
    ElMessage.success(result.detail)
    await loadPolicy()
  } finally {
    savingEgress.value = false
  }
}


const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  scheme: 'http',
  host: '',
  port: 3128,
  username: '',
  password: '',
  testUrl: '',
  enabled: true,
  remark: ''
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  host: [{ required: true, message: '请输入地址', trigger: 'blur' }]
}

const checkVisible = ref(false)
const checkRow = ref<EgressProxy | null>(null)
const checkUrl = ref('')
const checking = ref(false)
const checkResult = ref<ProxyCheckResult | null>(null)

const statusMeta: Record<string, { label: string; type: 'success' | 'danger' | 'info' }> = {
  ok: { label: '可用', type: 'success' },
  fail: { label: '不可用', type: 'danger' },
  unknown: { label: '未检测', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    const data = await listEgressProxies()
    rows.value = data.list
    schemes.value = data.schemes
    globalTestUrl.value = data.testUrl
    notes.value = data.notes
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    scheme: 'http',
    host: '',
    port: 3128,
    username: '',
    password: '',
    testUrl: '',
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: EgressProxy) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    scheme: row.scheme,
    host: row.host,
    port: row.port,
    username: row.username,
    password: '',
    testUrl: row.testUrl,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  await saveEgressProxy(editingId.value || 0, { ...form })
  ElMessage.success(editingId.value ? '已更新' : '已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: EgressProxy) {
  await ElMessageBox.confirm(`删除代理「${row.name}」？`, '确认删除', { type: 'warning' })
  await deleteEgressProxy(row.id)
  ElMessage.success('已删除')
  load()
}

function openCheck(row: EgressProxy) {
  checkRow.value = row
  checkUrl.value = row.testUrl || globalTestUrl.value
  checkResult.value = null
  checkVisible.value = true
}

async function doCheck() {
  if (!checkRow.value) return
  checking.value = true
  try {
    checkResult.value = await checkEgressProxy(checkRow.value.id, checkUrl.value)
    load()
  } finally {
    checking.value = false
  }
}

onMounted(async () => {
  await load()
  await loadPolicy()
})

</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          检测会<strong>直连一次、走代理一次</strong>，把两个结果摆在一起 ——
          只测代理的话，「网络本来就不通」和「代理坏了」给出的是同一个报错，分不开。
          <br />
          下面的<strong>统一出口</strong>决定哪条代理真的成为平台的出网口；HTTP 拨测另外
          <strong>按条</strong>指定代理，`proxyId=0` 就是「这条要直连」，统一出口不覆盖它。
          <br />
          指定的代理被停用或删除时，走它的请求<strong>直接失败并点名原因</strong>，
          不会静默改成直连：静默直连会把「代理挂了」显示成「目标一切正常」。
          <br />
          出口 IP 只能从<strong>会回显来源 IP</strong> 的测试地址读到；读不到就照实说读不到。
          平台不预设测试地址 —— 默认填一个公网地址等于替你决定了这台机器可以访问公网。
        </template>
      </el-alert>

      <el-card shadow="never" style="margin-bottom: 12px">
        <template #header>
          <strong>统一出口</strong>
          <el-tag v-if="policy?.configured" type="success" size="small" style="margin-left: 8px">
            {{ policy.proxyName }}（{{ policy.endpoint }}）
          </el-tag>
          <el-tag v-else type="info" size="small" style="margin-left: 8px">未统一出口</el-tag>
          <span style="color: #909399; margin-left: 8px; font-size: 13px">
            —— 决定云 API / IM / Webhook / 大模型 / Jenkins 的出网走哪里
          </span>
        </template>

        <el-alert v-if="policy?.problem" type="error" :closable="false" style="margin-bottom: 10px">
          <template #title>{{ policy.problem }}</template>
        </el-alert>

        <div class="page-toolbar" style="flex-wrap: wrap; gap: 8px">
          <el-select v-model="egressForm.proxyId" style="width: 260px" placeholder="选择出口代理">
            <el-option :value="0" label="不统一出口（各自遵守环境变量）" />
            <el-option
              v-for="row in rows"
              :key="row.id"
              :value="row.id"
              :label="`${row.name}（${row.endpoint}）`"
              :disabled="!row.enabled || row.lastStatus !== 'ok'"
            />
          </el-select>
          <el-input
            v-model="egressForm.bypass"
            type="textarea"
            :rows="2"
            style="width: 420px"
            placeholder="不走代理的目标，逗号分隔"
          />
          <el-button v-perm="'proxy:manage'" type="primary" :loading="savingEgress" @click="saveEgress">
            保存出口设置
          </el-button>
        </div>

        <div style="color: #909399; font-size: 13px; margin-bottom: 8px">
          bypass 支持：精确主机名、<code>.example.com</code>（域名后缀）、<code>10.0.0.0/8</code>（网段）。
          留空会恢复默认清单。
        </div>

        <el-alert
          v-if="policy?.bypassBad?.length"
          type="warning"
          :closable="false"
          style="margin-bottom: 10px"
        >
          <template #title>
            这些 bypass 条目解析不了，等于没写：{{ policy.bypassBad.join('、') }}
          </template>
        </el-alert>

        <el-descriptions :column="3" border size="small" style="margin-bottom: 10px">
          <el-descriptions-item
            v-for="(value, key) in policy?.env || {}"
            :key="key"
            :label="String(key)"
          >
            <span v-if="value">{{ value }}</span>
            <span v-else style="color: #909399">未设置</span>
          </el-descriptions-item>
        </el-descriptions>

        <el-row :gutter="12">
          <el-col :span="12">
            <div style="font-weight: 600; margin-bottom: 6px">会走统一出口</div>
            <el-table :data="policy?.covered || []" border stripe size="small">
              <el-table-column prop="module" label="模块" width="170" />
              <el-table-column prop="target" label="目标" min-width="180" show-overflow-tooltip />
              <el-table-column prop="note" label="说明" min-width="200" show-overflow-tooltip />
            </el-table>
          </el-col>
          <el-col :span="12">
            <div style="font-weight: 600; margin-bottom: 6px">不走，以及为什么</div>
            <el-table :data="policy?.notCovered || []" border stripe size="small">
              <el-table-column prop="module" label="模块" width="170" />
              <el-table-column prop="target" label="目标" min-width="180" show-overflow-tooltip />
              <el-table-column prop="note" label="原因" min-width="200" show-overflow-tooltip />
            </el-table>
          </el-col>
        </el-row>

        <ul style="margin-top: 10px; color: #909399; font-size: 13px; line-height: 1.8">
          <li v-for="(note, idx) in policy?.notes || []" :key="idx">{{ note }}</li>
        </ul>
      </el-card>


      <div class="page-toolbar">
        <el-tag v-if="globalTestUrl" type="info">默认测试地址：{{ globalTestUrl }}</el-tag>
        <el-tag v-else type="warning">未设默认测试地址（系统配置 proxy.test_url）</el-tag>
        <div class="grow"></div>
        <el-button v-perm="'proxy:manage'" type="primary" @click="openCreate">新增代理</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记出口代理">
        <el-table-column prop="name" label="名称" min-width="130" show-overflow-tooltip />
        <el-table-column prop="endpoint" label="地址" min-width="200" show-overflow-tooltip />
        <el-table-column label="认证" width="110">
          <template #default="{ row }">
            <el-tag v-if="!row.username" size="small" type="info">无</el-tag>
            <el-tag v-else size="small" :type="row.storage === 'encrypted' ? 'success' : 'warning'">
              {{ row.username }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近检测" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
              {{ statusMeta[row.lastStatus]?.label || row.lastStatus }}
            </el-tag>
            <span v-if="row.lastCheckAt" style="color: #909399; margin-left: 4px">
              {{ row.lastCheckAt.slice(5, 16).replace('T', ' ') }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">
            {{ row.lastStatus === 'unknown' ? '—' : row.lastCostMs + 'ms' }}
          </template>
        </el-table-column>
        <el-table-column label="出口 IP" width="130">
          <template #default="{ row }">
            <span v-if="row.exitIp">{{ row.exitIp }}</span>
            <span v-else style="color: #909399">未读到</span>
          </template>
        </el-table-column>
        <el-table-column label="最近错误" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ row.lastError || '—' }}</template>
        </el-table-column>
        <el-table-column label="拨测引用" width="100">
          <template #default="{ row }">{{ row.probeCount }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'proxy:manage'" link type="primary" @click="openCheck(row)">检测</el-button>
            <el-button v-perm="'proxy:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'proxy:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <ul style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
        <li v-for="(note, idx) in notes" :key="idx">{{ note }}</li>
      </ul>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑代理' : '新增代理'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="110px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如：机房出口代理" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.scheme">
            <el-radio-button v-for="s in schemes" :key="s.code" :value="s.code">
              {{ s.label }}
            </el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="地址" prop="host">
          <el-input v-model="form.host" placeholder="10.0.0.9 或 proxy.corp.local" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="认证账号">
          <el-input v-model="form.username" placeholder="留空表示不认证" />
        </el-form-item>
        <el-form-item label="口令">
          <el-input v-model="form.password" type="password" show-password
            :placeholder="editingId ? '留空表示不修改' : ''" />
        </el-form-item>
        <el-form-item label="测试地址">
          <el-input v-model="form.testUrl" placeholder="留空则用系统配置里的 proxy.test_url" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            填一个会把来源 IP 写在响应里的地址，才能看出走代理之后出口变没变
          </el-text>
        </el-form-item>
        <el-form-item label="启用">
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

    <el-dialog v-model="checkVisible" title="代理检测" width="620px">
      <el-form label-width="90px">
        <el-form-item label="代理">
          <span>{{ checkRow?.name }}（{{ checkRow?.endpoint }}）</span>
        </el-form-item>
        <el-form-item label="测试地址">
          <el-input v-model="checkUrl" placeholder="http://..." @keyup.enter="doCheck" />
        </el-form-item>
      </el-form>

      <div v-if="checkResult">
        <el-alert
          :type="checkResult.status === 'ok' ? 'success' : 'warning'"
          :closable="false"
          :title="checkResult.verdict"
          style="margin-bottom: 10px"
        />
        <el-descriptions :column="2" border>
          <el-descriptions-item label="直连">
            <el-tag size="small" :type="checkResult.direct.ok ? 'success' : 'danger'">
              {{ checkResult.direct.ok ? '通' : '不通' }}
            </el-tag>
            <span style="margin-left: 6px">{{ checkResult.direct.costMs }}ms</span>
          </el-descriptions-item>
          <el-descriptions-item label="走代理">
            <el-tag size="small" :type="checkResult.viaProxy.ok ? 'success' : 'danger'">
              {{ checkResult.viaProxy.ok ? '通' : '不通' }}
            </el-tag>
            <span style="margin-left: 6px">{{ checkResult.viaProxy.costMs }}ms</span>
          </el-descriptions-item>
          <el-descriptions-item label="直连出口 IP">
            {{ checkResult.direct.exitIp || '未读到' }}
          </el-descriptions-item>
          <el-descriptions-item label="代理出口 IP">
            {{ checkResult.viaProxy.exitIp || '未读到' }}
          </el-descriptions-item>
          <el-descriptions-item v-if="checkResult.direct.error" label="直连错误" :span="2">
            {{ checkResult.direct.error }}
          </el-descriptions-item>
          <el-descriptions-item v-if="checkResult.viaProxy.error" label="代理错误" :span="2">
            {{ checkResult.viaProxy.error }}
          </el-descriptions-item>
        </el-descriptions>
        <el-text v-if="checkResult.exitIPNote" type="info" size="small" style="display: block; margin-top: 8px">
          {{ checkResult.exitIPNote }}
        </el-text>
      </div>

      <template #footer>
        <el-button @click="checkVisible = false">关闭</el-button>
        <el-button type="primary" :loading="checking" @click="doCheck">
          {{ checkResult ? '再测一次' : '开始检测' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>
