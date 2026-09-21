<script setup lang="ts">
import { computed, onActivated, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ackAlert,
  getAlert,
  getAlertStats,
  listAlertSources,
  listAlerts,
  resolveAlert,
  type Alert,
  type AlertSource,
  type AlertStats,
  type NotifyRecord
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'


const router = useRouter()
const route = useRoute()
const loading = ref(false)
const rows = ref<Alert[]>([])
const total = ref(0)
const stats = ref<AlertStats | null>(null)
const query = reactive({
  page: 1,
  pageSize: 20,
  status: '',
  severity: '',
  keyword: '',
  // 高级筛选：接入源 + 时间范围，后端 ListAlerts 已按 source_id / last_seen_at 过滤
  sourceId: 0,
  startTime: '',
  endTime: ''
})
const sources = ref<AlertSource[]>([])
const showAdvanced = ref(false)
/** 高级筛选生效计数，用于按钮上打红点提醒用户「有隐藏条件」 */
const advancedActive = computed(
  () => [query.sourceId, query.startTime, query.endTime].filter((v) => !!v && v !== 0).length
)

const detailVisible = ref(false)
const current = ref<Alert | null>(null)
const records = ref<NotifyRecord[]>([])
const selection = ref<Alert[]>([])

const severityMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '警告', type: 'warning' },
  info: { text: '提示', type: 'info' }
}

const statusMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'success' }> = {
  firing: { text: '触发中', type: 'danger' },
  acked: { text: '已确认', type: 'warning' },
  resolved: { text: '已恢复', type: 'success' }
}

async function load() {
  loading.value = true
  try {
    const [data, s] = await Promise.all([listAlerts(query), getAlertStats()])
    rows.value = data.list || []
    total.value = data.total
    stats.value = s
  } finally {
    loading.value = false
  }
}

/** chip 的 key 编码规则：`字段:值`，全部用 `all`。点击后同时清空另一维度以避免矛盾条件 */
const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `当前 ${total.value} 条 · 本页 ${rows.value.length}`, static: true },
    {
      key: 'status:firing',
      label: '触发中',
      count: s?.firing ?? 0,
      hint: '需要处理',
      tone: 'danger'
    },
    {
      key: 'status:acked',
      label: '已确认',
      count: s?.acked ?? 0,
      hint: '处理中',
      tone: 'warning'
    },
    {
      key: 'status:resolved',
      label: '已恢复',
      count: s?.resolved ?? 0,
      hint: '仅回溯',
      tone: 'success'
    },
    {
      key: 'severity:critical',
      label: '严重',
      count: s?.critical ?? 0,
      hint: '未恢复',
      tone: 'danger'
    },
    {
      key: 'severity:warning',
      label: '警告',
      count: s?.warning ?? 0,
      hint: '未恢复',
      tone: 'warning'
    }
  ]
})

const activeChipKey = computed<string | null>(() => {
  if (query.severity) return `severity:${query.severity}`
  if (query.status) return `status:${query.status}`
  return null
})

function onChipSelect(key: string) {
  if (key === 'info') return
  const [field, value] = key.split(':')
  // 切换 chip 时清掉另一维度，避免"触发中 + 严重"这种矛盾组合把用户绕晕
  query.status = ''
  query.severity = ''
  if (field === 'status') query.status = value
  else if (field === 'severity') {
    query.severity = value
    // 概览里的「严重/警告」统计的是未恢复的，点进来必须落到同一个集合，
    // 否则 chip 上写 3、列表里 57，用户会以为统计或分页坏了
    query.status = 'unresolved'
  }
  query.page = 1
  load()
}

function resetFilters() {
  query.status = ''
  query.severity = ''
  query.keyword = ''
  query.sourceId = 0
  query.startTime = ''
  query.endTime = ''
  query.page = 1
  load()
}

function onSelectionChange(rows: Alert[]) {
  selection.value = rows
}

async function openDetail(row: Alert) {
  const data = await getAlert(row.id)
  current.value = data.alert
  records.value = data.records || []
  detailVisible.value = true
}

function parseLabels(raw: string): Record<string, string> {
  if (!raw) return {}
  try {
    return JSON.parse(raw)
  } catch {
    return {}
  }
}

async function ack(row: Alert) {
  const { value } = await ElMessageBox.prompt('请输入确认说明（可留空）', '确认告警', {
    inputValue: '',
    inputValidator: () => true
  })
  await ackAlert(row.id, value || '')
  ElMessage.success('已确认')
  detailVisible.value = false
  load()
}

