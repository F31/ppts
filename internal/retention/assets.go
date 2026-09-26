package retention

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件是「对象类型 → 保留/回收规则」的**单一来源**。
//
// 背景（架构审视 §3.3）：object_inventory.asset_type 实际有 15 种取值，而派生清理
// （DerivedToDelete）此前只硬编码了 artifact/audio/render 三种，其余 12 种**永不回收**；
// 时间轴/字幕键含 job.ID（app/narration.go:614），每次重生成都留下一批新对象。
//
// 规则必须集中在一处的原因：此前三种类型的 SQL 条件被复制三份，新增第四种要同时改三处，
// 漏改的那一处就静默不生效（与 pipeline 列清单曾踩的坑同源）。现在 SQL 由本表生成。

// derivedRetentionField 是未单独设档的对象类型共用的兜底保留天数键。
// tenants.policy 是 jsonb，新增键无需迁移；缺省为 0 表示不自动过期。
const derivedRetentionField = "derived_retention_days"

// ownerCheck 描述「库内是否仍有引用」的判定方式。
type ownerCheck struct {
	// Table 是归属表名。
	Table string
	// Column 是归属表里存放对象键的列名。
	Column string
	// InventoryColumn 是 object_inventory 里与之比对列（通常就是 object_key）。
	InventoryColumn string
}

// assetSpec 描述一种对象类型的保留与孤儿判定规则。
type assetSpec struct {
	// Name 对应 object_inventory.asset_type。
	Name string
	// PolicyField 是 tenants.policy 中该类型专属的保留天数键；
	// 为空表示无专属档位，回退到 derivedRetentionField。
	PolicyField string
	// Expires 表示该类型是否参与「按龄过期」。源文件、上传临时件、审计归档、
	// 租户导出各有自己的生命周期，绝不能按龄删（审计更是合规数据）。
	Expires bool
	// Owner 非空时，该类型可参与孤儿扫描：归属表里找不到引用即为孤儿。
	// 为 nil 表示「不知道怎么判引用」——一律不判孤儿（宁可留着，不可错删）。
	Owner *ownerCheck
	// Note 记录为何这样定档，供后来者复核。
	Note string
}

// assetRegistry 是全量类型表。**新增 asset_type 必须登记在此**，
// TestAssetRegistryCoversCodebaseTypes 会扫描源码里的 AssetType 字面量强制这一点。
var assetRegistry = []assetSpec{
	// —— 有专属保留档位的三种（沿用既有策略字段）——
	{Name: "artifact", PolicyField: "artifact_retention_days", Expires: true,
		Owner: &ownerCheck{Table: "artifacts", Column: "object_key", InventoryColumn: "object_key"},
		Note:  "导出成品；内容为寻址，成品行被删后对象即无主"},
	{Name: "audio", PolicyField: "audio_retention_days", Expires: true,
		Note: "配音音频，按 configHash 寻址"},
	{Name: "render", PolicyField: "render_retention_days", Expires: true,
		Note: "页面渲染图与 pages 清单"},

	// —— 此前完全没有回收通道的派生缓存，统一走兜底档位 ——
	{Name: "timeline", Expires: true, Note: "时间轴 bundle；键含 job.ID，重生成必留新对象（§3.3 点名的泄漏）"},
	{Name: "subtitle", Expires: true, Note: "srt/vtt 字幕，同时间轴按 job.ID 寻址"},
	{Name: "alignment", Expires: true, Note: "对齐 manifest"},
	{Name: "segments", Expires: true, Note: "分段结果缓存"},
	{Name: "synth", Expires: true, Note: "合成统计缓存"},
	{Name: "document", Expires: true, Note: "解析出的文档 JSON（extracted）"},
	{Name: "notes", Expires: true, Note: "幻灯片备注 JSON"},
	{Name: "scriptdraft", Expires: true, Note: "一键成稿缓存"},

	// —— 有自己的生命周期，禁止按龄过期 ——
	{Name: "source", Expires: false,
		Owner: &ownerCheck{Table: "source_revisions", Column: "object_key", InventoryColumn: "object_key"},
		Note:  "源文件版本由 source_retention_days / delete_source_after 单独治理"},
	{Name: "work", Expires: false,
		Owner: &ownerCheck{Table: "uploads", Column: "object_key", InventoryColumn: "object_key"},
		Note:  "上传临时件，由 pending 超时中止流程治理"},
	{Name: "audit", Expires: false, Note: "审计归档，合规数据，永不自动删"},
	{Name: "tenant", Expires: false, Note: "租户导出件，由导出流程治理"},
}

func assetSpecOf(assetType string) (assetSpec, bool) {
	for _, s := range assetRegistry {
		if s.Name == assetType {
			return s, true
		}
	}
	return assetSpec{}, false
}

// AssetTypes 返回已登记的全部对象类型（稳定升序）。
func AssetTypes() []string {
	out := make([]string, 0, len(assetRegistry))
	for _, s := range assetRegistry {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// retentionFieldOf 返回该类型实际使用的保留天数键；未登记的类型返回空（不参与过期）。
func retentionFieldOf(assetType string) string {
	s, ok := assetSpecOf(assetType)
	if !ok || !s.Expires {
		return ""
	}
	if s.PolicyField != "" {
		return s.PolicyField
	}
	return derivedRetentionField
}

// derivedExpiryClause 生成 DerivedToDelete 的 WHERE 条件（全部可过期类型的 OR 串联）。
//
// 三段式条件与原先手写版本语义一致：档位必须 > 0（0 表示租户未设档，不是「立即过期」——
// 把 0 当成立即删除会删掉全部历史数据），且对象更新时间早于 now - N 天。
func derivedExpiryClause(nowExpr string) string {
	parts := make([]string, 0, len(assetRegistry))
	for _, s := range assetRegistry {
		field := retentionFieldOf(s.Name)
		if field == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			`(oi.asset_type = '%s' AND (t.policy->>'%s')::int > 0`+
				` AND oi.updated_at < %s::timestamptz - ((t.policy->>'%s')::int || ' days')::interval)`,
			s.Name, field, nowExpr, field))
	}
	if len(parts) == 0 {
		return "FALSE"
	}
	return strings.Join(parts, "\n\t\t\t     OR ")
}

// orphanClause 生成孤儿判定的 WHERE 条件。
//
// 只有登记了 Owner 的类型参与：判定依据是「归属表里没有对应的引用行」。
// 未登记 Owner 的类型一律不参与 —— 猜错引用关系会删掉在用对象，代价远大于留着几个孤儿。
func orphanClause(cutoffExpr string) string {
	parts := make([]string, 0, len(assetRegistry))
	for _, s := range assetRegistry {
		if s.Owner == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf(
			`(oi.asset_type = '%s' AND NOT EXISTS (`+
				`SELECT 1 FROM %s own WHERE own.tenant_id = oi.tenant_id`+
				` AND own.%s = oi.%s))`,
			s.Name, s.Owner.Table, s.Owner.Column, s.Owner.InventoryColumn))
	}
	if len(parts) == 0 {
		return "FALSE"
	}
	// 静默期：刚写入清单、归属行尚未提交的对象不能判为孤儿，否则会在
	// 「写对象 → 落清单 → 提交业务行」的窗口里误删在途产物。
	return "oi.updated_at < " + cutoffExpr + "\n\t\t\t   AND (" + strings.Join(parts, "\n\t\t\t     OR ") + ")"
}
