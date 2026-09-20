<script setup lang="ts">
import { useRouter } from 'vue-router'
const router = useRouter()
</script>

<template>
  <el-result icon="warning" title="403" sub-title="当前账号无权访问该页面">
    <template #extra>
      <el-button type="primary" @click="router.push('/dashboard/overview')">返回工作台</el-button>

    </template>
  </el-result>
</template>
