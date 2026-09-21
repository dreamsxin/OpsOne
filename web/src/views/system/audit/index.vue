<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  exportAuditLogsCSV,
  listAuditLogs,
  listAuditOperators,
  type AuditLog
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'
import FilterChips, { type ChipItem } from '@/components/FilterChips.vue'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const exporting = ref(false)
const rows = ref<AuditLog[]>([])
const total = ref(0)
const failed = ref(0)
const operators = ref<string[]>([])
const range = ref<[string, string] | null>(null)

const query = reactive({
  page: 1,
  pageSize: 20,
  username: '',
  method: '',
  keyword: '',
  result: '',
  ip: ''
})

/** 检索条件（不含分页），列表与导出共用 */
const filters = computed(() => ({
  username: query.username,
  method: query.method,
  keyword: query.keyword,
  result: query.result,
  ip: query.ip,
  start: range.value?.[0] || '',
  end: range.value?.[1] || ''
}))

async function load() {
  loading.value = true
  try {
    const data = await listAuditLogs({
      page: query.page,
      pageSize: query.pageSize,
      ...filters.value
    })
    rows.value = data.list || []
    total.value = data.total
    failed.value = data.failed
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

function reset() {
  query.username = ''
  query.method = ''
  query.keyword = ''
  query.result = ''
  query.ip = ''
  range.value = null
  search()
}

/** 只看失败：排查时最常用的一步，单独给个入口 */
function onlyFailed() {
  query.result = 'fail'
  search()
}

/** 快筛 chips：失败有后端计数，写操作类型做无计数的快捷入口 */
const chips = computed<ChipItem[]>(() => [
  { key: 'info', label: `当前 ${total.value} 条 · 本页 ${rows.value.length}`, static: true },
  { key: 'result:fail', label: '失败', count: failed.value, hint: '点击只看失败', tone: 'danger' },
  { key: 'method:POST', label: '新建', hint: 'POST' },
  { key: 'method:PUT', label: '修改', hint: 'PUT / PATCH' },
  { key: 'method:DELETE', label: '删除', hint: 'DELETE', tone: 'danger' }
])

const activeChipKey = computed<string | null>(() => {
  if (query.result === 'fail') return 'result:fail'
  if (query.method === 'POST') return 'method:POST'
  if (query.method === 'PUT') return 'method:PUT'
  if (query.method === 'DELETE') return 'method:DELETE'
  return null
})

function onChipSelect(key: string) {
  if (key === 'info') return
  const wasActive = activeChipKey.value === key
  query.result = ''
  query.method = ''
  if (!wasActive) {
    const [field, value] = key.split(':')
    if (field === 'result') query.result = value
    else query.method = value
  }
  search()
}

/** 高级筛选（来源 IP / 时间范围）生效时给按钮打点 */
const advancedActive = computed(() => [query.ip, range.value?.[0] || ''].filter(Boolean).length)
const showAdvanced = ref(false)

async function exportCSV() {
  exporting.value = true
  try {
    await exportAuditLogsCSV(filters.value)
    ElMessage.success('已导出当前检索条件下的记录（最多 10000 条）')
  } catch {
    ElMessage.error('导出失败')
  } finally {
    exporting.value = false
  }
}

const methodType: Record<string, 'success' | 'warning' | 'danger'> = {
  POST: 'success',
  PUT: 'warning',
  PATCH: 'warning',
  DELETE: 'danger'
}

onMounted(async () => {
  await load()
  try {
    operators.value = await listAuditOperators()
  } catch {
    // 操作人下拉拿不到不影响检索，手输同样能查
  }
})
</script>

<template>
  <div class="page">
    <PageHeader title="操作审计" subtitle="只记录写操作（POST / PUT / DELETE），读请求不入库以控制数据量">
      <template #actions>
        <el-button :loading="exporting" @click="exportCSV">导出 CSV</el-button>
      </template>
    </PageHeader>

    <el-card>
      <FilterChips :items="chips" :model-value="activeChipKey" @select="onChipSelect" />

      <div class="page-toolbar" style="flex-wrap: wrap; margin-top: 12px">
        <el-select
          v-model="query.username"
          placeholder="操作人"
          style="width: 150px"
          clearable
          filterable
          allow-create
          default-first-option
        >
          <el-option v-for="name in operators" :key="name" :label="name" :value="name" />
        </el-select>
        <el-select v-model="query.method" placeholder="方法" style="width: 120px" clearable>
          <el-option label="POST 新建" value="POST" />
          <el-option label="PUT 修改" value="PUT" />
          <el-option label="DELETE 删除" value="DELETE" />
          <el-option label="PATCH 局部修改" value="PATCH" />
        </el-select>
        <el-select v-model="query.result" placeholder="结果" style="width: 120px" clearable>
          <el-option label="成功" value="success" />
          <el-option label="失败" value="fail" />
        </el-select>
        <el-input
          v-model="query.keyword"
          placeholder="接口关键字，如 /hosts/3"
          style="width: 220px"
          clearable
          @keyup.enter="search"
        />
        <el-button type="primary" @click="search">查询</el-button>
        <el-button @click="reset">重置</el-button>
        <el-button link type="primary" @click="showAdvanced = !showAdvanced">
          高级筛选
          <el-badge v-if="advancedActive" :value="advancedActive" style="margin-left: 4px" />
          <el-icon style="margin-left: 4px">
            <ArrowUp v-if="showAdvanced" />
            <ArrowDown v-else />
          </el-icon>
        </el-button>
        <div class="grow"></div>
      </div>

      <el-collapse-transition>
        <div v-show="showAdvanced" class="advanced-inline">
          <el-input
            v-model="query.ip"
            placeholder="来源 IP"
            style="width: 150px"
            clearable
            @keyup.enter="search"
          />
          <el-date-picker
            v-model="range"
            type="datetimerange"
            value-format="YYYY-MM-DD HH:mm:ss"
            start-placeholder="开始时间"
            end-placeholder="结束时间"
            style="width: 360px"
          />
        </div>
      </el-collapse-transition>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span>命中 {{ total }} 条</span>
          <span v-if="failed > 0"
            >，其中失败
            <el-link type="danger" underline="never" @click="onlyFailed">{{ failed }}</el-link>
            条</span
          >
          <span
            >。只记录写操作（POST / PUT / DELETE），读请求不入库以控制数据量；接口关键字同时匹配实际路径与路由模板，查某台主机身上发生的事可以直接搜
            <code>/hosts/3</code>。导出按当前检索条件走，单次最多 10000 条。保留天数见「系统管理 →
            数据留存」。</span
          >
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="username" label="操作人" width="120" />
        <el-table-column label="方法" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="methodType[row.method] || 'info'">{{ row.method }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="path" label="接口" min-width="200" show-overflow-tooltip />
        <el-table-column prop="action" label="路由模板" min-width="180" show-overflow-tooltip />
        <el-table-column label="状态码" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="ip" label="来源 IP" width="140" />
        <el-table-column prop="costMs" label="耗时(ms)" width="100" />
        <el-table-column prop="createdAt" label="时间" min-width="180" />
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>
  </div>
</template>

<style scoped>
.advanced-inline {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  padding: 8px 12px;
  margin-bottom: 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
}
</style>
