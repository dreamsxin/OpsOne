<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createFixedAsset,
  deleteFixedAsset,
  getDepartmentTree,
  getFixedAssetStats,
  listFixedAssets,
  listHosts,
  updateFixedAsset,
  type DeptNode,
  type FixedAsset,
  type FixedAssetStats,
  type Host
} from '@/api'

const loading = ref(false)
const rows = ref<FixedAsset[]>([])
const total = ref(0)
const stats = ref<FixedAssetStats | null>(null)
const query = reactive({
  page: 1,
  pageSize: 20,
  keyword: '',
  category: '',
  status: '',
  warrantyDays: ''
})

const hosts = ref<Host[]>([])
const deptTree = ref<DeptNode[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  category: 'server' as FixedAsset['category'],
  sn: '',
  model: '',
  vendor: '',
  location: '',
  owner: '',
  hostId: 0,
  deptId: 0,
  status: 'in_use' as FixedAsset['status'],
  purchaseDate: '',
  purchasePrice: 0,
  warrantyEnd: '',
  remark: ''
})

const categoryOptions = [
  { value: 'server', label: '服务器' },
  { value: 'network', label: '网络设备' },
  { value: 'storage', label: '存储设备' },
  { value: 'terminal', label: '办公终端' },
  { value: 'other', label: '其他' }
]

const statusOptions = [
  { value: 'in_use', label: '在用', type: 'success' as const },
  { value: 'idle', label: '闲置', type: 'info' as const },
  { value: 'repair', label: '维修中', type: 'warning' as const },
  { value: 'scrapped', label: '已报废', type: 'danger' as const }
]

const rules = {
  name: [{ required: true, message: '请输入资产名称', trigger: 'blur' }]
}

const categoryLabel = (v: string) => categoryOptions.find((o) => o.value === v)?.label || v
const statusMeta = (v: string) => statusOptions.find((o) => o.value === v)

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

function hostName(id: number) {
  return hosts.value.find((h) => h.id === id)?.name || `#${id}`
}

// 到保天数：负数表示已过保
function warrantyDays(row: FixedAsset) {
  if (!row.warrantyEnd) return null
  const end = new Date(row.warrantyEnd).getTime()
  return Math.ceil((end - Date.now()) / 86400000)
}

async function load() {
  loading.value = true
  try {
    const [data, s] = await Promise.all([listFixedAssets(query), getFixedAssetStats()])
    rows.value = data.list || []
    total.value = data.total
    stats.value = s
  } finally {
    loading.value = false
  }
}

