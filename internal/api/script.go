package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

const defaultLanguage = "zh-CN"

// sourceRevisionHeader 是「当前查看的源版本」显式头。浏览器可自由设置它（Accept-Language
// 在部分实现中受限），用于让讲稿/配音读写在正确的源版本上进行。缺失/非法 = 0（legacy）。
const sourceRevisionHeader = "X-PPTS-Source-Revision"

// requestSourceRevision 解析源版本头；缺失或非法返回 0（legacy：读路径会回退到存量稿）。
func requestSourceRevision(header httpHeader) int {
	raw := strings.TrimSpace(header.Get(sourceRevisionHeader))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ScriptService exposes the narration revision domain over Connect.
type ScriptService struct {
	pptsv1connect.UnimplementedScriptServiceHandler
	store    narration.Store
	jobs     JobCreator
	members  membership.Reader
	srcStore app.ScriptSourceStore
}

// NewScriptService creates a ScriptService. srcStore 承载"无备注页讲稿来源"选择（M3 ⑥），可为 nil（不注入来源）。
func NewScriptService(store narration.Store, jobs JobCreator, members membership.Reader, srcStore app.ScriptSourceStore) *ScriptService {
	return &ScriptService{store: store, jobs: jobs, members: members, srcStore: srcStore}
}

// registerScriptRoutes 提供不经 proto 的项目级讲稿列表。编辑器进入页面时只需要“已有讲稿”，
// 若逐页调用 ScriptService.Get，未生成页会产生大量 404。列表接口直接返回已存在 revisions。
func registerScriptRoutes(mux *http.ServeMux, scripts narration.Store, members membership.Reader, projects project.ProjectStore, recorder audit.Recorder, gateways gateway.StoreResolver, auth func(http.Handler) http.Handler) {
	providers := gateway.NewProviderCache()
	mux.Handle("GET /projects/{pid}/scripts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := requirePrincipal(r.Context())
		if err != nil {
			writeConnectError(w, err)
			return
		}
		if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
			return
		}
		revs, err := scripts.ListByProject(r.Context(), principal.TenantID, r.PathValue("pid"), requestSourceRevision(r.Header), requestLanguage(r.Header))
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
			return
		}
		out := make([]map[string]any, 0, len(revs))
		for _, rev := range revs {
			out = append(out, scriptRevisionJSON(rev))
		}
		writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
	})))
	mux.Handle("POST /projects/{pid}/scripts/rewrite", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := requirePrincipal(r.Context())
		if err != nil {
			writeConnectError(w, err)
			return
		}
		if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
			return
		}
		if gateways == nil {
			writeConnectError(w, connect.NewError(connect.CodeFailedPrecondition, errors.New("LLM gateway is not configured")))
			return
		}
		var body struct {
			SlideID string `json:"slideId"`
			Text    string `json:"text"`
			Action  string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
			return
		}
		body.Text = strings.TrimSpace(body.Text)
		if body.Text == "" {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("text is required")))
			return
		}
		instructions := rewriteInstructions(body.Action)
		if instructions == "" {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported rewrite action")))
			return
		}
		cfg, err := gateways.Resolve(r.Context(), principal.TenantID, gateway.KindLLM)
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeFailedPrecondition, err))
			return
		}
		provider, err := providers.LLM(cfg)
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		result, err := provider.Rewrite(ctx, llm.RewriteRequest{
			LogicalOpID:  principal.TenantID + ":" + r.PathValue("pid") + ":" + body.SlideID + ":rewrite:" + body.Action,
			Mode:         "polish",
			Language:     requestLanguage(r.Header),
			SourceText:   body.Text,
			Instructions: instructions,
		})
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeUnavailable, err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"text": strings.TrimSpace(result.Text)})
	})))
}

func rewriteInstructions(action string) string {
	suffix := "必须逐字保留所有数字、单位、日期、型号、专有名词和事实，不新增原文没有的信息。只输出改写后的正文。"
	switch action {
	case "shorten":
		return "请将原文压缩为更短、更适合口播的讲稿，保留关键结论和必要数据，减少重复解释。" + suffix
	case "polish":
		return "请润色原文，使其更自然、流畅、适合 PPT 演示口播。" + suffix
	case "transition":
		return "请为原文补充自然的前后衔接表达，使其更适合从上一页过渡到本页讲解。" + suffix
	case "ai_generated":
		return "基于原文生成一段更完整、自然、适合客户演示的讲解稿；可以补足衔接和解释，但不得引入原文没有支持的事实。" + suffix
	default:
		return ""
	}
}

