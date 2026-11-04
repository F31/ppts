package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// 本文件回答「导出侧的元素几何从哪来」（tasks 2.0b）。
//
// 结论：不需要新增产物。解析期已经把整份 extracted document 落成了
// `<tenant>/<project>/src-NN/document/extracted.json`，其中 `Shape.Bounds` 就是
// Inspect 时从 go-pptx 取到的 EMU 框。导出期按源版本读一次即可，
// 既不增加存储，也不重解析 PPTX。
//
// 为什么不做「导出期重解析」：重解析要拿源对象（几十 MB）并跑一遍 go-pptx，
// 在 worker 上是一秒级的额外 CPU 与内存峰值，而收益与读一份已经算好的 JSON 完全相同。
// 为什么不担心「解析产物与 PPTX 不一致」：这份 document 正是讲稿与编辑器看到的那一份，
// 用它反而是唯一自洽的选择——几何若来自第二次解析，用户改了源版本后
// 「编辑器里的第 2 条要点」与「成片里动的那条」可能不是同一条。

// PageGeometryScope 定位一个源版本的页文档。
type PageGeometryScope struct {
	TenantID         string
	ProjectID        string
	SourceRevisionNo int
}

// PageGeometrySource 返回某个源版本里**所有页**的元素几何（slideID → 元素）。
//
// 一次取全量而不是按页取：整份 document.json 是一次对象读，
// 逐页调用的接口会把它变成 N 次往返（S3 上就是 N 个 RTT）。
type PageGeometrySource interface {
	ElementsBySlide(ctx context.Context, scope PageGeometryScope) (map[string][]render.Element, error)
}

// FocusBox 是一个可作焦点目标的矩形（EMU，页坐标）。
//
// 与 render.Element 分开：元素是"能挂动效的层"，焦点目标可以是**不是层**的东西
// （图表里的某一根柱子）。把柱子伪装成元素会污染元素表——渲染侧会给它找轨道、
// 找不到就报 anchor_missing，而它本来就不该有轨道。
type FocusBox struct {
	ShapeID string
	// BarID 空 = 整个形状的包围盒；非空 = 该形状 chartBars[] 里某根柱子的框。
	BarID string
	Box   render.EMUBox
}

// PageFocusSource 返回某个源版本里所有可作焦点的矩形（slideID → 目标）。
//
// 单独一个接口而不是塞进 PageGeometrySource：预览只需要元素（它没有时间轴，也就
// 没有焦点序列），强制它实现一个用不上的方法等于让每个测试假实现都多写一段空壳。
type PageFocusSource interface {
	FocusBoxes(ctx context.Context, scope PageGeometryScope) (map[string][]FocusBox, error)
}

// DocumentGeometry 从解析产物读取元素几何。
type DocumentGeometry struct {
	objects objectstore.ObjectStore
}

// NewDocumentGeometry 用对象存储构造几何来源。objects 为 nil 时 ElementsBySlide 返回错误
// （调用方据此只编整页轨道），不在启动时 panic：几何缺失是可降级的能力边界。
func NewDocumentGeometry(objects objectstore.ObjectStore) *DocumentGeometry {
	return &DocumentGeometry{objects: objects}
}

// ElementsBySlide 实现 PageGeometrySource。
func (g *DocumentGeometry) ElementsBySlide(ctx context.Context, scope PageGeometryScope) (map[string][]render.Element, error) {
	doc, err := g.load(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]render.Element, len(doc.Pages))
	for _, page := range doc.Pages {
		if page == nil || page.SlideID == "" {
			continue
		}
		out[page.SlideID] = PageElements(page)
	}
	return out, nil
}

