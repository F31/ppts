package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/usage"
)

// JobCreator 是 API transport 需要的任务能力（创建任务 + 播放服务发现最近成功配音）。
type JobCreator interface {
	Create(context.Context, string, string, string, string, string, time.Time) (*pipeline.Job, error)
	LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*pipeline.Job, error)
	StepResultRef(ctx context.Context, jobID, stepType string) (string, error)
}

// QuotaManager 是配音生成前的额度预占能力（G3-2）。
type QuotaManager interface {
	Reserve(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind, units float64) (*usage.Reservation, error)
	Release(ctx context.Context, tenantID, logicalOperationID string, kind usage.Kind) error
}

// NarrationGenerationService creates revision-bound narration jobs.
type NarrationGenerationService struct {
	pptsv1connect.UnimplementedNarrationServiceHandler
	scripts    narration.Store
	jobs       JobCreator
	projects   project.ProjectStore
	objects    objectstore.ObjectStore
	quota      QuotaManager
	policy     TenantPolicyReader
	members    membership.Reader
	audit      audit.Recorder
	voiceStore app.VoiceSettingsStore
}

// NewNarrationGenerationService creates a narration task service.
func NewNarrationGenerationService(scripts narration.Store, jobs JobCreator, projects project.ProjectStore, objects objectstore.ObjectStore, quota QuotaManager, policy TenantPolicyReader, members membership.Reader, recorder audit.Recorder) *NarrationGenerationService {
	return &NarrationGenerationService{scripts: scripts, jobs: jobs, projects: projects, objects: objects, quota: quota, policy: policy, members: members, audit: recorder}
}

// WithVoiceSettings 注入项目级语音属性读取能力。
//
// 注入后，请求未显式点名模型时按项目保存的设置解析——用户在界面上选了模型却没点生成、
// 或第三方客户端只传 voice_id，都不会悄悄用回默认模型。未注入（测试/纯 API 部署）时
// 保持旧的"租户默认"语义。
func (s *NarrationGenerationService) WithVoiceSettings(store app.VoiceSettingsStore) *NarrationGenerationService {
	s.voiceStore = store
	return s
}

// ttsModelHeader 是「本次配音用哪个语音模型」的显式头。
//
// 为什么不加 proto 字段：本环境没有 buf/protoc，改 .proto 无法重新生成 pb.go（仓库里
// 已有大量同类取舍，见 /projects/{pid}/voice-preview、artifact、collab 等原生端点）。
// 走头的另一个好处是与 Idempotency-Key、X-PPTS-Bypass-Cache 同层，前端不必等协议升级。
const ttsModelHeader = "X-PPTS-TTS-Model"

// resolveNarrationModel 决定本次任务使用的模型名。
//
// 优先级：请求头 → 项目语音设置 → 空（租户默认）。
// 读设置失败时直接报错而不回落默认：用户已经保存过某个模型，这时用默认模型合成，
// 结果与"保存成功"的界面承诺相反，且从音频里看不出来。
func (s *NarrationGenerationService) resolveNarrationModel(ctx context.Context, header httpHeader, tenantID, projectID string) (string, error) {
	if model := strings.TrimSpace(header.Get(ttsModelHeader)); model != "" {
		return model, nil
	}
	if s.voiceStore == nil {
		return "", nil
	}
	settings, err := s.voiceStore.Get(ctx, tenantID, projectID)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, fmt.Errorf("read project voice settings: %w", err))
	}
	return strings.TrimSpace(settings.Model), nil
}

// validateSlidesInRevision 校验待配音的 slide 都属于指定源版本（revisionNo<=0 时回退当前版本）。
// 引入按版本隔离后，编辑旧版本只能对旧版本的页配音；旧版页不属于当前版本是正常现象，
// 不能再用"必须属于当前版本"来拒绝。此处按请求的源版本校验，防止版本与页表错配。
func (s *NarrationGenerationService) validateSlidesInRevision(ctx context.Context, tenantID, userID, projectID string, revisionNo int, slideIDs []string) error {
	pages, err := s.revisionPageIDs(ctx, tenantID, userID, projectID, revisionNo)
	if err != nil {
		return err
	}
	if pages == nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(pages))
	for _, id := range pages {
		allowed[id] = struct{}{}
	}
	for _, slideID := range slideIDs {
		if _, ok := allowed[slideID]; !ok {
			return connect.NewError(connect.CodeFailedPrecondition,
				errors.New("slide "+slideID+" does not belong to the selected PPT revision; refresh the editor and retry"))
		}
	}
	return nil
}