async function resolve(row: Alert) {
  const { value } = await ElMessageBox.prompt('请输入恢复说明（可留空）', '恢复告警', {
    inputValue: '',
    inputValidator: () => true
  })
  await resolveAlert(row.id, value || '')
  ElMessage.success('已恢复')
  detailVisible.value = false
  load()
}

/** 批量确认：只对当前 firing 的行生效，逐条调后端；失败的记下来一起报 */
async function batchAck() {
  const targets = selection.value.filter((r) => r.status === 'firing')
  if (!targets.length) {
    ElMessage.warning('选中的告警中没有触发中的行')
    return
  }
  const { value } = await ElMessageBox.prompt(
    `将确认 ${targets.length} 条告警（其余 ${selection.value.length - targets.length} 条已跳过）`,
    '批量确认',
    { inputValue: '批量确认', inputValidator: () => true }
  )
  let ok = 0
  const failed: string[] = []
  for (const r of targets) {
    try {
      await ackAlert(r.id, value || '')
      ok++
    } catch (e: any) {
      failed.push(`#${r.id}: ${e?.message || e}`)
    }
  }
  if (failed.length) ElMessage.warning(`成功 ${ok} 条，失败 ${failed.length} 条：${failed[0]}`)
  else ElMessage.success(`已确认 ${ok} 条`)
  selection.value = []
  load()
}

async function batchResolve() {
  const targets = selection.value.filter((r) => r.status !== 'resolved')
  if (!targets.length) {
    ElMessage.warning('选中的告警全部已恢复')
    return
  }
  const { value } = await ElMessageBox.prompt(
    `将恢复 ${targets.length} 条告警（其余 ${selection.value.length - targets.length} 条已跳过）`,
    '批量恢复',
    { inputValue: '批量恢复', inputValidator: () => true }
  )
  let ok = 0
  const failed: string[] = []
  for (const r of targets) {
    try {
      await resolveAlert(r.id, value || '')
      ok++
    } catch (e: any) {
      failed.push(`#${r.id}: ${e?.message || e}`)
    }
  }
  if (failed.length) ElMessage.warning(`成功 ${ok} 条，失败 ${failed.length} 条：${failed[0]}`)
  else ElMessage.success(`已恢复 ${ok} 条`)
  selection.value = []
  load()
}

/**
 * 关联查询：把告警标签翻译成 Loki / Prometheus / Jaeger 的入口 URL。
 * 平台自己不聚合，只是把用户手动跳过去要拼的东西拼好。
 */
const relatedLinks = computed(() => {
  const a = current.value
  if (!a) return null
  const labels = parseLabels(a.labels)
  const app = labels.app || labels.job || labels.service || ''
  const instance = labels.instance || labels.host || ''
  const traceId = labels.trace_id || labels.traceID || ''
  // Loki：有 app 或 instance 才值得跳，否则查全量意义不大
  const logql = app || instance ? `{${app ? `app="${app}"` : ''}${app && instance ? ',' : ''}${instance ? `instance="${instance}"` : ''}}` : ''
  const promql = app || instance
    ? `${app ? app.replace(/[^a-zA-Z0-9_:]/g, '_') + '_up' : 'up'}${instance ? `{instance="${instance}"}` : ''}`
    : ''
  return {
    loki: logql ? { path: '/monitor/logs', query: { q: logql } } : null,
    prom: promql ? { path: '/monitor/metrics', query: { q: promql } } : null,
    jaeger: traceId ? { path: '/monitor/traces', query: { traceId } } : null,
    // 同指纹历史：把 fingerprint 当关键字回查现有列表接口
    history: { path: '/monitor/alerts', query: { keyword: a.fingerprint } }
  }
})

function gotoRelated(link: { path: string; query: Record<string, string> } | null | undefined) {
  if (!link) return
  detailVisible.value = false
  router.push(link)
}

/** 接住深链：详情抽屉「同指纹历史」带 ?keyword=，仪表盘告警态势带 ?status= / ?severity= */
function applyRouteQuery() {
  let changed = false
  const pick = (key: string, current: string) => {
    const v = (route.query as Record<string, unknown>)[key]
    if (v === undefined || v === null) return current
    return String(v)
  }
  const nextKeyword = pick('keyword', query.keyword)
  const nextStatus = pick('status', query.status)
  const nextSeverity = pick('severity', query.severity)
  if (nextKeyword !== query.keyword || nextStatus !== query.status || nextSeverity !== query.severity) {
    query.keyword = nextKeyword
    query.status = nextStatus
    query.severity = nextSeverity
    query.page = 1
    changed = true
  }
  return changed
}

