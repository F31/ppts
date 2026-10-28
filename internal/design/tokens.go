package design

import (
	"fmt"
	"strings"
)

const (
	// MinBodyFontPx 是 1080p 下正文的最小字号（设计方案 §19.6）。
	MinBodyFontPx = 32.0
	// MinContrastRatio 是成片文本的统一对比度下限。
	// 刻意不使用 WCAG 的「大字号 3:1」豁免：本场景正文下限即 32px，
	// 照搬豁免等于全部文本降到 3:1，规则自我失效。见 design.md D2。
	MinContrastRatio = 4.5
)

// 颜色用途。用于把「可读性契约」限定在文本上，装饰元素不参与对比度门禁。
const (
	RoleInk      = "ink"      // 前景文本
	RolePaper    = "paper"    // 背景/画布
	RoleAccent   = "accent"   // 品牌强调
	RoleSemantic = "semantic" // 语义色（成功/警告/危险）
)

// Canvas 是成片画布。全项目只允许一套：1080p、16:9。
type Canvas struct {
	Width, Height int
}

// Aspect 返回宽高比。
func (c Canvas) Aspect() float64 {
	if c.Height == 0 {
		return 0
	}
	return float64(c.Width) / float64(c.Height)
}

// Grid 是列行栅格。列间距与行间距共用 Gutter。
type Grid struct {
	Columns, Rows            int
	MarginX, MarginY, Gutter int
}

func (g Grid) usable(width int) int   { return width - 2*g.MarginX - (g.Columns-1)*g.Gutter }
func (g Grid) usableH(height int) int { return height - 2*g.MarginY - (g.Rows-1)*g.Gutter }

// ColumnWidth 由参数推导，不独立声明——否则会出现「声明的列宽与栅格不符」。
// 不能整除时返回错误，不做四舍五入：半像素在 1080p 下表现为 1px 模糊描边。
func (g Grid) ColumnWidth(c Canvas) (int, error) {
	if g.Columns <= 0 {
		return 0, fmt.Errorf("design: 列数必须为正: %d", g.Columns)
	}
	u := g.usable(c.Width)
	if u <= 0 {
		return 0, fmt.Errorf("design: 画布宽 %d 容不下 %d 列（边距 %d、间距 %d）", c.Width, g.Columns, g.MarginX, g.Gutter)
	}
	if u%g.Columns != 0 {
		return 0, fmt.Errorf("design: 列宽不能整除（可用宽 %d ÷ %d 列，余 %d）", u, g.Columns, u%g.Columns)
	}
	return u / g.Columns, nil
}

// RowHeight 同 ColumnWidth。
func (g Grid) RowHeight(c Canvas) (int, error) {
	if g.Rows <= 0 {
		return 0, fmt.Errorf("design: 行数必须为正: %d", g.Rows)
	}
	u := g.usableH(c.Height)
	if u <= 0 {
		return 0, fmt.Errorf("design: 画布高 %d 容不下 %d 行（边距 %d、间距 %d）", c.Height, g.Rows, g.MarginY, g.Gutter)
	}
	if u%g.Rows != 0 {
		return 0, fmt.Errorf("design: 行高不能整除（可用高 %d ÷ %d 行，余 %d）", u, g.Rows, u%g.Rows)
	}
	return u / g.Rows, nil
}

// Verify 用整数等式校验栅格自洽，差额 MUST 为 0。
func (g Grid) Verify(c Canvas) error {
	cw, err := g.ColumnWidth(c)
	if err != nil {
		return err
	}
	rh, err := g.RowHeight(c)
	if err != nil {
		return err
	}
	if got := 2*g.MarginX + g.Columns*cw + (g.Columns-1)*g.Gutter; got != c.Width {
		return fmt.Errorf("design: 横向栅格不自洽：%d ≠ 画布宽 %d（差 %d）", got, c.Width, got-c.Width)
	}
	if got := 2*g.MarginY + g.Rows*rh + (g.Rows-1)*g.Gutter; got != c.Height {
		return fmt.Errorf("design: 纵向栅格不自洽：%d ≠ 画布高 %d（差 %d）", got, c.Height, got-c.Height)
	}
	return nil
}

// Rect 是像素矩形（左上原点）。
type Rect struct {
	X, Y, Width, Height int
}

