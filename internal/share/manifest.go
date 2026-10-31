package share

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/media"
)

// ManifestSchemaVersion 是包清单的结构版本。播放器只认这个版本；后续引入动画字段时递增，
// 并保持旧播放器可忽略未知字段（向前兼容）。
//
// v2（M6-2）：`slides[i]` 增加 `plate`/`transition`/`layers`。版本位必须升——
// 因为 v2 的 `image` 语义变了（从「完整页面」变成「挖空底板」），旧播放器读到会
// 把挖空后的纯色疤当正常画面播。包内自带播放器，正常使用不会跨版本混读，但版本位
// 能让新播放器明确拒绝它不认识的更高版本，而不是猜着播。
const ManifestSchemaVersion = 2

// ManifestDurationToleranceUS 是清单与时间轴时长的允许误差。
// 时间轴以微秒声明、WAV 按帧落位，跨页累加最多引入 1ms 级误差，100ms 足够严格。
const ManifestDurationToleranceUS = 100_000

// Manifest 是成品包的机读清单：包内路径 + 时间 + 播放参数。
//
// 时间字段全部由 media.Timeline 派生，不引入第二套时间（design D4）：
// slides[i].startUs/endUs 与 audio[j].startUs/endUs 直接取自 Timeline，
// 因此 HTML 的时序与 MP4/字幕天然一致。
type Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Title         string       `json:"title,omitempty"`
	Artifact      ArtifactInfo `json:"artifact"`
	Video         VideoInfo    `json:"video"`
	Slides        []SlideEntry `json:"slides"`
	Audio         []AudioEntry `json:"audio"`
	// AudioMix 是按时间轴落位的整段音轨（含段间静音）。播放器优先用它：
	// 单一 currentTime 即全局时间，seek 与字幕对齐无需换算，也不会累积漂移。
	AudioMix  string       `json:"audioMix,omitempty"`
	Subtitles SubtitleInfo `json:"subtitles"`
	// Fonts 本阶段恒为空：播放器只用系统字体族回退（不内嵌字体，M6-4 再说）。
	Fonts []string `json:"fonts"`
}

