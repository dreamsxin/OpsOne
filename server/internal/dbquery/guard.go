package dbquery

import (
	"fmt"
	"regexp"
	"strings"
)

// 语句守卫：判断一条 SQL 能不能在「只读查询」里执行。
//
// 思路是白名单 + 明确的黑名单补丁，而不是试图解析 SQL —— 真解析要引入一个方言完整的
// parser，维护成本远高于收益，而且解析器自身的差异也会成为绕过点。
// 这里的定位是「把明显的写操作和常见的绕过写法挡在门外」，真正的兜底是连接层的只读会话。

var (
	// 允许的起始关键字：都是读
	allowedHeads = []string{"select", "with", "show", "explain", "describe", "desc", "analyze"}

	// 长得像读、其实会写或会落文件的写法
	dangerPatterns = []struct {
		re     *regexp.Regexp
		reason string
	}{
		{regexp.MustCompile(`(?is)\binto\s+outfile\b`), "SELECT ... INTO OUTFILE 会往数据库服务器落文件"},
		{regexp.MustCompile(`(?is)\binto\s+dumpfile\b`), "SELECT ... INTO DUMPFILE 会往数据库服务器落文件"},
		{regexp.MustCompile(`(?is)\bfor\s+update\b`), "SELECT ... FOR UPDATE 会加写锁，可能阻塞业务"},
		{regexp.MustCompile(`(?is)\block\s+in\s+share\s+mode\b`), "LOCK IN SHARE MODE 会加锁"},
		{regexp.MustCompile(`(?is)\bpg_read_file\b|\bpg_ls_dir\b|\bpg_read_binary_file\b`), "读取数据库服务器本地文件的函数不允许使用"},
		{regexp.MustCompile(`(?is)\bcopy\b.*\bfrom\b`), "COPY ... FROM 会写入数据"},
		{regexp.MustCompile(`(?is)\bcopy\b.*\bto\b.*\bprogram\b`), "COPY ... TO PROGRAM 会在数据库服务器上执行命令"},
		{regexp.MustCompile(`(?is)\bdblink\b|\bpg_sleep\s*\(`), "dblink / pg_sleep 这类函数不允许使用"},
		{regexp.MustCompile(`(?is)\bbenchmark\s*\(|\bsleep\s*\(`), "sleep / benchmark 这类拖时间的函数不允许使用"},
		// WITH ... 里藏写操作：PG 支持 data-modifying CTE，白名单会被绕过
		{regexp.MustCompile(`(?is)^\s*with\b[\s\S]*\b(insert|update|delete|merge)\b`), "CTE 里包含写操作（PostgreSQL 允许 WITH ... INSERT）"},
	}

	// 明确的写/改语句，用来给出更准确的报错（不然只会说「不是只读语句」）
	writeHeads = map[string]string{
		"insert": "写入", "update": "更新", "delete": "删除", "merge": "合并写入",
		"drop": "删除对象", "create": "创建对象", "alter": "修改结构", "truncate": "清空表",
		"grant": "授权", "revoke": "回收权限", "set": "改会话变量", "call": "调用存储过程",
		"do": "执行匿名块", "begin": "开启事务", "commit": "提交事务", "rollback": "回滚",
		"vacuum": "整理表空间", "reindex": "重建索引", "kill": "杀会话", "load": "导入数据",
		"replace": "写入", "rename": "重命名", "flush": "刷新缓存", "reset": "重置",
		"lock": "加锁", "unlock": "解锁", "optimize": "重建表", "repair": "修表",
		"copy": "导入/导出数据", "import": "导入数据", "export": "导出数据",
	}

	commentBlock = regexp.MustCompile(`(?s)/\*.*?\*/`)
	commentLine  = regexp.MustCompile(`(?m)(--|#)[^\n]*$`)
)

// Guard 检查语句是否允许执行。返回归一化后的语句（去注释、去尾分号）与错误。
func Guard(raw string) (string, error) {
	// 先剥注释：`/*! MySQL 版本注释 */` 这类里面是能塞语句的
	stripped := commentBlock.ReplaceAllString(raw, " ")
	stripped = commentLine.ReplaceAllString(stripped, " ")
	stmt := strings.TrimSpace(stripped)
	stmt = strings.TrimRight(stmt, "; \t\r\n")
	if stmt == "" {
		return "", fmt.Errorf("语句为空")
	}

	// 多语句：去掉结尾分号后还有分号，就可能是 `select 1; drop table x`
	if strings.Contains(stmt, ";") {
		return "", fmt.Errorf("一次只能执行一条语句（检测到分号）")
	}

	lower := strings.ToLower(stmt)
	head := firstWord(lower)
	if verb, isWrite := writeHeads[head]; isWrite {
		return "", fmt.Errorf("这是%s语句（%s），只读查询里不允许执行", verb, strings.ToUpper(head))
	}
	if !hasHead(head) {
		return "", fmt.Errorf("只允许 SELECT / WITH / SHOW / EXPLAIN / DESCRIBE 开头的只读语句，当前是 %q", head)
	}
	for _, p := range dangerPatterns {
		if p.re.MatchString(stmt) {
			return "", fmt.Errorf("语句被拦截：%s", p.reason)
		}
	}
	return stmt, nil
}

func firstWord(lower string) string {
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '(' || r == ';'
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func hasHead(head string) bool {
	for _, allow := range allowedHeads {
		if head == allow {
			return true
		}
	}
	return false
}

// WithLimit 给没写 LIMIT 的 SELECT 补一个上限。
// 不改写语义、只兜底：忘了加 LIMIT 的一条 `select * from big_table` 足够把平台和库一起拖垮。
func WithLimit(stmt string, limit int) string {
	lower := strings.ToLower(stmt)
	head := firstWord(lower)
	if head != "select" && head != "with" {
		return stmt // SHOW / EXPLAIN 这类不支持 LIMIT
	}
	if strings.Contains(lower, " limit ") || strings.HasSuffix(lower, " limit") {
		return stmt
	}
	// 有 FETCH FIRST n ROWS 的写法也算已经限制了
	if strings.Contains(lower, "fetch first") || strings.Contains(lower, "fetch next") {
		return stmt
	}
	return fmt.Sprintf("%s LIMIT %d", stmt, limit)
}
