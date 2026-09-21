<script setup lang="ts">
import { computed, onActivated, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'

import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  checkHost,
  createHost,
  deleteHost,
  downloadHostTemplate,
  exportHostsCSV,
  getDepartmentTree,
  importHosts,
  listCredentials,
  listHosts,
  listTags,
  updateHost,
  type Credential,
  type DeptNode,

  type Host,
  type HostImportResult,
  type Tag
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import Pagination from '@/components/Pagination.vue'


const route = useRoute()

const importVisible = ref(false)
const importFile = ref<File | null>(null)
const importDryRun = ref(true)
const importing = ref(false)
const importResult = ref<HostImportResult | null>(null)

const importActionMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  create: { text: '新建', type: 'success' },
  update: { text: '更新', type: 'warning' },
  skip: { text: '跳过', type: 'danger' }
}

function openImport() {
  importFile.value = null
  importResult.value = null
  importDryRun.value = true
  importVisible.value = true
}

// el-upload 设为手动上传，这里只接住文件对象
function onImportFileChange(file: { raw: File }) {
  importFile.value = file.raw
  importResult.value = null
}

async function submitImport() {
  if (!importFile.value) {
    ElMessage.warning('请选择 CSV 文件')
    return
  }
  importing.value = true
  try {
    const res = await importHosts(importFile.value, importDryRun.value)
    importResult.value = res
    if (res.dryRun) {
      ElMessage.info(res.detail)
    } else {
      ElMessage.success(res.detail)
      load()
    }
  } finally {
    importing.value = false
  }
}

async function doExport() {
  await exportHostsCSV(query.env || undefined)
  ElMessage.success('已导出（不含登录凭据）')
}




const loading = ref(false)
const rows = ref<Host[]>([])
const total = ref(0)
const query = reactive({
  page: 1,
  pageSize: 20,
  keyword: '',
  env: '',
  status: ''
})

/**
 * 深链参数（仪表盘「离线主机」卡片带 status=offline，命令面板带 keyword=主机名）。
 * 本页会被页签工作台缓存，带着新参数再跳进来不会重新挂载，所以 activate 时也认一次；
 * appliedQuery 用来避免首屏挂载 + activate 查两遍。
 */
let appliedQuery = ''
function applyRouteQuery(): boolean {
  const kw = String(route.query.keyword ?? '')
  const env = String(route.query.env ?? '')
  const status = String(route.query.status ?? '')
  const stamp = `${kw}|${env}|${status}`
  if (!kw && !env && !status) return false
  if (stamp === appliedQuery) return false
  appliedQuery = stamp
  query.keyword = kw
  query.env = env
  query.status = status
  query.page = 1
  return true
}

/** 多选与批量操作：参考站 /assets/hosts 的工具栏中 60% 都是批量动作，
 *  没选中时隐藏、选中后才出现，避免给单主机场景加噪声 */
