<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, type FormInstance } from 'element-plus'
import {
  adoptCloudResource,
  cloudDrift,
  listCloudAccounts,
  listCloudResources,
  listCloudSyncRuns,
  listCredentials,
  runCloudSync,
  type CloudAccount,
  type CloudDrift,
  type CloudDriftItem,
  type CloudResource,
  type CloudSyncRun,
  type Credential
} from '@/api'

const tab = ref<'resources' | 'drift' | 'runs'>('resources')

const accounts = ref<CloudAccount[]>([])
const credentials = ref<Credential[]>([])
const syncing = ref(false)

/* ---------- 资源清单 ---------- */
const resLoading = ref(false)
const resources = ref<CloudResource[]>([])
const resTotal = ref(0)
const resQuery = reactive({
  page: 1,
  pageSize: 20,
  cloudAccountId: '' as number | '',
  resourceType: '',
  gone: '',
  matched: '',
  keyword: ''
})

/* ---------- 漂移 ---------- */
const driftLoading = ref(false)
const drift = ref<CloudDrift | null>(null)
const driftSide = ref<'cloud_only' | 'host_only' | 'gone'>('cloud_only')

/* ---------- 同步记录 ---------- */
const runLoading = ref(false)
const runs = ref<CloudSyncRun[]>([])
const runTotal = ref(0)
const runQuery = reactive({ page: 1, pageSize: 20 })

/* ---------- 纳管 ---------- */
const adoptVisible = ref(false)
const adoptTarget = ref<CloudResource | null>(null)
const adoptFormRef = ref<FormInstance>()
const adoptForm = reactive({
  name: '',
  username: 'root',
  port: 22,
  authType: 'password',
  secret: '',
  credentialId: 0,
  env: 'prod',
  deptId: 0
})

const typeLabels: Record<string, string> = { ecs: '云服务器', domain: '托管域名' }
const triggerLabels: Record<string, string> = { manual: '手动', cron: '定时' }

const aliyunAccounts = computed(() => accounts.value.filter((a) => a.provider === 'aliyun'))

const driftRows = computed(() => (drift.value?.items || []).filter((i) => i.side === driftSide.value))

function statusTagType(row: CloudResource) {
  if (row.gone) return 'danger'
  if (row.status === 'Running' || row.status === 'hosted') return 'success'
  return 'warning'
}

function ipText(row: CloudResource) {
  const parts = [row.privateIps, row.publicIps].filter(Boolean)
  return parts.length ? parts.join(' / ') : '-'
}

// 到期时间只对包年包月有意义；按量付费与域名本来就没有，不要显示成空格让人以为漏了
function expireText(row: CloudResource) {
  if (row.expiredAt) return row.expiredAt.slice(0, 10)
  if (row.resourceType === 'domain') return '接口不提供'
  return row.chargeType === 'PostPaid' ? '按量付费' : '-'
}

async function loadResources() {
  resLoading.value = true
  try {
    const params: Record<string, any> = { page: resQuery.page, pageSize: resQuery.pageSize }
    if (resQuery.cloudAccountId) params.cloudAccountId = resQuery.cloudAccountId
    if (resQuery.resourceType) params.resourceType = resQuery.resourceType
    if (resQuery.gone) params.gone = resQuery.gone
    if (resQuery.matched) params.matched = resQuery.matched
    if (resQuery.keyword) params.keyword = resQuery.keyword
    const page = await listCloudResources(params)
    resources.value = page.list
    resTotal.value = page.total
  } finally {
    resLoading.value = false
  }
}

async function loadDrift() {
  driftLoading.value = true
  try {
    drift.value = await cloudDrift()
  } finally {
    driftLoading.value = false
  }
}

async function loadRuns() {
  runLoading.value = true
  try {
    const page = await listCloudSyncRuns({ ...runQuery })
    runs.value = page.list
    runTotal.value = page.total
  } finally {
    runLoading.value = false
  }
}

