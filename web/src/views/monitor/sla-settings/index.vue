<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { getSLASettings, runSLAScan, updateSLASettings, type SLASettings } from '@/api'

const router = useRouter()
const loading = ref(false)
const saving = ref(false)
const scanning = ref(false)
const settings = ref<SLASettings | null>(null)

// form 只存「用户正在编辑的值」，保存成功后重新拉一次，避免界面和库里不一致
const form = reactive<{
  respond: Record<string, number>
  recover: Record<string, number>
  remindBefore: number
  repeatHours: number
}>({ respond: {}, recover: {}, remindBefore: 5, repeatHours: 4 })

async function load() {
  loading.value = true
  try {
    const res = await getSLASettings()
    settings.value = res
    form.respond = {}
    form.recover = {}
    for (const level of res.levels) {
      form.respond[level.severity] = level.respondMinutes
      form.recover[level.severity] = level.recoverMinutes
    }
    form.remindBefore = res.remindBefore
    form.repeatHours = res.repeatHours
  } finally {
    loading.value = false
  }
}

// 恢复目标小于响应目标一定是填反了，前端先提示一次，后端也会再挡
const badLevels = computed(() => {
  if (!settings.value) return [] as string[]
  return settings.value.levels
    .filter((level) => {
      const respond = form.respond[level.severity]
      const recover = form.recover[level.severity]
      return respond > 0 && recover > 0 && recover < respond
    })
    .map((level) => level.label)
})

function humanMinutes(minutes: number) {
  if (minutes <= 0) return '不设 SLA'
  if (minutes < 60) return `${minutes} 分钟`
  if (minutes % 60 === 0) return `${minutes / 60} 小时`
  return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分钟`
}

async function save() {
  if (badLevels.value.length) {
    ElMessage.warning(`${badLevels.value.join('、')}的恢复目标小于响应目标，请检查`)
    return
  }
  saving.value = true
  try {
    const res = await updateSLASettings({
      respond: form.respond,
      recover: form.recover,
      remindBefore: form.remindBefore,
      repeatHours: form.repeatHours
    })
    ElMessage.success(`已保存 ${res.updated} 项，事件列表的 SLA 立即按新目标算`)
    load()
  } finally {
    saving.value = false
  }
}

async function scan() {
  scanning.value = true
  try {
    await runSLAScan()
    ElMessage.success('已扫描一次，超时与临期的事件会给负责人发消息')
    load()
  } finally {
    scanning.value = false
  }
}

function gotoBreached() {
  router.push({ path: '/monitor/events', query: { sla: 'breached' } })
}

onMounted(load)
</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <template #header>事件 SLA 目标</template>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <div v-for="note in settings?.notes || []" :key="note">· {{ note }}</div>
      </el-alert>

      <el-table :data="settings?.levels || []" border stripe>
        <el-table-column label="事件级别" width="120">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.severity === 'critical' ? 'danger' : row.severity === 'warning' ? 'warning' : 'info'"
            >
              {{ row.label }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="响应目标（分钟）" width="200">
          <template #default="{ row }">
            <el-input-number
              v-model="form.respond[row.severity]"
              :min="0"
              :max="10080"
              :step="5"
              size="small"
              controls-position="right"
              style="width: 130px"
            />
          </template>
        </el-table-column>
        <el-table-column label="恢复目标（分钟）" width="200">
          <template #default="{ row }">
            <el-input-number
              v-model="form.recover[row.severity]"
              :min="0"
              :max="10080"
              :step="15"
              size="small"
              controls-position="right"
              style="width: 130px"
            />
          </template>
        </el-table-column>
        <el-table-column label="折算" min-width="220">
          <template #default="{ row }">
            <span style="color: #6b7280">
              响应 {{ humanMinutes(form.respond[row.severity]) }} · 恢复
              {{ humanMinutes(form.recover[row.severity]) }}
            </span>
          </template>
        </el-table-column>
      </el-table>

      <div class="sla-extra">
        <div class="sla-field">
          <span class="sla-label">临期提前量</span>
          <el-input-number
            v-model="form.remindBefore"
            :min="0"
            :max="10080"
            :step="5"
            size="small"
            controls-position="right"
            style="width: 130px"
          />
          <span class="sla-hint">分钟。距超时不足这么久就标成「临期」并提醒一次；填 0 表示只在真超时后才提醒</span>
        </div>
        <div class="sla-field">
          <span class="sla-label">重复提醒间隔</span>
          <el-input-number
            v-model="form.repeatHours"
            :min="1"
            :max="168"
            size="small"
            controls-position="right"
            style="width: 130px"
          />
          <span class="sla-hint">小时。同一个事件的同一条 SLA 在这段时间内不重复发消息</span>
        </div>
      </div>

      <div v-if="badLevels.length" style="margin-top: 10px">
        <el-alert
          type="warning"
          :closable="false"
          :title="`${badLevels.join('、')}的恢复目标小于响应目标，这通常是填反了`"
        />
      </div>

      <div class="page-toolbar" style="margin-top: 12px">
        <div class="grow"></div>
        <el-button @click="load">重置</el-button>
        <el-button v-perm="'sla:manage'" :loading="scanning" @click="scan">立即扫描一次</el-button>
        <el-button v-perm="'sla:manage'" type="primary" :loading="saving" @click="save">保存</el-button>
      </div>
    </el-card>

    <el-card style="margin-top: 12px">
      <template #header>当前欠账</template>
      <div class="sla-stats">
        <div class="sla-stat">
          <div class="sla-stat-num danger">{{ settings?.stats.respondBreached ?? 0 }}</div>
          <div class="sla-stat-text">响应已超时（还没人接手）</div>
        </div>
        <div class="sla-stat">
          <div class="sla-stat-num danger">{{ settings?.stats.recoverBreached ?? 0 }}</div>
          <div class="sla-stat-text">恢复已超时（还没解决）</div>
        </div>
        <div class="sla-stat">
          <div class="sla-stat-num warn">{{ settings?.stats.risk ?? 0 }}</div>
          <div class="sla-stat-text">临期（快到点了）</div>
        </div>
        <div class="grow"></div>
        <el-button type="primary" plain @click="gotoBreached">去事件中心处理</el-button>
      </div>
      <div style="margin-top: 8px; color: #6b7280; font-size: 12px">
        只统计还没完结的单子。历史上超时但已经做完的，去「事件复盘」看耗时，不在这里重复记账。
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.sla-extra {
  margin-top: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.sla-field {
  display: flex;
  align-items: center;
  gap: 10px;
}
.sla-label {
  width: 100px;
  color: #374151;
}
.sla-hint {
  color: #6b7280;
  font-size: 12px;
}
.sla-stats {
  display: flex;
  align-items: center;
  gap: 32px;
}
.sla-stat-num {
  font-size: 24px;
  font-weight: 600;
}
.sla-stat-num.danger {
  color: #dc2626;
}
.sla-stat-num.warn {
  color: #d97706;
}
.sla-stat-text {
  color: #6b7280;
  font-size: 12px;
}
</style>
