import http, { request, TOKEN_KEY, type ApiBody, type PageData } from './request'


// ---------- 类型 ----------

export interface LoginResult {
  token: string
  expiresAt: number
  user: { id: number; username: string; nickname: string }
  /** 口令正确但还缺动态验证码时，后端只返回这两个字段 */
  totpRequired?: boolean
  detail?: string
}

export interface Profile {
  id: number
  username: string
  nickname: string
  email: string
  lastLoginAt: string | null
  roles: { id: number; code: string; name: string }[]
  permissions: string[]
  totpEnabled: boolean
  totpEnforced: boolean
}

export interface MenuNode {
  id: number
  name: string
  title: string
  path: string
  component: string
  icon: string
  sort: number
  hidden: boolean
  children?: MenuNode[]
}

export interface Host {
  id: number
  name: string
  address: string
  port: number
  username: string
  authType: 'password' | 'key'
  env: 'dev' | 'test' | 'prod'
  tags: string
  osInfo: string
  status: 'online' | 'offline' | 'unknown'
  checkedAt: string | null
  remark: string
  proxyHostId: number
  deptId: number
  createdBy: number
  createdAt: string
  /** 引用凭证库里的共享凭据，0 表示用本机自填的口令 / 私钥 */
  credentialId: number
}



export interface SessionCommand {
  id: number
  sessionId: number
  command: string
  risk: 'normal' | 'warn' | 'blocked'
  ruleId: number
  /** 命中规则时抄下来的规则说明，规则后来被改名或删掉也还能看出原因 */
  ruleDesc: string
  offsetMs: number
  createdAt: string
}

export interface TerminalSession {
  id: number
  hostId: number
  hostName: string
  address: string
  loginUser: string
  userId: number
  username: string
  clientIp: string
  viaProxy: string
  status: 'active' | 'closed' | 'error'
  errorMsg: string
  commandCount: number
  blockedCount: number
  startedAt: string
  endedAt: string | null
  durationMs: number
  commands?: SessionCommand[]
}

export interface CommandRule {
  id: number
  pattern: string
  description: string
  action: 'block' | 'warn'
  enabled: boolean
}

export interface FileEntry {
  name: string
  path: string
  size: number
  mode: string
  isDir: boolean
  modTime: string
}

export interface FileAudit {
  id: number
  hostId: number
  hostName: string
  address: string
  username: string
  action: 'upload' | 'download' | 'delete' | 'mkdir' | 'rename'
  path: string
  targetPath: string
  size: number
  status: 'success' | 'failed'
  errorMsg: string
  clientIp: string
  createdAt: string
}

export interface CronJob {
  id: number
  name: string
  spec: string
  command: string
  hostIds: number[]
  timeout: number
  enabled: boolean
  /** 保存任务时是否确认过「会定期动生产主机」，调度触发时用的就是这次确认 */
  prodConfirmed: boolean
  operator: string
  runCount: number
  lastStatus: string
  lastRunAt: string | null
  lastJobId: number
  createdAt: string
}


export interface Alert {
  id: number
  sourceId: number
  sourceName: string
  fingerprint: string
  title: string
  summary: string
  severity: 'critical' | 'warning' | 'info'
  status: 'firing' | 'acked' | 'resolved'
  labels: string
  value: string
  count: number
  firstSeenAt: string
  lastSeenAt: string
  ackBy: string
  ackAt: string | null
  resolvedAt: string | null
  handleNote: string
}

export interface AlertStats {
  total: number
  firing: number
  acked: number
  resolved: number
  critical: number
  warning: number
}

export interface AlertSource {
  id: number
  name: string
  token: string
  enabled: boolean
  remark: string
  receivedCount: number
  lastSeenAt: string | null
  createdAt: string
}

export interface NotifyChannel {
  id: number
  name: string
  type: 'webhook' | 'email' | 'silent' | 'wecom' | 'dingtalk' | 'feishu'
  url: string
  headerKey: string
  recipients: string
  templateCode: string
  /** 群机器人 @ 名单：企业微信填 userid、钉钉填手机号；飞书只支持 @所有人 */
  mentionList: string
  mentionAll: boolean
  /** 用哪个发件邮箱发（仅 email）。0 = 用默认发件邮箱 */
  mailAccountId: number
  enabled: boolean
  remark: string
}


export interface NotifyRoute {
  id: number
  name: string
  priority: number
  matchSeverity: string
  matchLabels: string
  channelIds: number[]
  isDefault: boolean
  enabled: boolean
}

export interface NotifyRecord {
  id: number
  alertId: number
  alertTitle: string
  routeId: number
  routeName: string
  channelId: number
  channelName: string
  status: 'success' | 'failed'
  httpStatus: number
  errorMsg: string
  costMs: number
  createdAt: string
}

export interface NameCount {
  name: string
  count: number
}

export interface AlertSituation {
  range: string
  since: string
  total: number
  trend: { time: string; critical: number; warning: number; info: number }[]
  bySeverity: NameCount[]
  byStatus: NameCount[]
  bySource: NameCount[]
  topLabels: NameCount[]
  handleStats: {
    ackedNum: number
    resolvedNum: number
    avgAckSec: number
    avgResolveSec: number
  }
}

export interface Announcement {
  id: number
  title: string
  content: string
  level: 'info' | 'warning'
  published: boolean
  publishedAt: string | null
  publisher: string
  createdAt: string
}

export interface Message {
  id: number
  userId: number
  type: 'announcement' | 'alert'
  title: string
  content: string
  level: 'info' | 'warning' | 'critical'
  refId: number
  read: boolean
  readAt: string | null
  createdAt: string
}

export interface MessageSummary {
  unread: number
  unreadAlert: number
  latest: Message[]
}





export interface ExecResult {
  id: number
  jobId: number
  hostId: number
  hostName: string
  address: string
  status: 'success' | 'failed' | 'timeout'
  exitCode: number
  stdout: string
  stderr: string
  costMs: number
}

export interface ExecJob {
  id: number
  name: string
  command: string
  timeout: number
  status: string
  operator: string
  total: number
  successNum: number
  failedNum: number
  startedAt: string
  finishedAt: string | null
  results?: ExecResult[]
}

export interface Role {
  id: number
  code: string
  name: string
  description: string
  dataScope: 'all' | 'dept' | 'dept_below' | 'self' | 'custom'
  dataDeptIds: number[]
  menus?: { id: number }[]
}

export interface User {
  id: number
  username: string
  nickname: string
  email: string
  deptId: number
  status: number
  lastLoginAt: string | null
  totpEnabled: boolean
  totpBoundAt: string | null
  roles?: Role[]
}

export interface Company {
  id: number
  name: string
  code: string
  remark: string
  createdAt: string
}

export interface DeptNode {
  id: number
  companyId: number
  parentId: number
  name: string
  code: string
  leader: string
  sort: number
  userCount: number
  hostCount: number
  children?: DeptNode[]
}

export interface DataScopeDiagnosis {
  user: { id: number; username: string; deptId: number }
  roles: { id: number; name: string; code: string; dataScope: string; dataDeptIds: number[] }[]
  scope: { all: boolean; deptIds: number[]; includeSelf: boolean }
  deptNames: string[]
  visibleHosts: number
  totalHosts: number
}

export interface SysConfig {
  id: number
  group: string
  key: string
  value: string
  type: 'string' | 'int' | 'bool' | 'text'
  label: string
  remark: string
  builtin: boolean
  updatedBy: string
  updatedAt: string
  // secret 这一项的值是密钥（如 SMTP 口令）：接口不回传取值，
  // hasValue 只说明配过没有，提交空串表示不修改
  secret?: boolean
  hasValue?: boolean
}

export interface Branding {
  platformName: string
  loginNotice: string
}

export interface NameCountRow {
  name: string
  count: number
}

export interface PersonalWorkbench {
  profile: { username: string; nickname: string; deptId: number; lastLoginAt: string | null }
  todo: { unreadMessages: number; firingAlerts: number; criticalAlerts: number }
  mine: { hosts: number; cronJobs: number }
  recentJobs: ExecJob[]
  recentSessions: TerminalSession[]
  cronJobList: CronJob[]
}

export interface MyResources {
  scope: { all: boolean; deptIds: number[]; includeSelf: boolean }
  hostStats: { total: number; online: number; offline: number; unknown: number; prod: number }
  byEnv: NameCountRow[]
  byDept: NameCountRow[]
  hosts: Host[]
  myCronJobs: number
}

export interface Tag {
  id: number
  name: string
  category: string
  color: string
  remark: string
  hostCount: number
  dbCount: number
}

export interface DBInstance {
  id: number
  name: string
  type: 'mysql' | 'postgres' | 'redis' | 'mongo' | 'other'
  address: string
  port: number
  username: string
  dbName: string
  version: string
  env: 'dev' | 'test' | 'prod'
  deptId: number
  createdBy: number
  status: 'online' | 'offline' | 'unknown'
  checkedAt: string | null
  tags: string
  remark: string
  createdAt: string
}

export interface FixedAsset {
  id: number
  name: string
  category: 'server' | 'network' | 'storage' | 'terminal' | 'other'
  sn: string
  model: string
  vendor: string
  location: string
  owner: string
  hostId: number
  deptId: number
  createdBy: number
  status: 'in_use' | 'idle' | 'repair' | 'scrapped'
  purchaseDate: string | null
  purchasePrice: number
  warrantyEnd: string | null
  remark: string
  createdAt: string
}

export interface FixedAssetStats {
  total: number
  inUse: number
  idle: number
  repair: number
  scrapped: number
  expiring30: number
  expired: number
  totalPrice: number
}

export interface CloudAccount {
  id: number
  name: string
  provider: 'aliyun' | 'tencent' | 'huawei' | 'aws' | 'other'
  accessKeyId: string
  region: string
  accountId: string
  deptId: number
  enabled: boolean
  remark: string
  createdAt: string
}

export interface InventoryItem {
  id: number
  batchId: number
  assetId: number
  assetName: string
  sn: string
  expectLocation: string
  actualLocation: string
  result: 'pending' | 'matched' | 'missing' | 'moved'
  note: string
  checkedBy: string
  checkedAt: string | null
}

export interface InventoryBatch {
  id: number
  name: string
  scopeDeptId: number
  status: 'ongoing' | 'finished'
  operator: string
  totalCount: number
  checkedCount: number
  matchedCount: number
  missingCount: number
  movedCount: number
  remark: string
  startedAt: string
  finishedAt: string | null
  items?: InventoryItem[]
}

export interface PurchaseItem {
  id?: number
  orderId?: number
  name: string
  category: string
  model: string
  vendor: string
  quantity: number
  unitPrice: number
  remark: string
}

export interface PurchaseOrder {
  id: number
  orderNo: string
  title: string
  vendor: string
  applicant: string
  status: 'draft' | 'ordered' | 'received' | 'cancelled'
  amount: number
  deptId: number
  orderDate: string | null
  expectedDate: string | null
  receivedDate: string | null
  assetCreated: boolean
  remark: string
  items?: PurchaseItem[]
}





export interface AuditLog {
  id: number
  username: string
  method: string
  path: string
  action: string
  status: number
  ip: string
  costMs: number
  createdAt: string
}

/** 审计检索结果：除分页数据外带上命中条件里的失败条数 */
export interface AuditPage extends PageData<AuditLog> {
  failed: number
}


export interface MenuTreeNode {
  id: number
  parentId: number
  name: string
  title: string
  path: string
  component: string
  icon: string
  type: 'menu' | 'button'
  authCode: string
  sort: number
  hidden: boolean
  builtin: boolean
  children?: MenuTreeNode[]
}

export interface SiteLink {
  id: number
  name: string
  url: string
  category: string
  icon: string
  description: string
  sort: number
  enabled: boolean
}

export interface EmailTemplate {
  id: number
  code: string
  name: string
  subject: string
  body: string
  variables: string
  enabled: boolean
  remark: string
  builtin: boolean
}

export interface ResourceGrant {
  id: number
  subjectType: 'user' | 'role'
  subjectId: number
  subjectName: string
  resourceType: 'host' | 'database'
  resourceId: number
  resourceName: string
  actions: string
  expiresAt: string | null
  remark: string
  operator: string
  createdAt: string
}

export interface GrantDiagnosis {
  user: { id: number; username: string }
  scopedHosts: number
  grantedHosts: { hostId: number; hostName: string; address: string; actions: string[] }[]
}



// ---------- 接口 ----------

export const login = (username: string, password: string, code?: string) =>
  request<LoginResult>({ url: '/auth/login', method: 'POST', data: { username, password, code } })

export const getProfile = () => request<Profile>({ url: '/me' })
export const getMyMenus = () => request<MenuNode[]>({ url: '/me/menus' })
export const changePassword = (oldPassword: string, newPassword: string) =>
  request({ url: '/me/password', method: 'PUT', data: { oldPassword, newPassword } })

export const getDashboardStats = () => request<any>({ url: '/dashboard/stats' })

export const listHosts = (params: Record<string, any>) =>
  request<PageData<Host>>({ url: '/hosts', params })
export const createHost = (data: Record<string, any>) =>
  request<Host>({ url: '/hosts', method: 'POST', data })
export const updateHost = (id: number, data: Record<string, any>) =>
  request<Host>({ url: `/hosts/${id}`, method: 'PUT', data })
export const deleteHost = (id: number) => request({ url: `/hosts/${id}`, method: 'DELETE' })
export const checkHost = (id: number) =>
  request<{ status: string; osInfo: string; detail: string; costMs: number; viaProxy: string }>({
    url: `/hosts/${id}/check`,
    method: 'POST'
  })

export const listSessions = (params: Record<string, any>) =>
  request<PageData<TerminalSession>>({ url: '/sessions', params })
export const getSession = (id: number) =>
  request<{ session: TerminalSession; replayable: boolean }>({ url: `/sessions/${id}` })
/** 单个会话的命令明细，分页取；长会话有上千条，不一次全拉 */
export const listSessionCommands = (id: number, params: Record<string, any>) =>
  request<PageData<SessionCommand>>({ url: `/sessions/${id}/commands`, params })

/** 跨会话命令检索的一行：命令 + 它属于哪次会话、谁在哪台机器上敲的 */
export interface SessionCommandHit extends SessionCommand {
  username: string
  loginUser: string
  hostName: string
  address: string
  clientIp: string
}

export const searchSessionCommands = (params: Record<string, any>) =>
  request<{
    list: SessionCommandHit[]
    total: number
    blocked: number
    page: number
    pageSize: number
  }>({ url: '/sessions/commands', params })

export const listCommandRules = () => request<CommandRule[]>({ url: '/system/command-rules' })
export const createCommandRule = (data: Record<string, any>) =>
  request<CommandRule>({ url: '/system/command-rules', method: 'POST', data })
export const updateCommandRule = (id: number, data: Record<string, any>) =>
  request<CommandRule>({ url: `/system/command-rules/${id}`, method: 'PUT', data })
export const deleteCommandRule = (id: number) =>
  request({ url: `/system/command-rules/${id}`, method: 'DELETE' })
export const testCommandRule = (command: string) =>
  request<{ matched: boolean; ruleId?: number; action?: string; description?: string }>({
    url: '/system/command-rules/test',
    method: 'POST',
    data: { command }
  })


export const runExecJob = (data: Record<string, any>) =>
  request<ExecJob>({ url: '/exec/jobs', method: 'POST', data })
export const listExecJobs = (params: Record<string, any>) =>
  request<PageData<ExecJob>>({ url: '/exec/jobs', params })
export const getExecJob = (id: number) => request<ExecJob>({ url: `/exec/jobs/${id}` })

/* ---------- 按条件选主机 / 重跑失败 / 取消下发 ---------- */

export interface ExecResolvedHost {
  id: number
  name: string
  address: string
  env: string
  status: string
  tags: string
  /** false 表示可见但未授权执行，不会进入下发清单 */
  canExec: boolean
}

export interface ExecResolveResult {
  hostIds: number[]
  hosts: ExecResolvedHost[]
  /** 条件匹配到的台数 */
  matched: number
  /** 其中真正可执行的台数 */
  usable: number
  /** 其中生产主机台数 */
  prod: number
  notes: string[]
}

export const resolveExecHosts = (data: {
  keyword?: string
  env?: string
  status?: string
  deptId?: number
  tags?: string[]
}) => request<ExecResolveResult>({ url: '/exec/resolve-hosts', method: 'POST', data })

export const rerunFailedExecJob = (id: number) =>
  request<{ job: ExecJob; reran: number; skipped: number; note: string }>({
    url: `/exec/jobs/${id}/rerun-failed`,
    method: 'POST'
  })

export const cancelExecJob = (id: number) =>
  request<{ canceled: boolean; note: string }>({
    url: `/exec/jobs/${id}/cancel`,
    method: 'POST'
  })


// ---------- 下发闸门（命令规则 + 生产确认） ----------

export interface ExecPrecheckHit {
  ruleId: number
  pattern: string
  action: 'block' | 'warn'
  description: string
  line: number
  snippet: string
}

export interface ExecPrecheckResult {
  status: 'pass' | 'warn' | 'blocked' | 'unknown'
  blocked: boolean
  /** true 表示被拦截级命令规则拦下，false 且 blocked 表示只是缺生产确认 */
  ruleBlocked: boolean
  needConfirm: boolean
  reason: string
  hits: ExecPrecheckHit[]
  prodHosts: string[]
  hostCount: number
}

export interface ExecGuardLog {
  id: number
  source: string
  status: 'blocked' | 'warn'
  reason: string
  command: string
  hostCount: number
  prodCount: number
  hostNames: string
  ruleId: number
  pattern: string
  action: string
  cronJobId: number
  userId: number
  username: string
  clientIp: string
  createdAt: string
}

export const precheckExec = (data: { command: string; hostIds?: number[] }) =>
  request<ExecPrecheckResult>({ url: '/exec/precheck', method: 'POST', data })
export const listExecGuardLogs = (params?: Record<string, any>) =>
  request<{
    list: ExecGuardLog[]
    total: number
    page: number
    pageSize: number
    summary: Record<string, number>
  }>({ url: '/exec/guard-logs', params })


export const listUsers = (params: Record<string, any>) =>
  request<PageData<User>>({ url: '/system/users', params })
export const createUser = (data: Record<string, any>) =>
  request<User>({ url: '/system/users', method: 'POST', data })
export const updateUser = (id: number, data: Record<string, any>) =>
  request<User>({ url: `/system/users/${id}`, method: 'PUT', data })
export const deleteUser = (id: number) => request({ url: `/system/users/${id}`, method: 'DELETE' })

export const listRoles = () => request<Role[]>({ url: '/system/roles' })
export const createRole = (data: Record<string, any>) =>
  request<Role>({ url: '/system/roles', method: 'POST', data })
export const updateRole = (id: number, data: Record<string, any>) =>
  request<Role>({ url: `/system/roles/${id}`, method: 'PUT', data })
export const deleteRole = (id: number) => request({ url: `/system/roles/${id}`, method: 'DELETE' })

export const getMenuTree = () => request<MenuTreeNode[]>({ url: '/system/menus/tree' })
export const createMenu = (data: Record<string, any>) =>
  request({ url: '/system/menus', method: 'POST', data })
export const updateMenu = (id: number, data: Record<string, any>) =>
  request({ url: `/system/menus/${id}`, method: 'PUT', data })
export const deleteMenu = (id: number) =>
  request({ url: `/system/menus/${id}`, method: 'DELETE' })

export const listSiteLinks = (enabledOnly = false) =>
  request<SiteLink[]>({ url: '/site-links', params: enabledOnly ? { enabledOnly: 'true' } : {} })
export const createSiteLink = (data: Record<string, any>) =>
  request<SiteLink>({ url: '/site-links', method: 'POST', data })
export const updateSiteLink = (id: number, data: Record<string, any>) =>
  request<SiteLink>({ url: `/site-links/${id}`, method: 'PUT', data })
export const deleteSiteLink = (id: number) =>
  request({ url: `/site-links/${id}`, method: 'DELETE' })

export const listEmailTemplates = () => request<EmailTemplate[]>({ url: '/system/email-templates' })
export const createEmailTemplate = (data: Record<string, any>) =>
  request<EmailTemplate>({ url: '/system/email-templates', method: 'POST', data })
export const updateEmailTemplate = (id: number, data: Record<string, any>) =>
  request<EmailTemplate>({ url: `/system/email-templates/${id}`, method: 'PUT', data })
export const deleteEmailTemplate = (id: number) =>
  request({ url: `/system/email-templates/${id}`, method: 'DELETE' })
export const previewEmailTemplate = (id: number, vars?: Record<string, string>) =>
  request<{ subject: string; body: string; vars: Record<string, string> }>({
    url: `/system/email-templates/${id}/preview`,
    method: 'POST',
    data: { vars: vars || {} }
  })

export const listAuditLogs = (params: Record<string, any>) =>
  request<AuditPage>({ url: '/system/audit-logs', params })
export const listAuditOperators = () => request<string[]>({ url: '/system/audit-logs/operators' })

// ---------- 文件管理 ----------

export const listFiles = (hostId: number, path: string) =>
  request<{ path: string; parent: string; entries: FileEntry[] }>({
    url: `/hosts/${hostId}/files`,
    params: { path }
  })

export const uploadFile = (hostId: number, dir: string, file: File) => {
  const form = new FormData()
  form.append('path', dir)
  form.append('file', file)
  return request<{ path: string; size: number }>({
    url: `/hosts/${hostId}/files/upload`,
    method: 'POST',
    data: form,
    headers: { 'Content-Type': 'multipart/form-data' }
  })
}

