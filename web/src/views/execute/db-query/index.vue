<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  checkDBQueryStatement,
  describeDBTable,
  exportDBQueryCSV,
  listDBQueryLogs,
  listDBSchemas,
  listDBTables,
  listDatabases,
  runDBQuery,
  type DBColumnItem,
  type DBIndexItem,
  type DBQueryLog,
  type DBQueryResult,
  type DBSchemaItem,
  type DBTableItem,
  type DBInstance
} from '@/api'

const instances = ref<DBInstance[]>([])
const instanceId = ref<number | null>(null)
const schemas = ref<DBSchemaItem[]>([])
const schema = ref('')
const tables = ref<DBTableItem[]>([])
const tableFilter = ref('')
const loadingMeta = ref(false)
// 连不上/没凭据时把原因留在面板里：toast 会消失，而「为什么连不上」是这个页面最常见的问题
const metaError = ref('')


const statement = ref('select 1')
const running = ref(false)
const result = ref<DBQueryResult | null>(null)
const precheck = ref<{ allowed: boolean; reason?: string; statement?: string } | null>(null)

const activeTab = ref('result')
const structure = reactive({
  table: '',
  columns: [] as DBColumnItem[],
  indexes: [] as DBIndexItem[]
})

const logs = ref<DBQueryLog[]>([])
const logFilter = reactive({ status: '', keyword: '' })

const currentInstance = computed(() => instances.value.find((i) => i.id === instanceId.value))
const visibleTables = computed(() => {
  const kw = tableFilter.value.trim().toLowerCase()
  if (!kw) return tables.value
  return tables.value.filter((t) => t.name.toLowerCase().includes(kw))
})
const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' }> = {
  success: { text: '成功', type: 'success' },
  blocked: { text: '被拦截', type: 'warning' },
  failed: { text: '失败', type: 'danger' }
}

async function loadInstances() {
  const data = await listDatabases({ page: 1, pageSize: 200 })
  // 只有 MySQL / PostgreSQL 能在线查询，其它类型不列出来，免得点进去才发现不支持
  instances.value = (data.list || []).filter((x) => x.type === 'mysql' || x.type === 'postgres')
  if (instances.value.length && instanceId.value === null) {
    instanceId.value = instances.value[0].id
    await loadSchemas()
  }
}

async function loadSchemas() {
  if (!instanceId.value) return
  loadingMeta.value = true
  schemas.value = []
  tables.value = []
  schema.value = ''
  metaError.value = ''
  try {
    const data = await listDBSchemas(instanceId.value)
    schemas.value = data.list || []
    const prefer = currentInstance.value?.dbName
    const hit = schemas.value.find((s) => s.name === prefer) || schemas.value[0]
    if (hit) {
      schema.value = hit.name
      await loadTables()
    }
  } catch (e: any) {
    metaError.value = e?.message || '读取库列表失败'
  } finally {
    loadingMeta.value = false
  }
}

async function loadTables() {
  if (!instanceId.value || !schema.value) return
  loadingMeta.value = true
  try {
    const data = await listDBTables(instanceId.value, schema.value)
    tables.value = data.list || []
    metaError.value = ''
  } catch (e: any) {
    tables.value = []
    metaError.value = e?.message || '读取表列表失败'
  } finally {
    loadingMeta.value = false
  }
}

async function openTable(row: DBTableItem) {
  if (!instanceId.value) return
  const data = await describeDBTable(instanceId.value, schema.value, row.name)
  structure.table = row.name
  structure.columns = data.columns || []
  structure.indexes = data.indexes || []
  activeTab.value = 'structure'
}

function fillSelect(row: DBTableItem) {
  const qualified = currentInstance.value?.type === 'postgres' ? `${schema.value}.${row.name}` : row.name
  statement.value = `select * from ${qualified}`
  precheck.value = null
  activeTab.value = 'result'
}

async function doPrecheck() {
  precheck.value = await checkDBQueryStatement({ statement: statement.value })
  if (precheck.value.allowed) {
    ElMessage.success('预检通过，这是只读语句')
  } else {
    ElMessage.warning(precheck.value.reason || '语句会被拦截')
  }
}

