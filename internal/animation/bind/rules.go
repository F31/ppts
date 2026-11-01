package bind

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// 页面坐标系。两个样本都换算到 1920×1080，方位判定与面积占比才有可比的量纲。
const (
	pageW = 1920.0
	pageH = 1080.0
)

// denyKinds 是默认**不参与绑定**的元素类别：它们不是讲解的抓手，而是页面的元信息。
// 词典刻意保守——每加一条都要能说出「为什么它不该单独淡入」，否则会把该动的东西一起关掉。
var denyKinds = map[string]struct{}{
	"page_number": {}, "eyebrow": {}, "note": {}, "footer": {},
}

// auxTextRe 是从文本前缀识别「元信息」的第二道判据（与 kind 分开记，任一道命中就不绑）。
var auxTextRe = regexp.MustCompile(
	`^(图|表|注)\s*[:：]` + // 图注 / 表注
		`|资料来源|来源[:：]` + // 出处
		`|联系[:：]|分机|电话[:：]` + // 联系方式
		`|汇报人|报告人` + // 讲者信息
		`|内部资料|仅供[内参]|©`) // 文档密级与版权

// qtyRe 匹配「五个 / 四条 / 三步」这类数量概括。
//
// 原脚本用的是 `(?<!第)`：`第二条` 是序数，不是「两条」。Go 的 regexp 不支持后顾，
// 所以改成「先全量匹配，再逐个看前一个字符是不是『第』」——见 qtyMatch。
// 不加这条的话，逐条讲的页面会在每条上各触发一次计数规则，把整页的编号块反复绑到最后一段。
var qtyRe = regexp.MustCompile(`[一二两三四五六七八九十\p{Nd}]{1,3}\s*(?:步|个|条|项|块|部分|段|层|方面|件事|类)`)

// ordinalRe 匹配「第N步 / 第N条」：出现它就说明列表是被**逐条**讲的。
var ordinalRe = regexp.MustCompile(`第[一二三四五六七八九十\p{Nd}]{1,3}\s*(?:步|条|个|点|项|部分|块|类)`)

// itemRe 匹配元素文本内部的有序条目（`1. … 2. …`），用于「一个元素里装了整张列表」。
var itemRe = regexp.MustCompile(`(?:^|[\s、，,;；:：])\s*\p{Nd}{1,2}\s*[.、)）]\s*`)

// dirRe 只认「左/右/上/下 + 边/侧/方/角/半」这种带后缀的形态：
// 「下面我们看」里的「下面」是语篇连接词，不是空间指代，误判一次就绑错一整段。
var dirRe = regexp.MustCompile(`(左|右|上|下)(?:边|侧|方|角|半)`)

// markerRe 是编号标记的完整匹配。
var markerRe = regexp.MustCompile(`^0?\p{Nd}{1,2}$`)

var (
	centerWords = []string{"中间", "正中", "居中", "中央"}
	tableRefs   = []string{"这张表", "表格", "表里", "下表", "上表", "这张报表"}
	imageRefs   = []string{"这张图", "图里", "看图", "截图", "示意图", "架构图", "流程图",
		"照片", "画面", "右边是", "左侧是"}
)

// boundSet 记录「这个元素已经有主了，归哪一段」。
type boundSet map[int]int

func boundOf(pairs []pair) boundSet {
	out := make(boundSet, len(pairs))
	for _, p := range pairs {
		out[p.elem] = p.seg
	}
	return out
}

func (b boundSet) has(i int) bool {
	_, ok := b[i]
	return ok
}

func (b boundSet) update(pairs []pair) {
	for _, p := range pairs {
		b[p.elem] = p.seg
	}
}

func isAux(e Element) bool {
	if _, ok := denyKinds[e.Kind]; ok {
		return true
	}
	return auxTextRe.MatchString(strings.TrimSpace(e.Text))
}

func area(e Element) float64 {
	return max(e.Box.W, 0) * max(e.Box.H, 0)
}

func center(e Element) (float64, float64) {
	return e.Box.X + e.Box.W/2, e.Box.Y + e.Box.H/2
}

// contains 判断 inner 的**中心点**是否落在 outer 内。用中心点而不是整体包含：
// 卡片标题常有一两个像素压在卡片边界外，整体包含会漏掉它。
func contains(outer, inner Element) bool {
	ix, iy := center(inner)
	return outer.Box.X <= ix && ix <= outer.Box.X+outer.Box.W &&
		outer.Box.Y <= iy && iy <= outer.Box.Y+outer.Box.H
}