// CellRect 把槽位换算为像素矩形。入参是 1 基行列。
func (g Grid) CellRect(c Canvas, col, colSpan, row, rowSpan int) (Rect, error) {
	cw, err := g.ColumnWidth(c)
	if err != nil {
		return Rect{}, err
	}
	rh, err := g.RowHeight(c)
	if err != nil {
		return Rect{}, err
	}
	if col < 1 || row < 1 || colSpan < 1 || rowSpan < 1 {
		return Rect{}, fmt.Errorf("design: 行列与跨度为 1 基正整数（got col=%d row=%d colSpan=%d rowSpan=%d）", col, row, colSpan, rowSpan)
	}
	if col+colSpan-1 > g.Columns {
		return Rect{}, fmt.Errorf("design: 列越界（col=%d span=%d → 右边界 %d > %d 列）", col, colSpan, col+colSpan-1, g.Columns)
	}
	if row+rowSpan-1 > g.Rows {
		return Rect{}, fmt.Errorf("design: 行越界（row=%d span=%d → 下边界 %d > %d 行）", row, rowSpan, row+rowSpan-1, g.Rows)
	}
	return Rect{
		X:      g.MarginX + (col-1)*(cw+g.Gutter),
		Y:      g.MarginY + (row-1)*(rh+g.Gutter),
		Width:  colSpan*cw + (colSpan-1)*g.Gutter,
		Height: rowSpan*rh + (rowSpan-1)*g.Gutter,
	}, nil
}

// TypeToken 是命名字阶档位。LetterSpacing 单位为 em。
type TypeToken struct {
	Name          string
	Size          float64
	LineHeight    float64
	LetterSpacing float64
	// Body 标记正文档位：受 MinBodyFontPx 约束。装饰性档位（页码/来源）为 false。
	Body bool
}

// LineHeightPx 返回行高像素。
func (t TypeToken) LineHeightPx() float64 { return t.Size * t.LineHeight }

// ColorToken 是色板项。Readable=true 表示可承载正文，必须满足对比度契约。
type ColorToken struct {
	Name     string
	RGB      RGB
	Role     string
	Readable bool
}

// Theme 是全部视觉令牌的聚合。
type Theme struct {
	Canvas Canvas
	Grid   Grid
	Type   []TypeToken
	Colors []ColorToken
	Space  []int // 间距标尺
	Radius []int
	Stroke []int
}

// Default 返回成片视觉基线。所有数值集中在此，改动只发生在这里。
func Default() Theme {
	return Theme{
		Canvas: Canvas{Width: 1920, Height: 1080},
		Grid:   Grid{Columns: 12, Rows: 8, MarginX: 96, MarginY: 72, Gutter: 24},
		// 字阶为手工定档，不是等比数列：跨度过大时等比会推出 20px 这种不可读的档位。
		// 正文下限 32px 来自设计方案 §19.6。
		Type: []TypeToken{
			{Name: "display", Size: 120, LineHeight: 1.10, LetterSpacing: -0.020},
			{Name: "h1", Size: 84, LineHeight: 1.15, LetterSpacing: -0.015},
			{Name: "h2", Size: 64, LineHeight: 1.20, LetterSpacing: -0.010},
			{Name: "h3", Size: 48, LineHeight: 1.25, LetterSpacing: -0.005},
			{Name: "lead", Size: 40, LineHeight: 1.40, LetterSpacing: 0},
			{Name: "body", Size: 36, LineHeight: 1.55, LetterSpacing: 0, Body: true},
			{Name: "bodySm", Size: 32, LineHeight: 1.60, LetterSpacing: 0, Body: true},
			// caption 只用于页脚/来源/页码；非正文，不受 32px 下限约束。
			{Name: "caption", Size: 24, LineHeight: 1.40, LetterSpacing: 0.010},
		},
		// 品牌色系的可读化变体：控制台强调色 #4074FF / #25D3A7 在纯白上不足 4.5:1，
		// 压深到 #4338CA / #0F766E 后分别为 7.9 / 5.5，仍属同一色相。
		Colors: []ColorToken{
			{Name: "paper", RGB: MustParseHex("#FFFFFF"), Role: RolePaper},
			{Name: "paperAlt", RGB: MustParseHex("#F1F5F9"), Role: RolePaper},
			{Name: "ink", RGB: MustParseHex("#0F172A"), Role: RoleInk, Readable: true},
			{Name: "inkBody", RGB: MustParseHex("#1E293B"), Role: RoleInk, Readable: true},
			{Name: "inkMuted", RGB: MustParseHex("#475569"), Role: RoleInk, Readable: true},
			{Name: "accent", RGB: MustParseHex("#4338CA"), Role: RoleAccent, Readable: true},
			{Name: "accentAlt", RGB: MustParseHex("#0F766E"), Role: RoleAccent, Readable: true},
			{Name: "ok", RGB: MustParseHex("#166534"), Role: RoleSemantic, Readable: true},
			{Name: "warn", RGB: MustParseHex("#92400E"), Role: RoleSemantic, Readable: true},
			{Name: "danger", RGB: MustParseHex("#B91C1C"), Role: RoleSemantic, Readable: true},
		},
		Space:  []int{4, 8, 12, 16, 24, 32, 48, 64, 96, 128},
		Radius: []int{0, 8, 16, 24, 32},
		Stroke: []int{2, 3, 4, 6},
	}
}

// TypeByName 按名取字阶档位。
func (t Theme) TypeByName(name string) (TypeToken, bool) {
	for _, v := range t.Type {
		if v.Name == name {
			return v, true
		}
	}
	return TypeToken{}, false
}

