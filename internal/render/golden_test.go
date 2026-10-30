package render

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
)

var updateGolden = flag.Bool("update", false, "重写 testdata 下的 golden 文件")

// TestCompileGolden 用一份固定样片锁住编译语义。
//
// 它防的不是"今天的代码写错了"（那些有细粒度用例），而是"明天有人改了意图"：
// 比如把强度改成也缩放时长、把转场挪到前一页、把位移基准从 1080p 改成画布比例——
// 这些改动都能让所有单测通过，但会让已渲染的片子变样。
func TestCompileGolden(t *testing.T) {
	plan := samplePlan(t)

	got, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "plan.golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o640); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden 已重写: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden 失败（首次生成请加 -update）: %v", err)
	}
	if !bytes.Equal(got, want) {
		// 只差行尾时不要让人去 -update：那会把「检出被 CRLF 化」当成「语义变了」，
		// 顺手重写 golden，真正的改动就再也不拦人了。这条提示是 2026-10-30 加的——
		// 当时 go test ./... 41 个包通过、只有这个用例在本机恒红，原因是
		// core.autocrlf=true 把 testdata 也改了行尾（.gitattributes 现已固定 eol=lf）。
		if bytes.Equal(got, bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) {
			t.Fatalf("golden 与编译结果只差行尾（CRLF vs LF），不是语义变更。\n" +
				"请重新检出该文件：rm -f internal/render/testdata/plan.golden.json && git checkout -- internal/render/testdata/plan.golden.json\n" +
				"并确认 .gitattributes 里 `internal/render/testdata/*.golden.json text eol=lf` 生效。")
		}
		t.Fatalf("编译结果与 golden 不一致。\n若这是有意的语义变更，请跑 `go test ./internal/render -run TestCompileGolden -update` 并 review diff。\n--- 实际 ---\n%s", got)
	}
}

// samplePlan 是一份三页样片：封面（标题滑入）、要点页（分段锚定的三条要点 + 图片）、
// 收尾页（无规格）。它覆盖了元素锚定、分段换算、强度缩放、转场与空页。
func samplePlan(t *testing.T) *Timeline {
	t.Helper()
	c, err := NewCompiler(Options{
		Canvas:         design.Canvas{Width: 1920, Height: 1080},
		SlideWidthEMU:  emuWidescreen,
		SlideHeightEMU: emuHeight,
		FPS:            30,
		Background:     "#FFFFFF",
		Fonts:          []Font{{Family: "Noto Sans SC", Version: "2.004"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	box := func(x, y, w, h int64) *EMUBox {
		return &EMUBox{X: x * emuPerPixel, Y: y * emuPerPixel,
			Width: w * emuPerPixel, Height: h * emuPerPixel}
	}

	cover := revisionOf("business-calm", 1.0, &animation.Track{
		ElementID: "shape-1", Kind: animation.KindTitle,
		Clips: []*animation.Clip{{ID: "c1", Type: "slideIn", DurationMs: 700, Easing: "easeOut",
			Params: animation.Params{Direction: "up", Offset: 32}, Trigger: "onEnter"}},
	})

	bullets := revisionOf("tech-dynamic", 1.15,
		&animation.Track{ElementID: "shape-2", Kind: animation.KindTitle,
			Clips: []*animation.Clip{{ID: "c1", Type: "slideIn", DurationMs: 450, Easing: "easeOut",
				Params: animation.Params{Direction: "up", Offset: 56}, Trigger: "onEnter"}}},
		&animation.Track{ElementID: "shape-3", Kind: animation.KindBullet,
			Clips: []*animation.Clip{{ID: "c2", Type: "fadeIn", DurationMs: 400, Easing: "easeOut",
				Trigger: "withSegment:seg-2"}}},
		&animation.Track{ElementID: "shape-4", Kind: animation.KindImage,
			Clips: []*animation.Clip{{ID: "c3", Type: "pop", DurationMs: 420, Easing: "spring",
				Params: animation.Params{ScaleFrom: 0.92}, Trigger: "withSegment:seg-3"}}},
	)

	plan, err := c.Compile([]PageInput{
		{Index: 0, SlideID: "slide-1", DurationMs: 4200, BaseImage: "pages/p0.png",
			Elements: []Element{{ID: "shape-1", Kind: animation.KindTitle, Bounds: box(240, 360, 1440, 260)}},
			Revision: cover},
		{Index: 1, SlideID: "slide-2", DurationMs: 9600, BaseImage: "pages/p1.png",
			Elements: []Element{
				{ID: "shape-2", Kind: animation.KindTitle, Bounds: box(96, 96, 1100, 140)},
				{ID: "shape-3", Kind: animation.KindBullet, Bounds: box(120, 320, 820, 420)},
				{ID: "shape-4", Kind: animation.KindImage, Bounds: box(1080, 300, 720, 480)},
			},
			SegmentOffsets: map[string]int64{"seg-2": 2400, "seg-3": 5200},
			Revision:       bullets},
		{Index: 2, SlideID: "slide-3", DurationMs: 3000, BaseImage: "pages/p2.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
