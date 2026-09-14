package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

type artifactStoreStub struct {
	created *artifact.Artifact
}

func (s *artifactStoreStub) Create(_ context.Context, tenantID string, in artifact.NewArtifact) (*artifact.Artifact, error) {
	s.created = &artifact.Artifact{
		ID: "artifact-1", TenantID: tenantID, ProjectID: in.ProjectID, SnapshotHash: in.SnapshotHash,
		Format: in.Format, ObjectKey: in.ObjectKey, ContentHash: in.ContentHash,
		SizeBytes: in.SizeBytes, CreatedAt: time.Unix(100, 0),
	}
	return s.created, nil
}

func (s *artifactStoreStub) Get(context.Context, string, string) (*artifact.Artifact, error) {
	return s.created, nil
}

func exportJob(t *testing.T, snapshot ExportSnapshot) *pipeline.Job {
	t.Helper()
	b, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return &pipeline.Job{ID: "job-export", TenantID: "tenant-1", ProjectID: "project-1", Kind: pipeline.KindExport, InputSnapshot: string(b)}
}

func seedTimelineBundle(t *testing.T, objects objectstore.ObjectStore) (TimelineAsset, objectstore.ObjectKey) {
	t.Helper()
	timeline, err := media.BuildTimeline([]media.SlideInput{{
		SlideID:  "slide-1",
		Segments: []media.SegmentInput{{SegmentID: "seg-1", DisplayText: "字幕", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 100}},
	}}, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	srtKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "subtitle", AssetID: "sub", Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "subtitle", AssetID: "sub", Ext: "vtt"}
	bundleKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	putObject(t, objects, srtKey, []byte("1\r\n00:00:00,000 --> 00:00:00,100\r\n字幕\r\n"), "application/x-subrip")
	putObject(t, objects, vttKey, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:00.100\n字幕\n"), "text/vtt")
	bundle := TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()}
	b, _ := json.Marshal(bundle)
	putObject(t, objects, bundleKey, b, "application/json")
	return bundle, bundleKey
}

func TestExportHandlerPublishesSRTArtifact(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	_, bundleKey := seedTimelineBundle(t, objects)
	artifacts := &artifactStoreStub{}
	steps := &stepRecorder{}
	handler := NewExportHandler(artifacts, steps, objects, nil)

	err := handler.Handle(context.Background(), exportJob(t, ExportSnapshot{Format: artifact.FormatSRT, TimelineKey: bundleKey.String()}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if artifacts.created == nil || artifacts.created.Format != artifact.FormatSRT || artifacts.created.SizeBytes == 0 {
		t.Fatalf("artifact = %+v", artifacts.created)
	}
	// 最终成功步骤改为随任务终态原子提交（outbox，G3-5），由 PG 测试覆盖原子性。
	key, _ := objectstore.Parse(artifacts.created.ObjectKey)
	r, _, err := objects.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("artifact object: %v", err)
	}
	r.Close()
}

func TestExportHandlerPublishesMP4Artifact(t *testing.T) {
	encoder, err := media.NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	objects := objectstore.NewLocal(t.TempDir(), nil)
	bundle, bundleKey := seedTimelineBundle(t, objects)
	audioKey, _ := objectstore.Parse(bundle.Timeline.Slides[0].Segments[0].AudioKey)
	putObject(t, objects, audioKey, pcmWAVForExportTest(1000, 100), "audio/wav")
	pageKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "render", AssetType: "render", AssetID: "slide-1", Ext: "png"}
	putObject(t, objects, pageKey, pngForExportTest(), "image/png")
	artifacts := &artifactStoreStub{}
	handler := NewExportHandler(artifacts, &stepRecorder{}, objects, encoder)

	err = handler.Handle(context.Background(), exportJob(t, ExportSnapshot{
		Format: artifact.FormatMP4, TimelineKey: bundleKey.String(), PagePNGKeys: []string{pageKey.String()}, Width: 320, Height: 180, FPS: 20,
	}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if artifacts.created == nil || artifacts.created.Format != artifact.FormatMP4 || artifacts.created.SizeBytes <= 0 {
		t.Fatalf("artifact = %+v", artifacts.created)
	}
}

type failArtifactPutStore struct {
	objectstore.ObjectStore
	err error
}

func (s failArtifactPutStore) Put(ctx context.Context, key objectstore.ObjectKey, r io.Reader, meta objectstore.ObjectMeta) error {
	if key.AssetType == "artifact" {
		return s.err
	}
	return s.ObjectStore.Put(ctx, key, r, meta)
}

func TestExportHandlerObjectWriteFailureDoesNotCreateArtifact(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	_, bundleKey := seedTimelineBundle(t, objects)
	artifacts := &artifactStoreStub{}
	steps := &stepRecorder{}
	wantErr := errors.New("disk full")
	handler := NewExportHandler(artifacts, steps, failArtifactPutStore{ObjectStore: objects, err: wantErr}, nil)

	err := handler.Handle(context.Background(), exportJob(t, ExportSnapshot{Format: artifact.FormatSRT, TimelineKey: bundleKey.String()}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Handle err = %v want %v", err, wantErr)
	}
	if artifacts.created != nil {
		t.Fatalf("artifact created after object write failure: %+v", artifacts.created)
	}
	for _, step := range steps.latest {
		if step.StepType == "export" && step.State == pipeline.StepFailed {
			return
		}
	}
	t.Fatalf("failed export step not recorded: %+v", steps.latest)
}

func putObject(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey, data []byte, contentType string) {
	t.Helper()
	if err := objects.Put(context.Background(), key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: contentType, ContentHash: hashBytes(data), Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}

func pngForExportTest() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func pcmWAVForExportTest(sampleRate uint32, frames int) []byte {
	dataSize := uint32(frames * 2)
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	putLE32(out[4:], 36+dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putLE32(out[16:], 16)
	putLE16(out[20:], 1)
	putLE16(out[22:], 1)
	putLE32(out[24:], sampleRate)
	putLE32(out[28:], sampleRate*2)
	putLE16(out[32:], 2)
	putLE16(out[34:], 16)
	copy(out[36:40], "data")
	putLE32(out[40:], dataSize)
	return out
}

func putLE16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
