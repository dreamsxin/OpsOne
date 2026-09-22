<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { analyzeHostDisk, listHosts, type DiskAnalysis, type Host } from '@/api'

const hosts = ref<Host[]>([])
const hostId = ref<number>(0)
const path = ref('/')
const minFileMB = ref(100)
const loading = ref(false)
const result = ref<DiskAnalysis | null>(null)

// 常用起点：磁盘满了，十次里有九次在这几个目录之一
const quickPaths = ['/', '/var', '/var/log', '/var/lib/docker', '/home', '/data', '/tmp']

function formatKb(kb: number) {
  if (kb <= 0) return '0 KB'
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = kb
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value.toFixed(value >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}

// du 合计与 df 已用的差额；只有两个数都拿到才算
const gapKb = computed(() => {
  const r = result.value
  if (!r || r.rootKb <= 0 || r.filesystem.usedKb <= 0) return 0
  return r.filesystem.usedKb - r.rootKb
})

const fsPercent = computed(() => {
  const r = result.value
  if (!r || r.filesystem.totalKb <= 0) return 0
  return Math.round((r.filesystem.usedKb / r.filesystem.totalKb) * 100)
})

async function analyze() {
  if (!hostId.value) {
    ElMessage.warning('请选择主机')
    return
  }
  if (!path.value.startsWith('/')) {
    ElMessage.warning('路径必须是绝对路径')
    return
  }
  loading.value = true
  try {
    result.value = await analyzeHostDisk({
      hostId: hostId.value,
      path: path.value.trim(),
      minFileMB: minFileMB.value
    })
  } finally {
    loading.value = false
  }
}

function drillInto(target: string) {
  path.value = target
  analyze()
}

onMounted(async () => {
  const page = await listHosts({ page: 1, pageSize: 300 })
  hosts.value = page.list || []
  if (hosts.value.length > 0) hostId.value = hosts.value[0].id
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这一页是<strong>按需触发</strong>的：点「开始分析」才会连上主机跑 df 与 du。
          du 要遍历整棵目录树，几百万文件的数据盘一次要跑几分钟，期间还会冲掉 inode 缓存 ——
          所以平台<strong>不对它做定时任务</strong>，也不建议在业务高峰对生产机跑。
          <br />
          全部命令<strong>只读</strong>：df / du / find / readlink / stat，不删不改任何东西。
          单次执行有 120 秒超时，超时会提示你换一个更深的子目录。
        </template>
      </el-alert>

      <div class="page-toolbar" style="flex-wrap: wrap; gap: 8px">
        <el-select v-model="hostId" filterable placeholder="选择主机" style="width: 260px">
          <el-option
            v-for="host in hosts"
            :key="host.id"
            :value="host.id"
            :label="`${host.name}（${host.address}）`"
          />
        </el-select>
        <el-input v-model="path" placeholder="绝对路径，如 /var" style="width: 240px" />
        <el-input-number v-model="minFileMB" :min="1" :max="102400" :step="50" />
        <span style="color: #909399; font-size: 13px">MB 以上的文件才列出</span>
        <el-button type="primary" :loading="loading" @click="analyze">开始分析</el-button>
      </div>

      <div class="page-toolbar" style="flex-wrap: wrap; gap: 6px">
        <span style="color: #909399; font-size: 13px">常用起点：</span>
        <el-button v-for="p in quickPaths" :key="p" size="small" link @click="drillInto(p)">
          {{ p }}
        </el-button>
      </div>

      <template v-if="result">
        <el-descriptions :column="4" border style="margin: 12px 0">
          <el-descriptions-item label="分析路径">{{ result.path }}</el-descriptions-item>
          <el-descriptions-item label="所在挂载点">
            {{ result.filesystem.mount || '未取到' }}
          </el-descriptions-item>
          <el-descriptions-item label="文件系统已用 / 总量">
            <template v-if="result.filesystem.totalKb > 0">
              {{ result.filesystem.usedText }} / {{ result.filesystem.totalText }}（{{ fsPercent }}%）
            </template>
            <span v-else style="color: #909399">df 没取到</span>
          </el-descriptions-item>
          <el-descriptions-item label="du 统计合计">
            {{ result.rootText }}
            <span style="color: #909399; font-size: 12px">（磁盘占用，非大小之和）</span>
          </el-descriptions-item>
          <el-descriptions-item label="耗时">{{ result.costMs }} ms</el-descriptions-item>
          <el-descriptions-item label="跳过的目录 / 文件">
            <span :style="result.deniedDirs + result.deniedFiles > 0 ? 'color:#e6a23c' : ''">
              {{ result.deniedDirs }} / {{ result.deniedFiles }}
            </span>
            <span style="color: #909399; font-size: 12px">（有跳过则合计偏小）</span>
          </el-descriptions-item>
          <el-descriptions-item label="df 已用 − du 合计">
            <span v-if="gapKb > 0" :style="gapKb > 1024 * 1024 ? 'color:#f56c6c;font-weight:600' : ''">
              {{ formatKb(gapKb) }}
            </span>
            <span v-else-if="gapKb < 0">du 反而更大 {{ formatKb(-gapKb) }}</span>
            <span v-else style="color: #909399">无法比较</span>
          </el-descriptions-item>
          <el-descriptions-item label="已删除仍被持有">
            <span :style="result.deletedCount > 0 ? 'color:#e6a23c;font-weight:600' : ''">
              {{ result.deletedCount }} 个 / {{ formatKb(result.deletedKb) }}
            </span>
          </el-descriptions-item>
        </el-descriptions>

        <el-alert v-if="result.gapNote" type="error" :closable="false" style="margin-bottom: 12px">
          <template #title>
            <strong>df 与 du 对不上</strong>
            <div style="margin-top: 4px; line-height: 1.7">{{ result.gapNote }}</div>
          </template>
        </el-alert>

        <el-card shadow="never" style="margin-bottom: 12px">
          <template #header>
            <strong>一级子目录占用 Top {{ result.dirs.length }}</strong>
            <span style="color: #909399; margin-left: 8px">
              点目录名可以钻进去继续分析（只看一级，避免一次拉回整棵树）
            </span>
          </template>
          <el-table :data="result.dirs" border stripe size="small">
            <el-table-column label="目录" min-width="320">
              <template #default="{ row }">
                <el-button link type="primary" @click="drillInto(row.path)">{{ row.path }}</el-button>
              </template>
            </el-table-column>
            <el-table-column label="占用" width="120">
              <template #default="{ row }">{{ formatKb(row.sizeKb) }}</template>
            </el-table-column>
            <el-table-column label="占本次合计" min-width="220">
              <template #default="{ row }">
                <el-progress
                  v-if="row.percent > 0"
                  :percentage="Math.min(100, Math.round(row.percent))"
                  :stroke-width="12"
                />
                <span v-else style="color: #909399">根目录合计为 0，比例无意义</span>
              </template>
            </el-table-column>
          </el-table>
          <el-empty v-if="!result.dirs.length" description="没有一级子目录，或者都读不了" />
        </el-card>

        <el-card shadow="never" style="margin-bottom: 12px">
          <template #header>
            <strong>大文件（> {{ result.minFileMB }} MB）</strong>
            <span style="color: #909399; margin-left: 8px">
              不提供「列出所有文件」——那等于把一台机器的目录树拉回平台
            </span>
          </template>
          <el-table :data="result.files" border stripe size="small">
            <el-table-column prop="path" label="文件" min-width="420" />
            <el-table-column label="占用" width="120">
              <template #default="{ row }">{{ formatKb(row.sizeKb) }}</template>
            </el-table-column>
          </el-table>
          <el-empty v-if="!result.files.length" :description="`没有大于 ${result.minFileMB} MB 的文件`" />
        </el-card>

        <el-card shadow="never">
          <template #header>
            <strong>已删除但仍被进程持有的文件</strong>
            <span style="color: #909399; margin-left: 8px">
              扫 /proc/&lt;pid&gt;/fd 得到，不装 lsof。这类文件 du 看不到、df 照旧算着 ——
              空间要等进程关掉 fd 或重启才回来
            </span>
          </template>
          <el-table :data="result.deleted" border stripe size="small">
            <el-table-column prop="pid" label="PID" width="100" />
            <el-table-column prop="path" label="原路径" min-width="380" />
            <el-table-column label="仍占用" width="120">
              <template #default="{ row }">{{ formatKb(row.sizeKb) }}</template>
            </el-table-column>
          </el-table>
          <el-empty
            v-if="!result.deleted.length"
            description="没扫到 —— 注意：非 root 登录只能看到自己进程的 fd，这不等于没有"
          />
        </el-card>

        <ul style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
          <li v-for="(note, idx) in result.notes" :key="idx">{{ note }}</li>
        </ul>
      </template>

      <el-empty v-else-if="!loading" description="选一台主机和一个路径，点「开始分析」" />
    </el-card>
  </div>
</template>
