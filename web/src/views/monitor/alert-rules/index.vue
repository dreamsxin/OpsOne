<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import {
  createAlertRule,
  deleteAlertRule,
  diffAlertRuleVersions,
  evaluateAlertRule,
  listAlertRuleMetrics,
  listAlertRuleVersions,
  listAlertRules,
  restoreAlertRuleVersion,
  rollbackAlertRule,
  updateAlertRule,
  type AlertRule,
  type AlertRuleMetric,
  type RuleDiffItem,
  type RuleVersion
} from '@/api'

const loading = ref(false)
const rows = ref<AlertRule[]>([])
const metrics = ref<AlertRuleMetric[]>([])
const store = useUserStore()
const canManage = computed(() => store.has('alertrule:manage'))

/* ---------- 版本历史 ----------
   审计日志不记请求体，所以「谁把阈值从 80 改成 200」只能靠这份留痕回答。
   平台刻意不做策略审批 —— 真正兜住这件事的是可追溯 + 可回滚。 */
const versionVisible = ref(false)
const versionRule = ref<AlertRule | null>(null)
const versions = ref<RuleVersion[]>([])
const versionNotes = ref<string[]>([])
const diffItems = ref<RuleDiffItem[]>([])
const diffMeta = ref<{ left: any; right: any; same: boolean; changed: number } | null>(null)
const selectedVersions = ref<number[]>([])
// 单独一个入口：列出「规则已经不在了」的版本，用来恢复误删
const showOrphanOnly = ref(false)

const sourceMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  created: { label: '新建', type: 'info' },
  edited: { label: '改动', type: 'warning' },
  rollback: { label: '回滚', type: 'success' },
  deleted: { label: '删除前', type: 'danger' },
  restored: { label: '恢复', type: 'success' }
}

async function openVersions(row: AlertRule | null) {
  versionRule.value = row
  showOrphanOnly.value = row === null
  diffItems.value = []
  diffMeta.value = null
  selectedVersions.value = []
  const res = await listAlertRuleVersions(row ? { targetId: row.id } : {})
  versions.value = row ? res.versions : res.versions.filter((v) => !v.targetAlive)
  versionNotes.value = res.notes
  versionVisible.value = true
}

function toggleCompare(row: RuleVersion) {
  const idx = selectedVersions.value.indexOf(row.id)
  if (idx >= 0) {
    selectedVersions.value.splice(idx, 1)
  } else {
    selectedVersions.value.push(row.id)
    // 只保留最后选的两个
    if (selectedVersions.value.length > 2) selectedVersions.value.shift()
  }
  if (selectedVersions.value.length === 2) runDiff()
  else {
    diffItems.value = []
    diffMeta.value = null
  }
}

async function runDiff() {
  const [a, b] = selectedVersions.value
  // 小 id 在左：diff 的语义是「从旧到新」
  const res = await diffAlertRuleVersions(Math.min(a, b), Math.max(a, b))
  diffItems.value = res.items
  diffMeta.value = { left: res.left, right: res.right, same: res.same, changed: res.changed }
}

async function doRollback(row: RuleVersion) {
  if (!versionRule.value) return
  await ElMessageBox.confirm(
    `确认把「${versionRule.value.name}」回滚到第 ${row.version} 版？\n\n` +
      '回滚本身也会记成一个新版本，中间那几版仍然留在历史里；连续命中次数会归零。',
    '回滚规则',
    { type: 'warning' }
  )
  const res = await rollbackAlertRule(versionRule.value.id, row.id)
  if (res.changed) ElMessage.success(res.note)
  else ElMessage.info(res.note)
  await load()
  const fresh = rows.value.find((r) => r.id === versionRule.value?.id) || null
  await openVersions(fresh)
}

