<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  chatCompletion,
  checkModelUpstream,
  createModelUpstream,
  deleteModelUpstream,
  listModelCalls,
  listModelUpstreams,
  updateModelUpstream,
  type ModelCall,
  type ModelCallSummary,
  type ModelChatResult,
  type ModelPoolStat,
  type ModelUpstream
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const tab = ref('upstreams')

const loading = reactive({ list: false, submit: false, check: false, call: false, log: false })
const upstreams = ref<ModelUpstream[]>([])
const pools = ref<ModelPoolStat[]>([])

const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  healthy: { text: '可用', type: 'success' },
  unknown: { text: '未检查', type: 'info' },
  error: { text: '异常', type: 'danger' }
}

async function loadUpstreams() {
  loading.list = true
  try {
    const data = await listModelUpstreams()
    upstreams.value = data.list || []
    pools.value = data.pools || []
  } finally {
    loading.list = false
  }
}

// ---------- 上游维护 ----------

const form = reactive({
  visible: false,
  id: 0,
  name: '',
  alias: '',
  provider: 'openai',
  baseUrl: '',
  apiKey: '',
  model: '',
  weight: 10,
  timeoutSec: 60,
  inputPrice: 0,
  outputPrice: 0,
  enabled: true,
  remark: ''
})

function openForm(row?: ModelUpstream) {
  form.visible = true
  form.id = row?.id ?? 0
  form.name = row?.name ?? ''
  form.alias = row?.alias ?? ''
  form.provider = row?.provider ?? 'openai'
  form.baseUrl = row?.baseUrl ?? ''
  form.apiKey = ''
  form.model = row?.model ?? ''
  form.weight = row?.weight ?? 10
  form.timeoutSec = row?.timeoutSec ?? 60
  form.inputPrice = row?.inputPrice ?? 0
  form.outputPrice = row?.outputPrice ?? 0
  form.enabled = row?.enabled ?? true
  form.remark = row?.remark ?? ''
}

async function submit() {
  loading.submit = true
  try {
    const payload = {
      name: form.name,
      alias: form.alias,
      provider: form.provider,
      baseUrl: form.baseUrl,
      apiKey: form.apiKey,
      model: form.model,
      weight: form.weight,
      timeoutSec: form.timeoutSec,
      inputPrice: form.inputPrice,
      outputPrice: form.outputPrice,
      enabled: form.enabled,
      remark: form.remark
    }
    if (form.id) {
      await updateModelUpstream(form.id, payload)
      ElMessage.success('已保存')
    } else {
      const res = await createModelUpstream(payload)
      // 新建时后端会立刻探一次，把结论直接说出来
      if (res.check?.status === 'healthy') {
        ElMessage.success('已创建，' + res.check.detail)
      } else {
        ElMessage.warning('已创建，但检查没通过：' + (res.check?.detail || '未知原因'))
      }
    }
    form.visible = false
    loadUpstreams()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  } finally {
    loading.submit = false
  }
}

async function check(row: ModelUpstream) {
  loading.check = true
  try {
    const res = await checkModelUpstream(row.id)
    if (res.status === 'healthy') {
      ElMessage.success(res.detail)
    } else {
      ElMessage.error(res.detail)
    }
    loadUpstreams()
  } catch (err: any) {
    ElMessage.error(err?.message || '检查失败')
  } finally {
    loading.check = false
  }
}

async function remove(row: ModelUpstream) {
  await ElMessageBox.confirm(
    `删除上游「${row.name}」？调用流水不会被删除，用量与成本仍然可以回看。`,
    '删除上游',
    { type: 'warning' }
  )
  await deleteModelUpstream(row.id)
  ElMessage.success('已删除')
  loadUpstreams()
}

// ---------- 试调用 ----------

const tester = reactive({
  visible: false,
  mode: 'alias' as 'alias' | 'upstream',
  alias: '',
  upstreamId: 0,
  upstreamName: '',
  prompt: '你好，用一句话自我介绍。',
  result: null as ModelChatResult | null,
  error: ''
})

const aliasOptions = computed(() => pools.value.map((p) => p.alias))

function openTester(row?: ModelUpstream) {
  tester.visible = true
  tester.result = null
  tester.error = ''
  if (row) {
    tester.mode = 'upstream'
    tester.upstreamId = row.id
    tester.upstreamName = row.name
    tester.alias = row.alias
  } else {
    tester.mode = 'alias'
    tester.upstreamId = 0
    tester.upstreamName = ''
    tester.alias = aliasOptions.value[0] || ''
  }
}

