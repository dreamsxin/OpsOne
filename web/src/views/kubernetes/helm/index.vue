<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  listKubeClusters,
  listKubeHelmReleases,
  listKubeNamespaces,
  type KubeCluster,
  type KubeHelmRelease,
  type KubeNamespace
} from '@/api'

const loading = ref(false)
const clusters = ref<KubeCluster[]>([])
const namespaces = ref<KubeNamespace[]>([])
const releases = ref<KubeHelmRelease[]>([])
const byStatus = ref<Record<string, number>>({})
const notes = ref<string[]>([])
const errorMsg = ref('')

const query = reactive({ clusterId: 0, namespace: '', keyword: '' })

const statusMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  deployed: { label: '已部署', type: 'success' },
  failed: { label: '失败', type: 'danger' },
  superseded: { label: '已被取代', type: 'info' },
  uninstalled: { label: '已卸载', type: 'info' },
  uninstalling: { label: '卸载中', type: 'warning' },
  'pending-install': { label: '安装中', type: 'warning' },
  'pending-upgrade': { label: '升级中', type: 'warning' },
  'pending-rollback': { label: '回滚中', type: 'warning' },
  unknown: { label: '未知', type: 'info' }
}

const failedCount = computed(() =>
  Object.entries(byStatus.value)
    .filter(([status]) => status === 'failed' || status.startsWith('pending'))
    .reduce((sum, [, n]) => sum + n, 0)
)

async function loadNamespaces() {
  namespaces.value = []
  if (!query.clusterId) return
  try {
    namespaces.value = await listKubeNamespaces(query.clusterId)
  } catch {
    // 命名空间列不出来不该挡住 release 列表（可能只是没有 list namespaces 权限）
  }
}

async function load() {
  if (!query.clusterId) return
  loading.value = true
  errorMsg.value = ''
  try {
    const params: Record<string, any> = {}
    if (query.namespace) params.namespace = query.namespace
    if (query.keyword) params.keyword = query.keyword
    const res = await listKubeHelmReleases(query.clusterId, params)
    releases.value = res.releases
    byStatus.value = res.byStatus || {}
    notes.value = res.notes
  } catch (err: any) {
    releases.value = []
    errorMsg.value = err?.message || '读取失败'
  } finally {
    loading.value = false
  }
}

watch(() => query.clusterId, async () => {
  query.namespace = ''
  await loadNamespaces()
  load()
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
          Helm release 直接从集群里 <code>type=helm.sh/release.v1</code> 的 Secret 解出来 ——
          <strong>不需要 helm 二进制，也不访问 chart 仓库</strong>。
          这一页是**只读**的：install / upgrade / rollback / uninstall 都不做，
          那需要真正的 Helm 引擎（模板渲染、钩子、依赖、CRD 处理），半套实现比没有更危险。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="query.namespace" placeholder="全部命名空间" clearable filterable style="width: 190px" @change="load">
          <el-option v-for="ns in namespaces" :key="ns.name" :label="ns.name" :value="ns.name" />
        </el-select>
        <el-input
          v-model="query.keyword"
          placeholder="release 名 / chart 名"
          clearable
          style="width: 190px"
          @keyup.enter="load"
        />
        <el-button @click="load">查询</el-button>
        <div class="grow"></div>
        <el-tag type="info">共 {{ releases.length }}</el-tag>
        <el-tag v-if="failedCount" type="danger">失败或进行中 {{ failedCount }}</el-tag>
      </div>

      <el-alert v-if="errorMsg" type="error" :closable="false" style="margin-bottom: 12px" :title="errorMsg" />

      <el-table
        v-loading="loading"
        :data="releases"
        border
        stripe
        empty-text="这个集群（或命名空间）里没有 Helm release —— 也可能是用 configmap driver 存的，那种看不到"
      >
        <el-table-column prop="name" label="Release" min-width="160" show-overflow-tooltip />
        <el-table-column prop="namespace" label="命名空间" width="130" show-overflow-tooltip />
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.label || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="Chart" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">{{ row.chart }}-{{ row.chartVersion }}</template>
        </el-table-column>
        <el-table-column prop="appVersion" label="应用版本" width="110" show-overflow-tooltip />
        <el-table-column label="版本" width="110">
          <template #default="{ row }">
            <el-tooltip :content="`一共留了 ${row.revisions} 个历史版本`" placement="top">
              <span>第 {{ row.revision }} 次</span>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="最近一次操作" min-width="180" show-overflow-tooltip />
        <el-table-column label="最近更新" width="160">
          <template #default="{ row }">
            {{ row.updatedAt && !row.updatedAt.startsWith('0001') ? row.updatedAt.slice(0, 19).replace('T', ' ') : '—' }}
          </template>
        </el-table-column>
        <el-table-column label="对应 Secret" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <code style="font-size: 12px">{{ row.secretName }}</code>
          </template>
        </el-table-column>
      </el-table>

      <el-card v-if="notes.length" shadow="never" style="margin-top: 12px">
        <template #header>这一页的边界</template>
        <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
          <li v-for="(note, idx) in notes" :key="idx">{{ note }}</li>
        </ul>
      </el-card>
    </el-card>
  </div>
</template>
