<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  checkAllDomains,
  checkDomain,
  createDomain,
  deleteDomain,
  domainStats,
  getDepartmentTree,
  importCloudDomains,
  listDomains,
  updateDomain,
  type DeptNode,
  type DomainRecord,
  type DomainStats
} from '@/api'

const loading = ref(false)
const checking = ref(false)
const rows = ref<DomainRecord[]>([])
const total = ref(0)
const stats = ref<DomainStats | null>(null)
const deptTree = ref<DeptNode[]>([])

const query = reactive({
  page: 1,
  pageSize: 20,
  dnsStatus: '',
  expireStatus: '',
  source: '',
  keyword: ''
})

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  registrar: '',
  registeredAt: '',
  expiresAt: '',
  autoRenew: false,
  owner: '',
  purpose: '',
  expectIps: '',
  expectCname: '',
  expectNs: '',
  alertDays: 30,
  alertEnabled: true,
  deptId: 0,
  enabled: true,
  remark: ''
})

const detailVisible = ref(false)
const detailRow = ref<DomainRecord | null>(null)

const rules = {
  name: [{ required: true, message: '请输入域名', trigger: 'blur' }]
}

const dnsStatusMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  ok: { label: '与期望一致', type: 'success' },
  drift: { label: '解析漂移', type: 'danger' },
  unresolved: { label: '解析不到', type: 'danger' },
  error: { label: '巡检失败', type: 'warning' },
  nocheck: { label: '未设期望', type: 'info' },
  unknown: { label: '未巡检', type: 'info' }
}

const expireMeta: Record<string, { label: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  valid: { label: '正常', type: 'success' },
  expiring: { label: '将到期', type: 'warning' },
  expired: { label: '已过期', type: 'danger' },
  unknown: { label: '待补到期日', type: 'info' }
}

const deptNameMap = computed(() => {
  const map = new Map<number, string>()
  const walk = (nodes: DeptNode[]) => {
    for (const node of nodes) {
      map.set(node.id, node.name)
      if (node.children?.length) walk(node.children)
    }
  }
  walk(deptTree.value)
  return map
})

function expireText(row: DomainRecord) {
  if (!row.expiresAt) return '未登记'
  const day = row.expiresAt.slice(0, 10)
  if (row.expireStatus === 'expired') return `${day}（已过期）`
  return `${day}（${row.daysLeft} 天）`
}

