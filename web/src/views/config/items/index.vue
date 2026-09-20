<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { createConfig, deleteConfig, listConfigs, updateConfigs, type SysConfig } from '@/api'

const loading = ref(false)
const rows = ref<SysConfig[]>([])
const group = ref('')
const dirty = ref(new Set<string>())

const dialogVisible = ref(false)
const formRef = ref<FormInstance>()
const form = reactive({
  group: 'general',
  key: '',
  value: '',
  type: 'string' as SysConfig['type'],
  label: '',
  remark: ''
})

const rules = {
  key: [{ required: true, message: '请输入配置键', trigger: 'blur' }]
}

const groups = computed(() => Array.from(new Set(rows.value.map((r) => r.group))))

async function load() {
  loading.value = true
  dirty.value = new Set()
  try {
    rows.value = await listConfigs(group.value || undefined)
  } finally {
    loading.value = false
  }
}

function markDirty(row: SysConfig) {
  dirty.value.add(row.key)
}

async function saveDirty() {
  const items = rows.value
    .filter((row) => dirty.value.has(row.key))
    .map((row) => ({ key: row.key, value: row.value }))
  if (!items.length) {
    ElMessage.info('没有需要保存的修改')
    return
  }
  const res = await updateConfigs(items)
  ElMessage.success(`已保存 ${res.updated} 项`)
  load()
}

function openCreate() {
  Object.assign(form, { group: 'general', key: '', value: '', type: 'string', label: '', remark: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  await createConfig({ ...form })
  ElMessage.success('已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: SysConfig) {
  await ElMessageBox.confirm(`确认删除配置项「${row.key}」？`, '提示', { type: 'warning' })
  await deleteConfig(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          内置配置项只能改取值，不能删除。已接入运行时的键：
          <code>platform.name</code>、<code>platform.login_notice</code>（登录页与顶栏）、
          <code>exec.concurrency</code>（批量执行并发）、<code>file.max_upload_mb</code>（上传上限）、
          <code>session.record_keep_days</code>（启动时清理过期录像）。自定义键仅做存储，需要自行接入。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="group" placeholder="按分组过滤" clearable style="width: 160px" @change="load">
          <el-option v-for="item in groups" :key="item" :label="item" :value="item" />
        </el-select>
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'config:manage'" @click="openCreate">新增配置项</el-button>
        <el-button v-perm="'config:manage'" type="primary" @click="saveDirty">
          保存修改{{ dirty.size ? `（${dirty.size}）` : '' }}
        </el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="group" label="分组" width="110" />
        <el-table-column prop="key" label="配置键" min-width="200">
          <template #default="{ row }">
            <code style="font-size: 12px">{{ row.key }}</code>
            <el-tag v-if="row.builtin" size="small" type="info" style="margin-left: 6px">内置</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="label" label="名称" min-width="140" />
        <el-table-column label="取值" min-width="220">
          <template #default="{ row }">
            <el-switch
              v-if="row.type === 'bool'"
              :model-value="row.value === 'true'"
              @update:model-value="((row.value = $event ? 'true' : 'false'), markDirty(row))"
            />
            <el-input
              v-else
              v-model="row.value"
              :type="row.type === 'text' ? 'textarea' : 'text'"
              :rows="2"
              size="small"
              @input="markDirty(row)"
            />
          </template>
        </el-table-column>
        <el-table-column prop="type" label="类型" width="90" />
        <el-table-column prop="remark" label="说明" min-width="200" show-overflow-tooltip />
        <el-table-column prop="updatedBy" label="修改人" width="100" />
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button
              v-perm="'config:manage'"
              link
              type="danger"
              :disabled="row.builtin"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" title="新增配置项" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="分组">
          <el-input v-model="form.group" placeholder="如 general、platform" />
        </el-form-item>
        <el-form-item label="配置键" prop="key">
          <el-input v-model="form.key" placeholder="建议用 模块.用途，如 backup.retain_days" />
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="form.label" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type">
            <el-option label="字符串" value="string" />
            <el-option label="长文本" value="text" />
            <el-option label="整数" value="int" />
            <el-option label="布尔" value="bool" />
          </el-select>
        </el-form-item>
        <el-form-item label="取值">
          <el-input v-model="form.value" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
