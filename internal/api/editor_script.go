package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// editorRegenerateScriptDraft 入队 script_draft 任务（单页「重新生成讲稿」与「一键成稿」共用）。
// body: {slideIds: string[], mode, language?, sourceMode?, audience?, style?, targetSeconds?, overwrite?}。
//
// 语义（M4 收敛，参数校验见 parseScriptDraftParams）：
//   - overwrite 缺省 true；false = 只填空白页（已有讲稿的页会被跳过并计入 job_steps）。
//   - sourceMode ∈ {"", notes_first, notes_only, page_content}；**空串时**才采用页面级
//     「无备注页讲稿来源」选择（srcStore），显式给了 sourceMode 时页面级选择被覆盖。
//   - mode 缺省 original；original 不调 LLM（直接取素材原文），其余才走模型。
//   - 非法 mode / sourceMode / 空 slideIds / 负 targetSeconds 一律 400，不静默回退。
func editorRegenerateScriptDraft(w http.ResponseWriter, r *http.Request, jobs JobStore, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "permission_denied", "message": "requires editor role"})
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	var body scriptDraftBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	params, err := parseScriptDraftParams(body, requestLanguage(r.Header))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": err.Error()})
		return
	}
	snap := app.ScriptDraftSnapshot{
		ProjectID: projectID, Language: params.Language, Mode: string(params.Mode), SourceMode: params.SourceMode,
		Audience: params.Audience, Style: params.Style, TargetSeconds: params.TargetSeconds,
		Overwrite: params.Overwrite, SlideIDs: params.SlideIDs,
	}
	// 解析脚本必须基于一个明确的源版本：优先请求指定的"当前查看版本"；缺失则回退项目当前版本。
	// worker 在 RevisionNo=0 时会回退到首个版本 —— 旧版本可能页数不足/没有备注，
	// 导致"有备注的页没有讲稿"，因此这里始终显式绑定一个版本。
	snap.RevisionNo = params.RevisionNo
	if snap.RevisionNo <= 0 {
		snap.RevisionNo = requestSourceRevision(r.Header)
	}
	if snap.RevisionNo <= 0 {
		if userID, ok := projectAccessUser(r.Context(), members); ok {
			if proj, perr := projects.GetProject(r.Context(), principal.TenantID, userID, projectID); perr == nil && proj != nil {
				snap.RevisionNo = int(proj.CurrentRevision)
			}
		}
	}
	if srcStore != nil {
		if choices, lerr := srcStore.List(r.Context(), principal.TenantID, projectID, snap.RevisionNo); lerr == nil && len(choices) > 0 {
			sources := make(map[string]string, len(choices))
			customs := make(map[string]string, len(choices))
			for slideID, choice := range choices {
				sources[slideID] = string(choice.Kind)
				if choice.Kind == app.ScriptSourceCustom {
					customs[slideID] = choice.CustomText
				}
			}
			snap.Sources = sources
			snap.CustomSources = customs
		}
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idem == "" {
		idem = "regen-script:" + projectID + ":" + string(params.Mode) + ":" + strings.Join(snap.SlideIDs, ",") + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	job, err := jobs.Create(r.Context(), principal.TenantID, projectID, string(pipeline.KindScriptDraft), idem, string(snapBytes), time.Time{})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if recorder != nil {
		_ = recorder.Record(r.Context(), audit.Event{TenantID: principal.TenantID, ActorUser: principal.UserID, Action: "script_draft.create", ResourceType: "job", ResourceID: job.ID, Metadata: map[string]any{"project_id": projectID, "mode": string(params.Mode), "source_mode": params.SourceMode, "slide_count": len(params.SlideIDs), "revision_no": snap.RevisionNo}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID})
}

// scriptDraftBody 是 POST /projects/{pid}/script-draft 的请求体。
type scriptDraftBody struct {
	SlideIDs      []string `json:"slideIds"`
	Mode          string   `json:"mode"`
	Language      string   `json:"language"`
	SourceMode    string   `json:"sourceMode"`
	Audience      string   `json:"audience"`
	Style         string   `json:"style"`
	TargetSeconds int      `json:"targetSeconds"`
	Overwrite     *bool    `json:"overwrite"`
	// RevisionNo 是"当前查看的源版本"。>0 时按该版本解析并落稿；缺失/0 回退当前版本（向后兼容）。
	RevisionNo int `json:"revisionNo"`
}

// scriptDraftParams 是校验并归一化后的入参（不含需要查库的 ProjectID/RevisionNo/Sources）。
type scriptDraftParams struct {
	SlideIDs      []string
	Mode          narration.ScriptMode
	Language      string
	SourceMode    string
	Audience      string
	Style         string
	TargetSeconds int
	Overwrite     bool
	RevisionNo    int
}

// scriptSourceModes 是批量成稿来源的白名单。
// 空串 = 未指定：**只有此时页面级 Sources（"无备注页讲稿来源"）才会生效**；
// 一旦显式给了批量来源，页面级选择就被它覆盖 —— 这是既定语义，不是回退。
var scriptSourceModes = map[string]struct{}{
	"":             {},
	"notes_first":  {},
	"notes_only":   {},
	"page_content": {},
}

// parseScriptDraftParams 校验并归一化「重新生成讲稿」的入参；error 非 nil = 应答 400 invalid_argument。
//
// 这里刻意**拒绝**而不是静默回退，因为静默回退会让「用户选的」与「实际用的」不一致且界面上看不出来：
//   - mode 无法识别时曾静默变成 original，即「不调 LLM、直接拿备注原文当稿」，而用户以为走了 AI 生成；
//   - sourceMode 非法值曾静默走 default 分支（退回页面级 Sources），"来源=页面内容"变成别的含义；
//   - slideIds 为空曾建出一个 0 页任务，最后照样报"成稿完成"。
//
// 纯函数（只依赖入参），因此任何机器上都能单测（见 editor_test.go）。
func parseScriptDraftParams(body scriptDraftBody, headerLanguage string) (scriptDraftParams, error) {
	out := scriptDraftParams{
		Language:      strings.TrimSpace(body.Language),
		SourceMode:    strings.TrimSpace(body.SourceMode),
		Audience:      strings.TrimSpace(body.Audience),
		Style:         strings.TrimSpace(body.Style),
		TargetSeconds: body.TargetSeconds,
		Overwrite:     true,
		RevisionNo:    body.RevisionNo,
	}
	if out.Language == "" {
		out.Language = strings.TrimSpace(headerLanguage)
	}
	if body.Overwrite != nil {
		out.Overwrite = *body.Overwrite
	}
	mode, ok := scriptModeFromString(body.Mode)
	if !ok {
		return scriptDraftParams{}, fmt.Errorf("unknown mode %q", strings.TrimSpace(body.Mode))
	}
	out.Mode = mode
	if _, ok := scriptSourceModes[out.SourceMode]; !ok {
		return scriptDraftParams{}, fmt.Errorf("unknown sourceMode %q", out.SourceMode)
	}
	if out.TargetSeconds < 0 {
		return scriptDraftParams{}, errors.New("targetSeconds must not be negative")
	}
	seen := make(map[string]struct{}, len(body.SlideIDs))
	for _, raw := range body.SlideIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out.SlideIDs = append(out.SlideIDs, id)
	}
	if len(out.SlideIDs) == 0 {
		return scriptDraftParams{}, errors.New("slideIds must not be empty")
	}
	return out, nil
}

// scriptModeFromString 兼容 proto 枚举名与领域字符串（original/polish/ai_generated）。
// 空串与 SCRIPT_MODE_UNSPECIFIED 都表示「未指定」，按 original 处理；其余无法识别的取值返回 ok=false。
func scriptModeFromString(raw string) (narration.ScriptMode, bool) {
	switch strings.TrimSpace(raw) {
	case "", "SCRIPT_MODE_UNSPECIFIED", "SCRIPT_MODE_ORIGINAL", string(narration.ModeOriginal):
		return narration.ModeOriginal, true
	case "SCRIPT_MODE_POLISH", string(narration.ModePolish):
		return narration.ModePolish, true
	case "SCRIPT_MODE_AI_GENERATED", string(narration.ModeAIGenerated):
		return narration.ModeAIGenerated, true
	default:
		return narration.ModeOriginal, false
	}
}

type editorSlideSourceItem struct {
	SlideID    string `json:"slideId"`
	Source     string `json:"source"`
	CustomText string `json:"customText"`
}

// editorSetSlideSource 持久化单页讲稿来源选择（M3 ⑥）。body: {source, customText?}。
// source ∈ layout/title/body/notes/custom；custom 时 customText 必填且非空。
func editorSetSlideSource(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		http.Error(w, "project_id and slide_id are required", http.StatusBadRequest)
		return
	}
	var body struct {
		Source     string `json:"source"`
		CustomText string `json:"customText"`
		RevisionNo int    `json:"revisionNo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	kind := app.ScriptSourceKind(body.Source)
	if !app.ValidScriptSourceKinds[kind] {
		http.Error(w, "invalid source kind", http.StatusBadRequest)
		return
	}
	if kind == app.ScriptSourceCustom && strings.TrimSpace(body.CustomText) == "" {
		http.Error(w, "custom source requires customText", http.StatusBadRequest)
		return
	}
	// 源版本优先取显式头；缺失时回退 body.revisionNo（旧客户端）。
	srcRev := requestSourceRevision(r.Header)
	if srcRev <= 0 {
		srcRev = body.RevisionNo
	}
	if err := srcStore.Set(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID, srcRev, slideID, kind, body.CustomText); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, editorSlideSourceItem{SlideID: slideID, Source: body.Source, CustomText: body.CustomText})
}

// editorListSlideSources 返回项目内所有页的讲稿来源选择（M3 ⑥）。
func editorListSlideSources(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if projectID == "" {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	srcRev := requestSourceRevision(r.Header)
	if srcRev <= 0 {
		srcRev, _ = strconv.Atoi(r.URL.Query().Get("revision_no"))
	}
	choices, err := srcStore.List(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID, srcRev)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]editorSlideSourceItem, 0, len(choices))
	for _, choice := range choices {
		items = append(items, editorSlideSourceItem{SlideID: choice.SlideID, Source: string(choice.Kind), CustomText: choice.CustomText})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": items})
}
