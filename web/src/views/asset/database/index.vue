<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  checkDatabase,
  createDatabase,
  deleteDatabase,
  getDepartmentTree,
  listDatabases,
  listTags,
  updateDatabase,
  type DBInstance,
  type DeptNode,
  type Tag
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const rows = ref<DBInstance[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, keyword: '', type: '', env: '' })

const tags = ref<Tag[]>([])
const deptTree = ref<DeptNode[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  type: 'mysql' as DBInstance['type'],
  address: '',
  port: 3306,
  username: '',
  secret: '',
  dbName: '',
  version: '',
  env: 'dev' as DBInstance['env'],
  deptId: 0,
  tags: '',
  remark: ''
})

const typeOptions = [
  { value: 'mysql', label: 'MySQL', port: 3306 },
  { value: 'postgres', label: 'PostgreSQL', port: 5432 },
  { value: 'redis', label: 'Redis', port: 6379 },
  { value: 'mongo', label: 'MongoDB', port: 27017 },
  { value: 'other', label: '其他', port: 0 }
]

const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  online: { text: '端口可达', type: 'success' },
  offline: { text: '不可达', type: 'danger' },
  unknown: { text: '未探测', type: 'info' }
}

const rules = {
  name: [{ required: true, message: '请输入实例名称', trigger: 'blur' }],
  address: [{ required: true, message: '请输入地址', trigger: 'blur' }]
}

// tags 字段是逗号分隔字符串，表单里用数组操作更顺手
const tagList = computed({
  get: () => (form.tags ? form.tags.split(',').map((t) => t.trim()).filter(Boolean) : []),
  set: (value: string[]) => (form.tags = value.join(','))
})

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

async function load() {
  loading.value = true
  try {
    const data = await listDatabases(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function onTypeChange(value: string) {
  const preset = typeOptions.find((t) => t.value === value)
  if (preset?.port) form.port = preset.port
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    type: 'mysql',
    address: '',
    port: 3306,
    username: '',
    secret: '',
    dbName: '',
    version: '',
    env: 'dev',
    deptId: 0,
    tags: '',
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: DBInstance) {
  editingId.value = row.id
  Object.assign(form, { ...row, secret: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateDatabase(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createDatabase({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function check(row: DBInstance) {
  const res = await checkDatabase(row.id)
  if (res.status === 'online') {
    ElMessage.success(`端口可达（${res.costMs}ms）· ${res.note}`)
  } else {
    ElMessage.error(`不可达：${res.detail}`)
  }
  load()
}

async function remove(row: DBInstance) {
  await ElMessageBox.confirm(`确认删除数据库资产「${row.name}」？`, '危险操作', { type: 'warning' })
  await deleteDatabase(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  const [tagList2, tree] = await Promise.all([listTags(), getDepartmentTree()])
  tags.value = tagList2
  deptTree.value = tree
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        type="warning"
        :closable="false"
        style="margin-bottom: 12px"
        title="连通性探测只检测 TCP 端口可达性，不做数据库协议握手与账号验证；端口通不代表账号密码可用。凭据与主机一致为明文存储"
      />

      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 地址 / 库名 / 标签"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.type" placeholder="类型" clearable style="width: 140px">
          <el-option v-for="item in typeOptions" :key="item.value" :label="item.label" :value="item.value" />
        </el-select>
        <el-select v-model="query.env" placeholder="环境" clearable style="width: 120px">
          <el-option label="开发" value="dev" />
          <el-option label="测试" value="test" />
          <el-option label="生产" value="prod" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'db:manage'" type="primary" @click="openCreate">新增实例</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="实例" min-width="140" />
        <el-table-column label="类型" width="110">
          <template #default="{ row }">
            <el-tag size="small">{{ row.type }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="连接" min-width="180">
          <template #default="{ row }">{{ row.address }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column prop="dbName" label="库名" min-width="120" />
        <el-table-column prop="version" label="版本" width="100" />
        <el-table-column label="环境" width="90">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.env === 'prod' ? 'danger' : row.env === 'test' ? 'warning' : 'info'"
            >
              {{ row.env }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="归属部门" min-width="120">
          <template #default="{ row }">
            <span v-if="row.deptId">{{ deptNameMap.get(row.deptId) || '#' + row.deptId }}</span>
            <span v-else style="color: #9ca3af">未归属</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="tags" label="标签" min-width="130" show-overflow-tooltip />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="check(row)">探测</el-button>
            <el-button v-perm="'db:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'db:manage'" link type="danger" @click="remove(row)">删除</el-button>
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

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑实例' : '新增实例'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="实例名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%" @change="onTypeChange">
            <el-option v-for="item in typeOptions" :key="item.value" :label="item.label" :value="item.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="地址" prop="address">
          <el-input v-model="form.address" placeholder="IP 或域名" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="账号">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="form.secret"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '可选'"
          />
        </el-form-item>
        <el-form-item label="库名">
          <el-input v-model="form.dbName" />
        </el-form-item>
        <el-form-item label="版本">
          <el-input v-model="form.version" placeholder="如 8.0.36" />
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.env">
            <el-option label="开发" value="dev" />
            <el-option label="测试" value="test" />
            <el-option label="生产" value="prod" />
          </el-select>
        </el-form-item>
        <el-form-item label="归属部门">
          <el-tree-select
            v-model="form.deptId"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            check-strictly
            style="width: 100%"
            placeholder="未归属（仅「全部数据」范围可见）"
          />
        </el-form-item>
        <el-form-item label="标签">
          <el-select
            v-model="tagList"
            multiple
            filterable
            allow-create
            default-first-option
            style="width: 100%"
            placeholder="从字典选择或直接输入"
          >
            <el-option v-for="tag in tags" :key="tag.id" :label="tag.name" :value="tag.name" />
          </el-select>
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
