package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// 这条基准回答一个具体问题：**底板与层图生成值不值得单独缓存**（M5 design ADR-4）。
//
// 判据不是「它快不快」，而是「它占单页渲染耗时的比例」。单页 1080p30 的渲染
// 实测约 8–10 秒（ADR-16：2 核约 3.1× 实时），所以 staging 只有在百毫秒量级以上
// 才值得再引入一套缓存键与失效逻辑。
func BenchmarkStageSinglePage(b *testing.B) {
	dir := b.TempDir()
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			base.SetRGBA(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 180, A: 255})
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "pages"), 0o750); err != nil {
		b.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "pages", "p000.png"))
	if err != nil {
		b.Fatal(err)
	}
	if err := png.Encode(f, base); err != nil {
		b.Fatal(err)
	}
	f.Close()

	plan := multiPagePlan(1)
	plan.Pages[0].BaseImage = "pages/p000.png"

	backend := &RemotionBackend{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 工作目录就是 dir：底图已在 pages/ 下，plates/ 与 layers/ 每轮覆盖写，
		// 与真实渲染（同一个 job 目录重跑）一致。
		if _, err := backend.stagePages(plan, dir, []int{0}); err != nil {
			b.Fatal(err)
		}
	}
}