const selection = ref<Host[]>([])
function onSelectionChange(list: Host[]) {
  selection.value = list
}
async function batchDelete() {
  const prodCount = selection.value.filter((h) => h.env === 'prod').length
  const warn = prodCount ? `其中包含 ${prodCount} 台生产主机，` : ''
  await ElMessageBox.confirm(
    `${warn}确认删除选中的 ${selection.value.length} 台主机？删除后关联的会话与执行记录仍在，但无法再登陆。`,
    '危险操作',
    { type: 'warning', confirmButtonText: '确认删除', cancelButtonText: '取消' }
  )
  let ok = 0
  const failed: string[] = []
  for (const h of selection.value) {
    try {
      await deleteHost(h.id)
      ok++
    } catch (e: any) {
      failed.push(`${h.name}: ${e?.message || e}`)
    }
  }
  if (failed.length) ElMessage.warning(`成功 ${ok}，失败 ${failed.length}：${failed[0]}`)
  else ElMessage.success(`已删除 ${ok} 台`)
  selection.value = []
  load()
  loadAllHosts()
}
async function batchCheck() {
  const targets = [...selection.value]
  if (!targets.length) return
  let ok = 0
  let fail = 0
  for (const h of targets) {
    try {
      const res = await checkHost(h.id)
      if (res.status === 'online') ok++
      else fail++
    } catch {
      fail++
    }
  }
  ElMessage.success(`探测完成：在线 ${ok}，异常 ${fail}`)
  load()
}
const batchEnvVisible = ref(false)
const batchEnvValue = ref<'dev' | 'test' | 'prod'>('dev')
async function batchChangeEnv() {
  const targets = [...selection.value]
  let ok = 0
  const failed: string[] = []
  for (const h of targets) {
    try {
      // 保留 secret：后端 PUT 对空 secret 的语义是「不改」
      await updateHost(h.id, { ...h, env: batchEnvValue.value, secret: '' })
      ok++
    } catch (e: any) {
      failed.push(`${h.name}: ${e?.message || e}`)
    }
  }
  if (failed.length) ElMessage.warning(`成功 ${ok}，失败 ${failed.length}：${failed[0]}`)
  else ElMessage.success(`已将 ${ok} 台主机环境改为 ${batchEnvValue.value}`)
  batchEnvVisible.value = false
  selection.value = []
  load()
}
const batchTagVisible = ref(false)
const batchTagValue = ref('')
async function batchAppendTag() {
  const tag = batchTagValue.value.trim()
  if (!tag) {
    ElMessage.warning('请输入标签')
    return
  }
  let ok = 0
  const failed: string[] = []
  for (const h of selection.value) {
    const cur = (h.tags || '').split(',').map((x) => x.trim()).filter(Boolean)
    if (cur.includes(tag)) {
      ok++
      continue
    }
    const next = [...cur, tag].join(',')
    try {
      await updateHost(h.id, { ...h, tags: next, secret: '' })
      ok++
    } catch (e: any) {
      failed.push(`${h.name}: ${e?.message || e}`)
    }
  }
  if (failed.length) ElMessage.warning(`成功 ${ok}，失败 ${failed.length}：${failed[0]}`)
  else ElMessage.success(`已为 ${ok} 台主机追加标签「${tag}」`)
  batchTagVisible.value = false
  batchTagValue.value = ''
  selection.value = []
  load()
}

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  address: '',
  port: 22,
  username: 'root',
  authType: 'password' as 'password' | 'key',
  secret: '',
  env: 'dev' as 'dev' | 'test' | 'prod',
  tags: '',
  remark: '',
  proxyHostId: 0,
  deptId: 0,
  // 0 表示本机自填凭据；非 0 表示引用凭证库里的共享凭据
  credentialId: 0
})

// 凭证库里的可选凭据。列表接口不返回密钥，只用来做下拉与展示
const credentials = ref<Credential[]>([])
const credentialLabel = computed(() => {
  const map = new Map(credentials.value.map((c) => [c.id, `${c.name}（${c.username}）`]))
  return (id: number) => map.get(id) || `凭据 #${id}`
})


// 部门树用于归属选择与列表展示
const deptTree = ref<DeptNode[]>([])
// 标签字典用于表单下拉建议
const tagDict = ref<Tag[]>([])

// tags 存的是逗号分隔文本，表单里用数组操作
const tagList = computed({
  get: () => (form.tags ? form.tags.split(',').map((t) => t.trim()).filter(Boolean) : []),
  set: (value: string[]) => (form.tags = value.join(','))
})

const deptNameMap = computed(() => {
  const map = new Map<number, string>()
  const walk = (nodes: DeptNode[]) => {
    for (const node of nodes) {
      map.set(node.id, node.name)
      if (node.children?.length) walk(node.children)
    }
  }
  walk(deptTree.value)
  return map
})



const rules = {
  name: [{ required: true, message: '请输入主机名称', trigger: 'blur' }],
  address: [{ required: true, message: '请输入 IP 或域名', trigger: 'blur' }],
  username: [{ required: true, message: '请输入登录用户', trigger: 'blur' }]
}

