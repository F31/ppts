package render

// 片段编码契约的容器侧解析。
//
// 背景：规格要求片段是「H.264 / yuv420p / GOP 30」，但这条要求此前只活在
// `renderer/render.mjs` 的三个字面量里（`codec: 'h264'`、`pixelFormat: 'yuv420p'`、
// `gopSize` 取自 `--gop`）。没有人读它、没有人验它，于是它写错的表现要等到
// 「拼接时偶发花屏」才暴露——而样片恰好每页都短、每段都从关键帧起，把这个问题盖住了。
//
// 为什么在 Go 里解析容器而不是调 ffprobe：运行时镜像里没有 ffmpeg
// （见 deploy/renderer/Dockerfile 的 base 段），而这条契约必须在**镜像自检**里就能验。
// 装一个 ffprobe 只为读三个字段，等于给镜像再添几十 MB 与一片 CVE 面。
//
// 只解三件事，多一件都不解：
//   - 编码格式：`stsd` 里第一个 sample entry 的类型（`avc1`）；
//   - 色度取样：`avcC` 里 SPS 的 `chroma_format_idc`（1 = 4:2:0）；
//   - 关键帧间隔：`stss` 里同步样本号的相邻差（GOP 30）。
// 码率、像素质量、时长都不属于这条契约，也就不在这里判——判了就要跟着编码器漂。

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// maxProbeBytes 是容器解析愿意读进内存的上限。一个 1080p 的十秒片段通常
// 5–15MB，128MB 是「明显不是正常片段」的分界线；超过它说明拿错文件了，
// 早报比慢慢读好。
const maxProbeBytes = 128 << 20

// SegmentEncoding 是一个片段实测到的编码参数。
type SegmentEncoding struct {
	Codec         string // stsd 里第一个 sample entry 的类型：avc1 / avc3 / …
	ChromaFormat  int    // SPS chroma_format_idc：0 灰度 1 4:2:0 2 4:2:2 3 4:4:4
	GOP           int    // 关键帧最大间隔（帧，含片尾那段）；全帧内编码时为 1
	Keyframes     int    // 关键帧数量
	FirstKeyframe int    // 第一个关键帧的样本号（1 基）
	Frames        int    // 帧数（stsz 的 sample_count）
	HasAudio      bool   // 容器里有 `soun` 轨道
}

// SegmentEncodingContract 是 render.mjs 应当产出的参数。
//
// GOP 给的是**上限**：x264 遇到场景切换会插入额外的关键帧（间隔变短），
// 那是正常的编码行为；反过来的「间隔变长」才会让按页 seek 变慢、
// 让拼接处的首帧落在 P 帧上。
var SegmentEncodingContract = SegmentEncoding{
	Codec:        "avc1",
	ChromaFormat: 1,
	GOP:          30,
}

// ChromaName 把 chroma_format_idc 折成常用叫法，只用于报错文案。
func (s SegmentEncoding) ChromaName() string {
	switch s.ChromaFormat {
	case 0:
		return "gray"
	case 1:
		return "yuv420p"
	case 2:
		return "yuv422p"
	case 3:
		return "yuv444p"
	default:
		return fmt.Sprintf("chroma_format_idc=%d", s.ChromaFormat)
	}
}

// KeyframeText 用「个数 + 最大间隔」描述关键帧分布，用于自检输出。
func (s SegmentEncoding) KeyframeText() string {
	return fmt.Sprintf("%d keyframes, max gap %d", s.Keyframes, s.GOP)
}

