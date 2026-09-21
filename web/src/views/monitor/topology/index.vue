<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createTopology,
  createTopologyEdge,
  createTopologyNode,
  deleteTopology,
  deleteTopologyEdge,
  deleteTopologyNode,
  getTopology,
  listTopologies,
  listTopologyResources,
  saveTopologyLayout,
  type BindableResource,
  type TopologyDetail,
  type TopologyNode,
  type TopologyNodeKind,
  type TopologySummary
} from '@/api'

const NODE_W = 132
const NODE_H = 52

const loading = ref(false)
const topologies = ref<TopologySummary[]>([])
const currentId = ref<number>(0)
const detail = ref<TopologyDetail | null>(null)
const resources = ref<Record<string, BindableResource[]>>({})

/** 待保存的坐标：拖动只改内存，攒够了一次提交 */
const layoutDirty = ref(false)
/** 连线模式下已选中的起点 */
const linkFrom = ref<number>(0)

const healthText: Record<string, string> = {
  normal: '正常',
  warning: '需关注',
  error: '异常',
  unknown: '未知'
}
const healthColor: Record<string, string> = {
  normal: '#67c23a',
  warning: '#e6a23c',
  error: '#f56c6c',
  unknown: '#909399'
}
const kindText: Record<TopologyNodeKind, string> = {
  host: '主机',
  database: '数据库',
  probe: '拨测',
  certificate: '证书',
  external: '外部依赖'
}

const nodes = computed(() => detail.value?.nodes || [])
const edges = computed(() => detail.value?.edges || [])
const nodeById = computed(() => {
  const map: Record<number, TopologyNode> = {}
  nodes.value.forEach((n) => (map[n.id] = n))
  return map
})

async function loadList() {
  topologies.value = await listTopologies()
  if (!currentId.value && topologies.value.length) currentId.value = topologies.value[0].id
  if (currentId.value) await loadDetail()
}

async function loadDetail() {
  if (!currentId.value) {
    detail.value = null
    return
  }
  loading.value = true
  try {
    detail.value = await getTopology(currentId.value)
    layoutDirty.value = false
    linkFrom.value = 0
  } finally {
    loading.value = false
  }
}

function switchTopology(id: number) {
  currentId.value = id
  loadDetail()
}

// ---------- 拓扑本体 ----------

const topoDialog = reactive({ visible: false, name: '', remark: '' })

async function submitTopology() {
  if (!topoDialog.name.trim()) {
    ElMessage.warning('请填写拓扑名称')
    return
  }
  const created = await createTopology({ name: topoDialog.name.trim(), remark: topoDialog.remark })
  topoDialog.visible = false
  topoDialog.name = ''
  topoDialog.remark = ''
  currentId.value = created.id
  await loadList()
  ElMessage.success('拓扑已创建')
}

async function removeTopology(row: TopologySummary) {
  await ElMessageBox.confirm(`删除拓扑「${row.name}」？其节点与连线会一起删掉。`, '确认', {
    type: 'warning'
  })
  const res = await deleteTopology(row.id)
  if (currentId.value === row.id) currentId.value = 0
  await loadList()
  ElMessage.success(res.detail)
}

// ---------- 节点 ----------

const nodeDialog = reactive({
  visible: false,
  kind: 'host' as TopologyNodeKind,
  refId: undefined as number | undefined,
  name: '',
  remark: ''
})

function openNodeDialog() {
  nodeDialog.visible = true
  nodeDialog.kind = 'host'
  nodeDialog.refId = undefined
  nodeDialog.name = ''
  nodeDialog.remark = ''
}

async function submitNode() {
  // 新节点按已有数量错开摆放，避免全叠在左上角
  const index = nodes.value.length
  try {
    await createTopologyNode(currentId.value, {
      kind: nodeDialog.kind,
      refId: nodeDialog.kind === 'external' ? 0 : nodeDialog.refId || 0,
      name: nodeDialog.name.trim(),
      remark: nodeDialog.remark,
      x: 40 + (index % 4) * 180,
      y: 40 + Math.floor(index / 4) * 110
    })
  } catch (err: any) {
    ElMessage.error(err?.message || '添加节点失败')
    return
  }
  nodeDialog.visible = false
  await loadDetail()
  await loadList()
  ElMessage.success('节点已添加')
}

