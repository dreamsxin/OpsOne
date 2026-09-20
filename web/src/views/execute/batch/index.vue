<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listExecJobs, listHosts, runExecJob, type ExecJob, type Host } from '@/api'

const hosts = ref<Host[]>([])
const jobs = ref<ExecJob[]>([])
const jobTotal = ref(0)
const running = ref(false)
const current = ref<ExecJob | null>(null)
const detailVisible = ref(false)

const form = reactive({ name: '', command: '', hostIds: [] as number[], timeout: 60 })
const jobQuery = reactive({ page: 1, pageSize: 10 })

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
}

async function loadJobs() {
  const data = await listExecJobs(jobQuery)
  jobs.value = data.list || []
  jobTotal.value = data.total
}

async function submit() {
  if (!form.command.trim()) {
    ElMessage.warning('请输入要执行的命令')
    return
  }
  if (!form.hostIds.length) {
    ElMessage.warning('请选择至少一台主机')
    return
  }

  const prodHosts = hosts.value.filter((h) => form.hostIds.includes(h.id) && h.env === 'prod')
  if (prodHosts.length) {
    await ElMessageBox.confirm(
      `本次下发包含 ${prodHosts.length} 台生产主机，确认继续？`,
      '生产环境确认',
      { type: 'warning' }
    )
  }

  running.value = true
  try {
    current.value = await runExecJob({ ...form })
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

const statusType: Record<string, 'success' | 'danger' | 'warning'> = {
  success: 'success',
  failed: 'danger',
  timeout: 'warning'
}

onMounted(() => {
  loadHosts()
  loadJobs()
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
            <el-table-column label="操作" width="80" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openDetail(row)">详情</el-button>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination
            style="margin-top: 12px; justify-content: flex-end"
            layout="total, prev, pager, next"
            :total="jobTotal"
            v-model:current-page="jobQuery.page"
            :page-size="jobQuery.pageSize"
            @current-change="loadJobs"
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
  </div>
</template>
