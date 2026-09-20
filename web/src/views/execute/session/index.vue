<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { getSession, listSessions, type SessionCommand, type TerminalSession } from '@/api'
import { TOKEN_KEY } from '@/api/request'

const loading = ref(false)
const rows = ref<TerminalSession[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, keyword: '', username: '', status: '', riskOnly: '' })

const detailVisible = ref(false)
const current = ref<TerminalSession | null>(null)
const commands = ref<SessionCommand[]>([])
const replayable = ref(false)

// 回放播放器状态
const playerVisible = ref(false)
const playing = ref(false)
const speed = ref(1)
const progress = ref(0)
const totalSeconds = ref(0)
const playerBox = ref<HTMLDivElement>()

let term: Terminal | null = null
let fitAddon: FitAddon | null = null
let frames: { at: number; data: string }[] = []
let timer: number | null = null
let cursor = 0
let playStartedAt = 0
let playedOffset = 0

const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  active: { text: '进行中', type: 'success' },
  closed: { text: '已结束', type: 'info' },
  error: { text: '异常', type: 'danger' }
}

const riskMeta: Record<string, { text: string; type: 'info' | 'warning' | 'danger' }> = {
  normal: { text: '正常', type: 'info' },
  warn: { text: '高风险', type: 'warning' },
  blocked: { text: '已拦截', type: 'danger' }
}

