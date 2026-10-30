package render

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 这两条用例锁的是「staging 集合」而不是像素：
// 转场页的页首几帧画的是前一页**末帧**，而末帧要用前一页自己的底板与层图。
// 少 staging 前一页时，合成器会退回整页底图——对只有入场动画的页恰好等价，
// 对带出场动画（末帧非终态）的页就是旧画面。像素级后果由 cmd/render-perf 的
// edit-parity 判据兜底；这里先证明「集合里有没有前一页」这件事本身。

func stageFixture(t *testing.T, pages int) (*Timeline, string) {
	t.Helper()
	dir := t.TempDir()
	plan := multiPagePlan(pages)
	for i := range plan.Pages {
		plan.Pages[i].BaseImage = "pages/p" + string(rune('0'+i)) + ".png"
		img := image.NewRGBA(image.Rect(0, 0, 64, 36))
		for y := 0; y < 36; y++ {
			for x := 0; x < 64; x++ {
				img.SetRGBA(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 5), B: 200, A: 255})
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, "pages"), 0o750); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, "pages", "p"+string(rune('0'+i))+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	plan.Width, plan.Height = 64, 36
	for i := range plan.Pages {
		plan.Pages[i].Layers[0].Box = Box{X: 2, Y: 2, W: 20, H: 10}
	}
	return plan, dir
}

func TestStagePagesOnlyTouchesRequestedPages(t *testing.T) {
	plan, dir := stageFixture(t, 2)
	staged, err := (&RemotionBackend{}).stagePages(plan, dir, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if staged.Pages[1].PlateImage == "" || staged.Pages[1].Layers[0].Image == "" {
		t.Fatal("requested page must be staged")
	}
	if staged.Pages[0].PlateImage != "" || staged.Pages[0].Layers[0].Image != "" {
		t.Fatal("pages outside the request must not be staged (that is the whole point of the subset)")
	}
}

func TestStagePagesIncludesTransitionPredecessor(t *testing.T) {
	plan, dir := stageFixture(t, 2)
	plan.Pages[1].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}

	set := withTransitionPrevs(plan, []int{1})
	staged, err := (&RemotionBackend{}).stagePages(plan, dir, set)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Pages[0].PlateImage == "" || staged.Pages[0].Layers[0].Image == "" {
		t.Fatal("the page whose last frame appears in the transition must be staged too")
	}
	if staged.Pages[1].PlateImage == "" {
		t.Fatal("the rendered page must still be staged")
	}
}

// 没有转场时不该把前一页拉进来：那是白花 86ms/页（ADR-4 的实测值）。
func TestWithTransitionPrevsIgnoresPagesWithoutTransition(t *testing.T) {
	plan, _ := stageFixture(t, 3)
	plan.Pages[2].Transition = Transition{Type: animation.TransitionNone}
	got := withTransitionPrevs(plan, []int{2})
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("without a transition only the page itself is needed, got %v", got)
	}
}