// ColorByName 按名取色板项。
func (t Theme) ColorByName(name string) (ColorToken, bool) {
	for _, v := range t.Colors {
		if v.Name == name {
			return v, true
		}
	}
	return ColorToken{}, false
}

// Papers 返回全部背景色。
func (t Theme) Papers() []ColorToken {
	var out []ColorToken
	for _, c := range t.Colors {
		if c.Role == RolePaper {
			out = append(out, c)
		}
	}
	return out
}

// Verify 做令牌自检：栅格自洽、字阶单调且正文档位达标、标尺递增、色板可读。
func (t Theme) Verify() error {
	if t.Canvas.Width <= 0 || t.Canvas.Height <= 0 {
		return fmt.Errorf("design: 画布尺寸非法 %dx%d", t.Canvas.Width, t.Canvas.Height)
	}
	if err := t.Grid.Verify(t.Canvas); err != nil {
		return err
	}
	if err := t.verifyType(); err != nil {
		return err
	}
	if err := verifyScale("间距标尺", t.Space, 4); err != nil {
		return err
	}
	if err := verifyScale("圆角标尺", t.Radius, 1); err != nil {
		return err
	}
	if err := verifyScale("描边标尺", t.Stroke, 1); err != nil {
		return err
	}
	return t.verifyColors()
}

func (t Theme) verifyType() error {
	if len(t.Type) == 0 {
		return fmt.Errorf("design: 字阶为空")
	}
	seen := map[string]bool{}
	bodyCount := 0
	for i, tok := range t.Type {
		if tok.Name == "" {
			return fmt.Errorf("design: 第 %d 个字阶档位缺少名称", i)
		}
		if seen[tok.Name] {
			return fmt.Errorf("design: 字阶档位重名: %s", tok.Name)
		}
		seen[tok.Name] = true
		if tok.Size <= 0 || tok.LineHeight <= 0 {
			return fmt.Errorf("design: 字阶 %s 的字号/行高必须为正", tok.Name)
		}
		// 档位按声明顺序严格递减：顺序即层级，不另设 level 字段。
		if i > 0 && tok.Size >= t.Type[i-1].Size {
			return fmt.Errorf("design: 字阶 %s（%.0f）未小于上一档 %s（%.0f）",
				tok.Name, tok.Size, t.Type[i-1].Name, t.Type[i-1].Size)
		}
		if tok.Body {
			bodyCount++
			if tok.Size < MinBodyFontPx {
				return fmt.Errorf("design: 正文档位 %s 字号 %.0f 低于下限 %.0f（设计方案 §19.6）",
					tok.Name, tok.Size, MinBodyFontPx)
			}
		}
	}
	if bodyCount == 0 {
		return fmt.Errorf("design: 必须至少声明一个正文档位")
	}
	return nil
}

func verifyScale(name string, s []int, unit int) error {
	if len(s) == 0 {
		return fmt.Errorf("design: %s为空", name)
	}
	for i, v := range s {
		if v < 0 {
			return fmt.Errorf("design: %s含负值 %d", name, v)
		}
		if i > 0 && v <= s[i-1] {
			return fmt.Errorf("design: %s必须严格递增（%d 之后是 %d）", name, s[i-1], v)
		}
	}
	if s[0] < 0 || s[0]%unit != 0 {
		return fmt.Errorf("design: %s最小值 %d 必须是 %d 的倍数", name, s[0], unit)
	}
	return nil
}

func (t Theme) verifyColors() error {
	papers := t.Papers()
	if len(papers) == 0 {
		return fmt.Errorf("design: 色板必须至少声明一个背景色（role=paper）")
	}
	seen := map[string]bool{}
	inkCount := 0
	for _, c := range t.Colors {
		if c.Name == "" {
			return fmt.Errorf("design: 色板项缺少名称")
		}
		if seen[c.Name] {
			return fmt.Errorf("design: 色板项重名: %s", c.Name)
		}
		seen[c.Name] = true
		switch c.Role {
		case RoleInk, RolePaper, RoleAccent, RoleSemantic:
		case "":
			return fmt.Errorf("design: 色板项 %s 缺少用途（role）", c.Name)
		default:
			return fmt.Errorf("design: 色板项 %s 的用途 %q 不在允许集合内", c.Name, c.Role)
		}
		if c.Role == RoleInk {
			inkCount++
		}
		if !c.Readable {
			continue
		}
		for _, p := range papers {
			if r := ContrastRatio(c.RGB, p.RGB); r < MinContrastRatio {
				return fmt.Errorf("design: %s 在 %s 上对比度 %.2f:1 低于 %.1f:1（%s 不可读）",
					c.Name, p.Name, r, MinContrastRatio, strings.ToUpper(c.RGB.Hex()))
			}
		}
	}
	if inkCount == 0 {
		return fmt.Errorf("design: 色板必须至少声明一个前景文本色（role=ink）")
	}
	return nil
}
