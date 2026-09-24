//go:build pg

package app

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// TestRLSBackedStoresWriteRead 回归守护：project_voice_settings 与 slide_script_sources
// 均启用 FORCE RLS，存储必须在设置 app.tenant_id 的事务内读写；此前直连连接池导致
// PUT /voice-settings 与 slides/source 500（GET 因无行静默返回默认值）。
func TestRLSBackedStoresWriteRead(t *testing.T) {
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	const tenantID, projectID = "00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM project_voice_settings WHERE tenant_id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM slide_script_sources WHERE tenant_id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE tenant_id=$1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, tenantID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'rls') ON CONFLICT DO NOTHING`, tenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO projects(id,tenant_id,owner_user,title) VALUES($1,$2,$3,$3) ON CONFLICT DO NOTHING`,
			projectID, tenantID, "tester")
		return err
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	vs := NewVoiceSettingsStore(pool)
	if err := vs.Save(ctx, tenantID, projectID, VoiceSettings{Model: "m1", Voice: "v1", RatePercent: 120}); err != nil {
		t.Fatalf("voice Save: %v", err)
	}
	got, err := vs.Get(ctx, tenantID, projectID)
	if err != nil || got.Model != "m1" || got.Voice != "v1" || got.RatePercent != 120 {
		t.Fatalf("voice Get = %+v err=%v", got, err)
	}

	ss := NewScriptSourceStore(pool)
	if err := ss.Set(ctx, tenantID, projectID, 1, "slide-1", ScriptSourceTitle, ""); err != nil {
		t.Fatalf("scriptsource Set: %v", err)
	}
	list, err := ss.List(ctx, tenantID, projectID, 1)
	if err != nil {
		t.Fatalf("scriptsource List: %v", err)
	}
	if c, ok := list["slide-1"]; !ok || c.Kind != ScriptSourceTitle {
		t.Fatalf("scriptsource List = %+v", list)
	}
}
