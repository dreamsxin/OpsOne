<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  codeVaultTOTP,
  deleteVaultTOTP,
  listVaultAccesses,
  listVaultAccounts,
  listVaultTOTPs,
  saveVaultTOTP,
  uriVaultTOTP,
  type VaultAccess,
  type VaultAccount,
  type VaultTOTPItem
} from '@/api'

const loading = ref(false)
const rows = ref<VaultTOTPItem[]>([])
const total = ref(0)
const accounts = ref<VaultAccount[]>([])

const query = reactive({ page: 1, pageSize: 20, keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  issuer: '',
  account: '',
  accountId: 0,
  secret: '',
  owner: '',
  enabled: true,
  description: ''
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }]
}

const codeVisible = ref(false)
const codeRow = ref<VaultTOTPItem | null>(null)
const codeReason = ref('')
const coding = ref(false)
const codeResult = ref<{
  code: string
  nextCode?: string
  remainSeconds: number
  period: number
  serverTime: string
  note: string
} | null>(null)
const remain = ref(0)
let timer: number | undefined

const uriVisible = ref(false)
const uriText = ref('')

const accessVisible = ref(false)
const accessRows = ref<VaultAccess[]>([])

async function load() {
  loading.value = true
  try {
    const params: Record<string, any> = { page: query.page, pageSize: query.pageSize }
    if (query.keyword) params.keyword = query.keyword
    const page = await listVaultTOTPs(params)
    rows.value = page.list
    total.value = page.total
  } finally {
    loading.value = false
  }
}

async function loadAccounts() {
  const page = await listVaultAccounts({ page: 1, pageSize: 200 })
  accounts.value = page.list
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    issuer: '',
    account: '',
    accountId: 0,
    secret: '',
    owner: '',
    enabled: true,
    description: ''
  })
  dialogVisible.value = true
}

function openEdit(row: VaultTOTPItem) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    issuer: row.issuer,
    account: row.account,
    accountId: row.accountId,
    secret: '',
    owner: row.owner,
    enabled: row.enabled,
    description: row.description
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  await saveVaultTOTP(editingId.value || 0, { ...form })
  ElMessage.success(editingId.value ? '已更新' : '已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: VaultTOTPItem) {
  await ElMessageBox.confirm(
    `删除「${row.name}」？种子删掉之后就只能重新找一次原始二维码了。`,
    '确认删除',
    { type: 'warning' }
  )
  await deleteVaultTOTP(row.id)
  ElMessage.success('已删除')
  load()
}

function stopTimer() {
  if (timer) {
    window.clearInterval(timer)
    timer = undefined
  }
}

function openCode(row: VaultTOTPItem) {
  codeRow.value = row
  codeReason.value = ''
  codeResult.value = null
  stopTimer()
  codeVisible.value = true
}