async function removeNode(node: TopologyNode) {
  await ElMessageBox.confirm(`删除节点「${node.name}」？挂在它上面的连线会一起删掉。`, '确认', {
    type: 'warning'
  })
  const res = await deleteTopologyNode(currentId.value, node.id)
  await loadDetail()
  await loadList()
  ElMessage.success(res.detail)
}

// ---------- 连线 ----------

function onNodeClick(node: TopologyNode) {
  if (!linking.value) return
  if (!linkFrom.value) {
    linkFrom.value = node.id
    return
  }
  if (linkFrom.value === node.id) {
    linkFrom.value = 0
    return
  }
  submitEdge(linkFrom.value, node.id)
}

const linking = ref(false)

function toggleLinking() {
  linking.value = !linking.value
  linkFrom.value = 0
}

async function submitEdge(fromNodeId: number, toNodeId: number) {
  try {
    const res = await createTopologyEdge(currentId.value, { fromNodeId, toNodeId })
    if (res.cycle && res.cycle.length) {
      ElMessage.warning(`连线已建立，但依赖成环：${res.cycle.join(' → ')}`)
    } else {
      ElMessage.success('连线已建立')
    }
  } catch (err: any) {
    ElMessage.error(err?.message || '连线失败')
  } finally {
    linkFrom.value = 0
    await loadDetail()
    await loadList()
  }
}

async function removeEdge(edgeId: number) {
  await ElMessageBox.confirm('删除这条连线？', '确认', { type: 'warning' })
  await deleteTopologyEdge(currentId.value, edgeId)
  await loadDetail()
  await loadList()
  ElMessage.success('连线已删除')
}

// ---------- 拖动与布局 ----------

let dragging: { id: number; offsetX: number; offsetY: number } | null = null

/** 画布视窗：空表示 1:1 原始坐标，「适配窗口」会算出一个 viewBox 把全图缩进来 */
const viewBox = ref('')
const canvasWrap = ref<HTMLElement>()

/**
 * 屏幕坐标 → 画布坐标。
 *
 * 设了 viewBox 之后 SVG 内部坐标和屏幕像素不再 1:1，拖动必须换算，
 * 否则缩放状态下节点会跟着鼠标"飘"。preserveAspectRatio 用的是 xMinYMin meet，
 * 所以只有统一缩放、没有居中偏移。
 */
function toCanvasPoint(event: MouseEvent, el: Element) {
  const box = el.getBoundingClientRect()
  const px = event.clientX - box.left
  const py = event.clientY - box.top
  if (!viewBox.value) return { x: px, y: py }
  const [vx, vy, vw, vh] = viewBox.value.split(' ').map(Number)
  const scale = Math.min(box.width / vw, box.height / vh) || 1
  return { x: vx + px / scale, y: vy + py / scale }
}

function onNodeMouseDown(node: TopologyNode, event: MouseEvent) {
  if (linking.value) return
  const svg = (event.currentTarget as SVGElement).ownerSVGElement
  if (!svg) return
  const point = toCanvasPoint(event, svg)
  dragging = { id: node.id, offsetX: point.x - node.x, offsetY: point.y - node.y }
}

function onCanvasMouseMove(event: MouseEvent) {
  if (!dragging || !detail.value) return
  const node = nodeById.value[dragging.id]
  if (!node) return
  const point = toCanvasPoint(event, event.currentTarget as SVGElement)
  node.x = Math.max(0, Math.round(point.x - dragging.offsetX))
  node.y = Math.max(0, Math.round(point.y - dragging.offsetY))
  layoutDirty.value = true
}

function onCanvasMouseUp() {
  dragging = null
}

