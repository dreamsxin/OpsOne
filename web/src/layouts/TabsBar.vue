<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useTabsStore, type TabItem } from '@/stores/tabs'

/**
 * 多页签工作台：顶栏下方一行已打开页面。
 *
 * 交互对齐参考站：点击切换、中键/关闭按钮关闭、右键菜单
 * （刷新 / 固定 / 关闭左侧 / 关闭右侧 / 关闭其他 / 关闭全部）。
 * 关闭当前页签后跳到相邻页签；钉住的页签（首页）不可关闭。
 */
const router = useRouter()
const tabs = useTabsStore()

const barRef = ref<HTMLElement>()

type MenuState = { visible: boolean; x: number; y: number; tab: TabItem | null }
const menu = ref<MenuState>({ visible: false, x: 0, y: 0, tab: null })

const menuItems = computed(() => {
  const tab = menu.value.tab
  if (!tab) return []
  const idx = tabs.tabs.findIndex((t) => t.path === tab.path)
  const left = tabs.tabs.slice(0, idx).filter((t) => !t.affix)
  const right = tabs.tabs.slice(idx + 1).filter((t) => !t.affix)
  const closable = tabs.tabs.some((t) => !t.affix)
  return [
    { key: 'refresh', label: '刷新', disabled: tab.path !== tabs.activePath },
    { key: 'affix', label: tab.affix ? '取消固定' : '固定' },
    { key: 'close', label: '关闭', disabled: tab.affix },
    { key: 'closeLeft', label: '关闭左侧', disabled: !left.length },
    { key: 'closeRight', label: '关闭右侧', disabled: !right.length },
    { key: 'closeOthers', label: '关闭其他', disabled: !left.length && !right.length },
    // 「关闭全部」和左右无关：只剩首页 + 当前页时它也该能把当前页关掉
    { key: 'closeAll', label: '关闭全部', disabled: !closable }
  ]
})

function openMenu(tab: TabItem, e: MouseEvent) {
  e.preventDefault()
  // 菜单宽 140，靠右/靠下右键时做一次视口裁剪，别让它溢出屏幕
  const x = Math.min(e.clientX, window.innerWidth - 150)
  const y = Math.min(e.clientY, window.innerHeight - 220)
  menu.value = { visible: true, x: Math.max(4, x), y: Math.max(4, y), tab }
}

function closeMenu() {
  menu.value.visible = false
  menu.value.tab = null
}

function onMenuAction(key: string) {
  const tab = menu.value.tab
  closeMenu()
  // 菜单开着的时候页签可能已经被别的操作（中键关闭）摘掉了，动手前先确认它还在
  if (!tab || !tabs.tabs.some((t) => t.path === tab.path)) return
  const result = applyMenuAction(key, tab)
  if (result?.wasActive && result.nextPath) go(result.nextPath)
}

function applyMenuAction(key: string, tab: TabItem) {
  switch (key) {
    case 'refresh':
      tabs.refreshTab(tab.path)
      return null
    case 'affix':
      tabs.toggleAffix(tab.path)
      return null
    case 'close':
      return tabs.removeTab(tab.path)
    case 'closeLeft':
      return tabs.closeLeft(tab.path)
    case 'closeRight':
      return tabs.closeRight(tab.path)
    case 'closeOthers':
      return tabs.closeOthers(tab.path)
    case 'closeAll':
      return tabs.closeAll()
    default:
      return null
  }
}

/** 统一跳转入口：异步组件加载失败（发版后旧 chunk 没了）不能变成静默的未捕获异常 */
function go(path: string) {
  router.push(path).catch(() => {
    ElMessage.error('页面加载失败，请刷新后重试')
  })
}

async function onTabClick(tab: TabItem) {
  if (tab.path !== tabs.activePath) go(tab.path)
}

async function onTabClose(tab: TabItem) {
  closeMenu()
  const result = tabs.removeTab(tab.path)
  if (result.wasActive) go(result.nextPath || '/dashboard/overview')
}

/** 中键关闭，浏览器多页签的肌肉记忆 */
function onTabAuxClick(tab: TabItem, e: MouseEvent) {
  if (e.button === 1) {
    e.preventDefault()
    onTabClose(tab)
  }
}

/** 滚轮横向翻页：页签多到溢出时不用去拖滚动条 */
function onWheel(e: WheelEvent) {
  const el = barRef.value
  if (!el) return
  if (el.scrollWidth <= el.clientWidth) return
  e.preventDefault()
  el.scrollLeft += e.deltaY
}

function onGlobalClick() {
  closeMenu()
}

