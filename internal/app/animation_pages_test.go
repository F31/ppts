package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
)

// testLogger 把组装层的降级日志丢掉：这些用例断言的是行为，不是日志文本。
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingPublisher struct {
	calls [][]byte
	types []string
	err   error
}

func (p *recordingPublisher) Publish(_ context.Context, contentType string, data []byte) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	p.calls = append(p.calls, append([]byte(nil), data...))
	p.types = append(p.types, contentType)
	return fmt.Sprintf("pages/%03d.png", len(p.calls)-1), nil
}

type fakeSpecSource struct {
	revs map[animation.Key]*animation.Revision
	err  error
	got  []animation.Key
}

func (f *fakeSpecSource) Get(_ context.Context, key animation.Key) (*animation.Revision, error) {
	f.got = append(f.got, key)
	if f.err != nil {
		return nil, f.err
	}
	if rev, ok := f.revs[key]; ok {
		return rev, nil
	}
	return nil, animation.ErrNotFound
}

type fakeGeometry struct {
	docs  map[int]map[string][]render.Element
	err   error
	calls []PageGeometryScope
}

func (f *fakeGeometry) ElementsBySlide(_ context.Context, scope PageGeometryScope) (map[string][]render.Element, error) {
	f.calls = append(f.calls, scope)
	if f.err != nil {
		return nil, f.err
	}
	return f.docs[scope.SourceRevisionNo], nil
}

func assemblerInput() AnimationPageInput {
	return AnimationPageInput{
		Snapshot: ExportSnapshot{
			Width: 1280, Height: 720, FPS: 24,
			Animation: &AnimationOptions{Specs: []AnimationSpecRef{
				{SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2, Revision: 7},
			}},
		},
		TenantID: "t1", ProjectID: "p1",
		Slides: []media.SlideCue{
			{
				SlideID: "s1", StartUS: 1_000_000, EndUS: 4_500_000,
				Segments: []media.SegmentCue{
					{SegmentID: "seg1", StartUS: 1_000_000},
					{SegmentID: "seg2", StartUS: 3_000_000},
				},
			},
			{SlideID: "s2", StartUS: 4_500_000, EndUS: 6_000_000},
		},
		PagePNGs: [][]byte{pngForExportTest(), pngForExportTest()},
	}
}

func TestAnimationPageAssemblerBuildsPages(t *testing.T) {
	pub := &recordingPublisher{}
	key := animation.Key{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		key: {TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2, Revision: 7},
	}}
	a := NewAnimationPageAssembler(pub, specs)

	req, err := a.Build(context.Background(), assemblerInput())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pub.calls) != 2 {
		t.Fatalf("投递了 %d 张图，want 2", len(pub.calls))
	}
	for i, ct := range pub.types {
		if ct != "image/png" {
			t.Fatalf("page %d content type = %q", i, ct)
		}
	}
	if req.Width != 1280 || req.Height != 720 || req.FPS != 24 {
		t.Fatalf("canvas = %dx%d@%d, want 1280x720@24", req.Width, req.Height, req.FPS)
	}
	if len(req.Pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(req.Pages))
	}
	first := req.Pages[0]
	if first.BaseImage != "pages/000.png" || req.Pages[1].BaseImage != "pages/001.png" {
		t.Fatalf("baseImage 顺序错了：%q / %q", first.BaseImage, req.Pages[1].BaseImage)
	}
	// 时长按时间轴算，不是按页图张数平均分。
	if first.DurationMs != 3500 || req.Pages[1].DurationMs != 1500 {
		t.Fatalf("durations = %d / %d, want 3500 / 1500", first.DurationMs, req.Pages[1].DurationMs)
	}
	// 偏移必须是**相对本页起点**：seg2 在页内 2s 处，而不是绝对 3000ms。
	if first.SegmentOffsets["seg1"] != 0 || first.SegmentOffsets["seg2"] != 2000 {
		t.Fatalf("offsets = %v, want seg1=0 seg2=2000", first.SegmentOffsets)
	}
	if first.Revision == nil || first.Revision.Revision != 7 {
		t.Fatalf("page1 revision = %+v, want revision 7", first.Revision)
	}
	if req.Pages[1].Revision != nil {
		t.Fatal("没有规格的页必须是 Revision=nil（常态），而不是报错或借用别页")
	}
	if got := specs.got[0]; got.TenantID != "t1" || got.ProjectID != "p1" || got.SourceRevisionNo != 2 {
		t.Fatalf("查询维度不完整：%+v", got)
	}
}

