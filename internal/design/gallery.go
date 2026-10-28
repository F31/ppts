package design

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// BuildGallery 生成自包含的视觉基线预览页：单个 HTML、内联样式与脚本、零外链。
//
// 为什么必须自包含：预览页最常见的打开方式是双击（file://）。在 file:// 下
// fetch() 与 ES module 会被 CORS 拦，外链字体会静默失败——页面看起来"白屏"，
// 而人只会得出"基线没做"的结论，不会去查控制台。
func BuildGallery() ([]byte, error) {
	t := Default()
	if err := t.Verify(); err != nil {
		return nil, err
	}
	m := DefaultMotion()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	archs, err := Archetypes()
	if err != nil {
		return nil, err
	}
	cw, err := t.Grid.ColumnWidth(t.Canvas)
	if err != nil {
		return nil, err
	}
	rh, err := t.Grid.RowHeight(t.Canvas)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>成片视觉基线 · 预览</title><style>")
	b.WriteString(galleryCSS(t))
	b.WriteString("</style></head><body><div class=\"wrap\">")

	writeGalleryHeader(&b, t, m, cw, rh)
	writePalette(&b, t)
	writeTypeScale(&b, t)
	writeGrid(&b, t, cw, rh)
	writeArchetypes(&b, t, archs)
	writeMotion(&b, m)
	writeMetrics(&b)

	b.WriteString("<footer class=\"foot\">由 <code>internal/design</code> 生成 · 单文件自包含 · 数值改动请改令牌，不要改本页</footer>")
	b.WriteString("</div></body></html>\n")
	return []byte(b.String()), nil
}

// WriteGallery 把预览页写入文件（自动创建父目录）。
func WriteGallery(p string) error {
	data, err := BuildGallery()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("design: 创建输出目录失败: %w", err)
		}
	}
	return os.WriteFile(p, data, 0o644)
}

func writeGalleryHeader(b *strings.Builder, t Theme, m MotionProfile, cw, rh int) {
	fmt.Fprintf(b, `<header class="hd"><div class="eyebrow">视觉基线 · 批次一（G2 版式 / G4 运动 / G5 度量）</div>`)
	fmt.Fprintf(b, `<h1>成片视觉基线</h1>`)
	fmt.Fprintf(b, `<p class="lede">画布 %d×%d ｜ 栅格 %d×%d ｜ 列宽 %d ｜ 行高 %d ｜ 间距 %d ｜ 边距 %d/%d ｜ 字阶 %d 档 ｜ 缓动 %d 条</p>`,
		t.Canvas.Width, t.Canvas.Height, t.Grid.Columns, t.Grid.Rows, cw, rh, t.Grid.Gutter, t.Grid.MarginX, t.Grid.MarginY, len(t.Type), len(m.Easings))
	fmt.Fprintf(b, `<p class="note">这一页不是成品，是“标准摊开”：版式槽位、字号比例、对比度实测值、缓动曲线都在这里，改动只发生在 <code>tokens.go</code> 与 <code>archetypes.json</code>。</p></header>`)
}

func writeSectionTitle(b *strings.Builder, id, title, desc string) {
	fmt.Fprintf(b, `<section id="%s" class="sec"><h2>%s</h2><p class="sec-desc">%s</p>`, id, html.EscapeString(title), html.EscapeString(desc))
}

