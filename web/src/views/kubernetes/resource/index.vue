<script setup lang="ts">
/**
 * 集群资源浏览（按集群下钻）。
 *
 * 这一页的定位是「排查时能一路点下去」：选集群 → 选类型 → 找到对象 →
 * 看 YAML / 看它自己的事件 / 看它的 Pod。所以详情做成三个页签，而不是只给一段 YAML。
 *
 * 类型分三档，界面上必须看得出来：
 *   可写（apply / 副本数 / 滚动重启）、只读（Pod、ReplicaSet、Node 这些由控制器维护的）、
 *   脱敏只读（Secret 只给键名不给值，也不允许提交回去）。
 * 只读类型的按钮不给出来，而不是点了再报错。
 */
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  applyKubeResource,
  getKubeResource,
  listKubeChangeLogs,
  listKubeClusters,
  listKubeNamespaces,
  listKubeObjectEvents,
  listKubeRelatedPods,
  listKubeResourceKinds,
  listKubeResources,
  restartKubeWorkload,
  scaleKubeResource,
  type KubeChangeLog,
  type KubeCluster,
  type KubeEventItem,
  type KubeResourceItem,
  type KubeResourceKind
} from '@/api'
import Pagination from '@/components/Pagination.vue'

const clusters = ref<KubeCluster[]>([])
const kinds = ref<KubeResourceKind[]>([])
const namespaces = ref<string[]>([])

const clusterId = ref<number>(0)
const kind = ref('Deployment')
const namespace = ref('')
const keyword = ref('')
const tab = ref('resource')

const loading = reactive({ list: false, detail: false, submit: false, log: false, events: false, pods: false })
const items = ref<KubeResourceItem[]>([])
const namespaceIgnored = ref(false)
const currentCluster = computed(() => clusters.value.find((c) => c.id === clusterId.value))
const currentKind = computed(() => kinds.value.find((k) => k.kind === kind.value))

// 类型按分组展示：一个 18 项的平铺下拉找起来很费劲
const kindGroups = computed(() => {
  const groups = new Map<string, KubeResourceKind[]>()
  for (const item of kinds.value) {
    const list = groups.get(item.group) || []
    list.push(item)
    groups.set(item.group, list)
  }
  return [...groups.entries()].map(([name, list]) => ({ name, list }))
})

// 列表分页放在前端：API Server 的分页是 continue token 式的，
// 和「跳到第 5 页」对不上，一次取完再本地翻页语义更直白
const page = ref(1)
const pageSize = ref(20)
const pagedItems = computed(() =>
  items.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value)
)

// ---------- 详情抽屉：YAML / 事件 / 关联 Pod ----------
const detail = reactive({
  visible: false,
  tab: 'yaml',
  kind: '',
  namespace: '',
  name: '',
  yaml: '',
  original: '',
  hint: '',
  readOnly: false,
  redacted: false,
  restartable: false,
  podSelector: '',
  force: false,
  result: '',
  resultType: 'success' as 'success' | 'warning'
})
const dirty = computed(() => detail.yaml !== detail.original)
const events = ref<KubeEventItem[]>([])
const eventNote = ref('')
const relatedPods = ref<KubeResourceItem[]>([])
const podNote = ref('')

// ---------- 副本数对话框 ----------
const scaler = reactive({
  visible: false,
  kind: '',
  namespace: '',
  name: '',
  current: 0,
  replicas: 1,
  result: ''
})

// ---------- 改动留痕 ----------
const logQuery = reactive({ page: 1, pageSize: 20, action: '', status: '', realOnly: false })
const logs = ref<KubeChangeLog[]>([])
const logTotal = ref(0)

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

async function loadResources() {
  if (!clusterId.value) return
  loading.list = true
  try {
    const res = await listKubeResources(clusterId.value, kind.value, {
      ...(namespace.value ? { namespace: namespace.value } : {}),
      ...(keyword.value.trim() ? { keyword: keyword.value.trim() } : {})
    })
    items.value = res.items || []
    namespaceIgnored.value = !!res.namespaceIgnored
    page.value = 1
  } catch (err: any) {
    items.value = []
    ElMessage.error(err?.message || '读取资源失败')
  } finally {
    loading.list = false
  }
}

