<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createCommandRule,
  deleteCommandRule,
  listCommandRules,
  listExecGuardLogs,
  testCommandRule,
  updateCommandRule,
  type CommandRule,
  type ExecGuardLog
} from '@/api'

const loading = ref(false)
const rows = ref<CommandRule[]>([])

const guardLogs = ref<ExecGuardLog[]>([])
const guardTotal = ref(0)
const guardSummary = ref<Record<string, number>>({})
const guardQuery = reactive({ page: 1, pageSize: 10, status: '', keyword: '' })

const sourceLabel: Record<string, string> = {
  manual: '批量执行',
  script: '脚本下发',
  cron: '定时任务'
}


const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ pattern: '', description: '', action: 'block' as 'block' | 'warn', enabled: true })

const testCommand = ref('')
const testResult = ref<string>('')

const rules = {
  pattern: [{ required: true, message: '请输入正则表达式', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listCommandRules()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { pattern: '', description: '', action: 'block', enabled: true })
  dialogVisible.value = true
}

function openEdit(row: CommandRule) {
  editingId.value = row.id
  Object.assign(form, {
    pattern: row.pattern,
    description: row.description,
    action: row.action,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateCommandRule(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createCommandRule({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: CommandRule) {
  await updateCommandRule(row.id, {
    pattern: row.pattern,
    description: row.description,
    action: row.action,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '规则已启用' : '规则已停用')
}

async function remove(row: CommandRule) {
  await ElMessageBox.confirm(`确认删除规则「${row.description || row.pattern}」？`, '提示', {
    type: 'warning'
  })
  await deleteCommandRule(row.id)
  ElMessage.success('已删除')
  load()
}

async function runTest() {
  if (!testCommand.value.trim()) {
    ElMessage.warning('请输入要试跑的命令')
    return
  }
  const res = await testCommandRule(testCommand.value)
  if (res.matched) {
    const action = res.action === 'block' ? '拦截' : '告警'
    testResult.value = `命中规则 #${res.ruleId}（${res.description || '无描述'}），处理方式：${action}`
  } else {
    testResult.value = '未命中任何启用中的规则，该命令会正常执行'
  }
}

async function loadGuardLogs() {
  const data = await listExecGuardLogs(guardQuery)
  guardLogs.value = data.list || []
  guardTotal.value = data.total
  guardSummary.value = data.summary || {}
}

onMounted(() => {
  load()
  loadGuardLogs()
})
</script>


<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          规则同时作用于三处：Web 终端（在 PTY 输入流上实时拦截）、脚本库（保存与下发前静态预检）、
          批量执行与定时任务（下发前逐行匹配，命中拦截级规则一律不下发）。但它拦不住 vim 的 :!cmd、
          base64 解码执行等绕过方式，不能当作强制访问控制使用。修改后立即生效。
        </template>
      </el-alert>


      <div class="page-toolbar">
        <el-input v-model="testCommand" placeholder="试跑一条命令看是否命中规则" style="width: 300px" />
        <el-button @click="runTest">试跑</el-button>
        <span style="color: #6b7280">{{ testResult }}</span>
        <div class="grow"></div>
        <el-button v-perm="'rule:manage'" type="primary" @click="openCreate">新增规则</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="pattern" label="正则" min-width="260" show-overflow-tooltip />
        <el-table-column prop="description" label="说明" min-width="160" />
        <el-table-column label="命中处理" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="row.action === 'block' ? 'danger' : 'warning'">
              {{ row.action === 'block' ? '拦截' : '告警' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'rule:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'rule:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card style="margin-top: 12px">
      <template #header>
        <div class="guard-head">
          <span>下发拦截流水</span>
          <el-tag size="small" type="danger">已拦截 {{ guardSummary.blocked || 0 }}</el-tag>
          <el-tag size="small" type="warning">命中提醒 {{ guardSummary.warn || 0 }}</el-tag>
          <span class="guard-note">
            被拦下的下发不会产生执行记录，这里是唯一线索；命中提醒级规则的下发也会记一条
          </span>
        </div>
      </template>

      <div class="page-toolbar">
        <el-select v-model="guardQuery.status" placeholder="全部结果" clearable style="width: 140px" @change="loadGuardLogs">
          <el-option label="已拦截" value="blocked" />
          <el-option label="命中提醒" value="warn" />
        </el-select>
        <el-input
          v-model="guardQuery.keyword"
          placeholder="按命令 / 操作人 / 主机名搜索"
          clearable
          style="width: 240px"
          @keyup.enter="loadGuardLogs"
          @clear="loadGuardLogs"
        />
        <el-button @click="loadGuardLogs">查询</el-button>
      </div>

      <el-table :data="guardLogs" border stripe size="small">
        <el-table-column prop="createdAt" label="时间" width="170" />
        <el-table-column label="结果" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'blocked' ? 'danger' : 'warning'">
              {{ row.status === 'blocked' ? '已拦截' : '命中提醒' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="100">
          <template #default="{ row }">{{ sourceLabel[row.source] || row.source }}</template>
        </el-table-column>
        <el-table-column prop="command" label="命令" min-width="220" show-overflow-tooltip />
        <el-table-column prop="reason" label="原因" min-width="200" show-overflow-tooltip />
        <el-table-column label="目标" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            {{ row.hostCount }} 台<span v-if="row.prodCount">（生产 {{ row.prodCount }}）</span>
            <span class="guard-note">{{ row.hostNames }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="username" label="操作人" width="100" />
        <el-table-column prop="clientIp" label="来源 IP" width="130" />
      </el-table>
      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="guardTotal"
        v-model:current-page="guardQuery.page"
        :page-size="guardQuery.pageSize"
        @current-change="loadGuardLogs"
      />
    </el-card>


    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '新增规则'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="正则" prop="pattern">
          <el-input v-model="form.pattern" type="textarea" :rows="2" placeholder="如 ^\s*(shutdown|reboot)\b" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" placeholder="命中时展示给操作人的原因" />
        </el-form-item>
        <el-form-item label="命中处理">
          <el-radio-group v-model="form.action">
            <el-radio value="block">拦截（不下发命令）</el-radio>
            <el-radio value="warn">仅告警</el-radio>
          </el-radio-group>
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

<style scoped>
.guard-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.guard-note {
  color: #6b7280;
  font-size: 12px;
  font-weight: normal;
}
</style>