type ArtifactInfo struct {
	DurationUS   int64  `json:"durationUs"`
	SnapshotHash string `json:"snapshotHash,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
}

type VideoInfo struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	FPS    int `json:"fps"`
}

type SlideEntry struct {
	SlideID string `json:"slideId"`
	StartUS int64  `json:"startUs"`
	EndUS   int64  `json:"endUs"`
	Image   string `json:"image"`
	// Plate 为 true 表示 Image 是「挖空元素本体」的底板：元素区域已被周围底色填平，
	// 由 Layers 盖回去。没有动画的页为 false，Image 就是完整页面图。
	Plate bool `json:"plate,omitempty"`
	// Transition 描述「如何进入这一页」，挂在后一页（与 IR 同语义）。
	Transition *SlideTransition `json:"transition,omitempty"`
	// Layers 按绘制顺序排列（父在前、子在后），与 IR 的 layers 同序。
	Layers []SlideLayer `json:"layers,omitempty"`
	// Focus 是页级焦点序列，与 IR 的 page.focus 同语义：此刻讲的是谁，
	// 其余层由 shared/motion.ts 的 focusDim 推导出压暗量。播放器与 MP4 共用同一份求值。
	Focus []SlideFocus `json:"focus,omitempty"`
	// Shots 把一页切成若干镜头切片（IR 的 page.shots 原样透传）。
	// 为空时 Image 是唯一画面来源，与 M6-2 的清单逐字节一致。
	Shots []SlideShot `json:"shots,omitempty"`
}

// SlideShot 是一段页内镜头：一张铺满舞台的图，以及它取自原页的哪一块。
//
// 时间与 IR 同语义：CrossFadeMs 占本镜头开头，混合期内两张图同时可见，
// 因此任何取样判据都要避开 [startMs, startMs+crossFadeMs)。
type SlideShot struct {
	Index int `json:"index"`
	// Image 铺满舞台（整页图，或裁切放大后的特写图）。
	Image string `json:"image"`
	// Crop 是 Image 取自原页的区域；nil 表示整页。
	Crop        *SlideBox `json:"crop,omitempty"`
	StartMs     int64     `json:"startMs"`
	DurationMs  int64     `json:"durationMs"`
	CrossFadeMs int       `json:"crossFadeMs"`
}

// maxShotsPerSlide 与 render.MaxShotsPerPage 同值。share 不依赖 render（依赖纪律），
// 所以这里是字面量；manifest_test.go 里有一条断言守两者相等，改一边就红。
const maxShotsPerSlide = 4

// SlideFocus 是一段焦点区间：从 StartMs 起焦点落在 ElementID 上；EndMs 非零时
// 到那一刻放手（整页回到全亮），否则保持到下一段开始。
type SlideFocus struct {
	StartMs   int64  `json:"startMs"`
	ElementID string `json:"elementId"`
	EndMs     int64  `json:"endMs,omitempty"`
	FadeMs    int64  `json:"fadeMs,omitempty"`
}

// SlideTransition 是页间转场。缺省（nil 或 type=none）表示直接切换。
type SlideTransition struct {
	// none | fade | slide | zoom
	Type       string `json:"type"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// SlideLayer 是一个可做元素级动作的区域。
//
// 这里的 clips 是 **IR 原样**，不是预编译的 WAAPI 关键帧：预编译要在 Go 里
// 写一份 cubic-bezier 求解器，而求值代码只有 shared/motion.ts 一份——
// 两份求解器就是两端漂移的起点（tasks 2.9）。
type SlideLayer struct {
	ElementID string   `json:"elementId,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Box       SlideBox `json:"box"`
	// Image 是包内相对路径，尺寸等于 Box（像素）。
	Image string `json:"image"`
	// Origin 是缩放锚点（缺省 center）。柱子的「从基线长上去」靠它。
	Origin string `json:"origin,omitempty"`
	Clips  []Clip `json:"clips,omitempty"`
}

// SlideBox 是画布像素矩形。
type SlideBox struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Clip 是 IR 里一条已展开的动效。
//
// 叶类型（缓动四点、属性 from/to）直接用 animation 包的定义，不在这里重抄一遍：
// 重抄就意味着「清单里的 easing 字段名」与「渲染器读的 easing 字段名」有两处来源。
type Clip struct {
	ClipID     string                 `json:"clipId,omitempty"`
	Effect     string                 `json:"effect"`
	StartMs    int64                  `json:"startMs"`
	DurationMs int64                  `json:"durationMs"`
	Easing     animation.CubicBezier  `json:"easing"`
	Props      []animation.MotionProp `json:"props"`
}

type AudioEntry struct {
	SegmentID string `json:"segmentId"`
	Src       string `json:"src"`
	StartUS   int64  `json:"startUs"`
	EndUS     int64  `json:"endUs"`
}

type SubtitleInfo struct {
	// Src 是机读/交换用的 WebVTT（外部工具与后续托管页使用）。
	Src string `json:"src"`
	// Inline 是 file:// 下可加载的经典脚本，内容与 Src 同源 cues。
	Inline string `json:"inline"`
}

// newManifest 组装清单骨架；slides/audio 由打包过程填充（文件名在转码时才确定）。
func newManifest(timeline *media.Timeline, width, height, fps int, title, snapshotHash string, createdAt time.Time) *Manifest {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	if fps <= 0 {
		fps = 30
	}
	m := &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Title:         title,
		Video:         VideoInfo{Width: width, Height: height, FPS: fps},
		Slides:        make([]SlideEntry, 0, len(timeline.Slides)),
		Audio:         make([]AudioEntry, 0),
		Subtitles:     SubtitleInfo{Src: FileSubtitlesVTT, Inline: FileSubtitlesJS},
		Fonts:         []string{},
	}
	m.Artifact = ArtifactInfo{DurationUS: timeline.DurationUS, SnapshotHash: snapshotHash}
	if !createdAt.IsZero() {
		m.Artifact.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	}
	return m
}

// Validate 校验清单自洽性：路径合法、时间单调、总时长与 artifacts.durationUs 一致。
// files 为包内实际存在的条目集合（nil 表示只校验结构，不校验文件存在）。
//
// 这是打包完成后与测试/CI 共用的唯一结构门禁：M6-2 新增字段时在这里追加约束，
// 而不是在每个调用点各写一份。
func (m *Manifest) Validate(files map[string]struct{}) error {
	if m == nil {
		return fmt.Errorf("share: manifest is nil")
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("share: unsupported manifest schemaVersion %d", m.SchemaVersion)
	}
	if m.Video.Width <= 0 || m.Video.Height <= 0 || m.Video.FPS <= 0 {
		return fmt.Errorf("share: invalid video parameters %dx%d@%d", m.Video.Width, m.Video.Height, m.Video.FPS)
	}
	if m.Artifact.DurationUS <= 0 {
		return fmt.Errorf("share: manifest duration must be positive")
	}
	if len(m.Slides) == 0 {
		return fmt.Errorf("share: manifest has no slides")
	}
	if m.Subtitles.Src == "" || m.Subtitles.Inline == "" {
		return fmt.Errorf("share: manifest subtitles are incomplete")
	}
	if err := checkBundlePath(m.Subtitles.Src); err != nil {
		return err
	}
	if err := checkBundlePath(m.Subtitles.Inline); err != nil {
		return err
	}
	var cursor int64
	for i, slide := range m.Slides {
		if slide.SlideID == "" {
			return fmt.Errorf("share: slide %d has no id", i)
		}
		if slide.StartUS != cursor {
			return fmt.Errorf("share: slide %d starts at %dus, want %dus (gaps/overlaps are not allowed)", i, slide.StartUS, cursor)
		}
		if slide.EndUS <= slide.StartUS {
			return fmt.Errorf("share: slide %d has non-positive duration", i)
		}
		if err := checkBundlePath(slide.Image); err != nil {
			return fmt.Errorf("share: slide %d image: %w", i, err)
		}
		if err := validateSlideAnimation(i, slide, files); err != nil {
			return err
		}
		if err := validateSlideShots(i, slide, m.Video, files); err != nil {
			return err
		}
		if files != nil {
			if _, ok := files[slide.Image]; !ok {
				return fmt.Errorf("share: slide %d image %q is missing from bundle", i, slide.Image)
			}
		}
		cursor = slide.EndUS
	}
	if diff := cursor - m.Artifact.DurationUS; diff > ManifestDurationToleranceUS || diff < -ManifestDurationToleranceUS {
		return fmt.Errorf("share: slides end at %dus but artifact duration is %dus (tolerance %dus)",
			cursor, m.Artifact.DurationUS, ManifestDurationToleranceUS)
	}
	var audioCursor int64
	for i, audio := range m.Audio {
		if audio.StartUS < audioCursor {
			return fmt.Errorf("share: audio %d starts before previous segment ends", i)
		}
		if audio.EndUS <= audio.StartUS {
			return fmt.Errorf("share: audio %d has non-positive duration", i)
		}
		if err := checkBundlePath(audio.Src); err != nil {
			return fmt.Errorf("share: audio %d: %w", i, err)
		}
		if files != nil {
			if _, ok := files[audio.Src]; !ok {
				return fmt.Errorf("share: audio %d source %q is missing from bundle", i, audio.Src)
			}
		}
		audioCursor = audio.EndUS
	}
	if m.AudioMix != "" {
		if err := checkBundlePath(m.AudioMix); err != nil {
			return err
		}
		if files != nil {
			if _, ok := files[m.AudioMix]; !ok {
				return fmt.Errorf("share: audioMix %q is missing from bundle", m.AudioMix)
			}
		}
	}
	for _, name := range []string{FileIndexHTML, FileManifestJSON, FileManifestJS, FileTimelineJSON, FileAppJS, FileAppCSS} {
		if files != nil {
			if _, ok := files[name]; !ok {
				return fmt.Errorf("share: required entry %q is missing from bundle", name)
			}
		}
	}
	return nil
}

// validateSlideAnimation 校验一页的动画字段自洽。
//
// 三条约束都是「不校验就会静默出错」的类型：
//   - 有 layers 但没挖空：层图会盖在**已含元素本体**的完整页上，同一元素被画两次，
//     入场动画期间能看到重影。这种画面不报错，只会被当成「渲染有点糊」。
//   - 层图尺寸与 box 不一致：浏览器会缩放插值，文字边缘发虚，而两端 SSIM 会莫名偏低。
//   - 转场类型拼错：播放器不认识就退化成直接切换，看起来「转场没了」却不报错。
// validateSlideShots 校验页内镜头切片。
//
// 与 IR 的 validateShots 有一处刻意的重复：门禁对的是**产物**（人可能手改清单），
// 所以产物侧必须自己再判一次，不能假设清单一定由本仓的编译器生成。
func validateSlideShots(index int, slide SlideEntry, video VideoInfo, files map[string]struct{}) error {
	if len(slide.Shots) == 0 {
		return nil
	}
	if len(slide.Shots) > maxShotsPerSlide {
		return fmt.Errorf("share: slide %d has %d shots, limit %d", index, len(slide.Shots), maxShotsPerSlide)
	}
	// 首镜必须就是页级图：旧播放器只认 Image，两者不一致时同一份包在不同播放器上
	// 会给出两个画面——而这类分歧不会被任何截图发现。
	if slide.Shots[0].Image != slide.Image {
		return fmt.Errorf("share: slide %d first shot image %q must equal slide image %q",
			index, slide.Shots[0].Image, slide.Image)
	}
	pageMS := (slide.EndUS - slide.StartUS) / 1000
	frameMS := int64(33)
	if video.FPS > 0 {
		frameMS = int64(1000 / video.FPS)
	}
	var cursor int64
	for si, shot := range slide.Shots {
		if shot.Index != si {
			return fmt.Errorf("share: slide %d shot %d has index %d (order must match index)",
				index, si, shot.Index)
		}
		if err := checkBundlePath(shot.Image); err != nil {
			return fmt.Errorf("share: slide %d shot %d image: %w", index, si, err)
		}
		if files != nil {
			if _, ok := files[shot.Image]; !ok {
				return fmt.Errorf("share: slide %d shot %d image %q is missing from bundle",
					index, si, shot.Image)
			}
		}
		if shot.StartMs != cursor {
			return fmt.Errorf("share: slide %d shot %d starts at %dms, want %dms (gaps/overlaps are not allowed)",
				index, si, shot.StartMs, cursor)
		}
		if shot.DurationMs <= 0 {
			return fmt.Errorf("share: slide %d shot %d has non-positive duration", index, si)
		}
		if shot.CrossFadeMs < 0 || int64(shot.CrossFadeMs) >= shot.DurationMs {
			return fmt.Errorf("share: slide %d shot %d crossFade %dms must be in [0,%d)",
				index, si, shot.CrossFadeMs, shot.DurationMs)
		}
		if shot.Crop != nil {
			c := *shot.Crop
			if c.W <= 0 || c.H <= 0 {
				return fmt.Errorf("share: slide %d shot %d crop %dx%d must be positive", index, si, c.W, c.H)
			}
			if c.X < 0 || c.Y < 0 || c.X+c.W > video.Width || c.Y+c.H > video.Height {
				return fmt.Errorf("share: slide %d shot %d crop (%d,%d %dx%d) outside page %dx%d",
					index, si, c.X, c.Y, c.W, c.H, video.Width, video.Height)
			}
		}
		cursor += shot.DurationMs
	}
	if cursor < pageMS-frameMS || cursor > pageMS+frameMS {
		return fmt.Errorf("share: slide %d shots cover %dms, page is %dms", index, cursor, pageMS)
	}
	return nil
}

func validateSlideAnimation(index int, slide SlideEntry, files map[string]struct{}) error {
	if slide.Transition != nil {
		switch slide.Transition.Type {
		case "none", "fade", "slide", "zoom":
		default:
			return fmt.Errorf("share: slide %d has unknown transition %q", index, slide.Transition.Type)
		}
		if slide.Transition.DurationMs < 0 {
			return fmt.Errorf("share: slide %d transition duration must not be negative", index)
		}
	}
	if len(slide.Layers) == 0 {
		return nil
	}
	if !slide.Plate {
		return fmt.Errorf("share: slide %d has %d layers but its image is not a plate "+
			"(elements would be drawn twice)", index, len(slide.Layers))
	}
	if slide.Transition == nil {
		return fmt.Errorf("share: slide %d has layers but no transition entry", index)
	}
	for k, layer := range slide.Layers {
		if layer.Box.W <= 0 || layer.Box.H <= 0 {
			return fmt.Errorf("share: slide %d layer %d has non-positive box %dx%d",
				index, k, layer.Box.W, layer.Box.H)
		}
		if err := checkBundlePath(layer.Image); err != nil {
			return fmt.Errorf("share: slide %d layer %d image: %w", index, k, err)
		}
		if files != nil {
			if _, ok := files[layer.Image]; !ok {
				return fmt.Errorf("share: slide %d layer %d image %q is missing from bundle",
					index, k, layer.Image)
			}
		}
		for c, clip := range layer.Clips {
			if clip.DurationMs < 0 {
				return fmt.Errorf("share: slide %d layer %d clip %d has negative duration", index, k, c)
			}
			if clip.Effect == "" {
				return fmt.Errorf("share: slide %d layer %d clip %d has no effect tag", index, k, c)
			}
		}
	}
	return nil
}

// MarshalJSONLines 生成 manifest.js 的脚本体（经典脚本，file:// 下可加载）。
func (m *Manifest) scriptBody() ([]byte, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return []byte("window.__PPT_MANIFEST__ = " + string(payload) + ";\n"), nil
}

// subtitlesScriptBody 生成 subtitles.js：cues 来自时间轴（含字符级时间戳），
// 与 subtitles.vtt 同一份内容，只是换成经典脚本形式以绕开 file:// 的 fetch/CORS 限制。
func subtitlesScriptBody(cues []media.SubtitleCue) ([]byte, error) {
	if cues == nil {
		cues = []media.SubtitleCue{}
	}
	payload, err := json.Marshal(cues)
	if err != nil {
		return nil, err
	}
	return []byte("window.__PPT_SUBTITLES__ = " + string(payload) + ";\n"), nil
}

// checkBundlePath 拒绝一切可能逃出包根或指向外部资源的条目名。
// 条目名由打包器生成，这里仍逐条校验（防御性：清单字段一旦被手工改写就必须失败）。
func checkBundlePath(name string) error {
	if name == "" {
		return fmt.Errorf("share: empty bundle path")
	}
	if strings.ContainsAny(name, "\\:") {
		return fmt.Errorf("share: bundle path %q must not contain backslash or colon", name)
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("share: bundle path %q must be relative", name)
	}
	if strings.Contains(name, "://") || strings.HasPrefix(name, "//") {
		return fmt.Errorf("share: bundle path %q must not be an external URL", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("share: bundle path %q contains an invalid segment", name)
		}
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return fmt.Errorf("share: bundle path %q contains unsupported character %q", name, r)
		}
	}
	return nil
}
