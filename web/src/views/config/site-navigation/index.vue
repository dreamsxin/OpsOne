<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createSiteLink,
  deleteSiteLink,
  listSiteLinks,
  updateSiteLink,
  type SiteLink
} from '@/api'

const loading = ref(false)
const rows = ref<SiteLink[]>([])
const manageMode = ref(false)

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  url: '',
  category: 'general',
  icon: 'Link',
  description: '',
  sort: 0,
  enabled: true
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  url: [{ required: true, message: '请输入地址', trigger: 'blur' }]
}

// 按分类分组做卡片墙
const grouped = computed(() => {
  const map = new Map<string, SiteLink[]>()
  for (const item of rows.value) {
    if (!manageMode.value && !item.enabled) continue
    const list = map.get(item.category) || []
    list.push(item)
    map.set(item.category, list)
  }
  return Array.from(map.entries())
})

async function load() {
  loading.value = true
  try {
    rows.value = await listSiteLinks()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    url: '',
    category: 'general',
    icon: 'Link',
    description: '',
    sort: 0,
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: SiteLink) {
  editingId.value = row.id
  Object.assign(form, { ...row })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  if (editingId.value) {
    await updateSiteLink(editingId.value, { ...form })
    ElMessage.success('已更新')
  } else {
    await createSiteLink({ ...form })
    ElMessage.success('已新增')
  }
  dialogVisible.value = false
  load()
}

async function remove(row: SiteLink) {
  await ElMessageBox.confirm(`确认删除导航项「${row.name}」？`, '提示', { type: 'warning' })
  await deleteSiteLink(row.id)
  ElMessage.success('已删除')
  load()
}

function open(row: SiteLink) {
  window.open(row.url, '_blank', 'noopener,noreferrer')
}

onMounted(load)

</script>

<template>
  <div class="page" v-loading="loading">
    <el-card>
      <div class="page-toolbar">
        <el-switch v-model="manageMode" active-text="管理模式" />
        <span style="color: #6b7280">集中收拢内部系统入口，只允许 http/https 地址</span>
        <div class="grow"></div>
        <el-button @click="load">刷新</el-button>
        <el-button v-perm="'config:manage'" type="primary" @click="openCreate">新增导航</el-button>
      </div>

      <el-empty v-if="!grouped.length" description="还没有导航项" />

      <template v-if="!manageMode">
        <div v-for="[category, items] in grouped" :key="category">
          <el-divider content-position="left">{{ category }}</el-divider>
          <el-row :gutter="12">
            <el-col v-for="item in items" :key="item.id" :span="6" style="margin-bottom: 12px">
              <el-card shadow="hover" style="cursor: pointer" @click="open(item)">
                <div style="display: flex; align-items: center; gap: 8px">
                  <el-icon :size="20"><component :is="item.icon || 'Link'" /></el-icon>
                  <strong>{{ item.name }}</strong>
                </div>
                <div
                  style="
                    margin-top: 6px;
                    color: #6b7280;
                    font-size: 13px;
                    min-height: 20px;
                    overflow: hidden;
                    text-overflow: ellipsis;
                    white-space: nowrap;
                  "
                >
                  {{ item.description || item.url }}
                </div>
              </el-card>
            </el-col>
          </el-row>
        </div>
      </template>

      <el-table v-else :data="rows" border stripe>
        <el-table-column prop="category" label="分类" width="120" />
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="url" label="地址" min-width="240" show-overflow-tooltip />
        <el-table-column prop="description" label="说明" min-width="180" show-overflow-tooltip />
        <el-table-column prop="sort" label="排序" width="80" />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '启用' : '停用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="open(row)">打开</el-button>
            <el-button v-perm="'config:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'config:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑导航' : '新增导航'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="80px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="地址" prop="url">
          <el-input v-model="form.url" placeholder="https://..." />
        </el-form-item>
        <el-form-item label="分类">
          <el-input v-model="form.category" placeholder="如 监控、研发、办公" />
        </el-form-item>
        <el-form-item label="图标">
          <el-input v-model="form.icon" placeholder="Element Plus 图标名，如 Monitor" />
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sort" :min="0" :max="9999" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
