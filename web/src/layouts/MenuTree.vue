<script setup lang="ts">
// 侧边栏菜单的递归渲染：后端菜单树可以有任意层级，这里不再只渲染两层。
//
// 判定规则：有可见子节点的就是分组（el-sub-menu），否则是可点击页面（el-menu-item）。
// 分组自身即使配了 path 也不作为跳转目标——点它只是展开下一层。
import type { MenuNode } from '@/api'

defineProps<{ items: MenuNode[] }>()

function visibleChildren(node: MenuNode): MenuNode[] {
  return (node.children || []).filter((child) => !child.hidden)
}
</script>

<template>
  <template v-for="node in items" :key="node.id">
    <el-sub-menu v-if="visibleChildren(node).length" :index="node.path || String(node.id)">
      <template #title>
        <el-icon v-if="node.icon"><component :is="node.icon" /></el-icon>
        <span>{{ node.title }}</span>
      </template>
      <MenuTree :items="visibleChildren(node)" />
    </el-sub-menu>
    <el-menu-item v-else :index="node.path">
      <el-icon v-if="node.icon"><component :is="node.icon" /></el-icon>
      <template #title>{{ node.title }}</template>
    </el-menu-item>
  </template>
</template>
