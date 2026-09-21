<script setup lang="ts">
/**
 * 防火墙策略。
 *
 * 这一页的设计前提是「真机才是事实」：进来先选主机，点「现读」真连一次 SSH，
 * 把真机规则和平台登记并排放在一张表里，状态列直接说清楚每条规则的处境
 * （已生效 / 待下发 / 真机没有 / 真机有但平台没登记）。
 *
 * 顺序刻意是 现读 → 预检 → 下发 → 回读核对：
 * 预检会把将要执行的命令原样摊开给人看，并过一遍命令闸门；
 * 下发之后自动回读，验不上就明说「执行了但没生效」，不拿「请求成功」糊弄。
 *
 * 能力边界写在页头的提示里：流量监控 / 进程连接 / 带宽限速 / Agent 接入
 * 这些要装 agent 才能做，纯 SSH 做不了，就不画这些页面。
 */
import { computed, onActivated, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  adoptFirewallRule,
  applyFirewall,
  createFirewallRule,
  deleteFirewallGroup,
  deleteFirewallRule,
  dispatchFirewallGroup,
  getFirewallCleanup,
  getFirewallSnapshot,
  getFirewallState,
  listFirewallGroups,
  listFirewallRules,
  listFirewallSnapshots,
  listHosts,
  precheckFirewall,
  rollbackFirewall,
  saveFirewallGroup,
  updateFirewallRule,
  type FirewallCleanupItem,
  type FirewallGroup,
  type FirewallMerged,
  type FirewallPrecheck,
  type FirewallRule,
  type FirewallSnapshot,
  type FirewallState,
  type Host
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

/**
 * 两个视图放在同一页而不是拆成两个菜单：编辑完安全组通常紧接着就要去主机视图下发，
 * 拆开会变成来回跳菜单。
 */
const view = ref<'host' | 'group'>('host')


const hosts = ref<Host[]>([])
const hostId = ref<number | null>(null)
const loading = ref(false)
const state = ref<FirewallState | null>(null)
const direction = ref<'in' | 'out'>('in')
const stateFilter = ref<string | null>(null)
const keyword = ref('')

const backendMeta: Record<string, { label: string; tone: 'success' | 'warning' | 'danger' | 'info' }> = {
  firewalld: { label: 'firewalld', tone: 'success' },
  ufw: { label: 'ufw', tone: 'success' },
  iptables: { label: '裸 iptables', tone: 'warning' },
  none: { label: '未检测到', tone: 'danger' }
}

const stateMeta: Record<string, { label: string; tone: 'success' | 'warning' | 'danger' | 'info'; hint: string }> = {
  synced: { label: '已生效', tone: 'success', hint: '平台登记与真机一致' },
  drift: { label: '待下发', tone: 'warning', hint: '平台登记了，真机上还没有（或平台已停用但真机还在）' },
  extra: { label: '未登记', tone: 'info', hint: '真机上有，平台没登记；平台不会擅自删它' },
  pending: { label: '未下发', tone: 'warning', hint: '刚登记，还没下发过' }
}

const actionMeta: Record<string, { label: string; tone: 'success' | 'danger' | 'warning' }> = {
  accept: { label: '放行', tone: 'success' },
  drop: { label: '丢弃', tone: 'danger' },
  reject: { label: '拒绝', tone: 'warning' }
}

async function loadHosts() {
  try {
    const data = await listHosts({ page: 1, pageSize: 200 })
    hosts.value = data.list || []
    if (!hostId.value && hosts.value.length) hostId.value = hosts.value[0].id
  } catch {
    // 主机列表拿不到时下拉留空，页面其它部分照常可用
  }
}

/** 现读：真连一次主机。慢是正常的，所以按钮上挂 loading 而不是做轮询 */
async function readState() {
  if (!hostId.value) {
    ElMessage.warning('请先选择主机')
    return
  }
  loading.value = true
  try {
    state.value = await getFirewallState(hostId.value)
  } catch (e: any) {
    state.value = null
    ElMessage.error(e?.message || '读取失败')
  } finally {
    loading.value = false
  }
}

const rows = computed<FirewallMerged[]>(() => {
  const list = (state.value?.rules || []).filter((r) => r.direction === direction.value)
  const kw = keyword.value.trim().toLowerCase()
  return list.filter((r) => {
    if (stateFilter.value && r.state !== stateFilter.value) return false
    if (!kw) return true
    return [r.source, r.port, r.service, r.description, r.owner, r.raw]
      .join(' ')
      .toLowerCase()
      .includes(kw)
  })
})

const chips = computed<ChipItem[]>(() => {
  const s = state.value?.summary || {}
  const dirRules = (state.value?.rules || []).filter((r) => r.direction === direction.value)
  const countOf = (key: string) => dirRules.filter((r) => r.state === key).length
  return [
    { key: 'all', label: '全部', count: dirRules.length, hint: '当前方向', tone: 'primary' },
    { key: 'synced', label: '已生效', count: countOf('synced'), hint: '两边一致', tone: 'success' },
    { key: 'drift', label: '待下发', count: countOf('drift'), hint: '平台与真机不一致', tone: 'warning' },
    { key: 'extra', label: '未登记', count: countOf('extra'), hint: '真机有、平台没有', tone: 'info' },
    { key: 'summary', label: '合计', count: Object.values(s).reduce((a, b) => a + b, 0), hint: '双向', static: true }
  ]
})

function onChipSelect(key: string) {
  stateFilter.value = key === 'all' || key === 'summary' ? null : key
  page.value = 1
}

// 规则是一次读回来的全量数据，分页在前端做即可，不用再打接口
const page = ref(1)
const pageSize = ref(20)
const pagedRows = computed(() =>
  rows.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value)
)
// 方向、关键字变化都要回到第一页，否则会停在越界页码上看到空表
watch([direction, keyword], () => (page.value = 1))