async function doSync(account: CloudAccount, resourceType?: string) {
  syncing.value = true
  try {
    const result = await runCloudSync(account.id, resourceType)
    // 同步接口对失败也返回 200 并带回记录，这里按记录里的 status 分别提示，
    // 不能一律「同步完成」——那会把认证失败也说成成功
    const failed = result.filter((r) => r.status !== 'success')
    if (failed.length === 0) {
      ElMessage.success(result.map((r) => `${typeLabels[r.resourceType] || r.resourceType}：${r.message}`).join('；'))
    } else {
      ElMessage.error(failed.map((r) => `${typeLabels[r.resourceType] || r.resourceType}：${r.message}`).join('；'))
    }
    await Promise.all([loadResources(), loadRuns(), loadDrift()])
  } finally {
    syncing.value = false
  }
}

function openAdopt(row: CloudResource) {
  adoptTarget.value = row
  Object.assign(adoptForm, {
    name: row.name || row.resourceId,
    username: 'root',
    port: 22,
    authType: 'password',
    secret: '',
    credentialId: 0,
    env: 'prod',
    deptId: 0
  })
  adoptVisible.value = true
}

function openAdoptFromDrift(item: CloudDriftItem) {
  const found = resources.value.find((r) => r.id === item.cloudResId)
  if (found) {
    openAdopt(found)
    return
  }
  // 清单当前分页里没有这条，就用漂移项里的信息拼一个最小对象
  openAdopt({
    id: item.cloudResId,
    name: item.name,
    resourceId: item.resourceId,
    privateIps: item.address,
    publicIps: ''
  } as CloudResource)
}

async function submitAdopt() {
  const valid = await adoptFormRef.value?.validate().catch(() => false)
  if (!valid || !adoptTarget.value) return
  await adoptCloudResource(adoptTarget.value.id, { ...adoptForm })
  ElMessage.success('已纳管为主机资产')
  adoptVisible.value = false
  await Promise.all([loadResources(), loadDrift()])
}

