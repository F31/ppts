package animation

import "strings"

// StaleSegmentIDs 列出规格里 withSegment 指向、但当前讲稿已不存在的分段 id。
// 配音重做会换一批 seg-id；保存时 V7 会 409，这里给编辑器提前提示。
func StaleSegmentIDs(rev *Revision, live []string) []string {
	if rev == nil {
		return nil
	}
	ok := make(map[string]struct{}, len(live))
	for _, id := range live {
		if id != "" {
			ok[id] = struct{}{}
		}
	}
	var stale []string
	seen := make(map[string]struct{})
	for _, track := range rev.Tracks {
		if track == nil {
			continue
		}
		for _, clip := range track.Clips {
			if clip == nil || !strings.HasPrefix(clip.Trigger, TriggerSegmentPrefix) {
				continue
			}
			id := strings.TrimPrefix(clip.Trigger, TriggerSegmentPrefix)
			if id == "" {
				continue
			}
			if _, exists := ok[id]; exists {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			stale = append(stale, id)
		}
	}
	return stale
}
