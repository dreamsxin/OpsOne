<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createCronJob,
  deleteCronJob,
  getExecJob,
  listCronJobs,
  listExecJobs,
  listHosts,
  runCronJobNow,
  updateCronJob,
  type CronJob,
  type ExecJob,
  type Host
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const rows = ref<CronJob[]>([])
const total = ref(0)
const hosts = ref<Host[]>([])
const query = reactive({ page: 1, pageSize: 20, keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  spec: '0 3 * * *',
  command: '',
  hostIds: [] as number[],
  timeout: 60,
  enabled: true
})

const runsVisible = ref(false)
const runs = ref<ExecJob[]>([])
const currentJob = ref<CronJob | null>(null)
const detail = ref<ExecJob | null>(null)

const rules = {
  name: [{ required: true, message: '请输入任务名称', trigger: 'blur' }],
  spec: [{ required: true, message: '请输入 cron 表达式', trigger: 'blur' }],
  command: [{ required: true, message: '请输入要执行的命令', trigger: 'blur' }]
}

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  success: { text: '成功', type: 'success' },
  partial: { text: '部分成功', type: 'warning' },
  failed: { text: '失败', type: 'danger' }
}

const specPresets = [
  { label: '每 5 分钟', value: '*/5 * * * *' },
  { label: '每小时整点', value: '0 * * * *' },
  { label: '每天 03:00', value: '0 3 * * *' },
  { label: '每周一 02:00', value: '0 2 * * 1' },
  { label: '每月 1 号 04:00', value: '0 4 1 * *' }
]

