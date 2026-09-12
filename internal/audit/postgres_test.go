//go:build pg

package audit

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
