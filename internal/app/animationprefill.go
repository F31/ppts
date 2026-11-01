package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
)

// 本文件是「预填异步通道」的 worker 侧：把一页的元素与分段交给绑定器，
// 模型档（script）才真的调模型，结果按内容指纹落到对象存储。
//
// 为什么要有这条异步通道，而不是继续在 GET 里同步跑：
// 只有 script 档会问模型，实测单轮 P95 11.7s、5 轮并发 28–43s。把它塞进
// `GET /animation?default=true&prefill=true` 意味着编辑器打开时有一个页面会挂住十几秒，
// 超时上限还得跟着调——渲染一页动效不该让「打开编辑器」变成一次长轮询。

const (
	// prefillResultSchemaVersion 是结果对象的格式版本。改字段语义时递增；
	// 读端不认识就不认这条结果（宁可退回规则，也不要把旧字段当新语义用）。
	prefillResultSchemaVersion = "1"
	// prefillAssetType 是对象键的资产类型段。
	prefillAssetType = "prefill"
)

// PrefillResult 是一页预填的产出。它是**建议**，不是规格：编辑器拿到它之后
// 仍要经 animdefault 生成一份默认规格，再由用户决定保存与否。
type PrefillResult struct {
	SchemaVersion string `json:"schemaVersion"`
	SlideID       string `json:"slideId"`
	// ContentHash 与 Snapshot.ContentHash 对应：读端据此确认「这条结果算的是这一版内容」。
	ContentHash string `json:"contentHash"`
	JobID       string `json:"jobId"`
	// Route 是实际走的通道（rule / model）。model 且 Model.Fallback 为空才是模型真的产出了结果。
	Route       string         `json:"route"`
	Band        string         `json:"band"`
	BandReason  string         `json:"bandReason,omitempty"`
	Signals     []string       `json:"signals,omitempty"`
	Bindings    []bind.Binding `json:"bindings"`
	Model       bind.ModelRun  `json:"model"`
	GeneratedAt time.Time      `json:"generatedAt"`
}

// prefillObjectKey 是「某页某版内容」的预填结果对象键。
//
// 内容指纹进键，不设失效逻辑：页面文字或讲稿一改，指纹就变了，旧结果自然读不到。
// 这比「写入时删旧、读取时判新鲜」少一整类竞态，也不会出现「改了讲稿却还看到旧绑定」。
func prefillObjectKey(tenantID, projectID string, revNo int, contentHash string) objectstore.ObjectKey {
	return objectstore.ObjectKey{
		TenantID:  tenantID,
		ProjectID: projectID,
		Revision:  srcRevString(revNo),
		AssetType: prefillAssetType,
		AssetID:   "slide." + contentHash,
		Ext:       "json",
	}
}

// LoadPrefillResult 读一页的预填结果。
//
// 三个返回值刻意分开：ok=false 且 err=nil 是「没有」——正常情况（还没跑过、内容变了）；
// err!=nil 是「读的时候出错了」，调用方可以选择退回规则并如实说明，
// 而不是把存储故障伪装成「这页没有预填」。
func LoadPrefillResult(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revNo int, contentHash string) (*PrefillResult, bool, error) {
	if objects == nil || strings.TrimSpace(contentHash) == "" {
		return nil, false, nil
	}
	rc, _, err := objects.Get(ctx, prefillObjectKey(tenantID, projectID, revNo, contentHash))
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil, false, err
	}
	var out PrefillResult
	if err := json.Unmarshal(data, &out); err != nil {
		// 损坏的结果当作没有：它不可用，但也别让它变成一次 500——编辑器还有规则兜底。
		return nil, false, nil
	}
	if out.SchemaVersion != prefillResultSchemaVersion || out.ContentHash != contentHash {
		return nil, false, nil
	}
	return &out, true, nil
}

