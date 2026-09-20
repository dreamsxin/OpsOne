package model

import "time"

// User 平台用户
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:120;not null" json:"-"`
	Nickname     string     `gorm:"size:64" json:"nickname"`
	Email        string     `gorm:"size:128" json:"email"`
	Status       int        `gorm:"default:1" json:"status"` // 1 启用 0 禁用
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`

	Roles []Role `gorm:"many2many:user_roles" json:"roles,omitempty"`
}

// Role 角色
type Role struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Code        string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
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
	ProxyHostID uint       `gorm:"index;default:0" json:"proxyHostId"`
	Env         string     `gorm:"size:16;default:dev" json:"env"` // dev | test | prod
	Tags        string     `gorm:"size:255" json:"tags"`
	OSInfo      string     `gorm:"size:128" json:"osInfo"`
	Status      string     `gorm:"size:16;default:unknown" json:"status"` // online | offline | unknown
	CheckedAt   *time.Time `json:"checkedAt"`
	Remark      string     `gorm:"size:255" json:"remark"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
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