export const makeDir = (hostId: number, path: string) =>
  request({ url: `/hosts/${hostId}/files/mkdir`, method: 'POST', data: { path } })

export const renameFile = (hostId: number, from: string, to: string) =>
  request({ url: `/hosts/${hostId}/files/rename`, method: 'POST', data: { from, to } })

export const deleteFile = (hostId: number, path: string) =>
  request({ url: `/hosts/${hostId}/files`, method: 'DELETE', params: { path } })

/** 下载走原生 fetch 拿二进制流，再用 Blob 触发保存 */
export async function downloadFile(hostId: number, path: string) {
  const token = localStorage.getItem(TOKEN_KEY) || ''
  const url = `/api/v1/hosts/${hostId}/files/download?path=${encodeURIComponent(path)}`
  const res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } })
  if (!res.ok) {
    throw new Error(`下载失败（HTTP ${res.status}）`)
  }

  const blob = await res.blob()
  const link = document.createElement('a')
  link.href = URL.createObjectURL(blob)
  link.download = path.split('/').pop() || 'download'
  link.click()
  URL.revokeObjectURL(link.href)
}

export const listFileAudits = (params: Record<string, any>) =>
  request<PageData<FileAudit>>({ url: '/file-audits', params })

// ---------- 定时任务 ----------

export const listCronJobs = (params: Record<string, any>) =>
  request<PageData<CronJob>>({ url: '/scheduler/jobs', params })
export const createCronJob = (data: Record<string, any>) =>
  request<CronJob>({ url: '/scheduler/jobs', method: 'POST', data })
export const updateCronJob = (id: number, data: Record<string, any>) =>
  request<CronJob>({ url: `/scheduler/jobs/${id}`, method: 'PUT', data })
export const deleteCronJob = (id: number) =>
  request({ url: `/scheduler/jobs/${id}`, method: 'DELETE' })
export const runCronJobNow = (id: number, confirmProd = false) =>
  request<ExecJob>({ url: `/scheduler/jobs/${id}/run`, method: 'POST', data: { confirmProd } })

// ---------- 告警 ----------

export const listAlerts = (params: Record<string, any>) =>
  request<PageData<Alert>>({ url: '/alerts', params })
export const getAlertStats = () => request<AlertStats>({ url: '/alerts/stats' })
export const getAlert = (id: number) =>
  request<{ alert: Alert; records: NotifyRecord[] }>({ url: `/alerts/${id}` })
export const ackAlert = (id: number, note: string) =>
  request({ url: `/alerts/${id}/ack`, method: 'POST', data: { note } })
export const resolveAlert = (id: number, note: string) =>
  request({ url: `/alerts/${id}/resolve`, method: 'POST', data: { note } })

// ---------- 告警接入源 ----------

export const listAlertSources = () => request<AlertSource[]>({ url: '/alert-sources' })
export const createAlertSource = (data: Record<string, any>) =>
  request<AlertSource>({ url: '/alert-sources', method: 'POST', data })
export const updateAlertSource = (id: number, data: Record<string, any>) =>
  request<AlertSource>({ url: `/alert-sources/${id}`, method: 'PUT', data })
export const rotateAlertSourceToken = (id: number) =>
  request<AlertSource>({ url: `/alert-sources/${id}/rotate`, method: 'POST' })
export const deleteAlertSource = (id: number) =>
  request({ url: `/alert-sources/${id}`, method: 'DELETE' })

// ---------- 通知渠道与路由 ----------

export const listNotifyChannels = () => request<NotifyChannel[]>({ url: '/notify/channels' })
export const createNotifyChannel = (data: Record<string, any>) =>
  request<NotifyChannel>({ url: '/notify/channels', method: 'POST', data })
export const updateNotifyChannel = (id: number, data: Record<string, any>) =>
  request<NotifyChannel>({ url: `/notify/channels/${id}`, method: 'PUT', data })
export const deleteNotifyChannel = (id: number) =>
  request({ url: `/notify/channels/${id}`, method: 'DELETE' })
export const testNotifyChannel = (id: number) =>
  request<{ ok: boolean; httpStatus?: number; detail?: string; costMs?: number }>({
    url: `/notify/channels/${id}/test`,
    method: 'POST'
  })

export const listNotifyRoutes = () => request<NotifyRoute[]>({ url: '/notify/routes' })
export const createNotifyRoute = (data: Record<string, any>) =>
  request<NotifyRoute>({ url: '/notify/routes', method: 'POST', data })
export const updateNotifyRoute = (id: number, data: Record<string, any>) =>
  request<NotifyRoute>({ url: `/notify/routes/${id}`, method: 'PUT', data })
export const deleteNotifyRoute = (id: number) =>
  request({ url: `/notify/routes/${id}`, method: 'DELETE' })
export const testNotifyRoute = (data: { severity: string; labels: Record<string, string> }) =>
  request<{ matched: boolean; routeId?: number; routeName?: string; channelIds?: number[]; fallback?: boolean }>({
    url: '/notify/routes/test',
    method: 'POST',
    data
  })

export const listNotifyRecords = (params: Record<string, any>) =>
  request<PageData<NotifyRecord>>({ url: '/notify/records', params })

export const getAlertSituation = (range: string) =>
  request<AlertSituation>({ url: '/alerts/situation', params: { range } })

// ---------- 公告与站内消息 ----------

export const listAnnouncements = (params: Record<string, any>) =>
  request<PageData<Announcement>>({ url: '/system/announcements', params })
export const createAnnouncement = (data: Record<string, any>) =>
  request<Announcement>({ url: '/system/announcements', method: 'POST', data })
export const updateAnnouncement = (id: number, data: Record<string, any>) =>
  request<Announcement>({ url: `/system/announcements/${id}`, method: 'PUT', data })
export const deleteAnnouncement = (id: number) =>
  request({ url: `/system/announcements/${id}`, method: 'DELETE' })
export const publishAnnouncement = (id: number) =>
  request<{ published: boolean; messageSent: number }>({
    url: `/system/announcements/${id}/publish`,
    method: 'POST'
  })
export const unpublishAnnouncement = (id: number) =>
  request({ url: `/system/announcements/${id}/unpublish`, method: 'POST' })

export const listPublishedAnnouncements = (params: Record<string, any>) =>
  request<PageData<Announcement>>({ url: '/announcements/published', params })

export const listMessages = (params: Record<string, any>) =>
  request<PageData<Message>>({ url: '/me/messages', params })
export const getMessageSummary = () => request<MessageSummary>({ url: '/me/messages/summary' })
export const readMessage = (id: number) =>
  request({ url: `/me/messages/${id}/read`, method: 'POST' })
export const readAllMessages = () =>
  request<{ updated: number }>({ url: '/me/messages/read-all', method: 'POST' })

// ---------- 组织与数据权限 ----------

export const listCompanies = () => request<Company[]>({ url: '/system/companies' })
export const createCompany = (data: Record<string, any>) =>
  request<Company>({ url: '/system/companies', method: 'POST', data })
export const updateCompany = (id: number, data: Record<string, any>) =>
  request<Company>({ url: `/system/companies/${id}`, method: 'PUT', data })
export const deleteCompany = (id: number) =>
  request({ url: `/system/companies/${id}`, method: 'DELETE' })

export const getDepartmentTree = (companyId?: number) =>
  request<DeptNode[]>({ url: '/system/departments/tree', params: { companyId } })
export const createDepartment = (data: Record<string, any>) =>
  request({ url: '/system/departments', method: 'POST', data })
export const updateDepartment = (id: number, data: Record<string, any>) =>
  request({ url: `/system/departments/${id}`, method: 'PUT', data })
export const deleteDepartment = (id: number) =>
  request({ url: `/system/departments/${id}`, method: 'DELETE' })

export const diagnoseDataScope = (userId: number) =>
  request<DataScopeDiagnosis>({ url: `/system/data-permission/diagnose/${userId}` })

// ---------- 资源授权 ----------

export const listResourceGrants = (params: Record<string, any>) =>
  request<PageData<ResourceGrant>>({ url: '/resource-grants', params })
export const createResourceGrant = (data: Record<string, any>) =>
  request<ResourceGrant>({ url: '/resource-grants', method: 'POST', data })
export const updateResourceGrant = (id: number, data: Record<string, any>) =>
  request<ResourceGrant>({ url: `/resource-grants/${id}`, method: 'PUT', data })
export const deleteResourceGrant = (id: number) =>
  request({ url: `/resource-grants/${id}`, method: 'DELETE' })
export const diagnoseResourceGrants = (userId: number) =>
  request<GrantDiagnosis>({ url: `/resource-grants/diagnose/${userId}` })


// ---------- 平台配置 ----------

export const getBranding = () => request<Branding>({ url: '/public/branding' })

export const listConfigs = (group?: string) =>
  request<SysConfig[]>({ url: '/system/configs', params: { group } })
export const createConfig = (data: Record<string, any>) =>
  request<SysConfig>({ url: '/system/configs', method: 'POST', data })
export const updateConfigs = (items: { key: string; value: string }[]) =>
  request<{ updated: number }>({ url: '/system/configs', method: 'PUT', data: { items } })
export const deleteConfig = (id: number) =>
  request({ url: `/system/configs/${id}`, method: 'DELETE' })

// ---------- 个人工作台 ----------

export const getPersonalWorkbench = () => request<PersonalWorkbench>({ url: '/me/workbench' })
export const getMyResources = () => request<MyResources>({ url: '/me/resources' })
export const listMyActivity = (params: Record<string, any>) =>
  request<PageData<AuditLog>>({ url: '/me/activity', params })
export const listMySessions = (params: Record<string, any>) =>
  request<PageData<TerminalSession>>({ url: '/me/sessions', params })
export const listMyExecJobs = (params: Record<string, any>) =>
  request<PageData<ExecJob>>({ url: '/me/exec-jobs', params })

// ---------- 标签字典 ----------

export const listTags = () => request<Tag[]>({ url: '/tags' })
export const createTag = (data: Record<string, any>) =>
  request<Tag>({ url: '/tags', method: 'POST', data })
export const updateTag = (id: number, data: Record<string, any>) =>
  request<Tag>({ url: `/tags/${id}`, method: 'PUT', data })
export const deleteTag = (id: number) => request({ url: `/tags/${id}`, method: 'DELETE' })

// ---------- 数据库资产 ----------

export const listDatabases = (params: Record<string, any>) =>
  request<PageData<DBInstance>>({ url: '/databases', params })
export const createDatabase = (data: Record<string, any>) =>
  request<DBInstance>({ url: '/databases', method: 'POST', data })
export const updateDatabase = (id: number, data: Record<string, any>) =>
  request<DBInstance>({ url: `/databases/${id}`, method: 'PUT', data })
export const deleteDatabase = (id: number) =>
  request({ url: `/databases/${id}`, method: 'DELETE' })
export const checkDatabase = (id: number) =>
  request<{ status: string; costMs: number; detail: string; note: string }>({
    url: `/databases/${id}/check`,
    method: 'POST'
  })

// ---------- 固定资产 ----------

export const listFixedAssets = (params: Record<string, any>) =>
  request<PageData<FixedAsset>>({ url: '/fixed-assets', params })
export const getFixedAssetStats = () => request<FixedAssetStats>({ url: '/fixed-assets/stats' })
export const createFixedAsset = (data: Record<string, any>) =>
  request<FixedAsset>({ url: '/fixed-assets', method: 'POST', data })
export const updateFixedAsset = (id: number, data: Record<string, any>) =>
  request<FixedAsset>({ url: `/fixed-assets/${id}`, method: 'PUT', data })
export const deleteFixedAsset = (id: number) =>
  request({ url: `/fixed-assets/${id}`, method: 'DELETE' })

// ---------- 云账号 ----------

export const listCloudAccounts = (params?: Record<string, any>) =>
  request<CloudAccount[]>({ url: '/cloud-accounts', params })
export const createCloudAccount = (data: Record<string, any>) =>
  request<CloudAccount>({ url: '/cloud-accounts', method: 'POST', data })
export const updateCloudAccount = (id: number, data: Record<string, any>) =>
  request<CloudAccount>({ url: `/cloud-accounts/${id}`, method: 'PUT', data })
export const deleteCloudAccount = (id: number) =>
  request({ url: `/cloud-accounts/${id}`, method: 'DELETE' })

// ---------- 资产盘点 ----------

export const listInventoryBatches = (params: Record<string, any>) =>
  request<PageData<InventoryBatch>>({ url: '/inventory/batches', params })
export const getInventoryBatch = (id: number) =>
  request<InventoryBatch>({ url: `/inventory/batches/${id}` })
export const createInventoryBatch = (data: Record<string, any>) =>
  request<InventoryBatch>({ url: '/inventory/batches', method: 'POST', data })
export const finishInventoryBatch = (id: number) =>
  request<InventoryBatch>({ url: `/inventory/batches/${id}/finish`, method: 'POST' })
export const deleteInventoryBatch = (id: number) =>
  request({ url: `/inventory/batches/${id}`, method: 'DELETE' })
export const updateInventoryItem = (id: number, data: Record<string, any>) =>
  request({ url: `/inventory/items/${id}`, method: 'PUT', data })

// ---------- 采购记录 ----------

export const listPurchaseOrders = (params: Record<string, any>) =>
  request<PageData<PurchaseOrder>>({ url: '/purchase/orders', params })
export const getPurchaseOrder = (id: number) =>
  request<PurchaseOrder>({ url: `/purchase/orders/${id}` })
export const createPurchaseOrder = (data: Record<string, any>) =>
  request<PurchaseOrder>({ url: '/purchase/orders', method: 'POST', data })
export const updatePurchaseOrder = (id: number, data: Record<string, any>) =>
  request<PurchaseOrder>({ url: `/purchase/orders/${id}`, method: 'PUT', data })
export const deletePurchaseOrder = (id: number) =>
  request({ url: `/purchase/orders/${id}`, method: 'DELETE' })
export const receivePurchaseOrder = (id: number, data: Record<string, any>) =>
  request<{ received: boolean; createdAssets: number }>({
    url: `/purchase/orders/${id}/receive`,
    method: 'POST',
    data
  })

// ---------- 构建发布（Jenkins） ----------

export interface BuildServer {
  id: number
  name: string
  url: string
  username: string
  deptId: number
  createdBy: number
  enabled: boolean
  remark: string
  createdAt: string
}

export interface BuildJob {
  id: number
  name: string
  serverId: number
  serverName: string
  jobPath: string
  params: string
  deptId: number
  createdBy: number
  enabled: boolean
  lastBuildNo: number
  lastStatus: string
  lastRunAt: string | null
  remark: string
  createdAt: string
}

export interface BuildRecord {
  id: number
  jobId: number
  jobName: string
  buildNo: number
  status: string
  params: string
  queueUrl: string
  buildUrl: string
  triggeredBy: string
  durationMs: number
  errorMsg: string
  startedAt: string
  syncedAt: string | null
}

export const listBuildServers = () => request<BuildServer[]>({ url: '/build/servers' })
export const createBuildServer = (data: Record<string, any>) =>
  request<BuildServer>({ url: '/build/servers', method: 'POST', data })
export const updateBuildServer = (id: number, data: Record<string, any>) =>
  request<BuildServer>({ url: `/build/servers/${id}`, method: 'PUT', data })
export const deleteBuildServer = (id: number) =>
  request({ url: `/build/servers/${id}`, method: 'DELETE' })
export const testBuildServer = (id: number) =>
  request<{ ok: boolean; costMs: number; version?: string; nodeName?: string; mode?: string; detail?: string }>({
    url: `/build/servers/${id}/test`,
    method: 'POST'
  })

export const listBuildJobs = (params: Record<string, any>) =>
  request<PageData<BuildJob>>({ url: '/build/jobs', params })
export const createBuildJob = (data: Record<string, any>) =>
  request<BuildJob>({ url: '/build/jobs', method: 'POST', data })
export const updateBuildJob = (id: number, data: Record<string, any>) =>
  request<BuildJob>({ url: `/build/jobs/${id}`, method: 'PUT', data })
export const deleteBuildJob = (id: number) =>
  request({ url: `/build/jobs/${id}`, method: 'DELETE' })
export const triggerBuildJob = (id: number, data: { params?: Record<string, string> }) =>
  request<{ recordId: number; queueUrl: string; detail: string }>({
    url: `/build/jobs/${id}/trigger`,
    method: 'POST',
    data
  })

export const listBuildRecords = (params: Record<string, any>) =>
  request<PageData<BuildRecord>>({ url: '/build/records', params })
export const syncBuildRecord = (id: number) =>
  request<{ status: string; buildNo?: number; durationMs?: number; detail?: string }>({
    url: `/build/records/${id}/sync`,
    method: 'POST'
  })

// ---------- 证书管理 ----------

export interface Certificate {
  id: number
  name: string
  domain: string
  port: number
  serverName: string
  issuer: string
  subject: string
  dnsNames: string
  serialNumber: string
  fingerprint: string
  notBefore: string | null
  notAfter: string | null
  daysLeft: number
  status: string
  trusted: boolean
  verifyError: string
  lastCheckAt: string | null
  errorMsg: string
  alertDays: number
  alertEnabled: boolean
  deptId: number
  createdBy: number
  enabled: boolean
  remark: string
  createdAt: string
}

export interface CertificateStats {
  total: number
  valid: number
  expiring: number
  expired: number
  error: number
  unchecked: number
  untrusted: number
}

export const listCertificates = (params: Record<string, any>) =>
  request<PageData<Certificate>>({ url: '/certificates', params })
export const getCertificateStats = () => request<CertificateStats>({ url: '/certificates/stats' })
export const createCertificate = (data: Record<string, any>) =>
  request<Certificate>({ url: '/certificates', method: 'POST', data })
export const updateCertificate = (id: number, data: Record<string, any>) =>
  request<Certificate>({ url: `/certificates/${id}`, method: 'PUT', data })
export const deleteCertificate = (id: number) =>
  request({ url: `/certificates/${id}`, method: 'DELETE' })
export const checkCertificate = (id: number) =>
  request<Certificate>({ url: `/certificates/${id}/check`, method: 'POST' })
export const checkAllCertificates = () =>
  request<{ checked: number; valid?: number; expiring?: number; expired?: number; error?: number; detail?: string }>({
    url: '/certificates/check-all',
    method: 'POST'
  })

// ---------- 双因子口令（TOTP） ----------

export interface TOTPStatus {
  enabled: boolean
  boundAt: string | null
  pending: boolean
  mode: 'optional' | 'required'
  issuer: string
}

export interface TOTPSetup {
  secret: string
  uri: string
  digits: number
  period: number
}

export const getMyTOTP = () => request<TOTPStatus>({ url: '/me/totp' })
export const setupMyTOTP = () => request<TOTPSetup>({ url: '/me/totp/setup', method: 'POST' })
export const confirmMyTOTP = (code: string) =>
  request<{ enabled: boolean; boundAt: string }>({ url: '/me/totp/confirm', method: 'POST', data: { code } })
export const disableMyTOTP = (password: string, code: string) =>
  request<{ enabled: boolean }>({ url: '/me/totp/disable', method: 'POST', data: { password, code } })
export const resetUserTOTP = (id: number) =>
  request<{ username: string; enabled: boolean }>({
    url: `/system/users/${id}/totp/reset`,
    method: 'POST'
  })

// ---------- 告警规则 ----------

export interface AlertRuleMetric {
  key: string
  label: string
  unit: string
  windowed: boolean
  hint: string
  currentValue: number
  detail: string
}

export interface AlertRule {
  id: number
  name: string
  metric: string
  comparator: 'gt' | 'gte' | 'lt' | 'lte'
  threshold: number
  windowMinutes: number
  consecutiveTimes: number
  severity: string
  hitStreak: number
  lastValue: number
  lastStatus: string
  lastDetail: string
  lastEvalAt: string | null
  lastFireAt: string | null
  enabled: boolean
  remark: string
  createdAt: string
}

export interface AlertRuleEvalResult {
  status: string
  value: number
  threshold: number
  expression: string
  hit: boolean
  hitStreak: number
  needStreak: number
  detail: string
}

export const listAlertRuleMetrics = (windowMinutes?: number) =>
  request<AlertRuleMetric[]>({ url: '/monitor/alert-rules/metrics', params: { windowMinutes } })
export const listAlertRules = (params?: Record<string, any>) =>
  request<AlertRule[]>({ url: '/monitor/alert-rules', params })
export const createAlertRule = (data: Record<string, any>) =>
  request<AlertRule>({ url: '/monitor/alert-rules', method: 'POST', data })
export const updateAlertRule = (id: number, data: Record<string, any>) =>
  request<AlertRule>({ url: `/monitor/alert-rules/${id}`, method: 'PUT', data })
export const deleteAlertRule = (id: number) =>
  request({ url: `/monitor/alert-rules/${id}`, method: 'DELETE' })
export const evaluateAlertRule = (id: number) =>
  request<AlertRuleEvalResult>({ url: `/monitor/alert-rules/${id}/evaluate`, method: 'POST' })

// ---------- 平台健康 ----------

export interface HealthItem {
  key: string
  label: string
  status: 'ok' | 'warn' | 'error'
  value: string
  detail: string
}

export interface HealthReport {
  overall: 'ok' | 'warn' | 'error'
  warnCount: number
  errorCount: number
  checkedAt: string
  groups: { name: string; items: HealthItem[] }[]
}

export const getPlatformHealth = () => request<HealthReport>({ url: '/monitor/health' })

// ---------- 拨测探测 ----------

