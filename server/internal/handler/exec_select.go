package handler

// 批量执行的三件日常必需品：按条件选主机、只重跑失败的、停掉正在跑的。
//
// # 审计发现的三个日常缺口
//
//  1. **选主机只能勾 ID 列表，且前端硬上限 200 台**（拉 `pageSize=200` 且不带任何筛选）。
//     不能按标签/分组/环境批量选，`Host.Tags` 连下拉的 label 里都没有。
//     第 201 台主机在那个页面上根本选不到 —— 百台以上的环境直接不可用。
//  2. **没有「只重跑失败主机」**。100 台失败 12 台，只能看详情再手工勾一遍。
//  3. **不能取消正在跑的下发**。唯一的中断方式是关浏览器，而且记录照样写成 finished，
//     别人看到「有人往生产刷了个错命令」时束手无策。

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// execSelectMax 一次按条件最多选中多少台。
//
// 给上限不是为了性能，是为了**让人看清自己选了什么**：
// 条件写错时「匹配到 800 台」和「匹配到 8 台」在界面上长得一样，
// 而前者按下去就是一次事故。超过上限直接拒绝并告诉实际匹配数，让人先收紧条件。
const execSelectMax = 500

func uintsJSON(ids []uint) string {
	raw, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func parseUintsJSON(raw string) []uint {
	var ids []uint
	if raw == "" {
		return ids
	}
	_ = json.Unmarshal([]byte(raw), &ids)
	return ids
}

type execSelectReq struct {
	// Keyword 匹配名称 / 地址 / 标签
	Keyword string `json:"keyword"`
	Env     string `json:"env"`
	Status  string `json:"status"`
	DeptID  uint   `json:"deptId"`
	// Tags 标签，全部命中才算（AND）。以前标签完全没法用来选主机
	Tags []string `json:"tags"`
}

// ResolveExecHosts 按条件解析出目标主机清单。
//
// 返回的是**完整清单 + 计数**，而不是分页 —— 调用方要的是「这次要打哪些机器」，
// 分页在这里毫无意义。结果已经过数据范围与资源授权过滤，所以前端拿到的
// 就是「我确实能对它执行的机器」。
func (h *Handler) ResolveExecHosts(c *gin.Context) {
	var req execSelectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	user := middleware.CurrentUser(c)
	q := h.applyScopeWithGrants(h.DB.Model(&model.Host{}), user, "host")
	if kw := strings.TrimSpace(req.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR address LIKE ? OR tags LIKE ?", like, like, like)
	}
	if req.Env != "" {
		q = q.Where("env = ?", req.Env)
	}
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	if req.DeptID > 0 {
		q = q.Where("dept_id = ?", req.DeptID)
	}
	// 标签是 AND：选「prod + mysql」要的是同时带这两个标签的机器，
	// OR 语义会把范围悄悄放大，而放大的方向正好是危险的那一侧
	for _, tag := range req.Tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		q = q.Where("tags LIKE ?", "%"+tag+"%")
	}

	var hosts []model.Host
	if err := q.Order("env desc, name asc").Find(&hosts).Error; err != nil {
		response.Error(c, "查询失败")
		return
	}
	if len(hosts) > execSelectMax {
		response.BadRequest(c, fmt.Sprintf(
			"按这个条件匹配到 %d 台，超过单次上限 %d 台。请收紧条件 —— "+
				"「匹配到 800 台」和「匹配到 8 台」在界面上长得一样，而前者按下去就是一次事故",
			len(hosts), execSelectMax))
		return
	}

	// 逐台标出能不能执行：数据范围内可见但没授权执行动作的主机要让人看见，
	// 而不是在下发时被静默剔除（那样「我明明选了 20 台怎么只跑了 12 台」没法解释）
	ids := make([]uint, 0, len(hosts))
	list := make([]gin.H, 0, len(hosts))
	prod := 0
	for i := range hosts {
		host := hosts[i]
		canExec := h.hostActionAllowed(user, &host, model.ActionExec)
		if canExec {
			ids = append(ids, host.ID)
		}
		if host.Env == "prod" {
			prod++
		}
		list = append(list, gin.H{
			"id": host.ID, "name": host.Name, "address": host.Address,
			"env": host.Env, "status": host.Status, "tags": host.Tags,
			"canExec": canExec,
		})
	}

	response.OK(c, gin.H{
		"hostIds": ids,
		"hosts":   list,
		"matched": len(hosts),
		"usable":  len(ids),
		"prod":    prod,
		"notes": []string{
			"结果已按数据范围与资源授权过滤，canExec=false 的主机不会进入下发清单",
			"标签条件是「全部命中」而不是「任一命中」——放大范围的方向正好是危险的那一侧",
			fmt.Sprintf("单次上限 %d 台；再多请收紧条件，不要靠翻页凑", execSelectMax),
		},
	})
}