async function load() {
  loading.value = true
  try {
    const data = await listSessions(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function openDetail(row: TerminalSession) {
  const data = await getSession(row.id)
  current.value = data.session
  commands.value = data.session.commands || []
  replayable.value = data.replayable
  detailVisible.value = true
}

function formatDuration(ms: number) {
  if (!ms) return '-'
  const sec = Math.round(ms / 1000)
  if (sec < 60) return `${sec}s`
  return `${Math.floor(sec / 60)}m${sec % 60}s`
}

// ---------- 录像回放 ----------

async function openPlayer() {
  if (!current.value) return

  // 录像接口返回文件流，需要带上令牌，因此用 fetch 而不是 img/src 方式
  const token = localStorage.getItem(TOKEN_KEY) || ''
  const res = await fetch(`/api/v1/sessions/${current.value.id}/replay`, {
    headers: { Authorization: `Bearer ${token}` }
  })
  if (!res.ok) {
    ElMessage.error('录像加载失败')
    return
  }
  const text = await res.text()
  const parsed = parseCast(text)
  if (!parsed) {
    ElMessage.error('录像格式无法解析')
    return
  }

  frames = parsed.frames
  totalSeconds.value = frames.length ? frames[frames.length - 1].at : 0
  playerVisible.value = true



  // 等抽屉内的 DOM 就绪再初始化终端
  await new Promise((r) => setTimeout(r, 50))
  disposeTerm()
  term = new Terminal({
    cols: parsed.width,
    rows: parsed.height,
    fontSize: 13,
    fontFamily: 'Consolas, Menlo, monospace',
    theme: { background: '#000000' },
    disableStdin: true
  })
  fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(playerBox.value!)
  fitAddon.fit()

  resetPlayback()
  play()
}

interface ParsedCast {
  width: number
  height: number
  frames: { at: number; data: string }[]
}

function parseCast(text: string): ParsedCast | null {
  const lines = text.split('\n').filter((l) => l.trim())
  if (!lines.length) return null

  let header: any
  try {
    header = JSON.parse(lines[0])
  } catch {
    return null
  }

  const frames: { at: number; data: string }[] = []
  for (const line of lines.slice(1)) {
    try {
      const event = JSON.parse(line)
      if (Array.isArray(event) && event[1] === 'o') {
        frames.push({ at: Number(event[0]), data: String(event[2]) })
      }
    } catch {
      // 单帧损坏时跳过，不影响整体回放
    }
  }
  return { width: header.width || 120, height: header.height || 30, frames }
}

function resetPlayback() {
  stopTimer()
  cursor = 0
  playedOffset = 0
  progress.value = 0
  term?.reset()
}

function play() {
  if (!term || cursor >= frames.length) return
  playing.value = true
  playStartedAt = Date.now()
  schedule()
}

function schedule() {
  stopTimer()
  if (cursor >= frames.length) {
    playing.value = false
    return
  }
  const elapsed = playedOffset + ((Date.now() - playStartedAt) / 1000) * speed.value
  const wait = Math.max(0, (frames[cursor].at - elapsed) * 1000) / speed.value

  timer = window.setTimeout(() => {
    // 一次把已到时间的帧全部写出，避免高倍速下逐帧 setTimeout 造成卡顿
    const now = playedOffset + ((Date.now() - playStartedAt) / 1000) * speed.value
    while (cursor < frames.length && frames[cursor].at <= now) {
      term?.write(frames[cursor].data)
      progress.value = frames[cursor].at
      cursor++
    }
    schedule()
  }, wait)


}

function pause() {
  if (!playing.value) return
  playedOffset += ((Date.now() - playStartedAt) / 1000) * speed.value
  playing.value = false
  stopTimer()
}

function togglePlay() {
  if (playing.value) {
    pause()
  } else {
    if (cursor >= frames.length) resetPlayback()
    play()
  }
}

function changeSpeed(value: number) {
  const wasPlaying = playing.value
  pause()
  speed.value = value
  if (wasPlaying) play()
}

// 从命令明细跳到对应时间点：重放该时刻之前的所有帧以还原屏幕
function seekTo(offsetMs: number) {
  if (!term) {
    ElMessage.warning('请先打开回放')
    return
  }
  pause()
  term.reset()
  const target = offsetMs / 1000
  cursor = 0
  while (cursor < frames.length && frames[cursor].at <= target) {
    term.write(frames[cursor].data)
    cursor++
  }
  playedOffset = target
  progress.value = target
  play()
}

function stopTimer() {
  if (timer !== null) {
    clearTimeout(timer)
    timer = null
  }
}

function disposeTerm() {
  stopTimer()
  term?.dispose()
  term = null
  fitAddon = null
  playing.value = false
}

function closePlayer() {
  playerVisible.value = false
  disposeTerm()
}

onMounted(load)
onBeforeUnmount(disposeTerm)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="主机名 / 地址"
          style="width: 200px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-input v-model="query.username" placeholder="操作人" style="width: 160px" clearable />
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option label="进行中" value="active" />
          <el-option label="已结束" value="closed" />
          <el-option label="异常" value="error" />
        </el-select>
        <el-checkbox
          :model-value="query.riskOnly === 'true'"
          @change="((query.riskOnly = $event ? 'true' : ''), (query.page = 1), load())"
        >
          只看有拦截
        </el-checkbox>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="主机" min-width="180">
          <template #default="{ row }">
            {{ row.hostName }}
            <span style="color: #6b7280">{{ row.loginUser }}@{{ row.address }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="username" label="操作人" width="110" />
        <el-table-column label="跳板" min-width="130">
          <template #default="{ row }">
            <span v-if="row.viaProxy">{{ row.viaProxy }}</span>
            <el-tag v-else size="small" type="info">直连</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="命令" width="110">
          <template #default="{ row }">
            {{ row.commandCount }}
            <el-tag v-if="row.blockedCount > 0" size="small" type="danger" style="margin-left: 4px">
              拦截 {{ row.blockedCount }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="时长" width="90">
          <template #default="{ row }">{{ formatDuration(row.durationMs) }}</template>
        </el-table-column>
        <el-table-column prop="startedAt" label="开始时间" min-width="180" />
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>

    <el-drawer v-model="detailVisible" :title="`会话 #${current?.id ?? ''}`" size="60%">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item label="主机">
          {{ current.hostName }}（{{ current.loginUser }}@{{ current.address }}）
        </el-descriptions-item>
        <el-descriptions-item label="操作人">{{ current.username }}</el-descriptions-item>
        <el-descriptions-item label="来源 IP">{{ current.clientIp }}</el-descriptions-item>
        <el-descriptions-item label="跳板机">{{ current.viaProxy || '直连' }}</el-descriptions-item>
        <el-descriptions-item label="时长">{{ formatDuration(current.durationMs) }}</el-descriptions-item>
        <el-descriptions-item label="命令 / 拦截">
          {{ current.commandCount }} / {{ current.blockedCount }}
        </el-descriptions-item>
        <el-descriptions-item v-if="current.errorMsg" label="异常" :span="2">
          {{ current.errorMsg }}
        </el-descriptions-item>
      </el-descriptions>

      <div style="margin: 12px 0; display: flex; gap: 8px; align-items: center">
        <el-button v-perm="'session:replay'" type="primary" :disabled="!replayable" @click="openPlayer">
          打开录像回放
        </el-button>
        <span v-if="!replayable" style="color: #6b7280">该会话无可用录像</span>
      </div>

      <div v-show="playerVisible">
        <div style="display: flex; gap: 8px; align-items: center; margin-bottom: 8px">
          <el-button size="small" @click="togglePlay">{{ playing ? '暂停' : '播放' }}</el-button>
          <el-button size="small" @click="resetPlayback">重播</el-button>
          <el-select
            :model-value="speed"
            size="small"
            style="width: 90px"
            @update:model-value="changeSpeed"
          >
            <el-option :value="1" label="1x" />
            <el-option :value="2" label="2x" />
            <el-option :value="4" label="4x" />
            <el-option :value="8" label="8x" />
          </el-select>
          <span style="color: #6b7280">
            {{ Math.round(progress) }}s / {{ Math.round(totalSeconds) }}s
          </span>
          <el-button size="small" text @click="closePlayer">关闭</el-button>
        </div>
        <div ref="playerBox" style="background: #000; padding: 8px; border-radius: 6px"></div>
      </div>

      <el-divider content-position="left">命令明细</el-divider>
      <el-table :data="commands" size="small" border empty-text="该会话未记录到命令">
        <el-table-column label="时间点" width="90">
          <template #default="{ row }">{{ Math.round(row.offsetMs / 1000) }}s</template>
        </el-table-column>
        <el-table-column prop="command" label="命令" min-width="260" show-overflow-tooltip />
        <el-table-column label="风险" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="riskMeta[row.risk]?.type || 'info'">
              {{ riskMeta[row.risk]?.text || row.risk }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="定位" width="80">
          <template #default="{ row }">
            <el-button link type="primary" :disabled="!playerVisible" @click="seekTo(row.offsetMs)">
              跳转
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>
