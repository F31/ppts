// Package app 组装领域端口与适配器，实现业务用例（G1：上传/解析垂直链路）。
// 不播放 JSON 序列化策略；auth 上下文与 tenant 授权由 HTTP/Connect 层注入，
// 本包所有方法显式接收已授权的 tenantID。
package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// ParserVersion 记录解析器版本，供 source revision 与缓存键追踪。
const ParserVersion = "go-pptx-v1.0.1"

// IngestService 实现"上传 → 存源 → 建源版本 → 入队解析任务"（G1-1/G1-2）。
type IngestService struct {
	projects project.ProjectStore
	jobs     pipeline.Store
	objects  objectstore.ObjectStore
}

// NewIngestService 创建服务。
func NewIngestService(projects project.ProjectStore, jobs pipeline.Store, objects objectstore.ObjectStore) *IngestService {
	return &IngestService{projects: projects, jobs: jobs, objects: objects}
}

// IngestResult 是入库结果（源版本 + 已入队解析任务）。
type IngestResult struct {
	SourceRevision *project.SourceRevision
	ParseJob       *pipeline.Job
	ObjectKey      string
}

// ParseSnapshot 是 parse 任务的输入快照（与任务强绑定，V4.0 §7.1 Job.input_snapshot）。
type ParseSnapshot struct {
	SourceRevisionID string `json:"sourceRevisionId"`
	ProjectID        string `json:"projectId"`
	ObjectKey        string `json:"objectKey"`
	RevisionNo       int    `json:"revisionNo"`
	ParserVersion    string `json:"parserVersion"`
}

// Ingest 读取源字节，计算哈希写入对象存储（键含租户/项目前缀），
// 原子创建源版本，再以源哈希为幂等键入队解析任务。
// 对象已写入但后续失败会留下孤儿对象（G3 清理策略覆盖）。
func (s *IngestService) Ingest(ctx context.Context, tenantID, projectID string, src io.Reader) (*IngestResult, error) {
	data, hash, err := readAndHash(src)
	if err != nil {
		return nil, err
	}
	if projectID == "" || tenantID == "" {
		return nil, errors.New("app: tenantID and projectID are required")
	}

	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: "src", AssetType: "source", AssetID: hash, Ext: "pptx",
	}
	if err := s.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		ContentHash: hash, Size: int64(len(data)),
	}); err != nil {
		return nil, fmt.Errorf("app: store source: %w", err)
	}

	rev, err := s.projects.CreateSourceRevision(ctx, tenantID, project.NewSourceRevision{
		ProjectID: projectID, SourceHash: hash, ObjectKey: key.String(), ParserVersion: ParserVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("app: create source revision: %w", err)
	}

	snap, _ := json.Marshal(ParseSnapshot{
		SourceRevisionID: rev.ID, ProjectID: projectID, ObjectKey: key.String(),
		RevisionNo: rev.RevisionNo, ParserVersion: ParserVersion,
	})
	job, err := s.jobs.Create(ctx, tenantID, projectID, string(pipeline.KindParse), hash, string(snap), time.Time{})
	if err != nil {
		return nil, fmt.Errorf("app: enqueue parse job: %w", err)
	}
	return &IngestResult{SourceRevision: rev, ParseJob: job, ObjectKey: key.String()}, nil
}

// ParseHandler 是解析任务的 worker handler：
// 读源对象 → DocumentReader.Inspect → 写回解析产物（document/features）→ nil。
type ParseHandler struct {
	objects objectstore.ObjectStore
	reader  project.DocumentReader
}

// NewParseHandler 创建 handler。
func NewParseHandler(objects objectstore.ObjectStore, reader project.DocumentReader) *ParseHandler {
	return &ParseHandler{objects: objects, reader: reader}
}

// Handle 实现 pipeline.HandlerFunc。
func (h *ParseHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	var snap ParseSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		return fmt.Errorf("parse: invalid input snapshot: %w", err)
	}
	key, err := objectstore.Parse(snap.ObjectKey)
	if err != nil {
		return err
	}
	rc, meta, err := h.objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("parse: read source object: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	doc, err := h.reader.Inspect(ctx, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	docBytes, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	outKey := objectstore.ObjectKey{
		TenantID: key.TenantID, ProjectID: snap.ProjectID,
		Revision: srcRevString(snap.RevisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	if err := h.objects.Put(ctx, outKey, bytes.NewReader(docBytes), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: meta.ContentHash,
	}); err != nil {
		return fmt.Errorf("parse: write extracted document: %w", err)
	}
	return nil
}

func readAndHash(r io.Reader) ([]byte, string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func srcRevString(n int) string {
	return fmt.Sprintf("src-%02d", n)
}