// 右键菜单是 fixed 定位的，页面一滚/窗口一变它就会飘在错位置上，一并关掉；
// Esc 关闭是右键菜单的通用预期
function onEsc(e: KeyboardEvent) {
  if (e.key === 'Escape') closeMenu()
}

onMounted(() => {
  window.addEventListener('click', onGlobalClick)
  window.addEventListener('scroll', closeMenu, true)
  window.addEventListener('resize', closeMenu)
  window.addEventListener('keydown', onEsc)
})
onUnmounted(() => {
  window.removeEventListener('click', onGlobalClick)
  window.removeEventListener('scroll', closeMenu, true)
  window.removeEventListener('resize', closeMenu)
  window.removeEventListener('keydown', onEsc)
})
</script>

<template>
  <div ref="barRef" class="tabs-bar" @wheel="onWheel">
    <div
      v-for="tab in tabs.tabs"
      :key="tab.path"
      class="tabs-bar__item"
      :class="{ 'is-active': tab.path === tabs.activePath }"
      :title="tab.title"
      @click="onTabClick(tab)"
      @contextmenu="openMenu(tab, $event)"
      @auxclick="onTabAuxClick(tab, $event)"
    >
      <span class="tabs-bar__dot" />
      <span class="tabs-bar__title">{{ tab.title }}</span>
      <el-icon v-if="tab.affix" class="tabs-bar__pin" title="已固定"><Star /></el-icon>
      <el-icon
        v-if="!tab.affix"
        class="tabs-bar__close"
        title="关闭"
        @click.stop="onTabClose(tab)"
      >
        <Close />
      </el-icon>
    </div>

    <Teleport to="body">
      <div
        v-if="menu.visible"
        class="tabs-menu"
        :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
        @click.stop
        @contextmenu.prevent
      >
        <div
          v-for="item in menuItems"
          :key="item.key"
          class="tabs-menu__item"
          :class="{ 'is-disabled': item.disabled }"
          @click="!item.disabled && onMenuAction(item.key)"
        >
          {{ item.label }}
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.tabs-bar {
  display: flex;
  align-items: stretch;
  gap: 2px;
  height: 36px;
  /* 父级是 el-container 的纵向 flex：main 内容超高空时不能让页签栏被压缩 */
  flex-shrink: 0;
  box-sizing: border-box;
  padding: 0 8px;
  background: #fff;
  border-bottom: 1px solid var(--ops-border, #e5e7eb);
  overflow-x: auto;
  scrollbar-width: none;
}
.tabs-bar::-webkit-scrollbar {
  display: none;
}
.tabs-bar__item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 180px;
  padding: 0 10px;
  margin: 4px 0;
  border-radius: 6px;
  font-size: 12px;
  color: var(--ops-text-secondary, #6b7280);
  cursor: pointer;
  white-space: nowrap;
  user-select: none;
  flex-shrink: 0;
  transition: background-color 0.15s ease, color 0.15s ease;
}
.tabs-bar__item:hover {
  color: var(--ops-text-primary, #111827);
  background: var(--el-fill-color-light);
}
.tabs-bar__item.is-active {
  color: var(--ops-primary, #2563eb);
  background: var(--el-color-primary-light-9);
  font-weight: 500;
}
.tabs-bar__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--el-fill-color-dark);
  flex-shrink: 0;
}
.is-active .tabs-bar__dot {
  background: var(--ops-primary, #2563eb);
}
.tabs-bar__title {
  overflow: hidden;
  text-overflow: ellipsis;
}
.tabs-bar__pin {
  font-size: 10px;
  color: var(--el-color-warning);
  flex-shrink: 0;
}
.tabs-bar__close {
  font-size: 12px;
  border-radius: 3px;
  flex-shrink: 0;
  color: var(--ops-text-muted, #9ca3af);
}
.tabs-bar__close:hover {
  color: var(--el-color-danger);
  background: var(--el-fill-color-dark);
}
</style>

<style>
/* 右键菜单挂在 body 上，不能用 scoped */
.tabs-menu {
  position: fixed;
  z-index: 3000;
  min-width: 140px;
  padding: 4px;
  background: #fff;
  border: 1px solid var(--ops-border, #e5e7eb);
  border-radius: 6px;
  box-shadow: var(--el-box-shadow-light);
}
.tabs-menu__item {
  padding: 6px 12px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--ops-text-primary, #111827);
  cursor: pointer;
}
.tabs-menu__item:hover:not(.is-disabled) {
  background: var(--el-fill-color-light);
  color: var(--ops-primary, #2563eb);
}
.tabs-menu__item.is-disabled {
  color: var(--ops-text-muted, #9ca3af);
  cursor: not-allowed;
}
</style>
