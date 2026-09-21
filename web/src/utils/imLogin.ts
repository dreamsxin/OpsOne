/**
 * IM 扫码登录的落地参数处理。
 *
 * 后端回调会把浏览器 302 到「登录成功跳转地址」并带上一次性 ticket，
 * 但路由守卫会把未登录的人重定向到 /login 并丢掉原来的查询串 ——
 * 所以要在 router 启动之前就把 ticket 取下来存进 sessionStorage，
 * 再由登录页消费。存 sessionStorage 而不是 localStorage：关掉标签页就没了。
 *
 * 注意：抓取动作写成「模块副作用」而不是导出函数让 main.ts 调用。
 * vue-router 的 createWebHistory() 在 router 模块求值时就把当前 URL 记了下来，
 * 而 ESM 的 import 在 main.ts 的语句之前执行 —— 只要本模块排在 router 之前被
 * import，副作用就一定先跑，router 记到的就是已经洗干净的地址。
 */
const TICKET_KEY = 'ops-im-ticket'
const ERROR_KEY = 'ops-im-error'


/** 从 search 与 hash 两处取参数：既支持 history 模式也支持 hash 模式 */
function readParam(name: string): string {
  const fromSearch = new URLSearchParams(window.location.search).get(name)
  if (fromSearch) return fromSearch
  const hash = window.location.hash
  if (hash.includes('?')) {
    return new URLSearchParams(hash.slice(hash.indexOf('?') + 1)).get(name) || ''
  }
  return ''
}

/** 模块被 import 时立刻执行：把 ticket / 错误取下来并从地址栏抹掉 */
function captureImLoginResult() {
  const ticket = readParam('imTicket')
  const error = readParam('imError')
  if (!ticket && !error) return
  if (ticket) sessionStorage.setItem(TICKET_KEY, ticket)
  if (error) sessionStorage.setItem(ERROR_KEY, error)
  // ticket 是一次性的，别留在地址栏里被刷新或分享出去
  const clean = window.location.pathname + (window.location.hash.split('?')[0] || '')
  window.history.replaceState({}, '', clean || '/')
}

captureImLoginResult()


/** 登录页消费：取出即清除 */
export function takeImLoginResult(): { ticket: string; error: string } {
  const ticket = sessionStorage.getItem(TICKET_KEY) || ''
  const error = sessionStorage.getItem(ERROR_KEY) || ''
  sessionStorage.removeItem(TICKET_KEY)
  sessionStorage.removeItem(ERROR_KEY)
  return { ticket, error }
}
