package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// benchPublishSize 取一个「大成片」量级的样本：500 MB 是方案里点名的数字，
// 但 CI 单轮跑多次拷贝太慢；128 MB 已足够让拷贝成本显形，又不拖垮整包测试时长。
const benchPublishSize = 128 << 20

func writePublishSample(tb testing.TB, path string) {
	tb.Helper()
	f, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = byte(i)
	}
	written := 0
	for written < benchPublishSize {
		n := len(buf)
		if remaining := benchPublishSize - written; remaining < n {
			n = remaining
		}
		if _, err := f.Write(buf[:n]); err != nil {
			tb.Fatal(err)
		}
		written += n
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
}

// hashOnce 模拟导出链路里 fileExportOutput 的哈希读：这是两条路径共同的前置步骤，
// 对象键里的 AssetID 依赖内容哈希，发布前必须先算出来——所以这遍读谁也省不掉。
func hashOnce(tb testing.TB, path string) string {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// copySample 把母本拷到 dst（拷贝动作发生在 StopTimer 区间，不计入计时）。
// 「file」路径每轮迭代会把样本 rename 走，所以每轮都要从母本另拷一份。
func copySample(tb testing.TB, src, dst string) {
	tb.Helper()
	in, err := os.Open(src)
	if err != nil {
		tb.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		tb.Fatal(err)
	}
	if err := out.Close(); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkLocalPublish 对比「成品发布到本地对象根」的两条路径：
//
//	reader：改前的路径——hashReader 读一遍 + LocalFS.Put 把 reader 整份 io.Copy 进
//	        对象根（再读一遍、再写一遍）。500 MB 成片 = 读 1 GB、写 500 MB。
//	file：  改后的路径——hashReader 读一遍 + PutFile 同盘 rename（只动目录项）。
//
// b.SetBytes 统一按成品字节数计，ns/op 直接可比。
func BenchmarkLocalPublish(b *testing.B) {
	masterDir := b.TempDir()
	rootDir := b.TempDir()
	scratchDir := b.TempDir()
	master := filepath.Join(masterDir, "sample.bin")
	writePublishSample(b, master)
	store := NewLocal(rootDir, nil)
	key := ObjectKey{TenantID: "t", ProjectID: "p", Revision: "artifact", AssetType: "artifact", AssetID: "bench", Ext: "bin"}
	meta := ObjectMeta{ContentType: "application/octet-stream", Size: benchPublishSize}

	b.Run("reader", func(b *testing.B) {
		b.SetBytes(benchPublishSize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sample := filepath.Join(scratchDir, "reader.bin")
			b.StopTimer()
			copySample(b, master, sample)
			b.StartTimer()
			hashOnce(b, sample)
			f, err := os.Open(sample)
			if err != nil {
				b.Fatal(err)
			}
			if err := store.Put(context.Background(), key, f, meta); err != nil {
				b.Fatal(err)
			}
			f.Close()
			os.Remove(sample)
		}
	})

	b.Run("file", func(b *testing.B) {
		b.SetBytes(benchPublishSize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			sample := filepath.Join(scratchDir, "file.bin")
			b.StopTimer()
			copySample(b, master, sample)
			b.StartTimer()
			hashOnce(b, sample)
			if err := store.PutFile(context.Background(), key, sample, meta); err != nil {
				b.Fatal(err)
			}
		}
	})
}
