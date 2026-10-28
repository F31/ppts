package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

// localTimeline 与 twoPageTimeline 同构，但音频 key 是**相对素材根目录**的本地路径。
func localTimeline() *media.Timeline {
	return &media.Timeline{
		SchemaVersion: media.TimelineSchemaVersion,
		DurationUS:    250_000,
		Slides: []media.SlideCue{
			{
				SlideID: "slide-1", StartUS: 0, EndUS: 100_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-1", AudioKey: "audio/1.wav", StartUS: 0, EndUS: 100_000}},
			},
			{
				SlideID: "slide-2", StartUS: 100_000, EndUS: 200_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-2", AudioKey: "audio/2.wav", StartUS: 100_000, EndUS: 200_000}},
			},
		},
		Subtitles: []media.SubtitleCue{
			{
				SlideID: "slide-1", SegmentID: "seg-1", StartUS: 0, EndUS: 100_000,
				Text:  "第一页的讲解内容比较长，需要按标点切分成多行，才能验证逐行轮换。",
				Chars: []media.CharCue{{StartUS: 0, EndUS: 20_000, Char: "第"}},
			},
			{SlideID: "slide-2", SegmentID: "seg-2", StartUS: 100_000, EndUS: 200_000, Text: "第二页。"},
		},
	}
}

func localInput() BundleInput {
	return BundleInput{
		Timeline:     localTimeline(),
		PagePNGKeys:  []string{"pages/1.png", "pages/2.png"},
		SRTKey:       "subs/cues.srt",
		VTTKey:       "subs/cues.vtt",
		Width:        1920,
		Height:       1080,
		FPS:          30,
		Title:        "本地生成示例",
		SnapshotHash: "sha256:local",
	}
}

func writeLocalFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// writeLocalFixture 造一份"隐私模式"的真实素材目录：页图、音频、字幕、时间轴全在本地。
func writeLocalFixture(t *testing.T) (string, BundleInput) {
	t.Helper()
	root := t.TempDir()
	in := localInput()
	writeLocalFile(t, root, "pages/1.png", pngBytes(t, 1920, 1080))
	writeLocalFile(t, root, "pages/2.png", pngBytes(t, 1280, 720))
	writeLocalFile(t, root, "audio/1.wav", pcm16WAV(2400))
	writeLocalFile(t, root, "audio/2.wav", pcm16WAV(2400))
	writeLocalFile(t, root, "subs/cues.vtt", []byte("WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n第一页\n"))
	writeLocalFile(t, root, "subs/cues.srt", []byte("1\r\n00:00:00,000 --> 00:00:10,000\r\n第一页\r\n"))
	timelineJSON, err := json.Marshal(in.Timeline)
	if err != nil {
		t.Fatalf("marshal timeline: %v", err)
	}
	writeLocalFile(t, root, "timeline.json", timelineJSON)
	return root, in
}

// listDirEntries 递归列出目录下的文件（slash 分隔的相对路径）。
func listDirEntries(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(names)
	return names
}

