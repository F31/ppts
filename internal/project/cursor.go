package project

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// 项目列表分页游标。
//
// 排序键是 (时间, id)，不是只有时间。只按时间做 keyset 分页时，同一毫秒创建的
// 多个项目会一起越过边界：上一页把它们截断在中间，下一页用 `时间 < 游标` 又把这批
// 剩下的全部跳过——界面上的表现是「翻一页少了几份 PPT」，而且丢的是刚刚批量导入的
// 那一批（SQLite 的时间列只有毫秒精度，连续创建必然同毫秒）。
//
// 加上 id 作为次键之后，(时间, id) 唯一，分页成为严格全序：不重不漏。
//
// 编码形如 `2026-11-08T12:00:00.123Z|<uuid>`。兼容旧格式（只有时间、没有分隔符）：
// 已经发出去的游标不能因为升级一次就变成 500，那种游标按「时间 < 游标」处理，
// 行为与升级前一致。
const projectCursorSep = "|"

// encodeProjectCursor 把一行的排序键编码成游标。
func encodeProjectCursor(t time.Time, id string) string {
	return t.UTC().Format(time.RFC3339Nano) + projectCursorSep + id
}

// decodeProjectCursor 解析游标，返回排序时间与次键 id（旧格式的 id 为空串）。
func decodeProjectCursor(s string) (time.Time, string, error) {
	ts, id, hasID := strings.Cut(s, projectCursorSep)
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(ts))
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	if !hasID {
		id = ""
	}
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return time.Time{}, "", ErrInvalidCursor
		}
	}
	return t.UTC(), id, nil
}

// projectCursorFrom 取一行的排序键。归档列表按归档时间排（归档时间缺失时回落到
// 更新时间，与 SQL 里的 COALESCE 一致），其余列表按创建时间排。
func projectCursorFrom(p *Project, archived bool) time.Time {
	if archived && !p.ArchivedAt.IsZero() {
		return p.ArchivedAt
	}
	return p.CreatedAt
}
