package app

import (
	"context"
	"sync"

	"github.com/F31/ppts/internal/media"
)

// exportObjectFetchLimit 是一次导出里对象存储 GET 的并发上限。
// 这台生产机 4vCPU / 3.6G，拉页图和配音若串行，墙钟会叠在渲染之外；
// 4 路已经能把往返叠起来，再高会跟渲染器抢网卡和内存。
const exportObjectFetchLimit = 4

type objectFetch struct {
	data []byte
	err  error
}

// fetchTenantObjects 按 keys 顺序返回内容。空键对应 nil；同一键只拉一次。
// 失败写在 errs[i]，不中断其它键。
func (h *ExportHandler) fetchTenantObjects(ctx context.Context, tenantID string, keys []string, limit int) (data [][]byte, errs []error) {
	data = make([][]byte, len(keys))
	errs = make([]error, len(keys))
	if h == nil || h.objects == nil || len(keys) == 0 {
		return data, errs
	}
	if limit < 1 {
		limit = 1
	}
	uniq := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, k)
	}
	if len(uniq) == 0 {
		return data, errs
	}

	got := make(map[string]objectFetch, len(uniq))
	var mu sync.Mutex
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, key := range uniq {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				mu.Lock()
				got[key] = objectFetch{err: ctx.Err()}
				mu.Unlock()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			body, err := h.readTenantObject(ctx, tenantID, key)
			mu.Lock()
			got[key] = objectFetch{data: body, err: err}
			mu.Unlock()
		}(key)
	}
	wg.Wait()

	for i, key := range keys {
		if key == "" {
			continue
		}
		f := got[key]
		data[i] = f.data
		errs[i] = f.err
	}
	return data, errs
}

// loadExportPagePNGs 按时间轴页序取页图。缺图或读失败沿用上一页，首屏用深色占位。
func (h *ExportHandler) loadExportPagePNGs(ctx context.Context, tenantID string, keys []string, slides, width, height int) [][]byte {
	pageKeys := make([]string, slides)
	for i := 0; i < slides; i++ {
		if i < len(keys) {
			pageKeys[i] = keys[i]
		}
	}
	fetched, _ := h.fetchTenantObjects(ctx, tenantID, pageKeys, exportObjectFetchLimit)
	out := make([][]byte, 0, slides)
	var prev []byte
	for i := 0; i < slides; i++ {
		if pageKeys[i] != "" && len(fetched[i]) > 0 {
			prev = fetched[i]
			out = append(out, prev)
			continue
		}
		if prev == nil {
			prev = blankPagePNG(width, height)
		}
		out = append(out, prev)
	}
	return out
}

// loadExportAudioClips 拉时间轴上出现过的配音对象。任一失败整单失败。
func (h *ExportHandler) loadExportAudioClips(ctx context.Context, tenantID string, slides []media.SlideCue) (map[string][]byte, error) {
	keys := make([]string, 0)
	seen := map[string]struct{}{}
	for _, slide := range slides {
		for _, segment := range slide.Segments {
			if segment.AudioKey == "" {
				continue
			}
			if _, ok := seen[segment.AudioKey]; ok {
				continue
			}
			seen[segment.AudioKey] = struct{}{}
			keys = append(keys, segment.AudioKey)
		}
	}
	fetched, errs := h.fetchTenantObjects(ctx, tenantID, keys, exportObjectFetchLimit)
	clips := make(map[string][]byte, len(keys))
	for i, key := range keys {
		if errs[i] != nil {
			return nil, errs[i]
		}
		clips[key] = fetched[i]
	}
	return clips, nil
}
