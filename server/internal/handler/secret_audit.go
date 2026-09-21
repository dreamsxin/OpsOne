package handler

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/response"
)


// 密钥落库加密的推广。
//
// 凭证库那一轮只加密了 credentials 表。真正权限最大的两份凭据其实在别处：
// kube_clusters.kubeconfig（通常等于 cluster-admin）和 hosts.secret。这一轮把它们
// 和 db_instances.secret 一起纳入同一套 AES-GCM 加密。
//
// 实现上刻意**不用 GORM 钩子**而是在读写点显式调用：
// 钩子看起来更保险（"没有一条写路径能绕过"），但它在 Save 全列写回、
// Updates(map)、Select(...).Updates(struct) 这几种形态下的行为差别很大，
// 一处理解错就会把密文再加密一次或把明文写回去 —— 那种 bug 在库里是不可逆的。
// 显式调用的代价是「可能漏一条写路径」，所以配一个**加密体检**接口：
// 它直接按前缀数每张表还有多少明文行，漏了就能看见，而不是靠人盯代码。

// secretField 一处需要加密的字段
type secretField struct {
	// Label 界面上的名字
	Label string
	// Table / Column 库里的位置，体检与迁移直接用它拼 SQL
	Table  string
	Column string
	// Note 这份密钥的分量，界面上照实说
	Note string
}

// secretFields 目前纳入加密的字段。
//
// 没纳入的（LDAP 服务账号口令、IM AppSecret、Jenkins Token、云账号 AK/SK、
// 模型上游 Key、通知渠道的签名密钥、监控接入源的请求头）体检接口里会一并列出来，
// 标成「尚未纳入」—— 让人看得见还差哪些，而不是以为已经全加密了。
var secretFields = []secretField{
	{Label: "凭证库", Table: "credentials", Column: "secret", Note: "共享登录凭据的口令 / 私钥"},
	{Label: "凭证库（私钥口令）", Table: "credentials", Column: "passphrase", Note: "带口令私钥的那个口令"},
	{Label: "主机自带凭据", Table: "hosts", Column: "secret", Note: "口令或私钥；引用凭证库的主机这里为空"},
	{Label: "容器集群 kubeconfig", Table: "kube_clusters", Column: "kubeconfig", Note: "含 CA 与客户端私钥，通常等同集群管理员凭据"},
	{Label: "数据库实例凭据", Table: "db_instances", Column: "secret", Note: "只读查询用的库口令"},
}

// pendingSecretFields 还没纳入加密的字段，体检里如实列出来
var pendingSecretFields = []secretField{
	{Label: "LDAP 服务账号口令", Table: "ldap_servers", Column: "bind_password", Note: "只用于搜索目录"},
	{Label: "IM 应用密钥", Table: "im_apps", Column: "app_secret", Note: "能读整个通讯录"},
	{Label: "云账号 AK/SK", Table: "cloud_accounts", Column: "access_key_secret", Note: "目前只登记不使用"},
	{Label: "Jenkins Token", Table: "build_servers", Column: "token", Note: "能触发构建"},
	{Label: "模型上游 Key", Table: "model_upstreams", Column: "api_key", Note: "直接对应额度花费"},
	{Label: "通知渠道签名密钥", Table: "notify_channels", Column: "secret", Note: "钉钉加签 / 飞书签名"},
	{Label: "监控接入源请求头", Table: "metric_sources", Column: "header_value", Note: "Prometheus 网关令牌"},
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
	base := fmt.Sprintf("SELECT COUNT(1) FROM %s WHERE %s IS NOT NULL AND %s <> ''",
		field.Table, field.Column, field.Column)
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
			"SELECT id, %s AS value FROM %s WHERE %s IS NOT NULL AND %s <> '' AND %s NOT LIKE 'enc:v1:%%'",
			field.Column, field.Table, field.Column, field.Column, field.Column)
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


