<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  applyKubeResource,
  getKubeResource,
  listKubeChangeLogs,
  listKubeClusters,
  listKubeNamespaces,
  listKubeResourceKinds,
  listKubeResources,
  scaleKubeResource,
  type KubeChangeLog,
  type KubeCluster,
  type KubeResourceItem,
  type KubeResourceKind
} from '@/api'

const clusters = ref<KubeCluster[]>([])
const kinds = ref<KubeResourceKind[]>([])
const namespaces = ref<string[]>([])

const clusterId = ref<number>(0)
const kind = ref('Deployment')
const namespace = ref('')
const tab = ref('resource')

const loading = reactive({ list: false, detail: false, submit: false, log: false })
const items = ref<KubeResourceItem[]>([])
const currentCluster = computed(() => clusters.value.find((c) => c.id === clusterId.value))
const currentKind = computed(() => kinds.value.find((k) => k.kind === kind.value))

// ---------- YAML 抽屉 ----------
const editor = reactive({
  visible: false,
  kind: '',
  namespace: '',
  name: '',
  yaml: '',
  original: '',
  hint: '',
  force: false,
  result: '',
  resultType: 'success' as 'success' | 'warning'
})
const dirty = computed(() => editor.yaml !== editor.original)

// ---------- 副本数对话框 ----------
const scaler = reactive({
  visible: false,
  kind: '',
  namespace: '',
  name: '',
  current: 0,
  replicas: 1,
  result: ''
})

// ---------- 改动留痕 ----------
const logQuery = reactive({ page: 1, pageSize: 20, action: '', status: '', realOnly: false })
const logs = ref<KubeChangeLog[]>([])
const logTotal = ref(0)

async function loadClusters() {
  clusters.value = await listKubeClusters()
  const usable = clusters.value.find((c) => c.status === 'healthy' || c.status === 'degraded')
  clusterId.value = (usable || clusters.value[0])?.id || 0
}

async function loadNamespaces() {
  namespaces.value = []
  if (!clusterId.value) return
  try {
    const list = await listKubeNamespaces(clusterId.value)
    namespaces.value = list.map((n) => n.name)
  } catch {
    // 命名空间读不到（多为 RBAC 不足）不影响按全部查
  }
}

async function loadResources() {
  if (!clusterId.value) return
  loading.list = true
  try {
    const res = await listKubeResources(clusterId.value, kind.value, namespace.value)
    items.value = res.items || []
  } catch (err: any) {
    items.value = []
    ElMessage.error(err?.message || '读取资源失败')
  } finally {
    loading.list = false
  }
}

async function loadLogs() {
  loading.log = true
  try {
    const res = await listKubeChangeLogs({
      page: logQuery.page,
      pageSize: logQuery.pageSize,
      ...(clusterId.value ? { clusterId: clusterId.value } : {}),
      ...(logQuery.action ? { action: logQuery.action } : {}),
      ...(logQuery.status ? { status: logQuery.status } : {}),
      ...(logQuery.realOnly ? { realOnly: 'true' } : {})
    })
    logs.value = res.list || []
    logTotal.value = res.total
  } catch (err: any) {
    ElMessage.error(err?.message || '读取改动留痕失败')
  } finally {
    loading.log = false
  }
}

function searchLogs() {
  logQuery.page = 1
  loadLogs()
}

async function openEditor(row: KubeResourceItem) {
  editor.visible = true
  editor.kind = row.kind
  editor.namespace = row.namespace
  editor.name = row.name
  editor.yaml = ''
  editor.original = ''
  editor.hint = ''
  editor.force = false
  editor.result = ''
  loading.detail = true
  try {
    const detail = await getKubeResource(clusterId.value, row.kind, row.namespace, row.name)
    editor.yaml = detail.yaml
    editor.original = detail.yaml
    editor.hint = detail.hint
  } catch (err: any) {
    ElMessage.error(err?.message || '读取对象失败')
  } finally {
    loading.detail = false
  }
}

async function submitApply(dryRun: boolean) {
  if (!editor.yaml.trim()) {
    ElMessage.warning('YAML 不能为空')
    return
  }
  if (!dryRun) {
    try {
      await ElMessageBox.confirm(
        `确认把这段 YAML 应用到集群「${currentCluster.value?.name}」的 ${editor.kind}/${editor.name}？`,
        '应用到集群',
        { type: 'warning', confirmButtonText: '应用', cancelButtonText: '再想想' }
      )
    } catch {
      return
    }
  }
  loading.submit = true
  try {
    const res = await applyKubeResource(clusterId.value, {
      yaml: editor.yaml,
      dryRun,
      force: editor.force
    })
    editor.result = res.detail
    editor.resultType = dryRun ? 'warning' : 'success'
    if (!dryRun) {
      editor.original = editor.yaml
      ElMessage.success(res.detail)
      loadResources()
      loadLogs()
    }
  } catch (err: any) {
    editor.result = ''
    ElMessage.error(err?.message || '提交失败')
  } finally {
    loading.submit = false
  }
}

