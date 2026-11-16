package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/F31/ppts/internal/api"
)

// runApplyAnimation 用与 HTTP apply-all 同一条路径把预设写进 animation_revisions。
// 不读 / 不打印密钥：依赖进程环境（systemd EnvironmentFile）。
func runApplyAnimation(args []string) error {
	fs := flag.NewFlagSet("apply-animation", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tenantID := fs.String("tenant", "", "tenant UUID")
	projectID := fs.String("project", "", "project UUID")
	userID := fs.String("user", "", "acting user UUID; empty skips owner filter")
	preset := fs.String("preset", "teaching-clear", "catalog preset id")
	language := fs.String("language", "zh-CN", "spec language")
	overwrite := fs.Bool("overwrite", false, "replace manual edits")
	entrance := fs.Bool("entrance", false, "write gaze+entrance using catalog EntranceClips")
	sourceRev := fs.Int("source-revision", 0, "document revision; 0 = current")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*tenantID) == "" || strings.TrimSpace(*projectID) == "" {
		return fmt.Errorf("apply-animation: --tenant and --project are required")
	}

	ctx := context.Background()
	stores, err := openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()
	if stores.animation == nil {
		return fmt.Errorf("apply-animation: animation store is not configured")
	}

	pages, deck := api.NewAnimationResolver(stores.projects, stores.objects, stores.scripts, stores.jobs)
	h := api.NewAnimationHandler(stores.animation, pages, true).WithDeck(deck)
	summary, err := h.ApplyAllSlides(ctx, *tenantID, *userID, *projectID, *sourceRev, *language, api.ApplyAllOptions{
		PresetID:     *preset,
		Overwrite:    *overwrite,
		Prefill:      true,
		WithEntrance: *entrance,
	})
	if err != nil {
		return err
	}
	fmt.Printf("PRESET %s\n", summary.PresetID)
	fmt.Printf("TOTAL %d APPLIED %d SKIPPED %d\n", summary.Total, summary.Applied, summary.Skipped)
	for _, line := range summary.Lines {
		fmt.Println(line)
	}
	return nil
}
