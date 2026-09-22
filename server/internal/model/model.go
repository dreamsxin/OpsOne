// Package model 是平台的全部数据表定义。
//
// # 布尔字段不要加 gorm:"default:true"
//
// 这是这个包里唯一一条硬规则，因为它已经被踩了五次（Domain.AlertEnabled、
// NotifyTemplate.Enabled、AlertRule.Enabled，以及另外两处在代码里写了绕过逻辑的）。
//
// GORM 对带 default 标签的字段，在 Create 时会把**零值从 INSERT 语句里省掉**，
// 让数据库去填默认值。对布尔字段来说零值就是 false，于是：
//
//	用户在界面上把开关关掉  →  Enabled=false  →  GORM 不写这一列
//	                        →  数据库填 default true  →  开关自己弹回去了
//
// 表现是「关不掉」，而且不报错。`Select("*")` 不能解决（它管的是 Update），
// 能解决的只有：**不加 default 标签**，或者插入后再 Updates 一次把意图写回去。
// 后者是打补丁，所以这里选前者 —— 代价是数据库层面没有默认值，
// 由建记录的代码负责显式赋值（平台所有 CRUD handler 本来就是这么写的：
// 先 `enabled := true`，再按请求里的 *bool 覆盖）。
//
// 还带着这个标签的字段列在 bool_default_test.go 的 auditedPending 清单里，
// 每一条都注明了为什么还没动。新增模型如果给布尔字段加了这个标签，那个测试会失败。
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

	// 双因子口令（TOTP）。Secret 仍是明文存库（唯一还没纳入加密的凭据类字段，
	// 理由见 handler/secret_audit.go 的 pendingSecretFields），接口不返回。
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

// KubeGrant 容器平台授权：把「哪个集群、哪些命名空间、哪些资源类型」授予某个用户或角色。
//
// 为什么不复用 ResourceGrant：那张表是 (主体, 资源类型, 资源 ID, 动作) 四元组，
// 表达不了「命名空间清单」与「资源类型清单」这两个额外维度。硬塞进 Actions
// 字段会让它变成一个自定义 DSL，两边的校验逻辑都要跟着复杂化。
//
// 为什么不像参照站那样拆成「K8s 授权」（集群）与「K8s 数据授权」（命名空间 / 资源）两张表：
// 拆开之后必然出现「两处配置互相矛盾时听谁的」——而这个问题没有直觉正确的答案，
// 任何一种取舍都会让人算不准自己到底能看到什么。一条授权表达完整的可见范围。
//
// **默认不生效**：配置项 kube.grant_enforce 为 false 时，任何登录用户仍能看到所有集群
// （这是这一页上线前的既有行为）。默认打开会让升级瞬间把所有人锁在外面 ——
// 那种"安全"是靠制造事故换来的。页面上照实写明当前是哪种状态。
type KubeGrant struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// SubjectType user | role
	SubjectType string `gorm:"size:16;index;not null" json:"subjectType"`
	SubjectID   uint   `gorm:"index;not null" json:"subjectId"`
	SubjectName string `gorm:"size:64" json:"subjectName"`
	// ClusterID 必填。刻意不支持「全部集群」：那等于把一条授权写成一张空白支票，
	// 新接入的集群会被它自动包含进去，而没人会记得回来看这条授权
	ClusterID   uint   `gorm:"index;not null" json:"clusterId"`
	ClusterName string `gorm:"size:64" json:"clusterName"`
	// Namespaces 逗号分隔的命名空间清单。空表示该集群全部命名空间。
	// 非空时**跨命名空间的列表请求会被拒绝**（而不是静默只返回允许的那部分）——
	// 静默过滤会让人以为集群里只有这些东西
	Namespaces string `gorm:"size:512" json:"namespaces"`
	// Kinds 逗号分隔的资源类型（与资源浏览页的 kind 取值一致）。空表示不限
	Kinds string `gorm:"size:512" json:"kinds"`
	// AllowLogs 能否看 Pod 日志。单独一个开关是因为日志里常有业务数据与密钥，
	// 「能看到这个命名空间的对象」与「能读它的日志」不是一件事
	AllowLogs bool `gorm:"default:false" json:"allowLogs"`
	// AllowWrite 能否 apply / scale / restart。仍然要同时具备 kube:write 功能权限，
	// 两者是 AND 关系：功能权限管「这个人有没有这类操作」，授权管「在哪个集群上」
	AllowWrite bool `gorm:"default:false" json:"allowWrite"`
	// AllowForward 能否开端口转发隧道。同样要同时具备 kube:forward
	AllowForward bool `gorm:"default:false" json:"allowForward"`
	// ExpiresAt 空表示长期有效
	ExpiresAt *time.Time `json:"expiresAt"`
	Remark    string     `gorm:"size:255" json:"remark"`
	Operator  string     `gorm:"size:64" json:"operator"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// EmailTemplate 邮件模板，正文用 Go text/template 语法，如 {{.Title}}
type EmailTemplate struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Code      string    `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name      string    `gorm:"size:64;not null" json:"name"`
	Subject   string    `gorm:"size:255;not null" json:"subject"`
	Body      string    `gorm:"type:text" json:"body"`
	Variables string    `gorm:"size:255" json:"variables"` // 可用变量提示，逗号分隔
	Builtin   bool      `gorm:"default:false" json:"builtin"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ExposureTarget 暴露面监测目标。
//
// 回答的是「这台机器对外开着哪些端口，和我登记的一不一样」——新开的端口
// 往往不是谁刻意配置的，而是某次改动的副作用，平时没人发现。
type ExposureTarget struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Address IP 或域名，不带端口
	Address string `gorm:"size:191;not null" json:"address"`
	// Ports 要扫的端口清单，支持 22,80,443 与 8000-8010 混写
	Ports string `gorm:"size:500;not null" json:"ports"`
	// Baseline 登记在册、允许开放的端口。留空表示任何开放端口都要提示
	Baseline string `gorm:"size:500" json:"baseline"`
	// TimeoutMs 单个端口的连接超时。公网目标建议放宽到 1500ms 以上
	TimeoutMs    int  `gorm:"default:800" json:"timeoutMs"`
	AlertEnabled bool `json:"alertEnabled"`

	// 以下由扫描回填，不接受手工录入
	LastStatus string `gorm:"size:16;default:unknown" json:"lastStatus"` // unknown | ok | unexpected | failed
	// LastOpen / LastUnexpected / LastMissing 逗号分隔的端口，便于列表直接展示
	LastOpen       string     `gorm:"size:500" json:"lastOpen"`
	LastUnexpected string     `gorm:"size:500" json:"lastUnexpected"`
	LastMissing    string     `gorm:"size:500" json:"lastMissing"`
	LastCostMs     int64      `json:"lastCostMs"`
	LastError      string     `gorm:"size:255" json:"lastError"`
	LastScanAt     *time.Time `json:"lastScanAt"`
	TotalScans     int        `json:"totalScans"`

	Enabled   bool      `json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ExposureScan 一次扫描的结果。留着历史才能回答「这个端口是什么时候开的」
type ExposureScan struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TargetID uint   `gorm:"index;not null" json:"targetId"`
	Status   string `gorm:"size:16" json:"status"` // ok | unexpected | failed
	// Scanned 本次实际扫了多少个端口
	Scanned    int    `json:"scanned"`
	OpenPorts  string `gorm:"size:500" json:"openPorts"`
	Unexpected string `gorm:"size:500" json:"unexpected"`
	Missing    string `gorm:"size:500" json:"missing"`
	CostMs     int64  `json:"costMs"`
	ErrorMsg   string `gorm:"size:255" json:"errorMsg"`
	// Operator 手动扫描记操作人，定时记 scheduler
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// HostMetric 主机性能采样点。
//
// 走已有的 SSH 通道跑一条只读命令采集，不装 agent、不依赖 Prometheus：
// 平台既然已经握着主机凭据，再为了看 CPU 磁盘去铺一套采集体系不划算。
// 代价也说清楚：采样间隔受定时任务控制（默认 5 分钟），不适合看秒级抖动。
type HostMetric struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	HostID uint `gorm:"index;not null" json:"hostId"`

	// CPUPercent 采样瞬间的 CPU 使用率（0-100），由远端两次 /proc/stat 的差值算出
	CPUPercent float64 `json:"cpuPercent"`
	// MemPercent 用 MemAvailable 算，而不是 MemFree —— 后者会把可回收的缓存算成已用
	MemPercent  float64 `json:"memPercent"`
	SwapPercent float64 `json:"swapPercent"`
	// DiskMaxPercent 各本地文件系统里最满那个的使用率，DiskMaxMount 是它的挂载点。
	// 只留最满的一个：磁盘告警关心的是「哪里先满」，全量挂载点留着会把表撑大
	DiskMaxPercent float64 `json:"diskMaxPercent"`
	DiskMaxMount   string  `gorm:"size:128" json:"diskMaxMount"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
	// CPUCores 核数，用来把负载换算成单核负载（load1/cores）后才好跨机型比较
	CPUCores   int   `json:"cpuCores"`
	MemTotalMB int64 `json:"memTotalMB"`
	MemUsedMB  int64 `json:"memUsedMB"`
	ProcCount  int   `json:"procCount"`
	TCPConn    int   `json:"tcpConn"`
	UptimeSec  int64 `json:"uptimeSec"`

	// Status 采集本身是否成功；failed 时上面的数值全为 0，Error 记原因
	Status    string    `gorm:"size:16;default:ok;index" json:"status"` // ok | failed
	Error     string    `gorm:"size:255" json:"error"`
	CostMs    int64     `json:"costMs"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// OnCallSchedule 值班表：回答"这条告警半夜该叫谁"。
//
// 和通知路由的分工：路由决定"发到哪个渠道"（群机器人、邮件组），
// 值班表决定"发给哪个人"，并在没人确认时一级级往上叫。
type OnCallSchedule struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Members 有序的用户 ID，JSON 数组。轮换与升级都按这个顺序走
	Members string `gorm:"type:text" json:"members"`
	// Rotation 轮换周期：hourly | daily | weekly
	Rotation string `gorm:"size:16;default:daily" json:"rotation"`
	// StartAt 轮换基准时间。"现在轮到谁"= 从这个时刻起算过了几个周期。
	// 交接时刻就藏在这个时间的时分里，所以不需要再单独配一个"几点交班"
	StartAt time.Time `json:"startAt"`
	// MatchSeverity 只对这些级别的告警叫人，逗号分隔；空表示全部级别
	MatchSeverity string `gorm:"size:64" json:"matchSeverity"`
	// AckWaitMinutes 告警多久没人确认就升到下一级
	AckWaitMinutes int `gorm:"default:10" json:"ackWaitMinutes"`
	// MaxLevel 最多叫到第几级（1 表示只叫当班的人，不升级）
	MaxLevel int `gorm:"default:3" json:"maxLevel"`
	// NotifyEmail 是否同时发邮件（要求 SMTP 已配置且用户填了邮箱）
	NotifyEmail bool `gorm:"default:false" json:"notifyEmail"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OnCallOverride 代班：某段时间由指定的人顶替当班者。
// 只影响"当班"这一级，升级仍按成员顺序往下走。
type OnCallOverride struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ScheduleID uint      `gorm:"index;not null" json:"scheduleId"`
	UserID     uint      `gorm:"index;not null" json:"userId"`
	StartAt    time.Time `gorm:"index" json:"startAt"`
	EndAt      time.Time `gorm:"index" json:"endAt"`
	Reason     string    `gorm:"size:255" json:"reason"`
	CreatedBy  uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

// AlertEscalation 一次"叫人"的记录：某条告警在第几级通知了谁、走的什么渠道。
//
// 既是去重依据（同一告警同一级别只叫一次），也是事后复盘"到底有没有叫到人"的凭据。
type AlertEscalation struct {
	ID         uint `gorm:"primaryKey" json:"id"`
	AlertID    uint `gorm:"index;not null" json:"alertId"`
	ScheduleID uint `gorm:"index;not null" json:"scheduleId"`
	// Level 0 表示当班，1 及以上是逐级升级
	Level    int    `json:"level"`
	UserID   uint   `gorm:"index" json:"userId"`
	UserName string `gorm:"size:64" json:"userName"`
	// Source rotation | override，说明这一级是按轮换算出来的还是代班顶上的
	Source string `gorm:"size:16" json:"source"`
	// Channel message | email，同一级别两个渠道各记一条
	Channel   string    `gorm:"size:16" json:"channel"`
	Status    string    `gorm:"size:16;default:sent" json:"status"` // sent | failed
	Detail    string    `gorm:"size:255" json:"detail"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Host 主机资产
type Host struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	Address  string `gorm:"size:128;not null" json:"address"`
	Port     int    `gorm:"default:22" json:"port"`
	Username string `gorm:"size:64;not null" json:"username"`
	AuthType string `gorm:"size:16;default:password" json:"authType"` // password | key
	// Secret 登录密码或私钥。配了 OPS_SECRET_KEY 时加密落库（enc:v1: 前缀），不出接口。
	// CredentialID != 0 时这里应为空：凭据统一由凭证库保管，本地不再留一份
	Secret string `gorm:"type:text" json:"-"`
	// CredentialID 引用凭证库里的共享凭据，0 表示用上面这份自带凭据。
	// 非 0 时登录用的用户名 / 认证方式 / 密钥全部取凭据，取不到就直接失败，不回退
	CredentialID uint `gorm:"index;default:0" json:"credentialId"`
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

// Credential 共享登录凭据。一份口令 / 私钥存一处，多台主机引用它。
//
// 与 Host.Secret 的关系：主机 CredentialID != 0 时，登录用的用户名与密钥一律取这里，
// 主机自己那份 Secret 不再参与（切过来时会被清空，免得留一份过期口令当后门）。
// CredentialID = 0 的主机还是用自带凭据，老数据不需要迁移。
type Credential struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Type password | key。与 Host.AuthType 取值一致，直接喂给 sshx
	Type     string `gorm:"size:16;default:password" json:"type"`
	Username string `gorm:"size:64;not null" json:"username"`
	// Secret 口令或私钥 PEM。配了 OPS_SECRET_KEY 时以 AES-GCM 密文存（前缀 enc:v1:），
	// 没配就是明文 —— 界面上会如实标出来，不做「看起来加密了」的暗示
	Secret string `gorm:"type:text" json:"-"`
	// Passphrase 私钥口令。平台此前完全不支持带口令的私钥（ParsePrivateKey 直接报错），
	// 凭证库这条链路补上了
	Passphrase string `gorm:"type:text" json:"-"`
	// Fingerprint 私钥对应公钥的 SHA256 指纹，落库时算一次。
	// 它能回答「这份凭据到底是哪把钥匙」而不泄露私钥，也用于换钥匙后的比对
	Fingerprint string `gorm:"size:128" json:"fingerprint"`
	Description string `gorm:"size:255" json:"description"`
	// Owner 责任人（自由文本，与防火墙规则的 Owner 一致），凭据没人认领是常见的失控来源
	Owner   string `gorm:"size:64" json:"owner"`
	DeptID  uint   `gorm:"index;default:0" json:"deptId"`
	Enabled bool   `gorm:"default:true" json:"enabled"`
	// RotatedAt 最近一次换密钥的时间，为空表示从未轮换
	RotatedAt *time.Time `json:"rotatedAt"`
	// LastUsedAt / LastUsedHostID 最近一次真的用它连上了哪台机器。
	// 只在「测试连接」时更新：普通业务链路每次连接都写库会把这张表写成热点
	LastUsedAt     *time.Time `json:"lastUsedAt"`
	LastUsedHostID uint       `gorm:"default:0" json:"lastUsedHostId"`
	CreatedBy      uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// VaultAccount 账号密码库条目：人要用的那类账号口令。
