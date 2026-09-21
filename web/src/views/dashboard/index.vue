<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getAlertStats, getDashboardStats, listAuditLogs, type AlertStats, type AuditLog } from '@/api'
import { useUserStore } from '@/stores/user'
import DashboardGreeting from '@/components/DashboardGreeting.vue'

interface Stats {
  hostTotal: number
  online: number
  offline: number
  userTotal: number
  jobTotal: number
  envStats: { env: string; count: number }[] | null
  recentJobs: { id: number; name: string; operator: string; total: number; successNum: number; failedNum: number; startedAt: string }[]
}

const store = useUserStore()
const router = useRouter()
const stats = ref<Stats | null>(null)
const alertStats = ref<AlertStats | null>(null)
const activities = ref<AuditLog[]>([])
const loading = ref(false)
const updatedAt = ref<Date | null>(null)

const envLabel: Record<string, string> = { dev: '开发', test: '测试', prod: '生产' }

async function load() {
  loading.value = true
  try {
    // 三个数据源互不依赖：平台活动流与告警态势拉挂了也不影响主 KPI
    const [main, alerts, audit] = await Promise.all([
      getDashboardStats(),
      getAlertStats().catch(() => null),
      listAuditLogs({ page: 1, pageSize: 8 }).catch(() => null)
    ])
    stats.value = main
    alertStats.value = alerts
    activities.value = audit?.list || []
    updatedAt.value = new Date()
  } finally {
    loading.value = false
  }
}

/** 顶部一句话总结：按当前最紧急的指标拼一句人话，不做过度修辞 */
const summary = computed(() => {
  const s = stats.value
  if (!s) return '正在加载态势…'
  const bits: string[] = []
  if (s.offline > 0) bits.push(`${s.offline} 台主机离线`)
  if (s.jobTotal > 0) bits.push(`累计执行 ${s.jobTotal} 次`)
  if (!bits.length) bits.push('系统运行平稳')
  return bits.join(' · ')
})

const roleNames = computed(() => (store.profile?.roles || []).map((r) => r.name))

/** 主机成功率：给「纳管主机」卡片当副标题，让数字有上下文而不是干瘪 */
const onlineRate = computed(() => {
  const s = stats.value
  if (!s || !s.hostTotal) return '暂无主机'
  const rate = Math.round((s.online / s.hostTotal) * 100)
  return `在线率 ${rate}% · 离线 ${s.offline}`
})

const envSummary = computed(() => {
  const list = stats.value?.envStats || []
  if (!list.length) return '暂无环境分布'
  return list
    .map((x) => `${envLabel[x.env] || x.env} ${x.count}`)
    .join(' · ')
})

const userSummary = computed(() => {
  const n = stats.value?.userTotal ?? 0
  return n ? `累计 ${n} 位成员` : '暂无用户'
})

const jobSummary = computed(() => {
  const jobs = stats.value?.recentJobs || []
  if (!jobs.length) return '最近无执行'
  const last = jobs[0]
  const okRate = last.total ? Math.round((last.successNum / last.total) * 100) : 0
  return `最近 ${last.name} · 成功 ${okRate}%`
})

function gotoHosts(filter?: 'offline') {
  if (filter === 'offline') {
    router.push({ path: '/asset/host', query: { status: 'offline' } })
  } else {
    router.push('/asset/host')
  }
}

/**
 * 深链到告警列表。status / severity 两个维度一律写全（不需要的传空串），
 * 让目标页的筛选完全由 URL 决定，不会和它上次残留的条件叠加。
 * 严重/警告两格统计的是「未恢复」的，所以带上 status=unresolved，
 * 否则点进去会把历史上已恢复的也列出来，数字对不上。
 */
function gotoAlerts(q: Record<string, string>) {
  router.push({
    path: '/monitor/alerts',
    query: { status: q.status ?? '', severity: q.severity ?? '' }
  })
}

