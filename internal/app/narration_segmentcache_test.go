package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// newSegmentCacheHandler 造一个只依赖本地对象存储的 handler：这两个函数只用 h.objects。
func newSegmentCacheHandler(t *testing.T) (*NarrationHandler, objectstore.ObjectStore) {
	t.Helper()
	objects := objectstore.NewLocal(t.TempDir(), nil)
	return &NarrationHandler{objects: objects}, objects
}

func putBytes(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey, data []byte) {
	t.Helper()
	if err := objects.Put(context.Background(), key, bytes.NewReader(data),
		objectstore.ObjectMeta{ContentType: "application/json", ContentHash: hashBytes(data), Size: int64(len(data))}); err != nil {
		t.Fatalf("put %s: %v", key.String(), err)
	}
}

// TestLoadSegmentManifestStrictVsLenient 锁住两条缓存链路**有意不同**的失败语义。
//
// 这两条链路原来各写一份几乎相同的函数，唯一差别是「清单存在但不可用」时：
// 项目级（strict）要把已解析的清单带回调用方去报错，租户级共享缓存（lenient）
// 当作未命中重算。合并实现后，这个差异只剩一个枚举参数，因此必须由用例守住——
// 否则下次有人"顺手统一"就会把其中一边的行为改掉，而两条链路都没有别的证据。
func TestLoadSegmentManifestStrictVsLenient(t *testing.T) {
	const tenant = "tenant-1"
	manifestKey := objectstore.ObjectKey{
		TenantID: tenant, ProjectID: "project-1", Revision: "narration",
		AssetType: "segment", AssetID: "seg-1", Ext: "json",
	}
	audioKey := objectstore.ObjectKey{
		TenantID: tenant, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: "audio", AssetID: "a1", Ext: "wav",
	}

	validManifest := func(audio string, durationMS int64) []byte {
		b, err := json.Marshal(SegmentAsset{SegmentID: "seg-1", AudioKey: audio, DurationMS: durationMS})
		if err != nil {
			t.Fatalf("marshal manifest: %v", err)
		}
		return b
	}

	cases := []struct {
		name string
		// manifest 为 nil 表示不写清单对象（未命中）。
		manifest []byte
		// wantStrictNil / wantLenientNil 描述 ok=false 时两个模式各自是否带回清单。
		wantStrictNil  bool
		wantLenientNil bool
		wantOK         bool
		// omitAudio 表示不写音频对象，用于「清单有效但音频缺失」。
		omitAudio bool
	}{
		{name: "清单与音频都在", manifest: validManifest(audioKey.String(), 100), wantOK: true},
		{name: "清单 JSON 损坏", manifest: []byte("{not json"), wantStrictNil: false, wantLenientNil: true},
		{name: "清单缺 audioKey", manifest: validManifest("", 100), wantStrictNil: false, wantLenientNil: true},
		{name: "清单时长非正", manifest: validManifest(audioKey.String(), 0), wantStrictNil: false, wantLenientNil: true},
		{
			name: "清单指向别的租户",
			manifest: validManifest(objectstore.ObjectKey{
				TenantID: "tenant-2", ProjectID: sharedCacheProject, Revision: "cache",
				AssetType: "audio", AssetID: "a1", Ext: "wav",
			}.String(), 100),
			wantStrictNil: false, wantLenientNil: true,
		},
		// 音频对象缺失属于"缓存没做完"，两种模式都按未命中处理，不该报错。
		{name: "音频对象缺失", manifest: validManifest(audioKey.String(), 100), wantStrictNil: true, wantLenientNil: true, omitAudio: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, objects := newSegmentCacheHandler(t)
			if !tc.omitAudio {
				putBytes(t, objects, audioKey, []byte("RIFF"))
			}
			if tc.manifest != nil {
				putBytes(t, objects, manifestKey, tc.manifest)
			}

			strict, ok, err := handler.loadSegmentManifest(context.Background(), manifestKey, segmentManifestStrict)
			if err != nil {
				t.Fatalf("strict err = %v", err)
			}
			lenient, lok, lerr := handler.loadSegmentManifest(context.Background(), manifestKey, segmentManifestLenient)
			if lerr != nil {
				t.Fatalf("lenient err = %v", lerr)
			}

			if ok != tc.wantOK || lok != tc.wantOK {
				t.Fatalf("ok strict=%v lenient=%v, want %v", ok, lok, tc.wantOK)
			}
			if (strict == nil) != tc.wantStrictNil {
				t.Errorf("strict manifest nil = %v, want %v", strict == nil, tc.wantStrictNil)
			}
			if (lenient == nil) != tc.wantLenientNil {
				t.Errorf("lenient manifest nil = %v, want %v", lenient == nil, tc.wantLenientNil)
			}
		})
	}
}
