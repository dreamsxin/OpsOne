<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { listNotifyChannels, listNotifyRecords, type NotifyChannel, type NotifyRecord } from '@/api'

const loading = ref(false)
const rows = ref<NotifyRecord[]>([])
const total = ref(0)
const channels = ref<NotifyChannel[]>([])
const query = reactive({ page: 1, pageSize: 20, status: '', channelId: '' })

async function load() {
  loading.value = true
  try {
    const data = await listNotifyRecords(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  channels.value = await listNotifyChannels()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="query.status" placeholder="结果" clearable style="width: 120px">
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
        </el-select>
        <el-select v-model="query.channelId" placeholder="渠道" clearable style="width: 180px">
          <el-option
            v-for="channel in channels"
            :key="channel.id"
            :label="channel.name"
            :value="String(channel.id)"
          />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有通知投递记录">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="告警" min-width="220">
          <template #default="{ row }">
            <span style="color: #6b7280">#{{ row.alertId }}</span>
            {{ row.alertTitle }}
          </template>
        </el-table-column>
        <el-table-column prop="routeName" label="路由" width="140" />
        <el-table-column prop="channelName" label="渠道" width="140" />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
              {{ row.status === 'success' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="httpStatus" label="HTTP" width="80" />
        <el-table-column prop="costMs" label="耗时(ms)" width="100" />
        <el-table-column prop="errorMsg" label="错误" min-width="200" show-overflow-tooltip />
        <el-table-column prop="createdAt" label="时间" min-width="180" />
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
  </div>
</template>
