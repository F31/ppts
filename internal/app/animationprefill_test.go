package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/usage"
)

// 本文件盯的是「异步预填」的四条行为，而不是绑定算法本身（那在 bind 包里）：
//
//  1. 没配模型也必须写下一份规则结果，任务照常成功——预填失败不该让编辑器空着；
//  2. script 档真的调模型，且结果里的 route 会变成 model（不是「都算规则」）；
//  3. 模型超时/额度不足都退回规则，原因写进 Model.Fallback（这三种原因运维动作不同）；
//  4. token 记账用的是**真实用量**，不是估算值——估算只用于预占。

const prefillTestTenant = "11111111-1111-1111-1111-111111111111"
const prefillTestProject = "22222222-2222-2222-2222-222222222222"

// prefillJobPage 是「版面把顺序编码出来了、讲稿没念出来」的典型页：
// 三个文本块跨越中轴线（layout 有信号），讲稿是口语直讲（script 缺结构）。
// 这一档才会被路由给模型（rules 0.667 / model 0.833），也是本文件唯一要测的分支。
func prefillJobPage() *project.Page {
	box := func(y int64) *project.Box {
		return &project.Box{X: 800000, Y: y, Width: 8000000, Height: 700000}
	}
	return &project.Page{
		Index:   2,
		SlideID: "slide-3",
		Shapes: []*project.Shape{
			{ID: "shape-title", Kind: "text", Text: "第三季度回顾", Bounds: &project.Box{X: 800000, Y: 600000, Width: 8000000, Height: 900000}},
			{ID: "shape-b1", Kind: "text", Text: "收入增长 18%", Bounds: box(2400000)},
			{ID: "shape-b2", Kind: "text", Text: "毛利率 42%", Bounds: box(3200000)},
		},
	}
}

func prefillJobSnapshot(t *testing.T) bind.Snapshot {
	t.Helper()
	return bind.Snapshot{
		SlideID:          "slide-3",
		Language:         "zh-CN",
		SourceRevisionNo: 1,
		Elements:         PageBindElements(prefillJobPage(), 0, 0),
		Segments: []bind.Segment{
			{ID: "seg-1", Text: "先看整体：收入增长 18%，这是本季度的主旋律。"},
			{ID: "seg-2", Text: "毛利率 42%，比上季度高一个点。"},
		},
	}
}

func prefillTestJob(t *testing.T, snap bind.Snapshot) *pipeline.Job {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return &pipeline.Job{
		ID:            "job-prefill-1",
		TenantID:      prefillTestTenant,
		ProjectID:     prefillTestProject,
		Kind:          pipeline.KindAnimationPrefill,
		InputSnapshot: string(raw),
	}
}

func prefillTestObjects(t *testing.T) objectstore.ObjectStore {
	t.Helper()
	return objectstore.NewLocal(t.TempDir(), []byte("test-secret"))
}

func mustResult(t *testing.T, objects objectstore.ObjectStore, hash string) *PrefillResult {
	t.Helper()
	res, ok, err := LoadPrefillResult(context.Background(), objects, prefillTestTenant, prefillTestProject, 1, hash)
	if err != nil {
		t.Fatalf("读结果出错: %v", err)
	}
	if !ok {
		t.Fatal("任务成功但没有结果对象：编辑器拿不到任何东西")
	}
	return res
}

// fakeCompleter 是最小的模型通道替身：回固定文本、记调用次数。
type fakeCompleter struct {
	text   string
	err    error
	calls  int
	prompt int64
	compl  int64
}

func (f *fakeCompleter) Complete(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
	f.calls++
	if f.err != nil {
		return bind.ModelCompletion{}, f.err
	}
	return bind.ModelCompletion{Text: f.text, PromptTokens: f.prompt, CompletionTokens: f.compl}, nil
}

// fakeAccountant 记录预占/结算是按什么额度做的。
type fakeAccountant struct {
	reserveErr  error
	reserved    float64
	settled     float64
	settleCalls int
}

