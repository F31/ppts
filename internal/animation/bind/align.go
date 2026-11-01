package bind

// align 是单调对齐 DP，返回 (元素下标, 段下标, 相似度) 三元组，按元素下标升序。
//
// dp[i][j] = 前 i 个元素、只用到前 j 段时的最大相似度和。三种转移：
// 段 j 空着不用 / 元素 i 不绑 / 元素 i 绑到段 j（相似度 ≥ 阈值才允许）。
// `bind` 分支**不推进 j**，所以一段可以连续带出多个元素——「一段话带出好几个元素」
// 正是真实讲解的常态。
//
// 为什么不是「每个元素找最像的段」：四行条目共享「本期」这类高频词，逐个取最大值会把
// 它们全绑到第一段；而且真实讲解不会「讲到第三行又退回第一段」。这条单调约束把搜索空间
// 压掉一个量级，且它不来自数据，来自「讲解是线性的」这一事实。
func align(elements []Element, segments []Segment, threshold float64) ([]pair, [][]float64) {
	n, m := len(elements), len(segments)
	sims := make([][]float64, n)
	for i := range elements {
		sims[i] = make([]float64, m)
		for j := range segments {
			sims[i][j], _ = similarity(segments[j].Text, elements[i].Text, elements[i].Kind)
		}
	}
	if n == 0 || m == 0 {
		return nil, sims
	}

	dp := make([][]float64, n+1)
	back := make([][]byte, n+1) // 0=未访问 1=elem 2=seg 3=bind
	for i := range dp {
		dp[i] = make([]float64, m+1)
		back[i] = make([]byte, m+1)
	}

	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			// 默认：元素 i 不绑（与 dp[i-1][j] 等价，但显式记下来更利于回溯）
			best, how := dp[i-1][j], byte(1)
			if dp[i][j-1] > best { // 段 j 空着不用
				best, how = dp[i][j-1], 2
			}
			if s := sims[i-1][j-1]; s >= threshold && dp[i-1][j]+s > best {
				best, how = dp[i-1][j]+s, 3
			}
			dp[i][j], back[i][j] = best, how
		}
	}

	var pairs []pair
	i, j := n, m
	for i > 0 && j > 0 {
		switch back[i][j] {
		case 3:
			pairs = append(pairs, pair{elem: i - 1, seg: j - 1, score: sims[i-1][j-1]})
			i-- // j 不动：同一段可以继续绑前面的元素
		case 1:
			i--
		case 2:
			j--
		default:
			// 不可达：i、j 都 ≥1 时每个格子都会被赋值。留一个显式出口，
			// 免得不变量将来被改坏时静默死循环。
			i, j = 0, 0
		}
	}
	reversePairs(pairs)
	return pairs, sims
}

func reversePairs(p []pair) {
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
}