export interface Probe {
  id: number
  name: string
  type: 'http' | 'tcp'
  target: string
  method: string
  expectStatus: number
  expectKeyword: string
  timeoutSec: number
  alertEnabled: boolean
  consecutiveFails: number
  /** 走哪个出口代理（仅 http）。0 = 直连 */
  proxyId: number
  failStreak: number
  lastStatus: string
  lastCode: number
  lastCostMs: number
  lastError: string
  lastCheckAt: string | null
  totalChecks: number
  failChecks: number
  enabled: boolean
  remark: string
  createdAt: string
}

export interface ProbeRecord {
  id: number
  probeId: number
  status: string
  code: number
  costMs: number
  errorMsg: string
  operator: string
  createdAt: string
}

export const listProbes = (params?: Record<string, any>) =>
  request<Probe[]>({ url: '/monitor/probes', params })
export const createProbe = (data: Record<string, any>) =>
  request<Probe>({ url: '/monitor/probes', method: 'POST', data })
export const updateProbe = (id: number, data: Record<string, any>) =>
  request<Probe>({ url: `/monitor/probes/${id}`, method: 'PUT', data })
export const deleteProbe = (id: number) =>
  request({ url: `/monitor/probes/${id}`, method: 'DELETE' })
export const runProbe = (id: number) =>
  request<{ status: string; code: number; costMs: number; errorMsg: string; failStreak: number; needStreak: number }>({
    url: `/monitor/probes/${id}/run`,
    method: 'POST'
  })
export const listProbeRecords = (params: Record<string, any>) =>
  request<PageData<ProbeRecord>>({ url: '/monitor/probe-records', params })

// ---------- 指标查询 ----------

export interface MetricSource {
  id: number
  name: string
  type: string
  baseUrl: string
  headerKey: string
  timeoutSec: number
  isDefault: boolean
  status: 'unknown' | 'healthy' | 'error'
  version: string
  seriesCount: number
  lastError: string
  lastCheckAt: string | null
  enabled: boolean
  remark: string
}

export interface MetricPoint {
  at: number
  value: string
}

export interface MetricSeries {
  name: string
  labels: Record<string, string>
  points: MetricPoint[]
  value: string
}

export interface MetricQueryResult {
  resultType: string
  series: MetricSeries[]
  total: number
  truncated: boolean
  warnings: string[] | null
  costMs: number
  queriedAt?: string
  start?: string
  end?: string
  step?: number
}

export interface SavedMetricQuery {
  id: number
  name: string
  sourceId: number
  expr: string
  rangeMode: boolean
  remark: string
}

export const listMetricSources = () => request<MetricSource[]>({ url: '/monitor/metric-sources' })
export const createMetricSource = (data: Record<string, any>) =>
  request<{ source: MetricSource; check: Record<string, any> }>({
    url: '/monitor/metric-sources',
    method: 'POST',
    data
  })
export const updateMetricSource = (id: number, data: Record<string, any>) =>
  request<MetricSource>({ url: `/monitor/metric-sources/${id}`, method: 'PUT', data })
export const deleteMetricSource = (id: number) =>
  request({ url: `/monitor/metric-sources/${id}`, method: 'DELETE' })
export const checkMetricSource = (id: number) =>
  request<Record<string, any>>({ url: `/monitor/metric-sources/${id}/check`, method: 'POST' })

export const queryMetricInstant = (params: Record<string, any>) =>
  request<MetricQueryResult>({ url: '/monitor/metrics/query', params })
export const queryMetricRange = (params: Record<string, any>) =>
  request<MetricQueryResult>({ url: '/monitor/metrics/query-range', params })
export const listMetricNames = (sourceId: number, keyword?: string) =>
  request<{ names: string[]; total: number; truncated: boolean }>({
    url: '/monitor/metrics/names',
    params: { sourceId, ...(keyword ? { keyword } : {}) }
  })
export const listSavedMetricQueries = () =>
  request<SavedMetricQuery[]>({ url: '/monitor/metrics/saved' })
export const createSavedMetricQuery = (data: Record<string, any>) =>
  request<SavedMetricQuery>({ url: '/monitor/metrics/saved', method: 'POST', data })
export const deleteSavedMetricQuery = (id: number) =>
  request({ url: `/monitor/metrics/saved/${id}`, method: 'DELETE' })

// ---------- 链路追踪 ----------

export interface TraceSource {
  id: number
  name: string
  type: string
  baseUrl: string
  headerKey: string
  timeoutSec: number
  isDefault: boolean
  status: 'unknown' | 'healthy' | 'error'
  serviceCount: number
  lastError: string
  lastCheckAt: string | null
  enabled: boolean
  remark: string
}

export interface TraceSummary {
  traceId: string
  rootService: string
  rootOperation: string
  spanCount: number
  serviceCount: number
  durationMs: number
  startAt: number
  errorCount: number
  services: string[]
}

export interface TraceSpan {
  spanId: string
  parentId: string
  service: string
  operation: string
  offsetMs: number
  durationMs: number
  depth: number
  error: boolean
  tags: Record<string, string>
}

export const listTraceSources = () => request<TraceSource[]>({ url: '/monitor/trace-sources' })
export const createTraceSource = (data: Record<string, any>) =>
  request<{ source: TraceSource; check: Record<string, any> }>({
    url: '/monitor/trace-sources',
    method: 'POST',
    data
  })
export const updateTraceSource = (id: number, data: Record<string, any>) =>
  request<TraceSource>({ url: `/monitor/trace-sources/${id}`, method: 'PUT', data })
export const deleteTraceSource = (id: number) =>
  request({ url: `/monitor/trace-sources/${id}`, method: 'DELETE' })
export const checkTraceSource = (id: number) =>
  request<Record<string, any>>({ url: `/monitor/trace-sources/${id}/check`, method: 'POST' })
export const listTraceServices = (sourceId: number, service?: string) =>
  request<{ values: string[]; total: number }>({
    url: '/monitor/traces/services',
    params: { sourceId, ...(service ? { service } : {}) }
  })
export const searchTraces = (params: Record<string, any>) =>
  request<{
    items: TraceSummary[]
    total: number
    limit: number
    start: string
    end: string
    costMs: number
  }>({ url: '/monitor/traces/search', params })
export const getTraceDetail = (traceId: string, sourceId: number) =>
  request<{ summary: TraceSummary; spans: TraceSpan[]; truncated: boolean; costMs: number }>({
    url: `/monitor/traces/detail/${encodeURIComponent(traceId)}`,
    params: { sourceId }
  })

// ---------- 日志查询 ----------

export interface LogSource {
  id: number
  name: string
  type: string
  baseUrl: string
  tenant: string
  headerKey: string
  timeoutSec: number
  isDefault: boolean
  status: 'unknown' | 'healthy' | 'error'
  labelCount: number
  lastError: string
  lastCheckAt: string | null
  enabled: boolean
  remark: string
}

export interface LogRow {
  at: number
  nano: string
  line: string
  labels: Record<string, string>
  stream: string
  truncated: boolean
}

export interface LogQueryResult {
  rows: LogRow[]
  total: number
  truncated: boolean
  streams: number
  limit: number
  direction: string
  start: string
  end: string
  costMs: number
  linesProcessed: number
  bytesProcessed: number
}

export const listLogSources = () => request<LogSource[]>({ url: '/monitor/log-sources' })
export const createLogSource = (data: Record<string, any>) =>
  request<{ source: LogSource; check: Record<string, any> }>({
    url: '/monitor/log-sources',
    method: 'POST',
    data
  })
export const updateLogSource = (id: number, data: Record<string, any>) =>
  request<LogSource>({ url: `/monitor/log-sources/${id}`, method: 'PUT', data })
export const deleteLogSource = (id: number) =>
  request({ url: `/monitor/log-sources/${id}`, method: 'DELETE' })
export const checkLogSource = (id: number) =>
  request<Record<string, any>>({ url: `/monitor/log-sources/${id}/check`, method: 'POST' })
export const queryLogs = (params: Record<string, any>) =>
  request<LogQueryResult>({ url: '/monitor/logs/query', params })
export const listLogLabels = (params: Record<string, any>) =>
  request<{ values: string[]; total: number; truncated: boolean }>({
    url: '/monitor/logs/labels',
    params
  })

// ---------- 暴露面监测 ----------

export interface ExposureTarget {
  id: number
  name: string
  address: string
  ports: string
  baseline: string
  timeoutMs: number
  alertEnabled: boolean
  lastStatus: 'unknown' | 'ok' | 'unexpected' | 'failed'
  lastOpen: string
  lastUnexpected: string
  lastMissing: string
  lastCostMs: number
  lastError: string
  lastScanAt: string | null
  totalScans: number
  enabled: boolean
  remark: string
  createdAt: string
  updatedAt: string
}

export interface ExposureScan {
  id: number
  targetId: number
  status: 'ok' | 'unexpected' | 'failed'
  scanned: number
  openPorts: string
  unexpected: string
  missing: string
  costMs: number
  errorMsg: string
  operator: string
  createdAt: string
}

export interface ExposureScanResult {
  status: string
  scanned: number
  open: string
  unexpected: string
  missing: string
  costMs: number
  error: string
}

export const listExposureTargets = (params?: Record<string, any>) =>
  request<ExposureTarget[]>({ url: '/monitor/exposures', params: params || {} })
export const createExposureTarget = (data: Record<string, any>) =>
  request<{ target: ExposureTarget; result: ExposureScanResult }>({
    url: '/monitor/exposures',
    method: 'POST',
    data
  })
export const updateExposureTarget = (id: number, data: Record<string, any>) =>
  request<ExposureTarget>({ url: `/monitor/exposures/${id}`, method: 'PUT', data })
export const deleteExposureTarget = (id: number) =>
  request<{ detail: string }>({ url: `/monitor/exposures/${id}`, method: 'DELETE' })
export const scanExposureTarget = (id: number) =>
  request<ExposureScanResult>({ url: `/monitor/exposures/${id}/scan`, method: 'POST' })
export const listExposureScans = (params: Record<string, any>) =>
  request<PageData<ExposureScan>>({ url: '/monitor/exposure-scans', params })

// ---------- 聚合策略 ----------

export interface AggregationPolicy {
  id: number
  name: string
  dimensions: string
  matchSeverity: string
  windowMinutes: number
  minCount: number
  suppressNotify: boolean
  priority: number
  enabled: boolean
  remark: string
  createdAt: string
}

export interface AggregationDimension {
  key: string
  label: string
  count?: number
}

export interface AggregationBucket {
  key: string
  dimensions: Record<string, string>
  alertCount: number
  totalCount: number
  suppressed: number
  severities: Record<string, number>
  sampleIds: number[]
  sampleTitle: string
  firstSeenAt: string
  lastSeenAt: string
  grouped: boolean
}

export interface AggregationPreview {
  policy: AggregationPolicy
  buckets: AggregationBucket[]
  matchedAlerts: number
  bucketCount: number
  groupedBuckets: number
  groupedAlerts: number
  detail: string
}

export interface AggregationOverlap {
  policyA: string
  priorityA: number
  policyB: string
  priorityB: number
  alerts: number
  sample: string
  effective: string
}

export const listAggregationPolicies = () =>
  request<AggregationPolicy[]>({ url: '/monitor/aggregation' })
export const listAggregationDimensions = () =>
  request<AggregationDimension[]>({ url: '/monitor/aggregation/dimensions' })
export const createAggregationPolicy = (data: Record<string, any>) =>
  request<AggregationPolicy>({ url: '/monitor/aggregation', method: 'POST', data })
export const updateAggregationPolicy = (id: number, data: Record<string, any>) =>
  request<AggregationPolicy>({ url: `/monitor/aggregation/${id}`, method: 'PUT', data })
export const deleteAggregationPolicy = (id: number) =>
  request({ url: `/monitor/aggregation/${id}`, method: 'DELETE' })
export const previewAggregation = (id: number) =>
  request<AggregationPreview>({ url: `/monitor/aggregation/${id}/preview` })
export const detectAggregationOverlaps = () =>
  request<{ overlaps: AggregationOverlap[]; detail: string }>({ url: '/monitor/aggregation/overlaps' })

// ---------- 告警静默 / 维护窗口 ----------

export interface AlertSilence {
  id: number
  name: string
  kind: string // silence | maintenance
  matchSeverity: string
  matchLabels: string
  matchTitle: string
  matchSource: string
  matchAll: boolean
  startAt: string
  endAt: string
  reason: string
  enabled: boolean
  hitCount: number
  lastHitAt: string | null
  endedAt: string | null
  endedBy: string
  creatorName: string
  createdAt: string
  status: string // active | pending | expired | ended | disabled
  remainSeconds: number
}

export interface AlertSilenceList {
  list: AlertSilence[]
  total: number
  summary: Record<string, number>
}

export interface AlertSilencePreview {
  scanned: number
  matched: number
  samples: {
    id: number
    title: string
    severity: string
    sourceName: string
    labels: string
    lastSeenAt: string
  }[]
  window: { startAt: string; endAt: string }
}

export const listAlertSilences = (params?: Record<string, any>) =>
  request<AlertSilenceList>({ url: '/monitor/silences', params })
export const createAlertSilence = (data: Record<string, any>) =>
  request<AlertSilence>({ url: '/monitor/silences', method: 'POST', data })
export const updateAlertSilence = (id: number, data: Record<string, any>) =>
  request<AlertSilence>({ url: `/monitor/silences/${id}`, method: 'PUT', data })
export const endAlertSilence = (id: number) =>
  request<AlertSilence>({ url: `/monitor/silences/${id}/end`, method: 'POST' })
export const deleteAlertSilence = (id: number) =>
  request({ url: `/monitor/silences/${id}`, method: 'DELETE' })
export const previewAlertSilence = (data: Record<string, any>) =>
  request<AlertSilencePreview>({ url: '/monitor/silences/preview', method: 'POST', data })
export const listAlertSilenceHits = (id: number) =>
  request<{ silence: AlertSilence; list: any[]; total: number }>({
    url: `/monitor/silences/${id}/hits`
  })

// ---------- 数据库只读查询 ----------

export interface DBSchemaItem {
  name: string
  tables: number
}

export interface DBTableItem {
  schema: string
  name: string
  kind: string // table | view
  rows: number
  comment: string
}

export interface DBColumnItem {
  name: string
  dataType: string
  nullable: boolean
  default: string
  isPk: boolean
  comment: string
}

export interface DBIndexItem {
  name: string
  columns: string
  unique: boolean
}

export interface DBQueryResult {
  columns: string[]
  rows: string[][]
  truncated: boolean
  costMs: number
  statement: string
}

export interface DBQueryLog {
  id: number
  instanceId: number
  instanceName: string
  dbType: string
  schema: string
  statement: string
  status: string // success | blocked | failed
  reason: string
  rows: number
  costMs: number
  exported: boolean
  username: string
  clientIp: string
  createdAt: string
}

export const listDBSchemas = (id: number) =>
  request<{ instance: string; dbType: string; list: DBSchemaItem[] }>({
    url: `/databases/${id}/schemas`
  })
export const listDBTables = (id: number, schema: string) =>
  request<{ schema: string; list: DBTableItem[] }>({
    url: `/databases/${id}/tables`,
    params: { schema }
  })
export const describeDBTable = (id: number, schema: string, table: string) =>
  request<{ schema: string; table: string; columns: DBColumnItem[]; indexes: DBIndexItem[] }>({
    url: `/databases/${id}/columns`,
    params: { schema, table }
  })
export const runDBQuery = (id: number, data: Record<string, any>) =>
  request<DBQueryResult>({ url: `/databases/${id}/query`, method: 'POST', data })
export const checkDBQueryStatement = (data: Record<string, any>) =>
  request<{ allowed: boolean; reason?: string; statement?: string }>({
    url: '/databases/query/check',
    method: 'POST',
    data
  })
export const listDBQueryLogs = (params?: Record<string, any>) =>
  request<{ list: DBQueryLog[]; total: number }>({ url: '/databases/query-logs', params })

/** 导出查询结果为 CSV。与其它导出一致：走 fetch 拿 blob，自己触发下载 */
export async function exportDBQueryCSV(id: number, data: Record<string, any>) {
  const token = localStorage.getItem('ops-token') || ''
  const res = await fetch(`/api/v1/databases/${id}/query/export`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify(data)
  })
  if (!res.ok) {
    let msg = `导出失败（HTTP ${res.status}）`
    try {
      const body = await res.json()
      if (body?.msg) msg = body.msg
    } catch {
      // 响应不是 JSON 就用默认文案
    }
    throw new Error(msg)
  }
  const truncated = res.headers.get('X-Export-Truncated') === '1'
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `db-query-${Date.now()}.csv`
  a.click()
  URL.revokeObjectURL(url)
  return { truncated }
}

// ---------- 事件中心 ----------

export interface SLAClock {
  kind: string
  targetMinutes: number
  state: string // na | pending | risk | met | breached
  reason: string
  dueAt: string | null
  doneAt: string | null
  usedSeconds: number
  remainSeconds: number
}

export interface EventSLA {
  respond: SLAClock
  recover: SLAClock
  worst: string
  worstLabel: string
}

export interface OpsEvent {
  id: number
  title: string
  severity: string
  status: string
  statusLabel: string
  summary: string
  alertIds: number[]
  origin: string
  originNote: string
  assignee: string
  assignedBy: string
  assignedAt: string | null
  createdByName: string
  lastActivityAt: string
  resolvedAt: string | null
  resolvedBy: string
  respondedAt: string | null
  createdAt: string
  sla: EventSLA
}

export interface EventLog {
  id: number
  eventId: number
  action: string
  content: string
  operator: string
  createdAt: string
}

export interface EventStats {
  total: number
  open: number
  processing: number
  resolved: number
  mine: number
  unassigned: number
  today: number
  slaBreached: number
  slaRisk: number
}

// ---------- 监控设置（事件 SLA） ----------

export interface SLALevel {
  severity: string
  label: string
  respondKey: string
  respondMinutes: number
  recoverKey: string
  recoverMinutes: number
}

export interface SLASettings {
  levels: SLALevel[]
  remindBefore: number
  repeatHours: number
  stats: { respondBreached: number; recoverBreached: number; risk: number }
  notes: string[]
}

export const getSLASettings = () => request<SLASettings>({ url: '/monitor/sla-settings' })
export const updateSLASettings = (data: {
  respond?: Record<string, number>
  recover?: Record<string, number>
  remindBefore?: number
  repeatHours?: number
}) => request<{ updated: number }>({ url: '/monitor/sla-settings', method: 'PUT', data })
export const runSLAScan = () =>
  request<{ message: string }>({ url: '/monitor/sla-settings/scan', method: 'POST' })

export interface EventContext {
  hosts: { id?: number; name: string; address?: string; env?: string; status: string; checkedAt?: string }[]
  probes: Probe[]
  certificates: Certificate[]
  rules: AlertRule[]
  otherLabels: Record<string, string>
}

export interface EventDetail {
  event: OpsEvent
  alerts: Alert[]
  logs: EventLog[]
  context: EventContext
}

export const listEvents = (params: Record<string, any>) =>
  request<PageData<OpsEvent>>({ url: '/monitor/events', params })
export const getEventStats = () => request<EventStats>({ url: '/monitor/events/stats' })
export const getEvent = (id: number) => request<EventDetail>({ url: `/monitor/events/${id}` })
export const createEvent = (data: Record<string, any>) =>
  request<OpsEvent>({ url: '/monitor/events', method: 'POST', data })
export const createEventFromBucket = (data: Record<string, any>) =>
  request<OpsEvent>({ url: '/monitor/events/from-bucket', method: 'POST', data })
export const assignEvent = (id: number, data: { assignee: string; note?: string }) =>
  request<{ assignee: string }>({ url: `/monitor/events/${id}/assign`, method: 'POST', data })
export const addEventNote = (id: number, content: string) =>
  request({ url: `/monitor/events/${id}/note`, method: 'POST', data: { content } })
export const updateEventStatus = (
  id: number,
  data: { status: string; note?: string; resolveAlerts?: boolean }
) =>
  request<{ status: string; resolvedAlerts: number; reviewCreated?: boolean }>({
    url: `/monitor/events/${id}/status`,
    method: 'POST',
    data
  })
export const deleteEvent = (id: number) =>
  request({ url: `/monitor/events/${id}`, method: 'DELETE' })

// ---------- 事件复盘 ----------

export interface ReviewDurations {
  detectMinutes: number | null
  ackMinutes: number | null
  mitigateMinutes: number | null
  recoverMinutes: number | null
}

export interface EventReview {
  id: number
  eventId: number
  status: string
  statusLabel: string
  owner: string
  happenedAt: string | null
  detectedAt: string | null
  respondedAt: string | null
  mitigatedAt: string | null
  recoveredAt: string | null
  impact: string
  rootCause: string
  trigger: string
  detectGap: string
  mitigation: string
  lesson: string
  archivedAt: string | null
  archivedBy: string
  createdAt: string
  updatedAt: string
  durations: ReviewDurations
  /** 看板列表里附带的事件与改进项计数 */
  eventTitle?: string
  eventSeverity?: string
  eventStatus?: string
  totalItems?: number
  openItems?: number
  overdueItems?: number
}

export interface ActionItem {
  id: number
  reviewId: number
  eventId: number
  title: string
  detail: string
  kind: string
  kindLabel: string
  owner: string
  dueDate: string | null
  status: string
  statusLabel: string
  doneAt: string | null
  doneNote: string
  overdue: boolean
  lastRemindAt: string | null
  createdByName: string
  createdAt: string
  eventTitle?: string
}

/** 里程碑建议值：at 是建议时间，source 说明它是从哪条记录算出来的 */
export interface MilestoneHint {
  at: string | null
  source: string
}

