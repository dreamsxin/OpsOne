<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getMyResources, type MyResources } from '@/api'

const loading = ref(false)
const data = ref<MyResources | null>(null)

const envLabel: Record<string, string> = { dev: '开发', test: '测试', prod: '生产' }

const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  online: { text: '在线', type: 'success' },
  offline: { text: '离线', type: 'danger' },
  unknown: { text: '未探测', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    data.value = await getMyResources()
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
        <el-tag v-if="data?.scope.all" type="success">数据范围：全部数据</el-tag>
        <template v-else>
          <el-tag v-if="data?.scope.includeSelf" type="warning">含本人录入</el-tag>
          <el-tag type="info">可见部门 {{ data?.scope.deptIds.length ?? 0 }} 个</el-tag>
        </template>
        <span style="color: #6b7280">下列统计均已按你的数据范围过滤</span>
        <div class="grow"></div>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-row :gutter="12">
        <el-col :span="5">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ data?.hostStats.total ?? 0 }}</div>
            <div class="label">可见主机</div>
          </el-card>
        </el-col>
        <el-col :span="5">
          <el-card class="stat-card" shadow="never">
            <div class="value" style="color: #16a34a">{{ data?.hostStats.online ?? 0 }}</div>
            <div class="label">在线</div>
          </el-card>
        </el-col>
        <el-col :span="5">
          <el-card class="stat-card" shadow="never">
            <div class="value" style="color: #dc2626">{{ data?.hostStats.offline ?? 0 }}</div>
            <div class="label">离线</div>
          </el-card>
        </el-col>
        <el-col :span="5">
          <el-card class="stat-card" shadow="never">
            <div class="value" style="color: #d97706">{{ data?.hostStats.prod ?? 0 }}</div>
            <div class="label">其中生产环境</div>
          </el-card>
        </el-col>
        <el-col :span="4">
          <el-card class="stat-card" shadow="never">
            <div class="value">{{ data?.myCronJobs ?? 0 }}</div>
            <div class="label">我的定时任务</div>
          </el-card>
        </el-col>
      </el-row>
    </el-card>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="8">
        <el-card header="按环境分布">
          <el-empty v-if="!data?.byEnv?.length" description="没有可见主机" :image-size="60" />
          <div v-else>
            <div
              v-for="item in data.byEnv"
              :key="item.name"
              style="display: flex; justify-content: space-between; padding: 6px 0"
            >
              <span>{{ envLabel[item.name] || item.name }}</span>
              <el-tag size="small">{{ item.count }}</el-tag>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card header="按部门分布">
          <el-empty v-if="!data?.byDept?.length" description="没有可见主机" :image-size="60" />
          <div v-else>
            <div
              v-for="item in data.byDept"
              :key="item.name"
              style="display: flex; justify-content: space-between; padding: 6px 0"
            >
              <span>{{ item.name }}</span>
              <el-tag size="small">{{ item.count }}</el-tag>
            </div>
          </div>
        </el-card>
      </el-col>
      <el-col :span="8">
        <el-card header="状态概览">
          <div style="display: flex; justify-content: space-between; padding: 6px 0">
            <span>未探测</span>
            <el-tag size="small" type="info">{{ data?.hostStats.unknown ?? 0 }}</el-tag>
          </div>
          <div style="display: flex; justify-content: space-between; padding: 6px 0">
            <span>在线率</span>
            <el-tag size="small" type="success">
              {{
                data?.hostStats.total
                  ? Math.round(((data.hostStats.online || 0) / data.hostStats.total) * 100) + '%'
                  : '-'
              }}
            </el-tag>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px" header="可见主机（离线优先）">
      <el-table :data="data?.hosts || []" size="small" border empty-text="没有可见主机">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column label="连接" min-width="180">
          <template #default="{ row }">{{ row.username }}@{{ row.address }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column label="环境" width="90">
          <template #default="{ row }">{{ envLabel[row.env] || row.env }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="osInfo" label="系统" min-width="160" show-overflow-tooltip />
        <el-table-column prop="checkedAt" label="最近探测" min-width="170" />
      </el-table>
    </el-card>
  </div>
</template>
