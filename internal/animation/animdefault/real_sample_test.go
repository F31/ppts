package animdefault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/project"
)

// 本文件是 animation-spec-core 3.4 的落地：用真实样本解析出的页生成默认规格，作为 golden 用例。
//
// 为什么值得单独建一份 golden，而不是继续用 defaults_test.go 里的手写页：
// 手写页的形态是我们自己想的（4 个要点 + 1 图 + 1 表），真实样本的形态是别人排的——
// 7~27 个形状、组合形状平铺、大量无文字自绘形状、表格与图片混排。默认规格生成依赖
// 「能挂动画的元素有哪些、阅读顺序怎么排、页内元素会不会爆 30 轨/40 clip 上限」，
// 这些只有在真实页上才暴露得出来。
//
// 样本是产品自己的方案讲解稿（14 页，282KB，无客户名/报价等商务信息），随 testdata 入库，
// 所以这条用例在 CI 里也真跑，不靠 skip 装绿。
const (
	realSamplePath = "testdata/sample-customer-deck.pptx"
	// realSampleName 是样本在仓库外的原名，便于回溯来源。
	realSampleName         = "智讲PPT-客户方案讲解.pptx"
	realSampleGoldenPath   = "testdata/real_sample.golden.json"
	realSampleSnapshotPath = "testdata/real_sample_page03.golden.json"

	// 固定 8s/30fps：默认规格的轨数与 clip 时长都随页时长变化，golden 必须把上下文钉住，
	// 否则改上下文就会让整份 golden 失效，掩盖真正想看的算法漂移。
	realSampleDurationMS     = 8000
	realSampleFPS            = 30
	realSampleSnapshotPage   = 3
	realSampleSnapshotPreset = "business-calm"
)

var updateGolden = flag.Bool("update", false, "重写 testdata 下的默认规格 golden 文件")

type realSampleGolden struct {
	Sample         realSampleMeta     `json:"sample"`
	PageDurationMS int64              `json:"pageDurationMs"`
	FPS            int                `json:"fps"`
	Presets        []realSamplePreset `json:"presets"`
}

type realSampleMeta struct {
	File          string `json:"file"`
	OriginalName  string `json:"originalName"`
	SHA256        string `json:"sha256"`
	Bytes         int    `json:"bytes"`
	Pages         int    `json:"pages"`
	SchemaVersion string `json:"schemaVersion"`
	Parser        string `json:"parser"`
}

type realSamplePreset struct {
	ID     string           `json:"id"`
	Tracks int              `json:"tracks"`
	Clips  int              `json:"clips"`
	Pages  []realSamplePage `json:"pages"`
}

type realSamplePage struct {
	Index    int    `json:"index"`
	SlideID  string `json:"slideId"`
	Anchors  int    `json:"anchors"`
	Tracks   int    `json:"tracks"`
	Clips    int    `json:"clips"`
	SpecHash string `json:"specHash"`
}

