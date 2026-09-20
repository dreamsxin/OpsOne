<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createInventoryBatch,
  deleteInventoryBatch,
  finishInventoryBatch,
  getDepartmentTree,
  getInventoryBatch,
  listInventoryBatches,
  updateInventoryItem,
  type DeptNode,
  type InventoryBatch,
  type InventoryItem
} from '@/api'

const loading = ref(false)
const rows = ref<InventoryBatch[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, status: '' })
const deptTree = ref<DeptNode[]>([])

const dialogVisible = ref(false)
const formRef = ref<FormInstance>()
const form = reactive({ name: '', scopeDeptId: 0, remark: '' })

const detailVisible = ref(false)
const current = ref<InventoryBatch | null>(null)
const items = ref<InventoryItem[]>([])
const itemFilter = ref('')

const rules = {
  name: [{ required: true, message: '请输入批次名称', trigger: 'blur' }]
}

const resultMeta: Record<string, { text: string; type: 'info' | 'success' | 'danger' | 'warning' }> = {
  pending: { text: '待盘点', type: 'info' },
  matched: { text: '正常', type: 'success' },
  missing: { text: '缺失', type: 'danger' },
  moved: { text: '位置变更', type: 'warning' }
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

const filteredItems = computed(() =>
  itemFilter.value ? items.value.filter((item) => item.result === itemFilter.value) : items.value
)

const progress = (batch: InventoryBatch) =>
  batch.totalCount ? Math.round((batch.checkedCount / batch.totalCount) * 100) : 0

async function load() {
  loading.value = true
  try {
    const data = await listInventoryBatches(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function openCreate() {
  Object.assign(form, { name: '', scopeDeptId: 0, remark: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  const batch = await createInventoryBatch({ ...form })
  ElMessage.success(`已创建批次，快照 ${batch.totalCount} 件资产`)
  dialogVisible.value = false
  load()
  openDetail(batch)
}

async function openDetail(row: InventoryBatch) {
  const detail = await getInventoryBatch(row.id)
  current.value = detail
  items.value = detail.items || []
  itemFilter.value = ''
  detailVisible.value = true
}

async function mark(item: InventoryItem, result: 'matched' | 'missing' | 'moved') {
  let actualLocation = ''
  if (result === 'moved') {
    const { value } = await ElMessageBox.prompt('请输入实际位置', '位置变更', {
      inputValue: item.expectLocation
    })
    actualLocation = value
  }

  await updateInventoryItem(item.id, { result, actualLocation, note: '' })
  item.result = result
  item.actualLocation = actualLocation
  ElMessage.success('已登记')

  if (current.value) {
    const detail = await getInventoryBatch(current.value.id)
    current.value = detail
    items.value = detail.items || []
    load()
  }
}

async function finish(row: InventoryBatch) {
  const pending = row.totalCount - row.checkedCount
  const extra = pending > 0 ? `还有 ${pending} 件未盘点，将统一标记为缺失。` : ''
  await ElMessageBox.confirm(`确认完成批次「${row.name}」？${extra}`, '完成盘点', { type: 'warning' })
  const updated = await finishInventoryBatch(row.id)
  ElMessage.success(`已完成：正常 ${updated.matchedCount}，缺失 ${updated.missingCount}，位置变更 ${updated.movedCount}`)
  detailVisible.value = false
  load()
}

async function remove(row: InventoryBatch) {
  await ElMessageBox.confirm(`确认删除批次「${row.name}」？盘点明细会一并删除`, '危险操作', {
    type: 'warning'
  })
  await deleteInventoryBatch(row.id)
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
        title="创建批次时会对当前范围内的固定资产做快照（已报废资产不参与），之后新增或删除资产不影响已建批次，保证盘点结果可复查"
      />

      <div class="page-toolbar">
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option label="进行中" value="ongoing" />
          <el-option label="已完成" value="finished" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'inventory:manage'" type="primary" @click="openCreate">新建盘点批次</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有盘点批次">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="批次" min-width="160" />
        <el-table-column label="范围" min-width="130">
          <template #default="{ row }">
            <span v-if="row.scopeDeptId">{{ deptNameMap.get(row.scopeDeptId) || '#' + row.scopeDeptId }}</span>
            <span v-else style="color: #6b7280">全部可见资产</span>
          </template>
        </el-table-column>
        <el-table-column label="进度" width="170">
          <template #default="{ row }">
            <el-progress :percentage="progress(row)" :stroke-width="12" />
            <span style="color: #6b7280; font-size: 12px">
              {{ row.checkedCount }} / {{ row.totalCount }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="结果" min-width="200">
          <template #default="{ row }">
            <el-tag size="small" type="success">正常 {{ row.matchedCount }}</el-tag>
            <el-tag size="small" type="danger" style="margin-left: 4px">缺失 {{ row.missingCount }}</el-tag>
            <el-tag size="small" type="warning" style="margin-left: 4px">变更 {{ row.movedCount }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'finished' ? 'info' : 'success'">
              {{ row.status === 'finished' ? '已完成' : '进行中' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="发起人" width="110" />
        <el-table-column prop="startedAt" label="开始时间" min-width="170" />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">盘点</el-button>
            <el-button
              v-if="row.status === 'ongoing'"
              v-perm="'inventory:manage'"
              link
              type="success"
              @click="finish(row)"
            >
              完成
            </el-button>
            <el-button v-perm="'inventory:manage'" link type="danger" @click="remove(row)">删除</el-button>
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

    <el-drawer v-model="detailVisible" :title="`盘点明细 · ${current?.name ?? ''}`" size="62%">
      <div class="page-toolbar">
        <el-select v-model="itemFilter" placeholder="按结果筛选" clearable style="width: 150px">
          <el-option label="待盘点" value="pending" />
          <el-option label="正常" value="matched" />
          <el-option label="缺失" value="missing" />
          <el-option label="位置变更" value="moved" />
        </el-select>
        <el-tag v-if="current?.status === 'finished'" type="info">批次已完成，只读</el-tag>
        <div class="grow"></div>
        <el-button
          v-if="current?.status === 'ongoing'"
          v-perm="'inventory:manage'"
          type="success"
          @click="finish(current!)"
        >
          完成批次
        </el-button>
      </div>

      <el-table :data="filteredItems" border size="small" empty-text="没有符合条件的明细">
        <el-table-column prop="assetName" label="资产" min-width="150" />
        <el-table-column prop="sn" label="SN" min-width="130" show-overflow-tooltip />
        <el-table-column prop="expectLocation" label="台账位置" min-width="130" show-overflow-tooltip />
        <el-table-column prop="actualLocation" label="实际位置" min-width="130" show-overflow-tooltip />
        <el-table-column label="结果" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="resultMeta[row.result]?.type || 'info'">
              {{ resultMeta[row.result]?.text || row.result }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="checkedBy" label="盘点人" width="100" />
        <el-table-column label="登记" width="200" fixed="right">
          <template #default="{ row }">
            <template v-if="current?.status === 'ongoing'">
              <el-button v-perm="'inventory:manage'" link type="success" @click="mark(row, 'matched')">
                正常
              </el-button>
              <el-button v-perm="'inventory:manage'" link type="warning" @click="mark(row, 'moved')">
                位置变更
              </el-button>
              <el-button v-perm="'inventory:manage'" link type="danger" @click="mark(row, 'missing')">
                缺失
              </el-button>
            </template>
            <span v-else style="color: #9ca3af">-</span>
          </template>
        </el-table-column>
      </el-table>
    </el-drawer>

    <el-dialog v-model="dialogVisible" title="新建盘点批次" width="480px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="批次名称" prop="name">
          <el-input v-model="form.name" placeholder="如 2026Q1 机房盘点" />
        </el-form-item>
        <el-form-item label="盘点范围">
          <el-tree-select
            v-model="form.scopeDeptId"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            check-strictly
            style="width: 100%"
            placeholder="全部可见资产"
          />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">创建并快照</el-button>
      </template>
    </el-dialog>
  </div>
</template>
