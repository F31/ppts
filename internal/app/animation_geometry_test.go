package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// seedDocument 把一份 extracted document 落到与解析期完全相同的键上。
// 键写错的话几何会永远读不到，而失败只表现为「元素级动效静默消失」——
// 所以用例直接照抄 ingest.go 的 key 构造，而不是另造一个更好记的路径。
func seedDocument(t *testing.T, objects objectstore.ObjectStore, tenantID, projectID string, revNo int, doc project.Document) {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	putObject(t, objects, objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}, data, "application/json")
}

func geometryDocument() project.Document {
	box := func(x, y, w, h int64) *project.Box { return &project.Box{X: x, Y: y, Width: w, Height: h} }
	return project.Document{
		SchemaVersion: project.SchemaVersion,
		Pages: []*project.Page{
			{
				Index: 0, SlideID: "s1",
				Shapes: []*project.Shape{
					// 故意把要点写在前面：几何的顺序必须由阅读顺序（Y 小在前）决定，
					// 而不是 JSON 里的先后——那就成了「文件里怎么排，动效就怎么排」。
					// 尺寸用真实 EMU（13.33in 宽页）：几十 EMU 的框换算成像素是 0，
					// 编译期会以「无可用包围盒」跳过，用例就测不到自己要测的东西。
					{ID: "sh-bullet", Kind: "text", Text: "第二条", Bounds: box(838200, 2286000, 7315200, 571500)},
					{ID: "sh-title", Kind: "text", Text: "标题", Bounds: box(838200, 838200, 7315200, 1143000)},
					// 无包围盒的形状不该出现在几何里：渲染侧无法对它做位移/缩放。
					{ID: "sh-nobounds", Kind: "text", Text: "无框"},
				},
			},
			{
				Index: 1, SlideID: "s2",
				Shapes: []*project.Shape{
					{ID: "sh-image", Kind: "picture", Bounds: box(0, 0, 640, 360)},
				},
			},
		},
	}
}

func TestDocumentGeometryReadsExtractedDocument(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	seedDocument(t, objects, "t1", "p1", 2, geometryDocument())

	bySlide, err := NewDocumentGeometry(objects).ElementsBySlide(context.Background(),
		PageGeometryScope{TenantID: "t1", ProjectID: "p1", SourceRevisionNo: 2})
	if err != nil {
		t.Fatalf("read geometry: %v", err)
	}
	if len(bySlide) != 2 {
		t.Fatalf("页数 = %d, want 2（每页都要在表里，哪怕元素为空）", len(bySlide))
	}

	page := bySlide["s1"]
	if len(page) != 2 {
		t.Fatalf("s1 元素 = %d, want 2（无包围盒的必须跳过）: %+v", len(page), page)
	}
	if page[0].ID != "sh-title" || page[0].Kind != "title" {
		t.Fatalf("首个元素 = %+v, want sh-title/title（阅读顺序 + 第一个文字元素算标题）", page[0])
	}
	if page[1].ID != "sh-bullet" || page[1].Kind != "bullet" {
		t.Fatalf("第二个元素 = %+v, want sh-bullet/bullet", page[1])
	}
	if b := page[0].Bounds; b == nil || b.X != 838200 || b.Y != 838200 || b.Width != 7315200 || b.Height != 1143000 {
		t.Fatalf("几何没有原样带过来：%+v", b)
	}
	if got := bySlide["s2"]; len(got) != 1 || got[0].Kind != "image" {
		t.Fatalf("s2 = %+v, want 单个 image 元素", got)
	}
}

// 源版本缺失时不能退化成读 src-00：那是「上一次解析到第 0 版」的内容，
// 与当前讲稿可能毫无关系，拿它编译出的元素轨道会动错东西。
func TestDocumentGeometryRefusesUnknownSourceRevision(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	seedDocument(t, objects, "t1", "p1", 0, geometryDocument())

	_, err := NewDocumentGeometry(objects).ElementsBySlide(context.Background(),
		PageGeometryScope{TenantID: "t1", ProjectID: "p1", SourceRevisionNo: 0})
	if err == nil {
		t.Fatal("源版本为 0 时必须报错，而不是去读 src-00")
	}
	if !strings.Contains(err.Error(), "source revision") {
		t.Fatalf("错误信息应点明缺的是源版本：%v", err)
	}
}

func TestDocumentGeometryReportsMissingDocument(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	_, err := NewDocumentGeometry(objects).ElementsBySlide(context.Background(),
		PageGeometryScope{TenantID: "t1", ProjectID: "p1", SourceRevisionNo: 3})
	if err == nil {
		t.Fatal("没有解析产物时必须报错：调用方据此只编整页轨道")
	}
}

