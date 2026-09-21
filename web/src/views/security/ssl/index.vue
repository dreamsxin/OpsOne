<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  checkAllCertificates,
  checkCertificate,
  createCertificate,
  deleteCertificate,
  getCertificateStats,
  listCertificates,
  updateCertificate,
  type Certificate,
  type CertificateStats
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const checking = ref(false)
const rows = ref<Certificate[]>([])
const total = ref(0)
const stats = ref<CertificateStats | null>(null)
const query = reactive({ page: 1, pageSize: 20, status: '', keyword: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  domain: '',
  port: 443,
  serverName: '',
  alertDays: 30,
  alertEnabled: true,
  enabled: true,
  remark: ''
})
const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  domain: [{ required: true, message: '请输入域名或 IP', trigger: 'blur' }]
}

const detailVisible = ref(false)
const current = ref<Certificate | null>(null)

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  valid: { text: '正常', type: 'success' },
  expiring: { text: '即将到期', type: 'warning' },
  expired: { text: '已过期', type: 'danger' },
  error: { text: '巡检失败', type: 'danger' },
  unknown: { text: '未巡检', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    const [data, s] = await Promise.all([listCertificates(query), getCertificateStats()])
    rows.value = data.list || []
    total.value = data.total
    stats.value = s
  } finally {
    loading.value = false
  }
}

function filterBy(status: string) {
  query.status = status
  query.page = 1
  load()
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    domain: '',
    port: 443,
    serverName: '',
    alertDays: 30,
    alertEnabled: true,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: Certificate) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    domain: row.domain,
    port: row.port,
    serverName: row.serverName,
    alertDays: row.alertDays,
    alertEnabled: row.alertEnabled,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateCertificate(editingId.value, { ...form })
    ElMessage.success('已更新，可再执行一次巡检刷新结果')
  } else {
    const created = await createCertificate({ ...form })
    ElMessage.success(`已创建并巡检：${statusMeta[created.status]?.text || created.status}`)
  }
  dialogVisible.value = false
  load()
}

async function check(row: Certificate) {
  const result = await checkCertificate(row.id)
  if (result.status === 'error') {
    ElMessage.error(`巡检失败：${result.errorMsg}`)
  } else {
    ElMessage.success(`${statusMeta[result.status]?.text}，剩余 ${result.daysLeft} 天`)
  }
  load()
}

async function checkAll() {
  checking.value = true
  try {
    const res = await checkAllCertificates()
    if (res.detail) {
      ElMessage.info(res.detail)
    } else {
      ElMessage.success(
        `巡检 ${res.checked} 个：正常 ${res.valid}，将到期 ${res.expiring}，已过期 ${res.expired}，失败 ${res.error}`
      )
    }
    load()
  } finally {
    checking.value = false
  }
}

async function remove(row: Certificate) {
  await ElMessageBox.confirm(`确认删除「${row.name}」？已产生的告警会保留`, '提示', { type: 'warning' })
  await deleteCertificate(row.id)
  ElMessage.success('已删除')
  load()
}

function openDetail(row: Certificate) {
  current.value = row
  detailVisible.value = true
}

function daysColor(row: Certificate) {
  if (row.status === 'expired') return '#dc2626'
  if (row.status === 'expiring') return '#d97706'
  return '#16a34a'
}

onMounted(load)

</script>

