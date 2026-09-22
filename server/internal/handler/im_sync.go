package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 组织同步：把 IM 的部门树与成员映射到平台的公司/部门/用户。
//
// 同步的原则是「只补不毁」，因为这张表底下压着数据权限（用户的 DeptID 决定他能看哪些主机）
// 和全部历史留痕（审计日志按 username 记人）：
//
//   - 部门只新建与改名，**从不删除**：IM 侧撤掉一个部门，平台这边可能还挂着人和主机
//   - 用户只新建、绑定与更新资料，**从不删除**；IM 侧查不到的人改成停用（可关）
//   - 角色只在新建时给默认角色，**同步不碰已有用户的角色** —— 否则管理员手工调过的
//     权限会被下一次同步抹平
//   - 口令一律不动。新建用户塞一个随机口令（列不能为空），本人得走改密或后续的扫码登录
//   - 内置 admin（id=1）永不被停用
//
// 预演（dryRun）与执行走的是同一段逻辑，只是不写库：同步这种批量动作，
// 「先看清要改什么」比「改完再回滚」实在得多。

// imSyncMaxUsers 一次同步最多处理多少人，超了如实报错而不是截断了当没事
const imSyncMaxUsers = 5000

// imDeptCodePrefix 部门 Code 里放 IM 侧的部门 id，形如 wecom:12。
// 借用既有的 Department.Code 字段做映射，而不是再加一张表 —— 这样人在界面上
// 也能直接看出某个部门是从哪同步来的。
func imDeptCode(provider, imDeptID string) string {
	return provider + ":" + imDeptID
}

// imSyncChange 一条将要发生（或已发生）的改动
type imSyncChange struct {
	Kind string `json:"kind"` // dept | user
	// Action create | update | bind | disable | skip
	Action string `json:"action"`
	Name   string `json:"name"`
	ImID   string `json:"imId"`
	Detail string `json:"detail"`
}

// imSyncReport 一次同步的结果
type imSyncReport struct {
	DryRun  bool           `json:"dryRun"`
	Changes []imSyncChange `json:"changes"`

	DeptTotal    int `json:"deptTotal"`
	DeptCreated  int `json:"deptCreated"`
	DeptUpdated  int `json:"deptUpdated"`
	UserTotal    int `json:"userTotal"`
	UserCreated  int `json:"userCreated"`
	UserBound    int `json:"userBound"`
	UserUpdated  int `json:"userUpdated"`
	UserDisabled int `json:"userDisabled"`
	UserSkipped  int `json:"userSkipped"`
}

func (r *imSyncReport) add(kind, action, name, imID, detail string) {
	r.Changes = append(r.Changes, imSyncChange{
		Kind: kind, Action: action, Name: name, ImID: imID, Detail: detail,
	})
}

// randomPassword 给新导入的用户塞一个随机口令。
//
// 不是「空口令」也不是固定口令：PasswordHash 列不能为空，而固定口令等于给所有
// 导入账号开一扇同样的门。随机口令意味着这些账号只能靠管理员改密（或以后的扫码登录）进来。
func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// syncContext 一次同步用到的现场数据
type syncContext struct {
	app      *model.ImApp
	depts    []imDept
	users    []imUser
	deptName map[string]string // IM 部门 id -> 名称，用于拼部门路径
}

// deptPath 拼出 IM 侧的部门路径，便于人工核对映射
func (s *syncContext) deptPath(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if name := s.deptName[id]; name != "" {
			parts = append(parts, name)
		} else if id != "" {
			parts = append(parts, id)
		}
	}
	return strings.Join(parts, " / ")
}

// imAppSecret 解出 IM 应用密钥。
//
// 这个密钥同时用在「拉通讯录」和「扫码登录」两条链路上，所以统一从这里取 ——
// 解不开就直接报错，否则企业微信那边只会回一句 errcode=40001，看不出是平台侧的问题。
func (h *Handler) imAppSecret(app *model.ImApp) (string, error) {
	return h.openSecret("IM 应用密钥", app.AppSecret)
}

