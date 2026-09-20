<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createCompany,
  deleteCompany,
  listCompanies,
  updateCompany,
  type Company
} from '@/api'

const loading = ref(false)
const rows = ref<Company[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({ name: '', code: '', remark: '' })

const rules = {
  name: [{ required: true, message: '请输入公司名称', trigger: 'blur' }],
  code: [{ required: true, message: '请输入公司编码', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listCompanies()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { name: '', code: '', remark: '' })
  dialogVisible.value = true
}

function openEdit(row: Company) {
  editingId.value = row.id
  Object.assign(form, { name: row.name, code: row.code, remark: row.remark })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateCompany(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createCompany({ ...form })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: Company) {
  await ElMessageBox.confirm(`确认删除公司「${row.name}」？`, '提示', { type: 'warning' })
  await deleteCompany(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="公司是部门树的根。删除公司前需要先清空其下的部门"
      />

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'org:manage'" type="primary" @click="openCreate">新增公司</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="公司名称" min-width="180" />
        <el-table-column prop="code" label="编码" min-width="140" />
        <el-table-column prop="remark" label="备注" min-width="200" show-overflow-tooltip />
        <el-table-column prop="createdAt" label="创建时间" min-width="180" />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'org:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'org:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑公司' : '新增公司'" width="480px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="公司名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="编码" prop="code">
          <el-input v-model="form.code" :disabled="!!editingId" placeholder="如 HQ、SUB-SH" />
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
  </div>
</template>
