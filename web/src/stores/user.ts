import { defineStore } from 'pinia'
import { getMyMenus, getProfile, login as loginApi, type MenuNode, type Profile } from '@/api'
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
    displayName: (state) => state.profile?.nickname || state.profile?.username || ''
  },
  actions: {
    async login(username: string, password: string) {
      const res = await loginApi(username, password)
      this.token = res.token
      localStorage.setItem(TOKEN_KEY, res.token)
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
