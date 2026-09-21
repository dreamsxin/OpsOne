<script setup lang="ts">
/**
 * 安全意识（培训与考核）。
 *
 * 这一页分两块：上面是**我的必修**（人人可见），下面是**管理**（需要 awareness:manage）。
 *
 * 几条刻意的设计：
 *   - 确认已读是一条签署记录（人、时间、来源 IP），不是前端一个勾选框
 *   - 题目的正确答案不会下发到浏览器，判分在后端；所以这里只提交选择、等后端给分
 *   - 「发布」会真的给范围内的人投站内消息；逾期由定时任务再催一次，不是摆设
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  confirmAwarenessRead,
  createAwarenessQuestion,
  deleteAwarenessCourse,
  deleteAwarenessQuestion,
  getMyAwarenessCourse,
  listAwarenessCourses,
  listAwarenessQuestions,
  listAwarenessRecords,
  listMyAwareness,
  publishAwarenessCourse,
  remindAwarenessCourse,
  saveAwarenessCourse,
  submitAwarenessQuiz,
  unpublishAwarenessCourse,
  updateAwarenessQuestion,
  type AwarenessCourse,
  type AwarenessQuestion,
  type MyAwarenessItem
} from '@/api'
import PageHeader from '@/components/PageHeader.vue'

/* ---------------- 我的必修 ---------------- */

const myLoading = ref(false)
const myList = ref<MyAwarenessItem[]>([])
const myPending = ref(0)

const studyVisible = ref(false)
const studyLoading = ref(false)
const study = ref<any>(null)
const answers = reactive<Record<string, number[]>>({})
const quizResult = ref<any>(null)

async function loadMy() {
  myLoading.value = true
  try {
    const res = await listMyAwareness()
    myList.value = res.list || []
    myPending.value = res.pending
  } catch (err: any) {
    ElMessage.error(err?.message || '读取必修项失败')
  } finally {
    myLoading.value = false
  }
}

async function openStudy(row: MyAwarenessItem) {
  studyLoading.value = true
  quizResult.value = null
  Object.keys(answers).forEach((k) => delete answers[k])
  try {
    study.value = await getMyAwarenessCourse(row.id)
    for (const q of study.value.questions) answers[String(q.id)] = []
    studyVisible.value = true
  } catch (err: any) {
    ElMessage.error(err?.message || '打开失败')
  } finally {
    studyLoading.value = false
  }
}

async function doConfirmRead() {
  try {
    const res = await confirmAwarenessRead(study.value.course.id)
    ElMessage.success(res.detail)
    study.value.record = res.record
    loadMy()
  } catch (err: any) {
    ElMessage.error(err?.message || '确认失败')
  }
}

async function doSubmitQuiz() {
  const unanswered = study.value.questions.filter((q: any) => (answers[String(q.id)] || []).length === 0)
  if (unanswered.length > 0) {
    ElMessage.warning(`还有 ${unanswered.length} 道题没答`)
    return
  }
  try {
    const res = await submitAwarenessQuiz(study.value.course.id, { ...answers })
    quizResult.value = res
    if (res.status === 'passed') ElMessage.success(res.detail)
    else ElMessage.warning(res.detail)
    loadMy()
  } catch (err: any) {
    ElMessage.error(err?.message || '交卷失败')
  }
}

function reviewOf(id: number) {
  return quizResult.value?.review?.find((r: any) => r.id === id)
}

/** 单选题只保留最后一次选择：用 checkbox 统一渲染，单选靠这里收口 */
function pickOption(q: any, idx: number) {
  const key = String(q.id)
  if (!q.multi && (answers[key] || []).length > 1) answers[key] = [idx]
}

/* ---------------- 管理端 ---------------- */

const adminLoading = ref(false)
const courses = ref<AwarenessCourse[]>([])
const adminNote = ref('')

const courseDialog = ref(false)
const editingCourse = ref<number | null>(null)
const courseForm = reactive({
  title: '',
  summary: '',
  content: '',
  scope: 'all' as 'all' | 'role' | 'dept',
  scopeRoleId: 0,
  scopeDeptId: 0,
  passScore: 80,
  dueAt: ''
})