// revisionPageIDs 返回该源版本的页序（用于校验，也用于把部分页配音补成整份 deck）。
// 返回 nil 表示读不到页表（projects/objects 未注入或没有页）——调用方按"不校验"处理。
func (s *NarrationGenerationService) revisionPageIDs(ctx context.Context, tenantID, userID, projectID string, revisionNo int) ([]string, error) {
	if s.projects == nil || s.objects == nil {
		return nil, nil
	}
	proj, err := s.projects.GetProject(ctx, tenantID, userID, projectID)
	if err != nil || proj == nil || proj.CurrentRevision == 0 {
		return nil, nil
	}
	if revisionNo <= 0 {
		revisionNo = int(proj.CurrentRevision)
	}
	docs, err := loadProjectDocuments(ctx, s.projects, s.objects, tenantID, projectID, revisionNo, revisionNo)
	if err != nil {
		return nil, err
	}
	doc := docs[revisionNo]
	if doc == nil || len(doc.Pages) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(doc.Pages))
	for _, pg := range doc.Pages {
		if pg != nil && pg.SlideID != "" {
			ids = append(ids, pg.SlideID)
		}
	}
	return ids, nil
}

// expandToFullDeck 把"只重配几页"补成整份 deck 的快照。
//
// 为什么必须补：播放与导出取的是**最新一次成功配音任务**的时间轴。只覆盖被点名页的
// 任务会把 22 页的时间轴发布成 1 页（2026-11-07 实测），整份成片被悄悄截断，
// 而界面上只显示"这一页重新生成了"。
//
// 代价控制：补进来的页带上 RegenerateSlideIDs 之外的身份，worker 只对点名页跳过缓存，
// 其余页命中缓存，不产生额外 TTS 调用；配额估算仍只按点名页字数计。
func (s *NarrationGenerationService) expandToFullDeck(
	ctx context.Context, tenantID, userID, projectID string, revisionNo int, language string,
	snapshot *app.NarrationSnapshot, requested []string,
) {
	if snapshot == nil || s.scripts == nil || len(snapshot.SegmentIDs) > 0 {
		return
	}
	pages, err := s.revisionPageIDs(ctx, tenantID, userID, projectID, revisionNo)
	if err != nil || len(pages) <= len(requested) {
		return
	}
	bySlide := make(map[string]app.NarrationSlideSnapshot, len(pages))
	for _, sl := range snapshot.Slides {
		bySlide[sl.SlideID] = sl
	}
	// 一次读全该版本讲稿，而不是逐页 Get：补齐走的是整份 deck（22 页 = 22 个事务），
	// 而这里只用得上「脚本修订号 + 审批状态」两个字段。
	scripts, lerr := s.scripts.ListByProject(ctx, tenantID, projectID, revisionNo, language)
	if lerr != nil {
		return
	}
	scriptBySlide := make(map[string]*narration.Revision, len(scripts))
	for _, rev := range scripts {
		scriptBySlide[rev.SlideID] = rev
	}
	full := make([]app.NarrationSlideSnapshot, 0, len(pages))
	added := 0
	for _, pid := range pages {
		if sl, ok := bySlide[pid]; ok {
			full = append(full, sl)
			continue
		}
		rev := scriptBySlide[pid]
		if rev == nil {
			// 没有讲稿的页不参与配音：整份 deck 里允许存在未写讲稿的页。
			continue
		}
		if snapshot.RequireConfirmed && rev.Status != narration.StatusApproved && rev.Status != narration.StatusLocked {
			continue
		}
		full = append(full, app.NarrationSlideSnapshot{SlideID: pid, ScriptRevision: rev.Revision})
		added++
	}
	if added == 0 {
		return
	}
	snapshot.Slides = full
	snapshot.RegenerateSlideIDs = append([]string(nil), requested...)
}

type activeJobCounter interface {
	CountActive(ctx context.Context, tenantID string) (int, error)
}

type jobByIdempotencyFinder interface {
	ByIdempotency(ctx context.Context, tenantID, kind, idemKey string) (*pipeline.Job, error)
}

