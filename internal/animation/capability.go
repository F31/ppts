package animation

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

// 动画能力的开放判定（M4）。
//
// 这里的核心不是"算出一个 bool"，而是**三处入口必须得到同一个答案**：导出选项说能不能用、
// 写规格的 PUT 放不放行、导出入口勾不勾动画。三处各读一次 env、各解释一次策略，迟早漂移成
// 「面板能打开但保存 404」——M3 已经踩过一次同类分叉（api 与 worker 两套开关判定）。
//
// 因此 Resolve 是一个无 I/O 的纯函数：策略从哪来、部署闸怎么配，全部由调用方作为入参给进来。
// 取策略的 I/O 由 api 层包掉（见 internal/api/animation_gate.go）。

// 能力来源。用来回答"为什么可用/不可用"，也是运营排查的入口。
const (
	// SourceDeploy 表示结论来自部署闸（PPTS_ANIMATION_ENABLED）。
	SourceDeploy = "deploy"
	// SourceTenant 表示结论来自租户策略里的显式 enabled。
	SourceTenant = "tenant"
	// SourceRollout 表示结论来自租户级灰度百分比。
	SourceRollout = "rollout"
	// SourceDefault 表示租户没配过，结论来自部署级默认百分比。
	SourceDefault = "default"
	// SourcePolicy 表示策略没能读出来（不是"没配过"，是"读不到"）。
	SourcePolicy = "policy"
)

// 关闭原因。空串表示可用。
const (
	// ReasonDeployDisabled：部署没开动画通道。
	ReasonDeployDisabled = "deploy_disabled"
	// ReasonTenantDisabled：租户被显式关闭（封禁、欠费、主动停用）。
	ReasonTenantDisabled = "tenant_disabled"
	// ReasonRolloutMiss：租户没进灰度桶。
	ReasonRolloutMiss = "rollout_miss"
	// ReasonDefaultOff：租户没配过，且部署默认百分比为 0。
	ReasonDefaultOff = "default_off"
	// ReasonUnknownTenant：拿不到租户 ID，无法判定。
	ReasonUnknownTenant = "unknown_tenant"
	// ReasonPolicyUnreadable：策略存储读失败（区别于"没配过"）。
	ReasonPolicyUnreadable = "policy_unreadable"
)

// AnimationPolicy 是租户级开放策略，持久化在 tenants.policy 的 "animation" 键下。
//
// 布尔字段用指针，因为 nil 与 false 是两种语义：
//
//	nil   —— 没配过，走灰度
//	true  —— 显式开放（白名单），绕过灰度
//	false —— 显式关闭（封禁），压过灰度
//
// 用零值 bool 会把"封禁一个租户"与"该租户还没进灰度"合并。后果很具体：运营把欠费租户设为
// false，灰度扩到 100% 时它又被打开了。
type AnimationPolicy struct {
	// Enabled 是总开关。nil 表示未设置。
	Enabled *bool `json:"enabled,omitempty"`
	// TrackEditor 控制元素级轨道编辑（角色/套餐位）。nil 视为 false。
	TrackEditor *bool `json:"track_editor,omitempty"`
	// Preview 控制单页预览。nil 视为 true（随总开关）。
	Preview *bool `json:"preview,omitempty"`
	// MaxMinutes 是每月渲染分钟数上限。0 表示用系统默认。
	MaxMinutes float64 `json:"max_minutes,omitempty"`
	// RolloutPercent 是本租户参与灰度的百分比；nil 表示用部署级默认。
	// 显式 0 表示"本租户不参与灰度"，与 nil 不同。
	RolloutPercent *int `json:"rollout_percent,omitempty"`
}

// GateConfig 是判定所需的环境输入。
type GateConfig struct {
	// DeployEnabled 是部署闸：关闭时任何租户策略都不能开启能力。
	DeployEnabled bool
	// DefaultPercent 是租户未配置时使用的灰度百分比（0–100）。
	// 取值决定升级当天的线上行为，见 design ADR-3。
	DefaultPercent int
	// Salt 是分桶盐。不得随灰度批次轮换（ADR-4）。
	Salt string
}

// Capability 是某租户此刻的能力快照。
type Capability struct {
	Enabled     bool   `json:"enabled"`
	TrackEditor bool   `json:"trackEditor"`
	Preview     bool   `json:"preview"`
	Source      string `json:"source"`
	// Bucket 是租户所在灰度桶（0–99）；-1 表示未计算（租户 ID 缺失或未走到分桶）。
	Bucket int    `json:"bucket"`
	Reason string `json:"reason,omitempty"`
}

// Resolve 给出能力快照。优先级：部署闸 > 租户显式 > 租户灰度 > 部署默认。
//
// 为什么显式值优先于灰度（而不是反过来）：
//   - 显式 true 必须能在 0% 灰度期生效，否则"按租户开启"这件事在 M4 第一步就做不到；
//   - 显式 false 必须压过 100%，否则"封禁"在灰度铺开后自动失效。
func (p AnimationPolicy) Resolve(tenantID string, cfg GateConfig) Capability {
	c := Capability{Bucket: -1}

	// 没有租户 ID 就不分桶：空串会稳定落在某个桶里，等于给"未知租户"随机开门。
	if strings.TrimSpace(tenantID) == "" {
		c.Source = SourceDefault
		c.Reason = ReasonUnknownTenant
		return c
	}
	c.Bucket = Bucket(tenantID, cfg.Salt)

	if !cfg.DeployEnabled {
		c.Source = SourceDeploy
		c.Reason = ReasonDeployDisabled
		return c
	}

	switch {
	case p.Enabled != nil:
		c.Source = SourceTenant
		c.Enabled = *p.Enabled
		if !c.Enabled {
			c.Reason = ReasonTenantDisabled
		}
	case p.RolloutPercent != nil:
		c.Source = SourceRollout
		c.Enabled = c.Bucket < clampPercent(*p.RolloutPercent)
		if !c.Enabled {
			c.Reason = ReasonRolloutMiss
		}
	default:
		c.Source = SourceDefault
		c.Enabled = c.Bucket < clampPercent(cfg.DefaultPercent)
		if !c.Enabled {
			c.Reason = ReasonDefaultOff
		}
	}

	if c.Enabled {
		// 子能力不能越过总闸：总开关关着时 track_editor:true 没有意义。
		c.TrackEditor = boolValue(p.TrackEditor, false)
		c.Preview = boolValue(p.Preview, true)
	}
	return c
}

// Bucket 把租户映射到 [0,100) 的桶。
//
// 用 SHA-256 而不是 hash/fnv：租户 ID 多为有序短串（t_000123），小模数取模对雪崩性弱的哈希
// 更容易出现可见聚集。分桶要的是均匀，不是快——每个租户一次，开销可忽略。
//
// salt 与 tenantID 之间加 \x00 分隔：否则 salt="ab"+tenant="c" 与 salt="a"+tenant="bc" 会撞。
func Bucket(tenantID, salt string) int {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte{0})
	h.Write([]byte(tenantID))
	sum := h.Sum(nil)
	return int(binary.BigEndian.Uint64(sum[:8]) % 100)
}

func clampPercent(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
