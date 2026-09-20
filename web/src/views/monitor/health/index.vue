<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getPlatformHealth, type HealthReport } from '@/api'

const loading = ref(false)
const report = ref<HealthReport | null>(null)

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  ok: { text: '正常', type: 'success' },
  warn: { text: '需关注', type: 'warning' },
  error: { text: '异常', type: 'danger' }
}

const overallText: Record<string, string> = {
  ok: '平台各项自检正常',
  warn: '有需要关注的项，功能仍可用',
  error: '有异常项，相关功能实际不生效'
}

async function load() {
  loading.value = true
  try {
    report.value = await getPlatformHealth()
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <div class="page-toolbar">
        <el-tag v-if="report" :type="statusMeta[report.overall].type" size="large">
          {{ statusMeta[report.overall].text }}
        </el-tag>
        <span v-if="report" style="margin-left: 10px">
          {{ overallText[report.overall] }}
          <span v-if="report.warnCount || report.errorCount" style="color: #6b7280">
            （需关注 {{ report.warnCount }}，异常 {{ report.errorCount }}）
          </span>
        </span>
        <div class="grow"></div>
        <span v-if="report" style="margin-right: 10px; color: #6b7280">
          检查时间 {{ report.checkedAt }}
        </span>
        <el-button @click="load">重新检查</el-button>
      </div>

      <el-alert
        type="info"
        :closable="false"
        title="全部为即时计算，不落库、不做历史趋势：这一页回答「平台自己现在有没有毛病」。内置定时任务的运行记录只存内存，重启后会显示「还没到执行时间」。"
      />
    </el-card>

    <el-card v-for="group in report?.groups || []" :key="group.name" style="margin-top: 12px">
      <template #header>{{ group.name }}</template>
      <el-table :data="group.items" border stripe size="small">
        <el-table-column prop="label" label="检查项" width="180" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status].type">
              {{ statusMeta[row.status].text }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="value" label="当前值" min-width="160" />
        <el-table-column prop="detail" label="说明" min-width="360" />
      </el-table>
    </el-card>
  </div>
</template>