/**
 * 自动布局：按依赖关系分层（Kahn 拓扑排序），被依赖的排左边、依赖方排右边，同层竖着排开。
 *
 * 成环的节点排不出层号，统一丢到最后一层——图仍然能看，且与「依赖成环」提示对得上。
 * 只改内存坐标，要落库仍然得点「保存布局」。
 */
function autoLayout() {
  if (!nodes.value.length) return
  const indeg: Record<number, number> = {}
  const next: Record<number, number[]> = {}
  nodes.value.forEach((n) => {
    indeg[n.id] = 0
    next[n.id] = []
  })
  edges.value.forEach((e) => {
    if (indeg[e.toNodeId] === undefined || next[e.fromNodeId] === undefined) return
    indeg[e.toNodeId] += 1
    next[e.fromNodeId].push(e.toNodeId)
  })

  const layer: Record<number, number> = {}
  let queue = nodes.value.filter((n) => indeg[n.id] === 0).map((n) => n.id)
  queue.forEach((id) => (layer[id] = 0))
  let guard = 0
  while (queue.length && guard++ < 1000) {
    const nextQueue: number[] = []
    for (const id of queue) {
      for (const to of next[id]) {
        indeg[to] -= 1
        layer[to] = Math.max(layer[to] ?? 0, (layer[id] ?? 0) + 1)
        if (indeg[to] === 0) nextQueue.push(to)
      }
    }
    queue = nextQueue
  }

  const maxLayer = Math.max(0, ...Object.values(layer))
  const buckets: Record<number, TopologyNode[]> = {}
  for (const node of nodes.value) {
    const idx = layer[node.id] ?? maxLayer + 1 // 成环的放最后一层
    buckets[idx] = buckets[idx] || []
    buckets[idx].push(node)
  }
  Object.keys(buckets).forEach((key) => {
    const col = Number(key)
    buckets[col].forEach((node, row) => {
      node.x = 40 + col * (NODE_W + 90)
      node.y = 30 + row * (NODE_H + 34)
    })
  })
  layoutDirty.value = true
  ElMessage.success('已按依赖关系重排，确认效果后点「保存布局」')
}

/** 适配窗口：按节点包围盒算 viewBox，节点多到画布外时一眼能看全 */
function fitView() {
  if (!nodes.value.length) return
  const xs = nodes.value.map((n) => n.x)
  const ys = nodes.value.map((n) => n.y)
  const minX = Math.min(...xs) - 24
  const minY = Math.min(...ys) - 24
  const maxX = Math.max(...xs) + NODE_W + 24
  const maxY = Math.max(...ys) + NODE_H + 24
  viewBox.value = `${minX} ${minY} ${Math.max(200, maxX - minX)} ${Math.max(160, maxY - minY)}`
}

function resetView() {
  viewBox.value = ''
}

/** 全屏：把画布容器整块全屏，便于在大屏上看 */
async function toggleFullscreen() {
  if (document.fullscreenElement) {
    await document.exitFullscreen()
    return
  }
  await canvasWrap.value?.requestFullscreen?.()
}

// ---------- 一键校验 ----------

type CheckItem = { level: 'error' | 'warning' | 'info'; text: string }

const checkVisible = ref(false)
const checkItems = ref<CheckItem[]>([])

/**
 * 校验只看平台已有的事实，不猜：成环、孤立节点、没绑资源、绑定资源当前异常、
 * 自环与重复连线。发现不了的问题不假装发现。
 */
