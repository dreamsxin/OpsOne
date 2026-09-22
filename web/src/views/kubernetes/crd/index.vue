<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getKubeCRDResource,
  listKubeCRDResources,
  listKubeCRDs,
  listKubeClusters,
  listKubeGatewayRoutes,
  listKubeNamespaces,
  type KubeCRD,
  type KubeCluster,
  type KubeGatewayRoute,
  type KubeNamespace,
  type KubeResourceItem
} from '@/api'

const tab = ref<'crd' | 'gateway'>('crd')
const clusters = ref<KubeCluster[]>([])
const namespaces = ref<KubeNamespace[]>([])
const query = reactive({ clusterId: 0, namespace: '', keyword: '' })

/* ---------- CRD ---------- */
const crdLoading = ref(false)
const crds = ref<KubeCRD[]>([])
const groups = ref<string[]>([])
const notEstablished = ref(0)
const gatewayVersion = ref('')
const crdNotes = ref<string[]>([])
const groupFilter = ref('')
const crdError = ref('')

/* ---------- 实例 ---------- */
const instLoading = ref(false)
const instVisible = ref(false)
const current = ref<KubeCRD | null>(null)
const currentVersion = ref('')
const instances = ref<KubeResourceItem[]>([])
const instTotal = ref(0)
const instTruncated = ref(false)
const instNote = ref('')
const instError = ref('')

/* ---------- YAML ---------- */
const yamlVisible = ref(false)
const yamlText = ref('')
const yamlTitle = ref('')
const yamlNote = ref('')

/* ---------- Gateway API ---------- */
const gwLoading = ref(false)
const gwInstalled = ref(true)
const gwRoutes = ref<KubeGatewayRoute[]>([])
const gwNote = ref('')
const gwNotes = ref<string[]>([])
const gwVersion = ref('')

const filteredCRDs = computed(() => {
  if (!groupFilter.value) return crds.value
  return crds.value.filter((item) => item.group === groupFilter.value)
})

async function loadNamespaces() {
  namespaces.value = []
  if (!query.clusterId) return
  try {
    namespaces.value = await listKubeNamespaces(query.clusterId)
  } catch {
    // 没有 list namespaces 权限时不该挡住 CRD 浏览
  }
}

async function loadCRDs() {
  if (!query.clusterId) return
  crdLoading.value = true
  crdError.value = ''
  try {
    const params: Record<string, any> = {}
    if (query.keyword) params.keyword = query.keyword
    const res = await listKubeCRDs(query.clusterId, params)
    crds.value = res.crds
    groups.value = res.groups
    notEstablished.value = res.notEstablished
    gatewayVersion.value = res.gatewayAPI
    crdNotes.value = res.notes
  } catch (err: any) {
    crds.value = []
    crdError.value = err?.message || '读取失败'
  } finally {
    crdLoading.value = false
  }
}

async function openInstances(row: KubeCRD) {
  current.value = row
  currentVersion.value = row.storageVersion || row.servedVersions[0] || ''
  instVisible.value = true
  await loadInstances()
}

async function loadInstances() {
  if (!current.value) return
  instLoading.value = true
  instError.value = ''
  try {
    const params: Record<string, any> = {
      crd: current.value.name,
      version: currentVersion.value
    }
    if (current.value.namespaced && query.namespace) params.namespace = query.namespace
    const res = await listKubeCRDResources(query.clusterId, params)
    instances.value = res.items
    instTotal.value = res.total
    instTruncated.value = res.truncated
    instNote.value = res.note
  } catch (err: any) {
    instances.value = []
    instError.value = err?.message || '读取失败'
  } finally {
    instLoading.value = false
  }
}

async function openYAML(row: KubeResourceItem) {
  if (!current.value) return
  const res = await getKubeCRDResource(query.clusterId, {
    crd: current.value.name,
    version: currentVersion.value,
    namespace: row.namespace,
    name: row.name
  })
  yamlText.value = res.yaml
  yamlTitle.value = `${res.kind} ${row.namespace ? row.namespace + '/' : ''}${row.name}`
  yamlNote.value = res.note
  yamlVisible.value = true
}