function filterExpiring() {
  query.warrantyDays = query.warrantyDays === '30' ? '' : '30'
  query.page = 1
  load()
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    category: 'server',
    sn: '',
    model: '',
    vendor: '',
    location: '',
    owner: '',
    hostId: 0,
    deptId: 0,
    status: 'in_use',
    purchaseDate: '',
    purchasePrice: 0,
    warrantyEnd: '',
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: FixedAsset) {
  editingId.value = row.id
  Object.assign(form, {
    ...row,
    purchaseDate: row.purchaseDate ? row.purchaseDate.slice(0, 10) : '',
    warrantyEnd: row.warrantyEnd ? row.warrantyEnd.slice(0, 10) : ''
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateFixedAsset(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createFixedAsset({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: FixedAsset) {
  await ElMessageBox.confirm(`确认删除资产「${row.name}」？`, '危险操作', { type: 'warning' })
  await deleteFixedAsset(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  const [hostPage, tree] = await Promise.all([
    listHosts({ page: 1, pageSize: 200 }),
    getDepartmentTree()
  ])
  hosts.value = hostPage.list || []
  deptTree.value = tree
  load()
})
</script>

<template>
  <div class="page">
    <el-row :gutter="12">
      <el-col :span="5">
        <el-card class="stat-card" shadow="never">
          <div class="value">{{ stats?.total ?? 0 }}</div>
          <div class="label">资产总数</div>
        </el-card>
      </el-col>
      <el-col :span="5">
        <el-card class="stat-card" shadow="never">
          <div class="value" style="color: #16a34a">{{ stats?.inUse ?? 0 }}</div>
          <div class="label">在用 / 闲置 {{ stats?.idle ?? 0 }}</div>
        </el-card>
      </el-col>
      <el-col :span="5">
        <el-card class="stat-card" shadow="hover" style="cursor: pointer" @click="filterExpiring">
          <div class="value" style="color: #d97706">{{ stats?.expiring30 ?? 0 }}</div>
          <div class="label">30 天内到保（点击筛选）</div>
        </el-card>
      </el-col>
      <el-col :span="4">
        <el-card class="stat-card" shadow="never">
          <div class="value" style="color: #dc2626">{{ stats?.expired ?? 0 }}</div>
          <div class="label">已过保</div>
        </el-card>
      </el-col>
      <el-col :span="5">
        <el-card class="stat-card" shadow="never">
          <div class="value">{{ (stats?.totalPrice ?? 0).toLocaleString() }}</div>
          <div class="label">资产原值合计</div>
        </el-card>
      </el-col>
    </el-row>

    <el-card style="margin-top: 12px">
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / SN / 型号 / 位置"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.category" placeholder="类别" clearable style="width: 140px">
          <el-option v-for="item in categoryOptions" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option v-for="item in statusOptions" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-tag v-if="query.warrantyDays" closable type="warning" @close="((query.warrantyDays = ''), load())">
          仅看 30 天内到保
        </el-tag>
        <div class="grow"></div>
        <el-button v-perm="'asset:manage'" type="primary" @click="openCreate">新增资产</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="资产名称" min-width="150" />
        <el-table-column label="类别" width="110">
          <template #default="{ row }">{{ categoryLabel(row.category) }}</template>
        </el-table-column>
        <el-table-column prop="sn" label="SN" min-width="140" show-overflow-tooltip />
        <el-table-column prop="model" label="型号" min-width="130" show-overflow-tooltip />
        <el-table-column prop="location" label="位置" min-width="120" show-overflow-tooltip />
        <el-table-column prop="owner" label="使用人" width="100" />
        <el-table-column label="关联主机" min-width="120">
          <template #default="{ row }">
            <span v-if="row.hostId">{{ hostName(row.hostId) }}</span>
            <span v-else style="color: #9ca3af">-</span>
          </template>
        </el-table-column>
        <el-table-column label="归属部门" min-width="110">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta(row.status)?.type || 'info'">
              {{ statusMeta(row.status)?.label || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="保修" width="130">
          <template #default="{ row }">
            <span v-if="!row.warrantyEnd" style="color: #9ca3af">-</span>
            <el-tag v-else-if="warrantyDays(row)! < 0" size="small" type="danger">已过保</el-tag>
            <el-tag v-else-if="warrantyDays(row)! <= 30" size="small" type="warning">
              {{ warrantyDays(row) }} 天
            </el-tag>
            <span v-else>{{ row.warrantyEnd.slice(0, 10) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'asset:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'asset:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        :page-size="query.pageSize"
        @current-change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑资产' : '新增资产'" width="620px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="资产名称" prop="name">
              <el-input v-model="form.name" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="类别">
              <el-select v-model="form.category" style="width: 100%">
                <el-option
                  v-for="item in categoryOptions"
                  :key="item.value"
                  :label="item.label"
                  :value="item.value"
                />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="SN">
              <el-input v-model="form.sn" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="型号">
              <el-input v-model="form.model" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="厂商">
              <el-input v-model="form.vendor" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="位置">
              <el-input v-model="form.location" placeholder="如 机房 A-12 机柜" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="使用人">
              <el-input v-model="form.owner" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="状态">
              <el-select v-model="form.status" style="width: 100%">
                <el-option
                  v-for="item in statusOptions"
                  :key="item.value"
                  :label="item.label"
                  :value="item.value"
                />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="关联主机">
              <el-select v-model="form.hostId" filterable style="width: 100%">
                <el-option label="不关联" :value="0" />
                <el-option
                  v-for="host in hosts"
                  :key="host.id"
                  :label="`${host.name}（${host.address}）`"
                  :value="host.id"
                />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
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
          </el-col>
          <el-col :span="12">
            <el-form-item label="采购日期">
              <el-date-picker
                v-model="form.purchaseDate"
                type="date"
                value-format="YYYY-MM-DD"
                style="width: 100%"
              />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="保修到期">
              <el-date-picker
                v-model="form.warrantyEnd"
                type="date"
                value-format="YYYY-MM-DD"
                style="width: 100%"
              />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="采购价">
              <el-input-number v-model="form.purchasePrice" :min="0" :precision="2" style="width: 100%" />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="备注">
              <el-input v-model="form.remark" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
