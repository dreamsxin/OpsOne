<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createNotifyChannel,
  deleteNotifyChannel,
  listNotifyChannels,
  testNotifyChannel,
  updateNotifyChannel,
  type NotifyChannel
} from '@/api'

const loading = ref(false)
const rows = ref<NotifyChannel[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  type: 'webhook' as 'webhook' | 'silent',
  url: '',
  headerKey: '',
  headerValue: '',
  remark: '',
  enabled: true
})

const rules = {
  name: [{ required: true, message: '请输入渠道名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listNotifyChannels()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    type: 'webhook',
    url: '',
    headerKey: '',
    headerValue: '',
    remark: '',
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: NotifyChannel) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    type: row.type,
    url: row.url,
    headerKey: row.headerKey,
    headerValue: '',
    remark: row.remark,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (form.type === 'webhook' && !form.url) {
    ElMessage.warning('webhook 渠道必须填写 URL')
    return
  }

  if (editingId.value) {
    await updateNotifyChannel(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createNotifyChannel({ ...form })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: NotifyChannel) {
  await updateNotifyChannel(row.id, {
    name: row.name,
    type: row.type,
    url: row.url,
    headerKey: row.headerKey,
    remark: row.remark,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
}

async function test(row: NotifyChannel) {
  const res = await testNotifyChannel(row.id)
  if (res.ok) {
    ElMessage.success(`发送成功（HTTP ${res.httpStatus ?? '-'}，${res.costMs ?? 0}ms）`)
  } else {
    ElMessage.error(`发送失败：${res.detail}`)
  }
}

async function remove(row: NotifyChannel) {
  await ElMessageBox.confirm(`确认删除渠道「${row.name}」？`, '提示', { type: 'warning' })
  await deleteNotifyChannel(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="webhook 渠道以 HTTP POST 投递 JSON 报文；silent 渠道只落通知记录、不外发，可用于灰度或临时静默"
      />

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'channel:manage'" type="primary" @click="openCreate">新增渠道</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="渠道" min-width="140" />
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.type === 'webhook' ? 'primary' : 'info'">{{ row.type }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="url" label="地址" min-width="240" show-overflow-tooltip />
        <el-table-column prop="headerKey" label="鉴权头" width="130" />
        <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'channel:manage'" link type="primary" @click="test(row)">试发</el-button>
            <el-button v-perm="'channel:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'channel:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑渠道' : '新增渠道'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="渠道名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type">
            <el-radio value="webhook">webhook</el-radio>
            <el-radio value="silent">silent（仅记录）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook'" label="URL">
          <el-input v-model="form.url" placeholder="https://..." />
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook'" label="鉴权头名">
          <el-input v-model="form.headerKey" placeholder="可选，如 X-Token" />
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook'" label="鉴权头值">
          <el-input
            v-model="form.headerValue"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '可选'"
          />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
