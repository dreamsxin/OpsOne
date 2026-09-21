package dbquery

import (
	"strings"
	"testing"
)

// 守卫是这条链路的第一道闸，允许错放（连接层还有只读会话兜着），但不允许漏拦。
func TestGuardAllowsReadOnly(t *testing.T) {
	cases := []string{
		"select 1",
		"SELECT * FROM users WHERE id = 3",
		"  select name from t  ;  ",
		"with recent as (select * from orders limit 10) select count(*) from recent",
		"show tables",
		"SHOW VARIABLES LIKE 'max_connections'",
		"explain select * from users",
		"describe users",
		"desc users",
		"select * from t -- 这是注释",
		"select /* 内联注释 */ 1",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := Guard(raw); err != nil {
				t.Fatalf("应当允许，却被拦下: %v", err)
			}
		})
	}
}

func TestGuardBlocksWrites(t *testing.T) {
	cases := []struct {
		stmt string
		want string
	}{
		{"insert into t values (1)", "写入"},
		{"INSERT INTO t VALUES (1)", "写入"},
		{"update t set a = 1", "更新"},
		{"delete from t", "删除"},
		{"drop table t", "删除对象"},
		{"truncate table t", "清空表"},
		{"alter table t add column c int", "修改结构"},
		{"create table t (id int)", "创建对象"},
		{"grant all on t to u", "授权"},
		{"set global max_connections = 1", "改会话变量"},
		{"call some_proc()", "调用存储过程"},
		{"begin", "开启事务"},
		{"vacuum full", "整理表空间"},
		{"load data infile '/tmp/x' into table t", "导入数据"},
		{"replace into t values (1)", "写入"},
		{"kill 123", "杀会话"},
	}
	for _, tc := range cases {
		t.Run(tc.stmt, func(t *testing.T) {
			_, err := Guard(tc.stmt)
			if err == nil {
				t.Fatal("写操作竟然被放过了")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("报错应当说明是什么操作（%s），实际: %v", tc.want, err)
			}
		})
	}
}

// 这些是「长得像 SELECT 其实危险」的写法，最容易漏
func TestGuardBlocksSneakyReads(t *testing.T) {
	cases := []struct {
		name string
		stmt string
		want string
	}{
		{"CTE 里藏 INSERT", "with x as (insert into t values (1) returning *) select * from x", "CTE"},
		{"CTE 里藏 UPDATE", "WITH upd AS (UPDATE t SET a=1 RETURNING *) SELECT * FROM upd", "CTE"},
		{"落文件 OUTFILE", "select * from t into outfile '/tmp/x'", "落文件"},
		{"落文件 DUMPFILE", "select a from t into dumpfile '/tmp/x'", "落文件"},
		{"加写锁", "select * from t for update", "写锁"},
		{"共享锁", "select * from t lock in share mode", "加锁"},
		{"读服务器文件", "select pg_read_file('/etc/passwd')", "本地文件"},
		{"列目录", "select pg_ls_dir('/')", "本地文件"},
		{"COPY FROM", "copy t from '/tmp/x'", "数据"},
		{"sleep 拖时间", "select sleep(30)", "拖时间"},
		{"benchmark", "select benchmark(1000000, md5('x'))", "拖时间"},
		{"多语句", "select 1; drop table t", "一次只能执行一条"},
		{"注释里藏第二条", "select 1 /* x */ ; delete from t", "一次只能执行一条"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Guard(tc.stmt)
			if err == nil {
				t.Fatalf("这条应当被拦下: %s", tc.stmt)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("拦截原因应当提到 %q，实际: %v", tc.want, err)
			}
		})
	}
}

func TestGuardEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", ";", "-- 只有注释", "/* 只有注释 */"} {
		if _, err := Guard(raw); err == nil {
			t.Fatalf("空语句应当报错: %q", raw)
		}
	}
}

// MySQL 的 /*! ... */ 版本注释里可以藏语句，剥注释这一步不能漏
func TestGuardStripsVersionComment(t *testing.T) {
	if _, err := Guard("/*!40101 delete from t */"); err == nil {
		t.Fatal("版本注释里的写操作应当被拦下")
	}
}

func TestWithLimit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"没有 limit 就补", "select * from t", "select * from t LIMIT 100"},
		{"已有 limit 不动", "select * from t limit 5", "select * from t limit 5"},
		{"大写 LIMIT 也认", "select * from t LIMIT 5", "select * from t LIMIT 5"},
		{"fetch first 也算已限制", "select * from t fetch first 10 rows only", "select * from t fetch first 10 rows only"},
		{"WITH 开头也补", "with x as (select 1) select * from x", "with x as (select 1) select * from x LIMIT 100"},
		{"show 不补", "show tables", "show tables"},
		{"explain 不补", "explain select 1", "explain select 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithLimit(tc.in, 100); got != tc.want {
				t.Fatalf("期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

func TestSupported(t *testing.T) {
	for _, ok := range []string{TypeMySQL, TypePostgres} {
		if !Supported(ok) {
			t.Errorf("%s 应当支持", ok)
		}
	}
	for _, no := range []string{"redis", "mongo", "other", ""} {
		if Supported(no) {
			t.Errorf("%s 不该被当成支持的类型", no)
		}
	}
}

func TestTargetDSN(t *testing.T) {
	my := Target{Type: TypeMySQL, Address: "10.0.0.1", Port: 3306, Username: "u", Secret: "p", DBName: "app"}
	driver, dsn, err := my.dsn()
	if err != nil || driver != "mysql" {
		t.Fatalf("mysql dsn 出错: %v / %s", err, driver)
	}
	if !strings.Contains(dsn, "tcp(10.0.0.1:3306)/app") {
		t.Fatalf("mysql dsn 不对: %s", dsn)
	}

	pg := Target{Type: TypePostgres, Address: "10.0.0.2", Port: 5432, Username: "u", Secret: "p"}
	driver, dsn, err = pg.dsn()
	if err != nil || driver != "pgx" {
		t.Fatalf("pg dsn 出错: %v / %s", err, driver)
	}
	// 没填库名要回落到 postgres，且必须带上只读与超时
	for _, want := range []string{"dbname=postgres", "default_transaction_read_only=on", "statement_timeout=30000"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("pg dsn 少了 %q: %s", want, dsn)
		}
	}

	if _, _, err := (Target{Type: "redis", Address: "x", Port: 1}).dsn(); err == nil {
		t.Error("redis 应当报不支持")
	}
	if _, _, err := (Target{Type: TypeMySQL, Port: 3306}).dsn(); err == nil {
		t.Error("地址为空应当报错")
	}
}
