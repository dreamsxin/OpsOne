<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import RuleVersionDrawer from '@/components/RuleVersionDrawer.vue'
import {
  createDetectionRule,
  deleteDetectionRule,
  evaluateDetectionRule,
  listDetectionMeta,
  listDetectionRules,
  previewDetection,
  updateDetectionRule,
  type DetectionMeta,
  type DetectionMode,
  type DetectionOutcome,
  type DetectionRule,
  type DetectionStep
} from '@/api'

const loading = ref(false)
const rows = ref<DetectionRule[]>([])
const meta = ref<DetectionMeta>({ modes: [], sources: [], labelKeys: [] })

const dialog = reactive({
  visible: false,
  id: 0,
  name: '',
  mode: 'concurrent' as DetectionMode,
  joinLabel: '',
  windowMinutes: 30,
  severity: 'warning',
  enabled: true,
  remark: '',
  steps: [] as DetectionStep[]
})

const preview = ref<{ scanned: number; modeLabel: string; outcome: DetectionOutcome } | null>(null)
const previewing = ref(false)

const modeHint = computed(
  () => meta.value.modes.find((m) => m.key === dialog.mode)?.hint || ''
)

const statusType: Record<string, 'success' | 'danger' | 'warning' | 'info'> = {
  ok: 'success',
  firing: 'danger',
  error: 'warning',
  unknown: 'info'
}
const statusText: Record<string, string> = {
  ok: '未命中',
  firing: '命中中',
  error: '异常',
  unknown: '未评估'
}

async function load() {
  loading.value = true
  try {
    rows.value = await listDetectionRules()
  } finally {
    loading.value = false
  }
}

function emptyStep(): DetectionStep {
  return { name: '', titleKeyword: '', source: '', severity: '', labelKey: '', labelValue: '' }
}

function openCreate() {
  Object.assign(dialog, {
    visible: true,
    id: 0,
    name: '',
    mode: 'concurrent' as DetectionMode,
    joinLabel: '',
    windowMinutes: 30,
    severity: 'warning',
    enabled: true,
    remark: '',
    steps: [emptyStep(), emptyStep()]
  })
  preview.value = null
}

function openEdit(row: DetectionRule) {
  Object.assign(dialog, {
    visible: true,
    id: row.id,
    name: row.name,
    mode: row.mode,
    joinLabel: row.joinLabel,
    windowMinutes: row.windowMinutes,
    severity: row.severity,
    enabled: row.enabled,
    remark: row.remark,
    steps: (row.stepList || []).map((s) => ({ ...emptyStep(), ...s }))
  })
  preview.value = null
}

function addStep() {
  if (dialog.steps.length >= 5) {
    ElMessage.warning('最多 5 个步骤')
    return
  }
  dialog.steps.push(emptyStep())
}

function removeStep(index: number) {
  if (dialog.steps.length <= 2) {
    ElMessage.warning('至少保留两个步骤，单条条件请用「告警规则」')
    return
  }
  dialog.steps.splice(index, 1)
}

function payload() {
  return {
    name: dialog.name.trim(),
    mode: dialog.mode,
    joinLabel: dialog.joinLabel.trim(),
    windowMinutes: dialog.windowMinutes,
    severity: dialog.severity,
    enabled: dialog.enabled,
    remark: dialog.remark,
    steps: dialog.steps
  }
}

async function runPreview() {
  previewing.value = true
  try {
    preview.value = await previewDetection(payload())
  } catch (err: any) {
    ElMessage.error(err?.message || '预演失败')
  } finally {
    previewing.value = false
  }
}

async function submit() {
  if (!dialog.name.trim()) {
    ElMessage.warning('请填写规则名称')
    return
  }
  try {
    if (dialog.id) {
      await updateDetectionRule(dialog.id, payload())
    } else {
      await createDetectionRule(payload())
    }
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
    return
  }
  dialog.visible = false
  await load()
  ElMessage.success('已保存')
}

async function toggleEnabled(row: DetectionRule) {
  await updateDetectionRule(row.id, {
    name: row.name,
    mode: row.mode,
    joinLabel: row.joinLabel,
    windowMinutes: row.windowMinutes,
    severity: row.severity,
    remark: row.remark,
    steps: row.stepList,
    enabled: !row.enabled
  })
  await load()
  ElMessage.success(row.enabled ? '已停用' : '已启用')
}

