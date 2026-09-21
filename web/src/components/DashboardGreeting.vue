<script setup lang="ts">
/**
 * 仪表盘顶部问候条：时段 + 角色 + 姓名 + 一句总结 + 关键计数 + 刷新按钮 + 最近更新时间。
 *
 * 设计意图：进入平台第一屏就能读到「现在几点、我是谁、平台整体怎么样」，
 * 而不是先看四张冷冰冰的数字卡片。总结句由外部按当前最紧急的指标拼好传入，
 * 组件本身不做业务判断。
 */
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    name: string
    roles?: string[]
    /** 一句话总结，例：「1 条告警触发中」或「系统运行平稳」 */
    summary?: string
    /** 最近更新时间，ISO 字符串或已格式化文本；空则不显示 */
    updatedAt?: string | Date | null
    loading?: boolean
    /** 时段文案，可外部覆盖（例如做 A/B）；不传则按当前小时自动选 */
    periodLabel?: string
  }>(),
  { summary: '', updatedAt: null, loading: false, roles: () => [] }
)

const emit = defineEmits<{ (e: 'refresh'): void }>()

/** 按当前小时选问候语，凌晨/深夜单独用一档，避免"凌晨好"这种别扭说法 */
const period = computed(() => {
  if (props.periodLabel) return props.periodLabel
  const h = new Date().getHours()
  if (h < 5) return '深夜值守'
  if (h < 11) return '早安'
  if (h < 13) return '午间'
  if (h < 18) return '午后'
  if (h < 22) return '晚间'
  return '深夜值守'
})

const roleText = computed(() => (props.roles && props.roles.length ? props.roles.join(' · ') : ''))

const updatedText = computed(() => {
  const v = props.updatedAt
  if (!v) return ''
  if (typeof v === 'string') return v
  // Date 对象按本地时区 HH:mm:ss 展示，日期部分省略（就在当天）
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(v.getHours())}:${pad(v.getMinutes())}:${pad(v.getSeconds())}`
})
</script>

<template>
  <div class="greeting">
    <div class="greeting__main">
      <div class="greeting__line1">
        <span class="greeting__period">{{ period }}</span>
        <span v-if="roleText" class="greeting__role">{{ roleText }}</span>
      </div>
      <div class="greeting__line2">
        <span class="greeting__name">{{ name }}</span>
        <span class="greeting__sep">，</span>
        <span class="greeting__summary">{{ summary || '今天的运维态势已就绪' }}</span>
      </div>
    </div>
    <div class="greeting__side">
      <span v-if="updatedText" class="greeting__updated">最近更新 {{ updatedText }}</span>
      <el-button size="small" :loading="loading" @click="emit('refresh')">
        <el-icon v-if="!loading" style="margin-right: 4px"><Refresh /></el-icon>
        刷新态势
      </el-button>
    </div>
  </div>
</template>

<style scoped>
.greeting {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px 18px;
  margin-bottom: 12px;
  border-radius: 8px;
  background: linear-gradient(135deg, #eff6ff 0%, #f5f3ff 100%);
  border: 1px solid #e0e7ff;
}
.greeting__main {
  flex: 1;
  min-width: 0;
}
.greeting__line1 {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 4px;
}
.greeting__period {
  padding: 1px 8px;
  border-radius: 10px;
  background: rgba(99, 102, 241, 0.1);
  color: #4f46e5;
  font-weight: 500;
}
.greeting__role::before {
  content: '·';
  margin-right: 8px;
  color: #d1d5db;
}
.greeting__line2 {
  font-size: 16px;
  color: #111827;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.greeting__name {
  font-weight: 600;
}
.greeting__sep {
  color: #9ca3af;
}
.greeting__summary {
  color: #374151;
}
.greeting__side {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}
.greeting__updated {
  font-size: 12px;
  color: #6b7280;
}
</style>
