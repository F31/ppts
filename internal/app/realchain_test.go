//go:build pg

package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/tenant"
)

// TestRealChainUploadParseDraftNarration 验证真实项目主链路：
// 上传 → 解析 → 原文讲稿 → 配音（fake TTS）→ 时间轴 bundle 可发现。
func TestRealChainUploadParseDraftNarration(t *testing.T) {
	env := setupApp(t)
	ctx := context.Background()

	// 1) 上传并消费 parse 任务。
	_, parseJob := uploadDeck(t, env, 1, deckBytes(t))
	runParseWorker(t, env, parseJob.ID)
	slideID := parsedSlideID(t, env, 1)

	// 2) 生成原文讲稿。
	scripts := narration.NewPGStore(env.pool)
	draftSnap, _ := json.Marshal(ScriptDraftSnapshot{
		ProjectID: appProject, RevisionNo: 1, Language: "zh-CN", Mode: "original",
	})
	draftJob, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindScriptDraft), "e2e-draft", string(draftSnap), time.Time{})
	if err != nil {
		t.Fatalf("draft job: %v", err)
	}
	draftHandler := NewScriptDraftHandler(scripts, env.objects)
	if err := draftHandler.Handle(ctx, draftJob); err != nil {
		t.Fatalf("draft: %v", err)
	}
	rev, err := scripts.Get(ctx, appTenant, appProject, slideID, "zh-CN")
	if err != nil {
		t.Fatalf("Get script: %v", err)
	}

	// 3) 配音：固定讲稿 revision 入队 narration 并消费。
	narrSnap := NarrationSnapshot{
		Slides:        []NarrationSlideSnapshot{{SlideID: slideID, ScriptRevision: rev.Revision}},
		Language:      "zh-CN",
		VoiceID:       "fake-voice-1",
		SpeechControl: tts.SpeechControl{RatePercent: 100},
		SampleRate:    16000,
	}
	narrBytes, _ := json.Marshal(narrSnap)
	narrJob, err := env.jobs.Create(ctx, appTenant, appProject, string(pipeline.KindNarration), "e2e-narr", string(narrBytes), time.Time{})
	if err != nil {
		t.Fatalf("narration job: %v", err)
	}
	narrationHandler := NewNarrationHandler(scripts, env.jobs, env.objects, tts.NewFakeProvider())
	worker := pipeline.NewWorker(env.jobs, "e2e-narr", appTenant, narrationHandler.Handle, pipeline.WorkerOptions{Poll: 20 * time.Millisecond})
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = worker.Run(runCtx)
	done, err := env.jobs.Get(ctx, narrJob.ID, appTenant)
	if err != nil {
		t.Fatalf("Get narration job: %v", err)
	}
	if done.State != pipeline.StateSucceeded {
		t.Fatalf("narration job = %+v (err=%v)", done, done.LastError)
	}

	// 4) 通过 job_steps.result_ref 发现时间轴 bundle，并读取 bundle 确认 SRT/VTT/音频键。
	ref, err := env.jobs.StepResultRef(tenant.WithContext(ctx, appTenant), narrJob.ID, "timeline")
	if err != nil {
		t.Fatalf("StepResultRef: %v", err)
	}
	if ref == "" {
		t.Fatalf("timeline step ref empty")
	}
	bundleKey, err := objectstore.Parse(ref)
	if err != nil {
		t.Fatalf("parse bundle key: %v", err)
	}
	rc, _, err := env.objects.Get(ctx, bundleKey)
	if err != nil {
		t.Fatalf("Get bundle: %v", err)
	}
	bundleBytes, _ := io.ReadAll(rc)
	rc.Close()
	var bundle TimelineAsset
	if err := json.Unmarshal(bundleBytes, &bundle); err != nil || bundle.Timeline == nil || len(bundle.Timeline.Slides) != 1 {
		t.Fatalf("bundle = %+v err=%v", bundleBytes, err)
	}
	if bundle.Timeline.Slides[0].SlideID != slideID || len(bundle.Timeline.Slides[0].Segments) == 0 {
		t.Fatalf("timeline slide = %+v", bundle.Timeline.Slides)
	}
	if bundle.SRTKey == "" || bundle.VTTKey == "" {
		t.Fatalf("missing subtitle keys: srt=%q vtt=%q", bundle.SRTKey, bundle.VTTKey)
	}
	// 音频对象存在。
	audioKey, err := objectstore.Parse(bundle.Timeline.Slides[0].Segments[0].AudioKey)
	if err != nil {
		t.Fatal(err)
	}
	if a, _, err := env.objects.Get(ctx, audioKey); err != nil {
		t.Fatalf("audio missing: %v", err)
	} else {
		a.Close()
	}

	// 5) LatestSucceededJob 可发现最近成功配音。
	latest, err := env.jobs.LatestSucceededJob(ctx, appTenant, appProject, string(pipeline.KindNarration))
	if err != nil {
		t.Fatalf("LatestSucceededJob: %v", err)
	}
	if latest.ID != narrJob.ID {
		t.Fatalf("latest = %s want %s", latest.ID, narrJob.ID)
	}

	// 6) 无配音项目 → ErrNoSucceededJob。
	if _, err := env.jobs.LatestSucceededJob(ctx, appTenant, "00000000-0000-0000-0000-0000000000cc", string(pipeline.KindNarration)); !errors.Is(err, pipeline.ErrNoSucceededJob) {
		t.Fatalf("missing narration job: got %v want ErrNoSucceededJob", err)
	}
}
