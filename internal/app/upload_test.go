//go:build pg

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/upload"
)

func uploadClient(t *testing.T, env *appEnv) *UploadService {
	t.Helper()
	return NewUploadService(env.uploads, env.projects, env.jobs, env.objects)
}

func TestUploadDirectFlow(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	svc := uploadClient(t, env)

	res, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: appProject, Filename: "demo.pptx", SizeBytes: int64(len(deckBytes(t))),
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if res.ObjectKey == "" || len(res.SignedURLs) != 1 {
		t.Fatalf("result = %+v", res)
	}

	// 模拟直传：把字节写入本地对象存储异常键。
	key, err := objectstore.Parse(res.ObjectKey)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	data := deckBytes(t)
	if err := env.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	revID, jobID, err := svc.CompleteUpload(ctx, appTenant, res.Session.ID, hash, int64(len(data)))
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if revID == "" || jobID == "" {
		t.Fatalf("result = %q %q", revID, jobID)
	}
	rev, err := env.projects.GetSourceRevision(ctx, appTenant, appProject, 1)
	if err != nil {
		t.Fatalf("GetSourceRevision: %v", err)
	}
	if rev.DisplayName != "demo.pptx" {
		t.Fatalf("display name = %q, want original filename", rev.DisplayName)
	}

	// 幂等重放：同上传会话返回同一源版本与任务。
	revID2, jobID2, err := svc.CompleteUpload(ctx, appTenant, res.Session.ID, hash, int64(len(data)))
	if err != nil {
		t.Fatalf("CompleteUpload#2: %v", err)
	}
	if revID2 != revID || jobID2 != jobID {
		t.Fatalf("idempotent mismatch: %q/%q vs %q/%q", revID2, jobID2, revID, jobID)
	}

	// 任务快照指向源版本与对象键。
	job, err := env.jobs.Get(ctx, jobID, appTenant)
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}
	if job.Kind != pipeline.KindParse || job.IDempotencyKey != res.Session.ID {
		t.Fatalf("job = %+v", job)
	}
	var snap ParseSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.SourceRevisionID != revID || snap.ObjectKey == "" || !strings.HasSuffix(snap.ObjectKey, ".pptx") {
		t.Fatalf("snapshot = %+v", snap)
	}

	// SourceRevision 已落到对象键与哈希。
	parsed, _ := objectstore.Parse(snap.ObjectKey)
	if parsed.TenantID != appTenant || parsed.ProjectID != appProject {
		t.Fatalf("source key = %+v", parsed)
	}
	rc, _, err := env.objects.Get(ctx, parsed)
	if err != nil {
		t.Fatalf("Get source: %v", err)
	}
	rc.Close()
}

func TestUploadRejectsBadInputAndStaleTransitions(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	svc := uploadClient(t, env)

	// 非法项目 → 错误。
	if _, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: "missing", Filename: "demo.pptx", SizeBytes: 4,
	}); err == nil {
		t.Fatalf("CreateUpload for missing project: want error")
	}

	// 超限文件被拒绝。
	if _, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: appProject, Filename: "big.pptx", SizeBytes: MaxUploadBytes + 1,
	}); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("oversize: got %v want ErrUploadTooLarge", err)
	}

	// 对象未上传 → FailedPrecondition 语义。
	res, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: appProject, Filename: "demo.pptx", SizeBytes: 4,
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if _, _, err := svc.CompleteUpload(ctx, appTenant, res.Session.ID, "hash", 4); !errors.Is(err, ErrUploadNotReceived) {
		t.Fatalf("complete without object: got %v want ErrUploadNotReceived", err)
	}

	// 中止后完成被拒绝。
	if err := svc.AbortUpload(ctx, appTenant, res.Session.ID); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	if _, _, err := svc.CompleteUpload(ctx, appTenant, res.Session.ID, "hash", 4); !errors.Is(err, ErrUploadAborted) {
		t.Fatalf("complete aborted: got %v want ErrUploadAborted", err)
	}
}

func TestUploadAbortCleansObject(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()
	svc := uploadClient(t, env)

	res, err := svc.CreateUpload(ctx, appTenant, UploadRequest{
		ProjectID: appProject, Filename: "demo.pptx", SizeBytes: 4,
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	key, _ := objectstore.Parse(res.ObjectKey)
	if err := env.objects.Put(ctx, key, bytes.NewReader([]byte("data")), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AbortUpload(ctx, appTenant, res.Session.ID); err != nil {
		t.Fatalf("AbortUpload: %v", err)
	}
	if _, _, err := env.objects.Get(ctx, key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatalf("work object should be gone: got %v", err)
	}
	// 已完成会话不能中止 → ErrAlreadyCompleted。
	done, _ := svc.CreateUpload(ctx, appTenant, UploadRequest{ProjectID: appProject, Filename: "a.pptx", SizeBytes: 4})
	dkey, _ := objectstore.Parse(done.ObjectKey)
	if err := env.objects.Put(ctx, dkey, bytes.NewReader([]byte("data")), objectstore.ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("data"))
	if _, _, err := svc.CompleteUpload(ctx, appTenant, done.Session.ID, hex.EncodeToString(want[:]), 4); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if err := svc.AbortUpload(ctx, appTenant, done.Session.ID); !errors.Is(err, upload.ErrAlreadyCompleted) {
		t.Fatalf("abort completed: got %v want ErrAlreadyCompleted", err)
	}
}

// TestCreateSourceRevisionIdempotentByUpload 覆盖崩溃窗口：
// CompleteUpload 在 CreateSourceRevision 与 uploads.Complete 之间失败并重试时，
// 同一 upload_id 不得重复创建源版本或重复递增 current_revision。
func TestCreateSourceRevisionIdempotentByUpload(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()

	in := project.NewSourceRevision{
		ProjectID: appProject, SourceHash: "hash-crash", ObjectKey: appTenant + "/" + appProject + "/src/source/hash-crash.pptx",
		ParserVersion: ParserVersion, UploadID: "upload-crash-1",
	}
	first, err := env.projects.CreateSourceRevision(ctx, appTenant, in)
	if err != nil {
		t.Fatalf("CreateSourceRevision: %v", err)
	}
	second, err := env.projects.CreateSourceRevision(ctx, appTenant, in)
	if err != nil {
		t.Fatalf("CreateSourceRevision retry: %v", err)
	}
	if second.ID != first.ID || second.RevisionNo != first.RevisionNo {
		t.Fatalf("retry duplicated source revision: %+v vs %+v", second, first)
	}
	p, err := env.projects.GetProject(ctx, appTenant, appProject)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.CurrentRevision != first.RevisionNo {
		t.Fatalf("current_revision bumped on retry: got %d want %d", p.CurrentRevision, first.RevisionNo)
	}

	// 不同上传会话仍应创建新版本。
	other, err := env.projects.CreateSourceRevision(ctx, appTenant, project.NewSourceRevision{
		ProjectID: appProject, SourceHash: "hash-2", ObjectKey: appTenant + "/" + appProject + "/src/source/hash-2.pptx",
		ParserVersion: ParserVersion, UploadID: "upload-crash-2",
	})
	if err != nil {
		t.Fatalf("CreateSourceRevision other: %v", err)
	}
	if other.RevisionNo != first.RevisionNo+1 {
		t.Fatalf("new upload should create next revision: got %d want %d", other.RevisionNo, first.RevisionNo+1)
	}
}
