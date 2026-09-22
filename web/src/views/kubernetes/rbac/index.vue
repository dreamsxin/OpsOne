<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import {
  listKubeClusters,
  listKubeNamespaces,
  listKubeRBAC,
  type KubeCluster,
  type KubeNamespace,
  type KubeServiceAccount
} from '@/api'

const loading = ref(false)
const clusters = ref<KubeCluster[]>([])
const namespaces = ref<KubeNamespace[]>([])
const accounts = ref<KubeServiceAccount[]>([])
const stats = ref<Record<string, number>>({})
const notes = ref<string[]>([])
const total = ref(0)
const errorMsg = ref('')

const query = reactive({ clusterId: 0, namespace: '', keyword: '', risk: '' })

const detailVisible = ref(false)
const current = ref<KubeServiceAccount | null>(null)

async function loadNamespaces() {
  namespaces.value = []
  if (!query.clusterId) return
  try {
    namespaces.value = await listKubeNamespaces(query.clusterId)
  } catch {
    // 没有 list namespaces 权限时不该挡住 RBAC 反查
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
    if (query.risk) params.risk = query.risk
    const res = await listKubeRBAC(query.clusterId, params)
    accounts.value = res.accounts || []
    stats.value = res.stats || {}
    notes.value = res.notes
    total.value = res.total
  } catch (err: any) {
    accounts.value = []
    errorMsg.value = err?.message || '读取失败'
  } finally {
    loading.value = false
  }
}

function openDetail(row: KubeServiceAccount) {
  current.value = row
  detailVisible.value = true
}

