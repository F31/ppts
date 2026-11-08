package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

// publishRecorder 包住真实 LocalFS，记录成品发布走了哪条通道。
// 嵌入的是 ObjectStore 接口（不是 *LocalFS），所以 PutFile 不会被自动提升——
// 必须在这里显式转发，正好模拟「记录层在发布链路中间」的生产形态。
type publishRecorder struct {
	objectstore.ObjectStore
	putFileCalls   int
	putReaderCalls int
	lastFilePath   string
	lastFileMeta   objectstore.ObjectMeta
	lastFileKey    objectstore.ObjectKey
}

func (p *publishRecorder) PutFile(ctx context.Context, key objectstore.ObjectKey, path string, meta objectstore.ObjectMeta) error {
	p.putFileCalls++
	p.lastFilePath = path
	p.lastFileMeta = meta
	p.lastFileKey = key
	return p.ObjectStore.(objectstore.FilePublisher).PutFile(ctx, key, path, meta)
}

func (p *publishRecorder) Put(ctx context.Context, key objectstore.ObjectKey, r io.Reader, meta objectstore.ObjectMeta) error {
	if key.AssetType == "artifact" {
		p.putReaderCalls++
	}
	return p.ObjectStore.Put(ctx, key, r, meta)
}

// seedBundleAudio 给时间轴里声明的音频对象放一段合法 PCM（web_project 打包要读它）。
func seedBundleAudio(t *testing.T, bundle TimelineAsset, objects objectstore.ObjectStore) {
	t.Helper()
	audioKey, err := objectstore.Parse(bundle.Timeline.Slides[0].Segments[0].AudioKey)
	if err != nil {
		t.Fatal(err)
	}
	putObject(t, objects, audioKey, pcmWAVForExportTest(1000, 100), "audio/wav")
}

// TestExportPublishesFileArtifactByRename 是「文件成品发布只付哈希那一遍读」的判据：
//
// 改前：fileExportOutput 哈希读一遍 + LocalFS.Put 把 reader 整份 io.Copy（再读一遍、
// 再写一遍）。改后：哈希读一遍 + PutFile 同盘 rename。本用例不看时间、不看分配，
// 只断言结构事实——成品发布必须走文件通道（putReaderCalls == 0），且发布完的
// 对象内容与成品行的哈希/大小对得上。谁把 storeArtifact 改回 reader 路径，
// putReaderCalls 立刻变 1，这里先红。
func TestExportPublishesFileArtifactByRename(t *testing.T) {
	objects := &publishRecorder{ObjectStore: objectstore.NewLocal(t.TempDir(), nil)}
	bundle, bundleKey := seedTimelineBundle(t, objects)
	seedBundleAudio(t, bundle, objects)
	artifacts := &artifactStoreStub{}
	handler := NewExportHandler(artifacts, &stepRecorder{}, objects, nil)

	if err := handler.Handle(context.Background(), exportJob(t, ExportSnapshot{
		Format: artifact.FormatWebProject, TimelineKey: bundleKey.String(),
	})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if artifacts.created == nil {
		t.Fatal("no artifact created")
	}
	if objects.putFileCalls != 1 {
		t.Fatalf("file artifact must publish via PutFile exactly once, got %d (reader path used: %d)", objects.putFileCalls, objects.putReaderCalls)
	}
	if objects.putReaderCalls != 0 {
		t.Fatalf("file artifact must not go through the reader path, got %d reader Puts", objects.putReaderCalls)
	}
	// 发布通道拿到的元数据必须与成品行一致：哈希错了，下载校验与内容去重都会失灵。
	if objects.lastFileMeta.ContentHash != artifacts.created.ContentHash || objects.lastFileMeta.Size != artifacts.created.SizeBytes {
		t.Fatalf("publish meta = %+v, artifact row = hash %s size %d", objects.lastFileMeta, artifacts.created.ContentHash, artifacts.created.SizeBytes)
	}
	if objects.lastFileKey.String() != artifacts.created.ObjectKey {
		t.Fatalf("publish key = %s, artifact row = %s", objects.lastFileKey, artifacts.created.ObjectKey)
	}
	// 发布的对象内容与哈希对得上（防「rename 了错的东西」）。
	data := readAll(t, objects, objects.lastFileKey)
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != artifacts.created.ContentHash {
		t.Fatalf("published bytes hash mismatch: %s", hex.EncodeToString(sum[:]))
	}
	// 发布后原路径必须已不存在：rename 把它移走，或拷贝回退后由 exportOutput.Close
	// 清理——无论哪条路，泄漏整份成品到系统临时目录都是缺陷。
	if _, err := os.Stat(objects.lastFilePath); !os.IsNotExist(err) {
		t.Fatalf("artifact temp file must be renamed away, stat err = %v", err)
	}
}

// TestExportPublishesBytesArtifactViaReader 守另一半：内存产物（srt/vtt/pptx）没有
// 文件，必须仍走 reader 路径——publish 里「out.file != nil」的判断如果反了或丢了，
// 这里会红。
func TestExportPublishesBytesArtifactViaReader(t *testing.T) {
	objects := &publishRecorder{ObjectStore: objectstore.NewLocal(t.TempDir(), nil)}
	_, bundleKey := seedTimelineBundle(t, objects)
	artifacts := &artifactStoreStub{}
	handler := NewExportHandler(artifacts, &stepRecorder{}, objects, nil)

	if err := handler.Handle(context.Background(), exportJob(t, ExportSnapshot{
		Format: artifact.FormatSRT, TimelineKey: bundleKey.String(),
	})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if objects.putFileCalls != 0 {
		t.Fatalf("bytes artifact must not use PutFile, got %d calls", objects.putFileCalls)
	}
	if objects.putReaderCalls != 1 {
		t.Fatalf("bytes artifact must publish via reader exactly once, got %d", objects.putReaderCalls)
	}
}
