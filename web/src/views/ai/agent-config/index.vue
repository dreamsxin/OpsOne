<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createAgentConfig,
  deleteAgentConfig,
  listAgentConfigs,
  listModelUpstreams,
  runAgent,
  updateAgentConfig,
  type AgentConfig,
  type AgentDataSource,
  type ModelPoolStat
} from '@/api'

const loading = reactive({ list: false, submit: false, run: false })
const rows = ref<AgentConfig[]>([])
const dataSources = ref<AgentDataSource[]>([])
const pools = ref<ModelPoolStat[]>([])

const aliasOptions = computed(() => pools.value.map((p) => p.alias))
const sourceLabel = (key: string) =>
  dataSources.value.find((item) => item.key === key)?.label || key

async function load() {
  loading.list = true
  try {
    const data = await listAgentConfigs()
    rows.value = data.list || []
    dataSources.value = data.dataSources || []
  } finally {
    loading.list = false
  }
}

// ---------- 配置 ----------

// Vue 模板里写不了 {{.context}} 这种字面量（编译器会把它当插值），放到脚本里当常量
const ctxVar = '{' + '{.context}' + '}'
const inputVar = '{' + '{.input}' + '}'

const defaultTemplate = `下面是平台取到的数据：

${'{'}${'{'}.context${'}'}${'}'}

请判断当前最可能的问题、影响范围，并给出接下来该查什么。
补充说明：${'{'}${'{'}.input${'}'}${'}'}`

const form = reactive({
  visible: false,
  id: 0,
  name: '',
  alias: '',
  dataSource: 'alert',
  maxItems: 20,
  systemPrompt: '你是资深 SRE，回答要具体、可执行，不确定的地方直接说不确定。',
  promptTemplate: defaultTemplate,
  temperature: 0,
  maxTokens: 800,
  enabled: true,
  remark: ''
})

function openForm(row?: AgentConfig) {
  form.visible = true
  form.id = row?.id ?? 0
  form.name = row?.name ?? ''
  form.alias = row?.alias ?? aliasOptions.value[0] ?? ''
  form.dataSource = row?.dataSource ?? 'alert'
  form.maxItems = row?.maxItems ?? 20
  form.systemPrompt = row?.systemPrompt ?? form.systemPrompt
  form.promptTemplate = row?.promptTemplate ?? defaultTemplate
  form.temperature = row?.temperature ?? 0
  form.maxTokens = row?.maxTokens ?? 800
  form.enabled = row?.enabled ?? true
  form.remark = row?.remark ?? ''
}

async function submit() {
  loading.submit = true
  try {
    const payload = {
      name: form.name,
      alias: form.alias,
      dataSource: form.dataSource,
      maxItems: form.maxItems,
      systemPrompt: form.systemPrompt,
      promptTemplate: form.promptTemplate,
      temperature: form.temperature,
      maxTokens: form.maxTokens,
      enabled: form.enabled,
      remark: form.remark
    }
    if (form.id) {
      await updateAgentConfig(form.id, payload)
    } else {
      await createAgentConfig(payload)
    }
    ElMessage.success('已保存')
    form.visible = false
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  } finally {
    loading.submit = false
  }
}

async function remove(row: AgentConfig) {
  await ElMessageBox.confirm(
    `删除 Agent「${row.name}」？历史运行记录不会被删除，结论仍可回看。`,
    '删除 Agent',
    { type: 'warning' }
  )
  await deleteAgentConfig(row.id)
  ElMessage.success('已删除')
  load()
}

// ---------- 运行 ----------

const runner = reactive({
  visible: false,
  agent: null as AgentConfig | null,
  targetId: undefined as number | undefined,
  severity: '',
  input: '',
  output: '',
  meta: '',
  prompt: '',
  showPrompt: false,
  error: ''
})

const needsTarget = computed(
  () => dataSources.value.find((item) => item.key === runner.agent?.dataSource)?.needsTarget ?? false
)
const targetHint: Record<string, string> = {
  host_metric: '填主机 ID（主机管理列表里那一列）',
  exec_job: '填执行记录的作业 ID',
  session_command: '填会话 ID（会话审计里的 #编号）'
}

function openRunner(row: AgentConfig) {
  runner.visible = true
  runner.agent = row
  runner.targetId = undefined
  runner.severity = ''
  runner.input = ''
  runner.output = ''
  runner.meta = ''
  runner.prompt = ''
  runner.showPrompt = false
  runner.error = ''
}

async function execute() {
  if (!runner.agent) return
  loading.run = true
  runner.error = ''
  runner.output = ''
  try {
    const res = await runAgent(runner.agent.id, {
      targetId: runner.targetId || undefined,
      severity: runner.severity || undefined,
      input: runner.input || undefined
    })
    runner.output = res.run.output
    runner.prompt = res.prompt
    runner.meta = `${res.upstreamName} · ${res.model} · ${res.contextSummary} · ` +
      `${res.run.contextItems} 条上下文 / ${res.run.contextChars} 字符 · ` +
      `token ${res.run.promptTokens}/${res.run.completionTokens} · ` +
      `约 ${res.run.cost} 元 · ${res.run.latencyMs} ms` +
      (res.run.contextTruncated ? ' · 上下文已截断' : '')
  } catch (err: any) {
    runner.error = err?.message || '运行失败'
  } finally {
    loading.run = false
  }
}

