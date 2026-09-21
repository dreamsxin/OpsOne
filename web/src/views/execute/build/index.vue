<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createBuildJob,
  createBuildServer,
  deleteBuildJob,
  deleteBuildServer,
  listBuildJobs,
  listBuildRecords,
  listBuildServers,
  syncBuildRecord,
  testBuildServer,
  triggerBuildJob,
  updateBuildJob,
  updateBuildServer,
  type BuildJob,
  type BuildRecord,
  type BuildServer
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const tab = ref('jobs')

// ---------- 构建任务 ----------
const jobLoading = ref(false)
const jobs = ref<BuildJob[]>([])
const jobTotal = ref(0)
const jobQuery = reactive({ page: 1, pageSize: 20, keyword: '' })

const jobDialog = ref(false)
const jobEditingId = ref<number | null>(null)
const jobFormRef = ref<FormInstance>()
const jobForm = reactive({
  name: '',
  serverId: undefined as number | undefined,
  jobPath: '',
  params: '',
  enabled: true,
  remark: ''
})
const jobRules = {
  name: [{ required: true, message: '请输入任务名称', trigger: 'blur' }],
  serverId: [{ required: true, message: '请选择 Jenkins 服务器', trigger: 'change' }],
  jobPath: [{ required: true, message: '请输入 job 路径', trigger: 'blur' }]
}

const triggerDialog = ref(false)
const triggerJob = ref<BuildJob | null>(null)
const triggerParams = ref<{ key: string; value: string }[]>([])

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  triggered: { text: '已提交', type: 'info' },
  running: { text: '构建中', type: 'warning' },
  success: { text: '成功', type: 'success' },
  failure: { text: '失败', type: 'danger' },
  unstable: { text: '不稳定', type: 'warning' },
  aborted: { text: '已中止', type: 'info' },
  unknown: { text: '未知', type: 'info' }
}

async function loadJobs() {
  jobLoading.value = true
  try {
    const data = await listBuildJobs(jobQuery)
    jobs.value = data.list || []
    jobTotal.value = data.total
  } finally {
    jobLoading.value = false
  }
}

function openJobCreate() {
  jobEditingId.value = null
  Object.assign(jobForm, {
    name: '',
    serverId: servers.value[0]?.id,
    jobPath: '',
    params: '',
    enabled: true,
    remark: ''
  })
  jobDialog.value = true
}

function openJobEdit(row: BuildJob) {
  jobEditingId.value = row.id
  Object.assign(jobForm, {
    name: row.name,
    serverId: row.serverId,
    jobPath: row.jobPath,
    params: row.params,
    enabled: row.enabled,
    remark: row.remark
  })
  jobDialog.value = true
}

async function submitJob() {
  const valid = await jobFormRef.value?.validate().catch(() => false)
  if (!valid) return
  if (jobEditingId.value) {
    await updateBuildJob(jobEditingId.value, { ...jobForm })
    ElMessage.success('已更新')
  } else {
    await createBuildJob({ ...jobForm })
    ElMessage.success('已创建')
  }
  jobDialog.value = false
  loadJobs()
}

async function removeJob(row: BuildJob) {
  await ElMessageBox.confirm(`确认删除构建任务「${row.name}」？历史构建记录会保留`, '提示', {
    type: 'warning'
  })
  await deleteBuildJob(row.id)
  ElMessage.success('已删除')
  loadJobs()
}

function openTrigger(row: BuildJob) {
  triggerJob.value = row
  triggerParams.value = []
  if (row.params) {
    try {
      const parsed = JSON.parse(row.params) as Record<string, string>
      triggerParams.value = Object.entries(parsed).map(([key, value]) => ({ key, value }))
    } catch {
      ElMessage.warning('任务默认参数不是合法 JSON，已忽略')
    }
  }
  triggerDialog.value = true
}

function addTriggerParam() {
  triggerParams.value.push({ key: '', value: '' })
}

async function submitTrigger() {
  if (!triggerJob.value) return
  const params: Record<string, string> = {}
  for (const item of triggerParams.value) {
    if (item.key.trim()) params[item.key.trim()] = item.value
  }
  const res = await triggerBuildJob(triggerJob.value.id, { params })
  ElMessage.success(res.detail)
  triggerDialog.value = false
  loadJobs()
  tab.value = 'records'
  loadRecords()
}

// ---------- Jenkins 服务器 ----------
const serverLoading = ref(false)
const servers = ref<BuildServer[]>([])

const serverDialog = ref(false)
const serverEditingId = ref<number | null>(null)
const serverFormRef = ref<FormInstance>()
const serverForm = reactive({
  name: '',
  url: '',
  username: '',
  token: '',
  enabled: true,
  remark: ''
})
const serverRules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  url: [{ required: true, message: '请输入 Jenkins 地址', trigger: 'blur' }]
}

async function loadServers() {
  serverLoading.value = true
  try {
    servers.value = await listBuildServers()
  } finally {
    serverLoading.value = false
  }
}

function openServerCreate() {
  serverEditingId.value = null
  Object.assign(serverForm, { name: '', url: '', username: '', token: '', enabled: true, remark: '' })
  serverDialog.value = true
}

