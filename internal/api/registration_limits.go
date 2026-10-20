package api

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// A separate scope prevents historical failed attempts from becoming success counts.
const registrationSuccessScope = "auth.register_success_daily"

type registrationCounter interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func registrationDailyBlocked(ctx context.Context, db registrationCounter, ip string, limit int, now time.Time) (bool, error) {
	var count int64
	err := db.QueryRow(ctx, `SELECT COALESCE((SELECT count FROM ppts_rate_limit
		WHERE scope=$1 AND bucket_key=$2 AND window_start=$3),0)`,
		registrationSuccessScope, ip, bucketStart(now, 24*time.Hour)).Scan(&count)
	return count >= int64(limit), err
}

// Call within the transaction creating the account and roll back if false.
// UPSERT serializes contenders on the same IP bucket across all API replicas.
func reserveRegistrationSuccess(ctx context.Context, tx registrationCounter, ip string, limit int, now time.Time) (bool, error) {
	var count int64
	err := tx.QueryRow(ctx, rateLimitUpsertPG, registrationSuccessScope, ip,
		bucketStart(now, 24*time.Hour), int64(limit)+1).Scan(&count)
	return count <= int64(limit), err
}

func registrationLimitWindow(dailyBlocked bool) (time.Duration, string) {
	// Daily boundaries are also hourly boundaries, so a blocked daily bucket
	// always expires at least as late as the hourly one (including both blocked).
	if dailyBlocked {
		return 24 * time.Hour, "registration_daily_limited"
	}
	return time.Hour, "rate_limited"
}