// 导出排队期间规格被改：这一页按无规格处理，但绝不拿最新版渲下去——
// snapshotHash 已经承诺了产物依据的是哪一版。
func TestAnimationPageAssemblerDropsChangedRevision(t *testing.T) {
	pub := &recordingPublisher{}
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}: {
			Revision: 9, // 快照记的是 7
		},
	}}
	a := NewAnimationPageAssembler(pub, specs).WithLogger(testLogger(t))

	req, err := a.Build(context.Background(), assemblerInput())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.Pages[0].Revision != nil {
		t.Fatalf("版本不符仍采用了规格：revision=%d", req.Pages[0].Revision.Revision)
	}
}

func TestAnimationPageAssemblerToleratesSpecErrors(t *testing.T) {
	pub := &recordingPublisher{}
	specs := &fakeSpecSource{err: errors.New("db down")}
	a := NewAnimationPageAssembler(pub, specs).WithLogger(testLogger(t))

	req, err := a.Build(context.Background(), assemblerInput())
	if err != nil {
		t.Fatalf("读规格失败不该让整次导出退回静态：%v", err)
	}
	if req.Pages[0].Revision != nil {
		t.Fatal("读不到的规格不能凭空造一个")
	}
}

func TestAnimationPageAssemblerFailsOnPublisherError(t *testing.T) {
	a := NewAnimationPageAssembler(&recordingPublisher{err: errors.New("disk full")},
		&fakeSpecSource{}).WithLogger(testLogger(t))
	if _, err := a.Build(context.Background(), assemblerInput()); err == nil {
		t.Fatal("投递失败必须报错：baseImage 不存在时渲染出的每一页都是空的")
	}
}

func TestAnimationPageAssemblerRejectsBadInput(t *testing.T) {
	a := NewAnimationPageAssembler(&recordingPublisher{}, &fakeSpecSource{}).WithLogger(testLogger(t))

	in := assemblerInput()
	in.PagePNGs = in.PagePNGs[:1]
	if _, err := a.Build(context.Background(), in); err == nil {
		t.Fatal("页图数少于页数必须报错：少一张会让后面所有页错位")
	}

	in = assemblerInput()
	in.Slides[0].EndUS = in.Slides[0].StartUS
	if _, err := a.Build(context.Background(), in); err == nil {
		t.Fatal("零长页必须报错")
	}
}

func TestAnimationPageAssemblerUsesGeometryWhenAvailable(t *testing.T) {
	pub := &recordingPublisher{}
	// 几何只在「这一页真要渲动效」时才读：规格取不回（或版本不符）时元素会被丢掉，
	// 所以这里必须先让规格取得到，否则测的其实是另一条路径。
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}: {Revision: 7},
	}}
	geom := &fakeGeometry{docs: map[int]map[string][]render.Element{
		2: {"s1": {{ID: "shape-1", Kind: "title", Bounds: &render.EMUBox{X: 10, Y: 20, Width: 30, Height: 40}}}},
	}}
	a := NewAnimationPageAssembler(pub, specs).WithGeometry(geom)

	req, err := a.Build(context.Background(), assemblerInput())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(req.Pages[0].Elements) != 1 || req.Pages[0].Elements[0].ID != "shape-1" {
		t.Fatalf("geometry 未生效：%+v", req.Pages[0].Elements)
	}
	if len(req.Pages[1].Elements) != 0 {
		t.Fatalf("没有几何的页应当是空元素集，got %+v", req.Pages[1].Elements)
	}
	if len(geom.calls) != 1 {
		t.Fatalf("同一源版本应当只读一次文档，实际读了 %d 次：%+v", len(geom.calls), geom.calls)
	}
	if got := geom.calls[0]; got.SourceRevisionNo != 2 || got.TenantID != "t1" || got.ProjectID != "p1" {
		t.Fatalf("几何读取的作用域不对：%+v", got)
	}
}

