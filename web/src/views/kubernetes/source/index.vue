<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  checkKubeCluster,
  createKubeCluster,
  deleteKubeCluster,
  listKubeClusters,
  listKubeNodes,
  parseKubeconfigContexts,
  updateKubeCluster,
  type KubeCluster,
  type KubeNode
} from '@/api'

const loading = ref(false)
const rows = ref<KubeCluster[]>([])

const dialog = reactive({
  visible: false,
  id: 0,
  name: '',
  kubeconfig: '',
  contextName: '',
  contexts: [] as string[],
  enabled: true,
  remark: '',
  parsing: false,
  saving: false
})

const nodeDrawer = reactive({
  visible: false,
  title: '',
  loading: false,
  nodes: [] as KubeNode[]
})

const statusType: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
  healthy: 'success',
  degraded: 'warning',
  error: 'danger',
  unknown: 'info'
}
const statusText: Record<string, string> = {
  healthy: '健康',
  degraded: '降级',
  error: '不可达',
  unknown: '未检查'
}

async function load() {
  loading.value = true
  try {
    rows.value = await listKubeClusters()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  Object.assign(dialog, {
    visible: true,
    id: 0,
    name: '',
    kubeconfig: '',
    contextName: '',
    contexts: [],
    enabled: true,
    remark: ''
  })
}

function openEdit(row: KubeCluster) {
  Object.assign(dialog, {
    visible: true,
    id: row.id,
    name: row.name,
    kubeconfig: '',
    contextName: row.contextName,
    contexts: row.contextName ? [row.contextName] : [],
    enabled: row.enabled,
    remark: row.remark
  })
}

/** 解析上下文：粘完 kubeconfig 先让人确认用哪个上下文，而不是盲存 */
async function parseContexts() {
  if (!dialog.kubeconfig.trim()) {
    ElMessage.warning('请先粘贴 kubeconfig')
    return
  }
  dialog.parsing = true
  try {
    const res = await parseKubeconfigContexts(dialog.kubeconfig)
    dialog.contexts = res.contexts
    dialog.contextName = res.currentContext || res.contexts[0]
    ElMessage.success(`解析出 ${res.contexts.length} 个上下文`)
  } catch (err: any) {
    ElMessage.error(err?.message || '解析失败')
  } finally {
    dialog.parsing = false
  }
}

async function submit() {
  if (!dialog.name.trim()) {
    ElMessage.warning('请填写集群名称')
    return
  }
  dialog.saving = true
  try {
    if (dialog.id) {
      await updateKubeCluster(dialog.id, {
        name: dialog.name.trim(),
        kubeconfig: dialog.kubeconfig,
        contextName: dialog.contextName,
        enabled: dialog.enabled,
        remark: dialog.remark
      })
      ElMessage.success('已保存')
    } else {
      const res = await createKubeCluster({
        name: dialog.name.trim(),
        kubeconfig: dialog.kubeconfig,
        contextName: dialog.contextName,
        enabled: dialog.enabled,
        remark: dialog.remark
      })
      if (res.check.status === 'healthy') {
        ElMessage.success(`已接入：${res.check.detail}`)
      } else {
        ElMessage.warning(`已接入但检查未通过：${res.check.detail}`)
      }
    }
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
    return
  } finally {
    dialog.saving = false
  }
  dialog.visible = false
  await load()
}

async function check(row: KubeCluster) {
  const res = await checkKubeCluster(row.id)
  await load()
  if (res.status === 'healthy') {
    ElMessage.success(res.detail)
  } else {
    ElMessage.warning(res.detail)
  }
}

async function remove(row: KubeCluster) {
  await ElMessageBox.confirm(
    `移除集群「${row.name}」？只删除平台里的接入记录与凭据，不会对集群本身做任何改动。`,
    '确认',
    { type: 'warning' }
  )
  const res = await deleteKubeCluster(row.id)
  await load()
  ElMessage.success(res.detail)
}

async function openNodes(row: KubeCluster) {
  nodeDrawer.visible = true
  nodeDrawer.title = `${row.name} 的节点`
  nodeDrawer.loading = true
  nodeDrawer.nodes = []
  try {
    nodeDrawer.nodes = await listKubeNodes(row.id)
  } catch (err: any) {
    ElMessage.error(err?.message || '读取节点失败')
  } finally {
    nodeDrawer.loading = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-button v-perm="'kube:manage'" type="primary" @click="openCreate">接入集群</el-button>
        <el-button @click="load">刷新</el-button>
      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span
            >粘贴 kubeconfig 即可接入，平台只做<b>只读</b>调用：取版本、数节点、列工作负载与事件，不会改动集群任何东西。kubeconfig
            含客户端私钥，等同集群管理员凭据，保存后不再从接口返回。只支持内嵌凭据（<code>client-certificate-data</code>
            /
            <code>token</code>），不支持指向本地文件路径的 kubeconfig。默认每 5 分钟检查一次（<code>OPS_KUBE_CHECK_SPEC</code>
            可改或关闭），不可达或有节点 NotReady 会写告警并走通知路由。</span
          >
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="集群" min-width="130" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType[row.status] || 'info'">
              {{ statusText[row.status] || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="version" label="版本" width="150">
          <template #default="{ row }">{{ row.version || '—' }}</template>
        </el-table-column>
        <el-table-column label="节点" width="100">
          <template #default="{ row }">
            <span v-if="row.nodeTotal">{{ row.nodeReady }}/{{ row.nodeTotal }} 就绪</span>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column prop="server" label="API 地址" min-width="200" show-overflow-tooltip />
        <el-table-column prop="contextName" label="上下文" width="120" show-overflow-tooltip />
        <el-table-column label="最近检查" min-width="230">
          <template #default="{ row }">
            <div>{{ row.lastCheckAt || '还没检查过' }}</div>
            <div v-if="row.lastError" style="color: var(--el-color-danger)">{{ row.lastError }}</div>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '是' : '否' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="210" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'kube:manage'" link type="primary" @click="check(row)">检查</el-button>
            <el-button link type="primary" @click="openNodes(row)">节点</el-button>
            <el-button v-perm="'kube:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'kube:manage'" link type="danger" @click="remove(row)">移除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog
      v-model="dialog.visible"
      :title="dialog.id ? '编辑集群' : '接入集群'"
      width="720px"
    >
      <el-form label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="dialog.name" placeholder="例如 生产集群" />
        </el-form-item>
        <el-form-item label="kubeconfig">
          <el-input
            v-model="dialog.kubeconfig"
            type="textarea"
            :rows="9"
            :placeholder="
              dialog.id ? '留空表示保持原有凭据不变' : '粘贴完整 kubeconfig（含 base64 内嵌证书）'
            "
          />
          <el-button
            :loading="dialog.parsing"
            style="margin-top: 8px"
            @click="parseContexts"
          >
            解析上下文
          </el-button>
        </el-form-item>
        <el-form-item label="上下文">
          <el-select
            v-model="dialog.contextName"
            filterable
            allow-create
            default-first-option
            placeholder="留空则用 current-context"
            style="width: 260px"
          >
            <el-option v-for="name in dialog.contexts" :key="name" :label="name" :value="name" />
          </el-select>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="dialog.remark" />
          <el-checkbox v-model="dialog.enabled" style="margin-top: 8px">
            启用（参与定时检查）
          </el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" :loading="dialog.saving" @click="submit">
          {{ dialog.id ? '保存' : '接入并检查' }}
        </el-button>
      </template>
    </el-dialog>

    <el-drawer v-model="nodeDrawer.visible" :title="nodeDrawer.title" size="70%">
      <el-table v-loading="nodeDrawer.loading" :data="nodeDrawer.nodes" border stripe>
        <el-table-column prop="name" label="节点" min-width="150" />
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="row.ready ? 'success' : 'danger'">
              {{ row.ready ? 'Ready' : 'NotReady' }}
            </el-tag>
            <el-tag v-if="row.unschedulable" size="small" type="warning" style="margin-left: 4px">
              已封锁
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="150">
          <template #default="{ row }">{{ row.roles.join(', ') || '—' }}</template>
        </el-table-column>
        <el-table-column prop="internalIP" label="内网 IP" width="140" />
        <el-table-column prop="version" label="kubelet" width="140" />
        <el-table-column label="容量" min-width="180">
          <template #default="{ row }">
            CPU {{ row.cpu }} · 内存 {{ row.memory }} · Pod 上限 {{ row.pods }}
          </template>
        </el-table-column>
        <el-table-column prop="osImage" label="系统" min-width="150" show-overflow-tooltip />
      </el-table>
    </el-drawer>
  </div>
</template>
