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
	return spreadDuplicates(pairs, sims, m), sims
}

// spreadDuplicates 把「同文元素全堆在同一段上」摊开：从第二个起，依次挪到它后面
// 第一处**同样匹配、且还空着**的段。
//
// 为什么需要这一步：`bind` 分支不推进 j，所以「一段能带出多个元素」和「一个元素
// 找一个最像的段」在总分上可能**完全相等**，DP 会选前者——于是 k 个字面相同的元素
// 全挤在第 1 段，第 2…k 段空着。p32 的两个「主要产品」（两张卡片各一个）、
// p16 的两个「Token运营」（网关框内、右栏各一个）都是这样：0.8+0.8 与
// 「各绑各的」同分，最后第二个元素被算成错绑。
//
// 为什么不在 DP 里解决：要区分「第 j 段是否已经用过」得把它放进状态，
// 状态量从 n·m 变成 n·m·2^m。这个偏差是结构性的、只在同文时出现，
// 绑完再做一次局部修正更划算，也更好解释。
//
// 只挪到「分数不低于原段」的空段上：讲稿真的只提了一次、后面没有对应句时，
// 摊开会让分数下降，那时保持原样（一段带出多个元素本来就是常态）。
func spreadDuplicates(pairs []pair, sims [][]float64, m int) []pair {
	if len(pairs) < 2 {
		return pairs
	}
	used := make([]bool, m)
	bySeg := make(map[int][]int, len(pairs)) // seg -> 元素下标（升序）
	for _, p := range pairs {
		used[p.seg] = true
		bySeg[p.seg] = append(bySeg[p.seg], p.elem)
	}
	// 位移上界：本段之后第一处已被占用的段。越过它就会打乱单调顺序——
	// 那一处属于元素序更靠后的元素，不能插到它前面。
	limitOf := func(j int) int {
		for k := j + 1; k < m; k++ {
			if used[k] {
				return k
			}
		}
		return m
	}

	for j := 0; j < m; j++ {
		group := bySeg[j]
		if len(group) < 2 {
			continue
		}
		limit := limitOf(j)
		slot := j
		for _, e := range group[1:] { // 第一个留在原段，它是这段的主元素
			next := slot + 1
			for next < limit && sims[e][next] < sims[e][j] {
				next++
			}
			if next >= limit {
				break
			}
			setPairSeg(pairs, e, next, sims[e][next])
			used[next] = true
			slot = next
		}
	}
	return pairs
}

func setPairSeg(pairs []pair, elem, seg int, score float64) {
	for k := range pairs {
		if pairs[k].elem == elem {
			pairs[k].seg = seg
			pairs[k].score = score
			return
		}
	}
}

func reversePairs(p []pair) {
	for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
		p[i], p[j] = p[j], p[i]
	}
}