async function doRestore(row: RuleVersion) {
  await ElMessageBox.confirm(
    `确认按「${row.targetName}」第 ${row.version} 版恢复？\n\n` +
      '会建出一条**新规则**（新 ID），而且**默认停用** —— ' +
      '直接让它开始评估等于在没人确认的情况下恢复了一条可能已经不适用的策略。',
    '恢复误删的规则',
    { type: 'warning' }
  )
  const res = await restoreAlertRuleVersion(row.id)
  await ElMessageBox.alert(res.note, '已恢复', { type: 'success' })
  await load()
  openVersions(null)
}


const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  metric: '',
  comparator: 'gt' as AlertRule['comparator'],
  threshold: 0,
  windowMinutes: 60,
  consecutiveTimes: 1,
  severity: 'warning',
  enabled: true,
  remark: ''
})
const rules = {
  name: [{ required: true, message: '请输入规则名称', trigger: 'blur' }],
  metric: [{ required: true, message: '请选择指标', trigger: 'change' }]
}

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  ok: { text: '正常', type: 'success' },
  firing: { text: '触发中', type: 'danger' },
  error: { text: '指标异常', type: 'warning' },
  unknown: { text: '未评估', type: 'info' }
}

const severityMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '警告', type: 'warning' },
  info: { text: '提示', type: 'info' }
}

const comparatorOptions = [
  { label: '大于 >', value: 'gt' },
  { label: '大于等于 >=', value: 'gte' },
  { label: '小于 <', value: 'lt' },
  { label: '小于等于 <=', value: 'lte' }
]

const selectedMetric = computed(() => metrics.value.find((m) => m.key === form.metric))

function metricLabel(key: string) {
  return metrics.value.find((m) => m.key === key)?.label || key
}

function metricUnit(key: string) {
  return metrics.value.find((m) => m.key === key)?.unit || ''
}

const comparatorText: Record<string, string> = { gt: '>', gte: '>=', lt: '<', lte: '<=' }

function expression(row: AlertRule) {
  const windowed = metrics.value.find((m) => m.key === row.metric)?.windowed
  const prefix = windowed ? `最近 ${row.windowMinutes} 分钟：` : ''
  return `${prefix}${metricLabel(row.metric)} ${comparatorText[row.comparator]} ${row.threshold} ${metricUnit(row.metric)}`
}