async function loadLogs() {
  loading.log = true
  try {
    const res = await listKubeChangeLogs({
      page: logQuery.page,
      pageSize: logQuery.pageSize,
      ...(clusterId.value ? { clusterId: clusterId.value } : {}),
      ...(logQuery.action ? { action: logQuery.action } : {}),
      ...(logQuery.status ? { status: logQuery.status } : {}),
      ...(logQuery.realOnly ? { realOnly: 'true' } : {})
    })
    logs.value = res.list || []
    logTotal.value = res.total
  } catch (err: any) {
    ElMessage.error(err?.message || '读取改动留痕失败')
  } finally {
    loading.log = false
  }
}

function searchLogs() {
  logQuery.page = 1
  loadLogs()
}

async function openDetail(row: KubeResourceItem) {
  detail.visible = true
  detail.tab = 'yaml'
  detail.kind = row.kind
  detail.namespace = row.namespace
  detail.name = row.name
  detail.yaml = ''
  detail.original = ''
  detail.hint = ''
  detail.force = false
  detail.result = ''
  detail.readOnly = !!currentKind.value?.readOnly
  detail.redacted = !!currentKind.value?.redacted
  detail.restartable = !!currentKind.value?.restartable
  detail.podSelector = ''
  events.value = []
  relatedPods.value = []
  loading.detail = true
  try {
    const res = await getKubeResource(clusterId.value, row.kind, row.namespace, row.name)
    detail.yaml = res.yaml
    detail.original = res.yaml
    detail.hint = res.hint
    detail.readOnly = res.readOnly
    detail.redacted = res.redacted
    detail.restartable = res.restartable
    detail.podSelector = res.podSelector
  } catch (err: any) {
    ElMessage.error(err?.message || '读取对象失败')
  } finally {
    loading.detail = false
  }
}

async function loadEvents() {
  loading.events = true
  try {
    const res = await listKubeObjectEvents(clusterId.value, detail.kind, detail.namespace, detail.name)
    events.value = res.items || []
    eventNote.value = res.note
  } catch (err: any) {
    events.value = []
    ElMessage.error(err?.message || '读取事件失败')
  } finally {
    loading.events = false
  }
}

async function loadRelatedPods() {
  loading.pods = true
  try {
    const res = await listKubeRelatedPods(clusterId.value, detail.kind, detail.namespace, detail.name)
    relatedPods.value = res.items || []
    podNote.value = res.note
  } catch (err: any) {
    relatedPods.value = []
    ElMessage.error(err?.message || '读取关联 Pod 失败')
  } finally {
    loading.pods = false
  }
}

// 页签是懒加载的：一次点开就发三个请求太浪费，尤其事件那条在大集群上不快
watch(
  () => detail.tab,
  (value) => {
    if (!detail.visible) return
    if (value === 'events' && !events.value.length) loadEvents()
    if (value === 'pods' && !relatedPods.value.length) loadRelatedPods()
  }
)

async function submitApply(dryRun: boolean) {
  if (!detail.yaml.trim()) {
    ElMessage.warning('YAML 不能为空')
    return
  }
  if (!dryRun) {
    try {
      await ElMessageBox.confirm(
        `确认把这段 YAML 应用到集群「${currentCluster.value?.name}」的 ${detail.kind}/${detail.name}？`,
        '应用到集群',
        { type: 'warning', confirmButtonText: '应用', cancelButtonText: '再想想' }
      )
    } catch {
      return
    }
  }
  loading.submit = true
  try {
    const res = await applyKubeResource(clusterId.value, {
      yaml: detail.yaml,
      dryRun,
      force: detail.force
    })
    detail.result = res.detail
    detail.resultType = dryRun ? 'warning' : 'success'
    if (!dryRun) {
      detail.original = detail.yaml
      ElMessage.success(res.detail)
      loadResources()
      loadLogs()
    }
  } catch (err: any) {
    detail.result = ''
    ElMessage.error(err?.message || '提交失败')
  } finally {
    loading.submit = false
  }
}

