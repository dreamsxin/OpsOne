import { defineStore } from 'pinia'
import type { RouteLocationNormalizedLoaded } from 'vue-router'

/**
 * 多页签工作台状态。
 *
 * 页签的生命周期与 keep-alive 缓存绑定：tabs 里还开着的页面才允许被缓存
 * （BasicLayout 把 cachedNames 传给 keep-alive 的 include），关页签即释放缓存，
 * 避免「关了半天页面其实还占着内存」的老问题。
 *
 * 页签唯一标识用 fullPath 而不是 path：仪表盘点「离线主机」跳的是
 * /asset/host?status=offline，告警详情跳的是 /monitor/logs?q=xxx，
 * 只按 path 认页签的话这类带参数的跳转会命中已开的页签、复用缓存实例，
 * 表现为「地址栏变了、筛选没生效」。带不同参数就是不同页签。
 *
 * 组件名由 router 注册时按路由名注入（见 router/index.ts 的 namedLoader），
 * 这里只存字符串，不依赖组件实例。
 */
export interface TabItem {
  /** 完整路径（含 query），作为页签唯一标识 */
  path: string
  title: string
  /** keep-alive include 用的组件名，与路由 name 一致 */
  name: string
  /** 钉住的页签不可关闭（首页默认钉住） */
  affix?: boolean
}

/** 关闭页签后跳到哪，由调用方（BasicLayout）执行真正的跳转 */
export interface CloseResult {
  /** 关闭的是否是当前激活页签 */
  wasActive: boolean
  /** wasActive 时建议跳转的相邻页签路径 */
  nextPath?: string
}

const HOME_PATH = '/dashboard/overview'

/**
 * 页签上限。超了就挤掉最左边那个「非固定且不是当前页」的页签——
 * 每个开着的页签都是一个活着的组件实例（终端带 WebSocket、监控页带 echarts、
 * k8s 页带轮询），内网平台一天不刷新，无上限就是纯粹的内存与 CPU 累积。
 */
const MAX_TABS = 20

function normalizeName(route: RouteLocationNormalizedLoaded): string {
  // 动态路由的 name 即菜单 name 或路径；静态路由（如 /security/twofa）也有 name
  return String(route.name || route.path)
}

/**
 * 同一路由带不同 query 会开出多个页签，光有菜单标题分不清谁是谁，
 * 这里把第一个有值的参数拼到标题后面（如「告警列表·critical」）。
 */
function tabTitle(route: RouteLocationNormalizedLoaded): string {
  const base = String(route.meta.title || route.path)
  const first = Object.values(route.query).find((v) => v !== '' && v !== null && v !== undefined)
  if (first === undefined) return base
  const text = String(Array.isArray(first) ? first[0] : first)
  return `${base}·${text.length > 12 ? `${text.slice(0, 12)}…` : text}`
}