//
// 和上面的 Credential（凭证库）**刻意分成两张表**，因为安全模型正好相反：
//   - 凭证库是给平台自己用的，明文永不出接口，人也拿不到 —— 所以它不需要取用留痕；
//   - 密码库是给人用的（某个后台系统的管理员账号、某个第三方平台的登录口令），
//     明文必须能取出来，否则这一页只是个不能用的清单。
//
// 明文能取出来，安全性就只能靠「取用必留痕」来兜：每次取明文都写一条
// VaultAccess，取不留痕就不给明文（见 handler.revealVaultAccount）。
type VaultAccount struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Category 分类，仅用于归类展示：system | database | network | thirdparty | other
	Category string `gorm:"size:16;default:other" json:"category"`
	// Platform 这个账号属于哪个系统（如「Jenkins」「阿里云控制台」）
	Platform string `gorm:"size:64" json:"platform"`
	// URL 登录地址，选填
	URL      string `gorm:"size:512" json:"url"`
	Username string `gorm:"size:128;not null" json:"username"`
	// Secret 口令。配了 OPS_SECRET_KEY 时以 AES-GCM 密文存（前缀 enc:v1:）
	Secret      string `gorm:"type:text" json:"-"`
	Description string `gorm:"size:255" json:"description"`
	Owner       string `gorm:"size:64" json:"owner"`
	DeptID      uint   `gorm:"index;default:0" json:"deptId"`
	// Enabled 刻意不加 gorm default:true，见 Domain.AlertEnabled 上的说明
	Enabled bool `json:"enabled"`
	// RotateDays 期望多少天轮换一次口令，0 表示不提醒。
	// 非 0 时逾期会进告警通道（OPS_VAULT_SPEC 控制的固定任务）
	RotateDays int        `gorm:"default:0" json:"rotateDays"`
	RotatedAt  *time.Time `json:"rotatedAt"`
	// LastViewedAt / ViewCount 取用统计。这两个是 VaultAccess 的冗余汇总，
	// 列表页要显示「最近谁看过」，不能每行都去扫留痕表
	LastViewedAt *time.Time `json:"lastViewedAt"`
	LastViewedBy string     `gorm:"size:64" json:"lastViewedBy"`
	ViewCount    int        `gorm:"default:0" json:"viewCount"`
	CreatedBy    uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// VaultTOTP 2FA 验证码库：共享账号的 TOTP 种子托管。
//
// 解决的是「这个账号开了两步验证，验证器绑在离职同事手机上」。种子存平台，
// 谁需要就现算一个码 —— 算码用的是**服务器时间**，服务器时钟偏了码就是错的，
// 所以出码接口一并返回服务器时间，界面上照实显示。
type VaultTOTP struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Issuer / Account 验证器里显示的服务方与账号，重建二维码时要用
	Issuer  string `gorm:"size:64" json:"issuer"`
	Account string `gorm:"size:128" json:"account"`
	// AccountID 关联的密码库条目，0 表示不关联
	AccountID uint `gorm:"index;default:0" json:"accountId"`
	// Secret base32 种子。落库前必须能被 totp.DecodeSecret 解开，
	// 否则等到要用的时候才发现存进来的是一串乱码
	Secret       string     `gorm:"type:text" json:"-"`
	Description  string     `gorm:"size:255" json:"description"`
	Owner        string     `gorm:"size:64" json:"owner"`
	Enabled      bool       `json:"enabled"`
	LastViewedAt *time.Time `json:"lastViewedAt"`
	LastViewedBy string     `gorm:"size:64" json:"lastViewedBy"`
	ViewCount    int        `gorm:"default:0" json:"viewCount"`
	CreatedBy    uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// VaultAccess 取用留痕：谁在什么时候取走了哪一条的明文。
//
// 这张表是密码库能存在的前提，不是附加功能。所以：
//   - 留痕写库失败时接口直接报错，不返回明文；
//   - TargetName 冗余存一份，条目被删掉之后留痕还读得懂；
//   - 只记「取了什么」，绝不记明文本身。
type VaultAccess struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Target account | totp
	Target   string `gorm:"size:16;index:idx_vault_access_target" json:"target"`
	TargetID uint   `gorm:"index:idx_vault_access_target" json:"targetId"`
	// TargetName 取用时条目的名字，条目删了也留着
	TargetName string `gorm:"size:64" json:"targetName"`
	// Action reveal 取口令明文 | code 出验证码 | uri 导出 otpauth 二维码地址 | rotate 轮换口令
	Action     string `gorm:"size:16;index" json:"action"`
	Operator   string `gorm:"size:64;index" json:"operator"`
	OperatorID uint   `gorm:"index;default:0" json:"operatorId"`
	IP         string `gorm:"size:64" json:"ip"`
	// Reason 取用理由，选填。不设必填是因为强制填写只会收到一堆「日常运维」，
	// 留痕本身（谁、什么时候、取了哪一条）才是有用的部分
	Reason    string    `gorm:"size:255" json:"reason"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
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
	StartedAt    time.Time  `gorm:"index" json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt"`
	DurationMs   int64      `json:"durationMs"`

	Commands []SessionCommand `gorm:"foreignKey:SessionID" json:"commands,omitempty"`
}

// SessionCommand 会话中执行的单条命令
type SessionCommand struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	SessionID uint   `gorm:"index;not null" json:"sessionId"`
	Command   string `gorm:"type:text" json:"command"`
	// Risk 建索引：审计最常问的就是「把所有被拦下来的命令拉出来」
	Risk   string `gorm:"size:16;index;default:normal" json:"risk"` // normal | warn | blocked
	RuleID uint   `json:"ruleId"`
	// RuleDesc 命中规则时把规则说明抄一份下来。规则以后被改名或删掉，
	// 历史记录里也还能看出当时是因为什么被拦的
	RuleDesc  string    `gorm:"size:128" json:"ruleDesc"`
	OffsetMs  int64     `json:"offsetMs"` // 相对会话开始的毫秒偏移，便于回放定位
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// ExecJob 批量执行作业
type ExecJob struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:128" json:"name"`
	Command    string     `gorm:"type:text;not null" json:"command"`
	Timeout    int        `gorm:"default:60" json:"timeout"` // 单主机超时秒数
	Status     string     `gorm:"size:16;default:running" json:"status"`
	Source     string     `gorm:"size:16;default:manual" json:"source"` // manual | script | cron
	CronJobID  uint       `gorm:"index;default:0" json:"cronJobId"`
	CreatedBy  uint       `gorm:"index" json:"createdBy"`
	Operator   string     `gorm:"size:64" json:"operator"`
	Total      int        `json:"total"`
	SuccessNum int        `json:"successNum"`
	FailedNum  int        `json:"failedNum"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`

	// 下发闸门的判定结果（见 handler/exec_guard.go）
	RiskStatus    string `gorm:"size:16" json:"riskStatus"` // pass | warn（blocked 不会产生作业）
	RiskHits      string `gorm:"type:text" json:"riskHits"` // 命中的命令规则 JSON
	ProdCount     int    `json:"prodCount"`                 // 目标里的生产主机台数
	ProdConfirmed bool   `json:"prodConfirmed"`             // 下发时是否显式确认过生产变更

	Results []ExecResult `gorm:"foreignKey:JobID" json:"results,omitempty"`
}

// ExecGuardLog 下发闸门流水：被拦下的下发尝试，以及命中提醒级规则的下发。
//
// 放行且没命中任何规则的下发不记这里（exec_jobs 本身就是记录）；
// 被拦下的下发不会产生 exec_jobs，不单独记就彻底查不到。
type ExecGuardLog struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Source    string `gorm:"size:16;index" json:"source"` // manual | script | cron
	Status    string `gorm:"size:16;index" json:"status"` // blocked | warn
	Reason    string `gorm:"size:512" json:"reason"`
	Command   string `gorm:"type:text" json:"command"`
	HostCount int    `json:"hostCount"`
	ProdCount int    `json:"prodCount"`
	HostNames string `gorm:"type:text" json:"hostNames"` // 顿号分隔，超长截断
	RuleID    uint   `json:"ruleId"`
	Pattern   string `gorm:"size:256" json:"pattern"`
	Action    string `gorm:"size:16" json:"action"` // block | warn
	CronJobID uint   `gorm:"index;default:0" json:"cronJobId"`
	UserID    uint   `gorm:"index" json:"userId"`
	Username  string `gorm:"size:64" json:"username"`
	ClientIP  string `gorm:"size:64" json:"clientIp"`

	CreatedAt time.Time `json:"createdAt"`
}

// CronJob 定时任务：按 cron 表达式在一批主机上执行命令
type CronJob struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:128;not null" json:"name"`
	Spec    string `gorm:"size:64;not null" json:"spec"` // 五段 cron 表达式
	Command string `gorm:"type:text;not null" json:"command"`
	HostIDs string `gorm:"type:text" json:"-"` // JSON 数组，接口层用 hostIds 暴露
	Timeout int    `gorm:"default:60" json:"timeout"`
	Enabled bool   `gorm:"default:true" json:"enabled"`
	// ProdConfirmed 保存任务时是否确认过「这个任务会定期往生产主机上下发」。
	// 调度触发时无人在场，就用保存时的这次确认；目标改成含生产主机就要重新确认。
	ProdConfirmed bool       `json:"prodConfirmed"`
	CreatedBy     uint       `gorm:"index" json:"createdBy"`
	Operator      string     `gorm:"size:64" json:"operator"`
	RunCount      int        `json:"runCount"`
	LastStatus    string     `gorm:"size:16" json:"lastStatus"` // success | partial | failed
	LastRunAt     *time.Time `json:"lastRunAt"`
	LastJobID     uint       `json:"lastJobId"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
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
	ID       uint   `gorm:"primaryKey" json:"id"`
	UserID   uint   `gorm:"index" json:"userId"`
	Username string `gorm:"size:64" json:"username"`
	// TokenID / TokenName 非零表示这次调用来自 API 令牌而不是人工登录。
	// 令牌归属人仍记在 UserID 上，所以「谁的令牌干了什么」两头都查得到。
	TokenID   uint      `gorm:"index;default:0" json:"tokenId"`
	TokenName string    `gorm:"size:64" json:"tokenName"`
	Method    string    `gorm:"size:8" json:"method"`
	Path      string    `gorm:"size:255" json:"path"`
	Action    string    `gorm:"size:64" json:"action"`
	Status    int       `json:"status"`
	IP        string    `gorm:"size:64" json:"ip"`
	CostMs    int64     `json:"costMs"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// ApiToken 服务账号令牌：给 CI、脚本、外部系统调平台接口用。
//
// 设计取舍（详见 handler/api_token.go 与 docs/SECURITY.md 第 30 节）：
//   - 明文只在创建/轮换时返回一次，库里只存 SHA-256 哈希，平台自己也查不回来；
//   - 令牌权限 = 显式授予的权限码 ∩ 归属人当下的权限，归属人被降权/停用，令牌同时失效；
//   - 默认只读（只放 GET/HEAD），要写必须显式关掉只读并授权限码；
//   - 不能用于 Web 终端等 WebSocket 链路：那条路必须是人，会话审计才有意义。
type ApiToken struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Prefix 令牌前 12 位（含 opst_ 前缀），用于界面展示与快速定位，不足以还原令牌
	Prefix string `gorm:"size:24;index" json:"prefix"`
	// TokenHash SHA-256(明文)，只用于比对
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// OwnerUserID 归属人。令牌以这个人的身份访问，数据范围也按他算。
	OwnerUserID uint   `gorm:"index;not null" json:"ownerUserId"`
	OwnerName   string `gorm:"size:64" json:"ownerName"`
	// Scopes 显式授予的权限码，JSON 数组；只读令牌可以为空
	Scopes string `gorm:"type:text" json:"scopes"`
	// ReadOnly 只允许 GET/HEAD
	ReadOnly bool `json:"readOnly"`
	// AllowIPs 允许的来源，逗号分隔，支持 CIDR；空表示不限制
	AllowIPs  string     `gorm:"size:255" json:"allowIps"`
	ExpiresAt *time.Time `json:"expiresAt"`
	Enabled   bool       `json:"enabled"`

	LastUsedAt *time.Time `json:"lastUsedAt"`
	LastUsedIP string     `gorm:"size:64" json:"lastUsedIp"`
	UseCount   int64      `gorm:"default:0" json:"useCount"`

	RevokedAt *time.Time `json:"revokedAt"`
	RevokedBy string     `gorm:"size:64" json:"revokedBy"`
	Remark    string     `gorm:"size:255" json:"remark"`
	CreatedBy string     `gorm:"size:64" json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// FirewallRule 平台侧登记的一条防火墙规则，即「我们期望这台机器上有这条规则」。
//
// 它不是真机状态的镜像：真机状态每次都是现读（见 internal/fwx），
// 两者对账出来的差异写在 State 上。这样「谁加的、为什么加、什么时候该收」
// 这些真机里根本不存在的信息才有地方放。
//
// Origin=discovered 的规则是从真机读回来、平台之前不知道的存量规则，
// 补齐责任人和到期时间之后才算被平台接管。
type FirewallRule struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	HostID uint `gorm:"index;not null" json:"hostId"`
	// GroupID 属于哪个安全组；0 表示是这台机器的独立规则
	GroupID uint `gorm:"index;default:0" json:"groupId"`

	Direction string `gorm:"size:8;not null" json:"direction"` // in | out
	Action    string `gorm:"size:8;not null" json:"action"`    // accept | drop | reject
	Protocol  string `gorm:"size:8" json:"protocol"`           // tcp | udp | icmp | all
	Source    string `gorm:"size:64" json:"source"`            // IP 或 CIDR，any 表示不限
	Port      string `gorm:"size:32" json:"port"`              // 22 或 6000-6010
	Service   string `gorm:"size:32" json:"service"`           // firewalld 服务名，与 Port 二选一
	// RuleKey fwx.Rule.Key() 的结果，对账靠它，落库时算好
	RuleKey string `gorm:"size:128;index" json:"ruleKey"`

	Description string `gorm:"size:255" json:"description"`
	// Owner 责任人。没有责任人的放行规则是运维债，清理建议会点出来
	Owner string `gorm:"size:64" json:"owner"`
	// Lifecycle permanent 长期有效 | temporary 临时，必须有到期时间
	Lifecycle string     `gorm:"size:16;default:permanent" json:"lifecycle"`
	ExpiresAt *time.Time `json:"expiresAt"`

	// Origin platform 平台创建 | discovered 从真机读回来的存量规则
	Origin string `gorm:"size:16;default:platform" json:"origin"`
	// State pending 待下发 | synced 已生效 | drift 真机上没有 | extra 真机有平台没登记
	State   string `gorm:"size:16;default:pending" json:"state"`
	Enabled bool   `gorm:"default:true" json:"enabled"`

	// Hits / LastCheckAt 来自最近一次读取；HasCounter=false 说明这台机器读不到计数，
	// 界面必须区分「命中 0 次」和「没有计数器」
	Hits        int64      `gorm:"default:0" json:"hits"`
	HasCounter  bool       `gorm:"default:false" json:"hasCounter"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastHitAt   *time.Time `json:"lastHitAt"`

	CreatedBy string    `gorm:"size:64" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// FirewallGroup 安全组：一组规则 + 一批成员主机，下发时按组铺到成员上。
// 用来表达「所有 Web 机都放行 80/443」这类事实，避免一台台重复登记。
type FirewallGroup struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Description string `gorm:"size:255" json:"description"`
	// MemberHostIDs 主机 ID 列表，JSON 数组字符串（与 CronJob.HostIDs 同格式，都用 parseHostIDs 读）
	MemberHostIDs string    `gorm:"size:512" json:"memberHostIds"`
	Enabled       bool      `gorm:"default:true" json:"enabled"`
	CreatedBy     string    `gorm:"size:64" json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// FirewallSnapshot 下发前后的真机规则原样快照，用来回滚和查「这条规则是哪次下发加的」。
