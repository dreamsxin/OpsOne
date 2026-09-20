<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  closeKubeForward,
  createKubeForward,
  listKubeClusters,
  listKubeForwards,
  listKubeNamespaces,
  listKubePods,
  listKubeResources,
  type KubeCluster,
  type KubeForward,
  type KubeForwardLimits
} from '@/api'

const clusters = ref<KubeCluster[]>([])
const rows = ref<KubeForward[]>([])
const total = ref(0)
const loading = ref(false)
const limits = ref<KubeForwardLimits>({
  bind: '', portMin: 0, portMax: 0, maxTunnels: 0, ttlMinutes: 0, connMax: 0, running: 0
})

const query = reactive({ page: 1, pageSize: 20, status: '', clusterId: '' as number | '' })

// 运行中的隧道有实时计数，页面每 5 秒刷一次
let timer: number | null = null

const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  running: { text: '运行中', type: 'success' },
  stopped: { text: '已关闭', type: 'info' },
  error: { text: '异常', type: 'danger' }
}

async function load() {
  loading.value = true
  try {
    const data = await listKubeForwards(query)
    rows.value = data.list || []
    total.value = data.total
    limits.value = data.limits
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

// ---------- 新建隧道 ----------

const form = reactive({
  visible: false,
  submitting: false,
  clusterId: 0,
  namespace: '',
  targetKind: 'service' as 'pod' | 'service',
  targetName: '',
  targetPort: 80,
  listenPort: undefined as number | undefined,
  ttlMinutes: 0
})
const namespaces = ref<string[]>([])
const targets = ref<{ name: string; summary: string }[]>([])
const targetLoading = ref(false)

const usableClusters = computed(() => clusters.value.filter((c) => c.enabled))

async function openForm() {
  form.visible = true
  form.clusterId = usableClusters.value[0]?.id || 0
  form.namespace = ''
  form.targetKind = 'service'
  form.targetName = ''
  form.targetPort = 80
  form.listenPort = undefined
  form.ttlMinutes = limits.value.ttlMinutes
  targets.value = []
  await loadNamespaces()
}

async function loadNamespaces() {
  namespaces.value = []
  if (!form.clusterId) return
  try {
    const list = await listKubeNamespaces(form.clusterId)
    namespaces.value = list.map((n) => n.name)
    form.namespace = namespaces.value.includes('default') ? 'default' : namespaces.value[0] || ''
    await loadTargets()
  } catch (err: any) {
    ElMessage.error(err?.message || '读取命名空间失败')
  }
}

// 目标列表只是个下拉助手，拿不到也能手填
async function loadTargets() {
  targets.value = []
  form.targetName = ''
  if (!form.clusterId || !form.namespace) return
  targetLoading.value = true
  try {
    if (form.targetKind === 'pod') {
      const data = await listKubePods(form.clusterId, form.namespace)
      targets.value = (data.items || []).map((p) => ({
        name: p.name,
        summary: `${p.phase} · ${p.ready}`
      }))
    } else {
      const data = await listKubeResources(form.clusterId, 'Service', form.namespace)
      targets.value = (data.items || []).map((s) => ({ name: s.name, summary: s.summary }))
    }
  } catch (err: any) {
    ElMessage.warning('读取目标列表失败，可以直接手填名称：' + (err?.message || ''))
  } finally {
    targetLoading.value = false
  }
}

async function submit() {
  if (!form.clusterId || !form.namespace || !form.targetName) {
    ElMessage.warning('集群、命名空间与目标都要填')
    return
  }
  form.submitting = true
  try {
    const res = await createKubeForward({
      clusterId: form.clusterId,
      namespace: form.namespace,
      targetKind: form.targetKind,
      targetName: form.targetName,
      targetPort: form.targetPort,
      listenPort: form.listenPort || undefined,
      ttlMinutes: form.ttlMinutes || undefined
    })
    form.visible = false
    ElMessage.success(`隧道已开：${res.listenAddr}:${res.listenPort}`)
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '开隧道失败')
  } finally {
    form.submitting = false
  }
}

async function close(row: KubeForward) {
  await ElMessageBox.confirm(
    `关闭隧道 ${row.listenAddr}:${row.listenPort}？正在使用这个端口的连接会断开。`,
    '关闭隧道',
    { type: 'warning' }
  )
  const res = await closeKubeForward(row.id)
  if (res.closed) {
    ElMessage.success('已关闭')
  } else {
    ElMessage.info(res.reason || '隧道已不在运行')
  }
  load()
}

function copyAddr(row: KubeForward) {
  const text = `${row.listenAddr}:${row.listenPort}`
  navigator.clipboard?.writeText(text).then(
    () => ElMessage.success('已复制 ' + text),
    () => ElMessage.warning('浏览器不允许自动复制，请手动选中：' + text)
  )
}

function formatBytes(n: number) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = n
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

function remain(row: KubeForward) {
  if (row.status !== 'running') return '-'
  const ms = new Date(row.expiresAt).getTime() - Date.now()
  if (ms <= 0) return '即将关闭'
  const min = Math.floor(ms / 60000)
  if (min < 60) return `${min} 分钟`
  return `${Math.floor(min / 60)} 小时 ${min % 60} 分`
}

onMounted(async () => {
  clusters.value = await listKubeClusters()
  await load()
  timer = window.setInterval(load, 5000)
})
onBeforeUnmount(() => {
  if (timer !== null) window.clearInterval(timer)
})

watch(() => form.targetKind, loadTargets)
watch(() => form.namespace, loadTargets)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" show-icon class="notice">
        把集群里的服务端口临时映射到平台上，相当于把 kubectl port-forward 挪到平台来跑，不用给每个人发 kubeconfig。
        <strong>隧道端口本身不做认证</strong>：能连到 {{ limits.bind }}:{{ limits.portMin }}-{{ limits.portMax }}
        的人就等同于能访问被转发的服务，用完请及时关闭（到 {{ limits.ttlMinutes }} 分钟会自动关）。
        当前 {{ limits.running }}/{{ limits.maxTunnels }} 条，单条隧道最多 {{ limits.connMax }} 个并发连接。
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.clusterId" placeholder="全部集群" clearable style="width: 180px" @change="search">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="query.status" placeholder="全部状态" clearable style="width: 140px" @change="search">
          <el-option label="运行中" value="running" />
          <el-option label="已关闭" value="stopped" />
        </el-select>
        <el-button @click="load">刷新</el-button>
        <div class="flex-1" />
        <el-button v-perm="'kube:forward'" type="primary" @click="openForm">开隧道</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" style="width: 100%">
        <el-table-column label="监听" min-width="190">
          <template #default="{ row }">
            <el-button link type="primary" @click="copyAddr(row)">
              {{ row.listenAddr }}:{{ row.listenPort }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column label="目标" min-width="260">
          <template #default="{ row }">
            <div>{{ row.clusterName }} / {{ row.namespace }}</div>
            <div class="sub">
              <el-tag size="small" :type="row.targetKind === 'service' ? 'success' : 'info'">
                {{ row.targetKind === 'service' ? 'Service' : 'Pod' }}
              </el-tag>
              {{ row.targetName }}:{{ row.targetPort }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag :type="statusMeta[row.status]?.type || 'info'" size="small">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="连接" width="140">
          <template #default="{ row }">
            <div>活跃 {{ row.connActive }} / 累计 {{ row.connTotal }}</div>
            <div v-if="row.connFailed" class="sub danger">失败 {{ row.connFailed }}</div>
          </template>
        </el-table-column>
        <el-table-column label="流量（去 / 回）" width="170">
          <template #default="{ row }">{{ formatBytes(row.bytesIn) }} / {{ formatBytes(row.bytesOut) }}</template>
        </el-table-column>
        <el-table-column label="剩余时长" width="120">
          <template #default="{ row }">{{ remain(row) }}</template>
        </el-table-column>
        <el-table-column label="开启人" width="120" prop="username" />
        <el-table-column label="说明" min-width="200">
          <template #default="{ row }">
            <span :class="{ danger: row.status !== 'running' }">{{ row.errorMsg || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'running'"
              v-perm="'kube:forward'"
              link
              type="danger"
              @click="close(row)"
            >
              关闭
            </el-button>
            <span v-else>-</span>
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

    <el-dialog v-model="form.visible" title="开一条转发隧道" width="560px">
      <el-form label-width="110px">
        <el-form-item label="集群">
          <el-select v-model="form.clusterId" style="width: 100%" @change="loadNamespaces">
            <el-option v-for="item in usableClusters" :key="item.id" :label="item.name" :value="item.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="命名空间">
          <el-select v-model="form.namespace" filterable style="width: 100%">
            <el-option v-for="item in namespaces" :key="item" :label="item" :value="item" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标类型">
          <el-radio-group v-model="form.targetKind">
            <el-radio-button value="service">Service</el-radio-button>
            <el-radio-button value="pod">Pod</el-radio-button>
          </el-radio-group>
          <div class="hint">
            选 Service 时每条连接都会重新解析后端 Pod，Pod 重建或扩缩容之后隧道还能继续用；
            选 Pod 则钉死在这一个 Pod 上，它被重建后隧道就废了。
          </div>
        </el-form-item>
        <el-form-item label="目标名称">
          <el-select
            v-model="form.targetName"
            :loading="targetLoading"
            filterable
            allow-create
            default-first-option
            placeholder="可选可填"
            style="width: 100%"
          >
            <el-option v-for="item in targets" :key="item.name" :label="item.name" :value="item.name">
              <span>{{ item.name }}</span>
              <span class="option-sub">{{ item.summary }}</span>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item label="目标端口">
          <el-input-number v-model="form.targetPort" :min="1" :max="65535" />
          <span class="hint inline">
            {{ form.targetKind === 'service' ? 'Service 上开的端口，平台会换算成 Pod 端口' : 'Pod 容器监听的端口' }}
          </span>
        </el-form-item>
        <el-form-item label="监听端口">
          <el-input-number v-model="form.listenPort" :min="limits.portMin" :max="limits.portMax" />
          <span class="hint inline">留空自动挑，可用区间 {{ limits.portMin }}-{{ limits.portMax }}</span>
        </el-form-item>
        <el-form-item label="存活时长">
          <el-input-number v-model="form.ttlMinutes" :min="1" :max="limits.ttlMinutes" />
          <span class="hint inline">分钟，最长 {{ limits.ttlMinutes }}，到点自动关闭</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="form.visible = false">取消</el-button>
        <el-button type="primary" :loading="form.submitting" @click="submit">开启</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
}
.flex-1 {
  flex: 1;
}
.sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.danger {
  color: var(--el-color-danger);
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.hint.inline {
  margin-left: 8px;
}
.option-sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-left: 8px;
}
</style>
