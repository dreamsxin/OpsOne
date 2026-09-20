<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import {
  exportSessionCommandsCSV,
  getSession,
  listSessionCommands,
  listSessions,
  searchSessionCommands,
  type SessionCommand,
  type SessionCommandHit,
  type TerminalSession
} from '@/api'
import { TOKEN_KEY } from '@/api/request'

const tab = ref('sessions')

// ---------- 会话列表 ----------
const loading = ref(false)
const rows = ref<TerminalSession[]>([])
const total = ref(0)
const query = reactive({
  page: 1,
  pageSize: 20,
  keyword: '',
  username: '',
  loginUser: '',
  status: '',
  risk: '',
  start: '',
  end: ''
})

// ---------- 命令检索 ----------
const cmdLoading = ref(false)
const hits = ref<SessionCommandHit[]>([])
const hitTotal = ref(0)
const hitBlocked = ref(0)
const cmdQuery = reactive({
  page: 1,
  pageSize: 20,
  keyword: '',
  risk: '',
  username: '',
  loginUser: '',
  host: '',
  start: '',
  end: ''
})

const detailVisible = ref(false)
const current = ref<TerminalSession | null>(null)
const commands = ref<SessionCommand[]>([])
const cmdDetail = reactive({ page: 1, pageSize: 50, total: 0, risk: '', keyword: '' })
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

function searchSessions() {
  query.page = 1
  load()
}

// ---------- 跨会话命令检索 ----------

async function searchCommands() {
  cmdLoading.value = true
  try {
    const data = await searchSessionCommands(cmdQuery)
    hits.value = data.list || []
    hitTotal.value = data.total
    hitBlocked.value = data.blocked
  } catch (err: any) {
    hits.value = []
    hitTotal.value = 0
    ElMessage.error(err?.message || '检索失败')
  } finally {
    cmdLoading.value = false
  }
}

function runCommandSearch() {
  cmdQuery.page = 1
  searchCommands()
}

// 首次切到命令检索时自动查一次，否则空表看不出是「没数据」还是「没查」
let cmdLoaded = false
function onTabChange(name: string) {
  if (name === 'commands' && !cmdLoaded) {
    cmdLoaded = true
    searchCommands()
  }
}


async function exportCommands() {
  try {
    await exportSessionCommandsCSV({ ...cmdQuery, page: undefined, pageSize: undefined })
    ElMessage.success('已开始下载，单次最多导出 10000 条')
  } catch (err: any) {
    ElMessage.error(err?.message || '导出失败')
  }
}

// 从检索结果跳到那次会话，并把录像定位到这条命令的时间点
async function openFromHit(hit: SessionCommandHit) {
  await openDetail({ id: hit.sessionId } as TerminalSession)
  if (!replayable.value) {
    ElMessage.warning('该会话没有录像，只能看命令明细')
    return
  }
  await openPlayer()
  seekTo(hit.offsetMs)
}

async function openDetail(row: TerminalSession) {
  const data = await getSession(row.id)
  current.value = data.session
  replayable.value = data.replayable
  detailVisible.value = true
  cmdDetail.page = 1
  cmdDetail.risk = ''
  cmdDetail.keyword = ''
  await loadDetailCommands()
}