export interface EventReviewDetail {
  event: OpsEvent
  review: EventReview | null
  suggestions: Record<string, MilestoneHint>
  actionItems: ActionItem[]
}

export interface ReviewStats {
  pending: number
  draft: number
  reviewing: number
  archived: number
  openItems: number
  overdueItems: number
  mineItems: number
  avgRecoverMinutes: number | null
  recoverSamples: number
}

export interface EventEvidence {
  window: { from: string; to: string; note: string }
  notifyRecords: NotifyRecord[]
  escalations: {
    id: number
    alertId: number
    level: number
    userName: string
    source: string
    channel: string
    status: string
    detail: string
    createdAt: string
  }[]
  silences: AlertSilence[]
  suppressed: { alertId: number; title: string; suppressedBy: string }[]
  execGuardLogs: ExecGuardLog[]
  hostMetrics: HostMetric[]
  probeRecords: ProbeRecord[]
  sources: string[]
}

export const getEventReview = (eventId: number) =>
  request<EventReviewDetail>({ url: `/monitor/events/${eventId}/review` })
export const saveEventReview = (eventId: number, data: Record<string, any>) =>
  request<EventReview>({ url: `/monitor/events/${eventId}/review`, method: 'POST', data })
export const archiveEventReview = (eventId: number) =>
  request<{ status: string; actionItems: number }>({
    url: `/monitor/events/${eventId}/review/archive`,
    method: 'POST'
  })
export const reopenEventReview = (eventId: number, reason: string) =>
  request({ url: `/monitor/events/${eventId}/review/reopen`, method: 'POST', data: { reason } })
export const exportEventReview = (eventId: number) =>
  request<{ filename: string; markdown: string }>({ url: `/monitor/events/${eventId}/review/export` })
export const getEventEvidence = (eventId: number) =>
  request<EventEvidence>({ url: `/monitor/events/${eventId}/evidence` })

export const listReviews = (params: Record<string, any>) =>
  request<PageData<EventReview>>({ url: '/monitor/reviews', params })
export const getReviewStats = () => request<ReviewStats>({ url: '/monitor/reviews/stats' })

export const listActionItems = (params: Record<string, any>) =>
  request<PageData<ActionItem>>({ url: '/monitor/action-items', params })
export const createActionItem = (eventId: number, data: Record<string, any>) =>
  request<ActionItem>({ url: `/monitor/events/${eventId}/action-items`, method: 'POST', data })
export const updateActionItem = (id: number, data: Record<string, any>) =>
  request({ url: `/monitor/action-items/${id}`, method: 'PUT', data })
export const finishActionItem = (id: number, note?: string) =>
  request({ url: `/monitor/action-items/${id}/done`, method: 'POST', data: { note } })
export const deleteActionItem = (id: number) =>
  request({ url: `/monitor/action-items/${id}`, method: 'DELETE' })

// ---------- 处置剧本 ----------

export interface RunbookStep {
  title: string
  detail: string
  command: string
}

export interface Runbook {
  id: number
  name: string
  category: string
  summary: string
  matchLabels: Record<string, string>
  matchKeywords: string
  matchSeverity: string
  steps: RunbookStep[]
  stepCount: number
  commandSteps: number
  precheck: string
  rollback: string
  riskLevel: string
  enabled: boolean
  precheckStatus: string
  precheckHits: string
  precheckedAt: string | null
  version: number
  useCount: number
  solveCount: number
  lastUsedAt: string | null
  lastUsedBy: string
  creatorName: string
  createdAt: string
  updatedAt: string
  /** 没有任何匹配条件，只作兜底推荐 */
  generic: boolean
}

export interface RunbookUse {
  id: number
  runbookId: number
  runbookName: string
  version: number
  eventId: number
  alertId: number
  outcome: string
  outcomeLabel: string
  doneSteps: number[]
  note: string
  operator: string
  createdAt: string
}

export interface RunbookMatch {
  runbook: Runbook
  score: number
  reasons: string[]
}

export interface RunbookMatchResult {
  target: string
  input: { labels: Record<string, string>; title: string; severity: string }
  matches: RunbookMatch[]
  note: string
}

export interface RunbookStats {
  total: number
  enabled: number
  blocked: number
  warn: number
  generic: number
  neverUsed: number
  uses: number
  solved: number
}

export const listRunbooks = (params: Record<string, any>) =>
  request<PageData<Runbook>>({ url: '/monitor/runbooks', params })
export const getRunbookStats = () => request<RunbookStats>({ url: '/monitor/runbooks/stats' })
export const getRunbook = (id: number) =>
  request<{ runbook: Runbook; uses: RunbookUse[] }>({ url: `/monitor/runbooks/${id}` })
export const createRunbook = (data: Record<string, any>) =>
  request<{ runbook: Runbook; precheckHits: ExecPrecheckHit[] }>({
    url: '/monitor/runbooks',
    method: 'POST',
    data
  })
export const updateRunbook = (id: number, data: Record<string, any>) =>
  request<{ runbook: Runbook; precheckHits: ExecPrecheckHit[] }>({
    url: `/monitor/runbooks/${id}`,
    method: 'PUT',
    data
  })
export const deleteRunbook = (id: number) =>
  request({ url: `/monitor/runbooks/${id}`, method: 'DELETE' })
export const recheckRunbooks = () =>
  request<{ total: number; changed: number; disabled: string[]; note: string }>({
    url: '/monitor/runbooks/recheck',
    method: 'POST'
  })
export const matchRunbooks = (params: { eventId?: number; alertId?: number }) =>
  request<RunbookMatchResult>({ url: '/monitor/runbooks/match', params })
export const useRunbook = (id: number, data: Record<string, any>) =>
  request<{ id: number }>({ url: `/monitor/runbooks/${id}/use`, method: 'POST', data })
export const listRunbookUses = (params: Record<string, any>) =>
  request<PageData<RunbookUse>>({ url: '/monitor/runbook-uses', params })
export const draftRunbookFromReview = (eventId: number, name?: string) =>
  request<{ draft: Record<string, any>; note: string }>({
    url: '/monitor/runbooks/draft-from-review',
    method: 'POST',
    data: { eventId, name }
  })

// ---------- 主机服务 ----------

export interface HostService {
  id: number
  hostId: number
  hostName: string
  unit: string
  name: string
  description: string
  expectActive: boolean
  expectEnabled: boolean
  critical: boolean
  owner: string
  alertEnabled: boolean
  remark: string
  loadState: string
  activeState: string
  subState: string
  enableState: string
  ports: string[]
  procCount: number
  drift: string
  driftLabel: string
  driftDetail: string
  lastCheckAt: string | null
  lastError: string
  createdAt: string
  updatedAt: string
}

export interface DiscoveredUnit {
  unit: string
  loadState: string
  activeState: string
  subState: string
  enableState: string
  description: string
  ports: number[]
  procCount: number
  managed: boolean
}

export interface HostServiceStats {
  total: number
  hosts: number
  ok: number
  inactive: number
  unexpected: number
  disabled: number
  missing: number
  error: number
  unknown: number
  critical: number
  criticalDrift: number
}

export interface HostServiceAction {
  id: number
  hostId: number
  hostName: string
  serviceId: number
  unit: string
  action: string
  status: string
  detail: string
  beforeState: string
  afterState: string
  execJobId: number
  operator: string
  clientIp: string
  createdAt: string
}

export const listHostServices = (params: Record<string, any>) =>
  request<PageData<HostService>>({ url: '/host-services', params })
export const getHostServiceStats = () =>
  request<HostServiceStats>({ url: '/host-services/stats' })
export const discoverHostServices = (hostId: number) =>
  request<{
    hostId: number
    hostName: string
    units: DiscoveredUnit[]
    total: number
    listenPorts: number[]
    orphanPorts: number[]
    portsUnavailable: boolean
    note: string
    orphanNote: string
  }>({ url: `/hosts/${hostId}/services/discover` })
export const adoptHostServices = (data: Record<string, any>) =>
  request<{ created: number; skipped: number; note?: string }>({
    url: '/host-services',
    method: 'POST',
    data
  })
export const updateHostService = (id: number, data: Record<string, any>) =>
  request<{ drift: string; driftDetail: string }>({
    url: `/host-services/${id}`,
    method: 'PUT',
    data
  })
export const deleteHostService = (id: number) =>
  request({ url: `/host-services/${id}`, method: 'DELETE' })
export const checkHostServices = (hostId: number) =>
  request<{ total: number; drifted: number }>({
    url: `/host-services/check?hostId=${hostId}`,
    method: 'POST'
  })
export const operateHostService = (id: number, data: Record<string, any>) =>
  request<{
    status: string
    execJobId: number
    beforeState: string
    afterState: string
    drift: string
    driftDetail: string
    detail: string
  }>({ url: `/host-services/${id}/operate`, method: 'POST', data })
export const listHostServiceActions = (params: Record<string, any>) =>
  request<PageData<HostServiceAction>>({ url: '/host-services/actions', params })

// ---------- 配置文件 ----------

export interface ConfigFile {
  id: number
  hostId: number
  hostName: string
  path: string
  name: string
  category: string
  owner: string
  critical: boolean
  reloadUnit: string
  reloadAction: string
  alertEnabled: boolean
  remark: string
  desiredVersionId: number
  desiredVersion: number
  desiredHash?: string
  desiredSize?: number
  versionSeq: number
  actualHash: string
  actualSize: number
  actualMode: string
  actualMtime: string | null
  diffLines: number
  drift: string
  driftLabel: string
  driftDetail: string
  lastCheckAt: string | null
  lastError: string
  createdAt: string
  updatedAt: string
}

export interface ConfigVersion {
  id: number
  fileId: number
  version: number
  source: string
  content?: string
  hash: string
  size: number
  mode: string
  note: string
  operator: string
  createdAt: string
}

export interface ConfigApply {
  id: number
  fileId: number
  hostId: number
  hostName: string
  path: string
  action: string
  fromVersionId: number
  toVersionId: number
  status: string
  backupPath: string
  verifyHash: string
  reloadStatus: string
  reloadDetail: string
  detail: string
  execJobId: number
  operator: string
  clientIp: string
  createdAt: string
}

export interface ConfigStats {
  total: number
  hosts: number
  versions: number
  ok: number
  drift: number
  missing: number
  noDesired: number
  error: number
  unknown: number
  critical: number
  criticalDrift: number
  noReload: number
}

export interface ConfigDiff {
  mode: string
  left: { label: string; hash: string }
  right: { label: string; hash: string; mode?: string; size?: number }
  same: boolean
  diffLines: number
  diff: string
  note?: string
}

export const listConfigFiles = (params: Record<string, any>) =>
  request<PageData<ConfigFile>>({ url: '/config-files', params })
export const getConfigStats = () => request<ConfigStats>({ url: '/config-files/stats' })
export const getConfigFile = (id: number) =>
  request<{ file: ConfigFile; versions: ConfigVersion[]; applies: ConfigApply[] }>({
    url: `/config-files/${id}`
  })
export const getConfigVersion = (id: number) =>
  request<ConfigVersion>({ url: `/config-versions/${id}` })
export const createConfigFile = (data: Record<string, any>) =>
  request<Record<string, any>>({ url: '/config-files', method: 'POST', data })
export const updateConfigFile = (id: number, data: Record<string, any>) =>
  request({ url: `/config-files/${id}`, method: 'PUT', data })
export const deleteConfigFile = (id: number, force = false) =>
  request({ url: `/config-files/${id}${force ? '?force=1' : ''}`, method: 'DELETE' })
export const captureConfigFile = (id: number, setDesired: boolean) =>
  request<{ version: number; versionId: number; hash: string; setDesired: boolean; drift: string }>({
    url: `/config-files/${id}/capture${setDesired ? '?setDesired=1' : ''}`,
    method: 'POST'
  })
export const editConfigVersion = (id: number, data: Record<string, any>) =>
  request<{ version: number; versionId: number; hash: string; setDesired: boolean; note: string }>({
    url: `/config-files/${id}/edit`,
    method: 'POST',
    data
  })
export const diffConfigFile = (id: number, params?: { from?: number; to?: number }) =>
  request<ConfigDiff>({ url: `/config-files/${id}/diff`, params })
export const checkConfigFiles = (hostId?: number) =>
  request<{ total: number; drifted: number; note?: string }>({
    url: `/config-files/check${hostId ? '?hostId=' + hostId : ''}`,
    method: 'POST'
  })
export const applyConfigFile = (id: number, data: Record<string, any>) =>
  request<Record<string, any>>({ url: `/config-files/${id}/apply`, method: 'POST', data })
export const rollbackConfigFile = (id: number, data: Record<string, any>) =>
  request<Record<string, any>>({ url: `/config-files/${id}/rollback`, method: 'POST', data })
export const listConfigApplies = (params: Record<string, any>) =>
  request<PageData<ConfigApply>>({ url: '/config-files/applies', params })





// ---------- 数据留存 ----------

export interface RetentionTarget {
  key: string
  label: string
  days: number
  total: number
  expired: number
  cascade: string
  note: string
  irreversible: boolean
}

export interface RetentionStatus {
  targets: RetentionTarget[]
  expiredTotal: number
  permanentCount: number
  spec: string
  lastRun: { at: string | null; info: string }
  detail: string
}

export interface RetentionRunItem {
  key: string
  label: string
  days: number
  deleted: number
  skipped: boolean
  reason?: string
  deadline?: string
}

export const getRetentionStatus = () => request<RetentionStatus>({ url: '/system/retention' })
export const runRetention = (dryRun: boolean) =>
  request<{ dryRun: boolean; affected: number; items: RetentionRunItem[]; detail: string }>({
    url: `/system/retention/run?dryRun=${dryRun}`,
    method: 'POST'
  })

// ---------- 主机导入导出 ----------

export interface HostImportRow {
  line: number
  name: string
  address: string
  action: 'create' | 'update' | 'skip'
  reason: string
}

export interface HostImportResult {
  dryRun: boolean
  total: number
  created: number
  updated: number
  skipped: number
  rows: HostImportRow[]
  detail: string
}

/** 导入主机 CSV。dryRun=true 时只校验不写入 */
export async function importHosts(file: File, dryRun: boolean): Promise<HostImportResult> {
  const form = new FormData()
  form.append('file', file)
  const res = await http.post<ApiBody<HostImportResult>>(`/hosts/import?dryRun=${dryRun}`, form)
  if (res.data.code !== 0) {
    throw new Error(res.data.msg)
  }
  return res.data.data
}

/** 下载 CSV 等文件：鉴权头不能走 <a href>，这里取 blob 再触发保存 */
async function downloadBlob(url: string, filename: string) {
  const res = await http.get(url, { responseType: 'blob' })
  const link = document.createElement('a')
  link.href = URL.createObjectURL(res.data as Blob)
  link.download = filename
  link.click()
  URL.revokeObjectURL(link.href)
}

export const downloadHostTemplate = () =>
  downloadBlob('/hosts/import-template', 'host-import-template.csv')
export const exportHostsCSV = (env?: string) =>
  downloadBlob(env ? `/hosts/export?env=${env}` : '/hosts/export', 'hosts.csv')

/** 导出审计：用的是和列表完全相同的检索条件，导出内容与界面所见一致 */
export const exportAuditLogsCSV = (params: Record<string, any>) => {
  const search = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value !== '' && value !== undefined && value !== null) search.append(key, String(value))
  })
  const query = search.toString()
  return downloadBlob(`/system/audit-logs/export${query ? `?${query}` : ''}`, 'audit-logs.csv')
}

/** 导出会话命令检索结果：条件与界面一致 */
export const exportSessionCommandsCSV = (params: Record<string, any>) => {
  const search = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value !== '' && value !== undefined && value !== null) search.append(key, String(value))
  })
  const query = search.toString()
  return downloadBlob(`/sessions/commands/export${query ? `?${query}` : ''}`, 'session-commands.csv')
}

// ---------- 脚本库 ----------

export interface ScriptParam {
  name: string
  label?: string
  default?: string
  required?: boolean
}

export interface PrecheckHit {
  ruleId: number
  pattern: string
  action: string
  description: string
  line: number
  snippet: string
}

export interface Script {
  id: number
  name: string
  category: string
  description: string
  content: string
  params: string
  timeout: number
  riskLevel: string
  precheckStatus: string
  precheckHits: string
  precheckedAt: string | null
  useCount: number
  lastUsedAt: string | null
  lastUsedBy: string
  enabled: boolean
  creatorName: string
  createdAt: string
}

export const listScripts = (params: Record<string, any>) =>
  request<PageData<Script>>({ url: '/exec/scripts', params })
export const listScriptCategories = () => request<string[]>({ url: '/exec/scripts/categories' })
export const createScript = (data: Record<string, any>) =>
  request<{ script: Script; precheckHits: PrecheckHit[] }>({ url: '/exec/scripts', method: 'POST', data })
export const updateScript = (id: number, data: Record<string, any>) =>
  request<{ script: Script; precheckHits: PrecheckHit[] }>({ url: `/exec/scripts/${id}`, method: 'PUT', data })
export const deleteScript = (id: number) =>
  request({ url: `/exec/scripts/${id}`, method: 'DELETE' })
export const precheckScript = (id: number) =>
  request<{ precheckStatus: string; precheckHits: PrecheckHit[]; detail: string }>({
    url: `/exec/scripts/${id}/precheck`,
    method: 'POST'
  })
export const renderScript = (id: number, params: Record<string, string>) =>
  request<{ command: string; timeout: number }>({
    url: `/exec/scripts/${id}/render`,
    method: 'POST',
    data: { params }
  })
export const runScript = (
  id: number,
  data: {
    hostIds: number[]
    params?: Record<string, string>
    timeout?: number
    confirmProd?: boolean
  }
) =>
  request<{ job: ExecJob; command: string; precheckStatus: string; precheckHits: PrecheckHit[] }>({
    url: `/exec/scripts/${id}/run`,
    method: 'POST',
    data
  })

// ---------- 业务拓扑 ----------

export type TopologyNodeKind = 'host' | 'database' | 'probe' | 'certificate' | 'external'
export type TopologyHealth = 'normal' | 'warning' | 'error' | 'unknown'

export interface TopologySummary {
  id: number
  name: string
  remark: string
  creatorName: string
  nodeCount: number
  edgeCount: number
  health: TopologyHealth
  problems: number
  updatedAt: string
}

export interface TopologyNode {
  id: number
  topologyId: number
  name: string
  kind: TopologyNodeKind
  refId: number
  x: number
  y: number
  remark: string
  /** 以下三个由后端实时算出，不落库 */
  health: TopologyHealth
  detail: string
  refName: string
}

export interface TopologyEdge {
  id: number
  topologyId: number
  fromNodeId: number
  toNodeId: number
  label: string
}

export interface TopologyDetail {
  topology: { id: number; name: string; remark: string }
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  counts: Record<TopologyHealth, number>
  health: TopologyHealth
  /** 依赖成环时给出闭合路径（节点名），不成环为 null */
  cycle: string[] | null
}

export interface BindableResource {
  id: number
  name: string
  detail: string
}

export const listTopologies = () => request<TopologySummary[]>({ url: '/monitor/topologies' })
export const getTopology = (id: number) => request<TopologyDetail>({ url: `/monitor/topologies/${id}` })
export const listTopologyResources = () =>
  request<Record<Exclude<TopologyNodeKind, 'external'>, BindableResource[]>>({
    url: '/monitor/topology/resources'
  })
export const createTopology = (data: { name: string; remark?: string }) =>
  request<TopologySummary>({ url: '/monitor/topologies', method: 'POST', data })
export const updateTopology = (id: number, data: { name: string; remark?: string }) =>
  request({ url: `/monitor/topologies/${id}`, method: 'PUT', data })
export const deleteTopology = (id: number) =>
  request<{ detail: string }>({ url: `/monitor/topologies/${id}`, method: 'DELETE' })
export const createTopologyNode = (id: number, data: Record<string, any>) =>
  request<TopologyNode>({ url: `/monitor/topologies/${id}/nodes`, method: 'POST', data })
export const updateTopologyNode = (id: number, nodeId: number, data: Record<string, any>) =>
  request({ url: `/monitor/topologies/${id}/nodes/${nodeId}`, method: 'PUT', data })
export const deleteTopologyNode = (id: number, nodeId: number) =>
  request<{ detail: string }>({ url: `/monitor/topologies/${id}/nodes/${nodeId}`, method: 'DELETE' })
export const saveTopologyLayout = (id: number, nodes: { id: number; x: number; y: number }[]) =>
  request<{ saved: number; detail: string }>({
    url: `/monitor/topologies/${id}/layout`,
    method: 'POST',
    data: { nodes }
  })
export const createTopologyEdge = (
  id: number,
  data: { fromNodeId: number; toNodeId: number; label?: string }
) =>
  request<{ edge: TopologyEdge; cycle: string[] | null }>({
    url: `/monitor/topologies/${id}/edges`,
    method: 'POST',
    data
  })
export const deleteTopologyEdge = (id: number, edgeId: number) =>
  request<{ detail: string }>({ url: `/monitor/topologies/${id}/edges/${edgeId}`, method: 'DELETE' })

// ---------- 检测规则 ----------

export type DetectionMode = 'concurrent' | 'sequence' | 'join'

export interface DetectionStep {
  name?: string
  titleKeyword?: string
  source?: string
  severity?: string
  labelKey?: string
  labelValue?: string
}

