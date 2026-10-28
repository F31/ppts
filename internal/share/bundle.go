// Package share 组装「静态讲解成品包」（html_bundle，M6-1）。
//
// 包的定位：用户拿到 zip 解压后双击 index.html 就能播放带音频、字幕与逐字高亮的讲解，
// 不依赖后端、不依赖网络、不依赖本地安装的任何东西。为此有三条硬约束：
//
//  1. 经典脚本，不用 ES module / fetch：file:// 下这两者会被 CORS 拦住（design D2）。
//  2. 时间只有一个来源：清单里的时间全部由 media.Timeline 派生，播放器复用
//     web/src/playerClock.ts 的纯函数，与 MP4 烧录字幕同规则（design D3/D4）。
//  3. 打包不引入运行时依赖：zip 用 archive/zip，图像用 image/png + image/jpeg，
//     播放器产物由 go:embed 打进二进制（design 的「依赖」章节）。
package share

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/share/assets"
)

// 包内固定条目名。全部由本包生成，绝不采用用户输入拼接路径。
const (
	FileIndexHTML     = "index.html"
	FileManifestJSON  = "manifest.json"
	FileManifestJS    = "manifest.js"
	FileSubtitlesVTT  = "subtitles.vtt"
	FileSubtitlesSRT  = "subtitles.srt"
	FileSubtitlesJS   = "subtitles.js"
	FileTimelineJSON  = "timeline.json"
	FileAppJS         = "app.js"
	FileAppCSS        = "app.css"
	FileAudioMix      = "audio/mix.wav"
	slideEntryPattern = "slides/%03d%s"
	audioEntryPattern = "audio/%03d.wav"
)

// ResourceReader 抽象资源来源：云端是对象存储流，本地是 os.DirFS。
//
// 打包器只依赖接口 + 时间轴，不感知租户、签名 URL 或存储实现，隐私模式因此
// 不需要另一条打包路径（design D6）。
type ResourceReader interface {
	OpenPagePNG(ctx context.Context, key string) (io.ReadCloser, error)
	OpenObject(ctx context.Context, key string) (io.ReadCloser, error)
}

// BundleInput 是一次打包的全部输入。
type BundleInput struct {
	// Timeline 是唯一时间来源；为空或没有页面时打包失败。
	Timeline *media.Timeline
	// PagePNGKeys 按 slide 位置对齐；短于页数视为后续页缺图（沿用上一页）。
	PagePNGKeys []string
	// SRTKey / VTTKey 指向既有字幕对象；VTT 必填（成品必须带字幕）。
	SRTKey string
	VTTKey string
	Width  int
	Height int
	FPS    int
	Title  string
	// SnapshotHash 写入清单，便于把成品包与导出快照对照。
	SnapshotHash string
	// CreatedAt 为零值时省略。
	CreatedAt time.Time
}

// BundleBuilder 负责把输入组装成 zip。
type BundleBuilder struct {
	reader      ResourceReader
	logger      *slog.Logger
	maxLongEdge int
}

// NewBundleBuilder 构造打包器。logger 可为 nil（打包是纯函数式流程，日志只用于降级告警）。
func NewBundleBuilder(reader ResourceReader, logger *slog.Logger) *BundleBuilder {
	return &BundleBuilder{reader: reader, logger: logger, maxLongEdge: MaxPageLongEdge}
}

