<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { useTabsStore } from '@/stores/tabs'
import {
  changePassword,
  getBranding,
  getMessageSummary,
  readAllMessages,
  readMessage,
  type MenuNode,
  type MessageSummary
} from '@/api'
import MenuTree from './MenuTree.vue'
import TabsBar from './TabsBar.vue'
import CommandPalette from '@/components/CommandPalette.vue'



const store = useUserStore()
const tabs = useTabsStore()
const route = useRoute()
const router = useRouter()

const collapse = ref(false)
const pwdVisible = ref(false)
const pwdFormRef = ref<FormInstance>()
const pwdForm = ref({ oldPassword: '', newPassword: '', confirm: '' })


const summary = ref<MessageSummary>({ unread: 0, unreadAlert: 0, latest: [] })
const platformName = ref('OpsOne 运维平台')

// 铃铛下拉里的分类：全部 / 告警 / 公告。latest 是后端给的一小撮未读，
// 直接在前端按类型切，不为此加接口。
const bellTab = ref<'all' | 'alert' | 'announcement'>('all')
const bellItems = computed(() =>
  summary.value.latest.filter((m) => (bellTab.value === 'all' ? true : m.type === bellTab.value))
)

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

async function markRead(id: number) {
  try {
    await readMessage(id)
    await loadSummary()
  } catch {
    // 标已读失败不提示，下次打开铃铛会再拉一次
  }
}

async function markAllRead() {
  try {
    const res = await readAllMessages()
    await loadSummary()
    ElMessage.success(`已将 ${res.updated} 条消息标为已读`)
  } catch {
    // 拦截器已经弹过错误提示，这里只是别让它变成未捕获的 rejection
  }
}


const visibleMenus = computed(() => store.menus.filter((m) => !m.hidden))
const activeMenu = computed(() => route.path)

const paletteRef = ref<{ open: () => void }>()

// Ctrl/Cmd+K 打开命令面板。终端页要放行：xterm 的 textarea 已经把 Ctrl+K
// （readline 的 kill-to-EOL）发给远端 shell 了，这里再 preventDefault 也收不回，
// 结果会是「命令被截断 + 面板弹出来挡住终端」，所以来自终端的按键直接不接。
function onSearchHotkey(e: KeyboardEvent) {
  if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== 'k') return
  const el = e.target as HTMLElement | null
  if (el?.closest?.('.xterm')) return
  e.preventDefault()
  paletteRef.value?.open()
}


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
  window.addEventListener('keydown', onSearchHotkey)
})

onUnmounted(() => {
  window.removeEventListener('keydown', onSearchHotkey)
})

// 路由变化即登记页签；keep-alive 只缓存开着的页签，关页签即释放缓存
// 注意监听 fullPath 而不是 route 对象本身：useRoute 返回的是同一个响应式代理，
// watch 的 getter 每次返回同一引用，永远不会触发。用 fullPath 而不是 path 是因为
// 页签按 fullPath 区分（带不同 query 的同一路由是两个页签）
watch(
  () => route.fullPath,
  () => tabs.addTab(route),
  { immediate: true }
)

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
          <MenuTree :items="[menu]" />
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
        <!-- 命令面板入口：搜菜单之外还能搜主机/告警/脚本，详见 components/CommandPalette.vue -->
        <el-button class="cmd-entry" @click="paletteRef?.open()">
          <el-icon style="margin-right: 4px"><Search /></el-icon>
          搜索
          <span class="cmd-kbd">Ctrl K</span>
        </el-button>
        <el-dropdown trigger="click" @visible-change="onBellToggle">


          <el-badge :value="summary.unread" :hidden="summary.unread === 0" :max="99">
            <el-icon size="18" style="cursor: pointer; vertical-align: middle"><Bell /></el-icon>
          </el-badge>
          <template #dropdown>
            <div style="width: 320px; padding: 8px 12px">
              <div style="display: flex; align-items: center; margin-bottom: 6px">
                <strong>未读消息 {{ summary.unread }}</strong>
                <el-button link type="primary" style="margin-left: auto" @click="gotoInbox">
                  查看全部
                </el-button>
              </div>
              <el-radio-group v-model="bellTab" size="small" style="margin-bottom: 4px">
                <el-radio-button value="all">全部</el-radio-button>
                <el-radio-button value="alert">告警 {{ summary.unreadAlert }}</el-radio-button>
                <el-radio-button value="announcement">公告</el-radio-button>
              </el-radio-group>
              <el-empty v-if="!bellItems.length" description="没有未读消息" :image-size="50" />
              <div
                v-for="item in bellItems"
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
                  <el-icon
                    style="margin-left: auto; color: #9ca3af; flex-shrink: 0"
                    title="标为已读"
                    @click.stop="markRead(item.id)"
                  >
                    <Check />
                  </el-icon>
                </div>
              </div>
              <div
                v-if="bellItems.length"
                style="display: flex; justify-content: center; padding-top: 8px; border-top: 1px solid #f0f0f0; margin-top: 4px"
              >
                <el-button link size="small" @click="markAllRead">全部标为已读</el-button>
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

      <TabsBar />

      <el-main style="padding: 0; overflow: auto">
        <!--
          router-view 的插槽给的 Component 已经是 vnode，<component :is> 会再克隆一份。
          这里绝对不能在 keep-alive 里给它套 v-if：v-if 分支换出去的那份仍带着
          keep-alive 的 SHOULD_KEEP_ALIVE 标记，却由 router-view 自己卸载，
          运行时会抛 parentComponent.ctx.deactivate is not a function，
          结果就是地址栏变了、页面不换（切页签像失灵）。
          强制刷新改成换 key：换 key 让实例重建，同时 cachedNames 短暂摘掉该页
          以清掉旧缓存条目。
        -->
        <router-view v-slot="{ Component }">
          <keep-alive :include="tabs.cachedNames" :max="12">
            <component :is="Component" :key="tabs.keyOf(route.fullPath)" />
          </keep-alive>
        </router-view>
      </el-main>
    </el-container>
  </el-container>

  <CommandPalette ref="paletteRef" />

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