const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  online: { text: '在线', type: 'success' },
  offline: { text: '离线', type: 'danger' },
  unknown: { text: '未探测', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    const data = await listHosts(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

// 跳板机候选需要全量主机，与分页列表分开取
const allHosts = ref<Host[]>([])
async function loadAllHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  allHosts.value = data.list || []
}

const proxyCandidates = computed(() =>
  allHosts.value.filter((h) => !editingId.value || h.id !== editingId.value)
)

function hostLabel(id: number) {
  const host = allHosts.value.find((h) => h.id === id)
  return host ? `${host.name}(${host.address})` : `#${id}`
}


function resetQuery() {
  query.keyword = ''
  query.env = ''
  query.status = ''
  query.page = 1
  load()
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    address: '',
    port: 22,
    username: 'root',
    authType: 'password',
    secret: '',
    env: 'dev',
    tags: '',
    remark: '',
    proxyHostId: 0,
    deptId: 0,
    credentialId: 0
  })
  dialogVisible.value = true
}




function openEdit(row: Host) {
  editingId.value = row.id
  Object.assign(form, { ...row, secret: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  // 引用共享凭据时不需要本机口令；两种情况都没给才是真的不行
  if (!editingId.value && !form.secret && !form.credentialId) {
    ElMessage.warning('新增主机必须填写密码或私钥，或改为引用凭证库')
    return
  }


  if (editingId.value) {
    await updateHost(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createHost({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
  loadAllHosts()
}

async function remove(row: Host) {
  await ElMessageBox.confirm(`确认删除主机「${row.name}」？`, '危险操作', { type: 'warning' })
  await deleteHost(row.id)
  ElMessage.success('已删除')
  load()
  loadAllHosts()
}

async function check(row: Host) {
  const res = await checkHost(row.id)
  if (res.status === 'online') {
    const via = res.viaProxy ? `（经 ${res.viaProxy}）` : ''
    ElMessage.success(`连通正常${via} ${res.costMs}ms ${res.osInfo}`)
  } else {
    ElMessage.error(`连接失败：${res.detail || '未知原因'}`)
  }
  load()
}

onMounted(() => {
  applyRouteQuery()
  load()
  loadAllHosts()
  getDepartmentTree().then((data) => (deptTree.value = data))
  listTags().then((data) => (tagDict.value = data))
  listCredentials({ page: 1, pageSize: 200 }).then((data) => (credentials.value = data.list || []))
})


// 缓存页被带着新参数再次打开时重新应用筛选
onActivated(() => {
  if (applyRouteQuery()) load()
})



</script>

<template>
  <div class="page">
    <PageHeader
      title="主机管理"
      subtitle="支持环境 / 状态 / 标签筛选；探测会回填系统信息，跳板机链路直接标在列表上"
    >
      <template #actions>
        <el-button @click="doExport">
          <el-icon style="margin-right: 4px"><Download /></el-icon>
          导出 CSV
        </el-button>
        <el-button v-perm="'host:create'" @click="openImport">
          <el-icon style="margin-right: 4px"><Upload /></el-icon>
          批量导入
        </el-button>
        <el-button v-perm="'host:create'" type="primary" @click="openCreate">
          <el-icon style="margin-right: 4px"><Plus /></el-icon>
          新增主机
        </el-button>
      </template>
    </PageHeader>

    <el-card>
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 地址 / 标签"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.env" placeholder="环境" clearable style="width: 120px">
          <el-option label="开发" value="dev" />
          <el-option label="测试" value="test" />
          <el-option label="生产" value="prod" />
        </el-select>
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option label="在线" value="online" />
          <el-option label="离线" value="offline" />
          <el-option label="未探测" value="unknown" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="resetQuery">重置</el-button>
        <el-button :loading="loading" @click="load">
          <el-icon v-if="!loading" style="margin-right: 4px"><Refresh /></el-icon>
          刷新
        </el-button>
      </div>

      <div v-if="selection.length" class="bulk-bar">
        <span class="bulk-hint">已选 {{ selection.length }} 项</span>
        <el-button v-perm="'host:check'" size="small" @click="batchCheck">批量探测</el-button>
        <el-button v-perm="'host:update'" size="small" @click="batchEnvVisible = true">改环境</el-button>
        <el-button v-perm="'host:update'" size="small" @click="batchTagVisible = true">加标签</el-button>
        <el-button v-perm="'host:delete'" size="small" type="danger" plain @click="batchDelete">批量删除</el-button>
        <div class="grow"></div>
        <el-button link size="small" @click="selection = []">清空选择</el-button>
      </div>

      <el-table
        v-loading="loading"
        :data="rows"
        border
        stripe
        @selection-change="onSelectionChange"
      >
        <el-table-column type="selection" width="42" />
        <el-table-column prop="name" label="名称" min-width="130" show-overflow-tooltip />
        <el-table-column label="连接" min-width="170">
          <template #default="{ row }">{{ row.username }}@{{ row.address }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column prop="env" label="环境" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.env === 'prod' ? 'danger' : row.env === 'test' ? 'warning' : 'info'">
              {{ row.env }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="authType" label="认证" width="150">
          <template #default="{ row }">
            <template v-if="row.credentialId">
              <el-tag size="small" type="success" effect="plain">共享凭据</el-tag>
              <div class="cell-sub">{{ credentialLabel(row.credentialId) }}</div>
            </template>
            <span v-else>{{ row.authType === 'key' ? '私钥' : '密码' }}</span>
          </template>
        </el-table-column>

        <el-table-column prop="status" label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="osInfo" label="系统" min-width="150" show-overflow-tooltip />
        <el-table-column label="跳板机" min-width="140">
          <template #default="{ row }">
            <span v-if="row.proxyHostId">{{ hostLabel(row.proxyHostId) }}</span>
            <el-tag v-else size="small" type="info">直连</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="归属部门" min-width="130">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>

        <el-table-column prop="tags" label="标签" min-width="110" show-overflow-tooltip />

        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'host:check'" link type="primary" @click="check(row)">探测</el-button>
            <el-button v-perm="'host:update'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'host:delete'" link type="danger" @click="remove(row)">删除</el-button>
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

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑主机' : '新增主机'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="地址" prop="address">
          <el-input v-model="form.address" placeholder="IP 或域名" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="登录用户" prop="username">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item label="凭据来源">
          <el-select v-model="form.credentialId" style="width: 100%">
            <el-option label="本机自填（这台机器单独存一份口令 / 私钥）" :value="0" />
            <el-option
              v-for="c in credentials"
              :key="c.id"
              :label="`凭证库：${c.name}（${c.username}${c.type === 'key' ? ' · 私钥' : ''}）`"
              :value="c.id"
              :disabled="!c.enabled"
            />
          </el-select>
          <div class="form-hint">
            引用凭证库时，登录用户与密钥都以凭据为准，本机那份会被清空；凭据轮换后这台主机自动跟着变。
            从共享凭据切回本机自填，必须同时填一份新的密码或私钥。
          </div>
        </el-form-item>
        <template v-if="!form.credentialId">
          <el-form-item label="认证方式">
            <el-radio-group v-model="form.authType">
              <el-radio value="password">密码</el-radio>
              <el-radio value="key">私钥</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item :label="form.authType === 'key' ? '私钥' : '密码'">
            <el-input
              v-model="form.secret"
              :type="form.authType === 'key' ? 'textarea' : 'password'"
              :rows="4"
              show-password
              :placeholder="editingId ? '留空表示不修改' : '必填'"
            />
          </el-form-item>
        </template>

        <el-form-item label="环境">
          <el-select v-model="form.env">
            <el-option label="开发" value="dev" />
            <el-option label="测试" value="test" />
            <el-option label="生产" value="prod" />
          </el-select>
        </el-form-item>
        <el-form-item label="跳板机">
          <el-select v-model="form.proxyHostId" filterable style="width: 100%">
            <el-option label="直连（不经跳板机）" :value="0" />
            <el-option
              v-for="host in proxyCandidates"
              :key="host.id"
              :label="`${host.name}（${host.address}:${host.port}）`"
              :value="host.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="归属部门">
          <el-tree-select
            v-model="form.deptId"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            check-strictly
            style="width: 100%"
            placeholder="未归属（仅「全部数据」范围可见）"
          />
        </el-form-item>
        <el-form-item label="标签">
          <el-select
            v-model="tagList"
            multiple
            filterable
            allow-create
            default-first-option
            style="width: 100%"
            placeholder="从标签字典选择或直接输入"
          >
            <el-option v-for="tag in tagDict" :key="tag.id" :label="tag.name" :value="tag.name" />
          </el-select>
        </el-form-item>

        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="importVisible" title="批量导入主机" width="720px">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="按 address + port 判定是否同一台机器：已存在则更新（secret 留空保持原凭据），不存在则新建（必须给 secret）。建议先勾选「试运行」看报告，确认无误再正式导入。单次最多 500 行。"
      />

      <div style="display: flex; gap: 12px; align-items: center; flex-wrap: wrap">
        <el-button @click="downloadHostTemplate">下载模板</el-button>
        <el-upload
          :auto-upload="false"
          :limit="1"
          accept=".csv"
          :show-file-list="false"
          :on-change="onImportFileChange"
        >
          <el-button type="primary">选择 CSV</el-button>
        </el-upload>
        <span v-if="importFile" style="color: #6b7280">{{ importFile.name }}</span>
        <el-checkbox v-model="importDryRun">试运行（只校验不写入）</el-checkbox>
        <el-button :loading="importing" type="primary" @click="submitImport">
          {{ importDryRun ? '开始校验' : '确认导入' }}
        </el-button>
      </div>

      <template v-if="importResult">
        <el-divider />
        <el-alert
          :type="importResult.skipped ? 'warning' : 'success'"
          :closable="false"
          :title="importResult.detail"
          style="margin-bottom: 12px"
        />
        <el-table :data="importResult.rows" border size="small" max-height="320">
          <el-table-column prop="line" label="行号" width="70" />
          <el-table-column label="结果" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="importActionMeta[row.action]?.type || 'info'">
                {{ importActionMeta[row.action]?.text || row.action }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="name" label="名称" min-width="120" />
          <el-table-column prop="address" label="地址" min-width="120" />
          <el-table-column prop="reason" label="说明" min-width="240" show-overflow-tooltip />
        </el-table>
      </template>

      <template #footer>
        <el-button @click="importVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="batchEnvVisible" title="批量修改环境" width="420px">
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        title="环境变更会同步影响下发闸门（生产主机需二次确认），请谨慎操作"
        style="margin-bottom: 12px"
      />
      <el-form label-width="80px">
        <el-form-item label="目标环境">
          <el-radio-group v-model="batchEnvValue">
            <el-radio value="dev">开发</el-radio>
            <el-radio value="test">测试</el-radio>
            <el-radio value="prod">生产</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="影响主机">
          <span>{{ selection.length }} 台</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchEnvVisible = false">取消</el-button>
        <el-button type="primary" @click="batchChangeEnv">确认修改</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="batchTagVisible" title="批量追加标签" width="420px">
      <el-form label-width="80px">
        <el-form-item label="标签">
          <el-input v-model="batchTagValue" placeholder="已有同名标签不会重复追加" />
        </el-form-item>
        <el-form-item label="影响主机">
          <span>{{ selection.length }} 台</span>
        </el-form-item>
        <el-form-item v-if="tagDict.length" label="快选">
          <el-tag
            v-for="t in tagDict.slice(0, 12)"
            :key="t.id"
            size="small"
            style="margin-right: 4px; cursor: pointer"
            @click="batchTagValue = t.name"
          >
            {{ t.name }}
          </el-tag>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="batchTagVisible = false">取消</el-button>
        <el-button type="primary" @click="batchAppendTag">确认追加</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.cell-sub {
  font-size: 12px;
  color: var(--ops-text-secondary, #9ca3af);
  line-height: 1.4;
}
.form-hint {
  font-size: 12px;
  color: var(--ops-text-secondary, #9ca3af);
  line-height: 1.5;
}
.bulk-bar {

  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  margin-bottom: 8px;
  background: var(--el-color-primary-light-9);
  border-left: 3px solid var(--el-color-primary);
  border-radius: 4px;
}
.bulk-bar .grow {
  flex: 1;
}
</style>
