package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 安全意识（培训与考核）。
//
// 这个模块最容易做成「纯表单」：录一篇文章、点个发布，然后什么都不会发生。
// 所以这里只做三件能被查证的事：
//
//  1. **谁读了** —— 确认已读是一条签署记录（人、时间、来源 IP），不是一个前端勾选框
//  2. **谁答对了** —— 题目的正确答案带 json:"-"，绝不出接口，判分只在后端做；
//     否则「考核」就是把答案先发到浏览器再问一遍
//  3. **谁到期还没做** —— 发布即向范围内的人投站内消息；逾期由定时任务再提醒一次，
//     并在完成率里点名。没有这一条，前两条也只是摆设
//
// 刻意不做：课件上传与在线播放（平台不做内容管理，正文就是几条要点）、
// 证书与学分、抄送上级审批。这些要么属于知识库，要么属于 HR 系统。
const awarenessMaxQuestions = 20

// ---------- 目标人群 ----------

// awarenessTargets 解析一个培训项的必修人群。
//
// 只算启用中的账号：停用的人不该出现在「未完成」名单里，否则完成率永远到不了 100%。
func (h *Handler) awarenessTargets(course model.AwarenessCourse) ([]model.User, error) {
	q := h.DB.Model(&model.User{}).Where("users.status = ?", 1)
	switch course.Scope {
	case "role":
		q = q.Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("user_roles.role_id = ?", course.ScopeRoleID)
	case "dept":
		ids := h.deptWithChildren(course.ScopeDeptID)
		q = q.Where("users.dept_id IN ?", ids)
	}
	var users []model.User
	if err := q.Distinct().Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// deptWithChildren 部门及其所有子部门的 id。
// 按部门必修时如果只匹配本部门，「研发中心」下面的组一个都不会被覆盖，那不是人的预期。
func (h *Handler) deptWithChildren(root uint) []uint {
	ids := []uint{root}
	frontier := []uint{root}
	for len(frontier) > 0 {
		var children []uint
		if err := h.DB.Model(&model.Department{}).
			Where("parent_id IN ?", frontier).Pluck("id", &children).Error; err != nil {
			break
		}
		if len(children) == 0 {
			break
		}
		ids = append(ids, children...)
		frontier = children
	}
	return ids
}

// ---------- 管理端：培训项 ----------

type courseReq struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Content     string `json:"content"`
	Scope       string `json:"scope"`
	ScopeRoleID uint   `json:"scopeRoleId"`
	ScopeDeptID uint   `json:"scopeDeptId"`
	PassScore   int    `json:"passScore"`
	DueAt       string `json:"dueAt"`
}

func (r *courseReq) normalize() (*time.Time, error) {
	r.Title = strings.TrimSpace(r.Title)
	r.Summary = strings.TrimSpace(r.Summary)
	if r.Title == "" {
		return nil, fmt.Errorf("标题不能为空")
	}
	if strings.TrimSpace(r.Content) == "" {
		return nil, fmt.Errorf("正文不能为空 —— 没有内容的必修项没有意义")
	}
	switch r.Scope {
	case "all":
	case "role":
		if r.ScopeRoleID == 0 {
			return nil, fmt.Errorf("按角色必修时要选一个角色")
		}
	case "dept":
		if r.ScopeDeptID == 0 {
			return nil, fmt.Errorf("按部门必修时要选一个部门")
		}
	case "":
		r.Scope = "all"
	default:
		return nil, fmt.Errorf("必修范围只能是 all / role / dept")
	}
	if r.PassScore <= 0 {
		r.PassScore = 80
	}
	if r.PassScore > 100 {
		return nil, fmt.Errorf("及格分不能大于 100")
	}
	if strings.TrimSpace(r.DueAt) == "" {
		return nil, nil
	}
	due, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(r.DueAt), time.Local)
	if err != nil {
		due, err = time.ParseInLocation("2006-01-02", strings.TrimSpace(r.DueAt), time.Local)
		if err != nil {
			return nil, fmt.Errorf("截止时间格式应为 2006-01-02 或 2006-01-02 15:04:05")
		}
		// 只给日期时补到当天末尾，否则「截止到今天」等于今天零点就过期了
		due = due.Add(24*time.Hour - time.Second)
	}
	return &due, nil
}