onMounted(async () => {
  try {
    pools.value = (await listModelUpstreams()).pools || []
  } catch {
    // 资源池读不到不影响看配置，保存时后端还会再校验一次 Alias
  }
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" show-icon class="notice">
        Agent 在这里是<strong>只读的分析器</strong>：读平台自己的数据（告警 / 主机指标 / 执行结果 / 会话命令），
        拼成上下文交给模型，拿回一段结论。<strong>它不会执行任何命令、不会改任何东西</strong> ——
        结论仅供参考，动手前请自行核对。每次运行都会真实消耗模型额度，用量并入「用量与成本」。
      </el-alert>

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="flex-1" />
        <el-button v-perm="'agent:manage'" type="primary" @click="openForm()">新建 Agent</el-button>
      </div>

      <el-table v-loading="loading.list" :data="rows" style="width: 100%">
        <el-table-column label="名称" min-width="160">
          <template #default="{ row }">
            <div>{{ row.name }}</div>
            <div class="sub">{{ row.remark || '-' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="模型" width="130" prop="alias" />
        <el-table-column label="数据来源" width="150">
          <template #default="{ row }">
            <el-tag size="small" :type="row.dataSource === 'none' ? 'info' : 'success'">
              {{ sourceLabel(row.dataSource) }}
            </el-tag>
            <div class="sub">最多 {{ row.maxItems }} 条</div>
          </template>
        </el-table-column>
        <el-table-column label="回复上限" width="100">
          <template #default="{ row }">{{ row.maxTokens }} token</template>
        </el-table-column>
        <el-table-column label="temperature" width="110">
          <template #default="{ row }">{{ row.temperature || '默认' }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? '是' : '否' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button
              v-perm="'agent:run'"
              link
              type="primary"
              :disabled="!row.enabled"
              @click="openRunner(row)"
            >
              运行
            </el-button>
            <el-button v-perm="'agent:manage'" link type="primary" @click="openForm(row)">编辑</el-button>
            <el-button v-perm="'agent:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="form.visible" :title="form.id ? '编辑 Agent' : '新建 Agent'" width="680px">
      <el-form label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如 告警归因、主机体检" />
        </el-form-item>
        <el-form-item label="模型">
          <el-select v-model="form.alias" filterable allow-create style="width: 100%">
            <el-option v-for="item in aliasOptions" :key="item" :label="item" :value="item" />
          </el-select>
          <div class="hint">用资源池里的 Alias；保存时会校验它是否真有启用的上游。</div>
        </el-form-item>
        <el-form-item label="数据来源">
          <el-select v-model="form.dataSource" style="width: 100%">
            <el-option
              v-for="item in dataSources"
              :key="item.key"
              :label="item.label"
              :value="item.key"
            />
          </el-select>
          <div class="hint">除「不取平台数据」外，模板里必须用 {{ ctxVar }} 把数据放进去。</div>
        </el-form-item>
        <el-form-item label="上下文条数">
          <el-input-number v-model="form.maxItems" :min="1" :max="200" />
          <span class="hint inline">条数撞到上限时运行记录会标「上下文已截断」</span>
        </el-form-item>
        <el-form-item label="角色设定">
          <el-input v-model="form.systemPrompt" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item label="提示词模板">
          <el-input v-model="form.promptTemplate" type="textarea" :rows="8" />
          <div class="hint">可用变量：{{ ctxVar }}（平台数据）、{{ inputVar }}（运行时填的补充说明）。</div>
        </el-form-item>
        <el-form-item label="回复上限">
          <el-input-number v-model="form.maxTokens" :min="32" :max="8192" :step="128" />
          <span class="hint inline">token</span>
        </el-form-item>
        <el-form-item label="temperature">
          <el-input-number v-model="form.temperature" :min="0" :max="2" :step="0.1" />
          <span class="hint inline">0 表示不传，用上游默认</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="form.visible = false">取消</el-button>
        <el-button type="primary" :loading="loading.submit" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="runner.visible" :title="`运行：${runner.agent?.name || ''}`" width="720px">
      <el-form label-width="100px">
        <el-form-item label="数据来源">
          <span>{{ sourceLabel(runner.agent?.dataSource || '') }}</span>
        </el-form-item>
        <el-form-item v-if="needsTarget" label="目标 ID">
          <el-input-number v-model="runner.targetId" :min="1" />
          <span class="hint inline">{{ targetHint[runner.agent?.dataSource || ''] || '' }}</span>
        </el-form-item>
        <el-form-item v-if="runner.agent?.dataSource === 'alert'" label="只看级别">
          <el-select v-model="runner.severity" clearable placeholder="全部级别" style="width: 200px">
            <el-option label="critical" value="critical" />
            <el-option label="warning" value="warning" />
            <el-option label="info" value="info" />
          </el-select>
        </el-form-item>
        <el-form-item label="补充说明">
          <el-input v-model="runner.input" type="textarea" :rows="2" placeholder="可留空" />
        </el-form-item>
      </el-form>

      <el-alert v-if="runner.error" type="error" :closable="false" show-icon class="notice">
        {{ runner.error }}
      </el-alert>
      <div v-if="runner.output" class="result">
        <div class="result-meta">{{ runner.meta }}</div>
        <pre class="result-body">{{ runner.output }}</pre>
        <el-button link type="primary" @click="runner.showPrompt = !runner.showPrompt">
          {{ runner.showPrompt ? '收起' : '看看实际发出去的提示词' }}
        </el-button>
        <pre v-if="runner.showPrompt" class="result-body prompt">{{ runner.prompt }}</pre>
      </div>

      <template #footer>
        <el-button @click="runner.visible = false">关闭</el-button>
        <el-button v-perm="'agent:run'" type="primary" :loading="loading.run" @click="execute">
          运行
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
}
.flex-1 {
  flex: 1;
}
.sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.hint.inline {
  margin-left: 8px;
}
.result {
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  padding: 10px;
}
.result-meta {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-bottom: 6px;
}
.result-body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  max-height: 320px;
  overflow: auto;
}
.result-body.prompt {
  margin-top: 8px;
  background: var(--el-fill-color-light);
  padding: 8px;
  max-height: 240px;
}
</style>
