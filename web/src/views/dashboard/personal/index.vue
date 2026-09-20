<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getPersonalWorkbench, type PersonalWorkbench } from '@/api'

const router = useRouter()
const loading = ref(false)
const data = ref<PersonalWorkbench | null>(null)

const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  active: { text: '进行中', type: 'success' },
  closed: { text: '已结束', type: 'info' },
  error: { text: '异常', type: 'danger' }
}

const cronStatusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  success: { text: '成功', type: 'success' },
  partial: { text: '部分成功', type: 'warning' },
  failed: { text: '失败', type: 'danger' }
}

async function load() {
  loading.value = true
  try {
    data.value = await getPersonalWorkbench()
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <div style="display: flex; align-items: center; gap: 12px">
        <el-avatar :size="48"><el-icon :size="24"><UserFilled /></el-icon></el-avatar>
        <div>
          <div style="font-size: 17px; font-weight: 600">
            {{ data?.profile.nickname || data?.profile.username }}
            <span style="color: #6b7280; font-size: 13px">@{{ data?.profile.username }}</span>
          </div>
          <div style="color: #6b7280; font-size: 13px">
            上次登录：{{ data?.profile.lastLoginAt || '首次登录' }}
          </div>
        </div>
        <div class="grow" style="flex: 1"></div>
        <el-button @click="load">刷新</el-button>
      </div>
    </el-card>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="router.push('/message/inbox')">
          <div class="value" :style="{ color: data?.todo.unreadMessages ? '#dc2626' : undefined }">
            {{ data?.todo.unreadMessages ?? 0 }}
          </div>
          <div class="label">未读消息</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="router.push('/monitor/alerts')">
          <div class="value" style="color: #d97706">{{ data?.todo.firingAlerts ?? 0 }}</div>
          <div class="label">未恢复告警（含严重 {{ data?.todo.criticalAlerts ?? 0 }}）</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="router.push('/asset/host')">
          <div class="value">{{ data?.mine.hosts ?? 0 }}</div>
          <div class="label">我录入的主机</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="router.push('/execute/scheduler')">
          <div class="value">{{ data?.mine.cronJobs ?? 0 }}</div>
          <div class="label">我创建的定时任务</div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="12">
        <el-card header="我最近的批量执行">
          <el-table :data="data?.recentJobs || []" size="small" empty-text="还没有执行记录">
            <el-table-column prop="id" label="ID" width="60" />
            <el-table-column prop="name" label="作业" min-width="120" show-overflow-tooltip />
            <el-table-column label="结果" width="130">
              <template #default="{ row }">
                <el-tag type="success" size="small">{{ row.successNum }}</el-tag>
                <el-tag type="danger" size="small" style="margin-left: 4px">{{ row.failedNum }}</el-tag>
                <span style="margin-left: 4px; color: #6b7280">/ {{ row.total }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="时间" min-width="160" />
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card header="我最近的终端会话">
          <el-table :data="data?.recentSessions || []" size="small" empty-text="还没有会话记录">
            <el-table-column prop="id" label="ID" width="60" />
            <el-table-column label="主机" min-width="150">
              <template #default="{ row }">{{ row.hostName }}（{{ row.address }}）</template>
            </el-table-column>
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
                  {{ statusMeta[row.status]?.text || row.status }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="拦截" width="80">
              <template #default="{ row }">
                <el-tag v-if="row.blockedCount" size="small" type="danger">{{ row.blockedCount }}</el-tag>
                <span v-else style="color: #9ca3af">0</span>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="时间" min-width="160" />
          </el-table>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px" header="我的定时任务（启用中）">
      <el-table :data="data?.cronJobList || []" size="small" empty-text="没有启用中的定时任务">
        <el-table-column prop="name" label="任务" min-width="140" />
        <el-table-column prop="spec" label="cron" width="120" />
        <el-table-column prop="command" label="命令" min-width="200" show-overflow-tooltip />
        <el-table-column label="最近结果" width="110">
          <template #default="{ row }">
            <el-tag v-if="row.lastStatus" size="small" :type="cronStatusMeta[row.lastStatus]?.type || 'info'">
              {{ cronStatusMeta[row.lastStatus]?.text || row.lastStatus }}
            </el-tag>
            <span v-else style="color: #6b7280">未运行</span>
          </template>
        </el-table-column>
        <el-table-column prop="lastRunAt" label="最近运行" min-width="170" />
      </el-table>
    </el-card>
  </div>
</template>
