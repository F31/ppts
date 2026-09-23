package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// registerEditorRoutes 挂载核心创作辅助的 HTTP 端点（B2 M2 真实渲染缩略图；B2 M3 ⑥ 无备注页来源）。
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，供编辑器缩略图与 PPT 预览。
// PUT /projects/{pid}/slides/{sid}/source + GET /projects/{pid}/slides/sources 管理"无备注页讲稿来源"选择。
// 均受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, jobs JobStore, objects objectstore.ObjectStore, scripts narration.Store, srcStore app.ScriptSourceStore, voiceStore app.VoiceSettingsStore, gatewayStore gateway.StoreResolver, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, jobs, objects, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/revisions/voice-status", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRevisionVoiceStatus(w, r, jobs, objects, scripts, projects, members, recorder)
	})))
	// M3 ⑥：无备注页讲稿来源选择。
	mux.Handle("PUT /projects/{pid}/slides/{sid}/source", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSetSlideSource(w, r, srcStore, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/slides/sources", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListSlideSources(w, r, srcStore, projects, members, recorder)
	})))
	// 重新生成讲稿（用户显式覆盖已有讲稿）：原生 HTTP，避免改 proto。
	mux.Handle("POST /projects/{pid}/script-draft", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRegenerateScriptDraft(w, r, jobs, srcStore, projects, members, recorder)
	})))
	// 语音属性：项目级语音模型 / 音色 / 语速的读写。
	mux.Handle("GET /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorGetVoiceSettings(w, r, voiceStore, projects, members, recorder)
	})))
	mux.Handle("PUT /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSaveVoiceSettings(w, r, voiceStore, projects, members, recorder)
	})))
	// 可选语音模型（按 TTS 网关配置）+ 每个模型配置的音色。
	mux.Handle("GET /projects/{pid}/voice-models", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorVoiceModels(w, r, gatewayStore, projects, members, recorder)
	})))
	// 按源版本读取配音状态：供 PPT 列表页「导出」按钮就地弹窗（ExportDialog）时
	// 拿到该版本自己的 timeline/pagePngKeys，而不是 GetNarration 的最新任务。
	mux.Handle("GET /projects/{pid}/revisions/{rev}/narration", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRevisionNarration(w, r, jobs, objects, projects, members, recorder)
	})))
}

type projectVoiceSettingsPayload struct {
	Model       string `json:"model"`
	Voice       string `json:"voice"`
	RatePercent int    `json:"ratePercent"`
}

// editorGetVoiceSettings 读取项目语音属性；未保存过时返回缺省（rate=100）。
func editorGetVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	settings, err := voiceStore.Get(r.Context(), principal.TenantID, projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

// editorSaveVoiceSettings 保存项目语音属性（语音模型 / 音色 / 语速）。
func editorSaveVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
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
	var body projectVoiceSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	settings := app.VoiceSettings{
		Model: strings.TrimSpace(body.Model), Voice: strings.TrimSpace(body.Voice), RatePercent: body.RatePercent,
	}
	if settings.RatePercent == 0 {
		settings.RatePercent = 100
	}
	if settings.RatePercent < 50 || settings.RatePercent > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "rate_percent must be between 50 and 200"})
		return
	}
	if err := voiceStore.Save(r.Context(), principal.TenantID, projectID, settings); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

type voiceModelItem struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Voices    []string `json:"voices"`
	IsDefault bool     `json:"isDefault"`
}

// editorVoiceModels 列出本租户已启用的 TTS 网关（语音模型）及其配置的音色。
// 音色来自网关配置的 voice 字段（多个逗号分隔）；无可用网关时返回空列表，前端据此降级。
func editorVoiceModels(w http.ResponseWriter, r *http.Request, gatewayStore gateway.StoreResolver, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if gatewayStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "model gateway disabled"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	gws, err := gatewayStore.List(r.Context(), principal.TenantID, gateway.KindTTS)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]voiceModelItem, 0, len(gws))
	for _, gw := range gws {
		if gw == nil || !gw.Enabled {
			continue
		}
		voices := remoteTTSVoices(r.Context(), gatewayStore, principal.TenantID, gw)
		if len(voices) == 0 {
			voices = configuredTTSVoices(gw)
		}
		items = append(items, voiceModelItem{Name: gw.Name, Model: gw.Model, Voices: voices, IsDefault: gw.IsDefault})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": items})
}

