<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createAggregationPolicy,
  createEventFromBucket,
  deleteAggregationPolicy,
  detectAggregationOverlaps,
  listAggregationDimensions,
  listAggregationPolicies,
  previewAggregation,
  updateAggregationPolicy,
  type AggregationBucket,
  type AggregationDimension,
  type AggregationOverlap,
  type AggregationPolicy,
  type AggregationPreview
} from '@/api'

const loading = ref(false)
const rows = ref<AggregationPolicy[]>([])
const dimensions = ref<AggregationDimension[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  dimensionList: [] as string[],
  severityList: [] as string[],
  windowMinutes: 60,
  minCount: 2,
  suppressNotify: false,
  priority: 100,
  enabled: true,
  remark: ''
})
const rules = {
  name: [{ required: true, message: '请输入策略名称', trigger: 'blur' }]
}

const previewVisible = ref(false)
const preview = ref<AggregationPreview | null>(null)

const overlapVisible = ref(false)
const overlaps = ref<AggregationOverlap[]>([])
const overlapDetail = ref('')

const severityText: Record<string, string> = {
  critical: '严重',
  warning: '警告',
  info: '提示'
}

function dimensionLabel(key: string) {
  return dimensions.value.find((d) => d.key === key)?.label || key
}

function dimensionText(row: AggregationPolicy) {
  return row.dimensions
    .split(',')
    .map((d) => dimensionLabel(d))
    .join(' + ')
}

function severityScope(row: AggregationPolicy) {
  if (!row.matchSeverity) return '全部级别'
  return row.matchSeverity
    .split(',')
    .map((s) => severityText[s] || s)
    .join('、')
}

