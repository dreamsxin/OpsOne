import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import zhCn from 'element-plus/dist/locale/zh-cn.mjs'

import 'element-plus/dist/index.css'
import '@xterm/xterm/css/xterm.css'
import '@/styles/index.scss'

// 必须排在 router 之前：该模块在被 import 时就会把 IM 扫码登录的一次性 ticket
// 从地址栏摘走存起来，晚于 createWebHistory() 的话 router 会记住带 ticket 的脏 URL。
import './utils/imLogin'
import App from './App.vue'
import router from './router'
import { setupDirectives } from './directives/perm'

const app = createApp(App)


for (const [name, comp] of Object.entries(ElementPlusIconsVue)) {
  app.component(name, comp)
}

app.use(createPinia())
app.use(ElementPlus, { locale: zhCn })
setupDirectives(app)
app.use(router)
app.mount('#app')