export interface DetectionRule {
  id: number
  name: string
  mode: DetectionMode
  modeLabel: string
  steps: string
  stepList: DetectionStep[]
  joinLabel: string
  windowMinutes: number
  severity: string
  lastStatus: string
  lastDetail: string
  lastEvalAt: string | null
  lastFireAt: string | null
  enabled: boolean
  remark: string
}

export interface DetectionMatchedAlert {
  id: number
  title: string
  severity: string
  sourceName: string
  status: string
  firstSeenAt: string
}

export interface DetectionStepResult {
  index: number
  label: string
  matched: DetectionMatchedAlert[]
  hit: boolean
}

export interface DetectionOutcome {
  hit: boolean
  steps: DetectionStepResult[]
  joinValues: string[] | null
  detail: string
}

export interface DetectionMeta {
  modes: { key: DetectionMode; label: string; hint: string }[]
  sources: string[]
  labelKeys: string[]
}

export const listDetectionRules = () => request<DetectionRule[]>({ url: '/monitor/detection-rules' })
export const listDetectionMeta = () => request<DetectionMeta>({ url: '/monitor/detection-rules/meta' })
export const createDetectionRule = (data: Record<string, any>) =>
  request<DetectionRule>({ url: '/monitor/detection-rules', method: 'POST', data })
export const updateDetectionRule = (id: number, data: Record<string, any>) =>
  request<DetectionRule>({ url: `/monitor/detection-rules/${id}`, method: 'PUT', data })
export const deleteDetectionRule = (id: number) =>
  request({ url: `/monitor/detection-rules/${id}`, method: 'DELETE' })
/** 预演：用草稿条件看每步各命中哪些告警，不落库、不写告警 */
export const previewDetection = (data: Record<string, any>) =>
  request<{ scanned: number; modeLabel: string; outcome: DetectionOutcome }>({
    url: '/monitor/detection-rules/preview',
    method: 'POST',
    data
  })
/** 试跑：和定时评估同一条路径，会真实写入状态与告警 */
export const evaluateDetectionRule = (id: number) =>
  request<{ status: string; scanned: number; modeLabel: string; outcome: DetectionOutcome }>({
    url: `/monitor/detection-rules/${id}/evaluate`,
    method: 'POST'
  })

// ---------- 容器平台 ----------

export interface KubeCluster {
  id: number
  name: string
  contextName: string
  server: string
  version: string
  status: 'unknown' | 'healthy' | 'degraded' | 'error'
  nodeTotal: number
  nodeReady: number
  lastCheckAt: string | null
  lastError: string
  enabled: boolean
  remark: string
}

export interface KubeCheckResult {
  status: string
  detail: string
  version?: string
  platform?: string
  nodeTotal?: number
  nodeReady?: number
}

export interface KubeNode {
  name: string
  ready: boolean
  roles: string[]
  version: string
  osImage: string
  internalIP: string
  cpu: string
  memory: string
  pods: string
  unschedulable: boolean
  createdAt: string
}

export interface KubeNamespace {
  name: string
  phase: string
  createdAt: string
}

export interface KubeWorkload {
  kind: 'Deployment' | 'StatefulSet' | 'DaemonSet'
  namespace: string
  name: string
  desired: number
  ready: number
  updated: number
  available: number
  images: string[]
  healthy: boolean
  createdAt: string
}

export interface KubePod {
  namespace: string
  name: string
  phase: string
  nodeName: string
  podIP: string
  ready: string
  restarts: number
  images: string[]
  containers: string[]
  initContainers: string[]
  restarted: boolean
  containerMsg: string
  createdAt: string
}

export interface KubePodLogs {
  namespace: string
  pod: string
  container: string
  previous: boolean
  logs: string
  lines: number
  truncated: boolean
  tailLines: number
}

export interface KubeEvent {
  namespace: string
  type: string
  reason: string
  object: string
  message: string
  count: number
  lastSeen: string
}

export const listKubeClusters = () => request<KubeCluster[]>({ url: '/kube/clusters' })
export const parseKubeconfigContexts = (kubeconfig: string) =>
  request<{ contexts: string[]; currentContext: string }>({
    url: '/kube/contexts',
    method: 'POST',
    data: { kubeconfig }
  })
export const createKubeCluster = (data: Record<string, any>) =>
  request<{ cluster: KubeCluster; check: KubeCheckResult }>({
    url: '/kube/clusters',
    method: 'POST',
    data
  })
export const updateKubeCluster = (id: number, data: Record<string, any>) =>
  request<KubeCluster>({ url: `/kube/clusters/${id}`, method: 'PUT', data })
export const deleteKubeCluster = (id: number) =>
  request<{ detail: string }>({ url: `/kube/clusters/${id}`, method: 'DELETE' })
export const checkKubeCluster = (id: number) =>
  request<KubeCheckResult>({ url: `/kube/clusters/${id}/check`, method: 'POST' })
export const listKubeNodes = (id: number) => request<KubeNode[]>({ url: `/kube/clusters/${id}/nodes` })
export const listKubeNamespaces = (id: number) =>
  request<KubeNamespace[]>({ url: `/kube/clusters/${id}/namespaces` })

/* ---- 按类型的专用页：Helm 应用 / 自定义资源 / Gateway API / RBAC 账户（全部只读）---- */

export interface KubeHelmRelease {
  name: string
  namespace: string
  revision: number
  status: string
  chart: string
  chartVersion: string
  appVersion: string
  description: string
  firstDeployedAt: string
  updatedAt: string
  revisions: number
  secretName: string
  hasNotes: boolean
}

export interface KubeCRD {
  name: string
  group: string
  kind: string
  plural: string
  singular: string
  shortNames: string[]
  categories: string[]
  scope: string
  namespaced: boolean
  servedVersions: string[]
  storageVersion: string
  createdAt: string
  established: boolean
  knownAs: string
}

export interface KubeGatewayRouteRule {
  matches: string[]
  backends: string[]
}

export interface KubeGatewayRoute {
  namespace: string
  name: string
  gateways: string[]
  hostnames: string[]
  rules: KubeGatewayRouteRule[]
}

export interface KubePolicyRule {
  apiGroups: string[]
  resources: string[]
  verbs: string[]
  resourceNames?: string[]
  nonResourceURLs?: string[]
}

export interface KubeRoleRef {
  bindingKind: string
  bindingName: string
  bindingNamespace: string
  roleKind: string
  roleName: string
  scope: string
  rules: KubePolicyRule[]
  missing: boolean
}

export interface KubeServiceAccount {
  name: string
  namespace: string
  createdAt: string
  secrets: string[]
  bindings: KubeRoleRef[]
  clusterAdmin: boolean
  wildcard: boolean
  verbSummary: string[]
}

export const listKubeHelmReleases = (id: number, params?: Record<string, any>) =>
  request<{
    releases: KubeHelmRelease[]
    byStatus: Record<string, number>
    total: number
    notes: string[]
  }>({ url: `/kube/clusters/${id}/helm-releases`, params })

export const listKubeCRDs = (id: number, params?: Record<string, any>) =>
  request<{
    crds: KubeCRD[]
    groups: string[]
    groupCounts: Record<string, number>
    total: number
    notEstablished: number
    gatewayAPI: string
    notes: string[]
  }>({ url: `/kube/clusters/${id}/crds`, params })

export const listKubeCRDResources = (id: number, params: Record<string, any>) =>
  request<{
    items: KubeResourceItem[]
    total: number
    shown: number
    truncated: boolean
    crd: KubeCRD
    kind: string
    apiVersion: string
    namespaced: boolean
    note: string
  }>({ url: `/kube/clusters/${id}/crd-resources`, params })

export const getKubeCRDResource = (id: number, params: Record<string, any>) =>
  request<{
    crd: string
    kind: string
    apiVersion: string
    name: string
    namespace: string
    yaml: string
    note: string
  }>({ url: `/kube/clusters/${id}/crd-resource`, params })

export const listKubeGatewayRoutes = (id: number, params?: Record<string, any>) =>
  request<{
    installed: boolean
    version?: string
    routes: KubeGatewayRoute[]
    total?: number
    note?: string
    notes?: string[]
  }>({ url: `/kube/clusters/${id}/gateway-routes`, params })

export const listKubeRBAC = (id: number, params?: Record<string, any>) =>
  request<{
    accounts: KubeServiceAccount[]
    total: number
    shown: number
    stats: Record<string, number>
    notes: string[]
  }>({ url: `/kube/clusters/${id}/rbac`, params })

export interface KubeNodeTaint {
  key: string
  value: string
  effect: string
}

export interface KubeNodeCondition {
  type: string
  status: string
  reason: string
  message: string
  problem: boolean
}

export interface KubeNodeDetail {
  name: string
  ready: boolean
  roles: string[]
  version: string
  osImage: string
  kernel: string
  runtime: string
  internalIP: string
  capacityCpu: string
  capacityMemory: string
  capacityPods: string
  allocCpu: string
  allocMemory: string
  allocPods: string
  podCount: number
  podCapacity: number
  unschedulable: boolean
  taints: KubeNodeTaint[]
  conditions: KubeNodeCondition[]
  problems: string[]
  createdAt: string
}

export interface KubeNamespaceQuota {
  name: string
  items: string[]
}

export interface KubeNamespaceDetail {
  name: string
  phase: string
  createdAt: string
  terminating: boolean
  finalizers: string[]
  conditions: string[]
  labels: Record<string, string>
  podTotal: number
  podRunning: number
  podPending: number
  podFailed: number
  quotas: KubeNamespaceQuota[]
  hasLimitRange: boolean
  problems: string[]
}

export const listKubeNodeInventory = (id: number, params?: Record<string, any>) =>
  request<{
    nodes: KubeNodeDetail[]
    total: number
    shown: number
    ready: number
    notReady: number
    cordoned: number
    tainted: number
    versions: string[]
    versionSkew: boolean
    podCounted: boolean
    notes: string[]
  }>({ url: `/kube/clusters/${id}/node-inventory`, params })

export const listKubeNamespaceInventory = (id: number, params?: Record<string, any>) =>
  request<{
    namespaces: KubeNamespaceDetail[]
    total: number
    shown: number
    terminating: number
    noQuota: number
    podCounted: boolean
    quotaRead: boolean
    notes: string[]
  }>({ url: `/kube/clusters/${id}/namespace-inventory`, params })
export const listKubeWorkloads = (id: number, namespace?: string) =>
  request<{ items: KubeWorkload[]; total: number; unhealthy: number; warnings: string[] }>({
    url: `/kube/clusters/${id}/workloads`,
    params: namespace ? { namespace } : {}
  })
export const listKubePods = (id: number, namespace?: string) =>
  request<{ items: KubePod[]; total: number; abnormal: number }>({
    url: `/kube/clusters/${id}/pods`,
    params: namespace ? { namespace } : {}
  })
export const getKubePodLogs = (id: number, params: Record<string, any>) =>
  request<KubePodLogs>({ url: `/kube/clusters/${id}/pod-logs`, params })
export const listKubeEvents = (id: number, namespace?: string, onlyWarning?: boolean) =>
  request<{ items: KubeEvent[]; total: number }>({
    url: `/kube/clusters/${id}/events`,
    params: { ...(namespace ? { namespace } : {}), ...(onlyWarning ? { onlyWarning: 'true' } : {}) }
  })

// ---------- 集群资源管理 ----------

export interface KubeResourceKind {
  kind: string
  apiVersion: string
  resource: string
  namespaced: boolean
  scalable: boolean
  /** 支持滚动重启（有 Pod 模板的工作负载） */
  restartable: boolean
  /** 只读：apply / scale / restart 都会被拒 */
  readOnly: boolean
  /** 详情脱敏（Secret 只给键名不给值），因此也不能提交 */
  redacted: boolean
  /** 界面分组：工作负载 / 网络 / 配置 / 存储 / 集群 */
  group: string
}

export interface KubeResourceItem {
  kind: string
  namespace: string
  name: string
  summary: string
  createdAt: string
  labels?: Record<string, string>
}

export interface KubeResourceDetail {
  kind: string
  namespace: string
  name: string
  scalable: boolean
  restartable: boolean
  readOnly: boolean
  redacted: boolean
  yaml: string
  hint: string
  /** 非空表示这个对象能按标签下钻到 Pod */
  podSelector: string
}

export interface KubeRestartResult {
  kind: string
  namespace: string
  name: string
  restartedAt: string
  replicas: number
  dryRun: boolean
  detail: string
}

export interface KubeEventItem {
  namespace: string
  type: string
  reason: string
  message: string
  object: string
  count: number
  lastSeen: string
}


export interface KubeApplyResult {
  kind: string
  namespace: string
  name: string
  dryRun: boolean
  forced: boolean
  replicas: number
  detail: string
}

export interface KubeScaleResult {
  kind: string
  namespace: string
  name: string
  previous: number
  current: number
  dryRun: boolean
  detail: string
}

export interface KubeChangeLog {
  id: number
  clusterId: number
  clusterName: string
  kind: string
  namespace: string
  name: string
  action: 'apply' | 'scale' | 'restart'
  dryRun: boolean
  forced: boolean
  payload: string
  status: 'success' | 'failed'
  detail: string
  username: string
  clientIp: string
  costMs: number
  createdAt: string
}

export const listKubeResourceKinds = () => request<KubeResourceKind[]>({ url: '/kube/resource-kinds' })
export const listKubeResources = (
  id: number,
  kind: string,
  params?: { namespace?: string; keyword?: string; labelSelector?: string }
) =>
  request<{
    items: KubeResourceItem[]
    total: number
    matched: number
    kind: string
    scalable: boolean
    restartable: boolean
    readOnly: boolean
    namespaced: boolean
    namespaceIgnored: boolean
  }>({
    url: `/kube/clusters/${id}/resources`,
    params: { kind, ...(params || {}) }
  })
export const getKubeResource = (id: number, kind: string, namespace: string, name: string) =>
  request<KubeResourceDetail>({
    url: `/kube/clusters/${id}/resource`,
    params: { kind, namespace, name }
  })
export const listKubeObjectEvents = (id: number, kind: string, namespace: string, name: string) =>
  request<{ items: KubeEventItem[]; total: number; note: string }>({
    url: `/kube/clusters/${id}/resource/events`,
    params: { kind, namespace, name }
  })
export const listKubeRelatedPods = (id: number, kind: string, namespace: string, name: string) =>
  request<{ items: KubeResourceItem[]; total: number; selector: string; note: string }>({
    url: `/kube/clusters/${id}/resource/pods`,
    params: { kind, namespace, name }
  })
export const restartKubeWorkload = (
  id: number,
  data: { kind: string; namespace: string; name: string; dryRun: boolean }
) => request<KubeRestartResult>({ url: `/kube/clusters/${id}/resource/restart`, method: 'POST', data })

export interface KubePodContainer {
  name: string
  image: string
  init: boolean
  ready: boolean
  restarts: number
  /** running | waiting | terminated | unknown（status 里还没有这个容器） */
  state: string
  reason: string
  message: string
  /** -1 表示没有这个信息；0 是正常退出 */
  exitCode: number
  /** 上一次退出的原因与退出码，CrashLoop 时最关键的一行 */
  lastTerminated: string
  startedAt: string
  ports: string
  requests: string
  limits: string
  probes: string
  mounts: string[]
}

export interface KubePodDetailData {
  name: string
  namespace: string
  phase: string
  reason: string
  message: string
  nodeName: string
  podIp: string
  hostIp: string
  qosClass: string
  serviceAccount: string
  startedAt: string
  owner: string
  restarts: number
  readyCount: number
  totalCount: number
  containers: KubePodContainer[]
  conditions: { type: string; status: string; reason: string; message: string; lastTransition: string }[]
  volumes: { name: string; type: string; source: string }[]
  nodeSelector: string
  logContainers: string[]
}

export const getKubePodDetail = (id: number, namespace: string, name: string) =>
  request<{ detail: KubePodDetailData; note: string }>({
    url: `/kube/clusters/${id}/pod`,
    params: { namespace, name }
  })

export interface KubeNodeCapacity {
  name: string
  roles: string
  ready: boolean
  schedulable: boolean
  podCount: number
  podCapacity: number
  cpuCapacity: string
  cpuAllocatable: string
  cpuRequests: string
  cpuLimits: string
  cpuUsage: string
  cpuRequestPct: number
  cpuLimitPct: number
  cpuUsagePct: number
  memCapacity: string
  memAllocatable: string
  memRequests: string
  memLimits: string
  memUsage: string
  memRequestPct: number
  memLimitPct: number
  memUsagePct: number
  /** 这个节点有没有实际用量数据（要 metrics-server） */
  hasUsage: boolean
}

export interface KubeQuotaItem {
  name: string
  resource: string
  hard: string
  used: string
  percent: number
}

export interface KubeNamespaceCapacity {
  namespace: string
  podCount: number
  cpuRequests: string
  cpuLimits: string
  memRequests: string
  memLimits: string
  cpuUsage: string
  memUsage: string
  hasUsage: boolean
  quotas: KubeQuotaItem[]
  /** 没配 limits 的 Pod 数：这些 Pod 能把节点吃满 */
  noLimitPods: number
}

export const getKubeCapacity = (id: number, namespace?: string) =>
  request<{
    nodes: KubeNodeCapacity[]
    namespaces: KubeNamespaceCapacity[]
    podsCounted: number
    podsSkipped: number
    metricsAvailable: boolean
    metricsNote: string
    note: string
  }>({ url: `/kube/clusters/${id}/capacity`, params: namespace ? { namespace } : {} })



export const applyKubeResource = (id: number, data: { yaml: string; dryRun: boolean; force?: boolean }) =>
  request<KubeApplyResult>({ url: `/kube/clusters/${id}/resource/apply`, method: 'POST', data })
export const scaleKubeResource = (
  id: number,
  data: { kind: string; namespace: string; name: string; replicas: number; dryRun: boolean }
) => request<KubeScaleResult>({ url: `/kube/clusters/${id}/resource/scale`, method: 'POST', data })
export const listKubeChangeLogs = (params: Record<string, any>) =>
  request<PageData<KubeChangeLog>>({ url: '/kube/change-logs', params })

// ---------- 集群服务转发 ----------

export interface KubeForward {
  id: number
  clusterId: number
  clusterName: string
  namespace: string
  targetKind: 'pod' | 'service'
  targetName: string
  targetPort: number
  listenAddr: string
  listenPort: number
  status: 'running' | 'stopped' | 'error'
  errorMsg: string
  connTotal: number
  connFailed: number
  connActive: number
  bytesIn: number
  bytesOut: number
  username: string
  clientIp: string
  expiresAt: string
  lastActiveAt: string | null
  closedAt: string | null
  createdAt: string
}

/** 部署时定下的约束，界面上直接展示，免得用户靠报错去猜 */
export interface KubeForwardLimits {
  bind: string
  portMin: number
  portMax: number
  maxTunnels: number
  ttlMinutes: number
  connMax: number
  running: number
}

export interface KubeForwardPage extends PageData<KubeForward> {
  limits: KubeForwardLimits
}

export const listKubeForwards = (params: Record<string, any>) =>
  request<KubeForwardPage>({ url: '/kube/forwards', params })
export const createKubeForward = (data: {
  clusterId: number
  namespace: string
  targetKind: 'pod' | 'service'
  targetName: string
  targetPort: number
  listenPort?: number
  ttlMinutes?: number
}) =>
  request<{ id: number; listenAddr: string; listenPort: number; expiresAt: string }>({
    url: '/kube/forwards',
    method: 'POST',
    data
  })
export const closeKubeForward = (id: number) =>
  request<{ closed: boolean; reason?: string }>({ url: `/kube/forwards/${id}`, method: 'DELETE' })

// ---------- 模型资源池 ----------

export interface ModelUpstream {
  id: number
  name: string
  alias: string
  provider: string
  baseUrl: string
  model: string
  weight: number
  timeoutSec: number
  inputPrice: number
  outputPrice: number
  status: 'unknown' | 'healthy' | 'error'
  modelListed: boolean
  latencyMs: number
  lastError: string
  lastCheckAt: string | null
  enabled: boolean
  remark: string
  createdAt: string
}

/** 同 Alias 的上游算一个池子，页面上要能看出哪个 Alias 压根没有可用上游 */
export interface ModelPoolStat {
  alias: string
  total: number
  enabled: number
  healthy: number
}

export interface ModelCheckResult {
  status: 'healthy' | 'error'
  detail: string
  modelListed?: boolean
  latencyMs?: number
  modelCount?: number
}

export interface ModelCall {
  id: number
  upstreamId: number
  upstreamName: string
  alias: string
  provider: string
  model: string
  caller: 'api' | 'console'
  username: string
  clientIp: string
  promptTokens: number
  completionTokens: number
  totalTokens: number
  cost: number
  usageMissing: boolean
  latencyMs: number
  status: 'success' | 'failed'
  errorMsg: string
  retried: boolean
  createdAt: string
}

export interface ModelCallSummary {
  calls: number
  failed: number
  tokens: number
  cost: number
  avgLatencyMs: number
  usageMissing: number
}

export interface ModelChatResult {
  content: string
  alias: string
  model: string
  provider: string
  upstreamId: number
  upstreamName: string
  finishReason: string
  usage: { promptTokens: number; completionTokens: number; totalTokens: number; missing: boolean }
  cost: number
  latencyMs: number
  callId: number
  attempts: { upstreamId: number; upstreamName: string; status: string; error?: string }[]
}

export const listModelUpstreams = (params?: Record<string, any>) =>
  request<{ list: ModelUpstream[]; pools: ModelPoolStat[] }>({ url: '/ai/upstreams', params })
export const createModelUpstream = (data: Record<string, any>) =>
  request<{ upstream: ModelUpstream; check: ModelCheckResult }>({
    url: '/ai/upstreams',
    method: 'POST',
    data
  })
