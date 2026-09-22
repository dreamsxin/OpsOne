<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  createNotifyChannel,
  deleteNotifyChannel,
  listEmailTemplates,
  listMailAccounts,
  listNotifyChannels,
  testNotifyChannel,
  updateNotifyChannel,
  type EmailTemplate,
  type MailAccount,
  type NotifyChannel
} from '@/api'

const loading = ref(false)
const rows = ref<NotifyChannel[]>([])
const templates = ref<EmailTemplate[]>([])
const mailAccounts = ref<MailAccount[]>([])

type ChannelType = 'webhook' | 'email' | 'silent' | 'wecom' | 'dingtalk' | 'feishu'
const imTypes: ChannelType[] = ['wecom', 'dingtalk', 'feishu']
const typeLabel: Record<string, string> = {
  webhook: 'webhook',
  email: 'email',
  silent: 'silent',
  wecom: '企业微信',
  dingtalk: '钉钉',
  feishu: '飞书'
}
const isIM = (type: string) => imTypes.includes(type as ChannelType)
// 各家 @ 人的标识不一样，表单提示要跟着变
const mentionHint: Record<string, string> = {
  wecom: '逗号分隔的成员 userid，留空则不 @ 人',
  dingtalk: '逗号分隔的手机号（钉钉按手机号 @ 人，号码会附在正文末尾才会高亮）',
  feishu: '飞书 @ 单人需要 open_id，平台不做通讯录同步，只支持「@所有人」'
}

const dialogVisible = ref(false)
const editingId = ref<number | null>(null)
const formRef = ref<FormInstance>()
const form = reactive({
  name: '',
  type: 'webhook' as ChannelType,
  url: '',
  headerKey: '',
  headerValue: '',
  recipients: '',
  templateCode: '',
  mailAccountId: 0,
  secret: '',
  mentionList: '',
  mentionAll: false,
  remark: '',
  enabled: true
})


const rules = {
  name: [{ required: true, message: '请输入渠道名称', trigger: 'blur' }]
}

async function load() {
  loading.value = true
  try {
    rows.value = await listNotifyChannels()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '',
    type: 'webhook',
    url: '',
    headerKey: '',
    headerValue: '',
    recipients: '',
    templateCode: '',
    mailAccountId: 0,
    secret: '',
    mentionList: '',
    mentionAll: false,
    remark: '',
    enabled: true
  })
  dialogVisible.value = true
}

function openEdit(row: NotifyChannel) {
  editingId.value = row.id
  Object.assign(form, {
    name: row.name,
    type: row.type,
    url: row.url,
    headerKey: row.headerKey,
    headerValue: '',
    recipients: row.recipients,
    templateCode: row.templateCode,
    mailAccountId: row.mailAccountId || 0,
    secret: '',
    mentionList: row.mentionList || '',
    mentionAll: row.mentionAll || false,
    remark: row.remark,
    enabled: row.enabled
  })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return
  if ((form.type === 'webhook' || isIM(form.type)) && !form.url.trim()) {
    ElMessage.warning(`${typeLabel[form.type]} 渠道必须填写地址`)
    return
  }
  if (form.type === 'email' && !form.recipients.trim()) {
    ElMessage.warning('email 渠道必须填写收件人')
    return
  }
  if (form.type === 'feishu' && form.mentionList.trim()) {
    ElMessage.warning('飞书只支持 @所有人，请清空 @ 名单')
    return
  }

  try {
    if (editingId.value) {
      await updateNotifyChannel(editingId.value, { ...form })
      ElMessage.success('已更新')
    } else {
      await createNotifyChannel({ ...form })
      ElMessage.success('已创建')
    }
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
    return
  }
  dialogVisible.value = false
  load()
}


async function toggleEnabled(row: NotifyChannel) {
  await updateNotifyChannel(row.id, {
    name: row.name,
    type: row.type,
    url: row.url,
    headerKey: row.headerKey,
    recipients: row.recipients,
    templateCode: row.templateCode,
    mailAccountId: row.mailAccountId || 0,
    mentionList: row.mentionList,
    mentionAll: row.mentionAll,
    remark: row.remark,
    enabled: row.enabled
  })
  ElMessage.success(row.enabled ? '已启用' : '已停用')
}


async function test(row: NotifyChannel) {
  const res = await testNotifyChannel(row.id)
  if (res.ok) {
    ElMessage.success(`发送成功（HTTP ${res.httpStatus ?? '-'}，${res.costMs ?? 0}ms）`)
  } else {
    ElMessage.error(`发送失败：${res.detail}`)
  }
}

async function remove(row: NotifyChannel) {
  await ElMessageBox.confirm(`确认删除渠道「${row.name}」？`, '提示', { type: 'warning' })
  await deleteNotifyChannel(row.id)
  ElMessage.success('已删除')
  load()
}