func overlapY(a, b Element) float64 {
	lo, hi := max(a.Box.Y, b.Box.Y), min(a.Box.Y+a.Box.H, b.Box.Y+b.Box.H)
	if hi <= lo {
		return 0
	}
	return (hi - lo) / max(1, min(a.Box.H, b.Box.H))
}

func overlapX(a, b Element) float64 {
	lo, hi := max(a.Box.X, b.Box.X), min(a.Box.X+a.Box.W, b.Box.X+b.Box.W)
	if hi <= lo {
		return 0
	}
	return (hi - lo) / max(1, min(a.Box.W, b.Box.W))
}

func isMarker(e Element) bool {
	return e.Kind == "marker" || markerRe.MatchString(strings.TrimSpace(e.Text))
}

// parseDigits 把纯阿拉伯数字串转成 int；含非数字返回错误。
func parseDigits(s string) (int, error) {
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	return strconv.Atoi(s)
}

// qtyMatch 找段里第一个「不是序数」的数量词，返回整段匹配。
func qtyMatch(text string) (string, bool) {
	for _, loc := range qtyRe.FindAllStringIndex(text, -1) {
		if loc[0] > 0 {
			runes := []rune(text[:loc[0]])
			if len(runes) > 0 && runes[len(runes)-1] == '第' {
				continue
			}
		}
		return text[loc[0]:loc[1]], true
	}
	return "", false
}

// parseCountWord 把「五」/「五条」/「12」解析成数字。
func parseCountWord(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	if n, err := parseDigits(raw); err == nil {
		return n, true
	}
	if len([]rune(raw)) > 1 {
		if n, ok := cnInt(raw); ok {
			return n, true
		}
		return 0, false
	}
	n, ok := cnDigit[raw]
	return n, ok
}

// detectCount 返回段里的数量概括 N，没有则 ok=false。
//
// 1 不是「一组」，12 以上在这类讲义里更可能是年份或数据，都不是列表概括。
func detectCount(text string) (int, bool) {
	m, ok := qtyMatch(text)
	if !ok {
		return 0, false
	}
	raw := leadingCount(m)
	n, ok := parseCountWord(raw)
	if !ok || n < 2 || n > 12 {
		return 0, false
	}
	return n, true
}

// leadingCount 取匹配串开头的数字部分（中文数字连写或阿拉伯数字）。
func leadingCount(m string) string {
	end := 0
	for _, r := range m {
		if strings.ContainsRune("一二两三四五六七八九十", r) || (r >= '0' && r <= '9') {
			end += len(string(r))
			continue
		}
		break
	}
	return m[:end]
}

// hasOrdinalAfter 报告本段之后还有没有「第N步/第N条」。
//
// 有的话，数量词那一段只是引言（「今天讲四个方面」），列表是逐条讲的，
// 这时候把整组绑到引言段是**错绑**——而这正是用户看不出来的那类错误。
func hasOrdinalAfter(segments []Segment, si int) bool {
	for _, s := range segments[si+1:] {
		if ordinalRe.MatchString(s.Text) {
			return true
		}
	}
	return false
}

// markers 返回页内编号标记的下标。
func markers(elements []Element) []int {
	var out []int
	for i, e := range elements {
		if isMarker(e) {
			out = append(out, i)
		}
	}
	return out
}

// cluster 返回一个编号标记在版面上属于哪一块（元素下标）。
//
// 两条路径，覆盖两种真实排法：
//   - **卡片式**（`01 / 标题 / 注释` 叠在一张卡片里）：找最小的无文本容器把它包住，
//     容器里的元素整块算一个。
//   - **行式**（`01` 在左、标题在右，整行一个条目）：取同一 y 带里右侧最近的那个。
func cluster(marker int, elements []Element) []int {
	mark := elements[marker]

	box := -1
	for i, e := range elements {
		if strings.TrimSpace(e.Text) != "" {
			continue
		}
		if area(e) >= 0.4*pageW*pageH {
			continue
		}
		if !contains(e, mark) {
			continue
		}
		if box < 0 || area(e) < area(elements[box]) {
			box = i
		}
	}
	if box >= 0 {
		block := make([]int, 0, 4)
		for i, e := range elements {
			if i == box {
				continue
			}
			if contains(elements[box], e) {
				block = append(block, i)
			}
		}
		if len(block) > 1 {
			return append([]int{box}, block...)
		}
	}

	type cand struct {
		dx  float64
		idx int
	}
	var right []cand
	for i, e := range elements {
		if i == marker || strings.TrimSpace(e.Text) == "" {
			continue
		}
		if overlapY(e, mark) < 0.5 {
			continue
		}
		dx := e.Box.X - (mark.Box.X + mark.Box.W)
		if dx >= 0 && dx <= 420 {
			right = append(right, cand{dx: dx, idx: i})
		}
	}
	sort.SliceStable(right, func(a, b int) bool { return right[a].dx < right[b].dx })
	out := []int{marker}
	if len(right) > 0 {
		out = append(out, right[0].idx)
	}
	return out
}