//
// 存的是读命令的原始输出而不是解析结果：解析器以后会改，原始输出不会，
// 回滚时要能拿到当时真机到底长什么样。
type FirewallSnapshot struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	HostID  uint   `gorm:"index;not null" json:"hostId"`
	Backend string `gorm:"size:16" json:"backend"`
	// Reason before-apply | after-apply | manual
	Reason    string `gorm:"size:32" json:"reason"`
	RuleCount int    `json:"ruleCount"`
	// Raw 读命令的原始输出
	Raw string `gorm:"type:text" json:"raw"`
	// Rules 解析后的规则 JSON，回滚时按它生成命令
	Rules     string    `gorm:"type:text" json:"rules"`
	ExecJobID uint      `gorm:"index;default:0" json:"execJobId"`
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `json:"createdAt"`
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
	// SuppressedBy 命中聚合策略而未发通知时记下策略名，用来解释「为什么没收到通知」
	SuppressedBy string `gorm:"size:64" json:"suppressedBy"`
	// SilencedBy / SilenceID 命中静默或维护窗口而未发通知时记下是哪一条。
	// 与 SuppressedBy 分开存：聚合抑制用 SuppressedBy=="" 判断「首条是否已通知」，
	// 静默要是复用同一个字段，会把被静默的告警当成「已通知过的首条」，破坏聚合语义。
	SilencedBy string `gorm:"size:64" json:"silencedBy"`
	SilenceID  uint   `gorm:"index;default:0" json:"silenceId"`
}

// AlertSilence 告警静默 / 维护窗口。
//
// 语义：只拦「外发通知」，告警照常入库并标记是谁拦的 —— 维护窗口期间平台该看见的
// 还是要看见，只是不半夜叫人。窗口结束后新产生的告警自动恢复外发，不需要手工解除。
type AlertSilence struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Kind silence（临时静默）| maintenance（维护窗口）。只影响展示与筛选，判定逻辑相同。
	Kind string `gorm:"size:16;default:silence" json:"kind"`

	// ---------- 匹配条件（都为空且 MatchAll=false 时不允许保存）----------

	// MatchSeverity 逗号分隔的级别，空表示不限
	MatchSeverity string `gorm:"size:64" json:"matchSeverity"`
	// MatchLabels JSON 对象，要求告警标签逐项相等（与通知路由同语义）
	MatchLabels string `gorm:"type:text" json:"matchLabels"`
	// MatchTitle 标题包含该关键字（区分大小写按原文比较）
	MatchTitle string `gorm:"size:128" json:"matchTitle"`
	// MatchSource 逗号分隔的告警来源名，空表示不限
	MatchSource string `gorm:"size:255" json:"matchSource"`
	// MatchAll 显式勾选「匹配全部告警」，防止手滑漏填条件造成全局静音
	MatchAll bool `gorm:"default:false" json:"matchAll"`

	// ---------- 时间窗 ----------

	StartAt time.Time `gorm:"index" json:"startAt"`
	EndAt   time.Time `gorm:"index" json:"endAt"`

	Reason  string `gorm:"size:255" json:"reason"`
	Enabled bool   `gorm:"default:true" json:"enabled"`

	// ---------- 运行痕迹 ----------

	// HitCount 拦下过多少条告警的通知；LastHitAt 最近一次拦下的时间
	HitCount  int        `gorm:"default:0" json:"hitCount"`
	LastHitAt *time.Time `json:"lastHitAt"`
	// EndedAt / EndedBy 提前结束（维护提前完成时用），提前结束后立即失效
	EndedAt *time.Time `json:"endedAt"`
	EndedBy string     `gorm:"size:64" json:"endedBy"`

	CreatedBy   uint      `gorm:"index;default:0" json:"createdBy"`
	CreatorName string    `gorm:"size:64" json:"creatorName"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// DBQueryLog 数据库只读查询的执行流水。
//
// 为什么必须有：能查库就等于能看到业务数据本身，比看日志敏感得多。
// 被拦下的语句也要留痕（status=blocked），否则「谁试过跑 delete」这种事查不到。
type DBQueryLog struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	InstanceID   uint   `gorm:"index" json:"instanceId"`
	InstanceName string `gorm:"size:64" json:"instanceName"`
	DBType       string `gorm:"size:16" json:"dbType"`
	Schema       string `gorm:"size:64" json:"schema"`
	Statement    string `gorm:"type:text" json:"statement"`
	// Status success（执行成功）| blocked（被守卫拦下，没有下发到库）| failed（下发了但报错）
	Status   string `gorm:"size:16;index" json:"status"`
	Reason   string `gorm:"size:255" json:"reason"` // 拦截原因或数据库返回的错误
	Rows     int    `gorm:"default:0" json:"rows"`
	CostMs   int64  `gorm:"default:0" json:"costMs"`
	Exported bool   `gorm:"default:false" json:"exported"` // 是不是导出操作（数据外带）

	UserID    uint      `gorm:"index;default:0" json:"userId"`
	Username  string    `gorm:"size:64;index" json:"username"`
	ClientIP  string    `gorm:"size:64" json:"clientIp"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// AggregationPolicy 告警聚合策略：把同类告警按维度归到一个桶，
// 对付「一台机器挂了刷出一堆告警」这类噪音。//
// 归桶是实时计算的，不改动告警本身；只有显式打开「抑制通知」才会影响投递，
// 被抑制的告警仍然入库、页面可见，并在告警上记下是哪条策略抑制的。
type AggregationPolicy struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Dimensions 归桶维度，逗号分隔。取值：source | severity | title | label:<键名>
	Dimensions string `gorm:"size:255;not null" json:"dimensions"`
	// MatchSeverity 只处理这些级别，逗号分隔，空表示不限
	MatchSeverity string `gorm:"size:64" json:"matchSeverity"`
	// WindowMinutes 只把窗口内出现过的告警纳入同一个桶
	WindowMinutes int `gorm:"default:60" json:"windowMinutes"`
	// MinCount 桶内告警数达到该值才算「成桶」，低于此值按单条看
	MinCount int `gorm:"default:2" json:"minCount"`
	// SuppressNotify 同一桶内窗口期只通知首条，其余仅入库
	SuppressNotify bool `gorm:"default:false" json:"suppressNotify"`
	// Priority 越小越先匹配，一条告警只会被第一条命中的策略处理
	Priority  int       `gorm:"default:100" json:"priority"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NotifyChannel 通知渠道。webhook 走 HTTP POST，email 走 SMTP，silent 只落记录不外发。
type NotifyChannel struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:64;not null" json:"name"`
	Type        string `gorm:"size:16;default:webhook" json:"type"` // webhook | email | silent
	URL         string `gorm:"size:512" json:"url"`
	HeaderKey   string `gorm:"size:64" json:"headerKey"` // 可选的鉴权头名
	HeaderValue string `gorm:"size:255" json:"-"`        // 鉴权头值，加密落库，不出接口
	// Recipients / TemplateCode 仅 email 类型使用
	Recipients   string `gorm:"size:512" json:"recipients"`  // 逗号分隔收件人
	TemplateCode string `gorm:"size:64" json:"templateCode"` // 邮件模板编码，空则用内置格式
	// Secret 群机器人的签名密钥：钉钉「加签」、飞书「签名校验」用，不出接口。
	// 企业微信没有这个概念（密钥就在 URL 里）
	Secret string `gorm:"size:255" json:"-"`
	// MentionList @ 名单，逗号分隔。企业微信填 userid，钉钉填手机号；
	// 飞书只支持 @所有人（@ 单人要 open_id，平台不做通讯录同步）
	MentionList string `gorm:"size:255" json:"mentionList"`
	MentionAll  bool   `gorm:"default:false" json:"mentionAll"`

	// MailAccountID 用哪个发件邮箱发（仅 email 类型）。0 表示用默认发件邮箱，
	// 一个都没登记时回落到系统配置里那套全局 SMTP
	MailAccountID uint `gorm:"index;default:0" json:"mailAccountId"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MailAccount 发件邮箱。
//
// 在这之前全平台只有一套 SMTP（系统配置里的 6 个键），所有邮件都从同一个地址发出去。
// 一套不够用的场景很具体：告警想从 alert@ 发、值班呼叫想从 oncall@ 发，
// 或者不同部门用各自的邮箱以便收件人做规则过滤。
//
// 全局那套配置**没有被废弃**：没登记任何发件邮箱时仍然走它，这样既有部署不用动。
type MailAccount struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Host string `gorm:"size:128;not null" json:"host"`
	Port int    `gorm:"default:465" json:"port"`
	// Username SMTP 认证账号，留空表示不认证（内网中继常见）
	Username string `gorm:"size:128" json:"username"`
	// Password 加密落库，不出接口；更新时留空表示不修改
	Password string `gorm:"type:text" json:"-"`
	// From 发件人地址，留空则用 Username
	From string `gorm:"size:128" json:"from"`
	// FromName 发件人显示名。之前的实现只能发裸地址，收件箱里看到的是一串邮箱
	FromName string `gorm:"size:64" json:"fromName"`
	// TLSMode ssl（465 直连 TLS）| starttls（587/25 先明文再升级）| plain（完全不加密）。
	// 之前只有一个 smtp.tls 布尔开关，starttls 靠标准库「服务端 advertise 就升级」
	// 的默认行为被动发生 —— 那意味着中继不 advertise 时会**静默降级成明文**，
	// 而配置上看不出来。显式三选一之后 starttls 失败就是失败
	TLSMode string `gorm:"size:16;default:ssl" json:"tlsMode"`
	// SkipVerify 跳过证书校验。内网自签证书的 SMTP 之前根本连不上，
	// 但这是一次真实的降级，界面上必须标出来
	SkipVerify bool `gorm:"default:false" json:"skipVerify"`
	// IsDefault 默认发件邮箱，全局最多一个。渠道没指定邮箱时用它
	IsDefault bool `gorm:"index;default:false" json:"isDefault"`
	// Enabled 刻意不加 gorm default:true，见 Domain.AlertEnabled 上的说明
	Enabled bool   `json:"enabled"`
	Remark  string `gorm:"size:255" json:"remark"`

	// ---------- 试发痕迹 ----------

	LastTestAt *time.Time `json:"lastTestAt"`
	// LastTestOK 最近一次试发是否成功。三态：nil 从没试过 / true / false
	LastTestOK  *bool  `json:"lastTestOk"`
	LastTestErr string `gorm:"size:255" json:"lastTestErr"`
	LastTestTo  string `gorm:"size:128" json:"lastTestTo"`

	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EgressProxy 出口代理。
//
// 登记它有两个用处，缺一个这一页就是装饰：
//  1. **检测**：真的通过它发一次请求，把「直连通不通」和「走代理通不通」摆在一起对比 ——
//     这是判断「是网络不通还是代理坏了」唯一靠得住的方式；
//  2. **被用**：HTTP 拨测可以指定走某个代理。除此之外平台其它出网点
//     （云 API、IM、Webhook、指标 / 日志 / 链路数据源）**目前都不走代理**，
//     这一点写在页面上，不让人误以为登记了就全局生效。
type EgressProxy struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;uniqueIndex;not null" json:"name"`
	// Scheme http | https | socks5。三者都由标准库 http.Transport.Proxy 直接支持
	Scheme string `gorm:"size:16;default:http" json:"scheme"`
	Host   string `gorm:"size:128;not null" json:"host"`
	Port   int    `gorm:"default:3128" json:"port"`
	// Username / Password 代理认证，留空表示不认证。Password 加密落库
	Username string `gorm:"size:64" json:"username"`
	Password string `gorm:"type:text" json:"-"`
	// TestURL 检测时请求的地址，留空用全局默认（配置项 proxy.test_url）
	TestURL string `gorm:"size:255" json:"testUrl"`
	// Enabled 刻意不加 gorm default:true
	Enabled bool   `json:"enabled"`
	Remark  string `gorm:"size:255" json:"remark"`

	// ---------- 检测痕迹 ----------

	LastCheckAt *time.Time `json:"lastCheckAt"`
	// LastStatus unknown | ok | fail
	LastStatus string `gorm:"size:16;default:unknown" json:"lastStatus"`
	LastCostMs int64  `json:"lastCostMs"`
	LastError  string `gorm:"size:255" json:"lastError"`
	// ExitIP 检测时对端看到的出口 IP。只有测试地址会回显 IP 时才有值，
	// 否则显示「测试地址不回显 IP，无法判断出口」而不是留空让人猜
	ExitIP string `gorm:"size:64" json:"exitIp"`

	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

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

// KubeForward 一条到集群内服务的转发隧道。
//
// 隧道是进程内的活物（一个 TCP 监听 + 若干条到 API Server 的 WebSocket），
// 这张表只是它的档案：谁开的、转发到哪、用了多少、什么时候关的。
// 进程重启后监听全没了，启动时会把残留的 running 行改成 stopped —— 不假装还活着。
type KubeForward struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ClusterID   uint   `gorm:"index" json:"clusterId"`
	ClusterName string `gorm:"size:64" json:"clusterName"`
	Namespace   string `gorm:"size:128" json:"namespace"`
	// TargetKind pod | service。service 每来一条连接重新解析后端 Pod，
	// Pod 重建、扩缩容之后隧道还能继续用
	TargetKind string `gorm:"size:16" json:"targetKind"`
	TargetName string `gorm:"size:191" json:"targetName"`
	TargetPort int    `json:"targetPort"`
	ListenAddr string `gorm:"size:64" json:"listenAddr"`
	ListenPort int    `gorm:"index" json:"listenPort"`
	// ForwardStatus running | stopped | error
	ForwardStatus string `gorm:"size:16;index" json:"status"`
	ErrorMsg      string `gorm:"size:500" json:"errorMsg"`
	// ConnTotal 累计接入的连接数；ConnFailed 拨号失败的次数
	ConnTotal  int64 `json:"connTotal"`
	ConnFailed int64 `json:"connFailed"`
	// BytesIn 从本地流向 Pod 的字节数，BytesOut 反向
	BytesIn  int64 `json:"bytesIn"`
	BytesOut int64 `json:"bytesOut"`
	UserID   uint  `gorm:"index;default:0" json:"userId"`
	// Username 落冗余名字，开隧道的人改名或删号之后档案仍然可读
	Username string `gorm:"size:64" json:"username"`
	ClientIP string `gorm:"size:64" json:"clientIp"`
	// ExpiresAt 到点自动关闭，避免有人开完忘了关
	ExpiresAt    time.Time  `gorm:"index" json:"expiresAt"`
	LastActiveAt *time.Time `json:"lastActiveAt"`
	ClosedAt     *time.Time `json:"closedAt"`
	CreatedAt    time.Time  `gorm:"index" json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
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

// DBInstance 数据库资产。凭据与主机一致：配了 OPS_SECRET_KEY 时加密落库（见 docs/SECURITY.md）。
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
// 密钥的用途：阿里云账号的 AK/SK 会被「云资源同步」用来调只读的 OpenAPI
// （ECS DescribeInstances / 云解析 DescribeDomains），拉回来的资源进
// CloudResource 表并与主机资产做漂移对照。平台不用这份密钥做任何写操作。
// 腾讯云 / 华为云 / AWS 目前只登记不同步 —— 各家签名方式不同，逐家接。
type CloudAccount struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:64;not null" json:"name"`
	Provider        string    `gorm:"size:16;default:aliyun" json:"provider"` // aliyun | tencent | huawei | aws | other
	AccessKeyID     string    `gorm:"size:128" json:"accessKeyId"`
	AccessKeySecret string    `gorm:"type:text" json:"-"` // 配了密钥时加密落库，不出接口
	Region          string    `gorm:"size:64" json:"region"`
	AccountID       string    `gorm:"size:64" json:"accountId"` // 云上主账号 ID，便于对账
	DeptID          uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy       uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled         bool      `gorm:"default:true" json:"enabled"`
	Remark          string    `gorm:"size:255" json:"remark"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// CloudResource 从云上同步回来的资源清单。
//
// 这张表是「云上事实」的镜像，不是台账：同步时按 (账号, 类型, 资源 ID) 覆盖写，
// 云上已经查不到的记录标 Gone 而不是直接删 —— 机器被谁释放了、什么时候消失的，
// 是对账时最需要的那条线索。
//
// 与 Host 的关系：MatchedHostID 是同步时按 IP 自动匹配出来的，只说明「看起来是同一台」，
// 不代表纳管关系。真要纳管得在页面上点「纳管为主机」，那一步需要人补登录凭据。
type CloudResource struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// CloudAccountID 来源云账号
	CloudAccountID uint   `gorm:"index;not null" json:"cloudAccountId"`
	Provider       string `gorm:"size:16;index" json:"provider"`
	// ResourceType ecs（云服务器）| domain（云解析托管域名）
	ResourceType string `gorm:"size:16;index" json:"resourceType"`
	// ResourceID 云上唯一标识：ECS 是 InstanceId，域名是 DomainName
	ResourceID string `gorm:"size:128;index" json:"resourceId"`
	Name       string `gorm:"size:128" json:"name"`
	RegionID   string `gorm:"size:64;index" json:"regionId"`
	ZoneID     string `gorm:"size:64" json:"zoneId"`
	// Status 云上状态原文（Running / Stopped …）；域名固定为 hosted
	Status string `gorm:"size:32;index" json:"status"`
	// Gone 云上已经查不到这条资源了。为真时 Status 保留最后一次看到的值
	Gone       bool   `gorm:"index;default:false" json:"gone"`
	PrivateIPs string `gorm:"size:255" json:"privateIps"` // 逗号分隔
	PublicIPs  string `gorm:"size:255" json:"publicIps"`
	Spec       string `gorm:"size:128" json:"spec"` // 实例规格 / 域名版本
	OSName     string `gorm:"size:128" json:"osName"`
	ChargeType string `gorm:"size:32" json:"chargeType"` // PrePaid | PostPaid
	// ExpiredAt 包年包月到期时间。按量付费与域名为空 ——
	// 域名的**注册**到期时间不在云解析接口里，见 cloudapi.DNSDomain 的说明
	ExpiredAt *time.Time `json:"expiredAt"`
	// MatchedHostID 按 IP 自动匹配到的纳管主机，0 表示没匹配上
	MatchedHostID uint   `gorm:"index;default:0" json:"matchedHostId"`
	MatchBy       string `gorm:"size:16" json:"matchBy"` // private_ip | public_ip
	// Extra 云端原始字段的精简 JSON，供排查用
	Extra       string    `gorm:"type:text" json:"extra"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSyncAt  time.Time `json:"lastSyncAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CloudSyncRun 一次云资源同步的记录。
//
// 失败也要落一条：「上次同步是什么时候、为什么没成」比清单本身更常被问到。
// 清单页上的数字如果来自一次三天前失败后的残留，没有这张表根本看不出来。
type CloudSyncRun struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	CloudAccountID uint   `gorm:"index;not null" json:"cloudAccountId"`
	AccountName    string `gorm:"size:64" json:"accountName"`
	Provider       string `gorm:"size:16" json:"provider"`
	ResourceType   string `gorm:"size:16;index" json:"resourceType"`
	RegionID       string `gorm:"size:64" json:"regionId"`
	// Status success | failed
	Status string `gorm:"size:16;index" json:"status"`
	// Trigger manual（页面点的）| cron（定时任务）
	Trigger      string `gorm:"size:16" json:"trigger"`
	Operator     string `gorm:"size:64" json:"operator"`
	TotalCount   int    `json:"totalCount"`
	CreatedCount int    `json:"createdCount"`
	UpdatedCount int    `json:"updatedCount"`
	GoneCount    int    `json:"goneCount"`
	MatchedCount int    `json:"matchedCount"`
	// Message 失败原因或成功摘要，照实记云端返回的 Code
	Message    string     `gorm:"size:512" json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	DurationMS int64      `json:"durationMs"`
}

