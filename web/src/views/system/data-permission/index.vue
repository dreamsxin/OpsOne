<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  diagnoseDataScope,
  getDepartmentTree,
  listRoles,
  listUsers,
  updateRole,
  type DataScopeDiagnosis,
  type DeptNode,
  type Role,
  type User
} from '@/api'

const loading = ref(false)
const roles = ref<Role[]>([])
const users = ref<User[]>([])
const tree = ref<DeptNode[]>([])

const diagUserId = ref<number | undefined>(undefined)
const diagnosis = ref<DataScopeDiagnosis | null>(null)

const scopeOptions = [
  { value: 'all', label: '全部数据', hint: '不做任何过滤' },
  { value: 'dept', label: '本部门', hint: '仅用户所在部门的数据' },
  { value: 'dept_below', label: '本部门及下级', hint: '含所有下级部门' },
  { value: 'self', label: '仅本人', hint: '仅自己录入的数据' },
  { value: 'custom', label: '指定部门', hint: '手动勾选可见部门' }
]

const scopeLabel = (value: string) => scopeOptions.find((o) => o.value === value)?.label || value

// 部门 ID -> 名称，用于展示自定义范围
const deptNameMap = computed(() => {
  const map = new Map<number, string>()
  const walk = (nodes: DeptNode[]) => {
    for (const node of nodes) {
      map.set(node.id, node.name)
      if (node.children?.length) walk(node.children)
    }
  }
  walk(tree.value)
  return map
})

async function load() {
  loading.value = true
  try {
    roles.value = await listRoles()
  } finally {
    loading.value = false
  }
}

// 行内直接改数据范围，避免和角色菜单授权弹窗混在一起
async function saveScope(row: Role) {
  if (row.dataScope === 'custom' && !row.dataDeptIds?.length) {
    ElMessage.warning('「指定部门」范围需要至少勾选一个部门')
    return
  }
  await updateRole(row.id, {
    code: row.code,
    name: row.name,
    description: row.description,
    dataScope: row.dataScope,
    dataDeptIds: row.dataDeptIds || []
  })
  ElMessage.success(`角色「${row.name}」的数据范围已更新`)
  load()
  if (diagUserId.value) runDiagnose()
}

async function runDiagnose() {
  if (!diagUserId.value) {
    ElMessage.warning('请选择要诊断的用户')
    return
  }
  diagnosis.value = await diagnoseDataScope(diagUserId.value)
}

onMounted(async () => {
  const [userPage, deptTree] = await Promise.all([
    listUsers({ page: 1, pageSize: 200 }),
    getDepartmentTree()
  ])
  users.value = userPage.list || []
  tree.value = deptTree
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          数据范围按角色配置，用户有多个角色时取<strong>并集</strong>（范围越宽越优先）。
          当前作用于主机资产：列表、连通性探测、Web 终端、文件管理、批量执行与定时任务都会校验。
          范围配置为空时看不到任何数据，避免「配错就等于放开」。
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="roles" border stripe>
        <el-table-column prop="name" label="角色" min-width="140">
          <template #default="{ row }">
            {{ row.name }}
            <span style="color: #6b7280">（{{ row.code }}）</span>
          </template>
        </el-table-column>
        <el-table-column label="数据范围" width="180">
          <template #default="{ row }">
            <el-select v-model="row.dataScope" size="small" style="width: 100%">
              <el-option v-for="opt in scopeOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
            </el-select>
          </template>
        </el-table-column>
        <el-table-column label="指定部门" min-width="300">
          <template #default="{ row }">
            <el-tree-select
              v-if="row.dataScope === 'custom'"
              v-model="row.dataDeptIds"
              :data="tree"
              :props="{ label: 'name', children: 'children' }"
              node-key="id"
              multiple
              show-checkbox
              check-strictly
              size="small"
              style="width: 100%"
              placeholder="选择可见部门"
            />
            <span v-else style="color: #9ca3af">
              {{ scopeOptions.find((o) => o.value === row.dataScope)?.hint }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'role:manage'" link type="primary" @click="saveScope(row)">保存</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card style="margin-top: 12px" header="生效诊断">
      <div class="page-toolbar">
        <el-select v-model="diagUserId" placeholder="选择用户" filterable style="width: 240px">
          <el-option
            v-for="user in users"
            :key="user.id"
            :label="`${user.username}${user.nickname ? '（' + user.nickname + '）' : ''}`"
            :value="user.id"
          />
        </el-select>
        <el-button type="primary" @click="runDiagnose">诊断</el-button>
      </div>

      <el-empty v-if="!diagnosis" description="选择用户后可查看其实际生效的数据范围" :image-size="70" />
      <el-descriptions v-else :column="1" border size="small">
        <el-descriptions-item label="用户">
          {{ diagnosis.user.username }}
          <span style="color: #6b7280">
            所属部门：{{ deptNameMap.get(diagnosis.user.deptId) || (diagnosis.user.deptId ? '#' + diagnosis.user.deptId : '未归属') }}
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="角色与配置">
          <el-tag v-for="role in diagnosis.roles" :key="role.id" size="small" style="margin-right: 6px">
            {{ role.name }}: {{ scopeLabel(role.dataScope) }}
          </el-tag>
          <span v-if="!diagnosis.roles.length" style="color: #dc2626">没有任何角色，只能看到自己录入的数据</span>
        </el-descriptions-item>
        <el-descriptions-item label="生效范围">
          <el-tag v-if="diagnosis.scope.all" type="success" size="small">全部数据</el-tag>
          <template v-else>
            <el-tag v-if="diagnosis.scope.includeSelf" size="small" style="margin-right: 6px">本人录入</el-tag>
            <el-tag
              v-for="name in diagnosis.deptNames"
              :key="name"
              size="small"
              type="info"
              style="margin-right: 4px"
            >
              {{ name }}
            </el-tag>
            <span v-if="!diagnosis.scope.includeSelf && !diagnosis.deptNames.length" style="color: #dc2626">
              范围为空，看不到任何主机
            </span>
          </template>
        </el-descriptions-item>
        <el-descriptions-item label="可见主机">
          {{ diagnosis.visibleHosts }} / {{ diagnosis.totalHosts }}
        </el-descriptions-item>
      </el-descriptions>
    </el-card>
  </div>
</template>
