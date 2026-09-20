<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getRetentionStatus,
  runRetention,
  type RetentionRunItem,
  type RetentionStatus
} from '@/api'

const loading = ref(false)
const status = ref<RetentionStatus | null>(null)
const running = ref(false)
const runItems = ref<RetentionRunItem[]>([])
const runTitle = ref('')

async function load() {
  loading.value = true
  try {
    status.value = await getRetentionStatus()
  } finally {
    loading.value = false
  }
}

async function dryRun() {
  running.value = true
  try {
    const res = await runRetention(true)
    runItems.value = res.items
    runTitle.value = `试算结果：${res.detail}`
    ElMessage.info(res.detail)
  } finally {
    running.value = false
  }
}

async function execute() {
  if (!status.value) return
  await ElMessageBox.confirm(
    `将按保留天数删除超期数据，共约 ${status.value.expiredTotal} 行，删除后无法恢复。确认执行？`,
    '执行清理',
    { type: 'warning', confirmButtonText: '确认清理' }
  )
  running.value = true
  try {
    const res = await runRetention(false)
    runItems.value = res.items
    runTitle.value = `清理结果：${res.detail}`
    ElMessage.success(res.detail)
    load()
  } finally {
    running.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="保留天数在「系统管理 → 系统配置」的 retention 分组里改，0 表示永久保留。默认每天 03:30 自动清理（OPS_RETENTION_SPEC 可改或关闭）。建议先「试算」看会删多少，再执行。"
      />

      <div class="page-toolbar">
        <span v-if="status">
          可清理 <strong>{{ status.expiredTotal }}</strong> 行
          <span style="color: #6b7280">
            · 定时表达式 {{ status.spec || '未启用' }} · 最近执行 {{ status.lastRun.info }}
          </span>
        </span>
        <div class="grow"></div>
        <el-button @click="load">刷新</el-button>
        <el-button v-perm="'retention:run'" :loading="running" @click="dryRun">试算</el-button>
        <el-button
          v-perm="'retention:run'"
          type="danger"
          :loading="running"
          :disabled="!status?.expiredTotal"
          @click="execute"
        >
          执行清理
        </el-button>
      </div>

      <el-table :data="status?.targets || []" border stripe>
        <el-table-column prop="label" label="数据类型" width="140" />
        <el-table-column label="保留天数" width="110">
          <template #default="{ row }">
            <el-tag v-if="row.days <= 0" size="small" type="warning">永久保留</el-tag>
            <span v-else>{{ row.days }} 天</span>
          </template>
        </el-table-column>
        <el-table-column prop="total" label="当前行数" width="110" />
        <el-table-column label="可清理" width="110">
          <template #default="{ row }">
            <span :style="{ color: row.expired ? '#d97706' : '#6b7280' }">{{ row.expired }}</span>
          </template>
        </el-table-column>
        <el-table-column label="连带删除" min-width="150">
          <template #default="{ row }">{{ row.cascade || '—' }}</template>
        </el-table-column>
        <el-table-column label="说明" min-width="240">
          <template #default="{ row }">
            {{ row.note }}
            <el-tag v-if="row.irreversible" size="small" type="danger" style="margin-left: 6px">
              删后无法追溯
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="key" label="配置键" min-width="200" />
      </el-table>
    </el-card>

    <el-card v-if="runItems.length" style="margin-top: 12px">
      <template #header>{{ runTitle }}</template>
      <el-table :data="runItems" border stripe size="small">
        <el-table-column prop="label" label="数据类型" width="140" />
        <el-table-column label="保留天数" width="100">
          <template #default="{ row }">{{ row.days > 0 ? `${row.days} 天` : '永久' }}</template>
        </el-table-column>
        <el-table-column prop="deleted" label="行数" width="100" />
        <el-table-column label="结果" min-width="240">
          <template #default="{ row }">
            <span v-if="row.skipped" style="color: #6b7280">{{ row.reason }}</span>
            <span v-else>早于 {{ row.deadline }} 的数据</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>
