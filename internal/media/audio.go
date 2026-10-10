package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

type pcmWAV struct {
	sampleRate uint32
	channels   uint16
	blockAlign uint16
	data       []byte
}

// AssembleTimelineWAV places PCM16 WAV clips at the sample positions defined
// by the timeline. Unoccupied lead-in, gap, and tail regions remain silent.
// It returns the assembled WAV bytes; callers that want to bound memory for
// large timelines should use WriteTimelineWAV instead.
func AssembleTimelineWAV(timeline *Timeline, clips map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteTimelineWAV(&buf, timeline, clips); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteTimelineWAV places PCM16 WAV clips at the sample positions defined by
// the timeline and writes the assembled PCM16 WAV into w. When w implements
// io.WriteSeeker (e.g. *os.File), each clip is written at its exact offset via
// WriteAt, so the assembled output is never materialized in memory. For a
// non-seekable writer it falls back to buffering the whole output.
func WriteTimelineWAV(w io.Writer, timeline *Timeline, clips map[string][]byte) error {
	if timeline == nil || timeline.DurationUS <= 0 || len(timeline.Slides) == 0 {
		return errors.New("media: non-empty timeline is required")
	}
	parsed := make(map[string]*pcmWAV, len(clips))
	var sampleRate uint32
	var channels, blockAlign uint16
	var previousEnd int64
	for _, slide := range timeline.Slides {
		if slide.StartUS < previousEnd || slide.EndUS <= slide.StartUS || slide.EndUS > timeline.DurationUS {
			return errors.New("media: invalid slide intervals")
		}
		segmentEnd := slide.StartUS
		for _, segment := range slide.Segments {
			if segment.AudioKey == "" || segment.StartUS < segmentEnd || segment.StartUS < slide.StartUS || segment.EndUS <= segment.StartUS || segment.EndUS > slide.EndUS {
				return errors.New("media: invalid audio segment interval")
			}
			segmentEnd = segment.EndUS
			wav, ok := parsed[segment.AudioKey]
			if !ok {
				data, exists := clips[segment.AudioKey]
				if !exists {
					return fmt.Errorf("media: audio clip %q is missing", segment.AudioKey)
				}
				var err error
				wav, err = parsePCM16WAV(data)
				if err != nil {
					return fmt.Errorf("media: audio clip %q: %w", segment.AudioKey, err)
				}
				parsed[segment.AudioKey] = wav
			}
			if sampleRate == 0 {
				sampleRate, channels, blockAlign = wav.sampleRate, wav.channels, wav.blockAlign
			} else if wav.sampleRate != sampleRate || wav.channels != channels || wav.blockAlign != blockAlign {
				return errors.New("media: all audio clips must use the same sample rate and channel layout")
			}
			expectedFrames := samplePosition(segment.EndUS-segment.StartUS, sampleRate)
			actualFrames := int64(len(wav.data)) / int64(wav.blockAlign)
			// 时间轴时长以整毫秒声明，而 WAV 实际时长含亚毫秒尾差（解码按
			// float 秒折算、再取整毫秒，最多引入约 1ms 误差）。允许 2ms 容差，
			// 仍能拦截真正错误的素材（时长差以毫秒计）。
			toleranceFrames := samplePosition(2_000, sampleRate)
			if distance(expectedFrames, actualFrames) > toleranceFrames {
				return fmt.Errorf("media: audio clip %q duration differs from timeline", segment.AudioKey)
			}
		}
		previousEnd = slide.EndUS
	}
	if sampleRate == 0 {
		return errors.New("media: timeline contains no audio segments")
	}
	totalFrames := samplePosition(timeline.DurationUS, sampleRate)
	dataSize := totalFrames * int64(blockAlign)
	maxInt := int64(^uint(0) >> 1)
	if dataSize < 0 || dataSize > int64(math.MaxUint32)-36 || dataSize > maxInt-44 {
		return errors.New("media: assembled WAV is too large")
	}
	return writeTimelineWAV(w, timeline, parsed, sampleRate, channels, blockAlign, dataSize)
}

func writeTimelineWAV(w io.Writer, timeline *Timeline, parsed map[string]*pcmWAV, sampleRate uint32, channels, blockAlign uint16, dataSize int64) error {
	if ws, ok := w.(io.WriteSeeker); ok {
		return writeTimelineWAVSeekable(ws, timeline, parsed, sampleRate, channels, blockAlign, dataSize)
	}
	// 非 seekable writer：先整块拼接后写（内存一次，但调用方无需自己再分配）。
	buf := &bufSeeker{buf: make([]byte, 44+int(dataSize))}
	if err := writeTimelineWAVSeekable(buf, timeline, parsed, sampleRate, channels, blockAlign, dataSize); err != nil {
		return err
	}
	_, err := w.Write(buf.buf)
	return err
}

// bufSeeker 把预分配切片包装成 io.WriteSeeker，供非 seekable 目标的内存拼接回退。
type bufSeeker struct {
	buf []byte
	off int64
}

func (b *bufSeeker) Write(p []byte) (int, error) {
	n := copy(b.buf[b.off:], p)
	b.off += int64(n)
	return n, nil
}

func (b *bufSeeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = b.off
	case io.SeekEnd:
		base = int64(len(b.buf))
	default:
		return 0, fmt.Errorf("media: invalid seek whence %d", whence)
	}
	b.off = base + offset
	return b.off, nil
}