// CheckSegmentEncoding 实测一个片段，并与契约比较。
//
// 返回实测值是为了让调用方能把「实际是什么」打进日志——只报「不符合契约」
// 而不说实测值，等于让人回去再跑一遍。
func CheckSegmentEncoding(path string, want SegmentEncoding) (SegmentEncoding, error) {
	got, err := ProbeSegmentEncoding(path)
	if err != nil {
		return got, err
	}
	if want.Codec != "" && got.Codec != want.Codec {
		return got, fmt.Errorf("segment codec = %q, want %q", got.Codec, want.Codec)
	}
	if want.ChromaFormat > 0 && got.ChromaFormat != want.ChromaFormat {
		return got, fmt.Errorf("segment chroma = %s, want %s",
			got.ChromaName(), want.ChromaName())
	}
	if got.HasAudio {
		// 不做成契约里的可选开关：片段一律不带音轨（音频由 L5 统一混音）。
		// 有音轨会让 concat 的流布局不一致，拼接要么重编码、要么静默丢音。
		return got, errors.New("segment carries an audio track, want video only")
	}
	if got.FirstKeyframe != 0 && got.FirstKeyframe != 1 {
		// 片头没有关键帧 = 前几帧解不出来。片段是独立编码的，第 1 帧必须是 IDR。
		return got, fmt.Errorf("segment first keyframe at sample %d, want 1", got.FirstKeyframe)
	}
	if want.GOP > 0 && got.GOP > want.GOP {
		return got, fmt.Errorf("segment keyframe gap = %d frames, want ≤%d", got.GOP, want.GOP)
	}
	return got, nil
}

