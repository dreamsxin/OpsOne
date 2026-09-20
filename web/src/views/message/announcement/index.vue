<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { listPublishedAnnouncements, type Announcement } from '@/api'

const loading = ref(false)
const rows = ref<Announcement[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 10 })

async function load() {
  loading.value = true
  try {
    const data = await listPublishedAnnouncements(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <el-empty v-if="!rows.length" description="暂无公告" />
      <el-timeline v-else>
        <el-timeline-item
          v-for="item in rows"
          :key="item.id"
          :timestamp="item.publishedAt || item.createdAt"
          placement="top"
          :type="item.level === 'warning' ? 'warning' : 'primary'"
        >
          <el-card shadow="never">
            <div style="display: flex; align-items: center; gap: 8px">
              <el-tag v-if="item.level === 'warning'" size="small" type="warning">重要</el-tag>
              <strong style="font-size: 15px">{{ item.title }}</strong>
              <span style="margin-left: auto; color: #6b7280">{{ item.publisher }}</span>
            </div>
            <div style="margin-top: 8px; white-space: pre-wrap; line-height: 1.8; color: #374151">
              {{ item.content || '（无正文）' }}
            </div>
          </el-card>
        </el-timeline-item>
      </el-timeline>

      <el-pagination
        v-if="total > query.pageSize"
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
