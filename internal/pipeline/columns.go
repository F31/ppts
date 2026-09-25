package pipeline

import "strings"

// 本文件是 pipeline 双存储（postgres.go 与 sqlite.go）**任务列投影的唯一来源**。
//
// 背景（2026-09-25 审视）：双存储包的历史缺陷几乎都是同一类——列清单与 Scan 目标各写一份，
// 加列时只改一侧。这类改动编译期完全无感，运行期才以「类型不匹配」或更隐蔽的
// 「值被读进另一个字段」爆发（internal/artifact 曾因 SQLite 侧错位导致整个 profile 硬失败，
// 只因该包当时没有 SQLite 往返测试）。
//
// 收敛方式：把「列名 + PG 扫描目标 + SQLite 扫描目标」绑定在同一个表项里。两侧的列清单
// 与 dest() 都由这张表生成，"只改一侧"在物理上不再可能。顺序仍然只有一个实例——即这张表的下标。

// dialect 标识 SQL 方言。本包两张表实现的差异集中在少数表达式上（占位符、时间函数、
// json 数组长度函数名、是否支持行值比较的显式类型转换），这些差异必须以 dialect 为参数
// 显式表达，禁止在两侧实现里各写一半而慢慢漂移。
type dialect int

const (
	dialectPG dialect = iota
	dialectSQLite
)

// jobColumn 把一列的三件事绑在一起：名字、PostgreSQL 扫描目标、SQLite 扫描目标。
//
// 两侧目标类型本就不同（PG 直接扫进 time.Time / **string，SQLite 时间列是 RFC3339 TEXT
// 且用 sql.NullString 承载可空列），所以无法共用一个 buf；能且必须共用的是**顺序**。
type jobColumn struct {
	name string
	pg   func(*jobScanBuf) any
	sq   func(*sqJobBuf) any
}

// jobColumns 的顺序 == SELECT 清单顺序 == 两侧 Scan 目标顺序。
//
// 增删列只改这里；两侧的 SELECT 文本与 dest() 随之变化，不存在需要同步的第二处清单。
// 注意：追加列不等于可以放进核心投影——控制台查询列（jobs.phase / affected_pages）刻意不在此表，
// 否则会连带要求重建 0018 里的 ppts_claim_next_job 调度函数（理由见 model.go 的 JobPageRow 注释）。
var jobColumns = []jobColumn{
	{"id", func(b *jobScanBuf) any { return &b.j.ID }, func(b *sqJobBuf) any { return &b.j.ID }},
	{"tenant_id", func(b *jobScanBuf) any { return &b.j.TenantID }, func(b *sqJobBuf) any { return &b.j.TenantID }},
	{"project_id", func(b *jobScanBuf) any { return &b.j.ProjectID }, func(b *sqJobBuf) any { return &b.j.ProjectID }},
	{"kind", func(b *jobScanBuf) any { return &b.j.Kind }, func(b *sqJobBuf) any { return &b.j.Kind }},
	{"state", func(b *jobScanBuf) any { return &b.j.State }, func(b *sqJobBuf) any { return &b.j.State }},
	{"input_snapshot", func(b *jobScanBuf) any { return &b.j.InputSnapshot }, func(b *sqJobBuf) any { return &b.j.InputSnapshot }},
	{"idempotency_key", func(b *jobScanBuf) any { return &b.j.IDempotencyKey }, func(b *sqJobBuf) any { return &b.j.IDempotencyKey }},
	{"attempt", func(b *jobScanBuf) any { return &b.j.Attempt }, func(b *sqJobBuf) any { return &b.j.Attempt }},
	// 可空列：PG 侧用**string 让 pgx 区分 NULL，SQLite 侧用 sql.NullString。
	{"lease_owner", func(b *jobScanBuf) any { return &b.leaseOwner }, func(b *sqJobBuf) any { return &b.leaseOwner }},
	{"lease_until", func(b *jobScanBuf) any { return &b.leaseUntilPtr }, func(b *sqJobBuf) any { return &b.leaseUntil }},
	{"fencing_token", func(b *jobScanBuf) any { return &b.j.FencingToken }, func(b *sqJobBuf) any { return &b.j.FencingToken }},
	{"run_at", func(b *jobScanBuf) any { return &b.runAtPtr }, func(b *sqJobBuf) any { return &b.runAt }},
	{"progress", func(b *jobScanBuf) any { return &b.j.Progress }, func(b *sqJobBuf) any { return &b.j.Progress }},
	{"last_error", func(b *jobScanBuf) any { return &b.lastErr }, func(b *sqJobBuf) any { return &b.lastErr }},
	// 时间列：PG 直接扫进 time.Time，SQLite 存 RFC3339 TEXT（写入用 db.FormatTime，读取用 db.ParseTime）。
	{"created_at", func(b *jobScanBuf) any { return &b.createdAt }, func(b *sqJobBuf) any { return &b.createdAt }},
	{"updated_at", func(b *jobScanBuf) any { return &b.updatedAt }, func(b *sqJobBuf) any { return &b.updatedAt }},
	{"traceparent", func(b *jobScanBuf) any { return &b.j.TraceParent }, func(b *sqJobBuf) any { return &b.j.TraceParent }},
}

