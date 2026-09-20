import type { App, Directive } from 'vue'
import { useUserStore } from '@/stores/user'

// v-perm="'host:create'" 无权限时直接移除元素，按钮级权限控制
const perm: Directive<HTMLElement, string> = {
  mounted(el, binding) {
    const store = useUserStore()
    if (binding.value && !store.has(binding.value)) {
      el.remove()
    }
  }
}

export function setupDirectives(app: App) {
  app.directive('perm', perm)
}