function openScaler(row: KubeResourceItem) {
  scaler.visible = true
  scaler.kind = row.kind
  scaler.namespace = row.namespace
  scaler.name = row.name
  scaler.result = ''
  // 摘要形如「副本 1/3」，把期望值拿来当默认目标
  const matched = /(\d+)\s*\/\s*(\d+)/.exec(row.summary)
  scaler.current = matched ? Number(matched[2]) : 0
  scaler.replicas = scaler.current
}

async function submitScale(dryRun: boolean) {
  loading.submit = true
  try {
    const res = await scaleKubeResource(clusterId.value, {
      kind: scaler.kind,
      namespace: scaler.namespace,
      name: scaler.name,
      replicas: scaler.replicas,
      dryRun
    })
    scaler.result = res.detail
    if (!dryRun) {
      ElMessage.success(res.detail)
      scaler.visible = false
      loadResources()
      loadLogs()
    }
  } catch (err: any) {
    scaler.result = ''
    ElMessage.error(err?.message || '改副本数失败')
  } finally {
    loading.submit = false
  }
}

function showPayload(row: KubeChangeLog) {
  ElMessageBox.alert(row.payload || '（无）', `${row.action} ${row.kind}/${row.name}`, {
    customClass: 'payload-box',
    confirmButtonText: '关闭'
  })
}

watch(clusterId, async () => {
  namespace.value = ''
  await loadNamespaces()
  loadResources()
  searchLogs()
})
watch([kind, namespace], loadResources)

