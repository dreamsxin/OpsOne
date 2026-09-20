<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createRole,
  deleteRole,
  getMenuTree,
  listRoles,
  updateRole,
  type MenuTreeNode,
  type Role
} from '@/api'

const loading = ref(false)
const rows = ref<Role[]>([])
const menuTree = ref<MenuTreeNode[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const treeRef = ref()
const formRef = ref<FormInstance>()
const form = reactive({ code: '', name: '', description: '', menuIds: [] as number[] })

const rules = {
  code: [{ required: true, message: '请输入角色编码', trigger: 'blur' }],
  name: [{ required: true, message: '请输入角色名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listRoles()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, { code: '', name: '', description: '', menuIds: [] })
  dialogVisible.value = true
}

function openEdit(row: Role) {
  editingId.value = row.id
  Object.assign(form, {
    code: row.code,
    name: row.name,
    description: row.description,
    menuIds: (row.menus || []).map((m) => m.id)
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  // 半选的父节点也要提交，否则子菜单会失去父级入口
  const checked: number[] = treeRef.value?.getCheckedKeys() || []
  const halfChecked: number[] = treeRef.value?.getHalfCheckedKeys() || []
  const payload = { ...form, menuIds: [...checked, ...halfChecked] }

  if (editingId.value) {
    await updateRole(editingId.value, payload)
    ElMessage.success('已更新')
  } else {
    await createRole(payload)
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: Role) {
  await ElMessageBox.confirm(`确认删除角色「${row.name}」？关联用户将失去该角色权限`, '危险操作', {
    type: 'warning'
  })
  await deleteRole(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  menuTree.value = await getMenuTree()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'role:manage'" type="primary" @click="openCreate">新增角色</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="code" label="编码" min-width="120" />
        <el-table-column prop="name" label="名称" min-width="130" />
        <el-table-column prop="description" label="描述" min-width="200" />
        <el-table-column label="权限数" width="90">
          <template #default="{ row }">{{ (row.menus || []).length }}</template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'role:manage'" link type="primary" @click="openEdit(row)">
              编辑授权
            </el-button>
            <el-button v-perm="'role:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑角色' : '新增角色'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="角色编码" prop="code">
          <el-input v-model="form.code" :disabled="!!editingId" placeholder="如 ops-readonly" />
        </el-form-item>
        <el-form-item label="角色名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" />
        </el-form-item>
        <el-form-item label="菜单权限">
          <el-tree
            ref="treeRef"
            :data="menuTree"
            :props="{ label: 'title', children: 'children' }"
            node-key="id"
            show-checkbox
            default-expand-all
            :default-checked-keys="form.menuIds"
            style="width: 100%; max-height: 320px; overflow: auto"
          >
            <template #default="{ data }">
              <span>
                {{ data.title }}
                <el-tag v-if="data.type === 'button'" size="small" type="info" style="margin-left: 6px">
                  {{ data.authCode }}
                </el-tag>
              </span>
            </template>
          </el-tree>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
