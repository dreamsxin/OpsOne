import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'
import zhCn from 'element-plus/dist/locale/zh-cn.mjs'

import 'element-plus/dist/index.css'
import '@xterm/xterm/css/xterm.css'
import '@/styles/index.scss'

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
