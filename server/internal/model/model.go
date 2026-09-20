package model

import "time"

// Company 公司，部门树的根归属
type Company struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Code      string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Department 部门，支持多级，ParentID=0 为公司下的一级部门
type Department struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CompanyID uint      `gorm:"index" json:"companyId"`
	ParentID  uint      `gorm:"index;default:0" json:"parentId"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Code      string    `gorm:"size:64" json:"code"`
	Leader    string    `gorm:"size:64" json:"leader"`
	Sort      int       `gorm:"default:0" json:"sort"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// User 平台用户
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:120;not null" json:"-"`
	Nickname     string     `gorm:"size:64" json:"nickname"`
	Email        string     `gorm:"size:128" json:"email"`
	DeptID       uint       `gorm:"index;default:0" json:"deptId"`
	Status       int        `gorm:"default:1" json:"status"` // 1 启用 0 禁用
	LastLoginAt  *time.Time `json:"lastLoginAt"`

	// 双因子口令（TOTP）。Secret 明文存库，与主机凭据同等对待，接口不返回。
	// 绑定流程：setup 写入 Secret（Enabled=false）→ confirm 校验通过后置 Enabled。
	TOTPSecret      string     `gorm:"size:64" json:"-"`
	TOTPEnabled     bool       `gorm:"default:false" json:"totpEnabled"`
	TOTPBoundAt     *time.Time `json:"totpBoundAt"`
	TOTPLastCounter int64      `json:"-"` // 已用过的时间窗，用于拒绝同一验证码重放

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	Roles []Role `gorm:"many2many:user_roles" json:"roles,omitempty"`
}

// 数据范围取值
const (
	ScopeAll       = "all"        // 全部数据
	ScopeDept      = "dept"       // 本部门
	ScopeDeptBelow = "dept_below" // 本部门及下级
	ScopeSelf      = "self"       // 仅本人创建
	ScopeCustom    = "custom"     // 指定部门
)

// Role 角色。DataScope 决定该角色能看到哪些业务数据（当前作用于主机资产）。
type Role struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Code        string `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name        string `gorm:"size:64;not null" json:"name"`
	Description string `gorm:"size:255" json:"description"`
	// DataScope all | dept | dept_below | self | custom，缺省 all
	DataScope string `gorm:"size:16;default:all" json:"dataScope"`
	// DataDeptIDs DataScope=custom 时生效，JSON 数组；接口层用 dataDeptIds 暴露
	DataDeptIDs string    `gorm:"type:text" json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	Menus []Menu `gorm:"many2many:role_menus" json:"menus,omitempty"`
}

// Menu 菜单与按钮权限。Type=menu 时参与前端路由生成，Type=button 时只作为权限点。
//
// Builtin=true 表示由种子数据维护：结构字段（路径、组件、父级、类型、权限码）以代码为准，
// 每次启动覆盖；展示字段（标题、图标、排序、隐藏）由用户在菜单管理里维护，启动不覆盖。
type Menu struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ParentID  uint      `gorm:"index;default:0" json:"parentId"`
	Name      string    `gorm:"size:64;not null" json:"name"`  // 路由 name
	Title     string    `gorm:"size:64;not null" json:"title"` // 展示标题
	Path      string    `gorm:"size:128" json:"path"`
	Component string    `gorm:"size:128" json:"component"`
	Icon      string    `gorm:"size:64" json:"icon"`
	Type      string    `gorm:"size:16;default:menu" json:"type"`
	AuthCode  string    `gorm:"size:64" json:"authCode"` // 权限标识，如 host:create
	Sort      int       `gorm:"default:0" json:"sort"`
	Hidden    bool      `gorm:"default:false" json:"hidden"`
	Builtin   bool      `gorm:"default:false" json:"builtin"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// 资源授权支持的动作
const (
	ActionTerminal = "terminal" // 打开 Web 终端
	ActionFile     = "file"     // 文件管理
	ActionExec     = "exec"     // 批量执行与定时任务
	ActionManage   = "manage"   // 编辑与删除资产
	ActionAll      = "*"        // 全部动作
)

// ResourceGrant 资源授权：在数据范围之外，把指定资源额外授予某个用户或角色。
//
// 与数据范围是「叠加」关系：数据范围决定默认能看到什么，授权额外放开具体资源，
// 并可限定动作集合与有效期。授权只放开、不收窄，数据范围内已有的权限不受影响。
type ResourceGrant struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	SubjectType  string     `gorm:"size:16;index;not null" json:"subjectType"` // user | role
	SubjectID    uint       `gorm:"index;not null" json:"subjectId"`
	SubjectName  string     `gorm:"size:64" json:"subjectName"`
	ResourceType string     `gorm:"size:16;index;default:host" json:"resourceType"` // host | database
	ResourceID   uint       `gorm:"index;not null" json:"resourceId"`
	ResourceName string     `gorm:"size:128" json:"resourceName"`
	Actions      string     `gorm:"size:128;default:*" json:"actions"` // 逗号分隔，或 *
	ExpiresAt    *time.Time `json:"expiresAt"`                         // 空表示长期有效
	Remark       string     `gorm:"size:255" json:"remark"`
	Operator     string     `gorm:"size:64" json:"operator"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// SiteLink 站点导航条目，用于集中收拢内部系统入口