onMounted(async () => {
  templates.value = await listEmailTemplates()
  mailAccounts.value = await listMailAccounts()
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
        title="webhook 以 HTTP POST 投递固定 JSON；企业微信 / 钉钉 / 飞书按各家群机器人报文发纯文本消息（HTTP 200 但 errcode 非零也算失败）；email 走 SMTP 并套用邮件模板；silent 只落通知记录、不外发，可用于灰度或临时静默"

      />

      <div class="page-toolbar">
        <div class="grow"></div>
        <el-button v-perm="'channel:manage'" type="primary" @click="openCreate">新增渠道</el-button>
      </div>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="name" label="渠道" min-width="140" />
        <el-table-column label="类型" width="110">
          <template #default="{ row }">
            <el-tag
              size="small"
              :type="row.type === 'email' ? 'success' : isIM(row.type) ? 'warning' : row.type === 'silent' ? 'info' : 'primary'"
            >
              {{ typeLabel[row.type] || row.type }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="投递目标" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.type === 'email'">
              {{ row.recipients }}
              <el-tag v-if="row.templateCode" size="small" style="margin-left: 4px">
                {{ row.templateCode }}
              </el-tag>
            </span>
            <span v-else-if="row.type === 'silent'" style="color: #9ca3af">不外发</span>
            <span v-else>{{ row.url }}</span>
          </template>
        </el-table-column>
        <el-table-column label="@ 提醒" width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="!isIM(row.type)">—</span>
            <span v-else>
              <el-tag v-if="row.mentionAll" size="small" type="danger">@所有人</el-tag>
              <span v-if="row.mentionList" style="margin-left: 4px">{{ row.mentionList }}</span>
              <span v-if="!row.mentionAll && !row.mentionList">不 @</span>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="headerKey" label="鉴权头" width="110" />

        <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
        <el-table-column label="启用" width="90">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'channel:manage'" link type="primary" @click="test(row)">试发</el-button>
            <el-button v-perm="'channel:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'channel:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑渠道' : '新增渠道'" width="520px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="渠道名称" prop="name">
          <el-input v-model="form.name" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type">
            <el-radio value="webhook">webhook</el-radio>
            <el-radio value="wecom">企业微信</el-radio>
            <el-radio value="dingtalk">钉钉</el-radio>
            <el-radio value="feishu">飞书</el-radio>
            <el-radio value="email">email</el-radio>
            <el-radio value="silent">silent（仅记录）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook'" label="URL">
          <el-input v-model="form.url" placeholder="https://..." />
        </el-form-item>
        <el-form-item v-if="isIM(form.type)" label="机器人地址">
          <el-input v-model="form.url" placeholder="群机器人的 Webhook 地址" />
          <div class="hint">在群设置里添加机器人后复制，地址本身就是凭据，别外传</div>
        </el-form-item>
        <el-form-item v-if="form.type === 'dingtalk' || form.type === 'feishu'" label="签名密钥">
          <el-input
            v-model="form.secret"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '可选：钉钉「加签」/ 飞书「签名校验」的密钥'"
          />
          <div class="hint">
            {{ form.type === 'dingtalk'
              ? '钉钉若用「自定义关键词」校验，请把关键词写进告警标题，或改用加签'
              : '飞书开启签名校验后必填，否则会返回 sign match fail' }}
          </div>
        </el-form-item>
        <el-form-item v-if="isIM(form.type)" label="@ 名单">
          <el-input
            v-model="form.mentionList"
            :disabled="form.type === 'feishu'"
            placeholder="可选"
          />
          <div class="hint">{{ mentionHint[form.type] }}</div>
        </el-form-item>
        <el-form-item v-if="isIM(form.type)" label="@所有人">
          <el-switch v-model="form.mentionAll" />
          <span class="hint" style="margin-left: 8px">谨慎开启：每条告警都会 @ 全群</span>
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook' || isIM(form.type)" label="鉴权头名">
          <el-input v-model="form.headerKey" placeholder="可选，如 X-Token；机器人挂在自建网关后面时用" />
        </el-form-item>
        <el-form-item v-if="form.type === 'webhook' || isIM(form.type)" label="鉴权头值">
          <el-input
            v-model="form.headerValue"
            type="password"
            show-password
            :placeholder="editingId ? '留空表示不修改' : '可选'"
          />
        </el-form-item>
        <el-form-item v-if="form.type === 'email'" label="收件人">
          <el-input v-model="form.recipients" placeholder="逗号分隔，如 ops@example.com,sre@example.com" />
        </el-form-item>
        <el-form-item v-if="form.type === 'email'" label="发件邮箱">
          <el-select v-model="form.mailAccountId" style="width: 100%">
            <el-option :value="0" label="用默认发件邮箱（没登记则用系统配置里的全局 SMTP）" />
            <el-option
              v-for="acc in mailAccounts.filter((a) => a.enabled)"
              :key="acc.id"
              :value="acc.id"
              :label="`${acc.name}（${acc.effectiveFrom}）${acc.isDefault ? ' · 默认' : ''}`"
            />
          </el-select>
          <el-text type="info" size="small" style="display: block; margin-top: 4px">
            在「配置中心 → 发件邮箱」登记。指定的邮箱被删或停用时，这个渠道的邮件会直接失败并点名原因，不会静默换发件人
          </el-text>
        </el-form-item>
        <el-form-item v-if="form.type === 'email'" label="邮件模板">
          <el-select v-model="form.templateCode" clearable style="width: 100%" placeholder="默认使用内置告警模板">
            <el-option
              v-for="tpl in templates.filter((t) => t.enabled)"
              :key="tpl.id"
              :label="`${tpl.name}（${tpl.code}）`"
              :value="tpl.code"
            />
          </el-select>
        </el-form-item>
        <el-alert
          v-if="form.type === 'email'"
          type="info"
          :closable="false"
          style="margin: 0 0 18px 90px; width: calc(100% - 90px)"
          title="SMTP 服务器、账号、发件人等参数在「系统管理 → 系统配置」的 SMTP 分组里配置"
        />

        <el-form-item label="备注">
          <el-input v-model="form.remark" />
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

<style scoped>
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.5;
}
</style>