func configuredTTSVoices(gw *gateway.Gateway) []string {
	voices := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(raw string) {
		v := strings.TrimSpace(raw)
		if v == "" || (gw.Model != "" && v == gw.Model) {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		voices = append(voices, v)
	}
	for _, raw := range strings.Split(gw.Voice, ",") {
		add(raw)
	}
	if len(voices) == 0 && strings.TrimSpace(gw.Model) != "" {
		add(tts.DefaultSiliconFlowVoice(gw.Model))
	}
	return voices
}

func remoteTTSVoices(ctx context.Context, store gateway.StoreResolver, tenantID string, gw *gateway.Gateway) []string {
	cfg, err := store.ResolveNamed(ctx, tenantID, gw.Name, gateway.KindTTS)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, path := range []string{"/v1/audio/voices", "/v1/voices"} {
		voices := fetchTTSVoices(ctx, cfg, path)
		if len(voices) > 0 {
			return voices
		}
	}
	return nil
}

func fetchTTSVoices(ctx context.Context, cfg *gateway.Config, path string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+path, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseVoiceList(body, cfg.Model)
}

func parseVoiceList(body []byte, model string) []string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	var add func(any)
	add = func(v any) {
		switch x := v.(type) {
		case string:
			id := strings.TrimSpace(x)
			if id == "" || id == model {
				return
			}
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		case map[string]any:
			for _, key := range []string{"id", "voice", "voice_id", "name"} {
				if raw, ok := x[key]; ok {
					add(raw)
					return
				}
			}
		case []any:
			for _, item := range x {
				add(item)
			}
		}
	}
	switch root := payload.(type) {
	case []any:
		add(root)
	case map[string]any:
		for _, key := range []string{"voices", "data", "items", "result"} {
			if raw, ok := root[key]; ok {
				add(raw)
			}
		}
	}
	return out
}

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

type editorSlideRenderItem struct {
	SlideID string `json:"slideId"`
	URL     string `json:"url"`
}

// editorSlideRender 读取最近一次成功解析任务的页面清单（PageManifest），为每页渲染 PNG 签发短期匿名可读 URL。
// 解析未完成、页面清单缺失或某页渲染图不存在时，对应项跳过；整体缺失时返回空列表，前端优雅降级为序号/标题缩略图。
func editorSlideRender(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	ctx := r.Context()
	job, err := jobs.LatestSucceededJob(ctx, principal.TenantID, projectID, string(pipeline.KindParse))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if revStr := r.URL.Query().Get("revision_no"); revStr != "" {
		revNo, perr := strconv.Atoi(revStr)
		if perr != nil {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
			return
		}
		matched, merr := parseJobForRevision(ctx, jobs, principal.TenantID, projectID, revNo)
		if errors.Is(merr, pipeline.ErrNoSucceededJob) {
			writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
			return
		}
		if merr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		job = matched
	}
	ref, err := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "pages")
	if err != nil || ref == "" {
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		return
	}
	manifest, err := loadPageManifest(ctx, objects, principal.TenantID, ref)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parser, _ := objects.(signedURLParser)
	ttl := 15 * time.Minute
	out := make([]editorSlideRenderItem, 0, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID == "" || pg.Key == "" {
			continue
		}
		key, perr := objectstore.Parse(pg.Key)
		if perr != nil {
			continue
		}
		if perr = key.EnsureTenant(principal.TenantID); perr != nil {
			continue
		}
		signed, serr := objects.SignedURL(ctx, key, objectstore.OpRead, ttl)
		if serr != nil {
			continue
		}
		url := signed
		if parser != nil {
			url = rewriteLocalSignedURL(parser, signed)
		}
		out = append(out, editorSlideRenderItem{SlideID: pg.SlideID, URL: url})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": out})
}