function openScaler(row: KubeResourceItem) {
  scaler.visible = true
  scaler.kind = row.kind
  scaler.namespace = row.namespace
  scaler.name = row.name
  scaler.result = ''
  // 摘要形如「副本 1/3」，把期望值拿来当默认目标
  const matched = /(\d+)\s*\/\s*(\d+)/.exec(row.summary)
  scaler.current = matched ? Number(matched[2]) : 0
  scaler.replicas = scaler.current
}

async function submitScale(dryRun: boolean) {
  loading.submit = true
  try {
    const res = await scaleKubeResource(clusterId.value, {
      kind: scaler.kind,
      namespace: scaler.namespace,
      name: scaler.name,
      replicas: scaler.replicas,
      dryRun
    })
    scaler.result = res.detail
    if (!dryRun) {
      ElMessage.success(res.detail)
      scaler.visible = false
      loadResources()
      loadLogs()
    }
  } catch (err: any) {
    scaler.result = ''
    ElMessage.error(err?.message || '改副本数失败')
  } finally {
    loading.submit = false
  }
}

/**
 * 滚动重启。确认文案要说清它到底做了什么：
 * 不是「原地重启容器」，而是给 Pod 模板打时间戳让控制器把 Pod 换一批 ——
 * 副本只有 1 且没配就绪探针时，这个过程是有损的。
 */
async function restart(row: KubeResourceItem) {
  try {
    await ElMessageBox.confirm(
      `给集群「${currentCluster.value?.name}」的 ${row.kind}/${row.name} 触发滚动重启？\n\n` +
        '做法与 kubectl rollout restart 相同：给 Pod 模板打一个时间戳注解，由控制器逐步用新 Pod 替换旧 Pod。' +
        '这不是原地重启容器；副本数为 1 或没有就绪探针时会有短暂不可用。',
      '滚动重启',
      { type: 'warning', confirmButtonText: '重启', cancelButtonText: '取消' }
    )
  } catch {
    return
  }
  loading.submit = true
  try {
    const res = await restartKubeWorkload(clusterId.value, {
      kind: row.kind,
      namespace: row.namespace,
      name: row.name,
      dryRun: false
    })
    ElMessage.success(res.detail)
    loadResources()
    loadLogs()
  } catch (err: any) {
    ElMessage.error(err?.message || '滚动重启失败')
  } finally {
    loading.submit = false
  }
}

function showPayload(row: KubeChangeLog) {
  ElMessageBox.alert(row.payload || '（无）', `${row.action} ${row.kind}/${row.name}`, {
    customClass: 'payload-box',
    confirmButtonText: '关闭'
  })
}

function eventTagType(type: string) {
  return type === 'Warning' ? 'warning' : 'info'
}

watch(clusterId, async () => {
  namespace.value = ''
  await loadNamespaces()
  loadResources()
  searchLogs()
})
watch([kind, namespace], loadResources)

