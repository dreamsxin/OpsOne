<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import { checkHost, createHost, deleteHost, listHosts, updateHost, type Host } from '@/api'

const loading = ref(false)
const rows = ref<Host[]>([])
const total = ref(0)
const query = reactive({ page: 1, pageSize: 20, keyword: '', env: '', status: '' })

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  address: '',
  port: 22,
  username: 'root',
  authType: 'password' as 'password' | 'key',
  secret: '',
  env: 'dev' as 'dev' | 'test' | 'prod',
  tags: '',
  remark: '',
  proxyHostId: 0
})


const rules = {
  name: [{ required: true, message: '请输入主机名称', trigger: 'blur' }],
  address: [{ required: true, message: '请输入 IP 或域名', trigger: 'blur' }],
  username: [{ required: true, message: '请输入登录用户', trigger: 'blur' }]
}

const statusMeta: Record<string, { text: string; type: 'success' | 'danger' | 'info' }> = {
  online: { text: '在线', type: 'success' },
  offline: { text: '离线', type: 'danger' },
  unknown: { text: '未探测', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    const data = await listHosts(query)
    rows.value = data.list || []
    total.value = data.total
  } finally {
    loading.value = false
  }
}

// 跳板机候选需要全量主机，与分页列表分开取
const allHosts = ref<Host[]>([])
async function loadAllHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  allHosts.value = data.list || []
}

const proxyCandidates = computed(() =>
  allHosts.value.filter((h) => !editingId.value || h.id !== editingId.value)
)

function hostLabel(id: number) {
  const host = allHosts.value.find((h) => h.id === id)
  return host ? `${host.name}(${host.address})` : `#${id}`
}


function resetQuery() {
  query.keyword = ''
  query.env = ''
  query.status = ''
  query.page = 1
  load()
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    address: '',
    port: 22,
    username: 'root',
    authType: 'password',
    secret: '',
    env: 'dev',
    tags: '',
    remark: '',
    proxyHostId: 0
  })
  dialogVisible.value = true
}


function openEdit(row: Host) {
  editingId.value = row.id
  Object.assign(form, { ...row, secret: '' })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if (!editingId.value && !form.secret) {
    ElMessage.warning('新增主机必须填写密码或私钥')
    return
  }

  if (editingId.value) {
    await updateHost(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createHost({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
  loadAllHosts()
}

async function remove(row: Host) {
  await ElMessageBox.confirm(`确认删除主机「${row.name}」？`, '危险操作', { type: 'warning' })
  await deleteHost(row.id)
  ElMessage.success('已删除')
  load()
  loadAllHosts()
}

async function check(row: Host) {
  const res = await checkHost(row.id)
  if (res.status === 'online') {
    const via = res.viaProxy ? `（经 ${res.viaProxy}）` : ''
    ElMessage.success(`连通正常${via} ${res.costMs}ms ${res.osInfo}`)
  } else {
    ElMessage.error(`连接失败：${res.detail || '未知原因'}`)
  }
  load()
}

onMounted(() => {
  load()
  loadAllHosts()
})

</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-input
          v-model="query.keyword"
          placeholder="名称 / 地址 / 标签"
          style="width: 220px"
          clearable
          @keyup.enter="((query.page = 1), load())"
        />
        <el-select v-model="query.env" placeholder="环境" clearable style="width: 120px">
          <el-option label="开发" value="dev" />
          <el-option label="测试" value="test" />
          <el-option label="生产" value="prod" />
        </el-select>
        <el-select v-model="query.status" placeholder="状态" clearable style="width: 130px">
          <el-option label="在线" value="online" />
          <el-option label="离线" value="offline" />
          <el-option label="未探测" value="unknown" />
        </el-select>
        <el-button type="primary" @click="((query.page = 1), load())">查询</el-button>
        <el-button @click="resetQuery">重置</el-button>
        <div class="grow"></div>
        <el-button v-perm="'host:create'" type="primary" @click="openCreate">新增主机</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="名称" min-width="130" />
        <el-table-column label="连接" min-width="170">
          <template #default="{ row }">{{ row.username }}@{{ row.address }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column prop="env" label="环境" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.env === 'prod' ? 'danger' : row.env === 'test' ? 'warning' : 'info'">
              {{ row.env }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="authType" label="认证" width="90" />
        <el-table-column prop="status" label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="statusMeta[row.status]?.type || 'info'">
              {{ statusMeta[row.status]?.text || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="osInfo" label="系统" min-width="150" show-overflow-tooltip />
        <el-table-column label="跳板机" min-width="140">
          <template #default="{ row }">
            <span v-if="row.proxyHostId">{{ hostLabel(row.proxyHostId) }}</span>
            <el-tag v-else size="small" type="info">直连</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="tags" label="标签" min-width="110" show-overflow-tooltip />

        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'host:check'" link type="primary" @click="check(row)">探测</el-button>
            <el-button v-perm="'host:update'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'host:delete'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, sizes, prev, pager, next"
        :total="total"
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :page-sizes="[10, 20, 50, 100]"
        @current-change="load"
        @size-change="load"
      />
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑主机' : '新增主机'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="地址" prop="address">
          <el-input v-model="form.address" placeholder="IP 或域名" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" />
        </el-form-item>
        <el-form-item label="登录用户" prop="username">
          <el-input v-model="form.username" />
        </el-form-item>
        <el-form-item label="认证方式">
          <el-radio-group v-model="form.authType">
            <el-radio value="password">密码</el-radio>
            <el-radio value="key">私钥</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="form.authType === 'key' ? '私钥' : '密码'">
          <el-input
            v-model="form.secret"
            :type="form.authType === 'key' ? 'textarea' : 'password'"
            :rows="4"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '必填'"
          />
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.env">
            <el-option label="开发" value="dev" />
            <el-option label="测试" value="test" />
            <el-option label="生产" value="prod" />
          </el-select>
        </el-form-item>
        <el-form-item label="跳板机">
          <el-select v-model="form.proxyHostId" filterable style="width: 100%">
            <el-option label="直连（不经跳板机）" :value="0" />
            <el-option
              v-for="host in proxyCandidates"
              :key="host.id"
              :label="`${host.name}（${host.address}:${host.port}）`"
              :value="host.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="标签">
          <el-input v-model="form.tags" placeholder="逗号分隔，如 web,nginx" />
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
