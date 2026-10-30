package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/usage"
	"github.com/F31/ppts/migrations"
)

// 动画渲染额度（M4 §7.5）的受理侧。
//
// 这里守的是三件会直接变成线上事故的事：
//   1. 超限必须在**受理时**明确拒绝（不能先入队再在 worker 里失败 = 静默排队）；
//   2. 并发请求不能超卖（额度靠单行条件更新，压测必须能证伪）；
//   3. 任务没建成时预占必须放掉（否则一次 500 就吃掉一个月的额度）。

func newUsageTestStore(t *testing.T) usage.Store {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	// tenant_quotas 有指向 tenants(id) 的外键：额度行必须先有租户，
	// 否则预占会在 INSERT tenant_quotas 上直接失败（表现为 500，看不出是额度问题）。
	if _, err := sqldb.ExecContext(ctx, `INSERT INTO tenants(id, name) VALUES(?, ?)`, "tenant-1", "quota-test"); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return usage.NewSQLiteStore(sqldb)
}

// seedTimeline 在对象存储里放一份时间轴 JSON（只要能被额度预估读出来即可）。
func seedTimeline(t *testing.T, objects objectstore.ObjectStore, key string, durationMS int64) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"timeline": map[string]any{
			"schemaVersion": "1",
			"durationUs":    durationMS * 1000,
			"slides": []map[string]any{
				{"slideId": "slide-1", "startUs": 0, "endUs": durationMS * 1000},
			},
		},
		"srtKey": "tenant-1/project-1/narration/subtitle/tl.srt",
		"vttKey": "tenant-1/project-1/narration/subtitle/tl.vtt",
	})
	if err != nil {
		t.Fatalf("marshal timeline: %v", err)
	}
	objKey, err := objectstore.Parse(key)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if err := objects.Put(context.Background(), objKey, strings.NewReader(string(body)),
		objectstore.ObjectMeta{ContentType: "application/json", Size: int64(len(body))}); err != nil {
		t.Fatalf("put timeline: %v", err)
	}
}

func animationExportBody(timelineKey string) string {
	return fmt.Sprintf(`{"format":"mp4","timelineKey":%q,`+
		`"pagePngKeys":["tenant-1/project-1/render/page/p1.png"],`+
		`"animation":{"specs":[{"slideId":"slide-1","language":"zh-CN","sourceRevisionNo":1,"revision":2}]}}`,
		timelineKey)
}

func postAnimationExport(t *testing.T, serverURL, body, idempotencyKey string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// animationQuotaServer 起一个带账本、对象存储与策略的 handler。
func animationQuotaServer(t *testing.T, objects objectstore.ObjectStore, store usage.Store, maxMinutes float64) *httptest.Server {
	t.Helper()
	return animationQuotaServerWithPolicy(t, objects, store, &tenant.Policy{
		Animation: &animation.AnimationPolicy{Enabled: boolPtr(true), MaxMinutes: maxMinutes},
	})
}

func animationQuotaServerWithPolicy(t *testing.T, objects objectstore.ObjectStore, store usage.Store, policy *tenant.Policy) *httptest.Server {
	t.Helper()
	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		jobs, &fakeArtifactStore{}, objects, nil, Options{
			DevHeaders:              true,
			Animation:               newAnimationTestStore(t),
			AnimationEnabled:        true,
			AnimationProbe:          &stubProbe{},
			UsageStore:              store,
			Policy:                  fakePolicyReader{policy: policy},
			AnimationRolloutPercent: intPtr(100),
		}))
	t.Cleanup(server.Close)
	return server
}

func TestBundleExportReservesAnimationMinutes(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 300_000) // 5 分钟
	store := newUsageTestStore(t)
	server := animationQuotaServer(t, objects, store, 60)

	resp := postAnimationExport(t, server.URL, animationExportBody(timelineKey), "quota-reserve-1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
	q, err := store.GetQuota(context.Background(), "tenant-1", kind)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.LimitUnits != 60 {
		t.Fatalf("limit = %v, want 60（策略里的 max_minutes 必须落到额度行）", q.LimitUnits)
	}
	if q.ReservedUnits != 5 {
		t.Fatalf("reserved = %v, want 5（时间轴 300s）", q.ReservedUnits)
	}
	if q.ConsumedUnits != 0 {
		t.Fatalf("consumed = %v, want 0（受理不结算）", q.ConsumedUnits)
	}
}

