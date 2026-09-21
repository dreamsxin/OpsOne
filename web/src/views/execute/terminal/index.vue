<script setup lang="ts">
import { nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { listHosts, listScripts, type Host, type Script } from '@/api'
import { TOKEN_KEY } from '@/api/request'
import FileManager from '@/views/execute/file/index.vue'

const DEFAULT_FONT_SIZE = 13

const hosts = ref<Host[]>([])
const selectedId = ref<number | null>(null)
/** 真正连上的那台主机。文件侧栏必须用它而不是下拉框的当前值：
 *  连上 A 之后把下拉改成 B（没点连接，终端还是 A 的 PTY），
 *  文件侧栏若跟着下拉走就会在 B 上删文件 */
const connectedId = ref<number | null>(null)
const connected = ref(false)
const termBox = ref<HTMLDivElement>()

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null
let fontSize = DEFAULT_FONT_SIZE

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
}

function send(payload: Record<string, unknown>) {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(payload))
  }
}

function handleResize() {
  fitAddon?.fit()
  if (term) {
    send({ type: 'resize', cols: term.cols, rows: term.rows })
  }
}

async function connect() {
  if (!selectedId.value) {
    ElMessage.warning('请选择目标主机')
    return
  }
  disconnect()

  await nextTick()
  term = new Terminal({
    cursorBlink: true,
    fontSize,
    fontFamily: 'Consolas, Menlo, monospace',
    theme: { background: '#000000' }
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(termBox.value!)
  fitAddon.fit()

  // WebSocket 无法携带自定义请求头，令牌通过查询参数传递，后端会校验 Origin
  // 初始窗口尺寸一并带上，录像文件按该尺寸记录，回放时才不会错行
  const token = localStorage.getItem(TOKEN_KEY) || ''
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
  const params = new URLSearchParams({
    access_token: token,
    cols: String(term.cols),
    rows: String(term.rows)
  })
  ws = new WebSocket(
    `${scheme}://${location.host}/api/v1/hosts/${selectedId.value}/terminal?${params.toString()}`
  )


  ws.onopen = () => {
    connected.value = true
    connectedId.value = selectedId.value
    handleResize()
    term?.focus()
  }
  ws.onmessage = (event) => term?.write(event.data)
  ws.onclose = () => {
    connected.value = false
    term?.writeln('\r\n\x1b[33m连接已关闭\x1b[0m')
  }
  ws.onerror = () => ElMessage.error('WebSocket 连接异常')

  term.onData((data) => send({ type: 'input', data }))
  window.addEventListener('resize', handleResize)
}

function disconnect() {
  window.removeEventListener('resize', handleResize)
  ws?.close()
  ws = null
  term?.dispose()
  term = null
  fitAddon = null
  connected.value = false
  connectedId.value = null
  fileVisible.value = false
}

// ---------- 字号与全屏 ----------

function changeFontSize(delta: number) {
  fontSize = Math.min(22, Math.max(10, fontSize + delta))
  if (term) {
    term.options.fontSize = fontSize
    handleResize()
  }
}

function resetAppearance() {
  fontSize = DEFAULT_FONT_SIZE
  if (term) {
    term.options.fontSize = DEFAULT_FONT_SIZE
    handleResize()
  }
  ElMessage.success('已恢复默认字号')
}

function toggleFullscreen() {
  const el = termBox.value
  if (!el) return
  if (document.fullscreenElement) document.exitFullscreen()
  else el.requestFullscreen()
}

// 进出全屏容器尺寸会变，resize 事件不一定触发，这里主动补一次适配
function onFullscreenChange() {
  nextTick(handleResize)
}

// ---------- 文件侧栏 ----------

const fileVisible = ref(false)

// ---------- 命令面板：把脚本内容写进当前 PTY，走的是同一条会话通道（拦截与录像照常生效） ----------

const cmdVisible = ref(false)
const scripts = ref<Script[]>([])
const scriptsLoading = ref(false)
const scriptsError = ref(false)

async function openCommandPanel() {
  cmdVisible.value = true
  if (scripts.value.length) return
  scriptsLoading.value = true
  scriptsError.value = false
  try {
    const data = await listScripts({ page: 1, pageSize: 100, enabled: 'true' })
    scripts.value = data.list || []
  } catch {
    // 拉取失败不能让空态说成「没有脚本」，那是在撒谎
    scriptsError.value = true
  } finally {
    scriptsLoading.value = false
  }
}

/**
 * Script.params 是 JSON 字符串，脚本库保存时无条件 stringify，
 * 所以「没有参数」存进来是 "[]"（真值）。必须按解析后的条数判断，
 * 否则所有脚本的发送按钮都是灰的。
 */
function paramCount(row: Script): number {
  try {
    const list = JSON.parse(row.params || '[]')
    return Array.isArray(list) ? list.length : 0
  } catch {
    return 1
  }
}

async function runScriptInTerminal(row: Script) {
  if (!connected.value) {
    ElMessage.warning('请先连接主机')
    return
  }
  if (paramCount(row)) {
    ElMessage.info('该脚本带模板参数，请在「脚本库」渲染后到「作业下发」执行')
    return
  }
  // 高风险脚本走一次二次确认：批量下发那条路有闸门与生产确认，终端这条旁路不该更松
  if (row.riskLevel === 'high') {
    const ok = await ElMessageBox.confirm(
      `「${row.name}」被标为高风险，将直接在当前会话执行。命令仍会经过拦截规则与录像留痕。`,
      '确认发送',
      { type: 'warning', confirmButtonText: '发送', cancelButtonText: '取消' }
    ).catch(() => false)
    if (!ok) return
  }
  // 逐行发送：服务端按行判定拦截，整段发过去时若中间某行被拦，后续行会被静默丢弃，
  // 脚本停在半执行状态。逐行发至少让「执行到哪一行」和终端回显一致。
  const lines = row.content.split(/\r?\n/)
  for (const line of lines) {
    send({ type: 'input', data: line })
    send({ type: 'input', data: '\r' })
  }
  cmdVisible.value = false
  term?.focus()
  ElMessage.success(`已发送「${row.name}」共 ${lines.length} 行，注意确认终端输出`)
}

const riskMeta: Record<string, { text: string; type: 'info' | 'warning' | 'danger' }> = {
  low: { text: '低风险', type: 'info' },
  medium: { text: '中风险', type: 'warning' },
  high: { text: '高风险', type: 'danger' }
}

onMounted(() => {
  loadHosts()
  document.addEventListener('fullscreenchange', onFullscreenChange)
})
// 页签工作台缓存本页（WebSocket 保持连接是刻意的，会话不该因为切页签就断），
// 但切走期间窗口尺寸变了要在切回来时重新 fit，否则 xterm 的行列与容器错位；
// 全屏监听是 document 级的，跟着 activate/deactivate 走，别让后台页签也跑一遍
onActivated(() => {
  document.addEventListener('fullscreenchange', onFullscreenChange)
  if (connected.value) nextTick(handleResize)
})
onDeactivated(() => document.removeEventListener('fullscreenchange', onFullscreenChange))
onBeforeUnmount(() => {
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  disconnect()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select
          v-model="selectedId"
          placeholder="选择主机"
          filterable
          :disabled="connected"
          style="width: 320px"
        >
          <el-option
            v-for="host in hosts"
            :key="host.id"
            :label="`${host.name}（${host.username}@${host.address}:${host.port}）`"
            :value="host.id"
          />
        </el-select>
        <el-button v-perm="'terminal:connect'" type="primary" :disabled="connected" @click="connect">
          连接
        </el-button>
        <el-button :disabled="!connected" @click="disconnect">断开</el-button>
        <el-tag :type="connected ? 'success' : 'info'" size="small">
          {{ connected ? '已连接' : '未连接' }}
        </el-tag>

        <div class="grow"></div>

        <el-button :disabled="!connected" title="在侧栏管理当前主机的文件" @click="fileVisible = true">
          <el-icon style="margin-right: 4px"><Folder /></el-icon>
          文件
        </el-button>
        <el-button :disabled="!connected" title="从脚本库挑一条无参数脚本发到当前终端" @click="openCommandPanel">
          <el-icon style="margin-right: 4px"><List /></el-icon>
          命令
        </el-button>
        <el-button-group style="margin-left: 4px">
          <el-button title="减小字号" :disabled="!connected" @click="changeFontSize(-1)">
            <el-icon><ZoomOut /></el-icon>
          </el-button>
          <el-button title="增大字号" :disabled="!connected" @click="changeFontSize(1)">
            <el-icon><ZoomIn /></el-icon>
          </el-button>
        </el-button-group>
        <el-button :disabled="!connected" title="字号恢复默认" @click="resetAppearance">
          <el-icon style="margin-right: 4px"><RefreshLeft /></el-icon>
          恢复默认
        </el-button>
        <el-button title="终端全屏" @click="toggleFullscreen">
          <el-icon><FullScreen /></el-icon>
        </el-button>
      </div>

      <div ref="termBox" class="terminal-box"></div>
    </el-card>

    <el-drawer v-model="fileVisible" title="文件管理（当前会话主机）" size="60%">
      <FileManager v-if="fileVisible && connectedId" :host-id="connectedId" />
    </el-drawer>

    <el-drawer v-model="cmdVisible" title="命令面板" size="40%">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="脚本内容会作为输入写进当前终端会话，仍经过会话通道的命令拦截与录像留痕；带模板参数的脚本请改用「作业下发」"
      />
      <el-table
        v-loading="scriptsLoading"
        :data="scripts"
        border
        stripe
        size="small"
        :empty-text="
          scriptsError ? '脚本列表拉取失败，关掉重开可重试' : '暂无可用脚本，可在「执行中心 → 脚本库」维护'
        "
      >
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column prop="category" label="分类" width="110" />
        <el-table-column label="风险" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="riskMeta[row.riskLevel]?.type || 'info'">
              {{ riskMeta[row.riskLevel]?.text || row.riskLevel }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
        <el-table-column label="操作" width="110" fixed="right">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              :disabled="paramCount(row) > 0"
              :title="paramCount(row) > 0 ? '带模板参数，请用「作业下发」' : '发送到当前终端'"
              @click="runScriptInTerminal(row)"
            >
              发送
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>
