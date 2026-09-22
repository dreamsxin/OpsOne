<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  deleteMailAccount,
  getMailState,
  importGlobalSMTP,
  listMailAccounts,
  saveMailAccount,
  setDefaultMailAccount,
  testMailAccount,
  type MailAccount,
  type MailState
} from '@/api'

const loading = ref(false)
const rows = ref<MailAccount[]>([])
const state = ref<MailState | null>(null)

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  host: '',
  port: 465,
  username: '',
  password: '',
  from: '',
  fromName: '',
  tlsMode: 'ssl',
  skipVerify: false,
  isDefault: false,
  enabled: true,
  remark: ''
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  host: [{ required: true, message: '请输入 SMTP 服务器', trigger: 'blur' }]
}

const testVisible = ref(false)
const testRow = ref<MailAccount | null>(null)
const testTo = ref('')
const testing = ref(false)
const testResult = ref<{ ok: boolean; detail: string; costMs: number } | null>(null)

const tlsLabel = computed(() => {
  const map = new Map<string, string>()
  for (const m of state.value?.tlsModes || []) map.set(m.code, m.label)
  return map
})

// 端口与加密方式的常见组合。选了端口就把加密方式带过去，少一次踩坑
function onPortChange(port: number) {
  if (port === 465) form.tlsMode = 'ssl'
  else if (port === 587 || port === 25) form.tlsMode = 'starttls'
}

