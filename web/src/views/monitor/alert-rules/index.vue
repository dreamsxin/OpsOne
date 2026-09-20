<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { useUserStore } from '@/stores/user'
import {
  createAlertRule,
  deleteAlertRule,
  evaluateAlertRule,
  listAlertRuleMetrics,
  listAlertRules,
  updateAlertRule,
  type AlertRule,
  type AlertRuleMetric
} from '@/api'

const loading = ref(false)
const rows = ref<AlertRule[]>([])
const metrics = ref<AlertRuleMetric[]>([])
const store = useUserStore()
const canManage = computed(() => store.has('alertrule:manage'))

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
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'alertrule:manage'" link type="primary" @click="evaluate(row)">试跑</el-button>
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
  </div>
</template>
