package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
)

// vadWAV 生成 VAD 可用的 PCM16 mono WAV：首静音 + 语音/停顿交错的音节核（对齐估算升级路径）。
// 与 tts 包 burstWAV 模式一致，供 app 层对齐缓存测试使用。
func vadWAV(t *testing.T) []byte {
	t.Helper()
	const rate = 16000
	segments := []struct {
		ms     int
		voiced bool
	}{
		{300, false}, {600, true}, {200, false}, {500, true},
		{150, false}, {700, true}, {300, false},
	}
	totalMS := 0
	for _, s := range segments {
		totalMS += s.ms
	}
	n := rate * totalMS / 1000
	samples := make([]int16, n)
	pos := 0
	for _, s := range segments {
		cnt := rate * s.ms / 1000
		for k := 0; k < cnt; k++ {
			if s.voiced {
				samples[pos+k] = 12000
			}
		}
		pos += cnt
	}
	dataSize := uint32(len(samples) * 2)
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], 36+dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1)
	binary.LittleEndian.PutUint16(out[22:], 1)
	binary.LittleEndian.PutUint32(out[24:], rate)
	binary.LittleEndian.PutUint32(out[28:], rate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:], dataSize)
	for i, s := range samples {
		out[44+i*2] = byte(s)
		out[44+i*2+1] = byte(s >> 8)
	}
	return out
}

func durationMSOfTestWAV(t *testing.T, wav []byte) int64 {
	t.Helper()
	if len(wav) < 44 {
		t.Fatal("short wav")
	}
	const rate = 16000
	dataBytes := int(binary.LittleEndian.Uint32(wav[40:]))
	return int64(dataBytes) / 2 / rate * 1000
}

func TestRealignSharedSegmentUsesAlignmentCache(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	handler := NewNarrationHandler(nil, nil, objects, providerForTests())

	wav := vadWAV(t)
	durationMS := durationMSOfTestWAV(t, wav)
	// 估算类对齐（estimated）触发 VAD 重算升级。
	shared := &SegmentAsset{
		SegmentID: "seg-1", AudioHash: "deadbeef", Format: "audio/wav",
		DurationMS: durationMS,
		Alignment:  &tts.Alignment{Text: "今天天气很好，我们出去走走吧。", Method: tts.AlignEstimate},
	}
	audioKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "audio", AssetID: "cfg-hash", Ext: "wav",
	}
	shared.AudioKey = audioKey.String()
	if err := objects.Put(ctx, audioKey, bytes.NewReader(wav), objectstore.ObjectMeta{
		ContentType: "audio/wav", ContentHash: shared.AudioHash, Size: int64(len(wav)),
	}); err != nil {
		t.Fatalf("put audio: %v", err)
	}

	// 第一次：重算并写回对齐缓存。
	upgraded, ok := handler.realignSharedSegment(ctx, shared, shared.Alignment.Text)
	if !ok {
		t.Fatalf("first realign: expected VAD upgrade, got false")
	}
	if upgraded.Alignment.Method != tts.AlignEstimateVAD {
		t.Fatalf("first realign method = %s, want estimated_vad", upgraded.Alignment.Method)
	}
	if len(upgraded.Alignment.Tokens) == 0 {
		t.Fatalf("first realign tokens empty")
	}

	// 对齐缓存对象应已存在。
	cacheKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "alignment_vad", AssetID: audioKey.AssetID + "-a1", Ext: "json",
	}
	if _, _, err := objects.Get(ctx, cacheKey); err != nil {
		t.Fatalf("alignment cache object missing after first realign: %v", err)
	}

	// 第二次：直接命中对齐缓存（音频对象仍在，但不再触发重算路径的重复副作用）。
	again, ok := handler.realignSharedSegment(ctx, shared, shared.Alignment.Text)
	if !ok {
		t.Fatalf("second realign: expected cache hit, got false")
	}
	if again.Alignment.Method != tts.AlignEstimateVAD || again.Alignment.Text != shared.Alignment.Text {
		t.Fatalf("second realign mismatch: %+v", again.Alignment)
	}
	if len(again.Alignment.Tokens) != len(upgraded.Alignment.Tokens) {
		t.Fatalf("second realign tokens differ from first: %d vs %d", len(again.Alignment.Tokens), len(upgraded.Alignment.Tokens))
	}
}

// TestRealignSharedSegmentCacheInvalidatedByAudioHash 音频哈希不匹配时缓存视为未命中并重算。
func TestRealignSharedSegmentCacheInvalidatedByAudioHash(t *testing.T) {
	ctx := context.Background()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	handler := NewNarrationHandler(nil, nil, objects, providerForTests())

	wav := vadWAV(t)
	durationMS := durationMSOfTestWAV(t, wav)
	audioKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "audio", AssetID: "cfg-hash-2", Ext: "wav",
	}
	shared := &SegmentAsset{
		SegmentID: "seg-1", AudioHash: "hash-a", Format: "audio/wav", DurationMS: durationMS,
		AudioKey:  audioKey.String(),
		Alignment: &tts.Alignment{Text: "今天天气很好，我们出去走走吧。", Method: tts.AlignEstimate},
	}
	if err := objects.Put(ctx, audioKey, bytes.NewReader(wav), objectstore.ObjectMeta{
		ContentType: "audio/wav", ContentHash: shared.AudioHash, Size: int64(len(wav)),
	}); err != nil {
		t.Fatalf("put audio: %v", err)
	}
	// 第一次重算写入缓存。
	if _, ok := handler.realignSharedSegment(ctx, shared, shared.Alignment.Text); !ok {
		t.Fatal("expected upgrade")
	}
	// 篡改缓存载荷里的音频哈希：应被识别为不匹配并重算（仍成功升级，但不从缓存读取）。
	cacheKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "alignment_vad", AssetID: audioKey.AssetID + "-a1", Ext: "json",
	}
	r, _, err := objects.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("get cache: %v", err)
	}
	var cached cachedAlignment
	if err := json.NewDecoder(r).Decode(&cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	r.Close()
	cached.AudioHash = "hash-b"
	payload, _ := json.Marshal(cached)
	if err := objects.Put(ctx, cacheKey, bytes.NewReader(payload), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(payload), Size: int64(len(payload)),
	}); err != nil {
		t.Fatalf("overwrite cache: %v", err)
	}
	if _, ok := handler.realignSharedSegment(ctx, shared, shared.Alignment.Text); !ok {
		t.Fatal("expected re-upgrade after hash mismatch")
	}
}
