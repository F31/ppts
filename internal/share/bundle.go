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
	// layerEntryPattern 用「页号-layer-层序」，层序与清单 layers 下标一致：
	// 排查时能直接从文件名定位到清单第几项，不必反查。
	layerEntryPattern = "slides/%03d-layer-%02d.png"
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
	// PagePNGBytes 与 PagePNGKeys 同序；某项非空时直接用这些字节，不再回读。
	//
	// HTML 成品包要拿原始 PNG 做挖空（转码后再挖取色会偏），调用方本来就已读进内存，
	// 再让打包器读一遍等于同一份对象读两次——除了多一次往返，还留下
	// 「两次读之间对象被替换」这种不该存在的窗口。
	PagePNGBytes [][]byte
	// SRTKey / VTTKey 指向既有字幕对象；VTT 必填（成品必须带字幕）。
	SRTKey string
	VTTKey string
	Width  int
	Height int
	FPS    int
	Title  string
	// SnapshotHash 写入清单，便于把成品包与导出快照对照。
	SnapshotHash string
	// Pages 是逐页动画素材，按 slide 位置对齐；某项为零值表示该页没有动画。
	//
	// 分层位图由调用方（app 层）算好再传进来，打包器不解释动画语义——
	// 它只负责「把这些字节写进包、把这些元数据写进清单」。这样 share 不必依赖
	// render（也就不会把 animation 的语义带进一个只管打包的包）。
	Pages []PageAnimation
	// CreatedAt 为零值时省略。
	CreatedAt time.Time
}

// PageAnimation 是一页的动画素材。
//
// 为什么图像在调用方算：挖空要基于**原始页图**取色（转码后再挖，填色会偏），
// 而打包器拿到的是已经 Decide 好怎么转的字节流。谁先谁后是个容易写反的顺序，
// 放在调用方一处写清楚，比在打包器里埋一个隐含前提好。
type PageAnimation struct {
	// PlatePNG 是挖空元素本体后的整页底图（PNG 字节）。为空表示不挖空。
	PlatePNG   []byte
	Transition SlideTransition
	// Layers 按绘制顺序（父在前、子在后），与 IR 同序。
	Layers []LayerAsset
	// Focus 是页级焦点序列，原样透传到清单。
	Focus []SlideFocus
}