export const updateModelUpstream = (id: number, data: Record<string, any>) =>
  request<ModelUpstream>({ url: `/ai/upstreams/${id}`, method: 'PUT', data })
export const deleteModelUpstream = (id: number) =>
  request({ url: `/ai/upstreams/${id}`, method: 'DELETE' })
export const checkModelUpstream = (id: number) =>
  request<ModelCheckResult>({ url: `/ai/upstreams/${id}/check`, method: 'POST' })
/** 走网关调模型：传 Alias 走池子挑选，传 upstreamId 则指定试某一条 */
export const chatCompletion = (data: {
  model?: string
  upstreamId?: number
  messages: { role: string; content: string }[]
  temperature?: number
  maxTokens?: number
  caller?: 'console'
}) => request<ModelChatResult>({ url: '/ai/chat/completions', method: 'POST', data })
export const listModelCalls = (params: Record<string, any>) =>
  request<PageData<ModelCall> & { summary: ModelCallSummary }>({ url: '/ai/calls', params })

/** 一个时间桶的用量 */
export interface ModelUsageBucket {
  label: string
  calls: number
  failed: number
  tokens: number
  cost: number
  avgLatencyMs: number
}

export interface ModelUsageRank {
  name: string
  calls: number
  failed: number
  tokens: number
  cost: number
  avgLatencyMs: number
}

export interface ModelUsageOverview {
  summary: {
    calls: number
    failed: number
    successRate: number
    promptTokens: number
    completionTokens: number
    tokens: number
    cost: number
    avgLatencyMs: number
    maxLatencyMs: number
    usageMissing: number
    retried: number
  }
  trend: ModelUsageBucket[]
  byAlias: ModelUsageRank[]
  byUpstream: ModelUsageRank[]
  byUser: ModelUsageRank[]
  bucket: 'day' | 'hour'
  rowsScanned: number
  truncated: boolean
}

export const getModelUsage = (params: Record<string, any>) =>
  request<ModelUsageOverview>({ url: '/ai/usage', params })

/** 导出调用流水：条件与界面一致，只有元数据、没有正文 */
export const exportModelCallsCSV = (params: Record<string, any>) => {
  const search = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value !== '' && value !== undefined && value !== null) search.append(key, String(value))
  })
  const query = search.toString()
  return downloadBlob(`/ai/calls/export${query ? `?${query}` : ''}`, 'model-calls.csv')
}

// ---------- Agent ----------

export interface AgentConfig {
  id: number
  name: string
  alias: string
  dataSource: 'none' | 'alert' | 'host_metric' | 'exec_job' | 'session_command'
  maxItems: number
  systemPrompt: string
  promptTemplate: string
  temperature: number
  maxTokens: number
  enabled: boolean
  remark: string
  createdAt: string
}

/** 数据来源的元信息：needsTarget 决定界面要不要让人选目标对象 */
export interface AgentDataSource {
  key: string
  label: string
  needsTarget: boolean
}

export interface AgentRun {
  id: number
  agentId: number
  agentName: string
  alias: string
  dataSource: string
  targetId: number
  input: string
  output: string
  contextItems: number
  contextChars: number
  contextTruncated: boolean
  callId: number
  promptTokens: number
  completionTokens: number
  totalTokens: number
  cost: number
  latencyMs: number
  status: 'success' | 'failed'
  errorMsg: string
  username: string
  createdAt: string
}

export const listAgentConfigs = (params?: Record<string, any>) =>
  request<{ list: AgentConfig[]; dataSources: AgentDataSource[] }>({ url: '/ai/agents', params })
export const createAgentConfig = (data: Record<string, any>) =>
  request<AgentConfig>({ url: '/ai/agents', method: 'POST', data })
export const updateAgentConfig = (id: number, data: Record<string, any>) =>
  request<AgentConfig>({ url: `/ai/agents/${id}`, method: 'PUT', data })
export const deleteAgentConfig = (id: number) =>
  request({ url: `/ai/agents/${id}`, method: 'DELETE' })
/** 运行 Agent：拼平台数据 → 调模型 → 存结论 */
export const runAgent = (
  id: number,
  data: { targetId?: number; severity?: string; input?: string }
) =>
  request<{
    run: AgentRun
    prompt: string
    upstreamName: string
    model: string
    contextSummary: string
  }>({ url: `/ai/agents/${id}/run`, method: 'POST', data })
export const listAgentRuns = (params: Record<string, any>) =>
  request<PageData<AgentRun>>({ url: '/ai/agent-runs', params })
export const getAgentRun = (id: number) => request<AgentRun>({ url: `/ai/agent-runs/${id}` })

// ---------- IM 集成（组织同步） ----------

export interface ImApp {
  id: number
  name: string
  provider: 'wecom' | 'dingtalk' | 'feishu'
  corpId: string
  agentId: string
  baseUrl: string
  rootDeptId: string
  targetCompanyId: number
  defaultRoleId: number
  disableMissing: boolean
  loginEnabled: boolean
  redirectUri: string
  loginRedirect: string
  status: 'unknown' | 'healthy' | 'error'
  lastError: string
  lastCheckAt: string | null
  lastSyncAt: string | null
  enabled: boolean
  remark: string
  createdAt: string
}

export interface ImAccount {
  id: number
  appId: number
  provider: string
  imUserId: string
  imName: string
  imMobile: string
  imEmail: string
  imDeptPath: string
  userId: number
  username: string
  lastSyncAt: string | null
}

export interface ImSyncRun {
  id: number
  appId: number
  appName: string
  provider: string
  dryRun: boolean
  deptTotal: number
  deptCreated: number
  deptUpdated: number
  userTotal: number
  userCreated: number
  userBound: number
  userUpdated: number
  userDisabled: number
  userSkipped: number
  status: 'success' | 'failed'
  errorMsg: string
  costMs: number
  operator: string
  createdAt: string
}

/** 预演与执行返回同一种报告，dryRun 为 true 时一行都没写库 */
export interface ImSyncReport {
  dryRun: boolean
  changes: {
    kind: 'dept' | 'user'
    action: 'create' | 'update' | 'bind' | 'disable' | 'skip'
    name: string
    imId: string
    detail: string
  }[]
  deptTotal: number
  deptCreated: number
  deptUpdated: number
  userTotal: number
  userCreated: number
  userBound: number
  userUpdated: number
  userDisabled: number
  userSkipped: number
}

export const listImApps = () =>
  request<{ list: { app: ImApp; boundCount: number }[] }>({ url: '/system/im/apps' })
export const createImApp = (data: Record<string, any>) =>
  request<{ app: ImApp; check: { status: string; detail: string } }>({
    url: '/system/im/apps',
    method: 'POST',
    data
  })
export const updateImApp = (id: number, data: Record<string, any>) =>
  request<ImApp>({ url: `/system/im/apps/${id}`, method: 'PUT', data })
export const deleteImApp = (id: number) =>
  request<{ note: string }>({ url: `/system/im/apps/${id}`, method: 'DELETE' })
export const checkImApp = (id: number) =>
  request<{ status: string; detail: string; deptCount?: number; latencyMs?: number }>({
    url: `/system/im/apps/${id}/check`,
    method: 'POST'
  })
export const syncImApp = (id: number, dryRun: boolean) =>
  request<{ run: ImSyncRun; report: ImSyncReport }>({
    url: `/system/im/apps/${id}/sync`,
    method: 'POST',
    data: { dryRun }
  })
export const listImSyncRuns = (params: Record<string, any>) =>
  request<PageData<ImSyncRun>>({ url: '/system/im/sync-runs', params })
export const listImAccounts = (params: Record<string, any>) =>
  request<PageData<ImAccount>>({ url: '/system/im/accounts', params })
export const unbindImAccount = (id: number) =>
  request<{ note: string }>({ url: `/system/im/accounts/${id}`, method: 'DELETE' })

// ---------- LDAP / AD 账号接入 ----------

export interface LdapServer {
  id: number
  name: string
  host: string
  port: number
  encryption: 'none' | 'ldaps' | 'starttls'
  skipVerify: boolean
  bindDn: string
  baseDn: string
  userFilter: string
  attrNickname: string
  attrEmail: string
  timeoutSec: number
  enabled: boolean
  loginEnabled: boolean
  autoBind: boolean
  lastCheckAt: string | null
  lastStatus: string
  lastMessage: string
  createdAt: string
  /** 只告诉界面「服务账号口令配过没有」，口令本身不回传 */
  hasBindPassword: boolean
  url: string
  boundCount: number
}

export interface LdapDirectoryUser {
  dn: string
  uid: string
  nickname: string
  email: string
  /** 非空表示已绑定到这个平台账号 */
  boundTo: string
}

export interface LdapAccount {
  id: number
  serverId: number
  ldapUid: string
  ldapDn: string
  ldapName: string
  ldapEmail: string
  userId: number
  username: string
  boundBy: string
  lastLogin: string | null
  createdAt: string
}

export interface LdapTryStep {
  step: string
  ok: boolean
  detail: string
}

export const listLdapServers = () => request<LdapServer[]>({ url: '/system/ldap/servers' })
export const createLdapServer = (data: Record<string, any>) =>
  request<LdapServer>({ url: '/system/ldap/servers', method: 'POST', data })
export const updateLdapServer = (id: number, data: Record<string, any>) =>
  request<LdapServer>({ url: `/system/ldap/servers/${id}`, method: 'PUT', data })
export const deleteLdapServer = (id: number) =>
  request({ url: `/system/ldap/servers/${id}`, method: 'DELETE' })
export const checkLdapServer = (id: number) =>
  request<{ status: string; userCount: number; detail: string; url: string }>({
    url: `/system/ldap/servers/${id}/check`,
    method: 'POST'
  })
export const searchLdapUsers = (id: number, keyword: string) =>
  request<{ list: LdapDirectoryUser[]; total: number; limit: number }>({
    url: `/system/ldap/servers/${id}/users`,
    params: { keyword }
  })
export const tryLdapLogin = (data: { serverId: number; username: string; password: string }) =>
  request<{ ok: boolean; detail: string; steps: LdapTryStep[] }>({
    url: '/system/ldap/try-login',
    method: 'POST',
    data
  })
export const listLdapAccounts = (params?: Record<string, any>) =>
  request<PageData<LdapAccount>>({ url: '/system/ldap/accounts', params })
export const bindLdapAccount = (data: {
  serverId: number
  userId: number
  ldapDn: string
  ldapUid?: string
}) => request<LdapAccount>({ url: '/system/ldap/accounts', method: 'POST', data })
export const unbindLdapAccount = (id: number) =>
  request<{ detail: string }>({ url: `/system/ldap/accounts/${id}`, method: 'DELETE' })

// ---------- API 令牌（服务账号） ----------

export interface ApiToken {
  id: number
  name: string
  /** 明文前 12 位，仅用于辨认，不足以还原令牌 */
  prefix: string
  ownerUserId: number
  ownerName: string
  scopes: string
  scopeList: string[]
  readOnly: boolean
  allowIps: string
  expiresAt: string | null
  enabled: boolean
  lastUsedAt: string | null
  lastUsedIp: string
  useCount: number
  revokedAt: string | null
  revokedBy: string
  remark: string
  createdBy: string
  createdAt: string
  status: 'active' | 'disabled' | 'revoked' | 'expired'
}

export interface ApiTokenCreated {
  token: ApiToken
  /** 明文只在创建与轮换时返回一次 */
  plain: string
  detail: string
  usage?: string
  effectiveScopes?: { granted: string[]; ignored: string[] }
}

export const listApiTokens = (params?: Record<string, any>) =>
  request<PageData<ApiToken>>({ url: '/system/api-tokens', params })
export const listApiTokenScopes = () =>
  request<{ list: { code: string; title: string }[]; total: number }>({
    url: '/system/api-tokens/scopes'
  })
export const createApiToken = (data: Record<string, any>) =>
  request<ApiTokenCreated>({ url: '/system/api-tokens', method: 'POST', data })
export const updateApiToken = (id: number, data: Record<string, any>) =>
  request<ApiToken>({ url: `/system/api-tokens/${id}`, method: 'PUT', data })
export const rotateApiToken = (id: number) =>
  request<ApiTokenCreated>({ url: `/system/api-tokens/${id}/rotate`, method: 'POST' })
export const revokeApiToken = (id: number) =>
  request<ApiToken>({ url: `/system/api-tokens/${id}/revoke`, method: 'POST' })
export const deleteApiToken = (id: number) =>
  request({ url: `/system/api-tokens/${id}`, method: 'DELETE' })



// ---------- IM 扫码登录（免鉴权） ----------

export interface ImLoginProvider {
  id: number
  name: string
  provider: 'wecom' | 'dingtalk' | 'feishu'
}

/** 登录页可用的扫码方式，只返回 id/名称/类型，不带任何凭据 */
export const listImLoginProviders = () =>
  request<ImLoginProvider[]>({ url: '/public/im-logins' })
export const getImAuthorizeUrl = (appId: number) =>
  request<{ authorizeUrl: string; state: string; expiresIn: number }>({
    url: '/auth/im/authorize',
    params: { appId }
  })
/** 用回调带回的一次性 ticket 换 JWT（JWT 不走 URL） */
export const imLoginExchange = (ticket: string) =>
  request<{
    token: string
    expiresAt: number
    user: { id: number; username: string; nickname: string }
    loginBy: string
  }>({ url: '/auth/im/exchange', method: 'POST', data: { ticket } })

// ---------- 主机指标 ----------

export interface HostMetric {
  id: number
  hostId: number
  cpuPercent: number
  memPercent: number
  swapPercent: number
  diskMaxPercent: number
  diskMaxMount: string
  /** inode 最满的挂载点；inodeRead 为 false 表示这条采样没有 inode 数据（不是 0%） */
  inodeMaxPercent: number
  inodeMaxMount: string
  inodeRead: boolean
  load1: number
  load5: number
  load15: number
  cpuCores: number
  memTotalMB: number
  memUsedMB: number
  procCount: number
  tcpConn: number
  uptimeSec: number
  status: 'ok' | 'failed'
  error: string
  costMs: number
  createdAt: string
}

export interface HostMetricRow {
  hostId: number
  hostName: string
  address: string
  env: string
  /** 为 null 表示这台还没采过 */
  metric: HostMetric | null
  loadPerCore: number
  /** 最新采样已超过 staleMinutes，页面上要标出来 */
  stale: boolean
}

export const listHostMetrics = (env?: string) =>
  request<{
    items: HostMetricRow[]
    total: number
    collected: number
    failed: number
    staleMinutes: number
    spec: string
  }>({ url: '/hosts/metrics', params: env ? { env } : {} })
export const collectAllHostMetrics = () =>
  request<{ total: number; ok: number; failed: number; detail: string }>({
    url: '/hosts/metrics/collect',
    method: 'POST'
  })
export const collectHostMetric = (hostId: number) =>
  request<{ metric: HostMetric; detail: string }>({
    url: `/hosts/${hostId}/metrics/collect`,
    method: 'POST'
  })
export const listHostMetricHistory = (hostId: number, hours: number) =>
  request<{ items: HostMetric[]; total: number; hours: number }>({
    url: `/hosts/${hostId}/metrics`,
    params: { hours }
  })

// ---------- 值班升级 ----------

export type OnCallRotation = 'hourly' | 'daily' | 'weekly'

export interface UserBrief {
  id: number
  username: string
  nickname: string
  email: string
}

export interface OnCallPerson {
  level: number
  userId: number
  userName: string
  nickname: string
  email: string
  source: 'rotation' | 'override'
}

export interface OnCallSchedule {
  id: number
  name: string
  members: string
  rotation: OnCallRotation
  rotationLabel: string
  startAt: string
  matchSeverity: string
  ackWaitMinutes: number
  maxLevel: number
  notifyEmail: boolean
  enabled: boolean
  remark: string
  memberList: UserBrief[]
  current: OnCallPerson | null
  nextRotateAt: string | null
}

export interface OnCallShift {
  startAt: string
  endAt: string
  person: OnCallPerson | null
}

export interface OnCallOverride {
  id: number
  scheduleId: number
  userId: number
  userName: string
  startAt: string
  endAt: string
  reason: string
  active: boolean
}

export interface AlertEscalation {
  id: number
  alertId: number
  alertTitle: string
  scheduleId: number
  level: number
  userId: number
  userName: string
  source: string
  channel: string
  status: string
  detail: string
  createdAt: string
}

export const listOnCallSchedules = () => request<OnCallSchedule[]>({ url: '/monitor/oncall/schedules' })
export const listOnCallCandidates = () =>
  request<{ users: UserBrief[]; rotations: { key: OnCallRotation; label: string }[] }>({
    url: '/monitor/oncall/candidates'
  })
export const createOnCallSchedule = (data: Record<string, any>) =>
  request<OnCallSchedule>({ url: '/monitor/oncall/schedules', method: 'POST', data })
export const updateOnCallSchedule = (id: number, data: Record<string, any>) =>
  request<OnCallSchedule>({ url: `/monitor/oncall/schedules/${id}`, method: 'PUT', data })
export const deleteOnCallSchedule = (id: number) =>
  request<{ detail: string }>({ url: `/monitor/oncall/schedules/${id}`, method: 'DELETE' })
export const previewOnCall = (id: number, days: number) =>
  request<{ shifts: OnCallShift[]; rotationLabel: string }>({
    url: `/monitor/oncall/schedules/${id}/preview`,
    params: { days }
  })
/** 手动试跑：和定时扫描同一条路径，会真实发站内消息并落升级记录 */
export const runOnCallEscalation = (id: number) =>
  request<{ called: number; detail: string }>({
    url: `/monitor/oncall/schedules/${id}/run`,
    method: 'POST'
  })
export const listOnCallOverrides = (id: number) =>
  request<OnCallOverride[]>({ url: `/monitor/oncall/schedules/${id}/overrides` })
export const createOnCallOverride = (id: number, data: Record<string, any>) =>
  request<OnCallOverride>({ url: `/monitor/oncall/schedules/${id}/overrides`, method: 'POST', data })
export const deleteOnCallOverride = (id: number, overrideId: number) =>
  request<{ detail: string }>({
    url: `/monitor/oncall/schedules/${id}/overrides/${overrideId}`,
    method: 'DELETE'
  })
export const listAlertEscalations = (params: Record<string, any>) =>
  request<PageData<AlertEscalation>>({ url: '/monitor/oncall/escalations', params })

// ---------- 防火墙策略 ----------
//
// 关键约定：GetFirewallState 是「现读真机 + 与平台登记对账」，每次都真连主机，
// 所以它慢（20s 量级超时），不要放在轮询里。rules 列表是平台侧登记，读的是数据库。

/** 平台登记的一条规则 */
export interface FirewallRule {
  id: number
  hostId: number
  groupId: number
  direction: 'in' | 'out'
  action: 'accept' | 'drop' | 'reject'
  protocol: string
  source: string
  port: string
  service: string
  ruleKey: string
  description: string
  owner: string
  lifecycle: 'permanent' | 'temporary'
  expiresAt: string | null
  origin: 'platform' | 'discovered'
  state: 'pending' | 'synced' | 'drift' | 'extra'
  enabled: boolean
  hits: number
  hasCounter: boolean
  lastCheckAt: string | null
  lastHitAt: string | null
  createdBy: string
  createdAt: string
}

/** 真机规则与平台登记合并后的一行 */
export interface FirewallMerged {
  ruleId: number
  direction: 'in' | 'out'
  action: 'accept' | 'drop' | 'reject'
  protocol: string
  source: string
  port: string
  service: string
  ruleKey: string
  description: string
  owner: string
  lifecycle: string
  expiresAt: string | null
  origin: string
  enabled: boolean
  state: 'pending' | 'synced' | 'drift' | 'extra'
  onHost: boolean
  hits: number
  hasCounter: boolean
  raw: string
  index: number
}

export interface FirewallState {
  hostId: number
  hostName: string
  backend: 'firewalld' | 'ufw' | 'iptables' | 'none'
  active: boolean
  persistent: boolean
  note: string
  defaultPolicy: Record<string, string>
  readOk: boolean
  readError: string
  costMs: number
  rules: FirewallMerged[]
  summary: Record<string, number>
}

export interface FirewallPlan {
  backend: string
  persistent: boolean
  adds: string[]
  removes: string[]
  commands: string[]
  skipped: string[]
}

export interface FirewallPrecheck {
  plan: FirewallPlan
  hostName: string
  verdict: 'pass' | 'warn' | 'blocked' | 'unknown' | 'nothing'
  reason: string
  hits?: { line: string; rule: string; action: string }[]
  prodHosts?: string[]
  warning?: string
}

export interface FirewallSnapshot {
  id: number
  hostId: number
  backend: string
  reason: string
  ruleCount: number
  raw?: string
  rules?: string
  execJobId: number
  operator: string
  createdAt: string
}

export interface FirewallCleanupItem {
  ruleId: number
  hostId: number
  level: 'error' | 'warning' | 'info'
  reason: string
  detail: string
}

export interface FirewallGroup {
  id: number
  name: string
  description: string
  memberHostIds: string
  enabled: boolean
  createdBy: string
  memberCount: number
  ruleCount: number
}

export const getFirewallState = (hostId: number) =>
  request<FirewallState>({ url: '/firewall/state', params: { hostId } })
export const listFirewallRules = (params: Record<string, any>) =>
  request<PageData<FirewallRule>>({ url: '/firewall/rules', params })
export const createFirewallRule = (data: Record<string, any>) =>
  request<FirewallRule>({ url: '/firewall/rules', method: 'POST', data })
