<script setup lang="ts">
/**
 * 容量与配额。
 *
 * 这一页要回答「还能不能再塞」，而它最容易被做成一个误导人的页面，因为
 * **「已分配」和「实际用量」是两件事**：
 *   - 已分配（requests）决定调度器还愿不愿意往这台机器放 Pod
 *   - 实际用量是容器真正在烧的 CPU / 内存，要 metrics-server 才有
 * 只给其中一个、或把两者混在一栏里，都会让人得出错误结论（「用量才 20%，为什么调度不上去」）。
 * 所以两个都给、分栏摆、并且 metrics 缺失时明说缺，不用 0 充数。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getKubeCapacity,
  listKubeClusters,
  listKubeNamespaces,
  type KubeCluster,
  type KubeNamespaceCapacity,
  type KubeNodeCapacity
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'

const clusters = ref<KubeCluster[]>([])
const namespaces = ref<string[]>([])
const clusterId = ref(0)
const namespace = ref('')
const loading = ref(false)

const nodes = ref<KubeNodeCapacity[]>([])
const nsRows = ref<KubeNamespaceCapacity[]>([])
const metricsAvailable = ref(false)
const metricsNote = ref('')
const note = ref('')
const podsCounted = ref(0)
const podsSkipped = ref(0)

const currentCluster = computed(() => clusters.value.find((c) => c.id === clusterId.value))
// 配额是可选的，集群里没配就不必占一块地方。
// 没有配额的命名空间后端给的是 null，这里要兜住，否则整张表会因为一次 map 报错而空着
const quotaRows = computed(() =>
  nsRows.value.flatMap((ns) => (ns.quotas || []).map((q) => ({ namespace: ns.namespace, ...q })))
)


async function loadClusters() {
  clusters.value = await listKubeClusters()
  const usable = clusters.value.find((c) => c.status === 'healthy' || c.status === 'degraded')
  clusterId.value = (usable || clusters.value[0])?.id || 0
}

async function loadNamespaces() {
  namespaces.value = []
  if (!clusterId.value) return
  try {
    const list = await listKubeNamespaces(clusterId.value)
    namespaces.value = list.map((n) => n.name)
  } catch {
    // 读不到命名空间（多为 RBAC 不足）不影响按全部统计
  }
}

async function load() {
  if (!clusterId.value) return
  loading.value = true
  try {
    const res = await getKubeCapacity(clusterId.value, namespace.value || undefined)
    nodes.value = res.nodes || []
    nsRows.value = res.namespaces || []
    metricsAvailable.value = res.metricsAvailable
    metricsNote.value = res.metricsNote
    note.value = res.note
    podsCounted.value = res.podsCounted
    podsSkipped.value = res.podsSkipped
  } catch (err: any) {
    nodes.value = []
    nsRows.value = []
    ElMessage.error(err?.message || '统计容量失败')
  } finally {
    loading.value = false
  }
}

/** 占比的颜色：85% 以上算紧，70% 以上提醒 */
function pctType(value: number): 'success' | 'warning' | 'danger' {
  if (value >= 85) return 'danger'
  if (value >= 70) return 'warning'
  return 'success'
}

watch(clusterId, async () => {
  namespace.value = ''
  await loadNamespaces()
  load()
})
watch(namespace, load)

