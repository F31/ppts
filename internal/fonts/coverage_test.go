package fonts

import (
	"encoding/binary"
	"testing"
)

// 这些测试不读真实字体文件：仓库的 testdata 只有几十 KB 量级，塞一份子集 OTF
// 会让每个 clone 都背上一个二进制。改为**手工构造** SFNT 字节——本解析器只读
// cmap 一个表，不需要 glyf/CFF/name 等其余表，所以「合法输入」的门槛就是
// 「表目录 + cmap」两部分，几十行就能构造出覆盖全部分支的样本。

type testSeg struct {
	start, end uint16
	delta      int16
	// glyphs 非空时走 idRangeOffset 间接寻址，长度应等于区间宽度；
	// 为空时走 idDelta 直接寻址。
	glyphs []uint16
}

// buildFormat4 按规范布局拼一张 format 4 子表。
func buildFormat4(segs []testSeg) []byte {
	segCount := len(segs)
	glyphTotal := 0
	for _, s := range segs {
		glyphTotal += len(s.glyphs)
	}
	size := 16 + segCount*8 + glyphTotal*2
	b := make([]byte, size)

	binary.BigEndian.PutUint16(b[0:], 4)
	binary.BigEndian.PutUint16(b[2:], uint16(size))
	binary.BigEndian.PutUint16(b[4:], 0) // language
	binary.BigEndian.PutUint16(b[6:], uint16(segCount*2))
	binary.BigEndian.PutUint16(b[8:], 0) // searchRange（本解析器不读，置 0）
	binary.BigEndian.PutUint16(b[10:], 0)
	binary.BigEndian.PutUint16(b[12:], 0)

	endBase := 14
	startBase := endBase + segCount*2 + 2
	deltaBase := startBase + segCount*2
	rangeBase := deltaBase + segCount*2
	glyphBase := rangeBase + segCount*2

	glyphAt := 0
	for i, s := range segs {
		binary.BigEndian.PutUint16(b[endBase+2*i:], s.end)
		binary.BigEndian.PutUint16(b[startBase+2*i:], s.start)
		binary.BigEndian.PutUint16(b[deltaBase+2*i:], uint16(s.delta))
		if len(s.glyphs) == 0 {
			continue
		}
		// idRangeOffset 的基准是数组起点：从本字段到 glyphIdArray 起点的距离是
		// 2*(segCount-i)，再加上本段之前的字形数。
		off := 2*(segCount-i) + 2*glyphAt
		binary.BigEndian.PutUint16(b[rangeBase+2*i:], uint16(off))
		for j, g := range s.glyphs {
			binary.BigEndian.PutUint16(b[glyphBase+2*(glyphAt+j):], g)
		}
		glyphAt += len(s.glyphs)
	}
	return b
}

func buildFormat12(groups [][3]uint32) []byte {
	size := 16 + len(groups)*12
	b := make([]byte, size)
	binary.BigEndian.PutUint16(b[0:], 12)
	binary.BigEndian.PutUint16(b[2:], 0) // reserved
	binary.BigEndian.PutUint32(b[4:], uint32(size))
	binary.BigEndian.PutUint32(b[8:], 0) // language
	binary.BigEndian.PutUint32(b[12:], uint32(len(groups)))
	for i, g := range groups {
		at := 16 + i*12
		binary.BigEndian.PutUint32(b[at:], g[0])
		binary.BigEndian.PutUint32(b[at+4:], g[1])
		binary.BigEndian.PutUint32(b[at+8:], g[2])
	}
	return b
}

// buildSFNT 用显式的子表列表构造完整字体字节。
func buildSFNT(subs []testSub) []byte {
	headerLen := 4 + len(subs)*8
	offset := headerLen
	cmap := make([]byte, headerLen)
	binary.BigEndian.PutUint16(cmap[0:], 0)
	binary.BigEndian.PutUint16(cmap[2:], uint16(len(subs)))
	for i, s := range subs {
		binary.BigEndian.PutUint16(cmap[4+i*8:], s.platform)
		binary.BigEndian.PutUint16(cmap[6+i*8:], s.encoding)
		binary.BigEndian.PutUint32(cmap[8+i*8:], uint32(offset))
		cmap = append(cmap, s.data...)
		offset += len(s.data)
	}

	buf := make([]byte, 12+16+len(cmap))
	binary.BigEndian.PutUint32(buf[0:], 0x00010000)
	binary.BigEndian.PutUint16(buf[4:], 1)
	copy(buf[12:16], "cmap")
	binary.BigEndian.PutUint32(buf[20:], 12+16)
	binary.BigEndian.PutUint32(buf[24:], uint32(len(cmap)))
	copy(buf[28:], cmap)
	return buf
}