// countBind 处理「数量词 → 编号组」。
//
// 只在「本段之后没有第N条」时触发（见 hasOrdinalAfter）。两条路径：
//   - 一个元素内部就装着整张列表（`1. … 2. … 3. …` 写在一个形状的文本里）；
//   - 页面上正好有 N 个编号标记，每个标记按 cluster 取自己那一块。
func countBind(elements []Element, segments []Segment, bound boundSet) []pair {
	var out []pair
	for si, seg := range segments {
		n, ok := detectCount(seg.Text)
		if !ok || hasOrdinalAfter(segments, si) {
			continue
		}

		// 路径 A：一个元素里就装了 n 条
		inline := -1
		count := 0
		for i, e := range elements {
			if bound.has(i) || isAux(e) {
				continue
			}
			if len(itemRe.FindAllString(e.Text, -1)) != n {
				continue
			}
			inline, count = i, count+1
		}
		if count == 1 {
			out = append(out, pair{elem: inline, seg: si, source: animation.TriggerSourceCount})
			continue
		}

		// 路径 B：n 个编号标记
		var ms []int
		for _, m := range markers(elements) {
			if !isAux(elements[m]) {
				ms = append(ms, m)
			}
		}
		if len(ms) != n {
			continue
		}
		var block []int
		for _, m := range ms {
			block = append(block, cluster(m, elements)...)
		}
		seen := make(map[int]struct{}, len(block))
		for _, i := range block {
			if bound.has(i) {
				continue
			}
			if _, dup := seen[i]; dup {
				continue
			}
			seen[i] = struct{}{}
			out = append(out, pair{elem: i, seg: si, source: animation.TriggerSourceCount})
		}
	}
	return out
}

// directions 提取段里提到的方位（left/right/top/bottom/center 的子集）。
func directions(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, m := range dirRe.FindAllStringSubmatch(text, -1) {
		out[map[string]string{"左": "left", "右": "right", "上": "top", "下": "bottom"}[m[1]]] = struct{}{}
	}
	for _, w := range centerWords {
		if strings.Contains(text, w) {
			out["center"] = struct{}{}
		}
	}
	return out
}

// inRegion 判断元素是否落在段里提到的方位内。
//
// 左右用 0.5 切：讲义里的「右边那张图」经常从 45% 处开始，用 0.67 会把整栏图表判成中间。
// 上下同理用 0.5，但中间只认 0.30–0.70 / 0.20–0.80。
func inRegion(e Element, dirs map[string]struct{}) bool {
	cx, cy := center(e)
	fx, fy := cx/pageW, cy/pageH
	if _, ok := dirs["left"]; ok && fx >= 0.5 {
		return false
	}
	if _, ok := dirs["right"]; ok && fx <= 0.5 {
		return false
	}
	if _, ok := dirs["top"]; ok && fy >= 0.5 {
		return false
	}
	if _, ok := dirs["bottom"]; ok && fy <= 0.5 {
		return false
	}
	if _, ok := dirs["center"]; ok && !(fx >= 0.30 && fx <= 0.70 && fy >= 0.20 && fy <= 0.80) {
		return false
	}
	return true
}

