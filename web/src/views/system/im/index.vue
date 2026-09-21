<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  checkImApp,
  createImApp,
  deleteImApp,
  listCompanies,
  listImAccounts,
  listImApps,
  listImSyncRuns,
  listRoles,
  syncImApp,
  unbindImAccount,
  updateImApp,
  type Company,
  type ImAccount,
  type ImApp,
  type ImSyncReport,
  type ImSyncRun,
  type Role
} from '@/api'

const tab = ref('apps')
const loading = reactive({ list: false, submit: false, check: false, sync: false, sub: false })

const apps = ref<{ app: ImApp; boundCount: number }[]>([])
const companies = ref<Company[]>([])
const roles = ref<Role[]>([])

const providerLabel: Record<string, string> = {
  wecom: '企业微信',
  dingtalk: '钉钉',
  feishu: '飞书'
}
const statusMeta: Record<string, { text: string; type: 'success' | 'info' | 'danger' }> = {
  healthy: { text: '可用', type: 'success' },
  unknown: { text: '未检查', type: 'info' },
  error: { text: '异常', type: 'danger' }
}
const actionMeta: Record<string, { text: string; type: 'success' | 'warning' | 'info' | 'danger' }> = {
  create: { text: '新建', type: 'success' },
  update: { text: '更新', type: 'warning' },
  bind: { text: '绑定已有账号', type: 'warning' },
  disable: { text: '停用', type: 'danger' },
  skip: { text: '跳过', type: 'info' }
}

const companyName = (id: number) => companies.value.find((x) => x.id === id)?.name || '-'
const roleName = (id: number) => roles.value.find((x) => x.id === id)?.name || '未指定'

async function load() {
  loading.list = true
  try {
    apps.value = (await listImApps()).list || []
  } finally {
    loading.list = false
  }
}

// ---------- 应用配置 ----------

const form = reactive({
  visible: false,
  id: 0,
  name: '',
  provider: 'wecom' as ImApp['provider'],
  corpId: '',
  appSecret: '',
  agentId: '',
  baseUrl: '',
  rootDeptId: '',
  targetCompanyId: 0,
  defaultRoleId: 0,
  disableMissing: true,
  enabled: true,
  remark: '',
  loginEnabled: false,
  redirectUri: '',
  loginRedirect: ''
})

const corpIdLabel = computed(() => {
  if (form.provider === 'dingtalk') return 'AppKey'
  if (form.provider === 'feishu') return 'App ID'
  return 'CorpID'
})

function openForm(row?: ImApp) {
  form.visible = true
  form.id = row?.id ?? 0
  form.name = row?.name ?? ''
  form.provider = row?.provider ?? 'wecom'
  form.corpId = row?.corpId ?? ''
  form.appSecret = ''
  form.agentId = row?.agentId ?? ''
  form.baseUrl = row?.baseUrl ?? ''
  form.rootDeptId = row?.rootDeptId ?? ''
  form.targetCompanyId = row?.targetCompanyId ?? companies.value[0]?.id ?? 0
  form.defaultRoleId = row?.defaultRoleId ?? 0
  form.disableMissing = row?.disableMissing ?? true
  form.enabled = row?.enabled ?? true
  form.remark = row?.remark ?? ''
  form.loginEnabled = row?.loginEnabled ?? false
  form.redirectUri = row?.redirectUri ?? ''
  form.loginRedirect = row?.loginRedirect ?? ''
}

async function submit() {
  loading.submit = true
  try {
    const payload = {
      name: form.name,
      provider: form.provider,
      corpId: form.corpId,
      appSecret: form.appSecret,
      agentId: form.agentId,
      baseUrl: form.baseUrl,
      rootDeptId: form.rootDeptId,
      targetCompanyId: form.targetCompanyId,
      defaultRoleId: form.defaultRoleId,
      disableMissing: form.disableMissing,
      enabled: form.enabled,
      remark: form.remark,
      loginEnabled: form.loginEnabled,
      redirectUri: form.redirectUri,
      loginRedirect: form.loginRedirect
    }
    if (form.id) {
      await updateImApp(form.id, payload)
      ElMessage.success('已保存')
    } else {
      const res = await createImApp(payload)
      if (res.check?.status === 'healthy') {
        ElMessage.success('已创建，' + res.check.detail)
      } else {
        ElMessage.warning('已创建，但检查没通过：' + (res.check?.detail || '未知原因'))
      }
    }
    form.visible = false
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  } finally {
    loading.submit = false
  }
}

