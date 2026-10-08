<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getSiteLink, type SiteLink } from '@/api'
import { useTabsStore } from '@/stores/tabs'

// keep-alive 的 include 按组件名匹配页签缓存，必须与路由名一致
defineOptions({ name: 'SiteLinkEmbed' })

/**
 * 站点导航的内嵌打开页：把外部系统以 iframe 呈现在平台布局里。
 *
 * 对方站设了 X-Frame-Options / CSP frame-ancestors 时 iframe 会是空白，
 * 这里给一句明说原因的提示和「新窗口打开」的退路，而不是让人对着白屏猜。
 */
const route = useRoute()
const tabs = useTabsStore()
const link = ref<SiteLink | null>(null)
const loading = ref(true)
const errorMsg = ref('')

function openNew() {
  if (link.value) window.open(link.value.url, '_blank', 'noopener,noreferrer')
}

onMounted(async () => {
  try {
    link.value = await getSiteLink(Number(route.params.id))
    if (link.value && !link.value.enabled) {
      errorMsg.value = '该导航项已被停用'
      link.value = null
    }
  } catch (err: any) {
    errorMsg.value = err?.message || '导航项不存在'
  } finally {
    loading.value = false
  }
  // 页签标题用站点名，多个内嵌页签才分得清
  if (link.value) {
    const tab = tabs.tabs.find((t) => t.path === route.path)
    if (tab) tab.title = link.value.name
  }
})
</script>

<template>
  <div class="embed-page">
    <div class="embed-bar">
      <strong>{{ link?.name || '外部系统' }}</strong>
      <span class="embed-url">{{ link?.url }}</span>
      <div class="grow"></div>
      <span class="embed-hint">若下方空白，说明对方站点禁止被内嵌，请用新窗口打开</span>
      <el-button v-if="link" size="small" @click="openNew">新窗口打开</el-button>
    </div>

    <div v-if="loading" v-loading="true" class="embed-loading"></div>
    <el-empty v-else-if="errorMsg" :description="errorMsg" />
    <iframe
      v-else-if="link"
      :src="link.url"
      class="embed-frame"
      referrerpolicy="no-referrer"
      :title="link.name"
    ></iframe>
  </div>
</template>

<style scoped>
.embed-page {
  height: 100%;
  display: flex;
  flex-direction: column;
  background: #fff;
}
.embed-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--ops-border, #e5e7eb);
  flex-shrink: 0;
}
.embed-url {
  color: var(--ops-text-muted, #9ca3af);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.embed-hint {
  color: var(--ops-text-muted, #9ca3af);
  font-size: 12px;
}
.grow {
  flex: 1;
}
.embed-loading {
  flex: 1;
}
.embed-frame {
  flex: 1;
  width: 100%;
  border: 0;
  background: #fff;
}
</style>
