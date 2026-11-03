package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

// 竖屏决策（ADR-029 ③）的三条判据：
//   1. 比例归一：16:9 与今天逐像素一致（否则存量成片全部跟着变）；
//   2. 显式尺寸与源页不符时必须拒绝，而不是照单全收后产出拉伸画面；
//   3. 受理时的快照里写的就是推导后的尺寸——它进 snapshotHash，写晚了等于没改。

func TestFitCanvasKeepsSixteenNinePixelIdentical(t *testing.T) {
	cases := []struct {
		aspect float64
		w, h   int
	}{
		{16.0 / 9.0, 1920, 1080}, // 与既有默认值逐像素一致，不能变
		{4.0 / 3.0, 1440, 1080},
		{9.0 / 16.0, 1080, 1920},
		{3.0 / 4.0, 1080, 1440},
		{1.0, 1080, 1080},
	}
	for _, tc := range cases {
		w, h := fitCanvas(tc.aspect)
		if w != tc.w || h != tc.h {
			t.Fatalf("fitCanvas(%.3f) = %dx%d, want %dx%d", tc.aspect, w, h, tc.w, tc.h)
		}
	}
	// 奇数尺寸会被 H.264 直接拒绝，所以归一必须落在偶数上。
	if w, h := fitCanvas(1.2345); w%2 != 0 || h%2 != 0 {
		t.Fatalf("fitCanvas 产出了奇数尺寸 %dx%d", w, h)
	}
	// 超宽页不能推出离谱的编码请求。
	if w, h := fitCanvas(8.0); w > canvasMaxEdge || h > canvasMaxEdge {
		t.Fatalf("超宽页没有被夹到长边上限：%dx%d", w, h)
	}
}

func TestResolveCanvasValidation(t *testing.T) {
	cases := []struct {
		name           string
		w, h           int
		aspect         float64
		wantW, wantH   int
		wantErrContain string
	}{
		{name: "缺省按 16:9", w: 0, h: 0, aspect: 0, wantW: 1920, wantH: 1080},
		{name: "缺省按页图比例", w: 0, h: 0, aspect: 9.0 / 16.0, wantW: 1080, wantH: 1920},
		{name: "显式同比例放行", w: 1280, h: 720, aspect: 16.0 / 9.0, wantW: 1280, wantH: 720},
		{
			name: "显式比例不符要拒绝", w: 1920, h: 1080, aspect: 9.0 / 16.0,
			wantErrContain: "与源页比例",
		},
		{name: "只给宽度按比例补高度", w: 1080, h: 0, aspect: 16.0 / 9.0, wantW: 1080, wantH: 608},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h, err := resolveCanvas(tc.w, tc.h, tc.aspect)
			if tc.wantErrContain != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrContain) {
					t.Fatalf("err = %v, want 含 %q", err, tc.wantErrContain)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("resolveCanvas = %dx%d, want %dx%d", w, h, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestSourceAspectReadsPageHeader(t *testing.T) {
	objects := testObjects(t)
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 216, 384)) // 9:16
	for y := 0; y < 384; y++ {
		for x := 0; x < 216; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	key, err := objectstore.Parse("tenant-1/project-1/render/page/p1.png")
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if err := objects.Put(context.Background(), key, bytes.NewReader(buf.Bytes()),
		objectstore.ObjectMeta{ContentType: "image/png", Size: int64(buf.Len())}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got := sourceAspect(context.Background(), objects, []string{"tenant-1/project-1/render/page/p1.png"})
	if diff := got - 216.0/384.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("sourceAspect = %.6f, want %.6f", got, 216.0/384.0)
	}
	// 读不到就返回 0（调用方回退默认值），绝不 panic。
	if got := sourceAspect(context.Background(), objects, []string{"tenant-1/project-1/render/page/none.png"}); got != 0 {
		t.Fatalf("缺失页图应返回 0，得到 %.3f", got)
	}
}

// TestBundleExportDerivesPortraitSnapshot 走完整入口：竖屏页图 → 快照 1080×1920。
// 这条守的是"比例进得去、尺寸写在快照里"——写晚一步（在 worker 里补）就进不了
// snapshotHash，同一个幂等键会产出两种画布的成片。
func TestBundleExportDerivesPortraitSnapshot(t *testing.T) {
	objects := testObjects(t)
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 216, 384))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	key, err := objectstore.Parse("tenant-1/project-1/render/page/p1.png")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := objects.Put(context.Background(), key, bytes.NewReader(buf.Bytes()),
		objectstore.ObjectMeta{ContentType: "image/png", Size: int64(buf.Len())}); err != nil {
		t.Fatalf("put: %v", err)
	}

	jobs := &jobCreatorStub{}
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, jobs,
		&fakeArtifactStore{}, objects, nil, Options{DevHeaders: true}))
	t.Cleanup(server.Close)

	body := `{"format":"html_bundle","timelineKey":"tenant-1/project-1/narration/timeline/tl.json","pagePngKeys":["tenant-1/project-1/render/page/p1.png"]}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/projects/project-1/exports", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tenantHeader, "tenant-1")
	req.Header.Set(userHeader, "user-1")
	req.Header.Set("Idempotency-Key", "portrait-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var snapshot app.ExportSnapshot
	if err := json.Unmarshal([]byte(jobs.inputSnapshot), &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Width != 1080 || snapshot.Height != 1920 {
		t.Fatalf("竖屏页图应推导出 1080x1920，得到 %dx%d", snapshot.Width, snapshot.Height)
	}
}
