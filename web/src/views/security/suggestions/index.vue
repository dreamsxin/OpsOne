<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  applySecuritySuggestions,
  deleteSuggestionDismissal,
  listSecuritySuggestions,
  listSuggestionDismissals,
  type SecSuggestion,
  type SecSuggestionDismissal,
  type SecSuggestionList
} from '@/api'

const loading = ref(false)
const data = ref<SecSuggestionList | null>(null)
const selected = ref<SecSuggestion[]>([])
const kindFilter = ref('')

const dismissals = ref<SecSuggestionDismissal[]>([])
const dismissalNote = ref('')
const dismissalVisible = ref(false)

const kindMeta: Record<string, { type: 'success' | 'warning' | 'danger' | 'info'; hint: string }> = {
  port_baseline: {
    type: 'info',
    hint: '通过后把端口写进该目标的基线（只动基线一列），之后这个端口不再被报为「未登记」'
  },
  mute_fingerprint: {
    type: 'warning',
    hint: '通过后把指纹加入误报白名单，采集器直接跳过 —— 但挡掉的次数仍然照记'
  },
  signature_enforce: {
    type: 'danger',
    hint: '通过后特征从观察态提到拦截态、命令规则重写为 block —— 下一次下发会真的被拦'
  }
}

const rows = computed(() => {
  const items = data.value?.items ?? []
  return kindFilter.value ? items.filter((x) => x.kind === kindFilter.value) : items
})

const hasEnforce = computed(() => selected.value.some((x) => x.kind === 'signature_enforce'))

async function load() {
  loading.value = true
  try {
    data.value = await listSecuritySuggestions()
    const d = await listSuggestionDismissals()
    dismissals.value = d.rows
    dismissalNote.value = d.note
  } finally {
    loading.value = false
  }
}

function onSelect(rowsSel: SecSuggestion[]) {
  selected.value = rowsSel
}

async function apply(decision: 'approve' | 'dismiss', items?: SecSuggestion[]) {
  const list = items ?? selected.value
  if (!list.length) {
    ElMessage.warning('请先选择建议')
    return
  }

  let reason = ''
  if (decision === 'dismiss') {
    const input = await ElMessageBox.prompt(
      `拒绝 ${list.length} 条建议之后，它们不会再出现在列表里（随时可以在下面撤销）。写一句原因：`,
      '拒绝建议',
      { inputPlaceholder: '例如：这个端口下周就关了' }
    ).catch(() => null)
    if (!input) return
    reason = input.value || ''
  } else {
    // 提到拦截态是唯一有真实拦截后果的一类，单独确认
    const enforce = list.filter((x) => x.kind === 'signature_enforce')
    const detail = list.map((x) => `· ${x.action}`).join('\n')
    const warn = enforce.length
      ? `\n\n注意：其中 ${enforce.length} 条会让命令规则变成 block，下一次下发会真的被拦。`
      : ''
    const ok = await ElMessageBox.confirm(
      `将执行以下 ${list.length} 项改动：\n\n${detail}${warn}`,
      '通过建议',
      { type: enforce.length ? 'warning' : 'info', dangerouslyUseHTMLString: false }
    ).catch(() => false)
    if (!ok) return
  }

  const res = await applySecuritySuggestions({
    keys: list.map((x) => x.key),
    decision,
    reason
  })
  if (res.failed.length) {
    ElMessageBox.alert(
      `成功 ${res.done.length} 条，失败 ${res.failed.length} 条：\n\n` +
        res.failed.map((f) => `· ${f.error}`).join('\n'),
      '部分未完成',
      { type: 'warning' }
    )
  } else {
    ElMessage.success(
      decision === 'approve'
        ? res.done.map((d) => d.result).join('；')
        : `已记下 ${res.done.length} 条不再提示`
    )
  }
  selected.value = []
  load()
}

async function restore(row: SecSuggestionDismissal) {
  await deleteSuggestionDismissal(row.id)
  ElMessage.success('已撤销；如果这条建议当前仍然成立，它会重新出现')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这一页把「已有数据里看得出来的规律」变成可执行的一次改动。
          <strong>通过就是真的去改配置</strong>，不是标个已读 —— 一个点了没反应的按钮比没有这个按钮更糟；
          每条建议都写明了会改什么。
          <br />
          {{ data?.note }}
        </template>
      </el-alert>

      <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-radio-group v-model="kindFilter">
          <el-radio-button value="">全部（{{ data?.items.length ?? 0 }}）</el-radio-button>
          <el-radio-button value="port_baseline">
            端口基线（{{ data?.byKind.port_baseline ?? 0 }}）
          </el-radio-button>
          <el-radio-button value="mute_fingerprint">
            误报白名单（{{ data?.byKind.mute_fingerprint ?? 0 }}）
          </el-radio-button>
          <el-radio-button value="signature_enforce">
            特征提到拦截（{{ data?.byKind.signature_enforce ?? 0 }}）
          </el-radio-button>
        </el-radio-group>
        <div class="grow"></div>
        <el-button v-if="dismissals.length" @click="dismissalVisible = true">
          已拒绝 {{ dismissals.length }} 条
        </el-button>
        <el-button @click="load">刷新</el-button>
        <el-button v-perm="'suggestion:apply'" :disabled="!selected.length" @click="apply('dismiss')">
          拒绝（{{ selected.length }}）
        </el-button>
        <el-button
          v-perm="'suggestion:apply'"
          :type="hasEnforce ? 'danger' : 'primary'"
          :disabled="!selected.length"
          @click="apply('approve')"
        >
          通过（{{ selected.length }}）
        </el-button>
      </div>

      <el-table
        v-loading="loading"
        :data="rows"
        border
        stripe
        empty-text="当前没有建议 —— 数据里还看不出值得改配置的规律"
        @selection-change="onSelect"
      >
        <el-table-column type="selection" width="45" />
        <el-table-column label="类型" width="130">
          <template #default="{ row }">
            <el-tooltip :content="kindMeta[row.kind]?.hint" placement="top">
              <el-tag size="small" :type="kindMeta[row.kind]?.type || 'info'">{{ row.kindLabel }}</el-tag>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="复现" width="80">
          <template #default="{ row }">{{ row.hitCount }} 次</template>
        </el-table-column>
        <el-table-column prop="title" label="建议" min-width="260" show-overflow-tooltip />
        <el-table-column prop="reason" label="依据" min-width="300" show-overflow-tooltip />
        <el-table-column prop="action" label="通过后会做什么" min-width="240" show-overflow-tooltip />
        <el-table-column prop="extra" label="当前状态" min-width="170" show-overflow-tooltip />
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'suggestion:apply'" link type="primary" @click="apply('approve', [row])">
              通过
            </el-button>
            <el-button v-perm="'suggestion:apply'" link type="info" @click="apply('dismiss', [row])">
              拒绝
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-drawer v-model="dismissalVisible" title="已拒绝的建议" size="700px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>{{ dismissalNote }}</template>
      </el-alert>
      <el-table :data="dismissals" border stripe size="small" empty-text="没有被拒绝的建议">
        <el-table-column prop="kind" label="类型" width="150" />
        <el-table-column prop="title" label="建议" min-width="240" show-overflow-tooltip />
        <el-table-column prop="reason" label="拒绝原因" min-width="160" show-overflow-tooltip />
        <el-table-column prop="operator" label="操作人" width="100" />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'suggestion:apply'" link type="primary" @click="restore(row)">撤销</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>