// 尾部有垃圾的文档必须判失败：json.Decoder.Decode 会忽略尾部多余字节，
// 于是一份被截断拼接的产物会「成功」解析出半页形状，几何随之静默错位。
func TestDocumentGeometryRejectsTrailingGarbage(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	putObject(t, objects, objectstore.ObjectKey{
		TenantID: "t1", ProjectID: "p1",
		Revision: srcRevString(4), AssetType: "document", AssetID: "extracted", Ext: "json",
	}, []byte(`{"pages":[{"slideId":"s1"}]}{"pages":[]}`), "application/json")

	if _, err := NewDocumentGeometry(objects).ElementsBySlide(context.Background(),
		PageGeometryScope{TenantID: "t1", ProjectID: "p1", SourceRevisionNo: 4}); err == nil {
		t.Fatal("尾部多余的 JSON 必须判为损坏")
	}
}

func TestDocumentGeometryWithoutObjectStore(t *testing.T) {
	if _, err := NewDocumentGeometry(nil).ElementsBySlide(context.Background(),
		PageGeometryScope{TenantID: "t1", ProjectID: "p1", SourceRevisionNo: 1}); err == nil {
		t.Fatal("没有对象存储时必须报错")
	}
}

// 预览与导出共用同一个换算：两处各遍历一遍形状，迟早会在顺序/分类上分叉，
// 表现是「预览动的元素，导出没动」——而输入明明一样。
func TestPageElementsIsPathIndependent(t *testing.T) {
	doc := geometryDocument()
	first := PageElements(doc.Pages[0])
	second := PageElements(doc.Pages[0])
	if len(first) != len(second) {
		t.Fatalf("两次换算长度不同：%d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Kind != second[i].Kind {
			t.Fatalf("第 %d 个元素不稳定：%+v vs %+v", i, first[i], second[i])
		}
	}
	if PageElements(nil) != nil {
		t.Fatal("nil 页应返回 nil")
	}
}

// 这一条是 2.0b 的验收：从**解析产物**读出的几何必须真的变成渲染计划里的元素层。
// 到这一步之前，2.x 的整条通道虽然通，但每次导出都只有整页轨道——
// 中间任何一环（键写错、分类退化成 shape、编译期把元素丢了）都会让结论回到"只动整页"，
// 而它长得和"用户没配元素动效"一模一样。
func TestDocumentGeometryReachesRenderPlan(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	doc := geometryDocument()
	seedDocument(t, objects, "t1", "p1", 2, doc)

	spec, err := animdefault.DefaultSpec(doc.Pages[0], animdefault.DefaultOptions{
		SlideID: "s1", Language: "zh-CN", DurationMS: 3000, FPS: 24,
	})
	if err != nil {
		t.Fatalf("default spec: %v", err)
	}

	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}: spec,
	}}
	a := NewAnimationPageAssembler(&recordingPublisher{}, specs).
		WithGeometry(NewDocumentGeometry(objects)).
		WithLogger(testLogger(t))

	in := assemblerInput()
	in.Snapshot.Animation.Specs = []AnimationSpecRef{
		{SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2, Revision: spec.Revision},
	}
	req, err := a.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	compiler, err := render.NewCompiler(render.Options{
		Canvas: design.Canvas{Width: 1280, Height: 720}, FPS: 24, Background: "#FFFFFF",
	})
	if err != nil {
		t.Fatalf("compiler: %v", err)
	}
	plan, err := compiler.Compile([]render.PageInput{
		{Index: 0, SlideID: req.Pages[0].SlideID, DurationMs: req.Pages[0].DurationMs,
			BaseImage: req.Pages[0].BaseImage, Elements: req.Pages[0].Elements,
			Revision: req.Pages[0].Revision, SegmentOffsets: req.Pages[0].SegmentOffsets},
		{Index: 1, SlideID: req.Pages[1].SlideID, DurationMs: req.Pages[1].DurationMs,
			BaseImage: req.Pages[1].BaseImage, Elements: req.Pages[1].Elements,
			Revision: req.Pages[1].Revision, SegmentOffsets: req.Pages[1].SegmentOffsets},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	layers := plan.Pages[0].Layers
	animated := 0
	for _, l := range layers {
		if len(l.Clips) > 0 {
			animated++
		}
	}
	if animated == 0 {
		t.Fatalf("配了元素动效、几何也读到了，计划里却没有任何元素动画：%+v", layers)
	}
	// 没有规格的第二页不该凭空长出层：几何只对真要渲动效的页生效。
	if len(plan.Pages[1].Layers) != 0 {
		t.Fatalf("无规格页出现了 %d 个图层", len(plan.Pages[1].Layers))
	}
}
