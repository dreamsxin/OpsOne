<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createResourceGrant,
  deleteResourceGrant,
  diagnoseResourceGrants,
  listDatabases,
  listHosts,
  listResourceGrants,
  listRoles,
  listUsers,
  updateResourceGrant,
  type DBInstance,
  type GrantDiagnosis,
  type Host,
  type ResourceGrant,
  type Role,
  type User
} from '@/api'
import Pagination from '@/components/Pagination.vue'


const loading = ref(false)
const rows = ref<ResourceGrant[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, subjectType: '', resourceType: '', activeOnly: '' })

const users = ref<User[]>([])
const roles = ref<Role[]>([])
const hosts = ref<Host[]>([])
const databases = ref<DBInstance[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  subjectType: 'user' as 'user' | 'role',
  subjectId: undefined as number | undefined,
  resourceType: 'host' as 'host' | 'database',
  resourceId: undefined as number | undefined,
  actionList: [] as string[],
  allActions: true,
  expiresAt: '',
  remark: ''
})

const diagUserId = ref<number | undefined>(undefined)
const diagnosis = ref<GrantDiagnosis | null>(null)

const actionOptions = [
  { value: 'terminal', label: 'Web 终端' },
  { value: 'file', label: '文件管理' },
  { value: 'exec', label: '批量执行 / 定时任务' },
  { value: 'manage', label: '编辑与删除' }
]

const actionLabel = (v: string) =>
  v === '*' ? '全部动作' : actionOptions.find((o) => o.value === v)?.label || v

const rules = {
  subjectId: [{ required: true, message: '请选择授权主体', trigger: 'change' }],
  resourceId: [{ required: true, message: '请选择资源', trigger: 'change' }]
}

const subjectOptions = computed(() =>
  form.subjectType === 'role'
    ? roles.value.map((r) => ({ id: r.id, label: `${r.name}（${r.code}）` }))
    : users.value.map((u) => ({
        id: u.id,
        label: `${u.username}${u.nickname ? '（' + u.nickname + '）' : ''}`
      }))
)

const resourceOptions = computed(() =>
  form.resourceType === 'database'
    ? databases.value.map((d) => ({ id: d.id, label: `${d.name}（${d.address}:${d.port}）` }))
    : hosts.value.map((host) => ({ id: host.id, label: `${host.name}（${host.address}）` }))
)

function expired(row: ResourceGrant) {
  return !!row.expiresAt && new Date(row.expiresAt).getTime() < Date.now()
}

