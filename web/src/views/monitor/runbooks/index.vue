<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createRunbook,
  deleteRunbook,
  getRunbook,
  getRunbookStats,
  listRunbooks,
  listRunbookUses,
  recheckRunbooks,
  updateRunbook,
  type Runbook,
  type RunbookStats,
  type RunbookStep,
  type RunbookUse
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'

const loading = ref(false)
const route = useRoute()
const rows = ref<Runbook[]>([])
const total = ref(0)
const stats = ref<RunbookStats | null>(null)
const query = reactive({ page: 1, pageSize: 20, keyword: '', category: '', precheckStatus: '', enabled: '' })

const uses = ref<RunbookUse[]>([])
const useTotal = ref(0)
const useQuery = reactive({ page: 1, pageSize: 20, outcome: '' })
const tab = ref<'books' | 'uses'>('books')

const riskMeta: Record<string, { text: string; type: 'info' | 'warning' | 'danger' }> = {
  low: { text: '低', type: 'info' },
  medium: { text: '中', type: 'warning' },
  high: { text: '高', type: 'danger' }
}

const precheckMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  pass: { text: '通过', type: 'success' },
  warn: { text: '有提醒', type: 'warning' },
  blocked: { text: '被拦截', type: 'danger' },
  unknown: { text: '未预检', type: 'info' }
}

const outcomeMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  resolved: { text: '解决了', type: 'success' },
  partial: { text: '部分有效', type: 'warning' },
  invalid: { text: '没用', type: 'danger' }
}

