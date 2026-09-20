<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { listHosts, type Host } from '@/api'
import { TOKEN_KEY } from '@/api/request'

const hosts = ref<Host[]>([])
const selectedId = ref<number | null>(null)
const connected = ref(false)
const termBox = ref<HTMLDivElement>()

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let ws: WebSocket | null = null

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
    fontSize: 13,
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
}

onMounted(loadHosts)
onBeforeUnmount(disconnect)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="selectedId" placeholder="选择主机" filterable style="width: 320px">
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
      </div>

      <div ref="termBox" class="terminal-box"></div>
    </el-card>
  </div>
</template>
