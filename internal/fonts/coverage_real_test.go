package fonts

import (
	"os"
	"testing"
)

// 合成样本能证明解析逻辑对，但证明不了「它对真实字体也对」：真实 Noto CJK 是
// CFF 轮廓、cmap 同时带 format 4 与 format 12，字形号远大于合成样本的个位数。
// 这条用例读一份真实母版，断言已知存在的字与已知不存在的字。
//
// 默认跳过（仓库不背 11MB 二进制）。门禁点设在这里的原因：CI 没有字体，
// 交付链路的真正验证在渲染镜像构建期（Dockerfile 的 fonts 自检那一步）。
func TestReadFontCoverageRealFont(t *testing.T) {
	base := os.Getenv("PPTS_FONT_BASE")
	if base == "" {
		t.Skip("PPTS_FONT_BASE 未设置")
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("读母版: %v", err)
	}
	cov, err := ReadFontCoverage(data)
	if err != nil {
		t.Fatalf("ReadFontCoverage: %v", err)
	}
	t.Logf("子表: %v", cov.Tables())

	present := []rune{'A', 'z', '0', ' ', 0x3002, 0xFF0C, '一', '囧', '龘', 0x9FA5}
	for _, r := range present {
		if !cov.Has(r) {
			t.Errorf("Has(%q/%U) = false, want true", r, r)
		}
	}
	// 母版是 charset.txt 的产物，明确不收 CJK Ext-B 与 emoji。
	for _, r := range []rune{0x20000, 0x1F600, 0xE000} {
		if cov.Has(r) {
			t.Errorf("Has(%U) = true, want false（母版不该收这个区）", r)
		}
	}
}