// spatialBind 处理「方位指代 → 包围盒位置」。
//
// 只在**指向一类视觉对象**时才做：段里说「图/表/截图」，否则「右边第三个优点」
// 这种也会被当成空间指代，绑上的却是文本框。
//
// 精确率优先：候选必须收敛到 1 个。两个都在右边就放弃——宁可漏，不可错。
func spatialBind(elements []Element, segments []Segment, bound boundSet) []pair {
	var out []pair
	for si, seg := range segments {
		dirs := directions(seg.Text)
		wantsTable, wantsImage := false, false
		for _, k := range tableRefs {
			if strings.Contains(seg.Text, k) {
				wantsTable = true
				break
			}
		}
		if !wantsTable {
			for _, k := range imageRefs {
				if strings.Contains(seg.Text, k) {
					wantsImage = true
					break
				}
			}
		}
		if len(dirs) == 0 && !wantsTable && !wantsImage {
			continue
		}

		kinds := map[string]struct{}{"image": {}, "table": {}, "chart": {}, "shape": {}}
		if wantsTable {
			kinds = map[string]struct{}{"table": {}}
		}
		var cands []int
		for i, e := range elements {
			if bound.has(i) || isAux(e) {
				continue
			}
			if _, ok := kinds[e.Kind]; !ok {
				continue
			}
			if len(dirs) > 0 && !inRegion(e, dirs) {
				continue
			}
			cands = append(cands, i)
		}

		// 一个文本框被说成「图」是常见的口语（「右边是部署形态」没说图），
		// 但反过来把「图」绑到一行小字上就是错绑。按类别收敛一次。
		if len(cands) > 1 {
			for _, pref := range []string{"image", "table", "chart", "shape"} {
				var sub []int
				for _, i := range cands {
					if elements[i].Kind == pref {
						sub = append(sub, i)
					}
				}
				if len(sub) == 1 {
					cands = sub
					break
				}
			}
		}
		if len(cands) == 1 {
			out = append(out, pair{elem: cands[0], seg: si, source: animation.TriggerSourceSpatial})
		}
	}
	return out
}

