package app

import (
	"context"
	"errors"
	"time"

	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
)

// 动画渲染额度的结算侧（M4 §7.5）。
//
// 预占在 API（受理时），结算在 worker（渲完之后）：只有这里知道"哪几页真的渲了动画"。
// 两者用同一个逻辑操作 id（任务的幂等键）与同一个账期，账本才能对上。

// WithUsage 注入账本，用于动画渲染分钟的结算。
//
// 未注入时（本地 profile、单测）不做任何结算：额度是"成本闸"，不该成为
// 导出路径的必需依赖——账本连不上时导出仍必须出片。
func (h *ExportHandler) WithUsage(u UsageSettler) *ExportHandler {
	h.usage = u
	return h
}

// settleAnimationMinutes 以**实际渲了动画的时长**结算本次导出。
//
// 三个刻意的选择：
//
//   - 降级页不计费：降级页播的是静态段，没花渲染分钟数。按整片时长结算，
//     等于在渲染服务最不稳（降级最多）的时候按满额收费。
//   - av == nil（整体退回静态）结算 0 而不是不结算：预占必须有个终结动作，
//     否则一次渲染失败就把额度永久锁死到账期结束。
//   - 结算失败不让导出失败：成片已经做出来了，钱的事由日志与对账兜。
func (h *ExportHandler) settleAnimationMinutes(ctx context.Context, job *pipeline.Job, pageDurationsMS []int64, av *AnimatedVideo) {
	if h.usage == nil || job == nil || job.IDempotencyKey == "" {
		return
	}
	var animatedMS int64
	// av == nil 是"整体退回静态"：一页动画都没有，计 0。
	// 这个分支必须显式写出来——把条件写成「av != nil && 降级页跳过」时，
	// av==nil 会让循环把整片时长都算进动画分钟数，于是"渲染失败"被按满额收费
	// （用例 TestSettleAnimationMinutesReleasesHoldWhenFullyStatic 守这一条）。
	if av != nil {
		for i, d := range pageDurationsMS {
			if d <= 0 {
				continue
			}
			if containsInt(av.Degraded, i) {
				continue
			}
			animatedMS += d
		}
	}
	// 账期取**任务创建时间**而不是结算时刻：跨月运行的任务（月末提交、月初跑完）
	// 必须结算到受理时的那个账期，否则找不到预占，这笔用量就成了无主账。
	created := job.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	kind := usage.KindAnimationMinutes.Period(usage.MonthPeriod(created))
	minutes := float64(animatedMS) / 60000.0
	if err := h.usage.Settle(ctx, job.TenantID, job.IDempotencyKey, kind, minutes, ""); err != nil {
		if errors.Is(err, usage.ErrReservationNotFound) {
			// 没有预占：额度未启用，或受理时时间轴读不出来跳过了预占。
			return
		}
		if h.logger != nil {
			h.logger.Warn("animation: settle render minutes failed",
				"job_id", job.ID, "tenant_id", job.TenantID, "kind", string(kind),
				"minutes", minutes, "error", err)
		}
		return
	}
	if h.logger != nil && animatedMS > 0 {
		h.logger.Info("animation: settled render minutes",
			"job_id", job.ID, "kind", string(kind), "minutes", minutes,
			"degraded_pages", len(degradedPages(av)))
	}
}

func degradedPages(av *AnimatedVideo) []int {
	if av == nil {
		return nil
	}
	return av.Degraded
}
