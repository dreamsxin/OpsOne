<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createAnnouncement,
  deleteAnnouncement,
  listAnnouncements,
  publishAnnouncement,
  unpublishAnnouncement,
  updateAnnouncement,
  type Announcement
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const rows = ref<Announcement[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, published: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ title: '', content: '', level: 'info' as 'info' | 'warning' })

const rules = {
  title: [{ required: true, message: '请输入公告标题', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    const data = await listAnnouncements(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { title: '', content: '', level: 'info' })
  dialogVisible.value = true
}

function openEdit(row: Announcement) {
  editingId.value = row.id
  Object.assign(form, { title: row.title, content: row.content, level: row.level })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateAnnouncement(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createAnnouncement({ ...form })
    ElMessage.success('已创建，发布后才对其他人可见')
  }
  dialogVisible.value = false
  load()
}

async function publish(row: Announcement) {
  await ElMessageBox.confirm('发布后会给所有启用用户投递一条站内消息，确认发布？', '发布公告', {
    type: 'info'
  })
  const res = await publishAnnouncement(row.id)
  ElMessage.success(`已发布，投递站内消息 ${res.messageSent} 条`)
  load()
}

async function unpublish(row: Announcement) {
  await ElMessageBox.confirm('下线后会撤回尚未读的站内消息，已读记录保留，确认下线？', '下线公告', {
    type: 'warning'
  })
  await unpublishAnnouncement(row.id)
  ElMessage.success('已下线')
  load()
}

async function remove(row: Announcement) {
  await ElMessageBox.confirm(`确认删除公告「${row.title}」？关联的站内消息会一并清理`, '危险操作', {
    type: 'warning'
  })
  await deleteAnnouncement(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="query.published" placeholder="发布状态" clearable style="width: 140px">
          <el-option label="已发布" value="true" />
          <el-option label="未发布" value="false" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'announcement:manage'" type="primary" @click="openCreate">新建公告</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="title" label="标题" min-width="220" show-overflow-tooltip />
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.level === 'warning' ? 'warning' : 'info'">
              {{ row.level === 'warning' ? '重要' : '普通' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.published ? 'success' : 'info'">
              {{ row.published ? '已发布' : '未发布' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="publisher" label="发布人" width="110" />
        <el-table-column prop="publishedAt" label="发布时间" min-width="180" />
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="!row.published"
              v-perm="'announcement:manage'"
              link
              type="success"
              @click="publish(row)"
            >
              发布
            </el-button>
            <el-button v-else v-perm="'announcement:manage'" link type="warning" @click="unpublish(row)">
              下线
            </el-button>
            <el-button v-perm="'announcement:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'announcement:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑公告' : '新建公告'" width="600px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="80px">
        <el-form-item label="标题" prop="title">
          <el-input v-model="form.title" />
        </el-form-item>
        <el-form-item label="级别">
          <el-radio-group v-model="form.level">
            <el-radio value="info">普通</el-radio>
            <el-radio value="warning">重要</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="内容">
          <el-input v-model="form.content" type="textarea" :rows="8" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