// SavePrefillResult 写一页的预填结果（按内容指纹落键）。
//
// 导出是为了让「写入」与「读取」在同一处定义键：测试要造一份结果时，
// 自己拼键就必然有一天拼错，而错键的表现是「读不到」——看起来像功能没跑。
func SavePrefillResult(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revNo int, res *PrefillResult) error {
	if objects == nil || res == nil {
		return errors.New("animation_prefill: object store or result is nil")
	}
	if res.Bindings == nil {
		res.Bindings = []bind.Binding{}
	}
	if res.Signals == nil {
		res.Signals = []string{}
	}
	data, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("animation_prefill: marshal result: %w", err)
	}
	if err := objects.Put(ctx, prefillObjectKey(tenantID, projectID, revNo, res.ContentHash),
		bytes.NewReader(data), objectstore.ObjectMeta{
			ContentType: "application/json",
			ContentHash: hashBytes(data),
			Size:        int64(len(data)),
		}); err != nil {
		return fmt.Errorf("animation_prefill: write result: %w", err)
	}
	return nil
}

// PrefillOpID 是预填在额度账本里的逻辑操作 id。
//
// 导出是因为取消/失败时的释放发生在 worker 里（releaseUsage），而预占发生在 handler 里。
// 两处各拼一次字符串，「拼接规则改了但只改一处」就会让额度永远锁着——
// 用户看到的是「这个月没额度了」，实际一次都没跑成功。
func PrefillOpID(jobID string) string { return "prefill:" + jobID }

// AnimationPrefillHandler 执行 animation_prefill 任务。
//
// 与其它 handler 的最大差别：**它永不因为模型而失败**。没配模型、超时、返回垃圾、
// 额度不够，都写下一份规则结果并正常结束。预填是编辑器打开时的增强项，
// 让一次网关抖动把任务标红，用户看到的只是「预填失败」，而他本来也有一条可用的规则结果。
type AnimationPrefillHandler struct {
	objects objectstore.ObjectStore
	// completer 按租户解析模型通道；nil 或返回错误都视为「没有模型」，退回规则。
	completer func(ctx context.Context, tenantID string) (bind.ModelCompleter, error)
	tokens    LLMTokenAccountant
	// timeout 单页模型调用上限；<=0 用 bind 的默认值（45s）。
	timeout time.Duration
}

// NewAnimationPrefillHandler 创建 handler。objects 为 nil 时任务必然失败：
// 没有地方写结果，跑完也读不回来。
func NewAnimationPrefillHandler(objects objectstore.ObjectStore) *AnimationPrefillHandler {
	return &AnimationPrefillHandler{objects: objects}
}

// WithCompleter 注入按租户解析的模型通道（模型网关）；未注入时只跑规则。
func (h *AnimationPrefillHandler) WithCompleter(f func(ctx context.Context, tenantID string) (bind.ModelCompleter, error)) *AnimationPrefillHandler {
	h.completer = f
	return h
}

// WithTokenAccounting 注入 LLM token 额度记账；未注入时不做预占/结算（测试/私有化）。
func (h *AnimationPrefillHandler) WithTokenAccounting(a LLMTokenAccountant) *AnimationPrefillHandler {
	h.tokens = a
	return h
}

// WithTimeout 覆盖单页模型调用上限。
func (h *AnimationPrefillHandler) WithTimeout(d time.Duration) *AnimationPrefillHandler {
	h.timeout = d
	return h
}

