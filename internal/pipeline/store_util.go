package pipeline

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// 本文件只放**两套存储共用的方言无关函数**。
//
// 这些函数原先住在 postgres.go 里，于是 sqlite.go 必须引用 PG 实现文件才能拿到
// 游标编解码与参数归一——一个纯 SQLite 构建读起来像是「SQLite 依赖 PostgreSQL」。
// 放这里之后：postgres.go 只留 pgx 相关代码，sqlite.go 只留 database/sql 相关代码，
// 谁也不再需要通过对方的实现文件拿工具函数。

// mustJSONArray 把字符串切片序列化成 jsonb 参数文本（nil / 序列化失败退化为空数组）。
// affected_pages 只是可查询投影，其解析失败不应让任务创建失败。
func mustJSONArray(items []string) string {
	if items == nil {
		return "[]"
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// jobCursorSep 分隔「排序键值」与「任务 id」；用不可见字符避免与时间戳/阶段名冲突。
const jobCursorSep = "\x1f"

func encodeJobCursor(sortValue, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sortValue + jobCursorSep + id))
}

func decodeJobCursor(cursor string) (sortValue, id string, err error) {
	raw, derr := base64.RawURLEncoding.DecodeString(cursor)
	if derr != nil {
		return "", "", ErrBadJobCursor
	}
	parts := strings.SplitN(string(raw), jobCursorSep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrBadJobCursor
	}
	return parts[0], parts[1], nil
}

// jobPageSortValue 取一行在当前排序键下的值（用于生成下一页游标）。
func jobPageSortValue(f JobFilter, row JobPageRow) string {
	switch f.Sort {
	case "updated":
		return row.Job.UpdatedAt.Format(time.RFC3339Nano)
	case "phase":
		return row.Phase
	case "pages":
		return strconv.Itoa(row.PageCount)
	default:
		return row.Job.CreatedAt.Format(time.RFC3339Nano)
	}
}

// nullableRunAt 把「零值 = 立即可领取」翻译成 SQL NULL。
//
// jobs.run_at 的语义是「早于此刻不可领取」，NULL 就是立刻可领；SQLite 侧一直用
// sqNullableTime 做这件事，PG 侧却把 time.Time 的地址直接交给驱动，于是零值变成
// 公元 1 年。调度本身没坏（run_at <= now() 仍然成立），坏的是读侧：QueueDepthByKind
// 用 COALESCE(run_at, created_at) 报「最老的待领取任务」，对即时任务会给出 0001-01-01，
// 让「队列等了多久」这个判据失去意义。可空列的零值语义必须只定义一次。
func nullableRunAt(runAt time.Time) any {
	if runAt.IsZero() {
		return nil
	}
	return runAt
}

func nullableBytes(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}