func (f *fakeAccountant) Reserve(_ context.Context, _, _ string, _ usage.Kind, units float64) (*usage.Reservation, error) {
	if f.reserveErr != nil {
		return nil, f.reserveErr
	}
	f.reserved = units
	return &usage.Reservation{}, nil
}

func (f *fakeAccountant) Settle(_ context.Context, _, _ string, _ usage.Kind, actual float64, _ string) error {
	f.settleCalls++
	f.settled = actual
	return nil
}

func TestPrefillJobWritesRuleResultWithoutModel(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	h := NewAnimationPrefillHandler(objects)

	if err := h.Handle(context.Background(), prefillTestJob(t, snap)); err != nil {
		t.Fatalf("没有模型时任务不该失败：%v", err)
	}
	res := mustResult(t, objects, snap.ContentHash())
	if res.Route != string(bind.RouteRule) {
		t.Errorf("route = %q，期望 rule（没配模型只能走规则）", res.Route)
	}
	if res.Model.Fallback != bind.ModelFallbackNoModel {
		t.Errorf("model.fallback = %q，期望 %q", res.Model.Fallback, bind.ModelFallbackNoModel)
	}
	if len(res.Bindings) == 0 {
		t.Error("规则一条都没绑上：结果对象存在但没有内容，等于骗调用方「算过了」")
	}
}

func TestPrefillJobUsesModelOnScriptBand(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	model := &fakeCompleter{text: `{"bindings":[{"elementId":"shape-b1","segmentId":"seg-1","confidence":0.9},{"elementId":"shape-b2","segmentId":"seg-2","confidence":0.8}]}`}
	h := NewAnimationPrefillHandler(objects).WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) {
		return model, nil
	})

	if err := h.Handle(context.Background(), prefillTestJob(t, snap)); err != nil {
		t.Fatalf("任务失败：%v", err)
	}
	if model.calls != 1 {
		t.Fatalf("模型调用次数 = %d，期望 1（script 档必须问一次）", model.calls)
	}
	res := mustResult(t, objects, snap.ContentHash())
	if res.Route != string(bind.RouteModel) {
		t.Fatalf("route = %q，期望 model（模型产出了可用结果，不能写回 rule）", res.Route)
	}
	if res.Model.Fallback != "" {
		t.Errorf("model.fallback = %q，期望空（模型正常产出）", res.Model.Fallback)
	}
	if res.Model.Accepted != 2 {
		t.Errorf("accepted = %d，期望 2", res.Model.Accepted)
	}
}

// 模型超时不等于任务失败：规则结果必须照常落盘，并把原因记成 timeout。
func TestPrefillJobFallsBackOnModelTimeout(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	model := &fakeCompleter{err: errors.New("context deadline exceeded")}
	h := NewAnimationPrefillHandler(objects).WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) {
		return model, nil
	})

	if err := h.Handle(context.Background(), prefillTestJob(t, snap)); err != nil {
		t.Fatalf("模型出错不该让任务失败：%v", err)
	}
	res := mustResult(t, objects, snap.ContentHash())
	if res.Route != string(bind.RouteRule) {
		t.Errorf("route = %q，期望 rule（模型没产出，退回规则）", res.Route)
	}
	if res.Model.Fallback != bind.ModelFallbackError {
		t.Errorf("model.fallback = %q，期望 error", res.Model.Fallback)
	}
	if len(res.Bindings) == 0 {
		t.Error("退回规则后一条都没绑上")
	}
}

// 额度不够：不发请求、任务成功、原因写 quota。
// 与 error 分开是刻意的——「发了但网关坏」和「没发，因为账上不够」是两种运维动作。
func TestPrefillJobSkipsModelWhenQuotaExhausted(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	model := &fakeCompleter{text: `{"bindings":[]}`}
	acct := &fakeAccountant{reserveErr: usage.ErrInsufficientQuota}
	h := NewAnimationPrefillHandler(objects).
		WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) { return model, nil }).
		WithTokenAccounting(acct)

	if err := h.Handle(context.Background(), prefillTestJob(t, snap)); err != nil {
		t.Fatalf("额度不足不该让任务失败：%v", err)
	}
	if model.calls != 0 {
		t.Fatalf("模型调用次数 = %d，期望 0（额度不够就不许发请求）", model.calls)
	}
	res := mustResult(t, objects, snap.ContentHash())
	if res.Model.Fallback != bind.ModelFallbackQuota {
		t.Fatalf("model.fallback = %q，期望 %q", res.Model.Fallback, bind.ModelFallbackQuota)
	}
}

