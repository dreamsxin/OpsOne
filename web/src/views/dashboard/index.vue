<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getDashboardStats } from '@/api'

interface Stats {
  hostTotal: number
  online: number
  offline: number
  userTotal: number
  jobTotal: number
  envStats: { env: string; count: number }[] | null
  recentJobs: { id: number; name: string; operator: string; total: number; successNum: number; failedNum: number; startedAt: string }[]
}

const stats = ref<Stats | null>(null)
const loading = ref(false)

const envLabel: Record<string, string> = { dev: '开发', test: '测试', prod: '生产' }

onMounted(async () => {
  loading.value = true
  try {
    stats.value = await getDashboardStats()
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="page" v-loading="loading">
    <el-row :gutter="12">
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="value">{{ stats?.hostTotal ?? 0 }}</div>
          <div class="label">纳管主机</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="value" style="color: #16a34a">{{ stats?.online ?? 0 }}</div>
          <div class="label">在线</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="value" style="color: #dc2626">{{ stats?.offline ?? 0 }}</div>
          <div class="label">离线</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card">
          <div class="value">{{ stats?.jobTotal ?? 0 }}</div>
          <div class="label">执行作业</div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="8">
        <el-card header="环境分布">
          <el-empty v-if="!stats?.envStats?.length" description="暂无主机" />
          <div v-else>
            <div
              v-for="item in stats.envStats"
              :key="item.env"
              style="display: flex; justify-content: space-between; padding: 6px 0"
            >
              <span>{{ envLabel[item.env] || item.env }}</span>
              <el-tag size="small">{{ item.count }}</el-tag>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="16">
        <el-card header="最近执行">
          <el-table :data="stats?.recentJobs || []" size="small" empty-text="暂无执行记录">
            <el-table-column prop="name" label="作业" min-width="120" />
            <el-table-column prop="operator" label="操作人" width="110" />
            <el-table-column prop="total" label="主机数" width="80" />
            <el-table-column label="结果" width="140">
              <template #default="{ row }">
                <el-tag type="success" size="small">成功 {{ row.successNum }}</el-tag>
                <el-tag type="danger" size="small" style="margin-left: 4px">失败 {{ row.failedNum }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="开始时间" min-width="180" />
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>
