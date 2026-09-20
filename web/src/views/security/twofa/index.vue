<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import QRCode from 'qrcode'
import { useUserStore } from '@/stores/user'
import {
  confirmMyTOTP,
  disableMyTOTP,
  getMyTOTP,
  listUsers,
  resetUserTOTP,
  setupMyTOTP,
  type TOTPSetup,
  type TOTPStatus,
  type User
} from '@/api'

const store = useUserStore()

const status = ref<TOTPStatus | null>(null)
const setup = ref<TOTPSetup | null>(null)
const qrDataUrl = ref('')
const confirmCode = ref('')
const binding = ref(false)

const disableVisible = ref(false)
const disableForm = ref({ password: '', code: '' })

const users = ref<User[]>([])
const usersLoading = ref(false)

async function loadStatus() {
  status.value = await getMyTOTP()
}

async function startBind() {
  binding.value = true
  try {
    setup.value = await setupMyTOTP()
    qrDataUrl.value = await QRCode.toDataURL(setup.value.uri, { width: 200, margin: 1 })
    confirmCode.value = ''
  } finally {
    binding.value = false
  }
}

async function confirmBind() {
  if (confirmCode.value.length !== 6) {
    ElMessage.warning('请输入 6 位验证码')
    return
  }
  await confirmMyTOTP(confirmCode.value)
  ElMessage.success('绑定成功，下次登录需要输入动态验证码')
  setup.value = null
  qrDataUrl.value = ''
  await loadStatus()
  // 强制模式下绑定完才放行业务接口，刷新一次资料以解除跳转
  await store.loadProfile()
}

async function submitDisable() {
  await disableMyTOTP(disableForm.value.password, disableForm.value.code)
  ElMessage.success('已解绑')
  disableVisible.value = false
  disableForm.value = { password: '', code: '' }
  await loadStatus()
  await store.loadProfile()
}

async function copySecret() {
  if (!setup.value) return
  try {
    await navigator.clipboard.writeText(setup.value.secret)
    ElMessage.success('已复制密钥')
  } catch {
    ElMessage.warning('浏览器拒绝了剪贴板访问，请手动选中复制')
  }
}

async function loadUsers() {
  usersLoading.value = true
  try {
    const data = await listUsers({ page: 1, pageSize: 100 })
    users.value = data.list || []
  } finally {
    usersLoading.value = false
  }
}

async function resetOne(row: User) {
  await ElMessageBox.confirm(
    `确认重置「${row.username}」的双因子绑定？重置后该账号下次登录不再需要验证码，需要重新绑定。`,
    '提示',
    { type: 'warning' }
  )
  await resetUserTOTP(row.id)
  ElMessage.success('已重置')
  loadUsers()
  if (row.id === store.profile?.id) {
    await loadStatus()
    await store.loadProfile()
  }
}

onMounted(() => {
  loadStatus()
  if (store.has('totp:reset')) {
    loadUsers()
  }
})
</script>

