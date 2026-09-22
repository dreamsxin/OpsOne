<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  deleteKubeGrant,
  diagnoseKubeGrant,
  getKubeGrantState,
  listKubeClusters,
  listKubeGrants,
  listRoles,
  listUsers,
  saveKubeGrant,
  type KubeCluster,
  type KubeGrant,
  type KubeGrantDiagnosis,
  type KubeGrantState,
  type Role,
  type User
} from '@/api'

const loading = ref(false)
const rows = ref<KubeGrant[]>([])
const state = ref<KubeGrantState | null>(null)
const clusters = ref<KubeCluster[]>([])
const users = ref<User[]>([])
const roles = ref<Role[]>([])

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  subjectType: 'user',
  subjectId: 0,
  clusterId: 0,
  namespaces: '',
  kinds: '',
  allowLogs: false,
  allowWrite: false,
  allowForward: false,
  expiresAt: '',
  remark: ''
})

const diagVisible = ref(false)
const diagUserId = ref(0)
const diag = ref<KubeGrantDiagnosis | null>(null)

// 用户与角色的展示字段不同，模板里判断类型不方便，放到脚本里
function subjectLabel(item: User | Role) {
  if ('username' in item) {
    return `${item.username}（${item.nickname || '-'}）`
  }
  return item.name
}

async function load() {
  loading.value = true
  try {
    rows.value = await listKubeGrants()
    state.value = await getKubeGrantState()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    subjectType: 'user',
    subjectId: 0,
    clusterId: clusters.value[0]?.id || 0,
    namespaces: '',
    kinds: '',
    allowLogs: false,
    allowWrite: false,
    allowForward: false,
    expiresAt: '',
    remark: ''
  })
  dialogVisible.value = true
}

function openEdit(row: KubeGrant) {
  editingId.value = row.id
  Object.assign(form, {
    subjectType: row.subjectType,
    subjectId: row.subjectId,
    clusterId: row.clusterId,
    namespaces: row.namespaces,
    kinds: row.kinds,
    allowLogs: row.allowLogs,
    allowWrite: row.allowWrite,
    allowForward: row.allowForward,
    expiresAt: row.expiresAt ? row.expiresAt.slice(0, 10) : '',
    remark: row.remark
  })
  dialogVisible.value = true
}

async function submit() {
  if (!form.subjectId) {
    ElMessage.warning('请选择授权对象')
    return
  }
  if (!form.clusterId) {
    ElMessage.warning('请选择集群')
    return
  }
  const payload: Record<string, any> = { ...form }
  payload.expiresAt = form.expiresAt ? new Date(form.expiresAt + 'T23:59:59').toISOString() : null
  await saveKubeGrant(editingId.value || 0, payload)
  ElMessage.success(editingId.value ? '已更新' : '已新增')
  dialogVisible.value = false
  load()
}

async function remove(row: KubeGrant) {
  await ElMessageBox.confirm(
    `删除「${row.subjectName}」在「${row.clusterName}」上的授权？`,
    '确认删除',
    { type: 'warning' }
  )
  await deleteKubeGrant(row.id)
  ElMessage.success('已删除')
  load()
}

async function runDiagnose() {
  if (!diagUserId.value) {
    ElMessage.warning('请选择要诊断的用户')
    return
  }
  diag.value = await diagnoseKubeGrant(diagUserId.value)
}

function openDiagnose() {
  diag.value = null
  diagUserId.value = 0
  diagVisible.value = true
}

onMounted(async () => {
  clusters.value = (await listKubeClusters()) || []
  const userPage = await listUsers({ page: 1, pageSize: 200 })
  users.value = userPage.list || []
  roles.value = (await listRoles()) || []
  load()
})
</script>

