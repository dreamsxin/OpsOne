<script setup lang="ts">
/**
 * 分页控件的统一壳：
 * - layout 全站一致（total + sizes + prev/pager/next + jumper）
 * - page-sizes 提供 20/50/100/200
 * - v-model:current-page / v-model:page-size 直接和查询对象双向绑定
 *
 * 之前的散点写法「layout="total, prev, pager, next"」缺少每页条数与跳页，
 * 在数据量大的列表（会话/审计/告警）上翻找很不方便，这里一次收敛。
 */
withDefaults(
  defineProps<{
    total: number
    currentPage: number
    pageSize: number
    /** 需要更小的行高（比如嵌在抽屉里）时可传 small */
    small?: boolean
    /** 需要额外的自定义 layout 时覆盖默认 */
    layout?: string
    pageSizes?: number[]
  }>(),
  {
    small: false,
    layout: 'total, sizes, prev, pager, next, jumper',
    pageSizes: () => [20, 50, 100, 200]
  }
)

const emit = defineEmits<{
  (e: 'update:currentPage', v: number): void
  (e: 'update:pageSize', v: number): void
  (e: 'change'): void
}>()

function onCurrentChange(v: number) {
  emit('update:currentPage', v)
  emit('change')
}
function onSizeChange(v: number) {
  emit('update:pageSize', v)
  // 每页条数变了，回到第一页；否则可能停在越界的页码上
  emit('update:currentPage', 1)
  emit('change')
}
</script>

<template>
  <el-pagination
    class="ops-pagination"
    :class="{ 'ops-pagination--small': small }"
    :total="total"
    :current-page="currentPage"
    :page-size="pageSize"
    :page-sizes="pageSizes"
    :layout="layout"
    :small="small"
    background
    @current-change="onCurrentChange"
    @size-change="onSizeChange"
  />
</template>

<style scoped>
.ops-pagination {
  margin-top: 12px;
  justify-content: flex-end;
}
.ops-pagination--small {
  margin-top: 8px;
}
</style>
