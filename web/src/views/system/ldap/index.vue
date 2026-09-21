<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  bindLdapAccount,
  checkLdapServer,
  createLdapServer,
  deleteLdapServer,
  listLdapAccounts,
  listLdapServers,
  listUsers,
  searchLdapUsers,
  tryLdapLogin,
  unbindLdapAccount,
  updateLdapServer,
  type LdapAccount,
  type LdapDirectoryUser,
  type LdapServer,
  type LdapTryStep,
  type User
} from '@/api'

const loading = ref(false)
const servers = ref<LdapServer[]>([])
const accounts = ref<LdapAccount[]>([])
const accountTotal = ref(0)
const accountQuery = reactive({ page: 1, pageSize: 10, keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  host: '',
  port: 389,
  encryption: 'none' as 'none' | 'ldaps' | 'starttls',
  skipVerify: false,
  bindDn: '',
  bindPassword: '',
  baseDn: '',
  userFilter: '(&(objectClass=inetOrgPerson)(uid=%s))',
  attrNickname: 'displayName',
  attrEmail: 'mail',
  timeoutSec: 8,
  enabled: true,
  loginEnabled: true,
  autoBind: false
})

const rules = {
  name: [{ required: true, message: '请填写名称', trigger: 'blur' }],
  host: [{ required: true, message: '请填写服务器地址', trigger: 'blur' }],
  baseDn: [{ required: true, message: '请填写 Base DN', trigger: 'blur' }]
}

// 目录搜索 + 绑定
const bindVisible = ref(false)
const bindServer = ref<LdapServer | null>(null)
const dirKeyword = ref('')
const dirUsers = ref<LdapDirectoryUser[]>([])
const dirLoading = ref(false)
const platformUsers = ref<User[]>([])
const bindForm = reactive({ userId: null as number | null, entry: null as LdapDirectoryUser | null })

// 试登录诊断
const tryVisible = ref(false)
const tryForm = reactive({ serverId: null as number | null, username: '', password: '' })
const trySteps = ref<LdapTryStep[]>([])
const tryDetail = ref('')
const tryOk = ref(false)

const encryptionLabel: Record<string, string> = {
  none: '明文',
  ldaps: 'LDAPS',
  starttls: 'StartTLS'
}

async function load() {
  loading.value = true
  try {
    servers.value = await listLdapServers()
  } finally {
    loading.value = false
  }
  loadAccounts()
}

async function loadAccounts() {
  const data = await listLdapAccounts(accountQuery)
  accounts.value = data.list || []
  accountTotal.value = data.total
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    host: '',
    port: 389,
    encryption: 'none',
    skipVerify: false,
    bindDn: '',
    bindPassword: '',
    baseDn: '',
    userFilter: '(&(objectClass=inetOrgPerson)(uid=%s))',
    attrNickname: 'displayName',
    attrEmail: 'mail',
    timeoutSec: 8,
    enabled: true,
    loginEnabled: true,
    autoBind: false
  })
  dialogVisible.value = true
}