func (s *NarrationGenerationService) CreateGeneration(ctx context.Context, req *connect.Request[pptsv1.CreateGenerationRequest]) (*connect.Response[pptsv1.CreateGenerationResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	voiceID := strings.TrimSpace(req.Msg.GetVoiceId())
	if projectID == "" || voiceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and voice_id are required"))
	}
	if len(req.Msg.GetSlideIds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_ids are required until project page-order lookup is available"))
	}
	rate := int(req.Msg.GetRatePercent())
	if rate == 0 {
		rate = 100
	}
	if rate < 50 || rate > 200 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rate_percent must be between 50 and 200"))
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header is required"))
	}

	language := requestLanguage(req.Header())
	// 源版本：优先显式头（编辑器当前查看的版本）；缺失时回退当前版本（向后兼容）。
	sourceRevision := requestSourceRevision(req.Header())
	if sourceRevision <= 0 && s.projects != nil {
		if proj, perr := s.projects.GetProject(ctx, principal.TenantID, principal.UserID, projectID); perr == nil && proj != nil {
			sourceRevision = int(proj.CurrentRevision)
		}
	}
	snapshot := app.NarrationSnapshot{
		Language: language, VoiceID: voiceID, RequireConfirmed: req.Msg.GetLockConfirmedOnly(),
		SpeechControl: tts.SpeechControl{RatePercent: rate}, SampleRate: 16000,
		RevisionNo: sourceRevision,
		// 用户显式要求"重新生成"时跳过缓存、真实调用 TTS（否则内容哈希命中会让界面承诺落空）。
		BypassCache: requestBypassCache(req.Header()),
	}
	model, merr := s.resolveNarrationModel(ctx, req.Header(), principal.TenantID, projectID)
	if merr != nil {
		return nil, merr
	}
	snapshot.Model = model
	seen := make(map[string]struct{}, len(req.Msg.GetSlideIds()))
	slideOrder := make([]string, 0, len(req.Msg.GetSlideIds()))
	totalRunes := 0
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		if _, exists := seen[slideID]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate slide_id"))
		}
		seen[slideID] = struct{}{}
		slideOrder = append(slideOrder, slideID)
		revision, err := s.scripts.Get(ctx, principal.TenantID, projectID, sourceRevision, slideID, language)
		if err != nil {
			return nil, scriptError(err)
		}
		if snapshot.RequireConfirmed && revision.Status != narration.StatusApproved && revision.Status != narration.StatusLocked {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("all selected scripts must be approved or locked"))
		}
		for _, segment := range revision.Segments {
			if segment != nil {
				totalRunes += utf8.RuneCountInString(segment.SpokenText)
			}
		}
		snapshot.Slides = append(snapshot.Slides, app.NarrationSlideSnapshot{
			SlideID: slideID, ScriptRevision: revision.Revision,
		})
	}
	if err := s.validateSlidesInRevision(ctx, principal.TenantID, principal.UserID, projectID, sourceRevision, slideOrder); err != nil {
		return nil, err
	}
	s.expandToFullDeck(ctx, principal.TenantID, principal.UserID, projectID, sourceRevision, language, &snapshot, slideOrder)
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if existing, err := s.enforceConcurrentLimit(ctx, principal.TenantID, idempotencyKey, string(snapshotBytes)); err != nil {
		return nil, err
	} else if existing != nil {
		return connect.NewResponse(&pptsv1.CreateGenerationResponse{JobId: existing.ID, WithinBudget: true}), nil
	}

	// 配额预占：预占成功才创建可执行任务（V4.0 §12.2）。
	var reserved *usage.Reservation
	if s.quota != nil {
		units := usage.EstimateSeconds(totalRunes)
		res, rerr := s.quota.Reserve(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds, units)
		if errors.Is(rerr, usage.ErrInsufficientQuota) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("quota exceeded for requested narration"))
		}
		if rerr != nil {
			return nil, connect.NewError(connect.CodeInternal, rerr)
		}
		reserved = res
	}
	releaseReservation := func() {
		if reserved != nil && reserved.Created {
			_ = s.quota.Release(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds)
		}
	}
	job, err := s.jobs.Create(ctx, principal.TenantID, projectID, string(pipeline.KindNarration), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		releaseReservation()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		releaseReservation()
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, audit.Event{TenantID: principal.TenantID, ActorUser: principal.UserID, Action: "narration.create", ResourceType: "job", ResourceID: job.ID, Metadata: map[string]any{"project_id": projectID, "voice_id": voiceID, "slide_count": len(snapshot.Slides), "revision_no": sourceRevision, "rate_percent": rate}})
	}
	return connect.NewResponse(&pptsv1.CreateGenerationResponse{JobId: job.ID, WithinBudget: true}), nil
}

