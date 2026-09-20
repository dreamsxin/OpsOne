<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createOnCallOverride,
  createOnCallSchedule,
  deleteOnCallOverride,
  deleteOnCallSchedule,
  listAlertEscalations,
  listOnCallCandidates,
  listOnCallOverrides,
  listOnCallSchedules,
  previewOnCall,
  runOnCallEscalation,
  updateOnCallSchedule,
  type AlertEscalation,
  type OnCallOverride,
  type OnCallRotation,
  type OnCallSchedule,
  type OnCallShift,
  type UserBrief
} from '@/api'

const loading = ref(false)
const rows = ref<OnCallSchedule[]>([])
const candidates = ref<UserBrief[]>([])
const rotations = ref<{ key: OnCallRotation; label: string }[]>([])
const escalations = ref<AlertEscalation[]>([])
const escalationTotal = ref(0)

const dialog = reactive({
  visible: false,
  id: 0,
  name: '',
  members: [] as number[],
  rotation: 'daily' as OnCallRotation,
  startAt: '',
  matchSeverity: [] as string[],
  ackWaitMinutes: 10,
  maxLevel: 1,
  notifyEmail: false,
  enabled: true,
  remark: ''
})

const detail = reactive({
  visible: false,
  schedule: null as OnCallSchedule | null,
  days: 7,
  shifts: [] as OnCallShift[],
  overrides: [] as OnCallOverride[],
  loading: false
})

const overrideForm = reactive({ userId: undefined as number | undefined, range: null as [string, string] | null, reason: '' })

async function load() {
  loading.value = true
  try {
    rows.value = await listOnCallSchedules()
  } finally {
    loading.value = false
  }
}

async function loadEscalations() {
  const data = await listAlertEscalations({ page: 1, pageSize: 20 })
  escalations.value = data.list || []
  escalationTotal.value = data.total
}

function openCreate() {
  Object.assign(dialog, {
    visible: true,
    id: 0,
    name: '',
    members: [],
    rotation: 'daily' as OnCallRotation,
    startAt: '',
    matchSeverity: [],
    ackWaitMinutes: 10,
    maxLevel: 1,
    notifyEmail: false,
    enabled: true,
    remark: ''
  })
}

function openEdit(row: OnCallSchedule) {
  Object.assign(dialog, {
    visible: true,
    id: row.id,
    name: row.name,
    members: row.memberList.map((m) => m.id),
    rotation: row.rotation,
    startAt: row.startAt.replace('T', ' ').slice(0, 16),
    matchSeverity: row.matchSeverity ? row.matchSeverity.split(',') : [],
    ackWaitMinutes: row.ackWaitMinutes,
    maxLevel: row.maxLevel,
    notifyEmail: row.notifyEmail,
    enabled: row.enabled,
    remark: row.remark
  })
}

/** 成员顺序就是轮换顺序与升级顺序，所以要能调 */
function moveMember(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= dialog.members.length) return
  const list = dialog.members
  ;[list[index], list[target]] = [list[target], list[index]]
}

function memberName(id: number) {
  const user = candidates.value.find((u) => u.id === id)
  return user ? `${user.username}${user.nickname ? '（' + user.nickname + '）' : ''}` : `#${id}`
}

async function submit() {
  const payload = {
    name: dialog.name.trim(),
    members: dialog.members,
    rotation: dialog.rotation,
    startAt: dialog.startAt,
    matchSeverity: dialog.matchSeverity.join(','),
    ackWaitMinutes: dialog.ackWaitMinutes,
    maxLevel: dialog.maxLevel,
    notifyEmail: dialog.notifyEmail,
    enabled: dialog.enabled,
    remark: dialog.remark
  }
  try {
    if (dialog.id) {
      await updateOnCallSchedule(dialog.id, payload)
    } else {
      await createOnCallSchedule(payload)
    }
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
    return
  }
  dialog.visible = false
  await load()
  ElMessage.success('已保存')
}