async function check(row: ImApp) {
  loading.check = true
  try {
    const res = await checkImApp(row.id)
    if (res.status === 'healthy') ElMessage.success(res.detail)
    else ElMessage.error(res.detail)
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '检查失败')
  } finally {
    loading.check = false
  }
}

async function remove(row: ImApp) {
  await ElMessageBox.confirm(
    `删除「${row.name}」？绑定关系会一并清掉，但已经同步出来的部门和用户不会被删除。`,
    '删除 IM 应用',
    { type: 'warning' }
  )
  const res = await deleteImApp(row.id)
  ElMessage.success(res.note || '已删除')
  load()
}

// ---------- 同步 ----------

const syncResult = reactive({
  visible: false,
  app: null as ImApp | null,
  dryRun: true,
  report: null as ImSyncReport | null,
  error: ''
})

async function runSync(row: ImApp, dryRun: boolean) {
  if (!dryRun) {
    await ElMessageBox.confirm(
      '执行同步会真的写入部门与用户：新人建账号、已有同名账号建立绑定、IM 侧查不到的人' +
        '（若开了开关）会被停用。**不会删除任何部门或用户，也不会改已有用户的角色与口令。**' +
        '建议先预演看清改动。',
      '执行组织同步',
      { type: 'warning', dangerouslyUseHTMLString: false }
    )
  }
  loading.sync = true
  syncResult.visible = true
  syncResult.app = row
  syncResult.dryRun = dryRun
  syncResult.report = null
  syncResult.error = ''
  try {
    const res = await syncImApp(row.id, dryRun)
    syncResult.report = res.report
    load()
    if (tab.value === 'runs') loadRuns()
    if (tab.value === 'accounts') loadAccounts()
  } catch (err: any) {
    syncResult.error = err?.message || '同步失败'
  } finally {
    loading.sync = false
  }
}

// ---------- 绑定关系 ----------

const accounts = ref<ImAccount[]>([])
const accountTotal = ref(0)
const accountQuery = reactive({ page: 1, pageSize: 20, appId: '' as number | '', keyword: '' })

async function loadAccounts() {
  loading.sub = true
  try {
    const data = await listImAccounts(accountQuery)
    accounts.value = data.list || []
    accountTotal.value = data.total
  } finally {
    loading.sub = false
  }
}

async function unbind(row: ImAccount) {
  await ElMessageBox.confirm(
    `解绑「${row.imName || row.imUserId}」与平台账号「${row.username}」？平台账号本身不受影响。`,
    '解绑',
    { type: 'warning' }
  )
  const res = await unbindImAccount(row.id)
  ElMessage.success(res.note || '已解绑')
  loadAccounts()
}

// ---------- 同步历史 ----------

const runs = ref<ImSyncRun[]>([])
const runTotal = ref(0)
const runQuery = reactive({ page: 1, pageSize: 20, appId: '' as number | '', status: '' })

async function loadRuns() {
  loading.sub = true
  try {
    const data = await listImSyncRuns(runQuery)
    runs.value = data.list || []
    runTotal.value = data.total
  } finally {
    loading.sub = false
  }
}

let accountsLoaded = false
let runsLoaded = false
function onTabChange(name: string) {
  if (name === 'accounts' && !accountsLoaded) {
    accountsLoaded = true
    loadAccounts()
  }
  if (name === 'runs' && !runsLoaded) {
    runsLoaded = true
    loadRuns()
  }
}