async function runTest() {
  if (tester.mode === 'alias' && !tester.alias) {
    ElMessage.warning('先选一个模型（Alias）')
    return
  }
  loading.call = true
  tester.error = ''
  tester.result = null
  try {
    tester.result = await chatCompletion({
      model: tester.mode === 'alias' ? tester.alias : undefined,
      upstreamId: tester.mode === 'upstream' ? tester.upstreamId : undefined,
      messages: [{ role: 'user', content: tester.prompt }],
      caller: 'console'
    })
  } catch (err: any) {
    tester.error = err?.message || '调用失败'
  } finally {
    loading.call = false
    if (tab.value === 'calls') loadCalls()
  }
}

// ---------- 调用流水 ----------

const calls = ref<ModelCall[]>([])
const callTotal = ref(0)
const summary = ref<ModelCallSummary>({
  calls: 0, failed: 0, tokens: 0, cost: 0, avgLatencyMs: 0, usageMissing: 0
})
const callQuery = reactive({
  page: 1, pageSize: 20, alias: '', status: '', caller: '', username: '', start: '', end: ''
})

async function loadCalls() {
  loading.log = true
  try {
    const data = await listModelCalls(callQuery)
    calls.value = data.list || []
    callTotal.value = data.total
    summary.value = data.summary
  } finally {
    loading.log = false
  }
}

function searchCalls() {
  callQuery.page = 1
  loadCalls()
}

let callsLoaded = false
function onTabChange(name: string) {
  if (name === 'calls' && !callsLoaded) {
    callsLoaded = true
    loadCalls()
  }
}

function money(value: number) {
  if (!value) return '0'
  return value < 0.01 ? value.toFixed(6) : value.toFixed(4)
}

const callerLabels: Record<string, string> = {
  api: '接口调用',
  console: '界面试调用',
  check: '探活检查'
}
function callerText(caller: string) {
  return callerLabels[caller] || caller
}

onMounted(loadUpstreams)
</script>