function ruleText(rule: {
  apiGroups: string[]
  resources: string[]
  verbs: string[]
  resourceNames?: string[]
  nonResourceURLs?: string[]
}) {
  if (rule.nonResourceURLs?.length) {
    return `非资源路径 ${rule.nonResourceURLs.join('、')} → ${rule.verbs.join('、')}`
  }
  const groups = rule.apiGroups?.map((g) => (g === '' ? '核心组' : g)).join('、') || '—'
  let text = `${groups} / ${rule.resources?.join('、') || '—'} → ${rule.verbs?.join('、') || '—'}`
  if (rule.resourceNames?.length) {
    text += `（仅限 ${rule.resourceNames.join('、')}）`
  }
  return text
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
          「实际权限」是<strong>反查算出来的</strong>：从 RoleBinding / ClusterRoleBinding 找到
          Role / ClusterRole 再读它的 rules —— k8s 里没有「这个 SA 有哪些权限」这样一个对象。
          <br />
          即使只看某个命名空间，Role 与 Binding 也<strong>一律全集群读</strong>：
          命名空间里的 SA 可能被 ClusterRoleBinding 授权，只读本命名空间会漏掉最危险的那一类。
          这一页<strong>只读</strong> —— 改 RBAC 就是改提权路径，那类操作仍然走 kubectl。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="query.namespace" placeholder="全部命名空间" clearable filterable style="width: 180px" @change="load">
          <el-option v-for="ns in namespaces" :key="ns.name" :label="ns.name" :value="ns.name" />
        </el-select>
        <el-select v-model="query.risk" placeholder="全部账户" clearable style="width: 170px" @change="load">
          <el-option label="绑了 cluster-admin" value="admin" />
          <el-option label="有通配符权限" value="wildcard" />
          <el-option label="没有任何授权" value="unused" />
        </el-select>
        <el-input v-model="query.keyword" placeholder="账户名 / 命名空间" clearable style="width: 180px" @keyup.enter="load" />
        <el-button @click="load">查询</el-button>
      </div>

      <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-tag type="info">ServiceAccount {{ total }}</el-tag>
        <el-tag v-if="stats.clusterAdmin" type="danger">绑了 cluster-admin {{ stats.clusterAdmin }}</el-tag>
        <el-tag v-if="stats.wildcard" type="warning">有通配符权限 {{ stats.wildcard }}</el-tag>
        <el-tag v-if="stats.unbound" type="info">没有任何授权 {{ stats.unbound }}</el-tag>
        <el-tooltip
          v-if="stats.orphanBindings"
          content="绑了但引用的 Role 不存在 —— 表现是「绑了却什么权限都没有」，很容易被当成权限不够去加更大的角色"
          placement="top"
        >
          <el-tag type="danger">孤儿绑定 {{ stats.orphanBindings }}</el-tag>
        </el-tooltip>
        <el-tag type="info">
          Role {{ stats.roles }} / ClusterRole {{ stats.clusterRoles }} ·
          Binding {{ stats.bindings }} / ClusterBinding {{ stats.clusterBindings }}
        </el-tag>
      </div>

      <el-alert v-if="errorMsg" type="error" :closable="false" style="margin-bottom: 12px" :title="errorMsg" />

      <el-table v-loading="loading" :data="accounts" border stripe empty-text="没有匹配的 ServiceAccount">
        <el-table-column prop="name" label="ServiceAccount" min-width="190" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link type="primary" :underline="false" @click="openDetail(row)">{{ row.name }}</el-link>
          </template>
        </el-table-column>
        <el-table-column prop="namespace" label="命名空间" width="140" show-overflow-tooltip />
        <el-table-column label="风险" width="170">
          <template #default="{ row }">
            <el-tag v-if="row.clusterAdmin" size="small" type="danger">cluster-admin</el-tag>
            <el-tag v-else-if="row.wildcard" size="small" type="warning">通配符权限</el-tag>
            <el-tag v-else-if="!row.bindings?.length" size="small" type="info">无授权</el-tag>
            <span v-else style="color: #9ca3af">—</span>
          </template>
        </el-table-column>
        <el-table-column label="授权条数" width="100">
          <template #default="{ row }">{{ row.bindings?.length || 0 }}</template>
        </el-table-column>
        <el-table-column label="能做的动作" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.verbSummary?.length">{{ row.verbSummary.join('、') }}</span>
            <span v-else style="color: #9ca3af">没有任何权限</span>
          </template>
        </el-table-column>
        <el-table-column label="挂载 Secret" width="120">
          <template #default="{ row }">{{ row.secrets?.length || 0 }}</template>
        </el-table-column>
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">
            {{ row.createdAt && !row.createdAt.startsWith('0001') ? row.createdAt.slice(0, 19).replace('T', ' ') : '—' }}
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

    <el-drawer
      v-model="detailVisible"
      :title="`${current?.namespace}/${current?.name} 的实际权限`"
      size="780px"
    >
      <template v-if="current">
        <el-alert v-if="current.clusterAdmin" type="error" :closable="false" style="margin-bottom: 12px">
          <template #title>这个账户被绑到了 cluster-admin —— 它对整个集群有完全控制权</template>
        </el-alert>
        <el-alert v-else-if="current.wildcard" type="warning" :closable="false" style="margin-bottom: 12px">
          <template #title>这个账户拿到了通配符（*）权限，实际能做的比规则看起来更多</template>
        </el-alert>
        <el-empty v-if="!current.bindings?.length" description="没有任何 RoleBinding / ClusterRoleBinding 指向它" />

        <el-card v-for="(ref, idx) in current.bindings || []" :key="idx" shadow="never" style="margin-bottom: 12px">
          <template #header>
            <el-tag size="small" :type="ref.bindingKind === 'ClusterRoleBinding' ? 'warning' : 'info'">
              {{ ref.bindingKind }}
            </el-tag>
            <span style="margin-left: 8px">{{ ref.bindingName }}</span>
            <el-text type="info" size="small" style="margin-left: 8px">
              生效范围：{{ ref.scope }} · 指向 {{ ref.roleKind }} {{ ref.roleName }}
            </el-text>
          </template>
          <el-alert v-if="ref.missing" type="error" :closable="false">
            <template #title>
              引用的 {{ ref.roleKind }} 「{{ ref.roleName }}」在集群里不存在 ——
              这条绑定不会带来任何权限（很容易被当成权限不够去加更大的角色）
            </template>
          </el-alert>
          <el-table v-else :data="ref.rules" border stripe size="small" empty-text="这个角色没有任何规则">
            <el-table-column label="规则">
              <template #default="{ row }">{{ ruleText(row) }}</template>
            </el-table-column>
          </el-table>
        </el-card>
      </template>
    </el-drawer>
  </div>
</template>
