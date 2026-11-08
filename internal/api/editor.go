package api

import (
	"net/http"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// editorDeps 是编辑器辅助端点族的依赖集合。
//
// 这里用结构体而不是位置参数，有两个具体理由：
//  1. 端点族按文件拆开后，每个文件只从 deps 取自己需要的那几个字段，
//     不必为新增字段改动所有注册函数的签名；
//  2. 回调参数里 `nil, nil, nil` 这种写法无法自证"哪个依赖被置空"，
//     具名字段至少读起来是 `&editorDeps{objects: objects}`。
//
// 字段语义与位置参数时期完全一致，未做任何取值或校验上的改动。
type editorDeps struct {
	jobs          JobStore
	objects       objectstore.ObjectStore
	scripts       narration.Store
	scriptSources app.ScriptSourceStore
	voiceSettings app.VoiceSettingsStore
	gateways      gateway.StoreResolver
	projects      project.ProjectStore
	members       membership.Reader
	recorder      audit.Recorder
	auth          func(http.Handler) http.Handler
}

// registerEditorRoutes 是本端点族的**路由表**：只做「路径 → handler」的绑定，
// handler 实现按职责分放在同目录的 editor_*.go 里（渲染 / 语音 / 讲稿 / 配音状态 / 版本 / diff / 备注）。
//
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，供编辑器缩略图与 PPT 预览。
// PUT /projects/{pid}/slides/{sid}/source + GET /projects/{pid}/slides/sources 管理"无备注页讲稿来源"选择。
// 均受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, d *editorDeps) {
	auth := d.auth
	// 渲染与解析状态（editor_render.go）
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, d.jobs, d.objects, d.projects, d.members, d.recorder)
	})))
	mux.Handle("GET /projects/{pid}/parse-status", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectParseStatus(w, r, d.jobs, d.objects, d.projects, d.members, d.recorder)
	})))
	// 按源版本汇总的配音状态（editor_narration.go）
	mux.Handle("GET /projects/{pid}/revisions/voice-status", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRevisionVoiceStatus(w, r, d.jobs, d.objects, d.scripts, d.projects, d.members, d.recorder)
	})))
	// M3 ⑥：无备注页讲稿来源选择（editor_script.go）。
	mux.Handle("PUT /projects/{pid}/slides/{sid}/source", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSetSlideSource(w, r, d.scriptSources, d.projects, d.members, d.recorder)
	})))
	mux.Handle("GET /projects/{pid}/slides/sources", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListSlideSources(w, r, d.scriptSources, d.projects, d.members, d.recorder)
	})))
	// G2-2 并排对照：单页「原文素材 vs 当前讲稿」。只读端点，接受修改走既有讲稿保存通道。（scriptcompare.go）
	mux.Handle("GET /projects/{pid}/slides/{sid}/script/compare", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorScriptCompare(w, r, d.objects, d.scripts, d.scriptSources, d.projects, d.members, d.recorder)
	})))
	// 焦点候选：编辑器要知道"这一段能指谁"（形状文字 / 图表的每一根柱子）。
	// 只读；选中后写的是讲稿段落的 focus，走既有保存通道。（focustargets.go）
	mux.Handle("GET /projects/{pid}/slides/{sid}/focus-targets", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorFocusTargets(w, r, d.objects, d.projects, d.members, d.recorder)
	})))
	// 焦点建议（Q7 规则预填）：只读，一段给一组候选而不是替用户定一个。
	// 采纳写的是讲稿段落的 focus，仍走既有保存通道；这里不提供任何写端点。
	mux.Handle("GET /projects/{pid}/slides/{sid}/focus-suggestions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorFocusSuggestions(w, r, d.objects, d.scripts, d.projects, d.members, d.recorder)
	})))
	// 重新生成讲稿（用户显式覆盖已有讲稿）：原生 HTTP，避免改 proto。（editor_script.go）
	mux.Handle("POST /projects/{pid}/script-draft", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRegenerateScriptDraft(w, r, d.jobs, d.scriptSources, d.projects, d.members, d.recorder)
	})))
	// 语音属性与可选语音模型（editor_voice.go）
	mux.Handle("GET /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorGetVoiceSettings(w, r, d.voiceSettings, d.projects, d.members, d.recorder)
	})))
	mux.Handle("PUT /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSaveVoiceSettings(w, r, d.voiceSettings, d.projects, d.members, d.recorder)
	})))
	mux.Handle("GET /projects/{pid}/voice-models", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorVoiceModels(w, r, d.gateways, d.projects, d.members, d.recorder)
	})))
	// 按源版本读取配音状态：供 PPT 列表页「导出」按钮就地弹窗（ExportDialog）时
	// 拿到该版本自己的 timeline/pagePngKeys，而不是 GetNarration 的最新任务。
	mux.Handle("GET /projects/{pid}/revisions/{rev}/narration", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRevisionNarration(w, r, d.jobs, d.objects, d.projects, d.members, d.recorder)
	})))
	// 导入报告：这份源文件被解析成了什么、哪些内容成片时会丢。读的是该次解析的产物，
	// 不重新解析——用户看到的必须和当前讲稿/配音同源。
	mux.Handle("GET /projects/{pid}/revisions/{rev}/import-report", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorImportReport(w, r, d.projects, d.objects, d.members, d.recorder)
	})))
}