const questionDialog = ref(false)
const questionCourse = ref<AwarenessCourse | null>(null)
const questions = ref<AwarenessQuestion[]>([])
const questionForm = reactive({
  id: null as number | null,
  content: '',
  optionsText: '',
  answer: [] as number[],
  multi: false,
  explain: '',
  sort: 0
})
const optionPreview = computed(() =>
  questionForm.optionsText
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
)

const recordDialog = ref(false)
const recordCourse = ref<AwarenessCourse | null>(null)
const records = ref<any[]>([])
const recordStats = ref<Record<string, number>>({})

async function loadCourses() {
  adminLoading.value = true
  try {
    const res = await listAwarenessCourses()
    courses.value = res.list || []
    adminNote.value = res.note
  } catch (err: any) {
    // 没有 awareness:manage 的人读不到管理列表，这不是错误
    courses.value = []
  } finally {
    adminLoading.value = false
  }
}

function openCourseCreate() {
  editingCourse.value = null
  Object.assign(courseForm, {
    title: '',
    summary: '',
    content: '',
    scope: 'all',
    scopeRoleId: 0,
    scopeDeptId: 0,
    passScore: 80,
    dueAt: ''
  })
  courseDialog.value = true
}

function openCourseEdit(row: AwarenessCourse) {
  editingCourse.value = row.id
  Object.assign(courseForm, {
    title: row.title,
    summary: row.summary,
    content: row.content,
    scope: row.scope,
    scopeRoleId: row.scopeRoleId,
    scopeDeptId: row.scopeDeptId,
    passScore: row.passScore,
    dueAt: row.dueAt ? row.dueAt.slice(0, 19).replace('T', ' ') : ''
  })
  courseDialog.value = true
}

