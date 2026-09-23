<script setup lang="ts">
import { onActivated, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  cancelExecJob,
  listExecJobs,
  listHosts,
  precheckExec,
  rerunFailedExecJob,
  resolveExecHosts,
  runExecJob,
  type ExecJob,
  type ExecPrecheckResult,
  type ExecResolveResult,
  type Host
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const hosts = ref<Host[]>([])
const jobs = ref<ExecJob[]>([])
const jobTotal = ref(0)
const running = ref(false)
const current = ref<ExecJob | null>(null)
const detailVisible = ref(false)
const precheck = ref<ExecPrecheckResult | null>(null)

// 按条件选主机：原来只能在一个拉了前 200 台、不带筛选的下拉里勾，
// 第 201 台主机根本选不到，也没法按标签选
const selectVisible = ref(false)
const selecting = ref(false)
const selectForm = reactive({ keyword: '', env: '', tags: '' })
const resolved = ref<ExecResolveResult | null>(null)

const form = reactive({ name: '', command: '', hostIds: [] as number[], timeout: 60 })
const jobQuery = reactive({ page: 1, pageSize: 10 })


const route = useRoute()
// 剧本的「去执行这一步」会带 ?command=&name=&hosts= 跳进来。本页会被页签缓存，
// 再次带着新命令进来不会重新挂载，所以 activate 时也认一次。
// 只预填不自动执行：预检与生产确认仍然要人点。
let appliedCommand = ''
function applyRouteQuery() {
  const command = String(route.query.command ?? '')
  if (!command || command === appliedCommand) return
  appliedCommand = command
  form.command = command
  form.name = String(route.query.name ?? '') || form.name
  const hosts = String(route.query.hosts ?? '')
    .split(',')
    .map((x) => Number(x))
    .filter((x) => x > 0)
  if (hosts.length) form.hostIds = hosts
  precheck.value = null
  ElMessage.info('命令已预填，确认主机后再执行')
}

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
}

async function loadJobs() {
  const data = await listExecJobs(jobQuery)
  jobs.value = data.list || []
  jobTotal.value = data.total
}

function validate() {
  if (!form.command.trim()) {
    ElMessage.warning('请输入要执行的命令')
    return false
  }
  if (!form.hostIds.length) {
    ElMessage.warning('请选择至少一台主机')
    return false
  }
  return true
}

async function doPrecheck() {
  if (!validate()) return null
  const result = await precheckExec({ command: form.command, hostIds: form.hostIds })
  precheck.value = result
  return result
}

async function manualPrecheck() {
  const result = await doPrecheck()
  if (!result) return
  if (result.ruleBlocked) {
    ElMessage.error(result.reason)
  } else if (result.needConfirm) {
    ElMessage.warning(`可以下发，但目标含 ${result.prodHosts.length} 台生产主机，执行时需确认`)
  } else if (result.status === 'warn') {
    ElMessage.warning('命中提醒级命令规则，可以下发')
  } else {
    ElMessage.success('预检通过')
  }
}

async function submit() {
  // 先问后端：拦不拦、要不要确认。判定以后端为准（前端只加载了前 200 台主机，
  // 自己数生产主机会漏；而且直接调接口也能绕过前端弹窗）
  const result = await doPrecheck()
  if (!result) return
  if (result.ruleBlocked) {
    ElMessage.error(result.reason)
    return
  }
  if (result.needConfirm) {
    await ElMessageBox.confirm(
      `本次下发包含 ${result.prodHosts.length} 台生产主机（${result.prodHosts.join('、')}），确认继续？`,
      '生产环境确认',
      { type: 'warning' }
    )
  }

  running.value = true
  try {
    current.value = await runExecJob({ ...form, confirmProd: result.needConfirm })
    detailVisible.value = true
    ElMessage.success(`执行完成：成功 ${current.value.successNum}，失败 ${current.value.failedNum}`)
    loadJobs()
  } finally {
    running.value = false
  }
}


function openDetail(job: ExecJob) {
  current.value = job
  detailVisible.value = true
}

async function doResolve() {
  selecting.value = true
  try {
    resolved.value = await resolveExecHosts({
      keyword: selectForm.keyword || undefined,
      env: selectForm.env || undefined,
      tags: selectForm.tags
        .split(/[,，\s]+/)
        .map((x) => x.trim())
        .filter(Boolean)
    })
  } finally {
    selecting.value = false
  }
}

function applyResolved() {
  if (!resolved.value?.hostIds.length) {
    ElMessage.warning('没有可执行的主机')
    return
  }
  form.hostIds = [...resolved.value.hostIds]
  // 把选中的主机并进下拉的候选里，否则回到表单会显示成一串 ID
  const known = new Set(hosts.value.map((x) => x.id))
  for (const item of resolved.value.hosts) {
    if (!known.has(item.id)) {
      hosts.value.push(item as unknown as Host)
    }
  }
  selectVisible.value = false
  precheck.value = null
  ElMessage.success(`已选中 ${form.hostIds.length} 台（按条件匹配 ${resolved.value.matched} 台）`)
}