// jobSelectColumns 是两侧共用的列清单文本（由 jobColumns 生成）。
//
// 它是 var 而非 const：唯一来源在上面那张表里，这里是它的产出。
var jobSelectColumns = jobColumnList(jobColumns)

// jobColumnList 拼出 "a, b, c" 形式的列清单。
func jobColumnList(cols []jobColumn) string {
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.name)
	}
	return strings.Join(names, ", ")
}

// jobReturningList 拼出 "t.a, t.b, ..." 形式的带别名清单，用于 UPDATE ... FROM alias RETURNING。
// ClaimNext 的 RETURNING 必须用别名（更新目标表也有同名列），此前它与 jobSelectColumns
// 是两份手工清单——那是本包最大的一处重复。
func jobReturningList(alias string, cols []jobColumn) string {
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, alias+c.name)
	}
	return strings.Join(names, ", ")
}

// pgJobDest 生成 PostgreSQL 侧的扫描目标，顺序取自 jobColumns。
func pgJobDest(cols []jobColumn, b *jobScanBuf) []any {
	dest := make([]any, 0, len(cols))
	for _, c := range cols {
		dest = append(dest, c.pg(b))
	}
	return dest
}

// sqJobDest 生成 SQLite 侧的扫描目标，顺序取自 jobColumns（与 pgJobDest 同源）。
func sqJobDest(cols []jobColumn, b *sqJobBuf) []any {
	dest := make([]any, 0, len(cols))
	for _, c := range cols {
		dest = append(dest, c.sq(b))
	}
	return dest
}

// jsonArrayLenExpr 返回 affected_pages 的长度表达式（PG 用 jsonb_*，SQLite 用 json_*）。
func jsonArrayLenExpr(d dialect) string {
	if d == dialectSQLite {
		return "json_array_length(affected_pages)"
	}
	return "jsonb_array_length(affected_pages)"
}

// jobSortSpec 把排序键映射为 SQL 表达式与其类型（后者用于 PG keyset 游标的显式类型转换）。
//
// SQLite 不支持也不需要在比较处做显式类型转换（动态类型），cast 对 SQLite 返回空串。
// 未知取值退回 created：api 侧已做白名单校验并会返回 400，此处只是防御性兜底。
func jobSortSpec(d dialect, sort string) (expr, cast string) {
	switch sort {
	case "updated":
		return "updated_at", "timestamptz"
	case "phase":
		return "phase", "text"
	case "pages":
		return jsonArrayLenExpr(d), "int"
	default:
		return "created_at", "timestamptz"
	}
}

// jobPageExtraColumns 返回列表页在核心投影之外追加的派生列表达式。
func jobPageExtraColumns(d dialect) string {
	return ", phase, " + jsonArrayLenExpr(d)
}