onMounted(async () => {
  await loadClusters()
  if (clusterId.value) {
    await loadNamespaces()
    await load()
  }
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="容量与配额"
      subtitle="已分配（requests）决定还能不能调度上去，实际用量决定会不会 OOM —— 这是两个数，分开看"
    >
      <template #actions>
        <el-button :loading="loading" @click="load">刷新</el-button>
      </template>
    </PageHeader>

    <el-card shadow="never">
      <div class="page-toolbar">
        <el-select v-model="clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select
          v-model="namespace"
          placeholder="全部命名空间"
          clearable
          filterable
          style="width: 200px"
        >
          <el-option v-for="name in namespaces" :key="name" :label="name" :value="name" />
        </el-select>
        <span class="tip">
          统计了 {{ podsCounted }} 个活着的 Pod<span v-if="podsSkipped">
            ，跳过 {{ podsSkipped }} 个已结束的（它们已经把资源还回去了）</span
          >
        </span>
      </div>

      <el-empty v-if="!clusterId" description="还没有接入集群，先去「集群接入」加一个" />
      <template v-else>
        <el-alert
          v-if="currentCluster && currentCluster.status !== 'healthy'"
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`集群当前状态为「${currentCluster.status}」：${currentCluster.lastError || '请先在集群接入页做一次检查'}`"
        />
        <el-alert
          :type="metricsAvailable ? 'info' : 'warning'"
          :closable="false"
          show-icon
          style="margin-bottom: 12px"
          :title="metricsNote"
        />

        <div class="sub-title">节点</div>
        <el-table v-loading="loading" :data="nodes" border stripe>
          <el-table-column label="节点" min-width="180">
            <template #default="{ row }">
              {{ row.name }}
              <div class="muted">
                {{ row.roles || '—' }}
                <el-tag v-if="!row.ready" size="small" type="danger" style="margin-left: 4px">NotReady</el-tag>
                <el-tag v-if="!row.schedulable" size="small" type="warning" style="margin-left: 4px">
                  已封锁
                </el-tag>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="Pod" width="100" align="center">
            <template #default="{ row }">{{ row.podCount }} / {{ row.podCapacity || '—' }}</template>
          </el-table-column>
          <el-table-column label="CPU 已分配 / 可分配" min-width="210">
            <template #default="{ row }">
              <div>{{ row.cpuRequests }} / {{ row.cpuAllocatable }}</div>
              <el-progress
                :percentage="Math.min(row.cpuRequestPct, 100)"
                :status="pctType(row.cpuRequestPct) === 'success' ? undefined : pctType(row.cpuRequestPct)"
                :stroke-width="10"
              />
              <div class="muted">limits {{ row.cpuLimits }}（{{ row.cpuLimitPct }}%）</div>
            </template>
          </el-table-column>
          <el-table-column label="CPU 实际用量" min-width="170">
            <template #default="{ row }">
              <template v-if="row.hasUsage">
                <div>{{ row.cpuUsage }}（{{ row.cpuUsagePct }}%）</div>
                <el-progress
                  :percentage="Math.min(row.cpuUsagePct, 100)"
                  :status="pctType(row.cpuUsagePct) === 'success' ? undefined : pctType(row.cpuUsagePct)"
                  :stroke-width="10"
                />
              </template>
              <span v-else class="muted">未知（没有 metrics）</span>
            </template>
          </el-table-column>
          <el-table-column label="内存 已分配 / 可分配" min-width="210">
            <template #default="{ row }">
              <div>{{ row.memRequests }} / {{ row.memAllocatable }}</div>
              <el-progress
                :percentage="Math.min(row.memRequestPct, 100)"
                :status="pctType(row.memRequestPct) === 'success' ? undefined : pctType(row.memRequestPct)"
                :stroke-width="10"
              />
              <div class="muted">limits {{ row.memLimits }}（{{ row.memLimitPct }}%）</div>
            </template>
          </el-table-column>
          <el-table-column label="内存 实际用量" min-width="170">
            <template #default="{ row }">
              <template v-if="row.hasUsage">
                <div>{{ row.memUsage }}（{{ row.memUsagePct }}%）</div>
                <el-progress
                  :percentage="Math.min(row.memUsagePct, 100)"
                  :status="pctType(row.memUsagePct) === 'success' ? undefined : pctType(row.memUsagePct)"
                  :stroke-width="10"
                />
              </template>
              <span v-else class="muted">未知（没有 metrics）</span>
            </template>
          </el-table-column>
        </el-table>

        <div class="sub-title">命名空间</div>
        <el-table v-loading="loading" :data="nsRows" border stripe empty-text="没有统计到 Pod">
          <el-table-column prop="namespace" label="命名空间" min-width="160" />
          <el-table-column prop="podCount" label="Pod" width="80" align="center" />
          <el-table-column label="CPU requests / limits" min-width="180">
            <template #default="{ row }">{{ row.cpuRequests }} / {{ row.cpuLimits }}</template>
          </el-table-column>
          <el-table-column label="内存 requests / limits" min-width="190">
            <template #default="{ row }">{{ row.memRequests }} / {{ row.memLimits }}</template>
          </el-table-column>
          <el-table-column label="实际用量" min-width="170">
            <template #default="{ row }">
              <span v-if="row.hasUsage">{{ row.cpuUsage }} / {{ row.memUsage }}</span>
              <span v-else class="muted">未知</span>
            </template>
          </el-table-column>
          <el-table-column label="没配 limits 的 Pod" width="150" align="center">
            <template #default="{ row }">
              <el-tag v-if="row.noLimitPods" size="small" type="warning">{{ row.noLimitPods }}</el-tag>
              <span v-else class="muted">0</span>
            </template>
          </el-table-column>
        </el-table>

        <div class="sub-title">
          ResourceQuota
          <span class="tip">集群里没配配额时这里是空的 —— 空表示「没有配额限制」，不是「读不到」</span>
        </div>
        <el-table :data="quotaRows" border stripe size="small" empty-text="这个集群没有配 ResourceQuota">
          <el-table-column prop="namespace" label="命名空间" min-width="150" />
          <el-table-column prop="name" label="配额名" min-width="150" />
          <el-table-column prop="resource" label="资源项" min-width="170" />
          <el-table-column label="已用 / 上限" min-width="170">
            <template #default="{ row }">{{ row.used }} / {{ row.hard }}</template>
          </el-table-column>
          <el-table-column label="占比" min-width="150">
            <template #default="{ row }">
              <el-progress
                :percentage="Math.min(row.percent, 100)"
                :status="pctType(row.percent) === 'success' ? undefined : pctType(row.percent)"
                :stroke-width="10"
              />
            </template>
          </el-table-column>
        </el-table>

        <el-alert type="info" :closable="false" show-icon class="scope-note" :title="note" />
      </template>
    </el-card>
  </div>
</template>

<style scoped>
.page {
  padding: 16px;
}
.page-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.sub-title {
  font-weight: 600;
  font-size: 13px;
  margin: 16px 0 8px;
}
.tip,
.muted {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  font-weight: normal;
}
.scope-note {
  margin-top: 12px;
}
</style>