// courseView 管理端列表用：课程 + 题目数 + 实时完成情况
type courseView struct {
	model.AwarenessCourse
	QuestionCount int64  `json:"questionCount"`
	DoneCount     int64  `json:"doneCount"`
	TargetCount   int64  `json:"targetCount"`
	Overdue       bool   `json:"overdue"`
	ScopeLabel    string `json:"scopeLabel"`
}

func (h *Handler) courseScopeLabel(course model.AwarenessCourse) string {
	switch course.Scope {
	case "role":
		var role model.Role
		if err := h.DB.First(&role, course.ScopeRoleID).Error; err == nil {
			return "角色：" + role.Name
		}
		return fmt.Sprintf("角色 #%d（已删除）", course.ScopeRoleID)
	case "dept":
		var dept model.Department
		if err := h.DB.First(&dept, course.ScopeDeptID).Error; err == nil {
			return "部门：" + dept.Name + "（含子部门）"
		}
		return fmt.Sprintf("部门 #%d（已删除）", course.ScopeDeptID)
	default:
		return "全员"
	}
}

func (h *Handler) toCourseView(course model.AwarenessCourse) courseView {
	view := courseView{AwarenessCourse: course, ScopeLabel: h.courseScopeLabel(course)}
	h.DB.Model(&model.AwarenessQuestion{}).Where("course_id = ?", course.ID).Count(&view.QuestionCount)
	h.DB.Model(&model.AwarenessRecord{}).
		Where("course_id = ? AND status IN ?", course.ID, []string{"read", "passed"}).
		Count(&view.DoneCount)
	h.DB.Model(&model.AwarenessRecord{}).Where("course_id = ?", course.ID).Count(&view.TargetCount)
	view.Overdue = course.Published && course.DueAt != nil && course.DueAt.Before(time.Now())
	return view
}

func (h *Handler) ListAwarenessCourses(c *gin.Context) {
	var list []model.AwarenessCourse
	if err := h.DB.Order("id desc").Find(&list).Error; err != nil {
		response.Error(c, "查询培训项失败")
		return
	}
	views := make([]courseView, 0, len(list))
	for _, item := range list {
		views = append(views, h.toCourseView(item))
	}
	response.OK(c, gin.H{
		"list": views,
		"note": "发布即向范围内的人投站内消息；有题目时必须答到及格分才算完成，" +
			"正确答案不会下发到浏览器。逾期未完成的人会被再提醒一次并在明细里点名。",
	})
}

func (h *Handler) CreateAwarenessCourse(c *gin.Context) {
	var req courseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	due, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	course := model.AwarenessCourse{
		Title: req.Title, Summary: req.Summary, Content: req.Content,
		Scope: req.Scope, ScopeRoleID: req.ScopeRoleID, ScopeDeptID: req.ScopeDeptID,
		PassScore: req.PassScore, DueAt: due,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if err := h.DB.Create(&course).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, h.toCourseView(course))
}

func (h *Handler) UpdateAwarenessCourse(c *gin.Context) {
	var course model.AwarenessCourse
	if err := h.DB.First(&course, idParam(c)).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	var req courseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	due, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// 已发布的项目不许改必修范围：范围一变，已有的签署记录就对不上人了
	if course.Published && (req.Scope != course.Scope ||
		req.ScopeRoleID != course.ScopeRoleID || req.ScopeDeptID != course.ScopeDeptID) {
		response.BadRequest(c, "已发布的培训项不能改必修范围（已有签署记录会对不上人）；请先下线")
		return
	}
	updates := map[string]any{
		"title": req.Title, "summary": req.Summary, "content": req.Content,
		"scope": req.Scope, "scope_role_id": req.ScopeRoleID, "scope_dept_id": req.ScopeDeptID,
		"pass_score": req.PassScore, "due_at": due,
	}
	if err := h.DB.Model(&course).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&course, course.ID)
	response.OK(c, h.toCourseView(course))
}