export const useTabsStore = defineStore('tabs', {
  state: () => ({
    tabs: [] as TabItem[],
    activePath: '',
    /** 正在强制刷新的页签：短暂摘出 cachedNames 以清掉旧缓存条目 */
    refreshingPath: '',
    /** 每个页签的刷新计数，进 keep-alive 的 key，加一即重建实例 */
    refreshSeq: {} as Record<string, number>
  }),
  getters: {
    /**
     * keep-alive 的 include 列表：开着且不在刷新中的页签才有缓存资格。
     * 注意 include 是按组件名匹配的，同一路由的多个 query 变体共用一个 name，
     * 所以缓存条目数由 keep-alive 的 :max 兜底，不完全由这里决定。
     */
    cachedNames(state): string[] {
      const names = state.tabs
        .filter((t) => t.path !== state.refreshingPath)
        .map((t) => t.name)
      return [...new Set(names)]
    },
    /** keep-alive 里 <component> 的 key：fullPath + 刷新计数 */
    keyOf(state) {
      return (path: string) => `${path}#${state.refreshSeq[path] || 0}`
    }
  },
  actions: {
    /** 路由变化时调用；首页始终固定在第一个 */
    addTab(route: RouteLocationNormalizedLoaded) {
      if (!route.path || route.path === '/login' || route.meta.public) return
      const key = route.fullPath
      this.activePath = key
      if (key === HOME_PATH) {
        if (!this.tabs.some((t) => t.path === HOME_PATH)) {
          this.tabs.unshift({
            path: HOME_PATH,
            title: String(route.meta.title || '仪表盘'),
            name: normalizeName(route),
            affix: true
          })
        }
        return
      }
      if (!this.tabs.some((t) => t.path === key)) {
        this.tabs.push({
          path: key,
          title: tabTitle(route),
          name: normalizeName(route)
        })
        this.evictOverflow()
      }
    },
    /** 超出上限时从左往右挤掉第一个可关闭且非当前页的页签 */
    evictOverflow() {
      while (this.tabs.length > MAX_TABS) {
        const idx = this.tabs.findIndex((t) => !t.affix && t.path !== this.activePath)
        if (idx < 0) return
        delete this.refreshSeq[this.tabs[idx].path]
        this.tabs.splice(idx, 1)
      }
    },
    /** 返回相邻页签（优先右侧，其次左侧），供关闭当前页签后跳转 */
    neighborOf(path: string): TabItem | undefined {
      const idx = this.tabs.findIndex((t) => t.path === path)
      if (idx < 0) return undefined
      return this.tabs[idx + 1] || this.tabs[idx - 1]
    },
    removeTab(path: string): CloseResult {
      const idx = this.tabs.findIndex((t) => t.path === path)
      if (idx < 0) return { wasActive: false }
      if (this.tabs[idx].affix) return { wasActive: false }
      this.tabs.splice(idx, 1)
      delete this.refreshSeq[path]
      const wasActive = this.activePath === path
      // 关闭的是当前页签时跳到相邻页签：优先右侧（即 splice 后落到 idx 位置的），其次左侧
      const next = wasActive ? this.tabs[idx] || this.tabs[idx - 1] : undefined
      return { wasActive, nextPath: next?.path }
    },
    closeOthers(path: string): CloseResult {
      const keep = this.tabs.filter((t) => t.affix || t.path === path)
      this.dropSeqExcept(keep)
      this.tabs = keep
      const wasActive = !keep.some((t) => t.path === this.activePath)
      return {
        wasActive,
        nextPath: wasActive ? keep.find((t) => t.path === path)?.path || HOME_PATH : undefined
      }
    },
    closeLeft(path: string): CloseResult {
      const idx = this.tabs.findIndex((t) => t.path === path)
      // 右键菜单里存着的页签可能已经被别的操作（中键关闭）摘掉了，
      // 没有这个守卫时 idx=-1 会让下面的 filter 把所有可关页签一起清掉
      if (idx < 0) return { wasActive: false }
      const removed = this.tabs.slice(0, idx).filter((t) => !t.affix)
      if (!removed.length) return { wasActive: false }
      const keep = this.tabs.filter((t, i) => i >= idx || t.affix)
      this.dropSeqExcept(keep)
      this.tabs = keep
      const wasActive = removed.some((t) => t.path === this.activePath)
      return { wasActive, nextPath: wasActive ? path : undefined }
    },
    closeRight(path: string): CloseResult {
      const idx = this.tabs.findIndex((t) => t.path === path)
      if (idx < 0) return { wasActive: false }
      const removed = this.tabs.slice(idx + 1).filter((t) => !t.affix)
      if (!removed.length) return { wasActive: false }
      const keep = this.tabs.filter((t, i) => i <= idx || t.affix)
      this.dropSeqExcept(keep)
      this.tabs = keep
      const wasActive = removed.some((t) => t.path === this.activePath)
      return { wasActive, nextPath: wasActive ? path : undefined }
    },
    closeAll(): CloseResult {
      const keep = this.tabs.filter((t) => t.affix)
      this.dropSeqExcept(keep)
      this.tabs = keep
      const wasActive = !keep.some((t) => t.path === this.activePath)
      return { wasActive, nextPath: wasActive ? keep[0]?.path || HOME_PATH : undefined }
    },
    /** 批量关闭后清掉已经没有页签对应的刷新计数，避免这个 map 只增不减 */
    dropSeqExcept(keep: TabItem[]) {
      const alive = new Set(keep.map((t) => t.path))
      for (const path of Object.keys(this.refreshSeq)) {
        if (!alive.has(path)) delete this.refreshSeq[path]
      }
    },
    toggleAffix(path: string) {
      const tab = this.tabs.find((t) => t.path === path)
      if (tab) tab.affix = !tab.affix
    },
    /** 刷新页签：换 key 重建实例，并短暂摘出缓存名单以丢掉旧缓存 */
    refreshTab(path: string) {
      this.refreshSeq[path] = (this.refreshSeq[path] || 0) + 1
      this.refreshingPath = path
      setTimeout(() => {
        if (this.refreshingPath === path) this.refreshingPath = ''
      }, 300)
    },
    logout() {
      this.tabs = []
      this.activePath = ''
      this.refreshingPath = ''
      this.refreshSeq = {}
    }
  }
})