type revisionVoiceStatus struct {
	RevisionNo   int      `json:"revisionNo"`
	Status       string   `json:"status"`
	PageCount    int      `json:"pageCount"`
	VoicedPages  int      `json:"voicedPages"`
	MissingPages int      `json:"missingPages"`
	StalePages   int      `json:"stalePages"`
	MissingIDs   []string `json:"missingSlideIds,omitempty"`
	StaleIDs     []string `json:"staleSlideIds,omitempty"`
}

type revisionPages struct {
	count int
	ids   map[string]struct{}
}

func editorRevisionVoiceStatus(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, scripts narration.Store, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revs, err := projects.ListSourceRevisions(r.Context(), principal.TenantID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	language := requestLanguage(r.Header)
	pagesByRev := make(map[int]revisionPages, len(revs))
	// 讲稿按源版本隔离：逐版本取稿，避免同 slide_id 跨版本串扰。
	currentByRev := make(map[int]map[string]*narration.Revision, len(revs))
	for _, rv := range revs {
		docs, _ := loadProjectDocuments(r.Context(), projects, objects, principal.TenantID, projectID, rv.RevisionNo, rv.RevisionNo)
		ids := map[string]struct{}{}
		count := rv.PageCount
		if doc := docs[rv.RevisionNo]; doc != nil {
			count = len(doc.Pages)
			for _, pg := range doc.Pages {
				if pg != nil && pg.SlideID != "" {
					ids[pg.SlideID] = struct{}{}
				}
			}
		}
		pagesByRev[rv.RevisionNo] = revisionPages{count: count, ids: ids}
		list, _ := scripts.ListByProject(r.Context(), principal.TenantID, projectID, rv.RevisionNo, language)
		m := make(map[string]*narration.Revision, len(list))
		for _, s := range list {
			m[s.SlideID] = s
		}
		currentByRev[rv.RevisionNo] = m
	}
	voicedByRev := map[int]map[string]int64{}
	if jobs != nil {
		var cursor string
		for {
			page, next, jerr := jobs.List(r.Context(), principal.TenantID, projectID, string(pipeline.StateSucceeded), cursor, 100)
			if jerr != nil {
				writeConnectError(w, connect.NewError(connect.CodeInternal, jerr))
				return
			}
			for _, job := range page {
				if job.Kind != pipeline.KindNarration {
					continue
				}
				var snap app.NarrationSnapshot
				if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
					continue
				}
				revNo := snap.RevisionNo
				if revNo == 0 {
					revNo = inferNarrationRevision(snap, revs, pagesByRev)
				}
				if revNo == 0 {
					continue
				}
				m := voicedByRev[revNo]
				if m == nil {
					m = map[string]int64{}
					voicedByRev[revNo] = m
				}
				for _, slide := range snap.Slides {
					if slide.SlideID == "" {
						continue
					}
					if slide.ScriptRevision > m[slide.SlideID] {
						m[slide.SlideID] = slide.ScriptRevision
					}
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	out := make([]revisionVoiceStatus, 0, len(revs))
	for _, rv := range revs {
		pages := pagesByRev[rv.RevisionNo]
		voiced := voicedByRev[rv.RevisionNo]
		item := revisionVoiceStatus{RevisionNo: rv.RevisionNo, PageCount: pages.count, Status: "not_voiced"}
		for slideID := range pages.ids {
			scriptRev, ok := voiced[slideID]
			if !ok {
				item.MissingIDs = append(item.MissingIDs, slideID)
				continue
			}
			item.VoicedPages++
			if cur := currentByRev[rv.RevisionNo][slideID]; cur != nil && cur.Revision > scriptRev {
				item.StalePages++
				item.StaleIDs = append(item.StaleIDs, slideID)
			}
		}
		item.MissingPages = len(item.MissingIDs)
		switch {
		case item.VoicedPages == 0:
			item.Status = "not_voiced"
		case item.PageCount == 0 || item.VoicedPages < item.PageCount:
			item.Status = "partial"
		case item.StalePages > 0:
			item.Status = "stale"
		default:
			item.Status = "complete"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// editorRevisionNarration 读取指定源版本的配音状态（ready/timelineKey/pagePngKeys/revisionNo）。
// 供 PPT 列表页「导出」按钮就地弹 ExportDialog：只取该版本自己的配音任务（按 snapshot.RevisionNo
// 归集，复刻 editorRevisionVoiceStatus 的扫描逻辑），避免 GetNarration 只回“最新成功任务”导致
// 旧版本弹窗无素材。
func editorRevisionNarration(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revNo, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || revNo <= 0 {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	ctx := r.Context()
	// 找该版本匹配的成功配音任务（与 editorRevisionVoiceStatus 相同的按版本归集口径）。
	var timelineKey string
	var jobID string
	var cursor string
	for {
		page, next, jerr := jobs.List(ctx, principal.TenantID, projectID, string(pipeline.StateSucceeded), cursor, 100)
		if jerr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, job := range page {
			if job.Kind != pipeline.KindNarration {
				continue
			}
			var snap app.NarrationSnapshot
			if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
				continue
			}
			if snap.RevisionNo != revNo {
				continue
			}
			ref, rerr := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "timeline")
			if rerr != nil || ref == "" {
				continue
			}
			timelineKey = ref
			jobID = job.ID
			break
		}
		if timelineKey != "" || next == "" {
			break
		}
		cursor = next
	}
	if timelineKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false, "timelineKey": "", "pagePngKeys": []string{}, "revisionNo": revNo})
		return
	}
	pagePngKeys, _ := resolvePagePngKeys(ctx, jobs, objects, principal.TenantID, projectID, timelineKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"ready": true, "timelineKey": timelineKey, "pagePngKeys": pagePngKeys, "revisionNo": revNo, "jobId": jobID,
	})
}

func inferNarrationRevision(snap app.NarrationSnapshot, revs []*project.SourceRevision, pagesByRev map[int]revisionPages) int {
	if len(snap.Slides) == 0 {
		return 0
	}
	for _, rv := range revs {
		pages := pagesByRev[rv.RevisionNo]
		if len(pages.ids) == 0 {
			continue
		}
		matched := true
		for _, slide := range snap.Slides {
			if _, ok := pages.ids[slide.SlideID]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return rv.RevisionNo
		}
	}
	return 0
}

func parseJobForRevision(ctx context.Context, jobs JobStore, tenantID, projectID string, revisionNo int) (*pipeline.Job, error) {
	var cursor string
	for {
		page, next, err := jobs.List(ctx, tenantID, projectID, string(pipeline.StateSucceeded), cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, job := range page {
			if job.Kind != pipeline.KindParse {
				continue
			}
			var snap app.ParseSnapshot
			if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err == nil && snap.RevisionNo == revisionNo {
				return job, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil, pipeline.ErrNoSucceededJob
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

// registerRevisionRoutes 挂载源版本历史只读端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET    /projects/{pid}/revisions                 返回 current_revision 与未软删版本列表（倒序）。
//   - PATCH  /projects/{pid}/revisions/{revisionNo}    更新 PPT 显示名。
//   - DELETE /projects/{pid}/revisions/{revisionNo}    软删指定版本（禁止删除当前生效版本）。
//   - GET    /projects/{pid}/slides/{sid}/notes        读取单页备注。
//   - PATCH  /projects/{pid}/slides/{sid}/notes        保存单页备注。
func registerRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, members membership.Reader, notesStore project.SlideNotesStore, objects objectstore.ObjectStore, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListRevisions(w, r, projects, members, recorder)
	})))
	mux.Handle("PATCH /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorUpdateRevisionDisplayName(w, r, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/revisions/{revisionNo}/download", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDownloadRevision(w, r, projects, members, recorder, objects)
	})))
	mux.Handle("DELETE /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDeleteRevision(w, r, projects, members, recorder)
	})))
	// 已归档项目列表与恢复（原生 HTTP，绕过 proto）：GET /projects/archived、POST /projects/{pid}/restore。
	mux.Handle("GET /projects/archived", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListArchivedProjects(w, r, projects, members)
	})))
	mux.Handle("POST /projects/{pid}/restore", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRestoreProject(w, r, projects, members, recorder)
	})))
	if notesStore != nil {
		mux.Handle("GET /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorGetSlideNotes(w, r, notesStore, objects)
		})))
		mux.Handle("PATCH /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorSetSlideNotes(w, r, notesStore, projects, members, recorder)
		})))
	}
}

