<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  deleteVaultAccount,
  getVaultState,
  listVaultAccesses,
  listVaultAccounts,
  revealVaultAccount,
  rotateVaultAccount,
  saveVaultAccount,
  type VaultAccess,
  type VaultAccount,
  type VaultState
} from '@/api'

const loading = ref(false)
const rows = ref<VaultAccount[]>([])
const total = ref(0)
const state = ref<VaultState | null>(null)

const query = reactive({
  page: 1,
  pageSize: 20,
  category: '',
  enabled: '',
  overdueOnly: false,
  keyword: ''
})

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  category: 'system',
  platform: '',
  url: '',
  username: '',
  secret: '',
  owner: '',
  rotateDays: 90,
  enabled: true,
  description: ''
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  username: [{ required: true, message: '请输入登录用户', trigger: 'blur' }]
}

const revealVisible = ref(false)
const revealRow = ref<VaultAccount | null>(null)
const revealReason = ref('')
const revealed = ref<{ username: string; secret: string; url: string; note: string } | null>(null)
const revealing = ref(false)

const rotateVisible = ref(false)
const rotateRow = ref<VaultAccount | null>(null)
const rotateForm = reactive({ secret: '', reason: '' })

const accessVisible = ref(false)
const accessRows = ref<VaultAccess[]>([])
const accessTotal = ref(0)
const accessQuery = reactive({ page: 1, pageSize: 20, targetId: 0, action: '', operator: '' })

const categoryLabel = computed(() => {
  const map = new Map<string, string>()
  for (const item of state.value?.categories || []) map.set(item.code, item.label)
  return map
})

const actionMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  reveal: { label: '取口令', type: 'danger' },
  code: { label: '出验证码', type: 'warning' },
  uri: { label: '导出种子', type: 'danger' },
  rotate: { label: '轮换', type: 'success' }
}

function rotateText(row: VaultAccount) {
  if (row.rotateDays <= 0) return '未设周期'
  if (row.overdueDays > 0) return `逾期 ${row.overdueDays} 天`
  return `每 ${row.rotateDays} 天`
}

async function load() {
  loading.value = true
  try {
    const params: Record<string, any> = { page: query.page, pageSize: query.pageSize }
    if (query.category) params.category = query.category
    if (query.enabled) params.enabled = query.enabled
    if (query.overdueOnly) params.overdueOnly = 'true'
    if (query.keyword) params.keyword = query.keyword
    const page = await listVaultAccounts(params)
    rows.value = page.list
    total.value = page.total
    state.value = await getVaultState()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    category: 'system',
    platform: '',
    url: '',
    username: '',
    secret: '',
    owner: '',
    rotateDays: 90,
    enabled: true,
    description: ''
  })
  dialogVisible.value = true
}

function openEdit(row: VaultAccount) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    category: row.category,
    platform: row.platform,
    url: row.url,
    username: row.username,
    secret: '',
    owner: row.owner,
    rotateDays: row.rotateDays,
    enabled: row.enabled,
    description: row.description
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  const payload: Record<string, any> = { ...form }
  // 编辑时后端明确拒绝携带口令，这里不要把空串带过去
  if (editingId.value) delete payload.secret
  await saveVaultAccount(editingId.value || 0, payload)
  ElMessage.success(editingId.value ? '已更新' : '已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: VaultAccount) {
  await ElMessageBox.confirm(
    `删除「${row.name}」？取用留痕不会跟着删除，历史上谁取过这条口令仍然可查。`,
    '确认删除',
    { type: 'warning' }
  )
  await deleteVaultAccount(row.id)
  ElMessage.success('已删除')
  load()
}

function openReveal(row: VaultAccount) {
  revealRow.value = row
  revealReason.value = ''
  revealed.value = null
  revealVisible.value = true
}

async function doReveal() {
  if (!revealRow.value) return
  revealing.value = true
  try {
    revealed.value = await revealVaultAccount(revealRow.value.id, revealReason.value)
    load()
  } finally {
    revealing.value = false
  }
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('浏览器拒绝了剪贴板访问，请手动选中复制')
  }
}

function openRotate(row: VaultAccount) {
  rotateRow.value = row
  rotateForm.secret = ''
  rotateForm.reason = ''
  rotateVisible.value = true
}

async function doRotate() {
  if (!rotateRow.value || !rotateForm.secret) {
    ElMessage.warning('请输入新口令')
    return
  }
  await rotateVaultAccount(rotateRow.value.id, { ...rotateForm })
  ElMessage.success('已轮换')
  rotateVisible.value = false
  load()
}

async function loadAccess() {
  const params: Record<string, any> = {
    page: accessQuery.page,
    pageSize: accessQuery.pageSize,
    target: 'account'
  }
  if (accessQuery.targetId) params.targetId = accessQuery.targetId
  if (accessQuery.action) params.action = accessQuery.action
  if (accessQuery.operator) params.operator = accessQuery.operator
  const page = await listVaultAccesses(params)
  accessRows.value = page.list
  accessTotal.value = page.total
}

