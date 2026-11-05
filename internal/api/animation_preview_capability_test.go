package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
)

// 这一组用例守的是本轮缺陷的根因：预览依赖不齐时**不再静默不注册路由**。
//
// 此前 POST 落到 404 纯文本，前端的错误映射里没有 http_404，于是用户只看到
// 一句"预览失败"；既不知道这个部署压根没开预览，也不知道该找谁开。
// 现在能力探测和 POST 失败必须给出同一个原因码，且都必须是 JSON。

func previewCapabilityMux(p *AnimationPreviewer) http.Handler {
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	p.Register(mux, auth)
	return mux
}

func TestPreviewCapabilityReportsUnconfiguredReason(t *testing.T) {
	ts := httptest.NewServer(previewCapabilityMux(
		NewAnimationPreviewerUnavailable(PreviewReasonRendererMissing, 0)))
	defer ts.Close()

	resp, err := http.Get(ts.URL + animationURL() + "/preview/capability")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("能力探测应始终 200，实际 %d", resp.StatusCode)
	}
	var got previewCapability
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("响应必须是 JSON：%v", err)
	}
	if got.Configured {
		t.Fatal("未配置渲染服务时 configured 必须为 false")
	}
	if got.Reason != PreviewReasonRendererMissing {
		t.Fatalf("reason=%q，期望 %q", got.Reason, PreviewReasonRendererMissing)
	}
	if got.BudgetMs != previewBudget.Milliseconds() {
		t.Fatalf("budgetMs=%d，期望与服务端预算一致 %d", got.BudgetMs, previewBudget.Milliseconds())
	}
	if !strings.Contains(got.Message, "PPTS_RENDERER_URL") {
		t.Fatalf("message 应指出缺失的配置项，实际 %q", got.Message)
	}
}

func TestPreviewCapabilityEchoesConfiguredBudget(t *testing.T) {
	// 能力端点给出的预算必须就是提交时用的那个数：两边不一致会让用户
	// 按 3s 理解一个实际 12s 的等待（或反过来），提示就失去了意义。
	p := &AnimationPreviewer{}
	p.WithBudget(12 * time.Second)
	rec := httptest.NewRecorder()
	// 绕过中间件：能力端点只要求上下文里有 principal。
	p.capability(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/capability", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got previewCapability
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Configured || got.Reason != PreviewReasonOK {
		t.Fatalf("已配置的预览器应报告 ok，实际 %+v", got)
	}
	if got.BudgetMs != (12 * time.Second).Milliseconds() {
		t.Fatalf("budgetMs=%d，期望 12000", got.BudgetMs)
	}
	if got.Message != "" {
		t.Fatalf("可用时不应带「未启用」说明，实际 %q", got.Message)
	}
}

func TestPreviewUnavailableReturnsActionableJSON(t *testing.T) {
	ts := httptest.NewServer(previewCapabilityMux(
		NewAnimationPreviewerUnavailable(PreviewReasonAssetDirMissing, 0)))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+animationURL()+"/preview",
		strings.NewReader(`{"timelineKey":"timeline-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用时应返回 503，实际 %d（404 会让前端显示「预览失败」）", resp.StatusCode)
	}
	var body struct {
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("503 必须是 JSON 信封：%v", err)
	}
	if body.Code != "preview_unavailable" || body.Reason != PreviewReasonAssetDirMissing {
		t.Fatalf("信封不符：%+v", body)
	}
	if !strings.Contains(body.Message, "PPTS_ANIMATION_ASSET_DIR") {
		t.Fatalf("message 应指出缺失的配置项，实际 %q", body.Message)
	}
}

func TestPreviewUnavailableMessageCoversEveryReason(t *testing.T) {
	// 前端按 reason 本地化，服务端这句是兜底与日志用的：任何新原因都至少要
	// 能读出配置项，不能落到一句"未知错误"让人无从下手。
	for _, reason := range []string{
		PreviewReasonDisabled, PreviewReasonRendererMissing, PreviewReasonAssetDirMissing,
		PreviewReasonRendererInvalid, PreviewReasonAssetDirInvalid, "something_new",
	} {
		if msg := previewUnavailableMessage(reason); !strings.Contains(msg, "单页预览未启用") ||
			(reason != PreviewReasonOK && msg == "") {
			t.Fatalf("reason=%q 的说明不可行动：%q", reason, msg)
		}
	}
}

// withPrincipal 给直接调用 handler 的测试塞入身份，避免为了一个只读端点
// 再走一遍完整认证配置。
func withPrincipal(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{},
		Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID}))
}