const driftCount = computed(
  () => (state.value?.rules || []).filter((r) => r.state === 'drift').length
)

// ---------- 规则编辑 ----------

const editVisible = ref(false)
const editingId = ref<number | null>(null)
const adopting = ref(false)
const form = reactive({
  direction: 'in' as 'in' | 'out',
  action: 'accept' as 'accept' | 'drop' | 'reject',
  protocol: 'tcp',
  source: 'any',
  port: '',
  service: '',
  description: '',
  owner: '',
  lifecycle: 'permanent' as 'permanent' | 'temporary',
  expiresAt: '',
  enabled: true
})

function resetForm(row?: FirewallMerged) {
  form.direction = row?.direction || direction.value
  form.action = row?.action || 'accept'
  form.protocol = row?.protocol || 'tcp'
  form.source = row?.source || 'any'
  form.port = row?.port || ''
  form.service = row?.service || ''
  form.description = row?.description || ''
  form.owner = row?.owner || ''
  form.lifecycle = (row?.lifecycle as 'permanent' | 'temporary') || 'permanent'
  form.expiresAt = row?.expiresAt ? row.expiresAt.replace('T', ' ').slice(0, 19) : ''
  form.enabled = row ? row.enabled : true
}

function openCreate() {
  editingId.value = null
  adopting.value = false
  resetForm()
  editVisible.value = true
}

function openEdit(row: FirewallMerged) {
  editingId.value = row.ruleId
  adopting.value = false
  resetForm(row)
  editVisible.value = true
}

/** 接管真机上读到的存量规则：规则形状锁住不给改，只补责任人与生命周期 */
function openAdopt(row: FirewallMerged) {
  editingId.value = null
  adopting.value = true
  resetForm(row)
  editVisible.value = true
}

async function submitRule() {
  if (!hostId.value) return
  const payload = { ...form, hostId: hostId.value }
  try {
    if (adopting.value) await adoptFirewallRule(payload)
    else if (editingId.value) await updateFirewallRule(editingId.value, payload)
    else await createFirewallRule(payload)
    editVisible.value = false
    ElMessage.success(adopting.value ? '已接管，无需下发' : '已登记，下发后才会在真机生效')
    await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  }
}

async function toggleEnabled(row: FirewallMerged) {
  if (!row.ruleId) return
  try {
    await updateFirewallRule(row.ruleId, {
      direction: row.direction,
      action: row.action,
      protocol: row.protocol,
      source: row.source,
      port: row.port,
      service: row.service,
      description: row.description,
      owner: row.owner,
      lifecycle: row.lifecycle,
      expiresAt: row.expiresAt ? row.expiresAt.replace('T', ' ').slice(0, 19) : '',
      enabled: !row.enabled
    })
    ElMessage.success(row.enabled ? '已停用，下发后才会从真机移除' : '已启用，下发后才会在真机生效')
    await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '操作失败')
  }
}

async function removeRule(row: FirewallMerged) {
  if (!row.ruleId) return
  await ElMessageBox.confirm(
    '只会删掉平台登记，真机上那条规则不会跟着消失。要让真机也移除，请先「停用」再下发一次。',
    '确认删除登记',
    { type: 'warning', confirmButtonText: '删除登记', cancelButtonText: '取消' }
  )
  try {
    const res = await deleteFirewallRule(row.ruleId)
    ElMessage.success(res.note)
    await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

// ---------- 预检与下发 ----------

const planVisible = ref(false)
const planLoading = ref(false)
const precheck = ref<FirewallPrecheck | null>(null)
const confirmProd = ref(false)
const applying = ref(false)

const verdictMeta: Record<string, { label: string; type: 'success' | 'warning' | 'error' | 'info' }> = {
  pass: { label: '可以下发', type: 'success' },
  warn: { label: '命中提醒规则，可下发', type: 'warning' },
  blocked: { label: '被闸门拦下，不能下发', type: 'error' },
  unknown: { label: '未预检出结论', type: 'info' },
  nothing: { label: '没有要下发的改动', type: 'info' }
}

async function openPrecheck() {
  if (!hostId.value) return
  planVisible.value = true
  planLoading.value = true
  precheck.value = null
  try {
    precheck.value = await precheckFirewall({ hostId: hostId.value, confirmProd: confirmProd.value })
  } catch (e: any) {
    ElMessage.error(e?.message || '预检失败')
    planVisible.value = false
  } finally {
    planLoading.value = false
  }
}

/** 勾生产确认后重新预检：闸门判定会跟着变，不重算就会给出过期的结论 */
async function onConfirmProdChange() {
  if (planVisible.value && !planLoading.value) await openPrecheck()
}

const canApply = computed(
  () => precheck.value?.verdict === 'pass' || precheck.value?.verdict === 'warn'
)

async function doApply() {
  if (!hostId.value) return
  applying.value = true
  try {
    const res = await applyFirewall({ hostId: hostId.value, confirmProd: confirmProd.value })
    if (!res.applied) {
      ElMessage.info(res.reason || '没有要下发的改动')
    } else if (res.verified) {
      ElMessage.success('下发完成，回读已确认生效')
    } else {
      // 执行了但没验上，必须说清楚，不能显示成功
      ElMessageBox.alert(res.note || '下发已执行，但回读后仍有差异，请检查执行输出', '下发未确认生效', {
        type: 'warning'
      })
    }
    planVisible.value = false
    await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '下发失败')
  } finally {
    applying.value = false
  }
}

// ---------- 时间线与回滚 ----------

const timelineVisible = ref(false)
const snapshots = ref<FirewallSnapshot[]>([])
const snapshotDetail = ref<FirewallSnapshot | null>(null)

const reasonText: Record<string, string> = {
  'before-apply': '下发前',
  'after-apply': '下发后',
  'after-rollback': '回滚后',
  manual: '手动'
}

async function openTimeline() {
  if (!hostId.value) return
  timelineVisible.value = true
  snapshotDetail.value = null
  try {
    const data = await listFirewallSnapshots({ hostId: hostId.value, page: 1, pageSize: 50 })
    snapshots.value = data.list || []
  } catch (e: any) {
    ElMessage.error(e?.message || '读取快照失败')
  }
}

async function viewSnapshot(id: number) {
  try {
    snapshotDetail.value = await getFirewallSnapshot(id)
  } catch (e: any) {
    ElMessage.error(e?.message || '读取快照失败')
  }
}

async function doRollback(snap: FirewallSnapshot) {
  await ElMessageBox.confirm(
    `会把真机规则改回快照 #${snap.id}（${reasonText[snap.reason] || snap.reason} · ${snap.ruleCount} 条）的样子：` +
      '快照里有而现在没有的补回去，现在有而快照里没有的删掉。命令同样过下发闸门。',
    '确认回滚',
    { type: 'warning', confirmButtonText: '回滚', cancelButtonText: '取消' }
  )
  try {
    const res = await rollbackFirewall(snap.id, { confirmProd: confirmProd.value })
    if (!res.applied) ElMessage.info(res.reason || '无需回滚')
    else ElMessage.success(`已回滚：补 ${res.plan.adds.length} 条、删 ${res.plan.removes.length} 条`)
    timelineVisible.value = false
    await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '回滚失败')
  }
}

