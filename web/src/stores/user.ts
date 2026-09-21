import { defineStore } from 'pinia'
import {
  getMyMenus,
  getProfile,
  imLoginExchange,
  login as loginApi,
  type MenuNode,
  type Profile
} from '@/api'
import { TOKEN_KEY } from '@/api/request'

export const useUserStore = defineStore('user', {
  state: () => ({
    token: localStorage.getItem(TOKEN_KEY) || '',
    profile: null as Profile | null,
    menus: [] as MenuNode[],
    permissions: [] as string[],
    routesReady: false
  }),
  getters: {
    isLogin: (state) => !!state.token,
    displayName: (state) => state.profile?.nickname || state.profile?.username || '',
    /** 平台强制双因子但当前账号还没绑定：此时后端只放行 /me 系接口 */
    needBindTotp: (state) => !!state.profile?.totpEnforced && !state.profile?.totpEnabled
  },
  actions: {
    /**
     * 登录。口令正确但账号开了双因子时，后端返回 totpRequired，
     * 此处不写 token，交给页面补验证码后再调一次。
     */
    async login(username: string, password: string, code?: string) {
      const res = await loginApi(username, password, code)
      if (res.totpRequired) {
        return res
      }
      this.token = res.token
      localStorage.setItem(TOKEN_KEY, res.token)
      return res
    },
    /** IM 扫码登录：用回调带回来的一次性 ticket 换令牌，之后的流程与口令登录一致 */
    async loginByImTicket(ticket: string) {
      const res = await imLoginExchange(ticket)
      this.token = res.token
      localStorage.setItem(TOKEN_KEY, res.token)
      return res
    },
    async loadProfile() {
      const profile = await getProfile()
      this.profile = profile
      this.permissions = profile.permissions || []
      this.menus = await getMyMenus()
    },
    has(code: string) {
      return this.permissions.includes(code)
    },
    logout() {
      this.token = ''
      this.profile = null
      this.menus = []
      this.permissions = []
      this.routesReady = false
      localStorage.removeItem(TOKEN_KEY)
    }
  }
})
