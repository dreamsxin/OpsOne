<script setup lang="ts">
/**
 * 页面统一头部：左侧标题 + 副标题，右侧动作槽。
 * 面包屑仍由 BasicLayout 顶栏承担，页面内不再重复。
 */
defineProps<{
  title: string
  subtitle?: string
  /** 头部下方是否留白，默认 true；紧凑模式（如抽屉内嵌）可关掉 */
  spaced?: boolean
}>()
</script>

<template>
  <div class="page-header" :class="{ 'page-header--tight': spaced === false }">
    <div class="page-header__main">
      <h2 class="page-header__title">{{ title }}</h2>
      <p v-if="subtitle" class="page-header__subtitle">{{ subtitle }}</p>
    </div>
    <div class="page-header__actions">
      <slot name="actions" />
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 12px;
}
.page-header--tight {
  margin-bottom: 8px;
}
.page-header__main {
  flex: 1;
  min-width: 0;
}
.page-header__title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--ops-text-primary, #111827);
}
.page-header__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--ops-text-secondary, #6b7280);
  line-height: 1.5;
}
.page-header__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
</style>
