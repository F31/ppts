package fonts

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 这个文件是「V15 字体子集覆盖率 100%」的判据实现。
//
// 为什么要自己解析 cmap，而不是信任子集器返回的统计：门禁的价值在于**独立验证**。
// 子集器报告「保留了 200 个码位」是它自己的账本；如果它的过滤逻辑漏了某个字符，
// 账本会诚实地写下「保留了 199 个」，然后门禁照样绿——红不了的门禁等于没有。
// 唯一能证伪的做法是读产物本身：把 cmap 表解析出来，逐个查片的字符在不在里面。
//
// 只读 cmap 一个表，不引字体库：格式 4（BMP）与格式 12（UCS-4）覆盖了所有
// 现代字体的编码表，两者并集查询约 150 行，而一个完整字体解析器的依赖成本
// （含 CFF 轮廓）比这个功能大两个数量级。
//
// 不用 woff2 的原因也在这里：woff2 的 cmap 被 Brotli 压在 woff2 容器里，Go 标准库
// 没有 Brotli，校验就得再引一个解码器。实测同一份子集 OTF 打进 zip 是 75.5 KB、
// woff2 是 67.2 KB——12% 的体积差换掉一个解码依赖与一整类「解压失败怎么算」的分支，划算。

var (
	errFontNotSFNT = errors.New("fonts: not an SFNT file")
	errFontNoCmap  = errors.New("fonts: no usable cmap subtable")
)

// sfntTables 是 SFNT 表目录里的必要信息。
type sfntTables struct {
	version uint32
	offsets map[string][2]uint32 // tag -> {offset, length}
}

