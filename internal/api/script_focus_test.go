package api

import (
	"testing"

	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/internal/narration"
)

// TestFocusProtoTriState 锁住线上的三种状态：缺席（保留）/ 空消息（清空）/ 有目标。
//
// 这一层最容易被"顺手简化"成两种状态——消息缺席与空目标合并成"没有焦点"。合并之后
// 编辑器保存一次文字就会把整页焦点抹掉，而存稿、接口、日志都不会报错，只有成片里
// 那片该留孔的地方没了。
func TestFocusProtoTriState(t *testing.T) {
	segs, err := fromProtoSegments([]*pptsv1.Segment{
		{SegmentId: "seg-01", DisplayText: "a"},
		{SegmentId: "seg-02", DisplayText: "b", Focus: &pptsv1.FocusTarget{}},
		{SegmentId: "seg-03", DisplayText: "c", Focus: &pptsv1.FocusTarget{ShapeId: "shape-9", BarId: "b2"}},
	})
	if err != nil {
		t.Fatalf("fromProtoSegments: %v", err)
	}
	if segs[0].Focus != nil {
		t.Fatalf("缺省 focus 必须保持 nil（= 保留原值），got %+v", segs[0].Focus)
	}
	if segs[1].Focus == nil || segs[1].Focus.Valid() {
		t.Fatalf("空消息必须是非 nil 的空目标（= 清空），got %+v", segs[1].Focus)
	}
	if f := segs[2].Focus; f == nil || f.ShapeID != "shape-9" || f.BarID != "b2" {
		t.Fatalf("seg-03 focus = %+v", f)
	}

	rev := &narration.Revision{SlideID: "slide-1", Segments: []*narration.Segment{
		{SegmentID: "seg-01", DisplayText: "a", Focus: &narration.FocusTarget{ShapeID: "shape-1"}},
		{SegmentID: "seg-02", DisplayText: "b"},
	}}
	proto := toProtoRevision(rev)
	if f := proto.GetSegments()[0].GetFocus(); f.GetShapeId() != "shape-1" || f.GetBarId() != "" {
		t.Fatalf("proto focus = %+v", f)
	}
	if f := proto.GetSegments()[1].GetFocus(); f != nil {
		t.Fatalf("没有焦点时应省略字段，got %+v", f)
	}
	if got := focusJSON(nil); got != nil {
		t.Fatalf("REST 镜像里没有焦点应为 null，got %+v", got)
	}
	if got := focusJSON(&narration.FocusTarget{ShapeID: "shape-1", BarID: "b1"}); got == nil {
		t.Fatal("REST 镜像丢了焦点")
	}
}
