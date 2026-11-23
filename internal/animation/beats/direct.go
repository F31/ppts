package beats

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
)

const (
	directorPromptVersion  = "beats-direct-v2"
	defaultDirectorTimeout = 12 * time.Second
	maxDirectorBeats       = 24
	maxDirectorElemChars   = 120
	maxDirectorSegChars    = 400
	maxDirectorElems       = 30

	DirectorFallbackNoModel     = "no_model"
	DirectorFallbackTimeout     = "timeout"
	DirectorFallbackError       = "error"
	DirectorFallbackInvalidJSON = "invalid_json"
	DirectorFallbackRejected    = "rejected"
	DirectorFallbackNoSpeech    = "no_speech"
)

// DirectOptions 是阶段 2 导演的可选依赖。零值 = 不问模型。
type DirectOptions struct {
	Model   bind.ModelCompleter
	Cache   bind.CompletionCache
	ModelID string
	Timeout time.Duration
	// Refine 为真时，阶段 1 已经 Valid 也再问一次：用户在导出勾了「导演加强」。
	// 模型失败或拍表不合格时仍退回 phase1。
	Refine bool
}

const directorSystemPrompt = `你在做一件很窄的事：根据讲解稿，给这一页 PPT 排出注视节拍表。

规则：
1. 只能使用我给你的 shapeId 与 segmentId，不许创造新的 id。
2. 不许输出代码、ffmpeg、URL、JSX、滤镜或任何资源地址。
3. shot 只能是 gaze 或 hold。
4. startMs 必须落在该段 [startMs, endMs) 内，且整表严格递增。
5. 至少两个不同的 gaze shapeId。
6. 宁可少拍，也不要猜。没点名的形状不要出现。
7. 页码、页眉、图注已经不在候选里。
8. 可选创作字段（有把握才填，没有就省略）：audienceQuestion、visualMetaphor、initialState、actions、finalState、causalAction、causalChange、motionIntent、flow、anchorWord、anchorMs、confidence。不要为了填而编。

只输出一个 JSON 对象，形状严格如下，不要任何解释文字、不要 markdown 代码块：
{"beats":[{"startMs":0,"endMs":0,"segmentId":"...","shapeId":"...","shot":"gaze"}]}
endMs=0 表示持续到下一拍。`

// Direct 在阶段 1 拍表无效时问模型要一份约束 JSON。永不因为模型而失败：
// 没配模型、超时、输出不是拍表、校验一条都不该收，都退回 phase1。
//
// 阶段 1 已经 Valid 时默认连请求都不发（预览与未勾加强的导出）。
// Refine 时对 Valid 拍表再问一次，失败仍用阶段 1。
func Direct(ctx context.Context, in Input, phase1 Output, opt DirectOptions) Output {
	if phase1.Reason == ReasonNoSpeech {
		phase1.DirectorFallback = DirectorFallbackNoSpeech
		return phase1
	}
	if phase1.Sheet.Valid() && !opt.Refine {
		return phase1
	}
	if opt.Model == nil {
		phase1.DirectorFallback = DirectorFallbackNoModel
		return phase1
	}
	cands, _, _ := collectCandidates(in)
	if len(cands) < 2 {
		phase1.DirectorFallback = DirectorFallbackRejected
		return phase1
	}

	prompt := directorPromptFor(cands, in.Segments, phase1, opt.Refine)
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = defaultDirectorTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}

	raw, fallback, err := completeDirector(ctx, prompt, opt, timeout)
	if err != nil || raw == "" {
		if fallback == "" {
			fallback = DirectorFallbackError
		}
		phase1.DirectorFallback = fallback
		return phase1
	}

	beats, ok := parseDirectorBeats(raw, cands, in.Segments)
	if !ok {
		phase1.DirectorFallback = DirectorFallbackInvalidJSON
		return phase1
	}
	sheet := animation.BeatSheet{Source: animation.BeatSourceLLM, Beats: beats}
	if !sheet.Valid() {
		phase1.DirectorFallback = DirectorFallbackRejected
		return phase1
	}
	return Output{
		Sheet:       sheet,
		DenyIDs:     phase1.DenyIDs,
		DenyReasons: phase1.DenyReasons,
		Reason:      ReasonOK,
	}
}