export const updateFirewallRule = (id: number, data: Record<string, any>) =>
  request<FirewallRule>({ url: `/firewall/rules/${id}`, method: 'PUT', data })
export const deleteFirewallRule = (id: number) =>
  request<{ deleted: number; note: string }>({ url: `/firewall/rules/${id}`, method: 'DELETE' })
export const adoptFirewallRule = (data: Record<string, any>) =>
  request<FirewallRule>({ url: '/firewall/rules/adopt', method: 'POST', data })
export const precheckFirewall = (data: Record<string, any>) =>
  request<FirewallPrecheck>({ url: '/firewall/precheck', method: 'POST', data })
export const applyFirewall = (data: Record<string, any>) =>
  request<{
    applied: boolean
    reason?: string
    verified?: boolean
    note?: string
    plan: FirewallPlan
    remaining?: FirewallPlan
    snapshotId?: number
    job?: ExecJob
  }>({ url: '/firewall/apply', method: 'POST', data })
export const listFirewallSnapshots = (params: Record<string, any>) =>
  request<PageData<FirewallSnapshot>>({ url: '/firewall/snapshots', params })
export const getFirewallSnapshot = (id: number) =>
  request<FirewallSnapshot>({ url: `/firewall/snapshots/${id}` })
export const rollbackFirewall = (id: number, data: Record<string, any>) =>
  request<{ applied: boolean; reason?: string; plan: FirewallPlan; job?: ExecJob }>({
    url: `/firewall/snapshots/${id}/rollback`,
    method: 'POST',
    data
  })
export const getFirewallCleanup = (hostId?: number) =>
  request<{ idleDays: number; suggestions: FirewallCleanupItem[]; note: string }>({
    url: '/firewall/cleanup',
    params: hostId ? { hostId } : {}
  })
export const listFirewallGroups = () => request<FirewallGroup[]>({ url: '/firewall/groups' })
export const saveFirewallGroup = (id: number, data: Record<string, any>) =>
  id
    ? request<FirewallGroup>({ url: `/firewall/groups/${id}`, method: 'PUT', data })
    : request<FirewallGroup>({ url: '/firewall/groups', method: 'POST', data })
export const deleteFirewallGroup = (id: number) =>
  request<{ deleted: number; note: string }>({ url: `/firewall/groups/${id}`, method: 'DELETE' })
export const dispatchFirewallGroup = (id: number) =>
  request<{ hosts: number; created: number; skipped: number; note: string }>({
    url: `/firewall/groups/${id}/dispatch`,
    method: 'POST'
  })

/* ---------------- 凭证库（共享登录凭据） ---------------- */

export interface Credential {
  id: number
  name: string
  type: 'password' | 'key'
  username: string
  /** 私钥的公钥指纹（SHA256:...），口令类型为空 */
  fingerprint: string
  description: string
  owner: string
  deptId: number
  enabled: boolean
  rotatedAt: string | null
  lastUsedAt: string | null
  lastUsedHostId: number
  createdAt: string
  updatedAt: string
  /** encrypted = 库里是 AES-GCM 密文，plain = 明文（没配 OPS_SECRET_KEY 或还没轮换过） */
  storage: 'encrypted' | 'plain'
  hasPassphrase: boolean
  /** 有多少台主机在引用它 */
  hostCount: number
}

export interface CredentialState {
  encryptEnabled: boolean
  total: number
  sealed: number
  plain: number
  hostsShared: number
  hostsLocal: number
  note: string
}

export interface CredentialHostRef {
  id: number
  name: string
  address: string
  port: number
  env: string
  status: string
  checkedAt: string | null
}

export const listCredentials = (params: Record<string, any>) =>
  request<PageData<Credential>>({ url: '/credentials', params })
export const getCredentialState = () => request<CredentialState>({ url: '/credentials/state' })
export const listCredentialHosts = (id: number) =>
  request<CredentialHostRef[]>({ url: `/credentials/${id}/hosts` })
export const saveCredential = (id: number, data: Record<string, any>) =>
  id
    ? request<Credential>({ url: `/credentials/${id}`, method: 'PUT', data })
    : request<Credential>({ url: '/credentials', method: 'POST', data })
export const deleteCredential = (id: number) =>
  request<null>({ url: `/credentials/${id}`, method: 'DELETE' })
export const rotateCredential = (id: number, data: { secret: string; passphrase?: string }) =>
  request<{
    affectedHosts: number
    oldFingerprint: string
    fingerprint: string
    note: string
  }>({ url: `/credentials/${id}/rotate`, method: 'POST', data })
export const bindCredentialHosts = (id: number, hostIds: number[]) =>
  request<{ bound: number; skipped: number; skippedHosts: string[]; note: string }>({
    url: `/credentials/${id}/hosts`,
    method: 'POST',
    data: { hostIds }
  })
export const checkCredential = (id: number, hostId: number) =>
  request<{
    ok: boolean
    hostName: string
    address: string
    loginUser: string
    detail: string
    costMs: number
    viaProxy: string
    usernameMatch: boolean
    credUsername: string
  }>({ url: `/credentials/${id}/check`, method: 'POST', data: { hostId } })

/* ---------------- 容器平台授权 / 主机体检报告 ---------------- */

export interface KubeGrant {
  id: number
  subjectType: string
  subjectId: number
  subjectName: string
  clusterId: number
  clusterName: string
  namespaces: string
  kinds: string
  allowLogs: boolean
  allowWrite: boolean
  allowForward: boolean
  expiresAt: string | null
  remark: string
  operator: string
  createdAt: string
  updatedAt: string
  expired: boolean
  allNamespaces: boolean
  allKinds: boolean
}

export interface KubeGrantState {
  enforced: boolean
  grantCount: number
  clusterCount: number
  coveredClusters: number
  uncoveredClusters: number
  expiredGrants: number
  configKey: string
  adminPerm: string
  activeNote: string
  notes: string[]
}

export interface KubeGrantDiagnosis {
  userId: number
  username: string
  enforced: boolean
  isKubeAdmin: boolean
  clusters: {
    clusterId: number
    clusterName: string
    visible: boolean
    reason: string
    namespaces?: string
    kinds?: string
    allowLogs?: boolean
    allowWrite?: boolean
    allowForward?: boolean
  }[]
}

export const getKubeGrantState = () => request<KubeGrantState>({ url: '/kube/grant-state' })
export const listKubeGrants = (params: Record<string, any> = {}) =>
  request<KubeGrant[]>({ url: '/kube/grants', params })
export const saveKubeGrant = (id: number, data: Record<string, any>) =>
  id
    ? request<KubeGrant>({ url: `/kube/grants/${id}`, method: 'PUT', data })
    : request<KubeGrant>({ url: '/kube/grants', method: 'POST', data })
export const deleteKubeGrant = (id: number) =>
  request<null>({ url: `/kube/grants/${id}`, method: 'DELETE' })
export const diagnoseKubeGrant = (userId: number) =>
  request<KubeGrantDiagnosis>({ url: `/kube/grants/diagnose/${userId}` })

export interface HostReportItem {
  name?: string
  value?: string
  detail?: string
  note?: string
  warn?: boolean
  source?: string
  /** 服务 / 配置 / 日志段用的字段 */
  path?: string
  status?: string
  drift?: string
  critical?: boolean
  activeState?: string
  enableState?: string
  ports?: string
  diffLines?: number
  hitCount?: number
  category?: string
  checkedAt?: string | null
  scannedAt?: string | null
  rotated?: boolean
}

export interface HostReportSection {
  title: string
  source: string
  checkedAt: string | null
  stale: boolean
  /** 非空表示这一段没有数据，值就是原因 */
  gap: string
  summary: string
  items: HostReportItem[]
  problems: number
}

export interface HostReport {
  host: { id: number; name: string; address: string; env: string; status: string }
  generatedAt: string
  basic: HostReportItem[]
  sections: HostReportSection[]
  problemCount: number
  gapCount: number
  notCollected: { item: string; reason: string }[]
  staleAfterHrs: number
  notes: string[]
}

export const getHostReport = (id: number) => request<HostReport>({ url: `/hosts/${id}/report` })

/* ---------------- 磁盘占用分析 ---------------- */

export interface DiskEntry {
  path: string
  sizeKb: number
  /** 占本次分析根目录的比例；根目录合计为 0 时这里也是 0 */
  percent: number
}

export interface DeletedHeldFile {
  pid: string
  path: string
  sizeKb: number
}

export interface DiskAnalysis {
  host: { id: number; name: string; address: string }
  path: string
  costMs: number
  filesystem: {
    mount: string
    totalKb: number
    usedKb: number
    availKb: number
    usedText: string
    totalText: string
  }
  /** du 统计到的根目录合计 */
  rootKb: number
  rootText: string
  dirs: DiskEntry[]
  files: DiskEntry[]
  minFileMB: number
  deleted: DeletedHeldFile[]
  deletedCount: number
  deletedKb: number
  deniedDirs: number
  deniedFiles: number
  /** df 与 du 差额的解释；空串表示这次没有可比的结论 */
  gapNote: string
  notes: string[]
}

export const analyzeHostDisk = (data: { hostId: number; path?: string; minFileMB?: number }) =>
  request<DiskAnalysis>({ url: `/hosts/${data.hostId}/disk-usage`, method: 'post', data })


/* ---------------- 发件邮箱 / 出口代理 ---------------- */

export interface MailAccount {
  id: number
  name: string
  host: string
  port: number
  username: string
  from: string
  fromName: string
  /** ssl | starttls | plain */
  tlsMode: string
  skipVerify: boolean
  isDefault: boolean
  enabled: boolean
  remark: string
  lastTestAt: string | null
  /** null 表示从没试发过 */
  lastTestOk: boolean | null
  lastTestErr: string
  lastTestTo: string
  createdAt: string
  updatedAt: string
  storage: string
  hasPassword: boolean
  effectiveFrom: string
  channelCount: number
}

export interface MailState {
  total: number
  enabled: number
  tlsModes: { code: string; label: string; note: string }[]
  globalHost: string
  globalConfigured: boolean
  /** 当前实际会用哪套配置发信 */
  activeSender?: string
  activeFrom?: string
  activeSource?: string
  activeTLSMode?: string
  activeError?: string
  notes: string[]
}

export const getMailState = () => request<MailState>({ url: '/notify/mail-state' })
export const listMailAccounts = () => request<MailAccount[]>({ url: '/notify/mail-accounts' })
export const saveMailAccount = (id: number, data: Record<string, any>) =>
  id
    ? request<MailAccount>({ url: `/notify/mail-accounts/${id}`, method: 'PUT', data })
    : request<MailAccount>({ url: '/notify/mail-accounts', method: 'POST', data })
export const deleteMailAccount = (id: number) =>
  request<null>({ url: `/notify/mail-accounts/${id}`, method: 'DELETE' })
export const setDefaultMailAccount = (id: number) =>
  request<{ id: number; isDefault: boolean }>({
    url: `/notify/mail-accounts/${id}/default`,
    method: 'POST'
  })
export const testMailAccount = (id: number, to: string) =>
  request<{ ok: boolean; detail: string; costMs: number }>({
    url: `/notify/mail-accounts/${id}/test`,
    method: 'POST',
    data: { to }
  })
export const importGlobalSMTP = () =>
  request<MailAccount>({ url: '/notify/mail-accounts/import-global', method: 'POST' })

export interface EgressProxy {
  id: number
  name: string
  scheme: string
  host: string
  port: number
  username: string
  testUrl: string
  enabled: boolean
  remark: string
  lastCheckAt: string | null
  /** unknown | ok | fail */
  lastStatus: string
  lastCostMs: number
  lastError: string
  exitIp: string
  createdAt: string
  updatedAt: string
  storage: string
  hasPassword: boolean
  endpoint: string
  probeCount: number
}

export interface ProxyAttempt {
  ok: boolean
  code: number
  costMs: number
  error: string
  exitIp: string
}

export interface ProxyCheckResult {
  target: string
  direct: ProxyAttempt
  viaProxy: ProxyAttempt
  verdict: string
  status: string
  exitIPNote?: string
}

export const listEgressProxies = () =>
  request<{
    list: EgressProxy[]
    schemes: { code: string; label: string }[]
    testUrl: string
    notes: string[]
  }>({ url: '/network/proxies' })
export const saveEgressProxy = (id: number, data: Record<string, any>) =>
  id
    ? request<EgressProxy>({ url: `/network/proxies/${id}`, method: 'PUT', data })
    : request<EgressProxy>({ url: '/network/proxies', method: 'POST', data })
export const deleteEgressProxy = (id: number) =>
  request<null>({ url: `/network/proxies/${id}`, method: 'DELETE' })
export const checkEgressProxy = (id: number, testUrl?: string) =>
  request<ProxyCheckResult>({
    url: `/network/proxies/${id}/check`,
    method: 'POST',
    data: { testUrl: testUrl || '' }
  })

export interface EgressCoverage {
  module: string
  target: string
  note: string
}

export interface EgressPolicy {
  /** 是否配了统一出口 */
  configured: boolean
  proxyId: number
  proxyName: string
  endpoint: string
  /** 配了但那条代理不存在/已停用时的原因；空串表示没问题 */
  problem: string
  bypass: string[]
  /** 解析不了的 bypass 条目 */
  bypassBad: string[]
  /** 进程实际看到的代理环境变量 */
  env: Record<string, string>
  covered: EgressCoverage[]
  notCovered: EgressCoverage[]
  notes: string[]
}

export const getEgressPolicy = () => request<EgressPolicy>({ url: '/network/proxy-egress' })
export const saveEgressPolicy = (data: { proxyId: number; bypass: string }) =>
  request<{ proxyId: number; bypass: string; detail: string }>({
    url: '/network/proxy-egress',
    method: 'PUT',
    data
  })


/* ---------------- 账号密码库 / 2FA 验证码库 ---------------- */

export interface VaultAccount {
  id: number
  name: string
  category: string
  platform: string
  url: string
  username: string
  description: string
  owner: string
  deptId: number
  enabled: boolean
  rotateDays: number
  rotatedAt: string | null
  lastViewedAt: string | null
  lastViewedBy: string
  viewCount: number
  createdAt: string
  updatedAt: string
  /** encrypted | plain */
  storage: string
  hasSecret: boolean
  /** 逾期未轮换的天数，0 表示不逾期或没设周期 */
  overdueDays: number
  neverRotated: boolean
}

export interface VaultTOTPItem {
  id: number
  name: string
  issuer: string
  account: string
  accountId: number
  accountName: string
  description: string
  owner: string
  enabled: boolean
  lastViewedAt: string | null
  lastViewedBy: string
  viewCount: number
  createdAt: string
  updatedAt: string
  storage: string
}

export interface VaultAccess {
  id: number
  target: string
  targetId: number
  targetName: string
  action: string
  operator: string
  operatorId: number
  ip: string
  reason: string
  createdAt: string
}

export interface VaultState {
  encryptEnabled: boolean
  total: number
  sealed: number
  plain: number
  overdue: number
  noRotatePolicy: number
  totpTotal: number
  totpSealed: number
  access7d: number
  reveal7d: number
  categories: { code: string; label: string }[]
  notes: string[]
  lastCheckAt?: string
  lastCheckInfo?: string
}

export const getVaultState = () => request<VaultState>({ url: '/vault/state' })
export const listVaultAccounts = (params: Record<string, any>) =>
  request<PageData<VaultAccount>>({ url: '/vault/accounts', params })
export const saveVaultAccount = (id: number, data: Record<string, any>) =>
  id
    ? request<VaultAccount>({ url: `/vault/accounts/${id}`, method: 'PUT', data })
    : request<VaultAccount>({ url: '/vault/accounts', method: 'POST', data })
export const deleteVaultAccount = (id: number) =>
  request<null>({ url: `/vault/accounts/${id}`, method: 'DELETE' })
export const revealVaultAccount = (id: number, reason: string) =>
  request<{ username: string; secret: string; url: string; note: string }>({
    url: `/vault/accounts/${id}/reveal`,
    method: 'POST',
    data: { reason }
  })
export const rotateVaultAccount = (id: number, data: { secret: string; reason?: string }) =>
  request<VaultAccount>({ url: `/vault/accounts/${id}/rotate`, method: 'POST', data })
export const listVaultAccesses = (params: Record<string, any>) =>
  request<PageData<VaultAccess>>({ url: '/vault/accesses', params })

export const listVaultTOTPs = (params: Record<string, any>) =>
  request<PageData<VaultTOTPItem>>({ url: '/vault/totps', params })
export const saveVaultTOTP = (id: number, data: Record<string, any>) =>
  id
    ? request<VaultTOTPItem>({ url: `/vault/totps/${id}`, method: 'PUT', data })
    : request<VaultTOTPItem>({ url: '/vault/totps', method: 'POST', data })
export const deleteVaultTOTP = (id: number) =>
  request<null>({ url: `/vault/totps/${id}`, method: 'DELETE' })
export const codeVaultTOTP = (id: number, reason: string) =>
  request<{
    code: string
    nextCode?: string
    remainSeconds: number
    period: number
    serverTime: string
    note: string
  }>({ url: `/vault/totps/${id}/code`, method: 'POST', data: { reason } })
export const uriVaultTOTP = (id: number, reason: string) =>
  request<{ uri: string; note: string }>({
    url: `/vault/totps/${id}/uri`,
    method: 'POST',
    data: { reason }
  })

/* ---------------- 密钥加密体检 ---------------- */

export interface SecretAuditRow {
  label: string
  table: string
  column: string
  note: string
  total: number
  sealed: number
  plain: number
  /** false 表示这个字段还没纳入加密 */
  covered: boolean
  readErr: string
}

export const getSecretAudit = () =>
  request<{
    encryptEnabled: boolean
    rows: SecretAuditRow[]
    plainTotal: number
    sealedTotal: number
    note: string
    limit: string
  }>({ url: '/secrets/audit' })

export const migrateSecrets = () =>
  request<{
    results: { label: string; table: string; column: string; migrated: number; failed: number; firstError: string }[]
    migrated: number
    note: string
  }>({ url: '/secrets/migrate', method: 'POST' })

/**
 * 换密钥 / 取消加密。mode=encrypt 时 newKey 必填；mode=plain 表示把密文解回明文。
 * 平台会先做一次整库备份，成功后进程内存里的密钥立刻切换 ——
 * 调用方必须同步改部署配置里的 OPS_SECRET_KEY，否则下次重启读不出来。
 */
export const rekeySecrets = (data: { mode: 'encrypt' | 'plain'; newKey?: string; confirm: string }) =>
  request<{
    mode: string
    encryptEnabled: boolean
    results: {
      label: string
      table: string
      column: string
      rewritten: number
      skipped: number
      failed: number
      firstError: string
    }[]
    rewritten: number
    skipped: number
    failed: number
    backup: string
    note: string
  }>({ url: '/secrets/rekey', method: 'POST', data })

/* ---------------- 安全意识（培训与考核） ---------------- */

export interface AwarenessCourse {
  id: number
  title: string
  summary: string
  content: string
  scope: 'all' | 'role' | 'dept'
  scopeRoleId: number
  scopeDeptId: number
  passScore: number
  dueAt: string | null
  published: boolean
  publishedAt: string | null
  publisher: string
  targetCount: number
  doneCount: number
  lastRemindAt: string | null
  questionCount: number
  overdue: boolean
  scopeLabel: string
}

export interface AwarenessQuestion {
  id: number
  courseId: number
  content: string
  multi: boolean
  explain: string
  sort: number
  optionList: string[]
  answerList: number[]
}

export interface AwarenessRecord {
  id: number
  courseId: number
  userId: number
  username: string
  status: 'pending' | 'read' | 'passed' | 'failed'
  readAt: string | null
  attempts: number
  score: number
  passedAt: string | null
  clientIp: string
  wrong: string
}

export interface MyAwarenessItem {
  id: number
  title: string
  summary: string
  dueAt: string | null
  passScore: number
  questionCount: number
  status: 'pending' | 'read' | 'passed' | 'failed'
  score: number
  attempts: number
  overdue: boolean
}

export const listMyAwareness = () =>
  request<{ list: MyAwarenessItem[]; pending: number }>({ url: '/security/awareness/my' })
export const getMyAwarenessCourse = (id: number) =>
  request<{
    course: { id: number; title: string; summary: string; content: string; dueAt: string | null; passScore: number }
    /** 学员侧的题目不含正确答案，判分在后端做 */
    questions: { id: number; content: string; options: string[]; multi: boolean }[]
    record: AwarenessRecord
  }>({ url: `/security/awareness/my/${id}` })
export const confirmAwarenessRead = (id: number) =>
  request<{ record: AwarenessRecord; detail: string }>({
    url: `/security/awareness/my/${id}/read`,
    method: 'POST'
  })
export const submitAwarenessQuiz = (id: number, answers: Record<string, number[]>) =>
  request<{
    score: number
    passScore: number
    status: string
    correct: number
    total: number
    review: { id: number; ok: boolean; explain: string }[]
    detail: string
  }>({ url: `/security/awareness/my/${id}/quiz`, method: 'POST', data: { answers } })

export const listAwarenessCourses = () =>
  request<{ list: AwarenessCourse[]; note: string }>({ url: '/security/awareness/courses' })
export const saveAwarenessCourse = (id: number | null, data: Record<string, any>) =>
  id
    ? request<AwarenessCourse>({ url: `/security/awareness/courses/${id}`, method: 'PUT', data })
    : request<AwarenessCourse>({ url: '/security/awareness/courses', method: 'POST', data })
