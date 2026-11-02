package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/render"
)

// TestFailBudgetMapsRendererTimeoutToGatewayTimeout 守「超时不能被报成上游故障」这条线。
//
// render.Wait 有自己的 deadline 计时器，调用方的 ctx 还有一个，两者几乎同时到期，
// 谁先触发取决于调度。只按 ctx.Err() 判定时，Wait 先返回就落进 502 preview_timeout：
// 用户看到的指引是「去查渲染服务」，而真实原因只是这一页渲得太慢，该做的是稍后重试。
// 2026-11-02 的 CI 就因此在 TestAnimationPreviewTimeoutCancelsJob 上红过一次。
func TestFailBudgetMapsRendererTimeoutToGatewayTimeout(t *testing.T) {
	rec := httptest.NewRecorder()
	// ctx 明确未过期，只有 err 携带超时信号——这正是竞态里 Wait 先返回的那一侧。
	(&AnimationPreviewer{}).failBudget(context.Background(), rec,
		"preview_timeout", fmt.Errorf("render: job job-1 %w after 300ms", render.ErrTimeout))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("渲染超时应返回 504（ctx 未过期也一样），实际 %d：%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "preview_timeout") {
		t.Fatalf("响应体应带 preview_timeout，实际：%s", body)
	}
}

// TestFailBudgetKeepsBadGatewayForOtherErrors 守反面：这条不能顺手放宽。
// 把真实故障（连不上、返回 500）也报成「超时」，用户会一直重试而永远不查服务。
func TestFailBudgetKeepsBadGatewayForOtherErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	(&AnimationPreviewer{}).failBudget(context.Background(), rec,
		"renderer_unavailable", errors.New("dial tcp 127.0.0.1:8090: connect: connection refused"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("非超时错误应保持 502，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestFailedCtxWithoutTimeoutErrorStillTimesOut 守另一侧竞态：
// ctx 已过期但给到的 err 不是 render.ErrTimeout（例如 Submit 阶段就到期）。
func TestFailedCtxWithoutTimeoutErrorStillTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	rec := httptest.NewRecorder()
	(&AnimationPreviewer{}).failBudget(ctx, rec,
		"renderer_unavailable", errors.New("submit interrupted"))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("ctx 已过期时应返回 504，实际 %d：%s", rec.Code, rec.Body.String())
	}
}