func TestDefaultSpecRealSampleGolden(t *testing.T) {
	raw, err := os.ReadFile(realSamplePath)
	if err != nil {
		t.Fatalf("读样本 %s 失败（golden 用例不允许 skip，样本必须随仓库分发）: %v", realSamplePath, err)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])

	rd := project.NewGoPPTXReader(project.Limits{})
	doc, err := rd.Inspect(context.Background(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("解析样本失败: %v", err)
	}
	if len(doc.Pages) == 0 {
		t.Fatal("样本解析出 0 页")
	}

	got := buildRealSampleGolden(t, doc, sha, len(raw))

	if *updateGolden {
		writeGoldenJSON(t, realSampleGoldenPath, got)
		writeGoldenJSON(t, realSampleSnapshotPath, snapshotTracks(t, doc))
		t.Logf("已重写 golden：%s / %s", realSampleGoldenPath, realSampleSnapshotPath)
		return
	}

	want := readGolden[realSampleGolden](t, realSampleGoldenPath)
	if got.Sample.Parser != want.Sample.Parser {
		t.Errorf("解析器版本变化：%s → %s。先把 go-pptx 升级带来的页结构差异看清楚，再用 -update 重定基线", want.Sample.Parser, got.Sample.Parser)
	}
	if got.Sample.SHA256 != want.Sample.SHA256 {
		t.Errorf("样本内容变了：%s → %s（换样本就要整份重定基线）", want.Sample.SHA256, got.Sample.SHA256)
	}
	if got.Sample.Pages != want.Sample.Pages {
		t.Fatalf("页数 %d，golden 记的是 %d", got.Sample.Pages, want.Sample.Pages)
	}

	for i, gp := range got.Presets {
		if i >= len(want.Presets) {
			t.Fatalf("预设 %s 不在 golden 里", gp.ID)
		}
		wp := want.Presets[i]
		if gp.ID != wp.ID {
			t.Fatalf("预设顺序变了：%s ≠ %s", gp.ID, wp.ID)
		}
		if gp.Tracks != wp.Tracks || gp.Clips != wp.Clips {
			t.Errorf("预设 %s 总量漂移：tracks %d→%d, clips %d→%d", gp.ID, wp.Tracks, gp.Tracks, wp.Clips, gp.Clips)
		}
		for j, page := range gp.Pages {
			if j >= len(wp.Pages) {
				t.Fatalf("预设 %s 第 %d 页不在 golden 里", gp.ID, page.Index)
			}
			exp := wp.Pages[j]
			if page.Index != exp.Index || page.SlideID != exp.SlideID {
				t.Fatalf("预设 %s 页序变了：%d/%s ≠ %d/%s", gp.ID, page.Index, page.SlideID, exp.Index, exp.SlideID)
			}
			if page.Anchors != exp.Anchors {
				t.Errorf("预设 %s 第 %d 页可挂载锚点数 %d→%d（解析或分类逻辑变了）", gp.ID, page.Index, exp.Anchors, page.Anchors)
			}
			if page.Tracks != exp.Tracks || page.Clips != exp.Clips {
				t.Errorf("预设 %s 第 %d 页 tracks %d→%d, clips %d→%d", gp.ID, page.Index, exp.Tracks, page.Tracks, exp.Clips, page.Clips)
			}
			if page.SpecHash != exp.SpecHash {
				t.Errorf("预设 %s 第 %d 页规格内容漂移（specHash %s → %s）", gp.ID, page.Index, exp.SpecHash[:12], page.SpecHash[:12])
			}
		}
	}

	// 完整快照：hash 只告诉你「变了」，快照告诉你「变成什么」。
	snapWant := readGolden[[]*animation.Track](t, realSampleSnapshotPath)
	snapGot := snapshotTracks(t, doc)
	gotJSON, _ := json.MarshalIndent(snapGot, "", "  ")
	wantJSON, _ := json.MarshalIndent(snapWant, "", "  ")
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("第 %d 页 %s 的完整规格与快照不一致\n--- got ---\n%s\n--- want ---\n%s",
			realSampleSnapshotPage, realSampleSnapshotPreset, truncate(string(gotJSON), 2000), truncate(string(wantJSON), 2000))
	}
}

// buildRealSampleGolden 对每一页 × 每一套预设生成默认规格，并就地断言不变量。
// 不变量不写进 golden：它们是「必须永远成立」的约束，golden 记的是「当前长什么样」。
func buildRealSampleGolden(t *testing.T, doc *project.Document, sha string, size int) *realSampleGolden {
	t.Helper()
	out := &realSampleGolden{
		Sample: realSampleMeta{
			File:          realSamplePath,
			OriginalName:  realSampleName,
			SHA256:        sha,
			Bytes:         size,
			Pages:         len(doc.Pages),
			SchemaVersion: doc.SchemaVersion,
			Parser:        doc.ParserName + "/" + doc.ParserVersion,
		},
		PageDurationMS: realSampleDurationMS,
		FPS:            realSampleFPS,
	}

	for _, preset := range animation.Catalog().Presets() {
		gp := realSamplePreset{ID: preset.ID}
		for _, page := range doc.Pages {
			rev := realSampleSpec(t, page, preset.ID)
			assertRealPageInvariants(t, page.Index, preset.ID, rev)

			hash := specHash(t, rev)
			// 同一输入必须产出同一规格（3.3 已用手写页断言过，真实页也来一次：
			// 真实页里有大量同坐标形状，排序的稳定性只有在这种页上才会被压出来）。
			if again := specHash(t, realSampleSpec(t, page, preset.ID)); again != hash {
				t.Fatalf("预设 %s 第 %d 页两次生成不一致（非确定性）", preset.ID, page.Index)
			}

			clips := 0
			for _, tr := range rev.Tracks {
				clips += len(tr.Clips)
			}
			gp.Pages = append(gp.Pages, realSamplePage{
				Index:    page.Index,
				SlideID:  page.SlideID,
				Anchors:  len(PageAnchors(page)),
				Tracks:   len(rev.Tracks),
				Clips:    clips,
				SpecHash: hash,
			})
			gp.Tracks += len(rev.Tracks)
			gp.Clips += clips
		}
		out.Presets = append(out.Presets, gp)
	}
	return out
}

