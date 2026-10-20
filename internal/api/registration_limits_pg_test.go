//go:build pg

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistrationDailyTransactionAndConcurrency(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSNOrSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ip := fmt.Sprintf("registration-test-%d", time.Now().UnixNano())
	defer pool.Exec(ctx, "DELETE FROM ppts_rate_limit WHERE bucket_key=$1", ip)
	now := time.Now()
	// Historical failed attempts must not turn into successful registrations.
	if _, err := pool.Exec(ctx, `INSERT INTO ppts_rate_limit(scope,bucket_key,window_start,count)
		VALUES('auth.register_daily',$1,$2,999)`, ip, bucketStart(now, 24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	d := &authDeps{pool: pool, limits: newAuthLimits(defaultAuthRateConfig(), newMemoryBackend()),
		settings: authSettings{registrationEnabled: true, registerDailyPerIP: 2}}
	for _, body := range []string{
		`{"account":"invalid","password":"StrongPass42!","account_type":"personal"}`,
		`{"account":"user@example.com","password":"short","account_type":"personal"}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
		r.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		d.handleRegister(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d %s", w.Code, w.Body.String())
		}
	}
	// A reservation rolled back with a failed account creation must not count.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := reserveRegistrationSuccess(ctx, tx, ip, 2, now)
	if err != nil || !allowed {
		t.Fatalf("reserve: %v %v", allowed, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ppts_rate_limit WHERE scope=$1 AND bucket_key=$2", registrationSuccessScope, ip).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed registration counted: %d %v", count, err)
	}
	// Parallel API replicas must not commit more than the shared daily cap.
	var committed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback(ctx)
			allowed, err := reserveRegistrationSuccess(ctx, tx, ip, 2, now)
			if err != nil {
				t.Error(err)
				return
			}
			if allowed {
				if err := tx.Commit(ctx); err != nil {
					t.Error(err)
					return
				}
				committed.Add(1)
			}
		}()
	}
	wg.Wait()
	if committed.Load() != 2 {
		t.Fatalf("committed %d; cap=2", committed.Load())
	}
	blocked, err := registrationDailyBlocked(ctx, pool, ip, 2, now)
	if err != nil || !blocked {
		t.Fatalf("daily check: %v %v", blocked, err)
	}
	blocked, err = registrationDailyBlocked(ctx, pool, ip, 2, bucketStart(now, 24*time.Hour).Add(24*time.Hour))
	if err != nil || blocked {
		t.Fatalf("next window did not reset: %v %v", blocked, err)
	}
}
