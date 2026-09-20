package handler

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// hostCSVHeader 导入导出共用的列顺序。
//
// secret 只在导入时读取，导出永远为空：主机凭据不随导出文件外流。
var hostCSVHeader = []string{
	"name", "address", "port", "username", "authType", "secret",
	"env", "tags", "proxyHost", "remark",
}

const (
	// hostImportMaxRows 单次导入的行数上限，防止一次几万行把库和内存打满
	hostImportMaxRows = 500
	// hostImportMaxBytes 上传文件大小上限
	hostImportMaxBytes = 2 << 20
)

// utf8BOM Excel 打开 CSV 时靠 BOM 判断编码，不带的话中文会乱码
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// HostImportTemplate 下载导入模板（含两行示例）
func (h *Handler) HostImportTemplate(c *gin.Context) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="host-import-template.csv"`)
	_, _ = c.Writer.Write(utf8BOM)

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write(hostCSVHeader)
	_ = writer.Write([]string{
		"web-01", "10.0.1.10", "22", "root", "password", "登录密码",
		"prod", "web,nginx", "", "示例行，导入前请删除",
	})
	_ = writer.Write([]string{
		"db-01", "10.0.1.20", "22", "ops", "key",
		"-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----",
		"prod", "mysql", "jump-01", "私钥整段放在一个单元格里，CSV 会用引号包住换行",
	})
	writer.Flush()
}

// ExportHosts 导出数据权限内的主机清单。
//
// secret 列恒为空：导出文件常被转发、归档，凭据不跟着走。
func (h *Handler) ExportHosts(c *gin.Context) {
	var hosts []model.Host
	q := h.applyScopeWithGrants(h.DB.Model(&model.Host{}), middleware.CurrentUser(c), "host")
	if env := c.Query("env"); env != "" {
		q = q.Where("env = ?", env)
	}
	if err := q.Order("id asc").Find(&hosts).Error; err != nil {
		response.Error(c, "导出失败")
		return
	}

	// 跳板机要导出成名字，先把用到的补齐
	nameByID := map[uint]string{}
	for _, host := range hosts {
		nameByID[host.ID] = host.Name
	}
	missing := make([]uint, 0, 4)
	for _, host := range hosts {
		if host.ProxyHostID != 0 && nameByID[host.ProxyHostID] == "" {
			missing = append(missing, host.ProxyHostID)
		}
	}
	if len(missing) > 0 {
		var proxies []model.Host
		h.DB.Where("id IN ?", missing).Find(&proxies)
		for _, proxy := range proxies {
			nameByID[proxy.ID] = proxy.Name
		}
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="hosts.csv"`)
	_, _ = c.Writer.Write(utf8BOM)

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write(hostCSVHeader)
	for _, host := range hosts {
		_ = writer.Write([]string{
			host.Name, host.Address, strconv.Itoa(host.Port), host.Username,
			host.AuthType, "", host.Env, host.Tags,
			nameByID[host.ProxyHostID], host.Remark,
		})
	}
	writer.Flush()
}

