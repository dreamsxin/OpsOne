<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const title = computed(() => (route.meta.title as string) || '模块')
</script>

<template>
  <div class="page">
    <el-card>
      <el-result icon="info" :title="title">
        <template #sub-title>
          <div style="line-height: 1.8">
            该模块的菜单与权限位已就位，功能尚未实现。<br />
            路由：<code>{{ route.path }}</code>
          </div>
        </template>
        <template #extra>
          <el-text type="info">实现进度见仓库 docs/ROADMAP.md</el-text>
        </template>
      </el-result>
    </el-card>
  </div>
</template>
