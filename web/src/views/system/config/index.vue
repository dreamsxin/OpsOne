<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listConfigs, updateConfigs, type SysConfig } from '@/api'

const loading = ref(false)
const configs = ref<SysConfig[]>([])
const saving = ref(false)

const groupTitle: Record<string, string> = {
  platform: '平台信息',
  execute: '执行与文件',
  bastion: '堡垒机',
  general: '其他'
}

// 只呈现内置键，自定义键去「配置中心 → 配置项」维护
const grouped = computed(() => {
  const map = new Map<string, SysConfig[]>()
  for (const item of configs.value.filter((c) => c.builtin)) {
    const list = map.get(item.group) || []
    list.push(item)
    map.set(item.group, list)
  }
  return Array.from(map.entries())
})

async function load() {
  loading.value = true
  try {
    configs.value = await listConfigs()
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    // 密钥项留空表示不修改：这里就不提交，省得「保存 N 项」里混着没动的口令
    const items = configs.value
      .filter((c) => c.builtin && !(c.secret && !c.value))
      .map((c) => ({ key: c.key, value: c.value }))
    const res = await updateConfigs(items)
    ElMessage.success(`已保存 ${res.updated} 项，平台名称与登录提示刷新后生效`)
    load()
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <div class="page-toolbar">
        <span style="color: #6b7280">
          这里是内置平台参数的表单视图；自定义配置项请到「配置中心 → 配置项」维护
        </span>
        <div class="grow"></div>
        <el-button @click="load">重置</el-button>
        <el-button v-perm="'config:manage'" type="primary" :loading="saving" @click="save">保存</el-button>
      </div>

      <div v-for="[name, items] in grouped" :key="name">
        <el-divider content-position="left">{{ groupTitle[name] || name }}</el-divider>
        <el-form label-width="180px">
          <el-form-item v-for="item in items" :key="item.key" :label="item.label || item.key">
            <el-switch
              v-if="item.type === 'bool'"
              :model-value="item.value === 'true'"
              @update:model-value="item.value = $event ? 'true' : 'false'"
            />
            <el-input
              v-else-if="item.type === 'text'"
              v-model="item.value"
              type="textarea"
              :rows="3"
              style="max-width: 560px"
            />
            <el-input
              v-else-if="item.secret"
              v-model="item.value"
              type="password"
              show-password
              style="max-width: 360px"
              :placeholder="item.hasValue ? '已配置，留空表示不修改' : '未配置'"
            />
            <el-input v-else v-model="item.value" style="max-width: 360px" />
            <div style="color: #9ca3af; font-size: 12px; margin-top: 2px">
              <code>{{ item.key }}</code>
              <span v-if="item.remark"> · {{ item.remark }}</span>
              <span v-if="item.secret"> · 加密落库，接口不回传取值</span>
            </div>
          </el-form-item>
        </el-form>
      </div>
    </el-card>
  </div>
</template>