function openEdit(row: LdapServer) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    host: row.host,
    port: row.port,
    encryption: row.encryption,
    skipVerify: row.skipVerify,
    bindDn: row.bindDn,
    bindPassword: '', // 留空表示不修改
    baseDn: row.baseDn,
    userFilter: row.userFilter,
    attrNickname: row.attrNickname,
    attrEmail: row.attrEmail,
    timeoutSec: row.timeoutSec,
    enabled: row.enabled,
    loginEnabled: row.loginEnabled,
    autoBind: row.autoBind
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateLdapServer(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createLdapServer({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function check(row: LdapServer) {
  const res = await checkLdapServer(row.id)
  ElMessage.success(res.detail)
  load()
}

async function remove(row: LdapServer) {
  await ElMessageBox.confirm(
    `确认删除目录服务器「${row.name}」？绑定在它上面的账号会失去域登录能力`,
    '提示',
    { type: 'warning' }
  )
  await deleteLdapServer(row.id)
  ElMessage.success('已删除')
  load()
}

async function openBind(row: LdapServer) {
  bindServer.value = row
  bindForm.userId = null
  bindForm.entry = null
  dirKeyword.value = ''
  dirUsers.value = []
  bindVisible.value = true
  const data = await listUsers({ page: 1, pageSize: 200 })
  platformUsers.value = data.list || []
  searchDirectory()
}

async function searchDirectory() {
  if (!bindServer.value) return
  dirLoading.value = true
  try {
    const res = await searchLdapUsers(bindServer.value.id, dirKeyword.value)
    dirUsers.value = res.list || []
  } finally {
    dirLoading.value = false
  }
}

function pickEntry(row: LdapDirectoryUser) {
  bindForm.entry = row
  // 同名平台账号最可能是本人，先默认选上
  const hit = platformUsers.value.find((u) => u.username === row.uid)
  if (hit) bindForm.userId = hit.id
}

async function submitBind() {
  if (!bindServer.value || !bindForm.entry || !bindForm.userId) {
    ElMessage.warning('请先选中目录条目与平台账号')
    return
  }
  await bindLdapAccount({
    serverId: bindServer.value.id,
    userId: bindForm.userId,
    ldapDn: bindForm.entry.dn,
    ldapUid: bindForm.entry.uid
  })
  ElMessage.success('绑定成功，该账号今后只能用域口令登录')
  bindVisible.value = false
  load()
}

async function unbind(row: LdapAccount) {
  await ElMessageBox.confirm(
    `确认解绑 ${row.username} ↔ ${row.ldapDn}？解绑后该账号回落到本地口令登录`,
    '提示',
    { type: 'warning' }
  )
  const res = await unbindLdapAccount(row.id)
  ElMessage.success(res.detail)
  load()
}

function openTry(row: LdapServer) {
  tryForm.serverId = row.id
  tryForm.username = ''
  tryForm.password = ''
  trySteps.value = []
  tryDetail.value = ''
  tryVisible.value = true
}

async function submitTry() {
  if (!tryForm.serverId || !tryForm.username || !tryForm.password) {
    ElMessage.warning('登录名与口令都要填')
    return
  }
  const res = await tryLdapLogin({
    serverId: tryForm.serverId,
    username: tryForm.username,
    password: tryForm.password
  })
  trySteps.value = res.steps || []
  tryDetail.value = res.detail
  tryOk.value = res.ok
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          绑定即托管：平台账号一旦绑定到目录条目，本地口令立即失效，登录只认目录口令——
          否则域账号停用后，平台里那份旧口令就是个后门。平台不会按目录自动建号（与 IM 扫码登录同一原则），
          目录里新增的人需要管理员先在「用户管理」建账号再绑定。双因子对域账号同样生效。
        </template>
      </el-alert>


      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'ldap:manage'" type="primary" @click="openCreate">新增目录服务器</el-button>
      </div>

      <el-table v-loading="loading" :data="servers" border stripe>
        <el-table-column prop="name" label="名称" min-width="120" />
        <el-table-column prop="url" label="地址" min-width="180" />
        <el-table-column label="加密" width="100">
          <template #default="{ row }">
            {{ encryptionLabel[row.encryption] || row.encryption }}
            <span v-if="row.skipVerify" class="hint">（跳过证书校验）</span>
          </template>
        </el-table-column>
        <el-table-column prop="baseDn" label="Base DN" min-width="180" show-overflow-tooltip />
        <el-table-column prop="userFilter" label="用户过滤器" min-width="200" show-overflow-tooltip />
        <el-table-column label="开关" width="150">
          <template #default="{ row }">
            <div>启用：{{ row.enabled ? '是' : '否' }}</div>
            <div>域登录：{{ row.loginEnabled ? '开' : '关' }}</div>
            <div>自动绑定：{{ row.autoBind ? '开' : '关' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="绑定账号" width="90">
          <template #default="{ row }">{{ row.boundCount }}</template>
        </el-table-column>
        <el-table-column label="最近检查" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <div v-if="row.lastCheckAt">
              {{ row.lastStatus === 'success' ? '正常' : '失败' }} · {{ row.lastCheckAt }}
            </div>
            <div v-else class="hint">还没检查过</div>
            <div class="hint one-line">{{ row.lastMessage }}</div>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'ldap:manage'" link type="primary" @click="check(row)">测试连接</el-button>
            <el-button v-perm="'ldap:manage'" link type="primary" @click="openBind(row)">绑定账号</el-button>
            <el-button v-perm="'ldap:manage'" link type="primary" @click="openTry(row)">试登录</el-button>
            <el-button v-perm="'ldap:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'ldap:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card style="margin-top: 12px">
      <template #header>
        <div class="card-head">
          <span>账号绑定关系</span>
          <span class="hint">绑定存在 = 这个平台账号的口令由目录说了算</span>
        </div>
      </template>

      <div class="page-toolbar">
        <el-input
          v-model="accountQuery.keyword"
          placeholder="按平台账号 / 登录名 / DN 搜索"
          clearable
          style="width: 260px"
          @keyup.enter="loadAccounts"
          @clear="loadAccounts"
        />
        <el-button @click="loadAccounts">查询</el-button>
      </div>

      <el-table :data="accounts" border stripe size="small">
        <el-table-column prop="username" label="平台账号" width="130" />
        <el-table-column prop="ldapUid" label="目录登录名" width="130" />
        <el-table-column prop="ldapDn" label="目录条目" min-width="240" show-overflow-tooltip />
        <el-table-column prop="ldapName" label="显示名" width="120" />
        <el-table-column prop="ldapEmail" label="邮箱" min-width="160" />
        <el-table-column label="绑定方式" width="110">
          <template #default="{ row }">{{ row.boundBy === 'auto' ? '首登自动' : row.boundBy }}</template>
        </el-table-column>
        <el-table-column prop="lastLogin" label="最近域登录" min-width="170" />
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'ldap:manage'" link type="danger" @click="unbind(row)">解绑</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="accountTotal"
        v-model:current-page="accountQuery.page"
        :page-size="accountQuery.pageSize"
        @current-change="loadAccounts"
      />
    </el-card>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑目录服务器' : '新增目录服务器'"
      width="620px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如 公司 AD" />
        </el-form-item>
        <el-form-item label="服务器地址" prop="host">
          <el-input v-model="form.host" placeholder="ldap.example.com 或 IP" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="加密方式">
          <el-radio-group v-model="form.encryption">
            <el-radio value="none">明文 389</el-radio>
            <el-radio value="ldaps">LDAPS 636</el-radio>
            <el-radio value="starttls">StartTLS</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.encryption !== 'none'" label="跳过证书校验">
          <el-switch v-model="form.skipVerify" />
          <span class="hint">内网自签证书时才需要打开</span>
        </el-form-item>
        <el-form-item label="服务账号 DN">
          <el-input v-model="form.bindDn" placeholder="cn=readonly,dc=example,dc=com（留空为匿名搜索）" />
        </el-form-item>
        <el-form-item label="服务账号口令">
          <el-input
            v-model="form.bindPassword"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '用于搜索目录，不用于登录校验'"
          />
        </el-form-item>
        <el-form-item label="Base DN" prop="baseDn">
          <el-input v-model="form.baseDn" placeholder="dc=example,dc=com" />
        </el-form-item>
        <el-form-item label="用户过滤器">
          <el-input v-model="form.userFilter" />
          <span class="hint">必须含一个 %s；AD 常用 (&(objectClass=user)(sAMAccountName=%s))</span>
        </el-form-item>
        <el-form-item label="显示名属性">
          <el-input v-model="form.attrNickname" style="width: 180px" />
          <span class="hint">OpenLDAP 常用 cn / displayName</span>
        </el-form-item>
        <el-form-item label="邮箱属性">
          <el-input v-model="form.attrEmail" style="width: 180px" />
        </el-form-item>
        <el-form-item label="超时(秒)">
          <el-input-number v-model="form.timeoutSec" :min="1" :max="60" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="允许域登录">
          <el-switch v-model="form.loginEnabled" />
          <span class="hint">关掉后已绑定的账号会登不进来（不会回落到本地口令）</span>
        </el-form-item>
        <el-form-item label="首登自动绑定">
          <el-switch v-model="form.autoBind" />
          <span class="hint">平台里已有同名账号时，首次用域口令登录即自动绑定；账号不存在仍然拒绝</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="bindVisible" :title="`绑定账号 · ${bindServer?.name ?? ''}`" width="820px">
      <div class="page-toolbar">
        <el-input
          v-model="dirKeyword"
          placeholder="搜索目录里的人（按登录名）"
          clearable
          style="width: 260px"
          @keyup.enter="searchDirectory"
          @clear="searchDirectory"
        />
        <el-button @click="searchDirectory">搜索</el-button>
        <span class="hint">用服务账号搜索，最多返回 50 条</span>
      </div>

      <el-table
        v-loading="dirLoading"
        :data="dirUsers"
        border
        size="small"
        highlight-current-row
        @current-change="(row: any) => row && pickEntry(row)"
      >
        <el-table-column prop="uid" label="登录名" width="130" />
        <el-table-column prop="nickname" label="显示名" width="120" />
        <el-table-column prop="email" label="邮箱" min-width="160" />
        <el-table-column prop="dn" label="DN" min-width="240" show-overflow-tooltip />
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <span v-if="row.boundTo">已绑 {{ row.boundTo }}</span>
            <span v-else class="hint">未绑定</span>
          </template>
        </el-table-column>
      </el-table>

      <el-form label-width="120px" style="margin-top: 12px">
        <el-form-item label="选中的条目">
          <span v-if="bindForm.entry">{{ bindForm.entry.dn }}</span>
          <span v-else class="hint">点一行表格选中</span>
        </el-form-item>
        <el-form-item label="绑到平台账号">
          <el-select v-model="bindForm.userId" filterable placeholder="选择已有账号" style="width: 320px">
            <el-option
              v-for="u in platformUsers"
              :key="u.id"
              :label="`${u.username}（${u.nickname || '-'}）`"
              :value="u.id"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="bindVisible = false">取消</el-button>
        <el-button v-perm="'ldap:manage'" type="primary" @click="submitBind">绑定</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="tryVisible" title="试登录诊断" width="620px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          用真实口令走一遍目录校验，只诊断、不签发令牌、不建立绑定。
          接入调试期最常见的是过滤器或 Base DN 写错，登录页那句「用户名或密码错误」看不出来。
        </template>
      </el-alert>
      <el-form label-width="80px">
        <el-form-item label="登录名">
          <el-input v-model="tryForm.username" placeholder="域账号登录名" />
        </el-form-item>
        <el-form-item label="口令">
          <el-input v-model="tryForm.password" type="password" show-password />
        </el-form-item>
      </el-form>

      <el-steps v-if="trySteps.length" direction="vertical" :active="trySteps.length" style="margin-top: 8px">
        <el-step
          v-for="(s, idx) in trySteps"
          :key="idx"
          :title="s.step"
          :description="s.detail"
          :status="s.ok ? 'success' : 'error'"
        />
      </el-steps>
      <el-alert
        v-if="tryDetail"
        style="margin-top: 12px"
        :type="tryOk ? 'success' : 'error'"
        :closable="false"
        :title="tryDetail"
      />

      <template #footer>
        <el-button @click="tryVisible = false">关闭</el-button>
        <el-button v-perm="'ldap:manage'" type="primary" @click="submitTry">开始诊断</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.hint {
  margin-left: 6px;
  color: #6b7280;
  font-size: 12px;
}

.one-line {
  margin-left: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

</style>
