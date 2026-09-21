<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createPurchaseOrder,
  deletePurchaseOrder,
  getDepartmentTree,
  getPurchaseOrder,
  listPurchaseOrders,
  receivePurchaseOrder,
  updatePurchaseOrder,
  type DeptNode,
  type PurchaseItem,
  type PurchaseOrder
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const rows = ref<PurchaseOrder[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, keyword: '', status: '' })
const deptTree = ref<DeptNode[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  orderNo: '',
  title: '',
  vendor: '',
  applicant: '',
  status: 'draft' as PurchaseOrder['status'],
  deptId: 0,
  orderDate: '',
  expectedDate: '',
  remark: '',
  items: [] as PurchaseItem[]
})

const detailVisible = ref(false)
const current = ref<PurchaseOrder | null>(null)

const statusOptions = [
  { value: 'draft', label: '草稿', type: 'info' as const },
  { value: 'ordered', label: '已下单', type: 'primary' as const },
  { value: 'received', label: '已收货', type: 'success' as const },
  { value: 'cancelled', label: '已取消', type: 'danger' as const }
]

const categoryOptions = [
  { value: 'server', label: '服务器' },
  { value: 'network', label: '网络设备' },
  { value: 'storage', label: '存储设备' },
  { value: 'terminal', label: '办公终端' },
  { value: 'other', label: '其他' }
]

const statusMeta = (v: string) => statusOptions.find((o) => o.value === v)

