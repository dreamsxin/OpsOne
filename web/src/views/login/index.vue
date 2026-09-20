<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { getBranding } from '@/api'

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

onMounted(async () => {
  try {
    const branding = await getBranding()
    platformName.value = branding.platformName || platformName.value
    loginNotice.value = branding.loginNotice || ''
    document.title = platformName.value
  } catch {
    // 拿不到品牌信息时用内置默认值，不阻塞登录
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
    </el-card>
  </div>
</template>
