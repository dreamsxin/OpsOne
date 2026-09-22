<script setup lang="ts">
/**
 * 规则版本抽屉：告警规则 / 检测规则 / 聚合策略共用一个。
 *
 * 抽成组件而不是每页复制一份，理由和后端抽 ruleTargetSpec 一样 ——
 * 复制三份的代价不是行数，是口径会漂：比如「记录已删的版本只能恢复、不能回滚」
 * 这条规则，改了一处忘了另两处，用户就会在某一页看到一个点了没反应的按钮。
 */
import { computed, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  diffRuleVersions,
  listRuleVersions,
  restoreRuleVersion,
  rollbackRule,
  type RuleDiffItem,
  type RuleVersion,
  type RuleVersionTarget
} from '@/api'

const props = defineProps<{
  modelValue: boolean
  target: RuleVersionTarget
  /** 当前这条规则的 ID。为 0 表示看「已删除记录的版本」 */
  targetId: number
  targetName?: string
}>()
const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  /** 回滚或恢复成功后通知外层刷新列表 */
  changed: []
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v)
})

const loading = ref(false)
const versions = ref<RuleVersion[]>([])
const notes = ref<string[]>([])
const label = ref('规则')
/** 只看已删除记录的版本（可恢复的那些） */
const orphanOnly = ref(false)

const picked = ref<number[]>([])
const diff = ref<{ items: RuleDiffItem[]; changed: number; same: boolean } | null>(null)

const sourceMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  created: { label: '新建', type: 'success' },
  edited: { label: '编辑', type: 'info' },
  rollback: { label: '回滚', type: 'warning' },
  deleted: { label: '删除前', type: 'danger' },
  restored: { label: '恢复', type: 'success' }
}

async function load() {
  loading.value = true
  try {
    const params: Record<string, any> = {}
    if (!orphanOnly.value && props.targetId) params.targetId = props.targetId
    const data = await listRuleVersions(props.target, params)
    let list = data.versions || []
    if (orphanOnly.value) list = list.filter((v) => !v.targetAlive)
    versions.value = list
    notes.value = data.notes || []
    label.value = data.label || '规则'
  } finally {
    loading.value = false
  }
}

// 勾选两版就自动比对；勾第三版时挤掉最早的那一个
watch(picked, async (ids) => {
  if (ids.length > 2) {
    picked.value = ids.slice(-2)
    return
  }
  if (ids.length !== 2) {
    diff.value = null
    return
  }
  const [a, b] = [...ids].sort((x, y) => x - y)
  diff.value = await diffRuleVersions(a, b)
})

watch(
  () => [props.modelValue, props.target, props.targetId],
  ([open]) => {
    if (open) {
      picked.value = []
      diff.value = null
      orphanOnly.value = false
      load()
    }
  }
)

async function doRollback(row: RuleVersion) {
  await ElMessageBox.confirm(
    `把「${row.targetName}」回滚到第 ${row.version} 版？回滚本身也会记成一个新版本，中间那几版仍然在历史里。`,
    '确认回滚',
    { type: 'warning' }
  )
  const res = await rollbackRule(props.target, row.targetId, row.id)
  if (res.changed) {
    ElMessage.success(res.note)
    emit('changed')
  } else {
    ElMessage.info(res.note)
  }
  load()
}

async function doRestore(row: RuleVersion) {
  await ElMessageBox.confirm(
    `按「${row.targetName}」第 ${row.version} 版恢复出一条新记录？` +
      `新记录是新 ID，并且**默认停用** —— 直接让它生效等于在没人确认的情况下恢复了一条可能已经不适用的策略。`,
    '确认恢复',
    { type: 'warning' }
  )
  const res = await restoreRuleVersion(row.id)
  ElMessage.success(res.note)
  emit('changed')
  load()
}
</script>

<template>
  <el-drawer v-model="visible" :title="`${label}版本历史${targetName ? '：' + targetName : ''}`" size="62%">
    <div class="page-toolbar">
      <el-checkbox v-model="orphanOnly" @change="((picked = []), (diff = null), load())">
        只看已删除记录的版本（可恢复）
      </el-checkbox>
      <div class="grow"></div>
      <el-tag v-if="picked.length === 2" type="info">已选两版，下面是差异</el-tag>
      <el-tag v-else type="info">勾选两版可比较差异</el-tag>
    </div>

    <el-table v-loading="loading" :data="versions" border stripe size="small" empty-text="还没有版本记录">
      <el-table-column width="46">
        <template #default="{ row }">
          <el-checkbox-group v-model="picked">
            <el-checkbox :value="row.id" :label="''" />
          </el-checkbox-group>
        </template>
      </el-table-column>
      <el-table-column label="版本" width="70">
        <template #default="{ row }">第 {{ row.version }} 版</template>
      </el-table-column>
      <el-table-column label="来源" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="sourceMeta[row.source]?.type || 'info'">
            {{ sourceMeta[row.source]?.label || row.source }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="targetName" label="对象" min-width="120" show-overflow-tooltip />
      <el-table-column prop="operator" label="操作人" width="90" />
      <el-table-column label="时间" width="150">
        <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
      </el-table-column>
      <el-table-column prop="note" label="备注" min-width="130" show-overflow-tooltip>
        <template #default="{ row }">{{ row.note || '—' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="110" fixed="right">
        <template #default="{ row }">
          <el-button v-if="row.targetAlive" link type="warning" @click="doRollback(row)">回滚</el-button>
          <el-button v-else link type="primary" @click="doRestore(row)">恢复</el-button>
        </template>
      </el-table-column>
    </el-table>

    <template v-if="diff">
      <el-divider content-position="left">
        字段差异（{{ diff.changed }} 项不同{{ diff.same ? '，两版内容完全一致' : '' }}）
      </el-divider>
      <el-table :data="diff.items.filter((i) => i.changed)" border size="small" empty-text="两版没有差异">
        <el-table-column prop="label" label="字段" width="150" />
        <el-table-column label="改之前" min-width="160">
          <template #default="{ row }"><span class="diff-old">{{ row.before || '（空）' }}</span></template>
        </el-table-column>
        <el-table-column label="改之后" min-width="160">
          <template #default="{ row }"><span class="diff-new">{{ row.after || '（空）' }}</span></template>
        </el-table-column>
      </el-table>
    </template>

    <ul style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
      <li v-for="(note, idx) in notes" :key="idx">{{ note }}</li>
    </ul>
  </el-drawer>
</template>

<style scoped>
.diff-old {
  color: #f56c6c;
  text-decoration: line-through;
}
.diff-new {
  color: #67c23a;
  font-weight: 600;
}
</style>
