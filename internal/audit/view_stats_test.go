package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
)

var viewEventSeq atomic.Uint64

// insertViewEvent 直接指定 created_at 与 metadata 落一行。Record 只会写"现在"，
// 而统计的重要分界恰好是时间窗，必须能把行放到窗内外两侧。
func insertViewEvent(t *testing.T, sqldb *sql.DB, tenant, action, rtype, rid, event, session, createdAt string) {
	t.Helper()
	meta := `{"event":"` + event + `","session":"` + session + `"}`
	id := fmt.Sprintf("ev-%d", viewEventSeq.Add(1))
	if _, err := sqldb.Exec(
		`INSERT INTO audit_events (id, tenant_id, actor_user, action, resource_type, resource_id, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tenant, "anonymous", action, rtype, rid, meta, createdAt); err != nil {
		t.Fatalf("insert view event: %v", err)
	}
}

func viewStatsQuery(rid string, since time.Time) ViewStatsQuery {
	return ViewStatsQuery{
		Action:       "artifact_share.view_event",
		ResourceType: "artifact_share_link",
		ResourceID:   rid,
		Since:        since,
	}
}

func TestSQLiteViewStatsDeduplicatesSessionsAcrossBuckets(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteAuditStore(t)
	since := time.Now().UTC().Add(-24 * time.Hour)
	now := db.Now()

	// 三个会话：s1 看完全程、s2 只到 50、s3 只有首帧。同一 (event, session)
	// 各插两条，模拟微批重试与 pagehide 冲刷重复落库。
	for _, event := range []string{"view", "progress_25", "progress_50", "progress_75", "progress_100"} {
		for i := 0; i < 2; i++ {
			insertViewEvent(t, sqldb, db.LocalTenantID, "artifact_share.view_event", "artifact_share_link", "link-1", event, "s1", now)
		}
	}
	for _, event := range []string{"view", "progress_25", "progress_50"} {
		insertViewEvent(t, sqldb, db.LocalTenantID, "artifact_share.view_event", "artifact_share_link", "link-1", event, "s2", now)
	}
	insertViewEvent(t, sqldb, db.LocalTenantID, "artifact_share.view_event", "artifact_share_link", "link-1", "view", "s3", now)

	got, err := store.ViewStats(ctx, db.LocalTenantID, viewStatsQuery("link-1", since))
	if err != nil {
		t.Fatalf("view stats: %v", err)
	}
	if got.Sessions != 3 {
		t.Errorf("sessions = %d, want 3（按会话去重，不是按事件条数）", got.Sessions)
	}
	if n := got.EventSessions("view"); n != 3 {
		t.Errorf("views = %d, want 3", n)
	}
	for _, tc := range []struct {
		event string
		want  int64
	}{
		{"progress_25", 2}, {"progress_50", 2}, {"progress_75", 1}, {"progress_100", 1},
	} {
		if n := got.EventSessions(tc.event); n != tc.want {
			t.Errorf("%s = %d, want %d", tc.event, n, tc.want)
		}
	}
	if got.FirstEventAt.IsZero() || got.LastEventAt.IsZero() {
		t.Error("首末事件时间应非空")
	}
}

func TestSQLiteViewStatsIsolatesLinkTenantWindowAndAction(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteAuditStore(t)
	since := time.Now().UTC().Add(-24 * time.Hour)
	now := db.Now()
	old := db.FormatTime(time.Now().UTC().Add(-72 * time.Hour))
	const action = "artifact_share.view_event"
	const rtype = "artifact_share_link"
	if _, err := sqldb.Exec(`INSERT INTO tenants (id, name) VALUES (?, ?)`, auditOtherTenant, "另一租户"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	insertViewEvent(t, sqldb, db.LocalTenantID, action, rtype, "link-1", "view", "in-window", now)
	insertViewEvent(t, sqldb, db.LocalTenantID, action, rtype, "link-2", "view", "other-link", now)
	insertViewEvent(t, sqldb, auditOtherTenant, action, rtype, "link-1", "view", "other-tenant", now)
	insertViewEvent(t, sqldb, db.LocalTenantID, action, rtype, "link-1", "view", "too-old", old)
	insertViewEvent(t, sqldb, db.LocalTenantID, "artifact_share.anonymous_access", rtype, "link-1", "view", "other-action", now)
	insertViewEvent(t, sqldb, db.LocalTenantID, action, "project", "link-1", "view", "other-resource-type", now)
	insertViewEvent(t, sqldb, db.LocalTenantID, action, rtype, "link-1", "unknown_event", "unknown-event-session", now)

	got, err := store.ViewStats(ctx, db.LocalTenantID, viewStatsQuery("link-1", since))
	if err != nil {
		t.Fatalf("view stats: %v", err)
	}
	if got.Sessions != 2 {
		t.Errorf("sessions = %d, want 2（窗口内 + 未知事件名的会话）", got.Sessions)
	}
	if n := got.EventSessions("view"); n != 1 {
		t.Errorf("views = %d, want 1", n)
	}
	if n := got.EventSessions("unknown_event"); n != 1 {
		t.Errorf("unknown_event 应在 ByEvent 里可见 = %d, want 1", n)
	}
	// 未知事件名只是「不进任何白名桶」，不是把整行删掉：调用方按封闭集合取值。
	for _, name := range []string{"progress_25", "progress_50", "progress_75", "progress_100"} {
		if n := got.EventSessions(name); n != 0 {
			t.Errorf("%s = %d, want 0", name, n)
		}
	}
}

func TestSQLiteViewStatsNotTruncatedByListLimit(t *testing.T) {
	ctx := context.Background()
	store, sqldb := newSQLiteAuditStore(t)
	since := time.Now().UTC().Add(-24 * time.Hour)
	// 700 > List 的 500 上限。若有人把聚合退回「List 之后在 Go 里数」，
	// 这里会读到 500 而不是 700，直接判红——这是 design §1 的可执行理由。
	const total = 700
	for i := 0; i < total; i++ {
		insertViewEvent(t, sqldb, db.LocalTenantID, "artifact_share.view_event", "artifact_share_link",
			"link-1", "view", "session-"+itoaForTest(i), db.Now())
	}
	got, err := store.ViewStats(ctx, db.LocalTenantID, viewStatsQuery("link-1", since))
	if err != nil {
		t.Fatalf("view stats: %v", err)
	}
	if got.Sessions != total || got.EventSessions("view") != total {
		t.Errorf("sessions=%d views=%d, want %d/%d（不得被列表上限截断）",
			got.Sessions, got.EventSessions("view"), total, total)
	}
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestSQLiteViewStatsEmptyAndIncompleteQuery(t *testing.T) {
	ctx := context.Background()
	store, _ := newSQLiteAuditStore(t)
	since := time.Now().UTC().Add(-24 * time.Hour)

	got, err := store.ViewStats(ctx, db.LocalTenantID, viewStatsQuery("link-none", since))
	if err != nil {
		t.Fatalf("view stats: %v", err)
	}
	if got.Sessions != 0 || got.EventSessions("view") != 0 || got.ByEvent == nil {
		t.Errorf("空结果应为零值 + 非 nil map，got %+v", got)
	}
	if !got.FirstEventAt.IsZero() || !got.LastEventAt.IsZero() {
		t.Error("空结果的首末时间应为零值")
	}

	q := viewStatsQuery("link-1", since)
	for _, mutate := range []struct {
		name string
		fn   func(*ViewStatsQuery)
	}{
		{"tenant", func(*ViewStatsQuery) {}},
		{"action", func(q *ViewStatsQuery) { q.Action = "" }},
		{"resource_type", func(q *ViewStatsQuery) { q.ResourceType = "" }},
		{"resource_id", func(q *ViewStatsQuery) { q.ResourceID = "" }},
		{"since", func(q *ViewStatsQuery) { q.Since = time.Time{} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			copy := q
			mutate.fn(&copy)
			tenantID := db.LocalTenantID
			if mutate.name == "tenant" {
				tenantID = ""
			}
			_, err := store.ViewStats(ctx, tenantID, copy)
			if !errors.Is(err, ErrViewStatsQueryIncomplete) {
				t.Fatalf("err = %v, want ErrViewStatsQueryIncomplete", err)
			}
		})
	}
}