// ---------- 清理建议 ----------

const cleanupVisible = ref(false)
const cleanup = ref<{ idleDays: number; suggestions: FirewallCleanupItem[]; note: string } | null>(null)

async function openCleanup() {
  cleanupVisible.value = true
  try {
    cleanup.value = await getFirewallCleanup(hostId.value || undefined)
  } catch (e: any) {
    ElMessage.error(e?.message || '读取清理建议失败')
  }
}

const levelTag: Record<string, 'danger' | 'warning' | 'info'> = {
  error: 'danger',
  warning: 'warning',
  info: 'info'
}

// ---------- 安全组 ----------
//
// 安全组 = 一组规则 + 一批成员主机。「铺到成员」只把规则登记到成员主机上，
// 不自动下发：一次点按钮往一批生产机改防火墙风险太高，仍要逐台预检下发。

const groups = ref<FirewallGroup[]>([])
const groupsLoading = ref(false)
const currentGroup = ref<FirewallGroup | null>(null)
const groupRules = ref<FirewallRule[]>([])

const groupFormVisible = ref(false)
const groupEditingId = ref(0)
const groupForm = reactive({
  name: '',
  description: '',
  memberHostIds: [] as number[],
  enabled: true
})

const groupRuleVisible = ref(false)
const groupRuleEditingId = ref<number | null>(null)

function hostName(id: number) {
  return hosts.value.find((h) => h.id === id)?.name || `#${id}`
}

/** 后端存的是 JSON 数组（与 CronJob 的主机列表同格式） */
function memberIds(g: FirewallGroup): number[] {
  const raw = (g.memberHostIds || '').trim()
  if (!raw) return []
  try {
    const list = JSON.parse(raw)
    return Array.isArray(list) ? list.map(Number).filter((n) => n > 0) : []
  } catch {
    return []
  }
}

async function loadGroups() {
  groupsLoading.value = true
  try {
    groups.value = await listFirewallGroups()
    if (currentGroup.value) {
      const found = groups.value.find((g) => g.id === currentGroup.value!.id)
      currentGroup.value = found || null
      if (found) await selectGroup(found)
    }
  } catch (e: any) {
    ElMessage.error(e?.message || '读取安全组失败')
  } finally {
    groupsLoading.value = false
  }
}

async function selectGroup(g: FirewallGroup) {
  currentGroup.value = g
  try {
    const data = await listFirewallRules({ groupId: g.id, page: 1, pageSize: 200 })
    groupRules.value = data.list || []
  } catch (e: any) {
    ElMessage.error(e?.message || '读取安全组规则失败')
  }
}

function openGroupForm(g?: FirewallGroup) {
  groupEditingId.value = g?.id || 0
  groupForm.name = g?.name || ''
  groupForm.description = g?.description || ''
  groupForm.memberHostIds = g ? memberIds(g) : []
  groupForm.enabled = g ? g.enabled : true
  groupFormVisible.value = true
}

async function submitGroup() {
  if (!groupForm.name.trim()) {
    ElMessage.warning('安全组名称不能为空')
    return
  }
  try {
    const saved = await saveFirewallGroup(groupEditingId.value, { ...groupForm })
    groupFormVisible.value = false
    ElMessage.success('已保存')
    await loadGroups()
    if (!groupEditingId.value) await selectGroup(saved)
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  }
}

