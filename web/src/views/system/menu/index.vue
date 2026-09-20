<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createMenu,
  deleteMenu,
  getMenuTree,
  updateMenu,
  type MenuTreeNode
} from '@/api'

const loading = ref(false)
const tree = ref<MenuTreeNode[]>([])

const dialogVisible = ref(false)
const editing = ref<MenuTreeNode | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  parentId: 0,
  name: '',
  title: '',
  path: '',
  component: '',
  icon: '',
  type: 'menu' as 'menu' | 'button',
  authCode: '',
  sort: 0,
  hidden: false
})

const rules = {
  title: [{ required: true, message: '请输入菜单标题', trigger: 'blur' }]
}

// 内置菜单只能改展示字段，结构字段在代码里维护
const structuralDisabled = computed(() => !!editing.value?.builtin)

const flatMenus = computed(() => {
  const list: { id: number; label: string }[] = []
  const walk = (nodes: MenuTreeNode[], depth: number) => {
    for (const node of nodes) {
      if (node.type !== 'button') {
        list.push({ id: node.id, label: `${'　'.repeat(depth)}${node.title}` })
      }
      if (node.children?.length) walk(node.children, depth + 1)
    }
  }
  walk(tree.value, 0)
  return list
})

const counts = computed(() => {
  let builtin = 0
  let custom = 0
  const walk = (nodes: MenuTreeNode[]) => {
    for (const node of nodes) {
      node.builtin ? builtin++ : custom++
      if (node.children?.length) walk(node.children)
    }
  }
  walk(tree.value)
  return { builtin, custom }
})

async function load() {
  loading.value = true
  try {
    tree.value = await getMenuTree()
  } finally {
    loading.value = false
  }
}

function openCreate(parent?: MenuTreeNode) {
  editing.value = null
  Object.assign(form, {
    parentId: parent?.id ?? 0,
    name: '',
    title: '',
    path: '',
    component: '',
    icon: '',
    type: 'menu',
    authCode: '',
    sort: 0,
    hidden: false
  })
  dialogVisible.value = true
}

function openEdit(row: MenuTreeNode) {
  editing.value = row
  Object.assign(form, {
    parentId: row.parentId,
    name: row.name,
    title: row.title,
    path: row.path,
    component: row.component,
    icon: row.icon,
    type: row.type,
    authCode: row.authCode,
    sort: row.sort,
    hidden: row.hidden
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editing.value) {
    await updateMenu(editing.value.id, { ...form })
    ElMessage.success(
      editing.value.builtin ? '已更新展示设置（内置菜单的结构由代码维护）' : '已更新'
    )
  } else {
    await createMenu({ ...form })
    ElMessage.success('已新增，刷新页面后生效')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: MenuTreeNode) {
  await ElMessageBox.confirm(`确认删除菜单「${row.title}」？`, '提示', { type: 'warning' })
  await deleteMenu(row.id)
  ElMessage.success('已删除')
  load()
}

async function toggleHidden(row: MenuTreeNode) {
  await updateMenu(row.id, {
    parentId: row.parentId,
    name: row.name,
    title: row.title,
    path: row.path,
    component: row.component,
    icon: row.icon,
    type: row.type,
    authCode: row.authCode,
    sort: row.sort,
    hidden: row.hidden
  })
  ElMessage.success(row.hidden ? '已隐藏，刷新后从导航消失' : '已显示')
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          内置菜单（{{ counts.builtin }} 项）的结构由代码维护，启动时会校正父级、路径、组件与权限码，
          这里只能改<strong>标题、图标、排序、是否隐藏</strong>，改动不会被覆盖。
          自建菜单（{{ counts.custom }} 项）可完全自定义并删除。改完刷新浏览器生效。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'menu:manage'" type="primary" @click="openCreate()">新增菜单</el-button>
      </div>

      <el-table
        v-loading="loading"
        :data="tree"
        row-key="id"
        border
        default-expand-all
        :tree-props="{ children: 'children' }"
      >
        <el-table-column prop="title" label="标题" min-width="200">
          <template #default="{ row }">
            <el-icon v-if="row.icon" style="vertical-align: middle; margin-right: 4px">
              <component :is="row.icon" />
            </el-icon>
            {{ row.title }}
            <el-tag v-if="!row.builtin" size="small" type="success" style="margin-left: 6px">自建</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.type === 'button' ? 'warning' : 'primary'">
              {{ row.type === 'button' ? '按钮' : '菜单' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="path" label="路径" min-width="180" show-overflow-tooltip />
        <el-table-column prop="component" label="组件" min-width="180" show-overflow-tooltip />
        <el-table-column prop="authCode" label="权限码" min-width="140" />
        <el-table-column prop="sort" label="排序" width="80" />
        <el-table-column label="隐藏" width="90">
          <template #default="{ row }">
            <el-switch
              v-model="row.hidden"
              :disabled="row.type === 'button'"
              @change="toggleHidden(row)"
            />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.type !== 'button'"
              v-perm="'menu:manage'"
              link
              type="primary"
              @click="openCreate(row)"
            >
              加子项
            </el-button>
            <el-button v-perm="'menu:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button
              v-perm="'menu:manage'"
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
      :title="editing ? (editing.builtin ? '编辑内置菜单（仅展示设置）' : '编辑自建菜单') : '新增菜单'"
      width="560px"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="标题" prop="title">
          <el-input v-model="form.title" />
        </el-form-item>
        <el-form-item label="图标">
          <el-input v-model="form.icon" placeholder="Element Plus 图标名，如 Setting" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sort" :min="0" :max="9999" />
        </el-form-item>
        <el-form-item label="隐藏">
          <el-switch v-model="form.hidden" :disabled="form.type === 'button'" />
        </el-form-item>

        <el-divider content-position="left">
          结构设置{{ structuralDisabled ? '（内置菜单不可改）' : '' }}
        </el-divider>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type" :disabled="structuralDisabled">
            <el-radio value="menu">菜单</el-radio>
            <el-radio value="button">按钮权限</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="上级">
          <el-select v-model="form.parentId" :disabled="structuralDisabled" style="width: 100%">
            <el-option label="（顶级菜单）" :value="0" />
            <el-option
              v-for="item in flatMenus.filter((m) => m.id !== editing?.id)"
              :key="item.id"
              :label="item.label"
              :value="item.id"
            />
          </el-select>
        </el-form-item>
        <template v-if="form.type === 'menu'">
          <el-form-item label="路由名">
            <el-input v-model="form.name" :disabled="structuralDisabled" placeholder="唯一，如 MyPage" />
          </el-form-item>
          <el-form-item label="路径">
            <el-input v-model="form.path" :disabled="structuralDisabled" placeholder="如 /custom/page" />
          </el-form-item>
          <el-form-item label="组件">
            <el-input
              v-model="form.component"
              :disabled="structuralDisabled"
              placeholder="views 下的路径，如 /placeholder/index"
            />
          </el-form-item>
        </template>
        <el-form-item v-else label="权限码">
          <el-input v-model="form.authCode" :disabled="structuralDisabled" placeholder="如 host:create" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