function openServerEdit(row: BuildServer) {
  serverEditingId.value = row.id
  Object.assign(serverForm, {
    name: row.name,
    url: row.url,
    username: row.username,
    token: '',
    enabled: row.enabled,
    remark: row.remark
  })
  serverDialog.value = true
}

async function submitServer() {
  const valid = await serverFormRef.value?.validate().catch(() => false)
  if (!valid) return
  if (serverEditingId.value) {
    await updateBuildServer(serverEditingId.value, { ...serverForm })
    ElMessage.success('已更新')
  } else {
    await createBuildServer({ ...serverForm })
    ElMessage.success('已创建')
  }
  serverDialog.value = false
  loadServers()
  loadJobs()
}

async function removeServer(row: BuildServer) {
  await ElMessageBox.confirm(`确认删除服务器「${row.name}」？`, '提示', { type: 'warning' })
  await deleteBuildServer(row.id)
  ElMessage.success('已删除')
  loadServers()
}

async function testServer(row: BuildServer) {
  const res = await testBuildServer(row.id)
  if (res.ok) {
    ElMessage.success(`连接成功（${res.costMs}ms）版本：${res.version}`)
  } else {
    ElMessage.error(`连接失败（${res.costMs}ms）：${res.detail}`)
  }
}

// ---------- 构建记录 ----------
const recordLoading = ref(false)
const records = ref<BuildRecord[]>([])
const recordTotal = ref(0)
const recordQuery = reactive({ page: 1, pageSize: 20, status: '' })

async function loadRecords() {
  recordLoading.value = true
  try {
    const data = await listBuildRecords(recordQuery)
    records.value = data.list || []
    recordTotal.value = data.total
  } finally {
    recordLoading.value = false
  }
}

async function syncRecord(row: BuildRecord) {
  const res = await syncBuildRecord(row.id)
  ElMessage.success(res.detail || `当前状态：${statusMeta[res.status]?.text || res.status}`)
  loadRecords()
  loadJobs()
}