type SiteLink struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	URL         string    `gorm:"size:512;not null" json:"url"`
	Category    string    `gorm:"size:32;default:general" json:"category"`
	Icon        string    `gorm:"size:64" json:"icon"`
	Description string    `gorm:"size:255" json:"description"`
	Sort        int       `gorm:"default:0" json:"sort"`
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// EmailTemplate 邮件模板，正文用 Go text/template 语法，如 {{.Title}}
type EmailTemplate struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Code      string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Subject   string    `gorm:"size:255;not null" json:"subject"`
	Body      string    `gorm:"type:text" json:"body"`
	Variables string    `gorm:"size:255" json:"variables"` // 可用变量提示，逗号分隔
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Builtin   bool      `gorm:"default:false" json:"builtin"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Host 主机资产
type Host struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	Address  string `gorm:"size:128;not null" json:"address"`
	Port     int    `gorm:"default:22" json:"port"`
	Username string `gorm:"size:64;not null" json:"username"`
	AuthType string `gorm:"size:16;default:password" json:"authType"` // password | key
	// Secret 登录密码或私钥，当前为明文存储（已知风险，见 docs/SECURITY.md），不出接口
	Secret string `gorm:"type:text" json:"-"`
	// HostKey 主机公钥（authorized_keys 格式），开启指纹校验后首次连接自动记录
	HostKey string `gorm:"type:text" json:"-"`
	// ProxyHostID 跳板机，0 表示直连。目标主机不可直达时经该主机建立隧道
	ProxyHostID uint `gorm:"index;default:0" json:"proxyHostId"`
	// DeptID 归属部门，数据权限按此字段过滤；0 表示未归属（仅全部数据范围可见）
	DeptID uint `gorm:"index;default:0" json:"deptId"`
	// CreatedBy 录入人，数据范围为「仅本人」时按此过滤
	CreatedBy uint       `gorm:"index;default:0" json:"createdBy"`
	Env       string     `gorm:"size:16;default:dev" json:"env"` // dev | test | prod
	Tags      string     `gorm:"size:255" json:"tags"`
	OSInfo    string     `gorm:"size:128" json:"osInfo"`
	Status    string     `gorm:"size:16;default:unknown" json:"status"` // online | offline | unknown
	CheckedAt *time.Time `json:"checkedAt"`
	Remark    string     `gorm:"size:255" json:"remark"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// CommandRule 命令审计规则，命中后按 Action 处理
