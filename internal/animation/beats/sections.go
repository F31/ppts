package beats

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/F31/ppts/internal/animation/bind"
)

// sectionTarget 是一页内容结构里的一个分区。
// 只保存「第几块 + 这一块的落点形状」，不保存“板块/部分/章节”这类措辞：
// 这些词是讲稿的写法，不是页面的结构，换一种说法不该重新写一套正则。
type sectionTarget struct {
	Number    int
	ElementID string
	Title     string
}

var (
	// 页面标题的三种常见编号形态。编号必须能被页面结构确认，才作为 section。
	arabicSectionRe  = regexp.MustCompile(`^0*([0-9]{1,2})\s*(?:[.、)）:：]\s*|\s+)(.*)$`)
	chineseSectionRe = regexp.MustCompile(`^([一二两三四五六七八九十]{1,3})\s*[、.．]\s*(.+)$`)
	ordinalSectionRe = regexp.MustCompile(`^第\s*([0-9]{1,2}|[一二两三四五六七八九十]{1,3})\s*(?:[.、:：]\s*|\s+)?(.*)$`)
	sectionMarkerRe  = regexp.MustCompile(`^0*([0-9]{1,2})$`)

	// 旁白里的数字引用只认「第 + 数字」，不关心后面跟的是板块、区域、部分、章节。
	sectionRefRe = regexp.MustCompile(`第\s*([0-9]{1,2}|[一二两三四五六七八九十]{1,3})`)
)

// sectionsFromElements 从页面自己的编号和阅读顺序建立 section index。
//
// 例如报告目录页的标题是「01 大会全景」「02 AI 大模型」……，这里得到
// 1→shape-5、2→shape-8，后续旁白里的「第 4 板块」只需要把 4 对回 shape-14。
// 页面没有编号时返回空，调用方回退到原有拍点切分。
func sectionsFromElements(elements []bind.Element) []sectionTarget {
	seen := make(map[int]sectionTarget, 4)
	for _, e := range elements {
		if bind.IsAux(e) {
			continue
		}
		number, title, ok := sectionHeading(e)
		if !ok {
			continue
		}
		if old, dup := seen[number]; dup {
			// 同一个编号既有独立 marker 又有标题时，标题更适合当焦点落点。
			if old.Title == "" && title != "" {
				seen[number] = sectionTarget{Number: number, ElementID: e.ID, Title: title}
			}
			continue
		}
		seen[number] = sectionTarget{Number: number, ElementID: e.ID, Title: title}
	}
	out := make([]sectionTarget, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func sectionHeading(e bind.Element) (int, string, bool) {
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return 0, "", false
	}
	if e.Kind == "marker" {
		if m := sectionMarkerRe.FindStringSubmatch(text); m != nil {
			n, err := strconv.Atoi(m[1])
			return n, "", err == nil && n > 0 && n <= 12
		}
	}
	if m := ordinalSectionRe.FindStringSubmatch(text); m != nil {
		n, ok := parseNumeral(m[1])
		title := strings.TrimSpace(m[2])
		if ok && n > 0 && n <= 12 && (title != "" || e.Kind == "marker") {
			return n, title, true
		}
	}
	if m := arabicSectionRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.Atoi(m[1])
		title := strings.TrimSpace(m[2])
		if err == nil && n > 0 && n <= 12 && title != "" {
			return n, title, true
		}
	}
	if m := chineseSectionRe.FindStringSubmatch(text); m != nil {
		n, ok := parseNumeral(m[1])
		title := strings.TrimSpace(m[2])
		if ok && n > 0 && n <= 12 && title != "" {
			return n, title, true
		}
	}
	return 0, "", false
}

// sectionRefCuts 把旁白里的数字引用变成切点。
// 只有数字能对上页面 section 时才切；页面没有编号结构时不猜，回退原有拍点切分。
func sectionRefCuts(runes []rune, sections []sectionTarget) []charCut {
	if len(runes) == 0 || len(sections) == 0 {
		return nil
	}
	byNumber := make(map[int]string, len(sections))
	for _, s := range sections {
		if s.ElementID != "" {
			byNumber[s.Number] = s.ElementID
		}
	}
	if len(byNumber) == 0 {
		return nil
	}
	text := string(runes)
	var out []charCut
	for _, loc := range sectionRefRe.FindAllStringSubmatchIndex(text, -1) {
		n, ok := parseNumeral(text[loc[2]:loc[3]])
		if !ok {
			continue
		}
		id, ok := byNumber[n]
		if !ok {
			continue
		}
		at := utf8.RuneCountInString(text[:loc[0]])
		if at <= 0 || at >= len(runes) {
			continue
		}
		out = append(out, charCut{at: at, hard: true, section: n, sectionID: id})
	}
	return out
}

func parseNumeral(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n, true
	}
	return parseChineseNumeral(raw)
}

func parseChineseNumeral(raw string) (int, bool) {
	digits := map[rune]int{
		'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5,
		'六': 6, '七': 7, '八': 8, '九': 9,
	}
	total, current := 0, 0
	for _, r := range raw {
		switch r {
		case '十':
			if current == 0 {
				current = 1
			}
			total += current * 10
			current = 0
		default:
			n, ok := digits[r]
			if !ok {
				return 0, false
			}
			current = n
		}
	}
	total += current
	if total <= 0 {
		return 0, false
	}
	return total, true
}