onMounted(() => {
  loadServers()
  loadJobs()
  loadRecords()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="平台只负责触发 Jenkins 构建并记录结果，不做状态轮询：构建号与最终结果需点「同步」主动拉取。Token 请使用 Jenkins 的 API Token。"
      />

      <el-tabs v-model="tab">
        <el-tab-pane label="构建任务" name="jobs">
          <div class="page-toolbar">
            <el-input
              v-model="jobQuery.keyword"
              placeholder="任务名称或 job 路径"
              style="width: 220px"
              clearable
              @keyup.enter="((jobQuery.page = 1), loadJobs())"
            />
            <el-button type="primary" @click="((jobQuery.page = 1), loadJobs())">查询</el-button>
            <div class="grow"></div>
            <el-button v-perm="'build:manage'" type="primary" @click="openJobCreate">新建任务</el-button>
          </div>

          <el-table v-loading="jobLoading" :data="jobs" border stripe>
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column prop="name" label="任务" min-width="140" />
            <el-table-column prop="serverName" label="服务器" width="130" />
            <el-table-column prop="jobPath" label="job 路径" min-width="160" show-overflow-tooltip />
            <el-table-column label="启用" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
                  {{ row.enabled ? '启用' : '停用' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="最近结果" width="110">
              <template #default="{ row }">
                <el-tag v-if="row.lastStatus" size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
                  {{ statusMeta[row.lastStatus]?.text || row.lastStatus }}
                </el-tag>
                <span v-else style="color: #6b7280">未构建</span>
              </template>
            </el-table-column>
            <el-table-column prop="lastBuildNo" label="构建号" width="90" />
            <el-table-column prop="lastRunAt" label="最近触发" min-width="180" />
            <el-table-column label="操作" width="200" fixed="right">
              <template #default="{ row }">
                <el-button v-perm="'build:run'" link type="primary" @click="openTrigger(row)">构建</el-button>
                <el-button v-perm="'build:manage'" link type="primary" @click="openJobEdit(row)">编辑</el-button>
                <el-button v-perm="'build:manage'" link type="danger" @click="removeJob(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <Pagination
            v-model:current-page="jobQuery.page"
            v-model:page-size="jobQuery.pageSize"
            :total="jobTotal"
            @change="loadJobs"
          />
        </el-tab-pane>

        <el-tab-pane label="Jenkins 服务器" name="servers">
          <div class="page-toolbar">
            <div class="grow"></div>
            <el-button v-perm="'build:manage'" type="primary" @click="openServerCreate">新增服务器</el-button>
          </div>

          <el-table v-loading="serverLoading" :data="servers" border stripe>
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column prop="name" label="名称" min-width="130" />
            <el-table-column prop="url" label="地址" min-width="200" show-overflow-tooltip />
            <el-table-column prop="username" label="账号" width="130" />
            <el-table-column label="启用" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
                  {{ row.enabled ? '启用' : '停用' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
            <el-table-column label="操作" width="190" fixed="right">
              <template #default="{ row }">
                <el-button v-perm="'build:manage'" link type="primary" @click="testServer(row)">测试连接</el-button>
                <el-button v-perm="'build:manage'" link type="primary" @click="openServerEdit(row)">编辑</el-button>
                <el-button v-perm="'build:manage'" link type="danger" @click="removeServer(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="构建记录" name="records">
          <div class="page-toolbar">
            <el-select
              v-model="recordQuery.status"
              placeholder="全部状态"
              clearable
              style="width: 150px"
              @change="((recordQuery.page = 1), loadRecords())"
            >
              <el-option
                v-for="(meta, key) in statusMeta"
                :key="key"
                :label="meta.text"
                :value="key"
              />
            </el-select>
            <el-button type="primary" @click="((recordQuery.page = 1), loadRecords())">查询</el-button>
          </div>

          <el-table v-loading="recordLoading" :data="records" border stripe>
            <el-table-column prop="id" label="ID" width="70" />
            <el-table-column prop="jobName" label="任务" min-width="130" />
            <el-table-column label="构建号" width="90">
              <template #default="{ row }">
                <a v-if="row.buildUrl" :href="row.buildUrl" target="_blank" rel="noopener noreferrer">
                  #{{ row.buildNo }}
                </a>
                <span v-else style="color: #6b7280">待同步</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
                  {{ statusMeta[row.status]?.text || row.status }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="耗时" width="100">
              <template #default="{ row }">
                {{ row.durationMs ? `${(row.durationMs / 1000).toFixed(1)}s` : '-' }}
              </template>
            </el-table-column>
            <el-table-column prop="triggeredBy" label="触发人" width="110" />
            <el-table-column prop="params" label="参数" min-width="150" show-overflow-tooltip />
            <el-table-column prop="errorMsg" label="错误" min-width="150" show-overflow-tooltip />
            <el-table-column prop="startedAt" label="触发时间" min-width="180" />
            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button v-perm="'build:run'" link type="primary" @click="syncRecord(row)">同步</el-button>
              </template>
            </el-table-column>
          </el-table>

          <Pagination
            v-model:current-page="recordQuery.page"
            v-model:page-size="recordQuery.pageSize"
            :total="recordTotal"
            @change="loadRecords"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="jobDialog" :title="jobEditingId ? '编辑构建任务' : '新建构建任务'" width="560px">
      <el-form ref="jobFormRef" :model="jobForm" :rules="jobRules" label-width="110px">
        <el-form-item label="任务名称" prop="name">
          <el-input v-model="jobForm.name" />
        </el-form-item>
        <el-form-item label="Jenkins" prop="serverId">
          <el-select v-model="jobForm.serverId" style="width: 100%">
            <el-option
              v-for="item in servers"
              :key="item.id"
              :label="`${item.name}（${item.url}）`"
              :value="item.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="job 路径" prop="jobPath">
          <el-input v-model="jobForm.jobPath" placeholder="如 my-folder/my-app，不要带 /job/ 前缀" />
        </el-form-item>
        <el-form-item label="默认参数">
          <el-input
            v-model="jobForm.params"
            type="textarea"
            :rows="4"
            placeholder='字符串值的 JSON 对象，如 {"BRANCH":"main"}；留空表示无参数构建'
          />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="jobForm.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="jobForm.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="jobDialog = false">取消</el-button>
        <el-button type="primary" @click="submitJob">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="serverDialog" :title="serverEditingId ? '编辑服务器' : '新增服务器'" width="520px">
      <el-form ref="serverFormRef" :model="serverForm" :rules="serverRules" label-width="110px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="serverForm.name" />
        </el-form-item>
        <el-form-item label="地址" prop="url">
          <el-input v-model="serverForm.url" placeholder="http://jenkins.example.com:8080" />
        </el-form-item>
        <el-form-item label="账号">
          <el-input v-model="serverForm.username" placeholder="留空表示匿名访问" />
        </el-form-item>
        <el-form-item label="API Token">
          <el-input
            v-model="serverForm.token"
            type="password"
            show-password
            :placeholder="serverEditingId ? '留空表示不修改' : 'Jenkins 用户的 API Token'"
          />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="serverForm.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="serverForm.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="serverDialog = false">取消</el-button>
        <el-button type="primary" @click="submitServer">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="triggerDialog" :title="`触发构建 · ${triggerJob?.name ?? ''}`" width="520px">
      <el-alert
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="参数会覆盖任务默认参数；提交后 Jenkins 先进入队列，构建号需在构建记录中同步获取"
      />
      <div v-for="(item, index) in triggerParams" :key="index" class="param-row">
        <el-input v-model="item.key" placeholder="参数名" style="width: 180px" />
        <el-input v-model="item.value" placeholder="参数值" />
        <el-button link type="danger" @click="triggerParams.splice(index, 1)">移除</el-button>
      </div>
      <el-button link type="primary" @click="addTriggerParam">添加参数</el-button>
      <template #footer>
        <el-button @click="triggerDialog = false">取消</el-button>
        <el-button type="primary" @click="submitTrigger">开始构建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.param-row {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
}
</style>
