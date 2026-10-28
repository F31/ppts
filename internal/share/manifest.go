package share

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/F31/ppts/internal/media"
)

// ManifestSchemaVersion 是包清单的结构版本。播放器只认这个版本；
// 后续 M6-2 引入动画字段时递增，并保持旧播放器可忽略未知字段（向前兼容）。
const ManifestSchemaVersion = 1

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