// ProbeSegmentEncoding 解析片段头部，读出色度与关键帧分布。
func ProbeSegmentEncoding(path string) (SegmentEncoding, error) {
	var enc SegmentEncoding
	info, err := os.Stat(path)
	if err != nil {
		return enc, fmt.Errorf("probe segment: %w", err)
	}
	if info.Size() > maxProbeBytes {
		return enc, fmt.Errorf("probe segment: %d bytes exceeds %d limit", info.Size(), maxProbeBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return enc, fmt.Errorf("probe segment: %w", err)
	}
	if len(data) < 8 {
		return enc, errors.New("probe segment: file too short")
	}

	moov := findBox(data, 0, int64(len(data)), "moov")
	if moov == nil {
		return enc, errors.New("probe segment: no moov box (not a finalized mp4)")
	}

	// 一个片段可能有多条轨道。片段约定不带音轨（音频由 L5 统一混音），
	// 但仍按 handler 找 vide，避免哪天加了音轨就静默解到第一个 trak。
	// 片段约定不带音轨（音频由 L5 统一混音）：有音轨会让 concat 的流布局不一致，
	// 拼接时要么重编码、要么静默丢音。这里顺带记下来，交给契约去判。
	var found, hasAudio bool
	walkBoxes(data, moov.payload, moov.end, func(b mp4Box) bool {
		if b.typ != "trak" {
			return true
		}
		switch trakHandler(data, b) {
		case "soun":
			hasAudio = true
		case "vide":
			if got, ok := probeTrak(data, b); ok {
				enc, found = got, true
			}
		}
		return true
	})
	if !found {
		return enc, errors.New("probe segment: no video track")
	}
	enc.HasAudio = hasAudio
	return enc, nil
}

// probeTrak 解析一条轨道；不是视频轨返回 false。
func probeTrak(data []byte, trak mp4Box) (SegmentEncoding, bool) {
	var enc SegmentEncoding
	mdia := findBox(data, trak.payload, trak.end, "mdia")
	if mdia == nil {
		return enc, false
	}
	if trakHandler(data, trak) != "vide" {
		return enc, false
	}
	minf := findBox(data, mdia.payload, mdia.end, "minf")
	if minf == nil {
		return enc, false
	}
	stbl := findBox(data, minf.payload, minf.end, "stbl")
	if stbl == nil {
		return enc, false
	}

	if stsd := findBox(data, stbl.payload, stbl.end, "stsd"); stsd != nil {
		enc.Codec, enc.ChromaFormat = probeSampleDescription(data, *stsd)
	}
	if stsz := findBox(data, stbl.payload, stbl.end, "stsz"); stsz != nil && stsz.payload+12 <= stsz.end {
		enc.Frames = int(binary.BigEndian.Uint32(data[stsz.payload+8:]))
	}
	if stss := findBox(data, stbl.payload, stbl.end, "stss"); stss != nil && stss.payload+8 <= stss.end {
		enc.Keyframes, enc.GOP, enc.FirstKeyframe = probeSyncSamples(data, *stss, enc.Frames)
	} else {
		// 没有 stss = 每一帧都是同步样本（全帧内编码），间隔即 1。
		enc.Keyframes, enc.GOP = enc.Frames, 1
		enc.FirstKeyframe = 1
	}
	return enc, true
}

// trakHandler 返回轨道的 handler 类型（`vide` / `soun` / …），读不到时返回空串。
//
// 按 handler 而不是「第一个 trak」找视频轨：片段的轨道顺序没有契约保证，
// 而「第一个 trak 是视频」这个假设在加了音轨之后会静默解到音轨上。
func trakHandler(data []byte, trak mp4Box) string {
	mdia := findBox(data, trak.payload, trak.end, "mdia")
	if mdia == nil {
		return ""
	}
	hdlr := findBox(data, mdia.payload, mdia.end, "hdlr")
	// hdlr 载荷：version/flags(4) + pre_defined(4) + handler_type(4)。
	if hdlr == nil || hdlr.payload+12 > hdlr.end {
		return ""
	}
	return string(data[hdlr.payload+8 : hdlr.payload+12])
}

// probeSampleDescription 从 stsd 读编码类型，并尽力从 avcC 的 SPS 读色度取样。
func probeSampleDescription(data []byte, stsd mp4Box) (string, int) {
	// stsd 载荷：version/flags(4) + entry_count(4) + 若干 entry。
	if stsd.payload+16 > stsd.end {
		return "", 0
	}
	entry := stsd.payload + 8
	// entry：size(4) + format(4) + 后面的字段随类型而定。
	codec := string(data[entry+4 : entry+8])
	entrySize := int64(binary.BigEndian.Uint32(data[entry:]))
	entryEnd := entry + entrySize
	if entryEnd > stsd.end {
		entryEnd = stsd.end
	}
	if codec != "avc1" && codec != "avc3" {
		return codec, 0
	}
	// VisualSampleEntry 的固定字段 78 字节，子盒子（avcC）紧随其后。
	child := entry + 8 + 78
	var chroma int
	walkBoxes(data, child, entryEnd, func(b mp4Box) bool {
		if b.typ != "avcC" {
			return true
		}
		chroma = chromaFromAVCC(data[b.payload:b.end])
		return false
	})
	return codec, chroma
}

// chromaFromAVCC 从 AVCDecoderConfigurationRecord 里取 SPS，读出 chroma_format_idc。
//
// 取值失败返回 0：这里的用途是「校验契约」，读不到就当没读到，让调用方
// 按不匹配报错，而不是猜一个 1 混过去。
func chromaFromAVCC(avcc []byte) int {
	// version(1) profile(1) compat(1) level(1) lengthSizeMinusOne(1) numSPS(1)
	if len(avcc) < 8 {
		return 0
	}
	numSPS := int(avcc[5] & 0x1f)
	if numSPS == 0 {
		return 0
	}
	// 0xA5：SPS 数量之后紧跟 reserved(1) + length(2)? 不——这里只有 length(2)。
	if len(avcc) < 8 {
		return 0
	}
	spsLen := int(binary.BigEndian.Uint16(avcc[6:8]))
	if spsLen == 0 || 8+spsLen > len(avcc) {
		return 0
	}
	return chromaFromSPS(avcc[8 : 8+spsLen])
}

// chromaFromSPS 解 SPS 的前几个字段。
//
// SPS 是 RBSP：先去掉 1 字节 NAL 头，再去掉防竞争字节（00 00 03 → 00 00），
// 然后按指数哥伦布读 profile_idc / constraint / level_idc / sps_id，
// 高档次（High 及同族）才多一个 chroma_format_idc。
func chromaFromSPS(nal []byte) int {
	if len(nal) < 4 {
		return 0
	}
	rbsp := removeEmulationPrevention(nal[1:])
	br := bitReader{data: rbsp}
	// profile_idc 必须在这里取出：先跳过三个字节再取「第四个」，拿到的是
	// seq_parameter_set_id，任何高档次都会因此被当成 Baseline、一律回 4:2:0。
	// 这个错曾让 yuv444p 的用例静默通过（420 也是 1，两边一样）。
	profile, ok := br.bits(8)
	if !ok {
		return 0
	}
	if _, ok := br.bits(8); !ok { // constraint flags
		return 0
	}
	if _, ok := br.bits(8); !ok { // level_idc
		return 0
	}
	if _, ok := br.ue(); !ok { // seq_parameter_set_id
		return 0
	}
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma, ok := br.ue()
		if !ok {
			return 0
		}
		return int(chroma)
	default:
		// Baseline / Main / Extended 只有 4:2:0 一种。
		return 1
	}
}

