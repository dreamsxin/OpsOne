<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getAgentRun,
  listAgentConfigs,
  listAgentRuns,
  type AgentConfig,
  type AgentDataSource,
  type AgentRun
} from '@/api'

const loading = ref(false)
const rows = ref<AgentRun[]>([])
const total = ref(0)
const agents = ref<AgentConfig[]>([])
const dataSources = ref<AgentDataSource[]>([])

const query = reactive({
  page: 1,
  pageSize: 20,
  agentId: '' as number | '',
  status: '',
  dataSource: '',
  username: '',
  start: '',
  end: ''
})

const detail = reactive({ visible: false, run: null as AgentRun | null, loading: false })

const sourceLabel = (key: string) =>
  dataSources.value.find((item) => item.key === key)?.label || key

async function load() {
  loading.value = true
  try {
    const data = await listAgentRuns(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

async function openDetail(row: AgentRun) {
  detail.visible = true
  detail.loading = true
  detail.run = row
  try {
    // 列表不带正文，详情单独取
    detail.run = await getAgentRun(row.id)
  } catch (err: any) {
    ElMessage.error(err?.message || '读取详情失败')
  } finally {
    detail.loading = false
  }
}

function money(value: number) {
  if (!value) return '0'
  return value < 0.01 ? value.toFixed(6) : value.toFixed(4)
}

onMounted(async () => {
  try {
    const data = await listAgentConfigs()
    agents.value = data.list || []
    dataSources.value = data.dataSources || []
  } catch {
    // 拿不到 Agent 列表只影响筛选下拉，不影响看记录
  }
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" show-icon class="notice">
        每次运行都记一条：喂进去多少条平台数据、模型给了什么结论、花了多少 token 与钱。
        <strong>结论是模型说的，不是平台的判断</strong>，动手前请自行核对。
        Agent 的调用同样计入「用量与成本」。
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.agentId" placeholder="全部 Agent" clearable style="width: 170px" @change="search">
          <el-option v-for="item in agents" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="query.status" placeholder="全部状态" clearable style="width: 130px" @change="search">
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
        </el-select>
        <el-select v-model="query.dataSource" placeholder="全部来源" clearable style="width: 160px" @change="search">
          <el-option v-for="item in dataSources" :key="item.key" :label="item.label" :value="item.key" />
        </el-select>
        <el-input v-model="query.username" placeholder="运行人" clearable style="width: 130px" @keyup.enter="search" />
        <el-date-picker
          v-model="query.start"
          type="date"
          value-format="YYYY-MM-DD"
          placeholder="开始日期"
          style="width: 150px"
          @change="search"
        />
        <el-date-picker
          v-model="query.end"
          type="date"
          value-format="YYYY-MM-DD"
          placeholder="结束日期"
          style="width: 150px"
          @change="search"
        />
        <el-button @click="search">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" style="width: 100%">
        <el-table-column label="时间" width="170" prop="createdAt" />
        <el-table-column label="Agent" min-width="150">
          <template #default="{ row }">
            <div>{{ row.agentName }}</div>
            <div class="sub">{{ row.alias }}</div>
          </template>
        </el-table-column>
        <el-table-column label="上下文" min-width="170">
          <template #default="{ row }">
            <div>{{ sourceLabel(row.dataSource) }}<span v-if="row.targetId"> #{{ row.targetId }}</span></div>
            <div class="sub">
              {{ row.contextItems }} 条 / {{ row.contextChars }} 字符
              <span v-if="row.contextTruncated" class="warn">（已截断）</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="运行人" width="110" prop="username" />
        <el-table-column label="token" width="110">
          <template #default="{ row }">{{ row.promptTokens }} / {{ row.completionTokens }}</template>
        </el-table-column>
        <el-table-column label="成本(元)" width="110">
          <template #default="{ row }">{{ money(row.cost) }}</template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ row.latencyMs }} ms</template>
        </el-table-column>
        <el-table-column label="结果" min-width="200">
          <template #default="{ row }">
            <el-tag :type="row.status === 'success' ? 'success' : 'danger'" size="small">
              {{ row.status === 'success' ? '成功' : '失败' }}
            </el-tag>
            <div v-if="row.errorMsg" class="sub danger">{{ row.errorMsg }}</div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :disabled="row.status !== 'success'" @click="openDetail(row)">
              看结论
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-model:current-page="query.page"
        class="page-pager"
        layout="total, prev, pager, next"
        :total="total"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>

    <el-drawer v-model="detail.visible" :title="detail.run?.agentName || '运行详情'" size="60%">
      <div v-loading="detail.loading">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="时间">{{ detail.run?.createdAt }}</el-descriptions-item>
          <el-descriptions-item label="运行人">{{ detail.run?.username || '-' }}</el-descriptions-item>
          <el-descriptions-item label="模型">{{ detail.run?.alias }}</el-descriptions-item>
          <el-descriptions-item label="上下文">
            {{ sourceLabel(detail.run?.dataSource || '') }}
            <span v-if="detail.run?.targetId"> #{{ detail.run?.targetId }}</span>
            · {{ detail.run?.contextItems }} 条 / {{ detail.run?.contextChars }} 字符
          </el-descriptions-item>
          <el-descriptions-item label="token">
            {{ detail.run?.promptTokens }} / {{ detail.run?.completionTokens }}
          </el-descriptions-item>
          <el-descriptions-item label="成本 / 耗时">
            约 {{ money(detail.run?.cost || 0) }} 元 · {{ detail.run?.latencyMs }} ms
          </el-descriptions-item>
        </el-descriptions>

        <div v-if="detail.run?.input" class="block">
          <div class="block-title">运行时的补充说明</div>
          <pre class="block-body">{{ detail.run?.input }}</pre>
        </div>
        <div class="block">
          <div class="block-title">模型给出的结论</div>
          <pre class="block-body">{{ detail.run?.output || '（空）' }}</pre>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
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
.block {
  margin-top: 14px;
}
.block-title {
  font-weight: 600;
  margin-bottom: 6px;
}
.block-body {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  background: var(--el-fill-color-light);
  padding: 10px;
  border-radius: 4px;
  max-height: 460px;
  overflow: auto;
}
</style>