func writePalette(b *strings.Builder, t Theme) {
	writeSectionTitle(b, "palette", "色板", "标注的是实测 WCAG 对比度。只有标为「可读」的前景色参与 4.5:1 门禁；分隔线与色块不参与。")
	papers := t.Papers()
	b.WriteString(`<div class="palette">`)
	for _, c := range t.Colors {
		fmt.Fprintf(b, `<div class="sw"><div class="chip" style="background:%s;border-color:#0f172a1a"></div>`, c.RGB.Hex())
		fmt.Fprintf(b, `<div class="sw-meta"><div class="sw-name">%s<span class="tag">%s</span></div>`, html.EscapeString(c.Name), html.EscapeString(c.Role))
		fmt.Fprintf(b, `<div class="sw-hex">%s</div>`, c.RGB.Hex())
		if c.Readable {
			var pairs []string
			for _, p := range papers {
				pairs = append(pairs, fmt.Sprintf("%s %.2f:1", p.Name, ContrastRatio(c.RGB, p.RGB)))
			}
			fmt.Fprintf(b, `<div class="sw-ratio">%s</div>`, html.EscapeString(strings.Join(pairs, " · ")))
		} else {
			b.WriteString(`<div class="sw-ratio dim">背景/装饰，不参与对比度门禁</div>`)
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(`</section>`)
}

func writeTypeScale(b *strings.Builder, t Theme) {
	writeSectionTitle(b, "type", fmt.Sprintf("字阶（%d 档）", len(t.Type)), "按真实像素渲染，比例即实际比例。正文档位（body/bodySm）受 32px 下限约束，caption 仅限页脚/来源/页码。")
	b.WriteString(`<div class="types">`)
	for _, tok := range t.Type {
		badge := "装饰"
		if tok.Body {
			badge = "正文"
		}
		// 大字号档位用短样本：长样本在容器里被截断，反而看不出字号。
		sample := "结构化的模板 + 可编辑轨道"
		if tok.Size >= 84 {
			sample = "结构化模板"
		}
		fmt.Fprintf(b, `<div class="ty"><div class="ty-meta"><b>%s</b><span class="tag">%s</span><span class="ty-num">%.0fpx / 行高 %.2f / 字距 %.3fem</span></div>`,
			html.EscapeString(tok.Name), html.EscapeString(badge), tok.Size, tok.LineHeight, tok.LetterSpacing)
		fmt.Fprintf(b, `<div class="ty-sample" style="font-size:%.0fpx;line-height:%.2f;letter-spacing:%.3fem">%s</div></div>`,
			tok.Size, tok.LineHeight, tok.LetterSpacing, html.EscapeString(sample))
	}
	b.WriteString(`</div></section>`)
}

func writeGrid(b *strings.Builder, t Theme, cw, rh int) {
	writeSectionTitle(b, "grid", "栅格", fmt.Sprintf("%d 列 × %d 行，列宽 %d、行高 %d、间距 %d；横向与纵向的整数等式必须同时成立，差额为 0。",
		t.Grid.Columns, t.Grid.Rows, cw, rh, t.Grid.Gutter))
	b.WriteString(`<div class="slide grid-slide">`)
	// 列带
	for i := 0; i < t.Grid.Columns; i++ {
		x := t.Grid.MarginX + i*(cw+t.Grid.Gutter)
		fmt.Fprintf(b, `<div class="col-band" style="left:%s;width:%s"></div>`, pct(x, t.Canvas.Width), pct(cw, t.Canvas.Width))
	}
	// 行带
	for i := 0; i < t.Grid.Rows; i++ {
		y := t.Grid.MarginY + i*(rh+t.Grid.Gutter)
		fmt.Fprintf(b, `<div class="row-band" style="top:%s;height:%s"></div>`, pct(y, t.Canvas.Height), pct(rh, t.Canvas.Height))
	}
	fmt.Fprintf(b, `<div class="safe" style="left:%s;top:%s;right:%s;bottom:%s"></div>`,
		pct(t.Grid.MarginX, t.Canvas.Width), pct(t.Grid.MarginY, t.Canvas.Height),
		pct(t.Grid.MarginX, t.Canvas.Width), pct(t.Grid.MarginY, t.Canvas.Height))
	b.WriteString(`</div>`)
	fmt.Fprintf(b, `<p class="cap">蓝色为安全区（边距 %d/%d）；浅灰为 12 列与 8 行的可放置区域。</p>`, t.Grid.MarginX, t.Grid.MarginY)
	b.WriteString(`</section>`)
}

func writeArchetypes(b *strings.Builder, t Theme, archs []Archetype) {
	writeSectionTitle(b, "archetypes", fmt.Sprintf("版式原型（%d 个）", len(archs)),
		"每格标注角色与所占列×行。槽位是建议不是强制：内容不匹配时应回退自由排版，而不是硬塞。")
	b.WriteString(`<div class="arch-grid">`)
	for _, a := range archs {
		fmt.Fprintf(b, `<div class="arch-card"><div class="arch-hd"><b>%s</b><span class="arch-id">%d 槽位</span></div>`, html.EscapeString(a.ID), len(a.Slots))
		fmt.Fprintf(b, `<p class="arch-purpose">%s</p>`, html.EscapeString(a.Purpose))
		b.WriteString(`<div class="slide">`)
		rects, err := a.Rects(t)
		if err == nil {
			for _, sr := range rects {
				fmt.Fprintf(b, `<div class="slot" style="left:%s;top:%s;width:%s;height:%s"><span>%s · %d×%d</span></div>`,
					pct(sr.Rect.X, t.Canvas.Width), pct(sr.Rect.Y, t.Canvas.Height),
					pct(sr.Rect.Width, t.Canvas.Width), pct(sr.Rect.Height, t.Canvas.Height),
					html.EscapeString(sr.Slot.Role), sr.Slot.ColSpan, sr.Slot.RowSpan)
			}
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</div></section>`)
}

func writeMotion(b *strings.Builder, m MotionProfile) {
	writeSectionTitle(b, "motion", "运动质感",
		"数值对齐设计方案 §19.4。曲线按 CSS cubic-bezier 语义求值（先反解 x，再取 y），overshoot 的过冲就是它的目的。")

	// 缓动曲线
	b.WriteString(`<div class="curves">`)
	for _, e := range m.Easings {
		peak := e.Peak(64)
		fmt.Fprintf(b, `<div class="curve"><div class="curve-hd"><b>%s</b><span class="mono">%s</span></div>`, html.EscapeString(e.Name), html.EscapeString(e.CSS()))
		// preserveAspectRatio="none"：时序图不是几何图，横轴时间、纵轴进度各自拉伸才有可读性。
		b.WriteString(`<svg viewBox="0 -24 100 148" preserveAspectRatio="none" class="curve-svg" role="img">`)
		b.WriteString(`<rect x="0" y="-24" width="100" height="148" fill="none"></rect>`)
		fmt.Fprintf(b, `<line x1="0" y1="100" x2="100" y2="100" class="axis"></line><line x1="0" y1="0" x2="100" y2="0" class="axis"></line>`)
		fmt.Fprintf(b, `<path d="%s" class="curve-path"></path>`, easingPath(e, 48))
		b.WriteString(`</svg>`)
		fmt.Fprintf(b, `<div class="curve-ft">峰值 %.3f%s</div>`,
			peak, map[bool]string{true: "（过冲，符合预期）", false: ""}[peak > 1.001])
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)

	// stagger
	b.WriteString(`<div class="two-col"><div class="panel"><h3>组内错峰（5 个元素，间隔 75ms）</h3>`)
	for _, mode := range []StaggerMode{StaggerSequential, StaggerCenter} {
		offs := StaggerOffsets(5, 75, mode)
		var maxOff int64
		for _, off := range offs {
			if off > maxOff {
				maxOff = off
			}
		}
		// 同一偏移的元素是同时出现的（中心扩散下镜像成对），必须叠行显示，
		// 否则两个方块重合，看起来像"少了一个元素"。
		level := make([]int, len(offs))
		seen := map[int64]int{}
		maxLevel := 0
		for i, off := range offs {
			level[i] = seen[off]
			seen[off]++
			if level[i] > maxLevel {
				maxLevel = level[i]
			}
		}
		chartH := 3 + (maxLevel+1)*24 + 1
		fmt.Fprintf(b, `<div class="stag-block"><div class="stag-head"><span class="mono">%s</span><span class="mono">%s</span></div><div class="stag-chart" style="height:%dpx">`,
			html.EscapeString(string(mode)), html.EscapeString(intsToStr(offs)), chartH)
		for i, off := range offs {
			// 百分比定位：图宽随面板变化，但相对节奏不变；绝对像素会在窄面板里溢出。
			left := 0.0
			if maxOff > 0 {
				left = float64(off) / float64(maxOff) * 85
			}
			fmt.Fprintf(b, `<span class="stag-bar" style="left:%.1f%%;top:%dpx">%d</span>`, left, 3+level[i]*24, i+1)
		}
		b.WriteString(`</div></div>`)
	}
	fmt.Fprintf(b, `<p class="cap">间隔区间 %s ms：太密像抖动，太疏像掉帧。中心扩散时镜像元素同时出现，故上下叠放。</p></div>`, m.Stagger)

	// 速度分层
	b.WriteString(`<div class="panel"><h3>速度分层</h3>`)
	for _, tier := range []string{TierLarge, TierMedium, TierSmall} {
		r := m.SpeedTiers[tier]
		w := int(r.Max / 900 * 100)
		fmt.Fprintf(b, `<div class="tier"><span class="mono tier-name">%s</span><div class="tier-bar" style="width:%d%%"></div><span class="mono tier-val">%s ms</span></div>`, tier, w, html.EscapeString(r.String()))
	}
	fmt.Fprintf(b, `<p class="cap">大元素下限 %.0fms 不短于小元素上限 %.0fms；停顿 ≥ %dms。节拍：每页 %s 拍，单拍 %s ms。</p>`,
		m.SpeedTiers[TierLarge].Min, m.SpeedTiers[TierSmall].Max, m.HoldMs, m.Beat.PerPage, m.Beat.UnitMs)
	b.WriteString(`</div></div></section>`)
}

func writeMetrics(b *strings.Builder) {
	writeSectionTitle(b, "metrics", "观感度量", "error 阻断、warn 只建议。把启发式设成阻断，会逼编辑者为了过门禁而删内容。")
	rows := []struct {
		code, cond, sev, limit, why string
	}{
		{RuleFontSize, "正文最小字号", "error", "≥ 32px", "看不清没有商量空间"},
		{RuleContrast, "文本对比度", "error", "≥ 4.5:1", "不照搬 WCAG 大字号 3:1 豁免，否则规则自我失效"},
		{RuleElementCount, "主要元素数", "warn", "≤ 9 个", "7±2；超过建议拆页而非缩字号"},
		{RuleWhitespace, "留白比例", "warn", "≥ 35%", "满版会让观众找不到焦点"},
		{RuleDensity, "节奏密度", "warn / error", "≤ 3 / ≤ 6 clips/s", ">6 已不是节奏快，是每个元素都在动"},
		{RuleBeat, "每页节拍数", "warn", "3–5 拍", "单拍 900–1400ms"},
	}
	b.WriteString(`<table class="mtable"><thead><tr><th>编号</th><th>检查</th><th>级别</th><th>阈值</th><th>理由</th></tr></thead><tbody>`)
	for _, r := range rows {
		fmt.Fprintf(b, `<tr><td class="mono">%s</td><td>%s</td><td><span class="sev %s">%s</span></td><td class="mono">%s</td><td>%s</td></tr>`,
			r.code, html.EscapeString(r.cond), severityClass(r.sev), html.EscapeString(r.sev), html.EscapeString(r.limit), html.EscapeString(r.why))
	}
	b.WriteString(`</tbody></table>`)
	b.WriteString(`<p class="cap">事实值为 0 一律表示“未知”，跳过该项检查，不猜测——与动画规格里页时长 0 表示未知同一语义。</p>`)
	b.WriteString(`</section>`)
}

func severityClass(sev string) string {
	if strings.Contains(sev, "error") {
		return "sev-error"
	}
	return "sev-warn"
}

func easingPath(e Easing, samples int) string {
	var b strings.Builder
	for i := 0; i <= samples; i++ {
		t := float64(i) / float64(samples)
		x := t * 100
		y := (1 - e.At(t)) * 100
		if i == 0 {
			fmt.Fprintf(&b, "M%.2f %.2f", x, y)
		} else {
			fmt.Fprintf(&b, " L%.2f %.2f", x, y)
		}
	}
	return b.String()
}

func intsToStr(v []int64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pct(v, total int) string {
	if total == 0 {
		return "0%"
	}
	return strconv.FormatFloat(float64(v)/float64(total)*100, 'f', 3, 64) + "%"
}

const galleryCSSTemplate = `
:root{--paper:__PAPER__;--paperAlt:__PAPERALT__;--ink:__INK__;--inkBody:__INKBODY__;--inkMuted:__INKMUTED__;--accent:__ACCENT__;--accentAlt:__ACCENTALT__;--ok:__OK__;--warn:__WARN__;--danger:__DANGER__}
*{box-sizing:border-box}
body{margin:0;background:#eef2f7;color:var(--ink);font-family:"PingFang SC","Hiragino Sans GB","Microsoft YaHei",system-ui,-apple-system,"Segoe UI",sans-serif;-webkit-font-smoothing:antialiased}
.wrap{max-width:1180px;margin:0 auto;padding:48px 24px 72px}
.hd h1{font-size:44px;line-height:1.15;letter-spacing:-.02em;margin:12px 0 8px}
.eyebrow{font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--accent)}
.lede{font-size:15px;color:var(--inkBody);margin:0 0 8px}
.note{font-size:13px;line-height:1.7;color:var(--inkMuted);max-width:78ch}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.92em;background:rgba(15,23,42,.06);padding:1px 5px;border-radius:4px}
.sec{margin-top:56px;border-top:1px solid rgba(15,23,42,.09);padding-top:28px}
.sec h2{font-size:24px;margin:0 0 6px;letter-spacing:-.01em}
.sec-desc{font-size:14px;line-height:1.7;color:var(--inkMuted);max-width:82ch;margin:0 0 22px}
.cap{font-size:12px;color:var(--inkMuted);margin:10px 0 0;line-height:1.6}
.palette{display:grid;grid-template-columns:repeat(auto-fill,minmax(210px,1fr));gap:14px}
.sw{background:var(--paper);border:1px solid rgba(15,23,42,.08);border-radius:12px;overflow:hidden}
.chip{height:56px;border-bottom:1px solid rgba(15,23,42,.08)}
.sw-meta{padding:10px 12px}
.sw-name{font-size:13px;font-weight:600;display:flex;gap:6px;align-items:center}
.tag{font-size:10px;font-weight:500;color:var(--inkMuted);border:1px solid rgba(15,23,42,.14);border-radius:999px;padding:1px 6px}
.sw-hex{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:11px;color:var(--inkMuted);margin-top:2px}
.sw-ratio{font-size:11px;color:var(--inkBody);margin-top:6px;line-height:1.5}
.dim{color:var(--inkMuted)}
.types{display:flex;flex-direction:column;gap:18px}
.ty{border-left:3px solid var(--accent);padding-left:14px}
.ty-meta{font-size:12px;color:var(--inkMuted);display:flex;gap:8px;align-items:baseline;flex-wrap:wrap}
.ty-num{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:11px}
.ty-sample{color:var(--ink);margin-top:4px;overflow:hidden}
/* 用 aspect-ratio 而不是 padding-top：百分数 padding 按"包含块宽度"解析，
   一旦 .slide 比容器窄（如被 max-width 限制），高度就会被算大，16:9 直接失效。 */
.slide{position:relative;width:100%;aspect-ratio:16/9;background:var(--paper);border:1px solid rgba(15,23,42,.12);border-radius:8px;overflow:hidden}
.grid-slide{max-width:760px}
.col-band{position:absolute;top:0;bottom:0;background:rgba(15,23,42,.035)}
.row-band{position:absolute;left:0;right:0;background:rgba(15,23,42,.028)}
.safe{position:absolute;border:1.5px dashed rgba(67,56,202,.55);border-radius:4px}
.arch-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:20px}
.arch-card{background:var(--paper);border:1px solid rgba(15,23,42,.08);border-radius:14px;padding:14px}
.arch-hd{display:flex;justify-content:space-between;align-items:baseline;font-size:14px}
.arch-id{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:11px;color:var(--inkMuted)}
.arch-purpose{font-size:12px;line-height:1.6;color:var(--inkMuted);margin:6px 0 10px;min-height:38px}
.slot{position:absolute;border:1px dashed rgba(67,56,202,.5);background:rgba(67,56,202,.055);border-radius:3px;display:flex;align-items:flex-start;justify-content:flex-start;padding:2px 3px;overflow:hidden}
.slot span{font-size:8.5px;line-height:1.2;color:#4338ca;font-family:ui-monospace,Menlo,Consolas,monospace;white-space:nowrap;font-weight:600}
.curves{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:16px;margin-bottom:24px}
.curve{background:var(--paper);border:1px solid rgba(15,23,42,.08);border-radius:12px;padding:12px}
.curve-hd{display:flex;justify-content:space-between;align-items:baseline;font-size:13px;gap:8px}
.curve-svg{width:100%;height:110px;margin-top:6px}
.axis{stroke:rgba(15,23,42,.14);stroke-width:.8;stroke-dasharray:3 3}
.curve-path{fill:none;stroke:#4338ca;stroke-width:2.2;stroke-linecap:round}
.curve-ft{font-size:11px;color:var(--inkMuted);font-family:ui-monospace,Menlo,Consolas,monospace}
.two-col{display:grid;grid-template-columns:1fr 1fr;gap:18px}
@media (max-width:820px){.two-col{grid-template-columns:1fr}}
.panel{background:var(--paper);border:1px solid rgba(15,23,42,.08);border-radius:12px;padding:14px 16px}
.panel h3{font-size:14px;margin:0 0 12px}



.stag-block{margin-bottom:10px}
.stag-head{display:flex;justify-content:space-between;font-size:11px;color:var(--inkMuted);margin-bottom:4px}
.stag-chart{position:relative;border-radius:6px;background:rgba(15,23,42,.045)}
.stag-bar{position:absolute;top:2px;width:26px;height:24px;border-radius:6px;background:rgba(67,58,202,.14);border:1px solid rgba(67,58,202,.35);color:#4338ca;font-size:11px;display:flex;align-items:center;justify-content:center}

.tier{display:flex;align-items:center;gap:10px;margin-bottom:10px}
.tier-name{width:52px;font-size:11px;color:var(--inkMuted);flex:none}
.tier-bar{height:14px;border-radius:4px;background:linear-gradient(90deg,rgba(15,118,110,.25),rgba(15,118,110,.6));min-width:40px}
.tier span.mono{font-size:11px;color:var(--inkBody)}
.tier-val{white-space:nowrap}
.mtable{width:100%;border-collapse:collapse;font-size:13px;background:var(--paper);border-radius:12px;overflow:hidden}
.mtable th,.mtable td{text-align:left;padding:10px 12px;border-bottom:1px solid rgba(15,23,42,.07)}
.mtable th{font-size:11px;letter-spacing:.06em;text-transform:uppercase;color:var(--inkMuted);font-weight:600}
.sev{font-size:11px;font-family:ui-monospace,Menlo,Consolas,monospace;padding:1px 7px;border-radius:999px;border:1px solid}
.sev-error{color:#B91C1C;border-color:rgba(185,28,28,.3);background:rgba(185,28,28,.06)}
.sev-warn{color:#92400E;border-color:rgba(146,64,14,.3);background:rgba(146,64,14,.07)}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.foot{margin-top:56px;padding-top:20px;border-top:1px solid rgba(15,23,42,.09);font-size:12px;color:var(--inkMuted)}
`

func galleryCSS(t Theme) string {
	get := func(name string) string {
		if c, ok := t.ColorByName(name); ok {
			return c.RGB.Hex()
		}
		return "#000000"
	}
	repl := strings.NewReplacer(
		"__PAPER__", get("paper"),
		"__PAPERALT__", get("paperAlt"),
		"__INK__", get("ink"),
		"__INKBODY__", get("inkBody"),
		"__INKMUTED__", get("inkMuted"),
		"__ACCENT__", get("accent"),
		"__ACCENTALT__", get("accentAlt"),
		"__OK__", get("ok"),
		"__WARN__", get("warn"),
		"__DANGER__", get("danger"),
	)
	return repl.Replace(galleryCSSTemplate)
}