// editorListRevisions 返回项目源版本历史。current_revision 来自 projects 行（即"当前生效版本"）；
// 版本列表排除 source_deleted_at 非空的软删版本（保留不可变版本行用于追溯，但不可预览）。
func editorListRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	userID, _ := projectAccessUser(r.Context(), members)
	proj, err := projects.GetProject(r.Context(), principal.TenantID, userID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	revs, err := projects.ListSourceRevisions(r.Context(), principal.TenantID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(revs))
	for _, rv := range revs {
		out = append(out, map[string]any{
			"revision_no":    rv.RevisionNo,
			"created_at":     rv.CreatedAt.Format(time.RFC3339),
			"page_count":     rv.PageCount,
			"parser_version": rv.ParserVersion,
			"object_key":     rv.ObjectKey,
			"display_name":   rv.DisplayName,
			"is_current":     rv.RevisionNo == proj.CurrentRevision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"current_revision": proj.CurrentRevision, "revisions": out})
}

// editorUpdateRevisionDisplayName 更新 PPT 展示名（EDITOR+）。
func editorUpdateRevisionDisplayName(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := projects.UpdateSourceRevisionDisplayName(r.Context(), principal.TenantID, projectID, revNo, body.DisplayName); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorDownloadRevision 下载指定源版本的原始 PPTX（项目可访问即可下载）。
func editorDownloadRevision(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	rv, err := projects.GetSourceRevision(r.Context(), principal.TenantID, projectID, revNo)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	key, err := objectstore.Parse(rv.ObjectKey)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	rc, meta, err := objects.Get(r.Context(), key)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	defer rc.Close()
	name := strings.TrimSpace(rv.DisplayName)
	if name == "" {
		name = "presentation.pptx"
	}
	name = strings.ReplaceAll(name, "\"", "'")
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	if meta.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// editorDeleteRevision 软删指定源版本（禁止删除当前生效版本）。
func editorDeleteRevision(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	if err := projects.DeleteSourceRevision(r.Context(), principal.TenantID, projectID, revNo); err != nil {
		switch {
		case errors.Is(err, project.ErrProjectNotFound):
			writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		case errors.Is(err, project.ErrDeleteCurrentRevision), errors.Is(err, project.ErrDeleteLastRevision):
			writeConnectError(w, connect.NewError(connect.CodeFailedPrecondition, err))
		default:
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		}
		return
	}
	recorder.Record(r.Context(), audit.Event{
		TenantID:     principal.TenantID,
		ActorUser:    principal.UserID,
		Action:       "project.source_revision.delete",
		ResourceType: "project",
		ResourceID:   projectID,
		Metadata:     map[string]any{"revision_no": revNo},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorDiffRevisions 比较两个源版本的幻灯片差异（V4.0 §7.1 版本管理增强）。
// GET /projects/{pid}/revisions/{revA}/diff/{revB}
// 返回 {added: [{slideId,name}], removed: [...], changed: [{slideId,oldName,newName,oldNotes,newNotes}]}
func editorDiffRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revAStr := r.PathValue("revA")
	revBStr := r.PathValue("revB")
	if projectID == "" || revAStr == "" || revBStr == "" {
		http.Error(w, "project_id, revA, revB are required", http.StatusBadRequest)
		return
	}
	revA, err := strconv.Atoi(revAStr)
	if err != nil {
		http.Error(w, "invalid revA", http.StatusBadRequest)
		return
	}
	revB, err := strconv.Atoi(revBStr)
	if err != nil {
		http.Error(w, "invalid revB", http.StatusBadRequest)
		return
	}
	if revA == revB {
		writeJSON(w, http.StatusOK, map[string]any{"added": []map[string]any{}, "removed": []map[string]any{}, "changed": []map[string]any{}})
		return
	}

	docs, err := loadProjectDocuments(r.Context(), projects, objects, principal.TenantID, projectID, revA, revB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	diff := diffDocuments(docs[revA], docs[revB])
	writeJSON(w, http.StatusOK, diff)
}

// loadProjectDocuments 按 revision_no 顺序读取两个版本的 extracted document.json。
// 返回 map[revisionNo]*project.Document；缺失版本为 nil。
func loadProjectDocuments(ctx context.Context, projects project.ProjectStore, objects objectstore.ObjectStore, tenantID, projectID string, revA, revB int) (map[int]*project.Document, error) {
	out := make(map[int]*project.Document, 2)
	for _, rev := range [...]int{revA, revB} {
		_, err := projects.GetSourceRevision(ctx, tenantID, projectID, rev)
		if err != nil {
			out[rev] = nil
			continue
		}
		key := objectstore.ObjectKey{
			TenantID: tenantID, ProjectID: projectID,
			Revision: srcRevString(rev), AssetType: "document", AssetID: "extracted", Ext: "json",
		}
		rc, _, err := objects.Get(ctx, key)
		if err != nil {
			out[rev] = nil
			continue
		}
		var data []byte
		data, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			out[rev] = nil
			continue
		}
		var doc project.Document
		if err := json.Unmarshal(data, &doc); err != nil {
			out[rev] = nil
			continue
		}
		out[rev] = &doc
	}
	return out, nil
}

// diffDocuments 比较两个 Document，返回页级差异。
// oldDoc=revA, newDoc=revB：added=新增页、removed=删除页、changed=标题/备注变更页。
func diffDocuments(oldDoc, newDoc *project.Document) map[string]any {
	added := []map[string]any{}
	removed := []map[string]any{}
	changed := []map[string]any{}

	oldIndex := make(map[string]*project.Page, len(oldDoc.Pages))
	newIndex := make(map[string]*project.Page, len(newDoc.Pages))
	for _, pg := range oldDoc.Pages {
		oldIndex[pg.SlideID] = pg
	}
	for _, pg := range newDoc.Pages {
		newIndex[pg.SlideID] = pg
	}

	// removed: 在旧版有、新版无
	for id, oldPg := range oldIndex {
		if _, ok := newIndex[id]; !ok {
			removed = append(removed, map[string]any{"slideId": id, "name": oldPg.Name, "pageCount": oldDoc.Features.PageCount})
		}
	}
	// added: 在新版有、旧版无
	for id, newPg := range newIndex {
		if _, ok := oldIndex[id]; !ok {
			added = append(added, map[string]any{"slideId": id, "name": newPg.Name, "pageCount": newDoc.Features.PageCount})
		}
	}
	// changed: 同 slideId 但 title 或 notes 变化
	for id, newPg := range newIndex {
		oldPg, ok := oldIndex[id]
		if !ok {
			continue
		}
		if oldPg.Name != newPg.Name || oldPg.NotesText != newPg.NotesText {
			changed = append(changed, map[string]any{
				"slideId":   id,
				"oldName":   oldPg.Name,
				"newName":   newPg.Name,
				"oldNotes":  truncate(oldPg.NotesText, 80),
				"newNotes":  truncate(newPg.NotesText, 80),
				"pageCount": newDoc.Features.PageCount,
			})
		}
	}
	return map[string]any{
		"added":   added,
		"removed": removed,
		"changed": changed,
	}
}

// registerDiffRevisionRoutes 挂载源版本 diff 端点。
func registerDiffRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions/{revA}/diff/{revB}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDiffRevisions(w, r, projects, objects, members, recorder)
	})))
}

