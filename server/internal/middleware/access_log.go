package middleware

// 访问日志。
//
// # 为什么不用 gin.Logger()
//
// gin 默认的 formatter 会把 raw query 拼在 path 后面一起打出来，而平台为
// WebSocket 放行了 `?access_token=<JWT>`（浏览器的 WebSocket API 不支持自定义头，
// 这是没得选的做法）。两件事凑起来的后果是：**每次连终端，一个当前有效的
// 登录令牌就被写进访问日志**。以前只落 stderr，上一轮加了 OPS_LOG_FILE 之后
// 它会落盘并按保留份数留很久 —— 日志文件的读取权限通常比令牌本身宽得多。
//
// 所以这里自己写 formatter：查询串里的敏感参数一律换成占位串，其余原样保留
// （query 对排查问题有用，不能整个丢掉）。

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// sensitiveQueryKeys 需要在日志里脱敏的查询参数名（小写，子串匹配）
var sensitiveQueryKeys = []string{"access_token", "token", "password", "secret", "code", "key"}

// AccessLog 访问日志中间件，写到 out。
func AccessLog(out io.Writer) gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Output: out,
		Formatter: func(p gin.LogFormatterParams) string {
			return fmt.Sprintf("[GIN] %s | %3d | %13v | %15s | %-7s %s\n",
				p.TimeStamp.Format("2006/01/02 - 15:04:05"),
				p.StatusCode, p.Latency, p.ClientIP, p.Method,
				RedactQuery(p.Path))
		},
	})
}

// RedactQuery 把 URL 里的敏感查询参数换成占位串。
//
// 解析失败时**整段查询串都丢掉**而不是原样输出：解析失败通常意味着有奇怪的编码，
// 那种情况下「宁可少一点信息」比「可能把令牌原样打出去」好。
func RedactQuery(path string) string {
	idx := strings.IndexByte(path, '?')
	if idx < 0 {
		return path
	}
	base, raw := path[:idx], path[idx+1:]
	values, err := url.ParseQuery(raw)
	if err != nil {
		return base + "?<查询串解析失败，已省略>"
	}
	changed := false
	for key := range values {
		if isSensitiveQueryKey(key) {
			values.Set(key, "***已脱敏***")
			changed = true
		}
	}
	if !changed {
		return path
	}
	return base + "?" + values.Encode()
}

func isSensitiveQueryKey(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range sensitiveQueryKeys {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