func writeTimelineWAVSeekable(ws io.WriteSeeker, timeline *Timeline, parsed map[string]*pcmWAV, sampleRate uint32, channels, blockAlign uint16, dataSize int64) error {
	header := make([]byte, 44)
	writeWAVHeader(header, sampleRate, channels, blockAlign, uint32(dataSize))
	if _, err := ws.Write(header); err != nil {
		return err
	}
	// 先把数据区整体清零（留白 = 静音），再逐 clip 写帧。
	silence := make([]byte, blockAlign*4096)
	written := 0
	total := int(dataSize)
	for written < total {
		chunk := silence
		if len(chunk) > total-written {
			chunk = chunk[:total-written]
		}
		n, err := ws.Write(chunk)
		written += n
		if err != nil {
			return err
		}
	}
	for _, slide := range timeline.Slides {
		for _, segment := range slide.Segments {
			wav := parsed[segment.AudioKey]
			startFrame := samplePosition(segment.StartUS, sampleRate)
			expectedFrames := samplePosition(segment.EndUS-segment.StartUS, sampleRate)
			copyFrames := minInt64(expectedFrames, int64(len(wav.data))/int64(blockAlign))
			srcStart := 44 + startFrame*int64(blockAlign)
			if _, err := ws.Seek(srcStart, io.SeekStart); err != nil {
				return err
			}
			if _, err := ws.Write(wav.data[:int(copyFrames*int64(blockAlign))]); err != nil {
				return err
			}
		}
	}
	// 回到文件头重写 dataSize（组装中途未知最终大小，但 dataSize 已在校验期确定）。
	if _, err := ws.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := ws.Write(header); err != nil {
		return err
	}
	return nil
}

func parsePCM16WAV(data []byte) (*pcmWAV, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("invalid WAV header")
	}
	var format uint16
	var sampleRate uint32
	var channels, bits, blockAlign uint16
	var pcm []byte
	for offset := 12; offset+8 <= len(data); {
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start, end := offset+8, offset+8+size
		if size < 0 || end < start || end > len(data) {
			return nil, errors.New("invalid WAV chunk size")
		}
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("invalid WAV fmt chunk")
			}
			format = binary.LittleEndian.Uint16(data[start : start+2])
			channels = binary.LittleEndian.Uint16(data[start+2 : start+4])
			sampleRate = binary.LittleEndian.Uint32(data[start+4 : start+8])
			blockAlign = binary.LittleEndian.Uint16(data[start+12 : start+14])
			bits = binary.LittleEndian.Uint16(data[start+14 : start+16])
		case "data":
			pcm = data[start:end]
		}
		offset = end + size%2
	}
	if format != 1 || bits != 16 || sampleRate == 0 || channels == 0 || blockAlign != channels*2 || sampleRate > math.MaxUint32/uint32(blockAlign) || len(pcm)%int(blockAlign) != 0 {
		return nil, errors.New("only PCM16 WAV with a complete frame layout is supported")
	}
	return &pcmWAV{sampleRate: sampleRate, channels: channels, blockAlign: blockAlign, data: pcm}, nil
}

func writeWAVHeader(header []byte, sampleRate uint32, channels, blockAlign uint16, dataSize uint32) {
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataSize)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], channels)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*uint32(blockAlign))
	binary.LittleEndian.PutUint16(header[32:34], blockAlign)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)
}

func samplePosition(microseconds int64, sampleRate uint32) int64 {
	seconds := microseconds / 1_000_000
	remainder := microseconds % 1_000_000
	return seconds*int64(sampleRate) + (remainder*int64(sampleRate)+500_000)/1_000_000
}

func distance(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