// Domain 域名台账 + DNS 解析核对。
//
// 这张表分成三块，边界要清楚：
//  1. **人填的台账**：注册商、注册/到期日、责任人、用途。注册到期日**只能人填** ——
//     云解析接口是「DNS 托管」视角，给不出注册到期时间（见 cloudapi.DNSDomain），
//     所以这里没有「自动同步到期日」这回事，界面上会标出哪些域名还没填。
//  2. **人填的期望值**：期望解析到的地址 / CNAME / NS。留空表示**不核对那一类** ——
//     不填就判漂移等于每条域名一上来都是红的，那种告警没人会看。
//  3. **巡检回填的实际值**：真去查一次 DNS 得到的结果，以及与期望的差异说明。
//
// 与 Certificate 的关系：按证书的 SAN（`Certificate.DNSNames`）自动反查，
// 命中数与最早到期天数冗余在这里，方便在一个列表里同时看见「域名快到期」与「证书快到期」。
// 这个关联是算出来的，不落外键 —— 证书增删改后下一次巡检自然会更新。
type Domain struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name 域名本身，统一小写、不带末尾点与协议
	Name string `gorm:"size:128;not null;uniqueIndex" json:"name"`

	// ---------- 台账（人填） ----------
	Registrar    string     `gorm:"size:64" json:"registrar"` // 注册商：阿里云 / 腾讯云 / GoDaddy…
	RegisteredAt *time.Time `json:"registeredAt"`
	// ExpiresAt 注册到期日。为空表示还没登记 —— 界面上单独标出来催人补，
	// 不拿「空」当「还很久」
	ExpiresAt *time.Time `json:"expiresAt"`
	AutoRenew bool       `gorm:"default:false" json:"autoRenew"`
	Owner     string     `gorm:"size:64" json:"owner"` // 责任人
	// Purpose 用途说明。字段名不叫 Usage：USAGE 是 MySQL 保留字，
	// 拼在 WHERE 里会因为没加反引号直接报语法错
	Purpose   string `gorm:"size:128" json:"purpose"`
	DeptID    uint   `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint   `gorm:"index;default:0" json:"createdBy"`

	// ---------- DNS 期望值（人填，留空=不核对该类） ----------
	ExpectIPs   string `gorm:"size:255" json:"expectIps"` // 逗号分隔
	ExpectCNAME string `gorm:"size:128" json:"expectCname"`
	ExpectNS    string `gorm:"size:255" json:"expectNs"`

	// ---------- DNS 实际值（巡检回填） ----------
	ResolvedIPs   string `gorm:"size:255" json:"resolvedIps"`
	ResolvedCNAME string `gorm:"size:128" json:"resolvedCname"`
	ResolvedNS    string `gorm:"size:255" json:"resolvedNs"`
	// DNSStatus unknown（没查过）| ok | drift（与期望不符）| unresolved（查不到地址）
	//	| nocheck（三类期望都没填，只记录了实际值）| error
	DNSStatus string `gorm:"size:16;default:unknown;index" json:"dnsStatus"`
	// DNSDetail 差异或错误的人话说明，直接展示
	DNSDetail string `gorm:"size:512" json:"dnsDetail"`
	// DNSServer 这次核对实际用的 DNS 服务器，空串表示系统 resolver。
	// 回显它是为了避免「为什么和我 dig 的不一样」变成查不清的问题
	DNSServer   string     `gorm:"size:64" json:"dnsServer"`
	LastCheckAt *time.Time `json:"lastCheckAt"`

	// ---------- 到期状态（巡检回填） ----------
	// DaysLeft 距注册到期的天数；ExpiresAt 为空时无意义，看 ExpireStatus
	DaysLeft int `json:"daysLeft"`
	// ExpireStatus unknown（没填到期日）| valid | expiring | expired
	ExpireStatus string `gorm:"size:16;default:unknown;index" json:"expireStatus"`
	AlertDays    int    `gorm:"default:30" json:"alertDays"`
	// AlertEnabled 是否把巡检结果送进告警通道。
	//
	// 刻意**不加** gorm `default:true`：带 default 标签的布尔字段，GORM 会把
	// 零值（false）从 INSERT 里省掉让数据库填默认值 —— 于是用户在表单里关掉开关，
	// 落库之后又被翻回「开」，而界面上完全看不出来。默认值由 handler 显式给。
	AlertEnabled bool `json:"alertEnabled"`

	// ---------- 关联证书（按 SAN 算出来的，不落外键） ----------
	CertCount int `json:"certCount"`
	// CertNames 命中的证书名，逗号分隔，只为列表页少查一次
	CertNames string `gorm:"size:255" json:"certNames"`
	// CertMinDaysLeft 关联证书里最早到期的剩余天数；CertCount = 0 时为 0
	CertMinDaysLeft int `json:"certMinDaysLeft"`

	// ---------- 来源 ----------
	// Source manual（人工登记）| cloud（从云资源同步的托管域名导进来的）
	Source string `gorm:"size:16;default:manual" json:"source"`
	// CloudResourceID Source=cloud 时指向 cloud_resources 里那一行
	CloudResourceID uint `gorm:"index;default:0" json:"cloudResourceId"`

	// Enabled 停用后不参与批量巡检。与 AlertEnabled 同理不加 gorm default
	Enabled   bool      `json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// HostLogTarget 登记「哪台主机上的哪个日志文件要盯着」。
//
// 与 LogSource（Loki）的分工：LogSource 是查询入口，平台不存日志；
// 这张表走的是另一条路 —— **直接 SSH 读主机上的日志文件**，不依赖任何日志系统。
// 没有部署 Loki 的环境里，这是唯一能看到日志的路径。
//
// 平台**不复制日志内容**：只存「命中了几条」与命中的第一行样本（截断）。
// 把日志搬进平台的库既贵又没人查，而且会让日志的保留策略变成两处维护。
type HostLogTarget struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	Name   string `gorm:"size:64;not null" json:"name"`
	HostID uint   `gorm:"index;not null" json:"hostId"`
	// HostName 冗余一份主机名，列表页少一次 join；主机改名后由巡检回填
	HostName string `gorm:"size:64" json:"hostName"`
	// Path 日志文件绝对路径。必须落在允许的目录前缀下（OPS_LOG_PATH_PREFIXES），
	// 这是这个模块唯一真正的安全边界 —— 路径来自人输入
	Path string `gorm:"size:255;not null" json:"path"`

	// ---------- 关键字规则（人填） ----------
	// Keywords 命中即算异常的关键字，逗号分隔，**固定字符串、不区分大小写**。
	// 刻意不做正则：让人在界面上填正则，迟早会有一条灾难性回溯把巡检卡死
	Keywords string `gorm:"size:255" json:"keywords"`
	// IgnoreKeywords 命中这些的行直接跳过，用来压掉已知噪音。先判 ignore 再判 keyword
	IgnoreKeywords string `gorm:"size:255" json:"ignoreKeywords"`

	// ---------- 采集参数 ----------
	// MaxBytes 单次最多从文件里读多少字节的新增内容，默认 256KB。
	// 这个上限是必需的：sshx.Run 对输出没有大小限制，一个突然暴涨的日志能把服务端内存吃掉
	MaxBytes     int  `gorm:"default:262144" json:"maxBytes"`
	AlertEnabled bool `json:"alertEnabled"` // 不加 gorm default，见 Domain.AlertEnabled 的说明

	// ---------- 增量水位（巡检回填） ----------
	// LastOffset 上次读到的字节位置，下次从这里往后读
	LastOffset int64 `json:"lastOffset"`
	// LastInode 上次看到的 inode。inode 变了说明文件被轮转/重建过，水位要归零 ——
	// 只比大小判不出「轮转后的新文件恰好更大」这种情况
	LastInode string `gorm:"size:32" json:"lastInode"`
	// LastSize 上次看到的文件大小，供界面展示与判断被 truncate
	LastSize int64 `json:"lastSize"`

	// ---------- 结果（巡检回填，不接受手工录入） ----------
	// LastStatus unknown（没巡检过）| ok | hit（命中关键字）| missing（文件不在）
	//	| denied（读不了，通常是权限）| failed（SSH 或命令失败）
	LastStatus string `gorm:"size:16;default:unknown;index" json:"lastStatus"`
	// LastHitCount 上一轮在新增内容里命中了多少行
	LastHitCount int `json:"lastHitCount"`
	// LastSample 命中的第一行原文（截断）。只留一行：留多了这张表就变成日志副本了
	LastSample  string     `gorm:"size:512" json:"lastSample"`
	LastRotated bool       `json:"lastRotated"`
	LastCostMs  int64      `json:"lastCostMs"`
	LastError   string     `gorm:"size:255" json:"lastError"`
	LastScanAt  *time.Time `json:"lastScanAt"`
	TotalScans  int        `json:"totalScans"`

	DeptID    uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled   bool      `json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// HostLogScan 一次日志巡检的历史行。与 ExposureScan 同一性质：
// 目标表只留最后一次状态，要回答「什么时候开始报的」得靠这张流水。
type HostLogScan struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	TargetID uint   `gorm:"index;not null" json:"targetId"`
	HostID   uint   `gorm:"index" json:"hostId"`
	Status   string `gorm:"size:16;index" json:"status"`
	HitCount int    `json:"hitCount"`
	// NewBytes 这一轮读到的新增字节数。0 且 status=ok 表示「这段时间没有新日志」
	NewBytes int64  `json:"newBytes"`
	FileSize int64  `json:"fileSize"`
	Rotated  bool   `json:"rotated"`
	Sample   string `gorm:"size:512" json:"sample"`
	CostMs   int64  `json:"costMs"`
	// ErrorMsg 失败原因，或本轮的说明（比如首次巡检只对齐水位）
	ErrorMsg string `gorm:"size:255" json:"errorMsg"`
	// Operator 手动巡检记用户名，定时记 scheduler
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// HostLogUsage 一台主机上日志目录的占用快照。
//
// 存在的理由：磁盘被日志写满是最常见的一类故障，而它在「主机指标」里只表现为
// 根分区使用率上升，看不出是谁写的。这张表回答「哪台机器的日志目录多大、
// 里面最大的几个文件是谁」。
type HostLogUsage struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	HostID   uint   `gorm:"index;not null" json:"hostId"`
	HostName string `gorm:"size:64" json:"hostName"`
	Dir      string `gorm:"size:128" json:"dir"`
	// TotalKB 目录总占用（du -sk）
	TotalKB int64 `json:"totalKb"`
	// TopFiles 占用最大的若干条目，JSON 数组 [{"path":..,"sizeKb":..}]
	TopFiles string `gorm:"type:text" json:"topFiles"`
	// TopIsDir 为真表示本机的 find 不支持 -printf，退回 du -a，
	// TopFiles 里**混有目录**。如实标注，不假装是纯文件清单
	TopIsDir bool   `json:"topIsDir"`
	Status   string `gorm:"size:16;index" json:"status"` // ok | failed
	ErrorMsg string `gorm:"size:255" json:"errorMsg"`
	CostMs   int64  `json:"costMs"`

	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// RuleVersion 告警规则（以后也可以是检测规则）的配置快照。**只追加不修改** ——
// 它是回滚与「误删恢复」的唯一依据。
//
// 与 ConfigVersion 的差别值得写清楚：配置文件有「机器上的现状」这个第三方，
// 所以那边要在下发前专门存一版 pre-apply 当回滚点。告警规则的「生效态」就是
// 数据库里那一行本身，不存在第三方 —— 所以这里的做法更简单：**每次变更之后存一版**，
// 「改之前的样子」天然就是上一版。
//
// 为什么要有这张表：审计日志只记「谁在什么时候 PUT 了 /monitor/alert-rules/3」，
// **不记请求体**。所以「谁把阈值从 80 改成 200」这个问题在此之前是查不出来的 ——
// 而改错一条告警规则的后果是静默的：之后几周没人知道出了问题。
type RuleVersion struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Target 目标类型。目前只写 alert_rule；表故意做成多态的，
	// 以后接检测规则（它的 Steps 是一整块 JSON，更需要版本）不用改表结构
	Target   string `gorm:"size:24;index;not null" json:"target"`
	TargetID uint   `gorm:"index;not null" json:"targetId"`
	// TargetName 冗余存一份名字：规则删掉之后，版本列表仍然要能读懂
	TargetName string `gorm:"size:64" json:"targetName"`
	// Version 版本号，在同一个目标内自增
	Version int `gorm:"index" json:"version"`
	// Source created（新建）| edited（改动）| rollback（回滚产生的新版）
	//	| deleted（删除前的最后一版，留着它才能「误删恢复」）
	//	| restored（从某一版恢复出来的新规则的首版）
	Source string `gorm:"size:16;index" json:"source"`
	// Content 配置字段的 JSON 快照。**只含配置态、不含运行态**
	//（HitStreak / LastValue / LastStatus 那几个每轮评估都在变，
	// 混进来会让定时评估不停地「产生新版本」）
	Content string `gorm:"type:text" json:"content"`
	Hash    string `gorm:"size:64;index" json:"hash"`
	Note    string `gorm:"size:255" json:"note"`
	// Operator 操作人用户名。与 KubeChangeLog 同理冗余存名而不是 ID：
	// 用户改名或删号之后留痕仍然可读
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// NotifyTemplate 非邮件渠道的通知模板（IM 文本 / webhook JSON）。
//
// 为什么不和 EmailTemplate 合成一张表：邮件有主题、IM 与 webhook 没有，
// 硬合在一起会多出一个「对一半渠道没意义」的字段。变量表是共用的
// （见 handler 里的 notifyScenes），那才是真正需要收口的东西。
//
// 没有模板时**仍然按原来的硬编码格式发** —— 模板是可选的覆盖，不是前置条件。
// 这一点是刻意的：不能因为新增了模板功能就让没配模板的渠道发不出东西。
type NotifyTemplate struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Code string `gorm:"size:64;uniqueIndex;not null" json:"code"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Kind im（纯文本，三家 IM 通用）| webhook（JSON 报文）
	Kind string `gorm:"size:16;index;not null" json:"kind"`
	// Scene 决定可用变量，取值见 handler 的 notifyScenes：alert | oncall
	Scene string `gorm:"size:16;index;not null" json:"scene"`
	// Body 模板正文，Go text/template 语法。
	// webhook 类的 Body 渲染后必须是合法 JSON —— 保存时会校验
	Body      string    `gorm:"type:text" json:"body"`
	Builtin   bool      `gorm:"default:false" json:"builtin"`
	Enabled   bool      `json:"enabled"` // 不加 gorm default，见 Domain.AlertEnabled 的说明
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	Token     string    `gorm:"size:255" json:"-"` // 配了密钥时加密落库，不出接口
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

	AlertDays    int  `gorm:"default:30" json:"alertDays"` // 剩余天数低于该值判为 expiring
	AlertEnabled bool `json:"alertEnabled"`                // 是否把巡检结果送进告警通道

	DeptID    uint      `gorm:"index;default:0" json:"deptId"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	Enabled   bool      `json:"enabled"` // 停用后不参与批量巡检
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

	// VersionSeq 已经产生过多少个配置版本，下一版 = 这个数 +1。
	// 与 ConfigFile.VersionSeq 同一套做法（见 RuleVersion）
	VersionSeq int `gorm:"default:0" json:"versionSeq"`

	// Enabled 刻意**不加** gorm `default:true`：带 default 的布尔字段，GORM 会把
	// 零值（false）从 INSERT 里省掉让数据库填默认值 —— 于是「新建一条停用的规则」
	// 或「从版本恢复出一条默认停用的规则」都会被翻回启用，而界面上看不出来。
	// 默认值由 handler 显式给（与 Domain.AlertEnabled 同一个坑）
	Enabled   bool      `json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Probe 拨测任务：从平台所在网络位置去访问一个 HTTP 地址或 TCP 端口。