// LayerAsset 是一个元素层的独立位图：已按 Box 裁切，且不含其它动画元素的像素。
type LayerAsset struct {
	ElementID string
	Kind      string
	Box       SlideBox
	// PNG 是该层的位图字节，尺寸等于 Box。
	PNG []byte
	// Origin 是缩放锚点（center/bottom/…），与 IR 的 layer.origin 同义。
	Origin string
	Clips  []Clip
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
	zw := zip.NewWriter(out)
	manifest, files, err := b.buildInto(ctx, in, func(name string, data []byte) error {
		w, createErr := zw.Create(name)
		if createErr != nil {
			return createErr
		}
		_, writeErr := w.Write(data)
		return writeErr
	})
	if err != nil {
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

// buildInto 是打包的唯一定义：把清单、字幕、底图、音频与播放器按固定顺序交给 write。
//
// 只依赖「写一个条目」这一个动作，因此 zip（Build）与目录（BuildDir）共用同一份
// 组装逻辑与降级语义——这是隐私模式与云端产物结构一致的实现基础（tasks 5.3）。
func (b *BundleBuilder) buildInto(
	ctx context.Context,
	in BundleInput,
	write func(name string, data []byte) error,
) (*Manifest, map[string]struct{}, error) {
	if b == nil || b.reader == nil {
		return nil, nil, errors.New("share: resource reader is required")
	}
	if in.Timeline == nil || len(in.Timeline.Slides) == 0 {
		return nil, nil, errors.New("share: timeline with at least one slide is required")
	}
	if strings.TrimSpace(in.VTTKey) == "" {
		return nil, nil, errors.New("share: vtt key is required (the bundle must ship subtitles)")
	}
	timeline := in.Timeline

	manifest := newManifest(timeline, in.Width, in.Height, in.FPS, in.Title, in.SnapshotHash, in.CreatedAt)

	files := make(map[string]struct{}, len(timeline.Slides)+8)
	emit := func(name string, data []byte) error {
		if err := checkBundlePath(name); err != nil {
			return err
		}
		// 自包含自检（V14）：外链必须在打包时失败，而不是等断网的客户看到白屏。
		// 挂在唯一出口上，新增条目类型自动被覆盖。
		if err := checkExternalRefs(name, data); err != nil {
			return err
		}
		if err := write(name, data); err != nil {
			return err
		}
		files[name] = struct{}{}
		return nil
	}

	// ---- 时间轴（与旧 web_project 同名条目，便于下游平滑迁移解析）----
	timelineJSON, err := json.Marshal(timeline)
	if err != nil {
		return nil, nil, fmt.Errorf("share: marshal timeline: %w", err)
	}
	if err := emit(FileTimelineJSON, timelineJSON); err != nil {
		return nil, nil, err
	}

	// ---- 字幕 ----
	vtt, err := b.readObject(ctx, in.VTTKey)
	if err != nil {
		return nil, nil, fmt.Errorf("share: read vtt: %w", err)
	}
	if len(vtt) == 0 {
		return nil, nil, fmt.Errorf("share: vtt object %q is empty", in.VTTKey)
	}
	if err := emit(FileSubtitlesVTT, vtt); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(in.SRTKey) != "" {
		srt, srtErr := b.readObject(ctx, in.SRTKey)
		if srtErr != nil {
			return nil, nil, fmt.Errorf("share: read srt: %w", srtErr)
		}
		if err := emit(FileSubtitlesSRT, srt); err != nil {
			return nil, nil, err
		}
	}
	subtitlesJS, err := subtitlesScriptBody(timeline.Subtitles)
	if err != nil {
		return nil, nil, fmt.Errorf("share: marshal subtitles: %w", err)
	}
	if err := emit(FileSubtitlesJS, subtitlesJS); err != nil {
		return nil, nil, err
	}

	// ---- 页面底图 ----
	slides, err := b.writeSlides(ctx, emit, in)
	if err != nil {
		return nil, nil, err
	}
	manifest.Slides = slides

	// ---- 音频 ----
	audioEntries, mixName, err := b.writeAudio(ctx, emit, timeline)
	if err != nil {
		return nil, nil, err
	}
	manifest.Audio = audioEntries
	manifest.AudioMix = mixName

	// ---- 播放器与页面 ----
	appJS, err := assets.AppJS()
	if err != nil {
		return nil, nil, fmt.Errorf("share: embedded player script: %w", err)
	}
	appCSS, err := assets.AppCSS()
	if err != nil {
		return nil, nil, fmt.Errorf("share: embedded player stylesheet: %w", err)
	}
	if err := emit(FileAppJS, appJS); err != nil {
		return nil, nil, err
	}
	if err := emit(FileAppCSS, appCSS); err != nil {
		return nil, nil, err
	}
	indexHTML, err := renderIndexHTML(in.Title, manifest.Slides, timeline.Subtitles)
	if err != nil {
		return nil, nil, fmt.Errorf("share: render index.html: %w", err)
	}
	if err := emit(FileIndexHTML, indexHTML); err != nil {
		return nil, nil, err
	}

	// ---- 清单（最后写：文件名在转码后才知道）----
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("share: marshal manifest: %w", err)
	}
	if err := emit(FileManifestJSON, append(manifestJSON, '\n')); err != nil {
		return nil, nil, err
	}
	manifestJS, err := manifest.scriptBody()
	if err != nil {
		return nil, nil, fmt.Errorf("share: marshal manifest script: %w", err)
	}
	if err := emit(FileManifestJS, manifestJS); err != nil {
		return nil, nil, err
	}

	return manifest, files, nil
}

// writeSlides 逐页转码并写 entry，返回清单条目。
//
// 降级语义与 MP4 路径逐条对齐：缺键/读失败 → 沿用上一页（复用同一文件名，
// 播放器切页时 src 不变，等于"画面停住"）；首屏缺失 → 占位图。
//
// 有动画的页是例外：它的 `image` 是挖空底板，**不能**被后续缺图页沿用——
// 沿用会让用户看到一块纯色疤，而且看不到任何错误提示。所以「上一页是底板」时
// 退到占位图，宁可空白也不要一张有疤的图。
func (b *BundleBuilder) writeSlides(
	ctx context.Context,
	write func(string, []byte) error,
	in BundleInput,
) ([]SlideEntry, error) {
	entries := make([]SlideEntry, 0, len(in.Timeline.Slides))
	previousName := ""
	previousPlate := false
	for i, slide := range in.Timeline.Slides {
		anim := PageAnimation{}
		if i < len(in.Pages) {
			anim = in.Pages[i]
		}

		// 有分层素材的页：底板 + 每层位图，全部走 PNG（层图是文字时 JPEG 会糊边，
		// 且实测只省 29%，见 design ADR-5）。
		if len(anim.Layers) > 0 && len(anim.PlatePNG) > 0 {
			name := fmt.Sprintf(slideEntryPattern, i+1, ".png")
			if err := write(name, anim.PlatePNG); err != nil {
				return nil, err
			}
			layers := make([]SlideLayer, 0, len(anim.Layers))
			for k, layer := range anim.Layers {
				layerName := fmt.Sprintf(layerEntryPattern, i+1, k)
				if err := write(layerName, layer.PNG); err != nil {
					return nil, err
				}
				layers = append(layers, SlideLayer{
					ElementID: layer.ElementID, Kind: layer.Kind,
					Box: layer.Box, Image: layerName, Origin: layer.Origin, Clips: layer.Clips,
				})
			}
			transition := anim.Transition
			entries = append(entries, SlideEntry{
				SlideID: slide.SlideID, StartUS: slide.StartUS, EndUS: slide.EndUS, Image: name,
				Plate: true, Transition: &transition, Layers: layers, Focus: anim.Focus,
			})
			previousName, previousPlate = name, true
			continue
		}

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
			raw, readErr := b.readPageBytes(ctx, in, i, rawKey)
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
		// 沿用上一页时排除底板：底板是挖空的，拿来当完整页会看到纯色疤。
		if name == "" && previousName != "" && !previousPlate {
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
		previousName, previousPlate = name, false
		entries = append(entries, SlideEntry{
			SlideID: slide.SlideID, StartUS: slide.StartUS, EndUS: slide.EndUS, Image: name,
		})
	}
	return entries, nil
}

// writeAudio 写音频条目，返回清单条目与混音轨条目名（空表示没拼成）。
//
// 混音轨拼成时**不再保留逐段文件**：mix 已经包含全部采样，再存一份逐段等于
// 音频体积翻倍（实测 30 秒包：音频 2.75 MiB 占 80%，砍掉重复后降到 1.4 MiB）。
// 此时 `audio[].src` 统一指向 mix——条目语义从"这一段自己的音频"变成
// "这段讲稿在混音轨上的区间"，区间字段仍然准确，播放器在 mix 模式下也不读 src。
//
// 拼不成（素材非 PCM16、时长不符）时退回逐段文件 + 逐段播放：此时体积换可用性，
// 因为"有声音但多一份"显然好过"没声音"。
//
// 音频缺失必须失败：没有声音的"讲解成品"没有意义（design D8）。
func (b *BundleBuilder) writeAudio(
	ctx context.Context,
	write func(string, []byte) error,
	timeline *media.Timeline,
) ([]AudioEntry, string, error) {
	// 先读齐：能不能拼成混音轨，决定了要不要写逐段文件。
	clips := make(map[string][]byte)
	var order []string
	for _, slide := range timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := clips[segment.AudioKey]; ok {
				continue
			}
			if strings.TrimSpace(segment.AudioKey) == "" {
				return nil, "", fmt.Errorf("share: segment %q has no audio key", segment.SegmentID)
			}
			data, err := b.readObject(ctx, segment.AudioKey)
			if err != nil {
				return nil, "", fmt.Errorf("share: read audio segment %q: %w", segment.AudioKey, err)
			}
			if len(data) == 0 {
				return nil, "", fmt.Errorf("share: audio segment %q is empty", segment.AudioKey)
			}
			clips[segment.AudioKey] = data
			order = append(order, segment.AudioKey)
		}
	}

	names := make(map[string]string, len(order))
	mixName := ""
	var mix bytes.Buffer
	if len(order) > 0 {
		if mixErr := media.WriteTimelineWAV(&mix, timeline, clips); mixErr != nil {
			b.logf(slog.LevelWarn, "share: assemble audio mix failed, falling back to per-segment playback", "error", mixErr)
		} else if err := write(FileAudioMix, mix.Bytes()); err != nil {
			return nil, "", err
		} else {
			mixName = FileAudioMix
		}
	}

	if mixName != "" {
		for _, key := range order {
			names[key] = mixName
		}
	} else {
		for i, key := range order {
			name := fmt.Sprintf(audioEntryPattern, i+1)
			if err := write(name, clips[key]); err != nil {
				return nil, "", err
			}
			names[key] = name
		}
	}

	entries := make([]AudioEntry, 0)
	for _, slide := range timeline.Slides {
		for _, segment := range slide.Segments {
			entries = append(entries, AudioEntry{
				SegmentID: segment.SegmentID, Src: names[segment.AudioKey],
				StartUS: segment.StartUS, EndUS: segment.EndUS,
			})
		}
	}
	return entries, mixName, nil
}

func (b *BundleBuilder) readPagePNG(ctx context.Context, key string) ([]byte, error) {
	if err := checkResourceKey(key); err != nil {
		return nil, err
	}
	return readAllClose(b.reader.OpenPagePNG(ctx, key))
}

// readPageBytes 取一页的原始 PNG 字节：优先用调用方已读入的副本，否则回读。
//
// 副本路径**仍然要过 checkResourceKey**：key 的合法性是数据契约，与字节从哪来无关。
// 若因为「反正有字节了」就跳过校验，非法 key 会以另一种方式进到清单里。
func (b *BundleBuilder) readPageBytes(ctx context.Context, in BundleInput, index int, key string) ([]byte, error) {
	if index < len(in.PagePNGBytes) && len(in.PagePNGBytes[index]) > 0 {
		if err := checkResourceKey(key); err != nil {
			return nil, err
		}
		return in.PagePNGBytes[index], nil
	}
	return b.readPagePNG(ctx, key)
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
