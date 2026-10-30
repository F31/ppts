package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 样例片段由 ffmpeg 生成（命令记在注释里，便于怀疑时重造）：
//
//	ffmpeg -f lavfi -i "color=c=0x1E3A8A:s=64x36:d=1:r=30" \
//	  -c:v libx264 -pix_fmt yuv420p -g 10 -crf 30 -movflags +faststart seg_gop10_420.mp4
//	ffmpeg -f lavfi -i "color=c=0x0F766E:s=64x36:d=1:r=30" \
//	  -c:v libx264 -pix_fmt yuv444p -profile:v high444 -g 10 -crf 30 \
//	  -movflags +faststart seg_gop10_444.mp4
//
// 2400 字节一个，进仓库不心疼；从 lavfi 生成所以没有版权问题。

func TestProbeSegmentEncoding(t *testing.T) {
	cases := []struct {
		file   string
		codec  string
		chroma int
		gop    int
		frames int
		keys   int
	}{
		{"seg_gop10_420.mp4", "avc1", 1, 10, 30, 3},
		{"seg_gop10_444.mp4", "avc1", 3, 10, 30, 3},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got, err := ProbeSegmentEncoding(filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if got.Codec != c.codec {
				t.Errorf("codec = %q, want %q", got.Codec, c.codec)
			}
			if got.ChromaFormat != c.chroma {
				t.Errorf("chroma = %d (%s), want %d", got.ChromaFormat, got.ChromaName(), c.chroma)
			}
			if got.GOP != c.gop {
				t.Errorf("gop = %d, want %d", got.GOP, c.gop)
			}
			if got.Frames != c.frames {
				t.Errorf("frames = %d, want %d", got.Frames, c.frames)
			}
			if got.Keyframes != c.keys {
				t.Errorf("keyframes = %d, want %d", got.Keyframes, c.keys)
			}
		})
	}
}

func TestCheckSegmentEncoding(t *testing.T) {
	ok420 := filepath.Join("testdata", "seg_gop10_420.mp4")
	ok444 := filepath.Join("testdata", "seg_gop10_444.mp4")

	t.Run("契约内的片段通过", func(t *testing.T) {
		// 实测 GOP 10 ≤ 契约 30：x264 遇到场景切换会插关键帧，间隔只会变短，
		// 所以契约给的是上限，不是等号。
		if _, err := CheckSegmentEncoding(ok420, SegmentEncodingContract); err != nil {
			t.Fatalf("want pass, got %v", err)
		}
	})

	t.Run("GOP 超过上限即失败", func(t *testing.T) {
		_, err := CheckSegmentEncoding(ok420, SegmentEncoding{GOP: 5})
		if err == nil || !strings.Contains(err.Error(), "keyframe gap = 10") {
			t.Fatalf("want keyframe gap error, got %v", err)
		}
	})

	t.Run("色度不是 4:2:0 即失败", func(t *testing.T) {
		_, err := CheckSegmentEncoding(ok444, SegmentEncodingContract)
		if err == nil || !strings.Contains(err.Error(), "yuv444p") {
			t.Fatalf("want chroma error naming yuv444p, got %v", err)
		}
	})

	t.Run("编码不是 avc1 即失败", func(t *testing.T) {
		_, err := CheckSegmentEncoding(ok420, SegmentEncoding{Codec: "vp09"})
		if err == nil || !strings.Contains(err.Error(), `codec = "avc1"`) {
			t.Fatalf("want codec error, got %v", err)
		}
	})

	//	ffmpeg -f lavfi -i "color=c=0x1E3A8A:s=64x36:d=1:r=30" \
	//	  -f lavfi -i "anullsrc=r=44100:cl=stereo" -shortest \
	//	  -c:v libx264 -pix_fmt yuv420p -g 10 -c:a aac seg_withaudio.mp4
	t.Run("带音轨的片段即失败", func(t *testing.T) {
		got, err := ProbeSegmentEncoding(filepath.Join("testdata", "seg_withaudio.mp4"))
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if !got.HasAudio {
			t.Fatal("样例带音轨，HasAudio 应为 true——否则「无音轨」这条 MUST 等于没人守")
		}
		if _, err := CheckSegmentEncoding(filepath.Join("testdata", "seg_withaudio.mp4"), SegmentEncodingContract); err == nil {
			t.Fatal("带音轨的片段不该通过契约")
		}
	})

	// 这条是「洞」本身：整段只有一个关键帧时，相邻间隔表是空的。
	// 只算相邻差会得到 0，任何 GOP 契约都通过——把 render.mjs 的 gopSize 改成 300
	// 之后自检照样绿，实测过。所以间隔要把「最后一个关键帧到片尾」也算进去。
	//
	//	ffmpeg -f lavfi -i "color=c=0x1E3A8A:s=64x36:d=1.5:r=30" \
	//	  -c:v libx264 -pix_fmt yuv420p -g 300 -crf 30 seg_gop300.mp4   → 45 帧，1 个关键帧
	t.Run("整段只有一个关键帧时，片尾那段也要算间隔", func(t *testing.T) {
		got, err := ProbeSegmentEncoding(filepath.Join("testdata", "seg_gop300.mp4"))
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if got.Keyframes != 1 || got.Frames != 45 {
			t.Fatalf("样例应只有 1 个关键帧、45 帧，实际 %d / %d", got.Keyframes, got.Frames)
		}
		if got.GOP != 45 {
			t.Fatalf("GOP 应等于片长 45（片尾那一段没有关键帧），实际 %d", got.GOP)
		}
		if _, err := CheckSegmentEncoding(filepath.Join("testdata", "seg_gop300.mp4"), SegmentEncodingContract); err == nil {
			t.Fatal("45 帧无关键帧的片段不该通过 GOP 30 的契约")
		}
	})
}