// Handle 实现 pipeline.HandlerFunc。
func (h *AnimationPrefillHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	if job == nil {
		return errors.New("animation_prefill: nil job")
	}
	if h.objects == nil {
		return errors.New("animation_prefill: object store is not configured")
	}
	var snap bind.Snapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		return fmt.Errorf("animation_prefill: parse snapshot: %w", err)
	}
	// 快照解析成功但内容为空，说明调用方写坏了快照。按规则跑会得到一份「一条都没绑」的
	// 结果并被当成成功——那比失败更难查，所以这里显式拒绝。
	if snap.SlideID == "" || len(snap.Elements) == 0 {
		return errors.New("animation_prefill: snapshot has no slide id or elements")
	}
	hash := snap.ContentHash()

	opt := bind.RoutedOptions{Timeout: h.timeout}
	opID := PrefillOpID(job.ID)
	var meter *meteringCompleter
	quotaBlocked := false
	if c := h.modelFor(ctx, job.TenantID); c != nil {
		meter = &meteringCompleter{inner: c}
		opt.Model = meter
		if err := h.reserveLLMTokens(ctx, job.TenantID, opID, snap); err != nil {
			if !errors.Is(err, usage.ErrInsufficientQuota) {
				// 记账存储坏了不等于「额度不够」。这种情况必须让任务重试：
				// 放行就会产生一笔没人记账的模型调用——「预填偷偷烧额度」正是要杜绝的。
				return &pipeline.RetryError{
					Err: fmt.Errorf("animation_prefill: reserve llm tokens: %w", err),
					At:  time.Now().Add(15 * time.Second),
				}
			}
			// 额度不够就不问模型：请求发出去再「退」是退不回来的。
			// 保留规则结果并把原因写进 Model.Fallback，任务仍是成功终态。
			opt.Model = nil
			meter = nil
			quotaBlocked = true
		}
	}

	res := bind.RunRouted(ctx, snap.SlideID, snap.Elements, snap.Segments, opt)
	if quotaBlocked {
		res.Model.Fallback = bind.ModelFallbackQuota
	}
	if meter != nil {
		h.settleLLMTokens(ctx, job.TenantID, opID, meter)
	}

	out := &PrefillResult{
		SchemaVersion: prefillResultSchemaVersion,
		SlideID:       snap.SlideID,
		ContentHash:   hash,
		JobID:         job.ID,
		Route:         string(res.Route),
		Band:          string(res.Band),
		BandReason:    res.BandReason,
		Signals:       res.Signals,
		Bindings:      res.Bindings,
		Model:         res.Model,
		GeneratedAt:   time.Now().UTC(),
	}
	return SavePrefillResult(ctx, h.objects, job.TenantID, job.ProjectID, snap.SourceRevisionNo, out)
}

// modelFor 解析本租户的模型通道；任何失败都当作「没有模型」。
func (h *AnimationPrefillHandler) modelFor(ctx context.Context, tenantID string) bind.ModelCompleter {
	if h.completer == nil {
		return nil
	}
	c, err := h.completer(ctx, tenantID)
	if err != nil || c == nil {
		return nil
	}
	return c
}

// reserveLLMTokens 预占本次预填可能用掉的 token。
//
// 估算用快照里的全部文字量：模型看得到的只有这些（单元素/单段还有截断），
// 所以这是个上界。用上界预占、用真实用量结算，与讲稿成稿同一套口径。
func (h *AnimationPrefillHandler) reserveLLMTokens(ctx context.Context, tenantID, opID string, snap bind.Snapshot) error {
	if h.tokens == nil {
		return nil
	}
	runes := 0
	for _, e := range snap.Elements {
		runes += utf8.RuneCountInString(e.Text)
	}
	for _, s := range snap.Segments {
		runes += utf8.RuneCountInString(s.Text)
	}
	if _, err := h.tokens.Reserve(ctx, tenantID, opID, usage.KindLLMTokens, usage.EstimateLLMTokens(runes)); err != nil {
		return err
	}
	return nil
}

// settleLLMTokens 按供应商回传的真实 token 数结算。记账失败不改变任务结果：
// 账本最终一致性由重试/TTL 兜底，而已经算出来的绑定没必要因此丢掉。
func (h *AnimationPrefillHandler) settleLLMTokens(ctx context.Context, tenantID, opID string, meter *meteringCompleter) {
	if h.tokens == nil || meter == nil || !meter.called {
		return
	}
	actual := float64(meter.promptTokens + meter.completionTokens)
	_ = h.tokens.Settle(ctx, tenantID, opID, usage.KindLLMTokens, actual, "")
}

// meteringCompleter 透传模型调用并记下真实 token 用量。
//
// 用量只能从这一次调用的返回值拿到（RunRouted 不把 completion 透出来，
// 它也不该关心记账），所以包一层最省：绑定器的端口定义只有「问」和「答」。
type meteringCompleter struct {
	inner            bind.ModelCompleter
	called           bool
	promptTokens     int64
	completionTokens int64
}

func (m *meteringCompleter) Complete(ctx context.Context, req bind.ModelPrompt) (bind.ModelCompletion, error) {
	m.called = true
	out, err := m.inner.Complete(ctx, req)
	m.promptTokens += out.PromptTokens
	m.completionTokens += out.CompletionTokens
	return out, err
}
