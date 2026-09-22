<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  collectLogUsage,
  createHostLogTarget,
  deleteHostLogTarget,
  hostLogMeta,
  listHostLogScans,
  listHostLogTargets,
  listHosts,
  listLogUsage,
  scanAllHostLogTargets,
  scanHostLogTarget,
  updateHostLogTarget,
  viewHostLog,
  type Host,
  type HostLogMeta,
  type HostLogScan,
  type HostLogTarget,
  type HostLogUsageRow,
  type HostLogViewResult
} from '@/api'

const tab = ref<'view' | 'targets' | 'usage'>('view')
const meta = ref<HostLogMeta | null>(null)
const hosts = ref<Host[]>([])

/* ---------- 实时查看 ---------- */
const viewLoading = ref(false)
const viewForm = reactive({
  hostId: 0 as number,
  path: '/var/log/messages',
  lines: 200,
  keyword: '',
  ignoreCase: true
})
const viewResult = ref<HostLogViewResult | null>(null)

/* ---------- 监控点 ---------- */
const targetLoading = ref(false)
const scanning = ref(false)
const targets = ref<HostLogTarget[]>([])
const targetTotal = ref(0)
const targetQuery = reactive({ page: 1, pageSize: 20, status: '', keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  hostId: 0 as number,
  path: '',
  keywords: 'error,fatal,panic',
  ignoreKeywords: '',
  maxBytes: 262144,
  alertEnabled: true,
  deptId: 0,
  enabled: true,
  remark: ''
})

/* ---------- 巡检历史 ---------- */
const historyVisible = ref(false)
const historyRows = ref<HostLogScan[]>([])
const historyTarget = ref<HostLogTarget | null>(null)

/* ---------- 磁盘占用 ---------- */
const usageLoading = ref(false)
const usageRows = ref<HostLogUsageRow[]>([])
const usageDir = ref('')
const usageNote = ref('')

const statusMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  ok: { label: '正常', type: 'success' },
  hit: { label: '命中关键字', type: 'danger' },
  missing: { label: '文件不存在', type: 'warning' },
  denied: { label: '读不了', type: 'warning' },
  failed: { label: '巡检失败', type: 'danger' },
  unknown: { label: '未巡检', type: 'info' }
}

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  path: [{ required: true, message: '请输入日志文件路径', trigger: 'blur' }]
}

const prefixHint = computed(() =>
  meta.value ? meta.value.pathPrefixes.join('、') : '/var/log'
)

