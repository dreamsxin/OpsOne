<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import {
  listKubeClusters,
  listKubeNamespaceInventory,
  listKubeNodeInventory,
  type KubeCluster,
  type KubeNamespaceDetail,
  type KubeNodeDetail
} from '@/api'

const tab = ref<'node' | 'namespace'>('node')
const clusters = ref<KubeCluster[]>([])
const query = reactive({ clusterId: 0, keyword: '', onlyProblem: false, nsFilter: '' })

/* ---------- 节点 ---------- */
const nodeLoading = ref(false)
const nodes = ref<KubeNodeDetail[]>([])
const nodeStats = reactive({
  total: 0,
  ready: 0,
  notReady: 0,
  cordoned: 0,
  tainted: 0,
  versionSkew: false,
  podCounted: true
})
const versions = ref<string[]>([])
const nodeNotes = ref<string[]>([])
const nodeError = ref('')

/* ---------- 命名空间 ---------- */
const nsLoading = ref(false)
const namespaces = ref<KubeNamespaceDetail[]>([])
const nsStats = reactive({ total: 0, terminating: 0, noQuota: 0, quotaRead: true, podCounted: true })
const nsNotes = ref<string[]>([])
const nsError = ref('')

const effectMeta: Record<string, { label: string; type: 'warning' | 'danger' | 'info' }> = {
  NoSchedule: { label: '不调度新 Pod', type: 'warning' },
  PreferNoSchedule: { label: '尽量不调度', type: 'info' },
  NoExecute: { label: '驱逐不容忍的 Pod', type: 'danger' }
}

async function loadNodes() {
  if (!query.clusterId) return
  nodeLoading.value = true
  nodeError.value = ''
  try {
    const params: Record<string, any> = {}
    if (query.keyword) params.keyword = query.keyword
    if (query.onlyProblem) params.problem = '1'
    const res = await listKubeNodeInventory(query.clusterId, params)
    nodes.value = res.nodes
    Object.assign(nodeStats, {
      total: res.total,
      ready: res.ready,
      notReady: res.notReady,
      cordoned: res.cordoned,
      tainted: res.tainted,
      versionSkew: res.versionSkew,
      podCounted: res.podCounted
    })
    versions.value = res.versions
    nodeNotes.value = res.notes
  } catch (err: any) {
    nodes.value = []
    nodeError.value = err?.message || '读取失败'
  } finally {
    nodeLoading.value = false
  }
}

async function loadNamespaces() {
  if (!query.clusterId) return
  nsLoading.value = true
  nsError.value = ''
  try {
    const params: Record<string, any> = {}
    if (query.keyword) params.keyword = query.keyword
    if (query.nsFilter) params.filter = query.nsFilter
    const res = await listKubeNamespaceInventory(query.clusterId, params)
    namespaces.value = res.namespaces
    Object.assign(nsStats, {
      total: res.total,
      terminating: res.terminating,
      noQuota: res.noQuota,
      quotaRead: res.quotaRead,
      podCounted: res.podCounted
    })
    nsNotes.value = res.notes
  } catch (err: any) {
    namespaces.value = []
    nsError.value = err?.message || '读取失败'
  } finally {
    nsLoading.value = false
  }
}

function reload() {
  if (tab.value === 'node') loadNodes()
  else loadNamespaces()
}

watch(() => query.clusterId, () => {
  query.keyword = ''
  loadNodes()
  if (tab.value === 'namespace') loadNamespaces()
})

watch(tab, (value) => {
  if (value === 'namespace' && !namespaces.value.length) loadNamespaces()
})

