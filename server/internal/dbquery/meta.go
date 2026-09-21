package dbquery

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---------- 元数据 ----------

// Schema 一个库 / 模式
type Schema struct {
	Name   string `json:"name"`
	Tables int    `json:"tables"`
}

// Table 表（含视图）
type Table struct {
	Schema  string `json:"schema"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`    // table | view
	Rows    int64  `json:"rows"`    // 估算值，不保证准确
	Comment string `json:"comment"` // MySQL 有，PG 用 obj_description
}

// Column 列
type Column struct {
	Name     string `json:"name"`
	DataType string `json:"dataType"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default"`
	IsPK     bool   `json:"isPk"`
	Comment  string `json:"comment"`
}

// Index 索引
type Index struct {
	Name    string `json:"name"`
	Columns string `json:"columns"`
	Unique  bool   `json:"unique"`
}

// ListSchemas 列出库 / 模式（跳过系统库）
func ListSchemas(ctx context.Context, conn *sql.DB, dbType string) ([]Schema, error) {
	var q string
	switch dbType {
	case TypeMySQL:
		q = `SELECT s.schema_name, COALESCE(t.cnt, 0)
		     FROM information_schema.schemata s
		     LEFT JOIN (SELECT table_schema, COUNT(*) cnt FROM information_schema.tables GROUP BY table_schema) t
		            ON t.table_schema = s.schema_name
		     WHERE s.schema_name NOT IN ('information_schema','performance_schema','mysql','sys')
		     ORDER BY s.schema_name`
	case TypePostgres:
		q = `SELECT n.nspname, COALESCE(t.cnt, 0)
		     FROM pg_namespace n
		     LEFT JOIN (SELECT schemaname, COUNT(*) cnt FROM pg_tables GROUP BY schemaname) t
		            ON t.schemaname = n.nspname
		     WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
		     ORDER BY n.nspname`
	default:
		return nil, fmt.Errorf("不支持的类型 %s", dbType)
	}

	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schema
	for rows.Next() {
		var s Schema
		if err := rows.Scan(&s.Name, &s.Tables); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListTables 列出某个库 / 模式下的表与视图
func ListTables(ctx context.Context, conn *sql.DB, dbType, schema string) ([]Table, error) {
	var q string
	switch dbType {
	case TypeMySQL:
		q = `SELECT table_schema, table_name,
		            CASE WHEN table_type = 'VIEW' THEN 'view' ELSE 'table' END,
		            COALESCE(table_rows, 0), COALESCE(table_comment, '')
		     FROM information_schema.tables WHERE table_schema = ?
		     ORDER BY table_name`
	case TypePostgres:
		q = `SELECT c.relnamespace::regnamespace::text, c.relname,
		            CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'view' ELSE 'table' END,
		            GREATEST(c.reltuples::bigint, 0),
		            COALESCE(obj_description(c.oid), '')
		     FROM pg_class c
		     WHERE c.relnamespace::regnamespace::text = $1 AND c.relkind IN ('r','p','v','m')
		     ORDER BY c.relname`
	default:
		return nil, fmt.Errorf("不支持的类型 %s", dbType)
	}

	rows, err := conn.QueryContext(ctx, q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Kind, &t.Rows, &t.Comment); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DescribeTable 取列与索引
func DescribeTable(ctx context.Context, conn *sql.DB, dbType, schema, table string) ([]Column, []Index, error) {
	var colQ, idxQ string
	switch dbType {
	case TypeMySQL:
		colQ = `SELECT column_name, column_type, is_nullable, COALESCE(column_default,''),
		               CASE WHEN column_key = 'PRI' THEN 1 ELSE 0 END, COALESCE(column_comment,'')
		        FROM information_schema.columns WHERE table_schema = ? AND table_name = ?
		        ORDER BY ordinal_position`
		idxQ = `SELECT index_name, GROUP_CONCAT(column_name ORDER BY seq_in_index), MIN(non_unique)
		        FROM information_schema.statistics WHERE table_schema = ? AND table_name = ?
		        GROUP BY index_name ORDER BY index_name`
	case TypePostgres:
		colQ = `SELECT a.attname, format_type(a.atttypid, a.atttypmod),
		               CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
		               COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
		               CASE WHEN EXISTS (
		                 SELECT 1 FROM pg_index i
		                 WHERE i.indrelid = a.attrelid AND i.indisprimary AND a.attnum = ANY(i.indkey)
		               ) THEN 1 ELSE 0 END,
		               COALESCE(col_description(a.attrelid, a.attnum), '')
		        FROM pg_attribute a
		        LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		        WHERE a.attrelid = ($1 || '.' || $2)::regclass AND a.attnum > 0 AND NOT a.attisdropped
		        ORDER BY a.attnum`
		idxQ = `SELECT i.relname,
		               array_to_string(array_agg(a.attname ORDER BY x.ord), ','),
		               CASE WHEN ix.indisunique THEN 0 ELSE 1 END
		        FROM pg_index ix
		        JOIN pg_class i ON i.oid = ix.indexrelid
		        JOIN unnest(ix.indkey) WITH ORDINALITY AS x(attnum, ord) ON true
		        JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = x.attnum
		        WHERE ix.indrelid = ($1 || '.' || $2)::regclass
		        GROUP BY i.relname, ix.indisunique ORDER BY i.relname`
	default:
		return nil, nil, fmt.Errorf("不支持的类型 %s", dbType)
	}

	cols, err := scanColumns(ctx, conn, colQ, schema, table)
	if err != nil {
		return nil, nil, err
	}
	idx, err := scanIndexes(ctx, conn, idxQ, schema, table)
	if err != nil {
		return cols, nil, err
	}
	return cols, idx, nil
}

func scanColumns(ctx context.Context, conn *sql.DB, q, schema, table string) ([]Column, error) {
	rows, err := conn.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var c Column
		var nullable string
		var pk int
		if err := rows.Scan(&c.Name, &c.DataType, &nullable, &c.Default, &pk, &c.Comment); err != nil {
			return nil, err
		}
		c.Nullable = strings.EqualFold(nullable, "YES")
		c.IsPK = pk == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanIndexes(ctx context.Context, conn *sql.DB, q, schema, table string) ([]Index, error) {
	rows, err := conn.QueryContext(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Index
	for rows.Next() {
		var i Index
		var nonUnique int
		if err := rows.Scan(&i.Name, &i.Columns, &nonUnique); err != nil {
			return nil, err
		}
		i.Unique = nonUnique == 0
		out = append(out, i)
	}
	return out, rows.Err()
}

// ---------- 查询 ----------

// Result 一次只读查询的结果
type Result struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Truncated 是否因为行数上限被截断
	Truncated bool   `json:"truncated"`
	CostMs    int64  `json:"costMs"`
	Statement string `json:"statement"` // 实际下发的语句（可能补了 LIMIT）
}

// Query 执行只读查询。调用方必须已经过 Guard。
// 所有值统一转成字符串：前端要展示，而不同驱动的类型映射差异很大，
// 保留原生类型只会让前端到处做兼容。NULL 与空串用不同表示（<NULL>）。
func Query(ctx context.Context, conn *sql.DB, stmt string, maxRows int, timeout time.Duration) (*Result, error) {
	if maxRows <= 0 {
		maxRows = 200
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	rows, err := conn.QueryContext(runCtx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := &Result{Columns: cols, Rows: [][]string{}, Statement: stmt}
	for rows.Next() {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			break
		}
		holders := make([]any, len(cols))
		raw := make([]sql.RawBytes, len(cols))
		for i := range holders {
			holders[i] = &raw[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		line := make([]string, len(cols))
		for i := range raw {
			if raw[i] == nil {
				line[i] = "<NULL>"
				continue
			}
			line[i] = string(raw[i])
		}
		out.Rows = append(out.Rows, line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.CostMs = time.Since(started).Milliseconds()
	return out, nil
}