// 结算必须用真实 token，不能用预占的估算值：估算只保证不超支，不能当账。
func TestPrefillJobSettlesRealTokenUsage(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	model := &fakeCompleter{
		text:   `{"bindings":[{"elementId":"shape-b1","segmentId":"seg-1","confidence":0.9}]}`,
		prompt: 700, compl: 42,
	}
	acct := &fakeAccountant{}
	h := NewAnimationPrefillHandler(objects).
		WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) { return model, nil }).
		WithTokenAccounting(acct)

	if err := h.Handle(context.Background(), prefillTestJob(t, snap)); err != nil {
		t.Fatalf("任务失败：%v", err)
	}
	if acct.reserved <= 0 {
		t.Fatal("没有预占额度：那会变成「先花钱后记账」")
	}
	if acct.settleCalls != 1 {
		t.Fatalf("结算次数 = %d，期望 1", acct.settleCalls)
	}
	if acct.settled != 742 {
		t.Fatalf("结算用量 = %v，期望 742（真实 prompt+completion，不是估算）", acct.settled)
	}
}

// 快照写坏了必须报错：按空内容跑会得到「一条都没绑」的成功结果，
// 那比失败更难查——看起来像「这页本来就没东西可绑」。
func TestPrefillJobRejectsBrokenSnapshot(t *testing.T) {
	h := NewAnimationPrefillHandler(prefillTestObjects(t))
	job := &pipeline.Job{ID: "j1", TenantID: prefillTestTenant, ProjectID: prefillTestProject, InputSnapshot: `{"slideId":"x"}`}
	if err := h.Handle(context.Background(), job); err == nil {
		t.Fatal("没有元素却执行成功了")
	}
}

// 结果键必须随内容变化：讲稿一改，旧结果就不该再被读到。
func TestPrefillContentHashTracksInput(t *testing.T) {
	base := prefillJobSnapshot(t)
	other := base
	other.Segments = []bind.Segment{{ID: "seg-1", Text: base.Segments[0].Text}}
	if base.ContentHash() == other.ContentHash() {
		t.Fatal("少一段讲稿却算出同一个内容指纹：旧结果会被当成新结果读出来")
	}
	// 预设不参与指纹：它只影响时间轴，不影响「哪个元素属于哪一段」。
	sameContent := base
	sameContent.PresetID = "business-calm"
	if base.ContentHash() != sameContent.ContentHash() {
		t.Fatal("换预设改变了内容指纹：会多出一次没必要的模型调用")
	}
}

// 超时口径：handler 的 timeout 覆盖必须真的传到调用上下文上。
func TestPrefillTimeoutIsApplied(t *testing.T) {
	objects := prefillTestObjects(t)
	snap := prefillJobSnapshot(t)
	var got time.Duration
	h := NewAnimationPrefillHandler(objects).
		WithTimeout(20 * time.Millisecond).
		WithCompleter(func(context.Context, string) (bind.ModelCompleter, error) {
			return &deadlineCompleter{onCtx: func(d time.Duration) { got = d }}, nil
		})
	_ = h.Handle(context.Background(), prefillTestJob(t, snap))
	if got <= 0 || got > 100*time.Millisecond {
		t.Fatalf("模型看到的超时 = %v，期望约 20ms", got)
	}
}

type deadlineCompleter struct {
	onCtx func(time.Duration)
}

func (d *deadlineCompleter) Complete(ctx context.Context, _ bind.ModelPrompt) (bind.ModelCompletion, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.onCtx(time.Until(dl))
	}
	return bind.ModelCompletion{}, ctx.Err()
}