// RerunFailedExecJob 只对上一次失败/超时的主机重跑同一条命令。
//
// 不是「复制作业再跑一遍」：那会把已经成功的机器再执行一次，
// 而很多运维命令不是幂等的（追加配置、重启服务、扩容）。
func (h *Handler) RerunFailedExecJob(c *gin.Context) {
	var job model.ExecJob
	if err := h.DB.First(&job, idParam(c)).Error; err != nil {
		response.NotFound(c, "执行记录不存在")
		return
	}
	if job.Status == "running" {
		response.BadRequest(c, "这次下发还在跑，先等它结束或者取消它")
		return
	}

	var failed []model.ExecResult
	if err := h.DB.Where("job_id = ? AND status <> ?", job.ID, "success").
		Find(&failed).Error; err != nil {
		response.Error(c, "查询失败结果出错")
		return
	}
	if len(failed) == 0 {
		response.BadRequest(c, "这次下发没有失败的主机，不需要重跑")
		return
	}

	ids := make([]uint, 0, len(failed))
	for _, item := range failed {
		ids = append(ids, item.HostID)
	}

	user := middleware.CurrentUser(c)
	// 重跑是一次新的下发，权限要重新判一遍 —— 上次能跑不代表现在还能跑
	// （授权可能已经到期或被收回）
	allowed := h.filterVisibleHostIDs(user, ids)
	if len(allowed) == 0 {
		response.Forbidden(c, "这些主机现在都不在你的执行权限范围内")
		return
	}
	if !h.ensureProdPerm(c, allowed) {
		return
	}

	fresh, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name:    job.Name + "（重跑失败主机）",
		Command: job.Command, Timeout: job.Timeout, HostIDs: allowed,
		UserID: user.ID, Operator: user.Username, Source: job.Source,
		// 重跑的目标是上次失败的那批，生产确认沿用「这次调用者是否有 exec:prod」，
		// 不继承上一次作业的 ProdConfirmed —— 那是上一次的人做的决定
		ConfirmProd: true, ClientIP: c.ClientIP(),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"job":     fresh,
		"reran":   len(allowed),
		"skipped": len(ids) - len(allowed),
		"note": fmt.Sprintf("只对上次失败的 %d 台重跑；成功的机器不会被再执行一次"+
			"（很多运维命令不是幂等的）", len(allowed)),
	})
}

// CancelExecJob 停掉正在跑的下发。
//
// 取消是**进程内**的：作业的取消函数存在发起它的那个进程里。
// 多实例部署时在另一个实例上发起的作业取消不了 —— 这一点如实返回，
// 不假装成功（假装成功会让人以为命令已经停了）。
func (h *Handler) CancelExecJob(c *gin.Context) {
	var job model.ExecJob
	if err := h.DB.First(&job, idParam(c)).Error; err != nil {
		response.NotFound(c, "执行记录不存在")
		return
	}
	if job.Status != "running" {
		response.BadRequest(c, "这次下发已经结束了（"+job.Status+"）")
		return
	}

	user := middleware.CurrentUser(c)
	if !h.execCancels.cancel(job.ID) {
		response.BadRequest(c, "本进程里没有这个作业 —— 它可能刚刚结束，"+
			"也可能是另一个实例发起的（取消是进程内的）。刷新看看状态")
		return
	}
	// 记谁停的：这一行是事后复盘时的关键信息
	h.DB.Model(&model.ExecJob{ID: job.ID}).Update("canceled_by", user.Username)
	middleware.SetAuditDetail(c, fmt.Sprintf("取消下发 #%d（%s），命令：%s",
		job.ID, job.Name, truncate(job.Command, 200)))
	response.OK(c, gin.H{
		"canceled": true,
		"note": "已发出取消。已经在跑的那几台会收到 SIGKILL 尝试，" +
			"但**远端进程不保证被杀掉**（无 PTY 会话的信号转发不可靠，" +
			"nohup / & / 已 fork 的子进程一律不受影响）",
	})
}

// ensureProdPerm 目标里有生产主机时要求 exec:prod。
//
// 原来「生产确认」只是前端把后端返回的 needConfirm 直接回传的一个布尔 ——
// 直接调 API 写死 `confirmProd: true` 就绕过了。那是个**提示**，不是控制。
// 现在加一道真的门：能不能对生产下发由权限码决定，而不是由请求体里的字段决定。
func (h *Handler) ensureProdPerm(c *gin.Context, hostIDs []uint) bool {
	var prod int64
	if err := h.DB.Model(&model.Host{}).
		Where("id IN ? AND env = ?", hostIDs, "prod").Count(&prod).Error; err != nil {
		response.Error(c, "校验目标环境失败")
		return false
	}
	if prod == 0 {
		return true
	}
	if _, ok := middleware.Perms(c)["exec:prod"]; !ok {
		response.Forbidden(c, fmt.Sprintf(
			"目标里有 %d 台生产主机，需要「在生产主机上执行」权限（exec:prod）。"+
				"以前这里只靠前端的二次确认，直接调接口就能绕过", prod))
		return false
	}
	return true
}