type CommandRule struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Pattern     string    `gorm:"size:255;not null" json:"pattern"` // Go 正则
	Description string    `gorm:"size:255" json:"description"`
	Action      string    `gorm:"size:16;default:block" json:"action"` // block | warn
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Session 一次 Web 终端会话
type Session struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	HostID       uint       `gorm:"index" json:"hostId"`
	HostName     string     `gorm:"size:64" json:"hostName"`
	Address      string     `gorm:"size:128" json:"address"`
	LoginUser    string     `gorm:"size:64" json:"loginUser"` // 登录目标主机的系统账号
	UserID       uint       `gorm:"index" json:"userId"`
	Username     string     `gorm:"size:64" json:"username"` // 平台操作人
	ClientIP     string     `gorm:"size:64" json:"clientIp"`
	ViaProxy     string     `gorm:"size:128" json:"viaProxy"`             // 跳板机描述，空表示直连
	Status       string     `gorm:"size:16;default:active" json:"status"` // active | closed | error
	RecordPath   string     `gorm:"size:255" json:"-"`                    // 录像文件路径，不出接口
	ErrorMsg     string     `gorm:"size:255" json:"errorMsg"`
	CommandCount int        `json:"commandCount"`
	BlockedCount int        `json:"blockedCount"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt"`
	DurationMs   int64      `json:"durationMs"`

	Commands []SessionCommand `gorm:"foreignKey:SessionID" json:"commands,omitempty"`
}

// SessionCommand 会话中执行的单条命令
type SessionCommand struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	SessionID uint      `gorm:"index;not null" json:"sessionId"`
	Command   string    `gorm:"type:text" json:"command"`
	Risk      string    `gorm:"size:16;default:normal" json:"risk"` // normal | warn | blocked
	RuleID    uint      `json:"ruleId"`
	OffsetMs  int64     `json:"offsetMs"` // 相对会话开始的毫秒偏移，便于回放定位
	CreatedAt time.Time `json:"createdAt"`
}

