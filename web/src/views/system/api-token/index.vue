<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createApiToken,
  deleteApiToken,
  listApiTokenScopes,
  listApiTokens,
  listUsers,
  revokeApiToken,
  rotateApiToken,
  updateApiToken,
  type ApiToken,
  type ApiTokenCreated,
  type User
} from '@/api'

const loading = ref(false)
const rows = ref<ApiToken[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 10, keyword: '' })

const users = ref<User[]>([])
const scopeOptions = ref<{ code: string; title: string }[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  ownerUserId: null as number | null,
  scopes: [] as string[],
  readOnly: true,
  allowIps: '',
  expiresInDays: 90,
  remark: ''
})

// 明文只出现一次，用单独的对话框强调这件事
const plainVisible = ref(false)
const created = ref<ApiTokenCreated | null>(null)

const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'warning' | 'danger' }> = {
  active: { text: '生效中', type: 'success' },
  disabled: { text: '已停用', type: 'info' },
  expired: { text: '已过期', type: 'warning' },
  revoked: { text: '已撤销', type: 'danger' }
}

const rules = {
  name: [{ required: true, message: '请填写令牌名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    const data = await listApiTokens(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadRefs() {
  const [u, s] = await Promise.all([listUsers({ page: 1, pageSize: 200 }), listApiTokenScopes()])
  users.value = u.list || []
  scopeOptions.value = s.list || []
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    ownerUserId: null,
    scopes: [],
    readOnly: true,
    allowIps: '',
    expiresInDays: 90,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: ApiToken) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    ownerUserId: row.ownerUserId,
    scopes: row.scopeList || [],
    readOnly: row.readOnly,
    allowIps: row.allowIps,
    expiresInDays: 0,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateApiToken(editingId.value, { ...form })
    ElMessage.success('已更新（明文不变）')
    dialogVisible.value = false
  } else {
    const res = await createApiToken({ ...form })
    created.value = res
    dialogVisible.value = false
    plainVisible.value = true
  }
  load()
}

async function rotate(row: ApiToken) {
  await ElMessageBox.confirm(
    `确认轮换「${row.name}」？旧明文立即失效，调用方必须换成新的`,
    '提示',
    { type: 'warning' }
  )
  created.value = await rotateApiToken(row.id)
  plainVisible.value = true
  load()
}

async function revoke(row: ApiToken) {
  await ElMessageBox.confirm(`确认撤销「${row.name}」？撤销后立即失效且不可恢复`, '提示', {
    type: 'warning'
  })
  await revokeApiToken(row.id)
  ElMessage.success('已撤销')
  load()
}

async function remove(row: ApiToken) {
  await ElMessageBox.confirm(`确认删除「${row.name}」？`, '提示', { type: 'warning' })
  await deleteApiToken(row.id)
  ElMessage.success('已删除')
  load()
}

async function copyPlain() {
  if (!created.value) return
  try {
    await navigator.clipboard.writeText(created.value.plain)
    ElMessage.success('已复制到剪贴板')
  } catch {
    ElMessage.warning('浏览器不允许自动复制，请手动选中复制')
  }
}

