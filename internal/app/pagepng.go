package app

import (
	"context"
	"encoding/json"
	"io"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
)

// AlignPagePNGKeys 按时间轴页序补齐页面 PNG 的对象键。
//
// supplied 是导出请求里已经带上的键，可以是空、比页数短，或缺页位置是空串。
// 非空的位置保持不动。空位用这一源版本的 render/pages.json 按 slideId 填上。
//
// 解析任务和成功步骤会被保留策略清掉，页图和清单还在对象存储里。只认任务引用时，
// 导出快照里的 pagePngKeys 全是空的，成品就只剩语音和一块深色占位，看起来像「没有 PPT」。
// 清单缺失、渲染器不可用，或清单上的版本号对不上时，不拿别的版本的图来顶。
func AlignPagePNGKeys(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revisionNo int, slides []media.SlideCue, supplied []string) []string {
	out := make([]string, len(slides))
	for i := range slides {
		if i < len(supplied) {
			out[i] = supplied[i]
		}
	}
	if revisionNo <= 0 || objects == nil || !pageKeysNeedFill(out) {
		return out
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID, Revision: srcRevString(revisionNo),
		AssetType: "render", AssetID: "pages", Ext: "json",
	}
	rc, _, err := objects.Get(ctx, key)
	if err != nil {
		return out
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return out
	}
	var manifest PageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return out
	}
	if manifest.Renderer == RendererUnavailable {
		return out
	}
	if manifest.RevisionNo > 0 && manifest.RevisionNo != revisionNo {
		return out
	}
	bySlide := make(map[string]string, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID != "" && pg.Key != "" {
			bySlide[pg.SlideID] = pg.Key
		}
	}
	for i, slide := range slides {
		if out[i] == "" {
			out[i] = bySlide[slide.SlideID]
		}
	}
	return out
}

func snapshotNeedsPageImages(format artifact.Format) bool {
	switch format {
	case artifact.FormatMP4, artifact.FormatHTMLBundle, artifact.FormatHTMLSingle:
		return true
	default:
		return false
	}
}

func pageKeysNeedFill(keys []string) bool {
	for _, key := range keys {
		if key == "" {
			return true
		}
	}
	return false
}