func (s *ScriptService) Get(ctx context.Context, req *connect.Request[pptsv1.GetScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	rev, err := s.store.Get(ctx, p.TenantID, req.Msg.GetProjectId(), requestSourceRevision(req.Header()), req.Msg.GetSlideId(), requestLanguage(req.Header()))
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func (s *ScriptService) Update(ctx context.Context, req *connect.Request[pptsv1.UpdateScriptRequest]) (*connect.Response[pptsv1.UpdateScriptResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	segments, err := fromProtoSegments(req.Msg.GetSegments())
	if err != nil {
		return nil, err
	}
	rev, err := s.store.Update(ctx, p.TenantID, req.Msg.GetProjectId(), requestSourceRevision(req.Header()), req.Msg.GetSlideId(), requestLanguage(req.Header()), req.Msg.GetExpectedRevision(), segments)
	var conflict *narration.ErrConflict
	if errors.As(err, &conflict) {
		return connect.NewResponse(&pptsv1.UpdateScriptResponse{Conflict: true, Latest: toProtoRevision(conflict.Latest)}), nil
	}
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(&pptsv1.UpdateScriptResponse{Revision: toProtoRevision(rev)}), nil
}

func (s *ScriptService) Approve(ctx context.Context, req *connect.Request[pptsv1.ApproveScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleReviewer); err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	rev, err := s.store.SetStatus(ctx, p.TenantID, req.Msg.GetProjectId(), requestSourceRevision(req.Header()), req.Msg.GetSlideId(), requestLanguage(req.Header()), narration.StatusApproved)
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func (s *ScriptService) Lock(ctx context.Context, req *connect.Request[pptsv1.LockScriptRequest]) (*connect.Response[pptsv1.ScriptRevision], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleReviewer); err != nil {
		return nil, err
	}
	if err := requireProjectSlide(req.Msg.GetProjectId(), req.Msg.GetSlideId()); err != nil {
		return nil, err
	}
	status := narration.StatusLocked
	if !req.Msg.GetLock() {
		status = narration.StatusApproved
	}
	rev, err := s.store.SetStatus(ctx, p.TenantID, req.Msg.GetProjectId(), requestSourceRevision(req.Header()), req.Msg.GetSlideId(), requestLanguage(req.Header()), status)
	if err != nil {
		return nil, scriptError(err)
	}
	return connect.NewResponse(toProtoRevision(rev)), nil
}

func (s *ScriptService) GenerateDraft(ctx context.Context, req *connect.Request[pptsv1.GenerateDraftRequest]) (*connect.Response[pptsv1.GenerateDraftResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	idempotencyKey := strings.TrimSpace(req.Header().Get("Idempotency-Key"))
	if idempotencyKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("Idempotency-Key header is required"))
	}
	mode := toDomainMode(req.Msg.GetMode())
	snapshot := app.ScriptDraftSnapshot{
		ProjectID: projectID, Language: requestLanguage(req.Header()), Mode: string(mode),
		RevisionNo: requestSourceRevision(req.Header()),
	}
	// M3 ⑥：注入已存的"无备注页讲稿来源"选择，使 worker 在 pgInput/pgAnchors 中尊重用户显式来源。
	if s.srcStore != nil {
		if choices, lerr := s.srcStore.List(ctx, p.TenantID, projectID, requestSourceRevision(req.Header())); lerr == nil && len(choices) > 0 {
			sources := make(map[string]string, len(choices))
			customs := make(map[string]string, len(choices))
			for slideID, choice := range choices {
				sources[slideID] = string(choice.Kind)
				if choice.Kind == app.ScriptSourceCustom {
					customs[slideID] = choice.CustomText
				}
			}
			snapshot.Sources = sources
			snapshot.CustomSources = customs
		}
	}
	for _, rawSlideID := range req.Msg.GetSlideIds() {
		slideID := strings.TrimSpace(rawSlideID)
		if slideID == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("slide_id cannot be empty"))
		}
		snapshot.SlideIDs = append(snapshot.SlideIDs, slideID)
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	job, err := s.jobs.Create(ctx, p.TenantID, projectID, string(pipeline.KindScriptDraft), idempotencyKey, string(snapshotBytes), time.Time{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if job.InputSnapshot != string(snapshotBytes) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Idempotency-Key was already used for a different request"))
	}
	return connect.NewResponse(&pptsv1.GenerateDraftResponse{JobId: job.ID, FullySupported: true}), nil
}

func toDomainMode(mode pptsv1.ScriptMode) narration.ScriptMode {
	switch mode {
	case pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL:
		return narration.ModeOriginal
	case pptsv1.ScriptMode_SCRIPT_MODE_POLISH:
		return narration.ModePolish
	case pptsv1.ScriptMode_SCRIPT_MODE_AI_GENERATED:
		return narration.ModeAIGenerated
	default:
		return narration.ModeOriginal
	}
}

func requirePrincipal(ctx context.Context) (Principal, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication context is missing"))
	}
	return p, nil
}

func requireProjectSlide(projectID, slideID string) error {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(slideID) == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and slide_id are required"))
	}
	return nil
}

