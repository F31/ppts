package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/upload"
)

// 上传限制与签名 TTL。
const (
	// MaxUploadBytes 单文件上限（G1 单段直传）。
	MaxUploadBytes = 512 << 20
	// UploadURLTTL 预签名写链接有效期。
	UploadURLTTL = 15 * time.Minute
)

// 用例层错误哨兵：供 API transport 映射 Connect 错误码。
var (
	ErrUploadInvalid       = errors.New("upload: invalid filename, project or size")
	ErrUploadSizeMismatch  = errors.New("upload: size mismatch")
	ErrUploadHashMismatch  = errors.New("upload: content hash mismatch")
	ErrUploadNotReceived   = errors.New("upload: object not received; upload the file first")
	ErrUploadAborted       = errors.New("upload: session aborted")
	ErrUploadTooLarge      = errors.New("upload: file exceeds size limit")
	ErrUploadStateConflict = errors.New("upload: concurrent state transition")
)

// JobCreator 是上传用例所需的窄任务创建能力（与 API transport 的 JobCreator 一致）。
type JobCreator interface {
	Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*pipeline.Job, error)
}

// UploadService 实现"授权直传 → 校验 → 源版本 → 解析任务"用例（G1-1/G1-2）。
// 不直接暴露给 HTTP；由 internal/api 的 transport 层持有并映射错误码。
type UploadService struct {
	uploads  upload.Store
	projects project.ProjectStore
	jobs     JobCreator
	objects  objectstore.ObjectStore
}

// NewUploadService 创建服务。
func NewUploadService(uploads upload.Store, projects project.ProjectStore, jobs JobCreator, objects objectstore.ObjectStore) *UploadService {
	return &UploadService{uploads: uploads, projects: projects, jobs: jobs, objects: objects}
}

// UploadRequest 是 CreateUpload 的输入。
type UploadRequest struct {
	ProjectID         string
	Filename          string
	SizeBytes         int64
	ContentType       string
	DeleteSourceAfter bool
}

// UploadResult 是 CreateUpload 的输出（预签名链接 + 服务端分配的键）。
type UploadResult struct {
	Session    *upload.UploadSession
	SignedURLs []string
	ObjectKey  string
}

// CreateUpload 校验项目归属、分配受限对象键并签发预签名写链接。
func (s *UploadService) CreateUpload(ctx context.Context, tenantID string, in UploadRequest) (*UploadResult, error) {
	if in.ProjectID == "" || in.Filename == "" {
		return nil, ErrUploadInvalid
	}
	if in.SizeBytes <= 0 {
		return nil, ErrUploadInvalid
	}
	if in.SizeBytes > MaxUploadBytes {
		return nil, ErrUploadTooLarge
	}
	if _, err := s.projects.GetProject(ctx, tenantID, "", in.ProjectID); err != nil {
		return nil, fmt.Errorf("upload: project: %w", err)
	}

	id := newID()
	ext := sanitizeExt(in.Filename)
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: in.ProjectID,
		Revision: "uploads", AssetType: "work", AssetID: id, Ext: ext,
	}
	ses, err := s.uploads.Create(ctx, upload.NewUpload{
		ID: id, TenantID: tenantID, ProjectID: in.ProjectID,
		Filename: in.Filename, ContentType: in.ContentType, ObjectKey: key.String(),
		SizeBytes: in.SizeBytes, DeleteSourceAfter: in.DeleteSourceAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("upload: create session: %w", err)
	}
	signed, err := s.objects.SignedURL(ctx, key, objectstore.OpWrite, UploadURLTTL)
	if err != nil {
		return nil, fmt.Errorf("upload: sign write url: %w", err)
	}
	return &UploadResult{Session: ses, SignedURLs: []string{signed}, ObjectKey: key.String()}, nil
}

