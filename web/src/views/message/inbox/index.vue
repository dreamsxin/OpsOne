<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useRouter } from 'vue-router'
import { listMessages, readAllMessages, readMessage, type Message } from '@/api'
import Pagination from '@/components/Pagination.vue'


const router = useRouter()
const loading = ref(false)
const rows = ref<Message[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, unreadOnly: '', type: '' })

const detailVisible = ref(false)
const current = ref<Message | null>(null)

const levelMeta: Record<string, { text: string; type: 'danger' | 'warning' | 'info' }> = {
  critical: { text: '严重', type: 'danger' },
  warning: { text: '重要', type: 'warning' },
  info: { text: '普通', type: 'info' }
}

const typeLabel: Record<string, string> = { announcement: '公告', alert: '告警' }

async function load() {
  loading.value = true
  try {
    const data = await listMessages(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function openDetail(row: Message) {
  current.value = row
  detailVisible.value = true
  if (!row.read) {
    await readMessage(row.id)
    row.read = true
  }
}

async function markAll() {
  const res = await readAllMessages()
  ElMessage.success(`已标记 ${res.updated} 条为已读`)
  load()
}

function jumpToRef(row: Message) {
  detailVisible.value = false
  if (row.type === 'alert') {
    router.push('/monitor/alerts')
  } else {
    router.push('/message/announcement')
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="query.type" placeholder="类型" clearable style="width: 130px">
          <el-option label="公告" value="announcement" />
          <el-option label="告警" value="alert" />
        </el-select>
        <el-checkbox
          :model-value="query.unreadOnly === 'true'"
          @change="((query.unreadOnly = $event ? 'true' : ''), (query.page = 1), load())"
        >
          只看未读
        </el-checkbox>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button @click="markAll">全部标记已读</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="没有消息">
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.read ? 'info' : 'danger'">
              {{ row.read ? '已读' : '未读' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="90">
          <template #default="{ row }">{{ typeLabel[row.type] || row.type }}</template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="levelMeta[row.level]?.type || 'info'">
              {{ levelMeta[row.level]?.text || row.level }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="标题" min-width="260">
          <template #default="{ row }">
            <a
              style="cursor: pointer"
              :style="{ fontWeight: row.read ? 'normal' : '600' }"
              @click="openDetail(row)"
            >
              {{ row.title }}
            </a>
          </template>
        </el-table-column>
        <el-table-column prop="createdAt" label="时间" min-width="180" />
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="detailVisible" :title="current?.title ?? ''" width="560px">
      <div v-if="current">
        <div style="margin-bottom: 12px; color: #6b7280">
          {{ typeLabel[current.type] }} · {{ current.createdAt }}
        </div>
        <div style="white-space: pre-wrap; line-height: 1.8">{{ current.content || '（无正文）' }}</div>
      </div>
      <template #footer>
        <el-button v-if="current" @click="jumpToRef(current)">
          {{ current.type === 'alert' ? '去处理告警' : '查看公告' }}
        </el-button>
        <el-button type="primary" @click="detailVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>