// DeleteAwarenessCourse 删除培训项，连带题目与记录。
// 已发布过的不允许删：签署记录是「谁确认过什么」的凭据，删掉就没法回答了。
func (h *Handler) DeleteAwarenessCourse(c *gin.Context) {
	var course model.AwarenessCourse
	if err := h.DB.First(&course, idParam(c)).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	var done int64
	h.DB.Model(&model.AwarenessRecord{}).
		Where("course_id = ? AND status IN ?", course.ID, []string{"read", "passed", "failed"}).
		Count(&done)
	if done > 0 {
		response.BadRequest(c, fmt.Sprintf(
			"已经有 %d 个人在这个项目上留下了记录，不允许删除（签署与成绩是凭据）；可以先下线", done))
		return
	}
	if err := h.DB.Where("course_id = ?", course.ID).Delete(&model.AwarenessQuestion{}).Error; err != nil {
		response.Error(c, "清理题目失败")
		return
	}
	h.DB.Where("course_id = ?", course.ID).Delete(&model.AwarenessRecord{})
	if err := h.DB.Delete(&model.AwarenessCourse{}, course.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"id": course.ID})
}

// PublishAwarenessCourse 发布：建全员待办记录 + 投站内消息。
//
// 「发布」必须真的产生动作，否则和存草稿没区别。
func (h *Handler) PublishAwarenessCourse(c *gin.Context) {
	var course model.AwarenessCourse
	if err := h.DB.First(&course, idParam(c)).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	targets, err := h.awarenessTargets(course)
	if err != nil {
		response.Error(c, "解析必修范围失败")
		return
	}
	if len(targets) == 0 {
		response.BadRequest(c, "这个范围里没有启用中的账号，发布出去没人会收到")
		return
	}

	now := time.Now()
	created := 0
	for _, user := range targets {
		var exist model.AwarenessRecord
		err := h.DB.Where("course_id = ? AND user_id = ?", course.ID, user.ID).First(&exist).Error
		if err == nil {
			continue // 重复发布不清掉已有记录
		}
		record := model.AwarenessRecord{
			CourseID: course.ID, UserID: user.ID, Username: user.Username, Status: "pending",
		}
		if err := h.DB.Create(&record).Error; err != nil {
			log.Printf("[awareness] 建待办记录失败(user=%d): %v", user.ID, err)
			continue
		}
		created++
	}

	ids := make([]uint, 0, len(targets))
	for _, u := range targets {
		ids = append(ids, u.ID)
	}
	due := "不限期"
	if course.DueAt != nil {
		due = course.DueAt.Format("2006-01-02 15:04")
	}
	sent := h.fanoutMessages(ids, model.Message{
		Type:    "announcement",
		Title:   "[安全意识] " + course.Title,
		Content: fmt.Sprintf("%s（截止 %s）。到「安全合规 → 安全意识」完成。", orUnknown(course.Summary), due),
		Level:   "info", RefID: course.ID,
	})

	if err := h.DB.Model(&course).Updates(map[string]any{
		"published": true, "published_at": &now,
		"publisher": middleware.CurrentUser(c).Username, "target_count": len(targets),
	}).Error; err != nil {
		response.Error(c, "更新发布状态失败")
		return
	}
	h.DB.First(&course, course.ID)
	response.OK(c, gin.H{
		"course": h.toCourseView(course),
		"detail": fmt.Sprintf("必修人数 %d（新建待办 %d），已投递站内消息 %d 条", len(targets), created, sent),
	})
}

// UnpublishAwarenessCourse 下线：停止出现在必修列表里，已有记录一律保留
func (h *Handler) UnpublishAwarenessCourse(c *gin.Context) {
	var course model.AwarenessCourse
	if err := h.DB.First(&course, idParam(c)).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	if err := h.DB.Model(&course).Update("published", false).Error; err != nil {
		response.Error(c, "下线失败")
		return
	}
	response.OK(c, gin.H{"id": course.ID,
		"detail": "已下线。已完成与已答题的记录都保留 —— 那是凭据，不该随开关消失"})
}

// ---------- 管理端：题目 ----------

type questionReq struct {
	Content string   `json:"content"`
	Options []string `json:"options"`
	Answer  []int    `json:"answer"`
	Multi   bool     `json:"multi"`
	Explain string   `json:"explain"`
	Sort    int      `json:"sort"`
}

