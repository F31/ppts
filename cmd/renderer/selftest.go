package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/render"
)

// 自检参数：小画布、短页面——目标是「几分钟内证明这条链路能出片」，
// 不是产出一段好看的视频。任何一帧渲染失败都该让镜像构建/部署失败，
// 而不是等到用户点了导出才发现。
const (
	selftestWidth  = 640
	selftestHeight = 360
	selftestFPS    = 30
	selftestPageMS = 1500
)

// runSelfTest 用固定样例跑一遍完整后端链路：staging（含底板）→ 渲染 → 产物校验。
//
// 为什么放在 Go 侧而不是镜像里的 shell 脚本：它必须与线上走同一个 Backend 实现。
// 用 curl 打一次 /v1/render 只能证明 HTTP 活着，证明不了「Node 能起来、Chrome 能开、
// 片段能落盘」。
//
// faultPage >= 0 时走降级验收（tasks 3.7）：让该页第一次渲染必然失败，
// 期望它被换成静态画面、片段仍然齐全、原因如实报出。此时不校验「两页都有动画」。
func runSelfTest(ctx context.Context, backend render.Backend, workDir string, faultPage int, keep bool) error {
	// stub 只写文本占位片段，自检它等于证明「文件能创建」，证明不了「像素能出」。
	// 门禁一旦允许 stub 通过，就会出现「镜像自检绿、线上渲染全红」。
	if backend.Name() == "stub" {
		return fmt.Errorf("selftest: 需要真实光栅 backend（stub 不产出可播放片段），请用 -backend remotion")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("selftest: prepare workdir: %w", err)
	}
	dir, err := os.MkdirTemp(workDir, "selftest-")
	if err != nil {
		return fmt.Errorf("selftest: temp dir: %w", err)
	}
	plan := selftestPlan()
	for i := range plan.Pages {
		rel := fmt.Sprintf("selftest/p%03d.png", i)
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writePNG(path, paintSelfTestPage(i)); err != nil {
			return err
		}
		plan.Pages[i].BaseImage = rel
	}

	// 故障注入通过环境变量传给 render.mjs：它只影响脚本内的「第一次渲染」，
	// 重试时用的是静态计划，所以仍会正常出片。
	if faultPage >= 0 {
		if faultPage >= len(plan.Pages) {
			return fmt.Errorf("selftest: -selftest-degrade-page=%d 超出页数 %d", faultPage, len(plan.Pages))
		}
		// 只在自检进程内设置，退出前清掉：它会被 node 子进程继承，
		// 留着会让同一进程里后续的其它渲染也「随机失败」。
		if err := os.Setenv("PPTS_RENDER_FAULT_PAGES", fmt.Sprintf("%d", faultPage)); err != nil {
			return fmt.Errorf("selftest: set fault env: %w", err)
		}
		defer os.Unsetenv("PPTS_RENDER_FAULT_PAGES")
	}

	started := time.Now()
	report, err := backend.Render(ctx, plan, dir, nil)
	if err != nil {
		return fmt.Errorf("selftest: render: %w (workdir kept at %s)", err, dir)
	}
	if err := checkSelftestDegradation(report, faultPage, dir); err != nil {
		return err
	}
	for i := range plan.Pages {
		name := filepath.Join(dir, fmt.Sprintf("segment_%03d.mp4", i))
		info, err := os.Stat(name)
		if err != nil {
			return fmt.Errorf("selftest: segment %d missing: %w", i, err)
		}
		if info.Size() < 1024 {
			return fmt.Errorf("selftest: segment %d is suspiciously small (%d bytes)", i, info.Size())
		}
	}
	fmt.Printf("selftest ok: backend=%s pages=%d elapsed=%s degraded=%d\n",
		backend.Name(), len(plan.Pages), time.Since(started).Round(time.Millisecond), len(report.Degraded))

	// 成功即清理；失败保留现场，便于把镜像里的目录挂出来看。
	if keep {
		fmt.Printf("selftest artifacts kept at %s\n", dir)
		return nil
	}
	return os.RemoveAll(dir)
}

// checkSelftestDegradation 判降级是否符合预期。
//
// 未注入故障时「不该有降级」：自检样例底图齐全，出现降级说明链路有问题
// （例如底图取不到），报出来比「片段都在、只是没动」更有价值。
// 注入后反过来：必须恰好降级那一页，且原因必须是 page_render_failed——
// page_skipped 表示静态重试也失败，那一页会变成空洞。
func checkSelftestDegradation(report render.RenderReport, faultPage int, dir string) error {
	if faultPage < 0 {
		if len(report.Degraded) > 0 {
			return fmt.Errorf("selftest: %d page(s) degraded unexpectedly: %+v (workdir kept at %s)",
				len(report.Degraded), report.Degraded, dir)
		}
		return nil
	}
	if len(report.Degraded) != 1 {
		return fmt.Errorf("selftest: 注入第 %d 页故障后应恰好 1 条降级记录，实际 %+v (workdir kept at %s)",
			faultPage, report.Degraded, dir)
	}
	d := report.Degraded[0]
	if d.Page != faultPage {
		return fmt.Errorf("selftest: 降级页 = %d, want %d (workdir kept at %s)", d.Page, faultPage, dir)
	}
	if d.Code != render.DegradationStill {
		return fmt.Errorf("selftest: 降级码 = %q, want %q —— 静态重试也失败，该页会变成空洞 (workdir kept at %s)",
			d.Code, render.DegradationStill, dir)
	}
	return nil
}

func selftestPlan() *render.Timeline {
	var pages []render.Page
	for i := 0; i < 2; i++ {
		pages = append(pages, render.Page{
			Index:      i,
			SlideID:    fmt.Sprintf("selftest-%d", i),
			DurationMs: selftestPageMS,
			Layers: []render.Layer{{
				ElementID: fmt.Sprintf("title-%d", i),
				Kind:      "title",
				AssetMode: render.AssetBitmapRegion,
				Box:       render.Box{X: 40, Y: 40, W: 400, H: 80},
				Clips: []render.Motion{{
					ClipID:     "title-in",
					Effect:     "slideIn",
					StartMs:    0,
					DurationMs: 600,
					Easing:     animation.EasingCurves[animation.DefaultEasing],
					Props: []animation.MotionProp{
						{Name: "opacity", From: 0, To: 1},
						{Name: "translateY", From: 32, To: 0},
					},
				}},
			}},
			Transition: render.Transition{Type: "none"},
		})
	}
	pages[1].Transition = render.Transition{Type: "fade", DurationMs: 240}
	return &render.Timeline{
		SchemaVersion: render.IRSchemaVersion,
		FPS:           selftestFPS,
		Width:         selftestWidth,
		Height:        selftestHeight,
		Background:    "#FFFFFF",
		Pages:         pages,
	}
}

// paintSelfTestPage 画一页「底色 + 标题条 + 卡片」：标题条与图层盒对齐，
// 这样底板补洞才会真的被走到（自检要覆盖最容易出错的那条路径）。
func paintSelfTestPage(index int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, selftestWidth, selftestHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}}, image.Point{}, draw.Src)
	fills := []color.NRGBA{
		{0x1E, 0x3A, 0x8A, 0xFF},
		{0x0F, 0x76, 0x6E, 0xFF},
	}
	draw.Draw(img, image.Rect(40, 40, 440, 120), &image.Uniform{C: fills[index%len(fills)]}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(40, 160, 600, 320), &image.Uniform{C: color.NRGBA{0xF1, 0xF5, 0xF9, 0xFF}}, image.Point{}, draw.Src)
	return img
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