async function run() {
  if (!instanceId.value) {
    ElMessage.warning('先选一个实例')
    return
  }
  running.value = true
  try {
    result.value = await runDBQuery(instanceId.value, {
      schema: schema.value,
      statement: statement.value
    })
    activeTab.value = 'result'
    precheck.value = null
    loadLogs()
  } finally {
    running.value = false
  }
}

async function exportCsv() {
  if (!instanceId.value) return
  const { truncated } = await exportDBQueryCSV(instanceId.value, {
    schema: schema.value,
    statement: statement.value
  })
  ElMessage.success(truncated ? '已导出（达到上限 10000 行，结果被截断）' : '已导出')
  loadLogs()
}

async function loadLogs() {
  const params: Record<string, any> = { page: 1, pageSize: 50 }
  if (logFilter.status) params.status = logFilter.status
  if (logFilter.keyword) params.keyword = logFilter.keyword
  const data = await listDBQueryLogs(params)
  logs.value = data.list || []
}

onMounted(async () => {
  await loadInstances()
  loadLogs()
})
</script>

<template>
  <div class="page">
    <el-alert
      type="warning"
      :closable="false"
      style="margin-bottom: 12px"
      title="只读：语句要过白名单（只允许 SELECT / WITH / SHOW / EXPLAIN / DESCRIBE），连接本身也设成只读会话，两道闸都拦不住的写操作会被数据库自己拒绝。用的是「数据库资产」里登记的账号，所以在库里授予只读权限是第三道保险。每次查询（包括被拦下的）都会记入流水；导出 CSV 属于数据外带，单独留痕。"
    />

    <div class="layout">
      <el-card class="side">
        <div class="side-head">
          <el-select
            v-model="instanceId"
            placeholder="选择实例"
            style="width: 100%"
            @change="loadSchemas"
          >
            <el-option
              v-for="item in instances"
              :key="item.id"
              :label="`${item.name}（${item.type}）`"
              :value="item.id"
            />
          </el-select>
          <el-select v-model="schema" placeholder="选择库 / 模式" style="width: 100%" @change="loadTables">
            <el-option
              v-for="s in schemas"
              :key="s.name"
              :label="`${s.name}（${s.tables} 张表）`"
              :value="s.name"
            />
          </el-select>
          <el-input v-model="tableFilter" placeholder="过滤表名" clearable />
        </div>

        <div v-loading="loadingMeta" class="table-list">
          <div v-if="metaError" class="meta-error">{{ metaError }}</div>
          <div v-else-if="!visibleTables.length" class="empty">没有表</div>
          <div v-for="t in visibleTables" :key="t.name" class="table-item">
            <div class="table-name" :title="t.comment">
              <el-tag size="small" :type="t.kind === 'view' ? 'info' : 'primary'">
                {{ t.kind === 'view' ? '视图' : '表' }}
              </el-tag>
              <span class="name">{{ t.name }}</span>
            </div>
            <div class="table-ops">
              <el-link type="primary" @click="openTable(t)">结构</el-link>
              <el-link type="primary" @click="fillSelect(t)">查询</el-link>
            </div>
          </div>
        </div>
      </el-card>

      <el-card class="main">
        <el-input
          v-model="statement"
          type="textarea"
          :rows="6"
          placeholder="只读 SQL，例如 select * from orders where status = 'paid'"
        />
        <div class="toolbar">
          <el-button :loading="running" type="primary" @click="run">执行</el-button>
          <el-button @click="doPrecheck">预检</el-button>
          <el-button :disabled="!result" @click="exportCsv">导出 CSV</el-button>
          <el-tag v-if="result" type="info">
            {{ result.rows.length }} 行 · {{ result.costMs }} ms
          </el-tag>
          <el-tag v-if="result?.truncated" type="warning">结果被截断，还有更多</el-tag>
        </div>
        <el-alert
          v-if="precheck && !precheck.allowed"
          type="error"
          :closable="false"
          style="margin-top: 8px"
          :title="`这条语句会被拦下：${precheck.reason}`"
        />

        <el-tabs v-model="activeTab" style="margin-top: 8px">
          <el-tab-pane label="查询结果" name="result">
            <div v-if="result?.statement" class="hint">
              实际下发：<code>{{ result.statement }}</code>
              （平台会多取一行用来判断是否还有更多）
            </div>
            <el-table
              v-if="result"
              :data="result.rows"
              border
              stripe
              size="small"
              height="420"
              empty-text="没有数据"
            >
              <el-table-column
                v-for="(col, idx) in result.columns"
                :key="col + idx"
                :label="col"
                min-width="140"
                show-overflow-tooltip
              >
                <template #default="{ row }">{{ row[idx] }}</template>
              </el-table-column>
            </el-table>
            <div v-else class="empty">还没有执行查询</div>
          </el-tab-pane>

          <el-tab-pane :label="structure.table ? `表结构：${structure.table}` : '表结构'" name="structure">
            <el-table :data="structure.columns" border stripe size="small" empty-text="左侧点「结构」查看">
              <el-table-column prop="name" label="列" min-width="140" />
              <el-table-column prop="dataType" label="类型" min-width="140" />
              <el-table-column label="可空" width="70">
                <template #default="{ row }">{{ row.nullable ? '是' : '否' }}</template>
              </el-table-column>
              <el-table-column label="主键" width="70">
                <template #default="{ row }">
                  <el-tag v-if="row.isPk" size="small" type="warning">PK</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="default" label="默认值" min-width="120" show-overflow-tooltip />
              <el-table-column prop="comment" label="注释" min-width="140" show-overflow-tooltip />
            </el-table>
            <div v-if="structure.indexes.length" class="idx">
              <div class="hint">索引</div>
              <el-table :data="structure.indexes" border size="small">
                <el-table-column prop="name" label="索引名" min-width="160" />
                <el-table-column prop="columns" label="列" min-width="180" />
                <el-table-column label="唯一" width="70">
                  <template #default="{ row }">{{ row.unique ? '是' : '否' }}</template>
                </el-table-column>
              </el-table>
            </div>
          </el-tab-pane>

          <el-tab-pane label="查询流水" name="logs">
            <div class="toolbar">
              <el-select v-model="logFilter.status" placeholder="全部状态" clearable style="width: 140px" @change="loadLogs">
                <el-option label="成功" value="success" />
                <el-option label="被拦截" value="blocked" />
                <el-option label="失败" value="failed" />
              </el-select>
              <el-input
                v-model="logFilter.keyword"
                placeholder="语句关键字"
                clearable
                style="width: 200px"
                @keyup.enter="loadLogs"
              />
              <el-button @click="loadLogs">查询</el-button>
            </div>
            <el-table :data="logs" border stripe size="small" height="380" empty-text="还没有查询记录">
              <el-table-column prop="createdAt" label="时间" width="180">
                <template #default="{ row }">{{ row.createdAt?.slice(0, 19).replace('T', ' ') }}</template>
              </el-table-column>
              <el-table-column prop="username" label="操作人" width="100" />
              <el-table-column prop="instanceName" label="实例" width="130" />
              <el-table-column label="状态" width="90">
                <template #default="{ row }">
                  <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
                    {{ statusMeta[row.status]?.text || row.status }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="statement" label="语句" min-width="260" show-overflow-tooltip />
              <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
              <el-table-column label="行/耗时" width="110">
                <template #default="{ row }">{{ row.rows }} / {{ row.costMs }}ms</template>
              </el-table-column>
              <el-table-column label="导出" width="70">
                <template #default="{ row }">
                  <el-tag v-if="row.exported" size="small" type="warning">是</el-tag>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>
        </el-tabs>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.side {
  width: 320px;
  flex: none;
}
.main {
  flex: 1;
  min-width: 0;
}
.side-head {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 8px;
}
.table-list {
  max-height: 560px;
  overflow: auto;
}
.table-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 4px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.table-name {
  display: flex;
  gap: 6px;
  align-items: center;
  min-width: 0;
}
.table-name .name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.table-ops {
  display: flex;
  gap: 8px;
  flex: none;
}
.toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 8px;
}
.hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 6px;
}
.idx {
  margin-top: 12px;
}
.empty {
  padding: 24px;
  text-align: center;
  color: var(--el-text-color-secondary);
}

.meta-error {
  padding: 16px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-color-danger);
  word-break: break-all;
}

</style>
