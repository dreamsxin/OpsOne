<script setup lang="ts">
// 占位页：菜单与权限位已经排好，但功能确实还没做。
//
// 刻意把话说白：不写"敬请期待"这种含糊措辞，而是直接说未实现、给出路线图位置，
// 并指出当前能用什么替代——否则使用者会以为功能藏在别处没找到。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()

const title = computed(() => (route.meta.title as string) || '模块')

// 已知占位模块的说明与替代方案
const notes: Record<string, { plan: string; alternatives: { title: string; path: string }[] }> = {
  '/security/firewall': {
    plan: '下一轮实现：SSH 真连主机读取 firewalld / iptables / ufw 的现有规则，并支持放行与封禁下发（走命令闸门与生产二次确认）。',
    alternatives: [
      { title: '公网监测（看哪些端口暴露在公网）', path: '/monitor/public-ip' },
      { title: '命令规则（拦截高危的防火墙操作）', path: '/system/command-rule' }
    ]
  },
  '/security/awareness': {
    plan: '暂未排期。这类内容更适合放在公司内部知识库，平台侧只会做「公告 + 必读确认」这种轻量形态。',
    alternatives: [{ title: '公告管理', path: '/system/announcement' }]
  },
  '/security/features': {
    plan: '暂未排期。检测规则已经承担了「按特征判定异常」的职责，特征库只有在需要共享规则集时才有意义。',
    alternatives: [{ title: '检测规则', path: '/monitor/detection-rules' }]
  }
}

const note = computed(() => notes[route.path])
</script>

<template>
  <div class="page">
    <el-card>
      <el-result icon="info" :title="`${title}：功能尚未实现`">
        <template #sub-title>
          <div style="line-height: 1.9; text-align: left; max-width: 640px; margin: 0 auto">
            这个页面是占位的——菜单与权限位已经就位，但后端功能还没有做，
            所以这里不会有任何数据，也不要按"功能异常"去排查。
            <div style="margin-top: 8px">
              路由：<code>{{ route.path }}</code>
            </div>
            <div v-if="note" style="margin-top: 8px">{{ note.plan }}</div>
          </div>
        </template>
        <template #extra>
          <div v-if="note?.alternatives.length" style="margin-bottom: 8px">
            <span style="color: #6b7280; font-size: 13px">现在可以先用：</span>
            <el-button
              v-for="alt in note.alternatives"
              :key="alt.path"
              link
              type="primary"
              @click="router.push(alt.path)"
            >
              {{ alt.title }}
            </el-button>
          </div>
          <el-text type="info">完整实现进度与"刻意不做的部分"见仓库 docs/ROADMAP.md</el-text>
        </template>
      </el-result>
    </el-card>
  </div>
</template>