async function rerunFailed(job: ExecJob) {
  await ElMessageBox.confirm(
    `只对这次失败的主机重跑同一条命令（成功的机器不会再执行一次）。确认继续？`,
    `重跑失败主机 #${job.id}`,
    { type: 'warning' }
  )
  running.value = true
  try {
    const result = await rerunFailedExecJob(job.id)
    current.value = result.job
    detailVisible.value = true
    ElMessage.success(`已重跑 ${result.reran} 台，跳过 ${result.skipped} 台（权限已变化）`)
    loadJobs()
  } finally {
    running.value = false
  }
}

async function cancelJob(job: ExecJob) {
  await ElMessageBox.confirm(
    '会向已经在跑的主机发出 SIGKILL 尝试，但**远端进程不保证被杀掉**' +
      '（无 PTY 会话的信号转发不可靠，nohup / & / 已 fork 的子进程不受影响）。确认停止？',
    `停止下发 #${job.id}`,
    { type: 'warning' }
  )
  const result = await cancelExecJob(job.id)
  ElMessage.warning(result.note)
  loadJobs()
}


const statusType: Record<string, 'success' | 'danger' | 'warning'> = {
  success: 'success',
  failed: 'danger',
  timeout: 'warning'
}

onMounted(() => {
  applyRouteQuery()
  loadHosts()
  loadJobs()
})
onActivated(() => {
  applyRouteQuery()
})
</script>

