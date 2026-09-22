<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createNotifyTemplate,
  deleteNotifyTemplate,
  listNotifyTemplateVars,
  listNotifyTemplates,
  previewNotifyTemplate,
  updateNotifyTemplate,
  type NotifyScene,
  type NotifyTemplate
} from '@/api'

const loading = ref(false)
const rows = ref<NotifyTemplate[]>([])
const scenes = ref<NotifyScene[]>([])
const kinds = ref<{ code: string; label: string; note: string }[]>([])
const notes = ref<string[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  code: '',
  name: '',
  kind: 'im',
  scene: 'alert',
  body: '',
  enabled: true,
  remark: ''
})

const previewVisible = ref(false)
const previewText = ref('')
const previewValid = ref<boolean | undefined>(undefined)
const previewVars = ref<Record<string, string>>({})

const kindLabels = computed(() => {
  const map: Record<string, string> = {}
  for (const item of kinds.value) map[item.code] = item.label
  return map
})
const sceneLabels = computed(() => {
  const map: Record<string, string> = {}
  for (const item of scenes.value) map[item.code] = item.label
  return map
})
// 编辑时按当前选中的场景给出可用变量 —— 这份清单来自后端，不在前端硬编码
const currentVars = computed(() => scenes.value.find((s) => s.code === form.scene)?.vars ?? [])
const currentSceneNote = computed(() => scenes.value.find((s) => s.code === form.scene)?.note ?? '')
const currentKindNote = computed(() => kinds.value.find((k) => k.code === form.kind)?.note ?? '')

const rules = {
  code: [{ required: true, message: '请输入模板编码（渠道用它引用）', trigger: 'blur' }],
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  body: [{ required: true, message: '请输入模板正文', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listNotifyTemplates()
    const meta = await listNotifyTemplateVars()
    scenes.value = meta.scenes
    kinds.value = meta.kinds
    notes.value = meta.notes
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    code: '',
    name: '',
    kind: 'im',
    scene: 'alert',
    body: '[{{.severity}}] {{.title}}\n{{.summary}}',
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: NotifyTemplate) {
  editingId.value = row.id
  Object.assign(form, {
    code: row.code,
    name: row.name,
    kind: row.kind,
    scene: row.scene,
    body: row.body,
    enabled: row.enabled,
    remark: row.remark
  })
  dialogVisible.value = true
}

// Vue 模板里写不了 {{.xxx}} 这种字面量（编译器会当成插值），拼装放在脚本里
function varToken(key: string) {
  return `{{.${key}}}`
}

function insertVar(key: string) {
  form.body += varToken(key)
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (editingId.value) {
    await updateNotifyTemplate(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createNotifyTemplate({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function preview(row: NotifyTemplate) {
  const res = await previewNotifyTemplate(row.id)
  previewText.value = res.rendered
  previewValid.value = res.validJSON
  previewVars.value = res.vars
  previewVisible.value = true
}

async function remove(row: NotifyTemplate) {
  await ElMessageBox.confirm(`确认删除模板「${row.name}」？`, '提示', { type: 'warning' })
  await deleteNotifyTemplate(row.id)
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
          在这之前只有邮件能配模板：IM 的文本是代码里硬编码拼的，webhook 的 JSON 是字面量。
          这一页把这两类也做成可配置的，并且<strong>变量表收口到了一处</strong> ——
          下面「可用变量」那一栏来自后端的权威定义，不是手抄的提示文本。
          <br />
          <strong>没有配模板的渠道仍然按原来的格式发</strong>：模板是可选的覆盖，不是前置条件。
          渠道在「通知渠道」页用模板编码引用它。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button @click="load">刷新</el-button>
        <el-button v-perm="'channel:manage'" type="primary" @click="openCreate">新增模板</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有 IM / webhook 模板">
        <el-table-column prop="code" label="编码" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">
            <code style="font-size: 12px">{{ row.code }}</code>
            <el-tag v-if="row.builtin" size="small" type="info" style="margin-left: 6px">内置</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
        <el-table-column label="类型" width="120">
          <template #default="{ row }">{{ kindLabels[row.kind] || row.kind }}</template>
        </el-table-column>
        <el-table-column label="场景" width="110">
          <template #default="{ row }">{{ sceneLabels[row.scene] || row.scene }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="240" show-overflow-tooltip />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="preview(row)">预览</el-button>
            <el-button v-perm="'channel:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button
              v-perm="'channel:manage'"
              :disabled="row.builtin"
              link
              type="danger"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-card v-if="notes.length" shadow="never" style="margin-top: 12px">
        <template #header>这一页的口径</template>
        <ul style="margin: 0; padding-left: 20px; line-height: 1.9">
          <li v-for="(note, idx) in notes" :key="idx">{{ note }}</li>
        </ul>
      </el-card>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑模板' : '新增模板'" width="760px" top="5vh">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="110px">
        <el-form-item label="模板编码" prop="code">
          <el-input v-model="form.code" :disabled="!!editingId" placeholder="如 alert.im.compact；渠道用它引用" />
        </el-form-item>
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.kind" :disabled="!!editingId">
            <el-radio-button v-for="k in kinds" :key="k.code" :value="k.code">{{ k.label }}</el-radio-button>
          </el-radio-group>
          <el-text v-if="currentKindNote" type="info" size="small" style="display: block; margin-top: 4px">
            {{ currentKindNote }}
          </el-text>
        </el-form-item>
        <el-form-item label="场景">
          <el-radio-group v-model="form.scene" :disabled="!!editingId">
            <el-radio-button v-for="s in scenes" :key="s.code" :value="s.code">{{ s.label }}</el-radio-button>
          </el-radio-group>
          <el-text v-if="currentSceneNote" type="info" size="small" style="display: block; margin-top: 4px">
            {{ currentSceneNote }}
          </el-text>
        </el-form-item>
        <el-form-item label="可用变量">
          <div>
            <el-tag
              v-for="v in currentVars"
              :key="v.key"
              size="small"
              style="margin: 0 4px 4px 0; cursor: pointer"
              @click="insertVar(v.key)"
            >
              {{ v.label }} {{ varToken(v.key) }}
            </el-tag>
            <el-text type="info" size="small" style="display: block">
              点一下插入到正文。<strong>引用清单外的变量在保存时就会被拒</strong> ——
              text/template 对缺失的变量会静默渲染成空串，不校验要到真发信时才发现
            </el-text>
          </div>
        </el-form-item>
        <el-form-item label="正文" prop="body">
          <el-input v-model="form.body" type="textarea" :rows="10" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="previewVisible" title="预览（用样例变量渲染）" width="700px">
      <el-alert
        v-if="previewValid === false"
        type="error"
        :closable="false"
        title="渲染结果不是合法 JSON —— 这个模板发出去接收端会解不开"
        style="margin-bottom: 10px"
      />
      <el-alert
        v-else-if="previewValid === true"
        type="success"
        :closable="false"
        title="渲染结果是合法 JSON"
        style="margin-bottom: 10px"
      />
      <pre class="preview">{{ previewText }}</pre>
      <div style="margin-top: 10px">
        <el-tag v-for="(v, k) in previewVars" :key="k" size="small" style="margin: 0 4px 4px 0">
          {{ k }} = {{ v }}
        </el-tag>
      </div>
    </el-dialog>
  </div>
</template>

<style scoped>
.preview {
  margin: 0;
  padding: 12px;
  max-height: 48vh;
  overflow: auto;
  background: #1e1e1e;
  color: #d4d4d4;
  border-radius: 4px;
  font-family: Consolas, Monaco, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