// hostImportRow 一行的处理结果
type hostImportRow struct {
	Line    int    `json:"line"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Action  string `json:"action"` // create | update | skip
	Reason  string `json:"reason"`
}

// ImportHosts 从 CSV 批量导入主机。
//
// 以 address + port 判定是否同一台机器：已存在则更新（secret 留空表示不改），
// 不存在则新建（必须给 secret）。dryRun=true 时只校验不落库，用来先看报告。
func (h *Handler) ImportHosts(c *gin.Context) {
	dryRun := c.Query("dryRun") == "true" || c.PostForm("dryRun") == "true"

	uploaded, openErr := c.FormFile("file")
	if openErr != nil {
		response.BadRequest(c, "请上传 CSV 文件（表单字段名 file）")
		return
	}
	if uploaded.Size > hostImportMaxBytes {
		response.BadRequest(c, fmt.Sprintf("文件最大 %d MB", hostImportMaxBytes>>20))
		return
	}

	opened, openErr := uploaded.Open()
	if openErr != nil {
		response.BadRequest(c, "读取文件失败")
		return
	}
	defer opened.Close()

	raw, readErr := io.ReadAll(io.LimitReader(opened, hostImportMaxBytes))
	if readErr != nil {
		response.BadRequest(c, "读取文件失败")
		return
	}
	// 去掉 Excel 保存时带上的 BOM，否则第一列列名会带前缀匹配不上
	raw = bytes.TrimPrefix(raw, utf8BOM)

	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1 // 允许行尾少列，缺的按空处理
	records, csvErr := reader.ReadAll()
	if csvErr != nil {
		response.BadRequest(c, "CSV 解析失败: "+csvErr.Error())
		return
	}
	if len(records) < 2 {
		response.BadRequest(c, "文件里没有数据行")
		return
	}

	index, headerErr := mapCSVHeader(records[0])
	if headerErr != nil {
		response.BadRequest(c, headerErr.Error())
		return
	}
	if len(records)-1 > hostImportMaxRows {
		response.BadRequest(c, fmt.Sprintf("单次最多导入 %d 行，当前 %d 行", hostImportMaxRows, len(records)-1))
		return
	}

	user := middleware.CurrentUser(c)
	rows := make([]hostImportRow, 0, len(records)-1)
	created, updated, skipped := 0, 0, 0

	// 跳板机按名字解析，先缓存起来避免逐行查库
	proxyCache := map[string]uint{}

	for i, record := range records[1:] {
		line := i + 2 // 表头算第 1 行
		get := func(key string) string {
			pos, ok := index[key]
			if !ok || pos >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[pos])
		}

		name, address := get("name"), get("address")
		row := hostImportRow{Line: line, Name: name, Address: address, Action: "skip"}

		if name == "" && address == "" {
			continue // 整行空白，跳过不计
		}
		if name == "" || address == "" {
			row.Reason = "name 与 address 都不能为空"
			rows, skipped = append(rows, row), skipped+1
			continue
		}

		port := 22
		if raw := get("port"); raw != "" {
			parsed, convErr := strconv.Atoi(raw)
			if convErr != nil || parsed < 1 || parsed > 65535 {
				row.Reason = "port 必须是 1-65535 的数字"
				rows, skipped = append(rows, row), skipped+1
				continue
			}
			port = parsed
		}

		username := get("username")
		if username == "" {
			username = "root"
		}

		authType := strings.ToLower(get("authType"))
		switch authType {
		case "", "password":
			authType = "password"
		case "key":
		default:
			row.Reason = "authType 只能是 password 或 key"
			rows, skipped = append(rows, row), skipped+1
			continue
		}

		env := strings.ToLower(get("env"))
		switch env {
		case "", "dev":
			env = "dev"
		case "test", "prod":
		default:
			row.Reason = "env 只能是 dev / test / prod"
			rows, skipped = append(rows, row), skipped+1
			continue
		}

		var proxyID uint
		if proxyName := get("proxyHost"); proxyName != "" {
			if cached, ok := proxyCache[proxyName]; ok {
				proxyID = cached
			} else {
				var proxy model.Host
				if err := h.DB.Where("name = ?", proxyName).First(&proxy).Error; err != nil {
					row.Reason = "跳板机不存在: " + proxyName
					rows, skipped = append(rows, row), skipped+1
					continue
				}
				proxyID = proxy.ID
				proxyCache[proxyName] = proxy.ID
			}
		}

		secret := get("secret")
		var exist model.Host
		found := h.DB.Where("address = ? AND port = ?", address, port).First(&exist).Error == nil

		if !found && secret == "" {
			row.Reason = "新建主机必须提供 secret（密码或私钥）"
			rows, skipped = append(rows, row), skipped+1
			continue
		}
		if found && proxyID == exist.ID {
			row.Reason = "跳板机不能是自己"
			rows, skipped = append(rows, row), skipped+1
			continue
		}

		if found {
			row.Action = "update"
			row.Reason = fmt.Sprintf("按 %s:%d 匹配到已有主机 #%d", address, port, exist.ID)
			if secret == "" {
				row.Reason += "，secret 留空保持原凭据"
			}
			updated++
			if !dryRun {
				updates := map[string]any{
					"name": name, "username": username, "auth_type": authType,
					"env": env, "tags": get("tags"), "remark": get("remark"),
					"proxy_host_id": proxyID,
				}
				if secret != "" {
					updates["secret"] = secret
				}
				h.DB.Model(&model.Host{}).Where("id = ?", exist.ID).Updates(updates)
			}
		} else {
			row.Action = "create"
			created++
			if dryRun {
				row.Reason = fmt.Sprintf("将新建主机（%s:%d）", address, port)
			} else {
				host := model.Host{
					Name: name, Address: address, Port: port, Username: username,
					AuthType: authType, Secret: secret, Env: env, Tags: get("tags"),
					Remark: get("remark"), Status: "unknown", ProxyHostID: proxyID,
					DeptID: user.DeptID, CreatedBy: user.ID,
				}
				if err := h.DB.Create(&host).Error; err != nil {
					row.Action, row.Reason = "skip", "写入失败: "+err.Error()
					created--
					skipped++
				} else {
					row.Reason = fmt.Sprintf("新建主机 #%d，归属部门沿用导入人（%d）", host.ID, user.DeptID)
				}
			}
		}
		rows = append(rows, row)
	}

	detail := fmt.Sprintf("共 %d 行数据：新建 %d，更新 %d，跳过 %d", len(rows), created, updated, skipped)
	if dryRun {
		detail = "【试运行，未写入】" + detail
	}
	response.OK(c, gin.H{
		"dryRun": dryRun, "total": len(rows),
		"created": created, "updated": updated, "skipped": skipped,
		"rows": rows, "detail": detail,
	})
}

// mapCSVHeader 把表头映射成列下标，name/address 必须存在
func mapCSVHeader(header []string) (map[string]int, error) {
	index := map[string]int{}
	for i, column := range header {
		key := strings.TrimSpace(strings.TrimPrefix(column, string(utf8BOM)))
		index[key] = i
	}
	for _, required := range []string{"name", "address"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("表头缺少必需列 %s，建议先下载模板", required)
		}
	}
	return index, nil
}