// RegenerateSegments 以分段级重生成（G2-5）：仅对请求中的 segment_ids 重新合成，
// 其余分段复用既有缓存；复用 CreateGeneration 的任务入队、并发限流与配额预占流程。
func (s *NarrationGenerationService) RegenerateSegments(ctx context.Context, req *connect.Request[pptsv1.RegenerateSegmentsRequest]) (*connect.Response[pptsv1.RegenerateSegmentsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	slideID := strings.TrimSpace(req.Msg.GetSlideId())
	voiceID := strings.TrimSpace(req.Msg.GetVoiceId())
	if projectID == "" || slideID == "" || voiceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id, slide_id and voice_id are required"))
	}
	language := requestLanguage(req.Header())
	sourceRevision := requestSourceRevision(req.Header())
	if sourceRevision <= 0 && s.projects != nil {
		if proj, perr := s.projects.GetProject(ctx, principal.TenantID, principal.UserID, projectID); perr == nil && proj != nil {
			sourceRevision = int(proj.CurrentRevision)
		}
	}
	revision, err := s.scripts.Get(ctx, principal.TenantID, projectID, sourceRevision, slideID, language)
	if err != nil {
		return nil, scriptError(err)
	}
	if len(revision.Segments) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("slide has no segments to regenerate"))
	}
	if err := s.validateSlidesInRevision(ctx, principal.TenantID, principal.UserID, projectID, sourceRevision, []string{slideID}); err != nil {
		return nil, err
	}
	snapshot := app.NarrationSnapshot{
		Language:      language,
		VoiceID:       voiceID,
		SegmentIDs:    req.Msg.GetSegmentIds(),
		SpeechControl: tts.SpeechControl{RatePercent: 100},
		SampleRate:    16000,
		RevisionNo:    sourceRevision,
		// 分段级重生成同样是显式"重新生成"意图，沿用同一标志。
		BypassCache: requestBypassCache(req.Header()),
	}
	// 局部重生成也必须认同一个模型，否则"只重做这一段"会和整篇用不同的声音。
	if model, merr := s.resolveNarrationModel(ctx, req.Header(), principal.TenantID, projectID); merr != nil {
		return nil, merr
	} else {
		snapshot.Model = model
	}
	filter := make(map[string]struct{}, len(req.Msg.GetSegmentIds()))
	for _, id := range req.Msg.GetSegmentIds() {
		if id != "" {
			filter[id] = struct{}{}
		}
	}
	totalRunes := 0
	for _, seg := range revision.Segments {
		if seg == nil {
			continue
		}
		if len(filter) > 0 {
			if _, ok := filter[seg.SegmentID]; !ok {
				continue
			}
		}
		totalRunes += utf8.RuneCountInString(seg.SpokenText)
	}
	snapshot.Slides = append(snapshot.Slides, app.NarrationSlideSnapshot{SlideID: slideID, ScriptRevision: revision.Revision})
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		// 未带幂等键时以请求内容派生，避免重复提交产生重复任务。
		idempotencyKey = "regen:" + projectID + ":" + slideID + ":" + language + ":" + strings.Join(req.Msg.GetSegmentIds(), ",") + ":" + voiceID
	}
	if existing, err := s.enforceConcurrentLimit(ctx, principal.TenantID, idempotencyKey, string(snapshotBytes)); err != nil {
		return nil, err
	} else if existing != nil {
		return connect.NewResponse(&pptsv1.RegenerateSegmentsResponse{JobId: existing.ID}), nil
	}
	var reserved *usage.Reservation
	if s.quota != nil {
		units := usage.EstimateSeconds(totalRunes)
		res, rerr := s.quota.Reserve(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds, units)
		if errors.Is(rerr, usage.ErrInsufficientQuota) {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("quota exceeded for requested narration"))
		}
		if rerr != nil {
			return nil, connect.NewError(connect.CodeInternal, rerr)
		}
		reserved = res
	}
	releaseReservation := func() {
		if reserved != nil && reserved.Created {
			_ = s.quota.Release(ctx, principal.TenantID, idempotencyKey, usage.KindGenSeconds)
		}
	}
	job, err := s.jobs.Create(ctx, principal.TenantID, projectID, string(pipeline.KindNarration), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		releaseReservation()
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		releaseReservation()
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.RegenerateSegmentsResponse{JobId: job.ID}), nil
}

