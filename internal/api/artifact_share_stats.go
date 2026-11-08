package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// 作者侧在线观看统计（V20 的数据出口）。
//
// 端点只读，不产生新的审计事件，也不返回任何单条事件 / session / token / IP / UA。
// 三个「不能说谎」的地方：
//   - 数字只覆盖 window（默认 30 天、上限 90 天）：audit_events 会被归档清理，
//     说「累计」就是把留存窗口冒充全量。
//   - 采集总闸关闭时仍返回 200，但 telemetryEnabled=false；前端必须显示说明，
//     不能把零数据渲染成「0 次观看」。
//   - 完播率分母是窗口内的独立会话数，不是「打开过的人数」——100% 桶会因拖动
//     跳过而不补报（V20 的诚实判据），分母不是完美代理，响应里单独给出。
const (
	shareStatsDefaultDays = 30
	shareStatsMaxDays     = 90

	// retentionNote 是稳定短码，不是给人看的句子：文案由前端按语言渲染，
	// 后端不往 API 里塞中文，也不让前端靠字符串比对猜状态。
	shareStatsRetentionNote = "audit_retention_window"
)

var shareStatBuckets = []int{25, 50, 75, 100}

func parseShareStatsDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return shareStatsDefaultDays, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > shareStatsMaxDays {
		return 0, errors.New("days must be an integer between 1 and 90")
	}
	return n, nil
}

func artifactShareStats(w http.ResponseWriter, r *http.Request,
	artifacts artifact.Store, links artifact.ShareLinkStore,
	projects project.ProjectStore, members membership.Reader,
	auditStore audit.Store, linkID string) {
	// 与其他成品分享端点同一把锁：principal / editor / 项目可见 / 成品属于该项目 /
	// 格式是 html_bundle。不另写一套轻量鉴权。
	a, ok := loadShareableArtifact(w, r, artifacts, projects, members, auditStore)
	if !ok {
		return
	}
	principal, _ := requirePrincipal(r.Context())

	// 先按 (tenant, artifact) 列出再匹配 linkID，而不是「按 ID 直取」：
	// 后者会绕过成品归属校验，让 A 成品的路径读到 B 成品的观看数据。
	items, err := links.ListShareLinks(r.Context(), principal.TenantID, a.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var owned bool
	for _, item := range items {
		if item.ID == linkID {
			owned = true
			break
		}
	}
	if !owned {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	days, err := parseShareStatsDays(r.URL.Query().Get("days"))
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -days)
	stats, err := auditStore.ViewStats(r.Context(), principal.TenantID, audit.ViewStatsQuery{
		Action:       artifactShareActionViewEvent,
		ResourceType: artifactShareLinkResourceType,
		ResourceID:   linkID,
		Since:        since,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	progress := make([]map[string]any, 0, len(shareStatBuckets))
	for _, bucket := range shareStatBuckets {
		progress = append(progress, map[string]any{
			"bucket":   bucket,
			"sessions": stats.EventSessions("progress_" + strconv.Itoa(bucket)),
		})
	}
	completed := stats.EventSessions("progress_100")
	var completionRate float64
	if stats.Sessions > 0 {
		completionRate = float64(completed) / float64(stats.Sessions)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"linkId":           linkID,
		"artifactId":       a.ID,
		"projectId":        a.ProjectID,
		"telemetryEnabled": shareTelemetryEnabled(),
		"retentionNote":    shareStatsRetentionNote,
		"window": map[string]any{
			"days":  days,
			"since": since.Format(time.RFC3339),
			"until": now.Format(time.RFC3339),
		},
		"stats": map[string]any{
			"views":          stats.EventSessions(shareEventKindView),
			"sessions":       stats.Sessions,
			"progress":       progress,
			"completed":      completed,
			"completionRate": completionRate,
			"firstEventAt":   shareStatsOptionalTime(stats.FirstEventAt),
			"lastEventAt":    shareStatsOptionalTime(stats.LastEventAt),
		},
	})
}

// shareStatsOptionalTime 与 collab.optionalTime 刻意不共用：那边空值返回 ""，
// 是给「可选表单字段」用的；统计里空值必须真的是 null——前端靠它区分
// 「窗口内一条事件都没有」与「有事件但时间格式没解析出来」。
func shareStatsOptionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}