func completeDirector(ctx context.Context, prompt bind.ModelPrompt, opt DirectOptions, timeout time.Duration) (string, string, error) {
	var cacheKey string
	if opt.Cache != nil && opt.ModelID != "" {
		cacheKey = directorCacheKey(opt.ModelID, directorPromptVersion, prompt.System, prompt.User)
		if text, hit, err := opt.Cache.Get(ctx, cacheKey); err == nil && hit && strings.TrimSpace(text) != "" {
			return text, "", nil
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	comp, err := opt.Model.Complete(callCtx, prompt)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return "", DirectorFallbackTimeout, err
		}
		return "", DirectorFallbackError, err
	}
	text := strings.TrimSpace(comp.Text)
	if text == "" {
		return "", DirectorFallbackInvalidJSON, errors.New("beats: empty director completion")
	}
	if cacheKey != "" {
		_ = opt.Cache.Put(ctx, cacheKey, text)
	}
	return text, "", nil
}

func directorPromptFor(cands []bind.Element, segs []SpeechSeg, phase1 Output, refine bool) bind.ModelPrompt {
	var b strings.Builder
	b.WriteString("PAGE\n")
	n := 0
	for _, e := range cands {
		if n >= maxDirectorElems {
			break
		}
		text := clipDirectorText(e.Text, maxDirectorElemChars)
		if text == "" {
			text = "(no text)"
		}
		b.WriteString("E ")
		b.WriteString(e.ID)
		b.WriteString(" | ")
		if e.Kind == "" {
			b.WriteString("shape")
		} else {
			b.WriteString(e.Kind)
		}
		b.WriteString(" | ")
		b.WriteString(text)
		b.WriteByte('\n')
		n++
	}
	b.WriteString("SEG\n")
	for _, s := range segs {
		b.WriteString("S ")
		b.WriteString(s.ID)
		b.WriteString(" | ")
		b.WriteString(strconv.FormatInt(s.StartMs, 10))
		b.WriteByte('-')
		b.WriteString(strconv.FormatInt(s.EndMs, 10))
		b.WriteString(" | ")
		b.WriteString(clipDirectorText(s.Text, maxDirectorSegChars))
		b.WriteByte('\n')
	}
	if refine && phase1.Sheet.Valid() && len(phase1.Sheet.Beats) > 0 {
		b.WriteString("PHASE1\n")
		for _, beat := range phase1.Sheet.Beats {
			shot := beat.Shot
			if shot == "" {
				shot = animation.BeatShotGaze
			}
			b.WriteString("B ")
			b.WriteString(strconv.FormatInt(beat.StartMs, 10))
			b.WriteByte('-')
			b.WriteString(strconv.FormatInt(beat.EndMs, 10))
			b.WriteString(" | ")
			b.WriteString(beat.SegmentID)
			b.WriteString(" | ")
			b.WriteString(beat.ShapeID)
			b.WriteString(" | ")
			b.WriteString(shot)
			b.WriteByte('\n')
		}
		b.WriteString("若给不出至少两个不同 gaze shapeId 的更好排法，请原样复述 PHASE1。\n")
	}
	return bind.ModelPrompt{System: directorSystemPrompt, User: strings.TrimRight(b.String(), "\n")}
}

type llmBeatSheet struct {
	Beats []llmBeat `json:"beats"`
}

type llmBeat struct {
	StartMs   int64  `json:"startMs"`
	EndMs     int64  `json:"endMs"`
	SegmentID string `json:"segmentId"`
	ShapeID   string `json:"shapeId"`
	Shot      string `json:"shot"`
	Text      string `json:"text"`

	// 可选创作字段：缺失/非法时忽略，不影响旧六字段解析。
	Confidence       float64                `json:"confidence,omitempty"`
	AudienceQuestion string                 `json:"audienceQuestion,omitempty"`
	VisualMetaphor   string                 `json:"visualMetaphor,omitempty"`
	InitialState     string                 `json:"initialState,omitempty"`
	Actions          []animation.BeatAction `json:"actions,omitempty"`
	FinalState       string                 `json:"finalState,omitempty"`
	CausalAction     string                 `json:"causalAction,omitempty"`
	CausalChange     string                 `json:"causalChange,omitempty"`
	MotionIntent     string                 `json:"motionIntent,omitempty"`
	Flow             *animation.BeatFlow    `json:"flow,omitempty"`
	AnchorWord       string                 `json:"anchorWord,omitempty"`
	AnchorMs         int64                  `json:"anchorMs,omitempty"`
}

