<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { changePassword, getBranding, getMessageSummary, type MenuNode, type MessageSummary } from '@/api'


const store = useUserStore()
const route = useRoute()
const router = useRouter()

const collapse = ref(false)
const pwdVisible = ref(false)
const pwdFormRef = ref<FormInstance>()
const pwdForm = ref({ oldPassword: '', newPassword: '', confirm: '' })

const summary = ref<MessageSummary>({ unread: 0, unreadAlert: 0, latest: [] })
const platformName = ref('OpsOne 运维平台')

async function loadBranding() {
  try {
    const branding = await getBranding()
    if (branding.platformName) {
      platformName.value = branding.platformName
      document.title = branding.platformName
    }
  } catch {
    // 品牌信息拿不到时保留默认名称
  }
}


async function loadSummary() {
  try {
    summary.value = await getMessageSummary()
  } catch {
    // 未读汇总失败不影响主界面
  }
}

// 打开铃铛时刷新，避免常驻轮询
function onBellToggle(visible: boolean) {
  if (visible) loadSummary()
}

function gotoInbox() {
  router.push('/message/inbox')
}


const visibleMenus = computed(() => store.menus.filter((m) => !m.hidden))
const activeMenu = computed(() => route.path)
const breadcrumbs = computed(() => {
  const trail: string[] = []
  const walk = (nodes: MenuNode[], parents: string[]) => {
    for (const node of nodes) {
      const chain = [...parents, node.title]
      if (node.path === route.path) {
        trail.push(...chain)
        return true
      }
      if (node.children && walk(node.children, chain)) return true
    }
    return false
  }
  walk(store.menus, [])
  return trail
})

const pwdRules = {
  oldPassword: [{ required: true, message: '请输入原密码', trigger: 'blur' }],
  newPassword: [{ required: true, min: 8, message: '新密码至少 8 位', trigger: 'blur' }],
  confirm: [
    {
      validator: (_r: unknown, value: string, cb: (e?: Error) => void) =>
        value === pwdForm.value.newPassword ? cb() : cb(new Error('两次输入的密码不一致')),
      trigger: 'blur'
    }
  ]
}

async function submitPassword() {
  const valid = await pwdFormRef.value?.validate().catch(() => false)
  if (!valid) return
  await changePassword(pwdForm.value.oldPassword, pwdForm.value.newPassword)
  ElMessage.success('密码已修改，请重新登录')
  pwdVisible.value = false
  store.logout()
  router.push('/login')
}

async function handleLogout() {
  await ElMessageBox.confirm('确认退出登录？', '提示', { type: 'warning' })
  store.logout()
  router.push('/login')
}

onMounted(() => {
  loadSummary()
  loadBranding()
})

</script>


<template>
  <el-container style="height: 100%">
    <el-aside :width="collapse ? '64px' : '220px'" style="background: var(--ops-sidebar-bg)">
      <div
        style="
          height: 56px;
          display: flex;
          align-items: center;
          justify-content: center;
          color: #fff;
          font-weight: 600;
          letter-spacing: 1px;
        "
      >
        {{ collapse ? 'Ops' : platformName }}


      </div>
      <el-menu
        :default-active="activeMenu"
        :collapse="collapse"
        background-color="#1f2937"
        text-color="#cbd5e1"
        active-text-color="#ffffff"
        router
        unique-opened
      >
        <template v-for="menu in visibleMenus" :key="menu.id">
          <el-sub-menu v-if="menu.children?.length" :index="menu.path">
            <template #title>
              <el-icon v-if="menu.icon"><component :is="menu.icon" /></el-icon>
              <span>{{ menu.title }}</span>
            </template>
            <el-menu-item
              v-for="child in menu.children.filter((c) => !c.hidden)"
              :key="child.id"
              :index="child.path"
            >
              <el-icon v-if="child.icon"><component :is="child.icon" /></el-icon>
              <span>{{ child.title }}</span>
            </el-menu-item>
          </el-sub-menu>
          <el-menu-item v-else :index="menu.path">
            <el-icon v-if="menu.icon"><component :is="menu.icon" /></el-icon>
            <template #title>{{ menu.title }}</template>
          </el-menu-item>
        </template>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header
        style="
          height: 56px;
          background: #fff;
          border-bottom: 1px solid #e5e7eb;
          display: flex;
          align-items: center;
          gap: 12px;
        "
      >
        <el-button text @click="collapse = !collapse">
          <el-icon><Fold v-if="!collapse" /><Expand v-else /></el-icon>
        </el-button>
        <el-breadcrumb separator="/">
          <el-breadcrumb-item v-for="(item, idx) in breadcrumbs" :key="idx">
            {{ item }}
          </el-breadcrumb-item>
        </el-breadcrumb>
        <div style="flex: 1"></div>
        <el-dropdown trigger="click" @visible-change="onBellToggle">
          <el-badge :value="summary.unread" :hidden="summary.unread === 0" :max="99">
            <el-icon size="18" style="cursor: pointer; vertical-align: middle"><Bell /></el-icon>
          </el-badge>
          <template #dropdown>
            <div style="width: 300px; padding: 8px 12px">
              <div style="display: flex; align-items: center; margin-bottom: 6px">
                <strong>未读消息 {{ summary.unread }}</strong>
                <el-button link type="primary" style="margin-left: auto" @click="gotoInbox">
                  查看全部
                </el-button>
              </div>
              <el-empty v-if="!summary.latest.length" description="没有未读消息" :image-size="50" />
              <div
                v-for="item in summary.latest"
                :key="item.id"
                style="padding: 6px 0; border-top: 1px solid #f0f0f0; cursor: pointer"
                @click="gotoInbox"
              >
                <div style="display: flex; gap: 6px; align-items: center">
                  <el-tag size="small" :type="item.level === 'critical' ? 'danger' : 'info'">
                    {{ item.type === 'alert' ? '告警' : '公告' }}
                  </el-tag>
                  <span style="font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap">
                    {{ item.title }}
                  </span>
                </div>
              </div>
            </div>
          </template>
        </el-dropdown>
        <el-dropdown>

          <span style="cursor: pointer; display: flex; align-items: center; gap: 6px">
            <el-icon><UserFilled /></el-icon>
            {{ store.displayName }}
          </span>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="pwdVisible = true">修改密码</el-dropdown-item>
              <el-dropdown-item divided @click="handleLogout">退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </el-header>

      <el-main style="padding: 0; overflow: auto">
        <router-view v-slot="{ Component }">
          <keep-alive :max="8">
            <component :is="Component" />
          </keep-alive>
        </router-view>
      </el-main>
    </el-container>
  </el-container>

  <el-dialog v-model="pwdVisible" title="修改密码" width="420px">
    <el-form ref="pwdFormRef" :model="pwdForm" :rules="pwdRules" label-width="90px">
      <el-form-item label="原密码" prop="oldPassword">
        <el-input v-model="pwdForm.oldPassword" type="password" show-password />
      </el-form-item>
      <el-form-item label="新密码" prop="newPassword">
        <el-input v-model="pwdForm.newPassword" type="password" show-password />
      </el-form-item>
      <el-form-item label="确认密码" prop="confirm">
        <el-input v-model="pwdForm.confirm" type="password" show-password />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPassword">确定</el-button>
    </template>
  </el-dialog>
</template>
