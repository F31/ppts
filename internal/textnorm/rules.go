package textnorm

// Dictionary 是词典缝。用 Substitute 而非 Lookup(key)：
// ppts 既有 pronunciation.Apply 是"逐条 strings.ReplaceAll 替换整个 span"的语义，
// 单 key 查表无法复刻 replace-all 输出，而黄金对比测试②要求 Run 输出与 Apply 逐字节一致。
// 故窄接口定为一次 span 级替换，由 ppts 适配器用与 Apply 完全一致的实现提供。
type Dictionary interface {
	// Substitute 对整段 span 应用词典替换（语义同 pronunciation.Apply 的逐条 ReplaceAll），
	// 返回 (是否发生替换, 替换后文本)。
	Substitute(lang, text string) (bool, string)
}

// DefinitionRule 构造"定义权词典"规则。调用方注册时给 priority 10-19。
// 只作用于普通 span（Reading/Pause span 由 Scanner 短路，不会进入规则表）。
func DefinitionRule(dict Dictionary) Rule {
	if dict == nil {
		return func(*Context) (string, bool) { return "", false }
	}
	return func(ctx *Context) (string, bool) {
		ok, out := dict.Substitute(ctx.Lang, ctx.Text)
		if !ok {
			return "", false
		}
		return out, true
	}
}
