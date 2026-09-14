// Package validation contains deterministic guards for AI-generated narration text.
package validation

import (
	"regexp"
	"sort"
	"strings"
)

var (
	// 数字 + 可选单位：覆盖百分比、金额、容量、频率、时间等常见演示文案实体。
	numberUnitRe = regexp.MustCompile(`(?i)(?:\b\d+(?:\.\d+)?(?:%|pct|bps|x|ms|s|min|h|hz|khz|mhz|ghz|tb|gb|mb|kb|b|w|kw|mw|v|a|usd|rmb|cny|元|万元|亿元|秒|分钟|小时|倍)?\b|\d+(?:\.\d+)?(?:%|元|万元|亿元|秒|分钟|小时|倍))`)
	dateRe       = regexp.MustCompile(`\b\d{4}[-/.年]\d{1,2}[-/.月]\d{1,2}日?\b|\b\d{4}[-/.年]\d{1,2}月?\b`)
	modelRe      = regexp.MustCompile(`\b[A-Za-z]+[A-Za-z0-9._-]*\d[A-Za-z0-9._-]*\b`)
)

// Entity is a source fact that AI text must preserve unless explicitly approved.
type Entity struct {
	Text string
	Kind string // number | date | model
}

// Report is the preservation result for source-vs-target text.
type Report struct {
	Source   []Entity
	Missing  []Entity
	Inserted []Entity
}

func (r Report) OK() bool { return len(r.Missing) == 0 && len(r.Inserted) == 0 }

// ExtractEntities returns normalized numeric/date/model entities in first-seen order.
func ExtractEntities(text string) []Entity {
	seen := map[string]bool{}
	out := []Entity{}
	add := func(kind, raw string) {
		v := normalizeEntity(raw)
		if v == "" {
			return
		}
		key := kind + ":" + v
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Entity{Text: v, Kind: kind})
	}
	for _, m := range dateRe.FindAllString(text, -1) {
		add("date", m)
	}
	for _, m := range numberUnitRe.FindAllString(text, -1) {
		add("number", m)
	}
	for _, m := range modelRe.FindAllString(text, -1) {
		add("model", m)
	}
	return out
}

// CheckPreserved ensures the target did not drop source entities and did not add new ones.
func CheckPreserved(source, target string) Report {
	src := ExtractEntities(source)
	dst := ExtractEntities(target)
	dstSet := entitySet(dst)
	srcSet := entitySet(src)
	missing := make([]Entity, 0)
	inserted := make([]Entity, 0)
	for _, e := range src {
		if !dstSet[e.Kind+":"+e.Text] {
			missing = append(missing, e)
		}
	}
	for _, e := range dst {
		if !srcSet[e.Kind+":"+e.Text] {
			inserted = append(inserted, e)
		}
	}
	return Report{Source: src, Missing: missing, Inserted: inserted}
}

func entitySet(in []Entity) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, e := range in {
		out[e.Kind+":"+e.Text] = true
	}
	return out
}

func normalizeEntity(raw string) string {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, "，。；：、,.!?:;()（）[]【】\"'")
	v = strings.ToLower(v)
	v = strings.ReplaceAll(v, "年", "-")
	v = strings.ReplaceAll(v, "月", "-")
	v = strings.TrimSuffix(v, "日")
	v = strings.TrimSuffix(v, "-")
	return v
}

// FormatEntities returns stable strings for prompts and tests.
func FormatEntities(in []Entity) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		out = append(out, e.Kind+":"+e.Text)
	}
	sort.Strings(out)
	return out
}
