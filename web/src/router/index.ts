import {
  createRouter,
  createWebHistory,
  type RouteRecordRaw,
  type Router
} from 'vue-router'
import { useUserStore } from '@/stores/user'
import type { MenuNode } from '@/api'

// 所有业务页面按约定放在 views 下，后端菜单的 component 字段与此路径对应
// glob 的 key 形态在不同 Vite 版本下可能是相对路径或 /src 开头，统一归一化成 /dashboard/index 这种后缀
const viewLoaders: Record<string, () => Promise<unknown>> = {}
for (const [key, loader] of Object.entries(import.meta.glob('../views/**/*.vue'))) {
  const matched = key.match(/views(\/.*)\.vue$/)
  if (matched) {
    viewLoaders[matched[1]] = loader as () => Promise<unknown>
  }
}


const staticRoutes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/login/index.vue'),
    meta: { title: '登录', public: true }
  },
  {
    path: '/',
    name: 'Root',
    component: () => import('@/layouts/BasicLayout.vue'),
    redirect: '/dashboard/overview',

    children: [
      {
        // 双因子绑定是自助页面，不能只靠菜单授权：强制模式下没有该菜单的用户
        // 也必须能进来完成绑定。name 与内置菜单 503 的 Name 一致，
        // 动态注册时会跳过，避免同一路径注册两次
        path: '/security/twofa',
        name: 'TwoFA',
        component: () => import('@/views/security/twofa/index.vue'),
        meta: { title: '双因子口令' }
      }
    ]
  },
  {
    path: '/403',
    name: 'Forbidden',
    component: () => import('@/views/error/403.vue'),
    meta: { title: '无权访问' }
  },
  {
    // 不能标记为 public：否则未登录访问业务路径时会先命中这里并直接渲染 404，
    // 而不是跳转登录页
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/views/error/404.vue'),
    meta: { title: '页面不存在' }
  }

]

export const router: Router = createRouter({
  history: createWebHistory(),
  routes: staticRoutes
})

function resolveComponent(component: string) {
  return viewLoaders[component]
}

/**
 * 给每个路由的组件注入独立名字：keep-alive 的 include 按组件名匹配，
 * 而业务页面文件清一色叫 index.vue（推断名都是 "index"），同名会导致
 * 缓存与页签对不上。这里在加载后浅拷贝一份并改名为路由名，
 * 同一个组件被多个菜单复用（如占位页）时也能各自缓存、各自释放。
 */
function namedLoader(loader: () => Promise<unknown>, name: string) {
  return async () => {
    const mod = (await loader()) as { default?: Record<string, unknown> }
    const comp = mod.default || mod
    return Object.assign({}, comp, { name })
  }
}


/** 把后端菜单树拍平成路由，挂到布局容器下 */
function toRoutes(menus: MenuNode[]): RouteRecordRaw[] {
  const routes: RouteRecordRaw[] = []
  for (const menu of menus) {
    if (menu.component) {
      const loader = resolveComponent(menu.component)
      if (!loader) {
        console.warn(`[router] 菜单 ${menu.title} 指向的组件不存在: ${menu.component}`)
      } else {
        routes.push({
          path: menu.path,
          name: menu.name || menu.path,
          component: namedLoader(loader, menu.name || menu.path) as never,
          meta: { title: menu.title, icon: menu.icon, hidden: menu.hidden }
        })
      }
    }
    if (menu.children?.length) {
      routes.push(...toRoutes(menu.children))
    }
  }
  return routes
}

export function registerDynamicRoutes(menus: MenuNode[]) {
  for (const route of toRoutes(menus)) {
    if (!router.hasRoute(route.name!)) {
      router.addRoute('Root', route)
    }
  }
}

router.beforeEach(async (to) => {
  const store = useUserStore()

  // 动态路由未注册时必须先注册再放行：否则任何业务路径都会先命中兜底的 404 路由
  if (store.isLogin && !store.routesReady) {
    try {
      await store.loadProfile()
      registerDynamicRoutes(store.menus)
      store.routesReady = true
      // 只带 path/query/hash 重新解析，避免把已匹配到 404 的 name 一起透传
      return { path: to.path, query: to.query, hash: to.hash, replace: true }
    } catch {
      store.logout()
      return { path: '/login' }
    }
  }

  if (to.meta.public) {
    return true
  }
  if (!store.isLogin) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  // 强制双因子且未绑定：后端已经拦住了业务接口，这里只是把人引到绑定页
  if (store.needBindTotp && to.path !== '/security/twofa') {
    return { path: '/security/twofa' }
  }
  return true
})


export default router