// CompleteUpload 校验对象存在、大小与哈希一致，然后创建源版本与解析任务。
// 已完成会话幂等返回已存结果；中止会话拒绝。
func (s *UploadService) CompleteUpload(ctx context.Context, tenantID, uploadID, expectedHash string, sizeBytes int64) (sourceRevisionID, jobID string, err error) {
	ses, err := s.uploads.Get(ctx, tenantID, uploadID)
	if err != nil {
		return "", "", err
	}
	switch ses.State {
	case upload.StateAborted:
		return "", "", ErrUploadAborted
	case upload.StateCompleted:
		return ses.SourceRevisionID, ses.JobID, nil
	}
	if sizeBytes != ses.SizeBytes {
		return "", "", ErrUploadSizeMismatch
	}

	key, err := objectstore.Parse(ses.ObjectKey)
	if err != nil {
		return "", "", err
	}
	rc, _, err := s.objects.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return "", "", ErrUploadNotReceived
		}
		return "", "", fmt.Errorf("upload: read uploaded object: %w", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return "", "", err
	}
	if int64(len(data)) != sizeBytes {
		return "", "", ErrUploadSizeMismatch
	}
	sum := sha256.Sum256(data)
	actualHash := hex.EncodeToString(sum[:])
	if expectedHash == "" || !strings.EqualFold(actualHash, expectedHash) {
		return "", "", ErrUploadHashMismatch
	}

	srcKey := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: ses.ProjectID,
		Revision: "src", AssetType: "source", AssetID: actualHash, Ext: "pptx",
	}
	if err := s.objects.Put(ctx, srcKey, bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		ContentHash: actualHash, Size: int64(len(data)),
	}); err != nil {
		return "", "", fmt.Errorf("upload: store source: %w", err)
	}

	rev, err := s.projects.CreateSourceRevision(ctx, tenantID, project.NewSourceRevision{
		ProjectID: ses.ProjectID, SourceHash: actualHash, ObjectKey: srcKey.String(), ParserVersion: ParserVersion,
		UploadID: uploadID, Filename: ses.Filename,
	})
	if err != nil {
		return "", "", fmt.Errorf("upload: create source revision: %w", err)
	}
	snap := ParseSnapshot{
		SourceRevisionID: rev.ID, TenantID: tenantID, ProjectID: ses.ProjectID, ObjectKey: srcKey.String(),
		RevisionNo: rev.RevisionNo, ParserVersion: ParserVersion,
	}
	snapBytes, _ := marshalSnapshot(snap)
	job, err := s.jobs.Create(ctx, tenantID, ses.ProjectID, string(pipeline.KindParse), uploadID, snapBytes, time.Time{})
	if err != nil {
		return "", "", fmt.Errorf("upload: enqueue parse job: %w", err)
	}

	done, err := s.uploads.Complete(ctx, tenantID, uploadID, rev.ID, job.ID)
	if err != nil {
		// 并发完成：读取已存结果幂等返回。
		if errors.Is(err, upload.ErrAlreadyAborted) || errors.Is(err, upload.ErrStateConflict) {
			cur, gerr := s.uploads.Get(ctx, tenantID, uploadID)
			if gerr == nil && cur.State == upload.StateCompleted && cur.SourceRevisionID != "" {
				return cur.SourceRevisionID, cur.JobID, nil
			}
		}
		return "", "", err
	}
	if done.SourceRevisionID == "" {
		return "", "", ErrUploadStateConflict
	}
	return done.SourceRevisionID, done.JobID, nil
}

// AbortUpload 中止会话并清理已上传的临时对象。
func (s *UploadService) AbortUpload(ctx context.Context, tenantID, uploadID string) error {
	ses, err := s.uploads.Get(ctx, tenantID, uploadID)
	if err != nil {
		return err
	}
	switch ses.State {
	case upload.StateAborted:
		return nil
	case upload.StateCompleted:
		return upload.ErrAlreadyCompleted
	}
	if key, perr := objectstore.Parse(ses.ObjectKey); perr == nil {
		_ = s.objects.Delete(ctx, key)
	}
	_, err = s.uploads.Abort(ctx, tenantID, uploadID)
	return err
}

func marshalSnapshot(snap ParseSnapshot) (string, error) {
	b, err := json.Marshal(snap)
	return string(b), err
}

// sanitizeExt 提取并清洗扩展名，只允许小写字母数字，最长 10 字符。
func sanitizeExt(filename string) string {
	ext := ""
	if i := strings.LastIndexByte(filename, '.'); i >= 0 {
		ext = filename[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(ext) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 10 {
			break
		}
	}
	if b.Len() == 0 {
		return "pptx"
	}
	return b.String()
}

// newID 生成 32 位十六进制随机 ID（crypto/rand），用于会话与临时对象键。
func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// 熵源不可用时退回确定性内容：仅影响 ID 唯一性风险，不阻断服务。
		return "upload-" + hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(buf[:])
}
