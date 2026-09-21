<script setup lang="ts">
/**
 * 命令面板（Ctrl+K）。
 *
 * 不只搜菜单：输入后同时去查主机、告警、脚本这三类最常被"找"的业务对象，
 * 命中后直接跳到对应页面并把关键字带过去（列表页按 keyword 过滤）。
 *
 * 刻意的取舍：
 * - 没有后端统一搜索接口，这里是并行调三个现成的列表接口、各取前 5 条。
 *   所以它的能力上限就是那三个接口的过滤能力，不做模糊拼音、不做全文。
 * - 只读：面板只负责"找到并跳过去"，不在这里执行任何动作（下发、确认告警都要去页面上做，
 *   那些地方有二次确认与闸门）。
 * - 只列当前用户路由表里真实存在的页面：三个列表接口没有按菜单授权收口，
 *   没有该菜单的人搜出来也打不开，直接落到 404，不如不列。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { listAlerts, listHosts, listScripts, type MenuNode } from '@/api'
import { useUserStore } from '@/stores/user'

type Hit = {
  group: '菜单' | '主机' | '告警' | '脚本'
  title: string
  desc: string
  path: string
  query?: Record<string, string>
}

const router = useRouter()
const store = useUserStore()

const visible = ref(false)
const keyword = ref('')
const loading = ref(false)
const remoteHits = ref<Hit[]>([])
/** 哪几组查询失败了：失败和"没结果"必须能分辨，否则是在骗用户"系统里没有" */
const failedGroups = ref<string[]>([])
const activeIndex = ref(0)
const inputRef = ref()
const bodyRef = ref<HTMLElement>()

/** 菜单拍平：只保留能打开的页面 */
const flatMenus = computed<Hit[]>(() => {
  const list: Hit[] = []
  const walk = (nodes: MenuNode[], parents: string[]) => {
    for (const node of nodes) {
      if (node.hidden) continue
      const chain = [...parents, node.title]
      if (node.path && !node.children?.length) {
        list.push({ group: '菜单', title: node.title, desc: chain.join(' / '), path: node.path })
      }
      if (node.children?.length) walk(node.children, chain)
    }
  }
  walk(store.menus, [])
  return list
})

const menuHits = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  if (!kw) return flatMenus.value.slice(0, 8)
  return flatMenus.value
    .filter((m) => m.title.toLowerCase().includes(kw) || m.path.toLowerCase().includes(kw))
    .slice(0, 8)
})

const hits = computed<Hit[]>(() => [...menuHits.value, ...remoteHits.value])

let timer: number | undefined
/** 请求序号：debounce 挡不住"已发出的慢请求"，旧结果回来时必须丢掉 */
let seq = 0

/** 当前用户的路由表里有没有这个页面。没有就别列，点了只会落到 404 */
function reachable(path: string) {
  return router.resolve(path).matched.length > 0
}

/** 查业务对象：并行打三个列表接口，各取前 5 条；任一失败只是少一组结果并标注出来 */
async function searchRemote(kw: string) {
  if (kw.length < 2) {
    remoteHits.value = []
    failedGroups.value = []
    return
  }
  const mine = ++seq
  loading.value = true
  const failed: string[] = []
  const [hosts, alerts, scripts] = await Promise.all([
    reachable('/asset/host')
      ? listHosts({ page: 1, pageSize: 5, keyword: kw }).catch(() => {
          failed.push('主机')
          return null
        })
      : null,
    reachable('/monitor/alerts')
      ? listAlerts({ page: 1, pageSize: 5, keyword: kw }).catch(() => {
          failed.push('告警')
          return null
        })
      : null,
    reachable('/execute/script')
      ? listScripts({ page: 1, pageSize: 5, keyword: kw }).catch(() => {
          failed.push('脚本')
          return null
        })
      : null
  ])
  // 输入已经变了（或面板关了又开），这次的结果作废
  if (mine !== seq) return
  loading.value = false
  failedGroups.value = failed

  const list: Hit[] = []
  for (const host of hosts?.list || []) {
    list.push({
      group: '主机',
      title: host.name,
      desc: `${host.address}:${host.port} · ${host.env || '-'} · ${host.status}`,
      path: '/asset/host',
      query: { keyword: host.name }
    })
  }
  for (const alert of alerts?.list || []) {
    list.push({
      group: '告警',
      title: alert.title,
      desc: `${alert.severity} · ${alert.status} · ${alert.sourceName || '-'}`,
      path: '/monitor/alerts',
      query: { keyword: alert.title }
    })
  }
  for (const script of scripts?.list || []) {
    list.push({
      group: '脚本',
      title: script.name,
      desc: `${script.category || '未分类'} · 风险 ${script.riskLevel}`,
      path: '/execute/script',
      query: { keyword: script.name }
    })
  }
  remoteHits.value = list
  // 结果变少时把高亮夹回范围内，否则 Enter 会取到 undefined
  if (activeIndex.value >= hits.value.length) activeIndex.value = Math.max(0, hits.value.length - 1)
}