async function evaluate(row: DetectionRule) {
  const res = await evaluateDetectionRule(row.id)
  await load()
  if (res.status === 'firing') {
    ElMessage.warning(`命中：${res.outcome.detail}`)
  } else if (res.status === 'error') {
    ElMessage.error(res.outcome?.detail || '评估异常')
  } else {
    ElMessage.success(`未命中（扫描 ${res.scanned} 条告警）：${res.outcome.detail}`)
  }
}

async function remove(row: DetectionRule) {
  await ElMessageBox.confirm(
    `删除检测规则「${row.name}」？它当前触发中的告警会被一并关闭。`,
    '确认',
    { type: 'warning' }
  )
  await deleteDetectionRule(row.id)
  await load()
  ElMessage.success('已删除')
}

function stepSummary(row: DetectionRule) {
  const list = row.stepList || []
  return list
    .map((s, i) => {
      const parts: string[] = []
      if (s.titleKeyword) parts.push(`含「${s.titleKeyword}」`)
      if (s.source) parts.push(`源=${s.source}`)
      if (s.severity) parts.push(`级别=${s.severity}`)
      if (s.labelKey) parts.push(s.labelValue ? `${s.labelKey}=${s.labelValue}` : `带 ${s.labelKey}`)
      return `${i + 1}. ${s.name || parts.join(' 且 ')}`
    })
    .join(row.mode === 'sequence' ? ' → ' : ' ＋ ')
}

onMounted(async () => {
  await load()
  try {
    meta.value = await listDetectionMeta()
  } catch {
    // 候选值拿不到只影响下拉提示，手输同样能用
  }
})

/* 版本历史：与告警规则共用一套机制与同一个抽屉组件。
   审计日志不记请求体，改错一条规则的后果是静默的，所以留痕 + 回滚是唯一兜底 */
const versionVisible = ref(false)
const versionRow = ref<{ id: number; name: string } | null>(null)

