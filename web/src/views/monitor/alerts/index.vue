<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ackAlert,
  getAlert,
  getAlertStats,
  listAlerts,
  resolveAlert,
  type Alert,
  type AlertStats,
  type NotifyRecord
} from '@/api'

const loading = ref(false)
const rows = ref<Alert[]>([])
const total = ref(0)
const stats = ref<AlertStats | null>(null)
const query = reactive({ page: 1, pageSize: 20, status: '', severity: '', keyword: '' })

const detailVisible = ref(false)
const current = ref<Alert | null>(null)
const records = ref<NotifyRecord[]>([])

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

function filterBy(status: string) {
  query.status = status
  query.page = 1
  load()
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

onMounted(load)
</script>

<template>
  <div class="page">
    <el-row :gutter="12">
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('firing')">
          <div class="value" style="color: #dc2626">{{ stats?.firing ?? 0 }}</div>
          <div class="label">触发中</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('acked')">
          <div class="value" style="color: #d97706">{{ stats?.acked ?? 0 }}</div>
          <div class="label">已确认</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('resolved')">
          <div class="value" style="color: #16a34a">{{ stats?.resolved ?? 0 }}</div>
          <div class="label">已恢复</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('')">
          <div class="value">{{ stats?.critical ?? 0 }} / {{ stats?.warning ?? 0 }}</div>
          <div class="label">未恢复的严重 / 警告</div>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px">
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="标题 / 摘要 / 标签"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.severity" placeholder="级别" clearable style="width: 120px">
          <el-option label="严重" value="critical" />
          <el-option label="警告" value="warning" />
          <el-option label="提示" value="info" />
        </el-select>
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option label="触发中" value="firing" />
          <el-option label="已确认" value="acked" />
          <el-option label="已恢复" value="resolved" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="暂无告警，可在「配置中心 → Webhook 接入」创建接入源后推送">
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
        <el-table-column prop="lastSeenAt" label="最近出现" min-width="180" />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
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

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
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