// fetchDirectory 拉通讯录。单独拆出来便于把「取数据」与「算改动」分开测。
func (h *Handler) fetchDirectory(ctx context.Context, app *model.ImApp) (*syncContext, error) {
	dir, err := imDirectoryFor(app.Provider, app.BaseURL, h.egressClient(imDirTimeout))
	if err != nil {
		return nil, err
	}
	secret, err := h.imAppSecret(app)
	if err != nil {
		return nil, err
	}
	token, err := dir.token(ctx, app.CorpID, secret)
	if err != nil {
		return nil, err
	}
	depts, err := dir.departments(ctx, token, app.RootDeptID)
	if err != nil {
		return nil, err
	}
	if len(depts) == 0 {
		return nil, fmt.Errorf("%s 没有返回任何部门（检查通讯录权限与同步起点部门）", dir.label())
	}
	if len(depts) > imDirMaxDepts {
		return nil, fmt.Errorf("部门数 %d 超过上限 %d，请把同步起点收窄到某个子部门",
			len(depts), imDirMaxDepts)
	}

	current := &syncContext{app: app, depts: depts, deptName: map[string]string{}}
	for _, dept := range depts {
		current.deptName[dept.ID] = dept.Name
	}

	// 逐个部门拉人，按 IM userid 去重（一个人可能挂在多个部门下）
	seen := map[string]int{}
	for _, dept := range depts {
		people, err := dir.users(ctx, token, dept.ID)
		if err != nil {
			return nil, fmt.Errorf("读取部门「%s」成员失败: %w", dept.Name, err)
		}
		for _, person := range people {
			if strings.TrimSpace(person.ID) == "" {
				continue
			}
			if idx, ok := seen[person.ID]; ok {
				// 合并部门列表，保留第一次出现的主部门
				current.users[idx].DeptIDs = mergeStrings(current.users[idx].DeptIDs, person.DeptIDs)
				continue
			}
			if len(current.users) >= imSyncMaxUsers {
				return nil, fmt.Errorf("成员数超过上限 %d，请把同步起点收窄", imSyncMaxUsers)
			}
			seen[person.ID] = len(current.users)
			current.users = append(current.users, person)
		}
	}
	return current, nil
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, item := range append(a, b...) {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// applySync 把通讯录落到平台。dryRun 时只算不写。
func (h *Handler) applySync(current *syncContext, dryRun bool) (*imSyncReport, error) {
	app := current.app
	report := &imSyncReport{DryRun: dryRun, Changes: make([]imSyncChange, 0, 32)}
	report.DeptTotal = len(current.depts)
	report.UserTotal = len(current.users)

	// ---------- 部门 ----------
	// 先按 parent 排序，保证父部门先落库，子部门才能挂上去
	ordered := make([]imDept, len(current.depts))
	copy(ordered, current.depts)
	sort.SliceStable(ordered, func(i, j int) bool {
		return imDeptDepth(ordered[i], current.depts) < imDeptDepth(ordered[j], current.depts)
	})

	// imDeptID -> 平台部门 ID
	deptMap := map[string]uint{}
	for _, dept := range ordered {
		code := imDeptCode(app.Provider, dept.ID)
		var existing model.Department
		err := h.DB.Where("code = ? AND company_id = ?", code, app.TargetCompanyID).
			First(&existing).Error

		parentID := uint(0)
		if mapped, ok := deptMap[dept.ParentID]; ok {
			parentID = mapped
		}

		switch {
		case err == nil:
			deptMap[dept.ID] = existing.ID
			if existing.Name != dept.Name || existing.ParentID != parentID {
				report.DeptUpdated++
				report.add("dept", "update", dept.Name, dept.ID,
					fmt.Sprintf("原名「%s」，父部门 %d -> %d", existing.Name, existing.ParentID, parentID))
				if !dryRun {
					if err := h.DB.Model(&model.Department{}).Where("id = ?", existing.ID).
						Updates(map[string]any{
							"name": dept.Name, "parent_id": parentID, "sort": dept.Order,
						}).Error; err != nil {
						return nil, fmt.Errorf("更新部门「%s」失败: %w", dept.Name, err)
					}
				}
			}
		default:
			report.DeptCreated++
			report.add("dept", "create", dept.Name, dept.ID, "新建部门")
			if dryRun {
				// 预演时给个占位 ID，后面的子部门与用户才能算出正确的归属
				deptMap[dept.ID] = 0
				continue
			}
			created := model.Department{
				CompanyID: app.TargetCompanyID, ParentID: parentID, Name: dept.Name,
				Code: code, Sort: dept.Order,
			}
			if err := h.DB.Create(&created).Error; err != nil {
				return nil, fmt.Errorf("创建部门「%s」失败: %w", dept.Name, err)
			}
			deptMap[dept.ID] = created.ID
		}
	}

	// ---------- 成员 ----------
	now := time.Now()
	activeIm := map[string]bool{}
	for _, person := range current.users {
		activeIm[person.ID] = true

		primaryDept := uint(0)
		if len(person.DeptIDs) > 0 {
			primaryDept = deptMap[person.DeptIDs[0]]
		}
		path := current.deptPath(person.DeptIDs)
		displayName := person.Name
		if displayName == "" {
			displayName = person.ID
		}

		var binding model.ImAccount
		bindErr := h.DB.Where("provider = ? AND im_user_id = ?", app.Provider, person.ID).
			First(&binding).Error

		if bindErr == nil {
			// 已绑定：只更新资料，绝不动角色与口令
			var user model.User
			if err := h.DB.First(&user, binding.UserID).Error; err != nil {
				// 绑定指向的账号已经被删了，当作没绑过
				report.UserSkipped++
				report.add("user", "skip", displayName, person.ID, "绑定的平台账号已不存在，跳过")
				continue
			}
			changes := map[string]any{}
			if person.Name != "" && user.Nickname != person.Name {
				changes["nickname"] = person.Name
			}
			if person.Email != "" && user.Email != person.Email {
				changes["email"] = person.Email
			}
			if primaryDept != 0 && user.DeptID != primaryDept {
				changes["dept_id"] = primaryDept
			}
			// IM 侧回来了就恢复启用（之前可能因离职被停用）
			if person.Active && user.Status != 1 {
				changes["status"] = 1
			}
			if len(changes) == 0 {
				report.UserSkipped++
				report.add("user", "skip", displayName, person.ID, "资料无变化")
			} else {
				report.UserUpdated++
				report.add("user", "update", user.Username, person.ID, imChangeSummary(changes))
				if !dryRun {
					if err := h.DB.Model(&model.User{}).Where("id = ?", user.ID).
						Updates(changes).Error; err != nil {
						return nil, fmt.Errorf("更新用户「%s」失败: %w", user.Username, err)
					}
				}
			}
			if !dryRun {
				h.DB.Model(&model.ImAccount{}).Where("id = ?", binding.ID).Updates(map[string]any{
					"im_name": person.Name, "im_mobile": person.Mobile, "im_email": person.Email,
					"im_dept_path": truncate(path, 240), "app_id": app.ID,
					"username": user.Username, "last_sync_at": &now,
				})
			}
			continue
		}

		// 未绑定：先看平台里有没有同名账号，有就绑上去而不是再建一个
		username := strings.TrimSpace(person.ID)
		var existing model.User
		if err := h.DB.Where("username = ?", username).First(&existing).Error; err == nil {
			report.UserBound++
			report.add("user", "bind", existing.Username, person.ID,
				fmt.Sprintf("平台已有同名账号（id=%d），建立绑定而不新建", existing.ID))
			if !dryRun {
				if err := h.DB.Create(&model.ImAccount{
					AppID: app.ID, Provider: app.Provider, ImUserID: person.ID,
					ImName: person.Name, ImMobile: person.Mobile, ImEmail: person.Email,
					ImDeptPath: truncate(path, 240), UserID: existing.ID,
					Username: existing.Username, LastSyncAt: &now,
				}).Error; err != nil {
					return nil, fmt.Errorf("绑定用户「%s」失败: %w", existing.Username, err)
				}
			}
			continue
		}

		// 真的没有：新建
		report.UserCreated++
		detail := "新建账号"
		if primaryDept != 0 || dryRun {
			detail += "，部门 " + path
		}
		report.add("user", "create", username, person.ID, detail)
		if dryRun {
			continue
		}
		password, err := randomPassword()
		if err != nil {
			return nil, fmt.Errorf("生成随机口令失败: %w", err)
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("口令加密失败: %w", err)
		}
		created := model.User{
			Username: username, PasswordHash: string(hashed), Nickname: person.Name,
			Email: person.Email, DeptID: primaryDept, Status: 1,
		}
		// 先把「想要的状态」记在外面：Status 带 gorm default 1，Create 之后
		// 结构体上的 0 会被回填成 1，再判 created.Status 就永远是 1 了
		wantActive := person.Active
		if !wantActive {
			created.Status = 0
		}
		if err := h.DB.Create(&created).Error; err != nil {
			return nil, fmt.Errorf("创建用户「%s」失败: %w", username, err)
		}
		if !wantActive {
			if err := h.DB.Model(&model.User{}).Where("id = ?", created.ID).
				Updates(map[string]any{"status": 0}).Error; err != nil {
				return nil, fmt.Errorf("写回用户「%s」的停用状态失败: %w", username, err)
			}
			created.Status = 0
		}
		// 角色只在新建时给：之后管理员怎么调，同步都不再插手
		if app.DefaultRoleID != 0 {
			var role model.Role
			if err := h.DB.First(&role, app.DefaultRoleID).Error; err == nil {
				if err := h.DB.Model(&created).Association("Roles").Replace([]model.Role{role}); err != nil {
					return nil, fmt.Errorf("给用户「%s」绑定默认角色失败: %w", username, err)
				}
			}
		}
		if err := h.DB.Create(&model.ImAccount{
			AppID: app.ID, Provider: app.Provider, ImUserID: person.ID,
			ImName: person.Name, ImMobile: person.Mobile, ImEmail: person.Email,
			ImDeptPath: truncate(path, 240), UserID: created.ID,
			Username: created.Username, LastSyncAt: &now,
		}).Error; err != nil {
			return nil, fmt.Errorf("写入绑定失败: %w", err)
		}
	}

	// ---------- 离职处理：只停用，不删除 ----------
	if app.DisableMissing {
		var bindings []model.ImAccount
		if err := h.DB.Where("provider = ?", app.Provider).Find(&bindings).Error; err != nil {
			return nil, fmt.Errorf("读取绑定关系失败: %w", err)
		}
		for _, binding := range bindings {
			if activeIm[binding.ImUserID] {
				continue
			}
			var user model.User
			if err := h.DB.First(&user, binding.UserID).Error; err != nil {
				continue
			}
			if user.ID == 1 {
				// 内置管理员永不停用，否则一次误同步就能把自己关在门外
				report.UserSkipped++
				report.add("user", "skip", user.Username, binding.ImUserID,
					"内置管理员，不做停用")
				continue
			}
			if user.Status != 1 {
				continue
			}
			report.UserDisabled++
			report.add("user", "disable", user.Username, binding.ImUserID,
				"IM 侧已查不到此人，停用平台账号（不删除）")
			if !dryRun {
				if err := h.DB.Model(&model.User{}).Where("id = ?", user.ID).
					Updates(map[string]any{"status": 0}).Error; err != nil {
					return nil, fmt.Errorf("停用用户「%s」失败: %w", user.Username, err)
				}
			}
		}
	}
	return report, nil
}

// imChangeSummary 把改动 map 说成人话
func imChangeSummary(changes map[string]any) string {
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := map[string]string{
		"nickname": "姓名", "email": "邮箱", "dept_id": "部门", "status": "启用状态",
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		label := labels[key]
		if label == "" {
			label = key
		}
		parts = append(parts, fmt.Sprintf("%s -> %v", label, changes[key]))
	}
	return strings.Join(parts, "，")
}

// imDeptDepth 算某个部门在 IM 侧的层级，用来保证父部门先处理
func imDeptDepth(dept imDept, all []imDept) int {
	parents := map[string]string{}
	for _, item := range all {
		parents[item.ID] = item.ParentID
	}
	depth := 0
	current := dept.ParentID
	for i := 0; i < 32; i++ { // 防环
		next, ok := parents[current]
		if !ok {
			break
		}
		depth++
		current = next
	}
	return depth
}

// ---------- 应用维护 ----------

type imAppReq struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	CorpID          string `json:"corpId"`
	AppSecret       string `json:"appSecret"`
	AgentID         string `json:"agentId"`
	BaseURL         string `json:"baseUrl"`
	RootDeptID      string `json:"rootDeptId"`
	TargetCompanyID uint   `json:"targetCompanyId"`
	DefaultRoleID   uint   `json:"defaultRoleId"`
	DisableMissing  *bool  `json:"disableMissing"`
	LoginEnabled    *bool  `json:"loginEnabled"`
	RedirectURI     string `json:"redirectUri"`
	LoginRedirect   string `json:"loginRedirect"`
	Enabled         *bool  `json:"enabled"`
	Remark          string `json:"remark"`
}

func (r *imAppReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.CorpID = strings.TrimSpace(r.CorpID)
	r.AppSecret = strings.TrimSpace(r.AppSecret)
	r.AgentID = strings.TrimSpace(r.AgentID)
	r.RootDeptID = strings.TrimSpace(r.RootDeptID)
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	r.RedirectURI = strings.TrimSpace(r.RedirectURI)
	r.LoginRedirect = strings.TrimSpace(r.LoginRedirect)

	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if !isIMChannel(r.Provider) {
		return fmt.Errorf("IM 类型只支持 wecom / dingtalk / feishu")
	}
	if r.CorpID == "" {
		return fmt.Errorf("企业标识不能为空（企微 corpid / 钉钉 appkey / 飞书 app_id）")
	}
	if r.BaseURL != "" &&
		!strings.HasPrefix(r.BaseURL, "http://") && !strings.HasPrefix(r.BaseURL, "https://") {
		return fmt.Errorf("接口地址必须以 http:// 或 https:// 开头")
	}
	if r.TargetCompanyID == 0 {
		return fmt.Errorf("请选择同步到哪个公司")
	}
	// 开了扫码登录就必须有回调地址，否则一点按钮就是个错
	if r.LoginEnabled != nil && *r.LoginEnabled {
		if r.RedirectURI == "" {
			return fmt.Errorf("开启扫码登录要填回调地址（与 IM 后台登记的一致）")
		}
		if !strings.HasPrefix(r.RedirectURI, "http://") &&
			!strings.HasPrefix(r.RedirectURI, "https://") {
			return fmt.Errorf("回调地址必须以 http:// 或 https:// 开头")
		}
		if !strings.Contains(r.RedirectURI, "/auth/im/callback") {
			return fmt.Errorf("回调地址要指向平台的 /api/v1/auth/im/callback")
		}
		if r.LoginRedirect != "" &&
			!strings.HasPrefix(r.LoginRedirect, "http://") &&
			!strings.HasPrefix(r.LoginRedirect, "https://") {
			return fmt.Errorf("登录成功跳转地址必须以 http:// 或 https:// 开头")
		}
		if r.Provider == channelWecom && r.AgentID == "" {
			return fmt.Errorf("企业微信扫码登录需要填 AgentID")
		}
	}
	// 同步起点各家默认值不同，留空时按厂商习惯兜底
	if r.RootDeptID == "" {
		switch r.Provider {
		case channelFeishu:
			r.RootDeptID = "0"
		default:
			r.RootDeptID = "1"
		}
	}
	return nil
}

func (h *Handler) ListImApps(c *gin.Context) {
	var list []model.ImApp
	if err := h.DB.Order("id desc").Find(&list).Error; err != nil {
		response.Error(c, "查询 IM 应用失败")
		return
	}
	// 绑定数量一并给出来：页面上要能看出「同步过没有、绑了多少人」
	counts := map[uint]int64{}
	for _, app := range list {
		var count int64
		h.DB.Model(&model.ImAccount{}).Where("app_id = ?", app.ID).Count(&count)
		counts[app.ID] = count
	}
	items := make([]gin.H, 0, len(list))
	for _, app := range list {
		items = append(items, gin.H{
			"app": app, "boundCount": counts[app.ID],
		})
	}
	response.OK(c, gin.H{"list": items})
}

func (h *Handler) CreateImApp(c *gin.Context) {
	var req imAppReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.AppSecret == "" {
		response.BadRequest(c, "应用密钥不能为空")
		return
	}
	var company model.Company
	if err := h.DB.First(&company, req.TargetCompanyID).Error; err != nil {
		response.BadRequest(c, "目标公司不存在")
		return
	}

	app := model.ImApp{
		Name: req.Name, Provider: req.Provider, CorpID: req.CorpID,
		AppSecret: h.sealSecret(req.AppSecret), AgentID: req.AgentID, BaseURL: req.BaseURL,
		RootDeptID: req.RootDeptID, TargetCompanyID: req.TargetCompanyID,
		DefaultRoleID: req.DefaultRoleID, AppStatus: "unknown", Remark: req.Remark,
		RedirectURI: req.RedirectURI, LoginRedirect: req.LoginRedirect,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.LoginEnabled != nil {
		app.LoginEnabled = *req.LoginEnabled
	}
	disableMissing := true
	if req.DisableMissing != nil {
		disableMissing = *req.DisableMissing
	}
	app.DisableMissing = disableMissing
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	app.Enabled = wantEnabled

	if err := h.DB.Create(&app).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled / disable_missing 都带 gorm default true，显式关掉的要写回去
	fix := map[string]any{}
	if !wantEnabled {
		fix["enabled"] = false
	}
	if !disableMissing {
		fix["disable_missing"] = false
	}
	if len(fix) > 0 {
		h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).Updates(fix)
		h.DB.First(&app, app.ID)
	}

	// 建完立刻探一次，让人马上知道凭据对不对
	check := h.checkImApp(&app)
	h.DB.First(&app, app.ID)
	response.OK(c, gin.H{"app": app, "check": check})
}

func (h *Handler) UpdateImApp(c *gin.Context) {
	id := idParam(c)
	var app model.ImApp
	if err := h.DB.First(&app, id).Error; err != nil {
		response.NotFound(c, "IM 应用不存在")
		return
	}
	var req imAppReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "provider": req.Provider, "corp_id": req.CorpID,
		"agent_id": req.AgentID, "base_url": req.BaseURL, "root_dept_id": req.RootDeptID,
		"target_company_id": req.TargetCompanyID, "default_role_id": req.DefaultRoleID,
		"redirect_uri": req.RedirectURI, "login_redirect": req.LoginRedirect,
		"remark": req.Remark,
	}
	if req.LoginEnabled != nil {
		updates["login_enabled"] = *req.LoginEnabled
	}
	if req.AppSecret != "" {
		updates["app_secret"] = h.sealSecret(req.AppSecret) // 留空表示不修改
	}
	if req.DisableMissing != nil {
		updates["disable_missing"] = *req.DisableMissing
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&app, app.ID)
	response.OK(c, app)
}