async function load() {
  loading.value = true
  try {
    const [list, metricList] = await Promise.all([listAlertRules(), listAlertRuleMetrics()])
    rows.value = list || []
    metrics.value = metricList || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    metric: metrics.value[0]?.key || '',
    comparator: 'gt',
    threshold: 0,
    windowMinutes: 60,
    consecutiveTimes: 1,
    severity: 'warning',
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: AlertRule) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    metric: row.metric,
    comparator: row.comparator,
    threshold: row.threshold,
    windowMinutes: row.windowMinutes,
    consecutiveTimes: row.consecutiveTimes,
    severity: row.severity,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateAlertRule(editingId.value, { ...form })
    ElMessage.success('已更新，连续命中计数已重置')
  } else {
    await createAlertRule({ ...form })
    ElMessage.success('已创建，可点「试跑」立即评估一次')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: AlertRule) {
  await updateAlertRule(row.id, {
    name: row.name,
    metric: row.metric,
    comparator: row.comparator,
    threshold: row.threshold,
    windowMinutes: row.windowMinutes,
    consecutiveTimes: row.consecutiveTimes,
    severity: row.severity,
    enabled: row.enabled,
    remark: row.remark
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
  load()
}

async function evaluate(row: AlertRule) {
  const res = await evaluateAlertRule(row.id)
  const head = `${res.expression}：当前 ${res.value}`
  if (res.status === 'firing') {
    ElMessage.error(`${head}，已触发告警（${res.detail}）`)
  } else if (res.hit) {
    ElMessage.warning(`${head}，命中但连续次数 ${res.hitStreak}/${res.needStreak}，暂不告警`)
  } else {
    ElMessage.success(`${head}，未命中`)
  }
  load()
}

async function remove(row: AlertRule) {
  await ElMessageBox.confirm(`确认删除规则「${row.name}」？触发中的告警会一并恢复`, '提示', {
    type: 'warning'
  })
  await deleteAlertRule(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="规则的指标全部来自平台自己的数据（主机探测、证书巡检、执行与构建记录、告警与通知投递），不需要接 Prometheus。默认每 5 分钟评估一次（OPS_ALERT_RULE_SPEC 可改或关闭），命中后写入告警并走通知路由，条件不再满足时自动恢复。"
      />

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'alertrule:manage'" type="primary" @click="openCreate">新建规则</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有规则，可从「未处理告警数」或「探测离线主机数」建一条试试">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="规则" min-width="130" />
        <el-table-column label="条件" min-width="260">
          <template #default="{ row }">{{ expression(row) }}</template>
        </el-table-column>
        <el-table-column label="降噪" width="90">
          <template #default="{ row }">
            连续 {{ row.consecutiveTimes }} 次
          </template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="severityMeta[row.severity]?.type || 'info'">
              {{ severityMeta[row.severity]?.text || row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
              {{ statusMeta[row.lastStatus]?.text || row.lastStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近取值" width="110">
          <template #default="{ row }">
            <span v-if="row.lastEvalAt">
              {{ row.lastValue }}
              <span v-if="row.hitStreak > 0" style="color: #d97706">({{ row.hitStreak }})</span>
            </span>
            <span v-else style="color: #6b7280">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastDetail" label="明细" min-width="160" show-overflow-tooltip />
        <el-table-column prop="lastEvalAt" label="最近评估" min-width="180" />
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" :disabled="!canManage" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'alertrule:manage'" link type="primary" @click="evaluate(row)">试跑</el-button>
            <el-button link type="primary" @click="openVersions(row)">版本</el-button>
            <el-button v-perm="'alertrule:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'alertrule:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card style="margin-top: 12px">
      <template #header>
        <span>可用指标与当前取值</span>
        <span style="margin-left: 8px; color: #6b7280; font-size: 12px">
          带窗口的指标按各自规则的「最近 N 分钟」计算，这里统一按 60 分钟展示
        </span>
      </template>
      <el-table :data="metrics" border stripe size="small">
        <el-table-column prop="label" label="指标" min-width="180" />
        <el-table-column prop="key" label="键" min-width="160" />
        <el-table-column label="当前值" width="110">
          <template #default="{ row }">{{ row.currentValue }} {{ row.unit }}</template>
        </el-table-column>
        <el-table-column prop="detail" label="明细" min-width="160" show-overflow-tooltip />
        <el-table-column label="窗口" width="80">
          <template #default="{ row }">{{ row.windowed ? '是' : '—' }}</template>
        </el-table-column>
        <el-table-column prop="hint" label="说明" min-width="220" show-overflow-tooltip />
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '新建规则'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="规则名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="指标" prop="metric">
          <el-select v-model="form.metric" style="width: 100%">
            <el-option
              v-for="item in metrics"
              :key="item.key"
              :label="`${item.label}（当前 ${item.currentValue} ${item.unit}）`"
              :value="item.key"
            />
          </el-select>
          <div v-if="selectedMetric?.hint" style="margin-top: 4px; color: #6b7280; font-size: 12px">
            {{ selectedMetric.hint }}
          </div>
        </el-form-item>
        <el-form-item label="条件">
          <el-select v-model="form.comparator" style="width: 150px">
            <el-option v-for="item in comparatorOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
          <el-input-number v-model="form.threshold" :min="0" :precision="2" style="margin-left: 8px" />
          <span style="margin-left: 8px; color: #6b7280">{{ selectedMetric?.unit }}</span>
        </el-form-item>
        <el-form-item v-if="selectedMetric?.windowed" label="统计窗口">
          <el-input-number v-model="form.windowMinutes" :min="1" :max="10080" />
          <span style="margin-left: 8px; color: #6b7280">分钟</span>
        </el-form-item>
        <el-form-item label="连续命中">
          <el-input-number v-model="form.consecutiveTimes" :min="1" :max="10" />
          <span style="margin-left: 8px; color: #6b7280">次才告警，用于抑制抖动</span>
        </el-form-item>
        <el-form-item label="告警级别">
          <el-select v-model="form.severity" style="width: 150px">
            <el-option label="严重" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="提示" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer
      v-model="versionVisible"
      :title="versionRule ? `版本历史 · ${versionRule.name}` : '已删除规则的版本（可恢复）'"
      size="900px"
    >
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          审计日志<strong>不记请求体</strong>，所以「谁把阈值从 80 改成 200」只能靠这份留痕回答。
          平台<strong>刻意不做策略审批</strong> —— 改错一条告警规则的后果是静默的（之后几周没人知道
          出了问题），真正兜住这件事的是改动留痕 + 一键回滚，而不是事前点一下「同意」。
          <br />
          勾选两个版本即可比较；只比同一条规则的版本。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-button v-if="versionRule" @click="openVersions(null)">看已删除规则的版本</el-button>
        <el-button v-else @click="versionVisible = false">关闭</el-button>
        <div class="grow"></div>
        <el-tag v-if="selectedVersions.length" type="info">已选 {{ selectedVersions.length }} / 2</el-tag>
      </div>

      <el-table :data="versions" border stripe size="small" empty-text="没有版本记录">
        <el-table-column label="比较" width="60">
          <template #default="{ row }">
            <el-checkbox
              :model-value="selectedVersions.includes(row.id)"
              @change="toggleCompare(row)"
            />
          </template>
        </el-table-column>
        <el-table-column label="版本" width="70">
          <template #default="{ row }">第 {{ row.version }} 版</template>
        </el-table-column>
        <el-table-column v-if="!versionRule" prop="targetName" label="规则" min-width="130" show-overflow-tooltip />
        <el-table-column label="来源" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="sourceMeta[row.source]?.type || 'info'">
              {{ sourceMeta[row.source]?.label || row.source }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="操作人" width="100" />
        <el-table-column prop="note" label="说明" min-width="180" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.targetAlive && versionRule"
              v-perm="'alertrule:manage'"
              link
              type="primary"
              @click="doRollback(row)"
            >
              回滚
            </el-button>
            <el-button
              v-else-if="!row.targetAlive"
              v-perm="'alertrule:manage'"
              link
              type="primary"
              @click="doRestore(row)"
            >
              恢复
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-card v-if="diffMeta" shadow="never" style="margin-top: 12px">
        <template #header>
          第 {{ diffMeta.left.version }} 版 → 第 {{ diffMeta.right.version }} 版
          <el-tag v-if="diffMeta.same" size="small" type="info" style="margin-left: 8px">内容完全相同</el-tag>
          <el-tag v-else size="small" type="warning" style="margin-left: 8px">
            {{ diffMeta.changed }} 项有变化
          </el-tag>
        </template>
        <el-table :data="diffItems" border stripe size="small">
          <el-table-column prop="label" label="字段" width="150" />
          <el-table-column label="改之前" min-width="170">
            <template #default="{ row }">
              <span :class="{ 'diff-old': row.changed }">{{ row.before || '—' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="改之后" min-width="170">
            <template #default="{ row }">
              <span :class="{ 'diff-new': row.changed }">{{ row.after || '—' }}</span>
            </template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-card v-if="versionNotes.length" shadow="never" style="margin-top: 12px">
        <template #header>口径</template>
        <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
          <li v-for="(note, idx) in versionNotes" :key="idx">{{ note }}</li>
        </ul>
      </el-card>
    </el-drawer>
  </div>
</template>

<style scoped>
.diff-old {
  color: var(--el-color-danger);
  text-decoration: line-through;
}
.diff-new {
  color: var(--el-color-success);
  font-weight: 600;
}
</style>