async function remove(row: OnCallSchedule) {
  await ElMessageBox.confirm(`删除值班表「${row.name}」？其代班会一起删掉，历史升级记录保留。`, '确认', {
    type: 'warning'
  })
  const res = await deleteOnCallSchedule(row.id)
  await load()
  ElMessage.success(res.detail)
}

async function runNow(row: OnCallSchedule) {
  const res = await runOnCallEscalation(row.id)
  await loadEscalations()
  ElMessage({ type: res.called > 0 ? 'warning' : 'success', message: res.detail })
}

async function openDetail(row: OnCallSchedule) {
  detail.visible = true
  detail.schedule = row
  await loadDetail()
}

async function loadDetail() {
  if (!detail.schedule) return
  detail.loading = true
  try {
    const [preview, overrides] = await Promise.all([
      previewOnCall(detail.schedule.id, detail.days),
      listOnCallOverrides(detail.schedule.id)
    ])
    detail.shifts = preview.shifts || []
    detail.overrides = overrides || []
  } finally {
    detail.loading = false
  }
}

async function submitOverride() {
  if (!detail.schedule) return
  if (!overrideForm.userId || !overrideForm.range) {
    ElMessage.warning('请选择代班人与时间段')
    return
  }
  try {
    await createOnCallOverride(detail.schedule.id, {
      userId: overrideForm.userId,
      startAt: overrideForm.range[0],
      endAt: overrideForm.range[1],
      reason: overrideForm.reason
    })
  } catch (err: any) {
    ElMessage.error(err?.message || '添加代班失败')
    return
  }
  overrideForm.userId = undefined
  overrideForm.range = null
  overrideForm.reason = ''
  await loadDetail()
  await load()
  ElMessage.success('代班已添加')
}

async function removeOverride(item: OnCallOverride) {
  if (!detail.schedule) return
  await deleteOnCallOverride(detail.schedule.id, item.id)
  await loadDetail()
  await load()
  ElMessage.success('代班已删除')
}

function fmt(value: string | null) {
  return value ? value.replace('T', ' ').slice(0, 16) : '—'
}

onMounted(async () => {
  await load()
  await loadEscalations()
  try {
    const meta = await listOnCallCandidates()
    candidates.value = meta.users || []
    rotations.value = meta.rotations || []
  } catch {
    // 候选人拿不到只影响下拉
  }
})
</script>