async function load() {
  loading.value = true
  try {
    const data = await listResourceGrants(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    subjectType: 'user',
    subjectId: undefined,
    resourceType: 'host',
    resourceId: undefined,
    actionList: [],
    allActions: true,
    expiresAt: '',
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: ResourceGrant) {
  editingId.value = row.id
  Object.assign(form, {
    subjectType: row.subjectType,
    subjectId: row.subjectId,
    resourceType: row.resourceType,
    resourceId: row.resourceId,
    actionList: row.actions === '*' ? [] : row.actions.split(',').filter(Boolean),
    allActions: row.actions === '*',
    expiresAt: row.expiresAt ? row.expiresAt.slice(0, 10) : '',
    remark: row.remark
  })
  dialogVisible.value = true
}

function buildPayload() {
  return {
    subjectType: form.subjectType,
    subjectId: form.subjectId,
    resourceType: form.resourceType,
    resourceId: form.resourceId,
    actions: form.allActions ? '*' : form.actionList.join(','),
    expiresAt: form.expiresAt,
    remark: form.remark
  }
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!form.allActions && !form.actionList.length) {
    ElMessage.warning('请至少选择一个动作，或勾选「全部动作」')
    return
  }

  if (editingId.value) {
    await updateResourceGrant(editingId.value, buildPayload())
    ElMessage.success('已更新')
  } else {
    await createResourceGrant(buildPayload())
    ElMessage.success('已授权')
  }
  dialogVisible.value = false
  load()
  if (diagUserId.value) runDiagnose()
}

async function remove(row: ResourceGrant) {
  await ElMessageBox.confirm(
    `确认撤销对「${row.subjectName}」在「${row.resourceName}」上的授权？`,
    '提示',
    { type: 'warning' }
  )
  await deleteResourceGrant(row.id)
  ElMessage.success('已撤销')
  load()
  if (diagUserId.value) runDiagnose()
}

async function runDiagnose() {
  if (!diagUserId.value) {
    ElMessage.warning('请选择要诊断的用户')
    return
  }
  diagnosis.value = await diagnoseResourceGrants(diagUserId.value)
}

onMounted(async () => {
  const [userPage, roleList, hostPage, dbPage] = await Promise.all([
    listUsers({ page: 1, pageSize: 200 }),
    listRoles(),
    listHosts({ page: 1, pageSize: 200 }),
    listDatabases({ page: 1, pageSize: 200 })
  ])
  users.value = userPage.list || []
  roles.value = roleList
  hosts.value = hostPage.list || []
  databases.value = dbPage.list || []
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          资源授权与数据权限是<strong>叠加</strong>关系：数据范围决定默认能看到什么，
          这里额外把指定主机/数据库放开给某个用户或角色，并可限定动作与有效期。
          授权只放开、不收窄 —— 数据范围内已有的权限不受影响。
        </template>
      </el-alert>

      <div class="page-toolbar">
        <el-select v-model="query.subjectType" placeholder="主体类型" clearable style="width: 130px">
          <el-option label="用户" value="user" />
          <el-option label="角色" value="role" />
        </el-select>
        <el-select v-model="query.resourceType" placeholder="资源类型" clearable style="width: 140px">
          <el-option label="主机" value="host" />
          <el-option label="数据库" value="database" />
        </el-select>
        <el-checkbox
          :model-value="query.activeOnly === 'true'"
          @change="((query.activeOnly = $event ? 'true' : ''), (query.page = 1), load())"
        >
          只看未过期
        </el-checkbox>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <div class="grow"></div>
        <el-button v-perm="'grant:manage'" type="primary" @click="openCreate">新增授权</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有资源授权">
        <el-table-column label="主体" min-width="170">
          <template #default="{ row }">
            <el-tag size="small" :type="row.subjectType === 'role' ? 'warning' : 'primary'">
              {{ row.subjectType === 'role' ? '角色' : '用户' }}
            </el-tag>
            <span style="margin-left: 6px">{{ row.subjectName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="资源" min-width="220">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ row.resourceType === 'database' ? '数据库' : '主机' }}</el-tag>
            <span style="margin-left: 6px">{{ row.resourceName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="授权动作" min-width="220">
          <template #default="{ row }">
            <el-tag
              v-for="action in row.actions.split(',')"
              :key="action"
              size="small"
              style="margin-right: 4px"
            >
              {{ actionLabel(action) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="有效期" width="140">
          <template #default="{ row }">
            <span v-if="!row.expiresAt" style="color: #6b7280">长期</span>
            <el-tag v-else-if="expired(row)" size="small" type="danger">已过期</el-tag>
            <span v-else>{{ row.expiresAt.slice(0, 10) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="授权人" width="110" />
        <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'grant:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'grant:manage'" link type="danger" @click="remove(row)">撤销</el-button>
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

      <el-empty v-if="!diagnosis" description="选择用户后可查看其数据范围外额外拿到的主机" :image-size="70" />
      <template v-else>
        <el-descriptions :column="1" border size="small" style="margin-bottom: 12px">
          <el-descriptions-item label="用户">{{ diagnosis.user.username }}</el-descriptions-item>
          <el-descriptions-item label="数据范围内主机">{{ diagnosis.scopedHosts }} 台</el-descriptions-item>
          <el-descriptions-item label="额外授权主机">{{ diagnosis.grantedHosts.length }} 台</el-descriptions-item>
        </el-descriptions>
        <el-table :data="diagnosis.grantedHosts" border size="small" empty-text="没有数据范围外的额外授权">
          <el-table-column prop="hostName" label="主机" min-width="150" />
          <el-table-column prop="address" label="地址" min-width="150" />
          <el-table-column label="可执行动作" min-width="240">
            <template #default="{ row }">
              <el-tag v-for="action in row.actions" :key="action" size="small" style="margin-right: 4px">
                {{ actionLabel(action) }}
              </el-tag>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑授权' : '新增授权'" width="560px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="主体类型">
          <el-radio-group v-model="form.subjectType" :disabled="!!editingId" @change="form.subjectId = undefined">
            <el-radio value="user">用户</el-radio>
            <el-radio value="role">角色</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="主体" prop="subjectId">
          <el-select v-model="form.subjectId" :disabled="!!editingId" filterable style="width: 100%">
            <el-option v-for="item in subjectOptions" :key="item.id" :label="item.label" :value="item.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="资源类型">
          <el-radio-group
            v-model="form.resourceType"
            :disabled="!!editingId"
            @change="form.resourceId = undefined"
          >
            <el-radio value="host">主机</el-radio>
            <el-radio value="database">数据库</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="资源" prop="resourceId">
          <el-select v-model="form.resourceId" :disabled="!!editingId" filterable style="width: 100%">
            <el-option v-for="item in resourceOptions" :key="item.id" :label="item.label" :value="item.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="授权动作">
          <el-checkbox v-model="form.allActions">全部动作</el-checkbox>
          <el-checkbox-group v-if="!form.allActions" v-model="form.actionList" style="margin-top: 6px">
            <el-checkbox v-for="opt in actionOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </el-checkbox>
          </el-checkbox-group>
        </el-form-item>
        <el-form-item label="有效期至">
          <el-date-picker
            v-model="form.expiresAt"
            type="date"
            value-format="YYYY-MM-DD"
            placeholder="留空表示长期有效"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" placeholder="建议写清授权原因与工单号" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