function openAccess(row?: VaultAccount) {
  accessQuery.page = 1
  accessQuery.targetId = row?.id || 0
  accessQuery.action = ''
  accessQuery.operator = ''
  accessVisible.value = true
  loadAccess()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这一页和<strong>凭证库</strong>是两件事：凭证库的密钥只给平台自己用（登录主机），
          明文永不出接口；这里的口令是<strong>给人取走用的</strong>（某个后台系统的管理员账号）。
          <br />
          明文能取出来，「不返回密钥」这条防线就不存在了，所以换一种兜法：<strong>取用必留痕</strong>。
          每次点「取口令」都会记下是谁、什么时候、为什么取的；<strong>留痕写不进库时接口直接报错，
          不会把明文发出去</strong>。
          <br />
          改口令只能走「轮换」，编辑接口会拒绝携带口令 —— 否则「最近轮换时间」就是假的，
          逾期提醒也跟着失真。
          <br />
          <strong>不做</strong>：审批后才能取（内网自用，审批只会让人把口令抄到本地记事本里）、
          浏览器自动填充（要装插件）、复制即焚（明文已经到浏览器了，清剪贴板不构成保护）。
        </template>
      </el-alert>

      <div v-if="state" class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-tag :type="state.encryptEnabled ? 'success' : 'danger'">
          {{ state.encryptEnabled ? '口令加密落库' : '未配密钥：口令明文落库' }}
        </el-tag>
        <el-tag type="info">共 {{ state.total }}</el-tag>
        <el-tag type="success">密文 {{ state.sealed }}</el-tag>
        <el-tag :type="state.plain > 0 ? 'warning' : 'info'">明文 {{ state.plain }}</el-tag>
        <el-tag :type="state.overdue > 0 ? 'danger' : 'success'">逾期未轮换 {{ state.overdue }}</el-tag>
        <el-tag type="info">未设轮换周期 {{ state.noRotatePolicy }}</el-tag>
        <el-tag type="info">近 7 天取用 {{ state.reveal7d }} 次</el-tag>
        <el-tag v-if="state.lastCheckInfo" type="info">
          上次轮换检查：{{ state.lastCheckInfo }}
        </el-tag>
        <el-tag v-else type="warning">轮换检查未跑过（OPS_VAULT_SPEC 可能为空）</el-tag>
      </div>

      <div class="page-toolbar">
        <el-select v-model="query.category" placeholder="分类" clearable style="width: 140px">
          <el-option
            v-for="item in state?.categories || []"
            :key="item.code"
            :label="item.label"
            :value="item.code"
          />
        </el-select>
        <el-select v-model="query.enabled" placeholder="状态" clearable style="width: 110px">
          <el-option label="启用" value="true" />
          <el-option label="停用" value="false" />
        </el-select>
        <el-checkbox v-model="query.overdueOnly" @change="((query.page = 1), load())">
          只看逾期
        </el-checkbox>
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 系统 / 用户 / 责任人"
          clearable
          style="width: 220px"
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button @click="openAccess()">取用留痕</el-button>
        <el-button v-perm="'vault:manage'" type="primary" @click="openCreate">新增条目</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记账号">
        <el-table-column prop="name" label="名称" min-width="150" show-overflow-tooltip />
        <el-table-column label="分类" width="110">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ categoryLabel.get(row.category) || row.category }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="platform" label="所属系统" width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ row.platform || '—' }}</template>
        </el-table-column>
        <el-table-column prop="username" label="登录用户" width="140" show-overflow-tooltip />
        <el-table-column label="登录地址" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link v-if="row.url" type="primary" :href="row.url" target="_blank" :underline="false">
              {{ row.url }}
            </el-link>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="存储" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.storage === 'encrypted' ? 'success' : 'warning'">
              {{ row.storage === 'encrypted' ? '密文' : '明文' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="轮换" width="130">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.overdueDays > 0 ? 'danger' : row.rotateDays > 0 ? 'success' : 'info'"
            >
              {{ rotateText(row) }}
            </el-tag>
            <div v-if="row.neverRotated" style="font-size: 12px; color: #909399">从未轮换</div>
          </template>
        </el-table-column>
        <el-table-column label="最近取用" width="170">
          <template #default="{ row }">
            <span v-if="row.lastViewedAt">
              {{ row.lastViewedBy }}
              <span style="color: #909399">{{ row.lastViewedAt.slice(5, 16).replace('T', ' ') }}</span>
              <el-tag size="small" type="info" style="margin-left: 4px">{{ row.viewCount }} 次</el-tag>
            </span>
            <span v-else style="color: #909399">从未取用</span>
          </template>
        </el-table-column>
        <el-table-column prop="owner" label="责任人" width="100">
          <template #default="{ row }">{{ row.owner || '—' }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="250" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'vault:reveal'" link type="danger" @click="openReveal(row)">取口令</el-button>
            <el-button v-perm="'vault:manage'" link type="warning" @click="openRotate(row)">轮换</el-button>
            <el-button link type="primary" @click="openAccess(row)">留痕</el-button>
            <el-button v-perm="'vault:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'vault:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next"
        style="margin-top: 12px"
        @change="load"
      />

      <ul v-if="state" style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
        <li v-for="(note, idx) in state.notes" :key="idx">{{ note }}</li>
      </ul>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑条目' : '新增条目'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如：Jenkins 管理员" />
        </el-form-item>
        <el-form-item label="分类">
          <el-select v-model="form.category" style="width: 100%">
            <el-option
              v-for="item in state?.categories || []"
              :key="item.code"
              :label="item.label"
              :value="item.code"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="所属系统">
          <el-input v-model="form.platform" placeholder="如：Jenkins" />
        </el-form-item>
        <el-form-item label="登录地址">
          <el-input v-model="form.url" placeholder="http://..." />
        </el-form-item>
        <el-form-item label="登录用户" prop="username">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item v-if="!editingId" label="口令">
          <el-input v-model="form.secret" type="password" show-password />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            新增时必填。之后改口令走「轮换」，编辑接口会拒绝携带口令
          </el-text>
        </el-form-item>
        <el-form-item v-else label="口令">
          <el-text type="info">编辑不能改口令，请用列表里的「轮换」</el-text>
        </el-form-item>
        <el-form-item label="轮换周期">
          <el-input-number v-model="form.rotateDays" :min="0" :max="3650" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            单位天，0 表示不提醒。设了周期且逾期的会进告警通道
          </el-text>
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="form.owner" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="revealVisible" title="取口令明文" width="520px">
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>本次取用会被记入留痕：谁取的、什么时候取的、填的理由</template>
      </el-alert>
      <el-form label-width="80px">
        <el-form-item label="条目">
          <span>{{ revealRow?.name }}（{{ revealRow?.username }}）</span>
        </el-form-item>
        <el-form-item label="取用理由">
          <el-input v-model="revealReason" placeholder="选填，如：排查构建失败" />
        </el-form-item>
      </el-form>

      <div v-if="revealed">
        <el-descriptions :column="1" border>
          <el-descriptions-item label="登录用户">
            {{ revealed.username }}
            <el-button link type="primary" @click="copyText(revealed.username)">复制</el-button>
          </el-descriptions-item>
          <el-descriptions-item label="口令">
            <code>{{ revealed.secret }}</code>
            <el-button link type="primary" @click="copyText(revealed.secret)">复制</el-button>
          </el-descriptions-item>
        </el-descriptions>
        <el-text type="info" size="small" style="display: block; margin-top: 8px">
          {{ revealed.note }}
        </el-text>
      </div>

      <template #footer>
        <el-button @click="revealVisible = false">关闭</el-button>
        <el-button v-if="!revealed" type="danger" :loading="revealing" @click="doReveal">
          确认取用
        </el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="rotateVisible" title="轮换口令" width="480px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          轮换只改平台里存的这份记录，<strong>目标系统上的口令要你自己去改</strong>。
          先改系统、再回来登记，顺序反了会有一段时间对不上。
        </template>
      </el-alert>
      <el-form label-width="80px">
        <el-form-item label="条目">
          <span>{{ rotateRow?.name }}</span>
        </el-form-item>
        <el-form-item label="新口令">
          <el-input v-model="rotateForm.secret" type="password" show-password />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="rotateForm.reason" placeholder="选填，如：季度轮换" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="rotateVisible = false">取消</el-button>
        <el-button type="primary" @click="doRotate">确认轮换</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="accessVisible" title="取用留痕" size="60%">
      <div class="page-toolbar">
        <el-select v-model="accessQuery.action" placeholder="动作" clearable style="width: 130px">
          <el-option label="取口令" value="reveal" />
          <el-option label="轮换" value="rotate" />
        </el-select>
        <el-input
          v-model="accessQuery.operator"
          placeholder="操作人"
          clearable
          style="width: 160px"
          @keyup.enter="((accessQuery.page = 1), loadAccess())"
        />
        <el-button @click="((accessQuery.page = 1), loadAccess())">查询</el-button>
        <el-tag v-if="accessQuery.targetId" type="info">
          只看条目 #{{ accessQuery.targetId }}
        </el-tag>
      </div>

      <el-table :data="accessRows" border stripe empty-text="还没有取用记录">
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
        <el-table-column prop="operator" label="操作人" width="110" />
        <el-table-column label="动作" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="actionMeta[row.action]?.type || 'info'">
              {{ actionMeta[row.action]?.label || row.action }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="targetName" label="条目" min-width="140" show-overflow-tooltip />
        <el-table-column prop="ip" label="来源 IP" width="130" />
        <el-table-column prop="reason" label="理由" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ row.reason || '未填' }}</template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-model:current-page="accessQuery.page"
        v-model:page-size="accessQuery.pageSize"
        :total="accessTotal"
        :page-sizes="[20, 50]"
        layout="total, sizes, prev, pager, next"
        style="margin-top: 12px"
        @change="loadAccess"
      />

      <el-text type="info" size="small" style="display: block; margin-top: 10px">
        条目被删除后留痕不跟着删，「条目」列里存的是取用当时的名字。
      </el-text>
    </el-drawer>
  </div>
</template>
