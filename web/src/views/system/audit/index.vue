<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { listAuditLogs, type AuditLog } from '@/api'

const loading = ref(false)
const rows = ref<AuditLog[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, username: '' })

async function load() {
  loading.value = true
  try {
    const data = await listAuditLogs(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

const methodType: Record<string, 'success' | 'warning' | 'danger'> = {
  POST: 'success',
  PUT: 'warning',
  DELETE: 'danger'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-input
          v-model="query.username"
          placeholder="操作人"
          style="width: 200px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
      </div>

      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="仅记录写操作（POST / PUT / DELETE），读请求不入库以控制数据量"
      />

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="username" label="操作人" width="120" />
        <el-table-column label="方法" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="methodType[row.method] || 'info'">{{ row.method }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="path" label="接口" min-width="220" show-overflow-tooltip />
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
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>
  </div>
</template>