<template>
  <div class="page">
    <el-card>
      <div class="page-toolbar">
        <el-button v-perm="'oncall:manage'" type="primary" @click="openCreate">新建值班表</el-button>
        <el-button @click="((load(), loadEscalations()))">刷新</el-button>
      </div>

      <el-alert type="info" :closable="false" style="margin-bottom: 12px">
        <template #title>
          <span
            >和通知路由的分工：<b>路由决定发到哪个渠道</b>（群机器人、邮件组），<b>值班表决定叫哪个人</b>。告警出现即通知当班人（站内消息，可选同时发邮件），<code>{{
              '未确认'
            }}</code>
            超过设定分钟数就按成员顺序升到下一级，确认（或恢复）后停止。成员顺序既是轮换顺序也是升级顺序。为避免新建表时把积压的老告警全叫一遍，只处理
            24 小时内、且在本表创建之后出现的告警。默认每分钟扫一次（<code>OPS_ONCALL_SPEC</code>
            可改或关闭）。</span
          >
        </template>
      </el-alert>

      <el-table v-loading="loading" :data="rows" border stripe>
        <el-table-column prop="name" label="值班表" min-width="130" />
        <el-table-column label="当班" min-width="160">
          <template #default="{ row }">
            <template v-if="row.current">
              <el-tag size="small" :type="row.current.source === 'override' ? 'warning' : 'success'">
                {{ row.current.userName }}
              </el-tag>
              <span v-if="row.current.source === 'override'" style="margin-left: 4px; font-size: 12px">
                代班
              </span>
            </template>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column label="轮换" width="150">
          <template #default="{ row }">
            <div>{{ row.rotationLabel }} · {{ row.memberList.length }} 人</div>
            <div style="color: var(--el-text-color-secondary); font-size: 12px">
              下次交接 {{ fmt(row.nextRotateAt) }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="升级" width="140">
          <template #default="{ row }">
            <span v-if="row.maxLevel <= 1">只叫当班</span>
            <span v-else>{{ row.ackWaitMinutes }} 分钟未确认升一级，最多 {{ row.maxLevel }} 级</span>
          </template>
        </el-table-column>
        <el-table-column label="级别范围" width="120">
          <template #default="{ row }">{{ row.matchSeverity || '全部级别' }}</template>
        </el-table-column>
        <el-table-column label="邮件" width="70">
          <template #default="{ row }">
            <el-tag size="small" :type="row.notifyEmail ? 'success' : 'info'">
              {{ row.notifyEmail ? '发' : '不发' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'">
              {{ row.enabled ? '是' : '否' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">排班/代班</el-button>
            <el-button v-perm="'oncall:manage'" link type="primary" @click="runNow(row)">试跑</el-button>
            <el-button v-perm="'oncall:manage'" link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button v-perm="'oncall:manage'" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card style="margin-top: 12px">
      <template #header>
        <span>最近的叫人记录（共 {{ escalationTotal }} 条）</span>
      </template>
      <el-table :data="escalations" border stripe>
        <el-table-column label="告警" min-width="220">
          <template #default="{ row }">{{ row.alertTitle || '#' + row.alertId }}</template>
        </el-table-column>
        <el-table-column label="级别" width="90">
          <template #default="{ row }">第 {{ row.level + 1 }} 级</template>
        </el-table-column>
        <el-table-column prop="userName" label="叫的人" width="130" />
        <el-table-column label="来源" width="90">
          <template #default="{ row }">{{ row.source === 'override' ? '代班' : '轮换' }}</template>
        </el-table-column>
        <el-table-column label="渠道" width="90">
          <template #default="{ row }">{{ row.channel === 'email' ? '邮件' : '站内消息' }}</template>
        </el-table-column>
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status === 'sent' ? 'success' : 'danger'">
              {{ row.status === 'sent' ? '已送达' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="detail" label="说明" min-width="180" show-overflow-tooltip />
        <el-table-column label="时间" min-width="160">
          <template #default="{ row }">{{ fmt(row.createdAt) }}</template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog
      v-model="dialog.visible"
      :title="dialog.id ? '编辑值班表' : '新建值班表'"
      width="680px"
    >
      <el-form label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="dialog.name" placeholder="例如 工作日白班" />
        </el-form-item>
        <el-form-item label="值班成员">
          <el-select
            v-model="dialog.members"
            multiple
            filterable
            placeholder="选择成员，顺序就是轮换与升级顺序"
            style="width: 100%"
          >
            <el-option
              v-for="user in candidates"
              :key="user.id"
              :label="`${user.username}${user.nickname ? '（' + user.nickname + '）' : ''}`"
              :value="user.id"
            />
          </el-select>
          <div v-if="dialog.members.length" class="member-order">
            <div v-for="(id, index) in dialog.members" :key="id" class="member-item">
              <span>{{ index + 1 }}. {{ memberName(id) }}</span>
              <el-button link :disabled="index === 0" @click="moveMember(index, -1)">上移</el-button>
              <el-button
                link
                :disabled="index === dialog.members.length - 1"
                @click="moveMember(index, 1)"
              >
                下移
              </el-button>
            </div>
          </div>
        </el-form-item>
        <el-form-item label="轮换周期">
          <el-radio-group v-model="dialog.rotation">
            <el-radio v-for="item in rotations" :key="item.key" :value="item.key">
              {{ item.label }}
            </el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="轮换基准时间">
          <el-date-picker
            v-model="dialog.startAt"
            type="datetime"
            value-format="YYYY-MM-DD HH:mm"
            placeholder="留空取当前整点"
            style="width: 220px"
          />
          <span style="margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px">
            交接时刻就是这个时间的时分
          </span>
        </el-form-item>
        <el-form-item label="只叫这些级别">
          <el-select v-model="dialog.matchSeverity" multiple placeholder="留空表示全部级别" style="width: 260px">
            <el-option label="info" value="info" />
            <el-option label="warning" value="warning" />
            <el-option label="critical" value="critical" />
          </el-select>
        </el-form-item>
        <el-form-item label="升级">
          <el-input-number v-model="dialog.ackWaitMinutes" :min="1" :max="1440" />
          <span style="margin: 0 8px">分钟未确认升一级，最多</span>
          <el-input-number v-model="dialog.maxLevel" :min="1" :max="20" />
          <span style="margin-left: 8px">级</span>
          <div style="color: var(--el-text-color-secondary); font-size: 12px">
            级数不能超过成员人数，否则会转回同一个人反复骚扰；填 1 表示只叫当班
          </div>
        </el-form-item>
        <el-form-item label="同时发邮件">
          <el-switch v-model="dialog.notifyEmail" />
          <span style="margin-left: 8px; color: var(--el-text-color-secondary); font-size: 12px">
            需要 SMTP 已配置且用户填了邮箱；站内消息始终发送
          </span>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="dialog.remark" />
          <el-checkbox v-model="dialog.enabled" style="margin-top: 8px">启用</el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <el-drawer
      v-model="detail.visible"
      :title="detail.schedule ? detail.schedule.name + ' 的排班与代班' : ''"
      size="65%"
    >
      <div v-loading="detail.loading">
        <el-divider content-position="left">代班（只影响当班这一级）</el-divider>
        <div class="page-toolbar">
          <el-select v-model="overrideForm.userId" filterable placeholder="代班人" style="width: 180px">
            <el-option
              v-for="user in candidates"
              :key="user.id"
              :label="user.username"
              :value="user.id"
            />
          </el-select>
          <el-date-picker
            v-model="overrideForm.range"
            type="datetimerange"
            value-format="YYYY-MM-DD HH:mm"
            start-placeholder="开始"
            end-placeholder="结束"
            style="width: 340px"
          />
          <el-input v-model="overrideForm.reason" placeholder="原因（可选）" style="width: 160px" />
          <el-button v-perm="'oncall:manage'" type="primary" @click="submitOverride">添加</el-button>
        </div>
        <el-table :data="detail.overrides" border stripe>
          <el-table-column prop="userName" label="代班人" width="130" />
          <el-table-column label="时间段" min-width="240">
            <template #default="{ row }">{{ fmt(row.startAt) }} ~ {{ fmt(row.endAt) }}</template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="row.active ? 'warning' : 'info'">
                {{ row.active ? '生效中' : '未生效' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="reason" label="原因" min-width="140" />
          <el-table-column label="操作" width="80">
            <template #default="{ row }">
              <el-button v-perm="'oncall:manage'" link type="danger" @click="removeOverride(row)">
                删除
              </el-button>
            </template>
          </el-table-column>
        </el-table>

        <el-divider content-position="left">未来排班</el-divider>
        <div class="page-toolbar">
          <el-radio-group v-model="detail.days" @change="loadDetail">
            <el-radio-button :value="3">3 天</el-radio-button>
            <el-radio-button :value="7">7 天</el-radio-button>
            <el-radio-button :value="14">14 天</el-radio-button>
          </el-radio-group>
        </div>
        <el-table :data="detail.shifts" border stripe>
          <el-table-column label="时间段" min-width="260">
            <template #default="{ row }">{{ fmt(row.startAt) }} ~ {{ fmt(row.endAt) }}</template>
          </el-table-column>
          <el-table-column label="值班人" min-width="150">
            <template #default="{ row }">
              <template v-if="row.person">
                {{ row.person.userName }}
                <el-tag v-if="row.person.source === 'override'" size="small" type="warning">代班</el-tag>
              </template>
              <span v-else>—</span>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.member-order {
  margin-top: 8px;
  width: 100%;
}
.member-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  line-height: 24px;
}
</style>