async function removeGroup(g: FirewallGroup) {
  await ElMessageBox.confirm(
    `会删掉安全组「${g.name}」和它自己的 ${g.ruleCount} 条规则。` +
      '已经铺到成员主机上的那些规则不会自动撤销，需要在对应主机上停用并再下发一次。',
    '确认删除安全组',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
  )
  try {
    const res = await deleteFirewallGroup(g.id)
    ElMessage.success(res.note)
    if (currentGroup.value?.id === g.id) {
      currentGroup.value = null
      groupRules.value = []
    }
    await loadGroups()
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

async function doDispatch(g: FirewallGroup) {
  const members = memberIds(g)
  if (!members.length) {
    ElMessage.warning('这个安全组还没有成员主机')
    return
  }
  await ElMessageBox.confirm(
    `会把「${g.name}」的 ${g.ruleCount} 条规则登记到 ${members.length} 台成员主机上` +
      '（已有同样规则的会跳过）。<b>只登记不下发</b>，之后还要在主机视图里逐台预检并下发才会生效。',
    '铺到成员主机',
    { type: 'warning', dangerouslyUseHTMLString: true, confirmButtonText: '铺过去', cancelButtonText: '取消' }
  )
  try {
    const res = await dispatchFirewallGroup(g.id)
    ElMessageBox.alert(
      `涉及 ${res.hosts} 台主机：新登记 ${res.created} 条，已存在跳过 ${res.skipped} 条。${res.note}`,
      '已登记',
      { type: 'success' }
    )
    if (hostId.value && members.includes(hostId.value)) await readState()
  } catch (e: any) {
    ElMessage.error(e?.message || '铺规则失败')
  }
}

function openGroupRule(rule?: FirewallRule) {
  if (!currentGroup.value) return
  groupRuleEditingId.value = rule?.id || null
  form.direction = rule?.direction || 'in'
  form.action = rule?.action || 'accept'
  form.protocol = rule?.protocol || 'tcp'
  form.source = rule?.source || 'any'
  form.port = rule?.port || ''
  form.service = rule?.service || ''
  form.description = rule?.description || ''
  form.owner = rule?.owner || ''
  form.lifecycle = (rule?.lifecycle as 'permanent' | 'temporary') || 'permanent'
  form.expiresAt = rule?.expiresAt ? rule.expiresAt.replace('T', ' ').slice(0, 19) : ''
  form.enabled = rule ? rule.enabled : true
  groupRuleVisible.value = true
}

async function submitGroupRule() {
  if (!currentGroup.value) return
  // hostId 传 0：安全组规则是模板，不属于任何一台主机
  const payload = { ...form, hostId: 0, groupId: currentGroup.value.id }
  try {
    if (groupRuleEditingId.value) await updateFirewallRule(groupRuleEditingId.value, payload)
    else await createFirewallRule(payload)
    groupRuleVisible.value = false
    ElMessage.success('已保存到安全组，铺到成员主机后仍需逐台下发')
    await loadGroups()
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  }
}

async function removeGroupRule(rule: FirewallRule) {
  await ElMessageBox.confirm('只删安全组里的这条模板规则，已经铺到成员主机上的那些不受影响。', '确认删除', {
    type: 'warning'
  })
  try {
    await deleteFirewallRule(rule.id)
    ElMessage.success('已删除')
    await loadGroups()
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

watch(view, (v) => {
  if (v === 'group' && !groups.value.length) loadGroups()
})

onMounted(async () => {
  await loadHosts()
  // 刻意不自动现读：那是一次真 SSH，20s 量级，页面一打开就卡住并不好，
  // 而且默认选中的主机不一定是用户想看的那台。先把引导文案摆在表格空态里
})
// 页签工作台会缓存本页：切回来时规则可能已经被别人改过，但现读很贵，
// 这里只提示不自动重读，由人决定
onActivated(() => {
  if (state.value) state.value = { ...state.value }
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="防火墙策略"
      subtitle="平台只登记「期望有哪些规则」，真机状态每次现读，两边对账出差异；下发与回滚都走下发闸门"
    >
      <template #actions>
        <el-radio-group v-model="view" size="default">
          <el-radio-button value="host">按主机</el-radio-button>
          <el-radio-button value="group">安全组</el-radio-button>
        </el-radio-group>
        <template v-if="view === 'host'">
          <el-select
            v-model="hostId"
            placeholder="选择主机"
            filterable
            style="width: 260px"
            @change="readState"
          >
            <el-option
              v-for="h in hosts"
              :key="h.id"
              :label="`${h.name}（${h.address}）`"
              :value="h.id"
            />
          </el-select>
          <el-button :loading="loading" @click="readState">
            <el-icon v-if="!loading" style="margin-right: 4px"><Refresh /></el-icon>
            现读真机
          </el-button>
          <el-button @click="openTimeline">
            <el-icon style="margin-right: 4px"><Clock /></el-icon>
            时间线
          </el-button>
        </template>
        <el-button v-else :loading="groupsLoading" @click="loadGroups">
          <el-icon v-if="!groupsLoading" style="margin-right: 4px"><Refresh /></el-icon>
          刷新
        </el-button>
        <el-button @click="openCleanup">
          <el-icon style="margin-right: 4px"><Brush /></el-icon>
          清理建议
        </el-button>
      </template>
    </PageHeader>

    <template v-if="view === 'host'">


    <!-- 后端事实条：这一页的一切都以它为前提，所以放在最上面且信息密度最高 -->
    <!-- 现读是真连 SSH，慢到 20s 量级也正常，所以首次读取期间给个骨架，别让页面空着 -->
    <el-card v-if="loading && !state" class="fact-card" shadow="never">
      <div class="facts">
        <el-skeleton animated :rows="1" style="width: 420px" />
        <div class="fact-grow"></div>
        <span class="hint">正在 SSH 连上去读真机规则…</span>
      </div>
    </el-card>
    <el-card v-else-if="state" class="fact-card" shadow="never">
      <div v-if="!state.readOk" class="fact-error">
        <el-alert type="error" :closable="false" show-icon :title="`读不到 ${state.hostName} 的防火墙状态`">
          <div class="mono">{{ state.readError }}</div>
        </el-alert>
      </div>
      <template v-else>
        <div class="facts">
          <div class="fact">
            <span class="fact-label">规则后端</span>
            <el-tag :type="backendMeta[state.backend]?.tone || 'info'" disable-transitions>
              {{ backendMeta[state.backend]?.label || state.backend }}
            </el-tag>
          </div>
          <div class="fact">
            <span class="fact-label">运行状态</span>
            <el-tag :type="state.active ? 'success' : 'danger'" disable-transitions>
              {{ state.active ? '运行中' : '未运行' }}
            </el-tag>
          </div>
          <div class="fact">
            <span class="fact-label">改动持久化</span>
            <el-tag :type="state.persistent ? 'success' : 'warning'" disable-transitions>
              {{ state.persistent ? '重启后保留' : '重启即失效' }}
            </el-tag>
          </div>
          <div class="fact">
            <span class="fact-label">默认策略</span>
            <span class="fact-value mono">
              入 {{ state.defaultPolicy?.in || '未知' }} / 出 {{ state.defaultPolicy?.out || '未知' }}
            </span>
          </div>
          <div class="fact">
            <span class="fact-label">读取耗时</span>
            <span class="fact-value">{{ state.costMs }} ms</span>
          </div>
          <div class="fact-grow"></div>
          <el-button
            v-perm="'firewall:manage'"
            type="primary"
            :disabled="state.backend === 'none'"
            @click="openCreate"
          >
            <el-icon style="margin-right: 4px"><Plus /></el-icon>
            新增规则
          </el-button>
          <el-badge :value="driftCount" :hidden="driftCount === 0" type="warning">
            <el-button v-perm="'firewall:manage'" :disabled="state.backend === 'none'" @click="openPrecheck">
              预检并下发
            </el-button>
          </el-badge>
        </div>
        <div v-if="state.note" class="fact-note">{{ state.note }}</div>
      </template>
    </el-card>

    <el-card>
      <el-tabs v-model="direction">
        <el-tab-pane label="入方向" name="in" />
        <el-tab-pane label="出方向" name="out" />
      </el-tabs>

      <FilterChips :items="chips" :model-value="stateFilter || 'all'" @select="onChipSelect" />

      <div class="page-toolbar" style="margin-top: 12px">
        <el-input
          v-model="keyword"
          placeholder="搜索来源 / 端口 / 描述 / 责任人"
          clearable
          style="width: 260px"
        />
        <div class="grow"></div>
        <span class="hint">共 {{ rows.length }} 条</span>
      </div>

      <el-table
        v-loading="loading"
        :data="pagedRows"
        border
        stripe
        :empty-text="
          state
            ? state.readOk

              ? '这个方向上没有规则'
              : '读不到真机状态，上面的错误信息里有原因'
            : '请先选择主机并点「现读真机」'
        "
      >
        <el-table-column label="状态" width="104">
          <template #default="{ row }">
            <el-tooltip :content="stateMeta[row.state]?.hint" placement="top">
              <el-tag :type="stateMeta[row.state]?.tone || 'info'" size="small" disable-transitions>
                {{ stateMeta[row.state]?.label || row.state }}
              </el-tag>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="动作" width="84">
          <template #default="{ row }">
            <el-tag :type="actionMeta[row.action]?.tone || 'info'" size="small" effect="plain">
              {{ actionMeta[row.action]?.label || row.action }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="协议" prop="protocol" width="76" />
        <el-table-column label="来源" min-width="140">
          <template #default="{ row }">
            <span class="mono">{{ row.source || 'any' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="端口 / 服务" min-width="120">
          <template #default="{ row }">
            <span class="mono">{{ row.port || row.service || '不限' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="描述" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.description">{{ row.description }}</span>
            <span v-else class="muted mono" :title="row.raw">{{ row.raw || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="责任人" width="100">
          <template #default="{ row }">
            <span v-if="row.owner">{{ row.owner }}</span>
            <el-tag v-else size="small" type="warning" effect="plain">缺责任人</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="生命周期" width="150">
          <template #default="{ row }">
            <template v-if="row.lifecycle === 'temporary'">
              <el-tag size="small" type="warning" effect="plain">临时</el-tag>
              <div class="muted small">{{ (row.expiresAt || '').slice(0, 16).replace('T', ' ') }} 到期</div>
            </template>
            <span v-else-if="row.ruleId" class="muted">长期</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="命中" width="96" align="right">
          <template #default="{ row }">
            <span v-if="row.hasCounter" class="mono">{{ row.hits }}</span>
            <!-- 没有计数器和命中 0 次是两件事，不能都显示 0 -->
            <el-tooltip v-else content="这台机器的规则后端不提供命中计数（ufw / firewalld 服务名规则）" placement="top">
              <span class="muted">无计数</span>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <template v-if="row.ruleId">
              <el-button v-perm="'firewall:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button v-perm="'firewall:manage'" link type="warning" @click="toggleEnabled(row)">
                {{ row.enabled ? '停用' : '启用' }}
              </el-button>
              <el-button v-perm="'firewall:manage'" link type="danger" @click="removeRule(row)">删除</el-button>
            </template>
            <el-button v-else v-perm="'firewall:manage'" link type="primary" @click="openAdopt(row)">
              接管登记
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="rows.length"
      />

      <el-alert type="info" :closable="false" style="margin-top: 12px">
        <template #title>
          这一页只做「规则」：读取、登记、对账、下发、回滚、清理建议。
          <strong>流量监控、进程连接、带宽限速、Agent 接入审批不做</strong> —— 这些要在主机上装常驻 agent
          才能拿到实时数据，纯 SSH 只能定时采样，做出来是假的实时。
          防火墙命令需要 root：主机账号不是 root 时会自动加 <code>sudo -n</code>（见系统配置 firewall.sudo）。
        </template>
      </el-alert>
    </el-card>
    </template>

    <!-- 安全组视图：左边组列表，右边这个组的规则 -->
    <el-card v-else v-loading="groupsLoading">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
        title="安全组是一组规则 + 一批成员主机。「铺到成员」只把规则登记到成员主机上，不会自动下发 —— 一次点按钮往一批生产机改防火墙风险太高，之后仍要在「按主机」视图里逐台预检下发。"
      />
      <div class="group-layout">
        <div class="group-side">
          <div class="page-toolbar" style="margin-bottom: 8px">
            <strong>安全组</strong>
            <div class="grow"></div>
            <el-button v-perm="'firewall:manage'" size="small" type="primary" @click="openGroupForm()">
              新增
            </el-button>
          </div>
          <div
            v-for="g in groups"
            :key="g.id"
            class="group-item"
            :class="{ 'is-active': currentGroup?.id === g.id }"
            @click="selectGroup(g)"
          >
            <div class="group-item__head">
              <span class="group-item__name">{{ g.name }}</span>
              <el-tag v-if="!g.enabled" size="small" type="info" effect="plain">停用</el-tag>
            </div>
            <div class="muted small">{{ g.memberCount }} 台成员 · {{ g.ruleCount }} 条规则</div>
            <div v-if="g.description" class="muted small">{{ g.description }}</div>
          </div>
          <el-empty v-if="!groups.length" description="还没有安全组" :image-size="50" />
        </div>

        <div class="group-main">
          <template v-if="currentGroup">
            <div class="page-toolbar">
              <strong>{{ currentGroup.name }}</strong>
              <span class="muted small">{{ currentGroup.description || '没有说明' }}</span>
              <div class="grow"></div>
              <el-button v-perm="'firewall:manage'" size="small" @click="openGroupForm(currentGroup)">
                改成员 / 改名
              </el-button>
              <el-button v-perm="'firewall:manage'" size="small" type="primary" @click="openGroupRule()">
                新增规则
              </el-button>
              <el-button
                v-perm="'firewall:manage'"
                size="small"
                type="warning"
                :disabled="!currentGroup.ruleCount || !currentGroup.memberCount"
                @click="doDispatch(currentGroup)"
              >
                铺到成员
              </el-button>
              <el-button v-perm="'firewall:manage'" size="small" type="danger" plain @click="removeGroup(currentGroup)">
                删除组
              </el-button>
            </div>

            <div class="member-row">
              <span class="fact-label">成员主机</span>
              <template v-if="memberIds(currentGroup).length">
                <el-tag
                  v-for="id in memberIds(currentGroup)"
                  :key="id"
                  size="small"
                  disable-transitions
                  style="margin-right: 6px"
                >
                  {{ hostName(id) }}
                </el-tag>
              </template>
              <span v-else class="muted">还没有成员，先「改成员」把主机加进来</span>
            </div>

            <el-table :data="groupRules" border stripe size="small" empty-text="这个安全组还没有规则">
              <el-table-column label="方向" width="70">
                <template #default="{ row }">{{ row.direction === 'in' ? '入' : '出' }}</template>
              </el-table-column>
              <el-table-column label="动作" width="80">
                <template #default="{ row }">
                  <el-tag :type="actionMeta[row.action]?.tone || 'info'" size="small" effect="plain">
                    {{ actionMeta[row.action]?.label || row.action }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="协议" prop="protocol" width="70" />
              <el-table-column label="来源" min-width="130">
                <template #default="{ row }"><span class="mono">{{ row.source || 'any' }}</span></template>
              </el-table-column>
              <el-table-column label="端口 / 服务" min-width="110">
                <template #default="{ row }">
                  <span class="mono">{{ row.port || row.service || '不限' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="描述" prop="description" min-width="150" show-overflow-tooltip />
              <el-table-column label="责任人" prop="owner" width="96" />
              <el-table-column label="生命周期" width="130">
                <template #default="{ row }">
                  <template v-if="row.lifecycle === 'temporary'">
                    <el-tag size="small" type="warning" effect="plain">临时</el-tag>
                    <div class="muted small">{{ (row.expiresAt || '').slice(0, 16).replace('T', ' ') }}</div>
                  </template>
                  <span v-else class="muted">长期</span>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="120" fixed="right">
                <template #default="{ row }">
                  <el-button v-perm="'firewall:manage'" link type="primary" @click="openGroupRule(row)">
                    编辑
                  </el-button>
                  <el-button v-perm="'firewall:manage'" link type="danger" @click="removeGroupRule(row)">
                    删除
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </template>
          <el-empty v-else description="左边选一个安全组" :image-size="60" />
        </div>
      </div>
    </el-card>

    <!-- 安全组基本信息 -->
    <el-dialog
      v-model="groupFormVisible"
      :title="groupEditingId ? '编辑安全组' : '新增安全组'"
      width="560px"
    >
      <el-form label-width="92px">
        <el-form-item label="名称">
          <el-input v-model="groupForm.name" placeholder="如 web-通用放行" style="width: 280px" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="groupForm.description" placeholder="这组规则解决什么问题" />
        </el-form-item>
        <el-form-item label="成员主机">
          <el-select
            v-model="groupForm.memberHostIds"
            multiple
            filterable
            collapse-tags
            collapse-tags-tooltip
            placeholder="选择成员主机"
            style="width: 100%"
          >
            <el-option
              v-for="h in hosts"
              :key="h.id"
              :label="`${h.name}（${h.address}）`"
              :value="h.id"
            />
          </el-select>
          <span class="hint">数据权限外的主机会被后端剔除</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="groupForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="groupFormVisible = false">取消</el-button>
        <el-button type="primary" @click="submitGroup">保存</el-button>
      </template>
    </el-dialog>

    <!-- 安全组规则（复用同一个表单模型，只是不绑主机） -->
    <el-dialog
      v-model="groupRuleVisible"
      :title="groupRuleEditingId ? '编辑安全组规则' : '新增安全组规则'"
      width="560px"
    >
      <el-form label-width="92px">
        <el-form-item label="方向">
          <el-radio-group v-model="form.direction">
            <el-radio-button value="in">入方向</el-radio-button>
            <el-radio-button value="out">出方向</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="动作">
          <el-radio-group v-model="form.action">
            <el-radio-button value="accept">放行</el-radio-button>
            <el-radio-button value="drop">丢弃</el-radio-button>
            <el-radio-button value="reject">拒绝</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="协议">
          <el-select v-model="form.protocol" style="width: 140px">
            <el-option label="tcp" value="tcp" />
            <el-option label="udp" value="udp" />
            <el-option label="icmp" value="icmp" />
            <el-option label="全部" value="all" />
          </el-select>
        </el-form-item>
        <el-form-item label="来源">
          <el-input v-model="form.source" placeholder="IP 或 CIDR，any 表示不限" style="width: 240px" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input v-model="form.port" placeholder="如 443 或 6000-6010" style="width: 240px" />
          <span class="hint" style="margin-left: 8px">与服务名二选一</span>
        </el-form-item>
        <el-form-item label="服务名">
          <el-input v-model="form.service" placeholder="firewalld 服务名，如 ssh" style="width: 240px" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" placeholder="为什么要有这条规则" />
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="form.owner" placeholder="留空则记为当前登录人" style="width: 240px" />
        </el-form-item>
        <el-form-item label="生命周期">
          <el-radio-group v-model="form.lifecycle">
            <el-radio-button value="permanent">长期</el-radio-button>
            <el-radio-button value="temporary">临时</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.lifecycle === 'temporary'" label="到期时间">
          <el-date-picker
            v-model="form.expiresAt"
            type="datetime"
            placeholder="YYYY-MM-DD HH:mm:ss"
            value-format="YYYY-MM-DD HH:mm:ss"
            style="width: 240px"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="groupRuleVisible = false">取消</el-button>
        <el-button type="primary" @click="submitGroupRule">保存</el-button>
      </template>
    </el-dialog>


    <!-- 规则编辑 -->
    <el-dialog
      v-model="editVisible"
      :title="adopting ? '接管真机上的规则' : editingId ? '编辑规则' : '新增规则'"
      width="560px"
    >
      <el-alert
        v-if="adopting"
        type="info"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
        title="规则形状取自真机，这里只补责任人与生命周期；接管后状态直接是「已生效」，不需要下发"
      />
      <el-form label-width="92px">
        <el-form-item label="方向">
          <el-radio-group v-model="form.direction" :disabled="adopting">
            <el-radio-button value="in">入方向</el-radio-button>
            <el-radio-button value="out">出方向</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="动作">
          <el-radio-group v-model="form.action" :disabled="adopting">
            <el-radio-button value="accept">放行</el-radio-button>
            <el-radio-button value="drop">丢弃</el-radio-button>
            <el-radio-button value="reject">拒绝</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="协议">
          <el-select v-model="form.protocol" :disabled="adopting" style="width: 140px">
            <el-option label="tcp" value="tcp" />
            <el-option label="udp" value="udp" />
            <el-option label="icmp" value="icmp" />
            <el-option label="全部" value="all" />
          </el-select>
        </el-form-item>
        <el-form-item label="来源">
          <el-input
            v-model="form.source"
            :disabled="adopting"
            placeholder="IP 或 CIDR，any 表示不限"
            style="width: 240px"
          />
        </el-form-item>
        <el-form-item label="端口">
          <el-input
            v-model="form.port"
            :disabled="adopting"
            placeholder="如 3306 或 6000-6010"
            style="width: 240px"
          />
          <span class="hint" style="margin-left: 8px">与服务名二选一</span>
        </el-form-item>
        <el-form-item label="服务名">
          <el-input
            v-model="form.service"
            :disabled="adopting"
            placeholder="firewalld 服务名，如 ssh"
            style="width: 240px"
          />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" placeholder="为什么要有这条规则" />
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="form.owner" placeholder="留空则记为当前登录人" style="width: 240px" />
        </el-form-item>
        <el-form-item label="生命周期">
          <el-radio-group v-model="form.lifecycle">
            <el-radio-button value="permanent">长期</el-radio-button>
            <el-radio-button value="temporary">临时</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.lifecycle === 'temporary'" label="到期时间">
          <el-date-picker
            v-model="form.expiresAt"
            type="datetime"
            placeholder="YYYY-MM-DD HH:mm:ss"
            value-format="YYYY-MM-DD HH:mm:ss"
            style="width: 240px"
          />
          <span class="hint" style="margin-left: 8px">过期后会进清理建议</span>
        </el-form-item>
        <el-form-item v-if="!adopting" label="启用">
          <el-switch v-model="form.enabled" />
          <span class="hint" style="margin-left: 8px">
            停用并下发会把这条规则从真机移除
          </span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" @click="submitRule">保存</el-button>
      </template>
    </el-dialog>

    <!-- 预检 / 下发 -->
    <el-dialog v-model="planVisible" title="预检并下发" width="720px">
      <div v-loading="planLoading" style="min-height: 120px">
        <template v-if="precheck">
          <el-alert
            :type="verdictMeta[precheck.verdict]?.type || 'info'"
            :closable="false"
            show-icon
            :title="verdictMeta[precheck.verdict]?.label || precheck.verdict"
          >
            <div v-if="precheck.reason">{{ precheck.reason }}</div>
            <div v-if="precheck.warning" class="muted">{{ precheck.warning }}</div>
          </el-alert>

          <div v-if="precheck.plan?.commands?.length" class="plan">
            <div class="plan-head">
              将在 <strong>{{ precheck.hostName }}</strong> 上执行
              {{ precheck.plan.commands.length }} 条命令（新增 {{ precheck.plan.adds.length }} 条规则、移除
              {{ precheck.plan.removes.length }} 条）
            </div>
            <pre class="output-pre">{{ precheck.plan.commands.join('\n') }}</pre>
            <div v-if="precheck.plan.skipped?.length" class="plan-skip">
              <div class="plan-head">以下规则这台机器的后端不支持，已跳过：</div>
              <div v-for="(s, i) in precheck.plan.skipped" :key="i" class="muted small">{{ s }}</div>
            </div>
          </div>

          <div v-if="precheck.hits?.length" class="plan">
            <div class="plan-head">命中的命令规则：</div>
            <div v-for="(hit, i) in precheck.hits" :key="i" class="muted small mono">
              [{{ hit.action }}] {{ hit.rule }} ← {{ hit.line }}
            </div>
          </div>

          <div v-if="precheck.prodHosts?.length" class="plan">
            <el-checkbox v-model="confirmProd" @change="onConfirmProdChange">
              我确认要往生产主机（{{ precheck.prodHosts.join('、') }}）上改防火墙规则
            </el-checkbox>
          </div>
        </template>
      </div>
      <template #footer>
        <el-button @click="planVisible = false">关闭</el-button>
        <el-button
          v-perm="'firewall:apply'"
          type="primary"
          :loading="applying"
          :disabled="!canApply"
          @click="doApply"
        >
          确认下发
        </el-button>
      </template>
    </el-dialog>

    <!-- 时间线 -->
    <el-drawer v-model="timelineVisible" title="规则时间线" size="52%">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="每次下发与回滚前后各存一份真机规则原文，可以回滚到任意一份；保留天数见「系统管理 → 数据留存」"
      />
      <el-timeline>
        <el-timeline-item
          v-for="snap in snapshots"
          :key="snap.id"
          :timestamp="snap.createdAt?.replace('T', ' ').slice(0, 19)"
          placement="top"
          :type="snap.reason === 'before-apply' ? 'primary' : 'success'"
        >
          <div class="snap-row">
            <span>
              <strong>{{ reasonText[snap.reason] || snap.reason }}</strong>
              · {{ snap.backend }} · {{ snap.ruleCount }} 条 · {{ snap.operator }}
            </span>
            <div class="grow"></div>
            <el-button link type="primary" @click="viewSnapshot(snap.id)">查看</el-button>
            <el-button v-perm="'firewall:apply'" link type="warning" @click="doRollback(snap)">
              回滚到这里
            </el-button>
          </div>
        </el-timeline-item>
      </el-timeline>
      <el-empty v-if="!snapshots.length" description="这台主机还没有快照，下发一次就会有" :image-size="60" />

      <template v-if="snapshotDetail">
        <el-divider>快照 #{{ snapshotDetail.id }} 的真机原文</el-divider>
        <pre class="output-pre">{{ snapshotDetail.raw }}</pre>
      </template>
    </el-drawer>

    <!-- 清理建议 -->
    <el-drawer v-model="cleanupVisible" title="清理建议" size="46%">
      <el-alert v-if="cleanup" type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          判定口径：已过期 / 对所有来源放行且不限端口 / 没有责任人 / 创建超过
          {{ cleanup.idleDays }} 天且从未命中 / 与真机不一致。{{ cleanup.note }}
        </template>
      </el-alert>
      <el-table :data="cleanup?.suggestions || []" border stripe size="small" empty-text="没有需要清理的规则">
        <el-table-column label="级别" width="80">
          <template #default="{ row }">
            <el-tag :type="levelTag[row.level]" size="small" disable-transitions>
              {{ row.level === 'error' ? '应处理' : row.level === 'warning' ? '建议' : '提示' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="原因" prop="reason" min-width="150" />
        <el-table-column label="规则" min-width="200">
          <template #default="{ row }">
            <span class="mono small">{{ row.detail }}</span>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.fact-card {
  margin-bottom: 12px;
}
.facts {
  display: flex;
  align-items: center;
  gap: 24px;
  flex-wrap: wrap;
}
.fact {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 78px;
}
.fact-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.fact-value {
  font-size: 14px;
  font-weight: 500;
}
.fact-grow {
  flex: 1;
}
.fact-note {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.fact-error :deep(.el-alert__content) {
  width: 100%;
}
.plan {
  margin-top: 12px;
}
.plan-head {
  font-size: 13px;
  margin-bottom: 6px;
}
.plan-skip {
  margin-top: 10px;
}
.snap-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.grow {
  flex: 1;
}
.hint,
.muted {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.small {
  font-size: 12px;
}
.mono {
  font-family: Consolas, Menlo, monospace;
}
.group-layout {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}
.group-side {
  width: 240px;
  flex-shrink: 0;
  border-right: 1px solid var(--el-border-color-lighter);
  padding-right: 12px;
}
.group-main {
  flex: 1;
  min-width: 0;
}
.group-item {
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
  transition: background-color var(--ops-transition-fast, 0.15s ease);
}
.group-item:hover {
  background: var(--el-fill-color-light);
}
.group-item.is-active {
  background: var(--el-color-primary-light-9);
}
.group-item__head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.group-item__name {
  font-size: 13px;
  font-weight: 500;
}
.member-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin: 10px 0;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--el-fill-color-lighter);
}
</style>