async function load() {
  loading.value = true
  try {
    const data = await listCronJobs(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    spec: '0 3 * * *',
    command: '',
    hostIds: [],
    timeout: 60,
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: CronJob) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    spec: row.spec,
    command: row.command,
    hostIds: row.hostIds || [],
    timeout: row.timeout,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.hostIds.length) {
    ElMessage.warning('请选择至少一台目标主机')
    return
  }

  // 定时任务触发时没人在场，所以「会不会定期动生产机器」要在保存这一刻确认
  const prodHosts = hosts.value.filter((h) => form.hostIds.includes(h.id) && h.env === 'prod')
  if (prodHosts.length) {
    await ElMessageBox.confirm(
      `该任务会按计划在 ${prodHosts.length} 台生产主机上执行（${prodHosts
        .map((h) => h.name)
        .join('、')}），确认保存？`,
      '生产环境确认',
      { type: 'warning' }
    )
  }
  const payload = { ...form, confirmProd: prodHosts.length > 0 }

  if (editingId.value) {
    await updateCronJob(editingId.value, payload)
    ElMessage.success('已更新')
  } else {
    await createCronJob(payload)
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: CronJob) {
  await updateCronJob(row.id, {
    name: row.name,
    spec: row.spec,
    command: row.command,
    hostIds: row.hostIds,
    timeout: row.timeout,
    enabled: row.enabled,
    // 只是启停，不改目标；沿用这条任务已有的生产确认，避免启停时被闸门要求重新确认
    confirmProd: row.prodConfirmed
  })
  ElMessage.success(row.enabled ? '已启用调度' : '已停用调度')
  load()
}

async function runNow(row: CronJob) {
  const prodHosts = hosts.value.filter((h) => row.hostIds.includes(h.id) && h.env === 'prod')
  if (prodHosts.length) {
    await ElMessageBox.confirm(
      `该任务包含 ${prodHosts.length} 台生产主机，确认立即执行？`,
      '生产环境确认',
      { type: 'warning' }
    )
  }

  const job = await runCronJobNow(row.id, prodHosts.length > 0)

  ElMessage.success(`执行完成：成功 ${job.successNum}，失败 ${job.failedNum}`)
  detail.value = job
  currentJob.value = row
  runs.value = []
  runsVisible.value = true
  load()
}

async function remove(row: CronJob) {
  await ElMessageBox.confirm(`确认删除任务「${row.name}」？历史执行记录会保留`, '提示', {
    type: 'warning'
  })
  await deleteCronJob(row.id)
  ElMessage.success('已删除')
  load()
}

async function openRuns(row: CronJob) {
  currentJob.value = row
  detail.value = null
  const data = await listExecJobs({ page: 1, pageSize: 20, cronJobId: row.id })
  runs.value = data.list || []
  runsVisible.value = true
}

async function openRunDetail(job: ExecJob) {
  detail.value = await getExecJob(job.id)
}

function hostLabel(id: number) {
  const host = hosts.value.find((h) => h.id === id)
  return host ? host.name : `#${id}`
}

const resultType: Record<string, 'success' | 'danger' | 'warning'> = {
  success: 'success',
  failed: 'danger',
  timeout: 'warning'
}

onMounted(() => {
  loadHosts()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="任务名称"
          style="width: 200px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'cron:manage'" type="primary" @click="openCreate">新建任务</el-button>
      </div>

      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="cron 表达式为标准五段式（分 时 日 月 周），按服务端本地时区触发；每次触发会生成一条批量执行记录"
      />

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="任务" min-width="140" />
        <el-table-column prop="spec" label="cron" width="120" />
        <el-table-column prop="command" label="命令" min-width="180" show-overflow-tooltip />
        <el-table-column label="目标主机" min-width="150">
          <template #default="{ row }">
            <el-tag v-for="id in row.hostIds.slice(0, 2)" :key="id" size="small" style="margin-right: 4px">
              {{ hostLabel(id) }}
            </el-tag>
            <span v-if="row.hostIds.length > 2" style="color: #6b7280">
              +{{ row.hostIds.length - 2 }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="调度" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="最近结果" width="110">
          <template #default="{ row }">
            <el-tag v-if="row.lastStatus" size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
              {{ statusMeta[row.lastStatus]?.text || row.lastStatus }}
            </el-tag>
            <span v-else style="color: #6b7280">未运行</span>
          </template>
        </el-table-column>
        <el-table-column prop="runCount" label="次数" width="80" />
        <el-table-column prop="lastRunAt" label="最近运行" min-width="180" />
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'cron:run'" link type="primary" @click="runNow(row)">立即执行</el-button>
            <el-button link type="primary" @click="openRuns(row)">记录</el-button>
            <el-button v-perm="'cron:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'cron:manage'" link type="danger" @click="remove(row)">删除</el-button>
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

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑任务' : '新建任务'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="任务名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="cron 表达式" prop="spec">
          <el-input v-model="form.spec" placeholder="分 时 日 月 周，如 0 3 * * *" />
          <div style="margin-top: 6px">
            <el-tag
              v-for="preset in specPresets"
              :key="preset.value"
              size="small"
              style="margin-right: 6px; cursor: pointer"
              @click="form.spec = preset.value"
            >
              {{ preset.label }}
            </el-tag>
          </div>
        </el-form-item>
        <el-form-item label="目标主机">
          <el-select v-model="form.hostIds" multiple filterable style="width: 100%">
            <el-option
              v-for="host in hosts"
              :key="host.id"
              :label="`${host.name}（${host.address}）${host.env}`"
              :value="host.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="单机超时">
          <el-input-number v-model="form.timeout" :min="5" :max="600" />
          <span style="margin-left: 8px; color: #6b7280">秒</span>
        </el-form-item>
        <el-form-item label="命令" prop="command">
          <el-input v-model="form.command" type="textarea" :rows="5" />
        </el-form-item>
        <el-form-item label="启用调度">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="runsVisible" :title="`运行记录 · ${currentJob?.name ?? ''}`" size="55%">
      <el-table v-if="runs.length" :data="runs" border size="small" style="margin-bottom: 12px">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="结果" width="140">
          <template #default="{ row }">
            <el-tag type="success" size="small">{{ row.successNum }}</el-tag>
            <el-tag type="danger" size="small" style="margin-left: 4px">{{ row.failedNum }}</el-tag>
            <span style="margin-left: 4px; color: #6b7280">/ {{ row.total }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="触发方" width="110" />
        <el-table-column prop="startedAt" label="开始时间" min-width="180" />
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openRunDetail(row)">输出</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-collapse v-if="detail?.results?.length">
        <el-collapse-item v-for="item in detail.results" :key="item.id" :name="item.id">
          <template #title>
            <el-tag size="small" :type="resultType[item.status] || 'info'">{{ item.status }}</el-tag>
            <span style="margin-left: 8px">{{ item.hostName }}（{{ item.address }}）</span>
            <span style="margin-left: 8px; color: #6b7280">{{ item.costMs }}ms</span>
          </template>
          <pre class="output-pre">{{ item.stdout || item.stderr || '（无输出）' }}</pre>
        </el-collapse-item>
      </el-collapse>
      <el-empty v-else-if="!runs.length" description="该任务还没有运行记录" />
    </el-drawer>
  </div>
</template>
