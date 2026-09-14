package pronunciation

import "strings"

// Apply 按规则顺序对 text 逐条执行字面量替换。仅替换 enabled=true 的规则；
// pattern 为空时跳过。返回替换后的文本。
func Apply(text string, rules Rules) string {
	for _, rule := range rules {
		if !rule.Enabled || rule.Pattern == "" {
			continue
		}
		text = strings.ReplaceAll(text, rule.Pattern, rule.Replacement)
	}
	return text
}
