<script setup lang="ts">
/**
 * 特征库。
 *
 * 这一页最该先说清的事：**特征不是摆在这里看的，它要落到一个真在跑的检测点上**。
 *   - command 类特征「应用」之后，会在命令规则里生成 / 维护一条规则 ——
 *     批量执行、脚本下发、定时任务、防火墙下发这几条路径的闸门，以及 Web 终端，
 *     用的都是那张表。所以「已应用」等于「真会拦」。
 *   - observe / enforce 就是灰度：映射成规则的 warn（只提醒）与 block（直接拦下）。
 *   - port 类特征不改暴露面基线（基线是人的决定），只拿真机扫描出来的开放端口核对。
 *
 * 命令规则那边可能被人手工改过或删掉，所以每行都给对账状态（已生效 / 被改过 / 规则没了），
 * 而不是显示一个永远绿着的「已应用」。
 */
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  applySignature,
  checkPortSignature,
  deleteSignature,
  listSignatures,
  reconcileSignatures,
  revokeSignature,
  saveSignature,
  type Signature
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'

const loading = ref(false)
const rows = ref<Signature[]>([])
const stats = ref<Record<string, number>>({})
const note = ref('')
const kindFilter = ref('')
const keyword = ref('')

const dialog = ref(false)
const editingId = ref<number | null>(null)
const form = reactive({
  name: '',
  kind: 'command' as 'command' | 'port',
  pattern: '',
  description: '',
  stage: 'observe' as 'observe' | 'enforce',
  severity: 'medium' as 'high' | 'medium' | 'low',
  enabled: true
})

const portDialog = ref(false)
const portResult = ref<any>(null)
const portChecking = ref(false)

const statusMeta: Record<string, { text: string; type: 'success' | 'warning' | 'danger' | 'info' }> = {
  applied: { text: '已生效', type: 'success' },
  drift: { text: '被改过', type: 'warning' },
  missing: { text: '规则没了', type: 'danger' },
  unapplied: { text: '未应用', type: 'info' },
  'n/a': { text: '不需应用', type: 'info' }
}

async function load() {
  loading.value = true
  try {
    const res = await listSignatures({
      kind: kindFilter.value || undefined,
      keyword: keyword.value.trim() || undefined
    })
    rows.value = res.list || []
    stats.value = res.stats || {}
    note.value = res.note
  } catch (err: any) {
    ElMessage.error(err?.message || '读取特征库失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    kind: 'command',
    pattern: '',
    description: '',
    stage: 'observe',
    severity: 'medium',
    enabled: true
  })
  dialog.value = true
}

function openEdit(row: Signature) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    kind: row.kind,
    pattern: row.pattern,
    description: row.description,
    stage: row.stage,
    severity: row.severity,
    enabled: row.enabled
  })
  dialog.value = true
}

async function submit() {
  if (!form.name.trim() || !form.pattern.trim()) {
    ElMessage.warning('名称与特征内容都要填')
    return
  }
  try {
    const res: any = await saveSignature(editingId.value, { ...form })
    if (res?.warn) ElMessage.warning(res.warn)
    else ElMessage.success('已保存')
    dialog.value = false
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function doApply(row: Signature) {
  const action = row.stage === 'enforce' ? '直接拦下命中的下发' : '只提醒、仍会下发'
  await ElMessageBox.confirm(
    `把「${row.name}」写进命令规则。当前灰度是 ${row.stage}，即${action}。\n\n` +
      '命令规则是下发闸门与 Web 终端真正在用的表，应用之后立刻开始参与判定。',
    '应用到命令规则',
    { type: 'warning' }
  )
  try {
    const res = await applySignature(row.id)
    ElMessage.success(res.detail)
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '应用失败')
  }
}

async function doRevoke(row: Signature) {
  await ElMessageBox.confirm(
    `撤下「${row.name}」：删掉它生成的那条命令规则，特征本身保留。\n\n撤下之后这条特征不再参与任何拦截。`,
    '撤下',
    { type: 'warning' }
  )
  try {
    const res = await revokeSignature(row.id)
    ElMessage.success(res.detail)
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '撤下失败')
  }
}

async function doPortCheck(row: Signature) {
  portChecking.value = true
  try {
    portResult.value = await checkPortSignature(row.id)
    portDialog.value = true
  } catch (err: any) {
    ElMessage.error(err?.message || '核对失败')
  } finally {
    portChecking.value = false
  }
}

