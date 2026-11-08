package audit

import (
	"errors"
	"time"
)

// ViewStatsQuery 是观看事件聚合的查询条件。
//
// 四个字段都必须给：缺 Action / ResourceType / ResourceID 会让聚合退化成跨业务、
// 跨资源的全表统计；缺 Since 会让「最近 N 天」变成「有史以来」，而审计表会被
// 归档清理，看板会把留存窗口说成全量。宁可报错，也不给一个看着正常的错数字。
type ViewStatsQuery struct {
	Action       string
	ResourceType string
	ResourceID   string
	Since        time.Time // created_at >= since（含）
}

var ErrViewStatsQueryIncomplete = errors.New("audit: view stats query requires action, resource type, resource id and since")

func (q ViewStatsQuery) validate(tenantID string) error {
	if tenantID == "" || q.Action == "" || q.ResourceType == "" || q.ResourceID == "" || q.Since.IsZero() {
		return ErrViewStatsQueryIncomplete
	}
	return nil
}

// ViewStats 是聚合结果，不含任何单条事件、session、token 或 IP。
//
// Sessions 是窗口内跨所有事件名的独立会话数；ByEvent 给出每个事件名各自的
// 独立会话数（不能直接求和，同一会话会落在多个进度桶里）。未知事件名照样
// 出现在 ByEvent 里，由调用方按封闭白名单取值——存储层不偷偷替业务删数据。
type ViewStats struct {
	Sessions     int64
	ByEvent      map[string]int64
	FirstEventAt time.Time
	LastEventAt  time.Time
}

// EventSessions 是调用方读某个封闭事件名桶的唯一入口。
func (s ViewStats) EventSessions(name string) int64 {
	if s.ByEvent == nil {
		return 0
	}
	return s.ByEvent[name]
}
