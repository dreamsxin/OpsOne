<script setup lang="ts">
/**
 * 凭证库（共享登录凭据）。
 *
 * 要解决的问题很具体：同一个运维账号的口令散落在几十台主机记录里，换一次口令要逐台改，
 * 改漏的那台会静默失效，而且没人答得上「这台机器用的是哪份凭据」。
 *
 * 所以这一页的核心不是「又一个 CRUD」，而是三个动作：
 *   引用方（改它会影响谁）→ 测试连接（先确认真能登进去）→ 轮换（改一处，所有引用主机下次连接生效）
 *
 * 密钥永不回显，连脱敏版本都不给：能看的只有指纹、存储方式（密文/明文）和用过没有。
 * 加密是否真的开着取决于部署时有没有配 OPS_SECRET_KEY，页头会照实说，
 * 不做「有凭证库就等于加密了」的暗示。
 */
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  bindCredentialHosts,
  checkCredential,
  deleteCredential,
  getCredentialState,
  getSecretAudit,
  listCredentialHosts,
  listCredentials,
  listHosts,
  migrateSecrets,
  rotateCredential,
  saveCredential,
  type Credential,
  type CredentialHostRef,
  type CredentialState,
  type Host,
  type SecretAuditRow
} from '@/api'

import PageHeader from '@/components/PageHeader.vue'
import Pagination from '@/components/Pagination.vue'

const loading = ref(false)
const rows = ref<Credential[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const keyword = ref('')
const typeFilter = ref<'' | 'password' | 'key'>('')
const state = ref<CredentialState | null>(null)
const hosts = ref<Host[]>([])

async function load() {
  loading.value = true
  try {
    const data = await listCredentials({
      page: page.value,
      pageSize: pageSize.value,
      keyword: keyword.value || undefined,
      type: typeFilter.value || undefined
    })
    rows.value = data.list
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadState() {
  state.value = await getCredentialState()
}

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 500 })
  hosts.value = data.list
}

function search() {
  page.value = 1
  load()
}

/* ---------------- 新增 / 编辑 ---------------- */

const dialog = ref(false)
const editing = ref<Credential | null>(null)
const saving = ref(false)
const form = reactive({
  name: '',
  type: 'password' as 'password' | 'key',
  username: 'root',
  secret: '',
  passphrase: '',
  owner: '',
  description: '',
  enabled: true
})

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    type: 'password',
    username: 'root',
    secret: '',
    passphrase: '',
    owner: '',
    description: '',
    enabled: true
  })
  dialog.value = true
}

function openEdit(row: Credential) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    type: row.type,
    username: row.username,
    // 密钥不回显：编辑时留空表示不动它，要换走「轮换」
    secret: '',
    passphrase: '',
    owner: row.owner,
    description: row.description,
    enabled: row.enabled
  })
  dialog.value = true
}