onMounted(async () => {
  kinds.value = await listKubeResourceKinds()
  await loadClusters()
  if (clusterId.value) {
    await loadNamespaces()
    await loadResources()
  }
  loadLogs()
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select v-model="clusterId" placeholder="选择集群" style="width: 200px">
          <el-option v-for="item in clusters" :key="item.id" :label="item.name" :value="item.id" />
        </el-select>
        <el-select v-model="kind" style="width: 160px">
          <el-option v-for="item in kinds" :key="item.kind" :label="item.kind" :value="item.kind" />
        </el-select>
        <el-select
          v-model="namespace"
          placeholder="全部命名空间"
          clearable
          filterable
          style="width: 200px"
        >
          <el-option v-for="name in namespaces" :key="name" :label="name" :value="name" />
        </el-select>
        <el-button @click="loadResources">刷新</el-button>
        <span class="tip">
          改动走 Kubernetes 服务端 apply，提交前可先预检；平台只开放
          {{ kinds.map((k) => k.kind).join(' / ') }}，不提供删除
        </span>
      </div>

      <el-empty v-if="!clusterId" description="还没有接入集群，先去「集群接入」加一个" />
      <template v-else>
        <el-alert
          v-if="currentCluster && currentCluster.status !== 'healthy'"
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`集群当前状态为「${currentCluster.status}」：${currentCluster.lastError || '请先在集群接入页做一次检查'}`"
        />

        <el-tabs v-model="tab" @tab-change="tab === 'log' && loadLogs()">
          <el-tab-pane label="资源" name="resource">
            <el-table v-loading="loading.list" :data="items" border stripe>
              <el-table-column prop="namespace" label="命名空间" width="150" />
              <el-table-column prop="name" label="名称" min-width="220" show-overflow-tooltip />
              <el-table-column prop="summary" label="概况" min-width="200" show-overflow-tooltip />
              <el-table-column prop="createdAt" label="创建时间" min-width="180" />
              <el-table-column label="操作" width="170" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="openEditor(row)">YAML</el-button>
                  <el-button
                    v-if="currentKind?.scalable"
                    v-perm="'kube:write'"
                    link
                    type="primary"
                    @click="openScaler(row)"
                  >
                    副本数
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="改动留痕" name="log">
            <div class="page-toolbar">
              <el-select v-model="logQuery.action" placeholder="全部动作" clearable style="width: 140px">
                <el-option label="apply" value="apply" />
                <el-option label="scale" value="scale" />
              </el-select>
              <el-select v-model="logQuery.status" placeholder="全部结果" clearable style="width: 140px">
                <el-option label="成功" value="success" />
                <el-option label="失败" value="failed" />
              </el-select>
              <el-checkbox v-model="logQuery.realOnly">只看真改过的（排除预检）</el-checkbox>
              <el-button type="primary" @click="searchLogs">查询</el-button>
            </div>
            <el-table v-loading="loading.log" :data="logs" border stripe>
              <el-table-column prop="createdAt" label="时间" min-width="170" />
              <el-table-column prop="username" label="操作人" width="110" />
              <el-table-column prop="clusterName" label="集群" width="130" />
              <el-table-column label="对象" min-width="220" show-overflow-tooltip>
                <template #default="{ row }">
                  {{ row.kind }}/{{ row.name }}
                  <span style="color: var(--el-text-color-secondary)">（{{ row.namespace }}）</span>
                </template>
              </el-table-column>
              <el-table-column label="动作" width="150">
                <template #default="{ row }">
                  <el-tag size="small">{{ row.action }}</el-tag>
                  <el-tag v-if="row.dryRun" size="small" type="info" style="margin-left: 4px">预检</el-tag>
                  <el-tag v-if="row.forced" size="small" type="warning" style="margin-left: 4px">强制</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="结果" width="90">
                <template #default="{ row }">
                  <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
                    {{ row.status === 'success' ? '成功' : '失败' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="detail" label="详情" min-width="240" show-overflow-tooltip />
              <el-table-column label="提交内容" width="100" fixed="right">
                <template #default="{ row }">
                  <el-button link type="primary" @click="showPayload(row)">查看</el-button>
                </template>
              </el-table-column>
            </el-table>
            <el-pagination
              style="margin-top: 12px; justify-content: flex-end"
              layout="total, sizes, prev, pager, next"
              :total="logTotal"
              :page-sizes="[20, 50, 100]"
              v-model:current-page="logQuery.page"
              v-model:page-size="logQuery.pageSize"
              @current-change="loadLogs"
              @size-change="searchLogs"
            />
          </el-tab-pane>
        </el-tabs>
      </template>
    </el-card>

    <el-drawer v-model="editor.visible" size="60%" :title="`${editor.kind}/${editor.name}`">
      <div v-loading="loading.detail">
        <el-alert v-if="editor.hint" type="info" :closable="false" :title="editor.hint" />
        <el-input
          v-model="editor.yaml"
          type="textarea"
          :rows="24"
          spellcheck="false"
          class="yaml-editor"
          style="margin-top: 12px"
        />
        <el-alert
          v-if="editor.result"
          :type="editor.resultType"
          :closable="false"
          :title="editor.result"
          style="margin-top: 12px"
        />
      </div>
      <template #footer>
        <div class="drawer-footer">
          <el-checkbox v-model="editor.force">
            强制接管字段（字段冲突时用，会把别人管着的字段改成这份 YAML 的值）
          </el-checkbox>
          <div>
            <el-button @click="editor.visible = false">关闭</el-button>
            <el-button
              v-perm="'kube:write'"
              :loading="loading.submit"
              @click="submitApply(true)"
            >
              预检
            </el-button>
            <el-button
              v-perm="'kube:write'"
              type="primary"
              :disabled="!dirty"
              :loading="loading.submit"
              @click="submitApply(false)"
            >
              应用到集群
            </el-button>
          </div>
        </div>
      </template>
    </el-drawer>

    <el-dialog v-model="scaler.visible" width="440px" :title="`改副本数：${scaler.kind}/${scaler.name}`">
      <el-form label-width="100px">
        <el-form-item label="命名空间">{{ scaler.namespace }}</el-form-item>
        <el-form-item label="目标副本数">
          <el-input-number v-model="scaler.replicas" :min="0" :max="200" />
        </el-form-item>
      </el-form>
      <el-alert
        v-if="scaler.result"
        type="warning"
        :closable="false"
        :title="scaler.result"
        style="margin-bottom: 8px"
      />
      <template #footer>
        <el-button @click="scaler.visible = false">取消</el-button>
        <el-button v-perm="'kube:write'" :loading="loading.submit" @click="submitScale(true)">
          预检
        </el-button>
        <el-button
          v-perm="'kube:write'"
          type="primary"
          :loading="loading.submit"
          @click="submitScale(false)"
        >
          执行
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.tip {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.yaml-editor :deep(textarea) {
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  line-height: 1.6;
}
.drawer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
</style>