// parseSFNT 读表目录。TTC（'ttcf'）不支持：成品包里只会放单字面字体，
// 支持集合字体意味着「该用第几个字面」这个判断要落到调用方，而这里没有它的信息。
func parseSFNT(data []byte) (sfntTables, error) {
	if len(data) < 12 {
		return sfntTables{}, errFontNotSFNT
	}
	version := binary.BigEndian.Uint32(data[0:4])
	switch version {
	case 0x00010000, 0x74727565: // TrueType，或 Apple 的 'true'
	case 0x4F54544F: // 'OTTO'，CFF 轮廓
	default:
		return sfntTables{}, fmt.Errorf("%w: sfntVersion 0x%08X", errFontNotSFNT, version)
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if numTables <= 0 || 12+numTables*16 > len(data) {
		return sfntTables{}, fmt.Errorf("%w: table count %d", errFontNotSFNT, numTables)
	}
	t := sfntTables{version: version, offsets: make(map[string][2]uint32, numTables)}
	for i := 0; i < numTables; i++ {
		rec := data[12+i*16 : 12+i*16+16]
		tag := string(rec[0:4])
		off := binary.BigEndian.Uint32(rec[8:12])
		length := binary.BigEndian.Uint32(rec[12:16])
		if uint64(off)+uint64(length) > uint64(len(data)) {
			return sfntTables{}, fmt.Errorf("%w: table %q out of bounds", errFontNotSFNT, tag)
		}
		t.offsets[tag] = [2]uint32{off, length}
	}
	return t, nil
}

// seg4 是 cmap format 4 的一段区间。
type seg4 struct {
	start, end uint16
	delta      int16
	rangeOff   uint16
	// raw 是整张子表，fieldPos 是 idRangeOffset[i] 这个字段自身的绝对位置。
	//
	// 规范里偏移的基准是**字段自身地址**（`address of idRangeOffset[i]`），
	// 不是数组起点。按数组起点算在 i>0 时整体错位 2·i 字节：查出来的字形是邻居的，
	// 「有字形」的结论照样成立，错得非常安静——本文件最初的实现就踩了这条。
	raw      []byte
	fieldPos int
}

// cmapIndex 是一份字体里所有可用编码子表的并集。
//
// 并集而不是「挑最好的一张」：字体可能把 BMP 放在 format 4、把补充平面放在
// format 12，只读一张就会把另一半判成缺字。宁可比对宽一点，也不能冤枉字体。
type cmapIndex struct {
	segments []seg4
	groups   [][3]uint32 // start, end, startGlyphID
}

// ParseFontCoverage 返回该字体覆盖的码位集合的判定器。
type FontCoverage struct {
	index cmapIndex
	// tables 记录实际用到的子表格式，便于排查「为什么这个字查不到」。
	tables []string
}

// ReadFontCoverage 解析字体字节，得到可查询的覆盖率判定器。
//
// 返回值只用于查询，不保留对 data 的引用之外的任何状态，可安全复用。
func ReadFontCoverage(data []byte) (*FontCoverage, error) {
	dir, err := parseSFNT(data)
	if err != nil {
		return nil, err
	}
	entry, ok := dir.offsets["cmap"]
	if !ok {
		return nil, errFontNoCmap
	}
	cmap := data[entry[0] : entry[0]+entry[1]]
	if len(cmap) < 4 {
		return nil, errFontNoCmap
	}
	numTables := int(binary.BigEndian.Uint16(cmap[2:4]))
	if 4+numTables*8 > len(cmap) {
		return nil, fmt.Errorf("%w: encoding records truncated", errFontNoCmap)
	}
	cov := &FontCoverage{}
	seenOffsets := map[uint32]struct{}{}
	for i := 0; i < numTables; i++ {
		rec := cmap[4+i*8 : 4+i*8+8]
		off := binary.BigEndian.Uint32(rec[4:8])
		if off >= uint32(len(cmap)) {
			continue
		}
		// 同一张子表会被多个 encoding record 引用；重复解析没有意义。
		if _, dup := seenOffsets[off]; dup {
			continue
		}
		seenOffsets[off] = struct{}{}
		sub := cmap[off:]
		if len(sub) < 4 {
			continue
		}
		switch binary.BigEndian.Uint16(sub[0:2]) {
		case 4:
			if segs, ok := parseFormat4(sub); ok {
				cov.index.segments = append(cov.index.segments, segs...)
				cov.tables = append(cov.tables, "format4")
			}
		case 12:
			if groups, ok := parseFormat12(sub); ok {
				cov.index.groups = append(cov.index.groups, groups...)
				cov.tables = append(cov.tables, "format12")
			}
		}
	}
	if len(cov.index.segments) == 0 && len(cov.index.groups) == 0 {
		return nil, errFontNoCmap
	}
	return cov, nil
}

// Tables 返回参与判定的子表格式列表（如 ["format4","format12"]）。
func (c *FontCoverage) Tables() []string { return c.tables }

// Has 判断一个码位是否有字形。
//
// 判据是「映射到非 0 字形」：cmap 里存在映射但 glyph id 为 0，含义是 .notdef，
// 也就是方框。只查「码位在不在 cmap 里」会把 .notdef 当成覆盖，正好放过最该抓的缺陷。
func (c *FontCoverage) Has(r rune) bool {
	if r < 0 || r > 0x10FFFF {
		return false
	}
	if int64(r) <= 0xFFFF && c.segmentsHas(uint16(r)) {
		return true
	}
	return c.groupsHas(uint32(r))
}

func (c *FontCoverage) segmentsHas(ch uint16) bool {
	for _, s := range c.index.segments {
		if ch < s.start || ch > s.end {
			continue
		}
		if s.rangeOff == 0 {
			return (ch + uint16(s.delta)) != 0
		}
		pos := s.fieldPos + int(s.rangeOff) + 2*int(ch-s.start)
		if pos < 0 || pos+2 > len(s.raw) {
			return false
		}
		gid := binary.BigEndian.Uint16(s.raw[pos : pos+2])
		if gid == 0 {
			return false
		}
		return gid+uint16(s.delta) != 0
	}
	return false
}

func (c *FontCoverage) groupsHas(ch uint32) bool {
	for _, g := range c.index.groups {
		if ch < g[0] || ch > g[1] {
			continue
		}
		// startGlyphID + (ch - start) 为 0 表示 .notdef。
		return g[2]+(ch-g[0]) != 0
	}
	return false
}

// Missing 返回 cps 中没有字形的那些码位（保持输入顺序）。
func (c *FontCoverage) Missing(cps []rune) []rune {
	var missing []rune
	for _, r := range cps {
		if !c.Has(r) {
			missing = append(missing, r)
		}
	}
	return missing
}

func parseFormat4(sub []byte) ([]seg4, bool) {
	if len(sub) < 14 {
		return nil, false
	}
	segX2 := int(binary.BigEndian.Uint16(sub[6:8]))
	if segX2 == 0 || segX2%2 != 0 {
		return nil, false
	}
	segCount := segX2 / 2
	endBase := 14
	startBase := endBase + segX2 + 2 // 跳过 reservedPad
	deltaBase := startBase + segX2
	rangeBase := deltaBase + segX2
	if rangeBase+segX2 > len(sub) {
		return nil, false
	}
	segs := make([]seg4, 0, segCount)
	for i := 0; i < segCount; i++ {
		segs = append(segs, seg4{
			end:      binary.BigEndian.Uint16(sub[endBase+2*i : endBase+2*i+2]),
			start:    binary.BigEndian.Uint16(sub[startBase+2*i : startBase+2*i+2]),
			delta:    int16(binary.BigEndian.Uint16(sub[deltaBase+2*i : deltaBase+2*i+2])),
			rangeOff: binary.BigEndian.Uint16(sub[rangeBase+2*i : rangeBase+2*i+2]),
			raw:      sub,
			fieldPos: rangeBase + 2*i,
		})
	}
	return segs, true
}

func parseFormat12(sub []byte) ([][3]uint32, bool) {
	if len(sub) < 16 {
		return nil, false
	}
	n := binary.BigEndian.Uint32(sub[12:16])
	if n == 0 || uint64(16)+uint64(n)*12 > uint64(len(sub)) {
		return nil, false
	}
	groups := make([][3]uint32, 0, n)
	for i := uint32(0); i < n; i++ {
		b := 16 + i*12
		groups = append(groups, [3]uint32{
			binary.BigEndian.Uint32(sub[b : b+4]),
			binary.BigEndian.Uint32(sub[b+4 : b+8]),
			binary.BigEndian.Uint32(sub[b+8 : b+12]),
		})
	}
	return groups, true
}
