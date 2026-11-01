// Package bind 把「讲解段落」与「页内元素」对齐，产出可预填的绑定。
//
// 它的前身是 `scripts/fidelity/bind_elements.py`（Q7 实验），本包是同一套规则的 Go 移植。
// 移植的目标不是「差不多准」，而是**逐条复现金标**：`sample-customer-deck.pptx` 的
// 1/2/4/8/10 页 + `q8-script.json` 上，内容元素召回 0.972 / 精确 0.962（见 bind_golden_test.go）。
// 任何一处正则、权重、转移顺序的偏差都会把这两个数打下来，所以下面的实现刻意贴着原脚本写，
// 包括看起来多余的步骤（例如 count 规则里对 `(?<!第)` 的绕行）。
//
// 三条性质是硬约束：
//
//   - **纯函数**：不读时钟、不用随机数、不依赖 map 遍历顺序。同一输入重跑必须逐条相等，
//     否则用户「重跑一次结果就变」会直接毁掉对预填的信任。
//   - **单调**：段落与元素只沿讲解顺序前进。真实讲解不会「讲到第三行又退回第一段」，
//     这条约束不来自数据，来自讲解是线性的这一事实。
//   - **精确优先于召回**：阈值让它在不确定时「不绑」。漏绑用户看得见（轨道上是空位），
//     错绑用户看不见（不该动的时候动了）。
package bind

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 相似度权重与阈值。数值来自 Q7 实验（见 proposal.md 的对照表），不是本轮新调的参。
const (
	// DefaultThreshold 是「允许绑」的最低相似度。0.12 是「至少有两个 bigram 重合」的量级。
	DefaultThreshold = 0.12
	// StaggerMS 是同一段带出多个元素时的错落间隔，与 q7-script.json 手写的 0/140/320 一致。
	StaggerMS = 140

	weightDice  = 0.5
	weightLCS   = 0.3
	weightToken = 0.2
	// titleBoost 是标题类元素的相似度加成：标题是唯一「整句复述」概率高的元素，
	// 放宽它对召回有利，且它每页只有一个，误绑代价小。
	titleBoost = 1.15
	// minLCSLen 以下的文本只认「完全相等」：极短文本上 lcs 覆盖率会被一个字顶到 0.5，
	// 段里出现任意一个 0，「01」就"像"了。
	minLCSLen = 3
)

var (
	// 归一化：删除一切非字母数字字符。Python 侧写的是 `[\s\W_]`——空白、非单词字符、
	// 下划线，三者合起来正好是「非字母数字」，这里直接写成补集，避免再讨论 \w 含不含下划线。
	nonAlnum = regexp.MustCompile(`[^\p{L}\p{N}]+`)

	// 数字与英文分开抽：`174s` 整串是一个 token 时，对「174 秒」永远匹配不上
	// （中文量词不在 [a-z0-9] 里）。分开之后 174 与 s 各是一个 token，数字就对上了。
	numRe   = regexp.MustCompile(`\p{Nd}+(?:\.\p{Nd}+)?`)
	alphaRe = regexp.MustCompile(`[a-z]{2,}`)

	// 中文数字改写：只动「两位以上且带单位」的串，以及「X点Y」。
	// 单字不动是有意的——`一致`、`统一`、`一部分` 里的数词一旦被改写，
	// 文本会变成 `1致` `2次` 这种怪物，bigram 反而更差。
	cnDec = regexp.MustCompile(`([零一二三四五六七八九])点([零一二三四五六七八九]+)`)
	cnRun = regexp.MustCompile(`[零一二三四五六七八九十百千万两]{2,}`)

	cnDigit = map[string]int{"零": 0, "一": 1, "二": 2, "两": 2, "三": 3, "四": 4,
		"五": 5, "六": 6, "七": 7, "八": 8, "九": 9}
	cnUnit = map[string]int{"十": 10, "百": 100, "千": 1000, "万": 10000}
)

// normalize 归一化：NFKC、删除空白与标点、小写。
//
// 保留数字与拉丁字母。`°`、`·`、`－` 这些在讲解词里不会念出来，留着只会稀释 bigram。
func normalize(s string) string {
	s = norm.NFKC.String(s)
	s = nonAlnum.ReplaceAllString(s, "")
	return strings.ToLower(s)
}

// bigrams 按 **rune** 取二元组（不是字节）：中文一个字三个字节，按字节切会把一个字
// 拆成半个字，让所有中文文本的 Dice 都变成噪声。
func bigrams(s string) map[string]int {
	r := []rune(s)
	out := make(map[string]int, len(r))
	if len(r) < 2 {
		for _, c := range r {
			out[string(c)]++
		}
		return out
	}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])]++
	}
	return out
}

