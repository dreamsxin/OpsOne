<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createScript,
  deleteScript,
  listHosts,
  listScriptCategories,
  listScripts,
  precheckScript,
  renderScript,
  runScript,
  updateScript,
  type ExecJob,
  type Host,
  type PrecheckHit,
  type Script,
  type ScriptParam
} from '@/api'

const loading = ref(false)
const rows = ref<Script[]>([])
const total = ref(0)
const categories = ref<string[]>([])
const hosts = ref<Host[]>([])
const query = reactive({ page: 1, pageSize: 20, category: '', precheckStatus: '', keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  category: '',
  description: '',
  content: '',
  paramList: [] as ScriptParam[],
  timeout: 60,
  riskLevel: 'low',
  enabled: true
})
const rules = {
  name: [{ required: true, message: '请输入脚本名称', trigger: 'blur' }],
  content: [{ required: true, message: '请输入脚本内容', trigger: 'blur' }]
}

const runVisible = ref(false)
const current = ref<Script | null>(null)
const runParams = ref<Record<string, string>>({})
const runHostIDs = ref<number[]>([])
const renderedCommand = ref('')
const runJob = ref<ExecJob | null>(null)
const running = ref(false)

const hitsVisible = ref(false)
const hits = ref<PrecheckHit[]>([])
const hitsTitle = ref('')

const precheckMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  pass: { text: '通过', type: 'success' },
  warn: { text: '有提醒', type: 'warning' },
  blocked: { text: '被拦截', type: 'danger' },
  unknown: { text: '未预检', type: 'info' }
}

const riskMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  low: { text: '低', type: 'success' },
  medium: { text: '中', type: 'warning' },
  high: { text: '高', type: 'danger' }
}

const resultType: Record<string, 'success' | 'danger' | 'warning'> = {
  success: 'success',
  failed: 'danger',
  timeout: 'warning'
}

const currentParams = computed<ScriptParam[]>(() => {
  if (!current.value?.params) return []
  try {
    return JSON.parse(current.value.params)
  } catch {
    return []
  }
})

function parseHits(raw: string): PrecheckHit[] {
  if (!raw) return []
  try {
    return JSON.parse(raw) || []
  } catch {
    return []
  }
}