async function doCode() {
  if (!codeRow.value) return
  coding.value = true
  try {
    codeResult.value = await codeVaultTOTP(codeRow.value.id, codeReason.value)
    remain.value = codeResult.value.remainSeconds
    stopTimer()
    // 倒计时只是本地读秒，走到 0 就提示重新取 —— 不自动续码，
    // 否则一次留痕会对应到无限多个验证码
    timer = window.setInterval(() => {
      remain.value = Math.max(0, remain.value - 1)
      if (remain.value === 0) stopTimer()
    }, 1000)
    load()
  } finally {
    coding.value = false
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

async function showURI(row: VaultTOTPItem) {
  await ElMessageBox.confirm(
    'otpauth 地址里含种子原文，等同于第二因子本身。导出会被记入留痕，确认继续？',
    '导出种子',
    { type: 'warning' }
  )
  const res = await uriVaultTOTP(row.id, '导出到验证器')
  uriText.value = res.uri
  uriVisible.value = true
  load()
}

async function openAccess(row?: VaultTOTPItem) {
  const params: Record<string, any> = { page: 1, pageSize: 50, target: 'totp' }
  if (row) params.targetId = row.id
  const page = await listVaultAccesses(params)
  accessRows.value = page.list
  accessVisible.value = true
}

onMounted(() => {
  load()
  loadAccounts()
})
onBeforeUnmount(stopTimer)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          解决的是「这个账号开了两步验证，验证器绑在离职同事手机上」。种子存在平台，
          谁需要谁现算一个码。
          <br />
          <strong>把种子托管在这里，2FA 对这些账号就退化成「能进这一页的人都能登」</strong> ——
          这句话必须先说清楚，再决定要不要用。所以出码单独一个权限（vault:reveal），
          每次出码、每次导出种子都留痕。
          <br />
          验证码用<strong>服务器时间</strong>计算。服务器时钟偏差超过 30 秒，算出来的码全是错的，
          而表现是「验证码总是不对」，很容易被误判成种子存错了 —— 所以出码时会一并显示服务器时间。
          <br />
          录入时会用 base32 解一遍，乱码当场拒绝；otpauth 地址里带了非默认的位数 / 步长 / 算法也直接拒绝，
          <strong>不静默忽略</strong>（忽略的话码永远对不上，且没人想得到是这个原因）。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 服务方 / 账号 / 责任人"
          clearable
          style="width: 240px"
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button @click="openAccess()">取用留痕</el-button>
        <el-button v-perm="'vault:manage'" type="primary" @click="openCreate">新增种子</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有托管种子">
        <el-table-column prop="name" label="名称" min-width="150" show-overflow-tooltip />
        <el-table-column prop="issuer" label="服务方" width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ row.issuer || '—' }}</template>
        </el-table-column>
        <el-table-column prop="account" label="账号" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">{{ row.account || '—' }}</template>
        </el-table-column>
        <el-table-column label="关联账号" width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.accountName">{{ row.accountName }}</span>
            <span v-else style="color: #909399">未关联</span>
          </template>
        </el-table-column>
        <el-table-column label="存储" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.storage === 'encrypted' ? 'success' : 'warning'">
              {{ row.storage === 'encrypted' ? '密文' : '明文' }}
            </el-tag>
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
        <el-table-column label="操作" width="270" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'vault:reveal'" link type="danger" @click="openCode(row)">出验证码</el-button>
            <el-button v-perm="'vault:reveal'" link type="warning" @click="showURI(row)">导出种子</el-button>
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
        :page-sizes="[20, 50]"
        layout="total, sizes, prev, pager, next"
        style="margin-top: 12px"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑种子' : '新增种子'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如：Jenkins 管理员 2FA" />
        </el-form-item>
        <el-form-item label="种子">
          <el-input v-model="form.secret" type="textarea" :rows="2" placeholder="base32 种子，或直接粘 otpauth://totp/... 地址" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            粘 otpauth 地址时会自动提取服务方与账号。<strong>编辑时留空表示不换绑</strong>
          </el-text>
        </el-form-item>
        <el-form-item label="服务方">
          <el-input v-model="form.issuer" placeholder="验证器里显示的名字，如 OpsOne" />
        </el-form-item>
        <el-form-item label="账号">
          <el-input v-model="form.account" placeholder="如 admin@corp" />
        </el-form-item>
        <el-form-item label="关联账号">
          <el-select v-model="form.accountId" clearable style="width: 100%" placeholder="可关联到密码库条目">
            <el-option :value="0" label="不关联" />
            <el-option v-for="a in accounts" :key="a.id" :value="a.id" :label="`${a.name}（${a.username}）`" />
          </el-select>
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

    <el-dialog v-model="codeVisible" title="出验证码" width="480px" @closed="stopTimer">
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>本次出码会被记入留痕</template>
      </el-alert>
      <el-form label-width="80px">
        <el-form-item label="条目">
          <span>{{ codeRow?.name }}</span>
        </el-form-item>
        <el-form-item label="用途">
          <el-input v-model="codeReason" placeholder="选填，如：帮同事登录控制台" />
        </el-form-item>
      </el-form>

      <div v-if="codeResult" style="text-align: center">
        <div style="font-size: 34px; letter-spacing: 6px; font-family: monospace">
          {{ codeResult.code }}
        </div>
        <el-button link type="primary" @click="copyText(codeResult.code)">复制</el-button>
        <el-progress
          :percentage="Math.round((remain / codeResult.period) * 100)"
          :show-text="false"
          :status="remain <= 5 ? 'exception' : undefined"
          style="margin: 8px 0"
        />
        <div v-if="remain > 0" style="color: #909399">还剩 {{ remain }} 秒</div>
        <div v-else style="color: #f56c6c">已过期，请重新取一次</div>
        <div v-if="codeResult.nextCode" style="margin-top: 6px">
          下一个码：<code>{{ codeResult.nextCode }}</code>
        </div>
        <el-text type="info" size="small" style="display: block; margin-top: 10px">
          服务器时间 {{ codeResult.serverTime }} —— {{ codeResult.note }}
        </el-text>
      </div>

      <template #footer>
        <el-button @click="codeVisible = false">关闭</el-button>
        <el-button type="danger" :loading="coding" @click="doCode">
          {{ codeResult ? '再取一次' : '取验证码' }}
        </el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="uriVisible" title="otpauth 地址" width="600px">
      <el-alert type="error" :closable="false" style="margin-bottom: 12px">
        <template #title>这串地址里含种子原文，拿到它等于拿到第二因子本身</template>
      </el-alert>
      <el-input v-model="uriText" type="textarea" :rows="3" readonly />
      <el-text type="info" size="small" style="display: block; margin-top: 8px">
        把它贴进验证器的「手动添加 / 导入链接」即可。平台不生成二维码图片 ——
        那需要引一个绘图库，而链接本身已经够用。
      </el-text>
      <template #footer>
        <el-button @click="uriVisible = false">关闭</el-button>
        <el-button type="primary" @click="copyText(uriText)">复制</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="accessVisible" title="取用留痕" size="55%">
      <el-table :data="accessRows" border stripe empty-text="还没有取用记录">
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
        <el-table-column prop="operator" label="操作人" width="110" />
        <el-table-column label="动作" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="row.action === 'uri' ? 'danger' : 'warning'">
              {{ row.action === 'uri' ? '导出种子' : '出验证码' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="targetName" label="条目" min-width="140" show-overflow-tooltip />
        <el-table-column prop="ip" label="来源 IP" width="130" />
        <el-table-column prop="reason" label="用途" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">{{ row.reason || '未填' }}</template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>