function runCheck() {
  const items: CheckItem[] = []
  if (detail.value?.cycle?.length) {
    items.push({ level: 'error', text: `依赖成环：${detail.value.cycle.join(' → ')}` })
  }

  const linked = new Set<number>()
  const pairs = new Set<string>()
  for (const edge of edges.value) {
    linked.add(edge.fromNodeId)
    linked.add(edge.toNodeId)
    if (edge.fromNodeId === edge.toNodeId) {
      const name = nodeById.value[edge.fromNodeId]?.name || edge.fromNodeId
      items.push({ level: 'error', text: `自环连线：${name} 指向自己` })
    }
    const key = `${edge.fromNodeId}->${edge.toNodeId}`
    if (pairs.has(key)) {
      const from = nodeById.value[edge.fromNodeId]?.name || edge.fromNodeId
      const to = nodeById.value[edge.toNodeId]?.name || edge.toNodeId
      items.push({ level: 'warning', text: `重复连线：${from} → ${to}` })
    }
    pairs.add(key)
  }

  for (const node of nodes.value) {
    if (!linked.has(node.id)) {
      items.push({ level: 'warning', text: `孤立节点：${node.name}（没有任何连线）` })
    }
    if (node.kind !== 'external' && !node.refName) {
      items.push({
        level: 'warning',
        text: `${node.name} 没有绑定平台资源，健康度会一直是「未知」`
      })
    }
    if (node.health === 'error') {
      items.push({ level: 'error', text: `${node.name} 当前异常：${node.detail || '见资源详情'}` })
    } else if (node.health === 'warning') {
      items.push({ level: 'warning', text: `${node.name} 需关注：${node.detail || '见资源详情'}` })
    }
  }

  if (!items.length) {
    items.push({ level: 'info', text: '没发现问题：连线无环、节点都有连线与绑定、资源状态正常' })
  }
  checkItems.value = items
  checkVisible.value = true
}

async function saveLayout() {

  const res = await saveTopologyLayout(
    currentId.value,
    nodes.value.map((n) => ({ id: n.id, x: n.x, y: n.y }))
  )
  layoutDirty.value = false
  ElMessage.success(res.detail)
}

/** 连线端点：从源节点右边缘连到目标节点左边缘，线不穿过方块 */
function edgePath(fromId: number, toId: number) {
  const from = nodeById.value[fromId]
  const to = nodeById.value[toId]
  if (!from || !to) return ''
  const x1 = from.x + NODE_W
  const y1 = from.y + NODE_H / 2
  const x2 = to.x
  const y2 = to.y + NODE_H / 2
  const mid = (x1 + x2) / 2
  return `M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`
}