func parseDirectorBeats(raw string, cands []bind.Element, segs []SpeechSeg) ([]animation.Beat, bool) {
	if directorHasForbidden(raw) {
		return nil, false
	}
	obj, ok := extractDirectorJSON(raw)
	if !ok {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(obj))
	dec.DisallowUnknownFields()
	var top llmBeatSheet
	if err := dec.Decode(&top); err != nil {
		return nil, false
	}
	if len(top.Beats) == 0 || len(top.Beats) > maxDirectorBeats {
		return nil, false
	}
	candIDs := make(map[string]bind.Element, len(cands))
	for _, e := range cands {
		if e.ID != "" {
			candIDs[e.ID] = e
		}
	}
	segByID := make(map[string]SpeechSeg, len(segs))
	pageDur := pageDurationMs(segs)
	for _, s := range segs {
		segByID[s.ID] = s
		if s.EndMs > pageDur {
			pageDur = s.EndMs
		}
	}
	out := make([]animation.Beat, 0, len(top.Beats))
	var prevStart int64 = -1
	for _, item := range top.Beats {
		shot := strings.TrimSpace(item.Shot)
		if shot == "" {
			shot = animation.BeatShotGaze
		}
		if shot != animation.BeatShotGaze && shot != animation.BeatShotHold {
			return nil, false
		}
		if !directorIDOK(item.SegmentID) {
			return nil, false
		}
		seg, ok := segByID[item.SegmentID]
		if !ok {
			return nil, false
		}
		if item.StartMs < 0 || (pageDur > 0 && item.StartMs >= pageDur) {
			return nil, false
		}
		if item.StartMs < seg.StartMs || (seg.EndMs > seg.StartMs && item.StartMs >= seg.EndMs) {
			return nil, false
		}
		if item.EndMs < 0 || (item.EndMs > 0 && item.EndMs <= item.StartMs) {
			return nil, false
		}
		if pageDur > 0 && item.EndMs > pageDur {
			return nil, false
		}
		if item.StartMs <= prevStart {
			return nil, false
		}
		b := animation.Beat{
			StartMs:   item.StartMs,
			EndMs:     item.EndMs,
			SegmentID: item.SegmentID,
			Shot:      shot,
			Text:      strings.TrimSpace(item.Text),
			Intent:    intentFromLLM(item),
		}
		if shot == animation.BeatShotGaze {
			if !directorIDOK(item.ShapeID) {
				return nil, false
			}
			cand, ok := candIDs[item.ShapeID]
			if !ok {
				return nil, false
			}
			// 有明确序数的段落，模型不能把「第二部分」指到第三部分。
			// 无结构页仍允许模型补字面匹配不到的语义关系。
			if ordinalClauseRe.MatchString(seg.Text) &&
				bind.Score(seg.Text, cand.Text, cand.Kind) < bind.DefaultThreshold {
				return nil, false
			}
			b.ShapeID = item.ShapeID
		}
		if b.Text == "" {
			b.Text = seg.Text
		}
		out = append(out, b)
		prevStart = item.StartMs
	}
	return out, true
}

func intentFromLLM(item llmBeat) *animation.BeatIntent {
	if item.AudienceQuestion == "" && item.VisualMetaphor == "" &&
		item.InitialState == "" && len(item.Actions) == 0 &&
		item.FinalState == "" && item.CausalAction == "" &&
		item.CausalChange == "" && item.MotionIntent == "" && item.Flow == nil &&
		item.AnchorWord == "" && item.AnchorMs == 0 && item.Confidence == 0 {
		return nil
	}
	intent := &animation.BeatIntent{
		AudienceQuestion: item.AudienceQuestion,
		VisualMetaphor:   item.VisualMetaphor,
		InitialState:     item.InitialState,
		Actions:          item.Actions,
		FinalState:       item.FinalState,
		CausalAction:     item.CausalAction,
		CausalChange:     item.CausalChange,
		MotionIntent:     item.MotionIntent,
		Flow:             item.Flow,
		AnchorWord:       item.AnchorWord,
		AnchorMs:         item.AnchorMs,
	}
	if item.Confidence >= 0 && item.Confidence <= 1 {
		intent.Confidence = item.Confidence
	}
	return intent
}

func directorHasForbidden(raw string) bool {
	s := strings.ToLower(raw)
	for _, needle := range []string{
		"ffmpeg", "filter_complex", "filtergraph", "remotion",
		"javascript:", "<script", "http://", "https://", "data:",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func directorIDOK(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '_', '-', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func extractDirectorJSON(raw string) ([]byte, bool) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			inner := strings.TrimSpace(rest[:j])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "json"))
			s = inner
		}
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil, false
	}
	return []byte(s), true
}

func clipDirectorText(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " / "))
	if n <= 0 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func directorCacheKey(model, promptVersion, system, user string) string {
	h := sha256.New()
	for _, part := range []string{model, promptVersion, "0", system, user} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
