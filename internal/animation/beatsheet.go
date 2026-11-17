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