<template>
  <div class="page">
    <el-alert
      v-if="store.needBindTotp"
      type="warning"
      :closable="false"
      style="margin-bottom: 12px"
      title="平台已强制启用双因子口令，完成绑定后才能访问其他功能（后端会直接拒绝未绑定账号的业务请求）"
    />

    <el-card>
      <template #header>
        <span>我的双因子口令</span>
        <el-tag
          size="small"
          style="margin-left: 8px"
          :type="status?.enabled ? 'success' : 'info'"
        >
          {{ status?.enabled ? '已绑定' : '未绑定' }}
        </el-tag>
        <el-tag v-if="status" size="small" style="margin-left: 6px">
          策略：{{ status.mode === 'required' ? '强制绑定' : '自愿绑定' }}
        </el-tag>
      </template>

      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="标准 TOTP（SHA1 / 6 位 / 30 秒），可用 Google Authenticator、Microsoft Authenticator、1Password 等任意验证器。平台不提供恢复码，手机丢失只能由持「重置他人绑定」权限的管理员重置。"
      />

      <el-descriptions v-if="status?.enabled" :column="1" border>
        <el-descriptions-item label="绑定时间">{{ status.boundAt || '-' }}</el-descriptions-item>
        <el-descriptions-item label="登录要求">口令 + 动态验证码，验证码在同一时间窗内不可重复使用</el-descriptions-item>
      </el-descriptions>

      <div v-if="status?.enabled" style="margin-top: 12px">
        <el-button type="danger" plain :disabled="status.mode === 'required'" @click="disableVisible = true">
          解绑
        </el-button>
        <span v-if="status.mode === 'required'" style="margin-left: 8px; color: #6b7280">
          强制模式下不能自行解绑
        </span>
      </div>

      <template v-else>
        <div v-if="!setup">
          <el-button type="primary" :loading="binding" @click="startBind">开始绑定</el-button>
          <span v-if="status?.pending" style="margin-left: 8px; color: #d97706">
            上次绑定未完成，重新开始会生成新的密钥
          </span>
        </div>

        <div v-else>
          <el-steps :active="1" simple style="margin-bottom: 16px">
            <el-step title="扫码或手动录入" />
            <el-step title="输入验证码确认" />
          </el-steps>

          <div style="display: flex; gap: 20px; align-items: flex-start">
            <img v-if="qrDataUrl" :src="qrDataUrl" alt="TOTP 二维码" style="border: 1px solid #e5e7eb" />
            <div style="flex: 1">
              <div style="margin-bottom: 8px; color: #6b7280">扫不了码时手动录入密钥：</div>
              <el-input :model-value="setup.secret" readonly>
                <template #append>
                  <el-button @click="copySecret">复制</el-button>
                </template>
              </el-input>
              <div style="margin: 12px 0 8px; color: #6b7280">
                算法 SHA1 · {{ setup.digits }} 位 · {{ setup.period }} 秒
              </div>
              <el-input
                v-model="confirmCode"
                placeholder="输入验证器上的 6 位验证码"
                maxlength="6"
                style="max-width: 220px"
                @keyup.enter="confirmBind"
              />
              <div style="margin-top: 12px">
                <el-button type="primary" @click="confirmBind">确认绑定</el-button>
                <el-button @click="((setup = null), (qrDataUrl = ''))">取消</el-button>
              </div>
            </div>
          </div>
        </div>
      </template>
    </el-card>

    <el-card v-perm="'totp:reset'" style="margin-top: 12px">
      <template #header>
        <span>账号绑定情况</span>
        <el-button link type="primary" style="margin-left: 8px" @click="loadUsers">刷新</el-button>
      </template>

      <el-table v-loading="usersLoading" :data="users" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="username" label="账号" min-width="120" />
        <el-table-column prop="nickname" label="昵称" min-width="120" />
        <el-table-column label="双因子" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.totpEnabled ? 'success' : 'info'">
              {{ row.totpEnabled ? '已绑定' : '未绑定' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="totpBoundAt" label="绑定时间" min-width="180" />
        <el-table-column prop="lastLoginAt" label="最近登录" min-width="180" />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button link type="danger" :disabled="!row.totpEnabled" @click="resetOne(row)">
              重置
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="disableVisible" title="解绑双因子口令" width="420px">
      <el-alert
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="需要同时提供登录口令与当前验证码，避免会话被劫持后被直接关掉双因子"
      />
      <el-form label-width="90px">
        <el-form-item label="登录口令">
          <el-input v-model="disableForm.password" type="password" show-password />
        </el-form-item>
        <el-form-item label="验证码">
          <el-input v-model="disableForm.code" maxlength="6" placeholder="6 位动态验证码" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="disableVisible = false">取消</el-button>
        <el-button type="danger" @click="submitDisable">确认解绑</el-button>
      </template>
    </el-dialog>
  </div>
</template>
