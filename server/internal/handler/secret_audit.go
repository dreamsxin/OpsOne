package handler

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/response"
)


// 密钥落库加密的推广。
//
// 第一轮只加密 credentials 表；第二轮补上 kube_clusters.kubeconfig 与 hosts.secret
// （权限最大的两份）以及 db_instances.secret；这一轮把**对外系统的凭据**也全部纳入：
// LDAP 服务账号口令、IM 应用密钥、云账号 AK/SK、Jenkins Token、模型上游 Key、
// 通知渠道的签名密钥与鉴权头、指标/日志/链路三个数据源的请求头、SMTP 口令。
//
// 补的过程里发现上一轮那份「尚未纳入」清单自己就是不全的：notify_channels.header_value、
// log_sources.header_value、trace_sources.header_value 和 sys_configs 里的 smtp.password
// 四处根本没被登记过 —— 清单漏登记比字段没加密更危险，因为体检页会显得一切正常。
//
// 实现上刻意**不用 GORM 钩子**而是在读写点显式调用：
// 钩子看起来更保险（"没有一条写路径能绕过"），但它在 Save 全列写回、
// Updates(map)、Select(...).Updates(struct) 这几种形态下的行为差别很大，
// 一处理解错就会把密文再加密一次或把明文写回去 —— 那种 bug 在库里是不可逆的。
// 显式调用的代价是「可能漏一条写路径」，所以配一个**加密体检**接口：
// 它直接按前缀数每张表还有多少明文行，漏了就能看见，而不是靠人盯代码。
//
// 解密统一走 openSecret：cryptox.Open 对「没有 enc:v1: 前缀」的值原样返回，
// 所以同一个值多经过一次解密是安全的 —— 这一点让「在读取漏斗上统一解密」成为可行方案。

// secretField 一处需要加密的字段
type secretField struct {
	// Label 界面上的名字
	Label string
	// Table / Column 库里的位置，体检与迁移直接用它拼 SQL
	Table  string
	Column string
	// Where 额外的行过滤条件（不带 WHERE 关键字）。
	// 只为 sys_configs 这种「一张表里混着密钥与普通配置」的情形准备：
	// SMTP 口令是 key='smtp.password' 的那一行，整列加密会把平台名称也加密掉。
	Where string
	// Note 这份密钥的分量，界面上照实说
	Note string
}

// filter 拼出这一项的行过滤条件
func (f secretField) filter() string {
	base := fmt.Sprintf("%s IS NOT NULL AND %s <> ''", f.Column, f.Column)
	if f.Where != "" {
		base += " AND " + f.Where
	}
	return base
}

// secretFields 纳入加密的字段。
//
// 这份清单就是「平台声称加密了哪些密钥」的唯一出处：体检接口按它数明文行，
// 迁移接口按它重写存量数据。往外接一个新系统时，新的密钥列要同时加到这里 ——
// 忘了加，体检页就会漏报，那比不加密更糟。
var secretFields = []secretField{
	{Label: "凭证库", Table: "credentials", Column: "secret", Note: "共享登录凭据的口令 / 私钥"},
	{Label: "凭证库（私钥口令）", Table: "credentials", Column: "passphrase", Note: "带口令私钥的那个口令"},
	{Label: "主机自带凭据", Table: "hosts", Column: "secret", Note: "口令或私钥；引用凭证库的主机这里为空"},
	{Label: "容器集群 kubeconfig", Table: "kube_clusters", Column: "kubeconfig", Note: "含 CA 与客户端私钥，通常等同集群管理员凭据"},
	{Label: "数据库实例凭据", Table: "db_instances", Column: "secret", Note: "只读查询用的库口令"},

	// 以下是「对外系统凭据」这一轮补齐的
	{Label: "LDAP 服务账号口令", Table: "ldap_servers", Column: "bind_password", Note: "用于搜索目录；域账号登录链路也依赖它"},
	{Label: "IM 应用密钥", Table: "im_apps", Column: "app_secret", Note: "能读整个通讯录，也是扫码登录的凭据"},
	{Label: "云账号 AK/SK", Table: "cloud_accounts", Column: "access_key_secret", Note: "目前只登记不使用，仍按凭据对待"},
	{Label: "Jenkins Token", Table: "build_servers", Column: "token", Note: "能触发构建"},
	{Label: "模型上游 Key", Table: "model_upstreams", Column: "api_key", Note: "直接对应额度花费"},
	{Label: "通知渠道签名密钥", Table: "notify_channels", Column: "secret", Note: "钉钉加签 / 飞书签名"},
	{Label: "通知渠道鉴权头", Table: "notify_channels", Column: "header_value", Note: "自建网关的 Token，之前连清单都漏了"},
	{Label: "指标源请求头", Table: "metric_sources", Column: "header_value", Note: "Prometheus 前置网关的令牌"},
	{Label: "日志源请求头", Table: "log_sources", Column: "header_value", Note: "Loki 前置网关的令牌"},
	{Label: "链路源请求头", Table: "trace_sources", Column: "header_value", Note: "Jaeger 前置网关的令牌"},
	{Label: "SMTP 口令", Table: "sys_configs", Column: "value", Where: "`key` = 'smtp.password'",
		Note: "发信账号口令；这一条以前还会被配置接口原样回传给前端"},
}