//
// 所有启用的拨测按同一节奏执行（OPS_PROBE_SPEC），不支持每条独立周期。
type Probe struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	Name   string `gorm:"size:64;not null" json:"name"`
	Type   string `gorm:"size:8;default:http" json:"type"`  // http | tcp
	Target string `gorm:"size:255;not null" json:"target"`  // http 填 URL，tcp 填 host:port
	Method string `gorm:"size:8;default:GET" json:"method"` // 仅 http

	ExpectStatus  int    `gorm:"default:200" json:"expectStatus"` // 0 表示只要 2xx/3xx 就算通
	ExpectKeyword string `gorm:"size:128" json:"expectKeyword"`   // 响应体必须包含，空表示不校验
	TimeoutSec    int    `gorm:"default:10" json:"timeoutSec"`
	// ProxyID 走哪个出口代理（仅 http 类型）。0 表示直连。
	// 代理被停用或删除时这条拨测**直接失败并点名原因**，不静默改成直连 ——
	// 静默直连会把「代理挂了」表现成「目标正常」
	ProxyID uint `gorm:"index;default:0" json:"proxyId"`

	AlertEnabled     bool `json:"alertEnabled"`
	ConsecutiveFails int  `gorm:"default:1" json:"consecutiveFails"` // 连续失败几次才告警
	FailStreak       int  `json:"failStreak"`

	LastStatus  string     `gorm:"size:16;default:unknown" json:"lastStatus"` // unknown | up | down
	LastCode    int        `json:"lastCode"`                                  // HTTP 状态码，tcp 恒为 0
	LastCostMs  int64      `json:"lastCostMs"`
	LastError   string     `gorm:"size:255" json:"lastError"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	TotalChecks int        `json:"totalChecks"`
	FailChecks  int        `json:"failChecks"`

	Enabled   bool      `json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProbeRecord 单次拨测结果。超过保留期的记录会在定时拨测时清理。
type ProbeRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ProbeID   uint      `gorm:"index;not null" json:"probeId"`
	Status    string    `gorm:"size:16" json:"status"` // up | down
	Code      int       `json:"code"`
	CostMs    int64     `json:"costMs"`
	ErrorMsg  string    `gorm:"size:255" json:"errorMsg"`
	Operator  string    `gorm:"size:64" json:"operator"` // 手动拨测记操作人，定时为 scheduler
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Event 需要有人处置的事件。
//
// 告警是「机器发现的现象」，事件是「人要跟进的事」：由人从一条或多条告警升格而来，
// 有负责人、有处置过程。平台不自动建单，避免把告警噪音原样搬成工单噪音。
type Event struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Title    string `gorm:"size:255;not null" json:"title"`
	Severity string `gorm:"size:16;index;default:warning" json:"severity"` // critical | warning | info
	// Status open 待处理 | processing 处理中 | resolved 已解决 | closed 已关闭（无需处理）
	Status  string `gorm:"size:16;index;default:open" json:"status"`
	Summary string `gorm:"type:text" json:"summary"`
	// AlertIDs 关联告警 ID 的 JSON 数组，接口层用 alertIds 暴露
	AlertIDs string `gorm:"type:text" json:"-"`
	// Origin manual 手动选告警建单 | bucket 从聚合桶建单 | secevent 从安全事件升格。
	// secevent 这一种**没有关联告警**：线索来自流水表，不是告警
	Origin     string `gorm:"size:16;default:manual" json:"origin"`
	OriginNote string `gorm:"size:255" json:"originNote"` // 例如聚合策略名与桶 key

	Assignee   string     `gorm:"size:64;index" json:"assignee"`
	AssignedBy string     `gorm:"size:64" json:"assignedBy"`
	AssignedAt *time.Time `json:"assignedAt"`

	CreatedByName  string     `gorm:"size:64" json:"createdByName"`
	LastActivityAt time.Time  `json:"lastActivityAt"`
	ResolvedAt     *time.Time `json:"resolvedAt"`
	ResolvedBy     string     `gorm:"size:64" json:"resolvedBy"`

	// RespondedAt 第一次「真的有人动手」的时间，用来算响应 SLA。
	//
	// 口径只认两件事：状态离开 open（有人接手），或者写下第一条处置记录。
	// 指派**不算**响应 —— 把单子丢给别人不等于开始处理，这是故意的，
	// 否则值班的人可以靠互相转单把响应 SLA 刷成全绿。
	RespondedAt *time.Time `json:"respondedAt"`
	// SLARespondAlertedAt / SLARecoverAlertedAt 上一次因为 SLA 提醒过的时间。
	// 只为了不刷屏：同一个事件同一条 SLA 在提醒间隔内不重复发消息。
	SLARespondAlertedAt *time.Time `json:"slaRespondAlertedAt"`
	SLARecoverAlertedAt *time.Time `json:"slaRecoverAlertedAt"`

	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EventLog 事件处置时间线，只追加不修改
type EventLog struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	EventID uint   `gorm:"index;not null" json:"eventId"`
	Action  string `gorm:"size:16" json:"action"` // create | assign | note | status | sla
	// Content 一句人能看懂的说明，例如「状态 open -> processing」
	Content   string    `gorm:"size:500" json:"content"`
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// EventReview 事件复盘。一个事件最多一份复盘，事件被标记「已解决」时自动建草稿。
//
// 复盘不是给人补作业的表单，而是把「这次到底发生了什么、为什么没早点发现、下次怎么防」
// 三件事沉淀成可检索、可跟踪的记录。里程碑时间由平台从告警与处置时间线算出建议值，
// 人可以改，但改了会在复盘里留下「与系统记录不一致」的痕迹（前端展示建议值对照）。
type EventReview struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	EventID uint `gorm:"uniqueIndex;not null" json:"eventId"`
	// Status draft 草稿 | reviewing 评审中 | archived 已归档（归档后需重新打开才能改）
	Status string `gorm:"size:16;index;default:draft" json:"status"`
	// Owner 复盘负责人，默认取事件负责人
	Owner string `gorm:"size:64;index" json:"owner"`

	// ---------- 时间里程碑，用来算 MTTA / MTTR ----------
	// HappenedAt 故障实际开始（默认取最早告警的首次出现时间）
	HappenedAt *time.Time `json:"happenedAt"`
	// DetectedAt 平台/人发现（默认取最早告警的入库时间）
	DetectedAt *time.Time `json:"detectedAt"`
	// RespondedAt 有人开始响应（默认取最早的告警确认时间，没有则取 Event.RespondedAt）。
	// 与 SLA 同口径：指派不算响应。
	RespondedAt *time.Time `json:"respondedAt"`
	// MitigatedAt 止血完成（业务恢复可用，可能还没根治），只能人填
	MitigatedAt *time.Time `json:"mitigatedAt"`
	// RecoveredAt 完全恢复（默认取事件的解决时间）
	RecoveredAt *time.Time `json:"recoveredAt"`

	Impact     string `gorm:"type:text" json:"impact"`     // 影响面：谁受影响、影响多久、有没有数据损失
	RootCause  string `gorm:"type:text" json:"rootCause"`  // 根因
	Trigger    string `gorm:"type:text" json:"trigger"`    // 诱因：什么变更/事件点了火
	DetectGap  string `gorm:"type:text" json:"detectGap"`  // 发现环节的问题：为什么没更早发现
	Mitigation string `gorm:"type:text" json:"mitigation"` // 止血过程：做了哪些动作
	Lesson     string `gorm:"type:text" json:"lesson"`     // 经验教训

	ArchivedAt *time.Time `json:"archivedAt"`
	ArchivedBy string     `gorm:"size:64" json:"archivedBy"`
	CreatedBy  uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// EventActionItem 复盘改进项。复盘写完就完的话等于没复盘，所以改进项独立成条目：
// 有负责人、有截止日期、有状态，逾期会按天给负责人发站内消息催办。
type EventActionItem struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	ReviewID uint   `gorm:"index;not null" json:"reviewId"`
	EventID  uint   `gorm:"index;not null" json:"eventId"`
	Title    string `gorm:"size:200;not null" json:"title"`
	Detail   string `gorm:"type:text" json:"detail"`
	// Kind prevent 防复发 | detect 提升发现能力 | mitigate 加快止血 | process 流程改进
	Kind    string     `gorm:"size:16;default:prevent" json:"kind"`
	Owner   string     `gorm:"size:64;index" json:"owner"`
	DueDate *time.Time `gorm:"index" json:"dueDate"`
	// Status open 待开始 | doing 进行中 | done 已完成 | dropped 不做了
	Status   string     `gorm:"size:16;index;default:open" json:"status"`
	DoneAt   *time.Time `json:"doneAt"`
	DoneNote string     `gorm:"size:255" json:"doneNote"`
	// LastRemindAt 上次催办时间，用来保证一天最多催一次
	LastRemindAt  *time.Time `json:"lastRemindAt"`
	CreatedByName string     `gorm:"size:64" json:"createdByName"`
	CreatedBy     uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// ConfigFile 被管配置文件：登记「哪台机器的哪个路径，内容该是什么」。
//
// 和防火墙那套「期望态 vs 真机现读 + 收敛」是同一个思路，只是对象从规则集换成文件内容：
//   - 期望内容是一个版本号（指向 ConfigVersion），不是自由文本，所以能回滚、能比对；
//   - 基线不让人从零写，而是从真机抓一份下来当第一版 —— 从零写出来的基线第一次下发
//     就会把机器打坏；
//   - 巡检现读真机内容算 sha256 对比期望版本，不一致就是漂移；
//   - 下发前必须先把真机现状存成一版（那就是回滚点），再备份、原子替换、回读校验。
type ConfigFile struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	HostID uint `gorm:"not null;uniqueIndex:idx_config_host_path" json:"hostId"`
	// Path 绝对路径
	Path     string `gorm:"size:255;not null;uniqueIndex:idx_config_host_path" json:"path"`
	Name     string `gorm:"size:64" json:"name"`
	Category string `gorm:"size:32;index" json:"category"`
	Owner    string `gorm:"size:64;index" json:"owner"`
	// Critical 关键配置：漂移产 critical 告警，下发/回滚要抄路径确认
	Critical bool `gorm:"index" json:"critical"`
	// ReloadUnit 改完要 reload 哪个服务才生效。空表示改完即生效（或需要人自己处理）。
	// 「改了配置但没 reload」是最常见的「改了却没生效」，所以把它登记进来。
	ReloadUnit string `gorm:"size:128" json:"reloadUnit"`
	// ReloadAction reload | restart，默认 reload
	ReloadAction string `gorm:"size:16;default:reload" json:"reloadAction"`
	AlertEnabled bool   `json:"alertEnabled"`
	Remark       string `gorm:"size:255" json:"remark"`

	// DesiredVersionID 期望内容对应的版本。0 表示还没定基线
	DesiredVersionID uint `gorm:"index;default:0" json:"desiredVersionId"`
	// VersionSeq 该文件已有多少版，新版本号 = VersionSeq + 1
	VersionSeq int `gorm:"default:0" json:"versionSeq"`

	// ---------- 实际态，由巡检回填 ----------
	ActualHash  string     `gorm:"size:64" json:"actualHash"`
	ActualSize  int64      `json:"actualSize"`
	ActualMode  string     `gorm:"size:16" json:"actualMode"`
	ActualMtime *time.Time `json:"actualMtime"`
	// DiffLines 与期望内容差多少行（新增+删除），-1 表示算不出来
	DiffLines int `gorm:"default:-1" json:"diffLines"`

	// Drift ok 一致 | drift 内容不一致 | missing 文件不存在 | no-desired 还没定基线
	//     | too-large 文件超出可管大小 | binary 不是文本 | error 读取失败 | unknown 未巡检
	Drift       string     `gorm:"size:16;index;default:unknown" json:"drift"`
	DriftDetail string     `gorm:"size:255" json:"driftDetail"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastError   string     `gorm:"size:255" json:"lastError"`

	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ConfigVersion 一份配置内容快照。只追加不修改 —— 它是回滚的唯一依据。
type ConfigVersion struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	FileID  uint `gorm:"index;not null" json:"fileId"`
	Version int  `gorm:"index" json:"version"`
	// Source captured 从真机抓的 | edited 人在平台上编辑的 | pre-apply 下发前的真机现状
	//      | applied 下发后回读的
	Source string `gorm:"size:16" json:"source"`
	// Content 原文。存原文而不是 diff：解析器会变，原文不会（与防火墙快照同一理由）
	Content string `gorm:"type:text" json:"content"`
	Hash    string `gorm:"size:64;index" json:"hash"`
	Size    int64  `json:"size"`
	Mode    string `gorm:"size:16" json:"mode"`
	Note    string `gorm:"size:255" json:"note"`
	// Operator 抓取/编辑的人，pre-apply 与 applied 记的是触发下发的人
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// ConfigApply 一次下发 / 回滚 / 抓取留痕。只追加不修改。
type ConfigApply struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	FileID   uint   `gorm:"index;not null" json:"fileId"`
	HostID   uint   `gorm:"index" json:"hostId"`
	HostName string `gorm:"size:64" json:"hostName"`
	Path     string `gorm:"size:255" json:"path"`
	// Action capture 抓基线 | apply 下发期望版本 | rollback 回滚到历史版本
	Action string `gorm:"size:16;index" json:"action"`
	// FromVersionID 动作前真机内容对应的版本（下发/回滚时即回滚点）
	FromVersionID uint `gorm:"default:0" json:"fromVersionId"`
	ToVersionID   uint `gorm:"default:0" json:"toVersionId"`
	// Status success | failed | blocked（被下发闸门或二次确认拦下）
	Status string `gorm:"size:16;index" json:"status"`
	// BackupPath 远端备份文件路径，出事了可以直接在机器上拷回去
	BackupPath string `gorm:"size:255" json:"backupPath"`
	// VerifyHash 下发后回读的 hash，与目标版本一致才算成功
	VerifyHash string `gorm:"size:64" json:"verifyHash"`
	// ReloadStatus 关联服务的 reload 结果：skipped | success | failed
	ReloadStatus string    `gorm:"size:16" json:"reloadStatus"`
	ReloadDetail string    `gorm:"size:255" json:"reloadDetail"`
	Detail       string    `gorm:"size:500" json:"detail"`
	ExecJobID    uint      `gorm:"index;default:0" json:"execJobId"`
	Operator     string    `gorm:"size:64" json:"operator"`
	ClientIP     string    `gorm:"size:64" json:"clientIp"`
	CreatedAt    time.Time `gorm:"index" json:"createdAt"`
}

// HostService 主机上的一个被纳管服务（systemd unit）。
//
// 平台不把机器上几百个 unit 全部落库 —— 那是噪音。这里只存「人说了算的那几个」：
// 纳管时登记期望态（该不该在跑、该不该开机自启、是不是关键服务），巡检拿真机实际态
// 来对照，不一致就是漂移并产告警。没纳管的 unit 只在「发现服务」里实时列出来，不落库。
//
// 端口是按 systemd cgroup 反查的：先从 ss 拿到监听端口与持有它的 PID，再读
// /proc/<pid>/cgroup 判断这个进程属于哪个 unit。这样 nginx 这类 master/worker
// 模型也能把端口归到正确的服务上。
type HostService struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	HostID uint `gorm:"not null;uniqueIndex:idx_host_unit" json:"hostId"`
	// Unit systemd 单元名，含 .service 后缀
	Unit        string `gorm:"size:128;not null;uniqueIndex:idx_host_unit" json:"unit"`
	Name        string `gorm:"size:64" json:"name"`
	Description string `gorm:"size:255" json:"description"`

	// ---------- 期望态，由人登记 ----------
	// ExpectActive 期望这个服务在运行。置 false 表示「这个服务就该是停着的」，
	// 它在跑反而算漂移（例如被禁用的旧版本服务）
	ExpectActive bool `json:"expectActive"`
	// ExpectEnabled 期望开机自启。static / indirect 这类本来就不能 enable 的不算漂移
	ExpectEnabled bool `json:"expectEnabled"`
	// Critical 关键服务：漂移时产 critical 告警，停服务/取消自启需要二次确认
	Critical     bool   `gorm:"index" json:"critical"`
	Owner        string `gorm:"size:64;index" json:"owner"`
	AlertEnabled bool   `json:"alertEnabled"`
	Remark       string `gorm:"size:255" json:"remark"`

	// ---------- 实际态，由巡检回填 ----------
	LoadState   string `gorm:"size:24" json:"loadState"`         // loaded | not-found | masked | error
	ActiveState string `gorm:"size:24;index" json:"activeState"` // active | inactive | failed | activating…
	SubState    string `gorm:"size:24" json:"subState"`          // running | exited | dead | failed…
	EnableState string `gorm:"size:24" json:"enableState"`       // enabled | disabled | static | masked…
	Ports       string `gorm:"size:255" json:"ports"`            // 逗号分隔，按 cgroup 反查出来的监听端口
	ProcCount   int    `json:"procCount"`                        // 持有监听端口的进程数

	// Drift ok 一致 | inactive 该跑没跑 | unexpected 该停在跑 | disabled 该自启没自启
	//     | missing 机器上找不到这个 unit | unknown 还没巡检过 | error 巡检失败
	Drift       string     `gorm:"size:16;index;default:unknown" json:"drift"`
	DriftDetail string     `gorm:"size:255" json:"driftDetail"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastError   string     `gorm:"size:255" json:"lastError"`

	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// HostServiceAction 一次服务启停留痕。只追加不修改。