<template>
  <div class="page">
    <el-row :gutter="12">
      <el-col :span="10">
        <el-card header="下发命令">
          <el-form label-width="80px">
            <el-form-item label="作业名称">
              <el-input v-model="form.name" placeholder="可选，默认「批量执行」" />
            </el-form-item>
            <el-form-item label="目标主机">
              <el-select v-model="form.hostIds" multiple filterable placeholder="可多选" style="width: 100%">
                <el-option
                  v-for="host in hosts"
                  :key="host.id"
                  :label="`${host.name}（${host.address}）${host.env}`"
                  :value="host.id"
                />
              </el-select>
              <div style="margin-top: 6px">
                <el-button size="small" @click="selectVisible = true">按条件选主机</el-button>
                <span style="margin-left: 8px; color: #909399; font-size: 12px">
                  已选 {{ form.hostIds.length }} 台；下拉只加载了前 200 台，多于这个数请用条件选
                </span>
              </div>
            </el-form-item>
            <el-form-item label="超时(秒)">
              <el-input-number v-model="form.timeout" :min="5" :max="600" />
            </el-form-item>
            <el-form-item label="命令">
              <el-input v-model="form.command" type="textarea" :rows="6" placeholder="例如：df -h" />
            </el-form-item>
            <el-button v-perm="'exec:run'" type="primary" :loading="running" @click="submit">
              执行
            </el-button>
            <el-button v-perm="'exec:run'" @click="manualPrecheck">预检</el-button>

            <el-alert
              v-if="precheck"
              style="margin-top: 12px"
              :type="precheck.ruleBlocked ? 'error' : precheck.status === 'warn' || precheck.needConfirm ? 'warning' : 'success'"
              :closable="false"
              show-icon
            >
              <div>
                {{
                  precheck.ruleBlocked
                    ? precheck.reason
                    : precheck.needConfirm
                      ? `目标含 ${precheck.prodHosts.length} 台生产主机：${precheck.prodHosts.join('、')}`
                      : precheck.status === 'warn'
                        ? '命中提醒级命令规则，可以下发'
                        : '预检通过：未命中命令规则，目标里没有生产主机'
                }}
              </div>
              <div v-for="hit in precheck.hits" :key="hit.ruleId" class="hit-line">
                第 {{ hit.line }} 行命中
                <el-tag size="small" :type="hit.action === 'block' ? 'danger' : 'warning'">
                  {{ hit.action === 'block' ? '拦截' : '提醒' }}
                </el-tag>
                {{ hit.description || hit.pattern }}
              </div>
            </el-alert>
          </el-form>

        </el-card>
      </el-col>

      <el-col :span="14">
        <el-card header="执行历史">
          <el-table :data="jobs" border size="small">
            <el-table-column prop="id" label="ID" width="60" />
            <el-table-column prop="name" label="作业" min-width="110" />
            <el-table-column prop="operator" label="操作人" width="100" />
            <el-table-column label="结果" width="150">
              <template #default="{ row }">
                <el-tag type="success" size="small">{{ row.successNum }}</el-tag>
                <el-tag type="danger" size="small" style="margin-left: 4px">{{ row.failedNum }}</el-tag>
                <span style="margin-left: 4px; color: #6b7280">/ {{ row.total }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="开始时间" min-width="170" />
            <el-table-column label="操作" width="190" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDetail(row)">详情</el-button>
                <el-button
                  v-if="row.status === 'running'"
                  v-perm="'exec:run'"
                  link
                  type="danger"
                  @click="cancelJob(row)"
                >
                  停止
                </el-button>
                <el-button
                  v-if="row.failedNum > 0 && row.status !== 'running'"
                  v-perm="'exec:run'"
                  link
                  type="warning"
                  @click="rerunFailed(row)"
                >
                  重跑失败 {{ row.failedNum }} 台
                </el-button>
              </template>
            </el-table-column>
          </el-table>
          <Pagination
            v-model:current-page="jobQuery.page"
            v-model:page-size="jobQuery.pageSize"
            :total="jobTotal"
            @change="loadJobs"
          />
        </el-card>
      </el-col>
    </el-row>

    <el-drawer v-model="detailVisible" :title="`作业详情 #${current?.id ?? ''}`" size="50%">
      <el-descriptions v-if="current" :column="1" border size="small">
        <el-descriptions-item label="命令">{{ current.command }}</el-descriptions-item>
        <el-descriptions-item label="主机数">{{ current.total }}</el-descriptions-item>
        <el-descriptions-item label="成功 / 失败">
          {{ current.successNum }} / {{ current.failedNum }}
        </el-descriptions-item>
      </el-descriptions>

      <el-collapse v-if="current?.results?.length" style="margin-top: 12px">
        <el-collapse-item v-for="item in current.results" :key="item.id" :name="item.id">
          <template #title>
            <el-tag size="small" :type="statusType[item.status] || 'info'">{{ item.status }}</el-tag>
            <span style="margin-left: 8px">{{ item.hostName }}（{{ item.address }}）</span>
            <span style="margin-left: 8px; color: #6b7280">{{ item.costMs }}ms</span>
          </template>
          <pre class="output-pre">{{ item.stdout || item.stderr || '（无输出）' }}</pre>
        </el-collapse-item>
      </el-collapse>
      <el-empty v-else description="暂无结果明细，可从历史列表进入查看" />
    </el-drawer>

    <el-dialog v-model="selectVisible" title="按条件选主机" width="720px">
      <el-alert type="info" :closable="false" style="margin-bottom: 10px">
        <template #title>
          标签是<strong>全部命中</strong>而不是任一命中——放大范围的方向正好是危险的那一侧。
          结果已按数据范围与资源授权过滤，「不可执行」的主机会列出来但不进下发清单
          （否则「我明明选了 20 台怎么只跑了 12 台」没法解释）。
        </template>
      </el-alert>
      <div class="page-toolbar" style="flex-wrap: wrap; gap: 8px">
        <el-input v-model="selectForm.keyword" placeholder="名称 / 地址 / 标签" style="width: 200px" />
        <el-select v-model="selectForm.env" clearable placeholder="环境" style="width: 120px">
          <el-option value="prod" label="prod" />
          <el-option value="stage" label="stage" />
          <el-option value="test" label="test" />
          <el-option value="dev" label="dev" />
        </el-select>
        <el-input v-model="selectForm.tags" placeholder="标签，逗号分隔（全部命中）" style="width: 220px" />
        <el-button type="primary" :loading="selecting" @click="doResolve">匹配</el-button>
      </div>

      <template v-if="resolved">
        <div style="margin: 8px 0">
          匹配 <strong>{{ resolved.matched }}</strong> 台，其中可执行
          <strong>{{ resolved.usable }}</strong> 台，生产
          <el-tag v-if="resolved.prod > 0" type="danger" size="small">{{ resolved.prod }}</el-tag>
          <span v-else>0</span>
        </div>
        <el-table :data="resolved.hosts" border stripe size="small" max-height="320">
          <el-table-column prop="name" label="主机" min-width="140" />
          <el-table-column prop="address" label="地址" width="140" />
          <el-table-column prop="env" label="环境" width="80" />
          <el-table-column prop="tags" label="标签" min-width="120" show-overflow-tooltip />
          <el-table-column label="可执行" width="90">
            <template #default="{ row }">
              <el-tag :type="row.canExec ? 'success' : 'info'" size="small">
                {{ row.canExec ? '可' : '未授权' }}
              </el-tag>
            </template>
          </el-table-column>
        </el-table>
        <ul style="margin-top: 8px; color: #909399; font-size: 12px; line-height: 1.7">
          <li v-for="(note, idx) in resolved.notes" :key="idx">{{ note }}</li>
        </ul>
      </template>

      <template #footer>
        <el-button @click="selectVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!resolved?.usable" @click="applyResolved">
          选中这 {{ resolved?.usable ?? 0 }} 台
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.hit-line {
  margin-top: 4px;
  font-size: 12px;
}
</style>