// probeSyncSamples 读 stss，返回关键帧数量、相邻间隔的最大值，以及第一个关键帧的样本号。
//
// 「最大间隔」必须把**片尾那一段**算进去：一个 1500ms 的片段（45 帧）若只有开头一个
// 关键帧，相邻间隔表是空的——只看相邻差会得到 0，然后任何 GOP 契约都通过。
// 实测过：把 render.mjs 的 gopSize 改成 300，自检照样绿。片尾到最后一个关键帧的距离
// 同样是一段「解不动就得从头解」的区间，所以它是间隔的一部分。
func probeSyncSamples(data []byte, stss mp4Box, frames int) (keys, gop, first int) {
	count := int(binary.BigEndian.Uint32(data[stss.payload+4:]))
	if count <= 0 || stss.payload+8+int64(count)*4 > stss.end {
		return 0, 0, 0
	}
	first = 1
	prev := int64(0)
	for i := 0; i < count; i++ {
		// stss 里存的是 1 基的样本号。
		sample := int64(binary.BigEndian.Uint32(data[stss.payload+8+int64(i)*4:]))
		if i == 0 {
			first = int(sample)
			prev = sample
			continue
		}
		if gap := int(sample - prev); gap > gop {
			gop = gap
		}
		prev = sample
	}
	if frames > 0 && prev > 0 {
		if tail := frames - int(prev) + 1; tail > gop {
			gop = tail
		}
	}
	return count, gop, first
}

// mp4Box 描述一个已定位的盒子：payload 是载荷起点，end 是盒子末尾。
type mp4Box struct {
	typ     string
	payload int64
	end     int64
}

// walkBoxes 在 [start,end) 内顺序遍历盒子，fn 返回 false 时提前停止。
func walkBoxes(data []byte, start, end int64, fn func(mp4Box) bool) {
	for off := start; off+8 <= end; {
		size := int64(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		header := int64(8)
		switch size {
		case 0:
			// size 0 表示「一直到末尾」，只可能出现在最后一个盒子。
			size = end - off
		case 1:
			if off+16 > end {
				return
			}
			size = int64(binary.BigEndian.Uint64(data[off+8:]))
			header = 16
		}
		if size < header || off+size > end {
			// 盒子越界：容器坏了，或者我们走错了层。停止遍历，让上层按「没找到」处理。
			return
		}
		if !fn(mp4Box{typ: typ, payload: off + header, end: off + size}) {
			return
		}
		off += size
	}
}

// findBox 在 [start,end) 里找第一个类型匹配的盒子。
func findBox(data []byte, start, end int64, typ string) *mp4Box {
	var hit *mp4Box
	walkBoxes(data, start, end, func(b mp4Box) bool {
		if b.typ == typ {
			hit = &b
			return false
		}
		return true
	})
	return hit
}

// removeEmulationPrevention 去掉 RBSP 里的 00 00 03 防竞争字节。
func removeEmulationPrevention(in []byte) []byte {
	out := make([]byte, 0, len(in))
	zeros := 0
	for _, b := range in {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue
		}
		if b == 0x00 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, b)
	}
	return out
}

// bitReader 是指数哥伦布读数的极简实现，只服务 SPS 头部这几个字段。
type bitReader struct {
	data []byte
	pos  int // 位偏移
}

func (r *bitReader) bits(n int) (int, bool) {
	if r.pos+n > len(r.data)*8 {
		return 0, false
	}
	v := 0
	for i := 0; i < n; i++ {
		byteIdx := (r.pos + i) / 8
		bitIdx := 7 - (r.pos+i)%8
		v = v<<1 | int(r.data[byteIdx]>>uint(bitIdx)&1)
	}
	r.pos += n
	return v, true
}

// ue 读无符号指数哥伦布：前导零的个数决定后续位数。
func (r *bitReader) ue() (uint, bool) {
	zeros := 0
	for {
		b, ok := r.bits(1)
		if !ok {
			return 0, false
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, false
		}
	}
	if zeros == 0 {
		return 0, true
	}
	rest, ok := r.bits(zeros)
	if !ok {
		return 0, false
	}
	return uint(1)<<uint(zeros) - 1 + uint(rest), true
}
