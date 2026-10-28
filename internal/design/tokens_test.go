package design

import (
	"strings"
	"testing"
)

// cloneTheme 深拷贝切片字段，避免测试之间互相污染（Default() 返回的切片共享底层数组）。
func cloneTheme(t Theme) Theme {
	t.Type = append([]TypeToken(nil), t.Type...)
	t.Colors = append([]ColorToken(nil), t.Colors...)
	t.Space = append([]int(nil), t.Space...)
	t.Radius = append([]int(nil), t.Radius...)
	t.Stroke = append([]int(nil), t.Stroke...)
	return t
}

func TestDefaultThemeVerifies(t *testing.T) {
	if err := Default().Verify(); err != nil {
		t.Fatalf("默认令牌自检失败: %v", err)
	}
}

func TestGridMeasurements(t *testing.T) {
	th := Default()
	cw, err := th.Grid.ColumnWidth(th.Canvas)
	if err != nil {
		t.Fatalf("列宽推导失败: %v", err)
	}
	if cw != 122 {
		t.Errorf("列宽 = %d，期望 122", cw)
	}
	rh, err := th.Grid.RowHeight(th.Canvas)
	if err != nil {
		t.Fatalf("行高推导失败: %v", err)
	}
	if rh != 96 {
		t.Errorf("行高 = %d，期望 96", rh)
	}
	if got := 2*th.Grid.MarginX + th.Grid.Columns*cw + (th.Grid.Columns-1)*th.Grid.Gutter; got != th.Canvas.Width {
		t.Errorf("横向等式不成立: %d ≠ %d", got, th.Canvas.Width)
	}
	if got := 2*th.Grid.MarginY + th.Grid.Rows*rh + (th.Grid.Rows-1)*th.Grid.Gutter; got != th.Canvas.Height {
		t.Errorf("纵向等式不成立: %d ≠ %d", got, th.Canvas.Height)
	}
}

func TestGridRejectsUnevenParameters(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Theme)
		want   string
	}{
		{"gutter 不整除", func(th *Theme) { th.Grid.Gutter = 25 }, "不能整除"},
		{"边距过大", func(th *Theme) { th.Grid.MarginX = 960 }, "容不下"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			th := cloneTheme(Default())
			c.mutate(&th)
			err := th.Verify()
			if err == nil {
				t.Fatalf("期望校验失败，实际通过")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息 %q 未包含 %q", err.Error(), c.want)
			}
		})
	}
}

func TestCellRectAtEdges(t *testing.T) {
	th := Default()
	// 最右列最下行，单格：右下角必须贴住安全区边界。
	r, err := th.Grid.CellRect(th.Canvas, 12, 1, 8, 1)
	if err != nil {
		t.Fatalf("末格换算失败: %v", err)
	}
	if got, want := r.X+r.Width, th.Canvas.Width-th.Grid.MarginX; got != want {
		t.Errorf("右边界 = %d，期望 %d", got, want)
	}
	if got, want := r.Y+r.Height, th.Canvas.Height-th.Grid.MarginY; got != want {
		t.Errorf("下边界 = %d，期望 %d", got, want)
	}

	if _, err := th.Grid.CellRect(th.Canvas, 11, 3, 1, 1); err == nil {
		t.Error("col=11 span=3 应越界")
	}
	if _, err := th.Grid.CellRect(th.Canvas, 1, 1, 8, 2); err == nil {
		t.Error("row=8 span=2 应越界")
	}
	if _, err := th.Grid.CellRect(th.Canvas, 0, 1, 1, 1); err == nil {
		t.Error("col=0 非法（1 基）")
	}
}

func TestTypeScaleBodyFloor(t *testing.T) {
	th := cloneTheme(Default())
	for i := range th.Type {
		if th.Type[i].Name == "body" {
			th.Type[i].Size = 28
		}
	}
	err := th.Verify()
	if err == nil {
		t.Fatal("body 降到 28px 应被拒绝")
	}
	if !strings.Contains(err.Error(), "低于下限") {
		t.Errorf("错误信息应说明下限，实际: %v", err)
	}
}

func TestTypeScaleMustDescend(t *testing.T) {
	th := cloneTheme(Default())
	th.Type[2].Size = th.Type[1].Size + 10 // h2 > h1
	if err := th.Verify(); err == nil {
		t.Fatal("字阶未严格递减应被拒绝")
	}
}

func TestTypeScaleNeedsBodyToken(t *testing.T) {
	th := cloneTheme(Default())
	for i := range th.Type {
		th.Type[i].Body = false
	}
	if err := th.Verify(); err == nil {
		t.Fatal("缺少正文档位应被拒绝")
	}
}

func TestPaletteAllReadablePairsPass(t *testing.T) {
	th := Default()
	papers := th.Papers()
	if len(papers) == 0 {
		t.Fatal("缺少背景色")
	}
	checked := 0
	for _, c := range th.Colors {
		if !c.Readable {
			continue
		}
		for _, p := range papers {
			got := ContrastRatio(c.RGB, p.RGB)
			if got < MinContrastRatio {
				t.Errorf("%s 在 %s 上 %.2f:1 < %.1f:1", c.Name, p.Name, got, MinContrastRatio)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("没有可读组合被检查，说明色板标记有问题")
	}
}

func TestPaletteRejectsUnreadableInk(t *testing.T) {
	th := cloneTheme(Default())
	th.Colors = append(th.Colors, ColorToken{
		Name: "inkFaint", RGB: MustParseHex("#94A3B8"), Role: RoleInk, Readable: true,
	})
	err := th.Verify()
	if err == nil {
		t.Fatal("#94A3B8 在白底上不可读，应被拒绝")
	}
	if !strings.Contains(err.Error(), "inkFaint") {
		t.Errorf("错误应定位到 inkFaint，实际: %v", err)
	}
}

func TestPaletteRejectsUnknownRole(t *testing.T) {
	th := cloneTheme(Default())
	th.Colors = append(th.Colors, ColorToken{Name: "x", RGB: MustParseHex("#000000"), Role: "decor"})
	if err := th.Verify(); err == nil {
		t.Fatal("未知用途应被拒绝")
	}
}

func TestSpaceScaleIsMultipleOf4(t *testing.T) {
	th := Default()
	if len(th.Space) == 0 {
		t.Fatal("间距标尺为空")
	}
	for i, v := range th.Space {
		if v%4 != 0 {
			t.Errorf("间距第 %d 项 %d 不是 4 的倍数", i, v)
		}
		if i > 0 && v <= th.Space[i-1] {
			t.Errorf("间距未严格递增: %d → %d", th.Space[i-1], v)
		}
	}
}

func TestParseHex(t *testing.T) {
	ok := map[string]string{
		"#FFF":      "#FFFFFF",
		"#000000":   "#000000",
		"#0f172a":   "#0F172A",
		" #4338ca ": "#4338CA",
	}
	for in, want := range ok {
		c, err := ParseHex(in)
		if err != nil {
			t.Fatalf("ParseHex(%q) 失败: %v", in, err)
		}
		if c.Hex() != want {
			t.Errorf("ParseHex(%q) = %s，期望 %s", in, c.Hex(), want)
		}
	}
	for _, bad := range []string{"0f172a", "#0f172", "#0f172azz", ""} {
		if _, err := ParseHex(bad); err == nil {
			t.Errorf("ParseHex(%q) 应失败", bad)
		}
	}
}
