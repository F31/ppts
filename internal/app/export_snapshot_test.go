package app

import (
	"encoding/json"
	"testing"

	"github.com/F31/ppts/internal/artifact"
)

// 这组用例守的是一条不可见的契约：**开关关闭时，导出快照的序列化字节与引入动画之前
// 逐字节一致**。它很容易被忽略，因为单元测试全绿、功能也正常，坏掉的地方在数据里：
//
//   - snapshotHash 变 → 历史产物全部 miss（成品库里的旧视频再也对不上新请求）；
//   - 步骤键 `export:<format>:<hash>` 变 → 同一个任务被重跑，白花一次转码；
//   - internal/api 创建任务时用序列化字节比对 Idempotency-Key 载荷，
//     字节不同会被判成「同一个 Key 用在另一个请求上」，直接 409。
//
// 下面三条字面量是**加动画字段之前**从 ExportSnapshot 真实序列化出来的
// （探针用例打印，见 commit message），不是事后照着新代码写的期望值。
var frozenSnapshots = []struct {
	name  string
	bytes string
	hash  string
}{
	{
		name:  "mp4 全参数",
		bytes: `{"format":"mp4","timelineKey":"t/1/bundle.zip","pagePngKeys":["p/1.png","p/2.png"],"width":1920,"height":1080,"fps":30,"burnSubtitles":true,"includeNotes":true}`,
		hash:  "b75aabac540631b6fbb044a58304ae06198545f4e3d31fd4b71731c8db003ec0",
	},
	{
		name:  "srt 最小参数",
		bytes: `{"format":"srt","timelineKey":"t/1/bundle.zip"}`,
		hash:  "9f94fe9d9cc056c221b6942ca14f3a9ba25f8a250d3058877b1631a42ebd8e00",
	},
	{
		name:  "html_bundle 非默认分辨率",
		bytes: `{"format":"html_bundle","timelineKey":"t/1/b.zip","pagePngKeys":["p/1.png"],"width":1280,"height":720,"fps":30}`,
		hash:  "4283c7a6ca60fb5492efeff231e8fe84a5139221ab39b5e3c0693b130191536f",
	},
}

func TestSnapshotBytesFrozenWhenAnimationOff(t *testing.T) {
	// 直接断言字节，而不是只断言哈希：哈希相等只能说明"没变"，字节相等能说明**哪儿没变**，
	// 失败信息也能直接看出是哪个字段插进来或跑掉了。
	for _, tc := range frozenSnapshots {
		t.Run(tc.name, func(t *testing.T) {
			var snapshot ExportSnapshot
			if err := json.Unmarshal([]byte(tc.bytes), &snapshot); err != nil {
				t.Fatalf("反序列化历史快照失败：%v", err)
			}
			if snapshot.Animation != nil {
				t.Fatalf("历史快照不该带动画字段，得到 %+v", snapshot.Animation)
			}
			// 生产路径正是「反序列化 → 再序列化」：API 用原始结构算一次，
			// 导出步骤把 InputSnapshot 反序列化后重算一次。两次必须同字节。
			got, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatalf("序列化失败：%v", err)
			}
			if string(got) != tc.bytes {
				t.Fatalf("快照字节变了：\n got: %s\nwant: %s", got, tc.bytes)
			}
			hash, err := snapshotHashOf(snapshot)
			if err != nil {
				t.Fatalf("算哈希失败：%v", err)
			}
			if hash != tc.hash {
				t.Fatalf("snapshotHash 变了：got %s want %s\n"+
					"这是历史产物的键，改动它等于让成品库里所有旧视频失联。", hash, tc.hash)
			}
		})
	}
}

// 空动画选项必须与"没有动画选项"同键：两者都不渲染动画、产物同源。
// 若不放行，调用方只要多填一个空结构，同一份静态视频就会在成品库里出现两次。
func TestSnapshotHashNormalizesEmptyAnimation(t *testing.T) {
	base := ExportSnapshot{Format: artifact.FormatMP4, TimelineKey: "t/1/b.zip"}
	off, err := snapshotHashOf(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Animation = &AnimationOptions{}
	empty, err := snapshotHashOf(base)
	if err != nil {
		t.Fatal(err)
	}
	if empty != off {
		t.Fatalf("空动画选项应归一到无动画：got %s want %s", empty, off)
	}
	// 但序列化字节不必相同——规整只发生在哈希函数里，任务快照仍如实记录调用方给了什么。
	b, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"format":"mp4","timelineKey":"t/1/b.zip","animation":{}}` {
		t.Fatalf("空动画选项的线上形状变了：%s", b)
	}
}

func TestSnapshotHashTracksAnimationSpecs(t *testing.T) {
	off := ExportSnapshot{Format: artifact.FormatMP4, TimelineKey: "t/1/b.zip"}
	offHash, err := snapshotHashOf(off)
	if err != nil {
		t.Fatal(err)
	}

	specs := func(refs ...AnimationSpecRef) ExportSnapshot {
		return ExportSnapshot{
			Format: artifact.FormatMP4, TimelineKey: "t/1/b.zip",
			Animation: &AnimationOptions{Specs: refs},
		}
	}
	hashOf := func(s ExportSnapshot) string {
		t.Helper()
		h, err := snapshotHashOf(s)
		if err != nil {
			t.Fatalf("算哈希失败：%v", err)
		}
		return h
	}

	first := hashOf(specs(
		AnimationSpecRef{SlideID: "s1", Revision: 3},
		AnimationSpecRef{SlideID: "s2", Revision: 1},
	))
	if first == offHash {
		t.Fatal("启用动画后仍与静态版同键：用户改了规格再导出会拿到旧视频")
	}

	// 规格被改一版 → 必须换键。这是"导出排队期间改了规格"能被正确区分的前提。
	if h := hashOf(specs(
		AnimationSpecRef{SlideID: "s1", Revision: 4},
		AnimationSpecRef{SlideID: "s2", Revision: 1},
	)); h == first {
		t.Fatal("规格版本变了但快照键没变")
	}

	// 顺序有意义：Specs 与 PagePNGKeys 对齐，换序等于把某一页的规格套到另一页上。
	if h := hashOf(specs(
		AnimationSpecRef{SlideID: "s2", Revision: 1},
		AnimationSpecRef{SlideID: "s1", Revision: 3},
	)); h == first {
		t.Fatal("规格顺序变了但快照键没变：页码与规格的对应关系会被静默错配")
	}

	// 线上形状也锁一下：字段名是跨版本兼容面（旧版本服务读到未知字段不该崩，
	// 新版本必须能读懂旧任务里没有的 animation）。
	b, err := json.Marshal(specs(AnimationSpecRef{SlideID: "s1", Language: "zh-CN", SourceRevisionNo: 2, Revision: 3}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"format":"mp4","timelineKey":"t/1/b.zip","animation":{"specs":[{"slideId":"s1","language":"zh-CN","sourceRevisionNo":2,"revision":3}]}}`
	if string(b) != want {
		t.Fatalf("动画快照的线上形状变了：\n got: %s\nwant: %s", b, want)
	}
}
