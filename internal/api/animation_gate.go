package api

import (
	"context"
	"errors"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/tenant"
)

// defaultRolloutPercent 是「租户没配过」时的默认灰度百分比。
//
// 100 不是随手定的：它等于"部署闸开 = 所有租户可用"，也就是引入租户策略之前的线上行为。
// 改成 0 会让升级瞬间关掉所有客户的动画（design ADR-3）。
const defaultRolloutPercent = 100

func rolloutPercentOrDefault(p *int) int {
	if p == nil {
		return defaultRolloutPercent
	}
	return *p
}

// AnimationGate 回答一个问题：这个租户此刻能不能用动画。
//
// 抽成接口而不是让 handler 直接读 tenant.Policy：一是写路径、导出选项、导出入口都要问同一个问题，
// 必须得到同一个答案；二是测试不必造一张租户表。
type AnimationGate interface {
	Resolve(ctx context.Context, tenantID string) animation.Capability
}

// deployGate 是引入租户策略之前的判定：部署开 = 所有租户开。
//
// 保留它有两个用途：本地 profile 与既有单测继续用「一个 bool」表达能力；
// 以及给 `NewAnimationHandler(store, pages, enabled)` 这个老构造一个不改变语义的实现。
type deployGate struct{ enabled bool }

func (g deployGate) Resolve(context.Context, string) animation.Capability {
	if !g.enabled {
		return animation.Capability{Bucket: -1, Source: animation.SourceDeploy, Reason: animation.ReasonDeployDisabled}
	}
	return animation.Capability{Enabled: true, Preview: true, Bucket: -1, Source: animation.SourceDeploy}
}

// tenantAnimationGate 把「取策略」这层 I/O 包在纯函数外面。
type tenantAnimationGate struct {
	policies TenantPolicyReader
	cfg      animation.GateConfig
}

func newTenantAnimationGate(policies TenantPolicyReader, cfg animation.GateConfig) AnimationGate {
	return &tenantAnimationGate{policies: policies, cfg: cfg}
}

func (g *tenantAnimationGate) Resolve(ctx context.Context, tenantID string) animation.Capability {
	var policy animation.AnimationPolicy
	if g.policies != nil {
		p, err := g.policies.GetPolicy(ctx, tenantID)
		switch {
		case err == nil && p != nil && p.Animation != nil:
			policy = *p.Animation
		case errors.Is(err, tenant.ErrTenantNotFound):
			// 本地 profile、新建租户、以及还没写过策略的租户都会走到这里：
			// 「没有策略行」是「没配过」，不是「读失败」，所以继续走默认百分比。
		case err != nil:
			// fail closed：动画是花钱的能力，策略读不到时放开等于在最脆弱的时刻放开成本闸。
			// 原因单独给一个值，否则「数据库挂了」会显示成「你的租户没开这个功能」。
			return animation.Capability{Bucket: -1, Source: animation.SourcePolicy, Reason: animation.ReasonPolicyUnreadable}
		}
	}
	return policy.Resolve(tenantID, g.cfg)
}
