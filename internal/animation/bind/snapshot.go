package bind

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Snapshot 是一页预填的输入快照：绑定器需要的全部输入，按受理时刻的原样序列化。
//
// 为什么把元素与分段塞进任务快照，而不是让 worker 按 slideID 自己重新解析：
// worker 侧要重新解析就得把 project / objects / narration 三套 resolver 全搬进任务进程，
// 更要命的是解析产物在排队期间可能被新版本覆盖——那时 worker 绑的是**另一版页面**的元素，
// 结果却落到这一页上，就是一片看得见但查不出的错绑。快照把「这次预填针对哪一版内容」
// 钉死在受理时刻，任务重试也只会得到同一份输入。
type Snapshot struct {
	SlideID          string    `json:"slideId"`
	Language         string    `json:"language,omitempty"`
	SourceRevisionNo int       `json:"sourceRevisionNo,omitempty"`
	PresetID         string    `json:"presetId,omitempty"`
	Elements         []Element `json:"elements"`
	Segments         []Segment `json:"segments"`
}

// ContentHash 是「这一页此时此刻的内容」指纹，也是预填结果的对象键。
//
// 只算元素与分段，不算 PresetID / 语言 / 源版本：预填的产出（哪个元素属于哪一段）
// 只由这两样决定。把预设算进去会让「换个预设」变成一次没必要的模型调用，
// 而那一步只影响时间轴，不影响绑定关系。
//
// 顺序参与计算：元素顺序用的是形状树顺序（z-order），换序会改变阅读序重排的结果，
// 所以它确实是输入的一部分，不能排序后再哈希。
func (s Snapshot) ContentHash() string {
	var b strings.Builder
	b.Grow(64 * (len(s.Elements) + len(s.Segments)))
	for _, e := range s.Elements {
		// 分隔符用 \x00 / \x01：正常文本里不会出现，避免 "ab"+"c" 与 "a"+"bc" 撞在一起。
		fmt.Fprintf(&b, "e\x00%s\x00%s\x00%g\x00%g\x00%g\x00%g\x00%s\x01",
			e.ID, e.Kind, e.Box.X, e.Box.Y, e.Box.W, e.Box.H, e.Text)
	}
	for _, g := range s.Segments {
		fmt.Fprintf(&b, "s\x00%s\x00%s\x01", g.ID, g.Text)
	}
	sum := sha256.Sum256([]byte(b.String()))
	// 取前 16 个十六进制字符（64 bit）：它只用来分桶，不承担防篡改。
	// 更长的键在对象存储路径里只会让「这一页的结果在哪」更难一眼认出来。
	return hex.EncodeToString(sum[:])[:16]
}