// TestLocalBundleMatchesZipStructure 是 tasks 5.3 的核心断言：
// 同一份输入分别写成目录与 zip，条目集合必须完全相同，清单必须逐字段相等。
// 两者共用 buildInto + Manifest.Validate，这条断言是"隐私模式产物不漂移"的守卫。
func TestLocalBundleMatchesZipStructure(t *testing.T) {
	root, in := writeLocalFixture(t)
	ctx := context.Background()

	outDir := filepath.Join(t.TempDir(), "bundle")
	dirManifest, err := BuildLocalToDir(ctx, LocalBundle{Root: root, Input: in}, outDir)
	if err != nil {
		t.Fatalf("BuildLocalToDir: %v", err)
	}

	var zipBuf bytes.Buffer
	if _, err := BuildLocalToZip(ctx, LocalBundle{Root: root, Input: in}, &zipBuf); err != nil {
		t.Fatalf("BuildLocalToZip: %v", err)
	}
	zipManifest, zipFiles, err := VerifyBundle(zipBuf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	dirNames := listDirEntries(t, outDir)
	zipNames := make([]string, 0, len(zipFiles))
	for name := range zipFiles {
		zipNames = append(zipNames, name)
	}
	sort.Strings(zipNames)
	if !reflect.DeepEqual(dirNames, zipNames) {
		t.Fatalf("local bundle entries differ from zip\n dir: %v\n zip: %v", dirNames, zipNames)
	}

	if !reflect.DeepEqual(dirManifest, zipManifest) {
		t.Fatalf("local manifest differs from zip\n dir: %+v\n zip: %+v", dirManifest, zipManifest)
	}

	// 目录形态的实际价值：双击就能播的入口必须在。
	for _, want := range []string{FileIndexHTML, FileManifestJSON, FileSubtitlesJS, FileAudioMix} {
		if _, ok := zipFiles[want]; !ok {
			t.Fatalf("bundle is missing %s", want)
		}
		if _, statErr := os.Stat(filepath.Join(outDir, filepath.FromSlash(want))); statErr != nil {
			t.Fatalf("local bundle is missing %s: %v", want, statErr)
		}
	}
	if CountExternalRefs(zipFiles) != 0 {
		t.Fatalf("bundle must not reference external resources")
	}
}

func TestBuildDirRejectsNonEmptyOutput(t *testing.T) {
	root, in := writeLocalFixture(t)
	outDir := t.TempDir() // t.TempDir 本身非空的风险为零，这里显式放一个文件
	writeLocalFile(t, outDir, "stale.txt", []byte("old"))

	_, err := BuildLocalToDir(context.Background(), LocalBundle{Root: root, Input: in}, outDir)
	if !errors.Is(err, ErrOutDirNotEmpty) {
		t.Fatalf("err = %v, want ErrOutDirNotEmpty", err)
	}
	// 拒绝覆盖时必须保持目录原样（不能顺手清掉用户文件）。
	if _, statErr := os.Stat(filepath.Join(outDir, "stale.txt")); statErr != nil {
		t.Fatalf("stale file was removed: %v", statErr)
	}
}

func TestBuildDirCleansUpAfterFailure(t *testing.T) {
	root, in := writeLocalFixture(t)
	// 删掉一段音频：打包必须失败（没有声音的成品没有意义）。
	if err := os.Remove(filepath.Join(root, "audio", "2.wav")); err != nil {
		t.Fatalf("remove audio: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "bundle")
	if _, err := BuildLocalToDir(context.Background(), LocalBundle{Root: root, Input: in}, outDir); err == nil {
		t.Fatal("expected failure when an audio segment is missing")
	}
	if _, statErr := os.Stat(outDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed build left a partial directory behind: %v", statErr)
	}
}

func TestDirReaderRejectsUnsafeKeys(t *testing.T) {
	root, _ := writeLocalFixture(t)
	reader, err := NewDirReader(root)
	if err != nil {
		t.Fatalf("NewDirReader: %v", err)
	}
	cases := []string{
		"../outside.png",
		"pages/../../outside.png",
		"/etc/passwd",
		"C:/Windows/win.ini",
		"http://example.com/a.png",
		"https://example.com/a.png",
		"file:///c:/secret.png",
		"",
	}
	for _, key := range cases {
		if _, openErr := reader.OpenObject(context.Background(), key); openErr == nil {
			t.Fatalf("key %q must be rejected", key)
		}
	}
	if _, err := reader.OpenObject(context.Background(), "pages"); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("opening a directory must fail, got %v", err)
	}
}

func TestBuildDirAcceptsBackslashKeys(t *testing.T) {
	root, in := writeLocalFixture(t)
	in.PagePNGKeys = []string{`pages\1.png`, `pages\2.png`}
	outDir := filepath.Join(t.TempDir(), "bundle")
	manifest, err := BuildLocalToDir(context.Background(), LocalBundle{Root: root, Input: in}, outDir)
	if err != nil {
		t.Fatalf("BuildLocalToDir with backslash keys: %v", err)
	}
	if len(manifest.Slides) != 2 {
		t.Fatalf("slides = %d, want 2", len(manifest.Slides))
	}
}

func TestReadLocalTimeline(t *testing.T) {
	root, in := writeLocalFixture(t)
	timeline, err := ReadLocalTimeline(root, "timeline.json")
	if err != nil {
		t.Fatalf("ReadLocalTimeline: %v", err)
	}
	if !reflect.DeepEqual(timeline, in.Timeline) {
		t.Fatalf("timeline round-trip mismatch")
	}
	if _, err := ReadLocalTimeline(root, "missing.json"); err == nil {
		t.Fatal("expected error for missing timeline")
	}
}