async function load() {
  loading.value = true
  try {
    const [data, cats] = await Promise.all([listScripts(query), listScriptCategories()])
    rows.value = data.list || []
    total.value = data.total
    categories.value = cats || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    category: '',
    description: '',
    content: '',
    paramList: [],
    timeout: 60,
    riskLevel: 'low',
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: Script) {
  editingId.value = row.id
  let paramList: ScriptParam[] = []
  try {
    paramList = row.params ? JSON.parse(row.params) : []
  } catch {
    paramList = []
  }
  Object.assign(form, {
    name: row.name,
    category: row.category,
    description: row.description,
    content: row.content,
    paramList,
    timeout: row.timeout,
    riskLevel: row.riskLevel,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

function addParam() {
  form.paramList.push({ name: '', label: '', default: '', required: false })
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  const payload = {
    name: form.name,
    category: form.category,
    description: form.description,
    content: form.content,
    params: JSON.stringify(form.paramList),
    timeout: form.timeout,
    riskLevel: form.riskLevel,
    enabled: form.enabled
  }
  const res = editingId.value
    ? await updateScript(editingId.value, payload)
    : await createScript(payload)

  const status = res.script.precheckStatus
  if (status === 'blocked') {
    ElMessage.error('已保存，但命中拦截级命令规则，该脚本不允许下发')
  } else if (status === 'warn') {
    ElMessage.warning('已保存，命中提醒级命令规则，下发前请确认')
  } else {
    ElMessage.success('已保存，预检通过')
  }
  dialogVisible.value = false
  load()
}

async function recheck(row: Script) {
  const res = await precheckScript(row.id)
  ElMessage({ message: res.detail, type: res.precheckStatus === 'blocked' ? 'error' : 'success' })
  load()
}

function showHits(row: Script) {
  hits.value = parseHits(row.precheckHits)
  hitsTitle.value = `预检命中 · ${row.name}`
  hitsVisible.value = true
}

async function openRun(row: Script) {
  current.value = row
  runHostIDs.value = []
  runJob.value = null
  renderedCommand.value = ''
  const values: Record<string, string> = {}
  for (const p of currentParams.value) {
    values[p.name] = p.default || ''
  }
  runParams.value = values
  await preview()
  runVisible.value = true
}

async function preview() {
  if (!current.value) return
  try {
    const res = await renderScript(current.value.id, runParams.value)
    renderedCommand.value = res.command
  } catch {
    renderedCommand.value = ''
  }
}

async function submitRun() {
  if (!current.value) return
  if (!runHostIDs.value.length) {
    ElMessage.warning('请选择至少一台目标主机')
    return
  }
  const prodHosts = hosts.value.filter((h) => runHostIDs.value.includes(h.id) && h.env === 'prod')
  if (prodHosts.length) {
    await ElMessageBox.confirm(
      `目标包含 ${prodHosts.length} 台生产主机，确认下发脚本「${current.value.name}」？`,
      '生产环境确认',
      { type: 'warning' }
    )
  }

  running.value = true
  try {
    const res = await runScript(current.value.id, {
      hostIds: runHostIDs.value,
      params: runParams.value,
      // 后端会自己判断目标里有没有生产主机；这里如实告诉它「人已经确认过了」
      confirmProd: prodHosts.length > 0
    })

    runJob.value = res.job
    renderedCommand.value = res.command
    ElMessage.success(`执行完成：成功 ${res.job.successNum}，失败 ${res.job.failedNum}`)
    load()
  } finally {
    running.value = false
  }
}

async function remove(row: Script) {
  await ElMessageBox.confirm(`确认删除脚本「${row.name}」？已产生的执行记录不受影响`, '提示', {
    type: 'warning'
  })
  await deleteScript(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="脚本保存与下发前都会用「命令规则」做一次静态预检：命中拦截级规则的脚本不允许下发，命中提醒级只做提示。预检是文本匹配，挡得住写在明面上的高危命令，挡不住 base64 解码执行之类的绕过。下发走批量执行同一条链路，受同样的数据权限与并发限制。"
      />

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 说明 / 内容"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.category" placeholder="分类" clearable style="width: 140px">
          <el-option v-for="c in categories" :key="c" :label="c" :value="c" />
        </el-select>
        <el-select v-model="query.precheckStatus" placeholder="预检状态" clearable style="width: 130px">
          <el-option v-for="(meta, key) in precheckMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'script:manage'" type="primary" @click="openCreate">新建脚本</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有脚本">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="category" label="分类" width="110" />
        <el-table-column prop="description" label="说明" min-width="160" show-overflow-tooltip />
        <el-table-column label="风险" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="riskMeta[row.riskLevel]?.type || 'info'">
              {{ riskMeta[row.riskLevel]?.text || row.riskLevel }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="预检" width="110">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="precheckMeta[row.precheckStatus]?.type || 'info'"
              :style="{ cursor: row.precheckHits && row.precheckHits !== '[]' ? 'pointer' : 'default' }"
              @click="row.precheckHits && row.precheckHits !== '[]' && showHits(row)"
            >
              {{ precheckMeta[row.precheckStatus]?.text || row.precheckStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="使用" width="100">
          <template #default="{ row }">
            {{ row.useCount }} 次
            <span v-if="row.lastUsedBy" style="color: #6b7280">/{{ row.lastUsedBy }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="creatorName" label="维护人" width="100" />
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'exec:run'" link type="primary" @click="openRun(row)">下发</el-button>
            <el-button link type="primary" @click="recheck(row)">重新预检</el-button>
            <el-button v-perm="'script:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'script:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑脚本' : '新建脚本'" width="680px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="分类">
          <el-select
            v-model="form.category"
            filterable
            allow-create
            default-first-option
            placeholder="可新建分类，留空归入「未分类」"
            style="width: 220px"
          >
            <el-option v-for="c in categories" :key="c" :label="c" :value="c" />
          </el-select>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" />
        </el-form-item>
        <el-form-item label="脚本内容" prop="content">
          <el-input
            v-model="form.content"
            type="textarea"
            :rows="8"
            placeholder="shell 命令，可用 ${PARAM} 引用参数，例如：df -h ${MOUNT}"
          />
        </el-form-item>
        <el-form-item label="参数">
          <div v-for="(p, index) in form.paramList" :key="index" class="param-row">
            <el-input v-model="p.name" placeholder="参数名（大写字母数字下划线）" style="width: 190px" />
            <el-input v-model="p.label" placeholder="显示名" style="width: 130px" />
            <el-input v-model="p.default" placeholder="默认值" style="width: 140px" />
            <el-checkbox v-model="p.required">必填</el-checkbox>
            <el-button link type="danger" @click="form.paramList.splice(index, 1)">移除</el-button>
          </div>
          <el-button link type="primary" @click="addParam">添加参数</el-button>
        </el-form-item>
        <el-form-item label="超时">
          <el-input-number v-model="form.timeout" :min="1" :max="600" />
          <span style="margin-left: 8px; color: #6b7280">秒（单机）</span>
        </el-form-item>
        <el-form-item label="风险等级">
          <el-select v-model="form.riskLevel" style="width: 120px">
            <el-option v-for="(meta, key) in riskMeta" :key="key" :label="meta.text" :value="key" />
          </el-select>
          <span style="margin-left: 8px; color: #6b7280">仅作提示，拦截以预检结果为准</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存并预检</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="runVisible" :title="`下发脚本 · ${current?.name ?? ''}`" size="58%">
      <el-alert
        v-if="current?.precheckStatus === 'warn'"
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="该脚本命中过提醒级命令规则，请确认内容后再下发"
      />

      <el-form label-width="100px">
        <el-form-item v-for="p in currentParams" :key="p.name" :label="p.label || p.name">
          <el-input v-model="runParams[p.name]" :placeholder="p.required ? '必填' : '可留空'" @blur="preview" />
        </el-form-item>
        <el-form-item label="目标主机">
          <el-select v-model="runHostIDs" multiple filterable style="width: 100%">
            <el-option
              v-for="host in hosts"
              :key="host.id"
              :label="`${host.name}（${host.address}）${host.env}`"
              :value="host.id"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <div style="margin-bottom: 6px; font-weight: 500">最终执行的命令</div>
      <pre class="output-pre">{{ renderedCommand || '（参数不完整，无法渲染）' }}</pre>

      <div style="margin: 12px 0">
        <el-button type="primary" :loading="running" :disabled="!renderedCommand" @click="submitRun">
          确认下发
        </el-button>
        <el-button @click="preview">刷新预览</el-button>
      </div>

      <template v-if="runJob">
        <el-divider />
        <div style="margin-bottom: 8px">
          执行结果：
          <el-tag type="success" size="small">成功 {{ runJob.successNum }}</el-tag>
          <el-tag type="danger" size="small" style="margin-left: 4px">失败 {{ runJob.failedNum }}</el-tag>
          <span style="margin-left: 6px; color: #6b7280">共 {{ runJob.total }} 台</span>
        </div>
        <el-collapse>
          <el-collapse-item v-for="item in runJob.results || []" :key="item.id" :name="item.id">
            <template #title>
              <el-tag size="small" :type="resultType[item.status] || 'info'">{{ item.status }}</el-tag>
              <span style="margin-left: 8px">{{ item.hostName }}（{{ item.address }}）</span>
              <span style="margin-left: 8px; color: #6b7280">{{ item.costMs }}ms</span>
            </template>
            <pre class="output-pre">{{ item.stdout || item.stderr || '（无输出）' }}</pre>
          </el-collapse-item>
        </el-collapse>
      </template>
    </el-drawer>

    <el-drawer v-model="hitsVisible" :title="hitsTitle" size="48%">
      <el-table :data="hits" border size="small" empty-text="没有命中记录">
        <el-table-column label="动作" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.action === 'block' ? 'danger' : 'warning'">
              {{ row.action === 'block' ? '拦截' : '提醒' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="line" label="行号" width="70" />
        <el-table-column prop="description" label="规则说明" min-width="150" />
        <el-table-column prop="pattern" label="正则" min-width="150" show-overflow-tooltip />
        <el-table-column prop="snippet" label="命中内容" min-width="180" show-overflow-tooltip />
      </el-table>
    </el-drawer>
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