// ExecJob 批量执行作业
type ExecJob struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:128" json:"name"`
	Command    string     `gorm:"type:text;not null" json:"command"`
	Timeout    int        `gorm:"default:60" json:"timeout"` // 单主机超时秒数
	Status     string     `gorm:"size:16;default:running" json:"status"`
	Source     string     `gorm:"size:16;default:manual" json:"source"` // manual | cron
	CronJobID  uint       `gorm:"index;default:0" json:"cronJobId"`
	CreatedBy  uint       `gorm:"index" json:"createdBy"`
	Operator   string     `gorm:"size:64" json:"operator"`
	Total      int        `json:"total"`
	SuccessNum int        `json:"successNum"`
	FailedNum  int        `json:"failedNum"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	Results []ExecResult `gorm:"foreignKey:JobID" json:"results,omitempty"`
}

// CronJob 定时任务：按 cron 表达式在一批主机上执行命令
type CronJob struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:128;not null" json:"name"`
	Spec       string     `gorm:"size:64;not null" json:"spec"` // 五段 cron 表达式
	Command    string     `gorm:"type:text;not null" json:"command"`
	HostIDs    string     `gorm:"type:text" json:"-"` // JSON 数组，接口层用 hostIds 暴露
	Timeout    int        `gorm:"default:60" json:"timeout"`
	Enabled    bool       `gorm:"default:true" json:"enabled"`
	CreatedBy  uint       `gorm:"index" json:"createdBy"`
	Operator   string     `gorm:"size:64" json:"operator"`
	RunCount   int        `json:"runCount"`
	LastStatus string     `gorm:"size:16" json:"lastStatus"` // success | partial | failed
	LastRunAt  *time.Time `json:"lastRunAt"`
	LastJobID  uint       `json:"lastJobId"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// FileAudit 文件传输留痕，列目录不记录，仅记录产生变更或数据外带的动作
type FileAudit struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	HostID     uint      `gorm:"index" json:"hostId"`
	HostName   string    `gorm:"size:64" json:"hostName"`
	Address    string    `gorm:"size:128" json:"address"`
	UserID     uint      `gorm:"index" json:"userId"`
	Username   string    `gorm:"size:64" json:"username"`
	Action     string    `gorm:"size:16" json:"action"` // upload | download | delete | mkdir | rename
	Path       string    `gorm:"size:512" json:"path"`
	TargetPath string    `gorm:"size:512" json:"targetPath"`
	Size       int64     `json:"size"`
	Status     string    `gorm:"size:16" json:"status"` // success | failed
	ErrorMsg   string    `gorm:"size:255" json:"errorMsg"`
	ClientIP   string    `gorm:"size:64" json:"clientIp"`
	CreatedAt  time.Time `gorm:"index" json:"createdAt"`
}

// ExecResult 单主机执行结果
type ExecResult struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	JobID    uint   `gorm:"index;not null" json:"jobId"`
	HostID   uint   `gorm:"index;not null" json:"hostId"`
	HostName string `gorm:"size:64" json:"hostName"`
	Address  string `gorm:"size:128" json:"address"`
	Status   string `gorm:"size:16" json:"status"` // success | failed | timeout
	ExitCode int    `json:"exitCode"`
	Stdout   string `gorm:"type:text" json:"stdout"`
	Stderr   string `gorm:"type:text" json:"stderr"`
	CostMs   int64  `json:"costMs"`
}

// AuditLog 操作审计
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"userId"`
	Username  string    `gorm:"size:64" json:"username"`
	Method    string    `gorm:"size:8" json:"method"`
	Path      string    `gorm:"size:255" json:"path"`
	Action    string    `gorm:"size:64" json:"action"`
	Status    int       `json:"status"`
	IP        string    `gorm:"size:64" json:"ip"`
	CostMs    int64     `json:"costMs"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// AlertSource 告警接入源，外部系统用 Token 推送告警
type AlertSource struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:64;not null" json:"name"`
	Token         string     `gorm:"size:64;uniqueIndex;not null" json:"token"`
	Enabled       bool       `gorm:"default:true" json:"enabled"`
	Remark        string     `gorm:"size:255" json:"remark"`
	ReceivedCount int        `json:"receivedCount"`
	LastSeenAt    *time.Time `json:"lastSeenAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// Alert 告警实例，按 Fingerprint 去重，同一指纹重复上报只累加次数
type Alert struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	SourceID    uint       `gorm:"index" json:"sourceId"`
	SourceName  string     `gorm:"size:64" json:"sourceName"`
	Fingerprint string     `gorm:"size:64;index" json:"fingerprint"`
	Title       string     `gorm:"size:255;not null" json:"title"`
	Summary     string     `gorm:"type:text" json:"summary"`
	Severity    string     `gorm:"size:16;index" json:"severity"`              // critical | warning | info
	Status      string     `gorm:"size:16;index;default:firing" json:"status"` // firing | acked | resolved
	Labels      string     `gorm:"type:text" json:"labels"`                    // JSON 对象字符串
	Value       string     `gorm:"size:64" json:"value"`
	Count       int        `gorm:"default:1" json:"count"`
	FirstSeenAt time.Time  `json:"firstSeenAt"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	AckBy       string     `gorm:"size:64" json:"ackBy"`
	AckAt       *time.Time `json:"ackAt"`
	ResolvedAt  *time.Time `json:"resolvedAt"`
	HandleNote  string     `gorm:"size:255" json:"handleNote"`
}

// NotifyChannel 通知渠道。webhook 走 HTTP POST，email 走 SMTP，silent 只落记录不外发。
type NotifyChannel struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:64;not null" json:"name"`
	Type        string `gorm:"size:16;default:webhook" json:"type"` // webhook | email | silent
	URL         string `gorm:"size:512" json:"url"`
	HeaderKey   string `gorm:"size:64" json:"headerKey"` // 可选的鉴权头名
	HeaderValue string `gorm:"size:255" json:"-"`        // 鉴权头值，不出接口
	// Recipients / TemplateCode 仅 email 类型使用
	Recipients   string    `gorm:"size:512" json:"recipients"`  // 逗号分隔收件人
	TemplateCode string    `gorm:"size:64" json:"templateCode"` // 邮件模板编码，空则用内置格式
	Enabled      bool      `gorm:"default:true" json:"enabled"`
	Remark       string    `gorm:"size:255" json:"remark"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// NotifyRoute 通知路由：按告警级别与标签匹配，命中后投递到指定渠道。
// 按 Priority 升序取第一条命中的路由；都没命中时用 IsDefault 的兜底路由。
type NotifyRoute struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Name          string    `gorm:"size:64;not null" json:"name"`
	Priority      int       `gorm:"default:100" json:"priority"`
	MatchSeverity string    `gorm:"size:64" json:"matchSeverity"` // 逗号分隔，空表示不限
	MatchLabels   string    `gorm:"type:text" json:"matchLabels"` // JSON 对象，需全部命中
	ChannelIDs    string    `gorm:"type:text" json:"-"`           // JSON 数组，接口层用 channelIds 暴露
	IsDefault     bool      `gorm:"default:false" json:"isDefault"`
	Enabled       bool      `gorm:"default:true" json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// NotifyRecord 通知投递流水