// pendingSecretFields 仍是明文、这一轮刻意没动的字段。
//
// 留在这里不是忘了，是每一条都有单独的理由，体检页会照实列出来。
var pendingSecretFields = []secretField{
	{Label: "两步验证密钥", Table: "users", Column: "totp_secret",
		Note: "动密钥就动了登录路径：迁移中途出错会让已绑定 2FA 的人全部登不进来，需要单独一轮（含备用码与解绑兜底）"},
	{Label: "告警推送令牌", Table: "alert_sources", Column: "token",
		Note: "这是别人往平台推送时用的令牌，界面上要能看见原文复制给对方，加密了也得解回来展示，收益有限"},
}

// sealSecret 落库前加密。空串原样返回，已经是密文的不再加密（幂等，
// 这点很要紧：Save 是全列写回，同一个值会反复经过这里）。
func (h *Handler) sealSecret(raw string) string {
	if raw == "" || cryptox.IsSealed(raw) {
		return raw
	}
	return h.Crypto.Seal(raw)
}

// openSecret 使用前解密。解不开一律报错并点名是哪个字段 ——
// 拿着乱码去连主机会表现成「认证失败」，把人引向错误的排查方向。
func (h *Handler) openSecret(label, stored string) (string, error) {
	plain, err := h.Crypto.Open(stored)
	if err != nil {
		return "", fmt.Errorf("%s解密失败: %w", label, err)
	}
	return plain, nil
}

// ---------- 体检 ----------

type secretAuditRow struct {
	Label    string `json:"label"`
	Table    string `json:"table"`
	Column   string `json:"column"`
	Note     string `json:"note"`
	Total    int64  `json:"total"`
	Sealed   int64  `json:"sealed"`
	Plain    int64  `json:"plain"`
	Covered  bool   `json:"covered"`
	ReadErr  string `json:"readErr"`
}

// countSecretRows 数某一列里有多少非空值、其中多少是密文。
// 用 LIKE 'enc:v1:%' 判密文：与 cryptox 的前缀约定一致。
func (h *Handler) countSecretRows(field secretField, covered bool) secretAuditRow {
	row := secretAuditRow{
		Label: field.Label, Table: field.Table, Column: field.Column,
		Note: field.Note, Covered: covered,
	}
	base := fmt.Sprintf("SELECT COUNT(1) FROM %s WHERE %s", field.Table, field.filter())
	if err := h.DB.Raw(base).Scan(&row.Total).Error; err != nil {
		// 表还没建（某些模块从未用过）不算故障，如实说明
		row.ReadErr = err.Error()
		return row
	}
	sealed := base + fmt.Sprintf(" AND %s LIKE 'enc:v1:%%'", field.Column)
	if err := h.DB.Raw(sealed).Scan(&row.Sealed).Error; err != nil {
		row.ReadErr = err.Error()
		return row
	}
	row.Plain = row.Total - row.Sealed
	return row
}

