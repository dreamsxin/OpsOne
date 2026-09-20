<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { createTag, deleteTag, listTags, updateTag, type Tag } from '@/api'

const loading = ref(false)
const rows = ref<Tag[]>([])
const keyword = ref('')

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ name: '', category: 'general', color: 'info', remark: '' })

const colorOptions = [
  { value: 'info', label: '灰' },
  { value: 'primary', label: '蓝' },
  { value: 'success', label: '绿' },
  { value: 'warning', label: '橙' },
  { value: 'danger', label: '红' }
]

const rules = {
  name: [{ required: true, message: '请输入标签名称', trigger: 'blur' }]
}

const filtered = computed(() =>
  rows.value.filter(
    (row) =>
      !keyword.value ||
      row.name.includes(keyword.value) ||
      row.category.includes(keyword.value)
  )
)

const unusedCount = computed(
  () => rows.value.filter((row) => row.hostCount + row.dbCount === 0).length
)

async function load() {
  loading.value = true
  try {
    rows.value = await listTags()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { name: '', category: 'general', color: 'info', remark: '' })
  dialogVisible.value = true
}

function openEdit(row: Tag) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    category: row.category,
    color: row.color,
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateTag(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createTag({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: Tag) {
  const used = row.hostCount + row.dbCount
  const extra = used ? `该标签已被 ${used} 个资产使用，删除后资产上的文本不会变，只是从词表移除。` : ''
  await ElMessageBox.confirm(`确认删除标签「${row.name}」？${extra}`, '提示', { type: 'warning' })
  await deleteTag(row.id)
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
          标签字典用于统一词表与用量统计。资产上的标签仍以逗号分隔文本存储，
          在主机与数据库表单里可以直接从字典里选，也可以现场新建。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-input v-model="keyword" placeholder="名称 / 分类" style="width: 200px" clearable />
        <el-button @click="load">刷新</el-button>
        <span style="color: #6b7280">
          共 {{ rows.length }} 个标签，其中 {{ unusedCount }} 个尚未被使用
        </span>
        <div class="grow"></div>
        <el-button v-perm="'tag:manage'" type="primary" @click="openCreate">新增标签</el-button>
      </div>

      <el-table v-loading="loading" :data="filtered" border stripe>
        <el-table-column label="标签" min-width="160">
          <template #default="{ row }">
            <el-tag :type="row.color === 'info' ? 'info' : row.color" size="small">
              {{ row.name }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="category" label="分类" width="130" />
        <el-table-column label="主机使用" width="110">
          <template #default="{ row }">
            <span :style="{ color: row.hostCount ? undefined : '#9ca3af' }">{{ row.hostCount }}</span>
          </template>
        </el-table-column>
        <el-table-column label="数据库使用" width="120">
          <template #default="{ row }">
            <span :style="{ color: row.dbCount ? undefined : '#9ca3af' }">{{ row.dbCount }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="说明" min-width="220" show-overflow-tooltip />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'tag:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'tag:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑标签' : '新增标签'" width="460px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="80px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" :disabled="!!editingId" placeholder="不能包含逗号" />
        </el-form-item>
        <el-form-item label="分类">
          <el-input v-model="form.category" placeholder="如 role、env、owner" />
        </el-form-item>
        <el-form-item label="颜色">
          <el-select v-model="form.color">
            <el-option v-for="item in colorOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
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