type NotifyRecord struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	AlertID     uint      `gorm:"index" json:"alertId"`
	AlertTitle  string    `gorm:"size:255" json:"alertTitle"`
	RouteID     uint      `json:"routeId"`
	RouteName   string    `gorm:"size:64" json:"routeName"`
	ChannelID   uint      `gorm:"index" json:"channelId"`
	ChannelName string    `gorm:"size:64" json:"channelName"`
	Status      string    `gorm:"size:16" json:"status"` // success | failed
	HTTPStatus  int       `json:"httpStatus"`
	ErrorMsg    string    `gorm:"size:255" json:"errorMsg"`
	CostMs      int64     `json:"costMs"`
	CreatedAt   time.Time `gorm:"index" json:"createdAt"`
}

// Announcement 平台公告。发布后对全员可见，并给每个启用用户投递一条站内消息。
type Announcement struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Title       string     `gorm:"size:255;not null" json:"title"`
	Content     string     `gorm:"type:text" json:"content"`
	Level       string     `gorm:"size:16;default:info" json:"level"` // info | warning
	Published   bool       `gorm:"default:false;index" json:"published"`
	PublishedAt *time.Time `json:"publishedAt"`
	Publisher   string     `gorm:"size:64" json:"publisher"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Message 站内消息，按用户维度存储。来源为公告发布与严重告警。
type Message struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"userId"`
	Type      string     `gorm:"size:16;index" json:"type"` // announcement | alert
	Title     string     `gorm:"size:255" json:"title"`
	Content   string     `gorm:"type:text" json:"content"`
	Level     string     `gorm:"size:16;default:info" json:"level"` // info | warning | critical
	RefID     uint       `json:"refId"`                             // 关联的公告或告警 ID
	Read      bool       `gorm:"default:false;index" json:"read"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `gorm:"index" json:"createdAt"`
}

// SysConfig 平台配置项。Builtin 为 true 的键由种子数据维护，不可删除只可改值。
type SysConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Group     string    `gorm:"size:32;index;default:general" json:"group"`
	Key       string    `gorm:"size:64;uniqueIndex;not null" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	Type      string    `gorm:"size:16;default:string" json:"type"` // string | int | bool | text
	Label     string    `gorm:"size:64" json:"label"`
	Remark    string    `gorm:"size:255" json:"remark"`
	Builtin   bool      `gorm:"default:false" json:"builtin"`
	UpdatedBy string    `gorm:"size:64" json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Tag 资产标签字典。主机的 tags 字段仍是逗号分隔文本，这里提供统一词表与用量统计。
type Tag struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Category  string    `gorm:"size:32;default:general" json:"category"` // 用途分类，如 role / env / owner
	Color     string    `gorm:"size:16;default:info" json:"color"`       // Element Plus 标签类型
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DBInstance 数据库资产。凭据与主机一致为明文存储（见 docs/SECURITY.md）。
type DBInstance struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	Type     string `gorm:"size:16;default:mysql" json:"type"` // mysql | postgres | redis | mongo | other
	Address  string `gorm:"size:128;not null" json:"address"`
	Port     int    `gorm:"default:3306" json:"port"`
	Username string `gorm:"size:64" json:"username"`
	Secret   string `gorm:"type:text" json:"-"`
	DBName   string `gorm:"size:64" json:"dbName"`
	Version  string `gorm:"size:32" json:"version"`
	Env      string `gorm:"size:16;default:dev" json:"env"`
	// DeptID / CreatedBy 与主机一致，参与数据权限过滤
	DeptID    uint       `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint       `gorm:"index;default:0" json:"createdBy"`
	Status    string     `gorm:"size:16;default:unknown" json:"status"` // online | offline | unknown
	CheckedAt *time.Time `json:"checkedAt"`
	Tags      string     `gorm:"size:255" json:"tags"`
	Remark    string     `gorm:"size:255" json:"remark"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// FixedAsset 固定资产台账，用于设备盘点与到保跟踪
type FixedAsset struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	Category string `gorm:"size:16;default:server" json:"category"` // server | network | storage | terminal | other
	SN       string `gorm:"size:64;index" json:"sn"`
	Model    string `gorm:"size:64" json:"model"`
	Vendor   string `gorm:"size:64" json:"vendor"`
	Location string `gorm:"size:128" json:"location"`
	Owner    string `gorm:"size:64" json:"owner"`
	// HostID 关联的纳管主机，0 表示未关联
	HostID        uint       `gorm:"index;default:0" json:"hostId"`
	DeptID        uint       `gorm:"index;default:0" json:"deptId"`
	CreatedBy     uint       `gorm:"index;default:0" json:"createdBy"`
	Status        string     `gorm:"size:16;default:in_use" json:"status"` // in_use | idle | repair | scrapped
	PurchaseDate  *time.Time `json:"purchaseDate"`
	PurchasePrice float64    `json:"purchasePrice"`
	WarrantyEnd   *time.Time `json:"warrantyEnd"`
	Remark        string     `gorm:"size:255" json:"remark"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// CloudAccount 云账号台账。