async function loadDetailCommands() {
  if (!current.value) return
  const data = await listSessionCommands(current.value.id, {
    page: cmdDetail.page,
    pageSize: cmdDetail.pageSize,
    risk: cmdDetail.risk,
    keyword: cmdDetail.keyword
  })
  commands.value = data.list || []
  cmdDetail.total = data.total
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
      <el-tabs v-model="tab" @tab-change="onTabChange">
        <el-tab-pane label="会话" name="sessions">
          <div class="page-toolbar">
            <el-input
              v-model="query.keyword"
              placeholder="主机名 / 地址"
              style="width: 170px"
              clearable
              @keyup.enter="searchSessions"
            />
            <el-input v-model="query.username" placeholder="操作人" style="width: 130px" clearable />
            <el-input v-model="query.loginUser" placeholder="登录账号" style="width: 130px" clearable />
            <el-select v-model="query.status" placeholder="状态" clearable style="width: 120px">
              <el-option label="进行中" value="active" />
              <el-option label="已结束" value="closed" />
              <el-option label="异常" value="error" />
            </el-select>
            <el-select v-model="query.risk" placeholder="风险" clearable style="width: 140px">
              <el-option label="有被拦命令" value="blocked" />
              <el-option label="有风险命令" value="risky" />
            </el-select>
            <el-date-picker
              v-model="query.start"
              type="date"
              placeholder="开始日期"
              value-format="YYYY-MM-DD"
              style="width: 140px"
            />
            <el-date-picker
              v-model="query.end"
              type="date"
              placeholder="截止日期"
              value-format="YYYY-MM-DD"
              style="width: 140px"
            />
            <el-button type="primary" @click="searchSessions">查询</el-button>
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
            <el-table-column label="跳板" min-width="120">
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
            <el-table-column prop="startedAt" label="开始时间" min-width="170" />
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
        </el-tab-pane>

        <el-tab-pane label="命令检索" name="commands">
          <el-alert
            type="info"
            :closable="false"
            style="margin-bottom: 12px"
            title="跨所有会话检索命令——回答「谁在哪台机器上敲过 rm -rf」这类问题。点某一行可以跳到那次会话的录像并定位到该命令的时间点；结果可按当前条件导出 CSV（单次最多 10000 条）"
          />
          <div class="page-toolbar">
            <el-input
              v-model="cmdQuery.keyword"
              placeholder="命令关键字，如 rm -rf"
              style="width: 200px"
              clearable
              @keyup.enter="runCommandSearch"
            />
            <el-select v-model="cmdQuery.risk" placeholder="风险" clearable style="width: 130px">
              <el-option label="正常" value="normal" />
              <el-option label="高风险" value="warn" />
              <el-option label="已拦截" value="blocked" />
              <el-option label="高风险+拦截" value="risky" />
            </el-select>
            <el-input v-model="cmdQuery.username" placeholder="操作人" style="width: 130px" clearable />
            <el-input v-model="cmdQuery.loginUser" placeholder="登录账号" style="width: 130px" clearable />
            <el-input v-model="cmdQuery.host" placeholder="主机名 / 地址" style="width: 160px" clearable />
            <el-date-picker
              v-model="cmdQuery.start"
              type="date"
              placeholder="开始日期"
              value-format="YYYY-MM-DD"
              style="width: 140px"
            />
            <el-date-picker
              v-model="cmdQuery.end"
              type="date"
              placeholder="截止日期"
              value-format="YYYY-MM-DD"
              style="width: 140px"
            />
            <el-button type="primary" @click="runCommandSearch">检索</el-button>
            <el-button @click="exportCommands">导出 CSV</el-button>
          </div>

          <div class="meta">
            命中 {{ hitTotal }} 条<span v-if="hitBlocked">，其中已拦截 {{ hitBlocked }} 条</span>
          </div>

          <el-table v-loading="cmdLoading" :data="hits" border stripe>
            <el-table-column prop="createdAt" label="时间" min-width="170" />
            <el-table-column label="操作人" width="110">
              <template #default="{ row }">{{ row.username }}</template>
            </el-table-column>
            <el-table-column label="主机" min-width="180" show-overflow-tooltip>
              <template #default="{ row }">
                {{ row.hostName }}
                <span style="color: #6b7280">{{ row.loginUser }}@{{ row.address }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="command" label="命令" min-width="260" show-overflow-tooltip />
            <el-table-column label="风险" width="100">
              <template #default="{ row }">
                <el-tag size="small" :type="riskMeta[row.risk]?.type || 'info'">
                  {{ riskMeta[row.risk]?.text || row.risk }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="命中规则" min-width="140" show-overflow-tooltip>
              <template #default="{ row }">
                <span v-if="row.ruleDesc">{{ row.ruleDesc }}</span>
                <span v-else-if="row.ruleId">#{{ row.ruleId }}</span>
                <span v-else>—</span>
              </template>
            </el-table-column>
            <el-table-column label="会话" width="150" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" @click="openFromHit(row)">
                  #{{ row.sessionId }} · {{ Math.round(row.offsetMs / 1000) }}s
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-pagination
            style="margin-top: 12px; justify-content: flex-end"
            layout="total, sizes, prev, pager, next"
            :total="hitTotal"
            :page-sizes="[20, 50, 100]"
            v-model:current-page="cmdQuery.page"
            v-model:page-size="cmdQuery.pageSize"
            @current-change="searchCommands"
            @size-change="runCommandSearch"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-drawer v-model="detailVisible" :title="`会话 #${current?.id ?? ''}`" size="62%">
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
      <div class="page-toolbar">
        <el-input
          v-model="cmdDetail.keyword"
          placeholder="命令关键字"
          style="width: 180px"
          clearable
          @keyup.enter="((cmdDetail.page = 1), loadDetailCommands())"
        />
        <el-select
          v-model="cmdDetail.risk"
          placeholder="风险"
          clearable
          style="width: 130px"
          @change="((cmdDetail.page = 1), loadDetailCommands())"
        >
          <el-option label="正常" value="normal" />
          <el-option label="高风险" value="warn" />
          <el-option label="已拦截" value="blocked" />
          <el-option label="高风险+拦截" value="risky" />
        </el-select>
        <el-button @click="((cmdDetail.page = 1), loadDetailCommands())">筛选</el-button>
      </div>
      <el-table :data="commands" size="small" border empty-text="该会话未记录到命令">
        <el-table-column label="时间点" width="90">
          <template #default="{ row }">{{ Math.round(row.offsetMs / 1000) }}s</template>
        </el-table-column>
        <el-table-column prop="command" label="命令" min-width="240" show-overflow-tooltip />
        <el-table-column label="风险" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="riskMeta[row.risk]?.type || 'info'">
              {{ riskMeta[row.risk]?.text || row.risk }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="命中规则" min-width="130" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.ruleDesc">{{ row.ruleDesc }}</span>
            <span v-else-if="row.ruleId">#{{ row.ruleId }}</span>
            <span v-else>—</span>
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
      <el-pagination
        style="margin-top: 8px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="cmdDetail.total"
        v-model:current-page="cmdDetail.page"
        :page-size="cmdDetail.pageSize"
        @current-change="loadDetailCommands"
      />
    </el-drawer>
  </div>
</template>

<style scoped>
.meta {
  margin: 4px 0 8px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