function kb(v: number) {
  if (v >= 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + ' GB'
  if (v >= 1024) return (v / 1024).toFixed(1) + ' MB'
  return v + ' KB'
}

function bytes(v: number) {
  if (v >= 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + ' MB'
  if (v >= 1024) return (v / 1024).toFixed(1) + ' KB'
  return v + ' B'
}

async function doView() {
  if (!viewForm.hostId) {
    ElMessage.warning('请选择主机')
    return
  }
  viewLoading.value = true
  try {
    viewResult.value = await viewHostLog({ ...viewForm })
  } finally {
    viewLoading.value = false
  }
}

async function loadTargets() {
  targetLoading.value = true
  try {
    const params: Record<string, any> = { page: targetQuery.page, pageSize: targetQuery.pageSize }
    if (targetQuery.status) params.status = targetQuery.status
    if (targetQuery.keyword) params.keyword = targetQuery.keyword
    const page = await listHostLogTargets(params)
    targets.value = page.list
    targetTotal.value = page.total
  } finally {
    targetLoading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    hostId: viewForm.hostId || 0,
    path: '',
    keywords: 'error,fatal,panic',
    ignoreKeywords: '',
    maxBytes: meta.value?.scanMaxBytes ?? 262144,
    alertEnabled: true,
    deptId: 0,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: HostLogTarget) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    hostId: row.hostId,
    path: row.path,
    keywords: row.keywords,
    ignoreKeywords: row.ignoreKeywords,
    maxBytes: row.maxBytes,
    alertEnabled: row.alertEnabled,
    deptId: row.deptId,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

// 从实时查看那一屏直接把当前主机 + 路径变成一个常驻监控点
function watchCurrent() {
  if (!viewResult.value) return
  editingId.value = null
  Object.assign(form, {
    name: viewResult.value.path.split('/').pop() || '日志监控',
    hostId: viewResult.value.hostId,
    path: viewResult.value.path,
    keywords: viewForm.keyword || 'error,fatal,panic',
    ignoreKeywords: '',
    maxBytes: meta.value?.scanMaxBytes ?? 262144,
    alertEnabled: true,
    deptId: 0,
    enabled: true,
    remark: '从实时查看创建'
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateHostLogTarget(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createHostLogTarget({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  tab.value = 'targets'
  loadTargets()
}

async function runScan(row: HostLogTarget) {
  const after = await scanHostLogTarget(row.id)
  const m = statusMeta[after.lastStatus]
  const detail = after.lastStatus === 'hit'
    ? `命中 ${after.lastHitCount} 行：${after.lastSample}`
    : after.lastError || '没有新的命中'
  if (after.lastStatus === 'ok') ElMessage.success(`${m?.label}：${detail}`)
  else ElMessage.warning(`${m?.label}：${detail}`)
  loadTargets()
}

async function runScanAll() {
  scanning.value = true
  try {
    const s = await scanAllHostLogTargets()
    ElMessage.success(
      `共 ${s.checked}：正常 ${s.ok}，命中 ${s.hit}（${s.hitLines} 行），` +
        `文件缺失 ${s.missing}，读不了 ${s.denied}，失败 ${s.failed}`
    )
    loadTargets()
  } finally {
    scanning.value = false
  }
}

async function remove(row: HostLogTarget) {
  await ElMessageBox.confirm(
    `确认删除日志监控点「${row.name}」？巡检历史与它名下的告警会一并清掉。`,
    '危险操作',
    { type: 'warning' }
  )
  await deleteHostLogTarget(row.id)
  ElMessage.success('已删除')
  loadTargets()
}

async function openHistory(row: HostLogTarget) {
  historyTarget.value = row
  const page = await listHostLogScans({ targetId: row.id, pageSize: 50 })
  historyRows.value = page.list
  historyVisible.value = true
}

async function loadUsage() {
  usageLoading.value = true
  try {
    const res = await listLogUsage()
    usageRows.value = res.rows
    usageDir.value = res.dir
    usageNote.value = res.note
  } finally {
    usageLoading.value = false
  }
}

async function doCollectUsage() {
  usageLoading.value = true
  try {
    const res = await collectLogUsage()
    ElMessage.success(`${res.dir}：成功 ${res.ok} 台，失败 ${res.failed} 台`)
    await loadUsage()
  } finally {
    usageLoading.value = false
  }
}

onMounted(async () => {
  meta.value = await hostLogMeta()
  const page = await listHosts({ page: 1, pageSize: 500 })
  hosts.value = page.list
  if (hosts.value.length) viewForm.hostId = hosts.value[0].id
  await Promise.all([loadTargets(), loadUsage()])
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这一页<strong>不依赖任何日志系统</strong>：直接用 SSH 在主机上跑 <code>tail</code> /
          <code>grep</code> / <code>du</code> 读文件。没有部署 Loki 的环境里，这是唯一能看到日志的路径
          （「日志查询」那一页是 Loki 代理，没有 Loki 就是空页）。
          <br />
          <strong>全部是只读命令</strong>，平台不会改主机上的任何东西，也<strong>不把日志内容搬进库</strong> ——
          监控点只记「命中了几行」和命中的第一行样本。
          <br />
          路径必须落在允许的目录下（<strong>{{ prefixHint }}</strong>）。这是这个模块唯一真正的安全边界：
          没有它，能进这一页就等于能读主机上任意文件。要放开别的目录改 <code>OPS_LOG_PATH_PREFIXES</code>。
        </template>
      </el-alert>

      <el-tabs v-model="tab">
        <!-- ---------- 实时查看 ---------- -->
        <el-tab-pane label="实时查看" name="view">
          <el-form :model="viewForm" inline style="margin-bottom: 12px">
            <el-form-item label="主机">
              <el-select v-model="viewForm.hostId" filterable style="width: 200px" placeholder="选择主机">
                <el-option
                  v-for="host in hosts"
                  :key="host.id"
                  :label="`${host.name}（${host.address}）`"
                  :value="host.id"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="路径">
              <el-input v-model="viewForm.path" style="width: 260px" placeholder="/var/log/messages" />
            </el-form-item>
            <el-form-item label="关键字">
              <el-input
                v-model="viewForm.keyword"
                style="width: 180px"
                placeholder="留空则看末尾 N 行"
                @keyup.enter="doView"
              />
            </el-form-item>
            <el-form-item label="行数">
              <el-input-number v-model="viewForm.lines" :min="1" :max="meta?.viewMaxLines ?? 2000" />
            </el-form-item>
            <el-form-item label="忽略大小写">
              <el-switch v-model="viewForm.ignoreCase" />
            </el-form-item>
            <el-form-item>
              <el-button v-perm="'hostlog:view'" type="primary" :loading="viewLoading" @click="doView">
                查看
              </el-button>
              <el-button v-if="viewResult" v-perm="'hostlog:manage'" @click="watchCurrent">
                加为监控点
              </el-button>
            </el-form-item>
          </el-form>

          <template v-if="viewResult">
            <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
              <el-tag type="info">{{ viewResult.hostName }}</el-tag>
              <el-tag type="info">{{ viewResult.path }}</el-tag>
              <el-tag>{{ viewResult.mode === 'grep' ? 'grep 过滤' : '末尾 N 行' }}</el-tag>
              <el-tag type="info">{{ viewResult.lines }} 行</el-tag>
              <el-tag type="info">文件 {{ bytes(viewResult.fileSize) }}</el-tag>
              <el-tag type="info">{{ viewResult.costMs }} ms</el-tag>
              <el-tag v-if="viewResult.truncated" type="warning">输出已截断（超过单次上限）</el-tag>
              <el-tag v-if="viewResult.precheck === 'warn'" type="warning">命令规则告警</el-tag>
            </div>
            <el-alert
              v-if="viewResult.precheckHits.length"
              type="warning"
              :closable="false"
              style="margin-bottom: 8px"
            >
              <template #title>
                命令规则命中：{{ viewResult.precheckHits.map((x) => x.description).join('；') }}
              </template>
            </el-alert>
            <pre class="log-pane">{{ viewResult.rows.join('\n') || '（没有内容）' }}</pre>
          </template>
          <el-empty v-else description="选好主机与路径后点「查看」" />
        </el-tab-pane>

        <!-- ---------- 日志监控点 ---------- -->
        <el-tab-pane label="日志监控" name="targets">
          <el-alert type="info" :closable="false" style="margin-bottom: 12px">
            <template #title>
              巡检按 <strong>inode + 字节偏移</strong>增量读取：只看上次之后的新增内容，
              文件被轮转或清空时水位自动归零。<strong>首次巡检只记录水位、不回溯历史日志</strong> ——
              一个跑了半年的日志里成千上万条 ERROR 不是刚出的问题。
              关键字是<strong>固定字符串、不区分大小写</strong>（不是正则）；先判忽略词再判关键字。
              「读不到文件」也会告警：一个本该一直在写的日志突然消失，等于这个监控点已经瞎了。
            </template>
          </el-alert>

          <div class="page-toolbar">
            <el-select v-model="targetQuery.status" placeholder="全部状态" clearable style="width: 140px">
              <el-option label="正常" value="ok" />
              <el-option label="命中关键字" value="hit" />
              <el-option label="文件不存在" value="missing" />
              <el-option label="读不了" value="denied" />
              <el-option label="巡检失败" value="failed" />
              <el-option label="未巡检" value="unknown" />
            </el-select>
            <el-input
              v-model="targetQuery.keyword"
              placeholder="名称 / 路径 / 主机"
              clearable
              style="width: 200px"
              @keyup.enter="((targetQuery.page = 1), loadTargets())"
            />
            <el-button @click="((targetQuery.page = 1), loadTargets())">查询</el-button>
            <div class="grow"></div>
            <el-button v-perm="'hostlog:manage'" :loading="scanning" @click="runScanAll">全部巡检</el-button>
            <el-button v-perm="'hostlog:manage'" type="primary" @click="openCreate">新增监控点</el-button>
          </div>

          <el-table v-loading="targetLoading" :data="targets" border stripe empty-text="还没有日志监控点">
            <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
            <el-table-column prop="hostName" label="主机" width="130" show-overflow-tooltip />
            <el-table-column prop="path" label="日志文件" min-width="200" show-overflow-tooltip />
            <el-table-column label="状态" width="130">
              <template #default="{ row }">
                <el-tag size="small" :type="statusMeta[row.lastStatus]?.type || 'info'">
                  {{ statusMeta[row.lastStatus]?.label || row.lastStatus }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="上轮命中" width="100">
              <template #default="{ row }">
                <span v-if="row.lastHitCount">{{ row.lastHitCount }} 行</span>
                <span v-else style="color: #9ca3af">-</span>
              </template>
            </el-table-column>
            <el-table-column prop="keywords" label="关键字" min-width="140" show-overflow-tooltip />
            <el-table-column label="水位" width="130">
              <template #default="{ row }">
                <el-tooltip :content="`inode ${row.lastInode || '-'}，文件 ${bytes(row.lastSize)}`" placement="top">
                  <span>{{ bytes(row.lastOffset) }}</span>
                </el-tooltip>
                <el-tag v-if="row.lastRotated" size="small" type="warning" style="margin-left: 4px">轮转</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="样本" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">
                <code v-if="row.lastSample" style="font-size: 12px">{{ row.lastSample }}</code>
                <span v-else style="color: #9ca3af">{{ row.lastError || '-' }}</span>
              </template>
            </el-table-column>
            <el-table-column label="最近巡检" width="160">
              <template #default="{ row }">
                {{ row.lastScanAt ? row.lastScanAt.slice(0, 19).replace('T', ' ') : '未巡检' }}
              </template>
            </el-table-column>
            <el-table-column label="操作" width="200" fixed="right">
              <template #default="{ row }">
                <el-button v-perm="'hostlog:manage'" link type="primary" @click="runScan(row)">巡检</el-button>
                <el-button link type="primary" @click="openHistory(row)">历史</el-button>
                <el-button v-perm="'hostlog:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
                <el-button v-perm="'hostlog:manage'" link type="danger" @click="remove(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-pagination
            v-model:current-page="targetQuery.page"
            v-model:page-size="targetQuery.pageSize"
            :total="targetTotal"
            :page-sizes="[20, 50]"
            layout="total, sizes, prev, pager, next"
            style="margin-top: 12px"
            @change="loadTargets"
          />
        </el-tab-pane>

        <!-- ---------- 磁盘占用 ---------- -->
        <el-tab-pane label="日志占用" name="usage">
          <el-alert type="info" :closable="false" style="margin-bottom: 12px">
            <template #title>
              {{ usageNote || '磁盘被日志写满是最常见的一类故障，而它在主机指标里只表现为根分区使用率上升，看不出是谁写的。' }}
              采集范围是<strong>全部纳管主机</strong>，不只是配了监控点的那些 ——
              恰恰是没人管的机器更容易被日志写满。
            </template>
          </el-alert>

          <div class="page-toolbar">
            <el-tag type="info">目录：{{ usageDir || '/var/log' }}</el-tag>
            <div class="grow"></div>
            <el-button v-perm="'hostlog:manage'" :loading="usageLoading" @click="doCollectUsage">
              立即采集
            </el-button>
          </div>

          <el-table
            v-loading="usageLoading"
            :data="usageRows"
            border
            stripe
            empty-text="还没有采集过日志占用"
          >
            <el-table-column type="expand">
              <template #default="{ row }">
                <div style="padding: 8px 24px">
                  <el-alert v-if="row.topIsDir" type="warning" :closable="false" style="margin-bottom: 8px">
                    <template #title>
                      这台机器的 find 不支持 -printf，已退回 du -a，下面的清单<strong>混有目录</strong>，别照着它去删
                    </template>
                  </el-alert>
                  <el-table :data="row.top" size="small" border>
                    <el-table-column prop="path" label="路径" min-width="300" show-overflow-tooltip />
                    <el-table-column label="占用" width="120">
                      <template #default="scope">{{ kb(scope.row.sizeKb) }}</template>
                    </el-table-column>
                  </el-table>
                  <el-empty v-if="!row.top?.length" description="没有取到明细" :image-size="60" />
                </div>
              </template>
            </el-table-column>
            <el-table-column prop="hostName" label="主机" min-width="140" />
            <el-table-column label="总占用" width="120">
              <template #default="{ row }">{{ kb(row.totalKb) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 'ok' ? 'success' : 'danger'">
                  {{ row.status === 'ok' ? '正常' : '失败' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="errorMsg" label="说明" min-width="200" show-overflow-tooltip />
            <el-table-column label="清单" width="110">
              <template #default="{ row }">
                <el-tag v-if="row.topIsDir" size="small" type="warning">含目录</el-tag>
                <span v-else style="color: #9ca3af">仅文件</span>
              </template>
            </el-table-column>
            <el-table-column label="采集时间" width="160">
              <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
            </el-table-column>
          </el-table>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑日志监控点' : '新增日志监控点'" width="580px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="130px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如 web-01 的 nginx 错误日志" />
        </el-form-item>
        <el-form-item label="主机">
          <el-select v-model="form.hostId" filterable style="width: 100%">
            <el-option
              v-for="host in hosts"
              :key="host.id"
              :label="`${host.name}（${host.address}）`"
              :value="host.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="日志文件" prop="path">
          <el-input v-model="form.path" :placeholder="`必须在 ${prefixHint} 下`" />
        </el-form-item>
        <el-form-item label="关键字">
          <el-input v-model="form.keywords" placeholder="逗号分隔，固定字符串、不区分大小写" />
        </el-form-item>
        <el-form-item label="忽略词">
          <el-input v-model="form.ignoreKeywords" placeholder="命中这些的行直接跳过，用来压掉已知噪音" />
        </el-form-item>
        <el-form-item label="单轮读取上限">
          <el-input-number v-model="form.maxBytes" :min="1024" :step="65536" />
          <el-text type="info" style="margin-left: 8px">字节。日志突然暴涨时靠它兜底</el-text>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="form.alertEnabled" />
          <el-text type="info" style="margin-left: 8px">关掉后仍会巡检，只是不进告警通道</el-text>
        </el-form-item>
        <el-form-item label="参与批量巡检">
          <el-switch v-model="form.enabled" />
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

    <el-drawer v-model="historyVisible" :title="`巡检历史 · ${historyTarget?.name}`" size="700px">
      <el-table :data="historyRows" border stripe size="small" empty-text="还没有巡检记录">
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.label || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="命中" width="80">
          <template #default="{ row }">{{ row.hitCount || '-' }}</template>
        </el-table-column>
        <el-table-column label="新增" width="90">
          <template #default="{ row }">{{ bytes(row.newBytes) }}</template>
        </el-table-column>
        <el-table-column label="轮转" width="70">
          <template #default="{ row }">{{ row.rotated ? '是' : '-' }}</template>
        </el-table-column>
        <el-table-column label="样本 / 说明" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">{{ row.sample || row.errorMsg || '-' }}</template>
        </el-table-column>
        <el-table-column prop="operator" label="触发" width="90" />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>

<style scoped>
.log-pane {
  margin: 0;
  padding: 12px;
  max-height: 560px;
  overflow: auto;
  background: #1e1e1e;
  color: #d4d4d4;
  border-radius: 4px;
  font-family: Consolas, Monaco, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
