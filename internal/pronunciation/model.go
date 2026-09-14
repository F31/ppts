package pronunciation

import "encoding/json"

// Rule 定义一条发音替换规则。pattern 为字面量（大小写敏感），
// replacement 为期望 TTS 发音的文本。enabled=false 时跳过。
type Rule struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Enabled     bool   `json:"enabled"`
}

// Rules 是 JSON 序列化的规则集合，存储在 pronunciation_dictionaries.rules 列。
type Rules []Rule

// ParseRules 从原始 JSON bytes 解析规则集；非法 JSON 降级为空集。
func ParseRules(raw json.RawMessage) Rules {
	if len(raw) == 0 {
		return nil
	}
	var rules Rules
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil
	}
	return rules
}

// Marshal 序列化规则集为 JSON bytes。
func (r Rules) Marshal() (json.RawMessage, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