// ordinalBind 处理「第一条 / 第 3 步」→ 页面上第 N 个编号块。
//
// 字面相似度在这里毫无作用（「图和文字一起看」对「双通道理解」是 0），
// 但**序数与编号是对应的**，这层对应不依赖语义。
//
// 严格到什么程度：只认纯数字标记，要求序号在本页编号范围内，且整块都没被别人占。
// 有一条不满足就不绑——序数在口语里也常被虚指（「第一个想法」），宁可漏。
func ordinalBind(elements []Element, segments []Segment, bound boundSet) []pair {
	var out []pair
	for si, seg := range segments {
		m := ordinalRe.FindString(seg.Text)
		if m == "" {
			continue
		}
		n, ok := parseCountWord(leadingCount(strings.TrimPrefix(m, "第")))
		if !ok || n < 1 {
			continue
		}
		var ms []int
		for _, x := range markers(elements) {
			if !isAux(elements[x]) {
				ms = append(ms, x)
			}
		}
		if len(ms) < n {
			continue
		}
		target := ms[n-1]
		if bound.has(target) {
			continue
		}
		part := cluster(target, elements)
		if len(part) == 0 {
			continue
		}
		// 块里已经有文本元素被绑在**别的段**上，说明这一块已经归别人了，别抢。
		// 绑在同一段上则不算冲突——字面匹配常常只够绑上块里的一件。
		conflict := false
		for _, i := range part {
			if si2, ok := bound[i]; ok && strings.TrimSpace(elements[i].Text) != "" && si2 != si {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		for _, i := range part {
			if bound.has(i) {
				continue
			}
			out = append(out, pair{elem: i, seg: si, source: animation.TriggerSourceOrdinal})
		}
	}
	return out
}

// siblingBind 处理「同块带出」：和已绑元素挤在同一块里的东西，跟着它一起进。
//
// 最典型的是四步流程：一列的 `圆形底座 / 图标 / 标题 / 注释` 四件。字面匹配只够绑上
// 「上传文件」这一件（其余是「自动识别页面、备注与异常提示」这种意译），于是同一列里
// 其它三件整块消失——观感上是「点了名才出来一半」。
//
// 判据只有两条，都是几何的：**上下紧邻**（间隙 ≤ 60px，允许负值即轻微重叠）
// 或**互相包含**，且横向投影重合 ≥ 0.6。不判内容，因为这里本来就没有字面对应。
//
// 仍然只认「唯一去向」：旁边同时挨着两个绑在不同段上的元素就放弃。
func siblingBind(elements []Element, pairs []pair) []pair {
	bound := boundOf(pairs)
	var out []pair
	for i, e := range elements {
		if bound.has(i) || isAux(e) {
			continue
		}
		a := area(e)
		if a == 0 || a >= 0.85*pageW*pageH {
			continue
		}
		if a < 0.002*pageW*pageH { // 分隔条、小圆点
			continue
		}
		owners := map[int]struct{}{}
		for bi, si := range bound {
			b := elements[bi]
			if overlapX(b, e) < 0.6 {
				continue
			}
			if contains(b, e) || contains(e, b) {
				owners[si] = struct{}{}
				continue
			}
			gap := max(e.Box.Y-(b.Box.Y+b.Box.H), b.Box.Y-(e.Box.Y+e.Box.H))
			if gap <= 60 {
				owners[si] = struct{}{}
			}
		}
		if len(owners) == 1 {
			for si := range owners {
				out = append(out, pair{elem: i, seg: si, source: animation.TriggerSourceSibling})
			}
		}
	}
	return out
}

// structuralBind 处理「没有文本的元素，跟着它**装着的**内容走」。
//
// 覆盖容器类形状（卡片底、内容区底板）：它们没有文字，但位置就是内容块，
// 讲解带出这一块时应该同时出现。这是**结构推断**，不是文本匹配——
// 报告里单独统计，不能算进文本匹配的命中率。
//
// 要求几何上真的包住一个已绑元素，并排除整页铺底与边角碎块。第一轮这里是
// 「挂到前一个已绑元素」，在 5 页干净样本上没问题；换到真实讲义（每页 20+ 装饰形状）
// 就变成误绑机器：满页的圆形底、色块、分隔条全会被挂上。
func structuralBind(elements []Element, pairs []pair) []pair {
	bound := boundOf(pairs)
	var out []pair
	for i, e := range elements {
		if bound.has(i) || strings.TrimSpace(e.Text) != "" || isAux(e) {
			continue
		}
		a := area(e)
		if a == 0 || a >= 0.85*pageW*pageH { // 整页铺底 / 蒙层
			continue
		}
		if a < 0.002*pageW*pageH { // 分隔条、角标
			continue
		}
		var picks []int
		for k, si := range bound {
			if k == i || !contains(e, elements[k]) {
				continue
			}
			picks = append(picks, si)
		}
		// 标题就在这块的正上方时，卡片底板通常**跟标题一起**进来（讲「本期四件事」
		// 时整块淡入），而不是等第一行被念到——尤其当里面的内容一个都没绑上时，
		// 没有这条它会整块消失。只认 220px 以内的标题，中段卡片上方没有标题。
		for k, si := range bound {
			t := elements[k]
			// 标题本身得是「一段话」而不是横跨整页的整行标题：整行标题的 x 范围
			// 覆盖一切，用它判「正上方」等于不判（第二样本里把四个圆形底座全吸走了）。
			if t.Kind != "title" || t.Box.W >= 0.6*pageW || overlapX(t, e) <= 0.5 {
				continue
			}
			if d := e.Box.Y - (t.Box.Y + t.Box.H); d >= 0 && d <= 220 {
				picks = append(picks, si)
			}
		}
		if len(picks) > 0 {
			out = append(out, pair{elem: i, seg: minInts(picks), source: animation.TriggerSourceStructural})
		}
	}
	return out
}

// titlePrior 把页内第一个 title 元素绑到本页第一段（只在它没绑上时）。
//
// 为什么这条先验成立：讲一页必然从标题讲起，而标题的措辞经常是**概括**而不是重复
// 元素文字（「架构要点」被讲成「五段流水线，每段都有独立的重试」）。纯字面匹配在
// 这种页上一定会漏，而「标题在第一段」这个结构事实不依赖字面。
//
// 只在文本匹配失败时兜底，不覆盖已经绑上的结果——它是个补丁，不是主路径。
func titlePrior(elements []Element, segments []Segment, pairs []pair) []pair {
	if len(segments) == 0 {
		return nil
	}
	bound := make(map[int]struct{}, len(pairs))
	for _, p := range pairs {
		bound[p.elem] = struct{}{}
	}
	for i, e := range elements {
		if e.Kind != "title" {
			continue
		}
		// 只看页内第一个 title：第二个以后的标题不该无条件吸到第一段。
		if _, ok := bound[i]; ok {
			return nil
		}
		return []pair{{elem: i, seg: 0, source: animation.TriggerSourcePrior}}
	}
	return nil
}

func minInts(v []int) int {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