// FocusBoxes 实现 PageFocusSource：形状框 + 每根柱子的框。
//
// 会再读一次 document.json（与 ElementsBySlide 各读一次）。这是刻意的取舍：把两份
// 结果塞进一个"顺带返回"的结构会让每个只想要元素的调用方（预览）背上一份用不上的
// 数据；而这里多出来的成本是一次对象读，导出本来就只发生一次。真变贵了再说，先别
// 让接口变成"读什么都给"。
func (g *DocumentGeometry) FocusBoxes(ctx context.Context, scope PageGeometryScope) (map[string][]FocusBox, error) {
	doc, err := g.load(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]FocusBox, len(doc.Pages))
	for _, page := range doc.Pages {
		if page == nil || page.SlideID == "" {
			continue
		}
		var boxes []FocusBox
		var walk func([]*project.Shape)
		walk = func(shapes []*project.Shape) {
			for _, sh := range shapes {
				if sh == nil || sh.ID == "" {
					continue
				}
				if sh.Bounds != nil {
					boxes = append(boxes, FocusBox{
						ShapeID: sh.ID,
						Box: render.EMUBox{
							X: sh.Bounds.X, Y: sh.Bounds.Y,
							Width: sh.Bounds.Width, Height: sh.Bounds.Height,
						},
					})
				}
				// 柱子的框来自解析期的切柱结果。注意它的单位是**解析画布像素**
				// （1920×1080，见 bindCanvasW），不是 EMU——这里换回 EMU，让下游只需要
				// 一个换算系数（画布高 / 标准页高），不必按目标类型记两套坐标。
				//
				// 这个反推成立的前提与切柱时相同：页面高度按标准 7.5 英寸（6858000 EMU）
				// 折算。自定义页高的 deck 在切柱那一步就已经"切不出来"（报未切），
				// 所以不会走到这里拿到一个假的框。
				for _, bar := range sh.ChartBars {
					if bar.ID == "" {
						continue
					}
					boxes = append(boxes, FocusBox{
						ShapeID: sh.ID, BarID: bar.ID,
						Box: render.EMUBox{
							X: pxToEMU(bar.Box[0]), Y: pxToEMU(bar.Box[1]),
							Width: pxToEMU(bar.Box[2]), Height: pxToEMU(bar.Box[3]),
						},
					})
				}
				if len(sh.Children) > 0 {
					walk(sh.Children)
				}
			}
		}
		walk(page.Shapes)
		out[page.SlideID] = boxes
	}
	return out, nil
}

// load 读取并解码某个源版本的 document.json。
func (g *DocumentGeometry) load(ctx context.Context, scope PageGeometryScope) (*project.Document, error) {
	if g == nil || g.objects == nil {
		return nil, errors.New("animation: geometry source has no object store")
	}
	if scope.SourceRevisionNo <= 0 {
		// 0 表示上游没给出源版本身份。此时读 src-00 会读到「上一次解析到第 0 版时的内容」，
		// 也就是一个可能与当前讲稿毫无关系的旧文档——宁可不给几何。
		return nil, fmt.Errorf("animation: geometry scope has no source revision (project %q)", scope.ProjectID)
	}
	key := objectstore.ObjectKey{
		TenantID: scope.TenantID, ProjectID: scope.ProjectID,
		Revision: srcRevString(scope.SourceRevisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := g.objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("animation: read extracted document: %w", err)
	}
	defer rc.Close()

	// 读全量再 Unmarshal（而不是 Decoder.Decode）：Decode 会忽略尾部多余字节，
	// 一份被截断/拼接坏的文档会「成功」解析出半页形状，几何随之静默错位。
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("animation: read extracted document body: %w", err)
	}
	var doc project.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("animation: decode extracted document: %w", err)
	}
	return &doc, nil
}

// PageElements 把一页的元素转成渲染几何。
//
// 这是**唯一**一处「形状 → 元素」的换算，预览与导出共用：
// 预览若自己再遍历一遍，两条路径迟早会在顺序/分类上分叉，
// 表现是「预览动的元素，导出没动」——而两边的输入明明是一样的。
//
// 顺序与分类来自 animdefault.Anchors（与「按预设生成默认规格」同一套：阅读顺序，
// 第一个文字元素算标题）。没有包围盒的元素直接跳过：渲染侧无法对它做位移/缩放，
// 留下只会得到一条 anchor_missing 诊断。
func PageElements(page *project.Page) []render.Element {
	if page == nil {
		return nil
	}
	bounds := make(map[string]*project.Box)
	var walk func([]*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" {
				continue
			}
			if sh.Bounds != nil {
				bounds[sh.ID] = sh.Bounds
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	anchors := animdefault.Anchors(page)
	out := make([]render.Element, 0, len(anchors))
	for _, a := range anchors {
		box := bounds[a.ID]
		if box == nil {
			continue
		}
		out = append(out, render.Element{
			ID:   a.ID,
			Kind: a.Kind,
			Bounds: &render.EMUBox{
				X: box.X, Y: box.Y, Width: box.Width, Height: box.Height,
			},
		})
	}
	return out
}