onMounted(() => {
  load()
  loadRefs()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          令牌明文只在创建与轮换时出现一次，平台只存哈希，丢了只能轮换。令牌权限 = 授予的权限码 ∩
          归属人当下的权限，归属人被降权或停用，令牌立即跟着失效。默认只读（只放 GET）；要写必须显式关掉只读并授权限码。
          令牌不能用于 Web 终端与端口转发（那条链路必须是人，会话审计才有意义）。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="按名称 / 归属人 / 备注搜索"
          clearable
          style="width: 240px"
          @keyup.enter="load"
          @clear="load"
        />
        <el-button @click="load">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'token:manage'" type="primary" @click="openCreate">新建令牌</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="名称" min-width="130" />
        <el-table-column prop="prefix" label="前缀" width="140" />
        <el-table-column prop="ownerName" label="归属人" width="110" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="读写" width="90">
          <template #default="{ row }">{{ row.readOnly ? '只读' : '可写' }}</template>
        </el-table-column>
        <el-table-column label="权限码" min-width="180">
          <template #default="{ row }">
            <span v-if="!row.scopeList?.length" class="hint">无（只读令牌可为空）</span>
            <el-tag v-for="code in row.scopeList" :key="code" size="small" class="scope-tag">
              {{ code }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="来源限制" min-width="140">
          <template #default="{ row }">
            <span v-if="row.allowIps">{{ row.allowIps }}</span>
            <span v-else class="hint">不限制</span>
          </template>
        </el-table-column>
        <el-table-column prop="expiresAt" label="过期时间" min-width="170">
          <template #default="{ row }">
            <span v-if="row.expiresAt">{{ row.expiresAt }}</span>
            <span v-else class="hint">不过期</span>
          </template>
        </el-table-column>
        <el-table-column label="使用情况" min-width="180">
          <template #default="{ row }">
            <div>{{ row.useCount }} 次</div>
            <div v-if="row.lastUsedAt" class="hint">
              最近 {{ row.lastUsedAt }} · {{ row.lastUsedIp }}
            </div>
            <div v-else class="hint">还没被调用过</div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <template v-if="row.status !== 'revoked'">
              <el-button v-perm="'token:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button v-perm="'token:manage'" link type="primary" @click="rotate(row)">轮换</el-button>
              <el-button v-perm="'token:manage'" link type="danger" @click="revoke(row)">撤销</el-button>
            </template>
            <el-button
              v-if="row.useCount === 0"
              v-perm="'token:manage'"
              link
              type="danger"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑令牌' : '新建令牌'"
      width="600px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如 CI 部署流水线" />
        </el-form-item>
        <el-form-item label="归属人">
          <el-select v-model="form.ownerUserId" filterable placeholder="默认为当前登录账号" style="width: 100%">
            <el-option
              v-for="u in users"
              :key="u.id"
              :label="`${u.username}（${u.nickname || '-'}）`"
              :value="u.id"
            />
          </el-select>
          <span class="hint">令牌以这个人的身份访问，数据范围也按他算</span>
        </el-form-item>
        <el-form-item label="只读">
          <el-switch v-model="form.readOnly" />
          <span class="hint">只读令牌只能调 GET；关掉后还要授权限码才能写</span>
        </el-form-item>
        <el-form-item label="权限码">
          <el-select
            v-model="form.scopes"
            multiple
            filterable
            placeholder="可写令牌必须至少选一个"
            style="width: 100%"
          >
            <el-option
              v-for="s in scopeOptions"
              :key="s.code"
              :label="`${s.code}（${s.title}）`"
              :value="s.code"
            />
          </el-select>
          <span class="hint">归属人没有的权限码会被忽略（交集生效）</span>
        </el-form-item>
        <el-form-item label="来源 IP 白名单">
          <el-input v-model="form.allowIps" placeholder="10.0.0.5, 192.168.1.0/24（留空不限制）" />
        </el-form-item>
        <el-form-item label="有效期(天)">
          <el-input-number v-model="form.expiresInDays" :min="0" :max="730" />
          <span class="hint">0 表示不过期；编辑时填 0 表示不改动原有效期</span>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" placeholder="谁在用、用在哪条流水线" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="plainVisible" title="令牌明文（只显示这一次）" width="640px">
      <el-alert type="error" :closable="false" style="margin-bottom: 12px">
        <template #title>{{ created?.detail }}</template>
      </el-alert>
      <el-input :model-value="created?.plain" readonly>
        <template #append>
          <el-button @click="copyPlain">复制</el-button>
        </template>
      </el-input>

      <div v-if="created?.usage" class="usage">
        <div class="usage-title">调用示例</div>
        <pre>{{ created.usage }}</pre>
      </div>

      <div v-if="created?.effectiveScopes" class="usage">
        <div class="usage-title">生效权限</div>
        <div>
          已生效：
          <el-tag
            v-for="code in created.effectiveScopes.granted"
            :key="code"
            size="small"
            class="scope-tag"
            type="success"
          >
            {{ code }}
          </el-tag>
          <span v-if="!created.effectiveScopes.granted.length" class="hint">无</span>
        </div>
        <div v-if="created.effectiveScopes.ignored.length" style="margin-top: 4px">
          被忽略（归属人没有这些权限）：
          <el-tag
            v-for="code in created.effectiveScopes.ignored"
            :key="code"
            size="small"
            class="scope-tag"
            type="warning"
          >
            {{ code }}
          </el-tag>
        </div>
      </div>

      <template #footer>
        <el-button type="primary" @click="plainVisible = false">我已保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.hint {
  margin-left: 6px;
  color: #6b7280;
  font-size: 12px;
}

.scope-tag {
  margin: 0 4px 4px 0;
}

.usage {
  margin-top: 12px;
}

.usage-title {
  margin-bottom: 4px;
  font-size: 12px;
  color: #6b7280;
}

.usage pre {
  margin: 0;
  padding: 8px;
  background: #f5f7fa;
  border-radius: 4px;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
