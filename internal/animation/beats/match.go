package beats

import (
	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
)

func matchPieces(pieces []piece, cands []bind.Element) []animation.Beat {
	beats := make([]animation.Beat, 0, len(pieces))
	used := make(map[string]struct{}, len(cands))
	for _, p := range pieces {
		id := pickShape(p.Text, cands, used)
		b := animation.Beat{
			StartMs:   p.StartMs,
			EndMs:     p.EndMs,
			SegmentID: p.SegmentID,
		}
		if id == "" {
			b.Shot = animation.BeatShotHold
		} else {
			b.ShapeID = id
			b.Shot = animation.BeatShotGaze
			used[id] = struct{}{}
		}
		beats = append(beats, b)
	}
	assignOpeningHole(beats)
	return beats
}

func pickShape(text string, cands []bind.Element, used map[string]struct{}) string {
	bestIdx := -1
	var bestScore float64
	for i, el := range cands {
		if _, ok := used[el.ID]; ok {
			continue
		}
		s := bind.Score(text, el.Text, el.Kind)
		if s < bind.DefaultThreshold {
			continue
		}
		if bestIdx < 0 || betterMatch(el, s, cands[bestIdx], bestScore) {
			bestIdx = i
			bestScore = s
		}
	}
	if bestIdx < 0 {
		return ""
	}
	return cands[bestIdx].ID
}

// 全序：Score 高 → title → 阅读序 (Y,X) → 更小 ID。
func betterMatch(el bind.Element, score float64, best bind.Element, bestScore float64) bool {
	if score != bestScore {
		return score > bestScore
	}
	elTitle := el.Kind == "title"
	bestTitle := best.Kind == "title"
	if elTitle != bestTitle {
		return elTitle
	}
	if el.Box.Y != best.Box.Y {
		return el.Box.Y < best.Box.Y
	}
	if el.Box.X != best.Box.X {
		return el.Box.X < best.Box.X
	}
	return el.ID < best.ID
}

// 拍 0 未命中：把后面第一记 gaze 的 ShapeID 写回拍 0，该记改为 hold。
func assignOpeningHole(beats []animation.Beat) {
	if len(beats) == 0 {
		return
	}
	if beats[0].ShapeID != "" && beats[0].Shot != animation.BeatShotHold {
		return
	}
	for i := 1; i < len(beats); i++ {
		if beats[i].ShapeID == "" || beats[i].Shot == animation.BeatShotHold {
			continue
		}
		beats[0].ShapeID = beats[i].ShapeID
		beats[0].Shot = animation.BeatShotGaze
		beats[i].ShapeID = ""
		beats[i].Shot = animation.BeatShotHold
		return
	}
}

func uniqueGazeIDs(beats []animation.Beat) map[string]struct{} {
	seen := make(map[string]struct{})
	for _, b := range beats {
		if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
			continue
		}
		seen[b.ShapeID] = struct{}{}
	}
	return seen
}
