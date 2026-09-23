package media

import (
	"encoding/binary"
	"errors"
	"fmt"
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
func AssembleTimelineWAV(timeline *Timeline, clips map[string][]byte) ([]byte, error) {
	if timeline == nil || timeline.DurationUS <= 0 || len(timeline.Slides) == 0 {
		return nil, errors.New("media: non-empty timeline is required")
	}
	parsed := make(map[string]*pcmWAV, len(clips))
	var sampleRate uint32
	var channels, blockAlign uint16
	var previousEnd int64
	for _, slide := range timeline.Slides {
		if slide.StartUS < previousEnd || slide.EndUS <= slide.StartUS || slide.EndUS > timeline.DurationUS {
			return nil, errors.New("media: invalid slide intervals")
		}
		segmentEnd := slide.StartUS
		for _, segment := range slide.Segments {
			if segment.AudioKey == "" || segment.StartUS < segmentEnd || segment.StartUS < slide.StartUS || segment.EndUS <= segment.StartUS || segment.EndUS > slide.EndUS {
				return nil, errors.New("media: invalid audio segment interval")
			}
			segmentEnd = segment.EndUS
			wav, ok := parsed[segment.AudioKey]
			if !ok {
				data, exists := clips[segment.AudioKey]
				if !exists {
					return nil, fmt.Errorf("media: audio clip %q is missing", segment.AudioKey)
				}
				var err error
				wav, err = parsePCM16WAV(data)
				if err != nil {
					return nil, fmt.Errorf("media: audio clip %q: %w", segment.AudioKey, err)
				}
				parsed[segment.AudioKey] = wav
			}
			if sampleRate == 0 {
				sampleRate, channels, blockAlign = wav.sampleRate, wav.channels, wav.blockAlign
			} else if wav.sampleRate != sampleRate || wav.channels != channels || wav.blockAlign != blockAlign {
				return nil, errors.New("media: all audio clips must use the same sample rate and channel layout")
			}
			expectedFrames := samplePosition(segment.EndUS-segment.StartUS, sampleRate)
			actualFrames := int64(len(wav.data)) / int64(wav.blockAlign)
			// 时间轴时长以整毫秒声明，而 WAV 实际时长含亚毫秒尾差（解码按
			// float 秒折算、再取整毫秒，最多引入约 1ms 误差）。允许 2ms 容差，
			// 仍能拦截真正错误的素材（时长差以毫秒计）。
			toleranceFrames := samplePosition(2_000, sampleRate)
			if distance(expectedFrames, actualFrames) > toleranceFrames {
				return nil, fmt.Errorf("media: audio clip %q duration differs from timeline", segment.AudioKey)
			}
		}
		previousEnd = slide.EndUS
	}
	if sampleRate == 0 {
		return nil, errors.New("media: timeline contains no audio segments")
	}
	totalFrames := samplePosition(timeline.DurationUS, sampleRate)
	dataSize := totalFrames * int64(blockAlign)
	maxInt := int64(^uint(0) >> 1)
	if dataSize < 0 || dataSize > int64(math.MaxUint32)-36 || dataSize > maxInt-44 {
		return nil, errors.New("media: assembled WAV is too large")
	}
	out := make([]byte, 44+int(dataSize))
	writeWAVHeader(out[:44], sampleRate, channels, blockAlign, uint32(dataSize))
	for _, slide := range timeline.Slides {
		for _, segment := range slide.Segments {
			wav := parsed[segment.AudioKey]
			startFrame := samplePosition(segment.StartUS, sampleRate)
			expectedFrames := samplePosition(segment.EndUS-segment.StartUS, sampleRate)
			copyFrames := minInt64(expectedFrames, int64(len(wav.data))/int64(blockAlign))
			start := 44 + startFrame*int64(blockAlign)
			copy(out[int(start):], wav.data[:int(copyFrames*int64(blockAlign))])
		}
	}
	return out, nil
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