async function copyYAML() {
  try {
    await navigator.clipboard.writeText(yamlText.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('浏览器不允许复制，请手动选中')
  }
}

async function loadGateway() {
  if (!query.clusterId) return
  gwLoading.value = true
  try {
    const params: Record<string, any> = {}
    if (query.namespace) params.namespace = query.namespace
    const res = await listKubeGatewayRoutes(query.clusterId, params)
    gwInstalled.value = res.installed
    gwRoutes.value = res.routes || []
    gwNote.value = res.note || ''
    gwNotes.value = res.notes || []
    gwVersion.value = res.version || ''
  } finally {
    gwLoading.value = false
  }
}

watch(() => query.clusterId, async () => {
  query.namespace = ''
  groupFilter.value = ''
  await loadNamespaces()
  loadCRDs()
  if (tab.value === 'gateway') loadGateway()
})

watch(tab, (value) => {
  if (value === 'gateway' && !gwRoutes.value.length) loadGateway()
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
          CRD 是<strong>运行时从集群里读出来的</strong>，不是平台内置清单 —— 装了什么就能看到什么。
          自定义资源一律<strong>只读</strong>：平台不懂它们的语义，改错一条 Gateway 或 Application 的代价太大。
          详情里<strong>保留 status</strong> —— 自定义资源的 status 往往才是要看的那部分。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select
          v-model="query.namespace"
          placeholder="全部命名空间"
          clearable
          filterable
          style="width: 190px"
          @change="(tab === 'gateway' ? loadGateway() : loadInstances())"
        >
          <el-option v-for="ns in namespaces" :key="ns.name" :label="ns.name" :value="ns.name" />
        </el-select>
        <el-input
          v-model="query.keyword"
          placeholder="CRD 名 / kind"
          clearable
          style="width: 180px"
          @keyup.enter="loadCRDs"
        />
        <el-button @click="loadCRDs">查询</el-button>
        <div class="grow"></div>
        <el-tag type="info">CRD {{ crds.length }}</el-tag>
        <el-tag v-if="notEstablished" type="warning">未就绪 {{ notEstablished }}</el-tag>
        <el-tag v-if="gatewayVersion" type="success">Gateway API {{ gatewayVersion }}</el-tag>
      </div>

      <el-tabs v-model="tab">
        <el-tab-pane label="自定义资源定义" name="crd">
          <el-alert v-if="crdError" type="error" :closable="false" style="margin-bottom: 12px" :title="crdError" />

          <el-radio-group v-model="groupFilter" style="margin-bottom: 12px">
            <el-radio-button value="">全部组</el-radio-button>
            <el-radio-button v-for="g in groups" :key="g" :value="g">{{ g }}</el-radio-button>
          </el-radio-group>

          <el-table
            v-loading="crdLoading"
            :data="filteredCRDs"
            border
            stripe
            empty-text="这个集群里没有 CRD"
          >
            <el-table-column prop="kind" label="Kind" width="180" show-overflow-tooltip />
            <el-table-column prop="group" label="组" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">
                <div>{{ row.group }}</div>
                <el-text v-if="row.knownAs" type="info" size="small">{{ row.knownAs }}</el-text>
              </template>
            </el-table-column>
            <el-table-column label="作用域" width="100">
              <template #default="{ row }">{{ row.namespaced ? '命名空间' : '集群' }}</template>
            </el-table-column>
            <el-table-column label="版本" min-width="160">
              <template #default="{ row }">
                <el-tag
                  v-for="v in row.servedVersions"
                  :key="v"
                  size="small"
                  :type="v === row.storageVersion ? 'success' : 'info'"
                  style="margin-right: 4px"
                >
                  {{ v }}{{ v === row.storageVersion ? '（存储）' : '' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="110">
              <template #default="{ row }">
                <el-tooltip
                  v-if="!row.established"
                  content="CRD 自己的 Established 条件不为 True，查它的实例会失败 —— 那是 CRD 没装好"
                  placement="top"
                >
                  <el-tag size="small" type="danger">未就绪</el-tag>
                </el-tooltip>
                <el-tag v-else size="small" type="success">已就绪</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="短名" min-width="120" show-overflow-tooltip>
              <template #default="{ row }">{{ row.shortNames?.join('、') || '—' }}</template>
            </el-table-column>
            <el-table-column label="操作" width="110" fixed="right">
              <template #default="{ row }">
                <el-button :disabled="!row.established" link type="primary" @click="openInstances(row)">
                  看实例
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-card v-if="crdNotes.length" shadow="never" style="margin-top: 12px">
            <template #header>这一页的边界</template>
            <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
              <li v-for="(note, idx) in crdNotes" :key="idx">{{ note }}</li>
            </ul>
          </el-card>
        </el-tab-pane>

        <el-tab-pane label="Gateway API 路由" name="gateway">
          <el-alert
            v-if="!gwInstalled"
            type="warning"
            :closable="false"
            :title="gwNote"
            style="margin-bottom: 12px"
          />
          <template v-else>
            <el-alert type="info" :closable="false" style="margin-bottom: 12px">
              <template #title>
                这里把 HTTPRoute 的三层嵌套拍平成「什么流量 → 送到哪」。
                HTTPRoute 版本：<strong>{{ gwVersion }}</strong>
              </template>
            </el-alert>
            <el-table v-loading="gwLoading" :data="gwRoutes" border stripe empty-text="没有 HTTPRoute">
              <el-table-column prop="namespace" label="命名空间" width="130" show-overflow-tooltip />
              <el-table-column prop="name" label="HTTPRoute" min-width="150" show-overflow-tooltip />
              <el-table-column label="挂在哪个 Gateway" min-width="190" show-overflow-tooltip>
                <template #default="{ row }">{{ row.gateways?.join('、') || '—（没有 parentRef）' }}</template>
              </el-table-column>
              <el-table-column label="域名" min-width="180" show-overflow-tooltip>
                <template #default="{ row }">{{ row.hostnames?.join('、') || '—（不限域名）' }}</template>
              </el-table-column>
              <el-table-column label="路由规则" min-width="420">
                <template #default="{ row }">
                  <div v-if="!row.rules?.length" style="color: #9ca3af">没有规则</div>
                  <div v-for="(rule, idx) in row.rules" :key="idx" class="rule">
                    <div class="rule-match">{{ rule.matches.join(' 或 ') }}</div>
                    <div class="rule-arrow">→</div>
                    <div class="rule-backend">{{ rule.backends.join('、') }}</div>
                  </div>
                </template>
              </el-table-column>
            </el-table>
            <el-card v-if="gwNotes.length" shadow="never" style="margin-top: 12px">
              <template #header>这一页的边界</template>
              <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
                <li v-for="(note, idx) in gwNotes" :key="idx">{{ note }}</li>
              </ul>
            </el-card>
          </template>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-drawer v-model="instVisible" :title="`${current?.kind} 实例`" size="860px">
      <div class="page-toolbar">
        <el-select v-model="currentVersion" style="width: 160px" @change="loadInstances">
          <el-option v-for="v in current?.servedVersions || []" :key="v" :label="v" :value="v" />
        </el-select>
        <el-text type="info" size="small">{{ current?.name }}</el-text>
        <div class="grow"></div>
        <el-tag type="info">共 {{ instTotal }}</el-tag>
        <el-tag v-if="instTruncated" type="warning">已截断，缩小范围再看</el-tag>
      </div>
      <el-alert v-if="instError" type="error" :closable="false" style="margin-bottom: 12px" :title="instError" />
      <el-alert v-if="instNote" type="info" :closable="false" style="margin-bottom: 12px" :title="instNote" />

      <el-table v-loading="instLoading" :data="instances" border stripe size="small" empty-text="没有实例">
        <el-table-column v-if="current?.namespaced" prop="namespace" label="命名空间" width="140" show-overflow-tooltip />
        <el-table-column prop="name" label="名称" min-width="220" show-overflow-tooltip />
        <el-table-column label="创建时间" width="160">
          <template #default="{ row }">
            {{ row.createdAt && !row.createdAt.startsWith('0001') ? row.createdAt.slice(0, 19).replace('T', ' ') : '—' }}
          </template>
        </el-table-column>
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openYAML(row)">看 YAML</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <el-dialog v-model="yamlVisible" :title="yamlTitle" width="860px" top="5vh">
      <el-alert v-if="yamlNote" type="info" :closable="false" style="margin-bottom: 10px" :title="yamlNote" />
      <pre class="yaml-pane">{{ yamlText }}</pre>
      <template #footer>
        <el-button @click="copyYAML">复制</el-button>
        <el-button @click="yamlVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.rule {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 3px 0;
  font-size: 12px;
  line-height: 1.6;
}
.rule + .rule {
  border-top: 1px dashed var(--el-border-color-lighter);
}
.rule-match {
  flex: 1;
}
.rule-arrow {
  color: var(--el-color-primary);
  flex: none;
}
.rule-backend {
  flex: 1;
  color: var(--el-text-color-regular);
}
.yaml-pane {
  margin: 0;
  padding: 12px;
  max-height: 64vh;
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
