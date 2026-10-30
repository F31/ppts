package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
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
func NewBundleExportHandler(jobs JobCreator, members membership.Reader, recorder audit.Recorder,
	specs animstore.Store, gate AnimationGate, probe AnimationProbe,
	objects objectstore.ObjectStore, usageStore usage.Store) *BundleExportHandler {
	if gate == nil {
		gate = deployGate{}
	}
	return &BundleExportHandler{jobs: jobs, members: members, recorder: recorder,
		specs: specs, gate: gate, probe: probe, objects: objects,
		quota: &animationQuota{store: usageStore, objects: objects}}
}

type BundleExportHandler struct {
	jobs     JobCreator
	members  membership.Reader
	recorder audit.Recorder
	// 动画三件套与导出选项端点同源：入口必须自己判一次"这个租户此刻能不能出动画"，
	// 不能假设客户端一定先问过选项端点。
	specs animstore.Store
	gate  AnimationGate
	probe AnimationProbe
	// objects 只用于额度预估（读时间轴总时长）。额度必须按"这次要渲多少分钟"预占，
	// 而请求里只有 timelineKey —— 页数与时长都在时间轴本体里。
	objects objectstore.ObjectStore
	// quota 是动画渲染额度的受理侧（M4 §7.5）；store 为 nil 时全部操作退化为不限。
	quota *animationQuota
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
	// BurnSubtitles / IncludeNotes 与 Connect 入口同名同义。
	//
	// 这两个字段是本轮补的：mp4 带动画时必须走本端点（proto enum 加不了字段，见文件头），
	// 而本端点此前只读尺寸与帧率——同一种"mp4 导出"从两个入口进来会得到不同的字幕行为，
	// 且差异只在成片里看得出来。补齐后两个入口对 mp4 语义一致。
	BurnSubtitles bool `json:"burnSubtitles"`
	IncludeNotes  bool `json:"includeNotes"`
	// Animation 是动画通道的输入（只对 mp4 有效）。nil / 空 specs 都是"不要动画"，
	// 两者得到同一个快照键（见 app.SnapshotBytes）。
	//
	// 为什么规格身份要由客户端提交而不是后端取最新版：导出是对一份输入的承诺，
	// 排队期间规格被改，产物就不同；键若不随之改变，步骤去重会把新任务并进旧产物。
	Animation *bundleAnimationRequest `json:"animation"`
	// IdempotencyKey 是 header 的重试兜底：部分客户端无法设置自定义 header。
	IdempotencyKey string `json:"idempotencyKey"`
}

// bundleAnimationRequest 是动画选项的请求形状，与 app.AnimationOptions 一一对应。
// 单独定义而不直接反序列化成 app 类型：入口需要一个"可校验的原始形状"，
// 校验通过后才转成快照字段（见 animationExportOptions）。
type bundleAnimationRequest struct {
	Specs []bundleAnimationSpec `json:"specs"`
}

type bundleAnimationSpec struct {
	SlideID          string `json:"slideId"`
	Language         string `json:"language"`
	SourceRevisionNo int    `json:"sourceRevisionNo"`
	Revision         int64  `json:"revision"`
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
		BurnSubtitles: req.BurnSubtitles, IncludeNotes: req.IncludeNotes,
	}
	animation, err := animationExportOptions(string(format), req.Animation)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	// 勾了动画但此刻渲不出来（部署关掉 / 渲染服务不可达）时，**把动画从快照里摘掉**再降级：
	// 快照是"这次导出依据什么"的承诺，同一份快照必须对应同一份产物。
	// 若留着动画照着静态渲，这个快照键就会被写成静态字节——之前那份同键的动画产物被覆盖，
	// 用户下次下载"动画版"拿到的其实是静态版，而且全程没有任何报错。
	capability := h.gate.Resolve(r.Context(), principal.TenantID)
	dropped := ""
	if animation != nil {
		if reason := animationUnavailableReason(r.Context(), h.specs, capability, h.probe); reason != "" {
			animation, dropped = nil, reason
		}
	}
	// 额度预占必须在建任务之前：超限应当是**受理失败**，不是"任务入队后失败"。
	// 先建任务再在 worker 里拒绝，就是设计 §7.5 禁止的"静默排队"。
	//
	// 只对真的要走动画的请求预占：静态导出不该占渲染额度，否则用户导出十次静态版
	// 就把动画额度耗光了。
	var reserved *usage.Reservation
	if animation != nil {
		res, _, qerr := h.quota.Reserve(r.Context(), principal.TenantID, idempotencyKey, timelineKey, capability.MaxMinutes)
		if qerr != nil {
			var exceeded *quotaExhaustedError
			if errors.As(qerr, &exceeded) {
				writeQuotaExceeded(w, exceeded)
				return
			}
			writeConnectError(w, connect.NewError(connect.CodeInternal, qerr))
			return
		}
		reserved = res
	}
	// 预占是幂等的：同一幂等键重放返回既有记录（Created=false）。
	// 只有"本次新建"的预占才允许在后续失败时释放——否则会把重放请求对应的在途任务额度放掉。
	releaseReservation := func() {
		if reserved == nil || !reserved.Created {
			return
		}
		kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(time.Now()))
		err := h.quota.store.Release(r.Context(), principal.TenantID, idempotencyKey, kind)
		if errors.Is(err, usage.ErrReservationNotFound) || errors.Is(err, usage.ErrReservationReleased) || errors.Is(err, usage.ErrReservationSettled) {
			return
		}
	}
	snapshot.Animation = animation
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
	// 与 Connect 入口共用同一份规范字节（见 app.SnapshotBytes）。
	snapshotBytes, err := app.SnapshotBytes(snapshot)
	if err != nil {
		releaseReservation()
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	job, err := h.jobs.Create(r.Context(), principal.TenantID, projectID, string(pipeline.KindExport), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		releaseReservation()
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	if job.InputSnapshot != string(snapshotBytes) {
		// 幂等键被另一份请求占用：本次没有产生任何任务，预占必须放掉。
		releaseReservation()
		writeConnectError(w, connect.NewError(connect.CodeAlreadyExists,
			errors.New("Idempotency-Key was already used for a different request")))
		return
	}
	recordExport(r.Context(), h.recorder, principal.TenantID, principal.UserID, "export.create", job.ID,
		map[string]any{"project_id": projectID, "format": string(format), "entry": "rest"})
	out := map[string]any{"jobId": job.ID}
	if dropped != "" {
		// 降级事实必须回传给调用方：否则前端只能说"导出成功"，用户以为拿到了动效视频。
		out["animationDropped"] = dropped
	}
	writeJSON(w, http.StatusCreated, out)
}