// editorGetSlideNotes 读取单页演讲者备注（任意已认证成员可见）。
// 用户手写备注优先；缺失时回退解析文档中的原始备注，保证 PPT 自带备注可见。
func editorGetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	m, err := notesStore.Get(r.Context(), principal.TenantID, projectID, revNo)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	notes := ""
	stored, hasStored := "", false
	if m != nil {
		stored, hasStored = m[slideID]
	}
	switch {
	case hasStored:
		notes = stored // 用户显式覆盖（可能为空串 = 已清空）
	case objects != nil:
		notes = docSlideNotes(r.Context(), objects, principal.TenantID, projectID, revNo, slideID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slide_id": slideID, "notes": notes})
}

// docSlideNotes 从解析产物中读取指定页的原始备注；读取失败或无该页时返回空串。
func docSlideNotes(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revisionNo int, slideID string) string {
	docKey := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := objects.Get(ctx, docKey)
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	var doc struct {
		Pages []struct {
			SlideID   string `json:"slideId"`
			NotesText string `json:"notesText"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	for _, pg := range doc.Pages {
		if pg.SlideID == slideID {
			return strings.TrimSpace(pg.NotesText)
		}
	}
	return ""
}

// editorSetSlideNotes 保存单页演讲者备注（EDITOR+）。
func editorSetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := notesStore.Set(r.Context(), principal.TenantID, projectID, revNo, slideID, body.Notes); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorListArchivedProjects 返回已归档项目列表（供恢复入口）。
// 原生 HTTP：GET /projects/archived → {"projects":[{id,tenantId,owner,title,currentRevision,archived,createdAtUnix}]}。
func editorListArchivedProjects(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleViewer); err != nil {
		writeConnectError(w, err)
		return
	}
	list, _, err := projects.ListArchivedProjects(r.Context(), principal.TenantID, principal.UserID, "", 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		m := map[string]any{
			"id":              p.ID,
			"tenantId":        p.TenantID,
			"owner":           p.OwnerUser,
			"title":           p.Title,
			"currentRevision": p.CurrentRevision,
			"archived":        p.Archived,
			"createdAtUnix":   p.CreatedAt.Unix(),
			"archivedBy":      p.ArchivedBy,
			"archivedByName":  p.ArchivedByName,
			"archivedByEmail": p.ArchivedByEmail,
		}
		if !p.ArchivedAt.IsZero() {
			m["archivedAtUnix"] = p.ArchivedAt.Unix()
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// editorRestoreProject 恢复已归档项目。POST /projects/{pid}/restore（需 ADMIN，与归档同级）。
func editorRestoreProject(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleAdmin); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	if projectID == "" {
		http.Error(w, "pid required", http.StatusBadRequest)
		return
	}
	p, err := projects.UnarchiveProject(r.Context(), principal.TenantID, "", projectID)
	if err != nil {
		if errors.Is(err, project.ErrProjectNotFound) {
			writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if recorder != nil {
		recorder.Record(r.Context(), audit.Event{
			TenantID: principal.TenantID, ActorUser: principal.UserID,
			Action: "project.restore", ResourceType: "project", ResourceID: projectID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              p.ID,
		"tenantId":        p.TenantID,
		"owner":           p.OwnerUser,
		"title":           p.Title,
		"currentRevision": p.CurrentRevision,
		"archived":        p.Archived,
		"createdAtUnix":   p.CreatedAt.Unix(),
	})
}
