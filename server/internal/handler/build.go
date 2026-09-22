package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// jenkinsTimeout 单次调用 Jenkins 的超时
const jenkinsTimeout = 15 * time.Second

// ---------- Jenkins 服务器 ----------

type buildServerReq struct {
	Name     string `json:"name" binding:"required"`
	URL      string `json:"url" binding:"required"`
	Username string `json:"username"`
	Token    string `json:"token"` // 留空表示不修改
	DeptID   uint   `json:"deptId"`
	Enabled  *bool  `json:"enabled"`
	Remark   string `json:"remark"`
}

func (h *Handler) ListBuildServers(c *gin.Context) {
	var list []model.BuildServer
	q := h.applyScope(h.DB.Model(&model.BuildServer{}), middleware.CurrentUser(c))
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询构建服务器失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateBuildServer(c *gin.Context) {
	var req buildServerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与地址为必填项")
		return
	}
	if err := validateLinkURL(req.URL); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.BuildServer{
		Name: req.Name, URL: strings.TrimRight(req.URL, "/"), Username: req.Username,
		Token: h.sealSecret(req.Token), DeptID: req.DeptID, Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadBuildServerScoped(c *gin.Context) (*model.BuildServer, bool) {
	var item model.BuildServer
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "构建服务器不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "构建服务器不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateBuildServer(c *gin.Context) {
	itemPtr, ok := h.loadBuildServerScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req buildServerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := validateLinkURL(req.URL); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.URL = req.Name, strings.TrimRight(req.URL, "/")
	item.Username, item.DeptID, item.Remark = req.Username, req.DeptID, req.Remark
	if req.Token != "" {
		item.Token = h.sealSecret(req.Token)
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}

	// 名称变了要同步到任务上，否则列表展示会对不上
	h.DB.Model(&model.BuildJob{}).Where("server_id = ?", item.ID).
		Update("server_name", item.Name)
	response.OK(c, item)
}

func (h *Handler) DeleteBuildServer(c *gin.Context) {
	item, ok := h.loadBuildServerScoped(c)
	if !ok {
		return
	}
	var jobCount int64
	h.DB.Model(&model.BuildJob{}).Where("server_id = ?", item.ID).Count(&jobCount)
	if jobCount > 0 {
		response.BadRequest(c, fmt.Sprintf("该服务器下还有 %d 个构建任务，请先处理", jobCount))
		return
	}
	if err := h.DB.Delete(&model.BuildServer{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// TestBuildServer 调 /api/json 验证地址与凭据，并回读 Jenkins 版本
func (h *Handler) TestBuildServer(c *gin.Context) {
	server, ok := h.loadBuildServerScoped(c)
	if !ok {
		return
	}

	start := time.Now()
	resp, body, err := h.jenkinsRequest(server, http.MethodGet, "/api/json", nil)
	cost := time.Since(start).Milliseconds()
	if err != nil {
		response.OK(c, gin.H{"ok": false, "detail": err.Error(), "costMs": cost})
		return
	}

	version := resp.Header.Get("X-Jenkins")
	if version == "" {
		version = "未返回 X-Jenkins 头，可能不是 Jenkins 或被网关改写"
	}
	var info struct {
		NodeName string `json:"nodeName"`
		Mode     string `json:"mode"`
	}
	_ = json.Unmarshal(body, &info)

	response.OK(c, gin.H{
		"ok": true, "costMs": cost, "version": version,
		"nodeName": info.NodeName, "mode": info.Mode,
	})
}

// jenkinsRequest 统一封装带 Basic Auth 的调用。
//
// 注意：URL 由管理员填写，服务端会主动请求 —— 与通知渠道同源的 SSRF 面，见 docs/SECURITY.md。
func (h *Handler) jenkinsRequest(server *model.BuildServer, method, path string, form url.Values) (*http.Response, []byte, error) {
	target := server.URL + path

	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequest(method, target, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequest(method, target, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if server.Username != "" {
		// Token 在库里是密文，这里解开再放进 Basic Auth。
		// 解不开就别发请求：Jenkins 会回 401，读起来像是「Token 被吊销了」，方向就错了。
		token, err := h.openSecret("Jenkins Token", server.Token)
		if err != nil {
			return nil, nil, err
		}
		req.SetBasicAuth(server.Username, token)
	}

	// 走统一出口：Jenkins 多数装在内网，而内网网段默认就在 bypass 清单里
	client := h.egressClient(jenkinsTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp, body, fmt.Errorf("认证失败（HTTP %d），请检查账号与 API Token", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return resp, body, fmt.Errorf("Jenkins 返回 HTTP %d", resp.StatusCode)
	}
	return resp, body, nil
}

// jobAPIPath 把 folder/job 形式的路径转成 Jenkins 的 /job/x/job/y 形式
func jobAPIPath(jobPath string) string {
	parts := strings.Split(strings.Trim(jobPath, "/"), "/")
	segments := make([]string, 0, len(parts)*2)
	for _, part := range parts {
		if part == "" {
			continue
		}
		segments = append(segments, "job", url.PathEscape(part))
	}
	return "/" + strings.Join(segments, "/")
}

// ---------- 构建任务 ----------

type buildJobReq struct {
	Name     string `json:"name" binding:"required"`
	ServerID uint   `json:"serverId" binding:"required"`
	JobPath  string `json:"jobPath" binding:"required"`
	Params   string `json:"params"`
	DeptID   uint   `json:"deptId"`
	Enabled  *bool  `json:"enabled"`
	Remark   string `json:"remark"`
}

func (h *Handler) ListBuildJobs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.BuildJob{}), middleware.CurrentUser(c))
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR job_path LIKE ?", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询构建任务失败")
		return
	}
	var list []model.BuildJob
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询构建任务失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) CreateBuildJob(c *gin.Context) {
	var req buildJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称、服务器与 job 路径为必填项")
		return
	}
	if err := validateJSONObject(req.Params); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var server model.BuildServer
	if err := h.DB.First(&server, req.ServerID).Error; err != nil {
		response.BadRequest(c, "构建服务器不存在")
		return
	}

	item := model.BuildJob{
		Name: req.Name, ServerID: server.ID, ServerName: server.Name,
		JobPath: strings.Trim(req.JobPath, "/"), Params: req.Params,
		DeptID: req.DeptID, Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadBuildJobScoped(c *gin.Context) (*model.BuildJob, bool) {
	var item model.BuildJob
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "构建任务不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "构建任务不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateBuildJob(c *gin.Context) {
	itemPtr, ok := h.loadBuildJobScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req buildJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := validateJSONObject(req.Params); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var server model.BuildServer
	if err := h.DB.First(&server, req.ServerID).Error; err != nil {
		response.BadRequest(c, "构建服务器不存在")
		return
	}

	item.Name, item.ServerID, item.ServerName = req.Name, server.ID, server.Name
	item.JobPath, item.Params = strings.Trim(req.JobPath, "/"), req.Params
	item.DeptID, item.Remark = req.DeptID, req.Remark
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteBuildJob(c *gin.Context) {
	item, ok := h.loadBuildJobScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.BuildJob{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 构建记录保留，作为历史留痕
	response.OK(c, nil)
}

type triggerReq struct {
	Params map[string]string `json:"params"`
}

// TriggerBuildJob 触发构建。
//
// Jenkins 触发是异步的：返回 201 + Location 指向队列项，此时还没有构建号，
// 需要后续调「同步状态」才能拿到 buildNo 与结果。平台不做轮询。
func (h *Handler) TriggerBuildJob(c *gin.Context) {
	job, ok := h.loadBuildJobScoped(c)
	if !ok {
		return
	}
	if !job.Enabled {
		response.BadRequest(c, "该构建任务已停用")
		return
	}

	var server model.BuildServer
	if err := h.DB.First(&server, job.ServerID).Error; err != nil {
		response.BadRequest(c, "构建服务器不存在")
		return
	}
	if !server.Enabled {
		response.BadRequest(c, "构建服务器已停用")
		return
	}

	var req triggerReq
	_ = c.ShouldBindJSON(&req)

	// 默认参数打底，请求参数覆盖
	params := map[string]string{}
	if job.Params != "" {
		_ = json.Unmarshal([]byte(job.Params), &params)
	}
	for k, v := range req.Params {
		params[k] = v
	}

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	path := jobAPIPath(job.JobPath) + "/build"
	if len(form) > 0 {
		path = jobAPIPath(job.JobPath) + "/buildWithParameters"
	}

	paramJSON, _ := json.Marshal(params)
	user := middleware.CurrentUser(c)
	record := model.BuildRecord{
		JobID: job.ID, JobName: job.Name, Status: "triggered",
		Params: string(paramJSON), TriggeredBy: user.Username, StartedAt: time.Now(),
	}

	resp, _, err := h.jenkinsRequest(&server, http.MethodPost, path, form)
	if err != nil {
		record.Status = "failure"
		record.ErrorMsg = truncate(err.Error(), 240)
		h.DB.Create(&record)
		response.BadRequest(c, "触发失败: "+err.Error())
		return
	}

	record.QueueURL = resp.Header.Get("Location")
	if err := h.DB.Create(&record).Error; err != nil {
		response.Error(c, "触发成功但记录写入失败")
		return
	}

	now := time.Now()
	h.DB.Model(&model.BuildJob{}).Where("id = ?", job.ID).Updates(map[string]any{
		"last_status": "triggered", "last_run_at": &now,
	})

	response.OK(c, gin.H{
		"recordId": record.ID, "queueUrl": record.QueueURL,
		"detail": "已提交到 Jenkins 队列，构建号需同步后才能拿到",
	})
}

// ---------- 构建记录 ----------

func (h *Handler) ListBuildRecords(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.BuildRecord{})
	if jobID := c.Query("jobId"); jobID != "" {
		q = q.Where("job_id = ?", jobID)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询构建记录失败")
		return
	}
	var list []model.BuildRecord
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询构建记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// SyncBuildRecord 主动同步一条记录的状态：
// 先用队列项解析出构建号，再拉构建详情读结果与耗时。
func (h *Handler) SyncBuildRecord(c *gin.Context) {
	var record model.BuildRecord
	if err := h.DB.First(&record, idParam(c)).Error; err != nil {
		response.NotFound(c, "构建记录不存在")
		return
	}

	var job model.BuildJob
	if err := h.DB.First(&job, record.JobID).Error; err != nil {
		response.BadRequest(c, "关联的构建任务已删除，无法同步")
		return
	}
	if !h.resourceVisible(middleware.CurrentUser(c), job.DeptID, job.CreatedBy) {
		response.NotFound(c, "构建记录不存在")
		return
	}

	var server model.BuildServer
	if err := h.DB.First(&server, job.ServerID).Error; err != nil {
		response.BadRequest(c, "构建服务器不存在")
		return
	}

	buildURL := record.BuildURL
	// 还没有构建 URL，先从队列项解析
	if buildURL == "" {
		if record.QueueURL == "" {
			response.BadRequest(c, "该记录没有队列地址，可能触发时就失败了")
			return
		}
		_, body, err := h.jenkinsRequest(&server, http.MethodGet, apiPathOf(server.URL, record.QueueURL), nil)
		if err != nil {
			response.BadRequest(c, "读取队列信息失败: "+err.Error())
			return
		}

		var queue struct {
			Cancelled  bool `json:"cancelled"`
			Executable *struct {
				Number int    `json:"number"`
				URL    string `json:"url"`
			} `json:"executable"`
		}
		if err := json.Unmarshal(body, &queue); err != nil {
			response.Error(c, "队列信息解析失败")
			return
		}
		if queue.Cancelled {
			h.finishBuildRecord(&record, &job, "aborted", 0, 0, "队列项已被取消")
			response.OK(c, gin.H{"status": "aborted", "detail": "队列项已被取消"})
			return
		}
		if queue.Executable == nil {
			response.OK(c, gin.H{"status": record.Status, "detail": "仍在排队，尚未分配构建号"})
			return
		}
		record.BuildNo = queue.Executable.Number
		record.BuildURL = queue.Executable.URL
		buildURL = queue.Executable.URL
		h.DB.Model(&record).Updates(map[string]any{
			"build_no": record.BuildNo, "build_url": record.BuildURL,
		})
	}

	_, body, err := h.jenkinsRequest(&server, http.MethodGet, apiPathOf(server.URL, buildURL), nil)
	if err != nil {
		response.BadRequest(c, "读取构建详情失败: "+err.Error())
		return
	}

	var build struct {
		Building bool   `json:"building"`
		Result   string `json:"result"`
		Duration int64  `json:"duration"`
		Number   int    `json:"number"`
	}
	if err := json.Unmarshal(body, &build); err != nil {
		response.Error(c, "构建详情解析失败")
		return
	}

	if build.Building {
		response.OK(c, gin.H{"status": "running", "buildNo": build.Number, "detail": "构建进行中"})
		return
	}

	status := mapJenkinsResult(build.Result)
	h.finishBuildRecord(&record, &job, status, build.Number, build.Duration, "")
	response.OK(c, gin.H{
		"status": status, "buildNo": build.Number, "durationMs": build.Duration,
	})
}

// finishBuildRecord 回写记录与任务的最终状态
func (h *Handler) finishBuildRecord(record *model.BuildRecord, job *model.BuildJob, status string, buildNo int, duration int64, errMsg string) {
	now := time.Now()
	updates := map[string]any{"status": status, "synced_at": &now, "duration_ms": duration}
	if buildNo > 0 {
		updates["build_no"] = buildNo
	}
	if errMsg != "" {
		updates["error_msg"] = truncate(errMsg, 240)
	}
	h.DB.Model(record).Updates(updates)

	jobUpdates := map[string]any{"last_status": status}
	if buildNo > 0 {
		jobUpdates["last_build_no"] = buildNo
	}
	h.DB.Model(&model.BuildJob{}).Where("id = ?", job.ID).Updates(jobUpdates)
}

// apiPathOf 把 Jenkins 返回的绝对 URL 转成相对本服务器的 api/json 路径，
// 避免拼接出跨主机请求
func apiPathOf(serverURL, absoluteURL string) string {
	path := strings.TrimPrefix(absoluteURL, serverURL)
	if path == absoluteURL {
		// 不是同一个前缀（可能网关改写过），退化成只取 URL 的路径部分
		if parsed, err := url.Parse(absoluteURL); err == nil {
			path = parsed.Path
		}
	}
	return strings.TrimRight(path, "/") + "/api/json"
}

func mapJenkinsResult(result string) string {
	switch strings.ToUpper(result) {
	case "SUCCESS":
		return "success"
	case "FAILURE":
		return "failure"
	case "UNSTABLE":
		return "unstable"
	case "ABORTED":
		return "aborted"
	default:
		return "unknown"
	}
}

func validateJSONObject(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	target := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		return fmt.Errorf("参数必须是字符串值的 JSON 对象，例如 {\"BRANCH\":\"main\"}")
	}
	return nil
}
