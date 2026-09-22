<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createCloudAccount,
  deleteCloudAccount,
  getDepartmentTree,
  listCloudAccounts,
  updateCloudAccount,
  type CloudAccount,
  type DeptNode
} from '@/api'

const loading = ref(false)
const rows = ref<CloudAccount[]>([])
const deptTree = ref<DeptNode[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  provider: 'aliyun' as CloudAccount['provider'],
  accessKeyId: '',
  accessKeySecret: '',
  region: '',
  accountId: '',
  deptId: 0,
  enabled: true,
  remark: ''
})

const providerOptions = [
  { value: 'aliyun', label: '阿里云' },
  { value: 'tencent', label: '腾讯云' },
  { value: 'huawei', label: '华为云' },
  { value: 'aws', label: 'AWS' },
  { value: 'other', label: '其他' }
]

const providerLabel = (v: string) => providerOptions.find((o) => o.value === v)?.label || v

const rules = {
  name: [{ required: true, message: '请输入账号名称', trigger: 'blur' }]
}

const deptNameMap = computed(() => {
  const map = new Map<number, string>()
  const walk = (nodes: DeptNode[]) => {
    for (const node of nodes) {
      map.set(node.id, node.name)
      if (node.children?.length) walk(node.children)
    }
  }
  walk(deptTree.value)
  return map
})

// AK 只展示前后各 4 位，避免整串泄露在屏幕上
function maskKey(key: string) {
  if (!key) return '-'
  if (key.length <= 10) return key.slice(0, 2) + '****'
  return `${key.slice(0, 4)}****${key.slice(-4)}`
}

async function load() {
  loading.value = true
  try {
    rows.value = await listCloudAccounts()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    provider: 'aliyun',
    accessKeyId: '',
    accessKeySecret: '',
    region: '',
    accountId: '',
    deptId: 0,
    enabled: true,
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: CloudAccount) {
  editingId.value = row.id
  Object.assign(form, { ...row, accessKeySecret: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateCloudAccount(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createCloudAccount({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function toggleEnabled(row: CloudAccount) {
  await updateCloudAccount(row.id, {
    name: row.name,
    provider: row.provider,
    accessKeyId: row.accessKeyId,
    region: row.region,
    accountId: row.accountId,
    deptId: row.deptId,
    enabled: row.enabled,
    remark: row.remark
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
}

async function remove(row: CloudAccount) {
  await ElMessageBox.confirm(`确认删除云账号「${row.name}」？`, '危险操作', { type: 'warning' })
  await deleteCloudAccount(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  deptTree.value = await getDepartmentTree()
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          这里登记云账号与密钥；阿里云账号可以在<strong>「云资源同步」</strong>页把 ECS 与云解析域名拉进平台
          （只调只读接口，不做任何云上变更）。腾讯云 / 华为云 / AWS 目前只登记不同步。
          AccessKeySecret 配了 <code>OPS_SECRET_KEY</code> 时加密落库、接口不回传，请按最小权限申请只读子账号密钥。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-button @click="load">刷新</el-button>
        <div class="grow"></div>
        <el-button v-perm="'cloud:manage'" type="primary" @click="openCreate">新增云账号</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有登记云账号">
        <el-table-column prop="name" label="账号名称" min-width="150" />
        <el-table-column label="云厂商" width="110">
          <template #default="{ row }">
            <el-tag size="small">{{ providerLabel(row.provider) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="AccessKey" min-width="170">
          <template #default="{ row }">
            <code style="font-size: 12px">{{ maskKey(row.accessKeyId) }}</code>
          </template>
        </el-table-column>
        <el-table-column prop="region" label="区域" width="130" />
        <el-table-column prop="accountId" label="云账号 ID" min-width="140" />
        <el-table-column label="归属部门" min-width="120">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'cloud:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'cloud:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑云账号' : '新增云账号'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="110px">
        <el-form-item label="账号名称" prop="name">
          <el-input v-model="form.name" placeholder="如 阿里云-生产主账号" />
        </el-form-item>
        <el-form-item label="云厂商">
          <el-select v-model="form.provider" style="width: 100%">
            <el-option v-for="item in providerOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="AccessKeyID">
          <el-input v-model="form.accessKeyId" />
        </el-form-item>
        <el-form-item label="AccessKeySecret">
          <el-input
            v-model="form.accessKeySecret"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : ''"
          />
        </el-form-item>
        <el-form-item label="区域">
          <el-input v-model="form.region" placeholder="如 cn-hangzhou" />
        </el-form-item>
        <el-form-item label="云账号 ID">
          <el-input v-model="form.accountId" placeholder="便于与账单对账" />
        </el-form-item>
        <el-form-item label="归属部门">
          <el-tree-select
            v-model="form.deptId"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            check-strictly
            style="width: 100%"
            placeholder="未归属"
          />
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
  </div>
</template>