// 缓存页在后台照样会跑 watcher：别的页面产生带 status/severity/keyword 的 URL 时
// （仪表盘点「离线主机」就会 push /asset/host?status=offline），
// 没有路径守卫的话这个缓存着的告警页会跟着改自己的筛选并发一次请求，
// 用户切回来看到的是一个莫名其妙的空列表
watch(
  () => route.fullPath,
  () => {
    if (route.path !== '/monitor/alerts') return
    if (applyRouteQuery()) load()
  }
)

onActivated(() => {
  if (applyRouteQuery()) load()
})

onMounted(() => {
  applyRouteQuery()
  load()
  listAlertSources()
    .then((list) => (sources.value = list || []))
    .catch(() => {
      // 接入源列表拿不到只影响高级筛选的下拉，不挡列表
    })
})
</script>

<template>
  <div class="page">
    <PageHeader title="活跃告警" subtitle="外部推送 + 平台自检 都汇入这一个池子；同指纹去重只累加次数">
      <template #actions>
        <el-button :loading="loading" @click="load">
          <el-icon v-if="!loading" style="margin-right: 4px"><Refresh /></el-icon>
          刷新
        </el-button>
        <el-button type="primary" @click="router.push('/config/webhooks')">接入源</el-button>
      </template>
    </PageHeader>

    <el-card>
      <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

      <div class="page-toolbar" style="margin-top: 12px">
        <el-input
          v-model="query.keyword"
          placeholder="搜索标题 / 摘要 / 标签 / 指纹"
          style="width: 260px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="resetFilters">重置</el-button>
        <el-button
          link
          type="primary"
          @click="showAdvanced = !showAdvanced"
        >
          高级筛选
          <el-badge v-if="advancedActive" :value="advancedActive" style="margin-left: 4px" />
          <el-icon style="margin-left: 4px">
            <ArrowUp v-if="showAdvanced" />
            <ArrowDown v-else />
          </el-icon>
        </el-button>

        <div class="grow"></div>

        <template v-if="selection.length">
          <span class="bulk-hint">已选 {{ selection.length }} 项</span>
          <el-button
            v-perm="'alert:handle'"
            size="small"
            type="warning"
            plain
            @click="batchAck"
          >
            批量确认
          </el-button>
          <el-button
            v-perm="'alert:handle'"
            size="small"
            type="success"
            plain
            @click="batchResolve"
          >
            批量恢复
          </el-button>
        </template>
      </div>

      <el-collapse-transition>
        <div v-show="showAdvanced" class="advanced-filter">
          <el-form :inline="true" size="small">
            <el-form-item label="接入源">
              <el-select
                v-model="query.sourceId"
                placeholder="全部"
                clearable
                style="width: 180px"
                @change="((query.page = 1), load())"
              >
                <el-option label="全部" :value="0" />
                <el-option
                  v-for="src in sources"
                  :key="src.id"
                  :label="src.name"
                  :value="src.id"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="开始时间">
              <el-date-picker
                v-model="query.startTime"
                type="datetime"
                placeholder="YYYY-MM-DD HH:mm:ss"
                value-format="YYYY-MM-DD HH:mm:ss"
                style="width: 200px"
              />
            </el-form-item>
            <el-form-item label="结束时间">
              <el-date-picker
                v-model="query.endTime"
                type="datetime"
                placeholder="YYYY-MM-DD HH:mm:ss"
                value-format="YYYY-MM-DD HH:mm:ss"
                style="width: 200px"
              />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" @click="((query.page = 1), load())">应用</el-button>
              <el-button @click="((query.startTime = ''), (query.endTime = ''), (query.page = 1), load())">
                清空时间
              </el-button>
            </el-form-item>
          </el-form>
          <div class="advanced-note">
            时间范围按「最近一次出现时间」过滤：告警按指纹去重累加，同一条可能持续几小时，这里问的是这段时间里还在响的。
          </div>
        </div>
      </el-collapse-transition>

      <el-table
        v-loading="loading"
        :data="rows"
        border
        stripe
        empty-text="暂无告警，可在「配置中心 → Webhook 接入」创建接入源后推送"
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="42" :selectable="(row: Alert) => row.status !== 'resolved'" />
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="severityMeta[row.severity]?.type || 'info'">
              {{ severityMeta[row.severity]?.text || row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" label="标题" min-width="220" show-overflow-tooltip />
        <el-table-column prop="sourceName" label="来源" width="120" />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="count" label="次数" width="70" />
        <el-table-column label="通知" width="110">
          <template #default="{ row }">
            <el-tooltip v-if="row.silencedBy" :content="`被「${row.silencedBy}」静默，未外发通知`">
              <el-tag size="small" type="warning">已静默</el-tag>
            </el-tooltip>
            <el-tooltip
              v-else-if="row.suppressedBy"
              :content="`被聚合策略「${row.suppressedBy}」抑制，未外发通知`"
            >
              <el-tag size="small" type="info">已抑制</el-tag>
            </el-tooltip>
            <span v-else>已外发</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastSeenAt" label="最近出现" min-width="180" />
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <el-button link type="primary" @click="gotoRelated({ path: '/monitor/alerts', query: { keyword: row.fingerprint } })">
              同指纹
            </el-button>
            <el-button
              v-perm="'alert:handle'"
              link
              type="primary"
              :disabled="row.status !== 'firing'"
              @click="ack(row)"
            >
              确认
            </el-button>
            <el-button
              v-perm="'alert:handle'"
              link
              type="success"
              :disabled="row.status === 'resolved'"
              @click="resolve(row)"
            >
              恢复
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-drawer v-model="detailVisible" :title="`告警 #${current?.id ?? ''}`" size="55%">
      <el-descriptions v-if="current" :column="1" border size="small">
        <el-descriptions-item label="标题">{{ current.title }}</el-descriptions-item>
        <el-descriptions-item label="摘要">{{ current.summary || '-' }}</el-descriptions-item>
        <el-descriptions-item label="级别 / 状态">
          <el-tag size="small" :type="severityMeta[current.severity]?.type || 'info'">
            {{ severityMeta[current.severity]?.text }}
          </el-tag>
          <el-tag size="small" :type="statusMeta[current.status]?.type || 'info'" style="margin-left: 6px">
            {{ statusMeta[current.status]?.text }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="当前值">{{ current.value || '-' }}</el-descriptions-item>
        <el-descriptions-item label="标签">
          <el-tag
            v-for="(v, k) in parseLabels(current.labels)"
            :key="k"
            size="small"
            style="margin-right: 4px"
          >
            {{ k }}={{ v }}
          </el-tag>
          <span v-if="!Object.keys(parseLabels(current.labels)).length">-</span>
        </el-descriptions-item>
        <el-descriptions-item label="首次 / 最近">
          {{ current.firstSeenAt }} · {{ current.lastSeenAt }}
        </el-descriptions-item>
        <el-descriptions-item label="处理">
          {{ current.ackBy ? `${current.ackBy} 于 ${current.ackAt}` : '未确认' }}
          <span v-if="current.handleNote">（{{ current.handleNote }}）</span>
        </el-descriptions-item>
        <el-descriptions-item label="指纹">
          <code style="font-size: 12px">{{ current.fingerprint }}</code>
        </el-descriptions-item>
      </el-descriptions>

      <el-divider content-position="left">关联查询</el-divider>
      <div v-if="relatedLinks" class="related-links">
        <el-button v-if="relatedLinks.history" link type="primary" @click="gotoRelated(relatedLinks.history)">
          同指纹历史 →
        </el-button>
        <el-button v-if="relatedLinks.loki" link type="primary" @click="gotoRelated(relatedLinks.loki)">
          在 Loki 中查看日志 →
        </el-button>
        <el-button v-if="relatedLinks.prom" link type="primary" @click="gotoRelated(relatedLinks.prom)">
          在 Prometheus 中查看指标 →
        </el-button>
        <el-button v-if="relatedLinks.jaeger" link type="primary" @click="gotoRelated(relatedLinks.jaeger)">
          在 Jaeger 中查看 Trace →
        </el-button>
        <div v-if="!relatedLinks.loki && !relatedLinks.prom && !relatedLinks.jaeger" class="advanced-note">
          当前告警标签里没有 app / instance / trace_id，无法拼出关联查询。
        </div>
      </div>

      <el-divider content-position="left">通知投递</el-divider>
      <el-table :data="records" size="small" border empty-text="没有产生通知，可能是未命中路由或没有兜底路由">
        <el-table-column prop="routeName" label="路由" min-width="120" />
        <el-table-column prop="channelName" label="渠道" min-width="120" />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
              {{ row.status === 'success' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="httpStatus" label="HTTP" width="80" />
        <el-table-column prop="costMs" label="耗时(ms)" width="100" />
        <el-table-column prop="errorMsg" label="错误" min-width="180" show-overflow-tooltip />
        <el-table-column prop="createdAt" label="时间" min-width="180" />
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.advanced-filter {
  padding: 8px 12px;
  margin-bottom: 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
}
.advanced-note {
  font-size: 12px;
  color: var(--ops-text-muted);
  margin-top: 4px;
}
.advanced-note code {
  background: var(--el-fill-color);
  padding: 1px 4px;
  border-radius: 3px;
}
.related-links {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  padding: 4px 0;
}
</style>
