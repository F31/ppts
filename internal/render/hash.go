package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
)

// HashOptions 是参与 planHash 但不属于计划本体的环境量。
// 渲染器版本或字体版本变了，同一份计划必须重渲——它在缓存的语义里就是"不同的产物"。
type HashOptions struct {
	RendererVersion string
}

// Hash 返回计划的规范化 SHA-256（64 位十六进制）。
//
// 规范化做两件事：
//  1. 去掉 Warnings —— 诊断不影响像素，否则同一份规格在不同数据状态下会算出不同 hash；
//  2. 把 BaseImage 归一为 basename —— 同一份规格在不同租户/项目前缀下应命中同一份缓存。
//
// 不 hash 整个原始 JSON 还有第三个理由：json.Marshal 对结构体字段按声明顺序输出、
// 对 map 按键排序，本身就是确定性的；用规范化副本可以避免以后加字段时无意改变语义。
func (t *Timeline) Hash(opt HashOptions) (string, error) {
	canon, err := t.CanonicalJSON()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(opt.RendererVersion))
	h.Write([]byte{0})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CanonicalJSON 返回用于 hash 的规范化字节。
func (t *Timeline) CanonicalJSON() ([]byte, error) {
	c := *t
	c.Warnings = nil
	c.Pages = make([]Page, len(t.Pages))
	for i, p := range t.Pages {
		p.BaseImage = path.Base(p.BaseImage)
		// Layers 复制成非 nil 切片，避免 nil 与 [] 在 JSON 里一个输出 null、一个输出 []。
		p.Layers = append([]Layer{}, t.Pages[i].Layers...)
		if len(p.Layers) == 0 {
			p.Layers = nil
		}
		c.Pages[i] = p
	}
	if len(c.Fonts) == 0 {
		c.Fonts = nil
	}
	return json.Marshal(&c)
}

// ShortID 把 hash 前 24 位十六进制作为 job id：够短可读，碰撞概率可忽略（96 位）。
func ShortID(hash string) string {
	if len(hash) <= 24 {
		return hash
	}
	return hash[:24]
}
