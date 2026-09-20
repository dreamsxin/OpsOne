<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createDepartment,
  deleteDepartment,
  getDepartmentTree,
  listCompanies,
  updateDepartment,
  type Company,
  type DeptNode
} from '@/api'

const loading = ref(false)
const tree = ref<DeptNode[]>([])
const companies = ref<Company[]>([])
const filterCompany = ref<number | undefined>(undefined)

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  companyId: undefined as number | undefined,
  parentId: 0,
  name: '',
  code: '',
  leader: '',
  sort: 0
})

const rules = {
  companyId: [{ required: true, message: '请选择所属公司', trigger: 'change' }],
  name: [{ required: true, message: '请输入部门名称', trigger: 'blur' }]
}

// 上级部门候选：拍平成「层级 + 名称」便于在下拉里辨认
const flatDepts = computed(() => {
  const list: { id: number; label: string }[] = []
  const walk = (nodes: DeptNode[], depth: number) => {
    for (const node of nodes) {
      list.push({ id: node.id, label: `${'　'.repeat(depth)}${node.name}` })
      if (node.children?.length) walk(node.children, depth + 1)
    }
  }
  walk(tree.value, 0)
  return list
})

async function load() {
  loading.value = true
  try {
    tree.value = await getDepartmentTree(filterCompany.value)
  } finally {
    loading.value = false
  }
}

function openCreate(parent?: DeptNode) {
  editingId.value = null
  Object.assign(form, {
    companyId: parent?.companyId ?? filterCompany.value ?? companies.value[0]?.id,
    parentId: parent?.id ?? 0,
    name: '',
    code: '',
    leader: '',
    sort: 0
  })
  dialogVisible.value = true
}

function openEdit(row: DeptNode) {
  editingId.value = row.id
  Object.assign(form, {
    companyId: row.companyId,
    parentId: row.parentId,
    name: row.name,
    code: row.code,
    leader: row.leader,
    sort: row.sort
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateDepartment(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createDepartment({ ...form })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: DeptNode) {
  await ElMessageBox.confirm(`确认删除部门「${row.name}」？`, '提示', { type: 'warning' })
  await deleteDepartment(row.id)
  ElMessage.success('已删除')
  load()
}

function companyName(id: number) {
  return companies.value.find((c) => c.id === id)?.name || `#${id}`
}

onMounted(async () => {
  companies.value = await listCompanies()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="部门是数据权限的基本单位：用户归属部门决定他能看到哪些主机，主机归属部门决定谁能看到它。删除部门前需要先迁移其下的子部门、用户与主机"
      />

      <div class="page-toolbar">
        <el-select v-model="filterCompany" placeholder="按公司过滤" clearable style="width: 200px" @change="load">
          <el-option v-for="item in companies" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'org:manage'" type="primary" @click="openCreate()">新增部门</el-button>
      </div>

      <el-table
        v-loading="loading"
        :data="tree"
        row-key="id"
        border
        default-expand-all
        :tree-props="{ children: 'children' }"
        empty-text="还没有部门，先新增一个"
      >
        <el-table-column prop="name" label="部门" min-width="220" />
        <el-table-column prop="code" label="编码" width="130" />
        <el-table-column label="所属公司" min-width="150">
          <template #default="{ row }">{{ companyName(row.companyId) }}</template>
        </el-table-column>
        <el-table-column prop="leader" label="负责人" width="120" />
        <el-table-column label="人员" width="80">
          <template #default="{ row }">{{ row.userCount }}</template>
        </el-table-column>
        <el-table-column label="主机" width="80">
          <template #default="{ row }">{{ row.hostCount }}</template>
        </el-table-column>
        <el-table-column prop="sort" label="排序" width="80" />
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'org:manage'" link type="primary" @click="openCreate(row)">加子部门</el-button>
            <el-button v-perm="'org:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'org:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑部门' : '新增部门'" width="500px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="所属公司" prop="companyId">
          <el-select v-model="form.companyId" style="width: 100%">
            <el-option v-for="item in companies" :key="item.id" :label="item.name" :value="item.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="上级部门">
          <el-select v-model="form.parentId" style="width: 100%">
            <el-option label="（作为一级部门）" :value="0" />
            <el-option
              v-for="item in flatDepts.filter((d) => d.id !== editingId)"
              :key="item.id"
              :label="item.label"
              :value="item.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="部门名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="编码">
          <el-input v-model="form.code" />
        </el-form-item>
        <el-form-item label="负责人">
          <el-input v-model="form.leader" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sort" :min="0" :max="9999" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
