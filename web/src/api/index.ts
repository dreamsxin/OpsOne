import { request, TOKEN_KEY, type PageData } from './request'


// ---------- 类型 ----------

export interface LoginResult {
  token: string
  expiresAt: number
  user: { id: number; username: string; nickname: string }
}

export interface Profile {
  id: number
  username: string
  nickname: string
  email: string
  lastLoginAt: string | null
  roles: { id: number; code: string; name: string }[]
  permissions: string[]
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
  type: 'webhook' | 'silent'
  url: string
  headerKey: string
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
  menus?: { id: number }[]
}

export interface User {
  id: number
  username: string
  nickname: string
  email: string
  status: number
  lastLoginAt: string | null
  roles?: Role[]
}

export interface AuditLog {
  id: number
  username: string
  method: string
  path: string
  status: number
  ip: string
  costMs: number
  createdAt: string
}

export interface MenuTreeNode {
  id: number
  parentId: number
  title: string
  path: string
  type: 'menu' | 'button'
  authCode: string
  children?: MenuTreeNode[]
}

// ---------- 接口 ----------

export const login = (username: string, password: string) =>
  request<LoginResult>({ url: '/auth/login', method: 'POST', data: { username, password } })

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
export const listAuditLogs = (params: Record<string, any>) =>
  request<PageData<AuditLog>>({ url: '/system/audit-logs', params })

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


