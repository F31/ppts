package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// packWebProject 打包可离线播放的 Web 讲解工程：timeline.json + 字幕 + 各音频片段。
//
// 流式实现：zip 写入临时文件，音频片段逐个 io.Copy 进 zip entry（绝不整体载入内存），
// 返回 *os.File 供调用方流式上传后关闭。大 deck 的音频总量可能数十 MB，整体进 bytes.Buffer
// 会让 worker 峰值内存随音频总量线性增长。
func (h *ExportHandler) packWebProject(ctx context.Context, tenantID string, bundle *TimelineAsset) (*os.File, error) {
	tmp, err := os.CreateTemp("", "ppts-webproject-*.zip")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	zw := zip.NewWriter(tmp)
	addBytes := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		cleanup()
		return nil, err
	}
	srt, err := h.readTenantObject(ctx, tenantID, bundle.SRTKey)
	if err != nil {
		cleanup()
		return nil, err
	}
	vtt, err := h.readTenantObject(ctx, tenantID, bundle.VTTKey)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("timeline.json", timelineJSON); err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("subtitles.srt", srt); err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("subtitles.vtt", vtt); err != nil {
		cleanup()
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := seen[segment.AudioKey]; ok {
				continue
			}
			seen[segment.AudioKey] = struct{}{}
			// 音频逐个流式写入 zip：先建 entry，再从对象存储流式拷贝，不整段驻留内存。
			name := "audio/" + segment.AudioKey[strings.LastIndex(segment.AudioKey, "/")+1:]
			w, err := zw.Create(name)
			if err != nil {
				cleanup()
				return nil, err
			}
			key, perr := objectstore.Parse(segment.AudioKey)
			if perr != nil {
				cleanup()
				return nil, perr
			}
			if perr := key.EnsureTenant(tenantID); perr != nil {
				cleanup()
				return nil, perr
			}
			r, _, gerr := h.objects.Get(ctx, key)
			if gerr != nil {
				cleanup()
				return nil, gerr
			}
			_, cerr := io.Copy(w, r)
			r.Close()
			if cerr != nil {
				cleanup()
				return nil, cerr
			}
		}
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return tmp, nil
}
