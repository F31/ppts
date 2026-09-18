// Package app 组装领域端口与适配器，实现业务用例（G1：上传/解析垂直链路）。
// 不播放 JSON 序列化策略；auth 上下文与 tenant 授权由 HTTP/Connect 层注入，
// 本包所有方法显式接收已授权的 tenantID。
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// ParserVersion 记录解析器版本，供 source revision 与缓存键追踪。
const ParserVersion = "go-pptx-v2.0.0"

// ParseSnapshot 是 parse 任务的输入快照（与任务强绑定，V4.0 §7.1 Job.input_snapshot）。
type ParseSnapshot struct {
	SourceRevisionID string `json:"sourceRevisionId"`
	ProjectID        string `json:"projectId"`
	ObjectKey        string `json:"objectKey"`
	RevisionNo       int    `json:"revisionNo"`
	ParserVersion    string `json:"parserVersion"`
}

// PageEntry 是渲染后单页与源页面的对应关系（页序与 slideId 对齐）。
type PageEntry struct {
	SlideID string `json:"slideId"`
	Index   int    `json:"index"` // 0 基页序
	Key     string `json:"key"`   // 页面 PNG 对象键
}

// PageManifest 是解析阶段渲染产物的清单（供播放服务按 timeline 页序取页面图）。
type PageManifest struct {
	RevisionNo int         `json:"revisionNo"`
	Renderer   string      `json:"renderer,omitempty"`
	Pages      []PageEntry `json:"pages"`
}

// ParseHandler 是解析任务的 worker handler：
// 读源对象 → DocumentReader.Inspect → 写回解析产物（document/features）→ 可选渲染页面 PNG。
type ParseHandler struct {
	objects  objectstore.ObjectStore
	reader   project.DocumentReader
	renderer render.SlideRenderer
	steps    interface {
		MarkStep(context.Context, pipeline.JobStep) error
	}
}

// NewParseHandler 创建 handler。
func NewParseHandler(objects objectstore.ObjectStore, reader project.DocumentReader) *ParseHandler {
	return &ParseHandler{objects: objects, reader: reader}
}

// WithRenderer 注入渲染器；未注入（或渲染失败）时解析仍成功，仅缺页面图。
func (h *ParseHandler) WithRenderer(r render.SlideRenderer) *ParseHandler {
	h.renderer = r
	return h
}

// WithSteps 注入步骤记录器，用于登记页面渲染结果。
func (h *ParseHandler) WithSteps(s interface {
	MarkStep(context.Context, pipeline.JobStep) error
}) *ParseHandler {
	h.steps = s
	return h
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
	h.renderPages(ctx, job, key, data, doc, snap)
	return nil
}

// renderPages 把源页面渲染为 PNG 并登记页序清单。页面图是可选增强：
// 渲染器缺失/不可用或渲染失败都不会让解析任务失败，仅记录失败步骤。
func (h *ParseHandler) renderPages(ctx context.Context, job *pipeline.Job, srcKey objectstore.ObjectKey, data []byte, doc *project.Document, snap ParseSnapshot) {
	if h.renderer == nil || h.steps == nil {
		return
	}
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID, StepType: "pages",
		StepKey: "pages:v1:" + srcKey.AssetID,
	}
	fail := func() {
		step.State = pipeline.StepFailed
		_ = h.steps.MarkStep(ctx, step)
	}
	res, err := h.renderer.Render(ctx, bytes.NewReader(data), int64(len(data)), render.RenderOptions{})
	if err != nil || len(res.Pages) == 0 {
		fail()
		return
	}
	slideByIndex := make(map[int]string, len(doc.Pages))
	for _, pg := range doc.Pages {
		if pg != nil {
			slideByIndex[pg.Index] = pg.SlideID
		}
	}
	revision := srcRevString(snap.RevisionNo)
	entries := make([]PageEntry, 0, len(res.Pages))
	for _, page := range res.Pages {
		key := objectstore.ObjectKey{
			TenantID: srcKey.TenantID, ProjectID: snap.ProjectID, Revision: revision,
			AssetType: "render", AssetID: fmt.Sprintf("page-%04d", page.Index+1), Ext: "png",
		}
		if err := h.objects.Put(ctx, key, bytes.NewReader(page.PNG), objectstore.ObjectMeta{
			ContentType: "image/png", ContentHash: hashBytes(page.PNG), Size: int64(len(page.PNG)),
		}); err != nil {
			fail()
			return
		}
		entries = append(entries, PageEntry{SlideID: slideByIndex[page.Index], Index: page.Index, Key: key.String()})
	}
	manifest := PageManifest{RevisionNo: snap.RevisionNo, Renderer: res.Report.Renderer, Pages: entries}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		fail()
		return
	}
	manifestKey := objectstore.ObjectKey{
		TenantID: srcKey.TenantID, ProjectID: snap.ProjectID, Revision: revision,
		AssetType: "render", AssetID: "pages", Ext: "json",
	}
	if err := h.objects.Put(ctx, manifestKey, bytes.NewReader(manifestBytes), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(manifestBytes), Size: int64(len(manifestBytes)),
	}); err != nil {
		fail()
		return
	}
	step.State, step.ResultRef = pipeline.StepSuccess, manifestKey.String()
	_ = h.steps.MarkStep(ctx, step)
}

func srcRevString(n int) string {
	return fmt.Sprintf("src-%02d", n)
}