// 两页都配了动效、同一个源版本：文档只能读一次。
// 逐页读在 S3 上就是每页一个 RTT，而它们读的本来就是同一个对象。
func TestAnimationPageAssemblerReadsGeometryOncePerRevision(t *testing.T) {
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}: {Revision: 7},
		{TenantID: "t1", ProjectID: "p1", SlideID: "s2", Language: "zh-CN", SourceRevisionNo: 2}: {Revision: 3},
	}}
	geom := &fakeGeometry{docs: map[int]map[string][]render.Element{2: {}}}
	a := NewAnimationPageAssembler(&recordingPublisher{}, specs).WithGeometry(geom)

	in := assemblerInput()
	in.Snapshot.Animation.Specs = append(in.Snapshot.Animation.Specs,
		AnimationSpecRef{SlideID: "s2", Language: "zh-CN", SourceRevisionNo: 2, Revision: 3})
	req, err := a.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.Pages[0].Revision == nil || req.Pages[1].Revision == nil {
		t.Fatalf("两页都该有规格：%+v", req.Pages)
	}
	if len(geom.calls) != 1 {
		t.Fatalf("同一源版本读了 %d 次文档，want 1", len(geom.calls))
	}
}

// 没配动效的页不该触发文档读：整份 document.json 读进来只会被丢掉，
// 而导出里「只有一页配了动效」是常态。
func TestAnimationPageAssemblerSkipsGeometryForPagesWithoutSpec(t *testing.T) {
	geom := &fakeGeometry{docs: map[int]map[string][]render.Element{}}
	specs := &fakeSpecSource{revs: map[animation.Key]*animation.Revision{
		{TenantID: "t1", ProjectID: "p1", SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2}: {Revision: 7},
	}}
	a := NewAnimationPageAssembler(&recordingPublisher{}, specs).WithGeometry(geom)

	in := assemblerInput()
	if _, err := a.Build(context.Background(), in); err != nil {
		t.Fatalf("build: %v", err)
	}

	in = assemblerInput()
	in.Snapshot.Animation = nil
	if _, err := a.Build(context.Background(), in); err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(geom.calls) != 1 {
		t.Fatalf("没有规格时不该读几何，实际读了 %d 次", len(geom.calls))
	}
}

// 几何取不到只影响元素级轨道，整页轨道仍要能编出来（转场/整页推拉）。
func TestAnimationPageAssemblerToleratesGeometryError(t *testing.T) {
	a := NewAnimationPageAssembler(&recordingPublisher{}, &fakeSpecSource{}).
		WithGeometry(&fakeGeometry{err: errors.New("no elements")}).
		WithLogger(testLogger(t))

	req, err := a.Build(context.Background(), assemblerInput())
	if err != nil {
		t.Fatalf("几何失败不该让整次导出退回静态：%v", err)
	}
	if req.Pages[0].Elements != nil {
		t.Fatalf("elements = %+v, want nil", req.Pages[0].Elements)
	}
}

func TestAnimationPageAssemblerRequiresDependencies(t *testing.T) {
	if _, err := NewAnimationPageAssembler(nil, &fakeSpecSource{}).Build(context.Background(), assemblerInput()); err == nil {
		t.Fatal("缺 publisher 必须报错（接线错误，不该静默降级）")
	}
	if _, err := NewAnimationPageAssembler(&recordingPublisher{}, nil).Build(context.Background(), assemblerInput()); err == nil {
		t.Fatal("缺规格来源必须报错（接线错误，不该静默降级）")
	}
}