//
// 记「动之前是什么样、动之后是什么样」：光记「执行了 restart」回答不了
// 「到底起来了没有」，所以动作前后各读一次真机状态存进来。
type HostServiceAction struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	HostID    uint   `gorm:"index;not null" json:"hostId"`
	HostName  string `gorm:"size:64" json:"hostName"`
	ServiceID uint   `gorm:"index;default:0" json:"serviceId"`
	Unit      string `gorm:"size:128" json:"unit"`
	// Action start | stop | restart | reload | enable | disable
	Action string `gorm:"size:16;index" json:"action"`
	// Status success | failed | blocked（被下发闸门拦下）
	Status string `gorm:"size:16;index" json:"status"`
	Detail string `gorm:"size:500" json:"detail"`
	// BeforeState / AfterState 形如 active/running,enabled
	BeforeState string    `gorm:"size:64" json:"beforeState"`
	AfterState  string    `gorm:"size:64" json:"afterState"`
	ExecJobID   uint      `gorm:"index;default:0" json:"execJobId"`
	Operator    string    `gorm:"size:64" json:"operator"`
	ClientIP    string    `gorm:"size:64" json:"clientIp"`
	CreatedAt   time.Time `gorm:"index" json:"createdAt"`
}

// Runbook 处置剧本：把「这类告警来了该怎么办」写成可被推荐、可被执行、可被复盘的东西。
//
// 剧本不是 wiki 文档：
//   - 有匹配条件，告警/事件详情里会按条件自动推荐并给出匹配理由；
//   - 步骤里的命令会过命令规则静态预检，命中拦截规则的剧本不允许启用；
//   - 每次使用都记一条结果（解决了 / 部分有效 / 没用），用得多但从不解决问题的剧本能被看见。
type Runbook struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:128;not null" json:"name"`
	Category string `gorm:"size:32;index" json:"category"`
	// Summary 什么情况下用这本剧本
	Summary string `gorm:"size:500" json:"summary"`

	// ---------- 匹配条件。三项都为空表示通用剧本，兜底推荐且排在最后 ----------
	// MatchLabels 告警标签匹配条件，JSON 对象；要求全部命中，值为 * 表示只要求键存在
	MatchLabels string `gorm:"type:text" json:"matchLabels"`
	// MatchKeywords 标题关键词，逗号分隔，命中任意一个即算
	MatchKeywords string `gorm:"size:255" json:"matchKeywords"`
	// MatchSeverity 告警级别，空表示不限
	MatchSeverity string `gorm:"size:16" json:"matchSeverity"`

	// Steps 处置步骤，JSON 数组：[{"title":"","detail":"","command":""}]
	Steps string `gorm:"type:text" json:"steps"`
	// Precheck 动手之前要先确认什么
	Precheck string `gorm:"type:text" json:"precheck"`
	// Rollback 做错了怎么退回来
	Rollback  string `gorm:"type:text" json:"rollback"`
	RiskLevel string `gorm:"size:8;default:low" json:"riskLevel"` // low | medium | high
	Enabled   bool   `gorm:"default:true" json:"enabled"`

	// 以下由命令规则预检回填，与脚本库同一套规则
	PrecheckStatus string     `gorm:"size:16;default:unknown" json:"precheckStatus"` // unknown | pass | warn | blocked
	PrecheckHits   string     `gorm:"type:text" json:"precheckHits"`                 // JSON 数组
	PrecheckedAt   *time.Time `json:"precheckedAt"`

	// Version 内容每改一次 +1，使用记录会记下当时用的是第几版
	Version     int        `gorm:"default:1" json:"version"`
	UseCount    int        `json:"useCount"`
	SolveCount  int        `json:"solveCount"` // 其中被判定「解决了」的次数
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	LastUsedBy  string     `gorm:"size:64" json:"lastUsedBy"`
	CreatorName string     `gorm:"size:64" json:"creatorName"`
	CreatedBy   uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// RunbookUse 一次剧本使用记录。只追加不修改，是「这次故障按哪本剧本处置的」的凭据。
type RunbookUse struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	RunbookID   uint   `gorm:"index;not null" json:"runbookId"`
	RunbookName string `gorm:"size:128" json:"runbookName"`
	// Version 使用时剧本的版本号，剧本后来被改了也能对上当时看到的内容
	Version int  `gorm:"default:1" json:"version"`
	EventID uint `gorm:"index;default:0" json:"eventId"`
	AlertID uint `gorm:"index;default:0" json:"alertId"`
	// Outcome resolved 解决了 | partial 部分有效 | invalid 没用
	Outcome string `gorm:"size:16;index" json:"outcome"`
	// DoneSteps 做了哪几步，JSON 数组存步骤下标
	DoneSteps string    `gorm:"type:text" json:"doneSteps"`
	Note      string    `gorm:"size:500" json:"note"`
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Script 脚本库条目：把散落在各人手里的运维命令收拢成可复用、可审阅的资产。
//
// 脚本内容在保存时会用「命令规则」跑一遍静态预检，命中拦截规则的脚本不允许下发。
type Script struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:64;not null" json:"name"`
	Category    string `gorm:"size:32;index" json:"category"`
	Description string `gorm:"size:255" json:"description"`
	Content     string `gorm:"type:text;not null" json:"content"`
	// Params 参数定义，JSON 数组：[{"name":"PATH","label":"目录","default":"/tmp","required":true}]
	// 脚本里用 ${NAME} 占位，下发前替换
	Params  string `gorm:"type:text" json:"params"`
	Timeout int    `gorm:"default:60" json:"timeout"`
	// RiskLevel 由维护者声明，只作提示；真正的拦截看预检结果
	RiskLevel string `gorm:"size:8;default:low" json:"riskLevel"` // low | medium | high

	// 以下由预检回填
	PrecheckStatus string     `gorm:"size:16;default:unknown" json:"precheckStatus"` // unknown | pass | warn | blocked
	PrecheckHits   string     `gorm:"type:text" json:"precheckHits"`                 // JSON 数组，命中的规则
	PrecheckedAt   *time.Time `json:"precheckedAt"`

	UseCount    int        `json:"useCount"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
	LastUsedBy  string     `gorm:"size:64" json:"lastUsedBy"`
	Enabled     bool       `gorm:"default:true" json:"enabled"`
	CreatorName string     `gorm:"size:64" json:"creatorName"`
	CreatedBy   uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// KubeChangeLog 集群写操作留痕。
//
// 通用操作审计只记「谁调了哪个接口、返回多少」，但改集群这件事必须能答出
// 「改的是哪个对象、提交的是什么、是不是只预检」，所以单独存一张表。
type KubeChangeLog struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	ClusterID   uint   `gorm:"index" json:"clusterId"`
	ClusterName string `gorm:"size:64" json:"clusterName"`
	Kind        string `gorm:"size:32" json:"kind"`
	Namespace   string `gorm:"size:128" json:"namespace"`
	Name        string `gorm:"size:191" json:"name"`
	// Action apply | scale
	Action string `gorm:"size:16" json:"action"`
	// DryRun 只做服务端预检，集群没有真被改
	DryRun bool `json:"dryRun"`
	// Forced apply 时强行接管了别人管着的字段
	Forced bool `json:"forced"`
	// Payload apply 提交的 YAML 原文，或 scale 的目标副本数
	Payload string `gorm:"type:text" json:"payload"`
	Status  string `gorm:"size:16" json:"status"` // success | failed
	Detail  string `gorm:"size:500" json:"detail"`
	UserID  uint   `gorm:"index;default:0" json:"userId"`
	// Username 落冗余名字，用户改名或删号之后留痕仍然可读
	Username  string    `gorm:"size:64" json:"username"`
	ClientIP  string    `gorm:"size:64" json:"clientIp"`
	CostMs    int64     `json:"costMs"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// Topology 业务拓扑：把一条业务链路画成图，回答「这个业务现在哪一环挂了」。
//
// 拓扑本身不产生任何监控数据，节点健康度实时取自它绑定的资源（主机/数据库/拨测/证书），
// 所以这里存的只有结构与画布坐标。
type Topology struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;not null" json:"name"`
	Remark      string    `gorm:"size:255" json:"remark"`
	CreatorName string    `gorm:"size:64" json:"creatorName"`
	CreatedBy   uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TopologyNode 拓扑节点。Kind + RefID 指向平台内已有资源；
// kind=external 表示平台管不到的外部依赖（对方 API、运营商线路等），健康度恒为「未知」。
type TopologyNode struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	TopologyID uint   `gorm:"index;not null" json:"topologyId"`
	Name       string `gorm:"size:64;not null" json:"name"`
	Kind       string `gorm:"size:16;default:external" json:"kind"` // host | database | probe | certificate | external
	RefID      uint   `gorm:"default:0" json:"refId"`               // external 恒为 0
	// X / Y 画布坐标，拖动后由「保存布局」批量写回
	X         int       `gorm:"default:0" json:"x"`
	Y         int       `gorm:"default:0" json:"y"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TopologyEdge 依赖连线：From 依赖 To（箭头由 From 指向 To）
type TopologyEdge struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TopologyID uint      `gorm:"index;not null" json:"topologyId"`
	FromNodeID uint      `gorm:"index;not null" json:"fromNodeId"`
	ToNodeID   uint      `gorm:"index;not null" json:"toNodeId"`
	Label      string    `gorm:"size:32" json:"label"`
	CreatedAt  time.Time `json:"createdAt"`
}

// DetectionRule 检测规则：把「多条告警的组合」判定成一个问题。
//
// 和另外两个模块的分工：告警规则看单个指标越不越阈值，聚合策略把同类告警归堆降噪，
// 检测规则回答「A 和 B 一起出现（或先后出现）才说明是那个故障」。
// 它只读平台自己的告警表，不依赖任何外部关联引擎。
type DetectionRule struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Mode sequence 顺序链（按先后）| concurrent 并发窗（同窗口内都出现）| join 窗口 Join（还要落在同一个对象上）
	Mode string `gorm:"size:16;default:concurrent" json:"mode"`
	// Steps 步骤定义，JSON 数组，每步是一个告警匹配条件：
	// [{"name":"网关 5xx","titleKeyword":"5xx","source":"","severity":"","labelKey":"","labelValue":""}]
	Steps string `gorm:"type:text" json:"steps"`
	// JoinLabel mode=join 时用来对齐的标签键，例如 host：两步都得是同一台机器上的告警才算
	JoinLabel string `gorm:"size:32" json:"joinLabel"`
	// WindowMinutes 关联窗口。窗口内「发生过」的告警都算，包括已恢复的
	WindowMinutes int    `gorm:"default:30" json:"windowMinutes"`
	Severity      string `gorm:"size:16;default:warning" json:"severity"`

	LastStatus string     `gorm:"size:16;default:unknown" json:"lastStatus"` // unknown | ok | firing | error
	LastDetail string     `gorm:"size:500" json:"lastDetail"`
	LastEvalAt *time.Time `json:"lastEvalAt"`
	LastFireAt *time.Time `json:"lastFireAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// KubeCluster 纳管的 Kubernetes 集群。
//
// 平台只存 kubeconfig 全文并按需发起只读 REST 调用，不在本地缓存集群资源 ——
// 看到的永远是集群当下的状态，不存在「平台里还是旧的」这种问题。
// MetricSource 指标数据源（目前只支持 Prometheus 兼容的 HTTP API）。
//
// 平台不自己存时序数据：已经有 Prometheus 的地方再复制一份没意义，
// 这里只做查询入口，把 PromQL 结果拍成表格与折线图。
type MetricSource struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Type 预留：prometheus | victoriametrics 等 Prometheus 兼容实现
	Type string `gorm:"size:16;default:prometheus" json:"type"`
	// BaseURL Prometheus 根地址，例如 http://prom.internal:9090（不含 /api/v1）
	BaseURL string `gorm:"size:255;not null" json:"baseUrl"`
	// HeaderKey / HeaderValue 可选鉴权头，值不出接口
	HeaderKey   string `gorm:"size:64" json:"headerKey"`
	HeaderValue string `gorm:"size:255" json:"-"`
	TimeoutSec  int    `gorm:"default:15" json:"timeoutSec"`
	// IsDefault 界面默认选中的数据源，只允许一个
	IsDefault bool `gorm:"default:false" json:"isDefault"`

	// 以下由连通性检查回填
	Status      string     `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | error
	Version     string     `gorm:"size:64" json:"version"`
	SeriesCount int64      `json:"seriesCount"`
	LastError   string     `gorm:"size:500" json:"lastError"`
	LastCheckAt *time.Time `json:"lastCheckAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LogSource 日志数据源（目前只支持 Loki 的 HTTP API）。