// Build 写出 zip 并返回已校验的清单。
//
// 流式策略：页面图逐页转码后立刻写 entry（内存中只保留当前页与"上一页"），
// 音频片段逐个写 entry，但会保留一份引用供混音轨拼接（与 MP4 路径的 clips 同量级）。
func (b *BundleBuilder) Build(ctx context.Context, in BundleInput, out io.Writer) (*Manifest, error) {
	if b == nil || b.reader == nil {
		return nil, errors.New("share: resource reader is required")
	}
	if in.Timeline == nil || len(in.Timeline.Slides) == 0 {
		return nil, errors.New("share: timeline with at least one slide is required")
	}
	if strings.TrimSpace(in.VTTKey) == "" {
		return nil, errors.New("share: vtt key is required (the bundle must ship subtitles)")
	}
	timeline := in.Timeline

	manifest := newManifest(timeline, in.Width, in.Height, in.FPS, in.Title, in.SnapshotHash, in.CreatedAt)

	zw := zip.NewWriter(out)
	files := make(map[string]struct{}, len(timeline.Slides)+8)
	write := func(name string, data []byte) error {
		if err := checkBundlePath(name); err != nil {
			return err
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		files[name] = struct{}{}
		return nil
	}

	// ---- 时间轴（与旧 web_project 同名条目，便于下游平滑迁移解析）----
	timelineJSON, err := json.Marshal(timeline)
	if err != nil {
		return nil, fmt.Errorf("share: marshal timeline: %w", err)
	}
	if err := write(FileTimelineJSON, timelineJSON); err != nil {
		return nil, err
	}

	// ---- 字幕 ----
	vtt, err := b.readObject(ctx, in.VTTKey)
	if err != nil {
		return nil, fmt.Errorf("share: read vtt: %w", err)
	}
	if len(vtt) == 0 {
		return nil, fmt.Errorf("share: vtt object %q is empty", in.VTTKey)
	}
	if err := write(FileSubtitlesVTT, vtt); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.SRTKey) != "" {
		srt, srtErr := b.readObject(ctx, in.SRTKey)
		if srtErr != nil {
			return nil, fmt.Errorf("share: read srt: %w", srtErr)
		}
		if err := write(FileSubtitlesSRT, srt); err != nil {
			return nil, err
		}
	}
	subtitlesJS, err := subtitlesScriptBody(timeline.Subtitles)
	if err != nil {
		return nil, fmt.Errorf("share: marshal subtitles: %w", err)
	}
	if err := write(FileSubtitlesJS, subtitlesJS); err != nil {
		return nil, err
	}

	// ---- 页面底图 ----
	slides, err := b.writeSlides(ctx, zw, write, in)
	if err != nil {
		return nil, err
	}
	manifest.Slides = slides

	// ---- 音频 ----
	audioEntries, clips, err := b.writeAudio(ctx, write, timeline)
	if err != nil {
		return nil, err
	}
	manifest.Audio = audioEntries

	// 混音轨：把各段按时间轴落位成一条 WAV，播放器只需一个 currentTime。
	// 拼接失败（素材非 PCM16、时长不符）不阻断打包——播放器有「逐段播放」的降级路径。
	if len(clips) > 0 {
		var buf bytes.Buffer
		if mixErr := media.WriteTimelineWAV(&buf, timeline, clips); mixErr != nil {
			b.logf(slog.LevelWarn, "share: assemble audio mix failed, falling back to per-segment playback", "error", mixErr)
		} else if err := write(FileAudioMix, buf.Bytes()); err != nil {
			return nil, err
		} else {
			manifest.AudioMix = FileAudioMix
		}
	}

	// ---- 播放器与页面 ----
	appJS, err := assets.AppJS()
	if err != nil {
		return nil, fmt.Errorf("share: embedded player script: %w", err)
	}
	appCSS, err := assets.AppCSS()
	if err != nil {
		return nil, fmt.Errorf("share: embedded player stylesheet: %w", err)
	}
	if err := write(FileAppJS, appJS); err != nil {
		return nil, err
	}
	if err := write(FileAppCSS, appCSS); err != nil {
		return nil, err
	}
	indexHTML, err := renderIndexHTML(in.Title)
	if err != nil {
		return nil, fmt.Errorf("share: render index.html: %w", err)
	}
	if err := write(FileIndexHTML, indexHTML); err != nil {
		return nil, err
	}

	// ---- 清单（最后写：文件名在转码后才知道）----
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("share: marshal manifest: %w", err)
	}
	if err := write(FileManifestJSON, append(manifestJSON, '\n')); err != nil {
		return nil, err
	}
	manifestJS, err := manifest.scriptBody()
	if err != nil {
		return nil, fmt.Errorf("share: marshal manifest script: %w", err)
	}
	if err := write(FileManifestJS, manifestJS); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("share: close bundle: %w", err)
	}
	// 打包后立刻用同一套结构门禁自检：宁可在服务端失败，也不要交给用户一个打不开的包。
	if err := manifest.Validate(files); err != nil {
		return nil, err
	}
	return manifest, nil
}