watch(keyword, (kw) => {
  activeIndex.value = 0
  window.clearTimeout(timer)
  timer = window.setTimeout(() => searchRemote(kw.trim()), 250)
})

watch(visible, (open) => {
  if (!open) {
    // 关了就别再发请求，也别让已发出的结果写回来
    window.clearTimeout(timer)
    seq++
    loading.value = false
  }
})

// 高亮项滚进视口：列表能滚的时候连按 ↓ 到后面几项，用户得看得见 Enter 会打开哪个
watch(activeIndex, () => {
  nextTick(() => {
    bodyRef.value?.querySelector('.cmd-row.active')?.scrollIntoView({ block: 'nearest' })
  })
})

function open() {
  // 已经开着时再按 Ctrl+K 只重新聚焦，别把用户刚敲的关键字清掉
  if (visible.value) {
    nextTick(() => inputRef.value?.focus?.())
    return
  }
  visible.value = true
  keyword.value = ''
  remoteHits.value = []
  failedGroups.value = []
  activeIndex.value = 0
  nextTick(() => inputRef.value?.focus?.())
}

function go(hit: Hit) {
  visible.value = false
  router.push({ path: hit.path, query: hit.query })
}

function onKeydown(e: KeyboardEvent) {
  if (!hits.value.length) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    activeIndex.value = (activeIndex.value + 1) % hits.value.length
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    activeIndex.value = (activeIndex.value - 1 + hits.value.length) % hits.value.length
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const hit = hits.value[activeIndex.value]
    if (hit) go(hit)
  }
}

onBeforeUnmount(() => window.clearTimeout(timer))

defineExpose({ open })
</script>

<template>
  <el-dialog
    v-model="visible"
    width="620px"
    top="12vh"
    :show-close="false"
    append-to-body
    class="cmd-dialog"
  >
    <template #header>
      <el-input
        ref="inputRef"
        v-model="keyword"
        placeholder="搜菜单、主机、告警、脚本（↑↓ 选择，Enter 打开）"
        size="large"
        clearable
        @keydown="onKeydown"
      >
        <template #prefix><el-icon><Search /></el-icon></template>
        <template #suffix>
          <span v-if="loading" class="cmd-tip">查询中…</span>
        </template>
      </el-input>
    </template>

    <div ref="bodyRef" class="cmd-body">
      <el-alert
        v-if="failedGroups.length"
        type="warning"
        :closable="false"
        show-icon
        style="margin-bottom: 8px"
        :title="`${failedGroups.join('、')}查询失败，下面的结果不完整`"
      />
      <el-empty
        v-if="!hits.length"
        :image-size="60"
        description="没有匹配项（业务对象需输入 2 个字以上）"
      />
      <template v-else>
        <div
          v-for="(hit, idx) in hits"
          :key="`${hit.group}-${hit.title}-${idx}`"
          :class="['cmd-row', { active: idx === activeIndex }]"
          @mouseenter="activeIndex = idx"
          @click="go(hit)"
        >
          <el-tag size="small" disable-transitions>{{ hit.group }}</el-tag>
          <span class="cmd-title">{{ hit.title }}</span>
          <span class="cmd-desc">{{ hit.desc }}</span>
        </div>
      </template>
    </div>

    <template #footer>
      <span class="cmd-tip">
        只负责找到并跳转，不在这里执行动作——下发与告警处置仍然要去对应页面（那里有二次确认与闸门）
      </span>
    </template>
  </el-dialog>
</template>

<style scoped>
.cmd-body {
  max-height: 52vh;
  overflow: auto;
}

.cmd-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
  transition: background-color var(--ops-transition-fast, 0.15s ease);
}

.cmd-row.active {
  background: var(--el-color-primary-light-9);
}

.cmd-title {
  font-size: 14px;
  white-space: nowrap;
}

.cmd-desc {
  margin-left: auto;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 320px;
}

.cmd-tip {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
