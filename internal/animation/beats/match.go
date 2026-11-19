package beats

import (
	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
)

func matchPieces(pieces []piece, cands []bind.Element) []animation.Beat {
	beats := make([]animation.Beat, 0, len(pieces))
	matched := alignPiecesWithSections(pieces, cands)
	for i, p := range pieces {
		b := animation.Beat{
			StartMs:   p.StartMs,
			EndMs:     p.EndMs,
			SegmentID: p.SegmentID,
			Text:      p.Text,
		}
		if matched[i] < 0 {
			b.Shot = animation.BeatShotHold
		} else {
			b.ShapeID = cands[matched[i]].ID
			b.Shot = animation.BeatShotGaze
		}
		beats = append(beats, b)
	}
	assignOpeningHole(beats)
	return beats
}

// alignPiecesWithSections 先固定页面结构给出的 section 落点，再让其余拍点走 DP。
//
// section 落点来自旁白数字引用（第 4 板块）与页面编号（04 AI Coding）的对应。
// 它不是文本相似度猜出来的，所以即使一句里说了多个分区，也不会被 DP 合并掉一个。
func alignPiecesWithSections(pieces []piece, cands []bind.Element) []int {
	matched := make([]int, len(pieces))
	for i := range matched {
		matched[i] = -1
	}
	if len(pieces) == 0 || len(cands) == 0 {
		return matched
	}
	byID := make(map[string]int, len(cands))
	for i, c := range cands {
		if _, ok := byID[c.ID]; !ok && c.ID != "" {
			byID[c.ID] = i
		}
	}
	used := make(map[int]struct{}, len(pieces))
	anchored := false
	for i, p := range pieces {
		if p.SectionID == "" {
			continue
		}
		j, ok := byID[p.SectionID]
		if !ok {
			continue
		}
		if _, dup := used[j]; dup {
			continue
		}
		matched[i] = j
		used[j] = struct{}{}
		anchored = true
	}
	if !anchored {
		return alignPieces(pieces, cands)
	}

	var subPieces []piece
	var subPieceIndex []int
	for i, p := range pieces {
		if matched[i] >= 0 {
			continue
		}
		subPieces = append(subPieces, p)
		subPieceIndex = append(subPieceIndex, i)
	}
	var subCands []bind.Element
	var subCandIndex []int
	for j, c := range cands {
		if _, ok := used[j]; ok {
			continue
		}
		subCands = append(subCands, c)
		subCandIndex = append(subCandIndex, j)
	}
	if len(subPieces) > 0 && len(subCands) > 0 {
		subMatched := alignPieces(subPieces, subCands)
		for i, j := range subMatched {
			if j >= 0 {
				matched[subPieceIndex[i]] = subCandIndex[j]
			}
		}
	}
	return matched
}

// alignPieces 在拍点与候选形状之间做单调一一对齐。
//
// 旧实现逐个拍点取当前最高分并永久占用，前面的弱命中会抢走后面的目标；
// 这里用 DP 求全局最优：拍点和候选都只向前走，一拍最多一个形状、一个形状最多一拍。
// 讲解是线性的，这条单调约束与 bind.align 同源。
func alignPieces(pieces []piece, cands []bind.Element) []int {
	n, m := len(pieces), len(cands)
	matched := make([]int, n)
	for i := range matched {
		matched[i] = -1
	}
	if n == 0 || m == 0 {
		return matched
	}

	scores := make([][]float64, n)
	for i := range pieces {
		scores[i] = make([]float64, m)
		for j := range cands {
			scores[i][j] = bind.Score(pieces[i].Text, cands[j].Text, cands[j].Kind)
		}
	}

	dp := make([][]float64, n+1)
	back := make([][]byte, n+1) // 1=skip piece, 2=skip candidate, 3=bind
	for i := range dp {
		dp[i] = make([]float64, m+1)
		back[i] = make([]byte, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			best, how := dp[i-1][j], byte(1)
			if dp[i][j-1] > best {
				best, how = dp[i][j-1], 2
			}
			if s := scores[i-1][j-1]; s >= bind.DefaultThreshold && dp[i-1][j-1]+s > best {
				best, how = dp[i-1][j-1]+s, 3
			}
			dp[i][j], back[i][j] = best, how
		}
	}
	i, j := n, m
	for i > 0 && j > 0 {
		switch back[i][j] {
		case 3:
			matched[i-1] = j - 1
			i--
			j--
		case 1:
			i--
		case 2:
			j--
		default:
			i, j = 0, 0
		}
	}
	fillUnmatched(pieces, matched, cands)
	return matched
}

// fillUnmatched 给单调 DP 没匹配上的拍点补一次「未占用候选」的字面匹配。
//
// DP 假设讲解顺序与阅读顺序一致；真实讲稿偶尔会倒着点（先讲第二张再讲第一张）。
// 这层只补空位、绝不移动已匹配目标，所以不会破坏 TOC 的单调对齐；它只是把
// 旧贪心能覆盖的倒序句接住，避免为了平滑反而掉召回。
func fillUnmatched(pieces []piece, matched []int, cands []bind.Element) {
	used := make(map[string]struct{}, len(cands))
	for _, idx := range matched {
		if idx >= 0 && idx < len(cands) {
			used[cands[idx].ID] = struct{}{}
		}
	}
	for i, p := range pieces {
		if matched[i] >= 0 {
			continue
		}
		if idx := pickBestUnused(p.Text, cands, used); idx >= 0 {
			matched[i] = idx
			used[cands[idx].ID] = struct{}{}
		}
	}
}

func pickBestUnused(text string, cands []bind.Element, used map[string]struct{}) int {
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
	return bestIdx
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