// dice 是 bigram 的 Dice 系数：2·|交集| / (|A|+|B|)。
func dice(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	ca, cb := bigrams(a), bigrams(b)
	inter, total := 0, 0
	for k, v := range ca {
		total += v
		if w, ok := cb[k]; ok {
			inter += min(v, w)
		}
	}
	for _, v := range cb {
		total += v
	}
	if total == 0 {
		return 0
	}
	return 2 * float64(inter) / float64(total)
}

// lcsLen 返回最长公共**子串**（连续）长度。滚动数组，O(min(n,m)) 空间。
func lcsLen(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(rb)+1)
	best := 0
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		for j := 1; j <= len(rb); j++ {
			if ra[i-1] == rb[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > best {
					best = cur[j]
				}
			}
		}
		prev = cur
	}
	return best
}

// cnInt 把「一百七十四」解析成 174。非法组合返回 false（保持原样）。
func cnInt(run string) (int, bool) {
	total, section, number := 0, 0, 0
	for _, ch := range run {
		c := string(ch)
		if v, ok := cnDigit[c]; ok {
			number = v
			continue
		}
		unit, ok := cnUnit[c]
		if !ok {
			return 0, false
		}
		if unit == 10000 {
			section = (section + number) * unit
			total, section = total+section, 0
		} else {
			if number == 0 {
				number = 1
			}
			section += number * unit
		}
		number = 0
	}
	val := total + section + number
	if val <= 0 || val >= 100000 {
		return 0, false
	}
	return val, true
}

// cn2num 把中文数字改写成阿拉伯数字，只动「两位以上且带单位」的串。
func cn2num(s string) string {
	if locs := cnDec.FindAllStringSubmatchIndex(s, -1); len(locs) > 0 {
		var b strings.Builder
		last := 0
		for _, m := range locs {
			b.WriteString(s[last:m[0]])
			head := []rune(s[m[2]:m[3]])[0]
			b.WriteString(strconv.Itoa(cnDigit[string(head)]))
			b.WriteByte('.')
			for _, c := range s[m[4]:m[5]] {
				b.WriteString(strconv.Itoa(cnDigit[string(c)]))
			}
			last = m[1]
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	if locs := cnRun.FindAllStringIndex(s, -1); len(locs) > 0 {
		var b strings.Builder
		last := 0
		for _, m := range locs {
			b.WriteString(s[last:m[0]])
			if v, ok := cnInt(s[m[0]:m[1]]); ok {
				b.WriteString(strconv.Itoa(v))
			} else {
				b.WriteString(s[m[0]:m[1]])
			}
			last = m[1]
		}
		b.WriteString(s[last:])
		s = b.String()
	}
	return s
}

// tokens 抽出数字 token 与英文 token。中文不切词——没有分词器，切了也只是猜。
func tokens(s string) map[string]struct{} {
	s2 := strings.ToLower(cn2num(norm.NFKC.String(s)))
	out := make(map[string]struct{})
	for _, m := range numRe.FindAllString(s2, -1) {
		out[m] = struct{}{}
	}
	for _, m := range alphaRe.FindAllString(s2, -1) {
		out[m] = struct{}{}
	}
	return out
}

// SimilarityDetail 是三路相似度的明细，供报告与调参时看「分是哪来的」。
type SimilarityDetail struct {
	Dice  float64
	LCS   float64
	Token float64
}

// similarity 返回段落与元素的相似度（0..1）与明细。
//
// 三路加权：bigram Dice 0.5、最长公共子串覆盖率 0.3、token 命中 0.2。
// 子串覆盖率单独留一路，是因为短文本上 Dice 会被高频单字摊薄：
// 「架构要点」对「五段流水线…架构上每段独立重试」，共享的只有「段」「独」，
// Dice 低，但「架构」这个连续子串已经说明是同一件事。
func similarity(segText, elemText, elemKind string) (float64, SimilarityDetail) {
	a, b := normalize(cn2num(segText)), normalize(cn2num(elemText))
	if a == "" || b == "" {
		return 0, SimilarityDetail{}
	}

	d := dice(a, b)
	short := min(len([]rune(a)), len([]rune(b)))
	var lc float64
	if short < minLCSLen {
		if a == b {
			lc = 1
		}
	} else {
		lc = float64(lcsLen(a, b)) / float64(short)
	}

	ta, tb := tokens(segText), tokens(elemText)
	var tok float64
	if len(tb) > 0 {
		hit := 0
		for t := range tb {
			if _, ok := ta[t]; ok {
				hit++
			}
		}
		tok = float64(hit) / float64(len(tb))
	}

	score := weightDice*d + weightLCS*lc + weightToken*tok
	if elemKind == "title" {
		score *= titleBoost
	}
	return min(score, 1), SimilarityDetail{Dice: d, LCS: lc, Token: tok}
}