async function load() {
  loading.value = true
  try {
    const data = await listRunbooks(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  stats.value = await getRunbookStats()
}

async function loadUses() {
  const data = await listRunbookUses(useQuery)
  uses.value = data.list || []
  useTotal.value = data.total
}

const chips = computed<ChipItem[]>(() => {
  const s = stats.value
  return [
    { key: 'info', label: `共 ${s?.total ?? 0} 本 · 启用 ${s?.enabled ?? 0}`, static: true },
    { key: 'precheck:blocked', label: '命令被拦截', count: s?.blocked ?? 0, hint: '不允许启用', tone: 'danger' },
    { key: 'precheck:warn', label: '命令有提醒', count: s?.warn ?? 0, hint: '可用但要留意', tone: 'warning' },
    { key: 'generic', label: '通用剧本', count: s?.generic ?? 0, hint: '没设匹配条件，只能兜底', static: true },
    { key: 'never', label: '从没用过', count: s?.neverUsed ?? 0, hint: '建了但没人用', static: true },
    {
      key: 'solve',
      label: `使用 ${s?.uses ?? 0} 次 · 解决 ${s?.solved ?? 0}`,
      hint: s?.uses ? `解决率 ${Math.round(((s.solved ?? 0) / s.uses) * 100)}%` : '还没有使用记录',
      static: true
    }
  ]
})

const activeChipKey = computed<string | null>(() =>
  query.precheckStatus ? `precheck:${query.precheckStatus}` : null
)

function onChipSelect(key: string) {
  if (!key.startsWith('precheck:')) return
  const value = key.split(':')[1]
  query.precheckStatus = query.precheckStatus === value ? '' : value
  query.page = 1
  load()
}

// ---------- 编辑 ----------
const dialogVisible = ref(false)
const editing = ref<Runbook | null>(null)
const form = reactive({
  name: '',
  category: '',
  summary: '',
  matchKeywords: '',
  matchSeverity: '',
  precheck: '',
  rollback: '',
  riskLevel: 'low',
  enabled: true
})
const labelRows = ref<{ key: string; value: string }[]>([])
const stepRows = ref<RunbookStep[]>([])
const lastHits = ref<{ line: number; snippet: string; action: string; description: string }[]>([])

function openDialog(book?: Runbook) {
  editing.value = book || null
  Object.assign(form, {
    name: book?.name || '',
    category: book?.category || '',
    summary: book?.summary || '',
    matchKeywords: book?.matchKeywords || '',
    matchSeverity: book?.matchSeverity || '',
    precheck: book?.precheck || '',
    rollback: book?.rollback || '',
    riskLevel: book?.riskLevel || 'low',
    enabled: book ? book.enabled : true
  })
  labelRows.value = Object.entries(book?.matchLabels || {}).map(([key, value]) => ({ key, value }))
  stepRows.value = book?.steps?.length
    ? book.steps.map((s) => ({ ...s }))
    : [{ title: '', detail: '', command: '' }]
  lastHits.value = []
  dialogVisible.value = true
}

function addStep() {
  stepRows.value.push({ title: '', detail: '', command: '' })
}
function moveStep(idx: number, delta: number) {
  const target = idx + delta
  if (target < 0 || target >= stepRows.value.length) return
  const list = stepRows.value
  ;[list[idx], list[target]] = [list[target], list[idx]]
}

function buildPayload() {
  const matchLabels: Record<string, string> = {}
  for (const row of labelRows.value) {
    if (row.key.trim()) matchLabels[row.key.trim()] = row.value.trim() || '*'
  }
  return {
    ...form,
    matchLabels,
    steps: stepRows.value.filter((s) => s.title.trim() || s.command.trim())
  }
}

async function submit() {
  const payload = buildPayload()
  if (!payload.name.trim()) {
    ElMessage.warning('请填写剧本名称')
    return
  }
  if (!payload.steps.length) {
    ElMessage.warning('至少写一个处置步骤')
    return
  }
  try {
    const res = editing.value
      ? await updateRunbook(editing.value.id, payload)
      : await createRunbook(payload)
    lastHits.value = (res.precheckHits as any) || []
    if (res.runbook.precheckStatus === 'warn') {
      ElMessage.warning('已保存；步骤里的命令命中了提醒级命令规则，下发时会被记录')
    } else {
      ElMessage.success('已保存')
    }
    dialogVisible.value = false
    load()
    loadStats()
  } catch (err: any) {
    // 后端把 blocked 的原因写在 msg 里，直接呈现，不做二次加工
    lastHits.value = []
    throw err
  }
}

async function remove(book: Runbook) {
  await ElMessageBox.confirm(`确认删除剧本「${book.name}」？`, '提示', { type: 'warning' })
  await deleteRunbook(book.id)
  ElMessage.success('已删除')
  load()
  loadStats()
}

async function toggleEnabled(book: Runbook) {
  await updateRunbook(book.id, {
    name: book.name,
    category: book.category,
    summary: book.summary,
    matchLabels: book.matchLabels,
    matchKeywords: book.matchKeywords,
    matchSeverity: book.matchSeverity,
    steps: book.steps,
    precheck: book.precheck,
    rollback: book.rollback,
    riskLevel: book.riskLevel,
    enabled: !book.enabled
  })
  ElMessage.success(book.enabled ? '已停用' : '已启用')
  load()
  loadStats()
}

async function recheck() {
  const res = await recheckRunbooks()
  if (res.disabled.length) {
    ElMessageBox.alert(
      `重新预检 ${res.total} 本，${res.changed} 本状态有变化。以下剧本命中了拦截规则，已被自动停用：\n${res.disabled.join('、')}`,
      '有剧本被停用',
      { type: 'warning' }
    )
  } else {
    ElMessage.success(`重新预检 ${res.total} 本，${res.changed} 本状态有变化，没有需要停用的`)
  }
  load()
  loadStats()
}

// ---------- 详情 ----------
const detailVisible = ref(false)
const detail = ref<{ runbook: Runbook; uses: RunbookUse[] } | null>(null)

async function openDetail(book: Runbook) {
  detail.value = await getRunbook(book.id)
  detailVisible.value = true
}

function hitList(raw: string) {
  try {
    return JSON.parse(raw || '[]') as { line: number; snippet: string; action: string; description: string }[]
  } catch {
    return []
  }
}

function switchTab(name: string | number) {
  if (name === 'uses') loadUses()
}

onMounted(() => {
  load()
  loadStats()
  // 复盘页「沉淀成剧本」带过来的草稿：内容太长放不进 URL，走 sessionStorage
  if (route.query.draft === '1') {
    const raw = sessionStorage.getItem('runbookDraft')
    sessionStorage.removeItem('runbookDraft')
    if (raw) {
      try {
        const draft = JSON.parse(raw)
        openDialog()
        Object.assign(form, {
          name: draft.name || '',
          summary: draft.summary || '',
          matchSeverity: draft.matchSeverity || '',
          precheck: draft.precheck || '',
          rollback: draft.rollback || '',
          riskLevel: draft.riskLevel || 'medium'
        })
        labelRows.value = Object.entries(draft.matchLabels || {}).map(([key, value]) => ({
          key,
          value: String(value)
        }))
        stepRows.value = (draft.steps || []).length
          ? draft.steps.map((s: any) => ({ title: s.title || '', detail: s.detail || '', command: s.command || '' }))
          : [{ title: '', detail: '', command: '' }]
        ElMessage.info('草稿已带入，请把步骤改成「下次遇到同类问题怎么做」')
      } catch {
        ElMessage.warning('草稿解析失败，请手动创建')
      }
    }
  }
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="处置剧本"
      subtitle="告警来了照着做什么：按标签与关键词自动推荐，步骤里的命令过命令规则预检，用过要留结果"
    >
      <template #actions>
        <el-button v-perm="'runbook:manage'" @click="recheck">重新预检全部</el-button>
        <el-button v-perm="'runbook:manage'" type="primary" @click="openDialog()">新建剧本</el-button>
      </template>
    </PageHeader>

    <el-tabs v-model="tab" @tab-change="switchTab">
      <el-tab-pane label="剧本库" name="books">
        <el-card>
          <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

          <div class="page-toolbar" style="margin-top: 12px">
            <el-input
              v-model="query.keyword"
              placeholder="名称 / 适用说明 / 关键词"
              style="width: 220px"
              clearable
              @keyup.enter="((query.page = 1), load())"
            />
            <el-input v-model="query.category" placeholder="分类" style="width: 120px" clearable />
            <el-select v-model="query.enabled" placeholder="启用状态" clearable style="width: 120px">
              <el-option label="启用中" value="1" />
              <el-option label="已停用" value="0" />
            </el-select>
            <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
            <el-button
              @click="((query.keyword = ''), (query.category = ''), (query.enabled = ''), (query.precheckStatus = ''), (query.page = 1), load())"
            >
              重置
            </el-button>
          </div>

          <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有剧本">
            <el-table-column prop="name" label="剧本" min-width="190" show-overflow-tooltip>
              <template #default="{ row }">
                {{ row.name }}
                <el-tag v-if="!row.enabled" size="small" type="info" style="margin-left: 4px">停用</el-tag>
                <el-tag v-if="row.generic" size="small" style="margin-left: 4px">通用</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="category" label="分类" width="90" />
            <el-table-column label="匹配条件" min-width="200">
              <template #default="{ row }">
                <el-tag
                  v-for="(v, k) in row.matchLabels"
                  :key="k"
                  size="small"
                  style="margin-right: 4px"
                >
                  {{ k }}={{ v }}
                </el-tag>
                <span v-if="row.matchKeywords" style="color: #6b7280; font-size: 12px">
                  关键词：{{ row.matchKeywords }}
                </span>
                <span v-if="row.matchSeverity" style="color: #6b7280; font-size: 12px">
                  · {{ row.matchSeverity }}
                </span>
                <span v-if="row.generic" style="color: #9ca3af">无条件（兜底）</span>
              </template>
            </el-table-column>
            <el-table-column label="步骤" width="110">
              <template #default="{ row }">
                {{ row.stepCount }} 步
                <span v-if="row.commandSteps" style="color: #6b7280">（{{ row.commandSteps }} 带命令）</span>
              </template>
            </el-table-column>
            <el-table-column label="预检" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="precheckMeta[row.precheckStatus]?.type || 'info'">
                  {{ precheckMeta[row.precheckStatus]?.text || row.precheckStatus }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="风险" width="75">
              <template #default="{ row }">
                <el-tag size="small" :type="riskMeta[row.riskLevel]?.type || 'info'">
                  {{ riskMeta[row.riskLevel]?.text || row.riskLevel }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="使用 / 解决" width="110">
              <template #default="{ row }">
                <span v-if="row.useCount">
                  {{ row.useCount }} / {{ row.solveCount }}
                  <el-tag v-if="row.useCount >= 3 && row.solveCount === 0" size="small" type="danger">
                    从未解决
                  </el-tag>
                </span>
                <span v-else style="color: #9ca3af">没用过</span>
              </template>
            </el-table-column>
            <el-table-column label="版本" width="70">
              <template #default="{ row }">v{{ row.version }}</template>
            </el-table-column>
            <el-table-column label="操作" width="190" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDetail(row)">详情</el-button>
                <el-button v-perm="'runbook:manage'" link @click="openDialog(row)">编辑</el-button>
                <el-button v-perm="'runbook:manage'" link @click="toggleEnabled(row)">
                  {{ row.enabled ? '停用' : '启用' }}
                </el-button>
                <el-button v-perm="'runbook:manage'" link type="danger" @click="remove(row)">删除</el-button>
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
      </el-tab-pane>

      <el-tab-pane label="使用记录" name="uses">
        <el-card>
          <div class="page-toolbar">
            <el-select v-model="useQuery.outcome" placeholder="结果" clearable style="width: 130px">
              <el-option v-for="(meta, key) in outcomeMeta" :key="key" :label="meta.text" :value="key" />
            </el-select>
            <el-button type="primary" @click="((useQuery.page = 1), loadUses())">查询</el-button>
          </div>
          <el-alert
            type="info"
            :closable="false"
            style="margin-bottom: 12px"
            title="使用记录只追加不修改，是「这次故障按哪本剧本处置」的凭据。用得多但从不解决问题的剧本要么写错了，要么该拆。"
          />
          <el-table :data="uses" border stripe empty-text="还没有使用记录">
            <el-table-column prop="runbookName" label="剧本" min-width="180" show-overflow-tooltip />
            <el-table-column label="版本" width="70">
              <template #default="{ row }">v{{ row.version }}</template>
            </el-table-column>
            <el-table-column label="关联" width="120">
              <template #default="{ row }">
                <span v-if="row.eventId">事件 #{{ row.eventId }}</span>
                <span v-else-if="row.alertId">告警 #{{ row.alertId }}</span>
                <span v-else style="color: #9ca3af">未关联</span>
              </template>
            </el-table-column>
            <el-table-column label="结果" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="outcomeMeta[row.outcome]?.type || 'info'">
                  {{ row.outcomeLabel }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="做了几步" width="100">
              <template #default="{ row }">{{ row.doneSteps.length }} 步</template>
            </el-table-column>
            <el-table-column prop="note" label="说明" min-width="200" show-overflow-tooltip />
            <el-table-column prop="operator" label="操作人" width="110" />
            <el-table-column prop="createdAt" label="时间" min-width="170" />
          </el-table>
          <Pagination
            v-model:current-page="useQuery.page"
            v-model:page-size="useQuery.pageSize"
            :total="useTotal"
            @change="loadUses"
          />
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑剧本' : '新建剧本'" width="860px" top="5vh">
      <el-form label-width="90px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="按「什么问题」命名，不要按「哪台机器」" />
        </el-form-item>
        <el-form-item label="分类">
          <el-input v-model="form.category" placeholder="主机 / 服务 / 安全 / 平台" style="width: 200px" />
          <el-select v-model="form.riskLevel" style="width: 120px; margin-left: 12px">
            <el-option v-for="(meta, key) in riskMeta" :key="key" :label="`风险${meta.text}`" :value="key" />
          </el-select>
          <el-switch v-model="form.enabled" active-text="启用" style="margin-left: 16px" />
        </el-form-item>
        <el-form-item label="适用说明">
          <el-input v-model="form.summary" type="textarea" :rows="2" placeholder="什么情况下用这本剧本" />
        </el-form-item>

        <el-divider content-position="left">匹配条件</el-divider>
        <el-alert
          type="info"
          :closable="false"
          style="margin-bottom: 12px"
          title="标签条件要求全部命中（值填 * 表示只要求这个键存在）；关键词命中任一即可。三项都留空就是通用剧本，只会兜底出现在推荐末尾。别把 host / instance 这类实例级标签写进来——那是某一次的机器，不是这类问题。"
        />
        <el-form-item label="标签">
          <div style="width: 100%">
            <div
              v-for="(row, idx) in labelRows"
              :key="idx"
              style="display: flex; gap: 8px; margin-bottom: 6px"
            >
              <el-input v-model="row.key" placeholder="键，如 metric" style="width: 180px" />
              <el-input v-model="row.value" placeholder="值，* 表示只要求存在" style="width: 220px" />
              <el-button link type="danger" @click="labelRows.splice(idx, 1)">删除</el-button>
            </div>
            <el-button link type="primary" @click="labelRows.push({ key: '', value: '' })">
              + 加一条标签条件
            </el-button>
          </div>
        </el-form-item>
        <el-form-item label="关键词">
          <el-input v-model="form.matchKeywords" placeholder="逗号分隔，匹配告警/事件标题" />
        </el-form-item>
        <el-form-item label="级别">
          <el-select v-model="form.matchSeverity" clearable placeholder="不限" style="width: 150px">
            <el-option label="critical" value="critical" />
            <el-option label="warning" value="warning" />
            <el-option label="info" value="info" />
          </el-select>
        </el-form-item>

        <el-divider content-position="left">处置步骤</el-divider>
        <el-alert
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
          title="一步一条命令（不能带换行，预检要按步定位）。命令可以留空表示纯人工动作。命中拦截级命令规则的剧本不允许启用。"
        />
        <div v-for="(step, idx) in stepRows" :key="idx" style="margin-bottom: 10px">
          <div style="display: flex; gap: 8px; align-items: center; margin-bottom: 4px">
            <el-tag size="small">第 {{ idx + 1 }} 步</el-tag>
            <el-input v-model="step.title" placeholder="这一步要做什么" style="flex: 1" />
            <el-button link :disabled="idx === 0" @click="moveStep(idx, -1)">上移</el-button>
            <el-button link :disabled="idx === stepRows.length - 1" @click="moveStep(idx, 1)">下移</el-button>
            <el-button link type="danger" :disabled="stepRows.length === 1" @click="stepRows.splice(idx, 1)">
              删除
            </el-button>
          </div>
          <el-input v-model="step.command" placeholder="命令（可留空，表示人工动作）" style="margin-bottom: 4px">
            <template #prepend>命令</template>
          </el-input>
          <el-input v-model="step.detail" placeholder="补充说明：看什么、判断依据是什么" />
        </div>
        <el-button link type="primary" @click="addStep">+ 加一步</el-button>

        <el-divider content-position="left">动手前后</el-divider>
        <el-form-item label="动手前">
          <el-input v-model="form.precheck" type="textarea" :rows="2" placeholder="先确认什么，谁要被通知" />
        </el-form-item>
        <el-form-item label="回退办法">
          <el-input v-model="form.rollback" type="textarea" :rows="2" placeholder="做错了怎么退回来；不可逆的操作要写明" />
        </el-form-item>

        <el-alert
          v-if="lastHits.length"
          type="warning"
          :closable="false"
          title="上次保存时命中的命令规则"
          style="margin-top: 8px"
        >
          <div v-for="(hit, i) in lastHits" :key="i">
            第 {{ hit.line }} 步 · {{ hit.action }} · {{ hit.description }}：{{ hit.snippet }}
          </div>
        </el-alert>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="`剧本 · ${detail?.runbook.name ?? ''}`" size="62%">
      <template v-if="detail">
        <el-descriptions :column="3" border size="small">
          <el-descriptions-item label="分类">{{ detail.runbook.category || '-' }}</el-descriptions-item>
          <el-descriptions-item label="风险">
            {{ riskMeta[detail.runbook.riskLevel]?.text || detail.runbook.riskLevel }}
          </el-descriptions-item>
          <el-descriptions-item label="版本">v{{ detail.runbook.version }}</el-descriptions-item>
          <el-descriptions-item label="预检">
            <el-tag size="small" :type="precheckMeta[detail.runbook.precheckStatus]?.type || 'info'">
              {{ precheckMeta[detail.runbook.precheckStatus]?.text }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="使用">{{ detail.runbook.useCount }} 次</el-descriptions-item>
          <el-descriptions-item label="其中解决">{{ detail.runbook.solveCount }} 次</el-descriptions-item>
          <el-descriptions-item label="适用说明" :span="3">
            {{ detail.runbook.summary || '-' }}
          </el-descriptions-item>
        </el-descriptions>

        <el-alert
          v-if="hitList(detail.runbook.precheckHits).length"
          type="warning"
          :closable="false"
          style="margin: 12px 0"
          title="步骤里的命令命中了命令规则"
        >
          <div v-for="(hit, i) in hitList(detail.runbook.precheckHits)" :key="i">
            第 {{ hit.line }} 步 · {{ hit.action }} · {{ hit.description }}：{{ hit.snippet }}
          </div>
        </el-alert>

        <div v-if="detail.runbook.precheck" style="margin: 12px 0">
          <div style="font-weight: 500; margin-bottom: 4px">动手前</div>
          <div style="white-space: pre-wrap; color: #4b5563">{{ detail.runbook.precheck }}</div>
        </div>

        <div style="font-weight: 500; margin: 12px 0 6px">处置步骤</div>
        <el-timeline>
          <el-timeline-item
            v-for="(step, idx) in detail.runbook.steps"
            :key="idx"
            :timestamp="`第 ${idx + 1} 步`"
            placement="top"
          >
            <div style="font-weight: 500">{{ step.title }}</div>
            <div v-if="step.command" style="margin: 4px 0">
              <code style="background: #f3f4f6; padding: 2px 6px; border-radius: 4px">{{ step.command }}</code>
            </div>
            <div v-if="step.detail" style="color: #6b7280; font-size: 13px">{{ step.detail }}</div>
          </el-timeline-item>
        </el-timeline>

        <div v-if="detail.runbook.rollback" style="margin: 12px 0">
          <div style="font-weight: 500; margin-bottom: 4px">回退办法</div>
          <div style="white-space: pre-wrap; color: #4b5563">{{ detail.runbook.rollback }}</div>
        </div>

        <el-divider content-position="left">使用记录（最近 50 条）</el-divider>
        <el-table :data="detail.uses" border size="small" empty-text="还没有人用过这本剧本">
          <el-table-column label="结果" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="outcomeMeta[row.outcome]?.type || 'info'">
                {{ outcomeMeta[row.outcome]?.text || row.outcome }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="关联" width="110">
            <template #default="{ row }">
              <span v-if="row.eventId">事件 #{{ row.eventId }}</span>
              <span v-else-if="row.alertId">告警 #{{ row.alertId }}</span>
              <span v-else style="color: #9ca3af">-</span>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="70">
            <template #default="{ row }">v{{ row.version }}</template>
          </el-table-column>
          <el-table-column prop="note" label="说明" min-width="180" show-overflow-tooltip />
          <el-table-column prop="operator" label="操作人" width="100" />
          <el-table-column prop="createdAt" label="时间" min-width="170" />
        </el-table>
      </template>
    </el-drawer>
  </div>
</template>
