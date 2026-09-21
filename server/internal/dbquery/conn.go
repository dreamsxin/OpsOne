// Package dbquery 负责连到被纳管的数据库实例上做**只读**的元数据浏览与查询。
//
// 三条底线：
//  1. 只允许只读语句。语句类型用白名单判定，并额外挡掉几种「长得像 SELECT 其实会写」
//     的写法（CTE 里塞 INSERT、MySQL 的 SELECT ... INTO OUTFILE、FOR UPDATE 等）。
//  2. 连接层再加一道：会话设成只读（PG 用 default_transaction_read_only，
//     MySQL 用 SET SESSION TRANSACTION READ ONLY），白名单万一漏了也写不进去。
//  3. 一定有超时与行数上限。查询跑飞会拖垮被查的库，那是生产库。
//
// 只支持 MySQL / MariaDB 与 PostgreSQL。Redis、MongoDB 不在这里处理 —— 它们不是 SQL，
// 硬塞进同一套抽象只会让语义含糊。
package dbquery

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Target 一个待连接的数据库实例
type Target struct {
	Type     string // mysql | postgres
	Address  string
	Port     int
	Username string
	Secret   string
	DBName   string
}

const (
	TypeMySQL    = "mysql"
	TypePostgres = "postgres"
)

// Supported 这个类型能不能连
func Supported(dbType string) bool {
	return dbType == TypeMySQL || dbType == TypePostgres
}

// dsn 拼连接串。只读设置直接写进连接参数，避免「连上了但忘了设只读」。
func (t Target) dsn() (driver, dsn string, err error) {
	host := t.Address
	if host == "" {
		return "", "", fmt.Errorf("地址为空")
	}
	switch t.Type {
	case TypeMySQL:
		db := t.DBName
		// MySQL 的 DSN 允许不带库名（用来列库）
		d := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=8s&readTimeout=30s&writeTimeout=10s&parseTime=false&charset=utf8mb4&interpolateParams=false",
			t.Username, t.Secret, host, t.Port, db)
		return "mysql", d, nil
	case TypePostgres:
		db := t.DBName
		if db == "" {
			db = "postgres" // PG 必须连到某个库上，没填就用默认库
		}
		d := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=prefer connect_timeout=8 default_transaction_read_only=on statement_timeout=30000",
			host, t.Port, t.Username, t.Secret, db)
		return "pgx", d, nil
	default:
		return "", "", fmt.Errorf("暂不支持的数据库类型 %q（只支持 mysql / postgres）", t.Type)
	}
}

// Open 建连接。调用方负责 Close。
func Open(ctx context.Context, t Target) (*sql.DB, error) {
	driver, dsn, err := t.dsn()
	if err != nil {
		return nil, err
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	// 一次查询一条连接够了，别在被查的库上堆连接
	conn.SetMaxOpenConns(2)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if t.Type == TypeMySQL {
		// MySQL 没有连接参数级别的只读开关，只能连上后设会话级
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("设置只读会话失败: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "SET SESSION max_execution_time = 30000"); err != nil {
			// MariaDB 没有这个变量，失败不致命，超时还有 ctx 兜着
			_ = err
		}
	}
	return conn, nil
}

// Probe 连通性探测：真连一次并取版本号，替代原来只拨 TCP 端口的做法。
func Probe(ctx context.Context, t Target) (version string, err error) {
	conn, err := Open(ctx, t)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	q := "SELECT VERSION()"
	if t.Type == TypePostgres {
		q = "SELECT version()"
	}
	if err := conn.QueryRowContext(ctx, q).Scan(&version); err != nil {
		return "", err
	}
	return strings.TrimSpace(version), nil
}
