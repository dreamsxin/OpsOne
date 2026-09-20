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
}


export interface SessionCommand {
  id: number
  sessionId: number
  command: string
  risk: 'normal' | 'warn' | 'blocked'
  ruleId: number
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
  type: 'webhook' | 'email' | 'silent'
  url: string
  headerKey: string
  recipients: string
  templateCode: string
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
export const runCronJobNow = (id: number) =>
  request<ExecJob>({ url: `/scheduler/jobs/${id}/run`, method: 'POST' })

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

// ---------- 事件中心 ----------

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
  createdAt: string
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
}

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
  request<{ status: string; resolvedAlerts: number }>({
    url: `/monitor/events/${id}/status`,
    method: 'POST',
    data
  })
export const deleteEvent = (id: number) =>
  request({ url: `/monitor/events/${id}`, method: 'DELETE' })

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
  data: { hostIds: number[]; params?: Record<string, string>; timeout?: number }
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
  containerMsg: string
  createdAt: string
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
export const listKubeEvents = (id: number, namespace?: string, onlyWarning?: boolean) =>
  request<{ items: KubeEvent[]; total: number }>({
    url: `/kube/clusters/${id}/events`,
    params: { ...(namespace ? { namespace } : {}), ...(onlyWarning ? { onlyWarning: 'true' } : {}) }
  })

// ---------- 主机指标 ----------

export interface HostMetric {
  id: number
  hostId: number
  cpuPercent: number
  memPercent: number
  swapPercent: number
  diskMaxPercent: number
  diskMaxMount: string
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







