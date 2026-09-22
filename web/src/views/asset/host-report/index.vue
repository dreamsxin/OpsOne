<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getHostReport, listHosts, type Host, type HostReport, type HostReportSection } from '@/api'

const hosts = ref<Host[]>([])
const hostId = ref<number>(0)
const loading = ref(false)
const report = ref<HostReport | null>(null)

const driftLabel: Record<string, string> = {
  ok: '与期望一致',
  inactive: '没在跑',
  unexpected: '跑着但不该跑',
  disabled: '没设开机自启',
  missing: '机器上找不到',
  error: '巡检失败',
  unknown: '未巡检',
  drift: '与基线不同',
  'no-desired': '没有基线',
  'too-large': '文件过大未比对',
  binary: '二进制未比对'
}

const statusLabel: Record<string, string> = {
  ok: '正常',
  hit: '命中关键字',
  missing: '文件不存在',
  denied: '没有读权限',
  failed: '巡检失败',
  unknown: '未巡检'
}

async function load() {
  if (!hostId.value) {
    ElMessage.warning('请选择主机')
    return
  }
  loading.value = true
  try {
    report.value = await getHostReport(hostId.value)
  } finally {
    loading.value = false
  }
}

function sectionType(section: HostReportSection) {
  if (section.gap) return 'info'
  if (section.problems > 0) return 'warning'
  return 'success'
}

function printReport() {
  window.print()
}

onMounted(async () => {
  const page = await listHosts({ page: 1, pageSize: 300 })
  hosts.value = page.list || []
  if (hosts.value.length > 0) {
    hostId.value = hosts.value[0].id
    load()
  }
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这份报告是<strong>已有巡检数据的汇总</strong>，不是一次新的体检 ——
          打开它不会触发任何 SSH 连接。
          <br />
          平台<strong>没有常驻 agent</strong>：所有主机侧事实都来自 SSH 定时采样，
          间隔从 5 分钟到 30 分钟不等，每一段都标了自己的采集时间。所以这一页
          <strong>不等于</strong>那种由 agent 上报的资产报告 —— 漏洞、补丁、软件包、
          进程与端口清单一项都不在里面，页面底部逐条列出原因。
          <br />
          <strong>「没有数据」和「数据正常」是分开的</strong>：某一段显示成灰色说明这台主机
          压根没登记那类巡检，不是检查通过。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="hostId" filterable placeholder="选择主机" style="width: 280px">
          <el-option
            v-for="host in hosts"
            :key="host.id"
            :value="host.id"
            :label="`${host.name}（${host.address}）`"
          />
        </el-select>
        <el-button type="primary" :loading="loading" @click="load">生成报告</el-button>
        <div class="grow"></div>
        <el-button v-if="report" @click="printReport">打印 / 存 PDF</el-button>
      </div>

      <template v-if="report">
        <div class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
          <el-tag type="info">{{ report.host.name }} · {{ report.host.address }}</el-tag>
          <el-tag :type="report.host.status === 'online' ? 'success' : 'info'">
            {{ report.host.status }}
          </el-tag>
          <el-tag :type="report.problemCount > 0 ? 'warning' : 'success'">
            需要关注 {{ report.problemCount }} 项
          </el-tag>
          <el-tag type="info">没有数据的段落 {{ report.gapCount }}</el-tag>
          <el-tag type="info">生成于 {{ report.generatedAt.slice(0, 19).replace('T', ' ') }}</el-tag>
        </div>

        <el-descriptions title="基本信息" :column="2" border style="margin-bottom: 16px">
          <el-descriptions-item v-for="(item, idx) in report.basic" :key="idx" :label="item.name">
            {{ item.value || '—' }}
            <div style="font-size: 12px; color: #909399">{{ item.source }}</div>
          </el-descriptions-item>
        </el-descriptions>

        <div v-for="section in report.sections" :key="section.title" style="margin-bottom: 16px">
          <el-alert :type="sectionType(section)" :closable="false" style="margin-bottom: 8px">
            <template #title>
              <strong>{{ section.title }}</strong>
              <el-tag v-if="section.stale" size="small" type="danger" style="margin-left: 8px">
                已过期（超过 {{ report.staleAfterHrs }} 小时没更新）
              </el-tag>
              <el-tag v-if="section.checkedAt" size="small" type="info" style="margin-left: 8px">
                采集于 {{ section.checkedAt.slice(0, 19).replace('T', ' ') }}
              </el-tag>
              <div style="font-size: 12px; margin-top: 4px">来源：{{ section.source }}</div>
              <div v-if="section.gap" style="margin-top: 4px">
                <strong>没有数据：</strong>{{ section.gap }}
              </div>
              <div v-else-if="section.summary" style="margin-top: 4px">{{ section.summary }}</div>
            </template>
          </el-alert>

          <el-table v-if="section.items.length" :data="section.items" border stripe size="small">
            <el-table-column label="项" min-width="200">
              <template #default="{ row }">
                <span :style="row.warn ? 'color:#e6a23c;font-weight:600' : ''">
                  {{ row.name || row.path }}
                </span>
                <el-tag v-if="row.critical" size="small" type="danger" style="margin-left: 6px">关键</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="值 / 状态" min-width="160">
              <template #default="{ row }">
                <span v-if="row.value">{{ row.value }}</span>
                <span v-else-if="row.drift">{{ driftLabel[row.drift] || row.drift }}</span>
                <span v-else-if="row.status">{{ statusLabel[row.status] || row.status }}</span>
                <span v-else>—</span>
                <span v-if="row.detail" style="color: #909399; margin-left: 6px">{{ row.detail }}</span>
              </template>
            </el-table-column>
            <el-table-column label="说明" min-width="260">
              <template #default="{ row }">
                <span v-if="row.note" style="color: #909399">{{ row.note }}</span>
                <span v-else>—</span>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <el-card shadow="never" style="margin-top: 16px">
          <template #header>
            <strong>这份报告里没有的东西</strong>
            <span style="color: #909399; margin-left: 8px">
              —— 平台一次都没采集过，所以这里是空白，而不是「未发现问题」
            </span>
          </template>
          <el-table :data="report.notCollected" border stripe size="small">
            <el-table-column prop="item" label="项目" width="220" />
            <el-table-column prop="reason" label="为什么没有" min-width="400" />
          </el-table>
        </el-card>

        <ul style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
          <li v-for="(note, idx) in report.notes" :key="idx">{{ note }}</li>
        </ul>
      </template>

      <el-empty v-else-if="!loading" description="选择一台主机生成报告" />
    </el-card>
  </div>
</template>

<style scoped>
@media print {
  .page-toolbar {
    display: none;
  }
}
</style>