func (h *Handler) DeleteImApp(c *gin.Context) {
	id := idParam(c)
	// 绑定关系跟着应用删：留着会指向一个不存在的应用，反而误导
	if err := h.DB.Where("app_id = ?", id).Delete(&model.ImAccount{}).Error; err != nil {
		response.Error(c, "清理绑定关系失败")
		return
	}
	if err := h.DB.Delete(&model.ImApp{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 同步出来的部门与用户一律不动：它们已经是平台自己的数据了
	response.OK(c, gin.H{"note": "已删除应用与绑定关系；同步出来的部门和用户保持不变"})
}

// CheckImApp 连通性检查
func (h *Handler) CheckImApp(c *gin.Context) {
	id := idParam(c)
	var app model.ImApp
	if err := h.DB.First(&app, id).Error; err != nil {
		response.NotFound(c, "IM 应用不存在")
		return
	}
	response.OK(c, h.checkImApp(&app))
}

// checkImApp 取一次 token 再拉一次部门列表。
//
// 只取 token 不够：密钥对但没给通讯录权限的应用，token 能拿到、拉部门才会报错 ——
// 那才是同步真正要走的第一步。
func (h *Handler) checkImApp(app *model.ImApp) gin.H {
	now := time.Now()
	fail := func(detail string) gin.H {
		h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).Updates(map[string]any{
			"app_status": "error", "last_error": truncate(detail, 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	dir, err := imDirectoryFor(app.Provider, app.BaseURL, h.egressClient(imDirTimeout))
	if err != nil {
		return fail(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), imDirTimeout)
	defer cancel()

	started := time.Now()
	secret, err := h.imAppSecret(app)
	if err != nil {
		return fail(err.Error())
	}
	token, err := dir.token(ctx, app.CorpID, secret)
	if err != nil {
		return fail("取 access_token 失败: " + err.Error())
	}
	depts, err := dir.departments(ctx, token, app.RootDeptID)
	if err != nil {
		return fail("拿到了 token，但读部门失败（多半是没给通讯录权限）: " + err.Error())
	}
	latency := time.Since(started).Milliseconds()

	detail := fmt.Sprintf("%s 可用，耗时 %d ms，从起点 %s 能读到 %d 个部门",
		dir.label(), latency, app.RootDeptID, len(depts))
	h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).Updates(map[string]any{
		"app_status": "healthy", "last_error": "", "last_check_at": &now,
	})
	return gin.H{
		"status": "healthy", "detail": detail, "deptCount": len(depts), "latencyMs": latency,
	}
}

// ---------- 同步 ----------

type imSyncReq struct {
	// DryRun 只预演，不写库
	DryRun bool `json:"dryRun"`
}

func (h *Handler) SyncImApp(c *gin.Context) {
	id := idParam(c)
	var app model.ImApp
	if err := h.DB.First(&app, id).Error; err != nil {
		response.NotFound(c, "IM 应用不存在")
		return
	}
	if !app.Enabled {
		response.BadRequest(c, "该应用已停用")
		return
	}
	var req imSyncReq
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		response.BadRequest(c, "参数错误")
		return
	}

	operator := middleware.CurrentUser(c)
	run := model.ImSyncRun{
		AppID: app.ID, AppName: app.Name, Provider: app.Provider, DryRun: req.DryRun,
	}
	if operator != nil {
		run.UserIDOp, run.Operator = operator.ID, operator.Username
	}
	started := time.Now()

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	current, err := h.fetchDirectory(ctx, &app)
	if err != nil {
		run.SyncStatus = "failed"
		run.ErrorMsg = truncate(err.Error(), 480)
		run.CostMs = time.Since(started).Milliseconds()
		h.DB.Create(&run)
		response.BadRequest(c, err.Error())
		return
	}

	report, err := h.applySync(current, req.DryRun)
	run.CostMs = time.Since(started).Milliseconds()
	if err != nil {
		run.SyncStatus = "failed"
		run.ErrorMsg = truncate(err.Error(), 480)
		h.DB.Create(&run)
		response.Error(c, "同步失败: "+err.Error())
		return
	}

	run.SyncStatus = "success"
	run.DeptTotal, run.DeptCreated, run.DeptUpdated = report.DeptTotal, report.DeptCreated, report.DeptUpdated
	run.UserTotal, run.UserCreated, run.UserBound = report.UserTotal, report.UserCreated, report.UserBound
	run.UserUpdated, run.UserDisabled, run.UserSkipped = report.UserUpdated, report.UserDisabled, report.UserSkipped
	if err := h.DB.Create(&run).Error; err != nil {
		response.Error(c, "同步结果落库失败")
		return
	}
	if !req.DryRun {
		now := time.Now()
		h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).
			Updates(map[string]any{"last_sync_at": &now})
	}
	response.OK(c, gin.H{"run": run, "report": report})
}

// ListImSyncRuns 同步历史
func (h *Handler) ListImSyncRuns(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ImSyncRun{})
	if raw := strings.TrimSpace(c.Query("appId")); raw != "" {
		q = q.Where("app_id = ?", raw)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("sync_status = ?", status)
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询同步记录失败")
		return
	}
	var rows []model.ImSyncRun
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		response.Error(c, "查询同步记录失败")
		return
	}
	response.OK(c, gin.H{"list": rows, "total": total, "page": page, "pageSize": size})
}

// ListImAccounts 绑定关系
func (h *Handler) ListImAccounts(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ImAccount{})
	if raw := strings.TrimSpace(c.Query("appId")); raw != "" {
		q = q.Where("app_id = ?", raw)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("username LIKE ? OR im_name LIKE ? OR im_user_id LIKE ?", like, like, like)
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询绑定关系失败")
		return
	}
	var rows []model.ImAccount
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		response.Error(c, "查询绑定关系失败")
		return
	}
	response.OK(c, gin.H{"list": rows, "total": total, "page": page, "pageSize": size})
}

// UnbindImAccount 解绑。平台账号本身不动。
func (h *Handler) UnbindImAccount(c *gin.Context) {
	id := idParam(c)
	if err := h.DB.Delete(&model.ImAccount{}, id).Error; err != nil {
		response.Error(c, "解绑失败")
		return
	}
	response.OK(c, gin.H{"note": "已解绑；平台账号保持不变，下次同步会重新按同名规则匹配"})
}
