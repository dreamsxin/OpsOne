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
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`

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

// NotifyChannel 通知渠道。webhook 走 HTTP POST，silent 只落记录不外发。
type NotifyChannel struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	Type        string    `gorm:"size:16;default:webhook" json:"type"` // webhook | silent
	URL         string    `gorm:"size:512" json:"url"`
	HeaderKey   string    `gorm:"size:64" json:"headerKey"` // 可选的鉴权头名
	HeaderValue string    `gorm:"size:255" json:"-"`        // 鉴权头值，不出接口
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	Remark      string    `gorm:"size:255" json:"remark"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
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