<template>
  <div class="page">
    <el-row :gutter="12">
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('')">
          <div class="value">{{ stats?.total ?? 0 }}</div>
          <div class="label">证书总数</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('expiring')">
          <div class="value" style="color: #d97706">{{ stats?.expiring ?? 0 }}</div>
          <div class="label">即将到期</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('expired')">
          <div class="value" style="color: #dc2626">{{ stats?.expired ?? 0 }}</div>
          <div class="label">已过期</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterBy('error')">
          <div class="value">{{ stats?.error ?? 0 }} / {{ stats?.untrusted ?? 0 }}</div>
          <div class="label">巡检失败 / 链校验不通过</div>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="平台只做探测与到期提醒：连上目标端口读取对端证书，不签发、不托管私钥、不做自动续签。命中到期或巡检失败会按「证书巡检」这个接入源写入告警，再走通知路由派发。"
      />

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 域名 / 颁发者"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.status" placeholder="全部状态" clearable style="width: 140px">
          <el-option v-for="(meta, key) in statusMeta" :key="key" :label="meta.text" :value="key" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'cert:check'" :loading="checking" @click="checkAll">全部巡检</el-button>
        <el-button v-perm="'cert:manage'" type="primary" @click="openCreate">新增证书</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记证书">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="名称" min-width="130" />
        <el-table-column label="目标" min-width="180">
          <template #default="{ row }">
            {{ row.domain }}:{{ row.port }}
            <el-tag v-if="row.serverName" size="small" style="margin-left: 4px">
              SNI {{ row.serverName }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="剩余天数" width="100">
          <template #default="{ row }">
            <span v-if="row.lastCheckAt && row.status !== 'error'" :style="{ color: daysColor(row) }">
              {{ row.daysLeft }}
            </span>
            <span v-else style="color: #6b7280">-</span>
          </template>
        </el-table-column>
        <el-table-column label="链校验" width="90">
          <template #default="{ row }">
            <el-tag v-if="!row.lastCheckAt || row.status === 'error'" size="small" type="info">-</el-tag>
            <el-tag v-else size="small" :type="row.trusted ? 'success' : 'danger'">
              {{ row.trusted ? '通过' : '不通过' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="notAfter" label="到期时间" min-width="180" />
        <el-table-column prop="issuer" label="颁发者" min-width="160" show-overflow-tooltip />
        <el-table-column prop="lastCheckAt" label="最近巡检" min-width="180" />
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <el-button v-perm="'cert:check'" link type="primary" @click="check(row)">巡检</el-button>
            <el-button v-perm="'cert:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'cert:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑证书' : '新增证书'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="110px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="域名 / IP" prop="domain">
          <el-input v-model="form.domain" placeholder="例如 example.com，不要带 https:// 与路径" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
          <span style="margin-left: 8px; color: #6b7280">HTTPS 默认 443，其他 TLS 服务按实际填</span>
        </el-form-item>
        <el-form-item label="SNI">
          <el-input v-model="form.serverName" placeholder="留空则用上面的域名；按 IP 探测时需要填" />
        </el-form-item>
        <el-form-item label="提醒阈值">
          <el-input-number v-model="form.alertDays" :min="1" :max="365" />
          <span style="margin-left: 8px; color: #6b7280">剩余天数低于该值判为「即将到期」</span>
        </el-form-item>
        <el-form-item label="产生告警">
          <el-switch v-model="form.alertEnabled" />
          <span style="margin-left: 8px; color: #6b7280">关闭后只在本页展示状态，不写告警</span>
        </el-form-item>
        <el-form-item label="参与巡检">
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

    <el-drawer v-model="detailVisible" :title="`证书详情 · ${current?.name ?? ''}`" size="45%">
      <el-descriptions v-if="current" :column="1" border>
        <el-descriptions-item label="目标">{{ current.domain }}:{{ current.port }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag size="small" :type="statusMeta[current.status]?.type || 'info'">
            {{ statusMeta[current.status]?.text || current.status }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="主体">{{ current.subject || '-' }}</el-descriptions-item>
        <el-descriptions-item label="颁发者">{{ current.issuer || '-' }}</el-descriptions-item>
        <el-descriptions-item label="SAN 域名">{{ current.dnsNames || '-' }}</el-descriptions-item>
        <el-descriptions-item label="有效期">
          {{ current.notBefore || '-' }} ~ {{ current.notAfter || '-' }}
        </el-descriptions-item>
        <el-descriptions-item label="剩余天数">{{ current.daysLeft }}</el-descriptions-item>
        <el-descriptions-item label="序列号">{{ current.serialNumber || '-' }}</el-descriptions-item>
        <el-descriptions-item label="SHA256 指纹">
          <span style="word-break: break-all">{{ current.fingerprint || '-' }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="链校验">
          <el-tag size="small" :type="current.trusted ? 'success' : 'danger'">
            {{ current.trusted ? '通过' : '不通过' }}
          </el-tag>
          <span v-if="current.verifyError" style="margin-left: 8px; color: #dc2626">
            {{ current.verifyError }}
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="巡检错误">{{ current.errorMsg || '-' }}</el-descriptions-item>
        <el-descriptions-item label="最近巡检">{{ current.lastCheckAt || '未巡检' }}</el-descriptions-item>
        <el-descriptions-item label="备注">{{ current.remark || '-' }}</el-descriptions-item>
      </el-descriptions>
    </el-drawer>
  </div>
</template>