async function load() {
  loading.value = true
  try {
    const params: Record<string, any> = { page: query.page, pageSize: query.pageSize }
    if (query.dnsStatus) params.dnsStatus = query.dnsStatus
    if (query.expireStatus) params.expireStatus = query.expireStatus
    if (query.source) params.source = query.source
    if (query.keyword) params.keyword = query.keyword
    const page = await listDomains(params)
    rows.value = page.list
    total.value = page.total
    stats.value = await domainStats()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    registrar: '',
    registeredAt: '',
    expiresAt: '',
    autoRenew: false,
    owner: '',
    purpose: '',
    expectIps: '',
    expectCname: '',
    expectNs: '',
    alertDays: 30,
    alertEnabled: true,
    deptId: 0,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: DomainRecord) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    registrar: row.registrar,
    registeredAt: row.registeredAt ? row.registeredAt.slice(0, 10) : '',
    expiresAt: row.expiresAt ? row.expiresAt.slice(0, 10) : '',
    autoRenew: row.autoRenew,
    owner: row.owner,
    purpose: row.purpose,
    expectIps: row.expectIps,
    expectCname: row.expectCname,
    expectNs: row.expectNs,
    alertDays: row.alertDays,
    alertEnabled: row.alertEnabled,
    deptId: row.deptId,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateDomain(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createDomain({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function runCheck(row: DomainRecord) {
  const updated = await checkDomain(row.id)
  const meta = dnsStatusMeta[updated.dnsStatus]
  if (updated.dnsStatus === 'ok' || updated.dnsStatus === 'nocheck') {
    ElMessage.success(`${meta?.label}：${updated.dnsDetail}`)
  } else {
    ElMessage.warning(`${meta?.label}：${updated.dnsDetail}`)
  }
  load()
}

async function runCheckAll() {
  checking.value = true
  try {
    const s = await checkAllDomains()
    ElMessage.success(
      `共 ${s.checked}：一致 ${s.dnsOk}，漂移 ${s.dnsDrift}，解析异常 ${s.dnsBad}，` +
        `未设期望 ${s.dnsNoCheck}；将到期 ${s.expiring}，已过期 ${s.expired}`
    )
    load()
  } finally {
    checking.value = false
  }
}

async function importFromCloud() {
  const res = await importCloudDomains()
  await ElMessageBox.alert(
    `新增 ${res.created.length} 个：${res.created.join('、') || '（无）'}\n` +
      `已在台账中跳过 ${res.skipped.length} 个\n\n${res.note}`,
    '从云资源同步导入',
    { type: 'info' }
  )
  load()
}

async function remove(row: DomainRecord) {
  await ElMessageBox.confirm(`确认删除域名台账「${row.name}」？它名下的告警会一并恢复。`, '危险操作', {
    type: 'warning'
  })
  await deleteDomain(row.id)
  ElMessage.success('已删除')
  load()
}

function openDetail(row: DomainRecord) {
  detailRow.value = row
  detailVisible.value = true
}

onMounted(async () => {
  deptTree.value = await getDepartmentTree()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          巡检做两件真事：<strong>真去查一次 DNS</strong>（A/AAAA、CNAME、NS）并和你登记的期望值对照，
          以及按注册到期日算剩余天数。两类问题分开告警 —— 「解析被改了」和「忘续费了」不是一回事。
          <br />
          <strong>注册到期日只能人工登记</strong>：云解析接口是 DNS 托管视角，给不出注册到期时间。
          没填到期日的域名会显示成「待补到期日」，平台对它的续费一无所知，也不会发到期提醒。
          <br />
          期望值<strong>留空表示不核对那一类</strong>；三类都不填时状态是「未设期望」，只记录实际解析结果 ——
          「没检查」和「检查通过」不会被混成一个颜色。
          <br />
          <strong>不做</strong>：DNS 记录的增删改（需要注册商/解析商的写接口）、自动续费、WHOIS 查询。
        </template>
      </el-alert>

      <div v-if="stats" class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-tag type="info">共 {{ stats.total }}</el-tag>
        <el-tag type="success">解析一致 {{ stats.dnsOk }}</el-tag>
        <el-tag type="danger">解析漂移 {{ stats.dnsDrift }}</el-tag>
        <el-tag type="danger">解析不到 {{ stats.dnsUnresolve }}</el-tag>
        <el-tag type="info">未设期望 {{ stats.dnsNoCheck }}</el-tag>
        <el-tag type="warning">将到期 {{ stats.expiring }}</el-tag>
        <el-tag type="danger">已过期 {{ stats.expired }}</el-tag>
        <el-tag type="info">待补到期日 {{ stats.noExpiry }}</el-tag>
      </div>

      <div class="page-toolbar">
        <el-select v-model="query.dnsStatus" placeholder="解析状态" clearable style="width: 140px">
          <el-option label="与期望一致" value="ok" />
          <el-option label="解析漂移" value="drift" />
          <el-option label="解析不到" value="unresolved" />
          <el-option label="未设期望" value="nocheck" />
          <el-option label="未巡检" value="unknown" />
        </el-select>
        <el-select v-model="query.expireStatus" placeholder="到期状态" clearable style="width: 140px">
          <el-option label="正常" value="valid" />
          <el-option label="将到期" value="expiring" />
          <el-option label="已过期" value="expired" />
          <el-option label="待补到期日" value="unknown" />
        </el-select>
        <el-select v-model="query.source" placeholder="来源" clearable style="width: 130px">
          <el-option label="人工登记" value="manual" />
          <el-option label="云同步导入" value="cloud" />
        </el-select>
        <el-input
          v-model="query.keyword"
          placeholder="域名 / 责任人 / 用途 / IP"
          clearable
          style="width: 200px"
          @keyup.enter="((query.page = 1), load())"
        />
        <el-button @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'domain:manage'" @click="importFromCloud">从云资源导入</el-button>
        <el-button v-perm="'domain:check'" :loading="checking" @click="runCheckAll">全部巡检</el-button>
        <el-button v-perm="'domain:manage'" type="primary" @click="openCreate">新增域名</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记域名">
        <el-table-column prop="name" label="域名" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link type="primary" :underline="false" @click="openDetail(row)">{{ row.name }}</el-link>
            <el-tag v-if="row.source === 'cloud'" size="small" type="info" style="margin-left: 6px">云</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="解析状态" width="130">
          <template #default="{ row }">
            <el-tag size="small" :type="dnsStatusMeta[row.dnsStatus]?.type || 'info'">
              {{ dnsStatusMeta[row.dnsStatus]?.label || row.dnsStatus }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="实际解析" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.resolvedIps">{{ row.resolvedIps }}</span>
            <span v-else-if="row.resolvedCname">CNAME → {{ row.resolvedCname }}</span>
            <span v-else style="color: #9ca3af">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="registrar" label="注册商" width="110">
          <template #default="{ row }">
            <span v-if="row.registrar">{{ row.registrar }}</span>
            <span v-else style="color: #9ca3af">未登记</span>
          </template>
        </el-table-column>
        <el-table-column label="到期" min-width="160">
          <template #default="{ row }">
            <el-tag size="small" :type="expireMeta[row.expireStatus]?.type || 'info'">
              {{ expireMeta[row.expireStatus]?.label }}
            </el-tag>
            <span style="margin-left: 6px">{{ expireText(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="自动续费" width="90">
          <template #default="{ row }">{{ row.autoRenew ? '已开' : '未开' }}</template>
        </el-table-column>
        <el-table-column label="关联证书" width="130">
          <template #default="{ row }">
            <el-tooltip v-if="row.certCount" :content="row.certNames" placement="top">
              <span>{{ row.certCount }} 张 / 最早 {{ row.certMinDaysLeft }} 天</span>
            </el-tooltip>
            <span v-else style="color: #9ca3af">无</span>
          </template>
        </el-table-column>
        <el-table-column prop="owner" label="责任人" width="100" />
        <el-table-column label="归属部门" width="110">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>
        <el-table-column label="最近巡检" width="160">
          <template #default="{ row }">
            {{ row.lastCheckAt ? row.lastCheckAt.slice(0, 19).replace('T', ' ') : '未巡检' }}
          </template>
        </el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'domain:check'" link type="primary" @click="runCheck(row)">巡检</el-button>
            <el-button v-perm="'domain:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'domain:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next"
        style="margin-top: 12px"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑域名' : '新增域名'" width="620px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="130px">
        <el-divider content-position="left">台账</el-divider>
        <el-form-item label="域名" prop="name">
          <el-input v-model="form.name" placeholder="example.com（不要带协议与端口）" />
        </el-form-item>
        <el-form-item label="注册商">
          <el-input v-model="form.registrar" placeholder="阿里云 / 腾讯云 / GoDaddy…" />
        </el-form-item>
        <el-form-item label="注册日期">
          <el-date-picker v-model="form.registeredAt" type="date" value-format="YYYY-MM-DD" style="width: 100%" />
        </el-form-item>
        <el-form-item label="注册到期日">
          <el-date-picker
            v-model="form.expiresAt"
            type="date"
            value-format="YYYY-MM-DD"
            style="width: 100%"
            placeholder="不填则不做到期提醒"
          />
        </el-form-item>
        <el-form-item label="自动续费">
          <el-switch v-model="form.autoRenew" />
          <el-text type="info" style="margin-left: 8px">仅登记，平台不代你续费</el-text>
        </el-form-item>
        <el-form-item label="责任人">
          <el-input v-model="form.owner" />
        </el-form-item>
        <el-form-item label="用途">
          <el-input v-model="form.purpose" placeholder="官网 / 内部系统 / 邮件…" />
        </el-form-item>
        <el-form-item label="归属部门">
          <el-tree-select
            v-model="form.deptId"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            check-strictly
            style="width: 100%"
            placeholder="未归属"
          />
        </el-form-item>

        <el-divider content-position="left">DNS 期望值（留空表示不核对该类）</el-divider>
        <el-form-item label="期望解析地址">
          <el-input v-model="form.expectIps" placeholder="多个用逗号分隔，如 1.2.3.4,5.6.7.8" />
        </el-form-item>
        <el-form-item label="期望 CNAME">
          <el-input v-model="form.expectCname" placeholder="如 cdn.example.net" />
        </el-form-item>
        <el-form-item label="期望 NS">
          <el-input v-model="form.expectNs" placeholder="多个用逗号分隔，NS 被改通常意味着域名被转走" />
        </el-form-item>

        <el-divider content-position="left">提醒</el-divider>
        <el-form-item label="提前提醒天数">
          <el-input-number v-model="form.alertDays" :min="1" :max="365" />
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="form.alertEnabled" />
          <el-text type="info" style="margin-left: 8px">关掉后仍会巡检，只是不进告警通道</el-text>
        </el-form-item>
        <el-form-item label="参与批量巡检">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="detailRow?.name" size="520px">
      <el-descriptions v-if="detailRow" :column="1" border>
        <el-descriptions-item label="解析状态">
          <el-tag size="small" :type="dnsStatusMeta[detailRow.dnsStatus]?.type || 'info'">
            {{ dnsStatusMeta[detailRow.dnsStatus]?.label }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="结论">{{ detailRow.dnsDetail || '未巡检' }}</el-descriptions-item>
        <el-descriptions-item label="期望地址">{{ detailRow.expectIps || '不核对' }}</el-descriptions-item>
        <el-descriptions-item label="实际地址">{{ detailRow.resolvedIps || '-' }}</el-descriptions-item>
        <el-descriptions-item label="期望 CNAME">{{ detailRow.expectCname || '不核对' }}</el-descriptions-item>
        <el-descriptions-item label="实际 CNAME">{{ detailRow.resolvedCname || '无' }}</el-descriptions-item>
        <el-descriptions-item label="期望 NS">{{ detailRow.expectNs || '不核对' }}</el-descriptions-item>
        <el-descriptions-item label="实际 NS">{{ detailRow.resolvedNs || '-' }}</el-descriptions-item>
        <el-descriptions-item label="使用的 DNS">
          {{ detailRow.dnsServer || '系统 resolver（OPS_DNS_SERVER 未配）' }}
        </el-descriptions-item>
        <el-descriptions-item label="最近巡检">
          {{ detailRow.lastCheckAt ? detailRow.lastCheckAt.slice(0, 19).replace('T', ' ') : '未巡检' }}
        </el-descriptions-item>
        <el-descriptions-item label="注册商">{{ detailRow.registrar || '未登记' }}</el-descriptions-item>
        <el-descriptions-item label="到期">{{ expireText(detailRow) }}</el-descriptions-item>
        <el-descriptions-item label="关联证书">
          {{ detailRow.certCount ? `${detailRow.certNames}（最早 ${detailRow.certMinDaysLeft} 天到期）` : '无' }}
        </el-descriptions-item>
        <el-descriptions-item label="来源">
          {{ detailRow.source === 'cloud' ? '云资源同步导入' : '人工登记' }}
        </el-descriptions-item>
        <el-descriptions-item label="备注">{{ detailRow.remark || '-' }}</el-descriptions-item>
      </el-descriptions>
    </el-drawer>
  </div>
</template>