type testSub struct {
	platform, encoding uint16
	data               []byte
}

func TestFontCoverageFormat4(t *testing.T) {
	f4 := buildFormat4([]testSeg{
		// 'A'-'C' → gid 1..3，idDelta 直接寻址
		{start: 0x41, end: 0x43, delta: 1 - 0x41},
		// 一-三 → 间接寻址：一=5，丁=0（.notdef），丂=7
		{start: 0x4E00, end: 0x4E02, delta: 0, glyphs: []uint16{5, 0, 7}},
		// 规范要求的终止段
		{start: 0xFFFF, end: 0xFFFF, delta: 1},
	})
	font := buildSFNT([]testSub{{platform: 3, encoding: 1, data: f4}})

	cov, err := ReadFontCoverage(font)
	if err != nil {
		t.Fatalf("ReadFontCoverage: %v", err)
	}
	if got, want := cov.Tables(), []string{"format4"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("tables = %v, want %v", got, want)
	}

	for _, r := range []rune{'A', 'B', 'C', 0x4E00, 0x4E02} {
		if !cov.Has(r) {
			t.Errorf("Has(%U) = false, want true", r)
		}
	}
	// 'D' 不在任何段里；0x4E01 的 gid 是 0（.notdef），必须判为缺字。
	// 这两条是 cmap 解析最容易糊弄过去的地方：前者查段，后者查字形号。
	for _, r := range []rune{'D', 0x4E01, 0x4E03, 0x4DFF} {
		if cov.Has(r) {
			t.Errorf("Has(%U) = true, want false", r)
		}
	}
}

func TestFontCoverageFormat12(t *testing.T) {
	f12 := buildFormat12([][3]uint32{
		{0x20000, 0x20002, 0x10}, // gid 0x10,0x11,0x12
		{0x30000, 0x30000, 0},    // gid 0 → .notdef
	})
	font := buildSFNT([]testSub{{platform: 3, encoding: 10, data: f12}})

	cov, err := ReadFontCoverage(font)
	if err != nil {
		t.Fatalf("ReadFontCoverage: %v", err)
	}
	for _, r := range []rune{0x20000, 0x20001, 0x20002} {
		if !cov.Has(r) {
			t.Errorf("Has(%U) = false, want true", r)
		}
	}
	for _, r := range []rune{0x20003, 0x1FFFF, 0x30000} {
		if cov.Has(r) {
			t.Errorf("Has(%U) = true, want false", r)
		}
	}
}

// 同一字体里 BMP 在 format 4、补充平面在 format 12 是常见布局。
// 只读一张子表就会把另一半判成缺字——这条用例锁住「并集」而不是「择优」。
func TestFontCoverageMergesSubtables(t *testing.T) {
	f4 := buildFormat4([]testSeg{
		{start: 0x41, end: 0x41, delta: 1 - 0x41},
		{start: 0xFFFF, end: 0xFFFF, delta: 1},
	})
	f12 := buildFormat12([][3]uint32{{0x20000, 0x20000, 9}})
	font := buildSFNT([]testSub{
		{platform: 3, encoding: 1, data: f4},
		{platform: 3, encoding: 10, data: f12},
	})

	cov, err := ReadFontCoverage(font)
	if err != nil {
		t.Fatalf("ReadFontCoverage: %v", err)
	}
	if !cov.Has('A') || !cov.Has(0x20000) {
		t.Fatalf("并集失效：BMP=%v 补充平面=%v", cov.Has('A'), cov.Has(0x20000))
	}
}

func TestFontCoverageRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"空":        nil,
		"太短":       []byte{0, 1, 0, 0},
		"非 SFNT":   []byte("PK\x03\x04 this is a zip"),
		"表目录越界":    append([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}, make([]byte, 4)...),
		"无 cmap 表": buildSFNT(nil),
	}
	for name, data := range cases {
		if _, err := ReadFontCoverage(data); err == nil {
			t.Errorf("%s: 期望报错，实际通过", name)
		}
	}
}

func TestMissingPreservesOrder(t *testing.T) {
	f4 := buildFormat4([]testSeg{
		{start: 0x41, end: 0x41, delta: 1 - 0x41},
		{start: 0xFFFF, end: 0xFFFF, delta: 1},
	})
	cov, err := ReadFontCoverage(buildSFNT([]testSub{{platform: 3, encoding: 1, data: f4}}))
	if err != nil {
		t.Fatal(err)
	}
	got := cov.Missing([]rune{'B', 'A', 0x4E01, 'C'})
	want := []rune{'B', 0x4E01, 'C'}
	if len(got) != len(want) {
		t.Fatalf("missing = %U, want %U", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing[%d] = %U, want %U", i, got[i], want[i])
		}
	}
}