func TestProbeSegmentEncodingRejectsGarbage(t *testing.T) {
	dir := t.TempDir()

	notMP4 := filepath.Join(dir, "garbage.mp4")
	if err := os.WriteFile(notMP4, []byte("this is not an mp4 container at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeSegmentEncoding(notMP4); err == nil || !strings.Contains(err.Error(), "no moov") {
		t.Fatalf("want no-moov error, got %v", err)
	}

	truncated := filepath.Join(dir, "short.mp4")
	if err := os.WriteFile(truncated, []byte{0, 0, 0, 8}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeSegmentEncoding(truncated); err == nil {
		t.Fatal("want error for 4-byte file")
	}

	if _, err := ProbeSegmentEncoding(filepath.Join(dir, "missing.mp4")); err == nil {
		t.Fatal("want error for missing file")
	}
}

// ftyp-only 的片段（fakeMP4Backend 产出的那种）能过容器校验，但读不出编码参数。
// 这条用例锁住「两种校验各管一段」这个边界，免得哪天有人把 CheckSegmentEncoding
// 塞进 verifyMP4，把一堆既有用例改成「假片段也得有真编码」。
func TestProbeSegmentEncodingNeedsMoov(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// size(4) + "ftyp" + major brand + 填充，与 verifyMP4 认得的最小结构一致。
	buf := []byte{
		0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0, 0, 2, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '2', 'm', 'p', '4', '1',
		0, 0, 0, 0,
	}
	// verifyMP4 还要求 ≥1024 字节（它拿大小当「不是空文件」的证据），这里补足。
	buf = append(buf, make([]byte, 1024)...)
	if _, err := f.Write(buf); err != nil {
		t.Fatal(err)
	}
	if err := verifyMP4(f.Name()); err != nil {
		t.Fatalf("verifyMP4 应通过（它只管容器结构）: %v", err)
	}
	if _, err := ProbeSegmentEncoding(f.Name()); err == nil {
		t.Fatal("ProbeSegmentEncoding 应报错（没有 moov 就没有编码参数）")
	}
}
