<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import {
  listMyActivity,
  listMyExecJobs,
  listMySessions,
  type AuditLog,
  type ExecJob,
  type TerminalSession
} from '@/api'

const tab = ref('audit')

const auditRows = ref<AuditLog[]>([])
const auditTotal = ref(0)
const auditQuery = reactive({ page: 1, pageSize: 20 })

const sessionRows = ref<TerminalSession[]>([])
const sessionTotal = ref(0)
const sessionQuery = reactive({ page: 1, pageSize: 20 })

const jobRows = ref<ExecJob[]>([])
const jobTotal = ref(0)
const jobQuery = reactive({ page: 1, pageSize: 20 })

const loading = ref(false)

const methodType: Record<string, 'success' | 'warning' | 'danger'> = {
  POST: 'success',
  PUT: 'warning',
  DELETE: 'danger'
}

const sessionStatus: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  active: { text: '进行中', type: 'success' },
  closed: { text: '已结束', type: 'info' },
  error: { text: '异常', type: 'danger' }
}

async function loadAudit() {
  loading.value = true
  try {
    const data = await listMyActivity(auditQuery)
    auditRows.value = data.list || []
    auditTotal.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadSessions() {
  loading.value = true
  try {
    const data = await listMySessions(sessionQuery)
    sessionRows.value = data.list || []
    sessionTotal.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadJobs() {
  loading.value = true
  try {
    const data = await listMyExecJobs(jobQuery)
    jobRows.value = data.list || []
    jobTotal.value = data.total
  } finally {
    loading.value = false
  }
}

function onTabChange(name: string) {
  if (name === 'audit' && !auditRows.value.length) loadAudit()
  if (name === 'session' && !sessionRows.value.length) loadSessions()
  if (name === 'job' && !jobRows.value.length) loadJobs()
}

function formatDuration(ms: number) {
  if (!ms) return '-'
  const sec = Math.round(ms / 1000)
  return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m${sec % 60}s`
}

onMounted(loadAudit)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <el-tabs v-model="tab" @tab-change="onTabChange">
        <el-tab-pane label="操作流水" name="audit">
          <el-alert
            type="info"
            :closable="false"
            style="margin-bottom: 12px"
            title="只记录写操作（POST / PUT / DELETE），读请求不入库"
          />
          <el-table :data="auditRows" border stripe size="small" empty-text="还没有操作记录">
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column label="方法" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="methodType[row.method] || 'info'">{{ row.method }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="path" label="接口" min-width="260" show-overflow-tooltip />
            <el-table-column label="状态码" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">{{ row.status }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="ip" label="来源 IP" width="140" />
            <el-table-column prop="costMs" label="耗时(ms)" width="100" />
            <el-table-column prop="createdAt" label="时间" min-width="180" />
          </el-table>
          <el-pagination
            style="margin-top: 12px; justify-content: flex-end"
            layout="total, prev, pager, next"
            :total="auditTotal"
            v-model:current-page="auditQuery.page"
            :page-size="auditQuery.pageSize"
            @current-change="loadAudit"
          />
        </el-tab-pane>

        <el-tab-pane label="我的会话" name="session">
          <el-table :data="sessionRows" border stripe size="small" empty-text="还没有终端会话">
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column label="主机" min-width="200">
              <template #default="{ row }">
                {{ row.hostName }}
                <span style="color: #6b7280">{{ row.loginUser }}@{{ row.address }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="sessionStatus[row.status]?.type || 'info'">
                  {{ sessionStatus[row.status]?.text || row.status }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="命令 / 拦截" width="120">
              <template #default="{ row }">{{ row.commandCount }} / {{ row.blockedCount }}</template>
            </el-table-column>
            <el-table-column label="时长" width="90">
              <template #default="{ row }">{{ formatDuration(row.durationMs) }}</template>
            </el-table-column>
            <el-table-column prop="startedAt" label="开始时间" min-width="180" />
          </el-table>
          <el-pagination
            style="margin-top: 12px; justify-content: flex-end"
            layout="total, prev, pager, next"
            :total="sessionTotal"
            v-model:current-page="sessionQuery.page"
            :page-size="sessionQuery.pageSize"
            @current-change="loadSessions"
          />
        </el-tab-pane>

        <el-tab-pane label="我的执行" name="job">
          <el-table :data="jobRows" border stripe size="small" empty-text="还没有执行记录">
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column prop="name" label="作业" min-width="140" />
            <el-table-column label="来源" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.source === 'cron' ? 'warning' : 'info'">
                  {{ row.source === 'cron' ? '定时' : '手动' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="command" label="命令" min-width="200" show-overflow-tooltip />
            <el-table-column label="结果" width="140">
              <template #default="{ row }">
                <el-tag type="success" size="small">{{ row.successNum }}</el-tag>
                <el-tag type="danger" size="small" style="margin-left: 4px">{{ row.failedNum }}</el-tag>
                <span style="margin-left: 4px; color: #6b7280">/ {{ row.total }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="开始时间" min-width="180" />
          </el-table>
          <el-pagination
            style="margin-top: 12px; justify-content: flex-end"
            layout="total, prev, pager, next"
            :total="jobTotal"
            v-model:current-page="jobQuery.page"
            :page-size="jobQuery.pageSize"
            @current-change="loadJobs"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>
