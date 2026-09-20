<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createCommandRule,
  deleteCommandRule,
  listCommandRules,
  testCommandRule,
  updateCommandRule,
  type CommandRule
} from '@/api'

const loading = ref(false)
const rows = ref<CommandRule[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ pattern: '', description: '', action: 'block' as 'block' | 'warn', enabled: true })

const testCommand = ref('')
const testResult = ref<string>('')

const rules = {
  pattern: [{ required: true, message: '请输入正则表达式', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listCommandRules()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { pattern: '', description: '', action: 'block', enabled: true })
  dialogVisible.value = true
}

function openEdit(row: CommandRule) {
  editingId.value = row.id
  Object.assign(form, {
    pattern: row.pattern,
    description: row.description,
    action: row.action,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateCommandRule(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createCommandRule({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: CommandRule) {
  await updateCommandRule(row.id, {
    pattern: row.pattern,
    description: row.description,
    action: row.action,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '规则已启用' : '规则已停用')
}

async function remove(row: CommandRule) {
  await ElMessageBox.confirm(`确认删除规则「${row.description || row.pattern}」？`, '提示', {
    type: 'warning'
  })
  await deleteCommandRule(row.id)
  ElMessage.success('已删除')
  load()
}

async function runTest() {
  if (!testCommand.value.trim()) {
    ElMessage.warning('请输入要试跑的命令')
    return
  }
  const res = await testCommandRule(testCommand.value)
  if (res.matched) {
    const action = res.action === 'block' ? '拦截' : '告警'
    testResult.value = `命中规则 #${res.ruleId}（${res.description || '无描述'}），处理方式：${action}`
  } else {
    testResult.value = '未命中任何启用中的规则，该命令会正常执行'
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px">
        <template #title>
          规则在 PTY 输入流上匹配，能记录并阻断常见误操作，但拦不住 vim 的 :!cmd、base64
          解码执行等绕过方式，不能当作强制访问控制使用。修改后对新建会话立即生效。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-input v-model="testCommand" placeholder="试跑一条命令看是否命中规则" style="width: 300px" />
        <el-button @click="runTest">试跑</el-button>
        <span style="color: #6b7280">{{ testResult }}</span>
        <div class="grow"></div>
        <el-button v-perm="'rule:manage'" type="primary" @click="openCreate">新增规则</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="pattern" label="正则" min-width="260" show-overflow-tooltip />
        <el-table-column prop="description" label="说明" min-width="160" />
        <el-table-column label="命中处理" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="row.action === 'block' ? 'danger' : 'warning'">
              {{ row.action === 'block' ? '拦截' : '告警' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'rule:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'rule:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑规则' : '新增规则'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="正则" prop="pattern">
          <el-input v-model="form.pattern" type="textarea" :rows="2" placeholder="如 ^\s*(shutdown|reboot)\b" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" placeholder="命中时展示给操作人的原因" />
        </el-form-item>
        <el-form-item label="命中处理">
          <el-radio-group v-model="form.action">
            <el-radio value="block">拦截（不下发命令）</el-radio>
            <el-radio value="warn">仅告警</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
