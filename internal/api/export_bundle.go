package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
)

// RegisterBundleExport 挂载扩展产物格式的导出入口（原生 REST）。
//
// 为什么不是 Connect RPC：新增格式需要在 proto enum 里加 `ARTIFACT_FORMAT_HTML_BUNDLE`
// 并重新生成 pb.go，而本仓库的构建环境没有 buf/protoc（沿用 `/api/context-rules`、
// 动画规格端点的同一条先例）。既有 Connect `CreateExport` 保持原样——它支持的格式与
// 语义完全不变，这里只是多一个入口，不构成第二套导出逻辑：snapshot 结构与 job 消费
// 路径与 Connect 入口完全一致（同一个 JobCreator + 同一个 ExportHandler）。
//
// 端点：POST /api/projects/{projectId}/exports
func NewBundleExportHandler(jobs JobCreator, members membership.Reader, recorder audit.Recorder) *BundleExportHandler {
	return &BundleExportHandler{jobs: jobs, members: members, recorder: recorder}
}

type BundleExportHandler struct {
	jobs     JobCreator
	members  membership.Reader
	recorder audit.Recorder
}

func (h *BundleExportHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if h == nil || h.jobs == nil {
		return
	}
	mux.Handle("POST /api/projects/{projectId}/exports", auth(http.HandlerFunc(h.create)))
}

// maxBundleExportBodyBytes 限制请求体：正常请求只有几个 key + 页图 key 列表（每页 <200 字节）。
const maxBundleExportBodyBytes = 1 << 20

type bundleExportRequest struct {
	// Format 缺省为 mp4（与 Connect 入口的默认行为一致）；本端点主要供 html_bundle 使用。
	Format      string   `json:"format"`
	TimelineKey string   `json:"timelineKey"`
	PagePNGKeys []string `json:"pagePngKeys"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	FPS         int      `json:"fps"`
	// IdempotencyKey 是 header 的重试兜底：部分客户端无法设置自定义 header。
	IdempotencyKey string `json:"idempotencyKey"`
}

func (h *BundleExportHandler) create(w http.ResponseWriter, r *http.Request) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), h.members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required")))
		return
	}
	var req bundleExportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBundleExportBodyBytes))
	if err := decoder.Decode(&req); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid request body")))
		return
	}
	format := artifact.Format(strings.TrimSpace(req.Format))
	if format == "" {
		format = artifact.FormatMP4
	}
	switch format {
	case artifact.FormatMP4, artifact.FormatWebProject, artifact.FormatHTMLBundle:
	default:
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			errors.New("unsupported export format on this endpoint")))
		return
	}
	timelineKey := strings.TrimSpace(req.TimelineKey)
	if timelineKey == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("timeline_key is required")))
		return
	}
	if err := ensureTenantKey(principal.TenantID, timelineKey); err != nil {
		writeConnectError(w, err)
		return
	}
	// 与 Connect 入口同一约束：时间轴必须属于本项目，否则会产出"内容来自 A、挂在 B 名下"的成品。
	if err := ensureProjectKey(principal.TenantID, projectID, timelineKey); err != nil {
		writeConnectError(w, err)
		return
	}
	pageKeys := append([]string(nil), req.PagePNGKeys...)
	for _, key := range pageKeys {
		if err := ensureTenantKey(principal.TenantID, key); err != nil {
			writeConnectError(w, err)
			return
		}
	}
	if format == artifact.FormatMP4 && len(pageKeys) == 0 {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			errors.New("page_png_keys are required for mp4 export")))
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	}
	if idempotencyKey == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			errors.New("Idempotency-Key header is required")))
		return
	}
	snapshot := app.ExportSnapshot{
		Format: format, TimelineKey: timelineKey, PagePNGKeys: pageKeys,
		FPS: req.FPS, Width: req.Width, Height: req.Height,
	}
	// 与 Connect 入口保持同一套默认值：缺省 1080p30，避免同一份时间轴因入口不同产出不同尺寸。
	if snapshot.FPS <= 0 {
		snapshot.FPS = 30
	}
	if snapshot.Width <= 0 {
		snapshot.Width = 1920
	}
	if snapshot.Height <= 0 {
		snapshot.Height = 1080
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	job, err := h.jobs.Create(r.Context(), principal.TenantID, projectID, string(pipeline.KindExport), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	if job.InputSnapshot != string(snapshotBytes) {
		writeConnectError(w, connect.NewError(connect.CodeAlreadyExists,
			errors.New("Idempotency-Key was already used for a different request")))
		return
	}
	recordExport(r.Context(), h.recorder, principal.TenantID, principal.UserID, "export.create", job.ID,
		map[string]any{"project_id": projectID, "format": string(format), "entry": "rest"})
	writeJSON(w, http.StatusCreated, map[string]string{"jobId": job.ID})
}