// GetSecretAudit 密钥加密体检：每张表还有多少明文行。
//
// 这个接口存在的意义是「显式加密可能漏写路径」的兜底：漏了就会在这里显示成明文行，
// 而不是等到某天数据库被拷走才发现。
func (h *Handler) GetSecretAudit(c *gin.Context) {
	rows := make([]secretAuditRow, 0, len(secretFields)+len(pendingSecretFields))
	var plainTotal int64
	for _, field := range secretFields {
		row := h.countSecretRows(field, true)
		plainTotal += row.Plain
		rows = append(rows, row)
	}
	for _, field := range pendingSecretFields {
		rows = append(rows, h.countSecretRows(field, false))
	}

	note := "已纳入加密的字段里还有明文行，多为加密开关打开之前留下的老数据：点「加密存量数据」重写一遍即可"
	if !h.Crypto.Enabled() {
		note = "未配置 OPS_SECRET_KEY：所有密钥都是明文落库，迁移按钮不会有任何效果（也不会破坏数据）"
	} else if plainTotal == 0 {
		note = "已纳入加密的字段没有明文残留。注意「尚未纳入」的那些字段仍是明文，见下表 Covered 列"
	}
	response.OK(c, gin.H{
		"encryptEnabled": h.Crypto.Enabled(),
		"rows":           rows,
		"plainTotal":     plainTotal,
		"note":           note,
		// 这句是这一页最该被记住的一条：加密挡的是「库文件/备份被拷走」，不是万能的
		"limit": "密钥能被平台解密使用，所以同时拿到数据库与 OPS_SECRET_KEY 的人仍能拿到全部凭据；" +
			"加密挡的是只拿到库文件或备份的那类场景",
	})
}

// MigrateSecrets 把已纳入加密的字段里的明文行重写成密文。
//
// 只处理「非空且没有 enc:v1: 前缀」的行，因此可以反复点（幂等）。
// 不改任何业务语义：值不变，只是换一种存法。
func (h *Handler) MigrateSecrets(c *gin.Context) {
	if !h.Crypto.Enabled() {
		response.BadRequest(c, "未配置 OPS_SECRET_KEY，没有可用的密钥，迁移不会做任何事")
		return
	}

	type result struct {
		Label     string `json:"label"`
		Table      string `json:"table"`
		Column     string `json:"column"`
		Migrated   int    `json:"migrated"`
		Failed     int    `json:"failed"`
		FirstError string `json:"firstError"`
	}
	out := make([]result, 0, len(secretFields))
	totalMigrated := 0

	for _, field := range secretFields {
		item := result{Label: field.Label, Table: field.Table, Column: field.Column}
		type plainRow struct {
			ID    uint
			Value string
		}
		var rows []plainRow
		query := fmt.Sprintf(
			"SELECT id, %s AS value FROM %s WHERE %s AND %s NOT LIKE 'enc:v1:%%'",
			field.Column, field.Table, field.filter(), field.Column)
		if err := h.DB.Raw(query).Scan(&rows).Error; err != nil {
			item.Failed++
			item.FirstError = err.Error()
			out = append(out, item)
			continue
		}
		for _, row := range rows {
			sealed := h.Crypto.Seal(row.Value)
			update := fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", field.Table, field.Column)
			if err := h.DB.Exec(update, sealed, row.ID).Error; err != nil {
				item.Failed++
				if item.FirstError == "" {
					item.FirstError = err.Error()
				}
				continue
			}
			item.Migrated++
			totalMigrated++
		}
		out = append(out, item)
	}

	response.OK(c, gin.H{
		"results": out, "migrated": totalMigrated,
		"note": fmt.Sprintf("重写了 %d 行：值没有变，只是从明文换成 AES-GCM 密文。"+
			"可以反复执行（已是密文的行会被跳过）；"+
			"注意这一步不可逆地依赖 OPS_SECRET_KEY —— 换掉那个值之后这些行就解不开了", totalMigrated),
	})
}