onMounted(async () => {
  await loadList()
  try {
    resources.value = await listTopologyResources()
  } catch {
    // 资源清单拿不到只影响下拉，不影响看图
  }
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select
          v-model="currentId"
          placeholder="选择拓扑"
          style="width: 240px"
          @change="switchTopology"
        >
          <el-option v-for="item in topologies" :key="item.id" :label="item.name" :value="item.id">
            <span>{{ item.name }}</span>
            <span style="float: right; color: var(--el-text-color-secondary)">
              {{ item.nodeCount }} 节点
            </span>
          </el-option>
        </el-select>
        <el-button v-perm="'topology:manage'" type="primary" @click="topoDialog.visible = true">
          新建拓扑
        </el-button>
        <el-button v-perm="'topology:manage'" :disabled="!currentId" @click="openNodeDialog">
          添加节点
        </el-button>
        <el-button
          v-perm="'topology:manage'"
          :disabled="!currentId || nodes.length < 2"
          :type="linking ? 'warning' : 'default'"
          @click="toggleLinking"
        >
          {{ linking ? '退出连线' : '连线' }}
        </el-button>
        <el-button
          v-perm="'topology:manage'"
          :disabled="!currentId || nodes.length < 2"
          @click="autoLayout"
        >
          自动布局
        </el-button>
        <el-button
          v-perm="'topology:manage'"
          :disabled="!layoutDirty"
          type="success"
          @click="saveLayout"
        >
          保存布局
        </el-button>
        <el-button :disabled="!currentId || !nodes.length" @click="fitView">适配窗口</el-button>
        <el-button :disabled="!viewBox" @click="resetView">还原视图</el-button>
        <el-button :disabled="!currentId" @click="toggleFullscreen">全屏</el-button>
        <el-button :disabled="!currentId || !nodes.length" @click="runCheck">校验</el-button>
        <el-button :disabled="!currentId" @click="loadDetail">刷新状态</el-button>

      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span
            >节点绑定平台内已有资源（主机 / 数据库 / 拨测 /
            证书），颜色是这些资源的当前状态实时换算出来的，拓扑自己不采集任何数据。外部依赖节点恒为「未知」。拖动节点后需点「保存布局」。</span
          >
          <span v-if="detail?.cycle?.length" style="color: var(--el-color-warning)">
            依赖成环：{{ detail.cycle.join(' → ') }}（只提示，不阻止保存）
          </span>
        </template>
      </el-alert>

      <div v-if="detail" class="summary">
        <el-tag :color="healthColor[detail.health]" effect="dark" style="border: none">
          整体 {{ healthText[detail.health] }}
        </el-tag>
        <span v-for="key in ['error', 'warning', 'unknown', 'normal']" :key="key">
          {{ healthText[key] }} {{ detail.counts[key as 'error'] || 0 }}
        </span>
        <span>连线 {{ edges.length }} 条</span>
      </div>

      <el-empty v-if="!currentId" description="还没有拓扑，先新建一个" />
      <div v-else ref="canvasWrap" v-loading="loading" class="canvas-wrap">
        <svg
          class="canvas"
          :viewBox="viewBox || undefined"
          preserveAspectRatio="xMinYMin meet"
          @mousemove="onCanvasMouseMove"
          @mouseup="onCanvasMouseUp"
          @mouseleave="onCanvasMouseUp"
        >

          <defs>
            <marker
              id="topo-arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" fill="#b1b3b8" />
            </marker>
          </defs>

          <g v-for="edge in edges" :key="edge.id">
            <path
              :d="edgePath(edge.fromNodeId, edge.toNodeId)"
              fill="none"
              stroke="#b1b3b8"
              stroke-width="2"
              marker-end="url(#topo-arrow)"
              class="edge"
              @click="removeEdge(edge.id)"
            />
          </g>

          <g
            v-for="node in nodes"
            :key="node.id"
            :class="['node', { active: linkFrom === node.id }]"
            @mousedown="onNodeMouseDown(node, $event)"
            @click="onNodeClick(node)"
          >
            <rect
              :x="node.x"
              :y="node.y"
              :width="NODE_W"
              :height="NODE_H"
              rx="6"
              :fill="healthColor[node.health]"
              fill-opacity="0.14"
              :stroke="healthColor[node.health]"
              :stroke-width="linkFrom === node.id ? 3 : 1.5"
            />
            <text :x="node.x + 10" :y="node.y + 21" font-size="13" fill="#303133">
              {{ node.name }}
            </text>
            <text :x="node.x + 10" :y="node.y + 39" font-size="11" fill="#909399">
              {{ kindText[node.kind] }} · {{ healthText[node.health] }}
            </text>
            <title>{{ node.detail }}</title>
          </g>
        </svg>
      </div>

      <el-table v-if="currentId" :data="nodes" border stripe style="margin-top: 12px">
        <el-table-column prop="name" label="节点" min-width="140" />
        <el-table-column label="类型" width="110">
          <template #default="{ row }">{{ kindText[row.kind as TopologyNodeKind] }}</template>
        </el-table-column>
        <el-table-column prop="refName" label="绑定资源" min-width="140">
          <template #default="{ row }">{{ row.refName || '—' }}</template>
        </el-table-column>
        <el-table-column label="健康度" width="110">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="
                row.health === 'normal'
                  ? 'success'
                  : row.health === 'error'
                    ? 'danger'
                    : row.health === 'warning'
                      ? 'warning'
                      : 'info'
              "
            >
              {{ healthText[row.health] }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="判定理由" min-width="240" show-overflow-tooltip />
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'topology:manage'" link type="danger" @click="removeNode(row)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-table
        v-if="topologies.length"
        :data="topologies"
        border
        stripe
        style="margin-top: 16px"
      >
        <el-table-column prop="name" label="拓扑" min-width="140" />
        <el-table-column prop="remark" label="说明" min-width="200" show-overflow-tooltip />
        <el-table-column label="规模" width="130">
          <template #default="{ row }">{{ row.nodeCount }} 节点 / {{ row.edgeCount }} 连线</template>
        </el-table-column>
        <el-table-column label="状态" width="150">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="
                row.health === 'normal'
                  ? 'success'
                  : row.health === 'error'
                    ? 'danger'
                    : row.health === 'warning'
                      ? 'warning'
                      : 'info'
              "
            >
              {{ healthText[row.health] }}
            </el-tag>
            <span v-if="row.problems" style="margin-left: 6px; color: var(--el-text-color-secondary)">
              {{ row.problems }} 个待查
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="creatorName" label="创建人" width="110" />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="switchTopology(row.id)">查看</el-button>
            <el-button v-perm="'topology:manage'" link type="danger" @click="removeTopology(row)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="checkVisible" title="拓扑校验" width="560px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          校验只看平台已有的事实：连线成环、孤立节点、没绑资源、绑定资源当前异常、自环与重复连线。
          它不做可达性探测，也不判断"这条依赖对不对"——那要靠人。
        </template>
      </el-alert>
      <div v-for="(item, idx) in checkItems" :key="idx" class="check-line">
        <el-tag
          size="small"
          :type="item.level === 'error' ? 'danger' : item.level === 'warning' ? 'warning' : 'success'"
        >
          {{ item.level === 'error' ? '异常' : item.level === 'warning' ? '关注' : '正常' }}
        </el-tag>
        <span>{{ item.text }}</span>
      </div>
      <template #footer>
        <el-button type="primary" @click="checkVisible = false">知道了</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="topoDialog.visible" title="新建拓扑" width="420px">

      <el-form label-width="80px">
        <el-form-item label="名称">
          <el-input v-model="topoDialog.name" placeholder="例如 订单主链路" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="topoDialog.remark" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="topoDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="submitTopology">确定</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="nodeDialog.visible" title="添加节点" width="480px">
      <el-form label-width="90px">
        <el-form-item label="类型">
          <el-select v-model="nodeDialog.kind" style="width: 100%" @change="nodeDialog.refId = undefined">
            <el-option v-for="(label, key) in kindText" :key="key" :label="label" :value="key" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="nodeDialog.kind !== 'external'" label="绑定资源">
          <el-select v-model="nodeDialog.refId" filterable placeholder="选择资源" style="width: 100%">
            <el-option
              v-for="item in resources[nodeDialog.kind] || []"
              :key="item.id"
              :label="`${item.name}（${item.detail}）`"
              :value="item.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="节点名">
          <el-input
            v-model="nodeDialog.name"
            :placeholder="nodeDialog.kind === 'external' ? '外部依赖必须自己起名' : '留空则用资源名'"
          />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="nodeDialog.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="nodeDialog.visible = false">取消</el-button>
        <el-button type="primary" @click="submitNode">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.summary {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 10px;
  color: var(--el-text-color-regular);
  font-size: 13px;
}
.canvas-wrap {
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  background:
    linear-gradient(90deg, var(--el-fill-color-lighter) 1px, transparent 1px) 0 0 / 20px 20px,
    linear-gradient(var(--el-fill-color-lighter) 1px, transparent 1px) 0 0 / 20px 20px;
  overflow: auto;
}
.canvas {
  width: 100%;
  height: 420px;
  display: block;
}
/* 全屏时画布铺满，别还留着 420px 的高度 */
.canvas-wrap:fullscreen {
  background-color: var(--el-bg-color);
}
.canvas-wrap:fullscreen .canvas {
  height: 100vh;
}
.check-line {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 4px 0;
  font-size: 13px;
  line-height: 1.6;
}

.node {
  cursor: move;
}
.node text {
  user-select: none;
  pointer-events: none;
}
.edge {
  cursor: pointer;
}
.edge:hover {
  stroke: var(--el-color-danger);
}
</style>