func (r *questionReq) normalize() error {
	r.Content = strings.TrimSpace(r.Content)
	if r.Content == "" {
		return fmt.Errorf("题干不能为空")
	}
	cleaned := make([]string, 0, len(r.Options))
	for _, o := range r.Options {
		if strings.TrimSpace(o) != "" {
			cleaned = append(cleaned, strings.TrimSpace(o))
		}
	}
	r.Options = cleaned
	if len(r.Options) < 2 {
		return fmt.Errorf("至少要两个选项")
	}
	if len(r.Answer) == 0 {
		return fmt.Errorf("要指定正确答案")
	}
	if !r.Multi && len(r.Answer) > 1 {
		return fmt.Errorf("单选题只能有一个正确答案")
	}
	seen := map[int]bool{}
	for _, idx := range r.Answer {
		if idx < 0 || idx >= len(r.Options) {
			return fmt.Errorf("正确答案下标 %d 超出选项范围", idx)
		}
		if seen[idx] {
			return fmt.Errorf("正确答案有重复")
		}
		seen[idx] = true
	}
	sort.Ints(r.Answer)
	return nil
}

// questionView 管理端能看到答案（他们要维护题目），学员端走另一个结构
type questionView struct {
	model.AwarenessQuestion
	OptionList []string `json:"optionList"`
	AnswerList []int    `json:"answerList"`
}

func toQuestionView(q model.AwarenessQuestion) questionView {
	view := questionView{AwarenessQuestion: q, OptionList: []string{}, AnswerList: []int{}}
	_ = json.Unmarshal([]byte(q.Options), &view.OptionList)
	_ = json.Unmarshal([]byte(q.Answer), &view.AnswerList)
	return view
}

func (h *Handler) ListAwarenessQuestions(c *gin.Context) {
	var list []model.AwarenessQuestion
	if err := h.DB.Where("course_id = ?", idParam(c)).
		Order("sort asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询题目失败")
		return
	}
	views := make([]questionView, 0, len(list))
	for _, q := range list {
		views = append(views, toQuestionView(q))
	}
	response.OK(c, views)
}

func (h *Handler) CreateAwarenessQuestion(c *gin.Context) {
	courseID := idParam(c)
	var course model.AwarenessCourse
	if err := h.DB.First(&course, courseID).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	var count int64
	h.DB.Model(&model.AwarenessQuestion{}).Where("course_id = ?", courseID).Count(&count)
	if count >= awarenessMaxQuestions {
		response.BadRequest(c, fmt.Sprintf("一个培训项最多 %d 道题", awarenessMaxQuestions))
		return
	}

	var req questionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	options, _ := json.Marshal(req.Options)
	answer, _ := json.Marshal(req.Answer)
	q := model.AwarenessQuestion{
		CourseID: courseID, Content: req.Content, Options: string(options),
		Answer: string(answer), Multi: req.Multi, Explain: req.Explain, Sort: req.Sort,
	}
	if err := h.DB.Create(&q).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, toQuestionView(q))
}

func (h *Handler) UpdateAwarenessQuestion(c *gin.Context) {
	var q model.AwarenessQuestion
	if err := h.DB.First(&q, idParam(c)).Error; err != nil {
		response.NotFound(c, "题目不存在")
		return
	}
	var req questionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	options, _ := json.Marshal(req.Options)
	answer, _ := json.Marshal(req.Answer)
	if err := h.DB.Model(&q).Updates(map[string]any{
		"content": req.Content, "options": string(options), "answer": string(answer),
		"multi": req.Multi, "explain": req.Explain, "sort": req.Sort,
	}).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&q, q.ID)
	response.OK(c, toQuestionView(q))
}

