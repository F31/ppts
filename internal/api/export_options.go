package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// 导出选项探测（tasks 3.4）。
//
// 前端要回答一个问题才能渲染「动画视频」这一项：**这次导出真的能出动画吗**。
// 答案由三段组成，缺一即不可用，且必须说明缺的是哪一段——把三项混成一个 bool，
// 用户只会看到"勾选框是灰的"，无从知道是自己没配动效、部署没开、还是渲染服务挂了。
//
//   1. 动画规格存储可用（部署未上线时整个能力不存在）；
//   2. 部署开启了动画通道（写路径开关）；
//   3. 渲染服务此刻可达（真实探测，不是读配置——配置写着地址但容器没起来是常态）。
//
// 端点同时返回逐页规格身份：前端把这批身份**原样**放进导出请求的 animation.specs。
// 不在这里替前端"顺便"提交当前最新版：导出是快照，键必须固定用户看到的那一版；
// 排队期间规格被改，导出侧会因 Revision 不符把该页降级为静态并留痕，
// 而不是拿新版渲下去、让同一份快照键产出两份不同的视频。

// AnimationProbe 探测渲染服务是否可达。用接口而不是直接收 *render.Client：
// handler 只需要"能不能用"，测试也就不必起一个真渲染服务。
type AnimationProbe interface {
	Check(ctx context.Context) error
}

// ExportOptionsHandler 提供导出前的动画能力与逐页规格身份。
type ExportOptionsHandler struct {
	specs    animstore.Store
	objects  objectstore.ObjectStore
	projects project.ProjectStore
	members  membership.Reader
	// enabled 是部署侧的动画通道开关，与动画规格写路径同一个 env（PPTS_ANIMATION_ENABLED）。
	enabled bool
	probe   *cachedProbe
}

// NewExportOptionsHandler 创建 handler。specs 为 nil 时不注册路由（能力未上线）。
func NewExportOptionsHandler(specs animstore.Store, objects objectstore.ObjectStore,
	projects project.ProjectStore, members membership.Reader, enabled bool, probe AnimationProbe) *ExportOptionsHandler {
	h := &ExportOptionsHandler{
		specs: specs, objects: objects, projects: projects, members: members, enabled: enabled,
	}
	if probe != nil {
		h.probe = &cachedProbe{client: probe, ttl: defaultProbeTTL}
	}
	return h
}

// defaultProbeTTL 是渲染服务探测结论的缓存时长。
//
// 缓存不是为了省一次 HTTP，而是因为这个端点会被导出对话框反复打开：
// 每次都探，渲染服务的一次慢响应就会拖成"打开对话框要等两秒"。
// 10 秒足够短——服务修好后用户重开一次对话框就能看到变绿。
const defaultProbeTTL = 10 * time.Second

// cachedProbe 给探测加 TTL 缓存。成功与失败都缓存：失败不缓存会让
// "服务挂着"变成"每次打开对话框都多一次超时等待"。
type cachedProbe struct {
	client AnimationProbe
	ttl    time.Duration
	now    func() time.Time // nil 时用 time.Now（测试注入）

	mu   sync.Mutex
	at   time.Time
	last error
}

func (p *cachedProbe) Check(ctx context.Context) error {
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && now().Sub(p.at) < p.ttl {
		return p.last
	}
	p.last = p.client.Check(ctx)
	p.at = now()
	return p.last
}

func (h *ExportOptionsHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	// 即使 specs 为 nil 也注册：这个端点回答的是"能不能出动画"，"这个部署没有动画能力"
	// 本身就是有效答案。返回 404 会让前端只能显示"接口不存在"，而无法说明原因。
	if h == nil {
		return
	}
	mux.Handle("GET /api/projects/{projectId}/exports/animation-options",
		auth(http.HandlerFunc(h.animationOptions)))
}

