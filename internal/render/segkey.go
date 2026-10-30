package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"

	"github.com/F31/ppts/internal/animation"
)

// 本文件定义「一页片段的缓存键」（M5）。它回答一个问题：
// **哪些输入变了，这一页的像素就会变？**
//
// 键的粒度必须与产物粒度一致（一页一个 mp4），否则「改一页重渲一页」无从谈起。
// 键里少一个输入 → 命中旧画面（用户看到改了没变）；多一个无关输入 → 命中率虚低
// （每次都要重渲，缓存白建）。两种错都不报错，只能靠逐条推导入键字段来防。
//
// 与 Timeline.Hash（planHash）的关系：planHash 是**整片**的身份，用于任务幂等；
// 本文件是**单页**的身份，用于片段复用。两者共享同一套规范化纪律
// （basename、清派生字段、nil/空切片归一），但不共用函数——planHash 改动会改变
// 既有幂等语义，而片段键是新增的第二层，不该牵动第一层。

// SegmentEnv 是参与片段键、但不属于计划本体的环境量。
//
// 这些量变了，同一份计划会渲出不同的像素或不同的编码字节，因此必须进键。
// 默认值由各 backend 提供（见 SegmentEnvProvider），服务层不猜：
// 猜一个默认值等于给「忘了 bump 版本」发一张静默复用旧片段的许可证。
type SegmentEnv struct {
	// RendererVersion 是渲染服务版本（含合成器源码与依赖的语义版本）。
	RendererVersion string
	// Encoding 是编码参数摘要（如 `crf=18;gop=30`）。
	Encoding string
	// ChromeVersion 是浏览器版本（空表示不适用，例如 stub backend）。
	ChromeVersion string
}

// String 返回稳定的环境串。
//
// 用 JSON 而不是 `k=v;` 拼接：值里本来就可能有 `;`（编码参数摘要就是分号分隔的），
// 拼字符串时 `RendererVersion="v;encoding=x"` 与「两个字段」会撞成同一个串，
// 表现为「改了 CRF 却命中旧片段」。JSON 的引号与转义把边界写死了。
func (e SegmentEnv) String() string {
	payload, err := json.Marshal([]string{e.RendererVersion, e.Encoding, e.ChromeVersion})
	if err != nil {
		// 三个字符串不可能序列化失败；真失败也给出一个不会与其他值撞车的形状。
		return "env-error"
	}
	return string(payload)
}

// SegmentEnvProvider 由 backend 实现，用来声明「哪些环境量会改变产物」。
// 未实现的 backend（例如测试用的 stub）按零值处理——它本来就产不出真实像素。
type SegmentEnvProvider interface {
	SegmentEnv() SegmentEnv
}

// SegmentKeys 返回与 plan.Pages 同序的缓存键（64 位十六进制）。
func SegmentKeys(plan *Timeline, env SegmentEnv) ([]string, error) {
	if plan == nil {
		return nil, fmt.Errorf("render: segment keys need a plan")
	}
	out := make([]string, len(plan.Pages))
	for i := range plan.Pages {
		key, err := segmentKey(plan, i, env)
		if err != nil {
			return nil, err
		}
		out[i] = key
	}
	return out, nil
}

// segmentPage 是本页进入键的投影。
//
// 刻意不是 Page 本身：Page 里有 index / slideId / plateImage / layers[].image，
// 四个字段都不影响像素，却会在「换位置 / 换工作目录 / 重命名」时变化。
// 用独立结构而不是「复制 Page 再把字段清零」，是为了让以后给 Page 加字段时
// 必须显式决定它是否影响像素——默认不进键，而不是默认进键。
type segmentPage struct {
	DurationMs int64 `json:"durationMs"`
	// BaseImage 只取 basename：同一张图在不同租户/项目前缀下应命中同一份缓存
	// （与 Timeline.CanonicalJSON 同口径）。
	BaseImage  string      `json:"baseImage"`
	Layers     []Layer     `json:"layers"`
	Transition *Transition `json:"transition,omitempty"`
}

// segmentPrev 是「前一页末帧」进入键的投影。转场只画前一页的末帧，
// 因此前一页自己的 transition 不进键（它描述的是「如何进入前一页」）。
type segmentPrev struct {
	DurationMs int64   `json:"durationMs"`
	BaseImage  string  `json:"baseImage"`
	Layers     []Layer `json:"layers"`
}