//
// 只做账号与密钥的集中登记，**不做资源同步** —— 同步需要各云厂商 SDK 与出网能力，
// 目前不在实现范围内，页面上会显式说明，避免误以为能拉到云上资源。
type CloudAccount struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:64;not null" json:"name"`
	Provider        string    `gorm:"size:16;default:aliyun" json:"provider"` // aliyun | tencent | huawei | aws | other
	AccessKeyID     string    `gorm:"size:128" json:"accessKeyId"`
	AccessKeySecret string    `gorm:"type:text" json:"-"` // 明文存储，与主机凭据同等对待
	Region          string    `gorm:"size:64" json:"region"`
	AccountID       string    `gorm:"size:64" json:"accountId"` // 云上主账号 ID，便于对账
	DeptID          uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy       uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled         bool      `gorm:"default:true" json:"enabled"`
	Remark          string    `gorm:"size:255" json:"remark"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// InventoryBatch 资产盘点批次。创建时按范围对固定资产做快照生成明细。
type InventoryBatch struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Name         string     `gorm:"size:128;not null" json:"name"`
	ScopeDeptID  uint       `gorm:"default:0" json:"scopeDeptId"`          // 0 表示全部可见资产
	Status       string     `gorm:"size:16;default:ongoing" json:"status"` // ongoing | finished
	Operator     string     `gorm:"size:64" json:"operator"`
	DeptID       uint       `gorm:"index;default:0" json:"deptId"`
	CreatedBy    uint       `gorm:"index;default:0" json:"createdBy"`
	TotalCount   int        `json:"totalCount"`
	CheckedCount int        `json:"checkedCount"`
	MatchedCount int        `json:"matchedCount"`
	MissingCount int        `json:"missingCount"`
	MovedCount   int        `json:"movedCount"`
	Remark       string     `gorm:"size:255" json:"remark"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`

	Items []InventoryItem `gorm:"foreignKey:BatchID" json:"items,omitempty"`
}

// InventoryItem 盘点明细，一条对应一件固定资产
type InventoryItem struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	BatchID        uint       `gorm:"index;not null" json:"batchId"`
	AssetID        uint       `gorm:"index" json:"assetId"`
	AssetName      string     `gorm:"size:64" json:"assetName"`
	SN             string     `gorm:"size:64" json:"sn"`
	ExpectLocation string     `gorm:"size:128" json:"expectLocation"`
	ActualLocation string     `gorm:"size:128" json:"actualLocation"`
	Result         string     `gorm:"size:16;default:pending" json:"result"` // pending | matched | missing | moved
	Note           string     `gorm:"size:255" json:"note"`
	CheckedBy      string     `gorm:"size:64" json:"checkedBy"`
	CheckedAt      *time.Time `json:"checkedAt"`
}

// PurchaseOrder 采购单。收货入库时可按明细生成固定资产。
type PurchaseOrder struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	OrderNo      string     `gorm:"size:64;uniqueIndex;not null" json:"orderNo"`
	Title        string     `gorm:"size:128;not null" json:"title"`
	Vendor       string     `gorm:"size:64" json:"vendor"`
	Applicant    string     `gorm:"size:64" json:"applicant"`
	Status       string     `gorm:"size:16;default:draft" json:"status"` // draft | ordered | received | cancelled
	Amount       float64    `json:"amount"`                              // 由明细汇总
	DeptID       uint       `gorm:"index;default:0" json:"deptId"`
	CreatedBy    uint       `gorm:"index;default:0" json:"createdBy"`
	OrderDate    *time.Time `json:"orderDate"`
	ExpectedDate *time.Time `json:"expectedDate"`
	ReceivedDate *time.Time `json:"receivedDate"`
	AssetCreated bool       `gorm:"default:false" json:"assetCreated"` // 是否已生成固定资产
	Remark       string     `gorm:"size:255" json:"remark"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`

	Items []PurchaseItem `gorm:"foreignKey:OrderID" json:"items,omitempty"`
}

// PurchaseItem 采购明细
type PurchaseItem struct {
	ID        uint    `gorm:"primaryKey" json:"id"`
	OrderID   uint    `gorm:"index;not null" json:"orderId"`
	Name      string  `gorm:"size:64;not null" json:"name"`
	Category  string  `gorm:"size:16;default:server" json:"category"` // 与固定资产类别一致
	Model     string  `gorm:"size:64" json:"model"`
	Vendor    string  `gorm:"size:64" json:"vendor"`
	Quantity  int     `gorm:"default:1" json:"quantity"`
	UnitPrice float64 `json:"unitPrice"`
	Remark    string  `gorm:"size:255" json:"remark"`
}

// BuildServer Jenkins 服务器登记。Token 用 Jenkins 的 API Token，不要用登录密码。
type BuildServer struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	URL       string    `gorm:"size:255;not null" json:"url"` // 如 https://jenkins.example.com
	Username  string    `gorm:"size:64" json:"username"`
	Token     string    `gorm:"size:255" json:"-"` // 明文存储，不出接口
	DeptID    uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BuildJob 构建任务，映射到 Jenkins 上的一个 job
type BuildJob struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Name        string     `gorm:"size:64;not null" json:"name"`
	ServerID    uint       `gorm:"index;not null" json:"serverId"`
	ServerName  string     `gorm:"size:64" json:"serverName"`
	JobPath     string     `gorm:"size:255;not null" json:"jobPath"` // Jenkins job 名，支持 folder/job 形式
	Params      string     `gorm:"type:text" json:"params"`          // 默认参数，JSON 对象
	DeptID      uint       `gorm:"index;default:0" json:"deptId"`
	CreatedBy   uint       `gorm:"index;default:0" json:"createdBy"`
	Enabled     bool       `gorm:"default:true" json:"enabled"`
	Remark      string     `gorm:"size:255" json:"remark"`
	LastBuildNo int        `json:"lastBuildNo"`
	LastStatus  string     `gorm:"size:16" json:"lastStatus"` // triggered | success | failure | unstable | aborted | unknown
	LastRunAt   *time.Time `json:"lastRunAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// BuildRecord 一次构建触发的记录。状态需要主动同步，平台不做轮询。