async function submit() {
  if (!form.name.trim() || !form.username.trim()) {
    ElMessage.warning('凭据名称与登录用户必填')
    return
  }
  if (!editing.value && !form.secret.trim()) {
    ElMessage.warning(form.type === 'key' ? '请粘贴私钥内容' : '请填写登录口令')
    return
  }
  saving.value = true
  try {
    await saveCredential(editing.value?.id || 0, {
      name: form.name.trim(),
      type: form.type,
      username: form.username.trim(),
      secret: form.secret,
      passphrase: form.passphrase,
      owner: form.owner.trim(),
      description: form.description,
      enabled: form.enabled
    })
    ElMessage.success(editing.value ? '凭据已更新' : '凭据已创建')
    dialog.value = false
    await Promise.all([load(), loadState()])
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(row: Credential, enabled: boolean) {
  try {
    await saveCredential(row.id, {
      name: row.name,
      type: row.type,
      username: row.username,
      owner: row.owner,
      description: row.description,
      enabled
    })
    row.enabled = enabled
    ElMessage.success(enabled ? '已启用' : '已禁用，引用它的主机将连不上并提示原因')
  } catch {
    // 失败时把开关拨回去，否则界面显示的状态是假的
    row.enabled = !enabled
  }
}

async function remove(row: Credential) {
  await ElMessageBox.confirm(
    row.hostCount > 0
      ? `还有 ${row.hostCount} 台主机在引用「${row.name}」，平台会拒绝删除。先把这些主机改回自带凭据或换成别的凭据。`
      : `删除凭据「${row.name}」？密钥一并删除且不可恢复。`,
    '删除凭据',
    { type: 'warning' }
  )
  await deleteCredential(row.id)
  ElMessage.success('凭据已删除')
  await Promise.all([load(), loadState()])
}

/* ---------------- 引用方 ---------------- */

const refsDrawer = ref(false)
const refsRows = ref<CredentialHostRef[]>([])
const refsLoading = ref(false)
const current = ref<Credential | null>(null)

async function openRefs(row: Credential) {
  current.value = row
  refsDrawer.value = true
  refsLoading.value = true
  try {
    refsRows.value = await listCredentialHosts(row.id)
  } finally {
    refsLoading.value = false
  }
}

/* ---------------- 轮换 ---------------- */

const rotateDialog = ref(false)
const rotating = ref(false)
const rotateForm = reactive({ secret: '', passphrase: '' })
const rotateResult = ref<{ affectedHosts: number; oldFingerprint: string; fingerprint: string; note: string } | null>(
  null
)

function openRotate(row: Credential) {
  current.value = row
  rotateForm.secret = ''
  rotateForm.passphrase = ''
  rotateResult.value = null
  rotateDialog.value = true
}

async function doRotate() {
  if (!current.value || !rotateForm.secret.trim()) {
    ElMessage.warning('请填写新的口令或私钥')
    return
  }
  rotating.value = true
  try {
    rotateResult.value = await rotateCredential(current.value.id, {
      secret: rotateForm.secret,
      passphrase: rotateForm.passphrase
    })
    rotateForm.secret = ''
    rotateForm.passphrase = ''
    await Promise.all([load(), loadState()])
  } finally {
    rotating.value = false
  }
}

/* ---------------- 测试连接 ---------------- */

const checkDialog = ref(false)
const checking = ref(false)
const checkHostId = ref<number | null>(null)
const checkResult = ref<Awaited<ReturnType<typeof checkCredential>> | null>(null)

async function openCheck(row: Credential) {
  current.value = row
  checkResult.value = null
  checkHostId.value = null
  checkDialog.value = true
  // 已经有引用方时默认挑第一台：多数情况就是想复验这份凭据还能不能登
  if (row.hostCount > 0) {
    const refs = await listCredentialHosts(row.id)
    checkHostId.value = refs[0]?.id ?? null
  }
}

async function doCheck() {
  if (!current.value || !checkHostId.value) {
    ElMessage.warning('请选择一台用于测试的主机')
    return
  }
  checking.value = true
  checkResult.value = null
  try {
    checkResult.value = await checkCredential(current.value.id, checkHostId.value)
    await load()
  } finally {
    checking.value = false
  }
}

/* ---------------- 接入主机 ---------------- */

const bindDialog = ref(false)
const binding = ref(false)
const bindHostIds = ref<number[]>([])

function openBind(row: Credential) {
  current.value = row
  bindHostIds.value = []
  bindDialog.value = true
}

async function doBind() {
  if (!current.value || !bindHostIds.value.length) {
    ElMessage.warning('请选择要接入的主机')
    return
  }
  await ElMessageBox.confirm(
    `这 ${bindHostIds.value.length} 台主机将改为使用凭据「${current.value.name}」登录：` +
      `登录用户名以凭据为准，主机上原来那份口令 / 私钥会被清空（不留副本，避免留下不会被轮换的旧口令）。`,
    '接入共享凭据',
    { type: 'warning' }
  )
  binding.value = true
  try {
    const res = await bindCredentialHosts(current.value.id, bindHostIds.value)
    ElMessage.success(
      res.skipped > 0
        ? `已接入 ${res.bound} 台，跳过 ${res.skipped} 台（无权限或已不存在）`
        : `已接入 ${res.bound} 台主机`
    )
    bindDialog.value = false
    await Promise.all([load(), loadState()])
  } finally {
    binding.value = false
  }
}

/* ---------------- 展示辅助 ---------------- */

const hostName = computed(() => {
  const map = new Map(hosts.value.map((x) => [x.id, `${x.name}（${x.address}）`]))
  return (id: number) => map.get(id) || `#${id}`
})

function shortFp(fp: string) {
  if (!fp) return '—'
  return fp.length > 26 ? `${fp.slice(0, 26)}…` : fp
}

function fmt(t: string | null) {
  return t ? t.replace('T', ' ').slice(0, 19) : '—'
}

watch([keyword, typeFilter], () => {
  page.value = 1
})

/* ---------------- 密钥加密体检 ---------------- */

/**
 * 加密是在读写点显式调用的，所以「可能漏一条写路径」。
 * 体检直接按前缀数每张表还有多少明文行 —— 漏了就能看见，而不是靠人盯代码。
 */
const auditRows = ref<SecretAuditRow[]>([])
const auditNote = ref('')
const auditLimit = ref('')
const auditPlainTotal = ref(0)
const auditLoading = ref(false)
const migrating = ref(false)

async function loadAudit() {
  auditLoading.value = true
  try {
    const res = await getSecretAudit()
    auditRows.value = res.rows || []
    auditNote.value = res.note
    auditLimit.value = res.limit
    auditPlainTotal.value = res.plainTotal
  } catch (err: any) {
    ElMessage.error(err?.message || '读取加密体检失败')
  } finally {
    auditLoading.value = false
  }
}

async function doMigrate() {
  await ElMessageBox.confirm(
    '把已纳入加密的字段里的明文行重写成密文。值不会变，只是换一种存法；可以反复执行。\n\n' +
      '注意：重写之后这些行就依赖 OPS_SECRET_KEY 了 —— 换掉那个环境变量会导致它们解不开。',
    '加密存量数据',
    { type: 'warning' }
  )
  migrating.value = true
  try {
    const res = await migrateSecrets()
    ElMessage.success(res.note)
    await Promise.all([loadAudit(), loadState(), load()])
  } catch (err: any) {
    ElMessage.error(err?.message || '迁移失败')
  } finally {
    migrating.value = false
  }
}

onMounted(async () => {
  await Promise.all([load(), loadState(), loadHosts(), loadAudit()])
})

</script>

<template>
  <div class="page">
    <PageHeader
      title="凭证库"
      subtitle="一份口令 / 私钥存一处，多台主机引用它；轮换一次，所有引用主机的下一次连接自动生效"
    >
      <template #actions>
        <el-button @click="load()">刷新</el-button>
        <el-button type="primary" @click="openCreate">新增凭据</el-button>
      </template>
    </PageHeader>

    <!-- 事实条：加密到底开没开，是这一页最该先说清的事 -->
    <el-card v-if="state" class="fact-card" shadow="never">
      <div class="facts">
        <div class="fact">
          <span class="fact__label">存储方式</span>
          <el-tag :type="state.encryptEnabled ? 'success' : 'danger'" size="small" effect="dark">
            {{ state.encryptEnabled ? 'AES-GCM 密文' : '明文' }}
          </el-tag>
        </div>
        <el-divider direction="vertical" />
        <div class="fact"><span class="fact__label">凭据总数</span>{{ state.total }}</div>
        <div class="fact"><span class="fact__label">其中密文</span>{{ state.sealed }}</div>
        <div class="fact"><span class="fact__label">其中明文</span>{{ state.plain }}</div>
        <el-divider direction="vertical" />
        <div class="fact"><span class="fact__label">引用共享凭据的主机</span>{{ state.hostsShared }}</div>
        <div class="fact"><span class="fact__label">仍用自带凭据的主机</span>{{ state.hostsLocal }}</div>
      </div>
      <el-alert
        :type="state.encryptEnabled ? (state.plain > 0 ? 'warning' : 'info') : 'error'"
        :closable="false"
        show-icon
        class="fact-note"
      >
        {{ state.note }}
      </el-alert>
    </el-card>

    <el-card shadow="never">
      <div class="toolbar">
        <el-input
          v-model="keyword"
          placeholder="名称 / 登录用户 / 责任人"
          clearable
          style="width: 240px"
          @keyup.enter="search"
          @clear="search"
        />
        <el-select v-model="typeFilter" placeholder="全部类型" clearable style="width: 140px" @change="search">
          <el-option label="口令" value="password" />
          <el-option label="私钥" value="key" />
        </el-select>
        <el-button @click="search">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有凭据，点右上角新增">
        <el-table-column prop="name" label="名称" min-width="150" />
        <el-table-column label="类型" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="row.type === 'key' ? 'success' : 'info'">
              {{ row.type === 'key' ? '私钥' : '口令' }}
            </el-tag>
            <el-tag v-if="row.hasPassphrase" size="small" type="warning" style="margin-left: 4px">带口令</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="username" label="登录用户" width="120" />
        <el-table-column label="指纹" min-width="180">
          <template #default="{ row }">
            <el-tooltip v-if="row.fingerprint" :content="row.fingerprint" placement="top">
              <span class="mono">{{ shortFp(row.fingerprint) }}</span>
            </el-tooltip>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="存储" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.storage === 'encrypted' ? 'success' : 'danger'" effect="plain">
              {{ row.storage === 'encrypted' ? '密文' : '明文' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="引用主机" width="100" align="center">
          <template #default="{ row }">
            <el-button link type="primary" @click="openRefs(row)">{{ row.hostCount }} 台</el-button>
          </template>
        </el-table-column>
        <el-table-column prop="owner" label="责任人" width="100">
          <template #default="{ row }">{{ row.owner || '—' }}</template>
        </el-table-column>
        <el-table-column label="最近轮换" width="160">
          <template #default="{ row }">{{ fmt(row.rotatedAt) }}</template>
        </el-table-column>
        <el-table-column label="最近成功登录" min-width="200">
          <template #default="{ row }">
            <template v-if="row.lastUsedAt">
              {{ fmt(row.lastUsedAt) }}
              <div class="muted">{{ hostName(row.lastUsedHostId) }}</div>
            </template>
            <span v-else class="muted">从未验证过</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="80" align="center">
          <template #default="{ row }">
            <el-switch
              :model-value="row.enabled"
              @update:model-value="(v: any) => toggleEnabled(row, !!v)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="260" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openCheck(row)">测试连接</el-button>
            <el-button link type="primary" @click="openRotate(row)">轮换</el-button>
            <el-button link type="primary" @click="openBind(row)">接入主机</el-button>
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-card shadow="never" class="audit-card">
      <div class="audit-head">
        <div>
          <div class="audit-title">密钥加密体检</div>
          <div class="tip">
            加密是在读写点显式加的，所以可能漏写路径；这张表直接数每列还有多少明文行，漏了就看得见
          </div>
        </div>
        <div>
          <el-button :loading="auditLoading" @click="loadAudit">重新体检</el-button>
          <el-button
            v-perm="'credential:manage'"
            type="primary"
            :disabled="auditPlainTotal === 0"
            :loading="migrating"
            @click="doMigrate"
          >
            加密存量数据{{ auditPlainTotal ? `（${auditPlainTotal} 行）` : '' }}
          </el-button>
        </div>
      </div>

      <el-alert
        v-if="auditNote"
        :type="auditPlainTotal > 0 ? 'warning' : 'info'"
        :closable="false"
        show-icon
        :title="auditNote"
        style="margin-bottom: 8px"
      />
      <el-table v-loading="auditLoading" :data="auditRows" border stripe size="small">
        <el-table-column prop="label" label="字段" min-width="170" />
        <el-table-column label="库里位置" min-width="200">
          <template #default="{ row }">
            <span class="mono">{{ row.table }}.{{ row.column }}</span>
          </template>
        </el-table-column>
        <el-table-column label="纳入加密" width="100" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="row.covered ? 'success' : 'info'" effect="plain">
              {{ row.covered ? '已纳入' : '尚未' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="非空行 / 密文 / 明文" min-width="170">
          <template #default="{ row }">
            <span v-if="row.readErr" class="muted">读不到（{{ row.readErr }}）</span>
            <span v-else>
              {{ row.total }} / {{ row.sealed }} /
              <b :class="{ warn: row.covered && row.plain > 0 }">{{ row.plain }}</b>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="note" label="这份密钥的分量" min-width="230" show-overflow-tooltip />
      </el-table>
      <el-alert v-if="auditLimit" type="info" :closable="false" :title="auditLimit" style="margin-top: 8px" />
    </el-card>

    <el-alert type="info" :closable="false" show-icon class="scope-note">

      <template #title>这一页不做的事</template>
      口令借用审批与临时授权、到期自动轮换、SSH CA 签发短期证书（真正做到「凭据不落地」的方案）都没做；
      数据库实例、云账号、Jenkins、模型上游那些密钥仍各自存在自己的模块里，没有统一到这里。
      凭据能被平台解密使用，也就意味着拿到数据库文件与 OPS_SECRET_KEY 的人能拿到全部凭据 —— 这是这种设计的固有代价。
    </el-alert>

    <!-- 新增 / 编辑 -->
    <el-dialog v-model="dialog" :title="editing ? '编辑凭据' : '新增凭据'" width="620px">
      <el-form :model="form" label-width="96px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="例如 生产环境 root 口令 / 跳板机部署私钥" />
        </el-form-item>
        <el-form-item label="认证方式">
          <el-radio-group v-model="form.type">
            <el-radio-button value="password">口令</el-radio-button>
            <el-radio-button value="key">私钥</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="登录用户" required>
          <el-input v-model="form.username" placeholder="root / deploy" />
          <div class="hint">引用这份凭据的主机，登录用户名一律用这里的值</div>
        </el-form-item>
        <el-form-item :label="form.type === 'key' ? '私钥' : '口令'">
          <el-input
            v-if="form.type === 'key'"
            v-model="form.secret"
            type="textarea"
            :rows="6"
            placeholder="粘贴 PEM 格式私钥（-----BEGIN ... PRIVATE KEY-----）"
          />
          <el-input v-else v-model="form.secret" type="password" show-password placeholder="登录口令" />
          <div class="hint">
            {{ editing ? '留空表示不改动现有密钥；要换密钥请用「轮换」' : '密钥保存后不再回显，只能轮换' }}
          </div>
        </el-form-item>
        <el-form-item v-if="form.type === 'key'" label="私钥口令">
          <el-input v-model="form.passphrase" type="password" show-password placeholder="私钥没有加密就留空" />
          <div class="hint">带口令的私钥以前在平台上完全用不了，现在可以了</div>
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="form.owner" placeholder="谁对这份凭据负责" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 轮换 -->
    <el-dialog v-model="rotateDialog" :title="`轮换凭据：${current?.name || ''}`" width="620px">
      <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 12px">
        换完之后 {{ current?.hostCount || 0 }} 台引用主机的下一次连接就用新密钥。
        平台不会自动去逐台验证（几十台串行连一遍要几分钟），换完请挑一台点「测试连接」确认。
      </el-alert>
      <el-form label-width="96px">
        <el-form-item :label="current?.type === 'key' ? '新私钥' : '新口令'" required>
          <el-input
            v-if="current?.type === 'key'"
            v-model="rotateForm.secret"
            type="textarea"
            :rows="6"
            placeholder="粘贴新的 PEM 私钥"
          />
          <el-input v-else v-model="rotateForm.secret" type="password" show-password />
        </el-form-item>
        <el-form-item v-if="current?.type === 'key'" label="私钥口令">
          <el-input v-model="rotateForm.passphrase" type="password" show-password />
        </el-form-item>
      </el-form>
      <el-alert v-if="rotateResult" type="success" :closable="false" show-icon>
        <div>{{ rotateResult.note }}</div>
        <div v-if="rotateResult.fingerprint" class="mono">
          指纹：{{ rotateResult.oldFingerprint || '（无）' }} → {{ rotateResult.fingerprint }}
        </div>
      </el-alert>
      <template #footer>
        <el-button @click="rotateDialog = false">关闭</el-button>
        <el-button type="danger" :loading="rotating" @click="doRotate">确认轮换</el-button>
      </template>
    </el-dialog>

    <!-- 测试连接 -->
    <el-dialog v-model="checkDialog" :title="`测试连接：${current?.name || ''}`" width="560px">
      <el-form label-width="96px">
        <el-form-item label="用哪台主机">
          <el-select v-model="checkHostId" filterable placeholder="选择一台主机" style="width: 100%">
            <el-option
              v-for="h in hosts"
              :key="h.id"
              :label="`${h.name}（${h.address}:${h.port}）`"
              :value="h.id"
            />
          </el-select>
          <div class="hint">
            主机地址与跳板链用这台机器的，认证信息强制用被测凭据 —— 所以还没绑定也能先试通再铺
          </div>
        </el-form-item>
      </el-form>
      <el-alert
        v-if="checkResult"
        :type="checkResult.ok ? 'success' : 'error'"
        :closable="false"
        show-icon
      >
        <div v-if="checkResult.ok">
          登录成功，耗时 {{ checkResult.costMs }} ms，实际登录用户
          <b>{{ checkResult.loginUser || '未知' }}</b>
          <span v-if="!checkResult.usernameMatch" class="warn">
            （与凭据里写的 {{ checkResult.credUsername }} 不一致，可能被目标机器的配置改写了）
          </span>
        </div>
        <div v-else>登录失败：{{ checkResult.detail || '未知原因' }}</div>
        <div v-if="checkResult.viaProxy" class="muted">经跳板：{{ checkResult.viaProxy }}</div>
      </el-alert>
      <template #footer>
        <el-button @click="checkDialog = false">关闭</el-button>
        <el-button type="primary" :loading="checking" @click="doCheck">真连一次</el-button>
      </template>
    </el-dialog>

    <!-- 接入主机 -->
    <el-dialog v-model="bindDialog" :title="`把主机接入：${current?.name || ''}`" width="620px">
      <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 12px">
        接入会清空这些主机本机的口令 / 私钥，登录用户名改用凭据里的
        <b>{{ current?.username }}</b>
        。建议先用「测试连接」确认这份凭据在目标机器上真的能登。
      </el-alert>
      <el-select v-model="bindHostIds" multiple filterable placeholder="选择主机" style="width: 100%">
        <el-option
          v-for="h in hosts"
          :key="h.id"
          :label="`${h.name}（${h.address}）${h.credentialId ? ' · 已引用凭据' : ''}`"
          :value="h.id"
        />
      </el-select>
      <template #footer>
        <el-button @click="bindDialog = false">取消</el-button>
        <el-button type="primary" :loading="binding" @click="doBind">接入</el-button>
      </template>
    </el-dialog>

    <!-- 引用方 -->
    <el-drawer v-model="refsDrawer" :title="`引用方：${current?.name || ''}`" size="560px">
      <el-alert type="info" :closable="false" show-icon style="margin-bottom: 12px">
        这些主机登录时用的就是这份凭据。要解除引用，去「主机管理」编辑该主机并填回本机口令
        —— 解除必须同时给新凭据，否则会存出一台没有任何凭据、连不上又看不出原因的主机。
      </el-alert>
      <el-table v-loading="refsLoading" :data="refsRows" border stripe size="small" empty-text="还没有主机引用它">
        <el-table-column prop="name" label="主机" min-width="140" />
        <el-table-column prop="address" label="地址" min-width="140" />
        <el-table-column prop="env" label="环境" width="80" />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.status === 'online' ? 'success' : row.status === 'offline' ? 'danger' : 'info'"
            >
              {{ row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未知' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.page {
  padding: 16px;
}
.fact-card {
  margin-bottom: 12px;
}
.facts {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
  font-size: 13px;
}
.fact {
  display: flex;
  align-items: center;
  gap: 6px;
}
.fact__label {
  color: var(--ops-text-secondary, #6b7280);
}
.fact-note {
  margin-top: 12px;
}
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.muted {
  color: var(--ops-text-secondary, #9ca3af);
  font-size: 12px;
}
.warn {
  color: var(--el-color-warning);
}
.hint {
  font-size: 12px;
  color: var(--ops-text-secondary, #9ca3af);
  line-height: 1.5;
}
.scope-note {
  margin-top: 12px;
}
.audit-card {
  margin-top: 12px;
}
.audit-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 10px;
}
.audit-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 2px;
}
</style>