<template>
  <div class="page">
    <el-card>
      <el-tabs v-model="tab" @tab-change="onTabChange">
        <el-tab-pane label="上游与池子" name="upstreams">
          <el-alert type="info" :closable="false" show-icon class="notice">
            调用方只认 Alias，平台按权重挑一条上游转发，失败自动换下一条 —— 换供应商、加降级备份都不用改调用方代码。
            接口是 <code>POST /api/v1/ai/chat/completions</code>，参数与 OpenAI 的 chat 接口一致（<code>model</code> 传 Alias）。
            <strong>只支持非流式</strong>；成本按下面登记的单价算，是估值而不是账单。
            「检查」会拉一次模型列表<strong>并真打一次 max_tokens=1 的最小调用</strong>（不少实现的 <code>/models</code> 不校验密钥，
            只探列表会得出「可用」的假结论），这一次也会计入用量流水。
          </el-alert>

          <div v-if="pools.length" class="pools">
            <el-tag
              v-for="item in pools"
              :key="item.alias"
              size="large"
              :type="item.healthy > 0 ? 'success' : item.enabled > 0 ? 'warning' : 'info'"
            >
              {{ item.alias }}：{{ item.healthy }} 可用 / {{ item.enabled }} 启用 / {{ item.total }} 条
            </el-tag>
          </div>

          <div class="page-toolbar">
            <el-button @click="loadUpstreams">刷新</el-button>
            <el-button :disabled="!pools.length" @click="openTester()">试调用</el-button>
            <div class="flex-1" />
            <el-button v-perm="'model:manage'" type="primary" @click="openForm()">新增上游</el-button>
          </div>

          <el-table v-loading="loading.list" :data="upstreams" style="width: 100%">
            <el-table-column label="Alias / 名称" min-width="200">
              <template #default="{ row }">
                <div><strong>{{ row.alias }}</strong></div>
                <div class="sub">{{ row.name }}</div>
              </template>
            </el-table-column>
            <el-table-column label="上游" min-width="260">
              <template #default="{ row }">
                <div>{{ row.provider }} · {{ row.model }}</div>
                <div class="sub">{{ row.baseUrl }}</div>
              </template>
            </el-table-column>
            <el-table-column label="权重" width="80" prop="weight" />
            <el-table-column label="单价（元/千 token）" width="170">
              <template #default="{ row }">
                入 {{ row.inputPrice || 0 }} / 出 {{ row.outputPrice || 0 }}
              </template>
            </el-table-column>
            <el-table-column label="状态" width="180">
              <template #default="{ row }">
                <el-tag :type="statusMeta[row.status]?.type || 'info'" size="small">
                  {{ statusMeta[row.status]?.text || row.status }}
                </el-tag>
                <span v-if="row.status === 'healthy'" class="sub"> {{ row.latencyMs }} ms</span>
                <div v-if="row.status === 'healthy' && !row.modelListed" class="sub warn">
                  上游未列出该模型名
                </div>
                <div v-if="row.lastError" class="sub danger">{{ row.lastError }}</div>
              </template>
            </el-table-column>
            <el-table-column label="启用" width="80">
              <template #default="{ row }">
                <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
                  {{ row.enabled ? '是' : '否' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="220" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" :loading="loading.check" @click="check(row)">检查</el-button>
                <el-button link type="primary" @click="openTester(row)">试调用</el-button>
                <el-button v-perm="'model:manage'" link type="primary" @click="openForm(row)">编辑</el-button>
                <el-button v-perm="'model:manage'" link type="danger" @click="remove(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="调用流水" name="calls">
          <el-alert type="info" :closable="false" show-icon class="notice">
            每次调用（含失败与自动切换）都落一条流水，token 与成本按上游返回的 usage 计。
            <strong>提示词与回复正文不落库</strong> —— 那是业务数据，要看内容请在调用方自己的日志里看。
          </el-alert>

          <div class="page-toolbar">
            <el-select v-model="callQuery.alias" placeholder="全部模型" clearable style="width: 160px" @change="searchCalls">
              <el-option v-for="item in aliasOptions" :key="item" :label="item" :value="item" />
            </el-select>
            <el-select v-model="callQuery.status" placeholder="全部状态" clearable style="width: 130px" @change="searchCalls">
              <el-option label="成功" value="success" />
              <el-option label="失败" value="failed" />
            </el-select>
            <el-select v-model="callQuery.caller" placeholder="全部来源" clearable style="width: 140px" @change="searchCalls">
              <el-option label="接口调用" value="api" />
              <el-option label="界面试调用" value="console" />
              <el-option label="探活检查" value="check" />
            </el-select>
            <el-input
              v-model="callQuery.username"
              placeholder="调用人"
              clearable
              style="width: 140px"
              @keyup.enter="searchCalls"
            />
            <el-date-picker
              v-model="callQuery.start"
              type="date"
              value-format="YYYY-MM-DD"
              placeholder="开始日期"
              style="width: 150px"
              @change="searchCalls"
            />
            <el-date-picker
              v-model="callQuery.end"
              type="date"
              value-format="YYYY-MM-DD"
              placeholder="结束日期"
              style="width: 150px"
              @change="searchCalls"
            />
            <el-button @click="searchCalls">查询</el-button>
          </div>

          <el-alert type="success" :closable="false" class="notice">
            命中 {{ summary.calls }} 次（失败 {{ summary.failed }}），合计 {{ summary.tokens }} token、
            约 {{ money(summary.cost) }} 元，平均耗时 {{ summary.avgLatencyMs }} ms
            <span v-if="summary.usageMissing">
              ；其中 {{ summary.usageMissing }} 次上游没给 usage，token 与成本不计入
            </span>
          </el-alert>

          <el-table v-loading="loading.log" :data="calls" style="width: 100%">
            <el-table-column label="时间" width="170" prop="createdAt" />
            <el-table-column label="模型" min-width="180">
              <template #default="{ row }">
                <div>{{ row.alias }}</div>
                <div class="sub">{{ row.upstreamName }} · {{ row.model }}</div>
              </template>
            </el-table-column>
            <el-table-column label="调用人" width="120">
              <template #default="{ row }">
                <div>{{ row.username || '-' }}</div>
                <div class="sub">{{ callerText(row.caller) }}</div>
              </template>
            </el-table-column>
            <el-table-column label="token（入/出）" width="140">
              <template #default="{ row }">
                <span v-if="row.usageMissing" class="sub warn">上游未返回</span>
                <span v-else>{{ row.promptTokens }} / {{ row.completionTokens }}</span>
              </template>
            </el-table-column>
            <el-table-column label="成本（元）" width="110">
              <template #default="{ row }">{{ money(row.cost) }}</template>
            </el-table-column>
            <el-table-column label="耗时" width="90">
              <template #default="{ row }">{{ row.latencyMs }} ms</template>
            </el-table-column>
            <el-table-column label="结果" min-width="220">
              <template #default="{ row }">
                <el-tag :type="row.status === 'success' ? 'success' : 'danger'" size="small">
                  {{ row.status === 'success' ? '成功' : '失败' }}
                </el-tag>
                <el-tag v-if="row.retried" type="warning" size="small" class="gap">切换后重试</el-tag>
                <div v-if="row.errorMsg" class="sub danger">{{ row.errorMsg }}</div>
              </template>
            </el-table-column>
          </el-table>

          <Pagination
            v-model:current-page="callQuery.page"
            v-model:page-size="callQuery.pageSize"
            :total="callTotal"
            @change="loadCalls"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="form.visible" :title="form.id ? '编辑上游' : '新增上游'" width="620px">
      <el-form label-width="120px">
        <el-form-item label="对外模型名">
          <el-input v-model="form.alias" placeholder="调用方传的 model，如 chat-default" />
          <div class="hint">同一个 Alias 可以挂多条上游，权重大的先用，失败自动切下一条。</div>
        </el-form-item>
        <el-form-item label="上游名称">
          <el-input v-model="form.name" placeholder="给人看的名字，如 deepseek-主力" />
        </el-form-item>
        <el-form-item label="供应商">
          <el-input v-model="form.provider" placeholder="openai / deepseek / qwen / ollama ..." />
        </el-form-item>
        <el-form-item label="接口地址">
          <el-input v-model="form.baseUrl" placeholder="https://api.deepseek.com/v1" />
          <div class="hint">填到 /v1 为止，平台会自己拼 /chat/completions 与 /models。</div>
        </el-form-item>
        <el-form-item label="API Key">
          <el-input
            v-model="form.apiKey"
            type="password"
            show-password
            :placeholder="form.id ? '留空表示不修改' : '本地部署（Ollama 等）可以留空'"
          />
        </el-form-item>
        <el-form-item label="上游模型名">
          <el-input v-model="form.model" placeholder="deepseek-chat / qwen2.5:7b ..." />
        </el-form-item>
        <el-form-item label="权重">
          <el-input-number v-model="form.weight" :min="1" :max="1000" />
          <span class="hint inline">越大越优先，主备关系比按比例分流更好复盘</span>
        </el-form-item>
        <el-form-item label="超时">
          <el-input-number v-model="form.timeoutSec" :min="5" :max="600" />
          <span class="hint inline">秒，长回答要给够</span>
        </el-form-item>
        <el-form-item label="输入单价">
          <el-input-number v-model="form.inputPrice" :min="0" :step="0.001" :precision="6" />
          <span class="hint inline">元 / 千 token，留 0 表示不算成本</span>
        </el-form-item>
        <el-form-item label="输出单价">
          <el-input-number v-model="form.outputPrice" :min="0" :step="0.001" :precision="6" />
          <span class="hint inline">元 / 千 token</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
          <span class="hint inline">停用后不参与挑选，但仍可单独试调用</span>
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

    <el-dialog v-model="tester.visible" title="试调用" width="640px">
      <el-form label-width="90px">
        <el-form-item label="目标">
          <el-radio-group v-model="tester.mode">
            <el-radio-button value="alias">按 Alias 走池子</el-radio-button>
            <el-radio-button value="upstream" :disabled="!tester.upstreamId">
              指定这条上游
            </el-radio-button>
          </el-radio-group>
          <div v-if="tester.mode === 'upstream'" class="hint">
            直接试「{{ tester.upstreamName }}」，不走挑选与切换，停用的也能试。
          </div>
        </el-form-item>
        <el-form-item v-if="tester.mode === 'alias'" label="模型">
          <el-select v-model="tester.alias" style="width: 100%">
            <el-option v-for="item in aliasOptions" :key="item" :label="item" :value="item" />
          </el-select>
        </el-form-item>
        <el-form-item label="提示词">
          <el-input v-model="tester.prompt" type="textarea" :rows="3" />
        </el-form-item>
      </el-form>

      <el-alert v-if="tester.error" type="error" :closable="false" show-icon class="notice">
        {{ tester.error }}
      </el-alert>
      <div v-if="tester.result" class="result">
        <div class="result-meta">
          {{ tester.result.upstreamName }} · {{ tester.result.model }} ·
          {{ tester.result.latencyMs }} ms ·
          token {{ tester.result.usage.promptTokens }}/{{ tester.result.usage.completionTokens }} ·
          约 {{ money(tester.result.cost) }} 元
          <span v-if="tester.result.usage.missing" class="warn">（上游没返回 usage）</span>
        </div>
        <div v-if="tester.result.attempts.length > 1" class="sub warn">
          前面有上游失败并自动切换，详情见「调用流水」
        </div>
        <pre class="result-body">{{ tester.result.content }}</pre>
      </div>

      <template #footer>
        <el-button @click="tester.visible = false">关闭</el-button>
        <el-button v-perm="'model:call'" type="primary" :loading="loading.call" @click="runTest">
          发起调用
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
}
.pools {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
}
.flex-1 {
  flex: 1;
}
.sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.danger {
  color: var(--el-color-danger);
}
.warn {
  color: var(--el-color-warning);
}
.gap {
  margin-left: 6px;
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
  max-height: 260px;
  overflow: auto;
}
</style>