onMounted(async () => {
  kinds.value = await listKubeResourceKinds()
  await loadClusters()
  if (clusterId.value) {
    await loadNamespaces()
    await loadResources()
  }
  loadLogs()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="kind" filterable style="width: 200px">
          <el-option-group v-for="group in kindGroups" :key="group.name" :label="group.name">
            <el-option v-for="item in group.list" :key="item.kind" :label="item.kind" :value="item.kind">
              <span>{{ item.kind }}</span>
              <span v-if="item.redacted" class="opt-tag">脱敏只读</span>
              <span v-else-if="item.readOnly" class="opt-tag">只读</span>
            </el-option>
          </el-option-group>
        </el-select>
        <el-select
          v-model="namespace"
          placeholder="全部命名空间"
          clearable
          filterable
          :disabled="currentKind && !currentKind.namespaced"
          style="width: 190px"
        >
          <el-option v-for="name in namespaces" :key="name" :label="name" :value="name" />
        </el-select>
        <el-input
          v-model="keyword"
          placeholder="按名称过滤"
          clearable
          style="width: 180px"
          @keyup.enter="loadResources"
          @clear="loadResources"
        />
        <el-button @click="loadResources">刷新</el-button>
        <span class="tip">
          只开放白名单里的 {{ kinds.length }} 种类型，不提供删除；只读类型（Pod / ReplicaSet / Node 等）
          由控制器维护，改了也会被改回去，因此不给编辑入口
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
          v-if="namespaceIgnored"
          type="info"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`${kind} 是集群级对象，没有命名空间，已忽略命名空间筛选`"
        />

        <el-tabs v-model="tab" @tab-change="tab === 'log' && loadLogs()">
          <el-tab-pane label="资源" name="resource">
            <el-table v-loading="loading.list" :data="pagedItems" border stripe>
              <el-table-column
                v-if="!currentKind || currentKind.namespaced"
                prop="namespace"
                label="命名空间"
                width="150"
              />
              <el-table-column prop="name" label="名称" min-width="220" show-overflow-tooltip />
              <el-table-column prop="summary" label="概况" min-width="200" show-overflow-tooltip />
              <el-table-column prop="createdAt" label="创建时间" min-width="180" />
              <el-table-column label="操作" width="210" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="openDetail(row)">详情</el-button>
                  <el-button
                    v-if="currentKind?.scalable"
                    v-perm="'kube:write'"
                    link
                    type="primary"
                    @click="openScaler(row)"
                  >
                    副本数
                  </el-button>
                  <el-button
                    v-if="currentKind?.restartable"
                    v-perm="'kube:write'"
                    link
                    type="warning"
                    @click="restart(row)"
                  >
                    重启
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
            <Pagination
              v-model:current-page="page"
              v-model:page-size="pageSize"
              :total="items.length"
            />
          </el-tab-pane>

          <el-tab-pane label="改动留痕" name="log">
            <div class="page-toolbar">
              <el-select v-model="logQuery.action" placeholder="全部动作" clearable style="width: 140px">
                <el-option label="apply" value="apply" />
                <el-option label="scale" value="scale" />
                <el-option label="restart" value="restart" />
              </el-select>
              <el-select v-model="logQuery.status" placeholder="全部结果" clearable style="width: 140px">
                <el-option label="成功" value="success" />
                <el-option label="失败" value="failed" />
              </el-select>
              <el-checkbox v-model="logQuery.realOnly">只看真改过的（排除预检）</el-checkbox>
              <el-button type="primary" @click="searchLogs">查询</el-button>
            </div>
            <el-table v-loading="loading.log" :data="logs" border stripe>
              <el-table-column prop="createdAt" label="时间" min-width="170" />
              <el-table-column prop="username" label="操作人" width="110" />
              <el-table-column prop="clusterName" label="集群" width="130" />
              <el-table-column label="对象" min-width="220" show-overflow-tooltip>
                <template #default="{ row }">
                  {{ row.kind }}/{{ row.name }}
                  <span style="color: var(--el-text-color-secondary)">（{{ row.namespace }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="动作" width="150">
                <template #default="{ row }">
                  <el-tag size="small">{{ row.action }}</el-tag>
                  <el-tag v-if="row.dryRun" size="small" type="info" style="margin-left: 4px">预检</el-tag>
                  <el-tag v-if="row.forced" size="small" type="warning" style="margin-left: 4px">强制</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="结果" width="90">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
                    {{ row.status === 'success' ? '成功' : '失败' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="detail" label="详情" min-width="240" show-overflow-tooltip />
              <el-table-column label="提交内容" width="100" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="showPayload(row)">查看</el-button>
                </template>
              </el-table-column>
            </el-table>
            <Pagination
              v-model:current-page="logQuery.page"
              v-model:page-size="logQuery.pageSize"
              :total="logTotal"
              @change="loadLogs"
            />
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-card>

    <el-drawer v-model="detail.visible" size="62%" :title="`${detail.kind}/${detail.name}`">
      <el-tabs v-model="detail.tab">
        <el-tab-pane label="YAML" name="yaml">
          <div v-loading="loading.detail">
            <el-alert
              v-if="detail.hint"
              :type="detail.redacted ? 'warning' : detail.readOnly ? 'info' : 'info'"
              :closable="false"
              :title="detail.hint"
            />
            <el-input
              v-model="detail.yaml"
              type="textarea"
              :rows="22"
              :readonly="detail.readOnly"
              spellcheck="false"
              class="yaml-editor"
              style="margin-top: 12px"
            />
            <el-alert
              v-if="detail.result"
              :type="detail.resultType"
              :closable="false"
              :title="detail.result"
              style="margin-top: 12px"
            />
          </div>
        </el-tab-pane>

        <el-tab-pane label="事件" name="events">
          <el-alert v-if="eventNote" type="info" :closable="false" :title="eventNote" style="margin-bottom: 8px" />
          <el-table
            v-loading="loading.events"
            :data="events"
            border
            stripe
            size="small"
            empty-text="最近没有与这个对象相关的事件"
          >
            <el-table-column label="类型" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="eventTagType(row.type)">{{ row.type }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="reason" label="原因" width="150" />
            <el-table-column prop="message" label="消息" min-width="300" show-overflow-tooltip />
            <el-table-column prop="count" label="次数" width="70" align="center" />
            <el-table-column prop="lastSeen" label="最近一次" min-width="170" />
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="关联 Pod" name="pods">
          <el-alert v-if="podNote" type="info" :closable="false" :title="podNote" style="margin-bottom: 8px" />
          <el-table
            v-loading="loading.pods"
            :data="relatedPods"
            border
            stripe
            size="small"
            empty-text="按标签选择器没有匹配到 Pod"
          >
            <el-table-column prop="name" label="Pod" min-width="220" show-overflow-tooltip />
            <el-table-column prop="summary" label="状态" min-width="220" show-overflow-tooltip />
            <el-table-column prop="createdAt" label="创建时间" min-width="170" />
          </el-table>
        </el-tab-pane>
      </el-tabs>

      <template #footer>
        <div class="drawer-footer">
          <el-checkbox v-if="!detail.readOnly" v-model="detail.force">
            强制接管字段（字段冲突时用，会把别人管着的字段改成这份 YAML 的值）
          </el-checkbox>
          <span v-else class="tip">这个类型在平台上只读</span>
          <div>
            <el-button @click="detail.visible = false">关闭</el-button>
            <template v-if="!detail.readOnly">
              <el-button v-perm="'kube:write'" :loading="loading.submit" @click="submitApply(true)">
                预检
              </el-button>
              <el-button
                v-perm="'kube:write'"
                type="primary"
                :disabled="!dirty"
                :loading="loading.submit"
                @click="submitApply(false)"
              >
                应用到集群
              </el-button>
            </template>
          </div>
        </div>
      </template>
    </el-drawer>

    <el-dialog v-model="scaler.visible" width="440px" :title="`改副本数：${scaler.kind}/${scaler.name}`">
      <el-form label-width="100px">
        <el-form-item label="命名空间">{{ scaler.namespace }}</el-form-item>
        <el-form-item label="目标副本数">
          <el-input-number v-model="scaler.replicas" :min="0" :max="200" />
        </el-form-item>
      </el-form>
      <el-alert
        v-if="scaler.result"
        type="warning"
        :closable="false"
        :title="scaler.result"
        style="margin-bottom: 8px"
      />
      <template #footer>
        <el-button @click="scaler.visible = false">取消</el-button>
        <el-button v-perm="'kube:write'" :loading="loading.submit" @click="submitScale(true)">
          预检
        </el-button>
        <el-button
          v-perm="'kube:write'"
          type="primary"
          :loading="loading.submit"
          @click="submitScale(false)"
        >
          执行
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.tip {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.opt-tag {
  margin-left: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.yaml-editor :deep(textarea) {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.6;
}
.drawer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
</style>