func TestBundleExportRejectsWhenAnimationQuotaExhausted(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 300_000) // 5 分钟
	store := newUsageTestStore(t)
	server := animationQuotaServer(t, objects, store, 4) // 上限 4 分钟 < 5 分钟

	resp := postAnimationExport(t, server.URL, animationExportBody(timelineKey), "quota-reject-1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	var body struct {
		Code        string   `json:"code"`
		Limit       float64  `json:"limitMinutes"`
		Requested   float64  `json:"requestedMinutes"`
		ResetAt     string   `json:"resetAt"`
		Period      string   `json:"period"`
		UsedMinutes float64  `json:"usedMinutes"`
		Reserved    *float64 `json:"reservedMinutes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "animation_quota_exceeded" {
		t.Fatalf("code = %q", body.Code)
	}
	// 四个数缺一不可：用户要知道"上限多少、已用多少、这次要多少、什么时候恢复"。
	if body.Limit != 4 || body.Requested != 5 {
		t.Fatalf("limit/requested = %v/%v, want 4/5", body.Limit, body.Requested)
	}
	if body.Period == "" || body.ResetAt == "" {
		t.Fatalf("period/resetAt missing: %+v", body)
	}
	reset, err := time.Parse(time.RFC3339, body.ResetAt)
	if err != nil || !reset.After(time.Now()) {
		t.Fatalf("resetAt = %q, want a future instant", body.ResetAt)
	}
	// Retry-After 是给通用错误处理用的相对秒数：前端不必解析业务字段就能算出恢复时间。
	if retry := resp.Header.Get("Retry-After"); retry == "" {
		t.Fatal("Retry-After header missing on quota rejection")
	}
}

func TestBundleExportReleasesReservationWhenJobCreationFails(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 300_000)
	store := newUsageTestStore(t)

	// 建任务失败：预占必须放掉，否则一次 500 就吃掉 5 分钟额度。
	jobs := &jobCreatorStub{err: fmt.Errorf("insert failed")}
	policy := &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: boolPtr(true), MaxMinutes: 60}}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
		jobs, &fakeArtifactStore{}, objects, nil, Options{
			DevHeaders:              true,
			Animation:               newAnimationTestStore(t),
			AnimationEnabled:        true,
			AnimationProbe:          &stubProbe{},
			UsageStore:              store,
			Policy:                  fakePolicyReader{policy: policy},
			AnimationRolloutPercent: intPtr(100),
		}))
	t.Cleanup(server.Close)

	resp := postAnimationExport(t, server.URL, animationExportBody(timelineKey), "quota-release-1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
	q, err := store.GetQuota(context.Background(), "tenant-1", kind)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.ReservedUnits != 0 {
		t.Fatalf("reserved = %v, want 0（建任务失败必须释放）", q.ReservedUnits)
	}
}

func TestStaticExportDoesNotTouchAnimationQuota(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 300_000)
	store := newUsageTestStore(t)
	server := animationQuotaServer(t, objects, store, 1) // 上限 1 分钟：若静态导出也预占，这里必然 429

	body := `{"format":"mp4","timelineKey":"tenant-1/project-1/narration/timeline/tl.json",` +
		`"pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	resp := postAnimationExport(t, server.URL, body, "static-export-1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("static export status = %d, want 201（静态导出不该受动画额度约束）", resp.StatusCode)
	}
	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
	q, err := store.GetQuota(context.Background(), "tenant-1", kind)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.ReservedUnits != 0 || q.ConsumedUnits != 0 {
		t.Fatalf("reserved/consumed = %v/%v, want 0/0", q.ReservedUnits, q.ConsumedUnits)
	}
}

// TestAnimationQuotaDoesNotOversellUnderConcurrency 是"额度必须原子"的反证用例：
// 30 个并发请求、上限 10 分钟、每单 4 分钟 —— 只有 2 单能成功（4+4=8，第三单 12 > 10）。
//
// 用真的 SQLite 账本而不是 fake：这里要验证的正是"条件更新在并发下的行为"，
// 换成内存实现等于把被测对象换掉了。
func TestAnimationQuotaDoesNotOversellUnderConcurrency(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 240_000) // 4 分钟/单
	store := newUsageTestStore(t)
	server := animationQuotaServer(t, objects, store, 10)

	const requests = 30
	var created, rejected, other atomic.Int32
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func(i int) {
			defer wg.Done()
			resp := postAnimationExport(t, server.URL, animationExportBody(timelineKey), fmt.Sprintf("oversell-%02d", i))
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusTooManyRequests:
				rejected.Add(1)
			default:
				other.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := created.Load(); got != 2 {
		t.Fatalf("created = %d, want 2（10 分钟上限 / 每单 4 分钟）", got)
	}
	if got := rejected.Load(); got != requests-2 {
		t.Fatalf("rejected = %d, want %d (other=%d)", got, requests-2, other.Load())
	}
	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
	q, err := store.GetQuota(context.Background(), "tenant-1", kind)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.ReservedUnits != 8 {
		t.Fatalf("reserved = %v, want 8（超卖会让它大于上限 10）", q.ReservedUnits)
	}
	if q.ReservedUnits+q.ConsumedUnits > q.LimitUnits {
		t.Fatalf("reserved+consumed = %v > limit %v", q.ReservedUnits+q.ConsumedUnits, q.LimitUnits)
	}
}

// 策略是上限的唯一来源：把 max_minutes 改回 0（不限量）必须在**下一次导出**就生效。
// 若只在"配了上限"时才写额度表，改回不限量会留下上一版的 120——一个查不到的暗坑。
func TestAnimationQuotaLimitFollowsPolicy(t *testing.T) {
	objects := testObjects(t)
	const timelineKey = "tenant-1/project-1/narration/timeline/tl.json"
	seedTimeline(t, objects, timelineKey, 300_000) // 5 分钟
	store := newUsageTestStore(t)
	policy := &tenant.Policy{Animation: &animation.AnimationPolicy{Enabled: boolPtr(true), MaxMinutes: 4}}
	server := animationQuotaServerWithPolicy(t, objects, store, policy)

	resp := postAnimationExport(t, server.URL, animationExportBody(timelineKey), "policy-change-1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429（上限 4 分钟 < 5 分钟）", resp.StatusCode)
	}

	policy.Animation.MaxMinutes = 0 // 运营改成不限量
	resp2 := postAnimationExport(t, server.URL, animationExportBody(timelineKey), "policy-change-2")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201（改成不限量后必须立刻生效）", resp2.StatusCode)
	}
	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
	q, err := store.GetQuota(context.Background(), "tenant-1", kind)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if q.LimitUnits != -1 {
		t.Fatalf("limit = %v, want -1（策略改回不限量后额度行必须跟着变）", q.LimitUnits)
	}
}

func boolPtr(b bool) *bool { return &b }

func intPtr(n int) *int { return &n }