//
// 和指标一样：平台不存日志，只做查询入口。日志量级更大，复制一份既贵又没用。
type LogSource struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	Type string `gorm:"size:16;default:loki" json:"type"`
	// BaseURL Loki 根地址，例如 http://loki:3100（不含 /loki/api/...）
	BaseURL string `gorm:"size:255;not null" json:"baseUrl"`
	// Tenant 多租户 Loki 的 X-Scope-OrgID，单机模式留空
	Tenant      string `gorm:"size:64" json:"tenant"`
	HeaderKey   string `gorm:"size:64" json:"headerKey"`
	HeaderValue string `gorm:"size:255" json:"-"`
	TimeoutSec  int    `gorm:"default:30" json:"timeoutSec"`
	IsDefault   bool   `gorm:"default:false" json:"isDefault"`

	// 以下由连通性检查回填
	Status string `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | error
	// LabelCount 最近一小时的标签数，给人一个「这个源里有没有东西」的量感
	LabelCount  int        `json:"labelCount"`
	LastError   string     `gorm:"size:500" json:"lastError"`
	LastCheckAt *time.Time `json:"lastCheckAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TraceSource 链路数据源（目前只支持 Jaeger Query 的 HTTP API）。
//
// 与指标、日志同一个路子：平台只做查询入口，不接收上报、不存链路数据。
// 应用把 span 发给谁（Jaeger / OTel Collector）与平台无关。
type TraceSource struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	Type string `gorm:"size:16;default:jaeger" json:"type"`
	// BaseURL Jaeger Query 根地址，例如 http://jaeger:16686（不含 /api/...）
	BaseURL     string `gorm:"size:255;not null" json:"baseUrl"`
	HeaderKey   string `gorm:"size:64" json:"headerKey"`
	HeaderValue string `gorm:"size:255" json:"-"`
	TimeoutSec  int    `gorm:"default:20" json:"timeoutSec"`
	IsDefault   bool   `gorm:"default:false" json:"isDefault"`

	// 以下由连通性检查回填
	Status string `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | error
	// ServiceCount 上报过链路的服务数
	ServiceCount int        `json:"serviceCount"`
	LastError    string     `gorm:"size:500" json:"lastError"`
	LastCheckAt  *time.Time `json:"lastCheckAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SavedMetricQuery 常用查询。排查时反复敲同一串 PromQL 很费劲，存下来点一下就行。
type SavedMetricQuery struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"size:64;not null" json:"name"`
	SourceID uint   `gorm:"index;default:0" json:"sourceId"`
	Expr     string `gorm:"type:text;not null" json:"expr"`
	// RangeMode 默认以范围查询打开（折线图），否则即时查询（表格）
	RangeMode bool      `gorm:"default:true" json:"rangeMode"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type KubeCluster struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Kubeconfig 全文，含 CA 与客户端私钥，等同于集群管理员凭据，不出接口
	Kubeconfig string `gorm:"type:text" json:"-"`
	// ContextName 选用的上下文，留空表示用 kubeconfig 的 current-context
	ContextName string `gorm:"size:64" json:"contextName"`
	// Server 由 kubeconfig 解析回填，只为展示
	Server string `gorm:"size:255" json:"server"`

	// 以下由连通性检查回填，不接受手工录入
	Version     string     `gorm:"size:64" json:"version"`
	Status      string     `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | degraded | error
	NodeTotal   int        `json:"nodeTotal"`
	NodeReady   int        `json:"nodeReady"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastError   string     `gorm:"size:500" json:"lastError"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ModelUpstream 模型资源池里的一条上游。
//
// 一条记录 = 「某个供应商的某个模型」。多条记录用同一个 Alias 就组成一个池：
// 调用方只认 Alias，平台按权重挑一条转发，失败自动换下一条。
// 这样换供应商、加降级备份都不用改调用方的代码。
//
// 只支持 OpenAI 兼容的 /chat/completions（多数供应商都兼容这套），
// 不做流式：SSE 要维持长连接，而且中途拿不到完整的 usage，用量就算不准。
type ModelUpstream struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Alias 对外的逻辑模型名，调用方传这个；同 Alias 的多条记录互为备份
	Alias string `gorm:"size:64;index;not null" json:"alias"`
	// Provider 供应商标识，只为展示与归类（openai / deepseek / qwen / ollama ...）
	Provider string `gorm:"size:32;default:openai" json:"provider"`
	// BaseURL OpenAI 兼容根地址，含 /v1，例如 https://api.deepseek.com/v1
	BaseURL string `gorm:"size:255;not null" json:"baseUrl"`
	// APIKey 上游密钥，配了 OPS_SECRET_KEY 时加密落库（见 docs/SECURITY.md），不出接口
	APIKey string `gorm:"size:255" json:"-"`
	// Model 上游真实模型名，例如 deepseek-chat
	Model string `gorm:"size:128;not null" json:"model"`
	// Weight 挑选权重，越大越优先；同权重按 id 顺序
	Weight     int `gorm:"default:10" json:"weight"`
	TimeoutSec int `gorm:"default:60" json:"timeoutSec"`
	// InputPrice / OutputPrice 每千 token 单价（元），用来算成本。
	// 这是按登记单价算出的估值，不等于供应商账单 —— 平台不做对账。
	InputPrice  float64 `gorm:"default:0" json:"inputPrice"`
	OutputPrice float64 `gorm:"default:0" json:"outputPrice"`

	// 以下由连通性检查回填
	ModelStatus string `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | error
	// ModelListed 检查时在上游 /models 列表里确实看到了这个模型名
	ModelListed bool       `gorm:"default:false" json:"modelListed"`
	LatencyMs   int64      `json:"latencyMs"`
	LastError   string     `gorm:"size:500" json:"lastError"`
	LastCheckAt *time.Time `json:"lastCheckAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ModelCall 一次模型调用的流水，用量与成本都从这里统计。
//
// 只记元数据：提示词与回复正文一律不落库 —— 那等于把业务数据（可能含客户信息）
// 抄进运维平台的库里。要看具体内容，应该在调用方自己的日志里看。
type ModelCall struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	UpstreamID   uint   `gorm:"index;default:0" json:"upstreamId"`
	UpstreamName string `gorm:"size:64" json:"upstreamName"`
	Alias        string `gorm:"size:64;index" json:"alias"`
	Provider     string `gorm:"size:32" json:"provider"`
	Model        string `gorm:"size:128" json:"model"`
	// Caller 调用来源：api（走网关接口）| console（界面上试调用）
	Caller string `gorm:"size:16;index" json:"caller"`
	UserID uint   `gorm:"index;default:0" json:"userId"`
	// Username 落冗余名字，用户改名或删号之后流水仍然可读
	Username string `gorm:"size:64" json:"username"`
	ClientIP string `gorm:"size:64" json:"clientIp"`
	// Tokens 以上游返回的 usage 为准；上游没给就是 0，成本也算不出来
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TotalTokens      int     `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	// UsageMissing 上游没返回 usage，这条的 token 与成本不可信
	UsageMissing bool   `gorm:"default:false" json:"usageMissing"`
	LatencyMs    int64  `json:"latencyMs"`
	CallStatus   string `gorm:"size:16;index" json:"status"` // success | failed
	ErrorMsg     string `gorm:"size:500" json:"errorMsg"`
	// Retried 这一条是前一个上游失败后切过来的
	Retried   bool      `gorm:"default:false" json:"retried"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// AgentConfig 一个「看平台数据、给结论」的 Agent。
//
// 这里的 Agent 是只读的分析器，不是执行器：它能读平台自己的数据（告警、主机指标、
// 执行结果、会话命令），拼成上下文交给模型，拿回一段文字。**它不会执行任何命令、
// 不会改任何东西** —— 在一个能连生产机器的运维平台里放一个会自己动手的东西，
// 收益远不及风险。
type AgentConfig struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Alias 用模型资源池里的哪个逻辑模型（不是上游真实模型名）
	Alias string `gorm:"size:64;not null" json:"alias"`
	// DataSource 上下文取自哪类平台数据：none | alert | host_metric | exec_job | session_command
	DataSource string `gorm:"size:24;default:none" json:"dataSource"`
	// MaxItems 上下文最多取多少条，防止把几千条记录塞进提示词
	MaxItems int `gorm:"default:20" json:"maxItems"`
	// SystemPrompt 角色设定，作为 system 消息发出去
	SystemPrompt string `gorm:"type:text" json:"systemPrompt"`
	// PromptTemplate 用户消息模板，text/template 语法，可用 {{.context}} 与 {{.input}}
	PromptTemplate string  `gorm:"type:text" json:"promptTemplate"`
	Temperature    float64 `gorm:"default:0" json:"temperature"`
	MaxTokens      int     `gorm:"default:800" json:"maxTokens"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AgentRun 一次 Agent 运行的记录。
//
// 与网关的调用流水不同，这里**要存正文**：输入的是平台自己的数据、输出的是分析结论，
// 存下来才能回看「上次这条告警是怎么判的」。网关代理的那些调用属于业务数据，仍然不存。
type AgentRun struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	AgentID   uint   `gorm:"index;default:0" json:"agentId"`
	AgentName string `gorm:"size:64" json:"agentName"`
	Alias     string `gorm:"size:64" json:"alias"`
	// DataSource / TargetID 这次实际用的上下文来源与目标对象（主机、作业、会话的 ID）
	DataSource string `gorm:"size:24" json:"dataSource"`
	TargetID   uint   `gorm:"default:0" json:"targetId"`
	// Input 运行时补充的说明，Output 模型给出的结论
	Input  string `gorm:"type:text" json:"input"`
	Output string `gorm:"type:text" json:"output"`
	// ContextItems / ContextChars 这次喂进去了多少条、多少字符，便于判断上下文是否被截断
	ContextItems int `json:"contextItems"`
	ContextChars int `json:"contextChars"`
	// ContextTruncated 上下文条数撞到 MaxItems 上限，说明还有更多数据没进提示词
	ContextTruncated bool `gorm:"default:false" json:"contextTruncated"`
	// CallID 对应 model_calls 里的那一条，用量与成本在那边算
	CallID           uint      `gorm:"index;default:0" json:"callId"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	TotalTokens      int       `json:"totalTokens"`
	Cost             float64   `json:"cost"`
	LatencyMs        int64     `json:"latencyMs"`
	RunStatus        string    `gorm:"size:16;index" json:"status"` // success | failed
	ErrorMsg         string    `gorm:"size:500" json:"errorMsg"`
	UserID           uint      `gorm:"index;default:0" json:"userId"`
	Username         string    `gorm:"size:64" json:"username"`
	CreatedAt        time.Time `gorm:"index" json:"createdAt"`
}

// ImApp 一个 IM 企业自建应用（企业微信 / 钉钉 / 飞书）。
//
// 与「通知渠道」里的群机器人是两套东西：机器人只能往群里发消息，
// 这里用的是应用级凭据（corpid/appsecret），能读通讯录。
type ImApp struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:64;not null" json:"name"`
	// Provider wecom | dingtalk | feishu
	Provider string `gorm:"size:16;index;not null" json:"provider"`
	// CorpID 企业微信 corpid / 钉钉 corpId / 飞书 app_id
	CorpID string `gorm:"size:128;not null" json:"corpId"`
	// AppSecret 应用密钥，配了 OPS_SECRET_KEY 时加密落库，不出接口
	AppSecret string `gorm:"size:255" json:"-"`
	// AgentID 企业微信自建应用的 agentid，其他家留空
	AgentID string `gorm:"size:64" json:"agentId"`
	// BaseURL 接口根地址。留空用各家默认（qyapi.weixin.qq.com / oapi.dingtalk.com /
	// open.feishu.cn）；内网通过代理出网、或要对着私有网关调时可以改
	BaseURL string `gorm:"size:255" json:"baseUrl"`
	// RootDeptID 同步起点部门：企微填 1，飞书填 0 或部门 open_id，钉钉填 1
	RootDeptID string `gorm:"size:64" json:"rootDeptId"`
	// TargetCompanyID 同步到平台哪个公司下（部门树挂在它下面）
	TargetCompanyID uint `gorm:"default:0" json:"targetCompanyId"`
	// DefaultRoleID 新建用户默认给的角色。只在新建时生效，之后手工调过的角色不会被同步覆盖
	DefaultRoleID uint `gorm:"default:0" json:"defaultRoleId"`
	// DisableMissing IM 侧已经查不到的人（离职），把平台账号置为停用。
	// 只停用、不删除 —— 删了操作审计里的历史记录就对不上人了
	DisableMissing bool `gorm:"default:true" json:"disableMissing"`

	// LoginEnabled 是否允许用这个应用扫码登录平台
	LoginEnabled bool `gorm:"default:false" json:"loginEnabled"`
	// RedirectURI 在 IM 开放平台登记的回调地址，要指向平台的
	// /api/v1/auth/im/callback（域名必须与 IM 后台配的一致，否则厂商直接拒绝授权）
	RedirectURI string `gorm:"size:255" json:"redirectUri"`
	// LoginRedirect 登录成功后把浏览器送回哪个前端地址。平台只在它后面追加一次性
	// ticket，**不把 JWT 放进 URL** —— URL 会进浏览器历史与各级访问日志
	LoginRedirect string `gorm:"size:255" json:"loginRedirect"`

	// 以下由连通性检查与同步回填
	AppStatus   string     `gorm:"size:16;default:unknown" json:"status"` // unknown | healthy | error
	LastError   string     `gorm:"size:500" json:"lastError"`
	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastSyncAt  *time.Time `json:"lastSyncAt"`

	Enabled   bool      `gorm:"default:true" json:"enabled"`
	Remark    string    `gorm:"size:255" json:"remark"`
	CreatedBy uint      `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ImAccount IM 账号与平台账号的绑定关系。
//
// 单独一张表而不是往 users 上加列：一个平台账号未来可能同时绑企微与飞书，
// 而且 IM 侧的字段（手机号、部门路径）不该混进平台的用户模型里。
type ImAccount struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	AppID    uint   `gorm:"index;default:0" json:"appId"`
	Provider string `gorm:"size:16;index" json:"provider"`
	// ImUserID IM 侧的用户标识（企微 userid / 钉钉 userid / 飞书 open_id）
	ImUserID string `gorm:"size:128;index;not null" json:"imUserId"`
	ImName   string `gorm:"size:64" json:"imName"`
	// ImMobile / ImEmail 只作参照，平台用户模型里没有手机号
	ImMobile string `gorm:"size:32" json:"imMobile"`
	ImEmail  string `gorm:"size:128" json:"imEmail"`
	// ImDeptPath IM 侧的部门路径，便于人工核对映射对不对
	ImDeptPath string     `gorm:"size:255" json:"imDeptPath"`
	UserID     uint       `gorm:"index;not null" json:"userId"`
	Username   string     `gorm:"size:64" json:"username"`
	LastSyncAt *time.Time `json:"lastSyncAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// ImSyncRun 一次组织同步的结果。预演也记，便于回答「上次同步到底改了什么」。