func realSampleSpec(t *testing.T, page *project.Page, presetID string) *animation.Revision {
	t.Helper()
	rev, err := DefaultSpec(page, DefaultOptions{
		SlideID:    page.SlideID,
		Language:   "zh-CN",
		DurationMS: realSampleDurationMS,
		FPS:        realSampleFPS,
		PresetID:   presetID,
	})
	if err != nil {
		t.Fatalf("预设 %s 第 %d 页生成失败: %v", presetID, page.Index, err)
	}
	return rev
}

// assertRealPageInvariants 断言默认规格在真实页上也不会越界。
// 这里偏爱「逐条算」而不是只看 rev.Validate：Validate 是产品侧的拒收规则，
// 而默认规格是我们自己生成的——它必须天然满足这些界，越界就说明生成逻辑错了。
func assertRealPageInvariants(t *testing.T, pageIndex int, presetID string, rev *animation.Revision) {
	t.Helper()
	if len(rev.Tracks) == 0 {
		t.Errorf("预设 %s 第 %d 页生成了 0 条轨道（真实页至少有页面级兜底）", presetID, pageIndex)
	}
	if n := len(rev.Tracks); n > animation.MaxTracksPerSlide {
		t.Errorf("预设 %s 第 %d 页轨道 %d 超上限 %d", presetID, pageIndex, n, animation.MaxTracksPerSlide)
	}
	clips := 0
	for _, tr := range rev.Tracks {
		for _, c := range tr.Clips {
			clips++
			eff, ok := animation.Catalog().Effect(c.Type)
			if !ok {
				t.Errorf("预设 %s 第 %d 页出现未知动效 %q", presetID, pageIndex, c.Type)
				continue
			}
			if c.DurationMs < eff.DurationRange[0] || c.DurationMs > eff.DurationRange[1] {
				t.Errorf("预设 %s 第 %d 页 %s 时长 %d 越出 catalog 区间 %v", presetID, pageIndex, c.Type, c.DurationMs, eff.DurationRange)
			}
			if c.StartMs < 0 || c.StartMs+c.DurationMs > realSampleDurationMS {
				t.Errorf("预设 %s 第 %d 页 %s 时间轴 [%d,%d] 溢出页时长 %d", presetID, pageIndex, c.Type, c.StartMs, c.StartMs+c.DurationMs, realSampleDurationMS)
			}
			if !validTrigger(c.Trigger) {
				t.Errorf("预设 %s 第 %d 页 trigger %q 非法", presetID, pageIndex, c.Trigger)
			}
		}
	}
	if clips > animation.MaxClipsPerSlide {
		t.Errorf("预设 %s 第 %d 页 clip %d 超上限 %d", presetID, pageIndex, clips, animation.MaxClipsPerSlide)
	}
}

func validTrigger(tr string) bool {
	switch {
	case tr == animation.TriggerEnter:
		return true
	case strings.HasPrefix(tr, animation.TriggerSegmentPrefix):
		return len(tr) > len(animation.TriggerSegmentPrefix)
	case strings.HasPrefix(tr, "atTime:"):
		return true
	}
	return false
}

func specHash(t *testing.T, rev *animation.Revision) string {
	t.Helper()
	body, err := json.Marshal(struct {
		Preset    string             `json:"preset"`
		Intensity float64            `json:"intensity"`
		Status    string             `json:"status"`
		Tracks    []*animation.Track `json:"tracks"`
	}{rev.PresetID, rev.Intensity, string(rev.Status), rev.Tracks})
	if err != nil {
		t.Fatalf("规格序列化失败: %v", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func snapshotTracks(t *testing.T, doc *project.Document) []*animation.Track {
	t.Helper()
	for _, page := range doc.Pages {
		if page.Index == realSampleSnapshotPage {
			return realSampleSpec(t, page, realSampleSnapshotPreset).Tracks
		}
	}
	t.Fatalf("样本里没有第 %d 页", realSampleSnapshotPage)
	return nil
}

func writeGoldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", path, err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

func readGolden[T any](t *testing.T, path string) T {
	t.Helper()
	var v T
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 golden %s 失败（首次请用 go test -run TestDefaultSpecRealSampleGolden -update 生成）: %v", path, err)
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("解析 golden %s 失败: %v", path, err)
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n…（截断，完整内容见 %s）", realSampleSnapshotPath)
}