// writeSlides 逐页转码并写 entry，返回清单条目。
//
// 降级语义与 MP4 路径逐条对齐：缺键/读失败 → 沿用上一页（复用同一文件名，
// 播放器切页时 src 不变，等于"画面停住"）；首屏缺失 → 占位图。
func (b *BundleBuilder) writeSlides(
	ctx context.Context,
	_ *zip.Writer,
	write func(string, []byte) error,
	in BundleInput,
) ([]SlideEntry, error) {
	entries := make([]SlideEntry, 0, len(in.Timeline.Slides))
	previousName := ""
	for i, slide := range in.Timeline.Slides {
		var rawKey string
		if i < len(in.PagePNGKeys) {
			rawKey = in.PagePNGKeys[i]
		}
		name := ""
		if rawKey != "" {
			// 资源 key 非法（外链/绝对路径/..）是**数据污染**，不是"这页没图"：
			// MUST 让打包失败，绝不能把带外链的引用写进清单（spec「拒绝绝对路径」）。
			if keyErr := checkResourceKey(rawKey); keyErr != nil {
				return nil, fmt.Errorf("share: slide %d: %w", i, keyErr)
			}
			raw, readErr := b.readPagePNG(ctx, rawKey)
			if readErr != nil {
				// 单页读不到是「能力缺失」而非「内容错误」（A26），降级为沿用上一页。
				b.logf(slog.LevelWarn, "share: read page image failed, reusing previous frame",
					"slide", slide.SlideID, "index", i, "error", readErr)
			} else {
				encoded, ext, encodeErr := EncodePage(raw, b.maxLongEdge)
				if encodeErr != nil {
					// 解码/编码失败：原样放原始字节，扩展名随之改为 .png（必须与内容一致）。
					b.logf(slog.LevelWarn, "share: transcode page image failed, keeping original bytes",
						"slide", slide.SlideID, "index", i, "error", encodeErr)
					encoded, ext = raw, ".png"
				}
				name = fmt.Sprintf(slideEntryPattern, i+1, ext)
				if err := write(name, encoded); err != nil {
					return nil, err
				}
			}
		}
		if name == "" && previousName != "" {
			name = previousName
		}
		if name == "" {
			placeholder := PlaceholderPage(in.Width, in.Height)
			if len(placeholder) == 0 {
				return nil, errors.New("share: build placeholder page failed")
			}
			name = fmt.Sprintf(slideEntryPattern, i+1, ".jpg")
			if err := write(name, placeholder); err != nil {
				return nil, err
			}
		}
		previousName = name
		entries = append(entries, SlideEntry{
			SlideID: slide.SlideID, StartUS: slide.StartUS, EndUS: slide.EndUS, Image: name,
		})
	}
	return entries, nil
}

// writeAudio 逐段写 entry，返回清单条目与供混音轨使用的片段字节。
// 音频缺失必须失败：没有声音的"讲解成品"没有意义（design D8）。
func (b *BundleBuilder) writeAudio(
	ctx context.Context,
	write func(string, []byte) error,
	timeline *media.Timeline,
) ([]AudioEntry, map[string][]byte, error) {
	entries := make([]AudioEntry, 0)
	clips := make(map[string][]byte)
	names := make(map[string]string)
	for _, slide := range timeline.Slides {
		for _, segment := range slide.Segments {
			name, ok := names[segment.AudioKey]
			if !ok {
				if strings.TrimSpace(segment.AudioKey) == "" {
					return nil, nil, fmt.Errorf("share: segment %q has no audio key", segment.SegmentID)
				}
				data, err := b.readObject(ctx, segment.AudioKey)
				if err != nil {
					return nil, nil, fmt.Errorf("share: read audio segment %q: %w", segment.AudioKey, err)
				}
				if len(data) == 0 {
					return nil, nil, fmt.Errorf("share: audio segment %q is empty", segment.AudioKey)
				}
				name = fmt.Sprintf(audioEntryPattern, len(names)+1)
				if err := write(name, data); err != nil {
					return nil, nil, err
				}
				names[segment.AudioKey] = name
				clips[segment.AudioKey] = data
			}
			entries = append(entries, AudioEntry{
				SegmentID: segment.SegmentID, Src: name, StartUS: segment.StartUS, EndUS: segment.EndUS,
			})
		}
	}
	return entries, clips, nil
}

func (b *BundleBuilder) readPagePNG(ctx context.Context, key string) ([]byte, error) {
	if err := checkResourceKey(key); err != nil {
		return nil, err
	}
	return readAllClose(b.reader.OpenPagePNG(ctx, key))
}

func (b *BundleBuilder) readObject(ctx context.Context, key string) ([]byte, error) {
	if err := checkResourceKey(key); err != nil {
		return nil, err
	}
	return readAllClose(b.reader.OpenObject(ctx, key))
}

func readAllClose(rc io.ReadCloser, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// checkResourceKey 拒绝把外部 URL / 绝对路径当成资源 key 传进读取器。
// 对象存储 key 由服务端生成，出现这些形态说明上游数据已被污染，必须显式失败。
func checkResourceKey(key string) error {
	if stripSpace(key) == "" {
		return errors.New("share: empty resource key")
	}
	lower := strings.ToLower(key)
	for _, prefix := range []string{"http://", "https://", "file:", "//"} {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Errorf("share: resource key %q must not be an external reference", key)
		}
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("share: resource key %q must be relative", key)
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("share: resource key %q must not contain parent references", key)
	}
	return nil
}

func stripSpace(s string) string {
	return strings.TrimSpace(s)
}

func (b *BundleBuilder) logf(level slog.Level, msg string, args ...any) {
	if b.logger == nil {
		return
	}
	b.logger.Log(context.Background(), level, msg, args...)
}
