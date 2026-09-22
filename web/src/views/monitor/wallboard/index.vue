<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getWallboard, type WallboardData } from '@/api'

const data = ref<WallboardData | null>(null)
const loading = ref(false)
const errorMsg = ref('')
const fullscreen = ref(false)
// 30 秒刷新一次。大屏是投出去长期挂着的，太密会白占后端
const REFRESH_MS = 30000
let timer: number | undefined

const maxTrend = computed(() =>
  (data.value?.trend ?? []).reduce((max, x) => Math.max(max, x.total), 1)
)

const updatedAt = computed(() =>
  data.value?.at ? data.value.at.slice(11, 19) : '—'
)

function remainText(seconds: number) {
  const abs = Math.abs(seconds)
  const mins = Math.floor(abs / 60)
  const text = mins >= 60 ? `${Math.floor(mins / 60)} 小时 ${mins % 60} 分` : `${mins} 分`
  return seconds < 0 ? `已超 ${text}` : `剩 ${text}`
}

async function load() {
  loading.value = true
  try {
    data.value = await getWallboard()
    errorMsg.value = ''
  } catch (err: any) {
    errorMsg.value = err?.message || '读取失败'
  } finally {
    loading.value = false
  }
}

async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) {
      await document.exitFullscreen()
      fullscreen.value = false
    } else {
      await document.documentElement.requestFullscreen()
      fullscreen.value = true
    }
  } catch {
    // 浏览器不允许就算了，不值得为此报错
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, REFRESH_MS)
})
onUnmounted(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="page wallboard">
    <div class="bar">
      <div class="bar-title">值班大屏</div>
      <div class="bar-note">
        这一屏回答「现在要不要动手」。按天按源的分布在「告警态势」页 —— 那是复盘用的
      </div>
      <div class="grow"></div>
      <el-tag type="info">{{ updatedAt }} 刷新 · 每 30 秒</el-tag>
      <el-button size="small" :loading="loading" @click="load">立即刷新</el-button>
      <el-button size="small" @click="toggleFullscreen">
        {{ fullscreen ? '退出全屏' : '全屏' }}
      </el-button>
    </div>

    <el-alert v-if="errorMsg" type="error" :closable="false" :title="errorMsg" style="margin-bottom: 12px" />

    <template v-if="data">
      <!-- 第一排：现在有什么在响、有什么没人管 -->
      <div class="grid">
        <div class="cell" :class="{ hot: data.firing.critical > 0 }">
          <div class="num">{{ data.firing.critical }}</div>
          <div class="lbl">严重告警进行中</div>
        </div>
        <div class="cell" :class="{ warn: data.firing.total > 0 }">
          <div class="num">{{ data.firing.total }}</div>
          <div class="lbl">全部进行中</div>
        </div>
        <el-tooltip
          content="仍然在响，只是被静默或聚合抑制了、没有外发。它必须出现在这里 —— 否则告警数为 0 会被读成「一切正常」"
          placement="bottom"
        >
          <div class="cell" :class="{ warn: data.firing.suppressed > 0 }">
            <div class="num">{{ data.firing.suppressed }}</div>
            <div class="lbl">被抑制（没外发）</div>
          </div>
        </el-tooltip>
        <div class="cell" :class="{ hot: data.events.breached > 0 }">
          <div class="num">{{ data.events.breached }}</div>
          <div class="lbl">事件 SLA 已超时</div>
        </div>
        <div class="cell" :class="{ warn: data.events.atRisk > 0 }">
          <div class="num">{{ data.events.atRisk }}</div>
          <div class="lbl">SLA 临期</div>
        </div>
        <div class="cell" :class="{ warn: data.events.unassigned > 0 }">
          <div class="num">{{ data.events.unassigned }}</div>
          <div class="lbl">未指派的事件</div>
        </div>
        <el-tooltip
          content="告警发不出去是最隐蔽的一种故障：平台看着在告警，实际没人收到"
          placement="bottom"
        >
          <div class="cell" :class="{ hot: data.notify.failed24h > 0 }">
            <div class="num">{{ data.notify.failed24h }}</div>
            <div class="lbl">24h 通知投递失败</div>
          </div>
        </el-tooltip>
        <div class="cell" :class="{ warn: data.security.open > 0 }">
          <div class="num">{{ data.security.open }}</div>
          <div class="lbl">未结案安全事件</div>
        </div>
      </div>

      <div class="two">
        <!-- 最该先看的几张单 -->
        <el-card shadow="never">
          <template #header>
            SLA 超时 / 临期的事件
            <el-text type="info" size="small" style="margin-left: 8px">
              最多 8 条，超时最久的在前；「响应」口径：指派不算响应
            </el-text>
          </template>
          <el-table :data="data.events.urgent" size="small" border stripe empty-text="没有超时或临期的事件">
            <el-table-column label="级别" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.severity === 'critical' ? 'danger' : 'warning'">
                  {{ row.severity }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="title" label="事件" min-width="200" show-overflow-tooltip />
            <el-table-column label="负责人" width="100">
              <template #default="{ row }">
                <span v-if="row.assignee">{{ row.assignee }}</span>
                <el-tag v-else size="small" type="warning">未指派</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="SLA" width="140">
              <template #default="{ row }">
                <el-tag size="small" :type="row.slaState === 'breached' ? 'danger' : 'warning'">
                  {{ remainText(row.remainSeconds) }}
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <!-- 关键面 + 当前值班 -->
        <el-card shadow="never">
          <template #header>关键面与当前值班</template>
          <el-descriptions :column="2" border size="small">
            <el-descriptions-item label="主机离线">{{ data.hosts.offline }} / {{ data.hosts.total }}</el-descriptions-item>
            <el-descriptions-item label="拨测失败">{{ data.probes.down }} / {{ data.probes.total }}</el-descriptions-item>
            <el-descriptions-item label="证书将到期 / 已过期">
              {{ data.certs.expiring }} / {{ data.certs.expired }}
            </el-descriptions-item>
            <el-descriptions-item label="域名将到期 / 解析漂移">
              {{ data.domains.expiring }} / {{ data.domains.dnsDrift }}
            </el-descriptions-item>
            <el-descriptions-item label="日志命中关键字">{{ data.hostLogs.hit }}</el-descriptions-item>
            <el-descriptions-item label="日志监控读不到">{{ data.hostLogs.unreadable }}</el-descriptions-item>
          </el-descriptions>

          <div class="oncall">
            <div v-if="!data.onCall.length" class="oncall-empty">没有启用中的值班表</div>
            <div v-for="item in data.onCall" :key="item.id" class="oncall-row">
              <span class="oncall-name">{{ item.name }}</span>
              <span v-if="item.note" class="oncall-note">{{ item.note }}</span>
              <template v-else>
                <el-tag type="success">当班 {{ item.current }}</el-tag>
                <span v-if="(item.levels?.length ?? 0) > 1" class="oncall-note">
                  升级链：{{ item.levels?.join(' → ') }}
                </span>
              </template>
            </div>
          </div>
        </el-card>
      </div>

      <!-- 24 小时告警趋势 -->
      <el-card shadow="never" style="margin-top: 12px">
        <template #header>近 24 小时新增告警</template>
        <div v-if="!data.trend.length" class="trend-empty">这 24 小时没有新增告警</div>
        <div v-else class="trend">
          <div v-for="item in data.trend" :key="item.hour" class="bar-wrap">
            <div class="bar" :style="{ height: Math.max(4, (item.total / maxTrend) * 100) + '%' }">
              <span class="bar-num">{{ item.total }}</span>
            </div>
            <div class="bar-day">{{ item.hour.slice(11) }}</div>
          </div>
        </div>
      </el-card>

      <el-card shadow="never" style="margin-top: 12px">
        <template #header>这一屏的口径</template>
        <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
          <li v-for="(note, idx) in data.notes" :key="idx">{{ note }}</li>
        </ul>
      </el-card>
    </template>
  </div>
</template>

<style scoped>
.bar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.bar-title {
  font-size: 20px;
  font-weight: 600;
}
.bar-note {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.grow {
  flex: 1;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
}
.cell {
  padding: 16px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  text-align: center;
}
.cell .num {
  font-size: 34px;
  font-weight: 700;
  line-height: 1.1;
}
.cell .lbl {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.cell.hot {
  border-color: var(--el-color-danger);
}
.cell.hot .num {
  color: var(--el-color-danger);
}
.cell.warn {
  border-color: var(--el-color-warning);
}
.cell.warn .num {
  color: var(--el-color-warning);
}
.two {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(460px, 1fr));
  gap: 12px;
  margin-top: 12px;
}
.oncall {
  margin-top: 12px;
}
.oncall-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  font-size: 13px;
}
.oncall-name {
  min-width: 120px;
  font-weight: 500;
}
.oncall-note,
.oncall-empty,
.trend-empty {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.trend {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  height: 150px;
  padding: 8px 4px 0;
}
.bar-wrap {
  flex: 1;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
}
.bar {
  width: 100%;
  background: var(--el-color-primary-light-3);
  border-radius: 3px 3px 0 0;
  position: relative;
  min-height: 4px;
}
.bar-num {
  position: absolute;
  top: -17px;
  left: 0;
  right: 0;
  text-align: center;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
.bar-day {
  margin-top: 6px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