async function load() {
  loading.value = true
  try {
    const [list, dims] = await Promise.all([listAggregationPolicies(), listAggregationDimensions()])
    rows.value = list || []
    dimensions.value = dims || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    dimensionList: ['source', 'severity'],
    severityList: [],
    windowMinutes: 60,
    minCount: 2,
    suppressNotify: false,
    priority: 100,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: AggregationPolicy) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    dimensionList: row.dimensions ? row.dimensions.split(',') : [],
    severityList: row.matchSeverity ? row.matchSeverity.split(',') : [],
    windowMinutes: row.windowMinutes,
    minCount: row.minCount,
    suppressNotify: row.suppressNotify,
    priority: row.priority,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

function payload() {
  return {
    name: form.name,
    dimensions: form.dimensionList.join(','),
    matchSeverity: form.severityList.join(','),
    windowMinutes: form.windowMinutes,
    minCount: form.minCount,
    suppressNotify: form.suppressNotify,
    priority: form.priority,
    enabled: form.enabled,
    remark: form.remark
  }
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.dimensionList.length) {
    ElMessage.warning('请至少选择一个归桶维度')
    return
  }
  if (editingId.value) {
    await updateAggregationPolicy(editingId.value, payload())
    ElMessage.success('已更新')
  } else {
    await createAggregationPolicy(payload())
    ElMessage.success('已创建，可点「归桶预览」看效果')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: AggregationPolicy) {
  await updateAggregationPolicy(row.id, {
    name: row.name,
    dimensions: row.dimensions,
    matchSeverity: row.matchSeverity,
    windowMinutes: row.windowMinutes,
    minCount: row.minCount,
    suppressNotify: row.suppressNotify,
    priority: row.priority,
    enabled: row.enabled,
    remark: row.remark
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
  load()
}

async function openPreview(row: AggregationPolicy) {
  preview.value = await previewAggregation(row.id)
  previewVisible.value = true
}

async function openOverlaps() {
  const res = await detectAggregationOverlaps()
  overlaps.value = res.overlaps || []
  overlapDetail.value = res.detail
  overlapVisible.value = true
}

async function createEventForBucket(bucket: AggregationBucket) {
  if (!preview.value) return
  await ElMessageBox.confirm(
    `把这个桶里的 ${bucket.alertCount} 条告警建成一个事件？（建单时会按当前窗口重新取一次告警）`,
    '建为事件',
    { type: 'info' }
  )
  const event = await createEventFromBucket({
    policyId: preview.value.policy.id,
    bucketKey: bucket.key
  })
  ElMessage.success(`已建单 #${event.id}，可到「事件中心」指派处理`)
}

async function remove(row: AggregationPolicy) {
  await ElMessageBox.confirm(`确认删除策略「${row.name}」？已被抑制的告警记录不受影响`, '提示', {
    type: 'warning'
  })
  await deleteAggregationPolicy(row.id)
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
        title="归桶是实时计算的，不改动告警本身，随时可以预览。只有打开「抑制通知」时才会影响投递：同一个桶在窗口内只通知首条，其余告警仍然入库、页面可见，并在告警上记录是哪条策略抑制的。一条告警只会被优先级最小的那条策略处理。"
      />

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <el-button @click="openOverlaps">重叠检测</el-button>
        <div class="grow"></div>
        <el-button v-perm="'aggregation:manage'" type="primary" @click="openCreate">新建策略</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有聚合策略">
        <el-table-column prop="priority" label="优先级" width="90" />
        <el-table-column prop="name" label="策略" min-width="130" />
        <el-table-column label="归桶维度" min-width="200">
          <template #default="{ row }">{{ dimensionText(row) }}</template>
        </el-table-column>
        <el-table-column label="作用范围" width="120">
          <template #default="{ row }">{{ severityScope(row) }}</template>
        </el-table-column>
        <el-table-column label="窗口 / 成桶" width="120">
          <template #default="{ row }">{{ row.windowMinutes }} 分 / ≥{{ row.minCount }} 条</template>
        </el-table-column>
        <el-table-column label="抑制通知" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.suppressNotify ? 'warning' : 'info'">
              {{ row.suppressNotify ? '开启' : '仅归桶' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="150" show-overflow-tooltip />
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openPreview(row)">归桶预览</el-button>
            <el-button v-perm="'aggregation:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'aggregation:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑聚合策略' : '新建聚合策略'" width="580px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="120px">
        <el-form-item label="策略名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="归桶维度">
          <el-select v-model="form.dimensionList" multiple style="width: 100%">
            <el-option
              v-for="item in dimensions"
              :key="item.key"
              :label="item.count ? `${item.label}（${item.count} 条告警带此标签）` : item.label"
              :value="item.key"
            />
          </el-select>
          <div style="margin-top: 4px; color: #6b7280; font-size: 12px">
            维度越多桶越细，最多 5 个；标签维度来自现有告警里出现过的标签键
          </div>
        </el-form-item>
        <el-form-item label="作用级别">
          <el-select v-model="form.severityList" multiple placeholder="留空表示全部级别" style="width: 100%">
            <el-option label="严重" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="提示" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="统计窗口">
          <el-input-number v-model="form.windowMinutes" :min="1" :max="10080" />
          <span style="margin-left: 8px; color: #6b7280">分钟</span>
        </el-form-item>
        <el-form-item label="成桶条数">
          <el-input-number v-model="form.minCount" :min="1" :max="100" />
          <span style="margin-left: 8px; color: #6b7280">桶内达到该条数才算聚合</span>
        </el-form-item>
        <el-form-item label="抑制通知">
          <el-switch v-model="form.suppressNotify" />
          <span style="margin-left: 8px; color: #6b7280">同桶窗口内只通知首条</span>
        </el-form-item>
        <el-form-item label="优先级">
          <el-input-number v-model="form.priority" :min="1" :max="9999" />
          <span style="margin-left: 8px; color: #6b7280">越小越先匹配</span>
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

    <el-drawer v-model="previewVisible" :title="`归桶预览 · ${preview?.policy.name ?? ''}`" size="58%">
      <el-alert v-if="preview" type="info" :closable="false" :title="preview.detail" style="margin-bottom: 12px" />
      <el-table :data="preview?.buckets || []" border stripe size="small" empty-text="窗口内没有命中的未恢复告警">
        <el-table-column label="桶" min-width="240">
          <template #default="{ row }">
            <div>{{ row.key }}</div>
            <div style="color: #6b7280; font-size: 12px">样例：{{ row.sampleTitle }}</div>
          </template>
        </el-table-column>
        <el-table-column label="告警数" width="100">
          <template #default="{ row }">
            {{ row.alertCount }}
            <el-tag v-if="row.grouped" size="small" type="warning" style="margin-left: 4px">成桶</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="totalCount" label="累计次数" width="100" />
        <el-table-column label="级别分布" width="160">
          <template #default="{ row }">
            <el-tag
              v-for="(count, key) in row.severities"
              :key="key"
              size="small"
              style="margin-right: 4px"
              :type="key === 'critical' ? 'danger' : key === 'warning' ? 'warning' : 'info'"
            >
              {{ severityText[key] || key }} {{ count }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="已抑制" width="90">
          <template #default="{ row }">
            <span :style="{ color: row.suppressed ? '#d97706' : '#6b7280' }">{{ row.suppressed }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastSeenAt" label="最近出现" min-width="180" />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'event:manage'" link type="primary" @click="createEventForBucket(row)">
              建为事件
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <el-drawer v-model="overlapVisible" title="策略重叠检测" size="50%">
      <el-alert type="info" :closable="false" :title="overlapDetail" style="margin-bottom: 12px" />
      <el-table :data="overlaps" border stripe size="small" empty-text="没有重叠">
        <el-table-column label="策略 A" min-width="140">
          <template #default="{ row }">{{ row.policyA }}（{{ row.priorityA }}）</template>
        </el-table-column>
        <el-table-column label="策略 B" min-width="140">
          <template #default="{ row }">{{ row.policyB }}（{{ row.priorityB }}）</template>
        </el-table-column>
        <el-table-column prop="alerts" label="共同命中" width="100" />
        <el-table-column prop="effective" label="实际生效" width="140" />
        <el-table-column prop="sample" label="样例告警" min-width="180" show-overflow-tooltip />
      </el-table>
    </el-drawer>
  </div>
</template>
