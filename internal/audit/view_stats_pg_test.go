//go:build pg

package audit

import (
	"context"
	"testing"
	"time"
)

// TestPGViewStats 只验 PG 那一半方言（jsonb ->>、tenant.Run 下的 RLS）：
// SQLite 侧的完整分界用例在 view_stats_test.go，这里不重复。
// 少了这条，PG 的 metadata->>'event' 写错也不会被 CI 的 postgres 作业发现——
// 本机与预览实例都是 SQLite，线上才是 PG。
func TestPGViewStats(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	since := time.Now().UTC().Add(-time.Hour)

	record := func(tenantID, rid, event, session string) {
		t.Helper()
		if err := s.Record(ctx, Event{
			TenantID:     tenantID,
			ActorUser:    "anonymous",
			Action:       "artifact_share.view_event",
			ResourceType: "artifact_share_link",
			ResourceID:   rid,
			Metadata:     map[string]any{"event": event, "session": session},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	record(auditTenantA, "link-1", "view", "s1")
	record(auditTenantA, "link-1", "view", "s1") // 重试重复落库
	record(auditTenantA, "link-1", "progress_25", "s1")
	record(auditTenantA, "link-1", "progress_100", "s1")
	record(auditTenantA, "link-1", "view", "s2")
	record(auditTenantA, "link-2", "view", "other-link")
	record(auditTenantB, "link-1", "view", "other-tenant")

	got, err := s.ViewStats(ctx, auditTenantA, viewStatsQuery("link-1", since))
	if err != nil {
		t.Fatalf("ViewStats: %v", err)
	}
	if got.Sessions != 2 {
		t.Errorf("sessions = %d, want 2（RLS + resource_id 同时生效）", got.Sessions)
	}
	if n := got.EventSessions("view"); n != 2 {
		t.Errorf("views = %d, want 2（同一会话重复上报只算一次）", n)
	}
	if n := got.EventSessions("progress_25"); n != 1 {
		t.Errorf("progress_25 = %d, want 1", n)
	}
	if n := got.EventSessions("progress_100"); n != 1 {
		t.Errorf("progress_100 = %d, want 1", n)
	}
	if n := got.EventSessions("progress_50"); n != 0 {
		t.Errorf("progress_50 = %d, want 0", n)
	}
}

// CountByMetadata 的 PG 方言：jsonb ->> 与 metadata ? 的配合、以及 tenant.Run 下的隔离。
func TestPGCountByMetadata(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()

	record := func(tenantID, artifactID, linkID string) {
		t.Helper()
		if err := s.Record(ctx, Event{
			TenantID:     tenantID,
			ActorUser:    "anonymous",
			Action:       "artifact_share.play",
			ResourceType: "artifact",
			ResourceID:   artifactID,
			Metadata:     map[string]any{"link_id": linkID},
		}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	record(auditTenantA, "a1", "link-1")
	record(auditTenantA, "a1", "link-1")
	record(auditTenantA, "a1", "link-2")
	record(auditTenantA, "a2", "link-1")
	record(auditTenantB, "a1", "link-1")

	got, err := s.CountByMetadata(ctx, auditTenantA, "artifact_share.play", "artifact", "a1", "link_id")
	if err != nil {
		t.Fatalf("CountByMetadata: %v", err)
	}
	if got["link-1"] != 2 || got["link-2"] != 1 || len(got) != 2 {
		t.Fatalf("counts = %+v, want link-1=2 link-2=1", got)
	}
}