export const deleteAwarenessCourse = (id: number) =>
  request({ url: `/security/awareness/courses/${id}`, method: 'DELETE' })
export const publishAwarenessCourse = (id: number) =>
  request<{ course: AwarenessCourse; detail: string }>({
    url: `/security/awareness/courses/${id}/publish`,
    method: 'POST'
  })
export const unpublishAwarenessCourse = (id: number) =>
  request<{ id: number; detail: string }>({
    url: `/security/awareness/courses/${id}/unpublish`,
    method: 'POST'
  })
export const remindAwarenessCourse = (id: number) =>
  request<{ sent: number; detail: string }>({
    url: `/security/awareness/courses/${id}/remind`,
    method: 'POST'
  })
export const listAwarenessRecords = (id: number, status?: string) =>
  request<{ course: AwarenessCourse; list: AwarenessRecord[]; stats: Record<string, number> }>({
    url: `/security/awareness/courses/${id}/records`,
    params: { status }
  })
export const listAwarenessQuestions = (courseId: number) =>
  request<AwarenessQuestion[]>({ url: `/security/awareness/courses/${courseId}/questions` })
export const createAwarenessQuestion = (courseId: number, data: Record<string, any>) =>
  request<AwarenessQuestion>({
    url: `/security/awareness/courses/${courseId}/questions`,
    method: 'POST',
    data
  })
export const updateAwarenessQuestion = (id: number, data: Record<string, any>) =>
  request<AwarenessQuestion>({ url: `/security/awareness/questions/${id}`, method: 'PUT', data })
export const deleteAwarenessQuestion = (id: number) =>
  request({ url: `/security/awareness/questions/${id}`, method: 'DELETE' })

/* ---------------- 特征库 ---------------- */

export interface Signature {
  id: number
  name: string
  kind: 'command' | 'port'
  pattern: string
  description: string
  /** observe 只提醒（命令规则按 warn 应用）| enforce 真拦（按 block 应用） */
  stage: 'observe' | 'enforce'
  severity: 'high' | 'medium' | 'low'
  enabled: boolean
  builtin: boolean
  ruleId: number
  appliedAt: string | null
  appliedBy: string
  /** unapplied | applied | drift | missing | n/a（port 类不生成规则） */
  applyStatus: string
  applyDetail: string
  ruleAction: string
  ruleEnabled: boolean
}

export const listSignatures = (params?: { kind?: string; stage?: string; keyword?: string }) =>
  request<{ list: Signature[]; stats: Record<string, number>; note: string }>({
    url: '/security/signatures',
    params
  })
export const saveSignature = (id: number | null, data: Record<string, any>) =>
  id
    ? request<{ signature: Signature; warn?: string }>({
        url: `/security/signatures/${id}`,
        method: 'PUT',
        data
      })
    : request<Signature>({ url: '/security/signatures', method: 'POST', data })
export const deleteSignature = (id: number) =>
  request<{ id: number; ruleRemoved: boolean }>({ url: `/security/signatures/${id}`, method: 'DELETE' })
export const applySignature = (id: number) =>
  request<{ signature: Signature; detail: string }>({
    url: `/security/signatures/${id}/apply`,
    method: 'POST'
  })
export const revokeSignature = (id: number) =>
  request<{ signature: Signature; detail: string }>({
    url: `/security/signatures/${id}/revoke`,
    method: 'POST'
  })
export const checkPortSignature = (id: number) =>
  request<{
    ports: string
    hits: { targetId: number; name: string; address: string; hitPorts: string; lastScanAt: string; note: string }[]
    targetCount: number
    neverScanned: string[]
    detail: string
  }>({ url: `/security/signatures/${id}/port-check`, method: 'POST' })
export const reconcileSignatures = () =>
  request<{ total: number; drift: { id: number; name: string; status: string; detail: string }[]; note: string }>({
    url: '/security/signatures/reconcile'
  })

/* ---------------- 安全事件研判台 ---------------- */

export interface SecurityEvent {
  id: number
  fingerprint: string
  source: string
  sourceLabel: string
  title: string
  severity: string
  actor: string
  actorIp: string
  target: string
  port: string
  protocol: string
  evidence: string
  refTable: string
  refId: number
  eventId: number
  hitCount: number
  hitsAfterClose: number
  firstSeenAt: string
  lastSeenAt: string
  status: string
  statusLabel: string
  verdict: string
  owner: string
  closedAt: string | null
  closedBy: string
  createdAt: string
}

export interface SecurityEventLog {
  id: number
  eventId: number
  action: string
  content: string
  operator: string
  createdAt: string
}

export interface SecurityEventMute {
  id: number
  fingerprint: string
  source: string
  sourceLabel: string
  title: string
  reason: string
  hitCount: number
  lastHitAt: string | null
  operator: string
  createdAt: string
}

export interface SecurityEventStats {
  total: number
  new: number
  investigating: number
  confirmed: number
  handled: number
  falsePositive: number
  ignored: number
  critical: number
  rehit: number
  muteCount: number
  muteHits: number
  bySource: { source: string; label: string; total: number; open: number; cursor: number; table: string }[]
}

export interface SecurityEventDetail {
  event: SecurityEvent
  logs: SecurityEventLog[]
  evidence: string
  related: SecurityEvent[]
  muted: boolean
  muteHits: number
  notes: string[]
}

export const listSecurityEvents = (params: Record<string, any>) =>
  request<PageData<SecurityEvent>>({ url: '/security/events', params })
export const getSecurityEventStats = () =>
  request<SecurityEventStats>({ url: '/security/events/stats' })
export const getSecurityEvent = (id: number) =>
  request<SecurityEventDetail>({ url: `/security/events/${id}` })
export const collectSecurityEvents = () =>
  request<{ created: number; updated: number; rehit: number; muted: number }>({
    url: '/security/events/collect',
    method: 'POST'
  })
export const triageSecurityEvents = (data: {
  ids: number[]
  status: string
  verdict?: string
  owner?: string
  mute?: boolean
}) => request<{ changed: number; muted: number }>({ url: '/security/events/triage', method: 'POST', data })
export const addSecurityEventNote = (id: number, content: string) =>
  request({ url: `/security/events/${id}/note`, method: 'POST', data: { content } })
export const blockSecurityEventSource = (
  id: number,
  data: { hostId?: number; groupId?: number; port?: string }
) => request<{ rule: any; message: string }>({ url: `/security/events/${id}/block`, method: 'POST', data })
export const listSecurityMutes = (params?: Record<string, any>) =>
  request<PageData<SecurityEventMute>>({ url: '/security/event-mutes', params })
export const deleteSecurityMute = (id: number) =>
  request<{ revoked: string; blockedHits: number }>({ url: `/security/event-mutes/${id}`, method: 'DELETE' })

/* ---------------- 安全响应：原始数据 / 升格工单 / 学习建议 / 概览 ---------------- */

export interface SecRawField {
  label: string
  value: string
}

export interface SecRawResult {
  available: boolean
  reason?: string
  table?: string
  refId?: number
  fields?: SecRawField[]
  extra?: Record<string, any>
  evidence?: string
  note?: string
}

export interface SecSuggestion {
  key: string
  kind: 'port_baseline' | 'mute_fingerprint' | 'signature_enforce'
  kindLabel: string
  title: string
  reason: string
  action: string
  hitCount: number
  refId: number
  refName: string
  extra: string
}

export interface SecSuggestionList {
  items: SecSuggestion[]
  byKind: Record<string, number>
  dismissed: number
  minHits: number
  note: string
}

export interface SecSuggestionDismissal {
  id: number
  key: string
  kind: string
  title: string
  reason: string
  operator: string
  createdAt: string
}

export interface SecOverview {
  events: Record<string, number>
  intercept: Record<string, number>
  exposure: Record<string, number>
  certs: Record<string, number>
  domains: Record<string, number>
  signatures: Record<string, number>
  firewall: Record<string, number>
  twoFactor: { users: number; enabled: number }
  hostLogs: Record<string, number>
  suggestions: number
  trend: { day: string; total: number }[]
  gaps: { item: string; why: string }[]
  note: string
}

export const getSecurityEventRaw = (id: number) =>
  request<SecRawResult>({ url: `/security/events/${id}/raw` })
export const escalateSecurityEvent = (
  id: number,
  data: { assignee?: string; title?: string; summary?: string }
) => request<{ eventId: number; status: string; note: string }>({
  url: `/security/events/${id}/escalate`,
  method: 'POST',
  data
})
export const securityOverview = () => request<SecOverview>({ url: '/security/overview' })
export const listSecuritySuggestions = () =>
  request<SecSuggestionList>({ url: '/security/suggestions' })
export const applySecuritySuggestions = (data: {
  keys: string[]
  decision: 'approve' | 'dismiss'
  reason?: string
}) => request<{
  done: { key: string; title: string; result: string }[]
  failed: { key: string; error: string }[]
}>({ url: '/security/suggestions/apply', method: 'POST', data })
export const listSuggestionDismissals = () =>
  request<{ rows: SecSuggestionDismissal[]; note: string }>({
    url: '/security/suggestion-dismissals'
  })
export const deleteSuggestionDismissal = (id: number) =>
  request({ url: `/security/suggestion-dismissals/${id}`, method: 'DELETE' })

/* ---------------- 告警规则版本 / 通知模板 / 值班大屏 ---------------- */

export interface RuleVersion {
  id: number
  target: string
  targetId: number
  targetName: string
  version: number
  source: 'created' | 'edited' | 'rollback' | 'deleted' | 'restored'
  hash: string
  note: string
  operator: string
  createdAt: string
  targetAlive: boolean
}

export interface RuleDiffItem {
  key: string
  label: string
  before: string
  after: string
  changed: boolean
}

export interface NotifyTemplate {
  id: number
  code: string
  name: string
  kind: 'im' | 'webhook'
  scene: 'alert' | 'oncall'
  body: string
  builtin: boolean
  enabled: boolean
  remark: string
  createdAt: string
}

export interface NotifyVarSpec {
  key: string
  label: string
  sample: string
}

export interface NotifyScene {
  code: string
  label: string
  note: string
  vars: NotifyVarSpec[]
}

export interface WallboardData {
  at: string
  firing: Record<string, number>
  events: Record<string, any>
  hosts: Record<string, number>
  probes: Record<string, number>
  certs: Record<string, number>
  domains: Record<string, number>
  hostLogs: Record<string, number>
  security: Record<string, number>
  notify: Record<string, number>
  trend: { hour: string; total: number }[]
  onCall: { id: number; name: string; current?: string; levels?: string[]; note?: string }[]
  notes: string[]
}

/** 支持版本化的规则类型。后端 rule_version.go 里的注册表决定有哪些 */
export type RuleVersionTarget = 'alert_rule' | 'detection_rule' | 'aggregation_policy'

export interface RuleVersionTargetSpec {
  target: RuleVersionTarget
  label: string
  fields: { key: string; label: string }[]
  runtimeNote: string
}

export const listRuleVersionTargets = () =>
  request<RuleVersionTargetSpec[]>({ url: '/monitor/rule-version-targets' })
export const listRuleVersions = (target: RuleVersionTarget, params?: Record<string, any>) =>
  request<{
    target: string
    label: string
    fields: { key: string; label: string }[]
    versions: RuleVersion[]
    limit: number
    notes: string[]
  }>({ url: '/monitor/rule-versions', params: { ...params, target } })
export const getRuleVersion = (id: number) =>
  request<{
    version: RuleVersion
    label: string
    fields: { key: string; label: string; value: string }[]
  }>({ url: `/monitor/rule-versions/${id}` })
export const diffRuleVersions = (from: number, to: number) =>
  request<{
    target: string
    label: string
    left: Record<string, any>
    right: Record<string, any>
    same: boolean
    changed: number
    items: RuleDiffItem[]
  }>({ url: '/monitor/rule-versions/diff', params: { from, to } })
export const rollbackRule = (
  target: RuleVersionTarget,
  ruleId: number,
  versionId: number,
  note?: string
) =>
  request<{ changed: boolean; note: string }>({
    url: '/monitor/rule-versions/rollback',
    method: 'POST',
    data: { target, ruleId, versionId, note }
  })
export const restoreRuleVersion = (id: number) =>
  request<{ target: string; id: number; name: string; rule: Record<string, any>; note: string }>({
    url: `/monitor/rule-versions/${id}/restore`,
    method: 'POST'
  })

export const listNotifyTemplateVars = () =>
  request<{
    scenes: NotifyScene[]
    kinds: { code: string; label: string; note: string }[]
    notes: string[]
  }>({ url: '/notify/template-vars' })
export const listNotifyTemplates = (params?: Record<string, any>) =>
  request<NotifyTemplate[]>({ url: '/notify/templates', params })
export const createNotifyTemplate = (data: Record<string, any>) =>
  request<NotifyTemplate>({ url: '/notify/templates', method: 'POST', data })
export const updateNotifyTemplate = (id: number, data: Record<string, any>) =>
  request<NotifyTemplate>({ url: `/notify/templates/${id}`, method: 'PUT', data })
export const deleteNotifyTemplate = (id: number) =>
  request({ url: `/notify/templates/${id}`, method: 'DELETE' })
export const previewNotifyTemplate = (id: number, vars?: Record<string, string>) =>
  request<{
    rendered: string
    vars: Record<string, string>
    kind: string
    scene: string
    validJSON?: boolean
  }>({ url: `/notify/templates/${id}/preview`, method: 'POST', data: { vars: vars || {} } })

export const getWallboard = () => request<WallboardData>({ url: '/monitor/wallboard' })

/* ---------------- 云资源同步 ---------------- */

export interface CloudResource {
  id: number
  cloudAccountId: number
  provider: string
  resourceType: string
  resourceId: string
  name: string
  regionId: string
  zoneId: string
  status: string
  gone: boolean
  privateIps: string
  publicIps: string
  spec: string
  osName: string
  chargeType: string
  expiredAt: string | null
  matchedHostId: number
  matchBy: string
  extra: string
  firstSeenAt: string
  lastSyncAt: string
}

export interface CloudSyncRun {
  id: number
  cloudAccountId: number
  accountName: string
  provider: string
  resourceType: string
  regionId: string
  status: string
  trigger: string
  operator: string
  totalCount: number
  createdCount: number
  updatedCount: number
  goneCount: number
  matchedCount: number
  message: string
  startedAt: string
  finishedAt: string | null
  durationMs: number
}

export interface CloudDriftItem {
  side: 'cloud_only' | 'host_only' | 'gone'
  resourceId: string
  name: string
  address: string
  status: string
  provider: string
  cloudResId: number
  hostId: number
  lastSyncAt: string
}

export interface CloudDrift {
  syncedOnce: boolean
  cloudOnly: number
  hostOnly: number
  gone: number
  items: CloudDriftItem[]
  note: string
}

export const runCloudSync = (id: number, resourceType?: string) =>
  request<CloudSyncRun[]>({ url: `/cloud-accounts/${id}/sync`, method: 'POST', data: { resourceType } })
export const listCloudResources = (params?: Record<string, any>) =>
  request<PageData<CloudResource>>({ url: '/cloud-resources', params })
export const cloudResourceStats = () =>
  request<{ resourceType: string; status: string; gone: boolean; count: number }[]>({
    url: '/cloud-resources/stats'
  })
export const cloudDrift = () => request<CloudDrift>({ url: '/cloud-resources/drift' })
export const adoptCloudResource = (id: number, data: Record<string, any>) =>
  request<Host>({ url: `/cloud-resources/${id}/adopt`, method: 'POST', data })
export const listCloudSyncRuns = (params?: Record<string, any>) =>
  request<PageData<CloudSyncRun>>({ url: '/cloud-sync-runs', params })

/* ---------------- 域名管理 ---------------- */

export interface DomainRecord {
  id: number
  name: string
  registrar: string
  registeredAt: string | null
  expiresAt: string | null
  autoRenew: boolean
  owner: string
  purpose: string
  deptId: number
  createdBy: number
  expectIps: string
  expectCname: string
  expectNs: string
  resolvedIps: string
  resolvedCname: string
  resolvedNs: string
  dnsStatus: 'unknown' | 'ok' | 'drift' | 'unresolved' | 'nocheck' | 'error'
  dnsDetail: string
  dnsServer: string
  lastCheckAt: string | null
  daysLeft: number
  expireStatus: 'unknown' | 'valid' | 'expiring' | 'expired'
  alertDays: number
  alertEnabled: boolean
  certCount: number
  certNames: string
  certMinDaysLeft: number
  source: 'manual' | 'cloud'
  cloudResourceId: number
  enabled: boolean
  remark: string
  createdAt: string
  updatedAt: string
}

export interface DomainStats {
  total: number
  dnsOk: number
  dnsDrift: number
  dnsUnresolve: number
  dnsNoCheck: number
  dnsUnknown: number
  expiring: number
  expired: number
  noExpiry: number
  note: string
}

export interface DomainCheckSummary {
  checked: number
  dnsOk: number
  dnsDrift: number
  dnsBad: number
  dnsNoCheck: number
  expiring: number
  expired: number
  noExpiry: number
}

export const listDomains = (params?: Record<string, any>) =>
  request<PageData<DomainRecord>>({ url: '/domains', params })
export const domainStats = () => request<DomainStats>({ url: '/domains/stats' })
export const createDomain = (data: Record<string, any>) =>
  request<DomainRecord>({ url: '/domains', method: 'POST', data })
export const updateDomain = (id: number, data: Record<string, any>) =>
  request<DomainRecord>({ url: `/domains/${id}`, method: 'PUT', data })
export const deleteDomain = (id: number) => request({ url: `/domains/${id}`, method: 'DELETE' })
export const checkDomain = (id: number) =>
  request<DomainRecord>({ url: `/domains/${id}/check`, method: 'POST' })
export const checkAllDomains = () =>
  request<DomainCheckSummary>({ url: '/domains/check-all', method: 'POST' })
export const importCloudDomains = () =>
  request<{ created: string[]; skipped: string[]; note: string }>({
    url: '/domains/import-cloud',
    method: 'POST'
  })

/* ---------------- 主机日志（SSH 直读，不依赖 Loki） ---------------- */

export interface HostLogTarget {
  id: number
  name: string
  hostId: number
  hostName: string
  path: string
  keywords: string
  ignoreKeywords: string
  maxBytes: number
  alertEnabled: boolean
  lastOffset: number
  lastInode: string
  lastSize: number
  lastStatus: 'unknown' | 'ok' | 'hit' | 'missing' | 'denied' | 'failed'
  lastHitCount: number
  lastSample: string
  lastRotated: boolean
  lastCostMs: number
  lastError: string
  lastScanAt: string | null
  totalScans: number
  deptId: number
  enabled: boolean
  remark: string
}

export interface HostLogScan {
  id: number
  targetId: number
  hostId: number
  status: string
  hitCount: number
  newBytes: number
  fileSize: number
  rotated: boolean
  sample: string
  costMs: number
  errorMsg: string
  operator: string
  createdAt: string
}

export interface HostLogTopEntry {
  path: string
  sizeKb: number
}

export interface HostLogUsageRow {
  id: number
  hostId: number
  hostName: string
  dir: string
  totalKb: number
  topIsDir: boolean
  status: string
  errorMsg: string
  costMs: number
  createdAt: string
  top: HostLogTopEntry[]
}

export interface HostLogMeta {
  pathPrefixes: string[]
  usageDir: string
  viewMaxBytes: number
  viewMaxLines: number
  scanMaxBytes: number
  note: string
}

export interface HostLogViewResult {
  hostId: number
  hostName: string
  path: string
  mode: 'tail' | 'grep'
  lines: number
  rows: string[]
  fileSize: number
  truncated: boolean
  costMs: number
  precheck: string
  precheckHits: { pattern: string; action: string; description: string }[]
  note: string
}

export interface HostLogScanSummary {
  checked: number
  ok: number
  hit: number
  hitLines: number
  missing: number
  denied: number
  failed: number
}

export const hostLogMeta = () => request<HostLogMeta>({ url: '/monitor/host-logs/meta' })
export const viewHostLog = (data: {
  hostId: number
  path: string
  lines?: number
  keyword?: string
  ignoreCase?: boolean
}) => request<HostLogViewResult>({ url: '/monitor/host-logs/view', method: 'POST', data })
export const listHostLogTargets = (params?: Record<string, any>) =>
  request<PageData<HostLogTarget>>({ url: '/monitor/host-log-targets', params })
export const createHostLogTarget = (data: Record<string, any>) =>
  request<HostLogTarget>({ url: '/monitor/host-log-targets', method: 'POST', data })
export const updateHostLogTarget = (id: number, data: Record<string, any>) =>
  request<HostLogTarget>({ url: `/monitor/host-log-targets/${id}`, method: 'PUT', data })
export const deleteHostLogTarget = (id: number) =>
  request({ url: `/monitor/host-log-targets/${id}`, method: 'DELETE' })
export const scanHostLogTarget = (id: number) =>
  request<HostLogTarget>({ url: `/monitor/host-log-targets/${id}/scan`, method: 'POST' })
export const scanAllHostLogTargets = () =>
  request<HostLogScanSummary>({ url: '/monitor/host-log-targets/scan-all', method: 'POST' })
export const listHostLogScans = (params?: Record<string, any>) =>
  request<PageData<HostLogScan>>({ url: '/monitor/host-log-scans', params })
export const listLogUsage = () =>
  request<{ dir: string; rows: HostLogUsageRow[]; note: string }>({ url: '/monitor/log-usage' })
export const collectLogUsage = () =>
  request<{ dir: string; ok: number; failed: number }>({
    url: '/monitor/log-usage/collect',
    method: 'POST'
  })










