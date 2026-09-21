<script lang="ts">
/**
 * 列表页顶部快筛 chips：一行分类计数按钮，点击即应用筛选。
 *
 * 参考站 /monitor/alerts 的模式：每个 chip 显示 [名称 计数 · 说明]，
 * 相比 4 张大 stat-card 更节省纵向空间，也更能表达「点了会发生什么」。
 *
 * items[].key 由业务侧决定含义（severity=critical / status=firing / custom:timeout …），
 * 组件本身不做语义假设，只负责呈现与选中态。
 */
export type ChipItem = {
  key: string
  label: string
  count?: number | string
  hint?: string
  /** info / success / warning / danger，用于给 chip 上色 */
  tone?: 'info' | 'success' | 'warning' | 'danger' | 'primary'
  /** 纯展示不可点击的 chip（例如「当前查询 N · 本页 M 条」） */
  static?: boolean
}
</script>

<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    items: ChipItem[]
    /** 支持多选时传入 keys 数组；单选传字符串 */
    modelValue?: string | string[] | null
    multi?: boolean
  }>(),
  { multi: false, modelValue: null }
)

const emit = defineEmits<{
  (e: 'update:modelValue', v: string | string[] | null): void
  (e: 'select', key: string, item: ChipItem): void
}>()

function isActive(key: string): boolean {
  const v = props.modelValue
  if (Array.isArray(v)) return v.includes(key)
  return v === key
}

function toggle(item: ChipItem) {
  if (item.static) return
  const key = item.key
  if (props.multi) {
    const cur = Array.isArray(props.modelValue) ? [...props.modelValue] : []
    const idx = cur.indexOf(key)
    if (idx >= 0) cur.splice(idx, 1)
    else cur.push(key)
    emit('update:modelValue', cur)
  } else {
    emit('update:modelValue', isActive(key) ? null : key)
  }
  emit('select', key, item)
}
</script>

<template>
  <div class="filter-chips">
    <button
      v-for="item in items"
      :key="item.key"
      type="button"
      class="chip"
      :class="[
        `chip--${item.tone || 'info'}`,
        { 'chip--active': isActive(item.key), 'chip--static': item.static }
      ]"
      :disabled="item.static"
      @click="toggle(item)"
    >
      <span class="chip__label">{{ item.label }}</span>
      <span v-if="item.count !== undefined" class="chip__count">{{ item.count }}</span>
      <span v-if="item.hint" class="chip__hint">{{ item.hint }}</span>
    </button>
    <slot />
  </div>
</template>

<style scoped>
.filter-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 14px;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-blank);
  color: var(--el-text-color-regular);
  font-size: 12px;
  line-height: 20px;
  cursor: pointer;
  transition: all 0.15s ease;
  user-select: none;
}
.chip:hover:not(.chip--static) {
  border-color: var(--el-color-primary-light-5);
  color: var(--el-color-primary);
}
.chip--active {
  background: var(--el-color-primary-light-9);
  border-color: var(--el-color-primary);
  color: var(--el-color-primary);
  font-weight: 500;
}
.chip--static {
  cursor: default;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
}
.chip__count {
  padding: 0 6px;
  border-radius: 10px;
  background: var(--el-fill-color);
  font-weight: 600;
  font-size: 11px;
  min-width: 20px;
  text-align: center;
}
.chip--active .chip__count {
  background: var(--el-color-primary);
  color: #fff;
}
.chip--danger.chip--active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger);
  color: var(--el-color-danger);
}
.chip--danger.chip--active .chip__count {
  background: var(--el-color-danger);
  color: #fff;
}
.chip--warning.chip--active {
  background: var(--el-color-warning-light-9);
  border-color: var(--el-color-warning);
  color: var(--el-color-warning);
}
.chip--warning.chip--active .chip__count {
  background: var(--el-color-warning);
  color: #fff;
}
.chip--success.chip--active {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success);
  color: var(--el-color-success);
}
.chip--success.chip--active .chip__count {
  background: var(--el-color-success);
  color: #fff;
}
.chip__hint {
  color: var(--el-text-color-secondary);
  font-size: 11px;
}
.chip--active .chip__hint {
  color: inherit;
  opacity: 0.75;
}
</style>
