<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { getBranding, getImAuthorizeUrl, listImLoginProviders, type ImLoginProvider } from '@/api'
import { takeImLoginResult } from '@/utils/imLogin'

const store = useUserStore()
const router = useRouter()
const route = useRoute()

const formRef = ref<FormInstance>()
const loading = ref(false)
const form = ref({ username: '', password: '', code: '' })
// 账号绑定了双因子时，后端会先要求补验证码，再提交一次
const needCode = ref(false)

// 平台名称与登录提示来自系统配置，支持不改代码做白标
const platformName = ref('OpsOne 一体化运维平台')
const loginNotice = ref('')

// IM 扫码登录
const imProviders = ref<ImLoginProvider[]>([])
const imLoading = ref(false)
const providerLabel: Record<string, string> = {
  wecom: '企业微信',
  dingtalk: '钉钉',
  feishu: '飞书'
}

async function finishImLogin(ticket: string) {
  imLoading.value = true
  try {
    await store.loginByImTicket(ticket)
    ElMessage.success('已通过 IM 登录')
    router.replace((route.query.redirect as string) || '/dashboard/overview')
  } catch (err: any) {
    ElMessage.error(err?.message || '扫码登录失败，请重试')
  } finally {
    imLoading.value = false
  }
}

async function startImLogin(item: ImLoginProvider) {
  imLoading.value = true
  try {
    const res = await getImAuthorizeUrl(item.id)
    // 整页跳到厂商的扫码页，回调会把浏览器送回来
    window.location.href = res.authorizeUrl
  } catch (err: any) {
    ElMessage.error(err?.message || '发起扫码登录失败')
    imLoading.value = false
  }
}

onMounted(async () => {
  try {
    const branding = await getBranding()
    platformName.value = branding.platformName || platformName.value
    loginNotice.value = branding.loginNotice || ''
    document.title = platformName.value
  } catch {
    // 拿不到品牌信息时用内置默认值，不阻塞登录
  }
  try {
    imProviders.value = await listImLoginProviders()
  } catch {
    // 没配 IM 或接口不可用时就只显示口令登录
  }

  // ticket 已在 main.ts 里于路由守卫之前取下来存好，这里取出即用（取出即清除）
  const { ticket, error: imError } = takeImLoginResult()
  if (imError) {
    ElMessage.error(imError)
  } else if (ticket) {
    finishImLogin(ticket)
  }
})


const rules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    const res = await store.login(form.value.username, form.value.password, form.value.code)
    if (res.totpRequired) {
      needCode.value = true
      ElMessage.warning(res.detail || '请输入动态验证码')
      form.value.code = ''
      return
    }
    ElMessage.success('登录成功')
    const redirect = (route.query.redirect as string) || '/dashboard/overview'

    router.replace(redirect)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div
    style="
      height: 100%;
      display: flex;
      align-items: center;
      justify-content: center;
      background: linear-gradient(135deg, #1f2937, #2563eb);
    "
  >
    <el-card style="width: 380px; padding: 8px">
      <h2 style="text-align: center; margin: 8px 0 20px">{{ platformName }}</h2>
      <el-alert
        v-if="loginNotice"
        type="info"
        :closable="false"
        style="margin-bottom: 16px"
        :title="loginNotice"
      />


      <el-form ref="formRef" :model="form" :rules="rules" @keyup.enter="submit">
        <el-form-item prop="username">
          <el-input v-model="form.username" placeholder="用户名" size="large" :prefix-icon="'User'" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="密码"
            size="large"
            show-password
            :prefix-icon="'Lock'"
          />
        </el-form-item>
        <el-form-item v-if="needCode" prop="code">
          <el-input
            v-model="form.code"
            placeholder="验证器 6 位动态验证码"
            size="large"
            maxlength="6"
            :prefix-icon="'Key'"
          />
        </el-form-item>
        <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="submit">
          登录
        </el-button>
      </el-form>

      <template v-if="imProviders.length">
        <el-divider>
          <span style="color: var(--el-text-color-secondary); font-size: 12px">IM 扫码登录</span>
        </el-divider>
        <div style="display: flex; flex-direction: column; gap: 8px">
          <el-button
            v-for="item in imProviders"
            :key="item.id"
            size="large"
            :loading="imLoading"
            @click="startImLogin(item)"
          >
            用{{ providerLabel[item.provider] || item.provider }}扫码登录（{{ item.name }}）
          </el-button>
        </div>
        <div style="color: var(--el-text-color-secondary); font-size: 12px; margin-top: 8px">
          只有已经完成组织同步、绑定过平台账号的成员才能扫码进入。
        </div>
      </template>
    </el-card>
  </div>
</template>