<template>
  <div class="page">
    <el-card>
      <el-alert
        :type="state?.enforced ? 'success' : 'warning'"
        :closable="false"
        style="margin-bottom: 12px"
      >
        <template #title>
          <strong>{{ state?.activeNote }}</strong>
          <br />
          这一页之前的现状是：<strong>所有 K8s 只读接口对任意登录用户开放</strong> ——
          节点、工作负载、Pod、<strong>Pod 日志</strong>、RBAC 账户、Secret 的键名，
          只要登录就能看。主机与数据库早就有「数据范围 + 资源授权」两层机制，K8s 一直没接进去。
          <br />
          参照站把这件事拆成「K8s 授权」（能看哪些集群）与「K8s 数据授权」（能看哪些命名空间 /
          资源）两页，这里<strong>合成一页</strong>：拆开之后必然出现「两处配置矛盾时听谁的」，
          而那个问题没有直觉正确的答案，任何取舍都会让人算不准自己到底能看到什么。
          <br />
          开关在系统配置 <code>{{ state?.configKey }}</code>。
          <strong>默认关闭</strong>：默认打开会让升级瞬间把所有非管理员锁在外面。
        </template>
      </el-alert>

      <div v-if="state" class="page-toolbar" style="gap: 8px; flex-wrap: wrap">
        <el-tag :type="state.enforced ? 'success' : 'warning'">
          {{ state.enforced ? '授权已生效' : '授权未生效' }}
        </el-tag>
        <el-tag type="info">授权条目 {{ state.grantCount }}</el-tag>
        <el-tag type="info">集群 {{ state.clusterCount }}</el-tag>
        <el-tag :type="state.uncoveredClusters > 0 ? 'danger' : 'success'">
          无任何授权的集群 {{ state.uncoveredClusters }}
        </el-tag>
        <el-tag v-if="state.expiredGrants > 0" type="warning">
          已过期 {{ state.expiredGrants }}
        </el-tag>
        <el-tag type="info">{{ state.adminPerm }} 不受限制</el-tag>
      </div>

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button @click="openDiagnose">诊断某个人能看到什么</el-button>
        <el-button v-perm="'kubegrant:manage'" type="primary" @click="openCreate">新增授权</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe empty-text="还没有任何授权">
        <el-table-column label="授权对象" min-width="140">
          <template #default="{ row }">
            <el-tag size="small" :type="row.subjectType === 'role' ? 'warning' : 'info'">
              {{ row.subjectType === 'role' ? '角色' : '用户' }}
            </el-tag>
            <span style="margin-left: 6px">{{ row.subjectName }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="clusterName" label="集群" min-width="130" show-overflow-tooltip />
        <el-table-column label="命名空间" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <el-tag v-if="row.allNamespaces" size="small" type="success">全部</el-tag>
            <span v-else>{{ row.namespaces }}</span>
          </template>
        </el-table-column>
        <el-table-column label="资源类型" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <el-tag v-if="row.allKinds" size="small" type="success">全部</el-tag>
            <span v-else>{{ row.kinds }}</span>
          </template>
        </el-table-column>
        <el-table-column label="额外放开" width="190">
          <template #default="{ row }">
            <el-tag v-if="row.allowLogs" size="small" type="danger" style="margin-right: 4px">日志</el-tag>
            <el-tag v-if="row.allowWrite" size="small" type="warning" style="margin-right: 4px">写</el-tag>
            <el-tag v-if="row.allowForward" size="small" type="warning">转发</el-tag>
            <span v-if="!row.allowLogs && !row.allowWrite && !row.allowForward" style="color: #909399">
              仅只读对象
            </span>
          </template>
        </el-table-column>
        <el-table-column label="有效期" width="130">
          <template #default="{ row }">
            <span v-if="!row.expiresAt">长期</span>
            <el-tag v-else-if="row.expired" size="small" type="danger">已过期</el-tag>
            <span v-else>{{ row.expiresAt.slice(0, 10) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="operator" label="操作人" width="100" />
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'kubegrant:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'kubegrant:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <ul v-if="state" style="margin-top: 12px; color: #909399; font-size: 13px; line-height: 1.8">
        <li v-for="(note, idx) in state.notes" :key="idx">{{ note }}</li>
      </ul>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑授权' : '新增授权'" width="560px">
      <el-form ref="formRef" :model="form" label-width="110px">
        <el-form-item label="授权对象">
          <el-radio-group v-model="form.subjectType" :disabled="!!editingId" @change="form.subjectId = 0">
            <el-radio-button value="user">用户</el-radio-button>
            <el-radio-button value="role">角色</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="form.subjectType === 'role' ? '角色' : '用户'">
          <el-select v-model="form.subjectId" :disabled="!!editingId" filterable style="width: 100%">
            <el-option
              v-for="u in form.subjectType === 'user' ? users : roles"
              :key="u.id"
              :value="u.id"
              :label="subjectLabel(u)"
            />
          </el-select>
          <el-text v-if="editingId" type="info" size="small" style="display: block; margin-top: 4px">
            对象与集群不允许改：那等于换了一条授权，留着原记录会让审计看不懂
          </el-text>
        </el-form-item>
        <el-form-item label="集群">
          <el-select v-model="form.clusterId" :disabled="!!editingId" style="width: 100%">
            <el-option v-for="c in clusters" :key="c.id" :value="c.id" :label="c.name" />
          </el-select>
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            不支持「全部集群」：那等于一张空白支票，新接入的集群会被自动包含进来
          </el-text>
        </el-form-item>
        <el-form-item label="命名空间">
          <el-input v-model="form.namespaces" placeholder="逗号分隔，留空表示该集群全部命名空间" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            填了清单之后，<strong>跨命名空间的查询会被拒绝</strong>而不是静默只返回允许的那部分 ——
            静默过滤会让人以为集群里只有这些东西
          </el-text>
        </el-form-item>
        <el-form-item label="资源类型">
          <el-input v-model="form.kinds" placeholder="逗号分隔（如 Deployment,Service），留空表示不限" />
        </el-form-item>
        <el-form-item label="Pod 日志">
          <el-switch v-model="form.allowLogs" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            日志里常带业务数据与密钥，所以与「能看到对象」分开授权
          </el-text>
        </el-form-item>
        <el-form-item label="写操作">
          <el-switch v-model="form.allowWrite" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            apply / scale / restart。还需要同时具备 kube:write 功能权限（AND 关系）
          </el-text>
        </el-form-item>
        <el-form-item label="端口转发">
          <el-switch v-model="form.allowForward" />
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            隧道会把集群内的服务暴露到平台所在的机器上。还需要 kube:forward
          </el-text>
        </el-form-item>
        <el-form-item label="有效期至">
          <el-date-picker v-model="form.expiresAt" type="date" value-format="YYYY-MM-DD"
            placeholder="留空表示长期有效" style="width: 100%" />
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

    <el-dialog v-model="diagVisible" title="诊断：这个人能看到什么" width="700px">
      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          可见范围是「总开关 + 管理员豁免 + 多条授权的并集 + 有效期」四件事叠起来的结果，
          对着列表推演一定会算错，所以直接算给你看
        </template>
      </el-alert>
      <div class="page-toolbar">
        <el-select v-model="diagUserId" filterable placeholder="选择用户" style="width: 260px">
          <el-option
            v-for="u in users"
            :key="u.id"
            :value="u.id"
            :label="subjectLabel(u)"
          />
        </el-select>
        <el-button type="primary" @click="runDiagnose">诊断</el-button>
      </div>

      <div v-if="diag">
        <div class="page-toolbar" style="gap: 8px">
          <el-tag :type="diag.enforced ? 'success' : 'warning'">
            {{ diag.enforced ? '授权已生效' : '授权未生效' }}
          </el-tag>
          <el-tag v-if="diag.isKubeAdmin" type="danger">此人有 kube:manage，不受授权限制</el-tag>
        </div>
        <el-table :data="diag.clusters" border stripe>
          <el-table-column prop="clusterName" label="集群" min-width="120" />
          <el-table-column label="可见" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="row.visible ? 'success' : 'info'">
                {{ row.visible ? '是' : '否' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="命名空间" min-width="130">
            <template #default="{ row }">{{ row.namespaces || '—' }}</template>
          </el-table-column>
          <el-table-column label="资源类型" min-width="130">
            <template #default="{ row }">{{ row.kinds || '—' }}</template>
          </el-table-column>
          <el-table-column label="日志/写/转发" width="120">
            <template #default="{ row }">
              <span v-if="row.visible && row.namespaces !== undefined">
                {{ row.allowLogs ? '日志 ' : '' }}{{ row.allowWrite ? '写 ' : ''
                }}{{ row.allowForward ? '转发' : '' }}
                <span v-if="!row.allowLogs && !row.allowWrite && !row.allowForward">—</span>
              </span>
              <span v-else>—</span>
            </template>
          </el-table-column>
          <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
        </el-table>
      </div>
      <template #footer>
        <el-button @click="diagVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>
