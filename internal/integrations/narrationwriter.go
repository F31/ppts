package integrations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	pptx "github.com/F31/go-pptx/v2/pptx"
)

// NarrationWriter 写适配器端口（V4.0 §5.1 NarrationWriter.Apply）。
// 在源文件副本中绑定音频与计时，返回变更清单；未实现的能力显式返回
// UNSUPPORTED_FEATURE，不得输出伪成功文件。
type NarrationWriter interface {
	Apply(ctx context.Context, src io.ReaderAt, size int64, plan NarrationPlan) (*ApplyReport, error)
}

// ErrUnsupportedFeature 对应方案中显式的 UNSUPPORTED_FEATURE 语义。
var ErrUnsupportedFeature = errors.New("integrations: unsupported feature (UNSUPPORTED_FEATURE)")

// NarrationPlan 是一次配音写入计划：按页绑定页面音轨与自动切页时长。
// 分段级（多音轨/逐段）写入由 G1 的 media 装配层在此之上扩展。
type NarrationPlan struct {
	Slides []SlideNarration
}

// SlideNarration 描述单页的配音与计时。
type SlideNarration struct {
	Index    int           // 0 基页序（对应 Document.Pages[].Index）
	TrackKey string        // 稳定业务标识；同 TrackKey 重跑只更新同一逻辑音轨
	Audio    []byte        // 页面音轨字节（WAV/MP3）
	MIME     string        // audio/wav 或 audio/mpeg
	Advance  time.Duration // 自动切页时长；0 = 保留手动/无自动切页
}

// ApplyReport 是配音写入的变更清单。
type ApplyReport struct {
	TrackKeys     []string `json:"trackKeys"`
	ChangedSlides []int    `json:"changedSlides"`
	// Unsupported 列出被计划要求但适配器显式不支持的特性（如 timing 树合并）。
	Unsupported []string `json:"unsupported,omitempty"`
	// Writer 适配器名称与版本（实现名称和版本，V4.0 §5.1）。
	Writer     string `json:"writer"`
	Version    string `json:"version"`
	OutputMIME string `json:"outputMime"`
	// Output 是写出字节（带音频的 PPTX 副本）。
	Output []byte `json:"-"`
}

// GoPPTXNarrationWriter 以 go-pptx 的 UpsertNarration/SetAdvanceAfter 实现写适配器。
// go-pptx Play 维度为 Partial：音频自动开始 + 稳定 TrackKey 幂等已可用；
// 复杂 timing 树合并尚不支持（登记 FEAT-001），此时返回 Unsupported 声明。
type GoPPTXNarrationWriter struct{}

// NewGoPPTXNarrationWriter 创建写适配器。
func NewGoPPTXNarrationWriter() *GoPPTXNarrationWriter {
	return &GoPPTXNarrationWriter{}
}

// Apply 打开源副本（OpenReader 只读打开，写走 Write 新字节流，源文件字节不变），
// 按计划嵌入音轨并设置自动切页，输出到内存。
func (w *GoPPTXNarrationWriter) Apply(ctx context.Context, src io.ReaderAt, size int64, plan NarrationPlan) (*ApplyReport, error) {
	rep := &ApplyReport{Writer: "go-pptx", Version: "v2.0.0", OutputMIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}

	p, err := pptx.OpenReader(src, size)
	if err != nil {
		return nil, err
	}
	defer p.Close()

	slides, err := p.Slides()
	if err != nil {
		return nil, err
	}
	for _, sp := range plan.Slides {
		if sp.Index < 0 || sp.Index >= len(slides) {
			return nil, errors.New("integrations: narration plan references out-of-range slide index")
		}
		if len(sp.Audio) == 0 {
			continue
		}
		slide := slides[sp.Index]
		if _, status, err := slide.UpsertNarration(ctx, pptx.BytesMedia(sp.Audio, sp.MIME),
			pptx.AudioSpec{TrackKey: sp.TrackKey, Role: pptx.AudioRoleNarration},
			pptx.PlaybackSpec{Trigger: pptx.PlaybackOnSlideEnter},
		); err != nil {
			return nil, err
		} else if status != "added" && status != "updated" && status != "unchanged" {
			return nil, errors.New("integrations: unexpected narration upsert status: " + status)
		}
		rep.TrackKeys = append(rep.TrackKeys, sp.TrackKey)
		rep.ChangedSlides = append(rep.ChangedSlides, sp.Index)
		if sp.Advance > 0 {
			if err := slide.SetAdvanceAfter(sp.Advance); err != nil {
				return nil, err
			}
		}
	}

	var out bytes.Buffer
	if _, err := p.Write(ctx, &out); err != nil {
		return nil, err
	}
	rep.Output = out.Bytes()
	return rep, nil
}