type ImSyncRun struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	AppID    uint   `gorm:"index;default:0" json:"appId"`
	AppName  string `gorm:"size:64" json:"appName"`
	Provider string `gorm:"size:16" json:"provider"`
	// DryRun 预演不写库，只给出将要发生什么
	DryRun bool `gorm:"default:false" json:"dryRun"`

	DeptTotal    int `json:"deptTotal"`
	DeptCreated  int `json:"deptCreated"`
	DeptUpdated  int `json:"deptUpdated"`
	UserTotal    int `json:"userTotal"`
	UserCreated  int `json:"userCreated"`
	UserBound    int `json:"userBound"`
	UserUpdated  int `json:"userUpdated"`
	UserDisabled int `json:"userDisabled"`
	UserSkipped  int `json:"userSkipped"`

	SyncStatus string    `gorm:"size:16;index" json:"status"` // success | failed
	ErrorMsg   string    `gorm:"size:500" json:"errorMsg"`
	CostMs     int64     `json:"costMs"`
	UserIDOp   uint      `gorm:"column:operator_id;index;default:0" json:"operatorId"`
	Operator   string    `gorm:"size:64" json:"operator"`
	CreatedAt  time.Time `gorm:"index" json:"createdAt"`
}

// LdapServer 一台 LDAP / AD 目录服务器。
//
// 平台只读目录：用它校验口令、查人，不写回、也不做组织同步（那条路是「IM 组织同步」）。
// BindPassword 配了 OPS_SECRET_KEY 时加密落库，接口一律不返回。
type LdapServer struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	Name       string `gorm:"size:64;not null" json:"name"`
	Host       string `gorm:"size:128;not null" json:"host"`
	Port       int    `gorm:"default:389" json:"port"`
	Encryption string `gorm:"size:16;default:none" json:"encryption"` // none | ldaps | starttls
	// SkipVerify 内网自签证书常见，是否跳过证书校验由管理员显式决定
	SkipVerify   bool   `gorm:"default:false" json:"skipVerify"`
	BindDN       string `gorm:"size:255" json:"bindDn"` // 服务账号，留空为匿名（只影响搜索）
	BindPassword string `gorm:"size:255" json:"-"`
	BaseDN       string `gorm:"size:255" json:"baseDn"`
	// UserFilter 必须含一个 %s，登录名会被转义后替换进去
	UserFilter   string `gorm:"size:255" json:"userFilter"`
	AttrNickname string `gorm:"size:64;default:displayName" json:"attrNickname"`
	AttrEmail    string `gorm:"size:64;default:mail" json:"attrEmail"`
	TimeoutSec   int    `gorm:"default:8" json:"timeoutSec"`

	Enabled bool `json:"enabled"`
	// LoginEnabled 关掉之后，已绑定的账号立刻回落到本地口令都登不进来（见 handler 说明）
	LoginEnabled bool `json:"loginEnabled"`
	// AutoBind 首次用域口令登录时，若平台已存在同名账号则自动建立绑定。
	// 不存在的账号一律拒绝——平台永远不按目录自动建号。
	AutoBind bool `gorm:"default:false" json:"autoBind"`

	LastCheckAt *time.Time `json:"lastCheckAt"`
	LastStatus  string     `gorm:"size:16" json:"lastStatus"` // success | failed
	LastMessage string     `gorm:"size:500" json:"lastMessage"`
	CreatedBy   uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// LdapAccount 平台账号与目录条目的绑定关系。
//
// 绑定存在就意味着「这个平台账号的口令由目录说了算」，本地口令哈希不再参与登录判定。
type LdapAccount struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	ServerID uint `gorm:"index;not null" json:"serverId"`
	// LdapUID 目录里的登录名（uid / sAMAccountName 的值）
	LdapUID string `gorm:"size:128;index;not null" json:"ldapUid"`
	// LdapDN 条目 DN，登录时直接拿它做 bind
	LdapDN    string     `gorm:"size:255;not null" json:"ldapDn"`
	LdapName  string     `gorm:"size:64" json:"ldapName"`
	LdapEmail string     `gorm:"size:128" json:"ldapEmail"`
	UserID    uint       `gorm:"index;not null" json:"userId"`
	Username  string     `gorm:"size:64" json:"username"`
	BoundBy   string     `gorm:"size:64" json:"boundBy"` // 谁建立的绑定，auto 表示首次登录自动绑定
	LastLogin *time.Time `json:"lastLogin"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// ---------- 安全意识（培训与考核） ----------

// AwarenessCourse 一个必修项：一段要读的内容，加上可选的几道题。
//
// 做成「培训 + 考核」而不是「上传课件」：平台不做内容管理，正文就是几条要点，
// 真正有价值的是**谁读了、谁答对了、谁到期还没做**这三件事能被查出来。
type AwarenessCourse struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Title   string `gorm:"size:128;not null" json:"title"`
	Summary string `gorm:"size:255" json:"summary"`
	// Content 正文，Markdown 风格的纯文本（前端按预换行展示，不渲染 HTML）
	Content string `gorm:"type:text" json:"content"`
	// Scope 必修范围：all 全员 | role 指定角色 | dept 指定部门（含子部门）
	Scope       string `gorm:"size:16;default:all" json:"scope"`
	ScopeRoleID uint   `gorm:"default:0" json:"scopeRoleId"`
	ScopeDeptID uint   `gorm:"default:0" json:"scopeDeptId"`
	// PassScore 及格分（百分制）。没有题目时只要求「确认已读」，这个值不参与判定
	PassScore int `gorm:"default:80" json:"passScore"`
	// DueAt 截止时间。到期未完成的人会收到站内消息并在完成率里被点名；空表示不限期
	DueAt       *time.Time `json:"dueAt"`
	Published   bool       `gorm:"default:false" json:"published"`
	PublishedAt *time.Time `json:"publishedAt"`
	Publisher   string     `gorm:"size:64" json:"publisher"`
	// TargetCount / DoneCount 发布后回写的统计快照，列表页直接读，不用每行现算
	TargetCount int `gorm:"default:0" json:"targetCount"`
	DoneCount   int `gorm:"default:0" json:"doneCount"`
	// LastRemindAt 最近一次逾期提醒的时间，避免同一天反复轰炸
	LastRemindAt *time.Time `json:"lastRemindAt"`
	CreatedBy    uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// AwarenessQuestion 培训项下的一道题。
//
// Answer 带 json:"-"：正确答案绝不出接口，判分只在后端做 ——
// 否则「考核」就是把答案先发给浏览器再问一遍，等于送分。
type AwarenessQuestion struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	CourseID uint   `gorm:"index;not null" json:"courseId"`
	Content  string `gorm:"size:512;not null" json:"content"`
	// Options 选项文本，JSON 数组
	Options string `gorm:"type:text" json:"options"`
	// Answer 正确选项的下标，JSON 数组（多选就是多个）。不出接口
	Answer string `gorm:"size:64" json:"-"`
	// Multi 多选题。单选时提交多个答案一律判错
	Multi bool `gorm:"default:false" json:"multi"`
	// Explain 答案解析，只在交卷之后随结果返回
	Explain   string    `gorm:"size:512" json:"explain"`
	Sort      int       `gorm:"default:0" json:"sort"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AwarenessRecord 某个人在某个培训项上的完成情况。
//
// 一人一项一条（唯一索引），既是「已读确认」的签署记录，也是答题成绩单。
type AwarenessRecord struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	CourseID uint   `gorm:"uniqueIndex:idx_awareness_user_course;not null" json:"courseId"`
	UserID   uint   `gorm:"uniqueIndex:idx_awareness_user_course;not null" json:"userId"`
	Username string `gorm:"size:64" json:"username"`
	// Status pending 未开始 | read 已确认已读（无题目时即完成）| passed 已通过 | failed 答过但没及格
	Status string     `gorm:"size:16;default:pending;index" json:"status"`
	ReadAt *time.Time `json:"readAt"`
	// Attempts 交卷次数；Score 最近一次得分（百分制）
	Attempts int        `gorm:"default:0" json:"attempts"`
	Score    int        `gorm:"default:0" json:"score"`
	PassedAt *time.Time `json:"passedAt"`
	// ClientIP 确认已读时的来源，签署记录要能回答「是谁在哪确认的」
	ClientIP string `gorm:"size:64" json:"clientIp"`
	// Wrong 最近一次答错的题目 ID，JSON 数组；让人知道错在哪，而不是只给个分数
	Wrong     string    `gorm:"size:255" json:"wrong"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ---------- 特征库 ----------

// Signature 一条特征。
//
// 这个模块最容易做成「又一张永远不生效的模式表」，所以定的规矩是：
// **特征必须能落到一个真在跑的检测点上**，否则不收。目前两类：
//   - command：落到命令规则（下发闸门与 Web 终端都在用它拦命令）
//   - port：落到暴露面扫描结果（真机 TCP 探测出来的开放端口）
//
// 特征库负责「判断依据从哪来、灰度到什么程度」，执行仍由原来的检测点做 ——
// 平台不搞两套并行的拦截逻辑。
type Signature struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:128;not null" json:"name"`
	// Kind command | port
	Kind string `gorm:"size:16;default:command;index" json:"kind"`
	// Pattern command 时是 Go 正则；port 时是端口清单（支持 22,80 与 8000-8010 混写）
	Pattern     string `gorm:"size:255;not null" json:"pattern"`
	Description string `gorm:"size:255" json:"description"`
	// Stage 灰度开关：observe 只提醒（命令规则按 warn 应用）| enforce 真拦（按 block 应用）
	Stage string `gorm:"size:16;default:observe" json:"stage"`
	// Severity high | medium | low，只用于展示与排序
	Severity string `gorm:"size:16;default:medium" json:"severity"`
	Enabled  bool   `gorm:"default:true" json:"enabled"`
	// Builtin 内置特征：可以停用、可以改灰度，但不允许删除（删了下次启动又会回来，反而更乱）
	Builtin bool `gorm:"default:false" json:"builtin"`
	// RuleID command 类特征应用后对应的 command_rules 行，0 表示还没应用
	RuleID    uint       `gorm:"default:0" json:"ruleId"`
	AppliedAt *time.Time `json:"appliedAt"`
	AppliedBy string     `gorm:"size:64" json:"appliedBy"`
	CreatedBy uint       `gorm:"index;default:0" json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// ---------- 安全事件与研判 ----------

// SecurityEvent 一条要有人看一眼的安全事件。
//
// 这个模块最容易做成「一张漂亮但永远是空的研判台」，所以定的规矩是：
// **事件只能由采集器从平台已经在产生的真实记录里生成，界面上不提供手工新建**。
// 目前四个来源都是只追加的流水表，采集靠「上次消费到哪个 ID」推进：
//
//	exec-guard  下发闸门拦下的高危命令（exec_guard_logs.status = blocked）
//	terminal    Web 终端里被拦下的命令（session_commands.risk = blocked）
//	exposure    真机 TCP 探测到的未登记开放端口（exposure_scans.unexpected）
//	authz       越权被拒的写操作（audit_logs.status = 403）
//
// 刻意没有的来源：登录失败爆破。登录接口在公开路由上，审计中间件不覆盖它，
// 平台现在**根本没有记录过一次失败登录**，所以这块是诚实的空白，不假装有。
type SecurityEvent struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Fingerprint 同一个「谁 / 从哪 / 对谁 / 干了什么」只留一条，重复只累加次数。
	// 不去重的研判台第二天就没人看了。
	Fingerprint string `gorm:"size:64;uniqueIndex;not null" json:"fingerprint"`
	Source      string `gorm:"size:16;index;not null" json:"source"` // exec-guard | terminal | exposure | authz
	Title       string `gorm:"size:255;not null" json:"title"`
	Severity    string `gorm:"size:16;index;default:warning" json:"severity"` // critical | warning | info

	// 研判要回答的「谁对谁做了什么」。取不到的字段一律留空，不用 unknown 之类的占位
	// 冒充已知 —— 空字段在界面上显示「—」，让人知道这条线索本身缺了。
	Actor    string `gorm:"size:64;index" json:"actor"`   // 发起方：平台账号
	ActorIP  string `gorm:"size:64;index" json:"actorIp"` // 源地址
	Target   string `gorm:"size:255" json:"target"`       // 目标：主机名 / 地址 / 接口路径
	Port     string `gorm:"size:64" json:"port"`
	Protocol string `gorm:"size:16" json:"protocol"` // tcp | ssh | http
	// Evidence 证据摘要：命令原文、端口清单、命中的规则说明。研判就靠它。
	Evidence string `gorm:"type:text" json:"evidence"`
	// RefTable / RefID 溯源到原始流水的最新一条，点进去能看到没被摘要过的原文
	RefTable string `gorm:"size:32" json:"refTable"`
	RefID    uint   `gorm:"default:0" json:"refId"`
	// EventID 升格出来的事件工单号（model.Event），0 表示还没升格。
	//
	// 平台里「派发」只有这一种诚实的落法：把线索交给事件中心那套已经在跑的机制
	// （负责人、处置时间线、SLA 计时），而不是新造一条对外派发链路 ——
	// 没有对接的外部系统，派发页只会是一张永远空着的表。
	EventID uint `gorm:"index;default:0" json:"eventId"`

	HitCount    int       `gorm:"default:1" json:"hitCount"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `gorm:"index" json:"lastSeenAt"`
	// HitsAfterClose 结案之后又命中了多少次。
	//
	// 结案了还在响，要么是判错了，要么是攻击还在继续 —— 这个数字不能被
	// 「已处置」这个状态盖掉，所以单独存一列并在界面上顶出来。
	HitsAfterClose int `gorm:"default:0" json:"hitsAfterClose"`

	// Status new 待研判 | investigating 研判中 | confirmed 确认威胁 | false-positive 误报 |
	// ignored 忽略（是真的，但不用管）| handled 已处置
	Status string `gorm:"size:16;index;default:new" json:"status"`
	// Verdict 研判结论，人填。结案（confirmed 之外的终态）时必须有。
	Verdict   string     `gorm:"type:text" json:"verdict"`
	Owner     string     `gorm:"size:64;index" json:"owner"`
	ClosedAt  *time.Time `json:"closedAt"`
	ClosedBy  string     `gorm:"size:64" json:"closedBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// SecurityEventLog 安全事件的处置链路，只追加不修改
type SecurityEventLog struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	EventID uint `gorm:"index;not null" json:"eventId"`
	// Action collect 采集 | status 研判状态变更 | note 处置记录 | respond 处置动作 | rehit 结案后又命中
	Action    string    `gorm:"size:16" json:"action"`
	Content   string    `gorm:"size:500" json:"content"`
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// SecurityEventMute 误报白名单：被判成误报的指纹进这里，采集器下次直接跳过。
//
// 误报反馈不沉淀的话，同一条噪音每天都会重新冒出来，研判台很快就没人看了。
// 但「跳过」不等于「装作没发生」：命中次数照记，界面上能看到某条白名单
// 这段时间挡掉了多少次，挡得异常多就该回头看是不是当初判错了。
type SecurityEventMute struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Fingerprint string `gorm:"size:64;uniqueIndex;not null" json:"fingerprint"`
	Source      string `gorm:"size:16;index" json:"source"`
	// Title 建白名单时那条事件的标题，只为了让人看得懂这条白名单是干什么的
	Title  string `gorm:"size:255" json:"title"`
	Reason string `gorm:"type:text" json:"reason"`
	// HitCount 建了白名单之后又挡掉多少次
	HitCount  int        `gorm:"default:0" json:"hitCount"`
	LastHitAt *time.Time `json:"lastHitAt"`
	Operator  string     `gorm:"size:64" json:"operator"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// SecuritySuggestionDismissal 被人工「拒绝」的学习建议。
//
// 这张表存在的唯一理由是让「拒绝」这个按钮是真的：建议是每次现算的，
// 不记下来就会下一次刷新又冒出来，那个按钮就成了摆设。
// 记的是建议的稳定标识（Key），不是建议内容 —— 内容会随数据变，标识不变。
type SecuritySuggestionDismissal struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Key 建议的稳定标识，形如 `port_baseline|<targetId>|<port>`
	Key  string `gorm:"size:128;uniqueIndex;not null" json:"key"`
	Kind string `gorm:"size:32;index" json:"kind"`
	// Title 拒绝时那条建议的标题，只为了让人看得懂这条拒绝记录是什么
	Title     string    `gorm:"size:255" json:"title"`
	Reason    string    `gorm:"size:255" json:"reason"`
	Operator  string    `gorm:"size:64" json:"operator"`
	CreatedAt time.Time `json:"createdAt"`
}