func (h *Handler) DeleteAwarenessQuestion(c *gin.Context) {
	if err := h.DB.Delete(&model.AwarenessQuestion{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 管理端：完成情况 ----------

// ListAwarenessRecords 某个培训项的完成明细：谁做了、谁没做、谁没及格
func (h *Handler) ListAwarenessRecords(c *gin.Context) {
	courseID := idParam(c)
	var course model.AwarenessCourse
	if err := h.DB.First(&course, courseID).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	var records []model.AwarenessRecord
	q := h.DB.Where("course_id = ?", courseID)
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Order("status asc, username asc").Find(&records).Error; err != nil {
		response.Error(c, "查询完成情况失败")
		return
	}
	stats := map[string]int64{}
	for _, s := range []string{"pending", "read", "passed", "failed"} {
		var n int64
		h.DB.Model(&model.AwarenessRecord{}).
			Where("course_id = ? AND status = ?", courseID, s).Count(&n)
		stats[s] = n
	}
	response.OK(c, gin.H{
		"course": h.toCourseView(course), "list": records, "stats": stats,
	})
}

// RemindAwarenessCourse 催办：给未完成的人再投一条站内消息
func (h *Handler) RemindAwarenessCourse(c *gin.Context) {
	var course model.AwarenessCourse
	if err := h.DB.First(&course, idParam(c)).Error; err != nil {
		response.NotFound(c, "培训项不存在")
		return
	}
	if !course.Published {
		response.BadRequest(c, "还没发布，没有人需要被催")
		return
	}
	sent, names := h.remindCourse(course, "催办")
	if sent == 0 {
		response.OK(c, gin.H{"sent": 0, "detail": "所有人都已完成，没有要催的人"})
		return
	}
	response.OK(c, gin.H{"sent": sent,
		"detail": fmt.Sprintf("已提醒 %d 人：%s", sent, truncate(strings.Join(names, "、"), 200))})
}

// remindCourse 给未完成的人发提醒，返回条数与名单
func (h *Handler) remindCourse(course model.AwarenessCourse, reason string) (int, []string) {
	var records []model.AwarenessRecord
	if err := h.DB.Where("course_id = ? AND status IN ?", course.ID,
		[]string{"pending", "failed"}).Find(&records).Error; err != nil {
		return 0, nil
	}
	if len(records) == 0 {
		return 0, nil
	}
	ids := make([]uint, 0, len(records))
	names := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.UserID)
		names = append(names, r.Username)
	}
	due := "不限期"
	level := "info"
	if course.DueAt != nil {
		due = course.DueAt.Format("2006-01-02 15:04")
		if course.DueAt.Before(time.Now()) {
			due += "（已逾期）"
			level = "warning"
		}
	}
	sent := h.fanoutMessages(ids, model.Message{
		Type:    "announcement",
		Title:   fmt.Sprintf("[安全意识·%s] %s", reason, course.Title),
		Content: fmt.Sprintf("这项必修还没完成，截止 %s。到「安全合规 → 安全意识」完成。", due),
		Level:   level, RefID: course.ID,
	})
	return sent, names
}

// RemindOverdueAwareness 定时任务：把逾期未完成的项目再提醒一遍。
//
// 同一个项目一天只提醒一次（LastRemindAt），否则定时任务每跑一次就轰炸一遍。
func (h *Handler) RemindOverdueAwareness() {
	now := time.Now()
	var courses []model.AwarenessCourse
	if err := h.DB.Where("published = ? AND due_at IS NOT NULL AND due_at < ?", true, now).
		Find(&courses).Error; err != nil {
		log.Printf("[awareness] 逾期检查失败: %v", err)
		return
	}
	reminded, total := 0, 0
	for _, course := range courses {
		if course.LastRemindAt != nil && now.Sub(*course.LastRemindAt) < 24*time.Hour {
			continue
		}
		sent, _ := h.remindCourse(course, "逾期")
		h.DB.Model(&model.AwarenessCourse{}).Where("id = ?", course.ID).
			Update("last_remind_at", &now)
		if sent > 0 {
			reminded++
			total += sent
		}
	}
	h.markFixedRun("awareness", fmt.Sprintf("逾期项目 %d 个，提醒 %d 人", reminded, total))
	if reminded > 0 {
		log.Printf("[awareness] 逾期提醒完成: %d 个项目、%d 人", reminded, total)
	}
}

// ---------- 学员端 ----------

// myCourseView 学员看到的列表项：不含正确答案
type myCourseView struct {
	ID            uint       `json:"id"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	DueAt         *time.Time `json:"dueAt"`
	PassScore     int        `json:"passScore"`
	QuestionCount int64      `json:"questionCount"`
	Status        string     `json:"status"`
	Score         int        `json:"score"`
	Attempts      int        `json:"attempts"`
	Overdue       bool       `json:"overdue"`
}

// ListMyAwareness 我的必修项。只列已发布、且我在必修范围内（有记录）的项目。
func (h *Handler) ListMyAwareness(c *gin.Context) {
	user := middleware.CurrentUser(c)
	var records []model.AwarenessRecord
	if err := h.DB.Where("user_id = ?", user.ID).Find(&records).Error; err != nil {
		response.Error(c, "查询必修项失败")
		return
	}
	byCourse := map[uint]model.AwarenessRecord{}
	ids := make([]uint, 0, len(records))
	for _, r := range records {
		byCourse[r.CourseID] = r
		ids = append(ids, r.CourseID)
	}
	views := make([]myCourseView, 0)
	pending := 0
	if len(ids) > 0 {
		var courses []model.AwarenessCourse
		if err := h.DB.Where("id IN ? AND published = ?", ids, true).
			Order("due_at asc, id desc").Find(&courses).Error; err != nil {
			response.Error(c, "查询必修项失败")
			return
		}
		now := time.Now()
		for _, course := range courses {
			rec := byCourse[course.ID]
			var qCount int64
			h.DB.Model(&model.AwarenessQuestion{}).Where("course_id = ?", course.ID).Count(&qCount)
			done := rec.Status == "read" || rec.Status == "passed"
			if !done {
				pending++
			}
			views = append(views, myCourseView{
				ID: course.ID, Title: course.Title, Summary: course.Summary,
				DueAt: course.DueAt, PassScore: course.PassScore, QuestionCount: qCount,
				Status: rec.Status, Score: rec.Score, Attempts: rec.Attempts,
				Overdue: !done && course.DueAt != nil && course.DueAt.Before(now),
			})
		}
	}
	response.OK(c, gin.H{"list": views, "pending": pending})
}

// GetMyAwarenessCourse 学员视角的详情：正文 + 题目（**不含正确答案**）
func (h *Handler) GetMyAwarenessCourse(c *gin.Context) {
	user := middleware.CurrentUser(c)
	courseID := idParam(c)

	var record model.AwarenessRecord
	if err := h.DB.Where("course_id = ? AND user_id = ?", courseID, user.ID).
		First(&record).Error; err != nil {
		response.NotFound(c, "这项不在你的必修范围里")
		return
	}
	var course model.AwarenessCourse
	if err := h.DB.First(&course, courseID).Error; err != nil || !course.Published {
		response.NotFound(c, "培训项不存在或已下线")
		return
	}

	var questions []model.AwarenessQuestion
	h.DB.Where("course_id = ?", courseID).Order("sort asc, id asc").Find(&questions)
	type quizItem struct {
		ID      uint     `json:"id"`
		Content string   `json:"content"`
		Options []string `json:"options"`
		Multi   bool     `json:"multi"`
	}
	items := make([]quizItem, 0, len(questions))
	for _, q := range questions {
		item := quizItem{ID: q.ID, Content: q.Content, Multi: q.Multi, Options: []string{}}
		_ = json.Unmarshal([]byte(q.Options), &item.Options)
		items = append(items, item)
	}

	response.OK(c, gin.H{
		"course": gin.H{
			"id": course.ID, "title": course.Title, "summary": course.Summary,
			"content": course.Content, "dueAt": course.DueAt, "passScore": course.PassScore,
		},
		"questions": items,
		"record":    record,
	})
}

// ConfirmAwarenessRead 确认已读。这是一条签署记录：人、时间、来源 IP。
//
// 没有题目的项目到这一步就算完成；有题目的仍要答到及格分。
func (h *Handler) ConfirmAwarenessRead(c *gin.Context) {
	user := middleware.CurrentUser(c)
	courseID := idParam(c)
	var record model.AwarenessRecord
	if err := h.DB.Where("course_id = ? AND user_id = ?", courseID, user.ID).
		First(&record).Error; err != nil {
		response.NotFound(c, "这项不在你的必修范围里")
		return
	}
	var qCount int64
	h.DB.Model(&model.AwarenessQuestion{}).Where("course_id = ?", courseID).Count(&qCount)

	now := time.Now()
	updates := map[string]any{"read_at": &now, "client_ip": c.ClientIP()}
	if record.Status == "pending" {
		updates["status"] = "read"
	}
	if err := h.DB.Model(&record).Updates(updates).Error; err != nil {
		response.Error(c, "记录失败")
		return
	}
	h.DB.First(&record, record.ID)
	detail := "已记下你的确认"
	if qCount > 0 && record.Status != "passed" {
		detail = fmt.Sprintf("已记下你的确认，还有 %d 道题要答到及格分才算完成", qCount)
	}
	response.OK(c, gin.H{"record": record, "detail": detail})
}

type quizSubmitReq struct {
	// Answers 题目 ID -> 选中的选项下标
	Answers map[string][]int `json:"answers"`
}

// SubmitAwarenessQuiz 交卷。判分全在后端：正确答案从来没下发过。
func (h *Handler) SubmitAwarenessQuiz(c *gin.Context) {
	user := middleware.CurrentUser(c)
	courseID := idParam(c)

	var record model.AwarenessRecord
	if err := h.DB.Where("course_id = ? AND user_id = ?", courseID, user.ID).
		First(&record).Error; err != nil {
		response.NotFound(c, "这项不在你的必修范围里")
		return
	}
	var course model.AwarenessCourse
	if err := h.DB.First(&course, courseID).Error; err != nil || !course.Published {
		response.NotFound(c, "培训项不存在或已下线")
		return
	}
	var questions []model.AwarenessQuestion
	h.DB.Where("course_id = ?", courseID).Order("sort asc, id asc").Find(&questions)
	if len(questions) == 0 {
		response.BadRequest(c, "这个项目没有题目，确认已读即可")
		return
	}

	var req quizSubmitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	correct := 0
	wrong := make([]uint, 0)
	type reviewItem struct {
		ID      uint   `json:"id"`
		OK      bool   `json:"ok"`
		Explain string `json:"explain"`
	}
	review := make([]reviewItem, 0, len(questions))
	for _, q := range questions {
		var want []int
		_ = json.Unmarshal([]byte(q.Answer), &want)
		got := req.Answers[fmt.Sprint(q.ID)]
		ok := sameIntSet(want, got)
		if ok {
			correct++
		} else {
			wrong = append(wrong, q.ID)
		}
		review = append(review, reviewItem{ID: q.ID, OK: ok, Explain: q.Explain})
	}

	score := correct * 100 / len(questions)
	status := "failed"
	now := time.Now()
	updates := map[string]any{
		"attempts": record.Attempts + 1, "score": score,
	}
	if wrongJSON, err := json.Marshal(wrong); err == nil {
		updates["wrong"] = string(wrongJSON)
	}
	if score >= course.PassScore {
		status = "passed"
		updates["passed_at"] = &now
	}
	updates["status"] = status
	if record.ReadAt == nil {
		updates["read_at"] = &now
	}
	if err := h.DB.Model(&record).Updates(updates).Error; err != nil {
		response.Error(c, "记录成绩失败")
		return
	}
	h.DB.First(&record, record.ID)

	detail := fmt.Sprintf("得分 %d，及格线 %d —— 没通过，可以再答一次", score, course.PassScore)
	if status == "passed" {
		detail = fmt.Sprintf("得分 %d，已通过（及格线 %d）", score, course.PassScore)
	}
	response.OK(c, gin.H{
		"score": score, "passScore": course.PassScore, "status": status,
		"correct": correct, "total": len(questions),
		"review": review, "record": record, "detail": detail,
	})
}

// sameIntSet 两个下标集合是否一致（忽略顺序与重复）
func sameIntSet(want, got []int) bool {
	set := map[int]bool{}
	for _, v := range want {
		set[v] = true
	}
	seen := map[int]bool{}
	for _, v := range got {
		if !set[v] {
			return false
		}
		seen[v] = true
	}
	return len(seen) == len(set)
}