const rules = {
  orderNo: [{ required: true, message: '请输入采购单号', trigger: 'blur' }],
  title: [{ required: true, message: '请输入采购标题', trigger: 'blur' }]
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

// 表单内实时汇总，和后端按明细算出来的金额保持一致
const formAmount = computed(() =>
  form.items.reduce((sum, item) => sum + (item.quantity || 0) * (item.unitPrice || 0), 0)
)

async function load() {
  loading.value = true
  try {
    const data = await listPurchaseOrders(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function addItem() {
  form.items.push({
    name: '',
    category: 'server',
    model: '',
    vendor: '',
    quantity: 1,
    unitPrice: 0,
    remark: ''
  })
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    orderNo: `PO-${new Date().toISOString().slice(0, 10).replace(/-/g, '')}-01`,
    title: '',
    vendor: '',
    applicant: '',
    status: 'draft',
    deptId: 0,
    orderDate: new Date().toISOString().slice(0, 10),
    expectedDate: '',
    remark: '',
    items: []
  })
  addItem()
  dialogVisible.value = true
}

async function openEdit(row: PurchaseOrder) {
  const detail = await getPurchaseOrder(row.id)
  editingId.value = detail.id
  Object.assign(form, {
    orderNo: detail.orderNo,
    title: detail.title,
    vendor: detail.vendor,
    applicant: detail.applicant,
    status: detail.status,
    deptId: detail.deptId,
    orderDate: detail.orderDate ? detail.orderDate.slice(0, 10) : '',
    expectedDate: detail.expectedDate ? detail.expectedDate.slice(0, 10) : '',
    remark: detail.remark,
    items: detail.items || []
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.items.length || form.items.some((item) => !item.name)) {
    ElMessage.warning('请至少填写一条明细，且名称不能为空')
    return
  }

  if (editingId.value) {
    await updatePurchaseOrder(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createPurchaseOrder({ ...form })
    ElMessage.success('已创建')
  }
  dialogVisible.value = false
  load()
}

async function openDetail(row: PurchaseOrder) {
  current.value = await getPurchaseOrder(row.id)
  detailVisible.value = true
}

async function receive(row: PurchaseOrder) {
  const detail = await getPurchaseOrder(row.id)
  const count = (detail.items || []).reduce((sum, item) => sum + (item.quantity || 0), 0)

  const confirmed = await ElMessageBox.confirm(
    `确认收货？勾选「同时生成固定资产」会按明细数量生成 ${count} 条资产台账记录。`,
    '收货入库',
    {
      distinguishCancelAndClose: true,
      confirmButtonText: '收货并生成资产',
      cancelButtonText: '仅标记收货'
    }
  )
    .then(() => true)
    .catch((action) => {
      if (action === 'cancel') return false
      throw action
    })

  const { value: location } = confirmed
    ? await ElMessageBox.prompt('请输入入库位置（可留空）', '入库位置', { inputValue: '' })
    : { value: '' }

  const res = await receivePurchaseOrder(row.id, {
    receivedDate: new Date().toISOString().slice(0, 10),
    createAssets: confirmed,
    location
  })
  ElMessage.success(
    res.createdAssets > 0 ? `已收货，生成 ${res.createdAssets} 条固定资产` : '已标记收货'
  )
  detailVisible.value = false
  load()
}

async function remove(row: PurchaseOrder) {
  await ElMessageBox.confirm(`确认删除采购单「${row.orderNo}」？`, '危险操作', { type: 'warning' })
  await deletePurchaseOrder(row.id)
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
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="采购单金额由明细自动汇总；收货时可选择按明细数量生成固定资产台账，数量大于 1 会自动追加序号。已收货的单据不允许修改"
      />

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="单号 / 标题 / 供应商"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option v-for="item in statusOptions" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'purchase:manage'" type="primary" @click="openCreate">新建采购单</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有采购记录">
        <el-table-column prop="orderNo" label="单号" min-width="160" />
        <el-table-column prop="title" label="标题" min-width="180" show-overflow-tooltip />
        <el-table-column prop="vendor" label="供应商" min-width="130" />
        <el-table-column label="金额" width="130">
          <template #default="{ row }">{{ row.amount.toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="归属部门" min-width="120">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta(row.status)?.type || 'info'">
              {{ statusMeta(row.status)?.label || row.status }}
            </el-tag>
            <el-tag v-if="row.assetCreated" size="small" type="success" style="margin-left: 4px">已入库</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="orderDate" label="采购日期" min-width="120">
          <template #default="{ row }">{{ row.orderDate ? row.orderDate.slice(0, 10) : '-' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
            <el-button
              v-if="row.status !== 'received' && row.status !== 'cancelled'"
              v-perm="'purchase:manage'"
              link
              type="success"
              @click="receive(row)"
            >
              收货
            </el-button>
            <el-button
              v-if="row.status !== 'received'"
              v-perm="'purchase:manage'"
              link
              type="primary"
              @click="openEdit(row)"
            >
              编辑
            </el-button>
            <el-button v-perm="'purchase:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <Pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="total"
        @change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑采购单' : '新建采购单'" width="760px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="单号" prop="orderNo">
              <el-input v-model="form.orderNo" :disabled="!!editingId" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="标题" prop="title">
              <el-input v-model="form.title" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="供应商">
              <el-input v-model="form.vendor" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="申请人">
              <el-input v-model="form.applicant" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="状态">
              <el-select v-model="form.status" style="width: 100%">
                <el-option
                  v-for="item in statusOptions.filter((s) => s.value !== 'received')"
                  :key="item.value"
                  :label="item.label"
                  :value="item.value"
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
              <el-date-picker v-model="form.orderDate" type="date" value-format="YYYY-MM-DD" style="width: 100%" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="预计到货">
              <el-date-picker
                v-model="form.expectedDate"
                type="date"
                value-format="YYYY-MM-DD"
                style="width: 100%"
              />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="备注">
              <el-input v-model="form.remark" />
            </el-form-item>
          </el-col>
        </el-row>

        <el-divider content-position="left">
          采购明细（合计 {{ formAmount.toLocaleString() }}）
        </el-divider>
        <el-table :data="form.items" border size="small">
          <el-table-column label="名称" min-width="140">
            <template #default="{ row }">
              <el-input v-model="row.name" size="small" />
            </template>
          </el-table-column>
          <el-table-column label="类别" width="130">
            <template #default="{ row }">
              <el-select v-model="row.category" size="small" style="width: 100%">
                <el-option
                  v-for="item in categoryOptions"
                  :key="item.value"
                  :label="item.label"
                  :value="item.value"
                />
              </el-select>
            </template>
          </el-table-column>
          <el-table-column label="型号" min-width="120">
            <template #default="{ row }">
              <el-input v-model="row.model" size="small" />
            </template>
          </el-table-column>
          <el-table-column label="数量" width="100">
            <template #default="{ row }">
              <el-input-number v-model="row.quantity" :min="1" size="small" controls-position="right" style="width: 100%" />
            </template>
          </el-table-column>
          <el-table-column label="单价" width="120">
            <template #default="{ row }">
              <el-input-number v-model="row.unitPrice" :min="0" :precision="2" size="small" controls-position="right" style="width: 100%" />
            </template>
          </el-table-column>
          <el-table-column label="小计" width="110">
            <template #default="{ row }">
              {{ ((row.quantity || 0) * (row.unitPrice || 0)).toLocaleString() }}
            </template>
          </el-table-column>
          <el-table-column label="操作" width="70">
            <template #default="{ $index }">
              <el-button link type="danger" @click="form.items.splice($index, 1)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-button size="small" style="margin-top: 8px" @click="addItem">添加明细</el-button>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="detailVisible" :title="`采购单 · ${current?.orderNo ?? ''}`" size="55%">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item label="标题">{{ current.title }}</el-descriptions-item>
        <el-descriptions-item label="供应商">{{ current.vendor || '-' }}</el-descriptions-item>
        <el-descriptions-item label="申请人">{{ current.applicant || '-' }}</el-descriptions-item>
        <el-descriptions-item label="金额">{{ current.amount.toLocaleString() }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag size="small" :type="statusMeta(current.status)?.type || 'info'">
            {{ statusMeta(current.status)?.label }}
          </el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="是否已入库">
          {{ current.assetCreated ? '已生成固定资产' : '未生成' }}
        </el-descriptions-item>
        <el-descriptions-item label="采购 / 预计 / 收货">
          {{ current.orderDate ? current.orderDate.slice(0, 10) : '-' }} ·
          {{ current.expectedDate ? current.expectedDate.slice(0, 10) : '-' }} ·
          {{ current.receivedDate ? current.receivedDate.slice(0, 10) : '-' }}
        </el-descriptions-item>
        <el-descriptions-item label="备注">{{ current.remark || '-' }}</el-descriptions-item>
      </el-descriptions>

      <el-divider content-position="left">明细</el-divider>
      <el-table :data="current?.items || []" border size="small">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="model" label="型号" min-width="120" />
        <el-table-column prop="quantity" label="数量" width="80" />
        <el-table-column label="单价" width="110">
          <template #default="{ row }">{{ row.unitPrice.toLocaleString() }}</template>
        </el-table-column>
        <el-table-column label="小计" width="120">
          <template #default="{ row }">{{ (row.quantity * row.unitPrice).toLocaleString() }}</template>
        </el-table-column>
      </el-table>
    </el-drawer>
  </div>
</template>