// segmentInput 是键的完整载荷。字段顺序即序列化顺序。
type segmentInput struct {
	SchemaVersion string       `json:"schemaVersion"`
	FPS           int          `json:"fps"`
	Width         int          `json:"width"`
	Height        int          `json:"height"`
	Background    string       `json:"background,omitempty"`
	Fonts         []Font       `json:"fonts,omitempty"`
	Env           string       `json:"env"`
	Page          segmentPage  `json:"page"`
	Prev          *segmentPrev `json:"prev,omitempty"`
}

func segmentKey(plan *Timeline, i int, env SegmentEnv) (string, error) {
	page := plan.Pages[i]
	in := segmentInput{
		SchemaVersion: plan.SchemaVersion,
		FPS:           plan.FPS,
		Width:         plan.Width,
		Height:        plan.Height,
		Background:    plan.Background,
		Fonts:         canonicalFonts(plan.Fonts),
		Env:           env.String(),
		Page: segmentPage{
			DurationMs: page.DurationMs,
			BaseImage:  path.Base(page.BaseImage),
			Layers:     canonicalLayers(page.Layers),
			Transition: effectiveTransition(page.Transition),
		},
	}
	// 只有「转场真的会发生」时，前一页才影响本页像素：
	// 合成器要求 type != none 且 durationMs > 0 且存在前一页（页面 0 没有）。
	if in.Page.Transition != nil && i > 0 {
		prev := plan.Pages[i-1]
		in.Prev = &segmentPrev{
			DurationMs: prev.DurationMs,
			BaseImage:  path.Base(prev.BaseImage),
			Layers:     canonicalLayers(prev.Layers),
		}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("render: marshal segment key for page %d: %w", i, err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// effectiveTransition 把「不会发生的转场」归一成 nil。
// 合成器判定转场生效的条件是 type != none && durationMs > 0，
// 因此 `{type:"fade", durationMs:0}` 与 `{type:"none"}` 像素相同，键也该相同。
func effectiveTransition(t Transition) *Transition {
	if t.Type == "" || t.Type == "none" || t.DurationMs <= 0 {
		return nil
	}
	out := Transition{Type: t.Type, DurationMs: t.DurationMs}
	return &out
}

// canonicalLayers 复制图层并清掉不影响像素的字段：
//
//   - image 是 baseImage + boxes 的确定性派生物，文件名含页序与层序；
//   - clipId / effect 只作审计留痕（IR 规范明写「渲染后端 MUST NOT 读它决定行为」），
//     规格重新保存时 id 可能变，但画面不变。
//
// nil 归一成空切片：`null` 与 `[]` 在 JSON 里不同、在画面上相同，不归一就会
// 「同一份计划换个序列化路径命中不了缓存」。
func canonicalLayers(in []Layer) []Layer {
	out := make([]Layer, len(in))
	for i, l := range in {
		out[i] = Layer{
			ElementID: l.ElementID,
			Kind:      l.Kind,
			AssetMode: l.AssetMode,
			Box:       l.Box,
			Clips:     canonicalClips(l.Clips),
		}
	}
	return out
}

func canonicalClips(in []Motion) []Motion {
	out := make([]Motion, len(in))
	for i, c := range in {
		out[i] = Motion{
			StartMs:    c.StartMs,
			DurationMs: c.DurationMs,
			Easing:     c.Easing,
			Props:      canonicalProps(c.Props),
		}
	}
	return out
}

// canonicalProps 保持属性顺序原样：属性是「同一元素上的叠加指令」，
// 顺序本身不改变求值结果（shared/motion.ts 按名字求和），但重排它没有收益，
// 而把它排序会让「两个顺序不同的等价规格」看起来是两份输入——这不是缓存该猜的事。
func canonicalProps(in []animation.MotionProp) []animation.MotionProp {
	if len(in) == 0 {
		return []animation.MotionProp{}
	}
	out := make([]animation.MotionProp, len(in))
	copy(out, in)
	return out
}

func canonicalFonts(in []Font) []Font {
	if len(in) == 0 {
		return nil
	}
	out := make([]Font, len(in))
	copy(out, in)
	return out
}