// exportAnimationOptions 是动画导出选项的响应。
//
// Pages 只列时间轴里真实存在的页（顺序即时间轴顺序），revision=0 表示该页没有规格、
// 导出时按静态渲染。**不省略这一类页**：用户需要看到"12 页里只有 5 页配了动效"，
// 而不是自己数。
type exportAnimationOptions struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	// Pages 为 nil（而非空数组）时表示没有给出 timelineKey，只回答了能力问题。
	Pages []exportAnimationPage `json:"pages,omitempty"`

	HasTimeline bool `json:"hasTimeline"`
}

type exportAnimationPage struct {
	SlideID          string `json:"slideId"`
	Language         string `json:"language"`
	SourceRevisionNo int    `json:"sourceRevisionNo"`
	Revision         int64  `json:"revision"`
}

// animationOptions 处理 GET /api/projects/{projectId}/exports/animation-options。
//
// query:
//
//	timelineKey  可选。给出时返回逐页规格身份；省略时只返回能力判定。
//	language     可选，默认 zh-CN（与 animation.Key.Normalize 一致）。
func (h *ExportOptionsHandler) animationOptions(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "project_id is required"})
		return
	}

	resp := exportAnimationOptions{}
	if reason := h.unavailableReason(r.Context()); reason != "" {
		resp.Reason = reason
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Enabled = true

	timelineKey := strings.TrimSpace(r.URL.Query().Get("timelineKey"))
	if timelineKey == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if err := ensureTenantKey(principal.TenantID, timelineKey); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := ensureProjectKey(principal.TenantID, projectID, timelineKey); err != nil {
		writeConnectError(w, err)
		return
	}
	language := strings.TrimSpace(r.URL.Query().Get("language"))

	// 页集合取自时间轴本体，而不是"项目当前版本的页列表"：导出的页顺序由时间轴决定，
	// 版本升级后项目页可能已经变了，按项目取会给出与成片对不上的身份。
	bundle, _, err := loadBundle(r.Context(), h.objects, principal.TenantID, timelineKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_timeline", "message": "timeline is not readable"})
		return
	}
	if bundle.Timeline == nil || len(bundle.Timeline.Slides) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_timeline", "message": "timeline has no slides"})
		return
	}
	sourceRevisionNo := bundle.Timeline.SourceRevisionNo
	if sourceRevisionNo <= 0 {
		// 历史时间轴没写来源版本：回落到项目当前版本，与讲稿/动画的 legacy 规则一致。
		sourceRevisionNo = h.currentRevision(r.Context(), principal, projectID)
	}

	resp.HasTimeline = true
	resp.Pages = make([]exportAnimationPage, 0, len(bundle.Timeline.Slides))
	for _, slide := range bundle.Timeline.Slides {
		slideID := strings.TrimSpace(slide.SlideID)
		if slideID == "" {
			// 时间轴里有空页 id 说明数据坏了；跳过而不是让整次探测失败——
			// 导出本身也会因为这一页拿不到规格而走静态，用户仍然能导出。
			continue
		}
		page := exportAnimationPage{
			SlideID: slideID, Language: language, SourceRevisionNo: sourceRevisionNo,
		}
		key := animation.Key{
			TenantID: principal.TenantID, ProjectID: projectID, SlideID: slideID,
			Language: language, SourceRevisionNo: sourceRevisionNo,
		}.Normalize()
		page.Language = key.Language
		if rev, err := h.specs.Get(r.Context(), key); err == nil && rev != nil {
			// 用存储里实际的维度回填：Get 在精确版本缺失时会回退 legacy(0)，
			// 前端若照抄请求时的 SourceRevisionNo，导出侧会再走一次回退、
			// 结果虽相同，但快照里记的就不是"实际依据的那一版"。
			page.SourceRevisionNo = rev.SourceRevisionNo
			page.Language = rev.Language
			page.Revision = rev.Revision
		}
		resp.Pages = append(resp.Pages, page)
	}
	writeJSON(w, http.StatusOK, resp)
}