onMounted(async () => {
  try {
    companies.value = await listCompanies()
    roles.value = await listRoles()
  } catch {
    // 公司/角色读不到只影响下拉，保存时后端还会再校验
  }
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-tabs v-model="tab" @tab-change="onTabChange">
        <el-tab-pane label="IM 应用" name="apps">
          <el-alert type="warning" :closable="false" show-icon class="notice">
            这里用的是<strong>企业自建应用的凭据</strong>（能读通讯录），与「通知渠道」里的群机器人是两套东西。
            同步遵循<strong>只补不毁</strong>：部门只新建与改名、用户只新建/绑定/更新资料，
            <strong>从不删除</strong>；IM 侧查不到的人改为停用（可关）；
            <strong>已有用户的角色与口令一律不动</strong>（新建账号才给默认角色，口令是随机的）。
            建议每次先「预演」看清改动再执行。
          </el-alert>

          <div class="page-toolbar">
            <el-button @click="load">刷新</el-button>
            <div class="flex-1" />
            <el-button v-perm="'im:manage'" type="primary" @click="openForm()">新增应用</el-button>
          </div>

          <el-table v-loading="loading.list" :data="apps" style="width: 100%">
            <el-table-column label="应用" min-width="180">
              <template #default="{ row }">
                <div>{{ row.app.name }}</div>
                <div class="sub">{{ providerLabel[row.app.provider] || row.app.provider }}</div>
              </template>
            </el-table-column>
            <el-table-column label="企业标识 / 起点部门" min-width="220">
              <template #default="{ row }">
                <div>{{ row.app.corpId }}</div>
                <div class="sub">
                  起点 {{ row.app.rootDeptId }}
                  <span v-if="row.app.baseUrl"> · 自定义地址 {{ row.app.baseUrl }}</span>
                </div>
              </template>
            </el-table-column>
            <el-table-column label="同步到" min-width="150">
              <template #default="{ row }">
                <div>{{ companyName(row.app.targetCompanyId) }}</div>
                <div class="sub">新账号角色：{{ roleName(row.app.defaultRoleId) }}</div>
              </template>
            </el-table-column>
            <el-table-column label="离职处理" width="110">
              <template #default="{ row }">
                <el-tag :type="row.app.disableMissing ? 'warning' : 'info'" size="small">
                  {{ row.app.disableMissing ? '停用账号' : '不处理' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="状态" min-width="200">
              <template #default="{ row }">
                <el-tag :type="statusMeta[row.app.status]?.type || 'info'" size="small">
                  {{ statusMeta[row.app.status]?.text || row.app.status }}
                </el-tag>
                <el-tag v-if="!row.app.enabled" type="info" size="small" class="gap">已停用</el-tag>
                <div class="sub">已绑定 {{ row.boundCount }} 人</div>
                <div v-if="row.app.lastSyncAt" class="sub">上次同步 {{ row.app.lastSyncAt }}</div>
                <div v-if="row.app.lastError" class="sub danger">{{ row.app.lastError }}</div>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="260" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" :loading="loading.check" @click="check(row.app)">
                  检查
                </el-button>
                <el-button
                  v-perm="'im:sync'"
                  link
                  type="primary"
                  :disabled="!row.app.enabled"
                  @click="runSync(row.app, true)"
                >
                  预演
                </el-button>
                <el-button
                  v-perm="'im:sync'"
                  link
                  type="warning"
                  :disabled="!row.app.enabled"
                  @click="runSync(row.app, false)"
                >
                  执行同步
                </el-button>
                <el-button v-perm="'im:manage'" link type="primary" @click="openForm(row.app)">
                  编辑
                </el-button>
                <el-button v-perm="'im:manage'" link type="danger" @click="remove(row.app)">
                  删除
                </el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="账号绑定" name="accounts">
          <el-alert type="info" :closable="false" show-icon class="notice">
            IM 账号与平台账号的对应关系。同步时先按这张表找人，找不到再按同名账号绑定，
            都没有才新建。解绑只删这条对应关系，平台账号不受影响。
          </el-alert>
          <div class="page-toolbar">
            <el-select
              v-model="accountQuery.appId"
              placeholder="全部应用"
              clearable
              style="width: 180px"
              @change="loadAccounts"
            >
              <el-option v-for="item in apps" :key="item.app.id" :label="item.app.name" :value="item.app.id" />
            </el-select>
            <el-input
              v-model="accountQuery.keyword"
              placeholder="平台账号 / IM 姓名 / IM ID"
              clearable
              style="width: 240px"
              @keyup.enter="loadAccounts"
            />
            <el-button @click="loadAccounts">查询</el-button>
          </div>
          <el-table v-loading="loading.sub" :data="accounts" style="width: 100%">
            <el-table-column label="平台账号" min-width="140" prop="username" />
            <el-table-column label="IM 账号" min-width="200">
              <template #default="{ row }">
                <div>{{ row.imName || '-' }}</div>
                <div class="sub">{{ row.imUserId }}</div>
              </template>
            </el-table-column>
            <el-table-column label="IM 部门" min-width="200" prop="imDeptPath" />
            <el-table-column label="联系方式" min-width="180">
              <template #default="{ row }">
                <div>{{ row.imEmail || '-' }}</div>
                <div class="sub">{{ row.imMobile || '-' }}</div>
              </template>
            </el-table-column>
            <el-table-column label="最近同步" width="180" prop="lastSyncAt" />
            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button v-perm="'im:manage'" link type="danger" @click="unbind(row)">解绑</el-button>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination
            v-model:current-page="accountQuery.page"
            class="page-pager"
            layout="total, prev, pager, next"
            :total="accountTotal"
            :page-size="accountQuery.pageSize"
            @current-change="loadAccounts"
          />
        </el-tab-pane>

        <el-tab-pane label="同步历史" name="runs">
          <el-alert type="info" :closable="false" show-icon class="notice">
            预演也会留记录（标「预演」），方便回答「上次同步到底改了什么」。
          </el-alert>
          <div class="page-toolbar">
            <el-select
              v-model="runQuery.appId"
              placeholder="全部应用"
              clearable
              style="width: 180px"
              @change="loadRuns"
            >
              <el-option v-for="item in apps" :key="item.app.id" :label="item.app.name" :value="item.app.id" />
            </el-select>
            <el-select v-model="runQuery.status" placeholder="全部状态" clearable style="width: 130px" @change="loadRuns">
              <el-option label="成功" value="success" />
              <el-option label="失败" value="failed" />
            </el-select>
            <el-button @click="loadRuns">查询</el-button>
          </div>
          <el-table v-loading="loading.sub" :data="runs" style="width: 100%">
            <el-table-column label="时间" width="170" prop="createdAt" />
            <el-table-column label="应用" min-width="140">
              <template #default="{ row }">
                <div>{{ row.appName }}</div>
                <div class="sub">{{ providerLabel[row.provider] || row.provider }}</div>
              </template>
            </el-table-column>
            <el-table-column label="方式" width="90">
              <template #default="{ row }">
                <el-tag :type="row.dryRun ? 'info' : 'warning'" size="small">
                  {{ row.dryRun ? '预演' : '执行' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="部门" width="140">
              <template #default="{ row }">
                共 {{ row.deptTotal }}，新建 {{ row.deptCreated }}，更新 {{ row.deptUpdated }}
              </template>
            </el-table-column>
            <el-table-column label="成员" min-width="240">
              <template #default="{ row }">
                共 {{ row.userTotal }}：新建 {{ row.userCreated }}、绑定 {{ row.userBound }}、
                更新 {{ row.userUpdated }}、停用 {{ row.userDisabled }}、跳过 {{ row.userSkipped }}
              </template>
            </el-table-column>
            <el-table-column label="操作人" width="110" prop="operator" />
            <el-table-column label="结果" min-width="180">
              <template #default="{ row }">
                <el-tag :type="row.status === 'success' ? 'success' : 'danger'" size="small">
                  {{ row.status === 'success' ? '成功' : '失败' }}
                </el-tag>
                <span class="sub"> {{ row.costMs }} ms</span>
                <div v-if="row.errorMsg" class="sub danger">{{ row.errorMsg }}</div>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination
            v-model:current-page="runQuery.page"
            class="page-pager"
            layout="total, prev, pager, next"
            :total="runTotal"
            :page-size="runQuery.pageSize"
            @current-change="loadRuns"
          />
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-dialog v-model="form.visible" :title="form.id ? '编辑 IM 应用' : '新增 IM 应用'" width="640px">
      <el-form label-width="130px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="如 公司企业微信" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.provider">
            <el-radio-button value="wecom">企业微信</el-radio-button>
            <el-radio-button value="dingtalk">钉钉</el-radio-button>
            <el-radio-button value="feishu">飞书</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="corpIdLabel">
          <el-input v-model="form.corpId" />
        </el-form-item>
        <el-form-item label="应用密钥">
          <el-input
            v-model="form.appSecret"
            type="password"
            show-password
            :placeholder="form.id ? '留空表示不修改' : '自建应用的 secret'"
          />
        </el-form-item>
        <el-form-item v-if="form.provider === 'wecom'" label="AgentID">
          <el-input v-model="form.agentId" placeholder="选填，后续扫码登录会用到" />
        </el-form-item>
        <el-form-item label="接口地址">
          <el-input v-model="form.baseUrl" placeholder="留空用各家默认地址；走私有代理时填" />
        </el-form-item>
        <el-form-item label="同步起点部门">
          <el-input
            v-model="form.rootDeptId"
            :placeholder="form.provider === 'feishu' ? '留空默认 0（根部门）' : '留空默认 1（根部门）'"
          />
          <div class="hint">只想同步某个子部门时填它的 ID，能少拉很多人。</div>
        </el-form-item>
        <el-form-item label="同步到公司">
          <el-select v-model="form.targetCompanyId" style="width: 100%">
            <el-option v-for="item in companies" :key="item.id" :label="item.name" :value="item.id" />
          </el-select>
          <div class="hint">IM 的部门树会挂在这个公司下面。</div>
        </el-form-item>
        <el-form-item label="新账号角色">
          <el-select v-model="form.defaultRoleId" clearable placeholder="不给角色" style="width: 100%">
            <el-option v-for="item in roles" :key="item.id" :label="item.name" :value="item.id" />
          </el-select>
          <div class="hint">只在新建账号时生效；之后手工调过的角色，同步不会覆盖。</div>
        </el-form-item>
        <el-form-item label="离职处理">
          <el-switch v-model="form.disableMissing" />
          <span class="hint inline">IM 侧查不到的人，停用平台账号（永不删除）</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-divider>
          <span style="font-size: 12px; color: var(--el-text-color-secondary)">扫码登录（可选）</span>
        </el-divider>
        <el-form-item label="允许扫码登录">
          <el-switch v-model="form.loginEnabled" />
          <span class="hint inline">只有已绑定平台账号的成员能扫进来，不会自动建账号</span>
        </el-form-item>
        <template v-if="form.loginEnabled">
          <el-form-item label="回调地址">
            <el-input
              v-model="form.redirectUri"
              placeholder="https://ops.example.com/api/v1/auth/im/callback"
            />
            <div class="hint">必须与 IM 后台登记的完全一致，且指向平台的 /api/v1/auth/im/callback。</div>
          </el-form-item>
          <el-form-item label="登录后跳转">
            <el-input v-model="form.loginRedirect" placeholder="https://ops.example.com/" />
            <div class="hint">
              平台只在这个地址后面追加一次性 ticket，<strong>不会把令牌放进 URL</strong>。
            </div>
          </el-form-item>
        </template>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="form.visible = false">取消</el-button>
        <el-button type="primary" :loading="loading.submit" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog
      v-model="syncResult.visible"
      :title="(syncResult.dryRun ? '预演：' : '同步结果：') + (syncResult.app?.name || '')"
      width="820px"
    >
      <div v-loading="loading.sync">
        <el-alert v-if="syncResult.error" type="error" :closable="false" show-icon class="notice">
          {{ syncResult.error }}
        </el-alert>
        <template v-if="syncResult.report">
          <el-alert :type="syncResult.dryRun ? 'info' : 'success'" :closable="false" class="notice">
            部门：共 {{ syncResult.report.deptTotal }}，新建 {{ syncResult.report.deptCreated }}，
            更新 {{ syncResult.report.deptUpdated }}；
            成员：共 {{ syncResult.report.userTotal }}，新建 {{ syncResult.report.userCreated }}，
            绑定 {{ syncResult.report.userBound }}，更新 {{ syncResult.report.userUpdated }}，
            停用 {{ syncResult.report.userDisabled }}，跳过 {{ syncResult.report.userSkipped }}
            <strong v-if="syncResult.report.dryRun">（预演，未写入任何数据）</strong>
          </el-alert>
          <el-table :data="syncResult.report.changes" size="small" max-height="420">
            <el-table-column label="类型" width="80">
              <template #default="{ row }">{{ row.kind === 'dept' ? '部门' : '成员' }}</template>
            </el-table-column>
            <el-table-column label="动作" width="130">
              <template #default="{ row }">
                <el-tag :type="actionMeta[row.action]?.type || 'info'" size="small">
                  {{ actionMeta[row.action]?.text || row.action }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="对象" min-width="150" prop="name" />
            <el-table-column label="IM ID" min-width="120" prop="imId" />
            <el-table-column label="说明" min-width="260" prop="detail" />
          </el-table>
        </template>
      </div>
      <template #footer>
        <el-button @click="syncResult.visible = false">关闭</el-button>
        <el-button
          v-if="syncResult.dryRun && syncResult.report && !syncResult.error"
          v-perm="'im:sync'"
          type="warning"
          :loading="loading.sync"
          @click="syncResult.app && runSync(syncResult.app, false)"
        >
          按这个结果执行
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 12px;
}
.flex-1 {
  flex: 1;
}
.sub {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.danger {
  color: var(--el-color-danger);
}
.gap {
  margin-left: 6px;
}
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.hint.inline {
  margin-left: 8px;
}
</style>