onMounted(async () => {
  accounts.value = await listCloudAccounts()
  credentials.value = (await listCredentials({ page: 1, pageSize: 200 })).list
  await Promise.all([loadResources(), loadDrift(), loadRuns()])
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          从云账号拉回来的是<strong>云上事实</strong>，不是台账：同步只调只读接口
          （ECS DescribeInstances / 云解析 DescribeDomains），不会改动云上任何东西。
          云上已经查不到的资源会标成「已消失」保留，不直接删 —— 什么时候消失的是对账要用的线索。
          <br />
          未覆盖的部分：域名的<strong>注册</strong>到期时间（云解析接口给不出，需要域名注册 API）、
          DNS 记录的增删改、云账单。腾讯云 / 华为云 / AWS 只登记不同步。
          默认<strong>不自动同步</strong>（<code>OPS_CLOUD_SYNC_SPEC</code> 留空），因为这一项会真的出网。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-dropdown v-perm="'cloud:sync'" :disabled="syncing || aliyunAccounts.length === 0">
          <el-button type="primary" :loading="syncing">
            立即同步<el-icon><arrow-down /></el-icon>
          </el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="a in aliyunAccounts" :key="a.id" @click="doSync(a)">
                {{ a.name }}（{{ a.region || '未配地域' }}）
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
        <el-text v-if="aliyunAccounts.length === 0" type="warning">
          还没有阿里云账号，先去「云账号」页登记一个带只读权限的 AK/SK
        </el-text>
        <div class="grow"></div>
      </div>

      <el-tabs v-model="tab">
        <!-- ---------- 资源清单 ---------- -->
        <el-tab-pane label="资源清单" name="resources">
          <div class="page-toolbar">
            <el-select v-model="resQuery.cloudAccountId" placeholder="全部账号" clearable style="width: 180px">
              <el-option v-for="a in accounts" :key="a.id" :label="a.name" :value="a.id" />
            </el-select>
            <el-select v-model="resQuery.resourceType" placeholder="全部类型" clearable style="width: 130px">
              <el-option label="云服务器" value="ecs" />
              <el-option label="托管域名" value="domain" />
            </el-select>
            <el-select v-model="resQuery.gone" placeholder="全部状态" clearable style="width: 140px">
              <el-option label="云上仍存在" value="false" />
              <el-option label="云上已消失" value="true" />
            </el-select>
            <el-select v-model="resQuery.matched" placeholder="纳管情况" clearable style="width: 140px">
              <el-option label="已关联主机" value="true" />
              <el-option label="未关联主机" value="false" />
            </el-select>
            <el-input
              v-model="resQuery.keyword"
              placeholder="名称 / 实例 ID / IP"
              clearable
              style="width: 200px"
              @keyup.enter="((resQuery.page = 1), loadResources())"
            />
            <el-button @click="((resQuery.page = 1), loadResources())">查询</el-button>
          </div>

          <el-table v-loading="resLoading" :data="resources" border stripe empty-text="还没有同步过云资源">
            <el-table-column label="类型" width="100">
              <template #default="{ row }">{{ typeLabels[row.resourceType] || row.resourceType }}</template>
            </el-table-column>
            <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
            <el-table-column prop="resourceId" label="云上标识" min-width="170" show-overflow-tooltip />
            <el-table-column label="IP / 记录" min-width="190" show-overflow-tooltip>
              <template #default="{ row }">{{ ipText(row) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row }">
                <el-tag size="small" :type="statusTagType(row)">
                  {{ row.gone ? '云上已消失' : row.status }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="regionId" label="地域" width="120" />
            <el-table-column prop="spec" label="规格 / 版本" min-width="130" show-overflow-tooltip />
            <el-table-column label="到期" width="120">
              <template #default="{ row }">{{ expireText(row) }}</template>
            </el-table-column>
            <el-table-column label="关联主机" width="120">
              <template #default="{ row }">
                <el-tag v-if="row.matchedHostId" size="small" type="success">#{{ row.matchedHostId }}</el-tag>
                <span v-else style="color: #9ca3af">未关联</span>
              </template>
            </el-table-column>
            <el-table-column label="最近同步" width="160">
              <template #default="{ row }">{{ row.lastSyncAt?.slice(0, 19).replace('T', ' ') }}</template>
            </el-table-column>
            <el-table-column label="操作" width="110" fixed="right">
              <template #default="{ row }">
                <el-button
                  v-if="row.resourceType === 'ecs' && !row.matchedHostId && !row.gone"
                  v-perm="'cloud:adopt'"
                  link
                  type="primary"
                  @click="openAdopt(row)"
                >
                  纳管
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <el-pagination
            v-model:current-page="resQuery.page"
            v-model:page-size="resQuery.pageSize"
            :total="resTotal"
            :page-sizes="[20, 50, 100]"
            layout="total, sizes, prev, pager, next"
            style="margin-top: 12px"
            @change="loadResources"
          />
        </el-tab-pane>

        <!-- ---------- 漂移对照 ---------- -->
        <el-tab-pane label="漂移对照" name="drift">
          <el-alert v-if="drift" :closable="false" type="info" style="margin-bottom: 12px">
            <template #title>{{ drift.note }}</template>
          </el-alert>

          <el-radio-group v-model="driftSide" style="margin-bottom: 12px">
            <el-radio-button value="cloud_only">云上有、平台未纳管（{{ drift?.cloudOnly ?? 0 }}）</el-radio-button>
            <el-radio-button value="host_only" :disabled="!drift?.syncedOnce">
              平台有、云上没有（{{ drift?.hostOnly ?? 0 }}）
            </el-radio-button>
            <el-radio-button value="gone">云上已消失（{{ drift?.gone ?? 0 }}）</el-radio-button>
          </el-radio-group>

          <el-table v-loading="driftLoading" :data="driftRows" border stripe empty-text="这一类没有差异">
            <el-table-column prop="name" label="名称" min-width="160" show-overflow-tooltip />
            <el-table-column prop="resourceId" label="云上标识" min-width="170" show-overflow-tooltip />
            <el-table-column prop="address" label="地址" min-width="150" />
            <el-table-column prop="status" label="状态" width="110" />
            <el-table-column label="最近同步" width="160">
              <template #default="{ row }">{{ row.lastSyncAt?.slice(0, 19).replace('T', ' ') || '-' }}</template>
            </el-table-column>
            <el-table-column label="操作" width="110" fixed="right">
              <template #default="{ row }">
                <el-button
                  v-if="row.side === 'cloud_only'"
                  v-perm="'cloud:adopt'"
                  link
                  type="primary"
                  @click="openAdoptFromDrift(row)"
                >
                  纳管
                </el-button>
                <router-link v-else-if="row.side === 'host_only'" :to="'/asset/host'">
                  <el-button link type="primary">看主机</el-button>
                </router-link>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <!-- ---------- 同步记录 ---------- -->
        <el-tab-pane label="同步记录" name="runs">
          <el-table v-loading="runLoading" :data="runs" border stripe empty-text="还没有同步记录">
            <el-table-column prop="accountName" label="云账号" min-width="150" />
            <el-table-column label="类型" width="100">
              <template #default="{ row }">{{ typeLabels[row.resourceType] || row.resourceType }}</template>
            </el-table-column>
            <el-table-column prop="regionId" label="地域" width="120" />
            <el-table-column label="结果" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
                  {{ row.status === 'success' ? '成功' : '失败' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="触发" width="90">
              <template #default="{ row }">{{ triggerLabels[row.trigger] || row.trigger }}</template>
            </el-table-column>
            <el-table-column prop="operator" label="操作人" width="110" />
            <el-table-column prop="message" label="说明" min-width="280" show-overflow-tooltip />
            <el-table-column label="耗时" width="100">
              <template #default="{ row }">{{ row.durationMs }} ms</template>
            </el-table-column>
            <el-table-column label="开始时间" width="160">
              <template #default="{ row }">{{ row.startedAt?.slice(0, 19).replace('T', ' ') }}</template>
            </el-table-column>
          </el-table>

          <el-pagination
            v-model:current-page="runQuery.page"
            v-model:page-size="runQuery.pageSize"
            :total="runTotal"
            :page-sizes="[20, 50]"
            layout="total, sizes, prev, pager, next"
            style="margin-top: 12px"
            @change="loadRuns"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="adoptVisible" title="纳管为主机资产" width="520px">
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          纳管会用云上私网 IP 建一台主机资产。登录凭据必须由你提供 ——
          云 API 拿不到主机口令，不填就等于建一条连不上的假数据。
        </template>
      </el-alert>
      <el-form ref="adoptFormRef" :model="adoptForm" label-width="110px">
        <el-form-item label="云上资源">
          <el-text>{{ adoptTarget?.resourceId }}（{{ adoptTarget?.privateIps || adoptTarget?.publicIps }}）</el-text>
        </el-form-item>
        <el-form-item label="主机名称" prop="name" :rules="[{ required: true, message: '请输入主机名称' }]">
          <el-input v-model="adoptForm.name" />
        </el-form-item>
        <el-form-item label="登录用户" prop="username" :rules="[{ required: true, message: '请输入登录用户' }]">
          <el-input v-model="adoptForm.username" />
        </el-form-item>
        <el-form-item label="SSH 端口">
          <el-input-number v-model="adoptForm.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="引用凭证库">
          <el-select v-model="adoptForm.credentialId" clearable style="width: 100%" placeholder="不引用，下面填凭据">
            <el-option v-for="c in credentials" :key="c.id" :label="c.name" :value="c.id" />
          </el-select>
        </el-form-item>
        <template v-if="!adoptForm.credentialId">
          <el-form-item label="认证方式">
            <el-radio-group v-model="adoptForm.authType">
              <el-radio value="password">密码</el-radio>
              <el-radio value="key">私钥</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item label="密码 / 私钥">
            <el-input
              v-model="adoptForm.secret"
              :type="adoptForm.authType === 'key' ? 'textarea' : 'password'"
              :rows="4"
              show-password
            />
          </el-form-item>
        </template>
        <el-form-item label="环境">
          <el-select v-model="adoptForm.env" style="width: 100%">
            <el-option label="开发" value="dev" />
            <el-option label="测试" value="test" />
            <el-option label="生产" value="prod" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="adoptVisible = false">取消</el-button>
        <el-button type="primary" @click="submitAdopt">纳管</el-button>
      </template>
    </el-dialog>
  </div>
</template>
