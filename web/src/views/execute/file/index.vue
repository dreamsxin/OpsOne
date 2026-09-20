<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteFile,
  downloadFile,
  listFileAudits,
  listFiles,
  listHosts,
  makeDir,
  renameFile,
  uploadFile,
  type FileAudit,
  type FileEntry,
  type Host
} from '@/api'

const hosts = ref<Host[]>([])
const selectedId = ref<number | null>(null)
const loading = ref(false)
const currentPath = ref('/')
const entries = ref<FileEntry[]>([])
const uploading = ref(false)
const fileInput = ref<HTMLInputElement>()

const auditVisible = ref(false)
const audits = ref<FileAudit[]>([])
const auditTotal = ref(0)
const auditQuery = reactive({ page: 1, pageSize: 20 })

// 面包屑：把当前路径切成可点击的片段
const segments = computed(() => {
  const parts = currentPath.value.split('/').filter(Boolean)
  const list = [{ label: '/', path: '/' }]
  let acc = ''
  for (const part of parts) {
    acc += '/' + part
    list.push({ label: part, path: acc })
  }
  return list
})

const sortedEntries = computed(() =>
  [...entries.value].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
)

async function loadHosts() {
  const data = await listHosts({ page: 1, pageSize: 200 })
  hosts.value = data.list || []
}

async function load(path = currentPath.value) {
  if (!selectedId.value) return
  loading.value = true
  try {
    const data = await listFiles(selectedId.value, path)
    currentPath.value = data.path
    entries.value = data.entries || []
  } finally {
    loading.value = false
  }
}

function enter(entry: FileEntry) {
  if (entry.isDir) load(entry.path)
}

function goUp() {
  if (currentPath.value === '/') return
  const parent = currentPath.value.split('/').slice(0, -1).join('/') || '/'
  load(parent)
}

async function download(entry: FileEntry) {
  if (!selectedId.value) return
  try {
    await downloadFile(selectedId.value, entry.path)
    ElMessage.success('下载已开始')
  } catch (err) {
    ElMessage.error((err as Error).message)
  }
}

function pickFile() {
  fileInput.value?.click()
}

async function onFilePicked(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许重复选择同一个文件
  if (!file || !selectedId.value) return

  uploading.value = true
  try {
    const res = await uploadFile(selectedId.value, currentPath.value, file)
    ElMessage.success(`已上传到 ${res.path}`)
    load()
  } finally {
    uploading.value = false
  }
}

async function createDir() {
  if (!selectedId.value) return
  const { value } = await ElMessageBox.prompt('请输入目录名', '新建目录', {
    inputPattern: /^[^/]+$/,
    inputErrorMessage: '目录名不能包含斜杠'
  })
  const target = currentPath.value === '/' ? `/${value}` : `${currentPath.value}/${value}`
  await makeDir(selectedId.value, target)
  ElMessage.success('已创建')
  load()
}

async function rename(entry: FileEntry) {
  if (!selectedId.value) return
  const { value } = await ElMessageBox.prompt('请输入新名称', '重命名', {
    inputValue: entry.name,
    inputPattern: /^[^/]+$/,
    inputErrorMessage: '名称不能包含斜杠'
  })
  const dir = entry.path.split('/').slice(0, -1).join('/') || ''
  await renameFile(selectedId.value, entry.path, `${dir}/${value}`)
  ElMessage.success('已重命名')
  load()
}

async function remove(entry: FileEntry) {
  if (!selectedId.value) return
  await ElMessageBox.confirm(
    entry.isDir ? `确认删除目录「${entry.name}」？仅支持空目录` : `确认删除文件「${entry.name}」？`,
    '危险操作',
    { type: 'warning' }
  )
  await deleteFile(selectedId.value, entry.path)
  ElMessage.success('已删除')
  load()
}

async function openAudits() {
  const data = await listFileAudits(auditQuery)
  audits.value = data.list || []
  auditTotal.value = data.total
  auditVisible.value = true
}