func (s *NarrationGenerationService) enforceConcurrentLimit(ctx context.Context, tenantID, idempotencyKey, snapshot string) (*pipeline.Job, error) {
	if s.policy == nil {
		return nil, nil
	}
	policy, err := s.policy.GetPolicy(ctx, tenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	if policy.MaxConcurrentJobs <= 0 {
		return nil, nil
	}
	counter, ok := s.jobs.(activeJobCounter)
	if !ok {
		return nil, nil
	}
	active, err := counter.CountActive(ctx, tenantID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if active < policy.MaxConcurrentJobs {
		return nil, nil
	}
	if finder, ok := s.jobs.(jobByIdempotencyFinder); ok {
		job, err := finder.ByIdempotency(ctx, tenantID, string(pipeline.KindNarration), idempotencyKey)
		if err == nil {
			if job.InputSnapshot != snapshot {
				return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
			}
			return job, nil
		}
		if !errors.Is(err, pipeline.ErrJobNotFound) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("tenant concurrent job limit reached"))
}

// Estimate 估算所选页面的播报秒数。定价随正式 TTS 供应商确定（当前返回 0 费用）。
func (s *NarrationGenerationService) Estimate(ctx context.Context, req *connect.Request[pptsv1.NarrationEstimateRequest]) (*connect.Response[pptsv1.NarrationEstimateResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" || len(req.Msg.GetSlideIds()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and slide_ids are required"))
	}
	language := requestLanguage(req.Header())
	sourceRevision := requestSourceRevision(req.Header())
	totalRunes := 0
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		revision, err := s.scripts.Get(ctx, principal.TenantID, projectID, sourceRevision, slideID, language)
		if err != nil {
			return nil, scriptError(err)
		}
		for _, segment := range revision.Segments {
			if segment != nil {
				totalRunes += utf8.RuneCountInString(segment.SpokenText)
			}
		}
	}
	return connect.NewResponse(&pptsv1.NarrationEstimateResponse{
		EstimatedSeconds: int64(math.Ceil(usage.EstimateSeconds(totalRunes))),
	}), nil
}

// registerNarrationRoutes 挂载核心创作辅助的原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC）：
// 待确认稿计数与 stale 信号，供生成面板前置检查（C-5 强制阻止 + 过期提示）。
func registerNarrationRoutes(mux *http.ServeMux, scripts narration.Store, members membership.Reader, projects project.ProjectStore, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/narration/draft-count", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicNarrationDraftCount(w, r, scripts, members, projects, recorder)
	})))
	mux.Handle("GET /projects/{pid}/narration/stale", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicNarrationStale(w, r, scripts, members, projects, recorder)
	})))
}

func publicNarrationDraftCount(w http.ResponseWriter, r *http.Request, scripts narration.Store, members membership.Reader, projects project.ProjectStore, recorder audit.Recorder) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	pid := r.PathValue("pid")
	n, err := scripts.CountDraftSegments(r.Context(), principal.TenantID, pid)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draftSegments": n})
}

func publicNarrationStale(w http.ResponseWriter, r *http.Request, scripts narration.Store, members membership.Reader, projects project.ProjectStore, recorder audit.Recorder) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	pid := r.PathValue("pid")
	language := requestLanguage(r.Header)
	revs, err := scripts.ListByProject(r.Context(), principal.TenantID, pid, requestSourceRevision(r.Header), language)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	slides := make([]map[string]any, 0, len(revs))
	for _, rev := range revs {
		slides = append(slides, map[string]any{
			"slideId": rev.SlideID,
			"stale":   rev.AudioRevision < rev.Revision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": slides})
}

// writeConnectError 将 Connect 错误映射为原生 HTTP 状态码与 JSON 错误体。
func writeConnectError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated:
		status = http.StatusUnauthorized
	case connect.CodePermissionDenied, connect.CodeFailedPrecondition:
		status = http.StatusForbidden
	case connect.CodeInvalidArgument:
		status = http.StatusBadRequest
	case connect.CodeNotFound:
		status = http.StatusNotFound
	case connect.CodeResourceExhausted:
		status = http.StatusTooManyRequests
	case connect.CodeAlreadyExists:
		status = http.StatusConflict
	case connect.CodeUnimplemented:
		// 能力未配置/后端不支持（如 store 未实现可选读取能力）：语义上是 501，
		// 与 Internal 区分开，前端才能给出"该能力不可用"而非"服务异常"的提示。
		status = http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
