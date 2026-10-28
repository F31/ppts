package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/F31/ppts/internal/share"
)

// MethodExportBundle 是隐私模式下的本地打包方法。
const MethodExportBundle = "export_bundle"

// ExportBundleRequest 是本地打包请求。
//
// 所有路径必须是绝对路径：sidecar 由 Tauri 拉起，工作目录不受调用方控制，
// 相对路径会让"导出到哪"变得不可预测。资源 key（PagePNGKeys/VTTKey/SRTKey 与
// 时间轴里的 AudioKey）则一律相对 Root，且 MUST NOT 是 URL——本地路径不解析网络地址。
type ExportBundleRequest struct {
	Root        string   `json:"root"`
	OutDir      string   `json:"outDir"`
	TimelineKey string   `json:"timelineKey,omitempty"`
	VTTKey      string   `json:"vttKey"`
	SRTKey      string   `json:"srtKey,omitempty"`
	PagePNGKeys []string `json:"pagePngKeys"`
	Title       string   `json:"title,omitempty"`
	Width       int      `json:"width,omitempty"`
	Height      int      `json:"height,omitempty"`
	FPS         int      `json:"fps,omitempty"`
}

// ExportBundleResult 是打包结果摘要（不含文件内容：包已经在磁盘上了）。
type ExportBundleResult struct {
	OutDir        string `json:"outDir"`
	Entry         string `json:"entry"`
	Slides        int    `json:"slides"`
	AudioSegments int    `json:"audioSegments"`
	AudioMix      bool   `json:"audioMix"`
	DurationUS    int64  `json:"durationUs"`
	Bytes         int64  `json:"bytes"`
}

// methodExportBundle 在本地生成成品包：只读 Root、只写 OutDir，不接触对象存储、
// 不发起网络请求（tasks 5.2）。打包逻辑与云端完全共用 share 包，结构因此不会漂移。
func methodExportBundle(ctx context.Context, req *Request) (any, error) {
	if len(req.Payload) == 0 {
		return nil, invalidRequestf("export_bundle requires a payload")
	}
	var in ExportBundleRequest
	if err := json.Unmarshal(req.Payload, &in); err != nil {
		return nil, invalidRequestf("export_bundle payload: %v", err)
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	timelineKey := in.TimelineKey
	if strings.TrimSpace(timelineKey) == "" {
		timelineKey = "timeline.json"
	}
	timeline, err := share.ReadLocalTimeline(in.Root, timelineKey)
	if err != nil {
		return nil, classifyLocalError(err)
	}

	manifest, err := share.BuildLocalToDir(ctx, share.LocalBundle{
		Root: in.Root,
		Input: share.BundleInput{
			Timeline:    timeline,
			PagePNGKeys: in.PagePNGKeys,
			SRTKey:      in.SRTKey,
			VTTKey:      in.VTTKey,
			Width:       in.Width,
			Height:      in.Height,
			FPS:         in.FPS,
			Title:       in.Title,
		},
	}, in.OutDir)
	if err != nil {
		return nil, classifyLocalError(err)
	}

	size, err := dirSize(in.OutDir)
	if err != nil {
		return nil, err
	}
	return ExportBundleResult{
		OutDir:        in.OutDir,
		Entry:         share.FileIndexHTML,
		Slides:        len(manifest.Slides),
		AudioSegments: len(manifest.Audio),
		AudioMix:      manifest.AudioMix != "",
		DurationUS:    manifest.Artifact.DurationUS,
		Bytes:         size,
	}, nil
}

func (r ExportBundleRequest) validate() error {
	if strings.TrimSpace(r.Root) == "" {
		return invalidRequestf("export_bundle: root is required")
	}
	if strings.TrimSpace(r.OutDir) == "" {
		return invalidRequestf("export_bundle: outDir is required")
	}
	for name, p := range map[string]string{"root": r.Root, "outDir": r.OutDir} {
		if !filepath.IsAbs(p) {
			return invalidRequestf("export_bundle: %s must be an absolute path, got %q", name, p)
		}
	}
	if strings.TrimSpace(r.VTTKey) == "" {
		return invalidRequestf("export_bundle: vttKey is required")
	}
	return nil
}

// classifyLocalError 把"调用方传错了"与"引擎内部失败"分开：
// 目录非空、文件缺失属于前者，客户端据此提示用户换目录/补素材，重试同样请求无意义。
func classifyLocalError(err error) error {
	switch {
	case errors.Is(err, share.ErrOutDirNotEmpty):
		return invalidRequestf("%v", err)
	case errors.Is(err, fs.ErrNotExist):
		return invalidRequestf("%v", err)
	default:
		return fmt.Errorf("export_bundle: %w", err)
	}
}

// dirSize 统计目录总字节数，用于结果摘要与体积基线（tasks 6.7）。
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("export_bundle: measure output: %w", err)
	}
	return total, nil
}

// 编译期确认方法签名与分发表一致。
var _ func(context.Context, *Request) (any, error) = methodExportBundle