function openVersions(row: { id: number; name: string }) {
  versionRow.value = row
  versionVisible.value = true
}
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-button v-perm="'detection:manage'" type="primary" @click="openCreate">
          新建检测规则
        </el-button>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span
            >检测规则判定的是「多条告警的组合」：告警规则看单个指标越不越阈值，聚合策略把同类告警归堆降噪，这里回答「A 和
            B 一起出现（或先后出现）才说明是那个故障」。只读平台自己的告警表；窗口内<b>发生过</b>的告警都算，包括已恢复的。命中后写一条告警并走通知路由，条件不再满足自动恢复。默认每
            5 分钟评估（<code>OPS_DETECTION_SPEC</code> 可改或关闭）。</span
          >
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="规则" min-width="140" />
        <el-table-column label="模式" width="110">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ row.modeLabel }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="步骤" min-width="280">
          <template #default="{ row }">
            <span>{{ stepSummary(row) }}</span>
            <div v-if="row.mode === 'join'" style="color: var(--el-text-color-secondary)">
              按标签 {{ row.joinLabel }} 对齐
            </div>
          </template>
        </el-table-column>
        <el-table-column label="窗口" width="90">
          <template #default="{ row }">{{ row.windowMinutes }} 分钟</template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.severity === 'critical' ? 'danger' : row.severity === 'warning' ? 'warning' : 'info'"
            >
              {{ row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType[row.lastStatus] || 'info'">
              {{ statusText[row.lastStatus] || row.lastStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="lastDetail" label="最近判定" min-width="240" show-overflow-tooltip />
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '是' : '否' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="250" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'detection:manage'" link type="primary" @click="evaluate(row)">
              试跑
            </el-button>
            <el-button link type="primary" @click="openVersions(row)">版本</el-button>
            <el-button v-perm="'detection:manage'" link type="primary" @click="openEdit(row)">
              编辑
            </el-button>
            <el-button v-perm="'detection:manage'" link type="primary" @click="toggleEnabled(row)">
              {{ row.enabled ? '停用' : '启用' }}
            </el-button>
            <el-button v-perm="'detection:manage'" link type="danger" @click="remove(row)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog
      v-model="dialog.visible"
      :title="dialog.id ? '编辑检测规则' : '新建检测规则'"
      width="820px"
    >
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="dialog.name" placeholder="例如 发版引发服务离线" />
        </el-form-item>
        <el-form-item label="模式">
          <el-radio-group v-model="dialog.mode">
            <el-radio v-for="m in meta.modes" :key="m.key" :value="m.key">{{ m.label }}</el-radio>
          </el-radio-group>
          <div style="color: var(--el-text-color-secondary); font-size: 12px">{{ modeHint }}</div>
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'join'" label="对齐标签">
          <el-select
            v-model="dialog.joinLabel"
            filterable
            allow-create
            default-first-option
            placeholder="例如 host"
            style="width: 220px"
          >
            <el-option v-for="key in meta.labelKeys" :key="key" :label="key" :value="key" />
          </el-select>
          <span style="margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px">
            两步必须落在这个标签的同一个取值上
          </span>
        </el-form-item>
        <el-form-item label="关联窗口">
          <el-input-number v-model="dialog.windowMinutes" :min="1" :max="1440" />
          <span style="margin-left: 8px; color: var(--el-text-color-secondary)">分钟</span>
        </el-form-item>
        <el-form-item label="命中级别">
          <el-select v-model="dialog.severity" style="width: 140px">
            <el-option label="info" value="info" />
            <el-option label="warning" value="warning" />
            <el-option label="critical" value="critical" />
          </el-select>
          <el-checkbox v-model="dialog.enabled" style="margin-left: 16px">启用</el-checkbox>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="dialog.remark" />
        </el-form-item>
      </el-form>

      <el-divider content-position="left">步骤（各条件是「与」关系，留空表示不限制）</el-divider>
      <div v-for="(step, index) in dialog.steps" :key="index" class="step-row">
        <span class="step-index">{{ index + 1 }}</span>
        <el-input v-model="step.name" placeholder="步骤名(可选)" style="width: 130px" />
        <el-input v-model="step.titleKeyword" placeholder="标题/摘要关键字" style="width: 160px" />
        <el-select v-model="step.source" placeholder="接入源" clearable style="width: 130px">
          <el-option v-for="name in meta.sources" :key="name" :label="name" :value="name" />
        </el-select>
        <el-select v-model="step.severity" placeholder="级别" clearable style="width: 110px">
          <el-option label="info" value="info" />
          <el-option label="warning" value="warning" />
          <el-option label="critical" value="critical" />
        </el-select>
        <el-select
          v-model="step.labelKey"
          placeholder="标签键"
          clearable
          filterable
          allow-create
          default-first-option
          style="width: 120px"
        >
          <el-option v-for="key in meta.labelKeys" :key="key" :label="key" :value="key" />
        </el-select>
        <el-input v-model="step.labelValue" placeholder="标签值" style="width: 110px" />
        <el-button link type="danger" @click="removeStep(index)">删除</el-button>
      </div>
      <el-button link type="primary" @click="addStep">+ 添加步骤</el-button>

      <div v-if="preview" class="preview">
        <el-alert
          :type="preview.outcome.hit ? 'warning' : 'info'"
          :closable="false"
          :title="`扫描 ${preview.scanned} 条告警 · ${preview.modeLabel} · ${preview.outcome.hit ? '会命中' : '不会命中'}：${preview.outcome.detail}`"
        />
        <div v-for="step in preview.outcome.steps" :key="step.index" class="preview-step">
          <b>{{ step.index + 1 }}. {{ step.label }}</b>
          <span :style="{ color: step.hit ? 'var(--el-color-success)' : 'var(--el-color-danger)' }">
            命中 {{ step.matched.length }} 条
          </span>
          <div v-for="item in step.matched.slice(0, 5)" :key="item.id" class="preview-alert">
            #{{ item.id }} {{ item.title }}（{{ item.severity }} · {{ item.sourceName }} ·
            {{ item.status }}）
          </div>
        </div>
      </div>

      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button :loading="previewing" @click="runPreview">预演（不落库）</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
    <RuleVersionDrawer
      v-model="versionVisible"
      target="detection_rule"
      :target-id="versionRow?.id || 0"
      :target-name="versionRow?.name"
      @changed="load"
    />
  </div>
</template>

<style scoped>
.step-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.step-index {
  width: 20px;
  text-align: center;
  color: var(--el-text-color-secondary);
}
.preview {
  margin-top: 14px;
}
.preview-step {
  margin-top: 10px;
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.preview-alert {
  color: var(--el-text-color-secondary);
  padding-left: 14px;
}
</style>