async function remove(row: Signature) {
  await ElMessageBox.confirm(
    `删除「${row.name}」？` + (row.ruleId ? '它生成的命令规则会一起删掉。' : ''),
    '提示',
    { type: 'warning' }
  )
  try {
    const res = await deleteSignature(row.id)
    ElMessage.success(res.ruleRemoved ? '已删除特征与对应的命令规则' : '已删除')
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '删除失败')
  }
}

async function doReconcile() {
  try {
    const res = await reconcileSignatures()
    if (res.drift.length === 0) {
      ElMessage.success(res.note)
    } else {
      ElMessageBox.alert(
        res.drift.map((d) => `${d.name}：${d.detail}`).join('\n'),
        `${res.drift.length} 条特征与命令规则不一致`,
        { type: 'warning' }
      )
    }
    load()
  } catch (err: any) {
    ElMessage.error(err?.message || '对账失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <PageHeader
      title="特征库"
      subtitle="危险命令与端口特征的共享来源；应用之后由命令规则与暴露面扫描真正执行，这里只管来源与灰度"
    >
      <template #actions>
        <el-button @click="doReconcile">与命令规则对账</el-button>
        <el-button v-perm="'signature:manage'" type="primary" @click="openCreate">新增特征</el-button>
      </template>
    </PageHeader>

    <el-card shadow="never" class="fact-card">
      <div class="facts">
        <div class="fact"><span class="fact__label">已生效</span>{{ stats.applied || 0 }}</div>
        <div class="fact"><span class="fact__label">未应用</span>{{ stats.unapplied || 0 }}</div>
        <div class="fact">
          <span class="fact__label">被改过</span>
          <b :class="{ warn: (stats.drift || 0) > 0 }">{{ stats.drift || 0 }}</b>
        </div>
        <div class="fact">
          <span class="fact__label">规则没了</span>
          <b :class="{ warn: (stats.missing || 0) > 0 }">{{ stats.missing || 0 }}</b>
        </div>
      </div>
      <el-alert v-if="note" type="info" :closable="false" show-icon class="fact-note">{{ note }}</el-alert>
    </el-card>

    <el-card shadow="never">
      <div class="toolbar">
        <el-input
          v-model="keyword"
          placeholder="名称 / 内容 / 说明"
          clearable
          style="width: 240px"
          @keyup.enter="load"
          @clear="load"
        />
        <el-select v-model="kindFilter" placeholder="全部类型" clearable style="width: 180px" @change="load">
          <el-option label="危险命令（command）" value="command" />
          <el-option label="端口暴露（port）" value="port" />
        </el-select>
        <el-button @click="load">查询</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="特征" min-width="170">
          <template #default="{ row }">
            {{ row.name }}
            <el-tag v-if="row.builtin" size="small" type="info" style="margin-left: 6px">内置</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="110">
          <template #default="{ row }">
            {{ row.kind === 'port' ? '端口暴露' : '危险命令' }}
          </template>
        </el-table-column>
        <el-table-column label="内容" min-width="230">
          <template #default="{ row }"><code class="mono">{{ row.pattern }}</code></template>
        </el-table-column>
        <el-table-column label="灰度" width="100">
          <template #default="{ row }">
            <el-tag :type="row.stage === 'enforce' ? 'danger' : 'warning'" size="small">
              {{ row.stage === 'enforce' ? '真拦' : '只提醒' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="程度" width="90">
          <template #default="{ row }">
            <el-tag
              :type="row.severity === 'high' ? 'danger' : row.severity === 'low' ? 'info' : 'warning'"
              size="small"
              effect="plain"
            >
              {{ row.severity }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70" align="center">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? '是' : '否' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="落地状态" min-width="220">
          <template #default="{ row }">
            <el-tag :type="statusMeta[row.applyStatus]?.type || 'info'" size="small">
              {{ statusMeta[row.applyStatus]?.text || row.applyStatus }}
            </el-tag>
            <div class="hint">{{ row.applyDetail }}</div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <template v-if="row.kind === 'command'">
              <el-button v-perm="'signature:apply'" link type="primary" @click="doApply(row)">
                {{ row.applyStatus === 'applied' ? '重写规则' : '应用' }}
              </el-button>
              <el-button
                v-perm="'signature:apply'"
                link
                type="warning"
                :disabled="!row.ruleId"
                @click="doRevoke(row)"
              >
                撤下
              </el-button>
            </template>
            <el-button v-else link type="primary" :loading="portChecking" @click="doPortCheck(row)">
              按扫描结果核对
            </el-button>
            <el-button v-perm="'signature:manage'" link @click="openEdit(row)">编辑</el-button>
            <el-button
              v-perm="'signature:manage'"
              link
              type="danger"
              :disabled="row.builtin"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-alert type="info" :closable="false" show-icon class="scope-note">
      <template #title>这一页不做的事</template>
      不做病毒特征与文件哈希比对（那需要常驻 agent 扫盘，纯 SSH 采样做出来是假的）；
      不做特征自动更新源（内网环境没有可信更新通道）；端口特征也不会自动改暴露面基线 ——
      「哪些端口允许开」是人的决定，平台只负责把「实际开着的」摆出来。
    </el-alert>

    <el-dialog v-model="dialog" :title="editingId ? '编辑特征' : '新增特征'" width="600px">
      <el-form :model="form" label-width="96px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如 递归删除根目录" />
        </el-form-item>
        <el-form-item label="类型" required>
          <el-radio-group v-model="form.kind" :disabled="!!editingId">
            <el-radio label="command">危险命令（落到命令规则）</el-radio>
            <el-radio label="port">端口暴露（对扫描结果）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="form.kind === 'port' ? '端口清单' : '正则'" required>
          <el-input v-model="form.pattern" :placeholder="form.kind === 'port' ? '如 22,3389,8000-8010' : '如 ^\\s*mkfs'" />
          <div class="hint">
            {{
              form.kind === 'port'
                ? '支持单个端口与区间混写；保存时会校验并归一化'
                : 'Go 正则，保存时当场编译校验 —— 编译不过的特征等于在闸门里留一个永远不生效的洞'
            }}
          </div>
        </el-form-item>
        <el-form-item label="说明">
          <el-input v-model="form.description" placeholder="为什么这条危险，一句话" />
        </el-form-item>
        <el-form-item v-if="form.kind === 'command'" label="灰度">
          <el-radio-group v-model="form.stage">
            <el-radio label="observe">只提醒（warn）</el-radio>
            <el-radio label="enforce">真拦（block）</el-radio>
          </el-radio-group>
          <div class="hint">先 observe 跑一段、确认没有误伤，再升到 enforce</div>
        </el-form-item>
        <el-form-item label="严重程度">
          <el-select v-model="form.severity" style="width: 140px">
            <el-option label="high" value="high" />
            <el-option label="medium" value="medium" />
            <el-option label="low" value="low" />
          </el-select>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
          <div class="hint">停用之后，已应用的那条命令规则也会被同步关掉</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="portDialog" title="按真机扫描结果核对" width="720px">
      <template v-if="portResult">
        <el-alert type="info" :closable="false" show-icon style="margin-bottom: 10px">
          特征端口：{{ portResult.ports }}。{{ portResult.detail }}
        </el-alert>
        <el-table :data="portResult.hits" border stripe size="small">
          <el-table-column prop="name" label="目标" min-width="130" />
          <el-table-column prop="address" label="地址" min-width="140" />
          <el-table-column prop="hitPorts" label="命中端口" min-width="110" />
          <el-table-column prop="note" label="说明" min-width="190" />
          <el-table-column prop="lastScanAt" label="扫描时间" min-width="170" />
        </el-table>
        <div v-if="portResult.hits.length === 0" class="hint" style="margin-top: 8px">
          没有目标命中这条特征。注意「没有数据」与「没有风险」不是一回事 —— 从没扫过的目标不算通过。
        </div>
      </template>
      <template #footer>
        <el-button @click="portDialog = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.fact-card {
  margin-bottom: 12px;
}
.facts {
  display: flex;
  gap: 20px;
  flex-wrap: wrap;
  align-items: center;
}
.fact {
  font-size: 13px;
}
.fact__label {
  color: var(--ops-text-secondary, #9ca3af);
  margin-right: 6px;
}
.fact-note {
  margin-top: 10px;
}
.hint {
  font-size: 12px;
  color: var(--ops-text-secondary, #9ca3af);
  line-height: 1.5;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.warn {
  color: #e6a23c;
}
.scope-note {
  margin-top: 12px;
}
</style>
