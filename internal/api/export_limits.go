package api

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

// 免费层的三项限制（ADR-029 ②）：时长、分辨率、水印。
//
// 三条限制里只有**分辨率是夹紧**，另外两条是**受理时拒绝**。这个不对称是有意的：
//
//   - 分辨率：用户没有选择过它（前端不传尺寸，快照由服务端按页图推导）。
//     对一件用户没提过的要求报错，等于让免费用户完全无法导出。
//     夹紧则是"按你的套餐出片"，用户拿到的是能播的东西。
//   - 时长：片子本身超出了套餐定义，压短就是改内容。只能拒绝，并且必须
//     在**建任务之前**拒绝——设计 §7.5 禁止"静默排队"，用户等十分钟才被告知。
//   - 水印：不是拒绝项，而是产物的一部分。它写进快照，于是进 snapshotHash：
//     同一份输入在"有水印"与"无水印"两种策略下产出两个不同的产物键，
//     不会互相覆盖（否则用户升级后下载旧链接，拿到的还是带水印的那份）。
//
// 口径统一挂在**成品**上而不只挂动画：只对带动画的导出加水印，免费用户导一次
// 静态版就绕过去了。代价是免费层的静态导出也受 3 分钟限制，这一点在文档里写明。

// viewableFormats 是"人能看到画面"的格式：只有这些需要水印与时长限制。
//
// srt/vtt 是纯文本，加水印没有意义（也没有像素可打）；web_project 是播放器数据包，
// 它在浏览器里播，所以和水印有关——判据是"会不会被人当成品看"，不是"是不是视频"。
var viewableFormats = map[artifact.Format]bool{
	artifact.FormatMP4:        true,
	artifact.FormatWebProject: true,
	artifact.FormatHTMLBundle: true,
	artifact.FormatHTMLSingle: true,
}

// clampShortSide 把画布按短边上限等比缩小；maxShort <= 0 或已经满足时原样返回。
//
// 用短边而不是高度，是为了让"720p"在横竖两种画布上指同一档画质：
// 1920×1080 与 1080×1920 的短边都是 1080，夹到 720 后分别是 1280×720 与 720×1280。
//
// 结果取偶数：H.264 的 yuv420p 直接拒绝奇数尺寸，而那个失败点在渲染服务里，
// 用户要等到 worker 才看到（与 evenInt 同一条理由）。
func clampShortSide(w, h, maxShort int) (int, int) {
	if maxShort <= 0 || w <= 0 || h <= 0 {
		return w, h
	}
	short := min(w, h)
	if short <= maxShort {
		return w, h
	}
	scale := float64(maxShort) / float64(short)
	return evenInt(math.Round(float64(w) * scale)), evenInt(math.Round(float64(h) * scale))
}

// exportDurationExceeded 是"这条片子超出套餐时长"的判定结果。
//
// 单独成类型而不用 fmt.Errorf 拼一句话：REST 入口要把它写成结构化 JSON
// （前端要能用数字拼自己的文案），Connect 入口要把它包成 Connect 错误。
type exportDurationExceeded struct {
	LimitSec  int
	ActualSec float64
}

// Error 的文案由 ActualSec 现场格式化，不另存一份字符串：存两份就会出现
// "JSON 里是 360 秒、文案里是空"这种自相矛盾——任何直接构造该类型的地方
// （测试、新入口）都得记得填第二个字段，漏填不会编译失败。
func (e *exportDurationExceeded) Error() string {
	return fmt.Sprintf("成片时长 %s 超过当前套餐上限 %s；请精简讲稿，或升级套餐后再导出",
		formatSeconds(int(math.Round(e.ActualSec))), formatSeconds(e.LimitSec))
}

// formatSeconds 把秒数写成"3 分 00 秒"这种可读形式（不足 1 分钟只写秒）。
func formatSeconds(total int) string {
	if total < 60 {
		return fmt.Sprintf("%d 秒", total)
	}
	return fmt.Sprintf("%d 分 %02d 秒", total/60, total%60)
}

// enforceExportLimits 在受理时落实免费层限制，返回拒绝原因（nil = 通过）。
//
// 只修改 snapshot 的 Width/Height/Watermark 三个字段。调用方必须在本函数之后
// 再算 snapshotHash：尺寸与水印都进 hash，写晚一步等于同一个幂等键能产出两三种成片。
func enforceExportLimits(ctx context.Context, objects objectstore.ObjectStore, tenantID, timelineKey string,
	capability animation.Capability, snapshot *app.ExportSnapshot) error {
	if snapshot == nil || !viewableFormats[snapshot.Format] {
		return nil
	}

	// 分辨率：夹紧，不拒绝（理由见文件头）。
	snapshot.Width, snapshot.Height = clampShortSide(snapshot.Width, snapshot.Height, capability.MaxShortSide)

	// 水印：文案来自套餐。空串等价于"不加"，此时不写字段（omitempty），
	// 存量快照的 hash 不受影响。
	snapshot.Watermark = capability.Watermark

	// 时长：超限在受理时拒绝。读不出时间轴时**放行**——导出本身会在 worker 里失败，
	// 在这里因为"读不到"而拒绝，会把一次基础设施抖动说成"你的片子太长"。
	if capability.MaxDurationSec <= 0 {
		return nil
	}
	seconds, err := timelineSeconds(ctx, objects, tenantID, timelineKey)
	if err != nil || seconds <= 0 {
		return nil
	}
	// 比较留 1 秒余量：时间轴写的是微秒累加值，3:00.0004 这种舍入不该被拒。
	if seconds > float64(capability.MaxDurationSec)+1 {
		return &exportDurationExceeded{
			LimitSec:  capability.MaxDurationSec,
			ActualSec: seconds,
		}
	}
	return nil
}

// writeExportDurationExceeded 把时长超限写成 422。
//
// 用 422（Unprocessable Entity）而不是 400：请求格式本身没问题，是内容超出了套餐。
// 响应体带 limitSeconds / actualSeconds，前端才能拼出本地化文案——
// 只回一句中文错误，英文界面会突然冒出中文。
func writeExportDurationExceeded(w http.ResponseWriter, e *exportDurationExceeded) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"code":          "export_duration_exceeded",
		"message":       e.Error(),
		"limitSeconds":  e.LimitSec,
		"actualSeconds": math.Round(e.ActualSec*10) / 10,
	})
}

// connectExportDurationExceeded 是同一条判定的 Connect 形态。
func connectExportDurationExceeded(e *exportDurationExceeded) *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition, e)
}
