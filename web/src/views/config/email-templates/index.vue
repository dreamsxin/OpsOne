<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createEmailTemplate,
  deleteEmailTemplate,
  listEmailTemplates,
  previewEmailTemplate,
  updateEmailTemplate,
  type EmailTemplate
} from '@/api'

const loading = ref(false)
const rows = ref<EmailTemplate[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const editingBuiltin = ref(false)
const formRef = ref<FormInstance>()
const form = reactive({
  code: '',
  name: '',
  subject: '',
  body: '',
  variables: '',
  remark: '',
  enabled: true
})

const previewVisible = ref(false)
const preview = ref<{ subject: string; body: string; vars: Record<string, string> } | null>(null)

const rules = {
  code: [{ required: true, message: '请输入模板编码', trigger: 'blur' }],
  name: [{ required: true, message: '请输入模板名称', trigger: 'blur' }],
  subject: [{ required: true, message: '请输入邮件主题', trigger: 'blur' }]
}

// 模板占位符里含双花括号，直接写在 template 里会被 Vue 当成插值，放到脚本里传过去
const varSample = '{{.title}}'
const subjectSample = '[{{.severity}}] {{.title}}'


async function load() {
  loading.value = true
  try {
    rows.value = await listEmailTemplates()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  editingBuiltin.value = false
  Object.assign(form, {
    code: '',
    name: '',
    subject: '',
    body: '',
    variables: '',
    remark: '',
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: EmailTemplate) {
  editingId.value = row.id
  editingBuiltin.value = row.builtin
  Object.assign(form, {
    code: row.code,
    name: row.name,
    subject: row.subject,
    body: row.body,
    variables: row.variables,
    remark: row.remark,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateEmailTemplate(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createEmailTemplate({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function openPreview(row: EmailTemplate) {
  preview.value = await previewEmailTemplate(row.id)
  previewVisible.value = true
}

async function remove(row: EmailTemplate) {
  await ElMessageBox.confirm(`确认删除模板「${row.name}」？`, '提示', { type: 'warning' })
  await deleteEmailTemplate(row.id)
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
          正文使用 Go <code>text/template</code> 语法，变量写成 <code>{{ varSample }}</code>。

          保存时会做语法校验；点「预览」可用样例告警数据渲染看效果。
          email 类型的通知渠道会引用这里的模板，SMTP 参数在「系统管理 → 系统配置」里配。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'config:manage'" type="primary" @click="openCreate">新增模板</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="code" label="编码" min-width="150">
          <template #default="{ row }">
            <code style="font-size: 12px">{{ row.code }}</code>
            <el-tag v-if="row.builtin" size="small" type="info" style="margin-left: 6px">内置</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="name" label="名称" min-width="150" />
        <el-table-column prop="subject" label="主题" min-width="220" show-overflow-tooltip />
        <el-table-column prop="variables" label="可用变量" min-width="200" show-overflow-tooltip />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openPreview(row)">预览</el-button>
            <el-button v-perm="'config:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
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

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑模板' : '新增模板'"
      width="680px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="编码" prop="code">
          <el-input v-model="form.code" :disabled="!!editingId" placeholder="如 alert.critical" />
        </el-form-item>
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="主题" prop="subject">
          <el-input v-model="form.subject" :placeholder="'支持变量，如 ' + subjectSample" />

        </el-form-item>
        <el-form-item label="正文">
          <el-input v-model="form.body" type="textarea" :rows="12" />
        </el-form-item>
        <el-form-item label="可用变量">
          <el-input v-model="form.variables" placeholder="逗号分隔，仅作提示用" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
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

    <el-drawer v-model="previewVisible" title="模板预览（样例数据）" size="50%">
      <template v-if="preview">
        <el-descriptions :column="1" border size="small">
          <el-descriptions-item label="主题">{{ preview.subject }}</el-descriptions-item>
        </el-descriptions>
        <el-divider content-position="left">正文</el-divider>
        <pre class="output-pre" style="max-height: 420px">{{ preview.body }}</pre>
        <el-divider content-position="left">样例变量</el-divider>
        <div>
          <el-tag v-for="(v, k) in preview.vars" :key="k" size="small" style="margin: 0 4px 4px 0">
            {{ k }}={{ v }}
          </el-tag>
        </div>
      </template>
    </el-drawer>
  </div>
</template>