// unavailableReason 返回不可用原因；空串表示可用。
//
// 顺序有意义：先报配置缺失，再报探测失败。反过来会出现"渲染服务不可达"掩盖
// "这个部署根本没开动画"——后者是更根本的原因，也更容易修。
func (h *ExportOptionsHandler) unavailableReason(ctx context.Context) string {
	if h.specs == nil {
		return "animation_not_available"
	}
	if !h.enabled {
		return "animation_disabled_by_deployment"
	}
	if h.probe == nil {
		return "renderer_not_configured"
	}
	if err := h.probe.Check(ctx); err != nil {
		return "renderer_unavailable: " + err.Error()
	}
	return ""
}

func (h *ExportOptionsHandler) currentRevision(ctx context.Context, principal Principal, projectID string) int {
	if h.projects == nil {
		return 0
	}
	proj, err := h.projects.GetProject(ctx, principal.TenantID, principal.UserID, projectID)
	if err != nil || proj == nil {
		return 0
	}
	return proj.CurrentRevision
}

// animationExportOptions 把请求体里的 specs 校验并转成快照字段。
//
// 校验放在入口而不是导出步骤里：导出步骤跑在 worker 上，出错只能让任务失败；
// 而这些问题（页 id 留空、版本号写 0、格式不支持动画）都是**请求本身**错了，
// 应该立刻回 400 并指出哪一项。放过去的话，用户会等一个跑完的静态视频，
// 然后发现"我明明勾了动画"。
func animationExportOptions(format string, in *bundleAnimationRequest) (*app.AnimationOptions, error) {
	if in == nil || len(in.Specs) == 0 {
		// 空 specs 与"没传"等价：两者都不渲染动画，产物同源，快照键也必须同源
		// （app.SnapshotBytes 也做了同样的归一，这里提前归一避免下游再判一次）。
		return nil, nil
	}
	if format != string(artifact.FormatMP4) {
		return nil, errors.New("animation is only available for mp4 export")
	}
	if len(in.Specs) > maxAnimationSpecsPerExport {
		return nil, fmt.Errorf("too many animation specs: %d (max %d)", len(in.Specs), maxAnimationSpecsPerExport)
	}
	opts := &app.AnimationOptions{Specs: make([]app.AnimationSpecRef, 0, len(in.Specs))}
	seen := make(map[string]bool, len(in.Specs))
	for i, spec := range in.Specs {
		slideID := strings.TrimSpace(spec.SlideID)
		if slideID == "" {
			return nil, fmt.Errorf("animation.specs[%d].slideId is required", i)
		}
		if seen[slideID] {
			// 重复项在导出侧只会取第一条，用户却以为自己配了两份：
			// 入口直接拒绝，而不是让"哪条生效"取决于数组顺序。
			return nil, fmt.Errorf("animation.specs contains duplicate slideId %q", slideID)
		}
		seen[slideID] = true
		if spec.Revision <= 0 {
			// revision 是乐观并发版本，从 1 开始。0 表示"这一页没有规格"——
			// 那就别把它放进 specs：放进去会让导出侧多读一次库，
			// 而且这份快照会显得"依据了某一版"，实际没有。
			return nil, fmt.Errorf("animation.specs[%d].revision must be positive", i)
		}
		if spec.SourceRevisionNo < 0 {
			return nil, fmt.Errorf("animation.specs[%d].sourceRevisionNo must not be negative", i)
		}
		opts.Specs = append(opts.Specs, app.AnimationSpecRef{
			SlideID:          slideID,
			Language:         strings.TrimSpace(spec.Language),
			SourceRevisionNo: spec.SourceRevisionNo,
			Revision:         spec.Revision,
		})
	}
	return opts, nil
}

// maxAnimationSpecsPerExport 限制 specs 条数：正常一份讲稿远不到这个量级，
// 上限只用于挡住"用超大数组把导出入口当解析器用"的请求。
const maxAnimationSpecsPerExport = 2000
