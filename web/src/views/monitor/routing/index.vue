<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createNotifyRoute,
  deleteNotifyRoute,
  listNotifyChannels,
  listNotifyRoutes,
  testNotifyRoute,
  updateNotifyRoute,
  type NotifyChannel,
  type NotifyRoute
} from '@/api'

const loading = ref(false)
const rows = ref<NotifyRoute[]>([])
const channels = ref<NotifyChannel[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  priority: 100,
  matchSeverity: [] as string[],
  matchLabels: '',
  channelIds: [] as number[],
  isDefault: false,
  enabled: true
})

const testForm = reactive({ severity: 'critical', labels: '{"env":"prod"}' })
const testResult = ref('')

const rules = {
  name: [{ required: true, message: '请输入路由名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listNotifyRoutes()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    priority: 100,
    matchSeverity: [],
    matchLabels: '',
    channelIds: [],
    isDefault: false,
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: NotifyRoute) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    priority: row.priority,
    matchSeverity: row.matchSeverity ? row.matchSeverity.split(',') : [],
    matchLabels: row.matchLabels,
    channelIds: row.channelIds || [],
    isDefault: row.isDefault,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

function buildPayload() {
  return {
    name: form.name,
    priority: form.priority,
    matchSeverity: form.matchSeverity.join(','),
    matchLabels: form.matchLabels,
    channelIds: form.channelIds,
    isDefault: form.isDefault,
    enabled: form.enabled
  }
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.channelIds.length) {
    ElMessage.warning('请至少选择一个通知渠道')
    return
  }

  if (editingId.value) {
    await updateNotifyRoute(editingId.value, buildPayload())
    ElMessage.success('已更新')
  } else {
    await createNotifyRoute(buildPayload())
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: NotifyRoute) {
  await updateNotifyRoute(row.id, {
    name: row.name,
    priority: row.priority,
    matchSeverity: row.matchSeverity,
    matchLabels: row.matchLabels,
    channelIds: row.channelIds,
    isDefault: row.isDefault,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
  load()
}

async function remove(row: NotifyRoute) {
  await ElMessageBox.confirm(`确认删除路由「${row.name}」？`, '提示', { type: 'warning' })
  await deleteNotifyRoute(row.id)
  ElMessage.success('已删除')
  load()
}

async function runTest() {
  let labels: Record<string, string> = {}
  if (testForm.labels.trim()) {
    try {
      labels = JSON.parse(testForm.labels)
    } catch {
      ElMessage.error('标签必须是合法 JSON 对象')
      return
    }
  }

  const res = await testNotifyRoute({ severity: testForm.severity, labels })
  if (!res.matched) {
    testResult.value = '未命中任何路由，也没有兜底路由，这类告警不会产生通知'
    return
  }
  const names = (res.channelIds || []).map((id) => channelName(id)).join('、')
  testResult.value = `${res.fallback ? '回落到兜底路由' : '命中路由'}「${res.routeName}」，投递渠道：${names}`
}

function channelName(id: number) {
  const channel = channels.value.find((c) => c.id === id)
  return channel ? channel.name : `#${id}`
}

onMounted(async () => {
  channels.value = await listNotifyChannels()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          按优先级升序取<strong>第一条命中</strong>的路由（级别为空表示不限，标签条件需全部命中）；
          都没命中时回落到兜底路由；没有兜底路由则该告警不产生通知。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="testForm.severity" style="width: 110px">
          <el-option label="严重" value="critical" />
          <el-option label="警告" value="warning" />
          <el-option label="提示" value="info" />
        </el-select>
        <el-input v-model="testForm.labels" placeholder='样例标签 JSON，如 {"env":"prod"}' style="width: 240px" />
        <el-button @click="runTest">选路预演</el-button>
        <span style="color: #6b7280">{{ testResult }}</span>
        <div class="grow"></div>
        <el-button v-perm="'route:manage'" type="primary" @click="openCreate">新增路由</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="priority" label="优先级" width="90" />
        <el-table-column prop="name" label="路由" min-width="140">
          <template #default="{ row }">
            {{ row.name }}
            <el-tag v-if="row.isDefault" size="small" type="warning" style="margin-left: 4px">兜底</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="级别条件" min-width="140">
          <template #default="{ row }">{{ row.matchSeverity || '不限' }}</template>
        </el-table-column>
        <el-table-column label="标签条件" min-width="180">
          <template #default="{ row }">{{ row.matchLabels || '不限' }}</template>
        </el-table-column>
        <el-table-column label="渠道" min-width="180">
          <template #default="{ row }">
            <el-tag v-for="id in row.channelIds" :key="id" size="small" style="margin-right: 4px">
              {{ channelName(id) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'route:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'route:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑路由' : '新增路由'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="路由名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="优先级">
          <el-input-number v-model="form.priority" :min="1" :max="9999" />
          <span style="margin-left: 8px; color: #6b7280">数字越小越先匹配</span>
        </el-form-item>
        <el-form-item label="级别条件">
          <el-select v-model="form.matchSeverity" multiple placeholder="不限" style="width: 100%">
            <el-option label="严重" value="critical" />
            <el-option label="警告" value="warning" />
            <el-option label="提示" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="标签条件">
          <el-input v-model="form.matchLabels" type="textarea" :rows="2" placeholder='JSON 对象，如 {"env":"prod","service":"nginx"}' />
        </el-form-item>
        <el-form-item label="通知渠道">
          <el-select v-model="form.channelIds" multiple style="width: 100%">
            <el-option
              v-for="channel in channels"
              :key="channel.id"
              :label="`${channel.name}（${channel.type}）`"
              :value="channel.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="作为兜底">
          <el-switch v-model="form.isDefault" />
          <span style="margin-left: 8px; color: #6b7280">兜底路由只允许一条，设置后会自动取消其他</span>
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