onMounted(async () => {
  clusters.value = await listKubeClusters()
  if (clusters.value.length) query.clusterId = clusters.value[0].id
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这一页只看节点与命名空间**自身**的健康与约束。
          <strong>资源账本（已分配 / limits / 实际用量）在「容量与配额」页</strong> ——
          两处各算一遍迟早会对不上，而对不上的数字比没有数字更糟。
          <strong>只读</strong>：不提供 cordon / drain / 打污点、也不提供命名空间的创建与删除。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-input
          v-model="query.keyword"
          :placeholder="tab === 'node' ? '节点名 / IP / 角色' : '命名空间名'"
          clearable
          style="width: 200px"
          @keyup.enter="reload"
        />
        <el-checkbox v-if="tab === 'node'" v-model="query.onlyProblem" @change="loadNodes">
          只看有问题的
        </el-checkbox>
        <el-select
          v-if="tab === 'namespace'"
          v-model="query.nsFilter"
          placeholder="全部命名空间"
          clearable
          style="width: 190px"
          @change="loadNamespaces"
        >
          <el-option label="有问题的" value="problem" />
          <el-option label="卡在 Terminating" value="terminating" />
          <el-option label="没有配额约束" value="noquota" :disabled="!nsStats.quotaRead" />
          <el-option label="空的（没有 Pod）" value="empty" />
        </el-select>
        <el-button @click="reload">查询</el-button>
      </div>

      <el-tabs v-model="tab">
        <!-- ---------- 节点 ---------- -->
        <el-tab-pane label="节点" name="node">
          <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
            <el-tag type="info">共 {{ nodeStats.total }}</el-tag>
            <el-tag type="success">就绪 {{ nodeStats.ready }}</el-tag>
            <el-tag v-if="nodeStats.notReady" type="danger">未就绪 {{ nodeStats.notReady }}</el-tag>
            <el-tag v-if="nodeStats.cordoned" type="warning">已 cordon {{ nodeStats.cordoned }}</el-tag>
            <el-tag v-if="nodeStats.tainted" type="info">有污点 {{ nodeStats.tainted }}</el-tag>
            <el-tooltip
              v-if="nodeStats.versionSkew"
              content="集群里出现了多个 kubelet 版本 —— 升级做到一半停下来是很常见的现场"
              placement="top"
            >
              <el-tag type="warning">版本不一致：{{ versions.join('、') }}</el-tag>
            </el-tooltip>
            <el-tag v-else-if="versions.length" type="info">kubelet {{ versions[0] }}</el-tag>
            <el-tag v-if="!nodeStats.podCounted" type="warning">
              读不到 Pod 列表，Pod 数显示为「未知」
            </el-tag>
          </div>

          <el-alert v-if="nodeError" type="error" :closable="false" style="margin-bottom: 12px" :title="nodeError" />

          <el-table v-loading="nodeLoading" :data="nodes" border stripe empty-text="没有匹配的节点">
            <el-table-column type="expand">
              <template #default="{ row }">
                <div style="padding: 8px 24px">
                  <el-descriptions :column="3" border size="small">
                    <el-descriptions-item label="内核">{{ row.kernel || '—' }}</el-descriptions-item>
                    <el-descriptions-item label="容器运行时">{{ row.runtime || '—' }}</el-descriptions-item>
                    <el-descriptions-item label="操作系统">{{ row.osImage || '—' }}</el-descriptions-item>
                    <el-descriptions-item label="CPU 容量 / 可分配">
                      {{ row.capacityCpu || '—' }} / {{ row.allocCpu || '—' }}
                    </el-descriptions-item>
                    <el-descriptions-item label="内存 容量 / 可分配">
                      {{ row.capacityMemory || '—' }} / {{ row.allocMemory || '—' }}
                    </el-descriptions-item>
                    <el-descriptions-item label="Pod 上限 容量 / 可分配">
                      {{ row.capacityPods || '—' }} / {{ row.allocPods || '—' }}
                    </el-descriptions-item>
                  </el-descriptions>

                  <div v-if="row.taints?.length" style="margin-top: 10px">
                    <strong>污点</strong>
                    <el-table :data="row.taints" border size="small" style="margin-top: 6px">
                      <el-table-column prop="key" label="Key" min-width="240" />
                      <el-table-column prop="value" label="Value" width="140" />
                      <el-table-column label="效果" width="200">
                        <template #default="scope">
                          <el-tag size="small" :type="effectMeta[scope.row.effect]?.type || 'info'">
                            {{ scope.row.effect }}
                          </el-tag>
                          <el-text type="info" size="small" style="margin-left: 6px">
                            {{ effectMeta[scope.row.effect]?.label }}
                          </el-text>
                        </template>
                      </el-table-column>
                    </el-table>
                  </div>

                  <div style="margin-top: 10px">
                    <strong>状态条件</strong>
                    <el-text type="info" size="small" style="margin-left: 8px">
                      Ready 应当为 True，其余几个压力条件应当为 False —— 方向是反的
                    </el-text>
                    <el-table :data="row.conditions" border size="small" style="margin-top: 6px">
                      <el-table-column prop="type" label="条件" width="180" />
                      <el-table-column label="值" width="100">
                        <template #default="scope">
                          <el-tag size="small" :type="scope.row.problem ? 'danger' : 'success'">
                            {{ scope.row.status }}
                          </el-tag>
                        </template>
                      </el-table-column>
                      <el-table-column prop="reason" label="原因" width="220" show-overflow-tooltip />
                      <el-table-column prop="message" label="说明" min-width="260" show-overflow-tooltip />
                    </el-table>
                  </div>
                </div>
              </template>
            </el-table-column>
            <el-table-column prop="name" label="节点" min-width="170" show-overflow-tooltip />
            <el-table-column label="状态" width="120">
              <template #default="{ row }">
                <el-tag size="small" :type="row.ready ? 'success' : 'danger'">
                  {{ row.ready ? 'Ready' : 'NotReady' }}
                </el-tag>
                <el-tag v-if="row.unschedulable" size="small" type="warning" style="margin-left: 4px">
                  cordon
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="角色" width="140">
              <template #default="{ row }">{{ row.roles?.join('、') || '—' }}</template>
            </el-table-column>
            <el-table-column prop="internalIP" label="内网 IP" width="140" />
            <el-table-column prop="version" label="kubelet" width="110" />
            <el-table-column label="Pod 数" width="110">
              <template #default="{ row }">
                <span v-if="!nodeStats.podCounted" style="color: #9ca3af">未知</span>
                <span v-else>
                  {{ row.podCount }}<span v-if="row.podCapacity"> / {{ row.podCapacity }}</span>
                </span>
              </template>
            </el-table-column>
            <el-table-column label="污点" width="80">
              <template #default="{ row }">{{ row.taints?.length || 0 }}</template>
            </el-table-column>
            <el-table-column label="问题" min-width="300">
              <template #default="{ row }">
                <span v-if="!row.problems?.length" style="color: #9ca3af">没发现问题</span>
                <div v-for="(p, idx) in row.problems" :key="idx" style="color: var(--el-color-danger); font-size: 12px">
                  {{ p }}
                </div>
              </template>
            </el-table-column>
          </el-table>

          <el-card v-if="nodeNotes.length" shadow="never" style="margin-top: 12px">
            <template #header>这一页的边界</template>
            <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
              <li v-for="(note, idx) in nodeNotes" :key="idx">{{ note }}</li>
            </ul>
          </el-card>
        </el-tab-pane>

        <!-- ---------- 命名空间 ---------- -->
        <el-tab-pane label="命名空间" name="namespace">
          <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
            <el-tag type="info">共 {{ nsStats.total }}</el-tag>
            <el-tag v-if="nsStats.terminating" type="danger">卡在 Terminating {{ nsStats.terminating }}</el-tag>
            <el-tag v-if="nsStats.quotaRead && nsStats.noQuota" type="warning">
              没有配额约束 {{ nsStats.noQuota }}
            </el-tag>
            <el-tag v-if="!nsStats.quotaRead" type="warning">
              读不到 ResourceQuota，不判定「有没有配额」
            </el-tag>
            <el-tag v-if="!nsStats.podCounted" type="warning">读不到 Pod 列表，Pod 数显示为「未知」</el-tag>
          </div>

          <el-alert v-if="nsError" type="error" :closable="false" style="margin-bottom: 12px" :title="nsError" />

          <el-table v-loading="nsLoading" :data="namespaces" border stripe empty-text="没有匹配的命名空间">
            <el-table-column type="expand">
              <template #default="{ row }">
                <div style="padding: 8px 24px">
                  <el-alert
                    v-if="row.conditions?.length"
                    type="warning"
                    :closable="false"
                    style="margin-bottom: 10px"
                  >
                    <template #title>
                      <div v-for="(cond, idx) in row.conditions" :key="idx">{{ cond }}</div>
                    </template>
                  </el-alert>

                  <el-descriptions :column="2" border size="small">
                    <el-descriptions-item label="finalizer">
                      {{ row.finalizers?.join('、') || '—' }}
                    </el-descriptions-item>
                    <el-descriptions-item label="LimitRange">
                      {{ nsStats.quotaRead ? (row.hasLimitRange ? '有' : '没有') : '未知' }}
                    </el-descriptions-item>
                    <el-descriptions-item label="标签" :span="2">
                      <span v-if="!row.labels || !Object.keys(row.labels).length" style="color: #9ca3af">—</span>
                      <el-tag
                        v-for="(v, k) in row.labels"
                        :key="k"
                        size="small"
                        type="info"
                        style="margin-right: 4px"
                      >
                        {{ k }}={{ v }}
                      </el-tag>
                    </el-descriptions-item>
                  </el-descriptions>

                  <div v-if="row.quotas?.length" style="margin-top: 10px">
                    <strong>ResourceQuota</strong>
                    <el-text type="info" size="small" style="margin-left: 8px">
                      只列 hard 里设了上限的项
                    </el-text>
                    <el-card v-for="q in row.quotas" :key="q.name" shadow="never" style="margin-top: 6px">
                      <template #header>{{ q.name }}</template>
                      <div v-for="(item, idx) in q.items" :key="idx" style="font-size: 12px; line-height: 1.8">
                        {{ item }}
                      </div>
                    </el-card>
                  </div>
                </div>
              </template>
            </el-table-column>
            <el-table-column prop="name" label="命名空间" min-width="180" show-overflow-tooltip />
            <el-table-column label="状态" width="130">
              <template #default="{ row }">
                <el-tag size="small" :type="row.terminating ? 'danger' : 'success'">{{ row.phase }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="Pod（运行/等待/失败）" width="200">
              <template #default="{ row }">
                <span v-if="!nsStats.podCounted" style="color: #9ca3af">未知</span>
                <span v-else>
                  {{ row.podTotal }}（{{ row.podRunning }} /
                  <span :style="row.podPending ? 'color: var(--el-color-warning)' : ''">{{ row.podPending }}</span> /
                  <span :style="row.podFailed ? 'color: var(--el-color-danger)' : ''">{{ row.podFailed }}</span>）
                </span>
              </template>
            </el-table-column>
            <el-table-column label="配额约束" width="150">
              <template #default="{ row }">
                <span v-if="!nsStats.quotaRead" style="color: #9ca3af">未知</span>
                <template v-else>
                  <el-tag v-if="row.quotas?.length" size="small" type="success">
                    Quota {{ row.quotas.length }}
                  </el-tag>
                  <el-tag v-if="row.hasLimitRange" size="small" type="success" style="margin-left: 4px">
                    LimitRange
                  </el-tag>
                  <span v-if="!row.quotas?.length && !row.hasLimitRange" style="color: #9ca3af">无</span>
                </template>
              </template>
            </el-table-column>
            <el-table-column label="问题" min-width="320">
              <template #default="{ row }">
                <span v-if="!row.problems?.length" style="color: #9ca3af">没发现问题</span>
                <div v-for="(p, idx) in row.problems" :key="idx" style="color: var(--el-color-danger); font-size: 12px">
                  {{ p }}
                </div>
              </template>
            </el-table-column>
            <el-table-column label="创建时间" width="160">
              <template #default="{ row }">
                {{ row.createdAt && !row.createdAt.startsWith('0001') ? row.createdAt.slice(0, 19).replace('T', ' ') : '—' }}
              </template>
            </el-table-column>
          </el-table>

          <el-card v-if="nsNotes.length" shadow="never" style="margin-top: 12px">
            <template #header>这一页的边界</template>
            <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
              <li v-for="(note, idx) in nsNotes" :key="idx">{{ note }}</li>
            </ul>
          </el-card>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>
