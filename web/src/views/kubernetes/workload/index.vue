<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listKubeClusters,
  listKubeEvents,
  listKubeNamespaces,
  listKubePods,
  listKubeWorkloads,
  type KubeCluster,
  type KubeEvent,
  type KubePod,
  type KubeWorkload
} from '@/api'

const clusters = ref<KubeCluster[]>([])
const namespaces = ref<string[]>([])
const clusterId = ref<number>(0)
const namespace = ref('')
const tab = ref('workload')
const onlyWarning = ref(true)

const loading = reactive({ workload: false, pod: false, event: false })
const workloads = ref<KubeWorkload[]>([])
const workloadWarnings = ref<string[]>([])
const workloadStat = reactive({ total: 0, unhealthy: 0 })
const pods = ref<KubePod[]>([])
const podStat = reactive({ total: 0, abnormal: 0 })
const events = ref<KubeEvent[]>([])

const currentCluster = computed(() => clusters.value.find((c) => c.id === clusterId.value))

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
    // 命名空间读不到（多为 RBAC 不足）不影响按全部查
  }
}

async function loadWorkloads() {
  if (!clusterId.value) return
  loading.workload = true
  try {
    const res = await listKubeWorkloads(clusterId.value, namespace.value)
    workloads.value = res.items || []
    workloadWarnings.value = res.warnings || []
    workloadStat.total = res.total
    workloadStat.unhealthy = res.unhealthy
  } catch (err: any) {
    workloads.value = []
    ElMessage.error(err?.message || '读取工作负载失败')
  } finally {
    loading.workload = false
  }
}

async function loadPods() {
  if (!clusterId.value) return
  loading.pod = true
  try {
    const res = await listKubePods(clusterId.value, namespace.value)
    pods.value = res.items || []
    podStat.total = res.total
    podStat.abnormal = res.abnormal
  } catch (err: any) {
    pods.value = []
    ElMessage.error(err?.message || '读取 Pod 失败')
  } finally {
    loading.pod = false
  }
}

async function loadEvents() {
  if (!clusterId.value) return
  loading.event = true
  try {
    const res = await listKubeEvents(clusterId.value, namespace.value, onlyWarning.value)
    events.value = res.items || []
  } catch (err: any) {
    events.value = []
    ElMessage.error(err?.message || '读取事件失败')
  } finally {
    loading.event = false
  }
}

function reloadCurrent() {
  if (tab.value === 'workload') loadWorkloads()
  else if (tab.value === 'pod') loadPods()
  else loadEvents()
}

watch(clusterId, async () => {
  namespace.value = ''
  await loadNamespaces()
  reloadCurrent()
})
watch([namespace, tab, onlyWarning], reloadCurrent)

const phaseType: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
  Running: 'success',
  Succeeded: 'info',
  Pending: 'warning',
  Failed: 'danger'
}

onMounted(async () => {
  await loadClusters()
  if (clusterId.value) {
    await loadNamespaces()
    reloadCurrent()
  }
})
</script>

<template>
  <div class="page">
    <el-card>
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
        <el-button @click="reloadCurrent">刷新</el-button>
        <el-checkbox v-if="tab === 'event'" v-model="onlyWarning">只看 Warning</el-checkbox>
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
          v-if="workloadWarnings.length"
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`部分资源读取失败（多为 RBAC 不足）：${workloadWarnings.join('; ')}`"
        />

        <el-tabs v-model="tab">
          <el-tab-pane name="workload">
            <template #label>
              工作负载
              <el-badge
                v-if="workloadStat.unhealthy"
                :value="workloadStat.unhealthy"
                type="danger"
                style="margin-left: 6px"
              />
            </template>
            <el-table v-loading="loading.workload" :data="workloads" border stripe>
              <el-table-column prop="kind" label="类型" width="110" />
              <el-table-column prop="namespace" label="命名空间" width="140" />
              <el-table-column prop="name" label="名称" min-width="180" />
              <el-table-column label="副本" width="140">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.healthy ? 'success' : 'danger'">
                    {{ row.ready }}/{{ row.desired }} 就绪
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="可用/已更新" width="120">
                <template #default="{ row }">{{ row.available }} / {{ row.updated }}</template>
              </el-table-column>
              <el-table-column label="镜像" min-width="240">
                <template #default="{ row }">{{ row.images.join(', ') }}</template>
              </el-table-column>
              <el-table-column prop="createdAt" label="创建时间" min-width="180" />
            </el-table>
          </el-tab-pane>

          <el-tab-pane name="pod">
            <template #label>
              Pod
              <el-badge
                v-if="podStat.abnormal"
                :value="podStat.abnormal"
                type="danger"
                style="margin-left: 6px"
              />
            </template>
            <el-table v-loading="loading.pod" :data="pods" border stripe>
              <el-table-column prop="namespace" label="命名空间" width="140" />
              <el-table-column prop="name" label="名称" min-width="220" show-overflow-tooltip />
              <el-table-column label="状态" width="110">
                <template #default="{ row }">
                  <el-tag size="small" :type="phaseType[row.phase] || 'info'">{{ row.phase }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="ready" label="就绪" width="80" />
              <el-table-column prop="restarts" label="重启" width="70" />
              <el-table-column prop="nodeName" label="所在节点" min-width="150" />
              <el-table-column prop="podIP" label="Pod IP" width="130" />
              <el-table-column label="容器异常" min-width="200">
                <template #default="{ row }">
                  <span v-if="row.containerMsg" style="color: var(--el-color-danger)">
                    {{ row.containerMsg }}
                  </span>
                  <span v-else>—</span>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="事件" name="event">
            <el-table v-loading="loading.event" :data="events" border stripe>
              <el-table-column label="类型" width="100">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.type === 'Warning' ? 'warning' : 'info'">
                    {{ row.type }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="namespace" label="命名空间" width="130" />
              <el-table-column prop="object" label="对象" min-width="200" show-overflow-tooltip />
              <el-table-column prop="reason" label="原因" width="170" />
              <el-table-column prop="count" label="次数" width="70" />
              <el-table-column prop="message" label="详情" min-width="300" show-overflow-tooltip />
              <el-table-column prop="lastSeen" label="最近发生" min-width="180" />
            </el-table>
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-card>
  </div>
</template>
