package animation

// 节拍表 shot 与来源。阶段 1 只有注视与合并用的 hold；crop / entrance 不在这里。
const (
	BeatShotGaze = "gaze"
	BeatShotHold = "hold"

	BeatSourceVAD      = "vad"
	BeatSourcePunct    = "punct"
	BeatSourceFallback = "fallback"
	BeatSourceLLM      = "llm"
)

// BeatSheet 是一页旁白钟上的注视注解。不是第二套时间轴。
// JSON 标签只给单测与 debug dump；阶段 1 不进 PUT。
type BeatSheet struct {
	Source string `json:"source,omitempty"` // vad | punct | fallback | llm
	Beats  []Beat `json:"beats,omitempty"`
}

// Beat 一拍。ShapeID 空且 Shot=hold 表示并入上一拍。
type Beat struct {
	StartMs   int64  `json:"startMs"`
	EndMs     int64  `json:"endMs,omitempty"` // 0 = 到下一拍 / 页尾
	SegmentID string `json:"segmentId,omitempty"`
	ShapeID   string `json:"shapeId,omitempty"`
	Shot      string `json:"shot,omitempty"` // gaze | hold；空 = gaze
	// Text 是这一拍的旁白（DisplayText 片段），compile 用来画词级下划线。
	Text string `json:"text,omitempty"`
	// Intent 是导演层的可选创作元数据；缺省时旧 BeatSheet 完全不变。
	// 它不进 Timeline / IR / planHash，只供审阅、lint 和后续 MotionIntent 编译读取。
	Intent *BeatIntent `json:"intent,omitempty"`
}

// BeatIntent 是“这一段怎么讲”的可选创作字段。
// 所有字段都可缺省：模型只返回旧六字段时，Beat 行为与现状一致。
type BeatIntent struct {
	AudienceQuestion string       `json:"audienceQuestion,omitempty"`
	VisualMetaphor   string       `json:"visualMetaphor,omitempty"`
	InitialState     string       `json:"initialState,omitempty"`
	Actions          []BeatAction `json:"actions,omitempty"`
	FinalState       string       `json:"finalState,omitempty"`
	CausalAction     string       `json:"causalAction,omitempty"`
	CausalChange     string       `json:"causalChange,omitempty"`
	MotionIntent     string       `json:"motionIntent,omitempty"`
	Flow             *BeatFlow    `json:"flow,omitempty"`
	AnchorWord       string       `json:"anchorWord,omitempty"`
	AnchorMs         int64        `json:"anchorMs,omitempty"`
	Confidence       float64      `json:"confidence,omitempty"`
}

// BeatFlow 是导演层给出的 A→B 传递意图。它只描述目标与路线，
// 具体画成点/箭头由 render 的 Flow 编译器负责。
type BeatFlow struct {
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Carrier    string `json:"carrier,omitempty"`
	Route      string `json:"route,omitempty"`
	StartMs    int64  `json:"startMs,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// BeatAction 是 intent 内的一个动作描述；actor 仍是页内 shapeId。
type BeatAction struct {
	Actor      string `json:"actor,omitempty"`
	Action     string `json:"action,omitempty"`
	AnchorWord string `json:"anchorWord,omitempty"`
	AnchorMs   int64  `json:"anchorMs,omitempty"`
}

// Valid 计全局互异的非 hold ShapeID。少于 2 个则编译应回退 equal-spread。
func (s *BeatSheet) Valid() bool {
	if s == nil {
		return false
	}
	seen := map[string]struct{}{}
	for _, b := range s.Beats {
		if b.ShapeID == "" || b.Shot == BeatShotHold {
			continue
		}
		seen[b.ShapeID] = struct{}{}
	}
	return len(seen) >= 2
}