async function load() {
  loading.value = true
  try {
    rows.value = await listMailAccounts()
    state.value = await getMailState()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    host: '',
    port: 465,
    username: '',
    password: '',
    from: '',
    fromName: '',
    tlsMode: 'ssl',
    skipVerify: false,
    isDefault: false,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: MailAccount) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    host: row.host,
    port: row.port,
    username: row.username,
    password: '',
    from: row.from,
    fromName: row.fromName,
    tlsMode: row.tlsMode,
    skipVerify: row.skipVerify,
    isDefault: row.isDefault,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  await saveMailAccount(editingId.value || 0, { ...form })
  ElMessage.success(editingId.value ? '已更新' : '已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: MailAccount) {
  await ElMessageBox.confirm(`删除发件邮箱「${row.name}」？`, '确认删除', { type: 'warning' })
  await deleteMailAccount(row.id)
  ElMessage.success('已删除')
  load()
}

async function makeDefault(row: MailAccount) {
  await setDefaultMailAccount(row.id)
  ElMessage.success(`「${row.name}」已设为默认发件邮箱`)
  load()
}

function openTest(row: MailAccount) {
  testRow.value = row
  testTo.value = ''
  testResult.value = null
  testVisible.value = true
}

async function doTest() {
  if (!testRow.value || !testTo.value) {
    ElMessage.warning('请填写收件地址')
    return
  }
  testing.value = true
  try {
    testResult.value = await testMailAccount(testRow.value.id, testTo.value)
    load()
  } finally {
    testing.value = false
  }
}

async function doImport() {
  await ElMessageBox.confirm(
    '把系统配置里的那套 SMTP（含口令）复制成一个发件邮箱条目？原配置保留不动，仍作为没有任何发件邮箱时的兜底。',
    '从系统配置导入',
    { type: 'info' }
  )
  await importGlobalSMTP()
  ElMessage.success('已导入')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          在这一页之前，全平台只有<strong>一套</strong> SMTP（系统配置里的 smtp.* 六个键），
          所有邮件都从同一个地址发出。现在可以登记多个：告警从 alert@ 发、值班呼叫从 oncall@ 发。
          <br />
          解析顺序是<strong>渠道指定 → 默认发件邮箱 → 系统配置里的全局 SMTP</strong>。
          一个都没登记时仍走全局配置，所以升级后邮件照发；而渠道指定的邮箱被删或停用时
          <strong>那个渠道的邮件直接失败并点名原因</strong>，不会静默换一个发件人。
          <br />
          <strong>STARTTLS 现在是显式选项</strong>。老实现只有一个 TLS 布尔开关，关掉时交给标准库 ——
          对方不声明支持 STARTTLS 就会<strong>静默发明文</strong>，而配置上看不出来。
          现在选了 STARTTLS 而服务器不支持就直接报错。
          <br />
          顺手修的两处：中文主题按 RFC 2047 编码（之前部分客户端显示乱码）、
          发件人可以有显示名（之前收件箱里只看到一串邮箱地址）。
        </template>
      </el-alert>

      <div v-if="state" class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-tag v-if="state.activeSender" type="success">
          当前发信用：{{ state.activeSender }}（{{ state.activeFrom }}）
        </el-tag>
        <el-tag v-else type="danger">当前无法发信：{{ state.activeError }}</el-tag>
        <el-tag type="info">已登记 {{ state.total }}</el-tag>
        <el-tag type="info">启用 {{ state.enabled }}</el-tag>
        <el-tag :type="state.globalConfigured ? 'info' : 'warning'">
          全局 SMTP：{{ state.globalConfigured ? state.globalHost : '未配置' }}
        </el-tag>
      </div>

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'mail:manage'" :disabled="!state?.globalConfigured" @click="doImport">
          从系统配置导入
        </el-button>
        <el-button v-perm="'mail:manage'" type="primary" @click="openCreate">新增发件邮箱</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记发件邮箱（当前走系统配置里的全局 SMTP）">
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.name }}
            <el-tag v-if="row.isDefault" size="small" type="success" style="margin-left: 6px">默认</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="服务器" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">{{ row.host }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column label="加密" width="140">
          <template #default="{ row }">
            <el-tag size="small" :type="row.tlsMode === 'plain' ? 'danger' : 'success'">
              {{ tlsLabel.get(row.tlsMode) || row.tlsMode }}
            </el-tag>
            <el-tag v-if="row.skipVerify" size="small" type="warning" style="margin-left: 4px">
              跳过校验
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="发件人" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.fromName">{{ row.fromName }} &lt;{{ row.effectiveFrom }}&gt;</span>
            <span v-else>{{ row.effectiveFrom || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="口令" width="90">
          <template #default="{ row }">
            <el-tag v-if="!row.hasPassword" size="small" type="info">不认证</el-tag>
            <el-tag v-else size="small" :type="row.storage === 'encrypted' ? 'success' : 'warning'">
              {{ row.storage === 'encrypted' ? '密文' : '明文' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近试发" width="190">
          <template #default="{ row }">
            <span v-if="row.lastTestOk === null" style="color: #909399">从未试发</span>
            <span v-else>
              <el-tag size="small" :type="row.lastTestOk ? 'success' : 'danger'">
                {{ row.lastTestOk ? '成功' : '失败' }}
              </el-tag>
              <span style="color: #909399; margin-left: 4px">
                {{ row.lastTestAt?.slice(5, 16).replace('T', ' ') }}
              </span>
              <el-tooltip v-if="row.lastTestErr" :content="row.lastTestErr" placement="top">
                <el-text type="danger" size="small" style="cursor: help">原因</el-text>
              </el-tooltip>
            </span>
          </template>
        </el-table-column>
        <el-table-column label="引用渠道" width="100">
          <template #default="{ row }">{{ row.channelCount }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'mail:manage'" link type="primary" @click="openTest(row)">试发</el-button>
            <el-button
              v-if="!row.isDefault"
              v-perm="'mail:manage'"
              link
              type="warning"
              @click="makeDefault(row)"
            >
              设为默认
            </el-button>
            <el-button v-perm="'mail:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'mail:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <ul v-if="state" style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
        <li v-for="(note, idx) in state.notes" :key="idx">{{ note }}</li>
      </ul>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑发件邮箱' : '新增发件邮箱'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="110px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如：告警专用邮箱" />
        </el-form-item>
        <el-form-item label="SMTP 服务器" prop="host">
          <el-input v-model="form.host" placeholder="smtp.corp.local" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" @change="onPortChange" />
          <el-text type="info" size="small" style="margin-left: 8px">465 / 587 / 25</el-text>
        </el-form-item>
        <el-form-item label="加密方式">
          <el-radio-group v-model="form.tlsMode">
            <el-radio-button
              v-for="m in state?.tlsModes || []"
              :key="m.code"
              :value="m.code"
            >
              {{ m.label }}
            </el-radio-button>
          </el-radio-group>
          <el-text
            v-for="m in (state?.tlsModes || []).filter((x) => x.code === form.tlsMode)"
            :key="m.code"
            :type="m.code === 'plain' ? 'danger' : 'info'"
            size="small"
            style="display: block; margin-top: 4px"
          >
            {{ m.note }}
          </el-text>
        </el-form-item>
        <el-form-item label="跳过证书校验">
          <el-switch v-model="form.skipVerify" />
          <el-text v-if="form.skipVerify" type="warning" size="small" style="display: block; margin-top: 4px">
            这是一次真实的降级：中间人可以冒充这台 SMTP。只在内网自签证书的中继上用
          </el-text>
        </el-form-item>
        <el-form-item label="认证账号">
          <el-input v-model="form.username" placeholder="留空表示不认证（内网中继常见）" />
        </el-form-item>
        <el-form-item label="口令">
          <el-input v-model="form.password" type="password" show-password
            :placeholder="editingId ? '留空表示不修改' : ''" />
        </el-form-item>
        <el-form-item label="发件地址">
          <el-input v-model="form.from" placeholder="留空则用认证账号" />
        </el-form-item>
        <el-form-item label="发件显示名">
          <el-input v-model="form.fromName" placeholder="如：OpsOne 告警" />
        </el-form-item>
        <el-form-item label="设为默认">
          <el-switch v-model="form.isDefault" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            渠道没指定邮箱时用它。默认邮箱全局只有一个，设了新的旧的自动取消
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

    <el-dialog v-model="testVisible" title="试发一封" width="480px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          会用这套配置<strong>真的发一封信</strong>。主题里带中文，能正常显示说明编码生效
        </template>
      </el-alert>
      <el-form label-width="80px">
        <el-form-item label="发件邮箱">
          <span>{{ testRow?.name }}（{{ testRow?.host }}:{{ testRow?.port }}）</span>
        </el-form-item>
        <el-form-item label="收件地址">
          <el-input v-model="testTo" placeholder="you@corp.local" @keyup.enter="doTest" />
        </el-form-item>
      </el-form>
      <el-alert
        v-if="testResult"
        :type="testResult.ok ? 'success' : 'error'"
        :closable="false"
        :title="`${testResult.ok ? '已投递到 SMTP 服务器' : '失败'}（${testResult.costMs}ms）`"
        :description="testResult.detail"
      />
      <template #footer>
        <el-button @click="testVisible = false">关闭</el-button>
        <el-button type="primary" :loading="testing" @click="doTest">发送</el-button>
      </template>
    </el-dialog>
  </div>
</template>