async function submitCourse() {
  if (!courseForm.title.trim() || !courseForm.content.trim()) {
    ElMessage.warning('标题与正文都要填')
    return
  }
  try {
    await saveAwarenessCourse(editingCourse.value, { ...courseForm })
    ElMessage.success('已保存')
    courseDialog.value = false
    loadCourses()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function doPublish(row: AwarenessCourse) {
  await ElMessageBox.confirm(
    `发布「${row.title}」：会给「${row.scopeLabel}」里所有启用中的账号建待办并投一条站内消息。`,
    '发布',
    { type: 'warning' }
  )
  try {
    const res = await publishAwarenessCourse(row.id)
    ElMessage.success(res.detail)
    loadCourses()
    loadMy()
  } catch (err: any) {
    ElMessage.error(err?.message || '发布失败')
  }
}

async function doUnpublish(row: AwarenessCourse) {
  await ElMessageBox.confirm('下线之后它不再出现在必修列表里；已有的签署与成绩都保留。', '下线', {
    type: 'warning'
  })
  try {
    const res = await unpublishAwarenessCourse(row.id)
    ElMessage.success(res.detail)
    loadCourses()
    loadMy()
  } catch (err: any) {
    ElMessage.error(err?.message || '下线失败')
  }
}

async function doRemind(row: AwarenessCourse) {
  try {
    const res = await remindAwarenessCourse(row.id)
    ElMessage.success(res.detail)
  } catch (err: any) {
    ElMessage.error(err?.message || '催办失败')
  }
}

async function removeCourse(row: AwarenessCourse) {
  await ElMessageBox.confirm(`删除「${row.title}」？已经有人留下记录的项目不允许删除。`, '提示', {
    type: 'warning'
  })
  try {
    await deleteAwarenessCourse(row.id)
    ElMessage.success('已删除')
    loadCourses()
  } catch (err: any) {
    ElMessage.error(err?.message || '删除失败')
  }
}

async function openQuestions(row: AwarenessCourse) {
  questionCourse.value = row
  resetQuestionForm()
  try {
    questions.value = await listAwarenessQuestions(row.id)
    questionDialog.value = true
  } catch (err: any) {
    ElMessage.error(err?.message || '读取题目失败')
  }
}

function resetQuestionForm() {
  Object.assign(questionForm, {
    id: null,
    content: '',
    optionsText: '',
    answer: [],
    multi: false,
    explain: '',
    sort: 0
  })
}

function editQuestion(q: AwarenessQuestion) {
  Object.assign(questionForm, {
    id: q.id,
    content: q.content,
    optionsText: (q.optionList || []).join('\n'),
    answer: [...(q.answerList || [])],
    multi: q.multi,
    explain: q.explain,
    sort: q.sort
  })
}

async function submitQuestion() {
  if (!questionForm.content.trim()) {
    ElMessage.warning('题干不能为空')
    return
  }
  if (optionPreview.value.length < 2) {
    ElMessage.warning('至少两个选项（一行一个）')
    return
  }
  if (questionForm.answer.length === 0) {
    ElMessage.warning('要勾选正确答案')
    return
  }
  const payload = {
    content: questionForm.content,
    options: optionPreview.value,
    answer: questionForm.answer,
    multi: questionForm.multi,
    explain: questionForm.explain,
    sort: questionForm.sort
  }
  try {
    if (questionForm.id) await updateAwarenessQuestion(questionForm.id, payload)
    else await createAwarenessQuestion(questionCourse.value!.id, payload)
    ElMessage.success('已保存')
    questions.value = await listAwarenessQuestions(questionCourse.value!.id)
    resetQuestionForm()
    loadCourses()
  } catch (err: any) {
    ElMessage.error(err?.message || '保存失败')
  }
}

async function removeQuestion(q: AwarenessQuestion) {
  await ElMessageBox.confirm('删除这道题？', '提示', { type: 'warning' })
  try {
    await deleteAwarenessQuestion(q.id)
    questions.value = await listAwarenessQuestions(questionCourse.value!.id)
    loadCourses()
  } catch (err: any) {
    ElMessage.error(err?.message || '删除失败')
  }
}

async function openRecords(row: AwarenessCourse) {
  try {
    const res = await listAwarenessRecords(row.id)
    recordCourse.value = res.course
    records.value = res.list || []
    recordStats.value = res.stats || {}
    recordDialog.value = true
  } catch (err: any) {
    ElMessage.error(err?.message || '读取完成情况失败')
  }
}

const statusText: Record<string, string> = {
  pending: '未开始',
  read: '已确认已读',
  passed: '已通过',
  failed: '未及格'
}
const statusType: Record<string, 'info' | 'success' | 'warning' | 'danger'> = {
  pending: 'info',
  read: 'success',
  passed: 'success',
  failed: 'warning'
}

onMounted(() => {
  loadMy()
  loadCourses()
})
</script>

<template>
  <div class="page">
    <PageHeader
      title="安全意识"
      subtitle="必修内容 + 已读签署 + 考核判分；发布会真的投站内消息，逾期会被催办并在明细里点名"
    >
      <template #actions>
        <el-button v-perm="'awareness:manage'" type="primary" @click="openCourseCreate">新建培训项</el-button>
      </template>
    </PageHeader>

    <el-card shadow="never" class="block">
      <div class="block-head">
        <div>
          <div class="block-title">我的必修</div>
          <div class="hint">
            {{ myPending > 0 ? `还有 ${myPending} 项没完成` : '当前没有待完成的必修项' }}
          </div>
        </div>
        <el-button :loading="myLoading" @click="loadMy">刷新</el-button>
      </div>
      <el-table v-loading="myLoading" :data="myList" border stripe size="small">
        <el-table-column prop="title" label="项目" min-width="200" />
        <el-table-column prop="summary" label="说明" min-width="220" show-overflow-tooltip />
        <el-table-column label="题目" width="80" align="center">
          <template #default="{ row }">{{ row.questionCount }}</template>
        </el-table-column>
        <el-table-column label="截止" width="170">
          <template #default="{ row }">
            <span v-if="!row.dueAt" class="hint">不限期</span>
            <span v-else :class="{ warn: row.overdue }">
              {{ row.dueAt.slice(0, 16).replace('T', ' ') }}{{ row.overdue ? '（已逾期）' : '' }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="我的状态" width="150">
          <template #default="{ row }">
            <el-tag :type="statusType[row.status] || 'info'" size="small">
              {{ statusText[row.status] || row.status }}
            </el-tag>
            <span v-if="row.attempts > 0" class="hint"> {{ row.score }} 分</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :loading="studyLoading" @click="openStudy(row)">
              {{ row.status === 'passed' ? '查看' : '去完成' }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card v-perm="'awareness:manage'" shadow="never" class="block">
      <div class="block-head">
        <div>
          <div class="block-title">培训项管理</div>
          <div class="hint">{{ adminNote }}</div>
        </div>
        <el-button :loading="adminLoading" @click="loadCourses">刷新</el-button>
      </div>
      <el-table v-loading="adminLoading" :data="courses" border stripe size="small">
        <el-table-column prop="title" label="项目" min-width="180" />
        <el-table-column prop="scopeLabel" label="必修范围" width="150" />
        <el-table-column label="题目 / 及格" width="110">
          <template #default="{ row }">{{ row.questionCount }} 题 / {{ row.passScore }} 分</template>
        </el-table-column>
        <el-table-column label="完成率" width="130">
          <template #default="{ row }">
            <span v-if="!row.published" class="hint">未发布</span>
            <span v-else>
              {{ row.doneCount }} / {{ row.targetCount }}
              <span v-if="row.overdue" class="warn">（已逾期）</span>
            </span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.published ? 'success' : 'info'" size="small">
              {{ row.published ? '已发布' : '草稿' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="330" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openQuestions(row)">题目</el-button>
            <el-button link @click="openRecords(row)">完成情况</el-button>
            <el-button v-if="!row.published" link type="success" @click="doPublish(row)">发布</el-button>
            <template v-else>
              <el-button link type="warning" @click="doRemind(row)">催办</el-button>
              <el-button link @click="doUnpublish(row)">下线</el-button>
            </template>
            <el-button link @click="openCourseEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="removeCourse(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-alert type="info" :closable="false" show-icon class="scope-note">
      <template #title>这一页不做的事</template>
      不做课件上传与在线播放（平台不做内容管理，正文就是几条要点，长内容更适合放知识库并在这里给出链接）；
      不发证书与学分；不做「未完成就禁止登录」那种强制 —— 那会把一个提醒问题变成一个可用性事故。
      逾期的效果是：被催办、在完成明细里被点名。
    </el-alert>

    <!-- 学员：读 + 答题 -->
    <el-drawer v-model="studyVisible" :title="study?.course?.title || '必修项'" size="720px">
      <template v-if="study">
        <el-alert v-if="study.course.summary" type="info" :closable="false" style="margin-bottom: 10px">
          {{ study.course.summary }}
        </el-alert>
        <pre class="content">{{ study.course.content }}</pre>

        <div class="sign-row">
          <el-button
            type="primary"
            :disabled="!!study.record.readAt"
            @click="doConfirmRead"
          >
            {{ study.record.readAt ? '已确认已读' : '我已读完，确认' }}
          </el-button>
          <span class="hint">
            确认会记下时间与来源 IP（签署记录）。{{
              study.questions.length > 0 ? `之后还要答 ${study.questions.length} 道题。` : '这项没有题目，确认即完成。'
            }}
          </span>
        </div>

        <template v-if="study.questions.length > 0">
          <el-divider content-position="left">考核（及格线 {{ study.course.passScore }} 分）</el-divider>
          <div v-for="(q, idx) in study.questions" :key="q.id" class="quiz">
            <div class="quiz-title">
              {{ Number(idx) + 1 }}. {{ q.content }}
              <el-tag v-if="q.multi" size="small" type="warning" effect="plain">多选</el-tag>
              <el-tag
                v-if="reviewOf(q.id)"
                size="small"
                :type="reviewOf(q.id).ok ? 'success' : 'danger'"
              >
                {{ reviewOf(q.id).ok ? '答对' : '答错' }}
              </el-tag>
            </div>
            <el-checkbox-group v-model="answers[String(q.id)]" class="quiz-options">
              <el-checkbox
                v-for="(opt, oi) in q.options"
                :key="oi"
                :label="Number(oi)"
                :disabled="!!quizResult"
                @change="pickOption(q, Number(oi))"
              >
                {{ opt }}
              </el-checkbox>
            </el-checkbox-group>
            <div v-if="reviewOf(q.id)?.explain" class="hint">解析：{{ reviewOf(q.id).explain }}</div>
          </div>

          <div v-if="quizResult" class="quiz-result">
            <el-alert
              :type="quizResult.status === 'passed' ? 'success' : 'warning'"
              :closable="false"
              show-icon
              :title="quizResult.detail"
            />
          </div>
          <div class="sign-row">
            <el-button type="primary" :disabled="!!quizResult" @click="doSubmitQuiz">交卷</el-button>
            <el-button v-if="quizResult && quizResult.status !== 'passed'" @click="openStudy({ id: study.course.id } as any)">
              再答一次
            </el-button>
          </div>
        </template>
      </template>
    </el-drawer>

    <!-- 管理：培训项 -->
    <el-dialog v-model="courseDialog" :title="editingCourse ? '编辑培训项' : '新建培训项'" width="680px">
      <el-form :model="courseForm" label-width="96px">
        <el-form-item label="标题" required>
          <el-input v-model="courseForm.title" placeholder="如 高危命令与生产变更须知" />
        </el-form-item>
        <el-form-item label="一句话说明">
          <el-input v-model="courseForm.summary" placeholder="会出现在站内消息与列表里" />
        </el-form-item>
        <el-form-item label="正文" required>
          <el-input v-model="courseForm.content" type="textarea" :rows="10" placeholder="几条要点即可，按原样换行展示" />
        </el-form-item>
        <el-form-item label="必修范围">
          <el-radio-group v-model="courseForm.scope">
            <el-radio label="all">全员</el-radio>
            <el-radio label="role">按角色</el-radio>
            <el-radio label="dept">按部门</el-radio>
          </el-radio-group>
          <el-input
            v-if="courseForm.scope === 'role'"
            v-model.number="courseForm.scopeRoleId"
            placeholder="角色 ID"
            style="width: 140px; margin-left: 8px"
          />
          <el-input
            v-if="courseForm.scope === 'dept'"
            v-model.number="courseForm.scopeDeptId"
            placeholder="部门 ID"
            style="width: 140px; margin-left: 8px"
          />
          <div class="hint">按部门时含所有子部门；已发布的项目不允许改范围（签署记录会对不上人）</div>
        </el-form-item>
        <el-form-item label="及格分">
          <el-input-number v-model="courseForm.passScore" :min="1" :max="100" />
          <span class="hint" style="margin-left: 8px">没有题目时这个值不参与判定</span>
        </el-form-item>
        <el-form-item label="截止时间">
          <el-input v-model="courseForm.dueAt" placeholder="2026-10-01 或 2026-10-01 18:00:00，留空表示不限期" />
          <div class="hint">只给日期时按当天 23:59:59 算</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="courseDialog = false">取消</el-button>
        <el-button type="primary" @click="submitCourse">保存</el-button>
      </template>
    </el-dialog>

    <!-- 管理：题目 -->
    <el-dialog v-model="questionDialog" :title="`题目维护 - ${questionCourse?.title || ''}`" width="760px">
      <el-table :data="questions" border stripe size="small" style="margin-bottom: 12px">
        <el-table-column prop="sort" label="序" width="60" />
        <el-table-column prop="content" label="题干" min-width="220" show-overflow-tooltip />
        <el-table-column label="选项 / 答案" min-width="220">
          <template #default="{ row }">
            <div class="hint">{{ (row.optionList || []).join(' / ') }}</div>
            <div>正确：{{ (row.answerList || []).map((i: number) => (row.optionList || [])[i]).join('、') }}</div>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">{{ row.multi ? '多选' : '单选' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button link type="primary" @click="editQuestion(row)">编辑</el-button>
            <el-button link type="danger" @click="removeQuestion(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-divider content-position="left">{{ questionForm.id ? '编辑题目' : '新增题目' }}</el-divider>
      <el-form :model="questionForm" label-width="86px">
        <el-form-item label="题干" required>
          <el-input v-model="questionForm.content" />
        </el-form-item>
        <el-form-item label="选项" required>
          <el-input v-model="questionForm.optionsText" type="textarea" :rows="4" placeholder="一行一个选项" />
        </el-form-item>
        <el-form-item label="正确答案" required>
          <el-checkbox-group v-model="questionForm.answer">
            <el-checkbox v-for="(opt, oi) in optionPreview" :key="oi" :label="oi">{{ opt }}</el-checkbox>
          </el-checkbox-group>
          <div class="hint">正确答案只存在后端，不会随题目下发给浏览器</div>
        </el-form-item>
        <el-form-item label="多选题">
          <el-switch v-model="questionForm.multi" />
        </el-form-item>
        <el-form-item label="解析">
          <el-input v-model="questionForm.explain" placeholder="交卷之后才会展示给学员" />
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="questionForm.sort" :min="0" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resetQuestionForm">清空表单</el-button>
        <el-button type="primary" @click="submitQuestion">保存题目</el-button>
        <el-button @click="questionDialog = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 管理：完成情况 -->
    <el-dialog v-model="recordDialog" :title="`完成情况 - ${recordCourse?.title || ''}`" width="720px">
      <div class="facts" style="margin-bottom: 10px">
        <div class="fact"><span class="fact__label">未开始</span>{{ recordStats.pending || 0 }}</div>
        <div class="fact"><span class="fact__label">已确认已读</span>{{ recordStats.read || 0 }}</div>
        <div class="fact"><span class="fact__label">已通过</span>{{ recordStats.passed || 0 }}</div>
        <div class="fact"><span class="fact__label">未及格</span>{{ recordStats.failed || 0 }}</div>
      </div>
      <el-table :data="records" border stripe size="small">
        <el-table-column prop="username" label="账号" min-width="120" />
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="statusType[row.status] || 'info'" size="small">
              {{ statusText[row.status] || row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="成绩" width="120">
          <template #default="{ row }">
            <span v-if="row.attempts === 0" class="hint">未答题</span>
            <span v-else>{{ row.score }} 分 / {{ row.attempts }} 次</span>
          </template>
        </el-table-column>
        <el-table-column label="确认已读" min-width="170">
          <template #default="{ row }">
            <span v-if="!row.readAt" class="hint">-</span>
            <span v-else>{{ row.readAt.slice(0, 19).replace('T', ' ') }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="clientIp" label="来源 IP" width="130" />
      </el-table>
      <template #footer>
        <el-button @click="recordDialog = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.block {
  margin-bottom: 12px;
}
.block-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 10px;
}
.block-title {
  font-weight: 600;
  font-size: 14px;
  margin-bottom: 2px;
}
.hint {
  font-size: 12px;
  color: var(--ops-text-secondary, #9ca3af);
  line-height: 1.5;
}
.warn {
  color: #e6a23c;
}
.content {
  white-space: pre-wrap;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.7;
  background: var(--ops-fill-light, #f8fafc);
  padding: 12px;
  border-radius: 6px;
  margin: 0 0 12px;
}
.sign-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin: 10px 0;
}
.quiz {
  margin-bottom: 14px;
}
.quiz-title {
  font-size: 13px;
  font-weight: 600;
  margin-bottom: 6px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.quiz-options {
  display: flex;
  flex-direction: column;
}
.quiz-result {
  margin: 10px 0;
}
.facts {
  display: flex;
  gap: 20px;
  flex-wrap: wrap;
}
.fact {
  font-size: 13px;
}
.fact__label {
  color: var(--ops-text-secondary, #9ca3af);
  margin-right: 6px;
}
.scope-note {
  margin-top: 12px;
}
</style>
