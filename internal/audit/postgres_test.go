//go:build pg

package audit

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const (
	auditTenantA = "00000000-0000-0000-0000-0000000000c1"
	auditTenantB = "00000000-0000-0000-0000-0000000000c2"
)

func auditStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE audit_events, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, id := range []string{auditTenantA, auditTenantB} {
		if _, err := pool.Exec(context.Background(),
			"INSERT INTO tenants(id,name) VALUES ($1,'audit')", id); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	return NewPGStore(pool)
}

func TestAuditRecordAndList(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()

	if err := s.Record(ctx, Event{
		TenantID: auditTenantA, ActorUser: "user-1", Action: "job.cancel",
		ResourceType: "job", ResourceID: "job-1", Metadata: map[string]any{"state": "canceled"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record(ctx, Event{
		TenantID: auditTenantA, ActorUser: "user-1", Action: "job.retry",
		ResourceType: "job", ResourceID: "job-2",
	}); err != nil {
		t.Fatalf("Record retry: %v", err)
	}

	events, err := s.List(ctx, auditTenantA, Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d want 2", len(events))
	}
	// 倒序：最新在前。
	if events[0].Action != "job.retry" {
		t.Fatalf("first event = %+v want job.retry", events[0])
	}

	onlyCancel, err := s.List(ctx, auditTenantA, Filter{Action: "job.cancel"})
	if err != nil {
		t.Fatalf("List filter: %v", err)
	}
	if len(onlyCancel) != 1 || onlyCancel[0].ResourceID != "job-1" ||
		onlyCancel[0].Metadata["state"] != "canceled" {
		t.Fatalf("filtered = %+v", onlyCancel)
	}
}

func TestAuditTenantIsolated(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	if err := s.Record(ctx, Event{TenantID: auditTenantA, Action: "job.cancel"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	events, err := s.List(ctx, auditTenantB, Filter{})
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("tenant B should not see A events: %+v", events)
	}
}

func TestAuditValidation(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	if err := s.Record(ctx, Event{Action: "x"}); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("missing tenant = %v want ErrTenantRequired", err)
	}
	if err := s.Record(ctx, Event{TenantID: auditTenantA}); !errors.Is(err, ErrActionRequired) {
		t.Fatalf("missing action = %v want ErrActionRequired", err)
	}
	if _, err := s.List(ctx, "", Filter{}); !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("list missing tenant = %v want ErrTenantRequired", err)
	}
}

func TestAuditListBeforeAndDeleteBefore(t *testing.T) {
	s := auditStore(t)
	ctx := context.Background()
	// 直插不同 created_at（租户上下文内，受 RLS），验证归档边界。
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	for _, in := range []struct {
		when time.Time
		act  string
	}{
		{old, "old.one"}, {old, "old.two"}, {fresh, "fresh"},
	} {
		if err := tenant.Run(ctx, s.pool, auditTenantA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx,
				`INSERT INTO audit_events (tenant_id, actor_user, action, created_at)
				 VALUES ($1::uuid, 'user-1', $2, $3)`, auditTenantA, in.act, in.when)
			return err
		}); err != nil {
			t.Fatalf("insert %s: %v", in.act, err)
		}
	}
	cutoff := time.Now().Add(-24 * time.Hour)

	before, err := s.List(ctx, auditTenantA, Filter{Before: cutoff})
	if err != nil {
		t.Fatalf("List Before: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("before events = %d want 2", len(before))
	}

	deleted, err := s.DeleteBefore(ctx, auditTenantA, cutoff)
	if err != nil {
		t.Fatalf("DeleteBefore: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d want 2", deleted)
	}
	remaining, err := s.List(ctx, auditTenantA, Filter{})
	if err != nil {
		t.Fatalf("List remaining: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Action != "fresh" {
		t.Fatalf("remaining = %+v want only fresh", remaining)
	}
	// 租户 B 不受影响。
	delB, err := s.DeleteBefore(ctx, auditTenantB, cutoff)
	if err != nil {
		t.Fatalf("DeleteBefore B: %v", err)
	}
	if delB != 0 {
		t.Fatalf("tenant B deleted = %d want 0", delB)
	}
}