function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`
  return `${(size / 1024 / 1024 / 1024).toFixed(2)} GB`
}

const actionLabel: Record<string, string> = {
  upload: '上传',
  download: '下载',
  delete: '删除',
  mkdir: '新建目录',
  rename: '重命名'
}

onMounted(loadHosts)
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-select
          v-model="selectedId"
          placeholder="选择主机"
          filterable
          style="width: 300px"
          @change="load('/')"
        >
          <el-option
            v-for="host in hosts"
            :key="host.id"
            :label="`${host.name}（${host.username}@${host.address}）`"
            :value="host.id"
          />
        </el-select>
        <el-button :disabled="!selectedId" @click="goUp">上一级</el-button>
        <el-button :disabled="!selectedId" @click="load()">刷新</el-button>
        <el-button v-perm="'file:write'" type="primary" :disabled="!selectedId" :loading="uploading" @click="pickFile">
          上传文件
        </el-button>
        <el-button v-perm="'file:write'" :disabled="!selectedId" @click="createDir">新建目录</el-button>
        <div class="grow"></div>
        <el-button @click="openAudits">操作留痕</el-button>
      </div>

      <input ref="fileInput" type="file" style="display: none" @change="onFilePicked" />

      <el-breadcrumb separator="/" style="margin-bottom: 12px">
        <el-breadcrumb-item v-for="seg in segments" :key="seg.path">
          <a style="cursor: pointer" @click="load(seg.path)">{{ seg.label }}</a>
        </el-breadcrumb-item>
      </el-breadcrumb>

      <el-table v-loading="loading" :data="sortedEntries" border stripe empty-text="请选择主机或该目录为空">
        <el-table-column label="名称" min-width="260">
          <template #default="{ row }">
            <el-icon v-if="row.isDir"><Folder /></el-icon>
            <el-icon v-else><Document /></el-icon>
            <a
              v-if="row.isDir"
              style="margin-left: 6px; cursor: pointer; color: var(--el-color-primary)"
              @click="enter(row)"
            >
              {{ row.name }}
            </a>
            <span v-else style="margin-left: 6px">{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="110">
          <template #default="{ row }">{{ row.isDir ? '-' : formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column prop="mode" label="权限" width="130" />
        <el-table-column prop="modTime" label="修改时间" min-width="180" />
        <el-table-column label="操作" width="190" fixed="right">
          <template #default="{ row }">
            <el-button v-if="!row.isDir" link type="primary" @click="download(row)">下载</el-button>
            <el-button v-perm="'file:write'" link type="primary" @click="rename(row)">重命名</el-button>
            <el-button v-perm="'file:delete'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-drawer v-model="auditVisible" title="文件操作留痕" size="60%">
      <el-alert
        type="info"
        :closable="false"
        style="margin-bottom: 12px"
        title="仅记录上传、下载、删除、重命名、新建目录，浏览目录不记录"
      />
      <el-table :data="audits" border size="small">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="username" label="操作人" width="110" />
        <el-table-column label="主机" min-width="150">
          <template #default="{ row }">{{ row.hostName }}（{{ row.address }}）</template>
        </el-table-column>
        <el-table-column label="动作" width="100">
          <template #default="{ row }">{{ actionLabel[row.action] || row.action }}</template>
        </el-table-column>
        <el-table-column prop="path" label="路径" min-width="220" show-overflow-tooltip />
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ row.size ? formatSize(row.size) : '-' }}</template>
        </el-table-column>
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'success' ? 'success' : 'danger'">
              {{ row.status === 'success' ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="createdAt" label="时间" min-width="180" />
      </el-table>
      <el-pagination
        style="margin-top: 12px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="auditTotal"
        v-model:current-page="auditQuery.page"
        :page-size="auditQuery.pageSize"
        @current-change="openAudits"
      />
    </el-drawer>
  </div>
</template>