type BuildRecord struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	JobID       uint       `gorm:"index;not null" json:"jobId"`
	JobName     string     `gorm:"size:64" json:"jobName"`
	BuildNo     int        `json:"buildNo"` // 排队阶段为 0，同步后回填
	Status      string     `gorm:"size:16;default:triggered" json:"status"`
	Params      string     `gorm:"type:text" json:"params"`
	QueueURL    string     `gorm:"size:255" json:"queueUrl"`
	BuildURL    string     `gorm:"size:255" json:"buildUrl"`
	TriggeredBy string     `gorm:"size:64" json:"triggeredBy"`
	DurationMs  int64      `json:"durationMs"`
	ErrorMsg    string     `gorm:"size:255" json:"errorMsg"`
	StartedAt   time.Time  `json:"startedAt"`
	SyncedAt    *time.Time `json:"syncedAt"`
}

// Certificate TLS 证书巡检对象。
//
// 平台只做「探测 + 到期提醒」：连上目标端口读取对端证书，记录颁发者、有效期与
// 链校验结果。不签发、不托管私钥、不做自动续签（ACME）。
type Certificate struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	Name       string `gorm:"size:64;not null" json:"name"`
	Domain     string `gorm:"size:128;not null" json:"domain"` // 探测目标主机名或 IP
	Port       int    `gorm:"default:443" json:"port"`
	ServerName string `gorm:"size:128" json:"serverName"` // SNI，留空则用 Domain

	// 以下字段由巡检回填，不接受手工录入
	Issuer       string     `gorm:"size:255" json:"issuer"`
	Subject      string     `gorm:"size:255" json:"subject"`
	DNSNames     string     `gorm:"type:text" json:"dnsNames"` // 逗号分隔的 SAN
	SerialNumber string     `gorm:"size:64" json:"serialNumber"`
	Fingerprint  string     `gorm:"size:95" json:"fingerprint"` // SHA256，冒号分隔
	NotBefore    *time.Time `json:"notBefore"`
	NotAfter     *time.Time `json:"notAfter"`
	DaysLeft     int        `json:"daysLeft"`
	Status       string     `gorm:"size:16;default:unknown" json:"status"` // unknown | valid | expiring | expired | error
	Trusted      bool       `json:"trusted"`                               // 系统根证书链 + 主机名校验是否通过
	VerifyError  string     `gorm:"size:255" json:"verifyError"`
	LastCheckAt  *time.Time `json:"lastCheckAt"`
	ErrorMsg     string     `gorm:"size:255" json:"errorMsg"`

	AlertDays    int  `gorm:"default:30" json:"alertDays"`      // 剩余天数低于该值判为 expiring
	AlertEnabled bool `gorm:"default:true" json:"alertEnabled"` // 是否把巡检结果送进告警通道

	DeptID    uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled   bool      `gorm:"default:true" json:"enabled"` // 停用后不参与批量巡检
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AlertRule 基于平台自身数据的告警规则。
//
// 指标全部来自平台已有的表（主机探测状态、证书巡检、执行记录、告警积压等），
// 不依赖 Prometheus。规则周期评估，命中即写告警并走通知路由，不命中则恢复。
type AlertRule struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	Name   string `gorm:"size:64;not null" json:"name"`
	Metric string `gorm:"size:32;not null" json:"metric"` // 见 handler 里的内置指标注册表

	Comparator string  `gorm:"size:4;default:gt" json:"comparator"` // gt | gte | lt | lte
	Threshold  float64 `json:"threshold"`
	// WindowMinutes 仅对「最近 N 分钟」类指标有效，其余指标忽略
	WindowMinutes int `gorm:"default:60" json:"windowMinutes"`
	// ConsecutiveTimes 连续命中多少次才真正告警，用于抑制抖动
	ConsecutiveTimes int    `gorm:"default:1" json:"consecutiveTimes"`
	Severity         string `gorm:"size:16;default:warning" json:"severity"` // info | warning | critical

	HitStreak  int        `json:"hitStreak"`                                 // 当前连续命中次数
	LastValue  float64    `json:"lastValue"`                                 // 最近一次取到的指标值
	LastStatus string     `gorm:"size:16;default:unknown" json:"lastStatus"` // unknown | ok | firing | error
	LastDetail string     `gorm:"size:255" json:"lastDetail"`
	LastEvalAt *time.Time `json:"lastEvalAt"`
	LastFireAt *time.Time `json:"lastFireAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