/** 活动流里的方法做成彩色小标签，扫一眼就知道是读还是写 */
const methodTone: Record<string, string> = {
  GET: 'info',
  POST: 'success',
  PUT: 'warning',
  DELETE: 'danger'
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading && !stats">
    <DashboardGreeting
      :name="store.displayName || '运维伙伴'"
      :roles="roleNames"
      :summary="summary"
      :updated-at="updatedAt"
      :loading="loading"
      @refresh="load"
    />

    <el-row :gutter="12">
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="gotoHosts()">
          <div class="value">{{ stats?.hostTotal ?? 0 }}</div>
          <div class="label">纳管主机</div>
          <div class="sub">{{ onlineRate }}</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="gotoHosts()">
          <div class="value" style="color: #16a34a">{{ stats?.online ?? 0 }}</div>
          <div class="label">在线</div>
          <div class="sub">{{ envSummary }}</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="gotoHosts('offline')">
          <div class="value" style="color: #dc2626">{{ stats?.offline ?? 0 }}</div>
          <div class="label">离线</div>
          <div class="sub">{{ (stats?.offline ?? 0) > 0 ? '点击查看离线主机' : '当前全部在线' }}</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="router.push('/execute/batch')">
          <div class="value">{{ stats?.jobTotal ?? 0 }}</div>
          <div class="label">执行作业</div>
          <div class="sub">{{ jobSummary }}</div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="8">
        <el-card>
          <template #header>
            <div style="display: flex; align-items: center">
              <span>告警态势</span>
              <el-button link type="primary" style="margin-left: auto" @click="gotoAlerts({})">
                告警池
              </el-button>
            </div>
          </template>
          <div class="alert-grid">
            <div class="alert-cell" style="cursor: pointer" @click="gotoAlerts({ status: 'firing' })">
              <div class="alert-cell__value danger">{{ alertStats?.firing ?? 0 }}</div>
              <div class="alert-cell__label">触发中</div>
            </div>
            <div class="alert-cell" style="cursor: pointer" @click="gotoAlerts({ status: 'acked' })">
              <div class="alert-cell__value warning">{{ alertStats?.acked ?? 0 }}</div>
              <div class="alert-cell__label">已确认</div>
            </div>
            <div class="alert-cell" style="cursor: pointer" @click="gotoAlerts({ status: 'resolved' })">
              <div class="alert-cell__value success">{{ alertStats?.resolved ?? 0 }}</div>
              <div class="alert-cell__label">已恢复</div>
            </div>
            <div
              class="alert-cell"
              style="cursor: pointer"
              @click="gotoAlerts({ severity: 'critical', status: 'unresolved' })"
            >
              <div class="alert-cell__value danger">{{ alertStats?.critical ?? 0 }}</div>
              <div class="alert-cell__label">严重</div>
            </div>
            <div
              class="alert-cell"
              style="cursor: pointer"
              @click="gotoAlerts({ severity: 'warning', status: 'unresolved' })"
            >
              <div class="alert-cell__value warning">{{ alertStats?.warning ?? 0 }}</div>
              <div class="alert-cell__label">警告</div>
            </div>
            <div class="alert-cell">
              <div class="alert-cell__value">{{ alertStats?.total ?? 0 }}</div>
              <div class="alert-cell__label">累计</div>
            </div>
          </div>
          <div class="sub-note">点击任意一格按对应条件进入告警池</div>
        </el-card>
      </el-col>
      <el-col :span="16">
        <el-card>
          <template #header>
            <div style="display: flex; align-items: center">
              <span>平台活动流</span>
              <el-button link type="primary" style="margin-left: auto" @click="router.push('/system/audit')">
                审计检索
              </el-button>
            </div>
          </template>
          <el-table :data="activities" size="small" empty-text="暂无操作记录">
            <el-table-column prop="createdAt" label="时间" width="170" />
            <el-table-column prop="username" label="操作人" width="100" />
            <el-table-column label="方法" width="70">
              <template #default="{ row }">
                <el-tag size="small" :type="(methodTone[row.method] as any) || 'info'">
                  {{ row.method }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="action" label="动作" min-width="140" show-overflow-tooltip />
            <el-table-column prop="path" label="对象" min-width="200" show-overflow-tooltip />
            <el-table-column label="结果" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">
                  {{ row.status }}
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="12" style="margin-top: 12px">
      <el-col :span="8">
        <el-card header="环境分布">
          <el-empty v-if="!stats?.envStats?.length" description="暂无主机" :image-size="60" />
          <div v-else>
            <div
              v-for="item in stats.envStats"
              :key="item.env"
              style="display: flex; justify-content: space-between; padding: 6px 0"
            >
              <span>{{ envLabel[item.env] || item.env }}</span>
              <el-tag size="small">{{ item.count }}</el-tag>
            </div>
          </div>
          <div class="sub-note">{{ userSummary }}</div>
        </el-card>
      </el-col>
      <el-col :span="16">
        <el-card>
          <template #header>
            <div style="display: flex; align-items: center">
              <span>最近执行</span>
              <el-button link type="primary" style="margin-left: auto" @click="router.push('/execute/batch')">
                查看全部
              </el-button>
            </div>
          </template>
          <el-table :data="stats?.recentJobs || []" size="small" empty-text="暂无执行记录">
            <el-table-column prop="name" label="作业" min-width="120" show-overflow-tooltip />
            <el-table-column prop="operator" label="操作人" width="110" />
            <el-table-column prop="total" label="主机数" width="80" />
            <el-table-column label="结果" width="160">
              <template #default="{ row }">
                <el-tag type="success" size="small">成功 {{ row.successNum }}</el-tag>
                <el-tag
                  v-if="row.failedNum > 0"
                  type="danger"
                  size="small"
                  style="margin-left: 4px"
                >
                  失败 {{ row.failedNum }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="startedAt" label="开始时间" min-width="180" />
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.alert-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 4px;
}
.alert-cell {
  padding: 10px 4px;
  border-radius: 6px;
  text-align: center;
  transition: background-color 0.15s ease;
}
.alert-cell:hover {
  background: var(--el-fill-color-light);
}
.alert-cell__value {
  font-size: 22px;
  font-weight: 600;
  line-height: 1.3;
}
.alert-cell__value.danger {
  color: var(--el-color-danger);
}
.alert-cell__value.warning {
  color: var(--el-color-warning);
}
.alert-cell__value.success {
  color: var(--el-color-success);
}
.alert-cell__label {
  color: var(--ops-text-secondary);
  font-size: 12px;
}
.sub-note {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px dashed var(--ops-border);
  color: var(--ops-text-muted);
  font-size: 12px;
}
</style>