func requestLanguage(header httpHeader) string {
	// 显式语言头优先：浏览器可自由设置它（Accept-Language 在部分实现中受限），
	// 编辑器用它保证"生成的讲稿语言"与"面板展示的语言"一致。
	language := strings.TrimSpace(header.Get("X-PPTS-Language"))
	if language == "" {
		language = strings.TrimSpace(strings.Split(header.Get("Accept-Language"), ",")[0])
	}
	if language == "" {
		return defaultLanguage
	}
	if i := strings.IndexByte(language, ';'); i >= 0 {
		language = strings.TrimSpace(language[:i])
	}
	return language
}

type httpHeader interface {
	Get(string) string
}

func fromProtoSegments(in []*pptsv1.Segment) ([]*narration.Segment, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]*narration.Segment, 0, len(in))
	for _, segment := range in {
		if segment == nil || strings.TrimSpace(segment.GetSegmentId()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("every segment requires segment_id"))
		}
		if _, exists := seen[segment.GetSegmentId()]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate segment_id"))
		}
		seen[segment.GetSegmentId()] = struct{}{}
		out = append(out, &narration.Segment{
			SegmentID:   segment.GetSegmentId(),
			DisplayText: segment.GetDisplayText(),
			SpokenText:  segment.GetSpokenText(),
			SourceRefs:  append([]string(nil), segment.GetSourceRefs()...),
			// SourceAnchors are server provenance. User updates preserve existing anchors
			// in the store instead of accepting client-supplied values.
			Status: narration.ScriptStatus(segment.GetStatus()),
		})
	}
	return out, nil
}

func toProtoRevision(rev *narration.Revision) *pptsv1.ScriptRevision {
	if rev == nil {
		return nil
	}
	segments := make([]*pptsv1.Segment, 0, len(rev.Segments))
	for _, segment := range rev.Segments {
		segments = append(segments, &pptsv1.Segment{
			SegmentId:     segment.SegmentID,
			SlideId:       rev.SlideID,
			DisplayText:   segment.DisplayText,
			SpokenText:    segment.SpokenText,
			SourceRefs:    append([]string(nil), segment.SourceRefs...),
			SourceAnchors: toProtoSourceAnchors(segment.SourceAnchors),
			Status:        string(segment.Status),
		})
	}
	return &pptsv1.ScriptRevision{
		ProjectId:     rev.ProjectID,
		SlideId:       rev.SlideID,
		Language:      rev.Language,
		Mode:          toProtoMode(rev.Mode),
		Revision:      rev.Revision,
		Status:        string(rev.Status),
		Segments:      segments,
		UpdatedAtUnix: rev.UpdatedAt.Unix(),
	}
}

func scriptRevisionJSON(rev *narration.Revision) map[string]any {
	segments := make([]map[string]any, 0, len(rev.Segments))
	for _, segment := range rev.Segments {
		anchors := make([]map[string]any, 0, len(segment.SourceAnchors))
		for _, anchor := range segment.SourceAnchors {
			anchors = append(anchors, map[string]any{
				"slideId": anchor.SlideID, "shapeId": anchor.ShapeID, "kind": anchor.Kind,
				"raw": anchor.Raw, "confidence": anchor.Confidence,
			})
		}
		segments = append(segments, map[string]any{
			"segmentId":     segment.SegmentID,
			"slideId":       rev.SlideID,
			"displayText":   segment.DisplayText,
			"spokenText":    segment.SpokenText,
			"sourceRefs":    append([]string(nil), segment.SourceRefs...),
			"sourceAnchors": anchors,
			"status":        string(segment.Status),
		})
	}
	return map[string]any{
		"projectId": rev.ProjectID,
		"slideId":   rev.SlideID,
		"language":  rev.Language,
		"mode":      toProtoMode(rev.Mode).String(),
		"revision":  rev.Revision,
		"status":    string(rev.Status),
		"segments":  segments,
	}
}

func toProtoSourceAnchors(in []narration.SourceAnchor) []*pptsv1.SourceAnchor {
	out := make([]*pptsv1.SourceAnchor, 0, len(in))
	for _, anchor := range in {
		out = append(out, &pptsv1.SourceAnchor{
			SlideId: anchor.SlideID, ShapeId: anchor.ShapeID, Kind: anchor.Kind,
			Raw: anchor.Raw, Confidence: anchor.Confidence,
		})
	}
	return out
}

func toProtoMode(mode narration.ScriptMode) pptsv1.ScriptMode {
	switch mode {
	case narration.ModeOriginal:
		return pptsv1.ScriptMode_SCRIPT_MODE_ORIGINAL
	case narration.ModePolish:
		return pptsv1.ScriptMode_SCRIPT_MODE_POLISH
	case narration.ModeAIGenerated:
		return pptsv1.ScriptMode_SCRIPT_MODE_AI_GENERATED
	default:
		return pptsv1.ScriptMode_SCRIPT_MODE_UNSPECIFIED
	}
}

func scriptError(err error) error {
	switch {
	case errors.Is(err, narration.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, narration.ErrLocked):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
