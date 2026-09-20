<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createAlertSource,
  deleteAlertSource,
  listAlertSources,
  rotateAlertSourceToken,
  updateAlertSource,
  type AlertSource
} from '@/api'

const loading = ref(false)
const rows = ref<AlertSource[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ name: '', remark: '', enabled: true })

const detailVisible = ref(false)
const current = ref<AlertSource | null>(null)

const rules = {
  name: [{ required: true, message: '请输入接入源名称', trigger: 'blur' }]
}

const pushUrl = computed(() =>
  current.value ? `${location.origin}/api/v1/webhooks/alerts/${current.value.token}` : ''
)

const curlSample = computed(
  () => `curl -X POST ${pushUrl.value} \\
  -H "Content-Type: application/json" \\
  -d '{
    "title": "磁盘使用率超过阈值",
    "summary": "/data 使用率 92%，持续 5 分钟",
    "severity": "critical",
    "value": "92%",
    "labels": {"host": "web-01", "env": "prod", "service": "nginx"}
  }'`
)

async function load() {
  loading.value = true
  try {
    rows.value = await listAlertSources()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { name: '', remark: '', enabled: true })
  dialogVisible.value = true
}

function openEdit(row: AlertSource) {
  editingId.value = row.id
  Object.assign(form, { name: row.name, remark: row.remark, enabled: row.enabled })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateAlertSource(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    const created = await createAlertSource({ ...form })
    ElMessage.success('已创建，请复制推送地址')
    current.value = created
    detailVisible.value = true
  }
  dialogVisible.value = false
  load()
}

function openDetail(row: AlertSource) {
  current.value = row
  detailVisible.value = true
}

async function rotate(row: AlertSource) {
  await ElMessageBox.confirm('重置后旧推送地址立即失效，需要同步修改上游配置，确认继续？', '重置 Token', {
    type: 'warning'
  })
  const updated = await rotateAlertSourceToken(row.id)
  ElMessage.success('Token 已重置')
  current.value = updated
  detailVisible.value = true
  load()
}

async function toggleEnabled(row: AlertSource) {
  await updateAlertSource(row.id, { name: row.name, remark: row.remark, enabled: row.enabled })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
}

async function remove(row: AlertSource) {
  await ElMessageBox.confirm(`确认删除接入源「${row.name}」？推送地址立即失效`, '危险操作', {
    type: 'warning'
  })
  await deleteAlertSource(row.id)
  ElMessage.success('已删除')
  load()
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('浏览器拒绝了剪贴板访问，请手动复制')
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          每个接入源有独立的推送地址（Token 在 URL 里），外部监控系统直接 POST JSON 即可。
          告警按「标题 + 标签」生成指纹去重，同指纹重复上报只累加次数，不会重复通知。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'source:manage'" type="primary" @click="openCreate">新增接入源</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="接入源" min-width="150" />
        <el-table-column prop="remark" label="备注" min-width="150" show-overflow-tooltip />
        <el-table-column prop="receivedCount" label="累计接收" width="100" />
        <el-table-column prop="lastSeenAt" label="最近上报" min-width="180" />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">推送地址</el-button>
            <el-button v-perm="'source:manage'" link type="warning" @click="rotate(row)">重置</el-button>
            <el-button v-perm="'source:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'source:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑接入源' : '新增接入源'" width="480px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如 Prometheus 生产集群" />
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

    <el-drawer v-model="detailVisible" :title="`推送地址 · ${current?.name ?? ''}`" size="50%">
      <el-form label-width="90px">
        <el-form-item label="推送地址">
          <el-input :model-value="pushUrl" readonly>
            <template #append>
              <el-button @click="copy(pushUrl)">复制</el-button>
            </template>
          </el-input>
        </el-form-item>
      </el-form>

      <el-alert
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="该地址等同于凭据，泄露后任何人都能往平台灌告警；怀疑泄露时用「重置」换新地址"
      />

      <el-divider content-position="left">推送示例</el-divider>
      <pre class="output-pre">{{ curlSample }}</pre>
      <el-button size="small" style="margin-top: 8px" @click="copy(curlSample)">复制示例</el-button>

      <el-divider content-position="left">字段说明</el-divider>
      <ul style="line-height: 2; color: #4b5563">
        <li><code>title</code> 必填，告警标题</li>
        <li><code>severity</code> 可选，critical / warning / info，其他值归为 info</li>
        <li><code>labels</code> 可选，键值对，用于通知路由匹配</li>
        <li><code>status</code> 可选，传 <code>resolved</code> 表示恢复，会关闭同指纹的活跃告警</li>
        <li><code>fingerprint</code> 可选，不传则由标题与标签自动生成</li>
        <li>批量上报：把多条放进 <code>alerts</code> 数组一次提交</li>
      </ul>
    </el-drawer>
  </div>
</template>
